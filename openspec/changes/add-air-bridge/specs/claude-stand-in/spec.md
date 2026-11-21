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

### Requirement: The stand-in resolves its sandbox from the environment
The stand-in SHALL take its sandbox from `HERMOD_SANDBOX`, and SHALL fail before doing anything when it is unset.
`HERMOD_SANDBOX_CMD` SHALL NOT apply to the stand-in, whose command is always given.

#### Scenario: Sandbox from the environment
- **WHEN** the stand-in starts with `HERMOD_SANDBOX=prod-box`
- **THEN** it attaches to `prod-box`

#### Scenario: No sandbox
- **WHEN** the stand-in starts with `HERMOD_SANDBOX` unset
- **THEN** it exits non-zero without running ssh, Mutagen or tmux

#### Scenario: Sandbox command ignored
- **WHEN** the stand-in starts with `HERMOD_SANDBOX_CMD` set
- **THEN** the command it runs is still `claude` with its own arguments

### Requirement: Stdout belongs to Claude's caller
The stand-in SHALL write nothing of Hermod's own to stdout. Hermod's own output, its log lines, its warnings and
what its setup commands print, SHALL go to stderr and to a per-session log at `~/.cache/hermod/air/<uuid>.log`,
created with mode `0600`. A stderr that can no longer be written SHALL cost the log nothing and SHALL NOT end the run.

#### Scenario: Hermod's output on stderr and in the log
- **WHEN** the stand-in runs an agent session
- **THEN** Hermod's log lines and the output of its Mutagen commands are written to stderr and the session log
- **AND** stdout carries nothing from Hermod

#### Scenario: Stderr gone
- **WHEN** the reader of the stand-in's stderr has gone
- **THEN** the session log still gets every line and the run goes on to settle its mirrors

#### Scenario: Log is private
- **WHEN** the stand-in creates a session log
- **THEN** the file has mode `0600`

### Requirement: The stand-in never shadows a local claude
The stand-in SHALL be reached through the link `~/.local/share/hermod/bin/claude` to the `hermod` binary, a path
outside `PATH`, so a `claude` installed locally is still the one found on `PATH`.

#### Scenario: Local claude unaffected
- **WHEN** the stand-in link exists and the user runs `claude` from a shell
- **THEN** the shell runs the locally installed Claude, not the stand-in
