package sandbox

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	osexec "os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/zerlok/hermod/internal/claude"
	"github.com/zerlok/hermod/internal/secret"
	"github.com/zerlok/hermod/internal/shell"
)

const agentUUID = "0b7e5f3c-2a41-4c8e-9d6f-1e2a3b4c5d6e"

// leafCall is one command that reached the leaf, with its stdin read out.
type leafCall struct {
	cmd   shell.Command
	stdin string
}

// scriptedShell records every command and answers with a canned reply.
type scriptedShell struct {
	calls []leafCall
	ret   shell.Result
	err   error
}

func (s *scriptedShell) Run(_ context.Context, cmd shell.Command) (shell.Result, error) {
	var in string
	if cmd.Stdin != nil {
		b, _ := io.ReadAll(cmd.Stdin)
		in = string(b)
	}
	s.calls = append(s.calls, leafCall{cmd: cmd, stdin: in})
	return s.ret, s.err
}

func testAgent(exec, probe shell.Shell) *Agent {
	id, _ := claude.ParseSessionID(agentUUID)
	return NewAgent(exec, probe, AgentConfig{
		Host: "prod",
		ID:   id,
		Dir:  "dev/api",
		Tags: Tags{Mode: ModeAirTerminal, Origin: "laptop", Dir: "dev/api"},
	})
}

func TestProbeState(t *testing.T) {
	cases := []struct {
		name              string
		listed, tmux      bool
		sandboxTranscript bool
		localTranscript   bool
		want              AgentState
		wantStale         bool
	}{
		{"running", true, true, true, false, AgentRunning, false},
		{"running without a transcript yet", true, true, false, false, AgentRunning, false},
		{"running outside Hermod", true, false, true, true, AgentRunningOutside, false},
		{"closed on the sandbox", false, false, true, false, AgentClosed, false},
		{"closed only locally", false, false, false, true, AgentClosed, false},
		{"closed with a stale tmux session", false, true, true, false, AgentClosed, true},
		{"new", false, false, false, false, AgentNew, false},
		{"new with a stale tmux session", false, true, false, false, AgentNew, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := Probe{Listed: tc.listed, Tmux: tc.tmux, SandboxTranscript: tc.sandboxTranscript}
			if got := p.State(tc.localTranscript); got != tc.want {
				t.Errorf("State() = %v, want %v", got, tc.want)
			}
			if got := p.Stale(); got != tc.wantStale {
				t.Errorf("Stale() = %v, want %v", got, tc.wantStale)
			}
		})
	}
}

