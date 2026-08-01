## Context

Hermod runs locally and composes `mutagen + ssh + tmux` around a single interactive attach
(`control.Run`: read identity → open mirror → flush → attach → settle). The `shell` package is a
stack of argv-rewriting decorators (`ssh`, `tmux`, `loginShell`) over a local leaf whose
`NewLocal(dryRun)` either executes or **prints** — that leaf swap is the only dry-run seam. The
interactive attach ssh is built in `sandbox.NewSession` as
`NewLoginShell(NewTmux(NewSSH(inner, host, tty=true), session))`; a separate plain `NewSSH(...,
false)` serves the liveness probe. Session environment (the git identity) is carried in via
`sandbox.Config.Env → tmux -e`. A read-only probe stays real even under dry-run; side effects go
through the dry-run-aware leaf.

A desktop notification can only be raised by a process on the machine with the display. Hermod is
that process. The gap is a transport from a sandbox process back to it. This design adds one.

## Goals / Non-Goals

**Goals:**
- An opt-in remote→local signal channel that raises a native desktop notification (toast + sound).
- Reuse the existing decorator + dry-run + downward-layering model; add exactly one leaf-tier
  package and the minimum wiring.
- Best-effort by construction: no failure of the channel may affect the session or the
  pause/teardown decision.
- Secure on a shared sandbox: only the session user can reach the channel endpoint.

