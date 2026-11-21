package control

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestConfigure(t *testing.T) {
	emptyOrigin, namedOrigin := "", "work-laptop"
	cases := []struct {
		name       string
		sandbox    string
		entryName  string
		origin     *string
		dryRun     bool
		wantEntry  string            // the entry's name in acp.json; "" when acp.json is not written
		wantRun    []string          // the entry's command and args, paths under the temporary root as {root}
		wantEnv    map[string]string // the entry's env, paths under the temporary root as {root}
		wantOut    []string          // lines printed, paths under the temporary root as {root}
		wantNotOut []string
		wantErr    bool
	}{
		{
			"entry and link written, terminal mode printed",
			"prod-box", "", nil, false,
			"Claude (prod-box)",
			[]string{"{root}/go/bin/hermod", "air", "acp", "claude", "--node={root}/node/bin/node", "--acp={root}/node/bin/claude-agent-acp"},
			map[string]string{
				"HERMOD_SANDBOX":         "prod-box",
				"CLAUDE_CODE_EXECUTABLE": "{root}/data/hermod/bin/claude",
				"CLAUDE_CONFIG_DIR":      "{root}/data/hermod/air/prod-box/claude",
			},
			[]string{
				"Stand-in link {root}/data/hermod/bin/claude → {root}/go/bin/hermod: created",
				`Air agent "Claude (prod-box)" written to {root}/home/.jetbrains/acp.json`,
				"  {root}/data/hermod/bin/claude\n",
				"  HERMOD_SANDBOX=prod-box\n",
			},
			[]string{"Dry run"}, false,
		},
		{
			"named, with an origin",
			"prod-box", "Remote", &namedOrigin, false,
			"Remote",
			[]string{"{root}/go/bin/hermod", "air", "acp", "claude", "--node={root}/node/bin/node", "--acp={root}/node/bin/claude-agent-acp"},
			map[string]string{
				"HERMOD_SANDBOX":         "prod-box",
				"HERMOD_ORIGIN":          "work-laptop",
				"CLAUDE_CODE_EXECUTABLE": "{root}/data/hermod/bin/claude",
				"CLAUDE_CONFIG_DIR":      "{root}/data/hermod/air/prod-box/claude",
			},
			[]string{`Air agent "Remote"`}, nil, false,
		},
		{
			"empty origin written empty",
			"box-b", "", &emptyOrigin, false,
			"Claude (box-b)",
			[]string{"{root}/go/bin/hermod", "air", "acp", "claude", "--node={root}/node/bin/node", "--acp={root}/node/bin/claude-agent-acp"},
			map[string]string{
				"HERMOD_SANDBOX":         "box-b",
				"HERMOD_ORIGIN":          "",
				"CLAUDE_CODE_EXECUTABLE": "{root}/data/hermod/bin/claude",
				"CLAUDE_CONFIG_DIR":      "{root}/data/hermod/air/box-b/claude",
			},
			[]string{"HERMOD_SANDBOX=box-b"}, nil, false,
		},
		{
			"dry run prints the entry and changes nothing",
			"prod-box", "", nil, true,
			"", nil, nil,
			[]string{"Dry run", `"Claude (prod-box)": {`, `"HERMOD_SANDBOX": "prod-box"`, `"command": "{root}/go/bin/hermod"`, `"--node={root}/node/bin/node"`, `"--acp={root}/node/bin/claude-agent-acp"`, "  HERMOD_SANDBOX=prod-box\n"},
			[]string{"Stand-in link", "written to"}, false,
		},
		{
			"a sandbox that cannot name a directory",
			"a/b", "", nil, false,
			"", nil, nil, nil, nil, true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			sub := func(s string) string { return strings.ReplaceAll(s, "{root}", root) }
			var out bytes.Buffer
			err := Configure(Registration{
				Sandbox:  tc.sandbox,
				Name:     tc.entryName,
				Node:     filepath.Join(root, "node", "bin", "node"),
				Adapter:  filepath.Join(root, "node", "bin", "claude-agent-acp"),
				Hermod:   filepath.Join(root, "go", "bin", "hermod"),
				DataHome: filepath.Join(root, "data"),
				Home:     filepath.Join(root, "home"),
				Origin:   tc.origin,
				DryRun:   tc.dryRun,
				Out:      &out,
			})
			if (err != nil) != tc.wantErr {
				t.Fatalf("Configure() error = %v, wantErr %v", err, tc.wantErr)
			}
			for _, want := range tc.wantOut {
				if !strings.Contains(out.String(), sub(want)) {
					t.Errorf("output lacks %q:\n%s", sub(want), out.String())
				}
			}
			for _, not := range tc.wantNotOut {
				if strings.Contains(out.String(), not) {
					t.Errorf("output has %q:\n%s", not, out.String())
				}
			}
			acp, readErr := os.ReadFile(filepath.Join(root, "home", ".jetbrains", "acp.json"))
			_, linkErr := os.Lstat(filepath.Join(root, "data", "hermod", "bin", "claude"))
			if tc.wantEntry == "" {
				if readErr == nil || linkErr == nil {
					t.Errorf("something was written: acp.json %v, link %v", readErr, linkErr)
				}
				return
			}
			if linkErr != nil {
				t.Errorf("no stand-in link: %v", linkErr)
			}
			var doc struct {
				Servers map[string]struct {
					Command string            `json:"command"`
					Args    []string          `json:"args"`
					Env     map[string]string `json:"env"`
				} `json:"agent_servers"`
			}
			if err := json.Unmarshal(acp, &doc); err != nil {
				t.Fatalf("acp.json: %v", err)
			}
			want := map[string]string{}
			for k, v := range tc.wantEnv {
				want[k] = sub(v)
			}
			entry := doc.Servers[tc.wantEntry]
			if got := entry.Env; !reflect.DeepEqual(got, want) {
				t.Errorf("env = %v, want %v", got, want)
			}
			var wantRun []string
			for _, a := range tc.wantRun {
				wantRun = append(wantRun, sub(a))
			}
			if got := append([]string{entry.Command}, entry.Args...); !reflect.DeepEqual(got, wantRun) {
				t.Errorf("runs %q, want %q", got, wantRun)
			}
		})
	}
}

