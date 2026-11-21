package control

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zerlok/hermod/internal/claude"
	"github.com/zerlok/hermod/internal/git"
	"github.com/zerlok/hermod/internal/hook"
	"github.com/zerlok/hermod/internal/mirror"
	"github.com/zerlok/hermod/internal/pathmap"
	"github.com/zerlok/hermod/internal/sandbox"
	"github.com/zerlok/hermod/internal/secret"
	"github.com/zerlok/hermod/internal/shell"
)

// ErrRunningOutside is returned for a session that a Claude Hermod did not start
// runs on the sandbox: starting it again would run two copies of one session.
var ErrRunningOutside = errors.New("the session runs on the sandbox outside Hermod")

// ErrNothingToResume is returned for a `--resume` of a session that neither
// Claude nor any transcript knows.
var ErrNothingToResume = errors.New("no running session and no transcript to resume")

// hookPortCandidates is how many sandbox ports a new session offers the sandbox
// to pick its hook port from, the first one free winning.
const hookPortCandidates = 16

// Hook ports are picked below Linux's default ephemeral range (32768–60999),
// where outgoing connections would otherwise take them.
const (
	hookPortMin = 10000
	hookPortMax = 32767
)

// proxyStopTimeout bounds how long hooks in flight may finish once the attach
// has ended.
const proxyStopTimeout = 2 * time.Second

// agentRun is one run of an agent session, a terminal or a chat session: what
// it was given and what it learns on the way.
type agentRun struct {
	o         Options
	call      claude.Call
	id        claude.SessionID
	mode      sandbox.Mode
	runtime   sandbox.Runtime
	placement mirror.Placement // where the session's directory lies among the sandbox's mirrored roots
	agent     *sandbox.Agent
	mirrors   *runMirrors
	logger    *log.Logger
	random    io.Reader  // tokens and hook port candidates; crypto/rand.Reader outside tests
	window    exitWindow // a chat run's time to exit; sdkWindow outside tests
}

// runAgent runs an agent session: place its directory among the sandbox's
// mirrored roots, open and flush the project mirror, ask the sandbox what state
// the session is in, open the transcript mirror, start the session unless it
// runs, attach to it with its hooks forwarded to Air, a terminal session on a
// tty and a chat session as a stream, and settle the mirrors on detach.
func runAgent(ctx context.Context, o Options, effective, real shell.Shell, call claude.Call, random io.Reader) error {
	return runAgentWithin(ctx, o, effective, real, call, random, sdkWindow)
}

// runAgentWithin is runAgent with the window a chat run exits in.
func runAgentWithin(ctx context.Context, o Options, effective, real shell.Shell, call claude.Call, random io.Reader, window exitWindow) (err error) {
	id, _ := call.Session()
	o, logger, finish := sessionLogger(o, id)
	defer func() { finish(err) }()
	mode := modeOf(o.StandIn, call.Kind())
	runtime := runtimeOf(call.Kind())
	t := agentRun{
		o:       o,
		call:    call,
		id:      id,
		mode:    mode,
		runtime: runtime,
		logger:  logger,
		random:  random,
		window:  window,
	}
	// The settings are read before anything starts, so a missing file fails the
	// run with nothing to undo.
	settings, err := t.readSettings()
	if err != nil {
		return err
	}
	mcp, err := t.readMCPConfigs()
	if err != nil {
		return err
	}
	identity := git.New(real, o.LocalDir).Read(ctx)
	logger.Printf("git identity: %s", describe(identity))
	logger.Printf("origin: %s", describeOrigin(o.Origin))

	t.placement, err = place(ctx, o, real)
	if err != nil {
		return err
	}
	t.agent = sandbox.NewAgent(effective, real, sandbox.AgentConfig{
		Host:    o.Sandbox,
		ID:      id,
		Dir:     t.placement.Dir,
		Tags:    sandbox.Tags{Mode: mode, Origin: o.Origin, Dir: t.placement.Dir},
		Runtime: runtime,
	})
	t.mirrors = newRunMirrors(o, effective, real, logger)
	defer t.mirrors.close()
	session, err := t.mirrors.open(ctx, projectMirror(o.Sandbox, t.placement), t.placement.Root.Remote)
	if err != nil {
		return err
	}
	// Settling outlives a cancelled ctx: see attach.
	settle := context.WithoutCancel(ctx)
	// However the run ends, its mirrors are settled by their roots' liveness,
	// as a detach settles them: liveness covers every session under a root, so a
	// run that started or attached nothing still keeps the mirror of any other
	// session and frees one nobody uses. Only a failed hang-up pauses them.
	if err := session.Flush(ctx); err != nil {
		_ = t.settle(settle, byLiveness)
		return fmt.Errorf("flush mirror: %w", err)
	}
	rule, runErr := t.run(ctx, identity, settings, mcp)
	return errors.Join(runErr, t.settle(settle, rule))
}

