## Context

[`docs/air-integration.md`](../../../docs/air-integration.md) records how JetBrains Air runs Claude Code, what a
bridge must do to move that agent to a sandbox, and the decisions already taken: Air is a terminal to sessions that
live on the sandbox, each session is a tmux session that only the user ends, nothing of Hermod's runs on the
sandbox, hooks get a sandbox port per session, and MCP servers run on the sandbox only. This design does not
restate those decisions. It names the entities Hermod is built from, gives each its states, and describes how
every command uses them.

It departs from that document in one place: a client that re-attaches to a running chat receives what the agent
produced meanwhile, instead of restarting the agent (see [A paused relay holds](#a-paused-relay-holds)).

Hermod today runs one flow for one interactive terminal, started as `hermod <sandbox>`. The Air bridge adds a
second kind of caller: a program (the Agent SDK, or Air's terminal tab) that starts Hermod as its `claude`, owns
Hermod's stdio, and expects Claude's own CLI contract on it.

## Goals / Non-Goals

**Goals:**
- One binary and one session flow. A person's `hermod attach`, Air's terminal tab and an Air chat run the same
  flow and differ only in what they hand it.
- Every fact has one owner. An entity is the owner of one fact; no other entity restates it or decides by it.
- Chat mode registered with Air as one `acp.json` entry per sandbox, written by Hermod.
- Each Hermod process reads its own arguments and environment in one place, an argument winning over the
  environment.
- Everything Claude-specific behind one interface, unit-testable offline, so a second agent is a second
  implementation.
- One project attached to several sandboxes at once, and several sessions sharing one workspace.

**Non-Goals:**
- Rewriting MCP configuration. Claude's `--mcp-config` arguments are carried like any other argument; a server
  that runs a local program is not bridged.
- Holding calls to local endpoints while no client is attached. The relay model allows it
  ([later](#a-paused-relay-holds)); this change does not build it.
- Deleting sessions (Air offers no delete).
- Ordering the file, transcript and event channels against each other.
- Resolving sync conflicts between sandboxes; see
  [Concurrent sandboxes](#concurrent-sandboxes-are-allowed-conflicts-are-the-developers).
- A local record of sessions (state files or a database). Nothing in this design needs one.
- Agents other than Claude Code. The command line names the agent (`hermod air acp claude`) and the code reaches
  it through an interface, but only `claude` is implemented.

## Command line

```
hermod attach         [SANDBOX] [-s NAME] [-C DIR] [-n] [-- COMMAND…]
hermod air configure  {claude} [SANDBOX] [--name=NAME] [--origin=NAME] [-n]
hermod air acp        {claude} [SANDBOX]
claude ARGS…          (a link to hermod)  ≡  hermod attach -- claude ARGS…
```

| Variable             | Meaning                                                                                      |
|----------------------|----------------------------------------------------------------------------------------------|
| `HERMOD_SANDBOX`     | The sandbox when `SANDBOX` is not given. A command with neither fails before doing anything.  |
| `HERMOD_SANDBOX_CMD` | `attach`'s command when no `-- COMMAND` is given: a command line run by the sandbox's shell.  |
| `HERMOD_ORIGIN`      | The name this machine is given on the sandbox; the local hostname when unset. Set to empty, nothing on the sandbox names the machine. |

An argument always wins over the environment. Each flag's default is its variable's value, so a command reads one
resolved value and never looks at the environment again.

### `hermod attach`

Runs a [session](#session): the directory's workspace, a worker running `COMMAND`, and the caller attached to it.
It replaces the bare `hermod <sandbox>` form. `-s` names the worker, `-C` gives the directory, `-n` prints the
plan and changes nothing.

### The stand-in: `hermod` named `claude`

Air, and the SDK under it, start Claude as an executable path followed by Claude's arguments; no setting lets
anything be put in front. So Hermod is given the name: `~/.local/share/hermod/bin/claude` is a symlink to `hermod`,
and a `hermod` started as `claude ARGS…` runs exactly what `hermod attach -- claude ARGS…` runs. Every other name
runs the normal command line.

- The stand-in has no `SANDBOX` argument, so its sandbox is `HERMOD_SANDBOX`.
- Its command is given, so `HERMOD_SANDBOX_CMD` does not apply.
- Stdout belongs to Claude's caller: all of Hermod's own output, the dry-run plan included, goes to stderr and to
  a per-session log (`~/.cache/hermod/air/<uuid>.log`, mode `0600`).
- The link is outside `PATH`, so a local `claude` is never shadowed. `make install` creates it beside the binary's
  own installation; Hermod itself never writes it.

### `hermod air configure claude [SANDBOX]`

Registers a sandbox with Air by merging one entry into `~/.jetbrains/acp.json`, keeping every other entry and key:

```json
{
  "agent_servers": {
    "Claude (prod-box)": {
      "command": "/home/<you>/go/bin/hermod",
      "args": ["air", "acp", "claude"],
      "env": {
        "HERMOD_SANDBOX": "prod-box",
        "PATH": "<the PATH configure ran with>",
        "CLAUDE_CODE_EXECUTABLE": "/home/<you>/.local/share/hermod/bin/claude",
        "CLAUDE_CONFIG_DIR": "/home/<you>/.local/share/hermod/air/prod-box/claude"
      }
    }
  }
}
```

- `air acp` finds `claude-agent-acp`, and through it `node`, on `PATH`. Air does not load the user's shell, but
  `configure` runs in it, so the entry carries that shell's `PATH`. `configure` fails when the adapter is not on
  it, or when the stand-in link is missing.
- The sandbox goes into `env`, not `args`: the per-chat stand-ins inherit the entry's environment but never see
  `air acp`'s arguments, and both must resolve the same sandbox.
- `--name` sets the entry's name, `Claude (<sandbox>)` by default. Configuring the same name again replaces the
  entry. One entry per sandbox makes each sandbox its own agent in Air's picker.
- `--origin=NAME` writes `HERMOD_ORIGIN` into the entry's `env`; `--origin=` writes it empty, which leaves the
  machine unnamed. With neither the flag nor the variable, the entry has no `HERMOD_ORIGIN` and each chat uses the
  hostname.
- `-n` prints the entry and changes nothing.

That entry is all chat mode needs. For terminal mode, which `acp.json` plays no part in, `configure` also prints
the link's path, to be entered as Air's Claude executable, and the `HERMOD_SANDBOX=<sandbox>` line that Air's own
environment needs.

### `hermod air acp claude [SANDBOX]`

What the entry runs. It starts the ACP adapter with its own environment unchanged and relays ACP messages both
ways verbatim. The adapter passes that environment on to every `claude` it spawns, which is how each chat's
stand-in gets `HERMOD_SANDBOX`.

It acts on one message: `_session/archive` ("mark done") [closes](#close) that chat's session. The adapter is the
only process that sees it (see [Session lifecycle](../../../docs/air-integration.md#session-lifecycle)).

The adapter stays a separate program. It translates ACP into the Agent SDK's stream protocol and carries Air's
own extensions, negotiated when Air declares itself at `initialize`; Hermod relays that exchange and takes no part
in it.

## Entities

```
LOCAL workstation                                                 SANDBOX workstation

client ─────────────► session ─┬─ workspace ── FS relay   project dir  ⇄  project dir
(you, Air's terminal,          │               FS relay   transcripts  ◄  transcripts
 the ACP adapter)              │
                               └─ worker ───── IO relay   stdio        ⇄  the agent's stdio
                                               Net relay  local URL    ◄  a sandbox port
```

A **workstation** is a machine commands run on: the local one, or the sandbox. Hermod creates neither and keeps no
state for either, so a workstation is not an entity. It is a `shell.Shell`, the low-level API everything below is
built on: a shell runs one `shell.Command` (arguments, directory, environment, stdin, stdout, stderr) and holds no
state between commands.

| Entity    | The one fact it owns | Identified by | Lives until |
|-----------|----------------------|---------------|-------------|
| Relay     | One channel between the two workstations, and whether it is carrying | its kind and its two ends | its owner is gone |
| Workspace | A project present on both workstations: its FS relays and the path map they define | sandbox + project root | no client is attached and no worker lives in it |
| Worker    | One persistent process on the sandbox, with its IO and Net relays | a key: the agent's session ID, or a name | the process exits or is closed |
| Agent     | A conversation as the agent program knows it, and that program's contract | the agent's own session ID | the agent forgets it |

They nest one to many: a sandbox holds many workspaces, and a workspace holds many workers. Two chats in one
project are two workers in one workspace.

A **session** is one worker in its workspace. It is not stored anywhere; it is what the commands operate on.

### Relay

| State   | Meaning |
|---------|---------|
| Absent  | Does not exist. |
| Paused  | Exists and accepts traffic at the sandbox end, but delivers nothing. What arrives is held. |
| Running | Delivers in both directions. Entering this state first drains what was held. |

Attaching a client moves relays from Paused to Running; detaching moves them back. "Whether a client is attached"
is therefore not a separate fact: it is the relays' state.

| Kind | Scope     | Carries | Mechanism | What Paused holds, and where |
|------|-----------|---------|-----------|------------------------------|
| FS   | workspace | files between two directories | a Mutagen session | file changes, on each side's disk |
| IO   | worker    | the worker's stdin and stdout | tmux for a terminal; a FIFO and a log for a line stream | output and the agent's questions: in tmux's screen, or in the log |
| Net  | worker    | calls from a sandbox port to a local endpoint, through a filter | `ssh -R` to a local listener | nothing: see below |

- The FS relays of a workspace define its **path map**: (local root, sandbox root) pairs, longest prefix first.
  The IO and Net relays apply that map to what they carry.
- A Net relay has no Paused state in this change. Nothing listens on the sandbox port once the client is gone, so
  it goes from Running to Absent and a call made meanwhile fails at once.
- Reading a relay's state can fail. That is a failure value, not a fourth state, and whoever asked treats it as
  "may be in use".

### Workspace

Its state is derived from two counts and stored nowhere.

| Clients attached | Workers alive | State      | Its FS relays |
|------------------|---------------|------------|---------------|
| ≥ 1              | any           | Attached   | Running |
| 0                | ≥ 1           | Background | Paused |
| 0                | 0             | Absent     | Absent, flushed first |

- **Clients attached** is counted by a lease each attached process holds on the workspace
  ([Leases are file locks](#leases-are-file-locks)). **Workers alive** is read from the sandbox's worker list.
- Background means the sandbox keeps working and the result waits: what the workers change reaches the local
  directory at the next attach.
- Only attach and detach move a workspace. The last client to detach reads the worker count and applies the
  table. When the count cannot be read, the workspace goes to Background, which never removes something in use.
- A directory inside an existing workspace's local root joins that workspace. Any other directory becomes a new
  workspace at the corresponding sandbox path, a root that holds existing roots included, with a warning: the
  inner workspaces keep their relays, so their files are carried twice.

### Worker

| State   | Meaning |
|---------|---------|
| Absent  | No tmux session under the key. |
| Running | The process is alive. |
| Exited  | The process failed; its pane is kept to show the error and exit status. |

A worker is the tmux session `hermod-<key>` on the sandbox, tagged with its workspace's directory and its origin
([Names and tags](#one-identity-names-and-tags)). Start takes Absent to Running; a clean exit or a close leads to
Absent; a failed exit leads to Exited; removing an Exited worker leads to Absent. tmux is the only source.

A worker's IO relay has one of two forms, fixed when the worker starts:

- **terminal:** the client joins the tmux session on a tty.
- **line stream:** the process reads a FIFO and appends to a log; the client's stdin and stdout are copied to and
  from them one complete line at a time.

### Agent

| State   | Meaning |
|---------|---------|
| New     | The agent has no record of the conversation. |
| Running | The agent lists it as running. |
| Saved   | Not running, and a transcript exists, on the sandbox or only locally. |

The agent program is the only source: for Claude, `claude agents --json` and the transcript files. An agent that
waits for an answer is Running; what it waits for is held in a relay.

`control` reaches an agent through one interface and never names Claude:

| Control asks | The agent answers |
|--------------|-------------------|
| What do these arguments ask for? | The conversation's key, whether to resume or fork and from which conversation, and the IO form; or that this is a one-shot call with no conversation. |
| What state is this conversation in? | The command that asks the agent program, and how to read its output into a state. |
| What does a worker for it need? | The command to run with paths mapped, the files it must find, the local variables it must be given, and the relays it requires. |
| How is a line of its stream mapped? | The line with its paths mapped, in either direction. |
| How does a new client catch up? | From the held output, what to deliver, what to answer on the agent's behalf, and what to send again. |

The agent is handed the path map and never learns where the other workstation is or how it is reached.

## Flows

### Session

Every session, whoever starts it, runs these steps. The rows of the table after them are the only differences.

1. **Attach the workspace.** Find the directory's workspace or define a new one, take a lease on it, and bring its
   project relay to Running, flushed. The path map is now fixed for the run.
2. **Read the states.** One call on the sandbox reads the worker's state and, for an agent, the conversation's.
3. **Decide.** One table decides, and nothing else in the code does:

   | Agent        | Worker | Action |
   |--------------|--------|--------|
   | Running      | Running | attach |
   | Running      | Absent  | refuse: it runs outside Hermod, and a start would be a second copy |
   | New or Saved | Exited, or Running without its agent | remove the worker, then decide as Absent |
   | Saved        | Absent  | start with resume, copying the transcript to the sandbox first when it is only local |
   | New          | Absent  | start; fail when the caller asked to resume |

   A command with no agent has only the worker column: Absent starts it, Running attaches. A fork resumes another
   conversation than its key, so that conversation's transcript is looked for too and copied when it is only local.
4. **Start**, when the table says so. Create the worker's directory `~/.hermod/s/<key>/` (mode `0700`) with what
   the agent's answer listed, written over ssh stdin, then start the worker in the project's sandbox directory.
5. **Attach the worker.** Bring the relays the agent requires to Running on one ssh connection that refuses a
   shared master: the IO relay, the Net relay when the agent asked for one, and the transcript FS relay. The client
   is now connected to the agent.
6. **Detach.** Stdin EOF, SIGTERM or a dropped connection pauses the worker's relays and nothing else; the worker
   keeps running. Hermod exits inside the SDK's 2 s + 5 s window, so it is never left orphaned.
7. **Detach the workspace.** Release the lease. The last client applies the [workspace table](#workspace).

| Started as | Command | Agent | IO form | Net relay |
|------------|---------|-------|---------|-----------|
| `hermod attach` | the user's | none | terminal | none |
| `claude ARGS…` on a tty (Air's terminal tab, or by hand) | Claude's, mapped | Claude | terminal | Air's hooks, when the settings carry them |
| `claude ARGS…` under the adapter (an Air chat) | Claude's, mapped | Claude | line stream | Air's hooks |

For Claude, the worker's directory holds `env` (the selected `CLAUDE_*` and `ANTHROPIC_*` variables and the git
identity), `settings.json` (the caller's hook settings aimed at the sandbox port, with the session token in place
of Air's), the port and the token; a line-stream worker adds the FIFO, the log and the process ID a close waits
on. Every setup command runs with stdin from `/dev/null`; Hermod's stdin is left for the agent.

### One-shot call

Arguments that name no conversation (a subcommand such as `auth`, `--version`, `--help`, `-p` without a session)
are not a session. Hermod runs the command on the sandbox with the caller's stdio and exits with its code: no
workspace, no worker, no relay. It runs in the project's sandbox directory when that exists, else in the sandbox
user's home. This covers the adapter's `claude auth status` and Air's `-p` settings read, which then report the
sandbox's login and settings.

### Close

On `_session/archive` for a conversation, `air acp` closes its worker (the agent gets SIGHUP and saves its
transcript) and then detaches the workspace as step 7 does, holding a lease while it decides so it cannot race an
attached stand-in. The worker's directory stays, for a later resume.

A `_session/unarchive` closes nothing: the resume that follows finds a Saved conversation in step 3. It only keeps
a close still to come from ending the resumed session: a queued close of that conversation is dropped, and one
under way skips the close and detaches the workspace as a client does.

A worker that is already Absent has nothing to close, and its workspace is left as the last detach decided.

### Data between the client and the agent

```
LOCAL                                                              SANDBOX

Air ──ACP──► air acp ──ACP──► claude-agent-acp
              └ archive → close
                                 │ spawns `claude` (the stand-in)
                                 ▼
 request    stdin   ─► agent maps the line ─► IO relay  ════════►  agent stdin
 response   stdout  ◄─ agent maps the line ◄─ IO relay  ◄════════  agent stdout
 hooks      Air URL ◄─ filter (token, routes, paths) ◄─ Net relay ◄  POST to the sandbox port
 files      project ◄─────────────────────── FS relay  ════════►  project
 history    Claude config dir ◄───────────── FS relay  ◄════════  transcripts
```

Paths cross between the workstations in four places: the worker's arguments, the stream's lines, hook bodies and
the files themselves. The first three are mapped by the one path map the fourth defines.

## Packages

Dependencies point one way: `cli → control → {workspace, worker, claude, acp, air, git} → relay →
{shell, pathmap, env, secret}`. No package imports a sibling. `control` declares the agent interface and `claude`
implements it.

| Package     | Owns |
|-------------|------|
| `cli`       | The process's interface: commands, flags, the name of every variable Hermod reads, stdio, and the stand-in entry chosen by the program name. It builds one request per command and calls one use case. |
| `control`   | The use cases: session, one-shot call, close, ACP relay, registration. Each takes its own request. The decision table lives here, and the agent interface. |
| `workspace` | Which FS relays a project needs, their names and labels, the lease, the state table, and the path map. |
| `worker`    | The tmux session on the sandbox: its state, tags, directory, start and close, and the list of workers. |
| `relay`     | The three relay kinds behind one state set. Mechanism only: it opens, pauses and closes what it is told to. |
| `claude`    | Claude's contract as the agent interface: its arguments, read with the flag library; its state; its launch; its settings as typed documents; its stream. Pure functions; files are read by the caller. |
| `acp`       | The ACP wire format as typed messages, and a relay between two streams that shows each message to an observer. |
| `air`       | Air's own facts: the `acp.json` document as typed structures, and the names of the archive messages. |
| `pathmap`   | The map itself, and where a local directory lies on the sandbox. Applied to a string or a JSON value. |
| `env`       | Variables as key and value pairs. The one place that splits and joins `KEY=VALUE`; it prints keys only. |
| `secret`    | A token as an opaque type that prints as a mask. |
| `shell`     | `Command` and `Shell`: local, ssh, login shell, tmux. One command type, built once and passed down. |
| `git`       | The developer's identity. |

Scripts that run on the sandbox are files embedded in the package that owns them. Each takes its inputs as
arguments and environment; nothing is substituted into a script's text, so a value can never become shell syntax,
and `shell` does not have to express redirections or loops.

## Decisions

### The stand-in is `attach` under another name
One binary, one resolver, one flow. The alternatives each added a second way in: a hidden environment variable
telling `hermod` to act as Claude, a generated wrapper script, or a separate stand-in command with its own flow.
Naming the binary needs none of them, and a session typed by hand
(`hermod attach prod-box -- claude --resume <uuid>`) is the same session Air would open.

The cost is that the stand-in cannot take Hermod's flags, since all its arguments are Claude's. Its sandbox comes
from the environment, and so would any other option it comes to need.

### An agent declares, the entities act
Claude's arguments, settings, state and stream follow Claude's CLI and the SDK's protocol and change together when
those change. `claude` answers `control`'s questions with values (a command, files, relay requirements, a mapped
line) and performs nothing itself, so it needs no shell, no sandbox and no relay to be tested, and a new Claude
version is one package and its table-driven tests.

The same shape keeps the generic entities generic. A hook is, to `relay`, a sandbox port reaching a local URL
through a filter; that the filter checks a token and an allowed route is Claude's declaration, not the relay's
knowledge.

### Two sources say what state a session is in
Whether a conversation runs is the agent's to say, and whether a process exists under Hermod's name is tmux's.
Neither is inferred from the other or from files left behind, and what looks like a further state is a pair of
them: "running outside Hermod" is a Running agent with an Absent worker, and a stale session is a worker whose
agent is not Running. The worker's directory holds only what the running process needs and decides nothing.

### A paused relay holds
A worker outlives its clients, so a relay must say what happens to traffic that arrives with nobody attached. The
contract is that it is held and delivered at the next attach: files wait on disk, a terminal waits in tmux, and a
line stream waits in its log. An agent that asks its user a question therefore waits for the next client instead
of losing the question.

For a line stream, delivery is the agent's to define, because a new client begins the protocol again: Hermod
answers the SDK's `initialize` from the first response in the log, delivers the control requests still waiting for
an answer, and sends again the prompts the log has not echoed.

The Net relay is the exception in this change. Holding a call needs a listener on the sandbox that outlives the
client, and nothing of Hermod's runs there. When that is added (a local tool or MCP server the sandbox's agent
calls, queued until its user returns), it changes the Net relay alone, and a held call still lasts only as long as
its caller waits.

### One identity: names and tags
Everything a session owns is findable from its key or from the `hermod` prefix, with the tool that owns it.

| Thing | Name | Tags | Read with |
|---|---|---|---|
| Worker (sandbox) | tmux session `hermod-<key>`; a plain `attach` keeps its name | options `@hermod-dir`, `@hermod-origin` | `tmux ls -F '#{session_name} #{@hermod-dir} #{@hermod-origin}'` |
| Worker's directory (sandbox) | `~/.hermod/s/<key>/` | — | — |
| Project FS relay (local) | `hermod-<sandbox>-<slug>` | labels `hermod-kind=project`, `hermod-sandbox=<sandbox>` | `mutagen sync list --label-selector hermod-sandbox=<sandbox>` |
| Transcript FS relay (local) | `hermod-<sandbox>-<slug>-transcripts-<local>`, `<local>` a hash of its local end | labels `hermod-kind=transcripts`, `hermod-sandbox=<sandbox>` | same |
| Log (local) | `~/.cache/hermod/air/<uuid>.log` | — | — |

- **`@hermod-dir`** is the worker's project directory. The workspace's worker count is read from it, so no file on
  the sandbox has to record which workspace a worker belongs to.
- **Origin** is the machine the session was started from: its hostname, or `HERMOD_ORIGIN` when set, reduced to
  letters, digits, dots and dashes. An empty `HERMOD_ORIGIN` leaves the tag out, for a shared sandbox where the
  machine's name should not show.
- **The agent's own session name is not Hermod's to set.** It is what the user sees in Air and in Claude's own
  lists, so Hermod passes no `--name`. A Claude that Hermod runs is recognized by its tmux session.
- FS relays belong to a workspace, not to one worker, so they carry the sandbox and the kind but no key. A
  transcript relay also carries its local end: one sandbox directory's transcripts go to `~/.claude` for a
  terminal or a hand-run session and to an entry's `CLAUDE_CONFIG_DIR` for its chats, each by a relay of its own.
- `workspace` chooses these names and labels; `relay` is given them.

### The sandbox never holds Air's secrets
Air's hook token changes with every IDE run and is local by design. The sandbox's `settings.json` carries a
per-session token generated by Hermod; the Net relay's filter accepts only that token and only the routes the
settings named, and substitutes Air's current token. A running Claude's settings therefore stay valid across IDE
restarts, and Air's token never leaves the local machine. The session token is needed because a loopback port on a
shared sandbox is reachable by every user there. Secrets reach the sandbox only inside the worker's `0700`
directory, over ssh stdin, never on a command line.

### Only declared variables reach the sandbox
`cli` reads the process environment once. Of it, the worker is given the variables the agent declared it needs and
nothing else, so a local credential or setting never travels by default. `CLAUDE_CONFIG_DIR` is a local path and
is not among them.

### Each sandbox's chats are visible only through its own agent
Air lists chat-mode sessions through the adapter (`session/list`), which reads transcripts from Claude's config
directory. Each entry `configure` writes sets `CLAUDE_CONFIG_DIR` to a directory of that sandbox's own, and the
transcript relay brings that sandbox's transcripts there. "Claude (A)" then lists only A's chats and "Claude (B)"
only B's, so a chat can only be reopened on the sandbox it lives on, and Hermod needs no record of which sandbox
holds which chat. The user's local sessions in `~/.claude` stay out of both lists.

Two cases stay unseparated. Hermod's documentation advises against several sandboxes for one project in them; no
code guards against it:

- **Air's terminal mode.** Air reads `~/.claude` itself there, so every sandbox's chats share one list, and a chat
  reopened while another sandbox is configured forks onto it.
- **Claude started by hand.** `hermod attach B -- claude --resume <uuid of a chat on A>` forks the chat onto B.

### Relay names carry the sandbox
Today a mirror is named after the project's sandbox path alone (`dev-api`), so attaching to B in a project already
mirrored to A finds `dev-api` and resumes the mirror to A: the session runs on B while the files go to A. An FS
relay is named `hermod-<sandbox>-<slug>` instead (the alias reduced to the letters, digits and dashes Mutagen
accepts), and opening one checks its sandbox end before resuming. Mirrors made under the old names are no longer
found; the first run lists them and leaves terminating them to the user.

### The project relay never picks a winner
The developer has to see what the sandbox changed in the project's files, and to bring local changes to the
sandbox. Both need a relay that carries edits each way and discards neither side's: the project relay is
`two-way-safe`. A file changed on both sides is left as it is on each and reported by Mutagen as a conflict until
the developer settles it.

- **Alternative — local wins (`two-way-resolved`):** rejected. A local edit silently overwrites the agent's, so
  what the sandbox changed can no longer be seen.
- **Alternative — one-way:** rejected. One side's changes never reach the other.

### Concurrent sandboxes are allowed; conflicts are the developer's
One project can be attached to several sandboxes at once: each has its own workspace, workers and ports. The local
directory then relays between them, so one agent's edits reach the other sandboxes too. Hermod allows this and
does not manage it. It neither orders nor resolves the edits; what Mutagen reports as a conflict is the
developer's to settle, and so is excluding per-machine build output.

### Mutagen is the workspace registry
Which local roots are present on a sandbox, and where, is state Mutagen already holds: an FS relay's ends are the
root pair. Hermod labels its relays and reads the roots from `mutagen sync list`, keeping no file of its own that
could drift. Paths stay in the ends because a label value cannot hold one.

### Leases are file locks
Each process attached to a workspace holds a shared `flock` on `$XDG_RUNTIME_DIR/hermod/lease/<workspace>`. On
detach it tries a non-blocking exclusive lock; the process that gets it is the last client and applies the
workspace table. The kernel releases a crashed process's locks, so a killed stand-in never leaves a workspace
counted as attached. The lease is `workspace`'s own detail and no other package sees it.

- **Alternative — a local daemon owning workspaces and relays:** rejected. `hermod air acp` lives as long as the
  IDE run and could host one, but terminal mode and `attach` have no such process.

### The notification back-channel is removed
`hermod notify`, the `-N` flag, the `notify` package, `sandbox.Channel` and `HERMOD_NOTIFY_SOCK` go. The channel
does not fit what this design makes of Hermod:

- **It wants Hermod on the sandbox.** Its sender is `hermod notify` run there, which is what the
  `add-remote-agent` proposal exists to install. Nothing of Hermod's runs on the sandbox now.
- **It is one socket per sandbox user**, owned by the most recent attach. With several sessions attached at once,
  all but one lose it.
- **Air does not need it.** Claude's hooks reach Air through the Net relay, and Air shows "needs input" and "done"
  itself.

What is lost is the desktop toast for a session attached from a plain terminal. If that is wanted again, the Net
relay is the transport to build it on: Claude's own `Stop` and `Notification` hooks, received locally.

The `add-notify-backchannel` and `add-remote-agent` changes are withdrawn with it.

## Risks / Trade-offs

- **Catching up on a running line stream is unproven.** Whether the SDK accepts a locally answered `initialize`
  is not known. → Until it is shown to work, an attach to a Running line-stream worker closes it and starts with
  resume, losing only a turn in flight.
- **A held hook call does not outlast Claude's hook timeout**, and how long Claude waits for a tool or MCP call is
  not known. → Measure before building the Net relay's Paused state.
- **A workspace in Background shows nothing locally** until the next attach. → Accepted: a client that wants the
  files attaches.
- **Claude's `--mcp-config` may name a local file or a local program**, which the sandbox does not have. → Verify
  what Air passes; if a start fails on it, the agent's launch drops what the sandbox cannot run.
- **The adapter is assumed to list sessions from `CLAUDE_CONFIG_DIR`**, as Claude Code does. → Verify with
  `air-probe` and two entries. If it does not, every agent lists every chat, and a chat reopened through the wrong
  agent forks.
- **The ACP session ID is assumed to be Claude's session UUID**, which close takes it for. → Verify in `acp-tap`
  logs. If it is not, "mark done" closes nothing.
- **"Mark done" writes into a transcript the one-way relay owns**, which may stall that relay. → Test with
  `air-probe`; if it stalls, close resets the file.
- **A sandbox port picked free can be taken before `ssh -R` binds it.** → That attach runs without its Net relay,
  logged; the next attach tries the same port again.
- **A hand-edited entry can give `air acp` a `SANDBOX` argument that differs from its `HERMOD_SANDBOX`.** "Mark
  done" then looks for the worker on the wrong sandbox and closes nothing. → `configure` never writes such an
  entry.

## Delivery

Each step is usable on its own and builds only on the ones before it.

1. `env`, the single `shell.Command`, and `pathmap`.
2. The FS relay and `workspace`: names that carry the sandbox, labels, the lease and the state table.
3. `worker` and the session flow for a command with no agent: `hermod attach` with `HERMOD_SANDBOX` and
   `HERMOD_SANDBOX_CMD`; the notification back-channel removed.
4. The agent interface and `claude`: arguments, state, launch; one-shot calls; terminal sessions; the stand-in
   link in `make install`.
5. The Net relay and its filter; the transcript relay.
6. The line-stream IO relay, holding and catching up included; `hermod air configure`.
7. `acp`, `hermod air acp` and close on "mark done".

## Open Questions

- **Terminal mode has not been observed.** Switching Air to it still opened the chat UI. That Air starts the
  configured path under its own name, as the SDK does, is assumed.
- **`HERMOD_SANDBOX` in terminal mode.** It has to be in Air's own environment (e.g. `~/.config/environment.d/`).
  Whether Air's terminal-mode setting can carry environment of its own is not known.
- **Whether Air's environment already has the adapter on `PATH`.** If it does, the entry needs no `PATH` of its
  own.
- **Subscription use.** Air ships the adapter with `--hide-claude-auth`; whether running it without that flag is
  within Anthropic's terms has to be settled before a release.
- The questions in [`docs/air-integration.md`](../../../docs/air-integration.md#open-questions) still stand.
