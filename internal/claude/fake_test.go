package claude

import (
	"context"
	"io"

	"github.com/zerlok/hermod/internal/shell"
)

// fakeExec is a workstation that answers every command with the same output
// and result, and records the commands it was given.
type fakeExec struct {
	out    string
	stderr string
	err    error
	got    []shell.Command
}

func (f *fakeExec) Exec(_ context.Context, cmd shell.Command) shell.Execution {
	f.got = append(f.got, cmd)
	if cmd.Stdout != nil {
		_, _ = io.WriteString(cmd.Stdout, f.out)
	}
	if cmd.Stderr != nil {
		_, _ = io.WriteString(cmd.Stderr, f.stderr)
	}
	return shell.Finished(cmd, f.err)
}

// args are the arguments of each command the fake was given.
func (f *fakeExec) args() [][]string {
	var all [][]string
	for _, c := range f.got {
		all = append(all, c.Args)
	}
	return all
}
