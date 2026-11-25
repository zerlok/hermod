package claude

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/zerlok/hermod/internal/agent"
	"github.com/zerlok/hermod/internal/env"
	"github.com/zerlok/hermod/internal/pathmap"
	"github.com/zerlok/hermod/internal/secret"
)

// credential marks every secret a case's documents hold, so a test can tell
// that none reached a command line or an error.
const credential = "SECRET"

const (
	localDir  = "/home/me/dev/api"
	remoteDir = "/home/u/dev/api"
)

const (
	airHook = `{"hooks":{"Stop":[{"hooks":[{"headers":{"Authorization":"Bearer air-` + credential + `"},"type":"http","url":"http://localhost:63342/air/claude/hook?x=1"}]}]}}`
	aimed   = `{"hooks":{"Stop":[{"hooks":[{"headers":{"Authorization":"Bearer worker"},"type":"http","url":"http://localhost:45001/air/claude/hook?x=1"}]}]}}`
)

func paths(t *testing.T) pathmap.Map {
	t.Helper()
	m, err := pathmap.New(pathmap.Place{Local: localDir, Remote: remoteDir})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// reader reads the caller's files of a table.
func reader(files map[string]string) func(string) ([]byte, error) {
	return func(name string) ([]byte, error) {
		content, ok := files[name]
		if !ok {
			return nil, fmt.Errorf("open %s: %w", name, fs.ErrNotExist)
		}
		return []byte(content), nil
	}
}

// aim is a hook function that points every hook at port 45001 with the
// worker's token.
func aim(h agent.Hook) (agent.Hook, bool) {
	_, rest, _ := strings.Cut(strings.TrimPrefix(h.URL, "http://"), "/")
	return agent.Hook{
		URL:     "http://localhost:45001/" + rest,
		Headers: map[string]secret.Token{"Authorization": secret.FromString("Bearer worker")},
	}, true
}

// workerDir is where the tests' Place puts a file.
const workerDir = "/home/u/.hermod/s/w"

// placer is a Launch's Place that records each file it was given, as
// `name=content`.
type placer struct{ got []string }

func (p *placer) place(name string, content secret.Token) string {
	p.got = append(p.got, name+"="+content.Reveal())
	return workerDir + "/" + name
}

func TestStart(t *testing.T) {
	cases := []struct {
		name      string
		argv      []string
		dir       string
		local     map[string]string // the caller's files, by path
		noRead    bool              // the agent has no reader of the caller's files
		noPlace   bool              // the Launch has no Place
		hook      func(agent.Hook) (agent.Hook, bool)
		fails     error // the executor's result
		want      []string
		wantFiles []string // the files placed, as name=content, in order
		wantErr   error    // errAny for any error
	}{
		{
			name: "paths mapped, IDs unchanged, no name added",
			argv: []string{"claude", "--resume", uuid, "--add-dir", localDir + "/lib", "--append-system-prompt=" + localDir + "/x", "--", localDir + "/README"},
			want: []string{"claude", "--resume", uuid, "--add-dir", remoteDir + "/lib", "--append-system-prompt=" + remoteDir + "/x", "--", remoteDir + "/README"},
		},
		{
			name: "a path under no root left as it is",
			argv: []string{"claude", "--session-id", uuid, "--add-dir", "/srv/data"},
			want: []string{"claude", "--session-id", uuid, "--add-dir", "/srv/data"},
		},
		{
			name:      "a settings file placed, its hooks rewritten by the function",
			argv:      []string{"claude", "--session-id", uuid, "--settings", "/tmp/air/settings.json", "--model", "opus"},
			local:     map[string]string{"/tmp/air/settings.json": airHook},
			hook:      aim,
			want:      []string{"claude", "--session-id", uuid, "--settings", workerDir + "/settings.json", "--model", "opus"},
			wantFiles: []string{"settings.json=" + aimed},
		},
		{
			name:      "a relative settings file read under the caller's directory, kept with no function",
			argv:      []string{"claude", "--session-id", uuid, "--settings", "s.json"},
			local:     map[string]string{localDir + "/s.json": airHook},
			want:      []string{"claude", "--session-id", uuid, "--settings", workerDir + "/settings.json"},
			wantFiles: []string{"settings.json=" + airHook},
		},
		{
			name:      "inline settings, paths mapped",
			argv:      []string{"claude", "--resume", uuid, `--settings={"cwd":"` + localDir + `"}`},
			hook:      aim,
			want:      []string{"claude", "--resume", uuid, "--settings", workerDir + "/settings.json"},
			wantFiles: []string{`settings.json={"cwd":"` + remoteDir + `"}`},
		},
		{
			name:  "settings and every MCP config placed, paths mapped",
			argv:  []string{"claude", "--session-id", uuid, "--mcp-config", "cfg/a.json", `{"mcpServers":{"b":{"command":"` + localDir + `/b","env":{"TOKEN":"` + credential + `"}}}}`, "--strict-mcp-config", "--settings", `{"model":"opus"}`},
			local: map[string]string{localDir + "/cfg/a.json": `{"mcpServers":{"ide":{"command":"/opt/air/bin/ide","headers":{"Authorization":"` + credential + `"}}}}`},
			want:  []string{"claude", "--session-id", uuid, "--mcp-config", workerDir + "/mcp-1.json", workerDir + "/mcp-2.json", "--strict-mcp-config", "--settings", workerDir + "/settings.json"},
			wantFiles: []string{
				`settings.json={"model":"opus"}`,
				`mcp-1.json={"mcpServers":{"ide":{"command":"/opt/air/bin/ide","headers":{"Authorization":"` + credential + `"}}}}`,
				`mcp-2.json={"mcpServers":{"b":{"command":"` + remoteDir + `/b","env":{"TOKEN":"` + credential + `"}}}}`,
			},
		},
		{
			name:      "MCP config in the inline form takes one value, the prompt after it passed on",
			argv:      []string{"claude", "--session-id", uuid, `--mcp-config={"mcpServers":{}}`, "fix the login bug"},
			want:      []string{"claude", "--session-id", uuid, "--mcp-config", workerDir + "/mcp-1.json", "fix the login bug"},
			wantFiles: []string{`mcp-1.json={"mcpServers":{}}`},
		},
		{
			name: "options with no value passed as given",
			argv: []string{"claude", "--session-id", uuid, "--mcp-config", "--settings"},
			want: []string{"claude", "--session-id", uuid, "--mcp-config", "--settings"},
		},
		{
			name:    "files and no place for them",
			argv:    []string{"claude", "--session-id", uuid, "--settings", `{"model":"opus"}`},
			noPlace: true,
			wantErr: errAny,
		},
		{
			name:    "a one-shot call is no conversation",
			argv:    []string{"claude", "-p", "hello"},
			wantErr: errAny,
		},
		{
			name:    "a command Claude does not run",
			argv:    []string{"claude", "--model", "opus"},
			wantErr: errAny,
		},
		{
			name:    "a relative directory",
			argv:    []string{"claude", "--session-id", uuid},
			dir:     "dev/api",
			wantErr: errAny,
		},
		{
			name:    "a missing settings file",
			argv:    []string{"claude", "--session-id", uuid, "--settings", "/tmp/gone.json"},
			wantErr: fs.ErrNotExist,
		},
		{
			name:    "no reader of the caller's files",
			argv:    []string{"claude", "--session-id", uuid, "--settings", "/tmp/air/settings.json"},
			noRead:  true,
			wantErr: errAny,
		},
		{
			name: "a JSON prompt with no path kept as written, one with a path mapped",
			argv: []string{"claude", "--session-id", uuid, "--", "[1, 2]", `{"b": 1, "a": 2}`, `{"b": "` + localDir + `/x", "a": 1}`},
			want: []string{"claude", "--session-id", uuid, "--", "[1, 2]", `{"b": 1, "a": 2}`, `{"a":1,"b":"` + remoteDir + `/x"}`},
		},
		{
			name:    "a settings document with data after it",
			argv:    []string{"claude", "--session-id", uuid, "--settings", `{"hooks":{}} {}`},
			wantErr: errAny,
		},
		{
			name:    "an MCP config with data after it",
			argv:    []string{"claude", "--session-id", uuid, `--mcp-config={"a":1} x`},
			wantErr: errAny,
		},
		{
			name:    "a malformed inline document that fails at its credential",
			argv:    []string{"claude", "--session-id", uuid, `--mcp-config={"a":` + credential + `}`},
			wantErr: errNotJSON,
		},
		{
			name:    "a malformed inline document",
			argv:    []string{"claude", "--session-id", uuid, `--mcp-config={"` + credential + `":`},
			wantErr: errAny,
		},
		{
			name:    "settings whose hook has a header that is not a string",
			argv:    []string{"claude", "--session-id", uuid, "--settings", `{"hooks":{"Stop":[{"hooks":[{"type":"http","url":"http://localhost:1/h","headers":{"Authorization":1}}]}]}}`},
			hook:    aim,
			wantErr: errAny,
		},
		{
			name:    "the start fails",
			argv:    []string{"claude", "--session-id", uuid},
			fails:   errFailed,
			want:    []string{"claude", "--session-id", uuid},
			wantErr: errFailed,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{ReadFile: reader(tc.local)}
			if tc.noRead {
				cfg.ReadFile = nil
			}
			dir := tc.dir
			if dir == "" {
				dir = localDir
			}
			ex := &fakeExec{err: tc.fails}
			p := &placer{}
			launch := agent.Launch{Args: tc.argv, Dir: dir, Paths: paths(t), Hook: tc.hook, Place: p.place}
			if tc.noPlace {
				launch.Place = nil
			}
			err := New(cfg).Start(context.Background(), ex, launch)
			if !matches(err, tc.wantErr) {
				t.Fatalf("Start() error = %v, want %v", err, tc.wantErr)
			}
			if err != nil && strings.Contains(err.Error(), credential) {
				t.Errorf("Start() error shows a credential: %v", err)
			}
			if !reflect.DeepEqual(p.got, tc.wantFiles) {
				t.Errorf("placed =\n %q\nwant\n %q", p.got, tc.wantFiles)
			}
			if tc.want == nil {
				if len(ex.got) != 0 {
					t.Errorf("executed %q, want nothing", ex.args())
				}
				return
			}
			if len(ex.got) != 1 {
				t.Fatalf("executed %q, want one command", ex.args())
			}
			cmd := ex.got[0]
			if !reflect.DeepEqual(cmd.Args, tc.want) {
				t.Errorf("command =\n %q\nwant\n %q", cmd.Args, tc.want)
			}
			if strings.Contains(strings.Join(cmd.Args, " "), credential) {
				t.Errorf("the command line shows a credential: %q", cmd.Args)
			}
			if cmd.Dir != remoteDir {
				t.Errorf("directory %q, want %q", cmd.Dir, remoteDir)
			}
		})
	}
}

