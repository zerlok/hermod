package claude

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/zerlok/hermod/internal/agent"
	"github.com/zerlok/hermod/internal/env"
	"github.com/zerlok/hermod/internal/pathmap"
	"github.com/zerlok/hermod/internal/secret"
	"github.com/zerlok/hermod/internal/shell"
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

// localOnlyEnv are the variables of Claude's own that belong to the caller's
// workstation or to one process on it, so where the start runs they would
// point at nothing, or at the wrong thing:
//
//   - the executable Hermod stands in for and the caller's config directory;
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

// Start runs the conversation the Launch's arguments name through ex.
//
// The command is the caller's argv with every local path mapped, run by
// `claude` found on PATH where ex runs it, with no `--name` added: Claude's
// session name is what the user sees. The documents `--settings` and
// `--mcp-config` name, files or inline JSON, are placed in the worker's
// directory through the Launch, their paths mapped, and the options name the
// placed files, so a credential in them stays off the command line. The
// settings' hooks are rewritten by the Launch's function first. Only the
// variables Claude declares are taken from the caller's.
func (a Agent) Start(ctx context.Context, ex shell.Executor, l agent.Launch) error {
	r, err := a.read(l)
	if err != nil {
		return err
	}
	if !r.settings.IsZero() {
		doc, err := rewriteHooks(r.settings, l.Hook)
		if err != nil {
			return err
		}
		r.settings = doc
	}
	args, err := r.command(l.Place)
	if err != nil {
		return err
	}
	return run(ctx, ex, shell.Command{
		Args: args,
		Dir:  l.Paths.ToRemote(path.Clean(l.Dir)),
		Env:  l.Environ.Select(declared),
	})
}

// Hooks are the `type: http` hooks of the settings the Launch's arguments
// name, in the order Start hands them to the Launch's function.
func (a Agent) Hooks(l agent.Launch) ([]agent.Hook, error) {
	r, err := a.read(l)
	if err != nil || r.settings.IsZero() {
		return nil, err
	}
	var hooks []agent.Hook
	_, err = rewriteHooks(r.settings, func(h agent.Hook) (agent.Hook, bool) {
		hooks = append(hooks, h)
		return h, true
	})
	return hooks, err
}

// read carries the arguments of the conversation a Launch names to where it
// runs, reading the documents they name.
func (a Agent) read(l agent.Launch) (rewriter, error) {
	if invocation, _ := a.Classify(l.Args); invocation != agent.InvokesConversation {
		// The arguments are not named: an inline document may hold a credential.
		return rewriter{}, fmt.Errorf("claude: the arguments are a %s, not a conversation Claude runs", invocation)
	}
	if !path.IsAbs(l.Dir) {
		return rewriter{}, fmt.Errorf("claude: directory %q is not absolute", l.Dir)
	}
	r := rewriter{agent: a, dir: l.Dir, paths: l.Paths}
	return r, r.rewrite(l.Args[1:])
}

// adapterProgram is Claude's ACP adapter. It is installed without the Claude
// it would otherwise bundle, so it is told which one to run.
const adapterProgram = "claude-agent-acp"

// Adapter runs claude-agent-acp through ex in the Launch's directory, mapped,
// with the Claude that the executor this Agent is built with finds: the
// caller's own CLAUDE_CODE_EXECUTABLE names a path of the caller's and is never
// carried. The adapter reads the transcripts where Claude keeps them there,
// and a chat's settings come over ACP, so it is given no file.
func (a Agent) Adapter(ctx context.Context, ex shell.Executor, l agent.Launch) error {
	if !path.IsAbs(l.Dir) {
		return fmt.Errorf("claude: directory %q is not absolute", l.Dir)
	}
	executable, err := a.report(ctx, "executable")
	if err != nil {
		return err
	}
	return run(ctx, ex, shell.Command{
		Args: []string{adapterProgram},
		Dir:  l.Paths.ToRemote(path.Clean(l.Dir)),
		Env:  l.Environ.Select(declared).With(env.Pair{Key: envExecutable, Value: env.Plain(executable)}),
	})
}

// run executes a start command through ex to its end, with no input: the
// started program's stdin is not the start command's.
func run(ctx context.Context, ex shell.Executor, cmd shell.Command) error {
	out, err := shell.Run(ctx, ex, cmd)
	if err != nil {
		if detail := strings.TrimSpace(out.Stderr); detail != "" {
			err = fmt.Errorf("%w: %s", err, detail)
		}
		return fmt.Errorf("claude: start: %w", err)
	}
	return nil
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

// word is one argument of the start command: the caller's, mapped, or the name
// of a file placed in the worker's directory, whose path only the placing
// gives.
type word struct {
	text string
	file bool
}

type file struct {
	name    string
	content secret.Token
}

// rewriter carries a conversation's arguments to where it runs.
type rewriter struct {
	agent    Agent
	dir      string // the caller's directory, which a relative file is under
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
			name = path.Join(r.dir, name)
		}
		read, err := r.agent.cfg.ReadFile(name)
		if err != nil {
			return secret.Token{}, fmt.Errorf("claude: %s: %w", option, err)
		}
		content = read
	}
	mapped, err := mapJSON(content, r.paths)
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

// command is Claude's argv, each file placed through place and named by the
// path it returns: the settings first, then the MCP configuration in order.
func (r *rewriter) command(place func(name string, content secret.Token) string) ([]string, error) {
	files := r.files
	if !r.settings.IsZero() {
		files = append([]file{{name: settingsFile, content: r.settings}}, files...)
	}
	if len(files) > 0 && place == nil {
		return nil, errors.New("claude: no place for the conversation's files")
	}
	placed := make(map[string]string, len(files))
	for _, f := range files {
		placed[f.name] = place(f.name, f.content)
	}
	out := []string{Program}
	for _, w := range r.words {
		if w.file {
			out = append(out, placed[w.text])
		} else {
			out = append(out, w.text)
		}
	}
	return out, nil
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
		if mapped, err := mapJSON([]byte(w), paths); err == nil {
			return string(mapped)
		}
	}
	return paths.ToRemote(w)
}

// mapJSON maps every string of one JSON document to the remote. A document
// with no path in it is kept as written, since re-encoding would reformat the
// caller's own text, such as a prompt.
func mapJSON(doc []byte, paths pathmap.Map) ([]byte, error) {
	v, err := decodeDocument(doc)
	if err != nil {
		return nil, err
	}
	changed := false
	v = pathmap.Value(v, func(s string) string {
		out := paths.ToRemote(s)
		changed = changed || out != s
		return out
	})
	if !changed {
		return doc, nil
	}
	return marshal(v)
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
