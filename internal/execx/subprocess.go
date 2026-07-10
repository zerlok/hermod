package execx

import (
	"context"
	"errors"
	"os"
	"os/exec"
)

// NewSubprocess returns an Executor that runs commands as real child processes
// via os/exec, with no intermediate shell. Capture mode collects stdout;
// otherwise the child inherits the process stdio so an interactive attach shares
// the real terminal and the call blocks until it exits (i.e. until detach).
func runSubprocess(ctx context.Context, cmd Command) (Result, error) {
	if len(cmd.Argv) == 0 {
		return Result{}, errors.New("execx: empty argv")
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
		out, err := c.Output()
		return Result{Stdout: string(out)}, err
	}
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return Result{}, c.Run()
}
