## MODIFIED Requirements

### Requirement: Open the mirror as create-or-resume
The file mirror SHALL be established between a local root and `host:remote` over Mutagen. Opening the mirror SHALL
create a new Mutagen session when none exists under the mirror's name, or resume the existing (paused) session when
one is found, without the user having to distinguish the two cases. Before resuming, Hermod SHALL check that the
found session's remote endpoint is `host:remote`, and SHALL NOT resume a session that mirrors to another endpoint.

#### Scenario: No existing session
- **WHEN** the mirror is opened and no Mutagen session exists under the mirror's name
- **THEN** a new Mutagen session is created between the local root and `host:remote`

#### Scenario: Paused session exists
- **WHEN** the mirror is opened and a paused Mutagen session with the expected remote endpoint already exists under the mirror's name
- **THEN** that session is resumed rather than re-created, so the previously synced state is reused

#### Scenario: Session with another endpoint
- **WHEN** the mirror is opened and the session found under the mirror's name mirrors to a different remote endpoint
- **THEN** that session is not resumed

## ADDED Requirements

### Requirement: Mirror names and labels carry the sandbox
A project mirror SHALL be named `hermod-<sandbox>-<slug>` and a transcript mirror
`hermod-<sandbox>-<slug>-transcripts-<local>`, where `<sandbox>` is the host alias reduced to the letters, digits and
dashes Mutagen accepts, `<slug>` is the mirrored root's slug, and `<local>` is the start of a hash of the transcript
mirror's local end, so the transcripts of one sandbox directory brought to two local Claude config directories are
two mirrors. Each mirror SHALL carry the labels `hermod-kind` (`project` or `transcripts`) and
`hermod-sandbox=<sandbox>`. Mirrors carry no session ID. Mirrors made under the earlier names SHALL NOT be resumed;
Hermod SHALL list them and SHALL leave terminating them to the user.

#### Scenario: Same project on two sandboxes
- **WHEN** one project is attached to `box-a` and then to `box-b`
- **THEN** each sandbox has its own mirror, named after that sandbox
- **AND** attaching to `box-b` never resumes the mirror to `box-a`

#### Scenario: One directory's transcripts to two config directories
- **WHEN** a terminal session with `~/.claude` and an Air chat with its entry's `CLAUDE_CONFIG_DIR` run in one sandbox directory
- **THEN** each has its own transcript mirror, and neither resumes the other's

#### Scenario: Mirror under an earlier name
- **WHEN** a mirror named after the project path alone exists
- **THEN** Hermod lists it and does not resume or terminate it

### Requirement: Mutagen is the registry of mirrored roots
Hermod SHALL read which local roots are mirrored to a sandbox, and to which sandbox paths, from the endpoints of the
Mutagen sessions labelled `hermod-sandbox=<sandbox>`. Hermod SHALL NOT keep a local file or database recording
mirrors or sessions.

#### Scenario: Roots listed from Mutagen
- **WHEN** Hermod needs the mirrored roots of a sandbox
- **THEN** it lists Mutagen sessions with the label selector `hermod-sandbox=<sandbox>` and reads each root pair from the endpoints

### Requirement: The project mirror never picks a winner
A project mirror SHALL be created in Mutagen's `two-way-safe` mode, so edits made on either side reach the other and
neither side's edits are discarded. A file changed on both sides SHALL be left as it is on each and reported by
Mutagen as a conflict for the developer to settle. Hermod SHALL NOT resolve conflicts, including those between
several sandboxes attached to one project.

#### Scenario: Sandbox edit reaches the working tree
- **WHEN** the agent on the sandbox edits a mirrored file
- **THEN** the edit arrives in the local working tree

#### Scenario: Conflicting edits
- **WHEN** the same file is changed locally and on the sandbox
- **THEN** neither change is discarded and Mutagen reports a conflict

### Requirement: Transcript mirrors bring Claude's transcripts back
For each distinct working directory of an agent session, Hermod SHALL open a one-way `one-way-safe` Mutagen session
from `projects/<encoded sandbox directory>/` under the sandbox's Claude config directory (its `CLAUDE_CONFIG_DIR`, or
`~/.claude` when unset, as the session's state check reports it) to `projects/<encoded local directory>/` under the
local Claude config directory (`CLAUDE_CONFIG_DIR`, or `~/.claude` when unset), where a directory is encoded by
replacing every non-alphanumeric character with `-`. It SHALL be opened once the state check has reported the
sandbox's config directory. A transcript mirror SHALL NOT delete transcripts that exist only
locally, and SHALL be opened, leased and settled like a project mirror.

#### Scenario: Transcript lands in the configured directory
- **WHEN** an agent session runs in `/home/me/dev/api` with `CLAUDE_CONFIG_DIR=/home/me/.local/share/hermod/air/box/claude` and sandbox directory `/home/u/dev/api`, and the sandbox's Claude config directory is `/home/u/.claude`
- **THEN** transcripts from `/home/u/.claude/projects/-home-u-dev-api/` on the sandbox arrive in `/home/me/.local/share/hermod/air/box/claude/projects/-home-me-dev-api/`

#### Scenario: The sandbox's own config directory
- **WHEN** the sandbox user's profile sets `CLAUDE_CONFIG_DIR=/home/u/.config/claude`
- **THEN** the transcript mirror's sandbox end is `/home/u/.config/claude/projects/<encoded sandbox directory>/`

#### Scenario: Local-only transcript survives
- **WHEN** a transcript exists locally but not on the sandbox
- **THEN** the transcript mirror leaves it in place
