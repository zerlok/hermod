package shell

import (
	"context"
	"reflect"
	"testing"

	"github.com/zerlok/hermod/internal/execx"
)

// recShell records the last command it was asked to run and returns canned data.
type recShell struct {
	got execx.Command
	ret execx.Result
	err error
}

func (r *recShell) Run(_ context.Context, cmd execx.Command) (execx.Result, error) {
	r.got = cmd
	return r.ret, r.err
}

// recExec records the last command handed to the leaf executor.
type recExec struct{ got execx.Command }

func (r *recExec) Execute(_ context.Context, cmd execx.Command) (execx.Result, error) {
	r.got = cmd
	return execx.Result{}, nil
}

func TestLocalPassesCommandThrough(t *testing.T) {
	cases := []struct {
		name string
		cmd  execx.Command
	}{
		{"probe with dir", execx.Command{Argv: []string{"git", "config", "user.name"}, Dir: "/w", Capture: true}},
		{"plain", execx.Command{Argv: []string{"mutagen", "sync", "list"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			leaf := &recExec{}
			NewLocal(leaf).Run(context.Background(), tc.cmd)
			if !reflect.DeepEqual(leaf.got, tc.cmd) {
				t.Errorf("leaf got %+v, want %+v", leaf.got, tc.cmd)
			}
		})
	}
}

func TestSSHRewrite(t *testing.T) {
	cases := []struct {
		name string
		host string
		tty  bool
		in   execx.Command
		want []string
	}{
		{"probe no tty", "prod", false, execx.Command{Argv: []string{"tmux", "has-session", "-t", "api"}}, []string{"ssh", "prod", "tmux has-session -t api"}},
		{"interactive tty", "prod", true, execx.Command{Argv: []string{"tmux", "new-session", "-A"}}, []string{"ssh", "-t", "prod", "tmux new-session -A"}},
		{"quotes sandbox arg with space", "box", false, execx.Command{Argv: []string{"tmux", "new-session", "claude --model opus"}}, []string{"ssh", "box", "tmux new-session 'claude --model opus'"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inner := &recShell{}
			NewSSH(inner, tc.host, tc.tty).Run(context.Background(), tc.in)
			if !reflect.DeepEqual(inner.got.Argv, tc.want) {
				t.Errorf("argv = %q, want %q", inner.got.Argv, tc.want)
			}
			if inner.got.Dir != "" || inner.got.Env != nil {
				t.Errorf("sandbox intent leaked: dir=%q env=%v", inner.got.Dir, inner.got.Env)
			}
		})
	}
}

func TestTmuxRewrite(t *testing.T) {
	cases := []struct {
		name string
		sess string
		in   execx.Command
		want []string
	}{
		{"default shell no command", "api", execx.Command{}, []string{"tmux", "new-session", "-A", "-s", "api"}},
		{"with dir", "api", execx.Command{Dir: "/home/u/api"}, []string{"tmux", "new-session", "-A", "-s", "api", "-c", "/home/u/api"}},
		{"with env", "api", execx.Command{Env: []string{"GIT_AUTHOR_NAME=Jane Doe"}}, []string{"tmux", "new-session", "-A", "-s", "api", "-e", "GIT_AUTHOR_NAME=Jane Doe"}},
		{"passthrough joined", "api", execx.Command{Dir: "/home/u/api", Argv: []string{"claude", "--model", "opus"}}, []string{"tmux", "new-session", "-A", "-s", "api", "-c", "/home/u/api", "claude --model opus"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inner := &recShell{}
			NewTmux(inner, tc.sess).Run(context.Background(), tc.in)
			if !reflect.DeepEqual(inner.got.Argv, tc.want) {
				t.Errorf("argv = %q, want %q", inner.got.Argv, tc.want)
			}
		})
	}
}

// TestAttachStack asserts the full interactive stack (Tmux → SSH → leaf) yields
// one copy-pasteable ssh command line.
func TestAttachStack(t *testing.T) {
	cases := []struct {
		name    string
		host    string
		session string
		in      execx.Command
		want    []string
	}{
		{
			"default shell",
			"prod", "api",
			execx.Command{Dir: "/home/u/api"},
			[]string{"ssh", "-t", "prod", "tmux new-session -A -s api -c /home/u/api"},
		},
		{
			"passthrough with identity",
			"prod", "api",
			execx.Command{Dir: "/home/u/api", Env: []string{"GIT_AUTHOR_NAME=Jane Doe"}, Argv: []string{"claude", "--model", "opus"}},
			[]string{"ssh", "-t", "prod", "tmux new-session -A -s api -c /home/u/api -e 'GIT_AUTHOR_NAME=Jane Doe' 'claude --model opus'"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inner := &recShell{}
			stack := NewTmux(NewSSH(inner, tc.host, true), tc.session)
			stack.Run(context.Background(), tc.in)
			if !reflect.DeepEqual(inner.got.Argv, tc.want) {
				t.Errorf("argv = %q, want %q", inner.got.Argv, tc.want)
			}
		})
	}
}
