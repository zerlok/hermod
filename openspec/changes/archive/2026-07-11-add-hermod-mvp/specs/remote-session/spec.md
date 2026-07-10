## ADDED Requirements

### Requirement: Attach to or create a persistent remote session
The remote session SHALL be a persistent tmux window on the sandbox host, reached over SSH.
Attaching SHALL join an existing tmux session with the resolved session name when one is present,
or create it and then attach when it is absent. The attach SHALL be interactive, inheriting the
terminal so the user works directly in the remote session, and SHALL block until the user
detaches.

#### Scenario: Session already exists
- **WHEN** the user attaches and a tmux session with the resolved name already exists on the host
- **THEN** the user is attached to that existing session over SSH

#### Scenario: Session does not exist
- **WHEN** the user attaches and no tmux session with the resolved name exists on the host
- **THEN** the session is created on the host and the user is attached to it

#### Scenario: Attach blocks until detach
- **WHEN** the user is attached to the remote session
- **THEN** the attach call blocks while the user works
- **AND** it returns control only when the user detaches

### Requirement: Run the remote command inside the session
The remote session SHALL run the resolved remote command — the `--` passthrough args, or the
default shell when none were given — inside the persistent window.

#### Scenario: Passthrough command runs in the window
- **WHEN** a passthrough command was supplied and the session is created
- **THEN** that command runs inside the tmux window

#### Scenario: Default shell runs when no command given
- **WHEN** no passthrough command was supplied and the session is created
- **THEN** the default login shell runs inside the tmux window

### Requirement: Report session liveness
Hermod SHALL be able to determine whether the remote session is still running on the host.
Checking liveness SHALL be read-only and SHALL be available after detach to drive the
pause-vs-teardown decision.

#### Scenario: Session still running after detach
- **WHEN** liveness is checked after detach and the tmux session still exists on the host
- **THEN** the remote session is reported as still running

#### Scenario: Session gone after detach
- **WHEN** liveness is checked after detach and no tmux session exists on the host
- **THEN** the remote session is reported as no longer running
