package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zerlok/hermod/internal/agent"
	"github.com/zerlok/hermod/internal/session"
	"github.com/zerlok/hermod/internal/shell"
	"github.com/zerlok/hermod/internal/workspace"
)

// attachView is what an attach's request resolved, comparable in a table.
// A stream or the plan is named by the process's stream it is, "" for none.
type attachView struct {
	remote, remoteHome string
	dir, name          string
	args               []string
	forwards           []session.Forward
	origin, locks      string
	in, out, err       string
	plan               string
}

func viewAttach(req session.Request, r *recorder) attachView {
	v := attachView{
		remote: req.Remote.Host, remoteHome: req.Remote.Home,
		dir: req.Dir, name: req.Name, args: req.Args, forwards: req.Forwards,
		origin: req.Local.Origin, locks: req.Local.Locks,
		in: r.streamName(req.Streams.In), out: r.streamName(req.Streams.Out), err: r.streamName(req.Streams.Err),
	}
	if p, dry := req.Remote.Exec.(planner); dry {
		v.plan = r.streamName(p.plan)
	}
	return v
}

var attachBase = attachView{
	remote: "prod-box", remoteHome: testRemoteHome,
	dir: testCwd, name: "dev-api",
	origin: testHostname, locks: testHome + "/.cache/hermod/lease",
	in: "stdin", out: "stdout", err: "stderr",
}

func attachWith(change func(v *attachView)) attachView {
	v := attachBase
	change(&v)
	return v
}

