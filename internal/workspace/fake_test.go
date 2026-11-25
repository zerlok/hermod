package workspace

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/zerlok/hermod/internal/relay/relaytest"
	"github.com/zerlok/hermod/internal/shell"
	"github.com/zerlok/hermod/internal/worker"
	"github.com/zerlok/hermod/internal/worker/workertest"
)

const (
	testHost       = "prod"
	testLocalHome  = "/home/me"
	testRemoteHome = "/home/u"
)

// exitError is a command's failure with its exit status.
type exitError int

func (e exitError) Error() string { return "exit status " + strconv.Itoa(int(e)) }
func (e exitError) ExitCode() int { return int(e) }

// mutagenSession is one Mutagen session the fake keeps.
type mutagenSession struct {
	local, beta string // beta as host:path
	paused      bool
	labels      map[string]string
}

// tmuxSession is one tmux session on the fake remote.
type tmuxSession struct {
	state       worker.State
	tags        map[string]string
	diesOnClose bool // its process fails just before a close reaches it
}

// fake plays the local workstation's Mutagen and, over ssh, the remote's tmux
// as the worker scripts drive it. It records every command that changes a
// state as one line.
type fake struct {
	mu        sync.Mutex
	syncs     map[string]*mutagenSession
	sessions  map[string]*tmuxSession
	tmuxFails bool // tmux cannot tell which sessions exist
	ioEnds    bool // a joined stdio ends at once, as for a process that ended
	ran       []string
	lines     []string                     // every command line run over ssh
	written   map[string]map[string]string // the files each write put in a staging directory
	started   map[string]workertest.Start  // what each worker was started with, by session
}

func newFake(syncs map[string]*mutagenSession, sessions map[string]*tmuxSession) *fake {
	if syncs == nil {
		syncs = map[string]*mutagenSession{}
	}
	if sessions == nil {
		sessions = map[string]*tmuxSession{}
	}
	return &fake{syncs: syncs, sessions: sessions, written: map[string]map[string]string{}, started: map[string]workertest.Start{}}
}

// remote is the fake as a workspace's Remote.
func (f *fake) remote() Remote {
	return Remote{Host: testHost, Home: testRemoteHome, Exec: f, Read: f}
}

func testLocal(t *testing.T) Local {
	return Local{Home: testLocalHome, Locks: t.TempDir(), Origin: "laptop"}
}

// changes is what changed a state since the last call, and forgets it.
func (f *fake) changes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	ran := f.ran
	f.ran = nil
	return ran
}

