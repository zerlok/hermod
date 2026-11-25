package session

import (
	"context"
	"errors"
	"fmt"
	"log"
	"maps"
	"path"
	"slices"
	"sync"

	"github.com/zerlok/hermod/internal/agent"
	"github.com/zerlok/hermod/internal/workspace"
)

// Attachment is what attaching the workspace of a directory gives a chat
// client.
type Attachment struct {
	Workspace *workspace.Workspace
	Root      Path    // the workspace's project root on this workstation
	Paths     PathMap // fixed while the workspace is attached
	Adapter   Channel // the connection to the workspace's adapter
	// Started is whether the attach started the adapter, which then has not
	// seen how the client connected.
	Started bool
}

// AttachFunc attaches the workspace dir belongs to, starting its adapter when
// none runs. The adapter's connection ends when the workspace is detached.
type AttachFunc func(ctx context.Context, dir Path) (Attachment, error)

// ChatRequest is one chat client's connection, as the command line built it.
type ChatRequest struct {
	Client Channel
	// Agent answers for the remote where a chat belongs and which chats a
	// directory has.
	Agent agent.Agent
	// ACP answers the client's connection start.
	ACP    agent.ACP
	Remote workspace.Remote
	Local  workspace.Local
	Attach AttachFunc
	Log    *log.Logger // nil discards
}

// Chat serves one chat client. The client holds a single connection for every
// chat of every project, while each project's chats run in its workspace's own
// adapter: Chat finds the session each message is for, attaching a workspace
// when a message needs one no session holds, and answers itself what names no
// chat. It returns when the client's connection or ctx ends, after detaching
// every session; the adapters keep running.
func Chat(ctx context.Context, req ChatRequest) (err error) {
	running, stop := context.WithCancel(ctx)
	r := newRouter(running, req)
	defer func() { err = errors.Join(err, r.close(context.WithoutCancel(ctx), stop)) }()
	for {
		select {
		case <-ctx.Done():
			return nil
		case in, ok := <-req.Client.Receive():
			if !ok {
				return req.Client.Err()
			}
			if err := r.fromClient(running, in); err != nil {
				return err
			}
		case ev := <-r.events:
			if err := ev(running); err != nil {
				return err
			}
		}
	}
}

// router is what Chat keeps across messages: which session a message is for,
// and when a session is made or forgotten. What a session registers about its
// chats is the session's to decide.
type router struct {
	req      ChatRequest
	running  context.Context // ends the forwarders and the calls
	deps     sessionDeps
	sessions map[Path]*SessionControl // keyed by the exact directory the session was made for
	// connect is the client's Connect, kept for every adapter started later.
	connect Message

	// events are what reaches the one loop from elsewhere: an adapter's
	// message, or what a call returned.
	events     chan event
	forwarders sync.WaitGroup
	calls      sync.WaitGroup // the calls whose return has not reached the loop, which close waits for
}

type event func(ctx context.Context) error

func newRouter(running context.Context, req ChatRequest) *router {
	logger := orDiscardLog(req.Log)
	req.Log = logger
	r := &router{
		req:      req,
		running:  running,
		sessions: map[Path]*SessionControl{},
		events:   make(chan event),
	}
	r.deps = sessionDeps{
		client:  req.Client,
		agent:   req.Agent,
		acp:     req.ACP,
		log:     logger,
		owners:  map[ChatID]Path{},
		call:    r.call,
		post:    r.post,
		running: running,
	}
	return r
}

// close detaches every session, in the order of their directories, and stops
// the forwarders and the calls.
func (r *router) close(ctx context.Context, stop context.CancelFunc) error {
	var errs []error
	for _, dir := range slices.Sorted(maps.Keys(r.sessions)) {
		errs = append(errs, r.sessions[dir].Detach(ctx))
		delete(r.sessions, dir)
	}
	// Detaching ends each adapter's connection, which ends its forwarder;
	// stop ends the calls, and frees a goroutine blocked handing an event to
	// the loop that has returned.
	stop()
	r.forwarders.Wait()
	r.calls.Wait()
	return errors.Join(errs...)
}

