## MODIFIED Requirements

### Requirement: End-to-end orchestration flow
For a valid invocation, Hermod SHALL run one top-to-bottom pass: (1) read local git identity to carry into the
remote session, (2) take a shared lease on each mirror the run uses, open it — create or resume — and flush the
project mirror so work starts on a consistent tree, (3) attach to the remote session and block while the user works,
then (4) on detach, release the leases and let the last holder of each mirror decide whether to pause or close it.
The steps SHALL run in this order, with the attach starting only after the mirror is opened and flushed. An agent
session SHALL run this same pass with the steps the `agent-session` capability adds.

#### Scenario: Happy-path pass
- **WHEN** Hermod runs for a valid invocation
- **THEN** it reads git identity, leases, opens and flushes the mirror, attaches the remote session, and blocks
- **AND** on detach it releases its leases and proceeds to the pause-vs-teardown decision

### Requirement: Pause-vs-teardown keyed on remote-session liveness
On detach, the last holder of a mirror's lease SHALL consult the liveness of the mirror's root as the single source
of truth for the mirror's fate. When any session under the root is still active, or its liveness is unknown, Hermod
SHALL **pause** the mirror. When no session under the root is active, Hermod SHALL flush the mirror and then
**close** it. A holder that is not the last SHALL leave the mirror as it is. No other signal (exit codes, timers)
SHALL be consulted for this decision, whether or not the run started or attached a session. A root on which Hermod
failed to hang up on a Claude has unknown liveness, since that Claude may outlive its tmux session, and its mirrors
SHALL be paused.

#### Scenario: Session still alive → pause
- **WHEN** the last holder detaches and a session under the root is still active
- **THEN** Hermod pauses the mirror so it can be resumed later
- **AND** it does not terminate the mirror

#### Scenario: Session gone → flush then close
- **WHEN** the last holder detaches and no session under the root is active
- **THEN** Hermod flushes the mirror so nothing in flight is lost
- **AND** then closes (terminates) the mirror

#### Scenario: A run that attached nothing
- **WHEN** a run fails before it starts or attaches a session, and no session under the root is active
- **THEN** Hermod flushes and closes the mirror as a detach would

#### Scenario: Other holders remain
- **WHEN** a process detaches while another process still holds a lease on the mirror
- **THEN** the mirror is neither paused nor closed by the detaching process

#### Scenario: Liveness is the only signal
- **WHEN** Hermod makes the pause-vs-teardown decision
- **THEN** it decides solely on the root's liveness
- **AND** it ignores exit codes and timers

### Requirement: Mirror teardown is guaranteed on error
Once a mirror has been opened, Hermod SHALL guarantee that an error before a clean detach still releases its lease
and settles the mirror by the pause-vs-teardown rule, so an error during flush or attach never leaks a running sync
session or a lease.

#### Scenario: Error during attach still settles the mirror
- **WHEN** an error occurs after the mirror is opened but before a clean detach
- **THEN** the lease is released and the mirror is settled while handling that error
- **AND** no unsettled Mutagen session is left behind

## ADDED Requirements

### Requirement: Mirrors are shared through file-lock leases
Each process using a mirror SHALL hold a shared `flock` on `$XDG_RUNTIME_DIR/hermod/lease/<mirror>` while it uses
it. On exit it SHALL try a non-blocking exclusive lock on that file; the process that obtains it is the last holder
and settles the mirror. A process that ends without releasing its lock SHALL NOT keep the mirror leased.

#### Scenario: Two sessions share one mirror
- **WHEN** two sessions in one project use the same mirror and the first detaches
- **THEN** the first fails to take the exclusive lock and leaves the mirror running for the second

#### Scenario: A killed process releases its lease
- **WHEN** a process holding a lease is killed
- **THEN** its lock is released by the kernel and the next process to exit can become the last holder
