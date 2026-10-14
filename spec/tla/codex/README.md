# b.ssz: TLA+ check of the Codex design changes

Follow-up: [b.w21 current-rule model checkpoint](b.w21/README.md) extends
`B146R5Cx` with default-off legacy-request scenarios and nine scheduler
contexts. At commit `8c6998d4`, all nine b.w21 scheduler observations match
the predictions: four bug-check failures, two reachability-probe failures
and three passing controls. Its [manifest](b.w21/manifest.json) and
[receipt archive](b.w21/model-red-receipts.json) preserve job IDs, counts,
full logs and hashes. The model-failure gate is met; local sandbox regressions were then proven
red and the repair passed all 41 targeted cases. The final full sandbox unit
suite passed after expectation and artifact/provenance corrections: 67 Go
packages (37 cached), 420 Bun tests, zero failures and make exit 0.
The [submitted fixed-model results](b.w21/fixed/README.md) record ten PASS,
three expected failures and one unexpected `NoRace` failure: thirteen of
fourteen predictions match at `9d025f8f`. Full logs, counts and immutable
predictions are archived. The [round-2 handoff](b.w21/fixed-r2/README.md)
corrects the seeded modern ACK hold in the model; six separate contexts
are ready, all **NOT_RUN** (four expected PASS, one reachability FAIL,
one `NoRace` mutant FAIL). No production runtime changed.
The [integration evidence](../../../test/b.w21/integration/README.md) records
all fifteen jobs PASS at unchanged source `9d025f8f`: 152 EPIC cases plus
22 install-mode assertions/five scenarios. Actual network off is the User's
statement; historical network-on commands remain preserved. New mixed-store
API tests pass in targeted sandbox runs (118 API/store and eight artifact
cases). The fresh full sandbox suite also passed: 67 Go packages (39 cached),
420 Bun tests, zero failures and make exit 0.
See [unit receipts and review limits](../../../test/b.w21/README.md).

The b.ssz verdicts below are historical runs and have not been rerun with
these extensions. The nine current-rule contexts remain the immutable
`8c6998d4` baseline, including its independent relaunch probe and
`B21RecordsSettle`. `src/B146R5Cx.tla` is now the evolving model; the older
final-model equivalence claims below describe the submitted b.ssz snapshots,
not the modified current source. Original b.ssz job directories omit their
jar; the manifest-declared b.w21 red/fixed/round-2 contexts include canonical jar
copies for self-contained scheduler submission.

