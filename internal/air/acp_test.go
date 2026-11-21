package air

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func testAgent(mod func(*ClaudeAgent)) ClaudeAgent {
	a := ClaudeAgent{
		Sandbox:   "prod-box",
		Hermod:    "/home/me/go/bin/hermod",
		Node:      "/opt/node/bin/node",
		Adapter:   "/opt/node/bin/claude-agent-acp",
		StandIn:   "/home/me/.local/share/hermod/bin/claude",
		ConfigDir: "/home/me/.local/share/hermod/air/prod-box/claude",
	}
	if mod != nil {
		mod(&a)
	}
	return a
}

func TestNewEntry(t *testing.T) {
	cases := []struct {
		name     string
		agent    ClaudeAgent
		wantName string
		wantJSON string // "" when an error is wanted
	}{
		{
			"default name, no origin",
			testAgent(nil),
			"Claude (prod-box)",
			`{"Claude (prod-box)":{"command":"/home/me/go/bin/hermod","args":["air","acp","claude","--node=/opt/node/bin/node","--acp=/opt/node/bin/claude-agent-acp"],"env":{"CLAUDE_CODE_EXECUTABLE":"/home/me/.local/share/hermod/bin/claude","CLAUDE_CONFIG_DIR":"/home/me/.local/share/hermod/air/prod-box/claude","HERMOD_SANDBOX":"prod-box"}}}`,
		},
		{
			"given name and origin",
			testAgent(func(a *ClaudeAgent) { a.Name, a.Origin = "Remote", OriginOf("work-laptop") }),
			"Remote",
			`{"Remote":{"command":"/home/me/go/bin/hermod","args":["air","acp","claude","--node=/opt/node/bin/node","--acp=/opt/node/bin/claude-agent-acp"],"env":{"CLAUDE_CODE_EXECUTABLE":"/home/me/.local/share/hermod/bin/claude","CLAUDE_CONFIG_DIR":"/home/me/.local/share/hermod/air/prod-box/claude","HERMOD_ORIGIN":"work-laptop","HERMOD_SANDBOX":"prod-box"}}}`,
		},
		{
			"empty origin written empty",
			testAgent(func(a *ClaudeAgent) { a.Origin = OriginOf("") }),
			"Claude (prod-box)",
			`{"Claude (prod-box)":{"command":"/home/me/go/bin/hermod","args":["air","acp","claude","--node=/opt/node/bin/node","--acp=/opt/node/bin/claude-agent-acp"],"env":{"CLAUDE_CODE_EXECUTABLE":"/home/me/.local/share/hermod/bin/claude","CLAUDE_CONFIG_DIR":"/home/me/.local/share/hermod/air/prod-box/claude","HERMOD_ORIGIN":"","HERMOD_SANDBOX":"prod-box"}}}`,
		},
		{
			"paths kept as written",
			testAgent(func(a *ClaudeAgent) { a.Node = "/opt/a&b/<node>" }),
			"Claude (prod-box)",
			`{"Claude (prod-box)":{"command":"/home/me/go/bin/hermod","args":["air","acp","claude","--node=/opt/a&b/<node>","--acp=/opt/node/bin/claude-agent-acp"],"env":{"CLAUDE_CODE_EXECUTABLE":"/home/me/.local/share/hermod/bin/claude","CLAUDE_CONFIG_DIR":"/home/me/.local/share/hermod/air/prod-box/claude","HERMOD_SANDBOX":"prod-box"}}}`,
		},
		{"no sandbox", testAgent(func(a *ClaudeAgent) { a.Sandbox = " " }), "", ""},
		{"relative hermod", testAgent(func(a *ClaudeAgent) { a.Hermod = "hermod" }), "", ""},
		{"relative node", testAgent(func(a *ClaudeAgent) { a.Node = "node" }), "", ""},
		{"relative adapter", testAgent(func(a *ClaudeAgent) { a.Adapter = "bin/claude-agent-acp" }), "", ""},
		{"relative link", testAgent(func(a *ClaudeAgent) { a.StandIn = "claude" }), "", ""},
		{"relative config dir", testAgent(func(a *ClaudeAgent) { a.ConfigDir = "" }), "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, err := NewEntry(tc.agent)
			if (err != nil) != (tc.wantJSON == "") {
				t.Fatalf("NewEntry() error = %v, want one: %v", err, tc.wantJSON == "")
			}
			if err != nil {
				return
			}
			if e.Name() != tc.wantName {
				t.Errorf("Name() = %q, want %q", e.Name(), tc.wantName)
			}
			got, err := e.JSON()
			if err != nil {
				t.Fatal(err)
			}
			if compact(t, got) != tc.wantJSON {
				t.Errorf("JSON() =\n %s\nwant\n %s", compact(t, got), tc.wantJSON)
			}
		})
	}
}

// mergeEntry writes the entry into the acp.json at path as Configure does:
// ReadACP, then Merge.
func mergeEntry(path string, e Entry) error {
	f, err := ReadACP(path)
	if err != nil {
		return err
	}
	return f.Merge(e)
}

