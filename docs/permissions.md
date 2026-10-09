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
     eviction). A spawn's newest request is kept while that spawn has a
     request that still awaits an answer, because `decide` relies on it
     for a request recorded before this release (see "Requests recorded
     before this release" below), so the table can stay above the cap by
     one closed row per such spawn.
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
closed by `find-missing`, a `created_at > ?` deliverability predicate that
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
  request the relay hook denied at its timeout, and one `find-missing`
  denied when it marked the spawn `missing`).
- Row was closed by `find-missing` with a verdict recorded before the mark
  that its relay hook had not confirmed → `ErrNoOpenPermissionRequest`,
  whether or not the spawn was resumed since (see "A request of a finished
  spawn is closed" below).
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

- a decided request → `ErrAlreadyDecided`;
- an open request → `ErrNoOpenPermissionRequest`, whose message says the
  request is closed because the spawn is `ended` or `missing`, nothing was
  recorded, and not to answer it at the pane;
- no such request → `ErrNoOpenPermissionRequest`, as for a live spawn.

`decide` checks the spawn's state when it reads the spawn, and its write
checks it again in the same statement, so a spawn that finishes between
the two still gets nothing recorded. `send-keys` refuses an `ended` or
`missing` spawn before its relay guard reads any request
(`ErrSpawnNotInteractive`).

`find-missing` closes a spawn's open requests when it marks the spawn
`missing`, in the same store transaction as the mark: both are written or
neither is. Every request that still awaits an answer is closed, decided
or not:

- an undecided request gets `decision` `deny` with `decision_reason`
  `find_missing`, so a relay hook still polling for it reads that deny
  (and returns it, once confirmed, if its Claude Code is still the one
  that started it); `decide` on it returns `ErrAlreadyDecided`;
- a decided request whose relay hook had not confirmed the verdict keeps
  it (a hook whose Claude Code still runs may still confirm and return
  it); `decide` on it returns `ErrNoOpenPermissionRequest`, whose message
  says the request is closed, nothing was recorded, and not to answer it
  at the pane (`ErrAlreadyDecided` once its hook has confirmed it).

A closed request stays closed after a `resume`: it no longer awaits an
answer, so `get` and `list` do not show it and it no longer holds the
spawn in `check_permission`, and `decide` refuses it as above, never with
`ErrRelayFallenBack`. `get-permission` still reads it by its token, and
its `delivery` follows the rules in "Delivery" below like any request's:
`delivered` once its hook confirmed, `not_confirmed` while that hook may
still run, `fallen_back` once it is gone (or cannot be checked and
`confirm_by` has passed). A spawn marked `missing` never keeps an open
request.

### Delivery

Every relayed request carries its delivery facts. They are worked out each
time the request is read, by `decide`, `get`, `list` and `get-permission`;
nothing but `hook_gone_at` is written for them:

| Field | Meaning |
|---|---|
| `delivery` | `delivered`: the relay hook confirmed a verdict in the store before handing it to Claude Code. `not_confirmed`: no confirmation yet, and the hook may still run. `fallen_back`: no confirmation, no pane answer recorded through agent-director, and the hook is gone, or cannot be checked and `confirm_by` has passed: no answer from the relay reached the agent, and only an answer at the pane can close the request. |
| `confirm_by` | The request's settle instant: the moment Claude Code ends its relay hook plus 2 s. `not_confirmed` never lasts past it. |
| `hook_alive` | `true` or `false` by the check below; `null` when it cannot tell. |
| `hook_gone_at` | When a reader first found the request fallen back; `null` until then. |
| `attempted_decision`, `attempted_at` | The verdict a `decide` refused with `ErrRelayFallenBack` tried to record, and when; shown, never acted on. |
| `tool_use_id` | The `tool_use_id` of Claude Code's hook input; `null` when it gave none. |

**`decision` is not the outcome.** `decision` is the verdict recorded,
set before delivery is known: a request can read `decision` `allow` with
`delivery` `fallen_back`. Read `delivery` for the outcome.

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

**What `decide` does.** On a request whose hook has fallen back, `decide`
returns `ErrRelayFallenBack` at once and records no verdict; it stores the
caller's verdict as `attempted_decision` and `attempted_at`, and
`hook_gone_at` when not yet set. Otherwise one guarded write records the
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
agent), and not closed by `find-missing`. A row can read `waiting` while
one of its requests still awaits an answer, so a caller follows each
request it tracks with `get-permission`, not only rows in
`check_permission`.

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
  `not_confirmed`; after it, `delivered` when a verdict other than
  `find-missing`'s is recorded, else `fallen_back`. Such a request awaits
  an answer while it is undecided.
