package control

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"sync/atomic"
	"time"

	"github.com/zerlok/hermod/internal/pathmap"
	"github.com/zerlok/hermod/internal/sandbox"
)

// exitWindow is how a chat run fits the time its caller gives it to exit. The
// SDK closes Claude's stdin, sends SIGTERM 2 s later and SIGKILL 5 s after
// that; a run still settling then is killed with the mirror unsettled.
type exitWindow struct {
	// detach bounds how long the attach takes to end once the caller's stdin
	// has.
	detach time.Duration
	// settle bounds the mirror's settle after the attach, pauseReserve
	// included.
	settle time.Duration
	// pauseReserve is the end of settle kept for pausing the mirror when the
	// settle the liveness check chose has not finished: a pause loses nothing.
	pauseReserve time.Duration
}

// sdkWindow ends a chat run 5 s after the caller's stdin, inside the SDK's
// 7 s, leaving the rest for processes cut at the deadline to be reaped (see
// shell's WaitDelay).
var sdkWindow = exitWindow{detach: time.Second, settle: 4 * time.Second, pauseReserve: time.Second}

// attachChat joins a chat session as a stream. Each complete line of the
// caller's stdin reaches Claude with its local paths mapped to the sandbox, and
// each line Claude writes reaches the caller's stdout with sandbox paths mapped
// back, one line at a time as it comes. The attach ends when the caller's stdin
// does, given the window's detach time to wind down, when ctx is cancelled
// (SIGTERM), or when the connection drops; the session keeps running in each
// case.
func (t agentRun) attachChat(ctx context.Context, roots pathmap.Roots, fwd *sandbox.Forward) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// ssh reads a pipe of its own rather than a reader, which it would copy
	// from in a goroutine that the end of the attach could not stop while the
	// caller's stdin stays open.
	in, toSandbox, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("attach chat: %w", err)
	}
	defer in.Close()
	var detached atomic.Bool
	go func() {
		if err := pumpLines(toSandbox, orStdin(t.o.Stdio.In), roots.ToSandbox(), t.logger); err != nil {
			t.logger.Printf("stdin: %v", err)
		}
		_ = toSandbox.Close()
		grace := time.NewTimer(t.window.detach)
		defer grace.Stop()
		select {
		case <-grace.C:
			t.logger.Printf("stdin ended; the attach did not end within %v, cutting it", t.window.detach)
			detached.Store(true)
			cancel()
		case <-ctx.Done():
		}
	}()
	out := newLineWriter(t.o.Stdio.Out, roots.ToLocal(), t.logger)
	err = t.agent.AttachChat(ctx, fwd, sandbox.Streams{In: in, Out: out, Err: t.o.Stdio.Err})
	if detached.Load() {
		err = nil // cut short on purpose: stdin had ended
	}
	return errors.Join(err, out.Flush())
}

// pumpLines copies src to dst one complete line at a time, each mapped through
// m and written as soon as its newline arrives. A last line that src ends
// without a newline is dropped: it is part of a message the caller never
// finished, which Claude must not receive. It returns nil when src ends.
func pumpLines(dst io.Writer, src io.Reader, m pathmap.Map, logger *log.Logger) error {
	r := bufio.NewReader(src)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			if len(line) > 0 {
				logger.Printf("stdin ended inside a line: %d bytes left out", len(line))
			}
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if _, err := dst.Write(mapLine(m, line, logger)); err != nil {
			return err
		}
	}
}

// lineWriter maps what is written to it line by line through m and writes each
// complete line to w at once, keeping a partial line until its newline comes or
// Flush is called.
type lineWriter struct {
	w       io.Writer
	m       pathmap.Map
	logger  *log.Logger
	pending []byte
}

func newLineWriter(w io.Writer, m pathmap.Map, logger *log.Logger) *lineWriter {
	return &lineWriter{w: w, m: m, logger: logger}
}

func (l *lineWriter) Write(p []byte) (int, error) {
	l.pending = append(l.pending, p...)
	for {
		i := bytes.IndexByte(l.pending, '\n')
		if i < 0 {
			return len(p), nil
		}
		if _, err := l.w.Write(mapLine(l.m, l.pending[:i+1], l.logger)); err != nil {
			return 0, err
		}
		l.pending = append(l.pending[:0], l.pending[i+1:]...)
	}
}

// Flush writes the partial line kept, if any: the stream ended without its
// newline, and the caller still gets what Claude wrote.
func (l *lineWriter) Flush() error {
	if len(l.pending) == 0 {
		return nil
	}
	_, err := l.w.Write(mapLine(l.m, l.pending, l.logger))
	l.pending = nil
	return err
}

// mapLine maps one stream-json line, passing a line that is not JSON through
// unchanged: the stream is Claude's and the SDK's to judge, not Hermod's.
func mapLine(m pathmap.Map, line []byte, logger *log.Logger) []byte {
	mapped, err := m.Line(line)
	if err != nil {
		logger.Printf("a line passed unmapped: %v", err)
		return line
	}
	return mapped
}

func orStdin(r io.Reader) io.Reader {
	if r == nil {
		return os.Stdin
	}
	return r
}
