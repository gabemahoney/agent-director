# b.3ie: TLA+ model of the settle time

The bee's process requirement: model the derivation and the write paths in TLA+, with a green run,
a red run per invariant from a seeded mutation, controls and probes, before any product code.
Method and job layout follow b.146 round 6 (`../b.146/job-r6-README.md`).

Files:
- `B3ieSettle.tla`: the model (master copy).
- `cfg/*.cfg`: one cfg per run; `gen-jobs.sh` writes them and one self-contained scheduler job
  directory per run (`Dockerfile` from `job-Dockerfile`, `tlcjob.sh`, `tla2tools.jar`, the model,
  the cfg and `runs`).

## The design modelled

Per spawn row, three columns:
- `state`: the raw state, as today.
- `state_changed_ms`: when the raw value last changed (ms).
- `settled_state`: the last settled value as of the last raw change (the "base").

**Writes.** Each write is one SQL UPDATE whose SET sees the pre-update row, with `now` taken inside
the statement (under the write lock). `T_w` is the writer process's `settle_ms`, read from config
when the process started.
- A hook write of a non-terminal V (ApplyHookTransition, ApplyHookWaitingIfWorking, SessionStart,
  InsertRelayRequest):
  - V = `state`: none of the three columns changes (no clock restart).
  - V ≠ `state`: `settled_state := (T_w = 0 OR now − state_changed_ms ≥ T_w) ? state :
    settled_state`; `state := V`; `state_changed_ms := now`.
- An immediate write: any terminal value (`ended`, `missing`), hook or not, and every non-hook write
  (find-missing's mark and check_permission repair, EndHeldLaunch, MoveToPending, ResetForReuse,
  resume's and reuse's restores, InsertPending): `state := V`; `settled_state := V`;
  `state_changed_ms := now` when V ≠ `state`.

**Reads** (status, get, list and `list --state`; pure, one step, no wait), with the reader's `T_r`:
`reported = (T_r = 0 OR now − state_changed_ms ≥ T_r) ? state : settled_state`. A negative age
counts as not settled.

**Guards** (send-keys, decide with `ErrRelayFallenBack.err_details.state`, find-missing, resume,
pause, kill, expire, spawn held/reuse) read `state` only.

**Migration:** existing rows get `settled_state = state` and `state_changed_ms = 0`.

## What the model covers

- The row's three columns, a discrete ms clock (`Tick`) and `ConfigChange` steps that change T.
- Two hook processes. Each reads config and its payload when it starts, then does its one UPDATE
  later as one atomic step, so the processes interleave. That includes duplicate `waiting → working`
  writes in the same ms or 1 ms apart.
- Command writes, abstracted into the immediate class with the raw-state guards their WHERE clauses
  carry:
  - live → `missing` (mark);
  - `check_permission` → `waiting`/`working` (repair);
  - `ended`/`missing` → `pending` (MoveToPending, ResetForReuse);
  - `pending` → `ended`/`missing` (restores, EndHeldLaunch).
- Hooks may move a terminal row, as the source allows (see "Findings"). `HookFromTerminal = FALSE`
  models the guard the design assumed.
- Initial rows: a migrated row in any of `InitStates` (`state_changed_ms` long ago), or a row
  InsertPending wrote at time 0.
- One reader process, three ways of taking its clock (`ReadClock`):
  - `inline`: clock and row in one step;
  - `before`: clock, then row;
  - `after`: row, then clock.
- Guard steps that record what the guard acted on.
- **Reads never wait** by construction: every read is one or two non-blocking steps that compute
  from the row and a clock value. No action waits for a state to settle.

Abstracted away: the relay and permission-request rows, b.146's timing, the hook gate's pid checks
(a hook either applies or, with `HookFromTerminal = FALSE`, is refused on a terminal row), the
working-transition hold (it writes no state), several rows, and clock skew between processes (one
host clock).

