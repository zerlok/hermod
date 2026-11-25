package relay

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/zerlok/hermod/internal/shell"
)

// step is one move of a relay's holders, and what it leaves.
type step struct {
	do      string // "connect", "suspend", "release", or "drop": the remote side ends under the holder's lease
	holder  string
	wantRan [][]string // the commands the step ran that change a state
	want    State
}

// subject is one kind of relay under test, with how its state is read from
// the outside.
type subject struct {
	relay   Relay
	changes func() [][]string
	state   func(t *testing.T) State
	drop    func() // ends the remote side that serves the relay; nil for a kind with nothing serving it
}

// droppable is a long-running process that writes text and runs until its
// context ends or it is dropped, when it fails.
func droppable(text string, drop <-chan struct{}) script {
	return func(ctx context.Context, _ io.Reader, out, _ io.Writer) error {
		if _, err := io.WriteString(out, text); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-drop:
			return errFake
		}
	}
}

func fsSubject(t *testing.T, locks string) subject {
	m := newFakeMutagen(nil, "")
	return subject{
		relay:   NewFS(m.exec, m.exec, locks, testRoots, testName, nil, testLabels...),
		changes: m.changes,
		state: func(t *testing.T) State {
			listed, err := List(context.Background(), m.exec, locks, testLabels[1])
			if err != nil {
				t.Fatal(err)
			}
			m.changes()
			for _, l := range listed {
				if l.Name == testName {
					return l.State
				}
			}
			return Absent
		},
	}
}

// An IO relay buffers in its remote stdio, which outlives the relay and is
// the worker's: unleased, it reads as Disconnected whichever way it was given
// up, and the worker decides when the buffer goes.
func ioSubject(t *testing.T, locks string) subject {
	drop := make(chan struct{})
	ex := &fakeExec{answer: func([]string) script { return droppable("", drop) }}
	return subject{
		drop:    func() { drop <- struct{}{} },
		relay:   NewIO(ex, locks, "hermod-io", Terminal{Attach: shell.Command{Args: []string{"tmux", "attach", "-t", "=hermod-w"}}}, Streams{}),
		changes: ex.commands,
		state: func(t *testing.T) State {
			held, err := leased(locks, "hermod-io")
			if err != nil {
				t.Fatal(err)
			}
			if held {
				return Connected
			}
			return Disconnected
		},
	}
}

func netSubject(t *testing.T, locks string) subject {
	drop := make(chan struct{})
	ex := &fakeExec{answer: func([]string) script { return droppable(readyLine+"\n", drop) }}
	return subject{
		drop:    func() { drop <- struct{}{} },
		relay:   NewNet(ex, locks, "hermod-net", "prod-box", 45001, "127.0.0.1:1", nil),
		changes: ex.commands,
		state: func(t *testing.T) State {
			held, err := leased(locks, "hermod-net")
			if err != nil {
				t.Fatal(err)
			}
			switch {
			case held && ex.running() == 1:
				return Connected
			case !held && ex.running() == 0:
				return Absent
			}
			return 0 // leases and connections disagree: no state
		},
	}
}

var netForward = []string{"ssh", "-o", "ControlPath=none", "-o", "ExitOnForwardFailure=yes",
	"-R", "45001:127.0.0.1:PORT", "prod-box", "sh -c 'echo " + readyLine + " && exec cat >/dev/null'"}

