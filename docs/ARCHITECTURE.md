# Hermod — Architecture

This document is the technical source of truth for **how Hermod works and why**. The README
covers *what it does for the user*; this covers the domain model, the command-composition
design, and the one decision that shapes the whole flow — what happens when you detach.

Hermod is small on purpose. It **composes** Mutagen, SSH and tmux into one command and manages their lifecycle around a
single interactive attach. Almost all of the design is about keeping that composition legible and safe to dry-run.

---

## The one-sentence model

> Open a **file mirror** to the remote, run one **interactive session** inside a remote window, and — depending on
> whether that session outlived your detach — **pause** or **tear down** the mirror.

Everything below is in service of that sentence.

---

## Domain objects

| Object               | Responsibility                                                                                                                                      | Collaborators          |
|----------------------|-----------------------------------------------------------------------------------------------------------------------------------------------------|------------------------|
| `Options`            | The fully-resolved intent of one invocation: session name, sandbox host, cwd, agent args, ssh-config path, log level, dry-run. Immutable.           | built by the CLI layer |
| `Sandbox`            | The remote interactive session — a tmux window on `host`, reached over SSH. Knows how to `Attach` and to report whether it `IsActive`.              | `Shell` stack          |
| `MutagenSyncSession` | The file mirror between local cwd and `host:remote`. Owns the full lifecycle: `Open` (create **or** resume), `Flush`, `Pause`, `Close` (terminate). | local `Shell`          |
| `Git`                | Read-only discovery of the local `user.name` / `user.email`, surfaced as `GitUserInfo`.                                                             | local `Shell`          |
| `Shell`              | **Where** a command runs (local / over ssh / inside tmux / under a login shell). A composable decorator stack.                                      | `Executor` at the leaf |
| `Executor`           | **How** a single argv is run: for real (`Subprocess`) or printed (`DryRun`).                                                                        | —                      |

---

## Two axes: *where* vs *how*

The central design idea is separating **where** a command runs from **how** it is executed.
They compose independently, which is what makes the tool both dry-runnable and testable.

### `Shell` — where (composable decorators)

`Shell` has a single method — `Run(argv…, cwd, env, capture) → Result` — and every transport is
a decorator that rewrites the argv and delegates inward:

```
LoginShell( TmuxSessionShell( SshShell( LocalShell(executor) ) ) )
    │             │                 │            │
    │             │                 │            └─ leaf: actually runs argv on this machine
    │             │                 └─ wraps argv to run over ssh
    │             └─ wraps argv to attach/create the persistent session
    └─ wraps argv in a login shell at the right cwd
```

Different operations use different stacks: the interactive attach needs the full one, while
probes (like `IsActive`) use a shorter, non-interactive one. A `ShellFactory` builds the
stacks and is injected into the orchestrator, keeping the side-effecting transports out of
unit tests.

Adding a new transport (a container exec, a different multiplexer) means writing one more
decorator — no existing object changes.

### `Executor` — how (the dry-run seam)

At the leaf, `LocalShell` holds an `Executor`:

- `SubprocessExecutor` runs the argv for real (no intermediate shell), optionally capturing stdout.
  *(TODO: pick the Go implementation — plain `os/exec` covers probes, but the interactive attach
  needs real stdio/PTY inheritance.)*
- `DryRunExecutor` **prints** the argv as a copy-pasteable shell line and returns success.

One rule: **read-only probes stay real even in dry-run** — `Git` config reads and Mutagen
status queries always use the real executor, so `--dry-run` reflects true git identity and
true sync state.

---

## The orchestration flow

One pass, top to bottom (`Run(options)`):

1. read local git identity (if set) to carry into the remote session
2. open the mirror — create or resume, then flush so the session starts on a consistent tree
3. attach to the remote session (blocks while you work; edits sync both ways)
4. on detach, decide: session still alive → **pause** the mirror; session gone → **close** it
   (flushing first, so nothing in flight is lost)

The mirror is opened inside a scope that guarantees teardown, so an error during attach still
closes it rather than leaking a sync session.

### Pause vs teardown

The decision in step 4 is the reason Hermod exists as its own tool rather than a shell alias:

- **Session still running** — you are coming back. Tearing the mirror down would force a full
  re-create + re-scan next time, so Hermod **pauses** it.
- **Session gone** — the work is done. A paused mirror left behind is a resource leak and a
  stale-sync hazard, so Hermod **terminates** it.

*Pause what you'll resume, destroy what you finished.* The liveness of the remote session is
the single source of truth for this branch; nothing else (exit codes, timers) is consulted.

---

## Git identity, carried not committed

Hermod never uses git as a transport. It only reads the local `user.name`/`user.email` and
exports them into the remote session's environment, so commits made *on the remote* are
attributed to *you*. If local identity is unset, nothing is injected and the remote's own
config applies — a soft feature, never a hard failure.

---

## CLI seam

The CLI layer owns argument parsing and completion generation and nothing else: it turns flags
into an `Options` and hands off to the orchestrator. The sandbox argument is an `~/.ssh/config`
**host alias** (completion is sourced from that file), and everything after `--` passes through
verbatim to the remote command. Keeping parsing at the edge means the domain objects never
import the CLI framework and stay unit-testable in isolation.
