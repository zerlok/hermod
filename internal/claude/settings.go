package claude

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/zerlok/hermod/internal/agent"
	"github.com/zerlok/hermod/internal/secret"
)

// target is a local endpoint the settings' http hooks go to: its host with an
// explicit port, the Authorization header value it expects, and the path of
// the hook it was found at, which tells Air's endpoint.
type target struct {
	host          string
	authorization secret.Token
	path          string
}

// airHookPath is the path of Air's hook endpoint, by which its hooks are told
// from those to another local endpoint.
const airHookPath = "/air/"

// hookPlan is what becomes of a settings document's hooks to this
// workstation's loopback. Only a Net relay reaches them from the remote, and
// it carries one endpoint, Air's; a hook to any other loopback endpoint would
// reach whatever listens on the remote's own loopback, so it is dropped.
type hookPlan struct {
	chosen  target
	carried bool     // a hook goes to Air's endpoint
	dropped []string // the dropped hooks' endpoints, each once, as a log may show them
}

// planHooks finds Air's endpoint, the first hook whose path is Air's, and the
// loopback hooks that go elsewhere.
func planHooks(settings map[string]any) (hookPlan, error) {
	var plan hookPlan
	hooks := httpHooks(settings)
	for _, h := range hooks {
		found, err := localTarget(h)
		if err != nil {
			return hookPlan{}, err
		}
		if found != nil && !plan.carried && strings.HasPrefix(found.path, airHookPath) {
			plan.chosen, plan.carried = *found, true
		}
	}
	for _, h := range hooks {
		if u, ok := plan.drops(h); ok {
			if e := endpointOnly(u); !slices.Contains(plan.dropped, e) {
				plan.dropped = append(plan.dropped, e)
			}
		}
	}
	return plan, nil
}

// drops reports whether a hook goes to this workstation's loopback but not to
// the chosen endpoint, with its URL.
func (p hookPlan) drops(h map[string]any) (*url.URL, bool) {
	raw, _ := h["url"].(string)
	u, err := url.Parse(raw)
	if err != nil || !isLoopback(u) {
		return nil, false
	}
	found, _ := localTarget(h) // parsed without error by planHooks
	return u, found == nil || !p.carried || !found.same(p.chosen)
}

// apply removes the dropped hooks, and with route set points the carried ones
// at the remote port, carrying the worker's token.
func (p hookPlan) apply(settings map[string]any, route func(h map[string]any)) {
	filterHooks(settings, func(h map[string]any) bool {
		if _, dropped := p.drops(h); dropped {
			return false
		}
		if found, _ := localTarget(h); found != nil && route != nil {
			route(h)
		}
		return true
	})
}

// callback is where the settings' hooks to Air call back on this
// workstation; hooked is false when none goes there. The routes are the
// request URIs (path and query) of the hooks to Air's endpoint, each once, in
// the document's order: the only calls the relay has reason to pass on.
func (p hookPlan) callback(settings map[string]any) (cb agent.Callback, hooked bool) {
	if !p.carried {
		return agent.Callback{}, false
	}
	var routes []string
	for _, h := range httpHooks(settings) {
		found, _ := localTarget(h) // parsed without error by planHooks
		if found == nil || !found.same(p.chosen) {
			continue
		}
		u, _ := url.Parse(h["url"].(string))
		if uri := u.RequestURI(); !slices.Contains(routes, uri) {
			routes = append(routes, uri)
		}
	}
	return agent.NewCallback(p.chosen.host, routes, p.chosen.authorization), true
}

// carry is the settings document as the worker is given it: the dropped hooks
// removed, the document as given when none is, with the callback of the hooks
// to Air and the endpoints of the dropped ones.
func carry(document secret.Token) (carried secret.Token, cb agent.Callback, hooked bool, dropped []string, err error) {
	settings, err := decodeSettings(document)
	if err != nil {
		return secret.Token{}, agent.Callback{}, false, nil, err
	}
	plan, err := planHooks(settings)
	if err != nil {
		return secret.Token{}, agent.Callback{}, false, nil, err
	}
	cb, hooked = plan.callback(settings)
	if len(plan.dropped) == 0 {
		return document, cb, hooked, nil, nil
	}
	plan.apply(settings, nil)
	out, err := marshal(settings)
	if err != nil {
		return secret.Token{}, agent.Callback{}, false, nil, fmt.Errorf("claude: settings: %w", err)
	}
	return secret.FromString(string(out)), cb, hooked, plan.dropped, nil
}

// Hooks aims the settings' hooks at a port of the remote. Every hook to Air's
// endpoint is pointed at `localhost:<port>` with `Authorization: Bearer
// <token>`, so neither Air's port nor its token reaches the remote. Hooks to
// any other loopback endpoint are left out. Hooks to other URLs and other hook
// types, and a document with no hook to this workstation, are left as they
// are.
func (Agent) Hooks(document secret.Token, port int, token secret.Token) (secret.Token, error) {
	if port <= 0 || port > 65535 {
		return secret.Token{}, fmt.Errorf("claude: settings: hook port %d out of range", port)
	}
	settings, err := decodeSettings(document)
	if err != nil {
		return secret.Token{}, err
	}
	plan, err := planHooks(settings)
	switch {
	case err != nil:
		return secret.Token{}, err
	case !plan.carried && len(plan.dropped) == 0:
		return document, nil
	}
	plan.apply(settings, func(h map[string]any) { routeHook(h, port, token) })
	out, err := marshal(settings)
	if err != nil {
		return secret.Token{}, fmt.Errorf("claude: settings: %w", err)
	}
	return secret.FromString(string(out)), nil
}

