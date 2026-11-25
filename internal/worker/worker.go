// Package worker is one process on the remote that outlives whoever started
// it: a tmux session running a command, identified by a key and carrying tags,
// both its caller's to choose. It keeps the worker's lifecycle and runs it
// through a shell.Executor pointed at the remote, taking every tmux command
// from shell's tmux, and knows nothing about what its process is or who it is
// for.
//
// Every program it runs on the remote is a script embedded here, so nothing of
// Hermod's is installed there: write.sh, stream.sh and keep.sh start a worker,
// pane.sh is its session's process, and act.sh lists, closes, removes and
// joins one. Each takes its inputs as arguments, the tmux commands among them,
// and runs as it reads in the repository.
package worker

import (
	"bytes"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/zerlok/hermod/internal/env"
	"github.com/zerlok/hermod/internal/shell"
)

var (
	//go:embed write.sh
	writeScript []byte
	//go:embed stream.sh
	streamScript []byte
	//go:embed keep.sh
	keepScript []byte
	//go:embed pane.sh
	paneScript []byte
	//go:embed act.sh
	actScript []byte
)

// State is what tmux says of a worker.
type State int

const (
	// Absent has no tmux session under the key.
	Absent State = iota
	// Running has the process alive.
	Running
	// Exited has a process that failed; its pane is kept to show the error and
	// exit status.
	Exited
)

func (s State) String() string {
	switch s {
	case Absent:
		return "absent"
	case Running:
		return "running"
	case Exited:
		return "exited"
	}
	return "unknown"
}

// Form is how a worker offers its process's stdio, fixed when it starts.
type Form int

const (
	// Terminal is joined on a tty; tmux's screen buffers what nobody reads.
	Terminal Form = iota
	// LineStream reads lines written to a FIFO and appends its output to logs,
	// which buffer what nobody reads.
	LineStream
)

func (f Form) String() string {
	switch f {
	case Terminal:
		return "terminal"
	case LineStream:
		return "stream"
	}
	return "unknown"
}

func (f Form) valid() bool { return f == Terminal || f == LineStream }

func unknownForm(f Form) error { return fmt.Errorf("worker: unknown form %d", f) }

// Key identifies a worker: it is the tmux session's name and the name of the
// worker's directory.
type Key struct{ name string }

// NewKey checks a worker's key. It is letters, digits, '-', '_', '@' and '+',
// not starting with '-': tmux replaces '.' and ':' in a session name, so a
// listing would not find the key again, a '/' would leave the worker's
// directory, and a leading '-' reads as an option.
func NewKey(name string) (Key, error) {
	if name == "" || name[0] == '-' {
		return Key{}, fmt.Errorf("worker: invalid key %q", name)
	}
	for _, r := range name {
		if !IsKeyRune(r) {
			return Key{}, fmt.Errorf("worker: invalid key %q", name)
		}
	}
	return Key{name: name}, nil
}

// IsKeyRune reports whether a key may hold r.
func IsKeyRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_@+", r)
}

func (k Key) String() string { return k.name }

// IsZero reports whether the key is the zero value, which names no worker.
func (k Key) IsZero() bool { return k.name == "" }

// session is the key's tmux session.
func (k Key) session() shell.TmuxSession { return shell.NewTmuxSession(k.name) }

// workersUnderHome holds every worker's directory, relative to the remote
// user's home.
const workersUnderHome = ".hermod/s"

// underHome is the worker's directory relative to the remote user's home, the
// form the scripts take it in.
func (k Key) underHome() string { return path.Join(workersUnderHome, k.name) }

// staging is a new directory name, relative to the remote user's home, for
// one start to write its files into. Its leading '.' keeps it apart from every
// key, and its random part from every other start's.
func (k Key) staging() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("worker: name a staging directory: %w", err)
	}
	return path.Join(workersUnderHome, "."+k.name+"."+hex.EncodeToString(b)), nil
}

// Tag is one tmux user option a worker carries, such as `@hermod-workspace`,
// by which List finds it again.
type Tag struct{ name, value string }

