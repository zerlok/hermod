package shell

import (
	"context"

	"github.com/zerlok/hermod/internal/execx"
)

// ssh runs the inner command on host over ssh. The command is collapsed into a
// single shell-quoted argument because ssh joins its trailing args with spaces
// and the sandbox login shell re-parses them; a pre-quoted single string survives
// that round-trip intact.
type ssh struct {
	inner Shell
	host  string
	tty   bool
}

// NewSSH wraps inner so its command runs on host over ssh. Set tty to request a
// sandbox pty (-t) for an interactive attach; leave it false for probes.
func NewSSH(inner Shell, host string, tty bool) Shell {
	return ssh{inner: inner, host: host, tty: tty}
}

func (s ssh) Run(ctx context.Context, cmd execx.Command) (execx.Result, error) {
	argv := []string{"ssh"}
	if s.tty {
		argv = append(argv, "-t")
	}
	argv = append(argv, s.host, execx.Join(cmd.Argv))
	// Dir/Env describe sandbox intent already folded into the argv by inner
	// decorators, so the local ssh process gets neither; Capture is preserved
	// for probe commands. A fresh Command is returned rather than mutating cmd.
	return s.inner.Run(ctx, execx.Command{Argv: argv, Capture: cmd.Capture})
}
