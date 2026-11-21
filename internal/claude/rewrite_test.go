package claude

import (
	"reflect"
	"testing"

	"github.com/zerlok/hermod/internal/pathmap"
)

const (
	localRoot   = "/home/me/dev/api"
	sandboxRoot = "/home/u/dev/api"
	sessionDoc  = "/home/u/.hermod/s/" + uuid + "/settings.json"
	sessionMCP  = "/home/u/.hermod/s/" + uuid + "/mcp.json"
	airMCP      = `{"mcpServers":{"jetbrains_air_ide":{"type":"stdio","command":"/opt/goland/bin/goland","args":["stdioMcpServer"],"env":{"IJ_MCP_AUTH_TOKEN":"t0k"}}}}`
)

func toSandbox(t *testing.T) pathmap.Map {
	t.Helper()
	roots, err := pathmap.New(pathmap.Pair{Local: localRoot, Sandbox: sandboxRoot})
	if err != nil {
		t.Fatal(err)
	}
	return roots.ToSandbox()
}

func TestForSandbox(t *testing.T) {
	cases := []struct {
		name     string
		argv     []string
		settings string
		mcp      string
		resume   bool
		want     []string
	}{
		{
			"Air's new terminal chat",
			[]string{"/usr/bin/claude", "--session-id", uuid, "--settings", "/home/me/.cache/air/settings-1.json"},
			sessionDoc, "", false,
			[]string{"claude", "--session-id", uuid, "--settings", sessionDoc},
		},
		{
			"Air's reopened chat, inline settings form",
			[]string{"claude", "--resume", uuid, "--settings=/tmp/s.json"},
			sessionDoc, "", false,
			[]string{"claude", "--resume", uuid, "--settings", sessionDoc},
		},
		{
			"settings dropped without a sandbox file",
			[]string{"claude", "--session-id", uuid, "--settings", "/tmp/s.json", "--model", "opus"},
			"", "", false,
			[]string{"claude", "--session-id", uuid, "--model", "opus"},
		},
		{
			"local paths mapped, others kept",
			[]string{"claude", "--session-id", uuid, "--add-dir", localRoot + "/docs", "--add-dir=" + localRoot, "--add-dir", "/opt/elsewhere"},
			"", "", false,
			[]string{"claude", "--session-id", uuid, "--add-dir", sandboxRoot + "/docs", "--add-dir=" + sandboxRoot, "--add-dir", "/opt/elsewhere"},
		},
		{
			"JSON argument mapped inside",
			[]string{"claude", "--session-id", uuid, "--agents", `{"a":{"prompt":"read /home/me/dev/api/x","dir":"/home/me/dev/api/x"}}`},
			"", "", false,
			[]string{"claude", "--session-id", uuid, "--agents", `{"a":{"prompt":"read /home/me/dev/api/x","dir":"/home/u/dev/api/x"}}`},
		},
		{
			"an '=' in a JSON value is not an option's",
			[]string{"claude", "--session-id", uuid, "--agents", `{"r":{"prompt":"x=1","cwd":"/home/me/dev/api/sub"}}`},
			"", "", false,
			[]string{"claude", "--session-id", uuid, "--agents", `{"r":{"prompt":"x=1","cwd":"/home/u/dev/api/sub"}}`},
		},
		{
			"an '=' in a path is not an option's",
			[]string{"claude", "--session-id", uuid, "--add-dir", "/home/me/dev/api/a=b"},
			"", "", false,
			[]string{"claude", "--session-id", uuid, "--add-dir", "/home/u/dev/api/a=b"},
		},
		{
			"an '=' in a prompt is not an option's",
			[]string{"claude", "--session-id", uuid, "--", "set x=/home/me/dev/api/y"},
			"", "", false,
			[]string{"claude", "--session-id", uuid, "--", "set x=/home/me/dev/api/y"},
		},
		{
			"every MCP config named by the session's one file",
			[]string{"claude", "--session-id", uuid, "--mcp-config", localRoot + "/.mcp.json", airMCP, "--verbose", "--mcp-config=/tmp/b.json"},
			"", sessionMCP, false,
			[]string{"claude", "--session-id", uuid, "--mcp-config", sessionMCP, "--verbose"},
		},
		{
			"MCP configs dropped without a sandbox file",
			[]string{"claude", "--session-id", uuid, "--mcp-config", airMCP, "--verbose"},
			"", "", false,
			[]string{"claude", "--session-id", uuid, "--verbose"},
		},
		{
			"no name added, the caller's kept",
			[]string{"claude", "--session-id", uuid, "--name", "my chat"},
			"", "", false,
			[]string{"claude", "--session-id", uuid, "--name", "my chat"},
		},
		{
			"resume replaces a new session's ID",
			[]string{"claude", "--session-id=" + uuid, "--settings", "/tmp/s.json"},
			sessionDoc, "", true,
			[]string{"claude", "--resume", uuid, "--settings", sessionDoc},
		},
		{
			"resume of a resume is unchanged",
			[]string{"claude", "-r", uuid, "--model", "opus"},
			"", "", true,
			[]string{"claude", "--resume", uuid, "--model", "opus"},
		},
		{
			"resume of a fork resumes the fork",
			[]string{"claude", "--resume", fork, "--fork-session", "--session-id", uuid},
			"", "", true,
			[]string{"claude", "--resume", uuid},
		},
		{
			"words after -- are a prompt",
			[]string{"claude", "--session-id", uuid, "--", "--settings", localRoot + "/a.go"},
			sessionDoc, "", false,
			[]string{"claude", "--session-id", uuid, "--", "--settings", sandboxRoot + "/a.go"},
		},
		{
			"settings with no value",
			[]string{"claude", "--session-id", uuid, "--settings"},
			sessionDoc, "", false,
			[]string{"claude", "--session-id", uuid},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(tc.argv).ForSandbox(Rewrite{ToSandbox: toSandbox(t), Settings: tc.settings, MCPConfig: tc.mcp, Resume: tc.resume})
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ForSandbox() =\n %q\nwant\n %q", got, tc.want)
			}
		})
	}
}

