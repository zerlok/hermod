package sandbox

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"

	"github.com/zerlok/hermod/internal/claude"
	"github.com/zerlok/hermod/internal/pathmap"
	"github.com/zerlok/hermod/internal/secret"
	"github.com/zerlok/hermod/internal/shell"
)

// Mode says who drives a session.
type Mode int

const (
	// ModeAttach is a person's own terminal, through `hermod attach`. It is the
	// zero value: a session is taken for Air's only when the stand-in says so.
	ModeAttach Mode = iota
	// ModeAirTerminal is Air's terminal tab, through the stand-in.
	ModeAirTerminal
	// ModeAirACP is an Air chat, through the ACP adapter and the stand-in.
	ModeAirACP
)

func (m Mode) String() string {
	switch m {
	case ModeAttach:
		return "attach"
	case ModeAirTerminal:
		return "air-terminal"
	case ModeAirACP:
		return "air-acp"
	}
	return "unknown"
}

// The environment Hermod gives a Claude it starts on the sandbox, beside the
// origin, so the Claude says it is Hermod's, whose session it runs and for whom.
const (
	EnvSession = "HERMOD_SESSION" // the session's UUID
	EnvMode    = "HERMOD_MODE"    // the session's Mode
)

// Tags say where a tmux session came from, so someone looking at the sandbox can
// tell a session Hermod runs, and for whom, from one started there by hand.
type Tags struct {
	Mode   Mode
	Origin string // the machine it was started from; "" leaves the origin out
	Dir    string // the session's working directory on the sandbox
}

// options are the tags as tmux session options.
func (t Tags) options() []shell.SessionOption {
	opts := []shell.SessionOption{{Name: "@hermod-mode", Value: t.Mode.String()}}
	if t.Origin != "" {
		opts = append(opts, shell.SessionOption{Name: "@hermod-origin", Value: t.Origin})
	}
	if t.Dir != "" {
		opts = append(opts, shell.SessionOption{Name: dirTag, Value: t.Dir})
	}
	return opts
}

// Runtime is how a running Claude is connected to whoever attaches.
type Runtime int

const (
	// RuntimeTerminal runs Claude's terminal UI on tmux's pane, joined with
	// `tmux attach-session`.
	RuntimeTerminal Runtime = iota
	// RuntimeChat runs Claude's stream-json on files in the session directory,
	// since a pane would alter the bytes and an attach replays a screen: Claude
	// reads stdin from in.fifo, which it holds open itself so it never sees EOF,
	// and appends stdout to out.log.
	RuntimeChat
)

func (r Runtime) String() string {
	switch r {
	case RuntimeTerminal:
		return "terminal"
	case RuntimeChat:
		return "chat"
	}
	return "unknown"
}

// AgentState is what the sandbox says about an agent session.
type AgentState int

const (
	// AgentNew has neither a running Claude nor a transcript: start it.
	AgentNew AgentState = iota
	// AgentClosed has a transcript and no running Claude: start it with --resume.
	AgentClosed
	// AgentRunning runs in Hermod's tmux session: attach to it.
	AgentRunning
	// AgentRunningOutside runs in a Claude that Hermod did not start, so a start
	// would be a second copy of the session.
	AgentRunningOutside
)

func (s AgentState) String() string {
	switch s {
	case AgentNew:
		return "new"
	case AgentClosed:
		return "closed"
	case AgentRunning:
		return "running"
	case AgentRunningOutside:
		return "running outside Hermod"
	}
	return "unknown"
}

// Probe is what one look at the sandbox found for an agent session.
type Probe struct {
	Listed            bool         // `claude agents --json` lists the session: Claude runs it
	Tmux              bool         // tmux session hermod-<uuid> exists
	SandboxTranscript bool         // the transcript is on the sandbox, under the session's directory
	Transcripts       []string     // every transcript found of the sessions asked about, wherever it lies
	Home              string       // the sandbox user's home
	ConfigDir         string       // Claude's config directory on the sandbox
	HookPort          int          // the session's recorded hook port; 0 when none
	Token             secret.Token // the session's recorded token; zero when none
}

