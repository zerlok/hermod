package workspace

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zerlok/hermod/internal/env"
	"github.com/zerlok/hermod/internal/pathmap"
	"github.com/zerlok/hermod/internal/relay"
	"github.com/zerlok/hermod/internal/secret"
	"github.com/zerlok/hermod/internal/shell"
	"github.com/zerlok/hermod/internal/worker"
	"github.com/zerlok/hermod/internal/worker/workertest"
)

// rel is an FS relay that exists before a test, on host, of workspace: its
// own name when "".
type rel struct {
	host          string
	local, remote string
	workspace     string
}

// n is the name of the relay between local and remote on the test remote.
func n(local, remote string) string {
	return relayName(testHost, pathmap.Place{Local: local, Remote: remote})
}

func syncsOf(rels ...rel) map[string]*mutagenSession {
	syncs := map[string]*mutagenSession{}
	for _, r := range rels {
		host := r.host
		if host == "" {
			host = testHost
		}
		name := relayName(host, pathmap.Place{Local: r.local, Remote: r.remote})
		ws := r.workspace
		if ws == "" {
			ws = name
		}
		syncs[name] = &mutagenSession{
			local: r.local, beta: host + ":" + r.remote, paused: true,
			labels: map[string]string{labelRemote: remoteLabel(host), labelWorkspace: ws},
		}
	}
	return syncs
}

func tagged(workspace string, state worker.State) *tmuxSession {
	return &tmuxSession{state: state, tags: map[string]string{tagWorkspace: workspace}}
}

