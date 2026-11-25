// Package agent declares the contract of an agent program as behaviour: the
// operations the core asks of it, and the values they answer with. An agent
// performs every operation itself, through the shell.Executor it is built with
// or the one it is handed to start a conversation, so one agent may run
// commands while another reads a database. It is handed a path map and
// executors, and never learns which workstation an executor reaches or how.
package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/zerlok/hermod/internal/env"
	"github.com/zerlok/hermod/internal/pathmap"
	"github.com/zerlok/hermod/internal/secret"
	"github.com/zerlok/hermod/internal/shell"
)

// Agent is one agent program's contract.
type Agent interface {
	// Classify says what the arguments ask for, and for a conversation its ID.
	Classify(args []string) (Invocation, string)
	// List is the conversations of a directory and of the directories under
	// it, as the program reports them.
	List(ctx context.Context, dir string) ([]Conversation, error)
	// Lookup is one conversation: its directory and state. A conversation
	// the program does not have is ErrUnknown.
	Lookup(ctx context.Context, id string) (Conversation, error)
	// Start starts or resumes the conversation the Launch's arguments name,
	// through ex, and returns once ex has started it.
	Start(ctx context.Context, ex shell.Executor, launch Launch) error
	// StateDir is the directory where the program keeps the state of dir's
	// conversations, such as their transcripts.
	StateDir(ctx context.Context, dir string) (string, error)
}

// ACP is the optional contract of an agent that serves a chat client through
// the program's ACP adapter.
type ACP interface {
	// Adapter starts the program's ACP adapter for the Launch, through ex.
	Adapter(ctx context.Context, ex shell.Executor, launch Launch) error
	// Connect is the answer to a client's connection start.
	Connect() Handshake
}

// ErrUnknown is a conversation the agent program does not have.
var ErrUnknown = errors.New("agent: the program has no such conversation")

// Invocation is what a command's arguments ask of an agent.
type Invocation int

const (
	// InvokesCommand is a command the agent does not run: it runs as given.
	// It is the zero value, so arguments nobody classified are never taken
	// for a conversation.
	InvokesCommand Invocation = iota
	// InvokesOneShot runs to completion without a conversation, such as a
	// subcommand or a version query.
	InvokesOneShot
	// InvokesConversation runs the conversation the program stores under an
	// ID.
	InvokesConversation
)

func (i Invocation) String() string {
	switch i {
	case InvokesCommand:
		return "command"
	case InvokesOneShot:
		return "one-shot"
	case InvokesConversation:
		return "conversation"
	}
	return fmt.Sprintf("Invocation(%d)", int(i))
}

// Launch is what a start is given.
type Launch struct {
	// Args are the caller's arguments, program first, with local paths.
	Args []string
	// Dir is the directory the caller works in, on the caller's workstation,
	// absolute: a relative path in Args is relative to it.
	Dir string
	// Paths maps the caller's paths to where the start runs.
	Paths pathmap.Map
	// Environ is the caller's environment; the agent takes from it only the
	// variables it declares, so a local credential never travels by default.
	Environ env.Vars
	// Hook rewrites one hook of the agent's settings, or leaves it out when
	// ok is false. Nil keeps every hook as it is.
	Hook func(h Hook) (rewritten Hook, ok bool)
}

// Hook is one hook of an agent's settings: a URL the program calls, and the
// headers it sends. A header may carry a credential, so each stays masked.
type Hook struct {
	URL     string
	Headers map[string]secret.Token
}

// State is what the agent program says of a conversation.
type State int

const (
	// Stopped was ended, or never ran: its transcript, if any, stays and can be
	// resumed. It is the zero value, as a conversation starts out.
	Stopped State = iota
	// Running is listed as running by the program: it works on the last
	// message or waits for the next one, whether or not anyone reads it.
	Running
)

func (s State) String() string {
	switch s {
	case Stopped:
		return "stopped"
	case Running:
		return "running"
	}
	return fmt.Sprintf("unknown state %d", int(s))
}

// Conversation is one conversation as the agent program reports it.
type Conversation struct {
	ID      string
	Dir     string // where it runs, absolute and clean, on the agent's workstation
	State   State
	Title   string
	Updated time.Time // its last update; zero when the program gives none
}
