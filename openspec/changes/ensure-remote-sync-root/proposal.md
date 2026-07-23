## Why

Mutagen creates a synchronization root but **not** the root's parent directories. When the
home-relative remote path has more than one segment (the common case — `some/thing`, `dev/api`)
and the parent (`~/some`) does not already exist on the sandbox, Mutagen cannot stage the root and
stalls with a transition problem:

```
Beta:  <host>:some/thing
    Synchronizable contents: 0 directories
    Transition problems:
        <root>: unable to walk to transition root parent: unable to open
        synchronization root parent directory: no such file or directory
```

The remote tree is then never created, so nothing mirrors to the sandbox, and the tmux session's
`-c some/thing` start-directory has nothing to enter and silently falls back to `$HOME` — the user
lands in the wrong directory with no files synced. Reported as issue #5 ("hermod doesn't create new
dir on remote"). No existing requirement covers provisioning the remote sync root, so this is a
genuine spec gap, not a code defect against an existing contract.

## What Changes

- When opening the mirror and a new Mutagen session must be created, hermod SHALL first ensure the
  remote sync root directory (including any missing parent directories) exists on the sandbox host,
  before `mutagen sync create` runs.
- Provisioning is a dry-run-aware side effect: under dry-run it is printed as a copy-pasteable shell
  line and not executed, consistent with every other side effect.

## Capabilities

### New Capabilities

<!-- none -->

### Modified Capabilities

- `file-sync`: add a requirement that opening the mirror provisions the remote sync root (creating
  parent directories Mutagen will not) before creating the Mutagen session, so the initial sync can
  complete. This is a new requirement filling a gap, not a change to existing create/resume behavior.

## Impact

- `internal/mirror/mirror.go` — `NewMutagenSession` create branch runs a remote `mkdir -p` of the
  remote path (over `ssh <host>`) via the dry-run-aware exec shell before `mutagen sync create`.
- `internal/mirror/mirror_test.go` — assert the create path issues the remote `mkdir -p` before
  `mutagen sync create`, and the resume path does not.
- No new flag or user-facing option. Fixes both the stalled sync and the wrong-working-directory
  symptom (once the root exists, the tmux `-c` start-directory resolves correctly).
