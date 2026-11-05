# JetBrains Air — remote agent integration

This document records how [JetBrains Air](https://www.jetbrains.com/air/) runs Claude Code, what a bridge
must do to run that agent on a Hermod sandbox while Air stays local, and which of those requirements a working
prototype has already proven. It is the input for an OpenSpec change that brings the bridge into Hermod.

The prototype is [`air-remote-claude`](../air-remote-claude), a bash script standing in for the `claude`
executable. It talks to the sandbox with plain `ssh` and `mutagen`, without Hermod, so every mechanism below can be
read in one file.

Versions observed: GoLand 2026.3 with the Air plugin, `@agentclientprotocol/claude-agent-acp` 0.101.0,
`@anthropic-ai/claude-agent-sdk` 0.3.318, Claude Code 2.1.318 on the sandbox.

---

## Two ways Air runs Claude

Air drives Claude in one of two modes. Both start a `claude` process **locally**, in the project directory, so
replacing that one executable is enough to move the agent to the sandbox.

### Terminal mode

Claude's own terminal UI in an IDE tab. Air starts it as:

```
claude --session-id <uuid> --settings <ide-cache>/claude-hooks/settings-<hash>.json   # new chat
claude --resume     <uuid> --settings <ide-cache>/claude-hooks/settings-<hash>.json   # reopened chat
```

with `CLAUDE_CODE_AUTO_CONNECT_IDE=false` and `CLAUDE_CODE_IDE_SKIP_AUTO_INSTALL=true` (no IDE lockfile/websocket is
used). Air observes the session only from outside:

- **Hooks.** The settings file registers `type: http` hooks to `http://localhost:<port>/air/claude/hook` with a
  per-run Bearer token: `PreToolUse`/`PostToolUse` for `AskUserQuestion|ExitPlanMode`, `PostToolUse` for
  `Write|Edit|MultiEdit|NotebookEdit`, `Stop`, `Notification`, `UserPromptSubmit`, `SessionStart`, `SessionEnd`.
  Timeouts are 1 s (3 s for `SessionEnd`). The port and token change with every IDE run.
- **Transcripts.** Air lists sessions and resolves "session saved" from
  `~/.claude/projects/<encoded-local-dir>/<uuid>.jsonl` on the local machine.

Terminal mode gives the session list, the activity indicator, "needs input", and adding editor selections to the
context. It has **no** change tracking, cost, diff review or code comments — those exist only in chat mode.

### Chat mode (ACP)

Air's own chat UI talks the [Agent Client Protocol](https://agentclientprotocol.com) to an adapter, which runs
Claude through the Claude Agent SDK.

- **The adapter.** Air's registry entry runs
  `npx @agentclientprotocol/claude-agent-acp@<version> --hide-claude-auth`. That flag makes the adapter refuse any
  session a claude.ai subscription would pay for, so with the stock entry a Claude subscription only gets terminal
  mode.
- **A custom agent instead.** Air reads extra agents from `~/.jetbrains/acp.json` (`agent_servers`, each with
  `command`, `args`, `env`). The same adapter registered there without `--hide-claude-auth` offers the Claude
  subscription. The adapter needs Node ≥ 22 and Air does not load the user's shell, so `command` must be an
  explicit Node binary with the adapter script as its argument.
- **Air features are not tied to the registry entry.** The adapter enables its Air extensions (diff patches, plan
  files, session index, background subagents, …) when the client declares `_meta.jetbrains.air` at `initialize`
  (`isAirClient()`), so a custom-registered adapter gets them too.
- **Choosing the `claude` binary.** The adapter spawns `CLAUDE_CODE_EXECUTABLE` if set, else a CLI shipped next to a
  standalone build, else the SDK's platform package (`@anthropic-ai/claude-agent-sdk-<platform>`, a ~340 MB native
  CLI). With the variable set, the adapter can be installed with `--omit=optional` and carries no second Claude.

Working custom agent:

```json
{
  "agent_servers": {
    "Claude (remote)": {
      "command": "<node ≥ 22>/bin/node",
      "args": ["<node ≥ 22>/bin/claude-agent-acp"],
      "env": { "CLAUDE_CODE_EXECUTABLE": "<path to the bridge>" }
    }
  }
}
```

The SDK starts Claude headless, with no TTY:

