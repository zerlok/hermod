// Package cli is the thin edge: it parses one invocation, its arguments and its
// environment, into an immutable Options and hands off to the control layer. It
// is the one place an invocation is resolved, an argument always winning over
// the environment. It is the only package that imports the CLI framework,
// keeping the domain framework-free and testable.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"unicode"

	"github.com/spf13/cobra"

	"github.com/zerlok/hermod/internal/control"
)

// The environment an invocation reads. An argument always wins over these.
const (
	// envSandbox is the sandbox when no SANDBOX argument is given.
	envSandbox = control.EnvSandbox
	// envSandboxCmd is attach's command when no `-- COMMAND` is given: a command
	// line for the sandbox user's shell.
	envSandboxCmd = "HERMOD_SANDBOX_CMD"
	// envOrigin is the name this machine is given on the sandbox. Unset, it is the
	// hostname; set to empty, nothing names the machine.
	envOrigin = control.EnvOrigin
	// envCacheHome is the XDG base for the stand-in's session logs; ~/.cache
	// when unset.
	envCacheHome = "XDG_CACHE_HOME"
	// envClaudeConfigDir is Claude's local config directory, where it keeps this
	// machine's transcripts; ~/.claude when unset.
	envClaudeConfigDir = control.EnvClaudeConfigDir
	// envDataHome is the XDG base for the stand-in link and each sandbox's
	// Claude config directory; ~/.local/share when unset.
	envDataHome = "XDG_DATA_HOME"
	// envRuntimeDir is the XDG base for the mirrors' lease files.
	envRuntimeDir = "XDG_RUNTIME_DIR"
)

// The programs an Air entry runs, found on PATH unless given.
const (
	defaultNode    = "node"
	defaultAdapter = "claude-agent-acp"
)

// agentClaude is the one agent `hermod air` registers.
const agentClaude = "claude"

// standInName is the program name under which the binary stands in for Claude.
const standInName = "claude"

// app holds the seams the commands depend on, injected so parsing and hand-off
// can be tested without touching real hosts or the real environment.
type app struct {
	run        func(ctx context.Context, sandbox string, opts ...control.Option) error
	configure  func(r control.Registration) error
	runACP     func(ctx context.Context, a control.ACPAdapter) error
	cwd        func() (string, error)
	home       func() (string, error)
	hostname   func() (string, error)
	lookupEnv  func(key string) (string, bool)
	environ    func() []string
	lookPath   func(file string) (string, error)
	executable func() (string, error)
}

// Run runs this process's invocation and returns its exit code. A
// SIGINT/SIGTERM cancels the command context, so an interrupted attach unwinds
// through the normal teardown path rather than leaving the mirror leaking.
//
// SIGPIPE is caught too. A caller such as the Agent SDK may stop reading
// Hermod's stdout or stderr before Hermod is done, and Go kills a program that
// writes to a broken pipe on fd 1 or 2 unless it handles the signal; caught, the
// write fails with EPIPE and the run goes on to settle its mirrors. It is caught
// rather than ignored because an ignored signal stays ignored in the programs
// Hermod starts, and a caught one does not.
func Run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	brokenPipe := make(chan os.Signal, 1)
	signal.Notify(brokenPipe, syscall.SIGPIPE)
	defer signal.Stop(brokenPipe)

	a := app{
		run: control.Run, configure: control.Configure, runACP: control.RunACP,
		cwd: os.Getwd, home: os.UserHomeDir, hostname: os.Hostname,
		lookupEnv: os.LookupEnv, environ: os.Environ,
		lookPath: exec.LookPath, executable: os.Executable,
	}
	return exitCode(execute(ctx, a, os.Args), os.Stderr)
}

// execute dispatches on the program name in argv[0]: started as claude, the
// binary is the stand-in; under any other name it runs Hermod's command line.
func execute(ctx context.Context, a app, argv []string) error {
	if len(argv) > 0 && filepath.Base(argv[0]) == standInName {
		return a.standIn(ctx, argv[1:])
	}
	cmd := newRootCmd(a)
	if len(argv) > 0 {
		cmd.SetArgs(argv[1:])
	}
	return cmd.ExecuteContext(ctx)
}