func TestAgentProbe(t *testing.T) {
	const (
		head       = "home=/home/u\nconfig=/home/u/.claude\n"
		ownPath    = "/home/u/.claude/projects/-home-u-dev-api/" + agentUUID + ".jsonl"
		otherPath  = "/home/u/.claude/projects/-home-u-other/" + agentUUID + ".jsonl"
		transcript = "transcript=" + ownPath + "\n"
		elsewhere  = "transcript=" + otherPath + "\n"
		listed     = `[{"sessionId":"` + agentUUID + `","pid":7}]`
	)
	cases := []struct {
		name    string
		stdout  string
		err     error
		want    Probe
		wantErr string // "" for success
	}{
		{
			"running in Hermod's tmux session",
			head + "tmux=yes\n" + transcript + "hookport=45001\ntoken=tok\nagents:\n" + listed,
			nil,
			Probe{Listed: true, Tmux: true, SandboxTranscript: true, Transcripts: []string{ownPath}, Home: "/home/u", ConfigDir: "/home/u/.claude", HookPort: 45001, Token: secret.FromString("tok")},
			"",
		},
		{
			"new session",
			head + "tmux=no\nagents:\n[]",
			nil,
			Probe{Home: "/home/u", ConfigDir: "/home/u/.claude"},
			"",
		},
		{
			"transcript of another directory does not count",
			head + "tmux=no\n" + elsewhere + "agents:\n[]",
			nil,
			Probe{Transcripts: []string{otherPath}, Home: "/home/u", ConfigDir: "/home/u/.claude"},
			"",
		},
		{
			"unreadable port is none",
			head + "tmux=no\nhookport=x\nagents:\n[]",
			nil,
			Probe{Home: "/home/u", ConfigDir: "/home/u/.claude"},
			"",
		},
		{"probe fails", "", exitErr{code: 127}, Probe{}, "probe session"},
		{"no agents list", head + "tmux=no\n", nil, Probe{}, "unexpected output"},
		{"claude without agents", head + "tmux=no\nagents:\nerror: unknown command 'agents'", nil, Probe{}, "claude agents --json"},
		{"no home", "config=/c\ntmux=no\nagents:\n[]", nil, Probe{}, "no home"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			probe := &scriptedShell{ret: shell.Result{Stdout: tc.stdout}, err: tc.err}
			exec := &scriptedShell{}
			got, err := testAgent(exec, probe).Probe(context.Background())
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want one with %q", err, tc.wantErr)
				}
			} else if err != nil {
				t.Fatalf("Probe() error: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Probe() = %+v, want %+v", got, tc.want)
			}
			if len(exec.calls) != 0 || len(probe.calls) != 1 {
				t.Fatalf("calls: exec %d, probe %d, want 0 and 1", len(exec.calls), len(probe.calls))
			}
			argv := probe.calls[0].cmd.Argv
			if argv[0] != "ssh" || argv[1] != "prod" || !strings.Contains(argv[2], "claude agents --json") || !strings.HasSuffix(argv[2], agentUUID+`'`) {
				t.Errorf("probe argv = %q", argv)
			}
			if !probe.calls[0].cmd.Capture {
				t.Error("the probe does not capture its output")
			}
		})
	}
}

func TestAgentProbeAsksForOthers(t *testing.T) {
	const fork = "5d6e1e2a-3b4c-4c8e-9d6f-0b7e5f3c2a41"
	cases := []struct {
		name     string
		others   []string
		wantTail string
	}{
		{"the session alone", nil, " sh " + agentUUID + "'"},
		{"and a fork's source", []string{fork}, " sh " + agentUUID + " " + fork + "'"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			probe := &scriptedShell{ret: shell.Result{Stdout: "home=/h\nconfig=/h/.claude\ntmux=no\nagents:\n[]"}}
			var others []claude.SessionID
			for _, o := range tc.others {
				id, _ := claude.ParseSessionID(o)
				others = append(others, id)
			}
			if _, err := testAgent(&scriptedShell{}, probe).Probe(context.Background(), others...); err != nil {
				t.Fatal(err)
			}
			if line := probe.calls[0].cmd.Argv[2]; !strings.HasSuffix(line, tc.wantTail) {
				t.Errorf("probe %q does not end with %q", line, tc.wantTail)
			}
		})
	}
}

