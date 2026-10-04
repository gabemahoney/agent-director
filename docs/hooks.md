# Hooks

How agent-director's per-Spawn state-tracking hooks coexist with the
operator's own Claude Code hooks. Each Spawn gets eight hook entries
synthesized into its `--settings`; they fire on every Claude lifecycle
event and write the row update that powers `status` / `get` / `list`,
but only for the row's own agent (see "Only the row's own agent moves
the row" below).

For Claude Code's own hooks reference, see:
<https://docs.claude.com/en/docs/claude-code/hooks>.

## The eight state-tracking hooks

agent-director registers one entry per event listed below. Each entry
is in exec form — `{"type": "command", "command": "<abs-path>/agent-director",
"args": ["hook"]}` — so Claude Code starts `agent-director hook`
directly, with no shell in between, and feeds it the payload JSON on
stdin. The hook's parent process is therefore the Claude process that
fired it. The README states the minimum supported Claude Code version.

A Claude Code version older than that ignores `args` and runs the bare
binary through `/bin/sh`, so each hook reaches agent-director with no
verb and its payload on stdin. No hook applies, and the row stays
`pending`. A no-verb run checks stdin for this case:

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
- `help`, `--help` and `version` never read stdin.

The `no_exec_form` record has the fields every `ad.hook.ignored` record
has (see "The `ad.hook.ignored` reasons" below). Because it reads no
row, its `claude_instance_id` comes from the environment (null when
absent or invalid), and `row_session_id`, `row_pane_pid` and
`launcher_pid` are always null.

| Event | Tool matcher | Resulting state (SRD §5.2) |
| --- | --- | --- |
| `SessionStart` | — | `waiting` (also writes `claude_session_id`) |
| `UserPromptSubmit` | — | `working` |
| `PreToolUse` | `*` (all tools) | `working` |
| `PreToolUse` | tool=`AskUserQuestion` | `ask_user` |
| `PostToolUse` | — | `working` |
| `Stop` | — | `waiting` |
| `Notification` | — | soft refresh — bumps `last_seen_at`, state unchanged |
| `PermissionRequest` | `*` (all tools) | `check_permission` |
| `SessionEnd` | cause ∈ {`logout`, `prompt_input_exit`, `exit`} | `ended` (also sets `ended_at`) |
| `SessionEnd` | any other cause (including empty / `clear` / `compact` / auto-compaction) | soft refresh: bumps `last_seen_at`, state unchanged |

Unknown event names are treated as soft refreshes — the row's
`last_seen_at` updates and an info-level log line records the unknown
name so operators can spot new Claude Code events that need a classifier
update.

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
every hook's parent is the launcher's child and every hook is refused
with `pid_mismatch`; the row stays `pending`. A hook run through a shell
instead of in exec form looks the same (the pane process is Claude Code,
the hook's parent that shell), and agent-director does not tell the two
apart.

After refusing a hook with `pid_mismatch`, and only then, the hook reads
its parent's own parent pid once. When that is the row's recorded pane
process:

- the `ad.hook.ignored` record carries `launcher_pid`, that pid, for any
  event and any row state;
- when the hook is a SessionStart and the row is still `pending`, it also
  writes one `ad.hook.launcher_detected` record after it, with
  `claude_instance_id`, `launcher_pid`, `launcher_command` (null when
  unreadable), `parent_pid`, `parent_command`, `advice` and `source` =
  `ad_hook`. The `advice` names both causes and, for a launcher, what to
  change. Every
  such SessionStart writes one (startup, `/clear`, compaction, resume).

Neither changes whether the hook applies: the row, stdout and exit are
those of any ignored hook. A failed read leaves `launcher_pid` null and
writes no `ad.hook.launcher_detected`; a failed trail write changes
nothing.

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
Code's default cannot move the kill below the cap. The hook therefore
always ends its own wait about 60 s before Claude Code would kill it, and
writes its `ad.hook.ignored` record instead of being killed with no
trail record. No other agent-director entry for SessionStart carries a
timeout.

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

Only SessionStart waits. Claude Code sends no prompt to the agent until
its SessionStart hooks finish, so every later hook fires after the
wait. A subagent's or in-process teammate's SessionStart never waits
(see below). A row with no launch start, or one already past the grace
period, never waits. The wait makes no tmux
call and writes nothing until its final gated write.

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
  process.

### The `ad.hook.ignored` reasons

| Reason | When |
| --- | --- |
| `pid_mismatch` | The hook's parent is not the row's recorded pane process. |
| `no_pane_recorded` | The row records no pane yet. For SessionStart, written only after its bounded wait (see "SessionStart before the launch's identity write"). |
| `subagent_event` | A SessionStart or SessionEnd whose payload carries a non-empty `agent_id`. |
| `no_exec_form` | A no-verb run received a hook payload on stdin, from a Claude Code that does not run exec-form hooks. It is written with no store access. |

Each record carries `claude_instance_id`, `hook_event`, `reason`,
`parent_pid`, `parent_command`, `hook_session_id`, `row_session_id`,
`row_pane_pid`, `launcher_pid` and `source` = `ad_hook`. `launcher_pid`
is null unless the reason is `pid_mismatch` and the parent's own parent
is the row's pane process (see "A `claude` that does not exec"). `hook_session_id` is the
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
- DB write failure → exit 0, log entry.
- Unknown event name → exit 0, soft refresh, log entry.
- Hook from a process other than the row's recorded pane process, or for
  a row that records no pane → exit 0, nothing written, one
  `ad.hook.ignored` trail record (plus one `ad.hook.launcher_detected`
  for a `pending` row's SessionStart behind a `claude` that does not
  exec). A SessionStart for a `pending` row
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
via `[log] error_log_path` in `config.toml`). A missed state update is
annoying but never breaks a Claude session.

