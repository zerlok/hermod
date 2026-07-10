// Package cli is the thin edge: it parses one invocation into an immutable
// Options and hands off to the control layer. It is the only package that
// imports the CLI framework, keeping the domain framework-free and testable.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/zerlok/hermod/internal/control"
)

// app holds the seams the root command depends on, injected so parsing and
// hand-off can be tested without touching real hosts or the real environment.
type app struct {
	run  func(ctx context.Context, sandbox string, opts ...control.Option) error
	cwd  func() (string, error)
	home func() (string, error)
}

// Run builds and runs the root command, returning a process exit code.
func Run() int {
	a := app{run: control.Run, cwd: os.Getwd, home: os.UserHomeDir}
	if err := newRootCmd(a).Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "hermod:", err)
		return 1
	}
	return 0
}

func newRootCmd(a app) *cobra.Command {
	var (
		session string
		dir     string
		dryRun  bool
		quiet   bool
	)
	cmd := &cobra.Command{
		Use:   "hermod <sandbox> [flags] [-- <sandbox command>...]",
		Short: "Mirror your working directory to a sandbox sandbox and attach a persistent session",
		// The domain surfaces its own errors; usage is printed only for the
		// argument errors below, and Execute prints the error line itself.
		SilenceUsage:  true,
		SilenceErrors: true,
		Args: func(cmd *cobra.Command, args []string) error {
			switch numPositional(cmd, args) {
			case 1:
				return nil
			case 0:
				_ = cmd.Usage()
				return errors.New("a sandbox host alias is required")
			default:
				_ = cmd.Usage()
				return errors.New("only one sandbox host alias may be given")
			}
		},
		ValidArgsFunction: sshHostCompletions(a.home),
		RunE: func(cmd *cobra.Command, args []string) error {
			sandbox, passthrough := splitDash(cmd, args)
			cwd, err := a.cwd()
			if err != nil {
				return fmt.Errorf("resolve working directory: %w", err)
			}
			home, _ := a.home() // a missing home only weakens path mapping; not fatal
			return a.run(cmd.Context(), sandbox,
				control.WithWorkdir(dir, cwd, home),
				control.WithSession(session),
				control.WithCommand(passthrough),
				control.WithDryRun(dryRun),
				control.WithQuiet(quiet),
			)
		},
	}
	cmd.Flags().StringVarP(&session, "session", "s", "", "session name for tmux and mutagen (default: directory path slug)")
	cmd.Flags().StringVarP(&dir, "directory", "C", "", "directory to mirror (default: current directory)")
	cmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false, "print the commands that would run without executing side effects")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "suppress step logging")
	return cmd
}

// numPositional counts arguments before a `--` separator; without one, all args
// are positional.
func numPositional(cmd *cobra.Command, args []string) int {
	if n := cmd.ArgsLenAtDash(); n >= 0 {
		return n
	}
	return len(args)
}

// splitDash separates the sandbox alias from the verbatim passthrough command
// that follows `--`.
func splitDash(cmd *cobra.Command, args []string) (sandbox string, passthrough []string) {
	if n := cmd.ArgsLenAtDash(); n >= 0 {
		return args[0], args[n:]
	}
	return args[0], nil
}
