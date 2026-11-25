package relay

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/zerlok/hermod/internal/shell"
)

// Stdio is how an IO relay reaches one remote process's stdio: a Terminal or
// Lines.
type Stdio interface{ isStdio() }

// Terminal is the stdio of a process on a terminal: Attach joins it, and the
// relay copies the local streams to and from that command both ways.
type Terminal struct {
	Attach shell.Command
}

// Lines is the stdio of a line-stream process, which reads a FIFO and appends
// to logs: In writes its stdin into the FIFO, and Out and Err tail the logs to
// their stdout until the process ends.
type Lines struct {
	In  shell.Command
	Out shell.Command
	Err shell.Command
}

func (Terminal) isStdio() {}
func (Lines) isStdio()    {}

// Streams are the local side of an IO relay. A nil Out or Err discards what
// the process writes there; a nil In sends nothing and never leaves.
type Streams struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer
}

// IO is an IO relay: copies between local streams and a remote stdio. The
// remote stdio buffers while nothing is attached, so the relay has nothing to
// make or settle, only copying to serve.
type IO struct {
	ex     shell.Executor
	remote Stdio
	local  Streams
	input  *input
	leases leases
}

// NewIO is the IO relay named name between the remote stdio, whose commands
// run on ex, and the local streams; the lock files of its leases are in locks.
// A line stream has a single reader, so a second lease on it is refused with
// ErrInUse.
func NewIO(ex shell.Executor, locks, name string, remote Stdio, local Streams) *IO {
	_, lines := remote.(Lines)
	r := &IO{ex: ex, remote: remote, local: local, input: newInput(local.In, lines)}
	r.leases = leases{name: name, locks: locks, exclusive: lines, mech: r}
	return r
}

// Connect leases the relay and starts copying. Its lease's Done is closed with
// ErrLeft when the local input ends, or with ErrEnded or the failure when the
// remote side does.
func (r *IO) Connect(ctx context.Context) (Lease, error) { return r.leases.connect(ctx) }

func (r *IO) make(context.Context) error { return nil }

func (r *IO) settle(context.Context, giveUp) error { return nil }

func (r *IO) serve(ctx context.Context) (*serving, error) {
	srv, ctx := newServing(ctx)
	r.input.start()
	errOut := &lockedWriter{w: orDiscard(r.local.Err)}
	switch remote := r.remote.(type) {
	case Terminal:
		cmd := remote.Attach
		cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, orDiscard(r.local.Out), errOut
		ex := r.ex.Exec(ctx, cmd)
		srv.run(func() error { return ended(ex.Wait()) })
		srv.run(func() error {
			left, _ := r.input.copyTo(ctx, ex.Stdin)
			if left != nil {
				// Finished before the attach's input closes: an attach that ends
				// on it has not seen its remote side end.
				srv.finish(left)
			}
			_ = ex.Stdin.Close()
			// A failed write is the attach ending, which its Wait reports.
			<-ctx.Done()
			return nil
		})
	case Lines:
		in := remote.In
		in.Stdin, in.Stdout, in.Stderr = nil, io.Discard, errOut
		inEx := r.ex.Exec(ctx, in)
		out := remote.Out
		out.Stdin, out.Stdout, out.Stderr = shell.NoInput, orDiscard(r.local.Out), errOut
		outEx := r.ex.Exec(ctx, out)
		logs := remote.Err
		logs.Stdin, logs.Stdout, logs.Stderr = shell.NoInput, errOut, errOut
		errEx := r.ex.Exec(ctx, logs)
		srv.run(func() error { return ended(outEx.Wait()) })
		srv.run(func() error { return ended(errEx.Wait()) })
		srv.run(func() error { return feed(ctx, r.input, inEx) })
	default:
		_ = srv.stop(ctx)
		return nil, fmt.Errorf("relay: unhandled stdio %T", r.remote)
	}
	return srv, nil
}

