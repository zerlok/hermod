## 1. Project skeleton

- [x] 1.1 Initialize the Go module (`go mod init github.com/zerlok/hermod`), add the `spf13/cobra` dependency, and a `main` entrypoint that wires the CLI to the orchestrator
- [x] 1.2 Add the CI-relevant build/test targets so `go build ./...` and `go test ./...` pass on an empty skeleton
- [x] 1.3 Define the `Options` type (session name, sandbox host, cwd, agent args, ssh-config path, log level, dry-run) as an immutable value

## 2. Execution seam: Executor (how)

- [x] 2.1 Define the `Executor` interface: run one argv with cwd/env/capture and return a `Result`
- [x] 2.2 Implement `SubprocessExecutor` on `os/exec` (no PTY lib): inherited stdio + blocking `Run()` for the attach, `CommandContext(...).Output()` for captured probes; build argv as `[]string` run directly, never `sh -c`
- [x] 2.3 Implement `DryRunExecutor` that prints argv as a copy-pasteable shell line and returns success
- [x] 2.4 Unit-test that `DryRunExecutor` prints and never executes, and that captured-output mode returns stdout

## 3. Transport seam: Shell (where)

- [x] 3.1 Define the `Shell` interface (`Run(argv, cwd, env, capture) → Result`) and `LocalShell` leaf holding an `Executor`
- [x] 3.2 Implement `SshShell`, `TmuxSessionShell`, and `LoginShell` decorators that rewrite argv and delegate inward
- [x] 3.3 Implement `ShellFactory` producing the full interactive-attach stack and the shorter non-interactive probe stack
- [x] 3.4 Unit-test each decorator's argv rewriting with a fake leaf executor (no real hosts)

## 4. Git identity (git-identity spec)

- [x] 4.1 Implement read-only discovery of local `user.name`/`user.email` into `GitUserInfo`, using the real executor even under dry-run
- [x] 4.2 Compute the `GIT_AUTHOR_NAME`/`GIT_AUTHOR_EMAIL` env to carry when identity is present; inject nothing when unset
- [x] 4.3 Unit-test fully-set, partially-set, empty identity, and that a read failure is non-fatal

## 5. File mirror (file-sync spec)

- [x] 5.1 Implement `MutagenSyncSession.Open` as create-or-resume keyed on session name
- [x] 5.2 Implement `Flush`, `Pause`, and `Close` (terminate) over the Mutagen CLI
- [x] 5.3 Implement read-only status queries that never mutate sync state (real executor even under dry-run)
- [x] 5.4 Unit-test the lifecycle against a fake shell, asserting the emitted Mutagen argv for each operation

## 6. Remote session (remote-session spec)

- [x] 6.1 Implement `Sandbox.Attach`: attach-or-create the named tmux window over SSH, running the passthrough command or default shell, blocking until detach
- [x] 6.2 Implement `Sandbox.IsActive` as a read-only liveness probe (e.g. `tmux has-session`) over the non-interactive stack
- [x] 6.3 Carry the git-identity env into the attached session's environment
- [x] 6.4 Unit-test attach argv (existing vs new session, with/without passthrough) and `IsActive` true/false via a fake shell

## 7. Orchestration (session-orchestration spec)

- [x] 7.1 Implement `Run(options)`: read identity → open+flush mirror → attach → decide, in that order
- [x] 7.2 Open the mirror inside a teardown-guaranteeing scope so an error mid-attach still closes it
- [x] 7.3 Implement the pause-vs-teardown branch keyed solely on `IsActive` (active → pause; gone → flush then close)
- [x] 7.4 Ensure dry-run prints side-effecting commands and does not take over the terminal, while probes stay real
- [x] 7.5 Unit-test both branches, the error-teardown path, and dry-run behavior with an injected fake `ShellFactory`

## 8. CLI (cli spec)

- [x] 8.1 Parse `hermod <sandbox> [-s] [-C] [-n] [-- args...]` into `Options` with cobra (splitting `--` via `cmd.ArgsLenAtDash()`); error + usage on missing sandbox
- [x] 8.2 Default the session name to the working-directory base name and default the mirror dir to cwd
- [x] 8.3 Resolve the sandbox argument against the `~/.ssh/config` host alias and pass `--` args through verbatim
- [x] 8.4 Unit-test flag parsing, defaults, passthrough splitting, and the missing-argument path

## 9. End-to-end verification

- [x] 9.1 Verify `hermod prod-box -n` prints a copy-pasteable plan reflecting true git identity and true sync state
- [x] 9.2 Manually verify the live loop against a real sandbox: create, flush, attach, detach-with-session-alive → pause, detach-with-session-gone → teardown
- [x] 9.3 Confirm `go build ./...` and `go test ./...` are green and the README usage examples match actual behavior
