package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"sort"
	"strings"
	"syscall"
	"testing"

	"github.com/zerlok/hermod/internal/agent"
	"github.com/zerlok/hermod/internal/air"
	"github.com/zerlok/hermod/internal/session"
	"github.com/zerlok/hermod/internal/shell"
)

const (
	testCwd        = "/home/me/dev/api"
	testHome       = "/home/me"
	testHostname   = "laptop"
	testHermod     = "/home/me/go/bin/hermod"
	testRemoteHome = "/home/u"
)

// vars is a fixed environment; a key that is absent is unset.
type vars map[string]string

func (v vars) lookup(key string) (string, bool) {
	value, ok := v[key]
	return value, ok
}

func (v vars) environ() []string {
	var out []string
	for k, value := range v {
		out = append(out, k+"="+value)
	}
	sort.Strings(out)
	return out
}

var errNoRepo = errors.New("exit status 128")

// workstation plays the local leaf: the remote's answer to the read of its
// home over ssh, what another command prints by its command line, and a
// failure for any other, as git fails outside a repository. It records every
// command it runs.
type workstation struct {
	remoteOut string
	remoteErr error
	// remoteEnv, when set, plays the remote by a real sh in that environment:
	// it runs the command line ssh hands the remote, so the answer is what
	// the command prints there rather than remoteOut.
	remoteEnv []string
	answers   map[string]string
	ran       []string
}

func (w *workstation) Exec(_ context.Context, cmd shell.Command) shell.Execution {
	line := strings.Join(cmd.Args, " ")
	w.ran = append(w.ran, line)
	out, err := "", errNoRepo
	if answer, ok := w.answers[line]; ok {
		out, err = answer, nil
	}
	if len(cmd.Args) > 0 && cmd.Args[0] == "ssh" {
		out, err = w.remoteOut, w.remoteErr
		if w.remoteEnv != nil {
			out, err = runRemote(cmd.Args[len(cmd.Args)-1], w.remoteEnv)
		}
	}
	if cmd.Stdout != nil {
		_, _ = io.WriteString(cmd.Stdout, out)
	}
	return shell.Finished(cmd, err)
}

// runRemote runs line as ssh has the remote run it: by the user's shell, as
// one command line.
func runRemote(line string, environ []string) (string, error) {
	c := exec.Command("sh", "-c", line)
	c.Env = environ
	out, err := c.Output()
	return string(out), err
}

// planner is the dry-run leaf, told apart from the local one by its type.
type planner struct{ plan io.Writer }

func (planner) Exec(_ context.Context, cmd shell.Command) shell.Execution {
	return shell.Finished(cmd, nil)
}

// goneWriter is a stream whose reader has gone.
type goneWriter struct{}

func (goneWriter) Write([]byte) (int, error) { return 0, syscall.EPIPE }

// memLog is a log file in memory.
type memLog struct {
	bytes.Buffer
	closed bool
}

func (m *memLog) Close() error { m.closed = true; return nil }

// recorder records what the commands handed into the core, and what the
// process wrote.
type recorder struct {
	attached []session.Request
	chats    []session.ChatRequest
	// received are the kinds of the messages the chat's client connection
	// delivered while the core served it.
	received   []session.Kind
	configured []air.Request
	logs       map[string]*memLog
	stdout     bytes.Buffer
	stderr     bytes.Buffer
	local      *workstation
	stdin      io.Reader
}

// streamName names which of the process's streams s is, "" for none and
// "other" for one that is none of them.
func (r *recorder) streamName(s any) string {
	switch {
	case s == nil:
		return ""
	case s == r.stdin:
		return "stdin"
	case s == io.Writer(&r.stdout):
		return "stdout"
	case s == io.Writer(&r.stderr):
		return "stderr"
	}
	return "other"
}

// setup is what a test changes of the process.
type setup struct {
	env      vars
	terminal bool
	// say is written by the core, through the request's log, when a command
	// hands it a request.
	say string
	// logFails makes the stand-in's log impossible to open.
	logFails bool
	// stderrGone makes every write to stderr fail, as when its reader has
	// gone.
	stderrGone bool
	// answers is what the workstation prints for a command line.
	answers map[string]string
	// mutagenSays is written by the core, as an FS relay's Mutagen output,
	// to the request's Output when it has one.
	mutagenSays string
	// stdin is what the process reads; a chat's core reads every message of
	// it from the client's connection before it returns.
	stdin string
	// agent, when set, builds every agent in Claude's place.
	agent func(exec shell.Executor) agent.Agent
}

func newApp(t *testing.T, s setup) (app, *recorder) {
	t.Helper()
	r := &recorder{
		logs:  map[string]*memLog{},
		local: &workstation{remoteOut: testRemoteHome + "\n", answers: s.answers},
		stdin: strings.NewReader(s.stdin),
	}
	a := app{
		attach: func(ctx context.Context, req session.Request) error {
			r.attached = append(r.attached, req)
			if s.say != "" {
				req.Log.Print(s.say)
			}
			if req.Local.Output != nil {
				_, _ = io.WriteString(req.Local.Output, s.mutagenSays)
			}
			return nil
		},
		chat: func(_ context.Context, req session.ChatRequest) error {
			r.chats = append(r.chats, req)
			for msg := range req.Client.Receive() {
				r.received = append(r.received, msg.Kind())
			}
			return nil
		},
		configure: func(req air.Request) (air.Result, error) {
			r.configured = append(r.configured, req)
			entry, err := air.NewEntry(req.Name, req.Remote, req.Hermod, req.ChatArgs, req.Env)
			return air.Result{Entry: entry, RemoteLine: req.RemoteVariable + "=" + req.Remote}, err
		},
		local:      r.local,
		dryRun:     func(plan io.Writer) shell.Executor { return planner{plan: plan} },
		cwd:        func() (string, error) { return testCwd, nil },
		home:       func() (string, error) { return testHome, nil },
		hostname:   func() (string, error) { return testHostname, nil },
		lookupEnv:  s.env.lookup,
		environ:    s.env.environ,
		executable: func() (string, error) { return testHermod, nil },
		newAgent:   claudeAgent(func(string) ([]byte, error) { return nil, errors.New("no file") }),
		terminal:   func() bool { return s.terminal },
		openLog: func(name string) (io.WriteCloser, error) {
			if s.logFails {
				return nil, errors.New("session log: read-only file system")
			}
			l := &memLog{}
			r.logs[name] = l
			return l, nil
		},
	}
	if s.agent != nil {
		a.newAgent = s.agent
	}
	a.stdin, a.stdout, a.stderr = r.stdin, &r.stdout, &r.stderr
	if s.stderrGone {
		a.stderr = goneWriter{}
	}
	return a, r
}

// plainAgent is an agent that serves no chat client: it implements no
// agent.ACP. It answers nothing it is asked.
type plainAgent struct{}

func (plainAgent) Classify([]string) (agent.Invocation, string)               { return agent.InvokesCommand, "" }
func (plainAgent) List(context.Context, string) ([]agent.Conversation, error) { return nil, nil }
func (plainAgent) Lookup(context.Context, string) (agent.Conversation, error) {
	return agent.Conversation{}, agent.ErrUnknown
}
func (plainAgent) Start(context.Context, shell.Executor, agent.Launch) error { return nil }
func (plainAgent) StateDir(context.Context, string) (string, error)          { return "", nil }

// called is how many requests the commands handed into the core.
func (r *recorder) called() int { return len(r.attached) + len(r.chats) + len(r.configured) }
