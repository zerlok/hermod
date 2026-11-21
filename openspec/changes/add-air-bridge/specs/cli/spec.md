## MODIFIED Requirements

### Requirement: Single-command invocation
The CLI SHALL accept an invocation of the form `hermod attach [<sandbox>] [flags] [-- <command>...]`, where
`<sandbox>` names an `~/.ssh/config` host alias. When no sandbox argument is given, the CLI SHALL take the sandbox
from `HERMOD_SANDBOX`. When neither is given, the CLI SHALL print usage and exit with a non-zero status without
starting any sync or session. The bare `hermod <sandbox>` form SHALL NOT be accepted.

#### Scenario: Sandbox alias provided
- **WHEN** the user runs `hermod attach prod-box`
- **THEN** Hermod resolves the effective configuration for the `prod-box` host alias and begins the sync-and-attach flow

#### Scenario: Sandbox from the environment
- **WHEN** the user runs `hermod attach` with `HERMOD_SANDBOX=prod-box` set
- **THEN** Hermod resolves the effective configuration for the `prod-box` host alias

#### Scenario: Argument wins over the environment
- **WHEN** the user runs `hermod attach dev-box` with `HERMOD_SANDBOX=prod-box` set
- **THEN** Hermod resolves the effective configuration for the `dev-box` host alias

#### Scenario: Missing sandbox
- **WHEN** the user runs `hermod attach` with no positional argument and `HERMOD_SANDBOX` unset
- **THEN** the CLI prints usage and exits non-zero
- **AND** no Mutagen session, SSH connection, or tmux session is started

### Requirement: Fully resolve the run from one invocation
Hermod SHALL determine every effective setting for the run — session name, sandbox host, working directory, remote
command, and dry-run mode — from the invocation's arguments, its environment and their defaults before any sync or
remote session begins, an argument always winning over the environment. Each Hermod process SHALL resolve its own
invocation and SHALL NOT set the environment of another Hermod process to steer it. Once a run starts, Hermod SHALL
NOT prompt interactively for missing values; the invocation alone determines the run.

#### Scenario: Settings resolved before anything runs
- **WHEN** the user issues a valid invocation
- **THEN** all effective settings are resolved from the arguments, the environment and their defaults
- **AND** no sync or remote session starts until resolution is complete

### Requirement: Session name flag and default
The CLI SHALL accept a `-s <name>` flag naming the tmux session of an attach that is not an agent session. When `-s`
is omitted, the CLI SHALL default the session name to a slug of the mirrored directory's remote path (its
home-relative path with separators replaced by dashes, or a full path slug when outside home), so distinct projects
sharing a base name do not collide on one session. The session name SHALL NOT name the mirror, which belongs to the
mirrored root and the sandbox.

#### Scenario: Explicit session name
- **WHEN** the user runs `hermod attach prod-box -s my-session`
- **THEN** the resolved intent uses `my-session` as the tmux session name

#### Scenario: Default session name from directory path
- **WHEN** the user runs `hermod attach prod-box` from `~/dev/repos/myproj`
- **THEN** the resolved intent uses `dev-repos-myproj` as the tmux session name

### Requirement: Working-directory flag and default
The CLI SHALL accept a `-C <dir>` flag selecting the directory to mirror. When `-C` is omitted,
the CLI SHALL mirror the current working directory.

#### Scenario: Explicit working directory
- **WHEN** the user runs `hermod attach prod-box -C ~/work/api`
- **THEN** the resolved intent mirrors `~/work/api` rather than the current directory

#### Scenario: Default working directory
- **WHEN** the user runs `hermod attach prod-box` with no `-C`
- **THEN** the resolved intent mirrors the current working directory

### Requirement: Passthrough of the remote command
Everything after a `--` separator SHALL be the command and arguments to run in the remote session. When no `--`
args are supplied, the remote session SHALL run `HERMOD_SANDBOX_CMD` as a command line for the sandbox's shell, and
the default shell when that is unset. A command that is not an agent session SHALL be passed through verbatim; a
`claude` command SHALL be classified and run as the `agent-session` capability specifies.

#### Scenario: Passthrough args forwarded verbatim
- **WHEN** the user runs `hermod attach prod-box -- htop -d 5`
- **THEN** the remote session runs `htop -d 5` exactly as given

#### Scenario: Command from the environment
- **WHEN** the user runs `hermod attach prod-box` with no `--` and `HERMOD_SANDBOX_CMD` set
- **THEN** the remote session runs `HERMOD_SANDBOX_CMD` through the sandbox's shell

#### Scenario: Passthrough wins over the environment
- **WHEN** the user runs `hermod attach prod-box -- htop` with `HERMOD_SANDBOX_CMD` set
- **THEN** the remote session runs `htop`

#### Scenario: No command at all
- **WHEN** the user runs `hermod attach prod-box` with no `--` and `HERMOD_SANDBOX_CMD` unset
- **THEN** the remote session runs the user's default shell

#### Scenario: A claude command is an agent session candidate
- **WHEN** the user runs `hermod attach prod-box -- claude --resume <uuid>`
- **THEN** the command is classified as the `agent-session` capability specifies rather than passed through unexamined

### Requirement: Dry-run flag
The CLI SHALL accept a `-n` (dry-run) flag. When set, the resolved intent SHALL be marked
dry-run so Hermod prints the commands it would run instead of executing side-effecting ones.

#### Scenario: Dry-run requested
- **WHEN** the user runs `hermod attach prod-box -n`
- **THEN** the resolved intent is marked dry-run
- **AND** Hermod prints the commands it would run without starting a real sync or session

#### Scenario: Dry-run plan of a chat session on stderr
- **WHEN** the user runs `hermod attach -n prod-box -- claude --input-format stream-json --session-id <uuid>`
- **THEN** the plan is written to stderr, since a chat session's stdout is its stream
- **AND** stdout carries nothing from Hermod
