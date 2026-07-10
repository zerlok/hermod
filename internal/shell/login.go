package shell

import "context"

// loginShell rewrites a command to run under the sandbox user's login shell, so
// it inherits the full PATH and profile. Without this a PATH-installed tool
// (e.g. `claude`) is not found and the pane exits at once: tmux runs a bare
// passthrough in a non-login shell, whereas its default empty-command session is
// a login shell — which is why an empty command works and an explicit one does
// not. An empty command is passed through untouched (tmux already spawns the
// login shell itself).
type loginShell struct{ inner Shell }

// NewLoginShell wraps inner so a non-empty command runs under the sandbox user's
// login shell. Compose it outside the tmux/ssh transports so the rewrite happens
// before the command is embedded as the tmux window's shell-command.
func NewLoginShell(inner Shell) Shell { return loginShell{inner: inner} }

func (l loginShell) Run(ctx context.Context, cmd Command) (Result, error) {
	if len(cmd.Argv) == 0 {
		return l.inner.Run(ctx, cmd)
	}
	// `exec "$SHELL" -lc "$1"` runs the joined command line in a login shell,
	// resolving $SHELL on the sandbox (not locally) and preserving the command's
	// own word boundaries because join() re-quotes it into the single $1 string.
	// The literal "sh" is $0; the joined command is $1. exec avoids a lingering
	// wrapper process.
	argv := []string{"sh", "-c", `exec "$SHELL" -lc "$1"`, "sh", join(cmd.Argv)}
	return l.inner.Run(ctx, Command{Argv: argv, Dir: cmd.Dir, Env: cmd.Env, Capture: cmd.Capture})
}
