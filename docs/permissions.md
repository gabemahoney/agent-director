# Permissions

How agent-director's per-Spawn `permissions` block accumulates with the
user's `~/.claude/settings.json` and project-level settings. The merge
is performed by Claude Code, not by agent-director — this document
describes the resulting behavior so operators can predict what a Spawn
will allow / deny / ask for.

For Claude Code's own permissions reference, see:
<https://docs.claude.com/en/docs/claude-code/iam#permission-rules>.

## The three arrays

A `permissions` block has up to three string arrays:

- `allow` — patterns Claude is unconditionally allowed to execute.
- `deny` — patterns Claude is unconditionally not allowed to execute.
  Denies override allows.
- `ask` — patterns that prompt the operator for confirmation. Not
  used by orchestrator-driven Spawns in practice (set
  `disable_askuserquestion = true` to silence the asks; see below).

The pattern grammar is Claude Code's; bare tool names match every
invocation of that tool (`AskUserQuestion` denies every AUQ — see the
empirical confirmation in
`reference/permissions-deny-tool-name-research.md`).

## Per-tier concatenation

Each tier's `permissions.allow` / `deny` / `ask` arrays are concatenated
in the merge — lower-precedence tier first. Higher-precedence tiers add
to the list; they do not replace.

Tiers (lowest precedence first):

