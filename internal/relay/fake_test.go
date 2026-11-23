package relay

import (
	"context"
	"io"
	"regexp"
	"sync"

	"github.com/zerlok/hermod/internal/shell"
)

// script plays one command: it reads in, writes out and errOut, and returns
// the command's result. It runs on its own goroutine, as a process would.
type script func(ctx context.Context, in io.Reader, out, errOut io.Writer) error

// fakeExec is a shell.Executor that records each command and plays the script
// answer gives for it, which is handed the command's own arguments.
type fakeExec struct {
	answer func(args []string) script

	mu   sync.Mutex
	ran  [][]string
	live int // commands started and not yet ended
}

// localPort is the part of a command line that names a port this process
// picked, which differs from run to run.
var localPort = regexp.MustCompile(`127\.0\.0\.1:\d+$`)

func (f *fakeExec) Exec(ctx context.Context, cmd shell.Command) shell.Execution {
	args := make([]string, len(cmd.Args))
	for i, a := range cmd.Args {
		args[i] = localPort.ReplaceAllString(a, "127.0.0.1:PORT")
	}
	f.mu.Lock()
	f.ran = append(f.ran, args)
	f.live++
	f.mu.Unlock()

	var exIn io.WriteCloser
	in := cmd.Stdin
	if in == nil {
		r, w := io.Pipe()
		in, exIn = r, w
	}
	out, exOut, closeOut := outPipe(cmd.Stdout)
	errOut, exErr, closeErr := outPipe(cmd.Stderr)
	play := f.answer(cmd.Args)
	done := make(chan error, 1)
	go func() {
		err := play(ctx, in, out, errOut)
		closeOut()
		closeErr()
		if r, ok := in.(*io.PipeReader); ok && exIn != nil {
			_ = r.Close()
		}
		f.mu.Lock()
		f.live--
		f.mu.Unlock()
		done <- err
	}()
	return shell.NewExecution(exIn, exOut, exErr, func() error { return <-done })
}

// outPipe is where a script writes an output stream: the writer the command
// gave, or a pipe the execution offers.
func outPipe(given io.Writer) (io.Writer, io.ReadCloser, func()) {
	if given != nil {
		return given, nil, func() {}
	}
	r, w := io.Pipe()
	return w, r, func() { _ = w.Close() }
}

// commands is what f ran since the last call, and forgets it.
func (f *fakeExec) commands() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	ran := f.ran
	f.ran = nil
	return ran
}

func (f *fakeExec) running() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.live
}

// untilDone is a long-running process: it runs until its context ends.
func untilDone(ctx context.Context, _ io.Reader, _, _ io.Writer) error {
	<-ctx.Done()
	return ctx.Err()
}

// writes is a process that writes text to its stdout and then runs on until
// its context ends.
func writes(text string) script {
	return func(ctx context.Context, _ io.Reader, out, _ io.Writer) error {
		if _, err := io.WriteString(out, text); err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	}
}
