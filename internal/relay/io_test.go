package relay

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/zerlok/hermod/internal/shell"
)

// syncBuffer is a stream a test reads while a relay may still write it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// fifo plays a line stream's In command: what it reads is what reached the
// process's FIFO.
type fifo struct{ syncBuffer }

func (f *fifo) script(_ context.Context, in io.Reader, _, _ io.Writer) error {
	_, err := io.Copy(f, in)
	return err
}

// ends is a process that writes text to its stdout and ends with err.
func ends(text string, err error) script {
	return func(_ context.Context, _ io.Reader, out, _ io.Writer) error {
		_, _ = io.WriteString(out, text)
		return err
	}
}

var lineStdio = Lines{
	In:  shell.Command{Args: []string{"in"}},
	Out: shell.Command{Args: []string{"out"}},
	Err: shell.Command{Args: []string{"err"}},
}

func TestLines(t *testing.T) {
	cases := []struct {
		name       string
		input      string
		inputOpen  bool // the local side stays, its input never ending
		out, errs  script
		wantFIFO   string
		wantOut    string
		wantErrOut string
		wantErr    error
		wantMsg    string // a part of the error's text, when it says more
	}{
		{
			name:  "complete lines reach the FIFO, and the local side leaves",
			input: "a\nb\n", out: writes("log\n"), errs: writes("warn\n"),
			wantFIFO: "a\nb\n", wantOut: "log\n", wantErrOut: "warn\n", wantErr: ErrLeft,
		},
		{
			name:  "a partial final line never reaches the FIFO",
			input: "a\n{\"half\":", out: untilDone, errs: untilDone,
			wantFIFO: "a\n", wantErr: ErrLeft, wantMsg: "8 bytes left out",
		},
		{
			name:  "no input at all",
			input: "", out: untilDone, errs: untilDone,
			wantErr: ErrLeft,
		},
		{
			name:      "the process ends while the local side stays",
			inputOpen: true, out: ends("bye\n", nil), errs: untilDone,
			wantOut: "bye\n", wantErr: ErrEnded,
		},
		{
			name:      "the log tail fails",
			inputOpen: true, out: ends("", errFake), errs: untilDone,
			wantErr: errFake,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var fifo fifo
			ex := &fakeExec{answer: func(args []string) script {
				switch args[0] {
				case "in":
					return fifo.script
				case "out":
					return tc.out
				}
				return tc.errs
			}}
			var in io.Reader = strings.NewReader(tc.input)
			if tc.inputOpen {
				r, w := io.Pipe()
				t.Cleanup(func() { _ = w.Close() })
				in = r
			}
			var out, errOut syncBuffer
			l, err := NewIO(ex, t.TempDir(), "hermod-io", lineStdio, Streams{In: in, Out: &out, Err: &errOut}).Connect(context.Background())
			if err != nil {
				t.Fatalf("Connect() error: %v", err)
			}
			<-l.Done()
			gotErr := l.Err()
			if err := l.Suspend(context.Background()); err != nil {
				t.Fatalf("Suspend() error: %v", err)
			}
			if !errors.Is(gotErr, tc.wantErr) || !strings.Contains(gotErr.Error(), tc.wantMsg) {
				t.Errorf("Err() = %v, want %v saying %q", gotErr, tc.wantErr, tc.wantMsg)
			}
			if got := fifo.String(); got != tc.wantFIFO {
				t.Errorf("FIFO got %q, want %q", got, tc.wantFIFO)
			}
			if got := out.String(); got != tc.wantOut {
				t.Errorf("stdout got %q, want %q", got, tc.wantOut)
			}
			if got := errOut.String(); got != tc.wantErrOut {
				t.Errorf("stderr got %q, want %q", got, tc.wantErrOut)
			}
		})
	}
}

