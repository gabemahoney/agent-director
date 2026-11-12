# Permissions

agent-director layers per-spawn permissions over the agent's own controls.
Claude Code merges its permission rules; Codex and Cursor use their own
permission policies. For refusal handling and recovery, see the
[caller contract](caller-contract.md#permission-relay).

## The three arrays

A Claude spawn's `permissions` block accepts three string arrays:

- `allow`: patterns the agent may execute.
- `deny`: patterns the agent must not execute. Denies override allows.
- `ask`: patterns that require permission confirmation.

Use [Claude Code's permission-rule grammar](https://docs.claude.com/en/docs/claude-code/iam#permission-rules).
A bare tool name matches every invocation of that tool. `disable_ask_user`
controls the question tool, not permission confirmations from `ask`.

Codex and Cursor accept empty arrays, but not entries in these arrays.
Use their native permission controls instead. Input refusals, including
`ErrPermissionsUnsupported`, are in the
[caller contract](caller-contract.md#spawn-and-template-input-refusals).

## Cursor spawns

Cursor denies questions with either value of `disable_ask_user`. This includes
direct questions and questions presented as permission requests. The agent
never waits in `ask_user` for a question answer through agent-director.

Supported permission and blocking plan requests use `check_permission`,
`get-permission`, the permission fields of `get`/`list`, and `decide`.
Each request has its own `request_token`. Select it by its content, not
by menu order. An independent child's requests belong to that child's row;
parent association grants no approval authority.

For Cursor, `tool_name` is the supplied action title or plan name,
`tool_use_id` is its `toolCallId`, and `tool_input` is the complete supplied
params JSON, unchanged, including options and absent versus null fields.
A plan retains its supplied name, overview, plan and optional fields;
agent-director adds no option list or session field.

`decide --decision allow` selects allow-once, or accepts the plan once;
`deny` rejects it. Neither creates a persistent rule. A persistent permission
choice requires deliberately selecting that offered option at the pane.
Read the displayed choices and the
[shared-policy limits](multi-account.md#cursor-configuration-and-data-directories).

With relay `on`, the decision window is `relay.timeout_seconds` from
admission. `confirm_by` is fixed at that deadline plus four seconds.
Caller restarts do not extend it. Cursor has no per-request hook process;
`hook_alive` and per-request hook process fields are null.

With relay `off`, requests immediately read `fallen_back`, connected pane
choices are available, and `decide` is unavailable. Native choices last
30 minutes from admission with relay off, or from relay settlement with
relay on. An attached choice uses the displayed request-specific key once,
without Enter. It competes with an API decision for that request.

Requests retain their admitted conversation.
[Conversation rotation](usage.md#fresh-cursor-use) and resume do not transfer
choices or request identities. Replayed requests provide no permission,
execution proof or readiness.

**A turn's end.** When a prompt turn completes, including cancellation,
or `/clear` or `/new` ends it, requests of that conversation the gateway
never answered are proven gone with `proven_gone_how` `turn_end`.
They remain undelivered and may still read as awaiting an answer, but no
longer hold the dialog or row. Their pane keys are removed and no new
verdict can be recorded. Answered requests keep their
[dialog hold](#the-dialog-hold). Relay and fallback holds are separate;
their handling is in the [caller contract](caller-contract.md#permission-relay).

Matching execution evidence can prove one request gone with
`proven_gone_how` `tool_ran`. Shell proof requires a completed or failed
update for the exact invocation, an integer exit code, and non-null string
stdout/stderr. MCP proof requires a completed update whose `rawOutput`
contains exactly `{"success":true}` or `{"error":"Tool execution error"}`.
The exact failed MCP invocation proves only dialog closure, not execution
or success; it preserves the decision, reason, delivery and pane-answer fields.
Other validated execution preserves an existing verdict and non-null reason;
it infers allow only when undecided and fills a missing reason with `tool_ran`.
Generic completion and plan acceptance supply no such proof.

Ending a Cursor launch leaves its proof fields unchanged. After resume,
requests of an earlier launch hold neither input nor pause and are omitted
from `unproven_requests`. A request recorded by an older binary with no
launch on record still holds. For disconnects, uncertain delivery,
cancellation and proof persistence, follow the
[caller contract](caller-contract.md#cursor-operation-boundaries).

## Codex spawns

Set Codex permission rules in the `rules/` folder of the group's Codex folder.
Approval and sandbox options, and the hook-trust bypass, pass through
`agent_args` unchanged.

Each Codex launch enables `write_stdin_approval`: input to a command running
outside its sandbox asks for approval. Input to a command inside the sandbox
is never relayed. Pass `--disable write_stdin_approval` to turn the feature off.

"Always allow" at the pane adds a rule for the whole group's Codex folder.
Group folders are not a security boundary against agents that can call
agent-director. See [pane approval keys](#answering-a-codex-approval-at-the-pane).

## Per-tier concatenation

Claude Code concatenates each tier's `permissions.allow`, `deny` and `ask`
arrays, lower-precedence tier first. Higher tiers add entries, not replace them.

Tiers, lowest precedence first:

1. `~/.claude/settings.json` (user)
2. `.claude/settings.json` (project, in the spawn's cwd)
3. `.claude/settings.local.json` (local)
4. agent-director's `--settings` (per-spawn)
5. Managed policy (organization)

For example, `--deny "Bash(npm publish)"` adds that entry to the spawn's
effective denies. Existing user and project denies still apply.

## `disable_ask_user` config

To turn off the question tool for Claude and Codex launches:

```toml
[defaults]
disable_ask_user = true
```

The default and configuration reload rules are in
[Configuration](configuration.md#keys). Cursor denies questions with either value.

**Claude.** When true, agent-director adds `AskUserQuestion` to
`permissions.deny`. Claude Code denies every use and removes the question
tool from its deferred-tool registry. This deny is additive: caller, user
and project rules still apply. There is no per-spawn opt-out. Change the
setting for subsequent launches or use a separate home and tmux server below.

**Codex.** The current setting applies at each spawn, reuse and resume:

- When true, the question tool is off regardless of user, group, profile
  or project configuration. Asynchronous questions are denied too; the
  agent must put its question in its reply.
- Spawn arguments cannot re-enable it or override its configuration keys.
  The [caller contract](caller-contract.md#spawn-and-template-input-refusals)
  describes `ErrSpawnDeniedFlag`.
- Resume uses the current setting over stored arguments without refusing
  those arguments.
- When false, agent-director adds no restriction. Codex configuration
  controls the tool. A waiting question leaves the row `working`, not
  `ask_user`.

### One Spawn under another `HOME` or store

An alternate `HOME`, `--home`, `spawn --store-path`, or the TypeScript
client's `home` and `storePath` options alone do not isolate a spawn's
configuration or store. The caller and the pane's hooks must use the same
store. A running tmux server supplies the pane's `HOME`; `HOME` alone does
not select another tmux server.

Use a separate tmux server for another home. Create its `TMUX_TMPDIR`,
unset `TMUX`, and do not pass `--store-path`:

```sh
mkdir -p /path/to/alt-tmux
env -u TMUX HOME=/path/to/alt-home TMUX_TMPDIR=/path/to/alt-tmux agent-director spawn --cwd <dir> --agent claude
```

Put that spawn's configuration in
`/path/to/alt-home/.config/agent-director/config.toml`. Run later calls on
that store with the same environment. Claude Code also needs its own login,
user settings and first-run setup under the alternate home, or authentication
through `extra_env` (see [Multi-Account](multi-account.md)). Manage the
sessions only through that store's agent-director; see
[A session of another agent-director store](operator-actions.md#a-session-of-another-agent-director-store).

## `--dangerously-skip-permissions`

agent-director passes this flag through `agent_args` unchanged. It bypasses
Claude Code's permission engine, including the per-spawn `permissions` block.

Do not let untrusted callers spawn agents through agent-director if they
must not be able to use this flag.

## Relay mode

Launch with `relay_mode` `on` (`--relay-mode=on`) to answer permission
requests through agent-director. Relay mode stays fixed for the launch.
The default and override are in [Configuration](configuration.md#keys).

Each request has its own token and moves the row to `check_permission`.
Inspect `get` or `list` for `permission_requests`, then read the intended
request before deciding:

```sh
agent-director get-permission --request-token <token>
agent-director decide --instance-id <id> --request-token <token> \
    --decision allow --max-wait-ms 2000
```

Use `--decision deny` to reject. Only the first accepted verdict is recorded.
`--max-wait-ms` (`max_wait_ms` over MCP and in the TypeScript client) bounds
the whole call and has no default. A caller with a deadline should pass
at most that deadline minus one second. Without it, store waits use
`[store] busy_timeout_ms`.

`decide` waits at most one second for acknowledgement and returns the
[delivery fields](#delivery). For polling and all refusals, follow
[Permission relay](caller-contract.md#permission-relay).

For Claude and Codex, only the row's own agent is relayed, not a nested
agent process or teammate pane. See [Hooks](hooks.md).

Timeout defaults, ranges, overrides and the Claude/Codex decision window
are in [The relay hook's time limit](configuration.md#the-relay-hooks-time-limit).
Cursor uses the [request bounds above](#cursor-spawns).

### Fail-closed boundary

Relay failures and uncertain acknowledgements are covered in the
[caller contract](caller-contract.md#permission-relay).

### The relay on Codex rows

Codex uses the same decision, delivery and pane-answer commands as Claude.
With relay off, a Codex permission prompt moves the row to
`check_permission` and is answered at the pane.

#### What is relayed

Codex's native approval and sandbox settings determine which calls ask.
The relay sees those requests, not calls Codex runs without asking or
refuses outright.

| What Codex asks about | `tool_name` | `tool_input` |
|---|---|---|
| A shell command outside the sandbox, including a retry after sandbox refusal | `Bash` | `command`, and `description`: the model's reason |
| A file edit outside writable roots | `apply_patch` | `command`: the whole patch |
| Input to a command outside the sandbox | `write_stdin` | `chars`, the command's session and other call fields |
| An MCP tool call, including agent-director's own tools | `mcp__<server>__<tool>` | The call's arguments |
| Network access through the managed proxy | `Bash` | The attempted `command`; `description`: `network-access <scheme>://<host>:<port>` |

The MCP server name is the one in Codex configuration. `tool_use_id` is
always null; fields Codex does not supply are null or empty. Dialog order
can differ from request order. Match the displayed command or patch to
the request before answering.

#### Answering a Codex approval at the pane

Read the dialog's displayed keys. For a shell command, `y` allows once,
`p` allows and adds a rule for commands with that prefix, and `Escape`
denies. A file edit uses `a` for its persistent choice. Both persistent
choices use `--as allow`.

```sh
agent-director read-pane --instance-id <id>
agent-director send-keys --instance-id <id> --request-token <T> \
    --as allow --key y --expect-pane-sha256 <H>
```

Use the [pane-answer rules](#answering-at-the-pane) below.

#### Startup files that print

Keep shell startup files silent on stdout. Diagnosis and repair belong in
[A Codex agent shows its own approval dialog after a relayed verdict](operator-actions.md#a-codex-agent-shows-its-own-approval-dialog-after-a-relayed-verdict).

#### How the hold differs on Codex rows

A main-agent `Stop` or Interrupt can prove eligible main requests gone.
A Codex `SubagentStop` cannot prove a subagent's request gone; that hold
lasts until the agent ends. For interruption, lost hooks and stopping a
held row, follow the [caller contract](caller-contract.md#permission-relay).

### A request of a finished spawn is closed

Requests close when their spawn ends or is marked `missing`, and stay
closed after resume. They disappear from `permission_requests`, but remain
readable with `get-permission` while retained. Closure does not authorize
a response. The [caller contract](caller-contract.md#permission-relay)
defines refusal handling.

### Delivery

`decision` is the recorded verdict, not proof of receipt or execution.
Read it together with `delivery`, `pane_answer` and the proof fields.

| Field | Meaning |
|---|---|
| `request_token`, `request_id` | Request identity; use the token to read or answer that request. |
| `tool_name`, `tool_input` | The requested action and its supplied input. |
| `requested_at` | When the request was recorded. |
| `decision`, `decided_at` | The recorded verdict and its time; null until recorded. An outside `unknown` claim has a null decision. |
| `decision_reason` | Verdict annotation, described below. |
| `delivery` | `delivered`: durable relay acknowledgement before output, not proof of receipt, execution or dialog closure. `not_confirmed`: no acknowledgement yet. `fallen_back`: no acknowledgement and the relay is gone or its settlement bound passed; completed pane answers also read this way. It does not say whether a native prompt waits. |
| `confirm_by` | Fixed settlement deadline: Claude/Codex hook end plus two seconds, or Cursor decision deadline plus four seconds. `not_confirmed` never lasts past it. |
| `hook_alive` | Whether the per-request relay hook runs: true, false, or null when unknown. Always null for Cursor. |
| `hook_gone_at` | When a reader first found fallback; null until recorded. |
| `attempted_decision`, `attempted_at` | A verdict refused with `ErrRelayFallenBack` and its time. These are history, not an answer. |
| `tool_use_id` | Cursor's `toolCallId`; null for Claude and Codex. |
| `pane_answer` | `none`: no recorded pane answer; `intent`: started, key delivery unknown; `sent`: key sent or native Cursor choice recorded; `outside`: answer recorded with `record-pane-answer`; `tool_ran`: validated Cursor execution. The last three close the request, but only execution proof establishes the matching dialog gone. |
| `pane_as` | The pane answer's claimed verdict, `allow`, `deny` or `unknown`; null if none. |
| `proven_gone_at`, `proven_gone_how` | When and how agent evidence proved the dialog gone: `tool_ran`, `turn_end` or `agent_gone`; null until proven. See [The dialog hold](#the-dialog-hold). |
| `unproven_since` | When a request stopped awaiting an answer without proof of dialog closure. Null while it awaits an answer or after proof. |

`decision_reason` values are `operator` (API deny), `timeout` (relay timeout),
`find_missing` or `ended` (spawn closure), `pane` (pane choice), `pane_outside`
(outside claim), and `tool_ran` (Cursor execution). API allow initially has
no reason. Cursor execution can fill a missing reason without replacing an
existing verdict or non-null reason; failed MCP proof leaves both unchanged.
Treat unknown reasons as opaque annotations, not writer identities.

These delivery rules apply to requests with a settle instant. See below for
[requests recorded by an older binary](#requests-recorded-by-an-older-binary).

**Which requests readers show.** `get`, `get-many` and `list` include
requests still awaiting an answer in `permission_requests` when the row
reads `check_permission`. A row can leave that state with a request still
open, so track each known token with `get-permission`. `get` and `get-many`,
not `list`, also include `unproven_requests`: requests holding the dialog
on a live row, including ones whose records already read answered.

**The row after the last answer.** An unanswered, unproven request holds
moves to `working`. Once none holds, a later agent report moves the row.
For Cursor, matching proof of an allowed invocation can move it to `working`
only while the prompt turn is open and no rotation is under way. Late
proof does not replay a turn-end report. State and recovery details are
in the [caller contract](caller-contract.md#permission-relay).

Retained requests remain readable after closure. Request retention settings
are in [Configuration](configuration.md#keys).

### Requests recorded by an older binary

A request recorded by an older binary without hook identity, confirmation
or a settle instant uses time-derived delivery:

- `confirm_by` is `requested_at` plus the relay window plus two seconds;
  `hook_alive` is null.
- Before `confirm_by`, delivery is `not_confirmed`. At or after it,
  delivery is `delivered` if a verdict other than a closure deny
  (`find_missing` or `ended`) is recorded, otherwise `fallen_back`.
- These rules also apply when closed. Derived delivery grants no authority
  to respond or infer receipt.

For decisions, retained holds and recovery, follow the
[caller contract](caller-contract.md#permission-relay).

### Unanswered permission requests

No timer moves a known waiting permission request to `needs_intelligence`.
Use `check_permission` and the request fields, not a needs-intelligence
timeout, to monitor it. See
[Needs-intelligence and idle-screen rows](caller-contract.md#needs-intelligence-and-idle-screen-rows).

### Send-keys interaction

Permission guards apply in every live state, not only `check_permission`.
For `ErrSendKeysWhileRelayed`, `ErrRelayFallenBack`, `ErrDialogMaybeOpen`,
pane-answer refusals and retry rules, follow the
[caller contract](caller-contract.md#permission-relay).

To stop a held spawn, use `kill`, or have a person or an LLM inspect and
exit it at the pane:

1. Read the pane. Close any fallen-back request with a token-specific
   pane answer, or `record-pane-answer` if already answered. Answer any
   remaining dialog after inspecting it.
2. Read the pane again and check that the agent's prompt is empty.
   `send-keys` does not clear a draft before appending `/exit`.
3. Send `/exit` with that read's `pane_sha256`:

   ```sh
   agent-director send-keys --instance-id <id> --text /exit \
       --expect-pane-sha256 <H>
   ```

4. Poll `get` or `status` until the row reads `ended`: `send-keys` does
   not wait for exit as `pause` does.

### The dialog hold

A delivery acknowledgement or pane-answer record does not prove the dialog
gone. On relay-on rows, an unproven request holds plain input without a
pane hash and holds `pause`. Cursor requests from an earlier launch are
excluded; requests with no launch on record still hold.

Proof comes from the agent, not from the caller's answer record:

- `tool_ran`: validated matching Cursor invocation evidence, as
  [defined above](#cursor-spawns). Claude and Codex requests cannot use it.
- `turn_end`: eligible main-agent requests recorded before the main turn's
  end, or a Claude subagent's requests before that subagent's turn ends.
  A missing child ID alone never establishes main-turn eligibility.
  Cursor also retires unanswered requests at their conversation's turn end.
- `agent_gone`: a non-Cursor spawn ended, was marked `missing`, or resumed.
  Cursor launch closure leaves proof unchanged.

An independent child's or unknown-origin request is not released by the
parent's work or life change. Relay and fallback holds can remain after
dialog proof. For hold handling, pane hashes and `pause`, use the
[caller contract](caller-contract.md#permission-relay).

### Answering at the pane

Read the pane and match its dialog to the request before choosing a key:

```sh
agent-director read-pane --instance-id <id>
agent-director send-keys --instance-id <id> --request-token <T> \
    --as deny --key Escape --expect-pane-sha256 <H>
```

`--as allow|deny`, `--key` and `--expect-pane-sha256` are required.
Do not supply `--text`. The key is one named key (`Escape`, `Enter`,
`Up`, `Down`, `Tab`) or one character; it is sent once, with no appended
Enter. Use the key shown for that request, especially on Cursor.

`H` is `pane_sha256` from the `read-pane` you judged. Use the same
`--n-lines` for both calls (default 25) and a normal, ANSI-stripped read,
not an `ansi` read. agent-director compares the bytes, not the screen's
meaning. `--as` is your claim. Cursor also checks that a sent claim
matches the selected option before answering the runtime.

An attached Cursor menu choice is recorded automatically. If an outside
answer has genuinely not been recorded, record it without typing anything:

```sh
agent-director record-pane-answer --request-token <T> \
    --as unknown --expect-pane-sha256 <H>
```

Use `--as allow` or `--as deny` only if you saw the answer. Pass the same
`--n-lines` as the inspected read. This records `pane_answer` `outside`
and `decision_reason` `pane_outside`; it neither authorizes Cursor approval
nor proves the dialog gone. Timing and refusal handling belong in the
[caller contract](caller-contract.md#permission-relay).

For plain input, `send-keys --text` appends Enter; `--no-enter` omits it,
and `--key` sends one key without text or an appended Enter.
Never pass `--expect-pane-sha256` blindly from an automatic flow: it means
a person or an LLM judged that exact screen.
