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

// The environment an entry gives the adapter, which passes it on to every
// claude it starts: the stand-ins read it, never air acp's arguments.
const (
	// EnvSandbox names the sandbox. Every Hermod invocation reads it when given
	// no sandbox, so the entry's chats resolve the sandbox the entry was made for.
	EnvSandbox = "HERMOD_SANDBOX"
	// EnvOrigin names this machine on the sandbox; set to empty, it names none.
	EnvOrigin = "HERMOD_ORIGIN"
	// envExecutable and envConfigDir are claude.EnvExecutable and
	// claude.EnvConfigDir, spelled out here because air, a sibling of claude,
	// does not depend on it.
	envExecutable = "CLAUDE_CODE_EXECUTABLE"
	envConfigDir  = "CLAUDE_CONFIG_DIR"
)

// serversKey is the member of acp.json that holds the custom agents.
const serversKey = "agent_servers"

// ACPPath is Air's file of custom agents, under the user's home.
func ACPPath(home string) string {
	return filepath.Join(home, ".jetbrains", "acp.json")
}

// ConfigDir is the Claude config directory of one sandbox's chats: the
// adapter lists sessions from it, so each sandbox's agent lists only the chats
// that sandbox's transcripts bring back. The sandbox names a directory, so an
// alias that is not one plain name is refused.
func ConfigDir(dataHome, sandbox string) (string, error) {
	if sandbox == "" || sandbox == "." || sandbox == ".." || strings.ContainsAny(sandbox, `/\`) {
		return "", fmt.Errorf("sandbox %q cannot name a directory", sandbox)
	}
	return filepath.Join(dataHome, "hermod", "air", sandbox, "claude"), nil
}

// SandboxVariable is the line that gives a process the sandbox, as Air's own
// environment needs it for terminal mode, where no entry sets it.
func SandboxVariable(sandbox string) string { return EnvSandbox + "=" + sandbox }

// DefaultName is the name an entry gets when none is given: one entry per
// sandbox makes each sandbox its own agent in Air's picker.
func DefaultName(sandbox string) string { return "Claude (" + sandbox + ")" }

// Origin is the HERMOD_ORIGIN an entry carries. The zero value carries none, so
// each chat names the machine by its hostname; a set origin, even an empty
// one, is written as it is.
type Origin struct {
	name string
	set  bool
}

// OriginOf is an origin the entry carries.
func OriginOf(name string) Origin { return Origin{name: name, set: true} }

// ClaudeAgent is what the entry for one sandbox is made from. Every path is
// absolute: Air does not load the user's shell, so it could find nothing by
// name.
type ClaudeAgent struct {
	Name      string // the entry's name; "" is DefaultName
	Sandbox   string
	Hermod    string // the hermod binary, which the entry runs to wrap the adapter
	Node      string // the Node binary that runs the adapter
	Adapter   string // the claude-agent-acp script
	StandIn   string // the stand-in link, which the adapter runs as claude
	ConfigDir string // see ConfigDir
	Origin    Origin
}

// Entry is one agent in acp.json, built only by NewEntry.
type Entry struct {
	name string
	body entryBody
}

type entryBody struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

// acpArgs are the arguments that make hermod wrap the adapter for Claude
// (`hermod air acp claude`), the programs following as flags.
var acpArgs = []string{"air", "acp", "claude"}

// NewEntry is the entry that runs the adapter for one sandbox, wrapped by
// `hermod air acp claude`, which acts on the chats Air marks done. The sandbox
// is written only into the environment, which the wrapper, the adapter and
// every stand-in the adapter starts see alike.
func NewEntry(a ClaudeAgent) (Entry, error) {
	if strings.TrimSpace(a.Sandbox) == "" {
		return Entry{}, errors.New("an Air entry needs a sandbox")
	}
	for _, p := range []struct{ what, path string }{
		{"hermod", a.Hermod}, {"node", a.Node}, {"the ACP adapter", a.Adapter}, {"the stand-in link", a.StandIn}, {"the Claude config directory", a.ConfigDir},
	} {
		if !filepath.IsAbs(p.path) {
			return Entry{}, fmt.Errorf("the path of %s, %q, is not absolute", p.what, p.path)
		}
	}
	name := a.Name
	if name == "" {
		name = DefaultName(a.Sandbox)
	}
	env := map[string]string{
		EnvSandbox:    a.Sandbox,
		envExecutable: a.StandIn,
		envConfigDir:  a.ConfigDir,
	}
	if a.Origin.set {
		env[EnvOrigin] = a.Origin.name
	}
	args := append(append([]string(nil), acpArgs...), "--node="+a.Node, "--acp="+a.Adapter)
	return Entry{name: name, body: entryBody{Command: a.Hermod, Args: args, Env: env}}, nil
}

// Name is the entry's name in acp.json.
func (e Entry) Name() string { return e.name }

// JSON is the entry as acp.json holds it, under its name.
func (e Entry) JSON() ([]byte, error) {
	return marshal(map[string]entryBody{e.name: e.body})
}

// ACPFile is an acp.json as read, ready to take an entry. Reading it apart
// from writing lets a caller refuse a file no entry can be merged into before
// it changes anything else.
type ACPFile struct {
	path    string // the file itself, a symlink followed
	mode    os.FileMode
	doc     map[string]json.RawMessage
	servers map[string]json.RawMessage
}

// ReadACP reads the acp.json at path. A missing or empty file reads as one
// with no members, and an agent_servers of null as one with no entries; a file
// that is not a JSON object, or whose agent_servers is not one, is reported.
func ReadACP(path string) (ACPFile, error) {
	path, err := resolveLink(path)
	if err != nil {
		return ACPFile{}, err
	}
	f := ACPFile{path: path, mode: 0o600, doc: map[string]json.RawMessage{}, servers: map[string]json.RawMessage{}}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return f, nil
	case err != nil:
		return ACPFile{}, fmt.Errorf("read %s: %w", path, err)
	}
	if info, err := os.Stat(path); err == nil {
		f.mode = info.Mode().Perm()
	}
	if len(bytes.TrimSpace(b)) > 0 {
		if err := json.Unmarshal(b, &f.doc); err != nil || f.doc == nil {
			return ACPFile{}, fmt.Errorf("%s is not a JSON object; left unchanged", path)
		}
	}
	if raw, ok := f.doc[serversKey]; ok {
		if err := json.Unmarshal(raw, &f.servers); err != nil {
			return ACPFile{}, fmt.Errorf("%s: %s is not a JSON object; left unchanged", path, serversKey)
		}
		if f.servers == nil { // null
			f.servers = map[string]json.RawMessage{}
		}
	}
	return f, nil
}

// Merge writes the file with the entry in it, replacing an entry of the same
// name and keeping every other entry and member. The file is replaced in one
// rename, so Air never reads half of it.
func (f ACPFile) Merge(e Entry) error {
	// One encoding of the whole, so the other members keep the characters
	// they were written with.
	merged := make(map[string]any, len(f.servers)+1)
	for name, raw := range f.servers {
		merged[name] = raw
	}
	merged[e.name] = e.body
	top := make(map[string]any, len(f.doc)+1)
	for key, raw := range f.doc {
		top[key] = raw
	}
	top[serversKey] = merged
	out, err := marshal(top)
	if err != nil {
		return fmt.Errorf("encode %s: %w", f.path, err)
	}
	return replaceFile(f.path, out, f.mode)
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
// as they are rather than escaped for HTML.
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
