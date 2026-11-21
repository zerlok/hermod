package control

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/zerlok/hermod/internal/claude"
	"github.com/zerlok/hermod/internal/sandbox"
	"github.com/zerlok/hermod/internal/secret"
	"github.com/zerlok/hermod/internal/shell"
)

const (
	airAuth       = "Bearer air-run-token"
	airHookSecret = "air-run-token"
	hookPort      = "45001"
	sandboxHome   = "/home/u"
	projectDir    = "/home/me/dev/api"
	sandboxDir    = "dev/api"
)

// fixedRandom stands in for crypto/rand: every hook port candidate is 11799
// (10000 + 0x0707 mod 22768) and every token is 07 repeated.
func fixedRandom() io.Reader { return bytes.NewReader(bytes.Repeat([]byte{7}, 4096)) }

var sessionToken = strings.Repeat("07", 32)

// reply answers the commands whose joined argv contains match.
type reply struct {
	match  string
	ret    shell.Result
	err    error
	stderr string // written to the command's Stderr, as ssh writes its warnings
	stdout string // written to the command's Stdout, as the sandbox answers a stream
	// ignoreStdin answers without reading the command's stdin, as a connection
	// that drops does.
	ignoreStdin bool
	// untilCancelled answers only once the command's context is done, with its
	// error, as a connection that never ends on its own does.
	untilCancelled bool
}

// scripted is a shell leaf that records every command, with the stdin it was
// given, and answers by the first reply that matches.
type scripted struct {
	replies  []reply
	fallback reply
	started  chan struct{} // closed when a reply that waits for cancellation starts waiting
	mu       sync.Mutex
	cmds     []shell.Command
	stdins   []string
}

func (s *scripted) Run(ctx context.Context, cmd shell.Command) (shell.Result, error) {
	// A command whose context is done never starts, as with os/exec.
	if err := ctx.Err(); err != nil {
		return shell.Result{}, err
	}
	line := strings.Join(cmd.Argv, " ")
	r := s.fallback
	for _, c := range s.replies {
		if strings.Contains(line, c.match) {
			r = c
			break
		}
	}
	in := ""
	if cmd.Stdin != nil && cmd.Stdin != os.Stdin && !r.ignoreStdin && !r.untilCancelled {
		b, _ := io.ReadAll(cmd.Stdin)
		in = string(b)
	}
	s.mu.Lock()
	s.cmds = append(s.cmds, cmd)
	s.stdins = append(s.stdins, in)
	s.mu.Unlock()
	if r.stdout != "" && cmd.Stdout != nil {
		_, _ = io.WriteString(cmd.Stdout, r.stdout)
	}
	if r.stderr != "" && cmd.Stderr != nil {
		_, _ = io.WriteString(cmd.Stderr, r.stderr)
	}
	if r.untilCancelled {
		if s.started != nil {
			close(s.started)
		}
		<-ctx.Done()
		return shell.Result{}, ctx.Err()
	}
	return r.ret, r.err
}

// step names a command the terminal run issues, so a case can list them in
// order.
func step(cmd shell.Command) string {
	line := strings.Join(cmd.Argv, " ")
	switch {
	case strings.Contains(line, "one-way-safe"):
		return "transcripts create"
	case strings.Contains(line, "mutagen sync") && strings.Contains(line, "-transcripts"):
		return "transcripts " + map[string]string{"flush": "flush", "pause": "pause", "terminate": "close", "resume": "resume"}[cmd.Argv[2]]
	case cmd.Argv[0] == "mkdir":
		return "transcripts local mkdir"
	case strings.Contains(line, "mkdir -p -- "+sandboxHome+"/.claude/projects/"):
		return "transcripts mkdir"
	case strings.Contains(line, "list-sessions"):
		return "liveness"
	case strings.Contains(line, "mutagen sync create"):
		return "mirror create"
	case strings.Contains(line, "mutagen sync resume"):
		return "mirror resume"
	case strings.Contains(line, "tmux=yes"):
		return "look up"
	case strings.Contains(line, "mutagen sync flush"):
		return "mirror flush"
	case strings.Contains(line, "mutagen sync pause"):
		return "mirror pause"
	case strings.Contains(line, "mutagen sync terminate"):
		return "mirror close"
	case strings.Contains(line, "mkdir -p -- "+sandboxDir):
		return "mirror mkdir"
	case strings.Contains(line, "pane_pid"):
		return "close"
	case strings.Contains(line, "kill-session"):
		return "remove stale"
	case strings.Contains(line, "mkfifo"):
		return "chat files"
	case strings.Contains(line, "tail -c +1"):
		return "attach chat"
	case strings.Contains(line, "$1.part"):
		return "copy transcript"
	case strings.Contains(line, "hookport"):
		return "prepare"
	case strings.Contains(line, ".incoming"):
		return "write files"
	case strings.Contains(line, "new-session -d"):
		return "start"
	case strings.Contains(line, "attach-session"):
		return "attach"
	}
	return line
}

func probeOut(tmux bool, transcript bool, agents string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "home=%s\nconfig=%s/.claude\n", sandboxHome, sandboxHome)
	if tmux {
		b.WriteString("tmux=yes\n")
	} else {
		b.WriteString("tmux=no\n")
	}
	if transcript {
		fmt.Fprintf(&b, "transcript=%s/.claude/projects/-home-u-dev-api/%s.jsonl\n", sandboxHome, sessionUUID)
	}
	fmt.Fprintf(&b, "hookport=%s\ntoken=%s\nagents:\n%s", hookPort, sessionToken, agents)
	return b.String()
}

var (
	listed    = `[{"sessionId":"` + sessionUUID + `"}]`
	notListed = `[]`
)

