package sandbox

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
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
		command Command
		dir     string
		env     []string
		tags    Tags
		want    []string
	}{
		{
			"default shell",
			Command{}, "/home/u/api", nil,
			Tags{},
			[]string{"ssh", "-t", "prod", "tmux new-session -A -s api -c /home/u/api ';' set-option -t =api: -qo @hermod-mode attach"},
		},
		{
			"empty argv runs the default shell",
			Exec(nil), "/home/u/api", nil,
			Tags{},
			[]string{"ssh", "-t", "prod", "tmux new-session -A -s api -c /home/u/api ';' set-option -t =api: -qo @hermod-mode attach"},
		},
		{
			"empty line runs the default shell",
			ShellLine(""), "/home/u/api", nil,
			Tags{},
			[]string{"ssh", "-t", "prod", "tmux new-session -A -s api -c /home/u/api ';' set-option -t =api: -qo @hermod-mode attach"},
		},
		{
			"blank line runs the default shell",
			ShellLine(" \t "), "/home/u/api", nil,
			Tags{},
			[]string{"ssh", "-t", "prod", "tmux new-session -A -s api -c /home/u/api ';' set-option -t =api: -qo @hermod-mode attach"},
		},
		{
			"passthrough with identity",
			Exec([]string{"claude", "--model", "opus"}), "/home/u/api", []string{"GIT_AUTHOR_NAME=Jane Doe"},
			Tags{},
			[]string{"ssh", "-t", "prod", `tmux new-session -A -s api -c /home/u/api -e 'GIT_AUTHOR_NAME=Jane Doe' 'sh -c '\''exec "$SHELL" -lc "$1"'\'' sh '\''claude --model opus'\''' ';' set-option -t =api: -qo @hermod-mode attach`},
		},
		{
			"command line parsed by the login shell",
			ShellLine("htop -d 5 | tee log"), "/home/u/api", nil,
			Tags{},
			[]string{"ssh", "-t", "prod", `tmux new-session -A -s api -c /home/u/api 'sh -c '\''exec "$SHELL" -lc "$1"'\'' sh '\''eval '\''\'\'''\''htop -d 5 | tee log'\''\'\'''\'''\''' ';' set-option -t =api: -qo @hermod-mode attach`},
		},
		{
			"tagged with mode, origin and directory",
			Command{}, "dev/api", nil,
			Tags{Mode: ModeAttach, Origin: "laptop.local", Dir: "dev/api"},
			[]string{"ssh", "-t", "prod", "tmux new-session -A -s api -c dev/api ';' set-option -t =api: -qo @hermod-mode attach ';' set-option -t =api: -qo @hermod-origin laptop.local ';' set-option -t =api: -qo @hermod-dir dev/api"},
		},
		{
			"empty origin left out",
			Command{}, "dev/api", nil,
			Tags{Mode: ModeAirTerminal, Dir: "dev/api"},
			[]string{"ssh", "-t", "prod", "tmux new-session -A -s api -c dev/api ';' set-option -t =api: -qo @hermod-mode air-terminal ';' set-option -t =api: -qo @hermod-dir dev/api"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			leaf := &recShell{}
			s := NewSession(leaf, Config{
				Host: "prod", Session: "api", Dir: tc.dir, Command: tc.command, Env: tc.env, Tags: tc.tags,
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

func TestCommandArgv(t *testing.T) {
	cases := []struct {
		name    string
		command Command
		want    []string
	}{
		{"argv kept", Exec([]string{"claude", "--resume", "x"}), []string{"claude", "--resume", "x"}},
		{"command line has no argv", ShellLine("claude --resume x"), nil},
		{"default shell has no argv", Command{}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.command.Argv()
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Argv() = %q, want %q", got, tc.want)
			}
			if len(got) > 0 {
				got[0] = "edited"
				if again := tc.command.Argv(); !reflect.DeepEqual(again, tc.want) {
					t.Errorf("Argv() after editing a copy = %q, want %q", again, tc.want)
				}
			}
		})
	}
}

func TestRunComposesLoginShellOverSSH(t *testing.T) {
	cases := []struct {
		name string
		host string
		dir  string
		argv []string
		want []string
	}{
		{
			"utility call",
			"prod", "", []string{"claude", "auth", "status"},
			[]string{"ssh", "prod", `sh -c 'exec "$SHELL" -lc "$1"' sh 'claude auth status'`},
		},
		{
			"argument with spaces stays one word",
			"prod", "", []string{"claude", "-p", "hi there"},
			[]string{"ssh", "prod", `sh -c 'exec "$SHELL" -lc "$1"' sh 'claude -p '\''hi there'\'''`},
		},
		{
			"runs in the directory when it exists",
			"prod", "dev/my api", []string{"claude", "mcp", "list"},
			[]string{"ssh", "prod", `sh -c 'exec "$SHELL" -lc "$1"' sh 'sh -c '\''cd -- "$1" 2>/dev/null; shift; exec "$@"'\'' sh '\''dev/my api'\'' claude mcp list'`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			leaf := &recShell{}
			streams := Streams{In: strings.NewReader(""), Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
			if err := Run(context.Background(), leaf, tc.host, tc.dir, tc.argv, streams); err != nil {
				t.Fatalf("Run() error: %v", err)
			}
			if !reflect.DeepEqual(leaf.got.Argv, tc.want) {
				t.Errorf("argv = %q, want %q", leaf.got.Argv, tc.want)
			}
			if leaf.got.Stdin != streams.In || leaf.got.Stdout != streams.Out || leaf.got.Stderr != streams.Err {
				t.Error("the given streams did not reach the leaf")
			}
			if leaf.got.Capture {
				t.Error("Capture set: the command's output must go to the given streams")
			}
		})
	}
}

func TestRunReturnsTheExitCode(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantCode int
		wantOK   bool
	}{
		{"success", nil, 0, false},
		{"command failed", exitErr{code: 1}, 1, true},
		{"ssh failed", exitErr{code: 255}, 255, true},
		{"never started", errors.New("no ssh"), 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Run(context.Background(), cannedShell{err: tc.err}, "prod", "", []string{"claude"}, Streams{})
			code, ok := shell.ExitCode(err)
			if code != tc.wantCode || ok != tc.wantOK {
				t.Errorf("ExitCode() = %d, %v, want %d, %v", code, ok, tc.wantCode, tc.wantOK)
			}
			if (err == nil) != (tc.err == nil) {
				t.Errorf("err = %v, want %v", err, tc.err)
			}
		})
	}
}
