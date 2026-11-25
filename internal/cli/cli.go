// Package cli is the process's interface: the only package that reads the
// arguments, the environment, the process's streams and the name it was
// started under, and the one place every variable Hermod reads is named. It
// resolves each value once, an argument winning over its variable, builds
// what a command needs from the other adapters, and hands it into the core.
// It decides nothing from a state.
package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/zerlok/hermod/internal/agent"
	"github.com/zerlok/hermod/internal/air"
	"github.com/zerlok/hermod/internal/claude"
	"github.com/zerlok/hermod/internal/session"
	"github.com/zerlok/hermod/internal/shell"
	"github.com/zerlok/hermod/internal/workspace"
)

// The variables Hermod reads. An argument always wins over its variable.
const (
	// envRemote is the remote when no REMOTE argument is given.
	envRemote = "HERMOD_REMOTE"
	// envRemoteCmd is attach's command when no `-- COMMAND` is given: a
	// command line run by the remote user's shell.
	envRemoteCmd = "HERMOD_REMOTE_CMD"
	// envOrigin is the name this machine is given on the remote. Unset, it is
	// the hostname; set to empty, nothing on the remote names the machine.
	envOrigin = "HERMOD_ORIGIN"
	// envRuntimeDir is the XDG base for the relays' lock files.
	envRuntimeDir = "XDG_RUNTIME_DIR"
	// envCacheHome is the XDG base for the logs, and for the lock files where
	// there is no runtime directory; ~/.cache when unset.
	envCacheHome = "XDG_CACHE_HOME"
	// envDataHome is the XDG base for the default stand-in link;
	// ~/.local/share when unset.
	envDataHome = "XDG_DATA_HOME"
	// envPath is where configure looks for the stand-in link.
	envPath = "PATH"
	// envTeardownTimeout is how long a command may take to give up what it
	// holds when --teardown-timeout is not given, as a Go duration.
	envTeardownTimeout = "HERMOD_TEARDOWN_TIMEOUT"
)

// teardownTimeoutFlag is the flag every command takes its teardown's deadline
// from.
const teardownTimeoutFlag = "teardown-timeout"

// agentClaude is the one agent the `air` commands serve.
const agentClaude = "claude"

// The words of the `air` commands, which the stand-in's message and Air's entry
// repeat.
const (
	airCmd       = "air"
	acpCmd       = "acp"
	configureCmd = "configure"
)

// chatArgs is the command line that serves Air's chat: Air's entry runs it, and
// the stand-in names it to a program that asks for a conversation.
var chatArgs = []string{airCmd, acpCmd, agentClaude}

// app holds what the commands read of the process and the use cases they
// call, injected so a test runs no command and reads no real environment.
type app struct {
	attach    func(context.Context, session.Request) error
	chat      func(context.Context, session.ChatRequest) error
	configure func(air.Request) (air.Result, error)
	// teardown starts the contexts in which a command gives up what its run
	// holds, each bounded by timeout.
	teardown func(timeout time.Duration) workspace.Teardown

	// local runs every command on this workstation; a dry run plans the
	// commands that change a state with dryRun instead.
	local  shell.Executor
	dryRun func(plan io.Writer) shell.Executor

	cwd        func() (string, error)
	home       func() (string, error)
	hostname   func() (string, error)
	lookupEnv  func(key string) (string, bool)
	environ    func() []string
	executable func() (string, error)
	// newAgent builds the agent that answers through exec.
	newAgent func(exec shell.Executor) agent.Agent
	// terminal reports whether the caller is on a terminal.
	terminal func() bool
	// openLog opens a log for appending, creating it private.
	openLog func(name string) (io.WriteCloser, error)

	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
}

// Run runs this process's invocation and returns its exit code. The first
// SIGINT or SIGTERM ends the command's run, so a session unwinds through its
// detach rather than leaving its relays leased; the detach is bounded by the
// teardown's deadline, and a second signal ends it at once.
//
// SIGPIPE is caught too. A caller such as the Agent SDK may stop reading
// Hermod's stdout or stderr before Hermod is done, and Go kills a program that
// writes to a broken pipe on fd 1 or 2 unless it handles the signal; caught,
// the write fails with EPIPE and the run goes on to detach. It is caught rather
// than ignored because an ignored signal stays ignored in the programs Hermod
// starts, and a caught one does not.
func Run() int {
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	contexts, stop := newShutdown(signals, time.Now)
	defer stop()
	brokenPipe := make(chan os.Signal, 1)
	signal.Notify(brokenPipe, syscall.SIGPIPE)
	defer signal.Stop(brokenPipe)

	a := app{
		attach: session.Attach, chat: session.Chat, configure: air.Configure,
		teardown: contexts.teardown,
		local:    shell.NewLocal(), dryRun: shell.NewDryRun,
		cwd: os.Getwd, home: os.UserHomeDir, hostname: os.Hostname,
		lookupEnv: os.LookupEnv, environ: os.Environ, executable: os.Executable,
		newAgent: claudeAgent(readOnce(os.ReadFile)),
		terminal: func() bool { return isTerminal(os.Stdin) && isTerminal(os.Stdout) },
		openLog:  openLog,
		stdin:    os.Stdin, stdout: os.Stdout, stderr: os.Stderr,
	}
	return exitCode(execute(contexts.run, a, os.Args), os.Stderr)
}

