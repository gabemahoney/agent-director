# b.w21: fixed-model scheduler handoff

Fourteen self-contained contexts are prepared. Every observed result in
[manifest.json](manifest.json) is **NOT_RUN**. Eleven are expected PASS;
one independent reachability probe and two repair mutants are expected
FAIL. Expectations are predictions, not model-checking evidence.

The [original red checkpoint](../README.md), contexts, configurations,
manifest and [receipts](../model-red-receipts.json) remain frozen at their
submitted baseline. These jobs use a separate copy of the modified
`../../src/B146R5Cx.tla`; their exact source, runtime and context hashes
are in this manifest. The historical b.ssz results and phase-1 red
results do not validate this modified model.

## Mapping to the runtime repair

`PreV7ReadRepair` models `spawnRequests.closed` treating a decided pre-v7
request with durable `agent_gone` proof as logically closed. Both relay
and fallen-back holds release without a store write or invented ack.
`PreV7CloseRepair` models the lifecycle close of decided unacked rows with
no completed pane answer, independently of `awaitingAnswerSQL`; durable
closure survives resume. Both knobs default off.

The separate `legacyProof` records durable proof kind rather than using
the dialog's unproven flag as a substitute. `PreV7TurnEnd` seeds prior
`turn_end` proof: the close preserves that proof, while `closed_at`
independently releases a future resumed row. A current `turn_end`-only
request remains relay-held. Read-time repair does not release an
already-stuck earlier-launch row with only `turn_end` and no stored close;
the runtime retains that limitation. Modern live hooks receive no broad
proven-gone bypass.

`B21Spec` focuses on crash, find-missing, relaunch, ageing, pane claims and
plain keys before SessionStart. `Spec` retains the full modeled lifecycle.
Compressed legacy phases do not establish a wall-clock bound.

## Expected outcomes

| Case | Expected | Purpose |
|---|---|---|
| `w21_fixed_initial` | PASS | Full Spec releases an already-stuck seed; checks inherited safety/liveness and `B21RecordsSettle`. |
| `w21_fixed_relaunch` | PASS | Crash/find-missing/resume persists closure and releases pending keys. |
| `w21_fixed_relaunch_probe` | FAIL: `B21RelaunchPendingUnreachable` | Independent probe must still reach resumed pending before SessionStart. |
| `w21_fixed_expired_deny` | PASS | Already-stuck close-deny seed releases both holds; historical delivery remains fallen back. |
| `w21_fixed_expired_deny_live` | PASS | Fair plain keys eventually succeed without manual pane repair. |
| `w21_fixed_pane_write_probe` | PASS | `NeverHit` now holds: logical closure prevents the obsolete request's blocked pane-write path. |
| `w21_fixed_expired_ordinary_control` | PASS | Ordinary legacy expiry keeps historical derived delivery without a fabricated ack. |
| `w21_fixed_current_launch_control` | PASS | Current unproven legacy request remains held. |
| `w21_fixed_no_legacy_control` | PASS | Focused pending keys work without a request. |
| `w21_fixed_turn_end_control` | PASS | Current legacy request carrying only `turn_end` remains relay-held. |
| `w21_fixed_turn_end_relaunch` | PASS | Lifecycle closure preserves prior proof and releases the resumed row. |
| `w21_mutant_close_only` | FAIL: `RelayHoldOnlyCurrentLaunch` | Close repair alone cannot release an already-stuck initial state. |
| `w21_mutant_read_only` | FAIL: `RelayHoldOnlyCurrentLaunch` | Read repair alone cannot release relaunch with preserved `turn_end` and no close. |
| `w21_fixed_modern_full_control` | PASS | Full Spec and inherited `r5x_time` properties without a legacy seed protect modern behavior. |

The relaunch probe must fail independently of the guard, guarding against
vacuous passing recovery checks. The former pane-write reachability probe
now expects PASS because an obsolete request is closed logically before
a pane claim; its old expected failure belongs only to the frozen red model.

## User submission and receipt return

Submit the fourteen exact commands in
[scheduler-commands.txt](scheduler-commands.txt) through the user scheduler.
Every context includes the canonical `tlcjob.sh` and `tla2tools.jar`, its
model, cfg, Dockerfile and runs file. Commands use network off, 4 GiB,
4 CPUs, 15-minute scheduler timeout and a 700-second TLC budget.
Do not execute TLC/JVM or these containers locally.

Return each job ID, scheduler status and complete `run.log`, including
`VERDICT`, `JOB-VERDICT`, distinct-state counts, depth and counterexamples
for expected failures. Review all fourteen outcomes against the manifest.
A scheduler `succeeded` status alone is not a model PASS; parse errors,
timeouts and `ERROR`/`INCOMPLETE` cannot substitute for the expected failure.
Archive full logs and SHA-256 hashes in a separate receipt file and update
observations only after reviewing them. Preserve the original predictions
and frozen red evidence. No state count or equivalence claim is available
for the modified source until those results arrive.

[Local unit receipts](../../../../../test/b.w21/README.md) show red proof
and targeted green, followed by a full sandbox unit PASS: 67 Go packages
(37 cached), 420 Bun tests, zero failures and make exit 0. Docker
integration requires fifteen user-submitted executions; its separate
handoff is being prepared and has no results yet.
