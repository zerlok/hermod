package session

import (
	"context"
	"errors"
	"io"
	"log"
	"reflect"
	"strings"
	"testing"

	"github.com/zerlok/hermod/internal/relay"
	"github.com/zerlok/hermod/internal/worker"
	"github.com/zerlok/hermod/internal/workspace"
)

// adapterWord stands, in a table, for the key of the adapter worker of api's
// workspace.
const adapterWord = "ADAPTER"

// adapterKeyOf is the key of the adapter worker of dir's workspace, read on a
// fake of its own so the test's fake records nothing of it.
func adapterKeyOf(t *testing.T, dir string) string {
	t.Helper()
	ctx := context.Background()
	ws, err := workspace.Open(ctx, newFake().remote(), testLocal(t), dir, workspace.Worktree{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ws.Detach(ctx) }()
	key, err := ws.AdapterKey()
	if err != nil {
		t.Fatal(err)
	}
	return key.String()
}

// nullChannel is an adapter's connection that delivers nothing.
type nullChannel struct{ received chan Message }

func (c nullChannel) Receive() <-chan Message { return c.received }
func (nullChannel) Err() error                { return nil }
func (nullChannel) Send(Message) error        { return nil }

// adapterConn is what Connect was handed: the workspace root, whether the
// adapter was started, and the adapter's output and its input.
type adapterConn struct {
	root    Path
	started bool
	from    io.Reader
	to      io.Writer
}

// TestAdaptersAttach checks that attaching a directory's workspace starts its
// adapter as a line stream in the workspace's root when none runs, removes
// and starts again an Exited one, joins a Running one as a line stream, and
// reports which; that the adapter's connection ends when its relay stops or
// the workspace detaches; and that a workspace that cannot be opened, or a
// line stream another reader holds, connects nothing and leaves no lease.
func TestAdaptersAttach(t *testing.T) {
	stream := worker.LineStream.String()
	cases := []struct {
		name      string
		adapter   worker.State
		listFails bool
		held      bool // another attach holds the adapter's line stream
		waits     bool // the line stream serves until the workspace detaches
		wantErr   error
		// wantStarted is whether Attach reports it started the adapter, and
		// wantForm the form the fake saw it started in, "" for none.
		wantStarted bool
		wantForm    string
		wantJoins   []string
		wantChanges []string
	}{
		{"no adapter: start", worker.Absent, false, false, false, nil, true, stream, []string{stream}, []string{
			"create " + api, "flush " + api,
			"write env", "start in " + apiR + " " + adapterWord,
		}},
		{"exited adapter: remove, then start", worker.Exited, false, false, false, nil, true, stream, []string{stream}, []string{
			"create " + api, "flush " + api,
			"remove " + adapterWord,
			"write env", "start in " + apiR + " " + adapterWord,
		}},
		{"running adapter: join", worker.Running, false, false, false, nil, false, "", []string{stream}, []string{
			"create " + api, "flush " + api,
		}},
		{"running adapter, detached while it serves", worker.Running, false, false, true, nil, false, "", []string{stream}, []string{
			"create " + api, "flush " + api,
		}},
		{"relays that cannot be read", worker.Absent, true, false, false, errAny, false, "", nil, nil},
		// The holder's detach pausing the files shows the refused attach left
		// no lease of its own on them.
		{"a line stream another reader holds", worker.Running, false, true, true, relay.ErrInUse, false, "", []string{stream}, []string{
			"create " + api, "flush " + api,
			"flush " + api,
			"pause " + api,
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			key := adapterKeyOf(t, api)
			f := newFake()
			f.listFails = tc.listFails
			f.streamWaits = tc.waits
			if tc.adapter != worker.Absent {
				f.sessions[key] = &tmuxSession{state: tc.adapter}
			}
			local := testLocal(t)
			var conns []adapterConn
			adapters := Adapters{
				Remote: f.remote(), Local: local, Agent: fakeAgent{f: f},
				Identity:     func(context.Context, string) Identity { return Identity{} },
				MainWorktree: func(context.Context, string) (string, string, bool) { return "", "", false },
				Connect: func(root Path, started bool, from io.Reader, to io.Writer) Channel {
					conns = append(conns, adapterConn{root: root, started: started, from: from, to: to})
					return nullChannel{received: make(chan Message)}
				},
			}
			var holder Attachment
			if tc.held {
				var err error
				holder, err = adapters.Attach(ctx, Path(api))
				if err != nil {
					t.Fatalf("the holder's Attach: %v", err)
				}
				conns = nil
				defer func() { _ = holder.Workspace.Detach(ctx) }()
			}
			att, err := adapters.Attach(ctx, Path(api))
			if !matches(err, tc.wantErr) {
				t.Fatalf("Attach: %v, want %v", err, tc.wantErr)
			}
			if tc.held {
				if err := holder.Workspace.Detach(ctx); err != nil {
					t.Fatalf("the holder's Detach: %v", err)
				}
			}
			var want []string
			for _, c := range tc.wantChanges {
				want = append(want, strings.ReplaceAll(c, adapterWord, key))
			}
			if got := f.changes(); !reflect.DeepEqual(got, want) {
				t.Errorf("changes\n%q\nwant\n%q", got, want)
			}
			if got := f.startedForm(key); got != tc.wantForm {
				t.Errorf("started in form %q, want %q", got, tc.wantForm)
			}
			// A join's commands run while its relay serves, so they are read
			// once every workspace has detached.
			checkJoins := func() {
				t.Helper()
				if got := f.joined(); !reflect.DeepEqual(got, tc.wantJoins) {
					t.Errorf("joined %q, want %q", got, tc.wantJoins)
				}
			}
			if err != nil {
				checkJoins()
				if len(conns) != 0 {
					t.Error("a failed attach connected to an adapter")
				}
				return
			}
			if len(conns) != 1 {
				t.Fatalf("connected %d times, want once", len(conns))
			}
			if att.Started != tc.wantStarted || att.Root != Path(api) {
				t.Errorf("started, root = %v, %q, want %v, %q", att.Started, att.Root, tc.wantStarted, api)
			}
			if conns[0].started != tc.wantStarted || conns[0].root != Path(api) {
				t.Errorf("connected with started, root = %v, %q, want %v, %q", conns[0].started, conns[0].root, tc.wantStarted, api)
			}
			if !tc.waits {
				// A read to the end returns only once the connection has
				// ended, which a relay that ends at once does by itself.
				_, _ = io.ReadAll(conns[0].from)
			}
			if err := att.Workspace.Detach(ctx); err != nil {
				t.Fatalf("Detach: %v", err)
			}
			_, _ = io.ReadAll(conns[0].from)
			if _, err := conns[0].to.Write([]byte("{}\n")); err == nil {
				t.Error("the adapter's input took a write after its relay stopped")
			}
			checkJoins()
		})
	}
}

// errLog is an adapter's error log in memory.
type errLog struct {
	strings.Builder
	closed bool
}

func (l *errLog) Close() error { l.closed = true; return nil }

// TestAdaptersErrors checks that what an adapter writes to its stderr reaches
// the error log opened by its key, closed once its relay has stopped, and
// reaches the session's log when there is no error log or it cannot be
// opened.
func TestAdaptersErrors(t *testing.T) {
	const says = "adapter: bad request\n"
	cases := []struct {
		name       string
		errors     bool // Adapters has an Errors
		openFails  bool
		wantErrLog string // what the error log holds; "" opens none
		wantLog    []string
	}{
		{"no error log", false, false, "", []string{says}},
		{"an error log", true, false, says, nil},
		{"an error log that cannot be opened", true, true, "", []string{"read-only file system", says}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			key := adapterKeyOf(t, api)
			f := newFake()
			f.adapterSays = says
			f.sessions[key] = &tmuxSession{state: worker.Running}
			var (
				logged strings.Builder
				opened = map[string]*errLog{}
				conns  []adapterConn
			)
			adapters := Adapters{
				Remote: f.remote(), Local: testLocal(t), Agent: fakeAgent{f: f},
				Identity:     func(context.Context, string) Identity { return Identity{} },
				MainWorktree: func(context.Context, string) (string, string, bool) { return "", "", false },
				Connect: func(root Path, started bool, from io.Reader, to io.Writer) Channel {
					conns = append(conns, adapterConn{root: root, started: started, from: from, to: to})
					return nullChannel{received: make(chan Message)}
				},
				Log: log.New(&logged, "", 0),
			}
			if tc.errors {
				adapters.Errors = func(k worker.Key) (io.WriteCloser, error) {
					if tc.openFails {
						return nil, errors.New("error log: read-only file system")
					}
					l := &errLog{}
					opened[k.String()] = l
					return l, nil
				}
			}
			att, err := adapters.Attach(ctx, Path(api))
			if err != nil {
				t.Fatalf("Attach: %v", err)
			}
			// The relay ends at once; a read to the end waits for it.
			_, _ = io.ReadAll(conns[0].from)
			if err := att.Workspace.Detach(ctx); err != nil {
				t.Fatalf("Detach: %v", err)
			}
			for _, want := range tc.wantLog {
				if !strings.Contains(logged.String(), want) {
					t.Errorf("log %q, want it to hold %q", logged.String(), want)
				}
			}
			if tc.wantErrLog == "" {
				if len(opened) != 0 {
					t.Errorf("opened error logs %v, want none", opened)
				}
				return
			}
			l, ok := opened[key]
			if !ok || len(opened) != 1 {
				t.Fatalf("opened error logs %v, want only %s's", opened, key)
			}
			if l.String() != tc.wantErrLog || !l.closed {
				t.Errorf("error log %q, closed %v, want %q, closed", l.String(), l.closed, tc.wantErrLog)
			}
			if strings.Contains(logged.String(), tc.wantErrLog) {
				t.Errorf("log %q also holds the adapter's stderr", logged.String())
			}
		})
	}
}
