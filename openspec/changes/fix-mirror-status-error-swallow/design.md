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

### Decision: Confirm absence positively via a list-all disambiguation, not by guessing an error signal
`Status` returns `Absent` only when the probe positively establishes absence; every other failure
returns a non-nil error. This mirrors `sandbox.IsActive`, keeping the two probes consistent.

Concretely, without a way to run Mutagen in this environment we do **not** hard-code a not-found exit
code or match a stderr string. Instead:

1. Probe the focused query `mutagen sync list <name>` (capturing), as today.
2. On **success**, the session exists — parse `Paused` vs `Running` from that single-session output,
   unchanged from the current code.
3. On **error**, disambiguate with one list-all probe `mutagen sync list` (no name filter):
   - if list-all **succeeds**, the daemon is reachable and only *our* named session was missing →
     `Absent`.
   - if list-all also **errors**, the failure is ambiguous (daemon down / transport problem) →
     surface the original error.

This needs no assumption about Mutagen's not-found exit code, message text, or output format, so it is
robust across Mutagen versions and directly implements the spec's positive-confirmation rule.

- **Alternative — `shell.ExitCode(err)` against a fixed not-found code** (as `sandbox.IsActive` does
  for tmux's exit 1): cleaner and more consistent with the codebase idiom, but requires knowing
  Mutagen's not-found exit code and that it is distinct from a daemon-down failure. Not verifiable in
  this environment; rejected to avoid shipping a guessed constant. May replace the list-all probe
  later if the exit code is confirmed distinct on a real host.
- **Alternative — match the `"unable to locate requested sessions"` stderr string** (what the current
  test encodes): fragile across versions/localizations. Rejected.
- **Alternative — parse a single list-all call for our session's block**: one fewer probe, but
  requires parsing multi-session output to attribute a `Status:` line to the right `Name:` block.
  Rejected as more fragile than reusing the existing single-session parse on the success path.

### Decision: Propagate the error through the open path unchanged
`NewMutagenSession` already returns `Status`'s error at construction. Once `Status` stops masking
failures, an ambiguous probe makes the constructor return an error, and `control.Run` already wraps
that as `open mirror` and settles without creating anything. No new control-flow is needed.

## Risks / Trade-offs

- **A flaky daemon now aborts open instead of silently proceeding** → intended: aborting is strictly
  safer than clobbering remote state, and the error message tells the user why. Net improvement in
  correctness at the cost of a hard failure on a genuinely broken daemon.
- **Extra list-all probe on the error path** → only runs when the focused query already failed; a
  single additional read-only call, no side effects.
- **Assumes `mutagen sync list` (no filter) exits 0 whenever the daemon is reachable** → the standard
  behavior of a list command with no matching filter; confirm opportunistically on a real host, but
  the change does not depend on any Mutagen-internal constant.

## Open Questions

- None blocking. If Mutagen's not-found exit code is later confirmed distinct on a real host, the
  list-all disambiguation probe can be replaced by a single `shell.ExitCode` check for consistency
  with `sandbox.IsActive`.
