package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/zerlok/hermod/internal/env"
	"github.com/zerlok/hermod/internal/secret"
	"github.com/zerlok/hermod/internal/shell"
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
	switch {
	case stdin == string(keepScript):
		return "", r.start(args[3:])
	case stdin == string(streamScript):
		return "", r.stream(args[3:])
	case len(args) > 4 && args[2] == string(writeScript):
		return "", r.write(args[4], args[5:], stdin)
	case len(args) > 8 && args[2] == string(actScript):
		switch op, target := args[7], strings.Trim(args[8], "=:"); op {
		case "list":
			return r.list(args[8])
		case "close":
			return "", r.close(target)
		case "remove":
			return "", r.remove(target)
		}
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

// start makes the session, then puts the staging directory in place of the
// worker's; a session taken already only drops the staging directory.
func (r *remote) start(args []string) error {
	name, wd, staging := args[0], args[1], args[2]
	if _, ok := r.sessions[name]; ok {
		delete(r.dirs, staging)
		code, _ := strconv.Atoi(args[11])
		return exitError(code)
	}
	tags := map[string]string{}
	n, _ := strconv.Atoi(args[12])
	for i := 0; i < n; i++ {
		tags[args[13+2*i]] = args[14+2*i]
	}
	state := r.failsAs
	if state == Absent {
		state = Running
	}
	r.sessions[name] = session{state: state, tags: tags}
	r.dirs[wd] = r.dirs[staging]
	delete(r.dirs, staging)
	return nil
}

func (r *remote) list(format string) (string, error) {
	if r.listErr != nil {
		return "", r.listErr
	}
	if r.listOut != "" {
		return r.listOut, nil
	}
	if r.appears != "" {
		defer func() { r.sessions[r.appears], r.appears = session{state: Running}, "" }()
	}
	tag := strings.TrimSuffix(strings.TrimPrefix(strings.Fields(format)[1], "#{"), "}")
	var b strings.Builder
	for name, s := range r.sessions {
		dead := map[State]string{Running: "0", Exited: "1"}[s.state]
		fmt.Fprintf(&b, "%s %s %s\n", dead, s.tags[tag], name)
	}
	b.WriteString("end\n")
	return b.String(), nil
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
		{"absent", "0  other\nend\n", nil, Absent, false},
		{"running", "1  other\n0  hermod-k\nend\n", nil, Running, false},
		{"exited", "1  hermod-k\nend\n", nil, Exited, false},
		{"no server", "end\n", nil, Absent, false},
		{"a name with spaces", "0  my session\n0  hermod-k\nend\n", nil, Running, false},
		{"incomplete output", "0  hermod-k\n", nil, Absent, true},
		{"unreadable state", "x  hermod-k\nend\n", nil, Absent, true},
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
	return append([]string{"sh", "-c", string(actScript), "act", "3", "4", "5", op}, args...)
}

// stagingName matches a staging directory's name, whose random part a table
// cannot know; staged stands for it in a table.
var stagingName = regexp.MustCompile(`^\.hermod/s/\.[^/]+\.[0-9a-f]{16}$`)

const staged = "<staging>"

func withoutStaging(calls []call) []call {
	out := make([]call, len(calls))
	for i, c := range calls {
		args := make([]string, len(c.args))
		for j, a := range c.args {
			if stagingName.MatchString(a) {
				a = staged
			}
			args[j] = a
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
			"terminal",
			func(t *testing.T) Spec {
				return Spec{
					Key:     mustKey(t, "hermod-k"),
					Dir:     "/srv/api",
					Command: []string{"claude", "--resume", "id"},
					Vars:    env.New(env.Pair{Key: "GIT_AUTHOR_NAME", Value: env.Plain("Jane O'Neil")}),
					Tags:    []Tag{mustTag(t, "@hermod-workspace", "ws"), mustTag(t, "@hermod-origin", "box")},
					Form:    Terminal,
				}
			},
			[]call{
				{args: actArgs("list", "#{pane_dead}  #{session_name}")},
				{
					args:  []string{"sh", "-c", string(writeScript), "write", staged, "env", "40"},
					stdin: "export GIT_AUTHOR_NAME='Jane O'\\''Neil'\n",
				},
				{
					args: []string{"sh", "-s", "--", "hermod-k", ".hermod/s/hermod-k", staged, "/srv/api", "HERMOD_WORKER", "pid", "env",
						"terminal", "in.fifo", "out.log", "err.log", "3", "2",
						"@hermod-workspace", "ws", "@hermod-origin", "box", "claude", "--resume", "id"},
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
				{args: actArgs("list", "#{pane_dead}  #{session_name}")},
				{
					args:  []string{"sh", "-c", string(writeScript), "write", staged, "env", "0", "settings.json", "2", "mcp.json", "3"},
					stdin: "{}[1]",
				},
				{
					args:  []string{"sh", "-s", "--", staged, "in.fifo", "out.log", "err.log"},
					stdin: string(streamScript),
				},
				{
					args: []string{"sh", "-s", "--", "hermod-acp-ws", ".hermod/s/hermod-acp-ws", staged, "proj", "HERMOD_WORKER", "pid", "env",
						"stream", "in.fifo", "out.log", "err.log", "3", "0", "sh", "-c", `a\;`, `\;`},
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
			[][]string{actArgs("list", "#{pane_dead} #{@hermod-workspace} #{session_name}")},
			false,
		},
		{
			"none",
			map[string]session{},
			func(t *testing.T) Tag { return mustTag(t, "@hermod-origin", "box") },
			map[string]State{},
			[][]string{actArgs("list", "#{pane_dead} #{@hermod-origin} #{session_name}")},
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
		{"close", opClose, actArgs("close", "=hermod-k:", ".hermod/s/hermod-k", "pid", "HERMOD_WORKER")},
		{"remove", opRemove, actArgs("remove", "=hermod-k:")},
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
		{"terminal", Terminal, Attach{Command: shell.Command{Args: actArgs("attach", "=hermod-k:")}}, false},
		{"line stream", LineStream, Lines{
			In:  shell.Command{Args: actArgs("feed", ".hermod/s/hermod-k/in.fifo")},
			Out: shell.Command{Args: actArgs("log", "=hermod-k:", ".hermod/s/hermod-k/out.log", ".hermod/s/hermod-k/out.log.pos")},
			Err: shell.Command{Args: actArgs("log", "=hermod-k:", ".hermod/s/hermod-k/err.log", ".hermod/s/hermod-k/err.log.pos")},
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
		{"@Hermod", "x", true},
		{"@hermod-origin", "", true},
		{"@hermod-origin", "a b", true},
		{"@hermod-origin", "a;", true},
		{"@hermod-origin", "a\tb", true},
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

func TestKeyDir(t *testing.T) {
	cases := []struct {
		home, key, want string
	}{
		{"/home/u", "hermod-k", "/home/u/.hermod/s/hermod-k"},
		{"/root/", "hermod-acp-ws", "/root/.hermod/s/hermod-acp-ws"},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			if got := mustKey(t, tc.key).Dir(tc.home); got != tc.want {
				t.Errorf("Dir = %q, want %q", got, tc.want)
			}
		})
	}
}
