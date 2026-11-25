package relay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/zerlok/hermod/internal/relay/relaytest"
	"github.com/zerlok/hermod/internal/shell"
)

// fakeSession is one Mutagen session as fakeMutagen keeps it.
type fakeSession struct {
	local, beta string
	paused      bool
	labels      []Label
}

// fakeMutagen plays the mutagen commands an FS relay runs against sessions it
// keeps by name, "" for one made with no name, and records every command that changes a state; failing names a
// command whose every run fails.
type fakeMutagen struct {
	mu       sync.Mutex
	sessions map[string]*fakeSession
	failing  string
	exec     *fakeExec
	monitors relaytest.Monitors
}

var errFake = errors.New("exit status 1")

func newFakeMutagen(sessions map[string]*fakeSession, failing string) *fakeMutagen {
	if sessions == nil {
		sessions = map[string]*fakeSession{}
	}
	m := &fakeMutagen{sessions: sessions, failing: failing}
	m.exec = &fakeExec{answer: m.answer}
	return m
}

// changes is what the relay ran since the last call that changes a state:
// every command but a listing and a watch.
func (m *fakeMutagen) changes() [][]string {
	var out [][]string
	for _, args := range m.exec.commands() {
		if len(args) > 2 && args[0] == "mutagen" && (args[2] == "list" || args[2] == "monitor") {
			continue
		}
		out = append(out, args)
	}
	return out
}

