package sandbox

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/zerlok/hermod/internal/shell"
)

// probeShell stands in for the real probe leaf: it answers every command with a
// fixed stdout (or an error), regardless of the argv it is handed.
type probeShell struct {
	stdout string
	err    error
}

func (p probeShell) Run(_ context.Context, _ shell.Command) (shell.Result, error) {
	return shell.Result{Stdout: p.stdout}, p.err
}

// TestOpenChannelProvisions asserts the sandbox end is derived from the probed
// runtime dir, created with one mkdir through the effective leaf, and that both
// ends agree on the channel's name.
func TestOpenChannelProvisions(t *testing.T) {
	cases := []struct {
		name       string
		probed     string
		host       string
		channel    string
		wantMkdir  []string
		wantRemote string
		wantLocal  string
	}{
		{
			"xdg runtime dir", "/run/user/1000\n", "prod", "notify",
			[]string{"ssh", "prod", "mkdir -p -m 700 /run/user/1000/hermod"},
			"/run/user/1000/hermod/notify.sock",
			"/local/run/hermod/prod/notify.sock",
		},
		{
			"home fallback base", "/home/u/.hermod/run", "box", "notify",
			[]string{"ssh", "box", "mkdir -p -m 700 /home/u/.hermod/run/hermod"},
			"/home/u/.hermod/run/hermod/notify.sock",
			"/local/run/hermod/box/notify.sock",
		},
		{
			"host alias with a separator is flattened", "/run/user/1000", "u@a/b", "notify",
			[]string{"ssh", "u@a/b", "mkdir -p -m 700 /run/user/1000/hermod"},
			"/run/user/1000/hermod/notify.sock",
			"/local/run/hermod/u@a-b/notify.sock",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_RUNTIME_DIR", "/local/run")
			eff := &recShell{}
			got, err := OpenChannel(context.Background(), probeShell{stdout: tc.probed}, eff, tc.host, tc.channel)
			if err != nil {
				t.Fatalf("OpenChannel() error: %v", err)
			}
			if !reflect.DeepEqual(eff.got.Argv, tc.wantMkdir) {
				t.Errorf("mkdir argv = %q, want %q", eff.got.Argv, tc.wantMkdir)
			}
			want := Channel{Local: tc.wantLocal, Remote: tc.wantRemote}
			if got != want {
				t.Errorf("channel = %+v, want %+v", got, want)
			}
			if got.IsZero() {
				t.Error("an opened channel must not read as zero")
			}
		})
	}
}

// TestOpenChannelIsPerSandboxUser asserts the sandbox end is stable: the same host
// always yields the same address (one socket per sandbox user, not per project),
// while different hosts stay apart on the local end.
func TestOpenChannelIsPerSandboxUser(t *testing.T) {
	cases := []struct {
		name           string
		hostA, hostB   string
		wantSameRemote bool
		wantSameLocal  bool
	}{
		{"same host twice is one endpoint", "prod", "prod", true, true},
		{"different hosts share no local endpoint", "prod", "staging", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_RUNTIME_DIR", "/local/run")
			probe := probeShell{stdout: "/run/user/1000"}
			a, err := OpenChannel(context.Background(), probe, &recShell{}, tc.hostA, "notify")
			if err != nil {
				t.Fatalf("OpenChannel() a: %v", err)
			}
			b, err := OpenChannel(context.Background(), probe, &recShell{}, tc.hostB, "notify")
			if err != nil {
				t.Fatalf("OpenChannel() b: %v", err)
			}
			if same := a.Remote == b.Remote; same != tc.wantSameRemote {
				t.Errorf("same sandbox address = %v, want %v (%q vs %q)", same, tc.wantSameRemote, a.Remote, b.Remote)
			}
			if same := a.Local == b.Local; same != tc.wantSameLocal {
				t.Errorf("same local address = %v, want %v (%q vs %q)", same, tc.wantSameLocal, a.Local, b.Local)
			}
		})
	}
}

// TestOpenChannelRefusesUnusableRuntimeDir asserts a runtime dir that would change
// the meaning of the ssh forward spec (which splits on ':') — or that could not be
// read at all — refuses the channel rather than opening a surprising one.
func TestOpenChannelRefusesUnusableRuntimeDir(t *testing.T) {
	cases := []struct {
		name   string
		probed string
		err    error
	}{
		{"probe failure", "/run/user/1000", errors.New("unreachable")},
		{"empty", "", nil},
		{"relative path", "run/user/1000", nil},
		{"embedded colon", "/run/user:1000", nil},
		{"embedded space", "/run/user 1000", nil},
		{"banner line after the path", "/run/user/1000\nwelcome to prod", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_RUNTIME_DIR", "/local/run")
			eff := &recShell{}
			got, err := OpenChannel(context.Background(), probeShell{stdout: tc.probed, err: tc.err}, eff, "prod", "notify")
			if err == nil {
				t.Fatalf("expected an error, got channel %+v", got)
			}
			if !got.IsZero() {
				t.Errorf("a refused channel must be zero, got %+v", got)
			}
			if eff.got.Argv != nil {
				t.Errorf("nothing may be provisioned after a refused probe, got %q", eff.got.Argv)
			}
		})
	}
}

// TestZeroChannel pins the "no channel" reading, which is what keeps a session
// without one identical to a session that never had the concept.
func TestZeroChannel(t *testing.T) {
	cases := []struct {
		name    string
		channel Channel
		want    bool
	}{
		{"zero value", Channel{}, true},
		{"local only", Channel{Local: "/l/s.sock"}, true},
		{"remote only", Channel{Remote: "/r/s.sock"}, true},
		{"both ends", Channel{Local: "/l/s.sock", Remote: "/r/s.sock"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.channel.IsZero(); got != tc.want {
				t.Errorf("IsZero() = %v, want %v", got, tc.want)
			}
		})
	}
}
