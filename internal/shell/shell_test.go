package shell

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"
)

// recShell records the last command it was asked to run and returns canned data.
type recShell struct {
	got Command
	ret Result
	err error
}

func (r *recShell) Run(_ context.Context, cmd Command) (Result, error) {
	r.got = cmd
	return r.ret, r.err
}

func TestSSHRewrite(t *testing.T) {
	cases := []struct {
		name string
		host string
		tty  bool
		in   Command
		want []string
	}{
		{"probe no tty", "prod", false, Command{Argv: []string{"tmux", "has-session", "-t", "api"}}, []string{"ssh", "prod", "tmux has-session -t api"}},
		{"interactive tty", "prod", true, Command{Argv: []string{"tmux", "new-session", "-A"}}, []string{"ssh", "-t", "prod", "tmux new-session -A"}},
		{"quotes sandbox arg with space", "box", false, Command{Argv: []string{"tmux", "new-session", "claude --model opus"}}, []string{"ssh", "box", "tmux new-session 'claude --model opus'"}},
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
		in   Command
		want []string
	}{
		{"default shell no command", "api", Command{}, []string{"tmux", "new-session", "-A", "-s", "api"}},
		{"with dir", "api", Command{Dir: "/home/u/api"}, []string{"tmux", "new-session", "-A", "-s", "api", "-c", "/home/u/api"}},
		{"with env", "api", Command{Env: []string{"GIT_AUTHOR_NAME=Jane Doe"}}, []string{"tmux", "new-session", "-A", "-s", "api", "-e", "GIT_AUTHOR_NAME=Jane Doe"}},
		{"passthrough joined", "api", Command{Dir: "/home/u/api", Argv: []string{"claude", "--model", "opus"}}, []string{"tmux", "new-session", "-A", "-s", "api", "-c", "/home/u/api", "claude --model opus"}},
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

func TestLoginShellRewrite(t *testing.T) {
	cases := []struct {
		name string
		in   Command
		want []string
	}{
		{"empty command untouched", Command{Dir: "/home/u/api"}, nil},
		{"single word wrapped", Command{Argv: []string{"claude"}}, []string{"sh", "-c", `exec "$SHELL" -lc "$1"`, "sh", "claude"}},
		{"flags preserved in one word", Command{Argv: []string{"claude", "--model", "opus"}}, []string{"sh", "-c", `exec "$SHELL" -lc "$1"`, "sh", "claude --model opus"}},
		{"inner spaces stay quoted", Command{Argv: []string{"claude", "-p", "hi there"}}, []string{"sh", "-c", `exec "$SHELL" -lc "$1"`, "sh", "claude -p 'hi there'"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inner := &recShell{}
			NewLoginShell(inner).Run(context.Background(), tc.in)
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
		in      Command
		want    []string
	}{
		{
			"default shell",
			"prod", "api",
			Command{Dir: "/home/u/api"},
			[]string{"ssh", "-t", "prod", "tmux new-session -A -s api -c /home/u/api"},
		},
		{
			"passthrough with identity",
			"prod", "api",
			Command{Dir: "/home/u/api", Env: []string{"GIT_AUTHOR_NAME=Jane Doe"}, Argv: []string{"claude", "--model", "opus"}},
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

func TestFormat(t *testing.T) {
	cases := []struct {
		name string
		cmd  Command
		want string
	}{
		{"plain argv", Command{Argv: []string{"mutagen", "sync", "list"}}, "mutagen sync list"},
		{"quotes arg with space", Command{Argv: []string{"echo", "hello world"}}, "echo 'hello world'"},
		{"env prefix quoted value", Command{Argv: []string{"git", "commit"}, Env: []string{"GIT_AUTHOR_NAME=Jane Doe"}}, "GIT_AUTHOR_NAME='Jane Doe' git commit"},
		{"safe path unquoted", Command{Argv: []string{"ls"}, Dir: "/home/u/api"}, "cd /home/u/api && ls"},
		{"dir env and argv", Command{Argv: []string{"git", "commit"}, Dir: "/home/u/api", Env: []string{"K=v"}}, "cd /home/u/api && K=v git commit"},
		{"single quote in value", Command{Argv: []string{"echo", "it's"}}, `echo 'it'\''s'`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := format(tc.cmd); got != tc.want {
				t.Errorf("format() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDryRunLeafPrintsAndDoesNotExecute(t *testing.T) {
	cases := []struct {
		name string
		cmd  Command
		want string
	}{
		{"side effect printed", Command{Argv: []string{"mutagen", "sync", "terminate", "api"}}, "mutagen sync terminate api\n"},
		{"attach printed not run", Command{Argv: []string{"ssh", "-t", "prod", "tmux", "attach"}}, "ssh -t prod tmux attach\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			res, err := local{run: printTo(&buf)}.Run(context.Background(), tc.cmd)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.Stdout != "" {
				t.Errorf("Stdout = %q, want empty (nothing executed)", res.Stdout)
			}
			if got := buf.String(); got != tc.want {
				t.Errorf("printed %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSubprocessCapture(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want string
	}{
		{"printf no newline", []string{"printf", "hello"}, "hello"},
		{"echo with newline", []string{"echo", "sync-ok"}, "sync-ok\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := runSubprocess(context.Background(), Command{Argv: tc.argv, Capture: true})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.Stdout != tc.want {
				t.Errorf("Stdout = %q, want %q", res.Stdout, tc.want)
			}
		})
	}
}

func TestSubprocessEnv(t *testing.T) {
	cases := []struct {
		name string
		env  []string
		want string
	}{
		{"exported var visible", []string{"HERMOD_TEST=carried"}, "carried\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := runSubprocess(context.Background(), Command{
				Argv:    []string{"sh", "-c", "echo $HERMOD_TEST"},
				Env:     tc.env,
				Capture: true,
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.Stdout != tc.want {
				t.Errorf("Stdout = %q, want %q", res.Stdout, tc.want)
			}
		})
	}
}

func TestSubprocessEmptyArgv(t *testing.T) {
	cases := []struct {
		name string
		argv []string
	}{
		{"nil argv", nil},
		{"empty argv", []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := runSubprocess(context.Background(), Command{Argv: tc.argv})
			if err == nil {
				t.Fatal("expected error for empty argv, got nil")
			}
			if !strings.Contains(err.Error(), "argv") {
				t.Errorf("error = %v, want it to mention argv", err)
			}
		})
	}
}
