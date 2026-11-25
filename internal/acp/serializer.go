package acp

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"

	"github.com/zerlok/hermod/internal/session"
)

// Serializer reads and writes the messages of a connection, each a type of
// its own.
type Serializer interface {
	// Decode parses one line that from wrote fully into the type of its
	// message, or rejects it with a *Rejection. asked is the method of a
	// request this side wrote under an ID, by which an answer is parsed.
	Decode(line []byte, from Peer, asked func(id session.MessageID) (method string, ok bool)) (session.Message, error)
	// Encode writes msg as one line, without its newline, a Result in the
	// protocol's terms. received is the method of a request the other side
	// wrote under an ID, by which an answer to it is written.
	Encode(msg session.Message, received func(id session.MessageID) (method string, ok bool)) ([]byte, error)
}

// Rejection is why a serializer read no message from a line, and what the
// connection does in its place. Any serializer builds one with MethodNotFound,
// InvalidParams or Dropped.
type Rejection struct {
	request session.MessageID // the request answered with code; zero for none
	code    int
	err     error
}

func (r *Rejection) Error() string { return "acp: " + r.err.Error() }
func (r *Rejection) Unwrap() error { return r.err }

// MethodNotFound rejects request, whose method the reading side does not have:
// it is answered with JSON-RPC's method not found.
func MethodNotFound(request session.MessageID, err error) *Rejection {
	return &Rejection{request: request, code: codeMethodNotFound, err: err}
}

// InvalidParams rejects request, whose params do not parse: it is answered
// with JSON-RPC's invalid params.
func InvalidParams(request session.MessageID, err error) *Rejection {
	return &Rejection{request: request, code: codeInvalidParams, err: err}
}

// Dropped rejects a line that is no request, which is logged and goes no
// further.
func Dropped(err error) *Rejection { return &Rejection{err: err} }

func drop(format string, args ...any) *Rejection { return Dropped(fmt.Errorf(format, args...)) }

// Method is one method a serializer reads: its name, the peers that send it,
// and the types of its messages.
type Method struct {
	name    string
	from    []Peer
	request bool
	call    func(id session.MessageID, params []byte) (session.Message, error)
	answer  func(id session.MessageID, result, failure []byte) (session.Message, error)
	// reply is the answer to request id with session's result, in the terms
	// of the method's own result.
	reply func(id session.MessageID, result session.Result) (wire, error)
}

// RequestOf is the method of requests with params P, answered with a result R,
// that from send.
func RequestOf[P Params, R Result](from ...Peer) Method {
	var p P
	return Method{
		name:    p.Method(),
		from:    from,
		request: true,
		call: func(id session.MessageID, b []byte) (session.Message, error) {
			var params P
			if err := decode(paramsOf(b), &params); err != nil {
				return nil, err
			}
			return Request[P, R]{id: id, Params: params}, nil
		},
		answer: func(id session.MessageID, result, failure []byte) (session.Message, error) {
			switch {
			case result != nil && failure == nil:
				var r R
				if err := decode(result, &r); err != nil {
					return nil, fmt.Errorf("result: %w", err)
				}
				return Response[P, R]{id: id, result: &r}, nil
			case failure != nil && result == nil:
				var e Error
				if err := decode(failure, &e); err != nil {
					return nil, fmt.Errorf("error: %w", err)
				}
				return Response[P, R]{id: id, failure: &e}, nil
			}
			return nil, errors.New("an answer has a result or an error, and not both")
		},
		reply: func(id session.MessageID, result session.Result) (wire, error) {
			replier, ok := any(*new(R)).(Replier[R])
			if !ok {
				return nil, fmt.Errorf("hermod: %s is not answered by hermod", p.Method())
			}
			answer, err := replier.FromSession(result)
			if err != nil {
				return nil, err
			}
			return Response[P, R]{id: id, result: &answer}, nil
		},
	}
}

// NotificationOf is the method of notifications with params P that from send.
func NotificationOf[P Params](from ...Peer) Method {
	var p P
	return Method{
		name: p.Method(),
		from: from,
		call: func(_ session.MessageID, b []byte) (session.Message, error) {
			var params P
			if err := decode(paramsOf(b), &params); err != nil {
				return nil, err
			}
			return Notification[P]{Params: params}, nil
		},
	}
}

// paramsOf is the params of a message, the empty object when it has none:
// JSON-RPC lets params be left out, and a type then reads as one with no
// members, which is rejected when it requires any.
func paramsOf(b []byte) []byte {
	if b == nil {
		return []byte("{}")
	}
	return b
}

// Standard is ACP's standard serializer: a Go type for each of ACP's
// requests, answers and notifications.
type Standard struct {
	methods map[string]Method
}

var _ Serializer = Standard{}

// NewStandard is the serializer of ACP's own messages.
func NewStandard() Standard { return Standard{}.With(standardMethods()...) }

// With is s reading methods as well. A method of a name s already reads
// replaces s's own, so a client may give a standard method a meaning of its
// own; s is left as it was.
func (s Standard) With(methods ...Method) Standard {
	all := maps.Clone(s.methods)
	if all == nil {
		all = map[string]Method{}
	}
	for _, m := range methods {
		all[m.name] = m
	}
	return Standard{methods: all}
}

