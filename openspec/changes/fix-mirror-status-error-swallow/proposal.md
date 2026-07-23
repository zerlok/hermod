## Why

`mirror.Status` reads *every* failure of `mutagen sync list <name>` as `Absent` (`internal/mirror/mirror.go`). When the probe fails for any reason other than a genuinely missing session — a not-yet-ready Mutagen daemon, a transient connection hiccup — hermod concludes no session exists and, on open, runs `mutagen sync create` over the session that is actually still there. Because Mutagen keys sessions by ID rather than name, that spawns a fresh session whose initial sync can force the local tree onto the remote, dropping remote-only changes (unstaged files, and even committed changes). This violates the existing file-sync requirement that a status probe report **the true current sync state** — reporting `Absent` when the session is really Paused or Running is a false state. Reported as issue #4 ("sometimes hermod drops remote state").

## What Changes

- Fix `mirror.Status` to classify **only** a genuine "session not found" signal (Mutagen's not-found exit code) as `Absent`, matching the discipline `sandbox.IsActive` already applies to `tmux has-session`.
- Propagate any other probe failure as an error instead of silently degrading to `Absent`, so `NewMutagenSession` does not create-over-existing on an ambiguous probe — it fails loudly rather than clobbering remote state.
- Add regression tests: a not-found signal still reads as `Absent`; a non-not-found probe failure surfaces as an error and issues no `mutagen sync create`.

## Capabilities

### New Capabilities

<!-- none: this fixes code to meet an existing contract -->

### Modified Capabilities

- `file-sync`: refine the existing "Sync-status queries are read-only probes" requirement — the current scenario says the true state is reported but is silent on what happens when the probe itself fails for a non-"not-found" reason. Add a scenario pinning that an ambiguous probe failure is surfaced, not reported as `Absent`. No new requirement is introduced; the correct behavior is already required, only made precise where it was silent.

## Impact

- `internal/mirror/mirror.go` — `Status` error classification; `NewMutagenSession` open path now sees propagated errors.
- `internal/mirror/mirror_test.go` — existing cases that feed a generic error and expect `Absent` are updated to distinguish not-found from ambiguous failures.
- `internal/shell` — the not-found signal is read via the existing `shell.ExitCode` helper (already used by `sandbox.IsActive`); no new shell API.
- No CLI, flag, or user-facing behavior change on the happy path; the only observable difference is that an ambiguous probe now aborts open instead of recreating the mirror.
