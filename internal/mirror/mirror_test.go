package mirror

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/zerlok/hermod/internal/shell"
)

// reply is a canned (result, error) a fake shell returns for a given argv.
type reply struct {
	ret shell.Result
	err error
}

// recShell records every argv it runs and answers from a per-argv script keyed
// by the joined argv, falling back to def when no script entry matches. The
// script lets a single probe vary its response between the focused
// `sync list <name>` query and the list-all `sync list` disambiguation.
type recShell struct {
	def    reply
	script map[string]reply
	seen   [][]string
}

func (r *recShell) Run(_ context.Context, cmd shell.Command) (shell.Result, error) {
	r.seen = append(r.seen, cmd.Argv)
	if rp, ok := r.script[strings.Join(cmd.Argv, " ")]; ok {
		return rp.ret, rp.err
	}
	return r.def.ret, r.def.err
}

func testConfig() Config {
	return Config{Name: "api", Host: "prod-box", RemotePath: "dev/api", LocalPath: "/home/u/api"}
}

// probeScript wires a probe fake whose focused query keys on the config name and
// whose list-all query keys on the bare `mutagen sync list`.
func probeScript(focused, listAll reply) *recShell {
	return &recShell{script: map[string]reply{
		"mutagen sync list api": focused,
		"mutagen sync list":     listAll,
	}}
}

func TestNewMutagenSessionOpens(t *testing.T) {
	probeErr := errors.New("mutagen daemon unavailable")
	cases := []struct {
		name     string
		focused  reply
		listAll  reply
		wantErr  bool
		wantExec [][]string
	}{
		{
			name:    "absent provisions root then creates",
			focused: reply{err: probeErr},
			listAll: reply{},
			wantExec: [][]string{
				{"ssh", "prod-box", "mkdir", "-p", "dev/api"},
				{"mutagen", "sync", "create", "--name", "api", "/home/u/api", "prod-box:dev/api"},
			},
		},
		{
			name:     "paused resumes without provisioning",
			focused:  reply{ret: shell.Result{Stdout: "Name: api\nStatus: Paused\n"}},
			wantExec: [][]string{{"mutagen", "sync", "resume", "api"}},
		},
		{
			name:     "running resumes without provisioning",
			focused:  reply{ret: shell.Result{Stdout: "Name: api\nStatus: Watching for changes\n"}},
			wantExec: [][]string{{"mutagen", "sync", "resume", "api"}},
		},
		{
			name:     "ambiguous probe aborts open without create",
			focused:  reply{err: probeErr},
			listAll:  reply{err: probeErr},
			wantErr:  true,
			wantExec: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec := &recShell{}
			probe := probeScript(tc.focused, tc.listAll)
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
			probe := probeScript(reply{ret: shell.Result{Stdout: "Status: Watching for changes"}}, reply{})
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
	probeErr := errors.New("mutagen daemon unavailable")
	cases := []struct {
		name    string
		focused reply
		listAll reply
		want    State
		wantErr bool
	}{
		{"focused success paused", reply{ret: shell.Result{Stdout: "Status: Paused\n"}}, reply{}, Paused, false},
		{"focused success running", reply{ret: shell.Result{Stdout: "Status: Watching for changes\n"}}, reply{}, Running, false},
		{"focused error, list-all confirms absent", reply{err: probeErr}, reply{}, Absent, false},
		{"focused error, list-all error surfaced", reply{err: probeErr}, reply{err: probeErr}, Absent, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec := &recShell{}
			probe := probeScript(tc.focused, tc.listAll)
			// Query Status directly on the concrete type to avoid the open side
			// effects a constructed Session would issue first.
			m := &mutagen{name: "api", probe: probe, exec: exec}
			got, err := m.Status(context.Background())
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Status() error = nil, want non-nil (ambiguous probe must surface)")
				}
			} else {
				if err != nil {
					t.Fatalf("Status() error: %v", err)
				}
				if got != tc.want {
					t.Errorf("State = %v, want %v", got, tc.want)
				}
			}
			if len(exec.seen) != 0 {
				t.Errorf("status mutated state via %v", exec.seen)
			}
		})
	}
}
