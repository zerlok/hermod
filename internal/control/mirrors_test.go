package control

import (
	"context"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/zerlok/hermod/internal/claude"
	"github.com/zerlok/hermod/internal/mirror"
	"github.com/zerlok/hermod/internal/sandbox"
	"github.com/zerlok/hermod/internal/shell"
)

// listedRoot is a `mutagen sync list` entry of a project mirror of the
// sandbox "prod".
func listedRoot(name, local, remote string) string {
	return "--------\nName: " + name + "\nLabels:\n\thermod-kind: project\n\thermod-sandbox: prod\n" +
		"Alpha:\n\tURL: " + local + "\nBeta:\n\tURL: prod:" + remote + "\nStatus: [Paused]\n"
}

// TestRunAgentPlacesItsDirectory asserts an agent session reuses the mirror
// whose root holds its directory, running at the corresponding sandbox path,
// and that a directory outside every root gets a mirror of its own.
func TestRunAgentPlacesItsDirectory(t *testing.T) {
	cases := []struct {
		name       string
		listing    string // the sandbox's mirrors
		found      string // the listed mirror the run finds and resumes; "" when it creates one
		wantMirror []string
		wantDir    string // the session's directory on the sandbox
		wantSteps  []string
	}{
		{
			"inside a mirrored root",
			listedRoot("hermod-prod-dev", "/home/me/dev", "dev"),
			"hermod-prod-dev",
			[]string{"mutagen", "sync", "resume", "hermod-prod-dev"},
			"dev/api",
			[]string{"mirror resume", "mirror flush", "transcripts mkdir", "transcripts local mkdir", "transcripts create", "prepare", "write files", "start", "attach", "liveness", "mirror pause", "transcripts pause"},
		},
		{
			"inside a root mirrored outside home",
			listedRoot("hermod-prod-opt-work", "/home/me", "opt-work"),
			"hermod-prod-opt-work",
			[]string{"mutagen", "sync", "resume", "hermod-prod-opt-work"},
			"opt-work/dev/api",
			[]string{"mirror resume", "mirror flush", "transcripts mkdir", "transcripts local mkdir", "transcripts create", "prepare", "write files", "start", "attach", "liveness", "mirror pause", "transcripts pause"},
		},
		{
			"a worktree beside a mirrored root",
			listedRoot("hermod-prod-dev-api-wt", "/home/me/dev/api-wt", "dev/api-wt"),
			"",
			[]string{"mutagen", "sync", "create", "--name", "hermod-prod-dev-api", "--label", "hermod-kind=project", "--label", "hermod-sandbox=prod", "--mode", "two-way-safe", projectDir, "prod:dev/api"},
			"dev/api",
			[]string{"mirror mkdir", "mirror create", "mirror flush", "transcripts mkdir", "transcripts local mkdir", "transcripts create", "prepare", "write files", "start", "attach", "liveness", "mirror pause", "transcripts pause"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			argv := []string{"claude", "--session-id", sessionUUID}
			effective := &scripted{replies: []reply{{match: "hookport", ret: shell.Result{Stdout: hookPort + "\n"}}}}
			real := realProbes(probeOut(false, false, notListed), nil)
			replies := []reply{
				{match: "--label-selector hermod-sandbox=prod", ret: shell.Result{Stdout: tc.listing}},
				// Matched before a found project mirror, whose name it extends.
				{match: "-transcripts", ret: shell.Result{Stderr: "unable to locate requested sessions"}, err: exitErr{code: 1}},
			}
			if tc.found != "" {
				replies = append(replies, reply{match: "mutagen sync list " + tc.found, ret: shell.Result{Stdout: tc.listing}})
			}
			real.replies = append(replies, real.replies...)
			if err := runAgent(context.Background(), agentOptions(argv, t.TempDir(), io.Discard), effective, real, claude.Classify(argv), fixedRandom()); err != nil {
				t.Fatalf("runAgent() error: %v", err)
			}
			var steps []string
			for _, cmd := range effective.cmds {
				steps = append(steps, step(cmd))
			}
			if !reflect.DeepEqual(steps, tc.wantSteps) {
				t.Errorf("steps =\n %q\nwant\n %q", steps, tc.wantSteps)
			}
			if !containsArgv(effective.cmds, tc.wantMirror) {
				t.Errorf("no %q among %v", tc.wantMirror, effective.cmds)
			}
			start := startLine(effective)
			if !strings.Contains(start, "-c "+tc.wantDir+" ") || !strings.Contains(start, "@hermod-dir "+tc.wantDir) {
				t.Errorf("start %q does not run in and tag %s", start, tc.wantDir)
			}
		})
	}
}