// airSettings is the hook settings file Air hands Claude.
const airSettings = `{"hooks":{"Stop":[{"hooks":[{"type":"http","url":"http://localhost:63342/air/claude/hook","headers":{"Authorization":"` + airAuth + `"},"timeout":1}]}]}}`

func TestRunTerminal(t *testing.T) {
	newArgs := []string{"claude", "--session-id", sessionUUID, "--settings", "{settings}"}
	resumeArgs := []string{"claude", "--resume", sessionUUID, "--settings", "{settings}"}
	cases := []struct {
		name            string
		argv            []string
		probe           string
		probeErr        error
		localTranscript bool
		noSettingsFile  bool
		standIn         bool
		wantSteps       []string
		wantClaude      string // a substring of the start command; "" when none starts
		wantForward     bool   // the attach carries the hook forward
		wantStdin       map[string]string
		wantErr         error // nil: success; errAny: any error
	}{
		{
			"new session started and attached with hooks",
			newArgs, probeOut(false, false, notListed), nil, false, false, true,
			[]string{"mirror mkdir", "mirror create", "mirror flush", "transcripts mkdir", "transcripts local mkdir", "transcripts create", "prepare", "write files", "start", "attach", "liveness", "mirror pause", "transcripts pause"},
			"claude --session-id " + sessionUUID + " --settings " + sandboxHome + "/.hermod/s/" + sessionUUID + "/settings.json",
			true, nil, nil,
		},
		{
			"closed session on the sandbox resumed",
			newArgs, probeOut(false, true, notListed), nil, false, false, false,
			[]string{"mirror mkdir", "mirror create", "mirror flush", "transcripts mkdir", "transcripts local mkdir", "transcripts create", "prepare", "write files", "start", "attach", "liveness", "mirror pause", "transcripts pause"},
			"claude --resume " + sessionUUID + " --settings",
			true, nil, nil,
		},
		{
			"closed session known only locally: transcript copied up, then resumed",
			resumeArgs, probeOut(false, false, notListed), nil, true, false, false,
			[]string{"mirror mkdir", "mirror create", "mirror flush", "transcripts mkdir", "transcripts local mkdir", "transcripts create", "copy transcript", "prepare", "write files", "start", "attach", "liveness", "mirror pause", "transcripts pause"},
			"claude --resume " + sessionUUID + " --settings",
			true, map[string]string{"copy transcript": "{\"type\":\"user\"}\n"}, nil,
		},
		{
			"stale tmux session removed before the start",
			newArgs, probeOut(true, false, notListed), nil, false, false, false,
			[]string{"mirror mkdir", "mirror create", "mirror flush", "transcripts mkdir", "transcripts local mkdir", "transcripts create", "remove stale", "prepare", "write files", "start", "attach", "liveness", "mirror pause", "transcripts pause"},
			"claude --session-id " + sessionUUID,
			true, nil, nil,
		},
		{
			"running session attached, nothing started",
			resumeArgs, probeOut(true, true, listed), nil, false, false, false,
			[]string{"mirror mkdir", "mirror create", "mirror flush", "transcripts mkdir", "transcripts local mkdir", "transcripts create", "attach", "liveness", "mirror pause", "transcripts pause"},
			"", true, nil, nil,
		},
		{
			"no settings, no hooks",
			[]string{"claude", "--session-id", sessionUUID}, probeOut(false, false, notListed), nil, false, true, false,
			[]string{"mirror mkdir", "mirror create", "mirror flush", "transcripts mkdir", "transcripts local mkdir", "transcripts create", "prepare", "write files", "start", "attach", "liveness", "mirror pause", "transcripts pause"},
			"claude --session-id " + sessionUUID + `'`, false, nil, nil,
		},
		{
			"running outside Hermod refused",
			resumeArgs, probeOut(false, true, listed), nil, false, false, false,
			[]string{"mirror mkdir", "mirror create", "mirror flush", "liveness", "mirror pause"},
			"", false, nil, ErrRunningOutside,
		},
		{
			"resume of an unknown session fails",
			resumeArgs, probeOut(false, false, notListed), nil, false, false, false,
			[]string{"mirror mkdir", "mirror create", "mirror flush", "liveness", "mirror pause"},
			"", false, nil, ErrNothingToResume,
		},
		{
			"failed probe starts nothing",
			newArgs, "", exitErr{code: 255}, false, false, false,
			[]string{"mirror mkdir", "mirror create", "mirror flush", "liveness", "mirror pause"},
			"", false, nil, errAny,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			configDir := filepath.Join(base, "claude")
			settingsFile := filepath.Join(base, "settings.json")
			if !tc.noSettingsFile {
				writeFile(t, settingsFile, airSettings)
			}
			if tc.localTranscript {
				writeFile(t, claude.TranscriptPath(configDir, projectDir, mustID(t)), "{\"type\":\"user\"}\n")
			}
			argv := make([]string, len(tc.argv))
			for i, a := range tc.argv {
				argv[i] = strings.ReplaceAll(a, "{settings}", settingsFile)
			}
			effective := &scripted{replies: []reply{{match: "hookport", ret: shell.Result{Stdout: hookPort + "\n"}}}}
			real := realProbes(tc.probe, tc.probeErr)
			var stderr bytes.Buffer
			o := agentOptions(argv, configDir, &stderr)
			o.StandIn = tc.standIn

			err := runAgent(context.Background(), o, effective, real, claude.Classify(argv), fixedRandom())
			checkErr(t, err, tc.wantErr)

			var steps []string
			byStep := map[string]int{}
			for i, cmd := range effective.cmds {
				steps = append(steps, step(cmd))
				byStep[step(cmd)] = i
			}
			if !reflect.DeepEqual(steps, tc.wantSteps) {
				t.Errorf("steps =\n %q\nwant\n %q", steps, tc.wantSteps)
			}
			for name, want := range tc.wantStdin {
				if got := effective.stdins[byStep[name]]; got != want {
					t.Errorf("%s stdin = %q, want %q", name, got, want)
				}
			}
			if i, ok := byStep["start"]; ok != (tc.wantClaude != "") || ok && !strings.Contains(strings.Join(effective.cmds[i].Argv, " "), tc.wantClaude) {
				t.Errorf("start = %v, want one holding %q", startLine(effective), tc.wantClaude)
			}
			if i, ok := byStep["attach"]; ok {
				attachArgv := effective.cmds[i].Argv
				hasForward := strings.Contains(strings.Join(attachArgv, " "), "-R "+hookPort+":127.0.0.1:")
				if hasForward != tc.wantForward {
					t.Errorf("attach %q: forward = %v, want %v", attachArgv, hasForward, tc.wantForward)
				}
				if !reflect.DeepEqual(attachArgv[:4], []string{"ssh", "-t", "-o", "ControlPath=none"}) {
					t.Errorf("attach %q does not run on its own tty connection", attachArgv)
				}
			}
			assertNoSecretOnACommandLine(t, append(effective.cmds, real.cmds...))
			assertSetupReadsNoStdin(t, effective)
		})
	}
}

