package session

import (
	"bytes"
	"net/http"
	"slices"
	"strings"

	"github.com/zerlok/hermod/internal/pathmap"
	"github.com/zerlok/hermod/internal/relay"
	"github.com/zerlok/hermod/internal/secret"
)

// hookFilter is the relay.Filter of an agent's hooks. The remote port is
// reachable by every user of the remote, so only a call bearing the worker's
// token passes, and of those only a POST to a route the agent declared: the
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

// mapBody maps the paths in a JSON body; a body that is not JSON passes as it
// is, since a hook's caller and its endpoint agree on it without Hermod.
func mapBody(body []byte, mapPath func(string) string) []byte {
	if len(bytes.TrimSpace(body)) == 0 {
		return body
	}
	mapped, err := pathmap.JSON(body, mapPath)
	if err != nil {
		return body
	}
	return mapped
}
