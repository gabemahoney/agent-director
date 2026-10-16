# DRAFT — Decide: intermittent unconfirmed-commit deadline during semantic test setup

Unfiled draft for the user to file. No bug ticket ID or ticket status is assigned.

## Summary / Incident

On 2026-10-15, full sandbox runs reported four `Decide` unconfirmed-commit errors across two runs: one durable-origin case and three repair-fixture setup cases. The repair failures occurred before repair executed. The historical cause remains UNKNOWN; this draft preserves the residual investigation rather than claiming a deadline fix.

A later closeout full run observed a separate failure in the deliberate fresh-recovery call of `TestDecideAbsoluteContextSurvivesParkedPhase`, explicitly labeled `branch=operation_guard`. This distinguishes the branch for that new observation only. It establishes neither the four historical branches nor a timing root cause or product contract violation.

## Steps to reproduce

Reproduction is intermittent and has not been recovered in the balanced measurements below. These commands describe an isolated developer checkout, never a production store. Do not run test commands directly on the host.

1. Use a disposable checkout of the historical candidate `7b65cf09` (offline-fixture run) or `f0e9b48f` (FIFO-fixture run), both after deadline change `7b6c2e20`, with original test budgets/assertions and the repository's provisioned sandbox dependencies. The offline report identifies its work as based on `7b6c2e20`; candidate revisions identify the corresponding settled changes, not an independently recorded build stamp for each failed call.
2. From that checkout, run `make test-sandbox`. Preserve the complete log and sandbox environment. This was the context of the historical observations; targeted runs alone have not reproduced them.
3. For a narrower observation, run `make sandbox CMD="go test ./pkg/api -run '^(TestRepairCheckPermission|TestDecideKeepsDurableOrigin)$' -count=1 -v -timeout 90s"`. This is an investigation command, not a reliable reproducer.
4. On any new failure, retain the branch marker, original cause, actual call timestamps and correlated SQL/COMMIT/publication/cleanup phases, then independently inspect the durable request and request token before considering another decision. Never replay an uncertain approval.

## Environment

- Development branch: `feature/b.ftd`; historical candidate revisions above. Logs show Linux Docker sandbox execution, Go and Bun suites, Bun 1.3.13, and isolated sandbox HOME. The FIFO wrapper used a masked host HOME and stripped AD/tmux/git configuration before sandbox launch (`pc01-fifo-fix/environment.json`).
- Exact binary `version --json` stamp, database schema/config dump and per-call resource/scheduling trace were not recorded for the historical errors. Do not infer them or inspect real user stores to fill the gap.
- Current diagnostic work: `cfba67ae2a26f5de5a13f65bc58dbebda89bb244` plus two uncommitted diagnostic format-string changes. Product zero-wait execution still has its fixed one-second allowance; no deadline policy was extended.
- Evidence paths in this draft are relative to its containing directory.

## Expected result

Semantic setup should return nil error and retain the asserted durable origin/proof and later repair behavior when work fits its stated budget. A deadline can legitimately exhaust that budget; an unconfirmed result must remain internal, never a retry-safe busy refusal, and must require durable-request inspection. It is not yet established that these historical observations violate the product contract.

## Actual result

Verbatim historical excerpts from `pc01-offline-fix/test-sandbox.log`:

```text
    --- FAIL: TestDecideKeepsDurableOrigin/cursor (9.29s)
        decide_delivery_test.go:595: decide {RequestDelivery:{Delivery: ConfirmBy:0001-01-01 00:00:00 +0000 UTC HookAlive:<nil> HookGoneAt:<nil> AttemptedDecision:<nil> AttemptedAt:<nil> ToolUseID:<nil> PaneAnswer: PaneAs:<nil> ProvenGoneAt:<nil> ProvenGoneHow:<nil> UnprovenSince:<nil>}} store: commit outcome unconfirmed; read the request before retrying: context deadline exceeded
```

Verbatim historical excerpts from `pc01-fifo-fix/test-sandbox.log`:

```text
    --- FAIL: TestRepairCheckPermission/closed,_its_hook_can't_be_checked;_resumed,_idle_Notification_while_B_awaited,_then_B_delivered (6.40s)
        find_missing_repair_test.go:140: decide: store: commit outcome unconfirmed; read the request before retrying: context deadline exceeded
    --- FAIL: TestRepairCheckPermission/closed,_its_hook_can't_be_checked,_before_confirm_by;_resumed_and_B_delivered (6.39s)
        find_missing_repair_test.go:140: decide: store: commit outcome unconfirmed; read the request before retrying: context deadline exceeded
    --- FAIL: TestRepairCheckPermission/codex/a_verdict_closed_by_the_mark,_then_resumed_and_B_delivered (6.50s)
        find_missing_repair_test.go:140: decide: store: commit outcome unconfirmed; read the request before retrying: context deadline exceeded
```

These durations cover whole subtests, not individual Decide calls. Historical Decide call timings were not recorded. Both unconfirmed branches then emitted identical text, so the historical failure branch is UNKNOWN.

Verbatim new closeout excerpt from `commit-closeout-full/full.log`:

```text
--- FAIL: TestDecideAbsoluteContextSurvivesParkedPhase (9.99s)
    decide_absolute_deadline_test.go:58: deliberate fresh-budget decision: store: commit outcome unconfirmed; read the request before retrying (branch=operation_guard): context deadline exceeded
```