func key(t *testing.T, name string) worker.Key {
	t.Helper()
	k, err := worker.NewKey(name)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

const (
	api     = "/home/me/dev/api"
	apiR    = "/home/u/dev/api"
	wt      = "/home/me/dev/api-wt"
	wtR     = "/home/u/dev/api-wt"
	dev     = "/home/me/dev"
	devR    = "/home/u/dev"
	web     = "/home/me/dev/web"
	webR    = "/home/u/dev/web"
	outside = "/opt/work/api"
)

func TestMembersState(t *testing.T) {
	relayIn := func(s relay.State) []relay.Listed { return []relay.Listed{{Name: "r", State: s}} }
	workers := []worker.Listed{{State: worker.Running}}
	cases := []struct {
		name    string
		members Members
		want    State
	}{
		{"no relay and no worker", Members{}, Empty},
		{"a worker alone", Members{Workers: workers}, Detached},
		{"a disconnected relay", Members{Relays: relayIn(relay.Disconnected)}, Detached},
		{"a connected relay", Members{Relays: relayIn(relay.Connected)}, Attached},
		{"a connected relay and a worker", Members{Relays: relayIn(relay.Connected), Workers: workers}, Attached},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.members.State(); got != tc.want {
				t.Errorf("State() = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestMembersWorker(t *testing.T) {
	members := Members{Workers: []worker.Listed{
		{Worker: worker.New(worker.Remote{}, key(t, "hermod-a")), State: worker.Running},
		{Worker: worker.New(worker.Remote{}, key(t, "hermod-b")), State: worker.Exited},
	}}
	cases := []struct {
		key  string
		want worker.State
	}{
		{"hermod-a", worker.Running},
		{"hermod-b", worker.Exited},
		{"hermod-c", worker.Absent},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			if got := members.Worker(key(t, tc.key)); got != tc.want {
				t.Errorf("Worker(%s) = %s, want %s", tc.key, got, tc.want)
			}
		})
	}
}

// TestStateDiagram walks the workspace's diagram: Empty → Attached → Detached
// → Attached → Empty.
func TestStateDiagram(t *testing.T) {
	type world struct {
		f     *fake
		local Local
		ws    *Workspace
	}
	worker1 := "hermod-one"
	steps := []struct {
		name      string
		do        func(context.Context, *world) error
		want      State
		wantSyncs []string
	}{
		{
			name: "its relays are connected for the first time",
			do: func(ctx context.Context, w *world) (err error) {
				w.ws, err = Open(ctx, w.f.remote(), w.local, api, Worktree{})
				return err
			},
			want: Attached, wantSyncs: []string{n(api, apiR)},
		},
		{
			name: "the last lease is suspended, a worker remains",
			do: func(ctx context.Context, w *world) error {
				k, err := worker.NewKey(worker1)
				if err != nil {
					return err
				}
				if err := w.ws.Host(k, worker.Terminal, nil).Exec(ctx, shell.Command{Dir: apiR}).Wait(); err != nil {
					return err
				}
				return w.ws.Detach(ctx)
			},
			want: Detached, wantSyncs: []string{n(api, apiR) + " (paused)"},
		},
		{
			name: "its relays are connected again",
			do: func(ctx context.Context, w *world) (err error) {
				w.ws, err = Open(ctx, w.f.remote(), w.local, api, Worktree{})
				return err
			},
			want: Attached, wantSyncs: []string{n(api, apiR)},
		},
		{
			name: "the last lease is released, no worker",
			do: func(ctx context.Context, w *world) error {
				w.f.mu.Lock()
				delete(w.f.sessions, worker1)
				w.f.mu.Unlock()
				return w.ws.Detach(ctx)
			},
			want: Empty,
		},
	}
	ctx := context.Background()
	w := &world{f: newFake(nil, nil), local: testLocal(t)}
	for _, st := range steps {
		if err := st.do(ctx, w); err != nil {
			t.Fatalf("%s: %v", st.name, err)
		}
		m, err := w.ws.State(ctx)
		if err != nil {
			t.Fatalf("%s: State: %v", st.name, err)
		}
		if got := m.State(); got != st.want {
			t.Errorf("%s: state %s, want %s", st.name, got, st.want)
		}
		if got := w.f.syncNames(); !reflect.DeepEqual(got, st.wantSyncs) {
			t.Errorf("%s: relays %v, want %v", st.name, got, st.wantSyncs)
		}
	}
}

func TestOpen(t *testing.T) {
	cases := []struct {
		name          string
		existing      []rel
		dir           string
		tree          Worktree
		wantWorkspace string
		wantHolder    string // the relay that holds dir, labelled with the workspace
		wantChanges   []string
		wantPlaces    []pathmap.Place
		wantNested    []pathmap.Place
		wantRemoteDir string
	}{
		{
			name:          "a directory inside a covered root",
			existing:      []rel{{local: api, remote: apiR}},
			dir:           api + "/sub",
			wantWorkspace: n(api, apiR), wantHolder: n(api, apiR),
			wantChanges:   []string{"resume " + n(api, apiR), "flush " + n(api, apiR), "flush " + n(api, apiR)},
			wantPlaces:    []pathmap.Place{{Local: api, Remote: apiR}},
			wantRemoteDir: apiR + "/sub",
		},
		{
			name:          "the innermost of nested roots",
			existing:      []rel{{local: dev, remote: devR}, {local: api, remote: "/srv/api"}},
			dir:           api + "/x",
			wantWorkspace: n(api, "/srv/api"), wantHolder: n(api, "/srv/api"),
			wantChanges:   []string{"resume " + n(api, "/srv/api"), "flush " + n(api, "/srv/api"), "flush " + n(api, "/srv/api")},
			wantPlaces:    []pathmap.Place{{Local: api, Remote: "/srv/api"}},
			wantRemoteDir: "/srv/api/x",
		},
		{
			name:          "a sibling linked worktree",
			existing:      []rel{{local: api, remote: apiR}},
			dir:           wt,
			tree:          Worktree{Main: api, Root: wt},
			wantWorkspace: n(api, apiR), wantHolder: n(wt, wtR),
			wantChanges:   []string{"create " + n(wt, wtR), "flush " + n(wt, wtR)},
			wantPlaces:    []pathmap.Place{{Local: wt, Remote: wtR}, {Local: api, Remote: apiR}},
			wantRemoteDir: wtR,
		},
		{
			name:          "a subdirectory of a linked worktree joins the worktree's root",
			existing:      []rel{{local: api, remote: apiR}},
			dir:           wt + "/sub",
			tree:          Worktree{Main: api, Root: wt},
			wantWorkspace: n(api, apiR), wantHolder: n(wt, wtR),
			wantChanges:   []string{"create " + n(wt, wtR), "flush " + n(wt, wtR)},
			wantPlaces:    []pathmap.Place{{Local: wt, Remote: wtR}, {Local: api, Remote: apiR}},
			wantRemoteDir: wtR + "/sub",
		},
		{
			name:          "a subdirectory of a linked worktree whose relay exists",
			existing:      []rel{{local: api, remote: apiR}, {local: wt, remote: wtR, workspace: n(api, apiR)}},
			dir:           wt + "/sub",
			tree:          Worktree{Main: api, Root: wt},
			wantWorkspace: n(api, apiR), wantHolder: n(wt, wtR),
			wantChanges:   []string{"resume " + n(wt, wtR), "flush " + n(wt, wtR), "flush " + n(wt, wtR)},
			wantPlaces:    []pathmap.Place{{Local: wt, Remote: wtR}, {Local: api, Remote: apiR}},
			wantRemoteDir: wtR + "/sub",
		},
		{
			name:          "a linked worktree whose main tree no relay holds joins the directory",
			dir:           wt + "/sub",
			tree:          Worktree{Main: api, Root: wt},
			wantWorkspace: n(wt+"/sub", wtR+"/sub"), wantHolder: n(wt+"/sub", wtR+"/sub"),
			wantChanges:   []string{"create " + n(wt+"/sub", wtR+"/sub"), "flush " + n(wt+"/sub", wtR+"/sub")},
			wantPlaces:    []pathmap.Place{{Local: wt + "/sub", Remote: wtR + "/sub"}},
			wantRemoteDir: wtR + "/sub",
		},
		{
			name:          "the ends of another workspace's relay",
			existing:      []rel{{local: api, remote: apiR}, {local: web, remote: webR, workspace: n(api, apiR)}},
			dir:           web,
			wantWorkspace: n(api, apiR), wantHolder: n(web, webR),
			wantChanges:   []string{"resume " + n(web, webR), "flush " + n(web, webR), "flush " + n(web, webR)},
			wantPlaces:    []pathmap.Place{{Local: api, Remote: apiR}, {Local: web, Remote: webR}},
			wantRemoteDir: webR,
		},
		{
			name:          "a directory under no root",
			existing:      []rel{{local: api, remote: apiR}},
			dir:           web,
			wantWorkspace: n(web, webR), wantHolder: n(web, webR),
			wantChanges:   []string{"create " + n(web, webR), "flush " + n(web, webR)},
			wantPlaces:    []pathmap.Place{{Local: web, Remote: webR}},
			wantRemoteDir: webR,
		},
		{
			name:          "a directory outside the home",
			dir:           outside,
			wantWorkspace: n(outside, "/home/u/opt-work-api"), wantHolder: n(outside, "/home/u/opt-work-api"),
			wantChanges:   []string{"create " + n(outside, "/home/u/opt-work-api"), "flush " + n(outside, "/home/u/opt-work-api")},
			wantPlaces:    []pathmap.Place{{Local: outside, Remote: "/home/u/opt-work-api"}},
			wantRemoteDir: "/home/u/opt-work-api",
		},
		{
			name:          "the home itself",
			dir:           testLocalHome,
			wantWorkspace: n(testLocalHome, "/home/u/me"), wantHolder: n(testLocalHome, "/home/u/me"),
			wantChanges:   []string{"create " + n(testLocalHome, "/home/u/me"), "flush " + n(testLocalHome, "/home/u/me")},
			wantPlaces:    []pathmap.Place{{Local: testLocalHome, Remote: "/home/u/me"}},
			wantRemoteDir: "/home/u/me",
		},
		{
			name:          "a relay whose local end is no local path",
			existing:      []rel{{local: "elsewhere:/srv/api", remote: apiR}},
			dir:           api,
			wantWorkspace: n(api, apiR), wantHolder: n(api, apiR),
			wantChanges:   []string{"create " + n(api, apiR), "flush " + n(api, apiR)},
			wantPlaces:    []pathmap.Place{{Local: api, Remote: apiR}},
			wantRemoteDir: apiR,
		},
		{
			name:          "a root that holds existing roots",
			existing:      []rel{{local: api, remote: apiR}, {local: web, remote: webR, workspace: n(api, apiR)}},
			dir:           dev,
			wantWorkspace: n(dev, devR), wantHolder: n(dev, devR),
			wantChanges:   []string{"create " + n(dev, devR), "flush " + n(dev, devR)},
			wantPlaces:    []pathmap.Place{{Local: dev, Remote: devR}},
			wantNested:    []pathmap.Place{{Local: api, Remote: apiR}},
			wantRemoteDir: devR,
		},
		{
			name:          "a relay of another alias under the same label",
			existing:      []rel{{host: testHost + "-", local: api, remote: apiR}},
			dir:           api,
			wantWorkspace: n(api, apiR), wantHolder: n(api, apiR),
			wantChanges:   []string{"create " + n(api, apiR), "flush " + n(api, apiR)},
			wantPlaces:    []pathmap.Place{{Local: api, Remote: apiR}},
			wantRemoteDir: apiR,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := newFake(syncsOf(tc.existing...), nil)
			w, err := Open(ctx, f.remote(), testLocal(t), tc.dir, tc.tree)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer func() { _ = w.Detach(ctx) }()
			if got := w.Name(); got != tc.wantWorkspace {
				t.Errorf("Name() = %s, want %s", got, tc.wantWorkspace)
			}
			if got := f.syncs[tc.wantHolder]; got == nil || got.labels[labelWorkspace] != tc.wantWorkspace {
				t.Errorf("relay %s = %+v, want one of workspace %s", tc.wantHolder, got, tc.wantWorkspace)
			}
			if got := f.changes(); !reflect.DeepEqual(got, tc.wantChanges) {
				t.Errorf("changes %q, want %q", got, tc.wantChanges)
			}
			if got := w.PathMap().Places(); !reflect.DeepEqual(got, tc.wantPlaces) {
				t.Errorf("Places() = %v, want %v", got, tc.wantPlaces)
			}
			if got := w.Nested(); !reflect.DeepEqual(got, tc.wantNested) {
				t.Errorf("Nested() = %v, want %v", got, tc.wantNested)
			}
			if got := w.PathMap().ToRemote(tc.dir); got != tc.wantRemoteDir {
				t.Errorf("ToRemote(%s) = %s, want %s", tc.dir, got, tc.wantRemoteDir)
			}
		})
	}
}

func TestOpenRefuses(t *testing.T) {
	anyErr := errors.New("any error")
	cases := []struct {
		name     string
		existing []rel
		dir      string
		tree     Worktree
		local    Local
		wantErr  error
	}{
		{"a relative directory", nil, "dev/api", Worktree{}, Local{Home: testLocalHome}, anyErr},
		{"a relative home", nil, api, Worktree{}, Local{Home: "me"}, anyErr},
		{"a relative worktree root", nil, wt, Worktree{Main: api, Root: "dev/api-wt"}, Local{Home: testLocalHome}, anyErr},
		{"the filesystem root", nil, "/", Worktree{}, Local{Home: testLocalHome}, anyErr},
		{
			"a slug another directory outside the home took",
			[]rel{{local: "/opt/work-api", remote: "/home/u/opt-work-api"}},
			outside, Worktree{}, Local{Home: testLocalHome}, ErrRemoteTaken,
		},
		{
			"the home, whose base name a directory below it took",
			[]rel{{local: "/home/me/me", remote: "/home/u/me"}},
			testLocalHome, Worktree{}, Local{Home: testLocalHome}, ErrRemoteTaken,
		},
		{
			"a remote root that holds another local root's",
			[]rel{{local: "/opt/elsewhere", remote: "/home/u/opt-work-api/sub"}},
			outside, Worktree{}, Local{Home: testLocalHome}, ErrRemoteTaken,
		},
		{
			"a worktree root whose remote path another local root joins",
			[]rel{{local: api, remote: apiR}, {local: "/opt/wt", remote: wtR}},
			wt, Worktree{Main: api, Root: wt}, Local{Home: testLocalHome}, ErrRemoteTaken,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(syncsOf(tc.existing...), nil)
			tc.local.Locks = t.TempDir()
			_, err := Open(context.Background(), f.remote(), tc.local, tc.dir, tc.tree)
			if err == nil || (tc.wantErr != anyErr && !errors.Is(err, tc.wantErr)) {
				t.Errorf("Open: %v, want %v", err, tc.wantErr)
			}
			if got := f.changes(); len(got) != 0 {
				t.Errorf("changes %q, want none", got)
			}
		})
	}
}

