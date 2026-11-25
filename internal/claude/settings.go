package claude

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/zerlok/hermod/internal/agent"
	"github.com/zerlok/hermod/internal/secret"
)

// rewriteHooks applies rewrite to every `type: http` hook of a settings
// document, in a fixed order of the hook events, so the first hook given is
// the same on every run: a hook it leaves out is removed, and a matcher group
// left with no hook goes with it, as does an event left with no group. Other
// hook types are left as they are. A document no hook of which changes is
// returned as given; nil rewrite changes none.
func rewriteHooks(document secret.Token, rewrite func(agent.Hook) (agent.Hook, bool)) (secret.Token, error) {
	if rewrite == nil {
		return document, nil
	}
	settings, err := decodeSettings(document)
	if err != nil {
		return secret.Token{}, err
	}
	var (
		changed bool
		failed  error
	)
	filterHooks(settings, func(h map[string]any) bool {
		if failed != nil {
			return true
		}
		given, err := readHook(h)
		if err != nil {
			failed = err
			return true
		}
		out, keep := rewrite(given)
		switch {
		case !keep:
			changed = true
			return false
		case !sameHook(given, out):
			changed = true
			writeHook(h, out)
		}
		return true
	})
	switch {
	case failed != nil:
		return secret.Token{}, failed
	case !changed:
		return document, nil
	}
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

// readHook is an http hook's URL and headers. Claude sends a header only as a
// string, so a header of any other type is an error rather than a guess.
func readHook(h map[string]any) (agent.Hook, error) {
	raw, _ := h["url"].(string)
	hook := agent.Hook{URL: raw}
	headers, _ := h["headers"].(map[string]any)
	for k, v := range headers {
		s, ok := v.(string)
		if !ok {
			return agent.Hook{}, fmt.Errorf("claude: settings: a hook's header %s is not a string", k)
		}
		if hook.Headers == nil {
			hook.Headers = map[string]secret.Token{}
		}
		hook.Headers[k] = secret.FromString(s)
	}
	return hook, nil
}

// writeHook puts a rewritten URL and headers into an http hook.
func writeHook(h map[string]any, hook agent.Hook) {
	h["url"] = hook.URL
	if len(hook.Headers) == 0 {
		delete(h, "headers")
		return
	}
	headers := make(map[string]any, len(hook.Headers))
	for k, v := range hook.Headers {
		headers[k] = v.Reveal()
	}
	h["headers"] = headers
}

func sameHook(a, b agent.Hook) bool {
	if a.URL != b.URL || len(a.Headers) != len(b.Headers) {
		return false
	}
	for k, v := range a.Headers {
		if w, ok := b.Headers[k]; !ok || !v.Equal(w) {
			return false
		}
	}
	return true
}

// events are a settings document's hook events in a fixed order.
func events(settings map[string]any) (map[string]any, []string) {
	byName, _ := settings["hooks"].(map[string]any)
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)
	return byName, names
}

// filterHooks keeps the `type: http` hooks under `hooks.<event>[].hooks[]`
// that keep accepts, which may change them, and removes the others; a matcher
// group left with no hook is removed, and an event left with no group.
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
