package claude

import (
	"reflect"
	"strings"
	"testing"

	"github.com/zerlok/hermod/internal/secret"
)

const (
	airToken     = "Bearer air-run-token"
	sessionToken = "session-token-0123"
)

func TestSettingsForSandbox(t *testing.T) {
	cases := []struct {
		name         string
		doc          string
		want         string
		wantEndpoint string // "" when no hook goes to a local endpoint
		wantAuth     string
		wantDropped  []string
		wantRoutes   []string // the routed hooks' request URIs
	}{
		{
			"Air's hooks routed to the session's port and token",
			`{"hooks":{"Stop":[{"hooks":[{"type":"http","url":"http://localhost:63342/air/claude/hook","headers":{"Authorization":"` + airToken + `"},"timeout":1}]}],` +
				`"PostToolUse":[{"matcher":"Write|Edit","hooks":[{"type":"http","url":"http://localhost:63342/air/claude/hook?x=1","headers":{"authorization":"` + airToken + `","X-Other":"kept"},"timeout":1.5}]}]}}`,
			`{"hooks":{"PostToolUse":[{"hooks":[{"headers":{"Authorization":"Bearer ` + sessionToken + `","X-Other":"kept"},"timeout":1.5,"type":"http","url":"http://localhost:45001/air/claude/hook?x=1"}],"matcher":"Write|Edit"}],` +
				`"Stop":[{"hooks":[{"headers":{"Authorization":"Bearer ` + sessionToken + `"},"timeout":1,"type":"http","url":"http://localhost:45001/air/claude/hook"}]}]}}`,
			"http://localhost:63342", airToken, nil, []string{"/air/claude/hook?x=1", "/air/claude/hook"},
		},
		{
			"hook without a header gets the session's",
			`{"hooks":{"Stop":[{"hooks":[{"type":"http","url":"http://[::1]:7000/h"}]}]}}`,
			`{"hooks":{"Stop":[{"hooks":[{"headers":{"Authorization":"Bearer ` + sessionToken + `"},"type":"http","url":"http://localhost:45001/h"}]}]}}`,
			"http://[::1]:7000", "", nil, []string{"/h"},
		},
		{
			"remote and command hooks left alone, paths mapped",
			`{"cwd":"/home/me/dev/api","hooks":{"Stop":[{"hooks":[{"type":"http","url":"https://hooks.example/x"},{"type":"command","command":"/home/me/dev/api/hook.sh"}]}]}}`,
			`{"cwd":"/home/u/dev/api","hooks":{"Stop":[{"hooks":[{"type":"http","url":"https://hooks.example/x"},{"type":"command","command":"/home/u/dev/api/hook.sh"}]}]}}`,
			"", "", nil, nil,
		},
		{
			"no hooks",
			`{"model":"opus"}`,
			`{"model":"opus"}`,
			"", "", nil, nil,
		},
		{
			"Air's endpoint routed, another local one dropped",
			`{"hooks":{"PostToolUse":[{"hooks":[{"type":"http","url":"http://localhost:9000/mine"}]}],"Stop":[{"hooks":[{"type":"http","url":"http://localhost:63342/air/claude/hook"},{"type":"http","url":"http://u:p@127.0.0.1:9000/mine"},{"type":"command","command":"x"}]}]}}`,
			`{"hooks":{"Stop":[{"hooks":[{"headers":{"Authorization":"Bearer ` + sessionToken + `"},"type":"http","url":"http://localhost:45001/air/claude/hook"},{"command":"x","type":"command"}]}]}}`,
			"http://localhost:63342", "", []string{"http://localhost:9000/mine", "http://127.0.0.1:9000/mine"}, []string{"/air/claude/hook"},
		},
		{
			"without Air's, the first endpoint by event routed",
			`{"hooks":{"Stop":[{"hooks":[{"type":"http","url":"http://localhost:2/h"}]}],"PostToolUse":[{"hooks":[{"type":"http","url":"http://localhost:1/h"}]}]}}`,
			`{"hooks":{"PostToolUse":[{"hooks":[{"headers":{"Authorization":"Bearer ` + sessionToken + `"},"type":"http","url":"http://localhost:45001/h"}]}]}}`,
			"http://localhost:1", "", []string{"http://localhost:2/h"}, []string{"/h"},
		},
		{
			"one endpoint with a second token: that hook dropped",
			`{"hooks":{"Stop":[{"hooks":[{"type":"http","url":"http://localhost:1/h","headers":{"Authorization":"a"}},{"type":"http","url":"http://localhost:1/h","headers":{"Authorization":"b"}}]}]}}`,
			`{"hooks":{"Stop":[{"hooks":[{"headers":{"Authorization":"Bearer ` + sessionToken + `"},"type":"http","url":"http://localhost:45001/h"}]}]}}`,
			"http://localhost:1", "a", []string{"http://localhost:1/h"}, []string{"/h"},
		},
		{
			"a dropped hook's credentials in its user info and query left out",
			`{"hooks":{"Stop":[{"hooks":[{"type":"http","url":"http://localhost:63342/air/claude/hook"},{"type":"http","url":"http://user:PWSECRET@127.0.0.1:7777/x?tok=OTHERSECRET"}]}]}}`,
			`{"hooks":{"Stop":[{"hooks":[{"headers":{"Authorization":"Bearer ` + sessionToken + `"},"type":"http","url":"http://localhost:45001/air/claude/hook"}]}]}}`,
			"http://localhost:63342", "", []string{"http://127.0.0.1:7777/x"}, []string{"/air/claude/hook"},
		},
	}
	route := HookRoute{Port: 45001, Token: secret.FromString(sessionToken)}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SettingsForSandbox([]byte(tc.doc), toSandbox(t), route)
			if err != nil {
				t.Fatalf("SettingsForSandbox() error: %v", err)
			}
			if string(got.Doc()) != tc.want {
				t.Errorf("doc =\n %s\nwant\n %s", got.Doc(), tc.want)
			}
			if strings.Contains(string(got.Doc()), "air-run-token") {
				t.Error("Air's token reached the sandbox's settings")
			}
			if !reflect.DeepEqual(got.Dropped(), tc.wantDropped) {
				t.Errorf("Dropped() = %q, want %q", got.Dropped(), tc.wantDropped)
			}
			target, ok := got.Hooks()
			if ok != (tc.wantEndpoint != "") {
				t.Fatalf("Hooks() ok = %v, want %v", ok, tc.wantEndpoint != "")
			}
			if !ok {
				return
			}
			if target.Endpoint.String() != tc.wantEndpoint || target.Authorization.Reveal() != tc.wantAuth {
				t.Errorf("target = %s %q, want %s %q", target.Endpoint, target.Authorization.Reveal(), tc.wantEndpoint, tc.wantAuth)
			}
			if !reflect.DeepEqual(target.Routes, tc.wantRoutes) {
				t.Errorf("Routes = %q, want %q", target.Routes, tc.wantRoutes)
			}
		})
	}
}

