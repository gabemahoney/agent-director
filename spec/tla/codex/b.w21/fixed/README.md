# b.w21: submitted fixed-model results

All fourteen user-scheduler results at source
`9d025f8f2caf176f3a60421f52ba94b293021bdb` are archived in
[model-fixed-receipts.json](model-fixed-receipts.json), including the original
manifest, receipt text, complete logs and SHA-256 hashes.
[manifest.json](manifest.json) records **MISMATCH**: ten PASS results,
three expected failures and one unexpected `NoRace` failure. Thirteen of
fourteen predictions match. The separate [round-2 results](../fixed-r2/README.md)
establish four corrected-model PASS results and the two intended invariant
failures; this original mismatch remains historical evidence.

The submitted `cfg/`, `jobs/`, predictions and context hashes remain unchanged.
[scheduler-commands.txt](scheduler-commands.txt) is the historical submission
record; the round-2 archive records the affected cases and resource rerun.
The original nine
[red contexts and receipts](../README.md) also remain frozen.

## Expected and observed outcomes

Counts and search depths below are exactly the log values; `?` remains unknown.
PASS applies only to the submitted configuration and model bytes.

| Case | Original prediction | Observed | Distinct / depth | Scheduler job |
|---|---|---|---|---|
| `w21_fixed_initial` | PASS | FAIL: NoRace | `3835 / 11` | `j20261014225806-d7dc0c` |
| `w21_fixed_relaunch` | PASS | PASS | `54 / 10` | `j20261014225806-86ae84` |
| `w21_fixed_relaunch_probe` | FAIL: B21RelaunchPendingUnreachable | FAIL: B21RelaunchPendingUnreachable | `21 / 6` | `j20261014225806-a10f05` |
| `w21_fixed_expired_deny` | PASS | PASS | `2 / 2` | `j20261014225806-8a1739` |
| `w21_fixed_expired_deny_live` | PASS | PASS | `2 / 2` | `j20261014225807-8b13c1` |
| `w21_fixed_pane_write_probe` | PASS | PASS | `2 / 2` | `j20261014225807-be5750` |
| `w21_fixed_expired_ordinary_control` | PASS | PASS | `2 / 2` | `j20261014225807-67dc2b` |
| `w21_fixed_current_launch_control` | PASS | PASS | `4 / 4` | `j20261014225807-6c7112` |
| `w21_fixed_no_legacy_control` | PASS | PASS | `2 / 2` | `j20261014225807-ba158d` |
| `w21_fixed_turn_end_control` | PASS | PASS | `4 / 4` | `j20261014225807-5e8dad` |
| `w21_fixed_turn_end_relaunch` | PASS | PASS | `31 / 9` | `j20261014225807-85fe00` |
| `w21_mutant_close_only` | FAIL: RelayHoldOnlyCurrentLaunch | FAIL: RelayHoldOnlyCurrentLaunch | `? / ?` | `j20261014225807-3b2e06` |
| `w21_mutant_read_only` | FAIL: RelayHoldOnlyCurrentLaunch | FAIL: RelayHoldOnlyCurrentLaunch | `21 / 9` | `j20261014225808-078ef6` |
| `w21_fixed_modern_full_control` | PASS | PASS | `296382 / 35` | `j20261014225808-d196d2` |

The independent relaunch probe and both repair mutants failed their intended
invariants. `w21_fixed_modern_full_control` passed with 296,382 distinct
states, but its modern-only seed never exercises the faulty mixed-store
branch. Eleven omitted cases use focused `B21Spec` with no fresh modern
Ask/HX transitions. The twelfth, `w21_mutant_close_only`, uses full `Spec`
but fails `RelayHoldOnlyCurrentLaunch` in `Init`, before any transition
exercises the changed modern `RelayHolds` branch. Their receipts remain
evidence for these frozen source bytes and are not claims that the corrected
model has been checked.

## Model/runtime mapping and diagnosis

`PreV7ReadRepair` models logical closure of a decided pre-v7 request carrying
durable `agent_gone` proof, releasing relay and fallen-back holds without a
store write or invented ack. `PreV7CloseRepair` models persistent lifecycle
closure of decided unacked rows without a completed pane answer, independently
of awaiting-answer selection. The separate `legacyProof` preserves proof kind;
read-time repair does not release an already-stuck earlier-launch `turn_end`-only
row with no stored close. Both repair knobs default off.

The unexpected trace reaches a modern denied, acknowledged request at
`h[1] = "e"`, `ph[1] = 1`, then plain keys set `race = TRUE`. Source comparison
identifies a model error: the seeded modern time branch of `RelayHolds` used
`~ack[r] /\ ph[r] < 2`, dropping the hold while the hook was still alive.
Production `hookMayAnswer` holds a known-live hook independently of ACK,
`liveHolder` scans all requests, and that guard runs before any hash bypass.
The round-2 correction restores `ELSE JudgedAlive(r)`; it changes no runtime
code, `NoRace`, race assignment or original action. See the
[round-2 diagnosis and limits](../fixed-r2/README.md).

`B21Spec` focuses on crash, find-missing, relaunch, ageing, pane claims and
plain keys before SessionStart; `Spec` retains the full modeled lifecycle.
Compressed phases establish no wall-clock bound. The historical time
abstraction conservatively holds recorded modern hooks until phase 2,
including ACK; concrete ACKed/unknown release is covered by public API tests,
not separately represented by this time abstraction.

## Static verification and remaining gates

```sh
python3 spec/tla/codex/b.w21/fixed/verifycontexts.py
```

This verifies frozen contexts, preserved predictions and archived logs without
TLC, parsing, JVM, container or scheduler execution. A model verdict is distinct
from scheduler completion; the `NoRace` failure is not a passing model result.

All fifteen [integration run receipts](../../../../../test/b.w21/integration/README.md)
PASS against the unchanged runtime and source/context identity at `9d025f8f`.
The earlier full sandbox unit PASS (67 Go packages, 420 Bun tests) remains
historical; mixed-store targeted tests and the historical round-2 full sandbox suite
passed (67 Go packages, 39 cached; 420 Bun tests; zero failures; make exit 0). See the
[overall evidence and review limits](../../../../../test/b.w21/README.md).