// TestRunTerminalForkCopiesItsSource asserts a new fork of a session that only
// this machine has copies that session's transcript up before it starts.
func TestRunTerminalForkCopiesItsSource(t *testing.T) {
	const (
		source = "5d6e1e2a-3b4c-4c8e-9d6f-0b7e5f3c2a41"
		body   = "{\"type\":\"user\"}\n"
	)
	onSandbox := "transcript=" + sandboxHome + "/.claude/projects/-home-u-dev-api/" + source + ".jsonl\n"
	cases := []struct {
		name      string
		local     bool // this machine has the source's transcript
		sandbox   bool // the sandbox has it
		wantSteps []string
		wantCopy  string // the copy's stdin; "" when none
	}{
		{"only local: copied up", true, false, []string{"mirror mkdir", "mirror create", "mirror flush", "transcripts mkdir", "transcripts local mkdir", "transcripts create", "copy transcript", "prepare", "write files", "start", "attach", "liveness", "mirror pause", "transcripts pause"}, body},
		{"on the sandbox: nothing copied", true, true, []string{"mirror mkdir", "mirror create", "mirror flush", "transcripts mkdir", "transcripts local mkdir", "transcripts create", "prepare", "write files", "start", "attach", "liveness", "mirror pause", "transcripts pause"}, ""},
		{"nowhere: left to Claude", false, false, []string{"mirror mkdir", "mirror create", "mirror flush", "transcripts mkdir", "transcripts local mkdir", "transcripts create", "prepare", "write files", "start", "attach", "liveness", "mirror pause", "transcripts pause"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configDir := filepath.Join(t.TempDir(), "claude")
			sourceID, _ := claude.ParseSessionID(source)
			if tc.local {
				writeFile(t, claude.TranscriptPath(configDir, projectDir, sourceID), body)
			}
			probe := probeOut(false, false, notListed)
			if tc.sandbox {
				probe = strings.Replace(probe, "tmux=no\n", "tmux=no\n"+onSandbox, 1)
			}
			argv := []string{"claude", "--resume", source, "--fork-session", "--session-id", sessionUUID}
			effective := &scripted{replies: []reply{{match: "hookport", ret: shell.Result{Stdout: hookPort + "\n"}}}}
			real := realProbes(probe, nil)
			if err := runAgent(context.Background(), agentOptions(argv, configDir, io.Discard), effective, real, claude.Classify(argv), fixedRandom()); err != nil {
				t.Fatalf("runAgent() error: %v", err)
			}
			var steps []string
			copied := ""
			for i, cmd := range effective.cmds {
				steps = append(steps, step(cmd))
				if step(cmd) == "copy transcript" {
					copied = effective.stdins[i]
				}
			}
			if !reflect.DeepEqual(steps, tc.wantSteps) {
				t.Errorf("steps =\n %q\nwant\n %q", steps, tc.wantSteps)
			}
			if copied != tc.wantCopy {
				t.Errorf("copied %q, want %q", copied, tc.wantCopy)
			}
			if start := startLine(effective); !strings.Contains(start, "--resume "+source+" --fork-session --session-id "+sessionUUID) {
				t.Errorf("start %q does not fork the source", start)
			}
		})
	}
}

// TestRunTerminalTagsTheSession asserts the start carries the mode and origin as
// tmux options and as Claude's environment.
func TestRunTerminalTagsTheSession(t *testing.T) {
	cases := []struct {
		name     string
		standIn  bool
		origin   string
		wantHave []string
		wantNot  []string
	}{
		{
			"Air's terminal tab from a named machine",
			true, "laptop.local",
			[]string{"@hermod-mode air-terminal", "@hermod-origin laptop.local", "@hermod-dir dev/api", "HERMOD_SESSION=" + sessionUUID, "HERMOD_MODE=air-terminal", "HERMOD_ORIGIN=laptop.local"},
			nil,
		},
		{
			"a person's attach from an unnamed machine",
			false, "",
			[]string{"@hermod-mode attach", "@hermod-dir dev/api", "HERMOD_SESSION=" + sessionUUID, "HERMOD_MODE=attach"},
			[]string{"@hermod-origin", "HERMOD_ORIGIN"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			argv := []string{"claude", "--session-id", sessionUUID}
			effective := &scripted{replies: []reply{{match: "hookport", ret: shell.Result{Stdout: hookPort + "\n"}}}}
			o := agentOptions(argv, t.TempDir(), io.Discard)
			o.StandIn, o.Origin = tc.standIn, tc.origin
			if err := runAgent(context.Background(), o, effective, realProbes(probeOut(false, false, notListed), nil), claude.Classify(argv), fixedRandom()); err != nil {
				t.Fatalf("runAgent() error: %v", err)
			}
			start := startLine(effective)
			for _, want := range tc.wantHave {
				if !strings.Contains(start, want) {
					t.Errorf("start %q lacks %q", start, want)
				}
			}
			for _, not := range tc.wantNot {
				if strings.Contains(start, not) {
					t.Errorf("start %q has %q", start, not)
				}
			}
		})
	}
}

