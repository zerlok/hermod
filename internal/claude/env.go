package claude

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"github.com/zerlok/hermod/internal/secret"
)

// The variables of Claude's own that Hermod reads or sets on this machine.
const (
	// EnvConfigDir is Claude's config directory, where it keeps its transcripts;
	// ~/.claude when unset.
	EnvConfigDir = "CLAUDE_CONFIG_DIR"
	// EnvExecutable is the claude the Agent SDK runs.
	EnvExecutable = "CLAUDE_CODE_EXECUTABLE"
)

// localOnlyEnv are the variables of Claude's own that belong to this machine
// or to one process on it, so on the sandbox they would point at nothing, or at
// the wrong thing:
//
//   - the executable Hermod stands in for and the local config directory;
//   - what a running Claude sets for the programs it starts (its executable
//     path, PID, session, messaging socket and token, IDE port, project
//     directory and hook env file), present when Hermod itself runs under a
//     Claude.
//
// Every other `CLAUDE_*` variable is the caller's choice for the session, as
// the Agent SDK's are, and is carried.
var localOnlyEnv = map[string]bool{
	EnvExecutable: true,
	EnvConfigDir:  true,

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

// Environment selects, from the caller's environment, the variables Claude
// reads that the caller set for this session: `CLAUDE_*`, `ANTHROPIC_*` and
// `MAX_THINKING_TOKENS`, apart from the local-only ones. The entries keep their
// order and stay masked: ANTHROPIC_API_KEY and the like are credentials.
func Environment(environ secret.Env) secret.Env {
	return environ.Filter(func(key, _ string) bool {
		if localOnlyEnv[key] {
			return false
		}
		return strings.HasPrefix(key, "CLAUDE_") || strings.HasPrefix(key, "ANTHROPIC_") || key == "MAX_THINKING_TOKENS"
	})
}

// ProjectDirName is the name Claude gives a project's directory under
// `<config>/projects/`: the project's absolute path with every character that
// is not a letter or a digit replaced by '-'.
func ProjectDirName(projectDir string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		default:
			return '-'
		}
	}, projectDir)
}

// TranscriptDir is the directory where Claude keeps the transcripts of the
// sessions run in projectDir: under its config directory, named after
// projectDir.
func TranscriptDir(configDir, projectDir string) string {
	return path.Join(configDir, "projects", ProjectDirName(projectDir))
}

// TranscriptPath is where Claude keeps a session's transcript, in the
// TranscriptDir of the session's working directory.
func TranscriptPath(configDir, projectDir string, id SessionID) string {
	return path.Join(TranscriptDir(configDir, projectDir), id.String()+".jsonl")
}

// Running reports whether the output of `claude agents --json`, the sessions
// Claude has running, lists the session.
func Running(agentsJSON []byte, id SessionID) (bool, error) {
	var agents []struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(agentsJSON, &agents); err != nil {
		return false, fmt.Errorf("claude agents --json: %w", err)
	}
	for _, a := range agents {
		if strings.EqualFold(a.SessionID, id.String()) {
			return true, nil
		}
	}
	return false, nil
}
