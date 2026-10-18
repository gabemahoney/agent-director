# b.w21: why the TLA+ models did not catch it

Analysis only. No model, test or source was changed or run. Line numbers are from
this worktree (branch `analysis/b.w21-tla`, based on `ccb41e07`, which is v0.12.0
plus one docs commit). The bug is b.w21 in the Bugs hive. CSCB's evidence is
`claude-slack-channel-bots-project/Bugs/b.pks/b.pks.md`.

## Short answer

1. **No model starts from an upgraded store.** Every spec that has permission
   requests starts with none recorded. No spec has a "recorded before v7" request,
   and none has a request from an earlier launch of the same row.
2. **The two halves of the bug sit in different models.** Phase4 has the startup
   approver and resume, but no permission requests. B146R5Cx has the relay guard and
   the close, but one launch, no kill and no resume. The bug needs both together.
3. **The models follow the v7 rules, not the pre-v7 branch.** The model's close
   closes every open request, decided or not. That is right for v7 rows. The code's
   `CASE WHEN settled_at IS NULL THEN decision IS NULL` branch has no counterpart
   in any model.
4. **The liveness properties could not see it, even in principle.** A 24-hour hold
   is still "eventually" to TLC, because time is weakly fair. The nearest property,
   `StraySettles`, also excuses "the agent is gone", which is exactly this case.
5. **In the suite as it ships, the close never even runs.** `FMMissing` needs a
   crashed agent. None of the ten B146R5Cx cfgs turns on `Crash`, so the close
   (`CloseOnMissing`) is never reached in any run in this repo.

The smallest change that makes TLC report this bug is one knob that adds one
pre-v7 decided request to `Init`, plus one invariant, `RelayHoldOnlyCurrentLaunch`
(section 4). TLC then fails in the initial state, which is the cutover state.

## What the bug needs

Each of these has to be in the model for TLC to find the bug:

| # | Ingredient | Code |
|---|---|---|
| a | A request recorded before v7 (`settled_at` NULL), already decided, never acked | `internal/store/permission.go:230` `PreV7()`; migration leaves the new columns NULL, `internal/store/schema.go:771-775` |
| b | The close skips it, because for a pre-v7 row "awaiting" means "undecided" | `permission.go:277-279` `awaitingAnswerSQL`; `internal/store/request_close.go:28-41` |
| c | The row gets a new launch. On a missing row the request counted as closed only because of the row's state, and resume undoes that. | `pkg/api/sendkeys_relay.go:77-79` (`finishedState` in `closed`); `pkg/api/decide.go:548-550` |
| d | The guard reads every request the row has ever had. It judges a pre-v7 one by `created_at` + 24 h + 2 s and ignores `proven_gone_at`. | `pkg/api/sendkeys.go:667-669`; `sendkeys_relay.go:94-99`; `pkg/api/relay_delivery.go:188-193, 259-262`; `pkg/api/deliverability.go:168-170, 196-198` |
| e | The caller needs its keys within minutes: the startup dialog, a pending row before SessionStart, with `allow_pending` | `sendkeys.go:616`; CSCB's approver |

## Which models touch any of this

| Spec | Permission requests | Relay guard on plain send-keys | Close of requests | New launch (kill, resume) | Migrated initial state |
|---|---|---|---|---|---|
| `Phase4.tla`, `Phase4Split.tla` (also `launch/` cfgs) | none | none; the approver has no store refusal | none | yes | no |
| `Phase5Hook.tla` (also `subagent/` cfgs) | none (subagent records only) | none | subagent records only | yes | no |
| `Phase5HookCx.tla` | none; pause defers the relay hold to Hold36hCx (`:328-329`) | none | none | yes | no |
| `hold36h/tla/Hold36h.tla` | yes | none (row hold only) | none | no | no |
| `codex/src/Hold36hCx.tla` | yes | yes, as the property `PlainOK` | none | no | no |
| `codex/src/B146R5Cx.tla` | yes | yes, `StraySend` | yes, `FMMissing` | no | no |
| `settle/B3ieSettle.tla` | none | none | none | n/a | **yes**: the only migrated-state model (`:44, 57-58, 185-187`) |

