// Package session attaches a local client to a workspace on a remote. It is
// the only package that decides from states: what to do for a pair of an
// agent's and a worker's states, and where a chat's message goes. It reaches
// relays and workers only through workspace, and an agent only through
// agent.Agent, whose questions it answers by running the agent's commands on
// the remote. It keeps nothing past the process.
package session

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/zerlok/hermod/internal/agent"
	"github.com/zerlok/hermod/internal/env"
	"github.com/zerlok/hermod/internal/relay"
	"github.com/zerlok/hermod/internal/secret"
	"github.com/zerlok/hermod/internal/worker"
	"github.com/zerlok/hermod/internal/workspace"
)

// Request is one attach, as the command line resolved it.
type Request struct {
	Remote workspace.Remote
	Local  workspace.Local
	Dir    string             // the local directory the session works in, absolute
	Tree   workspace.Worktree // the linked git worktree Dir lies in; zero for none

	// Agent reads Args; nil runs them as a command no agent runs.
	Agent agent.Agent
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
	Streams  relay.Streams

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

func (r Request) logger() *log.Logger {
	if r.Log == nil {
		return log.New(io.Discard, "", 0)
	}
	return r.Log
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

// The files of a hooked worker's directory that keep its Net relay's port and
// token for as long as the worker exists: its agent's settings carry both,
// and every session that attaches it later serves the same ones.
const (
	portFile  = "hook-port"
	tokenFile = "hook-token"
)

// Hook ports are drawn below Linux's default ephemeral range (32768–60999),
// where outgoing connections would otherwise take them.
const (
	hookPortMin        = 10000
	hookPortMax        = 32767
	hookPortCandidates = 16
)

// Attach runs one session: it opens the workspace the directory belongs to,
// reads the worker's and the agent's states in one call on the remote, decides
// from them, starts the worker when the decision says so, attaches the client
// to it and detaches the workspace when the attach ends. A one-shot call runs
// on the remote instead, with no workspace. A detach that the client caused,
// by leaving, a signal or the worker's end, returns nil, so the caller exits
// at once and nothing has to kill it.
func Attach(ctx context.Context, req Request) error {
	call := agent.NewCommand(req.Args)
	if req.Agent != nil {
		call = req.Agent.Parse(req.Args)
	}
	if call.OneShot() {
		return oneShot(ctx, req, call)
	}
	key, err := keyOf(req, call)
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
	err = run(ctx, req, ws, call, key)
	return errors.Join(err, ws.Detach(ctx))
}

// keyOf is the key of the call's worker: the conversation's, or the name the
// caller gave a command no agent runs.
func keyOf(req Request, call agent.Call) (worker.Key, error) {
	if id, ok := call.ID(); ok {
		return workspace.ConversationKey(id)
	}
	return worker.NewKey(req.Name)
}

// run is a session on an opened workspace, up to the end of its attach.
func run(ctx context.Context, req Request, ws *workspace.Workspace, call agent.Call, key worker.Key) error {
	id, conversation := call.ID()
	var (
		probe agent.Probe
		look  []string
	)
	if conversation {
		probe = req.Agent.Probe(id)
		look = probe.Command()
	}
	seen, err := ws.Look(ctx, key, look, portFile, tokenFile)
	if err != nil {
		return err
	}
	st := states{worker: seen.State}
	if conversation {
		st.runsAgent = true
		if st.agent, err = probe.Read(seen.Out); err != nil {
			return err
		}
	}
	act, err := decide(st)
	if err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	req.logger().Printf("worker %s: %s", key, act)

	needs := agent.NewNeeds(call.Args())
	if conversation {
		if needs, err = req.Agent.Run(call, ws.PathMap()); err != nil {
			return err
		}
	}
	hook, hooked := readHook(seen)
	switch act {
	case join:
	case replace:
		if err := ws.End(ctx, key); err != nil {
			return err
		}
		fallthrough
	case start:
		if hook, hooked, err = startWorker(ctx, req, ws, key, needs); err != nil {
			return err
		}
	default:
		return fmt.Errorf("session: unhandled action %s", act)
	}
	addNets(ctx, req, ws, needs, hook, hooked)
	return left(ctx, ws.Attach(ctx, key, worker.Terminal, req.Streams))
}

// left is nil for an attach that ended because the client left, the worker's
// process ended or a signal stopped the session: those end a session as it is
// meant to end. Any other reason is the attach's failure. A stopped session is
// read from its context, not from the error: the signal also kills the
// relays' processes, and the attach may return one of their failures instead.
func left(ctx context.Context, err error) error {
	if ctx.Err() != nil || errors.Is(err, relay.ErrLeft) || errors.Is(err, relay.ErrEnded) {
		return nil
	}
	return err
}

// hook is a hooked worker's Net relay: the remote port its agent's hooks call
// and the token they carry.
type hook struct {
	port  int
	token secret.Token
}

// readHook is the hook a look found recorded in the worker's directory.
func readHook(seen worker.Look) (hook, bool) {
	portFound, okPort := seen.File(portFile)
	tokenFound, okToken := seen.File(tokenFile)
	if !okPort || !okToken {
		return hook{}, false
	}
	port, err := strconv.Atoi(strings.TrimSpace(string(portFound.Content)))
	token := secret.FromString(string(tokenFound.Content))
	if err != nil || port <= 0 || token.IsZero() {
		return hook{}, false
	}
	return hook{port: port, token: token}, true
}

// startWorker starts the worker: first the FS relays its agent requires,
// connected and flushed so the agent finds their files, then the worker's
// directory and the worker. A hooked agent's settings are aimed at a port
// picked free on the remote and carry a token generated for the worker, both
// recorded in its directory.
func startWorker(ctx context.Context, req Request, ws *workspace.Workspace, key worker.Key, needs agent.Needs) (hook, bool, error) {
	for _, place := range needs.Places() {
		if err := ws.AddFS(ctx, place); err != nil {
			return hook{}, false, err
		}
	}
	contents := needs.Files()
	if contents == nil {
		contents = map[string]secret.Token{}
	}
	var (
		h      hook
		hooked bool
	)
	if file, document, ok := needs.Settings(); ok {
		if _, _, _, ok := needs.Hooks(); ok {
			var err error
			if h, err = newHook(ctx, req, ws); err != nil {
				return hook{}, false, err
			}
			if document, err = req.Agent.Hooks(document, h.port, h.token); err != nil {
				return hook{}, false, err
			}
			hooked = true
			contents[portFile] = secret.FromString(strconv.Itoa(h.port) + "\n")
			contents[tokenFile] = h.token
		}
		contents[file] = document
	}
	var files []worker.File
	for _, name := range slices.Sorted(maps.Keys(contents)) {
		files = append(files, worker.File{Name: name, Content: []byte(contents[name].Reveal())})
	}
	_, err := ws.Start(ctx, worker.Spec{
		Key:     key,
		Dir:     ws.PathMap().ToRemote(req.Dir),
		Command: needs.Command(),
		Files:   files,
		Vars: needs.Vars(req.Environ).
			With(req.Identity.vars()...).
			With(env.Pair{Key: agent.DirVar, Value: env.Plain(key.Dir(req.Remote.Home))}),
		Form: worker.Terminal,
	})
	return h, hooked, err
}

// newHook picks the worker's hook port free on the remote and generates its
// token.
func newHook(ctx context.Context, req Request, ws *workspace.Workspace) (hook, error) {
	raw := make([]byte, 2*hookPortCandidates)
	if _, err := io.ReadFull(req.random(), raw); err != nil {
		return hook{}, fmt.Errorf("pick hook ports: %w", err)
	}
	candidates := make([]int, hookPortCandidates)
	for i := range candidates {
		candidates[i] = hookPortMin + int(binary.BigEndian.Uint16(raw[2*i:]))%(hookPortMax-hookPortMin+1)
	}
	port, err := ws.FreePort(ctx, portFile, candidates)
	if err != nil {
		return hook{}, err
	}
	token, err := secret.NewToken(req.random())
	if err != nil {
		return hook{}, err
	}
	return hook{port: port, token: token}, nil
}

// addNets adds the session's Net relays: the hooks' through their filter, and
// the user's own. A relay that cannot be added costs only what it carries,
// such as a port another process took on the remote: the session runs without
// it, and the next session tries the same port again.
func addNets(ctx context.Context, req Request, ws *workspace.Workspace, needs agent.Needs, h hook, hooked bool) {
	logger := req.logger()
	if address, routes, client, ok := needs.Hooks(); ok {
		if !hooked {
			logger.Printf("no hooks: the worker's directory records no hook port and token")
		} else if err := ws.AddNet(ctx, h.port, address, newHookFilter(h.token, routes, client, ws.PathMap())); err != nil {
			logger.Printf("no hooks: remote port %d: %v", h.port, err)
		}
	}
	for _, f := range req.Forwards {
		if err := ws.AddNet(ctx, f.Port, f.Address, nil); err != nil {
			logger.Printf("no relay from remote port %d to %s: %v", f.Port, f.Address, err)
		}
	}
}
