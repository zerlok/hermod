package claude

import (
	"strings"
	"testing"

	"github.com/zerlok/hermod/internal/agent"
)

const (
	uuid  = "0b7e5f3c-2a41-4c8e-9d6f-1e2a3b4c5d6e"
	upper = "0B7E5F3C-2A41-4C8E-9D6F-1E2A3B4C5D6E"
	fork  = "5d6e1e2a-3b4c-4c8e-9d6f-0b7e5f3c2a41"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		name   string
		argv   string // split on spaces; "" is an empty argv
		want   agent.Invocation
		wantID string // "" when the call is no conversation
	}{
		{"subcommand", "claude auth status", agent.InvokesOneShot, ""},
		{"mcp subcommand", "claude mcp list", agent.InvokesOneShot, ""},
		{"subcommand with a session flag", "claude agents --json --session-id " + uuid, agent.InvokesOneShot, ""},
		{"version", "claude --version", agent.InvokesOneShot, ""},
		{"short version", "claude -v", agent.InvokesOneShot, ""},
		{"help", "claude --help", agent.InvokesOneShot, ""},
		{"help with a session", "claude --session-id " + uuid + " -h", agent.InvokesOneShot, ""},
		{"settings read", "claude -p --input-format stream-json --output-format stream-json --verbose", agent.InvokesOneShot, ""},
		{"print long form", "claude --print hello", agent.InvokesOneShot, ""},
		{"print with a resume picker", "claude -p --resume", agent.InvokesOneShot, ""},
		{"print fork with no new session", "claude -p --resume " + uuid + " --fork-session", agent.InvokesOneShot, ""},
		{"by path", "/usr/local/bin/claude --version", agent.InvokesOneShot, ""},

		{"stream-json session", "claude --output-format stream-json --input-format stream-json --session-id=" + uuid, agent.InvokesConversation, uuid},
		{"stream-json resume", "claude --input-format=stream-json --resume=" + uuid, agent.InvokesConversation, uuid},
		{"print with a session", "claude -p --input-format stream-json --session-id " + uuid, agent.InvokesConversation, uuid},
		{"terminal resume", "claude --resume " + uuid + " --settings /tmp/s.json", agent.InvokesConversation, uuid},
		{"terminal new", "claude --session-id " + uuid + " --settings /tmp/s.json", agent.InvokesConversation, uuid},
		{"short resume", "claude -r " + uuid, agent.InvokesConversation, uuid},
		{"upper-case UUID", "claude --session-id " + upper, agent.InvokesConversation, upper},
		{"fork keyed by its new session", "claude --resume " + uuid + " --fork-session --session-id " + fork, agent.InvokesConversation, fork},
		{"fork with the new session first", "claude --session-id=" + fork + " --fork-session --resume=" + uuid, agent.InvokesConversation, fork},

		{"plain claude", "claude --model opus", agent.InvokesCommand, ""},
		{"bare claude", "claude", agent.InvokesCommand, ""},
		{"resume picker", "claude --resume", agent.InvokesCommand, ""},
		{"resume search term", "claude --resume fix-login", agent.InvokesCommand, ""},
		{"continue", "claude -c", agent.InvokesCommand, ""},
		{"fork with no new session", "claude --resume " + uuid + " --fork-session", agent.InvokesCommand, ""},
		{"malformed session id", "claude --session-id not-a-uuid", agent.InvokesCommand, ""},
		{"session id missing value", "claude --session-id", agent.InvokesCommand, ""},
		{"path traversal as an ID", "claude --session-id ../../../../../../../../../etc/passw", agent.InvokesCommand, ""},
		{"subcommand word later is a prompt", "claude --model opus auth", agent.InvokesCommand, ""},
		{"flags after -- are prompt text", "claude -- --version --session-id " + uuid, agent.InvokesCommand, ""},
		{"session id consumes its value", "claude --session-id --version", agent.InvokesCommand, ""},
		{"not claude", "htop -d 5", agent.InvokesCommand, ""},
		{"claude-like name", "claude-code --version", agent.InvokesCommand, ""},
		{"empty argv", "", agent.InvokesCommand, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, id := Agent{}.Classify(strings.Fields(tc.argv))
			if got != tc.want || id != tc.wantID {
				t.Errorf("Classify() = %v, %q, want %v, %q", got, id, tc.want, tc.wantID)
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
		{"the same, spaced and ordered otherwise", 1, `{"sessionCapabilities":{"resume":{},"list":{},"close":{}},"mcpCapabilities":{"sse":true,"http":true},"promptCapabilities":{"embeddedContext":true,"image":true},"loadSession":true}`, true},
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