// NewTag checks a tag: a session option shell's tmux takes, with a value,
// since a session that lacks the option lists it empty.
func NewTag(name, value string) (Tag, error) {
	if err := shell.CheckSessionOption(shell.SessionOption{Name: name, Value: value}); err != nil {
		return Tag{}, fmt.Errorf("worker: invalid tag: %w", err)
	}
	if value == "" {
		return Tag{}, fmt.Errorf("worker: tag %s has an empty value", name)
	}
	return Tag{name: name, value: value}, nil
}

// IsZero reports whether the tag is the zero value, which every untagged
// session would match.
func (t Tag) IsZero() bool { return t.name == "" }

// Name is the tag's tmux option.
func (t Tag) Name() string { return t.name }

// Value is the tag's value.
func (t Tag) Value() string { return t.value }

// File is one file written into the worker's directory before it starts. Its
// content may hold a secret, such as a settings document carrying a token, so
// it prints as its name and size only.
type File struct {
	Name    string
	Content []byte
}

func (f File) String() string   { return fmt.Sprintf("%s (%d bytes)", f.Name, len(f.Content)) }
func (f File) GoString() string { return f.String() }

// Format prints the file masked under every verb.
func (f File) Format(s fmt.State, _ rune) { _, _ = io.WriteString(s, f.String()) }

// The worker's directory holds these besides its caller's files.
const (
	envFile = "env"     // the variables, sourced by the process
	pidFile = "pid"     // a line stream's process ID, waited on by a close
	fifo    = "in.fifo" // a line stream's stdin
	outLog  = "out.log" // a line stream's stdout
	errLog  = "err.log" // a line stream's stderr

	// posSuffix names, after a log's name, the file where its reader records
	// how far it delivered, so the next attach resumes there.
	posSuffix = ".pos"
)

// reserved are the names in the worker's directory a caller's file may not
// take: the scripts' own.
var reserved = map[string]bool{
	envFile: true, pidFile: true, fifo: true, outLog: true, errLog: true,
	outLog + posSuffix: true, errLog + posSuffix: true,
}

func checkFileName(name string) error {
	if name == "" || name[0] == '.' || reserved[name] {
		return fmt.Errorf("worker: invalid file name %q", name)
	}
	for _, r := range name {
		if !(IsKeyRune(r) || r == '.') {
			return fmt.Errorf("worker: invalid file name %q", name)
		}
	}
	return nil
}

// Spec says what to start.
type Spec struct {
	Key     Key
	Dir     string   // where the process starts on the remote, absolute or under the remote user's home
	Command []string // the process and its arguments; none runs the user's login shell
	Files   []File   // written into the worker's directory
	Vars    env.Vars // given to the process from a file in the worker's directory, never on a command line; env.WorkerDir is the worker's own
	Tags    []Tag
	Form    Form
}

func (s Spec) check() error {
	if s.Key.IsZero() {
		return errors.New("worker: a spec needs a key")
	}
	if !s.Form.valid() {
		return unknownForm(s.Form)
	}
	seen := map[string]bool{}
	for _, f := range s.Files {
		if err := checkFileName(f.Name); err != nil {
			return err
		}
		if seen[f.Name] {
			return fmt.Errorf("worker: file %q given twice", f.Name)
		}
		seen[f.Name] = true
	}
	for _, key := range s.Vars.Keys() {
		if !shell.IsName(key) {
			return fmt.Errorf("worker: variable %q is not a shell name", key)
		}
		// The process is given its directory under this name after the
		// variables, so a value of the caller's would never reach it.
		if key == env.WorkerDir {
			return fmt.Errorf("worker: variable %s is the worker's own", key)
		}
	}
	for _, t := range s.Tags {
		if t.IsZero() {
			return errors.New("worker: a tag needs a name")
		}
	}
	return nil
}

// envDocument renders the variables as the lines the process sources.
func envDocument(vars env.Vars) []byte {
	var b bytes.Buffer
	for _, p := range vars.Pairs() {
		fmt.Fprintf(&b, "export %s=%s\n", p.Key, shell.Quote(p.Value.Reveal()))
	}
	return b.Bytes()
}

// Remote is the remote workstation as a worker reaches it: each Executor runs
// a command there.
type Remote struct {
	Exec shell.Executor // what changes the remote; a dry run prints it
	Read shell.Executor // what only reads a state, which always runs
}

