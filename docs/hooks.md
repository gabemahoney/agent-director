# Hooks

How agent-director's per-Spawn state-tracking hooks coexist with the
operator's own hooks. Each Claude Spawn gets eleven hook entries
synthesized into its `--settings`. Each Codex Spawn gets eight on Codex's
command line (see "The Codex entry set" below), plus a help entry and a
question-tool deny entry while their settings are on. They fire on every
lifecycle event of their agent and write the row update that powers
`status` / `get` / `list`,
but only for the row's own agent (see "Only the row's own agent moves
the row" below). A Claude Spawn's also write the agent's subagent records
(see "What the hooks write for subagent records" below); a Codex launch
registers no subagent events, so its agent has no records (see
[usage.md](usage.md#when-an-agent-is-tracked)). A Codex agent's
hooks run the same `hook` verb with `--agent codex` and map Codex's events
to states by their own table (see "Hooks from a Codex agent" below).

For Claude Code's own hooks reference, see:
<https://docs.claude.com/en/docs/claude-code/hooks>.

## The eleven state-tracking hooks

For a Claude agent, agent-director registers one entry per event listed
below (a Codex agent's are described under "Hooks from a Codex agent"). A
hook run with no `--agent` argument is a Claude hook. Each entry
is in exec form — `{"type": "command", "command": "<abs-path>/agent-director",
"args": ["hook"]}` — so Claude Code starts `agent-director hook`
directly, with no shell in between, and feeds it the payload JSON on
stdin. The hook's parent process is therefore the Claude process that
fired it. [Requirements](install.md#requirements) states the minimum
supported Claude Code version.
The `PermissionRequest` entry's arguments are `["hook", "--timeout",
"<N>"]`, N being the same per-hook `timeout` the entry gives Claude Code
(`[relay] timeout_seconds`), so the relay hook knows when Claude Code will
end it (see "PermissionRequest relay path" below).

A hook payload delivered to a no-verb invocation produces empty stdout and
exit 0, with `ad.hook.ignored` reason `no_exec_form`. It opens no store and
loads no config. The record takes `instance_id` from the environment;
`row_session_id` and `row_pane_pid` are null. Non-terminal stdin is read
with a 1 MiB cap and a 1-second deadline. Other input prints help. A run
with only global flags follows the same rule; `--home` selects the trail home.
`version` never reads stdin; `help` reads it only in a Codex agent's environment.
For diagnosis and repair see [no exec form](operator-actions.md#a-row-stays-pending-and-the-trail-shows-no_exec_form).

| Event | Tool matcher | Resulting state |
| --- | --- | --- |
| `SessionStart` | — | `waiting` (also writes `session_id`) |
| `UserPromptSubmit` | — | `working` |
| `PreToolUse` | `*` (all tools) | `working`; for the `Agent` tool it also writes subagent data, the state result unchanged (see "What the hooks write for subagent records" below) |
| `PreToolUse` | tool=`AskUserQuestion` | `ask_user` |
| `PostToolUse` | — | `working`; with `relay_mode=on`, it first proves the permission requests carrying the same `tool_use_id` gone (see "Hooks that prove a request's dialog gone" below), then closes a fallen-back one (see "A request whose tool ran" below); for the `Agent` tool it also writes subagent data, the state result unchanged |
| `PostToolUseFailure` | — | as `PostToolUse`: the tool ran and failed; for the `Agent` tool it also writes subagent data, the state result unchanged |
| `Stop` | — | `waiting`; with `relay_mode=on` and no `agent_id`, it first proves the main agent's permission requests recorded before it started gone; a main-agent `Stop` also writes subagent data, among it the subagents its `background_tasks` lists, the state result unchanged |
| `Notification` | `notification_type` = `idle_prompt`, no `agent_id` | `waiting` when the row is `working`, or `check_permission` with `relay_mode=on` and no permission request holding it (see "A relay row left in `check_permission`" below); any other state: soft refresh. Every applied write records `idle_since` (see "The idle-prompt Notification" below). With `relay_mode=on`, it first proves the main agent's permission requests recorded before it started gone, as `Stop` does |
| `Notification` | any other `notification_type`, or with `agent_id` | soft refresh — bumps `last_seen_at`, state unchanged |
| `PermissionRequest` | `*` (all tools) | `check_permission` |
| `SessionEnd` | cause ∈ {`logout`, `prompt_input_exit`, `exit`} | `ended` (also sets `ended_at`, closes the row's permission requests that still await an answer and proves every request of the row gone; see "The agent's end closes its requests" below; it also turns the row's `running` subagent records `lost`, see [usage.md](usage.md#when-an-agent-ends-goes-missing-or-relaunches)) |
| `SessionEnd` | any other cause (including empty / `clear` / `compact` / auto-compaction) | soft refresh: bumps `last_seen_at`, state unchanged |
| `SubagentStart` | — | soft refresh: bumps `last_seen_at`, state unchanged, `idle_since` and the settle clock untouched. With `agent_id` it records no session id, and writes the subagent's entry in the same write |
| `SubagentStop` | — | as `SubagentStart`; with `agent_id` its entry write also applies the subagents its `background_tasks` lists |

Unknown event names are treated as soft refreshes — the row's
`last_seen_at` updates and an info-level log line records the unknown
name so operators can spot new Claude Code events that need a classifier
update.

These are the states the hooks write, and what every verb that acts on a
row judges. With `[state] settle_ms` set in `config.toml`, `status`, `get`
and `list` show a state a hook wrote only once it has held that long, and
the previous settled state until then. A hook's `ended` shows at once; a
hook that moves a row out of `missing` or `ended` waits like any other
change. Each hook reads the setting when it fires, to judge whether the
state it replaces had settled. See [Settle time](configuration.md#settle-time-state).

### The idle-prompt Notification

A turn's `Stop` sets `waiting`. A hook that fires after `Stop`, with no
`Stop` after it, can leave the row `working` while the agent sits idle
at the prompt. One source is Claude Code's end-of-turn background
forks, such as the prompt-suggestion fork. Such a fork runs the
session's `PreToolUse` hooks from the agent's own process, but its tool
call never runs, so no `PostToolUse` follows. The payload does not tell
a fork from the agent.

Claude Code sends a `Notification` with `notification_type`
`idle_prompt` only while the main agent is idle at the prompt, about
60 s after its turn ended (Claude Code's default). Before Claude Code
2.1.288 it can fire while a background subagent is still running (see
"Limits" below). For that Notification, when the payload carries no
`agent_id`:

- a row that is `working` when the write lands returns to `waiting`.
  The update applies only for the row's own agent (see
  "Only the row's own agent moves the row"). The trail records an
  `ad.spawn.state_transition` from `working` to `waiting` with
  `soft_refresh` false and `triggering_event_name` `Notification`;
- so does a row with `relay_mode=on` in `check_permission` none of whose
  permission requests holds it. A request holds the row while it still
  awaits an answer (until its relay hook confirms a verdict in the store,
  it is closed at the pane, or it is closed with the row by
  `find-missing`'s mark or the agent's end) and Claude Code has not proven
  its dialog gone (see "Hooks that prove a request's dialog gone" below).
  The agent's turn ended after its
  last request was answered, and the hook that would have moved the row
  (the turn's `Stop`, after a denied request) was lost. The Notification's
  own proof comes first, so a main-agent request recorded before it
  started, answered at the pane outside agent-director, holds nothing by
  then. The trail records
  the move from `check_permission` to `waiting`;
- a row in any other state gets the soft refresh: `last_seen_at` is
  bumped and the state does not change. A row with the relay off in
  `check_permission`, or one with a request that holds it, is such a row.

Every applied write of this Notification, the soft refresh included, sets
the row's `idle_since` to the current time, and the agent's next applied
hook clears it (a `SubagentStart` or `SubagentStop` leaves it). `find-missing` reads it when it repairs a relay row left in
`check_permission`: `waiting` when it is set, `working` when it is not (see
"PermissionRequest relay path" below).

Every other Notification (`permission_prompt`, `auth_success`,
`elicitation_dialog`, a missing or unknown type), and an idle-prompt
Notification whose payload carries `agent_id`, is a soft refresh.

Limits:

- The row reads `working` until the idle-prompt Notification arrives,
  about 60 s after the turn ended.
- A stray hook whose write lands after the idle-prompt Notification's
  leaves the row `working` until the agent's next hook.
- If a new turn starts as the idle-prompt Notification fires, the
  Notification's write can land after the new turn's first hook. The
  row then reads `waiting` until the turn's next hook.
- On Claude Code 2.1.280 through 2.1.287, the idle-prompt Notification
  can fire while a background subagent is still running; 2.1.288 and later
  wait until background subagents finish. If that subagent's hook had
  moved the row to `working`, the row reads `waiting`, possibly while
  the subagent's tool still runs, until the subagent's next tool hook
  (`PreToolUse`, `PostToolUse` or `PermissionRequest`).

Setting `CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION=false` in the agent's
environment (`spawn`'s `extra_env`) turns off Claude Code's prompt
suggestions, so the prompt-suggestion fork's stray `PreToolUse` does not
fire.

## Cursor reporting

Cursor's gateway reports authoritative fresh session startup, same-launch
conversation rotation, submitted prompts, correlated turn completion and
confirmed intentional termination.
It directly invokes `agent-director hook --agent cursor` as the row's own
pane process. Reports also require independently verified runtime,
connection, transcript and current pane evidence for that launch. Optional
Cursor tool hooks cannot report lifecycle from a runtime child or another
process. Pane text is never classified to produce state.

Cursor startup confirms a conversation before the row becomes `waiting`;
accepted input yields `working`, a confirmed completion yields `waiting`,
and intentional termination yields `ended` only after the runtime is gone.
Launch return and pre-trust `failed` or `skipped` do not confirm startup.
Reporting is required independently of the help setting, saved pre-trust choice
and native permission policy. Initialization, load, mode or prompt failures
cannot supply a successful startup or turn report.
Rotation reporting authenticates the outgoing conversation and publishes the
fresh current identity with required outgoing history in one operation; an
old or child report cannot replace the main conversation. The interaction and
history references are in [Fresh Cursor use](usage.md#fresh-cursor-use);
publication uncertainty follows the caller contract below.
Unexpected child loss is distinct from transport failure and follows the
[caller contract](caller-contract.md#cursor-operation-boundaries).
[Permissions](permissions.md#cursor-spawns) owns authorization handling.

## Hooks from a Codex agent

A Codex agent's hook entries run the same verb, `agent-director hook
--agent codex`. The hook takes its agent from that argument alone: `claude`,
`codex` or `cursor`, and no `--agent` means `claude`. Neither the payload nor the
row's recorded agent can change it. An empty `--agent` or any other value
is a hook failure: it exits 0 with empty stdout and logs the entry, and a
relayed `PermissionRequest` gets its deny envelope (see "Fail-open
invariant"). `--agent` and `--timeout` may come in either order.

A Codex hook passes the same gate as a Claude one: it applies only when its
parent process is the row's recorded pane process (see "Only the row's own
agent moves the row"). Only the table that maps an event to a state differs.

### The Codex entry set

Every Codex launch (`spawn`, a reuse and `resume`) registers these entries on
Codex's command line, in no file:

| Event | Time limit |
| --- | --- |
| `SessionStart` | 600 s |
| `PreToolUse` | the relay window (`[relay] timeout_seconds`) |
| `PermissionRequest` | the relay window; the entry also tells the hook the window, as `--timeout <N>` |
| `SessionEnd`, `Interrupt` | 3 s |
| `UserPromptSubmit`, `PostToolUse`, `Stop` | Codex's default |

Each entry has no matcher and its command is the running binary in exec form:
`exec '<abs-path>/agent-director' hook --agent codex`. Codex runs it through a
shell (see "The shell between Codex and the hook" below), and `exec` leaves the
hook as Codex's child. A Codex launch registers no `Notification`,
`PostToolUseFailure`, `SubagentStart` or `SubagentStop` entry.

A `SubagentStart` or `SubagentStop` sent to a manually registered
`agent-director hook --agent codex` entry applies a soft refresh: it bumps
`last_seen_at`, keeps the state and writes no subagent record. It proves no
permission request gone. See [When an agent is tracked](usage.md#when-an-agent-is-tracked).

Two more entries come with their settings:

- While `inject_help_hook` is on, a help entry on `SessionStart`:
  `exec '<install path>' help`, with Codex's limit for the text it adds raised
  to 4000 (Codex counts about four bytes per unit; its default,
  2500, is too small for the list). It uses the installed path, not the running binary's (see
  [Settings](settings.md#defaultsinject_help_hook-config)). It is absent when
  the setting is off.
- While `disable_ask_user` is on, a `PreToolUse` entry that matches
  Codex's asynchronous question tool by name and blocks it, writing its reason
  to stderr (see [Permissions](permissions.md#disable_ask_user-config)).

agent-director's hooks run from the agent's first event with no hook-review
dialog in every launch, `no_pre_trust` and a failed pre-trust included (see
[Settings](settings.md#what-a-codex-launch-carries)). They are a separate layer:
they never replace or disable the user's, the group's or the project's own
Codex hooks, and the trust Codex recorded for those is kept. A Codex config
that turns hooks off, or allows managed hooks only, stops agent-director's hooks
too: the row then never reports in and stays `pending` (see "A SessionStart
that never writes" below, and for the check and the fix [A Codex row stays
`pending` or goes `missing` for another
reason](operator-actions.md#a-codex-row-stays-pending-or-goes-missing-for-another-reason)).

### The Codex states

| Event | Resulting state |
| --- | --- |
| `SessionStart` | `waiting` (also writes `session_id`) |
| `UserPromptSubmit` | `working` |
| `PreToolUse` | `working`, for any tool |
| `PostToolUse` | `working`, for any tool |
| `Interrupt`, no `agent_id` | `waiting` |
| `Interrupt` with `agent_id` | soft refresh: bumps `last_seen_at`, state unchanged (Codex does not send it) |
| `Stop` | `waiting` |
| `PermissionRequest` | `check_permission` |
| `SessionEnd`, no `agent_id` | the end report, for any reason (see "The end report" below): soft refresh, state unchanged; the row reaches `ended` only as "How a Codex row ends" below says |
| `SessionStart` or `SessionEnd` with `agent_id` | ignored, reason `subagent_event` (see "Subagents and in-process teammates") |
| `Notification`, `PostToolUseFailure`, `SubagentStart`, `SubagentStop`, any unknown event name | soft refresh: bumps `last_seen_at`, state unchanged |

No Codex tool moves a row to `ask_user`: Codex's question tools
(`request_user_input` and `request_user_input_async`) read `working` like
every other tool. Codex has no idle-prompt Notification either, so nothing
returns a `working` row to `waiting` except `Stop` and `Interrupt`. A Codex
row left `working` (the hook that would end the turn was lost) is therefore
never repaired: `find-missing` repairs only a row stuck at `check_permission`
(see "A relay row left in `check_permission`" below). For action on a stale row see the [caller contract](caller-contract.md#caller-contract).

`Interrupt` is Codex's Esc: it ends the turn the agent was working on. As
for Claude, a non-empty `agent_id` marks a subagent's hook. Apart from the
two session events above, such a hook applies its state transition from
the table, and records no session id.

### The session

- A Codex agent sends `SessionStart` at its first prompt, not when its
  screen opens. A launched Codex row therefore stays `pending` until the
  agent's first prompt; until then it looks like any agent that has not
  reported in (see "A SessionStart that never writes"). What that means for
  `pause`, `send-keys` and `resume` is in
  [usage.md](usage.md#spawn-states).
- The session id comes from the payload's `session_id`, and the transcript
  path is the path of Codex's session file, kept as Codex gave it. A hook
  with `agent_id` records neither.
- `/clear`, `/new`, `/fork` and `/resume` start another session in the same
  process. Codex names it in a `SessionStart` at the next prompt, so the
  row's session id follows from then; the earlier session is not ended.
  Until that prompt the row, and a `resume` of it, use the earlier session.
- When the agent exits (`/exit`), Codex sends `SessionEnd` for every session
  the process held, including a `/new` session that never got a prompt.
  Each one is an end report.
- Codex gives the `SessionEnd` and `Interrupt` hooks only a few seconds, so
  a store wait longer than that can get one killed before it writes (see
  "Fail-open invariant").

If a Codex hook finds the row recording no transcript path, it fills the
path in from its payload, but only when that file exists and its name
carries the hook's own session id. A path is never replaced. The trail
records the fill as an `ad.session.jsonl_healed` record with `source`
`ad_spawn_store`. A relayed `PermissionRequest`'s write does not fill the
path; the next hook that applies does. A thread Codex starts inside the session, such as the
one `/review` runs (see the Codex limits below), has its own file, whose name
carries another id, so it does not fill in the path of a row whose session
is known.

### The end report

A `SessionEnd` without `agent_id` is Codex reporting that a session ended.
It does not end the row: Codex sends one for every session the process held,
and a session that ended does not mean the agent did. The hook keeps the
row's state, applies a soft refresh and records that a report arrived. It
closes no permission request and no subagent record. The trail records one
`ad.spawn.state_transition` from the row's state to the same state, with
`soft_refresh` true and `triggering_event_name` `SessionEnd`. A later
`SessionStart` from the agent clears the record of the report. What turns a
report into `ended` is in "How a Codex row ends" below.

### How a Codex row ends

A Codex row moves to `ended` only when both hold: the agent process of its
current launch is proven gone (judged by its recorded start time, as
`find-missing` judges every row, with no tmux call) and the row holds an end
report. Two verbs make the move, each through the same guarded write:

- `find-missing`, for every live row it finds gone.
- `pause`, before it sends any key (in any live state, `pending` included) and
  in each poll while it waits for `ended`.

A Codex row never reads `ended` while its agent runs. An `/exit` typed at the
pane records the end report only, so the row stays in its last state until
`find-missing` or `pause` sees the agent gone (what a caller does is in the
[caller contract](caller-contract.md#when-a-codex-agent-exits)).

- **Gone, no end report:** `find-missing` marks the row `missing`, as for a
  Claude row.
- **Judged on tmux evidence alone** (the process cannot be checked, or was
  never recorded): `find-missing` marks the row `missing`, whatever report it
  holds.
- **The move closes the row's requests** and its `running` subagent records, together (see "The agent's end closes its requests" below).
  The row keeps its end report.

Whichever verb makes the move, it writes `find-missing`'s records: one
`ad.find_missing.tick` with `new_state` `ended` and `reconciliation_reason`
`proc_absent`, and, for each request it denies, one
`ad.row_mutation.committed` with `decision_reason` `ended` and
`writer_process` `find_missing`, which can therefore come from `pause`. It
writes no `ad.spawn.state_transition`.

### Subagent records

A Codex agent's hooks write no subagent records and no open Agent calls: its
`SubagentStart` and `SubagentStop` are soft refreshes, and its `Stop` makes
no subagent write. See [usage.md](usage.md#when-an-agent-is-tracked).

### The shell between Codex and the hook

Codex runs each hook through the account's login shell with `-c`: bash or
zsh, and bash for any other login shell, such as dash or fish. It ignores
`$SHELL`, and the shell is not a login shell. The entry's command starts with
`exec`, so the hook replaces the shell and Codex is the hook's parent, which
the own-agent check needs. This holds for every account shell.

Keep the shell's startup files silent on stdout: for zsh `-c` that is
`.zshenv` (the only file it reads), for bash the file `BASH_ENV` names (a
bash `-c` reads no other). agent-director launches Codex with `BASH_ENV` empty
(see [Settings](settings.md#what-a-codex-launch-carries)), so only a `BASH_ENV`
the caller sets in `extra_env`, or a zsh `.zshenv`, prints. Text they print reaches the hook's stdout ahead of
the hook's own output, and Codex reads the whole as the hook's output. The
model sees it from the `SessionStart` and `UserPromptSubmit` hooks, and an
`Interrupt` hook shows "Hook failed" in the pane. What it does to a relayed
`PermissionRequest` is in [Startup files that
print](permissions.md#startup-files-that-print). Text on stderr does no harm.

A Codex started through a launcher that runs it as a child (the node
launcher npm installs, or a wrapper that does not `exec`) makes the hook's
grandparent the pane process: the hook is refused with `pid_mismatch` (see "A
`claude` that does not exec"). The advice in that record names `codex`, the
launcher case and `[launch] codex_path`; see [Requirements](install.md#requirements)
and, for the steps, [A row stays `pending` and the trail shows
`pid_mismatch`](operator-actions.md#a-row-stays-pending-and-the-trail-shows-pid_mismatch).

### Limits

- **`/review`.** Codex's `/review` runs a review thread inside the session.
  Its tool and prompt hooks carry the session's id and no `agent_id`, so
  they set `working`. It sends no `Stop` and no `Interrupt` of its own, and
  after its last tool nothing returns the row to `waiting`: the row reads
  `working` until the agent's next prompt (an Esc during the review sends one
  `Interrupt`). For action on a stale review state see the [caller contract](caller-contract.md#caller-contract).
- **Other internal threads.** Codex's other internal threads (compaction,
  memory work) are not known to send hooks marked with `agent_id` either, so
  they can leave the same trace.

## Only the row's own agent moves the row

Every hook, of every event, applies only when its parent process (its
pid, with that process's start time) is the row's recorded pane
process: the process agent-director created the session with, which is
the agent itself. When the row's recorded pane start time
is unknown, the pid alone decides and the first applied hook records the
start time. A hook cannot write on behalf of another launch.

A hook from any other process that carries the row's
`AGENT_DIRECTOR_INSTANCE_ID` changes nothing: a nested `claude` or `codex`
started by hand in the agent's shell, a Codex service shared with other
agents, a teammate pane split in the agent's session, a leftover of an earlier
launch, or a shell that pipes a payload into `agent-director hook`. So does every hook while the row records no pane
yet (for SessionStart, only after the bounded wait described below).
Such a hook:

- writes no state, no `last_seen_at` and no permission request;
- exits 0 with empty stdout (a relayed PermissionRequest returns no
  decision, so Claude Code asks as it would with no relay);
- writes one `ad.hook.ignored` record to the trail, with reason
  `pid_mismatch` (the parent is another process; the record names its pid
  and command) or `no_pane_recorded` (the row records no pane yet).

`/clear`, `/resume` and compaction inside the agent's own Claude Code
come from the same process, so they apply, and SessionStart records the
new session id. The same holds for a Codex agent's `/clear`, `/new`,
`/fork` and `/resume`, whose new session a SessionStart names at the next
prompt (see "Hooks from a Codex agent"). For Claude/Codex, the payload's session id is
recorded, never used to decide whether a hook applies, and their hooks make
no tmux call. Cursor reports instead require the registered current session
and connection plus independent runtime, transcript and pane verification;
pane verification can call tmux. See [Cursor reporting](#cursor-reporting).

### A `claude` that does not exec

A launcher that starts the agent as a child, instead of replacing itself,
leaves its hooks with another parent than the recorded pane process. Such
hooks are ignored with `pid_mismatch`; this applies to Claude and Codex.
A refused SessionStart on a pending row whose hook grandparent is the pane
also writes `ad.hook.pane_is_grandparent`, with `instance_id`, `pane_pid`,
`pane_command`, `parent_pid`, `parent_command`, `advice` and `source` `ad_hook`.
Unreadable commands are null. Each such SessionStart may write the record;
it changes no state or output. See [diagnosis and repair](operator-actions.md#a-row-stays-pending-and-the-trail-shows-pid_mismatch).

### SessionStart before the launch's identity write

A first SessionStart can arrive before the launch has recorded its pane.
It waits while the row is pending, has a launch start, records no pane and
remains within its pending grace period. The wait ends at the earlier of
launch start plus `pending_grace_seconds` and 540 s after waiting began.
The 540 s cap uses elapsed time, so a wall-clock change cannot extend it.
Only SessionStart waits; subagent and in-process teammate session hooks
never do. A row with no launch start, or already past grace, never waits.

The row is checked every 250 ms. A recorded pane, a changed or deleted row
or a failed read ends the wait. The final own-agent check applies the hook
if its parent is now recorded; otherwise it records `pid_mismatch` or
`no_pane_recorded`. A deleted row produces no record.

The SessionStart state entry has a 600 s timeout. The cap bounds the wait,
not all work of the hook: busy-store waits can still consume the remaining
time and get the hook killed without a result or trail record. See
[store timeout](configuration.md#keys) and the recovery links below.

### A SessionStart that never writes

A missed or ignored SessionStart, a startup screen before the first hook,
or a Codex launch awaiting its first prompt can leave a running agent
`pending`. Agent-director cannot tell these from the row alone. A scheduled
`find-missing` notes an alive, past-grace row `unreported`, once its launch
owner no longer holds it and its pane is recorded or adopted. It keeps
`launch_started_at` and the first `liveness_unverified_since` of that launch.
An existing `provenance_conflict` note is kept. A later applied hook clears
the note; a working move held by a permission request clears only `idle_since`.

For caller action see the [live-row sequence](caller-contract.md#caller-contract);
for hook configuration repair see [Operator actions](operator-actions.md#a-row-stays-pending-and-the-trail-shows-pid_mismatch)
and [Codex pending rows](operator-actions.md#a-codex-row-stays-pending-or-goes-missing-for-another-reason).

### Subagents and in-process teammates

Claude Code runs every subagent inside the agent's own process. It does
the same for an in-process agent-team teammate's turns (the default
`teammateMode`). Their hooks therefore pass the parent-process check.
Claude Code marks them with a non-empty `agent_id` in the payload.
`agent_type` alone does not mark one: a session started with `--agent`
carries it and is the agent itself.

- **SessionStart or SessionEnd with `agent_id`:** the hook changes
  nothing. It is identified by the payload. It writes no state and no session id, and
  exits 0 with empty stdout. It writes one `ad.hook.ignored` record with
  reason `subagent_event`, and its `ad.hook.fired` record has
  `upsert_outcome` `no_change`. If no row has the id, no
  `ad.hook.ignored` record is written. So a subagent can neither replace
  the row's session id (which `resume` uses) nor end a live row.
- **Every other hook with `agent_id`:** it applies its state transition
  like any hook from the agent's process, but it records no session id
  and no transcript path. A subagent's tool and permission events
  therefore move the row's state and use its relay: the row reflects the
  process. The one exception is an idle-prompt `Notification` with
  `agent_id`: it is a soft refresh and never returns the row to
  `waiting` (see "The idle-prompt Notification").
- **SubagentStart and SubagentStop with `agent_id`:** these are not
  ignored: only SessionStart and SessionEnd are. Each is a soft refresh
  of the row that also writes the subagent's entry (see "What the hooks
  write for subagent records" below).
- **Permission requests with `agent_id`:** a subagent's `PostToolUse`
  proves its request gone by `tool_use_id` like the agent's own, but no
  `Stop` or idle-prompt Notification does: a background subagent can
  outlive the main agent's turn. `SubagentStop` is registered, but no
  subagent event proves a permission request gone (see "Hooks that prove a
  request's dialog gone").

### The `ad.hook.ignored` reasons

| Reason | When |
| --- | --- |
| `pid_mismatch` | The hook's parent is not the row's recorded pane process. |
| `no_pane_recorded` | The row records no pane yet. For SessionStart, written only after its bounded wait (see "SessionStart before the launch's identity write"). |
| `subagent_event` | A SessionStart or SessionEnd whose payload carries a non-empty `agent_id`, from a Claude or a Codex agent. |
| `no_exec_form` | A no-verb run received a hook payload on stdin, from a Claude Code that does not run exec-form hooks. It is written with no store access. |

Each record carries `instance_id`, `hook_event`, `reason`,
`parent_pid`, `parent_command`, `hook_session_id`, `row_session_id`,
`row_pane_pid` and `source` = `ad_hook`. For Claude, `hook_session_id` is
the basename of the payload's `transcript_path` with its extension removed
(the text from its last `.` on), or null when the payload gives none. For
Codex, it is the payload's `session_id`, or null when absent; a Codex hook
marked with a non-empty `agent_id` records no `hook_session_id`. It never
names another row.

Consequences: a nested agent's or a teammate pane's work is not
reflected in the row, and an `ended` row stays `ended` against every
other process's hooks.

## What the hooks write for subagent records

agent-director keeps one entry for each subagent id in each life of the row's
agent. An
entry becomes a record once an Agent call or a launch result confirms
it, or a listing does (the `background_tasks` list of a main-agent `Stop`
or of a `SubagentStop`); an entry that nothing confirms is an observation,
and is not a record. An open Agent call is a note that the agent called its `Agent`
tool and no subagent has yet been matched to the call. An entry's life never
changes. Its launch association can change within that life: a `resume` or
a `reuse` starts a new launch. The rules below look up entries only in the
row's current life. An id found only in an earlier life counts as having no
entry: a creating event makes a separate current-life entry, and a stop or
listing that cannot create leaves the earlier entry untouched. A
consumer reads the records with `list-subagents`; its output and fields are in
[usage.md](usage.md#read-an-agents-subagent-records).

Only the row's own agent's hooks write entries and open Agent calls. Each
write carries the same gate as every hook (see "Only the row's own agent
moves the row"), so a hook from any other process writes none, and so
does a hook for a row that is `ended` or `missing`. Entries are written
only for a launch whose `--settings` register `SubagentStart` and
`SubagentStop`; whether a launch is such a one, as a consumer sees it
(`tracked`), is in [usage.md](usage.md#when-an-agent-is-tracked). Every time written is the moment
the hook started.

- **`SubagentStart`**, with `agent_id` (without one it is the soft refresh
  alone). It writes in the same write as its soft refresh: both land or
  neither does.
  - A start for an id with no entry creates one, `running`, that records
    the payload's session id and, from `agent_type`, its type.
  - A start that creates the entry and supplies a type is matched to the
    oldest open Agent call of that type in the row's current launch: the
    call is closed and the entry is confirmed. The match only confirms. It
    copies nothing from the call, and each start matches at most one call.
  - A start without a type is never matched, so its entry is an
    observation.
  - A start for an id that already has an entry never matches a call.
  - A start for an id that has an entry moves it into the current launch,
    sets its type when it has none or got it from a lower source (a type
    from a start outranks one from a launch result), and, when the entry
    was `stopped` or `lost`, runs it again. A running entry stays running.
- **`SubagentStop`**, with `agent_id`. It writes in the same write as its
  soft refresh, and without an `agent_id` it is the soft refresh alone. It
  never creates an entry, and it changes only an entry of the current launch. A `running` or `lost` entry becomes `stopped`
  and takes the stop's time as `stopped_at`. A stop for an entry already
  stopped keeps `stopped_at`, unless the stop carries `stop_hook_active`
  true (the real stop after another stop hook blocked the first one) or the
  earlier time came from a launch result; the new stop's time replaces
  it. What a consumer sees of a record that reads stopped while another
  stop hook blocks the real stop is in
  [usage.md](usage.md#how-long-a-record-reads-running). After the stop, and
  whether or not it changed anything, the stop applies the subagents its
  `background_tasks` lists (see "The `background_tasks` list" below): each
  one that has a record, of any launch within the current life, is confirmed
  and moved into the
  current launch, and a null run mode becomes `background`. The listing
  never creates a record, closes one or reopens one, and never changes a
  state or a time.
- **The launch request**, a `PreToolUse` for the `Agent` tool. It opens an
  open Agent call holding the `tool_use_id`, the type from
  `tool_input.subagent_type` and the time. It opens none when the call has
  no `tool_use_id`, names no type or carries a `tool_input.name` (a
  teammate launch), and it changes nothing when that `tool_use_id` is
  already open, or already belongs to an entry of the current launch.
- **The launch result**, a `PostToolUse` for the `Agent` tool.
  - It closes the open Agent call with its `tool_use_id`, whatever else
    it does. A result that carries neither a `tool_use_id` nor an
    `agentId` writes nothing.
  - With `tool_response.agentId` it also confirms and updates that
    subagent's entry, moving it into the current launch. It fills what the
    entry does not yet have: the description (`tool_input.description`),
    the model (`tool_response.resolvedModel`), the parent (the subagent
    that made the call, from the payload's `agent_id`, otherwise the agent
    itself), the `tool_use_id` and the run mode. The type is
    `tool_input.subagent_type`, or, when the call named none, the
    `agentType` of a `completed` result. A type from a start outranks it.
  - `tool_response.status` `async_launched` counts as a start: it creates a
    `running` entry for an id with none, with run mode `background`.
    `completed` counts as a stop: it creates a `stopped` entry with run mode
    `foreground`, or stops a `running` or `lost` one, with the result's
    time as `stopped_at`. Any other status creates no entry and changes no
    state, though it still fills fields of an entry that exists.
- **`PostToolUseFailure`** for the `Agent` tool closes the open Agent call
  with its `tool_use_id` and writes nothing else.
- **A main-agent `Stop`** (no `agent_id`) closes the open Agent calls of
  the current launch opened more than 2 s before the `Stop`. No call of an
  earlier launch is left to close: the closes of a row that ends, goes
  missing or begins a new launch delete them. A main-agent `Stop` also
  applies its `background_tasks` as a snapshot of the agent's running
  background subagents (see "The `background_tasks` list" below), with the same snapshot:
  - A listed subagent with no record gets one, `running`, `run_mode`
    `background`, with the listed type and the `Stop`'s session id. Its
    description, model, parent, `tool_use_id` and start times are null.
  - A listed subagent that has a record, of any launch within the current life,
    is confirmed and
    moved into the current launch, and a null run mode becomes
    `background`. Its type is not applied, and its state and times never
    change, so a stopped or lost record is not reopened.
  - Then every `running` background record of the current launch that the
    list does not name, and that is old enough, becomes `lost` with
    `close_reason` `absent_from_snapshot` and the `Stop`'s time as `lost_at`.
    Which records are spared, and what a consumer sees, is in
    [usage.md](usage.md#when-a-record-turns-lost).

  A `Stop` that can change nothing writes nothing. A skipped `Stop` only
  defers a close to a later one.

  A `Stop` carrying `agent_id` writes nothing of this kind.
- **The `background_tasks` list**, of a main-agent `Stop` and of a
  `SubagentStop` with `agent_id`. A missing, null or non-array field is no
  snapshot: nothing is created or closed. An array is a snapshot, even an
  empty one, which can close records. An element that is not an object, or
  that is not a running subagent with a non-empty string `id`, lists
  nothing; of an element that is not a subagent only the `type` is read. Of
  a running subagent the `id` and the `agent_type` are read, the latter as
  the listed type, empty when it is missing or not a string. A subagent id repeated in the list counts once,
  the first. No listed task's description is read or stored. A wrongly typed
  field is not supplied and fails nothing.

Subagent data is accepted only from the row's own agent on an applied
hook. It adds no stdout or trail record; `ad.hook.fired` reports the row
write's outcome. `SubagentStart` and `SubagentStop` apply their row refresh
and entry together, so a failure leaves both unchanged. Agent-tool and
main-agent Stop entry failures leave any applied row update intact.

Internal agents (Claude Code's own, for one) produce no record, and
teammates produce none except by one residual. A teammate's launch
request names it and so opens no Agent call, and its result names no
subagent, so it confirms nothing. An internal agent's `SubagentStop`
finds no entry. A start that either one fires can leave an
observation, which stays one. The residual: a teammate whose type equals
that of a running launch's open Agent call can claim that call with the
start that creates its entry, and then shows as one record. The residuals
a consumer meets are in
[usage.md](usage.md#which-subagents-show-as-records).

Nothing of a subagent's content is read or stored: not the prompt, the
messages, the result text, an error, usage counters or an output file. The
only text kept from a launch is the
description the call gave, the type, the model and ids. Nothing of it
reaches the log or the trail.

Limits:

- Apart from the closes of a row that ends, goes missing or begins a new
  launch (see [usage.md](usage.md#when-an-agent-ends-goes-missing-or-relaunches)),
  only the hooks above end an entry. A subagent whose stop signals never
  reached agent-director (a foreground one is still stopped by its completed
  launch result) leaves its entry `running` until a close of the row or a
  snapshot ends it; which entries a snapshot can end is in
  [usage.md](usage.md#how-long-a-record-reads-running).
- A background subagent whose hooks were all missed has no entry until a
  later main-agent `Stop` lists it as running. A `SubagentStop`'s listing
  never creates an entry.

## Fail-open invariant

State-tracking failures leave the agent running. The `hook` verb
exits 0 with empty stdout on every internal failure, and on every hook
it does not apply:

- Missing `AGENT_DIRECTOR_INSTANCE_ID` env → exit 0, log entry.
- Malformed JSON payload → exit 0, log entry.
- Payload over 1 MiB → exit 0, log entry. The payload is dropped whole:
  nothing is written, subagent entries included. When both stop signals of
  a subagent exceed 1 MiB, its record is left `running`; see
  [usage.md](usage.md#how-long-a-record-reads-running).
- Config malformed → exit 0, log entry.
- Store open failure → exit 0, log entry.
- No usable `HOME` (unset or empty) → exit 0, log entry (normally on
  stderr), and no store opened or created anywhere: the store path `~/…`
  is refused, never resolved against another home such as the passwd
  entry, and no trail record is written. A relayed PermissionRequest
  still gets its deny envelope (see below).
- This host still has the old directory and no store in
  `~/.config/agent-director` (`ErrOldLayout`) → exit 0, one line on stderr,
  and nothing written or created, no log file or trail record. A relayed
  PermissionRequest still gets its deny envelope. A human fixes it: see
  [A verb refuses with `ErrOldLayout`](operator-actions.md#a-verb-refuses-with-erroldlayout).
- DB write failure → exit 0, log entry.
- Unknown event name → exit 0, soft refresh, log entry.
- Hook from a process other than the row's recorded pane process, or for
  a row that records no pane → exit 0, nothing written, one
  `ad.hook.ignored` trail record (plus, at most, one
  `ad.hook.pane_is_grandparent`; see "A `claude` that does not exec"). A
  SessionStart for a `pending` row
  that records no pane first waits, until the grace period ends or for
  at most 540 s, whichever comes first (see "SessionStart before the
  launch's identity write").
- SessionStart or SessionEnd from a subagent or in-process teammate
  (non-empty `agent_id`) → exit 0, nothing written to the row, one
  `ad.hook.ignored` record with reason `subagent_event` (none when no row
  has the id).
- Hook from a Claude Code that does not run exec-form hooks (a no-verb
  run given a hook payload on stdin) → exit 0, nothing on stdout or
  stderr, no store opened, one `ad.hook.ignored` record with reason
  `no_exec_form`.
- Hook with an `--agent` that is empty or none of `claude`, `codex` or `cursor` →
  exit 0, nothing written, log entry (a relayed PermissionRequest gets its
  deny envelope, see below).
- Hook for an id with no row → exit 0, nothing written.
- `SubagentStart` or `SubagentStop`, or a subagent write after another
  hook's row write, that fails → exit 0 with one log entry. The entry
  names the instance id and the event and carries no payload text. A
  `SubagentStart` or `SubagentStop` whose combined update fails writes
  nothing, the row refresh included, and its `ad.hook.fired` record has
  `upsert_outcome` `error`; the entry's next event, or a later snapshot (see
  [usage.md](usage.md#when-a-record-turns-lost)), repairs it. A
  failed subagent write after another hook's row write leaves that row
  write and its `upsert_outcome` as they were.

The subagent hooks add nothing to stdout: no context or decision, and
SubagentStop never blocks a stop. Busy-store waits are bounded by
`[store] busy_timeout_ms` per write; a hook that needs more than one write
can spend that bound more than once.

All log entries land in `~/.config/agent-director/errors.log` (configurable
via `[log] error_log_path` in `config.toml`), or on stderr when that
file cannot be opened. A missed state update is
annoying but never breaks a Claude session.

SessionStart's wait for the launch's identity write is the one case
where the hook deliberately takes time. It can delay the hook's exit
until the earlier of the launch start plus the effective
`pending_grace_seconds` and 540 s after the wait began, and Claude
Code's first response waits for SessionStart hooks to finish. The 540 s
cap keeps the wait, though not the hook's store writes around it, inside
the entry's 600 s timeout (see "SessionStart before the launch's identity
write"). It happens only in the race described above; the hook still exits
0 with empty stdout whatever the wait's result.

The relay-mode `PermissionRequest` path is fail-*closed* until its request
is recorded: any internal error before then, its first store write
included, or an invalid `--agent`, emits a `deny` decision envelope on stdout before the hook
exits, so the tool is denied. Once the request is recorded, the hook writes
only an answer it has first confirmed in the store; any failure after that
ends with no answer, so the agent asks in its own permission prompt and
the request reads `fallen_back`. See "PermissionRequest relay path" below
and [Permissions](permissions.md).

## Fire order

Claude Code merges hook entries across tiers and runs them in array
order (see `settings.md`). Lower tiers fire first:

```
user (~/.claude/settings.json)
  → project (.claude/settings.json in cwd)
    → local (.claude/settings.local.json)
      → agent-director (per-Spawn --settings)
        → policy (managed)
```

agent-director's hook is *last among user-installed tiers*. For state
tracking this is fine — by the time our hook runs, every user hook has
already had its turn; its state update lands with the most recent
`last_seen_at`. For relay-mode permission decisions the ordering
matters: our decision envelope on stdout is the one Claude consumes,
regardless of what earlier-running user hooks emit.

## Misbehaving user hooks

A user hook can stall (long-running tool call), exit non-zero, or write
malformed stdout. Each of these has a different blast radius:

- **Slow user hook** — Claude Code waits for every hook entry to return
  before consuming the event. A 30-second user-script hook stalls the
  entire pipeline; agent-director's state update doesn't land until
  the user hook returns. The `status` verb will keep showing the
  pre-event state during the stall.
- **Non-zero user hook** — for `PreToolUse`, a non-zero exit can block
  the tool call (Claude Code's policy). agent-director's hook still
  runs and writes its state update either way; the row state is independent
  of whether the tool was actually executed.
- **Malformed user hook stdout** — irrelevant for state-tracking hooks
  (they don't emit stdout). A poorly-formed permission-decision
  envelope from a user hook could confuse the relay path;
  agent-director's relay hook is always last, so it wins the decision
  channel.

To exclude a user hook from supervised spawns, see
[Operator actions](operator-actions.md#a-user-hook-interferes-with-supervised-agents). Hooks receive `AGENT_DIRECTOR_INSTANCE_ID` in
both Claude and Codex agents.

## Persistent hooks (the `help`-on-SessionStart pair)

The installer adds two persistent hook entries to the
user's `~/.claude/settings.json`:

- `agent-director help` on `SessionStart`
- `agent-director help` on `SessionEnd matcher=compact`

These are **not** per-Spawn; they fire on every Claude session the
operator runs, regardless of whether agent-director launched it.
They inject the verb list into the new conversation so the model
knows the supervision API surface after a `/compact` or fresh
session.

Both entries are in shell form (`"command": "<install path> help"`, no
`args`) and name the `help` verb explicitly. A Claude Code that does not
run exec-form hooks still runs `agent-director help`, so the no-verb
`no_exec_form` check never applies to them. The same holds for a Claude
Spawn's per-Spawn `inject_help_hook` SessionStart entry, which is in shell form
too. The installer adds that pair for Claude Code only. A Codex agent
agent-director launches has a help entry per launch (see "The Codex entry set"
above); a Codex folder gets a help entry of its own, for the agents you start
there by hand, only when you add it (see [Set up agent-director in a Codex
folder](settings.md#set-up-agent-director-in-a-codex-folder)).

### Persistent hook layering

The help pair applies to every operator Claude session; the per-spawn
entries apply only to supervised launches. Reinstalling skips matching
help commands and preserves other entries. Uninstall removes only the
installer's entries. See [Installation](install.md) and [Settings](settings.md).

### What `agent-director help` actually outputs

The persistent hook runs `agent-director help` which produces the
manifest-driven verb list as JSON. Claude Code captures the hook's
stdout and injects it as a system-message tool-availability hint
in the new conversation. The model sees the same surface a script
reading the CLI's `--help` would.

A Codex agent's help entry gets the whole list in Codex's structured form: one
JSON object whose `hookSpecificOutput` names the event `SessionStart` and
carries the list as its `additionalContext`. `help` prints it only when the
launch marked the agent as Codex and the hook payload on stdin is a
`SessionStart`; in every other case, a Codex agent's own `agent-director help`
in its shell included, it prints the plain list. In a Codex agent
`help` reads stdin for this with the 1 MiB cap and 1-second deadline of a
no-verb run, and prints the plain list when the read fails. The whole list
reaches the agent at every session start: its first prompt, and the next
prompt after `/clear`, `/new`, `/resume` or `/fork`.

With `inject_help_hook` enabled, Cursor receives the same complete plain JSON
guidance as MCP initialization instructions through its session-scoped ACP stdio
registration. This applies at fresh startup, latest-session load and each
confirmed `/clear` or `/new`, without an extra user turn or replayed prompt.
The registration explicitly selects the managed parent's home, opened AD store
and intended tmux server; it does not depend on dashboard MCP configuration or
ambient environment inheritance. The gateway requires the current attempt's
completed MCP initialization before conversation publication or input admission.
That acknowledgement establishes the transport exchange, not native model
consumption. Native full-guidance consumption remains unverified for fresh,
loaded and replacement conversations.

Cursor provides no supported ACP compaction callback on this route, so AD cannot
force guidance back into an already compacted conversation. Native retention
after compaction has not been checked live. This limitation applies only to
compaction; startup, latest load and confirmed conversation changes still require
guidance initialization. The [caller contract](caller-contract.md#cursor-operation-boundaries)
owns deadlines and failure behavior.

### Caveat — install fail-closed gap

A Claude or Codex help command that cannot run injects no verb list; the
conversation proceeds. For installation checks and repairs see [Installation](install.md).

## PermissionRequest relay path

When a Spawn's `relay_mode=on`, the hook handler takes a second branch
on PermissionRequest events instead of the ordinary state write: it
records the request, polls for the orchestrator's `decide` verdict, and
writes an answer on stdout only once it has confirmed it in the store. See
`permissions.md` for the user-facing contract; this section covers the
hook's side.

### The relay hook's clock

The hook reads the time first thing, and `--timeout N` (written by `spawn`
on the `PermissionRequest` entry, the same N as that entry's per-hook
`timeout`) gives the instant the agent will end it: its start plus N.
A hook entry spawned before the argument existed reads the loaded config's
`[relay] timeout_seconds` instead. Every step below keeps time against that
instant, with a 2 s reserve before it for writing the answer and exiting:

| Instant | What happens there |
| --- | --- |
| start + N − 3 s | the poll ends: no verdict yet means the timeout deny |
| start + N − 2 s | the last moment a store write may commit; later, the hook gives no answer |
| start + N | the agent ends the hook |
| start + N + 2 s | the request's settle instant (`settled_at`), `confirm_by` |

So `timeout_seconds` of 3 or less leaves no time to poll, and at 2 or less
no time to answer.

### The steps

1. The hook records the request and moves the row to `check_permission`
   together, only for the row's own agent. The request carries its token,
   hook identity, settle instant and payload's tool and agent ids. A Codex
   request has no `tool_use_id`. Failure before recording returns a deny.
2. It polls its own request at `max(50ms, relay.poll_base_ms +
   uniform(0, relay.poll_jitter_ms))`, stopping by the poll deadline.
3. A verdict is answered only after its delivery is confirmed in the
   store and the parent is still the agent that started the hook.
4. At the deadline, an undecided request receives a confirmed timeout deny.
   A verdict that arrived first is confirmed instead. The deny changes no state.

After recording, a failed confirmation, a changed parent, a deleted
request or five consecutive failed reads produces empty stdout. The agent
then asks in its own permission prompt; the request reads `fallen_back`
once readers see the relay hook gone. See the [caller contract](caller-contract.md#permission-relay)
for handling this outcome.

### Writing the envelope

The handler emits exactly one line on stdout — the
`hookSpecificOutput` envelope — or nothing.
Non-PermissionRequest events leave stdout empty (state-tracking has no
envelope contract).

### `AGENT_DIRECTOR_RELAY_MODE` env var

The hook takes relay mode from its launch environment's
`AGENT_DIRECTOR_RELAY_MODE`, so a store or config failure still has the
launch's relay policy.

### Fail-closed boundary

With relay on, failures before a PermissionRequest is recorded return a
deny envelope when the event name can be read. Unreadable stdin or
unparseable payloads produce no envelope: the event cannot be identified.
After recording, the hook writes a confirmed answer or nothing. Other
events receive no permission envelope. See [Permissions](permissions.md).

### Per-request rows in `permission_requests`

Every relay invocation has its own UUIDv4 `request_token`. Concurrent
requests coexist and are decided independently; nothing replaces an open
request. Deleting its spawn removes its requests; a hook whose request
disappears returns no answer. Closed requests are evicted oldest-first
only when eligible under the configured cap; open requests and unproven
closed requests of a live spawn are retained, past the cap if necessary.
See [Permissions](permissions.md) for retention and
[requests recorded by an older binary](permissions.md#requests-recorded-by-an-older-binary).

### A relay row left in `check_permission`

While any of its requests holds it, a row stays in `check_permission`
while the agent works: the agent's moves to `working` are held (the hook
is applied, writes no state, and clears `idle_since`). `AskUserQuestion`'s
`PreToolUse` and `Stop` are not held: they move the row to `ask_user` and
`waiting` with the request still open. A request holds the row while it
still awaits an answer and the agent has not proven its dialog gone (see
"Hooks that prove a request's dialog gone" below). Once none does, the
row stays there until the agent's next hook, which after a denied request
is normally the turn's `Stop`.

A request answered at the pane outside agent-director, by a person at
tmux for example, still awaits an answer in agent-director's records,
since nothing recorded that answer. It holds the row only until the agent
proves its dialog gone: the main agent's `Stop` or idle-prompt
Notification after it, or its tool's `PostToolUse` (an allow; Claude rows
only). On a Codex row only its `Stop` or Interrupt proves a main-agent
request. From then
on the agent's moves to `working` apply again, though the request stays
open until `record-pane-answer` closes it, so the row can read `waiting`,
`working` or `ask_user` while one of its requests still awaits an answer.
Limits:

- A subagent's request (`agent_id` set) is never proven by the main
  agent's turn end, since a background subagent can outlive it. Answered
  at the pane outside agent-director, it holds the row's moves to
  `working` until `record-pane-answer`, its tool's `PostToolUse` (Claude
  rows) or the agent's end; the main agent's `Stop` still moves the row to
  `waiting`.
- Claude Code sends no `Stop` when a user interrupts a turn, so after a
  deny at the pane that interrupts the turn, the row stays held until the
  idle-prompt Notification (once the agent idles) or the next turn's
  `Stop`. Codex sends an Interrupt, which ends the turn for these proofs
  and moves the row to `waiting`, and no idle-prompt Notification: a Codex
  turn has one end signal, its `Stop` or Interrupt. When that hook is lost,
  the row stays held until the next turn's `Stop` or Interrupt, or until
  `record-pane-answer`, after which `find-missing` repairs it to `working`.
- A request recorded in the same second a `Stop`, idle prompt or Codex
  Interrupt started is not proven by it, and holds until a later turn end.

For a missed hook and stale `check_permission` recovery, see
[Permissions](permissions.md) and the [caller contract](caller-contract.md#permission-relay).
A scheduled `find-missing` repairs an eligible Claude row to `waiting`
when `idle_since` is set, otherwise `working`; a Codex row always repairs
to `working`, since it sends no idle-prompt Notification.

A row with the relay off is in `check_permission` while the agent's own
prompt waits, with no request on record, so neither move applies to it.

### A request whose tool ran

This is a Claude row's close: Codex's requests carry no `tool_use_id`, and a
Codex `PostToolUse` proves and closes nothing.

A request whose relay hook ended without a confirmed verdict has fallen
back: Claude Code asks in its own prompt, and nothing the relay records
says how that prompt was answered. When the prompt is answered allow,
Claude Code runs the tool, and the agent's `PostToolUse` (or, when the
tool failed, `PostToolUseFailure`) hook carries the tool use's
`tool_use_id`, the same one the relay hook recorded on the request.

So on a relay-on agent (`AGENT_DIRECTOR_RELAY_MODE=on`), a `PostToolUse` or
`PostToolUseFailure` with a `tool_use_id`, with a proof of the dialog gone (see "Hooks that
prove a request's dialog gone" below):

- reads the row's requests recorded with a `tool_use_id` that carry that
  `tool_use_id` and still await an answer;
- judges each one's relay hook as the readers do (its process provably
  gone, or, when it cannot be checked, its `confirm_by` passed) and skips
  one whose hook may still answer it;
- closes each other one only from the row's own agent: `pane_answer` `tool_ran`,
  `decision` `allow`, `decision_reason` `tool_ran`, `hook_gone_at` when
  not yet set, and `proven_gone_at` with `proven_gone_how` `tool_ran` when
  not yet proven, only while the request still awaits an answer. Each close writes `ad.row_mutation.committed` with `writer_process` `hook`.

A matching tool-use hook proves the dialog gone even when the request
is left open because its relay hook may still answer. It closes only allows:
a deny runs no tool-use hook. A missed close can leave a request open;
see [a request answered outside agent-director](permissions.md#a-request-answered-outside-agent-director).
A different or missing `tool_use_id` closes nothing.

### The agent's end closes its requests

A request still awaiting an answer when its agent ends can never be
answered by that agent. The terminal `SessionEnd` (cause `logout`,
`prompt_input_exit` or `exit`) moves the row to `ended` and, in the same
update, closes every request of the row that still awaits an
answer, plus decided requests without a relay confirmation or completed
pane answer, including those recorded by an older binary: both the row
and request changes are written or neither is. Each gets `closed_at`; an
undecided one is also denied with `decision_reason` `ended` (so a relay
hook still polling for it reads a deny), and a decided one whose relay
hook had not confirmed the verdict keeps it. Each deny is written to the
trail as one `ad.row_mutation.committed` (`writer_process` `hook`), before
the row's `ad.spawn.state_transition`. `find-missing`'s mark closes a
`missing` row's requests the same way (`decision_reason` `find_missing`),
and `resume`'s move to `pending` closes any request an older binary
left open on the finished row (`decision_reason` `ended`,
`writer_process` `resume`). The move of a Codex row to `ended` (see "How a
Codex row ends" above), made by `find-missing` or by `pause`, closes them
too, as the `ended` close does: `decision_reason` `ended` and
`writer_process` `find_missing`, so `decide` on such a request returns
`ErrNoOpenPermissionRequest`. Each of these also proves every request of
the row gone together, closed now or before
(`proven_gone_how` `agent_gone`; see "Hooks that prove a request's dialog
gone" below).

A closed request stays closed after a `resume`: it no longer holds the
resumed agent's moves to `working`, is not listed by `get` or `list`,
refuses no `send-keys`, and `decide` refuses it, never with
`ErrRelayFallenBack` (see
[permissions.md](permissions.md#a-request-of-a-finished-spawn-is-closed)).

### Hooks that prove a request's dialog gone

agent-director's own records of a request (its relay hook's ack, a pane
answer, `record-pane-answer`) do not show that the agent's dialog for it
is gone. Until a hook of the agent shows it, the request holds a plain
`send-keys` without a pane hash, and `pause`, to the row
(`ErrDialogMaybeOpen`; see
[permissions.md](permissions.md#the-dialog-hold)), and, while it still
awaits an answer, the row's moves to `working` (see "A relay row left in
`check_permission`" above). On a relay-on agent
(`AGENT_DIRECTOR_RELAY_MODE=on`), these hooks write that proof on the
row's requests not yet proven, as `proven_gone_at` (the hook's clock) and
`proven_gone_how`:

| Hook | Requests it proves | `proven_gone_how` |
| --- | --- | --- |
| `PostToolUse` or `PostToolUseFailure` with a `tool_use_id` (a Claude agent's) | every request carrying that `tool_use_id`, whatever agent-director's records say of it, a subagent's included | `tool_ran` |
| `Stop` with no `agent_id` | every request with no `agent_id` recorded before the second the hook started, one recorded by an older binary included | `turn_end` |
| idle-prompt `Notification` with no `agent_id` (a Claude agent's) | as `Stop` | `turn_end` |
| `Interrupt` with no `agent_id` (a Codex agent's: Esc in a running turn) | as `Stop` | `turn_end` |

`turn_end` means the turn of the agent that asked ended after the request
was written; the main agent's `Stop`, idle-prompt Notification and Codex
Interrupt prove it for a request with no `agent_id`. No other hook proves
anything: not `PreToolUse`, not another Notification, not a `Stop`, idle
prompt or Interrupt carrying `agent_id` (a safety net: Codex sends none),
not a `SubagentStart` or `SubagentStop`, not a Codex `PostToolUse` (its
requests carry no `tool_use_id`), and not a `PostToolUse` with another
`tool_use_id` or none.

- **Written before.** A `Stop`, idle prompt or Interrupt covers only a request whose
  `created_at` is before the second the hook process started, so one
  recorded in that second or later stays unproven until a later proof. An
  idle prompt can run late, and one that read the requests after the next
  turn's permission request would otherwise prove that request gone while
  its dialog is up. It also reads the newest request it may cover before
  its write waits for the store's write lock, and its write covers no
  later request: a request recorded while the hook waits belongs to a
  later turn and stays unproven.
- **Own agent.** A hook from another process proves nothing.
- **Fail-open.** A failed proof write logs to `errors.log` and changes
  nothing else; the requests stay unproven until a later proof.
- **The agent's end.** The terminal `SessionEnd`, `find-missing`'s mark,
  the move of a Codex row to `ended` and `resume`'s move prove every request
  of the row gone (`agent_gone`),
  with their close (see "The agent's end closes its
  requests" above). A retained request of a finished row is proven by the same rule.
- **What a proof releases.** A request proven gone holds no move of the
  row, even while it still awaits an answer (see "A relay row left in
  `check_permission`" above). Proof also releases its dialog hold, but
  relay and fallback checks can still refuse plain `send-keys` or `pause`.
  `get-permission` keeps its delivery facts; decided requests recorded
  before v7 with `agent_gone` proof are treated as closed by those guards
  (see the [caller contract](caller-contract.md#permission-relay)).
- **Subagents.** A request with an `agent_id` is proven only by its
  tool's `PostToolUse` or the agent's end. A denied subagent request runs
  no tool, so it holds a plain `send-keys` without a pane hash, and
  `pause`, until the agent ends; one answered at the pane outside
  agent-director also holds the row's moves to `working` until
  `record-pane-answer`, its `PostToolUse` or the agent's end. On a Codex
  row no `PostToolUse` proves anything, so a subagent's request, allowed or
  denied, holds until the agent ends.

## References

- Claude Code hooks: <https://docs.claude.com/en/docs/claude-code/hooks>
