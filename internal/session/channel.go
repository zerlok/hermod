package session

import (
	"context"
	"errors"
)

// Receiver is the incoming half of a connection. Messages arrive on a Go
// channel so that one loop can wait on the client and on every adapter at
// once.
type Receiver[T any] interface {
	// Receive is closed when the connection ends; Err then says why.
	Receive() <-chan T
	Err() error
}

// Caller is the outgoing half of a connection. A request is a call that
// returns its answer, so nothing in session matches an answer to its request.
type Caller interface {
	// Call sends a request at once, after everything sent before it, and
	// returns its answer, or the error the other side answered with, to
	// returned, once. returned runs where the connection is read, after every
	// message that arrived before the answer has been taken from Receive and
	// before any that arrived after it is delivered, so the answer keeps its
	// place among the other side's messages. When ctx ends first, the request
	// is cancelled on the wire and returned gets ctx's error. An error Call
	// itself returns is a request it did not send; returned is then never
	// called.
	Call(ctx context.Context, msg Message, returned func(Message, error)) error
	// Notify sends a notification, or says why it could not.
	Notify(msg Message) error
}

// Channel is one side's connection to a chat: the client's, or an adapter's.
type Channel interface {
	Receiver[Inbound]
	Caller
}

// Inbound is what arrives on a Channel: a message and, for a request, the
// context it is served under, which ends when the sender cancels the request
// or the connection ends, and its Answer. A notification has neither.
type Inbound struct {
	Message Message
	Context context.Context
	Answer  Answer
}

// Answer answers one request that arrived, once.
type Answer interface {
	// Return answers with the other side's answer, which a Call returned.
	Return(msg Message) error
	// Reply answers with a Result that session answers itself.
	Reply(result Result) error
	Fail(err error) error
}

// mappedCaller passes every message on to c with its paths taken through out,
// and returns every answer with its paths taken through back.
type mappedCaller struct {
	c         Caller
	out, back func(Path) Path
}

// mappedError is an error the other side answered with that names paths,
// such as in its data: it crosses back mapped, as an answer does.
type mappedError interface {
	error
	Mapped(path func(Path) Path) error
}

func (m mappedCaller) Call(ctx context.Context, msg Message, returned func(Message, error)) error {
	return m.c.Call(ctx, msg.Mapped(m.out), func(answer Message, err error) {
		if e, ok := errors.AsType[mappedError](err); ok {
			err = e.Mapped(m.back)
		}
		if err != nil {
			returned(nil, err)
			return
		}
		returned(answer.Mapped(m.back), nil)
	})
}

func (m mappedCaller) Notify(msg Message) error { return m.c.Notify(msg.Mapped(m.out)) }

// answer answers in with what a call returned; a notification has no answer.
func answer(in Inbound, msg Message, err error) error {
	if in.Answer == nil {
		return nil
	}
	return respond(in.Answer, msg, err)
}

// respond answers with what a call returned: its answer, or its error.
func respond(a Answer, msg Message, err error) error {
	if err != nil {
		return a.Fail(err)
	}
	return a.Return(msg)
}
