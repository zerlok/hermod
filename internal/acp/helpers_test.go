package acp

import (
	"encoding/json"
	"io"
	"log"
	"reflect"
	"strings"
	"testing"

	"github.com/zerlok/hermod/internal/session"
)

// asked is the lookup of a side that sent, or received, one request, method,
// under any ID; "" for none.
func asked(method string) func(session.MessageID) (string, bool) {
	return func(session.MessageID) (string, bool) { return method, method != "" }
}

// read is the message the standard serializer reads from line.
func read(t *testing.T, from Peer, method, line string) session.Message {
	t.Helper()
	msg, err := NewStandard().Decode([]byte(line), from, asked(method))
	if err != nil {
		t.Fatalf("Decode(%s): %v", line, err)
	}
	return msg
}

// written is the line the standard serializer writes for msg, on a side that
// received no request.
func written(t *testing.T, msg session.Message) string {
	t.Helper()
	return answering(t, "", msg)
}

// answering is the line the standard serializer writes for msg, on a side that
// received one request, method, under any ID.
func answering(t *testing.T, method string, msg session.Message) string {
	t.Helper()
	line, err := NewStandard().Encode(msg, asked(method))
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	return string(line)
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

// num and text are the IDs a table writes.
func num(n int64) session.MessageID   { return session.NumberID(n) }
func text(s string) session.MessageID { return session.TextID(s) }

// lines is the lines of written, without their newlines.
func lines(written string) []string {
	var out []string
	for line := range strings.SplitSeq(written, "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func discard() *log.Logger { return log.New(io.Discard, "", 0) }
