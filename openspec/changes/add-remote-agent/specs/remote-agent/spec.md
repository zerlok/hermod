## ADDED Requirements

### Requirement: Hermod reports its own version
The Hermod binary SHALL know and report the version it was built as, through a `hermod version`
command and a `--version` flag, in a form that two Hermod binaries can compare. A build that carries
no stamped version SHALL report a development version that never compares as newer than a released
one.

#### Scenario: Released build reports its version
- **WHEN** a build-stamped binary is asked for its version
- **THEN** it reports the stamped version

#### Scenario: Development build never outranks a release
- **WHEN** an unstamped build's version is compared with a released version
- **THEN** the unstamped build does not compare as newer

### Requirement: Discover the sandbox agent on attach
On attach, Hermod SHALL determine the version of the Hermod agent installed in its own directory on
the sandbox, by running that agent. An agent that cannot run, or whose output cannot be read as a
version, SHALL be treated as absent. The discovery SHALL be read-only and SHALL run for real even
under dry-run.

#### Scenario: Agent present and readable
- **WHEN** a working agent is installed on the sandbox
- **THEN** Hermod determines its version

#### Scenario: Agent missing, broken, or unreadable
- **WHEN** no agent is installed, or the installed one fails to run or reports nothing usable
- **THEN** Hermod treats the sandbox as having no agent

### Requirement: Install or upgrade the agent when the sandbox is behind
When the sandbox has no agent, or an agent older than the local binary, Hermod SHALL install its own
executable there; when the sandbox agent is the same version or newer, Hermod SHALL leave it
untouched. The installed agent SHALL be owner-only executable, SHALL be published so that no
partially-uploaded binary is ever reachable at the agent path, and the publish SHALL be atomic with
respect to anything about to run it. Under `--dry-run` Hermod SHALL install nothing.

#### Scenario: Sandbox is behind
- **WHEN** the sandbox agent is older than the local binary, or absent
- **THEN** Hermod installs the local executable as the sandbox agent
- **AND** the agent path resolves to the newly installed binary only once it is complete and executable

#### Scenario: Sandbox is current
- **WHEN** the sandbox agent's version is equal to or newer than the local binary's
- **THEN** Hermod installs nothing and writes nothing to the sandbox

#### Scenario: Interrupted install
- **WHEN** an install is interrupted part-way
- **THEN** the agent path still resolves to the previously installed agent, or to nothing
- **AND** no partially-written binary is reachable at the agent path

#### Scenario: Dry run
- **WHEN** the user runs a dry run
- **THEN** the sandbox is not written to

### Requirement: Only install an agent the sandbox can run
Hermod SHALL install its executable only where the sandbox's platform matches the local binary's.
On a mismatch it SHALL install nothing and SHALL report that the sandbox has no agent, rather than
installing a binary that cannot execute.

#### Scenario: Platform mismatch
- **WHEN** the sandbox's operating system or architecture differs from the local binary's
- **THEN** nothing is installed
- **AND** Hermod reports why the sandbox has no agent

### Requirement: The session can reach the agent
When an agent is present, Hermod SHALL carry its absolute path into the session environment so a
process on the sandbox can invoke it without depending on the sandbox's `PATH`. Where the sandbox
has a conventional user binary directory that is on `PATH`, Hermod SHALL additionally make the agent
reachable there by its plain name, without ever replacing a `hermod` that Hermod did not install.

#### Scenario: Agent path carried into the session
- **WHEN** a session starts with an agent present on the sandbox
- **THEN** the session environment carries the agent's absolute path

#### Scenario: An existing user-installed hermod is left alone
- **WHEN** the sandbox's user binary directory already holds a `hermod` that Hermod did not install
- **THEN** Hermod does not replace or remove it

### Requirement: Bootstrapping is best-effort and never fatal
Agent discovery and installation SHALL never affect the session, the mirror, or the
pause-vs-teardown decision. Any failure — probe, upload, publish, or link — SHALL be logged and the
session SHALL proceed normally without an agent. Hermod SHALL provide a flag to skip bootstrapping
entirely.

#### Scenario: Install failure degrades to a session without an agent
- **WHEN** the agent cannot be installed for any reason
- **THEN** Hermod logs why
- **AND** the sync-and-attach flow runs normally
- **AND** the mirror is still settled by remote-session liveness

#### Scenario: Bootstrapping skipped
- **WHEN** the user runs with the skip flag
- **THEN** the sandbox is neither probed for an agent nor written to
