// Package acp is the Agent Client Protocol on the wire: JSON-RPC messages, one
// per line. A Conn reads each line through the Serializer it is built with,
// which parses it fully into the type of its message or rejects it, and writes
// any message the same way. Standard is ACP's own serializer, which a client's
// serializer may extend with the client's own messages. acp routes nothing
// itself.
package acp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"sync"

	"github.com/zerlok/hermod/internal/session"
)

// Conn is one side's ACP connection over a pair of streams. It reads until its
// input ends and writes one message per line.
type Conn struct {
	serializer Serializer
	peer       Peer // who writes what the connection reads
	log        *log.Logger

	received chan session.Message
	done     chan struct{}
	stop     sync.Once
	err      error // why reading ended; set before received is closed

	mu  sync.Mutex // one message per line, however many senders
	out io.Writer

	requests *requests
}

// requests are the requests each side wrote to the other, the method of each
// by its ID, until it is answered: an answer is read, or written, by the type
// of the request it answers. They have a lock of their own: the reader
// records a request while a sender may be blocked writing to a peer that
// waits to be read.
type requests struct {
	mu       sync.Mutex
	asked    map[session.MessageID]string // by this side
	received map[session.MessageID]string // by the peer
}

func newRequests() *requests {
	return &requests{asked: map[session.MessageID]string{}, received: map[session.MessageID]string{}}
}

func (r *requests) ask(id session.MessageID, method string) { r.put(r.asked, id, method) }
func (r *requests) receive(id session.MessageID, method string) {
	r.put(r.received, id, method)
}

// unask forgets a request that was never written.
func (r *requests) unask(id session.MessageID) { _, _ = r.take(r.asked, id) }

// answered is the method of the request id this side asked; reading its
// answer takes the entry, since a request has one answer.
func (r *requests) answered(id session.MessageID) (string, bool) { return r.take(r.asked, id) }

// answering is the method of the request id the peer asked; writing its
// answer takes the entry.
func (r *requests) answering(id session.MessageID) (string, bool) { return r.take(r.received, id) }

func (r *requests) put(m map[session.MessageID]string, id session.MessageID, method string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m[id] = method
}

func (r *requests) take(m map[session.MessageID]string, id session.MessageID) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	method, ok := m[id]
	delete(m, id)
	return method, ok
}

var _ session.Channel = (*Conn)(nil)

// NewClient is the connection to a chat client, which reads what the client
// writes through serializer and logs the lines it rejects.
func NewClient(in io.Reader, out io.Writer, serializer Serializer, logger *log.Logger) *Conn {
	return newConn(in, out, Client, serializer, newRequests(), logger)
}

// Adapters connects to agents' ACP adapters. Hermod is an adapter's client,
// and the agent runs on the remote: the initialize a connection writes offers
// none of the client capabilities that act on the client's own workstation,
// its files and its terminals, so the agent reads, writes and runs everything
// where it runs. An adapter runs on between the connections to it and may
// answer on one what was asked over an earlier one, so the requests of each
// adapter, by its key, outlive a connection.
type Adapters[K comparable] struct {
	serializer Serializer
	log        *log.Logger

	mu       sync.Mutex
	requests map[K]*requests
}

// NewAdapters connects to adapters through serializer, and logs the lines it
// rejects.
func NewAdapters[K comparable](serializer Serializer, logger *log.Logger) *Adapters[K] {
	return &Adapters[K]{serializer: serializer, log: logger, requests: map[K]*requests{}}
}

// Connect is a connection to the adapter of key, reading what it writes from
// in. started is whether the adapter has just started: a new process answers
// nothing asked of the one before it.
func (a *Adapters[K]) Connect(key K, started bool, in io.Reader, out io.Writer) *Conn {
	a.mu.Lock()
	r, ok := a.requests[key]
	if !ok || started {
		r = newRequests()
		a.requests[key] = r
	}
	a.mu.Unlock()
	return newConn(in, out, Agent, a.serializer, r, a.log)
}

func newConn(in io.Reader, out io.Writer, peer Peer, serializer Serializer, r *requests, logger *log.Logger) *Conn {
	c := &Conn{
		serializer: serializer,
		peer:       peer,
		log:        logger,
		received:   make(chan session.Message),
		done:       make(chan struct{}),
		out:        out,
		requests:   r,
	}
	go c.read(in)
	return c
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

// Send writes msg as one line. A request is recorded before it is written,
// since its answer may be read before the write returns, so that the answer
// is read by its type.
func (c *Conn) Send(msg session.Message) error {
	if c.peer == Agent {
		msg = withoutClientCapabilities(msg)
	}
	line, err := c.serializer.Encode(msg, c.requests.answering)
	if err != nil {
		return err
	}
	r, asks := msg.(requester)
	if !asks {
		return c.write(line)
	}
	id, method := r.asks()
	c.requests.ask(id, method)
	if err := c.write(line); err != nil {
		c.requests.unask(id)
		return err
	}
	return nil
}

func (c *Conn) write(line []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.out.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("acp: send: %w", err)
	}
	return nil
}

// read delivers each line of in that parses as a message until in ends. A
// final line without its newline is delivered when it is a whole message, and
// is the error the connection ends with when it is cut inside one.
func (c *Conn) read(in io.Reader) {
	defer close(c.received)
	r := bufio.NewReader(in)
	for {
		line, readErr := r.ReadBytes('\n')
		if line = bytes.TrimSpace(line); len(line) > 0 {
			if readErr != nil && !json.Valid(line) {
				c.fail(fmt.Errorf("acp: the connection ended inside a message: %.200s", line))
				return
			}
			msg, err := c.serializer.Decode(line, c.peer, c.requests.answered)
			if err != nil {
				c.reject(err)
			} else {
				if r, ok := msg.(requester); ok {
					c.requests.receive(r.asks())
				}
				if !c.deliver(msg) {
					return
				}
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

// deliver hands msg to the reader; false once the connection is stopped.
func (c *Conn) deliver(msg session.Message) bool {
	// A select picks at random among ready cases, so a stop that has come is
	// seen before a reader waiting for the message.
	select {
	case <-c.done:
		return false
	default:
	}
	select {
	case c.received <- msg:
		return true
	case <-c.done:
		return false
	}
}

// reject logs every rejected line and answers a rejected request with its
// error: nothing the serializer did not parse reaches the other side.
func (c *Conn) reject(err error) {
	c.log.Printf("dropped a line from the %s: %v", c.peer, err)
	var r *Rejection
	if !errors.As(err, &r) || r.request.IsZero() {
		return
	}
	id := requestID(r.request)
	line, encErr := marshal(frame{JSONRPC: jsonrpcVersion, ID: &id, Error: &Error{Code: r.code, Message: r.err.Error()}})
	if encErr == nil {
		encErr = c.write(line)
	}
	if encErr != nil {
		c.log.Printf("acp: could not answer the rejected request %s: %v", r.request, encErr)
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
