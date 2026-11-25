package session

import (
	"context"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/zerlok/hermod/internal/agent"
	"github.com/zerlok/hermod/internal/pathmap"
	"github.com/zerlok/hermod/internal/relay/relaytest"
	"github.com/zerlok/hermod/internal/secret"
	"github.com/zerlok/hermod/internal/shell"
	"github.com/zerlok/hermod/internal/worker"
	"github.com/zerlok/hermod/internal/worker/workertest"
	"github.com/zerlok/hermod/internal/workspace"
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

// sshFailed is ssh's own exit status, for a connection that dropped or a
// forward it could not bind.
const sshFailed = 255

// mutagenSession is one Mutagen session the fake keeps.
type mutagenSession struct {
	local, beta string // beta as host:path
	paused      bool
	labels      map[string]string
}

// tmuxSession is one tmux session on the fake remote.
type tmuxSession struct {
	state worker.State
	tags  map[string]string
}

// ending is how a joined terminal ends.
type ending int

const (
	waits   ending = iota // until the session stops it
	ends                  // at once, as for a process that ended
	drops                 // with ssh's failure, as for a dropped connection
	signals               // the process is signalled while it waits
	killed                // the process is signalled and its ssh fails as the signal kills it
)

// fake plays the local workstation's Mutagen and, over ssh, the remote's tmux
// as the worker scripts drive it, the agent program's probe and a one-shot
// call. It records every command that changes a state as one line, naming an
// FS relay by its local end.
type fake struct {
	mu           sync.Mutex
	syncs        map[string]*mutagenSession
	sessions     map[string]*tmuxSession
	dirs         map[string]map[string]string // the workers' directories under the remote home
	probe        string                       // what the agent program says of the conversation
	listFails    bool                         // Mutagen cannot list its sessions
	attach       ending
	signal       context.CancelFunc   // stops the session when a joined terminal signals
	forwardFails bool                 // a forward's remote port is taken
	exitCode     int                  // a one-shot call's exit status
	convs        []agent.Conversation // the conversations the agent program has, by remote directory
	queryFails   bool                 // the agent program's listing and lookup fail
	queryGarbled bool                 // the agent program's listing and lookup print what cannot be read
	workersFail  bool                 // tmux cannot list its sessions
	streamWaits  bool                 // a joined line stream serves until it is stopped, rather than ending at once
	adapterSays  string               // what a joined line stream's process wrote to its stderr
	forms        map[string]string    // the form each worker was started in, by key
	joins        []string             // the form of each join, in order
	ran          []string
}

func newFake() *fake {
	return &fake{
		syncs: map[string]*mutagenSession{}, sessions: map[string]*tmuxSession{},
		dirs: map[string]map[string]string{}, forms: map[string]string{},
	}
}

func (f *fake) remote() workspace.Remote {
	return workspace.Remote{Host: testHost, Home: testRemoteHome, Exec: f, Read: f}
}

func testLocal(t *testing.T) workspace.Local {
	return workspace.Local{Home: testLocalHome, Locks: t.TempDir(), Origin: "laptop"}
}

// changes is what changed a state, in order.
func (f *fake) changes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.ran...)
}

func (f *fake) record(words ...string) {
	f.ran = append(f.ran, strings.Join(words, " "))
}

type play func(ctx context.Context, in io.Reader, out, errOut io.Writer) error

