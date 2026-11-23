package claude

import (
	"reflect"
	"strings"
	"testing"

	"github.com/zerlok/hermod/internal/agent"
)

const (
	uuid  = "0b7e5f3c-2a41-4c8e-9d6f-1e2a3b4c5d6e"
	upper = "0B7E5F3C-2A41-4C8E-9D6F-1E2A3B4C5D6E"
	fork  = "5d6e1e2a-3b4c-4c8e-9d6f-0b7e5f3c2a41"
)

// form is what a parsed call asks for, as a test names it.
type form int

const (
	command form = iota
	oneShot
	conversation
)

func formOf(c agent.Call) form {
	if _, ok := c.ID(); ok {
		return conversation
	}
	if c.OneShot() {
		return oneShot
	}
	return command
}

func TestParse(t *testing.T) {
	cases := []struct {
		name   string
		argv   string // split on spaces; "" is an empty argv
		want   form
		wantID string // "" when the call is no conversation
	}{
		{"subcommand", "claude auth status", oneShot, ""},
		{"mcp subcommand", "claude mcp list", oneShot, ""},
		{"subcommand with a session flag", "claude agents --json --session-id " + uuid, oneShot, ""},
		{"version", "claude --version", oneShot, ""},
		{"short version", "claude -v", oneShot, ""},
		{"help", "claude --help", oneShot, ""},
		{"help with a session", "claude --session-id " + uuid + " -h", oneShot, ""},
		{"settings read", "claude -p --input-format stream-json --output-format stream-json --verbose", oneShot, ""},
		{"print long form", "claude --print hello", oneShot, ""},
		{"print with a resume picker", "claude -p --resume", oneShot, ""},
		{"print fork with no new session", "claude -p --resume " + uuid + " --fork-session", oneShot, ""},
		{"by path", "/usr/local/bin/claude --version", oneShot, ""},

		{"stream-json session", "claude --output-format stream-json --input-format stream-json --session-id=" + uuid, conversation, uuid},
		{"stream-json resume", "claude --input-format=stream-json --resume=" + uuid, conversation, uuid},
		{"print with a session", "claude -p --input-format stream-json --session-id " + uuid, conversation, uuid},
		{"terminal resume", "claude --resume " + uuid + " --settings /tmp/s.json", conversation, uuid},
		{"terminal new", "claude --session-id " + uuid + " --settings /tmp/s.json", conversation, uuid},
		{"short resume", "claude -r " + uuid, conversation, uuid},
		{"upper-case UUID", "claude --session-id " + upper, conversation, upper},
		{"fork keyed by its new session", "claude --resume " + uuid + " --fork-session --session-id " + fork, conversation, fork},
		{"fork with the new session first", "claude --session-id=" + fork + " --fork-session --resume=" + uuid, conversation, fork},

		{"plain claude", "claude --model opus", command, ""},
		{"bare claude", "claude", command, ""},
		{"resume picker", "claude --resume", command, ""},
		{"resume search term", "claude --resume fix-login", command, ""},
		{"continue", "claude -c", command, ""},
		{"fork with no new session", "claude --resume " + uuid + " --fork-session", command, ""},
		{"malformed session id", "claude --session-id not-a-uuid", command, ""},
		{"session id missing value", "claude --session-id", command, ""},
		{"path traversal as an ID", "claude --session-id ../../../../../../../../../etc/passw", command, ""},
		{"subcommand word later is a prompt", "claude --model opus auth", command, ""},
		{"flags after -- are prompt text", "claude -- --version --session-id " + uuid, command, ""},
		{"session id consumes its value", "claude --session-id --version", command, ""},
		{"not claude", "htop -d 5", command, ""},
		{"claude-like name", "claude-code --version", command, ""},
		{"empty argv", "", command, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			argv := strings.Fields(tc.argv)
			call := Agent{}.Parse(argv)
			if got := formOf(call); got != tc.want {
				t.Errorf("form = %v, want %v", got, tc.want)
			}
			if id, ok := call.ID(); id != tc.wantID || ok != (tc.wantID != "") {
				t.Errorf("ID() = %q, %v, want %q", id, ok, tc.wantID)
			}
			if got := call.Args(); !reflect.DeepEqual(got, argvOrNil(argv)) {
				t.Errorf("Args() = %q, want %q", got, argv)
			}
		})
	}
}

func argvOrNil(argv []string) []string {
	if len(argv) == 0 {
		return nil
	}
	return argv
}

var agentsArgv = []string{"claude", "agents", "--json"}

func TestProbe(t *testing.T) {
	cases := []struct {
		name        string
		id          string
		out         string
		wantCommand []string
		want        agent.State
		wantErr     bool
	}{
		{"listed", uuid, `[{"pid":1,"cwd":"/x","kind":"interactive","sessionId":"` + uuid + `","status":"busy"}]`, agentsArgv, agent.Running, false},
		{"asked in upper case", upper, `[{"sessionId":"` + uuid + `"}]`, agentsArgv, agent.Running, false},
		{"another conversation", uuid, `[{"sessionId":"` + fork + `"}]`, agentsArgv, agent.Stopped, false},
		{"none running", uuid, `[]`, agentsArgv, agent.Stopped, false},
		{"not JSON", uuid, `error: unknown command`, agentsArgv, agent.Stopped, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			probe := Agent{}.Probe(tc.id)
			if got := probe.Command(); !reflect.DeepEqual(got, tc.wantCommand) {
				t.Errorf("Command() = %q, want %q", got, tc.wantCommand)
			}
			got, err := probe.Read([]byte(tc.out))
			if got != tc.want || (err != nil) != tc.wantErr {
				t.Errorf("Read() = %v, %v, want %v, error %v", got, err, tc.want, tc.wantErr)
			}
		})
	}
}

func TestConnect(t *testing.T) {
	cases := []struct {
		name    string
		version int
		object  string
		want    bool
	}{
		{"claude-agent-acp's answer", acpVersion, acpCapabilities, true},
		{"the same, spaced and ordered otherwise", 1, `{"sessionCapabilities":{"resume":{},"list":{},"fork":{},"close":{}},"mcpCapabilities":{"sse":true,"http":true},"promptCapabilities":{"embeddedContext":true,"image":true},"loadSession":true}`, true},
		{"another version", 2, acpCapabilities, false},
		{"no session loading", 1, `{"loadSession":false}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			other, err := agent.NewHandshake(tc.version, []byte(tc.object))
			if err != nil {
				t.Fatalf("NewHandshake() error: %v", err)
			}
			if got := (Agent{}).Connect().Equal(other); got != tc.want {
				t.Errorf("Connect().Equal() = %v, want %v", got, tc.want)
			}
		})
	}
}
