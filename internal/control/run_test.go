package control

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/zerlok/hermod/internal/git"
	"github.com/zerlok/hermod/internal/mirror"
	"github.com/zerlok/hermod/internal/notify"
	"github.com/zerlok/hermod/internal/shell"
)

// cannedShell stands in for the real probe leaf: it returns fixed stdout (or an
// error) regardless of the argv it is handed.
type cannedShell struct {
	stdout string
	err    error
}

func (c cannedShell) Run(_ context.Context, _ shell.Command) (shell.Result, error) {
	return shell.Result{Stdout: c.stdout}, c.err
}

// recLeaf records the last command handed to it, standing in for the effective
// leaf that would provision the remote dir.
type recLeaf struct{ got shell.Command }

func (r *recLeaf) Run(_ context.Context, cmd shell.Command) (shell.Result, error) {
	r.got = cmd
	return shell.Result{}, nil
}

func discardLog() *log.Logger { return log.New(io.Discard, "", 0) }

// fakeMirror records the lifecycle methods invoked on it, in order. flushErr, if
// set, is returned from Flush to exercise the final-flush failure path.
type fakeMirror struct {
	calls    []string
	flushErr error
}

func (m *fakeMirror) Flush(context.Context) error {
	m.calls = append(m.calls, "flush")
	return m.flushErr
}
func (m *fakeMirror) Pause(context.Context) error { m.calls = append(m.calls, "pause"); return nil }
func (m *fakeMirror) Close(context.Context) error { m.calls = append(m.calls, "close"); return nil }
func (m *fakeMirror) Status(context.Context) (mirror.State, error) {
	return mirror.Running, nil
}

// fakeSandbox reports a canned liveness for the teardown decision.
type fakeSandbox struct {
	alive   bool
	liveErr error
}

func (s fakeSandbox) Attach(context.Context) error           { return nil }
func (s fakeSandbox) IsActive(context.Context) (bool, error) { return s.alive, s.liveErr }

// TestTeardownByLiveness asserts the one decision that gives hermod its reason
// to exist: pause when alive or when liveness is unknown, flush+close only when
// the session is confirmed gone.
func TestTeardownByLiveness(t *testing.T) {
	cases := []struct {
		name      string
		alive     bool
		liveErr   error
		flushErr  error
		wantCalls []string
	}{
		{"alive pauses", true, nil, nil, []string{"pause"}},
		{"gone flushes then closes", false, nil, nil, []string{"flush", "close"}},
		{"gone still closes when final flush fails", false, nil, errors.New("sync failed"), []string{"flush", "close"}},
		{"ambiguous liveness pauses", false, errors.New("unreachable"), nil, []string{"pause"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &fakeMirror{flushErr: tc.flushErr}
			err := teardown(context.Background(), m, fakeSandbox{alive: tc.alive, liveErr: tc.liveErr}, log.New(io.Discard, "", 0))
			if err != nil {
				t.Fatalf("teardown() error: %v", err)
			}
			if !reflect.DeepEqual(m.calls, tc.wantCalls) {
				t.Errorf("mirror calls = %v, want %v", m.calls, tc.wantCalls)
			}
		})
	}
}

// TestOpenNotify asserts the switch and the best-effort guarantee: on, it opens a
// channel, contributes the address to the session environment, and serves the
// local end; off, under dry-run, or on failure it yields the zero notifier and an
// error the run is expected only to log.
func TestOpenNotify(t *testing.T) {
	cases := []struct {
		name        string
		opts        Options
		probe       shell.Shell
		wantChannel bool
		wantErr     bool
		wantNote    bool
	}{
		{"notify off yields the zero notifier", Options{Sandbox: "prod"}, cannedShell{stdout: "/run/user/1000"}, false, false, false},
		{"notify on opens and serves", Options{Sandbox: "prod", Notify: true}, cannedShell{stdout: "/run/user/1000"}, true, false, false},
		{"dry-run opens nothing", Options{Sandbox: "prod", Notify: true, DryRun: true}, cannedShell{stdout: "/run/user/1000"}, false, false, true},
		{"probe failure reports and opens nothing", Options{Sandbox: "prod", Notify: true}, cannedShell{err: errors.New("unreachable")}, false, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A short runtime dir: a unix socket path must fit in ~108 bytes, and
			// t.TempDir() embeds the long subtest name, overflowing it.
			tmp, err := os.MkdirTemp("", "h")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(tmp) })
			t.Setenv("XDG_RUNTIME_DIR", tmp)
			var logbuf bytes.Buffer
			eff := &recLeaf{}
			n, err := openNotify(context.Background(), tc.probe, eff, tc.opts, log.New(&logbuf, "", 0))
			defer func() { _ = n.Stop() }()

			if (err != nil) != tc.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if gotNote := strings.Contains(logbuf.String(), "--dry-run"); gotNote != tc.wantNote {
				t.Errorf("dry-run note logged = %v, want %v (log=%q)", gotNote, tc.wantNote, logbuf.String())
			}
			if !tc.wantChannel && eff.got.Argv != nil {
				t.Errorf("nothing may be provisioned without a channel, got %q", eff.got.Argv)
			}
			if got := !n.Channel().IsZero(); got != tc.wantChannel {
				t.Errorf("channel opened = %v, want %v (%+v)", got, tc.wantChannel, n.Channel())
			}
			if want := tc.wantChannel; (n.Env() != nil) != want {
				t.Errorf("env contribution = %q, want any %v", n.Env(), want)
			}
			if tc.wantChannel {
				if want := notify.Env(n.Channel().Remote); !reflect.DeepEqual(n.Env(), want) {
					t.Errorf("env contribution = %q, want %q", n.Env(), want)
				}
				if _, err := os.Stat(n.Channel().Local); err != nil {
					t.Errorf("local end not served: %v", err)
				}
			}
		})
	}
}

// TestZeroNotifier pins what lets Run use the handle without ever branching on
// whether the channel came up.
func TestZeroNotifier(t *testing.T) {
	cases := []struct {
		name string
		n    notifier
	}{
		{"zero value", notifier{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !tc.n.Channel().IsZero() {
				t.Errorf("Channel() = %+v, want zero", tc.n.Channel())
			}
			if tc.n.Env() != nil {
				t.Errorf("Env() = %q, want none", tc.n.Env())
			}
			if err := tc.n.Stop(); err != nil {
				t.Errorf("Stop() error: %v", err)
			}
			if err := tc.n.Stop(); err != nil {
				t.Errorf("second Stop() error: %v", err)
			}
		})
	}
}

func TestIdentityEnv(t *testing.T) {
	cases := []struct {
		name string
		id   git.Identity
		want []string
	}{
		{"full author and committer", git.Identity{Name: "Jane Doe", Email: "jane@x.io"}, []string{"GIT_AUTHOR_NAME=Jane Doe", "GIT_COMMITTER_NAME=Jane Doe", "GIT_AUTHOR_EMAIL=jane@x.io", "GIT_COMMITTER_EMAIL=jane@x.io"}},
		{"name only", git.Identity{Name: "Jane Doe"}, []string{"GIT_AUTHOR_NAME=Jane Doe", "GIT_COMMITTER_NAME=Jane Doe"}},
		{"empty injects nothing", git.Identity{}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := identityEnv(tc.id); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("identityEnv() = %v, want %v", got, tc.want)
			}
		})
	}
}
