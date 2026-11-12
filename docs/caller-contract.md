# Caller contract

Rules for programs and agents that call agent-director. The class of every tmux
error:
[tmux refusal classes](architecture.md#caller-contract-tmux-refusal-classes).

- Only a GONE answer means a row's session is not there. Any other tmux error
  means "don't know", never "dead".
- Row state, reconciled by the [bounded recovery checks](#bounded-recovery-on-state-reads),
  is the liveness authority. `displayed_state` never changes it.
  `process_liveness` `alive` never confirms or refreshes it; a proven exit can
  establish `ended` or `missing` (see [Confirmations](#confirmations)).
  For launch readiness, poll with `status`;
  call `read-pane` only while the row is `pending`, at
  most once a second.
- Run as the same user, in the same tmux environment, as the agents.
- No verb removes a row you name; only a scheduled `expire` does.
- A situation that needs a human is in [Operator actions](operator-actions.md);
  never perform those steps. A `spawn` whose tmux session name is held returns
  an error naming the holder and ends its new row: leave the holder to a human.
- A call refused before anything was written or sent is safe to re-issue. Retry
  `ErrTmuxUnresponsive` and `ErrTmuxKillFailed` with backoff and a cap, and
  alert at the cap, but not a timed-out `spawn` or `resume`, nor a `send-keys`
  whose text may be typed (see below). After a `kill` ends the last session on
  a tmux server, a
  repeated `kill` can get `ErrTmuxUnresponsive` or `ErrTmuxNotAvailable`: wait
  and check again.
- A manual `send-keys` returning `ErrTmuxUnresponsive` with "may have been delivered" or
  "may be typed": do the step the error names, never resend the same text.
  `--text ""` sends Enter only. Retry a `pause` that returns
  `ErrTmuxUnresponsive` later.
- `ErrSubmissionUnconfirmed` with `outcome: "gateway_refused"`: follow
  [Cursor input outcomes](#cursor-input-outcomes). Otherwise, for automatic
  `send-keys`, follow
  [Ordinary text submission](#ordinary-text-submission); for manual
  `send-keys`, follow [Manual input ownership](#manual-input-ownership).
- `ErrPauseTimeout`: inspect before further input; never retry `pause`
  automatically. Follow [When pause cannot confirm exit](#when-pause-cannot-confirm-exit).
- `resume`, or a supported `spawn --reuse-finished` refused as "still stopping" or "still
  starting": wait and retry. Refused with "this row's own id", or
  `ErrTmuxSessionConflict` "this id's own abandoned launch": surface the named
  session to a human; never end it yourself.
- After a timed-out `spawn` or `resume` the row stays `pending`: do not retry
  until `get` shows `ended` or `missing`, then `spawn --instance-id`
  with `--reuse-finished` and the row's agent (`get` shows it). For Cursor,
  inspect the latest session and follow the
  [resume or fresh-instance boundary](#cursor-operation-boundaries). A row noted `unreported` has a running agent: use it
  or end it first (see [Maintenance](maintenance.md)).
- `ErrConfigMalformed` (a config value is refused): store-backed verbs fail,
  `serve` does not start, hooks record nothing and relayed permission requests
  are denied. Take no action, alert once, never read it as "dead"; a human fixes
  the value it names. An unknown argument or wrong-typed value returns
  `ErrInvalidFlags` and does nothing; `make-template` on an existing name
  without `--overwrite` returns `ErrTemplateExists`.
- `ErrOldLayout` (this host still has the old directory and no store in the
  current one): every store-opening verb fails, `serve` does not start, hooks
  record nothing and exit 0, relayed permission requests are denied, and
  `trail-emit` returns `ErrTrailWrite`. Nothing was written. Take no action,
  alert once, never read it as "dead"; a human runs this release's `install.sh`
  (see [Operator actions](operator-actions.md#a-verb-refuses-with-erroldlayout);
  [Upgrade](install.md#upgrade)).
- With `HOME` unset or empty, a `--home` (TypeScript `home`) of `~` or
  `~/…` refuses every call with `ErrInvalidFlags`; with no `--home`, every
  store-opening call returns `ErrStoreOpen`, even with an absolute
  `--store-path`. Set `HOME` or pass an absolute `--home`.
- `ErrJSONMarshal`: the verb succeeded and its changes stand, but its result
  was lost. Check `get`, `status` or `list` before repeating a call that
  changes something.
- Detect features by the binary's version (`agent-director version`, MCP
  `version`, TypeScript `binaryVersion`); `X.Y.Z-rc.N` counts as `X.Y.Z`. The
  `version` result's `config_dir` names the directory the binary keeps its
  files in.
- A TypeScript call that outlasts `callTimeoutMs` (30 s by default) returns
  `ErrCallTimeout` while the verb may still complete: check the row first.

`kill` success on a live row verifies the process checks of that call. A
finished-row call is a no-op without checking the process; it is not proof
of exit. `missing` is a judgement on available evidence, not an
exit proof. Follow the sequence below before relaunching an id.

To end a live row (`pending` included) and relaunch its id:

1. `agent-director kill --instance-id <id>`; on an error follow its
   class.
2. A `pending` row: wait until its `launch_started_at` (in `status`) plus
   `pending_grace_seconds` (ask the operator for the value in force, see
   [timing settings](configuration.md#timing-settings-tmux)) has passed. Inside
   the grace period, wait and check later; never escalate.
3. Run `sweep`, then check `status`; repeat about 5 s apart until `ended` or
   `missing`, at most three runs.
4. Still live: `kill` once more, wait about 5 s, run `sweep` once more and check.
5. Still live: stop and escalate to a human.
6. Once `ended` or `missing`, Cursor uses the
   [resume or fresh-instance boundary](#cursor-operation-boundaries). For Claude
   or Codex:
   - with a session id, to get the conversation back:
     `agent-director resume --instance-id <id>`
   - otherwise, a reuse (a fresh conversation with no memory of the old one), with
     the row's agent, which `get` shows:
     `agent-director spawn --instance-id <id> --reuse-finished --agent <agent> --cwd <dir>`

   A caller whose ids agent-director mints spawns fresh instead.

## Bounded recovery on state reads

`status`, `get`, `get-many`, `list` and `sweep` share eligible recovery checks.
`find-missing` remains a supported alias of `sweep` on CLI and MCP, as do
Go `FindMissing`/`Client.FindMissing` and TypeScript `findMissing`.
`status` and `get` check only the named row. `get-many` checks each named ID
in order. `list` selects IDs using the stored state, displayed state, other
filters and limit before checking them.
It returns those same IDs after recovery, even if a returned value no longer
matches the filter; it neither refills limit slots nor adds newly matching rows.
Invalid filters are refused before checks. Reading a parent does not check
hidden children; [family reporting](#family-reporting-limits) uses stored facts.

Checks can reconcile process or gateway loss, missed Codex turn endings,
stale permission state, display observations, newly available transcripts
and late exact input proof; a check of the agent process also records
[`process_liveness`](#confirmations). Each runs only with applicable evidence and
available capabilities. Finished rows skip liveness, turn, permission,
display and transcript checks; late exact input proof remains eligible.
Pending grace and a live launch owner protect an unfinished launch, except
from [exact Cursor gateway loss](#cursor-operation-boundaries).

Work is bounded per call (per ID for [`get-many`](#reading-many-agents)),
and writes recheck the sampled identity. A stale sample, unavailable provider or unconfirmed commit is not
successful recovery; a successful read does not prove every check ran or
committed. Checks never
deliver input or permission answers, replay a submission, signal or restart
an agent. Commands keep their own preconditions. Nothing runs between calls
unless an operator [schedules the sweeps](maintenance.md#scheduling-the-sweeps).

`status`, `get`, `get-many`, `list`, `sweep` and `find-missing` can fail with
`ErrStoreBusy` and an empty result when one of the call's bounded store reads
does not finish in time: other work held the store or a lock the read needed,
or the read's time ran out. A failure while selecting rows means that selection
checked nothing. A later failure of `status`, `get`, `get-many` or `list` leaves
committed observations recorded.

## Reading many agents

To poll many agents, use `list` for their IDs, then one `get-many`:

```sh
agent-director get-many agent-a agent-b
```

On the CLI, each word after `get-many` is one ID, unchanged (commas and a
leading `-` included), except the global flags `--store-path`, `--home`,
`--tmux-command` and `--create-if-missing`, which the CLI takes anywhere,
with their values.

Each `spawns` element is exactly that ID's `get` result, with every field and
guarantee described for `get` in this contract. IDs with no record go to
`not_found`; an `ended` or `missing` row with a record is found. Both fields
are always arrays, empty rather than null, each in the order the IDs were given.
Any other read failure stops the call with both lists empty and no later ID
read; earlier reads' committed observations stay recorded.

Before reading any agent, `get-many` refuses a call with no IDs (`ErrInvalidFlags`),
then one with more IDs than [`[get_many] id_cap`](configuration.md#keys)
(`ErrTooManyIds`), then one with an empty or repeated ID (`ErrInvalidFlags`).
Empty and repeated IDs are named by their 1-based positions, never their text.
On `ErrTooManyIds`, split the IDs across calls of at most `err_details.id_cap` IDs.

The result is not a single snapshot: agents are read one after another, so
`family_state` can disagree between members of one family within a result.
A large result may reach an MCP caller's model as a file.

Call time grows with the number of IDs and with slow tmux calls. A tmux
server that answers nothing costs about one
[`[tmux] query_timeout_ms`](configuration.md#timing-settings-tmux) per server
under the [stop rule](#unchanged-panes), unless that timeout uses up the rest of an ID's
[`[tmux] sweep_budget_seconds`](configuration.md#timing-settings-tmux).
A stalled pane capture costs up to
[`[tmux] action_timeout_ms`](configuration.md#timing-settings-tmux) per working
Claude or Codex agent. There is no fixed call-time limit; raise TypeScript's
`callTimeoutMs` if needed (see the timeout rule above).

A binary without `get-many` refuses it with `ErrUnknownVerb` (CLI and
TypeScript), or `ErrUnknownTool` over MCP. On either refusal, fall back to
`get` per ID.

## Sweep results

Run `agent-director sweep` for one bounded all-agent pass. It selects all stored
rows once, sorted by `instance_id`. Each selected identity remains in `checks`,
including finished, changed, removed and unattempted rows; rows created later
are not added. A per-agent failure does not suppress independent agents.

The result has `checks`, `count`, `ids`, `unverified` and `unverified_ids`.
All three arrays are non-null, including on an empty store. Each `checks` entry
has these fields:

| Field | Meaning |
| --- | --- |
| `instance_id` | Selected row's identity. |
| `state` | Lifecycle state, using the configured settle time. |
| `displayed_state` | `idle_screen` when shown; otherwise omitted, never `needs_intelligence`. |
| `family_state` | `working` or `waiting`, from the consistent stored projection; freshness is unconfirmed. |
| `idle_screen_availability` | `available` for Claude/Codex, `unavailable` for Cursor. |
| `process_liveness` | `alive`, `exited` or `unknown`. |
| `confirmations` | Exactly the three five-key records described under [Confirmations](#confirmations). |
| `completion` | `complete` or `incomplete`, describing coverage, not confirmed facts. |
| `reason` | `null` for `complete`; otherwise `deadline`, `busy`, `unreadable`, `stale_observation`, `no_evidence`, `commit_unconfirmed`, `unsupported` or `unattempted`. |

`complete` means every eligible check was attempted or found inapplicable.
Failed observations or writes can still leave a complete entry with unknown or
`not_confirmed` fields. Skipped pane work and missing optional capabilities are
incomplete; an attempted unreadable capture can be complete. A protected pending
row can be complete while its evidence remains pending. Cancellation or budget
exhaustion leaves interrupted and remaining work incomplete with reason
`deadline`. Otherwise, an incomplete entry uses its last specific failed
check reason, or `unsupported` for an unavailable capability, or `unattempted`
for other work left undone.

Unattempted work keeps applicable stored evidence without new confirmation or
attempt timestamps. Actual failed attempts retain their metadata, matched to
the returned value and owner. Observation alone does not confirm a guarded
write committed. Cursor terminal projection remains conservative: a successful
deferred write need not yield freshly confirmed state in this response, and a
refused write does not retain speculative `missing`.

`count` and sorted `ids` count only applied missing marks. `unverified` and
sorted `unverified_ids` count eligible live rows left with a liveness note,
including an already-current note. Neither list includes Codex moves to `ended`,
failed or refused writes, rows changed before their guarded write, or live
pending rows noted `unreported`. These counters measure neither coverage nor
confirmation.

A failed initial selection or closed client is an error, not a successful empty
sweep. After selection, interrupted work remains in a returned result; a
terminated transport need not return one. A timeout proves neither rollback
nor completion. Results grant no input, answer, restart, reuse or deletion
authority. Follow the existing command guards and live-row sequence above;
[Maintenance](maintenance.md#scheduling-the-sweeps) owns cadence and the separate,
operator-owned `expire` schedule.

## Confirmations

`status`, `get`, every `list` row and every `sweep` check carry
`process_liveness` and `confirmations` on the Go, CLI, MCP and TypeScript
surfaces. They report evidence and authorize nothing.

`process_liveness` is `alive`, `exited` or `unknown`: the row's recorded agent
process at its recorded start time, never a gateway, tmux session or child
process. `alive` never confirms or refreshes `state`: a stopped turn still
recorded as `working` stays `working`. Cursor always reports `unknown`, reason
`unsupported`.

`confirmations` holds exactly three records, `state`, `process_liveness` and
`displayed_state`, each with these five keys, always present:

| Key | Meaning |
| --- | --- |
| `status` | `confirmed`, `not_confirmed`, `unknown` or `pending` (below). |
| `confirmed_at` | When evidence first confirmed this exact returned value in its current continuous stay for the row's current launch, RFC3339 UTC with millisecond precision; `null` when none is known. |
| `source` | That evidence's kind, `null` exactly when `confirmed_at` is: `agent_event`, `caller_command`, `process`, `tmux`, `pane`, `transcript` or `store` (a durable agent-director record or intent, never provider receipt). |
| `checked_at` | The latest attempt to confirm the field, an unsuccessful one included; `null` when none is known. Reading stored values never advances it. |
| `reason` | `null` when `confirmed`; otherwise `no_evidence`, `unreadable`, `busy`, `stale_observation`, `deadline`, `operation_pending`, `pending_grace`, `unsupported` or `commit_unconfirmed`. Errors keep their own detailed causes. |

- `confirmed`: evidence supported the value at `confirmed_at` and no later
  failed attempt remains unresolved. It is a fact at that instant, not proof
  that the value held since.
- `not_confirmed`: the latest attempt failed. `confirmed_at` and `source` keep
  the first applicable confirmation, if any, and `process_liveness` keeps the
  value it confirmed. Without an earlier confirmation, both are `null`.
  An old time never makes a failed attempt `confirmed`.
- `unknown`: no applicable evidence; `confirmed_at` and `source` are `null`.
  `checked_at` is also null when no attempt is known.
- `pending`: a still-owned operation (`operation_pending`) or a launch's
  pending grace (`pending_grace`) stopped the check. A later eligible event,
  command or check must supply evidence; elapsed time alone does not retry it.
  The lifecycle state `pending` is different: its admission by
  `spawn`, reuse or `resume` is itself confirmed (`caller_command`).

For all three records, later evidence of the same value and owner keeps
`confirmed_at`; a new check advances only `checked_at`. Stored reads,
settlement, age projection and passing time create no evidence or attempt.
Historical or malformed evidence that
cannot establish the exact value and owner leaves an unknown record.

What confirms each field:

- **State.** Only the event or command that set the value, or a check that
  established it independently (a gone process, a Codex turn's end found in
  its rollout). A record belongs to one value in one continuous stay of the
  current launch; a later stay, launch or value never inherits it.
  A same-value report keeps an existing record, so a long `working` turn
  keeps the time it was reported; where the value has no record yet (as on a
  row from before confirmations existed), the row's own same-value event may
  make its first one. Re-read hook or transcript data, settling and passing
  time add nothing. With a
  [settle time](configuration.md#settle-time-state), the record is the shown
  settled value's.
- **Process.** State reads and `sweep` check the recorded process of
  the live rows they select. Repeated successful probes of the same value
  and process identity keep its first confirmation time. A move to `ended`
  or `missing` proven by that process being gone
  records `exited` with the move. Any other terminal move (a session end, a
  tmux finding, a Cursor gateway loss) leaves `unknown`, so a finished row
  never reports its last `alive`. A check that cannot read the recorded start
  time, a pid-only identity included, is `unreadable`, never `exited`; a row
  with no recorded process is `no_evidence`.
- **Display.** A successful sample of the current launch's own pane confirms
  the display it establishes, with source `pane`: `idle_screen`, or no
  Displayed State (`displayed_state` omitted). The record belongs to that
  exact display episode, including an earlier episode still shown while
  entry or exit settles. A new episode or launch never inherits the old
  record. A successful sample of a different settling
  candidate leaves the shown record unchanged. A non-sampling settle-out
  attempt also leaves it unchanged, even if the write fails; a committed
  exit without a pane sample clears it to `unknown`/`no_evidence`.
  With no applicable display evidence or attempt, both times and source are null.
  Cursor always reports `unknown`/`unsupported`, with null evidence.

A failed pane sample records `not_confirmed`, its `checked_at` and reason:
`unreadable` for failed capture or ownership lookup, `deadline` when the
call's tmux budget is spent. Once committed, that attempt survives later
stored reads, retaining any applicable earlier confirmation. An `unreadable`
check of a process recorded with its pid and start time is also retained;
a pid-only check is response-only.

A prevented or uncommitted check (`busy`, `stale_observation`,
`commit_unconfirmed`, or a write's `deadline`) appears only in that call's
response, with its own `checked_at`. It claims no commit and applies only
while the returned value, owner and stored record still match the attempt;
concurrently committed newer evidence keeps its own record.

`state_changed_at`, `last_seen_at`, a display's `since`, request times and
`context.as_of` keep their own meanings; none of them, and no response time,
is a confirmation. `family_state` has no record (see
[Family reporting limits](#family-reporting-limits)), and nested permission
requests, subagent records, delivery receipts and errors carry none.
Commands still make their own ownership and safety checks: no confirmation
authorizes input, a permission answer or a kill, `delivered` keeps its
acknowledgement meaning, and a timeout still proves neither rollback,
non-delivery nor exit.

## Pending rows and startup screens

A pending row noted `unreported` has an alive agent with no applied hook
since launch. Read its pane, then answer with `allow_pending` only after a
person or an LLM judged that screen. Ordinary pending Claude fallback text
waits for the agent to be ready; see [Ordinary text submission](#ordinary-text-submission).
Such a Claude row that never reports in gets
`ErrSubmissionUnconfirmed` with `outcome: "not_started"` after that wait,
with nothing typed. Use `key` (including `key: "Enter"`) for startup choices,
or a Claude Enter-only send; these take neither the readiness wait nor the
hook check. For Codex Enter-only sends, see [Manual input ownership](#manual-input-ownership).

A pending Codex fallback text answer that starts no turn returns
`ErrSubmissionUnconfirmed` with `keys_sent: true` even when the screen accepted
it: read the pane and `status` instead of resending. A caller that cannot judge
the pane uses the live-row sequence or hands it to a human. Cursor admits no
prompt until its conversation is confirmed; `allow_pending` does not override
that rule. Hook configuration diagnosis is in
[Operator actions](operator-actions.md#a-row-stays-pending-and-the-trail-shows-pid_mismatch).

A first SessionStart can wait up to `pending_grace_seconds` (at most 540 s)
for the launch to record its pane; Claude's first response waits for it.
A caller whose store differs from the one its pane's hooks use (an alternate
`HOME`, `--home` or `--store-path` on a shared tmux server) gets no error:
the row stays `pending`, noted `unreported` while the agent runs, and goes
`missing` once it exits. A shared tmux server started with an alternate
`HOME` sends every later spawn's hooks to that store. Use a
[separate tmux server](permissions.md#one-spawn-under-another-home-or-store).

## Experimental context fullness

`status`, `get` and each visible `list-subagents` record include the same
`context` object on the Go, CLI, MCP and TypeScript surfaces. This feature is
experimental: its underlying agent internals have no published contract and
can change without notice. Its output shape has no stable-format guarantee;
changes to that shape do not count as breaking changes.

| Field | Available reading |
| --- | --- |
| `used_tokens` | Integer estimate of the last response's context occupancy, not cumulative billing usage. |
| `window_tokens` | Positive integer capacity applicable to that session's current model and runtime window settings. |
| `fraction` | `used_tokens / window_tokens`; multiply by 100 for a percentage. |
| `source` | `codex_jsonl_last_token_usage`, `claude_jsonl_usage_statusline_capacity` or `claude_jsonl_usage_subagent_statusline_capacity`, according to the qualified source. |
| `as_of` | Source measurement time as an RFC3339 UTC timestamp with millisecond precision, not the time of the read. |

Unknown readings retain every key:

```json
{"used_tokens":null,"window_tokens":null,"fraction":null,"source":null,"as_of":null}
```

Unknown never means zero. Missing or unreliable context evidence does not
fail an otherwise successful instance read. Context reporting uses structured
evidence only and never reads or interprets screen text.

Claude numeric support is limited to Claude Code **2.1.280** and the first-party
Anthropic configuration without provider, endpoint, window or custom capability
overrides. The resolved model must match between the native callback and
transcript; capacity comes from that model's native callback. The latest
ordinary response's transcript usage must pair with its applicable native status-line
capacity in the current live launch. Usage adds input, cache creation, cache
read and output tokens once each; it does not sum requests or iterations.
Automatic status-line observation preserves the selected user command's
input, output and exit status (which command is selected: see
[Settings](settings.md#what---settings-carries)). Unresolved settings,
including JSONC or ambiguous ancestor Git local settings, leave native
callbacks unchanged and context unknown. Unqualified configurations remain
unknown. With `[defaults] disable_context_fullness` on, a Claude launch's
context is unknown for it and its subagents (see
[Configuration](configuration.md#keys)).

Codex numeric support is limited to Codex CLI **0.159.2**, the **OpenAI** provider,
and resolved sequential rollout history from a fresh session whose startup
agent-director admitted and whose original writer is still running. Existing,
resumed or forked sessions, ended writers and sessions with unproven origin
return unknown, even when their rollout names a tested version. Usage must
belong to the inspected session's current turn/model and match its reported
runtime capacity. This does not establish support for every provider or
configuration.

Managed Cursor returns all five fields as null in this build, including
**2026.10.01-e373342** and **2026.10.12-d47ca18**. Both versions' ACP mode
emits no ordinary usage-bearing completion events; hook configuration cannot
supply the missing producer. Native interactive context evidence exists for
both. A native `preCompact` executor is reachable through ACP shared resources,
but automatic managed compaction delivery remains unqualified.

Captured checkpoint token counts can survive compaction without a usage
measurement time or generation binding. The latest checkpoint therefore
cannot prove that no compaction occurred after its last usage update or
supply a trustworthy `as_of`. Managed Cursor context remains unknown until
upstream ACP supplies qualified usage evidence.
Other untested or unrecognised versions return unknown.

Each call reads current evidence on demand, without caching or a wall-clock
age cutoff. An old measurement can remain valid while its context is current.
Compaction and source-specific changes to the active response or context
invalidate an older estimate until later qualified usage and capacity appear.
Missing, malformed, incomplete or regressing records, mismatched
session/model/capacity, and unresolved
history, forks or rewinds produce unknown instead of an older reading.

Already-visible Claude subagents can report a reading under the same version
and configuration limits, using their own resolved model, transcript response
and native subagent status-line capacity. Progress counters are not occupancy. Other
visible children return unknown. A parent's usage or capacity never supplies
a child's reading. Context reporting adds no subagent discovery or tracking.
Callers own any policy for compaction, replacement or unknown readings.

## Family reporting limits

Use [`family_state`](usage.md#family-state) as a report of currently stored
family activity. `waiting` means neither task success nor permission or
readiness to take an action. With [settling](configuration.md#settle-time-state),
a family change in either direction, including to `waiting` after the last
working member stops, shows only after it has held for `settle_ms` (up to
60 s); until then the previous settled value shows.

Family freshness is unconfirmed: the value is a consistent stored projection,
not evidence that every member was checked. It has no
[confirmation](#confirmations) record, and the row's own confirmed records
say nothing about its descendants. A stopped agent still recorded
as `working` keeps the raw family working until an existing detector or
state change records otherwise. A hook payload over 1 MiB is dropped whole,
so its state or subagent report is lost; a missing report is not proof that
work ended. Cursor's existing per-agent read projection
can show `missing` before its terminal write succeeds; family reporting
continues to use stored states until that write commits.

Inspect each agent's own state and requests for permission handling and
follow each verb's existing preconditions. An ancestor's family value
does not forward a descendant's permission details. Existing read failures
remain errors; never treat a failed read as `waiting` or invent a third
family value. Reading a family value adds no wait for settling or writer
lock acquisition.

## A Codex turn that ends in an error

Codex sends no hook when a turn ends in an error, such as a model at
capacity. The row reads `working` until a check finds the turn's end in
Codex's rollout: a `status` or `get` of that row, a `get-many` naming it,
a `list` selecting it, or `sweep` can move it to `waiting` (shown after any
[settle time](configuration.md#settle-time-state)). Keep polling `status` as for
any turn. `waiting` does not mean the turn succeeded: read the pane for the
error before sending more work. The row's `state_changed_at` is the time of
that recovery write, not of the turn's end.

A turn Codex starts without a `UserPromptSubmit` hook leaves the row in the
previous turn's state (`waiting` if it ended) until its own `Stop` or
Interrupt; no read ends it.

That move proves none of the turn's permission requests gone. On a relay-on
row that had a request in that turn, a plain `send-keys` and `pause` can
then be refused with `ErrDialogMaybeOpen`: have a person or an LLM read the
pane and send with that read's `pane_sha256` as `--expect-pane-sha256` (to
stop the agent, send `/exit` that way), as in
[Permission relay](#permission-relay). The rule and its limits are in
[The Codex turn](hooks.md#the-codex-turn).

## State change time

`get` and each `list` row carry `state_changed_at`, on the Go, CLI, MCP and
TypeScript surfaces: when the row's raw state last changed, as an RFC3339 UTC
string with millisecond precision (`2026-10-20T14:03:07.123Z`). The fraction
drops trailing zeros, so it has 0 to 3 digits and none on a whole second:
parse the value as a time before comparing, and never compare the strings.
Every row of every agent has it, `pending` rows included; it is never `null`.
`status` does not carry it.

- **Changed since T.** To learn whether a row's state changed since a time of
  your own, compare `state_changed_at` with it. A hook or command that changes
  the raw state moves it; a write of the same state and a hook that only
  refreshes `last_seen_at` leave it. Never use `last_seen_at` for this: it has
  whole-second precision and moves on every applied hook.
- **Raw, not settled.** With a [settle time](configuration.md#settle-time-state)
  set, `state` may still show the earlier settled value, and a change that
  does not hold for the settle time can move `state_changed_at` without ever
  showing in `state`.
- **Not `since`.** The `since` of a row's
  [`displayed_state`](#needs-intelligence-and-idle-screen-rows)
  dates the display, not a state change: the display never moves
  `state_changed_at`, but a raw change that ends it (the agent moving on, or
  a recovery repair) does.
- **Not evidence.** It dates the raw change only. When evidence first
  confirmed the `state` shown in its current stay is `confirmations.state.confirmed_at` (see
  [Confirmations](#confirmations)).
- **Epoch.** `1970-01-01T00:00:00Z` means the raw state has not changed since
  agent-director began recording this time. It is not the time of a change,
  and reads as "no change since" any time you compare it with.
- **Cursor.** A Cursor row a read finds lost can show `missing` before
  `state_changed_at` moves; see
  [Cursor operation boundaries](#cursor-operation-boundaries).

## Needs-intelligence and idle-screen rows

`state` on `status`, `get` and every `list` row is always the row's
lifecycle state (`pending`, `waiting`, `working`, `ask_user`,
`check_permission`, `ended` or `missing`), settled as the
[settle time](configuration.md#settle-time-state) says. A row can also show
a Displayed State, a report beside its state, in `displayed_state`:

- `idle_screen`: a `working` Claude or Codex agent's pane has looked the same
  on successive checks for [its timeout](configuration.md#idle-screen-timeout-state);
  `state` stays `working`. The agent may be hung or working quietly: look at
  its pane before acting. See [Unchanged panes](#unchanged-panes).
- `needs_intelligence`: an unknown prompt on the agent's screen, one that is
  not a permission request agent-director knows about. The name is defined,
  but nothing detects such prompts yet, so no row shows it. A permission
  request never does: its row stays `check_permission` however long the
  request waits.

Test `displayed_state`, never `state`, for `idle_screen` or
`needs_intelligence`: `state` never holds either. `displayed_state` needs
agent-director 0.17.1 or later. A `needs_intelligence` display already in
the store is never reported; the first check that commits an observation of
the row ends it, with one exit trail record.

Every `status`, `get` and `list` row carries `idle_screen_availability`:
`available` for Claude and Codex, `unavailable` for Cursor, which never shows
`idle_screen`. It is a capability outcome, not a state. Why Cursor never
shows `idle_screen` is in
[architecture](architecture.md#cursor-and-idle_screen).

A Displayed State is a report only: it changes no refusal, hold or guard, and
`send-keys`, `decide` and `pause` judge the row's live state. A `waiting`
live member counts as `working` in [`family_state`](usage.md#family-state)
only while it shows `idle_screen`.

- **Fields.** `status`, `get` and `list` carry `displayed_state`. `status`
  and `get` also carry `since` (RFC3339 UTC with exactly millisecond
  precision) and, on `idle_screen`, `unchanged_minutes`: the whole minutes
  (rounded down) the pane had stayed the same at the last committed observation.
  `request_token` and `tool_name` belong to `needs_intelligence` only.
  `status` adds `tmux_session_name` only while `displayed_state` is present
  (`get` carries it always). A `list` row carries neither `since` nor the
  detail; call `get` for them.
- **Omissions.** A field that does not apply is absent: never `null`, empty
  or zero. Test for presence, not for a value. The
  [`confirmations.displayed_state` record](#confirmations) is always present,
  even when the display is omitted or unavailable; its nullable keys remain.
- **Filtering.** `list --state` selects by stored `state` or
  `displayed_state` before `--limit` and recovery checks. `--state working`
  and `--state idle_screen` both match a stored working row showing
  `idle_screen`. Returned membership follows
  [fixed selection](#bounded-recovery-on-state-reads).
  `--state needs_intelligence` is accepted and matches only a row showing it.
- **Detection happens on checks.** Nothing runs between your calls and
  nothing changes on a timer. With settling off, a row shows `idle_screen`
  from the first selected check after the timeout that commits the
  observation. Your usual `status` polling is enough; you need not read
  panes yourself. `sweep` also samples eligible rows.
  With a positive [settle time](configuration.md#settle-time-state), entry or
  exit starts its interval at the check that commits its candidate, not at
  the raw-state change. A later check must commit the change once that
  candidate has held for the interval. Until entry commits,
  `displayed_state` is absent.
- **`since`.** The time of the check that committed the entry, not the moment
  the timeout ran out. It is kept while the row clears under settling and
  when `unchanged_minutes` grows. A new working turn never inherits the
  earlier `since` or detail: if it qualifies too, that display ends at once
  and the new turn starts fresh.
- **Observation writes.** A check writes changed observations and evidence
  together. Even when the display, candidate and whole-minute detail are
  unchanged, a new pane attempt can update its confirmation record alone.
  Writes never wait for the store's lock. An unconfirmed observation returns
  the earlier committed display and detail, with any applicable
  [failed attempt](#confirmations). A later eligible check can try again.
- **Clearing.** When its pane changes or cannot be sampled, or the agent
  moves on, the row keeps `displayed_state` until the exit candidate settles
  and a check commits the exit. `ended`, `missing` and a state a command sets
  show at once. A shown `idle_screen` also ends at once on any Codex turn
  start or ownership change, including a `waiting` row retaining it.
- **Trail.** Each entry and exit appends one
  `ad.spawn.displayed_state_transition` trail record with `instance_id`,
  `prior_state`, `new_state`, `event` and `ts`; the trail is audit only and a
  failed append changes nothing. Rarely, under heavy store load, a check whose
  write is slow to commit may still show the previous display and detail and
  append no record for that change; a later read shows the committed
  display, with `since` from the earlier check.
- **Attaching.** A Displayed State adds no verb and no automatic answer. For
  `idle_screen`, [attach to its pane](usage.md#attach-to-a-spawns-pane).

### Unchanged panes

`status`, `get`, `get-many`, `list` and `sweep` sample eligible selected `working`
Claude or Codex rows: the agent's own pane of the current launch, fingerprinted
as the `pane_sha256` a default `read-pane` returns. Only the fingerprint is
kept; nothing reads what the pane says.

- **The unchanged period.** At least two successful matching captures of the
  current launch's own pane must span the effective timeout. The first starts
  the period; matching captures keep its start. A different fingerprint
  restarts it and begins clearing a shown `idle_screen` under settling.
  `check_permission`, `ask_user` and every other state never qualify.
  An intervening turn, launch or ownership change invalidates the baseline.
- **Only during checks.** Nothing samples between calls. The period starts
  at the first check that saw the current pane, so infrequent checks delay
  detection. Identical samples prove neither continuous inactivity between
  them nor agent exit.
- **A hint, not proof.** Healthy quiet work, such as a long tool with no
  output, can qualify. Look before acting, and give such work a longer
  [timeout](configuration.md#idle-screen-timeout-state), configured or per
  spawn.
- **Failed samples.** A sample that fails or cannot be trusted (the session
  gone or not this launch's, the pane not found, tmux not answering) resets
  the period: an entry not yet shown is dropped, and a shown row clears after
  the settle time. It never fails the read or other rows. The next
  successful sample starts a fresh period. A read that
  cannot commit skips its sample, a reset included, so a later read may
  continue the committed period.
- **Time.** A sample makes up to three tmux calls (a lookup, a pane listing
  and a capture), each bounded by
  [`query_timeout_ms` or `action_timeout_ms`](configuration.md#timing-settings-tmux).
  Within one call, a tmux server that does not answer a lookup or pane listing
  is not asked again unless that timeout uses up the rest of an ID's
  [`[tmux] sweep_budget_seconds`](configuration.md#timing-settings-tmux): its later rows count as failed
  samples for baseline handling, but their skipped work makes sweep coverage incomplete.

## Ordinary text submission

A nonempty `text` with Enter, without `key`, `request_token` or
`expect_pane_sha256`, uses native ordinary delivery where the launch supports
it. CR bytes are removed; LF newlines are preserved. Native success returns
`outcome: "submitted"`,
`request_id`, `session_id`, `record_sha256` and, when supplied by the native
record, `record_id`: an exact new user record in this launch's native
transcript proves the normalized text was submitted. Hook activity, a quiet input queue and pane text do not prove it.
This proves submission, not completion of the agent's turn.

Native delivery supports Linux with exactly Codex 0.159.2 or Claude Code
2.1.280, on fresh launches with an ordinary empty composer. For new input, the row must be
`waiting`, or `pending` with `allow_pending`. A resumed conversation,
positional startup prompt, alternate mode or unsupported agent argument is
outside this contract. Accepted extra arguments are `--model`/`-m`,
`--permission-mode`, `--effort` (each with a value), `--no-alt-screen` and
`--dangerously-skip-permissions`. The launch's keybinding requirements and
generated settings are in [Ordinary delivery launch settings](settings.md#ordinary-delivery-launch-settings).
When native capability is unavailable, including other versions, platforms,
resumed conversations or unsupported launch arguments, ordinary Codex input
uses one paste followed by one Enter; other agents retain literal text plus
Enter. Paste framing requires the application to have enabled bracketed paste;
it does not establish composer readiness.

For ordinary Claude fallback input read as `pending`, `send-keys` waits for the
agent's terminal to take raw input, SessionStart to move the same launch to
`waiting`, and 1.5 s from when the call first observes `waiting`. The row is
polled throughout and must remain `waiting` once observed. After the settle,
the call rechecks raw input, the row, ownership of the tmux server, session and
pane, and permission-relay and dialog guards. Readiness and revalidation share
one 12 s deadline, capped by the call's earlier deadline; an in-flight check
can make the call return later. For a resumed row, the wait also accepts a
SessionStart that keeps or rotates the saved session ID, under the same launch
and process guards; `session_id` can change.
An unavailable terminal-mode capability, a failed readiness read or check,
cancellation or deadline expiry during readiness or admission, or changed
launch or authority at admission returns `ErrSubmissionUnconfirmed` with `outcome: "not_started"`:
nothing was typed, and `reason` names the unmet condition. Admission's state
and relay/dialog guards keep their own refusal errors, also with nothing typed.
Claude fallback sends read in any state other than `pending`, Enter-only,
`no_enter`, `key` and pane-hash sends do not take this readiness path.

After a successful transport, pending Codex fallback and the pending Claude
path above are observed for up to 2 s for hook-derived `working`, `ask_user`
or `check_permission` state on the same launch. Success returns `{}`: a turn
was seen to start, without exact transcript confirmation. If that activity
is not observed, the call returns
`ErrSubmissionUnconfirmed` with `keys_sent: true`,
`submission_confirmed: false` and `row_state`, without an `outcome`.
For these refusals and failed hook observations, `row_state` is the last state
the call read successfully, even when that state caused the refusal.
Missing hooks can leave submission unknown even when input reached the agent.
Other fallback success returns `{}` without this hook confirmation; Cursor's
verdict is described below.

On macOS, Claude fallback text longer than 512 bytes after CR removal is typed
in pieces of at most 512 bytes, each followed by a 100 ms pause, with Enter
500 ms after the last pause. This includes `no_enter`, `expect_pane_sha256`
and `pending` sends; only a `pending` send with Enter and no hash is typed after
the readiness wait above. Shorter text, keys, pane answers and Enter-only sends
are typed in one call. The pauses add about 0.1 s per 512 bytes plus 0.5 s
before the Enter; the checks before each later piece and the Enter add to that,
and any readiness wait and hook observation come on top, so set the client
timeout to match. Text over 32 KiB returns `ErrSubmissionUnconfirmed` with
`outcome: "not_started"`, `phase: "pre_input"` and `keys_sent: false`, before
any input: put the prompt in a file the agent can read and send a short
instruction naming that file. Send text over 512 bytes to one row one call at
a time: a second call returns `not_started` with nothing typed while the first
owns the row's input (re-issue it once that call settles), and on a row with no
launch token the pieces of concurrent calls can interleave in its input.

Before each piece after the first, `no_enter` included, and before that Enter,
the call rereads the row, reruns the state, permission-relay and dialog guards,
and checks that the launch, session and the agent's pane and process are
unchanged. It stops there, typing nothing more and sending no Enter, when the
call was cancelled or reached its deadline, the launch, session, agent pane or
process changed, the row cannot be read, a guard refuses, or the row moved into
`check_permission` or `ask_user` during the call; a send that began in one of
those states is not stopped for that alone. Other state changes of a working
row, such as its hook updates or its turn ending, do not stop the send. A
cancellation or deadline before the first piece returns `not_started` with
nothing typed. A stop, a timed-out first piece or a failed later piece returns
`ErrSubmissionUnconfirmed` with `outcome: "partial_input_unconfirmed"`,
`phase: "text_attempted"`, `keys_sent: true` and
`enter_may_have_been_sent: false`. `bytes_attempted` counts the pieces
attempted: a failed piece counts, the piece a stop skipped does not.
`row_state` is the reread row's state when its launch, state or a guard stopped
the call, otherwise the state the call observed before typing. Part or all of
the text may be in the input, unsubmitted: read the pane before any further
input.

Codex fallback accepts literal TAB and LF inside the paste. Codex may trim
trailing whitespace, including TAB, before submission. It refuses invalid
UTF-8, other control characters after CR removal (including ESC, NUL and C1),
and a final whitespace-delimited token beginning with `$`. Trailing horizontal
whitespace still counts; LF, U+2028 or U+2029 after the token ends that match. Earlier
dollar tokens and dollars embedded in another token are not refused by this
rule. These refusals return `ErrSubmissionUnconfirmed` with
`outcome: "not_started"`, `phase: "pre_input"`, zero `bytes_attempted`,
`keys_sent: false` and `enter_may_have_been_sent: false`. Put the prompt in a
file the agent can read and send a short instruction naming that file.
These restrictions apply to ordinary Codex fallback, not the separate manual
operations below.

State, permission-relay and dialog guards run before capability selection or
stored-request observation. `ended` and `missing` rows are always refused
with `ErrSpawnNotInteractive`, including retries of existing requests.

Claude's local configuration checks are best effort: they do not verify the
effective `editorMode` or managed or remote policy. Automatic Claude input
remains enabled when those checks pass, but Vim settings or policy may still
remap or consume input. For a supported native attempt, if Enter is attempted
and no exact matching transcript record appears, agent-director reports
`ErrSubmissionUnconfirmed` with
`outcome: "submission_unconfirmed"`. It makes no submission claim without
that proof and never retries text or Enter.

For native delivery, text must be valid UTF-8, at most 1 MiB after CR removal,
with non-whitespace content. Apart from LF, control characters and Unicode
line/paragraph
separators are refused. A first non-whitespace `/` or `!` requires an
operator-controlled pane operation. Readiness is bounded by 12 s; input
delivery by 60 s, followed by up to 12 s for transcript confirmation. Set a
client timeout long enough to receive the result. Timeout or cancellation does
not prove non-delivery and cannot undo attempted input.

For native delivery, supply `delivery_request_id` to identify one attempt
within a launch. Repeat the same ID and normalized text to retrieve stored
confirmation or observe
late proof without replaying input. Reusing an ID for different text or a
different pane/process identity is refused. Use a fresh ID for an intentional
identical turn. If omitted, an immediate repeat of the most recent confirmed
identical text returns its existing result; this is not a general deduplication
of every earlier turn. Observing an existing request requires the state,
permission-relay and dialog guards to pass, and its exact launch and native
session identity to match. This observation sends no input. An unresolved
native attempt or unreadable/corrupt stored evidence blocks new input even with
a new ID or when capability becomes unavailable; fallback never resends it. Request IDs
do not give fallback sends native confirmation or deduplication. If a different
request discovers late proof for its predecessor,
it reports `not_started`; re-issue the new request only after the predecessor
has settled.

Native delivery, Claude fallback refusals before input, macOS paced Claude
typing, Codex fallback refusals or paste-transport failures, and Cursor gateway
refusals use these `ErrSubmissionUnconfirmed` details:

| Field | Meaning |
|---|---|
| `outcome` | `not_started`: this request attempted no input; `partial_input_unconfirmed`: text or manual input may be present, with no automatic Enter attempted; `submission_unconfirmed`: Enter may have been sent and submission remains unknown; `gateway_refused`: Cursor's gateway discarded the submission before it reached Cursor (see below). |
| `request_id`, `phase` | The owned native attempt, when allocated, and the last delivery phase. An earlier unresolved native attempt can be reported instead of the requested new input. Fallback sends have no request ID. |
| `bytes_attempted`, `enter_may_have_been_sent`, `keys_sent` | Conservative input-attempt facts, not byte receipts. `keys_sent` means input was attempted and may have reached the pane. |
| `last_queue` | Optional last terminal queue sample; zero is not submission evidence. |
| `submission_confirmed`, `row_state`, `reason` | Confirmation is false; state and reason describe the observation. Only `gateway_refused` establishes that Cursor did not receive the submission. |

A fallback paste-transport failure counts the whole offered payload as
attempted, even when delivery may not have begun. It reports
`partial_input_unconfirmed` when Enter is known unattempted, otherwise
`submission_unconfirmed`; neither proves which bytes arrived.

A `not_started` request can be re-issued after its stated condition clears.
For partial or unconfirmed input, never automatically repeat text, Enter or
editing keys: input may still submit later. For a native attempt, re-issue the
same request only to observe proof while the guards allow it; if it remains
unresolved, escalate to a human using
[Unresolved ordinary input](operator-actions.md#unresolved-ordinary-input).
For fallback uncertainty, check hook-derived `get` or `status` without
resending; if still unknown, escalate through the same procedure.
Agent-director never retries text or Enter on missing evidence, including
after a crash.

Operational input authority is in the configured SQLite database. Persisted
intent records what may have been attempted, not provider receipt. An uncertain
commit or released writer lock proves neither delivery nor non-delivery.
Only exact retained transcript proof confirms submission.

If cleanup fails after native submission or matching-request observation,
`ErrInternal` carries the unchanged submitted result in `err_details`, including
its exact proof fields. The Go call also returns that same result. This is
still confirmed submission: never resend because cleanup failed. An existing
primary error keeps its classification and details, with the cleanup cause
retained.

### Cursor input outcomes

Cursor `send-keys` with text and Enter, or empty text (Enter only), pane-hash
sends included, waits up to 2 s for the gateway's verdict. `key` sends, `Enter`
included, are not checked. A refusal attributed to this call returns
`ErrSubmissionUnconfirmed` with `outcome: "gateway_refused"`: nothing reached
Cursor and the gateway discarded the text. Its details include
`phase: "enter_attempted"`, `keys_sent: true`, `enter_may_have_been_sent: true`,
`submission_confirmed: false`, `bytes_attempted`, the gateway's `reason` and
`row_state`.

On `gateway_refused`, do not automatically repeat text or Enter. Refused
input is not queued and does not replace accepted work. If `reason` is
`capacity`, wait for capacity, then explicitly submit the refused text again.
Otherwise, read the pane and check `get`'s `state` and `unproven_requests`;
answer or clear what holds input before explicitly sending again.

A Cursor request of the current launch already answered (`delivered` or at
the pane) holds input until tool proof arrives, its turn ends, or the launch
ends ([Cursor spawns](permissions.md#cursor-spawns)). After turn end releases
that hold, the next prompt needs no kill and resume. A later `gateway_refused`
still means an admission hold remains; inspect it as above. The separate
undelivered-request relay hold can still block input (see
[Permission relay](#permission-relay)).

If turn-end proof refuses an answer before it is written, the pane prints
`Cursor turn ended before its answer was written; nothing sent`.
No answer bytes were written and the gateway remains usable. An answer
already issued may still be written; turn-end proof preserves its records.

An accepted or provisionally held submission still returns `{}`. So does a
call with no attributable verdict in the window or an unavailable transcript.
Success means keys were sent, not that the prompt was admitted or delivered.
Literal text and Enter are separate operations; concurrent callers have no
atomic submission guarantee. When admission is uncertain, inspect the pane,
`get` and the [protected transcript](usage.md#cursor-transcripts) without
blindly resending. Minimum gateway version: see [Upgrade](install.md#upgrade).

## Manual input ownership

Empty text (Enter only), `no_enter`, `key`, pane-hash sends and permission
pane answers remain separate manual operations. On success they return an empty
object and make no transcript-confirmation claim. Cursor sends with Enter also
check for [gateway refusal](#cursor-input-outcomes). They serialize with ordinary
delivery. Manual `send-keys` can return `ErrSubmissionUnconfirmed`
with `outcome: "not_started"`, `keys_sent: false` and
`enter_may_have_been_sent: false` when exclusive input ownership is unavailable
or its delivery journal cannot be read or saved. That call attempted no input;
re-issue it after the stated condition clears. If another request still owns
the writer, let it settle first; a persistent journal refusal needs an operator.
For `pause`, the same ownership or journal failures return `ErrInternal`
before any input is attempted. If its ownership recheck fails after it clears
the input line, `pause` returns `ErrInternal` with only that clear sent: no
`/exit` and no Enter, so the agent is not pausing. Check `get`'s `state`
before calling `pause` again. State, relay and recorded-name guards run
before it acquires input ownership.

After Enter-only or pane-hash text sends with Enter to a `pending` Codex row,
bounded hook observation can instead return `ErrSubmissionUnconfirmed` with
`keys_sent: true`, `submission_confirmed: false` and `row_state`, without a
native `outcome`. Check hook-derived `get` or `status` without resending text
or Enter. If submission remains unknown, use
[Unresolved ordinary input](operator-actions.md#unresolved-ordinary-input).

On macOS, Claude `no_enter` and pane-hash text sends take the paced typing,
size limit and outcomes in [Ordinary text submission](#ordinary-text-submission).

Manual input, including `pause`, changes the native composer contract and prevents
further new native delivery on that launch. A pane without native capability
retains its fallback input path after manual input. A state, permission or pane refusal
from `pause` does not change that contract. Manual input does not clear an
unresolved attempt. Direct terminal writers must also be excluded during an
automatic request; agent-director's lock cannot serialize them.

Cleanup failure after manual, permission-pane or pause input does not erase
what was attempted. Its details retain actual byte/key and Enter facts;
a key-only send does not imply an added Enter. Follow those facts and the
primary error rather than treating cleanup failure as proof that nothing was
sent. Manual interference remains recorded and does not clear native uncertainty.

## Transcript recovery

For Claude and Codex, `resume` continues only the row's current session with its recorded agent.
Cursor is covered by [Cursor operation boundaries](#cursor-operation-boundaries).
It never selects an older session from `prior_sessions`, even if its file
exists. No current `session_id` gives `ErrNoSessionId` before transcript
lookup. Otherwise, Claude tries the current `jsonl_path` and, after any stat
failure or when no path is recorded, recomputes the current session's path
from the row's cwd and config directory. Codex tries only its current recorded
path. The refusals write and launch nothing.

Claude recomputes under the row's saved absolute `CLAUDE_CONFIG_DIR`, else
`~/.claude`; a relative, `~`-prefixed or blank selection is not used, so
only a still-valid recorded `jsonl_path` finds that transcript. A row saved
with no selection resumes with pre-trust on `~/.claude.json`, even when the
pane's environment selects another directory, so the agent may stop at
Claude Code's folder-trust prompt. Answering it in the pane continues the
conversation; a fresh spawn or reuse does not.

Before that lookup, for every agent, `resume` checks that the row's saved cwd
is still a directory agent-director can enter, as `spawn` checks its `cwd`: a
path that is gone, or a directory that the user agent-director runs as cannot
enter (no search permission on it, such as mode 000, or on a parent), gives
`ErrCwdNotFound`, and one that is now a file `ErrCwdNotADirectory`. Nothing is
written or launched. Recreate the directory at that path, or for a permission
failure make it enterable by that user, and resume again; the conversation
continues. Or, for Claude and Codex, spawn the same id with `reuse_finished`,
the row's agent and an existing `cwd`, which starts a fresh conversation. A
Cursor row follows the error's fresh-recovery advice instead.

When the current candidates fail, `ErrJsonlNeverWritten` means the recorded
path is empty and visible current-life history is empty. An archived entry
for the current session itself does not count as visible history. Otherwise
the refusal is `ErrJsonlMissing`: its tries contain only current-session paths
with source `persisted` or `fallback`, never `history`. A Codex row with no
current path and visible history gets `ErrJsonlMissing` with zero tries and
an explanation that no transcript path is recorded for the current session.

Same-agent reuse retains earlier sessions. If its new session records no
transcript, `get` can report `transcript_status: rotated` and `resume` can
return `ErrJsonlMissing`, even if an earlier transcript exists. `rotated`
means visible history exists, not that resume will recover it. The
never-written condition remains the empty-path and empty-history rule.

For a Claude or Codex row with `ErrNoSessionId`, `ErrJsonlNeverWritten` or `ErrJsonlMissing`, spawn the
same id again with `reuse_finished` (`--reuse-finished` on the CLI) and the
row's agent (`get` shows it). This starts a fresh conversation with no memory
of the old conversation, which cannot be resumed through agent-director
afterwards. Same-agent reuse keeps inspectable history and records; a switch
starts a new life, including when switching back.

Reuse of any agent uses the same launch outcome rules. A determinate create
or label failure attempts a conditional restore; this includes a label failure
whose newly created session was ended or may still be running.
`ErrTmuxNotAvailable` or `ErrTmuxSessionCreate` reports the launch failure and
whether the row was restored. `ErrTmuxSessionConflict` reports ownership or
pane conflicts; a duplicate-name re-lookup can instead return
`ErrTmuxUnresponsive`. Follow its exact row and survivor outcome. A restored
row is not proof that a surviving session stopped; human inspection follows
[Operator actions](operator-actions.md#inspect-and-end-a-session-by-hand).

If restoration applies, the previous agent and life become current again,
with their saved configuration, latest identity and history. The attempted
reuse's archive remains. Deleted permission requests stay deleted, and
records closed by the attempt stay closed. A changed or removed row is never
overwritten; a failed restore can leave it `pending`. A launch timeout or
indeterminate nonzero create reply returns `ErrTmuxUnresponsive` without
restoring. A lost create reply can return success without identity and does
not restore. Check the row and surviving work before any retry. For Cursor,
cleanup and process death do not create per-request proof; delivery alone
does not establish execution or dialog closure.

## Cursor operation boundaries

Runtime compatibility is checked during asynchronous initialization. The exact
JSON keys `protocolVersion` and `agentCapabilities` must contain integer `1`
and a non-null object. Optional capabilities may be omitted; unknown extensions
remain opaque. A minimal capability object permits fresh text sessions.
`loadSession: true` is required only for load. Accepted initialization does not
prove that creating or loading a session, selecting a requested mode or submitting
a prompt succeeded; each operation needs its own authoritative response.

When `inject_help_hook` is enabled, each fresh, latest-loaded or replacement
conversation additionally requires its own completed guidance MCP initialization
before publication or prompt admission. A registration, candidate identity or
previous conversation's acknowledgement cannot satisfy that requirement.
Guidance setup shares the existing 45-second startup/load operation bounds;
rotation shares the first control's fixed three-second bound. Waiting for guidance
never renews a deadline. Transient pane-query timeouts or interrupted replies may
be checked again within that same bound; they grant no admission authority.
Unresolved failure, cancellation, authority loss or expiry stops admission;
`allow_pending` bypasses none of these checks. Transport initialization
does not establish native model consumption. See
[guidance delivery](hooks.md#what-agent-director-help-actually-outputs).

Cursor shell CLI access requires the sandbox to permit the AD executable,
selected store and tmux socket. Codex's sandbox settings in
[caller setup](usage.md#drive-a-spawn-from-another-agent) do not establish Cursor
shell reachability. Native Cursor shell reachability and Cursor-originated tool
use remain unverified; the supported managed MCP setup uses explicit ACP stdio
routing, without relying on ambient environment inheritance or a policy bypass.

Recreating an ordinary external CLI/MCP caller leaves the managed gateway,
runtime and current conversation running. Replacing the actual MCP process
configured for a Cursor conversation is a separate operation: while its launch
binding remains current, it can initialize again from the same registration and
make useful tool calls without restarting the gateway or runtime, changing the
conversation or resetting its history. After the original setup deadline, a
replacement requires a nonempty current protected runtime session matching the
current row session; `pending` alone does not disqualify a confirmed session.
Each ownership check has its own fixed allowance of
`2 × (query timeout + pipe-close wait)` (3.2 seconds at defaults). Transient query timeouts
or interrupted replies may be checked again within that allowance; retries never
reset it. Initial ownership and completed initialization are checked independently,
so waiting between them does not consume the later check's allowance. A setup
that starts before its deadline keeps that original deadline throughout.
Replacement verification cannot acknowledge a later setup attempt. Retired launch
registrations do not authorize a new launch. This is distinct from `pause` and `resume`, which
end and relaunch the managed runtime. Independently launched AD children retain
their own rows and lifetimes. Native Cursor process replacement and continuation
remain unverified.

`resume` accepts only `instance_id` (`--instance-id` on the CLI) for an
`ended` or `missing` Cursor row. It loads only the latest durably reported
session with its saved configuration; see [Pause and resume](usage.md#pause-and-resume).
Live and pending rows return `ErrSpawnNotResumable`. No saved session returns
`ErrNoSessionId`. A saved cwd that is gone, cannot be entered or is no longer
a directory returns `ErrCwdNotFound` or `ErrCwdNotADirectory` (see
[Transcript recovery](#transcript-recovery)). Missing, unreadable or insecure current protected transcript
returns `ErrJsonlMissing` before row or launch changes. Older sessions are
never candidates, even when their native storage or transcripts exist.

Native conversation availability is checked by Cursor during asynchronous
load; `resume` does not wait for it. Unsupported load, a missing native session,
invalid response, timeout or transport failure never selects an older session
or starts a new conversation. Inspect the pane, row and `ad.cursor.failed`
reason; a successful resume result confirms launch, not successful load.
Invalid protected-file continuity also fails in the gateway; no missing or
damaged transcript is reconstructed from replay or replaced with an empty file.
Load can leave the row `pending` until a real first prompt. Explicit
`allow_pending` (`--allow-pending` on the CLI) permits that first prompt only
after current binding verification and every durable dialog hold; it bypasses
neither. Requests of an earlier launch hold nothing (see
[Cursor spawns](permissions.md#cursor-spawns)). Replayed updates cannot
establish readiness, new requests, decisions, choices or execution proof, and
no transient input is recovered.

Cursor supports same-agent finished-id reuse and switches through the
[life and history rules](usage.md#naming-the-agent). Reuse starts fresh and
never loads a saved session. Its failure outcomes follow
[Transcript recovery](#transcript-recovery). If the latest session cannot be
resumed and you want to preserve the original row, session, transcript and
saved artifacts, reconcile any surviving launch first: use
`agent-director get --instance-id <id>` and, while the row is live, follow
`agent-director kill --instance-id <id>` and the live-row sequence above.
Missing state or a finished-row kill no-op is not proof of exit. If public
operations cannot resolve survivors, STOP and ask a human to follow
[Inspect and end a session by hand](operator-actions.md#inspect-and-end-a-session-by-hand)
before any fresh launch. Only when the intended account and configuration can
be preserved, run this from the intended project directory, omitting
`instance_id` so AD generates a unique id:

```sh
agent-director spawn --agent cursor --cwd "$PWD"
```

Before running, add caller-known supported `extra_env` (repeat
`--extra-env KEY=VALUE` on the CLI) or `--template NAME` carrying the intended
account and configuration. No selection is valid only for deliberately intended
personal login without custom configuration/data. Cursor retains the caller's
`HOME`; [Cursor directories](multi-account.md#cursor-configuration-and-data-directories)
select configuration/data without relocating or isolating login credentials.
For saved `ErrReservedEnvKey`, resume cannot change the saved environment.
Preserve the original row, session, transcript and saved artifacts. A new
instance is an option only with a supported selection matching the intended
account/configuration; never remove account intent and retry under the personal
login. If intent is unavailable or reconstructing it requires protected data,
STOP and ask a human. Do not read, export or edit the protected saved environment
to reconstruct a launch, or repair the original row in place.

Cursor `agent_args` accept the model, mode and single-prompt forms in
[Fresh Cursor use](usage.md#fresh-cursor-use). Unsupported or malformed
arguments return `ErrSpawnDeniedFlag` before writing or launching;
`make-template` applies the same check before writing a Cursor template.
Remove the named argument and any owned value, then retry. Conflicting modes,
unknown aliases, route-changing arguments and ordinary force/yolo/sandbox/trust
flags are refused. No compatible additional permission flag is supported.
agent-director cannot reliably detect Cursor repairing a malformed native
policy or silently falling back from it.
Cursor permission-list refusals are in [Permissions](permissions.md#cursor-spawns);
argument replacement and empty-list semantics are in
[Settings](settings.md#the-agent-and-its-arguments).

A prompt must be one final argument, or exactly one argument after an
argument-list `--` (a second `--` on the CLI). An earlier operand followed by
a trailing `--` is refused. To protect flag-like or command-like prompt text,
place that single prompt after the argument-list `--`.

An accepted mode argument does not confirm runtime configuration. Each fresh,
loaded or rotated conversation must advertise the requested mode and confirm
its selection before publication, readiness or prompt submission. Missing mode
state, an unavailable requested mode, a rejected or invalid acknowledgement,
timeout or lost authority fails asynchronously, with no automatic fallback.
Inspect the pane, row and `ad.cursor.failed` reason as for other gateway failures.
Only an advertised unavailable mode or a structured unsupported-operation error
(`-32601`) while mode selection is pending can offer mode-removal advice.
Authentication-required, unknown, internal, malformed, format, cancellation and
missing-resource failures do not establish that dropping mode remedies them.
If deliberately choosing to launch without that unavailable or unsupported mode,
remove the complete mode group (`--plan`, or `--mode` and its value), then use
the fresh generated-id procedure above, retaining intended configuration, to preserve
the failed row. This deliberately chooses the runtime's default mode; it does not
establish that the requested mode worked. A successful `spawn` or `resume`
result confirms launch only. Model availability is likewise not established
by launch success.

Cursor session selectors are `--resume [chatId]`, `--continue`, command-position
`resume` as the first non-option word, and `--new-session-id` with its required
value. Plain spawn and same-agent reuse refuse them. Only reuse with an
explicit id and a successfully read finished (`ended` or `missing`) row whose
agent differs from the resolved target removes recognized selectors and their
owned values. The surviving arguments are checked as for a fresh spawn.
Removed original tokens are reported in `dropped_args` in argv order; the
field is omitted when empty. Missing, live, unreadable or same-agent rows
grant no selector-removal authority. Resume filters recognized selectors and
their owned values from a copy of saved `agent_args`, leaving the stored list intact.
Values owned by model/mode options and prompt operands after `--` remain values or
prompt text even when they spell a selector; no `-r` or `-c` alias is assumed.
Normal input ordering and limits are in
[Fresh Cursor use](usage.md#fresh-cursor-use); `send-keys` results and recovery
are in [Cursor input outcomes](#cursor-input-outcomes).

Only confirmed store writes establish durable input or exact tool-proof
evidence. These records are not a recovery queue. A still-live accepted input
may finish its own prerequisites under the same launch, conversation and
decision guards. Gateway death, reopening the
store or transcript, and observational reads cannot replay it or a permission
response. An unconfirmed commit remains unknown; a missing record does not
prove that input was never sent. A committed issue record, relay acknowledgement,
completed transport write, provider execution and dialog proof are separate
facts. Permanent storage failure, lost evidence or a lost completion wake can
prevent progress; acceptance promises no unconditional completion.

During rotation, a provisional holding-capacity refusal requires a
later explicit submission after capacity is available; refused text will not
be delivered automatically. Wait for publication and remaining decision holds
to resolve before expecting held input to be admitted. A rotation notice or
fresh-identity confirmation is not evidence of admission or prompt completion.

The first control has a fixed three-second cancellation deadline, including
fresh-session creation, publication and protected recording. With help enabled,
this bound also includes the new conversation's guidance initialization.
Repeated controls cannot extend it. Cancellation timeout, missing, invalid or reused identity,
refused publication, lost authority or persistence failure stops admission and
visibly refuses held input to both conversations. Interrupting a pending
rotation also stops the owned runtime. Do not send refused input to the old
conversation or automatically replay it. Inspect `get` and available protected
records, then use the existing live-row sequence above if cleanup is needed;
start a fresh unique instance once the failed row is reconciled.

Publication and hook completion are distinct. A report failure or timeout can
occur after the fresh session and required outgoing history have committed.
Do not infer rollback from that failure, or treat an incomplete record as proof
that publication failed. Inspect the actual current session and history before
deciding what happened; AD does not restore the old conversation automatically.

After interruption, inspect each affected submission's cancellation or outcome.
When the gateway invites a new prompt and decision holds are resolved, submit
new input explicitly. Known retirement before any prompt bytes are written
can leave the same conversation available for that input. Cancelled work is
never automatically replayed; partial or uncertain writes stop the transport
and are never retried.

For shutdown, retain the working-state and dialog guards on `pause`; it is
available only on an eligible waiting row. Local `/exit` or eligible `pause`
cancels affected queued work and ends the row only after owned runtime
termination is confirmed. A queued prompt is not a reason to bypass a pause
refusal. Existing held-agent actions are in
[Permission relay](#permission-relay).
Exit or eligible pause can preempt rotation, but `ended` still requires actual
owned runtime death and a verified, durably recorded end for the authoritative
conversation. Unknown binding or exhausted persistence cannot establish success.

A slow pane can omit rendered output and show
`Cursor pane output omitted under pressure; inspect the protected transcript`.
Once physical output resumes, pending loss is reported even without another
prompt or output update. A permanently blocked writer cannot display the notice.
Inspect the protected transcript at `get`'s `jsonl_path` for recorded submission
outcomes. Display loss supplies no readiness, permission or termination evidence.

`spawn` returns before the runtime confirms startup. Missing programs,
unsupported platforms, failed lifetime protection or unavailable ownership
evidence cannot produce a ready report; a launch may remain `pending` until
a recovery check (a state read or `sweep`) confirms gateway loss.
Runtime/protocol/reporting failures emit `ad.cursor.failed` with a reason when
the gateway has identified its row.
Examples include an ACP operation timeout, unsupported ACP protocol,
changed runtime/session binding, a refused report or transport failure.
Each tool proof keeps the original `[store] busy_timeout_ms` window from its
first persistence attempt; retries never renew it. Failure to confirm storage
fails the gateway without establishing either persistence or rollback. The
actual event determines what must finish before the next lifecycle step:

| Event | Required proof persistence and limit |
|---|---|
| Ctrl-C interruption | The completed turn proves its requests gone, answered or not, except those with pending or stored tool proof. Deferred tool proofs are stored before its Stop report; idle-control recovery also stores them before reporting waiting. The first control's three-second deadline bounds this work. |
| `/clear` or `/new` | A completed outgoing turn first proves its requests gone, with the same tool-proof exceptions. Deferred tool proofs must be stored before rotation publication, within the same first-control deadline; failure refuses rotation. |
| Ordinary turn completion | Turn-end proof has its own fixed store-busy window; deferred tool proofs keep theirs and must be stored before the Stop report. |
| Waiting report | Startup and turn/idle-control Stop reports store their captured deferred proofs first, within each proof's remaining window and any earlier cancellation deadline. |
| `/exit`, eligible `pause` or terminating signal | Before terminating the runtime, try to store deferred proofs through their remaining original windows. This can outlast the cancellation deadline: a confirmed proof then survives, but intentional end cannot be confirmed and the row becomes `missing`, not `ended`. Storage failure remains failure. |

A lifecycle flush retains one immediate attempt even when its captured window
has already expired; it gains no retry time. Fatal cleanup is best effort within
the first control's remaining deadline, or three seconds from failure when no
control is pending. If the runtime is already gone, each proof gets one attempt
without busy retries; death supplies no missing authority or proof.
The prompt's 30-minute response phase pauses while admitted requests remain
open and restarts when an actual completion releases the last one. This is not
a fixed total-turn limit; it never renews the first control's deadline.
These are asynchronous launch failures, not new typed spawn errors.

Failure reasons identify the initiating operation (`initialize`, `session/new`,
`session/load`, `session/set_mode` or `session/prompt`) with bounded, sanitized
text. Structured ACP errors require exact `code` (an integer in the signed
32-bit range) and `message` (a string) keys. Only the numeric code supplies the
cause: unsupported operation, rejected format or parameters, cancellation,
authentication required or missing resource. Malformed errors and unknown
codes do not establish a cause; runtime internal errors also leave the cause
unknown. Authentication-looking messages, stderr and exit alone never establish
an authentication or configuration diagnosis. Raw runtime message/data,
environment, paths and credentials are not included in these failure reasons.
Process-start reasons separately distinguish unavailable executable or cwd,
operating-system permission denial and an unknown start failure. Human checks
are in [Operator actions](operator-actions.md#inspect-and-end-a-session-by-hand).

A successful `spawn` or `resume` return, including `pending`, confirms launch
acceptance only. It does not confirm initialization, load, requested mode or
prompt success. There is no automatic alternate runtime, account, session or
mode selection and no automatic replay of uncertain input.

Tool and unknown updates retain their content in the protected transcript
without treating it as main-agent text. Only main-agent text updates render as
conversation text. Display content alone grants no permission, execution or
completion authority; validated tool execution retains its separate proof checks. Invalid update schemas and
session mismatches are distinct failures; accepting tool content does not relax
session or authorization checks.

Pipe EOF is a transport failure, not proof of child death. On fatal gateway
failure, cleanup stops and joins the owned runtime and independently confirms
its death before marking the current launch `missing` and closing its requests.
This is not an intentional session end or a successful turn. Cleanup errors
remain failures. Local `/exit` and eligible `pause` can report termination only
after the owned runtime is confirmed gone; a stop failure cannot report success.

For a selected live Cursor row, the
[shared checks](#bounded-recovery-on-state-reads) judge its recorded gateway
process identity alone and never look it up in tmux. A reused PID with a
different start time means the recorded gateway is gone. Only that exact verdict
ends the row: a confirmed lost gateway is reported as `missing` without waiting
for `sweep`, pending grace, a live launch owner or the state settle
delay, even if its runtime is still alive.
Alive or unknown evidence leaves its lifecycle state unchanged; unknown evidence
leaves the row unverified with `liveness_note` `probe_eacces`. A Cursor row with
no recorded gateway is checked like any other agent's row.
The result is guarded by the examined launch and runtime binding. A gateway
lost before runtime registration can also be reported missing while no binding
exists for that instance; a stale binding is not absence. A replacement launch
retains its own state. An unresolved same-launch conflict fails direct `get`
or `status`. In `list`, stale proof for one row retains that row's older
coherent stored facts and leaves independent selected results intact; those
older facts are not fresh liveness proof.

The complete read result is assembled before a separate attempt to persist
terminal closure. That write does not wait for the store's write lock and keeps
its fixed one-second allowance, capped by the caller's remaining budget. A busy
store or deadline reached by this write alone can still return the guarded
`missing` result while the caller's context remains valid. Other write errors,
including failed request closure, read errors and caller cancellation or deadline
exhaustion return an error with empty results. There is no automatic retry.
A successful read does not confirm whether terminal closure committed.

Only state is projected: `ended_at`, `state_changed_at` and other timestamps
remain the stored facts read, so the first missing result may omit `ended_at`
and show the earlier `state_changed_at` even if closure subsequently commits.
Until closure commits, the projected `missing` reads
`confirmations.state.status` `pending` (`operation_pending`).
Terminal permission lists are empty; they prove no request gone, decision,
receipt or delivery. Terminal closure leaves Cursor request proof
unchanged, including after `delivered`; see
[Cursor permission lifetimes](permissions.md#cursor-spawns).
Finished Cursor rows receive no gateway check. State reads do not check outside
their selected IDs; `sweep` selects all agents.

A Cursor `missing` row is not proof that its runtime has exited. Resolve any
surviving work through the existing ownership and
[human inspection procedures](operator-actions.md#inspect-and-end-a-session-by-hand)
before a relaunch, then resume the latest saved session if available or use
the explicit fresh-instance procedure above. Never replay an
uncertain submission automatically.

Unknown process evidence authorizes neither adoption nor a kill. Existing
ownership errors and survivor limits still apply. Human inspection follows
[Operator actions](operator-actions.md#inspect-and-end-a-session-by-hand).

A recorded Cursor `jsonl_path` names the
[gateway transcript](usage.md#cursor-transcripts), not a native session
resume artifact. `transcript_status: present` means a path was recorded;
it does not prove the file is still readable. Before a confirmed startup,
there may be no session or reference. Failed creation, an insecure or
replaced path, failed writes or failed sync stop further prompt admission
and produce a visible failure; AD does not invent a saved reference or
claim unrecorded history. Inspect whatever protected records remain available;
queued work will not be recovered. Do not treat a partial transcript as complete
history, successful input execution or confirmed termination, and do not replay
uncertain work. A persistence failure cannot produce a successful intentional
end report.

## When pause cannot confirm exit

For Claude, Codex and Cursor, `ErrPauseTimeout` means `/exit` and Enter
were sent, but the row was not observed to reach `ended` within
`pause.timeout_seconds`. Its `err_details` object contains:

| Field | Meaning |
|---|---|
| `inspection_required: true` | A caller able to inspect the session must decide before further input. |
| `exit_keys_sent: true` | tmux accepted the exit keys; this does not prove the agent consumed them. |
| `exit_confirmed: false` | The wait did not confirm `ended`. |
| `row_state` | The last raw row state observed, without the configured settle delay. |

This does not establish why exit is delayed or prove that a prompt is open.
An exit confirmation, including Claude's background-work confirmation, is
one possible cause. Agent-director does not interpret the pane or answer it.

Read `get` and `read-pane`. A caller unable to inspect the session and
decide must pass the error and its details up to one that can, before
sending any more input. That caller may choose `send-keys` with the
inspected pane's `pane_sha256` as `--expect-pane-sha256`, or choose `kill`.
Existing input ownership and permission holds still apply. Do not choose
keys or force termination automatically from the timeout alone.

Observe the resulting row state before relaunching; a timeout does not
authorize `resume`. A Codex agent that exits after inspection follows
[When a Codex agent exits](#when-a-codex-agent-exits).

## When a Codex agent exits

A Claude row's clean exit ends the row at once. A Codex agent that exits other
than through `pause`, an `/exit` typed at its pane, leaves its row in its last
state, and so does a crash or a `kill` of either agent. Until something sees
the agent gone, `send-keys` gets `ErrTmuxSendKeys` and `read-pane`
`ErrTmuxCaptureFailed`. Run `sweep`: it marks the row `ended` when the
agent's end was reported, else `missing`, and an `ended` row is in neither of
its lists (see [How a Codex row ends](hooks.md#how-a-codex-row-ends)). Selected
state reads can make the same guarded move through
[shared recovery](#bounded-recovery-on-state-reads).

`pause` on a Codex row ends it itself:

- Its agent exits after the `/exit` and `pause` returns success with the row
  `ended`.
- A live row whose agent already exited with an end report, in any live state,
  returns success having sent nothing, and the row is `ended`.
- A gone agent with no end report, or one that cannot be judged, gets
  `ErrTmuxSendKeys` for a waiting row's keys, or `ErrPauseTimeout`.
  [Shared recovery](#bounded-recovery-on-state-reads) then settles the row.

If a live Codex row has a draft exceeding 64 lines, `pause` submits the
rest of the draft and the `/exit` to the agent as a prompt, the row goes
`working`, and `pause` returns `ErrPauseTimeout`. Follow the
[inspection procedure](#when-pause-cannot-confirm-exit) before further input.

## Spawn and template input refusals

Each of these is refused before anything is written: no row, session, trust
entry or template. Fix the input and re-issue the call. How `agent` and a
template's `agent_args` combine is in
[Settings](settings.md#templates-and-per-call-merge).

For effective `cursor`, `ErrReservedEnvKey` also refuses explicit
`CURSOR_API_KEY` or `AGENT_CLI_CREDENTIAL_STORE` by presence, even empty or
incomplete, because the credential profile is unreleased. It also refuses
invalid explicit `CURSOR_CONFIG_DIR` or `CURSOR_DATA_DIR`; the exact directory
requirements are in [Multi-Account](multi-account.md#cursor-configuration-and-data-directories).
Existing `HOME`, reserved `AGENT_DIRECTOR_*` and malformed-key checks run first,
in that order; Cursor then checks `CURSOR_API_KEY`,
`AGENT_CLI_CREDENTIAL_STORE`, `CURSOR_CONFIG_DIR`, `CURSOR_DATA_DIR` in order.
Cursor-specific diagnostics name the key, never its value. Agent-independent
templates defer these checks until the effective agent is resolved.

Correct invalid directory values to the intended literal supported paths;
keep `HOME` unchanged. Do not remove credential-profile intent to obtain a
personal-login launch. If that intended account cannot be selected by supported
means, stop and retain the refusal for an operator decision. Fresh/reuse calls
and Cursor template creation refuse before row, trust, template or launch
changes and before transporting credentials. Resume checks saved selection
after its existing HOME/malformed-key guards, before transcript lookup or
lifecycle changes; it preserves historical saved AD variables. Resume cannot
repair that selection; follow the [fresh-instance boundary](#cursor-operation-boundaries).

- `ErrAgentMissing`: the call gives no `agent` and its template names none.
  Name `claude`, `codex` or `cursor` per call (`--agent` on the CLI), or put `agent` in
  the template.
- `ErrAgentInvalid`: the agent is not exactly `claude`, `codex` or `cursor`; the value is
  case-sensitive. Pass one of the three.
- `ErrAgentArgsMismatch`: the template's `agent_args` are for another agent
  than the one this spawn resolves to, so they are not inherited. Pass this
  call's own non-empty `agent_args`, or spawn through a template for that agent.
- `ErrTemplateAgentMissing`: a template holds `agent_args` but names no agent,
  at `make-template` or when a `spawn` loads it. Give the template an `agent`,
  or remove its `agent_args`.
- `ErrTemplateMalformed`: a template sets the arguments key under its old
  name, holds a reserved key (`template`, `instance_id`, `tmux_session_name`
  or `reuse_finished`), or sets one key under names differing only in letter
  case. Use `agent_args`, remove the reserved key and keep one spelling.
  `make-template` refuses those reserved keys with `ErrInvalidFlags`.
- `ErrReservedEnvKey`: the merged template and call `extra_env` sets `HOME`
  (any value), an `AGENT_DIRECTOR_*` key, or a malformed name (empty, or
  containing `=` or NUL); `make-template` checks its own `extra_env` the
  same way. Remove the key; select another account as
  [Multi-Account](multi-account.md) says.
- `ErrPermissionsUnsupported`: a `codex` or `cursor` spawn or template carries an `allow`,
  `deny` or `ask` entry. Drop the lists; see
  [Permissions](permissions.md#codex-spawns) or
  [Cursor permissions](permissions.md#cursor-spawns).
- `ErrSpawnDeniedFlag`: the `agent_args` hold an argument the supervisor must
  own. Remove it and re-issue; the error names the argument.
  - A `claude` spawn always refuses `--settings`, `--output-format` and
    print mode: `--print`, `-p`, or a short cluster parsed as containing `-p`.
    Print is checked on the full original list before any selector is removed:
    `-pc` is refused even on a switching reuse.
  - Claude session selectors are `--resume` (`-r`, optional session),
    `--continue` (`-c`), `--fork-session`, `--from-pr` (optional value),
    `--teleport` (optional session), `--resume-session-at`,
    `--resume-drops-turn`, `--rewind-files` (each with a required value),
    `--reply-on-resume`, `--cloud` and `--remote` (optional value), and
    `--attach-serve` (required value). `--session-id` names a new session
    and is not a selector. Selectors and their owned values are refused on a
    plain spawn, reuse with no row, same-agent reuse, or a reuse whose row is
    live or could not be read. The error names the bare option: `-r` for
    `-rS1`, for example.
    Only a reuse with an explicit id and a successfully read finished
    (`ended` or `missing`) row whose stored agent differs from the resolved
    agent removes selectors from the launch and the stored arguments.
    The remainder is checked as for a plain spawn; the agent starts fresh
    and resumes nothing.
  - When removing an actual Claude `--cloud` or `--remote` leaves no parsed
    `--environment` or `--pool` with its required value, also removed are
    `--forward-home-settings`, `--ref` and `--on-branch`, with their owned
    values. They follow the same refusal policy on other spawn paths.
    With surviving environment/pool context, those controls remain; without
    actual cloud/remote removal, no such cleanup occurs. A spelling owned as
    another option's value or occurring as prompt text supplies no context.
  - Claude required scalar and list options own even a dash-prefixed first
    value; optional values consume a following non-option word. A value is
    not an option: `--model -c` and `--append-system-prompt "-concise answers"`
    pass. An exact denied long name or its `=` form is matched wherever it
    stands. When another option owns that token as a value, it is refused
    even on a switching reuse: `--append-system-prompt --resume foo`
    refuses `--resume`, and an owned `--resume=S1` is refused too. Such
    option-owned tokens are never dropped independently.
    Short clusters follow Claude's option/value rules: `-rS1` selects a
    session, `-cd` contains continue, `-dc` is debug value `c`, and `-nrocket`
    is name value `rocket`. Exact registered short options take precedence
    over splitting a cluster.
  - A `codex` spawn: a Codex subcommand or its alias as the first word
    before any `--` that is neither an option nor an option's value (`exec`,
    `resume`, `fork`, `login` and the rest). On a verified finished-agent
    switch, command-position `resume` or `fork`, their selector options
    (`--last`, `--all`, and `--include-non-interactive` for resume only), and
    the selected session id are removed. With `--last`, the first operand
    is the prompt rather than an id. Original prompt tokens survive behind
    `--` and the remainder is checked as for a plain spawn.
    Exact denied long names owned as another option's value always refuse,
    including on a switch. Other subcommands remain refused.
    The `--` meant is one inside
    `agent_args`; on the CLI that is a second `--`. A word after it is the
    prompt and passes. `-i` or `--image` takes every word up to the next
    option, so `-i a.png exec` passes (`exec` is a value) and `--image=a exec`
    is refused (the `=` form takes one value, so `exec` is in command
    position). `--remote` and
    `--remote-auth-token-env`; `-c` or `--config` with a key at, above or
    under `hooks`, `tui.disable_paste_burst`, `features.hooks` or its alias
    `features.codex_hooks`; and `--disable hooks` or `--disable codex_hooks`
    (Codex's alias of `hooks`). While `disable_ask_user` is on, also
    `--enable default_mode_request_user_input` and `-c` keys at, above or under
    `features.default_mode_request_user_input` or
    `tools.experimental_request_user_input` (see
    [Permissions](permissions.md#disable_ask_user-config)). Every spelling of
    a value is read (the next word, after `=`, or attached to a short flag),
    and an option is refused even after the `--`. A repeat of a flag the
    supervisor adds is not refused or removed; see
    [Codex's layers](settings.md#codexs-layers).
  - `resume` does not apply spawn's argument refusals. It filters recognized
    session selectors and Claude print mode from stored arguments as described
    in [Pause and resume](usage.md#pause-and-resume).
    Argument recognition covers the verified agent options. An option added by
    a later agent version is treated as having no value until recognized;
    its value ownership or session-selection behavior may therefore be missed.
    Do not rely on resume filtering or switching-spawn removal to protect
    against such options.

On a successful switching spawn, `dropped_args` reports the removed original
argument tokens in their original order, including owned values and a
partially removed cluster's original spelling. It is omitted from CLI/MCP
JSON and TypeScript results when empty; Go exposes `SpawnResult.DroppedArgs`.
The CLI prints exactly one stderr notice when the list is non-empty, naming
those tokens and saying the switch starts fresh and resumes nothing. Stdout
keeps the usual result envelope. Surviving arguments retain their meaning;
only necessary prompt placement behind options and `--`, or altered-cluster
re-encoding, changes their order or spelling. When an original recognized
required scalar or list option lacks its required value, only literal
filtering applies and the agent's parser rejects incomplete input.

Not refused: a relative, `~`-prefixed or blank `CLAUDE_CONFIG_DIR` or
`CODEX_HOME` reaches the agent unchanged, but `pre_trust` reads `failed`
with a warning and the agent may stop at its folder-trust prompt. Read the
pane and answer it, or relaunch with an absolute path.

## Permission relay

For Cursor, use the same request token, decision and delivery fields.
`hook_alive` is null and no per-request hook process is claimed; the fixed
`confirm_by` ends uncertainty independently of gateway liveness. Its precise
bounds and once-only approval mapping are in
[Cursor permissions](permissions.md#cursor-spawns).

A recorded verdict or historical closeout is not permission to emit a response.
Closure, proof and outside-answer records cannot create that permission.
A response claim is recorded before output; if its commit or output is uncertain,
do not replay it. A hook's late or uncertain acknowledgement commit can leave
`delivered` with no response output; silence does not imply `fallen_back`.
Neither a claim nor durable acknowledgement proves transport completion, agent
receipt, execution or dialog closure.

For Claude and Codex, a relay hook that fails before recording an identifiable
request denies it. One that cannot read the event or dies before recording,
or a binary that cannot run, gives no relay answer and leaves the agent's
native prompt. A prompt with no recorded request holds no input, so a plain
`send-keys` can answer it. Claude shows its native dialog while the relay
hook runs; that dialog is not evidence that the hook died.

Every Cursor relay reply, including timeout denial, requires its own durable
acknowledgement. Failed or missing acknowledgement means no relay answer is
sent to Cursor. Separately validated cancellation or policy rejection is a
distinct outcome. `delivered` can remain recorded after a crash without Cursor
receipt; it is not execution or dialog-closure proof. Never resend an uncertain
approval as though known absent. See [Cursor permissions](permissions.md#cursor-spawns)
for delivery, proof and request lifetime rules.

For fallback, inspect a connected current menu and make a fresh deliberate
choice for that request only while it is not closed or proven gone and no
response is claimed; an unacknowledged recorded verdict does not prevent it.
`fallen_back` alone does not guarantee an answerable
native request. Do not reuse a choice from before the relay deadline, or retry
an uncertain answer. Terminal cancellation or native timeout cannot revive a
request. On disconnect, follow [Cursor operation boundaries](#cursor-operation-boundaries)
and retain ownership checks. An outside answer record sends nothing and
supplies neither ACP approval authority nor proof.

- A relayed request's `decision` is the verdict recorded, not the outcome:
  read `delivery`. Pass `decide` a `--max-wait-ms` of at most your deadline
  minus 1 s (see [Intercept permission prompts](usage.md#intercept-permission-prompts)).
- After `decide` returns `delivery` `not_confirmed`, poll `get-permission`
  until `delivery` reads `delivered` or `fallen_back`, which it does by
  `confirm_by`.
- `get`, `get-many`, `list` and `get-permission` never wait for the store's write lock.
- With a [settle time](configuration.md#settle-time-state) set, `status`,
  `get`, `get-many` and `list` show the settled state with no lateness marker;
  [`displayed_state` settling](#needs-intelligence-and-idle-screen-rows)
  depends on successful observations. Verbs act on the live state, so a verb
  can refuse, with a named error, on a state `status` does not show yet.
- Some errors carry their facts in an `err_details` object beside
  `err_name` and `err_description` (in the CLI's error JSON, the MCP
  error's `data`, and the TypeScript client's `errDetails`):
  `ErrRelayFallenBack`, `ErrPaneAnswerInProgress`, `ErrClaimTooSoon`,
  `ErrPaneChanged`, `ErrDialogMaybeOpen`, `ErrTooManyIds`,
  [`ErrPauseTimeout`](#when-pause-cannot-confirm-exit), and an `ErrInternal`
  from a pane answer whose key was sent (`key_sent` true: read the pane before you
  send anything again). Read facts from it, never from the description; a
  caller that does not use it ignores it.

| Error | `err_details` |
|---|---|
| `ErrRelayFallenBack` | The request's `get-permission` fields; `state`; `open_requests`, the agent's other requests awaiting an answer, oldest first, each with `request_token`, `tool_name`, `requested_at`, `delivery`, `hook_alive` and `pane_answer` (`[]` for none, `null` when unreadable). |
| `ErrDialogMaybeOpen` | The oldest request holding the dialog, with its `get-permission` fields; `state`; `unproven_requests`, the agent's other such requests, oldest first, with the same fields. |
| `ErrPaneAnswerInProgress` | `request_token`, `pane_as`, `pane_intent_at`, `sender_alive` (`true`, or `null` when uncheckable) and `not_before`. |
| `ErrPaneChanged` | `n_lines` and the `request_token` named, if any. |
| `ErrClaimTooSoon` | `hook_alive`, `hook_gone_at` and `not_before`. |

`decide` records no verdict when it refuses with:

- `ErrRelayFallenBack`: the request's relay hook is gone and took no verdict,
  and no pane answer is recorded on it through agent-director, so no answer
  from the relay reached the agent. It comes within seconds of the hook's
  end (after `confirm_by` when agent-director cannot check the hook's
  process). Answer the request at the pane, or record that it was answered
  outside agent-director (see
  [Answer a request at the pane](usage.md#answer-a-request-at-the-pane)); the
  refused verdict is kept as `attempted_decision`.
- `ErrStoreBusy`: the bound ran out before the verdict could commit.
  This can include store acquisition or SQL work; it does not establish
  that another process or call held the store. Retry only as a new,
  deliberate call with a fresh budget. Without `--max-wait-ms`, a write
  lock not taken within `[store] busy_timeout_ms` is `ErrInternal`
  instead; a negative bound is `ErrInvalidFlags`.
- `ErrAlreadyDecided`: a verdict is already recorded (first `decide` wins, and
  the relay hook records a deny of its own when its time runs out,
  `decision_reason` `timeout`), or the request was answered at the pane.
- `ErrNoOpenPermissionRequest`: no eligible request by that token, including
  a closed, proven-gone or obsolete request. Do not answer it at the pane.
  A request still awaiting an answer
  when its agent ended, or when
  [shared recovery](#bounded-recovery-on-state-reads) marked it `missing`, is
  closed, and stays closed after `resume`. A decided request without a
  relay acknowledgement or completed pane answer is also closed when its
  agent ends or is marked missing; its recorded verdict is preserved.
  On a closed request, `decide` gets `ErrAlreadyDecided` only for a verdict
  the relay acknowledged before closure, a completed pane answer or a
  `find_missing` deny; an `ended` deny or an unacknowledged verdict gets
  `ErrNoOpenPermissionRequest`.

The positive `--max-wait-ms` bound covers store acquisition, SQL, cleanup
and reporting under the same original deadline. Expiry prevents new SQL
from being started by the operation, even if the store is free. Already
entered database work can continue while the caller returns; its connection
stays unavailable until safe disposal completes. Zero permits immediately
available work within a fixed one-second allowance, with no lock retries;
it does not mean zero execution time. These bounds depend on the host
scheduling AD and are not hard real-time guarantees.

Keep the one-second reply margin above. The CLI uses up to 500 ms of that
margin after the verb returns to drain accepted audit events. This does not
extend the verb's SQL or reporting deadline. Failed or pending audit shutdown
prints a stderr diagnostic without changing the verdict's exit code. Pending
events can be lost when the process exits, including a normal exit after
the drain times out. Read the structured result/error separately from stderr
notices; neither a successful decision nor process exit confirms audit
durability.

An `ErrInternal` reporting an unconfirmed commit is different from a
precommit refusal: the verdict may already be recorded. The diagnostic marker
`branch=commit_exec` means COMMIT execution returned an error;
`branch=operation_guard` means the caller returned before the operation result
was published after COMMIT started. Neither marker proves rollback or that
COMMIT did not happen. These markers are advice text, not error classifications.
Inspect the durable request with `get-permission` and its `request_token`
before deciding whether to retry. Do not infer that nothing was written, resend an uncertain approval,
or automatically replay the call. Cancellation does not undo an already
committed verdict. After a confirmed commit, later deadline exhaustion keeps
the verdict and returns the available delivery facts; follow `not_confirmed`
as above. A decision or delivery response is not a receipt that its audit
events have reached durable storage.

A pane answer (`send-keys --request-token`) sends nothing when it refuses
with:

- `ErrPaneChanged`: the pane changed since you read it. Read it again; the
  error does not carry the new hash. Volatile lines, such as a spinner or a
  clock, can cause it.
- `ErrPaneAnswerInProgress`: another pane answer to this request is still
  being sent, or its sender cannot be checked and `not_before` has not
  passed. Do nothing. A sender that died leaves `pane_answer` `intent`, its
  key possibly typed: read the pane again, then answer with the fresh hash
  or use `record-pane-answer`.
- `ErrStoreBusy`: the store's write lock was not taken in time.
- `ErrSendKeysWhileRelayed`: a relay hook of the agent may still answer
  its request: answer the request it names with `decide`, or retry later (a
  plain retry can then get `ErrDialogMaybeOpen`, below). Don't decide one
  `get-permission` shows proven gone `turn_end`; see the Cursor turn-end
  paragraph below.
- `ErrAlreadyDecided` or `ErrNoOpenPermissionRequest`: the request is no
  longer open.

`record-pane-answer` returns `ErrClaimTooSoon` while the relay hook may still
answer or has not been gone for 2 s yet, so the prompt may not be drawn:
retry at `err_details.not_before` (null only while the hook is seen running).
Its other refusals record nothing: `ErrPermissionRequestNotFound` (no request
has the token), `ErrNoOpenPermissionRequest` (closed with its spawn),
`ErrAlreadyDecided` (confirmed, or already closed at the pane),
`ErrPaneAnswerInProgress`, `ErrPaneChanged`, `ErrStoreBusy` and
`read-pane`'s tmux errors.

On relay-on rows, while a request is fallen back with no pane answer recorded, a
plain `send-keys` (no `--request-token`) is refused with
`ErrRelayFallenBack`, whose `err_details` name that request and the agent's
other open requests: close it with a pane answer or with
`record-pane-answer`. While a relay hook of the agent may still answer,
every `send-keys` is refused with `ErrSendKeysWhileRelayed`.

On Codex, `Escape` while an undecided main-agent request's relay hook polls
ends the hook: the command does not run and no dialog follows, but the
request falls back and stays open. Close it with `record-pane-answer --as deny`
and an inspected read's hash once the hook has been gone 2 s.

A decided request recorded before schema v7 with `proven_gone_how`
`agent_gone` no longer holds plain `send-keys`, even when its reported
`delivery` still reads `not_confirmed` or `fallen_back`. Recovery is
automatic in an already-upgraded store and preserves the recorded verdict
and delivery facts. A pane answer by that obsolete token is refused with
`ErrAlreadyDecided` for a `find_missing` deny, otherwise
`ErrNoOpenPermissionRequest`; a completed pane answer keeps its own refusal.
Other requests of the agent can still hold keys.

With `--allow-pending`, `ErrSendKeysWhileRelayed` can still hold a row
before SessionStart for minutes: an undecided request recorded before v7,
a legacy request without `agent_gone` proof (including an earlier-launch
request carrying only `turn_end` proof and no recorded close), or a current
relay hook that is live or whose status cannot be checked. Legacy holds
can last until `created_at` plus the configured relay window plus 2 s
(24 h + 2 s at the default). A decided legacy request with `agent_gone`
proof contributes no hold.

A request with no recorded settle instant takes no new verdict. Until
`requested_at` plus the relay window less 1 s, `decide` on an undecided one
is `ErrNoOpenPermissionRequest`. After that, `decide` first waits until its
`confirm_by` (at most 3 s; `ErrStoreBusy` if `--max-wait-ms` cannot cover
it). An existing verdict or completed pane answer then keeps its own refusal;
a still-open request gets `ErrRelayFallenBack` only while the row is
`check_permission` with no other open or later request, otherwise
`ErrNoOpenPermissionRequest`. A pane answer by its token skips that check:
after `ErrNoOpenPermissionRequest`, do not answer it at the pane.

On relay-off rows, the API does not guard `send-keys` against an open
permission prompt: keys can reach the prompt and answer it. Cursor's gateway
still checks prompt admission; see [Cursor input outcomes](#cursor-input-outcomes).
Before sending, check that the row is not in
`check_permission`, or have a person or an LLM read the pane and send with
that read's `pane_sha256` as `--expect-pane-sha256`.

On relay-on rows, after a permission request, a plain `send-keys` without
`--expect-pane-sha256` is refused with `ErrDialogMaybeOpen` until the agent's
hooks show the request's dialog gone. Follow the request's proof and delivery
fields. Apart from a Cursor turn's end (below), completing or interrupting the
main turn does not release a child or unknown-origin request, even when its
child id is null. See [The dialog hold](permissions.md#the-dialog-hold).
Nothing is sent. Keep the keys and retry later. If it is still held after
some seconds, have a person or an LLM read the pane and, having looked, send with that read's
`pane_sha256`: a matching hash passes the API's dialog hold, and a changed pane
is `ErrPaneChanged`. Every other refusal still applies. On Cursor the gateway
can still refuse the prompt with `gateway_refused`; follow
[Cursor input outcomes](#cursor-input-outcomes). Never pass
`--expect-pane-sha256` from an automatic flow.

A matching hash does not close every race: the pane can change between
capture and key, a key already in flight can land on a dialog that appears
after the check, identical Claude or Codex dialogs cannot be told apart, and
a native dialog drawn more than 2 s after its hook ends can appear after
`record-pane-answer`. The affected request stays unproven and holds plain
input until someone looks again.

A hook that ends a turn proves gone only the requests recorded before the
whole second it started. This holds for a main-turn end (Claude's `Stop` or
idle-prompt Notification, Codex's `Stop` or `Interrupt`) and for a Claude
subagent's `SubagentStop`, which covers only that subagent's requests. A
request recorded in that second, even just before the turn ended, stays
unproven and holds until a later proof.

A shared recovery check repairs a relay-on Claude or Codex row left in
`check_permission` once no request holds it. A Claude row goes to `waiting`
while its main-agent idle-prompt Notification still applies, otherwise to
`working`. `SubagentStart` and `SubagentStop` leave that notification in
effect; any other applied hook ends it. A Codex row always goes to `working`.

A Cursor row can stay `check_permission` across working reports while a
genuine unanswered, unproven request holds it; readiness requires the hold to
resolve. Late matching Cursor execution proof resolves only that request's
hold; it does not immediately write `waiting` or replay a prior turn-end
report. A matching proof of an allowed request that arrives while the prompt
turn is still open, with no rotation under way, also moves a row no other
request holds from `check_permission` to `working`; a late one does not (see
"The row after the last answer" in [permissions.md](permissions.md#delivery)).

An unanswered request retired at a Cursor turn's end (see "A turn's end" in
[Cursor spawns](permissions.md#cursor-spawns)) still reads as awaiting an
answer: `get-permission` shows it with `proven_gone_how` `turn_end`, and if a
later request moves the row to `check_permission`, `permission_requests`
lists it beside that one. Do not answer this proven-gone request at the pane
or choose it to decide. Before `confirm_by`, a new verdict is refused with
`ErrNoOpenPermissionRequest`, leaving the request unchanged. At or after
`confirm_by`, an undecided, still-open request takes the fallback refusal:
`ErrRelayFallenBack`, which can record `attempted_decision`, `attempted_at`
and fallback history, but no new verdict or response. An existing verdict
or pane answer retains its own refusal. On a relay-on row, until its
`confirm_by` (24 h + 4 s after
admission at the default relay window) every `send-keys` and `pause` is
refused with `ErrSendKeysWhileRelayed`, and `decide` does not lift that
refusal. After `confirm_by`, a plain `send-keys` or `pause` gets
`ErrRelayFallenBack` until `record-pane-answer` closes the request, which it
accepts from 2 s past `confirm_by` (`ErrClaimTooSoon` before).

`pause` is held like a plain `send-keys` without `--expect-pane-sha256`
(`ErrSendKeysWhileRelayed`, `ErrRelayFallenBack`, `ErrDialogMaybeOpen`)
and types nothing; it takes no pane hash. To stop a held agent, see
[Answer a request at the pane](usage.md#answer-a-request-at-the-pane), or
`kill` it.
