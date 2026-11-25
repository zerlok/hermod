package session

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/zerlok/hermod/internal/agent"
	"github.com/zerlok/hermod/internal/pathmap"
	"github.com/zerlok/hermod/internal/relay"
	"github.com/zerlok/hermod/internal/secret"
	"github.com/zerlok/hermod/internal/shell"
	"github.com/zerlok/hermod/internal/workspace"
)

// workerHook is where a worker's hooks call on the remote: its Net relay's
// port, and the token they carry.
type workerHook struct {
	port  int
	token secret.Token
}

// recorded is what a hooked worker's directory keeps: its port and token, and
// the routes its hooks call there.
type recorded struct {
	workerHook
	routes []string // request URIs, path and query
}

// hooks are a hooked worker's recorded hooks with the client's endpoint they
// reach through the Net relay.
type hooks struct {
	recorded
	client endpoint
}

// filter is the filter of the Net relay that carries the hooks to the
// client.
func (h hooks) filter(paths pathmap.Map) hookFilter {
	return newHookFilter(h.token, h.routes, h.client.authorization, paths)
}

// endpoint is a hook endpoint on this workstation: its host with an explicit
// port, which the relay dials, and the Authorization value it expects.
type endpoint struct {
	address       string
	authorization secret.Token
}

// aimer is the function a session gives an agent to rewrite its hooks, and
// what it learned doing so. Only a Net relay reaches this workstation's
// loopback from the remote, and it carries one endpoint, the client's: every
// hook to it is aimed at the worker's port on the remote with the worker's
// token. A hook to another loopback endpoint would reach whatever listens on
// the remote's own, and one to the client's address with another credential
// is not the client's to carry, so both are left out and logged; any other
// hook is kept as it is. After a failed pick no hook is aimed, so the worker's
// port and token are asked for at most once.
type aimer struct {
	req    Request
	client *endpoint // nil aims no hook
	// worker is the worker's port and token, asked for on the first hook to
	// aim, so a start whose settings aim none picks no port.
	worker func() (workerHook, error)

	picked  *workerHook
	routes  []string
	dropped []string
	err     error // why a hook to the client could not be aimed
}

func newAimer(req Request, client *endpoint, worker func() (workerHook, error)) *aimer {
	return &aimer{req: req, client: client, worker: worker}
}

// aim rewrites one hook as the aimer's doc says.
func (a *aimer) aim(h agent.Hook) (agent.Hook, bool) {
	u, err := url.Parse(h.URL)
	if err != nil || !isLoopback(u) {
		return h, true
	}
	target, carried := endpointOf(u, h)
	switch {
	case !carried || a.client == nil || target.address != a.client.address:
		a.drop(u, "no relay carries it to this workstation")
		return agent.Hook{}, false
	case !target.authorization.Equal(a.client.authorization):
		a.drop(u, "it carries another credential than the client's endpoint expects")
		return agent.Hook{}, false
	}
	if a.err != nil {
		return agent.Hook{}, false
	}
	if a.picked == nil {
		w, err := a.worker()
		if err != nil {
			a.err = err
			return agent.Hook{}, false
		}
		a.picked = &w
	}
	if uri := u.RequestURI(); !slices.Contains(a.routes, uri) {
		a.routes = append(a.routes, uri)
	}
	return routed(h, u, a.picked.port, a.picked.token), true
}

// drop logs a hook left out and why, each endpoint once, by scheme, host and
// path: its user info and query may carry a credential.
func (a *aimer) drop(u *url.URL, reason string) {
	e := (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path, RawPath: u.RawPath}).String()
	if slices.Contains(a.dropped, e) {
		return
	}
	a.dropped = append(a.dropped, e)
	a.req.logger().Printf("left out the hook to %s: %s", e, reason)
}

// record is the worker's record once the agent has rewritten its hooks: the
// developer's git identity, and for aimed hooks the port, the token and the
// routes, so a later session serves the same ones.
func (a *aimer) record() (workspace.Record, error) {
	if a.err != nil {
		return workspace.Record{}, a.err
	}
	rec := a.req.Identity.record()
	if h := a.aimed(); h != nil {
		rec.Files = []workspace.File{
			{Name: portFile, Content: secret.FromString(strconv.Itoa(h.port) + "\n")},
			{Name: routesFile, Content: secret.FromString(strings.Join(h.routes, "\n") + "\n")},
			{Name: tokenFile, Content: h.token},
		}
	}
	return rec, nil
}

// aimed is the hooks aimed at the client; nil when none was.
func (a *aimer) aimed() *hooks {
	if a.picked == nil || len(a.routes) == 0 {
		return nil
	}
	return &hooks{recorded: recorded{workerHook: *a.picked, routes: slices.Clone(a.routes)}, client: *a.client}
}

// endpointOf is where a loopback hook goes when a Net relay can carry it: a
// plain http URL, its port made explicit so the relay can dial it. carried is
// false for an https one, since the relay forwards each call as plain http.
func endpointOf(u *url.URL, h agent.Hook) (e endpoint, carried bool) {
	if u.Scheme != "http" {
		return endpoint{}, false
	}
	port := u.Port()
	if port == "" {
		port = "80"
	}
	e.address = net.JoinHostPort(u.Hostname(), port)
	for k, v := range h.Headers {
		if strings.EqualFold(k, "Authorization") {
			e.authorization = v
		}
	}
	return e, true
}