// TestRunTerminalCarriesEnvironment asserts the env file holds Claude's
// variables and the git identity, and nothing that names a local path.
func TestRunTerminalCarriesEnvironment(t *testing.T) {
	cases := []struct {
		name     string
		environ  []string
		wantHave []string
		wantNot  []string
	}{
		{
			"selected variables",
			[]string{"CLAUDE_CODE_ENTRYPOINT=cli", "CLAUDE_CODE_EXECUTABLE=/home/me/.local/share/hermod/bin/claude", "CLAUDE_CONFIG_DIR=/home/me/.claude", "ANTHROPIC_API_KEY=sk-ant-x", "CLAUDE_CODE_EXECPATH=/opt/claude/bin/claude", "CLAUDE_CODE_MESSAGING_TOKEN=parent-token", "PATH=/usr/bin"},
			[]string{"export CLAUDE_CODE_ENTRYPOINT=cli\n", "export ANTHROPIC_API_KEY=sk-ant-x\n", "export GIT_AUTHOR_NAME='Jane Doe'\n"},
			[]string{"CLAUDE_CODE_EXECUTABLE", "CLAUDE_CONFIG_DIR", "CLAUDE_CODE_EXECPATH", "parent-token", "PATH"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			argv := []string{"claude", "--session-id", sessionUUID}
			effective := &scripted{replies: []reply{{match: "hookport", ret: shell.Result{Stdout: hookPort + "\n"}}}}
			real := realProbes(probeOut(false, false, notListed), nil)
			real.replies = append([]reply{{match: "user.name", ret: shell.Result{Stdout: "Jane Doe\n"}}}, real.replies...)
			o := agentOptions(argv, t.TempDir(), io.Discard)
			o.Environ = secret.NewEnv(tc.environ)
			if err := runAgent(context.Background(), o, effective, real, claude.Classify(argv), fixedRandom()); err != nil {
				t.Fatalf("runAgent() error: %v", err)
			}
			var written string
			for i, cmd := range effective.cmds {
				if step(cmd) == "write files" {
					written = effective.stdins[i]
				}
			}
			for _, want := range tc.wantHave {
				if !strings.Contains(written, want) {
					t.Errorf("files %q lack %q", written, want)
				}
			}
			for _, not := range tc.wantNot {
				if strings.Contains(written, not) {
					t.Errorf("files %q hold %q", written, not)
				}
			}
			assertNoSecretOnACommandLine(t, effective.cmds, "sk-ant-x")
		})
	}
}

// TestRunTerminalCarriesMCPConfigs asserts every `--mcp-config` value, inline
// or a local file, reaches the sandbox merged into the session's mcp.json over
// stdin, its secrets on no command line and its local programs left out.
func TestRunTerminalCarriesMCPConfigs(t *testing.T) {
	const (
		ideServer = `{"mcpServers":{"jetbrains_air_ide":{"command":"/opt/goland/bin/goland","env":{"IJ_MCP_AUTH_TOKEN":"ide-secret"}}}}`
		webServer = `{"mcpServers":{"web":{"type":"http","url":"https://mcp.example/x","headers":{"Authorization":"Bearer mcp-secret"}}}}`
		sessMCP   = sandboxHome + "/.hermod/s/" + sessionUUID + "/mcp.json"
	)
	cases := []struct {
		name      string
		configs   map[string]string // a local file name → its body; "" names an inline value
		inline    []string
		wantStart string // a substring of the start line; "" when none starts
		wantFile  string // a substring of what is written; "" when nothing is
		wantNot   []string
		wantErr   error
	}{
		{
			"inline and a file, merged",
			map[string]string{"mcp.json": webServer}, []string{ideServer},
			"--mcp-config " + sessMCP, `"web":{"headers":{"Authorization":"Bearer mcp-secret"}`,
			[]string{"jetbrains_air_ide", "ide-secret"}, nil,
		},
		{
			"only local programs: the option is dropped",
			nil, []string{ideServer},
			"claude --session-id " + sessionUUID + `'`, "",
			[]string{"--mcp-config", "jetbrains_air_ide"}, nil,
		},
		{
			"a missing file fails before anything runs",
			map[string]string{"missing.json": ""}, nil,
			"", "", nil, errAny,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			argv := []string{"claude", "--session-id", sessionUUID}
			for name, body := range tc.configs {
				p := filepath.Join(base, name)
				if body != "" {
					writeFile(t, p, body)
				}
				argv = append(argv, "--mcp-config", p)
			}
			for _, v := range tc.inline {
				argv = append(argv, "--mcp-config", v)
			}
			effective := &scripted{replies: []reply{{match: "hookport", ret: shell.Result{Stdout: hookPort + "\n"}}}}
			real := realProbes(probeOut(false, false, notListed), nil)
			err := runAgent(context.Background(), agentOptions(argv, base, io.Discard), effective, real, claude.Classify(argv), fixedRandom())
			checkErr(t, err, tc.wantErr)
			start := startLine(effective)
			if (start != "") != (tc.wantStart != "") || !strings.Contains(start, tc.wantStart) {
				t.Errorf("start %q, want one holding %q", start, tc.wantStart)
			}
			written := ""
			for i, cmd := range effective.cmds {
				if step(cmd) == "write files" {
					written = effective.stdins[i]
				}
			}
			if !strings.Contains(written, tc.wantFile) {
				t.Errorf("files %q lack %q", written, tc.wantFile)
			}
			for _, not := range tc.wantNot {
				if strings.Contains(start, not) || strings.Contains(written, not) {
					t.Errorf("%q reaches the sandbox", not)
				}
			}
			assertNoSecretOnACommandLine(t, append(effective.cmds, real.cmds...), "mcp-secret", "ide-secret")
		})
	}
}

