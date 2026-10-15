# Caller contract

Rules for programs and agents that call agent-director. The class of every tmux
error:
[tmux refusal classes](architecture.md#caller-contract-tmux-refusal-classes).

- Only a GONE answer means a row's session is not there. Any other tmux error
  means "don't know", never "dead".
- Row state, kept honest by `find-missing`, is the liveness authority. Poll a
  launch with `status`; call `read-pane` only while the row is `pending`, at
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
- `send-keys` returning `ErrTmuxUnresponsive` with "may have been delivered" or
  "may be typed": do the step the error names, never resend the same text.
  `--text ""` sends Enter only. Retry a `pause` that returns
  `ErrTmuxUnresponsive` later.
- For Claude and Codex, `resume` or `spawn --reuse-finished` refused as "still stopping" or "still
  starting": wait and retry. Refused with "this row's own id", or
  `ErrTmuxSessionConflict` "this id's own abandoned launch": surface the named
  session to a human; never end it yourself.
- After a timed-out `spawn` or `resume` the row stays `pending`: do not retry
  until `get` shows `ended` or `missing`, then `spawn --instance-id`
  with `--reuse-finished` and the row's agent (`get` shows it), for Claude or
  Codex. Cursor instead uses the [fresh-instance boundary](#cursor-operation-boundaries). A row noted `unreported` has a running agent: use it
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
of exit. `missing` is the sweep's judgement on available evidence, not an
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
   [fresh-instance boundary](#cursor-operation-boundaries). For Claude or Codex:
   - with a session id, to get the conversation back:
     `agent-director resume --instance-id <id>`
   - otherwise, a reuse (a fresh conversation with no memory of the old one), with
     the row's agent, which `get` shows:
     `agent-director spawn --instance-id <id> --reuse-finished --agent <agent> --cwd <dir>`

   A caller whose ids agent-director mints spawns fresh instead.

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

If a failed reuse restores the row, its previous agent and life become current
again, with their history and records. Deleted permission requests stay
deleted, and records closed by the attempt stay closed.

## Cursor operation boundaries

Cursor supports fresh instances only. `resume` returns
`ErrSpawnNotResumable`; `spawn --reuse-finished` involving Cursor, including
switching to or from it, returns `ErrSpawnDeniedFlag`. These refusals precede
row or launch changes. Start a fresh unique instance instead; there is no AD
resume or reuse recovery for the Cursor conversation.

Only model selection (`--model`, `-m`, with a separate or `=` value) and a
single initial prompt are accepted in Cursor `agent_args`. Unsupported
arguments return `ErrSpawnDeniedFlag` before writing or launching. Cursor
permission-list refusals are in [Permissions](permissions.md#cursor-spawns).
Busy follow-ups and `/clear` or `/new` are refused visibly in the pane,
without forwarding. A successful `send-keys` means keys were sent, not that
the gateway admitted a prompt; inspect the pane when admission is uncertain.

`spawn` returns before the runtime confirms startup. Missing programs,
unsupported platforms, failed lifetime protection or unavailable ownership
evidence cannot produce a ready report; a launch may remain `pending` until
`find-missing` observes its absence. Runtime/protocol/reporting failures emit
`ad.cursor.failed` with a reason when the gateway has identified its row.
Examples include an ACP operation timeout, unsupported ACP protocol,
changed runtime/session binding, a refused report or transport failure.
These are asynchronous launch failures, not new typed spawn errors.

Pipe EOF is a transport failure, not proof of child death. Unexpected runtime
loss stops the gateway and follows the existing `missing` sweep outcome.
Local `/exit` and eligible `pause` can report termination only after the
owned runtime is confirmed gone; a stop failure cannot report success.
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
claim unrecorded history. A partial transcript does not establish successful
input execution or termination.

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
`working`, and `pause` returns `ErrPauseTimeout`.

The look-and-send recovery for a held agent (send `/exit` with the pane's hash,
poll `status` until `ended`) gains, on a Codex row, one step: once the agent has
exited, run `find-missing` (or `pause`).

## Spawn and template input refusals

Each of these is refused before anything is written: no row, session, trust
entry or template. Fix the input and re-issue the call. How `agent` and a
template's `agent_args` combine is in
[Settings](settings.md#templates-and-per-call-merge).

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
  `ErrPaneChanged`, `ErrDialogMaybeOpen`, and an `ErrInternal` from a pane
  answer whose key was sent (`key_sent` true: read the pane before you
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
- `ErrStoreBusy`: the store stayed busy for longer than `--max-wait-ms`
  (another agent-director process held it, or another call in the same
  process did). Retry.
- `ErrAlreadyDecided`: a verdict is already recorded (first `decide` wins, and
  the relay hook records a deny of its own when its time runs out,
  `decision_reason` `timeout`), or the request was answered at the pane.
- `ErrNoOpenPermissionRequest`: no open request by that token, or it is
  closed. Do not answer it at the pane. A request still awaiting an answer
  when its agent ended, or when `find-missing` marked it `missing`, is
  closed, and stays closed after `resume`.

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

`pause` is held like a plain `send-keys` without `--expect-pane-sha256`
(`ErrSendKeysWhileRelayed`, `ErrRelayFallenBack`, `ErrDialogMaybeOpen`)
and types nothing; it takes no pane hash. To stop a held agent, see
[Answer a request at the pane](usage.md#answer-a-request-at-the-pane), or
`kill` it.
