# JetBrains Air — remote agent integration

This document records how [JetBrains Air](https://www.jetbrains.com/air/) runs Claude Code, what a bridge
must do to run that agent on a remote workstation while Air stays local, and the decisions Hermod's bridge is built on.
The bridge itself — its command line, packages and flows — is the
[`add-air-bridge` design](../openspec/changes/add-air-bridge/design.md); how to set it up is in the
[README](../README.md#jetbrains-air).

Each mechanism below marked proven was first shown to work by a bash script standing in for `claude`, before the
bridge was built into Hermod. The [session lifecycle](#session-lifecycle) was recorded with a stand-in `claude`
that answers without a model, and a tap on the ACP stream between Air and the adapter. What is still unverified is listed under [Open questions](#open-questions).

Versions observed: GoLand 2026.3 with the Air plugin, `@agentclientprotocol/claude-agent-acp` 0.101.0,
`@anthropic-ai/claude-agent-sdk` 0.3.318, Claude Code 2.1.318 on the remote.

---

## Two ways Air runs Claude

Air drives Claude in one of two modes. Both start a `claude` process **locally**, in the project directory, so
replacing that one executable is enough to move the agent to a remote.

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

Working custom agent (the entry Hermod writes is in the
[`add-air-bridge` design](../openspec/changes/add-air-bridge/design.md#hermod-air-configure-claude-remote)):

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

Observed in chat mode. Each running IDE starts one adapter of its own, in a directory of the IDE's cache, for all
chats of all its open projects; the adapter runs one `claude` per chat. A chat names its project directory in
`session/new`; every later message carries only the session ID. Air numbers its requests from 1 on that connection,
in one sequence across projects. The adapter numbers its own requests (`session/request_permission`) from 0, in one
sequence across its chats, and Air answers under the same ID. Two adapters therefore send the same IDs; the count
past the first request, and two adapters side by side, were seen with a test client in place of Air.

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
- Air polls `session/list` every 60 s for each open project directory, all on the IDE's one connection.

Terminal mode's lifecycle is not observed yet.

---

## What the bridge must do

Each requirement states the mechanism and its status. A mechanism marked proven was shown with the stand-in, which
ran the adapter locally and `claude` on the remote; where Hermod's bridge does it another way, the requirement says
so.

### 1. Rewrite the arguments — proven

- `--settings <file>`: the file exists only locally, so its contents must reach the remote. `--settings` accepts
  inline JSON, but Hermod writes them to a file in the worker's directory, keeping them off the command line.
- Local paths in any argument (`--add-dir`, project paths) are rewritten to their remote form.
- `--session-id` / `--resume` pass through unchanged; the UUID is the key of the conversation's worker.

### 2. Keep stdin for the agent — proven

In chat mode the SDK writes `initialize` the moment the process starts. Any setup command that inherits stdin
(`ssh` forwards stdin by default) consumes it, and the agent then waits forever. All setup must run with stdin from
`/dev/null`; only the final agent process receives the caller's stdin.

### 3. Carry the environment — proven

`ssh` does not forward the caller's environment. The bridge passes `CLAUDE_*`, `ANTHROPIC_*` and
`MAX_THINKING_TOKENS` explicitly (`env K=V … claude`), excluding the variables that name local paths
(`CLAUDE_CODE_EXECUTABLE`, `CLAUDE_CONFIG_DIR`). On the command line the values are visible in the remote's
process list, so Hermod writes them to a file in the worker's directory over ssh stdin instead.

### 4. Route hook callbacks to the IDE, with paths rewritten — proven

Hook URLs point at `localhost:<port>` on the machine running Claude. The bridge opens the same port on the remote
with `ssh -R <port>:localhost:<proxy>` and points it at a **local proxy** in front of Air. Claude on the remote
reports remote paths (`cwd`, `transcript_path`, `tool_input.file_path`, …), which Air cannot place in its project,
so the proxy rewrites every JSON string remote → local on requests and local → remote on responses. Observed
latency through the tunnel is 2–100 ms, well inside the 1 s timeouts.

Proven with the remote keeping Air's port number and token and the proxy passing the `Authorization` header
through. Hermod gives each worker a port and a token of its own instead; see
[Hooks: a remote port per worker](#hooks-a-remote-port-per-worker). `ControlPath=none` is required on that
`ssh`; a shared master connection silently drops the new `-R` forwards.

### 5. Rewrite the stream — proven

In chat mode Air reads tool calls and file paths from the agent's output, not from hooks. With the adapter local,
the stand-in rewrote each line of Claude's stdout remote → local and each stdin line local → remote, unbuffered,
line by line. Hermod's bridge runs the adapter on the remote instead and maps the paths in the ACP messages
between Air and the adapter in the same way
([The adapter runs on the remote](../openspec/changes/add-air-bridge/design.md#the-adapter-runs-on-the-remote-one-per-workspace)).

### 6. Relay the project — proven

A two-way Mutagen session between the local project and the remote directory. Edits made on the remote reach the
editor through it; Air's change tracking comes from the hook and stream events, not from the files. It was proven
with local winning conflicts (`two-way-resolved`), which silently discards the remote's side. Hermod's FS relay is
`two-way-safe`, so the remote's changes stay visible and neither side's are lost; see
[the design](../openspec/changes/add-air-bridge/design.md#an-fs-relay-never-picks-a-winner).

### 7. Bring transcripts back — proven

Claude names the transcript directory after its working directory, every non-alphanumeric character replaced by
`-`, so the two ends differ:

```
local:   ~/.claude/projects/-home-<user>-dev-<project>/
remote:  ~/.claude/projects/-home-<remote-user>-dev-<project>/
```

A Mutagen session between those two directories makes a remote conversation visible and resumable in Air's
terminal mode, which lists conversations from the local config directory: `~/.claude`, or `CLAUDE_CONFIG_DIR` when
set. It was proven one-way, remote → local, in `one-way-safe` mode. Hermod carries a terminal session's
transcripts both ways, so a conversation that exists only locally is on the remote before Claude resumes it
([A terminal session's transcripts travel both ways](../openspec/changes/add-air-bridge/design.md#a-terminal-sessions-transcripts-travel-both-ways)).
Chats need no such relay: their adapter reads the remote's transcripts where they are. Transcript contents keep
remote paths; nothing observed so far depends on them.

### 8. Pass utility subcommands straight through — proven

`auth`, `mcp`, `--version` and similar run directly on the remote, without relay setup, and use the remote's
Claude login. Air's own `-p` settings read belongs here too: it carries no conversation, so it starts no worker,
and it reports the remote's settings.

### 9. MCP servers that run local programs — decided: not bridged

Air hands the agent a stdio MCP server (`jetbrains_air_ide`) whose command is the local IDE binary, connecting to
the IDE on a local port with a token. On the remote it fails to start and Air's IDE tools are missing from the
session. A conversation keeps running after Air detaches, so its tools cannot depend on the local machine; see
[MCP servers run on the remote only](#mcp-servers-run-on-the-remote-only).

### 10. Local directories passed with `--add-dir` — open

Air adds `<ide-cache>/ai-agent-extensions/agents/air-views/<id>/…`, which exists only locally. Claude starts without
it. Whether Air writes files there for the agent is not yet known; if it does, they need an FS relay of their own.

### 11. Concurrent sessions — decided

- **Hook port.** Every session of an IDE run uses the same port, but a remote port can be bound by only one
  `ssh -R`. A second concurrent session silently loses its hooks. See
  [Hooks: a remote port per worker](#hooks-a-remote-port-per-worker).
- **Relays.** Several sessions share one project's FS relay, and the last of them to leave decides whether it
  stays, through a lease per relay. See [Mirrors cover roots](#mirrors-cover-roots-worktrees-resolve-by-path) and
  [Relays are suspended while nobody is attached](#relays-are-suspended-while-nobody-is-attached).
- **Naming.** A terminal conversation's worker is named after Air's UUID, not the directory; a project's chats
  share one adapter worker, named after the project's workspace.

### 12. Durability — decided

An agent run directly over `ssh` ends with a dropped connection. See
[A conversation outlives its client, and only the user ends it](#a-conversation-outlives-its-client-and-only-the-user-ends-it).

---

## Feature status

Observed with the bash stand-in that first proved the mechanisms above; Hermod's bridge has not yet been run
against Air as a whole. Where the stand-in fell short, the numbered need under *Depends on* says how the bridge
answers it.

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

**Air is a terminal.** It attaches to and detaches from an agent that lives on the remote, the way `hermod attach`
attaches to a worker, while the IDE still shows the project's file changes locally and receives every event it
needs. Everything that rewrites, routes or decides runs in the local Hermod; the remote runs Claude, and in chat
mode its ACP adapter, in tmux on relayed files.

```
LOCAL (all of Hermod)                                       REMOTE (nothing of Hermod installed)
 Air terminal ─tty─► hermod (claude stand-in)  ◄═ssh═►  tmux hermod-<uuid> ─ claude
 Air chat ─ACP──────► hermod air acp: route, map ◄═ssh═►  tmux hermod-acp-<workspace> ─ claude-agent-acp ─ claude per chat
                      hook filter → Air          ◄═-R══  hook calls from claude
 mutagen: FS relays ⇄ the project, and a terminal session's transcripts
```

The command line, the decisions and their reasons are in the
[`add-air-bridge` design](../openspec/changes/add-air-bridge/design.md); the packages and flows are in
[ARCHITECTURE](ARCHITECTURE.md).

### Nothing of Hermod's runs on the remote

The remote needs `sshd`, `tmux`, `claude`, a POSIX shell with coreutils (`mkfifo`, `tail`) and git ≥ 2.48, and for
chat mode `claude-agent-acp`. Hermod sends short shell commands over `ssh` on each run and installs nothing. A
remote-side helper — a per-session process holding the agent's stdio, or a resident daemon — was rejected: either
one needs a Hermod binary matching the remote's platform and an upgrade path while sessions run, and turns Hermod
into server software instead of the connection to one. The Air bridge therefore does not depend on the withdrawn
`add-remote-agent` change.

### A conversation outlives its client, and only the user ends it

Every conversation runs in a worker, a tmux session on the remote, so it survives the connection in both modes:

- **Terminal mode** runs Claude's own UI in the worker `hermod-<uuid>`, started detached and joined on a tty.
- **Chat mode** runs one adapter per project in the worker `hermod-acp-<workspace>`, and the adapter runs one
  Claude per chat. An ACP stream cannot pass through tmux — a pty alters the bytes and an attach replays a screen,
  not the output — so tmux only keeps the process alive, and its stdio is a
  [line stream](ARCHITECTURE.md#line-streams): stdin from a FIFO the tmux session holds open, so the adapter never
  sees its connection end, and stdout appended to a log.

  Observed with Claude Code 2.1.325, one machine standing in for the remote (tmux and Claude, no ssh): a Claude
  started on such a line stream keeps running with nobody attached; `claude agents --json` lists it as it lists a
  terminal session (`"kind": "interactive"`, `"status": "idle"`); an `initialize` written through the attach is
  answered in the log; a detach on stdin EOF leaves it running and listed; and `tmux kill-session` ends it within
  a second, leaving the worker's directory.

A local process ending — a closed tab, a quit IDE, a dropped connection — is always a detach: the conversation
keeps running, and may be driven meanwhile through Claude's Remote Control. Air's `session/close`, sent on every tab
close and IDE quit, is such a detach, which `air acp` answers itself rather than passing it to the adapter. Ending
a conversation is the user's decision, which Air expresses as "mark done" (`_session/archive`, undone by opening
the chat again; see [Session lifecycle](#session-lifecycle)). That message reaches only the adapter, which ends the
chat; when a project is left with no chat shown and no conversation running, its adapter worker is closed too
([The last close ends the adapter](../openspec/changes/add-air-bridge/design.md#the-last-close-ends-the-adapter)).
In terminal mode nothing marks a conversation done; it runs until it is ended on the remote
(`tmux kill-session -t hermod-<uuid>`).

Deleting a conversation is out of scope for now, and Air offers no delete.

### Returning to a running chat goes through its adapter

A chat reopened while its Claude still runs reaches the adapter that runs it, which outlived the client. The
adapter replays the chat's history when it is opened again, and reuses a Claude it still runs: that was read in the
adapter's source, not yet observed with Air. A chat whose Claude has stopped is resumed by the adapter from the
remote's transcript. A terminal session needs none of this: its attach joins the running worker.

### Hooks: a remote port per worker

The remote port for a worker's hooks is chosen when the worker starts, kept in the
[worker's directory](ARCHITECTURE.md#a-workers-directory), written into its settings, and bound again with
`ssh -R` on every attach. That keeps concurrent sessions apart and a running Claude's settings valid across
reconnects. The settings on the remote carry a token of the worker's own, never Air's. After an IDE restart Air has
a new port and token while the running Claude keeps its own; the local filter accepts only the worker's token,
targets Air's current port and sets Air's current token in the `Authorization` header
([Hooks](ARCHITECTURE.md#hooks)). While detached, hooks fail and Air fails open; nothing on the remote depends on
them. A port taken by something else while detached costs that session its hooks, logged, not the session.

### MCP servers run on the remote only

A conversation continues after Air detaches, so its tools must not depend on the local machine. The MCP servers a
chat or a command line names are passed on as given and run on the remote, alongside the servers configured there
(user scope and the relayed project's `.mcp.json`). A server whose command is a local program, such as
`jetbrains_air_ide`, fails to start there, so Air's IDE tools are not available to a remote conversation.

### Mirrors cover roots, worktrees resolve by path

- A session whose directory lies inside an existing FS relay's root reuses that relay; a directory outside every
  root (a sibling worktree) gets an FS relay of its own to the corresponding remote path
  ([Which workspace a directory belongs to](ARCHITECTURE.md#which-workspace-a-directory-belongs-to)).
- One path map — (local root, remote root) pairs, longest prefix wins — is applied to arguments, hook bodies and
  ACP messages.
- Git worktrees use relative paths (git ≥ 2.48, `worktree.useRelativePaths`) on both ends, so their pointer files
  are valid on either side of the relay. Hermod requires that git version rather than rewriting pointer files;
  the versions seen so far (2.43 local, 2.47 remote) need upgrading.
- Claude names a transcript directory after the conversation's working directory, so each distinct directory a
  terminal session runs in gets its own transcript relay.

### Relays are suspended while nobody is attached

Each local Hermod holds a lease on every relay its session uses. When it detaches, the last holder settles the
relay: suspended, Disconnected, while a worker of the workspace remains on the remote (or the workers cannot be
read), and flushed and removed when none does ([Leases](ARCHITECTURE.md#leases), [Detach](ARCHITECTURE.md#detach)).
A detached conversation keeps working with its relays Disconnected; its edits arrive on the next attach. Why a
lease is a file lock is in the [design](../openspec/changes/add-air-bridge/design.md#leases-are-file-locks).

### File, transcript and hook channels are not synchronized

The project relay, the transcript relay and the hook and stream events travel independently. Air can show a change
card before the file reaches the editor, or open a transcript older than the live stream. That is accepted; the
bridge does not order them.

### Open questions

- **The ACP session ID.** Finding a chat in the agent program takes the `sessionId` Air sends for Claude's
  session UUID, and listing chats gives Air Claude's UUIDs as session IDs. The adapter is believed to use Claude's
  UUID as the ACP session ID; not yet confirmed on the ACP stream. If it does not, `session/list` returns IDs that
  Air cannot resume, and every chat that `air acp` does not hold, such as one reopened after a restart or marked
  done after its project's last tab closed, gets an error.
- **Terminal mode.** Not observed yet: switching the agent to terminal mode in Air's agent settings still opened
  the chat UI.
- **Returning to a running adapter.** Whether Air accepts the `initialize` answered by `air acp` from the agent's
  declaration, and the adapter's replay of a chat it still runs (see
  [above](#returning-to-a-running-chat-goes-through-its-adapter)).
- **Remote Control in chat mode.** Whether a Claude started headless by the SDK can be driven through Remote Control
  while detached.
- **Worktree layout.** Where Air creates worktrees — inside the project root or beside it — which decides how many
  FS relays a project needs.
- **Edits made locally while detached.** A file changed locally and by the detached conversation is a conflict on
  the next attach, which `two-way-safe` leaves unresolved until the user settles it.

---

## Notes

- **Subscription use.** Air ships the adapter with `--hide-claude-auth`, and Anthropic's Agent SDK documentation
  states that third-party products may not offer claude.ai login without approval. Running the adapter without the
  flag uses a Claude subscription outside what Air ships; anyone adopting this should check Anthropic's terms.
- **Secrets in transit.** Air's hook settings carry a Bearer token, and its MCP config an IDE token. Both are
  per-IDE-run and local-only by design; the bridge must not log them in clear. Hermod gives the remote a hook
  token of the worker's own, so Air's hook token never reaches it
  ([The remote never holds Air's secrets](../openspec/changes/add-air-bridge/design.md#the-remote-never-holds-airs-secrets)).
  MCP servers are passed on as given, the IDE's token with them; a terminal session's are kept in the
  [worker's directory](ARCHITECTURE.md#a-workers-directory), off the command line.