So no model has both the guard and a second launch. And no model with requests
starts from an upgraded store, although B3ieSettle shows the pattern was known:
it starts from "a migrated row" for the v8 settle columns.

## 1. Pre-v7 requests, the upgrade, and earlier launches

**Pre-v7 requests: not represented anywhere.**

- B146R5Cx `Init` records no request: `rec = [r \in R |-> FALSE]`,
  `dec = ... "none"`, `ack = ... FALSE` (`B146R5Cx.tla:468`). Hold36hCx and Hold36h
  do the same (`Hold36hCx.tla:233`, `Hold36h.tla:149`).
- A request can only come from `Ask(r)` inside a turn (`B146R5Cx.tla:520-524`,
  `aph = "turn"`). So every request in every run belongs to the running agent's
  own launch and is v7-shaped.
- The closest thing to the pre-v7 judgement is `LiveCheck = "time"`
  (`B146R5Cx.tla:139-140, 369-373`). It judges a hook by elapsed phase, not by its
  process. That is the v7 "can't tell" branch (`relay_delivery.go:267-272`), applied
  to a request this run's hook just wrote. It is not the pre-v7 branch
  (`relay_delivery.go:259-266`), which ignores the hook and turns a decided request
  into `delivered` only after the window.
- Pre-v7 "decided means not awaiting" exists only as a whole-run control:
  `Off` containing `"AckFirst"` makes `Closed(r)` read `dec[r] # "none"`
  (`B146R5Cx.tla:381`). All ten B146R5Cx cfgs have `Off = {}`. And a single run is
  either all-old or all-new. A store with old and new rows side by side, which is
  what the migration produces, cannot be expressed.

**Store upgrade or migration: not represented, except for settle.** No action
or initial state in any relay model stands for `migrateV6toV7`
(`schema.go:796`) or its backfill (`schema.go:756-764`). B3ieSettle models the v8
backfill only (`B3ieSettle.tla:57-58, 185-187`).

**Requests left over from an earlier launch of the same row: not represented.**

- B146R5Cx has one agent process. `proc` goes from `"alive"` to `"gone"` only
  (`AgentCrash`, `B146R5Cx.tla:595-603`) and never comes back. There is no kill and
  no resume in `Next` (`:1239-1246`, `Plain` at `:1213-1223`). A row marked
  `missing` stays missing.
- Phase4 and Phase5Hook have launches and lives (`Phase4.tla:381-386` claim;
  `Phase5Hook.tla:195-205` `Launch`), but no permission requests.
- The pattern that would have caught this exists for subagent records:
  `SubNoStaleRunning` says a running record belongs to the current launch
  (`Phase5Hook.tla:358`), and `Launch` closes old ones (`:205`). Permission requests
  never got the same treatment. They also carry no launch id in the store; the
  guard reads all of them (`sendkeys.go:667-669`).

## 2. Is the guard and the close modelled faithfully?

**The guard: no. It is a stricter abstraction that hides this bug.**

- The model's guard on a plain send-keys is `~AnyJudgedAlive` (`B146R5Cx.tla:1097`),
  where `AnyJudgedAlive == \E r \in R : rec[r] /\ JudgedAlive(r)` (`:373`).
- The code's guard is `hookMayAnswer` (`sendkeys_relay.go:94-99`): not closed, and
  the hook is seen alive or the delivery is `not_confirmed`. The two differ:
  - The model does not consult `Closed(r)` (code: `if r.closed(pr) return false`).
  - The model judges the hook. The code judges a pre-v7 request by its record
    and the clock only. Nothing in the model is "a record whose hook is unknown,
    held for 24 h from a `created_at` in the past".
  - Model time is a per-request phase that starts when this run's hook starts
    (`Tick`, `:709-721`), and `Tick` is weakly fair (`:1262`). A hold by time
    always ends a few steps later.
