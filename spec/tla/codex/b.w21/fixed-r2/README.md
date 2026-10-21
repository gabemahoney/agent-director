# b.w21: corrected model results

All six cases in [manifest.json](manifest.json) match their predictions:
four PASS and two intended invariant failures. Seven user-run scheduler
attempts are preserved in [model-r2-receipts.json](model-r2-receipts.json),
including the initial incomplete attempt and the completed rerun. This round
corrects a model error; production runtime is unchanged.
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

## Observed results

Counts and depths are the exact log values. Scheduler completion alone is not
a model PASS. The first initial attempt exhausted its time budget; only the
separate final rerun establishes the initial case's PASS.

| Case / attempt | Observed | Distinct / depth | Seconds | Scheduler job |
|---|---|---|---|---|
| `w21_r2_initial`, original | INCOMPLETE(time-budget) | `2000377 / >=26` | 703 | `j20261015000702-bf3dab` |
| `w21_r2_modern_full_control` | PASS | `296382 / 35` | 57 | `j20261015000702-e3dee4` |
| `w21_r2_mixed_acked_live_time` | PASS | `29 / 14` | 7 | `j20261015000702-38ad71` |
| `w21_r2_mixed_acked_live_exact` | PASS | `27 / 11` | 7 | `j20261015000702-4884e8` |
| `w21_r2_mixed_acked_live_probe` | FAIL: `B21MixedAckedLiveUnreachable` | `24 / 9` | 7 | `j20261015000703-bb16b3` |
| `w21_r2_mutant_ack_shortcut` | FAIL: `NoRace` | `31 / 12` | 7 | `j20261015000703-99fcf3` |
| `w21_r2_initial`, resource rerun | PASS | `6289976 / 43` | 2555 | `j20261015001930-15ca4e` |

The final initial rerun completed the state space with no error and checked
all 22 configured obligations: 17 invariants and five temporal properties,
including `NoRace`, `RelayHoldOnlyCurrentLaunch`, `B21ProofFidelity` and
`B21RecordsSettle`. The modern-only control also completed its temporal
checks. The reachability probe and ACK-shortcut mutant fail their intended
invariants; their counterexamples are retained in the archive.

## Resource rerun and evidence identity

The original six contexts, cfgs, predictions, hashes and
[scheduler commands](scheduler-commands.txt) remain byte-identical to
`b9f73c45f3802feb8cc02a9b03f4873f4dd859bb`; the commands' absolute
`--context` and scheduler paths name the original checkout and host. Their
historical `NOT_RUN` comments describe preparation, not current
observations. The manifest's `resource_overlay` records the separate rerun
context `/tmp/claude-1000/ad-codex/w21-r2-initial-big` (a historical record
from the original host; it is not in the repo), all six actual file hashes,
and the exact changed Dockerfile and `runs` bytes. The model, cfg, jar and
runner are identical to the original initial context; no obligation, model
behavior or configuration was weakened.

| Resource | Original attempts | Initial-case rerun |
|---|---|---|
| JVM heap / direct memory | `XMX=2500m`, `DIRECT=256m` | `XMX=16g`, `DIRECT=4g` |
| Runner total / per-run budget | `BUDGET=750`, `@700` seconds | `BUDGET=10200`, `@10000` seconds |
| Runner disk cap | `DISKCAP_MB=1500` | `DISKCAP_MB=30000` |
| Scheduler memory | 4 GiB (`4294967296` bytes) | 24 GiB (`25769803776` bytes) |
| Scheduler timeout | 900 seconds | 10800 seconds |
| Scheduler CPUs / runtime network | 4 / off | 4 / off |

All seven metadata records report `succeeded`, build exit 0 and run exit 0;
the original incomplete attempt is still incomplete as a model result.
The archive contains the exact user receipt and seven full run logs with
SHA-256 hashes. Job metadata includes only the verifier's allowlisted fields,
plus the source path and hash of each whole `job.json`; it excludes environment
and container-inspect data. Static verification compares the current context
bytes and safe scheduler metadata with the archived records. Its comparisons
with the live scheduler logs and `job.json`, the rerun context and the ticket
receipt run only where those original files still exist; elsewhere only
archived copies are checked.
The scheduler's context tar hash is opaque here: these checks do not establish
independent image-time identity. Each verdict applies to its archived model
and configuration, with the time-abstraction limit described above.

Eleven omitted cases use focused `B21Spec` with no modern Ask/HX transitions.
The twelfth, `w21_mutant_close_only`, uses full `Spec` but fails
`RelayHoldOnlyCurrentLaunch` in `Init`, before any transition exercises the
changed modern `RelayHolds` branch. These twelve cases do not need
resubmission; their prior results remain tied to the submitted bytes.

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

This checks frozen hashes, source correspondence, all seven archived attempts,
allowlisted metadata, resource differences and final prediction matches without
executing or parsing TLA+. Model execution was performed by the User through
the scheduler; no new job is required. It passes only in a full-history
checkout whose production runtime still matches `9d025f8f` and whose canonical
`src/B146R5Cx.tla` still matches the corrected model recorded in
[manifest.json](manifest.json) (`source_sha256`); once either changes, it
fails by design.

The [unit handoff](../../../../../test/b.w21/README.md) records 32 new public
API cases: pending/check_permission × ACKed live, unACKed unknown before
confirmation, ACKed unknown, ACKed gone × plain, valid hash, stale hash,
obsolete legacy token. Held cases refuse before pane access; released cases
retain hash/dialog/token controls. Targeted sandbox evidence is 118 API/store
and eight artifact cases passing, zero failures/skips. The historical round-2 full sandbox
suite also passed: 67 Go packages (39 cached), 420 Bun tests, zero failures
and make exit 0. Its receipt and count limits are in the unit handoff.

The merge of main `728180eb` retains its minimum-version and idle-hook docs
and tests alongside the recovery contract. Its fresh full `make test-sandbox`
passed with exit 0: 67 Go packages (39 cached), 12 without tests and 420 Bun
tests across 57 files, zero failures. All 714 test-file hashes were unchanged
during the run; the unit handoff records its source identity and count limits.