func TestAdapter(t *testing.T) {
	resolve := []string{"sh", "-c", conversationsScript, "conversations", "executable"}
	cases := []struct {
		name         string
		dir          string
		environ      []string
		answer       string // what resolving Claude's executable prints
		resolveFails error
		startFails   error
		wantResolve  [][]string
		want         [][]string
		wantDir      string
		wantEnv      []string
		wantErr      error
	}{
		{
			name:        "in the workspace's root, mapped, with the Claude found there",
			dir:         localDir,
			environ:     []string{"CLAUDE_CODE_EXECUTABLE=/home/me/bin/claude", "ANTHROPIC_API_KEY=" + credential},
			answer:      "Welcome!\nconversations:\n/usr/local/bin/claude\n",
			wantResolve: [][]string{resolve},
			want:        [][]string{{"claude-agent-acp"}},
			wantDir:     remoteDir,
			wantEnv:     []string{"ANTHROPIC_API_KEY=" + credential, "CLAUDE_CODE_EXECUTABLE=/usr/local/bin/claude"},
		},
		{
			name:        "the start fails",
			dir:         localDir,
			answer:      "conversations:\n/usr/local/bin/claude\n",
			startFails:  errFailed,
			wantResolve: [][]string{resolve},
			want:        [][]string{{"claude-agent-acp"}},
			wantDir:     remoteDir,
			wantEnv:     []string{"CLAUDE_CODE_EXECUTABLE=/usr/local/bin/claude"},
			wantErr:     errFailed,
		},
		{
			name:        "no Claude found",
			dir:         localDir,
			answer:      "conversations:\n\n",
			wantResolve: [][]string{resolve},
			wantErr:     errAny,
		},
		{
			name:         "resolving fails",
			dir:          localDir,
			resolveFails: errFailed,
			wantResolve:  [][]string{resolve},
			wantErr:      errFailed,
		},
		{
			name:    "a relative directory",
			dir:     "dev/api",
			wantErr: errAny,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resolver := &fakeExec{out: tc.answer, err: tc.resolveFails}
			ex := &fakeExec{err: tc.startFails}
			launch := agent.Launch{Dir: tc.dir, Paths: paths(t), Environ: env.Environ(tc.environ)}
			err := New(Config{Exec: resolver}).Adapter(context.Background(), ex, launch)
			if !matches(err, tc.wantErr) {
				t.Fatalf("Adapter() error = %v, want %v", err, tc.wantErr)
			}
			if !reflect.DeepEqual(resolver.args(), tc.wantResolve) {
				t.Errorf("resolved with %q, want %q", resolver.args(), tc.wantResolve)
			}
			if !reflect.DeepEqual(ex.args(), tc.want) {
				t.Fatalf("executed %q, want %q", ex.args(), tc.want)
			}
			if len(ex.got) != 1 {
				return
			}
			if ex.got[0].Dir != tc.wantDir {
				t.Errorf("directory %q, want %q", ex.got[0].Dir, tc.wantDir)
			}
			if got := ex.got[0].Env.Entries(env.Value.Reveal); !reflect.DeepEqual(got, tc.wantEnv) {
				t.Errorf("variables %q, want %q", got, tc.wantEnv)
			}
		})
	}
}

