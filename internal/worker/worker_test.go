package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/zerlok/hermod/internal/env"
	"github.com/zerlok/hermod/internal/secret"
	"github.com/zerlok/hermod/internal/shell"
	"github.com/zerlok/hermod/internal/worker/workertest"
)

// exitError is a remote command's failure with its exit status.
type exitError int

func (e exitError) Error() string { return fmt.Sprintf("exit status %d", int(e)) }
func (e exitError) ExitCode() int { return int(e) }

// session is one tmux session the fake remote holds.
type session struct {
	state State
	tags  map[string]string
}

// call is one command the fake remote was handed, with its stdin read whole.
type call struct {
	args  []string
	stdin string
}

// remote is a fake remote: it holds tmux sessions and the directories under
// the remote user's home, and answers each command a worker sends by what the
// command's script does there, so a test drives a worker through its states.
// Every call is recorded.
type remote struct {
	sessions map[string]session
	dirs     map[string]map[string]string // each directory's files by name, under the home
	appears  string                       // a session another start makes right after the first listing
	lingers  bool                         // a closed process outlives the hang-up
	failsAs  State                        // what a started process becomes: Running, or Exited for one that fails
	listOut  string                       // when set, the listing's output in place of the sessions
	listErr  error                        // when set, the listing fails
	tmuxErr  bool                         // tmux cannot tell whether a session exists
	calls    []call
}

func (r *remote) Exec(_ context.Context, cmd shell.Command) shell.Execution {
	var stdin string
	if cmd.Stdin != nil {
		b, _ := io.ReadAll(cmd.Stdin)
		stdin = string(b)
	}
	r.calls = append(r.calls, call{args: cmd.Args, stdin: stdin})
	out, err := r.answer(cmd.Args, stdin)
	if cmd.Stdout != nil {
		_, _ = io.WriteString(cmd.Stdout, out)
	}
	return shell.Finished(shell.Command{Stdin: cmd.Stdin, Stdout: cmd.Stdout, Stderr: cmd.Stderr}, err)
}

func (r *remote) answer(args []string, stdin string) (string, error) {
	c, ok := workertest.Read(args)
	switch {
	case !ok:
	case c.Script == "keep":
		return "", r.start(c)
	case c.Script == "stream":
		return "", r.stream(c.Args)
	case c.Script == "write":
		return "", r.write(c.Args[0], c.Args[1:], stdin)
	case c.Op == "list":
		return r.list(c)
	case c.Op == "close":
		return "", r.close(c.Session)
	case c.Op == "remove":
		return "", r.remove(c.Session)
	}
	return "", fmt.Errorf("unexpected command %q", args)
}

// write makes a staging directory and splits stdin into the files named.
func (r *remote) write(staging string, pairs []string, stdin string) error {
	if r.dirs == nil {
		r.dirs = map[string]map[string]string{}
	}
	if _, ok := r.dirs[staging]; ok {
		return exitError(1)
	}
	files := map[string]string{}
	for i := 0; i+1 < len(pairs); i += 2 {
		n, err := strconv.Atoi(pairs[i+1])
		if err != nil || n > len(stdin) {
			return exitError(1)
		}
		files[pairs[i]], stdin = stdin[:n], stdin[n:]
	}
	r.dirs[staging] = files
	return nil
}

// stream gives a staging directory its FIFO and empty logs.
func (r *remote) stream(args []string) error {
	files, ok := r.dirs[args[0]]
	if !ok {
		return exitError(1)
	}
	files[args[1]] = "fifo"
	files[args[2]], files[args[3]] = "", ""
	return nil
}

// start makes the session keep makes, with its tags, then puts the staging
// directory in place of the worker's; a session taken already only drops the
// staging directory.
func (r *remote) start(c workertest.Command) error {
	if _, ok := r.sessions[c.Session]; ok {
		delete(r.dirs, c.Start.Staging)
		return exitError(c.Refused)
	}
	state := r.failsAs
	if state == Absent {
		state = Running
	}
	r.sessions[c.Session] = session{state: state, tags: c.Start.Tags}
	r.dirs[c.Start.WorkerDir] = r.dirs[c.Start.Staging]
	delete(r.dirs, c.Start.Staging)
	return nil
}

// list prints the sessions as the listing's command formats them.
func (r *remote) list(c workertest.Command) (string, error) {
	if r.listErr != nil {
		return "", r.listErr
	}
	if r.listOut != "" {
		return r.listOut, nil
	}
	if r.appears != "" {
		defer func() { r.sessions[r.appears], r.appears = session{state: Running}, "" }()
	}
	var listed []workertest.Listed
	for name, s := range r.sessions {
		listed = append(listed, workertest.Listed{Name: name, Dead: s.state == Exited, Tags: s.tags})
	}
	return c.List(listed), nil
}

func (r *remote) close(name string) error {
	s, ok := r.sessions[name]
	switch {
	case r.tmuxErr:
		return exitError(readFailed)
	case !ok:
		return nil
	case s.state == Exited:
		return exitError(refusedStatus)
	}
	delete(r.sessions, name)
	if r.lingers {
		return exitError(lingersStatus)
	}
	return nil
}

func (r *remote) remove(name string) error {
	s, ok := r.sessions[name]
	switch {
	case r.tmuxErr:
		return exitError(readFailed)
	case !ok:
		return nil
	case s.state == Running:
		return exitError(refusedStatus)
	}
	delete(r.sessions, name)
	return nil
}

