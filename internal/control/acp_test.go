package control

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/zerlok/hermod/internal/claude"
	"github.com/zerlok/hermod/internal/mirror"
	"github.com/zerlok/hermod/internal/sandbox"
	"github.com/zerlok/hermod/internal/shell"
)

// fakeAdapter stands in for the ACP adapter: it reads every byte Air's side
// sends it, writes its answer to its stdout and exits with err.
type fakeAdapter struct {
	answer string
	err    error
	cmd    shell.Command
	got    string // what reached the adapter's stdin
}

func (f *fakeAdapter) Run(_ context.Context, cmd shell.Command) (shell.Result, error) {
	f.cmd = cmd
	b, _ := io.ReadAll(cmd.Stdin)
	f.got = string(b)
	_, _ = io.WriteString(cmd.Stdout, f.answer)
	return shell.Result{}, f.err
}

// recordedCloses records the sessions a run closes, marking one whose close
// was given a context already done, and one reopened by the time it ran.
type recordedCloses struct {
	mu  sync.Mutex
	ids []string
}

func (r *recordedCloses) close(ctx context.Context, id claude.SessionID, reopened func() bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case ctx.Err() != nil:
		r.ids = append(r.ids, "cancelled "+id.String())
		return ctx.Err()
	case reopened():
		r.ids = append(r.ids, "reopened "+id.String())
		return nil
	}
	r.ids = append(r.ids, id.String())
	return nil
}

// otherUUID is a second chat's session UUID.
const otherUUID = "1c8f6a4d-3b52-4d9f-8e7a-2f3b4c5d6e7f"

func archive(id string) string {
	return `{"jsonrpc":"2.0","id":7,"method":"_session/archive","params":{"sessionId":"` + id + `"}}` + "\n"
}

func unarchive(id string) string {
	return `{"jsonrpc":"2.0","id":8,"method":"_session/unarchive","params":{"sessionId":"` + id + `"}}` + "\n"
}

