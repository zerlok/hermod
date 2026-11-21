package shell

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// recShell records the last command it was asked to run and returns canned data.
type recShell struct {
	got Command
	ret Result
	err error
}

func (r *recShell) Run(_ context.Context, cmd Command) (Result, error) {
	r.got = cmd
	return r.ret, r.err
}

func TestSSHRewrite(t *testing.T) {
	cases := []struct {
		name string
		host string
		opts []SSHOption
		in   Command
		want []string
	}{
		{"probe no tty", "prod", nil, Command{Argv: []string{"tmux", "has-session", "-t", "api"}}, []string{"ssh", "prod", "tmux has-session -t api"}},
		{"interactive tty", "prod", []SSHOption{WithTty()}, Command{Argv: []string{"tmux", "new-session", "-A"}}, []string{"ssh", "-t", "prod", "tmux new-session -A"}},
		{"quotes sandbox arg with space", "box", nil, Command{Argv: []string{"tmux", "new-session", "claude --model opus"}}, []string{"ssh", "box", "tmux new-session 'claude --model opus'"}},
		{
			"reverse forward after tty before host",
			"prod", []SSHOption{WithTty(), WithReverseForward(45001, 38211)},
			Command{Argv: []string{"tmux", "attach"}},
			[]string{"ssh", "-t", "-R", "45001:127.0.0.1:38211", "prod", "tmux attach"},
		},
		{
			"own connection after tty before forwards",
			"prod", []SSHOption{WithReverseForward(45001, 38211), WithoutSharedMaster(), WithTty()},
			Command{Argv: []string{"tmux", "attach"}},
			[]string{"ssh", "-t", "-o", "ControlPath=none", "-R", "45001:127.0.0.1:38211", "prod", "tmux attach"},
		},
		{"own connection probe", "prod", []SSHOption{WithoutSharedMaster()}, Command{Argv: []string{"true"}}, []string{"ssh", "-o", "ControlPath=none", "prod", "true"}},
		{
			"two reverse forwards accumulate",
			"prod", []SSHOption{WithTty(), WithReverseForward(45001, 1), WithReverseForward(45002, 2)},
			Command{Argv: []string{"tmux", "attach"}},
			[]string{"ssh", "-t", "-R", "45001:127.0.0.1:1", "-R", "45002:127.0.0.1:2", "prod", "tmux attach"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inner := &recShell{}
			NewSSH(inner, tc.host, tc.opts...).Run(context.Background(), tc.in)
			if !reflect.DeepEqual(inner.got.Argv, tc.want) {
				t.Errorf("argv = %q, want %q", inner.got.Argv, tc.want)
			}
			if inner.got.Dir != "" || inner.got.Env != nil {
				t.Errorf("sandbox intent leaked: dir=%q env=%v", inner.got.Dir, inner.got.Env)
			}
		})
	}
}

