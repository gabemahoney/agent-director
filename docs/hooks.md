# Hooks

How agent-director's per-Spawn state-tracking hooks coexist with the
operator's own Claude Code hooks. Each Spawn gets nine hook entries
synthesized into its `--settings`; they fire on every Claude lifecycle
event and write the row update that powers `status` / `get` / `list`,
but only for the row's own agent (see "Only the row's own agent moves
the row" below).

For Claude Code's own hooks reference, see:
<https://docs.claude.com/en/docs/claude-code/hooks>.

## The nine state-tracking hooks

agent-director registers one entry per event listed below. Each entry
is in exec form — `{"type": "command", "command": "<abs-path>/agent-director",
"args": ["hook"]}` — so Claude Code starts `agent-director hook`
directly, with no shell in between, and feeds it the payload JSON on
stdin. The hook's parent process is therefore the Claude process that
fired it. The README states the minimum supported Claude Code version.
The `PermissionRequest` entry's arguments are `["hook", "--timeout",
"<N>"]`, N being the same per-hook `timeout` the entry gives Claude Code
(`[relay] timeout_seconds`), so the relay hook knows when Claude Code will
end it (see "PermissionRequest relay path" below).

A Claude Code version older than that ignores `args` and runs the bare
binary through `/bin/sh`, so each hook reaches agent-director with no
verb and its payload on stdin. No hook applies, and the row stays
`pending`; past its grace period `find-missing` notes it `unreported`
(see "A SessionStart that never writes" below), and no hook clears that
note. A no-verb run checks stdin for this case:

- When stdin is not a terminal, the run reads it with a 1 MiB cap and a
  1-second deadline.
- If the input is a hook payload (a JSON object whose `hook_event_name`
  is a non-empty string; the `event_name` alias does not count), the run
  prints nothing, exits 0 and writes one `ad.hook.ignored` trail record
  with reason `no_exec_form`. It opens no store, loads no config, writes
  no `ad.hook.fired` and writes nothing on stderr.
- In every other case it prints help as before: a terminal, a read
  error, empty input, input over 1 MiB, input still open at the deadline,
  or input that is not a hook payload.
- A run with only global flags counts as a no-verb run. With `--home`,
  the record goes under that home.
- `help`, `version` and their aliases (`--help`, `-h`, `--version`, `-v`)
  never read stdin.

The `no_exec_form` record has the fields every `ad.hook.ignored` record
has (see "The `ad.hook.ignored` reasons" below). Because it reads no
row, its `claude_instance_id` comes from the environment (null when
absent or invalid), and `row_session_id` and `row_pane_pid` are always
null.

| Event | Tool matcher | Resulting state (SRD §5.2) |
| --- | --- | --- |
| `SessionStart` | — | `waiting` (also writes `claude_session_id`) |
| `UserPromptSubmit` | — | `working` |
| `PreToolUse` | `*` (all tools) | `working` |
| `PreToolUse` | tool=`AskUserQuestion` | `ask_user` |
| `PostToolUse` | — | `working`; with `relay_mode=on`, it first closes a fallen-back permission request carrying the same `tool_use_id` (see "A request whose tool ran" below) |
| `PostToolUseFailure` | — | as `PostToolUse`: the tool ran and failed |
| `Stop` | — | `waiting` |
| `Notification` | `notification_type` = `idle_prompt`, no `agent_id` | `waiting` when the row is `working`, or `check_permission` with `relay_mode=on` and no permission request still awaiting an answer; any other state: soft refresh. Every applied write records `idle_since` (see "The idle-prompt Notification" below) |
| `Notification` | any other `notification_type`, or with `agent_id` | soft refresh — bumps `last_seen_at`, state unchanged |
| `PermissionRequest` | `*` (all tools) | `check_permission` |
| `SessionEnd` | cause ∈ {`logout`, `prompt_input_exit`, `exit`} | `ended` (also sets `ended_at`, and closes the row's permission requests that still await an answer; see "The agent's end closes its requests" below) |
| `SessionEnd` | any other cause (including empty / `clear` / `compact` / auto-compaction) | soft refresh: bumps `last_seen_at`, state unchanged |

Unknown event names are treated as soft refreshes — the row's
`last_seen_at` updates and an info-level log line records the unknown
name so operators can spot new Claude Code events that need a classifier
update.

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
  The write is one statement under the same gate as every hook (see
  "Only the row's own agent moves the row"). The trail records an
  `ad.spawn.state_transition` from `working` to `waiting` with
  `soft_refresh` false and `triggering_event_name` `Notification`;
- so does a row with `relay_mode=on` in `check_permission` none of whose
  permission requests still awaits an answer (a request awaits one until
  its relay hook confirms a verdict in the store, it is closed at the pane,
  or it is closed with the row by `find-missing`'s mark or the agent's
  end): the agent's turn ended after its
  last request was answered, and the hook that would have moved the row
  (the turn's `Stop`, after a denied request) was lost. The trail records
  the move from `check_permission` to `waiting`;
- a row in any other state gets the soft refresh: `last_seen_at` is
  bumped and the state does not change. A row with the relay off in
  `check_permission`, or one with a request that still awaits an answer,
  is such a row.

Every applied write of this Notification, the soft refresh included, sets
the row's `idle_since` to the current time, and the agent's next applied
hook clears it. `find-missing` reads it when it repairs a relay row left in
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
  can fire while a background subagent is still running (fixed in
  2.1.288, anthropics/claude-code#93672). If that subagent's hook had
  moved the row to `working`, the row reads `waiting`, possibly while
  the subagent's tool still runs, until the subagent's next tool hook
  (`PreToolUse`, `PostToolUse` or `PermissionRequest`).

Setting `CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION=false` in the agent's
environment (`spawn`'s `extra_env`) turns off Claude Code's prompt
suggestions, so the prompt-suggestion fork's stray `PreToolUse` does not
fire.

## Only the row's own agent moves the row

Every hook, of every event, applies only when its parent process (its
`getppid()`, with that pid's start time) is the row's recorded pane
process: the process agent-director created the session with, which is
the agent's Claude Code itself. When the row's recorded pane start time
is unknown, the pid alone decides and the first applied hook records the
start time. The check is part of each database write, so nothing can
change the row between the check and the write.

A hook from any other process that carries the row's
`AGENT_DIRECTOR_INSTANCE_ID` changes nothing: a nested `claude` started
in the agent's shell, a teammate pane split in the agent's session, a
leftover of an earlier launch, or a shell that pipes a payload into
`agent-director hook`. So does every hook while the row records no pane
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
new session id. The payload's session id is recorded, never used to
decide whether a hook applies. The hook makes no tmux call.

### A `claude` that does not exec

When the `claude` on PATH is a launcher or shim that runs Claude Code as
a child instead of exec-ing it, the pane process is the launcher, so
every hook is refused with `pid_mismatch` and the row stays `pending`;
past its grace period `find-missing` finds the launcher running and notes
the row `unreported`, and no hook clears that note. A
hook run through a shell instead of in exec form looks the same: either
way the pane process is the hook's grandparent, and agent-director does
not tell the two apart.

For a SessionStart refused with `pid_mismatch` while the row is
`pending`, and for no other hook, the hook reads its parent's own parent
pid once. When that is the row's pane process, it writes one
`ad.hook.pane_is_grandparent` record after the `ad.hook.ignored` one, with
`claude_instance_id`, `pane_pid`, `pane_command` (null when unreadable),
`parent_pid`, `parent_command`, `advice` (naming both causes) and
`source` = `ad_hook`. Each such SessionStart (startup, `/clear`,
compaction, resume) writes one. The row, stdout and exit stay those of
any ignored hook; an unreadable parent pid writes no record.

### SessionStart before the launch's identity write

The agent starts inside the call that creates its tmux session.
agent-director records the pane process (the launch's identity) only
after that call returns, so the agent's first SessionStart can arrive
while the row still records no pane. An idle agent fires no further
hook, so ignoring that SessionStart would leave the row `pending`.

So when SessionStart's gated write does not apply only because the row
records no pane, the hook waits instead of logging `no_pane_recorded`
at once. It waits while all of these hold:

- the row is `pending`;
- the row records no pane;
- the row has a launch start;
- the time is before the launch start plus the effective
  `pending_grace_seconds` (the same grace period `find-missing` uses);
- less than 540 s have passed since the hook began waiting.

So the wait has two bounds, and it ends at whichever comes first: the
grace bound (the launch start plus the grace period) or the cap (540 s
after the hook began waiting). The cap is measured on the monotonic
clock, so a launch start in the future or a step of the wall clock
cannot stretch the wait past it. `pending_grace_seconds` has no maximum;
with a grace above 540 s the cap ends the wait, while `find-missing`
still counts the row as inside its grace period.

The SessionStart `agent-director hook` entry in the synthesized
`--settings` carries `"timeout": 600`, Claude Code's current default
for command hooks. It is stated explicitly so that a change to Claude
Code's default cannot move the kill below the cap. No other
agent-director entry for SessionStart carries a timeout.

The cap bounds the wait, not the hook. The wait ends about 60 s before
Claude Code would kill the hook, but Claude Code counts its 600 s from the
hook's start, and the hook's store writes before and after the wait (up
to four) can each wait up to `[store] busy_timeout_ms` for a busy store's
lock. `busy_timeout_ms` is accepted up to 2147483647 ms and is not capped
against those 60 s, so a long store wait can still get the hook killed
before it writes its result or its `ad.hook.ignored` record, with no trail
record. So can any other death of the hook (a crash, an out-of-memory
kill). See "A SessionStart that never writes" below for how
`find-missing` reports the row.

While it waits, it re-reads the row every 250 ms. Each re-read is a
read only, so it never blocks the identity write. No sleep runs past
either bound. The wait ends early when:

- a pane is recorded;
- the row changes (it leaves `pending`, or its version moves);
- the row is gone;
- a read fails (logged; the hook stays fail-open).

After the wait, the hook makes the ordinary gated write once more with
the row as it is then, and that write decides the result:

- applied, when the pane now recorded is the hook's parent;
- `pid_mismatch`, when another process waited (a leftover or a stray);
- `no_pane_recorded`, when the row still records no pane at either
  bound;
- no record, when the row is gone.

An identity written just before either bound therefore still applies.

Only SessionStart waits. It is the first hook a launch's agent fires, and
Claude Code holds the agent's first response until its SessionStart hooks
finish, so the hooks of that response fire after the wait. A subagent's
or in-process teammate's SessionStart never waits
(see below). A row with no launch start, or one already past the grace
period, never waits. The wait makes no tmux
call and writes nothing until its final gated write.

### A SessionStart that never writes

An agent whose SessionStart hook died before its write (killed at its
timeout after a long store wait, a crash, an out-of-memory kill), or
whose SessionStart was ignored, runs while its row stays `pending`, and
an idle agent fires no later hook to move it. A row looks the same while
its agent is held at a Claude Code startup screen that comes before any
hook (folder trust, a warning, onboarding or login, an approval).
agent-director cannot tell these apart without reading the screen.

`find-missing` reports such a row and never moves it. Once the row is
past its pending grace period, the `spawn` or `resume` that launched it
is no longer running, its pane is recorded (or adopted by the sweep) and
that pane's process is alive, the sweep writes the `liveness_note`
`unreported` and leaves the row `pending`, in one write that applies only
if no hook wrote first. So the row is noted within
`pending_grace_seconds` plus one `find-missing` period, provided
`find-missing` is scheduled. A dead pane process marks the row `missing`
instead. The note keeps the row's `launch_started_at`. Its
`liveness_unverified_since` is the time a note first flagged the row in
this launch, kept across note changes. A later sweep that finds the agent
alive keeps the note; a sweep that cannot check the agent process may
replace it with an unverified note. The note never replaces a
`provenance_conflict` note: a sweep that finds such a `pending` row's
agent alive keeps `provenance_conflict` rather than clearing it.

`unreported` means the agent is alive but no hook has reported since its
launch: it may sit at a startup screen or idle at its prompt. To act on
it, the caller reads the pane (`read-pane`), then types
(`send-keys --allow-pending`); only a caller that looked should type. A
caller that cannot judge the pane ends the launch with the live-row
sequence of the README's "Caller contract" (`kill`, then `find-missing`)
or hands it to a human. `send-keys` still refuses a
`pending` row without `--allow-pending`, noted or not.

The agent's next applied hook clears the note and
`liveness_unverified_since`, as every applied hook clears both. A
SessionStart that arrives later records the session id and identity and moves the row to
`waiting`; a UserPromptSubmit (for example from the prompt the caller
typed) moves it to `working` and records the session id. A
UserPromptSubmit held because a permission request of the row still
awaits an answer writes nothing but the clearing of `idle_since`, so the
note stays.

### Subagents and in-process teammates

Claude Code runs every subagent inside the agent's own process. It does
the same for an in-process agent-team teammate's turns (the default
`teammateMode`). Their hooks therefore pass the parent-process check.
Claude Code marks them with a non-empty `agent_id` in the payload.
`agent_type` alone does not mark one: a session started with `--agent`
carries it and is the agent itself.

- **SessionStart or SessionEnd with `agent_id`:** the hook changes
  nothing. It is decided from the payload, before the parent-process
  check and before any write. It writes no state and no session id, and
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

### The `ad.hook.ignored` reasons

| Reason | When |
| --- | --- |
| `pid_mismatch` | The hook's parent is not the row's recorded pane process. |
| `no_pane_recorded` | The row records no pane yet. For SessionStart, written only after its bounded wait (see "SessionStart before the launch's identity write"). |
| `subagent_event` | A SessionStart or SessionEnd whose payload carries a non-empty `agent_id`. |
| `no_exec_form` | A no-verb run received a hook payload on stdin, from a Claude Code that does not run exec-form hooks. It is written with no store access. |

Each record carries `claude_instance_id`, `hook_event`, `reason`,
`parent_pid`, `parent_command`, `hook_session_id`, `row_session_id`,
`row_pane_pid` and `source` = `ad_hook`. `hook_session_id` is the
basename of the payload's `transcript_path` with its extension removed
(the text from its last `.` on), or null when the payload gives none. It
never names another row.

Consequences: a nested agent's or a teammate pane's work is not
reflected in the row, and an `ended` row stays `ended` against every
other process's hooks.

## Fail-open invariant

State-tracking hooks **must never block Claude Code**. The `hook` verb
exits 0 with empty stdout on every internal failure, and on every hook
it does not apply:

- Missing `AGENT_DIRECTOR_INSTANCE_ID` env → exit 0, log entry.
- Malformed JSON payload → exit 0, log entry.
- Payload over 1 MiB → exit 0, log entry.
- Config malformed → exit 0, log entry.
- Store open failure → exit 0, log entry.
- No usable `HOME` (unset or empty) → exit 0, log entry (normally on
  stderr), and no store opened or created anywhere: the store path `~/…`
  is refused, never resolved against another home such as the passwd
  entry, and no trail record is written. A relayed PermissionRequest
  still gets its deny envelope (see below).
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
- Hook for an id with no row → exit 0, nothing written.

All log entries land in `~/.agent-director/errors.log` (configurable
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
included, emits a `deny` decision envelope on stdout before the hook
exits, so the tool is denied. Once the request is recorded, the hook writes
only an answer it has first confirmed in the store; any failure after that
ends with no answer, so Claude Code asks in its own permission prompt and
the request reads `fallen_back`. See "PermissionRequest relay path" below
and "Permission relay" in `architecture.md`.

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
already had its turn; our UPSERT lands on top with the most recent
`last_seen_at`. For relay-mode permission decisions the ordering
matters: our decision envelope on stdout is the one Claude consumes,
regardless of what earlier-running user hooks emit.

## Misbehaving user hooks

A user hook can stall (long-running tool call), exit non-zero, or write
malformed stdout. Each of these has a different blast radius:

- **Slow user hook** — Claude Code waits for every hook entry to return
  before consuming the event. A 30-second user-script hook stalls the
  entire pipeline; agent-director's state UPSERT doesn't land until
  the user hook returns. The `status` verb will keep showing the
  pre-event state during the stall.
- **Non-zero user hook** — for `PreToolUse`, a non-zero exit can block
  the tool call (Claude Code's policy). agent-director's hook still
  runs and writes its UPSERT either way; the row state is independent
  of whether the tool was actually executed.
- **Malformed user hook stdout** — irrelevant for state-tracking hooks
  (they don't emit stdout). A poorly-formed permission-decision
  envelope from a user hook could confuse the relay path;
  agent-director's relay hook is always last, so it wins the decision
  channel.

The recommended mitigation is to put hooks the operator does NOT want
running inside agent-director Spawns behind a `AGENT_DIRECTOR_INSTANCE_ID`
env-var guard:

```bash
# Inside ~/.claude/settings.json hook command:
if [ -n "$AGENT_DIRECTOR_INSTANCE_ID" ]; then
  exit 0  # skip user hook for agent-director Spawns
