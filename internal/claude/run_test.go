package claude

import (
	"context"
	"fmt"
	"io/fs"
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

// fileScript is the start command's script that writes the files named, in
// order, into the worker's directory, followed by its exec line.
func fileScript(exec string, files ...string) string {
	lines := []string{`d="${HERMOD_WORKER_DIR:?}"`, "umask 077"}
	for i, name := range files {
		v := fmt.Sprintf("HERMOD_CLAUDE_FILE_%d", i+1)
		lines = append(lines, `printf '%s' "$`+v+`" >"$d/`+name+`" || exit`, "unset "+v)
	}
	return strings.Join(append(lines, exec), "\n")
}

func TestStart(t *testing.T) {
	cases := []struct {
		name    string
		argv    []string
		dir     string
		local   map[string]string // the caller's files, by path
		noRead  bool              // the agent has no reader of the caller's files
		hook    func(agent.Hook) (agent.Hook, bool)
		fails   error // the executor's result
		want    []string
		wantEnv []string
		wantErr error // errAny for any error
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
			name:    "a settings file carried as a variable, its hooks rewritten by the function",
			argv:    []string{"claude", "--session-id", uuid, "--settings", "/tmp/air/settings.json", "--model", "opus"},
			local:   map[string]string{"/tmp/air/settings.json": airHook},
			hook:    aim,
			want:    []string{"sh", "-c", fileScript(`exec claude "${1}" "${2}" "${3}" "$d/settings.json" "${4}" "${5}"`, "settings.json"), "claude", "--session-id", uuid, "--settings", "--model", "opus"},
			wantEnv: []string{"HERMOD_CLAUDE_FILE_1=" + aimed},
		},
		{
			name:    "a relative settings file read under the caller's directory, kept with no function",
			argv:    []string{"claude", "--session-id", uuid, "--settings", "s.json"},
			local:   map[string]string{localDir + "/s.json": airHook},
			want:    []string{"sh", "-c", fileScript(`exec claude "${1}" "${2}" "${3}" "$d/settings.json"`, "settings.json"), "claude", "--session-id", uuid, "--settings"},
			wantEnv: []string{"HERMOD_CLAUDE_FILE_1=" + airHook},
		},
		{
			name:    "inline settings, paths mapped",
			argv:    []string{"claude", "--resume", uuid, `--settings={"cwd":"` + localDir + `"}`},
			hook:    aim,
			want:    []string{"sh", "-c", fileScript(`exec claude "${1}" "${2}" "${3}" "$d/settings.json"`, "settings.json"), "claude", "--resume", uuid, "--settings"},
			wantEnv: []string{`HERMOD_CLAUDE_FILE_1={"cwd":"` + remoteDir + `"}`},
		},
		{
			name:  "settings and every MCP config carried, paths mapped",
			argv:  []string{"claude", "--session-id", uuid, "--mcp-config", "cfg/a.json", `{"mcpServers":{"b":{"command":"` + localDir + `/b","env":{"TOKEN":"` + credential + `"}}}}`, "--strict-mcp-config", "--settings", `{"model":"opus"}`},
			local: map[string]string{localDir + "/cfg/a.json": `{"mcpServers":{"ide":{"command":"/opt/air/bin/ide","headers":{"Authorization":"` + credential + `"}}}}`},
			want: []string{"sh", "-c", fileScript(`exec claude "${1}" "${2}" "${3}" "$d/mcp-1.json" "$d/mcp-2.json" "${4}" "${5}" "$d/settings.json"`, "settings.json", "mcp-1.json", "mcp-2.json"),
				"claude", "--session-id", uuid, "--mcp-config", "--strict-mcp-config", "--settings"},
			wantEnv: []string{
				`HERMOD_CLAUDE_FILE_1={"model":"opus"}`,
				`HERMOD_CLAUDE_FILE_2={"mcpServers":{"ide":{"command":"/opt/air/bin/ide","headers":{"Authorization":"` + credential + `"}}}}`,
				`HERMOD_CLAUDE_FILE_3={"mcpServers":{"b":{"command":"` + remoteDir + `/b","env":{"TOKEN":"` + credential + `"}}}}`,
			},
		},
		{
			name:    "MCP config in the inline form takes one value, the prompt after it passed on",
			argv:    []string{"claude", "--session-id", uuid, `--mcp-config={"mcpServers":{}}`, "fix the login bug"},
			want:    []string{"sh", "-c", fileScript(`exec claude "${1}" "${2}" "${3}" "$d/mcp-1.json" "${4}"`, "mcp-1.json"), "claude", "--session-id", uuid, "--mcp-config", "fix the login bug"},
			wantEnv: []string{`HERMOD_CLAUDE_FILE_1={"mcpServers":{}}`},
		},
		{
			name: "options with no value passed as given",
			argv: []string{"claude", "--session-id", uuid, "--mcp-config", "--settings"},
			want: []string{"claude", "--session-id", uuid, "--mcp-config", "--settings"},
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
			err := New(cfg).Start(context.Background(), ex, agent.Launch{Args: tc.argv, Dir: dir, Paths: paths(t), Hook: tc.hook})
			if !matches(err, tc.wantErr) {
				t.Fatalf("Start() error = %v, want %v", err, tc.wantErr)
			}
			if err != nil && strings.Contains(err.Error(), credential) {
				t.Errorf("Start() error shows a credential: %v", err)
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
			if got := cmd.Env.Entries(env.Value.Reveal); !reflect.DeepEqual(got, orEmpty(tc.wantEnv)) {
				t.Errorf("variables =\n %q\nwant\n %q", got, tc.wantEnv)
			}
			if strings.Contains(cmd.Env.String(), credential) {
				t.Errorf("the variables print a credential: %v", cmd.Env)
			}
		})
	}
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func TestAdapter(t *testing.T) {
	cases := []struct {
		name    string
		dir     string
		fails   error
		want    []string
		wantDir string
		wantErr error
	}{
		{"in the workspace's root, mapped", localDir, nil, []string{"sh", "-c", adapterScript, "claude-agent-acp"}, remoteDir, nil},
		{"the start fails", localDir, errFailed, []string{"sh", "-c", adapterScript, "claude-agent-acp"}, remoteDir, errFailed},
		{"a relative directory", "dev/api", nil, nil, "", errAny},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ex := &fakeExec{err: tc.fails}
			err := New(Config{}).Adapter(context.Background(), ex, agent.Launch{Dir: tc.dir, Paths: paths(t)})
			if !matches(err, tc.wantErr) {
				t.Fatalf("Adapter() error = %v, want %v", err, tc.wantErr)
			}
			var want [][]string
			if tc.want != nil {
				want = [][]string{tc.want}
			}
			if !reflect.DeepEqual(ex.args(), want) {
				t.Fatalf("executed %q, want %q", ex.args(), want)
			}
			if len(ex.got) == 1 && ex.got[0].Dir != tc.wantDir {
				t.Errorf("directory %q, want %q", ex.got[0].Dir, tc.wantDir)
			}
		})
	}
}

