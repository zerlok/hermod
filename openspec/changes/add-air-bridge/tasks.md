## 1. `hermod attach`, environment fallbacks, sandbox-scoped mirror names; back-channel removed

- [x] 1.1 In `internal/cli`, add the `attach [SANDBOX] [-s NAME] [-C DIR] [-n] [-- COMMAND…]` subcommand and drop the bare `hermod <sandbox>` form; resolve the sandbox from the argument, else `HERMOD_SANDBOX`, failing before any side effect when neither is set
- [x] 1.2 Resolve the command from `-- COMMAND…`, else `HERMOD_SANDBOX_CMD` run by the sandbox's shell, else the default shell; table-driven tests for every argument/environment combination in `internal/cli/cli_test.go`
- [x] 1.3 In `internal/mirror`, name mirrors `hermod-<sandbox>-<slug>` (alias reduced to Mutagen's letters, digits and dashes), decouple the mirror name from `-s`, and check a found session's remote endpoint before resuming; table-driven argv tests for create, resume and endpoint mismatch
- [x] 1.4 List mirrors under the earlier names on the first run and leave them in place; test that no terminate is issued
- [x] 1.5 Remove `hermod notify`, `-N`/`--no-notify`, `internal/notify`, `internal/control/notify.go`, `sandbox.Channel` and `HERMOD_NOTIFY_SOCK`, keeping `shell`'s reverse-forward option; delete their tests
- [x] 1.6 Withdraw the `add-notify-backchannel` and `add-remote-agent` changes by removing their directories under `openspec/changes/`
- [x] 1.7 Update README.md (usage, notifications section, project structure) and docs/ARCHITECTURE.md (domain objects, CLI seam) for `hermod attach` and the removed back-channel
- [x] 1.8 Run `gofmt -l .`, `go vet ./...`, `go test ./...` and `go build ./...`

## 2. Claude classification, path map, utility calls and the stand-in link

- [x] 2.1 Create the leaf package `internal/pathmap`: root pairs, longest prefix at a segment boundary in both directions, applied to a string, a JSON value and one stream-json line; table-driven tests
- [x] 2.2 Create `internal/claude` with `Classify` (utility call, chat session, terminal session, not an agent session) and session-UUID extraction from `--session-id`/`--resume`, as pure functions; table-driven tests covering every row of the design's classification table
- [x] 2.3 In `internal/shell`, give `Command` explicit `Stdin`, `Stdout` and `Stderr`, and give `NewSSH` an option that refuses a shared master connection (`ControlPath=none`); tests for the argv and stdio wiring
- [x] 2.4 In `internal/control`, run a utility call as `ssh <sandbox> claude ARGS…` with the caller's stdio and its exit code, opening no mirror, tmux session or proxy; fake-shell tests
- [x] 2.5 In `internal/cli`, dispatch on the program name: `claude ARGS…` runs `attach -- claude ARGS…` with the sandbox from `HERMOD_SANDBOX` and `HERMOD_SANDBOX_CMD` ignored; route all of Hermod's output, the dry-run plan included, to stderr and `~/.cache/hermod/air/<uuid>.log` (mode `0600`); tests for both program names
- [x] 2.6 Create `internal/air` with the stand-in link at `~/.local/share/hermod/bin/claude` (create and repair); tests against a temporary directory
- [x] 2.7 Run the checks of 1.8

## 3. Terminal sessions: session directory, tmux per UUID, hook proxy

- [x] 3.1 In `internal/claude`, rewrite an argv for the sandbox (settings file moved to the session directory, every `--mcp-config` value merged into the session directory's `mcp.json` without local-program servers, local paths mapped, `--session-id`/`--resume` untouched, no `--name` added) and a hook settings file (URL `localhost:<hookport>`, session token); select the `CLAUDE_*`/`ANTHROPIC_*`/`MAX_THINKING_TOKENS` environment without `CLAUDE_CODE_EXECUTABLE` and `CLAUDE_CONFIG_DIR`; table-driven tests
- [x] 3.2 Add an opaque token type that does not print; test its formatted forms
- [x] 3.3 In `internal/sandbox`, add agent sessions keyed by UUID: one ssh probe running `claude agents --json` with tmux and transcript checks, mapped to running / running outside Hermod / closed / new; fake-shell tests for each row and for removing a stale tmux session
- [x] 3.4 Create `~/.hermod/s/<uuid>/` (mode `0700`) with a free `hookport`, a new `token`, `env`, `settings.json` and `mcp.json`, all written over ssh stdin; tests asserting no secret appears in any argv
- [x] 3.5 Start tmux `hermod-<uuid>` in the project's directory running `. ~/.hermod/s/<uuid>/env && exec claude <rewritten args>`, tagged with `@hermod-mode`, `@hermod-origin`, `@hermod-dir`, and with `HERMOD_SESSION`, `HERMOD_MODE`, `HERMOD_ORIGIN` in Claude's environment; derive the origin from `HERMOD_ORIGIN` or the hostname, empty leaving it out; tag plain attach sessions the same way; table-driven tests
- [x] 3.6 Attach a terminal session with `tmux attach-session -t hermod-<uuid>` (never creating it; a session gone by then fails the run, and a failed Claude's pane stays with `remain-on-exit failed`) over one tty ssh connection refusing a shared master and carrying `-R <hookport>:127.0.0.1:<proxy port>`; run every setup command with stdin from `/dev/null`; fake-shell tests
- [x] 3.7 Create `internal/hook`: a handler on a loopback listener that accepts only the session token, maps bodies through `pathmap`, and forwards to Air's current URL with Air's token; `httptest`-based tests for forward, wrong token and path mapping
- [x] 3.8 Log and continue without hooks when the reverse forward cannot bind; test the attach still runs
- [x] 3.9 Wire the terminal-session branch into `control.Run`; fake-shell tests for new, closed (`--resume`, transcript copied up) and running sessions
- [x] 3.10 Run the checks of 1.8

## 4. Chat sessions: FIFO and log runtime, stream pump, `hermod air configure`

- [x] 4.1 In `internal/sandbox`, start a chat session with Claude's stdin on `in.fifo` held open by the tmux session and stdout appended to `out.log`; fake-shell argv tests
- [x] 4.2 Attach a chat session with a shell line that tails `out.log` and copies stdin into `in.fifo` one complete line at a time; argv tests
- [x] 4.3 In `internal/control`, pump both directions line by line, unbuffered, through `pathmap`; tests with in-memory pipes, including a partial final line
- [x] 4.4 End the attach on stdin EOF, SIGTERM or a dropped connection without touching the tmux session, and exit inside the 2 s + 5 s window; tests with a fake shell and a cancelled context
- [x] 4.5 Support start and `--resume` only: reopening a running chat session closes its tmux session and starts `--resume`; test that branch
- [x] 4.6 Verify on a sandbox that `claude agents --json` lists a running headless chat session; if it does not, read chat-session state from `tmux has-session` instead, and record the result in docs/air-integration.md
- [x] 4.7 In `internal/air`, merge an entry into `~/.jetbrains/acp.json` keeping other entries and keys, with `--node`/`--acp` resolved to absolute paths from `PATH`, `--name`, `--origin` and `-n`, the sandbox only in `env`; the entry runs the adapter directly (`command` is `--node`, `args` is `--acp`) until group 6; table-driven tests on temporary files
- [x] 4.8 Add `hermod air configure claude [SANDBOX]` to `internal/cli`, printing the link path and the `HERMOD_SANDBOX=<sandbox>` line for terminal mode; tests
- [x] 4.9 Document chat and terminal mode setup in README.md and the new packages in docs/ARCHITECTURE.md, linking to the design rather than restating it
- [x] 4.10 Run the checks of 1.8

## 5. Mirror labels, shared leases, transcript mirrors

- [x] 5.1 In `internal/mirror`, label mirrors `hermod-kind` and `hermod-sandbox`, and list a sandbox's roots with `mutagen sync list --label-selector hermod-sandbox=<sandbox>`; fake-shell tests
- [x] 5.2 Build the run's path map from those roots: reuse a mirror whose local root holds the directory, else create one for the corresponding sandbox path; table-driven tests including a sibling worktree
- [x] 5.3 Create project mirrors in `two-way-safe` mode; argv test
- [x] 5.4 Add one-way `one-way-safe` transcript mirrors from `projects/<encoded dir>/` under the sandbox's Claude config directory to the local `CLAUDE_CONFIG_DIR` (or `~/.claude`), one per local end; table-driven tests for the encoded paths
- [x] 5.5 Hold a shared `flock` per mirror on `$XDG_RUNTIME_DIR/hermod/lease/<mirror>`; on exit take a non-blocking exclusive lock and settle only as the last holder; tests with two holders in one process
- [x] 5.6 Read a root's liveness as any tagged tmux session with a `@hermod-dir` under that root, with unknown liveness pausing; fake-shell tests
- [ ] 5.7 Verify with `air-probe` and two entries that the adapter lists sessions from `CLAUDE_CONFIG_DIR`, and record the result in docs/air-integration.md
- [x] 5.8 Update docs/ARCHITECTURE.md (pause vs teardown, mirrors and leases) and docs/air-integration.md where it describes the mirrors as future work
- [x] 5.9 Run the checks of 1.8

## 6. `hermod air acp` and close on "mark done"

- [ ] 6.1 Verify in `acp-tap` logs that the ACP session ID is Claude's session UUID, and record the result in docs/air-integration.md
- [x] 6.2 Add `control.RunACP` and `hermod air acp claude [SANDBOX] [--node] [--acp]`: start the adapter with the environment unchanged and copy messages both ways verbatim; tests with in-memory pipes
- [x] 6.3 On `_session/archive`, close the session (`tmux kill-session -t hermod-<uuid>`, session directory kept) and settle its mirrors while holding a lease; fake-shell tests, including an attached stand-in on the same mirror
- [x] 6.4 Switch the entry `air configure` writes to run `hermod air acp claude --node=… --acp=…`; update its tests
- [ ] 6.5 Check with `air-probe` whether "mark done" stalls the transcript mirror, and record the result in docs/air-integration.md
- [x] 6.6 Run the checks of 1.8

## 7. Reattach to a running chat session

- [x] 7.1 Keep the fallback from 4.5 (close the tmux session and start `--resume`) as the path taken whenever live reattach is unavailable or fails; a test fixes this behaviour
- [ ] 7.2 In `internal/claude`, add the stream-json replay rules: the first `initialize` response in a log, control requests still awaiting an answer, and prompts the log has not echoed; table-driven tests over recorded log lines
- [ ] 7.3 In `internal/control`, answer the SDK's `initialize` locally, re-deliver pending control requests, resend unechoed prompts and stream the log from its end; tests with in-memory pipes
- [ ] 7.4 Verify live reattach against Air with `air-probe`; switch from the fallback only if Air accepts it, and record the result in docs/air-integration.md
- [ ] 7.5 Run the checks of 1.8 and `openspec validate add-air-bridge --strict`