// settle settles the mirrors once the run is over, by rule. A chat run's caller kills it at
// the end of its exit window, so a chat settles within the window's settle
// time, whatever ssh or Mutagen do.
func (t agentRun) settle(ctx context.Context, rule settleRule) error {
	if t.runtime != sandbox.RuntimeChat {
		return t.mirrors.settle(ctx, rule)
	}
	return settleWithin(ctx, t.window, t.mirrors, rule, t.logger)
}

// settleWithin settles the mirrors with the window's settle time less its
// pause reserve. When that has not finished by then (a liveness check over an
// unreachable connection, a slow flush), the mirrors still unsettled are paused
// in the reserve instead: a pause keeps every unsynced edit, where a mirror
// left mid-settle at the caller's SIGKILL is neither paused nor closed.
func settleWithin(ctx context.Context, w exitWindow, mirrors *runMirrors, rule settleRule, logger *log.Logger) error {
	limit := w.settle - w.pauseReserve
	bounded, cancel := context.WithTimeout(ctx, limit)
	err := mirrors.settle(bounded, rule)
	late := errors.Is(bounded.Err(), context.DeadlineExceeded)
	cancel()
	if err == nil || !late {
		return err
	}
	logger.Printf("settling the mirror took over %v (%v) → pausing it", limit, err)
	paused, cancel := context.WithTimeout(ctx, w.pauseReserve)
	defer cancel()
	return mirrors.pauseUnsettled(paused)
}

// pauseMirror settles a mirror when what the run did, given by reason, says
// nothing about the other sessions that may be using it: it is paused, never
// terminated.
func pauseMirror(ctx context.Context, name string, session mirror.Session, reason string, logger *log.Logger) error {
	logger.Printf("mirror %s: %s → pausing it", name, reason)
	return session.Pause(ctx)
}

