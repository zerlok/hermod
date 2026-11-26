package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/zerlok/hermod/internal/session"
)

// read is the message the standard registry reads from doc, the params of
// method or, when result, the result of its answer.
func read(t *testing.T, method string, result bool, doc string) session.Message {
	t.Helper()
	m, ok := Standard().methods[method]
	if !ok {
		t.Fatalf("no method %s", method)
	}
	readDoc := m.params
	if result {
		readDoc = m.result
	}
	msg, err := readDoc([]byte(doc))
	if err != nil {
		t.Fatalf("read %s: %v", doc, err)
	}
	return msg
}

// written is msg's params or result as a Conn writes them.
func written(t *testing.T, msg session.Message) string {
	t.Helper()
	b, err := marshal(msg.(wire).params())
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	return string(b)
}

// sameJSON reports whether two documents say the same, however spaced.
func sameJSON(t *testing.T, got, want string) bool {
	t.Helper()
	var a, b any
	if err := json.Unmarshal([]byte(want), &b); err != nil {
		t.Fatalf("the expected document is no JSON: %v: %s", err, want)
	}
	return json.Unmarshal([]byte(got), &a) == nil && reflect.DeepEqual(a, b)
}

// safeBuffer is a log the connection's reader writes while a test reads it.
type safeBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// farEnd is the other side of a Conn: it writes the lines the Conn reads,
// and receives the lines the Conn writes.
type farEnd struct {
	conn  *Conn
	w     *io.PipeWriter
	lines chan string
	log   *safeBuffer
}

// dial is a Conn to peer, of the standard registry, and its far end.
func dial(t *testing.T, peer Peer) *farEnd {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	e := &farEnd{w: inW, lines: make(chan string, 64), log: &safeBuffer{}}
	ctx, cancel := context.WithCancel(context.Background())
	e.conn = NewConn(ctx, inR, outW, Standard(), peer, log.New(e.log, "", 0))
	go func() {
		sc := bufio.NewScanner(outR)
		for sc.Scan() {
			e.lines <- sc.Text()
		}
	}()
	t.Cleanup(func() {
		cancel()
		_ = inW.Close()
		_ = outW.Close()
	})
	return e
}

// returned is what a Call returned.
type returned struct {
	msg session.Message
	err error
}

// call calls msg on the Conn under ctx; what it returns arrives on the
// channel.
func (e *farEnd) call(t *testing.T, ctx context.Context, msg session.Message) <-chan returned {
	t.Helper()
	done := make(chan returned, 1)
	if err := e.conn.Call(ctx, msg, func(m session.Message, err error) { done <- returned{m, err} }); err != nil {
		t.Fatalf("Call: %v", err)
	}
	return done
}

// send writes line to the Conn.
func (e *farEnd) send(t *testing.T, line string) {
	t.Helper()
	if _, err := io.WriteString(e.w, line+"\n"); err != nil {
		t.Fatalf("send: %v", err)
	}
}

// next is the next line the Conn wrote.
func (e *farEnd) next(t *testing.T) string {
	t.Helper()
	select {
	case line := <-e.lines:
		return line
	case <-time.After(5 * time.Second):
		t.Fatal("the connection wrote nothing")
		return ""
	}
}

// inbound is the next message the Conn delivered.
func (e *farEnd) inbound(t *testing.T) session.Inbound {
	t.Helper()
	select {
	case in, ok := <-e.conn.Receive():
		if !ok {
			t.Fatalf("the connection ended: %v", e.conn.Err())
		}
		return in
	case <-time.After(5 * time.Second):
		t.Fatal("the connection delivered nothing")
		return session.Inbound{}
	}
}

// frameOf reads a line the Conn wrote.
func frameOf(t *testing.T, line string) frame {
	t.Helper()
	var f frame
	if err := json.Unmarshal([]byte(line), &f); err != nil {
		t.Fatalf("not JSON-RPC: %s", line)
	}
	return f
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func testLogger() *log.Logger { return log.New(io.Discard, "", 0) }
