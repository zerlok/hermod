// Package acp is the Agent Client Protocol on the wire: JSON-RPC messages, one
// per line. A Conn reads each message into the session.Message it is routed
// by, giving it its Kind from ACP's shape and standard methods, and a client's
// own methods from the table it is given, and writes the answers session gives
// in ACP's terms. It routes nothing itself.
package acp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"sync"

	"github.com/zerlok/hermod/internal/session"
)

// Conn is one side's ACP connection over a pair of streams. It reads until its
// input ends and writes one message per line.
type Conn struct {
	received chan session.Message
	done     chan struct{}
	stop     sync.Once
	err      error // why reading ended; set before received is closed

	mu  sync.Mutex // one message per line, however many senders
	out io.Writer
}

var _ session.Channel = (*Conn)(nil)

// NewClient is the connection to a chat client, which reads what the client
// sends. A client's own methods take their Kind from methods; the standard
// ACP methods take theirs from acp whatever methods says.
func NewClient(in io.Reader, out io.Writer, methods map[string]session.Kind) *Conn {
	own := maps.Clone(methods)
	return newConn(in, out, func(env envelope) session.Kind { return clientKind(env, own) })
}

// NewAdapter is the connection to an agent's ACP adapter, which reads what the
// adapter sends.
func NewAdapter(in io.Reader, out io.Writer) *Conn {
	return newConn(in, out, adapterKind)
}

func newConn(in io.Reader, out io.Writer, kind func(envelope) session.Kind) *Conn {
	c := &Conn{received: make(chan session.Message), done: make(chan struct{}), out: out}
	go c.read(in, kind)
	return c
}

// clientKind is what a client's message means to a session. An answer from
// the client can only answer an adapter's question: the client's own requests
// are answered by the adapter or by session.
func clientKind(env envelope, own map[string]session.Kind) session.Kind {
	switch {
	case env.Method == "":
		return session.Answer
	case env.Method == methodCancel:
		return session.Cancel
	}
	if kind, ok := standard[env.Method]; ok {
		return kind
	}
	if kind, ok := own[env.Method]; ok {
		return kind
	}
	return session.Pass
}

// adapterKind is what an adapter's message means to a session: every request
// of an adapter asks the client.
func adapterKind(env envelope) session.Kind {
	switch {
	case env.Method == "":
		return session.Pass
	case env.Method == methodCancel:
		return session.Cancel
	case present(env.ID):
		return session.Question
	}
	return session.Pass
}

func (c *Conn) Receive() <-chan session.Message { return c.received }

// Err is why the connection ended once Receive is closed: nil when its input
// ended between messages, or when Close stopped it.
func (c *Conn) Err() error { return c.err }

// Close stops delivering messages; Receive is closed once the reading under
// way returns. The streams are the caller's and stay open.
func (c *Conn) Close() error {
	c.stop.Do(func() { close(c.done) })
	return nil
}

// Send writes msg as one line. It takes only a message a Conn made: session
// hands a channel the messages it received or the answers they gave.
func (c *Conn) Send(msg session.Message) error {
	m, ok := msg.(message)
	if !ok {
		return fmt.Errorf("acp: cannot write a %T", msg)
	}
	if m.silent {
		return nil
	}
	var line bytes.Buffer
	enc := json.NewEncoder(&line)
	// Paths and prose may hold '&', '<' or '>', which the default encoder
	// escapes for HTML; the stream is not HTML, so they go as written.
	enc.SetEscapeHTML(false)
	if err := enc.Encode(m.env); err != nil {
		return fmt.Errorf("acp: encode a message: %w", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.out.Write(line.Bytes()); err != nil {
		return fmt.Errorf("acp: send: %w", err)
	}
	return nil
}

// read delivers each line of in as a message until in ends. A final line
// without its newline is delivered when it is a whole message, and is the
// error the connection ends with when it is cut inside one.
func (c *Conn) read(in io.Reader, kind func(envelope) session.Kind) {
	defer close(c.received)
	r := bufio.NewReader(in)
	for {
		line, readErr := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			env, err := parse(line)
			switch {
			case err != nil && readErr != nil:
				c.fail(fmt.Errorf("acp: the connection ended inside a message: %w", err))
				return
			case err != nil:
				c.fail(err)
				return
			}
			// A select picks at random among ready cases, so a stop that has
			// come is seen before a reader waiting for the message.
			select {
			case <-c.done:
				return
			default:
			}
			select {
			case c.received <- message{env: env, kind: kind(env)}:
			case <-c.done:
				return
			}
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				c.fail(fmt.Errorf("acp: read: %w", readErr))
			}
			return
		}
	}
}

// fail records why reading ended, unless Close stopped the connection first:
// its owner closes the streams after it, and their failing then is no fault
// of the connection.
func (c *Conn) fail(err error) {
	select {
	case <-c.done:
	default:
		c.err = err
	}
}