The design under check is `../srd-design-brief.md`, sections "Codex session-end rule" and
"Codex state mapping and relay" (PRD t1.ssz.qi: States; Permission relay and typing; the
differences table). Nothing here edits any repo's `spec/`. No TLC was run on the dev host: every
verdict is a job-scheduler result (`verdicts.txt`, `job-ids.txt`). The first 21 jobs are the Codex
design check; the Q2 gate (the `/review` thread's unmarked hooks, 15 more jobs) is the last
section of this file's tables and finding F6.

## Which models, and why

| Model (copy here) | Source | Why it is touched |
|---|---|---|
| `src/Phase5HookCx.tla` | `spec/tla/Phase5Hook.tla` (dev/v0.12.0 = feature/b.146-relay = feature/b.3ie, SHA-256 `e34dc715…`) | The hook gate and the row's life: RowOnlyByAgent, PendingResolves, ReportInResolves. The Codex session-end rule changes what SessionEnd and SessionStart write and adds `ended` by find-missing and by pause. |
| `src/Hold36hCx.tla` | b.36h's `spec/tla/hold36h/tla/Hold36h.tla` (feature/b.146-relay 5be84a8 and feature/b.3ie, SHA-256 `7856c1d8…`) | The latest merged relay model: the working hold, the turn_end / tool_ran proofs (b.omt "fixcut"), the subagent, several turns, find-missing's check_permission repair. Codex removes tool_ran and the idle Notification and adds Interrupt as a turn end. The step-2c hold of plain send-keys and pause (ErrDialogMaybeOpen) is added as properties over its existing variables. |
| `src/B146R5Cx.tla` | b.146 round 5's `spec/tla/B146R5.tla` (design/b146-timeouts, SHA-256 `53daaa8c…`) | The integrated relay model the adopted hold (Unproven + Bypass) passed on: the pane protocol, decide, the store lock, deaths. Codex removes PostToolUse's close and proof and the idle Notification. |

Not changed, and why:
- `Phase4.tla`, `Phase4Split.tla` (labels, lookups, kill, resume, b.66h launch scope): Codex rows
  use the same tmux labels and verbs; nothing in the brief changes them.
- b.3ie's `B3ieSettle.tla` (settle time; feature/b.3ie, not yet in dev/v0.12.0): it abstracts the
  relay and the gate away and treats every non-terminal hook write alike and every terminal write
  (`ended`, `missing`) as immediate. Codex's Interrupt -> waiting is a non-terminal hook write, its
  `ended` comes from find-missing or pause (terminal, immediate), and the end report is a write
  that leaves the state (the "V = state: no column changes" case). Nothing it checks changes.
- b.3ie's copy of Hold36h is byte-identical to b.146-relay's, so the base above covers both.
- `B146Final`, `PaneAnswer`, `DeathRelay`: B146R5 is B146Final plus the round-5 guards, and covers
  the pane and death slices those earlier rounds checked.
- `B146R6` (rule 10, pre-hook startup dialogs, `unreported`): it already lets SessionStart never
  come; Codex's report-in at its first prompt is one more reason for that, not a new behaviour.
  The brief's (e) is covered the same way in Phase5HookCx: HookFire (SessionStart) is unfair, so
  "only at the first prompt" is one of the behaviours already checked, and PendingResolves keeps
  its existing exception (a visible, live agent that has not reported stays `pending`).

## Knobs, and the knobs-off argument

Every knob is a zero-arity definition, `FALSE` in the spec (so the source cfgs parse unchanged).
A cfg turns one on at the end of `CONSTANTS`, e.g. `Codex <- KnobOn`, as b.66h does in
`spec/tla/launch/`.

**Phase5HookCx**

| Knob | On means |
|---|---|
| `Codex` | The brief's rule. SessionEnd through the gate (any session id) sets the row's end report `er` and leaves the state; it may be the exit or in-TUI `/new`/`/resume`/`/fork` (the process keeps running). An applied SessionStart clears `er` and, as today, takes the new session id. find-missing marks a row whose recorded agent process is gone `ended` when `er` is set, `missing` otherwise. |
| `CxEndNow` | Control: a Codex SessionEnd ends the row at once (Claude's rule on reason "other"). |
| `PauseOn` | Pause is modelled: on a live row with a recorded pane it types `/exit` (`pw = "wait"`) and its wait returns when the row reads `ended` (or gives up on `missing`). |
| `PauseSeesGone` | The fix for finding F1: pause's wait judges the agent process as find-missing does and writes `ended` on a Codex row with an end report whose process is gone. |

Knobs off: `EndKinds = {TRUE}`, so HookEnd sets `ended` on the process and applies `ended` exactly
as before; `er` is never set, so HookStartRun's added `!.er = FALSE` and FM's `er` test change
nothing; the ghost `ce` is only set under `PauseOn`; PauseSend and PauseWait need `PauseOn` /
`pw = "wait"` and are never enabled; Launch's `pw' = "none" /\ ce' = FALSE` keeps their initial
values; the new probe tags are added only under `Codex` or `PauseOn`. The new field and the two new
variables are constant, so the state graph is Phase5Hook's state for state. Run `hx_eq_Sns`
checks it: ci_H_Sns must give b.zuj run 4's 11,387,737 distinct states.

**Hold36hCx**

| Knob | On means |
|---|---|
| `CxRelay` | Codex's events: PostToolUse proves and closes nothing (no tool_use_id on Codex's request); no idle Notification (`iq` never becomes due, IdleEmit off); Claude's deny-interrupt with no Stop is replaced by `CxInterrupt` (Esc mid-turn, or cancel at the main agent's dialog, which closes it denied) whose hook `IntHook` is the main agent's turn end: turn_end proof of every recorded main-agent request, then waiting. Stop and Interrupt are Codex's only end signals, so their loss needs `BothEndsDie` (Claude's both-ends-lost residual). find-missing's repair then always writes working (no idle_since). |
| `CxIntNoTurnEnd` | Control: the Interrupt hook writes waiting but proves nothing. |
| `CxTurnEndProvesSub` | Control: the main agent's turn end also proves the subagent's request. |
| `CxSubIntUnmarked` | Control: a subagent's Interrupt reaches AD unmarked and is read as the main agent's turn end. |
| `CxReview` | The Q2 gate (used with `CxRelay`): the `/review` thread's unmarked hooks (see "The Q2 gate" below). |
| Env `"LastGasp"` | New environment switch (b.146 decision 5's first window): a relay hook acks and dies before the agent gets the verdict, so a dialog shows though AD recorded a delivery. It is what makes the step-2c hold (rule 3) the only guard, so KeysSafe means something. Hold-safety and control runs only (it breaks WorkingTrue for both agents: an accepted residual of b.146). |

Knobs off and no `LastGasp`: CxInterrupt, IntHook, IntDie, SubInt and RelayGasp are never
enabled; `IdleDue = "due"` and `TurnEndSet = MainRec` give StopHook, StopDie and SubEnd their
original writes; Interrupt and IdleEmit get a `~CxRelay` guard that is TRUE; PostToolUse's
`pv`/`p` are the original expressions; PaneAnswer's added `~ack[r]` is implied (a shown dialog
never has an ack without LastGasp); the ghost `lw` is written only under CxRelay. The added
fairness conjunct `WF_vars(IntHook \/ IntDie)` is on an action that is never enabled. Run
`rx_eq_live` checks it: b.36h's cond_live must give 109,518 distinct states.

**B146R5Cx**: two Codex knobs. `CxRv` (the Q2 gate) is described in "The Q2 gate" below. `CxR5` (Codex's events: PostToolUse neither closes, through ToolUseClose,
nor proves; Stop goes straight to rest with no idle Notification; a lost Stop needs `BothEndsDie`).
Knob off: every changed expression reduces to the original (`~CxR5` TRUE, the IFs take the
original branch). Codex's Interrupt after an Escape at the dialog writes what Stop writes under the
brief's rule, so this one-turn model's Stop after a denied dialog stands for it (Interrupt as a
separate event, mid-turn, with a subagent and several turns, is Hold36hCx's).

## Properties

Existing properties are checked unchanged under the Codex knobs: RowOnlyByAgent, KillAllGone,
ClockBound, PendingResolves, ReportInResolves (Phase5HookCx); WorkingTrue, NoStaleHold, TypeOK
(Hold36hCx); every fin1_time_ub / fin1_t_ub property of B146R5 (NoSilentAllow, AnswersLand,
NoRace, FallenTrue, ClaimV3, RecordTrue, AckTrue, DecideTruthful, NoContradiction, BusyClean,
FieldsTrue, DecideInTime, ReadNeverWaits, NeverHeldNoRequest, DialogsClose, RecordsClose,
RowSettles, StraySettles, CallerSettles).

One is restated, with the modelled exception:
- **RowSettlesCx** (Hold36h's RowSettles): at rest the row reads `waiting`, *or `working` written
  after the main agent's turn ended* (a subagent's hook, find-missing's repair; ghost `lw`). Why:
  the PRD's differences table, "No automatic repair of a row left `working`": Claude's repair is the
  idle Notification, which Codex does not send. `rx_lim_rowsettles` shows the unrestated property
  fails under Codex events (expected), so the exception is real and is the only one.

New properties:
- **CxNoEndedWhileAlive**: a Codex row never reads `ended` while its agent process (this launch)
  is alive. **CxNoEndedWhileRunning** is the PRD's weaker wording (not while the agent goes on
  running, i.e. has not sent its exit SessionEnd); the CxEndNow control is checked against it, so
  its counterexample is the `/new` case, not the millisecond exit window Claude also has.
- **CxEndResolves**: a live Codex row with an end report whose agent is gone reaches `ended`.
- **PauseSeesEnd**: once the agent has answered pause's `/exit` (its exit SessionEnd went through
  the gate) and is gone, the row pause waits on reads `ended`. Checked with find-missing fair
  (`FairSpecP`) and with find-missing not scheduled during the wait (`PauseSpec`).
- **KeysSafe** (step 2c's purpose): when plain send-keys (or pause) would get through rules 1-3 of
  `sendKeysRefusal` (no relay hook may answer, nothing fallen back, every request proven gone), no
  dialog is shown.
- **KeysSettle**: at rest plain send-keys eventually gets through, except while the subagent's
  request is unproven (the documented exception: Claude, a denied one; Codex, any, until the agent
  ends).

## Finding F1 (design flaw): pause's wait does not observe a gone process

The brief's rule 2 says the paths that "already" observe a gone agent process for Claude rows
include "pause's wait". The code does not: `pkg/api/pause.go` `waitEnded` (b.146-relay
pause.go:298-335) polls only `GetSpawnState` until `ended` or the timeout, and pause "never writes
the row's state" (pause.go:197-198). For Claude the `ended` comes from the SessionEnd hook itself
(cause `exit`). For Codex, rule 1 makes that SessionEnd write only the end report, so:

1. Codex row `waiting` (live); `pause` types C-u, `/exit`, Enter; the wait starts.
2. Codex fires SessionEnd (reason "other") through the gate: end report recorded, state stays
   `waiting` (rule 1).
3. The Codex process exits.
4. The wait polls `waiting` until `pause.timeout_seconds` (30 s): `ErrPauseTimeout`. The row reads
   `waiting` until a scheduled find-missing runs (rule 2) and marks it `ended`.

So pause on a Codex row times out (unless find-missing happens to run inside the wait), where the
same pause on a Claude row returns success: a parity break and against the PRD AC "pause still ends
it `ended`". In the model: `hx_C_pause` (brief as written, `PauseSpec`) must FAIL PauseSeesEnd.

**Fix (smallest, parity-preserving):** pause's wait, on a Codex row, after each poll that does not
read `ended`, applies find-missing's single-row judgement of the recorded agent process (pid and
start time); when the process is gone and the row has an end report, it makes find-missing's
`ended` write for that row (conditional on the same launch, a live state and the end report still
set) and returns success. A gone process with no end report is left alone (find-missing will mark
it `missing`; pause times out, as a Claude pause does when its agent dies without SessionEnd). No
new state, write kind or verdict: the write is find-missing's, made sooner. Modelled as
`PauseSeesGone`; `hx_P` (fix, find-missing not scheduled) must PASS, and every other Codex run uses
the fix. The SRD should state that this is new behaviour of pause's wait, not existing behaviour.

## Other findings (not flaws in the stated properties)

- **F2: a stale end report turns a crash into `ended`.** After in-TUI `/new` (end report set) the
  report stays until the next prompt's SessionStart. A crash or kill in that window reads `ended`,
  not `missing`. The same if that SessionStart hook dies (fail-open). Harmless for the properties
  above (the process is gone); the SRD may want to say so. Probes: `hx_V_endlive` (the window),
  `hx_V_missing` (a crash with no report still reads `missing`).
- **F3: Codex subagent marking is load-bearing for the relay.** If a subagent's Interrupt reached AD
  unmarked, AD would read it as the main agent's turn end and release the hold on a main-agent
  dialog that is still up (`rx_C_subint` must FAIL KeysSafe). The PRD's assumption "Codex marks
  every hook a subagent fires" must be checked before release, as it says. Also: a marked
  subagent Interrupt should change no state (a soft refresh, as Claude's subagent lifecycle hooks);
  "Interrupt -> waiting" is for the main agent. Not modelled further.
- **F4: Codex has one end signal per turn.** With no idle Notification, a lost Stop or Interrupt
  hook leaves the turn's main-agent requests unproven (plain send-keys held, the row possibly left
  `working`) until the next turn's end; Claude needs both Stop and the idle Notification lost for
  that. Modelled as the existing BothEndsDie residual (allowed in `rx_safe`/`rx_keys`, excluded from
  the liveness runs, as for Claude). The way past is the existing hash bypass. Worth one line in the
  docs' differences.
- **F5 (not modelled): Esc while a main-agent relay hook polls.** If Codex lets the user interrupt
  while the PermissionRequest hook runs, rule 1 holds plain keys while the hook lives; afterwards
  the request is fallen back until record-pane-answer (as a Claude request whose hook died), and
  one committed after the Interrupt hook's start second stays unproven to the next turn end (the
  existing same-second residual). Safe provided Codex draws no dialog for a cancelled turn: check
  in the pre-release real-Codex run.

