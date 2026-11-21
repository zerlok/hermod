package control

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/zerlok/hermod/internal/air"
	"github.com/zerlok/hermod/internal/claude"
	"github.com/zerlok/hermod/internal/mirror"
	"github.com/zerlok/hermod/internal/sandbox"
	"github.com/zerlok/hermod/internal/shell"
)

// closeTimeout bounds one close of a session marked done: the hang-up, which
// waits up to 5 s for Claude, and the settle of its mirrors, whose final flush
// carries what the session changed while nobody was attached.
const closeTimeout = time.Minute

// Streams are a command's standard streams: sandbox.Streams, named here so
// the command line hands them in without depending on sandbox.
type Streams = sandbox.Streams

// ACPAdapter is the resolved intent of one `hermod air acp claude`: the ACP
// adapter to run in Air's place, and the sandbox whose sessions the chats Air
// marks done are closed on.
type ACPAdapter struct {
	Sandbox string
	Node    string // the Node binary that runs the adapter
	Script  string // the claude-agent-acp script
	// Stdio is Air's streams: stdin and stdout carry ACP and belong to the
	// adapter; Hermod writes its own lines to Stdio.Err only.
	Stdio Streams
	// SessionLogDir, when set, receives a log per session closed, <uuid>.log,
	// beside the stand-in's log of the same session.
	SessionLogDir string
	// LeaseDir holds the mirrors' lease files (see Options.LeaseDir).
	LeaseDir string
}

// RunACP runs the ACP adapter in Air's place. The adapter gets this process's
// environment unchanged, which it passes on to every claude it starts, and
// every message is copied between Air and it as it is. Of those messages one is
// acted on: `_session/archive`, Air's "mark done", which reaches only the
// adapter, closes that chat's session on the sandbox. Hermod exits with the
// adapter's code, once every close it began has finished.
func RunACP(ctx context.Context, a ACPAdapter) error {
	leaf := shell.NewLocal()
	o := a.closeOptions()
	return runACP(ctx, a, leaf, func(ctx context.Context, id claude.SessionID, reopened func() bool) error {
		return closeSession(ctx, o, leaf, id, reopened)
	})
}

// closeFunc closes the session of a chat marked done, unless reopened reports
// that Air has reopened the chat since.
type closeFunc func(ctx context.Context, id claude.SessionID, reopened func() bool) error

// closeOptions are the Options a close runs under: Air's stdout is the ACP
// stream, so Hermod's output goes to stderr as the stand-in's does.
func (a ACPAdapter) closeOptions() Options {
	return Options{
		Sandbox:       a.Sandbox,
		Stdio:         Streams{Err: a.Stdio.Err},
		StandIn:       true,
		SessionLogDir: a.SessionLogDir,
		LeaseDir:      a.LeaseDir,
	}
}

// runACP runs the adapter over leaf, calling closeOne for each session Air
// marks done.
func runACP(ctx context.Context, a ACPAdapter, leaf shell.Shell, closeOne closeFunc) error {
	logger := newLogger(a.Stdio.Err, false)
	// The adapter reads a pipe of its own rather than a reader, which os/exec
	// would copy from in a goroutine that outlives the adapter while Air's
	// stdin stays open.
	in, toAdapter, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("start the ACP adapter: %w", err)
	}
	defer in.Close()
	closes := newCloses(ctx, a.Sandbox, closeOne, logger)
	go func() {
		if err := copyMessages(toAdapter, orStdin(a.Stdio.In), closes.marked); err != nil {
			logger.Printf("ACP adapter: %v", err)
		}
		_ = toAdapter.Close()
	}()
	_, err = leaf.Run(ctx, shell.Command{
		Argv:   []string{a.Node, a.Script},
		Stdin:  in,
		Stdout: a.Stdio.Out,
		Stderr: a.Stdio.Err,
		// The adapter ends the claudes it started when it is asked to stop;
		// killed at once, it would leave them running, orphaned.
		Interrupt: syscall.SIGTERM,
	})
	closes.wait()
	switch {
	case err == nil:
		return nil
	case ctx.Err() != nil:
		// Stopped as asked, by the signal Air sent Hermod: however the adapter
		// ends on the SIGTERM passed on, by the signal or by an exit code of
		// its own, the stop is no failure.
		return nil
	}
	if code, ok := shell.ExitCode(err); ok && code > 0 {
		return ExitStatus(code)
	}
	return fmt.Errorf("ACP adapter: %w", err)
}

// copyMessages copies Air's messages to the adapter as they come, byte for
// byte, one line at a time, and hands each to seen once the adapter has it. A
// final line without its newline is copied as it is.
func copyMessages(dst io.Writer, src io.Reader, seen func(line []byte)) error {
	r := bufio.NewReader(src)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			if _, werr := dst.Write(line); werr != nil {
				return werr
			}
			seen(line)
		}
		switch {
		case errors.Is(err, io.EOF):
			return nil
		case err != nil:
			return err
		}
	}
}

// closes runs the closes of the sessions Air marks done, one at a time in the
// order Air marked them, since closes may share a mirror, and apart from the
// copy of messages, which never waits for one. A close outlives the cancelled
// context of a stopping run, within closeTimeout, so a chat marked done just
// before Air quits is still closed. A chat Air reopens is not closed after: its
// queued close is dropped, and one under way leaves the session running.
type closes struct {
	ctx      context.Context
	sandbox  string
	closeOne closeFunc
	logger   *log.Logger

	mu       sync.Mutex // guards what follows
	queue    []claude.SessionID
	working  bool             // a worker is draining the queue
	current  claude.SessionID // the session the worker is closing
	reopened bool             // Air has reopened current since it was marked done
	stopped  bool             // no more closes are taken
	done     sync.WaitGroup
}

