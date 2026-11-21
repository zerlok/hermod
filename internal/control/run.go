package control

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"github.com/zerlok/hermod/internal/claude"
	"github.com/zerlok/hermod/internal/git"
	"github.com/zerlok/hermod/internal/mirror"
	"github.com/zerlok/hermod/internal/sandbox"
	"github.com/zerlok/hermod/internal/shell"
)

// ExitStatus is the exit code of a program Hermod ran on the sandbox in its
// caller's place. Hermod exits with the same code and adds nothing of its own,
// since the caller reads the program's output as if it had run it.
type ExitStatus int

func (e ExitStatus) Error() string { return fmt.Sprintf("exit status %d", int(e)) }

// Run resolves the invocation by applying opts over the sandbox alias, then runs
// what its command asks for (see claude.Classify). Under dry-run, side-effecting
// commands are printed as a copy-pasteable plan while probes still run for real.
func Run(ctx context.Context, host string, opts ...Option) error {
	return run(ctx, resolve(host, opts...), shell.NewLocal())
}

// run runs the invocation over the local leaf real.
func run(ctx context.Context, o Options, real shell.Shell) error {
	// effective is dry-run-aware, for side effects and the interactive attach;
	// real always executes, so plan-shaping probes stay true under dry-run.
	effective := real
	if o.DryRun {
		effective = shell.NewDryRun(o.ownOut())
	}
	call := claude.Classify(o.Command.Argv())
	switch call.Kind() {
	case claude.UtilityCall:
		return runUtility(ctx, effective, o.Sandbox, utilityDir(ctx, o, real), call, o.Stdio)
	case claude.TerminalSession, claude.ChatSession:
		return runAgent(ctx, o, effective, real, call, rand.Reader)
	case claude.NotAgentSession:
		return attach(ctx, o, effective, real)
	}
	return fmt.Errorf("unhandled command kind %v", call.Kind())
}

// utilityDir is the sandbox directory a utility call runs in: where an agent
// session in the same directory would run, which lies under a mirrored root
// that holds it rather than at the path the directory would be mirrored to on
// its own. A utility call needs no mirror, so a listing that cannot be read
// (no Mutagen here) does not fail it: it runs at that own path, which the
// sandbox side falls back from to home when it does not exist.
func utilityDir(ctx context.Context, o Options, real shell.Shell) string {
	listed, err := mirror.List(ctx, real, o.Sandbox)
	if err != nil {
		return o.RemoteDir
	}
	return mirror.Place(listed, o.LocalDir, o.RemoteDir).Dir
}

// runUtility runs a utility call straight on the sandbox with the caller's
// streams: no mirror, no tmux session, no hooks. It runs in the project's
// sandbox directory when that exists, so a settings read or `mcp list` sees the
// project's settings. A failed call's exit code is returned as an ExitStatus for
// Hermod to exit with.
func runUtility(ctx context.Context, effective shell.Shell, host, dir string, call claude.Call, stdio sandbox.Streams) error {
	err := sandbox.Run(ctx, effective, host, dir, call.Argv(), stdio)
	if err == nil {
		return nil
	}
	// A negative code is a process killed by a signal: no code to pass on.
	if code, ok := shell.ExitCode(err); ok && code > 0 {
		return ExitStatus(code)
	}
	return fmt.Errorf("run %s on %s: %w", call.Kind(), host, err)
}

// openSessionLog opens the session's log, <dir>/<uuid>.log, for appending. The
// log can hold the session's arguments and paths, so it is private to the user:
// the directory 0700, the file 0600.
func openSessionLog(dir string, id claude.SessionID) (*os.File, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("session log: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, id.String()+".log"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("session log: %w", err)
	}
	return f, nil
}

