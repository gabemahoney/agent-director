# b.66h: launch-scoped actions in the model

Bee b.66h lets `kill`, `send-keys`, `read-pane` and `pause` carry the
`launch_id` the caller read. agent-director compares it with the row's
current launch right after its one row read, before every other check. When
they differ it refuses with `ErrLaunchChanged`: no tmux call and no write.
The design is in the bee and in
`tickets/Ideas/b.fmk/launch-scoped-actions.md` (outside this repo).

This directory holds the cfgs that check that design with the vendored model.
These runs are not in `ci/suite.tsv`, so a plain `make tla` does not run
them. To run them on the job scheduler, use
`make tla TLA_SUITE=spec/tla/launch/suite.tsv`. `ci/run_ci.sh` takes the
cfgs from `cfg/` here, without pins, and applies the `props` column of
`suite.tsv`.

## The model change

`../Phase4.tla` is the only spec that changed (`../PROVENANCE.txt` records
it). `Phase4Split.tla` and `Phase5Hook.tla` are unchanged.

**Knobs.** Three knobs are zero-arity definitions, all `FALSE`:

| Knob | TRUE means |
|---|---|
| `LaunchObs` | Each caller records the launch it read, and `ScopedActsOnlyOnObservedLaunch` is checked. |
| `LaunchScoped` | The design: the compare. With `LaunchObs` on and this off, the model is today's code (the control). |
| `RelaunchAny` | The human relauncher may resume the orchestrator's row as well as the bot's. A relaunch can then fall between the orchestrator's kill lookup and its act. |

They are definitions, not `CONSTANTS`, so every b.zuj cfg parses unchanged. A
cfg here turns a knob on with a definition override at the end of its
`CONSTANTS` section, for example `LaunchScoped <- KnobOn`. With all three off,
the model's steps are identical to b.zuj CI run 4's: every new variable keeps
its initial value, and every changed action does what it did before. A b.zuj
cfg without a VIEW therefore reaches run 4's distinct-state count exactly. A
cfg with `VIEW CIView` need not: under that view the count depends on which
state of each view class TLC meets first, so it can vary between runs of the
same model. Only its verdict is comparable.

**What a caller reads.**

- The bot reads the row in `B_Get` and `B_Resume` (variable `bobs`). Its
  kill (`B_NrKill`) and its keys (`B_Reconnect`) carry that launch.
- The orchestrator reads the row its launch left (`O_Launch`, variable
  `oobs`). Its kill lookup (`O_KillLookup`) carries that launch.
- On `changed`, the bot goes back to `B_Get`, and the orchestrator goes to
  `O_Reread`. Each then reads the row again and decides again.
- A `changed` result makes no lookup and no tmux call. It spends no tmux
  fault.

**New properties.**

- `ScopedActsOnlyOnObservedLaunch` (invariant on the ghost `scopeViol`).
  Every process a scoped kill ends, and every process in the pane a scoped
  send types to, belongs to the launch the caller read. The bee asked for an
  action property; a ghost is used because a send's target pane is not in
  the state. The ghost is set in the same steps as `HandsOff`.
- `ChangedIsInert` (action property). A `changed` step leaves the row, the
  sessions and the processes unchanged.
- `NotLaunchChanged`, a probe. It must be violated, which shows that a
  scoped call is refused somewhere.
- `NotRelaunchInKillWindow`, a probe. It must be violated, which shows that
  a relaunch really lands between the orchestrator's kill lookup and its
  act.

**Simplifications.**

- The bot reads the launch in `B_Resume` itself. The real `resume` returns
  no `launch_id` when it refuses a live row, so a real caller needs a `get`
  there. The `get` would read the same launch, so the property is unchanged.
- The compare also comes before send-keys' state guard (`B_SendGuard`). A
  scoped send to a row that is no longer live and no longer the read launch
  is refused as `changed`.

**Generalised for the relauncher.** The orchestrator's kill takes two steps:
`O_KillLookup`, then `O_KillAct`. With `RelaunchAny`, a new launch can start
between them. The existing ghosts of `HandsOff`, `KillHonest` and
`KillPendingOnlyCurrent` took "the row's current launch" at the act. For that
kill they now take the launch its lookup read (`okl`). This is what the code
does: it acts on the label and tmux ids from its one row read. Without
`RelaunchAny` the two are the same launch, so the b.zuj runs are unchanged.

**Not modelled.**

- `read-pane` changes nothing, so it has no ghost to check.
- `pause`'s wait.
- The startup-prompt approver (`send-keys --allow-pending`): it reads and
  acts in two steps of its own and is not scoped here.
- The operator's finished-row kill, `agent-director-admin kill-finished`
  (`HK_*`), is not scoped.

## The runs

All runs are safety runs of `Phase4Split` (the bounds of their base cfgs).

| cfg | Base | Expected | What it shows |
|---|---|---|---|
| `ls_V_bot` | `ci_S1h_lab` | `NotLaunchChanged` violated | A bot's scoped kill or keys get refused. |
| `ls_V_orch` | `ci_S1o_lab` + human resume | `NotLaunchChanged` violated | The orchestrator's scoped kill gets refused. |
| `ls_V_window` | `ci_S1o_lab` + human resume | `NotRelaunchInKillWindow` violated | A relaunch lands between the orchestrator's kill lookup and its act. |
| `ls_C_S1h_lab` | `ci_S1h_lab` | `ScopedActsOnlyOnObservedLaunch` violated | Control: with the compare off, the bot ends or types into a launch it never read. |
| `ls_C_S1o_lab` | `ci_S1o_lab` + human resume | `ScopedActsOnlyOnObservedLaunch` violated | Control: with the compare off, the orchestrator ends a launch it never read. |
| `ls_Cact` | `ci_Cact` + human resume | `HandsOff` or `ScopedActsOnlyOnObservedLaunch` violated | Accepted residual: the pane-id window after a server restart (`ActPidCheck` off) is still open. The compare does not close it. |
| `ls_S1o_lab` | `ci_S1o_lab` + human resume | pass | The design, orchestrator side, with a relaunch between lookup and act. |
| `ls_S1o_rst` | `ci_S1o_rst` + human resume | pass | The same, with a server restart. |
| `ls_S1h_lab` | `ci_S1h_lab` | pass | The design, bot side. |

`ls_S1h_lab` and its control are based on `ci_S1h_lab` (bot and human
resume), not on the bee's "G3b-style" sets (bot, human kill and a leftover).
`ci_S1h_lab` already has the relauncher and is a fast-tier run. The G3b sets
have no relauncher. Adding one to them would also put the operator's
unscoped kill (`HK_Lookup`, then `HK_Act`) next to a relaunch in a bot safety
run for the first time. That is a separate race, and this change leaves it
alone. `ls_S1o_lab` does combine them, but with no grouped viewer and no
`remain-on-exit`: a relaunch can then start only after the old launch's
session and process are gone, so the operator's act finds nothing to end.

The shortest control traces are about 11 to 14 steps. For example, on the
bot side:

1. The bot spawns, launches and reads the `pending` row (launch L1).
2. L1 reports in, writes a transcript, ends and exits.
3. The human resumes the row (launch L2).
4. The bot's kill, scoped to L1, ends L2.

The `what` column of `suite.tsv` and the header of each cfg say the same.