// feed copies the local input into a line stream's In command and is why the
// relay stopped: ErrLeft once the input ended and every complete line has
// reached the remote, the failure, or nil when the serving stopped first.
func feed(ctx context.Context, src *input, in shell.Execution) error {
	left, err := src.copyTo(ctx, in.Stdin)
	// Closing the input lets the command deliver what it was given and end;
	// stopping it earlier could lose the last line on its way.
	closeErr := in.Stdin.Close()
	waitErr := in.Wait()
	if left == nil {
		return err
	}
	if err := errors.Join(closeErr, waitErr); err != nil {
		return err
	}
	return left
}

// input is an IO relay's one reader of its local input. It outlives each
// serving, since a read cannot be cancelled: a reader of each serving's own
// would be left blocked when the serving stopped, and would take what the
// next serving should get. What a stopped serving took and did not write is
// held for the next one.
type input struct {
	src   io.Reader
	lines bool

	once   sync.Once
	chunks chan []byte
	ended  chan struct{}
	// why the input ended, set before ended is closed
	dropped int
	err     error

	mu   sync.Mutex
	held []byte
}

func newInput(src io.Reader, lines bool) *input {
	return &input{src: src, lines: lines, chunks: make(chan []byte), ended: make(chan struct{})}
}

// start starts reading, for the relay's first serving.
func (in *input) start() {
	if in.src == nil {
		return
	}
	in.once.Do(func() { go in.read() })
}

// read hands the input on in chunks until it ends: for a line stream one
// complete line at a time, each as soon as its newline arrives. A last line
// that the input ends without a newline is part of a message the client never
// finished, which the process must not receive: it is dropped and counted.
func (in *input) read() {
	if in.lines {
		r := bufio.NewReader(in.src)
		for {
			line, err := r.ReadBytes('\n')
			if err != nil {
				in.end(len(line), err)
				return
			}
			in.chunks <- line
		}
	}
	buf := make([]byte, inputChunk)
	for {
		n, err := in.src.Read(buf)
		if n > 0 {
			in.chunks <- bytes.Clone(buf[:n])
		}
		if err != nil {
			in.end(0, err)
			return
		}
	}
}

// inputChunk bounds one read of a terminal's input, in bytes.
const inputChunk = 32 << 10

func (in *input) end(dropped int, err error) {
	if errors.Is(err, io.EOF) {
		err = nil
	}
	in.dropped, in.err = dropped, err
	close(in.ended)
}

// copyTo writes the input to dst until it ends, which left reports as ErrLeft
// or the read's failure; err is a failed write. Both are nil when ctx ended
// first.
func (in *input) copyTo(ctx context.Context, dst io.Writer) (left, err error) {
	for {
		chunk := in.take()
		if chunk == nil {
			select {
			case chunk = <-in.chunks:
			case <-in.ended:
				return in.why(), nil
			case <-ctx.Done():
				return nil, nil
			}
		}
		if ctx.Err() != nil {
			in.hold(chunk)
			return nil, nil
		}
		n, err := dst.Write(chunk)
		if err != nil {
			// The In command passes a line on only once its newline arrives, so
			// the head of a line written before the failure never reached the
			// process: the next serving sends the whole line again.
			if !in.lines {
				chunk = chunk[n:]
			}
			in.hold(chunk)
			return nil, err
		}
	}
}

func (in *input) why() error {
	switch {
	case in.err != nil:
		return in.err
	case in.dropped > 0:
		return fmt.Errorf("%w inside a line: %d bytes left out", ErrLeft, in.dropped)
	}
	return ErrLeft
}

func (in *input) take() []byte {
	in.mu.Lock()
	defer in.mu.Unlock()
	chunk := in.held
	in.held = nil
	return chunk
}

func (in *input) hold(chunk []byte) {
	if len(chunk) == 0 {
		return
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	in.held = chunk
}

// ended is why a remote side stopped: its failure, or ErrEnded when it ended
// cleanly.
func ended(err error) error {
	if err != nil {
		return err
	}
	return ErrEnded
}

func orDiscard(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}

// lockedWriter serializes the writes of several commands into one stream, so
// their lines do not interleave mid-write.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