func TestCallSettingsAndResumes(t *testing.T) {
	cases := []struct {
		name        string
		argv        []string
		wantValue   string
		wantOK      bool
		wantResumes bool
		wantResumed SessionID // "" for none
	}{
		{"file", []string{"claude", "--session-id", uuid, "--settings", "/tmp/s.json"}, "/tmp/s.json", true, false, ""},
		{"inline form", []string{"claude", "--resume", uuid, "--settings={}"}, "{}", true, true, uuid},
		{"none", []string{"claude", "-r", uuid}, "", false, true, uuid},
		{"after -- is a prompt", []string{"claude", "--session-id", uuid, "--", "--settings", "x"}, "", false, false, ""},
		{"no value", []string{"claude", "--session-id", uuid, "--settings"}, "", false, false, ""},
		{"fork starts its own from another", []string{"claude", "--resume", uuid, "--fork-session", "--session-id", fork}, "", false, false, uuid},
		{"fork with no session of its own", []string{"claude", "--resume", uuid, "--fork-session"}, "", false, false, ""},
		{"not a session", []string{"claude", "--resume"}, "", false, false, ""},
		{"empty argv", nil, "", false, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			call := Classify(tc.argv)
			value, ok := call.Settings()
			if value != tc.wantValue || ok != tc.wantOK {
				t.Errorf("Settings() = %q, %v, want %q, %v", value, ok, tc.wantValue, tc.wantOK)
			}
			if got := call.Resumes(); got != tc.wantResumes {
				t.Errorf("Resumes() = %v, want %v", got, tc.wantResumes)
			}
			if got, ok := call.Resumed(); got != tc.wantResumed || ok != (tc.wantResumed != "") {
				t.Errorf("Resumed() = %q, %v, want %q", got, ok, tc.wantResumed)
			}
		})
	}
}

func TestIsInlineJSON(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  bool
	}{
		{"object", `{"hooks":{}}`, true},
		{"object with spaces", ` {"a":1} `, true},
		{"file", "/tmp/settings.json", false},
		{"broken object", `{"a":`, false},
		{"array is no settings", `[1]`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsInlineJSON(tc.value); got != tc.want {
				t.Errorf("IsInlineJSON(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

func TestCallMCPConfigs(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want []string
	}{
		{"one file", []string{"claude", "--session-id", uuid, "--mcp-config", "a.json"}, []string{"a.json"}},
		{"several values, up to the next option", []string{"claude", "--mcp-config", "a.json", "b.json", "--session-id", uuid}, []string{"a.json", "b.json"}},
		{"inline form takes more values", []string{"claude", "--mcp-config=a.json", "b.json", "-p"}, []string{"a.json", "b.json"}},
		{"repeated", []string{"claude", "--mcp-config", "a.json", "--verbose", "--mcp-config", `{"mcpServers":{}}`}, []string{"a.json", `{"mcpServers":{}}`}},
		{"stops at --", []string{"claude", "--mcp-config", "a.json", "--", "--mcp-config", "b.json"}, []string{"a.json"}},
		{"none", []string{"claude", "--session-id", uuid}, nil},
		{"empty argv", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.argv).MCPConfigs(); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("MCPConfigs() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMCPForSandbox(t *testing.T) {
	cases := []struct {
		name        string
		configs     []string
		want        string // "" when nothing is left
		wantRemoved []string
		wantErr     bool
	}{
		{
			"Air's IDE server removed, nothing left",
			[]string{airMCP}, "", []string{"jetbrains_air_ide"}, false,
		},
		{
			"servers that run on the sandbox kept and mapped",
			[]string{`{"mcpServers":{"ide":{"command":"/opt/goland/bin/goland"},"npx":{"command":"npx","args":["srv"]},"proj":{"command":"/home/me/dev/api/bin/srv"},"web":{"type":"http","url":"https://mcp.example/x?a=1&b=2"}}}`},
			`{"mcpServers":{"npx":{"args":["srv"],"command":"npx"},"proj":{"command":"/home/u/dev/api/bin/srv"},"web":{"type":"http","url":"https://mcp.example/x?a=1&b=2"}}}`,
			[]string{"ide"}, false,
		},
		{
			"several configs merged, a later server of one name winning",
			[]string{`{"mcpServers":{"a":{"command":"x"},"b":{"command":"old"}}}`, airMCP, `{"mcpServers":{"b":{"command":"new"}}}`},
			`{"mcpServers":{"a":{"command":"x"},"b":{"command":"new"}}}`,
			[]string{"jetbrains_air_ide"}, false,
		},
		{"not JSON", []string{`nope`}, "", nil, true},
		{"servers not an object", []string{`{"mcpServers":[]}`}, "", nil, true},
		{"none", nil, "", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var configs [][]byte
			for _, c := range tc.configs {
				configs = append(configs, []byte(c))
			}
			doc, removed, err := MCPForSandbox(configs, toSandbox(t))
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if string(doc) != tc.want {
				t.Errorf("doc = %s, want %s", doc, tc.want)
			}
			if !reflect.DeepEqual(removed, tc.wantRemoved) {
				t.Errorf("removed = %q, want %q", removed, tc.wantRemoved)
			}
		})
	}
}