// TestAttachPlacesItsDirectory asserts a plain attach places its directory as
// an agent session does, and settles by its root's liveness.
func TestAttachPlacesItsDirectory(t *testing.T) {
	cases := []struct {
		name       string
		listing    string
		found      string // the listed mirror the run finds and resumes; "" when it creates one
		liveness   reply
		wantAttach string // a substring of the attach
		wantSettle []string
	}{
		{
			"inside a mirrored root, a session left under it",
			listedRoot("hermod-prod-dev", "/home/me/dev", "dev"),
			"hermod-prod-dev",
			livenessReply("dev/web"),
			"-c dev/api ",
			[]string{"mutagen sync pause hermod-prod-dev"},
		},
		{
			"nothing mirrored, nothing left",
			"",
			"",
			livenessReply(),
			"-c dev/api ",
			[]string{"mutagen sync flush hermod-prod-dev-api", "mutagen sync terminate hermod-prod-dev-api"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			effective := &scripted{replies: []reply{tc.liveness}}
			real := realProbes("", nil)
			replies := []reply{{match: "--label-selector", ret: shell.Result{Stdout: tc.listing}}}
			if tc.found != "" {
				replies = append(replies, reply{match: "mutagen sync list " + tc.found, ret: shell.Result{Stdout: tc.listing}})
			}
			real.replies = append(replies, real.replies...)
			o := agentOptions([]string{"htop"}, t.TempDir(), io.Discard)
			if err := attach(context.Background(), o, effective, real); err != nil {
				t.Fatalf("attach() error: %v", err)
			}
			var attachLine string
			var settled []string
			attached := false
			for _, cmd := range effective.cmds {
				line := strings.Join(cmd.Argv, " ")
				switch {
				case strings.Contains(line, "new-session -A"):
					attachLine, attached = line, true
				case attached && !strings.Contains(line, "list-sessions"):
					settled = append(settled, line)
				}
			}
			if !strings.Contains(attachLine, tc.wantAttach) {
				t.Errorf("attach %q lacks %q", attachLine, tc.wantAttach)
			}
			if !reflect.DeepEqual(settled, tc.wantSettle) {
				t.Errorf("settled %q, want %q", settled, tc.wantSettle)
			}
		})
	}
}

// TestPlaceWarnsOfRootsSyncedTwice asserts a run that makes a new root over
// mirrored roots says so, even under -q.
func TestPlaceWarnsOfRootsSyncedTwice(t *testing.T) {
	cases := []struct {
		name       string
		listing    string
		localDir   string
		remoteDir  string
		wantStderr string
	}{
		{
			"a parent of a mirrored root",
			listedRoot("hermod-prod-dev-api", "/home/me/dev/api", "dev/api"),
			"/home/me/dev", "dev",
			"hermod: /home/me/dev/api is already mirrored on its own and will also be synced through /home/me/dev\n",
		},
		{
			"inside a mirrored root",
			listedRoot("hermod-prod-dev", "/home/me/dev", "dev"),
			"/home/me/dev/api", "dev/api",
			"",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stderr strings.Builder
			real := &scripted{replies: []reply{{match: "--label-selector", ret: shell.Result{Stdout: tc.listing}}}}
			o := Options{Sandbox: "prod", LocalDir: tc.localDir, RemoteDir: tc.remoteDir, Quiet: true, Stdio: sandbox.Streams{Err: &stderr}}
			if _, err := place(context.Background(), o, real); err != nil {
				t.Fatalf("place() error: %v", err)
			}
			if stderr.String() != tc.wantStderr {
				t.Errorf("stderr = %q, want %q", stderr.String(), tc.wantStderr)
			}
		})
	}
}

