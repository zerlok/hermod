package execx

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

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

func TestDryRunPrintsAndDoesNotExecute(t *testing.T) {
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
			res, err := New(WithDryRunOutput(&buf)).Execute(context.Background(), tc.cmd)
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
			res, err := New().Execute(context.Background(), Command{Argv: tc.argv, Capture: true})
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
			res, err := New().Execute(context.Background(), Command{
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
			_, err := New().Execute(context.Background(), Command{Argv: tc.argv})
			if err == nil {
				t.Fatal("expected error for empty argv, got nil")
			}
			if !strings.Contains(err.Error(), "argv") {
				t.Errorf("error = %v, want it to mention argv", err)
			}
		})
	}
}