// Worker is one tmux session on the remote.
type Worker struct {
	remote Remote
	key    Key
}

// New is the worker of key on remote; it runs nothing.
func New(remote Remote, key Key) Worker { return Worker{remote: remote, key: key} }

// Key is the worker's key.
func (w Worker) Key() Key { return w.key }

// Failures a worker reports as values a caller branches on.
var (
	// ErrPresent is a start under a key whose tmux session exists.
	ErrPresent = errors.New("worker: a tmux session exists under the key")
	// ErrRunning is a remove of a worker whose process is alive.
	ErrRunning = errors.New("worker: the process runs; close it instead")
	// ErrExited is a close of a worker whose process failed.
	ErrExited = errors.New("worker: the process exited; remove it instead")
	// ErrLingers is a close whose process did not exit after the hang-up: a
	// start now would run a second copy of it.
	ErrLingers = errors.New("worker: the process did not exit after its tmux session was closed")
)

// The exit statuses the scripts end with, apart from ssh's 255 and tmux's 1.
// Each script is given them as arguments.
const (
	refusedStatus = 3 // the state refuses the operation, or there is nothing to join
	lingersStatus = 4 // a closed process outlived the wait
	readFailed    = 5 // tmux could not tell which sessions exist
)

// act is the command that runs one operation of act.sh. Its text is an
// argument, not stdin, since attaching and feeding the FIFO take the caller's
// stdin.
func act(op string, args ...string) shell.Command {
	return shell.Command{Args: append([]string{
		"sh", "-c", string(actScript), "act",
		strconv.Itoa(refusedStatus), strconv.Itoa(lingersStatus), strconv.Itoa(readFailed), strconv.Itoa(shell.TmuxAbsent), op,
	}, args...)}
}

// Start writes the worker's directory and starts spec's process in it. The
// directory, mode 0700, gets the variables and the files over ssh stdin, so no
// secret is on a command line; every other setup command runs with an empty
// stdin, which leaves the remote's stdin to the process. A worker that is not
// Absent is ErrPresent, and its directory is left as it is.
//
// The files are written into a staging directory of this start's own, which
// replaces the worker's directory only once tmux made this start's session:
// another start that read Absent at the same moment cannot touch them, and no
// file of an earlier start is left beside them.
func Start(ctx context.Context, remote Remote, spec Spec) (Worker, error) {
	if err := spec.check(); err != nil {
		return Worker{}, err
	}
	w := New(remote, spec.Key)
	switch state, err := w.State(ctx); {
	case err != nil:
		return Worker{}, err
	case state != Absent:
		return Worker{}, fmt.Errorf("start %s, %s: %w", w.key, state, ErrPresent)
	}
	staging, err := w.key.staging()
	if err != nil {
		return Worker{}, err
	}
	if err := w.writeDir(ctx, staging, spec); err != nil {
		return Worker{}, err
	}
	if spec.Form == LineStream {
		if err := w.exec(ctx, "make the line stream of", script(streamScript, staging, fifo, outLog, errLog)); err != nil {
			return Worker{}, err
		}
	}
	session := w.key.session()
	options := make([]shell.SessionOption, len(spec.Tags))
	for i, t := range spec.Tags {
		options[i] = shell.SessionOption{Name: t.name, Value: t.value}
	}
	pane := append([]string{
		"sh", "-c", string(paneScript), "pane",
		w.key.underHome(), staging, spec.Dir, env.WorkerDir, pidFile, envFile,
		spec.Form.String(), fifo, outLog, errLog, shell.Line(session.KeepFailedPane()),
	}, spec.Command...)
	newSession, err := handed(ctx, func(in shell.Executor) shell.Executor {
		return shell.NewLogin(shell.NewTmux(in, w.key.name, shell.Detached(), shell.WithSessionOptions(options...)))
	}, pane)
	if err != nil {
		return Worker{}, fmt.Errorf("start worker %s: %w", w.key, err)
	}
	keep := append([]string{w.key.underHome(), staging, strconv.Itoa(refusedStatus), shell.Line(session.Has())}, newSession...)
	err = w.exec(ctx, "start", script(keepScript, keep...))
	if refused(err) {
		return Worker{}, fmt.Errorf("start %s: %w", w.key, ErrPresent)
	}
	if err != nil {
		return Worker{}, err
	}
	return w, nil
}