fi
# ... normal user-hook body ...
```

This lets the operator opt-out per Spawn surface without flipping
`--setting-sources project,local` at every `spawn` call.

## Persistent hooks (the `help`-on-SessionStart pair)

The install skill (Epic 12) adds two persistent hook entries to the
user's `~/.claude/settings.json`:

- `agent-director help` on `SessionStart`
- `agent-director help` on `SessionEnd matcher=compact`

These are **not** per-Spawn; they fire on every Claude session the
operator runs, regardless of whether agent-director launched it.
They inject the verb list into the new conversation so the model
knows the supervision API surface after a `/compact` or fresh
session. Mirrors the `bees sting` pattern.

Both entries are in shell form (`"command": "<install path> help"`, no
`args`) and name the `help` verb explicitly. A Claude Code that does not
run exec-form hooks still runs `agent-director help`, so the no-verb
`no_exec_form` check never applies to them. The same holds for the
per-Spawn `inject_help_hook` SessionStart entry, which is also shell
form.

### Why both events

- **`SessionStart`** — a brand-new Claude session starts with no
  context about agent-director. The hook injects the verb list so
  the model can reference `mcp__agent-director__spawn` and friends
  immediately.
- **`SessionEnd matcher=compact`** — `/compact` is Claude Code's
  manual conversation compaction. It tears down the current session
  and starts a fresh one. The compact-side hook fires BEFORE the
  new session boots, so by the time SessionStart runs, the verb
  list is already queued for re-injection.

### Why not embed in the synthesized `--settings`?

The per-Spawn hooks are injected inline via `--settings` at spawn
time and only apply to Spawns agent-director launched. The
`help`-on-SessionStart pair, by contrast, must fire on EVERY Claude
session — including the operator's own interactive ones where
they want to spawn something from inside Claude. That requires
persistent settings, not per-Spawn settings.

The asymmetry is deliberate: per-Spawn state-tracking is for
agent-director's own correctness; persistent help is for the
operator's discoverability.

### Idempotency and preserve-other-hooks

The install script uses `jq` to add the entries to
`hooks.SessionStart` and `hooks.SessionEnd` (with matcher=compact)
without disturbing what's already there. Re-running `install.sh`
matches existing entries by command string and skips duplicates
(SRD §16.2 idempotency).

The uninstall script matches entries by the install-root prefix in
their `command` field — it removes ONLY what install.sh wrote.
Pre-existing user hooks in those events stay verbatim.

### What `agent-director help` actually outputs

The persistent hook runs `agent-director help` which produces the
manifest-driven verb list as JSON. Claude Code captures the hook's
stdout and injects it as a system-message tool-availability hint
in the new conversation. The model sees the same surface a script
reading the CLI's `--help` would.

### Caveat — install fail-closed gap

If `agent-director` is missing from PATH at session start time
(e.g. mid-uninstall), the hook fails to invoke and Claude Code
falls back to no-injection — the conversation proceeds without the
verb list. This is the install-side analogue of the
permissions.md "structural caveat": fail-closed requires the
binary to actually run. The install script's upgrade-safety
pattern (versioned binary + atomic symlink swap) is specifically
designed to keep this window closed.

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
`timeout`) gives the instant Claude Code will end it: its start plus N.
A hook entry spawned before the argument existed reads the loaded config's
`[relay] timeout_seconds` instead. Every step below keeps time against that
instant, with a 2 s reserve before it for writing the answer and exiting:

| Instant | What happens there |
| --- | --- |
| start + N − 3 s | the poll ends: no verdict yet means the timeout deny |
| start + N − 2 s | the last moment a store write may commit; later, the hook gives no answer |
| start + N | Claude Code ends the hook |
| start + N + 2 s | the request's settle instant (`settled_at`), `confirm_by` |

So `timeout_seconds` of 3 or less leaves no time to poll, and at 2 or less
no time to answer.

### The steps

1. **The first write.** One store transaction moves the row to
   `check_permission` and records the request, both or neither, under the
   same gate as every hook (see "Only the row's own agent moves the row";
   a PermissionRequest from another process records nothing and writes
   nothing to stdout). The request carries the hook's own pid, start time
   and pid namespace, the payload's `tool_use_id` and `agent_id`, and its
   settle instant. Its wait for the store's write lock ends at the 2 s
   reserve; a lock not taken by then, or any other failure, writes nothing
   and returns a deny envelope (the tool is denied).
2. **The poll.** `internal/hook/polling.go`'s `Poll` reads the hook's own
   request (by the UUIDv4 `request_token` it minted) every
   `max(50ms, relay.poll_base_ms + uniform(0, relay.poll_jitter_ms))`,
   never sleeping past the poll's end. The 50 ms floor is load-bearing: a
   misconfigured `relay.poll_base_ms=0, relay.poll_jitter_ms=0` must not
   pin CPU. The loop only reads; it never writes the request.
3. **The ack, then the answer.** On reading a verdict the hook commits
   `delivered_at` in one statement that returns the verdict it confirmed,
   and only then writes that verdict as its envelope and exits. Inside
   that write's transaction, once it holds the store's write lock and
   before the statement, the hook checks that its parent process is still
   the Claude Code that started it (pid and start time); if not, nothing
   is written. So a Claude Code that exited while the hook waited for the
   lock gets no confirmation.
4. **The timeout deny.** At the poll's end, one guarded statement, with
   the same parent check inside its transaction, records `deny`,
   `decision_reason` `timeout` and `delivered_at` together, only while the
   request is still undecided; a verdict that landed first is confirmed
   and written instead. The deny leaves the row's state as it is.

The hook never writes an answer it has not confirmed in the store, because
Claude Code acts on a hook's JSON whatever its exit code. A changed parent,
a store write that fails or misses the reserve, a request deleted under it
(its spawn row removed) or five failed reads in a row end the hook with
empty stdout: Claude Code asks in its own permission prompt, and readers
find the request fallen back once they see the hook gone.

### Writing the envelope

The handler emits exactly one line on stdout — the
`hookSpecificOutput` envelope per SRD §6.3 — or nothing.
Non-PermissionRequest events leave stdout empty (state-tracking has no
envelope contract).

### `AGENT_DIRECTOR_RELAY_MODE` env var

The handler reads the relay mode from
`os.Getenv("AGENT_DIRECTOR_RELAY_MODE")`, NOT from the DB's
`spawns.relay_mode` column. This separation is the SRD §6.5
fail-closed safety guarantee: a DB-unreachable or schema-mismatch
failure still surfaces the correct relay decision because the env
var was set on the Spawn's tmux session at launch time (Epic 3) and
survives any DB-side breakage.

### Fail-closed boundary

`internal/hook/handler.go` runs a `failClosed` helper on every
failure path before the relay branch (instance id missing or invalid,
classify failure), and `cmd/agent-director`'s `runHook` writes the same
deny when the config cannot be loaded or the store cannot be opened. The
helper writes a deny envelope only when `relayActive`
is true AND the payload's peeked event name is `PermissionRequest`
(the b.45p gate). Failures where the event name is unknowable —
stdin read failure, unparseable payload — exit silently instead,
because a permission-shaped envelope from a non-PermissionRequest
process would be routed by fd to the in-flight tool and race the
legitimate PermissionRequest sibling. `runRelay` denies the same way
when it cannot mint a token or its first write fails or is cut; once
the request is recorded it writes a confirmed answer or nothing (see
"The steps" above). See `permissions.md` for the enumerated failure
modes.

### Per-request rows in `permission_requests`

The v2 schema keys `permission_requests` by a composite
`UNIQUE(claude_instance_id, request_token)`. Each relay invocation
mints its own UUIDv4 `request_token` and INSERTs its own row, so
concurrent PermissionRequest events for the same Spawn coexist and
are decided independently — a decision targets exactly one row, and
one poller. Rows are INSERT-only: nothing replaces an open row, and a
polling loop that sees `sql.ErrNoRows` (possible via `ON DELETE
CASCADE` when the spawn row is deleted) ends with no answer. Closed rows
(those no longer awaiting an answer) are evicted oldest-first when the
table exceeds `relay.permission_request_cap`, except a spawn's newest
request while that spawn has a request that still awaits an answer
(`decide` relies on it for a request recorded before this release; see
[permissions.md](permissions.md#requests-recorded-before-this-release)).

### A relay row left in `check_permission`

While any of its requests still awaits an answer, a row stays in
`check_permission`: the agent's moves to `working` are held (the hook is
applied, writes no state, and clears `idle_since`). Once none does, the
row stays there until the agent's next hook, which after a denied request
is normally the turn's `Stop`. If that hook is lost:

- the idle-prompt Notification moves the row to `waiting` (see "The
  idle-prompt Notification" above);
- a scheduled `find-missing` moves the row out of `check_permission` in
  one guarded statement once none of its requests still awaits an answer
  and none has a relay hook that may still run (one whose `confirm_by` has
  not passed and that is not provably gone): to `waiting` when `idle_since`
  is set, otherwise to `working`. A request closed with the row (when
  `find-missing` marked it `missing` or its agent ended it) is not judged:
  the row is back in `check_permission` only through a later hook (after a
  `resume`), and that request's leftover hook cannot move the row, so a
  hook that cannot be checked does not hold the row there until its
  `confirm_by`. The statement applies only while the row
  is still in `check_permission` with `relay_mode=on`, holds the snapshot
  the sweep read and has no request awaiting an answer, so a hook or a new
  request that lands first wins. It writes one `ad.find_missing.tick` with
  `reconciliation_reason` `stale_check_permission`; the row is in neither
  of `find-missing`'s result lists.

A row with the relay off is in `check_permission` while Claude Code's own
prompt waits, with no request on record, so neither move applies to it.

### A request whose tool ran

A request whose relay hook ended without a confirmed verdict has fallen
back: Claude Code asks in its own prompt, and nothing the relay records
says how that prompt was answered. When the prompt is answered allow,
Claude Code runs the tool, and the agent's `PostToolUse` (or, when the
tool failed, `PostToolUseFailure`) hook carries the tool use's
`tool_use_id`, the same one the relay hook recorded on the request.

So on a relay-on agent (`AGENT_DIRECTOR_RELAY_MODE=on`), a `PostToolUse` or
`PostToolUseFailure` with a `tool_use_id`, before its ordinary write:

- reads the row's requests recorded from this release on that carry that
  `tool_use_id` and still await an answer;
- judges each one's relay hook as the readers do (its process provably
  gone, or, when it cannot be checked, its `confirm_by` passed) and skips
  one whose hook may still answer it;
- closes each other one with one guarded statement, under the same gate as
  every hook (only the row's own agent): `pane_answer` `tool_ran`,
  `decision` `allow`, `decision_reason` `tool_ran`, and `hook_gone_at`
  when not yet set, only while the request still awaits an answer. Each
  close is written to the trail as one `ad.row_mutation.committed`
  (`writer_process` `hook`).

The close runs first so that the hook's own move to `working` is no longer
held by the request it closed. It closes only allows: Claude Code runs no
hook for a deny at its prompt, so a request denied at the pane outside
agent-director is closed with `record-pane-answer` (see
[permissions.md](permissions.md#a-request-answered-outside-agent-director)).
The close is fail-open, and the hook that makes it can die like any other,
so it is a help, not a guarantee. A hook with another `tool_use_id`, or
none, closes nothing.

### The agent's end closes its requests

A request still awaiting an answer when its agent ends can never be
answered by that agent. The terminal `SessionEnd` (cause `logout`,
`prompt_input_exit` or `exit`) moves the row to `ended` and, in the same
store transaction, closes every request of the row that still awaits an
answer: both are written or neither is. Each gets `closed_at`; an
undecided one is also denied with `decision_reason` `ended` (so a relay
hook still polling for it reads a deny), and a decided one whose relay
hook had not confirmed the verdict keeps it. Each deny is written to the
trail as one `ad.row_mutation.committed` (`writer_process` `hook`), before
the row's `ad.spawn.state_transition`. `find-missing`'s mark closes a
`missing` row's requests the same way (`decision_reason` `find_missing`),
and `resume`'s move to `pending` closes any request a release before this
one left open on the finished row (`decision_reason` `ended`,
`writer_process` `resume`).

A closed request stays closed after a `resume`: it no longer holds the
resumed agent's moves to `working`, is not listed by `get` or `list`,
refuses no `send-keys`, and `decide` refuses it, never with
`ErrRelayFallenBack` (see
[permissions.md](permissions.md#a-request-of-a-finished-spawn-is-closed)).

## References

- Claude Code hooks: <https://docs.claude.com/en/docs/claude-code/hooks>
- Empirical investigation (gitignored, in-repo):
  `reference/claude-settings-research.md`
