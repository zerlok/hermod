package shell

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// waitDelay bounds how long a command's streams are waited on once it has
// exited or its context is done and it was killed. A child it left behind
// (ssh's, a shell's background job) can hold the output pipes open, and without
// the bound the command would not return until that child exits.
const waitDelay = 500 * time.Millisecond

// interruptGrace is how long a command sent its Interrupt has to exit before
// it is killed.
const interruptGrace = 2 * time.Second

// local is the leaf Shell: it runs a command on this machine. run is chosen at
// construction — a real subprocess or, under dry-run, a printer — so a dry-run
// is a leaf swap and every decorator above it is unchanged.
type local struct {
	run func(context.Context, Command) (Result, error)
}

// NewLocal returns the leaf Shell that executes on this machine.
func NewLocal() Shell {
	return local{run: runSubprocess}
}

// NewDryRun returns the leaf Shell for a dry run: it prints each command to plan
// as a copy-pasteable shell line and runs nothing. Callers that need a probe to
// stay real under dry-run use a NewLocal leaf for it.
func NewDryRun(plan io.Writer) Shell {
	return local{run: printTo(plan)}
}

func (l local) Run(ctx context.Context, cmd Command) (Result, error) {
	return l.run(ctx, cmd)
}

// runSubprocess runs the argv as a real child process via os/exec, with no
// intermediate shell. Capture mode collects stdout and stderr; otherwise each
// stream is the one the command gives, else the process's own, so an
// interactive attach shares the real terminal and the call blocks until it
// exits (i.e. until detach).
func runSubprocess(ctx context.Context, cmd Command) (Result, error) {
	if len(cmd.Argv) == 0 {
		return Result{}, errors.New("shell: empty argv")
	}
	if cmd.Capture && (cmd.Stdout != nil || cmd.Stderr != nil) {
		return Result{}, errors.New("shell: Capture collects the output, so Stdout and Stderr must be nil")
	}
	c := exec.CommandContext(ctx, cmd.Argv[0], cmd.Argv[1:]...)
	c.Dir = cmd.Dir
	c.WaitDelay = waitDelay
	if cmd.Interrupt != nil {
		c.Cancel = func() error { return c.Process.Signal(cmd.Interrupt) }
		c.WaitDelay = interruptGrace
	}
	// A nil Env inherits the parent environment, which local tools (git, ssh,
	// mutagen, tmux) need for PATH and ssh-agent; extra entries are appended only
	// when the caller supplies them.
	if len(cmd.Env) > 0 {
		c.Env = append(os.Environ(), cmd.Env...)
	}
	if cmd.Capture {
		var stdout, stderr bytes.Buffer
		c.Stdin, c.Stdout, c.Stderr = cmd.Stdin, &stdout, &stderr
		err := c.Run()
		return Result{Stdout: stdout.String(), Stderr: stderr.String()}, exited(err)
	}
	c.Stdin = orStdin(cmd.Stdin)
	c.Stdout = orWriter(cmd.Stdout, os.Stdout)
	c.Stderr = orWriter(cmd.Stderr, os.Stderr)
	return Result{}, exited(c.Run())
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

func orStdin(r io.Reader) io.Reader {
	if r == nil {
		return os.Stdin
	}
	return r
}

func orWriter(w, fallback io.Writer) io.Writer {
	if w == nil {
		return fallback
	}
	return w
}

// printTo returns a run func that prints each command as a copy-pasteable shell
// line and executes nothing.
func printTo(w io.Writer) func(context.Context, Command) (Result, error) {
	return func(_ context.Context, cmd Command) (Result, error) {
		_, err := fmt.Fprintln(w, format(cmd))
		return Result{}, err
	}
}

// format renders a Command as a single copy-pasteable shell line, including any
// working directory and environment so the printed form runs standalone.
func format(cmd Command) string {
	var b strings.Builder
	if cmd.Dir != "" {
		b.WriteString("cd ")
		b.WriteString(quote(cmd.Dir))
		b.WriteString(" && ")
	}
	for _, e := range cmd.Env {
		if k, v, found := strings.Cut(e, "="); found {
			b.WriteString(k)
			b.WriteByte('=')
			b.WriteString(quote(v))
		} else {
			b.WriteString(quote(e))
		}
		b.WriteByte(' ')
	}
	b.WriteString(join(cmd.Argv))
	return b.String()
}
