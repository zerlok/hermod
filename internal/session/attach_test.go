package session

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"reflect"
	"strings"
	"testing"

	"github.com/zerlok/hermod/internal/agent"
	"github.com/zerlok/hermod/internal/env"
	"github.com/zerlok/hermod/internal/pathmap"
	"github.com/zerlok/hermod/internal/relay"
	"github.com/zerlok/hermod/internal/secret"
	"github.com/zerlok/hermod/internal/worker"
)

const (
	api    = testLocalHome + "/api"
	apiR   = testRemoteHome + "/api"
	convID = "c0ffee"
	convW  = "hermod-" + convID
)

// transcripts is the FS relay a conversation's agent requires.
var transcripts = pathmap.Place{Local: testLocalHome + "/.claude/projects/-home-me-api", Remote: testRemoteHome + "/.claude/projects/-home-u-api"}

func conversation(t *testing.T, needs agent.Needs) fakeAgent {
	t.Helper()
	call, err := agent.NewConversation(convID, []string{"claude", "--resume", convID})
	if err != nil {
		t.Fatal(err)
	}
	return fakeAgent{call: call, needs: needs}
}

// blocking is a client's input that never ends while the session runs.
func blocking(t *testing.T) io.Reader {
	r, w := io.Pipe()
	t.Cleanup(func() { _ = w.Close() })
	return r
}

// TestAttachDecides runs every row of the decision table, and a worker that
// runs no agent, and checks what each one changes on the remote, in order: a
// start adds the FS relays the agent requires before it writes the worker's
// directory, and starts the worker last.
func TestAttachDecides(t *testing.T) {
	cases := []struct {
		name        string
		probe       string // what the agent program says; "" runs a command no agent runs
		worker      worker.State
		wantErr     error
		wantChanges []string
	}{
		{"running conversation, running worker: attach", "running", worker.Running, nil, []string{
			"create " + api, "flush " + api,
			"pause " + api,
		}},
		{"running conversation, no worker: refuse", "running", worker.Absent, ErrRunsOutside, []string{
			"create " + api, "flush " + api,
			"flush " + api, "terminate " + api,
		}},
		{"running conversation, exited worker: refuse", "running", worker.Exited, ErrRunsOutside, []string{
			"create " + api, "flush " + api,
			"pause " + api,
		}},
		{"stopped conversation, running worker: remove, then start", "stopped", worker.Running, nil, []string{
			"create " + api, "flush " + api,
			"close " + convW,
			"create " + transcripts.Local, "flush " + transcripts.Local,
			"write env", "start in " + apiR + " " + convW,
			"pause " + transcripts.Local, "pause " + api,
		}},
		{"stopped conversation, exited worker: remove, then start", "stopped", worker.Exited, nil, []string{
			"create " + api, "flush " + api,
			"remove " + convW,
			"create " + transcripts.Local, "flush " + transcripts.Local,
			"write env", "start in " + apiR + " " + convW,
			"pause " + transcripts.Local, "pause " + api,
		}},
		{"stopped conversation, no worker: start", "stopped", worker.Absent, nil, []string{
			"create " + api, "flush " + api,
			"create " + transcripts.Local, "flush " + transcripts.Local,
			"write env", "start in " + apiR + " " + convW,
			"pause " + transcripts.Local, "pause " + api,
		}},
		{"an unreadable answer of the agent program", "garbage", worker.Running, errAny, []string{
			"create " + api, "flush " + api,
			"pause " + api,
		}},
		{"no agent, running worker: attach", "", worker.Running, nil, []string{
			"create " + api, "flush " + api,
			"pause " + api,
		}},
		{"no agent, exited worker: remove, then start", "", worker.Exited, nil, []string{
			"create " + api, "flush " + api,
			"remove edit",
			"write env", "start in " + apiR + " edit",
			"pause " + api,
		}},
		{"no agent, no worker: start", "", worker.Absent, nil, []string{
			"create " + api, "flush " + api,
			"write env", "start in " + apiR + " edit",
			"pause " + api,
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake()
			f.attach, f.probe = ends, tc.probe
			req := Request{Remote: f.remote(), Local: testLocal(t), Dir: api, Args: []string{"vim"}, Name: "edit"}
			key := "edit"
			if tc.probe != "" {
				req.Agent = conversation(t, agent.NewNeeds([]string{"claude", "--resume", convID}).WithPlace(transcripts))
				key = convW
			}
			if tc.worker != worker.Absent {
				f.sessions[key] = &tmuxSession{state: tc.worker}
			}
			err := Attach(context.Background(), req)
			if !matches(err, tc.wantErr) {
				t.Errorf("Attach: %v, want %v", err, tc.wantErr)
			}
			if got := f.changes(); !reflect.DeepEqual(got, tc.wantChanges) {
				t.Errorf("changes\n%q\nwant\n%q", got, tc.wantChanges)
			}
		})
	}
}