// TestAttachResolves runs every argument and variable of `attach`, and of the
// stand-in, which is `attach -- claude ARGS…` under another name, and checks
// the request each one hands the core.
func TestAttachResolves(t *testing.T) {
	cases := []struct {
		name     string
		argv     string // split on spaces; the first word is the program name
		env      vars
		terminal bool
		want     attachView
	}{
		{"the remote as an argument", "hermod attach prod-box", vars{}, true, attachBase},
		{"the remote from the environment", "hermod attach", vars{envRemote: "prod-box"}, true, attachBase},
		{"an argument wins over the environment", "hermod attach prod-box", vars{envRemote: "other"}, true, attachBase},
		{"a blank variable is unset", "hermod attach prod-box", vars{envRemote: " "}, true, attachBase},
		{"a worker's name", "hermod attach prod-box -s my-session", vars{}, true,
			attachWith(func(v *attachView) { v.name = "my-session" })},
		{"a directory", "hermod attach prod-box -C /home/me/work/web.v2", vars{}, true,
			attachWith(func(v *attachView) { v.dir, v.name = "/home/me/work/web.v2", "work-web-v2-7422e219" })},
		{"a directory whose dash is its own", "hermod attach prod-box -C /home/me/work/web-v2", vars{}, true,
			attachWith(func(v *attachView) { v.dir, v.name = "/home/me/work/web-v2", "work-web-v2" })},
		{"a directory of a key's characters", "hermod attach prod-box -C ~/a@b+c_d", vars{}, true,
			attachWith(func(v *attachView) { v.dir, v.name = "/home/me/a@b+c_d", "a@b+c_d" })},
		{"a directory starting with a dash", "hermod attach prod-box -C ~/-foo", vars{}, true,
			attachWith(func(v *attachView) { v.dir, v.name = "/home/me/-foo", "foo-9e796486" })},
		{"a directory under the home", "hermod attach prod-box -C ~/work", vars{}, true,
			attachWith(func(v *attachView) { v.dir, v.name = "/home/me/work", "work" })},
		{"a relative directory", "hermod attach prod-box -C ../web", vars{}, true,
			attachWith(func(v *attachView) { v.dir, v.name = "/home/me/dev/web", "dev-web" })},
		{"a directory outside the home", "hermod attach prod-box -C /opt/work/api", vars{}, true,
			attachWith(func(v *attachView) { v.dir, v.name = "/opt/work/api", "opt-work-api" })},
		{"the root directory", "hermod attach prod-box -C /", vars{}, true,
			attachWith(func(v *attachView) { v.dir, v.name = "/", "8a5edab2" })},
		{"a directory in another script", "hermod attach prod-box -C ~/проект", vars{}, true,
			attachWith(func(v *attachView) { v.dir, v.name = "/home/me/проект", "e5f78e8f" })},
		{"a directory partly in another script", "hermod attach prod-box -C ~/a-проект", vars{}, true,
			attachWith(func(v *attachView) { v.dir, v.name = "/home/me/a-проект", "a-f80b8c35" })},
		{"the home itself", "hermod attach prod-box -C ~", vars{}, true,
			attachWith(func(v *attachView) { v.dir, v.name = testHome, "me" })},
		{"a command", "hermod attach prod-box -- htop -d 5", vars{}, true,
			attachWith(func(v *attachView) { v.args = []string{"htop", "-d", "5"} })},
		{"a command line from the environment", "hermod attach prod-box", vars{envRemoteCmd: "htop -d 5"}, true,
			attachWith(func(v *attachView) { v.args = []string{"sh", "-c", userShellLine, "sh", "htop -d 5"} })},
		{"a command wins over the environment", "hermod attach prod-box -- htop", vars{envRemoteCmd: "top"}, true,
			attachWith(func(v *attachView) { v.args = []string{"htop"} })},
		{"a blank command line is the shell", "hermod attach prod-box", vars{envRemoteCmd: "  "}, true, attachBase},
		{"a relay", "hermod attach prod-box -R 9000:localhost:8080", vars{}, true,
			attachWith(func(v *attachView) { v.forwards = []session.Forward{{Port: 9000, Address: "localhost:8080"}} })},
		{"two relays", "hermod attach prod-box -R 9000:localhost:8080 -R 9001:127.0.0.1:8081", vars{}, true,
			attachWith(func(v *attachView) {
				v.forwards = []session.Forward{{Port: 9000, Address: "localhost:8080"}, {Port: 9001, Address: "127.0.0.1:8081"}}
			})},
		{"a dry run", "hermod attach prod-box -n", vars{}, true,
			attachWith(func(v *attachView) { v.plan = "stdout" })},
		{"an origin", "hermod attach prod-box", vars{envOrigin: "work-laptop"}, true,
			attachWith(func(v *attachView) { v.origin = "work-laptop" })},
		{"an empty origin names none", "hermod attach prod-box", vars{envOrigin: ""}, true,
			attachWith(func(v *attachView) { v.origin = "" })},
		{"a runtime directory for the locks", "hermod attach prod-box", vars{envRuntimeDir: "/run/user/1000"}, true,
			attachWith(func(v *attachView) { v.locks = "/run/user/1000/hermod/lease" })},
		{"a cache home for the locks", "hermod attach prod-box", vars{envCacheHome: "/xdg/cache"}, true,
			attachWith(func(v *attachView) { v.locks = "/xdg/cache/hermod/lease" })},
		{"a relative cache home is ignored", "hermod attach prod-box", vars{envCacheHome: "cache"}, true, attachBase},
		{"hermod by its path", "/usr/local/bin/hermod attach prod-box -- claude --version", vars{}, true,
			attachWith(func(v *attachView) { v.args = []string{"claude", "--version"} })},

		{"started as claude", "claude --session-id 0b7e5f3c-2a41-4c8e-9d6f-1e2a3b4c5d6e", vars{envRemote: "prod-box"}, true,
			attachWith(func(v *attachView) {
				v.args = []string{"claude", "--session-id", "0b7e5f3c-2a41-4c8e-9d6f-1e2a3b4c5d6e"}
			})},
		{"started by the link's path", "/home/me/.local/share/hermod/bin/claude auth status", vars{envRemote: "prod-box"}, false,
			attachWith(func(v *attachView) { v.args = []string{"claude", "auth", "status"} })},
		{"Hermod's flags stay Claude's", "claude -n", vars{envRemote: "prod-box"}, true,
			attachWith(func(v *attachView) { v.args = []string{"claude", "-n"} })},
		{"Hermod's command stays Claude's", "claude attach other-box", vars{envRemote: "prod-box"}, false,
			attachWith(func(v *attachView) { v.args = []string{"claude", "attach", "other-box"} })},
		{"the stand-in ignores the command line", "claude --model opus", vars{envRemote: "prod-box", envRemoteCmd: "htop"}, true,
			attachWith(func(v *attachView) { v.args = []string{"claude", "--model", "opus"} })},
		{"a one-shot call without a terminal", "claude --version", vars{envRemote: "prod-box"}, false,
			attachWith(func(v *attachView) { v.args = []string{"claude", "--version"} })},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, r := newApp(t, setup{env: tc.env, terminal: tc.terminal})
			if err := execute(context.Background(), a, strings.Fields(tc.argv)); err != nil {
				t.Fatalf("execute: %v", err)
			}
			if len(r.attached) != 1 {
				t.Fatalf("attached %d times, want once", len(r.attached))
			}
			req := r.attached[0]
			if got := viewAttach(req, r); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("request\n%+v\nwant\n%+v", got, tc.want)
			}
			if req.Agent == nil || req.LocalAgent == nil || req.IsHook == nil {
				t.Errorf("agent, local agent, hook test = %v, %v, %v; want all given", req.Agent, req.LocalAgent, req.IsHook != nil)
			}
		})
	}
}

// The git commands an attach reads the developer's facts with.
const (
	gitName      = "git config --get user.name"
	gitEmail     = "git config --get user.email"
	gitRevParse  = "git rev-parse --path-format=absolute --git-dir --git-common-dir --show-toplevel"
	gitWorktrees = "git worktree list --porcelain"
)

