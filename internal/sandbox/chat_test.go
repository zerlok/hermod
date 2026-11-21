package sandbox

import (
	"bytes"
	"context"
	"errors"
	"os"
	osexec "os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/zerlok/hermod/internal/claude"
	"github.com/zerlok/hermod/internal/shell"
)

func chatAgent(exec shell.Shell) *Agent {
	id, _ := claude.ParseSessionID(agentUUID)
	return NewAgent(exec, &scriptedShell{}, AgentConfig{
		Host:    "prod",
		ID:      id,
		Dir:     "dev/api",
		Tags:    Tags{Mode: ModeAirACP, Dir: "dev/api"},
		Runtime: RuntimeChat,
	})
}

// TestStartChat asserts a chat session's start makes its stream files ready
// before tmux runs Claude on them.
func TestStartChat(t *testing.T) {
	quoted := func(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
	cases := []struct {
		name      string
		argv      []string
		env       []string
		wantArgvs [][]string
	}{
		{
			"files first, then Claude on them",
			[]string{"claude", "--input-format", "stream-json", "--session-id", agentUUID},
			[]string{"HERMOD_MODE=air-acp"},
			[][]string{
				{"ssh", "prod", "sh -c " + quoted(chatFilesScript) + " sh " + agentUUID},
				{"ssh", "prod", "tmux new-session -d -s hermod-" + agentUUID + " -c dev/api -e HERMOD_MODE=air-acp " +
					quoted("sh -c "+quoted(`exec "$SHELL" -lc "$1"`)+" sh "+quoted("sh -c "+quoted(chatStartScript)+" sh "+agentUUID+" claude --input-format stream-json --session-id "+agentUUID)) +
					" ';' set-option -t =hermod-" + agentUUID + ": -qo @hermod-mode air-acp ';' set-option -t =hermod-" + agentUUID + ": -qo @hermod-dir dev/api"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec := &scriptedShell{}
			if err := chatAgent(exec).Start(context.Background(), tc.argv, tc.env); err != nil {
				t.Fatalf("Start() error: %v", err)
			}
			var got [][]string
			for _, c := range exec.calls {
				got = append(got, c.cmd.Argv)
				if !c.cmd.Capture {
					t.Errorf("%q does not capture: it would read this process's stdin", c.cmd.Argv)
				}
			}
			if !reflect.DeepEqual(got, tc.wantArgvs) {
				t.Errorf("argvs =\n %q\nwant\n %q", got, tc.wantArgvs)
			}
		})
	}
}

// TestStartChatFailure asserts a start whose stream files cannot be made runs
// no Claude.
func TestStartChatFailure(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		wantErr   string
		wantCalls int
	}{
		{"files not made", exitErr{code: 1}, "prepare chat stream files on prod: exit status: boom", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec := &scriptedShell{ret: shell.Result{Stderr: "boom\n"}, err: tc.err}
			err := chatAgent(exec).Start(context.Background(), []string{"claude"}, nil)
			if err == nil || err.Error() != tc.wantErr {
				t.Errorf("err = %v, want %q", err, tc.wantErr)
			}
			if len(exec.calls) != tc.wantCalls {
				t.Errorf("calls = %d, want %d", len(exec.calls), tc.wantCalls)
			}
		})
	}
}

func TestAgentClose(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantArgv []string
		wantErr  string // "" for success
	}{
		{"closed", nil, []string{"ssh", "prod", "sh -c '" + strings.ReplaceAll(closeScript, "'", `'\''`) + "' sh " + agentUUID}, ""},
		{"failed", exitErr{code: 255}, nil, "close session " + agentUUID + " on prod: exit status"},
		{"Claude outlives the hang-up", exitErr{code: closeLingersStatus}, nil, "close session " + agentUUID + " on prod: " + ErrClaudeLingers.Error()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec := &scriptedShell{err: tc.err}
			err := chatAgent(exec).Close(context.Background())
			if (tc.wantErr == "") != (err == nil) || err != nil && err.Error() != tc.wantErr {
				t.Fatalf("Close() = %v, want %q", err, tc.wantErr)
			}
			if tc.wantArgv != nil && !reflect.DeepEqual(exec.calls[0].cmd.Argv, tc.wantArgv) {
				t.Errorf("argv =\n %q\nwant\n %q", exec.calls[0].cmd.Argv, tc.wantArgv)
			}
			if !exec.calls[0].cmd.Capture {
				t.Error("close may read this process's stdin")
			}
		})
	}
}

