package cli

import (
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/zerlok/hermod/internal/control"
)

// configureApp wires configure to a recorder, with node and the adapter found
// where paths says, the binary at /home/u/go/bin/hermod and cwd /home/u/api.
func configureApp(got *[]control.Registration, e env, paths map[string]string) app {
	a := testApp(&capture{}, e, fixedHostname("laptop"), "/home/u")
	a.configure = func(r control.Registration) error {
		r.Out = nil // the command's own stream, not part of what is resolved
		*got = append(*got, r)
		return nil
	}
	a.lookPath = func(file string) (string, error) {
		if p, ok := paths[file]; ok {
			return p, nil
		}
		return "", errors.New("executable file not found in $PATH")
	}
	a.executable = func() (string, error) { return "/home/u/go/bin/hermod", nil }
	return a
}

func strPtr(s string) *string { return &s }

func TestConfigureResolves(t *testing.T) {
	onPath := map[string]string{
		"node":             "/opt/node/bin/node",
		"claude-agent-acp": "/opt/node/bin/claude-agent-acp",
		"./node":           "node",
		"/srv/node":        "/srv/node",
	}
	base := control.Registration{
		Sandbox:  "prod-box",
		Node:     "/opt/node/bin/node",
		Adapter:  "/opt/node/bin/claude-agent-acp",
		Hermod:   "/home/u/go/bin/hermod",
		DataHome: "/home/u/.local/share",
		Home:     "/home/u",
	}
	with := func(mod func(r *control.Registration)) control.Registration {
		r := base
		mod(&r)
		return r
	}
	cases := []struct {
		name        string
		commandLine string
		env         env
		want        control.Registration
	}{
		{"defaults", "air configure claude prod-box", env{}, base},
		{"sandbox from the environment", "air configure claude", env{envSandbox: "prod-box"}, base},
		{"argument wins over the environment", "air configure claude prod-box", env{envSandbox: "other"}, base},
		{"name", "air configure claude prod-box --name=Remote", env{}, with(func(r *control.Registration) { r.Name = "Remote" })},
		{"dry run", "air configure claude prod-box -n", env{}, with(func(r *control.Registration) { r.DryRun = true })},
		{"origin flag", "air configure claude prod-box --origin=work", env{envOrigin: "env-origin"}, with(func(r *control.Registration) { r.Origin = strPtr("work") })},
		{"empty origin flag", "air configure claude prod-box --origin=", env{envOrigin: "env-origin"}, with(func(r *control.Registration) { r.Origin = strPtr("") })},
		{"origin from the environment", "air configure claude prod-box", env{envOrigin: "env-origin"}, with(func(r *control.Registration) { r.Origin = strPtr("env-origin") })},
		{"empty origin from the environment", "air configure claude prod-box", env{envOrigin: ""}, with(func(r *control.Registration) { r.Origin = strPtr("") })},
		{"node by absolute path", "air configure claude prod-box --node=/srv/node", env{}, with(func(r *control.Registration) { r.Node = "/srv/node" })},
		{"node by relative path made absolute", "air configure claude prod-box --node=./node", env{}, with(func(r *control.Registration) { r.Node = "/home/u/api/node" })},
		{"XDG data home", "air configure claude prod-box", env{envDataHome: "/xdg/data"}, with(func(r *control.Registration) { r.DataHome = "/xdg/data" })},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []control.Registration
			cmd := newRootCmd(configureApp(&got, tc.env, onPath))
			cmd.SetArgs(strings.Fields(tc.commandLine))
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			if err := cmd.Execute(); err != nil {
				t.Fatalf("Execute() error: %v", err)
			}
			if len(got) != 1 || !reflect.DeepEqual(got[0], tc.want) {
				t.Errorf("Registration = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestConfigureErrorsChangeNothing asserts every rejected configure fails
// before anything is written.
func TestConfigureErrorsChangeNothing(t *testing.T) {
	onPath := map[string]string{"node": "/opt/node/bin/node", "claude-agent-acp": "/opt/node/bin/claude-agent-acp"}
	cases := []struct {
		name        string
		commandLine string
		env         env
		paths       map[string]string
		wantErr     string
	}{
		{"no agent", "air configure", env{envSandbox: "prod-box"}, onPath, "name the agent"},
		{"unknown agent", "air configure codex prod-box", env{}, onPath, `unknown agent "codex"`},
		{"no sandbox", "air configure claude", env{}, onPath, envSandbox},
		{"sandbox read as an ssh option", "air configure claude", env{envSandbox: "-oProxyCommand=x"}, onPath, "starts with '-'"},
		{"sandbox holding a space", "air configure claude", env{envSandbox: "a b"}, onPath, "holds whitespace"},
		{"sandbox padded with spaces", "air configure claude", env{envSandbox: " prod-box"}, onPath, "holds whitespace"},
		{"sandbox holding a double quote", `air configure claude we"ird`, env{}, onPath, "a quote"},
		{"sandbox holding a single quote", "air configure claude we'ird", env{}, onPath, "a quote"},
		{"sandbox holding a control character", "air configure claude", env{envSandbox: "a\x1bb"}, onPath, "control character"},
		{"two sandboxes", "air configure claude a b", env{}, onPath, "only one sandbox"},
		{"no node", "air configure claude prod-box", env{}, map[string]string{"claude-agent-acp": "/a"}, "give its path with --node"},
		{"no adapter", "air configure claude prod-box", env{}, map[string]string{"node": "/n"}, "give its path with --acp"},
		{"air with an unknown subcommand", "air prod-box", env{}, onPath, "unknown command"},
		{"air alone", "air", env{}, onPath, "name a subcommand"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []control.Registration
			cmd := newRootCmd(configureApp(&got, tc.env, tc.paths))
			cmd.SetArgs(strings.Fields(tc.commandLine))
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			err := cmd.Execute()
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("Execute() error = %v, want one mentioning %q", err, tc.wantErr)
			}
			if len(got) != 0 {
				t.Errorf("configure ran: %+v", got)
			}
		})
	}
}