```
claude --output-format stream-json --verbose --input-format stream-json
       --permission-prompt-tool=stdio --tools=default
       --mcp-config {"mcpServers":{"jetbrains_air_ide":{"type":"stdio","command":"<ide>/bin/goland",
                     "args":["stdioMcpServer"],"env":{IJ_MCP_SERVER_PORT, IJ_MCP_AUTH_TOKEN, …}}}}
       --setting-sources=user,project,local --permission-mode=auto --allow-dangerously-skip-permissions
       --include-partial-messages --add-dir=<ide-cache>/ai-agent-extensions/agents/air-views/<id>/…
       --session-id=<uuid> --replay-user-messages
```

with SDK environment: `CLAUDE_CODE_ENTRYPOINT=sdk-ts`, `CLAUDE_AGENT_SDK_VERSION`,
`CLAUDE_CODE_ENABLE_SDK_FILE_CHECKPOINTING`, `CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS`,
`CLAUDE_CODE_SDK_READS_SESSION_STATE`, `CLAUDE_CODE_STARTUP_FAILURE_RESULTS`. Stdin and stdout carry
newline-delimited JSON; the SDK opens with a `control_request` `initialize` and waits for its response. Between
prompts the adapter also runs short subcommands through the same executable (e.g. `claude auth status`).

Chat mode gives everything terminal mode does plus per-turn change cards, edit diffs ("Agent Turn" tabs), cost,
and **Review Code** with line comments sent back to the session (the Air docs' flow; observed available, sending
comments from a remote session not yet exercised).

Air also occasionally starts the configured `claude` headless on its own (observed:
`-p --input-format stream-json --output-format stream-json --verbose`, exiting within seconds). Its purpose is not
yet known.

---

## What the bridge must do

Each requirement states the mechanism and its status in the prototype.

### 1. Rewrite the arguments — proven

- `--settings <file>`: the file exists only locally. Pass its contents instead; `--settings` accepts inline JSON.
- Local paths in any argument (`--add-dir`, project paths) are rewritten to their sandbox form.
- `--session-id` / `--resume` pass through unchanged; the UUID is the key for transcripts and session naming.

### 2. Keep stdin for the agent — proven

In chat mode the SDK writes `initialize` the moment the process starts. Any setup command that inherits stdin
(`ssh` forwards stdin by default) consumes it, and the agent then waits forever. All setup must run with stdin from
`/dev/null`; only the final agent process receives the caller's stdin.

### 3. Carry the environment — proven

`ssh` does not forward the caller's environment. The bridge passes `CLAUDE_*`, `ANTHROPIC_*` and
`MAX_THINKING_TOKENS` explicitly (`env K=V … claude`), excluding `CLAUDE_CODE_EXECUTABLE`. On the command line the
values are visible in the sandbox's process list; a later version should pass them over stdin or `SendEnv`.

### 4. Route hook callbacks to the IDE, with paths rewritten — proven

Hook URLs point at `localhost:<port>` on the machine running Claude. The bridge opens the same port on the sandbox
with `ssh -R <port>:localhost:<proxy>` and points it at a **local proxy** in front of Air. Claude on the sandbox
reports sandbox paths (`cwd`, `transcript_path`, `tool_input.file_path`, …), which Air cannot place in its project,
so the proxy rewrites every JSON string sandbox → local on requests and local → sandbox on responses, and passes the
`Authorization` header through. Observed latency through the tunnel is 2–100 ms, well inside the 1 s timeouts.

The settings stay untouched: the sandbox keeps Air's port number. `ControlPath=none` is required on that `ssh`; a
shared master connection silently drops the new `-R` forwards.

### 5. Rewrite the stream — proven

In chat mode Air reads tool calls and file paths from Claude's stdout, not from hooks. The bridge rewrites each
stdout line sandbox → local and each stdin line local → sandbox, unbuffered, line by line.

### 6. Mirror the project — proven

A two-way Mutagen session between the local project and the sandbox directory (local wins conflicts). Edits made on
the sandbox reach the editor through the mirror; Air's change tracking comes from the hook/stream events, not from
the files.

### 7. Bring transcripts back — proven

Claude names the transcript directory after its working directory, every non-alphanumeric character replaced by
`-`, so the two ends differ:

```
local:   ~/.claude/projects/-home-<user>-dev-<project>/
sandbox: ~/.claude/projects/-home-<sandbox-user>-dev-<project>/
```

A one-way Mutagen session sandbox → local in `one-way-safe` mode (never deletes local-only transcripts) makes the
sessions visible and resumable in Air. Resuming a chat that exists only locally needs that one file copied up
first. Transcript contents keep sandbox paths; nothing observed so far depends on them.