// State reads the probe by Claude's word, the transcript counting wherever it
// is: localTranscript says whether this machine has one.
func (p Probe) State(localTranscript bool) AgentState {
	switch {
	case p.Listed && p.Tmux:
		return AgentRunning
	case p.Listed:
		return AgentRunningOutside
	case p.SandboxTranscript || localTranscript:
		return AgentClosed
	default:
		return AgentNew
	}
}

// Stale reports a tmux session left without its Claude, to remove before a
// start.
func (p Probe) Stale() bool { return p.Tmux && !p.Listed }

// Closed is the probe as it reads once the session has been closed: no Claude
// runs it and no tmux session holds it; its transcripts are where they were.
func (p Probe) Closed() Probe {
	p.Listed, p.Tmux = false, false
	return p
}

// AgentConfig describes an agent session.
type AgentConfig struct {
	Host string
	ID   claude.SessionID
	Dir  string // the project's directory on the sandbox, where Claude runs; home-relative or absolute
	Tags Tags
	// Runtime is how Claude is started and attached.
	Runtime Runtime
}

// Agent is a Claude session on the sandbox in tmux session `hermod-<uuid>`, with
// its directory `~/.hermod/s/<uuid>/` holding what the running Claude needs:
// `hookport`, `token`, `env`, `settings.json` and `mcp.json`, and for a chat
// session `in.fifo` and `out.log`, and the `pid` of the Claude last started. Whether it runs is Claude's to say; the
// directory decides nothing.
type Agent struct {
	cfg   AgentConfig
	exec  shell.Shell // side effects, dry-run-aware
	probe shell.Shell // read-only probes, always real
}

// NewAgent prepares an agent session; it runs nothing.
func NewAgent(exec, probe shell.Shell, cfg AgentConfig) *Agent {
	return &Agent{cfg: cfg, exec: exec, probe: probe}
}

// The layout of an agent session on the sandbox, from which both the Go code
// and the shell scripts run there take it (see layout).
const (
	sessionsDir = ".hermod/s" // the session directories, under the sandbox user's home
	tmuxPrefix  = "hermod-"   // starts an agent session's tmux session name, which the UUID ends
	dirTag      = "@hermod-dir"
)

// layout writes the session layout into a script run on the sandbox, whose
// first argument is the session's UUID: @SESSIONS@ is the directory of every
// session's directory, @DIR@ this session's, @TMUX@ its tmux session name,
// @DIRTAG@ the directory tag, @SESSIONENV@ the variable that names the session
// in its Claude's environment, and @LINGERS@ and @GONE@ the exit statuses its
// caller reads back.
func layout(script string) string {
	return strings.NewReplacer(
		"@SESSIONS@", "$HOME/"+sessionsDir,
		"@DIR@", "$HOME/"+sessionsDir+"/$1",
		"@TMUX@", tmuxPrefix+"$1",
		"@DIRTAG@", dirTag,
		"@LINGERS@", strconv.Itoa(closeLingersStatus),
		"@GONE@", strconv.Itoa(chatGoneStatus),
		"@SESSIONENV@", EnvSession,
	).Replace(script)
}

// TmuxName is the session's tmux session name.
func (a *Agent) TmuxName() string { return tmuxPrefix + a.cfg.ID.String() }

// SessionDir is the session's directory on the sandbox, under home.
func SessionDir(home string, id claude.SessionID) string {
	return path.Join(home, sessionsDir, id.String())
}

// ProjectDir is the project's absolute directory on the sandbox, under home.
func (a *Agent) ProjectDir(home string) string { return pathmap.Absolute(home, a.cfg.Dir) }

