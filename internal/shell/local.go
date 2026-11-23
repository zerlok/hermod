package shell

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/zerlok/hermod/internal/env"
)

// waitDelay bounds how long a command's streams are waited on once it has
// exited or its context is done and it was killed. A child it left behind
// (ssh's, a shell's background job) can hold the output pipes open, and without
// the bound the command would not return until that child exits.
const waitDelay = 500 * time.Millisecond

// interruptGrace is how long a command sent its Interrupt has to exit before
// it is killed.
const interruptGrace = 2 * time.Second

// local runs a command on this machine.
type local struct{}

// NewLocal is the leaf Executor that runs each command on this machine.
func NewLocal() Executor { return local{} }

// Exec starts the command as a child process with no intermediate shell.
func (local) Exec(ctx context.Context, cmd Command) Execution {
	if len(cmd.Args) == 0 {
		return Finished(cmd, errors.New("shell: a command needs a program"))
	}
	c := exec.CommandContext(ctx, cmd.Args[0], cmd.Args[1:]...)
	c.Dir = cmd.Dir
	c.WaitDelay = waitDelay
	if cmd.Interrupt != nil {
		c.Cancel = func() error { return c.Process.Signal(cmd.Interrupt) }
		c.WaitDelay = interruptGrace
	}
	// Without variables of its own the child inherits this process's
	// environment, which local tools (git, ssh, mutagen, tmux) need for PATH
	// and ssh-agent.
	if cmd.Env.Len() > 0 {
		c.Env = append(os.Environ(), cmd.Env.Reveal()...)
	}
	c.Stdin, c.Stdout, c.Stderr = cmd.Stdin, cmd.Stdout, cmd.Stderr

	var stdin io.WriteCloser
	if cmd.Stdin == nil {
		// The operating system's pipe: written to after the process ended, it
		// fails instead of blocking.
		w, err := c.StdinPipe()
		if err != nil {
			return Finished(cmd, fmt.Errorf("shell: stdin pipe: %w", err))
		}
		stdin = w
	}
	stdout, err := newOutput(cmd.Stdout)
	if err != nil {
		closeStdin(stdin)
		return Finished(cmd, fmt.Errorf("shell: stdout pipe: %w", err))
	}
	stderr, err := newOutput(cmd.Stderr)
	if err != nil {
		closeStdin(stdin)
		stdout.abandon()
		return Finished(cmd, fmt.Errorf("shell: stderr pipe: %w", err))
	}
	if stdout != nil {
		c.Stdout = stdout.child
	}
	if stderr != nil {
		c.Stderr = stderr.child
	}
	if err := c.Start(); err != nil {
		closeStdin(stdin)
		stdout.abandon()
		stderr.abandon()
		return NewExecution(stdin, stdout.reader(), stderr.reader(), func() error { return err })
	}
	stdout.pump()
	stderr.pump()
	done := make(chan error, 1)
	go func() {
		err := exited(c.Wait())
		time.AfterFunc(waitDelay, func() {
			stdout.cut(ctx.Err())
			stderr.cut(ctx.Err())
		})
		done <- err
	}()
	return NewExecution(stdin, stdout.reader(), stderr.reader(), func() error { return <-done })
}

func closeStdin(stdin io.WriteCloser) {
	if stdin != nil {
		_ = stdin.Close()
	}
}

// output is the pipe of an output stream the Command left nil. The child
// writes to an operating system pipe of its own, and a pump copies it to the
// caller's end, so each stream ends when its writers close it, whatever the
// other stream and Wait are doing, and the caller may read it after the
// process ended.
type output struct {
	child  *os.File // the write end the child is given
	parent *os.File // the read end the pump copies from
	r      *io.PipeReader
	w      *io.PipeWriter
}

// newOutput is the pipe for an output stream the Command left nil, and none
// for one it gave.
func newOutput(given io.Writer) (*output, error) {
	if given != nil {
		return nil, nil
	}
	parent, child, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	r, w := io.Pipe()
	return &output{child: child, parent: parent, r: r, w: w}, nil
}

// reader is the caller's end; nil for a stream the Command gave.
func (o *output) reader() io.ReadCloser {
	if o == nil {
		return nil
	}
	return o.r
}

