package session

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/zerlok/hermod/internal/agent"
)

// sessionContext is what a session's states work on besides the state itself.
type sessionContext struct {
	Attachment
	dir    Path // the directory the session was made for: its key among the sessions
	client Sender[Message]
	agent  agent.Agent
	acp    agent.ACP
	log    *log.Logger

	// A chat is found in the two ways a message names it: by the chat's ID,
	// and, while it is being opened, by the client's request that opens it. A
	// new chat has no ID until the adapter answers that request.
	chats   map[ChatID]*ChatControl
	opening map[MessageID]opening
	// closing is the Close that left no chat waited on, whose answer the
	// session waits for before it ends.
	closing optionalID
	// connect is the kept Connect this session sent its adapter first, whose
	// answer is the adapter's and not the client's.
	connect optionalID

	shared *shared
}

// opening is a chat being opened: the chat, and the ID it already has when the
// client returns to it.
type opening struct {
	chat *ChatControl
	id   ChatID
}

// optionalID is a request ID that may be unset. A notification has no ID and
// no answer, so an empty one is never set: it would match every notification.
type optionalID struct {
	id  MessageID
	set bool
}

func someID(id MessageID) optionalID { return optionalID{id: id, set: id != ""} }

func (o optionalID) is(id MessageID) bool { return o.set && o.id == id }

// shared is what the router shares with every session.
type shared struct {
	owners   map[ChatID]Path // chat ID → the directory of its session
	requests *requests       // what the adapters asked the client
}

// SessionSM is the machine a session's state is handed with every event.
type SessionSM = StateMachine[SessionState]

// SessionState is one state of a session, with the session's events as its
// methods, in the pattern of ChatState. The message events report whether
// they ended the session.
type SessionState interface {
	// HandleClientMessage: the client sent a message for this session.
	HandleClientMessage(ctx context.Context, sm SessionSM, s *sessionContext, msg Message) (bool, error)
	// HandleAdapterMessage: the adapter sent a message; nil says its
	// connection ended.
	HandleAdapterMessage(ctx context.Context, sm SessionSM, s *sessionContext, msg Message) (bool, error)
	// Located: the agent program reported a chat of this session's directory
	// in state, for a client message that names it.
	Located(sm SessionSM, s *sessionContext, id ChatID, state agent.State) error
	// Detach: whoever holds the session is finished with it.
	Detach(ctx context.Context, sm SessionSM, s *sessionContext) error
}

// SessionControl is one workspace held attached for the chat client: its
// machine and its context.
type SessionControl struct {
	sm      StateMachineImpl[SessionState]
	session sessionContext
}

// newSession is the session of dir on att. An adapter att started has not
// been told how the client connected, so the kept Connect is sent to it before
// anything else; nil is none kept yet.
func newSession(dir Path, att Attachment, deps sessionDeps, connect Message) (*SessionControl, error) {
	c := &SessionControl{
		sm: StateMachineImpl[SessionState]{state: sessionAttached{}},
		session: sessionContext{
			Attachment: att,
			dir:        dir,
			client:     deps.client,
			agent:      deps.agent,
			acp:        deps.acp,
			log:        deps.log,
			chats:      map[ChatID]*ChatControl{},
			opening:    map[MessageID]opening{},
			shared:     deps.shared,
		},
	}
	if att.Started {
		if connect == nil {
			c.session.log.Printf("adapter of %s started before the client connected: it is sent no Connect", dir)
		} else {
			c.session.connect = someID(connect.ID())
			if err := c.session.toAdapter(connect); err != nil {
				return nil, err
			}
		}
	}
	return c, nil
}

// sessionDeps are what every session of one client connection is built with.
type sessionDeps struct {
	client Sender[Message]
	agent  agent.Agent
	acp    agent.ACP
	log    *log.Logger
	shared *shared
}

func (c *SessionControl) HandleClientMessage(ctx context.Context, msg Message) (bool, error) {
	return fire(&c.sm, func(s SessionState) (bool, error) { return s.HandleClientMessage(ctx, &c.sm, &c.session, msg) })
}

func (c *SessionControl) HandleAdapterMessage(ctx context.Context, msg Message) (bool, error) {
	return fire(&c.sm, func(s SessionState) (bool, error) { return s.HandleAdapterMessage(ctx, &c.sm, &c.session, msg) })
}

