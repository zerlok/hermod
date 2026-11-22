package draft

import (
	"context"
	"fmt"
)

// ---- session: one workspace held attached

// attachment is what attaching to a workspace gives: the workspace, the map
// between its paths on the two workstations, and the connection to the
// remote that runs its chats.
type attachment struct {
	ws     *Workspace
	paths  PathMap
	remote Channel
}

// sessionContext is what a session's states work on besides the state
// itself.
type sessionContext struct {
	attachment
	dir    Path    // the directory the session was opened for: its key among the sessions
	client Channel // the chat client's connection

	// chats are found in the two ways a message names one: by the chat's
	// ID, and, while the chat is being opened, by the client's request that
	// opens it. A new chat has no ID until the adapter answers that request.
	chats   map[ChatID]*ChatControl
	opening map[MessageID]*ChatControl

	// Shared with every other session of the connection.
	owners map[ChatID]Path // chat ID -> the directory of the session it is in
	asked  *requests       // what the adapters asked the client
}

// SessionSM is the machine a session's state is handed with every event.
type SessionSM = StateMachine[SessionState]

// SessionState is one state of a session, with the session's events as its
// methods, in the same pattern as ChatState. Each event reports whether it
// ended the session.
type SessionState interface {
	// HandleClientMessage: the client sent a message for this workspace.
	HandleClientMessage(ctx context.Context, sm SessionSM, session *sessionContext, msg Message) (ended bool, err error)
	// HandleAdapterMessage: the adapter sent a message; nil says its connection ended.
	HandleAdapterMessage(ctx context.Context, sm SessionSM, session *sessionContext, msg Message) (ended bool, err error)
	// Detach: whoever holds the session is finished with it.
	Detach(ctx context.Context, sm SessionSM, session *sessionContext) error
}

// SessionControl is one attachment: its machine and its context. Whoever
// holds it fires an event by calling one of its methods and learns from the
// result whether the session ended.
type SessionControl struct {
	sm      StateMachineImpl[SessionState]
	session sessionContext
}

func NewSessionControl(dir Path, att attachment, client Channel, owners map[ChatID]Path, asked *requests) *SessionControl {
	return &SessionControl{
		sm: StateMachineImpl[SessionState]{state: sessionAttached{}},
		session: sessionContext{
			attachment: att,
			dir:        dir,
			client:     client,
			chats:      map[ChatID]*ChatControl{},
			opening:    map[MessageID]*ChatControl{},
			owners:     owners,
			asked:      asked,
		},
	}
}

func (c *SessionControl) HandleClientMessage(ctx context.Context, msg Message) (bool, error) {
	return c.sm.state.HandleClientMessage(ctx, &c.sm, &c.session, msg)
}

func (c *SessionControl) HandleAdapterMessage(ctx context.Context, msg Message) (bool, error) {
	return c.sm.state.HandleAdapterMessage(ctx, &c.sm, &c.session, msg)
}

func (c *SessionControl) Detach(ctx context.Context) error {
	return c.sm.state.Detach(ctx, &c.sm, &c.session)
}

// sessionAttached: the workspace's relays are leased and messages flow. It
// counts the chats the client waits on, and detaches when the last one is
// left.
type sessionAttached struct {
	waiting int
}

func (st sessionAttached) HandleClientMessage(ctx context.Context, sm SessionSM, s *sessionContext, msg Message) (bool, error) {
	switch msg.Kind() {
	case Open:
		c, known := s.chats[msg.Chat()]
		if !known { // a new chat, or one this session has not seen
			c = NewChatControl(msg.Chat(), FuncSender[Message](s.sendToClient))
		}
		if err := c.Open(msg.ID()); err != nil {
			return false, err
		}
		s.opening[msg.ID()] = c
		sm.SetState(sessionAttached{waiting: st.waiting + 1})

	case Suspend: // answered here and never passed on: the adapter would end the chat
		c, ok := s.chats[msg.Chat()]
		if !ok {
			return false, fmt.Errorf("no such chat: %s", msg.Chat())
		}
		if err := c.Suspend(); err != nil {
			return false, err
		}
		if err := s.client.Send(msg.Reply()); err != nil {
			return false, err
		}
		return st.left(ctx, sm, s)

	case Close:
		c, ok := s.chats[msg.Chat()]
		if !ok {
			return false, fmt.Errorf("no such chat: %s", msg.Chat())
		}
		wasWaiting, err := c.Close()
		if err != nil {
			return false, err
		}
		delete(s.chats, msg.Chat())
		delete(s.owners, msg.Chat())
		if err := s.sendToRemote(msg); err != nil {
			return false, err
		}
		if wasWaiting {
			return st.left(ctx, sm, s)
		}
		return false, nil
	}
	return false, s.sendToRemote(msg)
}

