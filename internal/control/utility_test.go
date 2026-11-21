package control

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/zerlok/hermod/internal/claude"
	"github.com/zerlok/hermod/internal/sandbox"
	"github.com/zerlok/hermod/internal/shell"
)

const sessionUUID = "0b7e5f3c-2a41-4c8e-9d6f-1e2a3b4c5d6e"

// recLeaf records every command that reaches the leaf and fails each with err.
type recLeaf struct {
	cmds []shell.Command
	err  error
}

func (r *recLeaf) Run(_ context.Context, cmd shell.Command) (shell.Result, error) {
	r.cmds = append(r.cmds, cmd)
	return shell.Result{}, r.err
}

// exitErr carries an exit code as *exec.ExitError does.
type exitErr struct{ code int }

func (e exitErr) Error() string { return "exit status" }
func (e exitErr) ExitCode() int { return e.code }

func TestRunUtility(t *testing.T) {
	cases := []struct {
		name       string
		dir        string // the project's sandbox directory; "" for none
		argv       []string
		leafErr    error
		wantArgv   []string
		wantStatus ExitStatus // 0 with wantErr unset: success
		wantErr    bool       // an error that is not an ExitStatus
	}{
		{
			"success",
			"", []string{"claude", "auth", "status"}, nil,
			[]string{"ssh", "prod", `sh -c 'exec "$SHELL" -lc "$1"' sh 'claude auth status'`},
			0, false,
		},
		{
			"runs in the project's directory",
			"dev/api", []string{"claude", "mcp", "list"}, nil,
			[]string{"ssh", "prod", `sh -c 'exec "$SHELL" -lc "$1"' sh 'sh -c '\''cd -- "$1" 2>/dev/null; shift; exec "$@"'\'' sh dev/api claude mcp list'`},
			0, false,
		},
		{
			"exit code passed back",
			"", []string{"claude", "auth", "status"}, exitErr{code: 1},
			[]string{"ssh", "prod", `sh -c 'exec "$SHELL" -lc "$1"' sh 'claude auth status'`},
			1, false,
		},
		{
			"ssh failure code passed back",
			"", []string{"claude", "--version"}, exitErr{code: 255},
			[]string{"ssh", "prod", `sh -c 'exec "$SHELL" -lc "$1"' sh 'claude --version'`},
			255, false,
		},
		{
			"killed by a signal has no code",
			"", []string{"claude", "--version"}, exitErr{code: -1},
			[]string{"ssh", "prod", `sh -c 'exec "$SHELL" -lc "$1"' sh 'claude --version'`},
			0, true,
		},
		{
			"never started",
			"", []string{"claude", "--version"}, errors.New("ssh: not found"),
			[]string{"ssh", "prod", `sh -c 'exec "$SHELL" -lc "$1"' sh 'claude --version'`},
			0, true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			leaf := &recLeaf{err: tc.leafErr}
			stdio := sandbox.Streams{In: strings.NewReader("{}\n"), Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
			err := runUtility(context.Background(), leaf, "prod", tc.dir, claude.Classify(tc.argv), stdio)

			var status ExitStatus
			isStatus := errors.As(err, &status)
			switch {
			case tc.wantErr && (err == nil || isStatus):
				t.Errorf("err = %v, want an error that is not an exit status", err)
			case !tc.wantErr && status != tc.wantStatus:
				t.Errorf("exit status = %d (err %v), want %d", status, err, tc.wantStatus)
			case !tc.wantErr && tc.wantStatus == 0 && err != nil:
				t.Errorf("err = %v, want nil", err)
			}
			// One command and only ssh: no Mutagen, no tmux, no proxy.
			if len(leaf.cmds) != 1 {
				t.Fatalf("commands = %d, want 1: %v", len(leaf.cmds), leaf.cmds)
			}
			got := leaf.cmds[0]
			if !reflect.DeepEqual(got.Argv, tc.wantArgv) {
				t.Errorf("argv = %q, want %q", got.Argv, tc.wantArgv)
			}
			if got.Stdin != stdio.In || got.Stdout != stdio.Out || got.Stderr != stdio.Err || got.Capture {
				t.Error("the call did not run with the caller's streams")
			}
		})
	}
}