// TestAttachReadsGit checks that the developer's git identity and the linked
// worktree the directory lies in reach the request.
func TestAttachReadsGit(t *testing.T) {
	cases := []struct {
		name         string
		git          map[string]string
		wantIdentity session.Identity
		wantMain     string
		wantRoot     string
	}{
		{"no repository", nil, session.Identity{}, "", ""},
		{"an identity", map[string]string{gitName: "Ann\n", gitEmail: "ann@example.com\n"},
			session.Identity{Name: "Ann", Email: "ann@example.com"}, "", ""},
		{"a main working tree", map[string]string{
			gitRevParse: "/home/me/dev/api/.git\n/home/me/dev/api/.git\n/home/me/dev/api\n",
		}, session.Identity{}, "", ""},
		{"a linked worktree", map[string]string{
			gitRevParse:  "/home/me/src/api/.git/worktrees/api\n/home/me/src/api/.git\n/home/me/dev/api\n",
			gitWorktrees: "worktree /home/me/src/api\nHEAD abc\nbranch refs/heads/main\n\nworktree /home/me/dev/api\n",
		}, session.Identity{}, "/home/me/src/api", "/home/me/dev/api"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, r := newApp(t, setup{answers: tc.git})
			if err := execute(context.Background(), a, strings.Fields("hermod attach prod-box")); err != nil {
				t.Fatalf("execute: %v", err)
			}
			if len(r.attached) != 1 {
				t.Fatalf("attached %d times, want once", len(r.attached))
			}
			req := r.attached[0]
			if req.Identity != tc.wantIdentity || req.Tree.Main != tc.wantMain || req.Tree.Root != tc.wantRoot {
				t.Errorf("identity, main, root = %+v, %q, %q, want %+v, %q, %q",
					req.Identity, req.Tree.Main, req.Tree.Root, tc.wantIdentity, tc.wantMain, tc.wantRoot)
			}
		})
	}
}

// TestRefusesBeforeAnything checks that an invocation that cannot be
// resolved fails before it runs any command or hands anything to the core.
func TestRefusesBeforeAnything(t *testing.T) {
	cases := []struct {
		name     string
		argv     string
		env      vars
		terminal bool
		wantErr  string
	}{
		{"attach without a remote", "hermod attach", vars{}, true, envRemote},
		{"attach with a blank remote", "hermod attach", vars{envRemote: "  "}, true, envRemote},
		{"a remote read as an ssh option", "hermod attach", vars{envRemote: "-oProxyCommand=x"}, true, "starts with '-'"},
		{"a remote holding a quote", "hermod attach", vars{envRemote: "prod'box"}, true, "quote"},
		{"two remotes", "hermod attach prod-box dev-box", vars{}, true, "only one remote"},
		{"a relay without an address", "hermod attach prod-box -R 9000", vars{}, true, "PORT:ADDRESS"},
		{"a relay to no port", "hermod attach prod-box -R 9000:localhost", vars{}, true, "host:port"},
		{"a relay from no port", "hermod attach prod-box -R http:localhost:80", vars{}, true, "PORT:ADDRESS"},
		{"the bare form", "hermod prod-box", vars{envRemote: "prod-box"}, true, "unknown command"},
		{"acp without a remote", "hermod air acp claude", vars{}, true, envRemote},
		{"acp for another agent", "hermod air acp codex prod-box", vars{}, true, "unknown agent"},
		{"acp without an agent", "hermod air acp", vars{envRemote: "prod-box"}, true, "name the agent"},
		{"configure without a remote", "hermod air configure claude", vars{}, true, envRemote},
		{"configure with two remotes", "hermod air configure claude a b", vars{}, true, "only one remote"},
		{"air without a subcommand", "hermod air", vars{}, true, "name a subcommand"},
		{"the stand-in without a remote", "claude --version", vars{}, true, envRemote},
		{"the stand-in's argument is no remote", "claude prod-box", vars{}, true, envRemote},
		{"the stand-in with a remote holding a newline", "claude --version", vars{envRemote: "prod\nbox"}, true, "control character"},
		{"a conversation without a terminal", "claude --session-id 0b7e5f3c-2a41-4c8e-9d6f-1e2a3b4c5d6e", vars{envRemote: "prod-box"}, false, "hermod air acp"},
		{"Claude's own session without a terminal", "claude", vars{envRemote: "prod-box"}, false, "hermod air acp"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, r := newApp(t, setup{env: tc.env, terminal: tc.terminal})
			err := execute(context.Background(), a, strings.Fields(tc.argv))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("execute: %v, want an error mentioning %q", err, tc.wantErr)
			}
			if r.called() != 0 || len(r.local.ran) != 0 || len(r.logs) != 0 {
				t.Errorf("handed in %d requests, ran %q and opened %d logs, want nothing", r.called(), r.local.ran, len(r.logs))
			}
		})
	}
}

// chatView is what an `air acp`'s request resolved.
type chatView struct {
	remote, remoteHome string
	origin, locks      string
}

