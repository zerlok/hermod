package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"path"
	"path/filepath"
	"strings"

	"github.com/zerlok/hermod/internal/acp"
	"github.com/zerlok/hermod/internal/air"
	"github.com/zerlok/hermod/internal/claude"
	"github.com/zerlok/hermod/internal/env"
	"github.com/zerlok/hermod/internal/git"
	"github.com/zerlok/hermod/internal/relay"
	"github.com/zerlok/hermod/internal/session"
	"github.com/zerlok/hermod/internal/shell"
	"github.com/zerlok/hermod/internal/worker"
	"github.com/zerlok/hermod/internal/workspace"
)

// envClaudeConfigDir is Claude's local config directory, where Claude keeps
// this workstation's transcripts; ~/.claude when unset.
const envClaudeConfigDir = claude.EnvConfigDir

// attachIntent is one attach, as its command line resolved it.
type attachIntent struct {
	remote   string
	dir      string // as given; "" is the working directory
	name     string // the worker's name; "" is the directory's slug
	args     []string
	forwards []session.Forward
	dryRun   bool
}

// local is what a command reads of this workstation, resolved once.
type local struct {
	cwd  string
	home string
}

func (a app) readLocal() (local, error) {
	cwd, err := a.cwd()
	if err != nil {
		return local{}, fmt.Errorf("resolve the working directory: %w", err)
	}
	home, err := a.home()
	if err != nil || !filepath.IsAbs(home) {
		return local{}, errors.New("resolve the home directory, which the workspaces are placed by")
	}
	return local{cwd: cwd, home: home}, nil
}

// attachOutput is where an attach writes Hermod's own output.
type attachOutput struct {
	plan io.Writer // a dry run's plan
	logs io.Writer // Hermod's log lines
	// setup also gets what the Mutagen commands that change a state print;
	// nil keeps it only for a failure's message.
	setup io.Writer
}

// runAttach builds the session's request and runs it, writing Hermod's own
// output to out.
func (a app) runAttach(ctx context.Context, in attachIntent, out attachOutput) error {
	l, err := a.readLocal()
	if err != nil {
		return err
	}
	dir := localDir(in.dir, l.cwd, l.home)
	name := in.name
	if name == "" {
		name = workerName(dir, l.home)
	}
	exec := a.local
	switch {
	case in.dryRun:
		exec = a.dryRun(out.plan)
	case out.setup != nil:
		exec = mutagenOutput{ex: exec, w: out.setup}
	}
	remote, configDir, err := a.readRemote(ctx, in.remote, exec)
	if err != nil {
		return err
	}
	identity := git.Read(ctx, a.local, dir)
	var tree workspace.Worktree
	if main, root, ok := git.MainWorktree(ctx, a.local, dir); ok {
		tree = workspace.Worktree{Main: main, Root: root}
	}
	return a.attach(ctx, session.Request{
		Remote:   remote,
		Local:    a.workspaceLocal(l.home),
		Dir:      dir,
		Tree:     tree,
		Agent:    a.agent(dir, l.home, configDir),
		Args:     in.args,
		Name:     name,
		Forwards: in.forwards,
		Environ:  env.Environ(a.environ()),
		Identity: session.Identity{Name: identity.Name, Email: identity.Email},
		Streams:  relay.Streams{In: a.stdin, Out: a.stdout, Err: a.stderr},
		Log:      log.New(out.logs, "", 0),
	})
}

// runChat serves Air's one ACP connection on stdin and stdout. stdout is the
// connection, so Hermod's own lines go to stderr.
func (a app) runChat(ctx context.Context, host string) error {
	l, err := a.readLocal()
	if err != nil {
		return err
	}
	remote, configDir, err := a.readRemote(ctx, host, a.local)
	if err != nil {
		return err
	}
	logger := log.New(a.stderr, "", 0)
	ag := a.agent(l.cwd, l.home, configDir)
	adapters := session.Adapters{
		Remote:  remote,
		Local:   a.workspaceLocal(l.home),
		Agent:   ag,
		Environ: env.Environ(a.environ()),
		Identity: func(ctx context.Context, dir string) session.Identity {
			id := git.Read(ctx, a.local, dir)
			return session.Identity{Name: id.Name, Email: id.Email}
		},
		MainWorktree: func(ctx context.Context, dir string) (string, string, bool) {
			return git.MainWorktree(ctx, a.local, dir)
		},
		Connect: func(fromAdapter io.Reader, toAdapter io.Writer) session.Channel {
			return acp.NewAdapter(fromAdapter, toAdapter)
		},
		Errors: a.adapterLog(l.home),
		Log:    logger,
	}
	client := acp.NewClient(a.stdin, a.stdout, air.Methods())
	defer func() { _ = client.Close() }()
	return a.chat(ctx, session.ChatRequest{
		Client: client,
		Agent:  ag,
		Remote: remote,
		Local:  adapters.Local,
		Attach: adapters.Attach,
		Log:    logger,
	})
}

// adapterLog opens the log of an adapter's stderr, by the adapter's key, as
// the stand-in's session is logged by its worker: the process's stderr is
// Air's, and serves every adapter at once.
func (a app) adapterLog(home string) func(key worker.Key) (io.WriteCloser, error) {
	return func(key worker.Key) (io.WriteCloser, error) {
		f, err := a.openLog(filepath.Join(logDir(home, a.lookupEnv), key.String()+".log"))
		if err != nil {
			return nil, err
		}
		return stampedLog{timestamped: &timestamped{w: f}, Closer: f}, nil
	}
}

