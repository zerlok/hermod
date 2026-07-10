# session-orchestration Specification

## Purpose

Defines the top-to-bottom orchestration pass that ties identity, mirror, and remote session together: the ordered happy path, the pause-vs-teardown decision keyed on remote-session liveness, guaranteed teardown on error, and dry-run behavior.

## Requirements

### Requirement: End-to-end orchestration flow
For a valid invocation, Hermod SHALL run one top-to-bottom pass: (1) read local git
identity to carry into the remote session, (2) open the mirror — create or resume — and flush it
so work starts on a consistent tree, (3) attach to the remote session and block while the user
works, then (4) on detach, decide whether to pause or close the mirror. The steps SHALL run in
this order, with the attach starting only after the mirror is opened and flushed.

#### Scenario: Happy-path pass
- **WHEN** Hermod runs for a valid invocation
- **THEN** it reads git identity, opens and flushes the mirror, attaches the remote session, and blocks
- **AND** on detach it proceeds to the pause-vs-teardown decision

### Requirement: Pause-vs-teardown keyed on remote-session liveness
On detach, Hermod SHALL consult the remote session's liveness as the single source of
truth for the mirror's fate. When the remote session is still active, Hermod SHALL
**pause** the mirror. When the remote session is gone, Hermod SHALL flush the mirror and
then **close** it. No other signal (exit codes, timers) SHALL be consulted for this decision.

#### Scenario: Session still alive → pause
- **WHEN** the user detaches and the remote session is still active
- **THEN** Hermod pauses the mirror so it can be resumed later
- **AND** it does not terminate the mirror

#### Scenario: Session gone → flush then close
- **WHEN** the user detaches and the remote session is no longer active
- **THEN** Hermod flushes the mirror so nothing in flight is lost
- **AND** then closes (terminates) the mirror

#### Scenario: Liveness is the only signal
- **WHEN** Hermod makes the pause-vs-teardown decision
- **THEN** it decides solely on remote-session liveness
- **AND** it ignores exit codes and timers

### Requirement: Mirror teardown is guaranteed on error
Once the mirror has been opened, Hermod SHALL guarantee it is torn down if an error occurs before
a clean detach, so that an error during flush or attach still closes the mirror rather than
leaking a running or paused sync session.

#### Scenario: Error during attach still closes the mirror
- **WHEN** an error occurs after the mirror is opened but before a clean detach
- **THEN** the mirror is closed while handling that error
- **AND** no orphaned Mutagen session is left behind

### Requirement: Dry-run prints instead of executing side effects
Under dry-run, side-effecting commands SHALL be printed as copy-pasteable shell lines and treated
as successful rather than executed. The interactive attach SHALL NOT take over the terminal in a
dry-run.

#### Scenario: Side-effecting commands are printed
- **WHEN** Hermod runs in dry-run mode
- **THEN** each side-effecting command (mirror open/flush/pause/close, session attach) is printed as a shell line
- **AND** none of them are actually executed

### Requirement: Read-only probes stay real under dry-run
Even in a dry-run, read-only probes — git-identity reads and Mutagen status queries — SHALL run
for real, so the dry-run reflects true git identity and true sync state.

#### Scenario: Probes run for real in dry-run
- **WHEN** Hermod runs a dry-run
- **THEN** git-identity reads and sync-status queries execute for real
- **AND** the printed dry-run plan reflects the actual identity and current sync state
