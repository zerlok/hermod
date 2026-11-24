## Why

JetBrains Air runs Claude Code as a local process, so an Air chat or terminal works only on the machine Air runs on.
[`docs/air-integration.md`](../../../docs/air-integration.md) shows that replacing what Air starts is enough to move
the agent to a remote machine while Air stays local, and a bash prototype has proven each mechanism. Hermod already
syncs a project to a remote and keeps a process alive there in tmux; bringing the bridge into Hermod gives Air chats
that outlive their tab and the IDE, keep Air's secrets local, and share a project's relays with sessions started by
hand.

## What Changes

- **BREAKING** `hermod attach [REMOTE] [-- COMMAND…]` replaces the bare `hermod <sandbox>` form. The remote and the
  command fall back to `HERMOD_REMOTE` and `HERMOD_REMOTE_CMD`; an argument always wins. `-R PORT:ADDRESS` relays a
  remote port to a local address.
- The `hermod` binary started under the name `claude` runs `hermod attach -- claude ARGS…` (the stand-in), which is
  what Air's terminal mode starts. Arguments that name no conversation run once on the remote.
- `hermod air configure claude [REMOTE]` writes one `~/.jetbrains/acp.json` entry per remote, which runs
  `hermod air acp claude REMOTE`.
- `hermod air acp` is Air's chat agent for one remote. Each project's chats run in one ACP adapter on the remote,
  and `air acp` routes Air's one connection between them. It answers the connection start and the chat list
  itself, from the agent, and finds a chat it does not hold in the agent program on the remote. A closed tab or a
  quit IDE leaves a chat running; "mark done" ends it, and ending a project's last running chat ends its adapter.
- Hermod is rebuilt around workspaces: the relays (files, stdio, calls) and the workers (tmux sessions) of one
  project on one remote. Sessions lease relays through file locks, and a workspace suspends them while a worker
  remains and releases them when none does. An agent's hooks reach Air through a relayed port with a token of the
  worker's own.
- **BREAKING** File relays are named after the remote and their two ends, and carry edits both ways without
  picking a winner; mirrors under path-only names are no longer found.
- **BREAKING** The notification back-channel (`hermod notify`, `-N`, `HERMOD_NOTIFY_SOCK`) is removed, and the
  `add-notify-backchannel` and `add-remote-agent` changes are withdrawn.

The command line, what Air expects and the decisions are in [design.md](design.md). Hermod's components, packages
and flows are in [ARCHITECTURE](../../../docs/ARCHITECTURE.md).

## Capabilities

### New Capabilities

- `claude-stand-in`: the `hermod` binary run under the name `claude`, its stdout contract, log, and link.
- `agent-session`: a conversation run in a worker: deciding from the agent's and the worker's states, one-shot
  calls, terminal sessions, and chats routed to a workspace's adapter.
- `path-mapping`: the (local root, remote root) map applied to strings and JSON values.
- `hook-proxy`: an agent's hooks carried to Air through a Net relay, with a token of the worker's own.
- `air-registration`: `hermod air configure`, and `hermod air acp` as Air's agent for one remote.

### Modified Capabilities

- `cli`: `hermod attach` with environment fallbacks replaces the single-command invocation; a `claude` command is
  no longer passed through untouched.
- `file-sync`: relays named after the remote and their two ends, the remote end checked on connect,
  `two-way-safe` relays, transcript relays, and Mutagen as the registry of synced roots.
- `remote-session`: workers are tmux sessions tagged with their workspace and origin.
- `session-orchestration`: leases on relays, given up by suspending or releasing as the workspace's workers
  decide.

## Impact

- Packages: those in [ARCHITECTURE](../../../docs/ARCHITECTURE.md#packages). `control`, `sandbox`, `mirror` and
  `hook` are replaced by them, and `notify` is removed.
- Files: `~/.jetbrains/acp.json` locally; the rest are in
  [Storage](../../../docs/ARCHITECTURE.md#storage) and [Names and tags](../../../docs/ARCHITECTURE.md#names-and-tags).
- Dependencies: the remote needs what
  [docs/air-integration.md](../../../docs/air-integration.md#nothing-of-hermods-runs-on-the-remote) lists, and
  `claude-agent-acp` for chat mode. Nothing of Hermod's is installed on the remote.
- Docs: README.md, docs/ARCHITECTURE.md and docs/air-integration.md follow the new command line and packages.
