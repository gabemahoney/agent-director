# b.3ie: scheduler jobs

Thirty-two jobs, one TLC run each, of `B3ieSettle.tla` (the settle-time model; see `README.md`).
Each directory is self-contained: `Dockerfile` (from `job-Dockerfile`), `tlcjob.sh` and
`tla2tools.jar` (byte-identical to the repo's `spec/tla/jobs/lib/tlcjob.sh` and
`spec/tla/tla2tools.jar`), `B3ieSettle.tla` (byte-identical to the master here), one cfg (a copy of
`cfg/<name>.cfg`) and `runs`. `gen-jobs.sh` wrote them and `verify-jobs.sh` checks them (32 OK).

The model is small. The one local sanity run (`tla-logs/sanity.log`: the design, T = 2, MaxTime 3,
MaxWrites 3, every invariant) found **158,868 distinct states in 6 s** under a 900 MB container.
The green runs use MaxTime 4 and MaxWrites 3 or 4 (expected up to the low tens of millions of
states, minutes). Red, control, limit and probe runs stop at their first violation. The Dockerfile
gives TLC `-Xmx8g`, 1 GB direct memory, a 3000 s budget (green runs cap themselves at 2900 s, the
rest at 900 s) and an 8 GB disk cap. The last output line is the verdict:
`JOB-VERDICT <cfg>=<result>/<distinct states>`.

Bounds unless the row says otherwise: 2 hook processes, T in 0..2, initial T = 2, MaxTime 4,
MaxWrites 3, migrated rows starting `waiting` or `working`, hooks write
`waiting`/`working`/`check_permission`/`ended`, commands on, guards on, reader clock inline,
hooks may move a terminal row (as the source allows).

| Job directory | Run | Expected |
|---|---|---|
| `job-pass_const` | the design, T = 2 constant, MaxWrites 4, fresh inserts too; every invariant | PASS |
| `job-pass_const_t1` | the same at T = 1 | PASS |
| `job-pass_t0` | the same at T = 0 (off) | PASS |
| `job-pass_before` | the design, reader takes its clock before the row read (negative ages reachable) | PASS |
| `job-pass_noterm` | the design with the guard it assumed (no hook moves a terminal row) | PASS |
| `job-pass_immnostamp` | robustness: immediate writes leave `state_changed_ms` alone (a verbatim reuse restore) | PASS |
| `job-pass_tchange` | the design, T starts at 1 and changes twice; invariants 1, 3, 4, 5 (2 holds until the first change) | PASS |
| `job-pass_tchange_before` | the same with the reader clock before the row read | PASS |
| `job-red_strictgt` | inv 1: `age > T` with no T = 0 short-cut, at T = 0 | FAIL `ZeroIsRaw` |
| `job-red_prevraw` | inv 2: the base is the previous raw value (A→B→C reads B) | FAIL `SettledIsSpec` |
| `job-red_samerestart` | inv 2: a same-value hook write restarts the clock | FAIL `SettledIsSpec` |
| `job-red_stalebase` | inv 2: base and same-value test from a Go-side read before the UPDATE (duplicate hooks) | FAIL `SettledIsSpec` |
| `job-red_nowoutside` | inv 2: the writer's `now` is taken before the write lock | FAIL `SettledIsSpec` |
| `job-red_negage` | inv 2 (reads): a negative age counts as settled | FAIL `ReadsLinearizable` |
| `job-red_readafter` | inv 2 (reads): the reader reads the row, then takes its clock | FAIL `ReadsLinearizable` |
| `job-red_termsettles` | inv 3: a hook's `ended` goes through the settle rule | FAIL `ImmediateAtOnce` |
| `job-red_cmdsettles` | inv 3: command writes go through the settle rule | FAIL `ImmediateAtOnce` |
| `job-red_guardreported` | inv 4: a guard acts on the reported state | FAIL `GuardsReadRaw` |
| `job-red_insertnobase` | inv 5 (T changing): InsertPending leaves `settled_state` at its default `''` | FAIL `NeverUnsupported` |
| `job-ctl_today` | control: today (reported = raw) at T = 2, in the bee's form | FAIL `HeldOrLastSettled` |
| `job-lim_tchange_bee` | limit: after a T change, a base judged under the old T is reported | FAIL `BeeFormReaderT` |
| `job-probe_flap` | probe: A→B→A within T is reached and a read returns A | FAIL `ProbeFlap` |
| `job-probe_shortturn` | probe: waiting→working→waiting within T reads waiting | FAIL `ProbeShortTurn` |
| `job-probe_holdsold` | probe: a read during an unsettled hook change returns the old settled value | FAIL `ProbeHoldsOld` |
| `job-probe_chaina` | probe: A→B→C within T reads A | FAIL `ProbeChainHoldsA` |
| `job-probe_chainc` | probe: A→B→C reads C once C held T from the B→C change | FAIL `ProbeChainReachesC` |
| `job-probe_dup` | probe: a duplicate hook write (same value, other hook, ≤ 1 ms) lands | FAIL `ProbeDup` |
| `job-probe_twohooks` | probe: two hook processes in flight with the same value | FAIL `ProbeTwoHooks` |
| `job-probe_tchange` | probe: T changes between a hook write and a read | FAIL `ProbeTChangeRead` |
| `job-probe_guarddiff` | probe: a guard acts on raw while the reported state differs | FAIL `ProbeGuardDiffers` |
| `job-probe_hookleavesterm` | probe: a hook moves a terminal row to a live state | FAIL `ProbeHookLeavesTerminal` |
| `job-probe_cmdcuts` | probe: a command write lands while a hook change is unsettled | FAIL `ProbeCmdCutsSettle` |

A FAIL verdict reads `FAIL(Invariant_<Name>_is_violated)`. Each red cfg lists, besides its target,
the invariants the mutation must not break; a verdict naming any other invariant is a finding.
`red_insertnobase` fails in the initial state (a freshly inserted row read within T), so its log
carries no condensed trace.

The spec copy in each directory must match `B3ieSettle.tla` here; re-run `gen-jobs.sh` after any
change to it, then `verify-jobs.sh`.
