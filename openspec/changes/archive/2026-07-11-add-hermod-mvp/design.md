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
- Graceful teardown on abnormal termination (signals): no signal handling is installed, so the
  leak guarantee covers in-band errors only (see Risks).

## Decisions

### Decision: Two-axis model — `Shell` (where) × `Executor` (how)
Model transports as decorator constructors the caller nests — `NewTmux(session, NewSSH(host, tty,
NewLocal(executor)))` — each rewriting argv and delegating inward, per `docs/ARCHITECTURE.md`. At
the leaf, the local `Shell` holds an `Executor` that either runs argv for real (`NewSubprocess`)
or prints it (`NewDryRun`); both are stateless functions behind constructors, with private types.
- **Why:** *where* and *how* compose independently. Dry-run is a leaf swap; a new transport is one
  more decorator with no change to existing objects; probes nest a shorter stack than the attach.
- **Alternatives considered:** a single `Runner` that string-templates full shell commands —
  rejected as it entangles transport with execution, defeats structured dry-run, and is hard to
  unit-test; per-operation bespoke command builders — rejected as duplicative and untestable.

### Decision: The caller composes shells; there is no ShellFactory
`control` builds the two leaf shells — a real one and a dry-run-aware one — and hands them to the
collaborators. Each collaborator nests the transports it needs: `remote.NewSandboxSession` wraps a
leaf in tmux-over-ssh (attach) and plain ssh (probe); `mirror` runs local leaves directly.
- **Why:** the nesting is a per-collaborator concern, so the callee decides it; a central factory
  enumerating every stack was indirection without payoff. Testability is preserved at a higher
  seam — `control` is exercised against fake `mirror.Session`/`remote.Session` (contract-level),
  and each decorator is unit-tested against a recording leaf.
- **Alternatives considered:** a `ShellFactory` enumerating per-operation stacks (the original
  plan) — rejected as indirection; testing argv at the orchestration layer instead of contracts —
  rejected as coupling tests to command strings.

### Decision: Plan-shaping probes stay real under dry-run; the liveness probe does not
Dry-run prints side-effecting commands. Git-identity reads and Mutagen `status` use the real leaf
even under dry-run, because they shape the printed plan (identity to carry; create vs resume). The
tmux liveness probe uses the dry-run-aware leaf: it decides a branch that only matters after a
real attach, which under dry-run was merely printed.
- **Why:** a dry-run is only useful if the plan reflects true git identity and true sync state; but
  really sshing to check liveness after a *printed* attach would touch the network for a decision
  about work that never happened.
- **Alternatives considered:** all probes real (would ssh on `-n`) or all faked (plan diverges from
  reality) — both rejected.

### Decision: Liveness is the sole pause/teardown signal, applied by one `settle` step
After the attach returns — cleanly, with an error, or skipped because a pre-attach flush failed —
one `settle` step consults `remote.IsActive` and nothing else: active → `Pause`; gone → `Flush`
then `Close`; **liveness unknown → `Pause`** (the safe, non-destructive default).
- **Why:** *pause what you'll resume, destroy what you finished.* Exit codes and timers are noisy
  proxies for "am I coming back." Crucially, a session that is still alive but that we failed to
  reattach to must not be torn down — so the attach error is not consulted, only liveness. One
  `settle` keeps teardown logic in a single place.
- **Alternatives considered:** keying on the remote command's exit code — rejected (a crashed agent
  in a live tmux session should pause); a `defer`-based teardown guard duplicating the close path —
  rejected in favor of the single `settle`.

### Decision: The mirror opens at construction; `settle` guarantees no leak
`mirror.NewMutagenSession` performs the create-or-resume at construction and returns an opened
`Session`. Every path after a successful open funnels through `settle`, so the mirror is always
paused or closed and never leaked.
- **Why:** construction-opens makes "you hold a live mirror" a type-level fact, and routing all
  exits through `settle` removes the earlier `defer`/closure guard and its duplicated close.
- **Alternatives considered:** a separate `Open` call plus a deferred `Close` guard — rejected as
  more moving parts for the same guarantee; manual close at each return site — rejected as
  leak-prone.

### Decision: CLI is a thin edge; `control.Resolve` emits an immutable `Options`
Parsing and completion live only in the CLI layer; it collects flags and the ambient cwd/home and
calls `control.Resolve`, which applies functional options (`WithSession`, `WithWorkdir`, …) over
the defaults to produce one immutable `Options`. Domain packages never import the CLI framework.
- **Why:** keeps the domain unit-testable in isolation and the parsing surface at the boundary;
  functional options make resolution a pure, table-testable function with the environment injected.

### Decision: Session name and remote path are derived, not just the base name
The remote path mirrors the local directory's position relative to `$HOME` (home-relative when
under home, a full path slug when outside); the default session name is that remote path as a
dash-joined slug.
- **Why:** a bare base name (`api`) collides across distinct projects that share it on one
  tmux/Mutagen session; the path slug (`dev-api`) is unique and stable. Mutagen and tmux both
  resolve a non-absolute remote path relative to the remote home, so no `~` expansion is needed.
- **Alternatives considered:** base name only (the original spec) — rejected for collisions; an
  absolute remote path — rejected because the remote `$HOME` is unknown locally.