SessionStart's wait for the launch's identity write is the one case
where the hook deliberately takes time. It can delay the hook's exit
until the earlier of the launch start plus the effective
`pending_grace_seconds` and 540 s after the wait began, and Claude
Code's first response waits for SessionStart hooks to finish. The 540 s
cap keeps the wait inside the entry's 600 s timeout. It happens only in
the race described above; the hook still exits 0 with empty stdout
whatever the wait's result.

The relay-mode `PermissionRequest` path is fail-*closed*: any internal
error emits a `deny` decision envelope on stdout before the hook exits.
See "Permission relay" in `architecture.md`.

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
on PermissionRequest events: after the normal state-tracking write
(state → `check_permission`), it records the request and enters a
polling loop, and only returns when the orchestrator's `decide` verb has
written a row in `permission_requests` or the relay times out. The branch
runs only when that write applied: a PermissionRequest from another
process records no request and writes nothing to stdout, and so does
one whose request insert, which carries the same check, does not
apply. See `permissions.md` for the user-facing
contract; this section covers the implementation.

### Polling loop

`internal/hook/polling.go` implements `Poll(ctx, store, clock, cfg,
instanceID, requestToken, rng)` — the token is the per-request UUIDv4
minted by `runRelay`, so each polling loop reads only its own row.
Each iteration:

1. `GetPermissionRequest(instanceID, requestToken)`:
   - `sql.ErrNoRows` → the row is gone. Rows are INSERT-only, so this
     is rare — normally an ON DELETE CASCADE from a spawn delete.
     Return fail-closed.
   - other SQL error → increment a retry counter; abandon after 5
     consecutive errors (`pollMaxReadRetries`).
   - row found, decision NULL → sleep and loop.
   - row found, decision populated → return the decision.
2. Check the timeout deadline; if expired → return fail-closed.
3. Sleep `max(50ms, base + uniform(0, jitter))`. The 50ms floor is
   load-bearing: a misconfigured `relay.poll_base_ms=0,
   relay.poll_jitter_ms=0` must not pin CPU.

The sleeper uses `time.NewTimer + select` so `ctx.Done()` preempts
the sleep cleanly. The loop never sleeps past the deadline.

### Writing the envelope

The handler emits exactly one line on stdout — the
`hookSpecificOutput` envelope per SRD §6.3. Non-PermissionRequest
events leave stdout empty (state-tracking has no envelope contract).

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
pre-relay failure path (instance-id missing, transition, UPSERT,
session-id). The helper writes a deny envelope only when `relayActive`
is true AND the payload's peeked event name is `PermissionRequest`
(the b.45p gate). Failures where the event name is unknowable —
stdin read failure, unparseable payload — exit silently instead,
because a permission-shaped envelope from a non-PermissionRequest
process would be routed by fd to the in-flight tool and race the
legitimate PermissionRequest sibling. `runRelay` itself runs the polling
loop and writes either the decision envelope or — on
timeout/ctx-cancel/preemption/read-retry-exhaustion — a deny
envelope. See `permissions.md` for the enumerated failure modes.

### Per-request rows in `permission_requests`

The v2 schema keys `permission_requests` by a composite
`UNIQUE(claude_instance_id, request_token)`. Each relay invocation
mints its own UUIDv4 `request_token` and INSERTs its own row, so
concurrent PermissionRequest events for the same Spawn coexist and
are decided independently — a decision targets exactly one row, and
one poller. Rows are INSERT-only: nothing replaces an open row, and a
polling loop that sees `sql.ErrNoRows` (possible via `ON DELETE
CASCADE` when the spawn row is deleted) fails closed. Closed
(decided) rows are evicted oldest-first when the table exceeds
`relay.permission_request_cap`.

## References

- Claude Code hooks: <https://docs.claude.com/en/docs/claude-code/hooks>
- Empirical investigation (gitignored, in-repo):
  `reference/claude-settings-research.md`
