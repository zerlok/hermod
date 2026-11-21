package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/zerlok/hermod/internal/control"
	"github.com/zerlok/hermod/internal/sandbox"
	"github.com/zerlok/hermod/internal/secret"
)

// capture records the arguments the runner was handed.
type capture struct {
	called  bool
	sandbox string
	opts    []control.Option
}

// resolved replays the captured options into an Options, exactly as control.Run
// would, so a test can assert the effective settings the invocation produced.
func (c capture) resolved() control.Options {
	o := control.Options{Sandbox: c.sandbox}
	for _, opt := range c.opts {
		opt(&o)
	}
	return o
}

// env is a fixed environment; a key that is absent is unset.
type env map[string]string

func (e env) lookup(key string) (string, bool) {
	v, ok := e[key]
	return v, ok
}

// environ is the environment as KEY=VALUE entries, in key order.
func (e env) environ() []string {
	var out []string
	for k, v := range e {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

// testApp wires the commands to a capturing runner and a fixed environment so
// parsing is deterministic.
func testApp(c *capture, e env, hostname func() (string, error), home string) app {
	return app{
		run: func(_ context.Context, sandbox string, opts ...control.Option) error {
			c.called = true
			c.sandbox = sandbox
			c.opts = opts
			return nil
		},
		cwd:       func() (string, error) { return "/home/u/api", nil },
		home:      func() (string, error) { return home, nil },
		hostname:  hostname,
		lookupEnv: e.lookup,
		environ:   e.environ,
	}
}

func fixedHostname(name string) func() (string, error) {
	return func() (string, error) { return name, nil }
}

func failingHostname() (string, error) { return "", errors.New("no hostname") }

func TestAttachResolvesOptions(t *testing.T) {
	cases := []struct {
		name        string
		commandLine string // the args as typed, split on spaces for readability
		env         env
		hostname    func() (string, error)
		wantSandbox string
		wantSession string
		wantDir     string
		wantRemote  string
		wantCommand sandbox.Command
		wantOrigin  string
		wantDryRun  bool
		wantQuiet   bool
	}{
		{"defaults from cwd", "attach prod", env{}, fixedHostname("laptop"),
			"prod", "api", "/home/u/api", "api", sandbox.Command{}, "laptop", false, false},
		{"explicit session", "attach prod -s my-session", env{}, fixedHostname("laptop"),
			"prod", "my-session", "/home/u/api", "api", sandbox.Command{}, "laptop", false, false},
		{"explicit directory slug session", "attach prod -C /home/u/work/svc", env{}, fixedHostname("laptop"),
			"prod", "work-svc", "/home/u/work/svc", "work/svc", sandbox.Command{}, "laptop", false, false},
		{"dry-run flag", "attach prod -n", env{}, fixedHostname("laptop"),
			"prod", "api", "/home/u/api", "api", sandbox.Command{}, "laptop", true, false},
		{"quiet flag", "attach prod -q", env{}, fixedHostname("laptop"),
			"prod", "api", "/home/u/api", "api", sandbox.Command{}, "laptop", false, true},
		{"passthrough verbatim", "attach prod -- htop -d 5", env{}, fixedHostname("laptop"),
			"prod", "api", "/home/u/api", "api", sandbox.Exec([]string{"htop", "-d", "5"}), "laptop", false, false},
		{"flags then passthrough", "attach prod -n -- htop -d 5", env{}, fixedHostname("laptop"),
			"prod", "api", "/home/u/api", "api", sandbox.Exec([]string{"htop", "-d", "5"}), "laptop", true, false},
		{"sandbox from the environment", "attach", env{envSandbox: "env-box"}, fixedHostname("laptop"),
			"env-box", "api", "/home/u/api", "api", sandbox.Command{}, "laptop", false, false},
		{"sandbox from the environment with flags and passthrough", "attach -n -- htop", env{envSandbox: "env-box"}, fixedHostname("laptop"),
			"env-box", "api", "/home/u/api", "api", sandbox.Exec([]string{"htop"}), "laptop", true, false},
		{"argument wins over the environment's sandbox", "attach prod", env{envSandbox: "env-box"}, fixedHostname("laptop"),
			"prod", "api", "/home/u/api", "api", sandbox.Command{}, "laptop", false, false},
		{"command line from the environment", "attach prod", env{envSandboxCmd: "htop -d 5 | tee log"}, fixedHostname("laptop"),
			"prod", "api", "/home/u/api", "api", sandbox.ShellLine("htop -d 5 | tee log"), "laptop", false, false},
		{"passthrough wins over the environment's command", "attach prod -- top", env{envSandboxCmd: "htop"}, fixedHostname("laptop"),
			"prod", "api", "/home/u/api", "api", sandbox.Exec([]string{"top"}), "laptop", false, false},
		{"empty passthrough falls back to the environment's command", "attach prod --", env{envSandboxCmd: "htop"}, fixedHostname("laptop"),
			"prod", "api", "/home/u/api", "api", sandbox.ShellLine("htop"), "laptop", false, false},
		{"empty environment command runs the default shell", "attach prod", env{envSandboxCmd: ""}, fixedHostname("laptop"),
			"prod", "api", "/home/u/api", "api", sandbox.Command{}, "laptop", false, false},
		{"blank environment command runs the default shell", "attach prod", env{envSandboxCmd: "   "}, fixedHostname("laptop"),
			"prod", "api", "/home/u/api", "api", sandbox.Command{}, "laptop", false, false},
		{"blank environment sandbox yields to the argument", "attach prod", env{envSandbox: "  "}, fixedHostname("laptop"),
			"prod", "api", "/home/u/api", "api", sandbox.Command{}, "laptop", false, false},
		{"origin from the environment", "attach prod", env{envOrigin: "work-laptop"}, fixedHostname("laptop"),
			"prod", "api", "/home/u/api", "api", sandbox.Command{}, "work-laptop", false, false},
		{"empty origin names nothing", "attach prod", env{envOrigin: ""}, fixedHostname("laptop"),
			"prod", "api", "/home/u/api", "api", sandbox.Command{}, "", false, false},
		{"origin reduced to safe characters", "attach prod", env{}, fixedHostname("my laptop_2.local"),
			"prod", "api", "/home/u/api", "api", sandbox.Command{}, "my-laptop-2.local", false, false},
		{"unreadable hostname names nothing", "attach prod", env{}, failingHostname,
			"prod", "api", "/home/u/api", "api", sandbox.Command{}, "", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var c capture
			cmd := newRootCmd(testApp(&c, tc.env, tc.hostname, "/home/u"))
			cmd.SetArgs(strings.Fields(tc.commandLine))
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			if err := cmd.Execute(); err != nil {
				t.Fatalf("Execute() error: %v", err)
			}
			if !c.called {
				t.Fatal("runner was not called")
			}
			want := control.Options{
				Sandbox:   tc.wantSandbox,
				Session:   tc.wantSession,
				LocalDir:  tc.wantDir,
				RemoteDir: tc.wantRemote,
				Command:   tc.wantCommand,
				Origin:    tc.wantOrigin,
				DryRun:    tc.wantDryRun,
				Quiet:     tc.wantQuiet,

				Environ:         secret.NewEnv(tc.env.environ()),
				ClaudeConfigDir: "/home/u/.claude",
				LeaseDir:        "/home/u/.cache/hermod/lease",
			}
			if got := c.resolved(); !reflect.DeepEqual(got, want) {
				t.Errorf("Options = %+v, want %+v", got, want)
			}
		})
	}
}

// TestInvocationErrorsRunNothing asserts every rejected invocation fails before
// the runner, which starts all syncs and sessions, is reached.
func TestInvocationErrorsRunNothing(t *testing.T) {
	cases := []struct {
		name        string
		commandLine string
		env         env
	}{
		{"no sandbox anywhere", "attach", env{}},
		{"empty environment sandbox", "attach", env{envSandbox: ""}},
		{"blank environment sandbox", "attach", env{envSandbox: "  "}},
		{"environment sandbox read as an ssh option", "attach", env{envSandbox: "-oProxyCommand=touch /tmp/x"}},
		{"argument starting with a dash", "attach -", env{}},
		{"argument holding a quote", `attach we"ird`, env{}},
		{"environment sandbox holding a tab", "attach", env{envSandbox: "a\tb"}},
		{"only passthrough", "attach -- htop", env{}},
		{"only flags", "attach -n -s x", env{envSandboxCmd: "htop"}},
		{"two aliases", "attach prod staging", env{}},
		{"bare sandbox form", "prod", env{}},
		{"bare sandbox form with the environment set", "prod", env{envSandbox: "prod"}},
		{"removed notify command", "notify done", env{envSandbox: "prod"}},
		{"removed no-notify flag", "attach prod -N", env{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var c capture
			cmd := newRootCmd(testApp(&c, tc.env, fixedHostname("laptop"), "/home/u"))
			cmd.SetArgs(strings.Fields(tc.commandLine))
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			if err := cmd.Execute(); err == nil {
				t.Fatal("expected error, got nil")
			}
			if c.called {
				t.Error("runner was called despite an invocation error")
			}
		})
	}
}

// TestAttachCompletesSandboxHosts runs cobra's completion entry point, which the
// generated shell scripts call, so the hosts reach `hermod attach <TAB>`.
func TestAttachCompletesSandboxHosts(t *testing.T) {
	cases := []struct {
		name      string
		sshConfig string
		args      []string
		want      []string
	}{
		{"first argument offers hosts", "Host prod-box\nHost api\nHost *\n", []string{"attach", ""}, []string{"api", "prod-box"}},
		{"after flags still offers hosts", "Host prod-box\n", []string{"attach", "-n", ""}, []string{"prod-box"}},
		{"second argument offers nothing", "Host prod-box\n", []string{"attach", "prod-box", ""}, nil},
		{"no ssh config offers nothing", "", []string{"attach", ""}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			if tc.sshConfig != "" {
				if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(home, ".ssh", "config"), []byte(tc.sshConfig), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var c capture
			var out bytes.Buffer
			cmd := newRootCmd(testApp(&c, env{}, fixedHostname("laptop"), home))
			cmd.SetArgs(append([]string{"__complete"}, tc.args...))
			cmd.SetOut(&out)
			cmd.SetErr(io.Discard)
			if err := cmd.Execute(); err != nil {
				t.Fatalf("Execute() error: %v", err)
			}
			if got := completions(out.String()); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("completions = %q, want %q", got, tc.want)
			}
			if c.called {
				t.Error("runner was called while completing")
			}
		})
	}
}

// completions reads the candidates from cobra's `__complete` output, which ends
// with a `:<directive>` line.
func completions(out string) []string {
	var got []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		got = append(got, line)
	}
	return got
}

func TestParseSSHHosts(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  []string
	}{
		{"aliases sorted and deduped", "Host prod-box\nHost api\nHost prod-box\n", []string{"api", "prod-box"}},
		{"multiple aliases per line", "Host a b c\n", []string{"a", "b", "c"}},
		{"patterns skipped", "Host *\nHost web-?\nHost real\n", []string{"real"}},
		{"case-insensitive keyword and indentation", "  host  Web\n", []string{"Web"}},
		{"empty config", "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseSSHHosts(strings.NewReader(tc.input)); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseSSHHosts() = %v, want %v", got, tc.want)
			}
		})
	}
}
