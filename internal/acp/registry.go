package acp

import (
	"encoding/json"
	"fmt"

	"github.com/zerlok/hermod/internal/session"
)

// Peer is the side of a connection that sends a message: a chat client, or
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

// Registry types every method a Conn carries: a typed handler for each, which
// reads its params into the method's Go type and checks them with that type's
// Validate, and, for a request, reads its answer the same way.
type Registry struct {
	methods map[string]method
}

type method struct {
	from    Peer // the side that sends it
	request bool
	params  func(b []byte) (session.Message, error)
	result  func(b []byte) (session.Message, error) // a request's only
}

// NewRegistry is a registry of no method; Standard is ACP's own.
func NewRegistry() *Registry { return &Registry{methods: map[string]method{}} }

// Request registers the request name, which from sends with params P and is
// answered with a result R. A method of a name r already has replaces it.
func Request[P, R any](r *Registry, name string, from Peer) {
	r.methods[name] = method{from: from, request: true, params: reader[P](name), result: reader[R](name)}
}

// Notification registers the notification name, which from sends with params
// P.
func Notification[P any](r *Registry, name string, from Peer) {
	r.methods[name] = method{from: from, params: reader[P](name)}
}

// reader reads a message of method name as a T. JSON-RPC lets params be left
// out, and a type then reads as one with no members, which its Validate
// rejects when it requires any.
func reader[T any](name string) func(b []byte) (session.Message, error) {
	return func(b []byte) (session.Message, error) {
		if b == nil {
			b = []byte("{}")
		}
		var v T
		if err := decode(b, &v); err != nil {
			return nil, err
		}
		return message[T]{method: name, value: v}, nil
	}
}

// message is one of the messages a registry reads: the params of a request or
// a notification, or the result of an answer, as their method's type.
type message[T any] struct {
	method string
	value  T
}

// wire is a message of acp's, as a Conn writes it.
type wire interface {
	session.Message
	name() string
	params() any
}

// The parts of a type that say something to a session. A type of ACP's has
// them when it means more than Pass, names a chat or a directory, or reads as
// a session.Result.
type (
	kinder   interface{ Kind() session.Kind }
	chatter  interface{ Chat() session.ChatID }
	dirNamer interface{ Dir() session.Path }
	resulter interface{ toSession() (session.Result, bool) }
)

func (m message[T]) name() string { return m.method }
func (m message[T]) params() any  { return m.value }

func (m message[T]) Kind() session.Kind {
	if k, ok := any(m.value).(kinder); ok {
		return k.Kind()
	}
	return session.Pass
}

func (m message[T]) Chat() session.ChatID {
	if c, ok := any(m.value).(chatter); ok {
		return c.Chat()
	}
	return ""
}

func (m message[T]) Dir() session.Path {
	if d, ok := any(m.value).(dirNamer); ok {
		return d.Dir()
	}
	return ""
}

func (m message[T]) Result() session.Result {
	if r, ok := any(m.value).(resulter); ok {
		if result, ok := r.toSession(); ok {
			return result
		}
	}
	return nil
}

func (m message[T]) Mapped(path func(session.Path) session.Path) session.Message {
	if p, ok := any(&m.value).(pather); ok {
		p.mapPaths(mapperOf(path))
	}
	return m
}

func mapperOf(path func(session.Path) session.Path) mapper {
	return func(s string) string { return string(path(session.Path(s))) }
}

// Error is a JSON-RPC error answer: what a Call returns when the other side
// answered with one, and what an Answer's Fail writes.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    Value  `json:"data,omitzero"`
}

func (e *Error) Error() string { return fmt.Sprintf("acp: %s (%d)", e.Message, e.Code) }

// Mapped is the error with the strings of its data taken through path: an
// error answer crosses to the other side mapped, as any answer does.
func (e *Error) Mapped(path func(session.Path) session.Path) error {
	mapped := *e
	mapped.mapPaths(mapperOf(path))
	return &mapped
}

func (e *Error) mapPaths(m mapper) { m.value(&e.Data) }

// JSON-RPC's error codes, and ACP's for a cancelled request.
const (
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternal       = -32603
	codeCancelled      = -32800
)

// frame is one JSON-RPC message as it is read and written. A request has a
// method and an ID, a notification a method and no ID, and an answer an ID and
// a result or an error.
type frame struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

const jsonrpcVersion = "2.0"