// TestRunTerminalWithASecondLocalHookEndpoint asserts settings with hooks to
// Air and to another local endpoint start the session with Air's hooks
// forwarded and the other's left out, logged.
func TestRunTerminalWithASecondLocalHookEndpoint(t *testing.T) {
	cases := []struct {
		name     string
		settings string
		wantLog  string
		wantNot  string // in the written settings
	}{
		{
			"Air's and the user's own",
			`{"hooks":{"Stop":[{"hooks":[{"type":"http","url":"http://localhost:63342/air/claude/hook","headers":{"Authorization":"` + airAuth + `"}},{"type":"http","url":"http://localhost:9000/mine"}]}]}}`,
			"left out on the sandbox: http://localhost:9000/mine",
			"9000",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			settingsFile := filepath.Join(base, "settings.json")
			writeFile(t, settingsFile, tc.settings)
			argv := []string{"claude", "--session-id", sessionUUID, "--settings", settingsFile}
			effective := &scripted{replies: []reply{{match: "hookport", ret: shell.Result{Stdout: hookPort + "\n"}}}}
			var stderr bytes.Buffer
			err := runAgent(context.Background(), agentOptions(argv, base, &stderr), effective, realProbes(probeOut(false, false, notListed), nil), claude.Classify(argv), fixedRandom())
			if err != nil {
				t.Fatalf("runAgent() error: %v", err)
			}
			var written, attach string
			for i, cmd := range effective.cmds {
				switch step(cmd) {
				case "write files":
					written = effective.stdins[i]
				case "attach":
					attach = strings.Join(cmd.Argv, " ")
				}
			}
			if !strings.Contains(attach, "-R "+hookPort+":127.0.0.1:") {
				t.Errorf("attach %q carries no hook forward", attach)
			}
			if strings.Contains(written, tc.wantNot) {
				t.Errorf("settings %q hold %q", written, tc.wantNot)
			}
			if !strings.Contains(stderr.String(), tc.wantLog) {
				t.Errorf("log %q lacks %q", stderr.String(), tc.wantLog)
			}
		})
	}
}

// TestRunTerminalWithoutHooksWhenThePortIsTaken asserts a forward ssh cannot
// bind costs the hooks, logged, and the attach still runs.
func TestRunTerminalWithoutHooksWhenThePortIsTaken(t *testing.T) {
	cases := []struct {
		name      string
		sshStderr string
		wantLog   string
		wantNot   string
	}{
		{"port taken", "Warning: remote port forwarding failed for listen port 45001\r\n", "hooks unavailable: sandbox port 45001 is taken", ""},
		{"port bound", "", "hooks: sandbox port 45001", "hooks unavailable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			settingsFile := filepath.Join(base, "settings.json")
			writeFile(t, settingsFile, airSettings)
			argv := []string{"claude", "--resume", sessionUUID, "--settings", settingsFile}
			effective := &scripted{replies: []reply{{match: "attach-session", stderr: tc.sshStderr}}}
			var stderr bytes.Buffer
			o := agentOptions(argv, base, &stderr)
			err := runAgent(context.Background(), o, effective, realProbes(probeOut(true, true, listed), nil), claude.Classify(argv), fixedRandom())
			if err != nil {
				t.Fatalf("runAgent() error: %v", err)
			}
			attached := false
			for _, cmd := range effective.cmds {
				attached = attached || step(cmd) == "attach"
			}
			if !attached {
				t.Error("the session was not attached")
			}
			if !strings.Contains(stderr.String(), tc.wantLog) {
				t.Errorf("log %q lacks %q", stderr.String(), tc.wantLog)
			}
			if tc.wantNot != "" && strings.Contains(stderr.String(), tc.wantNot) {
				t.Errorf("log %q has %q", stderr.String(), tc.wantNot)
			}
		})
	}
}

// TestRunTerminalClaudeExitedBeforeAttach asserts a session whose Claude is
// gone by the attach fails the run, saying so, rather than opening a shell.
func TestRunTerminalClaudeExitedBeforeAttach(t *testing.T) {
	cases := []struct {
		name      string
		attachErr error
		alive     bool
		wantErr   error
	}{
		{"gone", exitErr{code: 1}, false, sandbox.ErrSessionGone},
		{"connection dropped, session alive", exitErr{code: 255}, true, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			argv := []string{"claude", "--session-id", sessionUUID}
			effective := &scripted{replies: []reply{
				{match: "hookport", ret: shell.Result{Stdout: hookPort + "\n"}},
				{match: "attach-session", err: tc.attachErr},
			}}
			real := realProbes(probeOut(false, false, notListed), nil)
			if !tc.alive {
				real.replies = append([]reply{{match: "has-session -t =hermod-", err: exitErr{code: 1}}}, real.replies...)
			}
			err := runAgent(context.Background(), agentOptions(argv, t.TempDir(), io.Discard), effective, real, claude.Classify(argv), fixedRandom())
			checkErr(t, err, tc.wantErr)
		})
	}
}

