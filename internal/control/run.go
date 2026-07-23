package control

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/zerlok/hermod/internal/git"
	"github.com/zerlok/hermod/internal/mirror"
	"github.com/zerlok/hermod/internal/notify"
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

	// The notification back-channel is best-effort: setupNotify returns a plain
	// session (identity-only env, empty spec, no-op stop) on any failure, so it
	// can never affect the sync-and-attach flow or the pause/teardown decision.
	env, reverseSpec, stopNotify := setupNotify(ctx, real, effective, o, identityEnv(gitIdentity), logger)
	defer func() { _ = stopNotify() }()

	remote := sandbox.NewSession(effective, sandbox.Config{
		Host:           o.Sandbox,
		Session:        o.Session,
		Dir:            o.RemoteDir,
		Command:        o.Command,
		Env:            env,
		ReverseForward: reverseSpec,
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

// setupNotify wires the notification back-channel when enabled, returning the
// session env (identity plus the channel address), the ssh -R spec, and a stop
// func that closes the local listener. It is best-effort: any failure logs and
// falls back to a plain session (base env, empty spec, no-op stop). Under dry-run
// it prints the plan — the -R spec and env ride the effective leaf through the
// sandbox stack — and logs a listener note without binding anything.
func setupNotify(ctx context.Context, real, effective shell.Shell, o Options, baseEnv []string, logger *log.Logger) (env []string, reverseSpec string, stop func() error) {
	noop := func() error { return nil }
	if !o.Notify {
		return baseEnv, "", noop
	}
	ch, err := openNotify(ctx, real, effective, o.Sandbox, logger)
	if err != nil {
		logger.Printf("notifications disabled: %v", err)
		return baseEnv, "", noop
	}
	env = append(append([]string(nil), baseEnv...), ch.Env()...)
	reverseSpec = ch.ReverseSpec()
	if o.DryRun {
		logger.Printf("# notify: would listen on %s and raise a desktop notification on each message", ch.LocalSock())
		return env, reverseSpec, noop
	}
	stop, err = ch.Listen(ctx)
	if err != nil {
		logger.Printf("notifications disabled: %v", err)
		return baseEnv, "", noop
	}
	return env, reverseSpec, stop
}

// openNotify provisions the back-channel endpoints and builds the local Channel.
// The remote base directory is discovered with a real probe (a probe that shapes
// the plan stays real under dry-run) and created through the dry-run-aware
// effective shell, reusing the remote-provisioning pattern; the local socket path
// is derived under the local runtime dir. A crypto-random per-session token gives
// both ends a unique socket name, so concurrent sessions never collide.
func openNotify(ctx context.Context, real, effective shell.Shell, host string, logger *log.Logger) (*notify.Channel, error) {
	token, err := randToken()
	if err != nil {
		return nil, err
	}
	name := notify.SocketName(token)

	remoteBase, err := probeRemoteBase(ctx, real, host)
	if err != nil {
		return nil, fmt.Errorf("probe remote runtime dir: %w", err)
	}
	remoteDir := remoteBase + "/hermod"
	if _, err := shell.NewSSH(effective, host, false).Run(ctx, shell.Command{
		Argv: []string{"mkdir", "-p", "-m", "700", remoteDir},
	}); err != nil {
		return nil, fmt.Errorf("create remote notify dir: %w", err)
	}
	remoteSock := remoteDir + "/" + name
	localSock := filepath.Join(localRuntimeDir(), "hermod", name)

	return notify.New(notify.NewNotifier(effective), localSock, remoteSock, logger), nil
}

// probeRemoteBase reads the sandbox's runtime directory (XDG_RUNTIME_DIR, or a
// per-user fallback under $HOME) with a read-only ssh probe.
func probeRemoteBase(ctx context.Context, real shell.Shell, host string) (string, error) {
	res, err := shell.NewSSH(real, host, false).Run(ctx, shell.Command{
		Argv:    []string{"sh", "-c", `printf %s "${XDG_RUNTIME_DIR:-$HOME/.hermod/run}"`},
		Capture: true,
	})
	if err != nil {
		return "", err
	}
	base := strings.TrimSpace(res.Stdout)
	if base == "" {
		return "", errors.New("empty remote runtime dir")
	}
	return base, nil
}

// randToken returns 8 bytes of crypto-random entropy as hex, the per-session
// socket-name token.
func randToken() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// localRuntimeDir is the base for the local notify socket: XDG_RUNTIME_DIR when
// set, else the OS temp dir.
func localRuntimeDir() string {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return d
	}
	return os.TempDir()
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
