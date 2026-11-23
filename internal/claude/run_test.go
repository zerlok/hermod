package claude

import (
	"errors"
	"fmt"
	"io/fs"
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
	otherHook      = `{"hooks":{"Stop":[{"hooks":[{"type":"http","url":"http://u:` + credential + `@localhost:9000/mine?tok=` + credential + `"},{"type":"command","command":"x"}]}]}}`
	airDefaultPort = `{"hooks":{"Stop":[{"hooks":[{"type":"http","url":"http://localhost/air/claude/hook"}]}]}}`
	airHTTPS       = `{"hooks":{"Stop":[{"hooks":[{"type":"http","url":"https://127.0.0.1:63342/air/claude/hook"}]}]}}`
)

const airHook = `{"hooks":{"Stop":[{"hooks":[{"type":"http","url":"http://localhost:63342/air/claude/hook?x=1","headers":{"Authorization":"Bearer air-` + credential + `"}}]}]}}`

func paths(t *testing.T) pathmap.Map {
	t.Helper()
	m, err := pathmap.New(pathmap.Place{Local: "/home/me/dev/api", Remote: "/home/u/dev/api"})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// reader reads the local files of a table.
func reader(files map[string]string) func(string) ([]byte, error) {
	return func(name string) ([]byte, error) {
		content, ok := files[name]
		if !ok {
			return nil, fmt.Errorf("open %s: %w", name, fs.ErrNotExist)
		}
		return []byte(content), nil
	}
}

var config = Config{Dir: "/home/me/dev/api", ConfigDir: "/home/me/.claude", RemoteConfigDir: "/home/u/.claude"}

var transcriptPlace = pathmap.Place{Local: "/home/me/.claude/projects/-home-me-dev-api", Remote: "/home/u/.claude/projects/-home-u-dev-api"}

type settings struct{ file, doc string }

type hooks struct {
	address string
	routes  []string
	token   string
}

func TestRun(t *testing.T) {
	cases := []struct {
		name         string
		argv         []string
		local        map[string]string // the local files, by path
		config       *Config           // config reading local, when nil
		want         []string
		wantFiles    map[string]string
		wantPlaces   []pathmap.Place
		wantSettings *settings
		wantHooks    *hooks
		wantDropped  []string
		wantErr      error // errAny for any error
	}{
		{
			name:       "paths mapped, IDs unchanged, no name added",
			argv:       []string{"claude", "--resume", uuid, "--add-dir", "/home/me/dev/api/lib", "--append-system-prompt=/home/me/dev/api/x", "--", "/home/me/dev/api/README"},
			want:       []string{"claude", "--resume", uuid, "--add-dir", "/home/u/dev/api/lib", "--append-system-prompt=/home/u/dev/api/x", "--", "/home/u/dev/api/README"},
			wantPlaces: []pathmap.Place{transcriptPlace},
		},
		{
			name:       "a path under no root left as it is",
			argv:       []string{"claude", "--session-id", uuid, "--add-dir", "/srv/data"},
			want:       []string{"claude", "--session-id", uuid, "--add-dir", "/srv/data"},
			wantPlaces: []pathmap.Place{transcriptPlace},
		},
		{
			name:         "settings file moved to the worker's directory, its hooks declared",
			argv:         []string{"claude", "--session-id", uuid, "--settings", "/tmp/air/settings.json", "--model", "opus"},
			local:        map[string]string{"/tmp/air/settings.json": airHook},
			want:         []string{"sh", "-c", `exec claude "${1}" "${2}" "${3}" "${HERMOD_WORKER_DIR:?}/settings.json" "${4}" "${5}"`, "claude", "--session-id", uuid, "--settings", "--model", "opus"},
			wantPlaces:   []pathmap.Place{transcriptPlace},
			wantSettings: &settings{file: "settings.json", doc: airHook},
			wantHooks:    &hooks{address: "localhost:63342", routes: []string{"/air/claude/hook?x=1"}, token: "Bearer air-" + credential},
		},
		{
			name:         "inline settings without hooks, paths mapped",
			argv:         []string{"claude", "--resume", uuid, `--settings={"cwd":"/home/me/dev/api"}`},
			want:         []string{"sh", "-c", `exec claude "${1}" "${2}" "${3}" "${HERMOD_WORKER_DIR:?}/settings.json"`, "claude", "--resume", uuid, "--settings"},
			wantPlaces:   []pathmap.Place{transcriptPlace},
			wantSettings: &settings{file: "settings.json", doc: `{"cwd":"/home/u/dev/api"}`},
		},
		{
			name:         "settings with only another local endpoint: its hook dropped, no callback",
			argv:         []string{"claude", "--session-id", uuid, "--settings", otherHook},
			want:         []string{"sh", "-c", `exec claude "${1}" "${2}" "${3}" "${HERMOD_WORKER_DIR:?}/settings.json"`, "claude", "--session-id", uuid, "--settings"},
			wantPlaces:   []pathmap.Place{transcriptPlace},
			wantSettings: &settings{file: "settings.json", doc: `{"hooks":{"Stop":[{"hooks":[{"command":"x","type":"command"}]}]}}`},
			wantDropped:  []string{"http://localhost:9000/mine"},
		},
		{
			name:         "Air's hook on its scheme's default port called back on that port",
			argv:         []string{"claude", "--session-id", uuid, "--settings", airDefaultPort},
			want:         []string{"sh", "-c", `exec claude "${1}" "${2}" "${3}" "${HERMOD_WORKER_DIR:?}/settings.json"`, "claude", "--session-id", uuid, "--settings"},
			wantPlaces:   []pathmap.Place{transcriptPlace},
			wantSettings: &settings{file: "settings.json", doc: airDefaultPort},
			wantHooks:    &hooks{address: "localhost:80", routes: []string{"/air/claude/hook"}},
		},
		{
			name:         "an https loopback hook dropped, no callback",
			argv:         []string{"claude", "--session-id", uuid, "--settings", airHTTPS},
			want:         []string{"sh", "-c", `exec claude "${1}" "${2}" "${3}" "${HERMOD_WORKER_DIR:?}/settings.json"`, "claude", "--session-id", uuid, "--settings"},
			wantPlaces:   []pathmap.Place{transcriptPlace},
			wantSettings: &settings{file: "settings.json", doc: `{"hooks":{}}`},
			wantDropped:  []string{"https://127.0.0.1:63342/air/claude/hook"},
		},
		{
			name:  "every MCP config written as given, paths mapped",
			argv:  []string{"claude", "--session-id", uuid, "--mcp-config", "cfg/a.json", `{"mcpServers":{"b":{"command":"/home/me/dev/api/b","env":{"TOKEN":"` + credential + `"}}}}`, "--strict-mcp-config"},
			local: map[string]string{"/home/me/dev/api/cfg/a.json": `{"mcpServers":{"ide":{"command":"/opt/air/bin/ide","headers":{"Authorization":"` + credential + `"}}}}`},
			want: []string{"sh", "-c", `exec claude "${1}" "${2}" "${3}" "${HERMOD_WORKER_DIR:?}/mcp-1.json" "${HERMOD_WORKER_DIR:?}/mcp-2.json" "${4}"`,
				"claude", "--session-id", uuid, "--mcp-config", "--strict-mcp-config"},
			wantFiles: map[string]string{
				"mcp-1.json": `{"mcpServers":{"ide":{"command":"/opt/air/bin/ide","headers":{"Authorization":"` + credential + `"}}}}`,
				"mcp-2.json": `{"mcpServers":{"b":{"command":"/home/u/dev/api/b","env":{"TOKEN":"` + credential + `"}}}}`,
			},
			wantPlaces: []pathmap.Place{transcriptPlace},
		},
		{
			name:       "MCP config in the inline form",
			argv:       []string{"claude", "--session-id", uuid, `--mcp-config={"mcpServers":{}}`},
			want:       []string{"sh", "-c", `exec claude "${1}" "${2}" "${3}" "${HERMOD_WORKER_DIR:?}/mcp-1.json"`, "claude", "--session-id", uuid, "--mcp-config"},
			wantFiles:  map[string]string{"mcp-1.json": `{"mcpServers":{}}`},
			wantPlaces: []pathmap.Place{transcriptPlace},
		},
		{
			name:       "MCP config in the inline form takes one value, the prompt after it passed on",
			argv:       []string{"claude", "--session-id", uuid, `--mcp-config={"mcpServers":{}}`, "fix the login bug"},
			want:       []string{"sh", "-c", `exec claude "${1}" "${2}" "${3}" "${HERMOD_WORKER_DIR:?}/mcp-1.json" "${4}"`, "claude", "--session-id", uuid, "--mcp-config", "fix the login bug"},
			wantFiles:  map[string]string{"mcp-1.json": `{"mcpServers":{}}`},
			wantPlaces: []pathmap.Place{transcriptPlace},
		},
		{
			name:       "options with no value passed as given",
			argv:       []string{"claude", "--session-id", uuid, "--mcp-config", "--settings"},
			want:       []string{"claude", "--session-id", uuid, "--mcp-config", "--settings"},
			wantPlaces: []pathmap.Place{transcriptPlace},
		},
		{
			name: "one-shot call mapped, with no file and no place",
			argv: []string{"claude", "-p", "--add-dir", "/home/me/dev/api/lib", "hello"},
			want: []string{"claude", "-p", "--add-dir", "/home/u/dev/api/lib", "hello"},
		},
		{
			name:    "a command Claude does not run",
			argv:    []string{"claude", "--model", "opus"},
			wantErr: errAny,
		},
		{
			name:    "a missing settings file",
			argv:    []string{"claude", "--session-id", uuid, "--settings", "/tmp/gone.json"},
			wantErr: fs.ErrNotExist,
		},
		{
			name:    "a malformed inline document",
			argv:    []string{"claude", "--session-id", uuid, `--mcp-config={"` + credential + `":`},
			wantErr: errAny,
		},
		{
			name:    "settings that are not an object",
			argv:    []string{"claude", "--session-id", uuid, "--settings", `{"hooks":{"Stop":[{"hooks":[{"type":"http","url":"http://localhost:1/h","headers":{"Authorization":1}}]}]}}`},
			wantErr: errAny,
		},
		{
			name:    "no reader of local files",
			argv:    []string{"claude", "--session-id", uuid, "--settings", "/tmp/air/settings.json"},
			config:  &Config{Dir: config.Dir, ConfigDir: config.ConfigDir, RemoteConfigDir: config.RemoteConfigDir},
			wantErr: errAny,
		},
		{
			name:    "a relative config directory",
			argv:    []string{"claude", "--session-id", uuid},
			config:  &Config{Dir: config.Dir, ConfigDir: ".claude", RemoteConfigDir: config.RemoteConfigDir},
			wantErr: errAny,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config
			cfg.ReadFile = reader(tc.local)
			if tc.config != nil {
				cfg = *tc.config
			}
			a := New(cfg)
			needs, err := a.Run(a.Parse(tc.argv), paths(t))
			if tc.wantErr != nil {
				if err == nil || (tc.wantErr != errAny && !errors.Is(err, tc.wantErr)) {
					t.Fatalf("Run() error = %v, want %v", err, tc.wantErr)
				}
				if strings.Contains(err.Error(), credential) {
					t.Errorf("Run() error shows a credential: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Run() error: %v", err)
			}
			if got := needs.Command(); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Command() =\n %q\nwant\n %q", got, tc.want)
			}
			if strings.Contains(strings.Join(needs.Command(), " "), credential) {
				t.Errorf("Command() shows a credential: %q", needs.Command())
			}
			if got := reveal(needs.Files()); !reflect.DeepEqual(got, tc.wantFiles) {
				t.Errorf("Files() = %q, want %q", got, tc.wantFiles)
			}
			if got := needs.Places(); !reflect.DeepEqual(got, tc.wantPlaces) {
				t.Errorf("Places() = %v, want %v", got, tc.wantPlaces)
			}
			var gotSettings *settings
			if file, doc, ok := needs.Settings(); ok {
				gotSettings = &settings{file: file, doc: doc.Reveal()}
			}
			if !reflect.DeepEqual(gotSettings, tc.wantSettings) {
				t.Errorf("Settings() = %+v, want %+v", gotSettings, tc.wantSettings)
			}
			address, routes, token, hooked := needs.Hooks()
			var got *hooks
			if hooked {
				got = &hooks{address: address, routes: routes, token: token.Reveal()}
			}
			if !reflect.DeepEqual(got, tc.wantHooks) {
				t.Errorf("Hooks() = %+v, want %+v", got, tc.wantHooks)
			}
			if got := needs.DroppedHooks(); !reflect.DeepEqual(got, tc.wantDropped) {
				t.Errorf("DroppedHooks() = %q, want %q", got, tc.wantDropped)
			}
			if strings.Contains(strings.Join(needs.DroppedHooks(), " "), credential) {
				t.Errorf("DroppedHooks() shows a credential: %q", needs.DroppedHooks())
			}
		})
	}
}