// TestMergeEntry asserts the entry is merged into acp.json with every other
// entry and member kept, and that a file it cannot read as an object is left
// unchanged.
func TestMergeEntry(t *testing.T) {
	const other = `"Other":{"command":"/usr/bin/other","args":["--x"],"env":{"K":"a&b"}}`
	cases := []struct {
		name     string
		before   string // "" with absent: no file
		absent   bool
		entry    ClaudeAgent
		want     string // compact JSON; "" when an error is wanted
		wantMode os.FileMode
	}{
		{
			"no file: created private",
			"", true, testAgent(func(a *ClaudeAgent) { a.Name = "Remote" }),
			`{"agent_servers":{"Remote":{"command":"/home/me/go/bin/hermod","args":["air","acp","claude","--node=/opt/node/bin/node","--acp=/opt/node/bin/claude-agent-acp"],"env":{"CLAUDE_CODE_EXECUTABLE":"/home/me/.local/share/hermod/bin/claude","CLAUDE_CONFIG_DIR":"/home/me/.local/share/hermod/air/prod-box/claude","HERMOD_SANDBOX":"prod-box"}}}}`,
			0o600,
		},
		{
			"empty file",
			"\n", false, testAgent(func(a *ClaudeAgent) { a.Name = "Remote" }),
			`{"agent_servers":{"Remote":{"command":"/home/me/go/bin/hermod","args":["air","acp","claude","--node=/opt/node/bin/node","--acp=/opt/node/bin/claude-agent-acp"],"env":{"CLAUDE_CODE_EXECUTABLE":"/home/me/.local/share/hermod/bin/claude","CLAUDE_CONFIG_DIR":"/home/me/.local/share/hermod/air/prod-box/claude","HERMOD_SANDBOX":"prod-box"}}}}`,
			0o644,
		},
		{
			"other entries and members kept",
			`{"default_mcp_settings":{"a":[1,2]},"agent_servers":{` + other + `}}`, false, testAgent(func(a *ClaudeAgent) { a.Name = "Remote" }),
			`{"agent_servers":{` + other + `,"Remote":{"command":"/home/me/go/bin/hermod","args":["air","acp","claude","--node=/opt/node/bin/node","--acp=/opt/node/bin/claude-agent-acp"],"env":{"CLAUDE_CODE_EXECUTABLE":"/home/me/.local/share/hermod/bin/claude","CLAUDE_CONFIG_DIR":"/home/me/.local/share/hermod/air/prod-box/claude","HERMOD_SANDBOX":"prod-box"}}},"default_mcp_settings":{"a":[1,2]}}`,
			0o644,
		},
		{
			"same name replaced",
			`{"agent_servers":{"Remote":{"command":"/old/node","args":[],"env":{}},` + other + `}}`, false, testAgent(func(a *ClaudeAgent) { a.Name = "Remote" }),
			`{"agent_servers":{` + other + `,"Remote":{"command":"/home/me/go/bin/hermod","args":["air","acp","claude","--node=/opt/node/bin/node","--acp=/opt/node/bin/claude-agent-acp"],"env":{"CLAUDE_CODE_EXECUTABLE":"/home/me/.local/share/hermod/bin/claude","CLAUDE_CONFIG_DIR":"/home/me/.local/share/hermod/air/prod-box/claude","HERMOD_SANDBOX":"prod-box"}}}}`,
			0o644,
		},
		{
			"agent_servers null",
			`{"agent_servers":null,"k":1}`, false, testAgent(func(a *ClaudeAgent) { a.Name = "Remote" }),
			`{"agent_servers":{"Remote":{"command":"/home/me/go/bin/hermod","args":["air","acp","claude","--node=/opt/node/bin/node","--acp=/opt/node/bin/claude-agent-acp"],"env":{"CLAUDE_CODE_EXECUTABLE":"/home/me/.local/share/hermod/bin/claude","CLAUDE_CONFIG_DIR":"/home/me/.local/share/hermod/air/prod-box/claude","HERMOD_SANDBOX":"prod-box"}}},"k":1}`,
			0o644,
		},
		{"not JSON", `{"agent_servers":`, false, testAgent(nil), "", 0o644},
		{"not an object", `[1]`, false, testAgent(nil), "", 0o644},
		{"null", `null`, false, testAgent(nil), "", 0o644},
		{"agent_servers not an object", `{"agent_servers":[]}`, false, testAgent(nil), "", 0o644},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".jetbrains", "acp.json")
			if !tc.absent {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(tc.before), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			e, err := NewEntry(tc.entry)
			if err != nil {
				t.Fatal(err)
			}
			err = mergeEntry(path, e)
			if (err != nil) != (tc.want == "") {
				t.Fatalf("merge error = %v, want one: %v", err, tc.want == "")
			}
			got, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if tc.want == "" {
				if string(got) != tc.before {
					t.Errorf("the file changed to %q", got)
				}
			} else if compact(t, got) != tc.want {
				t.Errorf("acp.json =\n %s\nwant\n %s", compact(t, got), tc.want)
			}
			if info, err := os.Stat(path); err != nil || info.Mode().Perm() != tc.wantMode {
				t.Errorf("mode = %v, %v, want %o", info, err, tc.wantMode)
			}
			assertNoLeftovers(t, filepath.Dir(path), "acp.json")
		})
	}
}

