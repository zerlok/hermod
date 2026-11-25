// Package session attaches a local client to a workspace on a remote. It is
// the only package that decides from states: what to do for a pair of an
// agent's and a worker's states, and where a chat's message goes. It reaches
// relays and workers only through workspace, and an agent only through
// agent.Agent and agent.ACP, whose operations the agent performs itself; a
// start is handed the workspace's Host, so the agent runs and the workspace
// hosts. It keeps nothing past the process.
package session

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"strconv"
	"strings"

	"github.com/zerlok/hermod/internal/agent"
	"github.com/zerlok/hermod/internal/env"
	"github.com/zerlok/hermod/internal/pathmap"
	"github.com/zerlok/hermod/internal/secret"
	"github.com/zerlok/hermod/internal/shell"
	"github.com/zerlok/hermod/internal/workspace"
)

// Request is one attach, as the command line resolved it.
type Request struct {
	Remote workspace.Remote
	Local  workspace.Local
	Dir    string             // the local directory the session works in, absolute
	Tree   workspace.Worktree // the linked git worktree Dir lies in; zero for none

	// Agent classifies Args, looks a conversation up and starts it, answering
	// for the remote; nil runs Args as a command no agent runs.
	Agent agent.Agent
	// LocalAgent is the same program answering for this workstation, asked
	// only where it keeps a directory's conversations, so those of the
	// directory reach the remote before Agent starts one.
	LocalAgent agent.Agent
	// IsHook tells the client's hook endpoint by a hook's URL; nil aims no
	// hook at the client.
	IsHook func(u *url.URL) bool
	// Args is the command, program first; none runs the remote user's login
	// shell.
	Args []string
	// Name is the key of the worker of a command no agent runs. A
	// conversation's worker is keyed by the conversation.
	Name string
	// Forwards are the user's own Net relays, unfiltered.
	Forwards []Forward

	Environ  env.Vars // the caller's environment, of which a worker gets only what its agent declares
	Identity Identity
	Streams  workspace.Streams

	Log    *log.Logger // nil discards
	Random io.Reader   // hook tokens and ports; nil is crypto/rand
}

// Forward is a Net relay of the user's own: calls to Port on the remote reach
// Address on this workstation.
type Forward struct {
	Port    int
	Address string // host:port
}

// Identity is the developer's git identity, which a worker commits as.
type Identity struct {
	Name  string
	Email string
}

// vars are the identity as git reads it from the environment. Both author and
// committer are set, since git derives the committer on its own and would
// commit as the remote's user, or fail on a remote that has none.
func (i Identity) vars() []env.Pair {
	var pairs []env.Pair
	if i.Name != "" {
		pairs = append(pairs, env.Pair{Key: "GIT_AUTHOR_NAME", Value: env.Plain(i.Name)}, env.Pair{Key: "GIT_COMMITTER_NAME", Value: env.Plain(i.Name)})
	}
	if i.Email != "" {
		pairs = append(pairs, env.Pair{Key: "GIT_AUTHOR_EMAIL", Value: env.Plain(i.Email)}, env.Pair{Key: "GIT_COMMITTER_EMAIL", Value: env.Plain(i.Email)})
	}
	return pairs
}

// record is the record of a worker that is given the developer's git
// identity and nothing else.
func (i Identity) record() workspace.Record {
	return workspace.Record{Vars: env.New(i.vars()...)}
}

func (r Request) logger() *log.Logger { return orDiscardLog(r.Log) }

// orDiscardLog is l, or a logger that discards when l is nil.
func orDiscardLog(l *log.Logger) *log.Logger {
	if l == nil {
		return log.New(io.Discard, "", 0)
	}
	return l
}

func (r Request) random() io.Reader {
	if r.Random == nil {
		return rand.Reader
	}
	return r.Random
}

// ExitStatus is the exit code of a program run on the remote in the caller's
// place. The caller exits with it and adds nothing of its own, since its own
// caller reads the program's output as if it had run the program.
type ExitStatus int

func (e ExitStatus) Error() string { return fmt.Sprintf("exit status %d", int(e)) }

// The files of a hooked worker's directory that keep its Net relay's port,
// its token and the routes its hooks call for as long as the worker exists:
// its agent's settings carry the first two, and every session that attaches
// it later serves the same ones.
const (
	portFile   = "hook-port"
	tokenFile  = "hook-token"
	routesFile = "hook-routes"
)

// Hook ports are drawn below Linux's default ephemeral range (32768–60999),
// where outgoing connections would otherwise take them.
const (
	hookPortMin        = 10000
	hookPortMax        = 32767
	hookPortCandidates = 16
)

