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

Each lives in one package; dependencies point downward only
(`cli → control → git/mirror/sandbox → shell`).

| Object                 | Responsibility                                                                                                                                                                                                                                      | Collaborators                      |
|------------------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|------------------------------------|
| `control.Options`      | The fully-resolved intent of one invocation: sandbox host, session name, local dir, remote dir, passthrough command, dry-run, quiet. Immutable.                                                                                                     | built by the CLI edge              |
| `sandbox.Session`      | The remote interactive session — a tmux window on `host`, reached over SSH. `NewSession` **prepares** it (wraps a leaf shell in ssh + tmux) without attaching; `Attach` blocks until detach, `IsActive` probes liveness.                            | its ssh + tmux `Shell` stack       |
| `mirror.Session`       | The file mirror between the local dir and `host:remotePath`. `NewMutagenSession` **opens** it (create **or** resume) at construction; the interface then exposes `Flush`, `Pause`, `Close` (terminate), `Status`. Backed by a private Mutagen impl. | a side-effecting + a probe `Shell` |
| `git.Git` / `Identity` | Read-only discovery of `user.name` / `user.email` from the mirrored directory. `New(shell, dir).Read()` returns an `Identity`; it does not build environment.                                                                                       | a probe `Shell`                    |
| `shell.Shell`          | **Where** a command runs (local / over ssh / inside tmux), and at the leaf **how** it runs — for real or, under dry-run, printed. A decorator the caller nests.                                                                                     | —                                  |

`control` is the composition layer: it reads identity, builds the two leaf shells (real vs
effective), wires the collaborators, runs the flow, and owns the pause/teardown decision.

---

## Two axes: *where* vs *how*

The central design idea is separating **where** a command runs from **how** it is executed.
They compose independently, which is what makes the tool both dry-runnable and testable. Both
axes live in the `shell` package: the transport decorators are *where*, and the local leaf is
*how*.

### `Shell` — where (composable decorators)

`Shell` has a single method — `Run(cmd) → Result`, where `cmd` carries the argv, cwd, env, and
a capture flag — and every transport is a decorator constructed with an inner `Shell` that it
rewrites the argv for and delegates inward:

```
NewTmux( "sess", NewSSH( "host", tty, NewLocal(dryRun) ) )
    │                 │                    │
    │                 │                    └─ leaf: runs argv on this machine (or prints it)
    │                 └─ wraps argv to run over ssh (-t for the interactive attach)
    └─ wraps argv to attach/create the persistent tmux window (-c cwd, -e env)
```

The **caller composes the stack it needs**: `sandbox.Session` nests tmux-over-ssh for the
interactive attach and a plain ssh for the `IsActive` probe; `mirror` and `git` run local leaf
shells directly. There is no shared factory — `control` builds the two leaves (a real one and a
dry-run-aware one) and hands them to the collaborators, which nest transports as they see fit.
An earlier `LoginShell` decorator was dropped: tmux's own `-c`/`-e` flags carry the cwd and
environment, and the default shell covers the no-command case.

Adding a new transport (a container exec, a different multiplexer) means writing one more
decorator constructor — no existing object changes.

### The local leaf — how (the dry-run seam)

`NewLocal(dryRun)` returns the leaf `Shell`. The `dryRun` flag picks one of two internal run
functions once, at construction, so the swap is invisible to every decorator above it:

- the **real** run executes the argv via `os/exec`, no intermediate shell. Capture mode
  collects stdout (probes); otherwise the child inherits the process stdio, so the interactive
  attach shares the real terminal and the call blocks until detach. `ssh -t` allocates the pty
  on the *remote*, so no local PTY library is needed.
- the **dry-run** run **prints** the argv as a copy-pasteable shell line and returns success.

One rule: **the probes that shape the plan stay real even in dry-run** — git-identity reads and
Mutagen `status` always use the real leaf, so `--dry-run` reflects true git identity and true
sync state (create vs resume). The tmux **liveness** probe is the exception: it decides a branch
that only matters *after* a real attach, so under dry-run — where the attach was merely printed —
it is simulated too rather than reaching out to the host.

---

## The orchestration flow

One pass, top to bottom (`control.Run(options)`):

1. read local git identity (from the mirrored directory) to carry into the remote session
2. open the mirror — `NewMutagenSession` creates or resumes it — then flush so work starts on a
   consistent tree
3. attach to the remote session (blocks while you work; edits sync both ways)
4. on return, **settle** the mirror by remote-session liveness alone

Opening happens at construction, so once `NewMutagenSession` returns there is always a live
mirror to settle. Every path after open — a clean detach, an attach error, even a failed
pre-attach flush — funnels through the single `settle` step, so the mirror is never leaked and
teardown logic lives in exactly one place.

### Pause vs teardown

The `settle` decision is the reason Hermod exists as its own tool rather than a shell alias:

- **Session still running** (or **liveness unknown**) — you are coming back, or we cannot prove
  otherwise. Tearing the mirror down would force a full re-create + re-scan next time — and
  terminating a session you simply failed to reach would be worse — so Hermod **pauses** it.
- **Session gone** — the work is done. A paused mirror left behind is a resource leak and a
  stale-sync hazard, so Hermod flushes then **terminates** it.

*Pause what you'll resume, destroy what you finished.* The liveness of the remote session is the
single source of truth; nothing else (exit codes, timers, whether the attach itself errored) is
consulted. Ambiguous liveness biases toward **pause** — the safe, non-destructive default.

---

## Git identity, carried not committed

Hermod never uses git as a transport. It only reads the local `user.name`/`user.email` — from
the *mirrored directory*, so a repo-local identity is honored — and exports them into the remote
session's environment as **both** `GIT_AUTHOR_*` and `GIT_COMMITTER_*`, so commits made *on the
remote* are fully attributed to *you* (author-only would leave the committer as the box, or fail
on a box with no identity). The `git` package returns a bare `Identity`; turning it into those
env vars is `control`'s convention, not git's. If local identity is unset, nothing is injected
and the remote's own config applies — a soft feature, never a hard failure.

---

## CLI seam

The CLI layer owns argument parsing and completion and nothing else: it collects flags and the
ambient cwd/home into functional options and calls `control.Run`, which applies them over the
defaults to produce one immutable `Options` before running the flow. The sandbox argument is an
`~/.ssh/config` **host alias** (completion is sourced from that file), and everything after `--`
passes through verbatim to the remote command.

Two resolved defaults are worth noting. The **remote path** mirrors the local directory's
position relative to home: a dir under `$HOME` keeps its home-relative path (`~/dev/api` →
`dev/api`), one outside home is slugified under home (`/opt/work/api` → `opt-work-api`); Mutagen
and tmux both treat a non-absolute remote path as home-relative. The default **session name** is
that remote path as a dash-joined slug (`dev/api` → `dev-api`), so distinct projects sharing a
base name do not collide on one tmux/Mutagen session.

Keeping parsing at the edge means the domain packages never import the CLI framework and stay
unit-testable in isolation.