func TestStates(t *testing.T) {
	var (
		fsCreate = [][]string{
			{"ssh", "prod-box", "mkdir -p -- /home/u/dev/api"},
			{"mkdir", "-p", "--", "/home/me/dev/api"},
			{"mutagen", "sync", "create", "--name", testName,
				"--label", "hermod-remote=prod-box", "--label", "hermod-workspace=" + testName,
				"--mode", "two-way-safe", "/home/me/dev/api", "prod-box:/home/u/dev/api"},
		}
		fsPause     = [][]string{{"mutagen", "sync", "pause", testName}}
		fsResume    = [][]string{{"mutagen", "sync", "resume", testName}, {"mutagen", "sync", "flush", testName}}
		fsTerminate = [][]string{{"mutagen", "sync", "flush", testName}, {"mutagen", "sync", "terminate", testName}}
		ioAttach    = [][]string{{"tmux", "attach", "-t", "=hermod-w"}}
		netConnect  = [][]string{netForward}
	)
	cases := []struct {
		name  string
		kind  func(*testing.T, string) subject
		steps []step
	}{
		{"FS: suspended by the last of two holders, then connected and released", fsSubject, []step{
			{"connect", "a", fsCreate, Connected},
			{"connect", "b", nil, Connected},
			{"suspend", "a", nil, Connected},
			{"suspend", "b", fsPause, Disconnected},
			{"connect", "a", fsResume, Connected},
			{"release", "a", fsTerminate, Absent},
		}},
		{"FS: released by the first of two holders, then by the last", fsSubject, []step{
			{"connect", "a", fsCreate, Connected},
			{"connect", "b", nil, Connected},
			{"release", "a", nil, Connected},
			{"release", "b", fsTerminate, Absent},
		}},
		{"IO: suspended by the last of two holders, then connected and released", ioSubject, []step{
			{"connect", "a", ioAttach, Connected},
			{"connect", "b", nil, Connected},
			{"suspend", "a", nil, Connected},
			{"suspend", "b", nil, Disconnected},
			{"connect", "a", ioAttach, Connected},
			{"release", "a", nil, Disconnected},
		}},
		{"IO: a process that ended under two holders is attached anew for the next lease", ioSubject, []step{
			{"connect", "a", ioAttach, Connected},
			{"connect", "b", nil, Connected},
			{"drop", "a", nil, Connected},
			{"connect", "c", ioAttach, Connected},
			{"suspend", "a", nil, Connected},
			{"suspend", "b", nil, Connected},
			{"suspend", "c", nil, Disconnected},
		}},
		{"Net: a connection dropped under two holders is made anew for the next lease", netSubject, []step{
			{"connect", "a", netConnect, Connected},
			{"connect", "b", nil, Connected},
			{"drop", "a", nil, 0},
			{"connect", "c", netConnect, Connected},
			{"release", "a", nil, Connected},
			{"release", "b", nil, Connected},
			{"release", "c", nil, Absent},
		}},
		{"Net: suspending the last of two holders removes it, as releasing does", netSubject, []step{
			{"connect", "a", netConnect, Connected},
			{"connect", "b", nil, Connected},
			{"suspend", "a", nil, Connected},
			{"suspend", "b", nil, Absent},
			{"connect", "a", netConnect, Connected},
			{"release", "a", nil, Absent},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.kind(t, t.TempDir())
			held := map[string]Lease{}
			t.Cleanup(func() {
				for _, l := range held {
					_ = l.Release(context.Background())
				}
			})
			for i, st := range tc.steps {
				var err error
				switch st.do {
				case "connect":
					held[st.holder], err = s.relay.Connect(context.Background())
					if err == nil && isDone(held[st.holder]) {
						t.Errorf("step %d: connect by %s returned a lease that is already done", i, st.holder)
					}
				case "drop":
					s.drop()
					<-held[st.holder].Done()
				case "suspend":
					err = held[st.holder].Suspend(context.Background())
					delete(held, st.holder)
				case "release":
					err = held[st.holder].Release(context.Background())
					delete(held, st.holder)
				}
				if err != nil {
					t.Fatalf("step %d: %s by %s: %v", i, st.do, st.holder, err)
				}
				if got := s.changes(); !reflect.DeepEqual(got, st.wantRan) {
					t.Errorf("step %d: %s by %s ran %q, want %q", i, st.do, st.holder, got, st.wantRan)
				}
				if got := s.state(t); got != st.want {
					t.Errorf("step %d: %s by %s left %v, want %v", i, st.do, st.holder, got, st.want)
				}
			}
		})
	}
}

func isDone(l Lease) bool {
	select {
	case <-l.Done():
		return true
	default:
		return false
	}
}

func TestLeaseGivenUpTwice(t *testing.T) {
	cases := []struct {
		name   string
		first  string
		second string
	}{
		{"suspended, then released", "suspend", "release"},
		{"released, then suspended", "release", "suspend"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newFakeMutagen(nil, "")
			l, err := NewFS(m.exec, m.exec, t.TempDir(), testRoots, testName, nil).Connect(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			giveUp := map[string]func(context.Context) error{"suspend": l.Suspend, "release": l.Release}
			if err := giveUp[tc.first](context.Background()); err != nil {
				t.Fatalf("%s: %v", tc.first, err)
			}
			m.changes()
			if err := giveUp[tc.second](context.Background()); !errors.Is(err, errGivenUp) {
				t.Errorf("%s after %s: error = %v, want %v", tc.second, tc.first, err, errGivenUp)
			}
			if got := m.changes(); got != nil {
				t.Errorf("a lease given up twice ran %q", got)
			}
		})
	}
}

