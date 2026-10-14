# b.w21: current-rule model checkpoint

Phase 1 only: model the upgraded-store bug before changing Go code or adding
regressions. Every case in [manifest.json](manifest.json) is `NOT_RUN`.
Expected failures below are predictions, not observed counterexamples. Neither
TLC parsing nor model execution has occurred; product behavior is unchanged.

## Model fidelity and limits

[`B146R5Cx.tla`](../src/B146R5Cx.tla) adds five default-off knobs:
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

All nine observed results are **NOT_RUN**. Six expected failures cover four
bug checks and two reachability probes; three controls are expected to pass.
`PASS` means only the properties and scope of that configuration;
the current-launch control isolates relay
rule 1 with `R5 = {}` and does not permit relaunch.

| Case | Spec | Expected result and purpose |
|---|---|---|
| `w21_initial_red` | `Spec` | `RelayHoldOnlyCurrentLaunch` fails in Init: an already-stuck earlier-launch row holds pending keys before SessionStart. |
| `w21_relaunch_red` | `B21Spec` | `RelayHoldOnlyCurrentLaunch` fails; shortest expected trace is Init → AgentCrash → FMMissing → Relaunch. |
| `w21_relaunch_probe` | `B21Spec` | `B21RelaunchPendingUnreachable` fails only after Init → AgentCrash → FMMissing → Relaunch reaches pending before SessionStart. This is an independent reachability probe. |
| `w21_expired_deny_red` | `B21Spec` | `OldRequestsDoNotHoldPlainKeys` fails in Init: an expired close denial is still open and fallen back. |
| `w21_expired_deny_live_red` | `B21Spec` | Temporal failure of `B21KeysEventuallyAccepted`: fair pane claims cannot update the decided legacy row. |
| `w21_pane_write_probe` | `B21Spec` | `NeverHit` fails by reaching `legacyclaimblocked`: the pane write matches no row. This is a reachability probe. |
| `w21_expired_ordinary_control` | `B21Spec` | PASS: an ordinary decided legacy row derives delivered after expiry and permits focused pending keys. |
| `w21_current_launch_control` | `B21Spec` | PASS: current-launch legacy requests may hold before expiry; the launch invariant does not prohibit that. |
| `w21_no_legacy_control` | `B21Spec` | PASS: all b.w21 knobs off, no request, focused pending keys proceed. |

The relaunch probe checks launch provenance and lifecycle state, independently
of relay or fallen-back holds. Retain its expected failure after the future
fix: a passing bug check must still permit the resumed pending state to be
reached before SessionStart.

## User scheduler handoff

Run each exact submission command in
[scheduler-commands.txt](scheduler-commands.txt), one command per job, from
the user scheduler environment. Those nine commands use the absolute
contexts in this worktree, channel `C0B3X876XSB`, network off, 4 GiB memory,
4 CPUs and a 15-minute timeout. Each context is self-contained and uses the
existing `tlcjob.sh`; its run has a 700-second TLC budget. Do not execute
TLC, the JVM, container engines, tests or repository binaries on the host.
No scheduler job has been submitted for this checkpoint.

Return the case name, scheduler job ID and complete `run.log` for each job,
including `VERDICT`, `JOB-VERDICT`, state counts and condensed counterexample
where present. A parser error, timeout, disk cap or other `ERROR`/`INCOMPLETE`
result is not proof of the predicted failure. An unexpected invariant or
trace needs review before the code phase. Keep returned observations
separate from the expected results; the manifest records the unrun handoff.

This command checks file hashes, context/source equality, configuration
symbol names, knob defaults, nine-case membership and scheduler-command
consistency. Its added text checks cover the `nl` initializer and sole
relaunch assignment, crash/find-missing/relaunch wiring, scoped settlement
and inherited-property replacement, and the relaunch probe's configuration
and independence from hold predicates:

```sh
python3 /home/horde/projects/agent-director-project/agent-director-w21-tla/spec/tla/codex/b.w21/verifycontexts.py
```

It invokes no TLC, JVM, scheduler, container or product binary and provides
no parsing or execution evidence.

## Next phase after results return

Stop here for user scheduler results. Once the current-rule failures and
controls are confirmed, fix the code and add upgraded-store and expired-hold
regressions, proving them red on unfixed code through user scheduler jobs.
Then check the fixed model, `make test-sandbox` unit suite and every Docker
test group through the scheduler. No Go source or test changes belong to
this checkpoint.

The final fix must automatically release rows **already stuck in existing
stores**, through read-time handling or a one-time close, without a manual
repair. The final report must answer whether `ErrSendKeysWhileRelayed` can
still hold pending rows before SessionStart for minutes, and in which cases.
That answer is **pending code-fix verification**; the compressed model
phases and current-launch control do not establish the post-fix timing or
contract. Update affected customer and architecture docs after that
behavior is implemented and verified.