func newCloses(ctx context.Context, sandbox string, closeOne closeFunc, logger *log.Logger) *closes {
	return &closes{ctx: context.WithoutCancel(ctx), sandbox: sandbox, closeOne: closeOne, logger: logger}
}

// marked acts on a message that marks a chat done or reopens it, if it does.
func (c *closes) marked(line []byte) {
	raw, mark, ok := air.Marked(line)
	if !ok {
		return
	}
	// The ACP session ID is the chat's Claude session UUID, which keys its
	// session on the sandbox.
	id, ok := claude.ParseSessionID(raw)
	if !ok {
		c.logger.Printf("chat %q marked: not a Claude session UUID, nothing done", raw)
		return
	}
	switch mark {
	case air.MarkDone:
		c.archived(id)
	case air.MarkReopened:
		c.unarchived(id)
	default:
		c.logger.Printf("chat %s: unhandled mark %d, nothing done", id, mark)
	}
}

// archived queues the close of a session marked done.
func (c *closes) archived(id claude.SessionID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped {
		return
	}
	c.queue = append(c.queue, id)
	if !c.working {
		c.working = true
		c.done.Add(1)
		go c.work()
	}
}

// unarchived keeps a reopened chat's session from being closed: the
// `session/resume` that follows the reopen finds the session closed when its
// close is done, and running when it is not, but a close still to come would
// end the session resumed.
func (c *closes) unarchived(id claude.SessionID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	kept := c.queue[:0]
	for _, queued := range c.queue {
		if queued != id {
			kept = append(kept, queued)
		}
	}
	c.queue = kept
	if c.working && c.current == id {
		c.reopened = true
	}
}

// work closes the queued sessions until none is left.
func (c *closes) work() {
	defer c.done.Done()
	for {
		c.mu.Lock()
		if len(c.queue) == 0 {
			c.working = false
			c.mu.Unlock()
			return
		}
		id := c.queue[0]
		c.queue = c.queue[1:]
		c.current, c.reopened = id, false
		c.mu.Unlock()
		c.closeQueued(id)
	}
}

func (c *closes) closeQueued(id claude.SessionID) {
	c.logger.Printf("chat %s marked done: closing its session on %s", id, c.sandbox)
	ctx, cancel := context.WithTimeout(c.ctx, closeTimeout)
	defer cancel()
	if err := c.closeOne(ctx, id, c.reopenedSince); err != nil {
		c.logger.Printf("close %s: %v", id, err)
	}
}

// reopenedSince reports whether Air has reopened the chat being closed since
// it was marked done.
func (c *closes) reopenedSince() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reopened
}

// wait takes no more closes and waits for those queued.
func (c *closes) wait() {
	c.mu.Lock()
	c.stopped = true
	c.mu.Unlock()
	c.done.Wait()
}

// closeSession closes an agent session on the sandbox: its tmux session goes,
// hanging up on Claude, which saves its transcript; its session directory
// stays, for a later --resume. Its mirrors, named by the directory its tmux
// session is tagged with, are opened under a lease before the hang-up and
// settled after it as a detach settles them, so a stand-in attached to another
// session on one of them keeps it. When the hang-up fails, they are paused:
// a Claude that may still run is never left without them, nor its edits lost.
// A chat reopened by the time of the hang-up is left running, and its mirrors
// settled as a detach settles them: the resumed chat's stand-in uses it.
//
// A session whose tmux session is already gone is not looked for further: the
// directory that names its mirrors is known only from its tmux session's tag,
// so they are left as the last detach settled them, for the next attach under
// their root to resume.
func closeSession(ctx context.Context, o Options, leaf shell.Shell, id claude.SessionID, reopened func() bool) (err error) {
	o, logger, finish := sessionLogger(o, id)
	defer func() { finish(err) }()
	agent := sandbox.NewAgent(leaf, leaf, sandbox.AgentConfig{Host: o.Sandbox, ID: id})
	tagged, err := agent.Tagged(ctx)
	if err != nil {
		return err
	}
	if !tagged.Exists {
		logger.Printf("%s does not run on %s: nothing to close", agent.TmuxName(), o.Sandbox)
		return nil
	}
	mirrors := newRunMirrors(o, leaf, leaf, logger)
	defer mirrors.close()
	opened := openServing(ctx, o, leaf, mirrors, tagged, logger)
	if reopened() {
		logger.Printf("chat %s reopened: %s left running", id, agent.TmuxName())
		return errors.Join(opened, mirrors.settle(ctx, byLiveness))
	}
	closeErr := agent.Close(ctx)
	rule := byLiveness
	if closeErr == nil {
		logger.Printf("closed %s; its session directory stays for --resume", agent.TmuxName())
	} else {
		rule = afterFailedHangUp
	}
	return errors.Join(closeErr, opened, mirrors.settle(ctx, rule))
}

// openServing opens the mirrors that serve the session's tagged directory, each
// under a lease, the project mirror kept alive by its root's sessions and the
// transcript mirror by the directory's. A mirror that cannot be listed or
// opened is left as it is, and the session is closed all the same.
func openServing(ctx context.Context, o Options, leaf shell.Shell, mirrors *runMirrors, tagged sandbox.Tagged, logger *log.Logger) error {
	if tagged.Dir == "" {
		logger.Printf("the session is tagged with no directory: its mirrors are left as they are")
		return nil
	}
	listed, err := mirror.List(ctx, leaf, o.Sandbox)
	if err != nil {
		return err
	}
	var errs []error
	for _, cfg := range mirror.Serving(listed, o.Sandbox, tagged.Home, tagged.Dir) {
		root := tagged.Dir
		if cfg.Kind == mirror.Project {
			root = cfg.RemotePath
		}
		if _, err := mirrors.open(ctx, cfg, root); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
