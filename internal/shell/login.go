package shell

import "context"

// login rewrites a command to run under the remote user's login shell, so it
// inherits the full PATH and profile. Without it a tool installed on that PATH
// (`claude`) is not found and the pane exits at once: tmux runs an explicit
// command in a non-login shell, whereas its session with no command is a login
// shell. A command with no arguments is handed in as it is, since tmux then
// starts the login shell itself.
type login struct{ inner Executor }

// NewLogin wraps inner so a command runs under the remote user's login shell.
// It goes outside the tmux and ssh wrappers, so the rewrite happens before the
// command becomes the tmux session's shell command.
func NewLogin(inner Executor) Executor { return login{inner: inner} }

// Exec hands the command inward as the command that runs it under the user's
// login shell: `sh -c 'exec "$SHELL" -lc "$1"' sh LINE`. $SHELL is resolved
// where it runs, the joined line keeps the command's own word boundaries as
// the single $1, and exec leaves no wrapper process.
func (l login) Exec(ctx context.Context, cmd Command) Execution {
	if len(cmd.Args) == 0 {
		return l.inner.Exec(ctx, cmd)
	}
	wrapped := cmd
	wrapped.Args = []string{"sh", "-c", `exec "$SHELL" -lc "$1"`, "sh", join(cmd.Args)}
	return l.inner.Exec(ctx, wrapped)
}