func TestTmuxRewrite(t *testing.T) {
	tags := []SessionOption{{"@hermod-mode", "attach"}, {"@hermod-dir", "dev/my api"}}
	cases := []struct {
		name string
		sess string
		opts []TmuxOption
		in   Command
		want []string
	}{
		{"default shell no command", "api", nil, Command{}, []string{"tmux", "new-session", "-A", "-s", "api"}},
		{"with dir", "api", nil, Command{Dir: "/home/u/api"}, []string{"tmux", "new-session", "-A", "-s", "api", "-c", "/home/u/api"}},
		{"a '#' in the dir is no format", "api", nil, Command{Dir: "dev/p#(id>T)C#"}, []string{"tmux", "new-session", "-A", "-s", "api", "-c", "dev/p##(id>T)C##"}},
		{"with env", "api", nil, Command{Env: []string{"GIT_AUTHOR_NAME=Jane Doe"}}, []string{"tmux", "new-session", "-A", "-s", "api", "-e", "GIT_AUTHOR_NAME=Jane Doe"}},
		{"passthrough joined", "api", nil, Command{Dir: "/home/u/api", Argv: []string{"claude", "--model", "opus"}}, []string{"tmux", "new-session", "-A", "-s", "api", "-c", "/home/u/api", "claude --model opus"}},
		{
			"session options follow the command",
			"api", []TmuxOption{WithSessionOptions(tags...)},
			Command{Dir: "dev/api", Argv: []string{"htop"}},
			[]string{"tmux", "new-session", "-A", "-s", "api", "-c", "dev/api", "htop", ";", "set-option", "-t", "=api:", "-qo", "@hermod-mode", "attach", ";", "set-option", "-t", "=api:", "-qo", "@hermod-dir", "dev/my api"},
		},
		{
			"detached start",
			"hermod-x", []TmuxOption{Detached(), WithSessionOptions(tags[0])},
			Command{Argv: []string{"claude"}},
			[]string{"tmux", "new-session", "-d", "-s", "hermod-x", "claude", ";", "set-option", "-t", "=hermod-x:", "-qo", "@hermod-mode", "attach"},
		},
		{
			"options target the name tmux stores",
			"dev.api:2", []TmuxOption{WithSessionOptions(tags[0])},
			Command{},
			[]string{"tmux", "new-session", "-A", "-s", "dev.api:2", ";", "set-option", "-t", "=dev_api_2:", "-qo", "@hermod-mode", "attach"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inner := &recShell{}
			NewTmux(inner, tc.sess, tc.opts...).Run(context.Background(), tc.in)
			if !reflect.DeepEqual(inner.got.Argv, tc.want) {
				t.Errorf("argv = %q, want %q", inner.got.Argv, tc.want)
			}
		})
	}
}

func TestNoInputIsEmpty(t *testing.T) {
	cases := []struct {
		name string
		buf  int
		want error
	}{
		{"empty read", 0, io.EOF},
		{"buffered read", 64, io.EOF},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n, err := NoInput.Read(make([]byte, tc.buf))
			if n != 0 || err != tc.want {
				t.Errorf("Read() = %d, %v, want 0, %v", n, err, tc.want)
			}
		})
	}
}

func TestLoginShellRewrite(t *testing.T) {
	cases := []struct {
		name string
		in   Command
		want []string
	}{
		{"empty command untouched", Command{Dir: "/home/u/api"}, nil},
		{"single word wrapped", Command{Argv: []string{"claude"}}, []string{"sh", "-c", `exec "$SHELL" -lc "$1"`, "sh", "claude"}},
		{"flags preserved in one word", Command{Argv: []string{"claude", "--model", "opus"}}, []string{"sh", "-c", `exec "$SHELL" -lc "$1"`, "sh", "claude --model opus"}},
		{"inner spaces stay quoted", Command{Argv: []string{"claude", "-p", "hi there"}}, []string{"sh", "-c", `exec "$SHELL" -lc "$1"`, "sh", "claude -p 'hi there'"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inner := &recShell{}
			NewLoginShell(inner).Run(context.Background(), tc.in)
			if !reflect.DeepEqual(inner.got.Argv, tc.want) {
				t.Errorf("argv = %q, want %q", inner.got.Argv, tc.want)
			}
		})
	}
}

// TestAttachStack asserts the full interactive stack (Tmux → SSH → leaf) yields
// one copy-pasteable ssh command line.
func TestAttachStack(t *testing.T) {
	cases := []struct {
		name    string
		host    string
		session string
		in      Command
		want    []string
	}{
		{
			"default shell",
			"prod", "api",
			Command{Dir: "/home/u/api"},
			[]string{"ssh", "-t", "prod", "tmux new-session -A -s api -c /home/u/api"},
		},
		{
			"passthrough with identity",
			"prod", "api",
			Command{Dir: "/home/u/api", Env: []string{"GIT_AUTHOR_NAME=Jane Doe"}, Argv: []string{"claude", "--model", "opus"}},
			[]string{"ssh", "-t", "prod", "tmux new-session -A -s api -c /home/u/api -e 'GIT_AUTHOR_NAME=Jane Doe' 'claude --model opus'"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inner := &recShell{}
			stack := NewTmux(NewSSH(inner, tc.host, WithTty()), tc.session)
			stack.Run(context.Background(), tc.in)
			if !reflect.DeepEqual(inner.got.Argv, tc.want) {
				t.Errorf("argv = %q, want %q", inner.got.Argv, tc.want)
			}
		})
	}
}