// Attach runs one session: it opens the workspace the directory belongs to,
// reads the worker's and the agent's states, decides from them, starts the
// worker when the decision says so, attaches the client to it and detaches
// the workspace when the attach ends. A one-shot call runs on the remote
// instead, with no workspace. A detach that the client caused, by leaving, a
// signal or the worker's end, returns nil, so the caller exits at once and
// nothing has to kill it.
func Attach(ctx context.Context, req Request) error {
	invocation, id := agent.InvokesCommand, ""
	if req.Agent != nil {
		invocation, id = req.Agent.Classify(req.Args)
	}
	var (
		key workspace.Key
		err error
	)
	switch invocation {
	case agent.InvokesOneShot:
		return oneShot(ctx, req)
	case agent.InvokesConversation:
		key, err = workspace.ConversationKey(id)
	case agent.InvokesCommand:
		key, err = workspace.NewKey(req.Name)
	default:
		return fmt.Errorf("session: unhandled invocation %s", invocation)
	}
	if err != nil {
		return err
	}
	ws, err := workspace.Open(ctx, req.Remote, req.Local, req.Dir, req.Tree)
	if err != nil {
		return err
	}
	for _, p := range ws.Nested() {
		req.logger().Printf("workspace %s holds the workspace of %s, whose files are now carried twice", ws.Root(), p.Local)
	}
	err = run(ctx, req, ws, id, key)
	return errors.Join(err, ws.Detach(ctx))
}

// run is a session on an opened workspace, up to the end of its attach; id is
// the conversation's, "" for a command no agent runs.
func run(ctx context.Context, req Request, ws *workspace.Workspace, id string, key workspace.Key) error {
	seen, err := ws.Look(ctx, key, portFile, tokenFile, routesFile)
	if err != nil {
		return err
	}
	st := states{worker: seen.State}
	if id != "" {
		st.runsAgent = true
		conv, err := req.Agent.Lookup(ctx, id)
		switch {
		case errors.Is(err, agent.ErrUnknown):
			// A conversation the program does not have has not started yet.
			st.agent = agent.Stopped
		case err != nil:
			return err
		default:
			st.agent = conv.State
		}
	}
	act, err := decide(st)
	if err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	req.logger().Printf("worker %s: %s", key, act)

	var aimed *hooks
	err = carryOut(ctx, ws, key, act, func() error {
		var err error
		aimed, err = startWorker(ctx, req, ws, key, id)
		return err
	})
	if err != nil {
		return err
	}
	if act == join && id != "" {
		aimed = rejoinHooks(ctx, req, ws, seen)
	}
	addNets(ctx, req, ws, aimed)
	return left(ctx, ws.Attach(ctx, key, workspace.Terminal, req.Streams))
}

// carryOut carries out a decision on the worker of key: a join leaves it as it
// runs, a replace ends it and then starts it with begin, and a start starts it
// with begin.
func carryOut(ctx context.Context, ws *workspace.Workspace, key workspace.Key, act action, begin func() error) error {
	switch act {
	case join:
		return nil
	case replace:
		if err := ws.End(ctx, key); err != nil {
			return err
		}
		return begin()
	case start:
		return begin()
	}
	return fmt.Errorf("session: unhandled action %s", act)
}

// left is nil for an attach that ended because the client left, the worker's
// process ended or a signal stopped the session: those end a session as it is
// meant to end. Any other reason is the attach's failure. A stopped session is
// read from its context, not from the error: the signal also kills the
// relays' processes, and the attach may return one of their failures instead.
func left(ctx context.Context, err error) error {
	if ctx.Err() != nil || errors.Is(err, workspace.ErrLeft) || errors.Is(err, workspace.ErrEnded) {
		return nil
	}
	return err
}

// startWorker starts the worker of key in req.Dir on a terminal. For a
// conversation it first adds the FS relay between the local and the remote
// agent's state directories, connected and flushed so the agent finds the
// conversation's transcripts, then has the agent start it on the workspace's
// Host, its hooks aimed at the client; a command no agent runs is executed on
// the Host as given. It returns the hooks the agent's settings call, nil for
// none.
func startWorker(ctx context.Context, req Request, ws *workspace.Workspace, key workspace.Key, id string) (*hooks, error) {
	dir := ws.PathMap().ToRemote(req.Dir)
	if id == "" {
		host := ws.Host(key, workspace.Terminal, identityRecord(req.Identity))
		return nil, host.Exec(ctx, shell.Command{Args: req.Args, Dir: dir}).Wait()
	}
	if req.LocalAgent == nil {
		return nil, errors.New("session: a conversation needs the agent of this workstation too")
	}
	local, err := req.LocalAgent.StateDir(ctx, req.Dir)
	if err != nil {
		return nil, err
	}
	remote, err := req.Agent.StateDir(ctx, dir)
	if err != nil {
		return nil, err
	}
	if err := ws.AddFS(ctx, pathmap.Place{Local: local, Remote: remote}); err != nil {
		return nil, err
	}
	client, err := clientOf(ctx, req, ws)
	if err != nil {
		return nil, err
	}
	a := newAimer(req, client, func() (workerHook, error) { return newWorkerHook(ctx, req, ws) })
	host := ws.Host(key, workspace.Terminal, a.record)
	if err := req.Agent.Start(ctx, host, launch(req, ws, a.aim)); err != nil {
		return nil, err
	}
	return a.aimed(), nil
}

