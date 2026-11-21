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
(`cli → control → air/claude/git/hook/mirror/sandbox`, with `sandbox → claude` the one edge between siblings; below
them only the leaves `pathmap`, `secret` and `shell`). The design of the Air bridge
([`design.md`](../openspec/changes/add-air-bridge/design.md#packages)) says what each package owns.

| Object                 | Responsibility                                                                                                                                                                                                                                      | Collaborators                      |
|------------------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|------------------------------------|
| `control.Options`      | The fully-resolved intent of one invocation: sandbox host, session name, local dir, remote dir, sandbox command, origin, dry-run, quiet, the caller's streams and environment, the local Claude config directory, and whether it stands in for Claude. Immutable. | built by the CLI edge              |
| `sandbox.Session`      | The remote interactive session — a tmux window on `host`, reached over SSH. `NewSession` **prepares** it (wraps a leaf shell in ssh + tmux) without attaching; `Attach` blocks until detach. What a new window runs is a `sandbox.Command`: the default shell, an argv, or a command line the sandbox user's shell parses. | its ssh + tmux `Shell` stack       |
| `sandbox.Agent`        | A Claude session on the sandbox keyed by its UUID: tmux session `hermod-<uuid>` and the directory `~/.hermod/s/<uuid>/` holding what the running Claude needs. Probes its state from Claude's own list, starts it (a terminal on tmux's pane, a chat on a FIFO and a log), attaches to it, looks up the directory it is tagged with, and closes it. | a side-effecting + a probe `Shell` |
| `sandbox.Liveness`     | Whether any tmux session on `host` is tagged (`@hermod-dir`) with a directory under a given root, read in one ssh call for every root a run settles. | an ssh `Shell`                     |
| `mirror.Session`       | A file mirror, of one of two kinds: a project root between the local dir and `host:remotePath` (two-way), or Claude's transcripts of one directory from the sandbox to the local Claude config directory (one-way). Named `hermod-<sandbox>-<slug>` (`…-transcripts-<local>`, where `<local>` hashes the local end) and labelled with its kind and sandbox. `NewMutagenSession` **opens** it (create **or** resume, the found session's endpoint checked first) at construction; the interface then exposes `Flush`, `Pause`, `Close` (terminate), `Status`. Backed by a private Mutagen impl. `mirror.List` reads a sandbox's mirrors back by label, and `mirror.Place` places a directory among their roots. | a side-effecting + a probe `Shell` |
| `mirror.Lease`         | One process's share in a mirror: a shared `flock` on a file named after it. `Release` reports whether the process was the last holder. | the lease directory                |
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

`Shell` has a single method — `Run(cmd) → Result`, where `cmd` carries the argv, cwd, env, a
capture flag, optional explicit stdin/stdout/stderr and an optional signal to stop it with
instead of a kill — and every transport is a decorator constructed with an inner `Shell` that it
rewrites the argv for and delegates inward:

```
NewTmux( "sess", NewSSH( "host", tty, NewLocal() or NewDryRun(plan) ) )
    │                 │                    │
    │                 │                    └─ leaf: runs argv on this machine (or prints it)
    │                 └─ wraps argv to run over ssh (-t for the interactive attach)
    └─ wraps argv to attach/create the persistent tmux window (-c cwd, -e env)
```

The **caller composes the stack it needs**: `sandbox.Session` nests tmux-over-ssh for the
interactive attach, `sandbox.Liveness` a plain ssh for its probe; `mirror` and `git` run local leaf
shells directly. There is no shared factory — `control` builds the two leaves (a real one and a
dry-run-aware one) and hands them to the collaborators, which nest transports as they see fit.
tmux's own `-c`/`-e` flags carry the cwd and environment; a `LoginShell` decorator outside the
tmux layer runs an explicit command under the sandbox user's login shell, so it sees the same
`PATH` as the default shell does.

Adding a new transport (a container exec, a different multiplexer) means writing one more
decorator constructor — no existing object changes.

### The local leaf — how (the dry-run seam)