- `decide` records a verdict on it only before its window less 1 s, and
  returns at once, with no wait for a confirmation such a hook never
  writes. Refused from then, it first waits until `confirm_by` (at most
  3 s; under `--max-wait-ms` that ends sooner it returns `ErrStoreBusy`
  instead) and reads the request again: decided meanwhile (normally the
  hook's own timeout deny, `decision_reason` `timeout`) →
  `ErrAlreadyDecided`; still open → `ErrRelayFallenBack` only while the
  spawn is still in `check_permission` with no other open request and none
  recorded after this one, otherwise `ErrNoOpenPermissionRequest` (do not
  answer it at the pane). Eviction at the cap keeps the spawn's newest
  request while this one is open, so a later request stays visible to that
  check.

### Send-keys interaction

When a Spawn is sitting on a relayed permission prompt (`relay_mode=on`
AND `state=check_permission`), `send-keys` may refuse with
`ErrSendKeysWhileRelayed`: while the relay can still act, a pane-side
keystroke would race the relay's `decide()` write and split the modal
answer across two pane events, so the relay owns the answer and callers
drive the modal through `decide`. The error name is the contract; its
message (advice) names the request holding the guard, by its
`request_token`, says what to do, and states no release time. When
several requests hold the guard it names an undecided one before a
decided one, then the oldest. The named request is either:

- **Pending**: `spawn <id> is awaiting a relayed permission decision on
  request <request_token>; answer it with decide`.
- **Already decided**, named only when no pending request holds the
  guard: `spawn <id>: the relayed permission verdict on request
  <request_token> is recorded and its relay hook may still be delivering
  it; retry send-keys later`. There is nothing left to answer (`decide`
  on it returns `ErrAlreadyDecided`). It holds the guard until the spawn
  leaves `check_permission`, another of the spawn's requests falls back by
  its window (see below), or 2 s have passed since its window ended,
  whichever is first. A request whose relay hook died after its verdict
  was recorded, with no other request fallen back, holds until then.

With zero request rows the request is still being recorded, so the
message names none: `… whose request is not yet recorded; answer it
with decide once get lists it` (`get` lists open requests under
`permission_requests`).

**The guard is time-bounded, and judges every request by its window.**
Each request holds the guard until 2 s after its relay window ends,
counted from its own `created_at`, pending or decided — a request recorded
from this release on too, although `decide` judges such a request by its
relay hook's process. So after `decide` refuses such a request with
`ErrRelayFallenBack`, which it does within seconds of the hook's end,
`send-keys` is still refused on that request's account until 2 s after its
window ends. The guard consults the same time-based authority as the
pre-release `decide` contract (same file, same margin constant in
`pkg/api/deliverability.go`) — never dialog visibility. The one deliberate
difference from that `decide` contract is the *sign* of the safety margin:
`decide` fails **early** (refuses at `elapsed ≥ window − margin`), while the
guard fails **late** (releases only at `elapsed ≥ window + margin + 1 s` for
the rounding of `created_at`), so it does not free while a live relay hook
could still answer, but for the residual race below. Concretely:

- **Refuse while any row holds the guard.** A row holds until 2 s after
  its window ends, pending or decided: the relay can still deliver its
  verdict or its timeout deny, so send-keys stays out of the way. The next
  bullet is the one exception.
- **A decided row stops holding once another request has fallen back by
  its window**: its record is still open 2 s after its window ended. Its
  open record keeps the spawn in `check_permission`, so a decided request
  of the same spawn no longer holds the guard; otherwise a `send-keys`
  retried as that request's refusal advises would be refused for up to its
  full relay window. Pending requests still in their windows keep holding,
  so a pane answer never overtakes a verdict `decide` can still record.
  The trade-off: the exception assumes Claude Code shows the oldest
  pending permission dialog first, so the fallen-back request's dialog
  (recorded before every request still in its window) is the one on
  screen. It gives up the span between a `decide` and Claude Code acting
  on the decided request's hook output: up to one poll sleep of its live
  relay hook (`relay.poll_base_ms` plus jitter up to
  `relay.poll_jitter_ms`; see "Flow" above) before the hook reads the
  verdict, plus the hook confirming it, writing its output and exiting.
  The assumption fails when a fallen-back request's dialog is no longer
  on screen (its record was left open after the dialog closed): keys sent
  in that span can then land in the decided request's still-pending
  dialog. The guard does not apply `decide`'s checks (see "Known
  limitations" below).
- **Release once no row holds** — 2 s have passed since every row's
  window ended, a decided row's excepted once another request has fallen
  back. At that point no pending request's relay hook is presumed able to
  deliver a decision (but see "Residual race" below), the guard would be
  pure denial of service, and send-keys is the sanctioned recovery surface
  (below). The margin is internal: the refusal's message does not state
  it.
- **Zero rows keep the guard held.** With no row there is no signal and
  no authority to release; the state is a real mid-insert transient, so
  the guard refuses rather than open a race.

**Residual race.** The guard's release 2 s after the window ends rests on
a live relay hook having answered, or been killed by Claude Code, by then.
A relay hook of this release commits its last store write at least 2 s
before Claude Code ends it, which comes no later than 1 s after the
window counted from `created_at`. The guard can still release before a
live hook's verdict or deny reaches Claude Code only if both:

- the hook's delivery of its verdict or timeout deny (reading the
  verdict, or noticing its deadline, confirming it in the store; then
  writing its envelope and exiting) ends more than 2 s past its window,
  for example because the process stalls (a relay hook from before this
  release could also wait up to `[store] busy_timeout_ms`, 10 s by
  default, for each store write); and