// run starts the session unless it runs, then attaches to it, and returns the
// rule its mirrors are settled by: their roots' liveness, unless a hang-up of
// the running Claude failed, which liveness, read from tmux, would not show.
func (t agentRun) run(ctx context.Context, identity git.Identity, settings []byte, mcp [][]byte) (settleRule, error) {
	// A fork resumes another session than the one it is keyed by: the probe
	// looks for that one's transcript too.
	var others []claude.SessionID
	source, forks := t.call.Resumed()
	forks = forks && source != t.id
	if forks {
		others = append(others, source)
	}
	probe, err := t.agent.Probe(ctx, others...)
	if err != nil {
		return byLiveness, err
	}
	roots, err := runRoots(t.placement, probe.Home)
	if err != nil {
		return byLiveness, err
	}
	localTranscript := claude.TranscriptPath(t.o.ClaudeConfigDir, t.o.LocalDir, t.id)
	hasLocal := t.o.ClaudeConfigDir != "" && isFile(localTranscript)
	state := probe.State(hasLocal)
	t.logger.Printf("session %s: %s", t.id, state)
	if state == sandbox.AgentRunning && t.runtime == sandbox.RuntimeChat {
		// TODO: attach to the running Claude instead, once the SDK is known to
		// accept a locally answered initialize; until then a reopened chat costs
		// at most the turn in flight.
		t.logger.Printf("no live reattach to a running chat session: closing %s to resume it", t.agent.TmuxName())
		if err := t.agent.Close(ctx); err != nil {
			return afterFailedHangUp, err
		}
		probe = probe.Closed()
		state = probe.State(hasLocal)
		t.logger.Printf("session %s: %s", t.id, state)
	}

	switch {
	case state == sandbox.AgentRunningOutside:
		return byLiveness, fmt.Errorf("claude %s %s: %w; end it there first", t.call.Kind(), t.id, ErrRunningOutside)
	case state == sandbox.AgentNew && t.call.Resumes():
		return byLiveness, fmt.Errorf("claude --resume %s: %w", t.id, ErrNothingToResume)
	}
	if err := t.openTranscripts(ctx, probe); err != nil {
		return byLiveness, err
	}

	var (
		port  int
		token secret.Token
	)
	switch state {
	case sandbox.AgentNew:
		if forks {
			if err := t.copyForkSource(ctx, probe, source); err != nil {
				return byLiveness, err
			}
		}
		port, token, err = t.start(ctx, probe, roots, identity, settings, mcp, false)
	case sandbox.AgentClosed:
		if !probe.SandboxTranscript {
			if err := t.copyTranscript(ctx, localTranscript, t.agent.TranscriptPath(probe)); err != nil {
				return byLiveness, err
			}
		}
		port, token, err = t.start(ctx, probe, roots, identity, settings, mcp, true)
	case sandbox.AgentRunning:
		port, token = probe.HookPort, probe.Token
	default:
		return byLiveness, fmt.Errorf("unhandled session state %v", state)
	}
	if err != nil {
		return byLiveness, err
	}
	return byLiveness, t.attach(ctx, roots, settings, port, token)
}

// openTranscripts opens the mirror that brings Claude's transcripts of the
// session's directory from the sandbox's Claude config directory to this
// machine's, where Air lists and resumes the sessions. Without a local config
// directory there is nowhere to bring them.
func (t agentRun) openTranscripts(ctx context.Context, probe sandbox.Probe) error {
	if t.o.ClaudeConfigDir == "" {
		t.logger.Printf("no local Claude config directory: transcripts are not mirrored")
		return nil
	}
	_, err := t.mirrors.open(ctx, transcriptMirror(t.o, t.placement.Dir, probe.ConfigDir, t.agent.ProjectDir(probe.Home)), t.placement.Dir)
	return err
}

// transcriptMirror is the transcript mirror of a session directory: from
// Claude's transcript directory of sandboxDir under the sandbox's config
// directory to the one of the local directory under the local config
// directory. Each end is named after its own side's absolute path. dir is the
// session's directory as the mirror names it, home-relative when the sandbox
// path is.
func transcriptMirror(o Options, dir, sandboxConfigDir, sandboxDir string) mirror.Config {
	return mirror.Config{
		Kind:       mirror.Transcripts,
		Host:       o.Sandbox,
		RemotePath: claude.TranscriptDir(sandboxConfigDir, sandboxDir),
		LocalPath:  claude.TranscriptDir(o.ClaudeConfigDir, o.LocalDir),
		SessionDir: dir,
	}
}