## Runs

`suite.tsv` lists every run (spec, expected verdict, budget, heap, disk cap, the scheduler
`--timeout`/`--memory` to submit with, tier). `src/gencfg.sh` writes `src/cfg/*.cfg`; `mkjobs.sh`
writes one self-contained `job-<run>/` per row that has none (Dockerfile, the unchanged `tlcjob.sh`
and `tla2tools.jar`, the spec, the cfg, `runs`). The last line of a
job is `JOB-VERDICT <cfg>=<result>/<distinct>`. A control or probe is a match when it fails on the
named property; a FAIL on another property is a modelling problem, not a match.
`submit-order.txt` lists the 21 fast jobs in priority order, then 5 optional ones, then the gate
set (15 jobs; `submit-gate.sh` submits it, appending to `job-ids.txt`; `watch-gate.sh` watches it).
`mkjobs.sh` builds only the rows that have no job directory yet, so every submitted job directory
stays "as submitted"; it takes `tla2tools.jar` and `tlcjob.sh` from `agent-director-bssz/spec/tla`
(byte-identical to the copies in the 21 earlier job directories: `cmp` checked).

| Job | Expected | What |
|---|---|---|
| hx_eq_Sns | PASS, 11,387,737 states | knobs off = b.zuj run 4's ci_H_Sns |
| rx_eq_live | PASS, 109,518 states | knobs off = b.36h's cond_live |
| hx_S | PASS | Codex rule + pause (fix): gate safety, CxNoEndedWhileAlive, whole world |
| hx_C_endnow | FAIL CxNoEndedWhileRunning | control: SessionEnd ends a Codex row at once (`/new`) |
| hx_C_pause | FAIL PauseSeesEnd | control = the brief as written (F1) |
| hx_P | PASS | the fix: pause's wait reaches `ended` without find-missing |
| hx_L | PASS | PendingResolves, ReportInResolves, CxEndResolves, PauseSeesEnd |
| rx_safe | PASS | WorkingTrue, NoStaleHold under Codex events, every switch |
| rx_keys | PASS | KeysSafe under Codex events with the last-gasp window |
| rx_live | PASS | RowSettlesCx, KeysSettle, WorkingTrue, NoStaleHold |
| rx_C_intnoend | FAIL KeysSettle | control: Interrupt not counted as turn_end |
| rx_C_subproof | FAIL KeysSafe | control: main turn end releases the subagent's request |
| rx_C_subint | FAIL KeysSafe | control: unmarked subagent Interrupt (F3) |
| rx_lim_rowsettles | FAIL RowSettles | the documented difference behind RowSettlesCx |
| hx_V_followed | FAIL NotCxFollowed | probe: `/new` end report, then the row follows the new session |
| hx_V_fmended | FAIL NotCxFMEnded | probe: exit after the end report, find-missing ends the row |
| hx_V_pauseended | FAIL NotCxPauseEnded | probe: pause's wait (fix) writes `ended` |
| rx_V_subheld | FAIL NeverSubAllowHeld | probe: an allowed subagent request holds at rest |
| r5x_time | PASS | B146R5 every property under Codex events (fin1_time_ub) |
| r5x_t | PASS | B146R5 safety under Codex events (fin1_t_ub) |
| r5x_eq | PASS, exactly 833,740 states | B146R5Cx knobs-off equivalence: b.146's fin1_time_ub unchanged (added after review round 3; job j20261011130211-63b62c) |
| hx_S5 (optional) | PASS, 45,361,001 states | hx_S with pid reuse (MaxClock 5, PidPool 3); ran 9 min |
| hx_V_endlive (optional) | FAIL NotCxEndLive (433 states) | probe: end report while the agent keeps running (F2) |
| hx_V_missing (optional) | FAIL NotCxMissing (2,064 states) | probe: crash with no end report reads `missing` |
| rx_V_intproof (optional) | FAIL NeverHit (62 states) | probe: Interrupt proves a cancelled main request |
| rx_claude_keys (optional) | PASS, 239,820 states | knobs off (Claude today): KeysSafe with the last-gasp window |