func TestAgentCommands(t *testing.T) {
	cases := []struct {
		name      string
		run       func(a *Agent) error
		wantArgv  []string
		wantStdin string
	}{
		{
			"remove a stale tmux session",
			func(a *Agent) error { return a.RemoveStale(context.Background()) },
			[]string{"ssh", "prod", "tmux kill-session -t =hermod-" + agentUUID},
			"",
		},
		{
			"copy a transcript up",
			func(a *Agent) error {
				return a.CopyTranscript(context.Background(), "/home/u/.claude/projects/-home-u-dev-api/x.jsonl", strings.NewReader("{\"a\":1}\n"), 8)
			},
			[]string{"ssh", "prod", "sh -c " + shell.Quote(copyScript) + " sh /home/u/.claude/projects/-home-u-dev-api/x.jsonl 8"},
			"{\"a\":1}\n",
		},
		{
			"start detached, tagged, in the project directory",
			func(a *Agent) error {
				return a.Start(context.Background(),
					[]string{"claude", "--session-id", agentUUID},
					[]string{"HERMOD_SESSION=" + agentUUID, "HERMOD_MODE=air-terminal"})
			},
			[]string{"ssh", "prod", "tmux new-session -d -s hermod-" + agentUUID + " -c dev/api -e HERMOD_SESSION=" + agentUUID + " -e HERMOD_MODE=air-terminal " +
				`'sh -c '\''exec "$SHELL" -lc "$1"'\'' sh '\''sh -c '\''\'\'''\''tmux set-option -w remain-on-exit failed 2>/dev/null; echo "$$" > "$HOME/.hermod/s/$1/pid"; . "$HOME/.hermod/s/$1/env" && shift && exec "$@"'\''\'\'''\'' sh ` + agentUUID + ` claude --session-id ` + agentUUID + `'\'''` +
				" ';' set-option -t =hermod-" + agentUUID + ": -qo @hermod-mode air-terminal ';' set-option -t =hermod-" + agentUUID + ": -qo @hermod-origin laptop ';' set-option -t =hermod-" + agentUUID + ": -qo @hermod-dir dev/api"},
			"",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec, probe := &scriptedShell{}, &scriptedShell{}
			if err := tc.run(testAgent(exec, probe)); err != nil {
				t.Fatalf("error: %v", err)
			}
			if len(exec.calls) != 1 || len(probe.calls) != 0 {
				t.Fatalf("calls: exec %d, probe %d, want 1 and 0", len(exec.calls), len(probe.calls))
			}
			got := exec.calls[0]
			if !reflect.DeepEqual(got.cmd.Argv, tc.wantArgv) {
				t.Errorf("argv =\n %q\nwant\n %q", got.cmd.Argv, tc.wantArgv)
			}
			if got.stdin != tc.wantStdin {
				t.Errorf("stdin = %q, want %q", got.stdin, tc.wantStdin)
			}
			// A setup command never reads this process's stdin: it captures (empty
			// stdin) or is given its input.
			if !got.cmd.Capture {
				t.Error("setup command does not capture: it would read this process's stdin")
			}
		})
	}
}

