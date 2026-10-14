# b.w21: corrected model scheduler handoff

Six self-contained jobs are ready in [manifest.json](manifest.json), all
**NOT_RUN**. This round corrects a model error; production runtime is unchanged.
The [submitted fourteen-job evidence](../fixed/README.md) remains frozen:
ten PASS, three expected failures and the unexpected `w21_fixed_initial`
`NoRace` failure at 3,835 distinct states, search depth 11
(job `j20261014225806-d7dc0c`).

## Diagnosis and correction

Source comparison maps the condensed trace to lost SessionStart and
UserPromptSubmit writes (`LifeDie`), then `Ask(1)`, `Tick(1)`, `HX(1)`,
`HP(1)`, `HD(1)`, `OrcRead(1)` and `OrcBlindAct(1)`. The current request is
`deny`, ACKed, `h = "e"`, phase 1; the obsolete legacy request is logically
closed. The seeded modern `RelayHolds` time branch used `~ack /\ ph < 2`,
which dropped the current live hook's hold at ACK. The valid-hash bypass
then wrote keys while the hook could still answer, triggering `NoRace`.

The modern branch now uses `ELSE JudgedAlive(r)`, matching the original
`AnyJudgedAlive`/`JudgedAlive` model semantics. Production `hookMayAnswer`
already holds a known-live hook independently of ACK, scans all requests
through `liveHolder`, and runs before hash validation. The shared logical
closure releases only the qualifying decided pre-v7 `agent_gone` row;
it does not release the current modern request.

`NoRace`, every original race assignment, `KnownHookAlive`, `HookAlive`,
original actions and the full Spec's `LiveCheck = "time"` are preserved.
The historical time abstraction conservatively holds recorded modern hooks
until phase 2 even after ACK. It does not separately model the concrete
runtime release of an ACKed hook whose identity cannot be checked. Focused
exact-liveness checks and public API controls cover concrete identity outcomes.

## Submit and expected results

From this worktree root, the User runs:

```sh
bash spec/tla/codex/b.w21/fixed-r2/scheduler-commands.txt
```

| Case | Expected |
|---|---|
| `w21_r2_initial` | PASS: all original submitted full Spec obligations retained |
| `w21_r2_modern_full_control` | PASS: full modern-only control |
| `w21_r2_mixed_acked_live_time` | PASS: mixed-store NoRace and current live-hook hold |
| `w21_r2_mixed_acked_live_exact` | PASS: same focused safety with exact liveness |
| `w21_r2_mixed_acked_live_probe` | FAIL: `B21MixedAckedLiveUnreachable`, proving the ACKed/live mixed state is reachable |
| `w21_r2_mutant_ack_shortcut` | FAIL: `NoRace`, restoring only the faulty modern predicate |

Eleven omitted cases use focused `B21Spec` with no modern Ask/HX transitions.
The twelfth, `w21_mutant_close_only`, uses full `Spec` but fails
`RelayHoldOnlyCurrentLaunch` in `Init`, before any transition exercises the
changed modern `RelayHolds` branch. These twelve cases do not need
resubmission; their prior results remain tied to the submitted bytes.
Return all six full `run.log` files, job IDs, `VERDICT` and `JOB-VERDICT`
lines, counts, depths and counterexamples. Expected probe/mutant failures
must name their intended invariant; parse errors, timeouts and incomplete
runs do not satisfy those expectations. Every result here remains a prediction.

The runtime still matches all 269 production Go files at
`9d025f8f2caf176f3a60421f52ba94b293021bdb`, aggregate SHA-256
`2550ab71219eb05813efd2741a99696926af97f05398a077e8d6777e936a9f79`.
All fifteen integration jobs passed at that same source; no integration
repeat is required for this model-only correction. Preserve their immutable
context and all original red/fixed model contexts and cfgs.

## Static and public API evidence

```sh
python3 spec/tla/codex/b.w21/fixed-r2/verifycontexts.py
```

This checks hashes, source correspondence and context completeness without
executing or parsing TLA+. No corrected model has been run by this worker.

The [unit handoff](../../../../../test/b.w21/README.md) records 32 new public
API cases: pending/check_permission × ACKed live, unACKed unknown before
confirmation, ACKed unknown, ACKed gone × plain, valid hash, stale hash,
obsolete legacy token. Held cases refuse before pane access; released cases
retain hash/dialog/token controls. Targeted sandbox evidence is 118 API/store
and eight artifact cases passing, zero failures/skips. The fresh full sandbox
suite also passed: 67 Go packages (39 cached), 420 Bun tests, zero failures
and make exit 0. Its receipt and count limits are in the unit handoff.