// probeScript reports, one `key=value` per line, the sandbox's home and Claude
// config directory, whether the tmux session exists, the transcripts Claude has
// of the session and of any other session given after it, and the session
// directory's port and token; after an `agents:` line comes Claude's list of
// running sessions. It runs in a login shell, where `claude` and a
// CLAUDE_CONFIG_DIR set in the profile are found.
//
// A Claude Hermod started whose tmux session is gone is being hung up on, by a
// close that may have begun just before this probe: the probe gives it the
// close's 5 s to exit, so a chat reopened while it was being closed is not
// taken for one running outside Hermod.
var probeScript = layout(`c="${CLAUDE_CONFIG_DIR:-$HOME/.claude}"
d="@DIR@"
printf 'home=%s\nconfig=%s\n' "$HOME" "$c"
` + ourClaude + `
if tmux has-session -t "=@TMUX@" 2>/dev/null; then echo tmux=yes; else
  echo tmux=no
  i=0; while ours && [ "$i" -lt 50 ]; do sleep 0.1; i=$((i + 1)); done
fi
for id in "$@"; do
  for f in "$c"/projects/*/"$id".jsonl; do if [ -e "$f" ]; then printf 'transcript=%s\n' "$f"; fi; done
done
if [ -r "$d/hookport" ]; then printf 'hookport=%s\n' "$(cat "$d/hookport")"; fi
if [ -r "$d/token" ]; then printf 'token=%s\n' "$(cat "$d/token")"; fi
echo agents:
exec claude agents --json`)

// Probe asks the sandbox about the session in one ssh call, and also looks for
// the transcripts of others, such as the session a fork starts from. It is
// read-only.
func (a *Agent) Probe(ctx context.Context, others ...claude.SessionID) (Probe, error) {
	argv := []string{"sh", "-c", probeScript, "sh", a.cfg.ID.String()}
	for _, id := range others {
		argv = append(argv, id.String())
	}
	res, err := shell.NewLoginShell(shell.NewSSH(a.probe, a.cfg.Host)).Run(ctx, shell.Command{
		Argv:    argv,
		Capture: true,
	})
	if err != nil {
		return Probe{}, fmt.Errorf("probe session %s on %s: %w%s", a.cfg.ID, a.cfg.Host, err, detail(res.Stderr))
	}
	return a.parseProbe(res.Stdout)
}

func (a *Agent) parseProbe(out string) (Probe, error) {
	head, agents, found := strings.Cut(out, "agents:\n")
	if !found {
		return Probe{}, fmt.Errorf("probe session %s on %s: unexpected output", a.cfg.ID, a.cfg.Host)
	}
	var p Probe
	sc := bufio.NewScanner(strings.NewReader(head))
	for sc.Scan() {
		key, value, _ := strings.Cut(sc.Text(), "=")
		switch key {
		case "home":
			p.Home = value
		case "config":
			p.ConfigDir = value
		case "tmux":
			p.Tmux = value == "yes"
		case "transcript":
			p.Transcripts = append(p.Transcripts, value)
		case "hookport":
			p.HookPort, _ = strconv.Atoi(strings.TrimSpace(value)) // unreadable: none recorded
		case "token":
			p.Token = secret.FromString(strings.TrimSpace(value))
		}
	}
	if !path.IsAbs(p.Home) || !path.IsAbs(p.ConfigDir) {
		return Probe{}, fmt.Errorf("probe session %s on %s: no home or Claude config directory reported", a.cfg.ID, a.cfg.Host)
	}
	listed, err := claude.Running([]byte(agents), a.cfg.ID)
	if err != nil {
		return Probe{}, fmt.Errorf("probe session %s on %s: %w", a.cfg.ID, a.cfg.Host, err)
	}
	p.Listed = listed
	p.SandboxTranscript = p.HasTranscript(a.TranscriptPath(p))
	return p, nil
}

// HasTranscript reports whether the probe found the transcript at path.
func (p Probe) HasTranscript(path string) bool {
	for _, t := range p.Transcripts {
		if t == path {
			return true
		}
	}
	return false
}

// TranscriptPath is where Claude on the sandbox keeps the session's transcript.
func (a *Agent) TranscriptPath(p Probe) string { return a.TranscriptPathOf(p, a.cfg.ID) }

// TranscriptPathOf is where Claude on the sandbox keeps the transcript of a
// session run in this session's directory, such as the one a fork starts from.
func (a *Agent) TranscriptPathOf(p Probe, id claude.SessionID) string {
	return claude.TranscriptPath(p.ConfigDir, a.ProjectDir(p.Home), id)
}

