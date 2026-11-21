// Package sandbox is the interactive session on the sandbox: a persistent tmux
// window reached over SSH. Constructing a session prepares the transport but
// does not attach; Attach joins it. An agent
// session (agent.go) is a tmux session keyed by Claude's session UUID, with a
// directory of its own on the sandbox. Every shell line run there is written
// here.
package sandbox

import (
	"context"
	"io"
	"strings"

	"github.com/zerlok/hermod/internal/shell"
)

// Session is a prepared sandbox session.
type Session interface {
	// Attach joins or creates the tmux window and blocks until the user detaches.
	Attach(ctx context.Context) error
}

// Command is what a new session runs. The zero value runs the sandbox user's
// default shell; Exec runs an argv with its word boundaries kept; ShellLine runs
// a command line that the sandbox user's shell parses itself. The two forms are
// kept apart because quoting a line as one argv word would run a program named
// after the whole line.
type Command struct {
	argv []string
	line string
}

// Exec is a command given as an argv. An empty argv is the default shell.
func Exec(argv []string) Command {
	if len(argv) == 0 {
		return Command{}
	}
	return Command{argv: append([]string(nil), argv...)}
}

// ShellLine is a command given as one line for the sandbox user's shell to
// parse. A blank line is the default shell: run as a line it would exit at once
// and end the session on creation.
func ShellLine(line string) Command {
	if strings.TrimSpace(line) == "" {
		return Command{}
	}
	return Command{line: line}
}

// Argv is the command given as an argv, program first; it is nil for a command
// line and for the default shell, whose words only the sandbox's shell knows.
func (c Command) Argv() []string {
	return append([]string(nil), c.argv...)
}

// loginArgv is the argv handed to the login-shell decorator, which joins it into
// the single string its `$SHELL -lc` runs. A line is wrapped in `eval` so that
// shell parses the line's own words instead of taking the line as one word.
func (c Command) loginArgv() []string {
	if c.line != "" {
		return []string{"eval", c.line}
	}
	return c.argv
}

// Config describes the session to prepare.
type Config struct {
	Host    string
	Session string
	Dir     string   // sandbox working directory
	Command Command  // what a new session runs; the zero value runs the default shell
	Env     []string // git identity carried into the session
	Tags    Tags     // where a new session came from
}

// sandbox is a tmux-over-ssh Session. It wraps the given inner (leaf) shell with
// the ssh and tmux transports the attach needs.
type sandbox struct {
	session     string
	dir         string
	command     Command
	env         []string
	interactive shell.Shell
}

// NewSession prepares a session on cfg.Host, building the ssh + tmux stacks over
// inner (the effective, dry-run-aware shell: the attach is a side effect). It
// does not attach.
func NewSession(inner shell.Shell, cfg Config) Session {
	return &sandbox{
		session: cfg.Session,
		dir:     cfg.Dir,
		command: cfg.Command,
		env:     cfg.Env,
		interactive: shell.NewLoginShell(shell.NewTmux(shell.NewSSH(inner, cfg.Host, shell.WithTty()), cfg.Session,
			shell.WithSessionOptions(cfg.Tags.options()...))),
	}
}

func (s *sandbox) Attach(ctx context.Context) error {
	_, err := s.interactive.Run(ctx, shell.Command{
		Argv: s.command.loginArgv(),
		Dir:  s.dir,
		Env:  s.env,
		// Capture stays false: the child inherits stdio for an interactive attach.
	})
	return err
}

// Streams are the standard streams a command on the sandbox runs with. A nil
// stream is this process's own.
type Streams struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer
}

// Run runs argv on host under the sandbox user's login shell, so a tool on the
// login PATH is found, and returns when it exits. No tmux session holds it: it
// ends with the connection. The error carries the command's exit code when it
// ran and failed (see shell.ExitCode).
//
// dir, home-relative or absolute, is the working directory when it exists on the
// sandbox, so the command sees the project's own settings; when it does not
// (the project is not mirrored yet) or is "", the command runs in the sandbox
// user's home rather than failing.
func Run(ctx context.Context, inner shell.Shell, host, dir string, argv []string, streams Streams) error {
	if dir != "" {
		argv = append([]string{"sh", "-c", `cd -- "$1" 2>/dev/null; shift; exec "$@"`, "sh", dir}, argv...)
	}
	_, err := shell.NewLoginShell(shell.NewSSH(inner, host)).Run(ctx, shell.Command{
		Argv:   argv,
		Stdin:  streams.In,
		Stdout: streams.Out,
		Stderr: streams.Err,
	})
	return err
}