// TestConfigureRefusesAnUnmergeableFile asserts an acp.json no entry can be
// merged into fails the run before anything is written: no stand-in link, the
// file as it was.
func TestConfigureRefusesAnUnmergeableFile(t *testing.T) {
	cases := []struct {
		name   string
		before string
		dryRun bool
	}{
		{"agent_servers not an object", `{"agent_servers":[]}`, false},
		{"not an object", `[1]`, false},
		{"not JSON, dry run", `{"agent_servers":`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			acpFile := filepath.Join(root, "home", ".jetbrains", "acp.json")
			link := filepath.Join(root, "data", "hermod", "bin", "claude")
			if err := os.MkdirAll(filepath.Dir(acpFile), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(acpFile, []byte(tc.before), 0o644); err != nil {
				t.Fatal(err)
			}
			err := Configure(Registration{
				Sandbox:  "prod-box",
				Node:     filepath.Join(root, "node", "bin", "node"),
				Adapter:  filepath.Join(root, "node", "bin", "claude-agent-acp"),
				Hermod:   filepath.Join(root, "go", "bin", "hermod"),
				DataHome: filepath.Join(root, "data"),
				Home:     filepath.Join(root, "home"),
				DryRun:   tc.dryRun,
				Out:      &bytes.Buffer{},
			})
			if err == nil {
				t.Fatal("Configure() error = nil, want one")
			}
			if _, err := os.Lstat(link); err == nil {
				t.Error("the stand-in link was made")
			}
			if got, err := os.ReadFile(acpFile); err != nil || string(got) != tc.before {
				t.Errorf("acp.json = %q, %v, want %q", got, err, tc.before)
			}
		})
	}
}
