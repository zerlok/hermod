package acp

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/zerlok/hermod/internal/session"
)

const jsonrpcVersion = "2.0"

// JSON-RPC's error codes that Hermod answers with.
const (
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternal       = -32603
)

// Peer is the side of a connection that writes a message: a chat client, or
// an agent's ACP adapter. ACP gives each side its own methods.
type Peer uint8

const (
	Client Peer = iota + 1
	Agent
)

func (p Peer) String() string {
	switch p {
	case Client:
		return "client"
	case Agent:
		return "agent"
	}
	return fmt.Sprintf("peer(%d)", uint8(p))
}

// requestID is a session.MessageID as JSON-RPC writes it: a number or a
// string.
type requestID session.MessageID

func (id requestID) MarshalJSON() ([]byte, error) {
	m := session.MessageID(id)
	if n, ok := m.Number(); ok {
		return strconv.AppendInt(nil, n, 10), nil
	}
	if s, ok := m.Text(); ok {
		return marshal(s)
	}
	return []byte("null"), nil
}

func (id *requestID) UnmarshalJSON(b []byte) error {
	if isNull(b) {
		*id = requestID{}
		return nil
	}
	var s string
	if json.Unmarshal(b, &s) == nil {
		*id = requestID(session.TextID(s))
		return nil
	}
	var n int64
	if err := json.Unmarshal(b, &n); err != nil {
		return fmt.Errorf("an ID is an integer or a string, not %s", b)
	}
	*id = requestID(session.NumberID(n))
	return nil
}

// envelope is what tells one JSON-RPC message from another: a request, a
// notification or an answer, and its method or ID. The member that holds the
// rest is parsed next, into the type that the method, or the request an answer
// answers, names.
type envelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Result  json.RawMessage `json:"result"`
	Error   json.RawMessage `json:"error"`
}

// frame is one JSON-RPC message as it is written.
type frame struct {
	JSONRPC string     `json:"jsonrpc"`
	ID      *requestID `json:"id,omitempty"`
	Method  string     `json:"method,omitempty"`
	Params  any        `json:"params,omitempty"`
	Result  any        `json:"result,omitempty"`
	Error   *Error     `json:"error,omitempty"`
}

// Error is the error of an error answer.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    Value  `json:"data,omitzero"`
}

// Params is the params of one of the protocol's methods: the method's name,
// and what a message of it means to a session. A type of params answers from
// its zero value too.
type Params interface {
	// Method is the method's name on the wire.
	Method() string
	Kind() session.Kind
	Chat() session.ChatID
	Dir() session.Path
}

// Result is the result of the answer to one of the protocol's requests.
type Result interface {
	// Chat is the chat the answer names, as the answer to session/new names
	// the chat it made.
	Chat() session.ChatID
}

// Replier is a Result that session can answer a request with, built from
// session's own Result. The serializer writes a reply to a request through
// it; a request whose Result is no Replier is never answered by Hermod.
type Replier[R Result] interface {
	FromSession(r session.Result) (R, error)
}

// reader is a Result that reads as one of session's own Results.
type reader interface {
	toSession() (session.Result, bool)
}

// wire is a message acp writes.
type wire interface {
	session.Message
	frame() (frame, error)
}

// requester is a message that asks the other side. A connection records the
// method asked under its ID, so that the answer is read, or written, by the
// type of the request it answers.
type requester interface {
	asks() (id session.MessageID, method string)
}

// Request is a request of the method its params P name, answered with a
// Response[P, R].
type Request[P Params, R Result] struct {
	id     session.MessageID
	Params P
}

// NewRequest is the request id of params.
func NewRequest[P Params, R Result](id session.MessageID, params P) Request[P, R] {
	return Request[P, R]{id: id, Params: params}
}

func (r Request[P, R]) Kind() session.Kind     { return r.Params.Kind() }
func (r Request[P, R]) ID() session.MessageID  { return r.id }
func (r Request[P, R]) Chat() session.ChatID   { return r.Params.Chat() }
func (r Request[P, R]) Dir() session.Path      { return r.Params.Dir() }
func (r Request[P, R]) Result() session.Result { return nil }

func (r Request[P, R]) WithID(id session.MessageID) session.Message {
	r.id = id
	return r
}

func (r Request[P, R]) Mapped(path func(session.Path) session.Path) session.Message {
	r.Params = mapped(r.Params, path)
	return r
}

func (r Request[P, R]) Reply(result session.Result) session.Message {
	return reply{id: r.id, kind: answerKind[P](), result: result}
}

func (r Request[P, R]) Fail(err error) session.Message {
	return reply{id: r.id, kind: answerKind[P](), err: err}
}

func (r Request[P, R]) frame() (frame, error) {
	if r.id.IsZero() {
		return frame{}, fmt.Errorf("a %s request has no ID", r.Params.Method())
	}
	id := requestID(r.id)
	return frame{ID: &id, Method: r.Params.Method(), Params: r.Params}, nil
}

func (r Request[P, R]) asks() (session.MessageID, string) { return r.id, r.Params.Method() }

// Notification is a notification of the method its params P name.
type Notification[P Params] struct {
	Params P
}

// canceller is the params of a notification that cancels a request, whose ID
// is the request it cancels.
type canceller[P Params] interface {
	cancels() session.MessageID
	cancelling(id session.MessageID) P
}

func (n Notification[P]) Kind() session.Kind     { return n.Params.Kind() }
func (n Notification[P]) Chat() session.ChatID   { return n.Params.Chat() }
func (n Notification[P]) Dir() session.Path      { return n.Params.Dir() }
func (n Notification[P]) Result() session.Result { return nil }

