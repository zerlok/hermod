## Context

[`docs/air-integration.md`](../../../docs/air-integration.md) records how JetBrains Air runs Claude Code, what a
bridge must do to move that agent to another machine, and the decisions already taken: Air is a terminal to
sessions that live on that machine, each session is a tmux session that only the user ends, nothing of Hermod's
runs there, hooks get a port per session, and MCP servers run there only. This design does not restate those
decisions. It names the entities Hermod is built from, gives each its states, and describes how every command uses
them.

Hermod connects a local workstation to a **remote** one. Sandboxing an agent, which that document describes, is
one use of a remote; nothing in Hermod depends on what the remote is for.

Hermod today runs one flow for one interactive terminal, started as `hermod <sandbox>`. The Air bridge adds two
callers that are programs. Air's terminal tab starts Hermod as its `claude` and expects Claude's own CLI on a
terminal. Air's chat UI speaks the Agent Client Protocol (ACP) to an agent it starts from `acp.json`, and expects
`claude-agent-acp`, the adapter that translates ACP for Claude, to answer.

## Goals / Non-Goals

**Goals:**
- One binary and one session flow. A person's `hermod attach`, Air's terminal tab and an Air chat run the same
  flow and differ only in what they hand it.
- Every fact has one owner. An entity is the owner of one fact; no other entity restates it or decides by it.
- Chat mode registered with Air as one `acp.json` entry per remote, written by Hermod.
- Each Hermod process reads its own arguments and environment in one place, an argument winning over the
  environment.
- Everything Claude-specific behind one interface, unit-testable offline, so a second agent is a second
  implementation.
- One project attached to several remotes at once, and several sessions in one project sharing its relays.

**Non-Goals:**
- Translating ACP. The adapter does that, on the remote; Hermod routes ACP messages and maps the paths in them.
- Rewriting MCP configuration. The MCP servers a chat or a command line names are passed on as given and run on
  the remote; a server that needs a local program is not bridged.