func TestLinesSingleReader(t *testing.T) {
	cases := []struct {
		name      string
		giveUp    bool // the first reader gives its lease up before the second asks
		wantInUse bool
	}{
		{"a second reader while the first holds it is refused", false, true},
		{"a reader after the first left is granted", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ex := &fakeExec{answer: func([]string) script { return untilDone }}
			locks := t.TempDir()
			newRelay := func() *IO {
				r, w := io.Pipe()
				t.Cleanup(func() { _ = w.Close() })
				return NewIO(ex, locks, "hermod-io", lineStdio, Streams{In: r})
			}
			first, err := newRelay().Connect(context.Background())
			if err != nil {
				t.Fatalf("first Connect() error: %v", err)
			}
			defer first.Suspend(context.Background())
			if tc.giveUp {
				if err := first.Suspend(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			second, err := newRelay().Connect(context.Background())
			if errors.Is(err, ErrInUse) != tc.wantInUse {
				t.Fatalf("second Connect() error = %v, want refused as in use: %v", err, tc.wantInUse)
			}
			if second != nil {
				_ = second.Suspend(context.Background())
			}
		})
	}
}

func TestTerminal(t *testing.T) {
	// echo is a terminal that writes back what it is given and ends with err
	// when its input does.
	echo := func(err error) script {
		return func(_ context.Context, in io.Reader, out, _ io.Writer) error {
			if _, cerr := io.Copy(out, in); cerr != nil {
				return cerr
			}
			return err
		}
	}
	cases := []struct {
		name      string
		input     string
		inputOpen bool // the local side stays, its input never ending
		attach    script
		wantOut   string
		wantErr   error
	}{
		{"copies both ways until the local side leaves", "ls\n", false, echo(nil), "ls\n", ErrLeft},
		{"the process ends while the local side stays", "", true, ends("bye\n", nil), "bye\n", ErrEnded},
		{"an attach that fails", "", true, ends("", errFake), "", errFake},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ex := &fakeExec{answer: func([]string) script { return tc.attach }}
			var in io.Reader = strings.NewReader(tc.input)
			if tc.inputOpen {
				r, w := io.Pipe()
				t.Cleanup(func() { _ = w.Close() })
				in = r
			}
			var out syncBuffer
			stdio := Terminal{Attach: shell.Command{Args: []string{"tmux", "attach", "-t", "=hermod-w"}}}
			l, err := NewIO(ex, t.TempDir(), "hermod-io", stdio, Streams{In: in, Out: &out}).Connect(context.Background())
			if err != nil {
				t.Fatalf("Connect() error: %v", err)
			}
			<-l.Done()
			if err := l.Err(); !errors.Is(err, tc.wantErr) {
				t.Errorf("Err() = %v, want %v", err, tc.wantErr)
			}
			if err := l.Release(context.Background()); err != nil {
				t.Fatalf("Release() error: %v", err)
			}
			if got := out.String(); got != tc.wantOut {
				t.Errorf("stdout got %q, want %q", got, tc.wantOut)
			}
			if got := ex.running(); got != 0 {
				t.Errorf("%d commands still running after Release", got)
			}
		})
	}
}

func TestLinesReconnect(t *testing.T) {
	// breaks is an In command that reads n bytes of a line and fails, never
	// passing the head on, since its line has no newline yet.
	breaks := func(n int) script {
		return func(_ context.Context, in io.Reader, _, _ io.Writer) error {
			_, _ = io.ReadFull(in, make([]byte, n))
			return errFake
		}
	}
	cases := []struct {
		name       string
		firstIn    script // the first serving's In command; nil reads into the FIFO
		firstOut   script // the first serving's log tail
		before     string // written while the first lease is held
		waitFirst  bool   // the first lease is given up once its relay stopped on its own
		wantFirst  error  // why the first serving stopped
		lines      string // written after the first lease was given up, then the input ends
		wantFIFO   string
		wantSecond error // why the second serving stopped
	}{
		{
			name:    "a line whose write broke off reaches the next serving whole",
			firstIn: breaks(3), firstOut: untilDone, before: "{\"a\":1}\n", waitFirst: true, wantFirst: io.ErrClosedPipe,
			lines: "b\n", wantFIFO: "{\"a\":1}\nb\n", wantSecond: ErrLeft,
		},
		{
			name:     "lines written while suspended reach the next serving",
			firstOut: untilDone, lines: "a\nb\n", wantFIFO: "a\nb\n", wantSecond: ErrLeft,
		},
		{
			name:     "lines written after the process ended reach the next serving",
			firstOut: ends("", nil), waitFirst: true, wantFirst: ErrEnded,
			lines: "a\nb\n", wantFIFO: "a\nb\n", wantSecond: ErrLeft,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var fifo fifo
			var (
				mu        sync.Mutex
				outs, ins int
			)
			ex := &fakeExec{answer: func(args []string) script {
				switch args[0] {
				case "in":
					mu.Lock()
					defer mu.Unlock()
					ins++
					if ins == 1 && tc.firstIn != nil {
						return tc.firstIn
					}
					return fifo.script
				case "out":
					mu.Lock()
					defer mu.Unlock()
					outs++
					if outs == 1 {
						return tc.firstOut
					}
				}
				return untilDone
			}}
			r, w := io.Pipe()
			relay := NewIO(ex, t.TempDir(), "hermod-io", lineStdio, Streams{In: r})
			first, err := relay.Connect(context.Background())
			if err != nil {
				t.Fatalf("first Connect() error: %v", err)
			}
			if tc.before != "" {
				go func() { _, _ = io.WriteString(w, tc.before) }()
			}
			if tc.waitFirst {
				<-first.Done()
			}
			if err := first.Err(); !errors.Is(err, tc.wantFirst) {
				t.Errorf("first Err() = %v, want %v", err, tc.wantFirst)
			}
			if err := first.Suspend(context.Background()); err != nil {
				t.Fatalf("Suspend() error: %v", err)
			}
			go func() {
				_, _ = io.WriteString(w, tc.lines)
				_ = w.Close()
			}()
			second, err := relay.Connect(context.Background())
			if err != nil {
				t.Fatalf("second Connect() error: %v", err)
			}
			<-second.Done()
			if err := second.Err(); !errors.Is(err, tc.wantSecond) {
				t.Errorf("second Err() = %v, want %v", err, tc.wantSecond)
			}
			if err := second.Suspend(context.Background()); err != nil {
				t.Fatalf("second Suspend() error: %v", err)
			}
			if got := fifo.String(); got != tc.wantFIFO {
				t.Errorf("FIFO got %q, want %q", got, tc.wantFIFO)
			}
		})
	}
}