**Non-Goals:**
- Agent state *detection* (blocked/working/done). Hermod does not parse panes; the sandbox process
  decides when to send. (That is a multiplexer's job — see the README positioning.)
- Refreshing the carried address on re-attach to a pre-existing tmux window (see Open Questions).
- A persistent/standalone tunnel decoupled from the attach (documented as a future option, not
  built).

## Decisions

The full decided design (with Go signatures and exact argv) is the single source of truth and lives
in the judge-panel synthesis captured in `tasks.md`'s references; the load-bearing decisions:

### Decision: `sandbox` owns the channel; `notify` owns what travels over it
The two concerns split along the layering, and `control` wires them without knowing either one's
mechanics:

- **`sandbox.Channel` / `OpenChannel`** — the transport between local and sandbox. `sandbox`
  already owns the ssh stack and the host, so it is the natural owner of an endpoint pair and of
  the forward that carries it. `Config.Channel` replaces an opaque `-R` string: the session is told
  *what channel to carry*, not *what ssh flag to build*. Provisioning (runtime-dir probe,
  `mkdir -m 700`, address derivation) lives here, next to the other remote-provisioning it does.
- **`notify`** — the wire `Message`, the `Notifier` (interface + `linux`/`darwin`/`other` impls run
  through an injected `shell.Shell`, so they are argv-recordable and shell-free — no command
  injection from an untrusted body), `Listen` on a socket it is *handed*, and `Send`. It imports
  only `shell` + stdlib, never `sandbox`/`control`, and never runs a remote command. The `Message`
  type and the `HERMOD_NOTIFY_SOCK` key are defined **once** here and shared by both ends.

Neither package imports the other; `control` names the channel (`notify.ChannelName`), asks
`sandbox` for it, and hands its local end to `notify.Listen`.

- **Alternative — `notify` owns provisioning (a self-contained `Channel` that runs remote
  `mkdir`)**: rejected. It broadens a leaf package toward mini-orchestration.
- **Alternative — `control` owns provisioning** (as first built): rejected on review. It made the
  runner carry socket-addressing specifics that are the session's business, and left `sandbox`
  taking an opaque spec it could not reason about.

### Decision: The `-R` reverse forward rides the existing attach ssh
`NewSSH` gains a variadic `SSHOption`; `WithReverseForward(spec)` appends
`-R <spec> -o StreamLocalBindUnlink=yes -o StreamLocalBindMask=0177`. The tunnel is up exactly while
the attach connection is, and dies on detach — no second process, no separate lifecycle, no remote
`rm`. Existing callers pass no options and are unchanged; the probe ssh never gets `-R`.

- **No `ExitOnForwardFailure=yes`**: it would kill the whole session on a stale-socket bind refusal.
  It is safe *only* on a dedicated disposable `ssh -N` tunnel — a rule worth recording. Here a bind
  refusal just means notifications silently don't arrive.
- **Alternative — a separate `ssh -N -T` tunnel** (`WithoutRemoteCommand`): kept as a documented
  future option for deployments that must decouple tunnel lifetime from the attach; not built,
  because it doubles the lifecycle for no benefit on single-user sandboxes.

### Decision: Unix-domain sockets in a `0700` dir on both ends, one endpoint per sandbox user
The sandbox end is `${XDG_RUNTIME_DIR:-$HOME/.hermod/run}/hermod/<name>.sock` — **per user, not per
project**. A stable address is what a sender can rely on: every project on the box reaches its own
user's socket at the same path, and it is the address a future long-lived agent would own. Loopback
TCP is rejected: a `-R 127.0.0.1:PORT` forward is reachable by **every** user on a shared box,
whereas a unix socket inside a `0700` dir is reachable only by the session user (defense-in-depth
beyond the `0600` socket mode). Cleanup is automatic: the socket lives only as long as the attach
ssh, and `StreamLocalBindUnlink=yes` clears any stale socket at the next bind.

- **Sandbox base**: one **real** probe (stays real under dry-run) for
  `${XDG_RUNTIME_DIR:-$HOME/.hermod/run}`, validated as a clean absolute path, then a dry-run-aware
  `mkdir -p -m 700 <base>/hermod` (reusing the `ensure-remote-sync-root` pattern).
- **Local end**: `$XDG_RUNTIME_DIR` else `os.TempDir()`, then `hermod/<host>/<name>.sock` — keyed by
  host so sessions to different sandboxes never collide locally.
- **Alternative — a `crypto/rand` token per session** (as first built): rejected on review. It made
  every session's address different, which no sender can predict and which a per-user agent could
  never own. The cost is that two concurrent sessions to the *same* sandbox user share one endpoint
  (see Risks).

### Decision: `hermod notify` subcommand as the sender, `socat` as the zero-install fallback
The subcommand keeps the wire an internal versioned contract (encoder/decoder share the one
`Message`) and fits the single-static-binary ethos. Because the transport is a unix socket, bash
`/dev/tcp` cannot address it, so the documented fallback is
`printf '%s' '{"body":"done"}' | socat - UNIX-CONNECT:"$HERMOD_NOTIFY_SOCK"`.

### Decision: On by default, with `-N`/`--no-notify` to opt out
`control.Run` defaults `Options.Notify` on. The feature only earns its keep if it is already there
when the run turns out to be a long one — a flag you must remember beforehand is a flag you use
after the run you wanted it for — and the best-effort guarantee makes the default safe: where it
cannot work, the entire cost is one log line.

### Decision: Dry-run opens no channel at all
The leaf swap remains the only dry-run seam, but the channel is not put through it: a dry run does
not probe, does not `mkdir`, does not bind, and plans a session with no channel. It logs one honest
note that the back-channel was skipped, so the plan is not misread as complete. Printing a *plan*
for the channel would mean running a real probe against the host and deriving an address the run
will never use — real cost for a line the user cannot copy-paste to any effect.

- **Alternative — print the plan** (as first built: real probe, printed `mkdir`, printed `-R`, and a
  "would listen" note): rejected on review. A dry run should touch the sandbox as little as it can.

### Decision: Best-effort lifecycle owned by `control`, fenced off from `settle`
`setupNotify` is a switch and nothing more: on, it asks `sandbox` for the channel and hands its
local end to `notify.Listen`; off, under dry-run, or on any failure it returns the plain session
(base env, zero channel, no-op stop). Every failure — probe, `mkdir`, bind, forward collision,
accept/parse/dispatch — is logged and discarded; `Notify` returns nil unconditionally; the flow
continues as a plain session. `stopNotify` is deferred before the mirror `settle`, which still keys
the mirror's fate solely on session liveness.

## Risks / Trade-offs

- **`tmux -e` applies env only at session create** → on re-attach to a pre-existing window,
  `HERMOD_NOTIFY_SOCK` is whatever it was first time; a send then fails harmlessly (the tunnel
  itself always works). Same known limitation as the carried git identity. A future `tmux setenv`
  refresh would close it.
- **Sender needs the socket path in its env** → processes that scrub the environment won't have
  `HERMOD_NOTIFY_SOCK`; documented, and `hermod notify` reports it clearly.
- **Two concurrent sessions to the same sandbox user share one endpoint** → the direct cost of a
  per-user address. `StreamLocalBindUnlink=yes` means the most recent attach owns the socket, and
  the earlier session's notifications stop arriving (it is never broken otherwise). Accepted: the
  common case is one box, one user, one attach at a time, and `add-remote-agent` resolves it
  properly by putting a long-lived agent behind that one address.
- **Extra ssh options + one probe per run** → negligible, but it now happens on every default run
  rather than only under a flag; `--no-notify` and `--dry-run` skip it entirely.
- **`notify` is a reserved subcommand name** → because `hermod notify …` is the sender subcommand,
  a sandbox host aliased literally `notify` cannot be reached as `hermod notify`; cobra routes the
  word to the subcommand. This is inherent to giving the sender a verb, and `notify` is by far the
  most natural one; treated as a known, documented limitation rather than reworked into a `run`
  subcommand that would spoil the primary `hermod <host>` ergonomics.
- **Runtime-dir probe output is validated before use** → the probe result is spliced into the
  `ssh -R <remote>:<local>` spec as a raw argv element, where ssh's own forward parser splits on
  `:`. `probeRuntimeDir` therefore rejects a base that is not a clean absolute path (no `:` or
  whitespace), refusing the channel, so a surprising sandbox environment cannot alter the forward's
  meaning.
- **The sandbox needs the `hermod` binary to use `hermod notify`** → the `socat` fallback covers a
  bare box, but the ergonomic answer is `add-remote-agent`, which ships and upgrades the binary on
  attach the way Mutagen does.

## Open Questions

- None blocking. On-sandbox validation folded into `tasks.md`: confirm `hermod notify done` on the
  box raises a local toast, and that a bind/probe failure degrades to a plain session.
