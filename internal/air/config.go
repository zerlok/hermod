// Package air holds JetBrains Air's own facts: the acp.json document that
// makes a remote an agent in Air's chat, the stand-in link Air's terminal
// starts as its claude, Air's hook endpoint, and what Air's own ACP methods
// mean to a session.
package air

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// serversKey is the member of acp.json that holds the custom agents.
const serversKey = "agent_servers"

// ACPPath is Air's file of custom agents, under the user's home.
func ACPPath(home string) string {
	return filepath.Join(home, ".jetbrains", "acp.json")
}

// DefaultName is the name an entry gets when none is given: one entry per
// remote makes each remote its own agent in Air's picker.
func DefaultName(remote string) string { return "Claude (" + remote + ")" }

// acpArgs are the arguments before the remote that make hermod Air's chat
// agent for Claude.
var acpArgs = []string{"air", "acp", "claude"}

// Entry is one agent in acp.json, built only by NewEntry.
type Entry struct {
	name string
	body json.RawMessage
}

type entryBody struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env,omitempty"`
}

// NewEntry is the entry that runs `hermod air acp claude <remote>`. hermod
// must be an absolute path, since Air does not load the user's shell and could
// find nothing by name. env is the entry's environment; with none, the entry
// has no env member.
func NewEntry(name, remote, hermod string, env map[string]string) (Entry, error) {
	if strings.TrimSpace(remote) == "" {
		return Entry{}, errors.New("an Air entry needs a remote")
	}
	if !filepath.IsAbs(hermod) {
		return Entry{}, fmt.Errorf("the path of hermod, %q, is not absolute", hermod)
	}
	if name == "" {
		name = DefaultName(remote)
	}
	args := append(append([]string(nil), acpArgs...), remote)
	body, err := marshal(entryBody{Command: hermod, Args: args, Env: env})
	if err != nil {
		return Entry{}, fmt.Errorf("encode the entry %q: %w", name, err)
	}
	return Entry{name: name, body: body}, nil
}

// Name is the entry's name in acp.json.
func (e Entry) Name() string { return e.name }

// JSON is the entry as acp.json holds it, under its name.
func (e Entry) JSON() ([]byte, error) {
	return marshal(map[string]json.RawMessage{e.name: e.body})
}

// Config is an acp.json as read. Loading apart from saving lets a caller
// refuse a file no entry can be merged into before anything changes.
type Config struct {
	path    string // the file itself, a symlink followed
	mode    os.FileMode
	doc     map[string]json.RawMessage
	servers map[string]json.RawMessage
}

// Load reads the acp.json at path. A missing or empty file reads as one with
// no members, and an agent_servers of null as one with no entries; a file that
// is not a JSON object, or whose agent_servers is not one, is reported.
func Load(path string) (Config, error) {
	path, err := resolveLink(path)
	if err != nil {
		return Config{}, err
	}
	c := Config{path: path, mode: 0o600, doc: map[string]json.RawMessage{}, servers: map[string]json.RawMessage{}}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return c, nil
	case err != nil:
		return Config{}, fmt.Errorf("read %s: %w", path, err)
	}
	if info, err := os.Stat(path); err == nil {
		c.mode = info.Mode().Perm()
	}
	if len(bytes.TrimSpace(b)) > 0 {
		if err := json.Unmarshal(b, &c.doc); err != nil || c.doc == nil {
			return Config{}, fmt.Errorf("%s is not a JSON object; left unchanged", path)
		}
	}
	if raw, ok := c.doc[serversKey]; ok {
		if err := json.Unmarshal(raw, &c.servers); err != nil {
			return Config{}, fmt.Errorf("%s: %s is not a JSON object; left unchanged", path, serversKey)
		}
		if c.servers == nil { // null
			c.servers = map[string]json.RawMessage{}
		}
	}
	return c, nil
}

// Merge is the config with the entry in it, replacing an entry of the same
// name and keeping every other entry and member as it was written.
func (c Config) Merge(e Entry) Config {
	servers := make(map[string]json.RawMessage, len(c.servers)+1)
	for name, raw := range c.servers {
		servers[name] = raw
	}
	servers[e.name] = e.body
	c.servers = servers
	return c
}

// Save writes the config to the file it was loaded from, replacing it in one
// rename, so Air never reads half of it.
func (c Config) Save() error {
	top := make(map[string]any, len(c.doc)+1)
	for key, raw := range c.doc {
		top[key] = raw
	}
	top[serversKey] = c.servers
	out, err := marshal(top)
	if err != nil {
		return fmt.Errorf("encode %s: %w", c.path, err)
	}
	return replaceFile(c.path, out, c.mode)
}

// resolveLink follows a symlinked acp.json to the file it names, so the rename
// replaces that file rather than the user's link.
func resolveLink(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return path, nil
	}
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", path, err)
	}
	return target, nil
}

func replaceFile(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	defer os.Remove(tmp.Name()) // gone after a successful rename
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// marshal is v as indented JSON with a final newline, paths and all written
// as they are rather than escaped for HTML. A json.RawMessage inside v keeps
// its characters, so the members Hermod does not own read as they were.
func marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
