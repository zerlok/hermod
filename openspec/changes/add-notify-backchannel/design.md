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

### Decision: One new `notify` package at the `shell` tier
`notify` owns the wire `Message`, the `Notifier` (interface + `linux`/`darwin`/`other` impls run
through an injected `shell.Shell`, so they are argv-recordable and shell-free — no command
injection from an untrusted body), the local `Channel` (bind/serve/dispatch), and the remote
`Send`. It imports only `shell` + stdlib (`net`, `encoding/json`), never `sandbox`/`control`. The
`Message` type and the `HERMOD_NOTIFY_SOCK` env key are defined **once** here and shared by both
ends — one source of truth for the wire.

- **Alternative — put provisioning inside `notify` (a self-contained `Channel` that runs remote
  `mkdir`/`rm`)**: rejected. It broadens the module toward mini-orchestration and duplicates what
  `control`/`mirror` already do. `control` owns the remote probe + `mkdir`, keeping `notify` a pure
  local package.

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

### Decision: Unix-domain sockets in a `0700` dir on both ends, keyed on a per-session token
`control` mints a `crypto/rand` hex token per invocation; `notify.SocketName(token)` is the shared
basename. Loopback TCP is rejected: a `-R 127.0.0.1:PORT` forward is reachable by **every** user on
a shared box, whereas a unix socket inside a `0700` dir is reachable only by the session user
(defense-in-depth beyond the `0600` socket mode). Distinct tokens give distinct filenames, so
concurrent sessions to the same host never collide. Cleanup is automatic: the socket lives only as
long as the attach ssh, and `StreamLocalBindUnlink=yes` clears any stale socket at the next bind.

- **Remote base**: one **real** probe (stays real under dry-run) for
  `${XDG_RUNTIME_DIR:-$HOME/.hermod/run}`, then a dry-run-aware `mkdir -p -m 700 <base>/hermod`
  (reusing the `ensure-remote-sync-root` pattern). **Local base**: `$XDG_RUNTIME_DIR` else
  `os.TempDir()`, `MkdirAll(…, 0700)`.

### Decision: `hermod notify` subcommand as the sender, `socat` as the zero-install fallback
The subcommand keeps the wire an internal versioned contract (encoder/decoder share the one
`Message`) and fits the single-static-binary ethos. Because the transport is a unix socket, bash
`/dev/tcp` cannot address it, so the documented fallback is
`printf '%s' '{"body":"done"}' | socat - UNIX-CONNECT:"$HERMOD_NOTIFY_SOCK"`.

### Decision: The `notify` package carries no dry-run flag; `control` decides bind-vs-note
The leaf swap remains the only dry-run seam. `control` runs the real base probe (truthful printed
address), prints the `mkdir` and the `-R` attach through the effective leaf, and under `--dry-run`
does **not** call `Channel.Listen` — it logs one honest note
(`# notify: would listen on <localSock> …`). No port-0 placeholder, no double-bind, nothing bound.

### Decision: Best-effort lifecycle owned by `control`, fenced off from `settle`
An `openNotify` helper does probe + `mkdir` + `notify.New`. `control` starts the listener before
attach (non-dry-run) and `defer`s an idempotent `stop`. Every failure — probe, `mkdir`, bind,
forward collision, accept/parse/dispatch — is logged and discarded; `Notify` returns nil
unconditionally; the flow continues as a plain session. `stopNotify` sits before the mirror
`settle`, which still keys the mirror's fate solely on session liveness.

## Risks / Trade-offs

- **`tmux -e` applies env only at session create** → on re-attach to a pre-existing window,
  `HERMOD_NOTIFY_SOCK` is whatever it was first time; a send then fails harmlessly (the tunnel
  itself always works). Same known limitation as the carried git identity. A future `tmux setenv`
  refresh would close it.
- **Sender needs the socket path in its env** → processes that scrub the environment won't have
  `HERMOD_NOTIFY_SOCK`; documented, and `hermod notify` reports it clearly.
- **Extra ssh options + one remote probe per notify-enabled run** → negligible, and only when `-N`
  is set; with `-N` off, none of this code runs.

## Open Questions

- None blocking. On-sandbox validation folded into `tasks.md`: confirm `hermod notify done` on the
  box raises a local toast, and that a bind/probe failure degrades to a plain session.
