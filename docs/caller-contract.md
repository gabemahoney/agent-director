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
- MCP and Go Client calls to `decide`, `status`, `get`, `send-keys`, `read-pane`,
  `kill`, `pause`, `resume` and `list-subagents` with an omitted or empty
  `instance_id` return `ErrInvalidFlags` naming `instance_id`, before any row
  is read or action taken. An unknown non-empty id returns `ErrSpawnNotFound`.
- A call refused before anything was written or sent is safe to re-issue. Retry
  `ErrTmuxUnresponsive` and `ErrTmuxKillFailed` with backoff and a cap, and
  alert at the cap, but not a timed-out `spawn` or `resume`, nor a `send-keys`
  whose text may be typed (see below). After a `kill` ends the last session on
  a tmux server, a
  repeated `kill` can get `ErrTmuxUnresponsive` or `ErrTmuxNotAvailable`: wait
  and check again.
- An uncovered agent's manual `send-keys` returning `ErrTmuxUnresponsive` with "may have been delivered" or
  "may be typed": do the step the error names, never resend the same text.
  `--text ""` sends Enter only. Retry a `pause` that returns
  `ErrTmuxUnresponsive` later.
- `ErrSubmissionUnconfirmed` with `outcome: "gateway_refused"`: follow
  [Cursor input outcomes](#cursor-input-outcomes). Otherwise, for automatic
  `send-keys`, follow
  [Ordinary text submission](#ordinary-text-submission); for manual
  `send-keys`, follow [Manual input ownership](#manual-input-ownership).
- `ErrCantSend`: nothing was stored, sent or typed; act on its `reason`
  ([Busy-send refusals](#busy-send-refusals)).
- `ErrCodexTurnErrored`: nothing was sent; inspection is required before more
  input. Follow [A Codex turn that ends in an error](#a-codex-turn-that-ends-in-an-error).
- `ErrSendAcceptedForceFailed`: the Message was accepted or its pair repeated,
  but its force step failed. Never send it as new; follow
  [Stored force attempts](#stored-force-attempts).
- `ErrStoreBusy` before admission stored nothing; retry within the existing
  call allowances. A migrator suspended or stalled by the host while holding
  SQLite's writer lock beyond a caller's finite allowance returns this ordinary
  error. Zero-wait and small-positive waits remain bounded; do not increase
  budgets or accept outside SQLite. Normal [upgrades](install.md#upgrade)
  preserve sends and CLI availability. An uncertain commit is a different
  outcome: follow [When the reply is uncertain](#covered-sends).
- `ErrPauseTimeout`: inspect before further input; never retry `pause`
  automatically. Follow [When pause cannot confirm exit](#when-pause-cannot-confirm-exit).
  Covered and [busy uncovered Codex](#keyless-pause-of-busy-uncovered-codex)
  shutdown refusals use `ErrSpawnNotPausable` instead: act on
  the [`cause`](#keyless-pause-of-a-covered-agent).
- `resume`, or a supported `spawn --reuse-finished` refused as "still stopping" or "still
  starting": wait and retry. Refused with "this row's own id", or
  `ErrTmuxSessionConflict` "this id's own abandoned launch": surface the named
  session to a human; never end it yourself.
- After a tmux-create timeout in `spawn` or `resume` the row stays `pending`: do not retry
  until `get` shows `ended` or `missing`, then `spawn --instance-id`
  with `--reuse-finished` and the row's agent (`get` shows it). For Cursor,
  inspect the latest session and follow the
  [resume or fresh-instance boundary](#cursor-operation-boundaries). A row noted `unreported` has a running agent: use it
  or end it first (see [Maintenance](maintenance.md)).
  A [delegated resume without a usable reply](#launch-end-resume-and-reuse)
  has an unknown outcome; it does not promise a `pending` row.
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
  `version`, TypeScript `binaryVersion`); `X.Y.Z-rc.N` counts as `X.Y.Z`.
  For a target's busy-send coverage, use its
  [returned coverage](#busy-send-coverage). The
  `version` result's `config_dir` names the directory the binary keeps its
  files in.
- A TypeScript call that outlasts `callTimeoutMs` (30 s by default) returns
  `ErrCallTimeout` while the verb may still complete: check the row first.
  A first send to pending Codex can wait up to 20 s for hooks, plus transport
  and admission. Raise `callTimeoutMs` if it is below about 25000 ms; slow
  transport or store contention can require more.

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

   A caller whose ids agent-director mints spawns fresh instead. A Covered
   Agent's `resume` or reuse also checks that its earlier launch can no
   longer deliver ([Launch end, resume and reuse](#launch-end-resume-and-reuse)).

## CLI arguments

The global flags `--store-path`, `--home`, `--tmux-command` and
`--create-if-missing` accept `--flag value` or `--flag=value`. They count
before the verb or at a flag position in the verb's grammar. A value-taking
flag owns its next word whatever it spells; neither that word nor an inline
`=value` is another flag. Globals stop at the first positional word, genuine
`--`, unknown flag or malformed flag token. For `get-many`, put all globals
before the verb ([Reading many agents](#reading-many-agents)).
`--create-if-missing` always needs a value, exactly `true` or `false`.

After global flags are extracted, a flag-only CLI verb rejects every word
left after flag parsing with exit 1 and `ErrInvalidFlags`. The description
lists all leftover words as `unexpected arguments: [...]`, including any
flags after the first stray word. The verb performs no action.
`trail-emit relay-attempt` applies this rule after the sub-verb.

`spawn` accepts native agent arguments only after a standalone `--` separator.
`spawn --cwd /x prompt --template T` is refused with `ErrInvalidFlags` before
any row or tmux action; neither `prompt` nor the later flags reach the agent.
Use `spawn --cwd /x --template T -- prompt`. An empty tail, as in
`spawn --cwd /x --template T --`, keeps the template's arguments.

`pause --instance-id A B C` is refused in full: A is not paused. Call `pause`
separately for each ID, with one `--instance-id` per call; the same rule
applies to `status` and other single-ID verbs. For reading several IDs, use
[`get-many`](#reading-many-agents).

`send-keys --instance-id X "text"` is refused without sending Enter. Pass
text with `agent-director send-keys --instance-id X --text "text"`.
For Enter-only input on uncovered agents, see [Manual input ownership](#manual-input-ownership).

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

Proof reconciliation defers to input writers. Reads without pending proof
take no input lock; a busy proof check defers until a later read and does not
fail the enclosing read. A `pause` or `send-keys` waits briefly for a proof
read already in progress; see [input admission](#manual-input-ownership)
for the bounds and refusal details.

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

## Busy-send coverage

Every successful `get` and found `get-many` `spawns` entry includes
`busy_send: covered | not_covered` for the target's published current launch,
including pending and finished rows. Use that returned fact for coverage
decisions; agent kind and the calling binary's version do not determine it.
`list` and `status` do not expose `busy_send`. A failed read remains an
error; it never fabricates a successful `not_covered` row.

Resume retains the conversation and preserves its coverage; fresh reuse
starts a new conversation even with the same agent. A candidate awaiting
creation or publication keeps the earlier published coverage,
[admission capabilities](#busy-send-refusals) and [Agent Defaults](#agent-defaults)
visible and authoritative for sends until
the new launch is published following creation, adoption or report-in.
A lost [delegated resume reply](#launch-end-resume-and-reuse) does not tell
you whether that publication occurred.

A covered row also shows `agent_defaults`, its [settings](#agent-defaults),
on `get`, `get-many`, `status` and `list`; `delivery`, its inbox, on `get`,
`get-many` and `status`; and `delivery_capabilities`, what busy send can do
for it, on `get` and `get-many`: its
[current launch's admission capabilities](#busy-send-refusals), its
[force capability](#claude-code-force-capability) and its
[Confirm From Transcripts](#confirm-from-transcripts) capability. Uncovered
rows omit all three. Permission-request delivery fields keep their separate
meaning.

`delivery.held_count` and `held_bytes` count the Messages Held now. The held
limits also count Messages handed over that could still return to Held, so
staying under them does not promise the next send fits.
`delivery.awaiting_turn_start` counts the current launch's hand-overs with an
issued prompt but no admission or negative evidence, whatever their output
state. On Claude Code a prompt is admitted by agent-director's own
[sighting](#claude-code-force-capability) of it or, for Steer Turn input handed
over at a tool boundary, by a later main-agent turn end. That turn end counts
the input only when agent-director's initial check finds its complete
output already recorded, and that output was observed before the turn end
began, to the millisecond. Output recorded after that check, or observed in
the turn end's millisecond or later, waits for a later turn end, as does
input in a turn interrupted before it ended; meanwhile ordinary Wait Turn
delivery waits `readiness_unknown`. Comparing the output's observation with
the turn end's start uses the host's wall clock across hook processes, a
known limit: a clock step can count steer output observed after the turn end
began but recorded before that initial check, or hold back earlier output
until a later turn end. Turn-end evidence supplies no wake order.
Neither kind of admission makes a Message `delivered_confirmed`, changes a
Final Result or permits a resend. The count is zero after Launch End.
`delivery.force` is the agent's running or
pending [force](#stored-force-attempts), else the latest force that began a
hand-over, as a `force_result`; null with neither. A refusal before any
hand-over never replaces it. Once the selected force expires, the summary is
null; it never falls back to an older force. Reading these fields hands nothing
over, wakes nothing, finishes no force and changes no Result; `state`,
`process_liveness`, `displayed_state`, `confirmations` and the permission
fields keep their own meanings.

## Busy-send options

These options apply only to Covered Agents:

- `send-keys`: `send_mode` (`wait_turn` or `steer_turn`), `confirmed_delivery`,
  `caller_uuid` and `force`.
- `spawn` (reuse included), `resume` and `make-template`: the Agent Defaults
  `send_mode` and `confirmed_delivery`, and the experimental
  [`confirm_from_transcripts`](#confirm-from-transcripts).
- `pause`: `only_if_waiting`.

Any supplied value counts, `false`, `wait_turn` and a value inherited from a
template included. On the CLI a bare boolean flag is true; write false
as `--force=false`. Over MCP, `null` is refused with `ErrInvalidFlags`, never
read as omitted. Every `send-keys` to a Covered Agent is a Message
([Covered sends](#covered-sends)). Otherwise a call that names none of them,
or a `pause` with `only_if_waiting` false or omitted, works as the rest of
this contract says, raw keys included.

A call decides from the coverage of the launch its options apply to:

- `send-keys`, `pause` and `force-delivery`: the target's current launch, the
  [`busy_send`](#busy-send-coverage) that `get` returns.
- `resume`: its new launch, which keeps the conversation's coverage.
- `spawn`, reuse and `make-template`: the agent they resolve. New
  Claude Code conversations are covered; Codex, Cursor, Droid and OpenCode
  are not.

Where it is `covered`, `send-keys` sends a Message, `force` and
`force-delivery` [force its Held Messages](#stored-force-attempts),
`spawn`, reuse, `resume` and `make-template` store the
[Agent Defaults](#agent-defaults), and `pause` with `only_if_waiting: true`
[ends the agent only if it is waiting](#pause-only-if-waiting).

Where it is `not_covered`:

- `send-keys`, `spawn`, reuse, `resume` and `make-template` return
  `ErrCantSend` with `err_details`
  `{"class": "cant_send", "reason": "agent_not_covered", "resend_safe": true, "retry_with": "new_send"}`.
  Nothing was stored, sent, typed, written or launched; the same call without
  these options can succeed.
- `pause` with `only_if_waiting: true` returns `ErrSpawnNotPausable` with
  `err_details` `{"cause": "agent_not_covered"}`, whatever the row's state.
  Nothing was sent or ended.
- `make-template` decides for the template's `agent`. A template with no agent
  stores the options, and each `spawn` that uses it decides for the agent it
  resolves.

A call naming an option never falls back to input or a launch without it.

A `send_mode` other than `wait_turn` or `steer_turn`, or a `caller_uuid` that
is not a hyphenated hexadecimal UUID, is refused before coverage is decided:
`ErrInvalidFlags`, or `ErrTemplateMalformed` from `make-template` and for a
template's own `send_mode`. A `send-keys` to an uncovered or unknown target
checks its other input shapes first. An unknown id is `ErrSpawnNotFound`,
except for a [repeat](#covered-sends) of a stored pair. Then `send-keys`,
`pause` and `resume` decide coverage before any other check of the row, such
as its state, relay or agent capability. `spawn`, reuse and `make-template`
decide it only once their other checks of the template, agent, arguments,
environment, id and credentials pass; those keep their own errors.
`agent_not_covered` comes before any other `ErrCantSend` reason. Store
failures keep their own errors.

### Agent Defaults

A Covered Agent's Message takes its Agent Defaults for any of `send_mode` and
`confirmed_delivery` the send leaves out. Out of the box they are `wait_turn`
and `false`, and `confirm_from_transcripts` is `false`. Each value resolves on
its own:

- `spawn` and reuse: the call's value, else the template's, else the
  out-of-box value. An explicit `false` or `wait_turn` overrides the template.
- `resume`: the conversation keeps its values; a value the call names replaces
  that one when the resumed launch is published (see
  [Busy-send coverage](#busy-send-coverage)). Until publication the earlier
  values remain. A lost [delegated reply](#launch-end-resume-and-reuse) can
  leave publication unknown. To change an agent's defaults, resume it with new ones.
- `make-template`: stored for the spawns that use the template.

A `send_mode` the agent does not offer is `ErrCantSend`, reason
`steer_unsupported`, before anything is written or launched; a template with
no agent is checked at each spawn that uses it. For the experimental
`confirm_from_transcripts`, see [Confirm From Transcripts](#confirm-from-transcripts).

### Covered sends

A Covered Agent's `send-keys` stores its `text` as a Message in the agent's
inbox and returns its [Result](#message-results). Claude Code delivers
Messages keylessly. Acceptance types nothing and does not wait for the agent:
a Message is accepted in any state, `pending`, `ended` and `missing`
included, so `allow_pending` changes nothing.

The text is kept byte for byte: TAB, LF, Unicode and whitespace included,
nothing normalized. These are malformed, `ErrInvalidFlags` with nothing
stored:

- `key`, `no_enter`, `request_token`, `as`, `expect_pane_sha256` or `n_lines`,
  even at a zero, `false` or empty value. Follow the source advice:
  "send the text alone, and use tmux with record-pane-answer for keys on the pane";
  see [Answering at the pane](permissions.md#answering-at-the-pane).
- Empty `text`, or `text` or `delivery_request_id` that is not valid UTF-8
  (over MCP, a lone surrogate escape counts as invalid).
- A confirmed send without `caller_uuid`; an unconfirmed send with
  `caller_uuid` or `delivery_request_id`; a `delivery_request_id` that is
  empty, null or without `caller_uuid`.

**Confirmed and unconfirmed.** The resolved `confirmed_delivery` decides;
supplying an ID never turns it on. A confirmed send carries `caller_uuid`
and may carry `delivery_request_id`, its Message ID: any nonempty UTF-8,
unique only within that Caller UUID. If it carries none, agent-director makes
a UUID. The Result returns the lowercase `caller_uuid` and the Message ID as
`request_id`. Supply your own ID: after a lost reply, only a known pair can be
repeated safely. An unconfirmed send returns no ID and has no lookup.

**Repeats.** A pair of Caller UUID and Message ID belongs, store-wide, to the
first Message accepted with it. A send repeating the pair with the same target
and the exact text bytes stores nothing and returns the owner's current
Result while retained, even after the launch ended, the row was deleted or the
inbox filled. The owner's options stand; malformed parameters, a store failure
or a target now `not_covered` can refuse the repeat. While retained, the same
pair with another target or text is `ErrCantSend`, reason `id_conflict`.
After [expiry](#durable-lookups), matching repeats and uses with another text
or target are refused with `ErrCantSend`, reason
`retention_expired`. The pair stays owned forever. A refused send claims no pair.

**When the reply is uncertain.** A [pre-admission `ErrStoreBusy`](#caller-contract)
is a refusal. An
`ErrInternal` saying whether the Message was accepted is unknown (it has no
`err_details`) is not a refusal: the Message may be Held. For a confirmed
send, repeat the SAME Caller UUID and Message ID, target and exact text bytes,
or call `get-delivery` with that pair; the [repeat rules above](#covered-sends)
preserve ownership and never accept it twice. An unconfirmed send has no
identity, so nothing resolves it: `get`'s `delivery` counts are aggregates.
Never resend it automatically. Where a send must be recoverable, use confirmed
delivery with your own `caller_uuid` and `delivery_request_id`.

### Busy-send refusals

Other refusals are `ErrCantSend`. Nothing was stored, sent, typed or launched,
and no pair was claimed. `err_details` has `class: "cant_send"`, `reason`,
`resend_safe` and `retry_with`, plus `limit` (`actual`, `allowed`) for a limit
and `result`, the owner's Result, for `id_conflict`. A covered send is checked
in the table's order.

| `reason` | Refused because | Next step (`resend_safe`, `retry_with`) |
|---|---|---|
| `retention_expired` | `expire` removed the pair's Result; the pair stays owned. | `false`, `none`: never reuse the pair. |
| `id_conflict` | The pair names a retained Message with another target or text. | `true`, `new_send`: send it under a new `delivery_request_id`. |
| `agent_not_covered` | Busy send does not cover the launch. | `true`, `new_send`: the same call without busy-send options. |
| `agent_command` | The text's first non-whitespace character is `/` or `!`. | `false`, `none` |
| `steer_unsupported` | The agent does not offer the resolved `send_mode`. | `true`, `new_send`: use `wait_turn`. |
| `text_kind` | The text holds a control character below U+0020 (NUL and CR included) other than TAB and LF. | `false`, `none` |
| `text_bytes` | The text is over `message_bytes`. | `false`, `none` |
| `prompt_size` | The Message would not fit one prompt on its own. | `false`, `none` |
| `held_count`, `held_bytes` | The agent's held limit is full. | `true`, `new_send`: send again once Held Messages drain. |

`false`, `none`: the same send can never succeed. `delivery_capabilities.modes`
reports the published current launch's offered modes, text kind and
Message/prompt limits; `held_count` and `held_bytes` report its held limits.
These admission capabilities are fixed when the launch is created; different
values apply from the next published launch. Force and Confirm From Transcripts
capabilities describe the calling version's route separately.

The limits are agent-director's policy, counted in UTF-8 bytes, not measured
provider maxima. `message_bytes` counts a Message's own text; `prompt_bytes`
counts a whole prompt with agent-director's separators and the mode's wrapping and
escaping, and a Message within `message_bytes` always fits a prompt on its
own, whichever boundary delivers it. Retained Held Messages remain deliverable
whole through resume, with their original text and options.
Claude Code offers `wait_turn` and `steer_turn`, each allowing 8,000 Message
bytes and 9,600 prompt bytes of
`utf8_tab_lf` text; its held limits are 1,000 Messages and 8,000,000 bytes.

### Message Results

Every accepted send succeeds and returns its Message's Result, never
`ErrSubmissionUnconfirmed`, except for
[`ErrSendAcceptedForceFailed`](#stored-force-attempts). Its accepted Result,
a found `get-delivery` and an `id_conflict` refusal carry the same shape.
Decide from `class` and `final`:

| `class` | `final` | Meaning | Next step |
|---|---|---|---|
| `held` | `false` | Accepted; nothing has reached the agent. | Observe; never resend. |
| `queued` | `false` | The agent's harness holds it but hasn't taken it in, so it isn't Delivered. | Observe; never resend. |
| `delivered_confirmed` | `true` | Allowed exact evidence shows the harness took this Message in, on the right launch and session. | Never resend. |
| `sent_unconfirmed` | `false` or `true` | Some or all of it may have reached the agent; it can't be proven Delivered. Until final, proof may still arrive. | Never resend; once final, act on `reason`. |
| `not_started` | `true` | Accepted, then ended with nothing reaching the agent, because its conversation can no longer be resumed. | Resend with a new Message ID. |

Delivered means the message was passed to the harness, but not necessarily confirmed received by the LLM.
`queued` is not Delivered.

A final Result never changes. Its fields:

- `caller_uuid`, `request_id`: the confirmed pair; omitted for an unconfirmed
  send, whose Result is `held` or, once handed over, final `sent_unconfirmed`.
- `reason`: the stable machine-readable reason. Advice text is a hint.
- `resend_safe`, `retry_with`: `true`, `new_id` only for `not_started`;
  `false`, `none` otherwise. Repeating a pair reads its Result; it never
  resends.
- `wait`: null when final; otherwise `reason`, `since` (UTC) and, behind an
  earlier confirmed Message, `earlier` with its `caller_uuid` and `request_id`.
- `forced`: whether its hand-over was forced.
- `instance_id`: the original target. `launch_token` (null) and `session_id`
  (omitted) name the launch and session it was actually handed to, once it is.
- `evidence_kind` (null), `record_id` and `record_sha256` (omitted): the
  qualified receipt's identity, once one exists.
- No Message text, raw evidence or private paths.

`outcome` maps `class` for older callers: `delivered_confirmed` is `submitted`;
`held` and `not_started` are `not_started`; `queued` and `sent_unconfirmed` are
`submission_unconfirmed`, or `partial_input_unconfirmed` when only part of the
prompt is known written. No `outcome` is permission to resend.

### Wait Turn delivery and waits

Claude receives whole Message bodies in accepted order, grouped into the fewest
prompts that fit its [limits](#busy-send-refusals), each body introduced by
`[agent-director: message N of M]`. When several prompts are needed, one goes
per wake and each remaining prompt goes at the next eligible turn end. No
other agent-director input goes between them; a person's typing can. This adds
latency and exposure to `wait_turn_raced` between prompts.

An accepted Message has no age timeout. Read its Result's `wait`, or
`delivery.first_wait` for the first Held Message:

| Wait reason | Next step |
|---|---|
| `turn_running`, `compaction` | Wait for the agent's next eligible boundary. |
| `dialog` | Inspect the request; use [permission handling](permissions.md#answering-at-the-pane). Ordinary delivery never answers it. A [Claude pane denial](#claude-pane-denial-without-a-turn-end) can leave this wait indefinitely. |
| `earlier_message`, `handover_in_progress`, `input_unresolved`, `awaiting_proof` | Observe the earlier hand-over; never resend. |
| `agent_starting` | Wait for startup; `allow_pending` does not bypass it. |
| `launch_ended` | Follow [Launch end, resume and reuse](#launch-end-resume-and-reuse). |
| `route_unavailable` | Reconnect with keyless pause and resume. An existing Claude launch covered by migration has no delivery route until a new launch supplies one. |
| `readiness_unknown` | Inspect the agent and outstanding hand-over; waiting alone supplies no readiness evidence. |

A send's wake, [launch startup](#launch-end-resume-and-reuse), an idle-prompt
Notification, a turn end or, for [Steer Turn](#steer-turn-delivery), a tool
boundary can enable delivery. A lost or coalesced wake leaves Messages Held,
with no time limit, until a later eligible event or a
[force](#stored-force-attempts): reads, `sweep` and elapsed time never
deliver, and nothing retries while the agent is idle. Claude's wake hand-over
is race-exposed, a Steer Turn Message it carries included: its Result ends
`sent_unconfirmed`, reason `wait_turn_raced`, even if a later exact sighting
clears `awaiting_turn_start`. The sticky reason records
exposure to overlapping work, not proof that a race happened. A Final Result
never upgrades on later evidence. Neither that reason nor missing proof
permits a resend.

Every Claude wake, ordinary or forced, secures order only once both its
complete output and agent-director's exact, uniquely bound
[sighting](#claude-code-force-capability) are recorded, in either order,
while its Launch runs. Neither fact alone nor a parent `Stop` releases wake
order. A wake agent-director never sights, including one hidden by the
[sighting limits](#sighting-limits) or impossible to bind, keeps every later
Message of its conversation, Steer Turn included, Held until
[Launch End](#launch-end-resume-and-reuse). Elapsed time, reads, repeated
Stops and a force cannot bypass that barrier.

Complete live output makes the wake's Result Final `sent_unconfirmed`
and frees capacity without proving intake. A Final Result, zero
`handover_count` or zero `awaiting_turn_start` alone does not establish that
order is released: a sighting before complete output clears the count but
retains ownership. Later Messages can still wait `handover_in_progress`.
Ordinary delivery after order is secured needs its own eligible event and readiness; a
sighting does not itself deliver ordinary input.

Message-caused screen changes reset the ordinary `idle_screen` clock. No
length of waiting alone becomes `needs_intelligence`.

### Steer Turn delivery

A working Claude agent takes Steer Turn Messages inside its running turn, at
the main agent's next tool boundary, once a batch of its tool calls finishes.
The waiting Steer Turn Messages ahead of the first Wait Turn Message go
together, whole and in accepted order, in the fewest prompts that fit the
[limits](#busy-send-refusals); that Wait Turn Message and every later one wait
for turn end. Each tool boundary takes one prompt. When the Messages need
several, each goes at the next tool boundary, with no other agent-director
input between them, and any left at turn end follow
[Wait Turn delivery](#wait-turn-delivery-and-waits). A subagent's or
background agent's tool boundary delivers nothing. An idle agent takes Steer
Turn Messages through the same wake as Wait Turn.

A steer never interrupts or cancels the turn and answers no Dialog. An open
Dialog, a spawn's startup prompt not yet taken and an earlier hand-over still
unresolved hold it; earlier steered input still counted in
[`awaiting_turn_start`](#busy-send-coverage) does not. Tool boundaries in the
startup prompt's own turn are eligible once that prompt is taken.
An earlier wake holds a steer until its
[order is secured](#wait-turn-delivery-and-waits). The steer can then go at a
later eligible tool boundary; no additional Stop is needed as wake order
evidence.

With nothing ahead of it and nothing holding it, a Steer Turn Message reaches
the model at the same point as the same text typed at the agent's terminal at
the same moment, or sooner, as measured on Claude Code. One sent after the
turn's last tool boundary goes by the wake at turn end instead, with that
path's delays: a lost wake, or earlier input still counted in
`awaiting_turn_start`. No time is promised.

A complete steer at a tool boundary is not race-exposed, unlike the wake. Its
Result ends `sent_unconfirmed`, reason `handed_over_not_proven` for a
confirmed Message (`confirmation_off` for an unconfirmed one, which no lookup
reads), never `wait_turn_raced`. Partial or uncertain output holds every later
Message and is never written again ([Message Results](#message-results)).
Neither reason permits a resend.

### Checking whether work is done

For a Covered Agent, assess `waiting` together with
[`confirmations.state`](#confirmations), its confirmation time and uncertainty.
Also require `delivery.held_count == 0`, `handover_count == 0`, no
`startup_wait`, and [`awaiting_turn_start`](#busy-send-coverage) `== 0`. A
Final Result alone does not mean the turn its Message started or steered has
finished: a hand-over issued while idle can precede its turn, and one handed
into a running turn precedes that turn's end.
This observation proves neither continuous inactivity nor completion of
descendants; see [Family reporting limits](#family-reporting-limits).

### Stored force attempts

`force-delivery`, and `send-keys` with `force: true`, force a Covered Agent's
Held Messages: they skip only the waits for turn end, a Dialog or compaction,
and every other guard stands. **A force call blocks for up to about 15
seconds**: 12 to issue and observe, then 3 to record its end. Budget for that
wait, and set any call timeout above it. Each call stores its own attempt
under a new `force_id` that agent-director makes, and `get-force` reads it
again ([Durable lookups](#durable-lookups)).

**`force-delivery`.** Every outcome, refused included, is a success. Only an
empty id (`ErrInvalidFlags`), an unknown or deleted one (`ErrSpawnNotFound`)
and a store failure are errors: `ErrStoreBusy` stored nothing, and any other
is unnamed. An unnamed error that names a `force_id` means the attempt is
stored: read it with `get-force`, and never treat it as refused. One without a
`force_id` may still have stored a running attempt whose commit was not
acknowledged: `get` or `status` then shows it as `delivery.force`. Either is
finished as a call that never ends, below. On a target whose current launch
is `not_covered` the attempt is `refused`, reason
`agent_not_covered`, with `ended_at` equal to `started_at` and nothing
carried; nothing is delivered, typed or changed.

**`send-keys` with `force: true`.** The send is decided first, as any send. A
refused or uncertain send forces nothing and stores no attempt. An accepted
send returns its Message's [Result](#message-results) and a separate
`force_result`. A successful repeat of the pair with `force` makes a new attempt
and never accepts the Message again; after the target's deletion, it returns
the retained Result with a `refused` force, reason `record_deleted`. A force
step that errors after acceptance or a matching-pair repeat returns
`ErrSendAcceptedForceFailed`, even when the cause is store busy; it is neither
`ErrStoreBusy` nor an admission refusal, uncertain admission COMMIT or
`ErrSubmissionUnconfirmed`. Its details contain only required
`err_details.result`: exactly the accepted [Message Result](#message-results)
already built by that call, with the same fields, nulls and omissions.

Never send this Message as new to recover the force error. For a confirmed
Message, use `result.caller_uuid` and `result.request_id`, including an
agent-director-generated ID, for `get-delivery`. To force again, repeat that
pair with the original target, exact text and `force: true`; the send's ID
parameter is `delivery_request_id`. This accepts no second Message and keeps
the [repeat guards](#covered-sends). An unconfirmed Result omits both pair
fields and has no lookup: use explicit `force-delivery` for the agent's Held
Messages without resending. Every force guard still applies.

A failure before force storage leaves no attempt. An end-recording failure
after a committed claim can leave a real force running; this error's details
report no force outcome or ID. Inspect `get`/`status` `delivery.force` and
`get-force`, and follow the finite-deadline recovery under **A call that never
ends**, below. This error is separate from a successful response whose
`force_result.outcome` is `failed`. `force: false` is an ordinary send.

**`force_result`** has `force_id`, `outcome`, `reason`, `started_at`,
`ended_at` (null while `pending` or `running`) and two arrays, `[]` when
empty:

- `carried_pairs`: the pair of every confirmed Message the force carries, in
  accepted order, even after its Result expires.
- `carried_results`: the current [Results](#message-results) of those whose
  Result is still retained, in the same order, read with the force at one
  moment. An expired Result drops out, so it can be shorter than
  `carried_pairs`, without expiring the force itself.

An unconfirmed carried Message appears in neither. A force carries every
Message Held when it starts and nothing accepted later; those wait as usual.
A Message's `forced` turns true only when its own prompt is issued, and false
again if it returns to Held.

| `outcome` | `reason` | Meaning |
|---|---|---|
| `handed_over` | `admitted` | The agent's route took in every issued prompt. This is not Delivered: read each Message's Result. |
| `pending` | `awaiting_admission` | An issued prompt's admission can still arrive. |
| `refused` | below | This force handed nothing over. Existing custody is unchanged, and any Message it carried is Held again. |
| `failed` | `admission_unknown` | The route showed an issued prompt will never be admitted, or can never show admission. |
| `failed` | `launch_ended` | The launch ended before every issued prompt was admitted. |

Before carrying anything, a force is refused for the first of these that
applies:

1. `agent_starting`: the current launch has not started.
2. `launch_ended`: it has ended.
3. `route_unavailable`: it has no delivery route.
4. `handover_in_progress`: another force is running or pending, or an
   earlier hand-over still owns the agent's order or input, including one
   from a finished force.
5. `no_held_messages`: nothing is Held.
6. `earlier_message`: an earlier Message is still being handed over.
7. `input_unresolved`: agent-director's earlier input still awaits evidence
   that the agent took it (`awaiting_turn_start` above 0).

So `agent_starting` and `launch_ended` apply even with nothing Held, and a
pending force makes a later force `handover_in_progress`, not
`no_held_messages`. An unsighted wake retains ownership through a Stop, so
`handover_in_progress` can precede `input_unresolved` too.
A force that carried Messages is refused `not_issued`
when it issued no prompt by its call's end, even if the launch ended
meanwhile, or `not_submitted` when the route proved every issued prompt was
not submitted; a missing admission is not that proof.

**When the call ends**, carried Messages whose prompt was never issued return
to Held in accepted order, and agent-director notifies the agent's route, so
ordinary delivery follows with no later send. An issued Message keeps its
hand-over and is never issued again, unless the route proves its prompt
`not_submitted`: then its non-Final Messages, and those of any later
never-issued prompt, return to Held, unforced, for ordinary delivery.
agent-director returns them; that is no permission to resend. When a force
fails, a Final Result stays, a Message with no issued prompt stays Held, and
every other issued Message becomes Final `sent_unconfirmed`. Never resend it.

**Pending** needs no running process and survives a restart, with confirmed
delivery off too. It ends only when the route admits the last unadmitted
prompt (`handed_over`), shows it will never be admitted (`failed`,
`admission_unknown`) or proves it `not_submitted`, or the launch ends
(`failed`, `launch_ended`). A `not_submitted` prompt no longer counts as
issued, so the force ends `refused`, `not_submitted` if no issued prompt is
left, or `handed_over` if every other was admitted; as above, a missing
admission is not that proof. Reads, `sweep` and elapsed time never end it,
though a Launch End they record does.
A force's outcome and its Messages' Results are separate: a Message can be
Final while its force is pending, and admission never changes a Final Result.

**Running.** `get-force` and `delivery.force` show `outcome: "running"`,
`reason: ""` and `ended_at: null` while the attempt's call has not returned.
A force call's own `force_result` never does, and `running` says nothing
about admission.

**A call that never ends**, because its caller died or its end could not be
recorded, leaves the force `running`. Once its recorded 15-second deadline has
passed, the agent's next eligible event, or the next accepted `send-keys` or
`force-delivery` for that agent, finishes it by the rules above; a force
refused `agent_starting`, `launch_ended` or `route_unavailable` does not.
Nothing retries in the background.

#### Claude Code force capability

On Claude Code a force prompt is admitted when agent-director's own
`UserPromptSubmit` hook sees the whole prompt exactly, once Claude Code's
measured wrapping is undone, on the launch, session, connection and hand-over
that issued it: its sighting. Finished output is not admission, and admission
is neither a Message's receipt nor the model acting on it. Claude Code gives
no negative: a missing sighting, a hook's exit or elapsed time never fails or
refuses a force.

`delivery_capabilities.force` publishes `admission: "prompt_sighting"`, the
[sighting limits](#sighting-limits) as `limits`, and `states`: what a force
made in each agent state returns, in this order.

Force-state requalification on Claude Code 2.1.311 remains pending.

| `state` | `outcome` | `reason` |
|---|---|---|
| `idle` | `handed_over` | `admitted` |
| `tool_running` | `pending` | `turn_running` |
| `between_tools` | `pending` | `turn_running` |
| `permission_dialog` | `pending` | `dialog` |
| `plan_dialog` | `pending` | `dialog` |
| `compacting` | `pending` | `compaction` |
| `not_started` | `refused` | `agent_starting` |
| `ended` | `refused` | `launch_ended` |

The rows describe measured behaviour and promise no time; a force's own
`force_result` is its outcome. A force made while idle still
returns `pending` when its sighting comes after the call returns, and one in a
`pending` row returns `handed_over` when its sighting comes during the call. A
`pending` row's reason is what the prompt waits behind; a pending force's own
reason is `awaiting_admission`. The refusal order and every other guard above
still apply.

**Several prompts.** Each forced wake hands over one whole prompt. While the
same call remains authorized within its budget, another forced wake can issue
the next prompt only after the previous prompt has recorded
[order](#wait-turn-delivery-and-waits) and admission; no Stop is required.
Prompts not issued when the call ends go back to ordinary delivery, as above,
on its own schedule ([Wait Turn delivery and waits](#wait-turn-delivery-and-waits)).
Wakes during the call serve only the force, so a Message accepted meanwhile
waits for an eligible event after it.

A force types no keys, answers no Dialog, changes no permission and cancels
nothing, and a spawn's startup prompt still goes first. agent-director never
forces on its own: `idle_screen`, startup, `resume` and turn ends start no
force.

#### Sighting limits

`force.limits` names, in this order, where a sighting cannot show whether
Claude Code took a prompt, forced or not:

- `duplicate_text`: when two prompts awaiting a sighting render to the same
  text, a sighting binds to neither.
- `unrecoverable_escaping`: Claude Code escapes reserved closing tags, and
  text that only looks escaped reads the same, so a prompt holding either
  gives no sighting. Nor does one whose wrapping changed.
- `user_hook_after_sighting`: a later `UserPromptSubmit` hook of yours can
  block prompt processing or add `additionalContext`. agent-director cannot
  establish either effect on model intake. A sighting and secured order are
  not model intake proof; agent-director never changes your hooks.

agent-director adds no marker, nonce or ID to the text the model sees. These
limits can leave a force `pending` until Launch End, and ordinary delivery's
`awaiting_turn_start` nonzero. That uncertainty never permits a resend.

### Confirm From Transcripts

`confirm_from_transcripts` is an experimental setting, `false` out of the
box, that lets agent-director count a match in the agent's own transcript as
evidence that a force was admitted, where the agent's capability allows. It
is set ahead of the launch like the [Agent Defaults](#agent-defaults): at
`spawn` or reuse, in a template, or at `resume`. A plain `resume` keeps it.
Nothing changes it during a launch; a `resume` naming a new value applies it
to the new launch, and the earlier launch's end has already finished any
force pending there ([Launch end](#launch-end-resume-and-reuse)).

`agent_defaults.confirm_from_transcripts` shows the setting on `status`,
`get`, `get-many` and `list`. `delivery_capabilities` on `get` and `get-many`
shows what it can do for the agent, whatever the setting:
`confirm_from_transcripts_capability` is `can_confirm`, `not_needed` or
`cannot_confirm`, and `confirm_from_transcripts_reason` is a stable code
saying why. This experimental setting rests on agent internals with no
published contract and can change without notice. Changes to the setting or
what it reports are not breaking changes, including changes to those codes.

Claude Code is `not_needed`, reason `hook_sighting_first`: agent-director's
own [sighting](#claude-code-force-capability) came before Claude Code's
transcript record in every state measured. Claude Code accepts the setting,
and it changes nothing: agent-director reads no transcript to confirm a force,
and no force reason names a transcript match. For an agent busy send does not
cover, setting it, `false` included, is refused with `agent_not_covered`
([Busy-send options](#busy-send-options)).

### Durable lookups

`get-force` reads a force attempt by its `force_id`. `get-delivery` reads a
confirmed Message's Result by its pair, `caller_uuid` and
`delivery_request_id`. `force_id` and `caller_uuid` are hyphenated hexadecimal
UUIDs, in either case, read as lowercase; `delivery_request_id` is nonempty
UTF-8. Any other form is `ErrInvalidFlags`.

Each returns `lookup`: `found`, `unknown` (nothing stored) or
`retention_expired`, with `force_result` or `result` null unless `found`. A
pair no Message holds, including one only refused sends used and a native
`delivery_request_id` sent without `caller_uuid`, is `unknown`. A found
`result` is the [Result](#message-results) a repeat of the owner's send would
return at that moment; a found `force_result` is the
[attempt as stored now](#stored-force-attempts).

Non-Final Messages remain retained. Final Results become eligible for `expire`
after `defaults.expire_retention_days` (31 days by default) from their Final
transition; ended forces, refused attempts included, use their own end time.
An operator's `older_than` overrides that window; explicit zero removes the
age requirement. They stay `found` until `expire` removes their retained
payload: elapsed time alone never expires a lookup. See
[maintenance](maintenance.md#scheduling-the-sweeps) for cleanup and text
protection. An expired pair remains owned and never becomes `unknown` or free
for another send.

They read stored facts only: they start no work, run no
[recovery checks](#bounded-recovery-on-state-reads) and probe no agent, tmux
or process. Stored facts stay readable after a restart, the launch's end and
the row's deletion. Only a found `not_started` Result is permission to
resend; `unknown` and `retention_expired` never are. A store failure is an
error (`ErrStoreBusy` when a bounded read runs out of time), never `unknown`.

### Launch end, resume and reuse

For launch limits and preflight recovery, see
[Launch size and representation](#launch-size-and-representation).

If this process lacks a covered conversation's provider, `resume` transparently
delegates the whole call to the newest compatible retained installed version
that covers it, independently of the published version or rollback. Results
and named errors, descriptions and details keep their meaning through CLI,
MCP, Go and TypeScript. This applies only to `resume`; a covered target never
falls back to raw typing or an uncovered launch.

No matching retained version returns `ErrSpawnNotResumable` with
`err_details.cause: covering_version_unavailable` and `err_details.provider`.
Nothing changed or launched; installing a compatible covering version permits
retry. An unreadable or malformed installation is a diagnostic failure, not
evidence that no covering version exists. A pre-start failure reports that no
delegate started. If a delegate started but supplied no usable response,
plain `ErrInternal` without `err_details` means the outcome is unknown: a
Launch may exist. Check `get` and do not automatically retry. The original
process never falls back locally or tries another delegate.

When a Covered Agent becomes `ended` or `missing` (after `pause`, `kill`,
`sweep` or a crash), or its row is deleted, agent-director records launch end
and types and resends nothing:

- A Message the agent cannot have received stays Held, in accepted order with
  its options, and waits `launch_ended`.
- A Message the agent may have received becomes final `sent_unconfirmed`, with
  reason `wait_turn_raced` or `confirmation_off` where they apply, else
  `launch_ended`. Never resend it.
- A final Result never changes.
- A running or pending [force](#stored-force-attempts) ends: `refused`,
  `not_issued` if it issued nothing, `handed_over` if every issued prompt was
  admitted, else `failed`, `launch_ended`. No force carries to a new launch.

An agent marked `missing` may still be running. If it reports in again, its
old launch's delivery authority stays fenced; Held Messages still wait
`launch_ended` until `resume` starts a new launch.

`ended`, `missing`, a finished-row `kill` or `pause` no-op, or the agent's exit
alone does not show that the earlier launch can no longer deliver. Before
`resume` or a reuse writes anything, agent-director checks by process
identity, without tmux, that every process the earlier launch owned is gone:
the agent and any agent-director hook or delivery job that could still give it
input. If one still runs or cannot be checked, nothing is written or launched:
`resume` returns `ErrSpawnNotResumable` and reuse `ErrInstanceIdCollision`.
Retry once the condition the error names clears, such as that process having
exited (an agent-director hook or delivery job ends on its own within its job
budget); on these finished rows `kill` and `sweep` end nothing.

`resume` keeps the conversation: its Held Messages carry to the new launch in
accepted order with their options, and no startup prompt is replayed. They
wait `agent_starting` while the row is `pending`. After Claude's SessionStart
registers the wake path, startup makes one bounded wake attempt so an idle
resumed agent receives Held Messages without another send or turn. A spawn
without a startup prompt gets the same wake; with a startup prompt, its first
turn's end enables delivery. A failed startup wake is logged and leaves
Messages Held for the next eligible event. `sweep` and reads deliver nothing.
A failed launch leaves them Held, waiting `launch_ended`.

A reuse starts a new conversation. Until it is published, sends still go to
the earlier one ([Busy-send coverage](#busy-send-coverage)). Once it is
published, every Message still Held in the earlier conversation, those
accepted while the reuse was pending included, ends `not_started` with reason
`conversation_replaced`,
`resend_safe: true` and `retry_with: new_id`. A human's `delete` of the row
([Operator actions](operator-actions.md)) ends them the same way with reason
`record_deleted`. Messages the agent may have received keep
`sent_unconfirmed`. Confirmed Results stay readable through `get-delivery`
afterwards; an unconfirmed Message has no lookup.

## Reading many agents

To poll many agents, use `list` for their IDs, then one `get-many`:

```sh
agent-director get-many agent-a agent-b
```

On the CLI, every word after `get-many` is one ID, unchanged, including
commas, a leading `-`, global-flag spellings and `--`. Put global flags
before the verb.

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
rows once, sorted by `instance_id`. `checks` includes only identities whose raw
state was live at selection (`pending`, `waiting`, `working`, `ask_user` or
`check_permission`), before settling or recovery. Each remains even if it later
finishes, changes, disappears or is unattempted; rows created later are not
added. A per-agent failure does not suppress independent agents.

`checks` omits rows already `ended` or `missing` at selection. Read those rows
with `status`, `get`, `get-many` or `list`. Finished rows receive eligible recovery
checks, including late exact input proof.

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

## Failure reasons

`get`, each `get-many.spawns` row and `status` always include
`failure_reason: string | null`. A non-null value is a sanitized failure for
the reported state's current interval: `waiting` means a failed Cursor turn;
`missing` means a failed session. It is separate from
`confirmations.state.reason`, which explains confirmation status.

Use `get` or `get-many`'s `agent` field to identify recording support;
`status` does not include `agent`:

| `agent` | `failure_reason` |
| --- | --- |
| `cursor` | Recorded reason, or explicit `null` when no applicable reason is recorded. |
| All other agents | Always `null`, regardless of outcome. |

Legacy rows and failures without a recorded reason also return `null`; it
does not prove success. The reason follows state settling, including the
previous state's reason while that state is still reported. Reads do not clear
it. A later lifecycle event clears it when it replaces the state's evidence,
including a new prompt, valid completion, published rotation or new launch. Repeated
cancellation/idle recovery of the same failed turn retains it. Historical
outcomes remain in the protected transcript. Neither the field nor `waiting`
authorizes replay of input. See [Cursor operation boundaries](#cursor-operation-boundaries).

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
  When this reader hides a shared idle episode below its own timeout, only
  this call's successful matching pane sample can confirm no Displayed State,
  at that sample's time. This response-only record never replaces the stored
  episode's evidence. Without that sample, omission confirms nothing; the
  hidden episode's record cannot confirm it or prove a longer capture span.
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
since launch. Read its pane before acting. A Covered Agent accepts ordinary
Messages while pending and holds them `agent_starting`; `allow_pending` changes
nothing. For startup choices on Claude, use direct tmux after inspecting the
owned pane ([Answering at the pane](permissions.md#answering-at-the-pane)).
For uncovered agents, use `allow_pending` only after a person or an LLM judged
the screen; `key: "Enter"` is a startup choice, separate from ordinary text.
For Codex Enter-only sends, see [Manual input ownership](#manual-input-ownership).

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
any turn. `waiting` does not mean the turn succeeded or that its composer is
ready: the pane can show a menu. The row's `state_changed_at` is the time of
that recovery write, not of the turn's end.

`get` carries `last_turn_errored: boolean | null` at the top level;
`get-many` carries it in every found `spawns` row (exactly `get`'s shape), and
`list` in every `spawns` row. The key is always present on those surfaces,
never on `status` or `sweep`:

- `true`: the current Codex session's latest observed turn boundary is a
  completion with `payload.error` present. Its value is never read;
  `"error": null` also counts as present.
- `false`: that boundary is a completion without the error key, or a new
  `task_started`.
- `null`: another agent, or unavailable bounded rollout evidence, including
  a missing/unreadable rollout or no usable turn boundary. This means unknown,
  never successful completion.

This live observation is independent of state settling and of whether the
turn's start hook arrived. It detects neither menus nor composer readiness.
While the errored completion remains observable, reads, sends, menu answers
and pane-hash checks do not clear it; the next observed `task_started` does.
It is not retained independently of the rollout, so lost evidence can yield
`null`. The screen and rollout can change after a read.

When the observation is `true`, ordinary nonempty text plus Enter is refused
with `ErrCodexTurnErrored`, with no input sent, on relay-on and relay-off rows.
`--allow-pending` does not bypass it. Have a person or an LLM:

1. Inspect `read-pane`. If the immediate caller cannot interpret the screen,
   route the inspection to one that can; do not retry automatically.
2. Answer a menu with `send-keys --key` and the chosen key. Pasted text cannot
   answer it: `--text 2` can be ignored while its appended Enter accepts the
   highlighted option.
3. Inspect again. For intentional text input, pass that read's `pane_sha256`
   as `--expect-pane-sha256`, with the same `--n-lines` if customized. A changed
   pane returns `ErrPaneChanged`; inspect it again before choosing input.

The key and inspected-hash paths pass this hold only; state, ownership and
relay guards still apply. `--no-enter`, empty Enter-only calls and
request-token answers also fall outside this ordinary-text hold. The first
two remain manual operations, not an automated menu-recovery sequence.
None of these operations clears the observation or authorizes replay of
earlier input. A late pre-input refusal can retain a committed native delivery
intent, which remains unresolved; do not send a new request to bypass it.
If any input was already attempted before the error was observed, the result
is `ErrSubmissionUnconfirmed` instead; follow
[Ordinary text submission](#ordinary-text-submission).

A turn Codex starts without a `UserPromptSubmit` hook leaves the row in the
previous turn's state (`waiting` if it ended) until its own `Stop` or
Interrupt; no read ends it.

That move proves none of the turn's permission requests gone. On a relay-on
row that had a request in that turn, a plain `send-keys` and waiting `pause` can
then be refused with `ErrDialogMaybeOpen`. Relay and dialog refusals take
precedence over `ErrCodexTurnErrored`; neither the error observation nor
`--key` bypasses them. Follow [Permission relay](#permission-relay); a matching
inspected pane hash can pass the dialog hold too (to stop the agent, send
`/exit` that way). The rule and its limits are in
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

An ongoing idle episode is shared, but visibility follows each reader's
effective timeout. Omitting `displayed_state` and its detail leaves the
shared episode and `since` intact. See **Clearing** below for a
retained exit. A pre-upgrade binary still running, such as an MCP server,
keeps its old behaviour until restarted.

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
  [fixed selection](#bounded-recovery-on-state-reads): a selected stored
  `idle_screen` row can omit `displayed_state` for this reader's timeout;
  selection is never widened or refilled.
  `--state needs_intelligence` is accepted and matches only a row showing it.
- **Detection happens on checks.** Nothing runs between your calls and
  nothing changes on a timer. With settling off, the shared `idle_screen`
  episode starts at the check that commits its entry; see
  [Unchanged panes](#unchanged-panes) for eligibility.
  Your usual `status` polling is enough; you need not read panes yourself.
  `sweep` also samples eligible rows.
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
  the earlier committed display and detail, subject to this reader's timeout,
  with any applicable [failed attempt](#confirmations). A below-timeout
  matching sample preserves the shared candidate, episode and evidence;
  its no-display confirmation is response-only. A later eligible check can
  try again.
- **Clearing.** When its pane changes or cannot be sampled, or the agent
  moves on, every reader keeps the retained `displayed_state` until an
  eligible check commits the exit after its candidate has held for the
  settle interval. Missing checks or unavailable writes can retain it longer.
  A longer reader timeout alone never clears an episode or cancels an entry
  candidate. `ended`, `missing` and a state a command sets
  show at once. A shown `idle_screen` also ends at once on any Codex turn
  start or ownership change, including a `waiting` row retaining it.
- **Trail.** Each shared committed entry and exit appends one
  `ad.spawn.displayed_state_transition` trail record with `instance_id`,
  `prior_state`, `new_state`, `event` and `ts`; the trail is audit only and a
  failed append changes nothing. Hiding an episode for one reader or preserving
  a peer's candidate emits no transition. Rarely, under heavy store load, a
  check whose write is slow to commit may still show the previous display and detail and
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
  current launch's own pane must span the reader's effective timeout. The
  first starts the period; matching captures keep its start. The span ends at a successful
  capture, including one recorded by another reader or one that updated only
  confirmation evidence. Response time never extends it.
  A different fingerprint restarts it and begins clearing a shown
  `idle_screen` under settling.
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
  settling when an eligible check commits the exit. It never fails the read
  or other rows. The next successful sample starts a fresh period. A read
  that cannot commit skips its sample, a reset included, so a later read may
  continue the committed period.
- **Time.** A sample makes up to three tmux calls (a lookup, a pane listing
  and a capture), each bounded by
  [`query_timeout_ms` or `action_timeout_ms`](configuration.md#timing-settings-tmux).
  Within one call, a tmux server that does not answer a lookup or pane listing
  is not asked again unless that timeout uses up the rest of an ID's
  [`[tmux] sweep_budget_seconds`](configuration.md#timing-settings-tmux): its later rows count as failed
  samples for baseline handling, but their skipped work makes sweep coverage incomplete.

## Ordinary text submission

For an agent busy send does not cover, a nonempty `text` with Enter, without
`key`, `request_token`, `expect_pane_sha256` or a
[busy-send option](#busy-send-options), uses native ordinary delivery where
the launch supports it. A Covered Agent's send is a
[Message](#covered-sends) instead. CR bytes are removed; LF newlines are preserved. Native success returns
`outcome: "submitted"`,
`request_id`, `session_id`, `record_sha256` and, when supplied by the native
record, `record_id`: an exact new user record in this launch's native
transcript proves the normalized text was submitted. Hook activity, a quiet input queue and pane text do not prove it.
This proves submission, not completion of the agent's turn.

Ordinary Codex input also requires that `last_turn_errored` is not observed
as `true`; otherwise it returns `ErrCodexTurnErrored` before input. See
[A Codex turn that ends in an error](#a-codex-turn-that-ends-in-an-error)
for inspection and manual input, including the limits of `--allow-pending`.

Native delivery for uncovered Codex supports Linux with exactly Codex
0.159.2, on fresh launches with an ordinary empty composer. For new input, the row must be
`waiting`, or `pending` with `allow_pending`. A resumed conversation,
positional startup prompt, alternate mode or unsupported agent argument is
outside this contract. Accepted extra arguments are `--model`/`-m`,
`--permission-mode`, `--effort` (each with a value), `--no-alt-screen` and
`--dangerously-skip-permissions`. `--no-alt-screen` is supplied automatically;
callers need not add it for native delivery. Keybinding requirements and
generated settings are in [Ordinary delivery launch settings](settings.md#ordinary-delivery-launch-settings).
When native capability is unavailable, including other versions, platforms,
resumed conversations or unsupported launch arguments, ordinary Codex input
uses one paste followed by one Enter; other agents retain literal text plus
Enter. Paste framing requires the application to have enabled bracketed paste;
it does not establish composer readiness.

After successful pending Codex fallback transport, the call observes the same
launch for up to 20 s for hook-derived `working`, `ask_user` or
`check_permission`: startup hooks can lag accepted input. Pending Claude
fallback retains a 2 s observation after its readiness checks. An earlier
caller deadline or cancellation ends observation sooner. Success returns `{}`
without exact transcript confirmation.
Without that activity it returns `ErrSubmissionUnconfirmed` with
`keys_sent: true`, `submission_confirmed: false` and `row_state`, without
an `outcome`. Read the pane and state instead of resending. Other fallback
success returns `{}`; Cursor's verdict is described below.

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

State, permission-relay, dialog and Codex turn-error guards run before capability
selection or stored-request observation. `ended` and `missing` rows are always refused
with `ErrSpawnNotInteractive`, including retries of existing requests.
Ordinary sends share the [input-admission rules](#manual-input-ownership)
with manual sends and waiting uncovered `pause`.

For a supported native attempt, an Enter without an exact matching transcript
record returns `ErrSubmissionUnconfirmed`, `outcome: "submission_unconfirmed"`.
Agent-director never retries text or Enter.

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
of every earlier turn. Observing an existing request must pass the same guards
as new ordinary input and match its exact launch and native session identity.
Repeating a stored `delivery_request_id` while Codex's `last_turn_errored` is
`true` returns `ErrCodexTurnErrored` instead of reconciling; follow
[Codex turn errors](#a-codex-turn-that-ends-in-an-error).
This observation sends no input. An unresolved
native attempt or unreadable/corrupt stored evidence blocks new input even with
a new ID or when capability becomes unavailable; fallback never resends it. Request IDs
do not give fallback sends native confirmation or deduplication. If a different
request discovers late proof for its predecessor,
it reports `not_started`; re-issue the new request only after the predecessor
has settled.

Native delivery, Codex fallback refusals or paste-transport failures, and
Cursor gateway refusals use these `ErrSubmissionUnconfirmed` details:

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
still means an admission hold remains; inspect it as above. For undelivered
requests, see [Permission relay](#permission-relay).

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

For an agent busy send does not cover, empty text (Enter only), `no_enter`,
`key`, pane-hash sends and permission pane answers remain separate manual
operations; a Covered Agent's send refuses them as
[malformed](#covered-sends). On success they return an empty
object and make no transcript-confirmation claim. Cursor sends with Enter also
check for [gateway refusal](#cursor-input-outcomes). They serialize with ordinary
delivery.

Input admission gives writers priority over proof reads: a writer allows up to
100 ms to gain admission, then up to 2 s for a proof read already in
progress to finish. Later proof reads defer, so repeated reads cannot
overtake an admitted writer. A second writer still refuses if the first
retains ownership through the admission allowance. Caller cancellation caps
both waits. These are admission bounds, not whole-command or filesystem-stall
deadlines; an overlong proof read can still exhaust the wait.

On an input-admission or journal-open failure, `send-keys` returns
`ErrSubmissionUnconfirmed`; waiting uncovered `pause` returns `ErrInternal`.
Both report `outcome: "not_started"`, `phase: "pre_input"`, zero
`bytes_attempted`, false `keys_sent`, `enter_may_have_been_sent` and
`submission_confirmed`, and the last captured `row_state`. Their `reason`
and description retain the admission, lock-wait or storage/open cause;
filesystem errors retain the operation and cause without private paths.
Advice text is a hint, not a machine-readable cause code.

Manual `send-keys` also returns `ErrSubmissionUnconfirmed`/`not_started`
when its journal cannot be read or saved before input. Re-issue a no-input
call after its stated condition clears; let another writer settle first.
A persistent journal refusal needs an operator. A `pause` journal read/save
failure returns `ErrInternal` without input. If its ownership recheck fails after it clears
the input line, it returns `ErrInternal` with only that clear sent: no
`/exit` and no Enter, so the agent is not pausing. Check `get`'s `state`
before calling `pause` again. State, relay and recorded-name guards run
before it acquires input ownership.

After Enter-only or pane-hash text sends with Enter to a `pending` Codex row,
bounded hook observation can instead return `ErrSubmissionUnconfirmed` with
`keys_sent: true`, `submission_confirmed: false` and `row_state`, without a
native `outcome`. Check hook-derived `get` or `status` without resending text
or Enter. If submission remains unknown, use
[Unresolved ordinary input](operator-actions.md#unresolved-ordinary-input).

Manual input, including waiting uncovered `pause`, changes the native composer contract and prevents
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

After a resume moves the row to pending or a reuse resets it, a determinate
create or label failure attempts a conditional restore; this includes a label
failure whose newly created session was ended or may still be running.
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
restoring. A lost create reply returns success without identity even if the
new launch could not be recorded. It does not restore or retry creation.
Check the row and surviving work before any retry. For Cursor,
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
their own rows and lifetimes, including when a lead's session ends. Native
Cursor process replacement and continuation remain unverified.

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
or starts a new conversation. Inspect `get` or `status`'s
[`failure_reason`](#failure-reasons) and the pane if it still exists;
a successful resume result confirms launch, not successful load.
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
Inspect [`failure_reason`](#failure-reasons) and any surviving pane as for
other gateway failures.
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

A structurally valid error reply correlated to the active `session/prompt`
ends only that turn when the runtime is verified alive. Without a pending
rotation or exit, required proof and outcome persistence return the row to `waiting` with
[`failure_reason`](#failure-reasons); the gateway renders the failure and admits
later explicit input. No prompt is automatically retried. A second consecutive
prompt error ends the session. Any valid prompt completion, including
`cancelled`, refusal or a token limit, resets the count; updates, input admission
and conversation rotation do not. A new runtime starts at zero.

The failed submission has a protected `turn_outcome` with `outcome: "failed"`,
its sanitized `reason`, submission/request IDs and cancellation status; no
`stop_reason` is invented. This applies to the second failure too when runtime
liveness is verified. A successful `send-keys` reports admission, not turn
success. Pending permission requests follow the ordinary turn-end proof rules
without inventing a verdict, delivery or execution result. Recoverable failures
emit `ad.cursor.turn_failed`; session-ending failures emit `ad.cursor.failed`
when the gateway has identified its row. The trail remains audit-only.

Other ACP operations, malformed error payloads or frames, uncorrelated replies,
timeouts, transport or reporting failures, changed bindings, and a dead or
unverifiable runtime remain fatal gateway failures.
Each tool proof keeps the original `[store] busy_timeout_ms` window from its
first persistence attempt; retries never renew it. Failure to confirm storage
fails the gateway without establishing either persistence or rollback. The
actual event determines what must finish before the next lifecycle step:

| Event | Required proof persistence and limit |
|---|---|
| Ctrl-C interruption | The completed turn proves its requests gone, answered or not, except those with pending or stored tool proof. Deferred tool proofs are stored before its Stop report; idle-control recovery also stores them before reporting waiting. The first control's three-second deadline bounds this work. |
| `/clear` or `/new` | A completed outgoing turn first proves its requests gone, with the same tool-proof exceptions. Deferred tool proofs must be stored before rotation publication, within the same first-control deadline; failure refuses rotation. |
| Ordinary completion or recovered prompt failure | Turn-end proof has its own fixed store-busy window; deferred tool proofs keep theirs and must be stored before the Stop report. |
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
Gateway failures are asynchronous, not new typed spawn errors.

ACP failure reasons identify the initiating operation (`initialize`, `session/new`,
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
Its initiating sanitized reason is recorded as `failure_reason`, matching
`ad.cursor.failed`, even if a state read marked that same launch `missing`
first. The pane can disappear with the gateway; use the public field rather
than requiring pane or audit-trail access. Failures before runtime registration
can leave a later `missing` row without a recorded reason, even when the trail
contains one. This is not an intentional session end or a successful turn.
Cleanup errors remain failures. Local `/exit` and eligible `pause` can report
termination only after the owned runtime is confirmed gone; a stop failure
cannot report success.

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

For a waiting Claude, Codex or Cursor agent that busy send does not cover,
`ErrPauseTimeout` means `/exit` and Enter
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

### Keyless pause of busy uncovered Codex

Plain `pause` on uncovered Codex in `working`, `check_permission` or
`ask_user` sends SIGTERM to its recorded agent process, checked by pid and
start time. It sends no keys and needs no relay guard, even with an open
permission request. Once the agent is verified gone, it records `ended`,
keeps `session_id` and proves requests `agent_gone`. It needs no SessionEnd
report; Codex sends none on SIGTERM. If recovery marks the same launch
`missing` from that process exit during the pause, completion still records
`ended`. A row already `ended` or `missing` when pause starts is a no-op.

Signal and exit checks use the same [platform limits and refusal
facts](#keyless-pause-of-a-covered-agent) as covered keyless pause:
`ErrSpawnNotPausable` with `cause` and `signal_sent`, using
`agent_process_unverifiable`, `signal_unsupported`, `signal_uncertain`,
`exit_uncertain`, `launch_changed` or `budget_exhausted`. It never returns
`ErrPauseTimeout`. The one `pause.timeout_seconds` budget starts before the
first row read and bounds all remaining shutdown work, with the initial-read
caveat below. Success permits `resume`; a refusal requires the action for its
cause. This path checks the agent's exit, without covered delivery or pause
claims.

Waiting Codex still uses `/exit` under the relay guard. Pending remains
ineligible, except for the [end-report check](#when-a-codex-agent-exits).
`only_if_waiting: true` still refuses uncovered rows with `agent_not_covered`.
Uncovered Claude, Cursor, pane-only and covered rows keep their own rules.

### Keyless pause of a Covered Agent

`pause` on a Covered Agent types nothing, and neither waits for nor sends its
Held Messages: they stay Held for `resume`. A finished row is a no-op success.
In any live state, a running turn or an open dialog included, `pause` sends
SIGTERM to the agent's recorded process, checked by pid and start time. A
[pause claim](#standing-pause-claims) on the launch changes both, and
`only_if_waiting: true` first
[checks that the agent is waiting](#pause-only-if-waiting). It succeeds only
once, within `pause.timeout_seconds`, the agent has exited and
every other process its launch owned is checked gone. Successful verified
shutdown records `ended` and keeps `session_id`; `resume` may then follow. A
row already `missing` for another cause stays `missing`. It never returns
`ErrPauseTimeout`. The budget bounds shutdown work, not the time before a
refusal can return.

If the initial read outlasts the budget while the store is occupied, `pause`
waits until it can read the row. A covered or busy uncovered Codex row then refuses with
`ErrSpawnNotPausable`, `cause: budget_exhausted` and `signal_sent: false`,
with nothing signalled or written. This refusal can arrive after
`pause.timeout_seconds`. Caller cancellation at this point is the bare
context error. Waiting uncovered pause keeps its full post-input wait.

A shutdown refusal returns `ErrSpawnNotPausable` with `err_details` `cause` and
`signal_sent`, whether this call sent the SIGTERM (absent when that is not
known). It can follow a sent signal or recorded exit evidence. A refusal is
never shutdown: neither a sent signal nor an uncertain exit makes a resend
safe or permits `resume`.

On Darwin (macOS), the start-time check immediately precedes the signal, but
a narrow PID-reuse gap remains: a replacement process can be signalled.
Linux binds the signal to the checked process with pidfd. See the
[signal architecture](architecture.md#probe-layer-internalprobe).

| `cause` | Meaning | Next step |
|---|---|---|
| `launch_changed` | The launch was starting, or another launch replaced it. | `signal_sent: false`: retry `pause` once `get` shows the agent `waiting`. `true`: inspect with `get`. |
| `agent_process_unverifiable` | The agent's recorded process cannot be checked; nothing was signalled. | To end the agent, follow the [live-row sequence](#caller-contract) instead. |
| `signal_unsupported` | This system cannot send an identity-checked signal; nothing was signalled. | As for `agent_process_unverifiable`. |
| `signal_uncertain` | The signal call failed; whether it arrived is not known. | Inspect with `get` before retrying. |
| `budget_exhausted` | The one pause budget ran out at a store read/write, before the signal or at the final success check. | `signal_sent: false`: nothing was signalled; retry `pause`. `true`: inspect with `get` before more input, and retry `pause`. Absent, the signal's result is not known: inspect with `get` before more input or another `pause`. |
| `exit_uncertain` | The signal was sent; the agent did not exit in time. | Inspect with `get` before more input. |
| `owned_process_running` | A process its launch owned still runs: after the agent exited, another one, or, when this pause sent no signal because the agent's exit was already known or its launch [had ended](#standing-pause-claims), possibly the agent itself. | Inspect with `get`, and retry `pause` once that process has exited. |
| `owned_process_unverifiable` | The agent exited; a process its launch owned cannot be checked. | Inspect with `get`, and retry `pause` once it can be checked gone. |
| `destination_unfenced` | The agent exited; an open hand-over's destination cannot be fenced. | Inspect with `get`, and retry `pause` once that hand-over has closed. |

At the final success check, cancellation returns a wrapped context error and
budget expiry returns `budget_exhausted`. If the error says exit evidence
was recorded, it stays recorded: inspect with `get` before continuing.

#### Pause only if waiting

`pause` with `only_if_waiting: true` ends a Covered Agent only if it is
waiting with no turn pending, so it never ends a turn that started after the
caller looked. One store transaction decides from the row's raw state and
delivery records, never from the state a
[settle time](configuration.md#settle-time-state) shows or from
`displayed_state`. It refuses with `ErrSpawnNotPausable` for the first of
these that applies:

| `cause` | When | Next step |
|---|---|---|
| `not_waiting` | The raw state is not `waiting`; `err_details.observed_state` holds it. | Retry `pause` once the agent is waiting, or `pause` without the option. |
| `turn_pending` | [`delivery.awaiting_turn_start`](#busy-send-coverage) is above 0, an issued prompt's output is not recorded complete, or a [force](#stored-force-attempts) is `pending`. | Retry `pause` after that turn. |

Either refusal sends, ends and changes nothing, leaves no claim and has no
`signal_sent`. Otherwise the same transaction claims the launch, and the
keyless shutdown above goes on under the claim, within the same budget. From
the claim on, agent-director hands that launch no Message, wake, steer or
force prompt; Held Messages stay Held for `resume`.

Limits:

- A turn the agent starts itself in the instant before it exits, such as
  from input typed at its terminal, cannot be detected or prevented.
- A Claude turn interrupted with Esc ends with no main-agent turn end, so the
  row stays `working` or, after a permission dialog, `check_permission`.
  Input handed into that turn stays counted in
  `awaiting_turn_start`. So does input whose output was recorded too late to
  count at a turn end ([Busy-send coverage](#busy-send-coverage)).
  `not_waiting` or `turn_pending` then repeats until the agent's next turn
  ends or its launch ends. A prompt the [sighting limits](#sighting-limits)
  hide keeps `turn_pending` until launch end.

After an Esc interruption, use the [Claude interruption recovery](#claude-pane-denial-without-a-turn-end).
For other repeating refusals, retry after the agent's next completed turn or
use a plain keyless `pause` followed by `resume`.

#### Standing pause claims

A conditional pause's claim stands until its launch ends with evidence that
the agent exited, or a `resume` or reuse publishes a new launch. Row state,
elapsed time, reads and retries never end it. `kill` establishes that evidence
through the usual [live-row sequence](#caller-contract); a failed `kill`, one
that leaves the agent running, a finished-row `kill` no-op and a refused
`resume` leave the claim as it was. A new launch is judged on its own, never
by an old claim. After a claim ends, `resume` and reuse still
[check every process the earlier launch owned](#launch-end-resume-and-reuse).

While a claim stands, every `pause` of that launch, with or without
`only_if_waiting` and on a finished row too, sends no signal and answers from
the claim:

| The claiming pause | Answer |
|---|---|
| Recorded no outcome and is within its `pause.timeout_seconds` budget | `pause_in_progress`: retry `pause`. |
| Past its budget before recording its intent to signal | None: the claim is released and this `pause` goes on as if none stood. |
| Recorded its intent to signal, no outcome, past its budget | `pause_uncertain` |
| Recorded `paused`: it verified the agent's exit | Success on a finished row; on a live row, see below |
| Recorded `exit_uncertain` | `exit_uncertain` |
| Recorded a failure with a cause | That cause |
| Recorded a failure with no cause | `pause_uncertain` |

Of these answers only `exit_uncertain` carries `signal_sent` (`true`): an
intent alone proves no signal. A recorded answer belongs to the earlier pause,
which returned its own error; it repeats on every retry until the claim ends,
and no retry sends another signal. For `pause_uncertain` and a replayed
refusal, inspect with `get` before more input; `kill` ends the claim once it
establishes the agent's exit. A claiming pause that proves it sent nothing, such as
`agent_process_unverifiable` or `signal_unsupported`, releases its claim, so
the next `pause` starts afresh. A plain `pause` signals only while no claim
stands, so overlapping pauses send at most one SIGTERM per claim.

Once the agent's exit is known, because the claim ended or recorded `paused`,
`pause` with either option sends no signal: of a finished row it is a no-op
success, and of a live row it finishes the shutdown (the launch's end, the
owned-process checks and the row's end) and returns their result.
`only_if_waiting` does the same on a live row whose launch ended with no
claim; a plain `pause` there signals as usual. A row stays live this way when
a pause verified the agent's exit but not every owned process, or could not
record the launch's end, or when a `missing` agent reports in again. With no
signal sent, a process still running at the deadline is
`owned_process_running`, and an agent that cannot be checked is
`agent_process_unverifiable`, both with `signal_sent: false`.

## When a Codex agent exits

A Claude row's clean exit ends the row at once. A Codex agent that exits other
than through `pause`, an `/exit` typed at its pane, leaves its row in its last
state, and so does a crash or a `kill` of either agent. Until something sees
the agent gone, `read-pane` gets `ErrTmuxCaptureFailed`, and `send-keys` to
an agent busy send does not cover gets `ErrTmuxSendKeys`. A Covered Agent's
`send-keys` still accepts its Message ([Covered sends](#covered-sends)); what
then happens to it is in
[Launch end, resume and reuse](#launch-end-resume-and-reuse). Run `sweep`: it marks the row `ended` when the
agent's end was reported, else `missing`, and an `ended` row is in neither of
its lists (see [How a Codex row ends](hooks.md#how-a-codex-row-ends)). Selected
state reads can make the same guarded move through
[shared recovery](#bounded-recovery-on-state-reads).

`pause` on a Codex row ends it itself:

- A busy uncovered row uses [keyless pause](#keyless-pause-of-busy-uncovered-codex),
  including when its agent already exited without an end report.
- A waiting agent exits after the `/exit` and `pause` returns success with the row
  `ended`.
- A remaining live row whose agent already exited with an end report, pending included,
  returns success having sent nothing, and the row is `ended`.
- On the waiting keys path, a gone agent with no end report, or one that
  cannot be judged, gets `ErrTmuxSendKeys` or `ErrPauseTimeout`.
  [Shared recovery](#bounded-recovery-on-state-reads) then settles the row.

If a waiting Codex row has a draft exceeding 64 lines, `pause` submits the
rest of the draft and the `/exit` to the agent as a prompt, the row goes
`working`, and `pause` returns `ErrPauseTimeout`. Follow the
[inspection procedure](#when-pause-cannot-confirm-exit) before further input.

## Launch size and representation

Long launches pass through tmux's stdin transport when needed. The 8192-byte
transport threshold is not a limit on arguments, prompts or templates.
After template resolution and launch composition, preflight checks both the
tmux client invocation and the pane command:

- Each argument and full `KEY=VALUE` environment entry is limited to 131072
  bytes including its terminating NUL (at most 131071 bytes of content).
  Embedded NUL bytes are unrepresentable and refused regardless of length.
- Each total includes strings, NUL terminators, argv/environment pointers,
  an executable-path copy and a conservative 32768-byte reserve. A total at
  or above the runtime budget refuses. Linux uses
  `max(128 KiB, min(6 MiB, soft RLIMIT_STACK/4))`; Darwin uses `kern.argmax`.
  An unavailable budget, including on other platforms, also refuses.

The client uses agent-director's filtered ambient environment. The pane
estimate merges explicit, synthesized and forwarded entries over that
environment as a proxy for the tmux server's. A warm server's different
environment or stack limit is not predicted. Native preparation can add
arguments or environment after preflight; an entry near the cap may then
fail the repeated check at the create boundary.

Early refusal returns `ErrInvalidFlags` for spawn or reuse and
`ErrTmuxSessionCreate` for resume. No launch files, trust entry or row are
written or changed. Size diagnostics name the component, its byte count and
limit, never its value; NUL diagnostics identify representability separately.
`make-template` does not apply this runtime check; its saved values are
checked at launch.

For spawn or reuse, correct the input or ambient/runtime condition before
retrying. For resume, retry only after a correctable ambient environment or
runtime-budget condition changes. A refusal intrinsic to a saved argument or
explicit environment entry cannot be repaired by changing ambient values or
the stack limit; resume cannot replace those saved inputs. Keep the saved row
and history intact. If a fresh conversation is acceptable, spawn a new
instance with corrected inputs; otherwise surface the inability to resume.
Cursor fresh launches must also follow the
[fresh-instance recovery restrictions](#cursor-operation-boundaries).

A late refusal occurs before the tmux create invocation but follows ordinary
launch-failure handling: a new spawn's row stays `pending`; reuse and resume
attempt their conditional restoration. Follow the error's reported outcome.
These checks add no configuration setting or caller-supplied budget.

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
- `ErrCantSend`: the call or its template sets an Agent Default or
  `confirm_from_transcripts` for an agent busy send does not cover
  (`agent_not_covered`), or a `send_mode` the agent does not offer
  (`steer_unsupported`); see [Agent Defaults](#agent-defaults).
- `ErrInvalidFlags`: the composed spawn or reuse fails
  [launch-size or representation preflight](#launch-size-and-representation).
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
    supervisor adds is not refused or removed. Every Codex spawn, reuse and
    resume runs inline with `--no-alt-screen`; a caller copy before the first
    `--` is not added again. Callers cannot select the alternate screen by
    argument. Scrollback may contain an extra startup header (cosmetic).
    Capture contents and retention limits are in
    [`read-pane`](cli-reference.md#read-pane);
    other launch settings are in [Codex's layers](settings.md#codexs-layers).
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

A Covered Agent accepts ordinary Messages while permission or dialog boundaries
are unsafe and holds them; it never types an answer. `decide` keeps its own
authority and lifetime rules. For Claude pane answers use
[direct tmux and outside-answer recording](permissions.md#answering-at-the-pane);
for stopping or reconnecting use [keyless pause](#keyless-pause-of-a-covered-agent)
and [resume](#launch-end-resume-and-reuse). The `send-keys` key, hash,
relay-refusal rules below apply only to uncovered agents; typed-pause rules
apply to waiting uncovered rows. Busy uncovered Codex instead uses
[keyless pause](#keyless-pause-of-busy-uncovered-codex).

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
native prompt. For an uncovered agent, a prompt with no recorded request holds no input,
so a plain `send-keys` with Enter can answer that dialog yes. The dialog hold
cannot cover a request never recorded. For Claude, inspect and use direct tmux.
Claude shows its native dialog while the relay hook runs; that dialog is not evidence that the hook died.

A request identified as a Claude or Codex subagent's remains eligible for
`decide` after the main turn ends, the row moves to `waiting` or `ask_user`,
or a later main turn starts. It must remain open and unproven under the same
launch, life and session, with a live relay hook and its original time limit.
A verdict recorded before the state change can still be acknowledged afterward;
an unanswered request can still receive the hook's timeout deny. Follow its
token with `get-permission`. Presence in `unproven_requests`, a delivery value
or `hook_alive` alone does not establish response eligibility.

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
  for `delivered` or `fallen_back`, subject to your own deadline. Follow the
  [delivery rules](permissions.md#delivery); `confirm_by` alone does not
  establish fallback or permission to answer.
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

An uncovered agent's pane answer (`send-keys --request-token`) sends nothing when it refuses
with:

- `ErrPaneChanged`: the pane changed since you read it. Read it again; the
  error does not carry the new hash. Volatile lines, such as a spinner or a
  clock, can cause it. See the known Codex limit below before retrying on a
  working Codex row.
- `ErrPaneAnswerInProgress`: another pane answer to this request is still
  being sent, or its sender cannot be checked and `not_before` has not
  passed. Do nothing. A sender that died leaves `pane_answer` `intent`, its
  key possibly typed: read the pane again, then answer with the fresh hash
  or use `record-pane-answer`.
- `ErrStoreBusy`: the store's write lock was not taken in time.
- `ErrSendKeysWhileRelayed`: a relay hook of the agent may still answer
  its request: answer the request it names with `decide`, or retry later (a
  plain retry can then get `ErrDialogMaybeOpen`, below). Don't answer or
  decide one `get-permission` shows proven gone `turn_end`. A call naming
  that Cursor request's token still gets this refusal while its delivery is
  `not_confirmed`; see the Cursor turn-end paragraph below.
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
These row-wide holds exclude Cursor requests proven gone at turn end,
including requests of an earlier launch. Token-specific answer eligibility
still applies; see the Cursor turn-end paragraph below.

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
non-Cursor request carrying only `turn_end` proof and no recorded close), or
a current relay hook that is live or whose status cannot be checked. Legacy holds
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
Nothing is sent. Keep the keys and retry later, or use inspected input below.

### Claude pane denial without a turn end

At a Claude Code permission dialog, `Escape` or "No" denies the tool and
interrupts the turn. Claude sends no hook afterward: the row can stay
`check_permission` with no time limit, even though the pane shows an empty
input prompt. Busy send keeps later Messages Held with blocking wait reason
`dialog`; Messages behind an earlier one can report `earlier_message`.
This affects relay off and relay on when the dialog is answered at the pane.
Relay-on `decide --decision deny` and the relay-timeout deny do not interrupt
the turn and are unaffected.

On relay-on rows, recording the pane answer can let a
[recovery check](#lost-end-of-turn-reports) move the row to `working`.
That still supplies no turn end; Messages then wait for the turn to finish.

For a Covered Agent, use a plain [keyless `pause`](#keyless-pause-of-a-covered-agent)
without `only_if_waiting`, then `resume` after verified shutdown. This works
in `check_permission` with Held Messages: it types nothing, ends the agent
and keeps those Messages Held for `resume`. Alternatively, type a prompt at
the terminal that starts and ends a new turn, or wait for the next main-agent
turn end. A busy-send Message cannot start that recovery turn while held.

### Inspected input past a dialog hold

On an uncovered row, have a person or an LLM inspect a normal `read-pane`
and choose the input for that exact screen. Send with its `pane_sha256` as
`--expect-pane-sha256`, without `--request-token`. To answer a dialog whose
request already reads delivered or answered, send only the key just chosen:

```sh
agent-director read-pane --instance-id <id>
agent-director send-keys --instance-id <id> --key <key> --expect-pane-sha256 <H>
```

Use the same `--n-lines` for both calls. A matching hash passes only the
dialog hold; a changed pane is `ErrPaneChanged`. Every other refusal,
including live-relay and unanswered fallback guards, still applies.
For an eligible fallen-back request, use the
[token-specific pane answer](permissions.md#answering-at-the-pane) instead;
token-specific answers refuse delivered or already answered requests.
Covered Claude keeps its verified direct-tmux procedure at that same link.
On Cursor the gateway can still refuse the prompt with `gateway_refused`; follow
[Cursor input outcomes](#cursor-input-outcomes). Never pass
`--expect-pane-sha256` from an automatic flow.

Known Codex limit: after a relay allow, the row usually continues working,
but the request stays unproven until the turn ends. Only `turn_end` or
`agent_gone` proves a Codex request gone; its hooks give no earlier proof.
Until then, plain `send-keys` gets `ErrDialogMaybeOpen`. The inspected way
through is unusable while Codex is working: its status line
(`Working (Ns • esc to interrupt)`, with elapsed time and a spinner) changes
every second. The hash is stale before a person or an LLM can send it, so
the send gets `ErrPaneChanged`. Wait for the turn end (`waiting` or its
`turn_end` proof), then send. A `waiting` state recovered after an error
does not prove requests gone; see
[A Codex turn that ends in an error](#a-codex-turn-that-ends-in-an-error).
To stop a busy Codex agent without waiting for its turn, use
[keyless pause](#keyless-pause-of-busy-uncovered-codex).

A matching hash does not close every race. A key already in flight can land
on a dialog that appears after the last check; Enter can answer it yes.
Inspect the pane before choosing keys, but no hash check prevents this lag.

A relay hook can acknowledge a verdict and die before output. A pane key can
land on an identical Claude or Codex dialog belonging to another request,
or a dialog can appear more than 2 s after its hook ends, after
`record-pane-answer`. Records can then read `delivered` or closed while a
dialog still waits. The affected request stays unproven and holds plain
input; use [inspected input past the hold](#inspected-input-past-a-dialog-hold).
Without someone looking, the dialog can wait indefinitely.

A Claude or Codex agent can crash just before its leftover relay hook outputs
an answer, leaving a recorded acknowledgement without agent receipt. Treat
it as uncertain delivery; never replay it blindly.

### Lost end-of-turn reports

If Claude's `Stop` and idle-prompt Notification are both lost, the row can
stay `working` while idle, and permission holds can keep input waiting until
someone inspects the pane. Use
[inspected input](#inspected-input-past-a-dialog-hold) for an uncovered row,
or [keyless pause](#keyless-pause-of-a-covered-agent) for covered Claude.
[Shared recovery checks](#bounded-recovery-on-state-reads) do not reconstruct
lost hooks or supply missing dialog proof. Relay delivery is derived when a
request is read; it does not wait for a sweep.

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
or pane answer retains its own refusal.

A Cursor request proven gone at turn end no longer holds `send-keys`
without `--request-token` (plain or inspected), nor `pause`, before or after
`confirm_by`. This includes a verdict recorded but not acknowledged before
turn end and a request of an earlier launch. No `decide` or
`record-pane-answer` is needed for that release. A call naming that request's
token still faces its token-specific refusals, including
`ErrSendKeysWhileRelayed` while delivery is `not_confirmed`.
An undelivered request still reads `not_confirmed`
until `confirm_by`, then `fallen_back`; `record-pane-answer` still accepts
a claim only from 2 s past `confirm_by` (`ErrClaimTooSoon` before). Other requests can still
hold input. Claude and Codex turn-end proof does not release their relay
or fallback holds.

A waiting uncovered agent's `pause` is held like a plain `send-keys` without `--expect-pane-sha256`
(`ErrSendKeysWhileRelayed`, `ErrRelayFallenBack`, `ErrDialogMaybeOpen`)
and types nothing; it takes no pane hash. To stop a held agent, see
[Answer a request at the pane](usage.md#answer-a-request-at-the-pane), or
`kill` it.