// TestRunUtilityPlacesItsDirectory asserts a utility call runs where an agent
// session in the same directory would, and at the directory's own sandbox path
// when no listing places it, and that the read-only listing is the one command
// it issues besides the call.
func TestRunUtilityPlacesItsDirectory(t *testing.T) {
	const ownDir = "opt-work-api" // the path /opt/work/api is mirrored to on its own
	listing := []string{"mutagen", "sync", "list", "--label-selector", "hermod-sandbox=prod"}
	cases := []struct {
		name      string
		listing   reply
		wantArgv  []string
		wantLocal [][]string // the commands run on this machine
	}{
		{
			"inside a root mirrored at another path",
			reply{match: "--label-selector", ret: shell.Result{Stdout: listedRoot("hermod-prod-opt-work", "/opt/work", "opt-work")}},
			[]string{"ssh", "prod", `sh -c 'exec "$SHELL" -lc "$1"' sh 'sh -c '\''cd -- "$1" 2>/dev/null; shift; exec "$@"'\'' sh opt-work/api claude auth status'`},
			[][]string{listing},
		},
		{
			"nothing mirrored",
			reply{match: "--label-selector"},
			[]string{"ssh", "prod", `sh -c 'exec "$SHELL" -lc "$1"' sh 'sh -c '\''cd -- "$1" 2>/dev/null; shift; exec "$@"'\'' sh opt-work-api claude auth status'`},
			[][]string{listing},
		},
		{
			"a listing that cannot be read",
			reply{match: "--label-selector", err: errors.New("mutagen: not found")},
			[]string{"ssh", "prod", `sh -c 'exec "$SHELL" -lc "$1"' sh 'sh -c '\''cd -- "$1" 2>/dev/null; shift; exec "$@"'\'' sh opt-work-api claude auth status'`},
			[][]string{listing},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			leaf := &scripted{replies: []reply{tc.listing}}
			err := run(context.Background(), Options{
				Sandbox:   "prod",
				LocalDir:  "/opt/work/api",
				RemoteDir: ownDir,
				Command:   sandbox.Exec([]string{"claude", "auth", "status"}),
				Stdio:     sandbox.Streams{In: strings.NewReader(""), Out: io.Discard, Err: io.Discard},
			}, leaf)
			if err != nil {
				t.Fatalf("run() error: %v", err)
			}
			var calls, local [][]string
			for _, cmd := range leaf.cmds {
				if cmd.Argv[0] == "ssh" {
					calls = append(calls, cmd.Argv)
				} else {
					local = append(local, cmd.Argv)
				}
			}
			if want := [][]string{tc.wantArgv}; !reflect.DeepEqual(calls, want) {
				t.Errorf("sandbox calls =\n %q\nwant\n %q", calls, want)
			}
			if !reflect.DeepEqual(local, tc.wantLocal) {
				t.Errorf("local commands = %q, want %q", local, tc.wantLocal)
			}
		})
	}
}

// TestRunRoutesThePlan asserts where a dry run's plan goes: stdout normally,
// stderr when the caller owns stdout.
func TestRunRoutesThePlan(t *testing.T) {
	cases := []struct {
		name       string
		standIn    bool
		wantStdout string
		wantStderr string
	}{
		{"attach prints the plan on stdout", false, `ssh prod 'sh -c '\''exec "$SHELL" -lc "$1"'\'' sh '\''claude --version'\'''` + "\n", ""},
		{"stand-in prints the plan on stderr", true, "", `ssh prod 'sh -c '\''exec "$SHELL" -lc "$1"'\'' sh '\''claude --version'\'''` + "\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := run(context.Background(), Options{
				Sandbox: "prod",
				Command: sandbox.Exec([]string{"claude", "--version"}),
				DryRun:  true,
				StandIn: tc.standIn,
				Stdio:   sandbox.Streams{In: strings.NewReader(""), Out: &stdout, Err: &stderr},
			}, absentProbe{})
			if err != nil {
				t.Fatalf("run() error: %v", err)
			}
			if stdout.String() != tc.wantStdout || stderr.String() != tc.wantStderr {
				t.Errorf("stdout, stderr = %q, %q, want %q, %q", stdout.String(), stderr.String(), tc.wantStdout, tc.wantStderr)
			}
		})
	}
}

// absentProbe answers every probe as Mutagen does for a session it cannot
// locate, so the mirror is created; a git probe reads no identity from it.
type absentProbe struct{}

func (absentProbe) Run(context.Context, shell.Command) (shell.Result, error) {
	const notFound = "unable to locate requested sessions"
	return shell.Result{Stderr: notFound}, errors.New(notFound)
}

