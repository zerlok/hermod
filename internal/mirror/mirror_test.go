package mirror

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/zerlok/hermod/internal/shell"
)

// reply is a canned (result, error) a fake shell returns.
type reply struct {
	ret shell.Result
	err error
}

// recShell records every argv it runs, and the stdout each was given, and
// returns a canned result/error.
type recShell struct {
	ret     shell.Result
	err     error
	seen    [][]string
	stdouts []io.Writer
	stdins  []io.Reader
}

func (r *recShell) Run(_ context.Context, cmd shell.Command) (shell.Result, error) {
	r.seen = append(r.seen, cmd.Argv)
	r.stdouts = append(r.stdouts, cmd.Stdout)
	r.stdins = append(r.stdins, cmd.Stdin)
	return r.ret, r.err
}

// probeShell answers each probe by its joined argv, so the lookup of one name
// and the listing of every session can be given different replies. An argv
// with no reply fails, which a test reads as a probe that should not have run.
type probeShell struct {
	replies map[string]reply
	seen    [][]string
}

func (p *probeShell) Run(_ context.Context, cmd shell.Command) (shell.Result, error) {
	p.seen = append(p.seen, cmd.Argv)
	r, ok := p.replies[strings.Join(cmd.Argv, " ")]
	if !ok {
		return shell.Result{}, errors.New("unexpected probe")
	}
	return r.ret, r.err
}

func testConfig() Config {
	return Config{Host: "prod-box", RemotePath: "dev/api", LocalPath: "/home/u/api"}
}

const (
	testName       = "hermod-prod-box-dev-api"
	lookupArgv     = "mutagen sync list " + testName
	listAllArgv    = "mutagen sync list"
	separatorLine  = "--------------------------------------------------------------------------------\n"
	runningSession = separatorLine +
		"Name: " + testName + "\nIdentifier: sync_1\nLabels: None\n" +
		"Alpha:\n\tURL: /home/u/api\n\tConnected: Yes\n" +
		"Beta:\n\tURL: prod-box:dev/api\n\tConnected: Yes\n" +
		"Status: Watching for changes\n" + separatorLine
	pausedSession = separatorLine +
		"Name: " + testName + "\nIdentifier: sync_1\n" +
		"Alpha:\n\tURL: /home/u/api\n\tConnected: No\n" +
		"Beta:\n\tURL: prod-box:dev/api\n\tConnected: No\n" +
		"Status: [Paused]\n" + separatorLine
	otherEndpointSession = separatorLine +
		"Name: " + testName + "\n" +
		"Alpha:\n\tURL: /opt/dev/api\n" +
		"Beta:\n\tURL: prod-box:opt-dev-api\n" +
		"Status: Watching for changes\n" + separatorLine
	allSessions = separatorLine +
		"Name: dev-api\nAlpha:\n\tURL: /home/u/api\nBeta:\n\tURL: prod-box:dev/api\nStatus: [Paused]\n" + separatorLine +
		"Name: my-session\nAlpha:\n\tURL: /home/u/api\nBeta:\n\tURL: other-box:dev/api\nStatus: Watching for changes\n" + separatorLine +
		"Name: hermod-other-box-dev-api\nAlpha:\n\tURL: /home/u/api\nBeta:\n\tURL: other-box:dev/api\nStatus: Watching for changes\n" + separatorLine +
		"Name: web\nAlpha:\n\tURL: /home/u/web\nBeta:\n\tURL: prod-box:web\nStatus: Watching for changes\n" + separatorLine
)

// notFound is the probe reply for a session Mutagen cannot locate: it carries
// the not-found marker on stderr and the non-zero exit that accompanies it.
var notFound = reply{ret: shell.Result{Stderr: notFoundMarker}, err: errors.New(notFoundMarker)}

