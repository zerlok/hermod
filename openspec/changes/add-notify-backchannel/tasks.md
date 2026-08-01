## 1. notify package — wire + notifier

- [x] 1.1 Add `internal/notify/notify.go`: `Message{Title,Body,Urgency}`, `EnvSock` const (`HERMOD_NOTIFY_SOCK`), `ChannelName` const, `Env(sock) []string`, `Notifier` interface, `NewNotifier(sh shell.Shell) Notifier`. Import only `shell` + stdlib; never address or provision an endpoint.
- [x] 1.2 Add per-OS notifiers (`linuxNotifier` `notify-send --app-name=hermod --urgency=… -- <title> <body>` + best-effort `paplay`; `darwinNotifier` `osascript -e 'display notification … with title … sound name "Glass"'`; `noopNotifier`) in one file, selected by `runtime.GOOS` in `NewNotifier` (the notifiers are pure argv builders, so all compile and unit-test on any host — cleaner than build tags). All run tools through the injected `shell.Shell` with `Capture` set so their stdio never bleeds into the attach terminal; `Notify` returns nil unconditionally (best-effort).
- [x] 1.3 `urgency` maps `low|normal|critical` (default `normal`); `orDefault` fills the `hermod` title; `asAppleStr` strips control chars and quotes the AppleScript literal.

## 2. notify package — local listener + sandbox-side send

- [x] 2.1 `Listen(ctx, sock string, n Notifier, log *log.Logger) (stop func() error, err error)`: bind the socket it is handed (`0600`, dir `0700`), accept-loop in a goroutine until ctx is done; `stop` unlinks + closes, idempotent. Post-bind errors (accept/parse/dispatch) are logged and swallowed.
- [x] 2.2 `listener.handle`: read under `io.LimitReader(conn, 8<<10)` behind a read deadline, `json.Unmarshal`; drop on error or empty body; else `Notifier.Notify`. No error escapes.
- [x] 2.3 `Send(ctx, m Message) error`: read `EnvSock`; error clearly if unset; `net.Dial("unix", …)` and `json.NewEncoder(conn).Encode(m)`.

## 3. shell — reverse forward option

- [x] 3.1 `internal/shell/ssh.go`: add `reverse []string`, `SSHOption`, `WithReverseForward(spec)` (appends `-R <spec> -o StreamLocalBindUnlink=yes -o StreamLocalBindMask=0177`), variadic `NewSSH(inner, host, opts...)`; append `s.reverse` after `-t` and before host. No `ExitOnForwardFailure`. (`tty` became `WithTty()` in the same option set.)

## 4. sandbox — own the local↔sandbox channel

- [x] 4.1 Add `internal/sandbox/channel.go`: `Channel{Local, Remote}` + `IsZero`/`reverseSpec`, and `OpenChannel(ctx, real, effective, host, name)` — real runtime-dir probe (`sh -c 'printf %s "${XDG_RUNTIME_DIR:-$HOME/.hermod/run}"'`) validated as a clean absolute path, dry-run-aware `mkdir -p -m 700 <base>/hermod`, sandbox end `<base>/hermod/<name>.sock` (per user), local end `<localRuntimeDir>/hermod/<host>/<name>.sock`.
- [x] 4.2 `internal/sandbox/sandbox.go`: `Config.Channel Channel`; when non-zero, pass `shell.WithReverseForward(cfg.Channel.reverseSpec())` to the interactive `NewSSH` only; the probe `NewSSH` never gets it. `sandbox` does not import `notify`.

## 5. control — the switch, and nothing else

- [x] 5.1 `options.go`: `Options.Notify bool` + `WithNotify(on bool) Option`; `Run` defaults it **on**.
- [x] 5.2 `run.go`: `setupNotify` returns `(env, sandbox.Channel, stop)`. Off, under `--dry-run`, or on any failure → the plain session (base env, zero channel, no-op stop) and a log line. On → `sandbox.OpenChannel(…, notify.ChannelName)`, `notify.Listen(ctx, ch.Local, notify.NewNotifier(effective), logger)`, and `notify.Env(ch.Remote)` appended to the session env. No addressing or provisioning detail lives in `control`.
- [x] 5.3 Confirm `stopNotify` is deferred, idempotent, and sits before the mirror `settle`; the pause/teardown path is untouched.

## 6. cli — flag + sender subcommand

- [x] 6.1 `cli.go`: add `-N/--no-notify` to the root command and pass `control.WithNotify(!noNotify)`.
- [x] 6.2 Add a `hermod notify [--title][--urgency] <body>...` subcommand that joins args into the body and calls `notify.Send`. Register it as a sibling of the root run.

## 7. Tests (table-driven, argv-recording, no inline expected literals)

- [x] 7.1 `shell/ssh_test.go`: rows for `WithTty`/`WithReverseForward` argv order + a two-forward row + regression rows asserting probe/plain attach carry no `-R`.
- [x] 7.2 `notify` notifier argv table (linux + darwin constructed directly): urgency mapping, `hermod` title default, sound line, `--` end-of-options guard.
- [x] 7.3 `notify` wire/dispatch table over a real in-process bound socket + fake `Notifier`: well-formed, empty-body dropped, unknown-urgency passthrough, oversized dropped, malformed dropped; assert no error escapes.
- [x] 7.4 `notify` addressing table: `Env` shape, `Send` dials exactly what `Env` carries, unset env errors; `Listen`+`stop` binds then unlinks, idempotent.
- [x] 7.5 `sandbox/channel_test.go`: `OpenChannel` provisioning argv + both addresses; same host ⇒ same sandbox endpoint, different hosts ⇒ different local endpoints; unusable runtime dir (probe error, empty, relative, colon, space, trailing banner) refuses and provisions nothing.
- [x] 7.6 `sandbox_test.go`: a carried channel ⇒ `-R` with its spec in the interactive argv, never in the probe; zero/half channel ⇒ today's argv exactly.
- [x] 7.7 `run_test.go`: notify on ⇒ channel opened, `HERMOD_NOTIFY_SOCK` carried, socket bound; off / dry-run / probe failure ⇒ plain session, nothing provisioned, dry-run note logged.
- [x] 7.8 `cli_test.go`: `--no-notify`/`-N` opt-out rows (and notify on by default), `hermod notify` arg parsing with `notify.Send` stubbed.

## 8. Docs + validate

- [x] 8.1 Add a notification section to `README.md` (default-on, `hermod notify done` on the box, the `socat` fallback, the per-user endpoint) and resolve the README's forward reference to the back-channel.
- [x] 8.2 Note the packages in `README.md`'s project structure and `docs/ARCHITECTURE.md`'s domain-object table (`sandbox.Channel`, `notify.Notifier`, the layering line).
- [x] 8.3 Run `make build`, `make test`, `make lint`.
- [x] 8.4 Run `openspec validate add-notify-backchannel`.
- [ ] 8.5 On the sandbox host, manually verify `hermod <host>` then on the box `hermod notify done` raises a local toast; and that a forced bind/probe failure degrades to a plain session.
