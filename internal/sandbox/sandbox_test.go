package sandbox

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/zerlok/hermod/internal/execx"
	"github.com/zerlok/hermod/internal/shell"
)

// leafFactory hands the same leaf shell back as Effective (what sandbox builds
// its transports on); Real is unused here.
type leafFactory struct{ leaf shell.Shell }

func (f leafFactory) Real() shell.Shell      { return f.leaf }
func (f leafFactory) Effective() shell.Shell { return f.leaf }

// recShell records the last command handed to the leaf of a shell stack.
type recShell struct{ got execx.Command }

func (r *recShell) Run(_ context.Context, cmd execx.Command) (execx.Result, error) {
	r.got = cmd
	return execx.Result{}, nil
}

// exitErr carries an exit code so execx.ExitCode can read it, matching how a
// real *exec.ExitError behaves.
type exitErr struct{ code int }

func (e exitErr) Error() string { return "exit status" }
func (e exitErr) ExitCode() int { return e.code }

// cannedShell returns a fixed error, standing in for the liveness probe leaf.
type cannedShell struct{ err error }

func (c cannedShell) Run(_ context.Context, _ execx.Command) (execx.Result, error) {
	return execx.Result{}, c.err
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
			s := NewSession(leafFactory{leaf: leaf}, Config{
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
			s := NewSession(leafFactory{leaf: cannedShell{err: tc.probeErr}}, Config{Host: "prod", Session: "api"})
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
