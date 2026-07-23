## 1. Fix `mirror.Status`

- [x] 1.1 In `internal/mirror/mirror.go`, change `Status` so that on a focused-probe error it disambiguates with a list-all probe (`mutagen sync list`, no name): if list-all succeeds → `Absent, nil`; if list-all also errors → return the original error (surface it). On focused-probe success, keep the existing `Paused`/`Running` parse.
- [x] 1.2 Confirm `NewMutagenSession` surfaces a propagated `Status` error at construction (no change expected — `control.Run` already wraps it as `open mirror`), so an ambiguous probe aborts open instead of creating over an existing session.

## 2. Tests

- [x] 2.1 Update the mirror test double so it can return different results per argv (focused `sync list <name>` vs. list-all `sync list`), replacing the single-canned-result recorder where needed — keep it table-driven with no literal expected values inline.
- [x] 2.2 `TestStatusIsReadOnly` / status cases: (a) focused success → `Paused`/`Running`; (b) focused error + list-all success → `Absent`, no error; (c) focused error + list-all error → non-nil error, not `Absent`. Assert no mutating command is ever issued on the exec shell.
- [x] 2.3 `TestNewMutagenSessionOpens`: the "absent creates" case drives focused-error + list-all-success and still exercises the `create` branch; add a case where focused-error + list-all-error makes `NewMutagenSession` return an error and issue **no** `mutagen sync create` (recreate-over-existing regression guard).

## 3. Validate

- [x] 3.1 Run `make test` and `make lint`; mirror package and full suite pass.
- [x] 3.2 Run `openspec validate fix-mirror-status-error-swallow`.
