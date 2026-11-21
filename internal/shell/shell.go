// Package shell answers where a command runs. A Shell rewrites an argv for a
// transport (ssh, tmux) and delegates inward to the local leaf, which runs it
// for real or, under dry-run, prints it. Decorators nest freely — the caller
// composes the stack it needs — which keeps the tool both dry-runnable and
// unit-testable.
package shell

import (
	"context"
	"io"
	"os"
)

// Command is a single argv to run. Dir and Env supply context; Capture selects
// whether output is collected; Stdin, Stdout and Stderr give the child explicit
// streams, so a caller can hand it a pipe instead of the process's own.
type Command struct {
	Argv    []string // program and arguments, run directly with no intermediate shell
	Dir     string   // working directory; "" inherits the current process directory
	Env     []string // extra environment as KEY=VALUE, appended to the process env
	Capture bool     // collect stdout and stderr into Result; Stdout and Stderr must then be nil

	// Each stream left nil is the process's own, except stdin under Capture,
	// which is empty: a probe must not read input meant for something else.
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer

	// Interrupt, when set, is sent to the process once the context is done, in
	// place of killing it at once, so a program that has children of its own
	// can pass the signal on to them; one still running interruptGrace later
	// is killed. Nil kills it at once.
	Interrupt os.Signal
}

// rewrapped is cmd with a transport's argv in place of its own. Dir and Env are
// dropped, since a transport folds them into the argv; capture, the streams and
// the interrupt carry through to the leaf that runs it.
func (cmd Command) rewrapped(argv []string) Command {
	return Command{Argv: argv, Capture: cmd.Capture, Stdin: cmd.Stdin, Stdout: cmd.Stdout, Stderr: cmd.Stderr, Interrupt: cmd.Interrupt}
}

// NoInput is an empty stdin. A setup command gets it so that it cannot consume
// input meant for the program Hermod runs in its caller's place: ssh forwards
// its stdin to the sandbox by default.
var NoInput io.Reader = noInput{}

type noInput struct{}

func (noInput) Read([]byte) (int, error) { return 0, io.EOF }

// Result is the outcome of running a Command.
type Result struct {
	Stdout string // populated only when Command.Capture was set
	Stderr string // populated only when Command.Capture was set
}

// Shell runs a command in a particular place. Decorators wrap the argv and call
// an inner Shell; the local leaf is the innermost, executing on this machine.
type Shell interface {
	Run(ctx context.Context, cmd Command) (Result, error)
}