// readOnce is read with each file's first answer kept for the process. A
// session reads the caller's settings and MCP files more than once, finding
// the client's hook endpoint before the agent's start rewrites them; a file
// such as a pipe gives its bytes to one read only, and one the client rewrites
// in between would name another endpoint, so every read sees the first.
func readOnce(read func(name string) ([]byte, error)) func(name string) ([]byte, error) {
	type answer struct {
		content []byte
		err     error
	}
	var (
		mu      sync.Mutex
		answers = map[string]answer{}
	)
	return func(name string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		got, ok := answers[name]
		if !ok {
			got.content, got.err = read(name)
			answers[name] = got
		}
		// A copy, so a caller that changes its bytes changes no later read's.
		return bytes.Clone(got.content), got.err
	}
}

// execute dispatches on the program name in argv[0]: started under Claude's
// program name, the binary is the stand-in; under any other name it runs
// Hermod's command line.
func execute(ctx context.Context, a app, argv []string) error {
	if len(argv) > 0 && filepath.Base(argv[0]) == claude.Program {
		return a.standIn(ctx, argv[1:])
	}
	cmd := newRootCmd(a)
	cmd.SetIn(a.stdin)
	cmd.SetOut(a.stdout)
	cmd.SetErr(a.stderr)
	if len(argv) > 0 {
		cmd.SetArgs(argv[1:])
	}
	return cmd.ExecuteContext(ctx)
}

// exitCode maps a run's error to the process exit code, printing it to
// stderr. A program run in the caller's place passes its own code on, and
// Hermod adds no line: the caller reads that program's output as its own.
func exitCode(err error, stderr io.Writer) int {
	var status session.ExitStatus
	switch {
	case err == nil:
		return 0
	case errors.As(err, &status):
		return int(status)
	default:
		fmt.Fprintln(stderr, "hermod:", err)
		return 1
	}
}

func newRootCmd(a app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hermod",
		Short: "Attach a local client to a workspace on a remote",
		// The core reports its own errors; usage is printed only for argument
		// errors, and Run prints the error line itself.
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.PersistentFlags().Duration(teardownTimeoutFlag, defaultTeardownTimeout,
		"how long to give up what the command holds, from the first SIGINT or SIGTERM or the start of its teardown ("+envTeardownTimeout+")")
	cmd.AddCommand(newAttachCmd(a), newAirCmd(a))
	return cmd
}

func newAttachCmd(a app) *cobra.Command {
	var (
		name     string
		dir      string
		forwards []string
		dryRun   bool
	)
	cmd := &cobra.Command{
		Use:   "attach [REMOTE] [flags] [-- COMMAND...]",
		Short: "Attach to a worker running a command in this directory's workspace on a remote",
		Long: "Attach to a worker running a command in this directory's workspace on a remote.\n\n" +
			"REMOTE is an ~/.ssh/config host alias, " + envRemote + " when none is given. The worker runs\n" +
			"the command after --, else " + envRemoteCmd + " through the remote user's shell, else that shell.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args: func(cmd *cobra.Command, args []string) error {
			if numPositional(cmd, args) > 1 {
				_ = cmd.Usage()
				return errors.New("only one remote host alias may be given")
			}
			return nil
		},
		ValidArgsFunction: sshHostCompletions(a.home),
		RunE: func(cmd *cobra.Command, args []string) error {
			arg, passthrough := splitDash(cmd, args)
			remote, err := resolveRemote(arg, a.lookupEnv)
			if err != nil {
				_ = cmd.Usage()
				return err
			}
			fwds, err := parseForwards(forwards)
			if err != nil {
				_ = cmd.Usage()
				return err
			}
			timeout, err := teardownTimeout(cmd, a.lookupEnv)
			if err != nil {
				return err
			}
			return a.runAttach(cmd.Context(), attachIntent{
				remote:   remote,
				dir:      dir,
				name:     name,
				args:     resolveCommand(passthrough, a.lookupEnv),
				forwards: fwds,
				dryRun:   dryRun,
				teardown: timeout,
			}, attachOutput{plan: a.stdout, logs: a.stderr})
		},
	}
	cmd.Flags().StringVarP(&name, "session", "s", "", "the worker's name, for a command that is no agent's conversation (default: the directory's slug)")
	cmd.Flags().StringVarP(&dir, "directory", "C", "", "the directory to work in (default: the current directory)")
	cmd.Flags().StringArrayVarP(&forwards, "relay", "R", nil, "relay calls to PORT on the remote to ADDRESS here, as PORT:ADDRESS; repeatable")
	cmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false, "print the commands that would change a state, and run none of them")
	return cmd
}