// exitCode maps a run's error to the process exit code, printing it to stderr.
// A program run in the caller's place passes its own code on, and Hermod adds no
// line: the caller reads that program's output as its own.
func exitCode(err error, stderr io.Writer) int {
	var status control.ExitStatus
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
		Short: "Mirror your working directory to a sandbox and attach a persistent session",
		// The domain surfaces its own errors; usage is printed only for argument
		// errors, and Execute's caller prints the error line itself.
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.AddCommand(newAttachCmd(a), newAirCmd(a))
	return cmd
}

func newAttachCmd(a app) *cobra.Command {
	var (
		session string
		dir     string
		dryRun  bool
		quiet   bool
	)
	cmd := &cobra.Command{
		Use:   "attach [sandbox] [flags] [-- <sandbox command>...]",
		Short: "Mirror a directory to the sandbox and attach its persistent tmux session",
		Long: "Mirror a directory to the sandbox and attach its persistent tmux session.\n\n" +
			"The sandbox is an ~/.ssh/config host alias, " + envSandbox + " when none is given. The session runs\n" +
			"the command after --, else " + envSandboxCmd + " through the sandbox user's shell, else that shell.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args: func(cmd *cobra.Command, args []string) error {
			if numPositional(cmd, args) > 1 {
				_ = cmd.Usage()
				return errors.New("only one sandbox host alias may be given")
			}
			return nil
		},
		ValidArgsFunction: sshHostCompletions(a.home),
		RunE: func(cmd *cobra.Command, args []string) error {
			arg, passthrough := splitDash(cmd, args)
			sandbox, err := resolveSandbox(arg, a.lookupEnv)
			if err != nil {
				_ = cmd.Usage()
				return err
			}
			return a.attach(cmd.Context(), sandbox, dir,
				control.WithSession(session),
				resolveCommand(passthrough, a.lookupEnv),
				control.WithDryRun(dryRun),
				control.WithQuiet(quiet),
			)
		},
	}
	cmd.Flags().StringVarP(&session, "session", "s", "", "tmux session name (default: directory path slug)")
	cmd.Flags().StringVarP(&dir, "directory", "C", "", "directory to mirror (default: current directory)")
	cmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false, "print the commands that would run without executing side effects")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "suppress step logging")
	return cmd
}

func newAirCmd(a app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "air",
		Short: "Register sandboxes with JetBrains Air",
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = cmd.Usage()
			if len(args) > 0 {
				return fmt.Errorf("unknown command %q for %q", args[0], cmd.CommandPath())
			}
			return errors.New("name a subcommand: configure or acp")
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.AddCommand(newConfigureCmd(a), newACPCmd(a))
	return cmd
}

// agentArgs checks the arguments of an `air` subcommand: the agent, then at
// most one sandbox.
func agentArgs(cmd *cobra.Command, args []string) error {
	switch {
	case len(args) == 0:
		_ = cmd.Usage()
		return errors.New("name the agent: " + agentClaude)
	case args[0] != agentClaude:
		return fmt.Errorf("unknown agent %q: only %s is supported", args[0], agentClaude)
	case len(args) > 2:
		_ = cmd.Usage()
		return errors.New("only one sandbox host alias may be given")
	}
	return nil
}

// agentCompletions completes an `air` subcommand's agent, then its sandbox.
func agentCompletions(a app) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) == 0 {
			return []string{agentClaude}, cobra.ShellCompDirectiveNoFileComp
		}
		return sshHostCompletions(a.home)(cmd, args[1:], toComplete)
	}
}

// agentSandbox resolves an `air` subcommand's sandbox: the argument after the
// agent, else the environment.
func agentSandbox(cmd *cobra.Command, args []string, lookupEnv func(string) (string, bool)) (string, error) {
	arg := ""
	if len(args) > 1 {
		arg = args[1]
	}
	sandbox, err := resolveSandbox(arg, lookupEnv)
	if err != nil {
		_ = cmd.Usage()
	}
	return sandbox, err
}