// IsActive reports whether the session's tmux session exists. It is read-only;
// a failure other than its absence is returned for the caller to choose a safe
// default.
func (a *Agent) IsActive(ctx context.Context) (bool, error) {
	_, err := shell.NewSSH(a.probe, a.cfg.Host).Run(ctx, shell.Command{
		Argv:    []string{"tmux", "has-session", "-t", "=" + a.TmuxName()},
		Capture: true,
	})
	if err == nil {
		return true, nil
	}
	if code, ok := shell.ExitCode(err); ok && code == 1 {
		return false, nil
	}
	return false, err
}

// RemoveStale kills the session's tmux session, one left without its Claude.
func (a *Agent) RemoveStale(ctx context.Context) error {
	return a.setup(ctx, "remove stale tmux session", nil,
		"tmux", "kill-session", "-t", "="+a.TmuxName())
}

// copyScript writes stdin to the file $1 by way of $1.part, renamed into place
// only once it holds the $2 bytes sent. cat ends with status 0 on an input cut
// short, by a dropped connection or a cancelled copy, and a transcript under its
// own name is taken as whole by every later run, which resumes from it and never
// copies it again; so a short copy leaves nothing under that name.
const copyScript = `umask 077 && mkdir -p "$(dirname "$1")" && cat > "$1.part" &&
if [ "$(wc -c < "$1.part")" -eq "$2" ]; then mv -f "$1.part" "$1"; else rm -f "$1.part"; echo "copied short of $2 bytes" >&2; exit 1; fi`

// CopyTranscript writes a transcript of size bytes to dst on the sandbox, a file
// only this machine has, so Claude there can resume the session. Exactly size
// bytes of it are sent; dst exists only once they all arrived.
func (a *Agent) CopyTranscript(ctx context.Context, dst string, transcript io.Reader, size int64) error {
	return a.setup(ctx, "copy transcript", io.LimitReader(transcript, size),
		"sh", "-c", copyScript, "sh", dst, strconv.FormatInt(size, 10))
}

// prepareScript creates the session directory, private to the sandbox user,
// and records a hook port in it unless one is recorded already: the first
// candidate that no other session has recorded and no socket on the sandbox
// uses, read from /proc/net (Linux). A detached session's port is bound by
// nothing, so only its record keeps it from a new session. It prints the port.
var prepareScript = layout(`set -e
umask 077
d="@DIR@"; shift
mkdir -p "$d"; chmod 700 "$d"; cd "$d"
if ! [ -s hookport ]; then
  for p in "$@"; do
    if cat "@SESSIONS@"/*/hookport 2>/dev/null | grep -qx "$p"; then continue; fi
    if ! cat /proc/net/tcp /proc/net/tcp6 2>/dev/null | grep -q ":$(printf '%04X' "$p") "; then
      echo "$p" > hookport; break
    fi
  done
fi
cat hookport`)

// Prepare creates the session directory and returns its hook port, recorded on
// an earlier start or picked now from candidates. Under a dry run, which prints
// the command instead, the port is 0.
func (a *Agent) Prepare(ctx context.Context, candidates []int) (int, error) {
	argv := []string{"sh", "-c", prepareScript, "sh", a.cfg.ID.String()}
	for _, p := range candidates {
		argv = append(argv, strconv.Itoa(p))
	}
	res, err := shell.NewSSH(a.exec, a.cfg.Host).Run(ctx, shell.Command{Argv: argv, Capture: true})
	if err != nil {
		return 0, fmt.Errorf("prepare session directory on %s: %w%s", a.cfg.Host, err, detail(res.Stderr))
	}
	out := strings.TrimSpace(res.Stdout)
	if out == "" {
		return 0, nil
	}
	port, err := strconv.Atoi(out)
	if err != nil || port <= 0 || port > 65535 {
		return 0, fmt.Errorf("prepare session directory on %s: no hook port: %q", a.cfg.Host, out)
	}
	return port, nil
}