func mustKey(t *testing.T, name string) Key {
	t.Helper()
	k, err := NewKey(name)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func mustTag(t *testing.T, name, value string) Tag {
	t.Helper()
	tag, err := NewTag(name, value)
	if err != nil {
		t.Fatal(err)
	}
	return tag
}

// op is one thing a test does to a worker, as the diagram names it.
type op int

const (
	opStart op = iota
	opClose
	opRemove
	opCleanExit  // the process exits with status 0: tmux ends the session
	opFailedExit // the process fails: tmux keeps the pane
)

func (o op) apply(ctx context.Context, r *remote, w Worker) error {
	switch o {
	case opStart:
		_, err := Start(ctx, Remote{Exec: r, Read: r}, Spec{Key: w.Key(), Dir: "proj"})
		return err
	case opClose:
		return w.Close(ctx)
	case opRemove:
		return w.Remove(ctx)
	case opCleanExit:
		delete(r.sessions, w.Key().String())
	case opFailedExit:
		r.sessions[w.Key().String()] = session{state: Exited}
	}
	return nil
}

// errTmux stands, in a table, for the failure of a tmux that cannot tell
// which sessions exist.
var errTmux = errors.New("tmux cannot tell")

// matches reports whether err is want, where errTmux matches the exit status
// a script ends with when tmux cannot tell.
func matches(err, want error) bool {
	if want == errTmux {
		code, ok := shell.ExitCode(err)
		return ok && code == readFailed
	}
	return errors.Is(err, want)
}

// TestStates drives a worker along every transition of the diagram, and tries
// every operation the diagram does not have from each state.
func TestStates(t *testing.T) {
	cases := []struct {
		name    string
		from    State
		lingers bool
		tmuxErr bool
		op      op
		wantErr error
		want    State
	}{
		{"start", Absent, false, false, opStart, nil, Running},
		{"clean exit", Running, false, false, opCleanExit, nil, Absent},
		{"close", Running, false, false, opClose, nil, Absent},
		{"failed exit", Running, false, false, opFailedExit, nil, Exited},
		{"remove", Exited, false, false, opRemove, nil, Absent},
		{"start when running is refused", Running, false, false, opStart, ErrPresent, Running},
		{"start when exited is refused", Exited, false, false, opStart, ErrPresent, Exited},
		{"remove when running is refused", Running, false, false, opRemove, ErrRunning, Running},
		{"close when exited is refused", Exited, false, false, opClose, ErrExited, Exited},
		{"close when absent does nothing", Absent, false, false, opClose, nil, Absent},
		{"remove when absent does nothing", Absent, false, false, opRemove, nil, Absent},
		{"close whose process lingers", Running, true, false, opClose, ErrLingers, Absent},
		{"close when tmux cannot tell fails", Running, false, true, opClose, errTmux, Running},
		{"remove when tmux cannot tell fails", Exited, false, true, opRemove, errTmux, Exited},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			key := mustKey(t, "hermod-k")
			r := &remote{sessions: map[string]session{}, lingers: tc.lingers, tmuxErr: tc.tmuxErr}
			if tc.from != Absent {
				r.sessions[key.String()] = session{state: tc.from}
			}
			w := New(Remote{Exec: r, Read: r}, key)
			if err := tc.op.apply(ctx, r, w); !matches(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			got, err := w.State(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("state = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestStartRefusedWritesNothing checks that a start under a key tmux holds
// leaves the worker's directory and its line stream as they are.
func TestStartRefusedWritesNothing(t *testing.T) {
	cases := []struct {
		name      string
		state     State
		form      Form
		wantCalls int
	}{
		{"running terminal", Running, Terminal, 1},
		{"exited line stream", Exited, LineStream, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key := mustKey(t, "hermod-k")
			r := &remote{sessions: map[string]session{key.String(): {state: tc.state}}}
			_, err := Start(context.Background(), Remote{Exec: r, Read: r}, Spec{Key: key, Form: tc.form})
			if !errors.Is(err, ErrPresent) {
				t.Fatalf("error = %v, want %v", err, ErrPresent)
			}
			if len(r.calls) != tc.wantCalls {
				t.Errorf("calls = %q, want %d", r.calls, tc.wantCalls)
			}
		})
	}
}

func TestReadState(t *testing.T) {
	cases := []struct {
		name    string
		out     string
		err     error
		want    State
		wantErr bool
	}{
		{"absent", "0\tother\nend\n", nil, Absent, false},
		{"running", "1\tother\n0\thermod-k\nend\n", nil, Running, false},
		{"exited", "1\thermod-k\nend\n", nil, Exited, false},
		{"no server", "end\n", nil, Absent, false},
		{"a name with spaces", "0\tmy session\n0\thermod-k\nend\n", nil, Running, false},
		{"incomplete output", "0\thermod-k\n", nil, Absent, true},
		{"cut short after a name ending in the marker", "0\thermod-k\n0\tbackend\n", nil, Absent, true},
		{"cut short inside a line", "0\thermod-k\n0\tback", nil, Absent, true},
		{"unreadable state", "x\thermod-k\nend\n", nil, Absent, true},
		{"unreadable line", "0\nend\n", nil, Absent, true},
		{"tmux failed", "", exitError(readFailed), Absent, true},
		{"connection failed", "", exitError(255), Absent, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &remote{listOut: tc.out, listErr: tc.err}
			got, err := New(Remote{Exec: r, Read: r}, mustKey(t, "hermod-k")).State(context.Background())
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, want error %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("state = %s, want %s", got, tc.want)
			}
		})
	}
}

// actArgs are the arguments of an act.sh operation, the exit statuses first.
func actArgs(op string, args ...string) []string {
	return append([]string{"sh", "-c", string(actScript), "act", "3", "4", "5", "1", op}, args...)
}

// The tmux command lines the scripts are handed for the session hermod-k, as
// shell, whose own tests check them, makes them.
var (
	hasK  = shell.Line(shell.NewTmuxSession("hermod-k").Has())
	deadK = shell.Line(shell.NewTmuxSession("hermod-k").PaneDead())
	killK = "tmux kill-session -t =hermod-k:"
)

// listOf is the act.sh list operation for the tags named.
func listOf(tags ...string) []string {
	listing, err := shell.NewTmuxListing(tags...)
	if err != nil {
		panic(err)
	}
	return actArgs("list", shell.Line(listing.Command()))
}

// keepArgs are keep.sh's arguments for a start under key: its own, then the
// tmux command that makes the session detached, its process pane.sh with pane
// as its arguments under the login shell, and then sets options, each a
// name and a value.
func keepArgs(key string, pane []string, options ...string) []string {
	args := []string{"sh", "-s", "--", ".hermod/s/" + key, staged, "3", shell.Line(shell.NewTmuxSession(key).Has()),
		"tmux", "new-session", "-d", "-s", key, "--",
		"sh", "-c", `exec "$SHELL" -lc "$1"`, "sh", shell.Line(append([]string{"sh", "-c", string(paneScript), "pane"}, pane...)),
	}
	for i := 0; i+1 < len(options); i += 2 {
		args = append(args, ";", "set-option", "-t", "="+key+":", "-qo", options[i], options[i+1])
	}
	return args
}

// stagingName matches a staging directory's name, whose random part a table
// cannot know; staged stands for it in a table.
var stagingName = regexp.MustCompile(`\.hermod/s/\.[^/ ']+\.[0-9a-f]{16}`)

const staged = "STAGING"

func withoutStaging(calls []call) []call {
	out := make([]call, len(calls))
	for i, c := range calls {
		args := make([]string, len(c.args))
		for j, a := range c.args {
			args[j] = stagingName.ReplaceAllString(a, staged)
		}
		out[i] = call{args: args, stdin: c.stdin}
	}
	return out
}

func TestStartCommands(t *testing.T) {
	cases := []struct {
		name string
		spec func(t *testing.T) Spec
		want []call
	}{
		{
			"terminal, a tag value tmux would split at",
			func(t *testing.T) Spec {
				return Spec{
					Key:     mustKey(t, "hermod-k"),
					Dir:     "/srv/api",
					Command: []string{"claude", "--resume", "id"},
					Vars:    env.New(env.Pair{Key: "GIT_AUTHOR_NAME", Value: env.Plain("Jane O'Neil")}),
					Tags:    []Tag{mustTag(t, "@hermod-workspace", "ws"), mustTag(t, "@hermod-origin", "my box;")},
					Form:    Terminal,
				}
			},
			[]call{
				{args: listOf()},
				{
					args:  []string{"sh", "-c", string(writeScript), "write", staged, "env", "40"},
					stdin: "export GIT_AUTHOR_NAME='Jane O'\\''Neil'\n",
				},
				{
					args: keepArgs("hermod-k",
						[]string{".hermod/s/hermod-k", staged, "/srv/api", "HERMOD_WORKER_DIR", "pid", "env",
							"terminal", "in.fifo", "out.log", "err.log", "tmux set-option -w -t =hermod-k: remain-on-exit failed",
							"claude", "--resume", "id"},
						"@hermod-workspace", "ws", "@hermod-origin", `my box\;`),
					stdin: string(keepScript),
				},
			},
		},
		{
			"line stream with files and a word tmux would split at",
			func(t *testing.T) Spec {
				return Spec{
					Key:     mustKey(t, "hermod-acp-ws"),
					Dir:     "proj",
					Command: []string{"sh", "-c", "a;", ";"},
					Files:   []File{{Name: "settings.json", Content: []byte("{}")}, {Name: "mcp.json", Content: []byte("[1]")}},
					Form:    LineStream,
				}
			},
			[]call{
				{args: listOf()},
				{
					args:  []string{"sh", "-c", string(writeScript), "write", staged, "env", "0", "settings.json", "2", "mcp.json", "3"},
					stdin: "{}[1]",
				},
				{
					args:  []string{"sh", "-s", "--", staged, "in.fifo", "out.log", "err.log"},
					stdin: string(streamScript),
				},
				{
					args: keepArgs("hermod-acp-ws",
						[]string{".hermod/s/hermod-acp-ws", staged, "proj", "HERMOD_WORKER_DIR", "pid", "env",
							"stream", "in.fifo", "out.log", "err.log", "tmux set-option -w -t =hermod-acp-ws: remain-on-exit failed",
							"sh", "-c", "a;", ";"}),
					stdin: string(keepScript),
				},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &remote{sessions: map[string]session{}}
			if _, err := Start(context.Background(), Remote{Exec: r, Read: r}, tc.spec(t)); err != nil {
				t.Fatal(err)
			}
			if got := withoutStaging(r.calls); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("calls =\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}

// TestNoSecretInArgs checks that a secret in a variable or a file reaches the
// remote on stdin only.
func TestNoSecretInArgs(t *testing.T) {
	cases := []struct {
		name   string
		secret string
		spec   func(t *testing.T, value string) Spec
	}{
		{
			"variable",
			"sk-ant-variable",
			func(t *testing.T, value string) Spec {
				return Spec{
					Key:  mustKey(t, "hermod-k"),
					Vars: env.New(env.Pair{Key: "ANTHROPIC_API_KEY", Value: env.Secret(secret.FromString(value))}),
				}
			},
		},
		{
			"file",
			"tok-file",
			func(t *testing.T, value string) Spec {
				return Spec{
					Key:   mustKey(t, "hermod-k"),
					Files: []File{{Name: "token", Content: []byte(value)}},
					Form:  LineStream,
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &remote{sessions: map[string]session{}}
			if _, err := Start(context.Background(), Remote{Exec: r, Read: r}, tc.spec(t, tc.secret)); err != nil {
				t.Fatal(err)
			}
			var delivered bool
			for _, c := range r.calls {
				for _, a := range c.args {
					if strings.Contains(a, tc.secret) {
						t.Errorf("secret in argument %q", a)
					}
				}
				delivered = delivered || strings.Contains(c.stdin, tc.secret)
			}
			if !delivered {
				t.Error("secret never reached the remote")
			}
		})
	}
}

func TestList(t *testing.T) {
	cases := []struct {
		name      string
		sessions  map[string]session
		tag       func(t *testing.T) Tag
		want      map[string]State
		wantCalls [][]string
		wantErr   bool
	}{
		{
			"by workspace",
			map[string]session{
				"hermod-a":     {state: Running, tags: map[string]string{"@hermod-workspace": "ws"}},
				"hermod-b":     {state: Exited, tags: map[string]string{"@hermod-workspace": "ws"}},
				"hermod-c":     {state: Running, tags: map[string]string{"@hermod-workspace": "other"}},
				"untagged":     {state: Running},
				"not.a.worker": {state: Running, tags: map[string]string{"@hermod-workspace": "ws"}},
			},
			func(t *testing.T) Tag { return mustTag(t, "@hermod-workspace", "ws") },
			map[string]State{"hermod-a": Running, "hermod-b": Exited},
			[][]string{listOf("@hermod-workspace")},
			false,
		},
		{
			"none",
			map[string]session{},
			func(t *testing.T) Tag { return mustTag(t, "@hermod-origin", "box") },
			map[string]State{},
			[][]string{listOf("@hermod-origin")},
			false,
		},
		{
			"the zero tag, which every untagged session carries, is refused",
			map[string]session{"users-own": {state: Running}},
			func(*testing.T) Tag { return Tag{} },
			map[string]State{},
			nil,
			true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &remote{sessions: tc.sessions}
			found, err := List(context.Background(), Remote{Exec: r, Read: r}, tc.tag(t))
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, want error %v", err, tc.wantErr)
			}
			got := map[string]State{}
			for _, l := range found {
				got[l.Worker.Key().String()] = l.State
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("found %v, want %v", got, tc.want)
			}
			var calls [][]string
			for _, c := range r.calls {
				calls = append(calls, c.args)
			}
			if !reflect.DeepEqual(calls, tc.wantCalls) {
				t.Errorf("calls = %q, want %q", calls, tc.wantCalls)
			}
		})
	}
}

func TestCloseAndRemoveCommands(t *testing.T) {
	cases := []struct {
		name string
		op   op
		want []string
	}{
		{"close", opClose, actArgs("close", deadK, killK, ".hermod/s/hermod-k", "pid", "HERMOD_WORKER_DIR")},
		{"remove", opRemove, actArgs("remove", deadK, killK)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &remote{sessions: map[string]session{}}
			key := mustKey(t, "hermod-k")
			if err := tc.op.apply(context.Background(), r, New(Remote{Exec: r, Read: r}, key)); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(r.calls[0].args, tc.want) {
				t.Errorf("args = %q, want %q", r.calls[0].args, tc.want)
			}
		})
	}
}

// TestReadsAndChangesGoApart checks that a read runs on the Read executor and
// a change on the Exec one, so a dry run still reads the remote.
func TestReadsAndChangesGoApart(t *testing.T) {
	cases := []struct {
		name      string
		op        op
		wantExec  int
		wantReads int
	}{
		{"start", opStart, 2, 1},
		{"close", opClose, 1, 0},
		{"remove", opRemove, 1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sessions := map[string]session{}
			exec, read := &remote{sessions: sessions}, &remote{sessions: sessions}
			key := mustKey(t, "hermod-k")
			w := New(Remote{Exec: exec, Read: read}, key)
			var err error
			if tc.op == opStart {
				_, err = Start(context.Background(), Remote{Exec: exec, Read: read}, Spec{Key: key})
			} else {
				err = tc.op.apply(context.Background(), exec, w)
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(exec.calls) != tc.wantExec || len(read.calls) != tc.wantReads {
				t.Errorf("exec calls %d, reads %d; want %d, %d", len(exec.calls), len(read.calls), tc.wantExec, tc.wantReads)
			}
		})
	}
}

func TestStdio(t *testing.T) {
	cases := []struct {
		name    string
		form    Form
		want    Stdio
		wantErr bool
	}{
		{"terminal", Terminal, Attach{Command: shell.Command{Args: actArgs("attach", hasK, "tmux attach-session -t =hermod-k:")}}, false},
		{"line stream", LineStream, Lines{
			In:  shell.Command{Args: actArgs("feed", ".hermod/s/hermod-k/in.fifo")},
			Out: shell.Command{Args: actArgs("log", deadK, ".hermod/s/hermod-k/out.log", ".hermod/s/hermod-k/out.log.pos")},
			Err: shell.Command{Args: actArgs("log", deadK, ".hermod/s/hermod-k/err.log", ".hermod/s/hermod-k/err.log.pos")},
		}, false},
		{"unknown form", Form(7), nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &remote{}
			got, err := New(Remote{Exec: r, Read: r}, mustKey(t, "hermod-k")).Stdio(tc.form)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, want error %v", err, tc.wantErr)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Stdio = %+v, want %+v", got, tc.want)
			}
			if len(r.calls) != 0 {
				t.Errorf("calls = %q, want none", r.calls)
			}
		})
	}
}

// TestStartDirectory checks what a start leaves under the remote user's home:
// only its own files in the worker's directory, and nothing of a start that
// lost the session to another.
func TestStartDirectory(t *testing.T) {
	cases := []struct {
		name    string
		before  map[string]map[string]string
		appears bool // another start makes the session between this start's read and its own
		spec    Spec
		wantErr error
		want    map[string]map[string]string
	}{
		{
			"a line stream gets its FIFO and empty logs",
			map[string]map[string]string{},
			false,
			Spec{Form: LineStream},
			nil,
			map[string]map[string]string{".hermod/s/hermod-k": {"env": "", "in.fifo": "fifo", "out.log": "", "err.log": ""}},
		},
		{
			"a restart leaves no file of an earlier start",
			map[string]map[string]string{".hermod/s/hermod-k": {"env": "", "mcp.json": "tok-old", "pid": "41", "out.log.pos": "7"}},
			false,
			Spec{Files: []File{{Name: "settings.json", Content: []byte("{}")}}},
			nil,
			map[string]map[string]string{".hermod/s/hermod-k": {"env": "", "settings.json": "{}"}},
		},
		{
			"a start that loses the session to another touches none of its files",
			map[string]map[string]string{".hermod/s/hermod-k": {"env": "", "in.fifo": "fifo", "out.log": "first answer\n", "err.log": "", "out.log.pos": "0"}},
			true,
			Spec{Form: LineStream, Files: []File{{Name: "settings.json", Content: []byte("{}")}}},
			ErrPresent,
			map[string]map[string]string{".hermod/s/hermod-k": {"env": "", "in.fifo": "fifo", "out.log": "first answer\n", "err.log": "", "out.log.pos": "0"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key := mustKey(t, "hermod-k")
			r := &remote{sessions: map[string]session{}, dirs: tc.before}
			if tc.appears {
				r.appears = key.String()
			}
			spec := tc.spec
			spec.Key = key
			if _, err := Start(context.Background(), Remote{Exec: r, Read: r}, spec); !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if !reflect.DeepEqual(r.dirs, tc.want) {
				t.Errorf("directories = %q, want %q", r.dirs, tc.want)
			}
		})
	}
}

func TestNewKey(t *testing.T) {
	cases := []struct {
		name    string
		wantErr bool
	}{
		{"hermod-0b5e", false},
		{"hermod-acp-ws_1", false},
		{"me@box+1", false},
		{"", true},
		{"-s", true},
		{"a.b", true},
		{"a:b", true},
		{"a/b", true},
		{"a b", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewKey(tc.name); (err != nil) != tc.wantErr {
				t.Errorf("NewKey(%q) error = %v, want error %v", tc.name, err, tc.wantErr)
			}
		})
	}
}

func TestNewTag(t *testing.T) {
	cases := []struct {
		name, value string
		wantErr     bool
	}{
		{"@hermod-workspace", "hermod-box-api-1f2e", false},
		{"@hermod-origin", "laptop.local", false},
		{"hermod-origin", "x", true},
		{"@", "x", true},
		{"@hermod-origin", "", true},
		{"@hermod-origin", "a b", false},
		{"@hermod-origin", "a;", false},
		{"@hermod-origin", "a\tb", true},
		{"@hermod-origin", "a\nb", true},
	}
	for _, tc := range cases {
		t.Run(tc.name+"="+tc.value, func(t *testing.T) {
			if _, err := NewTag(tc.name, tc.value); (err != nil) != tc.wantErr {
				t.Errorf("NewTag(%q, %q) error = %v, want error %v", tc.name, tc.value, err, tc.wantErr)
			}
		})
	}
}

func TestSpecRefused(t *testing.T) {
	cases := []struct {
		name string
		spec func(t *testing.T) Spec
	}{
		{"no key", func(t *testing.T) Spec { return Spec{} }},
		{"unknown form", func(t *testing.T) Spec { return Spec{Key: mustKey(t, "k"), Form: Form(7)} }},
		{"a reserved file", func(t *testing.T) Spec { return Spec{Key: mustKey(t, "k"), Files: []File{{Name: envFile}}} }},
		{"a file outside the directory", func(t *testing.T) Spec { return Spec{Key: mustKey(t, "k"), Files: []File{{Name: "../x"}}} }},
		{"a log's read position", func(t *testing.T) Spec { return Spec{Key: mustKey(t, "k"), Files: []File{{Name: "out.log.pos"}}} }},
		{"a hidden file", func(t *testing.T) Spec { return Spec{Key: mustKey(t, "k"), Files: []File{{Name: ".x"}}} }},
		{"a file twice", func(t *testing.T) Spec {
			return Spec{Key: mustKey(t, "k"), Files: []File{{Name: "a.json"}, {Name: "a.json"}}}
		}},
		{"a variable no shell can set", func(t *testing.T) Spec {
			return Spec{Key: mustKey(t, "k"), Vars: env.New(env.Pair{Key: "A.B", Value: env.Plain("1")})}
		}},
		{"a zero tag", func(t *testing.T) Spec { return Spec{Key: mustKey(t, "k"), Tags: []Tag{{}}} }},
		{"the variable the worker sets itself", func(t *testing.T) Spec {
			return Spec{Key: mustKey(t, "k"), Vars: env.New(env.Pair{Key: env.WorkerDir, Value: env.Plain("/elsewhere")})}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &remote{sessions: map[string]session{}}
			if _, err := Start(context.Background(), Remote{Exec: r, Read: r}, tc.spec(t)); err == nil {
				t.Fatal("Start succeeded")
			}
			if len(r.calls) != 0 {
				t.Errorf("calls = %q, want none", r.calls)
			}
		})
	}
}

func TestFilePrintsMasked(t *testing.T) {
	cases := []struct {
		format string
		file   File
		secret string
	}{
		{"%v", File{Name: "token", Content: []byte("tok-1")}, "tok-1"},
		{"%+v", File{Name: "token", Content: []byte("tok-2")}, "tok-2"},
		{"%#v", File{Name: "token", Content: []byte("tok-3")}, "tok-3"},
		{"%s", File{Name: "token", Content: []byte("tok-4")}, "tok-4"},
		{"%q", File{Name: "token", Content: []byte("tok-5")}, "tok-5"},
	}
	for _, tc := range cases {
		t.Run(tc.format, func(t *testing.T) {
			if got := fmt.Sprintf(tc.format, tc.file); strings.Contains(got, tc.secret) || !strings.Contains(got, tc.file.Name) {
				t.Errorf("Sprintf(%s) = %q", tc.format, got)
			}
		})
	}
}

// canned is a remote that answers every command with one output, and records
// the commands.
type canned struct {
	out   string
	err   error
	calls []call
}

func (c *canned) Exec(_ context.Context, cmd shell.Command) shell.Execution {
	c.calls = append(c.calls, call{args: cmd.Args})
	if cmd.Stdout != nil {
		_, _ = io.WriteString(cmd.Stdout, c.out)
	}
	return shell.Finished(shell.Command{Stdin: cmd.Stdin, Stdout: cmd.Stdout, Stderr: cmd.Stderr}, c.err)
}

func TestLook(t *testing.T) {
	cases := []struct {
		name      string
		cmd       []string
		files     []string
		out       string
		err       error
		wantArgs  []string
		want      Look
		wantErr   bool
		wantCalls int
	}{
		{
			"a running worker with its files and the command's output",
			[]string{"claude", "agents", "--json"}, []string{"port", "token"},
			"state 0\nfile port 6\n20001\nfile token 3\nabcout\n[{\"id\":1}]\n", nil,
			actArgs("look", deadK, ".hermod/s/hermod-k", `sh -c 'exec "$SHELL" -lc "$1"' sh 'claude agents --json'`, "port", "token"),
			Look{State: Running, Files: []File{{Name: "port", Content: []byte("20001\n")}, {Name: "token", Content: []byte("abc")}}, Out: []byte("[{\"id\":1}]\n")},
			false, 1,
		},
		{
			"an exited worker, no file and no command",
			nil, []string{"port"},
			"state 1\nout\n", nil,
			actArgs("look", deadK, ".hermod/s/hermod-k", "", "port"),
			Look{State: Exited, Out: []byte{}},
			false, 1,
		},
		{
			"an absent worker, a command word quoted",
			[]string{"probe", "a b"}, nil,
			"state absent\nout\n", nil,
			actArgs("look", deadK, ".hermod/s/hermod-k", `sh -c 'exec "$SHELL" -lc "$1"' sh 'probe '\''a b'\'''`),
			Look{State: Absent, Out: []byte{}},
			false, 1,
		},
		{"an unreadable state", nil, nil, "state x\nout\n", nil, nil, Look{}, true, 1},
		{"a file longer than the output", nil, []string{"port"}, "state 0\nfile port 9\n1\nout\n", nil, nil, Look{}, true, 1},
		{"an output cut short", nil, nil, "state 0\n", nil, nil, Look{}, true, 1},
		{"a command that fails", []string{"false"}, nil, "state 0\nout\n", exitError(1), nil, Look{}, true, 1},
		{"a reserved file name", nil, []string{"pid"}, "", nil, nil, Look{}, true, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec, read := &canned{}, &canned{out: tc.out, err: tc.err}
			got, err := New(Remote{Exec: exec, Read: read}, mustKey(t, "hermod-k")).Look(context.Background(), tc.cmd, tc.files...)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, want error %v", err, tc.wantErr)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Look = %+v, want %+v", got, tc.want)
			}
			if len(read.calls) != tc.wantCalls || len(exec.calls) != 0 {
				t.Fatalf("reads %d, changes %d; want %d reads and no change", len(read.calls), len(exec.calls), tc.wantCalls)
			}
			if tc.wantArgs != nil && !reflect.DeepEqual(read.calls[0].args, tc.wantArgs) {
				t.Errorf("args = %q, want %q", read.calls[0].args, tc.wantArgs)
			}
		})
	}
}