- **proven_gone is ignored by the guard, in both places, but the model can never
  show why that matters.** The model's `unp` (Unproven) feeds only the round-5
  rule (`R5OK`, `:449-452`), never rule 1. That matches the code. But in the model a
  request whose agent is gone always comes with `proc = "gone"`, and `StraySend`
  needs `proc = "alive"` (`:1096`). The state "the request's agent is proven gone,
  yet a new agent of the row is alive and wants keys" does not exist, so ignoring
  proven_gone is never tested.
- Hold36hCx models the guard as a property, not an action: `PlainOK`
  (`Hold36hCx.tla:673-677`). Its rule 1 is `st[r] # "hook"`, the hook's real state,
  not a time-based `not_confirmed`. A record with no live hook can never hold there.
- Before SessionStart, no request exists in the model (`Ask` needs a turn), so
  the guard can never hold a pending row before SessionStart. The probe
  `NeverHeldNoRequest` (`:1392`) even asserts the round-5 guards never hold with
  no request. Nothing asserts that rule 1 doesn't either.

**The close: faithful for v7 rows only.**

- `FMMissing` (`B146R5Cx.tla:1195-1204`) closes every recorded request not already
  closed (`cls' = ... rec[q] /\ ~Closed(q)`) and denies the undecided ones. With
  `AckFirst` on, "not closed" is "not acked". That is the code's v7 branch
  (`delivered_at IS NULL`, `permission.go:279`).
- The code's pre-v7 branch (`settled_at IS NULL THEN decision IS NULL`,
  `permission.go:278`) has no counterpart, so the decided-pre-v7 row that the close
  skips cannot exist in the model.
- The model also counts every request of a missing row as closed
  (`Closed(r) ... \/ CloseOnMissing /\ rs = "missing"`, `:383`), mirroring
  `finishedState` in `closed` (`sendkeys_relay.go:78`). Because the model never
  resumes, "closed by row state" is permanent there. In the code it lasts only
  until resume moves the row to pending. That is how the request escapes the
  close and then comes back to life.
- The close's third statement (prove agent_gone, `request_close.go:46-48`) is
  modelled indirectly by `proc = "gone"`. See the proven_gone point above.
- **The close is never reached in the shipped runs.** `FMMissing` needs
  `proc = "gone"`, which only `AgentCrash` sets, and `AgentCrash` needs `"Crash"`
  in `Env` (`:596`). The `Env` of every `codex/src/cfg/r5x_*.cfg` leaves `Crash`
  out. All ten also have `N = 1` and `NI = 1`.

**Interesting:** the schema's own comment says the close covers "a request that
still awaited an answer then, decided or not, one recorded before v7 included"
(`schema.go:710-716`). The intent was to cover pre-v7 rows. The predicate it
uses excludes decided pre-v7 rows by definition.

## 3. Is there a liveness property like "a gone agent's row eventually accepts send-keys"?

No, not in that form. These are the nearest, and why each passes:

| Property | Where | Why it can't fail on this bug |
|---|---|---|
| `StraySettles == <>(st = "done" \/ proc = "gone")` | `B146R5Cx.tla:1365`; checked by `r5x_time` and four other cfgs | It excuses "the agent is gone", and with one launch a gone agent ends the obligation. There is no later launch that still needs keys. Even with a pre-v7 request added, a fair `Tick` ends the time hold eventually: 24 h is still "eventually". |
| `KeysSettle == <>[]((Rest /\ ~SubUnproven) => PlainOK)` | `Hold36hCx.tla:686`; `rx_live*` cfgs | `Rest` requires an idle agent after a turn (`:657-659`), never a pending row before SessionStart. Rule 1 is the hook's real state, and no request predates the run. |
| `PendingResolves` | `Phase4.tla:1231-1232`; `Phase5Hook.tla:328` | Its own exception covers this case: a pending row with a live agent process counts as resolved (`CurProcs(i) # {}`, `row.pn # 0 /\ AgentAlive`). An agent stuck at its startup dialog satisfies it. |
| Approver fairness | `Phase4.tla:1165-1167` (`WF_vars(B_ApproveLookup)`, `B_ApproveAct`) | The approver is fair and has no store refusal (`:1008-1033`). The model assumes the startup dialog can always be approved. |

