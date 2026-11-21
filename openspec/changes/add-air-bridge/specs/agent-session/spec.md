## ADDED Requirements

### Requirement: Classify a claude command
Hermod SHALL classify the command `attach` was given before doing anything else:

| Claude's arguments | Kind |
|---|---|
| a subcommand (`auth`, `mcp`, …), `--version`, `--help`, or `-p` with no session | utility call |
| `--input-format stream-json` with `--session-id` or `--resume <uuid>` | chat session |
| anything else with `--session-id` or `--resume <uuid>` | terminal session |
| anything else, or a command that is not `claude` | not an agent session |

A session is keyed by the UUID Claude stores it under: `--session-id`, else the `--resume <uuid>`. With
`--fork-session` Claude stores the resumed conversation under a new UUID, so only `--session-id` keys it, and a fork
without one has no session in the table above.

A command that is not an agent session SHALL run as today's attach, in a tmux session named after the directory.

#### Scenario: Subcommand is a utility call
- **WHEN** the command is `claude auth status`
- **THEN** it is classified as a utility call

#### Scenario: Settings read is a utility call
- **WHEN** the command is `claude -p --input-format stream-json --output-format stream-json --verbose`
- **THEN** it is classified as a utility call

#### Scenario: SDK session is a chat session
- **WHEN** the command is `claude --output-format stream-json --input-format stream-json --session-id=<uuid>`
- **THEN** it is classified as a chat session keyed by `<uuid>`

#### Scenario: Terminal session
- **WHEN** the command is `claude --resume <uuid> --settings <file>`
- **THEN** it is classified as a terminal session keyed by `<uuid>`

#### Scenario: Fork keyed by its new session
- **WHEN** the command is `claude --resume <uuid> --fork-session --session-id <new uuid>`
- **THEN** it is classified as a terminal session keyed by `<new uuid>`

#### Scenario: Plain claude
- **WHEN** the command is `claude --model opus`
- **THEN** it is not an agent session and runs as today's attach