// Files are what a start writes into the session directory. Each may hold a
// secret, so they travel on ssh stdin, never on a command line.
type Files struct {
	Token    secret.Token
	Env      secret.Env // written as a file the start sources
	Settings []byte     // settings.json
	MCP      []byte     // mcp.json, the MCP servers' config
}

// writeScript splits stdin into the token, env, settings.json and mcp.json by
// the lengths it is given, inside the private session directory.
var writeScript = layout(`set -e
umask 077
cd "@DIR@"
cat > .incoming
head -c "$2" .incoming > token
tail -c +"$(($2 + 1))" .incoming | head -c "$3" > env
tail -c +"$(($2 + $3 + 1))" .incoming | head -c "$4" > settings.json
tail -c +"$(($2 + $3 + $4 + 1))" .incoming > mcp.json
rm -f .incoming`)

// WriteFiles writes the session's token, env, settings.json and mcp.json.
func (a *Agent) WriteFiles(ctx context.Context, f Files) error {
	token := []byte(f.Token.Reveal())
	env := EnvFile(f.Env.Reveal())
	payload := bytes.Join([][]byte{token, env, f.Settings, f.MCP}, nil)
	return a.setup(ctx, "write session files", bytes.NewReader(payload),
		"sh", "-c", writeScript, "sh", a.cfg.ID.String(),
		strconv.Itoa(len(token)), strconv.Itoa(len(env)), strconv.Itoa(len(f.Settings)))
}

// EnvFile renders an environment as lines a POSIX shell sources. An entry whose
// key is not a shell name is left out: no shell could set it.
func EnvFile(env []string) []byte {
	var b bytes.Buffer
	for _, e := range env {
		key, value, ok := strings.Cut(e, "=")
		if !ok || !isShellName(key) {
			continue
		}
		fmt.Fprintf(&b, "export %s=%s\n", key, shell.Quote(value))
	}
	return b.Bytes()
}

// startScript sources the session's env and runs the command in its place, so
// Claude is the process tmux watches, and records its pid, which is Claude's
// once it runs in the script's place, as the session directory's `pid`: the
// pane's own process may be a shell between tmux and Claude, as a login shell
// that does not exec its last command (dash) leaves one. First it asks tmux to keep the pane when
// Claude fails, so the attach shows Claude's error and exit status instead of
// finding no session. It sets that from inside the pane, where it needs no
// target and a tmux older than 3.2, which lacks the `failed` value, refuses it
// without failing the start.
var startScript = layout(`tmux set-option -w remain-on-exit failed 2>/dev/null; echo "$$" > "@DIR@/pid"; . "@DIR@/env" && shift && exec "$@"`)

// chatStartScript is startScript for a chat session: Claude's stdin is in.fifo,
// opened for reading and writing so that Claude itself keeps a writer and never
// sees EOF however often attaches come and go, and its stdout is appended to
// out.log. Its stderr stays on the pane, kept there when Claude fails.
var chatStartScript = layout(`tmux set-option -w remain-on-exit failed 2>/dev/null; d="@DIR@"; echo "$$" > "$d/pid"; . "$d/env" && shift && exec "$@" <>"$d/in.fifo" >>"$d/out.log"`)

// chatFilesScript makes in.fifo, unless it is one already, and empties
// out.log, so the attach that follows a start streams this Claude's output
// from its first line. It runs before tmux does: an attach finds both files
// whenever it comes.
var chatFilesScript = layout(`set -e
umask 077
cd "@DIR@"
if ! [ -p in.fifo ]; then rm -f in.fifo; mkfifo in.fifo; fi
: > out.log`)

