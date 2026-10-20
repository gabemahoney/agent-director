# Caller contract

Rules for programs and agents that call agent-director. The class of every tmux
error:
[tmux refusal classes](architecture.md#caller-contract-tmux-refusal-classes).

- Only a GONE answer means a row's session is not there. Any other tmux error
  means "don't know", never "dead".
- Row state, reconciled by `find-missing` and the [Cursor read checks](#cursor-operation-boundaries),
  is the liveness authority. For launch readiness, poll with `status`;
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
- `ErrSubmissionUnconfirmed`: for automatic `send-keys`, follow
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

A pending row noted `unreported` has an alive agent with no applied hook
since launch. Read its pane, then type with `allow_pending` only after a
person or an LLM judged that screen. A caller that cannot judge it uses the
live-row sequence or hands it to a human. Cursor admits no prompt until its conversation is confirmed; `allow_pending`
does not override that admission rule. Hook configuration diagnosis is
in [Operator actions](operator-actions.md#a-row-stays-pending-and-the-trail-shows-pid_mismatch).

To end a live row (`pending` included) and relaunch its id:

1. `agent-director kill --instance-id <id>`; on an error follow its
   class.
2. A `pending` row: wait until its `launch_started_at` (in `status`) plus
   `pending_grace_seconds` (ask the operator for the value in force, see
   [timing settings](configuration.md#timing-settings-tmux)) has passed. Inside
   the grace period, wait and check later; never escalate.
3. Run `agent-director find-missing`, then check with `status` that the row is
   `ended` or `missing`; if not, wait about 5 s and repeat, up to three runs.
4. Still live: `kill` once more, wait about 5 s, run `find-missing`, check.
5. Still live: stop and escalate to a human.
6. Once `ended` or `missing`, Cursor uses the
   [resume or fresh-instance boundary](#cursor-operation-boundaries). For Claude or Codex:
   - with a session id, to get the conversation back:
     `agent-director resume --instance-id <id>`
   - otherwise, a reuse (a fresh conversation with no memory of the old one), with
     the row's agent, which `get` shows:
     `agent-director spawn --instance-id <id> --reuse-finished --agent <agent> --cwd <dir>`

   A caller whose ids agent-director mints spawns fresh instead.

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
input, output and exit status. Unresolved settings, including JSONC or
ambiguous ancestor Git local settings, leave native callbacks unchanged and
context unknown. Unqualified configurations remain unknown.

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
it can still show `waiting` briefly after work begins.

Family reporting adds no liveness detection. A stopped agent still recorded
as `working` keeps the raw family working until an existing detector or
state change records otherwise. Cursor's existing per-agent read projection
can show `missing` before its terminal write succeeds; family reporting
continues to use stored states until that write commits.

Inspect each agent's own state and requests for permission handling and
follow each verb's existing preconditions. An ancestor's family value
does not forward a descendant's permission details. Existing read failures
remain errors; never treat a failed read as `waiting` or invent a third
family value. Reading a family value adds no wait for settling or writer
lock acquisition.

## Stuck rows

A row reads `state` `stuck` with `stuck_reason` `permission_unanswered` while
its agent's own permission prompt has stayed unanswered past
[the timeout](configuration.md#permission-unanswered-timeout-state). Which
requests count is in
[permissions](permissions.md#stuck-on-an-unanswered-permission). Stuck is a
report only: it changes no refusal, hold or guard, and `send-keys`, `decide`
and `pause` judge the row's real state. How the row counts toward
`family_state` is in [Family state](usage.md#family-state).

A request recorded before the upgrade to schema v14, in the same second as or
before the row entered `check_permission` (or with an unknown interval start),
never qualifies; its record, hold and clock are kept.

- **Fields.** `status` and `get` carry `state`, `stuck_reason`, `since`
  (RFC3339 UTC with exactly millisecond precision), `request_token` and
  `tool_name`, the last two naming the request the row reports. `status` adds
  `tmux_session_name` only while the row is stuck (`get` carries it always).
  A `list` row carries `stuck_reason` but not `since`, `request_token` or
  `tool_name`; call `get` for them.
- **Omissions.** Outside `stuck`, these fields are absent, never `null` or
  empty. Test for presence, not for a value.
- **Filtering.** `list --state stuck` matches the state `list` shows, before
  `--limit` is applied, so a limit never hides a stuck row behind others. A
  row displayed `stuck` no longer matches `--state check_permission`; filter
  on `stuck` to see it. A polling loop that tests for `check_permission`
  therefore stops seeing the row once it reads `stuck`.
- **Detection happens on reads.** Nothing runs between your calls: a row
  becomes stuck on the first `status`, `get` or `list` after the timeout, so
  your usual polling of `status` is enough and no pane polling is needed.
  With a [settle time](configuration.md#settle-time-state), entering and
  leaving `stuck` must hold that long, so it shows up to that much later;
  until an entry has held, the row shows its ordinary settled state.
- **`since`.** The time of the read that first showed `stuck`, not the moment
  the timeout ran out. It is kept while the row clears under settling and
  when a different request becomes the one reported. A row's new unbroken stay
  in `check_permission` starts a fresh episode: it never inherits the earlier
  one's `since` or request, and the earlier one ends first.
- **Skipped writes.** A read writes only to commit something new: a request's
  fallback clock newly established, a change of the displayed state or its
  settling candidate, or a change of the request reported. Otherwise it writes
  nothing, a repeated read of a row already displayed `stuck` included. Its
  write never waits for the store's lock: a read that cannot commit returns
  the state and detail committed before, together, and a later read tries
  again.
- **Clearing.** The row returns to its real state when no request qualifies
  or the agent moves on, after the settle time. `ended`, `missing` and a state
  a command sets (including `find-missing`'s repair of a stale
  `check_permission` row) show at once. Each entry and exit appends an
  `ad.spawn.stuck_transition` trail record with `instance_id`, `prior_state`,
  `new_state`, `stuck_reason` and `ts`; the trail is audit only and a failed
  append changes nothing.
- **Attaching.** Treat `stuck` as a request for a deliberate human or caller
  choice at the agent's own prompt. Read the row's `tmux_socket` and
  `tmux_session_name` with `get` and attach as in
  [Usage](usage.md#fresh-cursor-use), which works for every agent, or read the
  request with `get-permission` and its `request_token` and follow
  [Permission relay](#permission-relay); the report adds no verb and no
  automatic answer.

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
resumed conversations or unsupported launch arguments, `send-keys` retains
literal text delivery followed by one Enter. Success returns an empty object
and makes no exact transcript-confirmation claim. A pending Codex row is
observed for up to 2 s for hook-derived `working`, `ask_user` or
`check_permission` state. If that activity is not observed, the call returns
`ErrSubmissionUnconfirmed` with `keys_sent: true`,
`submission_confirmed: false` and `row_state`; it never resends input.

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
client timeout long enough to receive the result. A client timeout does not
cancel a submission.

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
native attempt blocks new input even with a new ID or when capability becomes
unavailable; fallback never resends an unresolved native attempt. Request IDs
do not give fallback sends native confirmation or deduplication. If a different
request discovers late proof for its predecessor,
it reports `not_started`; re-issue the new request only after the predecessor
has settled.

For native delivery, `ErrSubmissionUnconfirmed` carries these `err_details`:

| Field | Meaning |
|---|---|
| `outcome` | `not_started`: this request attempted no input; `partial_input_unconfirmed`: text or manual input may be present, with no automatic Enter attempted; `submission_unconfirmed`: Enter may have been sent and submission remains unknown. |
| `request_id`, `phase` | The owned attempt, when allocated, and its last delivery phase. An earlier unresolved attempt can be reported instead of the requested new input. |
| `bytes_attempted`, `enter_may_have_been_sent`, `keys_sent` | Conservative input-attempt facts, not byte receipts. `keys_sent` means input was attempted and may have reached the pane. |
| `last_queue` | Optional last terminal queue sample; zero is not submission evidence. |
| `submission_confirmed`, `row_state`, `reason` | Confirmation is false; state and reason describe the observation, not proof of failure. |

A `not_started` request can be re-issued after its stated condition clears.
For partial or unconfirmed input, never automatically repeat text, Enter or
editing keys: input may still submit later. For a native attempt, re-issue the
same request only to observe proof while the guards allow it; if it remains
unresolved, escalate to a human using
[Unresolved ordinary input](operator-actions.md#unresolved-ordinary-input).
Agent-director never retries text or Enter on missing evidence, including
after a crash.

## Manual input ownership

Empty text (Enter only), `no_enter`, `key`, pane-hash sends and permission
pane answers remain separate manual operations. They return an empty success
object and make no transcript-confirmation claim. They serialize with ordinary
delivery. Manual `send-keys` can return `ErrSubmissionUnconfirmed`
with `outcome: "not_started"`, `keys_sent: false` and
`enter_may_have_been_sent: false` when exclusive input ownership is unavailable
or its delivery journal cannot be read or saved. That call attempted no input;
re-issue it after the stated condition clears. If another request still owns
the writer, let it settle first; a persistent journal refusal needs an operator.
For `pause`, the same ownership or journal failures return `ErrInternal`
before any input is attempted. State, relay and recorded-name guards run
before it acquires input ownership.

After Enter-only or pane-hash text sends with Enter to a `pending` Codex row,
bounded hook observation can instead return `ErrSubmissionUnconfirmed` with
`keys_sent: true`, `submission_confirmed: false` and `row_state`, without a
native `outcome`. Check hook-derived `get` or `status` without resending text
or Enter. If submission remains unknown, use
[Unresolved ordinary input](operator-actions.md#unresolved-ordinary-input).

Manual input, including `pause`, changes the native composer contract and prevents
further new native delivery on that launch. A pane without native capability
retains its literal input path after manual input. A state, permission or pane refusal
from `pause` does not change that contract. Manual input does not clear an
unresolved attempt. Direct terminal writers must also be excluded during an
automatic request; agent-director's lock cannot serialize them.

## Transcript recovery

For Claude and Codex, `resume` continues only the row's current session with its recorded agent.
Cursor is covered by [Cursor operation boundaries](#cursor-operation-boundaries).
It never selects an older session from `prior_sessions`, even if its file
exists. No current `session_id` gives `ErrNoSessionId` before transcript
lookup. Otherwise, Claude tries the current `jsonl_path` and, after any stat
failure or when no path is recorded, recomputes the current session's path
from the row's cwd and config directory. Codex tries only its current recorded
path. The refusals write and launch nothing.

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
`ErrNoSessionId`. Missing, unreadable or insecure current protected transcript
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
neither. Replayed updates cannot establish readiness, new requests, decisions,
choices or execution proof, and no transient input is recovered.

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
[Fresh Cursor use](usage.md#fresh-cursor-use). A successful `send-keys`
means keys were sent, not that the gateway admitted a prompt. Literal text
and Enter are separate operations; concurrent callers have no atomic
submission guarantee. Inspect the pane and the
[protected transcript](usage.md#cursor-transcripts) when admission or delivery
is uncertain; do not blindly resend an uncertain submission.

On an explicit capacity refusal, wait for capacity and explicitly submit the
refused text again. Refused input is not queued for later execution and does
not replace accepted work.

During rotation, a provisional holding-capacity refusal likewise requires a
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
`find-missing` or a public read confirms gateway loss. Runtime/protocol/reporting
failures emit `ad.cursor.failed` with a reason when the gateway has identified its row.
Examples include an ACP operation timeout, unsupported ACP protocol,
changed runtime/session binding, a refused report or transport failure.
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

For a live Cursor row, public `get` and `status` check only that row's recorded
gateway process identity. Public `list` checks live Cursor candidates within its
label, parent, working-directory and tmux-session filters before applying the
settled-state filter and limit. Invalid filters are refused before any store
read or mutation. A reused PID with a different start time means the
recorded gateway is gone. A confirmed lost gateway is reported as `missing`
without waiting for `find-missing` or the state settle delay, even if its runtime
is still alive. Alive or unknown process evidence leaves the row unchanged.
The result is guarded by the examined launch and runtime binding. A gateway
lost before runtime registration can also be reported missing while no binding
exists for that instance; a stale binding is not absence. A replacement launch
retains its own state. An unresolved conflict on the same dead launch returns
an error rather than reporting it as working.

The complete read result is assembled before a separate attempt to persist
terminal closure. That write does not wait for the store's write lock and keeps
its fixed one-second allowance, capped by the caller's remaining budget. A busy
store or deadline reached by this write alone can still return the guarded
`missing` result while the caller's context remains valid. Other write errors,
including failed request closure, read errors and caller cancellation or deadline
exhaustion return an error with empty results. There is no automatic retry.
A successful read does not confirm whether terminal closure committed.

Only state is projected: `ended_at` and other timestamps remain the stored
facts read, so the first missing result may omit `ended_at` even if closure
subsequently commits. Terminal permission lists are empty; they prove no request
gone, decision, receipt or delivery. Terminal closure leaves Cursor request proof
unchanged, including after `delivered`; see
[Cursor permission lifetimes](permissions.md#cursor-spawns).
Finished rows are not checked again. These reads do not sweep outside their
requested scope or change other agents' read behavior; explicit `find-missing`
remains available.

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
`ErrTmuxCaptureFailed`. Run `find-missing`: it marks the row `ended` when the
agent's end was reported, else `missing`, and an `ended` row is in neither of
its lists (see [How a Codex row ends](hooks.md#how-a-codex-row-ends)).

`pause` on a Codex row ends it itself:

- Its agent exits after the `/exit` and `pause` returns success with the row
  `ended`.
- A live row whose agent already exited with an end report, in any live state,
  returns success having sent nothing, and the row is `ended`.
- A gone agent with no end report, or one that cannot be judged, gets today's
  outcomes: `ErrTmuxSendKeys` for a waiting row's keys, or `ErrPauseTimeout`.
  `find-missing` then settles the row.

If a live Codex row has a draft exceeding 64 lines, `pause` submits the
rest of the draft and the `/exit` to the agent as a prompt, the row goes
`working`, and `pause` returns `ErrPauseTimeout`. Follow the
[inspection procedure](#when-pause-cannot-confirm-exit) before further input.

The look-and-send recovery for a held agent (send `/exit` with the pane's hash,
poll `status` until `ended`) gains, on a Codex row, one step: once the agent has
exited, run `find-missing` (or `pause`).

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
    under `hooks`, `tui.disable_paste_burst` or `features.hooks`;
    and `--disable hooks`. While `disable_ask_user` is on, also
    `--enable default_mode_request_user_input` and `-c` keys at, above or under
    `features.default_mode_request_user_input` or
    `tools.experimental_request_user_input` (see
    [Permissions](permissions.md#disable_ask_user-config)). Every spelling of
    a value is read (the next word, after `=`, or attached to a short flag),
    and an option is refused even after the `--`. A repeat of a flag the
    supervisor adds is not refused or removed: when the arguments already
    carry `--no-daemon`, `--dangerously-bypass-hook-trust` (pre-trust `ok`) or
    `--enable write_stdin_approval` before any `--`, the supervisor adds no
    second one, and Codex itself rejects a flag the arguments repeat (see
    [Codex's layers](settings.md#codexs-layers)).
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

## Permission relay

For Cursor, use the same request token, decision and delivery fields.
`hook_alive` is null and no per-request hook process is claimed; the fixed
`confirm_by` ends uncertainty independently of gateway liveness. Its precise
bounds and once-only approval mapping are in
[Cursor permissions](permissions.md#cursor-spawns).

Every Cursor relay reply, including timeout denial, requires its own durable
acknowledgement. Failed or missing acknowledgement means no relay answer is
sent to Cursor. Separately validated cancellation or policy rejection is a
distinct outcome. `delivered` can remain recorded after a crash without Cursor
receipt; it is not execution or dialog-closure proof. Never resend an uncertain
approval as though known absent. See [Cursor permissions](permissions.md#cursor-spawns)
for delivery, proof and request lifetime rules.

For fallback, inspect a connected current menu and make a fresh deliberate
choice for that request. `fallen_back` alone does not guarantee an answerable
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
- `get`, `list` and `get-permission` never wait for the store's write lock.
- With a [settle time](configuration.md#settle-time-state) set, `status`,
  `get` and `list` show the settled state, up to the settle time behind, and
  nothing marks it as late; verbs act on the live state, so a verb can
  refuse, with a named error, on a state `status` does not show yet.
- Some errors carry their facts in an `err_details` object beside
  `err_name` and `err_description` (in the CLI's error JSON, the MCP
  error's `data`, and the TypeScript client's `errDetails`):
  `ErrRelayFallenBack`, `ErrPaneAnswerInProgress`, `ErrClaimTooSoon`,
  `ErrPaneChanged`, `ErrDialogMaybeOpen`,
  [`ErrPauseTimeout`](#when-pause-cannot-confirm-exit), and an `ErrInternal`
  from a pane answer whose key was sent (`key_sent` true: read the pane before you
  send anything again). Read facts from it, never from the description; a
  caller that does not use it ignores it.

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
  deliberate call with a fresh budget.
- `ErrAlreadyDecided`: a verdict is already recorded (first `decide` wins, and
  the relay hook records a deny of its own when its time runs out,
  `decision_reason` `timeout`), or the request was answered at the pane.
- `ErrNoOpenPermissionRequest`: no open request by that token, or it is
  closed. Do not answer it at the pane. A request still awaiting an answer
  when its agent ended, or when `find-missing` marked it `missing`, is
  closed, and stays closed after `resume`. A decided request without a
  relay acknowledgement or completed pane answer is also closed when its
  agent ends or is marked missing; its recorded verdict is preserved.

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
  error does not carry the new hash.
- `ErrPaneAnswerInProgress`: another pane answer to this request is still
  being sent. Do nothing.
- `ErrSendKeysWhileRelayed`: a relay hook of the agent may still answer
  its request: answer the request it names with `decide`, or retry later
  (a plain retry can then get `ErrDialogMaybeOpen`, below).
- `ErrAlreadyDecided` or `ErrNoOpenPermissionRequest`: the request is no
  longer open.

`record-pane-answer` returns `ErrClaimTooSoon` while the relay hook may still
answer or has not been gone for 2 s yet, so the prompt may not be drawn:
retry at `err_details.not_before` (null only while the hook is seen running).

On relay-on rows, while a request is fallen back with no pane answer recorded, a
plain `send-keys` (no `--request-token`) is refused with
`ErrRelayFallenBack`, whose `err_details` name that request and the agent's
other open requests: close it with a pane answer or with
`record-pane-answer`. While a relay hook of the agent may still answer,
every `send-keys` is refused with `ErrSendKeysWhileRelayed`.

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

On relay-on rows, after a permission request, a plain `send-keys` without
`--expect-pane-sha256` is refused with `ErrDialogMaybeOpen` until the agent's
hooks show the request's dialog gone. Follow the request's proof and delivery
fields; completing or interrupting the main turn does not release a child or
unknown-origin request, even when its child id is null. See
[The dialog hold](permissions.md#the-dialog-hold). A Cursor row can stay
`check_permission` across working, Stop and Interrupt reports while a genuine
unanswered, unproven request holds it. Readiness requires the hold to resolve.
Nothing is sent. Its
`err_details` name the oldest such request, how agent-director's records
say it closed and since when, and the agent's other such requests. Keep
the keys and retry later. If it is still held after some seconds, have a
person or an LLM read the pane and, having looked, send with that read's
`pane_sha256`: a matching hash passes this hold (every other refusal still
applies), and a changed pane is `ErrPaneChanged`. Never pass
`--expect-pane-sha256` from an automatic flow.

Late matching Cursor execution proof resolves only that request's hold; it does
not immediately write `waiting` or replay a prior turn-end report.

`pause` is held like a plain `send-keys` without `--expect-pane-sha256`
(`ErrSendKeysWhileRelayed`, `ErrRelayFallenBack`, `ErrDialogMaybeOpen`)
and types nothing; it takes no pane hash. To stop a held agent, see
[Answer a request at the pane](usage.md#answer-a-request-at-the-pane), or
`kill` it.
