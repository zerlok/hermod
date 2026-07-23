## ADDED Requirements

### Requirement: Provision the remote sync root before creating the session
When opening the mirror requires creating a new Mutagen session, hermod SHALL first ensure the
remote sync root directory exists on the sandbox host, creating any missing parent directories,
before the Mutagen session is created. Mutagen creates the sync root but not its parents, so a
home-relative remote path with a missing parent would otherwise stall the initial sync. Provisioning
is a side effect and SHALL obey dry-run — printed as a shell line, not executed, under dry-run.

#### Scenario: Missing remote parent directory is created before sync
- **WHEN** the mirror is opened, no Mutagen session exists, and the remote path's parent directory does not exist on the host
- **THEN** the remote sync root and its missing parents are created on the host
- **AND** this happens before the Mutagen session is created, so the initial sync can complete

#### Scenario: Resume does not re-provision
- **WHEN** the mirror is opened and an existing Mutagen session is resumed
- **THEN** the remote sync root is not re-created, since the previously synced tree already exists

#### Scenario: Provisioning obeys dry-run
- **WHEN** the mirror is opened under dry-run and a new session would be created
- **THEN** the remote directory-creation command is printed as a copy-pasteable shell line
- **AND** it is not executed