### Decision: `spf13/cobra` is the CLI framework
The thin CLI edge is built on cobra. The single positional arg (sandbox host alias) uses cobra's
`ValidArgsFunction` for dynamic completion sourced from `~/.ssh/config`; the `--` passthrough is
split with `cmd.ArgsLenAtDash()`; shell-completion scripts (a README follow-on) come from cobra's
built-in `completion` generation.
- **Why:** the README advertises both tab-completion of ssh hosts and completion-script generation,
  and cobra makes both first-class rather than DIY. It is the community-standard framework, which
  fits the "nothing here is a black box" ethos. cobra stays confined to the CLI edge, so the
  domain remains framework-free per the decision above.
- **Alternatives considered:** `kong` — cleaner struct→`Options` mapping, but dynamic ssh-host
  completion and completion scripts are more DIY; `urfave/cli` — middle ground, less widely known.

### Decision: standard-library `os/exec` is the only execution primitive
The subprocess executor is built on `os/exec` with no third-party execution or PTY library. The
interactive attach runs `exec.Cmd` with `Stdin`/`Stdout`/`Stderr` inherited from the process and
`Run()` blocking until detach; probes use `exec.CommandContext(...).Output()` to capture stdout.
Mutagen, `ssh`, `tmux`, and `git` are invoked as their real binaries; argv is always built as a
`[]string` run directly (never `sh -c "…"`). A nil `Env` inherits the parent environment (so the
tools resolve `PATH`/ssh-agent); extra entries are appended only when supplied.
- **Why:** `ssh -t` allocates the PTY on the *remote*, and the local terminal is already a real
  TTY that ssh shares, so inheriting stdio suffices — a local PTY library would only matter for
  spawning a child that needs its own multiplexed pty, which the attach does not. `Run()` (not
  `syscall.Exec`) is required so control returns to hermod after detach to run the liveness probe.
  Running binaries directly (no intermediate shell) removes an injection surface and matches the
  architecture's "no intermediate shell" rule. Result: zero execution dependencies and a single
  static binary with no runtime deps — the external tools remain the user's to provide.
- **Alternatives considered:** `creack/pty` for the attach — unnecessary given `ssh -t`; Go SDKs
  for Mutagen/git (e.g. go-git) — rejected as reimplementation, contradicting "composes, doesn't
  reimplement," and Mutagen has no stable public Go API (its CLI is the contract).

## Risks / Trade-offs

- **Interactive attach needs real PTY/stdio inheritance, but probes only need captured output.** →
  The `Executor` seam already separates them; the `os/exec` subprocess supports both stdio
  inheritance (attach) and captured stdout (probes). Resolved in Decisions above.
- **`IsActive` probe races a session that dies between detach and probe.** → Acceptable: the probe
  reflects state at decision time; a session that dies just after is handled on the next
  invocation's open (resume finds nothing → create). No data loss because teardown flushes first.
- **A probe with no timeout could hang if the sandbox becomes unreachable after detach.** → The
  probe passes the caller's context straight through (no artificial deadline), matching "compose,
  don't second-guess the tools"; ssh's own `ConnectTimeout` is the user's to configure. A blanket
  probe timeout was considered and removed as unrequested policy.
- **Teardown is guaranteed for in-band errors only.** → `settle` runs on every path after a
  successful open, so returned errors never leak the mirror. Abnormal termination (a signal) is out
  of scope for the MVP: no signal handling is installed, so a kill mid-run can still orphan a
  session, cleaned up by the next invocation's resume-or-terminate.
- **Resume-or-create hides which path ran.** → Surface it in the step log so the user can see
  whether a session was created or resumed.
- **Dry-run realism depends on discipline** — any new side-effecting op must route through a
  `Shell`, not call out directly. → Enforce by making `Shell`/`Executor` the only path to argv.

## Migration Plan

Greenfield: no migration. The change lands the initial `openspec/specs/` baseline plus the Go
skeleton implementing it. Rollback is deleting the change; nothing depends on it yet.

## Open Questions

All resolved during implementation. The external command surface is now pinned:

- **Mutagen:** `sync create --name <name> <localPath> <host>:<remotePath>`; `sync resume|flush|
  pause|terminate <name>`; status via `sync list <name>` (non-zero exit → Absent, so open creates;
  output containing `Paused` → Paused, else Running). The session name is the Mutagen `--name`, and
  the beta endpoint is `<host>:<remotePath>` with `remotePath` home-relative.
- **tmux:** attach-or-create via `tmux new-session -A -s <name> -c <dir> -e KEY=VAL … [command]`;
  liveness via `tmux has-session -t <name>` (exit 1 → gone; other non-zero → ambiguous → pause).
- **ssh:** the interactive attach is `ssh -t <host> '<tmux command>'`; the probe drops `-t`. The
  remote command is collapsed into one shell-quoted argument so it survives the remote shell
  re-parsing ssh's space-joined argv.

Remaining follow-ons (out of MVP scope, tracked in the README): shell-completion script generation
and per-sandbox config presets. A known limitation: `sync list` string-matching for `Paused` and
treating any `list` error as Absent are best-effort; a daemon-down `list` could misread as Absent.
