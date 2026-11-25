package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/zerlok/hermod/internal/agent"
	"github.com/zerlok/hermod/internal/claude"
)

// standIn runs what `attach -- claude ARGS…` runs. Every argument is Claude's,
// so the remote comes from the environment alone, and HERMOD_REMOTE_CMD does
// not apply: the command is given. stdout belongs to Claude's caller, so all
// of Hermod's own output, the dry-run plan included, goes to stderr and the
// session's log.
//
// A conversation runs on a terminal only: without one, the caller is a
// program, which holds a conversation through `hermod air acp`. Only a
// one-shot call runs without one, since a call that names no conversation,
// such as a bare `claude`, still starts one that Claude names itself.
func (a app) standIn(ctx context.Context, claudeArgs []string) (err error) {
	remote, err := resolveRemote("", a.lookupEnv)
	if err != nil {
		return err
	}
	args := append([]string{claude.Program}, claudeArgs...)
	invocation, id := a.localAgent().Classify(args)
	if invocation != agent.InvokesOneShot && !a.terminal() {
		return errors.New("a conversation runs on a terminal only; a program holds one through `hermod " + strings.Join(chatArgs, " ") + "`")
	}
	l, err := a.readLocal()
	if err != nil {
		return err
	}
	timeout, err := resolveTeardownTimeout(nil, a.lookupEnv)
	if err != nil {
		return err
	}
	key := workerName(l.cwd, l.home)
	if invocation == agent.InvokesConversation {
		key = id
	}
	own, finish := a.sessionLog(filepath.Join(logDir(l.home, a.lookupEnv), key+".log"))
	defer func() { finish(err) }()
	return a.runAttach(ctx, attachIntent{remote: remote, args: args, teardown: timeout}, attachOutput{plan: own, logs: own, setup: own})
}

// sessionLog is where the stand-in writes its own output: the log at name and
// stderr. A log that cannot be opened is reported on stderr and the run goes
// on without it. finish records the run's error in the log, stderr getting it
// from the caller, and closes the log.
func (a app) sessionLog(name string) (own io.Writer, finish func(error)) {
	f, err := a.openLog(name)
	if err != nil {
		fmt.Fprintf(a.stderr, "hermod: %v\n", err)
		return a.stderr, func(error) {}
	}
	tee := &logTee{log: &timestamped{w: f}, err: a.stderr}
	return tee, func(err error) {
		if err != nil {
			_, _ = tee.writeLog([]byte(fmt.Sprintf("hermod: %v\n", err)))
		}
		_ = f.Close()
	}
}

// openLog opens a session's log for appending, private to the user.
func openLog(name string) (io.WriteCloser, error) {
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		return nil, fmt.Errorf("session log: %w", err)
	}
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("session log: %w", err)
	}
	return f, nil
}

// logTee writes Hermod's own output to the session log and then to stderr. A
// write never fails: the log comes first so a stderr whose reader has gone
// costs no line of it, and a failed write would otherwise fail what printed
// it, such as a Mutagen command whose output it carries. A Mutagen command
// writes its stdout and stderr at once, and the log's timestamps keep a state
// of the line, so the writes are taken one at a time.
type logTee struct {
	mu  sync.Mutex
	log io.Writer
	err io.Writer
}

func (t *logTee) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, _ = t.log.Write(p)
	_, _ = t.err.Write(p)
	return len(p), nil
}

// writeLog writes to the session log alone.
func (t *logTee) writeLog(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.log.Write(p)
}

// stampedLog is a log file whose lines are timestamped.
type stampedLog struct {
	*timestamped
	io.Closer
}

// timestamped prefixes each line written to the session log with the time,
// as the log of a long-lived session needs and stderr does not. A line may
// come in several writes, as a command's output does: only its start is
// stamped.
type timestamped struct {
	w       io.Writer
	now     func() time.Time // nil is time.Now
	midLine bool             // the last write ended inside a line
}

func (t *timestamped) Write(p []byte) (int, error) {
	now := t.now
	if now == nil {
		now = time.Now
	}
	written := 0
	for len(p) > 0 {
		if !t.midLine {
			if _, err := io.WriteString(t.w, now().Format("2006/01/02 15:04:05 ")); err != nil {
				return written, err
			}
		}
		line := p
		if i := bytes.IndexByte(p, '\n'); i >= 0 {
			line = p[:i+1]
		}
		n, err := t.w.Write(line)
		written += n
		if err != nil {
			return written, err
		}
		t.midLine = line[len(line)-1] != '\n'
		p = p[len(line):]
	}
	return written, nil
}