func (n Notification[P]) ID() session.MessageID {
	if c, ok := any(n.Params).(canceller[P]); ok {
		return c.cancels()
	}
	return session.MessageID{}
}

func (n Notification[P]) WithID(id session.MessageID) session.Message {
	if c, ok := any(n.Params).(canceller[P]); ok {
		n.Params = c.cancelling(id)
	}
	return n
}

func (n Notification[P]) Mapped(path func(session.Path) session.Path) session.Message {
	n.Params = mapped(n.Params, path)
	return n
}

func (n Notification[P]) Reply(session.Result) session.Message {
	return unanswered{of: n.Params.Method()}
}

func (n Notification[P]) Fail(error) session.Message { return unanswered{of: n.Params.Method()} }

func (n Notification[P]) frame() (frame, error) {
	return frame{Method: n.Params.Method(), Params: n.Params}, nil
}

// response is an answer of ACP's to a request the other side wrote.
type response interface {
	wire
	answers()
}

// Response is the answer to a Request[P, R]: its result, or an error.
type Response[P Params, R Result] struct {
	id      session.MessageID
	result  *R
	failure *Error
}

// Answer is the response's result, or its error when it has none.
func (r Response[P, R]) Answer() (R, *Error) {
	if r.result == nil {
		var zero R
		return zero, r.failure
	}
	return *r.result, nil
}

func (r Response[P, R]) Kind() session.Kind { return answerKind[P]() }

func (Response[P, R]) answers() {}

// answerKind is the Kind of an answer to a request of params P: Answer for the
// client's answer to a remote's question, and Pass for an adapter's answer to
// the client's request, which goes to the client.
func answerKind[P Params]() session.Kind {
	var request P
	if request.Kind() == session.Question {
		return session.Answer
	}
	return session.Pass
}

func (r Response[P, R]) ID() session.MessageID { return r.id }
func (r Response[P, R]) Dir() session.Path     { return "" }

func (r Response[P, R]) Chat() session.ChatID {
	if r.result == nil {
		return ""
	}
	return (*r.result).Chat()
}

func (r Response[P, R]) WithID(id session.MessageID) session.Message {
	r.id = id
	return r
}

func (r Response[P, R]) Mapped(path func(session.Path) session.Path) session.Message {
	if r.result != nil {
		result := mapped(*r.result, path)
		r.result = &result
	}
	if r.failure != nil {
		failure := mapped(*r.failure, path)
		r.failure = &failure
	}
	return r
}

func (r Response[P, R]) Result() session.Result {
	if r.result == nil {
		return nil
	}
	if read, ok := any(*r.result).(reader); ok {
		if result, ok := read.toSession(); ok {
			return result
		}
	}
	return nil
}

func (r Response[P, R]) Reply(session.Result) session.Message { return unanswered{of: "an answer"} }
func (r Response[P, R]) Fail(error) session.Message           { return unanswered{of: "an answer"} }

func (r Response[P, R]) frame() (frame, error) {
	id := requestID(r.id)
	f := frame{ID: &id}
	switch {
	case r.result != nil:
		f.Result = *r.result
	case r.failure != nil:
		f.Error = r.failure
	default:
		return frame{}, errors.New("an answer has neither a result nor an error")
	}
	return f, nil
}

// reply is session's answer to a request: a plain Result of session's, or an
// error, which the serializer writes in the terms of the method answered, as
// the connection recorded it.
type reply struct {
	id     session.MessageID
	kind   session.Kind
	result session.Result // nil with err
	err    error
}

func (r reply) Kind() session.Kind     { return r.kind }
func (r reply) ID() session.MessageID  { return r.id }
func (r reply) Chat() session.ChatID   { return "" }
func (r reply) Dir() session.Path      { return "" }
func (r reply) Result() session.Result { return r.result }

func (r reply) WithID(id session.MessageID) session.Message {
	r.id = id
	return r
}

// Mapped maps the directories of the conversations a ListResult holds, the
// one Result that names paths.
func (r reply) Mapped(path func(session.Path) session.Path) session.Message {
	l, ok := r.result.(session.ListResult)
	if !ok {
		return r
	}
	conversations := slices.Clone(l.Conversations)
	for i := range conversations {
		conversations[i].Dir = string(path(session.Path(conversations[i].Dir)))
	}
	r.result = session.ListResult{Conversations: conversations}
	return r
}

func (r reply) Reply(session.Result) session.Message { return unanswered{of: "an answer"} }
func (r reply) Fail(error) session.Message           { return unanswered{of: "an answer"} }

// unanswered is the answer to a message that is no request. JSON-RPC answers
// only a request, and session replies only to a message with an ID, so
// writing it is an error.
type unanswered struct{ of string }

func (u unanswered) Kind() session.Kind                                     { return session.Pass }
func (u unanswered) ID() session.MessageID                                  { return session.MessageID{} }
func (u unanswered) Chat() session.ChatID                                   { return "" }
func (u unanswered) Dir() session.Path                                      { return "" }
func (u unanswered) WithID(session.MessageID) session.Message               { return u }
func (u unanswered) Mapped(func(session.Path) session.Path) session.Message { return u }
func (u unanswered) Reply(session.Result) session.Message                   { return u }
func (u unanswered) Result() session.Result                                 { return nil }
func (u unanswered) Fail(error) session.Message                             { return u }

func (u unanswered) frame() (frame, error) {
	return frame{}, fmt.Errorf("%s has no answer", u.of)
}
