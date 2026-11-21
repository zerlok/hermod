package shell

import (
	"context"
	"strconv"
)

// ssh runs the inner command on host over ssh. The command is collapsed into a
// single shell-quoted argument because ssh joins its trailing args with spaces
// and the sandbox login shell re-parses them; a pre-quoted single string survives
// that round-trip intact.
type ssh struct {
	inner    Shell
	host     string
	tty      bool
	noMaster bool
	reverse  []string // extra flags for -R reverse forwards; empty for plain ssh
}

// SSHOption configures an ssh transport at construction. Existing callers pass
// none and are unaffected.
type SSHOption func(*ssh)

func WithTty() SSHOption {
	return func(s *ssh) {
		s.tty = true
	}
}

// WithoutSharedMaster makes the connection its own, never multiplexed over a
// ControlMaster the user's ssh config may set up. A connection that carries its
// own forward needs this: a forward requested through a shared master belongs to
// the master and outlives the connection that asked for it.
func WithoutSharedMaster() SSHOption {
	return func(s *ssh) {
		s.noMaster = true
	}
}

// WithReverseForward forwards remotePort on the sandbox's loopback to localPort
// on this machine's loopback (`-R <remotePort>:127.0.0.1:<localPort>`); repeated
// options accumulate. Deliberately no ExitOnForwardFailure: the forward rides an
// interactive attach, and a port that cannot be bound must cost only what the
// forward carries, not the session. ssh then warns on its stderr and goes on.
func WithReverseForward(remotePort, localPort int) SSHOption {
	return func(s *ssh) {
		s.reverse = append(s.reverse, "-R", strconv.Itoa(remotePort)+":127.0.0.1:"+strconv.Itoa(localPort))
	}
}

// NewSSH wraps inner so its command runs on host over ssh. Set tty to request a
// sandbox pty (-t) for an interactive attach; leave it false for probes. Options
// (e.g. WithReverseForward) tune the transport.
func NewSSH(inner Shell, host string, opts ...SSHOption) Shell {
	s := ssh{inner: inner, host: host}
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
	if s.noMaster {
		argv = append(argv, "-o", "ControlPath=none")
	}
	argv = append(argv, s.reverse...) // empty unless a forward was asked for
	argv = append(argv, s.host, join(cmd.Argv))
	// Dir/Env describe sandbox intent already folded into the argv by inner
	// decorators, so the local ssh process gets neither.
	return s.inner.Run(ctx, cmd.rewrapped(argv))
}