// TestACPResolves checks the request `air acp` hands the core.
func TestACPResolves(t *testing.T) {
	base := chatView{remote: "prod-box", remoteHome: testRemoteHome, origin: testHostname, locks: testHome + "/.cache/hermod/lease"}
	cases := []struct {
		name string
		argv string
		env  vars
		want chatView
	}{
		{"the remote as an argument", "hermod air acp claude prod-box", vars{}, base},
		{"the remote from the environment", "hermod air acp claude", vars{envRemote: "prod-box"}, base},
		{"an argument wins over the environment", "hermod air acp claude prod-box", vars{envRemote: "other"}, base},
		{"an origin from the entry", "hermod air acp claude prod-box", vars{envOrigin: "work"}, chatView{
			remote: "prod-box", remoteHome: testRemoteHome, origin: "work", locks: base.locks,
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, r := newApp(t, setup{env: tc.env})
			if err := execute(context.Background(), a, strings.Fields(tc.argv)); err != nil {
				t.Fatalf("execute: %v", err)
			}
			if len(r.chats) != 1 {
				t.Fatalf("served %d chats, want one", len(r.chats))
			}
			req := r.chats[0]
			got := chatView{remote: req.Remote.Host, remoteHome: req.Remote.Home, origin: req.Local.Origin, locks: req.Local.Locks}
			if got != tc.want {
				t.Errorf("request %+v, want %+v", got, tc.want)
			}
			if req.Client == nil || req.Agent == nil || req.ACP == nil || req.Attach == nil {
				t.Errorf("client, agent, ACP, attach = %v, %v, %v, %v; want all given", req.Client, req.Agent, req.ACP, req.Attach != nil)
			}
			if _, dry := req.Remote.Exec.(planner); dry {
				t.Error("a chat plans its commands, want them run")
			}
		})
	}
}

// configureView is what an `air configure`'s request resolved.
type configureView struct {
	remote, name, hermod, dataHome string
	chatArgs                       []string
	standIn                        string
	env                            map[string]string
	searchPath                     []string
	dryRun                         bool
}

// TestConfigureResolves checks the request `air configure` hands Air's
// adapter, and what it prints.
func TestConfigureResolves(t *testing.T) {
	base := configureView{
		remote: "prod-box", hermod: testHermod, dataHome: testHome + "/.local/share",
		chatArgs: []string{"air", "acp", "claude"}, standIn: "claude",
	}
	with := func(change func(v *configureView)) configureView {
		v := base
		change(&v)
		return v
	}
	cases := []struct {
		name       string
		argv       string
		env        vars
		want       configureView
		wantOutput []string
	}{
		{"the remote as an argument", "hermod air configure claude prod-box", vars{}, base,
			[]string{"written to " + testHome + "/.jetbrains/acp.json", envRemote + "=prod-box", "make install"}},
		{"the remote from the environment", "hermod air configure claude", vars{envRemote: "prod-box"}, base,
			[]string{"written to"}},
		{"an argument wins over the environment", "hermod air configure claude prod-box", vars{envRemote: "other"}, base,
			[]string{envRemote + "=prod-box"}},
		{"a name", "hermod air configure claude prod-box --name Prod", vars{}, with(func(v *configureView) { v.name = "Prod" }),
			[]string{`"Prod"`}},
		{"an origin", "hermod air configure claude prod-box --origin=work", vars{},
			with(func(v *configureView) { v.env = map[string]string{envOrigin: "work"} }), nil},
		{"an empty origin", "hermod air configure claude prod-box --origin=", vars{envOrigin: "home"},
			with(func(v *configureView) { v.env = map[string]string{envOrigin: ""} }), nil},
		{"an origin from the environment", "hermod air configure claude prod-box", vars{envOrigin: "home"},
			with(func(v *configureView) { v.env = map[string]string{envOrigin: "home"} }), nil},
		{"an empty origin from the environment", "hermod air configure claude prod-box", vars{envOrigin: ""},
			with(func(v *configureView) { v.env = map[string]string{envOrigin: ""} }), nil},
		{"a data home", "hermod air configure claude prod-box", vars{envDataHome: "/xdg/data"},
			with(func(v *configureView) { v.dataHome = "/xdg/data" }), nil},
		{"the search path", "hermod air configure claude prod-box", vars{envPath: "/usr/bin:/home/me/bin"},
			with(func(v *configureView) { v.searchPath = []string{"/usr/bin", "/home/me/bin"} }), nil},
		{"a dry run", "hermod air configure claude prod-box -n", vars{}, with(func(v *configureView) { v.dryRun = true }),
			[]string{"Dry run", `"command": "` + testHermod + `"`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, r := newApp(t, setup{env: tc.env})
			if err := execute(context.Background(), a, strings.Fields(tc.argv)); err != nil {
				t.Fatalf("execute: %v", err)
			}
			if len(r.configured) != 1 {
				t.Fatalf("configured %d times, want once", len(r.configured))
			}
			req := r.configured[0]
			got := configureView{
				remote: req.Remote, name: req.Name, hermod: req.Hermod, dataHome: req.DataHome,
				chatArgs: req.ChatArgs, standIn: req.StandInName,
				env: req.Env, searchPath: req.SearchPath, dryRun: req.DryRun,
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("request\n%+v\nwant\n%+v", got, tc.want)
			}
			if req.Home != testHome || req.RemoteVariable != envRemote {
				t.Errorf("home, remote variable = %q, %q, want %q, %q", req.Home, req.RemoteVariable, testHome, envRemote)
			}
			for _, want := range tc.wantOutput {
				if !strings.Contains(r.stdout.String(), want) {
					t.Errorf("output %q, want it to hold %q", r.stdout.String(), want)
				}
			}
			if len(r.local.ran) != 0 {
				t.Errorf("ran %q, want nothing", r.local.ran)
			}
		})
	}
}

