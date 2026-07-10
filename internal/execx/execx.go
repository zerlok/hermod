// Package execx runs one argv either for real or by printing it. It answers how
// a command runs; it knows nothing about where (transport) or why (domain).
package execx

import (
	"context"
	"io"
	"os"
	"strings"
)

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
}

// Executor runs one Command.
type Executor interface {
	Execute(ctx context.Context, cmd Command) (Result, error)
}

type Config struct {
	DryRun       bool
	DryRunOutput io.Writer
}

type Option interface {
	Apply(*Config)
}

type optionFunc func(*Config)

func (fn optionFunc) Apply(config *Config) { fn(config) }

func WithDryRun(toggle bool) Option {
	return optionFunc(func(config *Config) {
		config.DryRun = toggle
	})
}

func WithDryRunOutput(w io.Writer) Option {
	return optionFunc(func(config *Config) {
		config.DryRun = w != nil
		config.DryRunOutput = w
	})
}

func New(opts ...Option) Executor {
	config := &Config{DryRunOutput: os.Stdout}
	for _, opt := range opts {
		opt.Apply(config)
	}

	if config.DryRun {
		return executorFunc(dryRun{out: config.DryRunOutput}.run)
	}

	return executorFunc(runSubprocess)
}

// executorFunc adapts a plain function to the Executor interface, so stateless
// executors need no receiver type.
type executorFunc func(context.Context, Command) (Result, error)

func (f executorFunc) Execute(ctx context.Context, cmd Command) (Result, error) {
	return f(ctx, cmd)
}

// Join renders an argv as a single space-separated line with each element
// shell-quoted. It collapses a sandbox command into one argument when crossing a
// host boundary: ssh joins its trailing args with spaces, so the command must
// arrive pre-quoted as a single string to survive the sandbox shell re-parsing it.
func Join(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		parts[i] = Quote(a)
	}
	return strings.Join(parts, " ")
}

// Quote returns s verbatim when it is made only of shell-safe characters,
// otherwise single-quoted with embedded single quotes escaped.
func Quote(s string) string {
	if s == "" {
		return "''"
	}
	for _, r := range s {
		if !isShellSafe(r) {
			return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
		}
	}
	return s
}

func isShellSafe(r rune) bool {
	switch {
	case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		return true
	default:
		return strings.ContainsRune("_-./:=@,", r)
	}
}
