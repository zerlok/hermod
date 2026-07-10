package control

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/zerlok/hermod/internal/git"
	"github.com/zerlok/hermod/internal/mirror"
	"github.com/zerlok/hermod/internal/sandbox"
	"github.com/zerlok/hermod/internal/shell"
)

// Run resolves the invocation by applying opts over the sandbox alias, then runs
// the flow. Under dry-run, side-effecting commands are printed to stdout as a
// copy-pasteable plan while probes still run for real.
func Run(ctx context.Context, host string, opts ...Option) error {
	o := Options{Sandbox: host}
	for _, opt := range opts {
		opt(&o)
	}

	logger := newLogger(o.Quiet)

	// real always executes, so plan-shaping probes stay true under dry-run;
	// effective is dry-run-aware, for side effects and the interactive attach.
	real := shell.NewLocal(false)
	effective := shell.NewLocal(o.DryRun)

	gitIdentity := git.New(real, o.LocalDir).Read(ctx)
	logger.Printf("git identity: %s", describe(gitIdentity))

	remote := sandbox.NewSession(effective, sandbox.Config{
		Host:    o.Sandbox,
		Session: o.Session,
		Dir:     o.RemoteDir,
		Command: o.Command,
		Env:     identityEnv(gitIdentity),
	})

	session, err := mirror.NewMutagenSession(ctx, effective, real, mirror.Config{
		Name:       o.Session,
		Host:       o.Sandbox,
		RemotePath: o.RemoteDir,
		LocalPath:  o.LocalDir,
	})
	if err != nil {
		return fmt.Errorf("open mirror: %w", err)
	}

	// Settling the mirror must outlive a cancelled ctx: a SIGINT during the attach
	// cancels ctx and kills the attach, but the mirror still has to be paused or
	// closed — never left leaking — so teardown runs on a detached context.
	settle := context.WithoutCancel(ctx)

	if err := session.Flush(ctx); err != nil {
		// Setup failed before any work; still settle the mirror by liveness so it
		// is never left leaking, then report the original failure.
		_ = teardown(settle, session, remote, logger)
		return fmt.Errorf("flush mirror: %w", err)
	}

	// Attach blocks until detach. A non-zero result is not consulted for the
	// mirror's fate — liveness is the only signal — but it is worth logging.
	if err := remote.Attach(ctx); err != nil {
		logger.Printf("attach ended with error: %v", err)
	}

	return teardown(settle, session, remote, logger)
}

// teardown decides the mirror's fate from sandbox-session liveness alone: pause it
// when the session is still running (coming back) or when liveness can't be
// determined (never terminate what might still be there); flush then close it
// only when the session is confirmed gone.
func teardown(ctx context.Context, session mirror.Session, sandbox sandbox.Session, logger *log.Logger) error {
	alive, err := sandbox.IsActive(ctx)
	if err != nil {
		logger.Printf("liveness check failed (%v); pausing to preserve", err)
		return session.Pause(ctx)
	}
	if alive {
		logger.Printf("session still running → pausing mirror")
		return session.Pause(ctx)
	}
	logger.Printf("session gone → flushing then terminating mirror")
	if err := session.Flush(ctx); err != nil {
		// The final flush pushes any unsynced edits before terminate discards the
		// session; a failure here risks losing them, so surface it. Termination
		// still proceeds — a live "gone" session is a leak we must not keep.
		logger.Printf("final flush before terminate failed: %v", err)
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

func newLogger(quiet bool) *log.Logger {
	var w io.Writer = os.Stderr
	if quiet {
		w = io.Discard
	}
	return log.New(w, "", 0)
}

func describe(id git.Identity) string {
	if id.IsZero() {
		return "unset (deferring to the sandbox's git config)"
	}
	return fmt.Sprintf("%s <%s>", id.Name, id.Email)
}
