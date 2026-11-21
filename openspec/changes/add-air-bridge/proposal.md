## Why

JetBrains Air runs Claude Code as a local process, so an Air chat or terminal works only on the machine Air runs on.
[`docs/air-integration.md`](../../../docs/air-integration.md) shows that replacing that one `claude` executable is
enough to move the agent to a sandbox while Air stays local, and a bash prototype has proven each mechanism. Hermod
already mirrors a project to a sandbox and keeps a session alive in tmux; bringing the bridge into Hermod gives Air
sessions that survive the IDE, keep Air's secrets local, and share mirrors with sessions started by hand.

## What Changes

- **BREAKING** `hermod attach [SANDBOX] [-- COMMAND…]` replaces the bare `hermod <sandbox>` form. The sandbox and
  the command fall back to `HERMOD_SANDBOX` and `HERMOD_SANDBOX_CMD`; an argument always wins.
- The `hermod` binary started under the name `claude` runs `hermod attach -- claude ARGS…` (the stand-in). Its
  stdout belongs to its caller; Hermod's own output goes to stderr and a per-session log.
- A `claude` command is classified: utility calls run straight over ssh; sessions keyed by Claude's session UUID run
  in tmux `hermod-<uuid>` as terminal or chat sessions, with arguments, settings, environment, hook bodies and the
  chat stream mapped between local and sandbox paths. Claude on the sandbox is asked whether a session is running.
- A local hook proxy forwards each session's hooks from a sandbox port of its own to Air's current hook URL,
  swapping a per-session token for Air's.
- `hermod air configure claude [SANDBOX]` writes one `~/.jetbrains/acp.json` entry per sandbox and creates the
  stand-in link; `hermod air acp claude` wraps the ACP adapter and closes a session on "mark done".
- **BREAKING** Mirrors are named `hermod-<sandbox>-<slug>` and labelled; mirrors under the old names are no longer
  found. Mirrors are shared through file-lock leases, settled by their last holder, and joined by one-way
  transcript mirrors. tmux sessions and Claude processes carry mode, origin and directory tags.
- **BREAKING** The notification back-channel (`hermod notify`, `-N`, `HERMOD_NOTIFY_SOCK`) is removed, and the
  `add-notify-backchannel` and `add-remote-agent` changes are withdrawn.

The command line, packages, flows and decisions are in [design.md](design.md).

## Capabilities

### New Capabilities

- `claude-stand-in`: the `hermod` binary run under the name `claude`, its stdout contract, log, and link.
- `agent-session`: classifying a `claude` command, utility calls, and terminal and chat sessions keyed by UUID:
  their state, start, attach, detach, reattach, close, and names and tags on the sandbox.
- `path-mapping`: the (local root, sandbox root) map applied to strings, JSON values and stream-json lines.
- `hook-proxy`: the per-session sandbox port and token, and the local proxy to Air's hook endpoint.
- `air-registration`: `hermod air configure` and `hermod air acp`.

### Modified Capabilities

- `cli`: `hermod attach` with environment fallbacks replaces the single-command invocation; mirror names carry the
  sandbox; a `claude` command is no longer passed through untouched.
- `file-sync`: sandbox-scoped names and labels, remote-endpoint check on resume, `two-way-safe` project mirrors,
  one-way transcript mirrors, and Mutagen as the registry of mirrored roots.
- `remote-session`: tmux sessions carry mode, origin and directory tags; liveness of a root is read from those tags.
- `session-orchestration`: shared leases decide who settles a mirror, and settling counts every session under its
  root.

## Impact

- Packages: new `claude`, `pathmap`, `hook`, `air`, `secret`; changed `cli`, `control`, `sandbox`, `mirror`, `shell`;
  `notify` and `sandbox.Channel` removed (see the design's package table).
- Files written locally: `~/.jetbrains/acp.json`, `~/.local/share/hermod/bin/claude`,
  `~/.local/share/hermod/air/<sandbox>/claude/`, `~/.cache/hermod/air/<uuid>.log`,
  `$XDG_RUNTIME_DIR/hermod/lease/`. On the sandbox: `~/.hermod/s/<uuid>/`.
- Dependencies: the sandbox needs what
  [docs/air-integration.md](../../../docs/air-integration.md#nothing-of-hermods-runs-on-the-sandbox) lists; chat mode
  needs Node ≥ 22 and `claude-agent-acp` locally. Nothing of Hermod's is installed on the sandbox.
- Docs: README.md, docs/ARCHITECTURE.md and docs/air-integration.md follow the new command line and packages.