// rejoinHooks are the hooks of a running worker its directory records, aimed
// at the client's endpoint the caller's settings name now: the client's
// address and token change with every run of the client, while the worker's
// settings keep the port, token and routes they were started with. A worker
// that records none, or settings that cannot be read now, run without the
// hooks' Net relay.
func rejoinHooks(ctx context.Context, req Request, ws *workspace.Workspace, seen workspace.Look) *hooks {
	client, err := clientOf(ctx, req, ws)
	if err != nil {
		req.logger().Printf("no hooks: %v", err)
		return nil
	}
	if client == nil {
		return nil
	}
	rec, ok := readHook(seen)
	if !ok {
		req.logger().Printf("no hooks: %v", errNotRecorded)
		return nil
	}
	return &hooks{recorded: rec, client: *client}
}

// errNotRecorded is a running worker whose directory records no hooks.
var errNotRecorded = errors.New("the worker's directory records no hook port, token and routes")

// launch is what the agent is given to start req's conversation in ws.
func launch(req Request, ws *workspace.Workspace, hook func(agent.Hook) (agent.Hook, bool)) agent.Launch {
	return agent.Launch{Args: req.Args, Dir: req.Dir, Paths: ws.PathMap(), Environ: req.Environ, Hook: hook}
}

// identityRecord is the record of a worker given the developer's git
// identity and nothing else, as a Host reads it.
func identityRecord(id Identity) func() (workspace.Record, error) {
	return func() (workspace.Record, error) { return id.record(), nil }
}

// readHook is the hook a look found recorded in the worker's directory.
func readHook(seen workspace.Look) (recorded, bool) {
	portFound, okPort := seen.File(portFile)
	tokenFound, okToken := seen.File(tokenFile)
	routesFound, okRoutes := seen.File(routesFile)
	if !okPort || !okToken || !okRoutes {
		return recorded{}, false
	}
	port, err := strconv.Atoi(strings.TrimSpace(string(portFound.Content)))
	token := secret.FromString(string(tokenFound.Content))
	routes := strings.Fields(string(routesFound.Content))
	if err != nil || port <= 0 || token.IsZero() || len(routes) == 0 {
		return recorded{}, false
	}
	return recorded{workerHook: workerHook{port: port, token: token}, routes: routes}, true
}

// addNets adds the session's Net relays: the hooks' through their filter, and
// the user's own. A relay that cannot be added costs only what it carries,
// such as a port another process took on the remote: the session runs without
// it, and the next session tries the same port again.
func addNets(ctx context.Context, req Request, ws *workspace.Workspace, h *hooks) {
	logger := req.logger()
	if h != nil {
		if err := ws.AddNet(ctx, h.port, h.client.address, h.filter(ws.PathMap())); err != nil {
			logger.Printf("no hooks: remote port %d: %v", h.port, err)
		}
	}
	for _, f := range req.Forwards {
		if err := ws.AddNet(ctx, f.Port, f.Address, nil); err != nil {
			logger.Printf("no relay from remote port %d to %s: %v", f.Port, f.Address, err)
		}
	}
}

// newWorkerHook picks the worker's hook port free on the remote and
// generates its token.
func newWorkerHook(ctx context.Context, req Request, ws *workspace.Workspace) (workerHook, error) {
	raw := make([]byte, 2*hookPortCandidates)
	if _, err := io.ReadFull(req.random(), raw); err != nil {
		return workerHook{}, fmt.Errorf("pick hook ports: %w", err)
	}
	candidates := make([]int, hookPortCandidates)
	for i := range candidates {
		candidates[i] = hookPortMin + int(binary.BigEndian.Uint16(raw[2*i:]))%(hookPortMax-hookPortMin+1)
	}
	port, err := ws.FreePort(ctx, portFile, candidates)
	if err != nil {
		return workerHook{}, err
	}
	token, err := secret.NewToken(req.random())
	if err != nil {
		return workerHook{}, err
	}
	return workerHook{port: port, token: token}, nil
}
