package claude

import (
	"context"
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

// fileVarPrefix, followed by a file's position, is the variable that carries
// one file's contents to the start command, which writes it into the worker's
// directory: a variable reaches where the start runs in the worker's env file,
// never on a command line.
const fileVarPrefix = "HERMOD_CLAUDE_FILE_"

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
// `--mcp-config` name, files or inline JSON, are carried as variables, their
// paths mapped, and the start command writes them into the worker's directory
// and names those files in the options, so a credential in them stays off the
// command line. The settings' hooks are rewritten by the Launch's function
// first. Only the variables Claude declares are taken from the caller's.
func (a Agent) Start(ctx context.Context, ex shell.Executor, l agent.Launch) error {
	if invocation, _ := a.Classify(l.Args); invocation != agent.InvokesConversation {
		// The arguments are not named: an inline document may hold a credential.
		return fmt.Errorf("claude: the arguments are a %s, not a conversation Claude runs", invocation)
	}
	if !path.IsAbs(l.Dir) {
		return fmt.Errorf("claude: directory %q is not absolute", l.Dir)
	}
	r := rewriter{agent: a, dir: l.Dir, paths: l.Paths}
	if err := r.rewrite(l.Args[1:]); err != nil {
		return err
	}
	if !r.settings.IsZero() {
		doc, err := rewriteHooks(r.settings, l.Hook)
		if err != nil {
			return err
		}
		r.settings = doc
	}
	args, files := r.command()
	return run(ctx, ex, shell.Command{
		Args: args,
		Dir:  l.Paths.ToRemote(path.Clean(l.Dir)),
		Env:  l.Environ.Select(declared).With(files...),
	})
}

// adapterScript runs claude-agent-acp with the claude found on PATH: the
// adapter is installed without the Claude it would otherwise bundle, and the
// caller's own CLAUDE_CODE_EXECUTABLE names a path of the caller's that is
// never carried.
const adapterScript = `CLAUDE_CODE_EXECUTABLE=${CLAUDE_CODE_EXECUTABLE:-$(command -v claude)}
export CLAUDE_CODE_EXECUTABLE
exec claude-agent-acp`

// Adapter runs claude-agent-acp through ex in the Launch's directory, mapped.
// It reads the transcripts where Claude keeps them there, and a chat's
// settings come over ACP, so it is given no file.
func (Agent) Adapter(ctx context.Context, ex shell.Executor, l agent.Launch) error {
	if !path.IsAbs(l.Dir) {
		return fmt.Errorf("claude: directory %q is not absolute", l.Dir)
	}
	return run(ctx, ex, shell.Command{
		Args: []string{"sh", "-c", adapterScript, "claude-agent-acp"},
		Dir:  l.Paths.ToRemote(path.Clean(l.Dir)),
		Env:  l.Environ.Select(declared),
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

// word is one argument of the start command: the caller's, mapped, or the path
// of a file in the worker's directory, which only the command itself knows
// where it runs.
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

// command is the command that runs Claude, and the variables that carry its
// files. Without files it is Claude's argv itself. A file's path is known only
// where the worker runs, so then a shell first writes each file from its
// variable into the directory env.WorkerDir names, private to the user, and
// unsets the variable, so Claude's own environment does not hold it; the
// caller's words reach Claude as the shell's positional parameters, so none of
// them is ever read as shell text.
func (r *rewriter) command() ([]string, []env.Pair) {
	files := r.files
	if !r.settings.IsZero() {
		files = append([]file{{name: settingsFile, content: r.settings}}, files...)
	}
	if len(files) == 0 {
		out := []string{"claude"}
		for _, w := range r.words {
			out = append(out, w.text)
		}
		return out, nil
	}
	script := []string{`d="${` + env.WorkerDir + `:?}"`, "umask 077"}
	vars := make([]env.Pair, 0, len(files))
	for i, f := range files {
		name := fileVarPrefix + strconv.Itoa(i+1)
		script = append(script,
			`printf '%s' "$`+name+`" >"$d/`+f.name+`" || exit`,
			"unset "+name)
		vars = append(vars, env.Pair{Key: name, Value: env.Secret(f.content)})
	}
	exec := []string{"exec claude"}
	var params []string
	for _, w := range r.words {
		if w.file {
			exec = append(exec, `"$d/`+w.text+`"`)
			continue
		}
		params = append(params, w.text)
		exec = append(exec, `"${`+strconv.Itoa(len(params))+`}"`)
	}
	script = append(script, strings.Join(exec, " "))
	return append([]string{"sh", "-c", strings.Join(script, "\n"), "claude"}, params...), vars
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
