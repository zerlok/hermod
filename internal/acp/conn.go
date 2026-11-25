// Package acp is the Agent Client Protocol on the wire: a JSON-RPC 2.0
// connection, one message per line, that knows no method, and a Registry that
// gives each of ACP's messages a Go type, reads it fully into that type or
// rejects it. acp routes nothing itself.
package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"strconv"
	"sync"

	"github.com/zerlok/hermod/internal/session"
)

// cancelMethod is the notification either side cancels one of its requests
// with. A Conn sends and serves it itself: it is the end of a request's
// context, never a message.
const cancelMethod = "$/cancel_request"

// Conn is one side's ACP connection over a pair of streams. Each hop owns its
// IDs: a Call numbers its request from this connection's own counter and
// returns the answer under that ID when it arrives, and a request that arrives
// is answered under its ID on this connection only.
type Conn struct {
	reg  *Registry
	peer Peer // the side that writes what the connection reads
	log  *log.Logger

	ctx context.Context // ends with the connection
	end context.CancelFunc

	received chan session.Inbound
	err      error // why reading ended; set before received is closed

	wmu sync.Mutex // one message per line, however many writers
	out io.Writer

	mu     sync.Mutex
	next   int64
	calls  map[int64]*call     // this side's requests, until returned
	ended  bool                // every call was returned errEnded, and none is taken
	served map[string]*request // the peer's requests, by their ID's key
}

// call is one of this side's requests waiting for its answer.
type call struct {
	w        wire
	m        method
	returned func(session.Message, error)
	stop     func() bool // ends the wait for the call's context; nil until set
}

var _ session.Channel = (*Conn)(nil)

// errEnded is a call whose connection ended before its answer.
var errEnded = errors.New("acp: the connection ended")

// NewConn is the connection to peer over in and out, carrying the methods of
// reg. It ends when in ends or ctx does. A connection to an agent's adapter
// makes Hermod the adapter's client, and the agent runs on the remote: the
// initialize it writes offers none of the client capabilities that act on the
// client's own workstation, its files and its terminals.
func NewConn(ctx context.Context, in io.Reader, out io.Writer, reg *Registry, peer Peer, logger *log.Logger) *Conn {
	ctx, end := context.WithCancel(ctx)
	c := &Conn{
		reg:      reg,
		peer:     peer,
		log:      logger,
		ctx:      ctx,
		end:      end,
		received: make(chan session.Inbound),
		out:      out,
		// An adapter's line stream outlives each connection to it, so an
		// answer to an earlier connection's request may arrive on this one:
		// numbering from a random start keeps it from matching one of ours.
		next:   rand.Int64N(1 << 52),
		calls:  map[int64]*call{},
		served: map[string]*request{},
	}
	// A reader blocked on input that never comes would hold the calls
	// waiting past the connection's end.
	context.AfterFunc(ctx, c.endCalls)
	go c.read(in)
	return c
}

func (c *Conn) Receive() <-chan session.Inbound { return c.received }

// Err is why the connection ended once Receive is closed: nil when its input
// ended between messages, or when its context ended.
func (c *Conn) Err() error { return c.err }