func TestName(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want string
	}{
		{"nested home-relative path", Config{Host: "prod-box", RemotePath: "dev/api"}, "hermod-prod-box-dev-api"},
		{"slugified path outside home", Config{Host: "prod", RemotePath: "opt-work-api"}, "hermod-prod-opt-work-api"},
		{"alias dots and underscores become dashes", Config{Host: "box.example_1", RemotePath: "api"}, "hermod-box-example-1-api"},
		{"path characters Mutagen rejects become dashes", Config{Host: "prod", RemotePath: "dev/my_proj.v2"}, "hermod-prod-dev-my-proj-v2"},
		{"same path on two sandboxes differs", Config{Host: "box-b", RemotePath: "dev/api"}, "hermod-box-b-dev-api"},
		{"transcripts named after the session directory and the local end", Config{Kind: Transcripts, Host: "prod-box", RemotePath: "/home/u/.claude/projects/-home-u-dev-api", LocalPath: "/home/me/.claude/projects/-home-me-dev-api", SessionDir: "dev/api"}, "hermod-prod-box-dev-api-transcripts-c0c6a709"},
		{"transcripts of one directory to another local end differ", Config{Kind: Transcripts, Host: "prod-box", RemotePath: "/home/u/.claude/projects/-home-u-dev-api", LocalPath: "/home/me/.local/share/hermod/air/box/claude/projects/-home-me-dev-api", SessionDir: "dev/api"}, "hermod-prod-box-dev-api-transcripts-611526fd"},
		{"transcripts of a directory outside home", Config{Kind: Transcripts, Host: "box.a", RemotePath: "/x", LocalPath: "/x", SessionDir: "opt-work-api"}, "hermod-box-a-opt-work-api-transcripts-b3d1db31"},
		{"a project ending in -transcripts is not a directory's transcripts", Config{Kind: Project, Host: "box", RemotePath: "dev/api-transcripts"}, "hermod-box-dev-api-transcripts"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Name(tc.cfg); got != tc.want {
				t.Errorf("Name(%+v) = %q, want %q", tc.cfg, got, tc.want)
			}
		})
	}
}

func TestNewMutagenSessionOpens(t *testing.T) {
	cases := []struct {
		name         string
		probes       map[string]reply
		wantErr      bool
		wantMismatch *EndpointMismatchError
		wantExec     [][]string
		wantEarlier  []string
	}{
		{
			name:   "absent provisions root then creates two-way-safe",
			probes: map[string]reply{lookupArgv: notFound, listAllArgv: {}},
			wantExec: [][]string{
				{"ssh", "prod-box", "mkdir -p -- dev/api"},
				{"mutagen", "sync", "create", "--name", testName, "--label", "hermod-kind=project", "--label", "hermod-sandbox=prod-box", "--mode", "two-way-safe", "/home/u/api", "prod-box:dev/api"},
			},
		},
		{
			name:   "absent lists mirrors under earlier names and leaves them",
			probes: map[string]reply{lookupArgv: notFound, listAllArgv: {ret: shell.Result{Stdout: allSessions}}},
			wantExec: [][]string{
				{"ssh", "prod-box", "mkdir -p -- dev/api"},
				{"mutagen", "sync", "create", "--name", testName, "--label", "hermod-kind=project", "--label", "hermod-sandbox=prod-box", "--mode", "two-way-safe", "/home/u/api", "prod-box:dev/api"},
			},
			wantEarlier: []string{"dev-api", "my-session"},
		},
		{
			name:   "absent still creates when the earlier-name listing fails",
			probes: map[string]reply{lookupArgv: notFound, listAllArgv: {err: errors.New("daemon unavailable")}},
			wantExec: [][]string{
				{"ssh", "prod-box", "mkdir -p -- dev/api"},
				{"mutagen", "sync", "create", "--name", testName, "--label", "hermod-kind=project", "--label", "hermod-sandbox=prod-box", "--mode", "two-way-safe", "/home/u/api", "prod-box:dev/api"},
			},
		},
		{
			name:     "paused with the expected endpoint resumes without provisioning",
			probes:   map[string]reply{lookupArgv: {ret: shell.Result{Stdout: pausedSession}}},
			wantExec: [][]string{{"mutagen", "sync", "resume", testName}},
		},
		{
			name:     "running with the expected endpoint resumes without provisioning",
			probes:   map[string]reply{lookupArgv: {ret: shell.Result{Stdout: runningSession}}},
			wantExec: [][]string{{"mutagen", "sync", "resume", testName}},
		},
		{
			name:         "another endpoint is neither resumed nor replaced",
			probes:       map[string]reply{lookupArgv: {ret: shell.Result{Stdout: otherEndpointSession}}},
			wantErr:      true,
			wantMismatch: &EndpointMismatchError{Name: testName, Want: "prod-box:dev/api", Got: "prod-box:opt-dev-api"},
		},
		{
			name:    "listing without the session aborts open without create",
			probes:  map[string]reply{lookupArgv: {ret: shell.Result{Stdout: "unexpected output\n"}}},
			wantErr: true,
		},
		{
			name:    "ambiguous probe aborts open without create",
			probes:  map[string]reply{lookupArgv: {err: errors.New("mutagen daemon unavailable")}},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec := &recShell{}
			probe := &probeShell{replies: tc.probes}
			_, earlier, err := NewMutagenSession(context.Background(), exec, probe, testConfig())
			if (err != nil) != tc.wantErr {
				t.Fatalf("NewMutagenSession() error = %v, wantErr %v", err, tc.wantErr)
			}
			var mismatch *EndpointMismatchError
			if errors.As(err, &mismatch) != (tc.wantMismatch != nil) || (mismatch != nil && *mismatch != *tc.wantMismatch) {
				t.Errorf("mismatch error = %+v, want %+v", mismatch, tc.wantMismatch)
			}
			if !reflect.DeepEqual(exec.seen, tc.wantExec) {
				t.Errorf("open side effects = %v, want %v", exec.seen, tc.wantExec)
			}
			if !reflect.DeepEqual(earlier, tc.wantEarlier) {
				t.Errorf("earlier = %v, want %v", earlier, tc.wantEarlier)
			}
		})
	}
}

