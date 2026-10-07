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

1. Claude Code fires the PermissionRequest hook with the tool name +
   tool input.
2. The hook handler reads `AGENT_DIRECTOR_RELAY_MODE` from its env
   (NOT the DB — see "Fail-closed boundary" below).
3. If the env var is `on`, the handler:
   - Mints a per-request UUIDv4 `request_token` and INSERTs an open
     row into `permission_requests`, keyed by the composite
     `(claude_instance_id, request_token)`. Concurrent requests for
     the same Spawn each get their own row and are decided
     independently. Oldest *closed* rows are evicted in the same
     transaction when the table exceeds `relay.permission_request_cap`
     (default 1000; `0` disables eviction).
   - Polls its own row at
     `max(50ms, relay.poll_base_ms + uniform(0, relay.poll_jitter_ms))`
     intervals.
   - On a decided row → writes the decision envelope to stdout.
   - On timeout / ctx-cancel / row preempted / read-retry exhaustion
     → writes a deny envelope (fail-closed).
4. The orchestrator calls `agent-director decide --claude-instance-id
   <id> --request-token <token> --decision allow|deny --reason "..."`
   to write the decision. The token comes from the Spawn's open
   `permission_requests` in `get` output, or from `get-permission`.
5. The hook's polling loop sees the decision on its next read and
   emits the envelope.

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
failure mode as a deny — provided the event is known to be a
PermissionRequest. Failures that occur before the event name can be
read from the payload exit silently instead: Claude Code routes hook
stdout by file descriptor, so a permission-shaped deny emitted by a
process that might be handling a *different* event (e.g. PreToolUse)
would be applied to the in-flight tool and race the legitimate
PermissionRequest process (the b.45p fix). SRD §6.4 enumerates the
failure modes:

| Failure | Outcome |
|---|---|
| `AGENT_DIRECTOR_INSTANCE_ID` missing / invalid | deny envelope |
| Config load failure | deny envelope |
| Store open failure (including no usable `HOME`: unset or empty) | deny envelope |
| stdin payload read failure | silent exit 0 (event name unknowable — b.45p) |
| Classify failure (unparseable payload) | silent exit 0 (event name unknowable — b.45p) |
| UPSERT failure | deny envelope |
| Polling timeout (`relay.timeout_seconds`) | deny envelope |
| `ctx.Done()` during poll | deny envelope |
| Row preempted (`sql.ErrNoRows` during poll) | deny envelope |
| Read-retry budget exhausted (5 consecutive read errors) | deny envelope |

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
the relay hook about 1 ms after it starts. A window of 1 s loads, but
`decide` refuses from 1 s before the window ends (see "Deliver-or-refuse
contract" below), so under it every `decide` on an open request is
refused: it returns `ErrAlreadyDecided` once the relay hook has denied
the request at its timeout, otherwise `ErrRelayFallenBack`.

**How the window is enforced.** The window is real because agent-director
emits it into the per-Spawn synthesized settings. Each PermissionRequest and
PreToolUse hook entry carries an explicit per-hook `timeout` field — placed on
the inner command object, sibling to `type`/`command` — set to
`relay.timeout_seconds`. Without that field Claude Code kills any hook at its
own 600-second default and discards its output, so a late decision would be
silently voided; emitting the value makes Claude Code's per-hook kill boundary
equal to the window agent-director polls against.

**Override moves both boundaries in lockstep.** `relay.timeout_seconds` is a
single value read through one accessor, so overriding it changes the poll
loop's deadline and Claude Code's per-hook kill boundary together, and with
them `decide`'s window and the send-keys guard's — they can never disagree.
That holds because the config load bounds the value: inside the range,
Claude Code applies the per-hook `timeout` as written, and neither the
window, the window plus or minus the 1 s safety margin, nor the poll
deadline overflows, so no boundary silently becomes a different window.
Because the two boundaries are identical, the poll loop's
fail-closed timeout deny (the `Polling timeout` row above) is the intended
in-band terminator: when the
window elapses the hook writes a deny envelope and exits on its own, rather
than being killed mid-flight by Claude Code.

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
envelope arriving any time within the window dismisses that dialog.
Whether a decision is still deliverable is purely a function of
elapsed time since the request opened versus the configured
per-hook timeout; the dialog's presence or absence in the TUI
carries no information about hook liveness.

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

The decide verb writes the decision via a single-statement UPDATE
guarded by both the first-call-wins `decision IS NULL` predicate and a
`created_at > ?` deliverability predicate (the deliverability check and
the write are one atomic statement):

```sql
UPDATE permission_requests
   SET decision = ?, decision_reason = ?, decided_at = CURRENT_TIMESTAMP
 WHERE claude_instance_id = ? AND request_token = ? AND decision IS NULL
   AND created_at > ?
```

First call wins; concurrent second calls see RowsAffected==0. The
verb then does one follow-up SELECT to disambiguate the outcome. The
follow-up disambiguation resolves to exactly one of these three
outcomes:

- No row at all → `ErrNoOpenPermissionRequest`.
- Row exists with non-NULL decision → `ErrAlreadyDecided` (including a
  request the relay hook denied at its timeout).
- Row is still open but its relay window has elapsed →
  `ErrRelayFallenBack`, or `ErrAlreadyDecided` if the relay hook denies
  it while `decide` waits at the window's end (see "Deliver-or-refuse
  contract" below).

Two orchestrators racing to decide the same prompt see distinct
error messages and can act on them programmatically.

### Deliver-or-refuse contract

A successful `decide` **means the decision will be delivered**: the
spawn's relay hook received — or is still polling and will receive —
the decision envelope. There is no silent-absorption path where
`decide` reports success but the verdict goes to a dead hook that can
never emit it. Success and delivery are the same event.

When the request's relay window has elapsed, or is about to, `decide`
refuses rather than record a doomed verdict. It refuses once the time
since the row's `created_at` has reached the relay window less a safety
margin (`RelayKillSafetyMargin`, 1 s): from 1 s *before* the window
ends, so a verdict is never recorded for a request Claude Code is about
to — or has just — killed.

**The wait at the window's end.** A relay hook still alive when
`decide` starts refusing denies the request at its poll deadline
(`decision_reason` `timeout`), moves the row to `working` and normally
returns the deny to Claude Code, which closes the permission dialog. A
pane answer sent after that would be typed into Claude's prompt as a
user message. The hook's deadline is the window counted from when it
started polling, just after the request was stored, but the stored
`created_at` keeps whole seconds only. So counted from `created_at`, the
hook's deny can land up to 2 s after the window ends: 1 s for that
rounding, plus the 1 s safety margin for the hook's own delays. A
`decide` refused before then first waits until 2 s after the window's
end (at most 3 s; the call blocks for that time) and reads the request
again. A `decide` refused later does not wait. Either way, the refusal
of a request past its window is one of:

- **`ErrAlreadyDecided`**: the request was decided before the call or
  during the wait. If the relay hook denied it, the hook normally
  returned that deny to Claude Code, which closed the dialog, so there
  is nothing to answer at the pane. `get-permission` returns the
  recorded `decision_reason` (`timeout` for the hook's deny); the
  error's message names it too.
- **`ErrNoOpenPermissionRequest`**: the request was removed during the
  wait (its spawn's row was deleted).
- **`ErrRelayFallenBack`**: the request's record is still open and its
  relay hook can no longer answer it. The error's message tells the
  caller to answer at the pane with `send-keys`. It states no release
  time: by then the send-keys relay guard has already released on this
  request's account.

Guarantees when `ErrRelayFallenBack` is returned:

- **The decision was NOT recorded.** The row's `decision` stays NULL;
  no verdict is written into a void. Nothing about the request is lost.
- **The request's record is still open 2 s after its window ended.**
  Its relay hook neither delivered a verdict nor recorded its timeout
  deny, and is presumed dead. That rests on the hook's deny landing
  within those 2 s, which the rounding and the safety margin allow for.
  `decide` reads only the record, so it does not tell whether Claude
  Code's native permission dialog is still on screen.
- **Recourse is the pane, through `send-keys`.** Because the relay hook
  can no longer deliver a decision, the operator answers Claude Code's
  native permission dialog directly — through the sanctioned, audited
  `send-keys` recovery path, never raw tmux. That path opens on the same
  time-based signal with the margin's sign reversed: the send-keys relay
  guard releases 1 s *after* every one of the spawn's requests' windows
  has elapsed. On the refused request's account it has already released
  when `ErrRelayFallenBack` is returned, so the guard does not refuse a
  `send-keys` that follows it on that request's account; the caller
  never waits out the margin itself. If `send-keys` still refuses with
  `ErrSendKeysWhileRelayed`, another of the spawn's requests holds the
  guard; the refusal names it: answer that one with `decide`. See
  "Send-keys interaction" below.

**The undeliverability signal is time-based, never dialog-based.** A
request is undeliverable once the elapsed time since its
`created_at` reaches the configured per-hook timeout
(`relay.timeout_seconds`) less the safety margin — a pure function of
stored row state, the configured window, and the clock. It never
consults whether the native permission dialog is on screen: dialog
visibility carries no information about relay-hook liveness (see "The
native dialog is not a hook-death signal" above). The same time-only
signal that the guarded write applies is the one that classifies the
refusal. The wait at the window's end is counted from the same
`created_at` with the same margin, plus 1 s for the rounding of
`created_at`, so it ends 1 s after the send-keys guard's release point
for the request. That wait absorbs the span between `decide`'s refusal
and the guard's release, so neither error's message states a release
time.

**Precedence.** `ErrRelayFallenBack` applies **only to open rows**. A
row that already carries a decision returns `ErrAlreadyDecided`
regardless of its age — an old but already-decided request is never
reclassified as fallen-back. So the decided/undecided taxonomy stays
clean: decided rows → `ErrAlreadyDecided`; open-but-expired rows →
`ErrRelayFallenBack`; absent rows → `ErrNoOpenPermissionRequest`. A row
the relay hook denies at its timeout while `decide` waits is decided, so
it is `ErrAlreadyDecided`.

### Send-keys interaction

When a Spawn is sitting on a relayed permission prompt (`relay_mode=on`
AND `state=check_permission`), `send-keys` may refuse with
`ErrSendKeysWhileRelayed`: while the relay can still act, a pane-side
keystroke would race the relay's `decide()` write and split the modal
answer across two pane events, so the relay owns the answer and callers
drive the modal through `decide`. The error name is the contract; its
message (advice) names the request holding the guard, by its
`request_token`, advises answering it with `decide`, and states no
release time: `spawn <id> is awaiting a relayed permission decision on
request <request_token>; answer it with decide`. When several requests
hold the guard it names an undecided one before a decided one, then the
oldest. The named request is either:

- **Pending**: `decide` answers it (near the window's end, after the
  wait described in "The wait at the window's end" above).
- **Already decided**, its verdict possibly still being delivered by its
  relay hook: `decide` returns `ErrAlreadyDecided` and there is nothing
  to answer. It holds the guard until the spawn leaves
  `check_permission` or its relay can no longer deliver, whichever is
  first; while the spawn stays in `check_permission`, that can be up to
  the request's full relay window.

With zero request rows the request is still being recorded, so the
message names none: `… whose request is not yet recorded; answer it
with decide once get lists it` (`get` lists open requests under
`permission_requests`).

**The guard is time-bounded, not unconditional.** It consults the
*same* single time-based authority the decide contract uses (same file,
same margin constant in `pkg/api/deliverability.go`; see "The
undeliverability signal is time-based, never dialog-based" above) —
never dialog visibility, never a second independent check. The one
deliberate difference is the *sign* of the safety margin at the
boundary: `decide` fails **early** (refuses at `elapsed ≥ window −
margin`, so it never records a success a dying hook might not deliver),
while the guard fails **late** (releases only at `elapsed ≥ window +
margin`, so it never frees while a live poller could still emit a
decision). Both fail toward safety; it is one authority applied with the
sign that makes each caller safe. It evaluates every one of the spawn's
`permission_requests` rows, each row's window measured from its own
`created_at` and *regardless of the row's decision status* (a row
decided in-window still has a live poller about to deliver it).
Concretely:

- **Refuse while any row might still be delivered** — the relay can
  still deliver, so send-keys stays out of the way.
- **Release only once every row's window plus the safety margin has
  elapsed** (1 s after the last window ends) — at that point no poller
  can deliver any decision, the guard would be pure denial of service,
  and send-keys is the sanctioned recovery surface (below). The margin
  is internal: the refusal's message does not state it. A `send-keys`
  refused between `decide`'s cutoff (1 s before the window ends) and the
  guard's release is told to use `decide` on the request it names; for
  an open request `decide` waits out that span (see "The wait at the
  window's end" above) and returns `ErrAlreadyDecided` or
  `ErrRelayFallenBack`.
- **Zero rows keep the guard held.** With no row there is no signal and
  no authority to release; the state is a real mid-insert transient, so
  the guard refuses rather than open a race.

#### Sanctioned recovery of a wedged relayed spawn

When a relayed spawn is wedged past its window — the relay hook was
killed at its per-hook timeout and can no longer deliver — the operator
recovers it end-to-end through sanctioned AD surface, with no dedicated
answer-the-dialog verb and **without ever touching raw tmux**:

1. `decide` returns the typed `ErrRelayFallenBack`: the verdict was not
   recorded, the request's record is still open, and delivery is no
   longer possible. `decide` refuses from 1 s before the request's
   window ends and, until 2 s after it, waits for the rest of that time
   before it answers (see "The wait at the window's end" above). If it returns
   `ErrAlreadyDecided` instead, the relay hook (or another caller)
   answered the request and there is nothing to recover.
2. The send-keys guard releases by the same time-based authority, but
   only once every row's window plus the safety margin has elapsed —
   1 s after the last window ends (see the asymmetric-margin note
   above). On the refused request's account it has already released
   when `decide` returns `ErrRelayFallenBack`, so the operator sends at
   once. A request of the same spawn opened later may still hold it;
   `send-keys` then refuses with `ErrSendKeysWhileRelayed` naming that
   request, and the operator answers it with `decide`.
3. `send-keys` answers the native permission dialog directly.
4. The action is audited: it appears in the trail as
   `ad.send_keys.called` with `guard_evaluation=released`, so a recovery
   send is distinguishable from an ordinary send and from a guard
   refusal.

This is the in-band recovery surface referenced under `ErrRelayFallenBack`
above; it is now available.

## References

- Claude Code IAM / permissions:
  <https://docs.claude.com/en/docs/claude-code/iam#permission-rules>
- Claude Code settings file:
  <https://docs.claude.com/en/docs/claude-code/settings>
- Empirical investigation (gitignored, in-repo):
  `reference/permissions-deny-tool-name-research.md`,
  `reference/claude-settings-research.md`
