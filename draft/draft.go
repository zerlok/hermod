// Package draft sketches how the interfaces of docs/ARCHITECTURE.md are used:
// session routing a chat over Channel and Message, and workspace attaching
// and detaching with Relay, Lease and Keeper. Everything lives in one file so
// the shapes can be argued with before they are split into packages.
package draft

import (
	"context"
	"errors"
	"io"
)

// ---------------------------------------------------------------- session

// Kind is what a chat message means to a session.
type Kind int

const (
	Pass    Kind = iota // nothing to decide: the message goes to the other side
	Open                // the client opens a chat in a directory
	Close               // the client stops showing a chat, which keeps running
	Request             // one side asks the other and expects an answer
)

// Message is one message of a chat connection, with what routing needs.
type Message interface {
	Kind() Kind
	ID() string   // the request this message is, or answers
	Chat() string // the chat it belongs to, if any
	Dir() string  // the directory it names, if any
	WithID(id string) Message
	Mapped(path func(string) string) Message
	Answer() Message // the empty answer to this request
}

// Channel reads and writes whole messages.
type Channel interface {
	Read() (Message, error)
	Write(Message) error
}

// PathMap maps paths between the two workstations.
type PathMap interface {
	ToRemote(path string) string
	ToLocal(path string) string
}

// attached is one session: a workspace held attached, and its adapter.
type attached struct {
	ws      *Workspace
	paths   PathMap
	adapter Channel
	ids     func(adapterID string) string // renumbers an adapter's request IDs
}

// Chat routes one client's messages to the adapters of the workspaces its
// chats are in. attach returns the session of the workspace a directory
// belongs to, starting its adapter when none runs.
//
// cli wires it: Chat(ctx, acp.NewConn(os.Stdin, os.Stdout, air.Methods), attach)
func Chat(ctx context.Context, client Channel, attach func(ctx context.Context, dir string) (*attached, error)) error {
	chats := map[string]*attached{} // chat ID -> the session serving it

	for {
		msg, err := client.Read()
		if err != nil { // EOF: the client quit
			return detachAll(ctx, chats)
		}

		switch msg.Kind() {
		case Open: // a new chat names a directory
			a, err := attach(ctx, msg.Dir())
			if err != nil {
				return err
			}
			// The adapter's answer carries the chat ID; pump would record
			// chats[id] = a when it sees the answer to this request.
			if err := a.adapter.Write(msg.Mapped(a.paths.ToRemote)); err != nil {
				return err
			}

		case Close: // a closed tab: answered here, never passed on
			if err := client.Write(msg.Answer()); err != nil {
				return err
			}
			if err := release(ctx, chats, msg.Chat()); err != nil {
				return err
			}

		default: // everything else goes to the chat's adapter
			a := chats[msg.Chat()]
			if a == nil {
				return errors.New("no such chat: " + msg.Chat())
			}
			if err := a.adapter.Write(msg.Mapped(a.paths.ToRemote)); err != nil {
				return err
			}
		}
	}
}

// pump carries one adapter's messages to the client; one goroutine per
// adapter.
func (a *attached) pump(client Channel) {
	for {
		msg, err := a.adapter.Read()
		if err != nil {
			return
		}
		if msg.Kind() == Request { // request IDs collide across adapters
			msg = msg.WithID(a.ids(msg.ID()))
		}
		if err := client.Write(msg.Mapped(a.paths.ToLocal)); err != nil {
			return
		}
	}
}

// release detaches a workspace's session when its last open chat is closed.
func release(ctx context.Context, chats map[string]*attached, chat string) error {
	a := chats[chat]
	delete(chats, chat)
	for _, other := range chats {
		if other == a {
			return nil // the workspace still has an open chat
		}
	}
	if a == nil {
		return nil
	}
	return a.ws.Detach(ctx)
}

func detachAll(ctx context.Context, chats map[string]*attached) error {
	var errs []error
	seen := map[*attached]bool{}
	for _, a := range chats {
		if !seen[a] {
			seen[a] = true
			errs = append(errs, a.ws.Detach(ctx))
		}
	}
	return errors.Join(errs...)
}

// ------------------------------------------------------------------ relay

// Relay is one channel between the two workstations.
type Relay interface {
	// Connect takes a connect lease, makes the relay if it is Absent and
	// takes it to Connected.
	Connect(ctx context.Context) (Lease, error)
}

// Lease is a connect lease: a lock held by this process.
type Lease interface {
	// Release gives the lease up. When it was the last one the relay asks
	// its keepers: one still stands -> Disconnected; none -> Absent.
	Release(ctx context.Context) error
}

// Keeper is a keep lease. It is all a relay learns about who needs it.
type Keeper interface {
	Stands(ctx context.Context) (bool, error)
}

// IORelay is a Relay that carries a remote process's stdio.
type IORelay interface {
	Relay
	// Serve copies between the two sides until the local one leaves.
	Serve(ctx context.Context) error
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
	newFS   func(keepers ...Keeper) Relay
	newIO   func(w Worker, streams Streams, keepers ...Keeper) IORelay

	leases []Lease
}

// keeper is the only thing a relay learns about a worker.
type keeper struct{ w Worker }

func (k keeper) Stands(ctx context.Context) (bool, error) { return k.w.Exists(ctx) }

// Attach connects the workspace's relays and serves one worker's stdio on
// the streams until the local side leaves.
func (ws *Workspace) Attach(ctx context.Context, key string, streams Streams) error {
	workers, err := ws.workers(ctx)
	if err != nil {
		return err
	}
	keepers := make([]Keeper, 0, len(workers))
	for _, w := range workers {
		keepers = append(keepers, keeper{w})
	}

	fs := ws.newFS(keepers...)
	lease, err := fs.Connect(ctx) // takes the lock; makes or resumes the Mutagen session
	if err != nil {
		return err
	}
	ws.leases = append(ws.leases, lease)

	w := ws.worker(key)
	stdio := ws.newIO(w, streams, keeper{w})
	lease, err = stdio.Connect(ctx)
	if err != nil {
		return err
	}
	ws.leases = append(ws.leases, lease)

	return stdio.Serve(ctx) // blocks until the client leaves
}

// Detach releases every lease this process holds; each relay settles itself.
func (ws *Workspace) Detach(ctx context.Context) error {
	var errs []error
	for _, l := range ws.leases {
		errs = append(errs, l.Release(ctx))
	}
	ws.leases = nil
	return errors.Join(errs...)
}
