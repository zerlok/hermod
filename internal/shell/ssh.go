package shell

import "context"

// ssh runs the inner command on host over ssh. The command is collapsed into a
// single shell-quoted argument because ssh joins its trailing args with spaces
// and the sandbox login shell re-parses them; a pre-quoted single string survives
// that round-trip intact.
type ssh struct {
	inner   Shell
	host    string
	tty     bool
	reverse []string // extra flags for -R reverse forwards; empty for plain ssh
}

// SSHOption configures an ssh transport at construction. Existing callers pass
// none and are unaffected.
type SSHOption func(*ssh)

// WithReverseForward adds an `ssh -R <spec>` reverse forward (remote → local),
// with the StreamLocalBind hardening for a unix-socket endpoint: unlink a stale
// socket and bind it 0600 (owner only). spec is "<remoteSock>:<localSock>";
// repeated options accumulate. Deliberately no ExitOnForwardFailure — this rides
// the shared interactive attach ssh, so a bind failure must not kill the session
// (that flag is safe only on a dedicated, disposable `ssh -N` tunnel).
func WithReverseForward(spec string) SSHOption {
	return func(s *ssh) {
		s.reverse = append(s.reverse,
			"-R", spec,
			"-o", "StreamLocalBindUnlink=yes",
			"-o", "StreamLocalBindMask=0177")
	}
}

// NewSSH wraps inner so its command runs on host over ssh. Set tty to request a
// sandbox pty (-t) for an interactive attach; leave it false for probes. Options
// (e.g. WithReverseForward) tune the transport.
func NewSSH(inner Shell, host string, tty bool, opts ...SSHOption) Shell {
	s := ssh{inner: inner, host: host, tty: tty}
	for _, o := range opts {
		o(&s)
	}
	return s
}

func (s ssh) Run(ctx context.Context, cmd Command) (Result, error) {
	argv := []string{"ssh"}
	if s.tty {
		argv = append(argv, "-t")
	}
	argv = append(argv, s.reverse...) // nothing for probes / non-notify attaches
	argv = append(argv, s.host, join(cmd.Argv))
	// Dir/Env describe sandbox intent already folded into the argv by inner
	// decorators, so the local ssh process gets neither; Capture is preserved
	// for probe commands. A fresh Command is returned rather than mutating cmd.
	return s.inner.Run(ctx, Command{Argv: argv, Capture: cmd.Capture})
}
