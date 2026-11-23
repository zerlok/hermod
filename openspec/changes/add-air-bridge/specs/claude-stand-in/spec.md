## ADDED Requirements

### Requirement: Hermod named claude is attach
A `hermod` binary started under the program name `claude` with arguments `ARGS…` SHALL run exactly what
`hermod attach -- claude ARGS…` runs. Under every other program name the binary SHALL run the normal command line.
The stand-in SHALL take none of Hermod's flags: all its arguments are Claude's.

#### Scenario: Started as claude
- **WHEN** the binary is started as `claude --session-id <uuid> --settings <file>`
- **THEN** it runs the attach flow with the command `claude --session-id <uuid> --settings <file>`

#### Scenario: Started as hermod
- **WHEN** the binary is started as `hermod attach prod-box`
- **THEN** it runs the normal command line

#### Scenario: Hermod-looking arguments stay Claude's
- **WHEN** the binary is started as `claude -n`
- **THEN** `-n` is passed to Claude and the run is not a dry run

### Requirement: The stand-in resolves its remote from the environment
The stand-in SHALL take its remote from `HERMOD_REMOTE`, and SHALL fail before doing anything when it is unset.
`HERMOD_REMOTE_CMD` SHALL NOT apply to the stand-in, whose command is always given.

#### Scenario: Remote from the environment
- **WHEN** the stand-in starts with `HERMOD_REMOTE=prod-box`
- **THEN** it attaches to `prod-box`

#### Scenario: No remote
- **WHEN** the stand-in starts with `HERMOD_REMOTE` unset
- **THEN** it exits non-zero without running ssh, Mutagen or tmux

#### Scenario: Remote command ignored
- **WHEN** the stand-in starts with `HERMOD_REMOTE_CMD` set
- **THEN** the command it runs is still `claude` with its own arguments

### Requirement: A conversation needs a terminal
The stand-in SHALL run a conversation only when its caller is on a terminal. Without a terminal it SHALL still run
arguments that name no conversation as a one-shot call on the remote, with the caller's streams and exit code, and
SHALL refuse a conversation with an error that names `hermod air acp` as the way a program holds one.

#### Scenario: One-shot call without a terminal
- **WHEN** the stand-in is started as `claude --version` with no terminal
- **THEN** Claude's `--version` runs on the remote with the caller's streams
- **AND** the stand-in exits with its exit code

#### Scenario: Conversation without a terminal
- **WHEN** the stand-in is started as `claude --session-id <uuid>` with no terminal
- **THEN** it exits non-zero with an error naming `hermod air acp`
- **AND** no conversation is started on the remote

### Requirement: Stdout belongs to Claude's caller
The stand-in SHALL write nothing of Hermod's own to stdout. Hermod's own output, its log lines, its warnings, its
dry-run plan and what its setup commands print, SHALL go to stderr and to a per-session log file created with
mode `0600`. A stderr that can no longer be written SHALL cost the log nothing and SHALL NOT end the run.

#### Scenario: Hermod's output on stderr and in the log
- **WHEN** the stand-in runs a conversation
- **THEN** Hermod's log lines and the output of its Mutagen commands are written to stderr and the session log
- **AND** stdout carries nothing from Hermod

#### Scenario: Stderr gone
- **WHEN** the reader of the stand-in's stderr has gone
- **THEN** the session log still gets every line and the run ends as it would with stderr open

#### Scenario: Log is private
- **WHEN** the stand-in creates a session log
- **THEN** the file has mode `0600`

### Requirement: The stand-in link is the installer's
The stand-in SHALL be reached through a link named `claude` to the `hermod` binary, created by `make install`.
Where the link lives SHALL be the installer's choice, and no `hermod` command SHALL create, move or remove it.

#### Scenario: Hermod leaves the link alone
- **WHEN** any `hermod` command runs, `hermod air configure` included
- **THEN** no link named `claude` is created, moved or removed