// abandon ends the pipe of a command that did not start.
func (o *output) abandon() {
	if o == nil {
		return
	}
	_ = o.child.Close()
	_ = o.parent.Close()
	_ = o.w.Close()
}

// pump copies the child's output to the caller's end until the child and
// everything it started have closed it, or cut ends it. The parent's copy of
// the write end is closed first, or the pipe would never end.
func (o *output) pump() {
	if o == nil {
		return
	}
	_ = o.child.Close()
	go func() {
		err := o.copy()
		_ = o.parent.Close()
		_ = o.w.CloseWithError(err)
	}()
}

func (o *output) copy() error {
	buf := make([]byte, 32*1024)
	for {
		n, err := o.parent.Read(buf)
		if n > 0 {
			if _, werr := o.w.Write(buf[:n]); werr != nil {
				return werr
			}
		}
		switch {
		case errors.Is(err, io.EOF):
			return nil
		case errors.Is(err, os.ErrDeadlineExceeded):
			return o.drain(buf)
		case err != nil:
			return err
		}
	}
}

// drain copies what the pipe already holds, without waiting for more. The
// deadline that cut set fails a read even while data is waiting, so what the
// process wrote before it exited is read here rather than lost.
func (o *output) drain(buf []byte) error {
	if err := o.parent.SetReadDeadline(time.Time{}); err != nil {
		return err
	}
	raw, err := o.parent.SyscallConn()
	if err != nil {
		return err
	}
	for {
		var n int
		var rerr error
		// Returning true reads once without waiting, since the pipe is
		// non-blocking: an empty pipe gives EAGAIN instead of blocking.
		if err := raw.Read(func(fd uintptr) bool {
			n, rerr = syscall.Read(int(fd), buf)
			return true
		}); err != nil {
			return err
		}
		if errors.Is(rerr, syscall.EINTR) {
			continue
		}
		if n <= 0 || rerr != nil {
			return nil
		}
		if _, err := o.w.Write(buf[:n]); err != nil {
			return err
		}
	}
}

// cut stops waiting for a child the command left behind, which holds the pipe
// open: what is in the pipe is still delivered, and nothing after it. When the
// command was cancelled, a caller that no longer reads its end is given up on
// too, failing the pump's write with the cancellation so the pump ends.
func (o *output) cut(cancelled error) {
	if o == nil {
		return
	}
	_ = o.parent.SetReadDeadline(time.Now())
	if cancelled != nil {
		_ = o.w.CloseWithError(cancelled)
	}
}

// exited is a finished command's error. A command that succeeded while a child
// it left behind still held its streams succeeded: what it wrote had arrived
// by the time waitDelay cut the streams.
func exited(err error) error {
	if errors.Is(err, exec.ErrWaitDelay) {
		return nil
	}
	return err
}

// dryRun prints each command and runs nothing.
type dryRun struct {
	plan io.Writer
}

// NewDryRun is the leaf Executor for a dry run: it prints each command to plan
// as a line that can be pasted into a shell, and runs nothing. A command that
// only reads a state is given a NewLocal leaf instead, so it stays real.
func NewDryRun(plan io.Writer) Executor { return dryRun{plan: plan} }

// Exec prints the command; the execution ran nothing and succeeded unless the
// plan could not be written.
func (d dryRun) Exec(_ context.Context, cmd Command) Execution {
	// A plan line that a shell would parse differently is refused rather than
	// printed as one that can be pasted.
	if err := refuseBadKeys("dry-run", cmd); err != nil {
		return Finished(cmd, err)
	}
	_, err := fmt.Fprintln(d.plan, format(cmd))
	return Finished(cmd, err)
}

// format renders a Command as one shell line, its directory and variables
// included so the line runs on its own. A secret prints as its mask, so a plan
// shows that a credential is passed and never the credential. The caller has
// refused a key that is not a shell name, since a key is written unquoted.
func format(cmd Command) string {
	words := cmd.Env.Entries(func(v env.Value) string { return quote(v.String()) })
	if len(cmd.Args) > 0 {
		words = append(words, join(cmd.Args))
	}
	run := strings.Join(words, " ")
	if cmd.Dir == "" {
		return run
	}
	cd := "cd " + quote(cmd.Dir)
	// "&&" needs a command after it: a line ending in it is a syntax error.
	if run == "" {
		return cd
	}
	return cd + " && " + run
}
