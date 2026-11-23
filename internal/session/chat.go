package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"path"
	"slices"
	"strings"
	"sync"

	"github.com/zerlok/hermod/internal/agent"
	"github.com/zerlok/hermod/internal/shell"
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
	Agent  agent.Agent
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
		case msg, ok := <-req.Client.Receive():
			if !ok {
				return req.Client.Err()
			}
			if err := r.fromClient(running, msg); err != nil {
				return err
			}
		case in := <-r.adapters:
			if err := r.fromAdapter(running, in); err != nil {
				return err
			}
		}
	}
}

// router is what Chat keeps across messages: which session a message is for,
// and when a session is made or forgotten. What a session registers about its
// chats and requests is the session's to decide.
type router struct {
	req      ChatRequest
	running  context.Context // ends the forwarders
	deps     sessionDeps
	sessions map[Path]*SessionControl // keyed by the exact directory the session was made for
	// pending is each request of the client's that a session passed on: the
	// client's request ID → the directory of that session.
	pending map[MessageID]Path
	// connect is the client's Connect, kept for every adapter started later.
	connect Message

	adapters   chan adapterMessage
	forwarders sync.WaitGroup
}

// adapterMessage is a message an adapter sent, with the session it came from;
// a nil msg says the adapter's connection ended.
type adapterMessage struct {
	dir     Path
	session *SessionControl
	msg     Message
}

func newRouter(running context.Context, req ChatRequest) *router {
	logger := req.Log
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	req.Log = logger
	r := &router{
		req:      req,
		running:  running,
		sessions: map[Path]*SessionControl{},
		pending:  map[MessageID]Path{},
		adapters: make(chan adapterMessage),
	}
	r.deps = sessionDeps{
		client: req.Client,
		agent:  req.Agent,
		query:  r.query,
		log:    logger,
		shared: &shared{owners: map[ChatID]Path{}, requests: newRequests()},
	}
	return r
}

// close detaches every session, in the order of their directories, and stops
// the forwarders.
func (r *router) close(ctx context.Context, stop context.CancelFunc) error {
	var errs []error
	for _, dir := range slices.Sorted(maps.Keys(r.sessions)) {
		errs = append(errs, r.sessions[dir].Detach(ctx))
		r.forget(dir)
	}
	// Detaching ends each adapter's connection, which ends its forwarder;
	// stop frees one blocked handing a message to the loop that has returned.
	stop()
	r.forwarders.Wait()
	return errors.Join(errs...)
}

func (r *router) fromClient(ctx context.Context, msg Message) error {
	switch msg.Kind() {
	case Connect:
		r.connect = msg.WithoutClientCapabilities()
		return r.req.Client.Send(msg.Reply(HandshakeResult{Handshake: r.req.Agent.Connect()}))

	case List:
		return r.req.Client.Send(r.list(ctx, msg))

	case Cancel:
		dir, ok := r.pending[msg.ID()]
		if !ok {
			r.req.Log.Printf("dropped a cancel of %q, which no session holds", msg.ID())
			return nil
		}
		return r.handle(ctx, dir, msg)

	case Answer:
		dir, answer, live, err := r.deps.shared.requests.answer(msg)
		if err != nil {
			return err
		}
		if !live {
			r.req.Log.Printf("dropped an answer to %q, whose session detached", msg.ID())
			return nil
		}
		return r.handle(ctx, dir, answer)

	case Open:
		if msg.Chat() != "" {
			return r.forChat(ctx, msg)
		}
		if msg.Dir() == "" {
			return r.req.Client.Send(msg.Fail(errors.New("hermod: the request names neither a chat nor a directory")))
		}
		if _, err := r.session(ctx, msg.Dir()); err != nil {
			return err
		}
		return r.request(ctx, msg.Dir(), msg)

	case Suspend, Close:
		return r.forChat(ctx, msg)

	case Pass:
		dir, ok := r.deps.shared.owners[msg.Chat()]
		if !ok || msg.Chat() == "" {
			if msg.ID() == "" {
				return nil
			}
			return r.req.Client.Send(msg.Fail(fmt.Errorf("hermod: no chat %q is open", msg.Chat())))
		}
		return r.request(ctx, dir, msg)

	case Question:
		return fmt.Errorf("session: a question from the client: %q", msg.ID())
	}
	return fmt.Errorf("session: unhandled message kind %s", msg.Kind())
}