**The reference.** A ghost history `hist` records every raw value change as (value, time, kind,
writer's T). The kind is `imm` for terminal values and command writes, and `hook` otherwise.
- `FoldC(hist, T, now)` (SpecSettled) folds over the segments:
  - an `imm` segment sets the settled value at once;
  - a `hook` segment that held ≥ T (up to the next change, or now) sets it to its value.
- `FoldV` is the same fold, but judges each earlier segment with the T of the write that ended it,
  and the current segment with the reader's T.
- `BeeOK` restates FoldC in the bee's words: X held raw ≥ T (or was written at once), and no later
  raw value has held T since.

## Invariants

| # | Name | Meaning |
|---|---|---|
| 1 | `ZeroIsRaw` | With T = 0, reported = raw (for the row at any moment, and for every read with T_r = 0). |
| 2 | `SettledIsSpec` | With T unchanged, reported = FoldC(hist, T, now). |
| 2 | `HeldOrLastSettled` | The same in the bee's form: reported X held ≥ T, or is the last settled value. |
| 2 | `ReadsLinearizable` | What a reader returns equals FoldC (and satisfies BeeOK) at some instant inside its read window. |
| 3 | `ImmediateAtOnce` | When the last raw change was immediate, or the row is terminal: reported = raw for every T, and every read returns raw. |
| 4 | `GuardsReadRaw` | Every guard step acted on the raw `state`. |
| 5 | `NeverUnsupported` | For every T, and in every read: reported is a value the raw history held. |
| 5 | `WellDefined` | For every T: reported = FoldV(hist, T, now). The answer is fixed by the history and the T values in force. |
| limit | `BeeFormReaderT` | Every read satisfies the bee's form with the reader's own T. Holds while T is constant; fails after a T change. |

`TypeOK` also checks that the ghost history tracks the raw column.

## Mutations (knob `Mut`)

| Mut | Seeded bug | Breaks |
|---|---|---|
| `strictgt` | settled test `age > T`, no T = 0 short-cut | 1 (and 2) |
| `prevraw` | `settled_state := old state` on every change (a "previous state" column) | 2 |
| `samerestart` | a same-value hook write applies the change rule (clock restart) | 2 |
| `stalebase` | the same-value test and the base computed in Go from a read before the UPDATE | 2 (and 3) |
| `nowoutside` | the writer's `now` is a Go clock value bound before the write lock is taken | 2 |
| `negagesettles` | a negative age counts as settled (unsigned or absolute age) | 2 (reads) |
| ReadClock `after` | the reader reads the row first and takes its clock after | 2 (reads) |
| `termsettles` | a hook's `ended` goes through the settle rule | 3 (and 2) |
| `cmdsettles` | command writes go through the settle rule | 3 (and 2) |
| `guardreported` | a guard acts on the reported state | 4 |
| `insertnobase` | InsertPending leaves `settled_state` at its column default `''` | 5 (and 2, 3) |
| `today` | reported = raw (control) | 2 |
| `immnostamp` | immediate writes do not stamp (robustness) | nothing |

## Runs

Bounds unless the row says otherwise: 2 hook processes, T in 0..2, initial T = 2, MaxTime 4,
MaxWrites 3, migrated rows starting `waiting` or `working`, hooks write
`waiting`/`working`/`check_permission`/`ended`, commands on, guards on, reader clock inline,
hooks may move a terminal row (as the source allows). The job Dockerfile gives TLC `-Xmx8g`, 1 GB
direct memory, a 3000 s budget (green runs cap themselves at 2900 s, the rest at 900 s) and an 8 GB
disk cap; `pass_tchange_before` needs more (see `PROVENANCE.txt`). The last output line is the
verdict: `JOB-VERDICT <cfg>=<result>/<distinct states>`.

| Run | What | Expected |
|---|---|---|
| `pass_const` | the design, T = 2 constant, MaxWrites 4, fresh inserts too; every invariant | PASS |
| `pass_const_t1` | the same at T = 1 | PASS |
| `pass_t0` | the same at T = 0 (off) | PASS |
| `pass_before` | the design, reader takes its clock before the row read (negative ages reachable) | PASS |
| `pass_noterm` | the design with the guard it assumed (no hook moves a terminal row) | PASS |
| `pass_immnostamp` | robustness: immediate writes leave `state_changed_ms` alone (a verbatim reuse restore) | PASS |
| `pass_tchange` | the design, T starts at 1 and changes twice; invariants 1, 3, 4, 5 (2 holds until the first change) | PASS |
| `pass_tchange_before` | the same with the reader clock before the row read | PASS |
| `red_strictgt` | inv 1: `age > T` with no T = 0 short-cut, at T = 0 | FAIL `ZeroIsRaw` |
| `red_prevraw` | inv 2: the base is the previous raw value (A→B→C reads B) | FAIL `SettledIsSpec` |
| `red_samerestart` | inv 2: a same-value hook write restarts the clock | FAIL `SettledIsSpec` |
| `red_stalebase` | inv 2: base and same-value test from a Go-side read before the UPDATE (duplicate hooks) | FAIL `SettledIsSpec` |
| `red_nowoutside` | inv 2: the writer's `now` is taken before the write lock | FAIL `SettledIsSpec` |
| `red_negage` | inv 2 (reads): a negative age counts as settled | FAIL `ReadsLinearizable` |
| `red_readafter` | inv 2 (reads): the reader reads the row, then takes its clock | FAIL `ReadsLinearizable` |
| `red_termsettles` | inv 3: a hook's `ended` goes through the settle rule | FAIL `ImmediateAtOnce` |
| `red_cmdsettles` | inv 3: command writes go through the settle rule | FAIL `ImmediateAtOnce` |
| `red_guardreported` | inv 4: a guard acts on the reported state | FAIL `GuardsReadRaw` |
| `red_insertnobase` | inv 5 (T changing): InsertPending leaves `settled_state` at its default `''` | FAIL `NeverUnsupported` |
| `ctl_today` | control: today (reported = raw) at T = 2, in the bee's form | FAIL `HeldOrLastSettled` |
| `lim_tchange_bee` | limit: after a T change, a base judged under the old T is reported | FAIL `BeeFormReaderT` |
| `probe_flap` | probe: A→B→A within T is reached and a read returns A | FAIL `ProbeFlap` |
| `probe_shortturn` | probe: waiting→working→waiting within T reads waiting | FAIL `ProbeShortTurn` |
| `probe_holdsold` | probe: a read during an unsettled hook change returns the old settled value | FAIL `ProbeHoldsOld` |
| `probe_chaina` | probe: A→B→C within T reads A | FAIL `ProbeChainHoldsA` |
| `probe_chainc` | probe: A→B→C reads C once C held T from the B→C change | FAIL `ProbeChainReachesC` |
| `probe_dup` | probe: a duplicate hook write (same value, other hook, ≤ 1 ms) lands | FAIL `ProbeDup` |
| `probe_twohooks` | probe: two hook processes in flight with the same value | FAIL `ProbeTwoHooks` |
| `probe_tchange` | probe: T changes between a hook write and a read | FAIL `ProbeTChangeRead` |
| `probe_guarddiff` | probe: a guard acts on raw while the reported state differs | FAIL `ProbeGuardDiffers` |
| `probe_hookleavesterm` | probe: a hook moves a terminal row to a live state | FAIL `ProbeHookLeavesTerminal` |
| `probe_cmdcuts` | probe: a command write lands while a hook change is unsettled | FAIL `ProbeCmdCutsSettle` |

A FAIL verdict reads `FAIL(Invariant_<Name>_is_violated)`. Each red cfg lists, besides its target,
the invariants the mutation must not break; a verdict naming any other invariant is a finding.
`red_insertnobase` fails in the initial state (a freshly inserted row read within T).

- **Green (8):**
  - the design at T = 2, T = 1 and T = 0;
  - with the reader clock before the row read;
  - with no hook leaving a terminal state;
  - with immediate writes that do not stamp;
  - with T changing (twice), inline and clock-before reads.
- **Red (11):** one mutation each, as tabled above.
- **Control (1):** `today`.
- **Limit (1):** `lim_tchange_bee`.
- **Probes (11):**
  - flap A→B→A;
  - the short turn;
  - a read holding the old value;
  - the A→B→C chain, both halves;
  - duplicate hook writes;
  - two hooks in flight;
  - a T change between write and read;
  - a guard acting on raw while the reported state differs;
  - a hook leaving a terminal state;
  - a command cutting an unsettled change.

**Local sanity run**: the design, T = 2, MaxTime 3, MaxWrites 3, every
invariant. Result: **PASS, 158,868 distinct states, depth 12, 6 s**. Every other run is a
scheduler job.

## Findings for the implementation

1. **Hooks can move a terminal row.** The store's only check on a hook write is the SR-22.9 gate
   (pane pid and start time), and a row keeps its pane in two cases:
   - find-missing's mark keeps the pane, so `hook_gate_test.go` has a "missing before report-in" row
     that any hook moves to a live state;
   - the ended transition keeps the pane too, so a racing hook of the same agent can move `ended`
     to a live state if its parent check ran before the agent exited and its write lands after the
     SessionEnd write;
   - resume's restore writes the old pane identity back.

   `hook_writes.go` says the same in its comment on the non-terminal SET ("the row's own agent
   reaching a row find-missing marked missing before its agent reported in").

   The design still holds: such a move is hook-written, so it settles. The row reads `missing` or
   `ended` for T, then the live value (default runs; `probe_hookleavesterm` reaches it). The bee's
   sentence "hooks never move a row out of a terminal state" is not true of the source, and the
   docs should not repeat it.
2. **Same-value test and base must be SQL in the UPDATE.** ApplyHookTransitionResult reads the
   prior state in a separate, non-transactional statement before its UPDATE. If the same-value test
   or the base is computed in Go from that read, duplicate hooks restart the clock (`red_stalebase`).
3. **`now` must be read under the write lock.** Take it inside the statement, for example SQLite's
   `unixepoch('subsec')` (modernc v1.34 bundles SQLite 3.46). A Go clock bound before the busy wait
   breaks invariant 2 (`red_nowoutside`).
4. **Readers take the clock before the row read, or in the SELECT.** Reading the row first and the
   clock after is wrong (`red_readafter`). A negative age must count as not settled
   (`red_negage`).
5. **`list --state` filters in SQL with a LIMIT** (`internal/store/list.go`). The filter must use
   the settled expression in the same statement. Filtering in Go after the query would apply the
   LIMIT before the filter.
6. **Every immediate path must set `settled_state := state` in its own UPDATE.** That covers
   InsertPending (`red_insertnobase`), both restores and ResetForReuse. Otherwise, for example, a
   restore that sets only `state` leaves the reset's `pending` base and fresh stamp, so the row
   reads `pending` for T after it went back to `ended`. `red_cmdsettles` shows this class of bug. The stamp of an immediate write never matters (`pass_immnostamp`), so a
   verbatim restore of an old `state_changed_ms` is harmless.
7. **T changes.**
   - Earlier segments keep the judgement made with the T in force when they ended (`WellDefined`).
   - So right after a T change, a value can be reported that held less than the new T
     (`lim_tchange_bee`). It is always a value the raw history held (`NeverUnsupported`).
   - No natural mutation reports a never-held value. Only a missing default does (`insertnobase`,
     or a migration that stamps the migration time and does not copy the state).
   - The design's `state_changed_ms = 0` migration makes the migrated `settled_state` irrelevant.
8. **Write paths the brief did not list.**
   - Hook-kind: RecordSessionStartIdentity (SessionStart, `waiting` "whatever the prior state").
   - Immediate: InsertPending, RepairCheckPermissionIfSameLife, ResetForReuse and
     RestoreAfterFailedReuse (whose SET list is generated from `rawLifeColumns`).
   - ApplyHookWaitingIfWorking is two conditional statements; each must carry the settle SET.
   - `DialogMaybeOpenDetails.state` (err_details) is guard-side, like `ErrRelayFallenBack`'s.
