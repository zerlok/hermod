## MODIFIED Requirements

### Requirement: Open the mirror as create-or-resume
A file relay SHALL join a local root to a remote root on one remote over a Mutagen session. Opening it SHALL create
the Mutagen session when none exists for those two ends on that remote, resume it when it is paused, and use it as
it is when another process already has it running, without the user having to distinguish the cases. The same two
ends on one remote SHALL never be joined by a second Mutagen session. Before resuming or using a session, Hermod
SHALL check that its remote endpoint is the expected remote root on the expected remote, and SHALL NOT resume or use
a session that syncs to another endpoint.

#### Scenario: No existing session
- **WHEN** a file relay is opened and no Mutagen session exists for its two ends on that remote
- **THEN** a new Mutagen session is created between the local root and the remote root

#### Scenario: Paused session exists
- **WHEN** a file relay is opened and its Mutagen session exists paused, with the expected remote endpoint
- **THEN** that session is resumed rather than re-created, so the previously synced state is reused

#### Scenario: Session already running
- **WHEN** a file relay is opened while another Hermod process has its Mutagen session running
- **THEN** that session is used as it is, and no second session is created for the same two ends

#### Scenario: Session with another endpoint
- **WHEN** a file relay is opened and the session found for it syncs to a different remote endpoint
- **THEN** that session is neither resumed nor used, and the open fails

### Requirement: Pause the mirror
When the last process using a file relay suspends its use, the relay SHALL be paused: synchronization stops, the
Mutagen session stays registered, and the changes made on either side stay on that side's disk. The next open SHALL
resume the session and flush it, so those changes are delivered before the new user works on the files.

#### Scenario: Pause preserves the session
- **WHEN** the last process using a file relay suspends its use
- **THEN** synchronization stops
- **AND** the Mutagen session remains registered so a later open resumes it

#### Scenario: Remote edits made while paused
- **WHEN** an agent edits the project on the remote while its file relay is paused, and a session then opens the
  relay
- **THEN** the edits arrive in the local working tree before that session's client is attached

### Requirement: Close the mirror
When the last process using a file relay releases it, the relay SHALL be flushed and then its Mutagen session
terminated and removed, so nothing in flight is lost and no sync session is left. A file relay that another process
still uses SHALL NOT be paused or closed.

#### Scenario: Close terminates the session
- **WHEN** the last process using a file relay releases it
- **THEN** the relay is flushed, and its Mutagen session is then terminated and removed
- **AND** no paused or running sync session for those two ends remains

#### Scenario: Another process still uses the relay
- **WHEN** a process releases a file relay that another Hermod process still uses
- **THEN** the relay keeps syncing

## ADDED Requirements

### Requirement: A file relay belongs to one remote
A file relay SHALL be identified by its remote and its two ends, and nothing else: not the worker, the agent, or
what the files are. Its Mutagen session's name SHALL carry the remote, and its labels SHALL carry the remote and the
workspace it belongs to, so one project attached to two remotes has a relay to each, and any session that needs the
same two ends on the same remote finds the same relay. Mutagen sessions made under earlier, path-only names SHALL NOT
be resumed; Hermod SHALL list them and SHALL leave terminating them to the user.

#### Scenario: Same project on two remotes
- **WHEN** one project is attached to `box-a` and then to `box-b`
- **THEN** each remote has its own file relay, named after that remote
- **AND** attaching to `box-b` never resumes the relay to `box-a`

#### Scenario: Two sessions need the same ends
- **WHEN** two sessions on one remote need the same local and remote directories joined
- **THEN** both use the one file relay for those ends

#### Scenario: Session under an earlier name
- **WHEN** a Mutagen session named after the project path alone exists
- **THEN** Hermod lists it and neither resumes nor terminates it

### Requirement: Mutagen is the registry of synced roots
Hermod SHALL read which local roots are synced to a remote, and to which remote paths, from the endpoints of the
Mutagen sessions labelled with that remote or with one of its workspaces. Hermod SHALL NOT keep a local file or
database recording file relays, workspaces or sessions.

#### Scenario: Roots listed from Mutagen
- **WHEN** Hermod needs the synced roots of a workspace
- **THEN** it lists the Mutagen sessions by the workspace's label and reads each root pair from their endpoints

### Requirement: A file relay never picks a winner
Every file relay, for the project, another directory or transcripts, SHALL be created in Mutagen's `two-way-safe`
mode, so edits made on either side reach the other and neither side's edits are discarded. A file changed on both
sides SHALL be left as it is on each and reported by Mutagen as a conflict for the developer to settle. Hermod SHALL
NOT resolve conflicts, including those between several remotes attached to one project.

#### Scenario: Remote edit reaches the working tree
- **WHEN** the agent on the remote edits a synced file
- **THEN** the edit arrives in the local working tree

#### Scenario: Local edit reaches the remote
- **WHEN** the developer edits a synced file locally
- **THEN** the edit arrives in the remote directory

#### Scenario: Conflicting edits
- **WHEN** the same file is changed locally and on the remote
- **THEN** neither change is discarded and Mutagen reports a conflict

### Requirement: A terminal conversation's transcripts travel both ways
For each distinct working directory of a conversation run on a terminal, Hermod SHALL join
`projects/<encoded remote directory>/` under Claude's config directory on the remote to
`projects/<encoded local directory>/` under the local Claude config directory (`CLAUDE_CONFIG_DIR`, or `~/.claude`
when unset), where a directory is encoded by replacing every non-alphanumeric character with `-`, so that Air's
terminal mode lists and resumes the remote's conversations from the local config directory. The relay SHALL carry
both ways: a conversation that exists only locally reaches the remote, and none is deleted for existing on one side
only. A chat SHALL get no transcript relay, because its adapter reads the remote's transcripts where they are.

#### Scenario: Transcript lands in the local config directory
- **WHEN** a terminal conversation runs in `/home/me/dev/api`, whose remote directory is `/home/u/dev/api`, and
  Claude's config directory is `/home/u/.claude` on the remote and `/home/me/.claude` locally
- **THEN** transcripts from `/home/u/.claude/projects/-home-u-dev-api/` on the remote arrive in
  `/home/me/.claude/projects/-home-me-dev-api/`

#### Scenario: Local-only transcript survives
- **WHEN** a transcript exists locally but not on the remote
- **THEN** the transcript relay leaves it in place and brings it to the remote

#### Scenario: A chat has no transcript relay
- **WHEN** Air opens a chat in a project on the remote
- **THEN** no transcript relay is made for it

### Requirement: A run ends when its file relay disappears
While a run is attached, Hermod SHALL watch the Mutagen session of every file relay it leases, and SHALL end the
attach with an error naming the relay when that session disappears, such as when it is terminated by hand.

#### Scenario: Session terminated by hand
- **WHEN** the user terminates the Mutagen session of an attached run's file relay
- **THEN** the attach ends with an error naming the relay, and the workspace is detached
