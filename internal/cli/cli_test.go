package cli

import (
	"context"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/zerlok/hermod/internal/control"
	"github.com/zerlok/hermod/internal/notify"
)

// capture records the arguments the runner (or notify sender) was handed.
type capture struct {
	called  bool
	sandbox string
	opts    []control.Option
	sent    *notify.Message
}

// resolved replays the captured options into an Options, exactly as control.Run
// would, so a test can assert the effective settings the flags produced.
func (c capture) resolved() control.Options {
	o := control.Options{Sandbox: c.sandbox}
	for _, opt := range c.opts {
		opt(&o)
	}
	return o
}

// testApp wires the root command to a capturing runner and a fixed environment
// so parsing is deterministic.
func testApp(c *capture) app {
	return app{
		run: func(_ context.Context, sandbox string, opts ...control.Option) error {
			c.called = true
			c.sandbox = sandbox
			c.opts = opts
			return nil
		},
		send: func(_ context.Context, m notify.Message) error {
			c.sent = &m
			return nil
		},
		cwd:  func() (string, error) { return "/home/u/api", nil },
		home: func() (string, error) { return "/home/u", nil },
	}
}

func TestParseIntoOptions(t *testing.T) {
	cases := []struct {
		name        string
		commandLine string // the args as typed, split on spaces for readability
		wantSandbox string
		wantSession string
		wantDir     string
		wantRemote  string
		wantCommand []string
		wantDryRun  bool
		wantQuiet   bool
		wantNotify  bool
	}{
		{"defaults from cwd", "prod", "prod", "api", "/home/u/api", "api", nil, false, false, false},
		{"explicit session", "prod -s my-session", "prod", "my-session", "/home/u/api", "api", nil, false, false, false},
		{"explicit directory slug session", "prod -C /home/u/work/svc", "prod", "work-svc", "/home/u/work/svc", "work/svc", nil, false, false, false},
		{"dry-run flag", "prod -n", "prod", "api", "/home/u/api", "api", nil, true, false, false},
		{"quiet flag", "prod -q", "prod", "api", "/home/u/api", "api", nil, false, true, false},
		{"notify flag", "prod -N", "prod", "api", "/home/u/api", "api", nil, false, false, true},
		{"dry-run and notify compose", "prod -n -N", "prod", "api", "/home/u/api", "api", nil, true, false, true},
		{"passthrough verbatim", "prod -- claude --model opus", "prod", "api", "/home/u/api", "api", []string{"claude", "--model", "opus"}, false, false, false},
		{"flags then passthrough", "prod -n -- claude --model opus", "prod", "api", "/home/u/api", "api", []string{"claude", "--model", "opus"}, true, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var c capture
			cmd := newRootCmd(testApp(&c))
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
				DryRun:    tc.wantDryRun,
				Quiet:     tc.wantQuiet,
				Notify:    tc.wantNotify,
			}
			if got := c.resolved(); !reflect.DeepEqual(got, want) {
				t.Errorf("Options = %+v, want %+v", got, want)
			}
		})
	}
}

func TestMissingOrExtraSandboxFails(t *testing.T) {
	cases := []struct {
		name        string
		commandLine string
	}{
		{"no argument", ""},
		{"only passthrough", "-- claude"},
		{"two aliases", "prod staging"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var c capture
			cmd := newRootCmd(testApp(&c))
			cmd.SetArgs(strings.Fields(tc.commandLine))
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			if err := cmd.Execute(); err == nil {
				t.Fatal("expected error, got nil")
			}
			if c.called {
				t.Error("runner was called despite an argument error")
			}
		})
	}
}

func TestNotifySubcommandSends(t *testing.T) {
	cases := []struct {
		name        string
		commandLine string
		wantTitle   string
		wantUrgency string
		wantBody    string
	}{
		{"body from positional args", "notify build finished", "", "", "build finished"},
		{"title and urgency long flags", "notify --title CI --urgency critical failed", "CI", "critical", "failed"},
		{"title and urgency short flags", "notify -t CI -u low done", "CI", "low", "done"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var c capture
			cmd := newRootCmd(testApp(&c))
			cmd.SetArgs(strings.Fields(tc.commandLine))
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			if err := cmd.Execute(); err != nil {
				t.Fatalf("Execute() error: %v", err)
			}
			if c.called {
				t.Error("root runner was called for a notify subcommand")
			}
			if c.sent == nil {
				t.Fatal("notify sender was not called")
			}
			want := notify.Message{Title: tc.wantTitle, Urgency: tc.wantUrgency, Body: tc.wantBody}
			if *c.sent != want {
				t.Errorf("sent = %+v, want %+v", *c.sent, want)
			}
		})
	}
}

func TestNotifySubcommandRequiresBody(t *testing.T) {
	cases := []struct {
		name        string
		commandLine string
	}{
		{"no body argument", "notify"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var c capture
			cmd := newRootCmd(testApp(&c))
			cmd.SetArgs(strings.Fields(tc.commandLine))
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			if err := cmd.Execute(); err == nil {
				t.Fatal("expected an error when no body is given")
			}
			if c.sent != nil {
				t.Error("sender called despite missing body")
			}
		})
	}
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
