// Package claude is Claude Code's command-line contract as pure functions: which
// kind of run an argv asks for and which session it names, how an argv, a
// settings document and an environment are carried to the sandbox, and where
// Claude keeps a session's transcript. It follows Claude's CLI and changes when
// that does; it reads no files and runs nothing.
package claude

import (
	"path"
	"strings"
)

// Kind is what a command asks Hermod to run.
type Kind int

const (
	// NotAgentSession is any command that is not one of the kinds below,
	// including a command that is not claude. It runs as a plain attach. It is
	// the zero value, so an unclassified command is never taken for a session.
	NotAgentSession Kind = iota
	// UtilityCall is a short run with no session, such as `claude auth status`,
	// `claude --version` or a `-p` settings read. It runs straight on the sandbox.
	UtilityCall
	// ChatSession is a session driven over stream-json on stdin and stdout, as
	// the Agent SDK runs one.
	ChatSession
	// TerminalSession is an interactive session in a terminal.
	TerminalSession
)

func (k Kind) String() string {
	switch k {
	case NotAgentSession:
		return "not an agent session"
	case UtilityCall:
		return "utility call"
	case ChatSession:
		return "chat session"
	case TerminalSession:
		return "terminal session"
	}
	return "unknown kind"
}

// SessionID is a Claude session's UUID, the key for its transcript and for
// everything Hermod keeps for it. It is only ever built from a well-formed UUID,
// so it is safe as a file name and a tmux session name.
type SessionID string

// ParseSessionID accepts a UUID in its canonical 8-4-4-4-12 hexadecimal form.
func ParseSessionID(s string) (SessionID, bool) {
	if len(s) != 36 {
		return "", false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return "", false
			}
		default:
			if !isHex(r) {
				return "", false
			}
		}
	}
	return SessionID(s), true
}

func (id SessionID) String() string { return string(id) }

// Call is a classified command. It is built only by Classify, so a session ID is
// present exactly when the kind is a session.
type Call struct {
	kind    Kind
	session SessionID
	resumes bool      // the session is named by --resume: the caller expects it to exist
	resumed SessionID // the session --resume names, the call's own or a fork's source
	argv    []string
}

// Kind is what the command asks for.
func (c Call) Kind() Kind { return c.kind }

// Session is the session a chat or terminal session is keyed by; ok is false for
// every other kind.
func (c Call) Session() (id SessionID, ok bool) { return c.session, c.session != "" }

// Resumes reports whether the caller asked to resume the session the call is
// keyed by, rather than to start one under its UUID. A fork resumes another
// session and starts its own, so it does not count.
func (c Call) Resumes() bool { return c.resumes }

// Resumed is the session `--resume <uuid>` names: the call's own, or in a
// fork, the session the new one starts from. ok is false without a UUID to
// resume.
func (c Call) Resumed() (id SessionID, ok bool) { return c.resumed, c.resumed != "" }

// Argv is the command as given, program first.
func (c Call) Argv() []string { return append([]string(nil), c.argv...) }

// utilitySubcommands are Claude's subcommands. Each runs to completion without a
// conversation, so none is a session whatever else its arguments say.
var utilitySubcommands = map[string]bool{
	"agents": true, "attach": true, "auth": true, "auto-mode": true, "config": true,
	"doctor": true, "gateway": true, "import": true, "install": true, "logs": true,
	"mcp": true, "migrate-installer": true, "plugin": true, "plugins": true,
	"purge": true, "respawn": true, "rm": true, "setup-token": true, "stop": true,
	"kill": true, "ultrareview": true, "update": true, "upgrade": true,
}

// Classify reads a command's argv. A command is claude when its program's base
// name is `claude`; any other command is not an agent session.
//
//   - A subcommand, `--version` or `--help`, or `-p` with no session is a utility
//     call. Claude reads a subcommand only as its first argument, so a later word
//     is a prompt or an option's value.
//   - `--session-id <uuid>` or `--resume <uuid>` makes a session: a chat session
//     with `--input-format stream-json`, a terminal session otherwise. With
//     `--fork-session`, Claude writes the resumed conversation under a new UUID,
//     so only `--session-id` names the session; a fork without one is keyed by
//     no UUID known in advance.
//   - Anything else, including `--resume` with no UUID (Claude's own picker), is
//     not an agent session.
func Classify(argv []string) Call {
	call := Call{argv: append([]string(nil), argv...)}
	if len(argv) == 0 || path.Base(argv[0]) != "claude" {
		return call
	}
	a := scan(argv[1:])
	session := a.key()
	switch {
	case a.subcommand || a.info || (a.print && session == ""):
		call.kind = UtilityCall
	case session != "" && a.inputFormat == "stream-json":
		call.kind, call.session = ChatSession, session
	case session != "":
		call.kind, call.session = TerminalSession, session
	}
	call.resumes = call.session != "" && a.sessionID == ""
	if call.session != "" {
		call.resumed = a.resume
	}
	return call
}

// args is what Classify needs from Claude's arguments.
type args struct {
	subcommand  bool
	info        bool // --version or --help: Claude prints and exits
	print       bool
	sessionID   SessionID // from --session-id
	resume      SessionID // from --resume <uuid>
	fork        bool      // --fork-session: the resumed conversation gets a new UUID
	inputFormat string
}

// key is the UUID the session Claude writes is stored under: --session-id when
// given, else the resumed session's own, unless a fork gives it a new one.
func (a args) key() SessionID {
	switch {
	case a.sessionID != "":
		return a.sessionID
	case a.fork:
		return ""
	default:
		return a.resume
	}
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

// scan reads Claude's arguments, stopping at `--`, after which every word is a
// prompt. An option's value is read in both the `--opt value` and `--opt=value`
// forms.
func scan(argv []string) args {
	var a args
	if len(argv) > 0 {
		a.subcommand = utilitySubcommands[argv[0]]
	}
	for i := 0; i < len(argv); i++ {
		name, value, inline := splitOption(argv[i])
		// next is the option's value: the inline one, else the next word.
		next := func() string {
			if inline {
				return value
			}
			if i+1 < len(argv) {
				i++
				return argv[i]
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
		case "--input-format":
			a.inputFormat = next()
		case "--session-id":
			if id, ok := ParseSessionID(next()); ok {
				a.sessionID = id
			}
		case "--fork-session":
			a.fork = true
		case "-r", "--resume":
			// --resume takes an optional value: a UUID resumes that session, and
			// anything else is Claude's picker or a search term, which is left to
			// Claude, so a following word that is no UUID stays unread.
			if inline {
				if id, ok := ParseSessionID(value); ok {
					a.resume = id
				}
			} else if i+1 < len(argv) {
				if id, ok := ParseSessionID(argv[i+1]); ok {
					a.resume = id
					i++
				}
			}
		}
	}
	return a
}

func isHex(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
}
