## Context

`NewMutagenSession` (`internal/mirror/mirror.go`) opens the mirror as create-or-resume. On the
create branch it runs `mutagen sync create --name <name> <local> <host>:<remote>`. Mutagen creates
the sync **root** at `<remote>` but will not create the root's parent directories; when a
home-relative `<remote>` such as `some/thing` has a parent (`~/some`) that does not exist on the
sandbox, the sync stalls (`unable to open synchronization root parent directory`), the remote tree
is never populated, and the tmux `-c <remote>` start-directory falls back to `$HOME`.

The mirror package already runs local commands through its dry-run-aware `exec` shell, and its
`Config` carries `Host` and `RemotePath` separately. So the create branch can ensure the remote
directory exists before creating the session.

## Goals / Non-Goals

**Goals:**
- Guarantee the remote sync root (and any missing parents) exists on the host before a new Mutagen
  session is created.
- Keep provisioning a dry-run-aware side effect, printed rather than executed under dry-run.
- Fix both the stalled sync and the wrong-working-directory symptom of issue #5.

**Non-Goals:**
- Changing how the tmux start-directory is computed. Once the remote root exists, the existing
  relative `-c <remote>` resolves correctly against the SSH login `$HOME`; no attach-path change is
  needed. (Validated by test/manual check in tasks.)
- Provisioning on the resume path — the previously synced tree already exists there.
- Any conflict-resolution or sync-mode change (that is issue #4's separate change).

## Decisions

### Decision: `mkdir -p` the remote root over ssh in the create branch, via the exec shell
Before `mutagen sync create`, run `ssh <host> mkdir -p <remote>` through the same dry-run-aware
`exec` shell the create command uses. `mkdir -p` is idempotent and creates missing parents, which is
exactly the gap Mutagen leaves. Running it on `exec` (not `probe`) makes it print-only under dry-run,
consistent with every other side effect and with the "read-only probes stay real" rule (this is a
mutation, not a probe).

- **Alternative — a Mutagen flag to create parents**: Mutagen exposes no option to create the sync
  root's parents; rejected because it does not exist.
- **Alternative — provision from the sandbox package or orchestration layer**: the remote endpoint is
  the mirror's concern and the mirror already has `Host`/`RemotePath`; keeping it in
  `NewMutagenSession` colocates provisioning with the `create` it guards. The `ssh` argv is built
  directly (`["ssh", host, "mkdir", "-p", remote]`) rather than pulling in the `shell.NewSSH`
  decorator, matching how the mirror already emits plain `mutagen ...` argv.

### Decision: Provision only on create, not on resume
The mkdir runs inside the existing `st == Absent` create branch. On resume the tree was already
synced, so its root exists; re-creating it would be redundant work and muddy the resume path.

## Risks / Trade-offs

- **An extra ssh round-trip on session creation** → negligible: it happens once per new session,
  before a `mutagen sync create` that itself opens an ssh transport.
- **`mkdir -p` masks a genuinely wrong/unreachable host** → the immediately following
  `mutagen sync create` would fail on the same host anyway, surfacing the real error; the mkdir does
  not hide it.

## Open Questions

- None blocking. Confirm on the sandbox host that, with the root pre-created, the tmux `-c <remote>`
  start-directory lands in the mirrored directory (folded into tasks as a validation step).
