// Package cli is the thin edge: it parses one invocation into an immutable
// Options and hands off to the control layer. It is the only package that
// imports the CLI framework, keeping the domain framework-free and testable.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/zerlok/hermod/internal/control"
	"github.com/zerlok/hermod/internal/notify"
)

// app holds the seams the root command depends on, injected so parsing and
// hand-off can be tested without touching real hosts or the real environment.
type app struct {
	run  func(ctx context.Context, sandbox string, opts ...control.Option) error
	send func(ctx context.Context, m notify.Message) error
	cwd  func() (string, error)
	home func() (string, error)
}

// Run builds and runs the root command, returning a process exit code. A
// SIGINT/SIGTERM cancels the command context, so an interrupted attach unwinds
// through the normal teardown path rather than leaving the mirror leaking.
func Run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a := app{run: control.Run, send: notify.Send, cwd: os.Getwd, home: os.UserHomeDir}
	if err := newRootCmd(a).ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "hermod:", err)
		return 1
	}
	return 0
}

func newRootCmd(a app) *cobra.Command {
	var (
		session  string
		dir      string
		dryRun   bool
		quiet    bool
		noNotify bool
	)
	cmd := &cobra.Command{
		Use:   "hermod <sandbox> [flags] [-- <sandbox command>...]",
		Short: "Mirror your working directory to a sandbox and attach a persistent session",
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
				control.WithNotify(!noNotify),
			)
		},
	}
	cmd.Flags().StringVarP(&session, "session", "s", "", "session name for tmux and mutagen (default: directory path slug)")
	cmd.Flags().StringVarP(&dir, "directory", "C", "", "directory to mirror (default: current directory)")
	cmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false, "print the commands that would run without executing side effects")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "suppress step logging")
	cmd.Flags().BoolVarP(&noNotify, "no-notify", "N", false, "do not open the back-channel that lets a sandbox process raise a local desktop notification")
	cmd.AddCommand(newNotifyCmd(a))
	return cmd
}

// newNotifyCmd is the `hermod notify` sender, run on the sandbox to raise a
// notification on the local machine over the session back-channel. It is a
// sibling of the root run command and unaffected by the root's flags.
func newNotifyCmd(a app) *cobra.Command {
	var (
		title   string
		urgency string
	)
	cmd := &cobra.Command{
		Use:           "notify [flags] <body>...",
		Short:         "Send a desktop notification to the local machine over the session back-channel",
		Args:          cobra.MinimumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.send(cmd.Context(), notify.Message{
				Title:   title,
				Urgency: urgency,
				Body:    strings.Join(args, " "),
			})
		},
	}
	cmd.Flags().StringVarP(&title, "title", "t", "", "notification title (default: hermod)")
	cmd.Flags().StringVarP(&urgency, "urgency", "u", "", "urgency: low, normal, or critical (default: normal)")
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