// configureIntent is one `air configure`, as its command line resolved it.
type configureIntent struct {
	remote string
	name   string
	origin *string // HERMOD_ORIGIN for the entry; nil writes none
	dryRun bool
}

// runConfigure registers the remote with Air and prints what terminal mode
// needs besides, which Air keeps in its own settings and environment.
func (a app) runConfigure(in configureIntent, out io.Writer) error {
	home, err := a.home()
	if err != nil || !filepath.IsAbs(home) {
		return errors.New("resolve the home directory, where Air keeps acp.json")
	}
	hermod, err := a.executable()
	if err != nil {
		return fmt.Errorf("resolve the hermod binary: %w", err)
	}
	var entryEnv map[string]string
	if in.origin != nil {
		entryEnv = map[string]string{envOrigin: *in.origin}
	}
	var searchPath []string
	if p, _ := a.lookupEnv(envPath); p != "" {
		searchPath = filepath.SplitList(p)
	}
	res, err := a.configure(air.Request{
		Home:           home,
		DataHome:       xdgHome(envDataHome, filepath.Join(".local", "share"), home, a.lookupEnv),
		Remote:         in.remote,
		Name:           in.name,
		Hermod:         hermod,
		Env:            entryEnv,
		SearchPath:     searchPath,
		DryRun:         in.dryRun,
		RemoteVariable: envRemote,
	})
	if err != nil {
		return err
	}
	if in.dryRun {
		doc, err := res.Entry.JSON()
		if err != nil {
			return fmt.Errorf("encode the Air entry: %w", err)
		}
		fmt.Fprintf(out, "Dry run: %s is left as it is. The entry:\n%s", air.ACPPath(home), doc)
	} else {
		fmt.Fprintf(out, "Air agent %q written to %s\n", res.Entry.Name(), air.ACPPath(home))
	}
	standIn := res.StandIn
	if standIn == "" {
		standIn = "(none found: `make install` creates it)"
	}
	fmt.Fprintf(out, "\nFor Air's terminal mode, enter this as Air's Claude executable:\n  %s\n"+
		"and put this line in the environment Air starts with:\n  %s\n", standIn, res.RemoteLine)
	return nil
}

// mutagenOutput hands what each Mutagen command prints to w as well as to
// its reader, which keeps it only for a failure's message. Only Mutagen's
// commands are copied: every other command's streams are either read for an
// answer or, for the worker's stdio, the caller's own.
type mutagenOutput struct {
	ex shell.Executor
	w  io.Writer
}

func (m mutagenOutput) Exec(ctx context.Context, cmd shell.Command) shell.Execution {
	if len(cmd.Args) > 0 && cmd.Args[0] == "mutagen" {
		cmd.Stdout = alsoTo(cmd.Stdout, m.w)
		cmd.Stderr = alsoTo(cmd.Stderr, m.w)
	}
	return m.ex.Exec(ctx, cmd)
}

// alsoTo is reader followed by w; a nil reader, which reads the stream from
// the execution's pipe, is left so.
func alsoTo(reader, w io.Writer) io.Writer {
	if reader == nil {
		return nil
	}
	return io.MultiWriter(reader, w)
}

// agent is Claude's contract for a session in dir.
func (a app) agent(dir, home, remoteConfigDir string) claude.Agent {
	value, _ := a.lookupEnv(envClaudeConfigDir)
	return claude.New(claude.Config{
		Dir:             dir,
		ConfigDir:       claude.ConfigDir(value, home),
		RemoteConfigDir: remoteConfigDir,
		ReadFile:        a.readFile,
	})
}

// workspaceLocal is this workstation as a workspace needs it.
func (a app) workspaceLocal(home string) workspace.Local {
	return workspace.Local{
		Home:   home,
		Locks:  lockDir(home, a.lookupEnv),
		Origin: resolveOrigin(a.lookupEnv, a.hostname),
	}
}

// remoteHome prints the remote user's home and then runs its arguments.
const remoteHome = `printf '%s\n' "$HOME"; exec "$@"`

// readRemote reads, in one call on the remote under its user's login shell
// where a profile sets them, the user's home and Claude's config directory
// there. It only reads, so it runs in a dry run too. The remote's commands
// that change a state run on exec.
func (a app) readRemote(ctx context.Context, host string, exec shell.Executor) (workspace.Remote, string, error) {
	cmd := append([]string{"sh", "-c", remoteHome, "sh"}, claude.ConfigDirCommand()...)
	out, err := shell.Run(ctx, shell.NewLogin(shell.NewSSH(a.local, host)), shell.Command{Args: cmd})
	if err != nil {
		if detail := strings.TrimSpace(out.Stderr); detail != "" {
			err = fmt.Errorf("%w: %s", err, detail)
		}
		return workspace.Remote{}, "", fmt.Errorf("read the home on %s: %w", host, err)
	}
	// A login profile may print lines of its own first, so the answer is the
	// last two.
	lines := strings.Split(strings.TrimSuffix(out.Stdout, "\n"), "\n")
	if len(lines) < 2 {
		return workspace.Remote{}, "", fmt.Errorf("read the home on %s: unexpected answer %q", host, out.Stdout)
	}
	home, configDir := lines[len(lines)-2], lines[len(lines)-1]
	if !path.IsAbs(home) || !path.IsAbs(configDir) {
		return workspace.Remote{}, "", fmt.Errorf("read the home on %s: unexpected answer %q", host, out.Stdout)
	}
	return workspace.Remote{Host: host, Home: home, Exec: exec, Read: a.local}, configDir, nil
}