// TestDeclaredVars checks that both of Claude's starts take the same
// variables from the caller's, and only those.
func TestDeclaredVars(t *testing.T) {
	cases := []struct {
		name    string
		environ []string
		want    []string
	}{
		{
			"Claude's variables kept, local paths left out",
			[]string{"CLAUDE_CODE_ENTRYPOINT=sdk-ts", "CLAUDE_CODE_EXECUTABLE=/home/me/.local/share/hermod/bin/claude", "CLAUDE_CONFIG_DIR=/home/me/.claude", "PATH=/usr/bin"},
			[]string{"CLAUDE_CODE_ENTRYPOINT=sdk-ts"},
		},
		{
			"Anthropic and thinking tokens kept in order",
			[]string{"MAX_THINKING_TOKENS=1024", "HOME=/home/me", "ANTHROPIC_API_KEY=" + credential, "CLAUDE_AGENT_SDK_VERSION=0.1"},
			[]string{"MAX_THINKING_TOKENS=1024", "ANTHROPIC_API_KEY=" + credential, "CLAUDE_AGENT_SDK_VERSION=0.1"},
		},
		{
			"what a parent Claude sets for its children left out",
			[]string{"CLAUDE_CODE_EXECPATH=/opt/claude", "CLAUDE_PID=7", "CLAUDE_CODE_SESSION_ID=s", "CLAUDE_CODE_BRIDGE_SESSION_ID=b", "CLAUDE_CODE_CHILD_SESSION=1", "CLAUDE_CODE_SESSION_ATTENDED=1", "CLAUDE_CODE_MESSAGING_SOCKET=/tmp/s", "CLAUDE_CODE_MESSAGING_TOKEN=t", "CLAUDE_CODE_SSE_PORT=1", "CLAUDE_PROJECT_DIR=/p", "CLAUDE_ENV_FILE=/e", "CLAUDE_CODE_ENTRYPOINT=cli"},
			[]string{"CLAUDE_CODE_ENTRYPOINT=cli"},
		},
		{"an undeclared credential and look-alikes left out", []string{"GITHUB_TOKEN=" + credential, "CLAUDE=1", "MY_CLAUDE_X=1", "MAX_THINKING_TOKENS_X=1"}, []string{}},
		{"empty", nil, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			launch := agent.Launch{Args: []string{"claude", "--session-id", uuid}, Dir: localDir, Paths: paths(t), Environ: env.Environ(tc.environ)}
			ex := &fakeExec{}
			a := New(Config{})
			if err := a.Start(context.Background(), ex, launch); err != nil {
				t.Fatalf("Start() error: %v", err)
			}
			if err := a.Adapter(context.Background(), ex, launch); err != nil {
				t.Fatalf("Adapter() error: %v", err)
			}
			for i, name := range []string{"Start", "Adapter"} {
				if got := ex.got[i].Env.Entries(env.Value.Reveal); !reflect.DeepEqual(got, tc.want) {
					t.Errorf("%s: variables %q, want %q", name, got, tc.want)
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
