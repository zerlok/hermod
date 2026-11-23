## ADDED Requirements

### Requirement: Classify a claude command
Hermod SHALL classify the command `attach` was given before doing anything else:

| Claude's arguments | Kind |
|---|---|
| a subcommand (`auth`, `mcp`, …), `--version`, `--help`, or `-p` with no session | one-shot call |
| `--session-id <uuid>` or `--resume <uuid>` | conversation |
| anything else, or a command that is not `claude` | no agent: a worker that runs the command as given |

A conversation is keyed by the UUID Claude stores it under: `--session-id`, else the `--resume <uuid>`. With
`--fork-session` Claude stores the resumed conversation under a new UUID, so only `--session-id` keys it, and a fork
without one is no conversation in the table above.

A conversation SHALL run only with a terminal on the caller's side. Without one Hermod SHALL refuse it before doing
anything on the remote, and name `hermod air acp` as the way a program holds a conversation.

#### Scenario: Subcommand is a one-shot call
- **WHEN** the command is `claude auth status`
- **THEN** it is classified as a one-shot call

#### Scenario: Settings read is a one-shot call
- **WHEN** the command is `claude -p --input-format stream-json --output-format stream-json --verbose`
- **THEN** it is classified as a one-shot call

#### Scenario: Terminal conversation
- **WHEN** the command is `claude --resume <uuid> --settings <file>` on a terminal
- **THEN** it is classified as a conversation keyed by `<uuid>`

#### Scenario: Fork keyed by its new session
- **WHEN** the command is `claude --resume <uuid> --fork-session --session-id <new uuid>`
- **THEN** it is classified as a conversation keyed by `<new uuid>`

#### Scenario: Conversation without a terminal
- **WHEN** the command is `claude --output-format stream-json --input-format stream-json --session-id=<uuid>` and
  the caller's streams are not a terminal
- **THEN** Hermod refuses it, naming `hermod air acp`, and runs nothing on the remote

#### Scenario: Plain claude
- **WHEN** the command is `claude --model opus`
- **THEN** it runs as a worker with no agent, as `hermod attach` runs any other command

### Requirement: One-shot calls run straight on the remote
A one-shot call SHALL run `claude ARGS…` on the remote with the caller's stdin, stdout and stderr, and Hermod SHALL
exit with its exit code. It SHALL run in the project's remote directory when that exists on the remote, and in the
remote user's home otherwise. A one-shot call SHALL start no worker and SHALL make, connect or remove no relay;
finding the project's remote directory only reads the relays already there, and a read that fails SHALL NOT fail
the call.

#### Scenario: Exit code passed back
- **WHEN** `claude auth status` exits with code 1 on the remote
- **THEN** Hermod exits with code 1

#### Scenario: Project settings seen
- **WHEN** `claude mcp list` runs for a project already relayed to the remote
- **THEN** it runs in the project's remote directory

#### Scenario: Nothing else started
- **WHEN** a one-shot call runs
- **THEN** no worker is started and no relay changes state

### Requirement: Setup leaves stdin for the agent
Every command Hermod runs to set up a call or a worker SHALL take its stdin from `/dev/null`. Only the connection
that carries the agent program's own stdio SHALL receive the caller's stdin.

#### Scenario: Settings read reaches Claude
- **WHEN** the caller of a one-shot call writes its first message to stdin as soon as Hermod starts
- **THEN** no setup command consumes it, and it reaches Claude

### Requirement: Decide from the conversation's and the worker's states
Before starting or attaching a conversation, Hermod SHALL read whether the agent program lists the conversation as
running and the state of the conversation's worker, and SHALL act on the pair:

| Conversation | Worker | Then |
|---|---|---|
| running | running | attach |
| running | absent | refuse: it runs outside Hermod, and a start would be a second copy |
| not running | running or exited | remove the worker, then act as for absent |
| not running | absent | start, with the caller's arguments as given |

Both states SHALL be read from the agent program and tmux every time, and never from files a worker left behind.
Whether a conversation that is not running can be resumed SHALL be left to the agent program: Hermod starts it
with the caller's arguments, and Claude refuses a resume that has nothing to resume.

#### Scenario: Running conversation
- **WHEN** Claude lists the UUID and its worker is running
- **THEN** Hermod attaches without starting Claude

#### Scenario: Running outside Hermod
- **WHEN** Claude lists the UUID and no worker runs it
- **THEN** Hermod refuses and starts no second Claude

#### Scenario: Stale worker
- **WHEN** Claude does not list the UUID but its worker exists
- **THEN** Hermod removes that worker before starting