func (c *SessionControl) Located(id ChatID, state agent.State) error {
	_, err := fire(&c.sm, func(s SessionState) (struct{}, error) { return struct{}{}, s.Located(&c.sm, &c.session, id, state) })
	return err
}

// Detach is taken even by a machine an earlier event aborted: whoever holds
// the session gives its workspace up however the session failed.
func (c *SessionControl) Detach(ctx context.Context) error {
	return c.sm.state.Detach(ctx, &c.sm, &c.session)
}

// sessionAttached: the workspace's relays are leased and messages flow. It
// counts the chats the client waits on.
type sessionAttached struct {
	waiting int
}

func (st sessionAttached) HandleClientMessage(ctx context.Context, sm SessionSM, s *sessionContext, msg Message) (bool, error) {
	switch msg.Kind() {
	case Open:
		id := msg.Chat()
		c, known := s.chats[id]
		if id == "" || !known {
			c = newChat(FuncSender[Message](s.toClient))
		}
		if err := c.Open(msg.ID()); err != nil {
			return false, err
		}
		s.opening[msg.ID()] = opening{chat: c, id: id}
		sm.SetState(sessionAttached{waiting: st.waiting + 1})
		return false, s.toAdapter(msg)

	case Suspend:
		c, err := s.chat(msg)
		if err != nil {
			return false, err
		}
		waited, err := c.Suspend()
		if err != nil {
			return false, err
		}
		if err := s.toClient(msg.Reply(EmptyResult{})); err != nil {
			return false, err
		}
		st = st.left(waited)
		sm.SetState(st)
		// A Close still waiting for its answer decides the session's end.
		if st.waiting > 0 || s.closing.set {
			return false, nil
		}
		return st.ended(st.detach(ctx, sm, s))

	case Close:
		c, err := s.chat(msg)
		if err != nil {
			return false, err
		}
		waited, err := c.Close()
		if err != nil {
			return false, err
		}
		s.forgetChat(msg.Chat(), c)
		if err := s.toAdapter(msg); err != nil {
			return false, err
		}
		st = st.left(waited)
		sm.SetState(st)
		if st.waiting > 0 {
			return false, nil
		}
		if id := someID(msg.ID()); id.set {
			s.closing = id
			return false, nil
		}
		// A Close sent as a notification gets no answer to wait for; an
		// earlier Close that will be answered still decides.
		if s.closing.set {
			return false, nil
		}
		return st.ended(st.end(ctx, sm, s))

	case Pass, Cancel, Answer:
		return false, s.toAdapter(msg)

	case Connect, List, Question:
		return false, fmt.Errorf("session: a %s from the client is not a session's", msg.Kind())
	}
	return false, fmt.Errorf("session: unhandled message kind %s", msg.Kind())
}

// ended reports a session that gave its workspace up as ended. One whose
// workspace could not be given up still holds it, and is not.
func (sessionAttached) ended(err error) (bool, error) { return err == nil, err }

// left is the state after the client stopped showing a chat, which it waited
// on or not.
func (st sessionAttached) left(waited bool) sessionAttached {
	if waited {
		st.waiting--
	}
	return st
}

func (st sessionAttached) HandleAdapterMessage(ctx context.Context, sm SessionSM, s *sessionContext, msg Message) (bool, error) {
	if msg == nil { // the adapter is gone, and its chats with it
		return st.ended(st.detach(ctx, sm, s))
	}
	// The adapter numbers its own requests apart from the client's, so a
	// question, or its cancel, may carry the ID of a request of the client's
	// still waiting.
	if !adapterOwn(msg) {
		if s.connect.is(msg.ID()) {
			s.connect = optionalID{}
			s.compareHandshake(msg)
			return false, nil
		}
		if o, ok := s.opening[msg.ID()]; ok {
			delete(s.opening, msg.ID())
			opened, err := o.chat.Answered(msg)
			if err != nil || !opened {
				// A chat suspended while it opened stays so, and the
				// adapter replays it when the client returns.
				return false, err
			}
			id := msg.Chat()
			if id == "" {
				id = o.id
			}
			if id == "" {
				// An answer that names no new chat, such as an error, leaves
				// a chat nobody can name, so the client waits on it no more.
				s.log.Printf("session %s: the answer to %s names no chat; the chat cannot be reached", s.dir, msg.ID())
				st = st.left(true)
				sm.SetState(st)
				if st.waiting > 0 || s.closing.set {
					return false, nil
				}
				return st.ended(st.detach(ctx, sm, s))
			}
			s.chats[id] = o.chat
			s.shared.owners[id] = s.dir
			return false, nil
		}
		if s.closing.is(msg.ID()) {
			s.closing = optionalID{}
			if err := s.toClient(msg); err != nil {
				return false, err
			}
			if st.waiting > 0 { // an Open came meanwhile
				return false, nil
			}
			return st.ended(st.end(ctx, sm, s))
		}
	}
	if c, ok := s.chats[msg.Chat()]; ok && msg.Chat() != "" {
		return false, c.Output(msg)
	}
	return false, s.toClient(msg) // a message of no chat the session holds
}