func newACPCmd(a app) *cobra.Command {
	var node, adapter string
	cmd := &cobra.Command{
		Use:   "acp claude [sandbox] [flags]",
		Short: "Run the ACP adapter for Air, closing a chat's session on the sandbox when Air marks it done",
		Long: "Run the ACP adapter for Air, closing a chat's session on the sandbox when Air marks it done.\n\n" +
			"This is what the entry `air configure` writes runs. It starts --node running --acp with its own\n" +
			"environment and copies ACP between Air and the adapter as it is. The sandbox is an ~/.ssh/config\n" +
			"host alias, " + envSandbox + " when none is given.",
		SilenceUsage:      true,
		SilenceErrors:     true,
		Args:              agentArgs,
		ValidArgsFunction: agentCompletions(a),
		RunE: func(cmd *cobra.Command, args []string) error {
			sandbox, err := agentSandbox(cmd, args, a.lookupEnv)
			if err != nil {
				return err
			}
			home, _ := a.home() // no home: no session log, and leases only under XDG_RUNTIME_DIR
			return a.runACP(cmd.Context(), control.ACPAdapter{
				Sandbox:       sandbox,
				Node:          node,
				Script:        adapter,
				Stdio:         commandStreams(cmd),
				SessionLogDir: sessionLogDir(home, a.lookupEnv),
				LeaseDir:      leaseDir(home, a.lookupEnv),
			})
		},
	}
	cmd.Flags().StringVar(&node, "node", defaultNode, "the Node binary (≥ 22) that runs the adapter, found on PATH")
	cmd.Flags().StringVar(&adapter, "acp", defaultAdapter, "the claude-agent-acp adapter, found on PATH")
	return cmd
}

func newConfigureCmd(a app) *cobra.Command {
	var (
		node, adapter, name, origin string
		dryRun                      bool
	)
	cmd := &cobra.Command{
		Use:   "configure claude [sandbox] [flags]",
		Short: "Make a sandbox an agent in Air's chat, and print what Air's terminal mode needs",
		Long: "Make a sandbox an agent in Air's chat, and print what Air's terminal mode needs.\n\n" +
			"Creates or repairs the claude stand-in link and merges one entry into Air's acp.json. The sandbox is\n" +
			"an ~/.ssh/config host alias, " + envSandbox + " when none is given.",
		SilenceUsage:      true,
		SilenceErrors:     true,
		Args:              agentArgs,
		ValidArgsFunction: agentCompletions(a),
		RunE: func(cmd *cobra.Command, args []string) error {
			sandbox, err := agentSandbox(cmd, args, a.lookupEnv)
			if err != nil {
				return err
			}
			r, err := a.registration(sandbox, node, adapter)
			if err != nil {
				return err
			}
			r.Name, r.DryRun, r.Out = name, dryRun, cmd.OutOrStdout()
			r.Origin = resolveEntryOrigin(cmd.Flags().Changed("origin"), origin, a.lookupEnv)
			return a.configure(r)
		},
	}
	cmd.Flags().StringVar(&node, "node", defaultNode, "the Node binary (≥ 22) that runs the adapter, found on PATH")
	cmd.Flags().StringVar(&adapter, "acp", defaultAdapter, "the claude-agent-acp adapter, found on PATH")
	cmd.Flags().StringVar(&name, "name", "", "the agent's name in Air (default: Claude (<sandbox>))")
	cmd.Flags().StringVar(&origin, "origin", "", "the name the entry's chats give this machine on the sandbox ("+envOrigin+"); empty names none")
	cmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false, "print the entry without changing anything")
	return cmd
}