- Holding calls to local endpoints while no client is attached. The relay model allows it
  ([later](#a-disconnected-relay-holds)); this change does not build it.
- Deleting sessions (Air offers no delete).
- Ordering the file, transcript and event channels against each other.
- Resolving sync conflicts between remotes; see
  [Concurrent remotes](#concurrent-remotes-are-allowed-conflicts-are-the-developers).
- A local record of sessions (state files or a database). Nothing in this design needs one.
- A command that sets up a workspace ahead of its first session, or prints a workspace's state. A workspace's
  state is readable ([Workspace](#workspace)); this change adds no command for it.
- Keeping two sessions in one directory from editing the same files. That is the worker's to arrange
  ([File isolation](#file-isolation-is-the-workers)).
- Agents other than Claude Code. The command line names the agent (`hermod air acp claude`) and the code reaches
  it through an interface, but only `claude` is implemented.

## Command line

```
hermod attach         [REMOTE] [-s NAME] [-C DIR] [-R PORT:ADDRESS]… [-n] [-- COMMAND…]
hermod air configure  {claude} [REMOTE] [--name=NAME] [--origin=NAME] [-n]
hermod air acp        {claude} [REMOTE]
claude ARGS…          (a link to hermod)  ≡  hermod attach -- claude ARGS…
```

| Variable            | Meaning                                                                                     |
|---------------------|---------------------------------------------------------------------------------------------|
| `HERMOD_REMOTE`     | The remote when `REMOTE` is not given. A command with neither fails before doing anything.   |
| `HERMOD_REMOTE_CMD` | `attach`'s command when no `-- COMMAND` is given: a command line run by the remote's shell.  |
| `HERMOD_ORIGIN`     | The name this machine is given on the remote; the local hostname when unset. Set to empty, nothing on the remote names the machine. |

An argument always wins over the environment. Each flag's default is its variable's value, so a command reads one
resolved value and never looks at the environment again.

### `hermod attach`

Runs a [session](#session): the workspace the directory belongs to, a worker in it running `COMMAND` on the
remote, and the caller attached to both. It replaces the bare `hermod <sandbox>` form.

- `-s` names the worker, `-C` gives the directory, `-n` prints the plan and changes nothing.
- `-R PORT:ADDRESS` adds a Net relay of the user's own: calls to `PORT` on the remote reach `ADDRESS` locally,
  unfiltered, for a listener the user runs themselves. It may be given more than once.

### The stand-in: `hermod` named `claude`

Air's terminal tab starts Claude as an executable path followed by Claude's arguments; no setting lets anything be
put in front. So Hermod is given the name: a symlink called `claude` points to `hermod`, and a `hermod` started as
`claude ARGS…` runs exactly what `hermod attach -- claude ARGS…` runs. Every other name runs the normal command
line.

- The stand-in has no `REMOTE` argument, so its remote is `HERMOD_REMOTE`.
- Its command is given, so `HERMOD_REMOTE_CMD` does not apply.
- It runs a conversation on a terminal only. Without one it runs [one-shot calls](#one-shot-call) and refuses a
  conversation, naming `hermod air acp` as the way a program holds one.
- Stdout belongs to Claude's caller: all of Hermod's own output, the dry-run plan included, goes to stderr and to
  a log (`~/.cache/hermod/air/<key>.log`, mode `0600`).
- `make install` creates the link, and where is the installer's choice: outside `PATH`, beside a local `claude`,
  or on `PATH` as the machine's only `claude`. Hermod itself never writes it.

Chat mode does not use the stand-in.

### `hermod air configure claude [REMOTE]`

Registers a remote with Air by merging one entry into `~/.jetbrains/acp.json`, keeping every other entry and key:

```json
{
  "agent_servers": {
    "Claude (prod-box)": {
      "command": "/home/<you>/go/bin/hermod",
      "args": ["air", "acp", "claude", "prod-box"]
    }
  }
}
```

- `--name` sets the entry's name, `Claude (<remote>)` by default. Configuring the same name again replaces the
  entry. One entry per remote makes each remote its own agent in Air's picker.
- `--origin=NAME` writes `HERMOD_ORIGIN` into the entry's `env`; `--origin=` writes it empty, which leaves the
  machine unnamed. With neither the flag nor the variable, the entry has no `env` and each chat uses the hostname.
- `-n` prints the entry and changes nothing.

That entry is all chat mode needs. For terminal mode, which `acp.json` plays no part in, `configure` also prints
the stand-in link's path when it finds one (the `claude` on its `PATH` that is a link to `hermod`, else the link
at `make install`'s default place), to be entered as Air's Claude executable, and the `HERMOD_REMOTE=<remote>`
line that Air's own environment needs.

### `hermod air acp claude [REMOTE]`

What the entry runs: Air's agent for one remote. It starts nothing locally. The adapter runs on the remote as a
worker, one per workspace for all of that project's chats, and `air acp` carries Air's ACP messages to it and back
([Chat](#chat)). To Air it is the adapter; to each adapter it is Air.

- **It starts** when Air starts the entry's command: once per IDE run, when Air first needs the agent, and not
  once per chat.
- **It ends** when Air ends it: at an IDE quit Air closes its stdin and sends SIGTERM, and a vanished IDE leaves
  it with a closed stdin. Either way it detaches everything and exits; nothing on the remote ends with it.

The adapter stays a separate program. It translates ACP into Claude's own protocol and carries Air's extensions,
negotiated when Air declares itself at `initialize`. Hermod reads only the few messages it must route or act on
and passes every other one on with its paths mapped.

## Entities

Hermod is built from six entities: workstation, session, workspace, relay, worker and agent. What each one owns,
its states and the package that declares it are in [ARCHITECTURE](../../../docs/ARCHITECTURE.md#entities). This section gives the rules
they follow.

- A workspace holds any number of workers, none included. It exists from its first session and is gone with its
  last worker, and nothing sets one up beforehand.
- A worker belongs to one workspace and keeps running with no session attached. It runs a plain command, one
  agent, or an adapter that runs an agent per chat. Whatever a detached worker changes or writes reaches the local
  workstation at the next attach.
- `hermod attach` and the stand-in hold one session each. `hermod air acp` holds one for every workspace that has
  a chat open in Air.

### Relay

States and kinds: [ARCHITECTURE](../../../docs/ARCHITECTURE.md#relay).

- A relay is **leased** ([Leases are file locks](#leases-are-file-locks)): each session that connects it holds a
  lease, and it stays Connected while any lease is held.
- One rule decides what becomes of a relay when its last lease is released: it is Disconnected while its
  workspace has a live worker, and Absent otherwise. An FS relay is flushed before it becomes Absent. When the
  workers cannot be read, the relay is left Disconnected, which never removes something in use.
- An IO relay's remote end is one worker, so it exists exactly as long as that worker. A line stream has one
  reader: a second session that asks for it while it is leased is refused.
- A Net relay has no Disconnected state in this change. Nothing listens on the remote port once the client is
  gone, so it goes from Connected to Absent and a call made meanwhile fails at once.
- Reading a relay's state can fail. That is a failure value, not a fourth state.

### Workspace

A workspace is the relays and the workers of one project on one remote. It adds relays and workers, connects and
releases its relays together, applies the release rule from its own worker list, and reports the state of
everything in it. It carries nothing itself: a project's files are on both workstations because an FS relay joins
the two directories, not because a workspace exists.

States: [ARCHITECTURE](../../../docs/ARCHITECTURE.md#workspace).

A workspace's state is those of its members, read in two calls: its relays from Mutagen and its workers from tmux,
both by the workspace's name ([Names and tags](#one-identity-names-and-tags)).

**Which workspace a directory belongs to** is decided once, when a session starts:

1. the workspace whose project FS relay covers the directory;
2. else, for a linked git worktree, the workspace that covers its main working tree, where the worktree gets an FS
   relay of its own;
3. else a new workspace, whose project FS relay joins the directory to the corresponding remote path. A root that
   holds existing roots gets one too, with a warning: the inner workspaces stay, so their files are carried twice.

**Relays enter a workspace** in four ways, and a relay is its two ends, so the same two ends are never joined
twice: a session or a worker that needs a relay already there leases it.

| Relay | Added for | Shared by the workspace's sessions |
|-------|-----------|------------------------------------|
| FS, the project | the workspace itself | yes |
| FS, another directory | a session whose directory lies outside the project relay, or an agent that declares one (its transcripts) | yes, by every session that needs the same two ends |
| Net | an agent that declares one (its hooks), or the user's `-R` | no: its local end is a listener inside one session's process |
| IO | each worker | no: one per worker |

- An FS relay is configured with its two ends, a (local root, remote root) pair. The pairs of a workspace's FS
  relays, longest prefix first, are the **path map** its IO and Net relays apply to what they carry.
- A relay belongs to the workspace that made it. A workspace that needs two ends another one already joined leases
  that relay instead of making a second.

### Worker

States: [ARCHITECTURE](../../../docs/ARCHITECTURE.md#worker).

A worker is a tmux session on the remote, tagged with its workspace and its origin
([Names and tags](#one-identity-names-and-tags)). tmux is the only source of its state.

The worker provides the remote end of its IO relay in one of two forms, fixed when it starts:

- **terminal:** the client joins the tmux session on a tty.
- **line stream:** the process reads a FIFO and appends its stdout and stderr to logs; the client's streams are
  copied to and from them one complete line at a time.

### Agent

States: [ARCHITECTURE](../../../docs/ARCHITECTURE.md#agent).

The agent program is the only source: for Claude, `claude agents --json`. A detach changes nothing here: an agent
whose client has gone is Running, and what it waits for is held in a relay. Whether a stopped conversation can be
resumed is not a state Hermod keeps. The caller says whether to resume, and the agent program itself refuses when
there is nothing to resume.

Hermod reads an agent's state only for a worker that runs one agent. A chat's agent runs inside the adapter, which
starts, resumes and ends it; Hermod decides nothing about it.

`control` reaches an agent through one interface, [`agent.Agent`](../../../docs/ARCHITECTURE.md#agentagent--an-agent-programs-contract),
and never names Claude. The agent is handed the path map and never learns where the other workstation is or how
it is reached.

## Flows

### Session

Every session, whoever starts it, runs these steps. The rows of the table after them are the only differences.

1. **Set up the workspace.** Find [the workspace the directory belongs to](#workspace), or make it. Lease the FS
   relay that covers the directory and connect it, flushed. The path map is now fixed for the run.
2. **Read the states.** One call on the remote reads the worker's state and, for a worker that runs one agent,
   whether its conversation runs.
3. **Decide.**

   | Agent   | Worker            | Action |
   |---------|-------------------|--------|
   | Running | Running           | connect |
   | Running | Absent            | refuse: it runs outside Hermod, and a start would be a second copy |
   | Stopped | Running or Exited | remove the worker, then decide as Absent |
   | Stopped | Absent            | start, with the caller's arguments as given |

   A worker that does not run one agent has only the worker column: Absent starts it, Running connects, and an
   Exited one is removed first.
4. **Start**, when the table says so. The workspace first adds, connects and flushes the FS relays the agent
   requires, so its transcripts are on the remote before it reads them. Then create the worker's directory
   (mode `0700`) with what the agent's answer listed, written over ssh stdin, and start the worker in the
   workspace, in the directory's remote path.
5. **Attach the workspace.** The workspace adds the session's remaining relays, the IO relay to the worker among
   them, and connects them over ssh connections that refuse a shared master. The client is now connected to the
   worker.
6. **Detach the workspace.** Stdin EOF, a signal or a dropped connection releases the session's leases and nothing
   else; the worker keeps running, and the workspace applies [the release rule](#relay) to each relay released.
   A process that was ended this way then exits at once, so the program that started it never has to kill it.

| Started as | Worker runs | Agent | IO form | Net relays |
|------------|-------------|-------|---------|------------|
| `hermod attach` | the user's command | none | terminal | the user's `-R`, if any |
| `claude ARGS…` on a tty (Air's terminal tab, or by hand) | Claude's command, mapped | Claude | terminal | Air's hooks, when the settings carry them |
| `hermod air acp`, once per workspace | the adapter | one per chat, the adapter's | line stream | Air's hooks |

The worker's directory holds `env` (the variables the agent declared and the git identity), the Net relay's port
and token, and, for a line-stream worker, the FIFO, the logs and the process ID a close waits on. A worker that
runs Claude itself adds `settings.json`, the caller's hook settings as the agent rewrote them. Every setup command
runs with stdin from `/dev/null`; Hermod's stdin is left for the worker.

### One-shot call

Arguments that name no conversation (a subcommand such as `auth`, `--version`, `--help`, `-p` without a session)
are not a session. Hermod runs the command on the remote with the caller's stdio and exits with its code: no
workspace, no worker, no relay. It runs in the project's remote directory when that exists, else in the remote
user's home. This covers Air's `-p` settings read, which then reports the remote's settings.

### Chat

All chats of a project share one worker: an adapter on the remote, in the workspace the project belongs to.
`hermod air acp` holds a [session](#session) for each workspace that has a chat open in Air and routes Air's one
ACP connection between them, a new chat by the directory it names and every later message by its chat's session
ID.

For what belongs to no workspace, `air acp` runs one more adapter on the remote for as long as it runs itself: a
plain process over ssh, not a worker. It answers Air's `initialize`, authentication and the chat list, which it
reads from the remote's transcripts.

| Message from Air | What `air acp` does |
|------------------|---------------------|
| `initialize` | Removes the `fs` and `terminal` client capabilities, passes it to its own adapter, and Air gets that answer. It keeps the message: every workspace's adapter is sent it when it starts. |
| `session/new`, `session/load`, `session/resume` | Runs the session flow for the directory the message names, unless it already holds that workspace's session, then passes the message to the workspace's adapter. Hook settings in it go through the agent's rewrite. |
| `session/list`, and any message that names no open chat | Passes it to its own adapter. |
| `$/cancel_request` | Passes it to the adapter that has the request, with the request's ID as that adapter knows it. |
| `session/close` (a closed tab) | Answers it and passes nothing on, so the chat keeps running. When it was the workspace's last open chat, `air acp` detaches that session: the IO relay and the FS relays are released. |
| `_session/archive` ("mark done") | Passes on the `session/close` it held back, then the archive: the adapter ends the chat and records it. See [Close](#close). |
| stdin EOF or SIGTERM | Detaches every session it holds and releases every relay. The workers keep running. |

In the other direction, every message has its paths mapped back, and a request an adapter makes of Air (a
permission prompt, a question to the user) is given an ID of `air acp`'s own, since the adapters number theirs
independently.

An adapter ends every chat it runs when its ACP connection ends. A worker's stdin is a FIFO that stays open with
no client, so to an adapter the connection never ends; that is what a detach relies on.

**Closed tabs keep their hooks.** A workspace's Net relay is not released with its last tab: `air acp` keeps it
until the adapter is closed or `air acp` ends. Claude's hooks are how Air learns that an agent has finished or
waits for its user, and that matters most for a chat nobody is looking at. So with every tab closed, the files
stop travelling and the notifications do not.

**Returning to a running adapter.** A session that finds its workspace's adapter Running connects to it instead of
starting one. The adapter has already been initialized and may have written while nobody read:

- The kept `initialize` is not sent again: ACP allows it only as a connection's first request.
- Output held in the log for a chat Air has not opened is dropped: opening the chat (`session/load`) makes the
  adapter replay its history, and it reuses the chat it still runs instead of starting a second.
- A request the adapter made and nobody answered is delivered once Air has opened its chat and the replay has
  ended. The adapter does not make it again by itself, since its connection never ended.

### Close

Closing a worker ends its process (SIGHUP, so an agent saves its transcript). Its workspace then applies the
release rule to its relays, holding a lease while it decides so it cannot race another session: with another
worker left they stay, and with none they go. The worker's directory stays.

- **An adapter** is closed by `air acp` when it runs no chat, checked after each "mark done" and before a detach:
  `session/list` for the workspace's directory names its chats, and `session/status` says of each whether the
  adapter still runs it. A workspace with chats still running keeps its adapter and its relays.
- **A terminal worker** ends when its command exits; nothing in Air marks a terminal session done.
- A worker that is already Absent has nothing to close, and its relays are left as the last detach decided.

### Data between the client and the agent

```
LOCAL                                                              REMOTE

 chat
 requests   Air ──ACP──► air acp: route, map paths ──► IO relay ═══►  adapter stdin ──► claude, one per chat
 responses  Air ◄──ACP── air acp: map paths, own IDs ◄─ IO relay ◄═══  adapter stdout
 errors     log ◄───────────────────────────────────── IO relay ◄═══  adapter stderr

 terminal
 screen     tty ◄──────────────────────────────────── IO relay ◄═══►  claude in tmux
 history    ~/.claude ◄─────────────────────────────── FS relay ◄═══►  transcripts

 both
 hooks      Air URL ◄─ filter (token, routes, paths) ◄─ Net relay ◄═══  POST to the remote port
 files      project ◄───────────────────────────────── FS relay ◄═══►  project
```

Paths cross between the workstations in four places: a worker's arguments, ACP messages, hook bodies and the files
themselves. The first three are mapped by the one path map the fourth defines.

## Decisions

### The adapter runs on the remote, one per workspace
ACP has to be translated for Claude, and `claude-agent-acp` does that and carries Air's extensions. Where it runs
decides what Hermod has to understand. On the remote, beside the Claude it starts, Hermod carries ACP: a public,
typed protocol that already has what a returning client needs (a chat is opened again with a replay of its
history). The adapter also reads the remote's own transcripts, so Air lists that remote's chats with no files
copied.

- **Alternative — the adapter local, starting Hermod as its `claude` for each chat:** rejected. Hermod would map
  paths in the SDK's stream, which is not a public protocol, and invent how a new client catches up on it; and
  the local adapter would need every remote's transcripts copied to a directory of their own to list the chats.
- **Alternative — Hermod speaks ACP itself, with no adapter:** rejected. It would follow two protocols Hermod does
  not own, and Air's extensions with them.

One adapter serves all the chats of a workspace, as it serves all of Air's chats locally. A project then has one
worker, one stream and one hooks port however many chats it runs, and a new chat starts nothing but its agent.

- **Alternative — one adapter per chat:** rejected. Every chat would cost an adapter process, a stream and a port
  of its own, and a new chat's worker could not be named until its adapter had answered with the session ID.

The cost is that a tab cannot be detached on its own: a workspace's stream is released with its last open chat.
The remote must also have one more program, `claude-agent-acp`, which ships as a standalone executable.

### `air acp` routes, the adapters translate
Air holds all its chats on one ACP connection, and each project is a workspace with an adapter of its own. So
`air acp` cannot be a pipe: it sends each message to its chat's workspace. It understands only what that takes
(which directory a new chat names, which chat a message belongs to, and the lifecycle messages) and never the
content of a conversation.

Answering `session/close` itself is what lets a chat outlive its tab and the IDE. ACP defines that request as
cancelling the session's work and freeing it, so an adapter that received it would stop the agent. Only "mark
done" ends a chat.

Renumbering is within ACP's rules: each side numbers its own requests, and an ID means something only between two
neighbours.

- **Alternative — ACP's proxy chains:** not used. They are a draft that the client has to take part in, and Air
  is not known to.

### An agent acts on the remote only
ACP lets an agent ask its client to read or write a file and to run a terminal, when the client's `initialize`
says it can. Those act on the client's machine, which here is the other workstation: a command would run locally
while the agent works on the remote. `air acp` removes both capabilities from `initialize`, so the adapter does
all of it on the remote, and the FS relay carries the result.

The cost is that the agent reads a file as it is on disk, not as it stands unsaved in Air's editor.

### The stand-in is `attach` under another name
One binary, one resolver, one flow. The alternatives each added a second way in: a hidden environment variable
telling `hermod` to act as Claude, a generated wrapper script, or a separate stand-in command with its own flow.
Naming the binary needs none of them, and a session typed by hand
(`hermod attach prod-box -- claude --resume <uuid>`) is the same session Air's terminal would open.

The cost is that the stand-in cannot take Hermod's flags, since all its arguments are Claude's. Its remote comes
from the environment, and so would any other option it comes to need.

### An agent declares, the entities act
Claude's arguments, settings and state follow Claude's CLI and change together when it changes. `claude` answers
the [agent interface]({A}#agentagent--an-agent-programs-contract)'s questions with values (a command, files, relay requirements, a rewritten settings document) and
performs nothing itself, so it needs no shell, no remote and no relay to be tested, and a new Claude version is
one package and its table-driven tests.

The same shape keeps the generic entities generic. A hook is, to `relay`, a remote port reaching a local address
through a filter; that the filter checks a token and an allowed route is Claude's declaration, not the relay's
knowledge. A user's `-R` is the same relay with no filter.

### Two sources say what state a session is in
Whether a conversation runs is the agent's to say, and whether a process exists under Hermod's name is tmux's.
Neither is inferred from the other or from files left behind, and what looks like a further state is a pair of
them: "running outside Hermod" is a Running agent with an Absent worker, and a stale session is a worker whose
agent is Stopped. The worker's directory holds only what the running process needs and decides nothing.

### A disconnected relay holds
A worker outlives its clients, so a relay must say what happens to traffic that arrives with nobody attached. The
contract is that it is held and delivered at the next connect: files wait on disk, a terminal waits in tmux, and a
line stream waits in its logs. An agent that asks its user a question therefore waits for the next client instead
of losing the question.

For a line stream, what is delivered depends on the protocol it carries, because a new client begins the protocol
again. For ACP that is [returning to a running adapter](#chat).

The Net relay is the exception in this change. Holding a call needs a listener on the remote that outlives the
client, and nothing of Hermod's runs there. When that is added (a local tool or MCP server the remote's agent
calls, queued until its user returns), it changes the Net relay alone, and a held call still lasts only as long as
its caller waits.

### A workspace holds its workers
Whether a relay may be removed depends on whether a process on the remote still needs it, so the two have to be
read together. With the worker inside the workspace, the workspace answers that from its own members, and
everything belonging to a project on a remote is found under one name.

- **Alternative — workspace and worker as independent entities, joined by the session:** rejected. The release
  rule then needs a third party to tell the workspace whether the remote still uses a relay, and the only link
  between a relay and a worker is a directory, which a transcript relay does not share with any worker.

A workspace still starts no process and reads no agent: `worker` is the mechanism, `control` decides when one
starts, and the workspace only counts them.

### File isolation is the worker's
Several sessions in one project share the project's FS relay, so two agents in one directory edit the same files.
Keeping them apart is the worker's choice, and both ways need nothing new from Hermod:

- **Inside the project relay:** an agent that works in a subdirectory of its own (Claude's `--worktree`) is
  carried by the relay already there.
- **A relay of its own:** a session started in a directory outside the project relay, such as a worktree beside
  the project, gets an FS relay for it in the same workspace.

### A terminal session's transcripts travel both ways
In terminal mode Air reads Claude's local config directory itself, so a conversation that ran on the remote has to
be there. Transcripts are files, so carrying them is an FS relay's work and no step of the flow copies one. The
agent declares the relay, and it is an FS relay like any other, carrying both ways: a conversation that exists
only locally is on the remote before the agent resumes it, and the remote's conversations appear locally for Air
to list.

The cost is that every conversation of the project in the local config directory reaches the remote, those started
locally included. Chats need no such relay: their adapter reads the remote's transcripts where they are.

### One identity: names and tags
Everything a session uses is findable from its workspace or from the `hermod` prefix, with the tool that owns it.

| Thing | Name | Tags | Read with |
|---|---|---|---|
| Workspace | the name of its project FS relay | — | the two rows below |
| Worker (remote) | tmux session `hermod-<key>`, or `hermod-acp-<workspace>` for an adapter; a plain `attach` keeps its name | options `@hermod-workspace`, `@hermod-origin` | `tmux ls -F '#{session_name} #{@hermod-workspace} #{@hermod-origin}'` |
| Worker's directory (remote) | `~/.hermod/s/<tmux session>/` | — | — |
| FS relay (local) | `hermod-<remote>-<slug>-<local>`: `<slug>` from its remote end, `<local>` a hash of its local end | labels `hermod-remote=<remote>`, `hermod-workspace=<workspace>` | `mutagen sync list --label-selector hermod-workspace=<workspace>` |
| Log (local) | `~/.cache/hermod/air/<key>.log` | — | — |

- **`@hermod-workspace`** is the workspace the worker belongs to. Whether a workspace's relays are still needed
  is read from it, so no file on either workstation has to record it. `hermod-remote` lists every workspace on a
  remote.
- **Origin** is the machine the session was started from: its hostname, or `HERMOD_ORIGIN` when set, reduced to
  letters, digits, dots and dashes. An empty `HERMOD_ORIGIN` leaves the tag out, for a shared remote where the
  machine's name should not show.
- **The agent's own session name is not Hermod's to set.** It is what the user sees in Air and in Claude's own
  lists, so Hermod passes no `--name`. A Claude that Hermod runs is recognized by its tmux session.
- An FS relay is named after its two ends and nothing else: not the worker, the agent, or what the files are. Any
  session that needs the same two ends finds the same relay and leases it.
- `workspace` derives these names and the labels; `relay` and `worker` are given them.

### The remote never holds Air's secrets
Air's hook token changes with every IDE run and is local by design. The hook settings that reach the remote carry
a per-worker token generated by Hermod; the Net relay's filter accepts only that token and only the routes the
settings named, and substitutes Air's current token. A running Claude's settings therefore stay valid across IDE
restarts, and Air's token never leaves the local machine. The worker's token is needed because a loopback port on
a shared remote is reachable by every user there. Secrets reach the remote only inside the worker's `0700`
directory or the worker's own stdin, over ssh, never on a command line Hermod writes.

### Only declared variables reach the remote
`cli` reads the process environment once. Of it, the worker is given the variables the agent declared it needs and
nothing else, so a local credential or setting never travels by default.

### Each remote's chats are listed by its own agent
Air lists chats by asking the agent (`session/list`). Each entry `configure` writes is one remote's `air acp`, and
the adapters it asks read that remote's transcripts. "Claude (A)" then lists only A's chats and "Claude (B)" only
B's, so a chat can only be reopened on the remote it lives on, and Hermod needs no record of which remote holds
which chat. The user's local sessions stay out of both lists.

Two cases stay unseparated. Hermod's documentation advises against several remotes for one project in them; no
code guards against it:

- **Air's terminal mode.** Air reads `~/.claude` itself there, so every remote's conversations share one list, and
  one reopened while another remote is configured forks onto it.
- **Claude started by hand.** `hermod attach B -- claude --resume <uuid of a chat on A>` forks the chat onto B.

### Relay names carry the remote
Today a mirror is named after the project's remote path alone (`dev-api`), so attaching to B in a project already
mirrored to A finds `dev-api` and resumes the mirror to A: the session runs on B while the files go to A. An FS
relay's name starts with `hermod-<remote>-` instead (the alias reduced to the letters, digits and dashes Mutagen
accepts), and connecting one checks its remote end first. Mirrors made under the old names are no longer found;
the first run lists them and leaves terminating them to the user.

### An FS relay never picks a winner
The developer has to see what the remote changed in the project's files, and to bring local changes to the remote.
Both need a relay that carries edits each way and discards neither side's: every FS relay is `two-way-safe`. A
file changed on both sides is left as it is on each and reported by Mutagen as a conflict until the developer
settles it.

- **Alternative — local wins (`two-way-resolved`):** rejected. A local edit silently overwrites the agent's, so
  what the remote changed can no longer be seen.
- **Alternative — one-way:** rejected. One side's changes never reach the other.

### Concurrent remotes are allowed; conflicts are the developer's
One project can be attached to several remotes at once: each has its own workspace, with its own relays, workers
and ports. The local directory then relays between them, so one agent's edits reach the other remotes too. Hermod
allows this and does not manage it. It neither orders nor resolves the edits; what Mutagen reports as a conflict
is the developer's to settle, and so is excluding per-machine build output.

### Mutagen is the FS relay registry
Which local roots are present on a remote, and where, is state Mutagen already holds: an FS relay's ends are the
root pair. Hermod labels its relays and reads the roots from `mutagen sync list`, keeping no file of its own that
could drift. Paths stay in the ends because a label value cannot hold one.

### Leases are file locks
Each session that connects a relay through its workspace holds a shared `flock` on
`$XDG_RUNTIME_DIR/hermod/lease/<relay>`. On release it tries a non-blocking exclusive lock; the process that gets
it held the last lease and applies the release rule. The kernel releases a crashed process's locks, so a killed
Hermod never leaves a relay leased. The lease is `workspace`'s own detail and no other package sees it.

- **Alternative — a local daemon owning relays:** rejected. `hermod air acp` lives as long as the IDE run and
  could host one, but terminal mode and `attach` have no such process.

### The notification back-channel is removed
`hermod notify`, the `-N` flag, the `notify` package, `sandbox.Channel` and `HERMOD_NOTIFY_SOCK` go. The channel
does not fit what this design makes of Hermod:

- **It wants Hermod on the remote.** Its sender is `hermod notify` run there, which is what the
  `add-remote-agent` proposal exists to install. Nothing of Hermod's runs on the remote now.
- **It is one socket per remote user**, owned by the most recent attach. With several sessions attached at once,
  all but one lose it.
- **Air does not need it.** Claude's hooks reach Air through the Net relay, and Air shows "needs input" and "done"
  itself.

What is lost is the desktop toast for a session attached from a plain terminal. If that is wanted again, the Net
relay is the transport to build it on: Claude's own `Stop` and `Notification` hooks, received locally.

The `add-notify-backchannel` and `add-remote-agent` changes are withdrawn with it.

## Risks / Trade-offs

- **Returning to a running adapter is unproven.** That the adapter reuses a chat it still runs was read in its
  source (0.109.0); how an unanswered request is delivered after the replay was not. → Until shown to work, a
  connect to a Running adapter closes it and starts a new one: its chats resume from their transcripts, losing
  only a turn in flight.
- **`session/status` is a draft in ACP.** The adapter (0.109.0) serves it; a later one may change it. → When
  it is refused, an adapter is closed only when its workspace's last chat is marked done in the same run.
- **A conversation that runs in a terminal worker and is opened as a chat is started a second time** by the
  adapter, which knows nothing of the first. → `air acp` asks the agent whether a conversation runs before it
  passes on a load, and refuses one that runs outside the adapter.
- **The remote's adapter must suit the local Air.** Air pins the adapter version it ships; the remote has
  whichever was installed there. → `air acp` logs both sides' versions from `initialize`; documentation says to
  install the version Air uses.
- **Air's extension files may be expected locally.** The adapter writes plan files and reads the directories Air
  adds (`air-views`), which exist only locally. → Verify with `air-probe`; if Air needs them, they are FS relays
  the agent declares.
- **Where Air's hook settings travel in ACP has not been observed.** → Verify in `acp-tap` logs. If they are not
  in the messages `air acp` reads, chats run without Air's hooks.
- **The adapter hands a chat's MCP servers and settings to Claude on its command line** when it is told which
  `claude` to run, where the remote's other users can read them. → Run the Claude that ships with the adapter, or
  accept it on a remote that is not shared.
- **A chat's MCP server may name a local program**, which the remote does not have: the agent launches the
  servers it is given. → Verify what Air passes; the server then fails to start on the remote and the chat goes
  on without it. A server Air serves over the ACP connection itself is carried like any other message.
- **A held hook call does not outlast Claude's hook timeout**, and how long Claude waits for a tool or MCP call is
  not known. → Measure before building the Net relay's Disconnected state.
- **A relay leased from another workspace follows that workspace's workers**, not the borrower's, so it could go
  Absent under a detached borrower. → No case in this change borrows across workspaces: an agent's transcript
  relay has ends of its own per project directory. Guard it when a case appears.
- **A linked worktree's pointer files hold paths.** → They resolve on both workstations only as
  [relative paths](../../../docs/air-integration.md#mirrors-cover-roots-worktrees-resolve-by-path).
- **A Detached workspace shows nothing locally** until the next attach. → Accepted: a client that wants the
  files attaches.
- **A remote port picked free can be taken before `ssh -R` binds it.** → That session runs without its Net relay,
  logged; the next one tries the same port again.

## Delivery

Each step is usable on its own and builds only on the ones before it.

1. `env`, the single `shell.Command`, and `pathmap`.
2. The FS relay and `workspace` with no workers yet: which workspace a directory belongs to, names that carry the
   remote, labels and leases.
3. `worker` inside the workspace, the release rule, the terminal IO relay and the session flow for a command with
   no agent: `hermod attach` with `HERMOD_REMOTE` and `HERMOD_REMOTE_CMD`; the notification back-channel removed.
4. The agent interface and `claude`: arguments, running state, launch; one-shot calls; terminal sessions; the
   transcript relay; the stand-in link in `make install`.
5. The Net relay: Claude's filtered hooks, and `attach -R`.
6. The line-stream IO relay and `acp`; `hermod air acp` with its own adapter and one workspace, paths mapped,
   `session/close` held back and "mark done"; `hermod air configure`.
7. Chats of several workspaces on one connection, and returning to a running adapter.

## Open Questions

- **Which ACP version does Air ask for?** The adapter serves v1 and the v2 preview. They differ in what Hermod
  reads: v1 opens a chat with `session/load`, which replays and ends the replay with its answer; v2 uses
  `session/resume` with a `replay_complete` update. `acp` has to read the one Air uses.
- **When exactly does Air start the entry's command?** At IDE start, or at the first use of the agent. It decides
  only how early `air acp`'s own adapter runs.
- **Terminal mode has not been observed.** Switching Air to it still opened the chat UI. That Air starts the
  configured path under its own name is assumed.
- **`HERMOD_REMOTE` in terminal mode.** It has to be in Air's own environment (e.g. `~/.config/environment.d/`).
  Whether Air's terminal-mode setting can carry environment of its own is not known.
- **Subscription use.** Air ships the adapter with `--hide-claude-auth`; whether running it without that flag is
  within Anthropic's terms has to be settled before a release.
- The questions in [`docs/air-integration.md`](../../../docs/air-integration.md#open-questions) still stand.
