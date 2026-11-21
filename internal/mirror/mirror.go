// Package mirror is the file mirror between the local working directory and the
// sandbox. It exposes a Session contract; Mutagen is the (private) implementation.
package mirror

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"path"
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

// Kind is what a mirror carries, which sets its direction and mode.
type Kind int

const (
	// Project is a project's root, carried both ways in two-way-safe mode: a
	// file changed on both sides is a conflict for the developer, and neither
	// side's edit is discarded.
	Project Kind = iota
	// Transcripts is Claude's transcript directory of one working directory,
	// carried from the sandbox to this machine in one-way-safe mode, which never
	// deletes a transcript only this machine has.
	Transcripts
)

func (k Kind) String() string {
	switch k {
	case Project:
		return "project"
	case Transcripts:
		return "transcripts"
	}
	return "unknown"
}

// parseKind reads a kind from its label value.
func parseKind(s string) (Kind, bool) {
	for _, k := range []Kind{Project, Transcripts} {
		if k.String() == s {
			return k, true
		}
	}
	return 0, false
}

// Labels are the Mutagen labels every mirror carries, so a sandbox's mirrors
// are listed by a label selector rather than recorded in a file of Hermod's.
const (
	labelKind    = "hermod-kind"
	labelSandbox = "hermod-sandbox"
)

// Config identifies the mirror endpoints. The mirror's name is derived from the
// host and the sandbox directory it serves, so the same directory mirrored to
// two sandboxes is two mirrors, and for transcripts from the local end too.
type Config struct {
	Kind Kind
	Host string // sandbox host (ssh alias)
	// RemotePath is the mirror's end on the sandbox: a project root,
	// home-relative or absolute, or Claude's transcript directory there.
	RemotePath string
	// LocalPath is the mirror's end on this machine: a project root, or the
	// local transcript directory.
	LocalPath string
	// SessionDir names a Transcripts mirror: the sandbox directory whose
	// transcripts it carries. A Project mirror is named after its RemotePath.
	SessionDir string
	// Output receives what the mirror's own commands print (`mutagen sync create`
	// reports the session it made); nil is the process's stdout.
	Output io.Writer
}

// namePrefix starts every mirror name Hermod gives, so its mirrors are told
// apart from ones made under earlier names or by hand.
const namePrefix = "hermod-"

// transcriptsInfix follows the session directory's slug in a Transcripts
// mirror's name.
const transcriptsInfix = "-transcripts-"

// localTagLen is how many hex digits of its local end's hash a Transcripts
// mirror's name carries.
const localTagLen = 8

// Name is the mirror's Mutagen name: `hermod-<sandbox>-<slug>` for a project,
// where the slug is the remote path with its separators as dashes, and
// `hermod-<sandbox>-<slug>-transcripts-<local>` for the transcripts of the
// session directory the slug is made from. Mutagen accepts only letters, digits
// and dashes in a name, so every other character becomes a dash.
//
// <local> is the start of a hash of the local end. One sandbox directory's
// transcripts go to as many local Claude config directories as there are
// callers that keep one, such as ~/.claude for a terminal and an Air entry's
// own directory for its chats, and each needs its own mirror. It also keeps a
// project whose path ends in `-transcripts` from taking a Transcripts mirror's
// name.
func Name(cfg Config) string {
	if cfg.Kind == Transcripts {
		return transcriptsPrefix(cfg.Host, cfg.SessionDir) + localTag(cfg.LocalPath)
	}
	return namePrefix + nameSafe(cfg.Host) + "-" + slug(cfg.RemotePath)
}

// transcriptsPrefix starts the name of every Transcripts mirror of a session
// directory, whatever its local end.
func transcriptsPrefix(host, sessionDir string) string {
	return namePrefix + nameSafe(host) + "-" + slug(sessionDir) + transcriptsInfix
}

func localTag(local string) string {
	sum := sha256.Sum256([]byte(path.Clean(local)))
	return hex.EncodeToString(sum[:])[:localTagLen]
}

// isLocalTag reports whether s is a local end's tag as Name writes it.
func isLocalTag(s string) bool {
	if len(s) != localTagLen {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func slug(p string) string { return nameSafe(strings.ReplaceAll(p, "/", "-")) }

func nameSafe(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			return r
		default:
			return '-'
		}
	}, s)
}

// maxLabelValue is the longest label value Mutagen accepts.
const maxLabelValue = 63

// sandboxLabel is the sandbox as a label value: the alias reduced as in a
// name, without the leading or trailing dashes and the length Mutagen rejects
// in a label value.
func sandboxLabel(host string) string {
	v := strings.Trim(nameSafe(host), "-")
	if len(v) > maxLabelValue {
		v = strings.TrimRight(v[:maxLabelValue], "-")
	}
	return v
}

// Selector is the Mutagen label selector of a sandbox's mirrors.
func Selector(host string) string { return labelSandbox + "=" + sandboxLabel(host) }