// clientOf is the client's hook endpoint the caller's settings name now: the
// first carried hook IsHook accepts; nil for none. It is found before any hook
// is aimed, so every hook to it is aimed wherever it sits in the settings.
//
// TODO: the agent contract has no operation that only reads its settings'
// hooks: docs/ARCHITECTURE.md lists rewriting them among an agent's
// capabilities, but its method table has none. The hooks are therefore read
// by a start on an executor that runs nothing, which holds only for an agent
// whose start acts through its executor alone.
func clientOf(ctx context.Context, req Request, ws *workspace.Workspace) (*endpoint, error) {
	if req.IsHook == nil {
		return nil, nil
	}
	var client *endpoint
	find := func(h agent.Hook) (agent.Hook, bool) {
		if client != nil {
			return h, true
		}
		if u, err := url.Parse(h.URL); err == nil && isLoopback(u) && req.IsHook(u) {
			if e, carried := endpointOf(u, h); carried {
				client = &e
			}
		}
		return h, true
	}
	if err := req.Agent.Start(ctx, shell.NewDryRun(io.Discard), launch(req, ws, find)); err != nil {
		return nil, err
	}
	return client, nil
}

// routed is a hook pointed at the worker's port on the remote's loopback,
// carrying the worker's token in place of the client's.
func routed(h agent.Hook, u *url.URL, port int, token secret.Token) agent.Hook {
	headers := map[string]secret.Token{}
	for k, v := range h.Headers {
		if !strings.EqualFold(k, "Authorization") {
			headers[k] = v
		}
	}
	headers["Authorization"] = secret.FromString("Bearer " + token.Reveal())
	r := *u
	r.Scheme = "http"
	r.Host = net.JoinHostPort("localhost", strconv.Itoa(port))
	r.User = nil
	return agent.Hook{URL: r.String(), Headers: headers}
}

func isLoopback(u *url.URL) bool {
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	host := u.Hostname()
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// hookFilter is the relay.Filter of an agent's hooks. The remote port is
// reachable by every user of the remote, so only a call bearing the worker's
// token passes, and of those only a POST to a route that was aimed: the
// client's token must not reach anything else the client serves. The client's
// current token replaces the worker's, so a running agent's settings stay
// valid while the client restarts with a new token, and the client's never
// reaches the remote.
type hookFilter struct {
	worker secret.Token // what the worker's settings carry
	routes []string     // request URIs, path and query
	client secret.Token // the Authorization value the client expects now; zero sends none
	paths  pathmap.Map
}

func newHookFilter(worker secret.Token, routes []string, client secret.Token, paths pathmap.Map) hookFilter {
	return hookFilter{worker: worker, routes: slices.Clone(routes), client: client, paths: paths}
}

// Pass refuses a call without the worker's token, to another route or with
// another method than a hook's, and passes any other with the paths of its
// body mapped to this workstation and those of the answer back.
func (f hookFilter) Pass(c relay.Call) (relay.Call, error) {
	got, ok := strings.CutPrefix(c.Header.Get("Authorization"), "Bearer ")
	if !ok || f.worker.IsZero() || !secret.FromString(got).Equal(f.worker) {
		return relay.Call{}, &relay.Refusal{Status: http.StatusUnauthorized, Reason: "not the worker's token"}
	}
	if !slices.Contains(f.routes, c.URI) {
		return relay.Call{}, &relay.Refusal{Status: http.StatusNotFound, Reason: "not a hook's route"}
	}
	if c.Method != http.MethodPost {
		return relay.Call{}, &relay.Refusal{Status: http.StatusMethodNotAllowed, Reason: "not a hook's method"}
	}
	// Only what a hook needs is passed on, so no header the remote set reaches
	// the client.
	header := http.Header{}
	if ct := c.Header.Get("Content-Type"); ct != "" {
		header.Set("Content-Type", ct)
	}
	if !f.client.IsZero() {
		header.Set("Authorization", f.client.Reveal())
	}
	return relay.Call{
		Method: http.MethodPost,
		URI:    c.URI,
		Header: header,
		Body:   mapBody(c.Body, f.paths.ToLocal),
		Answer: func(answer []byte) []byte { return mapBody(answer, f.paths.ToRemote) },
	}, nil
}

// mapBody maps the paths in a JSON body; a body that is not one JSON value
// passes as it is, since a hook's caller and its endpoint agree on it without
// Hermod.
func mapBody(body []byte, mapPath func(string) string) []byte {
	dec := json.NewDecoder(bytes.NewReader(body))
	// Numbers stay as written: a float64 would round a large integer.
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return body
	}
	if _, err := dec.Token(); err != io.EOF {
		return body
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	// A path may hold '&', '<' or '>', and the body is not HTML.
	enc.SetEscapeHTML(false)
	if err := enc.Encode(pathmap.Value(v, mapPath)); err != nil {
		return body
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n"))
}