`NewLocal()` and `NewDryRun(plan)` return the two leaf `Shell`s. The choice is made once, at
construction, so the swap is invisible to every decorator above it:

- the **real** leaf executes the argv via `os/exec`, no intermediate shell. Capture mode
  collects stdout and stderr (probes); otherwise the child runs with the streams the command
  gives, else the process stdio, so the interactive attach shares the real terminal and the call
  blocks until detach. `ssh -t` allocates the pty on the *remote*, so no local PTY library is
  needed.
- the **dry-run** leaf **prints** the argv to `plan` as a copy-pasteable shell line and returns
  success. `plan` is stdout, or stderr when stdout is the caller's: when Hermod stands in for
  Claude, and in a chat session, whose stream is stdout.

One rule: **the probes that shape the plan stay real even in dry-run** — git-identity reads and
Mutagen `status` always use the real leaf, so `--dry-run` reflects true git identity and true
sync state (create vs resume). The tmux **liveness** probe is the exception: it decides a branch
that only matters *after* a real attach, so under dry-run — where the attach was merely printed —
it is not run, and the plan settles each mirror with the pause a session still running would get,
saying so in the log.

---

## The orchestration flow

`control.Run(options)` first classifies the command (`claude.Classify`, per the
[Air bridge design](../openspec/changes/add-air-bridge/design.md#classifying-the-command)): a
Claude utility call runs straight on the sandbox with the caller's stdio and exit code, a
terminal or chat session runs the flow below with the
[agent-session steps](../openspec/changes/add-air-bridge/design.md#agent-session) between the
flush and the attach (a chat session attaches as a stream of lines, its paths mapped each way,
rather than on a tty), and everything else that is not an agent session takes the flow below.
One pass, top to bottom:

1. read local git identity (from the mirrored directory) to carry into the remote session
2. place the directory among the sandbox's mirrored roots, listed from Mutagen by label: inside a
   root it reuses that root's mirror and runs at the corresponding sandbox path, outside every
   root it becomes a root of its own
3. take a lease on the mirror, open it — `NewMutagenSession` creates or resumes it — then flush so
   work starts on a consistent tree (an agent session adds its transcript mirror the same way)
4. attach to the remote session (blocks while you work; edits sync both ways)
5. on return, release the leases and **settle** each mirror this run held last, by the
   liveness of its root alone

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

*Pause what you'll resume, destroy what you finished.* The liveness of the mirror's root is the
single source of truth; nothing else (exit codes, timers, whether the attach itself errored) is
consulted. Ambiguous liveness biases toward **pause** — the safe, non-destructive default.

A mirror belongs to a root, not to one session, so "still running" means any tmux session on the
sandbox tagged with a directory under that root (`@hermod-dir`, which every session Hermod
starts carries), whether a plain attach or an agent session, and whoever started it. A
transcript mirror's root is its session directory. A run settles by this rule however it ended,
an agent session that started or attached nothing included: liveness already covers every other
session under the root. The one exception is a hang-up that failed, by a close or by a reattach
that closes the session to resume it: its Claude may outlive its tmux session, which liveness
cannot see, so its mirrors are paused.

### Leases: the last one out settles

Several runs can use one mirror at once: two Claude sessions in one project, or a terminal beside
an Air chat. Each run holds a shared `flock` on `$XDG_RUNTIME_DIR/hermod/lease/<mirror>` (under
the cache directory where there is no runtime directory) from before it opens the mirror until
it has settled it. On the way out it tries a non-blocking exclusive lock: only the last holder
gets it, and only that run settles the mirror; the others leave it running. A run that comes
while the last holder settles waits for its lease, so it never opens a mirror being closed. The
kernel drops a killed process's lock, so no mirror stays leased by a process that is gone. A dry
run takes no lease and plans the settle as the only holder. The reasons are in the
[design](../openspec/changes/add-air-bridge/design.md#leases-are-file-locks).

### Wrapping Air's ACP adapter

`control.RunACP`, behind `hermod air acp claude`, is the one long-lived process of chat mode: it
runs the ACP adapter in Air's place for as long as the IDE runs. The adapter inherits Hermod's
environment untouched, its stdout is Air's stdout, and Air's stdin reaches it through a pipe,
copied one line at a time and byte for byte. When Hermod is asked to stop it passes SIGTERM on
rather than killing the adapter, which then ends the chats' stand-ins itself.

Each line Air sends is checked for `_session/archive` and `_session/unarchive` once the adapter
has it (`air.Marked`). A chat marked done is queued for a close, run one at a time apart from the
copy, and finished before Hermod exits even when it was asked to stop. A chat reopened drops its
queued close, and a close under way for it leaves the session running. A close
([design](../openspec/changes/add-air-bridge/design.md#close)) reads the directory the session's
tmux session is tagged with (`sandbox.Agent.Tagged`), picks the mirrors serving it from Mutagen's
listing (`mirror.Serving`), opens them under leases, hangs up on Claude with
`tmux kill-session`, and settles the mirrors by the same rule and code as a detach. A Claude that
outlives the hang-up leaves its mirrors paused. A session with no tmux session has nothing to
close and no tag to name its mirrors, so they stay as the last detach left them; an untagged one
has no mirrors to settle.

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

The CLI layer owns argument parsing and completion and nothing else. Started under the name
`claude`, the binary is the [stand-in](../openspec/changes/add-air-bridge/design.md#the-stand-in-hermod-named-claude)
and runs `attach -- claude ARGS…`; under any other name, `hermod attach` collects
flags, the ambient cwd/home/hostname and its environment into functional options and calls
`control.Run`, which applies them over the defaults to produce one immutable `Options` before
running the flow. It is the one place an invocation is resolved, and an argument always wins
over the environment: the sandbox is the argument, else `HERMOD_SANDBOX`, and with neither the
command fails before anything runs; the command is everything after `--`, else
`HERMOD_SANDBOX_CMD` as a command line for the sandbox user's shell, else that shell. The
sandbox must not start with `-`, which ssh would read as an option. The sandbox is an
`~/.ssh/config` **host alias** (completion is sourced from that file), and an argv after `--`
passes through verbatim.

Two resolved defaults are worth noting. The **remote path** mirrors the local directory's
position relative to home: a dir under `$HOME` keeps its home-relative path (`~/dev/api` →
`dev/api`), one outside home is slugified under home (`/opt/work/api` → `opt-work-api`); Mutagen
and tmux both treat a non-absolute remote path as home-relative. The default **session name** is
that remote path as a dash-joined slug (`dev/api` → `dev-api`), so distinct projects sharing a
base name do not collide on one tmux session.

The **mirror** is not named by `-s`: it belongs to the mirrored directory and the sandbox, as
`hermod-<sandbox>-<slug>` with every character Mutagen rejects turned into a dash, so the same
directory attached to two sandboxes is two mirrors. A session found under that name is resumed
only when it syncs to the expected `host:remotePath`. The mirror is created `two-way-safe`, so
neither side's edits are discarded and a file changed on both sides is a conflict for the
developer. Other Mutagen sessions of the same directory, under names Hermod does not give, are
listed when the mirror is created and left alone. Every mirror is labelled `hermod-kind` and
`hermod-sandbox`, which is how a run finds the roots already mirrored to its sandbox: Mutagen is
the registry, and Hermod keeps no file of its own. The reasons are in the
[`add-air-bridge` design](../openspec/changes/add-air-bridge/design.md#mirror-names-carry-the-sandbox).

`hermod air configure claude [SANDBOX]` resolves its sandbox the same way and finds `node` and
the ACP adapter on `PATH`, since Air does not load the user's shell; `control.Configure` then
creates the stand-in link and merges the sandbox's entry into Air's `acp.json`, both owned by
the `air` package (see the
[design](../openspec/changes/add-air-bridge/design.md#hermod-air-configure-claude-sandbox)). The
entry runs `hermod air acp claude --node=… --acp=…`, which resolves its sandbox the same way and
hands `control.RunACP` the adapter to run and the streams that carry ACP.

Keeping parsing at the edge means the domain packages never import the CLI framework and stay
unit-testable in isolation.