// handed is args as the executors wrap makes hand them inward, run nowhere,
// for a script on the remote that decides whether to run them.
func handed(ctx context.Context, wrap func(shell.Executor) shell.Executor, args []string) ([]string, error) {
	var c capture
	if err := wrap(&c).Exec(ctx, shell.Command{Args: args}).Wait(); err != nil {
		return nil, err
	}
	return c.args, nil
}

// capture is an executor that records the arguments of the command it is
// handed and runs nothing.
type capture struct{ args []string }

func (c *capture) Exec(_ context.Context, cmd shell.Command) shell.Execution {
	c.args = cmd.Args
	return shell.Finished(cmd, nil)
}

// writeDir writes the variables and spec's files into staging, each name
// followed by its length in the arguments and the contents one after another
// on stdin.
func (w Worker) writeDir(ctx context.Context, staging string, spec Spec) error {
	files := append([]File{{Name: envFile, Content: envDocument(spec.Vars)}}, spec.Files...)
	args := []string{"sh", "-c", string(writeScript), "write", staging}
	var payload bytes.Buffer
	for _, f := range files {
		args = append(args, f.Name, strconv.Itoa(len(f.Content)))
		payload.Write(f.Content)
	}
	return w.exec(ctx, "write the directory of", shell.Command{Args: args, Stdin: &payload})
}

// listEnd is the line act.sh's list ends a complete listing with.
const listEnd = "end\n"

// complete is a listing without the line that ends it; ok is false when out
// lacks that line, as a listing cut short does.
func complete(out string) (sessions string, ok bool) {
	if out == listEnd {
		return "", true
	}
	sessions, ok = strings.CutSuffix(out, "\n"+listEnd)
	if !ok {
		return "", false
	}
	return sessions + "\n", true
}

// list reads every tmux session on the remote with the values of the tags
// named.
func list(ctx context.Context, remote Remote, tags ...string) ([]shell.TmuxListed, error) {
	listing, err := shell.NewTmuxListing(tags...)
	if err != nil {
		return nil, err
	}
	out, err := shell.Run(ctx, remote.Read, act("list", shell.Line(listing.Command())))
	if err != nil {
		return nil, fmt.Errorf("list tmux sessions: %w%s", err, detail(out.Stderr))
	}
	sessions, ended := complete(out.Stdout)
	if !ended {
		return nil, errors.New("list tmux sessions: incomplete output")
	}
	listed, err := listing.Parse(sessions)
	if err != nil {
		return nil, fmt.Errorf("list tmux sessions: %w", err)
	}
	return listed, nil
}

// stateOf is the state of a session a listing found.
func stateOf(s shell.TmuxListed) State {
	if s.Dead {
		return Exited
	}
	return Running
}

// State reads the worker's state from tmux. It is read-only; a look that fails
// is an error, not a state.
func (w Worker) State(ctx context.Context) (State, error) {
	sessions, err := list(ctx, w.remote)
	if err != nil {
		return Absent, err
	}
	for _, s := range sessions {
		if s.Name == w.key.name {
			return stateOf(s), nil
		}
	}
	return Absent, nil
}

// Listed is one worker List found, with its state.
type Listed struct {
	Worker Worker
	State  State
}

// List reads the workers on the remote that carry tag, in tmux's order. A
// session whose name is no key is not a worker and is left out. The zero tag
// is refused: every untagged session, the user's own among them, carries it.
func List(ctx context.Context, remote Remote, tag Tag) ([]Listed, error) {
	if tag.IsZero() {
		return nil, errors.New("worker: list needs a tag")
	}
	sessions, err := list(ctx, remote, tag.name)
	if err != nil {
		return nil, err
	}
	var found []Listed
	for _, s := range sessions {
		if s.Options[tag.name] != tag.value {
			continue
		}
		key, err := NewKey(s.Name)
		if err != nil {
			continue
		}
		found = append(found, Listed{Worker: New(remote, key), State: stateOf(s)})
	}
	return found, nil
}