func (m *fakeMutagen) answer(args []string) script {
	return func(ctx context.Context, _ io.Reader, out, errOut io.Writer) error {
		if m.failing != "" && strings.HasPrefix(strings.Join(args, " "), m.failing) {
			_, _ = io.WriteString(errOut, "failed as asked\n")
			return errFake
		}
		if args[0] != "mutagen" {
			return nil
		}
		verb, rest := args[2], args[3:]
		if verb == "monitor" {
			exists := func(name string) bool { _, ok := m.sessions[name]; return ok }
			return m.monitors.Run(ctx, rest, &m.mu, exists, errOut, errFake)
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		switch verb {
		case "list":
			return m.list(rest, out, errOut)
		case "create":
			s := &fakeSession{local: rest[len(rest)-2], beta: rest[len(rest)-1]}
			for i := 0; i+1 < len(rest); i++ {
				if rest[i] == "--label" {
					k, v, _ := strings.Cut(rest[i+1], "=")
					s.labels = append(s.labels, Label{k, v})
				}
			}
			m.sessions[rest[1]] = s
		case "pause", "resume", "terminate":
			s, ok := m.sessions[rest[0]]
			if !ok {
				relaytest.NotFound(errOut)
				return errFake
			}
			s.paused = verb == "pause"
			if verb == "terminate" {
				delete(m.sessions, rest[0])
				m.monitors.End(rest[0])
			}
		}
		return nil
	}
}

func (m *fakeMutagen) list(rest []string, out, errOut io.Writer) error {
	l, ok := relaytest.ReadList(rest)
	if !ok {
		_, _ = fmt.Fprintf(errOut, "Error: unexpected listing %q\n", rest)
		return errFake
	}
	var kept []relaytest.Session
	for key, s := range m.sessions {
		var labels map[string]string
		for _, label := range s.labels {
			if labels == nil {
				labels = map[string]string{}
			}
			labels[label.Key] = label.Value
		}
		kept = append(kept, relaytest.Session{Name: key, Local: s.local, Beta: s.beta, Paused: s.paused, Labels: labels})
	}
	if err := relaytest.Answer(l, kept, out, errOut); err != nil {
		return errFake
	}
	return nil
}

var (
	testRoots  = Roots{Local: "/home/me/dev/api", Host: "prod-box", Remote: "/home/u/dev/api"}
	testName   = "hermod-prod-box-home-u-dev-api-1a2b3c4d"
	testLabels = []Label{{"hermod-remote", "prod-box"}, {"hermod-workspace", testName}}
)

func TestFSConnect(t *testing.T) {
	cases := []struct {
		name     string
		sessions map[string]*fakeSession
		failing  string
		wantRan  [][]string
		wantErr  bool
		wantType any // the type of error the connect fails with, when it matters
	}{
		{
			name: "none: both roots made, then the session",
			wantRan: [][]string{
				{"ssh", "prod-box", "mkdir -p -- /home/u/dev/api"},
				{"mkdir", "-p", "--", "/home/me/dev/api"},
				{"mutagen", "sync", "create", "--name", testName,
					"--label", "hermod-remote=prod-box", "--label", "hermod-workspace=" + testName,
					"--mode", "two-way-safe", "/home/me/dev/api", "prod-box:/home/u/dev/api"},
			},
		},
		{
			name:     "paused: resumed and flushed",
			sessions: map[string]*fakeSession{testName: {local: "/home/me/dev/api", beta: "prod-box:/home/u/dev/api", paused: true}},
			wantRan:  [][]string{{"mutagen", "sync", "resume", testName}, {"mutagen", "sync", "flush", testName}},
		},
		{
			name:     "running: used as it is",
			sessions: map[string]*fakeSession{testName: {local: "/home/me/dev/api", beta: "prod-box:/home/u/dev/api"}},
		},
		{
			name:     "another remote end: neither resumed nor used",
			sessions: map[string]*fakeSession{testName: {local: "/home/me/dev/api", beta: "prod-box:/opt/api", paused: true}},
			wantErr:  true,
			wantType: &EndpointMismatchError{},
		},
		{
			name:     "a remote end with no host: not used",
			sessions: map[string]*fakeSession{testName: {local: "/home/me/dev/api", beta: "/home/u/dev/api"}},
			wantErr:  true,
			wantType: &EndpointMismatchError{},
		},
		{
			name:    "a listing that fails is no proof of absence",
			failing: "mutagen sync list",
			wantErr: true,
		},
		{
			name:    "a create that fails",
			failing: "mutagen sync create",
			wantRan: [][]string{
				{"ssh", "prod-box", "mkdir -p -- /home/u/dev/api"},
				{"mkdir", "-p", "--", "/home/me/dev/api"},
				{"mutagen", "sync", "create", "--name", testName,
					"--label", "hermod-remote=prod-box", "--label", "hermod-workspace=" + testName,
					"--mode", "two-way-safe", "/home/me/dev/api", "prod-box:/home/u/dev/api"},
			},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newFakeMutagen(tc.sessions, tc.failing)
			locks := t.TempDir()
			l, err := NewFS(m.exec, m.exec, locks, testRoots, testName, nil, testLabels...).Connect(context.Background())
			if (err != nil) != tc.wantErr {
				t.Fatalf("Connect() error = %v, want an error: %v", err, tc.wantErr)
			}
			if tc.wantType != nil {
				target := reflect.New(reflect.TypeOf(tc.wantType)).Interface()
				if !errors.As(err, target) {
					t.Errorf("Connect() error = %v, want a %T", err, tc.wantType)
				}
			}
			if got := m.changes(); !reflect.DeepEqual(got, tc.wantRan) {
				t.Errorf("ran %q, want %q", got, tc.wantRan)
			}
			held, lerr := leased(locks, testName)
			if lerr != nil {
				t.Fatal(lerr)
			}
			if held != !tc.wantErr {
				t.Errorf("leased after Connect = %v, want %v", held, !tc.wantErr)
			}
			if l != nil {
				_ = l.Suspend(context.Background())
			}
		})
	}
}

// terminate removes the session name as a `mutagen sync terminate` run by
// another process would.
func (m *fakeMutagen) terminate(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, name)
	m.monitors.End(name)
}

// watches are the commands the relay ran that watch a session.
func (m *fakeMutagen) watches() [][]string {
	var out [][]string
	for _, args := range m.exec.commands() {
		if len(args) > 2 && args[0] == "mutagen" && args[2] == "monitor" {
			out = append(out, args)
		}
	}
	return out
}