// registration resolves what configure writes: the programs as absolute paths,
// since Air does not load the user's shell and could find nothing by name, and
// this binary, the user's home and data home.
func (a app) registration(sandbox, node, adapter string) (control.Registration, error) {
	home, err := a.home()
	if err != nil || home == "" {
		return control.Registration{}, errors.New("resolve the home directory, where Air keeps acp.json")
	}
	nodePath, err := a.absProgram(node, "--node")
	if err != nil {
		return control.Registration{}, err
	}
	adapterPath, err := a.absProgram(adapter, "--acp")
	if err != nil {
		return control.Registration{}, err
	}
	hermod, err := a.executable()
	if err != nil {
		return control.Registration{}, fmt.Errorf("resolve the hermod binary: %w", err)
	}
	return control.Registration{
		Sandbox:  sandbox,
		Node:     nodePath,
		Adapter:  adapterPath,
		Hermod:   hermod,
		DataHome: xdgHome(envDataHome, filepath.Join(".local", "share"), home, a.lookupEnv),
		Home:     home,
	}, nil
}

// absProgram finds a program the way the user's shell would, a name on PATH
// and a path as it is, and makes it absolute.
func (a app) absProgram(program, flag string) (string, error) {
	found, err := a.lookPath(program)
	if err != nil {
		return "", fmt.Errorf("find %s: %w; give its path with %s", program, err, flag)
	}
	if filepath.IsAbs(found) {
		return found, nil
	}
	cwd, err := a.cwd()
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}
	return filepath.Join(cwd, found), nil
}

// resolveEntryOrigin is the HERMOD_ORIGIN an entry carries: the flag when
// given, even empty, else the environment's when set, even empty, else none.
func resolveEntryOrigin(flagSet bool, flag string, lookupEnv func(string) (string, bool)) *string {
	if flagSet {
		return &flag
	}
	if v, set := lookupEnv(envOrigin); set {
		return &v
	}
	return nil
}

// standIn runs `attach -- claude ARGS…`. Every argument is Claude's, so the
// sandbox comes from the environment alone, and HERMOD_SANDBOX_CMD does not
// apply: the command is given.
func (a app) standIn(ctx context.Context, claudeArgs []string) error {
	sandbox, err := resolveSandbox("", a.lookupEnv)
	if err != nil {
		return err
	}
	home, _ := a.home()
	return a.attach(ctx, sandbox, "",
		control.WithCommand(append([]string{standInName}, claudeArgs...)),
		control.WithStandIn(sessionLogDir(home, a.lookupEnv)),
	)
}

// sessionLogDir is where Air's sessions are logged, one file per session:
// ~/.cache/hermod/air under the XDG cache base. With no home and no base it is
// "", which keeps no log rather than one under the working directory.
func sessionLogDir(home string, lookupEnv func(string) (string, bool)) string {
	if cache := xdgHome(envCacheHome, ".cache", home, lookupEnv); cache != "" {
		return filepath.Join(cache, "hermod", "air")
	}
	return ""
}

// commandStreams are the command's streams, which cobra defaults to the
// process's own and a test sets.
func commandStreams(cmd *cobra.Command) control.Streams {
	return control.Streams{In: cmd.InOrStdin(), Out: cmd.OutOrStdout(), Err: cmd.ErrOrStderr()}
}

// attach runs the attach flow on sandbox for dir ("" is the working directory),
// with what the command line or the stand-in adds in opts.
func (a app) attach(ctx context.Context, sandbox, dir string, opts ...control.Option) error {
	cwd, err := a.cwd()
	if err != nil {
		return fmt.Errorf("resolve working directory: %w", err)
	}
	home, _ := a.home() // a missing home only weakens path mapping; not fatal
	base := []control.Option{
		control.WithWorkdir(dir, cwd, home),
		control.WithOrigin(resolveOrigin(a.lookupEnv, a.hostname)),
		control.WithEnvironment(a.environ()),
		control.WithClaudeConfigDir(claudeConfigDir(home, a.lookupEnv)),
		control.WithLeaseDir(leaseDir(home, a.lookupEnv)),
	}
	return a.run(ctx, sandbox, append(base, opts...)...)
}

