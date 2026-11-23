package claude

import (
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/zerlok/hermod/internal/agent"
	"github.com/zerlok/hermod/internal/pathmap"
	"github.com/zerlok/hermod/internal/secret"
)

// The files a conversation's worker is given in its directory.
const (
	settingsFile = "settings.json"
	mcpPrefix    = "mcp-" // followed by the value's position and ".json"
)

// The variables of Claude's own that Hermod leaves out or reads.
const (
	envConfigDir  = "CLAUDE_CONFIG_DIR"
	envExecutable = "CLAUDE_CODE_EXECUTABLE"
)

// localOnlyEnv are the variables of Claude's own that belong to this machine
// or to one process on it, so on the remote they would point at nothing, or at
// the wrong thing:
//
//   - the executable Hermod stands in for and the local config directory;
//   - what a running Claude sets for the programs it starts (its executable
//     path, PID, session, messaging socket and token, IDE port, project
//     directory and hook env file), present when Hermod itself runs under a
//     Claude.
//
// Every other `CLAUDE_*` variable is the caller's choice for the conversation,
// as the Agent SDK's are, and is carried.
var localOnlyEnv = map[string]bool{
	envExecutable: true,
	envConfigDir:  true,

	"CLAUDE_CODE_EXECPATH":          true,
	"CLAUDE_PID":                    true,
	"CLAUDE_CODE_SESSION_ID":        true,
	"CLAUDE_CODE_BRIDGE_SESSION_ID": true,
	"CLAUDE_CODE_CHILD_SESSION":     true,
	"CLAUDE_CODE_SESSION_ATTENDED":  true,
	"CLAUDE_CODE_MESSAGING_SOCKET":  true,
	"CLAUDE_CODE_MESSAGING_TOKEN":   true,
	"CLAUDE_CODE_SSE_PORT":          true,
	"CLAUDE_PROJECT_DIR":            true,
	"CLAUDE_ENV_FILE":               true,
}

// declared accepts the variables Claude reads that a caller sets for a
// conversation: `CLAUDE_*`, `ANTHROPIC_*` and `MAX_THINKING_TOKENS`, apart from
// the local-only ones.
func declared(key string) bool {
	if localOnlyEnv[key] {
		return false
	}
	return strings.HasPrefix(key, "CLAUDE_") || strings.HasPrefix(key, "ANTHROPIC_") || key == "MAX_THINKING_TOKENS"
}

// Run is what Claude needs on the remote for a call of its own.
//
// A conversation's command is the caller's argv with every local path mapped,
// run by `claude` found on the remote's PATH, with no `--name` added:
// Claude's session name is what the user sees. The documents `--settings` and
// `--mcp-config` name, files or inline JSON, are given to the worker as files
// in its directory, their paths mapped, and the options name those files, so
// a credential in them stays off the command line. The settings also lose
// their hooks to a loopback endpoint other than Air's, which nothing on the
// remote could reach; those are declared as dropped. The conversation's transcripts directory is a place, so a
// conversation that exists only locally is on the remote before Claude
// resumes it, and the remote's appear locally.
//
// A one-shot call runs with its paths mapped and no worker's directory.
func (a Agent) Run(call agent.Call, paths pathmap.Map) (agent.Needs, error) {
	args := call.Args()
	_, conversation := call.ID()
	switch {
	case call.OneShot():
		return agent.NewNeeds(append([]string{"claude"}, mapWords(args[1:], paths)...)).WithVars(declared), nil
	case !conversation:
		// The arguments are not named: an inline document may hold a credential.
		return agent.Needs{}, errors.New("claude: the call is not one Claude runs")
	}
	place, err := a.transcripts(paths)
	if err != nil {
		return agent.Needs{}, err
	}
	r := rewriter{agent: a, paths: paths}
	if err := r.rewrite(args[1:]); err != nil {
		return agent.Needs{}, err
	}
	needs := agent.NewNeeds(r.command()).WithVars(declared).WithPlace(place)
	for _, f := range r.files {
		needs = needs.WithFile(f.name, f.content)
	}
	if !r.settings.IsZero() {
		document, cb, hooked, dropped, err := carry(r.settings)
		switch {
		case err != nil:
			return agent.Needs{}, err
		case hooked:
			needs = needs.WithHookedSettings(settingsFile, document, cb)
		default:
			needs = needs.WithSettings(settingsFile, document)
		}
		needs = needs.WithDroppedHooks(dropped...)
	}
	return needs, nil
}

