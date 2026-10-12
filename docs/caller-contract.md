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
- `resume` or `spawn --reuse-finished` refused as "still stopping" or "still
  starting": wait and retry. Refused with "this row's own id", or
  `ErrTmuxSessionConflict` "this id's own abandoned launch": surface the named
  session to a human; never end it yourself.
- After a timed-out `spawn` or `resume` the row stays `pending`: do not retry
  until `get` shows `ended` or `missing`, then `spawn --instance-id`
  with `--reuse-finished` and the row's agent (`get` shows it). A row noted `unreported` has a running agent: use it
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
6. Once `ended` or `missing`:
   - with a session id, to get the conversation back:
     `agent-director resume --instance-id <id>`
   - otherwise, a reuse (the old conversation is unreachable for good), with
     the row's agent, which `get` shows:
     `agent-director spawn --instance-id <id> --reuse-finished --agent <agent> --cwd <dir>`

   A caller whose ids agent-director mints spawns fresh instead.

## Spawn and template input refusals

Each of these is refused before anything is written: no row, session, trust
entry or template. Fix the input and re-issue the call. How `agent` and a
template's `agent_args` combine is in
[Settings](settings.md#templates-and-per-call-merge).

- `ErrAgentMissing`: the call gives no `agent` and its template names none.
  Name `claude` or `codex` per call (`--agent` on the CLI), or put `agent` in
  the template.
- `ErrAgentInvalid`: the agent is not exactly `claude` or `codex`; the value is
  case-sensitive. Pass one of the two.
- `ErrAgentArgsMismatch`: the template's `agent_args` are for another agent
  than the one this spawn resolves to, so they are not inherited. Pass this
  call's own non-empty `agent_args`, or spawn through a template for that agent.
- `ErrTemplateAgentMissing`: a template holds `agent_args` but names no agent,
  at `make-template` or when a `spawn` loads it. Give the template an `agent`,
  or remove its `agent_args`.
- `ErrPermissionsUnsupported`: a `codex` spawn or template carries an `allow`,
  `deny` or `ask` entry. Drop the lists; see
  [Permissions](permissions.md#codex-spawns).
- `ErrSpawnDeniedFlag`: the `agent_args` hold an argument the supervisor must
  own. Remove it and re-issue; the error names the argument.
  - A `claude` spawn: `--settings`, `--resume`, `--continue`, `--print` and
    `--output-format`.
  - A `codex` spawn: a Codex subcommand or its alias as the first word
    before any `--` that is neither an option nor an option's value (`exec`,
    `resume`, `fork`, `login` and the rest). The `--` meant is one inside
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
  - `resume` never refuses the arguments it replays, for either agent: a
    stored argument that a `spawn` would now refuse still runs.

## Permission relay

- A relayed request's `decision` is the verdict recorded, not the outcome:
  read `delivery`. Pass `decide` a `--max-wait-ms` of at most your deadline
  minus 1 s (see [Intercept permission prompts](usage.md#intercept-permission-prompts)).
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

While a request of the agent is fallen back with no pane answer recorded, a
plain `send-keys` (no `--request-token`) is refused with
`ErrRelayFallenBack`, whose `err_details` name that request and the agent's
other open requests: close it with a pane answer or with
`record-pane-answer`. While a relay hook of the agent may still answer,
every `send-keys` is refused with `ErrSendKeysWhileRelayed`.

After a permission request, a plain `send-keys` without
`--expect-pane-sha256` is refused with `ErrDialogMaybeOpen` until Claude
Code shows the request's dialog gone: after an allow, once its tool has
run; after a deny, once the agent's turn ends. Nothing is sent. Its
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