// TestExecutableOperation runs the script's executable operation in a local
// sh, with Claude's variable set and unset, as a program started there finds
// Claude.
func TestExecutableOperation(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(bin+"/claude", nil, 0o755); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		environ []string
		want    string
	}{
		{"the environment's own", []string{"HOME=/home/u", "PATH=" + bin, envExecutable + "=/opt/claude"}, "conversations:\n/opt/claude\n"},
		{"found on PATH", []string{"HOME=/home/u", "PATH=" + bin}, "conversations:\n" + bin + "/claude\n"},
		{"none", []string{"HOME=/home/u", "PATH=" + t.TempDir()}, "conversations:\n\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command("/bin/sh", "-c", conversationsScript, "conversations", "executable")
			cmd.Env = tc.environ
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if string(out) != tc.want {
				t.Errorf("printed %q, want %q", out, tc.want)
			}
		})
	}
}

// TestDeclaredVars checks that both of Claude's starts take the same
// variables from the caller's, and only those, the adapter adding the Claude
// it runs.
func TestDeclaredVars(t *testing.T) {
	const executable = envExecutable + "=/usr/local/bin/claude"
	cases := []struct {
		name        string
		environ     []string
		want        []string
		wantAdapter []string
	}{
		{
			"Claude's variables kept, local paths left out",
			[]string{"CLAUDE_CODE_ENTRYPOINT=sdk-ts", "CLAUDE_CODE_EXECUTABLE=/home/me/.local/share/hermod/bin/claude", "CLAUDE_CONFIG_DIR=/home/me/.claude", "PATH=/usr/bin"},
			[]string{"CLAUDE_CODE_ENTRYPOINT=sdk-ts"},
			[]string{"CLAUDE_CODE_ENTRYPOINT=sdk-ts", executable},
		},
		{
			"Anthropic and thinking tokens kept in order",
			[]string{"MAX_THINKING_TOKENS=1024", "HOME=/home/me", "ANTHROPIC_API_KEY=" + credential, "CLAUDE_AGENT_SDK_VERSION=0.1"},
			[]string{"MAX_THINKING_TOKENS=1024", "ANTHROPIC_API_KEY=" + credential, "CLAUDE_AGENT_SDK_VERSION=0.1"},
			[]string{"MAX_THINKING_TOKENS=1024", "ANTHROPIC_API_KEY=" + credential, "CLAUDE_AGENT_SDK_VERSION=0.1", executable},
		},
		{
			"what a parent Claude sets for its children left out",
			[]string{"CLAUDE_CODE_EXECPATH=/opt/claude", "CLAUDE_PID=7", "CLAUDE_CODE_SESSION_ID=s", "CLAUDE_CODE_BRIDGE_SESSION_ID=b", "CLAUDE_CODE_CHILD_SESSION=1", "CLAUDE_CODE_SESSION_ATTENDED=1", "CLAUDE_CODE_MESSAGING_SOCKET=/tmp/s", "CLAUDE_CODE_MESSAGING_TOKEN=t", "CLAUDE_CODE_SSE_PORT=1", "CLAUDE_PROJECT_DIR=/p", "CLAUDE_ENV_FILE=/e", "CLAUDE_CODE_ENTRYPOINT=cli"},
			[]string{"CLAUDE_CODE_ENTRYPOINT=cli"},
			[]string{"CLAUDE_CODE_ENTRYPOINT=cli", executable},
		},
		{"an undeclared credential and look-alikes left out", []string{"GITHUB_TOKEN=" + credential, "CLAUDE=1", "MY_CLAUDE_X=1", "MAX_THINKING_TOKENS_X=1"}, []string{}, []string{executable}},
		{"empty", nil, []string{}, []string{executable}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			launch := agent.Launch{Args: []string{"claude", "--session-id", uuid}, Dir: localDir, Paths: paths(t), Environ: env.Environ(tc.environ)}
			ex := &fakeExec{}
			a := New(Config{Exec: &fakeExec{out: "conversations:\n/usr/local/bin/claude\n"}})
			if err := a.Start(context.Background(), ex, launch); err != nil {
				t.Fatalf("Start() error: %v", err)
			}
			if err := a.Adapter(context.Background(), ex, launch); err != nil {
				t.Fatalf("Adapter() error: %v", err)
			}
			for i, want := range [][]string{tc.want, tc.wantAdapter} {
				if got := ex.got[i].Env.Entries(env.Value.Reveal); !reflect.DeepEqual(got, want) {
					t.Errorf("start %d: variables %q, want %q", i, got, want)
				}
			}
		})
	}
}

