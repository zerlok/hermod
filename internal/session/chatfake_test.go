package session

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/zerlok/hermod/internal/agent"
	"github.com/zerlok/hermod/internal/workspace"
)

// fakeMsg is a chat message as a Channel would give it. name tells a table's
// messages apart: a request's answers and calls are written under it.
type fakeMsg struct {
	name string
	kind Kind
	chat ChatID
	dir  Path
	// The handshake an adapter's answer to a Connect gives; no capabilities
	// is none.
	version int
	caps    string
}

func (m fakeMsg) Kind() Kind   { return m.kind }
func (m fakeMsg) Chat() ChatID { return m.chat }
func (m fakeMsg) Dir() Path    { return m.dir }

func (m fakeMsg) Mapped(path func(Path) Path) Message {
	if m.dir != "" {
		m.dir = path(m.dir)
	}
	return m
}

func (m fakeMsg) Result() Result {
	if m.caps == "" {
		return nil
	}
	h, err := agent.NewHandshake(m.version, []byte(m.caps))
	if err != nil {
		return nil
	}
	return HandshakeResult{Handshake: h}
}

// String writes a message as a table states it: its name, its chat and its
// directory.
func (m fakeMsg) String() string {
	parts := []string{m.name}
	if m.chat != "" {
		parts = append(parts, "chat="+string(m.chat))
	}
	if m.dir != "" {
		parts = append(parts, "dir="+string(m.dir))
	}
	return strings.Join(parts, " ")
}

// describe writes a result as a table states it.
func describe(r Result) string {
	switch r := r.(type) {
	case EmptyResult:
		return "empty"
	case HandshakeResult:
		return fmt.Sprintf("v%d %s", r.Handshake.Version(), r.Handshake.Capabilities())
	case ListResult:
		var parts []string
		for _, c := range r.Conversations {
			parts = append(parts, fmt.Sprintf("%s@%s:%s", c.ID, c.Dir, c.State))
		}
		return "list " + strings.Join(parts, " ")
	}
	return fmt.Sprintf("unknown result %T", r)
}

// The messages of the tables, by what they are.
func connectReq(name string) fakeMsg { return fakeMsg{name: name, kind: Connect} }
func listReq(name, dir string) fakeMsg {
	return fakeMsg{name: name, kind: List, dir: Path(dir)}
}
func openIn(name, dir string) fakeMsg         { return fakeMsg{name: name, kind: Open, dir: Path(dir)} }
func reopen(name string, chat ChatID) fakeMsg { return fakeMsg{name: name, kind: Open, chat: chat} }
func suspendReq(name string, chat ChatID) fakeMsg {
	return fakeMsg{name: name, kind: Suspend, chat: chat}
}
func closeReq(name string, chat ChatID) fakeMsg { return fakeMsg{name: name, kind: Close, chat: chat} }

// msg is a message of a chat, or of none, that session only passes: a
// request, a notification, a question or an answer.
func msg(name string, chat ChatID) fakeMsg { return fakeMsg{name: name, chat: chat} }

func handshakeAnswer(version int, caps string) fakeMsg {
	return fakeMsg{name: "handshake", version: version, caps: caps}
}

// fakeAnswer answers one request, written to what its side received.
type fakeAnswer struct {
	side *fakeChannel
	name string
}

func (a fakeAnswer) Return(m Message) error {
	return a.side.record(fmt.Sprintf("return %s: %v", a.name, m))
}
func (a fakeAnswer) Reply(r Result) error {
	return a.side.record(fmt.Sprintf("reply %s: %s", a.name, describe(r)))
}
func (a fakeAnswer) Fail(error) error { return a.side.record("fail " + a.name) }

// callResult is what a test hands a call it answers.
type callResult struct {
	msg Message
	err error
}

// fakeCall is a call waiting on a fake connection for the test to answer it.
type fakeCall struct{ returned func(Message, error) }

