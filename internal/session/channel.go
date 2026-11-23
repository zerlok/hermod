package session

// Receiver is the incoming half of a connection. Messages arrive on a Go
// channel so that one loop can wait on the client and on every adapter at
// once.
type Receiver[T any] interface {
	// Receive is closed when the connection ends; Err then says why.
	Receive() <-chan T
	Err() error
}

// Sender is the outgoing half of a connection. Sending stays a call because
// it can fail.
type Sender[T any] interface {
	Send(msg T) error
}

// Channel is one side's connection to a chat: the client's, or an adapter's.
type Channel interface {
	Receiver[Message]
	Sender[Message]
}

// FuncSender is a Sender made of a function, which is how a chat is handed its
// way to the client.
type FuncSender[T any] func(msg T) error

func (fn FuncSender[T]) Send(msg T) error { return fn(msg) }

// TransformSender converts each message with F and sends it on to S.
type TransformSender[A, B any] struct {
	F func(A) B
	S Sender[B]
}

func (t TransformSender[A, B]) Send(msg A) error { return t.S.Send(t.F(msg)) }