// adapterScript runs claude-agent-acp with the claude found on the remote's
// PATH: the adapter is installed without the Claude it would otherwise bundle,
// and the caller's own CLAUDE_CODE_EXECUTABLE names a local path that is never
// carried.
const adapterScript = `CLAUDE_CODE_EXECUTABLE=${CLAUDE_CODE_EXECUTABLE:-$(command -v claude)}
export CLAUDE_CODE_EXECUTABLE
exec claude-agent-acp`

// Adapter is what claude-agent-acp needs on the remote. It reads the
// remote's transcripts where they are, so it needs no place, and a chat's
// settings come over ACP, so it needs no file.
func (Agent) Adapter(pathmap.Map) (agent.Needs, error) {
	return agent.NewNeeds([]string{"sh", "-c", adapterScript, "claude-agent-acp"}).WithVars(declared), nil
}

// transcripts is the place that joins the local directory where Claude keeps
// the conversations of cfg.Dir to the one where Claude on the remote keeps
// those of its mapped directory.
func (a Agent) transcripts(paths pathmap.Map) (pathmap.Place, error) {
	for _, dir := range []string{a.cfg.Dir, a.cfg.ConfigDir, a.cfg.RemoteConfigDir} {
		if !path.IsAbs(dir) {
			return pathmap.Place{}, fmt.Errorf("claude: directory %q is not absolute", dir)
		}
	}
	return pathmap.Place{
		Local:  transcriptDir(a.cfg.ConfigDir, a.cfg.Dir),
		Remote: transcriptDir(a.cfg.RemoteConfigDir, paths.ToRemote(path.Clean(a.cfg.Dir))),
	}, nil
}

// projectDirName is the name Claude gives a directory under
// `<config>/projects/`: its absolute path with every character that is not a
// letter or a digit replaced by '-'.
func projectDirName(dir string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		default:
			return '-'
		}
	}, dir)
}

// transcriptDir is where Claude keeps the transcripts of the conversations
// run in dir: under its config directory, named after dir.
func transcriptDir(configDir, dir string) string {
	return path.Join(configDir, "projects", projectDirName(dir))
}

// word is one argument of the command on the remote: the caller's, mapped, or
// the path of a file in the worker's directory, which only the remote knows.
type word struct {
	text string
	file bool
}

type file struct {
	name    string
	content secret.Token
}

// rewriter carries a conversation's arguments to the remote.
type rewriter struct {
	agent    Agent
	paths    pathmap.Map
	words    []word
	files    []file
	settings secret.Token
}

func (r *rewriter) literal(texts ...string) {
	for _, t := range texts {
		r.words = append(r.words, word{text: t})
	}
}

func (r *rewriter) rewrite(words []string) error {
	for i := 0; i < len(words); i++ {
		name, value, inline := splitOption(words[i])
		switch {
		case name == "--":
			r.literal("--")
			r.literal(mapWords(words[i+1:], r.paths)...)
			return nil
		case name == "--settings" && (inline || i+1 < len(words)):
			if !inline {
				i++
				value = words[i]
			}
			doc, err := r.document("--settings", value)
			if err != nil {
				return err
			}
			r.settings = doc
			r.literal("--settings")
			r.words = append(r.words, word{text: settingsFile, file: true})
		case name == "--mcp-config":
			var values []string
			if inline {
				values = []string{value}
			} else {
				end := skipValues(words, i)
				values = words[i+1 : end+1]
				i = end
			}
			if len(values) == 0 {
				r.literal(words[i])
				continue
			}
			r.literal("--mcp-config")
			for _, v := range values {
				doc, err := r.document("--mcp-config", v)
				if err != nil {
					return err
				}
				f := file{name: mcpPrefix + strconv.Itoa(len(r.files)+1) + ".json", content: doc}
				r.files = append(r.files, f)
				r.words = append(r.words, word{text: f.name, file: true})
			}
		case inline:
			r.literal(name + "=" + mapWord(value, r.paths))
		default:
			r.literal(mapWord(words[i], r.paths))
		}
	}
	return nil
}