func TestStateDir(t *testing.T) {
	config := []string{"sh", "-c", conversationsScript, "conversations", "config"}
	cases := []struct {
		name        string
		dir         string
		out         string
		fails       error
		wantCommand [][]string
		want        string
		wantErr     error
	}{
		{"under the config directory, named after the directory", localDir, "conversations:\n/home/me/.claude\n", nil, [][]string{config}, "/home/me/.claude/projects/-home-me-dev-api", nil},
		{"between a login profile's own lines", remoteDir + "/", "Welcome!\nconversations:\n/srv/claude\nBye!\n", nil, [][]string{config}, "/srv/claude/projects/-home-u-dev-api", nil},
		{"no marker", localDir, "/home/me/.claude\n", nil, [][]string{config}, "", errAny},
		{"a relative answer", localDir, "conversations:\nclaude\n", nil, [][]string{config}, "", errAny},
		{"the command fails", localDir, "", errFailed, [][]string{config}, "", errFailed},
		{"a relative directory", "dev/api", "", nil, nil, "", errAny},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ex := &fakeExec{out: tc.out, err: tc.fails}
			got, err := New(Config{Exec: ex}).StateDir(context.Background(), tc.dir)
			if !matches(err, tc.wantErr) {
				t.Fatalf("StateDir() error = %v, want %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("StateDir() = %q, want %q", got, tc.want)
			}
			if !reflect.DeepEqual(ex.args(), tc.wantCommand) {
				t.Errorf("executed %q, want %q", ex.args(), tc.wantCommand)
			}
		})
	}
}