func TestAddFS(t *testing.T) {
	transcripts := pathmap.Place{Local: "/home/me/.claude/projects/-home-me-dev-api", Remote: "/home/u/.claude/projects/-home-u-dev-api"}
	cases := []struct {
		name        string
		existing    []rel
		place       pathmap.Place
		wantChanges []string
		wantLabel   string // the workspace label of the added relay
		wantErr     bool
	}{
		{
			name:        "a new relay is made in the workspace",
			existing:    []rel{{local: api, remote: apiR}},
			place:       transcripts,
			wantChanges: []string{"create " + n(transcripts.Local, transcripts.Remote), "flush " + n(transcripts.Local, transcripts.Remote)},
			wantLabel:   n(api, apiR),
		},
		{
			name: "the same two ends of another workspace are leased",
			existing: []rel{
				{local: api, remote: apiR},
				{local: transcripts.Local, remote: transcripts.Remote, workspace: n(web, webR)},
			},
			place:       transcripts,
			wantChanges: []string{"resume " + n(transcripts.Local, transcripts.Remote), "flush " + n(transcripts.Local, transcripts.Remote), "flush " + n(transcripts.Local, transcripts.Remote)},
			wantLabel:   n(web, webR),
		},
		{
			name:     "a relative root",
			existing: []rel{{local: api, remote: apiR}},
			place:    pathmap.Place{Local: "projects", Remote: transcripts.Remote},
			wantErr:  true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := newFake(syncsOf(tc.existing...), nil)
			w, err := Open(ctx, f.remote(), testLocal(t), api, Worktree{})
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer func() { _ = w.Detach(ctx) }()
			f.changes()
			err = w.AddFS(ctx, tc.place)
			if (err != nil) != tc.wantErr {
				t.Fatalf("AddFS: %v, want error %v", err, tc.wantErr)
			}
			if got := f.changes(); !reflect.DeepEqual(got, tc.wantChanges) {
				t.Errorf("changes %q, want %q", got, tc.wantChanges)
			}
			if tc.wantErr {
				return
			}
			if got := f.syncs[relayName(testHost, tc.place)].labels[labelWorkspace]; got != tc.wantLabel {
				t.Errorf("workspace label %s, want %s", got, tc.wantLabel)
			}
		})
	}
}

