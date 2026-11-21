package claude

import (
	"reflect"
	"strings"
	"testing"
)

const (
	uuid  = "0b7e5f3c-2a41-4c8e-9d6f-1e2a3b4c5d6e"
	upper = "0B7E5F3C-2A41-4C8E-9D6F-1E2A3B4C5D6E"
	fork  = "5d6e1e2a-3b4c-4c8e-9d6f-0b7e5f3c2a41"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		name        string
		argv        string // split on spaces; "" is an empty argv
		wantKind    Kind
		wantSession SessionID // "" when the kind has none
	}{
		// Utility calls.
		{"subcommand", "claude auth status", UtilityCall, ""},
		{"mcp subcommand", "claude mcp list", UtilityCall, ""},
		{"subcommand with a session flag", "claude agents --json --session-id " + uuid, UtilityCall, ""},
		{"version", "claude --version", UtilityCall, ""},
		{"short version", "claude -v", UtilityCall, ""},
		{"help", "claude --help", UtilityCall, ""},
		{"help with a session", "claude --session-id " + uuid + " -h", UtilityCall, ""},
		{"settings read", "claude -p --input-format stream-json --output-format stream-json --verbose", UtilityCall, ""},
		{"print long form", "claude --print hello", UtilityCall, ""},
		{"print with a resume picker", "claude -p --resume", UtilityCall, ""},
		{"by path", "/usr/local/bin/claude --version", UtilityCall, ""},

		// Chat sessions.
		{"SDK session", "claude --output-format stream-json --input-format stream-json --session-id=" + uuid, ChatSession, uuid},
		{"SDK resume", "claude --input-format=stream-json --resume=" + uuid, ChatSession, uuid},
		{"SDK resume separate value", "claude --input-format stream-json --resume " + uuid + " --verbose", ChatSession, uuid},
		{"print with a stream-json session", "claude -p --input-format stream-json --session-id " + uuid, ChatSession, uuid},
		{"SDK fork keyed by its new session", "claude --input-format stream-json --resume " + uuid + " --fork-session --session-id " + fork, ChatSession, fork},

		// Terminal sessions.
		{"terminal resume", "claude --resume " + uuid + " --settings /tmp/s.json", TerminalSession, uuid},
		{"terminal new", "claude --session-id " + uuid + " --settings /tmp/s.json", TerminalSession, uuid},
		{"short resume", "claude -r " + uuid, TerminalSession, uuid},
		{"text input format", "claude --input-format text --session-id " + uuid, TerminalSession, uuid},
		{"upper-case UUID", "claude --session-id " + upper, TerminalSession, upper},
		{"print with a terminal session", "claude -p --session-id " + uuid, TerminalSession, uuid},
		{"fork keyed by its new session", "claude --resume " + uuid + " --fork-session --session-id " + fork, TerminalSession, fork},
		{"fork with the new session first", "claude --session-id=" + fork + " --fork-session --resume=" + uuid, TerminalSession, fork},

		// Not agent sessions.
		{"plain claude", "claude --model opus", NotAgentSession, ""},
		{"bare claude", "claude", NotAgentSession, ""},
		{"resume picker", "claude --resume", NotAgentSession, ""},
		{"resume search term", "claude --resume fix-login", NotAgentSession, ""},
		{"continue", "claude -c", NotAgentSession, ""},
		{"fork with no new session", "claude --resume " + uuid + " --fork-session", NotAgentSession, ""},
		{"print fork with no new session", "claude -p --resume " + uuid + " --fork-session", UtilityCall, ""},
		{"malformed session id", "claude --session-id not-a-uuid", NotAgentSession, ""},
		{"session id missing value", "claude --session-id", NotAgentSession, ""},
		{"subcommand word later is a prompt", "claude --model opus auth", NotAgentSession, ""},
		{"flags after -- are prompt text", "claude -- --version --session-id " + uuid, NotAgentSession, ""},
		{"session id consumes its value", "claude --session-id --version", NotAgentSession, ""},
		{"not claude", "htop -d 5", NotAgentSession, ""},
		{"claude-like name", "claude-code --version", NotAgentSession, ""},
		{"empty argv", "", NotAgentSession, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			argv := strings.Fields(tc.argv)
			call := Classify(argv)
			if call.Kind() != tc.wantKind {
				t.Errorf("Kind() = %v, want %v", call.Kind(), tc.wantKind)
			}
			id, ok := call.Session()
			if id != tc.wantSession || ok != (tc.wantSession != "") {
				t.Errorf("Session() = %q, %v, want %q", id, ok, tc.wantSession)
			}
			if !reflect.DeepEqual(call.Argv(), argvOrNil(argv)) {
				t.Errorf("Argv() = %q, want %q", call.Argv(), argv)
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

// TestCallIsImmutable asserts that neither the caller's argv nor a returned argv
// reaches into the Call.
func TestCallIsImmutable(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		edit string
	}{
		{"utility call", []string{"claude", "auth", "status"}, "rm"},
		{"terminal session", []string{"claude", "--resume", uuid}, "--evil"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := append([]string(nil), tc.argv...)
			call := Classify(in)
			in[1] = tc.edit
			call.Argv()[1] = tc.edit
			if !reflect.DeepEqual(call.Argv(), tc.argv) {
				t.Errorf("Argv() = %q, want %q", call.Argv(), tc.argv)
			}
		})
	}
}

func TestParseSessionID(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		want   SessionID
		wantOK bool
	}{
		{"canonical lower case", uuid, uuid, true},
		{"canonical upper case", upper, upper, true},
		{"empty", "", "", false},
		{"too short", uuid[:35], "", false},
		{"too long", uuid + "0", "", false},
		{"no dashes", strings.ReplaceAll(uuid, "-", "") + "0000", "", false},
		{"dash misplaced", "0b7e5f3-c2a41-4c8e-9d6f-1e2a3b4c5d6e", "", false},
		{"non-hex digit", "0b7e5f3c-2a41-4c8e-9d6f-1e2a3b4c5d6g", "", false},
		{"path traversal", "../../../../../../../../../etc/passw", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, ok := ParseSessionID(tc.in)
			if id != tc.want || ok != tc.wantOK {
				t.Errorf("ParseSessionID(%q) = %q, %v, want %q, %v", tc.in, id, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestKindString(t *testing.T) {
	cases := []struct {
		kind Kind
		want string
	}{
		{NotAgentSession, "not an agent session"},
		{UtilityCall, "utility call"},
		{ChatSession, "chat session"},
		{TerminalSession, "terminal session"},
		{Kind(99), "unknown kind"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			if got := tc.kind.String(); got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}
