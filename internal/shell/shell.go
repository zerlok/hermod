// Package shell answers where a command runs. A Shell rewrites an argv for a
// transport (ssh, tmux) and delegates inward to the local leaf, which runs it
// for real or, under dry-run, prints it. Decorators nest freely — the caller
// composes the stack it needs — which keeps the tool both dry-runnable and
// unit-testable.
package shell

import "context"

// Command is a single argv to run. Dir and Env supply context; Capture selects
// whether stdout is collected or the child inherits the process stdio.
type Command struct {
	Argv    []string // program and arguments, run directly with no intermediate shell
	Dir     string   // working directory; "" inherits the current process directory
	Env     []string // extra environment as KEY=VALUE, appended to the process env
	Capture bool     // capture stdout into Result; otherwise inherit process stdio
}

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
