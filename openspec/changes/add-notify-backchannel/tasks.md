## 1. notify package — wire + notifier

- [x] 1.1 Add `internal/notify/notify.go`: `Message{Title,Body,Urgency}`, `EnvSock` const (`HERMOD_NOTIFY_SOCK`), `Notifier` interface, `NewNotifier(sh shell.Shell) Notifier`, `SocketName(token) string`. Import only `shell` + stdlib.
- [x] 1.2 Add per-OS notifiers (`linuxNotifier` `notify-send --app-name=hermod --urgency=… -- <title> <body>` + best-effort `paplay`; `darwinNotifier` `osascript -e 'display notification … with title … sound name "Glass"'`; `noopNotifier`) in one file, selected by `runtime.GOOS` in `NewNotifier` (the notifiers are pure argv builders, so all compile and unit-test on any host — cleaner than build tags). All run tools through the injected `shell.Shell` with `Capture` set so their stdio never bleeds into the attach terminal; `Notify` returns nil unconditionally (best-effort).
- [x] 1.3 `urgency` maps `low|normal|critical` (default `normal`); `orDefault` fills the `hermod` title; `asAppleStr` strips control chars and quotes the AppleScript literal.

## 2. notify package — local channel + remote send

- [x] 2.1 `Channel` with `New(n Notifier, localSock, remoteSock string, log *log.Logger) *Channel`; `ReverseSpec() string` (`<remoteSock>:<localSock>`), `Env() []string` (`HERMOD_NOTIFY_SOCK=<remoteSock>`), `LocalSock() string`.
- [x] 2.2 `Listen(ctx) (stop func() error, err error)`: bind local unix socket (`0600`), accept-loop in a goroutine until ctx done; `stop` unlinks + closes, idempotent. Post-bind errors (accept/parse/dispatch) are logged and swallowed.
- [x] 2.3 `handle`: read under `io.LimitReader(conn, 8<<10)`, `json.Unmarshal`; drop on error or empty body; else `Notifier.Notify`. No error escapes.
- [x] 2.4 `Send(ctx, m Message) error`: read `EnvSock`; error clearly if unset; `net.Dial("unix", …)` and `json.NewEncoder(conn).Encode(m)`.

## 3. shell — reverse forward option

- [x] 3.1 `internal/shell/ssh.go`: add `reverse []string`, `SSHOption`, `WithReverseForward(spec)` (appends `-R <spec> -o StreamLocalBindUnlink=yes -o StreamLocalBindMask=0177`), variadic `NewSSH(inner, host, tty, opts...)`; append `s.reverse` after `-t` and before host. No `ExitOnForwardFailure`.

## 4. sandbox — thread the spec into the interactive stack only

- [x] 4.1 `internal/sandbox/sandbox.go`: add `Config.ReverseForward string`; when non-empty, pass `shell.WithReverseForward(spec)` to the interactive `NewSSH` only; the probe `NewSSH` never gets it. `sandbox` does not import `notify`.

## 5. control — wire, provision, lifecycle (all non-fatal)

- [x] 5.1 `options.go`: `Options.Notify bool` + `WithNotify(on bool) Option`.
- [x] 5.2 `run.go`: `openNotify` helper — real remote-base probe (`sh -c 'printf %s "${XDG_RUNTIME_DIR:-$HOME/.hermod/run}"'` via the **real** probe shell), dry-run-aware `mkdir -p -m 700 <base>/hermod` via the **effective** shell, resolve local base (`$XDG_RUNTIME_DIR` else `os.TempDir()`, `MkdirAll 0700`), `notify.New(notify.NewNotifier(effective), localSock, remoteSock, logger)`.
- [x] 5.3 In `Run`, when `o.Notify`: build the channel; on any error log "notifications disabled" and continue plain. Otherwise append `ch.Env()` to the session env and set `Config.ReverseForward = ch.ReverseSpec()`. Under dry-run log the listener note and do not bind; else `ch.Listen(ctx)` and `defer stop`. On `Listen` error, drop back to a plain session (empty spec, identity-only env).
- [x] 5.4 Confirm `stopNotify` is deferred, idempotent, and sits before the mirror `settle`; the pause/teardown path is untouched.

## 6. cli — flag + sender subcommand

- [x] 6.1 `cli.go`: add `-N/--notify` to the root command and pass `control.WithNotify(...)`.
- [x] 6.2 Add a `hermod notify [--title][--urgency] <body>...` subcommand that joins args into the body and calls `notify.Send`. Register it as a sibling of the root run.

## 7. Tests (table-driven, argv-recording, no inline expected literals)

- [x] 7.1 `shell/ssh_test.go`: rows for `WithReverseForward` argv order + a two-forward row + regression rows asserting probe/plain attach carry no `-R`.
- [x] 7.2 `notify` notifier argv table (linux + darwin constructed directly): urgency mapping, `hermod` title default, sound line.
- [x] 7.3 `notify` wire/dispatch table over a real in-process bound socket + fake `Notifier`: well-formed, empty-body dropped, unknown-urgency normalised, oversized dropped, malformed dropped; assert no error escapes.
- [x] 7.4 `notify` addressing table: `ReverseSpec`/`Env`/`SocketName` shapes; `Listen`+`stop` binds then unlinks (later dial refused).
- [x] 7.5 `sandbox_test.go`: `ReverseForward` set ⇒ `-R` in interactive argv, never in probe; `""` ⇒ today's argv exactly.
- [x] 7.6 `run_test.go`: `Notify:true` ⇒ captured `Config.Env` has identity + `HERMOD_NOTIFY_SOCK` and `Config.ReverseForward` well-formed; `Notify:false` ⇒ both empty; injected setup error ⇒ still reaches teardown as a plain session; `DryRun:true` ⇒ note logged, `Listen` never called.
- [x] 7.7 `cli_test.go`: `hermod notify` arg parsing (`--title/--urgency`, positional→body, unset env → error) with `notify.Send` stubbed.

## 8. Docs + validate

- [x] 8.1 Add a notification section to `README.md` (usage: `hermod prod-box -N -- claude`; on-box `hermod notify done`; the `socat` fallback) and resolve the README's forward reference to the back-channel.
- [x] 8.2 Run `make build`, `make test`, `make lint`.
- [x] 8.3 Run `openspec validate add-notify-backchannel`.
- [ ] 8.4 On the sandbox host, manually verify `hermod <host> -N` then on the box `hermod notify done` raises a local toast; and that a forced bind/probe failure degrades to a plain session.
