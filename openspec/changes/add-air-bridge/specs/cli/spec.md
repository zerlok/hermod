## MODIFIED Requirements

### Requirement: Single-command invocation
The CLI SHALL accept an invocation of the form `hermod attach [<remote>] [flags] [-- <command>...]`, where
`<remote>` names an `~/.ssh/config` host alias. When no remote argument is given, the CLI SHALL take the remote
from `HERMOD_REMOTE`. When neither is given, the CLI SHALL print usage and exit with a non-zero status without
starting any sync or remote process. The bare `hermod <remote>` form SHALL NOT be accepted.

#### Scenario: Remote alias provided
- **WHEN** the user runs `hermod attach prod-box`
- **THEN** Hermod resolves the effective configuration for the `prod-box` host alias and attaches to it

#### Scenario: Remote from the environment
- **WHEN** the user runs `hermod attach` with `HERMOD_REMOTE=prod-box` set
- **THEN** Hermod resolves the effective configuration for the `prod-box` host alias

#### Scenario: Argument wins over the environment
- **WHEN** the user runs `hermod attach dev-box` with `HERMOD_REMOTE=prod-box` set
- **THEN** Hermod resolves the effective configuration for the `dev-box` host alias

#### Scenario: Missing remote
- **WHEN** the user runs `hermod attach` with no positional argument and `HERMOD_REMOTE` unset
- **THEN** the CLI prints usage and exits non-zero
- **AND** no Mutagen session, SSH connection, or tmux session is started

#### Scenario: Bare form refused
- **WHEN** the user runs `hermod prod-box`
- **THEN** the CLI exits non-zero without starting any sync or remote process

### Requirement: Fully resolve the run from one invocation
Every Hermod command SHALL determine every effective setting for its run — among them the remote, the tmux session
name, the working directory, the remote command, the origin name, the relayed ports and dry-run mode — from the
invocation's arguments, its environment and their defaults before any sync or remote process begins, an argument
always winning over the environment. Each Hermod process SHALL resolve its own invocation and SHALL NOT set the
environment of another Hermod process to steer it. Once a run starts, Hermod SHALL NOT prompt interactively for
missing values; the invocation alone determines the run.

#### Scenario: Settings resolved before anything runs
- **WHEN** the user issues a valid invocation
- **THEN** all effective settings are resolved from the arguments, the environment and their defaults
- **AND** no sync or remote process starts until resolution is complete

#### Scenario: Origin from the environment
- **WHEN** the user runs `hermod attach prod-box` with `HERMOD_ORIGIN=laptop` set
- **THEN** the run names its origin `laptop` instead of the local hostname

### Requirement: Session name flag and default
The CLI SHALL accept a `-s <name>` flag naming the tmux session that `hermod attach` runs a command in when the
command is not an agent's conversation. When `-s` is omitted, the CLI SHALL default the name to a slug of the
working directory's remote path (its home-relative path with separators replaced by dashes, or a full path slug
when outside home), so distinct projects sharing a base name do not collide on one tmux session. The name SHALL NOT
name any file relay: a file relay is named after its two ends and its remote.

#### Scenario: Explicit session name
- **WHEN** the user runs `hermod attach prod-box -s my-session`
- **THEN** the command runs in the tmux session `my-session`

#### Scenario: Default session name from directory path
- **WHEN** the user runs `hermod attach prod-box` from `~/dev/repos/myproj`
- **THEN** the command runs in the tmux session `dev-repos-myproj`

#### Scenario: Two sessions share one project's files
- **WHEN** the user runs `hermod attach prod-box -s one` and `hermod attach prod-box -s two` in the same project
- **THEN** both tmux sessions work on the files of the one file relay that covers the project

### Requirement: Working-directory flag and default
The CLI SHALL accept a `-C <dir>` flag selecting the local directory the run works in. When `-C` is omitted, the CLI
SHALL use the current working directory. The command SHALL run in the directory's remote path, and the directory's
files SHALL be relayed to the remote as the `file-sync` capability specifies.

#### Scenario: Explicit working directory
- **WHEN** the user runs `hermod attach prod-box -C ~/work/api`
- **THEN** the run works in `~/work/api` rather than the current directory
- **AND** the command runs in the remote path that `~/work/api` is relayed to

#### Scenario: Default working directory
- **WHEN** the user runs `hermod attach prod-box` with no `-C`
- **THEN** the run works in the current working directory

