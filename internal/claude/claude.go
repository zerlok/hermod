// Package claude is Claude Code's contract as an agent.Agent: which call an
// argv asks for, what running it or Claude's ACP adapter needs on the remote,
// how to ask Claude which conversations exist and which run, and how its hook
// settings are aimed at a worker's port. It follows Claude's command line and
// files and changes when they do; it runs no command, and reads a local file
// only through the reader it is handed.
package claude

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"github.com/zerlok/hermod/internal/agent"
)

// Config is what Claude's contract needs to know of the workstations.
type Config struct {
	// Dir is the local directory a conversation run on a terminal works in,
	// absolute: Claude names its transcripts' directory after it, and a
	// relative path in the arguments is relative to it.
	Dir string
	// ConfigDir is Claude's local config directory, absolute.
	ConfigDir string
	// RemoteConfigDir is Claude's config directory on the remote, absolute:
	// the one Claude there uses, `${CLAUDE_CONFIG_DIR:-$HOME/.claude}` in the
	// remote's environment. The transcripts' place and the asking for
	// conversations both use it, so a conversation listed is one the relay
	// keeps.
	RemoteConfigDir string
	// ReadFile reads a local file that `--settings` or `--mcp-config` names,
	// whose contents the worker is given in place of the path.
	ReadFile func(name string) ([]byte, error)
}

// Agent is Claude Code's contract.
type Agent struct {
	cfg Config
}

var _ agent.Agent = Agent{}

// New is Claude's contract for the workstations cfg describes.
func New(cfg Config) Agent { return Agent{cfg: cfg} }

// oneShotSubcommands are Claude's subcommands. Each runs to completion without
// a conversation, so none is a conversation whatever else its arguments say.
var oneShotSubcommands = map[string]bool{
	"agents": true, "attach": true, "auth": true, "auto-mode": true, "config": true,
	"doctor": true, "gateway": true, "import": true, "install": true, "logs": true,
	"mcp": true, "migrate-installer": true, "plugin": true, "plugins": true,
	"purge": true, "respawn": true, "rm": true, "setup-token": true, "stop": true,
	"kill": true, "ultrareview": true, "update": true, "upgrade": true,
}

// Parse reads a command's argv. A command is Claude's when its program's base
// name is `claude`; any other command is not the agent's.
//
//   - A subcommand, `--version` or `--help`, or `-p` with no conversation is a
//     one-shot call. Claude reads a subcommand only as its first argument, so a
//     later word is a prompt or an option's value.
//   - `--session-id <uuid>` or `--resume <uuid>` is a conversation, whatever
//     its input format: one driven over stream-json is still one conversation.
//     With `--fork-session`, Claude writes the resumed conversation under a new
//     UUID, so only `--session-id` names it; a fork without one is keyed by no
//     UUID known in advance.
//   - Anything else, including `--resume` with no UUID (Claude's own picker),
//     runs as given.
func (Agent) Parse(argv []string) agent.Call {
	if len(argv) == 0 || path.Base(argv[0]) != "claude" {
		return agent.NewCommand(argv)
	}
	a := scan(argv[1:])
	id := a.key()
	switch {
	case a.subcommand || a.info || (a.print && id == ""):
		return agent.NewOneShot(argv)
	case id != "":
		// The ID is a parsed UUID, never empty here, so the call is built.
		call, _ := agent.NewConversation(id, argv)
		return call
	}
	return agent.NewCommand(argv)
}

// args is what Parse needs from Claude's arguments.
type args struct {
	subcommand bool
	info       bool // --version or --help: Claude prints and exits
	print      bool
	sessionID  string // from --session-id
	resume     string // from --resume <uuid>
	fork       bool   // --fork-session: the resumed conversation gets a new UUID
}

// key is the UUID the conversation Claude writes is stored under:
// --session-id when given, else the resumed one's own, unless a fork gives it
// a new one.
func (a args) key() string {
	switch {
	case a.sessionID != "":
		return a.sessionID
	case a.fork:
		return ""
	default:
		return a.resume
	}
}