// EndpointMismatchError reports a mirror found under the expected name that
// syncs to another sandbox endpoint. Resuming it would send the files somewhere
// other than where the session runs, so the open stops instead. The found mirror
// may be another endpoint's working mirror, since two aliases or two paths can
// reduce to one name (`box.a` and `box-a`), so the error does not suggest
// removing it.
type EndpointMismatchError struct {
	Name string // the mirror's name
	Want string // the endpoint this run mirrors to
	Got  string // the endpoint the found mirror syncs to
}

func (e *EndpointMismatchError) Error() string {
	return fmt.Sprintf("mirror %s syncs to %s, not %s: another mirror already has this name, and Hermod leaves it alone",
		e.Name, e.Got, e.Want)
}

// mutagen is the Mutagen-backed Session. Mutagen is invoked as its real CLI;
// hermod composes it rather than reimplementing it.
type mutagen struct {
	name     string
	cfg      Config
	endpoint string // the sandbox end as the client understands it, e.g. "prod-box:dev/api"
	output   io.Writer

	// exec runs mutating commands (dry-run-aware); probe runs the read-only
	// status query (always real).
	exec  shell.Shell
	probe shell.Shell
}

// NewMutagenSession opens the mirror as create-or-resume — an existing session
// (paused or running) to the same endpoint is resumed so its synced state is
// reused, otherwise a new one is created, labelled with its kind and sandbox —
// and returns it ready for use. Mutations run on exec (dry-run-aware); probes
// run on probe (always real).
//
// When it creates a project mirror, which is the first run under its name, it
// also returns the names of the other Mutagen sessions that sync the same local
// directory under names Hermod does not give, such as mirrors made under its
// earlier names or by hand. They are reported and never touched here.
func NewMutagenSession(ctx context.Context, exec, probe shell.Shell, cfg Config) (s Session, earlier []string, err error) {
	m := &mutagen{
		name:     Name(cfg),
		cfg:      cfg,
		endpoint: cfg.Host + ":" + cfg.RemotePath,
		output:   cfg.Output,
		exec:     exec,
		probe:    probe,
	}
	found, err := m.find(ctx)
	if err != nil {
		return nil, nil, err
	}
	if found != nil {
		if got := found.remote(cfg.Kind); got != m.endpoint {
			return nil, nil, &EndpointMismatchError{Name: m.name, Want: m.endpoint, Got: got}
		}
		if err := m.verb(ctx, "resume"); err != nil {
			return nil, nil, err
		}
		return m, nil, nil
	}
	if cfg.Kind == Project {
		earlier = m.earlier(ctx)
	}
	if err := m.create(ctx); err != nil {
		return nil, nil, err
	}
	return m, earlier, nil
}

// create makes the mirror's roots and the Mutagen session between them.
// Mutagen creates a sync root but not its parent directories, and a root with an
// absent parent stalls the sync, so each end is made first: the sandbox's for
// either kind, and the local one for transcripts, whose local end lies in a
// config directory that may not hold `projects/` yet. A transcript directory
// made on the sandbox also gives the one-way mirror a source that exists before
// Claude writes its first transcript there.
func (m *mutagen) create(ctx context.Context) error {
	// Through shell's ssh, which quotes the path for the sandbox's shell: ssh
	// joins its arguments for that shell to parse again.
	if err := m.runOn(ctx, shell.NewSSH(m.exec, m.cfg.Host), "mkdir", "-p", "--", m.cfg.RemotePath); err != nil {
		return err
	}
	argv := []string{"mutagen", "sync", "create", "--name", m.name,
		"--label", labelKind + "=" + m.cfg.Kind.String(),
		"--label", Selector(m.cfg.Host)}
	switch m.cfg.Kind {
	case Project:
		argv = append(argv, "--mode", "two-way-safe", m.cfg.LocalPath, m.endpoint)
	case Transcripts:
		if err := m.run(ctx, "mkdir", "-p", m.cfg.LocalPath); err != nil {
			return err
		}
		// Mutagen's one-way modes carry alpha to beta: the sandbox is alpha.
		argv = append(argv, "--mode", "one-way-safe", m.endpoint, m.cfg.LocalPath)
	default:
		return fmt.Errorf("mirror %s: unhandled kind %v", m.name, m.cfg.Kind)
	}
	return m.run(ctx, argv...)
}

func (m *mutagen) Flush(ctx context.Context) error { return m.verb(ctx, "flush") }
func (m *mutagen) Pause(ctx context.Context) error { return m.verb(ctx, "pause") }
func (m *mutagen) Close(ctx context.Context) error { return m.verb(ctx, "terminate") }

func (m *mutagen) verb(ctx context.Context, verb string) error {
	return m.run(ctx, "mutagen", "sync", verb, m.name)
}