The deeper reason: **this is a bounded but far too long hold, and liveness cannot
measure duration.** CSCB gives up after 5 minutes; AD holds for 24 h. A liveness
property passes as long as the hold ends at all. To catch it you need either a
safety property (the hold should never happen) or a model where the hold's clock
does not run inside the window that matters (section 4, variant C).

## 4. The smallest model change that produces this counterexample

All of these go in B146R5Cx, which already has the guard, decide, decided-but-unacked
records, the close and the `"time"` judgement. They sit behind a zero-arity knob,
like the b.ssz knobs, so every existing cfg and its state counts stay unchanged.

### A. Minimal: one initial state and one invariant (fails at state 1)

```tla
PreV7Old == FALSE                      \* knob; a cfg sets PreV7Old <- KnobOn
L        == 0                          \* the request left over from before the upgrade
R        == IF PreV7Old THEN 0 .. N ELSE 1 .. N
Legacy   == IF PreV7Old THEN {L} ELSE {}

\* In Init, for L only: recorded before v7 by an earlier launch, decided, never
\* acked, never closed, proven gone (the close's agent_gone), its hook done,
\* still inside its window (ph = 0); everything else as today (row pending,
\* aph = "ss": the new launch before SessionStart).
\*   rec[L] = TRUE, dec[L] \in {"allow", "deny"}, ack[L] = FALSE, cls[L] = FALSE,
\*   h[L] = "done", ph[L] = 0, dl[L] = "ans", got[L] = dec[L], unp[L] = FALSE

\* Ask must skip L:        Ask(r) == r # L /\ ...
\* A pre-v7 request is judged by its clock alone (relay_delivery.go:259-262):
JudgedAlive(r) == IF r \in Legacy THEN ph[r] < 2 ELSE <today's CASE>
\* Tick must also advance L under LiveCheck = "exact": add  \/ r \in Legacy

\* hookMayAnswer (sendkeys_relay.go:94): not closed, and its hook may answer.
RelayHolds(r) == rec[r] /\ ~Closed(r) /\ JudgedAlive(r)
\* b.w21: a request of an earlier launch never holds the relay guard.
RelayHoldOnlyCurrentLaunch == \A r \in Legacy : ~RelayHolds(r)
```

Run it with `r5x_time.cfg` plus `PreV7Old <- KnobOn` and
`INVARIANT RelayHoldOnlyCurrentLaunch`. TLC reports a violation in the initial
state: the row is pending before SessionStart, the agent is alive, the plain
send-keys is waiting (`st = "want"`), and L (decided, unacked, proven gone,
`ph = 0`) holds it. That is the cutover state in b.pks.

### B. With the "nothing clears it" part (a 4-step trace)

Add three things to A:
- `"Crash"` in `Env`.
- The pre-v7 branch in `FMMissing`'s close:
  `cls' = [q \in R |-> cls[q] \/ (rec[q] /\ ~Closed(q) /\ (q \in Legacy => dec[q] = "none"))]`.
- One action, `Relaunch` (kill + resume): `rs = "missing"` becomes `"pending"`,
  `proc' = "alive"`, `aph' = "ss"`, with a boolean ghost `nl' = TRUE`.

Change the invariant to `nl => \A r \in Legacy : ~RelayHolds(r)`, and in `Init`
let L belong to the running launch. TLC then gives:
`Init → AgentCrash → FMMissing` (L is not closed, only read as closed while the
row is missing) `→ Relaunch` (L holds again) → violation. This also shows that
kill, find-missing and resume do not clear it.

### C. If you want a liveness property to fail instead

Keep `StraySettles` and stop `Tick` from advancing L, because 24 h is longer than
any launch's startup bound. TLC then reports a lasso: the plain send-keys waits
forever while the agent stays alive.

