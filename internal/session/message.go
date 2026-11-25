package session

import (
	"fmt"

	"github.com/zerlok/hermod/internal/agent"
	"github.com/zerlok/hermod/internal/pathmap"
)

// Kind is what a chat message means to a session. A Channel gives each message
// its kind; session never reads a wire format or a client's own method names.
// A cancellation is no message: it is the end of a request's context.
type Kind int

const (
	// Pass has nothing to decide: a notification goes to the other side, and
	// a request becomes a call there whose answer is returned. It is the zero
	// Kind, so a message nobody classified is only carried.
	Pass Kind = iota
	// Connect is the client starting its connection. session answers it from
	// the agent and keeps it for every adapter it starts.
	Connect
	// List is the client asking for the chats of a directory. session answers
	// it from the agent program.
	List
	// Open is the client opening a chat in a directory, or returning to one.
	Open
	// Suspend is the client no longer showing a chat, which keeps running.
	// session answers it and never passes it on: an adapter that received it
	// would end the chat.
	Suspend
	// Close is the client ending a chat: its agent stops.
	Close
)

func (k Kind) String() string {
	switch k {
	case Pass:
		return "pass"
	case Connect:
		return "connect"
	case List:
		return "list"
	case Open:
		return "open"
	case Suspend:
		return "suspend"
	case Close:
		return "close"
	}
	return fmt.Sprintf("kind(%d)", int(k))
}

// ChatID is a chat's ID, the agent program's own ID of the conversation.
type ChatID string

// Path is a directory or file a message names, on the workstation the message
// is on its way to or from.
type Path string

// Message is one message of a chat connection, with what routing needs. A
// Message is a value: Mapped returns a new one.
type Message interface {
	Kind() Kind
	// Chat is the chat it belongs to; empty for none.
	Chat() ChatID
	// Dir is the directory it names; empty for none.
	Dir() Path
	// Mapped is the same message with every path in it taken through path.
	Mapped(path func(Path) Path) Message
	// Result is the Result this answer carries; nil for a message that is no
	// answer, or whose answer is none of the Results.
	Result() Result
}

// Result is what session answers a request with, or reads from an answer: a
// plain value, which the channel writes in its protocol's terms. It is closed:
// EmptyResult, HandshakeResult and ListResult are all there is.
type Result interface{ isResult() }

// EmptyResult answers a request that has nothing to return, such as a Suspend.
type EmptyResult struct{}

// HandshakeResult answers a Connect.
type HandshakeResult struct{ Handshake agent.Handshake }

// ListResult answers a List: the conversations of the directory asked, their
// directories on the client's workstation.
type ListResult struct{ Conversations []agent.Conversation }

func (EmptyResult) isResult()     {}
func (HandshakeResult) isResult() {}
func (ListResult) isResult()      {}

// PathMap maps paths between the two workstations.
type PathMap interface {
	ToRemote(p Path) Path
	ToLocal(p Path) Path
}

// NewPathMap is m as a PathMap.
func NewPathMap(m pathmap.Map) PathMap { return pathMap{m: m} }

type pathMap struct{ m pathmap.Map }

func (p pathMap) ToRemote(path Path) Path { return Path(p.m.ToRemote(string(path))) }
func (p pathMap) ToLocal(path Path) Path  { return Path(p.m.ToLocal(string(path))) }
