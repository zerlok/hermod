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
)

// local is the leaf Shell: it runs a command on this machine. run is chosen at
// construction — a real subprocess or, under dry-run, a printer — so a dry-run
// is a leaf swap and every decorator above it is unchanged.
type local struct {
	run func(context.Context, Command) (Result, error)
}

// NewLocal returns the leaf Shell that executes on this machine. When dryRun is
// set, commands are printed as a copy-pasteable shell line rather than run —
// callers that need a probe to stay real under dry-run build a separate
// non-dry-run leaf for it.
func NewLocal(dryRun bool) Shell {
	if dryRun {
		return local{run: printTo(os.Stdout)}
	}
	return local{run: runSubprocess}
}

func (l local) Run(ctx context.Context, cmd Command) (Result, error) {
	return l.run(ctx, cmd)
}

// runSubprocess runs the argv as a real child process via os/exec, with no
// intermediate shell. Capture mode collects stdout; otherwise the child inherits
// the process stdio so an interactive attach shares the real terminal and the
// call blocks until it exits (i.e. until detach).
func runSubprocess(ctx context.Context, cmd Command) (Result, error) {
	if len(cmd.Argv) == 0 {
		return Result{}, errors.New("shell: empty argv")
	}
	c := exec.CommandContext(ctx, cmd.Argv[0], cmd.Argv[1:]...)
	c.Dir = cmd.Dir
	// A nil Env inherits the parent environment, which local tools (git, ssh,
	// mutagen, tmux) need for PATH and ssh-agent; extra entries are appended only
	// when the caller supplies them.
	if len(cmd.Env) > 0 {
		c.Env = append(os.Environ(), cmd.Env...)
	}
	if cmd.Capture {
		var stdout, stderr bytes.Buffer
		c.Stdout, c.Stderr = &stdout, &stderr
		err := c.Run()
		return Result{Stdout: stdout.String(), Stderr: stderr.String()}, err
	}
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return Result{}, c.Run()
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