// decodeSettings reads a settings document, its numbers such as timeouts kept
// as written.
func decodeSettings(document secret.Token) (map[string]any, error) {
	var settings map[string]any
	dec := json.NewDecoder(strings.NewReader(document.Reveal()))
	dec.UseNumber()
	if err := dec.Decode(&settings); err != nil || settings == nil {
		return nil, errors.New("claude: settings: not a JSON object")
	}
	return settings, nil
}

// same reports whether two hooks go to one endpoint with one credential.
func (t target) same(other target) bool {
	return t.host == other.host && t.authorization.Equal(other.authorization)
}

// events are a settings document's hook events in a fixed order, so the first
// hook found is the same on every run.
func events(settings map[string]any) (map[string]any, []string) {
	byName, _ := settings["hooks"].(map[string]any)
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)
	return byName, names
}

// httpHooks lists the `type: http` hook objects in a settings document, under
// `hooks.<event>[].hooks[]`.
func httpHooks(settings map[string]any) []map[string]any {
	var hooks []map[string]any
	byName, names := events(settings)
	for _, name := range names {
		list, _ := byName[name].([]any)
		for _, g := range list {
			group, _ := g.(map[string]any)
			entries, _ := group["hooks"].([]any)
			for _, e := range entries {
				if h, ok := e.(map[string]any); ok && h["type"] == "http" {
					hooks = append(hooks, h)
				}
			}
		}
	}
	return hooks
}

// filterHooks keeps the `type: http` hooks keep accepts, which may change them,
// and removes the others; a matcher group left with no hook is removed, and an
// event left with no group.
func filterHooks(settings map[string]any, keep func(h map[string]any) bool) {
	byName, names := events(settings)
	for _, name := range names {
		list, ok := byName[name].([]any)
		if !ok {
			continue
		}
		var groups []any
		for _, g := range list {
			group, ok := g.(map[string]any)
			entries, isList := group["hooks"].([]any)
			if !ok || !isList {
				groups = append(groups, g)
				continue
			}
			var kept []any
			for _, e := range entries {
				if h, ok := e.(map[string]any); ok && h["type"] == "http" && !keep(h) {
					continue
				}
				kept = append(kept, e)
			}
			if len(kept) > 0 {
				group["hooks"] = kept
				groups = append(groups, group)
			}
		}
		if len(groups) > 0 {
			byName[name] = groups
		} else {
			delete(byName, name)
		}
	}
}

// localTarget is where one http hook goes when a Net relay can carry it: a
// plain http URL on this machine's loopback, its port made explicit so the
// relay can dial it. It is nil for a hook to anywhere else, and for an https
// one, since the relay forwards each call as plain http.
func localTarget(h map[string]any) (*target, error) {
	raw, _ := h["url"].(string)
	u, err := url.Parse(raw)
	if err != nil || !isLoopback(u) || u.Scheme != "http" {
		return nil, nil
	}
	port := u.Port()
	if port == "" {
		port = "80"
	}
	found := &target{host: net.JoinHostPort(u.Hostname(), port), path: u.Path}
	headers, _ := h["headers"].(map[string]any)
	for k, v := range headers {
		if strings.EqualFold(k, "Authorization") {
			s, ok := v.(string)
			if !ok {
				return nil, fmt.Errorf("claude: settings: hook %s has an Authorization header that is not a string", endpointOnly(u))
			}
			found.authorization = secret.FromString(s)
		}
	}
	return found, nil
}

// endpointOnly is a hook URL as a log may show it: its scheme, host and path.
// Its user info and query are left out, since either may carry a credential.
func endpointOnly(u *url.URL) string {
	return (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path, RawPath: u.RawPath}).String()
}

// routeHook points one http hook at the remote port, carrying the worker's
// token.
func routeHook(h map[string]any, port int, token secret.Token) {
	u, _ := url.Parse(h["url"].(string))
	headers, _ := h["headers"].(map[string]any)
	if headers == nil {
		headers = map[string]any{}
	}
	for k := range headers {
		if strings.EqualFold(k, "Authorization") {
			delete(headers, k)
		}
	}
	headers["Authorization"] = "Bearer " + token.Reveal()
	h["headers"] = headers
	routed := *u
	routed.Scheme = "http"
	routed.Host = net.JoinHostPort("localhost", strconv.Itoa(port))
	routed.User = nil
	h["url"] = routed.String()
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

// marshal encodes v without HTML escaping: the documents are read by Claude,
// not a browser, and paths and URLs may hold '&', '<' or '>'.
func marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}
