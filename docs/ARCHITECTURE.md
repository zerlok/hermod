# Hermod — Architecture

This document describes **what Hermod is built from**: its components and their states, the packages, the types and
interfaces each package declares, and which package calls which in every flow.

Hermod is small on purpose. It composes Mutagen, ssh and tmux, and for JetBrains Air the ACP adapter, into one
command. It keeps no second copy of any state: each state has one store, which is read every time the state is
needed. [Storage](#storage) lists the stores, most of them the tools' own.

## The model

> A **session** attaches a local client to a **workspace** on a remote: the **relays** that carry files, stdio and
> calls between the two workstations, and the **workers** that keep running there when the client is gone.

```mermaid
flowchart LR
    subgraph L["LOCAL workstation"]
        client["client<br/>you, Air's terminal, Air's chat UI"]
        lfiles["directories"]
        lport["a port"]
        session
    end
    subgraph W["workspace: one project on one remote"]
        fs["FS relay"]
        net["Net relay"]
        io["IO relay"]
    end
    subgraph R["REMOTE workstation"]
        rfiles["directories"]
        rport["a port"]
        worker
    end
    client <-->|stdio| session
%%    session -->|leases and connects the relays of| W
    lfiles <-->|syncs changes| fs
    net -->|Forwards| lport
    rfiles <-->|syncs changes| fs
    worker <--> |Reads & Writes| rfiles
    worker --> |Sends net|rport
    rport -->|Forwards| net
    io <-->|stdio| worker
    session <-->|stdio| io
```

## Components

The components of the model, from the one that drives a command down to what everything runs on. Each section
describes its component in its own terms and in terms of the components below it, never of those above. Every
component has a limited responsibility, and one package represents it.

### Session

A session is one attachment of a client: a local Hermod process that holds the relays of one workspace Connected
and joins the client to one of that workspace's workers. It is a running function of the `session` package.
Nothing records it, and it has no states: while it exists it is attached.

- **It decides.** A session reads the worker's state and, when the worker runs an agent, the agent's, and chooses
  what to do from the pair: start, resume, join, or refuse.
- **It uses an agent as values.** It asks the agent what a conversation needs, hands the answer to the workspace
  as an ordinary worker to start, and runs the agent's state query through `shell` on the remote. Nothing below a
  session knows what a conversation is.
- **It leases what it uses.** A session takes a connect lease on every relay it uses, through the workspace, and
  those leases end with it.
- **Leaving ends only the session.** A session ends when its client leaves (a closed terminal, a closed chat tab,
  a lost connection), when the worker exits, or when the process dies. The worker runs on, and an agent it runs
  stays Running.
- **One per workspace in use.** `hermod attach` and the stand-in hold one session. `hermod air acp` holds one for
  every workspace that has a chat open, and keeps in memory which chat belongs to which.

### Workspace

A workspace is the relays and the workers of one project on one remote. Its identity is the remote and the
project's root directory, so it is neither created nor removed: a project that Hermod has never touched, or has
finished with, has an Empty workspace. The `workspace` package represents it, and is the only package that puts
relays and workers together.

- **One name.** It names its relays and tags its workers after itself, so everything of one project on one remote
  is found again by that name.
- **Relays are connected and released together.** Connecting takes a connect lease on each relay for the caller,
  and releasing gives them up.
- **A worker keeps its relays.** The workspace makes each of its workers the keeper of the relays that worker
  depends on: the IO relay to its stdio, and the workspace's FS relays. They stay while the worker does.
- **The path map.** The two ends of its FS relays, longest root first, are the map between local and remote
  paths.

| State    | Meaning                                                                                  |
|----------|------------------------------------------------------------------------------------------|
| Empty    | No relay and no worker.                                                                  |
| Detached | It has a worker, and no relay of its has a connect lease: they are Disconnected.         |
| Attached | A connect lease is held on its relays: they are Connected.                               |

A workspace's state is computed from its relays and workers every time and never kept. Which root a directory
belongs to, and how relays enter a workspace, are in the
[design](../openspec/changes/add-air-bridge/design.md#workspace).

```mermaid
stateDiagram-v2
    [*] --> Empty
    Empty --> Attached: its relays are connected for the first time
    Attached --> Detached: last connect lease ends, a worker remains
    Detached --> Attached: its relays are connected
    Attached --> Empty: last connect lease ends, no worker
    Detached --> Empty: last worker is gone
```

### Relay

A relay is one channel between the two workstations, identified by its kind and its two ends. The `relay`
package represents it, with its leases: three kinds behind one set of states.

| Kind | Carries                                                                  | Mechanism                                                        | What Disconnected buffers, and where            |
|------|--------------------------------------------------------------------------|------------------------------------------------------------------|-------------------------------------------------|
| FS   | files between two directories                                            | a Mutagen session                                                | file changes, on each side's disk               |
| IO   | the stdin, stdout and stderr of one process on the remote                | copies between local streams and a remote stdio it is handed     | what the process writes, at the remote stdio    |
| Net  | calls from a remote port to a local address, optionally through a filter | `ssh -R` to a local listener                                     | nothing: a Net relay goes from Connected to Absent |

Each relay has one of the following states, and its leases decide which:

| State        | Leases on the relay        | Meaning                                                                                    |
|--------------|----------------------------|--------------------------------------------------------------------------------------------|
| Absent       | none                       | No relay: nothing is exchanged and nothing is buffered.                                    |
| Disconnected | keep leases only           | The relay exists and delivers nothing. Each end accepts what arrives there and buffers it. |
| Connected    | at least one connect lease | Both ends exchange continuously. The pace is the mechanism's and the network's.            |

```mermaid
stateDiagram-v2
    [*] --> Absent
    Absent --> Connected: first connect lease (the relay is made)
    Connected --> Disconnected: last connect lease ends, a keep lease remains
    Disconnected --> Connected: a connect lease is taken (the buffers are delivered first)
    Connected --> Absent: last lease ends (an FS relay is flushed first)
    Disconnected --> Absent: last keep lease ends
```

#### Leases

A lease is a claim on a relay. A relay counts its leases and knows nothing else about who holds them or why.

| Lease   | Claims that the relay | Is                                                                                                       | Ends when                                                 |
|---------|-----------------------|----------------------------------------------------------------------------------------------------------|-----------------------------------------------------------|
| Connect | delivers              | a shared lock on the relay's lock file, `$XDG_RUNTIME_DIR/hermod/lease/<relay>`, held by a local process | the process unlocks it, or dies and the kernel unlocks it |
| Keep    | exists, and buffers   | a **keeper**: a value the relay is given that answers one question, whether it still stands              | the keeper answers no                                     |

- **No lease is left behind.** Neither kind is a record someone must remember to delete: a killed process holds
  no lock, and a keeper is asked each time.
- **The last one out settles.** A process that releases a connect lease tries to lock the file exclusively,
  without waiting. If it gets the lock, no other connect lease is left: the relay asks its keepers, then
  disconnects or removes itself.
- **Unknown means keep.** When a keeper cannot answer, the relay is left Disconnected, which never removes
  something in use.
- **A keep lease can end with nobody there.** The relay then stays Disconnected until it is next connected or
  released, which settles it.
- **A Net relay takes connect leases only.** Its local end is a listener inside the process that connected it,
  so nothing could buffer for it and it has no Disconnected state.

A lock was chosen over a local daemon that owns the relays because not every Hermod command has a process that
outlives it to be one.

### Worker

A worker is one process on the remote that outlives whoever started it: a tmux session running a command. It is
identified by a key its caller chooses, and carries the tags its caller gives it. The `worker` package represents
it and knows nothing about what the process is or who it is for.

| State   | Meaning                                                                 |
|---------|-------------------------------------------------------------------------|
| Absent  | No tmux session under the key.                                          |
| Running | The process is alive.                                                   |
| Exited  | The process failed; its pane is kept to show the error and exit status. |

```mermaid
stateDiagram-v2
    [*] --> Absent
    Absent --> Running: start
    Running --> Absent: clean exit, or close
    Running --> Exited: failed exit
    Exited --> Absent: remove
```

A worker offers its process's stdio in one of two forms, fixed when it starts. Each buffers what the process
writes while nobody reads:

| Form        | How the stdio is reached                                          | Buffered in        |
|-------------|-------------------------------------------------------------------|--------------------|
| Terminal    | joining the tmux session on a tty                                 | tmux's screen      |
| Line stream | writing lines to a FIFO and reading the process's appended logs   | the logs           |

### Agent

An agent is two things under one name: one conversation of an agent program, and the contract that program
offers. A conversation is identified by the program's own session ID. The `agent` package declares the contract
and `claude` implements it for Claude Code.

| State   | Meaning                                                                                                                  |
|---------|--------------------------------------------------------------------------------------------------------------------------|
| Running | The agent program lists the conversation as running: it works on the last message or waits for the next one.             |
| Stopped | The conversation was ended: it was asked to end, or the program exited. Its transcript stays and can be resumed.         |

```mermaid
stateDiagram-v2
    [*] --> Stopped
    Stopped --> Running: the agent program starts or resumes the conversation
    Running --> Stopped: it is asked to end, or the program exits
```

A conversation's state does not depend on whether anyone reads its output. A Running conversation with no reader
stays Running and waits.

The contract is values only, and an agent performs nothing. Asked about a conversation, it answers with:

- the command that runs the conversation, with its paths mapped to the remote;
- what that command requires: the files it must find, the variables it must be given, the directories that must
  be the same on both workstations, and the port its hooks call back on;
- the command that asks the agent program for the conversation's state, and how to read the answer.

An agent is handed a path map and never learns where the other workstation is or how it is reached.

### Workstation

A workstation is a machine with a shell: the **local** one, where the client and Hermod run, and the **remote**,
where the work runs. Hermod reaches workstations by running commands there using `shell`.

| Workstation | What is there                                                                                         |
|-------------|-------------------------------------------------------------------------------------------------------|
| Local       | the client, the Hermod process, the Mutagen daemon, the lock files, the listener of every Net relay   |
| Remote      | the project's directories, the tmux server, the agent program and its adapter, each process's logs    |

Hermod runs no process of its own on the remote: everything there is tmux, the agent program or a
[script](#remote-scripts) a process was started with.

## Storage

Every state has one store and is read from there every time it is needed. Hermod keeps no second copy, so there
is nothing to drift.

| State                                                   | Stored in                                                                     | Read with                                            | Read by                                                          |
|---------------------------------------------------------|-------------------------------------------------------------------------------|------------------------------------------------------|------------------------------------------------------------------|
| Which FS relays exist, their ends and whether they sync | Mutagen                                                                       | `mutagen sync list`, by label                        | **Relay**, asked by **Workspace**                                |
| Which workers exist, their state and workspace          | tmux on the remote                                                            | `tmux ls`, by tag                                    | **Worker**, asked by **Workspace**                               |
| Whether a conversation runs                             | the agent program                                                             | `claude agents --json`                               | **Session**, with the command and the reading **Agent** declares |
| A relay's connect leases                                | the kernel, as locks on the relay's lock file on the local workstation        | trying to take the lock exclusively, without waiting | **Relay**                                                        |
| A relay's keep leases                                   | wherever its keepers read: for a worker, tmux on the remote                   | asking each keeper                                   | **Relay**, answered by **Worker** through **Workspace**          |
| What a Disconnected IO relay buffers                    | tmux's screen, or the logs in the worker's directory on the remote            | joining the tmux session, or reading the logs        | **Relay**                                                        |

Two of these stores are files Hermod writes itself:

| File                  | Where                                   | Holds                                                                                        |
|-----------------------|-----------------------------------------|----------------------------------------------------------------------------------------------|
| A lock file per relay | local, `$XDG_RUNTIME_DIR/hermod/lease/` | Nothing: the state is the lock on it, which the kernel holds. The file itself may be left.   |
| A worker's directory  | remote                                  | A line stream's FIFO and logs, and the files the process was started with (its settings).    |

## Packages

| Package     | Represents                                           | Constraints                                                                                                                                                                                                      | Declares                                                                     |
|-------------|------------------------------------------------------|------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|------------------------------------------------------------------------------|
| `cli`       | the process's interface | The only package that reads the arguments, the environment and the process's streams, and the one place every variable Hermod reads is named. Builds everything a command needs from the other adapters and hands it into the core: the agent implementation, the shells, the developer's git facts, the ACP connection and Air's own messages. Decides nothing from a state. | the commands |
| `session`   | [Session](#session) | The only package that decides from states: what to do for a pair of agent and worker states, and where a chat's message goes. Reaches relays and workers only through `workspace`, and an agent only through `agent.Agent`. Declares the messages it routes by what they mean to it (open, close, end or reopen a chat, or pass through) and the channel they arrive on, and never a wire format or a client's own method names. Keeps nothing past the process. | `Attach`, `Chat`, `ExitStatus`, `Message`, `Kind`, `Channel` |
| `workspace` | [Workspace](#workspace) | The only package that imports both `relay` and `worker`, and the one that names and tags them. Its state is computed on every call. | `Workspace`, `State`, `Members` |
| `relay`     | [Relay](#relay), [Leases](#leases) | The only package that takes a lock. Knows a keeper only as an interface. FS runs the `mutagen` command; Net runs `ssh -R` and a listener in this process; IO copies between local streams and a remote stdio it is handed. | `Relay`, `State`, `FS`, `IO`, `Net`, `Ends`, `Filter`, `Lease`, `Keeper` |
| `worker`    | [Worker](#worker)                                    | Mechanism only: builds tmux commands and runs them through a `shell` on the remote. Does not know what its process is. Holds the [scripts](#remote-scripts) a worker starts with.                                 | `Worker`, `State`, `Spec`, `Form`, `Stdio`, `List`                           |
| `agent`     | [Agent](#agent), the contract                        | An interface and values. No behaviour, and no dependency but the shared packages.                                                                                                                                | `Agent`, `State`, `Call`, `Probe`, `Needs`                                   |
| `claude`    | [Agent](#agent), for Claude Code | Pure functions: runs no command and reads no file. The only package that knows Claude Code's arguments, files and ACP adapter program. | `Agent` (implements `agent.Agent`) |
| `acp`       | the ACP wire format | Reads and writes one message per line, and turns each into the `session.Message` it is routed by: the standard methods by itself, a client's own methods by the table it is given. Routes nothing itself. | `Conn` (implements `session.Channel`) |
| `air`       | JetBrains Air's own facts | The only package that knows the `acp.json` document, the stand-in link and Air's own ACP messages. Says what each of those messages means as a `session.Kind`. Writing `acp.json` keeps every entry and key it does not own. | `Configure`, `Config`, `Entry`, `Methods` |
| `git`       | the developer's git facts | Read-only `git` commands on the local workstation. Called by `cli`, which passes the results on as values. | `Identity`, `Read`, `MainWorktree` |
| `shell`     | [Workstation](#workstation)                          | The only package that starts a process. Each wrapper rewrites the command and hands it inward, and dry-run is chosen once at the innermost one.                                                                  | `Shell`, `Command`, `Result`; `Local`, `DryRun`, `SSH`, `LoginShell`, `Tmux` |
| `pathmap`   | the map between local and remote paths               | Pure. Applies to a string or a JSON value, longest root first.                                                                                                                                                   | `Map`, `Place`                                                               |
| `env`       | variables as pairs                                   | The one place that splits and joins `KEY=VALUE`.                                                                                                                                                                 | `Vars`                                                                       |
| `secret`    | a value that must not be printed                     | A token prints as a mask everywhere but where it is delivered.                                                                                                                                                   | `Token`                                                                      |

### Package dependencies

Diagram below shows direct package imports. An **Adapter** imports the **Core** and the **Core** imports no **Adapter**; inside the **Core**, a
package imports only its own layer or a lower one.  The **domain** layer holds the key components. The **application**
layer drives them for one command, and the **foundation** layer is what they are built on.


```mermaid
flowchart TD
    subgraph adapters["Adapters"]
        cli
        git
        acp
        air
        claude
    end
    subgraph core["Core"]
        subgraph app["Application"]
            session
        end
        subgraph domain["Domain"]
            workspace
            worker
            relay
            agent
        end
        subgraph foundation["Foundation"]
            shell
            env
            secret
            pathmap
        end
    end
    cli --> git
    cli --> acp
    cli --> session
    cli --> air
    cli --> claude
    cli --> shell
    git --> shell
    acp --> session
    acp --> pathmap
    air --> session
    session --> workspace
    session --> agent
    session --> shell
    claude --> agent
    claude --> pathmap
    claude --> env
    claude --> secret
    workspace --> relay
    workspace --> worker
    workspace --> pathmap
    relay --> shell
    worker --> shell
    worker --> env
    agent --> pathmap
    agent --> env
    agent --> secret
    shell --> env
    env --> secret
```

### Remote scripts

A worker starts through a shell script on the remote: one that keeps a failed process's pane, and one that gives
a line stream its FIFO and logs. Each script is a file embedded in the `worker` package and is sent to the
remote's `sh` on its standard input when it is run, so nothing of Hermod's is installed there. A script takes its
inputs as arguments and environment, and nothing is substituted into its text: it reads the same in the
repository as on the remote.

## Interfaces

An interface is declared where it has more than one implementation or where a package must not know who implements
it. Everything else is a concrete type, tested through a fake `Shell`.

```mermaid
classDiagram
    namespace shell {
        class Command
        class Execution
        class Executor {
            <<interface>>
            +Exec(Command) Execution
        }
        class dryRun
        class local
        class ssh
        class login
        class tmux
    }
    namespace relay {
        class Relay {
            <<interface>>
            +State() State
            +Connect()
            +Disconnect()
            +Remove()
        }
        class fsRelay
        class ioRelay
        class netRelay
    }
    class Filter {
        <<interface>>
        +Pass(call) call
    }
    
    namespace agent {
        class Agent {
            <<interface>>
            +Parse(args) Call
            +Probe(key) Probe
            +Worker(Call, Map) Needs
            +Adapter() Needs
            +HookSettings(document, port, Token) document
        }
    }
    
    namespace claude {
        class claudeAgent["Agent"]
    }
    
    Executor <|.. local
    Executor <|.. dryRun
    Executor <|.. ssh
    Executor <|.. login
    Executor <|.. tmux
    Relay <|.. fsRelay
    Relay <|.. ioRelay
    Relay <|.. netRelay
    netRelay --> Filter
    Agent <|.. claudeAgent
```

### `shell.Shell` — a workstation

One method: run one `Command` and return its `Result`. A `Command` is built once and carries the argv, the
directory, the variables, the streams and how to stop it.

| Implementation | What it is                                                             |
|----------------|------------------------------------------------------------------------|
| `Local`        | Runs the command on this machine.                                      |
| `DryRun`       | Prints the command as a line that can be pasted, and runs nothing.     |
| `SSH`          | Wraps an inner `Shell`: the command runs on a host.                    |
| `LoginShell`   | Wraps an inner `Shell`: the command runs under the user's login shell. |
| `Tmux`         | Wraps an inner `Shell`: the command runs in a tmux session.            |

The first two say **how** a command runs and the other three **where**. A caller nests the ones it needs, and the
choice between `Local` and `DryRun` is made once, at the bottom, so no code above it knows about dry-run. Reads
that shape the plan (states, git identity) always use `Local`.

### `relay.Relay` — one channel

| Method       | What it does                                                                            |
|--------------|-----------------------------------------------------------------------------------------|
| `State`      | Reads the relay's state from its mechanism. A read that fails is an error, not a state. |
| `Connect`    | Makes the relay if Absent, and takes it to Connected.                                   |
| `Disconnect` | Takes it to Disconnected.                                                               |
| `Remove`     | Takes it to Absent.                                                                     |

| Implementation | Constructed from                                        | Adds                                                                    |
|----------------|---------------------------------------------------------|-------------------------------------------------------------------------|
| `FS`           | `Ends` (a local root and a remote root), a name, labels | `Flush`; `ListFS`, which reads the relays under a label with their ends |
| `IO`           | a `worker.Stdio` value and the client's streams         | `Serve`, which copies between them until the client leaves              |
| `Net`          | a remote port, a local address, an optional `Filter`    | —                                                                       |

`relay` connects, disconnects and removes what it is told to. It holds no lease and decides nothing.

### `relay.Filter` — what a Net relay lets through

One method: given a call that arrived on the remote port, return the call to make locally, or refuse it. `relay`
declares it and knows no implementation. `control` builds the one for an agent's hooks from what the agent
declared; a user's `-R` relay has none.

### `agent.Agent` — an agent program's contract

`control` reaches an agent only through this interface, and `cli` chooses the implementation from the command
line. Every method is a question answered with a value; an agent performs nothing.

| Method         | Control asks                            | The agent answers with                                                                                                                          |
|----------------|-----------------------------------------|-------------------------------------------------------------------------------------------------------------------------------------------------|
| `Parse`        | What do these arguments ask for?        | a `Call`: a conversation and its key, or a one-shot call                                                                                        |
| `Probe`        | Is this conversation running?           | a `Probe`: the command that asks the agent program, and how to read its output into a `State`                                                   |
| `Worker`       | What does a worker for this call need?  | a `Needs`: the command with paths mapped, the files it must find, the names of the local variables it must be given, and the relays it requires |
| `Adapter`      | What does its ACP adapter need?         | a `Needs` of the same shape                                                                                                                     |
| `HookSettings` | How do its hook settings reach a relay? | the settings document with its hooks aimed at a remote port and carrying the worker's token                                                     |

An agent is handed a `pathmap.Map` and never learns where the other workstation is or how it is reached.

### Concrete types

| Type                  | Operations                                                                                                                                                 | Notes                                                                                                                                     |
|-----------------------|------------------------------------------------------------------------------------------------------------------------------------------------------------|-------------------------------------------------------------------------------------------------------------------------------------------|
| `workspace.Workspace` | `For` (the workspace a directory belongs to, made if there is none), `State`, `PathMap`, `AddFS`, `AddNet`, `Worker`, `Start`, `Attach`, `Detach`, `Close` | The only type that pairs relays with workers. `Detach` and `Close` apply the release rule. Leases are its own detail.                     |
| `worker.Worker`       | `State`, `Start`, `Close`, `Remove`, `Stdio`; `List` by tag                                                                                                | `Spec` says what to start: key, directory, command, files, variables, tags and the IO `Form` (terminal or line stream).                   |
| `acp.Conn`            | `Read`, `Write`                                                                                                                                            | One message per line. A `Message` gives its method, its ID, the chat it belongs to and the directory it names, and takes a `pathmap.Map`. |
| `air.Config`          | `Load`, `Merge`, `Save`                                                                                                                                    | Merging keeps every entry and key it does not own.                                                                                        |
| `pathmap.Map`         | `ToRemote`, `ToLocal`                                                                                                                                      | Built from the ends of a workspace's FS relays, longest prefix first.                                                                     |

## Flows

The steps and their rules are in the [design](../openspec/changes/add-air-bridge/design.md#flows). The diagrams
here show which package does each step.

### Session: `hermod attach`, and `claude` on a terminal

```mermaid
sequenceDiagram
    participant cli
    participant control
    participant agent as agent (claude)
    participant workspace
    participant relay
    participant worker
    cli ->> control: Attach(request)
    opt the command is an agent's
        control ->> agent: Parse(args)
        agent -->> control: Call (a conversation and its key)
    end
    control ->> workspace: For(remote, directory)
    workspace ->> relay: ListFS, then Connect and Flush the FS relay
    workspace -->> control: Workspace, PathMap
    control ->> workspace: Worker(key).State
    workspace ->> worker: State
    opt the worker runs an agent
        control ->> agent: Probe(key)
        Note over control: runs the probe's command on the remote
    end
    Note over control: decides from the two states
    opt start
        control ->> agent: Worker(Call, PathMap)
        agent -->> control: Needs
        control ->> workspace: AddFS for each FS relay the agent requires
        control ->> workspace: Start(Spec)
        workspace ->> worker: Start
    end
    control ->> workspace: AddNet, then Attach(worker, streams)
    workspace ->> relay: Connect the Net relays, Serve the IO relay
    Note over relay: blocks until the client leaves
    control ->> workspace: Detach
    workspace ->> worker: List (does the workspace still have a worker?)
    workspace ->> relay: Disconnect or Remove each relay released
    control -->> cli: the result
```

### One-shot call

```mermaid
sequenceDiagram
    participant cli
    participant control
    participant agent as agent (claude)
    participant shell
    cli ->> control: Attach(request)
    control ->> agent: Parse(args)
    agent -->> control: Call (one-shot)
    control ->> shell: Run the command on the remote with the caller's streams
    shell -->> control: Result
    control -->> cli: ExitStatus
```

No workspace, no worker and no relay take part.

### Chat: `hermod air acp`

```mermaid
sequenceDiagram
    participant Air
    participant control
    participant acp
    participant agent as agent (claude)
    participant workspace
    participant adapter as adapters on the remote
    Air ->> control: starts Chat(request) through cli
    control ->> agent: Adapter()
    agent -->> control: Needs
    control ->> adapter: starts its own adapter over ssh (no worker)
    Air ->> control: initialize
    control ->> acp: Read, drop the fs and terminal capabilities
    control ->> adapter: initialize, to its own adapter
    adapter -->> Air: the answer
    Air ->> control: session/new (a directory)
    control ->> workspace: For(remote, directory)
    control ->> workspace: Worker(acp).State
    opt Absent
        control ->> workspace: Start(Spec for the adapter), AddNet for its hooks
        control ->> adapter: the kept initialize
    end
    control ->> workspace: Attach(worker, a line stream)
    control ->> adapter: session/new, paths mapped to the remote
    adapter -->> control: answer and updates
    control -->> Air: the same, paths mapped back, requests renumbered
    Air ->> control: session/close (a closed tab)
    control -->> Air: the answer; nothing is passed on
    opt it was the workspace's last open chat
control->>workspace: Detach (the Net relay is kept)
end

Air->>control: _session/archive (mark done)
control->>adapter: session/close, then the archive
opt the adapter runs no other chat
control->>workspace: Close(worker)
end

Air->>control: stdin EOF or SIGTERM
control->>workspace: Detach, for every workspace it holds
```

`control` holds one session per workspace that has a chat open and keeps, only while it runs, which chat belongs
to which workspace and which request ID it gave to which adapter. Neither is stored: after a restart a chat's
workspace is found again from the directory its `session/load` names.

### Close

```mermaid
sequenceDiagram
    participant control
    participant workspace
    participant worker
    participant relay
    control ->> workspace: Close(worker)
    Note over workspace: takes a lease on each relay first
    workspace ->> worker: Close
    workspace ->> worker: List (is another worker left?)
    alt another worker is left
        Note over workspace: the relays stay as they are
    else none
        workspace ->> relay: Flush the FS relays, then Remove every relay
    end
```

### Registration: `hermod air configure`

```mermaid
sequenceDiagram
    participant cli
    participant control
    participant air
    cli ->> control: Configure(request)
    control ->> air: Load acp.json
    control ->> air: Merge the remote's Entry
    control ->> air: Save
    control -->> cli: the entry, and what terminal mode needs
```

## The command-line seam

`cli` is the only package that reads the process: its arguments, its environment, its streams and the name it was
started under. It resolves each value once, an argument winning over its variable, builds one request, picks the
agent implementation the command names, and calls one use case. Started as `claude`, the binary builds the same
request `hermod attach -- claude ARGS…` builds. No package below `cli` imports the command-line framework or looks
at the environment.