func (f *fake) Exec(ctx context.Context, cmd shell.Command) shell.Execution {
	var p play
	if args := cmd.Args; len(args) > 0 && args[0] == "ssh" {
		p = f.onRemote(workertest.Words(args[len(args)-1]), forwarded(args))
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

// forwarded is the remote port an ssh command line forwards, "" for none.
func forwarded(args []string) string {
	for i, a := range args {
		if a == "-R" && i+1 < len(args) {
			port, _, _ := strings.Cut(args[i+1], ":")
			return port
		}
	}
	return ""
}

func outPipe(given io.Writer) (io.Writer, io.ReadCloser, func()) {
	if given != nil {
		return given, nil, func() {}
	}
	r, w := io.Pipe()
	return w, r, func() { _ = w.Close() }
}

func (f *fake) onLocal(args []string) play {
	if len(args) < 3 || args[0] != "mutagen" {
		return func(context.Context, io.Reader, io.Writer, io.Writer) error { return nil }
	}
	verb, rest := args[2], args[3:]
	return func(_ context.Context, _ io.Reader, out, errOut io.Writer) error {
		f.mu.Lock()
		defer f.mu.Unlock()
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
			f.record(verb, s.local)
			return nil
		}
		s, ok := f.syncs[rest[0]]
		if !ok {
			relaytest.NotFound(errOut)
			return exitError(1)
		}
		f.record(verb, s.local)
		switch verb {
		case "pause":
			s.paused = true
		case "resume":
			s.paused = false
		case "terminate":
			delete(f.syncs, rest[0])
		}
		return nil
	}
}

func (f *fake) list(rest []string, out, errOut io.Writer) error {
	if f.listFails {
		_, _ = fmt.Fprintln(errOut, "Error: unable to connect to daemon")
		return exitError(1)
	}
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

func (f *fake) onRemote(words []string, port string) play {
	switch {
	case len(words) == 5 && words[0] == "sh" && words[2] == `exec "$SHELL" -lc "$1"`:
		inner := workertest.Words(words[4])
		if len(inner) == 2 && (inner[0] == agentList || inner[0] == agentLocate) {
			return f.query(inner[0], inner[1])
		}
		return f.oneShot(inner)
	}
	switch c, isWorker := workertest.Read(words); {
	case isWorker && c.Script == "act":
		return f.act(c)
	case isWorker && c.Script == "write":
		return f.write(c.Args[0], c.Args[1:])
	case isWorker && c.Script == "keep":
		return f.keep(c)
	case len(words) == 3 && words[0] == "sh" && strings.HasPrefix(words[2], "echo hermod-net-ready"):
		return f.forward(port)
	}
	return func(_ context.Context, in io.Reader, _, _ io.Writer) error {
		_, err := io.Copy(io.Discard, in)
		return err
	}
}

// oneShot runs a call in the directory it is given, recording where.
func (f *fake) oneShot(words []string) play {
	return func(context.Context, io.Reader, io.Writer, io.Writer) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.record(append([]string{"run in", words[4]}, words[5:]...)...)
		if f.exitCode != 0 {
			return exitError(f.exitCode)
		}
		return nil
	}
}

// The commands fakeAgent declares for its listing and its lookup.
const (
	agentList   = "agent-list"
	agentLocate = "agent-locate"
)

// query answers the agent program's listing of a directory, every
// conversation in it or under it, or its lookup of one conversation, a line
// "ID DIR STATE" for each.
func (f *fake) query(verb, arg string) play {
	return func(_ context.Context, _ io.Reader, out, errOut io.Writer) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.queryFails {
			_, _ = fmt.Fprintln(errOut, "claude: command not found")
			return exitError(127)
		}
		if f.queryGarbled {
			_, _ = fmt.Fprintln(out, "garbled")
			return nil
		}
		for _, c := range f.convs {
			_, under := pathmap.Under(c.Dir, arg)
			if (verb == agentList && under) || (verb == agentLocate && c.ID == arg) {
				_, _ = fmt.Fprintf(out, "%s %s %s\n", c.ID, c.Dir, c.State)
			}
		}
		return nil
	}
}

// forward carries a Net relay's forward until the relay stops it.
func (f *fake) forward(port string) play {
	return func(ctx context.Context, _ io.Reader, out, _ io.Writer) error {
		f.mu.Lock()
		f.record("forward", port)
		fails := f.forwardFails
		f.mu.Unlock()
		if fails {
			return exitError(sshFailed)
		}
		_, _ = fmt.Fprintln(out, "hermod-net-ready")
		<-ctx.Done()
		return nil
	}
}