// attach mirrors the directory and attaches the sandbox session running the
// command, settling the mirror on detach. Only the attach itself writes to the
// caller's stdout; what the mirror's commands print goes to ownOut.
func attach(ctx context.Context, o Options, effective, real shell.Shell) error {
	logger := newLogger(o.Stdio.Err, o.Quiet)

	gitIdentity := git.New(real, o.LocalDir).Read(ctx)
	logger.Printf("git identity: %s", describe(gitIdentity))
	logger.Printf("origin: %s", describeOrigin(o.Origin))

	placement, err := place(ctx, o, real)
	if err != nil {
		return err
	}
	remote := sandbox.NewSession(effective, sandbox.Config{
		Host:    o.Sandbox,
		Session: o.Session,
		Dir:     placement.Dir,
		Command: o.Command,
		Env:     identityEnv(gitIdentity),
		Tags:    sandbox.Tags{Mode: sandbox.ModeAttach, Origin: o.Origin, Dir: placement.Dir},
	})
	mirrors := newRunMirrors(o, effective, real, logger)
	defer mirrors.close()
	session, err := mirrors.open(ctx, projectMirror(o.Sandbox, placement), placement.Root.Remote)
	if err != nil {
		return err
	}

	// Settling the mirror must outlive a cancelled ctx: a SIGINT during the attach
	// cancels ctx and kills the attach, but the mirror still has to be paused or
	// closed — never left leaking — so teardown runs on a detached context.
	settle := context.WithoutCancel(ctx)

	if err := session.Flush(ctx); err != nil {
		// Setup failed before any work; still settle the mirror by liveness so it
		// is never left leaking, then report the original failure.
		_ = mirrors.settle(settle, byLiveness)
		return fmt.Errorf("flush mirror: %w", err)
	}

	// Attach blocks until detach. A non-zero result is not consulted for the
	// mirror's fate — liveness is the only signal — but it is worth logging.
	if err := remote.Attach(ctx); err != nil {
		logger.Printf("attach ended with error: %v", err)
	}

	return mirrors.settle(settle, byLiveness)
}

// liveness is what the settle rule reads: whether any session under the
// mirror's root outlived the detach.
type liveness interface {
	IsActive(ctx context.Context) (bool, error)
}

// teardown decides the mirror's fate from the liveness of its root alone: pause
// it when a session under the root still runs (coming back) or when liveness
// can't be determined (never terminate what might still be there); flush then
// close it only when no session is left.
func teardown(ctx context.Context, name string, session mirror.Session, root liveness, logger *log.Logger) error {
	alive, err := root.IsActive(ctx)
	if err != nil {
		logger.Printf("mirror %s: liveness check failed (%v); pausing to preserve", name, err)
		return session.Pause(ctx)
	}
	if alive {
		logger.Printf("mirror %s: a session under its root still runs → pausing it", name)
		return session.Pause(ctx)
	}
	logger.Printf("mirror %s: no session under its root → flushing then terminating it", name)
	if err := session.Flush(ctx); err != nil {
		// The final flush pushes any unsynced edits before terminate discards the
		// session; a failure here risks losing them, so surface it. Termination
		// still proceeds — a live "gone" session is a leak we must not keep.
		logger.Printf("mirror %s: final flush before terminate failed: %v", name, err)
	}
	return session.Close(ctx)
}

// identityEnv renders a git identity as the environment carried into the sandbox
// session. Both author and committer are set: git derives the committer
// independently, so author-only would commit as the box (or fail on a box with
// no identity). Env-building lives here, not in the git package, because it is
// the orchestration's convention.
func identityEnv(id git.Identity) []string {
	var env []string
	if id.Name != "" {
		env = append(env, "GIT_AUTHOR_NAME="+id.Name, "GIT_COMMITTER_NAME="+id.Name)
	}
	if id.Email != "" {
		env = append(env, "GIT_AUTHOR_EMAIL="+id.Email, "GIT_COMMITTER_EMAIL="+id.Email)
	}
	return env
}

func newLogger(w io.Writer, quiet bool) *log.Logger {
	if quiet {
		w = io.Discard
	}
	return log.New(w, "", 0)
}

func describeOrigin(origin string) string {
	if origin == "" {
		return "none"
	}
	return origin
}

func describe(id git.Identity) string {
	if id.IsZero() {
		return "unset (deferring to the sandbox's git config)"
	}
	return fmt.Sprintf("%s <%s>", id.Name, id.Email)
}
