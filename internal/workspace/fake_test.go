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

	"github.com/zerlok/hermod/internal/shell"
	"github.com/zerlok/hermod/internal/worker"
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

// The statuses worker's scripts exit with, as it passes them.
const (
	refusedStatus = 3
	unreadable    = 5
)

// mutagenNotFound is what Mutagen says of a name it has no session under.
const mutagenNotFound = "unable to locate requested sessions"

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
}

func newFake(syncs map[string]*mutagenSession, sessions map[string]*tmuxSession) *fake {
	if syncs == nil {
		syncs = map[string]*mutagenSession{}
	}
	if sessions == nil {
		sessions = map[string]*tmuxSession{}
	}
	return &fake{syncs: syncs, sessions: sessions}
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
	switch verb {
	case "list":
	case "create":
		f.record(verb, rest[1])
	default:
		f.record(verb, rest[0])
	}
	return func(_ context.Context, _ io.Reader, out, errOut io.Writer) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		if _, exists := f.syncs[rest[0]]; verb != "list" && verb != "create" && !exists {
			_, _ = fmt.Fprintf(errOut, "Error: %s\n", mutagenNotFound)
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
		return nil
	}
}

func (f *fake) list(rest []string, out, errOut io.Writer) error {
	var names []string
	switch {
	case len(rest) == 2 && rest[0] == "--label-selector":
		k, v, _ := strings.Cut(rest[1], "=")
		for name, s := range f.syncs {
			if s.labels[k] == v {
				names = append(names, name)
			}
		}
	case len(rest) == 1:
		if _, ok := f.syncs[rest[0]]; !ok {
			_, _ = fmt.Fprintf(errOut, "Error: %s\n", mutagenNotFound)
			return exitError(1)
		}
		names = []string{rest[0]}
	}
	sort.Strings(names)
	for _, name := range names {
		s := f.syncs[name]
		_, _ = fmt.Fprintf(out, "--------\nName: %s\nLabels:\n", name)
		for k, v := range s.labels {
			_, _ = fmt.Fprintf(out, "\t%s: %s\n", k, v)
		}
		status := "Watching for changes"
		if s.paused {
			status = "[Paused]"
		}
		_, _ = fmt.Fprintf(out, "Alpha:\n\tURL: %s\nBeta:\n\tURL: %s\nStatus: %s\n", s.local, s.beta, status)
	}
	return nil
}

// remoteWords is the command an ssh command line runs on the remote, split
// back into its words.
func remoteWords(args []string) []string {
	return splitWords(args[len(args)-1])
}

// splitWords splits a line the way a POSIX shell splits the words shell
// quotes: bare words and single-quoted parts.
func splitWords(line string) []string {
	var (
		words  []string
		cur    strings.Builder
		inWord bool
		quoted bool
	)
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quoted && c == '\'':
			quoted = false
		case quoted:
			cur.WriteByte(c)
		case c == '\'':
			quoted, inWord = true, true
		case c == '\\' && i+1 < len(line):
			i++
			cur.WriteByte(line[i])
			inWord = true
		case c == ' ':
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteByte(c)
			inWord = true
		}
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words
}

// sessionOf is the session name a target such as `=name:` names.
func sessionOf(target string) string {
	return strings.TrimSuffix(strings.TrimPrefix(target, "="), ":")
}

func (f *fake) onRemote(words []string) play {
	switch {
	case len(words) > 7 && words[0] == "sh" && words[1] == "-c" && words[3] == "act":
		return f.act(words[7], words[8:])
	case len(words) > 3 && words[0] == "sh" && words[1] == "-s" && len(words) > 7:
		return f.keep(words[3:])
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

// keep starts a worker as keep.sh does, from its arguments.
func (f *fake) keep(args []string) play {
	return func(_ context.Context, in io.Reader, _, _ io.Writer) error {
		_, _ = io.Copy(io.Discard, in)
		f.mu.Lock()
		defer f.mu.Unlock()
		name := args[0]
		if _, ok := f.sessions[name]; ok {
			return exitError(refusedStatus)
		}
		n, _ := strconv.Atoi(args[12])
		tags := map[string]string{}
		for i := 0; i < n; i++ {
			tags[args[13+2*i]] = args[14+2*i]
		}
		f.sessions[name] = &tmuxSession{state: worker.Running, tags: tags}
		f.ran = append(f.ran, "start "+name)
		return nil
	}
}

// act plays one operation of act.sh.
func (f *fake) act(op string, args []string) play {
	return func(ctx context.Context, in io.Reader, out, _ io.Writer) error {
		switch op {
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
			return exitError(unreadable)
		}
		switch op {
		case "list":
			tag := ""
			if fields := strings.Fields(args[0]); len(fields) == 3 {
				tag = strings.TrimSuffix(strings.TrimPrefix(fields[1], "#{"), "}")
			}
			var names []string
			for name := range f.sessions {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				s := f.sessions[name]
				dead := "0"
				if s.state == worker.Exited {
					dead = "1"
				}
				_, _ = fmt.Fprintf(out, "%s %s %s\n", dead, s.tags[tag], name)
			}
			_, _ = fmt.Fprintln(out, "end")
		case "close":
			name := sessionOf(args[0])
			if s, ok := f.sessions[name]; ok && s.diesOnClose {
				s.state = worker.Exited
			}
			if s, ok := f.sessions[name]; ok && s.state == worker.Exited {
				return exitError(refusedStatus)
			}
			delete(f.sessions, name)
			f.ran = append(f.ran, "close "+name)
		case "remove":
			name := sessionOf(args[0])
			if s, ok := f.sessions[name]; ok && s.state == worker.Running {
				return exitError(refusedStatus)
			}
			delete(f.sessions, name)
			f.ran = append(f.ran, "remove "+name)
		}
		return nil
	}
}
