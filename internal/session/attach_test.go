package session

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"maps"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/zerlok/hermod/internal/agent"
	"github.com/zerlok/hermod/internal/env"
	"github.com/zerlok/hermod/internal/pathmap"
	"github.com/zerlok/hermod/internal/relay"
	"github.com/zerlok/hermod/internal/secret"
	"github.com/zerlok/hermod/internal/workspace"
)

const (
	api    = testLocalHome + "/api"
	apiR   = testRemoteHome + "/api"
	convID = "c0ffee"
	convW  = "hermod-" + convID
)

// transcripts is the FS relay between the local and the remote agent's state
// directories of api.
var transcripts = pathmap.Place{Local: testLocalHome + "/.claude/projects/-home-me-api", Remote: testRemoteHome + "/.claude/projects/-home-u-api"}

// conversation is a request that runs the conversation convID through
// agents for both workstations, with its settings' hooks; Air's hooks are
// told by their path.
func conversation(f *fake, req Request, hooks ...agent.Hook) Request {
	remote := fakeAgent{f: f, invocation: agent.InvokesConversation, id: convID, stateRoot: testRemoteHome + "/.claude/projects", hooks: hooks}
	req.Agent = remote
	req.LocalAgent = fakeAgent{f: f, stateRoot: testLocalHome + "/.claude/projects"}
	req.Args = []string{"claude", "--resume", convID}
	req.IsHook = func(u *url.URL) bool { return strings.HasPrefix(u.Path, "/air/") }
	return req
}

