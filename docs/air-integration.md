# JetBrains Air — remote agent integration

This document records how [JetBrains Air](https://www.jetbrains.com/air/) runs Claude Code, what a bridge
must do to run that agent on a Hermod sandbox while Air stays local, which of those requirements a working
prototype has already proven, and the architecture chosen for bringing the bridge into Hermod. It is the input for
an OpenSpec change that does so.

The prototype is [`air-remote-claude`](../air-remote-claude), a bash script standing in for the `claude`
executable. It talks to the sandbox with plain `ssh` and `mutagen`, without Hermod, so every mechanism below can be
read in one file. The [session lifecycle](#session-lifecycle) was recorded with [`air-probe`](../air-probe): a
stand-in `claude` that answers without a model, and a tap on the ACP stream between Air and the adapter.

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

Air also runs the configured `claude` itself, outside any session, to read Claude's settings: the IDE starts
`claude -p --input-format stream-json --output-format stream-json --verbose` in the project directory with no
`CLAUDE_*` environment, writes one `get_settings` control request, and closes stdin.

---

## Session lifecycle

Observed in chat mode. Air runs one adapter per IDE run for all chats; the adapter runs one `claude` per chat.

| Air action         | ACP request, Air → adapter                                     | The chat's `claude` gets       |
|--------------------|----------------------------------------------------------------|--------------------------------|
| New chat           | `session/new`                                                  | started before any prompt      |
| Reopen a chat      | `session/resume`                                               | started with `--resume=<uuid>` |
| Rename             | `_session/rename`                                              | `rename_session` request       |
| Change mode        | `session/set_config_option`                                    | `set_permission_mode` request  |
| Close tabs         | `session/close` per chat                                       | `interrupt` request, stdin EOF |
| Mark done          | `session/close` if open, then `_session/archive`               | only the close                 |
| Reopen a done chat | `_session/unarchive`, then `session/resume`                    | as for a reopen                |
| Quit the IDE       | `session/close` per open chat, then EOF and SIGTERM to adapter | as for a close                 |

- **A close always ends the process the same way.** After stdin EOF the SDK waits 2 s, sends SIGTERM, and 5 s later
  SIGKILL. On an IDE quit the adapter exits right after the closes: it sends SIGTERM to every `claude` left and does
  not wait, so a process that ignores SIGTERM keeps running, orphaned.
- **Done reaches only the adapter.** `_session/archive` arrives after the chat's `claude` has exited, so a `claude`
  stand-in cannot tell a closed tab, a quit IDE and "mark done" apart. "Mark done" is the adapter appending
  `custom-title` and `agent-name` records with an `[archived] ` prefix to the local transcript. Opening a done chat
  clears the mark.
- **Air offers no delete.** The adapter implements `session/delete` (it removes the transcript), but Air's UI has no
  action that sends it.
- **The IDE can vanish without a shutdown.** An adapter's ACP stream once stopped between two `session/list` polls
  with no EOF, signal or exit, and the next IDE run had a new process. What a running chat sees then was not
  observed.
- Air polls `session/list` every 60 s for each open project directory.

Terminal mode's lifecycle is not observed yet.

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
Claude login. Air's own `-p` settings read belongs here too: it carries no session, so it opens no tmux session, and
it reports the sandbox's settings.

### 9. MCP servers that run local programs — decided: not bridged

Air hands the agent a stdio MCP server (`jetbrains_air_ide`) whose command is the local IDE binary, connecting to
the IDE on a local port with a token. On the sandbox it fails to start and Air's IDE tools are missing from the
session. A session keeps running after Air detaches, so its tools cannot depend on the local machine; see
[MCP servers run on the sandbox only](#mcp-servers-run-on-the-sandbox-only).

### 10. Local directories passed with `--add-dir` — open

Air adds `<ide-cache>/ai-agent-extensions/agents/air-views/<id>/…`, which exists only locally. Claude starts without
it. Whether Air writes files there for the agent is not yet known; if it does, they need a mirror of their own.

### 11. Concurrent sessions — decided

- **Hook port.** Every session of an IDE run uses the same port, but a sandbox port can be bound by only one
  `ssh -R`. A second concurrent session silently loses its hooks. See
  [Hooks: a sandbox port per session](#hooks-a-sandbox-port-per-session).
- **Mirror.** Several sessions can share one Mutagen mirror; pause/teardown must wait for the last of them. See
  [Mirrors cover roots](#mirrors-cover-roots-worktrees-resolve-by-path) and
  [Mirrors pause when nobody is attached](#mirrors-pause-when-nobody-is-attached).
- **Naming.** Session identity is Air's UUID, not the directory slug.

### 12. Durability — decided

The prototype runs the agent directly over `ssh`; a dropped connection ends the session. See
[A session is a tmux session](#a-session-is-a-tmux-session-and-only-the-user-ends-it).

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
| Air IDE tools (`jetbrains_air_ide`)          | n/a           | not supported | 9      |
| Several sessions at once                     | breaks hooks  | untested  | 11         |

---

## Design decisions

**Air is a terminal.** It attaches to and detaches from an agent session that lives on the sandbox, the way
`hermod <sandbox>` attaches to tmux today, while the IDE still shows the session's file changes locally and receives
every event it needs. Everything that rewrites, proxies or decides runs in the local Hermod; the sandbox runs Claude
in tmux on mirrored files.

```
LOCAL (all of Hermod)                                    SANDBOX (nothing of Hermod installed)
 Air chat/terminal ─stdio─► hermod (claude stand-in)       sshd
                            ├ path map: argv, stream  ◄═ssh═►  tmux hermod-<uuid> ─ claude
                            ├ hook proxy → Air        ◄═-R══        └ MCP servers from the sandbox's config
                            └ lease + settle per mirror        ~/.hermod/s/<uuid>/   session state only
 mutagen: project mirrors ⇄, transcript mirrors ◄         mirrored roots, worktrees inside or beside them
```

An entry point that Air (or any SDK host) can use as `CLAUDE_CODE_EXECUTABLE`, and that a terminal host can use as
its `claude`, takes the agent's own arguments verbatim and reads sandbox and project from configuration.

Where it lands in Hermod's packages:

- **`mirror`** — already opens, resumes and pauses a mirror; gains sharing across sessions and one-way transcript
  mirrors.
- **`sandbox`** — the tmux session becomes per agent session rather than per directory, and `Channel` gains TCP
  reverse forwards for hooks beside its unix sockets.
- **`notify`** — already serves HTTP on a channel locally; the hook proxy is a second handler of that shape.
- **`shell`** — the stand-in passes stdio through, which `Command` cannot express today.
- **Path mapping** is new: one local ↔ sandbox mapping applied to arguments, hook bodies and the stream.

### Nothing of Hermod's runs on the sandbox

The sandbox needs `sshd`, `tmux`, `claude`, a POSIX shell with coreutils (`mkfifo`, `tail`) and git ≥ 2.48. Hermod
sends short shell commands over `ssh` on each run, as it already does for tmux, and installs nothing. A sandbox-side
helper — a per-session process holding the agent's stdio, or a resident daemon — was rejected: either one needs a
Hermod binary matching the sandbox's platform, an upgrade path while sessions run, and turns Hermod into server
software instead of the connection to one. The Air bridge therefore does not depend on
[remote-agent](../openspec/changes/add-remote-agent/proposal.md).

### A session is a tmux session, and only the user ends it

Each Air session runs in a tmux session named `hermod-<uuid>`, so it survives the connection in both modes:

- **Terminal mode** is today's attach: `ssh -t <sandbox> tmux new-session -A -s hermod-<uuid> -- claude …`.
- **Chat mode** cannot pass its stream through tmux — a pty alters the bytes and an attach replays a screen, not
  the output — so tmux only keeps the process alive. Claude reads stdin from a FIFO the tmux session holds open (so
  it never sees EOF) and appends stdout to a log, both in `~/.hermod/s/<uuid>/` (mode `0700`). Attaching tails the
  log and forwards stdin into the FIFO one complete line at a time (`read -r`), so a connection dropped mid-line
  never hands Claude partial JSON. User messages echoed back by `--replay-user-messages` acknowledge input; prompts
  not yet echoed are resent on reattach.

A session is **running** (with zero or more attached clients) or **closed** (Claude stopped, transcript kept,
resumable). A local process ending — a closed tab, a quit IDE, a dropped connection — is always a detach: the
session keeps running, and may be driven meanwhile through Claude's Remote Control. Air's `session/close` is such a
detach, sent on every tab close and IDE quit. Closing happens only by the user's decision, which Air expresses as
"mark done" (`_session/archive`, undone by opening the chat again); see [Session lifecycle](#session-lifecycle).

Deleting a session is out of scope for now, and Air offers no delete. When added, it stops Claude and the session's
tmux session, stops the mirror if no other session uses it, and removes the session's files from the sandbox.

### Reattach attaches to the live session

When Air reopens a session whose tmux session exists, the bridge attaches to the running Claude instead of
starting another: Air loads history from the transcript, the bridge answers the new SDK's `initialize` locally from
the first response in the log, re-delivers control requests still waiting for an answer (permission prompts
raised while detached), and streams the log from its end. Only when the tmux session is gone does the bridge start
`claude --resume <uuid>` in a new one.

### Hooks: a sandbox port per session

The sandbox port for a session's hooks is chosen when the session is created, stored in
`~/.hermod/s/<uuid>/hookport`, inlined into that session's `--settings`, and bound again with `ssh -R` on every
attach. That keeps concurrent sessions apart and a running Claude's settings valid across reconnects. After an IDE
restart Air has a new port and token while the running Claude still has the old ones; the local hook proxy targets
Air's current port and replaces the `Authorization` header. While detached, hooks fail and Air fails open; nothing on
the sandbox depends on them. A port taken by something else while detached costs that session its hooks, logged,
not the session.

### MCP servers run on the sandbox only

A session continues after Air detaches, so its tools must not depend on the local machine. Servers in
`--mcp-config` whose command is a local program (`jetbrains_air_ide`) are removed, and Claude uses the MCP servers
configured on the sandbox (user scope and the mirrored project's `.mcp.json`). Air's IDE tools are not available to
a remote session.

### Mirrors cover roots, worktrees resolve by path

- A session whose directory lies inside an existing mirror root reuses that mirror; a directory outside every root
  (a sibling worktree) gets its own mirror to the corresponding sandbox path.
- One path map — (local root, sandbox root) pairs, longest prefix wins — is applied to arguments, hook bodies and
  the stream.
- Git worktrees use relative paths (git ≥ 2.48, `worktree.useRelativePaths`) on both ends, so their pointer files
  are valid on either side of the mirror. Hermod requires that git version rather than rewriting pointer files;
  the versions seen so far (2.43 local, 2.47 sandbox) need upgrading.
- Claude names a transcript directory after the session's working directory, so each distinct directory gets its
  own one-way transcript mirror.

### Mirrors pause when nobody is attached

Each local Hermod holds a shared lease on every mirror its session uses. When it exits, the last holder settles the
mirror by today's rule: pause while any session under that root is alive (or its liveness is unknown) on the
sandbox, flush and close when none is. A detached session keeps working with its mirrors paused; its edits arrive on
the next attach. This applies to project and transcript mirrors alike.

### File, transcript and hook channels are not synchronized

The project mirror, the transcript mirror and the hook and stream events travel independently. Air can show a change
card before the file reaches the editor, or open a transcript older than the live stream. That is accepted; the
bridge does not order them.

### Open questions

- **Seeing done.** It reaches only the adapter, after the chat's `claude` has exited. A bridge that ends the
  sandbox session on it has to sit on the ACP stream (wrapping the adapter, as `acp-tap` does) or watch the local
  transcript for the archive marker; as the `claude` stand-in alone it never learns of it.
- **Archive marker and the transcript mirror.** "Mark done" writes to the local copy of a transcript that the
  one-way mirror owns. A locally changed file is likely a conflict that stops later sandbox writes from reaching it;
  not yet tested.
- **Terminal mode.** Not observed yet: switching the agent to terminal mode in Air's agent settings still opened
  the chat UI.
- **Attaching to a live chat-mode process.** Whether Air accepts the locally answered `initialize`, the re-delivered
  pending requests, and history from the transcript joined to the live log.
- **Remote Control in chat mode.** Whether a Claude started headless by the SDK can be driven through Remote Control
  while detached.
- **Worktree layout.** Where Air creates worktrees — inside the project root or beside it — which decides how many
  mirrors a project needs.
- **Edits made locally while paused.** Local wins conflicts on resume, so a local edit to a file the detached
  session also changed discards the session's edit.

---

## Notes

- **Subscription use.** Air ships the adapter with `--hide-claude-auth`, and Anthropic's Agent SDK documentation
  states that third-party products may not offer claude.ai login without approval. Running the adapter without the
  flag uses a Claude subscription outside what Air ships; anyone adopting this should check Anthropic's terms.
- **Secrets in transit.** Air's hook settings carry a Bearer token, and its MCP config an IDE token. Both are
  per-IDE-run and local-only by design; the bridge must not log them in clear or place them on the sandbox beyond the
  agent's own process. (The prototype's log, mode `0600`, still records argv and so the MCP token.)