// forChat routes a message that names a chat to the session that holds it,
// locating the chat in the agent program when none does. A chat the program
// does not have is answered with an error, and the client goes on.
func (r *router) forChat(ctx context.Context, msg Message) error {
	dir, ok := r.deps.shared.owners[msg.Chat()]
	if !ok {
		conv, err := r.locate(ctx, msg.Chat())
		if err != nil {
			return r.fail(msg, err)
		}
		// A stopped chat keeps nothing running to suspend: attaching for it
		// would start an adapter that no Close ever ends.
		if msg.Kind() == Suspend && conv.State == agent.Stopped {
			if msg.ID() == "" {
				return nil
			}
			return r.req.Client.Send(msg.Reply(EmptyResult{}))
		}
		paths, err := workspace.Map(ctx, r.req.Remote, r.req.Local)
		if err != nil {
			return r.fail(msg, err)
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
	return r.request(ctx, dir, msg)
}

// fail answers a request the router could not route with err; a notification
// has no answer, so its failure is only logged.
func (r *router) fail(msg Message, err error) error {
	if msg.ID() == "" {
		r.req.Log.Printf("dropped a %s of chat %q: %v", msg.Kind(), msg.Chat(), err)
		return nil
	}
	return r.req.Client.Send(msg.Fail(err))
}

// locate asks the agent program where a chat belongs.
func (r *router) locate(ctx context.Context, chat ChatID) (agent.Conversation, error) {
	lookup := r.req.Agent.Locate(string(chat))
	out, err := r.query(ctx, lookup.Command())
	if err != nil {
		return agent.Conversation{}, err
	}
	conv, found, err := lookup.Read(out)
	if err != nil {
		return agent.Conversation{}, err
	}
	if !found {
		return agent.Conversation{}, fmt.Errorf("hermod: the agent program on %s has no chat %q", r.req.Remote.Host, chat)
	}
	return conv, nil
}

// list answers a List with the conversations of exactly the directory asked:
// the agent program lists those under it too, which are other directories'
// chats. A directory no relay covers is mapped as a new workspace would join
// it, so a project never attached here gets the remote's list as well.
func (r *router) list(ctx context.Context, msg Message) Message {
	dir := string(msg.Dir())
	if dir == "" {
		return msg.Reply(ListResult{})
	}
	paths, err := workspace.Map(ctx, r.req.Remote, r.req.Local, dir)
	if err != nil {
		return msg.Fail(err)
	}
	remoteDir := path.Clean(paths.ToRemote(dir))
	listing := r.req.Agent.List(remoteDir)
	out, err := r.query(ctx, listing.Command())
	if err != nil {
		return msg.Fail(err)
	}
	found, err := listing.Read(out)
	if err != nil {
		return msg.Fail(err)
	}
	kept := []agent.Conversation{}
	for _, c := range found {
		if c.Dir == remoteDir {
			c.Dir = paths.ToLocal(c.Dir)
			kept = append(kept, c)
		}
	}
	return msg.Reply(ListResult{Conversations: kept})
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
	s, err := newSession(dir, att, r.deps, r.connect)
	if err != nil {
		return nil, errors.Join(err, att.Workspace.Detach(ctx))
	}
	r.sessions[dir] = s
	r.forwarders.Add(1)
	go func() {
		defer r.forwarders.Done()
		r.forward(dir, s, att.Adapter)
	}()
	return s, nil
}

// request hands a client message to the session of dir, recording a request
// that the session passes to its adapter, so a Cancel of it finds the session.
// A Suspend is answered by the session and never reaches an adapter.
func (r *router) request(ctx context.Context, dir Path, msg Message) error {
	if msg.ID() != "" && msg.Kind() != Suspend {
		r.pending[msg.ID()] = dir
	}
	return r.handle(ctx, dir, msg)
}

func (r *router) handle(ctx context.Context, dir Path, msg Message) error {
	s, ok := r.sessions[dir]
	if !ok {
		return fmt.Errorf("session: no session holds %s", dir)
	}
	ended, err := s.HandleClientMessage(ctx, msg)
	if ended {
		r.forget(dir)
	}
	return err
}

func (r *router) fromAdapter(ctx context.Context, in adapterMessage) error {
	s, ok := r.sessions[in.dir]
	if !ok || s != in.session {
		return nil // on its way when its session ended
	}
	// A question, or the cancel of one, is in the adapter's own request space,
	// numbered apart from the client's; anything else with an ID answers one
	// of the client's.
	if in.msg != nil && !adapterOwn(in.msg) && r.pending[in.msg.ID()] == in.dir {
		delete(r.pending, in.msg.ID())
	}
	ended, err := s.HandleAdapterMessage(ctx, in.msg)
	if ended {
		r.forget(in.dir)
	}
	return err
}

// forget drops a session that ended, and the client's requests it held.
func (r *router) forget(dir Path) {
	delete(r.sessions, dir)
	for id, held := range r.pending {
		if held == dir {
			delete(r.pending, id)
		}
	}
}

// forward carries one adapter's messages into the router's loop, and then the
// news that the adapter's connection ended.
func (r *router) forward(dir Path, s *SessionControl, adapter Channel) {
	deliver := func(msg Message) bool {
		select {
		case r.adapters <- adapterMessage{dir: dir, session: s, msg: msg}:
			return true
		case <-r.running.Done():
			return false
		}
	}
	for {
		select {
		case msg, ok := <-adapter.Receive():
			if !ok {
				if err := adapter.Err(); err != nil {
					r.req.Log.Printf("adapter of %s: %v", dir, err)
				}
				deliver(nil)
				return
			}
			if !deliver(msg) {
				return
			}
		case <-r.running.Done():
			return
		}
	}
}

// query runs a command of the agent's on the remote, under the remote user's
// login shell where the agent program is found.
func (r *router) query(ctx context.Context, cmd []string) ([]byte, error) {
	ex := shell.NewLogin(shell.NewSSH(r.req.Remote.Read, r.req.Remote.Host))
	out, err := shell.Run(ctx, ex, shell.Command{Args: cmd})
	if err != nil {
		if detail := strings.TrimSpace(out.Stderr); detail != "" {
			err = fmt.Errorf("%w: %s", err, detail)
		}
		return nil, fmt.Errorf("run %q on %s: %w", cmd, r.req.Remote.Host, err)
	}
	return []byte(out.Stdout), nil
}