func TestRunACP(t *testing.T) {
	const (
		newSession = `{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"cwd":"/home/me/dev/api","mcpServers":[]}}` + "\n"
		answer     = `{"jsonrpc":"2.0","id":1,"result":{"sessionId":"` + sessionUUID + `"}}` + "\n" + `{"jsonrpc":"2.0","method":"session/update","params":{"text":"ünïcode \u2028"}}` + "\n"
		node       = "/opt/node/bin/node"
		script     = "/opt/node/bin/claude-agent-acp"
	)
	cases := []struct {
		name          string
		fromAir       string
		adapterAnswer string
		adapterErr    error
		cancelled     bool // the run was asked to stop, as Air's SIGTERM does
		wantArgv      []string
		wantInterrupt os.Signal
		wantToAir     string
		wantClosed    []string
		wantStatus    ExitStatus // 0 with wantErr unset: success
		wantErr       bool       // an error that is no ExitStatus
		wantLogPart   string     // a substring of Hermod's stderr; "" for none
	}{
		{
			name:          "messages copied both ways as they are",
			fromAir:       newSession + `{"jsonrpc":"2.0","id":2,"method":"session/prompt","params":{"prompt":[{"type":"text","text":"a\\nb"}]}}` + "\n",
			adapterAnswer: answer, wantToAir: answer,
			wantArgv: []string{node, script}, wantInterrupt: syscall.SIGTERM,
		},
		{
			name:          "a final line without its newline still reaches the adapter",
			fromAir:       newSession + `{"jsonrpc":"2.0","id":2,"method":"session/cancel"}`,
			adapterAnswer: answer, wantToAir: answer,
			wantArgv: []string{node, script}, wantInterrupt: syscall.SIGTERM,
		},
		{
			name:          "mark done closes the session and still reaches the adapter",
			fromAir:       newSession + archive(sessionUUID),
			adapterAnswer: answer, wantToAir: answer,
			wantArgv: []string{node, script}, wantInterrupt: syscall.SIGTERM,
			wantClosed:  []string{sessionUUID},
			wantLogPart: "chat " + sessionUUID + " marked done",
		},
		{
			name:          "several marked done, closed in order",
			fromAir:       archive(sessionUUID) + archive(otherUUID),
			adapterAnswer: answer, wantToAir: answer,
			wantArgv: []string{node, script}, wantInterrupt: syscall.SIGTERM,
			wantClosed: []string{sessionUUID, otherUUID},
		},
		{
			name:          "unarchive alone needs nothing",
			fromAir:       unarchive(sessionUUID),
			adapterAnswer: answer, wantToAir: answer,
			wantArgv: []string{node, script}, wantInterrupt: syscall.SIGTERM,
		},
		{
			name:          "a session ID that is not a Claude UUID closes nothing",
			fromAir:       archive("sess_42"),
			adapterAnswer: answer, wantToAir: answer,
			wantArgv: []string{node, script}, wantInterrupt: syscall.SIGTERM,
			wantLogPart: "not a Claude session UUID",
		},
		{
			name:          "the adapter's exit code passed on",
			fromAir:       archive(sessionUUID),
			adapterAnswer: answer, wantToAir: answer,
			adapterErr: exitErr{code: 3},
			wantArgv:   []string{node, script}, wantInterrupt: syscall.SIGTERM,
			wantClosed: []string{sessionUUID},
			wantStatus: 3,
		},
		{
			name:          "stopped by the signal Hermod was sent, a close begun still done",
			fromAir:       archive(sessionUUID),
			adapterAnswer: answer, wantToAir: answer,
			adapterErr: exitErr{code: -1},
			cancelled:  true,
			wantArgv:   []string{node, script}, wantInterrupt: syscall.SIGTERM,
			wantClosed: []string{sessionUUID},
		},
		{
			name:          "stopped as asked, the adapter exiting with a code of its own",
			adapterAnswer: answer, wantToAir: answer,
			adapterErr: exitErr{code: 143},
			cancelled:  true,
			wantArgv:   []string{node, script}, wantInterrupt: syscall.SIGTERM,
		},
		{
			name:          "killed by a signal Hermod was not sent",
			adapterAnswer: answer, wantToAir: answer,
			adapterErr: exitErr{code: -1},
			wantArgv:   []string{node, script}, wantInterrupt: syscall.SIGTERM,
			wantErr: true,
		},
		{
			name:       "an adapter that does not start",
			adapterErr: errors.New("exec: node: not found"),
			wantArgv:   []string{node, script}, wantInterrupt: syscall.SIGTERM,
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancelled {
				cancel()
			}
			adapter := &fakeAdapter{answer: tc.adapterAnswer, err: tc.adapterErr}
			var toAir, stderr bytes.Buffer
			closed := &recordedCloses{}
			a := ACPAdapter{
				Sandbox: "prod",
				Node:    node,
				Script:  script,
				Stdio:   Streams{In: strings.NewReader(tc.fromAir), Out: &toAir, Err: &stderr},
			}
			err := runACP(ctx, a, adapter, closed.close)

			var status ExitStatus
			switch {
			case tc.wantErr:
				if err == nil || errors.As(err, &status) {
					t.Fatalf("runACP() = %v, want an error that is no exit status", err)
				}
			case tc.wantStatus != 0:
				if !errors.As(err, &status) || status != tc.wantStatus {
					t.Fatalf("runACP() = %v, want exit status %d", err, tc.wantStatus)
				}
			case err != nil:
				t.Fatalf("runACP() error: %v", err)
			}
			if adapter.got != tc.fromAir {
				t.Errorf("the adapter got\n %q\nwant\n %q", adapter.got, tc.fromAir)
			}
			if toAir.String() != tc.wantToAir {
				t.Errorf("Air got\n %q\nwant\n %q", toAir.String(), tc.wantToAir)
			}
			if !reflect.DeepEqual(closed.ids, tc.wantClosed) {
				t.Errorf("closed %q, want %q", closed.ids, tc.wantClosed)
			}
			if !reflect.DeepEqual(adapter.cmd.Argv, tc.wantArgv) {
				t.Errorf("argv = %q, want %q", adapter.cmd.Argv, tc.wantArgv)
			}
			// No Env: the adapter runs with Hermod's own environment.
			if adapter.cmd.Env != nil || adapter.cmd.Interrupt != tc.wantInterrupt {
				t.Errorf("the adapter runs with env %q and interrupt %v, want its own environment and %v", adapter.cmd.Env, adapter.cmd.Interrupt, tc.wantInterrupt)
			}
			if !strings.Contains(stderr.String(), tc.wantLogPart) {
				t.Errorf("stderr lacks %q:\n%s", tc.wantLogPart, stderr.String())
			}
		})
	}
}