func TestFormat(t *testing.T) {
	cases := []struct {
		name string
		cmd  Command
		want string
	}{
		{"plain argv", Command{Argv: []string{"mutagen", "sync", "list"}}, "mutagen sync list"},
		{"quotes arg with space", Command{Argv: []string{"echo", "hello world"}}, "echo 'hello world'"},
		{"env prefix quoted value", Command{Argv: []string{"git", "commit"}, Env: []string{"GIT_AUTHOR_NAME=Jane Doe"}}, "GIT_AUTHOR_NAME='Jane Doe' git commit"},
		{"safe path unquoted", Command{Argv: []string{"ls"}, Dir: "/home/u/api"}, "cd /home/u/api && ls"},
		{"dir env and argv", Command{Argv: []string{"git", "commit"}, Dir: "/home/u/api", Env: []string{"K=v"}}, "cd /home/u/api && K=v git commit"},
		{"single quote in value", Command{Argv: []string{"echo", "it's"}}, `echo 'it'\''s'`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := format(tc.cmd); got != tc.want {
				t.Errorf("format() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDryRunLeafPrintsAndDoesNotExecute(t *testing.T) {
	cases := []struct {
		name string
		cmd  Command
		want string
	}{
		{"side effect printed", Command{Argv: []string{"mutagen", "sync", "terminate", "api"}}, "mutagen sync terminate api\n"},
		{"attach printed not run", Command{Argv: []string{"ssh", "-t", "prod", "tmux", "attach"}}, "ssh -t prod tmux attach\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			res, err := NewDryRun(&buf).Run(context.Background(), tc.cmd)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.Stdout != "" {
				t.Errorf("Stdout = %q, want empty (nothing executed)", res.Stdout)
			}
			if got := buf.String(); got != tc.want {
				t.Errorf("printed %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSubprocessCapture(t *testing.T) {
	cases := []struct {
		name       string
		argv       []string
		wantStdout string
		wantStderr string
	}{
		{"printf no newline", []string{"printf", "hello"}, "hello", ""},
		{"echo with newline", []string{"echo", "sync-ok"}, "sync-ok\n", ""},
		{"stderr captured separately", []string{"sh", "-c", "echo out; echo err >&2"}, "out\n", "err\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := runSubprocess(context.Background(), Command{Argv: tc.argv, Capture: true})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.Stdout != tc.wantStdout {
				t.Errorf("Stdout = %q, want %q", res.Stdout, tc.wantStdout)
			}
			if res.Stderr != tc.wantStderr {
				t.Errorf("Stderr = %q, want %q", res.Stderr, tc.wantStderr)
			}
		})
	}
}

func TestSubprocessEnv(t *testing.T) {
	cases := []struct {
		name string
		env  []string
		want string
	}{
		{"exported var visible", []string{"HERMOD_TEST=carried"}, "carried\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := runSubprocess(context.Background(), Command{
				Argv:    []string{"sh", "-c", "echo $HERMOD_TEST"},
				Env:     tc.env,
				Capture: true,
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.Stdout != tc.want {
				t.Errorf("Stdout = %q, want %q", res.Stdout, tc.want)
			}
		})
	}
}

func TestSubprocessEmptyArgv(t *testing.T) {
	cases := []struct {
		name string
		argv []string
	}{
		{"nil argv", nil},
		{"empty argv", []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := runSubprocess(context.Background(), Command{Argv: tc.argv})
			if err == nil {
				t.Fatal("expected error for empty argv, got nil")
			}
			if !strings.Contains(err.Error(), "argv") {
				t.Errorf("error = %v, want it to mention argv", err)
			}
		})
	}
}

// TestDecoratorsCarryStreams asserts every transport hands the command's streams
// and capture mode through to the leaf, so a stream given at the top of a stack
// is the one the child runs with.
func TestDecoratorsCarryStreams(t *testing.T) {
	cases := []struct {
		name    string
		stack   func(inner Shell) Shell
		capture bool
	}{
		{"ssh", func(in Shell) Shell { return NewSSH(in, "prod") }, false},
		{"tmux", func(in Shell) Shell { return NewTmux(in, "api") }, false},
		{"login shell", func(in Shell) Shell { return NewLoginShell(in) }, false},
		{"login over ssh", func(in Shell) Shell { return NewLoginShell(NewSSH(in, "prod")) }, false},
		{"full attach stack", func(in Shell) Shell { return NewLoginShell(NewTmux(NewSSH(in, "prod", WithTty()), "api")) }, false},
		{"capture through ssh", func(in Shell) Shell { return NewSSH(in, "prod") }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdin := strings.NewReader("in")
			var stdout, stderr bytes.Buffer
			cmd := Command{Argv: []string{"claude", "auth", "status"}, Capture: tc.capture, Stdin: stdin, Interrupt: syscall.SIGTERM}
			if !tc.capture {
				cmd.Stdout, cmd.Stderr = &stdout, &stderr
			}
			inner := &recShell{}
			tc.stack(inner).Run(context.Background(), cmd)
			if inner.got.Stdin != cmd.Stdin || inner.got.Stdout != cmd.Stdout || inner.got.Stderr != cmd.Stderr {
				t.Errorf("streams = %v, %v, %v, want %v, %v, %v",
					inner.got.Stdin, inner.got.Stdout, inner.got.Stderr, cmd.Stdin, cmd.Stdout, cmd.Stderr)
			}
			if inner.got.Capture != tc.capture {
				t.Errorf("Capture = %v, want %v", inner.got.Capture, tc.capture)
			}
			if inner.got.Interrupt != cmd.Interrupt {
				t.Errorf("Interrupt = %v, want %v", inner.got.Interrupt, cmd.Interrupt)
			}
		})
	}
}

// TestSubprocessInterrupt asserts a cancelled command given an Interrupt gets
// that signal and the time to act on it, and one without is killed at once.
func TestSubprocessInterrupt(t *testing.T) {
	const script = `trap 'echo interrupted; exit 3' TERM; echo ready; while :; do sleep 0.05; done`
	cases := []struct {
		name       string
		interrupt  os.Signal
		wantStdout string
		wantCode   int // -1: killed by a signal
	}{
		{"interrupted", syscall.SIGTERM, "ready\ninterrupted\n", 3},
		{"killed", nil, "ready\n", -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			out := &signalOnWrite{ready: make(chan struct{})}
			go func() {
				<-out.ready
				cancel()
			}()
			_, err := runSubprocess(ctx, Command{Argv: []string{"sh", "-c", script}, Stdin: NoInput, Stdout: out, Interrupt: tc.interrupt})
			code, ok := ExitCode(err)
			if !ok || code != tc.wantCode {
				t.Errorf("exit = %v (%d), want code %d", err, code, tc.wantCode)
			}
			if got := out.String(); got != tc.wantStdout {
				t.Errorf("stdout = %q, want %q", got, tc.wantStdout)
			}
		})
	}
}

// signalOnWrite is a buffer that closes ready on its first write.
type signalOnWrite struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	ready chan struct{}
	once  sync.Once
}