// errAny stands, in a table, for any error.
var errAny = errors.New("any error")

func matches(err, want error) bool {
	if want == errAny {
		return err != nil
	}
	return errors.Is(err, want)
}

// TestAttachWorkerDirectory checks what a start writes into the worker's
// directory: the agent's files, its settings aimed at a free remote port with
// a token of the worker's, the port and token recorded beside them, and only
// the declared variables with the git identity.
func TestAttachWorkerDirectory(t *testing.T) {
	zeros := strings.Repeat("00", 32)
	cases := []struct {
		name  string
		needs agent.Needs
		want  map[string]string
	}{
		{
			"hooked settings",
			agent.NewNeeds([]string{"claude"}).
				WithFile("mcp.json", secret.FromString(`{"k":"v"}`)).
				WithVars(func(key string) bool { return key == "CLAUDE_X" }).
				WithHookedSettings("settings.json", secret.FromString("doc"), agent.NewCallback("127.0.0.1:1", []string{"/hook"}, secret.FromString("Bearer client"))),
			map[string]string{
				"env":           "export CLAUDE_X=1\nexport GIT_AUTHOR_NAME=Me\nexport GIT_COMMITTER_NAME=Me\nexport HERMOD_WORKER_DIR=/home/u/.hermod/s/hermod-c0ffee\n",
				"mcp.json":      `{"k":"v"}`,
				"settings.json": "doc port=10000 token=" + zeros,
				portFile:        "10000\n",
				tokenFile:       zeros,
			},
		},
		{
			"settings without hooks",
			agent.NewNeeds([]string{"claude"}).WithSettings("settings.json", secret.FromString("doc")),
			map[string]string{
				"env":           "export GIT_AUTHOR_NAME=Me\nexport GIT_COMMITTER_NAME=Me\nexport HERMOD_WORKER_DIR=/home/u/.hermod/s/hermod-c0ffee\n",
				"settings.json": "doc",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake()
			f.attach, f.probe = ends, "stopped"
			err := Attach(context.Background(), Request{
				Remote: f.remote(), Local: testLocal(t), Dir: api,
				Agent:    conversation(t, tc.needs),
				Environ:  env.Environ([]string{"CLAUDE_X=1", "SECRET=s"}),
				Identity: Identity{Name: "Me"},
				Random:   bytes.NewReader(make([]byte, 1024)),
			})
			if err != nil {
				t.Fatalf("Attach: %v", err)
			}
			if got := f.dirs[".hermod/s/"+convW]; !reflect.DeepEqual(got, tc.want) {
				t.Errorf("worker's directory\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}

// netFailures are how the log tells of a Net relay the session runs without.
var netFailures = []string{"no hooks", "no relay"}

// hiddenTokens are the tokens of TestAttachNets: the client's and the one the
// worker is given.
var hiddenTokens = []string{"client", strings.Repeat("00", 32)}

// TestAttachNets checks the session's Net relays: the hooks' on the port the
// worker's directory records, the user's own, and a port taken on the remote,
// which costs only the relay.
func TestAttachNets(t *testing.T) {
	hooked := agent.NewNeeds([]string{"claude"}).
		WithHookedSettings("settings.json", secret.FromString("doc"), agent.NewCallback("127.0.0.1:1", []string{"/hook"}, secret.Token{}))
	cases := []struct {
		name         string
		needs        *agent.Needs // nil runs a command no agent runs
		worker       worker.State
		recorded     map[string]string // the worker's directory before
		forwards     []Forward
		forwardFails bool
		wantForwards []string
		wantLog      string
	}{
		{"a new hooked worker", &hooked, worker.Absent, nil, nil, false, []string{"forward 10000"}, ""},
		{"a running hooked worker", &hooked, worker.Running, map[string]string{portFile: "20001\n", tokenFile: "t"}, nil, false, []string{"forward 20001"}, ""},
		{"a running hooked worker with nothing recorded", &hooked, worker.Running, nil, nil, false, nil, "no hooks"},
		{"a hook port taken on the remote", &hooked, worker.Absent, nil, nil, true, []string{"forward 10000"}, "no hooks: remote port 10000"},
		{"the user's own relay", nil, worker.Absent, nil, []Forward{{Port: 8080, Address: "127.0.0.1:1"}}, false, []string{"forward 8080"}, ""},
		{"the user's port taken", nil, worker.Absent, nil, []Forward{{Port: 8080, Address: "127.0.0.1:1"}}, true, []string{"forward 8080"}, "no relay from remote port 8080"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake()
			f.attach, f.probe, f.forwardFails = ends, "running", tc.forwardFails
			if tc.worker == worker.Absent {
				f.probe = "stopped"
			}
			var logged bytes.Buffer
			req := Request{
				Remote: f.remote(), Local: testLocal(t), Dir: api, Args: []string{"vim"}, Name: "edit",
				Forwards: tc.forwards, Log: log.New(&logged, "", 0), Random: bytes.NewReader(make([]byte, 1024)),
			}
			key := "edit"
			if tc.needs != nil {
				req.Agent, key = conversation(t, *tc.needs), convW
			}
			if tc.worker != worker.Absent {
				f.sessions[key] = &tmuxSession{state: tc.worker}
				f.dirs[".hermod/s/"+key] = tc.recorded
			}
			if err := Attach(context.Background(), req); err != nil {
				t.Fatalf("Attach: %v", err)
			}
			var got []string
			for _, c := range f.changes() {
				if strings.HasPrefix(c, "forward ") {
					got = append(got, c)
				}
			}
			if !reflect.DeepEqual(got, tc.wantForwards) {
				t.Errorf("forwards %q, want %q", got, tc.wantForwards)
			}
			for _, failure := range netFailures {
				if want := tc.wantLog != "" && strings.HasPrefix(tc.wantLog, failure); strings.Contains(logged.String(), failure) != want {
					t.Errorf("log %q, want %q", logged.String(), tc.wantLog)
				}
			}
			if !strings.Contains(logged.String(), tc.wantLog) {
				t.Errorf("log %q, want it to hold %q", logged.String(), tc.wantLog)
			}
			for _, token := range hiddenTokens {
				if strings.Contains(logged.String(), token) {
					t.Errorf("log %q shows a token", logged.String())
				}
			}
		})
	}
}

// TestAttachEnds checks that whatever ends the attach, the session detaches
// the workspace and returns: nil when the client left, the worker's process
// ended or a signal stopped it, and the failure of a dropped connection.
func TestAttachEnds(t *testing.T) {
	cases := []struct {
		name        string
		ending      ending
		eof         bool // the client's input ends
		wantErr     bool
		wantChanges []string
	}{
		{"stdin EOF", waits, true, false, []string{"create " + api, "flush " + api, "pause " + api}},
		{"the process ended", ends, false, false, []string{"create " + api, "flush " + api, "pause " + api}},
		{"a signal", signals, false, false, []string{"create " + api, "flush " + api, "pause " + api}},
		{"a signal that fails the connection", killed, false, false, []string{"create " + api, "flush " + api, "pause " + api}},
		{"a dropped connection", drops, false, true, []string{"create " + api, "flush " + api, "pause " + api}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f := newFake()
			f.attach, f.signal = tc.ending, cancel
			f.sessions["edit"] = &tmuxSession{state: worker.Running}
			in := blocking(t)
			if tc.eof {
				in = strings.NewReader("")
			}
			err := Attach(ctx, Request{
				Remote: f.remote(), Local: testLocal(t), Dir: api, Args: []string{"vim"}, Name: "edit",
				Streams: relay.Streams{In: in},
			})
			if (err != nil) != tc.wantErr {
				t.Errorf("Attach: %v, want error %v", err, tc.wantErr)
			}
			if got := f.changes(); !reflect.DeepEqual(got, tc.wantChanges) {
				t.Errorf("changes %q, want %q", got, tc.wantChanges)
			}
			if _, ok := f.sessions["edit"]; !ok {
				t.Error("the worker is gone, want it running on")
			}
		})
	}
}

// TestOneShot checks that a one-shot call runs on the remote the command its
// agent declares, in the project's remote directory, or the home when that
// cannot be read, passes its exit code back, changes nothing else, and logs
// the names of the variables it cannot carry but never their values.
func TestOneShot(t *testing.T) {
	const value = "sekrit"
	status := []string{"claude", "auth", "status"}
	cases := []struct {
		name        string
		relayed     bool // the project has an FS relay to another remote root
		dir         string
		args        []string
		environ     []string
		listFails   bool
		exitCode    int
		wantErr     error
		wantChanges []string
		wantLogged  []string
	}{
		{"a relayed project", true, api + "/sub", status, nil, false, 0, nil, []string{"run in /home/u/elsewhere/sub claude auth status"}, nil},
		{"a project never relayed", false, api, status, nil, false, 0, nil, []string{"run in " + apiR + " claude auth status"}, nil},
		{"relays that cannot be read", true, api, status, nil, true, 0, nil, []string{"run in " + testRemoteHome + " claude auth status"}, []string{"running in the remote home"}},
		{"an exit code passed back", false, api, status, nil, false, 1, ExitStatus(1), []string{"run in " + apiR + " claude auth status"}, nil},
		{"a path argument mapped", false, api, []string{"claude", "-p", "--settings", api + "/x.json"}, nil, false, 0, nil,
			[]string{"run in " + apiR + " claude -p --settings " + apiR + "/x.json"}, nil},
		{"a declared variable left out", false, api, status, []string{"AGENT_KEY=" + value, "OTHER=1"}, false, 0, nil,
			[]string{"run in " + apiR + " claude auth status"}, []string{"left out: AGENT_KEY\n"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake()
			f.listFails, f.exitCode = tc.listFails, tc.exitCode
			if tc.relayed {
				f.syncs["hermod-prod-x"] = &mutagenSession{local: api, beta: testHost + ":" + testRemoteHome + "/elsewhere",
					labels: map[string]string{"hermod-remote": testHost, "hermod-workspace": "hermod-prod-x"}}
			}
			var logged bytes.Buffer
			err := Attach(context.Background(), Request{
				Remote: f.remote(), Local: testLocal(t), Dir: tc.dir,
				Agent:   fakeAgent{call: agent.NewOneShot(tc.args)},
				Environ: env.Environ(tc.environ),
				Log:     log.New(&logged, "", 0),
			})
			if !errors.Is(err, tc.wantErr) || (tc.wantErr == nil) != (err == nil) {
				t.Errorf("Attach: %v, want %v", err, tc.wantErr)
			}
			if got := f.changes(); !reflect.DeepEqual(got, tc.wantChanges) {
				t.Errorf("changes %q, want %q", got, tc.wantChanges)
			}
			for _, want := range tc.wantLogged {
				if !strings.Contains(logged.String(), want) {
					t.Errorf("log %q, want it to hold %q", logged.String(), want)
				}
			}
			if strings.Contains(logged.String(), value) || strings.Contains(strings.Join(f.changes(), "\n"), value) {
				t.Errorf("a variable's value reached the log %q or the remote %q", logged.String(), f.changes())
			}
		})
	}
}

func TestExitStatus(t *testing.T) {
	cases := []struct {
		status ExitStatus
		want   string
	}{
		{ExitStatus(1), "exit status 1"},
		{ExitStatus(127), "exit status 127"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			if got := tc.status.Error(); got != tc.want {
				t.Errorf("Error() = %q, want %q", got, tc.want)
			}
		})
	}
}