func TestFSWatch(t *testing.T) {
	watch := [][]string{{"mutagen", "sync", "monitor", testName}}
	cases := []struct {
		name      string
		dryRun    bool   // the relay changes states on a dry-run executor and reads them on the fake
		failing   string // a command that fails
		elsewhere bool   // another process terminates the session
		giveUp    bool   // this process gives its lease up
		wantErr   error  // what Err is, through errors.Is; nil for an error wantMsg describes, or none
		wantMsg   string // what Err says; "" for no message to check
		wantWatch [][]string
	}{
		{name: "a session terminated elsewhere", elsewhere: true, wantMsg: "mutagen sync monitor " + testName + ": ", wantWatch: watch},
		{name: "a watch that fails, as with no daemon", failing: "mutagen sync monitor", wantMsg: "failed as asked", wantWatch: watch},
		{name: "a lease given up stops the watch", giveUp: true, wantWatch: watch},
		{name: "a dry run plans the watch of the session it did not make", dryRun: true, wantErr: ErrEnded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newFakeMutagen(map[string]*fakeSession{}, tc.failing)
			var exec shell.Executor = m.exec
			if tc.dryRun {
				exec = shell.NewDryRun(io.Discard)
			} else {
				m.sessions[testName] = &fakeSession{local: testRoots.Local, beta: testRoots.endpoint()}
			}
			l, err := NewFS(exec, m.exec, t.TempDir(), testRoots, testName, nil, testLabels...).Connect(context.Background())
			if err != nil {
				t.Fatalf("Connect: %v", err)
			}
			if tc.elsewhere {
				m.terminate(testName)
			}
			if tc.giveUp {
				if err := l.Suspend(context.Background()); err != nil {
					t.Fatalf("Suspend: %v", err)
				}
			}
			<-l.Done()
			err = l.Err()
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Errorf("Err() = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr == nil && ((err != nil) != (tc.wantMsg != "") || (err != nil && !strings.Contains(err.Error(), tc.wantMsg))) {
				t.Errorf("Err() = %v, want one saying %q", err, tc.wantMsg)
			}
			if got := m.watches(); !reflect.DeepEqual(got, tc.wantWatch) {
				t.Errorf("watched with %q, want %q", got, tc.wantWatch)
			}
			if !tc.giveUp {
				_ = l.Suspend(context.Background())
			}
			if n := m.exec.running(); n != 0 {
				t.Errorf("%d commands still running after the lease was given up", n)
			}
		})
	}
}

func TestFSConcurrentConnect(t *testing.T) {
	cases := []struct {
		name        string
		holders     int
		wantCreates int
	}{
		{"leases taken at once in one process create one session", 4, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newFakeMutagen(nil, "")
			relay := NewFS(m.exec, m.exec, t.TempDir(), testRoots, testName, nil, testLabels...)
			leases := make([]Lease, tc.holders)
			errs := make([]error, tc.holders)
			var wg sync.WaitGroup
			for i := range leases {
				wg.Go(func() { leases[i], errs[i] = relay.Connect(context.Background()) })
			}
			wg.Wait()
			for i, err := range errs {
				if err != nil {
					t.Fatalf("Connect() %d error: %v", i, err)
				}
			}
			creates := 0
			for _, args := range m.changes() {
				if len(args) > 2 && args[0] == "mutagen" && args[2] == "create" {
					creates++
				}
			}
			if creates != tc.wantCreates {
				t.Errorf("ran %d creates, want %d", creates, tc.wantCreates)
			}
			for _, l := range leases {
				_ = l.Release(context.Background())
			}
		})
	}
}

func TestFSFlush(t *testing.T) {
	cases := []struct {
		name    string
		failing string
		wantRan [][]string
		wantErr bool
	}{
		{"flushes by name", "", [][]string{{"mutagen", "sync", "flush", testName}}, false},
		{"a flush that fails", "mutagen sync flush", [][]string{{"mutagen", "sync", "flush", testName}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newFakeMutagen(nil, tc.failing)
			err := NewFS(m.exec, m.exec, t.TempDir(), testRoots, testName, nil).Flush(context.Background())
			if (err != nil) != tc.wantErr {
				t.Fatalf("Flush() error = %v, want an error: %v", err, tc.wantErr)
			}
			if got := m.changes(); !reflect.DeepEqual(got, tc.wantRan) {
				t.Errorf("ran %q, want %q", got, tc.wantRan)
			}
		})
	}
}