- Claude Code kills the hook more than 1 s after its per-hook timeout.

Keys sent at the pane then can land in Claude's prompt. Nothing stored
shows a hook stuck delivering, so this race is not closed. All of these
instants assume the hook, the store and the caller share one wall clock.

#### Sanctioned recovery of a wedged relayed spawn

At the window's end the relay hook normally denies the request itself
(see "Relay timeout default and override" above): `decide` then returns
`ErrAlreadyDecided` (`decision_reason` `timeout`) and there is nothing to
recover. A relayed spawn is wedged when its relay hook died or was killed
without answering: `decide` returns `ErrRelayFallenBack`, with no verdict
recorded, and only an answer at the pane can close the request. The
operator then recovers it through sanctioned AD surface, with no dedicated
answer-the-dialog verb and **without ever touching raw tmux**:

1. `decide` returns the typed `ErrRelayFallenBack` (see "Delivery" above,
   and for a request recorded before this release "Requests recorded
   before this release"). If it returns `ErrAlreadyDecided` instead, the
   relay hook (or another caller) answered the request and there is
   nothing to recover. If it returns `ErrNoOpenPermissionRequest`, the
   request is closed or its dialog cannot be shown to be on screen: do not
   answer it at the pane.
2. The send-keys guard releases by its time-based authority, once no row
   holds it — 2 s after the last pending request's window ends (while a
   request that fell back by its window stays open, decided requests no
   longer hold it). A request of the same spawn still pending in its
   window may still hold it; `send-keys` then refuses with
   `ErrSendKeysWhileRelayed` naming that request, and the operator answers
   it with `decide`.
3. Once the guard has released, `send-keys` is accepted: an operator who
   has looked at the pane answers there.
4. The action is audited: it appears in the trail as
   `ad.send_keys.called` with `guard_evaluation=released`, so a recovery
   send is distinguishable from an ordinary send and from a guard
   refusal.

### Known limitations

`decide` and the readers judge stored records and the relay hook's
process, never Claude Code's screen. These cases are not covered:

- **What fallen back does not say.** `fallen_back` and
  `ErrRelayFallenBack` mean no answer from the relay reached the agent.
  They do not say whether Claude Code's permission prompt still waits:
  someone at the pane may already have answered it, and nothing records
  such an answer. The request then keeps awaiting an answer: `get` keeps
  listing it, the spawn stays in `check_permission` until a later hook
  moves it out (the agent's `Stop` or `AskUserQuestion`), and
  `find-missing` does not repair the row.
- **A confirmed answer not returned.** The relay hook confirms its verdict
  or its timeout deny at least 2 s before Claude Code ends it, then writes
  it. A hook that dies or stalls past Claude Code's kill between the two
  leaves the request reading `delivered` while Claude Code never received
  the answer; `decide` on it returns `ErrAlreadyDecided`, which advises no
  pane answer.
- **Requests recorded before this release.** `decide`'s check that the
  spawn is shown sitting on such a request alone reads stored records
  only, so it can be wrong both ways. A record left open after its dialog
  closed (answered at the pane after the hook was killed, or closed by a
  timeout deny the hook returned without recording it) keeps the spawn in
  `check_permission` until a later hook moves it out, and until then
  `decide` still returns `ErrRelayFallenBack` for it; keys sent at the
  pane as it advises are typed into Claude's prompt. A later request in
  the gap between its hook moving the spawn to `check_permission` and its
  record being written can make the check pass for a stale record too.
  And a subagent's request recorded while an earlier dialog waits, or a
  stale record still open, makes `decide` refuse a request whose dialog
  waits with `ErrNoOpenPermissionRequest`; the request stalls until a
  human answers it at the pane.
- **`send-keys` does not enforce `decide`'s advice.** The send-keys
  relay guard releases on the time-based signal (see "Send-keys
  interaction" above) and does not apply `decide`'s checks. Once it has
  released, `send-keys` is accepted even for a request `decide` refused
  with `ErrNoOpenPermissionRequest`. Following that error's "do not
  answer it at the pane" is up to the caller.

## References

- Claude Code IAM / permissions:
  <https://docs.claude.com/en/docs/claude-code/iam#permission-rules>
- Claude Code settings file:
  <https://docs.claude.com/en/docs/claude-code/settings>
- Empirical investigation (gitignored, in-repo):
  `reference/permissions-deny-tool-name-research.md`,
  `reference/claude-settings-research.md`
