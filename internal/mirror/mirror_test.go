package mirror

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/zerlok/hermod/internal/execx"
	"github.com/zerlok/hermod/internal/shell"
)

// recShell records every argv it runs and returns a canned result/error.
type recShell struct {
	ret  execx.Result
	err  error
	seen [][]string
}

func (r *recShell) Run(_ context.Context, cmd execx.Command) (execx.Result, error) {
	r.seen = append(r.seen, cmd.Argv)
	return r.ret, r.err
}

// factoryOf serves fixed leaves so a test can inspect the mutation and probe
// commands separately.
type factoryOf struct{ real, eff shell.Shell }

func (f factoryOf) Real() shell.Shell      { return f.real }
func (f factoryOf) Effective() shell.Shell { return f.eff }

func testConfig() Config {
	return Config{Name: "api", Host: "prod-box", RemotePath: "dev/api", LocalPath: "/home/u/api"}
}

func TestNewMutagenSessionOpens(t *testing.T) {
	notFound := errors.New("unable to locate requested sessions")
	cases := []struct {
		name     string
		probeRet execx.Result
		probeErr error
		want     []string
	}{
		{"absent creates", execx.Result{}, notFound, []string{"mutagen", "sync", "create", "--name", "api", "/home/u/api", "prod-box:dev/api"}},
		{"paused resumes", execx.Result{Stdout: "Name: api\nStatus: Paused\n"}, nil, []string{"mutagen", "sync", "resume", "api"}},
		{"running resumes", execx.Result{Stdout: "Name: api\nStatus: Watching for changes\n"}, nil, []string{"mutagen", "sync", "resume", "api"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec := &recShell{}
			probe := &recShell{ret: tc.probeRet, err: tc.probeErr}
			if _, err := NewMutagenSession(context.Background(), factoryOf{real: probe, eff: exec}, testConfig()); err != nil {
				t.Fatalf("NewMutagenSession() error: %v", err)
			}
			if len(exec.seen) != 1 || !reflect.DeepEqual(exec.seen[0], tc.want) {
				t.Errorf("open side effect = %v, want single %v", exec.seen, tc.want)
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
			probe := &recShell{ret: execx.Result{Stdout: "Status: Watching for changes"}}
			s, err := NewMutagenSession(ctx, factoryOf{real: probe, eff: exec}, testConfig())
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
	notFound := errors.New("unable to locate requested sessions")
	cases := []struct {
		name     string
		probeRet execx.Result
		probeErr error
		want     State
	}{
		{"absent", execx.Result{}, notFound, Absent},
		{"paused", execx.Result{Stdout: "Status: Paused\n"}, nil, Paused},
		{"running", execx.Result{Stdout: "Status: Watching for changes\n"}, nil, Running},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec := &recShell{}
			probe := &recShell{ret: tc.probeRet, err: tc.probeErr}
			// Query Status directly on the concrete type to avoid the open side
			// effects a constructed Session would issue first.
			m := &mutagen{name: "api", probe: probe, exec: exec}
			got, err := m.Status(context.Background())
			if err != nil {
				t.Fatalf("Status() error: %v", err)
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