// syncNames is the names of the Mutagen sessions, sorted, with the paused ones
// marked.
func (f *fake) syncNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var names []string
	for name, s := range f.syncs {
		if s.paused {
			name += " (paused)"
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

type play func(ctx context.Context, in io.Reader, out, errOut io.Writer) error

func (f *fake) Exec(ctx context.Context, cmd shell.Command) shell.Execution {
	args := cmd.Args
	remote := false
	if len(args) > 0 && args[0] == "ssh" {
		f.mu.Lock()
		f.lines = append(f.lines, args[len(args)-1])
		f.mu.Unlock()
		args, remote = remoteWords(args), true
	}
	var p play
	if remote {
		p = f.onRemote(args)
	} else {
		p = f.onLocal(args)
	}

	var exIn io.WriteCloser
	in := cmd.Stdin
	if in == nil {
		r, w := io.Pipe()
		in, exIn = r, w
	}
	out, exOut, closeOut := outPipe(cmd.Stdout)
	errOut, exErr, closeErr := outPipe(cmd.Stderr)
	done := make(chan error, 1)
	go func() {
		err := p(ctx, in, out, errOut)
		closeOut()
		closeErr()
		if r, ok := in.(*io.PipeReader); ok {
			_ = r.Close()
		}
		done <- err
	}()
	return shell.NewExecution(exIn, exOut, exErr, func() error { return <-done })
}

func outPipe(given io.Writer) (io.Writer, io.ReadCloser, func()) {
	if given != nil {
		return given, nil, func() {}
	}
	r, w := io.Pipe()
	return w, r, func() { _ = w.Close() }
}

func ok(context.Context, io.Reader, io.Writer, io.Writer) error { return nil }

func (f *fake) record(words ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ran = append(f.ran, strings.Join(words, " "))
}

func (f *fake) onLocal(args []string) play {
	if len(args) < 3 || args[0] != "mutagen" {
		return ok
	}
	verb, rest := args[2], args[3:]
	var said string // what the command prints on success, as Mutagen does
	switch verb {
	case "list":
	case "create":
		said = verb + " " + rest[1]
		f.record(verb, rest[1])
	default:
		said = verb + " " + rest[0]
		f.record(verb, rest[0])
	}
	return func(_ context.Context, _ io.Reader, out, errOut io.Writer) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		if _, exists := f.syncs[rest[0]]; verb != "list" && verb != "create" && !exists {
			relaytest.NotFound(errOut)
			return exitError(1)
		}
		switch verb {
		case "list":
			return f.list(rest, out, errOut)
		case "create":
			s := &mutagenSession{local: rest[len(rest)-2], beta: rest[len(rest)-1], labels: map[string]string{}}
			for i := 0; i+1 < len(rest); i++ {
				if rest[i] == "--label" {
					k, v, _ := strings.Cut(rest[i+1], "=")
					s.labels[k] = v
				}
			}
			f.syncs[rest[1]] = s
		case "pause":
			f.syncs[rest[0]].paused = true
		case "resume":
			f.syncs[rest[0]].paused = false
		case "terminate":
			delete(f.syncs, rest[0])
		}
		_, _ = fmt.Fprintln(out, said)
		return nil
	}
}

func (f *fake) list(rest []string, out, errOut io.Writer) error {
	l, ok := relaytest.ReadList(rest)
	if !ok {
		_, _ = fmt.Fprintf(errOut, "Error: unexpected listing %q\n", rest)
		return exitError(1)
	}
	var kept []relaytest.Session
	for name, s := range f.syncs {
		kept = append(kept, relaytest.Session{Name: name, Local: s.local, Beta: s.beta, Paused: s.paused, Labels: s.labels})
	}
	if err := relaytest.Answer(l, kept, out, errOut); err != nil {
		return exitError(1)
	}
	return nil
}

// remoteWords is the command an ssh command line runs on the remote, split
// back into its words.
func remoteWords(args []string) []string {
	return workertest.Words(args[len(args)-1])
}

func (f *fake) onRemote(words []string) play {
	c, isWorker := workertest.Read(words)
	switch {
	case isWorker && c.Script == "act":
		return f.act(c)
	case isWorker && c.Script == "keep":
		return f.keep(c)
	case isWorker && c.Script == "write":
		return f.write(c.Args[0], c.Args[1:])
	case len(words) == 3 && words[0] == "sh" && words[1] == "-c" && strings.HasPrefix(words[2], "echo hermod-net-ready"):
		return f.forward
	}
	return func(_ context.Context, in io.Reader, _, _ io.Writer) error {
		_, err := io.Copy(io.Discard, in)
		return err
	}
}

// forward carries a Net relay's forward until the relay stops it.
func (f *fake) forward(ctx context.Context, _ io.Reader, out, _ io.Writer) error {
	_, _ = fmt.Fprintln(out, "hermod-net-ready")
	<-ctx.Done()
	f.record("net ends")
	return nil
}

// keep starts a worker as keep.sh does.
func (f *fake) keep(c workertest.Command) play {
	return func(_ context.Context, in io.Reader, _, _ io.Writer) error {
		_, _ = io.Copy(io.Discard, in)
		f.mu.Lock()
		defer f.mu.Unlock()
		if _, ok := f.sessions[c.Session]; ok {
			return exitError(c.Refused)
		}
		f.sessions[c.Session] = &tmuxSession{state: worker.Running, tags: c.Start.Tags}
		f.started[c.Session] = c.Start
		f.ran = append(f.ran, "start "+c.Session)
		return nil
	}
}

// write reads the files a start writes into its staging directory: pairs of
// a name and a length, the contents on stdin.
func (f *fake) write(staging string, pairs []string) play {
	return func(_ context.Context, in io.Reader, _, _ io.Writer) error {
		b, _ := io.ReadAll(in)
		f.mu.Lock()
		defer f.mu.Unlock()
		files := map[string]string{}
		for i := 0; i+1 < len(pairs); i += 2 {
			n, _ := strconv.Atoi(pairs[i+1])
			files[pairs[i]], b = string(b[:n]), b[n:]
		}
		f.written[staging] = files
		return nil
	}
}

// act plays one operation of act.sh.
func (f *fake) act(c workertest.Command) play {
	return func(ctx context.Context, in io.Reader, out, _ io.Writer) error {
		switch c.Op {
		case "attach", "log":
			if !f.ioEnds {
				<-ctx.Done()
			}
			return nil
		case "feed":
			_, err := io.Copy(io.Discard, in)
			return err
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.tmuxFails {
			return exitError(c.Failed)
		}
		switch c.Op {
		case "list":
			var names []string
			for name := range f.sessions {
				names = append(names, name)
			}
			sort.Strings(names)
			var listed []workertest.Listed
			for _, name := range names {
				s := f.sessions[name]
				listed = append(listed, workertest.Listed{Name: name, Dead: s.state == worker.Exited, Tags: s.tags})
			}
			_, _ = io.WriteString(out, c.List(listed))
		case "close":
			name := c.Session
			if s, ok := f.sessions[name]; ok && s.diesOnClose {
				s.state = worker.Exited
			}
			if s, ok := f.sessions[name]; ok && s.state == worker.Exited {
				return exitError(c.Refused)
			}
			delete(f.sessions, name)
			f.ran = append(f.ran, "close "+name)
		case "look":
			state := "absent"
			if s, ok := f.sessions[c.Session]; ok {
				state = map[worker.State]string{worker.Running: "0", worker.Exited: "1"}[s.state]
			}
			_, _ = fmt.Fprintf(out, "state %s\nout\n", state)
		case "port":
			_, _ = fmt.Fprintln(out, c.Ports[0])
		case "remove":
			name := c.Session
			if s, ok := f.sessions[name]; ok && s.state == worker.Running {
				return exitError(c.Refused)
			}
			delete(f.sessions, name)
			f.ran = append(f.ran, "remove "+name)
		}
		return nil
	}
}