Sizes are estimates: ci_H_Sns was 11.4 M states in 3 min and ci_H_L 31 M in 24 min (b.zuj run 4);
the Codex runs add one row field, two variables and a two-way SessionEnd, so expect two to four
times the Claude counts. `hx_L` (liveness, PidPool 3) has a 7,000 s budget: submit it with
`--timeout 2h`. If a PASS run ends INCOMPLETE with no violation, rerun it with a longer timeout
and a larger `BUDGET` and `@` cap. Hold36hCx runs are small (b.36h's largest was 2.8 M).

## Not modelled

- Kill's row write, `resume` and `reuse_finished` of a Codex row (unchanged verbs; Launch stands
  for them).
- Session-id archiving (`prior_sessions`); the model only follows the session id.
- Codex subagent lifecycle hooks other than the F3 control (assumed marked and ignored, as for
  Claude).
- The SessionEnd hook being cut short by Codex's 3 s cap before its write: the gate already needs
  the parent alive, so a report that never lands gives `missing`, as for a Claude crash.
- Esc during a running tool (a late PostToolUse after the interrupt): one more way to leave a row
  `working`, inside the RowSettlesCx exception; Esc while a relay hook polls (F5).
- Parallel tool calls, decide's timing and the store lock beyond what each copied model already
  has.