// TestStandInOutput checks that the stand-in leaves stdout to Claude's
// caller, writing Hermod's own lines to stderr and to the log of the
// session, named after the conversation or the worker; that a log that
// cannot be opened costs only the log; and that a stderr whose reader has
// gone costs no line of the log and does not fail the run.
func TestStandInOutput(t *testing.T) {
	const say = "worker dev-api: attach"
	logs := testHome + "/.cache/hermod/air/"
	cases := []struct {
		name       string
		argv       string
		env        vars
		logFails   bool
		stderrGone bool
		wantLog    string // "" opens none
		wantStderr []string
	}{
		{"a conversation", "claude --resume 0b7e5f3c-2a41-4c8e-9d6f-1e2a3b4c5d6e", vars{envRemote: "prod-box"}, false, false,
			logs + "0b7e5f3c-2a41-4c8e-9d6f-1e2a3b4c5d6e.log", []string{say}},
		{"Claude's own session", "claude", vars{envRemote: "prod-box"}, false, false, logs + "dev-api.log", []string{say}},
		{"a cache home", "claude", vars{envRemote: "prod-box", envCacheHome: "/xdg/cache"}, false, false,
			"/xdg/cache/hermod/air/dev-api.log", []string{say}},
		{"a log that cannot be opened", "claude", vars{envRemote: "prod-box"}, true, false, "",
			[]string{say, "read-only file system"}},
		{"stderr gone", "claude", vars{envRemote: "prod-box"}, false, true, logs + "dev-api.log", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, r := newApp(t, setup{env: tc.env, terminal: true, say: say, logFails: tc.logFails, stderrGone: tc.stderrGone})
			if err := execute(context.Background(), a, strings.Fields(tc.argv)); err != nil {
				t.Fatalf("execute: %v", err)
			}
			if r.stdout.Len() != 0 {
				t.Errorf("stdout %q, want nothing of Hermod's", r.stdout.String())
			}
			for _, want := range tc.wantStderr {
				if !strings.Contains(r.stderr.String(), want) {
					t.Errorf("stderr %q, want it to hold %q", r.stderr.String(), want)
				}
			}
			if tc.wantLog == "" {
				if len(r.logs) != 0 {
					t.Errorf("opened logs %v, want none", r.logs)
				}
				return
			}
			l, ok := r.logs[tc.wantLog]
			if !ok || len(r.logs) != 1 {
				t.Fatalf("opened logs %v, want only %s", r.logs, tc.wantLog)
			}
			if !strings.Contains(l.String(), say) || !l.closed {
				t.Errorf("log %q, closed %v, want it to hold %q and be closed", l.String(), l.closed, say)
			}
		})
	}
}

// TestMutagenOutput checks where each command has the FS relays write
// Mutagen's output: the stand-in to stderr and its log, and never to stdout,
// which may be its caller's to read; attach leaves it to the core.
func TestMutagenOutput(t *testing.T) {
	const printed = "Flushed the session"
	logName := testHome + "/.cache/hermod/air/dev-api.log"
	cases := []struct {
		name       string
		argv       string
		wantStderr bool
		wantLog    bool
	}{
		{"the stand-in", "claude", true, true},
		{"attach", "hermod attach prod-box", false, false},
		{"attach's dry run", "hermod attach -n prod-box", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, r := newApp(t, setup{env: vars{envRemote: "prod-box"}, terminal: true, mutagenSays: printed})
			if err := execute(context.Background(), a, strings.Fields(tc.argv)); err != nil {
				t.Fatalf("execute: %v", err)
			}
			if r.stdout.Len() != 0 {
				t.Errorf("stdout %q, want nothing", r.stdout.String())
			}
			if got := strings.Contains(r.stderr.String(), printed); got != tc.wantStderr {
				t.Errorf("stderr %q holds the output: %v, want %v", r.stderr.String(), got, tc.wantStderr)
			}
			l := r.logs[logName]
			if got := l != nil && strings.Contains(l.String(), printed); got != tc.wantLog {
				t.Errorf("log %v holds the output: %v, want %v", r.logs, got, tc.wantLog)
			}
		})
	}
}

