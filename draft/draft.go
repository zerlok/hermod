// Package draft sketches how the interfaces of docs/ARCHITECTURE.md are used:
// session routing a chat over Channel and Message, and workspace attaching
// and detaching with Relay and Lease. Everything lives in one file so the
// shapes can be argued with before they are split into packages.
package draft

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
)

// ---------------------------------------------------------------- session

// Kind is what a chat message means to a session.
type Kind int

const (
	Pass     Kind = iota // nothing to decide: the message goes to the other side
	Open                 // the client opens a chat in a directory, or returns to one
	Suspend              // the client stops showing a chat, which keeps running
	Close                // the client ends a chat: its agent stops
	Question             // a remote asks the client and expects an answer
	Answer               // the client answers a remote's question
)

type MessageID string
type ChatID string
type Path string

// Message is one message of a chat connection, with what routing needs.
type Message interface {
	Kind() Kind
	ID() MessageID // the request this message is, or answers
	Chat() ChatID  // the chat it belongs to, if any
	Dir() Path     // the directory it names, if any
	WithID(id MessageID) Message
	Mapped(path func(Path) Path) Message
	Reply() Message // the empty answer to this request
}

// PathMap maps paths between the two workstations.
type PathMap interface {
	ToRemote(path Path) Path
	ToLocal(path Path) Path
}

// ---- Chat: the router

// RemoteMessage is a message an adapter sent, with the session it came from.
// A nil msg says the adapter's connection ended.
type RemoteMessage struct {
	dir Path
	msg Message
}

// Chat serves one chat client: it finds the session each message is for and
// hands the message to it. The client holds a single connection for every
// chat in every project, while each project's chats run in that workspace's
// own adapter, so a message is routed by its directory when it opens a chat,
// by the request it answers, and by its chat otherwise.
//
// The maps are the state the sessions share. Chat decides which
// session a message is for and when a session is made or forgotten; what a
// session registers about its chats and requests is the session's to decide.
//
// attach attaches to the workspace a directory belongs to, starting its
// adapter when none runs.
//
// cli wires it: Chat(ctx, acp.NewConn(os.Stdin, os.Stdout, air.Methods), attach)
func Chat(ctx context.Context, client Channel, attach func(ctx context.Context, dir Path) (attachment, error)) error {
	r := &router{
		client:   client,
		attach:   attach,
		sessions: map[Path]*SessionControl{},
		owners:   map[ChatID]Path{},
		asked:    newRequests(),
		remotes:  make(chan RemoteMessage),
	}
	running, stop := context.WithCancel(ctx)
	defer func() {
		// Detaching closes each adapter's channel, which ends its forwarder;
		// stop frees one that is blocked handing a message to this loop.
		for _, s := range r.sessions {
			_ = s.Detach(context.WithoutCancel(ctx))
		}
		stop()
		r.consumers.Wait()
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case msg, ok := <-client.Receive():
			if !ok { // the client quit
				return client.Err()
			}
			dir, msg, err := r.route(running, msg)
			if err != nil {
				return err
			}
			s, ok := r.sessions[dir]
			if !ok {
				return fmt.Errorf("no session for %s", dir)
			}
			ended, err := s.HandleClientMessage(running, msg)
			if err != nil {
				return err
			}
			if ended {
				delete(r.sessions, dir)
			}

		case in := <-r.remotes:
			s, ok := r.sessions[in.dir]
			if !ok {
				continue // what was on its way when the session ended
			}
			ended, err := s.HandleAdapterMessage(running, in.msg)
			if err != nil {
				return err
			}
			if ended {
				delete(r.sessions, in.dir)
			}
		}
	}
}

// router is what Chat keeps across messages.
type router struct {
	client Channel
	attach func(ctx context.Context, dir Path) (attachment, error)

	sessions  map[Path]*SessionControl // directory -> session
	owners    map[ChatID]Path          // chat ID -> directory of its session
	asked     *requests
	remotes   chan RemoteMessage
	consumers sync.WaitGroup
}

