# b.w21: current-rule model checkpoint

Phase 1 results are recorded for the unfixed model at commit `8c6998d4`.
All nine user-scheduler jobs returned the expected outcomes: four bug-check
failures, two reachability-probe failures and three passing controls.
[manifest.json](manifest.json) records `ALL_EXPECTED`; the original predictions,
scheduler receipt and full logs with SHA-256 hashes are archived in
[model-red-receipts.json](model-red-receipts.json).
The model-failure gate is met. Go regressions are being written on unfixed code;
no regression execution or product repair has occurred at this checkpoint.

## Model fidelity and limits

The [baseline `B146R5Cx.tla`](jobs/w21_initial_red/B146R5Cx.tla) has five default-off knobs:
`PreV7Old` seeds a decided, unacked pre-v7 request; `PreV7Current` makes it
belong to the current launch; `PreV7Relaunch` permits one relaunch;
`PreV7Expired` starts past the delivery cutoff; `PreV7CloseDeny` selects a
`find_missing`/`ended` close denial instead of an ordinary verdict.
The launch-provenance ghost `nl` records an earlier launch; it starts as
`PreV7Old /\ ~PreV7Current` and only `Relaunch` sets it true thereafter.
It is not a new store column. Existing configurations leave these knobs off;
equivalence with their historical model runs has not been rechecked.

The legacy branches represent the current delivery, close and pane-write
predicates. A decided legacy request does not match `StoreAwaits`, so the
modelled close and pane writes leave it open. Before expiry its delivery is
`not_confirmed`; after expiry an ordinary verdict derives `delivered`, while
a close denial derives `fallen_back`. Proven-gone evidence and launch
provenance do not release the current legacy relay guard.

`RelayHoldOnlyCurrentLaunch` requires an earlier-launch legacy request to
stop holding the relay guard. `OldRequestsDoNotHoldPlainKeys` also requires
it to stop blocking through the fallen-back branch. `B21Spec` focuses on the
startup slice: crash, find-missing, relaunch, ageing, pane claims and plain
keys. It suspends SessionStart and new requests. `AgentCrash` abstracts
agent loss for the kill/find-missing/resume variant; it does not model the
full kill verb or process tree. Legacy phases 0/1 compress the hold and
phase 2 represents expiry; they give no minutes-to-hours bound.

`w21_initial_red` replaces its inherited `RecordsClose` property with
`B21RecordsSettle`: eventually, permanently, recorded nonlegacy requests
must be closed and recorded legacy requests must release both relay and
fallen-back holds. This permits a future read-time repair without requiring
stored closure of legacy rows. The original `RecordsClose` definition and
the configuration's other inherited checks remain unchanged.

## Expected and observed results

These are observed scheduler results for the immutable current-rule contexts.
`PASS` means only the properties and scope of that configuration;
the current-launch control isolates relay
rule 1 with `R5 = {}` and does not permit relaunch.

| Case | Scheduler job ID | Observed result | Distinct states / depth |
|---|---|---|---|
| `w21_initial_red` | `j20261014212739-5f4e9e` | FAIL: `RelayHoldOnlyCurrentLaunch` in Init | `? / ?` |
| `w21_relaunch_red` | `j20261014212739-63c424` | FAIL: `RelayHoldOnlyCurrentLaunch` after relaunch | `26 / 7` |
| `w21_relaunch_probe` | `j20261014212739-65b1d1` | FAIL: `B21RelaunchPendingUnreachable` (reachability probe) | `21 / 6` |
| `w21_expired_deny_red` | `j20261014212739-fca35b` | FAIL: `OldRequestsDoNotHoldPlainKeys` in Init | `? / ?` |
| `w21_expired_deny_live_red` | `j20261014212739-50ab4a` | FAIL: temporal property `B21KeysEventuallyAccepted` | `6 / 5` |
| `w21_pane_write_probe` | `j20261014212739-e0fbf5` | FAIL: `NeverHit` (reachability probe) | `7 / 6` |
| `w21_expired_ordinary_control` | `j20261014212740-fdce80` | PASS | `2 / 2` |
| `w21_current_launch_control` | `j20261014212740-941d72` | PASS | `4 / 4` |
| `w21_no_legacy_control` | `j20261014212740-364124` | PASS | `2 / 2` |

