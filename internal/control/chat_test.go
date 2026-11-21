package control

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zerlok/hermod/internal/claude"
	"github.com/zerlok/hermod/internal/sandbox"
	"github.com/zerlok/hermod/internal/shell"
)

// testWindow stands in for sdkWindow, so a test of a cut never waits seconds.
var testWindow = exitWindow{detach: 10 * time.Millisecond, settle: 100 * time.Millisecond, pauseReserve: 50 * time.Millisecond}

func chatArgv(session ...string) []string {
	return append([]string{"claude", "--output-format", "stream-json", "--verbose", "--input-format", "stream-json"}, session...)
}

func TestRunChat(t *testing.T) {
	newArgs := chatArgv("--session-id=" + sessionUUID)
	resumeArgs := chatArgv("--resume=" + sessionUUID)
	// An attach on a connection of its own, without a tty.
	ownConnection := []string{"ssh", "-o", "ControlPath=none", "prod"}
	cases := []struct {
		name            string
		argv            []string
		probe           string
		localTranscript bool
		attachErr       error
		closeErr        error // how closing a running chat ends
		wantSteps       []string
		wantAttach      []string // the head of the attach's argv; nil when none attaches
		wantClaude      string   // a substring of the start command; "" when none starts
		wantErr         error
	}{
		{
			"new chat started on its stream files and attached",
			newArgs, probeOut(false, false, notListed), false, nil, nil,
			[]string{"mirror mkdir", "mirror create", "mirror flush", "transcripts mkdir", "transcripts local mkdir", "transcripts create", "prepare", "write files", "chat files", "start", "attach chat", "liveness", "mirror pause", "transcripts pause"},
			ownConnection,
			"claude --output-format stream-json --verbose --input-format stream-json --session-id=" + sessionUUID, nil,
		},
		{
			"closed chat resumed",
			resumeArgs, probeOut(false, true, notListed), false, nil, nil,
			[]string{"mirror mkdir", "mirror create", "mirror flush", "transcripts mkdir", "transcripts local mkdir", "transcripts create", "prepare", "write files", "chat files", "start", "attach chat", "liveness", "mirror pause", "transcripts pause"},
			ownConnection,
			"claude --resume " + sessionUUID + " --output-format stream-json", nil,
		},
		{
			"closed chat known only locally: transcript copied up, then resumed",
			resumeArgs, probeOut(false, false, notListed), true, nil, nil,
			[]string{"mirror mkdir", "mirror create", "mirror flush", "transcripts mkdir", "transcripts local mkdir", "transcripts create", "copy transcript", "prepare", "write files", "chat files", "start", "attach chat", "liveness", "mirror pause", "transcripts pause"},
			ownConnection,
			"claude --resume " + sessionUUID + " --output-format stream-json", nil,
		},
		{
			"running chat closed and resumed",
			resumeArgs, probeOut(true, true, listed), false, nil, nil,
			[]string{"mirror mkdir", "mirror create", "mirror flush", "close", "transcripts mkdir", "transcripts local mkdir", "transcripts create", "prepare", "write files", "chat files", "start", "attach chat", "liveness", "mirror pause", "transcripts pause"},
			ownConnection,
			"claude --resume " + sessionUUID + " --output-format stream-json", nil,
		},
		{
			"running chat reopened with --session-id closed and resumed",
			newArgs, probeOut(true, true, listed), false, nil, nil,
			[]string{"mirror mkdir", "mirror create", "mirror flush", "close", "transcripts mkdir", "transcripts local mkdir", "transcripts create", "prepare", "write files", "chat files", "start", "attach chat", "liveness", "mirror pause", "transcripts pause"},
			ownConnection,
			"claude --resume " + sessionUUID + " --output-format stream-json", nil,
		},
		{
			"running chat with no transcript yet closed and started again",
			newArgs, probeOut(true, false, listed), false, nil, nil,
			[]string{"mirror mkdir", "mirror create", "mirror flush", "close", "transcripts mkdir", "transcripts local mkdir", "transcripts create", "prepare", "write files", "chat files", "start", "attach chat", "liveness", "mirror pause", "transcripts pause"},
			ownConnection,
			"claude --output-format stream-json --verbose --input-format stream-json --session-id=" + sessionUUID, nil,
		},
		{
			"running chat whose Claude outlives the close: nothing started",
			resumeArgs, probeOut(true, true, listed), false, nil, exitErr{code: 4},
			[]string{"mirror mkdir", "mirror create", "mirror flush", "close", "mirror pause"},
			nil,
			"", sandbox.ErrClaudeLingers,
		},
		{
			"running outside Hermod refused",
			resumeArgs, probeOut(false, true, listed), false, nil, nil,
			[]string{"mirror mkdir", "mirror create", "mirror flush", "liveness", "mirror pause"},
			nil,
			"", ErrRunningOutside,
		},
		{
			"resume of an unknown chat fails",
			resumeArgs, probeOut(false, false, notListed), false, nil, nil,
			[]string{"mirror mkdir", "mirror create", "mirror flush", "liveness", "mirror pause"},
			nil,
			"", ErrNothingToResume,
		},
		{
			"Claude gone by the attach",
			newArgs, probeOut(false, false, notListed), false, exitErr{code: 3}, nil,
			[]string{"mirror mkdir", "mirror create", "mirror flush", "transcripts mkdir", "transcripts local mkdir", "transcripts create", "prepare", "write files", "chat files", "start", "attach chat", "liveness", "mirror pause", "transcripts pause"},
			ownConnection,
			"claude --output-format stream-json", errAny,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configDir := filepath.Join(t.TempDir(), "claude")
			if tc.localTranscript {
				writeFile(t, claude.TranscriptPath(configDir, projectDir, mustID(t)), "{\"type\":\"user\"}\n")
			}
			effective := &scripted{replies: []reply{
				{match: "hookport", ret: shell.Result{Stdout: hookPort + "\n"}},
				{match: "tail -c +1", err: tc.attachErr},
				{match: "pane_pid", err: tc.closeErr},
			}}
			o := agentOptions(tc.argv, configDir, io.Discard)
			o.StandIn = true
			err := runAgentWithin(context.Background(), o, effective, realProbes(tc.probe, nil), claude.Classify(tc.argv), fixedRandom(), testWindow)
			checkErr(t, err, tc.wantErr)

			var steps []string
			for _, cmd := range effective.cmds {
				steps = append(steps, step(cmd))
				if step(cmd) == "attach chat" && !reflect.DeepEqual(cmd.Argv[:min(len(tc.wantAttach), len(cmd.Argv))], tc.wantAttach) {
					t.Errorf("attach %q, want one starting %q", cmd.Argv, tc.wantAttach)
				}
			}
			if !reflect.DeepEqual(steps, tc.wantSteps) {
				t.Errorf("steps =\n %q\nwant\n %q", steps, tc.wantSteps)
			}
			start := startLine(effective)
			if (start != "") != (tc.wantClaude != "") || !strings.Contains(start, tc.wantClaude) {
				t.Errorf("start %q, want one holding %q", start, tc.wantClaude)
			}
			assertNoSecretOnACommandLine(t, effective.cmds)
			assertSetupReadsNoStdin(t, effective)
		})
	}
}