// post hands ev to the loop, unless the router has stopped.
func (r *router) post(ev event) {
	select {
	case r.events <- ev:
	case <-r.running.Done():
	}
}

// call passes ret's request on to as a call, under the request's context for
// as long as the router runs, and brings what it returns back into the loop
// through back, in order with the other messages of to's connection, to the
// session s of dir while the router holds it. A request that could not be sent
// returns its error the same way.
func (r *router) call(dir Path, s *SessionControl, to Caller, ret returned, back func(event)) {
	ctx, cancel := context.WithCancel(ret.in.Context)
	stop := context.AfterFunc(r.running, cancel)
	returnTo := func(back func(event)) func(Message, error) {
		return func(msg Message, err error) {
			defer r.calls.Done()
			cancel()
			stop()
			ret.answer, ret.err = msg, err
			back(func(ctx context.Context) error {
				if held, ok := r.sessions[dir]; !ok || held != s {
					return answer(ret.in, ret.answer, ret.err)
				}
				ended, err := s.Returned(ctx, ret)
				if ended {
					delete(r.sessions, dir)
				}
				return err
			})
		}
	}
	r.calls.Add(1)
	if err := to.Call(ctx, ret.in.Message, returnTo(back)); err != nil {
		// Called from the loop, the error cannot be handed to it here; it
		// follows nothing of to's, whose connection may have ended.
		go returnTo(r.post)(nil, err)
	}
}

func (r *router) fromClient(ctx context.Context, in Inbound) error {
	msg := in.Message
	switch msg.Kind() {
	case Connect:
		r.connect = msg
		return reply(in, HandshakeResult{Handshake: r.req.ACP.Connect()})

	case List:
		result, err := r.list(ctx, msg.Dir())
		if err != nil {
			return answer(in, nil, err)
		}
		return reply(in, result)

	case Open:
		if msg.Chat() != "" {
			return r.forChat(ctx, in)
		}
		if msg.Dir() == "" {
			return answer(in, nil, errors.New("hermod: the request names neither a chat nor a directory"))
		}
		if _, err := r.session(ctx, msg.Dir()); err != nil {
			return err
		}
		return r.handle(ctx, msg.Dir(), in)

	case Suspend, Close:
		return r.forChat(ctx, in)

	case Pass:
		dir, ok := r.deps.owners[msg.Chat()]
		if !ok || msg.Chat() == "" {
			return answer(in, nil, fmt.Errorf("hermod: no chat %q is open", msg.Chat()))
		}
		return r.handle(ctx, dir, in)
	}
	return fmt.Errorf("session: unhandled message kind %s", msg.Kind())
}

// reply answers a request session answers itself; a notification has no
// answer.
func reply(in Inbound, result Result) error {
	if in.Answer == nil {
		return nil
	}
	return in.Answer.Reply(result)
}

// forChat routes a message that names a chat to the session that holds it,
// locating the chat in the agent program when none does. A chat the program
// does not have is answered with an error, and the client goes on.
func (r *router) forChat(ctx context.Context, in Inbound) error {
	msg := in.Message
	dir, ok := r.deps.owners[msg.Chat()]
	if !ok {
		conv, err := r.locate(ctx, msg.Chat())
		if err != nil {
			return r.fail(in, err)
		}
		// A stopped chat keeps nothing running to suspend: attaching for it
		// would start an adapter that no Close ever ends.
		if msg.Kind() == Suspend && conv.State == agent.Stopped {
			return reply(in, EmptyResult{})
		}
		paths, err := workspace.Map(ctx, r.req.Remote, r.req.Local)
		if err != nil {
			return r.fail(in, err)
		}
		dir = Path(paths.ToLocal(conv.Dir))
		s, err := r.session(ctx, dir)
		if err != nil {
			return err
		}
		if err := s.Located(msg.Chat(), conv.State); err != nil {
			return err
		}
	}
	return r.handle(ctx, dir, in)
}