// Start starts the session's Claude, detached, in tmux session hermod-<uuid> in
// the project's directory, tagged, running argv after the session's env is
// sourced; env holds what may show on a command line (HERMOD_SESSION and the
// like). It runs under the sandbox user's login shell, which finds claude. A
// chat session's in.fifo and out.log are made ready first.
func (a *Agent) Start(ctx context.Context, argv, env []string) error {
	script := startScript
	if a.cfg.Runtime == RuntimeChat {
		script = chatStartScript
		if err := a.setup(ctx, "prepare chat stream files", nil, "sh", "-c", chatFilesScript, "sh", a.cfg.ID.String()); err != nil {
			return err
		}
	}
	tmux := shell.NewTmux(shell.NewSSH(a.exec, a.cfg.Host), a.TmuxName(),
		shell.Detached(), shell.WithSessionOptions(a.cfg.Tags.options()...))
	res, err := shell.NewLoginShell(tmux).Run(ctx, shell.Command{
		Argv:    append([]string{"sh", "-c", script, "sh", a.cfg.ID.String()}, argv...),
		Dir:     a.cfg.Dir,
		Env:     env,
		Capture: true,
	})
	if err != nil {
		return fmt.Errorf("start session %s on %s: %w%s", a.cfg.ID, a.cfg.Host, err, detail(res.Stderr))
	}
	return nil
}

// ourClaude defines `ours`, which reports whether the pid recorded in the
// session directory $d is a live Claude Hermod started for session $1: its
// environment carries the session's HERMOD_SESSION, so a pid reused by another
// process since is not taken for it. With no pid recorded, by a start before
// pids were, it falls back to the pane's process, which is Claude's when the
// shells between exec it.
var ourClaude = layout(`u="$1"
p=$(cat "$d/pid" 2>/dev/null) || p=$(tmux display-message -p -t "=@TMUX@:" '#{pane_pid}' 2>/dev/null) || p=
ours() { [ -n "$p" ] && kill -0 "$p" 2>/dev/null && tr '\0' '\n' < "/proc/$p/environ" 2>/dev/null | grep -qx "@SESSIONENV@=$u"; }`)

// closeScript ends the session's tmux session, which hangs up on Claude so it
// saves its transcript and exits, and waits up to 5 s for that Claude to be
// gone, so a start that follows never runs beside it. A Claude still there
// after the wait ends the script with closeLingersStatus. It waits on Claude's
// own pid, recorded by the start, not on the pane's process, which may be a
// shell that is gone while Claude still saves its transcript.
var closeScript = layout(`d="@DIR@"
` + ourClaude + `
tmux kill-session -t "=@TMUX@:" 2>/dev/null || true
i=0
while ours && [ "$i" -lt 50 ]; do sleep 0.1; i=$((i + 1)); done
if ours; then exit @LINGERS@; fi`)

// closeLingersStatus is the close script's exit status when Claude outlives
// the wait; ssh reserves 255 for its own failures.
const closeLingersStatus = 4

// ErrClaudeLingers is returned by a close whose Claude did not exit after its
// tmux session ended: a start now would run a second copy of the session.
var ErrClaudeLingers = errors.New("Claude did not exit after its tmux session was closed")

// Close ends the session: its tmux session and its Claude. The session
// directory stays, for a later --resume. A Claude that has not exited 5 s after
// the hang-up is ErrClaudeLingers.
func (a *Agent) Close(ctx context.Context) error {
	err := a.setup(ctx, "close session "+a.cfg.ID.String(), nil, "sh", "-c", closeScript, "sh", a.cfg.ID.String())
	if code, ok := shell.ExitCode(err); ok && code == closeLingersStatus {
		return fmt.Errorf("close session %s on %s: %w", a.cfg.ID, a.cfg.Host, ErrClaudeLingers)
	}
	return err
}

// Tagged is what the sandbox says of an agent session's tmux session: whether it
// exists and, when it does, the directory it is tagged with, which names the
// mirrors that serve it.
type Tagged struct {
	Exists bool
	Home   string // the sandbox user's home, under which a home-relative Dir lies
	Dir    string // the session's @hermod-dir; "" when untagged
}

// taggedScript reports the sandbox's home and whether the session's tmux
// session exists, with its directory tag, then `end`. has-session decides
// existence: display-message prints empty fields for a missing target rather
// than failing.
var taggedScript = layout(`printf 'home=%s\n' "$HOME"
if tmux has-session -t "=@TMUX@" 2>/dev/null; then
  printf 'tmux=yes\ndir=%s\n' "$(tmux display-message -p -t "=@TMUX@:" '#{@DIRTAG@}')"
else
  echo tmux=no
fi
echo end`)