#### Scenario: Resume of an unknown conversation
- **WHEN** the caller asked to `--resume <uuid>` and no transcript of `<uuid>` exists on either workstation
- **THEN** Hermod starts Claude with the caller's arguments, and Claude's refusal reaches the caller

### Requirement: A conversation's transcripts are on the remote before it starts
Before starting a conversation Hermod SHALL connect and flush the file relays the agent declares for its
transcripts, so that a conversation that exists only locally is on the remote before Claude resumes it, and the
remote's conversations appear in the local config directory.

#### Scenario: Local-only transcript resumed
- **WHEN** a conversation whose transcript exists only locally is resumed
- **THEN** the transcript is on the remote before Claude starts with `--resume <uuid>`

#### Scenario: Remote conversation listed locally
- **WHEN** a conversation started on the remote has written its transcript
- **THEN** the transcript appears in the local config directory

### Requirement: A worker's directory holds what its process needs
Hermod SHALL give every worker that runs an agent program or its adapter a directory of its own on the remote,
created when missing with mode `0700`, holding the files the process was started with and nothing that decides a
state. Secrets SHALL reach the remote only inside that directory or on the worker's own stdin, written over ssh
stdin and never on a command line. The directory SHALL stay after the worker closes.

#### Scenario: Directory created private
- **WHEN** a new conversation starts
- **THEN** its worker's directory exists on the remote with mode `0700`

#### Scenario: No secret on a command line
- **WHEN** Hermod writes a token, the variables or the settings into the worker's directory
- **THEN** their contents travel on ssh stdin and appear in no argv

### Requirement: Rewrite Claude's arguments for the remote
Hermod SHALL rewrite a conversation's arguments before starting Claude: the contents of the `--settings` file SHALL
be written into the worker's directory, with its hooks aimed as the `hook-proxy` capability specifies, and the
argument pointed at that file; local paths in every argument SHALL be mapped to the remote through the run's path
map; `--session-id` and `--resume` SHALL pass through unchanged. The MCP servers the arguments name SHALL be passed
on as given, with only their paths mapped, and run on the remote: each `--mcp-config` value, inline or a file, SHALL
be written into the worker's directory and the option pointed at that file. Hermod SHALL NOT add `--name` or
otherwise change Claude's session name.

#### Scenario: Settings moved to the worker's directory
- **WHEN** the command carries `--settings <local file>`
- **THEN** Claude on the remote is started with `--settings` naming the file in its worker's directory

#### Scenario: MCP configuration passed on
- **WHEN** the command carries `--mcp-config <local file>`
- **THEN** Claude on the remote is started with the same option naming a file in its worker's directory, its paths
  mapped to the remote and nothing removed from it

#### Scenario: MCP secrets off the command line
- **WHEN** `--mcp-config` holds a server with a credential in its `env` or `headers`
- **THEN** that server reaches the remote over ssh stdin, and Claude's argv names only the file

#### Scenario: Session name untouched
- **WHEN** Hermod starts Claude for a conversation
- **THEN** the argv carries no `--name` that the caller did not pass

### Requirement: Only declared variables reach the remote
A worker that runs Claude or its ACP adapter SHALL be given only the variables Claude declares and the git
identity: the caller's `CLAUDE_*`, `ANTHROPIC_*` and `MAX_THINKING_TOKENS`, leaving out `CLAUDE_CODE_EXECUTABLE`
and `CLAUDE_CONFIG_DIR`, which name local paths, and the variables a running Claude sets for the programs it starts
(such as `CLAUDE_CODE_EXECPATH`, `CLAUDE_PID` and `CLAUDE_CODE_MESSAGING_TOKEN`), which belong to one local process.
No other local variable SHALL reach the remote.

#### Scenario: Local paths not carried
- **WHEN** the caller's environment has `CLAUDE_CODE_ENTRYPOINT`, `CLAUDE_CODE_EXECUTABLE` and `CLAUDE_CONFIG_DIR`
- **THEN** the worker is given `CLAUDE_CODE_ENTRYPOINT` and neither of the other two

#### Scenario: A parent Claude's variables not carried
- **WHEN** Hermod itself runs under a Claude that set `CLAUDE_CODE_EXECPATH` and `CLAUDE_CODE_MESSAGING_TOKEN`
- **THEN** the worker is given neither

#### Scenario: Undeclared variable stays local
- **WHEN** the caller's environment holds a credential under a name Claude does not declare
- **THEN** no worker is given it