// TestRunTerminalSettlesASharedMirror asserts a run settles each of its
// mirrors by its root's liveness, whether or not it started or attached
// anything: a session left under the root keeps the mirror, and one nobody
// uses is closed.
func TestRunTerminalSettlesASharedMirror(t *testing.T) {
	cases := []struct {
		name        string
		argv        []string
		probe       string
		probeErr    error
		liveness    reply
		wantSettled []string
		wantErr     error
	}{
		{"running outside Hermod refused, no session left", []string{"claude", "--resume", sessionUUID}, probeOut(false, true, listed), nil, livenessReply(), []string{"mirror flush", "mirror close"}, ErrRunningOutside},
		{"unknown resume refused, no session left", []string{"claude", "--resume", sessionUUID}, probeOut(false, false, notListed), nil, livenessReply(), []string{"mirror flush", "mirror close"}, ErrNothingToResume},
		{"unknown resume refused, a session left under the root", []string{"claude", "--resume", sessionUUID}, probeOut(false, false, notListed), nil, livenessReply(sandboxDir + "/web"), []string{"mirror pause"}, ErrNothingToResume},
		{"failed probe, liveness unknown", []string{"claude", "--session-id", sessionUUID}, "", exitErr{code: 255}, reply{match: "list-sessions", err: exitErr{code: 255}}, []string{"mirror pause"}, errAny},
		{"attached, then no session left", []string{"claude", "--session-id", sessionUUID}, probeOut(false, false, notListed), nil, livenessReply(), []string{"mirror flush", "mirror close", "transcripts flush", "transcripts close"}, nil},
		{"attached, a session left in a subdirectory", []string{"claude", "--session-id", sessionUUID}, probeOut(false, false, notListed), nil, livenessReply(sandboxDir + "/web"), []string{"mirror pause", "transcripts pause"}, nil},
		{"attached, a session left in the session's directory", []string{"claude", "--session-id", sessionUUID}, probeOut(false, false, notListed), nil, livenessReply(sandboxHome + "/" + sandboxDir), []string{"mirror pause", "transcripts pause"}, nil},
		{"attached, a session left beside the root", []string{"claude", "--session-id", sessionUUID}, probeOut(false, false, notListed), nil, livenessReply(sandboxDir + "-wt"), []string{"mirror flush", "mirror close", "transcripts flush", "transcripts close"}, nil},
		{"attached, liveness unknown", []string{"claude", "--session-id", sessionUUID}, probeOut(false, false, notListed), nil, reply{match: "list-sessions", err: exitErr{code: 255}}, []string{"mirror pause", "transcripts pause"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			effective := &scripted{replies: []reply{{match: "hookport", ret: shell.Result{Stdout: hookPort + "\n"}}, tc.liveness}}
			real := realProbes(tc.probe, tc.probeErr)
			err := runAgent(context.Background(), agentOptions(tc.argv, t.TempDir(), io.Discard), effective, real, claude.Classify(tc.argv), fixedRandom())
			checkErr(t, err, tc.wantErr)
			if settled := settledSteps(effective); !reflect.DeepEqual(settled, tc.wantSettled) {
				t.Errorf("settled %q, want %q", settled, tc.wantSettled)
			}
		})
	}
}

// TestRunTerminalDryRunHidesTokens asserts a dry run's plan names the session's
// files but holds neither Air's token nor the session's.
func TestRunTerminalDryRunHidesTokens(t *testing.T) {
	cases := []struct {
		name     string
		wantPlan []string
	}{
		{"new session", []string{"hookport", ".incoming", "new-session -d -s hermod-" + sessionUUID, "attach-session -t =hermod-" + sessionUUID}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			settingsFile := filepath.Join(base, "settings.json")
			writeFile(t, settingsFile, airSettings)
			argv := []string{"claude", "--session-id", sessionUUID, "--settings", settingsFile}
			var plan, stderr bytes.Buffer
			o := agentOptions(argv, base, &stderr)
			o.DryRun = true
			err := runAgent(context.Background(), o, shell.NewDryRun(&plan), realProbes(probeOut(false, false, notListed), nil), claude.Classify(argv), fixedRandom())
			if err != nil {
				t.Fatalf("runAgent() error: %v", err)
			}
			for _, want := range tc.wantPlan {
				if !strings.Contains(plan.String(), want) {
					t.Errorf("plan lacks %q:\n%s", want, plan.String())
				}
			}
			for _, secret := range []string{sessionToken, airHookSecret} {
				if strings.Contains(plan.String(), secret) || strings.Contains(stderr.String(), secret) {
					t.Errorf("a token shows in the plan or the log")
				}
			}
		})
	}
}

// TestRunTerminalDryRunPlansTheSettle asserts a dry run plans each mirror's
// settle as a pause, saying why, rather than reading the sandbox's sessions
// through the printing shell and reporting the look as failed.
func TestRunTerminalDryRunPlansTheSettle(t *testing.T) {
	cases := []struct {
		name        string
		wantPlan    []string
		wantLog     string
		notExpected []string // in neither the plan nor the log
	}{
		{"new session", []string{"mutagen sync pause hermod-prod-dev-api"}, dryRunSettle, []string{"list-sessions", "liveness check failed"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			argv := []string{"claude", "--session-id", sessionUUID}
			var plan, stderr bytes.Buffer
			o := agentOptions(argv, "", &stderr)
			o.DryRun = true
			if err := runAgent(context.Background(), o, shell.NewDryRun(&plan), realProbes(probeOut(false, false, notListed), nil), claude.Classify(argv), fixedRandom()); err != nil {
				t.Fatalf("runAgent() error: %v", err)
			}
			for _, want := range tc.wantPlan {
				if !strings.Contains(plan.String(), want) {
					t.Errorf("plan lacks %q:\n%s", want, plan.String())
				}
			}
			if !strings.Contains(stderr.String(), tc.wantLog) {
				t.Errorf("log %q lacks %q", stderr.String(), tc.wantLog)
			}
			for _, s := range tc.notExpected {
				if strings.Contains(plan.String(), s) || strings.Contains(stderr.String(), s) {
					t.Errorf("%q in the plan or the log", s)
				}
			}
		})
	}
}