// TestReadRemote checks the read of the remote's home. A row with a remote
// environment runs the command readRemote sends under a real sh, so its
// quoting is checked as well as the parsing of its answer.
func TestReadRemote(t *testing.T) {
	cases := []struct {
		name      string
		out       string
		err       error
		remoteEnv []string // nil answers out
		wantHome  string
		wantErr   bool
	}{
		{"an answer", "/home/u\n", nil, nil, "/home/u", false},
		{"after a profile's own lines", "Welcome!\n/home/u\n", nil, nil, "/home/u", false},
		{"a relative home", "u\n", nil, nil, "", true},
		{"no answer", "", nil, nil, "", true},
		{"ssh fails", "", errors.New("exit status 255"), nil, "", true},
		{"the command", "", nil, []string{"SHELL=/bin/sh", "HOME=/nonexistent/u"}, "/nonexistent/u", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, r := newApp(t, setup{})
			r.local.remoteOut, r.local.remoteErr, r.local.remoteEnv = tc.out, tc.err, tc.remoteEnv
			remote, err := a.readRemote(context.Background(), "prod-box", r.local)
			if (err != nil) != tc.wantErr {
				t.Fatalf("readRemote: %v, want error %v", err, tc.wantErr)
			}
			if remote.Home != tc.wantHome {
				t.Errorf("home = %q, want %q", remote.Home, tc.wantHome)
			}
		})
	}
}

// TestAdapterLog checks where `air acp` logs an adapter's stderr: a
// timestamped log named by the adapter's key, closed with it.
func TestAdapterLog(t *testing.T) {
	cases := []struct {
		name     string
		env      vars
		key      string
		logFails bool
		wantLog  string // "" opens none
	}{
		{"by the key", vars{}, "hermod-acp-api", false, testHome + "/.cache/hermod/air/hermod-acp-api.log"},
		{"under a cache home", vars{envCacheHome: "/xdg/cache"}, "hermod-acp-api", false, "/xdg/cache/hermod/air/hermod-acp-api.log"},
		{"a log that cannot be opened", vars{}, "hermod-acp-api", true, ""},
	}
	const line = "adapter: bad request\n"
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, r := newApp(t, setup{env: tc.env, logFails: tc.logFails})
			key, err := workspace.NewKey(tc.key)
			if err != nil {
				t.Fatal(err)
			}
			w, err := a.adapterLog(testHome)(key)
			if (err != nil) != (tc.wantLog == "") {
				t.Fatalf("open: %v, want a log %q", err, tc.wantLog)
			}
			if tc.wantLog == "" {
				if len(r.logs) != 0 {
					t.Errorf("opened logs %v, want none", r.logs)
				}
				return
			}
			if _, err := io.WriteString(w, line); err != nil {
				t.Fatal(err)
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			l, ok := r.logs[tc.wantLog]
			if !ok || len(r.logs) != 1 {
				t.Fatalf("opened logs %v, want only %s", r.logs, tc.wantLog)
			}
			if got := l.String(); !strings.HasSuffix(got, " "+line) || got == line || !l.closed {
				t.Errorf("log %q, closed %v, want %q timestamped and closed", got, l.closed, line)
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
		{"the program's code passed on silently", session.ExitStatus(3), 3, ""},
		{"a wrapped program's code", fmt.Errorf("run: %w", session.ExitStatus(127)), 127, ""},
		{"Hermod's own failure", errors.New("open workspace: boom"), 1, "hermod: open workspace: boom\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stderr strings.Builder
			if got := exitCode(tc.err, &stderr); got != tc.wantCode || stderr.String() != tc.wantStderr {
				t.Errorf("exitCode = %d, %q, want %d, %q", got, stderr.String(), tc.wantCode, tc.wantStderr)
			}
		})
	}
}

func TestOpenLogIsPrivate(t *testing.T) {
	cases := []struct {
		name     string
		under    string // the log's place under a temporary directory
		wantMode os.FileMode
	}{
		{"in a directory that exists", "a.log", 0o600},
		{"in directories it makes", "hermod/air/a.log", 0o600},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name := filepath.Join(t.TempDir(), tc.under)
			f, err := openLog(name)
			if err != nil {
				t.Fatal(err)
			}
			_ = f.Close()
			info, err := os.Stat(name)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != tc.wantMode {
				t.Errorf("mode %o, want %o", got, tc.wantMode)
			}
		})
	}
}

func TestTimestamped(t *testing.T) {
	at := time.Date(2026, 11, 24, 9, 30, 0, 0, time.UTC)
	cases := []struct {
		name   string
		writes []string
		want   string
	}{
		{"one line", []string{"a\n"}, "2026/11/24 09:30:00 a\n"},
		{"a line in two writes", []string{"a", "b\n"}, "2026/11/24 09:30:00 ab\n"},
		{"two lines in one write", []string{"a\nb\n"}, "2026/11/24 09:30:00 a\n2026/11/24 09:30:00 b\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var b strings.Builder
			w := &timestamped{w: &b, now: func() time.Time { return at }}
			for _, s := range tc.writes {
				if _, err := w.Write([]byte(s)); err != nil {
					t.Fatal(err)
				}
			}
			if b.String() != tc.want {
				t.Errorf("log %q, want %q", b.String(), tc.want)
			}
		})
	}
}

