package draft

type Receiver[T any] interface {
	// Receive is closed when the connection ends; Err then says why.
	Receive() <-chan T
	Err() error
}

type Sender[T any] interface {
	Send(T) error
}

// Channel is one side's connection. Incoming messages arrive on a Go
// channel, so one loop can wait on the client and every adapter at once;
// sending stays a call because it can fail.
type Channel interface {
	Receiver[Message]
	Sender[Message]
}

type FuncSender[T any] func(T) error

func (fn FuncSender[T]) Send(msg T) error { return fn(msg) }

type TransformSender[A, B any] struct {
	F func(A) (B, error)
	S Sender[B]
}

func (t *TransformSender[A, B]) Send(msg A) error {
	m, err := t.F(msg)
	if err != nil {
		return err
	}
	return t.S.Send(m)
}