## The Q2 gate: the `/review` thread's unmarked hooks

Required by the User before any relay work (probe answers, "Orchestrator decisions on Q1-Q6", Q2):
the narrower answer (keep SR-8.2's proofs and holds, document the `/review` residual) is taken only
if a run of Hold36hCx and B146R5Cx with an unmarked review-hook action shows KeysSafe and
NoStaleHold still hold. They do. Phase5HookCx is untouched and not re-run (the review's hooks are
the verified process's ordinary tool and prompt hooks, already in its `HookFire`). This gate does
not model Q5(a) (held for the User in E4-T6).

### Knobs added

| Model | Knob | On means |
|---|---|---|
| Hold36hCx | `CxReview` (with `CxRelay`) | A review starts only at the main agent's prompt (`aph = "idle"`: before any turn or after the turn-end hook ran; no main hook in flight; no main-agent request in `hook` or `pend`, i.e. no main dialog that may be shown; the subagent may be in any state). While it runs (`rv = "run"`) it sends unmarked hooks in a hook slot of its own, `wr["rev"]` (so they interleave with the subagent's and the main agent's): each is a working write through the existing two-step hold (`WorkRead("rev")`, then `WorkWrite("rev")` or `WorkDrop("rev")` under `HookDeath`). They prove nothing, close nothing and open no request. The review ends silently (`RevEnd`: no Stop, no write; weakly fair). Esc during it (`RevInterrupt`) is one main-agent Interrupt: the review ends and `aph = "intdue"`, so the existing `IntHook` runs (turn_end proof of the recorded main requests, then waiting); a review write in flight may land after it. A review write goes through `LwAfter` as any working write: at `aph = "idle"` or `"intdue"` it records `lw = "after"`, so RowSettlesCx's existing exception covers it. `TurnStart` needs no review running; `Rest` also needs `rv = "none"`. |
| B146R5Cx | `CxRv` (with `CxR5`) | `RevWrite`: an unmarked review working write, `rs' = WorkingRow(AnyAwaiting)` (the hold), at the prompt before the turn (`aph = "idle"`) or at rest after Stop (`aph = "rest"`), with the agent alive and the lock free. Its drawn output is a pane change for the round-5 guards: the new variable `rvd` is part of `Scrn` and flips with each review write. It is not a quiet hook for IdleGate (`QuietTo(FALSE)`), clears `idl`, and proves and closes nothing (`unp`, `pane`, `dec` untouched). The ghost `rvw` records working left by a review write after the turn ended. Unfair (the user may or may not type `/review`). |

### Knobs-off argument

**CxReview.** New variable `rv` (initially `"none"`) is constant while `RevStart`, `RevHook`,
`RevEnd` and `RevInterrupt` (all need `CxReview`) are never enabled; `Agents` is `{"main", "sub"}`
unless `CxReview` (so `wr`, `rtx`, TypeOK and the fairness quantifier over agents have exactly their
old domain); `TurnStart`'s added `rv = "none"` and `Rest`'s added `rv = "none"` are TRUE; the added
fairness conjunct `WF_vars(RevEnd)` is on an action never enabled; the probe tag `"review"` is only
tested under `Probe = "review"`. `rx_eq_live_g` (all knobs off) must give 109,518, and
`rx_safe_k0`, `rx_keys_k0`, `rx_live_k0` (the cfgs of `rx_safe`, `rx_keys`, `rx_live`, with the
final model text) must give 64,027 / 67,511 / 35,589.

**CxRv.** New variables `rvd` and `rvw` (both initially FALSE) are constant while `RevWrite` is
never enabled; `Scrn` gains the constant `rvd`, which changes no `Changed` test; every UNCHANGED
list that named `nz` now names `rvd, rvw` too. `r5x_eq_g` (all knobs off) must give 833,740, and
`r5x_time_k0`, `r5x_t_k0` (the cfgs of `r5x_time`, `r5x_t` with the final model text) must give
296,382 / 2,122,224.

### Properties

Unchanged: every existing property, checked as before. Not added: KeysSafe and NoStaleHold exist
only in Hold36hCx (B146R5Cx checks its own fin1 set).

- **RowSettlesCx** (Hold36hCx) is not restated again: the review's writes are `lw = "after"`, inside
  its existing exception, and `Rest` additionally requires no review running. `rx_live_rv` shows no
  new exception is needed.
- **RowSettlesRv** (B146R5Cx): RowSettles with one exception, `working` written by a review hook
  after the turn ended (ghost `rvw`): at rest the row reads `waiting` or `working` with `rvw`.
  `r5x_lim_rowsettles_rv` shows the unrestated RowSettles fails with `CxRv` (the exception is real
  and is the only one).
- **Probe `"review"`** (`NeverHit` with `Probe = "review"`): Hold36hCx: a review write lands
  `working` in a rest state (`RestWithout("rev")`); B146R5Cx: a review write turns `waiting` into
  `working` at rest. Both must be reached.

### Runs

| Run | Job | Expected | Result | Distinct states |
|---|---|---|---|---|
| `rx_eq_live_g` | j20261012070055-51f897 | PASS = 109,518 (equivalence, level 1) | PASS | 109,518 |
| `r5x_eq_g` | j20261012070055-6fb2fa | PASS = 833,740 (equivalence, level 1) | PASS | 833,740 |
| `rx_safe_k0` | j20261012070056-0d25be | PASS = 64,027 (equivalence, level 2) | PASS | 64,027 |
| `rx_keys_k0` | j20261012070056-3e49ea | PASS = 67,511 (equivalence, level 2) | PASS | 67,511 |
| `rx_live_k0` | j20261012070056-c7bab7 | PASS = 35,589 (equivalence, level 2) | PASS | 35,589 |
| `r5x_time_k0` | j20261012070056-9a5562 | PASS = 296,382 (equivalence, level 2) | PASS | 296,382 |
| `r5x_t_k0` | j20261012070056-dee74a | PASS = 2,122,224 (equivalence, level 2) | PASS | 2,122,224 |
| `rx_safe_rv` | j20261012070055-239da0 | PASS: TypeOK, WorkingTrue, NoStaleHold | PASS | 404,248 |
| `rx_keys_rv` | j20261012070055-4f0182 | PASS: TypeOK, KeysSafe, NoStaleHold (LastGasp on) | PASS | 424,284 |
| `rx_live_rv` | j20261012070056-462079 | PASS: WorkingTrue, NoStaleHold, RowSettlesCx, KeysSettle | PASS | 144,196 |
| `rx_V_review` | j20261012070009-621c00 | FAIL NeverHit (probe) | FAIL NeverHit | 75 |
| `r5x_time_rv` | j20261012070056-fe6b01 | PASS: every fin1_time_ub property, RowSettlesRv for RowSettles | PASS | 658,632 |
| `r5x_t_rv` | j20261012070056-4df375 | PASS: the 15 invariants | PASS | 4,722,108 |
| `r5x_lim_rowsettles_rv` | j20261012070056-eec2cb | FAIL RowSettles (the documented residual) | FAIL RowSettles | 28,459 |
| `r5x_V_review` | j20261012070009-c6556d | FAIL NeverHit (probe) | FAIL NeverHit | 653 |

Both equivalence levels reproduce their counts exactly. Each reuses the scheduler timeout, memory
and budget of the run it extends (`suite.tsv`, tier `gate`). Counterexamples: `rx_V_review`:
`RevStart`, `RevHook`, `RevEnd`, `WorkWrite("rev")` (rest, row `working`, `lw = "after"`);
`r5x_V_review` and `r5x_lim_rowsettles_rv`: turn, Stop (`aph = "rest"`, row `waiting`), then one
`RevWrite` (row `working`, `rvw`), and the row stays `working` for good.

### Finding F6: the `/review` residual sits inside RowSettlesCx's exception

A review writes `working` through the hold (unmarked tool and prompt hooks), never closes or proves
anything, sends no Stop, and Codex has no idle Notification, so a row it moved to `working` at the
prompt (before a turn, or after the last Stop) stays `working` until the next prompt's Stop, or
until the next Interrupt (Esc during the review gives one main-agent Interrupt, which writes
`waiting`). It does not weaken the hold: the review opens no request, so it adds no unproven request,
and the working write is held by the same `holdingRequestSQL` read as any other. This is exactly the
row left `working` that RowSettlesCx already allows (a working write after the main turn ended: a
subagent's hook, find-missing's repair), so Hold36hCx needs no new exception (`rx_live_rv`
PASS). In B146R5Cx the same residual needs its own named exception, RowSettlesRv (`r5x_time_rv`
PASS, `r5x_lim_rowsettles_rv` FAIL RowSettles). The probes show the residual is reached. To document
for the narrower Q2 answer: "a `/review` can leave the row `working` until the next prompt ends".

### Evidence facts and the model

Evidence: probe answers RN-8 row; `probe/logs/rn8/<v>/review/summary.txt` and, on 0.162.1,
`reviewintr/` (identical on 0.156.0, 0.159.2, 0.162.1).

| Evidence fact | Model element |
|---|---|
| `/review` was only seen typed at an idle prompt | `RevStart` needs `aph = "idle"` (Hold36hCx); `RevWrite` needs `aph \in {"idle", "rest"}` (B146R5Cx). Whether it can start elsewhere is not modelled (see "Not modelled") |
| Its thread sends UserPromptSubmit, PreToolUse and PostToolUse (Bash) | Each is one working write: `RevHook` (read) then `WorkWrite("rev")` (Hold36hCx); `RevWrite` (B146R5Cx). They are not told apart: all three are working writes through the hold |
| They carry the main session id and no `agent_id` | The writes are unmarked: no subagent marking, so they are not the subagent's hook slot; `wr["rev"]` is their own slot and the write is the main row's |
| It opens no request (no PermissionRequest; approval policy Never) | No `st`, `rec`, `ack`, `lock` change by a review action; `AskBegin` is not used |
| It sends no Stop or Interrupt of its own | `RevEnd` writes nothing; only `RevInterrupt` (Esc) leads to `IntHook` |
| Its last PreToolUse has no PostToolUse | Any number of review writes, the last may be a read-only or dead hook (`HookDeath`, `WorkDrop("rev")`); the review ends with a write possibly in flight |
| No Stop follows the review | `RevEnd` has no turn-end proof and no write, so the row stays as the last review write left it (F6) |
| Esc during it gives exactly one main-agent Interrupt | `RevInterrupt`: one `aph = "intdue"`, then `IntHook` as for any Interrupt (proves the recorded main requests, writes waiting); an in-flight review write may land after it |
| `turn_id` | AD does not read it |
| `permission_mode` (`bypassPermissions` on the review's hooks) | AD does not read it |
| The review PostToolUse's `tool_use_id` | AD does not read it on Codex (no tool_ran proof or close with `CxRelay` / `CxR5`: PostToolUse proves and closes nothing), so even a PostToolUse carrying one proves nothing here |

### Not modelled (for Epic 6's live check)

- The other internal subagents (Compact, memory consolidation, guardian): judged from Codex's
  source only; their tool hooks carry no `agent_id` either, and the narrower Q2 answer leans on
  them being tool and prompt hooks only.
- `/review` starting only at an idle prompt: the model lets it start at any main-agent prompt
  (not while a main dialog may be shown), and does not check that Codex refuses it elsewhere (in
  a turn, at a dialog, with a subagent running). The model also does not start it while the turn-end
  hook is still due (`aph = "stopdue"` or `"intdue"`), which the gate allowed it to over-approximate.
- One agent's hooks running in sequence, including parallel tool calls: a review's hooks are one
  slot with at most one working write in flight, as for the main agent and the subagent; parallel
  tool calls inside the review thread are not modelled.
