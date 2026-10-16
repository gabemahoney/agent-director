# Relay reader test: free-connection read intermittently returns nil hook_gone_at

Draft for the user to file. No Bug Bee has been created or assigned a status.

## Summary / incident

On 2026-10-16, `TestReaderSkipsHookGoneWhileTheConnectionIsHeld` failed once in the retained Task 2 Go-repair full-gate logs and recurred in the Task 3 FIFO-diagnostic full run. After the fixture released its held connection, the single free-phase get-permission returned `hook_gone_at = nil` instead of the exact fixture time `e.now`. The initial held-connection response, one-second bound, and independent durable no-write assertions did not fail. The observed impact is an intermittent API test/full-gate failure; a production defect or product race has not been established.

## Steps to reproduce

1. Use a development checkout of `feature/b.ftd` in the repository's normal test sandbox. The observed failure used HEAD `181c7eb090f6da1eee2c52f1d959ff4e7fbd899b` **plus pending** README recovery-row and `cursor_resume_liveness_test.go` snapshot-verification fixture fixes. It was neither a clean `181c7eb0` tree nor accepted HEAD `21e9b481584e06513b25b9b6732485a332305456`.
2. Run the original full command from the repository root:

   ```sh
   make test-sandbox
   ```

3. Inspect the free-phase assertion in `TestReaderSkipsHookGoneWhileTheConnectionIsHeld`. On the current accepted revision, retain any failure-only `RecordHookGone` trace emitted by that assertion.

This is not a reliable reproducer. Do not infer a repair from a passing attempt. The original Task 2 full-attempt history is:

| Attempt | Result |
| --- | --- |
| First, before diagnostic instrumentation | All 68 real Go test packages passed; Bun 430 passed, 0 failed. Overall exit 2 came from an extra evidence directory being discovered as a Go package and failing to build. That packaging issue was corrected separately. |
| Second, after evidence packaging correction | This reader assertion failed; 67 Go packages passed, API failed; Bun 430 passed, 0 failed. Overall exit 2, 221.393 seconds. |
| Third, with diagnostic instrumentation | All 68 Go packages passed; Bun 430 passed, 0 failed. Overall exit 0, 222.738 seconds. The exact focused test also passed once with `-count=1`. |

The subsequent user-run Task 2 integration gate passed 18/18 groups, 181 cases, including cursor-resume 9/9. Task 3 then supplied a natural instrumented recurrence in `../e4-task3/fifo-diagnostic/full.log:160–161`. The later Task 3 return-full run passed with Go 68 packages (62 cached) and Bun 430/0. These passing gates establish nonrecurrence in those runs, not a repair.

## Environment

- Development branch `feature/b.ftd`; historical source state is pinned above. Accepted HEAD `21e9b481584e06513b25b9b6732485a332305456` includes the diagnostic wrapper and separate wrapper verification.
- Execution used `make test-sandbox`, with sandbox HOME and test fixture stores. No real AD store, personal Cursor/auth state, or live service is needed for reproduction.
- The failing run ran from `2026-10-16T18:06:29.426Z` to `2026-10-16T18:10:10.819Z`. The expected fixture timestamp in the failure was `2026-10-16 18:08:33.331 +0000 UTC`.
- Task 3 recurrence: development HEAD `494bcd5802401474e464ffeb721b5ad8a3388cfc` plus the pending Task 3 source/test candidate. It is neither a clean HEAD nor the later settled return-full input. The original free-phase expected timestamp was `2026-10-16 19:33:47.531 +0000 UTC`.
- Installed-binary build stamp and host OS were not captured in the cited failure receipt; do not invent them or query a real store to fill them in.

## Expected result

The test's contract is: while the store's connection is held, the reader promptly returns fallen_back with no `hook_gone_at` and writes none; after release, one free-phase get-permission records and returns the exact original `e.now`. Keep the single read and exact timestamp oracle, with the original held-phase bound and no-write checks.

## Actual result

Verbatim from `go-gate-repair/tests/test-sandbox.log:140–141`:

```text
--- FAIL: TestReaderSkipsHookGoneWhileTheConnectionIsHeld (7.59s)
    relay_delivery_test.go:350: get-permission with the connection free: hook_gone_at = <nil>; want 2026-10-16 18:08:33.331 +0000 UTC recorded
```

