// Package relay is one channel between the local and the remote workstation,
// and the leases that claim it. Three kinds share one set of states: an FS
// relay carries files between two directories over a Mutagen session, an IO
// relay copies between local streams and a remote process's stdio, and a Net
// relay carries calls from a remote port to a local address. A relay counts
// its leases and knows nothing about who holds them or why: whoever gives a
// lease up says whether the relay is suspended or released, and the relay acts
// on that only when it was the last lease.
package relay

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// State is a relay's state, decided by its leases. Reading it can fail, which
// is an error beside the State rather than a State of its own, so the zero
// value is no state at all.
type State int

const (
	// Absent is no relay: nothing is exchanged and nothing is buffered.
	Absent State = iota + 1
	// Disconnected is a relay that exists with no connect lease on it: each end
	// accepts what arrives there and buffers it.
	Disconnected
	// Connected is a relay with at least one connect lease: both ends exchange
	// continuously.
	Connected
)

func (s State) String() string {
	switch s {
	case Absent:
		return "absent"
	case Disconnected:
		return "disconnected"
	case Connected:
		return "connected"
	}
	return fmt.Sprintf("State(%d)", int(s))
}

// Relay is one channel between the workstations.
type Relay interface {
	// Connect takes a connect lease, makes the relay when it is Absent, takes it
	// to Connected and starts whatever serves it in this process. What serves it
	// is shared by the leases this process takes on the same Relay while it
	// serves, and runs until the last of them is given up, ctx of the Connect
	// that started it ends, or it stops on its own; a Connect after that starts
	// it anew.
	Connect(ctx context.Context) (Lease, error)
}

// Lease is a connect lease: a claim that the relay delivers.
type Lease interface {
	// Done is closed when the relay stops serving: on its own, for an IO relay
	// whose local side left or whose process ended and a Net relay whose
	// connection dropped, or when this process gave up its last lease on it. An
	// FS relay has nothing serving it in this process, so its Done stays open.
	Done() <-chan struct{}
	// Err is why Done was closed: ErrLeft, ErrEnded or the failure, and nil for
	// a relay this process stopped serving itself.
	Err() error
	// Suspend gives the lease up; the last one leaves the relay Disconnected,
	// or removes a Net relay, which cannot buffer.
	Suspend(ctx context.Context) error
	// Release gives the lease up; the last one removes the relay, flushing an
	// FS relay first.
	Release(ctx context.Context) error
}

var (
	// ErrLeft is why an IO relay stopped when its local input ended.
	ErrLeft = errors.New("relay: the local side left")
	// ErrEnded is why a relay stopped when its remote side ended cleanly.
	ErrEnded = errors.New("relay: the remote side ended")
	// ErrInUse refuses a second reader of a line stream.
	ErrInUse = errors.New("relay: a line stream has one reader, and its relay is leased")
	// errGivenUp refuses a lease given up twice.
	errGivenUp = errors.New("relay: the lease was already given up")
)

// giveUp is how a holder gives its lease up.
type giveUp int

const (
	suspend giveUp = iota + 1
	release
)

// mechanism is what one kind of relay does at each step of its leases.
type mechanism interface {
	// make makes the relay Connected across processes, after the lease's lock
	// is taken: creates or resumes an FS relay.
	make(ctx context.Context) error
	// serve starts what serves the relay in this process, for the first lease
	// this process takes on it; nil for a kind with nothing to serve.
	serve(ctx context.Context) (*serving, error)
	// settle acts on the last lease given up across processes, while its
	// holder still has the lock to itself.
	settle(ctx context.Context, how giveUp) error
}

// leases is the part every kind shares: a lock file per relay for the leases
// across processes, and a count of this process's holders for what serves it
// here.
type leases struct {
	name      string
	locks     string // the directory of the lock files
	exclusive bool   // a lease is the only one: a line stream's single reader
	mech      mechanism

	// making serializes make in this process: two leases taken at once would
	// otherwise both find no FS session and both create one under the name.
	//
	// TODO: two processes can still both create one; making the relay under
	// the exclusive lock would close that.
	making sync.Mutex

	mu  sync.Mutex
	srv *serving // what serves leases taken now; nil for a kind with nothing to serve
}