func TestLookFile(t *testing.T) {
	look := Look{Files: []File{{Name: "port", Content: []byte("1")}}}
	cases := []struct {
		name   string
		file   string
		want   File
		wantOK bool
	}{
		{"a file the look found", "port", File{Name: "port", Content: []byte("1")}, true},
		{"a file the directory lacks", "token", File{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := look.File(tc.file)
			if ok != tc.wantOK || !reflect.DeepEqual(got, tc.want) {
				t.Errorf("File = %v %v, want %v %v", got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestFreePort(t *testing.T) {
	cases := []struct {
		name       string
		record     string
		candidates []int
		out        string
		err        error
		wantArgs   []string
		want       int
		wantErr    error
	}{
		{"the first free candidate", "port", []int{20001, 20002}, "20002\n", nil,
			actArgs("port", ".hermod/s", "port", "/proc/net", "20001", "20002"), 20002, nil},
		{"every candidate taken", "port", []int{20001}, "", exitError(refusedStatus),
			actArgs("port", ".hermod/s", "port", "/proc/net", "20001"), 0, ErrNoPort},
		{"an answer that is no candidate", "port", []int{20001}, "22\n", nil,
			actArgs("port", ".hermod/s", "port", "/proc/net", "20001"), 0, errUnreadable},
		{"a connection that fails", "port", []int{20001}, "", exitError(255),
			actArgs("port", ".hermod/s", "port", "/proc/net", "20001"), 0, errUnreadable},
		{"a record outside the worker's directory", "../port", []int{20001}, "", nil, nil, 0, errUnreadable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec, read := &canned{}, &canned{out: tc.out, err: tc.err}
			got, err := FreePort(context.Background(), Remote{Exec: exec, Read: read}, tc.record, tc.candidates)
			switch {
			case tc.wantErr == errUnreadable && err == nil, tc.wantErr != errUnreadable && !errors.Is(err, tc.wantErr):
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("port = %d, want %d", got, tc.want)
			}
			if len(exec.calls) != 0 {
				t.Errorf("changes = %q, want none", exec.calls)
			}
			if tc.wantArgs != nil && (len(read.calls) != 1 || !reflect.DeepEqual(read.calls[0].args, tc.wantArgs)) {
				t.Errorf("reads = %q, want %q", read.calls, tc.wantArgs)
			}
		})
	}
}

// errUnreadable stands, in a table, for any error that is not one of the
// package's own.
var errUnreadable = errors.New("any error")

// TestPortScript runs act.sh's port operation under a local sh against records
// in a temporary home and socket tables in a temporary directory, since the
// fakes above only replay its answer and the host's own sockets must not
// decide a case.
func TestPortScript(t *testing.T) {
	const header = "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"
	cases := []struct {
		name       string
		records    map[string]string
		sockets    map[string]string // the socket tables by name
		candidates []string
		wantOut    string
		wantStatus int
	}{
		{"records without a final newline", map[string]string{"a": "20001", "b": "20002"}, nil, []string{"20001", "20002"}, "", refusedStatus},
		{"records with a final newline", map[string]string{"a": "20001\n", "b": "20002\n"}, nil, []string{"20001", "20002"}, "", refusedStatus},
		{"a candidate that only prefixes a record", map[string]string{"a": "200011"}, nil, []string{"20001"}, "20001\n", 0},
		{"no worker records a port", nil, nil, []string{"20001"}, "20001\n", 0},
		{"a socket listening on a candidate", nil,
			map[string]string{"tcp": header + "   0: 0100007F:4E21 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 1\n"},
			[]string{"20001", "20002"}, "20002\n", 0},
		{"a socket connected to a candidate", nil,
			map[string]string{"tcp6": header + "   0: 00000000000000000000000001000000:9C40 00000000000000000000000001000000:4E21 01 00000000:00000000 00:00000000 00000000  1000        0 1\n"},
			[]string{"20001", "20002"}, "20002\n", 0},
		{"every candidate in use", nil,
			map[string]string{"tcp": header + "   0: 0100007F:4E21 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 1\n"},
			[]string{"20001"}, "", refusedStatus},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			for dir, content := range tc.records {
				path := filepath.Join(home, ".hermod", "s", dir)
				if err := os.MkdirAll(path, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(path, "port"), []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			net := t.TempDir()
			for name, content := range tc.sockets {
				if err := os.WriteFile(filepath.Join(net, name), []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command("sh", actArgs("port", append([]string{".hermod/s", "port", net}, tc.candidates...)...)[1:]...)
			cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
			out, err := cmd.Output()
			status := 0
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				status = exitErr.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			if string(out) != tc.wantOut || status != tc.wantStatus {
				t.Errorf("port = %q status %d, want %q status %d", out, status, tc.wantOut, tc.wantStatus)
			}
		})
	}
}

// runScript runs a script under a local sh with home as $HOME, the way the
// remote runs it, and returns what it wrote to stdout and its exit status.
func runScript(t *testing.T, home string, args []string, stdin io.Reader) (string, int) {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "SHELL=/bin/sh"}
	cmd.Stdin = stdin
	out, err := cmd.Output()
	var exitErr *exec.ExitError
	switch {
	case errors.As(err, &exitErr):
		return string(out), exitErr.ExitCode()
	case err != nil:
		t.Fatal(err)
	}
	return string(out), 0
}

// TestActScript runs act.sh's operations that take tmux command lines under a
// local sh, each line a stand-in for what tmux would answer, since the fakes
// above only replay the script's answers.
func TestActScript(t *testing.T) {
	const (
		absent      = "(exit 1)" // what shell's HAS and DEAD exit with for a missing session
		unreadable  = `echo "lost server" >&2; (exit 2)`
		running     = "echo 0"
		dead        = "echo 1"
		present     = "true"
		killed      = "echo killed"
		wdir        = ".hermod/s/hermod-k"
		listed      = `printf '0\thermod-k\n'`
		joined      = "echo joined"
		lookCommand = "echo answer"
	)
	cases := []struct {
		name       string
		args       []string
		wantOut    string
		wantStatus int
	}{
		{"list", []string{"list", listed}, "0\thermod-k\nend\n", 0},
		{"list with no session", []string{"list", "true"}, "end\n", 0},
		{"list tmux cannot answer", []string{"list", unreadable}, "", readFailed},
		{"remove an exited worker", []string{"remove", dead, killed}, "killed\n", 0},
		{"remove a running worker", []string{"remove", running, killed}, "", refusedStatus},
		{"remove an absent worker", []string{"remove", absent, killed}, "", 0},
		{"remove when tmux cannot tell", []string{"remove", unreadable, killed}, "", readFailed},
		{"close a running worker", []string{"close", running, killed, wdir, "pid", "HERMOD_WORKER_DIR"}, "killed\n", 0},
		{"close an exited worker", []string{"close", dead, killed, wdir, "pid", "HERMOD_WORKER_DIR"}, "", refusedStatus},
		{"close an absent worker", []string{"close", absent, killed, wdir, "pid", "HERMOD_WORKER_DIR"}, "", 0},
		{"close when tmux cannot tell", []string{"close", unreadable, killed, wdir, "pid", "HERMOD_WORKER_DIR"}, "", readFailed},
		{"attach", []string{"attach", present, joined}, "joined\n", 0},
		{"attach to nothing", []string{"attach", absent, joined}, "", refusedStatus},
		{"look at a running worker", []string{"look", running, wdir, lookCommand}, "state 0\nout\nanswer\n", 0},
		{"look at an exited worker", []string{"look", dead, wdir, ""}, "state 1\nout\n", 0},
		{"look at an absent worker", []string{"look", absent, wdir, ""}, "state absent\nout\n", 0},
		{"look when tmux cannot tell", []string{"look", unreadable, wdir, ""}, "", readFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, status := runScript(t, t.TempDir(), actArgs(tc.args[0], tc.args[1:]...), nil)
			if out != tc.wantOut || status != tc.wantStatus {
				t.Errorf("out = %q status %d, want %q status %d", out, status, tc.wantOut, tc.wantStatus)
			}
		})
	}
}

// TestKeepScript runs keep.sh under a local sh, with stand-ins for tmux's
// answer to HAS and for the command that makes the session, against a
// temporary home that holds the worker's directory and its staging one.
func TestKeepScript(t *testing.T) {
	const (
		made  = "made" // the file the stand-in for making the session leaves under the home
		taken = `[ -e "$HOME/made" ]`
	)
	cases := []struct {
		name       string
		has        string
		make       string
		wantStatus int
		want       map[string]string // the files under the home, by path
	}{
		{"a free key", "false", `touch "$HOME/made"`, 0,
			map[string]string{".hermod/s/hermod-k/env": "new", made: ""}},
		{"a key a session holds", "true", `touch "$HOME/made"`, refusedStatus,
			map[string]string{".hermod/s/hermod-k/env": "old"}},
		{"a key another start takes meanwhile", taken, `touch "$HOME/made"; false`, refusedStatus,
			map[string]string{".hermod/s/hermod-k/env": "old", made: ""}},
		{"tmux fails to make the session", "false", "false", 1,
			map[string]string{".hermod/s/hermod-k/env": "old"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			for dir, content := range map[string]string{".hermod/s/hermod-k": "old", ".hermod/s/.hermod-k.0": "new"} {
				if err := os.MkdirAll(filepath.Join(home, dir), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(home, dir, "env"), []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			args := []string{"sh", "-s", "--", ".hermod/s/hermod-k", ".hermod/s/.hermod-k.0", strconv.Itoa(refusedStatus), tc.has, "sh", "-c", tc.make}
			_, status := runScript(t, home, args, strings.NewReader(string(keepScript)))
			if status != tc.wantStatus {
				t.Errorf("status %d, want %d", status, tc.wantStatus)
			}
			if got := filesUnder(t, home); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("files = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestPaneScript runs pane.sh under a local sh against a temporary home, its
// worker's directory already in place, with a command that reports where it
// runs and the variables it was given. "~" in a table stands for the home.
func TestPaneScript(t *testing.T) {
	const report = `printf '%s %s %s\n' "$PWD" "$A" "$HERMOD_WORKER_DIR"`
	cases := []struct {
		name       string
		dir        string
		form       string
		wantOut    string
		wantStatus int
		want       map[string]string // the files under the home, by path
	}{
		{"a terminal under the home", "proj", "terminal", "~/proj 1 ~/.hermod/s/hermod-k\n", 0,
			map[string]string{".hermod/s/hermod-k/env": "export A=1 HERMOD_WORKER_DIR=wrong\n", "kept": ""}},
		{"a line stream at an absolute directory", "~/proj", "stream", "", 0,
			map[string]string{
				".hermod/s/hermod-k/env":     "export A=1 HERMOD_WORKER_DIR=wrong\n",
				".hermod/s/hermod-k/in.fifo": "",
				".hermod/s/hermod-k/out.log": "~/proj 1 ~/.hermod/s/hermod-k\n",
				".hermod/s/hermod-k/err.log": "",
				".hermod/s/hermod-k/pid":     "<pid>",
				"kept":                       "",
			}},
		{"a directory that is not there", "gone", "terminal", "", 1,
			map[string]string{".hermod/s/hermod-k/env": "export A=1 HERMOD_WORKER_DIR=wrong\n", "kept": ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			wd := filepath.Join(home, ".hermod", "s", "hermod-k")
			if err := os.MkdirAll(wd, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(home, "proj"), 0o700); err != nil {
				t.Fatal(err)
			}
			files := map[string]string{"env": "export A=1 HERMOD_WORKER_DIR=wrong\n", "in.fifo": "", "out.log": "", "err.log": ""}
			if tc.form != "stream" {
				files = map[string]string{"env": files["env"]}
			}
			for name, content := range files {
				if err := os.WriteFile(filepath.Join(wd, name), []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			args := []string{"sh", "-c", string(paneScript), "pane",
				".hermod/s/hermod-k", ".hermod/s/.hermod-k.0", strings.ReplaceAll(tc.dir, "~", home), "HERMOD_WORKER_DIR", "pid", "env",
				tc.form, "in.fifo", "out.log", "err.log", `touch "$HOME/kept"`, "sh", "-c", report}
			out, status := runScript(t, home, args, nil)
			if want := strings.ReplaceAll(tc.wantOut, "~", home); out != want || status != tc.wantStatus {
				t.Errorf("out = %q status %d, want %q status %d", out, status, want, tc.wantStatus)
			}
			got := filesUnder(t, home)
			if _, ok := got[".hermod/s/hermod-k/pid"]; ok {
				got[".hermod/s/hermod-k/pid"] = "<pid>"
			}
			want := map[string]string{}
			for name, content := range tc.want {
				want[name] = strings.ReplaceAll(content, "~", home)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("files = %q, want %q", got, want)
			}
		})
	}
}

// filesUnder is every regular file under root, by its path from root.
func filesUnder(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		files[rel] = string(content)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}