### Requirement: Passthrough of the remote command
Everything after a `--` separator SHALL be the command and arguments to run on the remote. When no `--` args are
supplied, the remote SHALL run `HERMOD_REMOTE_CMD` as a command line for the remote's shell, and the user's default
shell when that is unset. A command that is not an agent's SHALL be passed through verbatim; a `claude` command
SHALL be classified and run as the `agent-session` capability specifies.

#### Scenario: Passthrough args forwarded verbatim
- **WHEN** the user runs `hermod attach prod-box -- htop -d 5`
- **THEN** the remote runs `htop -d 5` exactly as given

#### Scenario: Command from the environment
- **WHEN** the user runs `hermod attach prod-box` with no `--` and `HERMOD_REMOTE_CMD` set
- **THEN** the remote runs `HERMOD_REMOTE_CMD` through the remote's shell

#### Scenario: Passthrough wins over the environment
- **WHEN** the user runs `hermod attach prod-box -- htop` with `HERMOD_REMOTE_CMD` set
- **THEN** the remote runs `htop`

#### Scenario: No command at all
- **WHEN** the user runs `hermod attach prod-box` with no `--` and `HERMOD_REMOTE_CMD` unset
- **THEN** the remote runs the user's default shell

#### Scenario: A claude command is examined
- **WHEN** the user runs `hermod attach prod-box -- claude --resume <uuid>`
- **THEN** the command is classified as the `agent-session` capability specifies rather than passed through unexamined

### Requirement: Dry-run flag
The CLI SHALL accept a `-n` (dry-run) flag. When set, Hermod SHALL print each command it would run, as a line that
can be pasted, and SHALL run none of them.

#### Scenario: Dry-run requested
- **WHEN** the user runs `hermod attach prod-box -n`
- **THEN** Hermod prints the commands it would run
- **AND** no Mutagen session, SSH connection, or tmux session is started or changed

## ADDED Requirements

### Requirement: Relay a remote port to a local address
`hermod attach` SHALL accept a `-R <port>:<address>` flag, any number of times. For each one, while the run is
attached, a connection to `<port>` on the remote SHALL reach `<address>` on the local workstation, passed through
unfiltered. The relayed port SHALL belong to the run that asked for it and SHALL NOT be shared with another run in
the same project.

#### Scenario: A local listener reached from the remote
- **WHEN** the user runs `hermod attach prod-box -R 9000:localhost:8080` and a process on the remote connects to
  port 9000
- **THEN** the connection reaches `localhost:8080` on the local workstation

#### Scenario: Several ports
- **WHEN** the user runs `hermod attach prod-box -R 9000:localhost:8080 -R 9001:localhost:8081`
- **THEN** both ports are relayed for the run

#### Scenario: Port gone with the run
- **WHEN** the run that asked for `-R 9000:localhost:8080` detaches
- **THEN** a connection to port 9000 on the remote no longer reaches `localhost:8080`

### Requirement: Graceful shutdown
Every Hermod command SHALL stop its work at the first SIGINT or SIGTERM and then give up what it holds (the relay
leases, the attached workspaces) within a teardown deadline: `--teardown-timeout`, else `HERMOD_TEARDOWN_TIMEOUT`,
else 10 seconds, counted from the first signal or from the start of the teardown, whichever is first. At the deadline, or at a second SIGINT or SIGTERM, the command
SHALL exit at once with an error, abandoning what is left. The stand-in SHALL take the deadline from
`HERMOD_TEARDOWN_TIMEOUT` alone.

#### Scenario: Teardown within the deadline
- **WHEN** an attached run receives SIGTERM and its relays settle in time
- **THEN** it detaches the workspace and exits

#### Scenario: Teardown cut by the deadline
- **WHEN** an attached run receives SIGTERM and detaching takes longer than the teardown deadline
- **THEN** it exits with an error at the deadline, and its file relays are left Disconnected

#### Scenario: Teardown without a signal
- **WHEN** `hermod air acp`'s stdin closes with no signal and detaching hangs
- **THEN** it exits with an error at the teardown deadline, counted from the start of the teardown

#### Scenario: Second signal
- **WHEN** a run that is tearing down receives a second SIGINT
- **THEN** it exits at once with an error

#### Scenario: Deadline from the environment
- **WHEN** the user runs `hermod attach prod-box` with `HERMOD_TEARDOWN_TIMEOUT=30s` and no `--teardown-timeout`
- **THEN** the run's teardown deadline is 30 seconds