The new marker identifies the outer operation guard for this call. The 9.99 s duration covers the whole test; the exact Decide call duration was not measured. The preserved run failed with 66 passing Go packages, two failing Go packages and 13 packages without tests; Bun passed 430 tests with zero failures. Its other failure was the Cursor mailbox refill fixture. The original failed receipt remains in `commit-closeout-full/test-writer-report.json`; targeted repair success is separate evidence, not a full-suite pass.

## Why investigate

The failures prevent semantic tests from reaching their intended assertions and leave commit status uncertain. Possible explanations include legitimate budget exhaustion, performance/resource effects or a product race. None is established as the historical cause or a proven contract violation. A larger fixture budget does not resolve that uncertainty.

## Root cause — UNKNOWN

`DEADLINE-INVESTIGATION-RESULT.json` and the `deadline-measurement`, `deadline-fullcontext`, `deadline-fullsuite-trace` and `deadline-cpuquota` reports/datasets retain the measured evidence. Matched `64a35baf` and `f0e9b48f` populations each ran 345 repetitions of 36 original leaves (12,420 outcomes per revision), including full API coverage and verified four-CPU quota runs: zero failures on each revision. There is no observed failure at `64a35baf`; zero observations prove neither absence nor regression. Additional HEAD samples are outside that balanced comparison.

The direct slow-trail fault (`original-slow-trail/RESULT.json`) returned nil error and `not_confirmed` in 1.00079228 s, with an independently reopened durable deny/origin/proof check completed before the real writer mutex was released at 1.10010628 s. It did not reproduce the historical error. `trail-hypothesis-measure/REPORT.md` partitions the approximately 950 ms caller wait mostly into queue and writer-mutex waiting; all 416 bounded SQL results were published before trail emission. This supplies no direct causal trail RED and no historical root fix. Indirect shared-resource effects remain unproven.

Current diagnostic labels distinguish `branch=commit_exec` (COMMIT ExecContext returned failure) and `branch=operation_guard` (caller returned before operation result publication after COMMIT started). Neither proves rollback or that COMMIT did not happen. Both retain the common unconfirmed prefix, cause wrapping, internal classification and durable inspection advice. `commit-branch-red/test-writer-report.json` records exactly two missing-marker failures; `commit-branch-green/test-writer-report.json` records independent unchanged GREEN for eight store leaves and 36 API leaves. This validates diagnostic distinction, not a historical root-cause repair.

The semantic setup decisions in TestRepairCheckPermission and TestDecideKeepsDurableOrigin request a finite 5000 ms budget, with simulated acknowledgement waits advancing the injected clock. Existing nil-error, not_confirmed, reopened durable-origin/proof and repair assertions remain. Following the new failure, only the later fresh-recovery call in TestDecideAbsoluteContextSurvivesParkedPhase changed from a direct 500 ms call with a frozen injected clock to the existing decideWith helper with an explicit 5000 ms semantic budget and simulated acknowledgement clock advancement. Its initial 100 ms call, 1100 ms wall bound, absolute-context/stale-relative and precommit assertions, and durable allow/token readback remain byte-for-byte unchanged. The whole deadline test file is therefore no longer unchanged; `commit-closeout-fixture-repair/decide_absolute_deadline_test.go.patch` retains the exact one-line change and historical receipts are not rewritten. Other zero-wait, SQL, cleanup and trail deadline coverage and product deadlines remain unchanged, including the fixed one-second zero-wait execution allowance. No retries or unbounded waits were added.

The separate Cursor fixture repair waits for each actual admission under the existing three-second bound and retains the first successful claim until the entire exact batch is admitted. A protected `turn_outcome` record precedes Stop reportWaiting and enableQueue, so it is not an admission barrier. Capacity, FIFO, cancellation, explicit refused-text resubmission and no-replay assertions remain unchanged; no production change is involved. `commit-closeout-fixture-repair/test-writer-report.json` records targeted Cursor, API and store success with preservation hashes. Full sandbox validation and the user's 17-group acceptance gate remain required. The historical residual remains open for investigation by the user, without filing a ticket here.

## Acceptance criteria

- [ ] Capture an actual failure branch with original cause, actual Decide call timing and correlated acquisition/SQL/COMMIT/result-publication/cleanup evidence, plus independent durable-request/token state. Preserve raw evidence and environment/provenance; do not infer the historical branch from text.
- [ ] Establish an evidence-backed cause and either a scoped fix with causal RED/GREEN or documented capacity/budget closure explaining legitimate exhaustion and its operating conditions. If the original failure stays unreproduced, explicitly retain UNKNOWN rather than claiming it fixed.
- [ ] Preserve original promised deadlines, zero-wait policy, internal unconfirmed classification and inspection-before-retry behavior. Add no automatic retries, swallowed errors, uncertain-approval replay or renewed deadlines.
- [ ] Retain semantic assertions with finite fixture budgets and dedicated deadline coverage; report measured timing separately from whole-subtest duration.

## Out of scope

Other agents' product work, E4, diagnosis of Cursor UI behavior, live Cursor/account activity, production store access, and filing this draft automatically. Full sandbox and the user's 17-group acceptance gate belong to closeout validation; no such new passing receipt is claimed by this documentation pass.
