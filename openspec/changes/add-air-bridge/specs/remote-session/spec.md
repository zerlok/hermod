## MODIFIED Requirements

### Requirement: Attach to or create a persistent remote session
The remote session SHALL be a persistent tmux session on the remote, reached over SSH. For a command that runs no
agent, attaching SHALL join the tmux session with the resolved name when it is running, start it and then join it
when it is absent, and remove it and then start it anew when its command had failed. The attach SHALL be
interactive, inheriting the terminal so the user works directly in the remote session, and SHALL block until the
user detaches. Detaching SHALL end only the attach: the remote session keeps running.

#### Scenario: Session already exists
- **WHEN** the user attaches and a tmux session with the resolved name is running on the remote
- **THEN** the user is attached to that existing session over SSH

#### Scenario: Session does not exist
- **WHEN** the user attaches and no tmux session with the resolved name exists on the remote
- **THEN** the session is created on the remote and the user is attached to it

#### Scenario: Failed session started anew
- **WHEN** the user attaches and the tmux session with the resolved name holds a command that failed
- **THEN** that session is removed, and a new one is created and attached

#### Scenario: Attach blocks until detach
- **WHEN** the user is attached to the remote session
- **THEN** the attach call blocks while the user works
- **AND** it returns control only when the user detaches, and the remote session keeps running

### Requirement: Run the remote command inside the session
The remote session SHALL run the resolved remote command — the `--` passthrough args, or the default shell when
none were given — inside the persistent tmux session. A command that exits cleanly SHALL leave no tmux session
behind. A command that fails SHALL leave its tmux session with its pane kept, showing its error and exit status,
until the session is removed.

#### Scenario: Passthrough command runs in the window
- **WHEN** a passthrough command was supplied and the session is created
- **THEN** that command runs inside the tmux session

#### Scenario: Default shell runs when no command given
- **WHEN** no passthrough command was supplied and the session is created
- **THEN** the default login shell runs inside the tmux session

#### Scenario: Clean exit leaves nothing
- **WHEN** the command in the remote session exits with code 0
- **THEN** no tmux session remains under its name

#### Scenario: Failed command keeps its pane
- **WHEN** the command in the remote session exits with a non-zero code
- **THEN** the tmux session remains, and its pane shows the command's error and exit status

### Requirement: Report session liveness
Hermod SHALL be able to read which of a workspace's remote sessions exist on the remote, and whether each is
running or its command failed. The read SHALL ask tmux every time, by the tag every remote session of the workspace
carries, and SHALL NOT rely on any record Hermod keeps of its own. Reading SHALL be read-only and SHALL be
available after detach, where it decides whether the workspace's relays are kept or removed as the
`session-orchestration` capability specifies. A read that fails SHALL count as a remote session still running, so
nothing in use is removed.

#### Scenario: Session still running after detach
- **WHEN** liveness is read after detach and the tmux session still exists on the remote
- **THEN** the workspace is reported as having a running remote session

#### Scenario: Session gone after detach
- **WHEN** liveness is read after detach and no tmux session of the workspace exists on the remote
- **THEN** the workspace is reported as having no remote session

#### Scenario: Another session keeps the workspace in use
- **WHEN** one remote session of a workspace has ended and another tagged with the same workspace still exists
- **THEN** the workspace is reported as having a running remote session

#### Scenario: Liveness cannot be read
- **WHEN** the remote cannot be asked which tmux sessions exist
- **THEN** the workspace is treated as having a running remote session

## ADDED Requirements

### Requirement: Hermod's tmux sessions are found by name and tag
Every tmux session Hermod creates SHALL be named and tagged so that it is found again from its workspace on the
remote alone:

| Remote session | Name |
|---|---|
| a conversation | `hermod-<key>`, the key being the conversation's ID |
| a workspace's ACP adapter | `hermod-acp-<workspace>` |
| a plain `hermod attach` | its resolved name, unchanged |

Each SHALL carry the session options `@hermod-workspace`, naming the workspace it belongs to, and `@hermod-origin`,
naming the machine it was started from. The origin SHALL be `HERMOD_ORIGIN` when set and the local hostname
otherwise, reduced to letters, digits, dots and dashes; when `HERMOD_ORIGIN` is set to empty, the origin tag SHALL
be left out.

#### Scenario: Tags readable with tmux
- **WHEN** Hermod creates a tmux session for an attach from host `laptop.local`
- **THEN** `tmux ls -F '#{session_name} #{@hermod-workspace} #{@hermod-origin}'` shows the session's workspace and
  origin `laptop.local`

#### Scenario: One adapter name per workspace
- **WHEN** two chats are opened in the same project
- **THEN** one tmux session named `hermod-acp-<workspace>` runs on the remote for that project's workspace

#### Scenario: Origin from the environment
- **WHEN** `HERMOD_ORIGIN=build box #1` is set
- **THEN** the origin tag is derived from `build box #1` and holds only letters, digits, dots and dashes

#### Scenario: Empty origin
- **WHEN** `HERMOD_ORIGIN` is set to empty
- **THEN** the session carries no origin tag

### Requirement: A remote session's stdio outlives its client
A remote session SHALL offer its process's stdio in one of two forms, fixed when it starts: a terminal, joined on a
tty, or a line stream, which hands the process a client's input only in whole lines and appends its stdout and stderr
to logs on the remote. A line-stream process's stdin SHALL stay open while no client is attached, so the
process never sees end of input when a client leaves. What the process writes while no client is attached SHALL be
kept on the remote, on the terminal's screen or in the logs.

#### Scenario: Client leaves a line stream
- **WHEN** the only client attached to a line-stream remote session disconnects
- **THEN** the process keeps running and does not read end of input

#### Scenario: Output written while detached
- **WHEN** a remote session's process writes output while no client is attached
- **THEN** that output is kept on the remote, on the terminal's screen or in the logs