func newAirCmd(a app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   airCmd,
		Short: "Serve a remote's agent to JetBrains Air",
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = cmd.Usage()
			if len(args) > 0 {
				return fmt.Errorf("unknown command %q for %q", args[0], cmd.CommandPath())
			}
			return errors.New("name a subcommand: " + configureCmd + " or " + acpCmd)
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.AddCommand(newConfigureCmd(a), newACPCmd(a))
	return cmd
}

// agentArgs checks the arguments of an `air` subcommand: the agent, then at
// most one remote.
func agentArgs(cmd *cobra.Command, args []string) error {
	switch {
	case len(args) == 0:
		_ = cmd.Usage()
		return errors.New("name the agent: " + agentClaude)
	case args[0] != agentClaude:
		return fmt.Errorf("unknown agent %q: only %s is supported", args[0], agentClaude)
	case len(args) > 2:
		_ = cmd.Usage()
		return errors.New("only one remote host alias may be given")
	}
	return nil
}

// agentCompletions completes an `air` subcommand's agent, then its remote.
func agentCompletions(a app) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) == 0 {
			return []string{agentClaude}, cobra.ShellCompDirectiveNoFileComp
		}
		return sshHostCompletions(a.home)(cmd, args[1:], toComplete)
	}
}

// agentRemote resolves an `air` subcommand's remote: the argument after the
// agent, else the environment.
func agentRemote(cmd *cobra.Command, args []string, lookupEnv func(string) (string, bool)) (string, error) {
	arg := ""
	if len(args) > 1 {
		arg = args[1]
	}
	remote, err := resolveRemote(arg, lookupEnv)
	if err != nil {
		_ = cmd.Usage()
	}
	return remote, err
}

func newACPCmd(a app) *cobra.Command {
	return &cobra.Command{
		Use:   acpCmd + " " + agentClaude + " [REMOTE]",
		Short: "Serve Air's chats of every project from their workspaces on a remote",
		Long: "Serve Air's chats of every project from their workspaces on a remote.\n\n" +
			"This is what the entry `" + airCmd + " " + configureCmd + "` writes runs: it speaks ACP with Air on stdin and stdout,\n" +
			"and routes each chat to the adapter of its project's workspace. REMOTE is an ~/.ssh/config\n" +
			"host alias, " + envRemote + " when none is given.",
		SilenceUsage:      true,
		SilenceErrors:     true,
		Args:              agentArgs,
		ValidArgsFunction: agentCompletions(a),
		RunE: func(cmd *cobra.Command, args []string) error {
			remote, err := agentRemote(cmd, args, a.lookupEnv)
			if err != nil {
				return err
			}
			timeout, err := teardownTimeout(cmd, a.lookupEnv)
			if err != nil {
				return err
			}
			return a.runChat(cmd.Context(), remote, timeout)
		},
	}
}

func newConfigureCmd(a app) *cobra.Command {
	var (
		name, origin string
		dryRun       bool
	)
	cmd := &cobra.Command{
		Use:   configureCmd + " " + agentClaude + " [REMOTE] [flags]",
		Short: "Make a remote an agent in Air's chat, and print what Air's terminal mode needs",
		Long: "Make a remote an agent in Air's chat, and print what Air's terminal mode needs.\n\n" +
			"Merges one entry into Air's acp.json, keeping every other entry. REMOTE is an ~/.ssh/config\n" +
			"host alias, " + envRemote + " when none is given.",
		SilenceUsage:      true,
		SilenceErrors:     true,
		Args:              agentArgs,
		ValidArgsFunction: agentCompletions(a),
		RunE: func(cmd *cobra.Command, args []string) error {
			remote, err := agentRemote(cmd, args, a.lookupEnv)
			if err != nil {
				return err
			}
			return a.runConfigure(configureIntent{
				remote: remote,
				name:   name,
				origin: resolveEntryOrigin(cmd.Flags().Changed("origin"), origin, a.lookupEnv),
				dryRun: dryRun,
			}, a.stdout)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "the agent's name in Air (default: Claude (<remote>))")
	cmd.Flags().StringVar(&origin, "origin", "", "the name the entry's chats give this machine on the remote ("+envOrigin+"); empty names none")
	cmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false, "print the entry without changing anything")
	return cmd
}

// numPositional counts the arguments before a `--` separator; without one,
// all are positional.
func numPositional(cmd *cobra.Command, args []string) int {
	if n := cmd.ArgsLenAtDash(); n >= 0 {
		return n
	}
	return len(args)
}

// splitDash separates the optional remote alias from the command that
// follows `--`, kept word for word.
func splitDash(cmd *cobra.Command, args []string) (remote string, passthrough []string) {
	n := numPositional(cmd, args)
	if n > 0 {
		remote = args[0]
	}
	return remote, args[n:]
}
