# b.xjn: subagent records in the model

Bee b.xjn makes agent-director track an agent's subagents through Claude Code
hooks. Subagent hooks (SubagentStart and SubagentStop, plus the launch request
and launch result of the `Agent` tool and the Stop hook's in-flight snapshot)
write subagent records through the existing hook gate: the hook's parent pid
and start time must equal the row's recorded pane process. A record write
touches only the row's current launch and never an `ended` or `missing` row.
Ending the row (SessionEnd), find-missing's `missing` mark and a new launch
(resume or reuse) close every running record as lost, in their own
transaction. The design is in the bee (Idea b.xjn, outside this repo).

This directory holds the cfgs and manifest that check that design with the
vendored model. These runs are not in `ci/suite.tsv`, so a plain `make tla`
does not run them. To run them on the job scheduler, use
`make tla TLA_SUITE=spec/tla/subagent/suite.tsv`. `ci/run_ci.sh` takes the
cfgs from `cfg/` here, without pins, and applies the `props` column of
`suite.tsv`. Only an operator runs `make tla`.

## The model change

`../Phase5Hook.tla` is the only spec that changed (`../PROVENANCE.txt` records
it, with the SHA-256 of the file before the change). `Phase4.tla` and
`Phase4Split.tla` are untouched by this bee.

**Variables.** `subs` holds the records, each `[lau, st]`: `lau` is the launch
and `st` is `running` or `closed`. Stopped and lost are one value (`closed`),
because no property tells them apart. `subViol` is a ghost: a record was
written by a process other than the current launch's agent. One record per
launch stands for all of that launch's subagents.

**Actions.**

- `SubHook(p, k)` is a subagent hook fired by any Claude process `p` that
  carries the instance id: the agent, a nested `claude`, a teammate pane, a
  stray or a leftover. `k` is `start` (SubagentStart, or a record created from
  a launch result or a Stop snapshot) or `stop` (SubagentStop, or a covering
  snapshot that omits a running record). It may fire after `p`'s SessionEnd
  while `p` still runs. The step is a soft refresh of the row plus the record
  write. The record write needs the gate and a row that is neither `ended` nor
  `missing`. A stop with no running record writes nothing.
- `SubClose` (using `CloseAll`) closes every running record. It runs in the
  applied SessionEnd (`HookEnd`), in find-missing's mark (`FM`) and in the
  resume and reuse launch (`Launch`).
- `SubSame` keeps `subs` and `subViol` unchanged in every other action.

**Transaction forms.** In the code, SubagentStart and SubagentStop write the
record in the same store transaction as their soft refresh. The launch
request, the launch result and the Stop hook write the record in their own
gated transaction, beside the hook's unchanged row write. The model's
`SubHook` is one gated step (a soft refresh with the record write), and a
hook's own row write, where it has one, is a separate step, `HookAct`. Splitting
a transaction only adds interleavings, so the safety shown for the split holds
for the combined form too.

**Knobs.** Four knobs are zero-arity definitions, all `FALSE`:

| Knob | TRUE means |
|---|---|
| `Sub` | The design: subagent hooks and records exist. |
| `SubNoGate` | Control: the record write is not gated, so any process carrying the id writes. |
| `SubNoClose` | Control: SessionEnd, find-missing's mark and a new launch close no running record. |
| `SubOnTerminal` | Control: a gated subagent hook writes the record of an `ended` or `missing` row. |

They are definitions, not `CONSTANTS`, so every b.zuj cfg parses unchanged. A
cfg here turns a knob on with a definition override at the end of its
`CONSTANTS` section, for example `Sub <- KnobOn`. `KnobOn` is the definition
`TRUE`. Every cfg but `sx_eq_Sns` turns `Sub` on, and each control turns its
own knob on as well.

**Knobs off, the state graph is `Phase5Hook`'s.** With `Sub` off, `SubHook` is
never enabled, `subs` stays `{}` and `subViol` stays `FALSE`, so the model's
steps are b.zuj CI run 4's. A cfg without a VIEW reaches run 4's
distinct-state count exactly. `sx_eq_Sns` is `ci_H_Sns` with the b.xjn module
and must reach 11,387,737 distinct states. `run_ci.sh` checks only the
verdict, so a person compares that count by hand in the job log.

## The properties

Existing, unchanged: `RowOnlyByAgent`, `KillAllGone`, `ClockBound`,
`PendingResolves` and `ReportInResolves`.

New:

- `SubOnlyByAgent` (invariant on `subViol`). A subagent record is written only
  by the row's current agent.
- `SubNoStaleRunning` (invariant). A running record belongs to the current
  launch of a row that is neither `ended` nor `missing`. `RunningRec` is the
  helper that is true when some record is running.
- `SubLostResolves` (liveness, with find-missing fair). Once the agent is gone,
  no record stays running.
- `NotSubRunning`, `NotSubClosed` and `NotSubNextLaunch`, probes. Each must be
  violated, which shows the state it denies is reachable: a running record, a
  closed record, and the next launch running a subagent while the first
  launch's record is closed.

## Refinements after the runs

The design was refined after the runs. None needs a new run, because each one
only restricts what the model allows, changes field values, or is one more
instance of a step the model already has. The SRD (SR-14.1) rests on this
argument.

- **Entries keyed by (instance id, life_number, subagent id), with the launch a field.**
  Reads and writes select the row's current life. Only an agent switch
  advances the life; a same-agent reuse retains its records, and a switch
  back is another life. A start in a new launch of the same life moves the
  entry to that launch. The model's one record
  per launch is a superset representation: the old launch's record is closed
  by `Launch`'s `SubClose` before any new start, so every property holds.
- **Confirmation.** Unconfirmed observations are hidden, and the launch
  request opens a claimable launch. The model does not tell hidden entries
  from shown ones. The launch request's write is a gated write that never
  creates a running entry, so it is one more instance of `SubHook`'s gated
  write.
- **Snapshot closes of background entries only.** Fewer closes. `SubHook`'s
  stop is already nondeterministic, so this only restricts behaviours.
- **Writes refused for an untracked launch or a row with no launch token, and
  the entry writes a SubagentStart or SubagentStop with no `agent_id` does not
  make.** Fewer writes.
- **A `completed` launch result stops a `running` or `lost` entry.** A gated
  write that closes, one more instance of `SubHook`'s stop.
- **The main-agent Stop skips its entry transaction when it cannot change
  anything, and tool hooks write entries for the `Agent` tool only.** Fewer
  writes.
- **The failed-launch restores close nothing.** No entry can exist for the
  restored launch, since no hook applies while the row records no pane.
  `Launch`'s `SubClose` still stands for the move or the reset itself.
- **An `ended` row moved back to a live state by a later hook of its own
  process.** The entries were closed when the row ended. Once the row is live
  again, a start may reopen one in the current launch, which is
  `SubNoStaleRunning`'s allowed case, and the writer is still the gated agent
  (`SubOnlyByAgent`). The model's `HookAct` needs a process that has not
  ended, so the model does not take this path itself. A `missing` row that
  comes back is in the model, whose `HookAct`, like the store's write, does
  not exclude `ended` or `missing` rows.
- **The claim, the time rules and the closing writes' instants, the listing
  rules, and a `completed` result's `agentType` as a type source.** They change
  field values only, never which process writes or which entries are running.
  A claim only confirms and fills no field.
- **SubagentStart and SubagentStop leave `idle_since` alone.** No relay model
  changes (Hold36h, and b.146's B146R5).
- **The wrong-type rule, the 1 KiB bound, the store's clock for the ended
  write's and the mark's `lost_at`, the tracking flag every launch write
  stores as 1, the meaning of "supplied", and the cap's eviction order.** The
  wrong-type rule only removes writes. The bound, the clock and the flag change
  values only. "Supplied" only decides which launch requests open a claimable
  launch, a gated write that never creates a running entry. The eviction order
  only chooses which entry a deletion removes, a running one only when nothing
  closed remains, as before.

## LifeRule and the Phase4 interpretation

The checked [LifeRule evidence](../../../tickets/handoff/b.ssz/Ideas/b.ssz/tla/life/README.md)
records final job `j20261012213844-d4b7e2`: the design passed every invariant
and action property with 6,202,169 distinct states; controls failed their
expected properties and probes reached their states. It checks same-agent
retention, fresh switching and switching back, current-life subagent reads,
latest-session-only resume, failed-reuse restoration and independent children.
The model does not compose store concurrency or the hook gate.

`Phase5Hook`'s `subs` remains a record-by-launch abstraction, with neither
a subagent id nor a life. Its `Launch` still represents reuse. Adding
`life_number` to the stored key and filtering to the current life restricts
visible records without changing that gate or launch abstraction.

In `Phase4` and `Phase4Split`, `life` means launch generation rather than
the implemented agent life. Their historical `HistC` and `Sel` fallback
interpretation and `GetNoEarlierLife` / `ResumeNoEarlierLife` history claims
are superseded by LifeRule's `HistTruth`, `SwitchForgets`, `NeverOlder` and
`ResumeLatest`. `Sel` has no implementation counterpart: resume never selects
an older history entry. The valid tmux, provenance and kill properties
remain supported by their captured runs and refinement arguments. This is
a prose interpretation of existing evidence, with no model/config change
and no new run.

Claimable launches are not records, and no property speaks of them.

After the runs, the header comment of the model was corrected to match the
design (a record write is gated, shares SubagentStart's and SubagentStop's
soft-refresh transaction, is its own gated transaction beside the row write for
the launch request, the launch result and Stop, touches only the row's current
launch and never an `ended` or `missing` row, and `SubHook` models it as one
gated step). The change is comment only: SANY parses the file and no definition
changed. So the verdicts below hold for the corrected file that is vendored
here. The bee's run copies keep the comment the runs used.

**Not modelled.**

- The `agent_id` guard (`subagent_event` in the code). The model lets any
  process carrying the id fire `SubHook`, which is the harder case for the
  gate.
- In-process teammates' row moves.

## The runs

The b.xjn runs were made on the job scheduler on 2026-10-11, one job per run,
with the bee's own copy of the model under the module name `Phase5HookSub`
(the same model, apart from the module name and the header comment above).
TLC times are the job log's `VERDICT secs=`.

| cfg | Expected | Job id | Verdict (distinct states) | TLC time (heap) |
|---|---|---|---|---|
| `sx_eq_Sns` | pass, 11,387,737 states | j20261011151703-e203c6 | pass, 11,387,737 | 182 s (16 GiB) |
| `sx_S` | pass | j20261011151703-73101c | pass, 25,336,151 | 502 s (16 GiB) |
| `sx_S5` | pass | j20261011151704-a79665 | pass, 87,503,959 | 1254 s (20 GiB) |
| `sx_L` | pass | j20261011151704-b725c5 | pass, 11,212,188 | 808 s (20 GiB) |
| `sx_C_nogate` | `SubOnlyByAgent` violated | j20261011151704-682edf | violated (4,110): a nested claude writes a record | under 15 s (8 GiB) |
| `sx_C_noclose` | `SubNoStaleRunning` violated | j20261011151704-959e55 | violated (1,717) | under 15 s (8 GiB) |
| `sx_C_noclose_live` | `SubLostResolves` violated | j20261011151704-e63be3 | violated (165,952) | 8 s (8 GiB) |
| `sx_C_terminal` | `SubNoStaleRunning` violated | j20261011151704-59d8c4 | violated (1,520): SessionEnd, then SubagentStart on the ended row | under 15 s (8 GiB) |
| `sx_V_running` | `NotSubRunning` violated | j20261011151704-93173f | violated (440) | under 15 s (8 GiB) |
| `sx_V_closed` | `NotSubClosed` violated | j20261011151704-a6085e | violated (1,878) | under 15 s (8 GiB) |
| `sx_V_nextlaunch` | `NotSubNextLaunch` violated | j20261011151704-52b4d9 | violated (724,438) | 12 s (8 GiB) |

What each run shows:

- `sx_eq_Sns` is the knobs-off equivalence: `ci_H_Sns`'s bounds with no knob
  on. It reaches b.zuj run 4's count, so the existing Phase5Hook runs are
  unchanged.
- `sx_S` checks `RowOnlyByAgent`, `KillAllGone`, `ClockBound`,
  `SubOnlyByAgent` and `SubNoStaleRunning` over the whole world (nested
  claude, teammate, stray, leftover, rotation, hang-up, lost reply, crash) with
  an unreadable start time, at `ci_H_Sns`'s bounds.
- `sx_S5` is `sx_S` with pid reuse and readable start times (`MaxClock` 5,
  `PidPool` 3).
- `sx_L` is the liveness run with find-missing fair: `PendingResolves`,
  `ReportInResolves` and `SubLostResolves`.
- `sx_C_nogate`, `sx_C_noclose`, `sx_C_noclose_live` and `sx_C_terminal` are
  controls. Each turns one safeguard off and must violate the property that
  safeguard keeps, so the passing runs are not vacuous.
- `sx_V_running`, `sx_V_closed` and `sx_V_nextlaunch` are probes. Each must
  violate its `Not...` invariant, which shows the state is reachable.

The caps in `suite.tsv` are about twice each run's TLC time or more: `sx_S5`
2600 s, `sx_L` 1800 s, `sx_S` 1200 s, `sx_eq_Sns` 600 s (`ci_H_Sns`'s cap), and
900 s for each control and probe.

## The groups

Under `run_ci.sh`'s rule, a group's job budget is the sum of its runs' caps
plus 20 s each, plus 120 s, at most 10200 s. A group's rows are adjacent in
`suite.tsv`, and the jobs run one at a time in the order of each group's first
appearance.

| Group | Runs | Job budget |
|---|---|---|
| `sx_S5` | `sx_S5` | 2740 s |
| `sx_L` | `sx_L` | 1940 s |
| `sx` | the other nine runs | 8400 s |

`sx_S5` and `sx_L` each have a group of their own because they are the long
runs. With the parse job (600 s) the plan is four jobs.

## Rerun

Only an operator runs `make tla`, with `TLA_CHANNEL` and `TLA_JOBSCHED` set.

- This suite: `make tla TLA_SUITE=spec/tla/subagent/suite.tsv TLA_TIER=full`
- The 22 Phase5Hook rows of `ci/suite.tsv` (group `g6`), which the changed
  spec also has to pass:
  `make tla TLA_GROUPS=g6 TLA_TIER=full`

Both end `CI-VERDICT PASS` before the change merges. Run 4's verdicts do not
cover the changed `Phase5Hook.tla` until a `make tla` run passes.

`make tla-print TLA_SUITE=spec/tla/subagent/suite.tsv` prints the plan (the
runs, the group jobs and their budgets) and checks the pins. It submits
nothing and contacts no scheduler. It is the only step that runs on the host.

The distinct-state count of `sx_eq_Sns` (11,387,737) is compared by hand in its
job log. `run_ci.sh` reads the verdict only.

**If a run cannot finish.** Every run here is tier `fast`, and two are at risk:
`jobs/lib/tlcjob.sh` pins a 16 GiB heap, and the bee ran `sx_S5` and `sx_L`
with 20 GiB. One fallback rule (SRD t1.xjn.du SR-14.3 / RN-3) can apply to
either of them. If it cannot finish within the runner's memory limit, its
per-run cap or its group's budget, mark that run tier `full` in `suite.tsv`
and keep the verdicts, job ids, TLC times and heaps above as its evidence. The suite must then pass at `TLA_TIER=fast` instead. Neither
`jobs/lib/tlcjob.sh` nor the model's bounds change for it.