// start writes the session's directory and starts its Claude in tmux, returning
// the session's hook port and token.
func (t agentRun) start(ctx context.Context, probe sandbox.Probe, roots pathmap.Roots, identity git.Identity, settings []byte, mcp [][]byte, resume bool) (int, secret.Token, error) {
	if probe.Stale() {
		t.logger.Printf("removing tmux session %s, left without its Claude", t.agent.TmuxName())
		if err := t.agent.RemoveStale(ctx); err != nil {
			return 0, secret.Token{}, err
		}
	}
	candidates, err := portCandidates(t.random)
	if err != nil {
		return 0, secret.Token{}, err
	}
	port, err := t.agent.Prepare(ctx, candidates)
	if err != nil {
		return 0, secret.Token{}, err
	}
	token, err := secret.NewToken(t.random)
	if err != nil {
		return 0, secret.Token{}, err
	}
	files := sandbox.Files{
		Token: token,
		Env:   claude.Environment(t.o.Environ).With(identityEnv(identity)...),
	}
	rewrite := claude.Rewrite{ToSandbox: roots.ToSandbox(), Resume: resume}
	if settings != nil {
		doc, err := claude.SettingsForSandbox(settings, roots.ToSandbox(), claude.HookRoute{Port: port, Token: token})
		if err != nil {
			return 0, secret.Token{}, err
		}
		if dropped := doc.Dropped(); len(dropped) > 0 {
			t.logger.Printf("hooks to another local endpoint than Air's, left out on the sandbox: %s", strings.Join(dropped, ", "))
		}
		files.Settings = doc.Doc()
		rewrite.Settings = sandbox.SessionDir(probe.Home, t.id) + "/settings.json"
	}
	if len(mcp) > 0 {
		doc, removed, err := claude.MCPForSandbox(mcp, roots.ToSandbox())
		if err != nil {
			return 0, secret.Token{}, err
		}
		if len(removed) > 0 {
			t.logger.Printf("MCP servers that run local programs, left out on the sandbox: %s", strings.Join(removed, ", "))
		}
		if doc != nil {
			files.MCP = doc
			rewrite.MCPConfig = sandbox.SessionDir(probe.Home, t.id) + "/mcp.json"
		}
	}
	if err := t.agent.WriteFiles(ctx, files); err != nil {
		return 0, secret.Token{}, err
	}
	if err := t.agent.Start(ctx, t.call.ForSandbox(rewrite), t.hermodEnv()); err != nil {
		return 0, secret.Token{}, err
	}
	t.logger.Printf("started %s on hook port %d", t.agent.TmuxName(), port)
	return port, token, nil
}

// attach joins the session, with its hooks forwarded to Air when the
// caller's settings carry Air's. A forward that cannot be set up costs the
// hooks, not the session.
func (t agentRun) attach(ctx context.Context, roots pathmap.Roots, settings []byte, port int, token secret.Token) error {
	fwd, stop := t.hookForward(roots, settings, port, token)
	defer stop()
	var err error
	switch t.runtime {
	case sandbox.RuntimeTerminal:
		err = t.agent.AttachTerminal(ctx, fwd, t.o.Stdio)
	case sandbox.RuntimeChat:
		err = t.attachChat(ctx, roots, fwd)
	default:
		return fmt.Errorf("unhandled runtime %v", t.runtime)
	}
	switch {
	case err == nil:
	case errors.Is(err, sandbox.ErrSessionGone):
		return fmt.Errorf("claude %s %s: %w", t.call.Kind(), t.id, err)
	default:
		t.logger.Printf("attach ended with error: %v", err)
	}
	return nil
}

// hookForward starts the hook proxy and returns the forward to it, or nil when
// there are no hooks to forward; stop ends the proxy.
func (t agentRun) hookForward(roots pathmap.Roots, settings []byte, port int, token secret.Token) (*sandbox.Forward, func()) {
	none := func() {}
	if settings == nil {
		return nil, none
	}
	doc, err := claude.SettingsForSandbox(settings, roots.ToSandbox(), claude.HookRoute{Port: port, Token: token})
	if err != nil {
		t.logger.Printf("no hooks: %v", err)
		return nil, none
	}
	target, ok := doc.Hooks()
	switch {
	case !ok:
		return nil, none
	case t.o.DryRun:
		t.logger.Printf("dry run: no hook proxy")
		return nil, none
	case port == 0 || token.IsZero():
		t.logger.Printf("no hooks: the session directory records no hook port or token")
		return nil, none
	}
	proxy, err := hook.Start(hook.NewHandler(hook.Config{
		Session:       token,
		Target:        target.Endpoint,
		Routes:        target.Routes,
		Authorization: target.Authorization,
		ToLocal:       roots.ToLocal(),
		ToSandbox:     roots.ToSandbox(),
		Log:           t.logger,
	}))
	if err != nil {
		t.logger.Printf("no hooks: %v", err)
		return nil, none
	}
	t.logger.Printf("hooks: sandbox port %d → proxy 127.0.0.1:%d → %s", port, proxy.Port(), target.Endpoint.Redacted())
	fwd := &sandbox.Forward{
		SandboxPort: port,
		LocalPort:   proxy.Port(),
		OnFailure: func() {
			t.logger.Printf("hooks unavailable: sandbox port %d is taken; the session runs without them, and the next attach tries the port again", port)
		},
	}
	stop := func() {
		ctx, cancel := context.WithTimeout(context.Background(), proxyStopTimeout)
		defer cancel()
		if err := proxy.Close(ctx); err != nil {
			t.logger.Printf("hook proxy: %v", err)
		}
	}
	return fwd, stop
}