// route finds the session a client message is for, attaching a new one when
// the message opens a chat in a directory no session holds.
func (r *router) route(ctx context.Context, msg Message) (Path, Message, error) {
	switch msg.Kind() {
	case Open:
		dir := msg.Dir()
		if _, ok := r.sessions[dir]; ok {
			return dir, msg, nil
		}
		att, err := r.attach(ctx, dir)
		if err != nil {
			return "", nil, err
		}
		r.sessions[dir] = NewSessionControl(dir, att, r.client, r.owners, r.asked)
		r.consumers.Go(func() { consume(ctx, dir, att.remote, r.remotes) })
		return dir, msg, nil
	case Answer:
		return r.asked.answer(msg)
	case Pass:
	case Suspend:
	case Close:
	case Question:
	}
	if dir, ok := r.owners[msg.Chat()]; ok {
		return dir, msg, nil
	}
	return "", nil, fmt.Errorf("no such chat: %s", msg.Chat())
}

// consume carries one adapter's messages into the loop, and then the news
// that the adapter's connection ended. out is shared by every session's
// forwarder, so none of them may close it.
func consume(ctx context.Context, dir Path, remote Channel, out chan<- RemoteMessage) {
	send := func(msg Message) bool {
		select {
		case out <- RemoteMessage{dir: dir, msg: msg}:
			return true
		case <-ctx.Done():
			return false
		}
	}
	for msg := range remote.Receive() {
		if !send(msg) {
			return
		}
	}
	send(nil)
}

// ------------------------------------------------------------------ relay

// Relay is one channel between the two workstations.
type Relay interface {
	// Connect takes a lease, makes the relay if it is Absent, takes it to
	// Connected and starts whatever serves it in this process: nothing for
	// an FS relay, the copying for an IO relay, the listener for a Net relay.
	Connect(ctx context.Context) (Lease, error)
}

// Lease is one process's claim that a relay is Connected: a lock held by
// this process. Whoever holds it says how to give it up, and the relay acts
// on that only when it was the last lease.
type Lease interface {
	// Done is closed when the relay stops serving on its own: an IO relay
	// whose local side left or whose process ended, a Net relay whose
	// connection dropped. Err then says why.
	Done() <-chan struct{}
	Err() error
	// Suspend gives the lease up and leaves the relay Disconnected: it
	// exists, and buffers.
	Suspend(ctx context.Context) error
	// Release gives the lease up and takes the relay to Absent.
	Release(ctx context.Context) error
}

// Streams are the local side of an IO relay.
type Streams struct {
	In  io.Reader
	Out io.Writer
}

// ----------------------------------------------------------------- worker

// Worker is one persistent process on the remote.
type Worker interface {
	Exists(ctx context.Context) (bool, error)
}

// -------------------------------------------------------------- workspace

// Workspace is the relays and the workers of one project on one remote.
type Workspace struct {
	workers func(ctx context.Context) ([]Worker, error) // read from tmux, by the workspace's tag
	worker  func(key string) Worker
	fs      Relay
	newIO   func(w Worker, streams Streams) Relay

	leases []Lease
}

// Attach connects the workspace's relays and serves one worker's stdio on
// the streams until the local side leaves.
func (ws *Workspace) Attach(ctx context.Context, key string, streams Streams) error {
	fs, err := ws.fs.Connect(ctx) // takes the lock; makes or resumes the Mutagen session
	if err != nil {
		return err
	}
	ws.leases = append(ws.leases, fs)

	stdio, err := ws.newIO(ws.worker(key), streams).Connect(ctx)
	if err != nil {
		return err
	}
	ws.leases = append(ws.leases, stdio)

	select {
	case <-stdio.Done(): // the client left, or the worker's process ended
		return stdio.Err()
	case <-fs.Done():
		return fs.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Detach gives up every lease this process holds. The workspace is what
// knows whether a worker still needs the relays, so it chooses: suspend them
// while one runs, release them when none does. Workers that cannot be read
// count as running, which never removes something in use.
func (ws *Workspace) Detach(ctx context.Context) error {
	workers, err := ws.workers(ctx)
	keep := err != nil || len(workers) > 0

	errs := []error{err}
	for _, l := range ws.leases {
		if keep {
			errs = append(errs, l.Suspend(ctx))
		} else {
			errs = append(errs, l.Release(ctx))
		}
	}
	ws.leases = nil
	return errors.Join(errs...)
}