// fakeChannel is one side's connection. What session sends it, and how
// session answers what it sent, is recorded in order; a call waits until the
// test answers it, its context ends, or the connection does.
type fakeChannel struct {
	in    chan Inbound
	err   error // why the connection ended
	fails bool  // every send and answer fails

	mu       sync.Mutex
	changed  *sync.Cond // broadcast on every line recorded
	got      []string
	calls    map[string]*fakeCall // waiting, by the name of their message
	requests map[string]context.CancelFunc
}

func newFakeChannel() *fakeChannel {
	c := &fakeChannel{
		in:       make(chan Inbound),
		calls:    map[string]*fakeCall{},
		requests: map[string]context.CancelFunc{},
	}
	c.changed = sync.NewCond(&c.mu)
	return c
}

var errSend = errors.New("send failed")

func (c *fakeChannel) record(line string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.recordLocked(line)
	if c.fails {
		return errSend
	}
	return nil
}

func (c *fakeChannel) recordLocked(line string) {
	c.got = append(c.got, line)
	c.changed.Broadcast()
}

func (c *fakeChannel) recorded() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.got...)
}

// await waits until the connection records line.
func (c *fakeChannel) await(line string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for !slices.Contains(c.got, line) {
		c.changed.Wait()
	}
}

func (c *fakeChannel) Receive() <-chan Inbound { return c.in }
func (c *fakeChannel) Err() error              { return c.err }

func (c *fakeChannel) Notify(m Message) error { return c.record(fmt.Sprintf("notify %v", m)) }

// Call records the call and keeps it until the test answers it, or its
// context ends.
func (c *fakeChannel) Call(ctx context.Context, m Message, returned func(Message, error)) error {
	name := m.(fakeMsg).name
	call := &fakeCall{returned: returned}
	c.mu.Lock()
	c.recordLocked(fmt.Sprintf("call %v", m))
	c.calls[name] = call
	c.mu.Unlock()
	context.AfterFunc(ctx, func() {
		c.mu.Lock()
		waiting := c.calls[name] == call
		if waiting {
			delete(c.calls, name)
			c.recordLocked("cancelled " + name)
		}
		c.mu.Unlock()
		if waiting {
			returned(nil, ctx.Err())
		}
	})
	return nil
}

// answer hands the call of name its result. The return goes on to the loop
// the test runs, so it is handed over in a goroutine of its own.
func (c *fakeChannel) answer(t *testing.T, name string, r callResult) {
	t.Helper()
	c.mu.Lock()
	call, ok := c.calls[name]
	delete(c.calls, name)
	c.mu.Unlock()
	if !ok {
		t.Fatalf("no call %s waits for an answer", name)
	}
	go call.returned(r.msg, r.err)
}

// called waits until the call of name is made, and takes it to return.
func (c *fakeChannel) called(name string) func(Message, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for c.calls[name] == nil {
		c.changed.Wait()
	}
	call := c.calls[name]
	delete(c.calls, name)
	return call.returned
}

// end fails every call waiting, as a connection that ended does, and is how
// many it failed.
func (c *fakeChannel) end() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(c.calls)
	for name, call := range c.calls {
		delete(c.calls, name)
		go call.returned(nil, errors.New("connection ended"))
	}
	return n
}