// readSettings reads the caller's `--settings`, inline JSON or a file; a
// relative path is under the working directory. It is nil when the call has
// none.
func (t agentRun) readSettings() ([]byte, error) {
	value, ok := t.call.Settings()
	if !ok {
		return nil, nil
	}
	if claude.IsInlineJSON(value) {
		return []byte(value), nil
	}
	if !filepath.IsAbs(value) {
		value = filepath.Join(t.o.LocalDir, value)
	}
	b, err := os.ReadFile(value)
	if err != nil {
		return nil, fmt.Errorf("read --settings: %w", err)
	}
	return b, nil
}

// readMCPConfigs reads the caller's `--mcp-config` values, each inline JSON or
// a file; a relative path is under the working directory. Claude on the
// sandbox gets them merged into one file of the session's, so a file outside
// the mirror and a secret inside a config both stay off its command line.
func (t agentRun) readMCPConfigs() ([][]byte, error) {
	var docs [][]byte
	for _, value := range t.call.MCPConfigs() {
		if claude.IsInlineJSON(value) {
			docs = append(docs, []byte(value))
			continue
		}
		if !filepath.IsAbs(value) {
			value = filepath.Join(t.o.LocalDir, value)
		}
		b, err := os.ReadFile(value)
		if err != nil {
			return nil, fmt.Errorf("read --mcp-config: %w", err)
		}
		docs = append(docs, b)
	}
	return docs, nil
}

// copyForkSource copies up the transcript of the session a new fork starts
// from when only this machine has it, so Claude on the sandbox can resume it.
// Neither side having it is left to Claude, which reports it.
func (t agentRun) copyForkSource(ctx context.Context, probe sandbox.Probe, source claude.SessionID) error {
	remote := t.agent.TranscriptPathOf(probe, source)
	if probe.HasTranscript(remote) || t.o.ClaudeConfigDir == "" {
		return nil
	}
	local := claude.TranscriptPath(t.o.ClaudeConfigDir, t.o.LocalDir, source)
	if !isFile(local) {
		return nil
	}
	return t.copyTranscript(ctx, local, remote)
}

