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
)

// fakeSession is one Mutagen session as fakeMutagen keeps it.
type fakeSession struct {
	local, beta string
	paused      bool
	labels      []Label
}

// fakeMutagen plays the mutagen commands an FS relay runs against sessions it
// keeps, and records every command that changes a state; failing names a
// command whose every run fails.
type fakeMutagen struct {
	mu       sync.Mutex
	sessions map[string]*fakeSession
	failing  string
	exec     *fakeExec
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
// every command but a listing.
func (m *fakeMutagen) changes() [][]string {
	var out [][]string
	for _, args := range m.exec.commands() {
		if len(args) > 2 && args[0] == "mutagen" && args[2] == "list" {
			continue
		}
		out = append(out, args)
	}
	return out
}

func (m *fakeMutagen) answer(args []string) script {
	return func(_ context.Context, _ io.Reader, out, errOut io.Writer) error {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.failing != "" && strings.HasPrefix(strings.Join(args, " "), m.failing) {
			_, _ = io.WriteString(errOut, "failed as asked\n")
			return errFake
		}
		if args[0] != "mutagen" {
			return nil
		}
		verb, rest := args[2], args[3:]
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
		case "pause":
			m.sessions[rest[0]].paused = true
		case "resume":
			m.sessions[rest[0]].paused = false
		case "terminate":
			delete(m.sessions, rest[0])
		}
		return nil
	}
}

func (m *fakeMutagen) list(rest []string, out, errOut io.Writer) error {
	var names []string
	switch {
	case len(rest) == 0:
		for name := range m.sessions {
			names = append(names, name)
		}
	case len(rest) == 2 && rest[0] == "--label-selector":
		k, v, _ := strings.Cut(rest[1], "=")
		for name, s := range m.sessions {
			for _, l := range s.labels {
				if l.Key == k && l.Value == v {
					names = append(names, name)
				}
			}
		}
	case len(rest) == 1:
		if _, ok := m.sessions[rest[0]]; !ok {
			_, _ = fmt.Fprintf(errOut, "Error: %s\n", notFoundMarker)
			return errFake
		}
		names = []string{rest[0]}
	}
	for _, name := range names {
		s := m.sessions[name]
		_, _ = fmt.Fprintf(out, "--------\nName: %s\nIdentifier: sync_1\n", name)
		if len(s.labels) > 0 {
			_, _ = fmt.Fprintln(out, "Labels:")
			for _, l := range s.labels {
				_, _ = fmt.Fprintf(out, "\t%s: %s\n", l.Key, l.Value)
			}
		} else {
			_, _ = fmt.Fprintln(out, "Labels: None")
		}
		_, _ = fmt.Fprintf(out, "Alpha:\n\tURL: %s\n\tConnected: Yes\nBeta:\n\tURL: %s\n\tConnected: Yes\n", s.local, s.beta)
		status := "Watching for changes"
		if s.paused {
			status = "[Paused]"
		}
		_, _ = fmt.Fprintf(out, "Status: %s\n--------\n", status)
	}
	return nil
}

var (
	testEnds   = Ends{Local: "/home/me/dev/api", Host: "prod-box", Remote: "/home/u/dev/api"}
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
			l, err := NewFS(m.exec, m.exec, locks, testEnds, testName, testLabels...).Connect(context.Background())
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
			relay := NewFS(m.exec, m.exec, t.TempDir(), testEnds, testName, testLabels...)
			leases := make([]Lease, tc.holders)
			errs := make([]error, tc.holders)
			var wg sync.WaitGroup
			for i := range leases {
				wg.Add(1)
				go func() {
					defer wg.Done()
					leases[i], errs[i] = relay.Connect(context.Background())
				}()
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
			err := NewFS(m.exec, m.exec, t.TempDir(), testEnds, testName).Flush(context.Background())
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
				{Name: testName, Ends: testEnds, State: Connected,
					Labels: map[string]string{"hermod-remote": "prod-box", "hermod-workspace": testName}},
				{Name: other, Ends: Ends{Local: "/home/me/dev/web", Host: "prod-box", Remote: "/home/u/dev/web"}, State: Disconnected,
					Labels: map[string]string{"hermod-remote": "prod-box", "hermod-workspace": other}},
			},
		},
		{
			name:  "a running session with no lease is Disconnected",
			label: Label{"hermod-workspace", testName},
			want: []Listed{
				{Name: testName, Ends: testEnds, State: Disconnected,
					Labels: map[string]string{"hermod-remote": "prod-box", "hermod-workspace": testName}},
			},
		},
		{
			name:  "a label nothing carries",
			label: Label{"hermod-remote", "box-b"},
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

func TestUnlabeled(t *testing.T) {
	sessions := func() map[string]*fakeSession {
		return map[string]*fakeSession{
			testName:  {local: "/home/me/dev/api", beta: "prod-box:/home/u/dev/api", labels: testLabels},
			"dev-api": {local: "/home/me/dev/api", beta: "box-a:/home/u/dev/api", paused: true},
			"by-hand": {local: "/home/me/dev/api", beta: "prod-box:dev/api", labels: []Label{{"owner", "me"}}},
			"dev-web": {local: "/home/me/dev/web", beta: "box-a:/home/u/dev/web"},
		}
	}
	cases := []struct {
		name    string
		local   string
		failing string
		want    []string
		wantErr bool
	}{
		{
			name:  "sessions under earlier names are listed and left alone",
			local: "/home/me/dev/api",
			want:  []string{"by-hand", "dev-api"},
		},
		{
			name:  "a root no session syncs",
			local: "/home/me/dev/cli",
		},
		{
			name:    "a listing that fails",
			local:   "/home/me/dev/api",
			failing: "mutagen sync list",
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newFakeMutagen(sessions(), tc.failing)
			got, err := Unlabeled(context.Background(), m.exec, tc.local, "hermod-remote")
			if (err != nil) != tc.wantErr {
				t.Fatalf("Unlabeled() error = %v, want an error: %v", err, tc.wantErr)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Unlabeled() = %q, want %q", got, tc.want)
			}
			if ran := m.changes(); len(ran) != 0 {
				t.Errorf("ran %q, want no change to any session", ran)
			}
		})
	}
}