// TestRunChatTagsTheSession asserts an Air chat is tagged as one.
func TestRunChatTagsTheSession(t *testing.T) {
	cases := []struct {
		name     string
		standIn  bool
		wantHave []string
	}{
		{"Air's chat", true, []string{"@hermod-mode air-acp", "HERMOD_SESSION=" + sessionUUID, "HERMOD_MODE=air-acp"}},
		{"a chat typed by hand", false, []string{"@hermod-mode attach", "HERMOD_MODE=attach"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			argv := chatArgv("--session-id", sessionUUID)
			effective := &scripted{replies: []reply{{match: "hookport", ret: shell.Result{Stdout: hookPort + "\n"}}}}
			o := agentOptions(argv, t.TempDir(), io.Discard)
			o.StandIn = tc.standIn
			if err := runAgentWithin(context.Background(), o, effective, realProbes(probeOut(false, false, notListed), nil), claude.Classify(argv), fixedRandom(), testWindow); err != nil {
				t.Fatalf("runAgent() error: %v", err)
			}
			start := startLine(effective)
			for _, want := range tc.wantHave {
				if !strings.Contains(start, want) {
					t.Errorf("start %q lacks %q", start, want)
				}
			}
		})
	}
}

// TestRunChatStream asserts the caller's stdin reaches the sandbox and Claude's
// output reaches the caller, line by line with paths mapped each way, and that
// nothing of Hermod's own reaches the caller's stdout, whoever started the chat.
func TestRunChatStream(t *testing.T) {
	cases := []struct {
		name       string
		standIn    bool
		stdin      string
		sandboxOut string
		wantToBox  string
		wantOut    string
	}{
		{
			"path values mapped both ways, prose left as it is", true,
			`{"type":"control_request","request":{"cwd":"/home/me/dev/api"}}` + "\n" + `{"type":"user","message":"see /home/me/dev/api/main.go"}` + "\n",
			`{"type":"system","cwd":"/home/u/dev/api"}` + "\n" + `{"type":"result","files":["/home/u/dev/api/main.go"]}` + "\n",
			`{"type":"control_request","request":{"cwd":"/home/u/dev/api"}}` + "\n" + `{"type":"user","message":"see /home/me/dev/api/main.go"}` + "\n",
			`{"type":"system","cwd":"/home/me/dev/api"}` + "\n" + `{"type":"result","files":["/home/me/dev/api/main.go"]}` + "\n",
		},
		{
			"a chat typed by hand: only Claude's lines on stdout", false,
			`{"a":1}` + "\n",
			`{"b":2}` + "\n",
			`{"a":1}` + "\n",
			`{"b":2}` + "\n",
		},
		{
			"a partial last stdin line never sent, a partial last output line delivered", true,
			`{"a":1}` + "\n" + `{"type":"user","mess`,
			`{"b":2}` + "\n" + `{"c":"/home/u/dev/api"}`,
			`{"a":1}` + "\n",
			`{"b":2}` + "\n" + `{"c":"/home/me/dev/api"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			argv := chatArgv("--session-id", sessionUUID)
			effective := &scripted{replies: []reply{
				{match: "hookport", ret: shell.Result{Stdout: hookPort + "\n"}},
				{match: "tail -c +1", stdout: tc.sandboxOut},
				{match: "mutagen sync create", stdout: "Created session sync_x\n"},
			}}
			var stdout bytes.Buffer
			o := agentOptions(argv, t.TempDir(), io.Discard)
			o.StandIn = tc.standIn
			o.Stdio.In, o.Stdio.Out = strings.NewReader(tc.stdin), &stdout
			if err := runAgentWithin(context.Background(), o, effective, realProbes(probeOut(false, false, notListed), nil), claude.Classify(argv), fixedRandom(), testWindow); err != nil {
				t.Fatalf("runAgent() error: %v", err)
			}
			toBox := ""
			for i, cmd := range effective.cmds {
				if step(cmd) == "attach chat" {
					toBox = effective.stdins[i]
				}
			}
			if toBox != tc.wantToBox {
				t.Errorf("sandbox got %q, want %q", toBox, tc.wantToBox)
			}
			if stdout.String() != tc.wantOut {
				t.Errorf("caller got %q, want %q", stdout.String(), tc.wantOut)
			}
		})
	}
}

// TestRunChatDetach asserts each way an attach ends is a detach: the run
// returns in time, closes nothing on the sandbox, and pauses the mirror for the
// session that keeps running, also when the sandbox no longer answers.
func TestRunChatDetach(t *testing.T) {
	ended := reply{match: "tail -c +1", untilCancelled: true}
	dropped := reply{match: "tail -c +1", ignoreStdin: true, err: exitErr{code: 255}}
	answers := livenessReply(sandboxDir)
	unreachable := reply{match: "list-sessions", untilCancelled: true}
	cases := []struct {
		name      string
		stdinEnds bool  // the caller's stdin ends; otherwise it stays open
		cancel    bool  // the run's context is cancelled once the attach runs, as SIGTERM does
		attach    reply // how the connection behaves
		liveness  reply // how the sandbox answers the liveness check
		wantAfter []string
		wantLog   string
	}{
		{"stdin ends, the connection does not", true, false, ended, answers, []string{"liveness", "mirror pause", "transcripts pause"}, "cutting it"},
		{"SIGTERM", false, true, ended, answers, []string{"liveness", "mirror pause", "transcripts pause"}, "attach ended with error"},
		{"connection dropped", false, false, dropped, answers, []string{"liveness", "mirror pause", "transcripts pause"}, "attach ended with error"},
		{"connection dropped, sandbox unreachable", false, false, dropped, unreachable, []string{"liveness", "mirror pause", "transcripts pause"}, "settling the mirror took over"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			argv := chatArgv("--session-id", sessionUUID)
			effective := &scripted{
				replies: []reply{{match: "hookport", ret: shell.Result{Stdout: hookPort + "\n"}}, tc.attach, tc.liveness},
				started: make(chan struct{}),
			}
			probes := realProbes(probeOut(false, false, notListed), nil)
			var stderr bytes.Buffer
			o := agentOptions(argv, t.TempDir(), &stderr)
			stdin, feed := io.Pipe()
			defer feed.Close()
			o.Stdio.In = stdin
			if tc.stdinEnds {
				_ = feed.Close()
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel {
				go func() { <-effective.started; cancel() }()
			}
			done := make(chan error, 1)
			go func() {
				done <- runAgentWithin(ctx, o, effective, probes, claude.Classify(argv), fixedRandom(), testWindow)
			}()
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("runAgent() error: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the run did not end")
			}
			var after []string
			attached := false
			for _, cmd := range effective.cmds {
				s := step(cmd)
				if attached {
					after = append(after, s)
				}
				attached = attached || s == "attach chat"
			}
			if !reflect.DeepEqual(after, tc.wantAfter) {
				t.Errorf("after the attach: %q, want %q", after, tc.wantAfter)
			}
			if !strings.Contains(stderr.String(), tc.wantLog) {
				t.Errorf("log %q lacks %q", stderr.String(), tc.wantLog)
			}
		})
	}
}

// TestRunChatDryRun asserts a dry run plans the chat runtime and holds no
// token.
func TestRunChatDryRun(t *testing.T) {
	cases := []struct {
		name     string
		probe    string
		wantPlan []string
	}{
		{"new chat", probeOut(false, false, notListed), []string{"mkfifo in.fifo", "new-session -d -s hermod-" + sessionUUID, "tail -c +1"}},
		{"running chat", probeOut(true, true, listed), []string{"kill-session", "--resume " + sessionUUID, "tail -c +1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			argv := chatArgv("--session-id", sessionUUID)
			var plan, stderr bytes.Buffer
			o := agentOptions(argv, t.TempDir(), &stderr)
			o.DryRun = true
			if err := runAgentWithin(context.Background(), o, shell.NewDryRun(&plan), realProbes(tc.probe, nil), claude.Classify(argv), fixedRandom(), testWindow); err != nil {
				t.Fatalf("runAgent() error: %v", err)
			}
			for _, want := range tc.wantPlan {
				if !strings.Contains(plan.String(), want) {
					t.Errorf("plan lacks %q:\n%s", want, plan.String())
				}
			}
			if strings.Contains(plan.String()+stderr.String(), sessionToken) {
				t.Error("the token shows in the plan or the log")
			}
		})
	}
}

func TestRuntimeOf(t *testing.T) {
	cases := []struct {
		name string
		kind claude.Kind
		want string
	}{
		{"chat", claude.ChatSession, "chat"},
		{"terminal", claude.TerminalSession, "terminal"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := runtimeOf(tc.kind).String(); got != tc.want {
				t.Errorf("runtimeOf() = %q, want %q", got, tc.want)
			}
		})
	}
}
