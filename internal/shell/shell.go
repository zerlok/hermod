// Package shell answers where and how a command runs. An Executor executes one
// Command and returns its Execution. The local leaf runs it on this machine
// and the dry-run leaf prints it; ssh, login and tmux rewrite the command and
// hand it inward. A caller nests the ones it needs, and the choice between
// local and dry-run is made once, at the innermost one, so nothing above it
// knows about dry-run and every wrapper is tested by the command it hands in.
package shell

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/zerlok/hermod/internal/env"
)

// Command is one command to execute, built once.
type Command struct {
	Args []string // the program and its arguments, run with no intermediate shell
	Dir  string   // working directory; "" is the current one
	Env  env.Vars // variables added to the process environment

	// A stream left nil is offered on the Execution instead: a pipe the
	// caller writes the input to and closes, or reads the output from to its
	// end, since the process blocks until its output is read.
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer

	// Interrupt, when set, is sent to the process once the context is done, in
	// place of killing it at once, so a program that has children of its own
	// can pass the signal on to them; one still running interruptGrace later
	// is killed. Nil kills it at once.
	Interrupt os.Signal
}

// rewrapped is cmd with a wrapper's arguments in place of its own. Dir and Env
// are dropped, since the wrapper folded them into the arguments; the streams
// and the interrupt carry through to the leaf that runs it.
func (cmd Command) rewrapped(args []string) Command {
	return Command{Args: args, Stdin: cmd.Stdin, Stdout: cmd.Stdout, Stderr: cmd.Stderr, Interrupt: cmd.Interrupt}
}

// refuseSecrets is the error of a wrapper that would fold a secret variable of
// cmd into a command line, nil when cmd has none. A command line is readable
// in the process list of both machines, so a credential must reach a remote
// program through a file or its stdin instead.
func refuseSecrets(wrapper string, cmd Command) error {
	for _, p := range cmd.Env.Pairs() {
		if p.Value.IsSecret() {
			return fmt.Errorf("shell: %s cannot put the secret variable %s on a command line", wrapper, p.Key)
		}
	}
	return nil
}

// refuseBadKeys is the error of a wrapper that would write a variable of cmd
// whose key is not a shell name, nil when every key is one. The key is written
// unquoted on a line a shell parses, so any other key runs as something else:
// `A;B=1 ls` runs A, and `FOO.BAR=1 ls` runs a program of that name.
func refuseBadKeys(wrapper string, cmd Command) error {
	for _, key := range cmd.Env.Keys() {
		if !isName(key) {
			return fmt.Errorf("shell: %s cannot put the variable %q on a command line: %w", wrapper, key, errNotAName)
		}
	}
	return nil
}

var errNotAName = errors.New("not a shell name")

// isName reports whether key is a shell variable name, [A-Za-z_][A-Za-z0-9_]*.
func isName(key string) bool {
	if key == "" {
		return false
	}
	for i, r := range key {
		letter := r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if !letter && (i == 0 || r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// Execution is a running command: the pipes of the streams its Command left
// nil, each nil where the Command gave one, and its result.
type Execution struct {
	Stdin  io.WriteCloser
	Stdout io.ReadCloser
	Stderr io.ReadCloser
	wait   func() error
}

// NewExecution is an execution over the given pipes whose result wait
// returns; wait is called once however often Wait is.
func NewExecution(stdin io.WriteCloser, stdout, stderr io.ReadCloser, wait func() error) Execution {
	return Execution{Stdin: stdin, Stdout: stdout, Stderr: stderr, wait: sync.OnceValue(wait)}
}

// Finished is the execution of cmd that ran nothing and ended with err: its
// pipes take no input and give no output.
func Finished(cmd Command, err error) Execution {
	var ex Execution
	if cmd.Stdin == nil {
		ex.Stdin = discard{}
	}
	if cmd.Stdout == nil {
		ex.Stdout = io.NopCloser(bytes.NewReader(nil))
	}
	if cmd.Stderr == nil {
		ex.Stderr = io.NopCloser(bytes.NewReader(nil))
	}
	ex.wait = func() error { return err }
	return ex
}

// errNeverStarted is the result of an Execution no Executor returned.
var errNeverStarted = errors.New("shell: the execution was never started")

// Wait waits for the command to end and returns its result: nil when it
// succeeded, else an error that ExitCode reads when the process ran.
func (e Execution) Wait() error {
	if e.wait == nil {
		return errNeverStarted
	}
	return e.wait()
}

// Executor executes a command on a workstation.
type Executor interface {
	Exec(ctx context.Context, cmd Command) Execution
}

// Output is what a command wrote, as Run collects it.
type Output struct {
	Stdout string
	Stderr string
}

// Run executes cmd to its end and collects what it wrote to the streams the
// Command left nil. A nil Stdin is empty: a command that reads a state must
// not consume input meant for something else.
func Run(ctx context.Context, ex Executor, cmd Command) (Output, error) {
	var stdout, stderr bytes.Buffer
	if cmd.Stdin == nil {
		cmd.Stdin = NoInput
	}
	if cmd.Stdout == nil {
		cmd.Stdout = &stdout
	}
	if cmd.Stderr == nil {
		cmd.Stderr = &stderr
	}
	err := ex.Exec(ctx, cmd).Wait()
	return Output{Stdout: stdout.String(), Stderr: stderr.String()}, err
}

// NoInput is an empty stdin. A setup command gets it so that it cannot consume
// input meant for the program Hermod runs in its caller's place: ssh forwards
// its stdin to the remote by default.
var NoInput io.Reader = noInput{}

type noInput struct{}

func (noInput) Read([]byte) (int, error) { return 0, io.EOF }

// discard is the stdin of a command that runs nothing.
type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
func (discard) Close() error                { return nil }