// TestRunTerminalLogsTheSession asserts the stand-in keeps the session's log,
// with the steps and the run's error.
func TestRunTerminalLogsTheSession(t *testing.T) {
	cases := []struct {
		name     string
		argv     []string
		probe    string
		wantLogs []string
		wantErr  error
	}{
		{"started", []string{"claude", "--session-id", sessionUUID}, probeOut(false, false, notListed), []string{"session " + sessionUUID + ": new", "started hermod-" + sessionUUID}, nil},
		{"refused", []string{"claude", "--resume", sessionUUID}, probeOut(false, false, listed), []string{"running outside Hermod"}, ErrRunningOutside},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			logDir := filepath.Join(base, "air")
			effective := &scripted{replies: []reply{{match: "hookport", ret: shell.Result{Stdout: hookPort + "\n"}}}}
			var stderr bytes.Buffer
			o := agentOptions(tc.argv, base, &stderr)
			o.StandIn, o.SessionLogDir = true, logDir
			err := runAgent(context.Background(), o, effective, realProbes(tc.probe, nil), claude.Classify(tc.argv), fixedRandom())
			checkErr(t, err, tc.wantErr)
			body, readErr := os.ReadFile(filepath.Join(logDir, sessionUUID+".log"))
			if readErr != nil {
				t.Fatalf("session log: %v", readErr)
			}
			for _, want := range tc.wantLogs {
				if !strings.Contains(string(body), want) {
					t.Errorf("log %q lacks %q", body, want)
				}
			}
			if err != nil && strings.Count(stderr.String(), err.Error()) != 0 {
				t.Errorf("the error is on stderr twice: the caller prints it")
			}
		})
	}
}

func TestRunTerminalSettingsUnreadable(t *testing.T) {
	cases := []struct {
		name string
		argv []string
	}{
		{"missing settings file", []string{"claude", "--session-id", sessionUUID, "--settings", "/nonexistent/settings.json"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			effective := &scripted{}
			real := realProbes(probeOut(false, false, notListed), nil)
			err := runAgent(context.Background(), agentOptions(tc.argv, t.TempDir(), io.Discard), effective, real, claude.Classify(tc.argv), fixedRandom())
			if err == nil {
				t.Fatal("runAgent() error = nil, want one")
			}
			if len(effective.cmds) != 0 || len(real.cmds) != 0 {
				t.Errorf("commands ran: %v %v", effective.cmds, real.cmds)
			}
		})
	}
}

