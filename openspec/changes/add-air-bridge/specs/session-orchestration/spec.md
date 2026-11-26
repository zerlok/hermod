## RENAMED Requirements

- FROM: `### Requirement: Pause-vs-teardown keyed on remote-session liveness`
- TO: `### Requirement: Suspend or release keyed on the workspace's workers`

- FROM: `### Requirement: Mirror teardown is guaranteed on error`
- TO: `### Requirement: Leases are given up on error`

## MODIFIED Requirements

### Requirement: End-to-end orchestration flow
For a session on a terminal (`hermod attach`, or the stand-in on a tty), Hermod SHALL run one top-to-bottom pass:
(1) connect and flush the file relay that covers the directory, so work starts on a consistent tree; (2) read the
state of the session's worker and, for a worker that runs an agent, of its conversation, and decide as the
`agent-session` capability specifies; (3) when a start is decided, connect and flush the file relays the agent
requires, then start the worker with the local git identity among its variables; (4) connect the session's remaining
relays, attach the client to the worker and block while the user works; then (5) when the attach ends, detach the
workspace. The steps SHALL run in this order, and the client SHALL be attached only after the file relays it works
on are connected and flushed. Every detach, by a terminal session or by a chat client, SHALL give up the leases as
the requirement "Suspend or release keyed on the workspace's workers" specifies.

#### Scenario: Happy-path pass
- **WHEN** Hermod runs for a valid invocation on a terminal
- **THEN** it connects and flushes the file relay, decides from the states, starts the worker when needed, attaches
  the client and blocks
- **AND** when the attach ends it detaches the workspace

#### Scenario: Transcripts land before the agent reads them
- **WHEN** a conversation is started whose agent requires a file relay of its own
- **THEN** that relay is connected and flushed before the worker starts

### Requirement: Suspend or release keyed on the workspace's workers
When a process gives up its leases on a workspace's relays, Hermod SHALL read the workspace's workers on the remote
and decide from them alone. While any worker of the workspace exists, or the workers cannot be read, every lease
SHALL be suspended: a relay whose last lease is suspended stays and holds what arrives at either end, except a Net
relay, which is removed ([Leases](../../../../../docs/ARCHITECTURE.md#leases)), and a file relay is paused as the
`file-sync` capability specifies. With no worker left, every lease SHALL be released: a relay whose last lease is
released is removed, and a file relay is flushed first. A relay on which another process still holds a lease SHALL
be left as it is. No other signal (exit codes, timers, the state of an agent's conversation, or
whether a client shows a chat) SHALL be consulted for this decision.

#### Scenario: A worker remains → suspend
- **WHEN** the last process holding leases on a workspace's relays detaches and a worker of the workspace still
  exists
- **THEN** the workspace's file relays are paused, not removed, so a later session resumes them

#### Scenario: No worker left → flush then remove
- **WHEN** the last process holding leases on a workspace's relays detaches and no worker of the workspace exists
- **THEN** each file relay is flushed so nothing in flight is lost
- **AND** then every relay of the workspace is removed

#### Scenario: Workers cannot be read
- **WHEN** a process detaches and the workspace's workers cannot be read from the remote
- **THEN** its leases are suspended, so nothing a worker may still use is removed

#### Scenario: A run that started nothing
- **WHEN** a run fails before it starts or attaches a worker, and the workspace has no worker
- **THEN** its leases are released, and the relays it connected are removed as a detach with no worker removes them

#### Scenario: Other holders remain
- **WHEN** a process detaches while another process still holds a lease on the same relays
- **THEN** the relays keep running for that process, whatever the detaching process chose

#### Scenario: Workers are the only signal
- **WHEN** Hermod decides whether to suspend or release
- **THEN** it decides solely from the workspace's workers
- **AND** it ignores exit codes, timers and the agent's state

### Requirement: Leases are given up on error
Once a process has connected any relay of a workspace, an error before a clean detach SHALL still give up every
lease that process holds, by the same rule as a detach, so an error during a flush, a start or an attach never
leaves a relay leased or a sync session running that nothing uses. An error met while giving up one lease SHALL NOT
stop the others from being given up, and every such error SHALL be reported.

#### Scenario: Error during attach still gives up the leases
- **WHEN** an error occurs after the relays are connected but before a clean detach
- **THEN** every lease the process holds is suspended or released while handling that error
- **AND** no Mutagen session is left running that no process leases and no worker needs

#### Scenario: One relay fails to settle
- **WHEN** giving up the lease on one relay fails
- **THEN** the leases on the workspace's other relays are given up all the same
- **AND** the run reports the failure

### Requirement: Dry-run prints instead of executing side effects
Under dry-run, side-effecting commands SHALL be printed as copy-pasteable shell lines and treated as successful
rather than executed. The interactive attach SHALL NOT take over the terminal in a dry-run.

#### Scenario: Side-effecting commands are printed
- **WHEN** Hermod runs in dry-run mode
- **THEN** each side-effecting command (relay connect, flush, pause and removal, worker start, the attach) is
  printed as a shell line
- **AND** none of them are actually executed

### Requirement: Read-only probes stay real under dry-run
Even in a dry-run, Hermod SHALL run for real the commands that only read a state: git-identity reads, Mutagen
status queries, and the reading of the workspace's workers and of a conversation's state, so the dry-run plan
reflects the true identity, sync state and workers.

#### Scenario: Probes run for real in dry-run
- **WHEN** Hermod runs a dry-run
- **THEN** git-identity reads, sync-status queries and worker and conversation reads execute for real
- **AND** the printed dry-run plan reflects the actual identity, sync state and workers

## ADDED Requirements

### Requirement: Relays are shared through leases
Each process using a relay SHALL hold a lease on it for as long as it uses it, so several sessions of one workspace
share its relays. A lease SHALL be held by the operating system for the live process, so a process that ends,
including one that is killed, SHALL hold no lease and leave nothing to clean up. A process giving up its lease SHALL
say whether it suspends or releases it, and the relay SHALL act on that only when it was the last lease; otherwise
the relay SHALL be left as it is.

#### Scenario: Two sessions share one relay
- **WHEN** two sessions in one project use the same file relay and the first detaches
- **THEN** the relay keeps syncing for the second

#### Scenario: A killed process holds no lease
- **WHEN** a process holding a lease is killed
- **THEN** its lease ends with it, and the next process to give up its lease can be the last

#### Scenario: The last holder's choice settles the relay
- **WHEN** one process suspends its lease while another still holds one, and the other then releases its lease
- **THEN** the relay is removed, as the last holder chose