func (w *signalOnWrite) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	defer w.once.Do(func() { close(w.ready) })
	return w.buf.Write(p)
}

func (w *signalOnWrite) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func TestSubprocessExplicitStreams(t *testing.T) {
	cases := []struct {
		name       string
		script     string
		stdin      string
		wantStdout string
		wantStderr string
	}{
		{"stdin to stdout", "cat", "hello\n", "hello\n", ""},
		{"stdin to stderr", "cat >&2", "line\n", "", "line\n"},
		{"both streams", "echo out; echo err >&2", "", "out\n", "err\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			res, err := runSubprocess(context.Background(), Command{
				Argv:   []string{"sh", "-c", tc.script},
				Stdin:  strings.NewReader(tc.stdin),
				Stdout: &stdout,
				Stderr: &stderr,
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if stdout.String() != tc.wantStdout || stderr.String() != tc.wantStderr {
				t.Errorf("stdout, stderr = %q, %q, want %q, %q", stdout.String(), stderr.String(), tc.wantStdout, tc.wantStderr)
			}
			if res != (Result{}) {
				t.Errorf("Result = %+v, want empty: nothing is captured", res)
			}
		})
	}
}

func TestSubprocessCaptureReadsStdin(t *testing.T) {
	cases := []struct {
		name  string
		stdin io.Reader
		want  string
	}{
		{"explicit stdin", strings.NewReader("given"), "given"},
		{"no stdin reads empty", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := runSubprocess(context.Background(), Command{Argv: []string{"cat"}, Capture: true, Stdin: tc.stdin})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.Stdout != tc.want {
				t.Errorf("Stdout = %q, want %q", res.Stdout, tc.want)
			}
		})
	}
}