func (st sessionAttached) HandleAdapterMessage(ctx context.Context, sm SessionSM, s *sessionContext, msg Message) (bool, error) {
	if msg == nil { // the adapter is gone, and its chats with it
		if err := st.Detach(ctx, sm, s); err != nil {
			return false, err
		}
		return true, nil
	}
	if c, ok := s.opening[msg.ID()]; ok { // the answer to a request that opens a chat
		delete(s.opening, msg.ID())
		opened, err := c.Answered(msg)
		if opened { // from now on it is found by the ID the answer carries
			s.chats[msg.Chat()] = c
			s.owners[msg.Chat()] = s.dir
		}
		return false, err
	}
	if c, ok := s.chats[msg.Chat()]; ok {
		return false, c.Output(msg)
	}
	return false, s.sendToClient(msg) // a message of no chat
}

// left: the client waits on one chat fewer. The session ends with the last.
func (st sessionAttached) left(ctx context.Context, sm SessionSM, s *sessionContext) (bool, error) {
	if st.waiting > 1 {
		sm.SetState(sessionAttached{waiting: st.waiting - 1})
		return false, nil
	}
	if err := st.Detach(ctx, sm, s); err != nil {
		return false, err
	}
	return true, nil
}

// Detach gives the workspace up, and only then is the session detached: one
// whose workspace could not be detached still holds it. What the session
// put into the shared maps leaves with it.
func (st sessionAttached) Detach(ctx context.Context, sm SessionSM, s *sessionContext) error {
	if err := s.ws.Detach(ctx); err != nil {
		return err
	}
	for chat := range s.chats {
		delete(s.owners, chat)
	}
	clear(s.chats)
	clear(s.opening)
	s.asked.forget(s.dir)
	sm.SetState(sessionDetached{}) // the worker and its chats run on
	return nil
}

// sessionDetached: the leases are given up. The session takes nothing more;
// a client that returns gets a new one.
type sessionDetached struct{}

func (sessionDetached) HandleClientMessage(_ context.Context, _ SessionSM, _ *sessionContext, msg Message) (bool, error) {
	return true, fmt.Errorf("message for a detached session: chat %s", msg.Chat())
}
func (sessionDetached) HandleAdapterMessage(context.Context, SessionSM, *sessionContext, Message) (bool, error) {
	return true, nil // what was on its way when the session ended
}
func (sessionDetached) Detach(context.Context, SessionSM, *sessionContext) error { return nil }

func (s *sessionContext) sendToRemote(msg Message) error {
	return s.remote.Send(msg.Mapped(s.paths.ToRemote))
}

func (s *sessionContext) sendToClient(msg Message) error {
	switch msg.Kind() {
	case Pass:
	case Open:
	case Suspend:
	case Close:
	case Question:
		msg = s.asked.ask(s.dir, msg)
	case Answer:
	}
	return s.client.Send(msg.Mapped(s.paths.ToLocal))
}

// ---- requests: what the adapters asked the client

// requests pairs each adapter's request with the client's answer. Every
// adapter numbers its requests from its own counter, so two adapters send
// the same ID over the client's one connection: a request gets a fresh ID on
// the way out, and the answer finds its session by that ID.
type requests struct {
	next    uint64
	pending map[MessageID]asked
}

type asked struct {
	dir Path      // the session that asked
	id  MessageID // the adapter's own ID
}

func newRequests() *requests { return &requests{pending: map[MessageID]asked{}} }

// ask returns the request under an ID that is unique on the client's connection.
func (r *requests) ask(dir Path, msg Message) Message {
	r.next++
	id := MessageID(fmt.Sprintf("hermod-%d", r.next))
	r.pending[id] = asked{dir: dir, id: msg.ID()}
	return msg.WithID(id)
}

// answer returns the session that asked, and the answer under the adapter's own ID.
func (r *requests) answer(msg Message) (Path, Message, error) {
	a, ok := r.pending[msg.ID()]
	if !ok {
		return "", nil, fmt.Errorf("answer to an unknown request: %s", msg.ID())
	}
	delete(r.pending, msg.ID())
	return a.dir, msg.WithID(a.id), nil
}

// forget drops what a session asked and can no longer be answered.
func (r *requests) forget(dir Path) {
	for id, a := range r.pending {
		if a.dir == dir {
			delete(r.pending, id)
		}
	}
}
