## Context

`mirror.Status` (`internal/mirror/mirror.go`) probes with `mutagen sync list <name>` and reads
**any** error as `Absent`:

```go
res, err := m.probe.Run(ctx, /* mutagen sync list <name> */)
if err != nil {
    return Absent, nil
}
```

The sibling probe `sandbox.IsActive` (`internal/sandbox/sandbox.go`) already handles the same
class of problem correctly: it treats only the specific "gone" exit code as absent and returns any
other error to the caller, which then chooses the safe default. The two probes are asymmetric, and
that asymmetry is the bug — see the proposal for the data-loss chain it causes.

## Goals / Non-Goals

**Goals:**
- `Status` reports `Absent` only on positive confirmation that no session exists.
- Any other probe failure propagates as an error, so `NewMutagenSession` aborts opening rather than
  creating a new session over an existing one.
- Make the two probes symmetric: `Status` follows the same discipline as `sandbox.IsActive`.

**Non-Goals:**
- Changing sync mode, conflict resolution, or any create/resume/flush behavior.
- Retrying or recovering from a failed probe — surfacing the error is enough; the caller aborts.
- Any CLI or user-facing flag change.

## Decisions

### Decision: Read absence from the focused probe's own output
`Status` returns `Absent` only when the probe positively establishes absence; every other failure
returns a non-nil error, so an ambiguous probe aborts open rather than recreating over an existing
session.

Absence is read from the focused probe's **own output**, not from a separate liveness check:
`mutagen sync list <name>` reports `unable to locate requested sessions` when no session matches. So:

1. Run the focused query `mutagen sync list <name>`, capturing stdout **and stderr**.
2. If the output carries the not-found marker → `Absent`.
3. Otherwise, if the command errored → surface the error (ambiguous: daemon down / transport).
4. Otherwise the session exists — parse `Paused` vs `Running` from stdout, unchanged.

This required exposing captured stderr on `shell.Result` (Mutagen prints the marker on stderr); the
leaf shell now captures stdout and stderr separately under `Capture`.

- **Alternative — a second `mutagen sync list` (list-all) probe to disambiguate** (the first
  implementation): reviewer feedback on PR #6 called out using "does the daemon list at all" as an
  indirect, odd proxy for "is *this* session absent". Reading the marker straight from the focused
  call's output is more direct and needs only one probe. Rejected.
- **Alternative — `shell.ExitCode(err)` against a fixed not-found code** (as `sandbox.IsActive` does
  for tmux's exit 1): would avoid string matching, but requires knowing Mutagen's not-found exit code
  and that it is distinct from other failures — not verifiable here. Rejected.

### Decision: Propagate the error through the open path unchanged
`NewMutagenSession` already returns `Status`'s error at construction. Once `Status` stops masking
failures, an ambiguous probe makes the constructor return an error, and `control.Run` already wraps
that as `open mirror` and settles without creating anything. No new control-flow is needed.

## Risks / Trade-offs

- **A flaky daemon now aborts open instead of silently proceeding** → intended: aborting is strictly
  safer than clobbering remote state, and the error message tells the user why.
- **Matching the not-found marker string couples to Mutagen's message text** → the marker is a stable,
  documented phrase and is exactly the signal the original code's test already encoded; if a future
  Mutagen reworded it, the tests pin the expected marker so the break is caught, not silent.

## Open Questions

- None blocking.
