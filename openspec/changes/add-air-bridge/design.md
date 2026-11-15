## Context

[`docs/air-integration.md`](../../../docs/air-integration.md) records how JetBrains Air runs Claude Code, what a
bridge must do to move that agent to a sandbox, and the decisions already taken: Air is a terminal to sessions that
live on the sandbox, each session is a tmux session that only the user ends, nothing of Hermod's runs on the
sandbox, hooks get a sandbox port per session, MCP servers run on the sandbox only, and mirrors cover roots and
pause when nobody is attached. This design does not restate those decisions. It gives the command line, places the
work in Hermod's packages, and describes the flows that connect them.

Hermod today runs one flow (`control.Run`: identity → mirror → flush → attach → settle) for one interactive
terminal, started as `hermod <sandbox>`. The Air bridge adds a second kind of caller: a program (the Agent SDK, or
Air's terminal tab) that starts Hermod as its `claude`, owns Hermod's stdio, and expects Claude's own CLI contract
on it.

## Goals / Non-Goals

**Goals:**
- One binary and one flow. `hermod attach` is the flow; started under the name `claude`, the same binary runs
  that flow with Claude's arguments as the command.
- Chat mode registered with Air as one `acp.json` entry per sandbox, written by Hermod.
- One place resolves an invocation: each Hermod process reads its own arguments and environment, an argument
  winning over the environment. No Hermod process sets the environment of another.
- Claude-specific knowledge in one package of pure functions, unit-testable offline.
- One project attached to several sandboxes at once, and several sessions sharing one mirror.

**Non-Goals:**
- Bridging MCP servers that run local programs (decided: not bridged).
- Deleting sessions (Air offers no delete).
- Ordering the file, transcript and event channels against each other.
- Resolving sync conflicts between sandboxes; see
  [Concurrent sandboxes](#concurrent-sandboxes-are-allowed-conflicts-are-the-developers).
- A local record of sessions (state files or a database). Nothing in this design needs one.
- Agents other than Claude Code. The command line names the agent (`hermod air acp claude`) so others can be
  added, but only `claude` is implemented.

## Command line

```
hermod attach         [SANDBOX] [-s NAME] [-C DIR] [-n] [--no-sync] [--sync-mode=MODE] [-- COMMAND…]
hermod air configure  {claude} [SANDBOX] [--node=PATH] [--acp=PATH] [--name=NAME] [--origin=NAME] [-n]
hermod air acp        {claude} [SANDBOX] [--node=PATH] [--acp=PATH]
claude ARGS…          (a link to hermod)  ≡  hermod attach -- claude ARGS…
```

| Variable             | Meaning                                                                                      |
|----------------------|----------------------------------------------------------------------------------------------|
| `HERMOD_SANDBOX`     | The sandbox when `SANDBOX` is not given. A command with neither fails before doing anything.  |
| `HERMOD_SANDBOX_CMD` | `attach`'s command when no `-- COMMAND` is given: a command line run by the sandbox's shell.  |
| `HERMOD_ORIGIN`      | The name this machine is given on the sandbox; the local hostname when unset. Set to empty, nothing on the sandbox names the machine. |

An argument always wins over the environment.

### `hermod attach`

Today's flow under its own name: mirror the directory, attach to the tmux session (created with `COMMAND` if it
does not exist), pause or tear the mirror down on detach. It replaces the bare `hermod <sandbox>` form. `-s`, `-C`
and `-n` (dry run) are today's flags. New:

- `--no-sync` connects without touching the mirror: it is neither opened nor settled, so a paused mirror stays
  paused. For looking at a running session, or for a sandbox that has its own copy of the files.
- `--sync-mode=MODE` is Mutagen's mode for a mirror being created: `two-way-safe` (the default),
  `two-way-resolved`, `one-way-safe`, `one-way-replica`. Mutagen cannot change the mode of an existing mirror, so
  asking for a different one is an error that names the mirror to terminate.

When `COMMAND` is `claude …`, `attach` runs it as an [agent session](#agent-session).

### The stand-in: `hermod` named `claude`

Air, and the SDK under it, start Claude as an executable path followed by Claude's arguments; no setting lets
anything be put in front. So Hermod is given the name: `~/.local/share/hermod/bin/claude` is a symlink to `hermod`,
and a `hermod` started as `claude ARGS…` runs exactly what `hermod attach -- claude ARGS…` runs. Every other name
runs the normal command line.

- The stand-in has no `SANDBOX` argument, so its sandbox is `HERMOD_SANDBOX`.
- Its command is given, so `HERMOD_SANDBOX_CMD` does not apply.
- Stdout belongs to Claude's caller: all of Hermod's own output, the dry-run plan included, goes to stderr and to
  a per-session log (`~/.cache/hermod/air/<uuid>.log`, mode `0600`).
- The link is outside `PATH`, so a local `claude` is never shadowed. `air configure` creates it, and running it
  again repairs it.

### `hermod air configure claude [SANDBOX]`

Registers a sandbox with Air. It creates the stand-in link and merges one entry into `~/.jetbrains/acp.json`,
keeping every other entry and key:

```json
{
  "agent_servers": {
    "Claude (prod-box)": {
      "command": "/home/<you>/go/bin/hermod",
      "args": ["air", "acp", "claude",
               "--node=/home/<you>/.nvm/versions/node/v22.11.0/bin/node",
               "--acp=/home/<you>/.nvm/versions/node/v22.11.0/bin/claude-agent-acp"],
      "env": {
        "HERMOD_SANDBOX": "prod-box",
        "CLAUDE_CODE_EXECUTABLE": "/home/<you>/.local/share/hermod/bin/claude",
        "CLAUDE_CONFIG_DIR": "/home/<you>/.local/share/hermod/air/prod-box/claude"
      }
    }
  }
}
```

- Air does not load the user's shell, but `configure` runs in it: `--node` and `--acp` default to `node` and
  `claude-agent-acp` from the user's `PATH` and are written as absolute paths, so Air never has to find them.
- The sandbox goes into `env`, not `args`: the per-chat stand-ins inherit the entry's environment but never see
  `air acp`'s arguments, and both must resolve the same sandbox.
- `--name` sets the entry's name, `Claude (<sandbox>)` by default. Configuring the same name again replaces the
  entry. One entry per sandbox makes each sandbox its own agent in Air's picker.
- `--origin=NAME` writes `HERMOD_ORIGIN` into the entry's `env`, so the chats it starts carry that name on the
  sandbox; `--origin=` writes it empty, which leaves the machine unnamed. Like the sandbox, it falls back to the
  `HERMOD_ORIGIN` that `configure` itself runs with. With neither, the entry has no `HERMOD_ORIGIN` and each chat
  uses the hostname.
- `-n` prints the entry and changes nothing.

That entry is all chat mode needs. For terminal mode, which `acp.json` plays no part in, `configure` also prints
the link's path, to be entered as Air's Claude executable, and the `HERMOD_SANDBOX=<sandbox>` line that Air's own
environment needs: Air starts the executable with that environment.

### `hermod air acp claude [SANDBOX]`

What the entry runs. It starts the ACP adapter (`--node` running `--acp`) with its own environment unchanged and
copies ACP messages both ways verbatim. The adapter passes that environment on to every `claude` it spawns
(observed with `air-probe`), which is how each chat's stand-in gets `HERMOD_SANDBOX`.

It acts on one message: `_session/archive` ("mark done") closes that chat's session on the sandbox. The adapter is
the only process that sees it (see [Session lifecycle](../../../docs/air-integration.md#session-lifecycle)).

## Architecture

```
LOCAL                                                              SANDBOX (sshd, tmux, claude, sh, git)

Air chat UI ──ACP──► hermod air acp claude ──ACP──► claude-agent-acp
                      └ _session/archive → close session               │ one per chat
                                                                       ▼
Air terminal tab ──tty──────────────────────────────────────► claude ARGS…   (hermod, by its name)
you ──────────────► hermod attach [SANDBOX] -- COMMAND…                │
                             └───────────────┬─────────────────────────┘
                                        the attach flow
                                             ├ mirrors (leased) ───────────► mutagen: project ⇄, transcripts ◄
                                             ├ hook proxy 127.0.0.1:P ◄═════ ssh -R <hookport>:127.0.0.1:P
                                             │    └► Air's hook URL                 (agent sessions from Air)
                                             └ ssh ════════════════════════► tmux <session> ─ COMMAND
                                                                             ~/.hermod/s/<uuid>/  (agent sessions)
```

### Packages

Dependencies point one way: `cli → control → {claude, hook, air, sandbox, mirror, git} → {pathmap, shell}`.

| Package   | Status    | Owns |
|-----------|-----------|------|
| `cli`     | changed   | The commands above, and the stand-in entry chosen by the program name. The one place that resolves sandbox and command from arguments and environment. |
| `control` | changed   | `Run`, the attach flow, now with the agent-session branch; `RunACP`, the chat-mode wrapper. The settle rule stays in one place. |
| `claude`  | new       | Claude's CLI contract as pure functions: classify an argv, extract the session UUID, rewrite an argv and a settings file for the sandbox, select the environment to carry, and the stream-json rules used on reattach. Files are read by the caller. |
| `pathmap` | new, leaf | (local root, sandbox root) pairs, longest prefix wins, applied to a string, a JSON value or a stream-json line. Knows nothing about Claude. |
| `hook`    | new       | The local hook proxy: an HTTP handler on a loopback listener that checks the session's token, rewrites bodies through `pathmap`, and forwards to Air's current hook URL with Air's current token. |
| `air`     | new       | Air's files: merge an entry into `acp.json`, create the stand-in link. |
| `sandbox` | changed   | Agent sessions keyed by UUID beside today's sessions keyed by name: probe, start (terminal or chat runtime), attach, close. Owns the session directory on the sandbox and every shell line run there, and the TCP reverse forward for hooks. |
| `mirror`  | changed   | Names that carry the sandbox, the sync mode, one-way transcript mirrors, labels, and a shared lease per mirror. |
| `shell`   | changed   | `Command` gains explicit stdio (`Stdin`, `Stdout`, `Stderr`), so a caller can pump a stream through a transform instead of inheriting the terminal; `NewSSH` gains an option to refuse a shared master connection. |
| `notify`  | removed   | See [The notification back-channel is removed](#the-notification-back-channel-is-removed). |

## Flows

### Classifying the command

`claude.Classify` reads the command `attach` was given:

| Claude's arguments                                                              | Kind             |
|---------------------------------------------------------------------------------|------------------|
| a subcommand (`auth`, `mcp`, …), `--version`, `--help`, or `-p` with no session   | utility call     |
| `--input-format stream-json` with `--session-id` or `--resume <uuid>`             | chat session     |
| anything else with `--session-id` or `--resume <uuid>`                            | terminal session |
| anything else                                                                     | not an agent session: today's attach, in a tmux session named after the directory |

A command that is not `claude` is the last row.

### Utility call

Hermod runs `ssh <sandbox> claude ARGS…` with the caller's stdio and exits with its code. No mirror, no tmux, no
hooks. This covers the adapter's `claude auth status` and Air's `-p` settings read, which then report the
sandbox's login and settings.

### Agent session

1. **Map paths.** List Hermod's mirrors for this sandbox from Mutagen. The session's directory either lies inside
   one mirror's local root or becomes a new root at the corresponding sandbox path. The resulting `pathmap` is
   fixed for the run.
2. **Open mirrors.** Take a shared lease on the project mirror and on the transcript mirror for this directory,
   open each (create or resume), flush the project mirror. The transcript mirror brings
   `~/.claude/projects/<dir>/` on the sandbox to the same place under the local Claude config directory
   (`CLAUDE_CONFIG_DIR`, `~/.claude` by default). Every setup command runs with stdin from `/dev/null`; Hermod's
   stdin is left for Claude.
3. **Ask Claude about the session.** One ssh call runs `claude agents --json` on the sandbox, which lists every
   running session with its `sessionId`, `pid`, `cwd`, `name` and `status`, and checks for the tmux session and the
   transcript:

   | Claude lists the UUID | tmux `hermod-<uuid>` | Transcript | Then |
   |---|---|---|---|
   | yes | yes | —   | **running** → attach (step 5) |
   | yes | no  | —   | running outside Hermod → refuse: resuming would start a second copy |
   | no  | —   | yes | **closed** → start with `--resume`, copying the transcript up first if it exists only locally |
   | no  | —   | no  | **new** → start, or fail if the caller asked to `--resume` |

   A tmux session left without its Claude is removed before a start.
4. **Start.** Create `~/.hermod/s/<uuid>/` (mode `0700`) if it is missing, with a free sandbox port in `hookport`
   and a new per-session `token`. Write `env` (the selected `CLAUDE_*`/`ANTHROPIC_*` variables and the git
   identity) and `settings.json` (the caller's hook settings, with the URL set to `localhost:<hookport>` and the
   session token in place of Air's) into it over ssh stdin. Start tmux session `hermod-<uuid>`, tagged as in
   [Names and tags](#one-identity-names-and-tags), running `. ./env && exec claude <rewritten args>`. In a chat session Claude's stdin is `in.fifo`, held open by the tmux
   session, and its stdout is appended to `out.log`.
5. **Attach.** If the caller's settings carry Air's hooks, start the hook proxy on `127.0.0.1:0` aimed at Air's
   current URL and token. Open one ssh connection, refusing a shared master, with
   `-R <hookport>:127.0.0.1:<proxy port>`:
   - **terminal session:** `tmux new-session -A -s hermod-<uuid>` on a tty;
   - **chat session:** a shell line that tails `out.log` to stdout and copies stdin into `in.fifo` one complete
     line at a time. Hermod passes both directions through `pathmap`, line by line, unbuffered. When the session
     was already running, Hermod answers the SDK's `initialize` from the first response in the log, re-delivers
     control requests still waiting for an answer, and resends prompts the log has not echoed.
6. **Detach.** Stdin EOF, SIGTERM or a dropped connection ends the attach and nothing else; the tmux session
   keeps running. Hermod exits inside the SDK's 2 s + 5 s window, so it is never left orphaned.
7. **Settle.** Release the leases. The last holder of a mirror settles it by today's rule, with liveness read as
   "any `hermod-*` tmux session tagged with a directory under this root".

With `--no-sync`, steps 2 and 7 are skipped.

### Close

On `_session/archive` for a UUID, `RunACP` closes that session: `tmux kill-session -t hermod-<uuid>` (Claude gets
SIGHUP and saves its transcript); the session directory stays, for `--resume`. It then settles the session's
mirrors as step 7 does, holding a lease while it decides so it cannot race an attached stand-in. A
`_session/unarchive` needs nothing: the `session/resume` that follows finds a closed session in step 3.

## Decisions

### The stand-in is `attach` under another name
One binary, one resolver, one flow. The alternatives each added a second way in: a hidden environment variable
telling `hermod` to act as Claude, a generated wrapper script, or a separate stand-in command with its own flow.
Naming the binary needs none of them, and a session typed by hand
(`hermod attach prod-box -- claude --resume <uuid>`) is the same session Air would open.

The cost is that the stand-in cannot take Hermod's flags, since all its arguments are Claude's. Its sandbox comes
from the environment, and so would any other option it needs (see [Open Questions](#open-questions)).

### `claude` owns Claude's contract
Argv classification and rewriting, environment selection and the stream-json replay rules all follow Claude's CLI
and the SDK's protocol, and change together when those change. In one package of pure functions, a new Claude or
SDK version is one package and its table-driven tests. `pathmap` is separate because hook bodies use it too.

- **Alternative — a generic `agent` interface:** rejected. There is one agent; an abstraction now would be shaped
  by guesses about the second.

### Claude says what state a session is in
Whether a session runs is Claude's to say, not something to infer from the files and tmux sessions Hermod left
behind. `claude agents --json` on the sandbox is the authority; the session directory holds only what the running
process needs (`hookport`, `token`, `env`, `settings.json`, `in.fifo`, `out.log`) and decides nothing. This also
catches a session started on the sandbox outside Hermod, which a tmux check alone would resume as a second copy.

A command that is not an agent session has no Claude to ask and keeps today's check, `tmux has-session`.

### One identity: names and tags
Everything a session owns is findable from its UUID or from the `hermod` prefix, with the tool that owns it, and
everything on the sandbox says where it came from. Someone looking at the sandbox can tell a Claude started there
by hand from one that Hermod runs for an Air chat on a particular laptop.

| Thing | Name | Tags | Read with |
|---|---|---|---|
| tmux session (sandbox) | `hermod-<uuid>`; a plain `attach` keeps its name | options `@hermod-mode`, `@hermod-origin`, `@hermod-dir` | `tmux ls -F '#{session_name} #{@hermod-mode} #{@hermod-origin} #{@hermod-dir}'` |
| Claude process (sandbox) | untouched | environment `HERMOD_SESSION`, `HERMOD_MODE`, `HERMOD_ORIGIN` | `/proc/<pid>/environ`, with the `pid` from `claude agents --json` |
| Session directory (sandbox) | `~/.hermod/s/<uuid>/` | — | — |
| Project mirror (local) | `hermod-<sandbox>-<slug>` | labels `hermod-kind=project`, `hermod-sandbox=<sandbox>` | `mutagen sync list --label-selector hermod-sandbox=<sandbox>` |
| Transcript mirror (local) | `hermod-<sandbox>-<slug>-transcripts` | labels `hermod-kind=transcripts`, `hermod-sandbox=<sandbox>` | same |
| Log (local) | `~/.cache/hermod/air/<uuid>.log` | — | — |

- **Mode** says who drives the session: `air-acp` (an Air chat, through the adapter), `air-terminal` (Air's terminal
  tab) or `attach` (a person's own terminal). The stand-in sets the first two from Claude's arguments.
- **Origin** is the machine the session was started from: its hostname, or `HERMOD_ORIGIN` when set, reduced to
  letters, digits, dots and dashes. An empty `HERMOD_ORIGIN` leaves the origin tags out, for a shared sandbox
  where the machine's name should not show. For Air chats, `air configure --origin` puts it in the entry.
- **Claude's session name is not Hermod's to set.** It is what the user sees in Air and in Claude's own lists, so
  Hermod passes no `--name` and adds no prefix or suffix. A Claude that Hermod runs is recognized by its
  environment, not by its name; one without `HERMOD_SESSION` was started on the sandbox itself.
- Mirrors belong to a root, not to one session, so they carry the sandbox and the kind but no session ID. They
  live on the local machine, so they need no origin.
- `@hermod-dir` is what the settle rule reads, so no file on the sandbox has to record it.

### The sandbox never holds Air's secrets
Air's hook token changes with every IDE run and is local by design. The sandbox's `settings.json` carries a
per-session token generated by Hermod; the proxy accepts only that token and substitutes Air's current one. A
running Claude's settings therefore stay valid across IDE restarts, and Air's token never leaves the local machine.
The session token is needed because a loopback port on a shared sandbox is reachable by every user there. Secrets
reach the sandbox only inside the session's `0700` directory, over ssh stdin, never on a command line. Tokens are
an opaque type that does not print, so no log can leak them.

### Each sandbox's chats are visible only through its own agent
Air lists chat-mode sessions through the adapter (`session/list`), which reads transcripts from Claude's config
directory. Each entry `configure` writes sets `CLAUDE_CONFIG_DIR` to a directory of that sandbox's own, and the
stand-in mirrors that sandbox's transcripts there. "Claude (A)" then lists only A's chats and "Claude (B)" only
B's, so a chat can only be reopened on the sandbox it lives on, and Hermod needs no record of which sandbox holds
which chat. The user's local sessions in `~/.claude` stay out of both lists. `CLAUDE_CONFIG_DIR` is a local path:
it is not among the variables carried to the sandbox.

Two cases stay unseparated. Hermod's documentation advises against several sandboxes for one project in them; no
code guards against it:

- **Air's terminal mode.** Air reads `~/.claude` itself there, so every sandbox's chats share one list, and a chat
  reopened while another sandbox is configured forks onto it.
- **Claude started by hand.** `hermod attach B -- claude --resume <uuid of a chat on A>` forks the chat onto B.

### Mirror names carry the sandbox
Today a mirror is named after the project's sandbox path alone (`dev-api`), so attaching to B in a project already
mirrored to A finds `dev-api` and resumes the mirror to A: the session runs on B while the files go to A. A mirror
is named `hermod-<sandbox>-<slug>` instead (the alias reduced to the letters, digits and dashes Mutagen accepts), and
opening one checks its remote endpoint before resuming. Mirrors made under the old names are no longer found; the
first run lists them and leaves terminating them to the user.

### Concurrent sandboxes are allowed; conflicts are the developer's
One project can be attached to several sandboxes at once: each has its own mirror, tmux sessions and hook ports.
The local directory then relays between them, so one agent's edits reach the other sandboxes too. Hermod allows
this and does not manage it. It neither orders nor resolves the edits; what Mutagen reports as a conflict is the
developer's to settle, and so is excluding per-machine build output from the mirror.

### Mutagen is the mirror registry
Which local roots are mirrored, and to where, is state Mutagen already holds: a mirror's endpoints are the root
pair. Hermod labels its mirrors (see [Names and tags](#one-identity-names-and-tags)) and reads the roots from
`mutagen sync list`, keeping no file of its own that could drift. Paths stay in the endpoints because a label
value cannot hold one.

### Leases are file locks
Each process using a mirror holds a shared `flock` on `$XDG_RUNTIME_DIR/hermod/lease/<mirror>`. On exit it tries
a non-blocking exclusive lock; the process that gets it is the last holder and settles the mirror. The kernel
releases a crashed process's locks, so a killed stand-in never leaves a mirror leased.

- **Alternative — a local daemon owning mirrors and proxies:** rejected. `hermod air acp` lives as long as the IDE
  run and could host one, but terminal mode and `attach` have no such process.

### The notification back-channel is removed
`hermod notify`, the `-N` flag, the `notify` package, `sandbox.Channel` and `HERMOD_NOTIFY_SOCK` go. The channel
does not fit what this design makes of Hermod:

- **It wants Hermod on the sandbox.** Its sender is `hermod notify` run there, which is what the
  [remote-agent](../add-remote-agent/proposal.md) proposal exists to install. Nothing of Hermod's runs on the
  sandbox now.
- **It is one socket per sandbox user**, owned by the most recent attach. With several sessions attached at once,
  all but one lose it.
- **Air does not need it.** Claude's hooks reach Air through the hook proxy, and Air shows "needs input" and
  "done" itself.

What is lost is the desktop toast for a session attached from a plain terminal. If that is wanted again, the hook
forward is the transport to build it on: Claude's own `Stop` and `Notification` hooks, received locally.

The [`add-notify-backchannel`](../add-notify-backchannel/proposal.md) and
[`add-remote-agent`](../add-remote-agent/proposal.md) changes are withdrawn with it. `shell`'s reverse-forward
option stays; the hook forward uses it.

## Risks / Trade-offs

- **Reattaching to a running chat session is unproven.** Whether the SDK accepts a locally answered `initialize`
  is not known. → Build it last. Until then, and if it fails, a reattach closes the tmux session and starts
  `--resume`, losing only a turn in flight.
- **The adapter is assumed to list sessions from `CLAUDE_CONFIG_DIR`**, as Claude Code does. → Verify with
  `air-probe` and two entries. If it does not, every agent lists every chat, and a chat reopened through the wrong
  agent forks.
- **`claude agents --json` is assumed to list a headless chat session** as it lists interactive ones, and to exist
  in the sandbox's Claude version. → Verify on the sandbox with a chat session running. If it does not list them,
  chat sessions fall back to `tmux has-session`.
- **The ACP session ID is assumed to be Claude's session UUID.** → Verify in `acp-tap` logs before building close.
- **"Mark done" writes into a transcript the one-way mirror owns**, which may stall that mirror. → Test with
  `air-probe`; if it stalls, close resets the file.
- **A sandbox port picked free can be taken before `ssh -R` binds it.** → That attach runs without hooks, logged;
  the next attach tries the same port again.
- **A hand-edited entry can give `air acp` a `SANDBOX` argument that differs from its `HERMOD_SANDBOX`.** "Mark
  done" then looks for the session on the wrong sandbox and closes nothing. → `configure` never writes such an
  entry.

## Delivery

Each step is usable on its own.

1. `hermod attach` with `HERMOD_SANDBOX`, `HERMOD_SANDBOX_CMD`, `--no-sync`, `--sync-mode` and mirror names that
   carry the sandbox; the notification back-channel removed.
2. `claude.Classify`, `pathmap` and utility calls; the stand-in link.
3. Terminal sessions: the session directory, tmux per UUID, the hook proxy.
4. Chat sessions: the FIFO and log runtime and the stream pump, start and `--resume` only; `hermod air configure`,
   with the entry running the adapter directly until step 6.
5. Mirror labels, shared leases and transcript mirrors.
6. `hermod air acp` and close on "mark done".
7. Reattach to a running chat session.

## Open Questions

- **Terminal mode has not been observed.** Switching Air to it still opened the chat UI. That Air starts the
  configured path under its own name, as the SDK does, is assumed.
- **`HERMOD_SANDBOX` in terminal mode.** It has to be in Air's own environment (e.g. `~/.config/environment.d/`).
  Whether Air's terminal-mode setting can carry environment of its own is not known.
- **Options for the stand-in.** `--no-sync` and `--sync-mode` could reach it only as environment variables. Whether
  any are needed, and their names, is not settled.
- The questions in [`docs/air-integration.md`](../../../docs/air-integration.md#open-questions) still stand, except
  "Seeing done", which `hermod air acp` answers.