// document is the JSON document an option's value gives, inline or in a local
// file, with its paths mapped. It may hold credentials, so it stays masked.
func (r *rewriter) document(option, value string) (secret.Token, error) {
	content := []byte(value)
	if !isInline(value) {
		if r.agent.cfg.ReadFile == nil {
			return secret.Token{}, fmt.Errorf("claude: %s %s: no reader of local files", option, value)
		}
		name := value
		if !path.IsAbs(name) {
			name = path.Join(r.agent.cfg.Dir, name)
		}
		read, err := r.agent.cfg.ReadFile(name)
		if err != nil {
			return secret.Token{}, fmt.Errorf("claude: %s: %w", option, err)
		}
		content = read
	}
	mapped, err := pathmap.JSON(content, r.paths.ToRemote)
	if err != nil {
		return secret.Token{}, fmt.Errorf("claude: %s %s: %w", option, describe(value), err)
	}
	return secret.FromString(string(mapped)), nil
}

// describe names an option's value in an error: a file by its path, and an
// inline document not at all, since it may hold a credential.
func describe(value string) string {
	if isInline(value) {
		return "(inline)"
	}
	return value
}

// command is the command that runs Claude on the remote. Without files it is
// Claude's argv itself. A file's path is known only to the worker, so then a
// shell joins each to the variable that holds the worker's directory; the
// caller's words reach Claude as the shell's positional parameters, so none of
// them is ever read as shell text.
func (r *rewriter) command() []string {
	hasFile := false
	for _, w := range r.words {
		hasFile = hasFile || w.file
	}
	if !hasFile {
		out := []string{"claude"}
		for _, w := range r.words {
			out = append(out, w.text)
		}
		return out
	}
	script := []string{"exec claude"}
	var params []string
	for _, w := range r.words {
		if w.file {
			script = append(script, `"${`+agent.DirVar+`:?}/`+w.text+`"`)
			continue
		}
		params = append(params, w.text)
		script = append(script, `"${`+strconv.Itoa(len(params))+`}"`)
	}
	return append([]string{"sh", "-c", strings.Join(script, " "), "claude"}, params...)
}

// isInline reports whether an option's value is a JSON document rather than a
// file path: Claude takes either for `--settings` and `--mcp-config`. A
// malformed document is still one, so it is refused as a document and never
// looked up, or named in an error, as a path.
func isInline(value string) bool { return strings.HasPrefix(strings.TrimSpace(value), "{") }

func mapWords(words []string, paths pathmap.Map) []string {
	out := make([]string, len(words))
	for i, w := range words {
		out[i] = mapWord(w, paths)
	}
	return out
}

// mapWord maps one argument: a JSON value through every string inside it, any
// other word as a path.
func mapWord(w string, paths pathmap.Map) string {
	trimmed := strings.TrimSpace(w)
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		if mapped, err := pathmap.JSON([]byte(w), paths.ToRemote); err == nil {
			return string(mapped)
		}
	}
	return paths.ToRemote(w)
}

// skipValues is the index of the last value of the variadic option at
// words[i], given without '=': every following word up to one that looks like
// an option, as Claude's parser reads it. Given as `--option=value`, the
// option takes that one value and the next word is not its.
func skipValues(words []string, i int) int {
	for i+1 < len(words) && !looksLikeOption(words[i+1]) {
		i++
	}
	return i
}

func looksLikeOption(w string) bool { return len(w) > 1 && w[0] == '-' }