// Call writes msg as a request at once, and hands its answer, read by the
// result type of its method, to returned when it arrives: on the reading
// goroutine, after every message read before it has been taken from Receive,
// and before any read after it is delivered. A Call whose ctx ends first
// cancels the request on the wire and returns ctx's error; one whose
// connection ends returns errEnded. An error Call itself returns is a request
// that was not written, and returned is not called.
func (c *Conn) Call(ctx context.Context, msg session.Message, returned func(session.Message, error)) error {
	w, m, err := c.method(msg, true)
	if err != nil {
		return err
	}
	if init, ok := w.(message[InitializeParams]); ok && c.peer == Agent {
		w = withoutClientCapabilities(init)
	}
	c.mu.Lock()
	if c.ended {
		c.mu.Unlock()
		return errEnded
	}
	c.next++
	id := c.next
	cl := &call{w: w, m: m, returned: returned}
	c.calls[id] = cl
	c.mu.Unlock()
	rawID := strconv.AppendInt(nil, id, 10)
	if err := c.write(frame{ID: rawID, Method: w.name()}, w.params()); err != nil {
		if c.take(id) == nil {
			return nil // the connection's end returned it already
		}
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.calls[id] == cl {
		cl.stop = context.AfterFunc(ctx, func() {
			if c.take(id) == nil {
				return // its answer won
			}
			if err := c.write(frame{Method: cancelMethod}, cancelParams{RequestID: rawID}); err != nil {
				c.log.Printf("acp: cancel %s %d: %v", w.name(), id, err)
			}
			returned(nil, ctx.Err())
		})
	}
	return nil
}

// take removes the call id from those waiting, for the one who returns it;
// nil when it was returned already.
func (c *Conn) take(id int64) *call {
	c.mu.Lock()
	defer c.mu.Unlock()
	cl, ok := c.calls[id]
	if !ok {
		return nil
	}
	delete(c.calls, id)
	if cl.stop != nil {
		cl.stop()
	}
	return cl
}

// endCalls returns every call waiting errEnded, and refuses those that come
// after.
func (c *Conn) endCalls() {
	c.mu.Lock()
	waiting := c.calls
	c.calls, c.ended = map[int64]*call{}, true
	c.mu.Unlock()
	for _, cl := range waiting {
		if cl.stop != nil {
			cl.stop()
		}
		cl.returned(nil, errEnded)
	}
}

// result is what a call returns for an answer that has result, or err.
func (cl *call) result(result []byte, err error) (session.Message, error) {
	if err != nil {
		return nil, err
	}
	msg, err := cl.m.result(result)
	if err != nil {
		return nil, fmt.Errorf("acp: the answer to %s: %w", cl.w.name(), err)
	}
	return msg, nil
}

// Notify writes msg as a notification.
func (c *Conn) Notify(msg session.Message) error {
	w, _, err := c.method(msg, false)
	if err != nil {
		return err
	}
	return c.write(frame{Method: w.name()}, w.params())
}

// method is msg as acp writes it, with its method, which r registers as a
// request or as a notification as asked.
func (c *Conn) method(msg session.Message, request bool) (wire, method, error) {
	w, ok := msg.(wire)
	if !ok {
		return nil, method{}, fmt.Errorf("acp: a %T is no message of acp's", msg)
	}
	m, ok := c.reg.methods[w.name()]
	if !ok || m.request != request {
		return nil, method{}, fmt.Errorf("acp: %q is registered as no %s", w.name(), kindOf(request))
	}
	return w, m, nil
}

// cancelParams are the params of $/cancel_request.
type cancelParams struct {
	RequestID json.RawMessage `json:"requestId"`
	Extensible
}

// write writes f as one line, with v as its params, result or error.
func (c *Conn) write(f frame, v any) error {
	f.JSONRPC = jsonrpcVersion
	if v != nil {
		b, err := marshal(v)
		if err != nil {
			return fmt.Errorf("acp: encode %T: %w", v, err)
		}
		if f.Method != "" {
			f.Params = b
		} else {
			f.Result = b
		}
	}
	line, err := marshal(f)
	if err != nil {
		return fmt.Errorf("acp: encode a message: %w", err)
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if _, err := c.out.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("acp: send: %w", err)
	}
	return nil
}

// read serves each line of in until in ends. A final line without its newline
// is served when it is a whole message, and is the error the connection ends
// with when it is cut inside one.
func (c *Conn) read(in io.Reader) {
	r := bufio.NewReader(in)
	for {
		line, readErr := r.ReadBytes('\n')
		if line = bytes.TrimSpace(line); len(line) > 0 {
			if readErr != nil && !json.Valid(line) {
				c.close(fmt.Errorf("acp: the connection ended inside a message: %.200s", line))
				return
			}
			if !c.serve(line) {
				c.close(nil)
				return
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				readErr = nil
			} else {
				readErr = fmt.Errorf("acp: read: %w", readErr)
			}
			c.close(readErr)
			return
		}
	}
}

// close ends the connection's context, and every call waiting and every
// served request's context with it. The calls are returned before Receive
// closes, so whoever reads both hears of them before the end.
func (c *Conn) close(err error) {
	if c.ctx.Err() == nil {
		c.err = err
	}
	c.endCalls()
	c.end()
	close(c.received)
}

// serve handles one line: an answer to one of this side's calls, a cancel, or
// a message to deliver. It is false once the connection's context ended.
func (c *Conn) serve(line []byte) bool {
	var f frame
	if err := json.Unmarshal(line, &f); err != nil || f.JSONRPC != jsonrpcVersion {
		c.log.Printf("acp: dropped a line from the %s that is no JSON-RPC %s message: %.200s", c.peer, jsonrpcVersion, line)
		return true
	}
	key, isRequest, err := idKey(f.ID)
	switch {
	case err != nil:
		c.log.Printf("acp: dropped a message from the %s: %v", c.peer, err)
	case f.Method == "":
		c.answered(f)
	case f.Method == cancelMethod && !isRequest:
		c.cancelled(f.Params)
	default:
		return c.arrived(f, key, isRequest)
	}
	return true
}

// idKey is a request's ID as a key that tells a number from a string, and
// whether there is one: JSON-RPC's null ID names no request.
func idKey(id json.RawMessage) (key string, ok bool, err error) {
	if id == nil || isNull(id) {
		return "", false, nil
	}
	var v any
	if err := decodeNumbers(id, &v); err != nil {
		return "", false, err
	}
	switch v := v.(type) {
	case string:
		return "s" + v, true, nil
	case json.Number:
		if _, err := v.Int64(); err == nil {
			return "n" + v.String(), true, nil
		}
	}
	return "", false, fmt.Errorf("an ID is an integer or a string, not %s", id)
}

// answered returns an answer to the call that waits for it, on the reading
// goroutine, so it keeps its place among the messages read around it.
func (c *Conn) answered(f frame) {
	var cl *call
	if id, err := strconv.ParseInt(string(f.ID), 10, 64); err == nil {
		cl = c.take(id)
	}
	var err error
	switch {
	case cl == nil:
		c.log.Printf("acp: dropped an answer from the %s to %s, which this side does not wait for", c.peer, f.ID)
		return
	case f.Error != nil && f.Result == nil:
		err = f.Error
	case f.Result == nil || f.Error != nil:
		err = errors.New("acp: an answer has a result or an error, and not both")
	}
	cl.returned(cl.result(f.Result, err))
}

// cancelled ends the context of the peer's request that params name. The
// request is still answered.
func (c *Conn) cancelled(params json.RawMessage) {
	var p cancelParams
	err := decode(params, &p)
	key, ok, idErr := idKey(p.RequestID)
	if err != nil || idErr != nil || !ok {
		c.log.Printf("acp: dropped a cancel from the %s of no request: %s", c.peer, params)
		return
	}
	c.mu.Lock()
	r, ok := c.served[key]
	c.mu.Unlock()
	if ok {
		r.cancel()
	}
}

// arrived reads a request or a notification by its method and delivers it.
// A request whose method has no handler is answered -32601, and one whose
// params do not read -32602; a notification either way is dropped.
func (c *Conn) arrived(f frame, key string, isRequest bool) bool {
	m, ok := c.reg.methods[f.Method]
	if !ok || m.from != c.peer || m.request != isRequest {
		return c.reject(f, isRequest, codeMethodNotFound, fmt.Errorf("method not found: no %s %q from the %s", kindOf(isRequest), f.Method, c.peer))
	}
	msg, err := m.params(f.Params)
	if err != nil {
		return c.reject(f, isRequest, codeInvalidParams, fmt.Errorf("invalid params of %q: %w", f.Method, err))
	}
	in := session.Inbound{Message: msg}
	if isRequest {
		ctx, cancel := context.WithCancel(c.ctx)
		r := &request{c: c, id: f.ID, key: key, cancel: cancel}
		c.mu.Lock()
		c.served[key] = r
		c.mu.Unlock()
		in.Context, in.Answer = ctx, r
	}
	select {
	case c.received <- in:
		return true
	case <-c.ctx.Done():
		return false
	}
}

func kindOf(isRequest bool) string {
	if isRequest {
		return "request"
	}
	return "notification"
}

// reject logs a message that is not delivered, and answers it when it is a
// request: nothing undecoded reaches the other side.
func (c *Conn) reject(f frame, isRequest bool, code int, err error) bool {
	c.log.Printf("acp: dropped a message from the %s: %v", c.peer, err)
	if isRequest {
		if err := c.write(frame{ID: f.ID, Error: &Error{Code: code, Message: err.Error()}}, nil); err != nil {
			c.log.Printf("acp: could not answer the rejected request %s: %v", f.ID, err)
		}
	}
	return true
}

// request is a request that arrived, the session.Answer that answers it under
// its ID.
type request struct {
	c        *Conn
	id       json.RawMessage
	key      string
	cancel   context.CancelFunc
	answered bool // under c.mu
}

func (r *request) Return(msg session.Message) error {
	w, ok := msg.(wire)
	if !ok {
		return r.Fail(fmt.Errorf("hermod: a %T is no answer of acp's", msg))
	}
	return r.finish(frame{}, w.params())
}

func (r *request) Reply(result session.Result) error {
	v, err := fromSession(result)
	if err != nil {
		return r.Fail(err)
	}
	return r.finish(frame{}, v)
}

// Fail answers with err: an error the other side answered a call with as it
// was, a cancelled call as ACP's cancelled request, and any other as an
// internal error.
func (r *request) Fail(err error) error {
	var e *Error
	switch {
	case errors.As(err, &e):
	case errors.Is(err, context.Canceled):
		e = &Error{Code: codeCancelled, Message: "request cancelled"}
	default:
		e = &Error{Code: codeInternal, Message: err.Error()}
	}
	return r.finish(frame{Error: e}, nil)
}

// finish writes the answer, once. A request whose connection has ended has
// nobody left to answer.
func (r *request) finish(f frame, v any) error {
	r.c.mu.Lock()
	done := r.answered
	r.answered = true
	if r.c.served[r.key] == r {
		delete(r.c.served, r.key)
	}
	r.c.mu.Unlock()
	r.cancel()
	switch {
	case done:
		return fmt.Errorf("acp: request %s is answered already", r.id)
	case r.c.ctx.Err() != nil:
		return nil
	}
	f.ID = r.id
	return r.c.write(f, v)
}