func (s Standard) Decode(line []byte, from Peer, asked func(session.MessageID) (string, bool)) (session.Message, error) {
	var env envelope
	if err := json.Unmarshal(line, &env); err != nil {
		return nil, drop("not a JSON-RPC message: %w", err)
	}
	if err := exactMembers(line, reflect.TypeFor[envelope]()); err != nil {
		return nil, drop("not a JSON-RPC message: %w", err)
	}
	if env.JSONRPC != jsonrpcVersion {
		return nil, drop("not a JSON-RPC %s message: %.200s", jsonrpcVersion, line)
	}
	var raw requestID
	if env.ID != nil {
		if err := json.Unmarshal(env.ID, &raw); err != nil {
			return nil, drop("id: %w", err)
		}
	}
	id := session.MessageID(raw)
	if env.Method == "" {
		return s.decodeAnswer(env, id, asked)
	}
	// A request has an ID, and a notification none: a method of one kind
	// sent as the other is a method the sender does not have.
	m, ok := s.methods[env.Method]
	if !ok || !slices.Contains(m.from, from) || m.request == id.IsZero() {
		if !id.IsZero() {
			return nil, MethodNotFound(id, fmt.Errorf("method not found: no request %q from the %s", env.Method, from))
		}
		return nil, drop("no notification %q from the %s", env.Method, from)
	}
	msg, err := m.call(id, env.Params)
	if err != nil {
		if !id.IsZero() {
			return nil, InvalidParams(id, fmt.Errorf("invalid params of %q: %w", env.Method, err))
		}
		return nil, drop("the params of %q: %w", env.Method, err)
	}
	return msg, nil
}

func (s Standard) decodeAnswer(env envelope, id session.MessageID, asked func(session.MessageID) (string, bool)) (session.Message, error) {
	if id.IsZero() {
		return nil, drop("an answer that names no request: %s", env.Error)
	}
	method, ok := asked(id)
	if !ok {
		return nil, drop("an answer to %s, which this side did not ask", id)
	}
	m, ok := s.methods[method]
	if !ok || m.answer == nil {
		return nil, drop("an answer to %s, a %q this serializer does not read", id, method)
	}
	msg, err := m.answer(id, env.Result, env.Error)
	if err != nil {
		return nil, drop("the %s answer to %s: %w", method, id, err)
	}
	return msg, nil
}

// Encode writes any message: the messages an acp serializer reads as they
// are, session's answer to a request by the result type of the method it
// answers, and a message of another implementation by what it is to a session.
func (s Standard) Encode(msg session.Message, received func(session.MessageID) (string, bool)) ([]byte, error) {
	var f frame
	var err error
	switch m := msg.(type) {
	case reply:
		method, _ := received(m.id)
		f, err = s.answer(m.id, method, m.result, m.err)
	case response:
		// The request is answered, whatever the record says of it: an
		// answer that passes on is written as it was read.
		_, _ = received(m.ID())
		f, err = m.frame()
	case wire:
		f, err = m.frame()
	case nil:
		return nil, errors.New("acp: no message to write")
	default:
		f, err = s.foreign(msg, received)
	}
	if err != nil {
		return nil, fmt.Errorf("acp: %w", err)
	}
	f.JSONRPC = jsonrpcVersion
	line, err := marshal(f)
	if err != nil {
		return nil, fmt.Errorf("acp: encode a message: %w", err)
	}
	return line, nil
}

// foreign is msg, a Message no acp serializer made, as the one message of
// ACP's that a session's view of a message names: an answer, of an answer's
// Kind, to a request the other side wrote under msg's ID, written from its
// Result. A request or a notification of another implementation names no
// method and has no form on the wire.
func (s Standard) foreign(msg session.Message, received func(session.MessageID) (string, bool)) (frame, error) {
	if kind := msg.Kind(); kind != session.Pass && kind != session.Answer {
		return frame{}, fmt.Errorf("a %T of kind %s names no method of ACP's", msg, kind)
	}
	method, ok := received(msg.ID())
	if !ok {
		return frame{}, fmt.Errorf("a %T answers %s, no request the other side wrote", msg, msg.ID())
	}
	result := msg.Result()
	if result == nil {
		return s.answer(msg.ID(), method, nil, errors.New("hermod: the answer holds no result"))
	}
	return s.answer(msg.ID(), method, result, nil)
}

// answer is the answer to request id of method with result, or with err. A
// Result that the method's result cannot hold, or an answer to a request
// whose method is not known, is written as an error answer, so the requester
// still hears back.
func (s Standard) answer(id session.MessageID, method string, result session.Result, err error) (frame, error) {
	if id.IsZero() {
		return frame{}, errors.New("an answer names no request")
	}
	if err == nil {
		m, ok := s.methods[method]
		switch {
		case method == "":
			err = fmt.Errorf("hermod: %s is no request the other side wrote", id)
		case !ok || m.reply == nil:
			err = fmt.Errorf("hermod: %s is not answered by hermod", method)
		default:
			answer, replyErr := m.reply(id, result)
			if replyErr == nil {
				return answer.frame()
			}
			err = replyErr
		}
	}
	rid := requestID(id)
	return frame{ID: &rid, Error: &Error{Code: codeInternal, Message: err.Error()}}, nil
}