func TestAttachChat(t *testing.T) {
	remote := "sh -c '" + strings.ReplaceAll(attachChatScript, "'", `'\''`) + "' sh " + agentUUID
	cases := []struct {
		name     string
		fwd      *Forward
		stdin    string
		wantArgv []string
	}{
		{"own connection, no tty", nil, "{}\n", []string{"ssh", "-o", "ControlPath=none", "prod", remote}},
		{"with the hook forward", &Forward{SandboxPort: 45001, LocalPort: 38211}, "{}\n", []string{"ssh", "-o", "ControlPath=none", "-R", "45001:127.0.0.1:38211", "prod", remote}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec := &scriptedShell{}
			streams := Streams{In: strings.NewReader(tc.stdin), Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
			if err := chatAgent(exec).AttachChat(context.Background(), tc.fwd, streams); err != nil {
				t.Fatalf("AttachChat() error: %v", err)
			}
			got := exec.calls[0]
			if !reflect.DeepEqual(got.cmd.Argv, tc.wantArgv) {
				t.Errorf("argv =\n %q\nwant\n %q", got.cmd.Argv, tc.wantArgv)
			}
			if got.stdin != tc.stdin || got.cmd.Stdout != streams.Out || got.cmd.Capture {
				t.Error("the attach does not run with the given streams")
			}
		})
	}
}

