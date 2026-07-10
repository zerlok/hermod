# cli Specification

## Purpose

Defines the Hermod command-line interface: how a single invocation names the sandbox host, resolves every effective setting for the run, and passes a command through to the remote session.

## Requirements

### Requirement: Single-command invocation
The CLI SHALL accept one invocation of the form `hermod <sandbox> [flags] [-- <remote args>...]`,
where `<sandbox>` is a required positional argument naming an `~/.ssh/config` host alias. When no
sandbox argument is supplied, the CLI SHALL print usage and exit with a non-zero status without
starting any sync or session.

#### Scenario: Sandbox alias provided
- **WHEN** the user runs `hermod prod-box`
- **THEN** Hermod resolves the effective configuration for the `prod-box` host alias and begins the sync-and-attach flow

#### Scenario: Missing sandbox argument
- **WHEN** the user runs `hermod` with no positional argument
- **THEN** the CLI prints usage and exits non-zero
- **AND** no Mutagen session, SSH connection, or tmux session is started

### Requirement: Fully resolve the run from one invocation
Hermod SHALL determine every effective setting for the run — session name, sandbox host, working
directory, remote command, and dry-run mode — from the single invocation and its defaults before
any sync or remote session begins. Once a run starts, Hermod SHALL NOT prompt interactively for
missing values; the invocation alone determines the run.

#### Scenario: Settings resolved before anything runs
- **WHEN** the user issues a valid invocation
- **THEN** all effective settings are resolved from the flags and their defaults
- **AND** no sync or remote session starts until resolution is complete

### Requirement: Session name flag and default
The CLI SHALL accept a `-s <name>` flag naming the tmux and Mutagen session. When `-s` is
omitted, the CLI SHALL default the session name to a slug of the mirrored directory's remote
path (its home-relative path with separators replaced by dashes, or a full path slug when
outside home), so distinct projects sharing a base name do not collide on one session.

#### Scenario: Explicit session name
- **WHEN** the user runs `hermod prod-box -s my-session`
- **THEN** the resolved intent uses `my-session` as the session name for both tmux and Mutagen

#### Scenario: Default session name from directory path
- **WHEN** the user runs `hermod prod-box` from `~/dev/repos/myproj`
- **THEN** the resolved intent uses `dev-repos-myproj` as the session name

### Requirement: Working-directory flag and default
The CLI SHALL accept a `-C <dir>` flag selecting the directory to mirror. When `-C` is omitted,
the CLI SHALL mirror the current working directory.

#### Scenario: Explicit working directory
- **WHEN** the user runs `hermod prod-box -C ~/work/api`
- **THEN** the resolved intent mirrors `~/work/api` rather than the current directory

#### Scenario: Default working directory
- **WHEN** the user runs `hermod prod-box` with no `-C`
- **THEN** the resolved intent mirrors the current working directory

### Requirement: Passthrough of the remote command
Everything after a `--` separator SHALL be passed through verbatim as the command and arguments
to run in the remote session. When no `--` args are supplied, the remote session SHALL run the
default shell. Hermod SHALL NOT interpret or rewrite the passthrough args.

#### Scenario: Passthrough args forwarded verbatim
- **WHEN** the user runs `hermod prod-box -- claude --model opus`
- **THEN** the remote session runs `claude --model opus` exactly as given

#### Scenario: No passthrough args
- **WHEN** the user runs `hermod prod-box` with no `--`
- **THEN** the remote session runs the user's default shell

### Requirement: Dry-run flag
The CLI SHALL accept a `-n` (dry-run) flag. When set, the resolved intent SHALL be marked
dry-run so Hermod prints the commands it would run instead of executing side-effecting ones.

#### Scenario: Dry-run requested
- **WHEN** the user runs `hermod prod-box -n`
- **THEN** the resolved intent is marked dry-run
- **AND** Hermod prints the commands it would run without starting a real sync or session