// fail answers a request the router could not route with err; a notification
// has no answer, so its failure is only logged.
func (r *router) fail(in Inbound, err error) error {
	if in.Answer == nil {
		r.req.Log.Printf("dropped a %s of chat %q: %v", in.Message.Kind(), in.Message.Chat(), err)
		return nil
	}
	return in.Answer.Fail(err)
}

// locate asks the agent program where a chat belongs.
func (r *router) locate(ctx context.Context, chat ChatID) (agent.Conversation, error) {
	conv, err := r.req.Agent.Lookup(ctx, string(chat))
	if errors.Is(err, agent.ErrUnknown) {
		return agent.Conversation{}, fmt.Errorf("hermod: the agent program on %s has no chat %q", r.req.Remote.Host, chat)
	}
	return conv, err
}

// list is the conversations of exactly the directory asked: the agent program
// lists those under it too, which are other directories' chats. A directory no
// relay covers is mapped as a new workspace would join it, so a project never
// attached here gets the remote's list as well.
func (r *router) list(ctx context.Context, dir Path) (ListResult, error) {
	if dir == "" {
		return ListResult{}, nil
	}
	paths, err := workspace.Map(ctx, r.req.Remote, r.req.Local, string(dir))
	if err != nil {
		return ListResult{}, err
	}
	remoteDir := path.Clean(paths.ToRemote(string(dir)))
	found, err := r.req.Agent.List(ctx, remoteDir)
	if err != nil {
		return ListResult{}, err
	}
	kept := []agent.Conversation{}
	for _, c := range found {
		if c.Dir == remoteDir {
			c.Dir = paths.ToLocal(c.Dir)
			kept = append(kept, c)
		}
	}
	return ListResult{Conversations: kept}, nil
}

// session is the session of dir, attached when none holds it.
func (r *router) session(ctx context.Context, dir Path) (*SessionControl, error) {
	if s, ok := r.sessions[dir]; ok {
		return s, nil
	}
	att, err := r.req.Attach(ctx, dir)
	if err != nil {
		return nil, err
	}
	// The adapter's answers take the way of its messages, so each keeps its
	// place among them.
	// A call cancelled as the connection ended can return after its
	// forwarder has gone, and then goes to the loop directly.
	returns, gone := make(chan event), make(chan struct{})
	back := func(ev event) {
		select {
		case returns <- ev:
		case <-gone:
			r.post(ev)
		case <-r.running.Done():
		}
	}
	s := newSession(dir, att, r.deps, r.connect, back)
	r.sessions[dir] = s
	r.forwarders.Go(func() {
		defer close(gone)
		r.forward(dir, s, att.Adapter, returns)
	})
	return s, nil
}

func (r *router) handle(ctx context.Context, dir Path, in Inbound) error {
	s, ok := r.sessions[dir]
	if !ok {
		return fmt.Errorf("session: no session holds %s", dir)
	}
	ended, err := s.HandleClientMessage(ctx, in)
	if ended {
		delete(r.sessions, dir)
	}
	return err
}

// fromAdapter hands a session an adapter's message; a nil Message says the
// adapter's connection ended. What was on its way when its session ended is
// dropped.
func (r *router) fromAdapter(ctx context.Context, dir Path, s *SessionControl, in Inbound) error {
	if held, ok := r.sessions[dir]; !ok || held != s {
		return nil
	}
	ended, err := s.HandleAdapterMessage(ctx, in)
	if ended {
		delete(r.sessions, dir)
	}
	return err
}

// forward carries one adapter's messages and the returns of the calls on its
// connection into the router's loop, in the order they arrived, and then the
// news that the adapter's connection ended.
func (r *router) forward(dir Path, s *SessionControl, adapter Channel, returns <-chan event) {
	for {
		select {
		case ev := <-returns:
			r.post(ev)
		case in, ok := <-adapter.Receive():
			if !ok {
				if err := adapter.Err(); err != nil {
					r.req.Log.Printf("adapter of %s: %v", dir, err)
				}
				in = Inbound{}
			}
			r.post(func(ctx context.Context) error { return r.fromAdapter(ctx, dir, s, in) })
			if !ok {
				return
			}
		case <-r.running.Done():
			return
		}
	}
}
