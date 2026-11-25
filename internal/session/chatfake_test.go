package session

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"testing"

	"github.com/zerlok/hermod/internal/agent"
	"github.com/zerlok/hermod/internal/workspace"
)

// fakeMsg is a chat message as a Channel would give it, comparable so that a
// table states what each side receives.
type fakeMsg struct {
	kind   Kind
	id     MessageID
	chat   ChatID
	dir    Path
	result string // the result of a reply session made, as describe writes it
	failed bool   // an error answer
	// The handshake an adapter's answer to a Connect gives; no capabilities
	// is none.
	version int
	caps    string
}

func (m fakeMsg) Kind() Kind                  { return m.kind }
func (m fakeMsg) ID() MessageID               { return m.id }
func (m fakeMsg) Chat() ChatID                { return m.chat }
func (m fakeMsg) Dir() Path                   { return m.dir }
func (m fakeMsg) WithID(id MessageID) Message { m.id = id; return m }
func (m fakeMsg) Reply(r Result) Message      { return fakeMsg{id: m.id, result: describe(r)} }
func (m fakeMsg) Fail(error) Message          { return fakeMsg{id: m.id, failed: true} }

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

// fid is the ID a table writes as text; "" is none.
func fid(id string) MessageID {
	if id == "" {
		return MessageID{}
	}
	return TextID(id)
}

// The messages of the tables, by what they are.
func connectReq(id string) fakeMsg          { return fakeMsg{kind: Connect, id: fid(id)} }
func listReq(id string, dir string) fakeMsg { return fakeMsg{kind: List, id: fid(id), dir: Path(dir)} }
func cancelReq(id string) fakeMsg           { return fakeMsg{kind: Cancel, id: fid(id)} }
func openIn(id string, dir string) fakeMsg  { return fakeMsg{kind: Open, id: fid(id), dir: Path(dir)} }
func reopen(id string, chat ChatID) fakeMsg { return fakeMsg{kind: Open, id: fid(id), chat: chat} }
func suspendReq(id string, chat ChatID) fakeMsg {
	return fakeMsg{kind: Suspend, id: fid(id), chat: chat}
}
func closeReq(id string, chat ChatID) fakeMsg { return fakeMsg{kind: Close, id: fid(id), chat: chat} }
func ask(id string, chat ChatID) fakeMsg      { return fakeMsg{kind: Question, id: fid(id), chat: chat} }
func reply(id string) fakeMsg                 { return fakeMsg{kind: Answer, id: fid(id)} }
func failure(id string) fakeMsg               { return fakeMsg{id: fid(id), failed: true} }
func result(id string, r string) fakeMsg      { return fakeMsg{id: fid(id), result: r} }
func update(chat ChatID) fakeMsg              { return fakeMsg{chat: chat} }

// prompt is a request of the client's in a chat, or an adapter's answer that
// names the chat, such as the answer to an Open of a new chat.
func prompt(id string, chat ChatID) fakeMsg { return fakeMsg{id: fid(id), chat: chat} }

// done is an adapter's answer that names no chat.
func done(id string) fakeMsg { return fakeMsg{id: fid(id)} }

func handshakeAnswer(id string, version int, caps string) fakeMsg {
	return fakeMsg{id: fid(id), version: version, caps: caps}
}

// fakeChannel is one side's connection: what a test hands in arrives on in,
// and what session sends is recorded.
type fakeChannel struct {
	in     chan Message
	err    error // why the connection ended
	notify chan Message

	mu   sync.Mutex
	sent []Message
}

func newFakeChannel() *fakeChannel {
	return &fakeChannel{in: make(chan Message), notify: make(chan Message, 64)}
}

func (c *fakeChannel) Receive() <-chan Message { return c.in }
func (c *fakeChannel) Err() error              { return c.err }

func (c *fakeChannel) Send(msg Message) error {
	c.mu.Lock()
	c.sent = append(c.sent, msg)
	c.mu.Unlock()
	c.notify <- msg
	return nil
}

func (c *fakeChannel) messages() []Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Message(nil), c.sent...)
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
	names    map[string]string // an adapter worker's name → how a table names it
}

func newChatEnv(t *testing.T) *chatEnv {
	return &chatEnv{
		f:        newFake(),
		local:    testLocal(t),
		client:   newFakeChannel(),
		running:  map[string]bool{},
		refuse:   map[string]bool{},
		adapters: map[Path]*fakeChannel{},
		names:    map[string]string{},
	}
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
		Client: e.client,
		Agent:  fakeAgent{f: e.f},
		ACP:    fakeAgent{f: e.f, handshake: testHandshake},
		Remote: e.f.remote(),
		Local:  e.local,
		Attach: e.attach,
		Log:    log.New(&e.logged, "", 0),
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
func (e *chatEnv) attach(ctx context.Context, dir Path) (Attachment, error) {
	if e.refuse[string(dir)] {
		return Attachment{}, errRefused
	}
	ws, err := workspace.Open(ctx, e.f.remote(), e.local, string(dir), workspace.Worktree{})
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
