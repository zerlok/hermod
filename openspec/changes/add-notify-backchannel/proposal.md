## Why

Notifications that a remote agent is **done** or **blocked** do not reach the local desktop when
the work runs on a sandbox. The usual terminal mechanisms fail through the `tmux → ssh` path:
tmux swallows OSC 9/777 desktop-notification escapes unless passthrough is enabled, and Ubuntu's
GNOME Terminal (unpatched VTE) does not implement them at all — only a **local** process calling
the desktop notification service (libnotify / `osascript`) actually raises a toast. Hermod is
already that local process: it runs on the user's machine and bridges to the box. Nothing today
lets a process on the sandbox signal back to it.

No existing capability covers a remote→local signal channel, so this is a genuine capability gap.

## What Changes

- The sandbox session learns to open a **channel** between local and sandbox: a named endpoint pair
  the attach connection carries as a **reverse SSH forward**, for exactly as long as the attach
  lives. Callers ask for a channel by name; they never build forwards themselves.
- Hermod opens such a channel for notifications **by default**, with a `-N`/`--no-notify` opt-out.
  A short message sent to it from the sandbox reaches Hermod running locally, which raises a native
  desktop notification (toast + sound). On by default is safe because the channel is best-effort:
  where it cannot work, the cost is one log line.
- The message travels over a **unix-domain socket** inside a `0700` directory on each end (never a
  loopback TCP port, which is reachable by every user on a shared box). The endpoint is **one per
  sandbox user**, not per project, so its address is stable across every session on that box. It is
  carried into the session as `HERMOD_NOTIFY_SOCK`, riding the same `tmux -e` path that already
  carries the git identity.
- A new **`hermod notify [--title T] [--urgency low|normal|critical] <body>`** subcommand sends one
  message from the sandbox. A `socat` one-liner is documented as a zero-install fallback, so the box
  never strictly needs the Hermod binary. (Shipping the binary automatically is the separate
  `add-remote-agent` proposal.)
- The back-channel is **best-effort and never fatal**: any failure to provision, bind, or notify is
  logged and the session runs normally without notifications. Under `--dry-run` no channel is opened
  at all — nothing is probed, provisioned, or bound — and the omission is noted.

## Capabilities

### New Capabilities

- `notify-backchannel`: a default-on channel from a sandbox process to the local Hermod, which
  raises a desktop notification. Covers the opt-out, the carried socket address, best-effort
  (never-fatal) behaviour, the sender, and dry-run.

### Modified Capabilities

- `remote-session`: the session gains the local↔sandbox channel — opening an endpoint pair (one per
  sandbox user) and carrying it on the attach connection only.
- `cli`: add the `-N/--no-notify` flag and the `hermod notify` sender subcommand to the invocation
  surface. (Deltas live with the change; synced to the main specs on archive.)

## Impact

- **New** `internal/notify/` package (leaf tier: imports only `shell` + stdlib): the wire `Message`,
  the `Notifier` interface + per-OS impls (`notify-send`+`paplay` / `osascript` / no-op), `Listen`
  (bind + serve + dispatch on a socket it is handed), `Env`, and the sandbox-side `Send`. It never
  addresses or provisions an endpoint.
- `internal/shell/ssh.go` — `NewSSH` gains a variadic `SSHOption`; `WithReverseForward(spec)` adds
  the `-R` argv plus `StreamLocalBind` hardening. Existing callers compile unchanged; the probe ssh
  never carries `-R`.
- **New** `internal/sandbox/channel.go` — `Channel{Local, Remote}` plus `OpenChannel` (real runtime-
  dir probe with validation, dry-run-aware `mkdir -m 700`, address derivation); `Config.Channel`
  threaded into the interactive stack only.
- `internal/control/run.go` + `options.go` — `Options.Notify` (default on) / `WithNotify`; a
  `setupNotify` switch that opens the channel, starts the listener before attach, and stops it
  after — all non-fatal, with no addressing or provisioning detail of its own.
- `internal/cli/cli.go` — the `-N/--no-notify` flag and the `hermod notify` subcommand.
- Tests alongside each package, table-driven with argv recording and no inline expected literals.
- No change to the mirror's pause-vs-teardown decision: the back-channel sits entirely before
  `settle` and cannot affect it.
