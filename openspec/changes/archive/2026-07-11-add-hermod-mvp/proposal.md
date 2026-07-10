## Why

Working on a remote dev box means hand-wiring three tools every session — Mutagen to mirror
files, SSH to get a shell, tmux to survive drops — plus threading git identity through so
commits are attributed correctly, and tearing it all down cleanly on the way out. The friction
is high enough that people avoid remote sandboxes even when the sandbox is where the work
belongs. Hermod collapses that setup into one command. This change establishes the MVP: the
core loop of **sync → attach → pause/teardown**, defined as behavior before any code is written
(the repo is spec-driven).

## What Changes

- Introduce the `hermod <sandbox>` command that, in one invocation, mirrors the working
  directory to a remote sandbox, attaches an interactive session running there, and decides on
  detach whether to pause or tear the mirror down.
- Define the CLI surface: the sandbox argument as an `~/.ssh/config` host alias, session-name
  (`-s`), working-directory (`-C`), dry-run (`-n`) flags, and `--` passthrough to the remote
  command.
- Define the file mirror lifecycle over Mutagen: open (create **or** resume), flush before work
  starts, pause, and terminate.
- Define the remote interactive session over SSH + tmux: attach to or create a persistent
  window, and report whether it is still alive after detach.
- Define git-identity carry: read local `user.name`/`user.email` and export them into the
  remote session's environment, as a soft feature that never hard-fails.
- Define the orchestration flow and its central decision — the liveness of the remote session
  after detach is the single source of truth for **pause** (coming back) vs **teardown** (done).
- Define dry-run semantics, including the rule that read-only probes (git identity, sync status)
  stay real even under `--dry-run`.

Out of scope for this MVP (tracked as follow-ons in the README): shell completion generation and
per-sandbox config presets.

## Capabilities

### New Capabilities
- `cli`: Parse one invocation into a resolved, immutable intent — sandbox host alias, session
  name, working directory, agent passthrough args, ssh-config path, log level, dry-run — and hand
  off to the orchestrator.
- `file-sync`: The Mutagen file mirror between local working directory and the sandbox, with a
  full lifecycle: open (create or resume), flush, pause, and close.
- `remote-session`: The remote interactive session — a tmux window on the sandbox reached over
  SSH — that can be attached to or created, and whose liveness can be probed.
- `git-identity`: Read-only discovery of local git author identity, carried into the remote
  session's environment so remote commits are attributed to the user.
- `session-orchestration`: The end-to-end `sync → attach → pause/teardown` flow, the pause-vs-
  teardown decision keyed on remote-session liveness, and dry-run execution semantics.

### Modified Capabilities
<!-- None — this is the initial set of specs for a greenfield project. -->

## Impact

- Greenfield Go module: introduces the `hermod` binary and its domain packages (single binary,
  no runtime dependencies).
- Orchestrates external tools already on the user's machine — Mutagen, `ssh`, `tmux` — and
  reads `~/.ssh/config`; does not bundle or provision them.
- Establishes the initial `openspec/specs/` baseline that future changes will amend.