// withConversation gives the fake program the conversation convID in state;
// "garbled" makes its answers unreadable, "" leaves it without the
// conversation.
func withConversation(f *fake, state string) {
	switch state {
	case "garbled":
		f.queryGarbled = true
	case agent.Running.String():
		f.convs = []agent.Conversation{{ID: convID, Dir: apiR, State: agent.Running}}
	case agent.Stopped.String():
		f.convs = []agent.Conversation{{ID: convID, Dir: apiR, State: agent.Stopped}}
	}
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
// directory, and starts the worker last; one whose settings' hooks cannot be
// read starts none.
func TestAttachDecides(t *testing.T) {
	cases := []struct {
		name        string
		agent       bool   // the command is a conversation's
		probe       string // what the agent program says of the conversation
		worker      workspace.WorkerState
		hooksFail   bool // the agent cannot read its settings' hooks
		wantErr     error
		wantChanges []string
	}{
		{"running conversation, running worker: attach", true, "running", workspace.WorkerRunning, false, nil, []string{
			"create " + api, "flush " + api,
			"pause " + api,
		}},
		{"running conversation, no worker: refuse", true, "running", workspace.WorkerAbsent, false, ErrRunsOutside, []string{
			"create " + api, "flush " + api,
			"flush " + api, "terminate " + api,
		}},
		{"running conversation, exited worker: refuse", true, "running", workspace.WorkerExited, false, ErrRunsOutside, []string{
			"create " + api, "flush " + api,
			"pause " + api,
		}},
		{"stopped conversation, running worker: remove, then start", true, "stopped", workspace.WorkerRunning, false, nil, []string{
			"create " + api, "flush " + api,
			"close " + convW,
			"create " + transcripts.Local, "flush " + transcripts.Local,
			"write env", "start in " + apiR + " " + convW,
			"pause " + transcripts.Local, "pause " + api,
		}},
		{"stopped conversation, exited worker: remove, then start", true, "stopped", workspace.WorkerExited, false, nil, []string{
			"create " + api, "flush " + api,
			"remove " + convW,
			"create " + transcripts.Local, "flush " + transcripts.Local,
			"write env", "start in " + apiR + " " + convW,
			"pause " + transcripts.Local, "pause " + api,
		}},
		{"a conversation the program does not have yet, no worker: start", true, "", workspace.WorkerAbsent, false, nil, []string{
			"create " + api, "flush " + api,
			"create " + transcripts.Local, "flush " + transcripts.Local,
			"write env", "start in " + apiR + " " + convW,
			"pause " + transcripts.Local, "pause " + api,
		}},
		{"stopped conversation, no worker: start", true, "stopped", workspace.WorkerAbsent, false, nil, []string{
			"create " + api, "flush " + api,
			"create " + transcripts.Local, "flush " + transcripts.Local,
			"write env", "start in " + apiR + " " + convW,
			"pause " + transcripts.Local, "pause " + api,
		}},
		{"an unreadable answer of the agent program", true, "garbled", workspace.WorkerRunning, false, errAny, []string{
			"create " + api, "flush " + api,
			"pause " + api,
		}},
		{"settings whose hooks cannot be read, no worker: refuse", true, "stopped", workspace.WorkerAbsent, true, errAny, []string{
			"create " + api, "flush " + api,
			"create " + transcripts.Local, "flush " + transcripts.Local,
			"flush " + transcripts.Local, "terminate " + transcripts.Local,
			"flush " + api, "terminate " + api,
		}},
		{"no agent, running worker: attach", false, "", workspace.WorkerRunning, false, nil, []string{
			"create " + api, "flush " + api,
			"pause " + api,
		}},
		{"no agent, exited worker: remove, then start", false, "", workspace.WorkerExited, false, nil, []string{
			"create " + api, "flush " + api,
			"remove edit",
			"write env", "start in " + apiR + " edit",
			"pause " + api,
		}},
		{"no agent, no worker: start", false, "", workspace.WorkerAbsent, false, nil, []string{
			"create " + api, "flush " + api,
			"write env", "start in " + apiR + " edit",
			"pause " + api,
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake()
			f.attach = ends
			withConversation(f, tc.probe)
			req := Request{Remote: f.remote(), Local: testLocal(t), Dir: api, Args: []string{"vim"}, Name: "edit", Teardown: unbounded}
			key := "edit"
			if tc.agent {
				req, key = conversation(f, req), convW
				if tc.hooksFail {
					req.Agent = failingHooks(req.Agent)
				}
			}
			if tc.worker != workspace.WorkerAbsent {
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

// failingHooks is a conversation's agent that cannot read its settings'
// hooks.
func failingHooks(a agent.Agent) agent.Agent {
	failing := a.(fakeAgent)
	failing.hooksFail = errors.New("unreadable settings")
	return failing
}

// errAny stands, in a table, for any error.
var errAny = errors.New("any error")

func matches(err, want error) bool {
	if want == errAny {
		return err != nil
	}
	return errors.Is(err, want)
}

// clientToken is the client's credential in the hooks of
// TestAttachWorkerDirectory and TestAttachNets, which no log may show.
const clientToken = "client-credential"

// The hooks of TestAttachWorkerDirectory and TestAttachNets.
var (
	airHook   = agent.Hook{URL: "http://127.0.0.1:1/air/hook?x=1", Headers: map[string]secret.Token{"Authorization": secret.FromString("Bearer " + clientToken)}}
	airPlain  = agent.Hook{URL: "http://127.0.0.1:1/air/stop"}
	airOther  = agent.Hook{URL: "http://127.0.0.1:1/api/x", Headers: map[string]secret.Token{"Authorization": secret.FromString("Bearer " + clientToken)}}
	otherHook = agent.Hook{URL: "http://user:" + clientToken + "@localhost:9000/mine?tok=" + clientToken}
	httpsHook = agent.Hook{URL: "https://127.0.0.1:1/air/hook"}
	webHook   = agent.Hook{URL: "https://hooks.example/x", Headers: map[string]secret.Token{"X": secret.FromString("y")}}
)

// zeros is the worker's token the tests' random source generates.
var zeros = strings.Repeat("00", 32)

// aimedHook is airHook as the worker's settings carry it.
func aimedHook(port, uri string) agent.Hook {
	return agent.Hook{URL: "http://localhost:" + port + uri, Headers: map[string]secret.Token{"Authorization": secret.FromString("Bearer " + zeros)}}
}

// TestAttachWorkerDirectory checks what a start writes into the worker's
// directory and what its settings' hooks become: the client's hooks aimed at a
// free remote port with a token of the worker's, recorded beside them with
// their routes, other loopback hooks left out and logged, any other hook kept;
// and only the declared variables with the git identity.
func TestAttachWorkerDirectory(t *testing.T) {
	identity := "export GIT_AUTHOR_NAME=Me\nexport GIT_COMMITTER_NAME=Me\n"
	cases := []struct {
		name       string
		hooks      []agent.Hook
		files      []file // the files the agent places
		want       map[string]string
		wantHooks  []agent.Hook
		wantPlaced []string
		wantLogged []string
	}{
		{
			"the client's hooks aimed and recorded",
			[]agent.Hook{airHook, airHook, webHook},
			nil,
			map[string]string{
				"env":      "export AGENT_X=1\n" + identity,
				portFile:   "10000\n",
				routesFile: "/air/hook?x=1\n",
				tokenFile:  zeros,
			},
			[]agent.Hook{aimedHook("10000", "/air/hook?x=1"), aimedHook("10000", "/air/hook?x=1"), webHook},
			nil,
			nil,
		},
		{
			"another loopback endpoint left out and logged without its credentials",
			[]agent.Hook{otherHook, airPlain, otherHook},
			nil,
			map[string]string{
				"env":      "export AGENT_X=1\n" + identity,
				portFile:   "10000\n",
				routesFile: "/air/stop\n",
				tokenFile:  zeros,
			},
			[]agent.Hook{aimedHook("10000", "/air/stop")},
			nil,
			[]string{"left out the hook to http://localhost:9000/mine: no relay carries it to this workstation"},
		},
		{
			"a hook to the client's endpoint before the client's own hook aimed too",
			[]agent.Hook{airOther, airHook},
			nil,
			map[string]string{
				"env":      "export AGENT_X=1\n" + identity,
				portFile:   "10000\n",
				routesFile: "/api/x\n/air/hook?x=1\n",
				tokenFile:  zeros,
			},
			[]agent.Hook{aimedHook("10000", "/api/x"), aimedHook("10000", "/air/hook?x=1")},
			nil,
			nil,
		},
		{
			"a hook to the client's endpoint with another token left out",
			[]agent.Hook{airHook, airPlain},
			nil,
			map[string]string{
				"env":      "export AGENT_X=1\n" + identity,
				portFile:   "10000\n",
				routesFile: "/air/hook?x=1\n",
				tokenFile:  zeros,
			},
			[]agent.Hook{aimedHook("10000", "/air/hook?x=1")},
			nil,
			[]string{"left out the hook to http://127.0.0.1:1/air/stop: it carries another credential than the client's endpoint expects"},
		},
		{
			"an https hook to the client left out, no port picked",
			[]agent.Hook{httpsHook, webHook},
			nil,
			map[string]string{"env": "export AGENT_X=1\n" + identity},
			[]agent.Hook{webHook},
			nil,
			[]string{"left out the hook to https://127.0.0.1:1/air/hook: no relay carries it to this workstation"},
		},
		{
			"no hooks",
			nil,
			nil,
			map[string]string{"env": "export AGENT_X=1\n" + identity},
			nil,
			nil,
			nil,
		},
		{
			"the agent's files placed in the worker's directory beside its hooks' record",
			[]agent.Hook{airHook},
			[]file{{"settings.json", `{"hooks":{}}`}, {"mcp-1.json", `{"mcpServers":{}}`}},
			map[string]string{
				"env":           "export AGENT_X=1\n" + identity,
				"settings.json": `{"hooks":{}}`,
				"mcp-1.json":    `{"mcpServers":{}}`,
				portFile:        "10000\n",
				routesFile:      "/air/hook?x=1\n",
				tokenFile:       zeros,
			},
			[]agent.Hook{aimedHook("10000", "/air/hook?x=1")},
			[]string{testRemoteHome + "/.hermod/s/" + convW + "/settings.json", testRemoteHome + "/.hermod/s/" + convW + "/mcp-1.json"},
			nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake()
			f.attach = ends
			var logged bytes.Buffer
			req := conversation(f, Request{
				Remote: f.remote(), Local: testLocal(t), Dir: api, Teardown: unbounded,
				Environ:  env.Environ([]string{"AGENT_X=1", "SECRET=s"}),
				Identity: Identity{Name: "Me"},
				Log:      log.New(&logged, "", 0),
				Random:   bytes.NewReader(make([]byte, 1024)),
			}, tc.hooks...)
			placing := req.Agent.(fakeAgent)
			placing.files = tc.files
			req.Agent = placing
			if err := Attach(context.Background(), req); err != nil {
				t.Fatalf("Attach: %v", err)
			}
			if got := f.dirs[".hermod/s/"+convW]; !reflect.DeepEqual(got, tc.want) {
				t.Errorf("worker's directory\n%q\nwant\n%q", got, tc.want)
			}
			if len(f.hooked) != 1 || !sameHooks(f.hooked[0], tc.wantHooks) {
				t.Errorf("hooks started with %v, want one start with %v", revealHooks(f.hooked), revealHooks([][]agent.Hook{tc.wantHooks}))
			}
			if !reflect.DeepEqual(f.placed, tc.wantPlaced) {
				t.Errorf("files placed at %q, want %q", f.placed, tc.wantPlaced)
			}
			for _, want := range tc.wantLogged {
				if !strings.Contains(logged.String(), want) {
					t.Errorf("log %q, want it to hold %q", logged.String(), want)
				}
			}
			if strings.Contains(logged.String(), clientToken) {
				t.Errorf("log %q shows a credential", logged.String())
			}
		})
	}
}

func sameHooks(a, b []agent.Hook) bool {
	return reflect.DeepEqual(revealHooks([][]agent.Hook{a}), revealHooks([][]agent.Hook{b}))
}

// revealHooks prints hooks with their headers' values, for comparing them.
func revealHooks(all [][]agent.Hook) []string {
	var out []string
	for _, hooks := range all {
		for _, h := range hooks {
			line := h.URL
			for _, k := range slices.Sorted(maps.Keys(h.Headers)) {
				line += " " + k + "=" + h.Headers[k].Reveal()
			}
			out = append(out, line)
		}
		out = append(out, "--")
	}
	return out
}

// netFailures are how the log tells of a Net relay the session runs without.
var netFailures = []string{"no hooks", "no relay"}

// TestAttachNets checks the session's Net relays: the hooks' on the port the
// worker's directory records, the user's own, and a port taken on the remote
// or a running worker's settings that cannot be read now, which cost only the
// relay.
func TestAttachNets(t *testing.T) {
	recorded := map[string]string{portFile: "20001\n", tokenFile: "t", routesFile: "/air/hook?x=1\n"}
	cases := []struct {
		name         string
		agent        bool // the command is a conversation's
		hooks        []agent.Hook
		worker       workspace.WorkerState
		hooksFail    bool              // the agent cannot read its settings' hooks
		recorded     map[string]string // the worker's directory before
		forwards     []Forward
		forwardFails bool
		wantForwards []string
		wantLog      string
	}{
		{"a new hooked worker", true, []agent.Hook{airHook}, workspace.WorkerAbsent, false, nil, nil, false, []string{"forward 10000"}, ""},
		{"a running hooked worker", true, []agent.Hook{airHook}, workspace.WorkerRunning, false, recorded, nil, false, []string{"forward 20001"}, ""},
		{"a running hooked worker with nothing recorded", true, []agent.Hook{airHook}, workspace.WorkerRunning, false, nil, nil, false, nil, "no hooks"},
		{"a running hooked worker whose settings cannot be read now", true, []agent.Hook{airHook}, workspace.WorkerRunning, true, recorded, nil, false, nil, "no hooks: unreadable settings"},
		{"a running worker the caller's settings hook to no client", true, []agent.Hook{webHook}, workspace.WorkerRunning, false, recorded, nil, false, nil, ""},
		{"a new worker with no hook to the client", true, []agent.Hook{webHook}, workspace.WorkerAbsent, false, nil, nil, false, nil, ""},
		{"a hook port taken on the remote", true, []agent.Hook{airHook}, workspace.WorkerAbsent, false, nil, nil, true, []string{"forward 10000"}, "no hooks: remote port 10000"},
		{"the user's own relay", false, nil, workspace.WorkerAbsent, false, nil, []Forward{{Port: 8080, Address: "127.0.0.1:1"}}, false, []string{"forward 8080"}, ""},
		{"the user's port taken", false, nil, workspace.WorkerAbsent, false, nil, []Forward{{Port: 8080, Address: "127.0.0.1:1"}}, true, []string{"forward 8080"}, "no relay from remote port 8080"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake()
			f.attach, f.forwardFails = ends, tc.forwardFails
			state := agent.Stopped.String()
			if tc.worker == workspace.WorkerRunning {
				state = agent.Running.String()
			}
			withConversation(f, state)
			var logged bytes.Buffer
			req := Request{
				Remote: f.remote(), Local: testLocal(t), Dir: api, Args: []string{"vim"}, Name: "edit",
				Forwards: tc.forwards, Log: log.New(&logged, "", 0), Random: bytes.NewReader(make([]byte, 1024)),
				Teardown: unbounded,
			}
			key := "edit"
			if tc.agent {
				req, key = conversation(f, req, tc.hooks...), convW
				if tc.hooksFail {
					req.Agent = failingHooks(req.Agent)
				}
			}
			if tc.worker != workspace.WorkerAbsent {
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
			for _, token := range []string{clientToken, zeros} {
				if strings.Contains(logged.String(), token) {
					t.Errorf("log %q shows a token", logged.String())
				}
			}
		})
	}
}

// TestRejoinHooks checks the hooks a session serves for a running worker:
// the port, worker's token and routes its directory records, carried to the
// client's endpoint the caller's settings name now, whose token may differ
// from the one the worker was started with. It drives one call on a recorded
// route through the filter they make.
func TestRejoinHooks(t *testing.T) {
	recorded := map[string]string{portFile: "20001\n", tokenFile: "w0", routesFile: "/air/hook?x=1\n"}
	newClient := agent.Hook{URL: "http://127.0.0.1:2/air/hook?x=2", Headers: map[string]secret.Token{"Authorization": secret.FromString("Bearer new")}}
	cases := []struct {
		name        string
		hooks       []agent.Hook      // the caller's settings now
		recorded    map[string]string // the worker's directory
		wantHooked  bool
		wantPort    int
		wantRoutes  []string
		wantAddress string
		wantWorker  string // the token the filter accepts
		wantClient  string // the Authorization the filter sends on
	}{
		{"the client's endpoint as when the worker started", []agent.Hook{airHook}, recorded,
			true, 20001, []string{"/air/hook?x=1"}, "127.0.0.1:1", "w0", "Bearer " + clientToken},
		{"the client restarted with another address and token", []agent.Hook{newClient}, recorded,
			true, 20001, []string{"/air/hook?x=1"}, "127.0.0.1:2", "w0", "Bearer new"},
		{"the caller's settings hook to no client", []agent.Hook{webHook}, recorded,
			false, 0, nil, "", "", ""},
		{"a worker that records no hooks", []agent.Hook{airHook}, nil,
			false, 0, nil, "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake()
			ctx := context.Background()
			req := conversation(f, Request{Remote: f.remote(), Local: testLocal(t), Dir: api, Teardown: unbounded}, tc.hooks...)
			f.sessions[convW] = &tmuxSession{state: workspace.WorkerRunning}
			f.dirs[".hermod/s/"+convW] = tc.recorded
			ws, err := workspace.Open(ctx, req.Remote, req.Local, req.Dir, workspace.Worktree{}, unbounded)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			key, err := workspace.ConversationKey(convID)
			if err != nil {
				t.Fatal(err)
			}
			seen, err := ws.Look(ctx, key, portFile, tokenFile, routesFile)
			if err != nil {
				t.Fatalf("Look: %v", err)
			}
			got := rejoinHooks(req, ws, seen)
			if (got != nil) != tc.wantHooked {
				t.Fatalf("hooks %v, want hooked %v", got, tc.wantHooked)
			}
			if got == nil {
				return
			}
			if got.port != tc.wantPort || !reflect.DeepEqual(got.routes, tc.wantRoutes) || got.client.address != tc.wantAddress {
				t.Errorf("hooks on port %d, routes %q, to %s; want %d, %q, %s", got.port, got.routes, got.client.address, tc.wantPort, tc.wantRoutes, tc.wantAddress)
			}
			passed, err := got.filter(ws.PathMap()).Pass(relay.Call{
				Method: http.MethodPost,
				URI:    tc.wantRoutes[0],
				Header: http.Header{"Authorization": {"Bearer " + tc.wantWorker}},
			})
			if err != nil {
				t.Fatalf("Pass: %v", err)
			}
			if a := passed.Header.Get("Authorization"); a != tc.wantClient {
				t.Errorf("sent on with Authorization %q, want %q", a, tc.wantClient)
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
			f.sessions["edit"] = &tmuxSession{state: workspace.WorkerRunning}
			in := blocking(t)
			if tc.eof {
				in = strings.NewReader("")
			}
			err := Attach(ctx, Request{
				Remote: f.remote(), Local: testLocal(t), Dir: api, Args: []string{"vim"}, Name: "edit",
				Streams: workspace.Streams{In: in}, Teardown: unbounded,
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

// TestOpenFails checks that an open whose flush fails gives up what it leased
// within a teardown started for it, which a signal that ended the run does not
// cut, for an attach and for the adapters alike.
func TestOpenFails(t *testing.T) {
	attach := func(ctx context.Context, f *fake, local workspace.Local, teardown workspace.Teardown) error {
		return Attach(ctx, Request{
			Remote: f.remote(), Local: local, Dir: api, Args: []string{"vim"}, Name: "edit",
			Streams: workspace.Streams{In: strings.NewReader("")}, Teardown: teardown,
		})
	}
	adapters := func(ctx context.Context, f *fake, local workspace.Local, teardown workspace.Teardown) error {
		_, err := Adapters{
			Remote: f.remote(), Local: local, Agent: fakeAgent{f: f},
			Identity:     func(context.Context, string) Identity { return Identity{} },
			MainWorktree: func(context.Context, string) (string, string, bool) { return "", "", false },
			Connect:      func(io.Reader, io.Writer) Channel { return nullChannel{received: make(chan Inbound)} },
		}.Attach(ctx, Path(api), teardown)
		return err
	}
	cases := []struct {
		name         string
		open         func(context.Context, *fake, workspace.Local, workspace.Teardown) error
		signalled    bool // the run context ended before the open
		wantChanges  []string
		wantCut      []string
		wantTeardown int
	}{
		{"an attach", attach, false, []string{"create " + api, "flush " + api, "flush " + api, "terminate " + api}, nil, 1},
		{"an attach after a signal", attach, true, []string{"create " + api, "flush " + api, "flush " + api, "terminate " + api}, []string{"flush " + api}, 1},
		{"the adapters", adapters, false, []string{"create " + api, "flush " + api, "flush " + api, "terminate " + api}, nil, 1},
		{"the adapters after a signal", adapters, true, []string{"create " + api, "flush " + api, "flush " + api, "terminate " + api}, []string{"flush " + api}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.signalled {
				cancel()
			}
			f := newFake()
			f.flushFails = true
			started := 0
			teardown := func() (context.Context, context.CancelFunc) {
				started++
				return unbounded()
			}
			if err := tc.open(ctx, f, testLocal(t), teardown); err == nil {
				t.Error("open: no error, want the flush's")
			}
			if got := f.changes(); !reflect.DeepEqual(got, tc.wantChanges) {
				t.Errorf("changes %q, want %q", got, tc.wantChanges)
			}
			if !reflect.DeepEqual(f.cut, tc.wantCut) {
				t.Errorf("run with an ended context %q, want %q", f.cut, tc.wantCut)
			}
			if started != tc.wantTeardown {
				t.Errorf("teardowns started %d, want %d", started, tc.wantTeardown)
			}
		})
	}
}

// TestOneShot checks that a one-shot call runs on the remote as given, in the
// project's remote directory, or the home when that cannot be read, passes its
// exit code back, changes nothing else, and carries no variable.
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
		{"arguments as given", false, api, []string{"claude", "-p", "--settings", api + "/x.json"}, nil, false, 0, nil,
			[]string{"run in " + apiR + " claude -p --settings " + api + "/x.json"}, nil},
		{"no variable carried", false, api, status, []string{"AGENT_KEY=" + value, "OTHER=1"}, false, 0, nil,
			[]string{"run in " + apiR + " claude auth status"}, nil},
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
				Remote: f.remote(), Local: testLocal(t), Dir: tc.dir, Args: tc.args,
				Agent:   fakeAgent{f: f, invocation: agent.InvokesOneShot},
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