// TestMergeEntryIsIdempotent asserts configuring the same entry twice leaves
// one entry and the same file.
func TestMergeEntryIsIdempotent(t *testing.T) {
	cases := []struct {
		name  string
		agent ClaudeAgent
		want  []string // entry names after both merges
	}{
		{"same name twice", testAgent(func(a *ClaudeAgent) { a.Name = "Remote" }), []string{"Remote"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "acp.json")
			e, err := NewEntry(tc.agent)
			if err != nil {
				t.Fatal(err)
			}
			var files [][]byte
			for range 2 {
				if err := mergeEntry(path, e); err != nil {
					t.Fatal(err)
				}
				b, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				files = append(files, b)
			}
			if string(files[0]) != string(files[1]) {
				t.Errorf("the second merge changed the file:\n%s\n%s", files[0], files[1])
			}
			var doc struct {
				Servers map[string]json.RawMessage `json:"agent_servers"`
			}
			if err := json.Unmarshal(files[1], &doc); err != nil {
				t.Fatal(err)
			}
			var names []string
			for n := range doc.Servers {
				names = append(names, n)
			}
			if !reflect.DeepEqual(names, tc.want) {
				t.Errorf("entries = %q, want %q", names, tc.want)
			}
		})
	}
}

// TestMergeEntryThroughALink asserts a symlinked acp.json keeps its link and
// the file it names is updated.
func TestMergeEntryThroughALink(t *testing.T) {
	cases := []struct {
		name     string
		wantName string
	}{
		{"dotfiles link", "Claude (prod-box)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			real := filepath.Join(root, "dotfiles", "acp.json")
			link := filepath.Join(root, ".jetbrains", "acp.json")
			for _, d := range []string{filepath.Dir(real), filepath.Dir(link)} {
				if err := os.MkdirAll(d, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(real, []byte("{}"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(real, link); err != nil {
				t.Fatal(err)
			}
			e, err := NewEntry(testAgent(nil))
			if err != nil {
				t.Fatal(err)
			}
			if err := mergeEntry(link, e); err != nil {
				t.Fatal(err)
			}
			if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
				t.Errorf("the link was replaced: %v, %v", info, err)
			}
			b, err := os.ReadFile(real)
			if err != nil || !strings.Contains(string(b), tc.wantName) {
				t.Errorf("the linked file = %q, %v, want it to hold %q", b, err, tc.wantName)
			}
		})
	}
}

func TestConfigDir(t *testing.T) {
	cases := []struct {
		name     string
		dataHome string
		sandbox  string
		want     string // "" when an error is wanted
	}{
		{"plain alias", "/home/me/.local/share", "prod-box", "/home/me/.local/share/hermod/air/prod-box/claude"},
		{"dotted alias", "/xdg", "box.example.com", "/xdg/hermod/air/box.example.com/claude"},
		{"slash", "/xdg", "a/b", ""},
		{"backslash", "/xdg", `a\b`, ""},
		{"dot-dot", "/xdg", "..", ""},
		{"dot", "/xdg", ".", ""},
		{"empty", "/xdg", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ConfigDir(tc.dataHome, tc.sandbox)
			if got != tc.want || (err != nil) != (tc.want == "") {
				t.Errorf("ConfigDir() = %q, %v, want %q", got, err, tc.want)
			}
		})
	}
}

func TestNamesAndPaths(t *testing.T) {
	cases := []struct {
		name         string
		sandbox      string
		home         string
		wantName     string
		wantACPPath  string
		wantVariable string
	}{
		{"prod-box", "prod-box", "/home/me", "Claude (prod-box)", "/home/me/.jetbrains/acp.json", "HERMOD_SANDBOX=prod-box"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DefaultName(tc.sandbox); got != tc.wantName {
				t.Errorf("DefaultName() = %q, want %q", got, tc.wantName)
			}
			if got := ACPPath(tc.home); got != tc.wantACPPath {
				t.Errorf("ACPPath() = %q, want %q", got, tc.wantACPPath)
			}
			if got := SandboxVariable(tc.sandbox); got != tc.wantVariable {
				t.Errorf("SandboxVariable() = %q, want %q", got, tc.wantVariable)
			}
		})
	}
}

// compact is a JSON document with its whitespace removed, characters kept.
func compact(t *testing.T, doc []byte) string {
	t.Helper()
	var out strings.Builder
	inString, escaped := false, false
	for _, c := range string(doc) {
		switch {
		case inString:
			out.WriteRune(c)
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
		case c == '"':
			inString = true
			out.WriteRune(c)
		case c == ' ' || c == '\n' || c == '\t' || c == '\r':
		default:
			out.WriteRune(c)
		}
	}
	return out.String()
}
