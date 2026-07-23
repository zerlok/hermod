## MODIFIED Requirements

### Requirement: Sync-status queries are read-only probes
Querying the mirror's status (e.g. whether a session exists, or whether it is paused) SHALL be a
read-only probe that never mutates sync state. The probe SHALL report a session as absent only when
it positively determines no session exists; a probe failure for any other reason (e.g. the sync
backend being unavailable) SHALL be surfaced as an error rather than reported as absent.

#### Scenario: Status query does not change state
- **WHEN** the mirror's status is queried
- **THEN** the true current sync state is reported
- **AND** the query does not create, pause, resume, or terminate any session

#### Scenario: Ambiguous probe failure is not read as absent
- **WHEN** the mirror's status is queried and the probe fails for a reason other than the session being absent
- **THEN** the failure is surfaced to the caller
- **AND** the status is not reported as absent, so opening the mirror does not create a new session over an existing one