func TestIsTerminal(t *testing.T) {
	// A pty's master side answers for its terminal, so it stands for one
	// without a controlling terminal; a system without ptys skips its row.
	ptmx, ptmxErr := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if ptmxErr == nil {
		defer ptmx.Close()
	}
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer devNull.Close()
	pipeR, pipeW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pipeR.Close()
	defer pipeW.Close()
	cases := []struct {
		name string
		f    *os.File
		want bool
	}{
		{"a terminal", ptmx, true},
		{"a character device that is no terminal", devNull, false},
		{"a pipe", pipeR, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.f == nil {
				t.Skipf("no pty: %v", ptmxErr)
			}
			if got := isTerminal(tc.f); got != tc.want {
				t.Errorf("isTerminal = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseSSHHosts(t *testing.T) {
	cases := []struct {
		name   string
		config string
		want   []string
	}{
		{"concrete aliases, sorted", "Host prod-box\nHost dev a\n", []string{"a", "dev", "prod-box"}},
		{"patterns left out", "Host *\nHost web-?\nHost prod\n", []string{"prod"}},
		{"case-insensitive keyword, duplicates once", "host prod\nHOST prod\n", []string{"prod"}},
		{"none", "Match all\n", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseSSHHosts(strings.NewReader(tc.config)); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("hosts %q, want %q", got, tc.want)
			}
		})
	}
}

// TestReadOnce checks that every read of a file gets its first answer, for a
// file that gives its bytes to one read only, such as a pipe, and for one that
// fails.
func TestReadOnce(t *testing.T) {
	errRead := errors.New("no such file")
	cases := []struct {
		name      string
		first     []byte // the file's bytes on its first read; later reads get none
		firstErr  error
		reads     int
		want      []byte
		wantErr   error
		wantCalls int
	}{
		{"a file read once however often it is asked for", []byte(`{"hooks":{}}`), nil, 3, []byte(`{"hooks":{}}`), nil, 1},
		{"a failed read kept", nil, errRead, 2, nil, errRead, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			read := readOnce(func(string) ([]byte, error) {
				calls++
				if calls > 1 {
					return nil, nil
				}
				return tc.first, tc.firstErr
			})
			for i := range tc.reads {
				got, err := read("/dev/fd/63")
				if !errors.Is(err, tc.wantErr) || !bytes.Equal(got, tc.want) {
					t.Errorf("read %d = %q, %v, want %q, %v", i+1, got, err, tc.want, tc.wantErr)
				}
			}
			if calls != tc.wantCalls {
				t.Errorf("read the file %d times, want %d", calls, tc.wantCalls)
			}
		})
	}
}

// TestAgentWiring checks which workstation each agent a command hands the
// core asks: the agent that answers for the remote reaches it over ssh, and
// the local one runs on this workstation.
func TestAgentWiring(t *testing.T) {
	const remote, local = "prod-box", "local"
	attachAgent := func(r *recorder) agent.Agent { return r.attached[0].Agent }
	attachLocal := func(r *recorder) agent.Agent { return r.attached[0].LocalAgent }
	chatAgent := func(r *recorder) agent.Agent { return r.chats[0].Agent }
	chatACP := func(r *recorder) agent.Agent {
		a, ok := r.chats[0].ACP.(agent.Agent)
		if !ok {
			return nil
		}
		return a
	}
	cases := []struct {
		name  string
		argv  string
		agent func(r *recorder) agent.Agent
		want  string
	}{
		{"attach's agent", "hermod attach prod-box", attachAgent, remote},
		{"attach's local agent", "hermod attach prod-box", attachLocal, local},
		{"a dry run's agent still reads", "hermod attach prod-box -n", attachAgent, remote},
		{"the stand-in's agent", "claude --version", attachAgent, remote},
		{"the stand-in's local agent", "claude --version", attachLocal, local},
		{"the chat's agent", "hermod air acp claude prod-box", chatAgent, remote},
		{"the chat's ACP", "hermod air acp claude prod-box", chatACP, remote},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, r := newApp(t, setup{env: vars{envRemote: remote}, terminal: true})
			if err := execute(context.Background(), a, strings.Fields(tc.argv)); err != nil {
				t.Fatalf("execute: %v", err)
			}
			ag := tc.agent(r)
			if ag == nil {
				t.Fatal("no agent handed in")
			}
			before := len(r.local.ran)
			_, _ = ag.StateDir(context.Background(), testCwd)
			ran := r.local.ran[before:]
			if len(ran) != 1 {
				t.Fatalf("ran %q, want one command", ran)
			}
			got := local
			if words := strings.Fields(ran[0]); len(words) > 1 && words[0] == "ssh" && slices.Contains(words, remote) {
				got = remote
			}
			if got != tc.want {
				t.Errorf("asked %s by %q, want %s", got, ran[0], tc.want)
			}
		})
	}
}

