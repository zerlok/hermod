package claude

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/zerlok/hermod/internal/pathmap"
	"github.com/zerlok/hermod/internal/secret"
)

// HookTarget is where the caller's hooks go on this machine: Air's hook
// endpoint and the Authorization header value it expects, which changes with
// every IDE run.
type HookTarget struct {
	// Endpoint is the scheme and host of the hook URLs, without a path: a hook
	// keeps its own path, which the proxy forwards to unchanged.
	Endpoint *url.URL
	// Authorization is the header value the hooks carry; zero when they carry
	// none.
	Authorization secret.Token
	// Routes are the request URIs (path and query) of the hooks routed to the
	// endpoint, each once, in the document's order: the only URLs the proxy has
	// reason to forward.
	Routes []string
	path   string // the first hook's path, which tells Air's endpoint
}

// SandboxSettings is a settings document rewritten for the sandbox.
type SandboxSettings struct {
	doc     []byte
	target  *HookTarget
	dropped []string
}

// Doc is the document to write to the session's settings.json.
func (s SandboxSettings) Doc() []byte { return append([]byte(nil), s.doc...) }

// Hooks is where the caller's hooks went before the rewrite; ok is false when
// the document had no hook to a local endpoint, and no proxy is needed.
func (s SandboxSettings) Hooks() (target HookTarget, ok bool) {
	if s.target == nil {
		return HookTarget{}, false
	}
	return *s.target, true
}

// Dropped are the URLs, without user info or query, of the hooks to a local endpoint
// other than the routed one, which the rewrite left out.
func (s SandboxSettings) Dropped() []string { return append([]string(nil), s.dropped...) }

// HookRoute is how the rewritten hooks reach the sandbox end of the hook
// forward.
type HookRoute struct {
	Port  int          // the sandbox port the session's hook forward binds
	Token secret.Token // the session's own token, which the proxy accepts
}

// airHookPath is the path of Air's hook endpoint, by which its hooks are told
// from those to another local endpoint.
const airHookPath = "/air/"

// SettingsForSandbox rewrites a `--settings` document for the sandbox. Local
// paths in it are mapped. An `http` hook to a loopback URL goes to an endpoint
// on this machine, which only the hook forward can reach, and the forward
// carries one endpoint: Air's, by its path, else the first in the document.
// Every hook to that endpoint is pointed at `localhost:<route.Port>` with the
// session's token in its Authorization header, so neither Air's port nor its
// token reaches the sandbox. Hooks to any other loopback endpoint would reach
// whatever listens on the sandbox's own loopback, so they are left out and
// listed in Dropped. Hooks to other URLs and other hook types are left as they
// are.
func SettingsForSandbox(doc []byte, toSandbox pathmap.Map, route HookRoute) (SandboxSettings, error) {
	mapped, err := toSandbox.JSON(doc)
	if err != nil {
		return SandboxSettings{}, fmt.Errorf("settings: %w", err)
	}
	var settings map[string]any
	dec := json.NewDecoder(strings.NewReader(string(mapped)))
	dec.UseNumber() // numbers such as timeouts pass through as written
	if err := dec.Decode(&settings); err != nil {
		return SandboxSettings{}, fmt.Errorf("settings: not a JSON object: %w", err)
	}
	var targets []HookTarget
	for _, h := range httpHooks(settings) {
		found, err := localTarget(h)
		if err != nil {
			return SandboxSettings{}, err
		}
		if found != nil {
			targets = append(targets, *found)
		}
	}
	if len(targets) == 0 {
		return SandboxSettings{doc: mapped}, nil
	}
	target := chooseTarget(targets)
	var dropped []string
	filterHooks(settings, func(h map[string]any) bool {
		found, _ := localTarget(h) // the same hooks parsed without error above
		switch {
		case found == nil:
			return true
		case found.same(target):
			if uri := routeHook(h, route); !slices.Contains(target.Routes, uri) {
				target.Routes = append(target.Routes, uri)
			}
			return true
		default:
			u, _ := url.Parse(h["url"].(string))
			dropped = append(dropped, endpointOnly(u))
			return false
		}
	})
	out, err := marshal(settings)
	if err != nil {
		return SandboxSettings{}, fmt.Errorf("settings: %w", err)
	}
	return SandboxSettings{doc: out, target: &target, dropped: dropped}, nil
}

// chooseTarget is the endpoint the forward carries: the first of Air's, else
// the first found.
func chooseTarget(targets []HookTarget) HookTarget {
	for _, t := range targets {
		if strings.HasPrefix(t.path, airHookPath) {
			return t
		}
	}
	return targets[0]
}

// same reports whether two hooks go to one endpoint with one credential.
func (t HookTarget) same(other HookTarget) bool {
	return t.Endpoint.String() == other.Endpoint.String() && t.Authorization.Equal(other.Authorization)
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

// localTarget is where one http hook goes when its URL is on this machine's
// loopback; it is nil for a hook to anywhere else.
func localTarget(h map[string]any) (*HookTarget, error) {
	raw, _ := h["url"].(string)
	u, err := url.Parse(raw)
	if err != nil || !isLoopback(u) {
		return nil, nil
	}
	target := &HookTarget{Endpoint: &url.URL{Scheme: u.Scheme, Host: u.Host}, path: u.Path}
	headers, _ := h["headers"].(map[string]any)
	for k, v := range headers {
		if strings.EqualFold(k, "Authorization") {
			s, ok := v.(string)
			if !ok {
				return nil, fmt.Errorf("settings: hook %s has an Authorization header that is not a string", u.Redacted())
			}
			target.Authorization = secret.FromString(s)
		}
	}
	return target, nil
}

// endpointOnly is a hook URL as a log may show it: its scheme, host and path.
// Its user info and query are left out, since either may carry a credential.
func endpointOnly(u *url.URL) string {
	return (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path, RawPath: u.RawPath}).String()
}

// routeHook points one http hook to the routed endpoint at the session's
// sandbox port and token, and returns the request URI it posts to.
func routeHook(h map[string]any, route HookRoute) string {
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
	headers["Authorization"] = "Bearer " + route.Token.Reveal()
	h["headers"] = headers
	routed := *u
	routed.Scheme = "http"
	routed.Host = net.JoinHostPort("localhost", strconv.Itoa(route.Port))
	routed.User = nil
	h["url"] = routed.String()
	return routed.RequestURI()
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