func TestSettingsForSandboxRefuses(t *testing.T) {
	cases := []struct {
		name string
		doc  string
	}{
		{"not JSON", `{"hooks":`},
		{"not an object", `[1,2]`},
		{
			"header that is not a string",
			`{"hooks":{"Stop":[{"hooks":[{"type":"http","url":"http://localhost:1/h","headers":{"Authorization":1}}]}]}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SettingsForSandbox([]byte(tc.doc), toSandbox(t), HookRoute{Port: 45001, Token: secret.FromString(sessionToken)})
			if err == nil {
				t.Fatal("err = nil, want one")
			}
			if got.Doc() != nil {
				t.Errorf("doc = %s, want none", got.Doc())
			}
		})
	}
}

func TestEnvironment(t *testing.T) {
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
			[]string{"MAX_THINKING_TOKENS=1024", "HOME=/home/me", "ANTHROPIC_MODEL=opus", "CLAUDE_AGENT_SDK_VERSION=0.1"},
			[]string{"MAX_THINKING_TOKENS=1024", "ANTHROPIC_MODEL=opus", "CLAUDE_AGENT_SDK_VERSION=0.1"},
		},
		{"values with = and newlines kept", []string{"CLAUDE_X=a=b\nc"}, []string{"CLAUDE_X=a=b\nc"}},
		{"look-alikes left out", []string{"CLAUDE=1", "MY_CLAUDE_X=1", "MAX_THINKING_TOKENS_X=1", "malformed"}, nil},
		{
			"what a parent Claude sets for its children left out",
			[]string{"CLAUDE_CODE_EXECPATH=/opt/claude", "CLAUDE_PID=7", "CLAUDE_CODE_SESSION_ID=s", "CLAUDE_CODE_BRIDGE_SESSION_ID=b", "CLAUDE_CODE_CHILD_SESSION=1", "CLAUDE_CODE_SESSION_ATTENDED=1", "CLAUDE_CODE_MESSAGING_SOCKET=/tmp/s", "CLAUDE_CODE_MESSAGING_TOKEN=t", "CLAUDE_CODE_SSE_PORT=1", "CLAUDE_PROJECT_DIR=/p", "CLAUDE_ENV_FILE=/e", "CLAUDE_CODE_ENTRYPOINT=cli"},
			[]string{"CLAUDE_CODE_ENTRYPOINT=cli"},
		},
		{"empty", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Environment(secret.NewEnv(tc.environ)).Reveal(); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Environment() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTranscriptPath(t *testing.T) {
	cases := []struct {
		name       string
		configDir  string
		projectDir string
		want       string
	}{
		{"home project", "/home/me/.claude", "/home/me/dev/api", "/home/me/.claude/projects/-home-me-dev-api/" + uuid + ".jsonl"},
		{"dots and spaces", "/c", "/home/me/my.app v2", "/c/projects/-home-me-my-app-v2/" + uuid + ".jsonl"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, _ := ParseSessionID(uuid)
			if got := TranscriptPath(tc.configDir, tc.projectDir, id); got != tc.want {
				t.Errorf("TranscriptPath() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTranscriptDir(t *testing.T) {
	cases := []struct {
		name       string
		configDir  string
		projectDir string
		want       string
	}{
		{"sandbox home project", "/home/u/.claude", "/home/u/dev/api", "/home/u/.claude/projects/-home-u-dev-api"},
		{"local config directory of a sandbox", "/home/me/.local/share/hermod/air/box/claude", "/home/me/dev/api", "/home/me/.local/share/hermod/air/box/claude/projects/-home-me-dev-api"},
		{"every non-alphanumeric character a dash", "/c", "/home/me/my_app.v2 x", "/c/projects/-home-me-my-app-v2-x"},
		{"trailing slash on the config directory", "/c/", "/p", "/c/projects/-p"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := TranscriptDir(tc.configDir, tc.projectDir); got != tc.want {
				t.Errorf("TranscriptDir() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRunning(t *testing.T) {
	cases := []struct {
		name    string
		out     string
		want    bool
		wantErr bool
	}{
		{"listed", `[{"pid":1,"cwd":"/x","kind":"interactive","sessionId":"` + uuid + `","status":"busy"}]`, true, false},
		{"listed in upper case", `[{"sessionId":"` + upper + `"}]`, true, false},
		{"another session", `[{"sessionId":"` + fork + `"}]`, false, false},
		{"none running", `[]`, false, false},
		{"not JSON", `error: unknown command`, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, _ := ParseSessionID(uuid)
			got, err := Running([]byte(tc.out), id)
			if got != tc.want || (err != nil) != tc.wantErr {
				t.Errorf("Running() = %v, %v, want %v, err %v", got, err, tc.want, tc.wantErr)
			}
		})
	}
}