var errAny = errors.New("any error")

func reveal(files map[string]secret.Token) map[string]string {
	if len(files) == 0 {
		return nil
	}
	out := map[string]string{}
	for name, content := range files {
		out[name] = content.Reveal()
	}
	return out
}

func TestAdapter(t *testing.T) {
	cases := []struct {
		name         string
		cfg          Config
		want         []string
		wantFiles    map[string]string
		wantPlaces   []pathmap.Place
		wantSettings *settings
	}{
		{name: "for a configured agent", cfg: config, want: []string{"sh", "-c", adapterScript, "claude-agent-acp"}},
		{name: "for an agent with nothing configured", cfg: Config{}, want: []string{"sh", "-c", adapterScript, "claude-agent-acp"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			needs, err := New(tc.cfg).Adapter(paths(t))
			if err != nil {
				t.Fatalf("Adapter() error: %v", err)
			}
			if got := needs.Command(); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Command() = %q, want %q", got, tc.want)
			}
			if got := reveal(needs.Files()); !reflect.DeepEqual(got, tc.wantFiles) {
				t.Errorf("Files() = %q, want %q", got, tc.wantFiles)
			}
			if got := needs.Places(); !reflect.DeepEqual(got, tc.wantPlaces) {
				t.Errorf("Places() = %v, want %v", got, tc.wantPlaces)
			}
			var gotSettings *settings
			if file, doc, ok := needs.Settings(); ok {
				gotSettings = &settings{file: file, doc: doc.Reveal()}
			}
			if !reflect.DeepEqual(gotSettings, tc.wantSettings) {
				t.Errorf("Settings() = %+v, want %+v", gotSettings, tc.wantSettings)
			}
		})
	}
}

// TestDeclaredVars checks that both of Claude's commands declare the same
// variables, and only those.
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
			a := New(config)
			run, err := a.Run(a.Parse([]string{"claude", "--session-id", uuid}), paths(t))
			if err != nil {
				t.Fatalf("Run() error: %v", err)
			}
			adapter, err := a.Adapter(paths(t))
			if err != nil {
				t.Fatalf("Adapter() error: %v", err)
			}
			for name, needs := range map[string]agent.Needs{"Run": run, "Adapter": adapter} {
				got := needs.Vars(env.Environ(tc.environ)).Entries(env.Value.Reveal)
				if !reflect.DeepEqual(got, tc.want) {
					t.Errorf("%s: Vars() = %q, want %q", name, got, tc.want)
				}
			}
		})
	}
}