**Fidelity notes for whoever builds this.** Past its window, a pre-v7 decided
request is `delivered` in the code unless its verdict is a close's deny
(`relay_delivery.go:263-266`, `CloseDenied` at `permission.go:239-241`). The model
would instead call it fallen back and let `OrcAct`'s record-pane-answer close it
(`B146R5Cx.tla:1034-1040`). In the code that write can't land: it is guarded by
`awaitingAnswerSQL` (`internal/store/pane_writes.go:225-228`). Model both, or the
model will hide the issue in section 5, item 1.

## 5. What else the same gap might hide

These come from reading the code only. Nothing here was run.

1. **After 24 h, b4beedcb may turn into a hold that never clears.** b4beedcb is
   CSCB bot's request (deny, `decision_reason = find_missing`). That reason makes
   it `CloseDenied`, so once its window passes `deliveryOf` returns `fallen_back`,
   not `delivered` (`relay_delivery.go:263-266`). Its `closed_at` is NULL and the
   row is live, so `fallenBack` is true. Plain send-keys then gets
   `ErrRelayFallenBack` (`sendkeys_relay.go:189-191`) in place of
   `ErrSendKeysWhileRelayed`. The usual way out is a pane answer or
   record-pane-answer. Both write only `WHERE ... awaitingAnswerSQL`
   (`pane_writes.go:64-68, 225-228`), which a decided pre-v7 request never matches,
   and the close skips it too. So the hold may simply change error at
   2026-10-15T01:19:52Z and not end. Any pre-v7 request denied by find-missing or
   by `ended` is in the same class. The b.w21 regression should cover the
   post-window case too.
2. **Pre-v7 requests on a live row at upgrade.** The migration proves gone only
   the requests of finished rows (`schema.go:756-764, 784-786`). On a live row,
   every pre-v7 request is unproven, so rule 3 (`ErrDialogMaybeOpen`) holds plain
   send-keys and pause until the agent's next Stop or idle Notification. An
   undecided one also holds rule 1 for 24 h. No model starts with an unproven
   request at rest.
3. **Pause is held the same way.** It shares the guard (b.zdz, `sendkeys.go:644-646`).
   Phase5HookCx defers pause's relay hold to Hold36hCx (`Phase5HookCx.tla:328-329`),
   which has no pre-v7 or cross-launch requests either.
4. **Anything "closed" only by the row's state comes back on resume.** `closed()`
   includes `finishedState` (`sendkeys_relay.go:78`). Any request the close does not
   stamp with `closed_at` reopens when resume moves the row to pending. Today that
   is acked requests (harmless: `delivered`, judged by the hook process), completed
   pane answers (closed anyway), and decided pre-v7 requests (this bug). Any future
   column that makes a request "not awaiting" without being "closed" will leak
   across launches the same way. Permission requests have no launch id, and the
   guard reads them all.
5. **Two store readers disagree on whether such a row is closed.** The cap
   eviction deletes requests that are "not awaiting" and proven gone
   (`internal/store/relay_writes.go:360-373`). It treats the decided pre-v7 request
   as closed and evictable, while the guard treats it as live. Whether the hold
   ends early therefore depends on store pressure. That is harmless, but it is
   another sign the code holds two definitions of "closed".
6. **Undecided pre-v7 requests of finished rows** stay undecided until resume's
   close denies them (`schema.go:787-788`). This one is handled, because the
   undecided branch is "awaiting". But it relies on the same un-modelled resume
   path, so no model has ever checked it.

## What to take from this

- The models answered "is the v7 design safe and live for one launch of a fresh
  store?". They answered it well. b.w21 lives where three untested things meet:
  the migration's initial state, a second launch of the same row, and the pre-v7
  branch of each predicate.
- Every migration that adds a nullable column with a `CASE WHEN col IS NULL`
  branch should add a knob giving the model a mixed initial state, as B3ieSettle
  does for v8.
- Every per-row record set needs the launch-scoping property subagent records
  already have (`SubNoStaleRunning`). For requests, that property is
  `RelayHoldOnlyCurrentLaunch`.
- Prefer safety properties when the real requirement has a deadline. In TLC,
  "eventually" lets a 24-hour hold pass.
