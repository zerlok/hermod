## 1. version — a version the binary knows about itself

- [ ] 1.1 Add `internal/version`: a stamped `var version string`, a `Current()` that falls back to `debug.ReadBuildInfo()` and then to a development pseudo-version, and `Compare(a, b)` with the rule that a development version never compares as newer than a release.
- [ ] 1.2 `Makefile`: stamp `-ldflags "-X …/internal/version.version=$(VERSION)"` from `git describe`; keep unstamped builds working.
- [ ] 1.3 `cli`: `hermod version` subcommand and a `--version` flag that short-circuits the host argument.

## 2. shell — stdin on a Command

- [ ] 2.1 `shell.Command` gains `Stdin io.Reader`; decorators (`ssh`, `tmux`, `loginShell`) pass it through untouched; the real leaf wires it to the child, and the dry-run leaf prints the command with a `< <source>` note and reads nothing.
- [ ] 2.2 Regression tests: every existing decorator and leaf behaves exactly as today when `Stdin` is nil.

## 3. agent — probe and install plan

- [ ] 3.1 Add `internal/agent`: `Dir` (`~/.hermod/bin`), the agent path, and `Probe(ctx, real shell.Shell, host) (version string, ok bool)` running `<agentDir>/hermod version` — any failure or unparseable output reads as absent.
- [ ] 3.2 `Platform(ctx, real, host)`: read the sandbox's `uname -s`/`uname -m`, map to Go's `GOOS`/`GOARCH`, and report whether the local executable can run there.
- [ ] 3.3 `Install(ctx, effective shell.Shell, host, exe io.Reader, version string)`: `cat >` a `.partial` under the agent dir, `chmod 700`, `mv` to `hermod-<version>`, `ln -sfn` the `hermod` path at it. Nothing reachable at the agent path is ever partial.
- [ ] 3.4 `LinkOnPath(ctx, effective, host)`: best-effort `~/.local/bin/hermod` symlink, only when the directory exists and the target is absent or already points into the agent dir.

## 4. control — one more best-effort step

- [ ] 4.1 `Options.Agent bool` (default on) + `WithAgent(on bool)`; `--no-agent` on the CLI.
- [ ] 4.2 `setupAgent` beside `setupNotify`: probe → compare → install when behind → link → return the agent path for the session env (`HERMOD_BIN`). Every failure logs one line and returns no path; dry-run installs nothing.
- [ ] 4.3 Confirm the flow, the mirror, and the pause/teardown decision are untouched on every failure path.

## 5. Tests (table-driven, argv-recording, no inline expected literals)

- [ ] 5.1 `version`: comparison table (older/equal/newer/dev-vs-release/unparseable).
- [ ] 5.2 `agent`: probe table (working agent, non-zero exit, garbage output, empty) → present/absent; platform table (match, OS mismatch, arch mismatch, unreadable `uname`).
- [ ] 5.3 `agent`: install argv order table asserting partial-then-publish and that no command targets the agent path before the binary is complete.
- [ ] 5.4 `control`: agent on/off/dry-run/probe-failure/mismatch ⇒ what is carried in the env and what is run; every failure still reaches teardown as a normal session.
- [ ] 5.5 `cli`: `hermod version`, `--version` without a host, and the `--no-agent` flag.

## 6. Docs + validate

- [ ] 6.1 `README.md`: drop the "install hermod on the box" caveat from the notification section; document `HERMOD_BIN`, the platform-mismatch case, and `--no-agent`.
- [ ] 6.2 `docs/ARCHITECTURE.md`: add the agent to the domain-object table and note `Command.Stdin` in the shell section.
- [ ] 6.3 Run `make build`, `make test`, `make lint`; run `openspec validate add-remote-agent`.
- [ ] 6.4 On a real sandbox: first run installs the agent and `hermod notify done` works with no manual install; a second run installs nothing; a forced older agent is upgraded.
