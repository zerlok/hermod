# file-sync Specification

## Purpose

Defines the Mutagen-backed file mirror between the local working directory and the remote host: opening as create-or-resume, flushing before work, pausing, closing, and read-only status probing.

## Requirements

### Requirement: Open the mirror as create-or-resume
The file mirror SHALL be established between the local working directory and `host:remote` over
Mutagen. Opening the mirror SHALL create a new Mutagen session when none exists for the session
name, or resume the existing (paused) session when one is found, without the user having to
distinguish the two cases.

#### Scenario: No existing session
- **WHEN** the mirror is opened and no Mutagen session exists for the session name
- **THEN** a new bidirectional Mutagen session is created between the local working directory and `host:remote`

#### Scenario: Paused session exists
- **WHEN** the mirror is opened and a paused Mutagen session already exists for the session name
- **THEN** that session is resumed rather than re-created, so the previously synced state is reused

### Requirement: Flush before work starts
After opening, the mirror SHALL flush — block until the local and remote trees are consistent —
before the interactive session begins, so work starts against a fully-synced tree.

#### Scenario: Flush completes before attach
- **WHEN** the mirror has just been opened
- **THEN** it flushes to a consistent state
- **AND** the interactive session is not started until the flush completes

### Requirement: Pause the mirror
The mirror SHALL support being paused, suspending synchronization while preserving the session so
it can later be resumed, reusing the previously synced state rather than re-creating from scratch.

#### Scenario: Pause preserves the session
- **WHEN** the mirror is paused
- **THEN** synchronization stops
- **AND** the Mutagen session remains registered so a later open resumes it

### Requirement: Close the mirror
The mirror SHALL support being closed, which terminates the Mutagen session and removes it so no
sync session is left running.

#### Scenario: Close terminates the session
- **WHEN** the mirror is closed
- **THEN** the Mutagen session is terminated and removed
- **AND** no paused or running sync session for that name remains

### Requirement: Sync-status queries are read-only probes
Querying the mirror's status (e.g. whether a session exists, or whether it is paused) SHALL be a
read-only probe that never mutates sync state.

#### Scenario: Status query does not change state
- **WHEN** the mirror's status is queried
- **THEN** the true current sync state is reported
- **AND** the query does not create, pause, resume, or terminate any session