func (st sessionAttached) Located(_ SessionSM, s *sessionContext, id ChatID, state agent.State) error {
	if _, ok := s.chats[id]; ok {
		return fmt.Errorf("session: chat %s located again in %s", id, s.dir)
	}
	c, err := newLocatedChat(id, state, FuncSender[Message](s.toClient))
	if err != nil {
		return err
	}
	s.chats[id] = c
	s.shared.owners[id] = s.dir
	return nil
}

func (st sessionAttached) Detach(ctx context.Context, sm SessionSM, s *sessionContext) error {
	return st.detach(ctx, sm, s)
}

// detach gives the workspace up, and only then is the session Detached: one
// whose workspace could not be detached still holds it. The adapter and the
// chats it runs go on.
func (st sessionAttached) detach(ctx context.Context, sm SessionSM, s *sessionContext) error {
	if err := s.Workspace.Detach(ctx); err != nil {
		return err
	}
	s.leave(sm)
	return nil
}

// end ends the session after the last chat it waited on was closed. One
// adapter runs every chat of the workspace, a subdirectory's included, so it
// is closed only when the agent program runs no conversation in the root or
// under it; a listing that cannot be read counts as one running, which never
// ends something in use.
func (st sessionAttached) end(ctx context.Context, sm SessionSM, s *sessionContext) error {
	running, err := s.runsConversation(ctx)
	if err != nil {
		s.log.Printf("session %s: keeping the adapter: %v", s.dir, err)
	}
	if err != nil || running {
		return st.detach(ctx, sm, s)
	}
	key, err := s.Workspace.AdapterKey()
	if err != nil {
		return errors.Join(err, st.detach(ctx, sm, s))
	}
	if err := s.Workspace.Close(ctx, key); err != nil {
		return err
	}
	s.leave(sm)
	return nil
}

// sessionDetached: the leases are given up. The session takes nothing more;
// a client that returns gets a new one.
type sessionDetached struct{}

func (sessionDetached) HandleClientMessage(_ context.Context, _ SessionSM, s *sessionContext, msg Message) (bool, error) {
	return true, fmt.Errorf("session: a %s for the detached session of %s", msg.Kind(), s.dir)
}

// HandleAdapterMessage drops what was on its way when the session ended.
func (sessionDetached) HandleAdapterMessage(context.Context, SessionSM, *sessionContext, Message) (bool, error) {
	return true, nil
}

func (sessionDetached) Located(_ SessionSM, s *sessionContext, id ChatID, _ agent.State) error {
	return fmt.Errorf("session: chat %s located for the detached session of %s", id, s.dir)
}

func (sessionDetached) Detach(context.Context, SessionSM, *sessionContext) error { return nil }

// chat is the chat a client message names, which the router found here.
func (s *sessionContext) chat(msg Message) (*ChatControl, error) {
	c, ok := s.chats[msg.Chat()]
	if !ok || msg.Chat() == "" {
		return nil, fmt.Errorf("session: no chat %q in %s", msg.Chat(), s.dir)
	}
	return c, nil
}

// forgetChat drops a closed chat, so what the adapter still sends of it goes to
// the client as a message of no chat, the answer to a request that opened it
// included.
func (s *sessionContext) forgetChat(id ChatID, c *ChatControl) {
	delete(s.chats, id)
	delete(s.shared.owners, id)
	for request, o := range s.opening {
		if o.chat == c {
			delete(s.opening, request)
		}
	}
}

// leave drops what the session put into the shared maps, its chats and the
// questions they hold, and switches it to Detached.
func (s *sessionContext) leave(sm SessionSM) {
	for id := range s.chats {
		delete(s.shared.owners, id)
	}
	clear(s.chats)
	clear(s.opening)
	s.shared.requests.forget(s.dir)
	sm.SetState(sessionDetached{})
}

