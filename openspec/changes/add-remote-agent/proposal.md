## Why

Hermod's sandbox-side features assume a `hermod` binary on the box. `hermod notify` is the first
one, and it lands with a documented `curl` fallback precisely because nothing installs it.

Nobody installs a helper binary on every sandbox by hand — and if they do, nobody keeps it in step
with the local one as Hermod changes. The wire between the two ends is an internal contract; an
older binary on the box is a silent mismatch, not an error anyone will notice.

Mutagen, which Hermod already depends on, solved this years ago: the CLI carries an **agent** it
installs on the remote on first use and upgrades whenever the local version moves ahead. Hermod
already opens an SSH connection to the box on every run and already probes it. Shipping its own
binary the same way turns "install hermod on the sandbox first" into nothing the user ever does.

## What Changes

- Hermod gains a **version** it knows about itself (build-stamped, reported by `hermod version` and
  `--version`), so both ends can compare what they are running.
- On attach, Hermod **probes the sandbox for its agent** — the `hermod` binary in Hermod's own
  install directory on the box — and reads its version.
- When the agent is **missing or older than the local binary**, Hermod **installs or upgrades it**
  by streaming its own executable over the existing SSH connection into a versioned path, then
  atomically pointing the agent path at it. A newer or equal agent is left alone.
- The agent's absolute path is **carried into the session** so hooks and scripts can invoke it
  without depending on the box's `PATH`; where the sandbox has a conventional user `bin` directory
  on `PATH`, Hermod also links it there so plain `hermod notify done` works.
- Bootstrapping is **best-effort and never fatal**, exactly like the notification channel: any
  failure to probe, upload, or link is logged and the session runs normally — with the `curl`
  fallback still documented for a box Hermod could not provision.
- The local binary is only uploadable to a box of the **same platform**. Hermod SHALL detect a
  mismatch and skip cleanly rather than install a binary that cannot run; serving per-platform
  agents is deliberately out of scope for this change (see Non-Goals in `design.md`).

## Capabilities

### New Capabilities

- `remote-agent`: the sandbox-side Hermod binary — version discovery, install-or-upgrade on attach,
  where it lives, how the session reaches it, and the never-fatal guarantee.

### Modified Capabilities

- `cli`: report the binary's version (`hermod version` / `--version`), and a flag to skip
  bootstrapping for a box that must not be written to.

## Impact

- **Depends on** `add-notify-backchannel` (the first consumer of a sandbox-side binary, and the
  source of the per-sandbox-user endpoint convention this change builds on).
- **New** `internal/agent/` package (sibling of `sandbox`/`notify`): version comparison, the probe,
  and the install plan.
- `internal/shell` — a `Command` needs to carry **stdin** so a binary can be streamed to
  `ssh host 'cat > …'` through the existing decorator stack; today `Command` has no stdin seam. This
  is the one structural change outside the new package.
- `internal/control/run.go` — one more best-effort step before attach, on the same footing as
  `setupNotify`: it cannot affect the flow or the pause/teardown decision.
- `Makefile` / build — stamp the version into the binary (`-ldflags -X`), with a documented
  fallback for unstamped (`go run`, `go install`) builds.
- `README.md` — the notification section's "install hermod on the box" caveat goes away.