// write makes a staging directory of the files on stdin.
func (f *fake) write(staging string, pairs []string) play {
	return func(_ context.Context, in io.Reader, _, _ io.Writer) error {
		b, _ := io.ReadAll(in)
		f.mu.Lock()
		defer f.mu.Unlock()
		files := map[string]string{}
		names := []string{"write"}
		for i := 0; i+1 < len(pairs); i += 2 {
			n, _ := strconv.Atoi(pairs[i+1])
			files[pairs[i]], b = string(b[:n]), b[n:]
			names = append(names, pairs[i])
		}
		f.dirs[staging] = files
		f.record(names...)
		return nil
	}
}

// keep starts a worker as keep.sh does.
func (f *fake) keep(c workertest.Command) play {
	return func(_ context.Context, in io.Reader, _, _ io.Writer) error {
		_, _ = io.Copy(io.Discard, in)
		f.mu.Lock()
		defer f.mu.Unlock()
		name := c.Session
		if _, ok := f.sessions[name]; ok {
			return exitError(c.Refused)
		}
		f.sessions[name] = &tmuxSession{state: worker.Running, tags: c.Start.Tags}
		f.dirs[c.Start.WorkerDir] = f.dirs[c.Start.Staging]
		delete(f.dirs, c.Start.Staging)
		f.forms[name] = c.Start.Form
		f.record("start in", c.Start.Dir, name)
		return nil
	}
}

// act plays one operation of act.sh.
func (f *fake) act(c workertest.Command) play {
	return func(ctx context.Context, _ io.Reader, out, _ io.Writer) error {
		switch c.Op {
		case "attach":
			f.mu.Lock()
			f.joins = append(f.joins, worker.Terminal.String())
			f.mu.Unlock()
			return f.join(ctx)
		case "feed", "log":
			if c.Op == "log" && path.Base(c.Args[1]) == "err.log" {
				f.mu.Lock()
				says := f.adapterSays
				f.mu.Unlock()
				_, _ = io.WriteString(out, says)
			}
			return f.stream(ctx, c.Op)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		switch c.Op {
		case "list":
			if f.workersFail {
				return exitError(c.Failed)
			}
			var names []string
			for name := range f.sessions {
				names = append(names, name)
			}
			sort.Strings(names)
			var listed []workertest.Listed
			for _, name := range names {
				s := f.sessions[name]
				tags := s.tags
				if tags == nil {
					tags = map[string]string{}
					for _, o := range c.Listed {
						tags[o] = f.workspaceOf()
					}
				}
				listed = append(listed, workertest.Listed{Name: name, Dead: s.state == worker.Exited, Tags: tags})
			}
			_, _ = io.WriteString(out, c.List(listed))
		case "look":
			state := "absent"
			if s, ok := f.sessions[c.Session]; ok {
				state = dead(s.state)
			}
			_, _ = fmt.Fprintf(out, "state %s\n", state)
			for _, name := range c.Look.Files {
				if content, ok := f.dirs[c.Look.WorkerDir][name]; ok {
					_, _ = fmt.Fprintf(out, "file %s %d\n%s", name, len(content), content)
				}
			}
			_, _ = fmt.Fprintln(out, "out")
			if c.Look.Line != "" {
				_, _ = io.WriteString(out, f.probe)
			}
		case "port":
			_, _ = fmt.Fprintln(out, c.Ports[0])
		case "close":
			name := c.Session
			if s, ok := f.sessions[name]; ok && s.state == worker.Exited {
				return exitError(c.Refused)
			}
			delete(f.sessions, name)
			f.record("close", name)
		case "remove":
			name := c.Session
			if s, ok := f.sessions[name]; ok && s.state == worker.Running {
				return exitError(c.Refused)
			}
			delete(f.sessions, name)
			f.record("remove", name)
		}
		return nil
	}
}

// workspaceOf is the workspace of the FS relays there, which a worker a test
// made without tags belongs to.
func (f *fake) workspaceOf() string {
	var names []string
	for _, s := range f.syncs {
		names = append(names, s.labels["hermod-workspace"])
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "-"
	}
	return names[0]
}

// stream is one command of a joined line stream: the feed of its FIFO, which
// marks the join, or the read of one of its logs. Each ends at once, as for a
// process that ended, unless the fake serves the stream until it is stopped.
func (f *fake) stream(ctx context.Context, op string) error {
	f.mu.Lock()
	if op == "feed" {
		f.joins = append(f.joins, worker.LineStream.String())
	}
	waits := f.streamWaits
	f.mu.Unlock()
	if waits {
		<-ctx.Done()
	}
	return nil
}

// joined is the form of each join, in order.
func (f *fake) joined() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.joins...)
}

