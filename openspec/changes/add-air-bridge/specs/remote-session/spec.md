## MODIFIED Requirements

### Requirement: Report session liveness
Hermod SHALL be able to determine whether a remote session is still running on the host, and whether any session
under a mirrored root is. A session's liveness is whether its tmux session exists; a root is alive while any tmux
session Hermod tagged with a `@hermod-dir` under that root exists. Checking liveness SHALL be read-only and SHALL be
available after detach to drive the pause-vs-teardown decision.

#### Scenario: Session still running after detach
- **WHEN** liveness is checked after detach and the tmux session still exists on the host
- **THEN** the remote session is reported as still running

#### Scenario: Session gone after detach
- **WHEN** liveness is checked after detach and no tmux session exists on the host
- **THEN** the remote session is reported as no longer running

#### Scenario: Another session keeps the root alive
- **WHEN** the root's liveness is checked and another tagged tmux session with a directory under that root exists
- **THEN** the root is reported as alive

## ADDED Requirements

### Requirement: Hermod's tmux sessions say where they came from
Every tmux session Hermod creates SHALL carry the session options `@hermod-mode`, `@hermod-origin` and `@hermod-dir`.
The mode SHALL be `air-acp` for an Air chat, `air-terminal` for Air's terminal tab and `attach` for a person's own
terminal. The origin SHALL be `HERMOD_ORIGIN` when set and the local hostname otherwise, reduced to letters, digits,
dots and dashes; when `HERMOD_ORIGIN` is set to empty, the origin tags SHALL be left out. The directory SHALL be the
session's sandbox working directory. A session that is not an agent session SHALL keep its resolved name.

#### Scenario: Tags readable with tmux
- **WHEN** Hermod creates a tmux session for an attach from host `laptop.local`
- **THEN** `tmux ls -F '#{session_name} #{@hermod-mode} #{@hermod-origin} #{@hermod-dir}'` shows mode `attach`, origin `laptop.local` and the session's directory

#### Scenario: Origin from the environment
- **WHEN** `HERMOD_ORIGIN=build box #1` is set
- **THEN** the origin tag is derived from `build box #1` and holds only letters, digits, dots and dashes

#### Scenario: Empty origin
- **WHEN** `HERMOD_ORIGIN` is set to empty
- **THEN** the session carries no origin tag
