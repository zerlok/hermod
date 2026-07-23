package mirror

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/zerlok/hermod/internal/shell"
)

// reply is a canned (result, error) a fake shell returns.
type reply struct {
	ret shell.Result
	err error
}

// recShell records every argv it runs and returns a canned result/error.
type recShell struct {
	ret  shell.Result
	err  error
	seen [][]string
}

func (r *recShell) Run(_ context.Context, cmd shell.Command) (shell.Result, error) {
	r.seen = append(r.seen, cmd.Argv)
	return r.ret, r.err
}

func testConfig() Config {
	return Config{Name: "api", Host: "prod-box", RemotePath: "dev/api", LocalPath: "/home/u/api"}
}

// notFound is the probe reply for a session Mutagen cannot locate: it carries
// the not-found marker on stderr and the non-zero exit that accompanies it.
var notFound = reply{ret: shell.Result{Stderr: notFoundMarker}, err: errors.New(notFoundMarker)}

func TestNewMutagenSessionOpens(t *testing.T) {
	ambiguous := reply{err: errors.New("mutagen daemon unavailable")}
	cases := []struct {
		name     string
		probe    reply
		wantErr  bool
		wantExec [][]string
	}{
		{
			name:  "absent provisions root then creates",
			probe: notFound,
			wantExec: [][]string{
				{"ssh", "prod-box", "mkdir", "-p", "dev/api"},
				{"mutagen", "sync", "create", "--name", "api", "/home/u/api", "prod-box:dev/api"},
			},
		},
		{
			name:     "paused resumes without provisioning",
			probe:    reply{ret: shell.Result{Stdout: "Name: api\nStatus: Paused\n"}},
			wantExec: [][]string{{"mutagen", "sync", "resume", "api"}},
		},
		{
			name:     "running resumes without provisioning",
			probe:    reply{ret: shell.Result{Stdout: "Name: api\nStatus: Watching for changes\n"}},
			wantExec: [][]string{{"mutagen", "sync", "resume", "api"}},
		},
		{
			name:     "ambiguous probe aborts open without create",
			probe:    ambiguous,
			wantErr:  true,
			wantExec: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec := &recShell{}
			probe := &recShell{ret: tc.probe.ret, err: tc.probe.err}
			_, err := NewMutagenSession(context.Background(), exec, probe, testConfig())
			if tc.wantErr {
				if err == nil {
					t.Fatalf("NewMutagenSession() error = nil, want non-nil")
				}
			} else if err != nil {
				t.Fatalf("NewMutagenSession() error: %v", err)
			}
			if !reflect.DeepEqual(exec.seen, tc.wantExec) {
				t.Errorf("open side effects = %v, want %v", exec.seen, tc.wantExec)
			}
		})
	}
}

func TestLifecycleArgv(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		call func(Session) error
		want []string
	}{
		{"flush", func(s Session) error { return s.Flush(ctx) }, []string{"mutagen", "sync", "flush", "api"}},
		{"pause", func(s Session) error { return s.Pause(ctx) }, []string{"mutagen", "sync", "pause", "api"}},
		{"close terminates", func(s Session) error { return s.Close(ctx) }, []string{"mutagen", "sync", "terminate", "api"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec := &recShell{}
			// A running session resumes on open, leaving one prior side effect.
			probe := &recShell{ret: shell.Result{Stdout: "Status: Watching for changes"}}
			s, err := NewMutagenSession(ctx, exec, probe, testConfig())
			if err != nil {
				t.Fatalf("NewMutagenSession() error: %v", err)
			}
			opened := len(exec.seen)
			if err := tc.call(s); err != nil {
				t.Fatalf("call error: %v", err)
			}
			if len(exec.seen) != opened+1 || !reflect.DeepEqual(exec.seen[opened], tc.want) {
				t.Errorf("argv = %v, want a single %v after open", exec.seen[opened:], tc.want)
			}
		})
	}
}

func TestStatusIsReadOnly(t *testing.T) {
	cases := []struct {
		name    string
		probe   reply
		want    State
		wantErr bool
	}{
		{"not-found marker reads absent", notFound, Absent, false},
		{"paused", reply{ret: shell.Result{Stdout: "Status: Paused\n"}}, Paused, false},
		{"running", reply{ret: shell.Result{Stdout: "Status: Watching for changes\n"}}, Running, false},
		// An error that is not the not-found marker is Unknown, never Absent, so it
		// can't be mistaken for "no session" and trigger a create over an existing one.
		{"ambiguous error is unknown, not absent", reply{err: errors.New("mutagen daemon unavailable")}, Unknown, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec := &recShell{}
			probe := &recShell{ret: tc.probe.ret, err: tc.probe.err}
			// Query Status directly on the concrete type to avoid the open side
			// effects a constructed Session would issue first.
			m := &mutagen{name: "api", probe: probe, exec: exec}
			got, err := m.Status(context.Background())
			if (err != nil) != tc.wantErr {
				t.Fatalf("Status() error = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("State = %v, want %v", got, tc.want)
			}
			if len(exec.seen) != 0 {
				t.Errorf("status mutated state via %v", exec.seen)
			}
		})
	}
}