// TestAttachChatOutcome asserts an attach the script ends for want of a Claude
// reports the session gone, and any other failure passes through.
func TestAttachChatOutcome(t *testing.T) {
	sshFailed := exitErr{code: 255}
	cases := []struct {
		name    string
		err     error
		wantErr error // nil: success
	}{
		{"detached", nil, nil},
		{"no Claude", exitErr{code: chatGoneStatus}, ErrSessionGone},
		{"connection dropped", sshFailed, sshFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec := &scriptedShell{err: tc.err}
			err := chatAgent(exec).AttachChat(context.Background(), nil, Streams{})
			if !errors.Is(err, tc.wantErr) || (tc.wantErr == nil) != (err == nil) {
				t.Errorf("AttachChat() = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// TestChatFilesScript runs the script in a POSIX shell against a temporary
// home: in.fifo becomes a FIFO whatever was there, and out.log starts empty.
func TestChatFilesScript(t *testing.T) {
	cases := []struct {
		name   string
		before map[string]string // file → content; "fifo" makes a FIFO
	}{
		{"nothing there", nil},
		{"a FIFO and an old log", map[string]string{"in.fifo": "fifo", "out.log": "{\"old\":1}\n"}},
		{"a regular file in the FIFO's place", map[string]string{"in.fifo": "stray"}},
	}
	sh := lookSh(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			dir := sessionDirIn(t, home)
			for name, body := range tc.before {
				p := filepath.Join(dir, name)
				var err error
				if body == "fifo" {
					err = syscall.Mkfifo(p, 0o600)
				} else {
					err = os.WriteFile(p, []byte(body), 0o600)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			cmd := osexec.Command(sh, "-c", chatFilesScript, "sh", agentUUID)
			cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("chat files script: %v: %s", err, out)
			}
			fifo, err := os.Stat(filepath.Join(dir, "in.fifo"))
			if err != nil || fifo.Mode()&os.ModeNamedPipe == 0 || fifo.Mode().Perm() != 0o600 {
				t.Errorf("in.fifo = %v, %v, want a FIFO private to the user", fifo, err)
			}
			log, err := os.Stat(filepath.Join(dir, "out.log"))
			if err != nil || log.Size() != 0 {
				t.Errorf("out.log = %v, %v, want an empty file", log, err)
			}
		})
	}
}

// TestAttachChatScript runs the attach in a POSIX shell against a temporary
// home, with a tmux that reports the pane: only complete stdin lines reach
// in.fifo, and an attach with no Claude to join ends at once.
func TestAttachChatScript(t *testing.T) {
	cases := []struct {
		name       string
		paneDead   string // what tmux reports; "" reports no session
		fifo       bool
		stdin      string
		wantStatus int
		wantFIFO   string
	}{
		{"complete lines delivered, a partial one not", "0", true, "{\"a\":1}\n{\"b\": \"two words\"}\n{\"partial", 0, "{\"a\":1}\n{\"b\": \"two words\"}\n"},
		{"stdin ends at once", "0", true, "", 0, ""},
		{"pane dead", "1", true, "{\"a\":1}\n", chatGoneStatus, ""},
		{"no session", "", true, "{\"a\":1}\n", chatGoneStatus, ""},
		{"no FIFO", "0", false, "{\"a\":1}\n", chatGoneStatus, ""},
	}
	sh := lookSh(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			dir := sessionDirIn(t, home)
			bin := filepath.Join(home, "bin")
			writeExecutable(t, filepath.Join(bin, "tmux"), "#!/bin/sh\n[ -n '"+tc.paneDead+"' ] || exit 1\necho '"+tc.paneDead+"'\n")
			if err := os.WriteFile(filepath.Join(dir, "out.log"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			var claudeEnd *os.File // the FIFO held open as Claude holds it
			if tc.fifo {
				p := filepath.Join(dir, "in.fifo")
				if err := syscall.Mkfifo(p, 0o600); err != nil {
					t.Fatal(err)
				}
				f, err := os.OpenFile(p, os.O_RDWR, 0)
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				claudeEnd = f
			}
			cmd := osexec.Command(sh, "-c", attachChatScript, "sh", agentUUID)
			cmd.Env = []string{"HOME=" + home, "PATH=" + bin + ":" + os.Getenv("PATH")}
			cmd.Stdin = strings.NewReader(tc.stdin)
			status := 0
			if err := cmd.Run(); err != nil {
				code, ok := shell.ExitCode(err)
				if !ok {
					t.Fatalf("attach script: %v", err)
				}
				status = code
			}
			if status != tc.wantStatus {
				t.Errorf("status = %d, want %d", status, tc.wantStatus)
			}
			if claudeEnd != nil {
				if got := drain(t, claudeEnd); got != tc.wantFIFO {
					t.Errorf("Claude reads %q, want %q", got, tc.wantFIFO)
				}
			}
		})
	}
}

func TestRuntimeString(t *testing.T) {
	cases := []struct {
		runtime Runtime
		want    string
	}{
		{RuntimeTerminal, "terminal"},
		{RuntimeChat, "chat"},
		{Runtime(99), "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			if got := tc.runtime.String(); got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

func lookSh(t *testing.T) string {
	t.Helper()
	sh, err := osexec.LookPath("sh")
	if err != nil {
		t.Skip("no POSIX shell")
	}
	return sh
}

func sessionDirIn(t *testing.T, home string) string {
	t.Helper()
	dir := filepath.Join(home, ".hermod", "s", agentUUID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// drain reads what is buffered in a FIFO without waiting for more.
func drain(t *testing.T, f *os.File) string {
	t.Helper()
	// NOTE: Fd puts the file in blocking mode on every call, so it is read once.
	fd := int(f.Fd())
	if err := syscall.SetNonblock(fd, true); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	buf := make([]byte, 4096)
	for {
		n, err := syscall.Read(fd, buf)
		if n <= 0 || err != nil {
			return out.String()
		}
		out.Write(buf[:n])
	}
}

// TestProbeClosed asserts a closed session reads as closed or new by its
// transcripts alone.
func TestProbeClosed(t *testing.T) {
	cases := []struct {
		name              string
		probe             Probe
		localTranscript   bool
		wantState         AgentState
		wantStale         bool
		wantSandboxRecord bool
	}{
		{"running with a transcript", Probe{Listed: true, Tmux: true, SandboxTranscript: true}, false, AgentClosed, false, true},
		{"running with a transcript only here", Probe{Listed: true, Tmux: true}, true, AgentClosed, false, false},
		{"running with no transcript", Probe{Listed: true, Tmux: true}, false, AgentNew, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.probe.Closed()
			if s := got.State(tc.localTranscript); s != tc.wantState {
				t.Errorf("State() = %v, want %v", s, tc.wantState)
			}
			if got.Stale() != tc.wantStale || got.SandboxTranscript != tc.wantSandboxRecord {
				t.Errorf("Closed() = %+v", got)
			}
		})
	}
}

// TestAgentTagged asserts the look-up of a session's tmux session runs one
// read-only ssh call and reads its tag, an incomplete answer failing it.
func TestAgentTagged(t *testing.T) {
	lookUp := []string{"ssh", "prod", "sh -c '" + strings.ReplaceAll(taggedScript, "'", `'\''`) + "' sh " + agentUUID}
	cases := []struct {
		name     string
		out      string
		err      error
		want     Tagged
		wantErr  bool
		wantArgv []string
	}{
		{
			name: "tagged", out: "home=/home/u\ntmux=yes\ndir=dev/api\nend\n",
			want: Tagged{Exists: true, Home: "/home/u", Dir: "dev/api"}, wantArgv: lookUp,
		},
		{
			name: "untagged", out: "home=/home/u\ntmux=yes\ndir=\nend\n",
			want: Tagged{Exists: true, Home: "/home/u"}, wantArgv: lookUp,
		},
		{
			name: "gone", out: "home=/home/u\ntmux=no\nend\n",
			want: Tagged{Home: "/home/u"}, wantArgv: lookUp,
		},
		{name: "incomplete output", out: "home=/home/u\ntmux=yes\n", wantErr: true, wantArgv: lookUp},
		{name: "no home", out: "home=\ntmux=no\nend\n", wantErr: true, wantArgv: lookUp},
		{name: "ssh fails", err: errors.New("connection refused"), wantErr: true, wantArgv: lookUp},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec := &scriptedShell{}
			probe := &scriptedShell{ret: shell.Result{Stdout: tc.out}, err: tc.err}
			got, err := testAgent(exec, probe).Tagged(context.Background())
			if (err != nil) != tc.wantErr {
				t.Fatalf("Tagged() error = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("Tagged() = %+v, want %+v", got, tc.want)
			}
			if len(exec.calls) != 0 {
				t.Errorf("the look-up ran %d side-effecting commands", len(exec.calls))
			}
			if len(probe.calls) != 1 || !probe.calls[0].cmd.Capture {
				t.Fatalf("probe calls = %+v, want one captured", probe.calls)
			}
			if argv := probe.calls[0].cmd.Argv; !reflect.DeepEqual(argv, tc.wantArgv) {
				t.Errorf("argv = %q, want %q", argv, tc.wantArgv)
			}
		})
	}
}

// TestTaggedScript runs the look-up script against a stand-in tmux that answers
// as the real one does: has-session fails for a missing session, while
// display-message prints empty fields for it.
func TestTaggedScript(t *testing.T) {
	cases := []struct {
		name    string
		tmux    string // the stand-in tmux's body
		wantOut string
	}{
		{"tagged", `case "$1" in has-session) exit 0 ;; display-message) echo dev/api ;; esac`, "home={home}\ntmux=yes\ndir=dev/api\nend\n"},
		{"untagged", `case "$1" in has-session) exit 0 ;; display-message) echo ;; esac`, "home={home}\ntmux=yes\ndir=\nend\n"},
		{"missing", `case "$1" in has-session) echo "can't find session" >&2; exit 1 ;; display-message) echo ;; esac`, "home={home}\ntmux=no\nend\n"},
		{"no server running", `echo 'no server running on /tmp/tmux-1000/default' >&2; exit 1`, "home={home}\ntmux=no\nend\n"},
	}
	sh := lookSh(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			bin := filepath.Join(home, "bin")
			writeExecutable(t, filepath.Join(bin, "tmux"), "#!/bin/sh\n"+tc.tmux+"\n")
			cmd := osexec.Command(sh, "-c", taggedScript, "sh", agentUUID)
			cmd.Env = []string{"HOME=" + home, "PATH=" + bin + ":" + os.Getenv("PATH")}
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("look-up script: %v", err)
			}
			if want := strings.ReplaceAll(tc.wantOut, "{home}", home); string(out) != want {
				t.Errorf("output = %q, want %q", out, want)
			}
		})
	}
}
