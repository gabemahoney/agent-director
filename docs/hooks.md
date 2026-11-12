# Hooks

agent-director adds state-tracking hooks to supervised Claude Code and Codex
launches without replacing your own hooks. Cursor reports through its managed
gateway. These reports supply the states returned by `status`, `get`,
`get-many` and `list`.

[Settings](settings.md) describes the launch settings. [Permissions](permissions.md)
owns the permission relay. For Claude Code's own hook configuration, see its
[hooks reference](https://docs.claude.com/en/docs/claude-code/hooks).

## The eleven state-tracking hooks

Every Claude launch registers `SessionStart`, `UserPromptSubmit`, `PreToolUse`,
`PostToolUse`, `PostToolUseFailure`, `Stop`, `Notification`, `SessionEnd`,
`PermissionRequest`, `SubagentStart` and `SubagentStop` through `--settings`.
The entries run the absolute path of `agent-director` in exec form.
Use the Claude Code version in [Requirements](install.md#requirements).

| Event | Reported state |
| --- | --- |
| `SessionStart` | `waiting`; records the current `session_id` |
| `UserPromptSubmit` | `working` |
| `PreToolUse` | `working`, or `ask_user` for `AskUserQuestion` |
| `PostToolUse`, `PostToolUseFailure` | `working` |
| `Stop` | `waiting` |
| Main-agent `Notification` with `notification_type: idle_prompt` | Returns `working` to `waiting`; can also release stale relay state as described below |
| Other `Notification` | State unchanged |
| `PermissionRequest` | `check_permission` |
| `SessionEnd` with cause `logout`, `prompt_input_exit` or `exit` | `ended` |
| Other `SessionEnd`, including `clear` and `compact` | State unchanged |
| `SubagentStart`, `SubagentStop`, unknown events | State unchanged |

An unchanged-state report refreshes `last_seen_at`. Permission requests can hold
moves to `working`; see [PermissionRequest relay path](#permissionrequest-relay-path).
Subagent session events do not change the main session.

State reads apply the configured [settle time](configuration.md#settle-time-state).
`ended` is visible immediately.

### The idle-prompt Notification

Claude Code normally sends `idle_prompt` about 60 s after the main turn ends.
Before Claude Code 2.1.288 it can fire while a background subagent is still running
(see Limits below). A main-agent idle notification returns `working` to
`waiting`; other states remain unchanged except for
[stale relay state](#a-relay-row-left-in-check_permission).

Limits:

- On Claude Code 2.1.280 through 2.1.287, the idle-prompt Notification
  can fire while a background subagent is still running; 2.1.288 and later
  wait until background subagents finish. If that subagent's hook had
  moved the row to `working`, the row reads `waiting`, possibly while
  the subagent's tool still runs, until the subagent's next tool hook.
- State can lag until this notification arrives. A tool hook arriving after
  it can report `working` again; a notification overlapping a new turn can
  temporarily report `waiting`.

To disable Claude Code's prompt-suggestion background work, set
`CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION=false` through the spawn's `extra_env`.

## Cursor reporting

Cursor reports startup, conversation changes, submitted prompts, completed
turns and intentional termination through the managed gateway, not optional
user tool hooks or pane-text matching.

Confirmed startup reports `waiting`, accepted input reports `working`, and
completed turns report `waiting`. Intentional termination reports `ended`
only after the runtime exits. Launch return and pre-trust results do not
confirm readiness. Reporting remains enabled with `inject_help_hook` off or
`no_pre_trust` on.

See [Fresh Cursor use](usage.md#fresh-cursor-use) for conversation handling,
[Cursor permissions](permissions.md#cursor-spawns) for authorization, and
[Cursor operation boundaries](caller-contract.md#cursor-operation-boundaries)
for failed or uncertain operations.

## Hooks from a Codex agent

Codex launches use `agent-director hook --agent codex`. The default without
`--agent` is `claude`; `cursor` is reserved for the managed Cursor reports.

### The Codex entry set

Every Codex spawn, reuse and resume adds eight entries on the command line.
It writes no hook configuration to your Codex folder.

| Event | Time limit |
| --- | --- |
| `SessionStart` | 600 s |
| `PreToolUse`, `PermissionRequest` | `[relay] timeout_seconds` |
| `SessionEnd`, `Interrupt` | 3 s |
| `UserPromptSubmit`, `PostToolUse`, `Stop` | Codex's default |

With `inject_help_hook` on, a `SessionStart` help entry is added with
`additionalContextLimit = 4000`. With `disable_ask_user` on, an additional
`PreToolUse` entry blocks the asynchronous question tool; see
[Permissions](permissions.md#disable_ask_user-config).

agent-director's entries are trusted even with `no_pre_trust` or failed
pre-trust. They do not replace or disable user, group or project hooks, or
change those hooks' recorded trust. Codex configuration must permit these
hooks to run; see [Codex hook configuration](operator-actions.md#a-codex-row-stays-pending-or-goes-missing-for-another-reason).

### The Codex states

| Event | Reported state |
| --- | --- |
| `SessionStart` | `waiting`; records `session_id` |
| `UserPromptSubmit` | `working` |
| `PreToolUse`, `PostToolUse` | `working`, subject to permission holds; a late hook from an ended main turn leaves state unchanged |
| `Stop`, main-agent `Interrupt` | `waiting` |
| `PermissionRequest` | `check_permission` |
| `SessionEnd` | End report only; see [How a Codex row ends](#how-a-codex-row-ends) |

Codex question tools report `working`, never `ask_user`. Codex sends no
idle-prompt Notification and registers no `PostToolUseFailure`, `Notification`,
`SubagentStart` or `SubagentStop` entry. Such events, if manually registered,
leave state unchanged. Subagent session events are ignored, but subagent and
internal-thread tool and permission events, such as `/review`'s, still move
the row even though Codex subagent records are not tracked.

### The Codex turn

`Interrupt` is Codex's Esc and ends the current main turn. `Stop` and
`Interrupt` report `waiting`; late tool hooks from an ended main turn do not
return it to `working`.

For a turn that sends no end hook, follow
[A Codex turn that ends in an error](caller-contract.md#a-codex-turn-that-ends-in-an-error).
Recovery of `waiting` is not proof that a permission dialog is gone; see
[the Codex dialog hold](permissions.md#how-the-hold-differs-on-codex-rows).

### The session

Codex sends its first `SessionStart` at the first prompt, not when the screen
opens. Until then the spawn stays `pending`.

`/clear`, `/new`, `/fork` and `/resume` start another session in the same
process. Its identity is recorded at the next prompt. Until then, including
for `resume`, agent-director still refers to the previous session.

### How a Codex row ends

A Codex spawn reaches `ended` only after an end report and confirmation that
its agent process exited. `status`, `get`, `get-many`, `list`, `sweep` or
`pause` can confirm it. `/exit` alone does not immediately change the reported
state. See [When a Codex agent exits](caller-contract.md#when-a-codex-agent-exits)
for caller handling.

### The shell between Codex and the hook

Keep shell startup files silent on stdout: `.zshenv` for zsh, and the file
named by `BASH_ENV` for bash (see
[what a Codex launch carries](settings.md#what-a-codex-launch-carries)).
Send startup messages to stderr instead.

For diagnosis and repair, see
[A Codex agent shows its own approval dialog after a relayed verdict](operator-actions.md#a-codex-agent-shows-its-own-approval-dialog-after-a-relayed-verdict).

### Limits

Codex's `/review` and other internal threads can leave the reported state
`working` after their work ends. For stale state, follow the
[caller contract](caller-contract.md#caller-contract).

## Only the row's own agent moves the row

Only the agent launched for a spawn can report its state or use its relay.
A nested agent, another tmux pane or a process left from another launch
cannot report on its behalf, even with the same `AGENT_DIRECTOR_INSTANCE_ID`.

A Claude or Codex launch wrapper must `exec` the agent rather than run it as
a child. See [Requirements](install.md#requirements) and
[wrapper diagnosis](operator-actions.md#a-row-stays-pending-and-the-trail-shows-pid_mismatch).

### Subagents and in-process teammates

Claude subagents and in-process teammates can report tool and permission
events through the main process. Those events affect the spawn's state.
Their `SessionStart` and `SessionEnd` do not replace the main session or end
the spawn.

A subagent's idle notification does not report `waiting`. Its permission
requests use the same relay but retain their own
[dialog hold](permissions.md#the-dialog-hold).

## What the hooks write for subagent records

Claude launches track subagents through `SubagentStart`, `SubagentStop`, the
`Agent` tool's launch events and main-agent `Stop` snapshots. Codex launches
do not track subagent records.

Read them with `list-subagents`. The fields, confirmation rules and limits
are in [Read an agent's subagent records](usage.md#read-an-agents-subagent-records).
Subagent prompts, messages, result text, errors and output files are not stored.

## Fail-open invariant

State-tracking failures leave the agent running; they do not confirm its
state. State hooks add no stdout context or permission decision, and
`SubagentStop` does not block a stop. Relay permission decisions have a
separate [fail-closed boundary](#fail-closed-boundary).

For a spawn that never reports, see
[Pending rows and startup screens](caller-contract.md#pending-rows-and-startup-screens).
Diagnostics go to the configured
[error log](configuration.md#keys), or stderr if it cannot be opened.

## Fire order

Claude Code combines hooks from these settings tiers, in order:

```text
user → project → local → agent-director (--settings) → managed policy
```

agent-director runs last among user-installed tiers. Your hooks remain active;
their delays can delay state reporting. See the
[Claude Code hooks reference](https://docs.claude.com/en/docs/claude-code/hooks)
for native hook behavior.

## Your own hooks

Your hooks run before agent-director's. A slow one holds back the state update
until it returns. agent-director's relay hook runs after yours, so its
permission decision wins. Hooks receive `AGENT_DIRECTOR_INSTANCE_ID` in Claude
and Codex agents. To skip one of your hooks inside supervised agents, see
[Operator actions](operator-actions.md#a-user-hook-interferes-with-supervised-agents).

## Persistent hooks (the `help`-on-SessionStart pair)

The installer adds `agent-director help` to Claude Code's `SessionStart` and
`SessionEnd` with matcher `compact` in `~/.claude/settings.json`. These run
for every Claude session, not only supervised spawns.

Reinstalling preserves other entries and skips matching help commands.
Uninstall removes the installer's entries. Installation choices are in
[Installation](install.md#claude-code-and-codex); per-launch guidance is in
[Settings](settings.md#defaultsinject_help_hook-config).

### What `agent-director help` actually outputs

`help` supplies the complete JSON verb list. Claude Code receives it as hook
context. Codex receives a structured `SessionStart` response at the first
prompt and the next prompt after `/clear`, `/new`, `/resume` or `/fork`.
Outside a Codex `SessionStart` hook, `help` prints the plain list.

With `inject_help_hook` enabled, Cursor receives the complete plain JSON list
as MCP initialization instructions at fresh startup, latest-session load and
confirmed `/clear` or `/new`. No extra user turn or replayed prompt is needed.
This uses a session-scoped registration, not dashboard MCP configuration.

Completed initialization confirms transport, not that the model consumed the
guidance. Native consumption for fresh, loaded and replacement conversations
remains unverified. Cursor has no supported ACP compaction callback on this
route; agent-director cannot re-inject guidance after compaction, and native
retention is unverified. See
[Cursor operation boundaries](caller-contract.md#cursor-operation-boundaries)
for deadlines and failure handling.

A Claude or Codex help command that cannot run injects no verb list; the
conversation goes on.

## PermissionRequest relay path

With `relay_mode=on`, permission requests go to the orchestrator.
[Permissions](permissions.md#relay-mode) owns decisions, timeouts, delivery
fields, concurrent requests and retention. Also see
[requests recorded by an older binary](permissions.md#requests-recorded-by-an-older-binary).

### Fail-closed boundary

For relay guarantees and caller handling of uncertain delivery, see
[Permission relay](caller-contract.md#permission-relay).

### A relay row left in `check_permission`

See [Permission relay](caller-contract.md#permission-relay) and
[Permissions: Delivery](permissions.md#delivery) for state holds and missed-hook
recovery. Do not infer permission readiness from `waiting` alone.

### The agent's end closes its requests

Unanswered or unconfirmed requests, including those recorded by an older binary,
close when their spawn ends or is marked `missing`; `resume` does not reopen them.
See [A request of a finished spawn is closed](permissions.md#a-request-of-a-finished-spawn-is-closed).

### Hooks that prove a request's dialog gone

The [dialog hold](permissions.md#the-dialog-hold) defines which agent reports
prove a dialog gone. A recorded decision or delivery acknowledgement alone
does not prove it.
