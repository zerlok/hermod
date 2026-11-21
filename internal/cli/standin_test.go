package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/zerlok/hermod/internal/control"
	"github.com/zerlok/hermod/internal/sandbox"
)

const standInUUID = "0b7e5f3c-2a41-4c8e-9d6f-1e2a3b4c5d6e"

// TestExecuteDispatchesOnProgramName asserts the binary started as claude runs
// what `hermod attach -- claude ARGS…` runs, and under any other name runs the
// command line.
func TestExecuteDispatchesOnProgramName(t *testing.T) {
	cases := []struct {
		name        string
		argv        string // split on spaces; the first word is the program name
		env         env
		home        string
		wantSandbox string
		wantCommand sandbox.Command
		wantDryRun  bool
		wantStandIn bool
		wantLogDir  string
	}{
		{"started as claude", "claude --session-id " + standInUUID + " --settings /tmp/s.json", env{envSandbox: "prod-box"}, "/home/u",
			"prod-box", sandbox.Exec([]string{"claude", "--session-id", standInUUID, "--settings", "/tmp/s.json"}), false, true, "/home/u/.cache/hermod/air"},
		{"started by the link's path", "/home/u/.local/share/hermod/bin/claude auth status", env{envSandbox: "prod-box"}, "/home/u",
			"prod-box", sandbox.Exec([]string{"claude", "auth", "status"}), false, true, "/home/u/.cache/hermod/air"},
		{"bare claude", "claude", env{envSandbox: "prod-box"}, "/home/u",
			"prod-box", sandbox.Exec([]string{"claude"}), false, true, "/home/u/.cache/hermod/air"},
		{"Hermod-looking arguments stay Claude's", "claude -n", env{envSandbox: "prod-box"}, "/home/u",
			"prod-box", sandbox.Exec([]string{"claude", "-n"}), false, true, "/home/u/.cache/hermod/air"},
		{"attach-looking arguments stay Claude's", "claude attach other-box", env{envSandbox: "prod-box"}, "/home/u",
			"prod-box", sandbox.Exec([]string{"claude", "attach", "other-box"}), false, true, "/home/u/.cache/hermod/air"},
		{"sandbox command ignored", "claude --model opus", env{envSandbox: "prod-box", envSandboxCmd: "htop"}, "/home/u",
			"prod-box", sandbox.Exec([]string{"claude", "--model", "opus"}), false, true, "/home/u/.cache/hermod/air"},
		{"XDG cache home", "claude", env{envSandbox: "prod-box", envCacheHome: "/xdg/cache"}, "/home/u",
			"prod-box", sandbox.Exec([]string{"claude"}), false, true, "/xdg/cache/hermod/air"},
		{"relative XDG cache home ignored", "claude", env{envSandbox: "prod-box", envCacheHome: "cache"}, "/home/u",
			"prod-box", sandbox.Exec([]string{"claude"}), false, true, "/home/u/.cache/hermod/air"},
		{"no home keeps no session log", "claude", env{envSandbox: "prod-box"}, "",
			"prod-box", sandbox.Exec([]string{"claude"}), false, true, ""},
		{"started as hermod", "hermod attach prod-box -n", env{}, "/home/u",
			"prod-box", sandbox.Command{}, true, false, ""},
		{"hermod by path", "/usr/local/bin/hermod attach prod-box -- claude --version", env{}, "/home/u",
			"prod-box", sandbox.Exec([]string{"claude", "--version"}), false, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var c capture
			a := testApp(&c, tc.env, fixedHostname("laptop"), tc.home)
			if err := execute(context.Background(), a, strings.Fields(tc.argv)); err != nil {
				t.Fatalf("execute() error: %v", err)
			}
			if !c.called {
				t.Fatal("runner was not called")
			}
			got := c.resolved()
			if got.Sandbox != tc.wantSandbox || !reflect.DeepEqual(got.Command, tc.wantCommand) || got.DryRun != tc.wantDryRun {
				t.Errorf("sandbox, command, dry run = %q, %+v, %v, want %q, %+v, %v",
					got.Sandbox, got.Command, got.DryRun, tc.wantSandbox, tc.wantCommand, tc.wantDryRun)
			}
			if got.StandIn != tc.wantStandIn || got.SessionLogDir != tc.wantLogDir {
				t.Errorf("stand-in, log dir = %v, %q, want %v, %q", got.StandIn, got.SessionLogDir, tc.wantStandIn, tc.wantLogDir)
			}
		})
	}
}

