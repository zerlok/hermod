package sandbox

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/zerlok/hermod/internal/shell"
)

// recShell records the last command handed to the leaf of a shell stack.
type recShell struct{ got shell.Command }

func (r *recShell) Run(_ context.Context, cmd shell.Command) (shell.Result, error) {
	r.got = cmd
	return shell.Result{}, nil
}

// exitErr carries an exit code so shell.ExitCode can read it, matching how a
// real *exec.ExitError behaves.
type exitErr struct{ code int }

func (e exitErr) Error() string { return "exit status" }
func (e exitErr) ExitCode() int { return e.code }

// cannedShell returns a fixed error, standing in for the liveness probe leaf.
type cannedShell struct{ err error }

func (c cannedShell) Run(_ context.Context, _ shell.Command) (shell.Result, error) {
	return shell.Result{}, c.err
}

func TestAttachComposesSshTmuxLine(t *testing.T) {
	cases := []struct {
		name    string
		command []string
		dir     string
		env     []string
		want    []string
	}{
		{
			"default shell",
			nil, "/home/u/api", nil,
			[]string{"ssh", "-t", "prod", "tmux new-session -A -s api -c /home/u/api"},
		},
		{
			"passthrough with identity",
			[]string{"claude", "--model", "opus"}, "/home/u/api", []string{"GIT_AUTHOR_NAME=Jane Doe"},
			[]string{"ssh", "-t", "prod", `tmux new-session -A -s api -c /home/u/api -e 'GIT_AUTHOR_NAME=Jane Doe' 'sh -c '\''exec "$SHELL" -lc "$1"'\'' sh '\''claude --model opus'\'''`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			leaf := &recShell{}
			s := NewSession(leaf, Config{
				Host: "prod", Session: "api", Dir: tc.dir, Command: tc.command, Env: tc.env,
			})
			if err := s.Attach(context.Background()); err != nil {
				t.Fatalf("Attach() error: %v", err)
			}
			if !reflect.DeepEqual(leaf.got.Argv, tc.want) {
				t.Errorf("argv = %q, want %q", leaf.got.Argv, tc.want)
			}
		})
	}
}

// TestChannelRidesInteractiveOnly asserts a carried channel becomes a reverse
// forward on the attach ssh and never on the liveness probe.
func TestChannelRidesInteractiveOnly(t *testing.T) {
	cases := []struct {
		name         string
		channel      Channel
		wantInAttach bool
		wantSpec     string
	}{
		{"no channel leaves attach unchanged", Channel{}, false, ""},
		{"half a channel is no channel", Channel{Remote: "/r/s.sock"}, false, ""},
		{"channel adds -R to attach", Channel{Local: "/l/s.sock", Remote: "/r/s.sock"}, true, "/r/s.sock:/l/s.sock"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			attach := &recShell{}
			NewSession(attach, Config{Host: "prod", Session: "api", Dir: "/home/u/api", Channel: tc.channel}).
				Attach(context.Background())
			hasR := containsArg(attach.got.Argv, "-R")
			if hasR != tc.wantInAttach {
				t.Errorf("attach argv -R present = %v, want %v (argv=%q)", hasR, tc.wantInAttach, attach.got.Argv)
			}
			if tc.wantSpec != "" && !containsArg(attach.got.Argv, tc.wantSpec) {
				t.Errorf("attach argv missing forward spec %q, got %q", tc.wantSpec, attach.got.Argv)
			}

			probe := &recShell{}
			NewSession(probe, Config{Host: "prod", Session: "api", Channel: tc.channel}).
				IsActive(context.Background())
			if containsArg(probe.got.Argv, "-R") {
				t.Errorf("probe argv must never carry -R, got %q", probe.got.Argv)
			}
		})
	}
}

func containsArg(argv []string, want string) bool {
	for _, a := range argv {
		if a == want {
			return true
		}
	}
	return false
}

func TestIsActive(t *testing.T) {
	cases := []struct {
		name      string
		probeErr  error
		wantAlive bool
		wantErr   bool
	}{
		{"session exists", nil, true, false},
		{"session gone (exit 1)", exitErr{code: 1}, false, false},
		{"connection error propagates", exitErr{code: 255}, false, true},
		{"non-exit error propagates", errors.New("boom"), false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewSession(cannedShell{err: tc.probeErr}, Config{Host: "prod", Session: "api"})
			alive, err := s.IsActive(context.Background())
			if alive != tc.wantAlive {
				t.Errorf("alive = %v, want %v", alive, tc.wantAlive)
			}
			if (err != nil) != tc.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}
