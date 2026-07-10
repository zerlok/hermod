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
			[]string{"ssh", "-t", "prod", "tmux new-session -A -s api -c /home/u/api -e 'GIT_AUTHOR_NAME=Jane Doe' 'claude --model opus'"},
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