func TestNewMutagenSessionOpensTranscripts(t *testing.T) {
	const (
		name     = "hermod-prod-box-dev-api-transcripts-c0c6a709"
		remote   = "/home/u/.claude/projects/-home-u-dev-api"
		local    = "/home/me/.claude/projects/-home-me-dev-api"
		endpoint = "prod-box:" + remote
	)
	cfg := Config{Kind: Transcripts, Host: "prod-box", RemotePath: remote, LocalPath: local, SessionDir: "dev/api"}
	lookup := "mutagen sync list " + name
	session := func(alpha string) string {
		return separatorLine + "Name: " + name + "\nLabels:\n\thermod-kind: transcripts\n\thermod-sandbox: prod-box\n" +
			"Alpha:\n\tURL: " + alpha + "\nBeta:\n\tURL: " + local + "\nStatus: [Paused]\n" + separatorLine
	}
	cases := []struct {
		name         string
		probes       map[string]reply
		wantExec     [][]string
		wantMismatch *EndpointMismatchError
	}{
		{
			name:   "absent makes both ends, then creates one-way-safe from the sandbox, listing nothing else",
			probes: map[string]reply{lookup: notFound},
			wantExec: [][]string{
				{"ssh", "prod-box", "mkdir -p -- " + remote},
				{"mkdir", "-p", local},
				{"mutagen", "sync", "create", "--name", name, "--label", "hermod-kind=transcripts", "--label", "hermod-sandbox=prod-box", "--mode", "one-way-safe", endpoint, local},
			},
		},
		{
			name:     "paused from the same sandbox directory resumes",
			probes:   map[string]reply{lookup: {ret: shell.Result{Stdout: session(endpoint)}}},
			wantExec: [][]string{{"mutagen", "sync", "resume", name}},
		},
		{
			name:         "from another sandbox directory is neither resumed nor replaced",
			probes:       map[string]reply{lookup: {ret: shell.Result{Stdout: session("prod-box:/home/v/.claude/projects/-home-v-dev-api")}}},
			wantMismatch: &EndpointMismatchError{Name: name, Want: endpoint, Got: "prod-box:/home/v/.claude/projects/-home-v-dev-api"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec := &recShell{}
			probe := &probeShell{replies: tc.probes}
			_, earlier, err := NewMutagenSession(context.Background(), exec, probe, cfg)
			var mismatch *EndpointMismatchError
			if errors.As(err, &mismatch) != (tc.wantMismatch != nil) || (mismatch != nil && *mismatch != *tc.wantMismatch) {
				t.Fatalf("err = %v, want mismatch %+v", err, tc.wantMismatch)
			}
			if tc.wantMismatch == nil && err != nil {
				t.Fatalf("NewMutagenSession() error: %v", err)
			}
			if !reflect.DeepEqual(exec.seen, tc.wantExec) {
				t.Errorf("open side effects = %v, want %v", exec.seen, tc.wantExec)
			}
			if earlier != nil {
				t.Errorf("earlier = %v, want none for transcripts", earlier)
			}
		})
	}
}