Line 350 is the original historical failure location; the accepted diagnostic change moves/expands that assertion. No underlying store error was printed in that Task 2 run.

Task 3 natural recurrence, verbatim from `../e4-task3/fifo-diagnostic/full.log:160–161`:

```text
--- FAIL: TestReaderSkipsHookGoneWhileTheConnectionIsHeld (9.34s)
    relay_delivery_test.go:351: get-permission with the connection free: hook_gone_at = <nil>; want 2026-10-16 19:33:47.531 +0000 UTC recorded; RecordHookGone calls=1 at=2026-10-16 19:33:47.531 +0000 UTC maxWait=0s requestIDs=[1] stored=map[] elapsed=1.00069845s error=(*fmt.wrapError) store: commit outcome unconfirmed; read the request before retrying (branch=operation_guard): context deadline exceeded
```

The instrumented call occurred once, with zero lock wait and the original exact timestamp oracle. Nil public output does not establish that the write was absent durably.

## Root cause / remaining unknowns

The original Task 2 cause remains unknown. `recordGone` returns without propagating a delegated `RecordHookGone` error (`pkg/api/relay_delivery.go:378–391`, verified in current source). Its historical log cannot identify the failed stage; whole-test duration does not attribute operation time.

The Task 3 recurrence narrows that separate observation to `branch=operation_guard`: the worker had set `commitStarted=true` but had not published completion when the original one-second guard expired. Connection lease, busy-timeout PRAGMA, BEGIN IMMEDIATE, UPDATE/RETURNING and the precommit context check had completed. The remaining interval includes scheduling before native COMMIT, native COMMIT/response, and scheduling before callback result publication. It does **not** prove that native COMMIT itself consumed one second. The recurrence's durable outcome was not captured.

`withConnOperation` registers cleanup first and result publication second (`internal/store/lockwait.go:153–161`); LIFO publishes the callback result before cleanup. Cleanup/timeout restore/connection close alone therefore cannot cause this exact unconfirmed branch. The original holder release joins that holder's cleanup too. A successful request read does not independently establish operation-gate availability.

The accepted `hookGoneTrace` delegates unchanged and captures count, request/timestamp/maxWait arguments, returned map, elapsed time and underlying error. It changes neither the original single read nor exact assertion. Current failure-only diagnostics additionally capture bounded goroutine stacks and independent read-only raw durable timestamp readback before test cleanup, without a shared Store lease, reconciliation or retry. They run after the failure and may observe later progress; they are not an atomic deadline snapshot. Controlled sentinels validated these diagnostic paths; they do not establish a natural cause.

Measurement-only triage retained one focused natural pass: native COMMIT wall duration 1.453259ms, with separate lease/PRAGMA/BEGIN/UPDATE/cleanup timings. A controlled 1100ms hold **after successful native COMMIT, before result publication** made the original assertion fail with the same `operation_guard` branch while independent readback found the exact timestamp durably stored. This demonstrates reachability and why public nil is not proof of no write. The artificial hold does not identify the original native I/O, response or scheduler cause. Neither this pass nor the final full's nonrecurrence is a fix; no product SQL, retry, timeout or uncertainty-classification change was justified.

Next natural evidence must retain the unchanged failure and associate actual operation phase/error/stacks with independent durable readback. Historical native COMMIT versus response/descheduling cause remains **UNKNOWN**. The separate historical Decide/COMMIT case also remains **UNKNOWN** and is not resolved by this reader evidence.

## Acceptance criteria

- [x] Retain the Task 3 natural instrumented recurrence and separately labelled controlled post-COMMIT reproduction; retain the explicit limits on historical attribution.
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
- [Task 3 natural full recurrence](../e4-task3/fifo-diagnostic/full.log), lines 160–161; original log retained unchanged.
- [Measurement-only phase triage](../e4-task3/phase-triage/REPORT.md), [natural timing log](../e4-task3/phase-triage/hook-natural.log) and [controlled post-COMMIT log](../e4-task3/phase-triage/hook-controlled.log).
- [Failure-only diagnostic verification](../e4-task3/display-rework/REPORT.md), including corrected sentinel raw readback and its initially failed diagnostic SQL attempt.
- [Latest canonical full receipt](../e4-task3/return-full/REPORT.md): passing run, cache limits and unchanged original assertions; no historical-cause or repair claim.