### 8. Pass utility subcommands straight through — proven

`auth`, `mcp`, `--version` and similar run directly on the sandbox, without mirror setup, and use the sandbox's
Claude login.

### 9. Bridge MCP servers that run local programs — open

Air hands the agent a stdio MCP server (`jetbrains_air_ide`) whose command is the local IDE binary, connecting to
the IDE on a local port with a token. On the sandbox it fails to start and Air's IDE tools are missing from the
session. Proposed bridge: rewrite each local-command server in `--mcp-config` into a sandbox-side relay that pipes
stdio over a reverse-forwarded unix socket to the real command, started locally on demand. The IDE port and token
never leave the local machine.

### 10. Local directories passed with `--add-dir` — open

Air adds `<ide-cache>/ai-agent-extensions/agents/air-views/<id>/…`, which exists only locally. Claude starts without
it. Whether Air writes files there for the agent is not yet known; if it does, they need a mirror of their own.

### 11. Concurrent sessions — open

- **Hook port.** Every session of an IDE run uses the same port, but a sandbox port can be bound by only one
  `ssh -R`. A second concurrent session silently loses its hooks. Each session needs its own sandbox port, which
  means rewriting the port in the inlined settings.
- **Mirror.** All sessions of a project share one Mutagen mirror; pause/teardown must wait for the last of them.
- **Naming.** Session identity is Air's UUID, not the directory slug.

### 12. Durability — open

The prototype runs the agent directly over `ssh`; a dropped connection ends the session. Terminal mode can sit in
tmux as Hermod does today. Chat mode is a stdio pipe with no terminal, so surviving a dropped connection would need
the agent process detached from the connection and stdio re-attached on reconnect.

---

## Feature status

| Air feature                                  | Terminal mode | Chat mode | Depends on |
|----------------------------------------------|:-------------:|:---------:|------------|
| Session list, open, resume                   | works         | works     | 7          |
| Activity, "needs input", unread              | works         | works     | 4 / 5      |
| Editor selection as context                  | works         | works     | —          |
| Edits visible in the editor                  | works         | works     | 6          |
| Per-turn changes, edit diffs                 | n/a           | works     | 5          |
| Cost                                         | n/a           | works     | 5          |
| Review Code, line comments to the session    | n/a           | available | 5, 6       |
| Air IDE tools (`jetbrains_air_ide`)          | n/a           | missing   | 9          |
| Several sessions at once                     | breaks hooks  | untested  | 11         |

---

## Fit with Hermod

The bridge is the same composition Hermod already owns — mirror, ssh, a reverse-forwarded channel — run
non-interactively under an agent's executable name instead of attached to a terminal:

- **`mirror`** — already opens/resumes/pauses a project mirror; needs sharing across concurrent sessions and a second,
  one-way session for transcripts.
- **`sandbox.Channel`** — already carries a reverse-forwarded unix socket for notifications; hooks (TCP) and MCP
  relays (unix socket) are more endpoints of the same kind.
- **`notify`** — already serves HTTP on a channel locally; the hook proxy is a second handler of that shape.
- **`shell`** — the headless path needs stdio passed through untouched, which `Command` cannot express today (the
  [remote-agent](../openspec/changes/add-remote-agent/proposal.md) change already adds a stdin seam).
- **Path rewriting** is new: one local ↔ sandbox mapping (project dir, transcript dir) applied to arguments, hook
  bodies and the stdio stream.
- **The sandbox-side relay** for MCP (9) is a natural job for the Hermod agent binary that
  [remote-agent](../openspec/changes/add-remote-agent/proposal.md) installs on the box.

An entry point that Air (or any SDK host) can use as `CLAUDE_CODE_EXECUTABLE`, and that a terminal host can use as
its `claude`, would take the agent's own arguments verbatim and read sandbox and project from configuration.

---

## Notes

- **Subscription use.** Air ships the adapter with `--hide-claude-auth`, and Anthropic's Agent SDK documentation
  states that third-party products may not offer claude.ai login without approval. Running the adapter without the
  flag uses a Claude subscription outside what Air ships; anyone adopting this should check Anthropic's terms.
- **Secrets in transit.** Air's hook settings carry a Bearer token, and its MCP config an IDE token. Both are
  per-IDE-run and local-only by design; the bridge must not log them in clear or place them on the sandbox beyond the
  agent's own process. (The prototype's log, mode `0600`, still records argv and so the MCP token.)
