package shell

import (
	"context"
	"strconv"
)

// ssh runs the inner command on host over ssh. The command is collapsed into a
// single shell-quoted argument because ssh joins its trailing arguments with
// spaces and the remote's shell parses them again; a quoted single string
// survives that round trip intact.
type ssh struct {
	inner    Executor
	host     string
	tty      bool
	noMaster bool
	failFast bool
	reverse  []string // extra flags for -R reverse forwards; empty for plain ssh
}

// SSHOption configures an ssh wrapper at construction.
type SSHOption func(*ssh)

// WithTty requests a pty on the remote (-t), for a command a terminal attaches
// to.
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

// WithExitOnForwardFailure makes ssh exit when a forward cannot be set up,
// for a connection that exists only to carry its forwards: without it ssh warns
// and goes on, and the connection would carry nothing.
func WithExitOnForwardFailure() SSHOption {
	return func(s *ssh) {
		s.failFast = true
	}
}

// WithReverseForward forwards remotePort on the remote's loopback to localPort
// on this machine's loopback (`-R <remotePort>:127.0.0.1:<localPort>`); repeated
// options accumulate. A forward that cannot be bound costs only what it carries:
// ssh warns on its stderr and goes on, unless WithExitOnForwardFailure is given.
func WithReverseForward(remotePort, localPort int) SSHOption {
	return func(s *ssh) {
		s.reverse = append(s.reverse, "-R", strconv.Itoa(remotePort)+":127.0.0.1:"+strconv.Itoa(localPort))
	}
}

// NewSSH wraps inner so its command runs on host over ssh.
func NewSSH(inner Executor, host string, opts ...SSHOption) Executor {
	s := ssh{inner: inner, host: host}
	for _, o := range opts {
		o(&s)
	}
	return s
}

// Exec hands the command inward as an ssh command line. Its directory and
// variables are the remote's, so they go into the remote line rather than to
// the local ssh process.
func (s ssh) Exec(ctx context.Context, cmd Command) Execution {
	if err := refuseSecrets("ssh", cmd); err != nil {
		return Finished(cmd, err)
	}
	if err := refuseBadKeys("ssh", cmd); err != nil {
		return Finished(cmd, err)
	}
	args := []string{"ssh"}
	if s.tty {
		args = append(args, "-t")
	}
	if s.noMaster {
		args = append(args, "-o", "ControlPath=none")
	}
	if s.failFast {
		args = append(args, "-o", "ExitOnForwardFailure=yes")
	}
	args = append(args, s.reverse...)
	args = append(args, s.host, format(cmd))
	return s.inner.Exec(ctx, cmd.rewrapped(args))
}