// Tagged looks up the session's tmux session and its directory tag. It is
// read-only.
func (a *Agent) Tagged(ctx context.Context) (Tagged, error) {
	res, err := shell.NewSSH(a.probe, a.cfg.Host).Run(ctx, shell.Command{
		Argv:    []string{"sh", "-c", taggedScript, "sh", a.cfg.ID.String()},
		Capture: true,
	})
	if err != nil {
		return Tagged{}, fmt.Errorf("look up %s on %s: %w%s", a.TmuxName(), a.cfg.Host, err, detail(res.Stderr))
	}
	var (
		t     Tagged
		ended bool
	)
	sc := bufio.NewScanner(strings.NewReader(res.Stdout))
	for sc.Scan() {
		key, value, _ := strings.Cut(sc.Text(), "=")
		switch key {
		case "home":
			t.Home = value
		case "tmux":
			t.Exists = value == "yes"
		case "dir":
			t.Dir = value
		case "end":
			ended = true
		}
	}
	if !ended || !path.IsAbs(t.Home) {
		return Tagged{}, fmt.Errorf("look up %s on %s: incomplete output", a.TmuxName(), a.cfg.Host)
	}
	return t, nil
}

// Forward is the hook forward an attach carries: the session's sandbox port to
// the local proxy's.
type Forward struct {
	SandboxPort int
	LocalPort   int
	// OnFailure is called once if ssh reports that it could not bind the sandbox
	// port. The attach goes on without hooks.
	OnFailure func()
}

// ErrSessionGone is returned by an attach that found no tmux session: Claude
// exited between its start and the attach, so there is nothing to join.
var ErrSessionGone = errors.New("the session's tmux session is gone: Claude exited")

// AttachTerminal joins the session's tmux session on a tty over one ssh
// connection of its own: a forward asked of a shared master connection would
// belong to the master. It blocks until the user detaches. A nil forward
// attaches without one.
//
// It joins only a session that exists: creating one here would give the user a
// shell under the session's name, untagged, in place of Claude. A session gone
// by the time of the attach is ErrSessionGone.
func (a *Agent) AttachTerminal(ctx context.Context, fwd *Forward, streams Streams) error {
	opts := []shell.SSHOption{shell.WithTty(), shell.WithoutSharedMaster()}
	stderr := streams.Err
	if fwd != nil {
		opts = append(opts, shell.WithReverseForward(fwd.SandboxPort, fwd.LocalPort))
		stderr = &forwardWatch{w: orStderr(streams.Err), onFailure: fwd.OnFailure}
	}
	_, err := shell.NewSSH(a.exec, a.cfg.Host, opts...).Run(ctx, shell.Command{
		Argv:   []string{"tmux", "attach-session", "-t", "=" + a.TmuxName()},
		Stdin:  streams.In,
		Stdout: streams.Out,
		Stderr: stderr,
	})
	if err == nil {
		return nil
	}
	if alive, probeErr := a.IsActive(ctx); probeErr == nil && !alive {
		return fmt.Errorf("attach %s on %s: %w", a.TmuxName(), a.cfg.Host, ErrSessionGone)
	}
	return err
}

// attachChatScript is a chat session's attach. It streams out.log from its
// first line to stdout and copies stdin into in.fifo one complete line at a
// time: `read -r` returns the line only once its newline came, so a connection
// dropped mid-line hands Claude no part of it. It ends with status 0 when stdin
// ends, and with chatGoneStatus when there is no Claude to attach to or its
// pane dies while attached, so the caller is not left waiting on a log nobody
// writes. The session itself is never touched.
//
// The stdin copy runs in the background, with stdin given explicitly since a
// background list's stdin is otherwise /dev/null, and signals the shell when
// it ends; the shell polls the pane between waits, which a signal cuts short.
var attachChatScript = layout(`d="@DIR@"
s="=@TMUX@:"
alive() { [ "$(tmux display-message -p -t "$s" '#{pane_dead}' 2>/dev/null)" = 0 ]; }
alive && [ -p "$d/in.fifo" ] && [ -f "$d/out.log" ] || exit @GONE@
trap 'kill "$t" 2>/dev/null; exit 0' USR1
me=$$
exec 3<>"$d/in.fifo" 4<&0
tail -c +1 -f "$d/out.log" 3>&- 4<&- </dev/null & t=$!
{ while IFS= read -r l; do printf '%s\n' "$l"; done >&3; kill -USR1 "$me"; } <&4 &
r=$!
while alive; do sleep 1 >/dev/null 2>&1 </dev/null & wait $!; done
kill "$t" "$r" 2>/dev/null
exit @GONE@`)

