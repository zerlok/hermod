package session

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/zerlok/hermod/internal/agent"
	"github.com/zerlok/hermod/internal/workspace"
)

// sessionContext is what a session's states work on besides the state itself.
type sessionContext struct {
	Attachment
	dir     Path // the directory the session was made for: its key among the sessions
	self    *SessionControl
	client  Caller // the client, paths mapped to the local ones and its answers back
	adapter Caller // the adapter, paths mapped to the remote ones and its answers back
	agent   agent.Agent
	acp     agent.ACP
	log     *log.Logger
	call    callFunc
	// teardown starts the context of a detach the session makes itself.
	teardown workspace.Teardown
	// clientReturns and adapterReturns bring a call's return back into the
	// loop, in order with the messages of the connection it was made on.
	clientReturns, adapterReturns func(event)

	chats map[ChatID]*ChatControl
	// closing counts the Closes that left no chat waited on and whose calls
	// have not returned: the session ends only after the last returns.
	closing int
	// connecting is while the kept Connect has not returned: what is for the
	// adapter waits in queued, since ACP allows initialize only as a
	// connection's first request.
	connecting bool
	queued     []returned
	owners     map[ChatID]Path // shared with the router: chat ID → the directory of its session
}

// callFunc passes a request on to as a call, and hands what it returns back
// through back to the session s of dir, in the router's one loop.
type callFunc func(dir Path, s *SessionControl, to Caller, ret returned, back func(event))

// returned is a call a session made and what it returned: the call that passed
// a request on, whose Answer it returns to, or the kept Connect.
type returned struct {
	call   sessionCall
	in     Inbound
	answer Message
	err    error
}

// sessionCall is what a session acts on when one of its calls returns:
// connectCall, openCall, closeCall or passCall.
type sessionCall interface{ isSessionCall() }

// connectCall is the kept Connect, called on an adapter the attach started.
type connectCall struct{}

// openCall passed the client's Open of chat on; id is the ID the chat has,
// empty for a new chat.
type openCall struct {
	chat *ChatControl
	id   ChatID
}

// closeCall passed the client's Close on; decides is set when it left no chat
// waited on, so its return decides whether the session ends.
type closeCall struct{ decides bool }

// passCall passed a request on that the session only carries.
type passCall struct{}

func (connectCall) isSessionCall() {}
func (openCall) isSessionCall()    {}
func (closeCall) isSessionCall()   {}
func (passCall) isSessionCall()    {}

// SessionSM is the machine a session's state is handed with every event.
type SessionSM = StateMachine[SessionState]

