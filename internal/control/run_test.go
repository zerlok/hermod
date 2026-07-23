package control

import (
	"context"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
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

// TestOpenNotifyProvisions asserts openNotify probes the remote base (real leaf),
// creates the remote dir via the effective leaf, and derives matching socket
// addresses. The socket basename carries a random token, so the deterministic
// parts (the mkdir argv, the remote dir prefix, matching basenames) are asserted.
func TestOpenNotifyProvisions(t *testing.T) {
	cases := []struct {
		name          string
		remoteBase    string
		host          string
		wantMkdir     []string
		wantRemotePre string
	}{
		{
			"xdg runtime dir", "/run/user/1000\n", "prod",
			[]string{"ssh", "prod", "mkdir -p -m 700 /run/user/1000/hermod"},
			"/run/user/1000/hermod/hermod-notify-",
		},
		{
			"home fallback base", "/home/u/.hermod/run", "box",
			[]string{"ssh", "box", "mkdir -p -m 700 /home/u/.hermod/run/hermod"},
			"/home/u/.hermod/run/hermod/hermod-notify-",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eff := &recLeaf{}
			ch, err := openNotify(context.Background(), cannedShell{stdout: tc.remoteBase}, eff, tc.host, discardLog())
			if err != nil {
				t.Fatalf("openNotify() error: %v", err)
			}
			if !reflect.DeepEqual(eff.got.Argv, tc.wantMkdir) {
				t.Errorf("mkdir argv = %q, want %q", eff.got.Argv, tc.wantMkdir)
			}
			remote, local, found := strings.Cut(ch.ReverseSpec(), ":")
			if !found {
				t.Fatalf("ReverseSpec() = %q, want remote:local", ch.ReverseSpec())
			}
			if !strings.HasPrefix(remote, tc.wantRemotePre) || !strings.HasSuffix(remote, ".sock") {
				t.Errorf("remote sock = %q, want prefix %q and .sock suffix", remote, tc.wantRemotePre)
			}
			if filepath.Base(local) != filepath.Base(remote) {
				t.Errorf("local/remote basenames differ: %q vs %q", filepath.Base(local), filepath.Base(remote))
			}
			if want := []string{notify.EnvSock + "=" + remote}; !reflect.DeepEqual(ch.Env(), want) {
				t.Errorf("Env() = %q, want %q", ch.Env(), want)
			}
		})
	}
}

// TestSetupNotify asserts the best-effort wiring: off is a pure passthrough; on
// appends the address env and a spec and binds a listener; dry-run appends env and
// a spec but binds nothing; a probe failure degrades to a plain session.
func TestSetupNotify(t *testing.T) {
	base := []string{"GIT_AUTHOR_NAME=Jane Doe"}
	cases := []struct {
		name       string
		opts       Options
		probe      shell.Shell
		wantEnvKey bool
		wantSpec   bool
		wantBound  bool
	}{
		{"notify off is passthrough", Options{Sandbox: "prod"}, cannedShell{stdout: "/run/user/1000"}, false, false, false},
		{"notify on appends env, spec, binds", Options{Sandbox: "prod", Notify: true}, cannedShell{stdout: "/run/user/1000"}, true, true, true},
		{"dry-run appends env and spec, binds nothing", Options{Sandbox: "prod", Notify: true, DryRun: true}, cannedShell{stdout: "/run/user/1000"}, true, true, false},
		{"probe failure degrades to plain", Options{Sandbox: "prod", Notify: true}, cannedShell{err: errors.New("unreachable")}, false, false, false},
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
			env, spec, stop := setupNotify(context.Background(), tc.probe, &recLeaf{}, tc.opts, base, discardLog())
			defer func() { _ = stop() }()

			if len(env) == 0 || env[0] != base[0] {
				t.Errorf("base env not preserved: %q", env)
			}
			hasKey := false
			for _, e := range env {
				if strings.HasPrefix(e, notify.EnvSock+"=") {
					hasKey = true
				}
			}
			if hasKey != tc.wantEnvKey {
				t.Errorf("env carries %s = %v, want %v (env=%q)", notify.EnvSock, hasKey, tc.wantEnvKey, env)
			}
			if (spec != "") != tc.wantSpec {
				t.Errorf("spec = %q, want non-empty %v", spec, tc.wantSpec)
			}
			if tc.wantSpec {
				_, local, _ := strings.Cut(spec, ":")
				_, statErr := os.Stat(local)
				if bound := statErr == nil; bound != tc.wantBound {
					t.Errorf("socket bound = %v, want %v (local=%q)", bound, tc.wantBound, local)
				}
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
