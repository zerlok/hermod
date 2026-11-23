## Context

[`docs/air-integration.md`](../../../docs/air-integration.md) records how JetBrains Air runs Claude Code, what a
bridge must do to move that agent to another machine, and what Air was seen to send. Hermod's components, their
states, its packages and its flows are in [ARCHITECTURE](../../../docs/ARCHITECTURE.md). This design gives the
command line the change adds, what Air expects of it, and the decisions the change is built on with their reasons.
For how Hermod carries out each one it links to ARCHITECTURE.

Hermod connects a local workstation to a **remote** one. Sandboxing an agent, which the Air document describes, is
one use of a remote; nothing in Hermod depends on what the remote is for.

The bare `hermod <sandbox>` form runs one flow for one interactive terminal. The Air bridge adds two callers that
are programs. Air's terminal tab starts Hermod as its `claude` and expects Claude's own CLI on a terminal. Air's
chat UI speaks the Agent Client Protocol (ACP) to an agent it starts from `acp.json`, and expects
`claude-agent-acp`, the adapter that translates ACP for Claude, to answer.

## Goals / Non-Goals

**Goals:**
- One binary and one session flow. A person's `hermod attach`, Air's terminal tab and an Air chat run the same
  flow and differ only in what they hand it.
- Every fact has one owner. An entity is the owner of one fact; no other entity restates it or decides by it.
- Chat mode registered with Air as one `acp.json` entry per remote, written by Hermod.
- Each Hermod process reads its own arguments and environment in one place, an argument winning over the
  environment ([The command-line seam](../../../docs/ARCHITECTURE.md#the-command-line-seam)).
- Everything Claude-specific behind one interface, unit-testable offline, so a second agent is a second
  implementation.
- A chat outlives its tab and the IDE: only "mark done" ends it.
- One project attached to several remotes at once, and several sessions in one project sharing its relays.

**Non-Goals:**
- Translating ACP. The adapter does that, on the remote; Hermod routes ACP messages and maps the paths in them.
- Rewriting MCP configuration. The MCP servers a chat or a command line names are passed on as given and run on
  the remote; a server that needs a local program is not bridged.
- Holding calls to local endpoints while no client is attached. The relay model allows it
  ([later](#a-disconnected-relay-holds)); this change does not build it.
- Deleting chats (Air offers no delete).
- Ordering the file, transcript and event channels against each other.
- Resolving sync conflicts between remotes; see
  [Concurrent remotes](#concurrent-remotes-are-allowed-conflicts-are-the-developers).
- A local record of sessions or chats (state files or a database). Nothing in this design needs one.
- A command that sets up a workspace ahead of its first session, or prints a workspace's state. A workspace's
  state is readable ([Workspace](../../../docs/ARCHITECTURE.md#workspace)); this change adds no command for it.
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
| `HERMOD_ORIGIN`     | The name this machine is given on the remote; the local hostname when unset. Set to empty, nothing on the remote names the machine ([Names and tags](../../../docs/ARCHITECTURE.md#names-and-tags)). |

An argument always wins over the environment. Each flag's default is its variable's value, so a command reads one
resolved value and never looks at the environment again.

### `hermod attach`

Runs a [session](../../../docs/ARCHITECTURE.md#session-hermod-attach-and-claude-on-a-terminal): the workspace the
directory belongs to, a worker in it running `COMMAND` on the remote, and the caller attached to both. It replaces
the bare `hermod <sandbox>` form.

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
- It runs a conversation on a terminal only. Without one it runs
  [one-shot calls](../../../docs/ARCHITECTURE.md#one-shot-call) and refuses a conversation, naming
  `hermod air acp` as the way a program holds one.
- Its stdout belongs to Claude's caller ([The command-line seam](../../../docs/ARCHITECTURE.md#the-command-line-seam)).
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

- `command` is an absolute path, since Air does not load the user's shell.
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

What the entry runs: Air's agent for one remote. Each project's chats run in one adapter on the remote, a worker
of the project's workspace, and `air acp` routes Air's one ACP connection between them
([Chat](../../../docs/ARCHITECTURE.md#chat-hermod-air-acp)). To Air it is the adapter; to each adapter it is Air.

- **It starts** when Air starts the entry's command: once per IDE run, when Air first needs the agent, and not
  once per chat.
- **It ends** when Air ends it: at an IDE quit Air closes its stdin and sends SIGTERM, and a vanished IDE leaves
  it with a closed stdin. Either way it detaches every session it holds and exits; nothing on the remote ends with
  it.

The adapter stays a separate program. It translates ACP into Claude's own protocol and carries Air's extensions,
negotiated when Air declares itself at `initialize`. Hermod reads only the few messages it must route or act on
([Air's messages](../../../docs/ARCHITECTURE.md#airs-messages)) and passes every other one on with its paths
mapped.

## What Air expects

Observed in [`docs/air-integration.md`](../../../docs/air-integration.md); each row is a fact of Air's that the
change answers.

| Air | What answers it |
|-----|-----------------|
| Terminal mode starts the configured executable with Claude's arguments and nothing in front ([Terminal mode](../../../docs/air-integration.md#terminal-mode)). | [The stand-in](#the-stand-in-hermod-named-claude) |
| Terminal mode lists and resumes conversations from the local `~/.claude` ([Terminal mode](../../../docs/air-integration.md#terminal-mode)). | [A terminal session's transcripts travel both ways](#a-terminal-sessions-transcripts-travel-both-ways) |
| Air reads extra agents from `~/.jetbrains/acp.json`, each a `command`, `args` and `env`, and does not load the user's shell ([Chat mode](../../../docs/air-integration.md#chat-mode-acp)). | [`hermod air configure`](#hermod-air-configure-claude-remote) |
| Each running IDE starts one agent process for all chats of all its open projects. A chat names its directory when it is opened; every later message carries only its session ID ([Session lifecycle](../../../docs/air-integration.md#session-lifecycle)). | [`air acp` routes, the adapters translate](#air-acp-routes-the-adapters-translate), [The agent program knows where a chat belongs](#the-agent-program-knows-where-a-chat-belongs) |
| Air numbers its requests from 1 in one sequence across projects. An adapter numbers its own from 0 across its chats, and Air answers under the same ID ([Session lifecycle](../../../docs/air-integration.md#session-lifecycle)). | [Request IDs](../../../docs/ARCHITECTURE.md#request-ids) |
| Air sends `session/close` for every closed tab and for every open chat at an IDE quit; ACP defines it as cancelling the session's work and freeing it ([Session lifecycle](../../../docs/air-integration.md#session-lifecycle)). | [`air acp` routes, the adapters translate](#air-acp-routes-the-adapters-translate) |
| "Mark done" sends `session/close` if the chat is open, then `_session/archive`, which only the adapter acts on ([Session lifecycle](../../../docs/air-integration.md#session-lifecycle)). | [Air's messages](../../../docs/ARCHITECTURE.md#airs-messages) |
| Air polls `session/list` every 60 s for each open project directory ([Session lifecycle](../../../docs/air-integration.md#session-lifecycle)). | [Each remote's chats are listed by its own agent](#each-remotes-chats-are-listed-by-its-own-agent) |
| Air declares itself with `_meta.jetbrains.air` at `initialize`, which turns on the adapter's Air extensions ([Chat mode](../../../docs/air-integration.md#chat-mode-acp)). | [Hermod answers what names no chat](#hermod-answers-what-names-no-chat) |
| Air runs the configured `claude -p …` outside any chat to read Claude's settings ([Chat mode](../../../docs/air-integration.md#chat-mode-acp)). | [One-shot call](../../../docs/ARCHITECTURE.md#one-shot-call), which then reports the remote's settings |
| Air's hook settings call `localhost:<port>` with a Bearer token, both new with every IDE run, and time out after 1 s ([Terminal mode](../../../docs/air-integration.md#terminal-mode)). | [The remote never holds Air's secrets](#the-remote-never-holds-airs-secrets) |
| Air hands the agent an MCP server whose command is the local IDE binary ([MCP servers](../../../docs/air-integration.md#9-mcp-servers-that-run-local-programs--decided-not-bridged)). | Not bridged (Non-Goals) |

## How Hermod does it

Hermod is built from six components: session, workspace, relay, worker, agent and workstation. Each one, its
states and its package are in [ARCHITECTURE](../../../docs/ARCHITECTURE.md#components); every rule the change
follows is in one section there:

| Topic | ARCHITECTURE |
|-------|--------------|
| What state is stored where | [Storage](../../../docs/ARCHITECTURE.md#storage) |
| Leases, and how one is suspended or released | [Leases](../../../docs/ARCHITECTURE.md#leases), [Detach](../../../docs/ARCHITECTURE.md#detach) |
| The steps of every session, and what each caller hands it | [Session flow](../../../docs/ARCHITECTURE.md#session-hermod-attach-and-claude-on-a-terminal) |
| Arguments that name no conversation | [One-shot call](../../../docs/ARCHITECTURE.md#one-shot-call) |
| Routing Air's connection to each workspace's adapter | [Chat](../../../docs/ARCHITECTURE.md#chat-hermod-air-acp) |
| Ending a worker | [Close](../../../docs/ARCHITECTURE.md#close) |
| Which workspace a directory belongs to | [Which workspace a directory belongs to](../../../docs/ARCHITECTURE.md#which-workspace-a-directory-belongs-to) |
| Which relays a workspace has, and the path map | [How relays enter a workspace](../../../docs/ARCHITECTURE.md#how-relays-enter-a-workspace) |
| Names, tags and labels | [Names and tags](../../../docs/ARCHITECTURE.md#names-and-tags) |
| What a worker starts with | [A worker's directory](../../../docs/ARCHITECTURE.md#a-workers-directory) |
| A chat adapter's stdio | [Line streams](../../../docs/ARCHITECTURE.md#line-streams) |
| An agent's hooks | [Hooks](../../../docs/ARCHITECTURE.md#hooks) |
| Every channel between the client and the agent | [Data between the client and the agent](../../../docs/ARCHITECTURE.md#data-between-the-client-and-the-agent) |

## Decisions

### The adapter runs on the remote, one per workspace
ACP has to be translated for Claude, and `claude-agent-acp` does that and carries Air's extensions. Where it runs
decides what Hermod has to understand. On the remote, beside the Claude it starts, Hermod carries ACP: a public,
typed protocol that already has what a returning client needs (a chat is opened again with a replay of its
history). The adapter also resumes chats from the remote's own transcripts, so no files are copied for them.

- **Alternative — the adapter local, starting Hermod as its `claude` for each chat:** rejected. Hermod would map
  paths in the SDK's stream, which is not a public protocol, and invent how a new client catches up on it; and
  the local adapter would need every remote's transcripts copied to a directory of their own to list the chats.
- **Alternative — Hermod speaks ACP itself, with no adapter:** rejected. It would follow two protocols Hermod does
  not own, and Air's extensions with them.

The adapter ends every chat it runs when its ACP connection ends. A worker's stdin keeps that connection open
with no client ([Line streams](../../../docs/ARCHITECTURE.md#line-streams)), so a chat outlives its tab.

One adapter serves all the chats of a workspace, as it serves all of Air's chats locally. A project then has one
worker, one stream and one hooks port however many chats it runs, and a new chat starts nothing but its agent.

- **Alternative — one adapter per chat:** rejected. Every chat would cost an adapter process, a stream and a port
  of its own, and a new chat's worker could not be named until its adapter had answered with the session ID.

The cost is that a tab cannot be detached on its own: a workspace is detached with its last open chat. With it
the workspace's Net relay goes too, so Air hears no hook of a hidden chat until one of the project's chats is
opened again; a Net relay is never Disconnected ([Leases](../../../docs/ARCHITECTURE.md#leases)). The remote must
also have one more program, `claude-agent-acp`, which ships as a standalone executable.

### `air acp` routes, the adapters translate
Air holds all its chats on one ACP connection, and each project is a workspace with an adapter of its own. So
`air acp` cannot be a pipe: it sends each message to its chat's workspace. It understands only what that takes
(the connection start, the chat list, a cancel, which directory a new chat names, which chat a message belongs
to, and the lifecycle messages) and never the content of a conversation. Each message is given one of a few
meanings (start the connection, list the chats, cancel a request, open, suspend or close a chat, a remote's
question, the client's answer, or pass through), so the routing knows neither ACP's wire format nor Air's own
method names ([Air's messages](../../../docs/ARCHITECTURE.md#airs-messages)).

Answering `session/close` itself is what lets a chat outlive its tab and the IDE. ACP defines that request as
cancelling the session's work and freeing it, so an adapter that received it would stop the agent. It is a
suspend, and only "mark done", a close, ends a chat.

Renumbering the adapters' requests is within ACP's rules: each side numbers its own requests, and an ID means
something only between two neighbours.

- **Alternative — ACP's proxy chains:** not used. They are a draft that the client has to take part in, and Air
  is not known to.

### Hermod answers what names no chat
A client's connection start, its chat list and its cancels belong to no one chat, while every adapter serves one
workspace. `air acp` answers the first two itself from what the agent declares, and sends a cancel to the
workspace whose adapter holds the cancelled request
([The client's connection](../../../docs/ARCHITECTURE.md#the-clients-connection)). A connection start is then
answered before any workspace is attached, and the chat list comes from the agent program on the remote, which
holds every conversation there.

- **Alternative — one more adapter for what belongs to no workspace, run over ssh for as long as `air acp` runs:**
  rejected. It is one more process on the remote for the IDE's whole run, and a cancel, which names no chat,
  would reach it instead of the adapter that holds the request.

### The agent program knows where a chat belongs
Hermod keeps no record of chats. While a session holds a workspace, `air acp` knows its chats from their opening;
a chat whose session has detached, or that `air acp` has not seen since a restart, reaches it by its ID alone. The
agent program on the remote holds every conversation with its directory, so `air acp` asks it, attaches the
workspace of that directory, and takes the chat in the state the program reports
([Events](../../../docs/ARCHITECTURE.md#events)). "Mark done" after the project's last tab was closed, and a
reopen after an IDE restart, then reach the right adapter. A chat the program does not have is an error for that
request only.

- **Alternative — keep the owner of a chat after its session detaches:** rejected. It is lost with the process,
  so a restart would still need the agent program, and the map would be a second copy of what the program holds.

### The last close ends the adapter
An adapter left with no running conversation keeps its workspace Detached, and its relays Disconnected, until the
remote ends it. So when a close leaves a workspace with no chat shown, `air acp` asks the agent program whether a
conversation of the workspace, in its root or a directory under it, still runs, and with none it closes the
adapter worker, which releases the relays
([Session states](../../../docs/ARCHITECTURE.md#session-states)). One adapter runs every chat of its workspace,
so it is ended only when none runs; a suspended chat's adapter runs on.

### An agent acts on the remote only
ACP lets an agent ask its client to read or write a file and to run a terminal, when the client's `initialize`
says it can. Those act on the client's machine, which here is the other workstation: a command would run locally
while the agent works on the remote. `air acp` removes both capabilities from `initialize`
([The client's connection](../../../docs/ARCHITECTURE.md#the-clients-connection)), so the adapter does all of it on
the remote, and the FS relay carries the result.

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
the [agent interface](../../../docs/ARCHITECTURE.md#agentagent--an-agent-programs-contract)'s questions with
values and performs nothing itself, so it needs no shell, no remote and no relay to be tested, and a new Claude
version is one package and its table-driven tests.

The same shape keeps the generic entities generic. A hook is, to `relay`, a remote port reaching a local address
through a [filter](../../../docs/ARCHITECTURE.md#relayfilter--what-a-net-relay-lets-through); that the filter
checks a token and an allowed route is Claude's declaration, not the relay's knowledge. A user's `-R` is the same
relay with no filter.

### Two sources say what a conversation is doing
Whether a conversation runs is the agent program's to say, and whether a process exists under Hermod's name is
tmux's ([Storage](../../../docs/ARCHITECTURE.md#storage)). Neither is inferred from the other or from files left
behind, and what looks like a further state is a pair of them: "running outside Hermod" is a Running agent with an
Absent worker, and a stale session is a worker whose agent is Stopped. The worker's directory holds only what the
running process needs and decides nothing.

### A disconnected relay holds
A worker outlives its clients, so a relay must say what happens to traffic that arrives with nobody attached. The
contract is that it is held and delivered at the next connect
([Relay](../../../docs/ARCHITECTURE.md#relay)). An agent that asks its user a question therefore waits for the
next client instead of losing the question.

For a line stream, what is delivered depends on the protocol it carries, because a new client begins the protocol
again. For ACP the adapter replays a chat's history when the chat is opened again, and a question asked while the
chat was hidden, with another chat of its project shown, is held until then
([Chat states](../../../docs/ARCHITECTURE.md#chat-states)).

The Net relay is the exception in this change. Holding a call needs a listener on the remote that outlives the
client, and nothing of Hermod's runs there. When that is added (a local tool or MCP server the remote's agent
calls, queued until its user returns), it changes the Net relay alone, and a held call still lasts only as long as
its caller waits.

### A workspace holds its workers
Whether a relay may be removed depends on whether a process on the remote still needs it, so the two have to be
read together. With the worker inside the workspace, the workspace answers that from its own members
([Detach](../../../docs/ARCHITECTURE.md#detach)), and everything belonging to a project on a remote is found under
one name.

- **Alternative — workspace and worker as independent entities, joined by the session:** rejected. A third party
  would then have to tell the workspace whether the remote still uses a relay, and the only link between a relay
  and a worker is a directory, which a transcript relay does not share with any worker.

A workspace decides no start and reads no agent; it reads its workers only to choose between suspend and release
([Packages](../../../docs/ARCHITECTURE.md#packages)).

### Leases are file locks
A lease is a lock the kernel holds for a live process, so a killed Hermod never leaves a relay leased
([Leases](../../../docs/ARCHITECTURE.md#leases)).

- **Alternative — a local daemon owning relays:** rejected. `hermod air acp` lives as long as the IDE run and
  could host one, but terminal mode and `attach` have no such process.

Whoever gives a lease up says whether the relay is suspended or released, and a relay acts on that only when it
was the last; the workspace chooses from its workers. A relay therefore counts leases and knows nothing of
workers.

- **Alternative — a second kind of lease, held on a worker's behalf while it runs:** rejected. A detached worker
  has no local process to hold a lock for it, so that lease would be a record someone must remember to end, and
  a relay would have to know what a worker is.

### File isolation is the worker's
Several sessions in one project share the project's FS relay, so two agents in one directory edit the same files.
Keeping them apart is the worker's choice, and both ways need nothing new from Hermod:

- **Inside the project relay:** an agent that works in a subdirectory of its own (Claude's `--worktree`) is
  carried by the relay already there.
- **A relay of its own:** a session started in a directory outside the project relay, such as a worktree beside
  the project, gets an FS relay for it in the same workspace
  ([How relays enter a workspace](../../../docs/ARCHITECTURE.md#how-relays-enter-a-workspace)).

### A terminal session's transcripts travel both ways
In terminal mode Air reads Claude's local config directory itself, so a conversation that ran on the remote has to
be there. Transcripts are files, so carrying them is an FS relay's work and no step of the flow copies one. The
agent declares the relay, and it is an FS relay like any other, carrying both ways: a conversation that exists
only locally is on the remote before the agent resumes it, and the remote's conversations appear locally for Air
to list.

The cost is that every conversation of the project in the local config directory reaches the remote, those started
locally included. Chats need no such relay: their adapter reads the remote's transcripts where they are.

### The remote never holds Air's secrets
Air's hook token changes with every IDE run and is local by design. The remote gets a token of the worker's own,
and a local filter swaps it for Air's current one ([Hooks](../../../docs/ARCHITECTURE.md#hooks)). A running
Claude's settings therefore stay valid across IDE restarts, and Air's token never leaves the local machine.
Secrets reach the remote only inside the worker's directory or its own stdin
([A worker's directory](../../../docs/ARCHITECTURE.md#a-workers-directory)).

### Each remote's chats are listed by its own agent
Air lists chats by asking the agent (`session/list`). Each entry `configure` writes is one remote's `air acp`, and
it asks the agent program on that remote. "Claude (A)" then lists only A's chats and "Claude (B)" only
B's, so a chat can only be reopened on the remote it lives on, and Hermod needs no record of which remote holds
which chat. The user's local sessions stay out of both lists.

Two cases stay unseparated. Hermod's documentation advises against several remotes for one project in them; no
code guards against it:

- **Air's terminal mode.** Air reads `~/.claude` itself there, so every remote's conversations share one list, and
  one reopened while another remote is configured forks onto it.
- **Claude started by hand.** `hermod attach B -- claude --resume <uuid of a chat on A>` forks the chat onto B.

### Relay names carry the remote
A mirror named after the project's remote path alone (`dev-api`) is found again by a session on any remote: in a
project mirrored to A, attaching to B would resume the mirror to A, and the session would run on B while the files
went to A. An FS relay's name therefore carries the remote, and connecting one checks its remote end first
([Names and tags](../../../docs/ARCHITECTURE.md#names-and-tags)).

Mirrors made under path-only names are not found under the new ones; the first run lists them and leaves
terminating them to the user.

### An FS relay never picks a winner
The developer has to see what the remote changed in the project's files, and to bring local changes to the remote,
so every FS relay is `two-way-safe`
([How relays enter a workspace](../../../docs/ARCHITECTURE.md#how-relays-enter-a-workspace)).

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
root pair. Hermod reads them from Mutagen and keeps no file of its own that could drift
([How relays enter a workspace](../../../docs/ARCHITECTURE.md#how-relays-enter-a-workspace)).

### The notification back-channel is removed
`hermod notify`, the `-N` flag, the `notify` package, `sandbox.Channel` and `HERMOD_NOTIFY_SOCK` go. The channel
does not fit what this design makes of Hermod:

- **It wants Hermod on the remote.** Its sender is `hermod notify` run there, which is what the
  `add-remote-agent` proposal exists to install. Nothing of Hermod's runs on the remote.
- **It is one socket per remote user**, owned by the most recent attach. With several sessions attached at once,
  all but one lose it.
- **Air does not need it.** Claude's hooks reach Air through the Net relay, and Air shows "needs input" and "done"
  itself.

What is lost is the desktop toast for a session attached from a plain terminal. If that is wanted again, the Net
relay is the transport to build it on: Claude's own `Stop` and `Notification` hooks, received locally.

The `add-notify-backchannel` and `add-remote-agent` changes are withdrawn with it.

## Risks / Trade-offs

- **Returning to a running adapter is unproven.** That the adapter reuses a chat it still runs when the chat is
  opened again was read in its source (0.109.0), not observed with Air. → Verify with Air.
- **A conversation that runs in a terminal worker and is opened as a chat is started a second time** by the
  adapter, which knows nothing of the first. → Unguarded in this change.
- **The remote's adapter must suit the local Air.** Air pins the adapter version it ships; the remote has
  whichever was installed there. Air no longer sees the adapter's answer to `initialize`: it sees the `Handshake`
  the agent declares, which may promise a protocol version, capabilities or Air extensions the installed adapter
  lacks. → `air acp` logs both sides' versions from `initialize` and where the adapter's dropped answer differs
  from the declared `Handshake`; documentation says to install the version Air uses.
- **Air's extension files may be expected locally.** The adapter writes plan files and reads the directories Air
  adds (`air-views`), which exist only locally. → Verify with Air; if Air needs them, they are FS relays
  the agent declares.
- **Where Air's hook settings travel in ACP has not been observed.** → Verify on the ACP stream. If they are not
  in the messages `air acp` reads, chats run without Air's hooks.
- **The adapter hands a chat's MCP servers and settings to Claude on its command line** when it is told which
  `claude` to run, where the remote's other users can read them. → Run the Claude that ships with the adapter, or
  accept it on a remote that is not shared.
- **A chat's MCP server may name a local program**, which the remote does not have: the agent launches the
  servers it is given. → Verify what Air passes; the server then fails to start on the remote and the chat goes
  on without it. A server Air serves over the ACP connection itself is carried like any other message.
- **A held hook call does not outlast Claude's hook timeout**, and how long Claude waits for a tool or MCP call is
  not known. → Measure before building the Net relay's Disconnected state.
- **A relay leased from another workspace is suspended or released by the workspace that gives up its last
  lease**, from that workspace's workers, not the owner's, so it could go Absent under the owner's detached
  workers. → No case in this change borrows across workspaces: an agent's transcript relay has ends of its own
  per project directory. Guard it when a case appears.
- **A linked worktree's pointer files hold paths.** → They resolve on both workstations only as
  [relative paths](../../../docs/air-integration.md#mirrors-cover-roots-worktrees-resolve-by-path).
- **A Detached workspace shows nothing locally** until the next attach. → Accepted: a client that wants the
  files attaches.
- **The agent program's IDs are taken for Air's chat IDs.** Locating a chat and listing chats assume that the
  adapter uses Claude's session UUID as the ACP session ID
  ([The agent program knows where a chat belongs](#the-agent-program-knows-where-a-chat-belongs)); if it does not,
  every chat that `air acp` does not hold gets an error and listed chats cannot be resumed. → Verify on the ACP
  stream.
- **Reopening a chat marked done gets an error for its `_session/unarchive`**, which names a chat that no session
  holds any more and is not located ([Events](../../../docs/ARCHITECTURE.md#events)). → Unguarded in this change.
- **A chat opened, closed or marked done in a subdirectory of a project with a chat shown ends `air acp`.** The
  `Open`, or the chat located in that subdirectory, makes a second session for the same workspace, whose attach is
  refused ([Events](../../../docs/ARCHITECTURE.md#events)). → Unguarded in this change.
- **A question asked while a project has no chat shown reaches Air before a new chat is open.** It waits in the
  adapter's line stream and is delivered when a new chat of the project opens, ahead of that open's answer
  ([Events](../../../docs/ARCHITECTURE.md#events)). → Unguarded in this change.
- **An adapter whose chats were only suspended runs on.** Its workspace stays Detached and its relays
  Disconnected until a close leaves none of its conversations running. → Accepted: a Disconnected relay costs a Mutagen session and nothing
  travels on it.

## Delivery

The build order is in [tasks.md](tasks.md).

## Open Questions

- **Which ACP version does Air ask for?** The adapter serves v1 and the v2 preview. They differ in what Hermod
  reads: v1 opens a chat with `session/load`, which replays and ends the replay with its answer; v2 uses
  `session/resume` with a `replay_complete` update. `acp` has to read the one Air uses.
- **Terminal mode has not been observed.** Switching Air to it still opened the chat UI. That Air starts the
  configured path under its own name is assumed.
- **`HERMOD_REMOTE` in terminal mode.** It has to be in Air's own environment (e.g. `~/.config/environment.d/`).
  Whether Air's terminal-mode setting can carry environment of its own is not known.
- **Subscription use.** Air ships the adapter with `--hide-claude-auth`; whether running it without that flag is
  within Anthropic's terms has to be settled before a release.
- The questions in [`docs/air-integration.md`](../../../docs/air-integration.md#open-questions) still stand.