// inbound is m as it arrives on this side's connection: a request, whose
// context the test may cancel, when ask, and a notification otherwise.
func (c *fakeChannel) inbound(m fakeMsg, ask bool) Inbound {
	if !ask {
		return Inbound{Message: m}
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	c.requests[m.name] = cancel
	c.mu.Unlock()
	return Inbound{Message: m, Context: ctx, Answer: fakeAnswer{side: c, name: m.name}}
}

// cancel cancels the request of name this side sent.
func (c *fakeChannel) cancel(name string) {
	c.mu.Lock()
	cancel := c.requests[name]
	c.mu.Unlock()
	cancel()
}

// errRefused is an attach the test refuses.
var errRefused = errors.New("attach refused")

// chatEnv is a chat client's world: the fake remote, the client's connection,
// and the adapter of each directory a session attached.
type chatEnv struct {
	f        *fake
	local    workspace.Local
	client   *fakeChannel
	running  map[string]bool // directories whose adapter runs before the chat
	refuse   map[string]bool // directories whose attach fails
	logged   bytes.Buffer
	mu       sync.Mutex
	adapters map[Path]*fakeChannel
	names    map[string]string  // an adapter worker's name → how a table names it
	stop     context.CancelFunc // ends the router's run, as a signal does
}

func newChatEnv(t *testing.T) *chatEnv {
	e := &chatEnv{
		f:        newFake(),
		local:    testLocal(t),
		running:  map[string]bool{},
		refuse:   map[string]bool{},
		adapters: map[Path]*fakeChannel{},
		names:    map[string]string{},
	}
	e.client = newFakeChannel()
	return e
}

var testHandshake = mustHandshake(1, `{"loadSession":true}`)

func mustHandshake(version int, caps string) agent.Handshake {
	h, err := agent.NewHandshake(version, []byte(caps))
	if err != nil {
		panic(err)
	}
	return h
}

func (e *chatEnv) request() ChatRequest {
	return ChatRequest{
		Client:   e.client,
		Agent:    fakeAgent{f: e.f},
		ACP:      fakeAgent{f: e.f, handshake: testHandshake},
		Remote:   e.f.remote(),
		Local:    e.local,
		Attach:   e.attach,
		Teardown: unbounded,
		Log:      log.New(&e.logged, "", 0),
	}
}

// adapter is the connection to the adapter of dir's session.
func (e *chatEnv) adapter(dir Path) *fakeChannel {
	e.mu.Lock()
	defer e.mu.Unlock()
	if c, ok := e.adapters[dir]; ok {
		return c
	}
	c := newFakeChannel()
	e.adapters[dir] = c
	return c
}

// attach opens dir's workspace and starts its adapter worker unless it runs,
// as cli's attach does.
func (e *chatEnv) attach(ctx context.Context, dir Path, teardown workspace.Teardown) (Attachment, error) {
	if e.refuse[string(dir)] {
		return Attachment{}, errRefused
	}
	ws, err := workspace.Open(ctx, e.f.remote(), e.local, string(dir), workspace.Worktree{}, teardown)
	if err != nil {
		return Attachment{}, err
	}
	key, err := ws.AdapterKey()
	if err != nil {
		return Attachment{}, err
	}
	e.f.mu.Lock()
	e.names[key.String()] = "acp:" + ws.Root()
	_, runs := e.f.sessions[key.String()]
	if !runs {
		e.f.sessions[key.String()] = &tmuxSession{state: workspace.WorkerRunning, tags: map[string]string{"@hermod-workspace": ws.Name()}}
	}
	e.f.mu.Unlock()
	return Attachment{
		Workspace: ws,
		Root:      Path(ws.Root()),
		Paths:     NewPathMap(ws.PathMap()),
		Adapter:   e.adapter(dir),
		Started:   !runs && !e.running[string(dir)],
	}, nil
}

// settle runs the events that the calls of a step bring back into the
// router's loop, one for each call the step returned.
func (e *chatEnv) settle(r *router, returns int) error {
	for range returns {
		ev := <-r.events
		if err := ev(r.running); err != nil {
			return err
		}
	}
	return nil
}

// changes are the fake's changes with each adapter worker named as a table
// names it.
func (e *chatEnv) changes() []string {
	got := e.f.changes()
	for i, line := range got {
		for name, as := range e.names {
			line = strings.ReplaceAll(line, name, as)
		}
		got[i] = line
	}
	return got
}

// logLines are the lines the chat logged.
func (e *chatEnv) logLines() []string {
	var lines []string
	for _, l := range strings.Split(e.logged.String(), "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}