func TestAgentCommandFailure(t *testing.T) {
	cases := []struct {
		name    string
		run     func(a *Agent) error
		wantErr string
	}{
		{"remove", func(a *Agent) error { return a.RemoveStale(context.Background()) }, "remove stale tmux session on prod: exit status: boom"},
		{"copy", func(a *Agent) error { return a.CopyTranscript(context.Background(), "/x", strings.NewReader(""), 0) }, "copy transcript on prod: exit status: boom"},
		{"start", func(a *Agent) error { return a.Start(context.Background(), []string{"claude"}, nil) }, "start session " + agentUUID + " on prod: exit status: boom"},
		{"write", func(a *Agent) error { return a.WriteFiles(context.Background(), Files{}) }, "write session files on prod: exit status: boom"},
		{"prepare", func(a *Agent) error { _, err := a.Prepare(context.Background(), []int{1}); return err }, "prepare session directory on prod: exit status: boom"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec := &scriptedShell{ret: shell.Result{Stderr: "boom\n"}, err: exitErr{code: 1}}
			err := tc.run(testAgent(exec, &scriptedShell{}))
			if err == nil || err.Error() != tc.wantErr {
				t.Errorf("err = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestAgentPrepare(t *testing.T) {
	cases := []struct {
		name       string
		candidates []int
		stdout     string
		wantArgv   []string
		wantPort   int
		wantErr    bool
	}{
		{
			"recorded or picked port", []int{45001, 45002}, "45002\n",
			[]string{"ssh", "prod", "sh -c '" + strings.ReplaceAll(prepareScript, "'", `'\''`) + "' sh " + agentUUID + " 45001 45002"},
			45002, false,
		},
		{"dry run prints and learns nothing", []int{45001}, "", nil, 0, false},
		{"not a port", []int{45001}, "nope\n", nil, 0, true},
		{"out of range", []int{45001}, "70000\n", nil, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec := &scriptedShell{ret: shell.Result{Stdout: tc.stdout}}
			port, err := testAgent(exec, &scriptedShell{}).Prepare(context.Background(), tc.candidates)
			if port != tc.wantPort || (err != nil) != tc.wantErr {
				t.Errorf("Prepare() = %d, %v, want %d, err %v", port, err, tc.wantPort, tc.wantErr)
			}
			if tc.wantArgv != nil && !reflect.DeepEqual(exec.calls[0].cmd.Argv, tc.wantArgv) {
				t.Errorf("argv =\n %q\nwant\n %q", exec.calls[0].cmd.Argv, tc.wantArgv)
			}
			if !exec.calls[0].cmd.Capture || exec.calls[0].stdin != "" {
				t.Error("prepare reads input")
			}
		})
	}
}

// TestPrepareScriptPicksAPortOfItsOwn runs the prepare script in a POSIX shell
// against a temporary home: a port another session has recorded is never
// picked, though nothing binds it while that session is detached.
func TestPrepareScriptPicksAPortOfItsOwn(t *testing.T) {
	const other = "11111111-2222-3333-4444-555555555555"
	cases := []struct {
		name       string
		recorded   map[string]string // session UUID → its hookport file
		candidates []string
		want       string
	}{
		{"first candidate", nil, []string{"11001", "11002"}, "11001"},
		{"another session's port skipped", map[string]string{other: "11001\n"}, []string{"11001", "11002"}, "11002"},
		{"own recorded port kept", map[string]string{agentUUID: "11003\n", other: "11001\n"}, []string{"11001", "11002"}, "11003"},
	}
	sh, err := osexec.LookPath("sh")
	if err != nil {
		t.Skip("no POSIX shell")
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			for id, port := range tc.recorded {
				dir := filepath.Join(home, ".hermod", "s", id)
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "hookport"), []byte(port), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cmd := osexec.Command(sh, append([]string{"-c", prepareScript, "sh", agentUUID}, tc.candidates...)...)
			cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("prepare script: %v", err)
			}
			if got := strings.TrimSpace(string(out)); got != tc.want {
				t.Errorf("port = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestCopyScriptLeavesNoShortTranscript runs the copy script with a POSIX shell
// and asserts a transcript appears under its name only when every byte sent
// arrived, as a cut connection gives cat an early EOF that it ends on with 0.
func TestCopyScriptLeavesNoShortTranscript(t *testing.T) {
	cases := []struct {
		name     string
		existing string // the transcript already at dst; "" for none
		input    string // what reaches the script's stdin
		size     string // the byte count the script is told
		wantErr  bool
		wantFile bool   // dst exists afterwards
		wantBody string // dst's content when it exists
	}{
		{"whole copy lands", "", "{\"a\":1}\n", "8", false, true, "{\"a\":1}\n"},
		{"cut copy leaves nothing", "", "{\"a\":", "8", true, false, ""},
		{"empty transcript lands", "", "", "0", false, true, ""},
		{"cut copy keeps what was there", "old\n", "{\"a\":", "8", true, true, "old\n"},
	}
	sh, err := osexec.LookPath("sh")
	if err != nil {
		t.Skip("no POSIX shell")
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dst := filepath.Join(t.TempDir(), "projects", "-p", "x.jsonl")
			if tc.existing != "" {
				if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(dst, []byte(tc.existing), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cmd := osexec.Command(sh, "-c", copyScript, "sh", dst, tc.size)
			cmd.Stdin = strings.NewReader(tc.input)
			err := cmd.Run()
			if (err != nil) != tc.wantErr {
				t.Errorf("copy script error = %v, want error %v", err, tc.wantErr)
			}
			body, readErr := os.ReadFile(dst)
			switch {
			case (readErr == nil) != tc.wantFile:
				t.Errorf("dst exists = %v, want %v", readErr == nil, tc.wantFile)
			case tc.wantFile && string(body) != tc.wantBody:
				t.Errorf("dst = %q, want %q", body, tc.wantBody)
			}
			if _, err := os.Stat(dst + ".part"); err == nil {
				t.Error("a .part file is left behind")
			}
		})
	}
}

// TestWriteFilesOverStdin asserts the session's files travel on stdin, split by
// the lengths on the command line, and that no secret is on any command line.
func TestWriteFilesOverStdin(t *testing.T) {
	cases := []struct {
		name      string
		files     Files
		secrets   []string
		wantStdin string
		wantTail  string // the end of the ssh line: uuid and lengths
	}{
		{
			"token, env, settings and MCP config",
			Files{
				Token:    secret.FromString("tok-123"),
				Env:      secret.NewEnv([]string{"ANTHROPIC_API_KEY=sk-ant-secret", "GIT_AUTHOR_NAME=Jane Doe"}),
				Settings: []byte(`{"hooks":{"Stop":[{"hooks":[{"headers":{"Authorization":"Bearer tok-123"}}]}]}}`),
				MCP:      []byte(`{"mcpServers":{"w":{"headers":{"Authorization":"Bearer mcp-key"}}}}`),
			},
			[]string{"tok-123", "sk-ant-secret", "mcp-key"},
			"tok-123" + "export ANTHROPIC_API_KEY=sk-ant-secret\nexport GIT_AUTHOR_NAME='Jane Doe'\n" + `{"hooks":{"Stop":[{"hooks":[{"headers":{"Authorization":"Bearer tok-123"}}]}]}}` + `{"mcpServers":{"w":{"headers":{"Authorization":"Bearer mcp-key"}}}}`,
			" sh " + agentUUID + " 7 73 79",
		},
		{
			"nothing to write",
			Files{},
			nil,
			"",
			" sh " + agentUUID + " 0 0 0",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec := &scriptedShell{}
			if err := testAgent(exec, &scriptedShell{}).WriteFiles(context.Background(), tc.files); err != nil {
				t.Fatalf("WriteFiles() error: %v", err)
			}
			got := exec.calls[0]
			if got.stdin != tc.wantStdin {
				t.Errorf("stdin =\n %q\nwant\n %q", got.stdin, tc.wantStdin)
			}
			line := strings.Join(got.cmd.Argv, " ")
			if !strings.HasSuffix(line, tc.wantTail) {
				t.Errorf("command line %q does not end with %q", line, tc.wantTail)
			}
			for _, s := range tc.secrets {
				if strings.Contains(line, s) {
					t.Errorf("secret %q on the command line %q", s, line)
				}
			}
		})
	}
}

// TestWriteScriptSplitsTheFiles runs the write script in a POSIX shell against
// a temporary home and reads back each file it wrote.
func TestWriteScriptSplitsTheFiles(t *testing.T) {
	cases := []struct {
		name  string
		files Files
		want  map[string]string // file name → content
	}{
		{
			"every file",
			Files{Token: secret.FromString("tok"), Env: secret.NewEnv([]string{"A=1"}), Settings: []byte(`{"s":1}`), MCP: []byte(`{"m":2}`)},
			map[string]string{"token": "tok", "env": "export A=1\n", "settings.json": `{"s":1}`, "mcp.json": `{"m":2}`},
		},
		{
			"no settings, an MCP config",
			Files{Token: secret.FromString("tok"), MCP: []byte(`{"m":2}`)},
			map[string]string{"token": "tok", "env": "", "settings.json": "", "mcp.json": `{"m":2}`},
		},
	}
	sh, err := osexec.LookPath("sh")
	if err != nil {
		t.Skip("no POSIX shell")
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			dir := filepath.Join(home, ".hermod", "s", agentUUID)
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			exec := &scriptedShell{}
			if err := testAgent(exec, &scriptedShell{}).WriteFiles(context.Background(), tc.files); err != nil {
				t.Fatal(err)
			}
			// The ssh line's remote words, after "ssh prod", are the script's argv.
			call := exec.calls[0]
			cmd := osexec.Command(sh, "-c", call.cmd.Argv[2])
			cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
			cmd.Stdin = strings.NewReader(call.stdin)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("write script: %v: %s", err, out)
			}
			for name, want := range tc.want {
				got, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil || string(got) != want {
					t.Errorf("%s = %q, %v, want %q", name, got, err, want)
				}
			}
		})
	}
}

func TestEnvFile(t *testing.T) {
	cases := []struct {
		name string
		env  []string
		want string
	}{
		{"plain and quoted", []string{"A=1", "B=two words", "C=it's"}, "export A=1\nexport B='two words'\nexport C='it'\\''s'\n"},
		{"empty value", []string{"A="}, "export A=''\n"},
		{"newline kept inside quotes", []string{"A=x\ny"}, "export A='x\ny'\n"},
		{"names a shell cannot set left out", []string{"1A=x", "A-B=x", "=x", "noequals", "_ok=1"}, "export _ok=1\n"},
		{"none", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(EnvFile(tc.env)); got != tc.want {
				t.Errorf("EnvFile() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAttachTerminal(t *testing.T) {
	cases := []struct {
		name     string
		fwd      *Forward
		wantArgv []string
	}{
		{
			"own connection on a tty",
			nil,
			[]string{"ssh", "-t", "-o", "ControlPath=none", "prod", "tmux attach-session -t =hermod-" + agentUUID},
		},
		{
			"with the hook forward",
			&Forward{SandboxPort: 45001, LocalPort: 38211},
			[]string{"ssh", "-t", "-o", "ControlPath=none", "-R", "45001:127.0.0.1:38211", "prod", "tmux attach-session -t =hermod-" + agentUUID},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec := &scriptedShell{}
			streams := Streams{In: strings.NewReader("keys"), Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
			if err := testAgent(exec, &scriptedShell{}).AttachTerminal(context.Background(), tc.fwd, streams); err != nil {
				t.Fatalf("AttachTerminal() error: %v", err)
			}
			got := exec.calls[0]
			if !reflect.DeepEqual(got.cmd.Argv, tc.wantArgv) {
				t.Errorf("argv = %q, want %q", got.cmd.Argv, tc.wantArgv)
			}
			if got.stdin != "keys" || got.cmd.Stdout != streams.Out || got.cmd.Capture {
				t.Error("the attach does not run with the caller's streams")
			}
		})
	}
}

// TestAttachTerminalOutcome asserts an attach that finds no session reports
// that Claude exited, and any other failure passes through.
func TestAttachTerminalOutcome(t *testing.T) {
	sshFailed := exitErr{code: 255}
	cases := []struct {
		name       string
		attachErr  error
		probeErr   error // the liveness check after a failed attach
		wantErr    error // nil: success
		wantProbes int
	}{
		{"detached", nil, nil, nil, 0},
		{"session gone between start and attach", exitErr{code: 1}, exitErr{code: 1}, ErrSessionGone, 1},
		{"connection dropped, session alive", sshFailed, nil, sshFailed, 1},
		{"connection dropped, liveness unknown", sshFailed, sshFailed, sshFailed, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec, probe := &scriptedShell{err: tc.attachErr}, &scriptedShell{err: tc.probeErr}
			err := testAgent(exec, probe).AttachTerminal(context.Background(), nil, Streams{})
			if !errors.Is(err, tc.wantErr) || (tc.wantErr == nil) != (err == nil) {
				t.Errorf("AttachTerminal() = %v, want %v", err, tc.wantErr)
			}
			if len(probe.calls) != tc.wantProbes {
				t.Errorf("liveness checks = %d, want %d", len(probe.calls), tc.wantProbes)
			}
			// The attach never creates a session: that would be a shell under its name.
			for _, c := range exec.calls {
				if strings.Contains(strings.Join(c.cmd.Argv, " "), "new-session") {
					t.Errorf("the attach creates a session: %q", c.cmd.Argv)
				}
			}
		})
	}
}

func TestForwardWatch(t *testing.T) {
	cases := []struct {
		name         string
		writes       []string
		wantFailures int
	}{
		{"failure reported", []string{"Warning: remote port forwarding failed for listen port 45001\r\n"}, 1},
		{"split across writes", []string{"Warning: remote port forw", "arding failed for listen port 45001\r\n"}, 1},
		{"reported once", []string{"remote port forwarding failed\n", "remote port forwarding failed\n"}, 1},
		{"other messages pass", []string{"Connection to prod closed.\r\n"}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			failures := 0
			w := &forwardWatch{w: &out, onFailure: func() { failures++ }}
			for _, s := range tc.writes {
				if n, err := w.Write([]byte(s)); n != len(s) || err != nil {
					t.Fatalf("Write() = %d, %v", n, err)
				}
			}
			if failures != tc.wantFailures {
				t.Errorf("failures = %d, want %d", failures, tc.wantFailures)
			}
			if out.String() != strings.Join(tc.writes, "") {
				t.Errorf("passed through %q, want %q", out.String(), strings.Join(tc.writes, ""))
			}
		})
	}
}

func TestAgentIsActive(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		wantAlive bool
		wantErr   bool
	}{
		{"exists", nil, true, false},
		{"gone", exitErr{code: 1}, false, false},
		{"unreachable", exitErr{code: 255}, false, true},
		{"never ran", errors.New("no ssh"), false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			probe := &scriptedShell{err: tc.err}
			alive, err := testAgent(&scriptedShell{}, probe).IsActive(context.Background())
			if alive != tc.wantAlive || (err != nil) != tc.wantErr {
				t.Errorf("IsActive() = %v, %v, want %v, err %v", alive, err, tc.wantAlive, tc.wantErr)
			}
			want := []string{"ssh", "prod", "tmux has-session -t =hermod-" + agentUUID}
			if !reflect.DeepEqual(probe.calls[0].cmd.Argv, want) {
				t.Errorf("argv = %q, want %q", probe.calls[0].cmd.Argv, want)
			}
		})
	}
}

func TestAgentPaths(t *testing.T) {
	cases := []struct {
		name           string
		dir            string
		home           string
		wantProject    string
		wantTranscript string
		wantSessionDir string
	}{
		{"home-relative", "dev/api", "/home/u", "/home/u/dev/api", "/home/u/.claude/projects/-home-u-dev-api/" + agentUUID + ".jsonl", "/home/u/.hermod/s/" + agentUUID},
		{"absolute", "/srv/api", "/home/u", "/srv/api", "/home/u/.claude/projects/-srv-api/" + agentUUID + ".jsonl", "/home/u/.hermod/s/" + agentUUID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, _ := claude.ParseSessionID(agentUUID)
			a := NewAgent(nil, nil, AgentConfig{Host: "prod", ID: id, Dir: tc.dir})
			if got := a.ProjectDir(tc.home); got != tc.wantProject {
				t.Errorf("ProjectDir() = %q, want %q", got, tc.wantProject)
			}
			if got := a.TranscriptPath(Probe{Home: tc.home, ConfigDir: tc.home + "/.claude"}); got != tc.wantTranscript {
				t.Errorf("TranscriptPath() = %q, want %q", got, tc.wantTranscript)
			}
			if got := SessionDir(tc.home, id); got != tc.wantSessionDir {
				t.Errorf("SessionDir() = %q, want %q", got, tc.wantSessionDir)
			}
		})
	}
}

func TestModeString(t *testing.T) {
	cases := []struct {
		mode Mode
		want string
	}{
		{ModeAttach, "attach"},
		{ModeAirTerminal, "air-terminal"},
		{ModeAirACP, "air-acp"},
		{Mode(99), "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			if got := tc.mode.String(); got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}