// TestFSOutput checks that what the FS relays' Mutagen commands print reaches
// Local.Output, through each path that builds an FS relay.
func TestFSOutput(t *testing.T) {
	transcripts := pathmap.Place{Local: "/home/me/.claude/projects/-home-me-dev-api", Remote: "/home/u/.claude/projects/-home-u-dev-api"}
	cases := []struct {
		name       string
		existing   []rel
		op         string // after Open: "" for none, "connect" or "addfs"
		noOutput   bool
		wantOutput string // what Output holds after op, or after Open for none
	}{
		{
			name:       "an Open that makes its relay",
			wantOutput: "create " + n(api, apiR) + "\nflush " + n(api, apiR) + "\n",
		},
		{
			name:       "a connect resumes a listed relay of the workspace",
			existing:   []rel{{local: api, remote: apiR}, {local: web, remote: webR, workspace: n(api, apiR)}},
			op:         "connect",
			wantOutput: "resume " + n(web, webR) + "\nflush " + n(web, webR) + "\n",
		},
		{
			name:       "an added relay",
			existing:   []rel{{local: api, remote: apiR}},
			op:         "addfs",
			wantOutput: "create " + n(transcripts.Local, transcripts.Remote) + "\nflush " + n(transcripts.Local, transcripts.Remote) + "\n",
		},
		{
			name:     "no Output",
			noOutput: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := newFake(syncsOf(tc.existing...), nil)
			var output strings.Builder
			local := testLocal(t)
			if !tc.noOutput {
				local.Output = &output
			}
			w, err := Open(ctx, f.remote(), local, api, Worktree{})
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer func() { _ = w.Detach(ctx) }()
			if tc.op != "" {
				output.Reset()
			}
			switch tc.op {
			case "connect":
				err = w.connect(ctx)
			case "addfs":
				err = w.AddFS(ctx, transcripts)
			}
			if err != nil {
				t.Fatalf("%s: %v", tc.op, err)
			}
			if got := output.String(); got != tc.wantOutput {
				t.Errorf("output %q, want %q", got, tc.wantOutput)
			}
		})
	}
}