// TestAttachKeepsStdoutForTheCaller asserts where the output of Hermod's setup
// commands goes: the caller's stdout for a plain attach, stderr when the caller
// of a stand-in owns stdout. The interactive attach always keeps the terminal.
func TestAttachKeepsStdoutForTheCaller(t *testing.T) {
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	cases := []struct {
		name      string
		standIn   bool
		wantSetup io.Writer
	}{
		{"attach prints setup output on stdout", false, stdout},
		{"stand-in prints setup output on stderr", true, stderr},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			leaf := &recLeaf{}
			o := Options{
				Sandbox:   "prod",
				Session:   "dev-api",
				LocalDir:  "/home/me/dev/api",
				RemoteDir: "dev/api",
				Command:   sandbox.Exec([]string{"claude", "--model", "opus"}),
				Quiet:     true,
				StandIn:   tc.standIn,
				Stdio:     sandbox.Streams{In: strings.NewReader(""), Out: stdout, Err: stderr},
			}
			if err := attach(context.Background(), o, leaf, absentProbe{}); err != nil {
				t.Fatalf("attach() error: %v", err)
			}
			var setup, interactive int
			for _, cmd := range leaf.cmds {
				switch {
				case cmd.Capture:
				case len(cmd.Argv) > 1 && cmd.Argv[0] == "ssh" && cmd.Argv[1] == "-t":
					interactive++
					if cmd.Stdout != nil {
						t.Errorf("attach %q writes to %v, not the terminal", cmd.Argv, cmd.Stdout)
					}
				default:
					setup++
					if cmd.Stdout != tc.wantSetup {
						t.Errorf("setup %q writes to %v, want %v", cmd.Argv, cmd.Stdout, tc.wantSetup)
					}
				}
			}
			if setup == 0 || interactive != 1 {
				t.Errorf("setup commands = %d, attaches = %d, want some and one: %v", setup, interactive, leaf.cmds)
			}
		})
	}
}

// TestOpenSessionLog asserts the session log is private to the user, and that
// a log that cannot be made changes nothing in its place.
func TestOpenSessionLog(t *testing.T) {
	cases := []struct {
		name string
		// dir returns the log directory under base.
		dir      func(t *testing.T, base string) string
		wantErr  bool
		wantDir  os.FileMode
		wantFile os.FileMode
	}{
		{"created private", func(t *testing.T, base string) string { return filepath.Join(base, "air") }, false, 0o700, 0o600},
		{"log dir is a file", func(t *testing.T, base string) string {
			p := filepath.Join(base, "file")
			if err := os.WriteFile(p, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			return p
		}, true, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := tc.dir(t, t.TempDir())
			id, _ := claude.ParseSessionID(sessionUUID)
			f, err := openSessionLog(dir, id)
			if (err != nil) != tc.wantErr {
				t.Fatalf("openSessionLog() error = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				if info, statErr := os.Stat(dir); statErr != nil || !info.Mode().IsRegular() {
					t.Errorf("the file in the log dir's place was changed: %v, %v", info, statErr)
				}
				return
			}
			defer f.Close()
			if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != tc.wantDir {
				t.Errorf("log dir = %v, %v, want mode %o", info, err, tc.wantDir)
			}
			if info, err := f.Stat(); err != nil || info.Mode().Perm() != tc.wantFile || info.Name() != sessionUUID+".log" {
				t.Errorf("log = %v, %v, want %s.log with mode %o", info, err, sessionUUID, tc.wantFile)
			}
		})
	}
}

func TestResolveDefaultsToProcessStreams(t *testing.T) {
	cases := []struct {
		name     string
		opts     []Option
		wantPlan *os.File
		wantLog  string
	}{
		{"attach plans on stdout", nil, os.Stdout, ""},
		{"stand-in plans on stderr", []Option{WithStandIn("/c/hermod/air")}, os.Stderr, "/c/hermod/air"},
		{"a chat typed by hand plans on stderr", []Option{WithCommand(chatArgv("--session-id", sessionUUID))}, os.Stderr, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := resolve("prod", tc.opts...)
			if o.Stdio.In != os.Stdin || o.Stdio.Out != os.Stdout || o.Stdio.Err != os.Stderr {
				t.Error("streams are not the process's own")
			}
			if o.ownOut() != tc.wantPlan {
				t.Errorf("ownOut() = %v, want %v", o.ownOut(), tc.wantPlan.Name())
			}
			if o.SessionLogDir != tc.wantLog {
				t.Errorf("SessionLogDir = %q, want %q", o.SessionLogDir, tc.wantLog)
			}
		})
	}
}
