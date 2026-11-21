package control

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/zerlok/hermod/internal/pathmap"
)

func streamRoots(t *testing.T) pathmap.Roots {
	t.Helper()
	roots, err := pathmap.New(pathmap.Pair{Local: projectDir, Sandbox: sandboxHome + "/" + sandboxDir})
	if err != nil {
		t.Fatal(err)
	}
	return roots
}

func TestPumpLines(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantLog string // "" when nothing is logged
	}{
		{
			"lines mapped to the sandbox",
			`{"cwd":"/home/me/dev/api/x"}` + "\n" + `{"type":"user"}` + "\r\n",
			`{"cwd":"/home/u/dev/api/x"}` + "\n" + `{"type":"user"}` + "\r\n",
			"",
		},
		{
			"an incomplete last line left out",
			`{"a":1}` + "\n" + `{"cwd":"/home/me/dev`,
			`{"a":1}` + "\n",
			"stdin ended inside a line: 20 bytes left out",
		},
		{"a line that is not JSON passed as it is", "not json /home/me/dev/api\n", "not json /home/me/dev/api\n", "a line passed unmapped"},
		{"a blank line passed", "\n", "\n", ""},
		{"nothing", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out, logs bytes.Buffer
			if err := pumpLines(&out, strings.NewReader(tc.in), streamRoots(t).ToSandbox(), log.New(&logs, "", 0)); err != nil {
				t.Fatalf("pumpLines() error: %v", err)
			}
			if out.String() != tc.want {
				t.Errorf("out = %q, want %q", out.String(), tc.want)
			}
			if (tc.wantLog == "") != (logs.Len() == 0) || !strings.Contains(logs.String(), tc.wantLog) {
				t.Errorf("log = %q, want %q", logs.String(), tc.wantLog)
			}
		})
	}
}

// TestPumpLinesUnbuffered asserts each line is passed on as soon as its
// newline arrives, before the next one is written.
func TestPumpLinesUnbuffered(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
		want  []string
	}{
		{"one at a time", []string{`{"a":"/home/me/dev/api"}` + "\n", `{"b":2}` + "\n"}, []string{`{"a":"/home/u/dev/api"}` + "\n", `{"b":2}` + "\n"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src, feed := io.Pipe()
			sink, dst := io.Pipe()
			done := make(chan error, 1)
			go func() {
				done <- pumpLines(dst, src, streamRoots(t).ToSandbox(), log.New(io.Discard, "", 0))
				_ = dst.Close()
			}()
			got := bufio.NewReader(sink)
			for i, line := range tc.lines {
				if _, err := io.WriteString(feed, line); err != nil {
					t.Fatal(err)
				}
				read, err := readLineWithin(got, time.Second)
				if err != nil || read != tc.want[i] {
					t.Fatalf("line %d = %q, %v, want %q before the next is written", i, read, err, tc.want[i])
				}
			}
			_ = feed.Close()
			if err := <-done; err != nil {
				t.Errorf("pumpLines() error: %v", err)
			}
		})
	}
}

// TestPumpLinesWriteFailure asserts a pump whose destination is gone ends
// with the failure instead of reading on.
func TestPumpLinesWriteFailure(t *testing.T) {
	gone := errors.New("closed pipe")
	cases := []struct {
		name    string
		in      string
		wantErr error
	}{
		{"destination gone", "{}\n{}\n", gone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := pumpLines(failingWriter{gone}, strings.NewReader(tc.in), pathmap.Map{}, log.New(io.Discard, "", 0))
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("pumpLines() = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// TestLineWriter asserts what Claude writes reaches the caller one mapped line
// per write, as soon as its newline arrives, and a last partial line on Flush.
func TestLineWriter(t *testing.T) {
	cases := []struct {
		name      string
		writes    []string
		wantAfter []string // what the caller has after each write
		wantFlush string   // what it has after Flush
	}{
		{
			"a line split across writes",
			[]string{`{"p":"/home/u/de`, `v/api/x"}` + "\n" + `{"q"`, `:1}` + "\n"},
			[]string{"", `{"p":"/home/me/dev/api/x"}` + "\n", `{"p":"/home/me/dev/api/x"}` + "\n" + `{"q":1}` + "\n"},
			`{"p":"/home/me/dev/api/x"}` + "\n" + `{"q":1}` + "\n",
		},
		{
			"several lines in one write",
			[]string{`{"a":1}` + "\n" + `{"b":"/home/u/dev/api"}` + "\n"},
			[]string{`{"a":1}` + "\n" + `{"b":"/home/me/dev/api"}` + "\n"},
			`{"a":1}` + "\n" + `{"b":"/home/me/dev/api"}` + "\n",
		},
		{
			"a last line without its newline",
			[]string{`{"a":1}` + "\n" + `{"b":"/home/u/dev/api"}`},
			[]string{`{"a":1}` + "\n"},
			`{"a":1}` + "\n" + `{"b":"/home/me/dev/api"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			w := newLineWriter(&out, streamRoots(t).ToLocal(), log.New(io.Discard, "", 0))
			for i, chunk := range tc.writes {
				if n, err := w.Write([]byte(chunk)); n != len(chunk) || err != nil {
					t.Fatalf("Write() = %d, %v", n, err)
				}
				if out.String() != tc.wantAfter[i] {
					t.Errorf("after write %d: %q, want %q", i, out.String(), tc.wantAfter[i])
				}
			}
			if err := w.Flush(); err != nil {
				t.Fatalf("Flush() error: %v", err)
			}
			if out.String() != tc.wantFlush {
				t.Errorf("after Flush: %q, want %q", out.String(), tc.wantFlush)
			}
		})
	}
}

func TestLineWriterFailure(t *testing.T) {
	gone := errors.New("caller gone")
	cases := []struct {
		name    string
		write   string
		wantErr error
	}{
		{"complete line", "{}\n", gone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newLineWriter(failingWriter{gone}, pathmap.Map{}, log.New(io.Discard, "", 0))
			if _, err := w.Write([]byte(tc.write)); !errors.Is(err, tc.wantErr) {
				t.Errorf("Write() = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

type failingWriter struct{ err error }

func (f failingWriter) Write([]byte) (int, error) { return 0, f.err }

func readLineWithin(r *bufio.Reader, d time.Duration) (string, error) {
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := r.ReadString('\n')
		ch <- result{line, err}
	}()
	select {
	case res := <-ch:
		return res.line, res.err
	case <-time.After(d):
		return "", errors.New("no line in time")
	}
}