// leaseDir is where the mirrors' lease files live: $XDG_RUNTIME_DIR/hermod/lease,
// a per-user directory the system empties at logout, which suits files that
// only carry locks. Without one (macOS has none) it is under the cache
// directory, and with neither "", which shares no mirror.
func leaseDir(home string, lookupEnv func(string) (string, bool)) string {
	if v, _ := lookupEnv(envRuntimeDir); filepath.IsAbs(v) {
		return filepath.Join(v, "hermod", "lease")
	}
	if cache := xdgHome(envCacheHome, ".cache", home, lookupEnv); cache != "" {
		return filepath.Join(cache, "hermod", "lease")
	}
	return ""
}

// xdgHome is an XDG base directory: the variable when it holds an absolute
// path, else fallback under home, else "" when home is unknown. The XDG spec
// has a relative value ignored.
func xdgHome(key, fallback, home string, lookupEnv func(string) (string, bool)) string {
	if v, _ := lookupEnv(key); filepath.IsAbs(v) {
		return v
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, fallback)
}

// claudeConfigDir is Claude's local config directory: the variable when set,
// else ~/.claude, else "" when home is unknown.
func claudeConfigDir(home string, lookupEnv func(string) (string, bool)) string {
	if v, _ := lookupEnv(envClaudeConfigDir); v != "" {
		return v
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".claude")
}

// resolveSandbox picks the sandbox from the argument, else the environment. A
// blank variable counts as unset: it names no host. The alias becomes an ssh
// argument and part of the Mutagen endpoint, so one starting with '-' is
// rejected: ssh would read it as an option, and an option such as ProxyCommand
// runs a local command. The environment can carry any such value; the argument
// only a lone '-'. An ssh host alias holds no whitespace, quote or control
// character, and one that did would also break the HERMOD_SANDBOX line and
// the Air entry written for it, so such a value is rejected too.
func resolveSandbox(arg string, lookupEnv func(string) (string, bool)) (string, error) {
	alias, source := arg, "the argument"
	if strings.TrimSpace(alias) == "" {
		v, _ := lookupEnv(envSandbox)
		alias, source = v, envSandbox
	}
	switch {
	case strings.TrimSpace(alias) == "":
		return "", errors.New("a sandbox host alias is required: give one or set " + envSandbox)
	case strings.HasPrefix(alias, "-"):
		return "", fmt.Errorf("sandbox %q from %s is not a host alias: it starts with '-'", alias, source)
	case strings.ContainsFunc(alias, notInAlias):
		return "", fmt.Errorf("sandbox %q from %s is not a host alias: it holds whitespace, a quote or a control character", alias, source)
	}
	return alias, nil
}

func notInAlias(r rune) bool {
	return unicode.IsSpace(r) || unicode.IsControl(r) || r == '"' || r == '\''
}

// resolveCommand picks the session's command from the passthrough argv, else the
// environment's command line; with neither, the session runs the default shell.
func resolveCommand(passthrough []string, lookupEnv func(string) (string, bool)) control.Option {
	if len(passthrough) > 0 {
		return control.WithCommand(passthrough)
	}
	line, _ := lookupEnv(envSandboxCmd)
	return control.WithCommandLine(line)
}

// resolveOrigin names this machine: the environment's value when set, even
// empty, else the hostname. A hostname that cannot be read names nothing.
func resolveOrigin(lookupEnv func(string) (string, bool), hostname func() (string, error)) string {
	if v, set := lookupEnv(envOrigin); set {
		return v
	}
	h, err := hostname()
	if err != nil {
		return ""
	}
	return h
}

// numPositional counts arguments before a `--` separator; without one, all args
// are positional.
func numPositional(cmd *cobra.Command, args []string) int {
	if n := cmd.ArgsLenAtDash(); n >= 0 {
		return n
	}
	return len(args)
}

// splitDash separates the optional sandbox alias from the verbatim passthrough
// command that follows `--`.
func splitDash(cmd *cobra.Command, args []string) (sandbox string, passthrough []string) {
	n := numPositional(cmd, args)
	if n > 0 {
		sandbox = args[0]
	}
	return sandbox, args[n:]
}