### Requirement: Start a conversation in a worker
Hermod SHALL start a conversation as a worker of the workspace its directory belongs to, keyed by the
conversation's UUID, tagged as the `remote-session` capability specifies, in the directory's remote path, running
Claude with the rewritten arguments and the declared variables, on a terminal.

#### Scenario: Conversation started
- **WHEN** a new conversation starts in a project directory
- **THEN** a worker keyed by its UUID runs Claude in the directory's remote path

### Requirement: Attach to a terminal conversation
Hermod SHALL attach a terminal conversation by joining its worker on a tty. The attach SHALL join only a worker that exists and SHALL NOT create one, which would be
a shell under the conversation's name. A Claude that fails SHALL leave the worker Exited, with its error and exit
status kept for the attach to show. A worker gone by the time of the attach SHALL fail the run.

#### Scenario: Terminal attach
- **WHEN** a terminal conversation is attached
- **THEN** the caller is joined to the conversation's worker on a tty

#### Scenario: Claude failed
- **WHEN** Claude exits with an error after its start
- **THEN** the worker is kept, and the attach shows Claude's error and exit status

#### Scenario: Worker gone
- **WHEN** the conversation's worker no longer exists when the attach runs
- **THEN** no worker is created and the run fails

### Requirement: Leaving ends only the attach
Stdin EOF, a signal or a dropped connection SHALL end the attach and nothing else: the worker and its agent keep
running, and the workspace's relays are left as the `session-orchestration` capability specifies for a workspace
with a worker. Hermod SHALL then exit at once, so the program that started it never has to kill it.

#### Scenario: Terminal closed
- **WHEN** the terminal of an attached conversation is closed
- **THEN** Hermod exits, and the conversation's worker keeps running on the remote

### Requirement: Each workspace's chats run in one adapter
`hermod air acp` SHALL hold the client's one ACP connection for every chat of every project, and run each
project's chats in one ACP adapter on the remote: a worker of the workspace the chat's directory belongs to, its
stdio a line stream. A request that opens a new chat (`session/new`) SHALL be routed by the directory it names,
attaching that workspace and starting its adapter when none runs; every later message of the chat, and a request
that opens it again (`session/load`, `session/resume`), SHALL be routed by the chat's session ID to the same
adapter. Every message SHALL reach the adapter
with its paths mapped to the remote, and the client with its paths mapped back. A line SHALL reach the adapter only
whole. A second `hermod air acp` that opens a chat in a workspace whose adapter is already attached SHALL end
with that error, and the first SHALL keep its adapter.

#### Scenario: Two chats of one project
- **WHEN** Air opens two chats in the same project
- **THEN** both reach the same adapter, and only one adapter runs for the project

#### Scenario: Chats of two projects
- **WHEN** Air opens one chat in each of two projects on the same remote
- **THEN** each chat's messages reach the adapter of its own project's workspace

#### Scenario: Paths mapped
- **WHEN** Air sends a message naming a file under the local project root
- **THEN** the adapter receives the corresponding path under the remote root, and its answer reaches Air with the
  local path

#### Scenario: Next IDE run returns
- **WHEN** a new `hermod air acp` opens a chat in a project whose adapter still runs
- **THEN** the chat reaches that adapter and no new one is started

#### Scenario: Later message found by its chat
- **WHEN** Air sends a prompt that names only a chat's session ID
- **THEN** it reaches the adapter of the workspace the chat was opened in

#### Scenario: Partial line never reaches the adapter
- **WHEN** the connection drops in the middle of a line to the adapter
- **THEN** the adapter receives no part of that line

#### Scenario: Adapter held by another IDE
- **WHEN** a second IDE's `hermod air acp` opens a chat in a project whose adapter the first one is attached to
- **THEN** the second `hermod air acp` exits with the error, and the first keeps its adapter

### Requirement: Hermod answers what names no chat
`hermod air acp` SHALL answer `initialize` itself, from what the agent declares, and `session/list` from the agent
program on the remote, with no adapter. It SHALL remove the `fs` and `terminal` client capabilities from
`initialize`, so the agent reads, writes and runs everything on the remote, and SHALL send that `initialize` first
to every adapter it starts. A `$/cancel_request` SHALL reach the adapter that holds the cancelled request.

#### Scenario: Connection answered without an adapter
- **WHEN** Air sends `initialize` and no chat is open
- **THEN** Air receives the answer and no adapter is started

#### Scenario: Client capabilities removed
- **WHEN** Air's `initialize` declares the `fs` and `terminal` capabilities
- **THEN** no adapter receives either of them

