## Context

Hermod is a single static Go binary that runs locally and drives `mutagen + ssh + tmux`. Every run
already opens an SSH connection to the sandbox and already runs read-only probes against it (the
mirror's sync root, the runtime dir for the notification channel), then provisions what it needs
through the dry-run-aware effective leaf. `control.Run` is the composition layer; `sandbox` owns the
transport to the box; `shell` decorators rewrite argv down to a leaf that either executes or prints.

`add-notify-backchannel` introduced the first feature whose *sandbox* half is a Hermod command
(`hermod notify`), and shipped with a `curl` one-liner as the zero-install fallback — an admission
that nothing puts the binary there. This change removes the premise.

Two facts shape the design. Hermod has **no version concept at all** today: no build stamp, no
`version` command, nothing to compare. And `shell.Command` has **no stdin**: every command is argv
in, captured-or-inherited stdout out, so there is currently no way to stream ~10 MB of binary to the
far end through the decorator stack.

## Goals / Non-Goals

**Goals:**
- Never ask the user to install Hermod on a sandbox, or to remember to upgrade it.
- Idempotent and cheap: on the overwhelmingly common run — agent already current — the cost is one
  read-only probe, and nothing is written.
- Best-effort by construction, same invariant as the notification channel: no failure may affect the
  session, the mirror, or the pause/teardown decision.
- Safe on a shared box: the agent lives in the user's own directory, is never world-writable, and a
  half-finished upload can never be executed.

**Non-Goals:**
- **Cross-platform agent serving.** Mutagen embeds agent binaries for every platform. Hermod v1
  uploads only its own executable and skips when the sandbox platform differs. Fetching a matching
  release asset — locally, or on the box — is the natural follow-up.
- **A long-lived agent process (daemon).** "Agent" here means the binary, not a resident service.
  Multiplexing concurrent sessions behind one endpoint is exactly what a daemon would buy, and is
  left for a later change (see Open Questions).
- Package-manager installs, or managing a `hermod` the user installed themselves outside Hermod's
  directory.

## Decisions

### Decision: A version the binary knows about itself
`internal/version` exposes one string, stamped at build time (`-ldflags -X`) and falling back to
`debug.ReadBuildInfo()` for `go install` builds. `hermod version` and `--version` report it; the
agent reports the same way, so the comparison is between two answers to the same question.

- **Unstamped/dev builds** (`go run`, a bare `go build`) resolve to a pseudo-version that never
  compares as newer, so a developer's local build never downgrades a released agent. The escape
  hatch is an explicit force flag.
- **Alternative — hash the binary instead of versioning it**: rejected as the primary key. A content
  hash answers "is it the same build" but not "is it older", so it cannot express the review's rule
  (*upgrade when the remote is behind*), and it makes every local rebuild look like an upgrade. A
  hash is still worth carrying as a tiebreaker for equal versions.

### Decision: Probe with the agent itself, not with the filesystem
`<agentDir>/hermod version` — run through the real (never dry-run) probe shell — is the whole check.
A non-zero exit or unparseable output reads as "no usable agent", which collapses missing, corrupt,
truncated, wrong-arch, and not-executable into one branch that installs. Nothing infers from
`stat`/`test -x` what only running it can tell.

### Decision: Stream the binary over the existing SSH connection
Install is `ssh host 'cat > <agentDir>/.hermod-<version>.partial'` with the local executable
(`os.Executable`) as stdin, then `chmod 700` and `mv` into `<agentDir>/hermod-<version>`, then an
atomic `ln -sfn` of `<agentDir>/hermod` at it. The temp-then-rename means an interrupted upload can
never leave a runnable half-binary at the agent path, and the symlink swap makes the upgrade atomic
for anything about to exec it.

This requires **`shell.Command.Stdin io.Reader`** — the one change outside the new package. It fits
the existing model (decorators pass it through untouched; only the leaf consumes it) and the dry-run
leaf prints the command with a `< <local binary>` note rather than reading anything.

- **Alternative — `scp`/`rsync`**: rejected. It needs a second tool and a second connection with its
  own auth, and it would be the only place Hermod steps outside its own transport stack. Streaming
  inherits the ssh config, jump hosts, and agent forwarding the session already uses.
- **Alternative — base64 into argv**: rejected outright; argv limits are ~2 MB and the binary is
  larger by an order of magnitude.

### Decision: `~/.hermod/bin`, plus a link where `PATH` will find it
The agent lives in Hermod's own directory (`~/.hermod/bin`), never in a system path and never behind
`sudo`. Because the session's login shell decides `PATH`, Hermod additionally:

- carries the agent's absolute path into the session as `HERMOD_BIN`, so a hook can always call
  `"$HERMOD_BIN" notify done` regardless of `PATH`; and
- best-effort links `~/.local/bin/hermod` when that directory exists, which is on `PATH` by default
  on the distributions Hermod targets, so the documented `hermod notify done` just works.

Neither step is allowed to fail the run, and the link is only created when it is absent or already
points into `~/.hermod/bin` — Hermod never replaces a `hermod` the user put there themselves.

### Decision: Bootstrapping is one more best-effort step in `control`
It sits beside `setupNotify`, before attach, and returns nothing the flow depends on. Probe failure,
platform mismatch, a full disk, a read-only home — each logs one line and the session continues,
with the `curl` fallback still documented. A `--no-agent` flag skips it entirely for a box that
must not be written to.

## Risks / Trade-offs

- **Uploading a binary to a box on every version bump** → ~10 MB per upgrade, once per sandbox per
  version, on a connection already carrying a file mirror. Acceptable; the steady state is a probe.
- **Platform mismatch is common for macOS users** → a Mac driving a Linux box (a normal setup) gets
  no agent until per-platform serving lands. Mitigated by keeping the fallback documented and by
  saying so in one log line rather than failing.
- **`shell.Command` grows a field** → every leaf must decide what to do with stdin. Small, but it is
  a change to the most load-bearing type in the codebase, so it lands with tests pinning that the
  existing leaves are unaffected.
- **Trusting the sandbox's answer to `hermod version`** → the box could report anything, but the box
  is already trusted with the mirrored source and the session; the failure mode is a needless
  reinstall, not an escalation.
- **A shared sandbox account with two users' Hermods** → last upgrade wins the symlink. Versioned
  filenames mean neither breaks mid-run, and both converge on the newest.

## Open Questions

- **Per-platform agents**: bundle cross-built binaries (simple, +N × binary size), or download the
  matching release asset (needs network on one end and a trust story)? Not blocking v1, which skips
  on mismatch.
- **Does the agent eventually become a daemon?** A resident agent owning the per-user endpoint would
  let concurrent sessions to the same sandbox user share one channel — the limitation
  `add-notify-backchannel` documents. Worth revisiting once the binary is reliably present.