func TestSelector(t *testing.T) {
	cases := []struct {
		name string
		host string
		want string
	}{
		{"plain alias", "prod-box", "hermod-sandbox=prod-box"},
		{"characters a label rejects become dashes", "box.example_1", "hermod-sandbox=box-example-1"},
		{"no leading or trailing dash", "_box.", "hermod-sandbox=box"},
		{"cut to Mutagen's length", strings.Repeat("a", 62) + ".b", "hermod-sandbox=" + strings.Repeat("a", 62)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Selector(tc.host); got != tc.want {
				t.Errorf("Selector(%q) = %q, want %q", tc.host, got, tc.want)
			}
		})
	}
}

// TestCommandsWriteToOutput asserts that every mutating command, on open and
// through the lifecycle, prints to the configured Output and to nothing else,
// and reads no input.
func TestCommandsWriteToOutput(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name   string
		probes map[string]reply
		then   func(Session) error // after a successful open; nil for none
		want   int                 // mutating commands run
	}{
		{"create", map[string]reply{lookupArgv: notFound, listAllArgv: {}}, nil, 2},
		{"resume", map[string]reply{lookupArgv: {ret: shell.Result{Stdout: runningSession}}}, nil, 1},
		{"flush", map[string]reply{lookupArgv: {ret: shell.Result{Stdout: runningSession}}}, func(s Session) error { return s.Flush(ctx) }, 2},
		{"pause", map[string]reply{lookupArgv: {ret: shell.Result{Stdout: runningSession}}}, func(s Session) error { return s.Pause(ctx) }, 2},
		{"terminate", map[string]reply{lookupArgv: {ret: shell.Result{Stdout: runningSession}}}, func(s Session) error { return s.Close(ctx) }, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec := &recShell{}
			out := &bytes.Buffer{}
			cfg := testConfig()
			cfg.Output = out
			s, _, err := NewMutagenSession(ctx, exec, &probeShell{replies: tc.probes}, cfg)
			if err != nil {
				t.Fatalf("NewMutagenSession() error: %v", err)
			}
			if tc.then != nil {
				if err := tc.then(s); err != nil {
					t.Fatalf("call error: %v", err)
				}
			}
			if len(exec.stdouts) != tc.want {
				t.Fatalf("commands = %v, want %d", exec.seen, tc.want)
			}
			for i, w := range exec.stdouts {
				if w != out {
					t.Errorf("%v writes to %v, not the configured output", exec.seen[i], w)
				}
				if exec.stdins[i] != shell.NoInput {
					t.Errorf("%v reads %v, not empty input", exec.seen[i], exec.stdins[i])
				}
			}
		})
	}
}

func TestLifecycleArgv(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		call func(Session) error
		want []string
	}{
		{"flush", func(s Session) error { return s.Flush(ctx) }, []string{"mutagen", "sync", "flush", testName}},
		{"pause", func(s Session) error { return s.Pause(ctx) }, []string{"mutagen", "sync", "pause", testName}},
		{"close terminates", func(s Session) error { return s.Close(ctx) }, []string{"mutagen", "sync", "terminate", testName}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec := &recShell{}
			// A running session resumes on open, leaving one prior side effect.
			probe := &probeShell{replies: map[string]reply{lookupArgv: {ret: shell.Result{Stdout: runningSession}}}}
			s, _, err := NewMutagenSession(ctx, exec, probe, testConfig())
			if err != nil {
				t.Fatalf("NewMutagenSession() error: %v", err)
			}
			opened := len(exec.seen)
			if err := tc.call(s); err != nil {
				t.Fatalf("call error: %v", err)
			}
			if len(exec.seen) != opened+1 || !reflect.DeepEqual(exec.seen[opened], tc.want) {
				t.Errorf("argv = %v, want a single %v after open", exec.seen[opened:], tc.want)
			}
		})
	}
}