func TestList(t *testing.T) {
	other := "hermod-prod-box-home-u-dev-web-5e6f7a8b"
	sessions := func() map[string]*fakeSession {
		return map[string]*fakeSession{
			testName: {local: "/home/me/dev/api", beta: "prod-box:/home/u/dev/api", labels: testLabels},
			other: {local: "/home/me/dev/web", beta: "prod-box:/home/u/dev/web", paused: true,
				labels: []Label{{"hermod-remote", "prod-box"}, {"hermod-workspace", other}}},
			"by-hand": {local: "/home/me/dev/api", beta: "prod-box:dev/api"},
			"": {local: "/home/me/dev/cli", beta: "box-u:/home/u/dev/cli",
				labels: []Label{{"hermod-remote", "box-u"}}},
		}
	}
	cases := []struct {
		name    string
		label   Label
		leased  []string // the relays a lease is held on
		failing string
		want    []Listed
		wantErr bool
	}{
		{
			name:   "every relay of a remote, by state",
			label:  Label{"hermod-remote", "prod-box"},
			leased: []string{testName},
			want: []Listed{
				{Name: testName, Roots: testRoots, State: Connected,
					Labels: map[string]string{"hermod-remote": "prod-box", "hermod-workspace": testName}},
				{Name: other, Roots: Roots{Local: "/home/me/dev/web", Host: "prod-box", Remote: "/home/u/dev/web"}, State: Disconnected,
					Labels: map[string]string{"hermod-remote": "prod-box", "hermod-workspace": other}},
			},
		},
		{
			name:  "a running session with no lease is Disconnected",
			label: Label{"hermod-workspace", testName},
			want: []Listed{
				{Name: testName, Roots: testRoots, State: Disconnected,
					Labels: map[string]string{"hermod-remote": "prod-box", "hermod-workspace": testName}},
			},
		},
		{
			name:  "a label nothing carries",
			label: Label{"hermod-remote", "box-b"},
		},
		{
			name:  "a session with no name is no relay, whatever its labels",
			label: Label{"hermod-remote", "box-u"},
		},
		{
			name:    "a listing that fails",
			label:   Label{"hermod-remote", "prod-box"},
			failing: "mutagen sync list",
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newFakeMutagen(sessions(), tc.failing)
			locks := t.TempDir()
			for _, name := range tc.leased {
				lk, err := takeLock(context.Background(), locks, name, false)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = lk.close() })
			}
			got, err := List(context.Background(), m.exec, locks, tc.label)
			if (err != nil) != tc.wantErr {
				t.Fatalf("List() error = %v, want an error: %v", err, tc.wantErr)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("List() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestFSOutput(t *testing.T) {
	cases := []struct {
		name       string
		op         string // "connect" with no session, or "flush"
		noWriter   bool
		failing    bool // every Mutagen command that changes a state fails
		wantOutput string
		wantErrMsg string // a part of the error; "" for none
	}{
		{
			name:       "a create: only Mutagen's command reaches the writer",
			op:         "connect",
			wantOutput: "mutagen sync create: out\nmutagen sync create: err\n",
		},
		{
			name:       "a flush",
			op:         "flush",
			wantOutput: "mutagen sync flush: out\nmutagen sync flush: err\n",
		},
		{
			name:       "a failing flush: its message reaches the writer and the error",
			op:         "flush",
			failing:    true,
			wantOutput: "mutagen sync flush: out\nmutagen sync flush: err\n",
			wantErrMsg: "mutagen sync flush: err",
		},
		{
			name:       "no writer: the message still reaches the error",
			op:         "flush",
			noWriter:   true,
			failing:    true,
			wantErrMsg: "mutagen sync flush: err",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec := &fakeExec{answer: func(args []string) script {
				return func(_ context.Context, _ io.Reader, out, errOut io.Writer) error {
					if args[0] == "mutagen" && args[2] == "list" {
						relaytest.NotFound(errOut)
						return errFake
					}
					say := strings.Join(args[:min(3, len(args))], " ")
					_, _ = fmt.Fprintf(out, "%s: out\n", say)
					_, _ = fmt.Fprintf(errOut, "%s: err\n", say)
					if tc.failing && args[0] == "mutagen" {
						return errFake
					}
					return nil
				}
			}}
			var output strings.Builder
			var w io.Writer = &output
			if tc.noWriter {
				w = nil
			}
			fs := NewFS(exec, exec, t.TempDir(), testRoots, testName, w)
			var err error
			switch tc.op {
			case "connect":
				_, err = fs.Connect(context.Background())
			case "flush":
				err = fs.Flush(context.Background())
			}
			if got := output.String(); got != tc.wantOutput {
				t.Errorf("output %q, want %q", got, tc.wantOutput)
			}
			if (err != nil) != (tc.wantErrMsg != "") || err != nil && !strings.Contains(err.Error(), tc.wantErrMsg) {
				t.Errorf("error %v, want one holding %q", err, tc.wantErrMsg)
			}
		})
	}
}

func TestParseList(t *testing.T) {
	type read struct {
		Name   string
		Roots  Roots
		Paused bool
		Labels map[string]string
	}
	cases := []struct {
		name    string
		out     string
		want    []read
		wantErr bool
	}{
		{name: "no session", out: "[]\n"},
		{name: "no session, as null", out: "null\n"},
		{
			name: "several, a paused one and labels",
			out: `[{"identifier":"sync_a","name":"a","alpha":{"protocol":"local","path":"/home/me/dev/api","connected":true},` +
				`"beta":{"protocol":"ssh","host":"prod-box","path":"/home/u/dev/api","connected":true},` +
				`"labels":{"hermod-remote":"prod-box"},"paused":false,"status":"watching","successfulCycles":3},` +
				`{"identifier":"sync_b","name":"b","alpha":{"protocol":"local","path":"/home/me/dev/web"},` +
				`"beta":{"protocol":"ssh","host":"prod-box","path":"/home/u/dev/web"},"paused":true,"status":"disconnected"}]`,
			want: []read{
				{Name: "a", Roots: Roots{Local: "/home/me/dev/api", Host: "prod-box", Remote: "/home/u/dev/api"},
					Labels: map[string]string{"hermod-remote": "prod-box"}},
				{Name: "b", Roots: Roots{Local: "/home/me/dev/web", Host: "prod-box", Remote: "/home/u/dev/web"}, Paused: true},
			},
		},
		{
			name: "an ssh end with a user and a port",
			out: `[{"name":"a","alpha":{"protocol":"local","path":"/l"},` +
				`"beta":{"protocol":"ssh","user":"u","host":"box","port":2222,"path":"/r"}}]`,
			want: []read{{Name: "a", Roots: Roots{Local: "/l", Host: "u@box:2222", Remote: "/r"}}},
		},
		{
			name: "a local beta end has no host",
			out:  `[{"name":"a","alpha":{"protocol":"local","path":"/l"},"beta":{"protocol":"local","path":"/r"}}]`,
			want: []read{{Name: "a", Roots: Roots{Local: "/l", Remote: "/r"}}},
		},
		{
			name: "ends of other protocols keep their host",
			out: `[{"name":"a","alpha":{"protocol":"ssh","host":"box","path":"/l"},` +
				`"beta":{"protocol":"docker","host":"c1","path":"/r"}}]`,
			want: []read{{Name: "a", Roots: Roots{Local: "box:/l", Host: "docker://c1", Remote: "/r"}}},
		},
		{
			name: "a session with no name",
			out: `[{"identifier":"sync_x","alpha":{"protocol":"local","path":"/l"},` +
				`"beta":{"protocol":"ssh","host":"box","path":"/r"}}]`,
			want: []read{{Roots: Roots{Local: "/l", Host: "box", Remote: "/r"}}},
		},
		{name: "Mutagen's text listing", out: "--------\nName: a\n", wantErr: true},
		{name: "a session that is not an object", out: `["a"]`, wantErr: true},
		{name: "nothing", out: "", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sessions, err := parseList([]byte(tc.out))
			if (err != nil) != tc.wantErr {
				t.Fatalf("parseList() error = %v, want an error: %v", err, tc.wantErr)
			}
			var got []read
			for _, s := range sessions {
				got = append(got, read{Name: s.Name, Roots: s.roots(), Paused: s.Paused, Labels: s.Labels})
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseList() = %+v, want %+v", got, tc.want)
			}
		})
	}
}