// copyTranscript copies a transcript only this machine has to the sandbox, so
// Claude there can resume the session.
func (t agentRun) copyTranscript(ctx context.Context, local, remote string) error {
	f, err := os.Open(local)
	if err != nil {
		return fmt.Errorf("copy transcript: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("copy transcript: %w", err)
	}
	t.logger.Printf("copying transcript %s to the sandbox", local)
	return t.agent.CopyTranscript(ctx, remote, f, info.Size())
}

// hermodEnv is what Claude on the sandbox carries to say Hermod started it, for
// whom and from where. None of it is secret, so it rides on tmux's command line.
func (t agentRun) hermodEnv() []string {
	env := []string{sandbox.EnvSession + "=" + t.id.String(), sandbox.EnvMode + "=" + t.mode.String()}
	if t.o.Origin != "" {
		env = append(env, EnvOrigin+"="+t.o.Origin)
	}
	return env
}

// modeOf is who drives a session: the stand-in runs for Air, a chat through the
// ACP adapter and a terminal session in Air's terminal tab; anything else is a
// person's own attach.
func modeOf(standIn bool, kind claude.Kind) sandbox.Mode {
	switch {
	case standIn && kind == claude.ChatSession:
		return sandbox.ModeAirACP
	case standIn && kind == claude.TerminalSession:
		return sandbox.ModeAirTerminal
	default:
		return sandbox.ModeAttach
	}
}

// runtimeOf is how a session of the kind runs on the sandbox.
func runtimeOf(kind claude.Kind) sandbox.Runtime {
	if kind == claude.ChatSession {
		return sandbox.RuntimeChat
	}
	return sandbox.RuntimeTerminal
}

// portCandidates draws the hook ports a new session offers the sandbox.
func portCandidates(random io.Reader) ([]int, error) {
	raw := make([]byte, 2*hookPortCandidates)
	if _, err := io.ReadFull(random, raw); err != nil {
		return nil, fmt.Errorf("pick hook ports: %w", err)
	}
	ports := make([]int, hookPortCandidates)
	for i := range ports {
		ports[i] = hookPortMin + int(binary.BigEndian.Uint16(raw[2*i:]))%(hookPortMax-hookPortMin+1)
	}
	return ports, nil
}

// sessionLogger is the run's step logger. Under the stand-in it also writes the
// session's log, where Air's runs leave their trace: so does every other line
// of Hermod's own on stderr, warnings and the output of its setup commands
// included, which is why it returns the options with Stdio.Err writing to the
// log too. A log that cannot be opened is reported on stderr and the run goes
// on. finish records the run's error in the log, stderr getting it from the
// caller, and closes the log.
func sessionLogger(o Options, id claude.SessionID) (out Options, logger *log.Logger, finish func(error)) {
	if o.SessionLogDir == "" {
		return o, newLogger(o.Stdio.Err, o.Quiet), func(error) {}
	}
	f, err := openSessionLog(o.SessionLogDir, id)
	if err != nil {
		fmt.Fprintf(o.Stdio.Err, "hermod: %v\n", err)
		return o, newLogger(o.Stdio.Err, o.Quiet), func(error) {}
	}
	file := &timestamped{w: f}
	steps := io.Writer(file)
	if !o.Quiet {
		steps = logTee{log: file, err: o.Stdio.Err}
	}
	o.Stdio.Err = logTee{log: file, err: o.Stdio.Err}
	return o, log.New(steps, "", 0), func(err error) {
		if err != nil {
			fmt.Fprintf(file, "hermod: %v\n", err)
		}
		_ = f.Close()
	}
}

// logTee writes Hermod's own output to the session log and then to stderr. A
// write never fails: the log comes first so a stderr whose reader has gone
// costs no line of it, and a failed write would otherwise fail what printed it,
// such as a Mutagen command whose output it carries.
type logTee struct {
	log io.Writer
	err io.Writer
}

func (t logTee) Write(p []byte) (int, error) {
	_, _ = t.log.Write(p)
	if t.err != nil {
		_, _ = t.err.Write(p)
	}
	return len(p), nil
}

// timestamped prefixes each line written to the session log with the time, as
// the log of a long-lived session needs and stderr does not. A line may come in
// several writes, as a command's output does: only its start is stamped.
type timestamped struct {
	w       io.Writer
	midLine bool // the last write ended inside a line
}

func (t *timestamped) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		if !t.midLine {
			if _, err := io.WriteString(t.w, time.Now().Format("2006/01/02 15:04:05 ")); err != nil {
				return written, err
			}
		}
		line := p
		if i := bytes.IndexByte(p, '\n'); i >= 0 {
			line = p[:i+1]
		}
		n, err := t.w.Write(line)
		written += n
		if err != nil {
			return written, err
		}
		t.midLine = line[len(line)-1] != '\n'
		p = p[len(line):]
	}
	return written, nil
}

func isFile(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.Mode().IsRegular()
}