All jobs have scheduler status `succeeded`; the model verdict is recorded
separately above. Each `VERDICT` reports 7 seconds. The two initial-state
failures report `distinct=? depth=?`; those counts remain unknown. Depth is
the reported search depth, not the counterexample length.
The temporal job reports
`FAIL(Temporal_property_B21KeysEventuallyAccepted_was_violated)`, matching
the meaning of the original generic prediction
`FAIL(Temporal_properties_were_violated)`; both strings are preserved.

The initial failure shows an already-stuck earlier-launch row holding pending
keys before SessionStart. The relaunch failure and independent probe show
Init → AgentCrash → FMMissing → Relaunch reaching that pending state.
The expired-deny failure shows a fallen-back hold in Init; the temporal
counterexample and pane-write probe show that pane claims cannot update the
decided legacy row and can reach `legacyclaimblocked`. The ordinary-expired
control permits focused pending keys, the current-launch control permits a
hold before expiry, and the no-legacy control permits focused pending keys
without a request. These checks do not establish a post-fix timing bound.

The relaunch probe checks launch provenance and lifecycle state, independently
of relay or fallen-back holds. Retain its expected failure after the future
fix: a passing bug check must still permit the resumed pending state to be
reached before SessionStart.

## Scheduler evidence and baseline preservation

The submitted commands are preserved in
[scheduler-commands.txt](scheduler-commands.txt). Those nine commands use the absolute
contexts in this worktree, channel `C0B3X876XSB`, network off, 4 GiB memory,
4 CPUs and a 15-minute timeout. Each context is self-contained and uses the
existing `tlcjob.sh`; its run has a 700-second TLC budget. Do not execute
TLC, the JVM, container engines, tests or repository binaries on the host.

The receipt archive contains every complete `run.log`, including `VERDICT`,
`JOB-VERDICT`, state counts and counterexamples, together with the original
unrun manifest and inherited configuration. Observations are separate from
the preserved predictions. No result is a parser error, timeout or
`ERROR`/`INCOMPLETE` outcome.

The nine `cfg/` files and submitted `jobs/` contexts remain byte-identical to
commit `8c6998d4`. The verifier anchors the baseline model and context hashes
to that commit and the archived inherited configuration; it does not compare
the baseline against the evolving working model in `../src/`.
`mkcontexts.py` refuses to overwrite existing artifacts or observations.
Create separate contexts and manifests for the fixed model and its receipts.

This command checks baseline hashes, archived receipts and logs,
configuration symbol names, knob defaults, nine-case membership and
scheduler-command consistency. Its text checks cover the `nl` initializer and sole
relaunch assignment, crash/find-missing/relaunch wiring, scoped settlement
and inherited-property replacement, and the relaunch probe's configuration
and independence from hold predicates:

```sh
python3 /home/horde/projects/agent-director-project/agent-director-w21-tla/spec/tla/codex/b.w21/verifycontexts.py
```

It invokes no TLC, JVM, scheduler, container or product binary. It verifies
the archived execution evidence without parsing or rerunning the model.

## Remaining code and validation gates

The current-rule failures and controls are confirmed. Upgraded-store and
post-24-hour regressions must still be proven red on unfixed code before
product repair. Follow the [unit-red handoff](../../../../test/b.w21/README.md)
on a separate disposable test host with a container engine; the scheduler's
job containers cannot run `make test-sandbox` unchanged. Regression
red/green results, fixed-model results, the `make test-sandbox` unit suite
and every Docker test group remain pending. This phase-1 evidence does not
claim a product fix or a passing Go suite.

The final fix must automatically release rows **already stuck in existing
stores**, through read-time handling or a one-time close, without a manual
repair. The final report must answer whether `ErrSendKeysWhileRelayed` can
still hold pending rows before SessionStart for minutes, and in which cases.
That answer is **pending code-fix verification**; the compressed model
phases and current-launch control do not establish the post-fix timing or
contract. Update affected customer and architecture docs after that
behavior is implemented and verified.