// TestStandInWithoutSandboxRunsNothing asserts the stand-in fails before the
// runner, which starts ssh, Mutagen and tmux, when no sandbox can be resolved.
func TestStandInWithoutSandboxRunsNothing(t *testing.T) {
	cases := []struct {
		name    string
		argv    string
		env     env
		wantErr string
	}{
		{"unset", "claude --version", env{}, envSandbox},
		{"blank", "claude --version", env{envSandbox: "  "}, envSandbox},
		{"read as an ssh option", "claude --version", env{envSandbox: "-oProxyCommand=x"}, "starts with '-'"},
		{"an argument is no sandbox", "claude prod-box", env{}, envSandbox},
		{"holding a newline", "claude --version", env{envSandbox: "prod\nbox"}, "control character"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var c capture
			err := execute(context.Background(), testApp(&c, tc.env, fixedHostname("laptop"), "/home/u"), strings.Fields(tc.argv))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("execute() error = %v, want one mentioning %q", err, tc.wantErr)
			}
			if c.called {
				t.Error("runner was called without a sandbox")
			}
		})
	}
}

func TestExitCode(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantCode   int
		wantStderr string
	}{
		{"success", nil, 0, ""},
		{"program's code passed on silently", control.ExitStatus(3), 3, ""},
		{"wrapped program's code", fmt.Errorf("run: %w", control.ExitStatus(127)), 127, ""},
		{"Hermod's own failure", errors.New("open mirror: boom"), 1, "hermod: open mirror: boom\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			if got := exitCode(tc.err, &stderr); got != tc.wantCode {
				t.Errorf("exitCode() = %d, want %d", got, tc.wantCode)
			}
			if stderr.String() != tc.wantStderr {
				t.Errorf("stderr = %q, want %q", stderr.String(), tc.wantStderr)
			}
		})
	}
}

func TestXDGHome(t *testing.T) {
	cases := []struct {
		name string
		env  env
		home string
		want string
	}{
		{"absolute variable wins", env{envCacheHome: "/xdg/cache"}, "/home/u", "/xdg/cache"},
		{"unset falls back under home", env{}, "/home/u", "/home/u/.cache"},
		{"empty falls back under home", env{envCacheHome: ""}, "/home/u", "/home/u/.cache"},
		{"relative ignored", env{envCacheHome: "rel/cache"}, "/home/u", "/home/u/.cache"},
		{"absolute variable without home", env{envCacheHome: "/xdg/cache"}, "", "/xdg/cache"},
		{"nothing known", env{}, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := xdgHome(envCacheHome, ".cache", tc.home, tc.env.lookup); got != tc.want {
				t.Errorf("xdgHome() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestClaudeConfigDir(t *testing.T) {
	cases := []struct {
		name string
		env  env
		home string
		want string
	}{
		{"default under home", env{}, "/home/u", "/home/u/.claude"},
		{"from the environment", env{envClaudeConfigDir: "/home/u/.local/share/hermod/air/prod/claude"}, "/home/u", "/home/u/.local/share/hermod/air/prod/claude"},
		{"empty variable is unset", env{envClaudeConfigDir: ""}, "/home/u", "/home/u/.claude"},
		{"no home, no directory", env{}, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := claudeConfigDir(tc.home, tc.env.lookup); got != tc.want {
				t.Errorf("claudeConfigDir() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLeaseDir(t *testing.T) {
	cases := []struct {
		name string
		env  env
		home string
		want string
	}{
		{"runtime directory", env{envRuntimeDir: "/run/user/1000"}, "/home/u", "/run/user/1000/hermod/lease"},
		{"relative runtime directory ignored", env{envRuntimeDir: "run"}, "/home/u", "/home/u/.cache/hermod/lease"},
		{"no runtime directory: the cache", env{}, "/home/u", "/home/u/.cache/hermod/lease"},
		{"no runtime directory: the cache base from the environment", env{envCacheHome: "/c"}, "/home/u", "/c/hermod/lease"},
		{"nothing to go by", env{}, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := leaseDir(tc.home, tc.env.lookup); got != tc.want {
				t.Errorf("leaseDir() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestStandInCarriesTheEnvironment asserts the stand-in hands the run the
// caller's environment, from which a session selects Claude's variables.
func TestStandInCarriesTheEnvironment(t *testing.T) {
	cases := []struct {
		name          string
		env           env
		wantEnviron   []string
		wantConfigDir string
	}{
		{
			"Air's environment",
			env{envSandbox: "prod-box", "CLAUDE_CODE_ENTRYPOINT": "cli", envClaudeConfigDir: "/c"},
			[]string{"CLAUDE_CODE_ENTRYPOINT=cli", "CLAUDE_CONFIG_DIR=/c", "HERMOD_SANDBOX=prod-box"},
			"/c",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var c capture
			a := testApp(&c, tc.env, fixedHostname("laptop"), "/home/u")
			if err := execute(context.Background(), a, []string{"claude", "--session-id", standInUUID}); err != nil {
				t.Fatalf("execute() error: %v", err)
			}
			got := c.resolved()
			if !reflect.DeepEqual(got.Environ.Reveal(), tc.wantEnviron) || got.ClaudeConfigDir != tc.wantConfigDir {
				t.Errorf("environ, config dir = %q, %q, want %q, %q", got.Environ.Reveal(), got.ClaudeConfigDir, tc.wantEnviron, tc.wantConfigDir)
			}
		})
	}
}
