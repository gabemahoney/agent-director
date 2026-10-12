# b.ssz Epic 5: TLA+ check of Gabe's life and resume rule (2026-10-12)

Gabe's rule (`../../e5-brief-life-rule.md`; SRD t1.ssz.pm SR-6.8.9), in his words: "Resume always continues the last agent's latest session. Switching agents without resume is fine. The one thing you can't do is resume across a switch." In detail:
1. A new life begins only when the agent changes; a same-agent reuse, `resume` and the agent's own session changes keep it.
2. Subagent records belong to a life: kept across launches of one agent, never across a switch.
3. AD-agent children are not tied to a life: a switch neither hides nor stops them.
4. A switch gives up everything before it, for good; a switch back is a new life, not a return.
5. `resume` relaunches the latest session of the current life, never an older one (B's history fallback is removed).
6. The key case: Claude, then Codex, then Claude launched with `--resume` is not an error; Claude starts fresh, and the old Claude session is never resumed.
7. Nothing shown is ever false.

`src/LifeRule.tla` is a small standalone model of one row: spawn; `resume` (the current session only); reuse naming either agent and either of two folders; the restore after a failed reuse; a launch given up; the agent's own session change; subagent starts and stops; one AD-agent child; and, under controls only, B's history fallback, a reuse that resumes across a switch (`ShortFormsPass`) and a same-agent pick of an older session (`SamePick`, `SamePickPass`). It does not model the store's concurrency or the hook gate (b.xjn's and b.fmk's models check those, and the rule does not change them).

`mkjob.py <dir>` builds a self-contained scheduler job (the unchanged `tlcjob.sh` and `tla2tools.jar` from `agent-director-bssz/spec/tla`, the spec, one cfg per run, the `runs` file). No TLC ran on the dev host: every job ran through the job scheduler (channel C0B3X876XSB, `--network off`, `--memory 8g --timeout 1h`). Constants: `Sids = {s1, s2}`, `Kids = {k1}`, `Locs = {L1, L2}`, `MaxLaunch = 5`, `MaxSess = 4`.

## Properties
- Never false (invariants): `SubsTruth`, `UntrackedEmpty`, `NoStaleRunning`, `HistTruth`, `ResumeOpens`, `SwitchForgets`, `KeyCaseNoOldSession`, `NeverOlder`, `KidsShown` (and `TypeOK`).
- The rule (action properties): `LifeIffAgent`, `CarryInLife`, `KidsUntouched`, `ResumeLatest`.
- Probes (must be reached): `NotHidden`, `NotCarried`, `NotKidAfterSwitch`, `NotHistCarried`, `NotKeyCase`, `NotSwitchedBack`, `NotResumed`.

## Knobs (all FALSE = the design)
| Knob | Control |
|---|---|
| `LifePerLaunch` | every reuse starts a new life (B today) |
| `NeverNewLife` | a switch keeps the life too |
| `NoLifeFilter` | subagent records keyed and shown across lives (b.xjn as merged) |
| `NoClose` | no write closes a running record |
| `ChildFollowsLife` | `list --parent` filtered by the parent's life (the withdrawn `parent_life_number` design) |
| `HistoryFallback` | `resume` falls back to an older session when the latest transcript is gone (B today) |
| `NoFolderCheck` | with `HistoryFallback`, the fallback ignores the folder a session ran in |
| `SwitchBackReturns` | a switch back to an agent returns to its old life |
| `ShortFormsPass` | a switching reuse's session-picking argument reaches the agent (the design drops it: a fresh start) |
| `SamePickPass` | a same-agent reuse's session-picking argument, or one replayed by `resume`, reaches the agent (the design refuses it, b.d6q, and strips it from the replay) |

## Jobs
1. `job-lr/`, j20261012201229-a982b2: nine of ten as expected; its no-close control only turned off the launch-time close and passed with the design's exact count (vacuous: a row is finished, and its running records closed, before any launch). Superseded.
2. `job-lr2/`, j20261012201507-371f56: thirteen of thirteen as expected, with the folder model; at the time `resume` still had B's fallback plus a folder check. Superseded by Gabe's resume rule.
3. `job-lr3/`, j20261012203214-a71da1: cancelled (predated the resume rule).
4. `job-lr4/`, j20261012203534-a97883: `lr_S` FAILED `NeverOlder` (651 states) - a model bug: the restore after an undone same-agent reuse did not put back the ghost recording that reuse, so the restored latest session counted as "older". The design was right (the undone reuse never happened, and resuming its latest session is Gabe's rule). Every other verdict was as expected. Fixed in the model.
5. `job-lr5/`, j20261012203850-af6e9d: cancelled (had the same ghost bug).
6. `job-lr6/`, j20261012204016-ecfe2b: all twenty as expected, but the key-case control's shortest counterexample was Claude to Codex picking an old Claude session, not the full Claude, Codex, Claude path. The key-case invariant was made specific to that path (`SwitchForgets` keeps the general form).
7. `job-lr7/`, j20261012204524-4b8b97: all twenty as expected, with the key case modelled as a refusal (Gabe's first wording).
8. `job-lr8/`, j20261012210609-7a49cb: all twenty as expected after Gabe's correction of the key case (not an error, a fresh start that resumes nothing); in the design `ReuseResumeAcross` is the ordinary switching reuse, so `lr_S` reached exactly job 7's 6,202,169 states.
9. `job-lr9/`, j20261012213304-72fa49: adds the same-agent pick (b.d6q folded in, resume's replay stripped). All twenty-one as expected, but `lr_C_samepick` failed through the cross-switch path, because one knob opened both paths.
10. `job-lr10/`, j20261012213844-d4b7e2: the final run, below. The same-agent pick has its own knob (`SamePickPass`), and `lr_C_samepick` fails on a same-agent path: Codex rotates to session 2, ends, then a same-agent pick reopens session 1.

After job 10, two comments in `src/LifeRule.tla` were corrected (the header's item 4 and `ResumeCurrent`'s comment still described the removed fallback); no definition changed, so job 10 stands for the current source.

## Final run (job 10)
| Run | Expected | Result | Distinct states |
|---|---|---|---|
| `lr_S` (the design: every invariant and property) | PASS | PASS | 6,202,169 |
| `lr_C_lifeperlaunch` (every reuse a new life, B today) | FAIL LifeIffAgent | FAIL LifeIffAgent | 35 |
| `lr_C_nevernew` (a switch keeps the life) | FAIL HistTruth | FAIL HistTruth | 64 |
| `lr_C_nofilter` (records shown across lives) | FAIL UntrackedEmpty | FAIL UntrackedEmpty | 217 |
| `lr_C_noclose` (no write closes a running record) | FAIL NoStaleRunning | FAIL NoStaleRunning | 85 |
| `lr_C_fallback` (B's history fallback) | FAIL ResumeLatest | FAIL ResumeLatest | 283 |
| `lr_C_fallback_old` (B's fallback after a same-agent reuse) | FAIL NeverOlder | FAIL NeverOlder | 533 |
| `lr_C_nofolder` (B's fallback, same-agent life, no folder check) | FAIL ResumeOpens | FAIL ResumeOpens | 569 |
| `lr_lim_b_nofolder` (B: fallback and a life per reuse) | PASS | PASS | 23,009,175 |
| `lr_C_across` (a switching reuse's resume argument reaches the agent) | FAIL SwitchForgets | FAIL SwitchForgets | 211 |
| `lr_C_keycase` (Claude, Codex, Claude: the resume argument reaches Claude) | FAIL KeyCaseNoOldSession | FAIL KeyCaseNoOldSession | 1,452 |
| `lr_C_samepick` (a same-agent reuse or a replayed argument picks an older session) | FAIL ResumeLatest | FAIL ResumeLatest | 206 |
| `lr_C_switchback` (a switch back returns to the old life) | FAIL SwitchForgets | FAIL SwitchForgets | 500 |
| `lr_C_childlife` (`list --parent` filtered by life) | FAIL KidsShown | FAIL KidsShown | 413 |
| probes `NotHidden`, `NotCarried`, `NotKidAfterSwitch`, `NotHistCarried`, `NotKeyCase`, `NotSwitchedBack`, `NotResumed` | each reached | each reached | 56 to 1,423 |

The key-case control's counterexample is exactly Gabe's case: Claude spawns and reports session 1, ends; the id is reused as Codex, reports session 2, ends; then a reuse naming Claude whose resume argument reaches Claude (`ReuseResumeAcross` under the control; the design drops the argument and starts fresh) makes session 1 current again.