// TestSubprocessCaptureRejectsStreams asserts the contradictory command fails
// before anything runs.
func TestSubprocessCaptureRejectsStreams(t *testing.T) {
	cases := []struct {
		name   string
		stdout io.Writer
		stderr io.Writer
	}{
		{"stdout given", &bytes.Buffer{}, nil},
		{"stderr given", nil, &bytes.Buffer{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "ran")
			_, err := runSubprocess(context.Background(), Command{
				Argv:    []string{"touch", marker},
				Capture: true,
				Stdout:  tc.stdout,
				Stderr:  tc.stderr,
			})
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			if _, statErr := os.Stat(marker); !errors.Is(statErr, fs.ErrNotExist) {
				t.Errorf("command ran: stat %s = %v", marker, statErr)
			}
		})
	}
}

// TestSubprocessReturnsDespiteALeftChild asserts a command returns soon after it
// ends or is cancelled even while a child it left behind holds its output open,
// and that a command which succeeded still reports success.
func TestSubprocessReturnsDespiteALeftChild(t *testing.T) {
	cases := []struct {
		name    string
		argv    []string
		capture bool
		timeout time.Duration // 0: never cancelled
		within  time.Duration // well under the left child's life
		wantErr bool
	}{
		{"cancelled, captured", []string{"sh", "-c", "sleep 3 & sleep 3"}, true, 100 * time.Millisecond, 2 * time.Second, true},
		{"cancelled, streamed", []string{"sh", "-c", "sleep 3 & sleep 3"}, false, 100 * time.Millisecond, 2 * time.Second, true},
		{"succeeded, captured", []string{"sh", "-c", "sleep 3 & echo done"}, true, 0, 2 * time.Second, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.timeout)
				defer cancel()
			}
			cmd := Command{Argv: tc.argv, Capture: tc.capture, Stdin: NoInput}
			if !tc.capture {
				cmd.Stdout, cmd.Stderr = &bytes.Buffer{}, &bytes.Buffer{}
			}
			start := time.Now()
			_, err := runSubprocess(ctx, cmd)
			if took := time.Since(start); took > tc.within {
				t.Errorf("returned after %v, want within %v", took, tc.within)
			}
			if (err != nil) != tc.wantErr {
				t.Errorf("error = %v, want one: %v", err, tc.wantErr)
			}
		})
	}
}