// scan reads Claude's arguments, stopping at `--`, after which every word is a
// prompt. An option's value is read in both the `--opt value` and `--opt=value`
// forms.
func scan(words []string) args {
	var a args
	if len(words) > 0 {
		a.subcommand = oneShotSubcommands[words[0]]
	}
	for i := 0; i < len(words); i++ {
		name, value, inline := splitOption(words[i])
		// next is the option's value: the inline one, else the next word.
		next := func() string {
			if inline {
				return value
			}
			if i+1 < len(words) {
				i++
				return words[i]
			}
			return ""
		}
		switch name {
		case "--":
			return a
		case "-v", "--version", "-h", "--help":
			a.info = true
		case "-p", "--print":
			a.print = true
		case "--session-id":
			if v := next(); isSessionID(v) {
				a.sessionID = v
			}
		case "--fork-session":
			a.fork = true
		case "-r", "--resume":
			// --resume takes an optional value: a UUID resumes that
			// conversation, and anything else is Claude's picker or a search
			// term, which is left to Claude, so a following word that is no
			// UUID stays unread.
			if inline {
				if isSessionID(value) {
					a.resume = value
				}
			} else if i+1 < len(words) && isSessionID(words[i+1]) {
				i++
				a.resume = words[i]
			}
		}
	}
	return a
}

// splitOption reads a word as an option in the `--opt=value` form: a word that
// starts with '-' and holds an '=' is the option before the first '=' and its
// value after. Any other word is whole, as a prompt, a path or a JSON value
// holding an '=' is not an option.
func splitOption(word string) (name, value string, inline bool) {
	if !strings.HasPrefix(word, "-") {
		return word, "", false
	}
	return strings.Cut(word, "=")
}

// isSessionID accepts a UUID in its canonical 8-4-4-4-12 hexadecimal form,
// the only form Claude stores a conversation under. Only such an ID is safe as
// a file name and a tmux session name.
func isSessionID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !isHex(r) {
				return false
			}
		}
	}
	return true
}

func isHex(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
}

// agentsCommand lists the conversations Claude runs, each with its directory.
var agentsCommand = []string{"claude", "agents", "--json"}

// Probe asks `claude agents --json`, which lists exactly the conversations
// Claude runs.
func (Agent) Probe(id string) agent.Probe {
	return agent.NewProbe(id, agentsCommand, func(out []byte) (agent.State, error) {
		running, err := readAgents(out)
		if err != nil {
			return agent.Stopped, err
		}
		if _, ok := running[strings.ToLower(id)]; ok {
			return agent.Running, nil
		}
		return agent.Stopped, nil
	})
}

// running is one entry of `claude agents --json`.
type running struct {
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd"`
	Name      string `json:"name"`
	StartedAt int64  `json:"startedAt"` // milliseconds since the epoch
}

// readAgents reads `claude agents --json` by lower-cased conversation ID:
// Claude writes IDs in lower case, while a caller may give one in upper case.
func readAgents(out []byte) (map[string]running, error) {
	var list []running
	if err := json.Unmarshal(out, &list); err != nil {
		return nil, fmt.Errorf("claude agents --json: %w", err)
	}
	byID := make(map[string]running, len(list))
	for _, r := range list {
		byID[strings.ToLower(r.SessionID)] = r
	}
	return byID, nil
}

// acpVersion is the ACP protocol version claude-agent-acp speaks.
const acpVersion = 1

// acpCapabilities are the agent capabilities claude-agent-acp answers
// `initialize` with: images and embedded context in prompts, MCP servers over
// HTTP and SSE, and loading, listing, resuming, forking and closing sessions.
const acpCapabilities = `{
  "loadSession": true,
  "promptCapabilities": {"image": true, "embeddedContext": true},
  "mcpCapabilities": {"http": true, "sse": true},
  "sessionCapabilities": {"close": {}, "fork": {}, "list": {}, "resume": {}}
}`

// Connect is claude-agent-acp's own answer to a connection start, so a client
// is answered before any adapter runs.
func (Agent) Connect() agent.Handshake {
	h, err := agent.NewHandshake(acpVersion, []byte(acpCapabilities))
	if err != nil {
		panic(fmt.Sprintf("claude: the declared ACP capabilities are invalid: %v", err))
	}
	return h
}