// heldClose is a close that waits to be let go, so marks can arrive while it
// is under way. It records what it closes as recordedCloses does.
type heldClose struct {
	recordedCloses
	started chan struct{}
	release chan struct{}
}

func (h *heldClose) close(ctx context.Context, id claude.SessionID, reopened func() bool) error {
	h.started <- struct{}{}
	<-h.release
	return h.recordedCloses.close(ctx, id, reopened)
}

// TestClosesReopen asserts a chat reopened after it was marked done is not
// closed after the reopen: a close still queued is dropped, and one under way
// learns of it before its hang-up.
func TestClosesReopen(t *testing.T) {
	cases := []struct {
		name       string
		underWay   string   // a mark whose close is under way while then arrives; "" for none
		then       []string // the marks that follow
		wantClosed []string
	}{
		{"reopened while its close is under way", archive(sessionUUID), []string{unarchive(sessionUUID)}, []string{"reopened " + sessionUUID}},
		{"reopened while its close is queued", archive(sessionUUID), []string{archive(otherUUID), unarchive(otherUUID)}, []string{sessionUUID}},
		{"another chat reopened", archive(sessionUUID), []string{unarchive(otherUUID)}, []string{sessionUUID}},
		{"marked done again after a reopen", archive(sessionUUID), []string{unarchive(sessionUUID), archive(sessionUUID)}, []string{"reopened " + sessionUUID, sessionUUID}},
		{"reopened with nothing to close", "", []string{unarchive(sessionUUID)}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			held := &heldClose{started: make(chan struct{}, len(tc.then)+1), release: make(chan struct{})}
			c := newCloses(context.Background(), "prod", held.close, log.New(io.Discard, "", 0))
			if tc.underWay != "" {
				c.marked([]byte(tc.underWay))
				<-held.started
			}
			for _, line := range tc.then {
				c.marked([]byte(line))
			}
			close(held.release)
			c.wait()
			if !reflect.DeepEqual(held.ids, tc.wantClosed) {
				t.Errorf("closed %q, want %q", held.ids, tc.wantClosed)
			}
		})
	}
}

// TestCopyMessages asserts Air's bytes reach the adapter exactly, each line
// handed on only once the adapter has it.
func TestCopyMessages(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		wantSeen []string
	}{
		{"lines", "{\"a\":1}\n{\"b\":2}\n", []string{"{\"a\":1}\n", "{\"b\":2}\n"}},
		{"a final partial line", "{\"a\":1}\n{\"b\"", []string{"{\"a\":1}\n", "{\"b\""}},
		{"blank lines kept", "\n\n{}\n", []string{"\n", "\n", "{}\n"}},
		{"a line longer than any buffer", strings.Repeat("x", 1<<20) + "\n", []string{strings.Repeat("x", 1<<20) + "\n"}},
		{"nothing", "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var dst bytes.Buffer
			var seen []string
			err := copyMessages(&dst, strings.NewReader(tc.in), func(line []byte) {
				if !strings.HasSuffix(dst.String(), string(line)) {
					t.Errorf("line %q seen before the adapter has it", line)
				}
				seen = append(seen, string(line))
			})
			if err != nil {
				t.Fatalf("copyMessages() error: %v", err)
			}
			if dst.String() != tc.in {
				t.Errorf("copied %q, want %q", dst.String(), tc.in)
			}
			if !reflect.DeepEqual(seen, tc.wantSeen) {
				t.Errorf("seen %q, want %q", seen, tc.wantSeen)
			}
		})
	}
}

// TestCopyMessagesWriteFailure asserts a copy to an adapter that is gone stops
// and hands on nothing it could not deliver.
func TestCopyMessagesWriteFailure(t *testing.T) {
	cases := []struct {
		name    string
		writeTo io.Writer
		wantErr error
	}{
		{"the adapter's stdin closed", failingWriter{err: os.ErrClosed}, os.ErrClosed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seen := 0
			err := copyMessages(tc.writeTo, strings.NewReader(archive(sessionUUID)), func([]byte) { seen++ })
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("copyMessages() = %v, want %v", err, tc.wantErr)
			}
			if seen != 0 {
				t.Errorf("%d undelivered lines handed on", seen)
			}
		})
	}
}