// TestConfigOperation runs the script's config operation in a local sh, with
// Claude's variable set and unset, as Claude reads its config directory.
func TestConfigOperation(t *testing.T) {
	cases := []struct {
		name    string
		environ []string
		want    string
		wantErr bool
	}{
		{"unset", []string{"HOME=/home/u"}, "conversations:\n/home/u/.claude\n", false},
		{"set", []string{"HOME=/home/u", envConfigDir + "=/srv/claude"}, "conversations:\n/srv/claude\n", false},
		{"relative", []string{"HOME=/home/u", envConfigDir + "=claude"}, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command("sh", "-c", conversationsScript, "conversations", "config")
			cmd.Env = tc.environ
			out, err := cmd.Output()
			if (err != nil) != tc.wantErr {
				t.Fatalf("run: %v, want error %v", err, tc.wantErr)
			}
			if string(out) != tc.want {
				t.Errorf("printed %q, want %q", out, tc.want)
			}
		})
	}
}

func TestHooks(t *testing.T) {
	cases := []struct {
		name    string
		argv    []string
		local   map[string]string // the caller's files, by path
		want    []string          // each hook as its URL and its headers, `k=v`
		wantErr error             // errAny for any error
	}{
		{
			name:  "Air's hook in a settings file",
			argv:  []string{"claude", "--session-id", uuid, "--settings", "/tmp/air/settings.json"},
			local: map[string]string{"/tmp/air/settings.json": airHook},
			want:  []string{"http://localhost:63342/air/claude/hook?x=1 Authorization=Bearer air-" + credential},
		},
		{
			name: "another endpoint's hooks, in the events' order, other hook types left out",
			argv: []string{"claude", "--resume", uuid, `--settings={"hooks":{"Stop":[{"hooks":[{"type":"http","url":"http://localhost:9000/b"},{"type":"command","command":"x"}]}],"PostToolUse":[{"hooks":[{"type":"http","url":"https://hooks.example/a"}]}]}}`},
			want: []string{"https://hooks.example/a", "http://localhost:9000/b"},
		},
		{
			name: "settings with no hooks",
			argv: []string{"claude", "--session-id", uuid, "--settings", `{"model":"opus"}`},
		},
		{
			name: "no settings",
			argv: []string{"claude", "--session-id", uuid, "--model", "opus"},
		},
		{
			name:    "a one-shot call is no conversation",
			argv:    []string{"claude", "-p", "--settings", "/tmp/air/settings.json"},
			wantErr: errAny,
		},
		{
			name:    "a missing settings file",
			argv:    []string{"claude", "--session-id", uuid, "--settings", "/tmp/gone.json"},
			wantErr: fs.ErrNotExist,
		},
		{
			name:    "a hook header that is not a string",
			argv:    []string{"claude", "--session-id", uuid, "--settings", `{"hooks":{"Stop":[{"hooks":[{"type":"http","url":"http://localhost:1/h","headers":{"Authorization":1}}]}]}}`},
			wantErr: errAny,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hooks, err := New(Config{ReadFile: reader(tc.local)}).Hooks(agent.Launch{Args: tc.argv, Dir: localDir, Paths: paths(t)})
			if !matches(err, tc.wantErr) {
				t.Fatalf("Hooks() error = %v, want %v", err, tc.wantErr)
			}
			var got []string
			for _, h := range hooks {
				line := h.URL
				for k, v := range h.Headers {
					line += " " + k + "=" + v.Reveal()
				}
				got = append(got, line)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Hooks() = %q, want %q", got, tc.want)
			}
		})
	}
}
