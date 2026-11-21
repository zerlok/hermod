package claude

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/zerlok/hermod/internal/pathmap"
)

// Rewrite says how a session's argv is carried to the sandbox.
type Rewrite struct {
	// ToSandbox maps the local paths in the arguments.
	ToSandbox pathmap.Map
	// Settings is the sandbox path of the session's settings.json, which
	// `--settings` names in place of the caller's local file. "" drops the
	// option.
	Settings string
	// MCPConfig is the sandbox path of the session's MCP config, written by
	// MCPForSandbox, which one `--mcp-config` names in place of every value the
	// caller gave. "" drops the option.
	MCPConfig string
	// Resume starts the session with `--resume <uuid>` in place of the session
	// options the caller gave, for a session Claude has a transcript of.
	Resume bool
}

// ForSandbox is the call's argv as Claude on the sandbox runs it, program first:
//
//   - `--settings` names r.Settings, since the caller's file is local;
//   - `--mcp-config`, with all of its values, names r.MCPConfig once: the
//     caller's configs are local files or inline JSON that may hold secrets;
//   - every other word that is a local path, or a JSON value holding one, is
//     mapped to the sandbox;
//   - `--session-id` and `--resume` pass unchanged unless r.Resume replaces them;
//   - no `--name` is added: Claude's session name is what the user sees.
//
// The program is `claude` by name, found on the sandbox's PATH.
func (c Call) ForSandbox(r Rewrite) []string {
	out := []string{"claude"}
	id, keyed := c.Session()
	if r.Resume && keyed {
		out = append(out, "--resume", id.String())
	}
	if len(c.argv) == 0 {
		return out
	}
	words := c.argv[1:]
	mcpNamed := false
	for i := 0; i < len(words); i++ {
		name, value, inline := splitOption(words[i])
		// next is the option's value: the inline one, else the next word.
		next := func() (string, bool) {
			if inline {
				return value, true
			}
			if i+1 < len(words) {
				i++
				return words[i], true
			}
			return "", false
		}
		switch {
		case name == "--":
			out = append(out, "--")
			for _, w := range words[i+1:] {
				out = append(out, mapWord(w, r.ToSandbox))
			}
			return out
		case name == "--settings":
			if _, ok := next(); ok && r.Settings != "" {
				out = append(out, "--settings", r.Settings)
			}
		case name == "--mcp-config":
			i = skipValues(words, i)
			if r.MCPConfig != "" && !mcpNamed {
				out = append(out, "--mcp-config", r.MCPConfig)
				mcpNamed = true
			}
		case r.Resume && keyed && (name == "--session-id"):
			next()
		case r.Resume && keyed && name == "--fork-session":
		case r.Resume && keyed && (name == "-r" || name == "--resume"):
			// --resume takes an optional value; only a UUID is its value (see scan).
			if !inline && i+1 < len(words) {
				if _, ok := ParseSessionID(words[i+1]); ok {
					i++
				}
			}
		case inline:
			out = append(out, name+"="+mapWord(value, r.ToSandbox))
		default:
			out = append(out, mapWord(words[i], r.ToSandbox))
		}
	}
	return out
}

// Settings is the value `--settings` was given, a file path or inline JSON;
// ok is false when the call has none.
func (c Call) Settings() (value string, ok bool) {
	if len(c.argv) == 0 {
		return "", false
	}
	words := c.argv[1:]
	for i := 0; i < len(words); i++ {
		name, v, inline := splitOption(words[i])
		switch {
		case name == "--":
			return "", false
		case name == "--settings" && inline:
			return v, true
		case name == "--settings" && i+1 < len(words):
			return words[i+1], true
		}
	}
	return "", false
}

// IsInlineJSON reports whether an option's value is a JSON document rather than
// a file path: Claude takes either for `--settings` and `--mcp-config`.
func IsInlineJSON(value string) bool {
	trimmed := strings.TrimSpace(value)
	return strings.HasPrefix(trimmed, "{") && json.Valid([]byte(trimmed))
}

// mapWord maps one argument: a JSON value through every string inside it, any
// other word as a path.
func mapWord(w string, m pathmap.Map) string {
	trimmed := strings.TrimSpace(w)
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		if mapped, err := m.JSON([]byte(w)); err == nil {
			return string(mapped)
		}
	}
	return m.Path(w)
}

// MCPConfigs are the values `--mcp-config` was given, file paths or inline
// JSON, in order. Claude's option takes several: every word after it up to the
// next option is one, as after `--mcp-config=<value>`.
func (c Call) MCPConfigs() []string {
	if len(c.argv) == 0 {
		return nil
	}
	words := c.argv[1:]
	var values []string
	for i := 0; i < len(words); i++ {
		name, value, inline := splitOption(words[i])
		switch {
		case name == "--":
			return values
		case name == "--mcp-config":
			if inline {
				values = append(values, value)
			}
			end := skipValues(words, i)
			values = append(values, words[i+1:end+1]...)
			i = end
		}
	}
	return values
}

// skipValues is the index of the last value of the variadic option at
// words[i]: every following word up to one that looks like an option, as
// Claude's parser reads it, whether or not words[i] carries a value after '='.
func skipValues(words []string, i int) int {
	for i+1 < len(words) && !looksLikeOption(words[i+1]) {
		i++
	}
	return i
}

func looksLikeOption(w string) bool { return len(w) > 1 && w[0] == '-' }

// MCPForSandbox merges the caller's `--mcp-config` documents, in order, into
// the one the session's Claude reads on the sandbox, a later server of the same
// name winning as it would in Claude. A server whose command is an absolute
// path outside every mirrored root is a local program (Air's own IDE server is
// one) and is removed, its name returned in removed; a command found by name,
// or one inside a mirrored root, runs on the sandbox. Local paths in what is
// kept are mapped. doc is nil when nothing is left.
func MCPForSandbox(configs [][]byte, m pathmap.Map) (doc []byte, removed []string, err error) {
	merged := map[string]any{}
	servers := map[string]any{}
	for _, c := range configs {
		dec := json.NewDecoder(bytes.NewReader(c))
		dec.UseNumber() // numbers pass through as written
		var d map[string]any
		if err := dec.Decode(&d); err != nil {
			return nil, nil, fmt.Errorf("--mcp-config: not a JSON object: %w", err)
		}
		for k, v := range d {
			if k != "mcpServers" {
				merged[k] = v
				continue
			}
			list, ok := v.(map[string]any)
			if !ok {
				return nil, nil, errors.New("--mcp-config: mcpServers is not an object")
			}
			for name, s := range list {
				servers[name] = s
			}
		}
	}
	for name, s := range servers {
		if isLocalProgram(s, m) {
			delete(servers, name)
			removed = append(removed, name)
		}
	}
	sort.Strings(removed)
	if len(servers) > 0 {
		merged["mcpServers"] = servers
	}
	if len(merged) == 0 {
		return nil, removed, nil
	}
	encoded, err := marshal(merged)
	if err != nil {
		return nil, nil, fmt.Errorf("--mcp-config: %w", err)
	}
	mapped, err := m.JSON(encoded)
	if err != nil {
		return nil, nil, fmt.Errorf("--mcp-config: %w", err)
	}
	return mapped, removed, nil
}

func isLocalProgram(server any, m pathmap.Map) bool {
	s, ok := server.(map[string]any)
	if !ok {
		return false
	}
	command, ok := s["command"].(string)
	return ok && strings.HasPrefix(command, "/") && m.Path(command) == command
}

// marshal encodes v without HTML escaping: the documents are read by Claude, not
// a browser, and paths and URLs may hold '&', '<' or '>'.
func marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}