#### Scenario: A new adapter is initialized
- **WHEN** a chat opens in a project whose adapter is not running
- **THEN** the adapter is started and receives Air's `initialize` before the request that opens the chat, and Air
  does not receive the adapter's answer to it

#### Scenario: Chats listed from the remote
- **WHEN** Air sends `session/list` for a project directory
- **THEN** the answer lists the conversations the agent program on the remote has in that directory, with local
  paths

#### Scenario: Cancel reaches the request's adapter
- **WHEN** Air sends `$/cancel_request` for a prompt it sent to a chat of a project
- **THEN** the cancel reaches that project's adapter

### Requirement: A chat Hermod does not hold is found in the agent program
`hermod air acp` SHALL ask the agent program on the remote for the directory and state of a chat that no attached
workspace holds when `session/close`, `_session/archive`, `session/load` or `session/resume` names it, attach the
workspace of that directory, and handle the request as for a chat it holds. A chat the agent program does not have
SHALL be answered to Air as an error, and `hermod air acp` SHALL go on serving every other chat.

#### Scenario: Reopened after a restart
- **WHEN** a new `hermod air acp` receives `session/resume` for a chat that runs on the remote
- **THEN** the workspace of the chat's directory is attached and the request reaches its adapter

#### Scenario: Unknown chat
- **WHEN** Air names a chat that the agent program on the remote does not have
- **THEN** Air receives an error for that request, and `hermod air acp` keeps running

### Requirement: A closed tab leaves its chat running
`session/close`, which Air sends for every closed tab and for every open chat at an IDE quit, SHALL be answered by
Hermod with an empty result and SHALL NOT reach the adapter, so the chat's agent keeps running. While a chat is
hidden and another chat of its workspace is shown, a request its adapter sends for it SHALL be held and delivered
right after the answer to the request that opens the chat again, and the chat's other output SHALL NOT be sent,
since the adapter replays the chat when it is opened again. When no chat of a workspace is shown any more, Hermod
SHALL detach the workspace, and its adapter keeps running.

#### Scenario: Tab closed
- **WHEN** Air sends `session/close` for a chat
- **THEN** Air receives an empty result, the adapter receives nothing, and the chat's agent keeps running

#### Scenario: Last tab of a project closed
- **WHEN** Air closes the only open chat of a project
- **THEN** the workspace is detached and its adapter keeps running on the remote

#### Scenario: Question asked while hidden
- **WHEN** the adapter sends `session/request_permission` for a chat whose tab is closed while another chat of its
  project is shown, and Air opens the chat again
- **THEN** Air receives the answer to its `session/resume` first and the permission request after it

### Requirement: Mark done ends a chat
`_session/archive`, which Air sends when a chat is marked done, SHALL reach the chat's adapter, and the adapter ends
the chat's agent. Hermod SHALL then forget the chat, and pass to Air as it is whatever the adapter still sends of
it. When no chat of the workspace is shown any more, Hermod SHALL close the adapter once it has answered, if no
conversation of the project's workspace, in its root or a directory under it, still runs, and the workspace's relays SHALL be released; otherwise it
SHALL detach the workspace and the adapter keeps running.

#### Scenario: Chat marked done
- **WHEN** Air sends `_session/archive` for a chat of a project whose workspace has another chat open
- **THEN** the archive reaches the adapter, and the chat's agent stops

#### Scenario: Only shown chat marked done
- **WHEN** Air marks done the only shown chat of a project, and no other conversation of the project runs
- **THEN** the archive reaches the adapter, the chat's agent stops, the adapter is closed, and the workspace's
  relays are released

#### Scenario: Marked done while another conversation runs
- **WHEN** Air marks done the only shown chat of a project whose other chat runs hidden
- **THEN** the chat's agent stops, the workspace is detached, and the adapter keeps running

### Requirement: The adapters' requests are renumbered
Every request an adapter sends SHALL reach the client under an ID unique on the client's connection, and the
client's answer SHALL reach the adapter that asked under that adapter's own ID. The client's requests SHALL keep
their IDs. A request whose adapter is detached before it is answered SHALL be forgotten.

#### Scenario: Two adapters use the same ID
- **WHEN** the adapters of two projects each send `session/request_permission` with ID 0
- **THEN** Air receives two requests with different IDs, and each answer reaches the adapter that asked, under
  ID 0

#### Scenario: Client IDs unchanged
- **WHEN** Air sends a prompt with ID 7
- **THEN** the adapter receives it with ID 7
