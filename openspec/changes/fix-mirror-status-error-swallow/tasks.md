## 1. Capture stderr through the shell

- [x] 1.1 Add `Stderr` to `shell.Result` and capture stdout and stderr separately in the leaf's `Capture` branch (`internal/shell/local.go`), so the not-found marker Mutagen prints on stderr is available to callers. Add a `TestSubprocessCapture` case asserting stderr is captured.

## 2. Fix `mirror.Status`

- [x] 2.1 In `internal/mirror/mirror.go`, read absence from the focused probe's own output: if `res.Stdout`/`res.Stderr` contains the not-found marker (`unable to locate requested sessions`, a package const) → `Absent`; else if the command errored → surface the error; else parse `Paused`/`Running` as before. Remove the second list-all probe.
- [x] 2.2 Confirm `NewMutagenSession` surfaces a propagated `Status` error at construction (no change expected — `control.Run` already wraps it as `open mirror`), so an ambiguous probe aborts open instead of creating over an existing session.

## 3. Tests

- [x] 3.1 Simplify the mirror test double back to a single canned result/error; add a shared `notFound` reply carrying the marker on stderr. Keep tables the source of expected values.
- [x] 3.2 `TestStatusIsReadOnly` cases: not-found marker → `Absent` (no error); `Paused`/`Running` parse; ambiguous error (no marker) → non-nil error, not `Absent`. Assert no mutating command is issued on the exec shell.
- [x] 3.3 `TestNewMutagenSessionOpens`: not-found marker exercises the `create` branch (ordered `[ssh mkdir -p, mutagen sync create]`); an ambiguous probe makes `NewMutagenSession` return an error and issue **no** `mutagen sync create` (recreate-over-existing regression guard).

## 4. Validate

- [x] 4.1 Run `make test` and `make lint`; mirror package and full suite pass.
- [x] 4.2 Run `openspec validate fix-mirror-status-error-swallow`.