func TestModeOf(t *testing.T) {
	cases := []struct {
		name    string
		standIn bool
		kind    claude.Kind
		want    sandbox.Mode
	}{
		{"stand-in terminal", true, claude.TerminalSession, sandbox.ModeAirTerminal},
		{"stand-in chat", true, claude.ChatSession, sandbox.ModeAirACP},
		{"typed terminal session", false, claude.TerminalSession, sandbox.ModeAttach},
		{"plain attach", false, claude.NotAgentSession, sandbox.ModeAttach},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := modeOf(tc.standIn, tc.kind); got != tc.want {
				t.Errorf("modeOf() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPortCandidates(t *testing.T) {
	cases := []struct {
		name    string
		random  []byte
		want    int // every candidate, for a uniform source
		wantErr bool
	}{
		{"lowest", make([]byte, 32), hookPortMin, false},
		{"wraps into range", bytes.Repeat([]byte{0xff}, 32), hookPortMin + 0xffff%(hookPortMax-hookPortMin+1), false},
		{"short source", []byte{1}, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := portCandidates(bytes.NewReader(tc.random))
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			for _, p := range got {
				if p != tc.want || p < hookPortMin || p > hookPortMax {
					t.Errorf("candidate %d, want %d within [%d, %d]", p, tc.want, hookPortMin, hookPortMax)
				}
			}
			if !tc.wantErr && len(got) != hookPortCandidates {
				t.Errorf("candidates = %d, want %d", len(got), hookPortCandidates)
			}
		})
	}
}

// livenessReply answers the liveness check with tmux sessions tagged with
// dirs, on a sandbox whose home is sandboxHome.
func livenessReply(dirs ...string) reply {
	out := "home=" + sandboxHome + "\n"
	for _, d := range dirs {
		out += "dir=" + d + "\n"
	}
	return reply{match: "list-sessions", ret: shell.Result{Stdout: out + "dir=\nend\n"}}
}

// settledSteps are the steps a run took to settle its mirrors: those after the
// attach, or after the last setup step when it attached nothing, the liveness
// check left out.
func settledSteps(s *scripted) []string {
	var steps []string
	for _, cmd := range s.cmds {
		steps = append(steps, step(cmd))
	}
	from := 0
	for i, st := range steps {
		if st == "attach" || st == "attach chat" || st == "mirror flush" && from == 0 {
			from = i + 1
		}
	}
	var settled []string
	for _, st := range steps[from:] {
		if st != "liveness" {
			settled = append(settled, st)
		}
	}
	return settled
}

// errAny marks a case that expects some error.
var errAny = errors.New("any error")

func checkErr(t *testing.T, err, want error) {
	t.Helper()
	switch {
	case want == nil && err != nil:
		t.Fatalf("error: %v", err)
	case want == errAny && err == nil:
		t.Fatal("error = nil, want one")
	case want != nil && want != errAny && !errors.Is(err, want):
		t.Fatalf("err = %v, want %v", err, want)
	}
}

// realProbes answers the read-only probes: the agent probe with out, Mutagen as
// for a mirror it cannot find, git with no identity, and the liveness check
// with a running tmux session.
func realProbes(out string, err error) *scripted {
	const notFound = "unable to locate requested sessions"
	return &scripted{
		replies: []reply{
			{match: "claude agents --json", ret: shell.Result{Stdout: out}, err: err},
			{match: "mutagen sync list", ret: shell.Result{Stderr: notFound}, err: errors.New(notFound)},
			{match: "git config", err: exitErr{code: 1}},
			{match: "has-session", ret: shell.Result{}},
		},
	}
}

func agentOptions(argv []string, configDir string, stderr io.Writer) Options {
	return Options{
		Sandbox:         "prod",
		Session:         "dev-api",
		LocalDir:        projectDir,
		RemoteDir:       sandboxDir,
		Command:         sandbox.Exec(argv),
		ClaudeConfigDir: configDir,
		Stdio:           sandbox.Streams{In: strings.NewReader(""), Out: io.Discard, Err: stderr},
	}
}

func startLine(s *scripted) string {
	for _, cmd := range s.cmds {
		if step(cmd) == "start" {
			return strings.Join(cmd.Argv, " ")
		}
	}
	return ""
}

func assertNoSecretOnACommandLine(t *testing.T, cmds []shell.Command, more ...string) {
	t.Helper()
	for _, cmd := range cmds {
		line := strings.Join(append(cmd.Argv, cmd.Env...), " ")
		for _, s := range append([]string{sessionToken, airHookSecret}, more...) {
			if strings.Contains(line, s) {
				t.Errorf("a secret is on the command line %q", cmd.Argv)
			}
		}
	}
}

// assertSetupReadsNoStdin checks that only the attach is given the caller's
// stdin; every setup command captures (empty stdin) or is handed its input.
func assertSetupReadsNoStdin(t *testing.T, s *scripted) {
	t.Helper()
	for _, cmd := range s.cmds {
		if s := step(cmd); s == "attach" || s == "attach chat" {
			continue
		}
		if !cmd.Capture && cmd.Stdin != shell.NoInput {
			t.Errorf("setup %q may read the caller's stdin", cmd.Argv)
		}
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustID(t *testing.T) claude.SessionID {
	t.Helper()
	id, ok := claude.ParseSessionID(sessionUUID)
	if !ok {
		t.Fatal("bad UUID")
	}
	return id
}

// brokenPipe is a stderr whose reader has gone.
type brokenPipe struct{}

func (brokenPipe) Write([]byte) (int, error) { return 0, errors.New("write: broken pipe") }

// TestSessionLoggerKeepsEveryLine asserts the session log gets the step lines
// and every other line Hermod writes to stderr, whether or not stderr can be
// written, and that stderr gets all but the steps -q hides.
func TestSessionLoggerKeepsEveryLine(t *testing.T) {
	cases := []struct {
		name       string
		stderr     string // "buffer" or "broken"
		quiet      bool
		step       string // logged through the logger
		own        string // written to the options' Stdio.Err
		wantLog    []string
		wantStderr string
	}{
		{"both reach the log and stderr", "buffer", false, "session new", "warning",
			[]string{"session new\n", "warning\n"}, "session new\nwarning\n"},
		{"quiet hides the step from stderr only", "buffer", true, "session new", "warning",
			[]string{"session new\n", "warning\n"}, "warning\n"},
		{"a broken stderr costs the log nothing", "broken", false, "session new", "warning",
			[]string{"session new\n", "warning\n"}, ""},
	}
	id, _ := claude.ParseSessionID(sessionUUID)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			var stderr io.Writer = &buf
			if tc.stderr == "broken" {
				stderr = brokenPipe{}
			}
			dir := t.TempDir()
			o := Options{Stdio: sandbox.Streams{Err: stderr}, Quiet: tc.quiet, SessionLogDir: dir}
			out, logger, finish := sessionLogger(o, id)
			logger.Print(tc.step)
			if _, err := fmt.Fprintln(out.Stdio.Err, tc.own); err != nil {
				t.Errorf("write to Stdio.Err: %v", err)
			}
			finish(nil)
			body, err := os.ReadFile(filepath.Join(dir, sessionUUID+".log"))
			if err != nil {
				t.Fatalf("session log: %v", err)
			}
			lines := strings.SplitAfter(string(body), "\n")
			for i, want := range tc.wantLog {
				if i >= len(lines) || !strings.HasSuffix(lines[i], " "+want) {
					t.Errorf("log line %d of %q, want one ending %q", i, body, want)
				}
			}
			if buf.String() != tc.wantStderr {
				t.Errorf("stderr = %q, want %q", buf.String(), tc.wantStderr)
			}
		})
	}
}

// TestTimestampedStampsLines asserts each line of the log is stamped once,
// however the writes split it.
func TestTimestampedStampsLines(t *testing.T) {
	cases := []struct {
		name   string
		writes []string
		lines  []string // each written line, without its stamp
	}{
		{"one line per write", []string{"a\n", "b\n"}, []string{"a\n", "b\n"}},
		{"a line in two writes", []string{"a", "b\n"}, []string{"ab\n"}},
		{"two lines in one write", []string{"a\nb\n"}, []string{"a\n", "b\n"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			w := &timestamped{w: &buf}
			for _, s := range tc.writes {
				if _, err := io.WriteString(w, s); err != nil {
					t.Fatal(err)
				}
			}
			got := strings.SplitAfter(buf.String(), "\n")
			got = got[:len(got)-1] // the empty string after the last newline
			if len(got) != len(tc.lines) {
				t.Fatalf("log %q has %d lines, want %d", buf.String(), len(got), len(tc.lines))
			}
			const stamp = len("2006/01/02 15:04:05 ")
			for i, want := range tc.lines {
				if len(got[i]) < stamp || got[i][stamp:] != want {
					t.Errorf("line %d = %q, want a stamp and %q", i, got[i], want)
				}
			}
		})
	}
}
