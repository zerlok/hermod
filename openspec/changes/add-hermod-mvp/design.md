## Context

Hermod is a greenfield Go CLI that composes Mutagen, SSH, and tmux into one command around a
single interactive attach. The technical source of truth for the domain model is
[`docs/ARCHITECTURE.md`](../../../docs/ARCHITECTURE.md); this document records the design
decisions that back the MVP specs and does not restate that model. The specs in this change
(`cli`, `file-sync`, `remote-session`, `git-identity`, `session-orchestration`) define the
behavior; this design covers how to structure the code so that behavior is dry-runnable and
unit-testable without touching real hosts.

Constraints: single static binary, no runtime dependencies; the external tools (Mutagen, `ssh`,
`tmux`) and the sandbox are the user's to provide; identity moves as environment, never as a git
transport.

## Goals / Non-Goals

**Goals:**
- Implement the `sync → attach → pause/teardown` loop exactly as the specs describe.
- Keep *where* a command runs orthogonal to *how* it is executed, so dry-run and tests fall out of
  the same seam.
- Make the pause-vs-teardown branch depend on one signal only: remote-session liveness.
- Guarantee the mirror is never leaked, even on error mid-attach.

**Non-Goals:**
- Shell completion generation and per-sandbox config presets (README follow-ons).
- Reimplementing or provisioning Mutagen/SSH/tmux or the sandbox.
- Choosing the final PTY/stdio mechanism for the interactive attach (see Open Questions).

## Decisions

### Decision: Two-axis model — `Shell` (where) × `Executor` (how)
Model transports as a decorator stack `LoginShell(TmuxSessionShell(SshShell(LocalShell(executor))))`,
each rewriting argv and delegating inward, per `docs/ARCHITECTURE.md`. At the leaf, `LocalShell`
holds an `Executor` that either runs argv for real (`SubprocessExecutor`) or prints it
(`DryRunExecutor`).
- **Why:** *where* and *how* compose independently. Dry-run is a leaf swap; a new transport is one
  more decorator with no change to existing objects; probes use a shorter stack than the attach.
- **Alternatives considered:** a single `Runner` that string-templates full shell commands —
  rejected as it entangles transport with execution, defeats structured dry-run, and is hard to
  unit-test; per-operation bespoke command builders — rejected as duplicative and untestable.

### Decision: `ShellFactory` injected into the orchestrator
A `ShellFactory` builds the per-operation stacks (full stack for the interactive attach, a shorter
non-interactive stack for probes like `IsActive`) and is injected into the orchestrator.
- **Why:** keeps side-effecting transports out of unit tests — tests inject a fake factory and
  assert the orchestration flow and the pause/teardown branch without real hosts.
- **Alternatives considered:** orchestrator constructs shells directly — rejected as untestable.

### Decision: Read-only probes stay real even under dry-run
`DryRunExecutor` governs side-effecting commands only. Git-identity reads and Mutagen status
queries always use the real executor.
- **Why:** a dry-run is only useful if it reflects true git identity and true sync state; a
  fully-faked dry-run would print a plan that diverges from reality.
- **Alternatives considered:** fake everything under dry-run — rejected as misleading.

### Decision: Liveness is the sole pause/teardown signal
On detach, consult `Sandbox.IsActive` and nothing else: active → `Pause`, gone → `Flush` then
`Close`.
- **Why:** *pause what you'll resume, destroy what you finished.* Exit codes and timers are noisy
  proxies for "am I coming back"; session liveness is the direct answer and keeps the branch a
  single, legible decision.
- **Alternatives considered:** keying on the remote command's exit code — rejected because a
  crashed agent inside a still-running tmux session should still pause, not tear down.

### Decision: Open the mirror inside a teardown-guaranteeing scope
`MutagenSyncSession` is opened within a scope (defer/closure) that guarantees `Close` runs if the
flow exits abnormally after open.
- **Why:** an error during flush or attach must not leak a running or paused sync session.
- **Alternatives considered:** manual close at each return site — rejected as leak-prone.

### Decision: CLI is a thin edge that emits an immutable `Options`
Parsing and (future) completion live only in the CLI layer; it produces one immutable `Options`
and hands off. Domain objects never import the CLI framework.
- **Why:** keeps the domain unit-testable in isolation and the parsing surface at the boundary.

## Risks / Trade-offs

- **Interactive attach needs real PTY/stdio inheritance, but probes only need captured output.** →
  The `Executor` seam already separates them; the `SubprocessExecutor` must support both stdio
  inheritance (attach) and captured stdout (probes). Deferred as an Open Question, not a blocker.
- **`IsActive` probe races a session that dies between detach and probe.** → Acceptable: the probe
  reflects state at decision time; a session that dies just after is handled on the next
  invocation's open (resume finds nothing → create). No data loss because teardown flushes first.
- **Resume-or-create hides which path ran.** → Surface it in logs/dry-run output so the user can
  see whether a session was created or resumed.
- **Dry-run realism depends on discipline** — any new side-effecting op must route through the
  `Executor`, not call out directly. → Enforce by making `Shell`/`Executor` the only path to
  running argv.

## Migration Plan

Greenfield: no migration. The change lands the initial `openspec/specs/` baseline plus the Go
skeleton implementing it. Rollback is deleting the change; nothing depends on it yet.

## Open Questions

- Which Go mechanism backs `SubprocessExecutor` for the interactive attach — `os/exec` with
  inherited stdio, or an explicit PTY library — given probes are satisfied by plain `os/exec`?
- Exact Mutagen CLI surface used for create/resume/flush/pause/terminate and status, and how the
  session name maps to Mutagen's session identifier.
- How `IsActive` is implemented over tmux (e.g. `tmux has-session`) through the non-interactive
  shell stack.