// TestHookWiring checks that attach hands the core Air's hook test.
func TestHookWiring(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want bool
	}{
		{"Air's endpoint", "http://127.0.0.1:63342/air/hooks/claude", true},
		{"another loopback endpoint", "http://127.0.0.1:8080/hooks", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, r := newApp(t, setup{})
			if err := execute(context.Background(), a, strings.Fields("hermod attach prod-box")); err != nil {
				t.Fatalf("execute: %v", err)
			}
			u, err := url.Parse(tc.url)
			if err != nil {
				t.Fatal(err)
			}
			if got := r.attached[0].IsHook(u); got != tc.want {
				t.Errorf("IsHook(%s) = %v, want %v", tc.url, got, tc.want)
			}
		})
	}
}

// TestChatRegistry checks that the chat's client connection reads stdin and
// answers on stdout with ACP's methods and Air's: Air's own messages arrive
// by what they mean, and a method neither registers never reaches the core, a
// request being answered with method not found and a notification only
// logged.
func TestChatRegistry(t *testing.T) {
	cases := []struct {
		name    string
		line    string
		want    []session.Kind
		wantOut string
		wantLog string
	}{
		{
			name: "Air's close of a tab",
			line: `{"jsonrpc":"2.0","id":1,"method":"session/close","params":{"sessionId":"s1"}}`,
			want: []session.Kind{session.Suspend},
		},
		{
			name: "Air's archive",
			line: `{"jsonrpc":"2.0","id":1,"method":"_session/archive","params":{"sessionId":"s1"}}`,
			want: []session.Kind{session.Close},
		},
		{
			name: "a standard message",
			line: `{"jsonrpc":"2.0","id":1,"method":"session/list","params":{"cwd":"/home/me/dev/api"}}`,
			want: []session.Kind{session.List},
		},
		{
			name:    "a request of an unknown method",
			line:    `{"jsonrpc":"2.0","id":1,"method":"_other/thing","params":{}}`,
			wantOut: `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"method not found: no request \"_other/thing\" from the client"}}` + "\n",
			wantLog: `acp: dropped a message from the client: method not found: no request "_other/thing" from the client` + "\n",
		},
		{
			name:    "a notification of an unknown method",
			line:    `{"jsonrpc":"2.0","method":"_other/thing","params":{}}`,
			wantLog: `acp: dropped a message from the client: method not found: no notification "_other/thing" from the client` + "\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, r := newApp(t, setup{stdin: tc.line + "\n"})
			if err := execute(context.Background(), a, strings.Fields("hermod air acp claude prod-box")); err != nil {
				t.Fatalf("execute: %v", err)
			}
			if !reflect.DeepEqual(r.received, tc.want) {
				t.Errorf("received %v, want %v", r.received, tc.want)
			}
			if got := r.stdout.String(); got != tc.wantOut {
				t.Errorf("stdout %q, want %q", got, tc.wantOut)
			}
			if got := r.stderr.String(); got != tc.wantLog {
				t.Errorf("stderr %q, want %q", got, tc.wantLog)
			}
		})
	}
}

// TestChatNeedsACP checks that the chat is served only by an agent that
// implements agent.ACP: another is refused before the core is handed a
// request.
func TestChatNeedsACP(t *testing.T) {
	cases := []struct {
		name       string
		agent      func(exec shell.Executor) agent.Agent
		wantErr    bool
		wantCalled int
	}{
		{name: "Claude", agent: nil, wantErr: false, wantCalled: 1},
		{name: "an agent without ACP", agent: func(shell.Executor) agent.Agent { return plainAgent{} }, wantErr: true, wantCalled: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, r := newApp(t, setup{agent: tc.agent})
			err := execute(context.Background(), a, strings.Fields("hermod air acp claude prod-box"))
			if (err != nil) != tc.wantErr {
				t.Errorf("execute: %v, want an error: %t", err, tc.wantErr)
			}
			if got := r.called(); got != tc.wantCalled {
				t.Errorf("handed the core %d requests, want %d", got, tc.wantCalled)
			}
		})
	}
}

// TestEntryServesChat checks that the command an `air configure` entry runs is
// the command that serves Air's chat, for the remote the entry names.
func TestEntryServesChat(t *testing.T) {
	cases := []struct {
		name       string
		argv       string
		wantRemote string
	}{
		{"the remote as an argument", "hermod air configure claude prod-box", "prod-box"},
		{"the remote from the environment", "hermod air configure claude", "dev-box"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, r := newApp(t, setup{env: vars{envRemote: "dev-box"}})
			if err := execute(context.Background(), a, strings.Fields(tc.argv)); err != nil {
				t.Fatalf("configure: %v", err)
			}
			req := r.configured[0]
			entry := append(append([]string{req.Hermod}, req.ChatArgs...), req.Remote)
			if err := execute(context.Background(), a, entry); err != nil {
				t.Fatalf("the entry's command %q: %v", entry, err)
			}
			if len(r.chats) != 1 || r.chats[0].Remote.Host != tc.wantRemote {
				t.Errorf("served %d chats, want one for %s", len(r.chats), tc.wantRemote)
			}
		})
	}
}