// Look is what one read of a worker found: its state, the files asked for
// that its directory holds, and what the command run beside it wrote.
type Look struct {
	State State
	Files []File
	Out   []byte
}

// File is the file under name the look found in the worker's directory; ok is
// false when the directory holds none.
func (l Look) File(name string) (File, bool) {
	for _, f := range l.Files {
		if f.Name == name {
			return f, true
		}
	}
	return File{}, false
}

// Look reads in one call on the remote the worker's state, the named files of
// its directory and the output of cmd, run under the remote user's login
// shell, where a program such as an agent is found; an empty cmd runs nothing.
// A state and an answer read in one call cannot fall apart between two reads.
// The files may hold a secret, so they travel on the command's output and
// print masked. It only reads a state, so it runs in a dry run too.
func (w Worker) Look(ctx context.Context, cmd []string, files ...string) (Look, error) {
	for _, name := range files {
		if err := checkFileName(name); err != nil {
			return Look{}, err
		}
	}
	var line string
	if len(cmd) > 0 {
		login, err := handed(ctx, shell.NewLogin, cmd)
		if err != nil {
			return Look{}, fmt.Errorf("look at worker %s: %w", w.key, err)
		}
		line = shell.Line(login)
	}
	args := []string{shell.Line(w.key.session().PaneDead()), w.key.underHome(), line}
	out, err := shell.Run(ctx, w.remote.Read, act("look", append(args, files...)...))
	if err != nil {
		return Look{}, fmt.Errorf("look at worker %s: %w%s", w.key, err, detail(out.Stderr))
	}
	look, err := parseLook(out.Stdout)
	if err != nil {
		return Look{}, fmt.Errorf("look at worker %s: %w", w.key, err)
	}
	return look, nil
}

// parseLook reads look's output: the state line, each file's header and
// bytes, then `out` and the command's output.
func parseLook(out string) (Look, error) {
	head, rest, _ := strings.Cut(out, "\n")
	var look Look
	switch head {
	case "state 0":
		look.State = Running
	case "state 1":
		look.State = Exited
	case "state absent":
		look.State = Absent
	default:
		return Look{}, fmt.Errorf("unreadable state %q", head)
	}
	for {
		line, after, found := strings.Cut(rest, "\n")
		if !found {
			return Look{}, errors.New("incomplete output")
		}
		if line == "out" {
			look.Out = []byte(after)
			return look, nil
		}
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[0] != "file" {
			return Look{}, fmt.Errorf("unreadable line %q", line)
		}
		n, err := strconv.Atoi(fields[2])
		if err != nil || n < 0 || n > len(after) {
			return Look{}, fmt.Errorf("unreadable length of file %s", fields[1])
		}
		look.Files = append(look.Files, File{Name: fields[1], Content: []byte(after[:n])})
		rest = after[n:]
	}
}

// socketTables is where Linux lists the remote's TCP sockets, which FreePort
// reads for the ports in use.
const socketTables = "/proc/net"

// ErrNoPort is a pick of a port in which every candidate is taken.
var ErrNoPort = errors.New("worker: every candidate port is taken on the remote")

// FreePort picks the first of candidates that no socket on the remote uses and
// no worker's directory records in its file record, where a worker that
// listens on a port keeps it while nothing is bound to it. It only reads a
// state, so it runs in a dry run too.
func FreePort(ctx context.Context, remote Remote, record string, candidates []int) (int, error) {
	if err := checkFileName(record); err != nil {
		return 0, err
	}
	args := []string{workersUnderHome, record, socketTables}
	for _, p := range candidates {
		args = append(args, strconv.Itoa(p))
	}
	out, err := shell.Run(ctx, remote.Read, act("port", args...))
	if refused(err) {
		return 0, ErrNoPort
	}
	if err != nil {
		return 0, fmt.Errorf("pick a port: %w%s", err, detail(out.Stderr))
	}
	port, err := strconv.Atoi(strings.TrimSpace(out.Stdout))
	if err != nil || !slices.Contains(candidates, port) {
		return 0, fmt.Errorf("pick a port: unreadable answer %q", out.Stdout)
	}
	return port, nil
}