// SessionState is one state of a session, with the session's events as its
// methods, in the pattern of ChatState. The message events report whether
// they ended the session.
type SessionState interface {
	// HandleClientMessage: the client sent a message for this session.
	HandleClientMessage(ctx context.Context, sm SessionSM, s *sessionContext, in Inbound) (bool, error)
	// HandleAdapterMessage: the adapter sent a message; a nil Message says its
	// connection ended.
	HandleAdapterMessage(ctx context.Context, sm SessionSM, s *sessionContext, in Inbound) (bool, error)
	// Returned: a call the session made returned.
	Returned(ctx context.Context, sm SessionSM, s *sessionContext, ret returned) (bool, error)
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

// sessionDeps are what every session of one client connection is built with.
type sessionDeps struct {
	client  Caller
	agent   agent.Agent
	acp     agent.ACP
	log     *log.Logger
	owners  map[ChatID]Path
	call    callFunc
	post    func(event)     // the loop's own way in, which the client's returns take
	running context.Context // the router's, which the kept Connect is called under
	// teardown starts the context of a detach that ends a session during the
	// run, which a signal that ends the run does not cut.
	teardown workspace.Teardown
}

// newSession is the session of dir on att, whose adapter's returns come back
// through adapterReturns. An adapter att started has not been told how the
// client connected, so it is called with the kept Connect before anything
// else; nil is none kept yet.
func newSession(dir Path, att Attachment, deps sessionDeps, connect Message, adapterReturns func(event)) *SessionControl {
	c := &SessionControl{sm: StateMachineImpl[SessionState]{state: sessionAttached{}}}
	c.session = sessionContext{
		Attachment:     att,
		dir:            dir,
		self:           c,
		client:         mappedCaller{c: deps.client, out: att.Paths.ToLocal, back: att.Paths.ToRemote},
		adapter:        mappedCaller{c: att.Adapter, out: att.Paths.ToRemote, back: att.Paths.ToLocal},
		agent:          deps.agent,
		acp:            deps.acp,
		log:            deps.log,
		call:           deps.call,
		teardown:       deps.teardown,
		clientReturns:  deps.post,
		adapterReturns: adapterReturns,
		chats:          map[ChatID]*ChatControl{},
		owners:         deps.owners,
	}
	s := &c.session
	switch {
	case !att.Started:
	case connect == nil:
		s.log.Printf("adapter of %s started before the client connected: it is sent no Connect", dir)
	default:
		s.connecting = true
		s.call(dir, c, s.adapter, returned{call: connectCall{}, in: Inbound{Message: connect, Context: deps.running}}, adapterReturns)
	}
	return c
}

func (c *SessionControl) HandleClientMessage(ctx context.Context, in Inbound) (bool, error) {
	return fire(&c.sm, func(s SessionState) (bool, error) { return s.HandleClientMessage(ctx, &c.sm, &c.session, in) })
}

func (c *SessionControl) HandleAdapterMessage(ctx context.Context, in Inbound) (bool, error) {
	return fire(&c.sm, func(s SessionState) (bool, error) { return s.HandleAdapterMessage(ctx, &c.sm, &c.session, in) })
}

func (c *SessionControl) Returned(ctx context.Context, ret returned) (bool, error) {
	return fire(&c.sm, func(s SessionState) (bool, error) { return s.Returned(ctx, &c.sm, &c.session, ret) })
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

func (st sessionAttached) HandleClientMessage(ctx context.Context, sm SessionSM, s *sessionContext, in Inbound) (bool, error) {
	msg := in.Message
	switch msg.Kind() {
	case Open:
		if in.Answer == nil {
			return false, errors.New("session: an open that is no request")
		}
		id := msg.Chat()
		c, known := s.chats[id]
		if id == "" || !known {
			c = newChat(s.toClient)
		}
		if err := c.Open(in.Answer); err != nil {
			return false, err
		}
		sm.SetState(sessionAttached{waiting: st.waiting + 1})
		return false, s.toAdapter(returned{call: openCall{chat: c, id: id}, in: in})

	case Suspend:
		c, err := s.chat(msg)
		if err != nil {
			return false, err
		}
		waited, err := c.Suspend()
		if err != nil {
			return false, err
		}
		if in.Answer != nil {
			if err := in.Answer.Reply(EmptyResult{}); err != nil {
				return false, err
			}
		}
		return st.left(ctx, sm, s, waited, st.detach)

	case Close:
		c, err := s.chat(msg)
		if err != nil {
			return false, err
		}
		waited, err := c.Close()
		if err != nil {
			return false, err
		}
		s.forgetChat(msg.Chat())
		// A Close sent as a notification gets no answer to wait for.
		decides := in.Answer != nil && (st.waiting == 0 || (waited && st.waiting == 1))
		if decides {
			s.closing++
		}
		if err := s.toAdapter(returned{call: closeCall{decides: decides}, in: in}); err != nil {
			return false, err
		}
		return st.left(ctx, sm, s, waited, st.end)

	case Pass:
		return false, s.toAdapter(returned{call: passCall{}, in: in})
	}
	return false, fmt.Errorf("session: a %s from the client is not a session's", msg.Kind())
}

// left is the event of the client no longer showing a chat, which it waited
// on or not. Waiting on none, the session ends by end, unless a Close still
// waits for its answer, which then decides.
func (st sessionAttached) left(ctx context.Context, sm SessionSM, s *sessionContext, waited bool,
	end func(context.Context, SessionSM, *sessionContext) error) (bool, error) {
	if waited {
		st.waiting--
	}
	sm.SetState(st)
	if st.waiting > 0 || s.closing > 0 {
		return false, nil
	}
	return st.ended(end(ctx, sm, s))
}

// ended reports a session that gave its workspace up as ended. One whose
// workspace could not be given up still holds it, and is not.
func (sessionAttached) ended(err error) (bool, error) { return err == nil, err }

func (st sessionAttached) HandleAdapterMessage(ctx context.Context, sm SessionSM, s *sessionContext, in Inbound) (bool, error) {
	if in.Message == nil { // the adapter is gone, and its chats with it
		return st.ended(st.detach(ctx, sm, s))
	}
	if c, ok := s.chats[in.Message.Chat()]; ok && in.Message.Chat() != "" {
		return false, c.Output(in)
	}
	return false, s.toClient(in) // a message of no chat the session holds
}

func (st sessionAttached) Returned(ctx context.Context, sm SessionSM, s *sessionContext, ret returned) (bool, error) {
	switch call := ret.call.(type) {
	case connectCall:
		s.compareHandshake(ret.answer, ret.err)
		s.connecting = false
		queued := s.queued
		s.queued = nil
		for _, q := range queued {
			if err := s.toAdapter(q); err != nil {
				return false, err
			}
		}
		return false, nil

	case openCall:
		opened, err := call.chat.Answered(ret.in.Answer, ret.answer, ret.err)
		if err != nil {
			return false, err
		}
		if !opened {
			// A chat suspended or closed while it opened, or opened again
			// since, stays as it is, and the client's request still gets its
			// answer.
			return false, answer(ret.in, ret.answer, ret.err)
		}
		id := call.id
		if ret.err == nil && ret.answer.Chat() != "" {
			id = ret.answer.Chat()
		}
		if id == "" {
			// A new chat whose open failed has no ID the client could name,
			// so the client waits on it no more.
			s.log.Printf("session %s: the answer to an open names no chat; the chat cannot be reached", s.dir)
			return st.left(ctx, sm, s, true, st.detach)
		}
		s.chats[id] = call.chat
		s.owners[id] = s.dir
		return false, nil

	case closeCall:
		if err := answer(ret.in, ret.answer, ret.err); err != nil {
			return false, err
		}
		if !call.decides {
			return false, nil
		}
		s.closing--
		// An Open that came meanwhile has the session waiting on a chat.
		return st.left(ctx, sm, s, false, st.end)

	case passCall:
		return false, answer(ret.in, ret.answer, ret.err)
	}
	return false, fmt.Errorf("session: a return of unhandled call %T", ret.call)
}

func (st sessionAttached) Located(_ SessionSM, s *sessionContext, id ChatID, state agent.State) error {
	if _, ok := s.chats[id]; ok {
		return fmt.Errorf("session: chat %s located again in %s", id, s.dir)
	}
	c, err := newLocatedChat(id, state, s.toClient)
	if err != nil {
		return err
	}
	s.chats[id] = c
	s.owners[id] = s.dir
	return nil
}

// Detach gives the workspace up within ctx, the teardown of whoever holds the
// session.
func (st sessionAttached) Detach(ctx context.Context, sm SessionSM, s *sessionContext) error {
	return st.gaveUp(sm, s, s.Workspace.Detach(ctx))
}

// detach gives the workspace up within a teardown started for it. The adapter
// and the chats it runs go on.
func (st sessionAttached) detach(_ context.Context, sm SessionSM, s *sessionContext) error {
	return st.gaveUp(sm, s, s.teardown.Detach(s.Workspace))
}

// gaveUp is Detached only for a workspace given up: one whose workspace could
// not be detached still holds it.
func (sessionAttached) gaveUp(sm SessionSM, s *sessionContext, err error) error {
	if err != nil {
		return err
	}
	return s.leave(sm)
}

// end ends the session once it waits on no chat. One adapter runs every chat
// of the workspace, a subdirectory's included, so it is closed only when the
// agent program runs no conversation in the root or under it; a listing that
// cannot be read counts as one running, which never ends something in use.
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
	if err := s.Workspace.Close(ctx, key, s.teardown); err != nil {
		return err
	}
	return s.leave(sm)
}

// sessionDetached: the leases are given up. The session takes nothing more;
// a client that returns gets a new one.
type sessionDetached struct{}

func (sessionDetached) HandleClientMessage(_ context.Context, _ SessionSM, s *sessionContext, in Inbound) (bool, error) {
	return true, fmt.Errorf("session: a %s for the detached session of %s", in.Message.Kind(), s.dir)
}

// HandleAdapterMessage drops what was on its way when the session ended.
func (sessionDetached) HandleAdapterMessage(context.Context, SessionSM, *sessionContext, Inbound) (bool, error) {
	return true, nil
}

// Returned still answers the request a call passed on.
func (sessionDetached) Returned(_ context.Context, _ SessionSM, _ *sessionContext, ret returned) (bool, error) {
	return true, answer(ret.in, ret.answer, ret.err)
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
// the client as a message of no chat.
func (s *sessionContext) forgetChat(id ChatID) {
	delete(s.chats, id)
	delete(s.owners, id)
}

// leave drops what the session put into the owners, and its chats with the
// questions they hold, unanswered, and switches it to Detached. A request
// still waiting for the kept Connect fails.
func (s *sessionContext) leave(sm SessionSM) error {
	for id := range s.chats {
		delete(s.owners, id)
	}
	clear(s.chats)
	sm.SetState(sessionDetached{})
	var errs []error
	for _, q := range s.queued {
		errs = append(errs, answer(q.in, nil, fmt.Errorf("hermod: the session of %s ended", s.dir)))
	}
	s.queued = nil
	return errors.Join(errs...)
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
func (s *sessionContext) compareHandshake(answer Message, err error) {
	if err != nil {
		s.log.Printf("session %s: the adapter refused the connection start: %v", s.dir, err)
		return
	}
	declared := s.acp.Connect()
	got, ok := answer.Result().(HandshakeResult)
	switch {
	case !ok:
		s.log.Printf("session %s: the adapter's answer to the connection start is no handshake", s.dir)
	case got.Handshake.Version() != declared.Version():
		s.log.Printf("session %s: the adapter speaks protocol version %d, the client was answered %d", s.dir, got.Handshake.Version(), declared.Version())
	case !got.Handshake.Equal(declared):
		s.log.Printf("session %s: the adapter offers capabilities %s, the client was answered %s", s.dir, got.Handshake.Capabilities(), declared.Capabilities())
	}
}

// toAdapter hands the adapter ret's message: a notification at once, and a
// request as a call whose return comes back to the session.
func (s *sessionContext) toAdapter(ret returned) error {
	switch {
	case s.connecting:
		s.queued = append(s.queued, ret)
	case ret.in.Answer == nil:
		return s.adapter.Notify(ret.in.Message)
	default:
		s.call(s.dir, s.self, s.adapter, ret, s.adapterReturns)
	}
	return nil
}

// toClient hands the client an adapter's message: a notification at once,
// and a request as a call whose answer returns to the adapter.
func (s *sessionContext) toClient(in Inbound) error {
	if in.Answer == nil {
		return s.client.Notify(in.Message)
	}
	s.call(s.dir, s.self, s.client, returned{call: passCall{}, in: in}, s.clientReturns)
	return nil
}
