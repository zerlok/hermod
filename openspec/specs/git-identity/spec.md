# git-identity Specification

## Purpose

Defines how Hermod reads local git identity and carries it into the remote session's environment so remote commits are attributed to the user, treating a missing identity as a soft, non-fatal case.

## Requirements

### Requirement: Read local git identity
Hermod SHALL read the local git `user.name` and `user.email` before opening the remote session,
using a read-only discovery that never writes git config. The discovered identity MAY be
complete, partial, or absent.

#### Scenario: Identity configured locally
- **WHEN** the local git config has both `user.name` and `user.email` set
- **THEN** both values are read without modifying any git config

#### Scenario: Identity not configured
- **WHEN** the local git config has no `user.name` or `user.email`
- **THEN** the discovered identity is empty and this is not treated as an error

### Requirement: Carry identity into the remote session
When local git identity is present, Hermod SHALL export it into the remote session's environment
as `GIT_AUTHOR_NAME` and `GIT_AUTHOR_EMAIL`, so commits made on the remote are attributed to the
user. Git SHALL be used only as identity carried through environment, never as a transport for
files between hosts.

#### Scenario: Identity exported to remote
- **WHEN** local identity is present and the remote session starts
- **THEN** `GIT_AUTHOR_NAME` and `GIT_AUTHOR_EMAIL` are exported into the remote session's environment

#### Scenario: Commit on remote is attributed to the user
- **WHEN** the user makes a commit inside the remote session with identity carried in
- **THEN** the commit is attributed to the user's local name and email rather than the box's config

### Requirement: Identity carry is a soft feature
Missing local git identity SHALL NEVER be a hard failure. When identity is unset, Hermod SHALL
inject nothing and allow the remote's own git config to apply.

#### Scenario: Unset identity injects nothing
- **WHEN** local git identity is unset and the remote session starts
- **THEN** no `GIT_AUTHOR_NAME`/`GIT_AUTHOR_EMAIL` is injected
- **AND** the flow proceeds normally, deferring to the remote's own git config

### Requirement: Identity read stays real under dry-run
Reading local git identity SHALL be a read-only probe that runs for real even when the invocation
is a dry-run, so a dry-run reflects the true git identity that would be carried.

#### Scenario: Dry-run reads true identity
- **WHEN** the invocation is a dry-run and local identity is set
- **THEN** the identity read executes for real
- **AND** the dry-run output reflects the actual name and email that would be exported