// chatGoneStatus is the attach script's exit status when Claude is not there
// to attach to; ssh reserves 255 for its own failures.
const chatGoneStatus = 3

// AttachChat joins a chat session as a stream over one ssh connection of its
// own, without a tty, which would alter the bytes: streams.Out receives what
// Claude writes from the start of its log, and each complete line read from
// streams.In reaches Claude. It blocks until streams.In ends, the connection
// drops or Claude is gone, which is ErrSessionGone. A nil forward attaches
// without one.
func (a *Agent) AttachChat(ctx context.Context, fwd *Forward, streams Streams) error {
	opts := []shell.SSHOption{shell.WithoutSharedMaster()}
	stderr := streams.Err
	if fwd != nil {
		opts = append(opts, shell.WithReverseForward(fwd.SandboxPort, fwd.LocalPort))
		stderr = &forwardWatch{w: orStderr(streams.Err), onFailure: fwd.OnFailure}
	}
	_, err := shell.NewSSH(a.exec, a.cfg.Host, opts...).Run(ctx, shell.Command{
		Argv:   []string{"sh", "-c", attachChatScript, "sh", a.cfg.ID.String()},
		Stdin:  streams.In,
		Stdout: streams.Out,
		Stderr: stderr,
	})
	if code, ok := shell.ExitCode(err); ok && code == chatGoneStatus {
		return fmt.Errorf("attach %s on %s: %w", a.TmuxName(), a.cfg.Host, ErrSessionGone)
	}
	return err
}

// setup runs one setup command on the sandbox: stdin is input when given and
// empty otherwise, never this process's, which belongs to what Hermod attaches.
func (a *Agent) setup(ctx context.Context, what string, input io.Reader, argv ...string) error {
	res, err := shell.NewSSH(a.exec, a.cfg.Host).Run(ctx, shell.Command{Argv: argv, Stdin: input, Capture: true})
	if err != nil {
		return fmt.Errorf("%s on %s: %w%s", what, a.cfg.Host, err, detail(res.Stderr))
	}
	return nil
}

// forwardFailed is how ssh reports a reverse forward it could not bind, after
// which it carries on without it.
const forwardFailed = "remote port forwarding failed"

// forwardWatch passes ssh's own stderr through and calls onFailure once when ssh
// reports the forward failed. With a tty, the sandbox's output arrives on
// stdout, so only ssh's messages pass here.
type forwardWatch struct {
	w         io.Writer
	onFailure func()
	tail      []byte // the end of what was written, for a message split across writes
	once      sync.Once
}

func (f *forwardWatch) Write(p []byte) (int, error) {
	f.tail = append(f.tail, p...)
	if bytes.Contains(f.tail, []byte(forwardFailed)) && f.onFailure != nil {
		f.once.Do(f.onFailure)
	}
	if keep := len(forwardFailed); len(f.tail) > keep {
		f.tail = append([]byte(nil), f.tail[len(f.tail)-keep:]...)
	}
	return f.w.Write(p)
}

func isShellName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_', r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}

// detail is a command's stderr as the tail of an error message.
func detail(stderr string) string {
	if s := strings.TrimSpace(stderr); s != "" {
		return ": " + s
	}
	return ""
}

func orStderr(w io.Writer) io.Writer {
	if w == nil {
		return os.Stderr
	}
	return w
}
