package shell

import "context"

// tmux wraps a command as a persistent tmux window, attaching to the named
// session if it exists and creating it otherwise (`new-session -A`). The start
// directory (cmd.Dir) and environment (cmd.Env) are applied via tmux's own -c
// and -e flags, which take effect only when the session is created. An empty
// inner argv means "no command": tmux spawns the default shell.
type tmux struct {
	inner   Shell
	session string
}

// NewTmux wraps inner so its command runs in the named tmux session.
func NewTmux(inner Shell, session string) Shell {
	return tmux{inner: inner, session: session}
}

func (t tmux) Run(ctx context.Context, cmd Command) (Result, error) {
	argv := []string{"tmux", "new-session", "-A", "-s", t.session}
	if cmd.Dir != "" {
		argv = append(argv, "-c", cmd.Dir)
	}
	for _, e := range cmd.Env {
		argv = append(argv, "-e", e)
	}
	if len(cmd.Argv) > 0 {
		// Pass the sandbox command as one string so tmux runs it via the shell
		// and does not mistake the command's own flags for tmux options.
		argv = append(argv, join(cmd.Argv))
	}
	// Dir/Env are consumed into the tmux flags above; a fresh Command carries
	// only the rewritten argv (and the preserved Capture flag) inward.
	return t.inner.Run(ctx, Command{Argv: argv, Capture: cmd.Capture})
}
