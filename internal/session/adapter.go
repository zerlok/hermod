package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"

	"github.com/zerlok/hermod/internal/agent"
	"github.com/zerlok/hermod/internal/env"
	"github.com/zerlok/hermod/internal/relay"
	"github.com/zerlok/hermod/internal/worker"
	"github.com/zerlok/hermod/internal/workspace"
)

// Adapters attaches a chat client to the workspaces its directories belong
// to, each through the workspace's one adapter of the agent program. Its
// Attach is Chat's AttachFunc; the command line fills it in.
type Adapters struct {
	Remote workspace.Remote
	Local  workspace.Local
	Agent  agent.Agent

	Environ env.Vars // the caller's environment, of which the adapter gets only what the agent declares
	// Identity is the developer's git identity in a local directory, which
	// an adapter started there commits as.
	Identity func(ctx context.Context, dir string) Identity
	// MainWorktree is the main working tree of the linked git worktree that
	// holds a local directory, with that worktree's root; ok is false for a
	// directory in no linked worktree.
	MainWorktree func(ctx context.Context, dir string) (main, root string, ok bool)
	// Connect is the connection to an adapter over its output and its input.
	Connect func(fromAdapter io.Reader, toAdapter io.Writer) Channel

	// Errors opens the log of what the adapter of key writes to its stderr.
	// nil, or a log that cannot be opened, leaves those lines to Log.
	Errors func(key worker.Key) (io.WriteCloser, error)

	Log    *log.Logger // nil discards
	Random io.Reader   // hook tokens and ports; nil is crypto/rand
}

// Attach attaches the workspace dir belongs to and its adapter, a line-stream
// worker it starts when none runs: an Exited one is removed and started
// again. The adapter's connection ends when the workspace is detached, or
// when the relay that carries it stops serving.
func (a Adapters) Attach(ctx context.Context, dir Path) (Attachment, error) {
	local := string(dir)
	var tree workspace.Worktree
	if main, root, ok := a.MainWorktree(ctx, local); ok {
		tree = workspace.Worktree{Main: main, Root: root}
	}
	ws, err := workspace.Open(ctx, a.Remote, a.Local, local, tree)
	if err != nil {
		return Attachment{}, err
	}
	att, err := a.attach(ctx, ws, local)
	if err != nil {
		return Attachment{}, errors.Join(err, ws.Detach(ctx))
	}
	return att, nil
}

func (a Adapters) attach(ctx context.Context, ws *workspace.Workspace, dir string) (Attachment, error) {
	req := Request{
		Remote: a.Remote, Local: a.Local, Dir: ws.Root(), Agent: a.Agent,
		Environ: a.Environ, Identity: a.Identity(ctx, dir), Log: a.Log, Random: a.Random,
	}
	logger := req.logger()
	for _, p := range ws.Nested() {
		logger.Printf("workspace %s holds the workspace of %s, whose files are now carried twice", ws.Root(), p.Local)
	}
	key, err := ws.AdapterKey()
	if err != nil {
		return Attachment{}, err
	}
	seen, err := ws.Look(ctx, key, nil, portFile, tokenFile)
	if err != nil {
		return Attachment{}, err
	}
	act, err := decide(states{worker: seen.State})
	if err != nil {
		return Attachment{}, fmt.Errorf("%s: %w", key, err)
	}
	logger.Printf("adapter %s: %s", key, act)
	needs, err := a.Agent.Adapter(ws.PathMap())
	if err != nil {
		return Attachment{}, err
	}
	if err := apply(ctx, req, ws, key, act, needs, seen, worker.LineStream); err != nil {
		return Attachment{}, err
	}

	errs, closeErrs := a.errors(key, logger)
	toAdapter, input := io.Pipe()
	output, fromAdapter := io.Pipe()
	wait, err := ws.Join(ctx, key, worker.LineStream, relay.Streams{In: toAdapter, Out: fromAdapter, Err: errs})
	if err != nil {
		closeErrs()
		return Attachment{}, err
	}
	go func() {
		err := left(ctx, wait())
		closeErrs()
		_ = fromAdapter.CloseWithError(err)
		_ = toAdapter.CloseWithError(err)
	}()
	return Attachment{
		Workspace: ws,
		Root:      Path(ws.Root()),
		Paths:     NewPathMap(ws.PathMap()),
		Adapter:   a.Connect(output, input),
		Started:   act != join,
	}, nil
}

// errors is where the adapter of key writes its stderr, and how to close it
// once its relay has stopped. A log that cannot be opened costs only itself:
// the lines go to logger instead.
func (a Adapters) errors(key worker.Key, logger *log.Logger) (io.Writer, func()) {
	if a.Errors == nil {
		return logger.Writer(), func() {}
	}
	f, err := a.Errors(key)
	if err != nil {
		logger.Printf("adapter %s: %v", key, err)
		return logger.Writer(), func() {}
	}
	return f, func() { _ = f.Close() }
}
