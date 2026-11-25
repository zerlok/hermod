package session

import (
	"fmt"
	"strconv"

	"github.com/zerlok/hermod/internal/agent"
	"github.com/zerlok/hermod/internal/pathmap"
)

// Kind is what a chat message means to a session. A Channel gives each message
// its kind; session never reads a wire format or a client's own method names.
type Kind int

const (
	// Pass has nothing to decide: the message goes to the other side. It is
	// the zero Kind, so a message nobody classified is only carried.
	Pass Kind = iota
	// Connect is the client starting its connection. session answers it from
	// the agent and keeps it for every adapter it starts.
	Connect
	// List is the client asking for the chats of a directory. session answers
	// it from the agent program.
	List
	// Cancel is a side cancelling one of its own requests: the one ID gives.
	// The client cancels a request it sent, an adapter a question it asked.
	Cancel
	// Open is the client opening a chat in a directory, or returning to one.
	Open
	// Suspend is the client no longer showing a chat, which keeps running.
	// session answers it and never passes it on: an adapter that received it
	// would end the chat.
	Suspend
	// Close is the client ending a chat: its agent stops.
	Close
	// Question is a remote asking the client, expecting an Answer.
	Question
	// Answer is the client answering a remote's Question.
	Answer
)

func (k Kind) String() string {
	switch k {
	case Pass:
		return "pass"
	case Connect:
		return "connect"
	case List:
		return "list"
	case Cancel:
		return "cancel"
	case Open:
		return "open"
	case Suspend:
		return "suspend"
	case Close:
		return "close"
	case Question:
		return "question"
	case Answer:
		return "answer"
	}
	return fmt.Sprintf("kind(%d)", int(k))
}

// MessageID is a request's ID on one connection, as the side that made the
// request wrote it: a number or a text. The two never equal each other, even
// when they read alike, because a side matches an answer to its request by the
// ID exactly as it wrote it. The zero MessageID is none: the ID of a message
// that is no request and answers none.
type MessageID struct {
	form   idForm
	number int64
	text   string
}

type idForm uint8

const (
	noID idForm = iota
	numberID
	textID
)

// NumberID is the ID n, written as a number.
func NumberID(n int64) MessageID { return MessageID{form: numberID, number: n} }

// TextID is the ID s, written as a text; the empty text is an ID too.
func TextID(s string) MessageID { return MessageID{form: textID, text: s} }

// Number is the ID's number, when it was written as one.
func (id MessageID) Number() (int64, bool) { return id.number, id.form == numberID }

// Text is the ID's text, when it was written as one.
func (id MessageID) Text() (string, bool) { return id.text, id.form == textID }

// IsZero reports whether the ID is none.
func (id MessageID) IsZero() bool { return id.form == noID }

// String is the ID for a log line: a number as its digits, a text quoted.
func (id MessageID) String() string {
	switch id.form {
	case numberID:
		return strconv.FormatInt(id.number, 10)
	case textID:
		return strconv.Quote(id.text)
	}
	return "none"
}

// ChatID is a chat's ID, the agent program's own ID of the conversation.
type ChatID string

// Path is a directory or file a message names, on the workstation the message
// is on its way to or from.
type Path string

// Message is one message of a chat connection, with what routing needs. A
// Message is a value: each With method returns a new one.
type Message interface {
	Kind() Kind
	// ID is the request this message is, answers, or, for a Cancel, cancels.
	ID() MessageID
	// Chat is the chat it belongs to; empty for none.
	Chat() ChatID
	// Dir is the directory it names; empty for none.
	Dir() Path
	// WithID is the same message under another request ID.
	WithID(id MessageID) Message
	// Mapped is the same message with every path in it taken through path.
	Mapped(path func(Path) Path) Message
	// Reply is the answer to this request with result, for a request session
	// answers itself. Only a request has an answer: session replies to a
	// message only when its ID is not zero.
	Reply(result Result) Message
	// Result is the Result this answer carries; nil for a message that is no
	// answer, or whose answer is none of the Results.
	Result() Result
	// Fail is an error answer to this request, as Reply is.
	Fail(err error) Message
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