1. `~/.claude/settings.json` (user)
2. `.claude/settings.json` (project, in Spawn's cwd)
3. `.claude/settings.local.json` (local)
4. agent-director's `--settings` (per-Spawn)
5. Managed policy (organization)

### Worked example

| Tier | `deny` entries |
| ---- | --- |
| user | `["Bash(rm -rf /)"]` |
| project | `["WebFetch(http://example.com/*)"]` |
| per-Spawn (via `--deny`) | `["Bash(npm publish)"]` |

Effective `deny` for the Spawn:

```
["Bash(rm -rf /)", "WebFetch(http://example.com/*)", "Bash(npm publish)"]
```

A Spawn launched with `--deny "Bash(npm publish)"` adds that single
entry. The operator's pre-existing rules continue to apply.

## `disable_askuserquestion` config

`config.toml`:

```toml
[defaults]
disable_askuserquestion = true
```

When true, every `spawn` call adds `"AskUserQuestion"` to the
synthesized `permissions.deny` array. The string `"AskUserQuestion"` is
a bare tool name — Claude Code's matcher treats it as "deny every use
of the AUQ tool" *and* removes the tool from the deferred-tool registry
(the model never sees its schema). Confirmed in
`reference/permissions-deny-tool-name-research.md`.

Recommended for orchestrator-driven setups where every Claude should
make its own decisions rather than prompting a human. The deny is
additive — caller-supplied `--deny` entries still concatenate, and the
user / project tiers still apply.

To re-enable AUQ for a specific Spawn while keeping the global config
off, either flip the config (affects every Spawn) or use a separate
config file via `AGENT_DIRECTOR_CONFIG` (not yet wired; tracked for a
future Epic). The cleanest current option is to spawn the special-case
Spawn from an alternate `HOME` whose `~/.agent-director/config.toml`
does not have the flag set.

## `--dangerously-skip-permissions`

agent-director **does not strip** this flag. A caller passing
`--dangerously-skip-permissions` through `claude_args` bypasses Claude
Code's permission engine entirely — the per-Spawn `permissions` block
becomes irrelevant for that Spawn. This is intentional per the PRD
(the supervisor exposes the same trust boundary as the operator's own
shell).

Operators who want to restrict callers from using this flag should not
let untrusted parties spawn Claude instances through agent-director.

## Relay mode

A Spawn launched with `--relay-mode=on` hands every PermissionRequest
to the orchestrator instead of Claude Code's native consent dialog.
Useful when an unattended supervisor needs to decide allow/deny based
on policy rather than a human at the keyboard.

Only the Spawn's own Claude Code is relayed. A PermissionRequest whose
hook parent is not the row's recorded pane process (a nested `claude`, a
teammate pane), or one on a row that records no pane yet, opens no
request and returns no decision, so that process's Claude Code asks as
it would with no relay; the hook writes one `ad.hook.ignored` trail
record (see [hooks.md](hooks.md#only-the-rows-own-agent-moves-the-row)).

### Flow

1. Claude Code fires the PermissionRequest hook with the tool name + tool
   input.
2. The hook handler reads `AGENT_DIRECTOR_RELAY_MODE` from its env
   (NOT the DB — see "Why env-var, not DB" below).
3. If the env var is `on`, the relay hook:
   - Mints a per-request UUIDv4 `request_token` and, in one store
     transaction, moves the Spawn to `check_permission` and records the
     open request in `permission_requests`, keyed by the composite
     `(claude_instance_id, request_token)`: both are written or neither
     is. The request carries the hook's own process identity (pid, start
     time and pid namespace) and the payload's `tool_use_id`. Concurrent
     requests for the same Spawn each get their own row and are decided
     independently. Oldest *closed* rows (those no longer awaiting an
     answer) are evicted in the same transaction when the table exceeds
     `relay.permission_request_cap` (default 1000; `0` disables
     eviction), but only once Claude Code has proven the request's dialog
     gone (`proven_gone_at` set) or its spawn is `ended` or `missing`: a
     closed request not yet proven gone on a live spawn still holds plain
     `send-keys` (see "The dialog hold" below), so it is kept, and the
     table can stay above the cap by the number of such requests. A
     spawn's newest request is kept as well while that spawn has a
     request that still awaits an answer, because `decide` relies on it
     for a request recorded before this release (see "Requests recorded
     before this release" below), so the table can stay above the cap by
     one more closed row per such spawn.
   - Polls its own row at
     `max(50ms, relay.poll_base_ms + uniform(0, relay.poll_jitter_ms))`
     intervals, until 3 s before Claude Code ends it (see "Relay timeout
     default and override" below).
   - On a decided row → confirms the verdict in the store first (it
     records `delivered_at`), then writes the decision envelope to stdout.
   - At the poll's end with no verdict → records the `timeout` deny and
     its confirmation in one statement, then writes a deny envelope.
   - On a failure → see "Fail-closed boundary" below: a deny before the
     request is recorded, no answer after.
4. The orchestrator calls `agent-director decide --claude-instance-id
   <id> --request-token <token> --decision allow|deny [--max-wait-ms N]`
   to record the verdict. The token comes from the Spawn's
   `permission_requests` in `get` or `list` output.
5. The hook's polling loop sees the verdict on its next read, confirms it
   and writes the envelope. `decide` waits at most 1 s for that
   confirmation and reports it (see "Delivery" below).
6. A relay hook that ends with no confirmed verdict (it died, or Claude
   Code ended it) leaves its request fallen back: no answer from the relay
   reached the agent, `decide` refuses it with `ErrRelayFallenBack`, and
   only an answer at the pane, or a record of one, closes it (see
   "Answering at the pane" below).
7. From the request's first write until Claude Code's own hooks prove its
   dialog gone (its tool ran, the agent's turn ended, or the agent
   ended), a plain
   `send-keys` without a pane hash to the spawn is refused with
   `ErrDialogMaybeOpen`, whatever agent-director's records say of the
   request (see "The dialog hold" below).

### Envelope wire format

SRD §6.3 / Claude Code 2.x nested shape:

```json
{
  "hookSpecificOutput": {
    "hookEventName": "PermissionRequest",
    "decision": {
      "behavior": "allow",
      "message": "trusted command"
    }
  }
}
```

- `behavior` is `allow` or `deny`.
- `message` is the orchestrator's reason. On `deny` with empty reason
  the envelope defaults to `"Denied by orchestrator"` so the TUI
  always shows something. On `allow` with empty reason the `message`
  field is omitted entirely (Claude Code drops empty messages
  silently).

### Fail-closed boundary (SRD §6.4)

When `AGENT_DIRECTOR_RELAY_MODE=on` the hook handler treats every
failure before the request is recorded as a deny — provided the event is
known to be a PermissionRequest. Failures that occur before the event name
can be read from the payload exit silently instead: Claude Code routes hook
stdout by file descriptor, so a permission-shaped deny emitted by a
process that might be handling a *different* event (e.g. PreToolUse)
would be applied to the in-flight tool and race the legitimate
PermissionRequest process (the b.45p fix).

Once the request is recorded, the hook writes only an answer it has first
confirmed in the store, and every failure ends with **no answer**: the hook
exits 0 with empty stdout, Claude Code asks in its own permission prompt,
and the request falls back (its `delivery` reads `fallen_back` once a
reader finds the hook gone). Claude Code acts on a hook's JSON whatever
its exit code, so an answer written before its confirmation could reach
the agent while `decide` and `get-permission` report that it did not. SRD
§6.4 enumerates the failure modes:

| Failure | Outcome |
|---|---|
| `AGENT_DIRECTOR_INSTANCE_ID` missing / invalid | deny envelope |
| Config load failure | deny envelope |
| Store open failure (including no usable `HOME`: unset or empty) | deny envelope |
| stdin payload read failure | silent exit 0 (event name unknowable — b.45p) |
| Classify failure (unparseable payload) | silent exit 0 (event name unknowable — b.45p) |
| The first write (the request and `check_permission`) fails, or its wait for the store's write lock reaches 2 s before Claude Code ends the hook | deny envelope; nothing recorded |
| The poll reaches its end (3 s before Claude Code ends the hook) | `timeout` deny recorded and confirmed, then a deny envelope |
| The hook's parent process is no longer the Claude Code that started it | no answer |
| The confirmation or the timeout deny fails, or cannot commit by 2 s before Claude Code ends the hook | no answer |
| `ctx.Done()` during poll | no answer |
| Row preempted (`sql.ErrNoRows` during poll) | no answer |
| Read-retry budget exhausted (5 consecutive read errors) | no answer |

#### Relay timeout default and override

The relay window (`relay.timeout_seconds`) defaults to **86400 seconds (1 day)**,
long enough for human-paced approval flows — a human's approval that arrives
after a meeting or overnight still lands inside the window. Operators who want a
tighter bound can override it in `~/.agent-director/config.toml`:

```toml
[relay]
timeout_seconds = 3600   # example: 1-hour window
```

**Range.** The value is whole seconds from 1 to 2147483 (about 24.8 days).
A missing key, or 0, gives the default. A negative value or one above
2147483 is refused when the config loads, never replaced by the default
or capped: every store-backed verb fails with `ErrConfigMalformed` naming
the key, its value and its range, `serve` does not start, and the hook
denies every relayed PermissionRequest (the `Config load failure` row
above) until the file is fixed. The maximum is the largest per-hook
`timeout` Claude Code honours: Claude Code arms a hook's timeout as a
JavaScript timer of `timeout` × 1000 ms, and the runtime replaces a delay
above 2^31−1 ms with 1 ms, so a larger value would have Claude Code cancel
the relay hook about 1 ms after it starts.

**How the window is enforced.** The window is real because agent-director
emits it into the per-Spawn synthesized settings. Each PermissionRequest and
PreToolUse hook entry carries an explicit per-hook `timeout` field — placed on
the inner command object, sibling to `type`/`command` — set to
`relay.timeout_seconds`. Without that field Claude Code kills any hook at its
own 600-second default and discards its output, so a late decision would be
silently voided. The PermissionRequest entry also passes the same value to
the hook as its `--timeout` argument, so the relay hook knows the instant
Claude Code will end it: its own start plus the window.

**The hook keeps the last 3 s for itself.** Counted back from that instant:

- 2 s before it, the hook's last store write may commit. A write that
  cannot commit by then is not made: the first write gives way to a deny,
  a later one to no answer (see "Fail-closed boundary" above). The 2 s are
  left for writing the answer and exiting.
- 3 s before it, the poll ends and the hook makes its `timeout` deny,
  whose own wait for the store's write lock may take the next second.
- 2 s after it is the request's settle instant (`settled_at`), its
  `confirm_by`: by then the hook has confirmed an answer or is gone.

So a request has the window less 3 s to be decided. A window of 3 s or
less loads but leaves no time: at 3 s the hook denies the request at once,
and at 2 s or less it gives no answer, so Claude Code asks in its own
permission prompt and the request falls back.

**One value for the hook and Claude Code.** `relay.timeout_seconds` is a
single value read through one accessor, and `spawn` writes it both as
Claude Code's per-hook `timeout` and as the hook's `--timeout`, so for a
given agent the hook's deadlines and Claude Code's kill can never
disagree. That holds because the config load bounds the value: inside the
range, Claude Code applies the per-hook `timeout` as written. An agent
keeps the value in force when it was launched (`spawn` or `resume`); a
changed value applies to agents launched after it. The hook counts from
its own start, the instant Claude Code arms its kill, so at the window's
end the hook normally records and confirms its timeout deny, writes it and
exits on its own, rather than being killed mid-flight by Claude Code.

The fail-closed boundary is scoped to PermissionRequest events. A
non-PermissionRequest event with `RELAY_MODE=on` (e.g. SessionStart)
still follows the regular state-tracking fail-open path — Claude
Code drops envelopes on non-permission events anyway, so emitting
one there is harmless noise.

**Structural caveat.** Fail-closed requires the `agent-director`
binary to actually run. If Claude Code can't invoke it at all —
binary missing, PATH not set, settings JSON unparseable — no hook
runs and Claude Code decides the request through its native
permission dialog alone. From the policy view that is a hole the
operator must close at install time.

**The native dialog is not a hook-death signal.** When the relay
hook *is* running, Claude Code shows its native permission dialog
concurrently, as racing UI displayed alongside the live hook — not
as a fallback and not as evidence the hook has stopped. A decision
envelope arriving while the hook runs dismisses that dialog. Whether a
request can still be answered through the relay is judged from the relay
hook's process and the request's record (see "Delivery" below); the
dialog's presence or absence in the TUI carries no information about hook
liveness, and agent-director never reads it.

### Why env-var, not DB

`AGENT_DIRECTOR_RELAY_MODE` is set on the Spawn's tmux session at
launch (SRD §6.5). The hook reads it from the OS process env, NOT
from the spawns row's `relay_mode` column. This separation preserves
the fail-closed safety guarantee across multiple failure modes:

- DB unreachable → env still says `on` → fail-closed deny.
- Schema mismatch → env still says `on` → fail-closed deny.
- Config malformed → env still says `on` → fail-closed deny.

Storing the mode in the DB and reading it from there would create a
race: any failure path that hits the DB before the relay
determination would not know whether to fail closed or fail open.
The env var lifts that decision above every DB-dependent failure
mode.

### Race-freeness of `decide`

The decide verb writes the decision via a single-statement UPDATE in its
own write transaction, guarded by the first-call-wins `decision IS NULL`
predicate, the request being unconfirmed, without a pane answer and not
closed with its spawn, a `created_at > ?` deliverability predicate that
applies only to a request recorded before this release (one with no
`settled_at`), and a check that the request's spawn is not `ended` or
`missing` (the checks and the write are one atomic statement):

```sql
UPDATE permission_requests
   SET decision = ?, decision_reason = ?, decided_at = CURRENT_TIMESTAMP
 WHERE claude_instance_id = ? AND request_token = ? AND decision IS NULL
   AND delivered_at IS NULL AND pane_answer = 'none' AND closed_at IS NULL
   AND (settled_at IS NOT NULL OR created_at > ?)
   AND NOT EXISTS (SELECT 1 FROM spawns
                    WHERE claude_instance_id = ? AND state IN (?, ?))  -- 'ended', 'missing'
```

First call wins; concurrent second calls see RowsAffected==0. The
verb then does a follow-up SELECT (and, for an open row, a read of its
spawn) to disambiguate the outcome. Before the write, `decide` has already
refused a request that has fallen back (`ErrRelayFallenBack`, see
"Delivery" below). The follow-up disambiguation resolves to exactly one of
these outcomes:

- No row at all → `ErrNoOpenPermissionRequest`.
- Row exists with non-NULL decision → `ErrAlreadyDecided` (including a
  request the relay hook denied at its timeout, one `find-missing` denied
  when it marked the spawn `missing`, and one closed at the pane with a
  verdict).
- Row was closed with its spawn, by `find-missing`'s mark or by the
  spawn's end, before its relay hook confirmed a verdict, with a verdict
  `find-missing` did not write (one recorded before the close, or the
  `ended` deny) → `ErrNoOpenPermissionRequest`, whether or not the spawn
  was resumed since (see "A request of a finished spawn is closed" below).
- Row is still open but its spawn is `ended` or `missing` →
  `ErrNoOpenPermissionRequest` (see "A request of a finished spawn is
  closed" below).
- A request recorded before this release, still open past its relay
  window → `ErrRelayFallenBack`, `ErrAlreadyDecided` or
  `ErrNoOpenPermissionRequest` (see "Requests recorded before this
  release" below).

When the caller passes `--max-wait-ms`, the reads before the write and the
write itself wait for the store at most what is left of it; a wait cut by
it is `ErrStoreBusy`, with nothing recorded (see "Delivery" below).

Two orchestrators racing to decide the same prompt see distinct
error messages and can act on them programmatically.

### A request of a finished spawn is closed

A request whose spawn is `ended` or `missing` is closed, decided or not:
its agent is gone (or judged gone by `find-missing`), so no relay hook of
it can deliver a verdict. `decide` records nothing for it and answers
with an existing error name:

- a decided request → `ErrAlreadyDecided`, except one closed (see below)
  before its relay hook confirmed a verdict that `find-missing` did not
  write → `ErrNoOpenPermissionRequest`;
- an open request (one an earlier release left open) →
  `ErrNoOpenPermissionRequest`, whose message says the request is closed
  because the spawn is `ended` or `missing`, nothing was recorded, and not
  to answer it at the pane;
- no such request → `ErrNoOpenPermissionRequest`, as for a live spawn.

`decide` checks the spawn's state when it reads the spawn, and its write
checks it again in the same statement, so a spawn that finishes between
the two still gets nothing recorded. `send-keys` refuses an `ended` or
`missing` spawn before its relay guard reads any request
(`ErrSpawnNotInteractive`).

Three writes close a spawn's open requests, each in the same store
transaction as its own change, so both are written or neither is:

- `find-missing`'s mark of the spawn `missing`;
- the agent's terminal SessionEnd, which moves the spawn to `ended`;
- `resume`'s move of the finished spawn to `pending`, as a backstop for a
  request left open by a release before this close.

The same close proves every request of the spawn gone, closed now or
before (`proven_gone_how` `agent_gone`; see "The dialog hold" below): its
agent is gone, so no dialog of it is left on the pane.

Every request that still awaits an answer is closed (`closed_at` is set),
decided or not:

- an undecided request gets `decision` `deny` with `decision_reason`
  `find_missing` (the mark) or `ended` (the other two), so a relay hook
  still polling for it reads that deny (and returns it, once confirmed, if
  its Claude Code is still the one that started it). `decide` on the
  mark's deny returns `ErrAlreadyDecided`, and on the `ended` deny
  `ErrNoOpenPermissionRequest`;
- a decided request whose relay hook had not confirmed the verdict keeps
  it (a hook whose Claude Code still runs may still confirm and return
  it); `decide` on it returns `ErrNoOpenPermissionRequest`, whose message
  says the request is closed (its spawn ended, or `find-missing` marked it
  missing, before its relay hook delivered a verdict), nothing was
  recorded, and not to answer it at the pane (`ErrAlreadyDecided` once its
  hook has confirmed it).

Each deny is written to the trail as one `ad.row_mutation.committed`
(`writer_process` `find_missing`, `hook` or `resume`).

A closed request stays closed after a `resume`: it no longer awaits an
answer, so `get` and `list` do not show it, it no longer holds the spawn in
`check_permission` or off `working`, it is proven gone, so it never refuses
`send-keys`, and
`decide` refuses it as above, never with `ErrRelayFallenBack`.
`get-permission` still reads it by its token, and its `delivery` follows
the rules in "Delivery" below like any request's: `delivered` once its hook
confirmed, `not_confirmed` while that hook may still run, `fallen_back`
once it is gone (or cannot be checked and `confirm_by` has passed). A
spawn marked `missing` or ended by its agent never keeps an open request.

### Delivery

Every relayed request carries its delivery facts. They are worked out each
time the request is read, by `decide`, `get`, `list` and `get-permission`;
nothing but `hook_gone_at` is written for them. `proven_gone_at` and
`proven_gone_how` are written by the agent's own hooks, by the close of
its spawn's requests and, for a spawn already finished, by the upgrade to
this release, never by a reader (see "The dialog hold" below):

| Field | Meaning |
|---|---|
| `delivery` | `delivered`: the relay hook confirmed a verdict in the store before handing it to Claude Code. `not_confirmed`: no confirmation yet, and the hook may still run. `fallen_back`: no confirmation, and the hook is gone, or cannot be checked and `confirm_by` has passed: no answer from the relay reached the agent. While `pane_answer` is `none` or `intent` the request is open, and only an answer at the pane, or `record-pane-answer`, closes it; with `sent`, `outside` or `tool_ran` it is closed and stays `fallen_back`. |
| `confirm_by` | The request's settle instant: the moment Claude Code ends its relay hook plus 2 s. `not_confirmed` never lasts past it. |
| `hook_alive` | `true` or `false` by the check below; `null` when it cannot tell. |
| `hook_gone_at` | When a reader first found the request fallen back; `null` until then. |
| `attempted_decision`, `attempted_at` | The verdict a `decide` refused with `ErrRelayFallenBack` tried to record, and when; shown, never acted on. |
| `tool_use_id` | The `tool_use_id` of Claude Code's hook input; `null` when it gave none. |
| `pane_answer` | `none`: no pane answer recorded through agent-director (something outside it, such as a person at tmux, may still have answered). `intent`: a pane answer through `send-keys` was started, and whether its key was typed is unknown. `sent`: its key was sent. `outside`: a caller recorded it answered outside agent-director (`record-pane-answer`). `tool_ran`: Claude Code reported that its tool ran. `sent`, `outside` and `tool_ran` close the request. See "Answering at the pane" below. |
| `pane_as` | The verdict a pane answer claims: `allow`, `deny`, or `unknown` (a record whose caller did not see the answer); `null` when none was recorded. It is the caller's claim, stored and never checked. |
| `proven_gone_at` | When Claude Code proved the request's dialog gone; `null` until then. agent-director's own records (`delivered`, a pane answer, `record-pane-answer`) are no proof. While it is `null` on a live spawn, a plain `send-keys` without a pane hash is refused with `ErrDialogMaybeOpen`. |
| `proven_gone_how` | `tool_ran`: a `PostToolUse` or `PostToolUseFailure` carried its `tool_use_id`. `turn_end`: the turn of the agent that asked ended after it was written; the main agent's `Stop` or idle-prompt Notification proves a request with no `agent_id`. `agent_gone`: its spawn was marked `missing`, ended or resumed, or was already `ended` or `missing` when the store was upgraded to this release. `null` until proven. |
| `unproven_since` | For a request not proven gone that no longer awaits an answer in agent-director's records: when its record stopped awaiting one (its relay hook's ack, its pane answer, its `record-pane-answer`, or the close of its spawn's requests; for a request recorded before this release, its recorded verdict). `null` while it still awaits an answer, and once it is proven gone. |

**`decision` is not the outcome.** `decision` is the verdict recorded,
set before delivery is known: a request can read `decision` `allow` with
`delivery` `fallen_back`. Read `delivery` for the outcome, and
`pane_answer` for a request closed at the pane, whose `decision` is the
caller's claim (`null` for a record claiming `unknown`).

**`decision_reason`** names who recorded the verdict: `operator` (a
`decide` deny; a `decide` allow records none), `timeout` (the relay hook's
own deny), `find_missing` and `ended` (the deny a close wrote, see "A
request of a finished spawn is closed" above), `pane` (a pane answer
through `send-keys`), `pane_outside` (`record-pane-answer`) and `tool_ran`
(the PostToolUse close). `pane` comes with an `allow` or a `deny`,
`pane_outside` with either or with none (`unknown`), and `tool_ran` with an
`allow`. A caller that meets a value it does not know reads the recorded
verdict from `decision` (`null` for a `pane_outside` `unknown` claim) and
the outcome from `delivery` and `pane_answer`; it never reads a reason as
a verdict.

**How a reader judges the relay hook.** The request records the hook's
pid, its start time (on Linux, field 22 of `/proc/<pid>/stat`) and its pid
namespace, as the hook read them at its start. In the reader's own pid
namespace:

- no process with that pid, a process with another start time, or a
  zombie → gone (`hook_alive` `false`);
- the pid with that start time in any other state, stopped or in
  uninterruptible sleep included → alive (`hook_alive` `true`);
- another or unreadable pid namespace, an unreadable `/proc`, or no
  identity on record → cannot tell (`hook_alive` `null`): the request
  falls back only once `confirm_by` has passed.

A reader checks the hook before it reads the request's record, so a hook
that confirmed its answer and exited between the two is never reported
fallen back.

**What `decide` does.** On a request that has fallen back, `decide`
returns `ErrRelayFallenBack` at once and records no verdict; it stores the
caller's verdict as `attempted_decision` and `attempted_at`, and
`hook_gone_at` when not yet set, and the error's `err_details` give the
request's facts and the spawn's other open requests (see "Send-keys
interaction" below). On a request already answered at the pane
(`pane_answer` `sent`, `outside` or `tool_ran`) it returns
`ErrAlreadyDecided` naming that `pane_answer`, as `send-keys` and
`record-pane-answer` do, and records nothing, not even
`attempted_decision`. Otherwise one guarded write records the
verdict (first call wins), then `decide` waits at most 1 s, by its own
clock and its reads of the request included, for the hook's confirmation,
and returns the request's delivery facts with `delivery` `delivered` or
`not_confirmed`. It never returns `fallen_back`. On `not_confirmed` the
caller polls `get-permission` until `delivery` reads `delivered` or
`fallen_back`, which it does by `confirm_by`. A live hook reads a verdict
within one poll sleep (at most 200 ms at the default poll settings), so the
normal case returns `delivered`.

**`--max-wait-ms`** (`max_wait_ms` over MCP and in the TypeScript client)
bounds the whole call, from its start, and has no default:

- Every read before the verdict, and the verdict write, waits for the
  store at most what is left of the bound (with nothing left, the write
  goes ahead only if the lock is free at once). That covers a wait for the
  store's write lock, held by another process, and a wait for the store's
  one connection, held by another call in the same process (a Go program
  making concurrent calls on one Client). A wait cut by the bound is
  `ErrStoreBusy`: nothing was recorded, so a retry is safe.
- Once the verdict is recorded, `decide` never returns `ErrStoreBusy`: a
  bound reached while it waits for the confirmation returns
  `not_confirmed`.
- A caller with a deadline passes at most its deadline minus 1 s, which
  leaves time for the reply.
- Without it, the verdict write waits up to `[store] busy_timeout_ms`, and
  a lock not taken in that time is `ErrInternal`, as before this option.
  A negative value is `ErrInvalidFlags`.

**Reading verbs never wait.** `get`, `list` and `get-permission` write
`hook_gone_at` the first time they find a request fallen back only if the
store's write lock is free at that moment; otherwise they skip the write
and report `hook_gone_at` as stored. `decide` always writes it (within its
bound).

**Which requests `get` and `list` show.** A row in `check_permission`
lists, in `permission_requests`, the requests that still await an answer:
not confirmed by their relay hook and with no pane answer recorded,
decided or not (a recorded verdict not yet confirmed has not reached the
agent), and not closed with the spawn. A row can read `waiting` while
one of its requests still awaits an answer, so a caller follows each
request it tracks with `get-permission`, not only rows in
`check_permission`. `get` (not `list`) also carries `unproven_requests`:
every request of the row not proven gone, in any state but `ended` and
`missing`, with the same fields, including those agent-director's records
read closed (see "The dialog hold" below).

**The row after the last answer.** While any of its requests still awaits
an answer, the row stays in `check_permission`. After that it moves on
with the agent's next hook; when that hook is lost, the idle-prompt
Notification or a scheduled `find-missing` moves it to `waiting` or
`working` (see [hooks.md](hooks.md#a-relay-row-left-in-check_permission)).
The relay hook's timeout deny itself leaves the row's state as it is.

### Requests recorded before this release

A request recorded by a relay hook of an earlier release has no hook
identity, no confirmation and no settle instant. It is judged by time, as
before the upgrade:

- `confirm_by` is its `requested_at` plus the relay window plus 2 s, and
  `hook_alive` is `null`. Until `confirm_by`, `delivery` is
  `not_confirmed`; after it, `delivered` when a verdict other than a
  close's deny (`find_missing` or `ended`) is recorded, else `fallen_back`.
  Such a request awaits an answer while it is undecided and not closed.
- Fallen back and still undecided, such a request is open and fallen back
  like any other: a plain `send-keys` to its spawn is refused with
  `ErrRelayFallenBack` until it is closed, by a pane answer, by
  `record-pane-answer`, or with its spawn (the spawn ends, or `find-missing`
  marks it `missing`). This holds in every live state of the spawn, so a
  request left open before the upgrade (its dialog answered at the pane
  after its relay hook was killed, for example) refuses plain `send-keys`
  once you upgrade: close it with `record-pane-answer --as unknown` and the
  `pane_sha256` of a `read-pane` you looked at. `decide` does not close
  it: once the spawn has left `check_permission` or recorded a later
  request, `decide` on it returns `ErrNoOpenPermissionRequest` (see
  below), while plain `send-keys` is still refused with
  `ErrRelayFallenBack` on its account. Its PostToolUse does not close it
  (no `tool_use_id` is recorded for it).
- Every such request of a live spawn, decided or not, has no proof that
  its dialog is gone (`proven_gone_at` `null`), so it holds a plain
  `send-keys` without a pane hash to its spawn (`ErrDialogMaybeOpen`; see
  "The dialog hold" below) until the main agent's next `Stop` or
  idle-prompt Notification, or the spawn's end. The upgrade itself proves
  gone (`agent_gone`) every request of a spawn already `ended` or
  `missing`. `record-pane-answer` does not lift that hold. An
  agent already idle at its prompt when you upgrade, its idle-prompt
  Notification already sent, sends neither until a new turn starts, so its
  first plain `send-keys` needs a person or an LLM to read the pane and
  send with that read's `pane_sha256`.
- `decide` records a verdict on it only before its window less 1 s, and
  returns at once, with no wait for a confirmation such a hook never
  writes. Refused from then, it first waits until `confirm_by` (at most
  3 s; under `--max-wait-ms` that ends sooner it returns `ErrStoreBusy`
  instead) and reads the request again (a request already answered at the
  pane is `ErrAlreadyDecided` at once, with no wait): decided meanwhile
  (normally the hook's own timeout deny, `decision_reason` `timeout`) →
  `ErrAlreadyDecided`; still open → `ErrRelayFallenBack` only while the
  spawn is still in `check_permission` with no other open request and none
  recorded after this one, otherwise `ErrNoOpenPermissionRequest` (do not
  answer it at the pane). Eviction at the cap keeps the spawn's newest
  request while this one is open, so a later request stays visible to that
  check.

### Send-keys interaction

On a spawn with `relay_mode=on`, in every live state (a row can read
`waiting` or `working` while one of its requests is still open), `send-keys`
first reads every permission request of the spawn, judging each relay hook
as the readers do (see "How a reader judges the relay hook" above). It
refuses in this order, sending nothing:

| Call | Refused while | Error |
|---|---|---|
| any | a relay hook of the spawn may still answer its request: its process runs, or it cannot be checked and the request is open before its `confirm_by` | `ErrSendKeysWhileRelayed` |
| plain (no `request_token`) | a request of the spawn is fallen back with `pane_answer` `none` or `intent` | `ErrRelayFallenBack`, with `err_details`, naming the oldest such request |
| plain, without `expect_pane_sha256` | a request of the spawn is not proven gone (`proven_gone_at` `null`), however agent-director's records say it closed | `ErrDialogMaybeOpen`, with `err_details`, naming the oldest such request (see "The dialog hold" below) |
| pane answer (`request_token` T) | the spawn has no request T | `ErrNoOpenPermissionRequest` |
| pane answer | T is not fallen back: confirmed by its relay hook, closed with its spawn, or already closed at the pane | `ErrAlreadyDecided` or `ErrNoOpenPermissionRequest` |
| pane answer | another pane answer to T is still being sent | `ErrPaneAnswerInProgress`, with `err_details` |
| either, with `expect_pane_sha256` | the agent's pane no longer has that hash | `ErrPaneChanged`, with `err_details` |

All but the last are decided from the store and process checks, before
any tmux call; the last needs a capture of the pane. A request closed with
its spawn is proven gone with it and refuses nothing. A request closed at
the pane (`pane_answer` `sent`, `outside` or `tool_ran`) refuses nothing
but, until it is proven gone, a plain call without a pane hash
(`ErrDialogMaybeOpen`). A request its relay hook confirmed is held by the
first rule only while that hook's process is seen running (it is writing
its verdict to Claude Code), then by the dialog hold until it is proven
gone. There is no time window of the guard's own: the first rule ends when
the relay hook does, and a request recorded before this release, whose
hook recorded no identity, holds by it until its `confirm_by`, its relay
window plus 2 s (see "Residual race" below).

So only a call that names the request it answers can type on a spawn with
an open fallen-back request, a plain `send-keys` (an automatic command and
its Enter, say) cannot land on that request's prompt, and a plain
`send-keys` without a pane hash cannot land on a dialog agent-director
believes answered until Claude Code shows it gone.

`ErrSendKeysWhileRelayed`'s message (advice; the error name is the
contract) names one request whose relay hook may still answer, one still
awaiting an answer before a confirmed one, then the oldest, and states no
release time:

- **Undecided**: `spawn <id> is awaiting a relayed permission decision on
  request <request_token>; answer it with decide`.
- **Verdict recorded**: `spawn <id>: the relayed permission verdict on
  request <request_token> is recorded and its relay hook may still be
  delivering it; retry send-keys later`. There is nothing left to answer
  (`decide` on it returns `ErrAlreadyDecided`).

A plain retry after the relay hook has ended can get `ErrDialogMaybeOpen`
instead, until Claude Code proves the request gone: after an allow, when
its tool has run; after a deny, when the main agent's turn ends (its
`Stop` or idle-prompt Notification).

`ErrRelayFallenBack` (from a plain `send-keys`, and from `decide`) carries
`err_details`, an object with:

- the request's fields as `get-permission` gives them: `request_id`,
  `request_token`, `tool_name`, `tool_input`, `requested_at`, `decision`,
  `decision_reason` and its delivery facts (`delivery` `fallen_back`,
  `confirm_by`, `hook_alive`, `hook_gone_at`, `attempted_decision`,
  `attempted_at`, `tool_use_id`, `pane_answer` `none` or `intent`,
  `pane_as`, `proven_gone_at`, `proven_gone_how`, `unproven_since`);
- `state`: the spawn's state;
- `open_requests`: every other request of the spawn that still awaits an
  answer, oldest first, each with `request_token`, `tool_name`,
  `requested_at`, `delivery`, `hook_alive` and `pane_answer` (`[]` when
  there is none; `null` when `decide` could not read them).

Its description says the request's relay hook is gone and acked no
verdict and that no pane answer is recorded on it through agent-director;
it never says that a prompt is on screen. A refusal that finds a request
fallen back records its `hook_gone_at` when it has none, waiting for the
store's write lock as any write does (fail-open).

**Residual race.** A request whose relay hook cannot be checked is held
until its `confirm_by` and is then fallen back. For a request recorded
before this release, that is 2 s after its relay window ends, counted from
its `created_at`, and rests on a live relay hook having answered, or been
killed by Claude Code, by then. Keys can still reach Claude's prompt before
a live hook's verdict or deny does only if both:

- the hook's delivery of its verdict or timeout deny (reading the verdict,
  or noticing its deadline, and its store writes; then writing its
  envelope and exiting) ends more than 2 s past its window, for example
  because the process stalls (a relay hook from before this release could
  wait up to `[store] busy_timeout_ms`, 10 s by default, for each store
  write); and
- Claude Code kills the hook more than 1 s after its per-hook timeout.

A relay hook of this release commits its last store write at least 2 s
before Claude Code ends it, and its `confirm_by` is that end plus 2 s.
Nothing stored shows a hook stuck delivering, so this race is not closed
for a call that carries a pane hash (a pane answer, or a plain call that
passes the dialog hold). A plain call without one stays refused after
`confirm_by` too, with `ErrRelayFallenBack` or `ErrDialogMaybeOpen`, until
the request is closed and Claude Code proves it gone. All of these
instants assume the hook, the store and the caller share one wall clock.

### The dialog hold

agent-director's own records of a request say what agent-director did,
not what Claude Code shows. A request can read `delivered` while its
dialog is still on the pane (its relay hook acked the verdict, then died
before writing it), and so can one closed at the pane (the key landed on
an identical dialog of another request) or recorded answered with
`record-pane-answer` (its dialog was drawn late). An Enter typed then
answers that dialog. So from a request's first write, agent-director
treats its dialog as possibly on the pane until Claude Code's own hooks
prove it gone:

| `proven_gone_how` | Proof |
|---|---|
| `tool_ran` | The agent's `PostToolUse` or `PostToolUseFailure` carrying the request's `tool_use_id`: the tool ran, so its dialog was answered. Any request with that id, a subagent's included. |
| `turn_end` | The turn of the agent that asked ended after the request was written. The main agent's `Stop`, or its idle-prompt Notification (`notification_type` `idle_prompt`, no `agent_id`), proves it for every request with no `agent_id` recorded before it, one recorded before this release included. |
| `agent_gone` | `find-missing`'s mark of the spawn `missing`, the agent's terminal `SessionEnd`, or `resume`'s move of the finished spawn to `pending`: every request of the spawn. The upgrade to this release proves every request of a spawn already `ended` or `missing` the same way. |

The ack (`delivered`), a pane answer recorded `sent` and
`record-pane-answer` prove nothing. A hook proves something only when it
comes from the row's own agent (see
[hooks.md](hooks.md#only-the-rows-own-agent-moves-the-row)) on a spawn with
`relay_mode=on`. The proof is stored on the request as `proven_gone_at`
and `proven_gone_how`.

**The hold.** While any request of the spawn is not proven gone, a plain
`send-keys` without `expect_pane_sha256` is refused with
`ErrDialogMaybeOpen` and nothing is sent, in every live state of the
spawn. `ErrSendKeysWhileRelayed` and `ErrRelayFallenBack` are checked
first. In practice the hold lasts, after an allow, until the request's
tool has run, and after a deny, until the main agent's turn ends. A spawn
with no unproven permission request is never held (one that never asked
for a permission included), and a spawn with the relay off never is.
`pause` is not held (see "Known limitations" below).

`ErrDialogMaybeOpen` carries `err_details`, an object with:

- the oldest request not proven gone, with its fields as `get-permission`
  gives them, how agent-director's records say it closed (`delivery`,
  `pane_answer`) and since when (`unproven_since`, `null` while it still
  awaits an answer);
- `state`: the spawn's state;
- `unproven_requests`: every other request of the spawn not proven gone,
  oldest first, with the same fields (`[]` when there is none).

Its description states those facts and that nothing was sent; it never
says that a dialog is on screen.

**The look-and-send path.** A plain `send-keys` whose
`expect_pane_sha256` matches the pane as captured now is not held: the
hash says that a person or an LLM judged this exact screen. A mismatch is
`ErrPaneChanged`, and every other refusal still applies (a relay hook
that may still answer, a fallen-back request, the pane-answer rules).
**Never pass the hash from an automatic flow:** a program that reads the
pane and passes its hash without judging it lets through exactly the
Enter the hold exists to stop.

**The unproven report.** `get` lists, in `unproven_requests`, every
request of the row not proven gone (in any state but `ended` and
`missing`), each with `unproven_since`; `get-permission`, `decide` and the
`permission_requests` of `get` and `list` carry `proven_gone_at`,
`proven_gone_how` and `unproven_since` on each request. A request that
reads `delivered`, or closed at the pane, but stays unproven for a while
is a reason to read the pane: its dialog may still be there.

**What a caller does.** On `ErrDialogMaybeOpen`, keep the keys and retry
later. If it is still held after some seconds, have a person or an LLM
read the pane: if a dialog waits, answer it with a plain `send-keys --key`
and that read's `pane_sha256`; if not, send the held keys with that
`pane_sha256`.

### Answering at the pane

agent-director never reads, matches or acts on what the pane says, and
never picks a key. The caller looks at the pane (`read-pane`) and decides;
agent-director checks only that the pane's bytes are still the ones the
caller read (`pane_sha256`, a SHA-256 of exactly the bytes `read-pane`
returned), sends exactly the key the caller named, and stores the
caller's claim of what that key answered (`as`) without checking it.

#### A pane answer through `send-keys`

```sh
agent-director send-keys --claude-instance-id <id> --request-token <T> \
    --as allow|deny --key <K> --expect-pane-sha256 <H> [--n-lines N]
```

- `--as`, `--key` and `--expect-pane-sha256` are required, and `--text`
  must be empty; anything else is `ErrInvalidFlags`, with nothing read,
  sent or written. `--key` is one named key (`Escape`, `Enter`, `Up`,
  `Down`, `Tab`, sent by name) or one character, typed literally. H is the
  `pane_sha256` of the `read-pane` the caller judged, and `--n-lines` that
  read's (default 25).
- Under the store's write lock, in one transaction, it reads the spawn and
  its requests again and applies the checks above again, captures the
  agent's pane (the last N lines, ANSI stripped, as `read-pane` gives it by
  default) and compares its SHA-256 with H, and records its intent:
  `pane_answer` `intent`, `pane_as`, its own process (pid, start time and
  pid namespace) as the sender, and `pane_intent_at`, the time read after
  the checks and the capture, inside the transaction. That commits before
  any key is sent.
- It sends exactly that one key, never followed by Enter.
- It records `pane_answer` `sent`, `decision` the `--as` value and
  `decision_reason` `pane`, written to the trail as one
  `ad.row_mutation.committed` (`writer_process` `send_keys`), unless the
  request was closed meanwhile (its tool's PostToolUse close, or a close
  with its spawn): that close wins, and the call still succeeds, since the
  key was sent.
- When the key send fails, or that last write does, it releases its
  intent, trying up to 3 times within 10 s: the request stays
  `pane_answer` `intent` (the key may have been typed) and a retry with a
  fresh hash is accepted at once. A last write that failed after the key
  was sent is `ErrInternal` with `err_details` `key_sent` `true` and the
  `request_token`: the key was sent, so do not send it again without
  reading the pane; close the request with `record-pane-answer` if the
  pane shows it answered.
- The store's write lock not taken within its busy timeout is
  `ErrStoreBusy`, with nothing sent or recorded.

**A second pane answer at once** (a double click, or two callers). While a
pane answer's intent is recorded and its sender process runs (judged as a
relay hook is), another pane answer to the same request is refused with
`ErrPaneAnswerInProgress`, so its key cannot land on the next prompt or in
the chat. Its `err_details` give `request_token`, `pane_as`,
`pane_intent_at`, `sender_alive` (`true`, or `null` when the sender cannot
be checked) and `not_before`. An intent whose sender runs never lapses by
its age: its key may still be on its way. When the sender cannot be
checked (another pid namespace, an unreadable `/proc`, or no identity
recorded), the intent counts as in progress until `not_before`: the tmux
action timeout plus the pipe-close wait plus 2 s after it was committed.
Once the sender has ended, or released its intent, a
retry needs a fresh hash: if the prompt is still there, answering again is
safe; if it is gone, the caller records the answer with
`record-pane-answer` instead.

**`ErrPaneChanged`** (from `send-keys` or `record-pane-answer`): the pane's
bytes changed since the caller read it (someone answered, another prompt
came up, the prompt closed); nothing was sent or recorded. It never
carries the new hash, since a caller that retried with it would skip
looking: read the pane again. Its `err_details` give `n_lines` and the
`request_token` named, if any. Volatile bytes in the captured lines (a
spinner, a clock) cause such refusals; pick `--n-lines` to leave them out.
The hash of an `ansi` read never matches.

#### Plain `send-keys`

A plain call types `--text` and then presses Enter, as before. `--no-enter`
types the text with no Enter (with neither text nor key it is
`ErrInvalidFlags`), and `--key K` sends that one key alone, with no Enter
and no text. `--expect-pane-sha256` is optional: given, the pane is
compared as for a pane answer (`ErrPaneChanged`) before anything is typed,
and a match passes the dialog hold (see "The dialog hold" above).
`--as` without `--request-token` is `ErrInvalidFlags`.

**Never pass `expect_pane_sha256` from an automatic flow.** The hash says
that a person or an LLM judged that exact screen; passed blindly, it lets
through the keys the check exists to stop.

#### A request answered outside agent-director

A request answered at tmux by a person, or by anything else outside
agent-director, stays fallen back, and a plain `send-keys` stays refused,
until it is closed. Once closed, a plain `send-keys` without a pane hash
is still held (`ErrDialogMaybeOpen`) until Claude Code proves the request
gone. Two ways close it:

1. **`record-pane-answer`** (it types nothing):

   ```sh
   agent-director record-pane-answer --request-token <T> \
       --as allow|deny|unknown --expect-pane-sha256 <H> [--n-lines N]
   ```

   Pass `--as unknown` unless the answer was seen; the claim is stored,
   never checked. It is accepted when T is fallen back, no pane answer
   through `send-keys` on it is still being sent, its relay hook has been
   gone at least 2 s (from its `hook_gone_at`, written now when it has
   none, or from its `confirm_by` when that is earlier and the hook is
   judged by time: it cannot be checked, or the request was recorded
   before this release), and, under the store's write lock, the pane still
   has hash H. It
   records `pane_answer` `outside`, `pane_as` and `decision` the claim
   (`decision` `null` for `unknown`) and `decision_reason` `pane_outside`,
   written to the trail as one `ad.row_mutation.committed`
   (`writer_process` `record_pane_answer`), and returns them.
   - **Why the 2 s.** Claude Code draws its prompt only after the relay
     hook ends, so a pane read in between may not show it yet, and the
     hash of that read would still match. Too soon, or while the relay
     hook may still answer the request, is `ErrClaimTooSoon`, whose
     `err_details` give `hook_alive`, `hook_gone_at` and `not_before`:
     `null` only while the relay hook is seen running; otherwise
     `hook_gone_at` plus 2 s, or `confirm_by` plus 2 s for a hook judged
     by time when that is earlier (also while such a hook may still
     answer). Retry at `not_before`: a retry then is not refused as too
     soon, and one before it gets the same refusal and `not_before`.
   - Its other refusals: `ErrPermissionRequestNotFound` (no request has the
     token), `ErrNoOpenPermissionRequest` (closed with its spawn),
     `ErrAlreadyDecided` (confirmed, or already closed at the pane),
     `ErrPaneAnswerInProgress`, `ErrPaneChanged`, `ErrStoreBusy`, and the
     tmux errors of `read-pane`.
2. **The PostToolUse close.** When the agent's `PostToolUse` or
   `PostToolUseFailure` hook carries the `tool_use_id` of a fallen-back
   request recorded from this release on, the tool ran, so its prompt was
   answered allow: the hook records `pane_answer` `tool_ran`, `decision`
   `allow` and `decision_reason` `tool_ran`, only while the request still
   awaits an answer and its relay hook is judged gone (see
   [hooks.md](hooks.md#a-request-whose-tool-ran)). It closes only allows:
   Claude Code runs no hook for a deny at its prompt. That hook can fail
   too, so this close is a help, not a guarantee. The same hook proves the
   request gone (`tool_ran`), whether or not it closes it.

#### Recovering a wedged relayed spawn

At the window's end the relay hook normally denies the request itself
(see "Relay timeout default and override" above): `decide` then returns
`ErrAlreadyDecided` (`decision_reason` `timeout`) and there is nothing to
recover. A relayed spawn is wedged when its relay hook died or was killed
without answering: `decide` and a plain `send-keys` return
`ErrRelayFallenBack`. Recover it through agent-director, **never through
raw tmux**:

1. Take the request's facts from the error's `err_details` (or
   `get-permission`). If `decide` returns `ErrAlreadyDecided` instead, the
   relay hook or another caller answered the request and there is nothing
   to recover; if it returns `ErrNoOpenPermissionRequest`, the request is
   closed or its prompt cannot be shown to wait: do not answer it at the
   pane. If a plain `send-keys` is still refused with `ErrRelayFallenBack`
   on that request (a stale record from before this release), close it
   with step 4.
2. `read-pane`, and have a person or an LLM look at it.
3. If the request's prompt waits there, answer it with `send-keys` and its
   `request_token`, the key and verdict chosen, and that read's
   `pane_sha256`.
4. If it was answered already, close it with `record-pane-answer` (`--as
   unknown` unless the answer was seen).
5. Both are audited: `ad.send_keys.called` (with `guard_evaluation`
   `released` for a pane answer let through) and the request's
   `ad.row_mutation.committed`.
6. Neither proves the request gone: a plain `send-keys` without a pane
   hash stays held (`ErrDialogMaybeOpen`) until Claude Code does (after a
   deny, normally the turn's `Stop`). To type before that, read the pane
   again and send with that read's `pane_sha256`.

### Known limitations

`decide`, `send-keys` and the readers judge stored records and processes,
never Claude Code's screen. These cases are not covered:

- **What fallen back does not say.** `fallen_back` and
  `ErrRelayFallenBack` mean no answer from the relay reached the agent and
  no pane answer is recorded through agent-director. They do not say
  whether Claude Code's permission prompt still waits: someone at the pane
  may already have answered it. Until such a request is closed
  (`record-pane-answer`, or the PostToolUse close of an allow), `get`
  keeps listing it, a plain `send-keys` is refused, the spawn stays in
  `check_permission` until a later hook moves it out (the agent's `Stop`
  or `AskUserQuestion`), and `find-missing` does not repair the row.
  Once it is closed, a plain `send-keys` without a pane hash stays held
  until Claude Code proves it gone.
- **A prompt agent-director has no record of.** A relay hook that dies, or
  is killed, before it records its request leaves a prompt that no request
  stands for, so a plain `send-keys` is not refused, and its Enter can
  answer that prompt. The dialog hold starts at the request's first write,
  so it does not cover this case.
- **A confirmed answer not returned.** The relay hook confirms its verdict
  or its timeout deny at least 2 s before Claude Code ends it, then writes
  it. A hook that dies or stalls past Claude Code's kill between the two
  leaves the request reading `delivered` while Claude Code never received
  the answer: `decide` on it returns `ErrAlreadyDecided`, which advises no
  pane answer. The request is not proven gone, so a plain `send-keys`
  without a pane hash is held (`ErrDialogMaybeOpen`) and `get` lists it in
  `unproven_requests` until someone looks at the pane.
- **Between the capture and the key.** The pane can change between
  agent-director's capture and the moment Claude Code reads the key, a
  window of milliseconds that no check closes. Likewise, a key already on
  its way when a dialog appears, before its request's first write, lands
  on that dialog.
- **Identical prompts.** Two prompts whose captured bytes are the same
  (same tool, same input, same surrounding lines) cannot be told apart: a
  pane answer meant for one can close the other, and the record then names
  the wrong request. The other request stays unproven, so a plain
  `send-keys` without a pane hash is held until someone looks.
- **A prompt drawn more than 2 s late.** `record-pane-answer` waits 2 s
  after the relay hook was found gone (or after its `confirm_by`, for a
  hook judged by time). A Claude Code that stalls longer
  before drawing its prompt can let a caller record the request answered
  before the prompt appears. The request stays unproven, so a plain
  `send-keys` without a pane hash is held until Claude Code proves it gone.
- **A subagent's denied request.** A request carrying an `agent_id` (a
  subagent's or an in-process teammate's) is proven gone only by its
  tool's `PostToolUse` or by the spawn's end: the main agent's `Stop` and
  idle-prompt Notification do not cover it, since a background subagent
  can outlive the main turn, and agent-director does not register the
  subagent's own stop hook. A deny runs no tool, so after a denied
  subagent request a plain `send-keys` without a pane hash is held until
  the spawn's agent ends; only a person or an LLM who looked, sending with
  the pane's hash, gets keys through.
- **Lost end-of-turn hooks.** After a denied request, if both the turn's
  `Stop` and the idle-prompt Notification are lost, a plain `send-keys`
  without a pane hash stays held until someone looks and sends with the
  pane's hash, or the spawn ends.
- **A key to stop a busy agent after a deny.** An `Escape` sent to stop the
  agent is a plain `send-keys`: right after a deny it is held until the
  turn ends, so it needs a person or an LLM to look and send it with the
  pane's hash.
- **`pause` is not held.** `pause` types `C-u`, `/exit` and Enter into a
  `waiting` spawn with no relay check: neither the dialog hold nor
  `ErrSendKeysWhileRelayed` or `ErrRelayFallenBack` refuses it. A spawn
  can read `waiting` while one of its requests is not proven gone (a
  subagent's request after the main agent's `Stop`, or a request recorded
  before this release on an agent already idle at its prompt when you
  upgraded), and if that request's dialog is still on the pane, `pause`'s
  Enter answers it. Before pausing a relay-on spawn, read `get` and pause
  only while its `unproven_requests` is empty, or have a person or an LLM
  look at the pane first.
- **A pane answer whose sender died.** It leaves `pane_answer` `intent`,
  its key possibly typed. The request stays open and fallen back, so plain
  `send-keys` stays refused, until a retry with a fresh hash or
  `record-pane-answer` closes it.
- **Requests recorded before this release.** `decide`'s check that the
  spawn is shown sitting on such a request alone reads stored records
  only, so it can be wrong both ways. A record left open after its prompt
  closed (answered at the pane after the hook was killed, or closed by a
  timeout deny the hook returned without recording it) keeps the spawn in
  `check_permission` until a later hook moves it out, and until then
  `decide` still returns `ErrRelayFallenBack` for it, whose advice is a
  pane answer: a caller that looks at the pane finds no prompt for it and
  records it with `record-pane-answer` instead. A later request in the gap
  between its hook moving the spawn to `check_permission` and its record
  being written can make the check pass for a stale record too. And a
  subagent's request recorded while an earlier prompt waits, or a stale
  record still open, makes `decide` refuse a request whose prompt waits
  with `ErrNoOpenPermissionRequest`; the request stalls until a human
  answers it at the pane.
- **A pane answer does not apply `decide`'s check.** `send-keys` with the
  token of a request recorded before this release is accepted once the
  request has fallen back and the caller's hash matches, even when
  `decide` refused that request with `ErrNoOpenPermissionRequest`.
  Following that error's "do not answer it at the pane" is up to the
  caller.

## References

- Claude Code IAM / permissions:
  <https://docs.claude.com/en/docs/claude-code/iam#permission-rules>
- Claude Code settings file:
  <https://docs.claude.com/en/docs/claude-code/settings>
- Empirical investigation (gitignored, in-repo):
  `reference/permissions-deny-tool-name-research.md`,
  `reference/claude-settings-research.md`
