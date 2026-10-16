# Relay reader test: free-connection read intermittently returns nil hook_gone_at

Draft for the user to file. No Bug Bee has been created or assigned a status.

## Summary / incident

On 2026-10-16, `TestReaderSkipsHookGoneWhileTheConnectionIsHeld` failed once in the retained Task 2 Go-repair full-gate logs. After the fixture released its held connection, the single free-phase get-permission returned `hook_gone_at = nil` instead of the exact fixture time `e.now`. The initial held-connection response, one-second bound, and independent durable no-write assertions did not fail. The observed impact is an intermittent API test/full-gate failure; a production defect or product race has not been established.

## Steps to reproduce

1. Use a development checkout of `feature/b.ftd` in the repository's normal test sandbox. The observed failure used HEAD `181c7eb090f6da1eee2c52f1d959ff4e7fbd899b` **plus pending** README recovery-row and `cursor_resume_liveness_test.go` snapshot-verification fixture fixes. It was neither a clean `181c7eb0` tree nor accepted HEAD `21e9b481584e06513b25b9b6732485a332305456`.
2. Run the original full command from the repository root:

   ```sh
   make test-sandbox
   ```

3. Inspect the free-phase assertion in `TestReaderSkipsHookGoneWhileTheConnectionIsHeld`. On the current accepted revision, retain any failure-only `RecordHookGone` trace emitted by that assertion.

This is not a reliable reproducer. Do not infer a repair from a passing attempt. The retained full-attempt history is:

| Attempt | Result |
| --- | --- |
| First, before diagnostic instrumentation | All 68 real Go test packages passed; Bun 430 passed, 0 failed. Overall exit 2 came from an extra evidence directory being discovered as a Go package and failing to build. That packaging issue was corrected separately. |
| Second, after evidence packaging correction | This reader assertion failed; 67 Go packages passed, API failed; Bun 430 passed, 0 failed. Overall exit 2, 221.393 seconds. |
| Third, with diagnostic instrumentation | All 68 Go packages passed; Bun 430 passed, 0 failed. Overall exit 0, 222.738 seconds. The exact focused test also passed once with `-count=1`. |

The subsequent user-run Task 2 integration gate passed 18/18 groups, 181 cases, including cursor-resume 9/9. That green gate does not prove this naturally occurring failure was fixed.

## Environment

- Development branch `feature/b.ftd`; historical source state is pinned above. Accepted HEAD `21e9b481584e06513b25b9b6732485a332305456` includes the diagnostic wrapper and separate wrapper verification.
- Execution used `make test-sandbox`, with sandbox HOME and test fixture stores. No real AD store, personal Cursor/auth state, or live service is needed for reproduction.
- The failing run ran from `2026-10-16T18:06:29.426Z` to `2026-10-16T18:10:10.819Z`. The expected fixture timestamp in the failure was `2026-10-16 18:08:33.331 +0000 UTC`.
- Installed-binary build stamp and host OS were not captured in the cited failure receipt; do not invent them or query a real store to fill them in.

## Expected result

The test's contract is: while the store's connection is held, the reader promptly returns fallen_back with no `hook_gone_at` and writes none; after release, one free-phase get-permission records and returns the exact original `e.now`. Keep the single read and exact timestamp oracle, with the original held-phase bound and no-write checks.

## Actual result

Verbatim from `go-gate-repair/tests/test-sandbox.log:140–141`:

```text
--- FAIL: TestReaderSkipsHookGoneWhileTheConnectionIsHeld (7.59s)
    relay_delivery_test.go:350: get-permission with the connection free: hook_gone_at = <nil>; want 2026-10-16 18:08:33.331 +0000 UTC recorded
```

Line 350 is the historical failure location; the accepted diagnostic change moves/expands that assertion. No underlying store error was printed in this historical run.

## Root cause / remaining unknowns

Unknown. `recordGone` intentionally returns without propagating a `RecordHookGone` error (`pkg/api/relay_delivery.go:378–391`, verified in current source). The historical log therefore cannot identify admission through `operationGate`, the 1ms pool-acquisition allowance, SQL, COMMIT, cleanup, or another failure stage. The 7.59-second whole-test duration does not attribute the failing operation's elapsed time.

The read-only diagnosis discusses outstanding bounded-operation workers as a possible fixture-precondition issue. It does not demonstrate the historical cause. In particular, the holder's release joins that holder's completion, including its connection cleanup; claiming a retained cleanup race in that holder is unsupported. Successful request reads alone also do not establish operation-gate availability.

The accepted `hookGoneTrace` wrapper in `pkg/api/relay_delivery_test.go:349–377` already captures the real delegated call's count, timestamp/maxWait/request-ID arguments, returned timestamp map, elapsed time, and underlying error type/text, and adds them only to assertion failure output. It returns the original result/error unchanged. This is instrumentation, not a fixture or product fix: no extra SQL, retries, sleeps, polling, barrier, or deadline change was added, and the original assertion remains. No failing instrumented run has yet supplied the missing attribution.

## Acceptance criteria

- [ ] Capture a naturally failing instrumented run, or document a controlled reproduction with a clear distinction between what it proves and what remains unknown historically.
- [ ] Attribute the failed free-phase write using actual captured evidence before selecting a fixture or product repair. Preserve the trace if natural reproduction remains unavailable; do not report the historical root cause as established.
- [ ] Any resulting repair preserves the original held-connection one-second bound, fallen_back/nil response, independent durable no-write check, single free-phase request, and exact original timestamp assertion. No retry or relaxed assertion substitutes for fixing the demonstrated issue.
- [ ] Verify any repair with a causal check appropriate to the captured failure and sandboxed regression checks. Report passing runs with their cache/reproduction limits; a green gate alone is not causal repair evidence.

## Out of scope

- Already completed README recovery-row wording and retained-descriptor snapshot-verification allowance fixes.
- The separate historical one-second Decide/COMMIT issue; this report has no evidence that the failures share a cause.
- Product deadline changes, scheduling pressure, ad hoc timeout increases, or a proposed connection/gate barrier without supporting reproduction evidence.
- Filing the Bug Bee, changing ticket statuses, or changing code as part of this draft.

## Evidence

Paths below are relative to this draft's directory. Retain or attach the relevant evidence when filing; local ignored artifacts may not accompany a checkout.

- [Full-attempt report](go-gate-repair/tests/report.md), [first full log](go-gate-repair/tests/attempt-1-test-sandbox.log), [second full log](go-gate-repair/tests/test-sandbox.log), and [second summary](go-gate-repair/tests/full-summary.json).
- [Read-only reader-release diagnosis](go-gate-repair/reader-release-diagnosis/report.md); its proposed barrier was not applied as a fix.
- [Diagnostic instrumentation report](go-gate-repair/free-reader-trace/report.md) and [third full summary](go-gate-repair/free-reader-trace/full-summary.json).