// listedTranscripts is a `mutagen sync list` entry of the transcript mirror of
// the sandbox "prod" for dir.
func listedTranscripts(name string) string {
	return "--------\nName: " + name + "\nLabels:\n\thermod-kind: transcripts\n\thermod-sandbox: prod\n" +
		"Alpha:\n\tURL: prod:" + sandboxHome + "/.claude/projects/-home-u-dev-api\nBeta:\n\tURL: /home/me/.claude/projects/-home-me-dev-api\nStatus: [Paused]\n"
}

func TestCloseSession(t *testing.T) {
	const (
		projectName     = "hermod-prod-dev"
		transcriptsName = "hermod-prod-dev-api-transcripts-c0c6a709"
		sessionLog      = sessionUUID + ".log"
	)
	listing := listedRoot(projectName, "/home/me/dev", "dev") + listedTranscripts(transcriptsName)
	tagged := func(dir string) reply {
		return reply{match: "tmux=yes", ret: shell.Result{Stdout: "home=" + sandboxHome + "\ntmux=yes\ndir=" + dir + "\nend\n"}}
	}
	gone := reply{match: "tmux=yes", ret: shell.Result{Stdout: "home=" + sandboxHome + "\ntmux=no\nend\n"}}
	cases := []struct {
		name        string
		lookUp      reply
		listing     reply
		reopened    bool // Air reopened the chat by the time of the hang-up
		hangUp      reply
		liveness    reply
		otherHolds  []string // mirrors another process holds a lease on, as an attached stand-in does
		wantSteps   []string // what the close ran, read-only Mutagen listings left out
		wantFree    []string // mirrors whose lease no one holds once the close is over
		wantLog     string   // the session log the close writes
		wantLogPart string   // a substring of Hermod's stderr; "" for none
		wantErr     error
	}{
		{
			name:   "alone under the root: the session and both mirrors closed",
			lookUp: tagged("dev/api"), listing: reply{ret: shell.Result{Stdout: listing}}, liveness: livenessReply(),
			wantSteps: []string{"look up", "mirror resume", "transcripts resume", "close", "liveness", "mirror flush", "mirror close", "transcripts flush", "transcripts close"},
			wantFree:  []string{projectName, transcriptsName}, wantLog: sessionLog,
		},
		{
			name:   "another session under the root keeps the project mirror",
			lookUp: tagged("dev/api"), listing: reply{ret: shell.Result{Stdout: listing}}, liveness: livenessReply("dev/web"),
			wantSteps: []string{"look up", "mirror resume", "transcripts resume", "close", "liveness", "mirror pause", "transcripts flush", "transcripts close"},
			wantFree:  []string{projectName, transcriptsName}, wantLog: sessionLog,
		},
		{
			name:   "a stand-in attached to another session on the mirror keeps it",
			lookUp: tagged("dev/api"), listing: reply{ret: shell.Result{Stdout: listing}}, liveness: livenessReply("dev/web"),
			otherHolds: []string{projectName},
			wantSteps:  []string{"look up", "mirror resume", "transcripts resume", "close", "liveness", "transcripts flush", "transcripts close"},
			wantFree:   []string{transcriptsName}, wantLog: sessionLog,
		},
		{
			name:   "a session tagged with an absolute directory",
			lookUp: tagged(sandboxHome + "/dev/api"), listing: reply{ret: shell.Result{Stdout: listing}}, liveness: livenessReply(),
			wantSteps: []string{"look up", "mirror resume", "close", "liveness", "mirror flush", "mirror close"},
			wantFree:  []string{projectName, transcriptsName}, wantLog: sessionLog,
		},
		{
			name:   "a Claude that outlives the hang-up: mirrors paused",
			lookUp: tagged("dev/api"), listing: reply{ret: shell.Result{Stdout: listing}}, liveness: livenessReply(),
			hangUp:    reply{err: exitErr{code: 4}},
			wantSteps: []string{"look up", "mirror resume", "transcripts resume", "close", "mirror pause", "transcripts pause"},
			wantFree:  []string{projectName, transcriptsName}, wantLog: sessionLog,
			wantLogPart: "the hang-up failed, and its Claude may still run → pausing it",
			wantErr:     sandbox.ErrClaudeLingers,
		},
		{
			name:   "a chat reopened before the hang-up: left running, mirrors settled as on a detach",
			lookUp: tagged("dev/api"), listing: reply{ret: shell.Result{Stdout: listing}}, liveness: livenessReply("dev/api"),
			reopened:  true,
			wantSteps: []string{"look up", "mirror resume", "transcripts resume", "liveness", "mirror pause", "transcripts pause"},
			wantFree:  []string{projectName, transcriptsName}, wantLog: sessionLog,
			wantLogPart: "reopened: hermod-" + sessionUUID + " left running",
		},
		{
			name:   "no tmux session: nothing to close",
			lookUp: gone, listing: reply{ret: shell.Result{Stdout: listing}}, liveness: livenessReply(),
			wantSteps: []string{"look up"},
			wantFree:  []string{projectName, transcriptsName}, wantLog: sessionLog,
		},
		{
			name:   "an untagged session: closed, its mirrors left",
			lookUp: tagged(""), listing: reply{ret: shell.Result{Stdout: listing}}, liveness: livenessReply(),
			wantSteps: []string{"look up", "close"},
			wantFree:  []string{projectName, transcriptsName}, wantLog: sessionLog,
		},
		{
			name:   "mirrors that cannot be listed: closed all the same",
			lookUp: tagged("dev/api"), listing: reply{err: errors.New("mutagen daemon unavailable")}, liveness: livenessReply(),
			wantSteps: []string{"look up", "close"},
			wantFree:  []string{projectName, transcriptsName}, wantLog: sessionLog,
			wantErr: errAny,
		},
		{
			name:   "a look-up that fails: nothing done",
			lookUp: reply{match: "tmux=yes", err: exitErr{code: 255}}, listing: reply{ret: shell.Result{Stdout: listing}}, liveness: livenessReply(),
			wantSteps: []string{"look up"},
			wantFree:  []string{projectName, transcriptsName}, wantLog: sessionLog,
			wantErr: errAny,
		},
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
			tc.lookUp.match, tc.listing.match, tc.hangUp.match = "tmux=yes", "--label-selector", "pane_pid"
			leaf := &scripted{replies: []reply{
				tc.liveness, tc.lookUp, tc.listing, tc.hangUp,
				// Matched before the project mirror, whose name it extends.
				{match: "mutagen sync list " + transcriptsName, ret: shell.Result{Stdout: listing}},
				{match: "mutagen sync list " + projectName, ret: shell.Result{Stdout: listing}},
			}}
			logDir := t.TempDir()
			var stderr bytes.Buffer
			o := ACPAdapter{Sandbox: "prod", Stdio: Streams{Err: &stderr}, LeaseDir: leaseDir, SessionLogDir: logDir}.closeOptions()
			err := closeSession(context.Background(), o, leaf, mustID(t), func() bool { return tc.reopened })
			checkErr(t, err, tc.wantErr)

			var steps []string
			for _, cmd := range leaf.cmds {
				if !strings.Contains(strings.Join(cmd.Argv, " "), "mutagen sync list") {
					steps = append(steps, step(cmd))
				}
			}
			if !reflect.DeepEqual(steps, tc.wantSteps) {
				t.Errorf("steps =\n %q\nwant\n %q", steps, tc.wantSteps)
			}
			for _, cmd := range leaf.cmds {
				if !cmd.Capture && cmd.Stdin != shell.NoInput {
					t.Errorf("%q may read Air's stdin, which carries ACP", cmd.Argv)
				}
			}
			if _, err := os.Stat(filepath.Join(logDir, tc.wantLog)); err != nil {
				t.Errorf("no session log: %v", err)
			}
			if !strings.Contains(stderr.String(), tc.wantLogPart) {
				t.Errorf("stderr lacks %q:\n%s", tc.wantLogPart, stderr.String())
			}
			for _, name := range tc.wantFree {
				l, err := mirror.TakeLease(context.Background(), leaseDir, name)
				if err != nil {
					t.Fatalf("lease %s: %v", name, err)
				}
				if last, err := l.Release(); err != nil || !last {
					t.Errorf("lease %s is still held by the close", name)
				}
				_ = l.Close()
			}
		})
	}
}
