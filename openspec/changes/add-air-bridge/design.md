## Context

[`docs/air-integration.md`](../../../docs/air-integration.md) records how JetBrains Air runs Claude Code, what a
bridge must do to move that agent to another machine, and the decisions already taken: Air is a terminal to
sessions that live on that machine, each session is a tmux session that only the user ends, nothing of Hermod's
runs there, hooks get a port per session, and MCP servers run there only. This design does not restate those
decisions. It names the entities Hermod is built from, gives each its states, and describes how every command uses
them.

Hermod connects a local workstation to a **remote** one. Sandboxing an agent, which that document describes, is
one use of a remote; nothing in Hermod depends on what the remote is for.

Hermod today runs one flow for one interactive terminal, started as `hermod <sandbox>`. The Air bridge adds a
second kind of caller: a program (the Agent SDK, or Air's terminal tab) that starts Hermod as its `claude`, owns
Hermod's stdio, and expects Claude's own CLI contract on it.

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
- Rewriting MCP configuration. Claude's `--mcp-config` arguments are carried like any other argument; a server
  that runs a local program is not bridged.
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

Air, and the SDK under it, start Claude as an executable path followed by Claude's arguments; no setting lets
anything be put in front. So Hermod is given the name: a symlink called `claude` points to `hermod`, and a `hermod`
started as `claude ARGS…` runs exactly what `hermod attach -- claude ARGS…` runs. Every other name
runs the normal command line.

- The stand-in has no `REMOTE` argument, so its remote is `HERMOD_REMOTE`.
- Its command is given, so `HERMOD_REMOTE_CMD` does not apply.
- Stdout belongs to Claude's caller: all of Hermod's own output, the dry-run plan included, goes to stderr and to
  a per-session log (`~/.cache/hermod/air/<uuid>.log`, mode `0600`).
- `make install` creates the link, and where is the installer's choice: outside `PATH`, beside a local `claude`,
  or on `PATH` as the machine's only `claude`. Hermod itself never writes it.
- Hermod finds the link as the `claude` on the user's login-shell `PATH` when that is a link to `hermod`, else at
  `make install`'s default place.

### `hermod air configure claude [REMOTE]`

Registers a remote with Air by merging one entry into `~/.jetbrains/acp.json`, keeping every other entry and key:

```json
{
  "agent_servers": {
    "Claude (prod-box)": {
      "command": "/home/<you>/go/bin/hermod",
      "args": ["air", "acp", "claude"],
      "env": {
        "HERMOD_REMOTE": "prod-box",
        "CLAUDE_CONFIG_DIR": "/home/<you>/.local/share/hermod/air/prod-box/claude"
      }
    }
  }
}
```

- The entry holds no path: `air acp` finds the adapter and the stand-in when it runs. `configure` fails when
  [the stand-in's link](#the-stand-in-hermod-named-claude) is not found, since no chat could start.
- The remote goes into `env`, not `args`: the per-chat stand-ins inherit the entry's environment but never see
  `air acp`'s arguments, and both must resolve the same remote.
- `--name` sets the entry's name, `Claude (<remote>)` by default. Configuring the same name again replaces the
  entry. One entry per remote makes each remote its own agent in Air's picker.
- `--origin=NAME` writes `HERMOD_ORIGIN` into the entry's `env`; `--origin=` writes it empty, which leaves the
  machine unnamed. With neither the flag nor the variable, the entry has no `HERMOD_ORIGIN` and each chat uses the
  hostname.
- `-n` prints the entry and changes nothing.

That entry is all chat mode needs. For terminal mode, which `acp.json` plays no part in, `configure` also prints
the link's path, to be entered as Air's Claude executable, and the `HERMOD_REMOTE=<remote>` line that Air's own
environment needs.

### `hermod air acp claude [REMOTE]`

What the entry runs. It starts the ACP adapter and relays ACP messages both ways verbatim. Air does not load the
user's shell, so `air acp` starts the adapter through the user's login shell, as commands on the remote are
started: `claude-agent-acp`, and through it `node`, are found on the `PATH` the user's own terminal has. It gives
the adapter `CLAUDE_CODE_EXECUTABLE`, set to [the stand-in's link](#the-stand-in-hermod-named-claude), so every
`claude` the adapter spawns is Hermod. The adapter passes its environment on to each of them, which is how each
chat's stand-in gets `HERMOD_REMOTE`.

It acts on one message: `_session/archive` ("mark done") [closes](#close) that chat's worker. The adapter is the
only process that sees it (see [Session lifecycle](../../../docs/air-integration.md#session-lifecycle)).

The adapter stays a separate program. It translates ACP into the Agent SDK's stream protocol and carries Air's
own extensions, negotiated when Air declares itself at `initialize`; Hermod relays that exchange and takes no part
in it.

## Entities

```
LOCAL workstation                                                 REMOTE workstation

  client ──► session ──► workspace ─┬─ FS relay   project dir   ⇄  project dir
(you, Air's terminal,               ├─ FS relay   transcripts   ⇄  transcripts
 the ACP adapter)                   ├─ Net relay  local address ◄  a remote port
                                    │
                                    ├─ worker ─── IO relay ─────⇄  a process
                                    └─ worker ─── IO relay ─────⇄  a process
```

Hermod keeps state for none of these; each is read from its source when asked.

| Entity      | The one fact it owns | Identified by | Lives until |
|-------------|----------------------|---------------|-------------|
| Workstation | A machine commands run on: the local one, or the remote. Hermod interacts with stations via `shell.Shell` that runs `shell.Command` | its host | — |
| Session     | One Hermod process on the local workstation, attached to one workspace and one of its workers. It is stored nowhere | the process | the process ends; the worker keeps running and its relays are disconnected |
| Workspace   | The relays and the workers of one project on one remote | the two ends of its project FS relay | it has no worker and no session |
| Relay       | One channel between the two workstations, and whether it is connected | its kind and its two ends | no lease holds it and its workspace has no worker |
| Worker      | One persistent process on the remote | a key: the agent's session ID, or a name | the remote process is gone |
| Agent       | The agent program running in a worker, and that program's contract | the agent's own session ID | the client ends the conversation, or the program exits |

A workspace holds any number of workers, none included: it exists from its first session and is gone with its last
worker, and nothing sets one up beforehand. A worker belongs to one workspace and keeps running with no session
attached. An agent is one kind of worker's process; whatever a detached worker changes or writes, an agent or not,
reaches the local workstation at the next attach.

### Relay

| State        | Meaning |
|--------------|---------|
| Absent       | Does not exist. |
| Disconnected | Exists and accepts traffic at the remote end, but delivers nothing. What arrives is held. |
| Connected    | Delivers in both directions. Entering this state first drains what was held. |

| Kind | Carries | Mechanism (implementation) | What Disconnected holds, and where |
|------|---------|----------------------------|------------------------------------|
| FS   | files between two directories | a Mutagen session | file changes, on each side's disk |
| IO   | the worker's stdin, stdout and stderr | tmux for a terminal; a FIFO and logs for a line stream | output and the agent's questions: in tmux's screen, or in the logs |
| Net  | calls from a remote port to a local address, optionally through a filter | `ssh -R` to a local listener | nothing: see below |

- A relay is **leased** ([Leases are file locks](#leases-are-file-locks)): each session that connects it holds a
  lease, and it stays Connected while any lease is held.
- One rule decides what becomes of a relay when its last lease is released: it is Disconnected while its
  workspace has a live worker, and Absent otherwise. An FS relay is flushed before it becomes Absent. When the
  workers cannot be read, the relay is left Disconnected, which never removes something in use.
- An IO relay's remote end is one worker, so it exists exactly as long as that worker.
- A Net relay has no Disconnected state in this change. Nothing listens on the remote port once the client is
  gone, so it goes from Connected to Absent and a call made meanwhile fails at once.
- Reading a relay's state can fail. That is a failure value, not a fourth state.

### Workspace

A workspace is the relays and the workers of one project on one remote. It adds relays and workers, connects and
releases its relays together, applies the release rule from its own worker list, and reports the state of
everything in it. It carries nothing itself: a project's files are on both workstations because an FS relay joins
the two directories, not because a workspace exists.

| State    | Meaning |
|----------|---------|
| Absent   | It has no relay and no worker. |
| Detached | No session holds it: its workers run on, its relays are Disconnected, and what the remote does meanwhile is held. |
| Attached | A session holds it: the relays that session leases are Connected. |

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

| State   | Meaning |
|---------|---------|
| Absent  | No tmux session under the key. |
| Running | The process is alive. |
| Exited  | The process failed; its pane is kept to show the error and exit status. |

A worker is the tmux session `hermod-<key>` on the remote, tagged with its workspace and its origin
([Names and tags](#one-identity-names-and-tags)). Start takes Absent to Running; a clean exit or a close leads to
Absent; a failed exit leads to Exited; removing an Exited worker leads to Absent. tmux is the only source.

The worker provides the remote end of its IO relay in one of two forms, fixed when it starts:

- **terminal:** the client joins the tmux session on a tty.
- **line stream:** the process reads a FIFO and appends its stdout and stderr to logs; the client's streams are
  copied to and from them one complete line at a time.

### Agent

| State   | Meaning |
|---------|---------|
| Running | The agent program lists the conversation as running. |
| Stopped | It does not. |

The agent program is the only source: for Claude, `claude agents --json`. A detach changes nothing here: an agent
whose client has gone is Running, and what it waits for is held in a relay. Whether a stopped conversation can be
resumed is not a state Hermod keeps. The caller's arguments say whether to resume, the transcript relay carries
what a resume needs, and the agent program itself refuses when there is nothing to resume.

`control` reaches an agent through one interface and never names Claude:

| Control asks | The agent answers |
|--------------|-------------------|
| What do these arguments ask for? | The conversation's key and the IO form; or that this is a one-shot call with no conversation. |
| Is this conversation running? | The command that asks the agent program, and how to read its output into a state. |
| What does a worker for it need? | The command to run with paths mapped, the files it must find, the local variables it must be given, and the relays it requires. |
| How is a line of its stream mapped? | The line with its paths mapped, in either direction. |
| How does a new client catch up? | From the held output, what to deliver, what to answer on the agent's behalf, and what to send again. |

The agent is handed the path map and never learns where the other workstation is or how it is reached.

## Flows

### Session

Every session, whoever starts it, runs these steps. The rows of the table after them are the only differences.

1. **Set up the workspace.** Find [the workspace the directory belongs to](#workspace), or make it. Lease the FS
   relay that covers the directory and connect it, flushed. The path map is now fixed for the run.
2. **Read the states.** One call on the remote reads the worker's state and, for an agent, whether its
   conversation runs.
3. **Decide.**

   | Agent   | Worker            | Action |
   |---------|-------------------|--------|
   | Running | Running           | connect |
   | Running | Absent            | refuse: it runs outside Hermod, and a start would be a second copy |
   | Stopped | Running or Exited | remove the worker, then decide as Absent |
   | Stopped | Absent            | start, with the caller's arguments as given |

   A command with no agent has only the worker column: Absent starts it, Running connects.
4. **Start**, when the table says so. The workspace first adds, connects and flushes the FS relays the agent
   requires,
   so its transcripts are on the remote before it reads them. Then create the worker's directory
   `~/.hermod/s/<key>/` (mode `0700`) with what the agent's answer listed, written over ssh stdin, and start the
   worker in the workspace, in the directory's remote path.
5. **Attach the workspace.** The workspace adds the session's remaining relays, the IO relay to the worker among
   them, and connects them on one ssh connection that refuses a shared master. The client is now connected to the
   worker.
6. **Detach the workspace.** Stdin EOF, SIGTERM or a dropped connection releases the session's leases and nothing
   else; the worker keeps running, and the workspace applies [the release rule](#relay) to each relay released.
   Hermod exits inside the SDK's 2 s + 5 s window, so it is never left orphaned.

| Started as | Command | Agent | IO form | Net relays |
|------------|---------|-------|---------|------------|
| `hermod attach` | the user's | none | terminal | the user's `-R`, if any |
| `claude ARGS…` on a tty (Air's terminal tab, or by hand) | Claude's, mapped | Claude | terminal | Air's hooks, when the settings carry them |
| `claude ARGS…` under the adapter (an Air chat) | Claude's, mapped | Claude | line stream | Air's hooks |

For Claude, the worker's directory holds `env` (the selected `CLAUDE_*` and `ANTHROPIC_*` variables and the git
identity), `settings.json` (the caller's hook settings aimed at the remote port, with the session token in place
of Air's), the port and the token; a line-stream worker adds the FIFO, the logs and the process ID a close waits
on. Every setup command runs with stdin from `/dev/null`; Hermod's stdin is left for the agent.

### One-shot call

Arguments that name no conversation (a subcommand such as `auth`, `--version`, `--help`, `-p` without a session)
are not a session. Hermod runs the command on the remote with the caller's stdio and exits with its code: no
workspace, no worker, no relay. It runs in the project's remote directory when that exists, else in the remote
user's home. This covers the adapter's `claude auth status` and Air's `-p` settings read, which then report the
remote's login and settings.

### Close

On `_session/archive` for a conversation, `air acp` closes its worker (the agent gets SIGHUP and saves its
transcript), which ends the conversation's agent. Its workspace then applies the release rule to its relays,
holding a lease while it decides so it cannot race an attached stand-in: with another worker left they stay, and
with none they go. The worker's directory stays, for a later resume.

A `_session/unarchive` closes nothing: the resume that follows finds a Stopped agent in step 3. It only keeps a
close still to come from ending the resumed conversation: a queued close of that conversation is dropped, and one
under way skips the close and releases the relays as a detach does.

A worker that is already Absent has nothing to close, and its relays are left as the last detach decided.

### Data between the client and the agent

```
LOCAL                                                              REMOTE

Air ──ACP──► air acp ──ACP──► claude-agent-acp
              └ archive → close
                                 │ spawns `claude` (the stand-in)
                                 ▼
 request    stdin   ─► agent maps the line ─► IO relay  ════════►  agent stdin
 response   stdout  ◄─ agent maps the line ◄─ IO relay  ◄════════  agent stdout
 errors     stderr  ◄──────────────────────── IO relay  ◄════════  agent stderr
 hooks      Air URL ◄─ filter (token, routes, paths) ◄─ Net relay ◄  POST to the remote port
 files      project ◄─────────────────────── FS relay  ════════►  project
 history    Claude config dir ◄───────────── FS relay  ════════►  transcripts
```

Paths cross between the workstations in four places: the worker's arguments, the stream's lines, hook bodies and
the files themselves. The first three are mapped by the one path map the fourth defines.

## Packages

Dependencies point one way: `cli → control → {workspace, claude, acp, air, git} → {relay, worker} →
{shell, pathmap, env, secret}`. No package imports a sibling. `control` declares the agent interface and `claude`
implements it.

| Package     | Owns |
|-------------|------|
| `cli`       | The process's interface: commands, flags, the name of every variable Hermod reads, stdio, and the stand-in entry chosen by the program name. It builds one request per command and calls one use case. |
| `control`   | The use cases: session, one-shot call, close, ACP relay, registration. Each takes its own request. The decision table lives here, and the agent interface. |
| `workspace` | One project's relays and workers on one remote: which workspace a directory belongs to, finding or making each relay, the names and labels, the leases, the release rule, the state of the whole, and the path map read from its FS relays. |
| `worker`    | The tmux session on the remote: its state, tags, directory, start and close, its stdio as an IO relay can reach it, and the list of workers under a tag. Mechanism only: it knows nothing of workspaces or relays. |
| `relay`     | The three relay kinds behind one state set. Mechanism only: it connects, disconnects and removes what it is told to. |
| `claude`    | Claude's contract as the agent interface: its arguments, read with the flag library; its running state; its launch; its settings as typed documents; its stream. Pure functions; files are read by the caller. |
| `acp`       | The ACP wire format as typed messages, and a relay between two streams that shows each message to an observer. |
| `air`       | Air's own facts: the `acp.json` document as typed structures, and the names of the archive messages. |
| `pathmap`   | The map itself, and where a local directory lies on the remote. Applied to a string or a JSON value. |
| `env`       | Variables as key and value pairs. The one place that splits and joins `KEY=VALUE`; it prints keys only. |
| `secret`    | A token as an opaque type that prints as a mask. |
| `shell`     | `Command` and `Shell`: local, ssh, login shell, tmux. One command type, built once and passed down. |
| `git`       | The developer's identity, and a linked worktree's main working tree. |

Scripts that run on the remote are files embedded in the package that owns them. Each takes its inputs as
arguments and environment; nothing is substituted into a script's text, so a value can never become shell syntax,
and `shell` does not have to express redirections or loops.

## Decisions

### The stand-in is `attach` under another name
One binary, one resolver, one flow. The alternatives each added a second way in: a hidden environment variable
telling `hermod` to act as Claude, a generated wrapper script, or a separate stand-in command with its own flow.
Naming the binary needs none of them, and a session typed by hand
(`hermod attach prod-box -- claude --resume <uuid>`) is the same session Air would open.

The cost is that the stand-in cannot take Hermod's flags, since all its arguments are Claude's. Its remote comes
from the environment, and so would any other option it comes to need.

### An agent declares, the entities act
Claude's arguments, settings, state and stream follow Claude's CLI and the SDK's protocol and change together when
those change. `claude` answers `control`'s questions with values (a command, files, relay requirements, a mapped
line) and performs nothing itself, so it needs no shell, no remote and no relay to be tested, and a new Claude
version is one package and its table-driven tests.

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

For a line stream, delivery is the agent's to define, because a new client begins the protocol again: Hermod
answers the SDK's `initialize` from the first response in the log, delivers the control requests still waiting for
an answer, and sends again the prompts the log has not echoed.

The Net relay is the exception in this change. Holding a call needs a listener on the remote that outlives the
client, and nothing of Hermod's runs there. When that is added (a local tool or MCP server the remote's agent
calls, queued until its user returns), it changes the Net relay alone, and a held call still lasts only as long as
its caller waits.

### Transcripts travel both ways
An agent's transcripts are files, so carrying them is an FS relay's work and no step of the flow copies one. The
transcript relay is an FS relay like any other, carrying both ways: a conversation that exists only locally is on the
remote before the agent resumes it, the remote's conversations appear locally for Air to list, and what Air writes
into a local transcript reaches the remote.

The cost is that every conversation of the project in that local Claude config directory reaches the remote,
those started locally included.

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

### One identity: names and tags
Everything a session uses is findable from its key or from the `hermod` prefix, with the tool that owns it.

| Thing | Name | Tags | Read with |
|---|---|---|---|
| Workspace | the name of its project FS relay | — | the two rows below |
| Worker (remote) | tmux session `hermod-<key>`; a plain `attach` keeps its name | options `@hermod-workspace`, `@hermod-origin` | `tmux ls -F '#{session_name} #{@hermod-workspace} #{@hermod-origin}'` |
| Worker's directory (remote) | `~/.hermod/s/<key>/` | — | — |
| FS relay (local) | `hermod-<remote>-<slug>-<local>`: `<slug>` from its remote end, `<local>` a hash of its local end | labels `hermod-remote=<remote>`, `hermod-workspace=<workspace>` | `mutagen sync list --label-selector hermod-workspace=<workspace>` |
| Log (local) | `~/.cache/hermod/air/<uuid>.log` | — | — |

- **`@hermod-workspace`** is the workspace the worker belongs to. Whether a workspace's relays are still needed
  is read from it, so no file on either workstation has to record it. `hermod-remote` lists every workspace on a
  remote.
- **Origin** is the machine the session was started from: its hostname, or `HERMOD_ORIGIN` when set, reduced to
  letters, digits, dots and dashes. An empty `HERMOD_ORIGIN` leaves the tag out, for a shared remote where the
  machine's name should not show.
- **The agent's own session name is not Hermod's to set.** It is what the user sees in Air and in Claude's own
  lists, so Hermod passes no `--name`. A Claude that Hermod runs is recognized by its tmux session.
- An FS relay is named after its two ends and nothing else: not the worker, the agent, or what the files are. Any
  session that needs the same two ends finds the same relay and leases it. One remote directory joined to two
  local ones is two relays: a remote's transcripts go to `~/.claude` for a terminal or a hand-run session and to
  an entry's `CLAUDE_CONFIG_DIR` for its chats.
- `workspace` derives these names and the label; `relay` is given them.

### The remote never holds Air's secrets
Air's hook token changes with every IDE run and is local by design. The remote's `settings.json` carries a
per-session token generated by Hermod; the Net relay's filter accepts only that token and only the routes the
settings named, and substitutes Air's current token. A running Claude's settings therefore stay valid across IDE
restarts, and Air's token never leaves the local machine. The session token is needed because a loopback port on a
shared remote is reachable by every user there. Secrets reach the remote only inside the worker's `0700`
directory, over ssh stdin, never on a command line.

### Only declared variables reach the remote
`cli` reads the process environment once. Of it, the worker is given the variables the agent declared it needs and
nothing else, so a local credential or setting never travels by default. `CLAUDE_CONFIG_DIR` is a local path and
is not among them.

### Each remote's chats are visible only through its own agent
Air lists chat-mode sessions through the adapter (`session/list`), which reads transcripts from Claude's config
directory. Each entry `configure` writes sets `CLAUDE_CONFIG_DIR` to a directory of that remote's own, and the
transcript relay brings that remote's transcripts there. "Claude (A)" then lists only A's chats and "Claude (B)"
only B's, so a chat can only be reopened on the remote it lives on, and Hermod needs no record of which remote
holds which chat. The user's local sessions in `~/.claude` stay out of both lists.

Two cases stay unseparated. Hermod's documentation advises against several remotes for one project in them; no
code guards against it:

- **Air's terminal mode.** Air reads `~/.claude` itself there, so every remote's chats share one list, and a chat
  reopened while another remote is configured forks onto it.
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
One project can be attached to several remotes at once: each has its own relays, workers and ports. The local
directory then relays between them, so one agent's edits reach the other remotes too. Hermod allows this and does
not manage it. It neither orders nor resolves the edits; what Mutagen reports as a conflict is the developer's to
settle, and so is excluding per-machine build output.

### Mutagen is the FS relay registry
Which local roots are present on a remote, and where, is state Mutagen already holds: an FS relay's ends are the
root pair. Hermod labels its relays and reads the roots from `mutagen sync list`, keeping no file of its own that
could drift. Paths stay in the ends because a label value cannot hold one.

### Leases are file locks
Each session that connects a relay through its workspace holds a shared `flock` on `$XDG_RUNTIME_DIR/hermod/lease/<relay>`. On
release it tries a non-blocking exclusive lock; the process that gets it held the last lease and applies the
release rule. The kernel releases a crashed process's locks, so a killed stand-in never leaves a relay leased. The
lease is `workspace`'s own detail and no other package sees it.

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

- **Catching up on a running line stream is unproven.** Whether the SDK accepts a locally answered `initialize`
  is not known. → Until it is shown to work, a connect to a Running line-stream worker closes it and starts with
  resume, losing only a turn in flight.
- **A held hook call does not outlast Claude's hook timeout**, and how long Claude waits for a tool or MCP call is
  not known. → Measure before building the Net relay's Disconnected state.
- **A relay leased from another workspace follows that workspace's workers**, not the borrower's, so it could go
  Absent under a detached borrower. → No case in this change borrows across workspaces: an agent's transcript
  relay has ends of its own per project directory. Guard it when a case appears.
- **A linked worktree's pointer files hold paths.** → They resolve on both workstations only as
  [relative paths](../../../docs/air-integration.md#mirrors-cover-roots-worktrees-resolve-by-path).
- **A Detached workspace shows nothing locally** until the next attach. → Accepted: a client that wants the
  files attaches.
- **A transcript written on both sides at once is a conflict** that stops that file from travelling. "Mark done"
  writes a local transcript while its agent may still be saving the remote one. → Test with `air-probe`; if it
  conflicts, close waits for the agent's save before the relay is released.
- **Claude's `--mcp-config` may name a local file or a local program**, which the remote does not have. → Verify
  what Air passes; if a start fails on it, the agent's launch drops what the remote cannot run.
- **The adapter is assumed to list sessions from `CLAUDE_CONFIG_DIR`**, as Claude Code does. → Verify with
  `air-probe` and two entries. If it does not, every agent lists every chat, and a chat reopened through the wrong
  agent forks.
- **The ACP session ID is assumed to be Claude's session UUID**, which close takes it for. → Verify in `acp-tap`
  logs. If it is not, "mark done" closes nothing.
- **A remote port picked free can be taken before `ssh -R` binds it.** → That session runs without its Net relay,
  logged; the next one tries the same port again.
- **A hand-edited entry can give `air acp` a `REMOTE` argument that differs from its `HERMOD_REMOTE`.** "Mark
  done" then looks for the worker on the wrong remote and closes nothing. → `configure` never writes such an
  entry.

## Delivery

Each step is usable on its own and builds only on the ones before it.

1. `env`, the single `shell.Command`, and `pathmap`.
2. The FS relay and `workspace` with no workers yet: which workspace a directory belongs to, names that carry the
   remote, labels and leases.
3. `worker` inside the workspace, the release rule, the terminal IO relay and the session flow for a command with
   no agent: `hermod attach` with
   `HERMOD_REMOTE` and `HERMOD_REMOTE_CMD`; the notification back-channel removed.
4. The agent interface and `claude`: arguments, running state, launch; one-shot calls; terminal sessions; the
   transcript relay; the stand-in link in `make install`.
5. The Net relay: Claude's filtered hooks, and `attach -R`.
6. The line-stream IO relay, holding and catching up included; `hermod air configure`.
7. `acp`, `hermod air acp` and close on "mark done".

## Open Questions

- **Terminal mode has not been observed.** Switching Air to it still opened the chat UI. That Air starts the
  configured path under its own name, as the SDK does, is assumed.
- **`HERMOD_REMOTE` in terminal mode.** It has to be in Air's own environment (e.g. `~/.config/environment.d/`).
  Whether Air's terminal-mode setting can carry environment of its own is not known.
- **Should `hermod air acp` attach the workspace with no worker?** It runs for the whole IDE run, so it could keep
  transcripts arriving while no chat is open, and later hold one hooks relay for all of a workspace's chats.
  Whether Air starts it in the project's directory is not known.
- **Subscription use.** Air ships the adapter with `--hide-claude-auth`; whether running it without that flag is
  within Anthropic's terms has to be settled before a release.
- The questions in [`docs/air-integration.md`](../../../docs/air-integration.md#open-questions) still stand.
