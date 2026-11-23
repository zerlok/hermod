// Package agent declares the contract of an agent program: the questions the
// core asks about a conversation, and the values that answer them. An agent
// performs nothing: every command it declares is run by its caller, on the
// remote, and read back with the reading the agent declared beside it. An agent
// is handed a path map and never learns where the other workstation is or how
// it is reached.
package agent

import (
	"fmt"
	"path"
	"time"

	"github.com/zerlok/hermod/internal/pathmap"
	"github.com/zerlok/hermod/internal/secret"
)

// Agent is one agent program's contract. Each method is a question answered
// with a value.
type Agent interface {
	// Parse says what the arguments ask for: a conversation and its ID, a
	// one-shot call, or a command the agent does not run.
	Parse(args []string) Call
	// Run says what running a call needs on the remote, its paths mapped by
	// paths.
	Run(call Call, paths pathmap.Map) (Needs, error)
	// Adapter says what the program's ACP adapter needs on the remote, for the
	// workspace paths maps, which decides where the program keeps the
	// workspace's conversations.
	Adapter(paths pathmap.Map) (Needs, error)
	// Probe says how to ask the agent program whether a conversation runs.
	Probe(id string) Probe
	// Connect is the answer to a client's connection start.
	Connect() Handshake
	// List says how to ask the agent program for the conversations of a remote
	// directory and of the directories under it.
	List(dir string) Listing
	// Locate says how to ask the agent program where one conversation belongs.
	Locate(id string) Lookup
	// Hooks rewrites a settings document so that its hooks call back on a port
	// of the remote and carry a token generated for the worker. The rewritten
	// document holds that token, so it stays a secret on both sides.
	Hooks(document secret.Token, port int, token secret.Token) (secret.Token, error)
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

func (s State) check() error {
	switch s {
	case Stopped, Running:
		return nil
	}
	return fmt.Errorf("agent: %v", s)
}

// Conversation is one conversation as the agent program reports it.
type Conversation struct {
	ID      string
	Dir     string // where it runs, a path on the remote
	State   State
	Title   string
	Updated time.Time // its last update; zero when the program gives none
}

func (c Conversation) check() error {
	if c.ID == "" {
		return fmt.Errorf("agent: a conversation in %q has no ID", c.Dir)
	}
	// Sessions and the router match a directory by its exact string, and
	// pathmap.Under trusts both paths to be clean, so a directory written any
	// other way would name one place while matching another.
	if !path.IsAbs(c.Dir) || path.Clean(c.Dir) != c.Dir {
		return fmt.Errorf("agent: conversation %s has no absolute, clean directory: %q", c.ID, c.Dir)
	}
	return c.State.check()
}