// Close ends a Running worker with SIGHUP; for a line stream it waits for the
// process to exit, which is ErrLingers when it does not. An Exited worker is
// ErrExited, since its pane is removed instead, and an Absent one has nothing
// to close. A tmux that cannot tell is an error. The worker's directory stays.
func (w Worker) Close(ctx context.Context) error {
	session := w.key.session()
	err := w.exec(ctx, "close", act("close",
		shell.Line(session.PaneDead()), shell.Line(session.Kill()),
		w.key.underHome(), pidFile, env.WorkerDir))
	switch code, _ := shell.ExitCode(err); {
	case err == nil:
		return nil
	case code == refusedStatus:
		return fmt.Errorf("close %s: %w", w.key, ErrExited)
	case code == lingersStatus:
		return fmt.Errorf("close %s: %w", w.key, ErrLingers)
	}
	return err
}

// Remove removes an Exited worker. A Running one is ErrRunning, an Absent one
// has nothing to remove, and a tmux that cannot tell is an error.
func (w Worker) Remove(ctx context.Context) error {
	session := w.key.session()
	err := w.exec(ctx, "remove", act("remove", shell.Line(session.PaneDead()), shell.Line(session.Kill())))
	if refused(err) {
		return fmt.Errorf("remove %s: %w", w.key, ErrRunning)
	}
	return err
}

// Stdio is the remote commands that join a worker's process's stdio: an
// Attach for a terminal, Lines for a line stream. Each command exits 0 when
// its remote side ended, and non-zero when it failed, such as when there is
// nothing under the key to join. How a command ended does not say whether the
// process is gone: a terminal also ends when its tmux client detaches, so the
// caller reads the State afterwards.
type Stdio interface{ isStdio() }

// Attach joins a terminal worker's tmux session on the caller's tty, which
// its runner must request. It ends when the client detaches or the session
// does.
type Attach struct{ Command shell.Command }

// Lines are a line-stream worker's three streams, each its own command. In
// writes its stdin into the FIFO; Out and Err deliver their log from where the
// last reader stopped and end once the process is gone and all it wrote was
// delivered.
type Lines struct{ In, Out, Err shell.Command }

func (Attach) isStdio() {}
func (Lines) isStdio()  {}

// Stdio is the commands that join the worker's stdio in form, which its
// caller started it with; a form Start would refuse is refused here too.
func (w Worker) Stdio(form Form) (Stdio, error) {
	session := w.key.session()
	switch form {
	case Terminal:
		return Attach{Command: act("attach", shell.Line(session.Has()), shell.Line(session.Attach()))}, nil
	case LineStream:
		dir := w.key.underHome()
		logOf := func(log string) shell.Command {
			return act("log", shell.Line(session.PaneDead()), path.Join(dir, log), path.Join(dir, log+posSuffix))
		}
		return Lines{
			In:  act("feed", path.Join(dir, fifo)),
			Out: logOf(outLog),
			Err: logOf(errLog),
		}, nil
	}
	return nil, unknownForm(form)
}

// refused reports whether err is a script's refusal.
func refused(err error) bool {
	code, ok := shell.ExitCode(err)
	return ok && code == refusedStatus
}

// script is the command that runs a remote script: sh reads it from stdin and
// takes args as its positional parameters.
func script(text []byte, args ...string) shell.Command {
	return shell.Command{Args: append([]string{"sh", "-s", "--"}, args...), Stdin: bytes.NewReader(text)}
}

// exec runs one command that changes the remote, collecting its output so a
// failure names what tmux or the script said.
func (w Worker) exec(ctx context.Context, what string, cmd shell.Command) error {
	out, err := shell.Run(ctx, w.remote.Exec, cmd)
	if err != nil {
		return fmt.Errorf("%s worker %s: %w%s", what, w.key, err, detail(out.Stderr))
	}
	return nil
}

// detail is a command's stderr as the tail of an error message.
func detail(stderr string) string {
	if s := strings.TrimSpace(stderr); s != "" {
		return ": " + s
	}
	return ""
}
