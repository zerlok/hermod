package shell

import (
	"context"
	"strings"
)

// tmux wraps a command as a persistent tmux window, attaching to the named
// session if it exists and creating it otherwise (`new-session -A`). The start
// directory (cmd.Dir) and environment (cmd.Env) are applied via tmux's own -c
// and -e flags, which take effect only when the session is created. An empty
// inner argv means "no command": tmux spawns the default shell.
type tmux struct {
	inner    Shell
	session  string
	detached bool
	options  []SessionOption
}

// SessionOption is a tmux session option, such as a user option `@name`, set on
// the session when it is created.
type SessionOption struct {
	Name  string
	Value string
}

// TmuxOption configures a tmux transport at construction.
type TmuxOption func(*tmux)

// Detached creates the session without attaching to it (`new-session -d`), for
// a session started by one connection and joined by another. A session that
// already exists is left as it is, its command not run again.
func Detached() TmuxOption {
	return func(t *tmux) { t.detached = true }
}

// WithSessionOptions sets options on the session. Each is set only where it is
// not set yet (`set-option -qo`), so the session keeps the values it was created
// with when a later attach joins it.
func WithSessionOptions(opts ...SessionOption) TmuxOption {
	return func(t *tmux) { t.options = append(t.options, opts...) }
}

// NewTmux wraps inner so its command runs in the named tmux session.
func NewTmux(inner Shell, session string, opts ...TmuxOption) Shell {
	t := tmux{inner: inner, session: session}
	for _, o := range opts {
		o(&t)
	}
	return t
}

func (t tmux) Run(ctx context.Context, cmd Command) (Result, error) {
	argv := []string{"tmux", "new-session"}
	if t.detached {
		argv = append(argv, "-d")
	} else {
		argv = append(argv, "-A")
	}
	argv = append(argv, "-s", t.session)
	if cmd.Dir != "" {
		argv = append(argv, "-c", formatLiteral(cmd.Dir))
	}
	for _, e := range cmd.Env {
		argv = append(argv, "-e", e)
	}
	if len(cmd.Argv) > 0 {
		// Pass the sandbox command as one string so tmux runs it via the shell
		// and does not mistake the command's own flags for tmux options.
		argv = append(argv, join(cmd.Argv))
	}
	// A lone ";" word separates tmux commands. Each option names its session
	// (see target): without one, tmux sets it on the session of the client's
	// $TMUX, when there is one, rather than on the session just created. -q keeps
	// an option that is already set from failing the sequence and showing an
	// error in the client.
	for _, o := range t.options {
		argv = append(argv, ";", "set-option", "-t", target(t.session), "-qo", o.Name, o.Value)
	}
	// Dir/Env are consumed into the tmux flags above.
	return t.inner.Run(ctx, cmd.rewrapped(argv))
}

// formatLiteral is s as a tmux format that expands to s itself. tmux expands
// -c as a format, where `#(cmd)` runs cmd and other `#` sequences are
// replaced, so a directory name holding a '#' would otherwise not be the
// directory the session starts in, or would run a command; `##` is a literal
// '#'.
func formatLiteral(s string) string { return strings.ReplaceAll(s, "#", "##") }

// target names a session exactly (`=name:`): tmux stores a session name with
// '.' and ':' replaced by '_', and without '=' a name also matches any session
// it is the prefix of.
func target(session string) string {
	return "=" + strings.NewReplacer(".", "_", ":", "_").Replace(session) + ":"
}
