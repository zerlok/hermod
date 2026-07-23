// Package mirror is the file mirror between the local working directory and the
// sandbox. It exposes a Session contract; Mutagen is the (private) implementation.
package mirror

import (
	"context"
	"strings"

	"github.com/zerlok/hermod/internal/shell"
)

// State is the read-only lifecycle state of a mirror.
type State int

const (
	// Unknown means the state could not be determined — the probe failed for a
	// reason other than the session being absent. It is the zero value, so a
	// State is never taken for Absent without a positive not-found signal.
	Unknown State = iota
	// Absent means no session exists for the name.
	Absent
	// Paused means the session exists but synchronization is suspended.
	Paused
	// Running means the session exists and is synchronizing.
	Running
)

// Session is an opened file mirror. Opening (create-or-resume) happens at
// construction, so a Session is always live; the methods drive the rest of its
// lifecycle.
type Session interface {
	// Flush blocks until the local and sandbox trees are consistent.
	Flush(ctx context.Context) error
	// Pause suspends synchronization while preserving the session for resume.
	Pause(ctx context.Context) error
	// Close terminates and removes the session.
	Close(ctx context.Context) error
	// Status reports the current state without mutating it.
	Status(ctx context.Context) (State, error)
}

// Config identifies the mirror endpoints.
type Config struct {
	Name       string // session name, shared with tmux
	Host       string // sandbox host (ssh alias)
	RemotePath string // path on the sandbox, relative to its home
	LocalPath  string // local directory to mirror
}

// mutagen is the Mutagen-backed Session. Mutagen is invoked as its real CLI;
// hermod composes it rather than reimplementing it.
type mutagen struct {
	name      string
	localPath string
	endpoint  string // beta endpoint the client understands, e.g. "prod-box:dev/api"

	// exec runs mutating commands (dry-run-aware); probe runs the read-only
	// status query (always real).
	exec  shell.Shell
	probe shell.Shell
}

// NewMutagenSession opens the mirror as create-or-resume — an existing session
// (paused or running) is resumed so its synced state is reused, otherwise a new
// bidirectional session is created — and returns it ready for use. Mutations run
// on exec (dry-run-aware); the status probe runs on probe (always real).
func NewMutagenSession(ctx context.Context, exec, probe shell.Shell, cfg Config) (Session, error) {
	m := &mutagen{
		name:      cfg.Name,
		localPath: cfg.LocalPath,
		endpoint:  cfg.Host + ":" + cfg.RemotePath,
		exec:      exec,
		probe:     probe,
	}
	st, err := m.Status(ctx)
	if err != nil {
		return nil, err
	}
	if st != Absent {
		if err := m.verb(ctx, "resume"); err != nil {
			return nil, err
		}
		return m, nil
	}
	// Mutagen creates the sync root but not its parent directories; ensure the
	// remote root (and any missing parents) exists before creating the session,
	// otherwise a home-relative remote path with an absent parent stalls the sync.
	if _, err := m.exec.Run(ctx, shell.Command{
		Argv: []string{"ssh", cfg.Host, "mkdir", "-p", cfg.RemotePath},
	}); err != nil {
		return nil, err
	}
	_, err = m.exec.Run(ctx, shell.Command{
		Argv: []string{"mutagen", "sync", "create", "--name", m.name, m.localPath, m.endpoint},
	})
	if err != nil {
		return nil, err
	}
	return m, nil
}

func (m *mutagen) Flush(ctx context.Context) error { return m.verb(ctx, "flush") }
func (m *mutagen) Pause(ctx context.Context) error { return m.verb(ctx, "pause") }
func (m *mutagen) Close(ctx context.Context) error { return m.verb(ctx, "terminate") }

func (m *mutagen) verb(ctx context.Context, verb string) error {
	_, err := m.exec.Run(ctx, shell.Command{
		Argv: []string{"mutagen", "sync", verb, m.name},
	})
	return err
}

// notFoundMarker is what `mutagen sync list <name>` reports when no session
// matches the name. It is the positive signal that a session is Absent, read
// from the probe's own output rather than inferred from the exit status.
const notFoundMarker = "unable to locate requested sessions"

// Status reports the current sync state without mutating it. Absence is read
// from the probe's own output: `mutagen sync list <name>` reports the not-found
// marker when no such session exists, which we read as Absent so the caller can
// create it. Any other failure is surfaced, so an ambiguous probe aborts opening
// rather than creating a new session over an existing one.
func (m *mutagen) Status(ctx context.Context) (State, error) {
	res, err := m.probe.Run(ctx, shell.Command{
		Argv:    []string{"mutagen", "sync", "list", m.name},
		Capture: true,
	})
	if strings.Contains(res.Stdout, notFoundMarker) || strings.Contains(res.Stderr, notFoundMarker) {
		return Absent, nil
	}
	if err != nil {
		// A failure that is not the not-found marker is ambiguous (daemon down /
		// transport): the state is Unknown, not Absent — never let it be read as
		// "no session" and trigger a create over an existing one.
		return Unknown, err
	}
	if strings.Contains(res.Stdout, "Paused") {
		return Paused, nil
	}
	return Running, nil
}