func TestStatusIsReadOnly(t *testing.T) {
	cases := []struct {
		name    string
		probe   reply
		want    State
		wantErr bool
	}{
		{"not-found marker reads absent", notFound, Absent, false},
		{"paused", reply{ret: shell.Result{Stdout: pausedSession}}, Paused, false},
		{"running", reply{ret: shell.Result{Stdout: runningSession}}, Running, false},
		// An error that is not the not-found marker is Unknown, never Absent, so it
		// can't be mistaken for "no session" and trigger a create over an existing one.
		{"ambiguous error is unknown, not absent", reply{err: errors.New("mutagen daemon unavailable")}, Unknown, true},
		{"unparsed output is unknown, not absent", reply{ret: shell.Result{Stdout: "Status: Paused\n"}}, Unknown, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec := &recShell{}
			probe := &probeShell{replies: map[string]reply{lookupArgv: tc.probe}}
			// Query Status directly on the concrete type to avoid the open side
			// effects a constructed Session would issue first.
			m := &mutagen{name: testName, probe: probe, exec: exec}
			got, err := m.Status(context.Background())
			if (err != nil) != tc.wantErr {
				t.Fatalf("Status() error = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("State = %v, want %v", got, tc.want)
			}
			if len(exec.seen) != 0 {
				t.Errorf("status mutated state via %v", exec.seen)
			}
		})
	}
}

func TestParseList(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want []listed
	}{
		{"empty output", "", nil},
		{"running session", runningSession, []listed{{name: testName, alpha: "/home/u/api", beta: "prod-box:dev/api"}}},
		{"paused session", pausedSession, []listed{{name: testName, alpha: "/home/u/api", beta: "prod-box:dev/api", paused: true}}},
		{"several sessions", allSessions, []listed{
			{name: "dev-api", alpha: "/home/u/api", beta: "prod-box:dev/api", paused: true},
			{name: "my-session", alpha: "/home/u/api", beta: "other-box:dev/api"},
			{name: "hermod-other-box-dev-api", alpha: "/home/u/api", beta: "other-box:dev/api"},
			{name: "web", alpha: "/home/u/web", beta: "prod-box:web"},
		}},
		{"labels read", separatorLine +
			"Name: x\nLabels:\n\thermod-kind: project\n\thermod-sandbox: box\nAlpha:\n\tURL: /l\nBeta:\n\tURL: box:r\nStatus: Watching for changes\n",
			[]listed{{name: "x", alpha: "/l", beta: "box:r", labels: map[string]string{"hermod-kind": "project", "hermod-sandbox": "box"}}}},
		{"no labels", separatorLine +
			"Name: x\nLabels: None\nAlpha:\n\tURL: /l\nBeta:\n\tURL: box:r\nStatus: Watching for changes\n",
			[]listed{{name: "x", alpha: "/l", beta: "box:r"}}},
		{"a label naming Paused is not a paused status", separatorLine +
			"Name: x\nLabels:\n\tnote: Paused\nAlpha:\n\tURL: /l\nBeta:\n\tURL: box:r\nStatus: Watching for changes\n",
			[]listed{{name: "x", alpha: "/l", beta: "box:r", labels: map[string]string{"note": "Paused"}}}},
		{"a path naming Paused is not a paused status", separatorLine +
			"Name: x\nAlpha:\n\tURL: /home/u/Paused\nBeta:\n\tURL: box:Paused\nStatus: Watching for changes\n",
			[]listed{{name: "x", alpha: "/home/u/Paused", beta: "box:Paused"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseList(tc.out); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseList() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestCreateQuotesTheSandboxRoot asserts the sandbox root a new mirror makes
// reaches the sandbox's shell as one quoted word, however its path reads.
func TestCreateQuotesTheSandboxRoot(t *testing.T) {
	cases := []struct {
		name      string
		remote    string
		wantMkdir []string
	}{
		{"a space", "dev/my project", []string{"ssh", "prod-box", "mkdir -p -- 'dev/my project'"}},
		{"a command substitution", "dev/p$(id>S)", []string{"ssh", "prod-box", "mkdir -p -- 'dev/p$(id>S)'"}},
		{"a leading dash", "-x", []string{"ssh", "prod-box", "mkdir -p -- -x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{Kind: Project, Host: "prod-box", RemotePath: tc.remote, LocalPath: "/home/u/api"}
			exec := &recShell{}
			probe := &probeShell{replies: map[string]reply{"mutagen sync list " + Name(cfg): notFound, "mutagen sync list": {}}}
			if _, _, err := NewMutagenSession(context.Background(), exec, probe, cfg); err != nil {
				t.Fatalf("NewMutagenSession() error: %v", err)
			}
			if len(exec.seen) == 0 || !reflect.DeepEqual(exec.seen[0], tc.wantMkdir) {
				t.Errorf("first command = %q, want %q", exec.seen, tc.wantMkdir)
			}
		})
	}
}
