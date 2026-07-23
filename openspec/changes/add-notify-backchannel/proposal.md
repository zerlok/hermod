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

- A new **opt-in** `--notify` (`-N`) flag. When set, Hermod opens a **reverse SSH forward** on the
  interactive attach connection so a process on the sandbox can send a short message back to Hermod
  running locally, which raises a native desktop notification (toast + sound).
- The message travels over a **per-session unix-domain socket** inside a `0700` directory on each
  end (never a loopback TCP port, which is reachable by every user on a shared box). The remote
  socket path is carried into the session as `HERMOD_NOTIFY_SOCK`, riding the same `tmux -e` path
  that already carries the git identity.
- A new **`hermod notify [--title T] [--urgency low|normal|critical] <body>`** subcommand sends one
  message from the sandbox. A `socat` one-liner is documented as a zero-install fallback, so the box
  never strictly needs the Hermod binary.
- The back-channel is **best-effort and never fatal**: any failure to provision, bind, or notify is
  logged and the session runs normally without notifications. Under `--dry-run` the tunnel, the
  remote `mkdir -m 700`, and the `HERMOD_NOTIFY_SOCK` env are printed; nothing is bound.

## Capabilities

### New Capabilities

- `notify-backchannel`: an opt-in reverse-SSH channel from a sandbox process to the local Hermod,
  which raises a desktop notification. Covers enabling, the carried socket address, best-effort
  (never-fatal) behaviour, and dry-run.

### Modified Capabilities

- `cli`: add the `-N/--notify` flag and the `hermod notify` sender subcommand to the invocation
  surface. (Delta lives with the change; synced to the main `cli` spec on archive.)

## Impact

- **New** `internal/notify/` package (leaf tier: imports only `shell` + stdlib): the wire `Message`,
  the `Notifier` interface + per-OS impls (`notify-send`+`paplay` / `osascript` / no-op), the local
  `Channel` (bind + serve + dispatch), and the remote `Send`.
- `internal/shell/ssh.go` — `NewSSH` gains a variadic `SSHOption`; `WithReverseForward(spec)` adds
  the `-R` argv plus `StreamLocalBind` hardening. Existing callers compile unchanged; the probe ssh
  never carries `-R`.
- `internal/sandbox/sandbox.go` — `Config.ReverseForward` (opaque `-R` spec) threaded into the
  interactive stack only.
- `internal/control/run.go` + `options.go` — `Options.Notify` / `WithNotify`; an `openNotify`
  helper (real remote-base probe, dry-run-aware `mkdir -m 700`, build the `Channel`); start the
  listener before attach and stop it after, all non-fatal.
- `internal/cli/cli.go` — the `-N/--notify` flag and the `hermod notify` subcommand.
- Tests alongside each package, table-driven with argv recording and no inline expected literals.
- No change to the mirror's pause-vs-teardown decision: the back-channel sits entirely before
  `settle` and cannot affect it.