func TestHostTags(t *testing.T) {
	cases := []struct {
		name     string
		origin   string
		wantTags map[string]string
	}{
		{"with an origin", "my laptop", map[string]string{tagWorkspace: n(api, apiR), tagOrigin: "my-laptop"}},
		{"without one", "", map[string]string{tagWorkspace: n(api, apiR)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := newFake(nil, nil)
			local := testLocal(t)
			local.Origin = tc.origin
			w, err := Open(ctx, f.remote(), local, api, Worktree{})
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer func() { _ = w.Detach(ctx) }()
			k, err := w.AdapterKey()
			if err != nil {
				t.Fatal(err)
			}
			if err := w.Host(k, worker.LineStream, nil).Exec(ctx, shell.Command{Dir: apiR}).Wait(); err != nil {
				t.Fatalf("Host: %v", err)
			}
			if got := f.sessions[k.String()].tags; !reflect.DeepEqual(got, tc.wantTags) {
				t.Errorf("tags %v, want %v", got, tc.wantTags)
			}
		})
	}
}

func TestDetach(t *testing.T) {
	ws := n(api, apiR)
	transcripts := rel{local: "/home/me/.claude/projects/x", remote: "/home/u/.claude/projects/x", workspace: n(web, webR)}
	cases := []struct {
		name        string
		sessions    map[string]*tmuxSession
		leased      []rel // relays of other workspaces this one leases too
		net         bool  // a Net relay is leased before the other relays
		tmuxFails   bool
		wantChanges []string
		wantErr     bool
	}{
		{
			name:        "a worker remains",
			sessions:    map[string]*tmuxSession{"hermod-a": tagged(ws, worker.Running)},
			wantChanges: []string{"pause " + ws},
		},
		{
			name:        "an exited worker remains",
			sessions:    map[string]*tmuxSession{"hermod-a": tagged(ws, worker.Exited)},
			wantChanges: []string{"pause " + ws},
		},
		{
			name:        "only another workspace's worker",
			sessions:    map[string]*tmuxSession{"hermod-a": tagged(n(web, webR), worker.Running)},
			wantChanges: []string{"flush " + ws, "terminate " + ws},
		},
		{
			name:        "no worker",
			wantChanges: []string{"flush " + ws, "terminate " + ws},
		},
		{
			name:        "the workers cannot be read",
			sessions:    map[string]*tmuxSession{},
			tmuxFails:   true,
			wantChanges: []string{"pause " + ws},
			wantErr:     true,
		},
		{
			name:        "another workspace's relay, whose worker runs",
			sessions:    map[string]*tmuxSession{"hermod-a": tagged(transcripts.workspace, worker.Running)},
			leased:      []rel{transcripts},
			wantChanges: []string{"pause " + n(transcripts.local, transcripts.remote), "flush " + ws, "terminate " + ws},
		},
		{
			name:   "a Net relay leased before an FS relay",
			leased: []rel{transcripts},
			net:    true,
			wantChanges: []string{
				"net ends",
				"flush " + n(transcripts.local, transcripts.remote), "terminate " + n(transcripts.local, transcripts.remote),
				"flush " + ws, "terminate " + ws,
			},
		},
		{
			name:   "another workspace's relay, with no worker of either",
			leased: []rel{transcripts},
			wantChanges: []string{
				"flush " + n(transcripts.local, transcripts.remote), "terminate " + n(transcripts.local, transcripts.remote),
				"flush " + ws, "terminate " + ws,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := newFake(syncsOf(append([]rel{{local: api, remote: apiR}}, tc.leased...)...), tc.sessions)
			w, err := Open(ctx, f.remote(), testLocal(t), api, Worktree{})
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if tc.net {
				if err := w.AddNet(ctx, 7000, "127.0.0.1:1", nil); err != nil {
					t.Fatalf("AddNet: %v", err)
				}
			}
			for _, r := range tc.leased {
				if err := w.AddFS(ctx, pathmap.Place{Local: r.local, Remote: r.remote}); err != nil {
					t.Fatalf("AddFS: %v", err)
				}
			}
			f.changes()
			f.tmuxFails = tc.tmuxFails
			err = w.Detach(ctx)
			if (err != nil) != tc.wantErr {
				t.Errorf("Detach: %v, want error %v", err, tc.wantErr)
			}
			if got := f.changes(); !reflect.DeepEqual(got, tc.wantChanges) {
				t.Errorf("changes %q, want %q", got, tc.wantChanges)
			}
			if err := w.Detach(ctx); err != nil {
				t.Errorf("a second Detach: %v, want nothing left to give up", err)
			}
		})
	}
}