// sharedConfigDir is a local Claude config directory with no transcript in it,
// whose transcript mirror's name is fixed.
const sharedConfigDir = "/nonexistent/claude"

// TestRunAgentSharesItsMirrors asserts a run leaves a mirror another process
// holds a lease on as it is, and settles the ones it holds alone.
func TestRunAgentSharesItsMirrors(t *testing.T) {
	cases := []struct {
		name        string
		otherHolds  []string // mirrors another process holds a lease on
		wantSettled []string
		ownLeases   []string // the leases only the run held, which it must give up
	}{
		{"alone", nil, []string{"mirror flush", "mirror close", "transcripts flush", "transcripts close"}, []string{"hermod-prod-dev-api", "hermod-prod-dev-api-transcripts-258b76de"}},
		{"the project mirror shared", []string{"hermod-prod-dev-api"}, []string{"transcripts flush", "transcripts close"}, []string{"hermod-prod-dev-api-transcripts-258b76de"}},
		{"both shared", []string{"hermod-prod-dev-api", "hermod-prod-dev-api-transcripts-258b76de"}, nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			leaseDir := filepath.Join(t.TempDir(), "lease")
			for _, name := range tc.otherHolds {
				l, err := mirror.TakeLease(context.Background(), leaseDir, name)
				if err != nil {
					t.Fatal(err)
				}
				defer l.Close()
			}
			argv := []string{"claude", "--session-id", sessionUUID}
			effective := &scripted{replies: []reply{{match: "hookport", ret: shell.Result{Stdout: hookPort + "\n"}}, livenessReply()}}
			o := agentOptions(argv, sharedConfigDir, io.Discard)
			o.LeaseDir = leaseDir
			if err := runAgent(context.Background(), o, effective, realProbes(probeOut(false, false, notListed), nil), claude.Classify(argv), fixedRandom()); err != nil {
				t.Fatalf("runAgent() error: %v", err)
			}
			if got := settledSteps(effective); !reflect.DeepEqual(got, tc.wantSettled) {
				t.Errorf("settled %q, want %q", got, tc.wantSettled)
			}
			// Every lease is given up: a holder that comes next is the last.
			for _, name := range tc.ownLeases {
				l, err := mirror.TakeLease(context.Background(), leaseDir, name)
				if err != nil {
					t.Fatalf("lease %s: %v", name, err)
				}
				if last, err := l.Release(); err != nil || !last {
					t.Errorf("lease %s is still held by the run", name)
				}
				_ = l.Close()
			}
		})
	}
}