// startedForm is the form the worker of key was started in, "" for none.
func (f *fake) startedForm(key string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.forms[key]
}

// join is a joined terminal, ending as the fake was told.
func (f *fake) join(ctx context.Context) error {
	f.mu.Lock()
	how, signal := f.attach, f.signal
	f.mu.Unlock()
	switch how {
	case ends:
		return nil
	case drops:
		return exitError(sshFailed)
	case signals:
		signal()
	case killed:
		signal()
		<-ctx.Done()
		return exitError(sshFailed)
	}
	<-ctx.Done()
	return nil
}

func dead(s worker.State) string {
	if s == worker.Exited {
		return "1"
	}
	return "0"
}

// fakeAgent answers the agent's questions from what a test gave it.
type fakeAgent struct {
	call      agent.Call
	needs     agent.Needs
	handshake agent.Handshake
}

func (a fakeAgent) Parse([]string) agent.Call { return a.call }

// Run gives a one-shot call its arguments mapped word by word and declares
// the variables named AGENT_*, as an agent's one-shot needs differ from its
// arguments.
func (a fakeAgent) Run(call agent.Call, paths pathmap.Map) (agent.Needs, error) {
	if !call.OneShot() {
		return a.needs, nil
	}
	var command []string
	for _, word := range call.Args() {
		command = append(command, paths.ToRemote(word))
	}
	return agent.NewNeeds(command).WithVars(func(key string) bool { return strings.HasPrefix(key, "AGENT_") }), nil
}

func (a fakeAgent) Adapter(pathmap.Map) (agent.Needs, error) { return agent.Needs{}, nil }

func (a fakeAgent) Probe(id string) agent.Probe {
	return agent.NewProbe(id, []string{"probe", id}, func(out []byte) (agent.State, error) {
		switch strings.TrimSpace(string(out)) {
		case "running":
			return agent.Running, nil
		case "stopped":
			return agent.Stopped, nil
		}
		return agent.Stopped, fmt.Errorf("unreadable %q", out)
	})
}

func (a fakeAgent) Connect() agent.Handshake { return a.handshake }

func (a fakeAgent) List(dir string) agent.Listing {
	return agent.NewListing(dir, []string{agentList, dir}, readConversations)
}

func (a fakeAgent) Locate(id string) agent.Lookup {
	return agent.NewLookup(id, []string{agentLocate, id}, func(out []byte) (agent.Conversation, bool, error) {
		found, err := readConversations(out)
		if err != nil || len(found) == 0 {
			return agent.Conversation{}, false, err
		}
		return found[0], true, nil
	})
}

// readConversations reads the fake program's lines "ID DIR STATE".
func readConversations(out []byte) ([]agent.Conversation, error) {
	var found []agent.Conversation
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 3 {
			return nil, fmt.Errorf("unreadable %q", line)
		}
		c := agent.Conversation{ID: fields[0], Dir: fields[1]}
		switch fields[2] {
		case agent.Running.String():
			c.State = agent.Running
		case agent.Stopped.String():
			c.State = agent.Stopped
		default:
			return nil, fmt.Errorf("unreadable state %q", fields[2])
		}
		found = append(found, c)
	}
	return found, nil
}

// Hooks stamps the port and the token on the document, so a test reads what
// the worker's settings carry.
func (a fakeAgent) Hooks(document secret.Token, port int, token secret.Token) (secret.Token, error) {
	return secret.FromString(fmt.Sprintf("%s port=%d token=%s", document.Reveal(), port, token.Reveal())), nil
}