func TestClose(t *testing.T) {
	ws := n(api, apiR)
	cases := []struct {
		name        string
		sessions    map[string]*tmuxSession
		key         string
		tmuxFails   bool
		wantChanges []string
		wantLeft    []string // the sessions left
		wantErr     bool
	}{
		{
			name: "another worker is left",
			sessions: map[string]*tmuxSession{
				"hermod-a": tagged(ws, worker.Running),
				"hermod-b": tagged(ws, worker.Running),
			},
			key:         "hermod-a",
			wantChanges: []string{"close hermod-a", "pause " + ws},
			wantLeft:    []string{"hermod-b"},
		},
		{
			name:        "none is left",
			sessions:    map[string]*tmuxSession{"hermod-a": tagged(ws, worker.Running)},
			key:         "hermod-a",
			wantChanges: []string{"close hermod-a", "flush " + ws, "terminate " + ws},
		},
		{
			name:        "an exited worker is removed",
			sessions:    map[string]*tmuxSession{"hermod-a": tagged(ws, worker.Exited)},
			key:         "hermod-a",
			wantChanges: []string{"remove hermod-a", "flush " + ws, "terminate " + ws},
		},
		{
			name:        "a worker that fails before the close reaches it is removed",
			sessions:    map[string]*tmuxSession{"hermod-a": {state: worker.Running, tags: map[string]string{tagWorkspace: ws}, diesOnClose: true}},
			key:         "hermod-a",
			wantChanges: []string{"remove hermod-a", "flush " + ws, "terminate " + ws},
		},
		{
			name:        "an absent worker has nothing to close",
			sessions:    map[string]*tmuxSession{"hermod-b": tagged(ws, worker.Running)},
			key:         "hermod-a",
			wantChanges: []string{"pause " + ws},
			wantLeft:    []string{"hermod-b"},
		},
		{
			name:        "the worker's state cannot be read",
			sessions:    map[string]*tmuxSession{"hermod-a": tagged(ws, worker.Running)},
			key:         "hermod-a",
			tmuxFails:   true,
			wantChanges: []string{"pause " + ws},
			wantLeft:    []string{"hermod-a"},
			wantErr:     true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := newFake(syncsOf(rel{local: api, remote: apiR}), tc.sessions)
			w, err := Open(ctx, f.remote(), testLocal(t), api, Worktree{})
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			f.changes()
			f.tmuxFails = tc.tmuxFails
			err = w.Close(ctx, key(t, tc.key))
			if (err != nil) != tc.wantErr {
				t.Errorf("Close: %v, want error %v", err, tc.wantErr)
			}
			if got := f.changes(); !reflect.DeepEqual(got, tc.wantChanges) {
				t.Errorf("changes %q, want %q", got, tc.wantChanges)
			}
			var left []string
			for name := range f.sessions {
				left = append(left, name)
			}
			if !reflect.DeepEqual(left, tc.wantLeft) {
				t.Errorf("sessions left %v, want %v", left, tc.wantLeft)
			}
		})
	}
}

func TestMap(t *testing.T) {
	// probe is one path on each side that the map joins both ways.
	type probe struct{ local, remote string }
	cases := []struct {
		name     string
		existing []rel
		dirs     []string // the directories the caller names
		probes   []probe
	}{
		{
			name: "no relay on the remote",
			probes: []probe{
				{web + "/main.go", webR + "/main.go"},
				{outside, outside},
			},
		},
		{
			name:     "one workspace",
			existing: []rel{{local: api, remote: "/srv/api"}},
			probes: []probe{
				{api + "/main.go", "/srv/api/main.go"},
				{web, webR},
			},
		},
		{
			name: "two workspaces with nested roots",
			existing: []rel{
				{local: dev, remote: "/srv/dev"},
				{local: api, remote: "/srv/api"},
				{local: wt, remote: "/srv/api-wt", workspace: n(api, "/srv/api")},
			},
			probes: []probe{
				{api + "/x.go", "/srv/api/x.go"},
				{wt + "/x.go", "/srv/api-wt/x.go"},
				{web, "/srv/dev/web"},
			},
		},
		{
			name:     "a directory under no root, in both directions",
			existing: []rel{{local: api, remote: "/srv/api"}},
			dirs:     []string{"/opt/data"},
			probes: []probe{
				{"/home/me/notes", "/home/u/notes"},
				{"/opt/data/x", "/home/u/opt-data/x"},
				{api, "/srv/api"},
			},
		},
		{
			name: "the home itself",
			dirs: []string{testLocalHome},
			probes: []probe{
				{testLocalHome, "/home/u/me"},
				{web, "/home/u/me/dev/web"},
			},
		},
		{
			name:     "a named directory a root holds",
			existing: []rel{{local: api, remote: "/srv/api"}},
			dirs:     []string{api + "/sub"},
			probes:   []probe{{api + "/sub", "/srv/api/sub"}},
		},
		{
			name:     "a relay whose local end is no local path",
			existing: []rel{{local: "elsewhere:/srv/api", remote: "/srv/api"}},
			probes:   []probe{{web, webR}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(syncsOf(tc.existing...), nil)
			m, err := Map(context.Background(), f.remote(), testLocal(t), tc.dirs...)
			if err != nil {
				t.Fatalf("Map: %v", err)
			}
			for _, p := range tc.probes {
				if got := m.ToRemote(p.local); got != p.remote {
					t.Errorf("ToRemote(%s) = %s, want %s", p.local, got, p.remote)
				}
				if got := m.ToLocal(p.remote); got != p.local {
					t.Errorf("ToLocal(%s) = %s, want %s", p.remote, got, p.local)
				}
			}
			if got := f.changes(); len(got) != 0 {
				t.Errorf("changes %q, want none", got)
			}
		})
	}
}