// runsConversation asks the agent program whether a conversation of the
// workspace runs, in its root or under it.
func (s *sessionContext) runsConversation(ctx context.Context) (bool, error) {
	found, err := s.agent.List(ctx, string(s.Paths.ToRemote(s.Root)))
	if err != nil {
		return false, err
	}
	for _, c := range found {
		if c.State == agent.Running {
			return true, nil
		}
	}
	return false, nil
}

// compareHandshake logs where the adapter's answer to the kept Connect differs
// from what the agent declared, which the client was answered with.
func (s *sessionContext) compareHandshake(msg Message) {
	declared := s.acp.Connect()
	got, ok := msg.Handshake()
	switch {
	case !ok:
		s.log.Printf("session %s: the adapter's answer to the connection start is no handshake", s.dir)
	case got.Version() != declared.Version():
		s.log.Printf("session %s: the adapter speaks protocol version %d, the client was answered %d", s.dir, got.Version(), declared.Version())
	case !got.Equal(declared):
		s.log.Printf("session %s: the adapter offers capabilities %s, the client was answered %s", s.dir, got.Capabilities(), declared.Capabilities())
	}
}

func (s *sessionContext) toAdapter(msg Message) error {
	return s.Adapter.Send(msg.Mapped(s.Paths.ToRemote))
}

// toClient sends msg to the client with its paths mapped back, a question,
// and the cancel of one, under the ID of the router's own the client knows it
// by. A cancel of a question the client was never asked, one held in a
// Suspended chat or already answered, has nothing to cancel on the client.
func (s *sessionContext) toClient(msg Message) error {
	switch msg.Kind() {
	case Question:
		msg = s.shared.requests.ask(s.dir, msg)
	case Cancel:
		id, ok := s.shared.requests.asked(s.dir, msg.ID())
		if !ok {
			s.log.Printf("session %s: dropped a cancel of %q, a question the client was not asked", s.dir, msg.ID())
			return nil
		}
		msg = msg.WithID(id)
	}
	return s.client.Send(msg.Mapped(s.Paths.ToLocal))
}

// adapterOwn reports whether an adapter's message is in its own request space:
// a question, or the cancel of one, and no answer to the client's.
func adapterOwn(msg Message) bool { return msg.Kind() == Question || msg.Kind() == Cancel }

// requests pairs each adapter's question with the client's answer. Every
// adapter numbers its requests from its own counter, so two adapters send the
// same ID over the client's one connection: a question gets a fresh ID on the
// way out, and the answer finds its session by that ID.
type requests struct {
	next    uint64
	pending map[MessageID]asked
	// dropped are the questions of sessions that detached, which the client
	// may still answer, or cancel, as it would any question.
	dropped map[MessageID]struct{}
}

type asked struct {
	dir Path      // the session that asked
	id  MessageID // the adapter's own ID
}

func newRequests() *requests {
	return &requests{pending: map[MessageID]asked{}, dropped: map[MessageID]struct{}{}}
}

func (r *requests) ask(dir Path, msg Message) Message {
	r.next++
	id := MessageID(fmt.Sprintf("hermod-%d", r.next))
	r.pending[id] = asked{dir: dir, id: msg.ID()}
	return msg.WithID(id)
}

// asked is the ID the client knows the question dir's adapter asked under id
// by. The entry stays: a cancelled request is still answered.
func (r *requests) asked(dir Path, id MessageID) (MessageID, bool) {
	for ours, a := range r.pending {
		if a.dir == dir && a.id == id {
			return ours, true
		}
	}
	return "", false
}

// answer is the session that asked, and the answer under the adapter's own ID;
// live is false for a question whose session detached, whose answer has
// nowhere to go.
func (r *requests) answer(msg Message) (dir Path, answer Message, live bool, err error) {
	if _, ok := r.dropped[msg.ID()]; ok {
		delete(r.dropped, msg.ID())
		return "", nil, false, nil
	}
	a, ok := r.pending[msg.ID()]
	if !ok {
		return "", nil, false, fmt.Errorf("session: answer to an unknown request %q", msg.ID())
	}
	delete(r.pending, msg.ID())
	return a.dir, msg.WithID(a.id), true, nil
}

// forget drops what a session asked and can no longer be answered.
func (r *requests) forget(dir Path) {
	for id, a := range r.pending {
		if a.dir == dir {
			delete(r.pending, id)
			r.dropped[id] = struct{}{}
		}
	}
}
