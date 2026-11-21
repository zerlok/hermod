package cli

import (
	"context"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/zerlok/hermod/internal/control"
)

// acpApp wires `air acp` to a recorder, home /home/u.
func acpApp(got *[]control.ACPAdapter, e env, home string) app {
	a := testApp(&capture{}, e, fixedHostname("laptop"), home)
	a.runACP = func(_ context.Context, r control.ACPAdapter) error {
		r.Stdio = control.Streams{} // the command's own streams, not part of what is resolved
		*got = append(*got, r)
		return nil
	}
	return a
}

func TestACPResolves(t *testing.T) {
	base := control.ACPAdapter{
		Sandbox:       "prod-box",
		Node:          "node",
		Script:        "claude-agent-acp",
		SessionLogDir: "/home/u/.cache/hermod/air",
		LeaseDir:      "/home/u/.cache/hermod/lease",
	}
	with := func(mod func(r *control.ACPAdapter)) control.ACPAdapter {
		r := base
		mod(&r)
		return r
	}
	cases := []struct {
		name        string
		commandLine string
		env         env
		home        string
		want        control.ACPAdapter
	}{
		{"defaults", "air acp claude prod-box", env{}, "/home/u", base},
		{"sandbox from the environment", "air acp claude", env{envSandbox: "prod-box"}, "/home/u", base},
		{"argument wins over the environment", "air acp claude prod-box", env{envSandbox: "other"}, "/home/u", base},
		{"the entry's programs", "air acp claude --node=/opt/node/bin/node --acp=/opt/node/bin/claude-agent-acp", env{envSandbox: "prod-box"}, "/home/u",
			with(func(r *control.ACPAdapter) { r.Node, r.Script = "/opt/node/bin/node", "/opt/node/bin/claude-agent-acp" })},
		{"runtime directory for leases", "air acp claude prod-box", env{envRuntimeDir: "/run/user/1000"}, "/home/u",
			with(func(r *control.ACPAdapter) { r.LeaseDir = "/run/user/1000/hermod/lease" })},
		{"XDG cache home", "air acp claude prod-box", env{envCacheHome: "/xdg/cache"}, "/home/u",
			with(func(r *control.ACPAdapter) {
				r.SessionLogDir, r.LeaseDir = "/xdg/cache/hermod/air", "/xdg/cache/hermod/lease"
			})},
		{"no home: no log, no lease", "air acp claude prod-box", env{}, "",
			with(func(r *control.ACPAdapter) { r.SessionLogDir, r.LeaseDir = "", "" })},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []control.ACPAdapter
			cmd := newRootCmd(acpApp(&got, tc.env, tc.home))
			cmd.SetArgs(strings.Fields(tc.commandLine))
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			if err := cmd.Execute(); err != nil {
				t.Fatalf("Execute() error: %v", err)
			}
			if len(got) != 1 || !reflect.DeepEqual(got[0], tc.want) {
				t.Errorf("ACPAdapter = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestACPHandsOverItsStreams asserts the adapter gets the command's own
// streams, which carry ACP.
func TestACPHandsOverItsStreams(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"Air's stream", `{"jsonrpc":"2.0","id":0,"method":"initialize"}` + "\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got control.Streams
			a := testApp(&capture{}, env{envSandbox: "prod-box"}, fixedHostname("laptop"), "/home/u")
			a.runACP = func(_ context.Context, r control.ACPAdapter) error {
				got = r.Stdio
				return nil
			}
			in, out, errOut := strings.NewReader(tc.in), &strings.Builder{}, &strings.Builder{}
			cmd := newRootCmd(a)
			cmd.SetArgs([]string{"air", "acp", "claude"})
			cmd.SetIn(in)
			cmd.SetOut(out)
			cmd.SetErr(errOut)
			if err := cmd.Execute(); err != nil {
				t.Fatalf("Execute() error: %v", err)
			}
			if got.In != in || got.Out != out || got.Err != errOut {
				t.Errorf("streams = %+v, want the command's", got)
			}
		})
	}
}

// TestACPErrorsRunNothing asserts every rejected `air acp` fails before the
// adapter starts.
func TestACPErrorsRunNothing(t *testing.T) {
	cases := []struct {
		name        string
		commandLine string
		env         env
		wantErr     string
	}{
		{"no agent", "air acp", env{envSandbox: "prod-box"}, "name the agent"},
		{"unknown agent", "air acp codex prod-box", env{}, `unknown agent "codex"`},
		{"no sandbox", "air acp claude", env{}, envSandbox},
		{"sandbox read as an ssh option", "air acp claude", env{envSandbox: "-oProxyCommand=x"}, "starts with '-'"},
		{"two sandboxes", "air acp claude a b", env{}, "only one sandbox"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []control.ACPAdapter
			cmd := newRootCmd(acpApp(&got, tc.env, "/home/u"))
			cmd.SetArgs(strings.Fields(tc.commandLine))
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			err := cmd.Execute()
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("Execute() error = %v, want one mentioning %q", err, tc.wantErr)
			}
			if len(got) != 0 {
				t.Errorf("the adapter ran: %+v", got)
			}
		})
	}
}