// run runs one mutating command on exec, its output going to the mirror's
// Output. It reads no input: this process's stdin belongs to what Hermod
// attaches, and the mirror's own ssh would otherwise consume it.
func (m *mutagen) run(ctx context.Context, argv ...string) error {
	return m.runOn(ctx, m.exec, argv...)
}

// runOn is run on another transport over exec, such as ssh to the sandbox.
func (m *mutagen) runOn(ctx context.Context, sh shell.Shell, argv ...string) error {
	_, err := sh.Run(ctx, shell.Command{Argv: argv, Stdin: shell.NoInput, Stdout: m.output})
	return err
}

// notFoundMarker is what `mutagen sync list <name>` reports when no session
// matches the name. It is the positive signal that a session is Absent, read
// from the probe's own output rather than inferred from the exit status.
const notFoundMarker = "unable to locate requested sessions"

// Status reports the current sync state without mutating it.
func (m *mutagen) Status(ctx context.Context) (State, error) {
	found, err := m.find(ctx)
	switch {
	case err != nil:
		return Unknown, err
	case found == nil:
		return Absent, nil
	case found.paused:
		return Paused, nil
	default:
		return Running, nil
	}
}

// find lists the session under the mirror's name, nil when Mutagen reports none.
// Absence is read from the probe's own output: `mutagen sync list <name>` reports
// the not-found marker when no such session exists. Any other failure is
// surfaced, so an ambiguous probe aborts opening rather than creating a new
// session over an existing one.
func (m *mutagen) find(ctx context.Context) (*listed, error) {
	res, err := m.probe.Run(ctx, shell.Command{
		Argv:    []string{"mutagen", "sync", "list", m.name},
		Capture: true,
	})
	if strings.Contains(res.Stdout, notFoundMarker) || strings.Contains(res.Stderr, notFoundMarker) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	for _, s := range parseList(res.Stdout) {
		if s.name == m.name {
			return &s, nil
		}
	}
	// A successful listing of the name always holds that session; one that does
	// not is output this parser does not understand, which is not proof of absence.
	return nil, fmt.Errorf("mutagen sync list %s: session not found in its output", m.name)
}

// earlier lists the sessions syncing this local directory under names Hermod
// does not give. The listing is advisory, so a failed probe lists nothing.
func (m *mutagen) earlier(ctx context.Context) []string {
	res, err := m.probe.Run(ctx, shell.Command{
		Argv:    []string{"mutagen", "sync", "list"},
		Capture: true,
	})
	if err != nil {
		return nil
	}
	var names []string
	for _, s := range parseList(res.Stdout) {
		if s.alpha == m.cfg.LocalPath && !strings.HasPrefix(s.name, namePrefix) {
			names = append(names, s.name)
		}
	}
	return names
}

// listed is one session as `mutagen sync list` prints it.
type listed struct {
	name   string
	alpha  string // alpha endpoint URL
	beta   string // beta endpoint URL
	paused bool
	labels map[string]string // nil when it has none
}

// remote is the URL of the session's sandbox end for a mirror of the kind.
func (l listed) remote(k Kind) string {
	if k == Transcripts {
		return l.alpha
	}
	return l.beta
}

// local is the URL of the session's local end for a mirror of the kind.
func (l listed) local(k Kind) string {
	if k == Transcripts {
		return l.beta
	}
	return l.alpha
}

// parseList reads the sessions from `mutagen sync list` output. Each session
// starts at a top-level `Name:` line; its endpoints are the indented `URL:`
// lines under the top-level `Alpha:` and `Beta:` headers, its labels the
// indented `key: value` lines under a `Labels:` header, and a paused session
// says so in its status.
func parseList(out string) []listed {
	var (
		sessions []listed
		section  string
	)
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		if line != "" && line[0] != ' ' && line[0] != '\t' {
			section = ""
			switch {
			case strings.HasPrefix(trimmed, "Name:"):
				sessions = append(sessions, listed{name: strings.TrimSpace(strings.TrimPrefix(trimmed, "Name:"))})
				continue
			case trimmed == "Alpha:" || trimmed == "Beta:" || trimmed == "Labels:":
				section = trimmed
				continue
			}
		}
		if len(sessions) == 0 {
			continue
		}
		cur := &sessions[len(sessions)-1]
		if section == "Labels:" {
			if key, value, ok := strings.Cut(trimmed, ": "); ok {
				if cur.labels == nil {
					cur.labels = map[string]string{}
				}
				cur.labels[key] = value
			}
			continue
		}
		url, isURL := strings.CutPrefix(trimmed, "URL:")
		switch {
		case isURL && section == "Alpha:":
			cur.alpha = strings.TrimSpace(url)
		case isURL && section == "Beta:":
			cur.beta = strings.TrimSpace(url)
		case !isURL && strings.Contains(trimmed, "Paused"):
			cur.paused = true
		}
	}
	return sessions
}