func TestMapRefusesARelativeHome(t *testing.T) {
	cases := []struct {
		name   string
		remote string
		local  string
	}{
		{"a relative remote home", "u", testLocalHome},
		{"a relative local home", testRemoteHome, "me"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(syncsOf(rel{local: api, remote: apiR}), nil)
			remote := f.remote()
			remote.Home = tc.remote
			if _, err := Map(context.Background(), remote, Local{Home: tc.local, Locks: t.TempDir()}); err == nil {
				t.Error("Map succeeded")
			}
		})
	}
}

func TestAttach(t *testing.T) {
	cases := []struct {
		name    string
		form    worker.Form
		ends    bool // the worker's stdio ends at once
		taken   bool // another reader holds the worker's line stream
		gone    bool // the project FS relay is terminated from outside
		wantErr error
	}{
		{"a terminal whose process ends", worker.Terminal, true, false, false, relay.ErrEnded},
		{"a line stream whose process ends", worker.LineStream, true, false, false, relay.ErrEnded},
		{"a second reader of a line stream", worker.LineStream, false, true, false, relay.ErrInUse},
		{"an FS relay terminated elsewhere", worker.LineStream, false, false, true, ErrRelayGone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			ws := n(api, apiR)
			f := newFake(syncsOf(rel{local: api, remote: apiR}), map[string]*tmuxSession{"hermod-a": tagged(ws, worker.Running)})
			f.ioEnds = tc.ends
			local := testLocal(t)
			k := key(t, "hermod-a")
			if tc.taken {
				other := relay.NewIO(shell.NewSSH(f, testHost), local.Locks, ioName(testHost, k), relay.Lines{}, relay.Streams{})
				lease, err := other.Connect(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = lease.Release(ctx) }()
			}
			w, err := Open(ctx, f.remote(), local, api, Worktree{})
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer func() { _ = w.Detach(ctx) }()
			w.poll = time.Millisecond
			if tc.gone {
				f.mu.Lock()
				delete(f.syncs, ws)
				f.mu.Unlock()
			}
			if err := w.Attach(ctx, k, tc.form, relay.Streams{}); !errors.Is(err, tc.wantErr) {
				t.Errorf("Attach: %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// TestJoin checks which failure Join reports itself, before anyone reads the
// streams, and which one its wait reports once the relays served.
func TestJoin(t *testing.T) {
	cases := []struct {
		name        string
		taken       bool // another reader holds the worker's line stream
		wantJoinErr error
		wantWaitErr error
	}{
		{"a line stream whose process ends", false, nil, relay.ErrEnded},
		{"a second reader of a line stream", true, relay.ErrInUse, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			ws := n(api, apiR)
			f := newFake(syncsOf(rel{local: api, remote: apiR}), map[string]*tmuxSession{"hermod-a": tagged(ws, worker.Running)})
			f.ioEnds = true
			local := testLocal(t)
			k := key(t, "hermod-a")
			if tc.taken {
				other := relay.NewIO(shell.NewSSH(f, testHost), local.Locks, ioName(testHost, k), relay.Lines{}, relay.Streams{})
				lease, err := other.Connect(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = lease.Release(ctx) }()
			}
			w, err := Open(ctx, f.remote(), local, api, Worktree{})
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer func() { _ = w.Detach(ctx) }()
			wait, err := w.Join(ctx, k, worker.LineStream, relay.Streams{})
			if !errors.Is(err, tc.wantJoinErr) {
				t.Fatalf("Join: %v, want %v", err, tc.wantJoinErr)
			}
			if err != nil {
				return
			}
			if err := wait(); !errors.Is(err, tc.wantWaitErr) {
				t.Errorf("wait: %v, want %v", err, tc.wantWaitErr)
			}
		})
	}
}

// TestEnd checks that ending a worker keeps the leases: nothing of the
// workspace's relays changes, so the caller can start the worker again.
func TestEnd(t *testing.T) {
	ws := n(api, apiR)
	cases := []struct {
		name        string
		state       worker.State
		tmuxFails   bool
		wantChanges []string
		wantLeft    []string
		wantErr     bool
	}{
		{"a running worker is closed", worker.Running, false, []string{"close hermod-a"}, nil, false},
		{"an exited worker is removed", worker.Exited, false, []string{"remove hermod-a"}, nil, false},
		{"an absent worker has nothing to end", worker.Absent, false, nil, nil, false},
		{"the worker's state cannot be read", worker.Running, true, nil, []string{"hermod-a"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			sessions := map[string]*tmuxSession{}
			if tc.state != worker.Absent {
				sessions["hermod-a"] = tagged(ws, tc.state)
			}
			f := newFake(syncsOf(rel{local: api, remote: apiR}), sessions)
			w, err := Open(ctx, f.remote(), testLocal(t), api, Worktree{})
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer func() { _ = w.Detach(ctx) }()
			f.changes()
			f.tmuxFails = tc.tmuxFails
			if err := w.End(ctx, key(t, "hermod-a")); (err != nil) != tc.wantErr {
				t.Errorf("End: %v, want error %v", err, tc.wantErr)
			}
			if got := f.changes(); !reflect.DeepEqual(got, tc.wantChanges) {
				t.Errorf("changes %q, want %q", got, tc.wantChanges)
			}
			var left []string
			for name := range f.sessions {
				left = append(left, name)
			}
			if !reflect.DeepEqual(left, tc.wantLeft) {
				t.Errorf("sessions left %v, want %v", left, tc.wantLeft)
			}
		})
	}
}

// TestLookAndFreePort checks that both reach the remote's workers and change
// nothing.
func TestLookAndFreePort(t *testing.T) {
	ws := n(api, apiR)
	cases := []struct {
		name      string
		state     worker.State
		wantState worker.State
		wantPort  int
	}{
		{"a running worker", worker.Running, worker.Running, 20001},
		{"an absent worker", worker.Absent, worker.Absent, 20001},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			sessions := map[string]*tmuxSession{}
			if tc.state != worker.Absent {
				sessions["hermod-a"] = tagged(ws, tc.state)
			}
			f := newFake(syncsOf(rel{local: api, remote: apiR}), sessions)
			w, err := Open(ctx, f.remote(), testLocal(t), api, Worktree{})
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer func() { _ = w.Detach(ctx) }()
			f.changes()
			look, err := w.Look(ctx, key(t, "hermod-a"))
			if err != nil || look.State != tc.wantState {
				t.Errorf("Look = %v, %v; want %v", look.State, err, tc.wantState)
			}
			port, err := w.FreePort(ctx, "port", []int{tc.wantPort})
			if err != nil || port != tc.wantPort {
				t.Errorf("FreePort = %d, %v; want %d", port, err, tc.wantPort)
			}
			if got := f.changes(); len(got) != 0 {
				t.Errorf("changes %q, want none", got)
			}
		})
	}
}

// errRecord is a record that cannot be read.
var errRecord = errors.New("no port free")

// TestHost checks what a command executed on a Host starts: the worker under
// the Host's key and form, in the command's directory, running its arguments,
// with its variables and the record's in the worker's env and the record's
// files beside it, none of them on a command line, and an Execution that has
// ended once the worker runs.
func TestHost(t *testing.T) {
	const hidden = "SEKRIT"
	cases := []struct {
		name      string
		form      worker.Form
		cmd       shell.Command
		record    Record
		recordErr error
		present   bool // a worker runs under the key already
		wantErr   error
		want      *workertest.Start // nil starts nothing
		wantFiles map[string]string
	}{
		{
			name: "a terminal worker with variables and a record",
			form: worker.Terminal,
			cmd: shell.Command{
				Args: []string{"claude", "--resume", "c1"},
				Dir:  apiR,
				Env:  env.New(env.Pair{Key: "TOKEN", Value: env.Secret(secret.FromString(hidden))}),
			},
			record: Record{
				Files: []File{{Name: "hook-token", Content: secret.FromString(hidden)}},
				Vars:  env.New(env.Pair{Key: "GIT_AUTHOR_NAME", Value: env.Plain("Me")}),
			},
			want: &workertest.Start{Dir: apiR, Form: "terminal", Command: []string{"claude", "--resume", "c1"}},
			wantFiles: map[string]string{
				"env":        "export TOKEN=" + hidden + "\nexport GIT_AUTHOR_NAME=Me\n",
				"hook-token": hidden,
			},
		},
		{
			name:      "a line stream with no record",
			form:      worker.LineStream,
			cmd:       shell.Command{Args: []string{"adapter"}, Dir: apiR},
			want:      &workertest.Start{Dir: apiR, Form: "stream", Command: []string{"adapter"}},
			wantFiles: map[string]string{"env": ""},
		},
		{
			name:      "a record that cannot be read starts nothing",
			form:      worker.Terminal,
			cmd:       shell.Command{Args: []string{"claude"}, Dir: apiR},
			recordErr: errRecord,
			wantErr:   errRecord,
		},
		{
			name:    "a worker already under the key",
			form:    worker.Terminal,
			cmd:     shell.Command{Args: []string{"claude"}, Dir: apiR},
			present: true,
			wantErr: worker.ErrPresent,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := newFake(nil, nil)
			w, err := Open(ctx, f.remote(), testLocal(t), api, Worktree{})
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer func() { _ = w.Detach(ctx) }()
			k := key(t, "hermod-c1")
			if tc.present {
				f.sessions[k.String()] = tagged(w.Name(), worker.Running)
			}
			record := func() (Record, error) { return tc.record, tc.recordErr }
			err = w.Host(k, tc.form, record).Exec(ctx, tc.cmd).Wait()
			if !errors.Is(err, tc.wantErr) || (err == nil) != (tc.wantErr == nil) {
				t.Fatalf("Wait: %v, want %v", err, tc.wantErr)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			got, started := f.started[k.String()]
			if started != (tc.want != nil) {
				t.Fatalf("started %v, want %v", started, tc.want != nil)
			}
			for _, line := range f.lines {
				if strings.Contains(line, hidden) {
					t.Errorf("a command line holds a secret: %q", line)
				}
			}
			if tc.want == nil {
				return
			}
			if got.Dir != tc.want.Dir || got.Form != tc.want.Form || !reflect.DeepEqual(got.Command, tc.want.Command) {
				t.Errorf("started in %q as %q running %q, want in %q as %q running %q", got.Dir, got.Form, got.Command, tc.want.Dir, tc.want.Form, tc.want.Command)
			}
			if files := f.written[got.Staging]; !reflect.DeepEqual(files, tc.wantFiles) {
				t.Errorf("worker's directory %q, want %q", files, tc.wantFiles)
			}
		})
	}
}