### Requirement: Utility calls run straight on the sandbox
A utility call SHALL run `ssh <sandbox> claude ARGS…` with the caller's stdin, stdout and stderr, and Hermod SHALL
exit with its exit code. A utility call SHALL open no mirror, no tmux session and no hook proxy. It SHALL run in the
project's sandbox directory when that exists on the sandbox, and in the sandbox user's home otherwise. The project's
sandbox directory SHALL be found from the read-only listing of the sandbox's mirrors (`mutagen sync list
--label-selector hermod-sandbox=<sandbox>`), and a listing that cannot be read SHALL NOT fail the call.

#### Scenario: Exit code passed back
- **WHEN** `claude auth status` exits with code 1 on the sandbox
- **THEN** Hermod exits with code 1

#### Scenario: Project settings seen
- **WHEN** `claude mcp list` runs for a project already mirrored to the sandbox
- **THEN** it runs in the project's sandbox directory

#### Scenario: Nothing else started
- **WHEN** a utility call runs
- **THEN** the only Mutagen command issued is the read-only listing of the sandbox's mirrors
- **AND** no tmux or hook-proxy command is issued

### Requirement: Setup leaves stdin for Claude
Every command Hermod runs to set up an agent session SHALL take its stdin from `/dev/null`. Only the connection
that carries Claude's own stdio SHALL receive the caller's stdin.

#### Scenario: Initialize reaches Claude
- **WHEN** the caller writes `initialize` to stdin as soon as the stand-in starts
- **THEN** no setup command consumes it, and it reaches Claude

### Requirement: Claude says what state a session is in
Before starting or attaching, Hermod SHALL run one ssh call that runs `claude agents --json` on the sandbox and
checks for tmux session `hermod-<uuid>` and the session's transcript, and SHALL act on the result:

| Claude lists the UUID | tmux `hermod-<uuid>` | Transcript | Then |
|---|---|---|---|
| yes | yes | — | running: attach |
| yes | no | — | running outside Hermod: refuse |
| no | — | yes | closed: start with `--resume`, copying the transcript up first if it exists only locally |
| no | — | no | new: start, or fail if the caller asked to `--resume` |

A tmux session `hermod-<uuid>` left without its Claude SHALL be removed before a start. The session directory SHALL
NOT be used to decide the state. A Claude that Hermod started for the UUID, by the pid the session directory records
and its `HERMOD_SESSION`, whose tmux session is gone is being hung up on by a close: the check SHALL give it the
close's 5 s to exit before reading Claude's list.

#### Scenario: Running session
- **WHEN** Claude lists the UUID and `hermod-<uuid>` exists
- **THEN** Hermod attaches without starting Claude

#### Scenario: Running outside Hermod
- **WHEN** Claude lists the UUID and no `hermod-<uuid>` exists
- **THEN** Hermod refuses and starts no second Claude

#### Scenario: Reopened while being closed
- **WHEN** the check runs while a close of the session has killed `hermod-<uuid>` and its Claude is still exiting
- **THEN** Hermod waits for that Claude to exit and takes the session for closed, not for running outside Hermod

#### Scenario: Closed session with a local-only transcript
- **WHEN** Claude does not list the UUID and the transcript exists only locally
- **THEN** Hermod copies the transcript to the sandbox and starts Claude with `--resume <uuid>`

#### Scenario: Resume of an unknown session
- **WHEN** the caller asked to `--resume <uuid>` and neither Claude nor any transcript knows it
- **THEN** Hermod fails without starting Claude

#### Scenario: Stale tmux session
- **WHEN** Claude does not list the UUID but `hermod-<uuid>` exists
- **THEN** Hermod removes that tmux session before starting

### Requirement: The session directory holds what the running process needs
For an agent session Hermod SHALL create `~/.hermod/s/<uuid>/` on the sandbox with mode `0700` when it is missing,
holding `hookport`, `token`, `env`, `settings.json`, `mcp.json`, `pid` (the pid of the Claude it last started) and,
for a chat session, `in.fifo` and `out.log`. Secrets SHALL
reach it only over ssh stdin, never on a command line. The directory SHALL stay after the session closes.

#### Scenario: Directory created private
- **WHEN** a new agent session starts
- **THEN** `~/.hermod/s/<uuid>/` exists with mode `0700`

#### Scenario: No secret on a command line
- **WHEN** Hermod writes `token`, `env`, `settings.json` or `mcp.json`
- **THEN** their contents travel on ssh stdin and appear in no argv

### Requirement: Rewrite Claude's arguments for the sandbox
Hermod SHALL rewrite a session's arguments before starting Claude: the contents of the `--settings` file SHALL be
written to the session directory's `settings.json` and the argument pointed at it; local paths in any argument SHALL
be mapped to the sandbox through the run's path map; every `--mcp-config` value, inline or a file, SHALL be merged
into the session directory's `mcp.json`, without the servers whose command is a local program, and the option
pointed at it; `--session-id` and `--resume` SHALL pass through unchanged. Hermod SHALL NOT add `--name` or
otherwise change Claude's session name.

#### Scenario: Settings moved to the session directory
- **WHEN** the command carries `--settings <local file>`
- **THEN** Claude on the sandbox is started with `--settings` naming the session's `settings.json`

#### Scenario: Local MCP server removed
- **WHEN** `--mcp-config` holds `jetbrains_air_ide` whose command is the local IDE binary
- **THEN** Claude on the sandbox is started without that server

#### Scenario: MCP secrets off the command line
- **WHEN** `--mcp-config` holds a server that runs on the sandbox with a credential in its `env` or `headers`
- **THEN** that server reaches the sandbox in `mcp.json` over ssh stdin, and Claude's argv names only that file

#### Scenario: Session name untouched
- **WHEN** Hermod starts Claude for a session
- **THEN** the argv carries no `--name` that the caller did not pass

### Requirement: Carry Claude's environment to the sandbox
Hermod SHALL write the caller's `CLAUDE_*`, `ANTHROPIC_*` and `MAX_THINKING_TOKENS` variables and the git identity
into the session's `env` file, leaving out `CLAUDE_CODE_EXECUTABLE` and `CLAUDE_CONFIG_DIR`, which name local paths,
and the variables a running Claude sets for the programs it starts (such as `CLAUDE_CODE_EXECPATH`, `CLAUDE_PID`
and `CLAUDE_CODE_MESSAGING_TOKEN`), which belong to one local process.
Claude SHALL be started with `HERMOD_SESSION`, `HERMOD_MODE` and `HERMOD_ORIGIN` in its environment, with the mode
and origin of its tmux session, so a Claude without `HERMOD_SESSION` is known to have been started on the sandbox
itself.

#### Scenario: Local paths not carried
- **WHEN** the caller's environment has `CLAUDE_CODE_ENTRYPOINT`, `CLAUDE_CODE_EXECUTABLE` and `CLAUDE_CONFIG_DIR`
- **THEN** the `env` file holds `CLAUDE_CODE_ENTRYPOINT` and neither of the other two

#### Scenario: A parent Claude's variables not carried
- **WHEN** Hermod itself runs under a Claude that set `CLAUDE_CODE_EXECPATH` and `CLAUDE_CODE_MESSAGING_TOKEN`
- **THEN** the `env` file holds neither

#### Scenario: Claude process carries tags
- **WHEN** Hermod starts Claude for an Air chat
- **THEN** Claude's environment has `HERMOD_SESSION=<uuid>` and `HERMOD_MODE=air-acp`

### Requirement: Start a session in tmux
Hermod SHALL start an agent session as tmux session `hermod-<uuid>`, tagged as the `remote-session` capability
specifies, in the project's directory, sourcing the session directory's `env` and then running
`exec claude <rewritten args>`. In a chat session Claude's
stdin SHALL be `in.fifo`, held open by the tmux session so Claude never sees EOF, and its stdout SHALL be appended to
`out.log`.

#### Scenario: Chat session runtime
- **WHEN** a chat session starts
- **THEN** Claude reads from `in.fifo` and appends to `out.log`
- **AND** a detach does not close `in.fifo` for Claude

### Requirement: Attach to a terminal session
Hermod SHALL attach a terminal session with `tmux attach-session -t hermod-<uuid>` on a tty, over one ssh connection
that refuses a shared master connection. The attach SHALL join only a session that exists and SHALL NOT create one,
which would be a shell under the session's name. Claude SHALL run with tmux's `remain-on-exit failed`, so a Claude
that fails leaves its pane, its error and its exit status for the attach to show. A session gone by the time of the
attach SHALL fail the run.

#### Scenario: Terminal attach
- **WHEN** a terminal session is attached
- **THEN** ssh runs with a tty and without a shared master, and joins `hermod-<uuid>`

#### Scenario: Claude failed
- **WHEN** Claude exits with an error after its start
- **THEN** its pane stays, and the attach shows Claude's error and exit status

#### Scenario: Session gone
- **WHEN** `hermod-<uuid>` no longer exists when the attach runs
- **THEN** no session is created and the run fails

### Requirement: Attach to a chat session as a stream
Hermod SHALL attach a chat session over one ssh connection that refuses a shared master and runs a shell line that
tails `out.log` to stdout and copies stdin into `in.fifo` one complete line at a time. Hermod SHALL pass each line
through the run's path map, local to sandbox on stdin and sandbox to local on stdout, line by line and unbuffered.

#### Scenario: Paths mapped in the stream
- **WHEN** Claude writes a line naming a sandbox path to `out.log`
- **THEN** the caller receives that line with the local path

#### Scenario: Partial line never reaches Claude
- **WHEN** the connection drops in the middle of a stdin line
- **THEN** Claude receives no part of that line

### Requirement: A detach never ends the session
Stdin EOF, SIGTERM or a dropped connection SHALL end the attach and nothing else: the tmux session and Claude keep
running. Hermod SHALL exit within the SDK's window of 2 s after stdin EOF plus 5 s after SIGTERM.

#### Scenario: Air closes a tab
- **WHEN** the caller closes stdin
- **THEN** Hermod settles its mirrors and exits within the window
- **AND** `hermod-<uuid>` keeps running on the sandbox

### Requirement: Reattach to a running chat session
When a chat session is running, Hermod SHALL attach to the running Claude instead of starting another: it SHALL
answer the SDK's `initialize` from the first response in `out.log`, re-deliver control requests still waiting for an
answer, resend prompts that `out.log` has not echoed, and stream the log from its end. Where this live reattach is
not available, or fails, Hermod SHALL instead close the tmux session and start the session with `--resume`.

#### Scenario: Live reattach
- **WHEN** Air reopens a chat whose Claude is running
- **THEN** the SDK's `initialize` is answered from the log and no second Claude starts

#### Scenario: Pending permission prompt re-delivered
- **WHEN** Claude raised a permission request while nobody was attached
- **THEN** the reattached caller receives that request

#### Scenario: Fallback to resume
- **WHEN** a running chat session is reopened and live reattach is not available
- **THEN** Hermod closes `hermod-<uuid>` and starts Claude with `--resume <uuid>`

### Requirement: Close a session
Closing an agent session SHALL run `tmux kill-session -t hermod-<uuid>` on the sandbox, wait up to 5 s for its Claude
to exit, and leave the session directory for a later `--resume`. The mirrors serving the directory the tmux session
is tagged with SHALL be opened under a lease before the hang-up and settled after it by the pause-vs-teardown rule;
when the hang-up fails, or Claude outlives the wait, they SHALL be paused. A session whose tmux session is already
gone SHALL have nothing closed and its mirrors SHALL NOT be settled: the directory that names them is known only from
the tmux session's tag. A chat that Air reopens SHALL NOT be closed after: a close still queued for it SHALL be
dropped, and a close already under way SHALL skip the hang-up and settle the mirrors as a detach does.

#### Scenario: Close keeps the directory
- **WHEN** a session is closed
- **THEN** `hermod-<uuid>` no longer exists and `~/.hermod/s/<uuid>/` remains

#### Scenario: Close does not race an attach
- **WHEN** a session is closed while a stand-in is attached to another session on the same mirror
- **THEN** the mirror is not closed under the attached stand-in

#### Scenario: Session already gone
- **WHEN** a session is closed whose `hermod-<uuid>` no longer exists
- **THEN** nothing is closed and its mirrors are left as the last detach left them

#### Scenario: Chat reopened while its close is under way
- **WHEN** Air reopens a chat after its close has begun and before the hang-up
- **THEN** `hermod-<uuid>` keeps running and its mirrors are settled as a detach settles them

#### Scenario: Chat reopened while its close is queued
- **WHEN** Air reopens a chat whose close has not begun
- **THEN** the close is dropped and `hermod-<uuid>` is not touched
