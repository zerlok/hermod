// Package sandbox is the interactive session on the sandbox: a persistent tmux
// window reached over SSH. Constructing a session prepares the transport but
// does not attach; Attach joins it and IsActive probes its liveness.
package sandbox

import (
	"context"

	"github.com/zerlok/hermod/internal/execx"
	"github.com/zerlok/hermod/internal/shell"
)

// Session is a prepared sandbox session.
type Session interface {
	// Attach joins or creates the tmux window and blocks until the user detaches.
	Attach(ctx context.Context) error
	// IsActive reports whether the tmux session still exists on the host.
	IsActive(ctx context.Context) (bool, error)
}

// Config describes the session to prepare.
type Config struct {
	Host    string
	Session string
	Dir     string   // sandbox working directory
	Command []string // passthrough command; nil runs the default shell
	Env     []string // git identity carried into the session
}

// sandbox is a tmux-over-ssh Session. It wraps the given inner (leaf) shell with
// the ssh and tmux transports it needs: an interactive stack for the attach and
// a plain ssh stack for the liveness probe.
type sandbox struct {
	session     string
	dir         string
	command     []string
	env         []string
	interactive shell.Shell
	probe       shell.Shell
}

// NewSession prepares a session on cfg.Host, building the ssh + tmux stacks over
// the factory's Effective leaf (the attach is a side effect, and the liveness
// probe follows a real attach). It does not attach.
func NewSession(factory shell.Factory, cfg Config) Session {
	leaf := factory.Effective()
	return &sandbox{
		session:     cfg.Session,
		dir:         cfg.Dir,
		command:     cfg.Command,
		env:         cfg.Env,
		interactive: shell.NewTmux(shell.NewSSH(leaf, cfg.Host, true), cfg.Session),
		probe:       shell.NewSSH(leaf, cfg.Host, false),
	}
}

func (s *sandbox) Attach(ctx context.Context) error {
	_, err := s.interactive.Run(ctx, execx.Command{
		Argv: s.command,
		Dir:  s.dir,
		Env:  s.env,
		// Capture stays false: the child inherits stdio for an interactive attach.
	})
	return err
}

// IsActive is read-only. `tmux has-session` exits 1 when the session is gone,
// which we read as not-active; any other failure (e.g. an ssh connection error)
// is ambiguous and returned so the caller can choose a safe default.
func (s *sandbox) IsActive(ctx context.Context) (bool, error) {
	_, err := s.probe.Run(ctx, execx.Command{
		Argv:    []string{"tmux", "has-session", "-t", s.session},
		Capture: true,
	})
	if err == nil {
		return true, nil
	}
	if code, ok := execx.ExitCode(err); ok && code == 1 {
		return false, nil
	}
	return false, err
}