func (ls *leases) connect(ctx context.Context) (Lease, error) {
	lk, err := takeLock(ctx, ls.locks, ls.name, ls.exclusive)
	if err != nil {
		return nil, err
	}
	ls.making.Lock()
	err = ls.mech.make(ctx)
	ls.making.Unlock()
	if err != nil {
		return nil, errors.Join(err, lk.close())
	}
	ls.mu.Lock()
	defer ls.mu.Unlock()
	if ls.srv == nil || ls.srv.finished() {
		// A serving that stopped on its own serves no one, though leases on it
		// may still be held: a new lease gets a serving of its own, and those
		// leases keep the stopped one, whose Done stays closed.
		if ls.srv != nil {
			ls.srv.stop()
			ls.srv = nil
		}
		srv, err := ls.mech.serve(ctx)
		if err != nil {
			// The lock was taken for this lease alone, so whether it was the last
			// one decides the relay as a Suspend would: one that cannot be served
			// stays for the next try.
			return nil, errors.Join(err, ls.settleLast(ctx, lk, suspend))
		}
		ls.srv = srv
	}
	if ls.srv != nil {
		ls.srv.holders++
	}
	return &lease{owner: ls, lock: lk, srv: ls.srv}, nil
}

// settleLast settles the relay when lk was its last lease, then drops lk.
func (ls *leases) settleLast(ctx context.Context, lk *lock, how giveUp) error {
	last, err := lk.last()
	if err == nil && last {
		err = ls.mech.settle(ctx, how)
	}
	return errors.Join(err, lk.close())
}

func (ls *leases) giveUp(ctx context.Context, l *lease, how giveUp) error {
	ls.mu.Lock()
	if l.srv != nil {
		l.srv.holders--
		// A serving already replaced was stopped then.
		if l.srv.holders == 0 && l.srv == ls.srv {
			ls.srv.stop()
			ls.srv = nil
		}
	}
	ls.mu.Unlock()
	return ls.settleLast(ctx, l.lock, how)
}

// lease is one holder's lease on a relay.
type lease struct {
	owner *leases
	lock  *lock
	srv   *serving // nil for a kind with nothing to serve

	once sync.Once
}

// never is the Done of a relay that has nothing serving it in this process.
var never = make(chan struct{})

func (l *lease) Done() <-chan struct{} {
	if l.srv == nil {
		return never
	}
	return l.srv.done
}

func (l *lease) Err() error {
	if l.srv == nil {
		return nil
	}
	return l.srv.result()
}

func (l *lease) Suspend(ctx context.Context) error { return l.end(ctx, suspend) }
func (l *lease) Release(ctx context.Context) error { return l.end(ctx, release) }

func (l *lease) end(ctx context.Context, how giveUp) error {
	err := errGivenUp
	l.once.Do(func() { err = l.owner.giveUp(ctx, l, how) })
	return err
}

// serving is what serves a relay in this process: goroutines and commands
// that end together.
type serving struct {
	done   chan struct{}
	once   sync.Once
	err    error
	cancel context.CancelFunc
	wg     sync.WaitGroup

	holders int // this process's leases on it, guarded by the leases' mu
}

func newServing(ctx context.Context) (*serving, context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	return &serving{done: make(chan struct{}), cancel: cancel}, ctx
}

// finish ends the serving with why; the first reason given is kept.
func (s *serving) finish(why error) {
	s.once.Do(func() {
		s.err = why
		close(s.done)
		s.cancel()
	})
}

// run runs f as part of the serving, which stop waits for; its end, whatever
// it returns, ends the serving. Something that may block where cancelling
// cannot reach it, such as a read of a local stream, is not run here.
func (s *serving) run(f func() error) {
	s.wg.Go(func() { s.finish(f()) })
}

// stop ends the serving and waits for what it ran.
func (s *serving) stop() {
	s.finish(nil)
	s.wg.Wait()
}

func (s *serving) finished() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

func (s *serving) result() error {
	select {
	case <-s.done:
		return s.err
	default:
		return nil
	}
}