// lockOp is one step of a lock scenario, by a named holder. Every holder is
// its own open file, which flock treats as its own process.
type lockOp struct {
	do        string // "take", "take-exclusive", "last" or "close"
	holder    string
	want      bool // take: granted; last: the holder is the last
	wantInUse bool // take: refused as a line stream already read
}

func TestLock(t *testing.T) {
	cases := []struct {
		name string
		ops  []lockOp
	}{
		{"a sole holder is the last", []lockOp{
			{"take", "a", true, false}, {"last", "a", true, false},
		}},
		{"of two holders the first to leave is not the last, the second is", []lockOp{
			{"take", "a", true, false}, {"take", "b", true, false},
			{"last", "a", false, false}, {"close", "a", false, false},
			{"last", "b", true, false},
		}},
		{"two holders leaving at once: the last to try is the last", []lockOp{
			{"take", "a", true, false}, {"take", "b", true, false},
			{"last", "a", false, false}, {"last", "b", true, false},
		}},
		{"a holder that ends without giving up leaves no lease behind", []lockOp{
			{"take", "a", true, false}, {"take", "b", true, false},
			{"close", "a", false, false},
			{"last", "b", true, false},
		}},
		{"a new holder waits while the last one settles", []lockOp{
			{"take", "a", true, false}, {"last", "a", true, false},
			{"take", "b", false, false},
			{"close", "a", false, false},
			{"take", "b", true, false}, {"last", "b", true, false},
		}},
		{"a second single reader is refused at once", []lockOp{
			{"take-exclusive", "a", true, false},
			{"take-exclusive", "b", false, true},
			{"last", "a", true, false}, {"close", "a", false, false},
			{"take-exclusive", "b", true, false},
		}},
		{"a single reader is refused while a shared lease is held", []lockOp{
			{"take", "a", true, false},
			{"take-exclusive", "b", false, true},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "lease")
			held := map[string]*lock{}
			t.Cleanup(func() {
				for _, l := range held {
					_ = l.close()
				}
			})
			for i, op := range tc.ops {
				switch op.do {
				case "take", "take-exclusive":
					ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
					l, err := takeLock(ctx, dir, testName, op.do == "take-exclusive")
					cancel()
					if (err == nil) != op.want {
						t.Fatalf("step %d: %s by %s: err = %v, want granted %v", i, op.do, op.holder, err, op.want)
					}
					if errors.Is(err, ErrInUse) != op.wantInUse {
						t.Fatalf("step %d: %s by %s: err = %v, want refused as in use %v", i, op.do, op.holder, err, op.wantInUse)
					}
					if err == nil {
						held[op.holder] = l
					}
				case "last":
					last, err := held[op.holder].last()
					if err != nil || last != op.want {
						t.Fatalf("step %d: last by %s = %v, %v, want %v", i, op.holder, last, err, op.want)
					}
				case "close":
					if err := held[op.holder].close(); err != nil {
						t.Fatalf("step %d: close by %s: %v", i, op.holder, err)
					}
					delete(held, op.holder)
				}
			}
		})
	}
}

func TestLockFile(t *testing.T) {
	cases := []struct {
		name     string
		dir      string // under a temporary directory
		wantMode os.FileMode
	}{
		{"made private, parents included", "run/hermod/lease", 0o700},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), tc.dir)
			l, err := takeLock(context.Background(), dir, testName, false)
			if err != nil {
				t.Fatalf("takeLock() error: %v", err)
			}
			defer l.close()
			info, err := os.Stat(dir)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != tc.wantMode {
				t.Errorf("lease directory mode = %v, want %v", got, tc.wantMode)
			}
			if _, err := os.Stat(filepath.Join(dir, testName)); err != nil {
				t.Errorf("lease file: %v", err)
			}
		})
	}
}

func TestStateString(t *testing.T) {
	cases := []struct {
		state State
		want  string
	}{
		{Absent, "absent"},
		{Disconnected, "disconnected"},
		{Connected, "connected"},
		{0, "State(0)"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			if got := tc.state.String(); got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}