func TestRunRoots(t *testing.T) {
	cases := []struct {
		name      string
		placement mirror.Placement
		home      string
		paths     map[string]string // a local path → the sandbox path it maps to
	}{
		{
			"home-relative and absolute roots",
			mirror.Placement{Roots: []mirror.Root{{Local: "/home/me/dev/api", Remote: "dev/api"}, {Local: "/opt/work", Remote: "/srv/work"}}},
			"/home/u",
			map[string]string{"/home/me/dev/api/main.go": "/home/u/dev/api/main.go", "/opt/work/x": "/srv/work/x", "/home/me/dev/web": "/home/me/dev/web"},
		},
		{
			"a pair sharing the run's sandbox root is left out",
			mirror.Placement{Roots: []mirror.Root{{Local: "/home/me/dev/api", Remote: "dev/api"}, {Local: "/home/me/old/api", Remote: "/home/u/dev/api"}}},
			"/home/u",
			map[string]string{"/home/me/dev/api/a": "/home/u/dev/api/a", "/home/me/old/api/a": "/home/me/old/api/a"},
		},
		{
			"a pair sharing the run's local root is left out",
			mirror.Placement{Roots: []mirror.Root{{Local: "/home/me/dev/api", Remote: "dev/api"}, {Local: "/home/me/dev/api/", Remote: "elsewhere"}}},
			"/home/u",
			map[string]string{"/home/me/dev/api/a": "/home/u/dev/api/a"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			roots, err := runRoots(tc.placement, tc.home)
			if err != nil {
				t.Fatalf("runRoots() error: %v", err)
			}
			for local, want := range tc.paths {
				if got := roots.ToSandbox().Path(local); got != want {
					t.Errorf("%s maps to %s, want %s", local, got, want)
				}
			}
		})
	}
}

func TestTranscriptMirror(t *testing.T) {
	cases := []struct {
		name             string
		configDir        string
		localDir         string
		sessionDir       string
		sandboxConfigDir string
		sandboxDir       string
		want             mirror.Config
	}{
		{
			"Air's per-sandbox config directory",
			"/home/me/.local/share/hermod/air/box/claude", "/home/me/dev/api", "dev/api", "/home/u/.claude", "/home/u/dev/api",
			mirror.Config{Kind: mirror.Transcripts, Host: "prod", RemotePath: "/home/u/.claude/projects/-home-u-dev-api", LocalPath: "/home/me/.local/share/hermod/air/box/claude/projects/-home-me-dev-api", SessionDir: "dev/api"},
		},
		{
			"the default config directory, a directory outside home",
			"/home/me/.claude", "/opt/work/my_app.v2", "opt-work-my-app-v2", "/home/u/.claude", "/home/u/opt-work-my_app.v2",
			mirror.Config{Kind: mirror.Transcripts, Host: "prod", RemotePath: "/home/u/.claude/projects/-home-u-opt-work-my-app-v2", LocalPath: "/home/me/.claude/projects/-opt-work-my-app-v2", SessionDir: "opt-work-my-app-v2"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := Options{Sandbox: "prod", LocalDir: tc.localDir, ClaudeConfigDir: tc.configDir}
			if got := transcriptMirror(o, tc.sessionDir, tc.sandboxConfigDir, tc.sandboxDir); got != tc.want {
				t.Errorf("transcriptMirror() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestRunAgentWithoutAConfigDirectory asserts a run with no local Claude config
// directory mirrors no transcripts and still runs the session.
func TestRunAgentWithoutAConfigDirectory(t *testing.T) {
	cases := []struct {
		name      string
		wantSteps []string
	}{
		{"no transcript mirror", []string{"mirror mkdir", "mirror create", "mirror flush", "prepare", "write files", "start", "attach", "liveness", "mirror pause"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			argv := []string{"claude", "--session-id", sessionUUID}
			effective := &scripted{replies: []reply{{match: "hookport", ret: shell.Result{Stdout: hookPort + "\n"}}, livenessReply(sandboxDir)}}
			o := agentOptions(argv, "", io.Discard)
			if err := runAgent(context.Background(), o, effective, realProbes(probeOut(false, false, notListed), nil), claude.Classify(argv), fixedRandom()); err != nil {
				t.Fatalf("runAgent() error: %v", err)
			}
			var steps []string
			for _, cmd := range effective.cmds {
				steps = append(steps, step(cmd))
			}
			if !reflect.DeepEqual(steps, tc.wantSteps) {
				t.Errorf("steps =\n %q\nwant\n %q", steps, tc.wantSteps)
			}
		})
	}
}

func containsArgv(cmds []shell.Command, want []string) bool {
	for _, cmd := range cmds {
		if reflect.DeepEqual(cmd.Argv, want) {
			return true
		}
	}
	return false
}
