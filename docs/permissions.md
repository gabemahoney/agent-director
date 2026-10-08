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
     (default 1000; `0` disables eviction). A spawn's newest request is
     kept while that spawn has an open request, because `decide` relies
     on it (see "Shown to be sitting on the request alone" below), so
     the table can stay above the cap by one closed row per such spawn.
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
the request at its timeout, otherwise `ErrRelayFallenBack` or
`ErrNoOpenPermissionRequest` (see "The wait at the window's end" below).

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
window, the window less the 1 s safety margin or plus 2 s (the boundaries
below), nor the poll deadline overflows, so no boundary silently becomes a
different window.
The two boundaries use the same window but count it from different
instants: Claude Code from when it starts the hook, the poll loop from the
request's stored `created_at`. `created_at` keeps whole seconds only, so it
is up to 1 s earlier than the hook's insert, which comes just after Claude
Code starts the hook. So the poll deadline normally comes first, and the
poll loop's fail-closed timeout deny (the `Polling timeout` row above) is
the intended in-band terminator: when the window elapses the hook records
the deny, writes a deny envelope and exits on its own, which closes the
dialog, rather than being killed mid-flight by Claude Code. Claude Code
kills the hook first only when its insert came late enough after its start
to fall in a later whole second, or when the hook stalled or died; that
request then falls back (see "Deliver-or-refuse contract" below).

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
  it while `decide` waits at the window's end, or
  `ErrNoOpenPermissionRequest` if the spawn is not shown to be sitting
  on that request alone (see "Deliver-or-refuse contract" below).

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
user message. The hook's deadline is the window counted from the
request's stored `created_at`; after it the hook still has to record the
deny and exit. A hook that has not reached its deadline is instead killed
by Claude Code, whose per-hook timeout started just before the request was
stored; `created_at` keeps whole seconds only, so counted from
`created_at` that kill can come up to 1 s after the window ends, plus its
own lateness. So `decide` allows 2 s after the window ends: 1 s for that
rounding, plus the 1 s safety margin. A
`decide` refused before then first waits until 2 s after the window's
end (at most 3 s; the call blocks for that time) and reads the request
again. A `decide` refused later does not wait. Either way, the refusal
of a request past its window is one of:

- **`ErrAlreadyDecided`**: the request was decided before the call or
  during the wait. If the relay hook denied it, the hook normally
  returned that deny to Claude Code, which closed the dialog, so there
  is nothing to answer at the pane (see "Known limitations" below for
  when it did not). `get-permission` returns the recorded
  `decision_reason` (`timeout` for the hook's deny); the error's
  message names it too.
- **`ErrNoOpenPermissionRequest`**: the request was removed during the
  wait (its spawn's row was deleted), or its record is still open and its
  relay hook can no longer answer it, but the spawn is not shown to be
  sitting on that request alone (see below). Its permission dialog may
  have closed, and a pane answer to a closed dialog is typed into
  Claude's prompt as a user message, so do not answer it at the pane.
  In that second case the message says why and ends `… so its
  permission dialog cannot be shown to be on screen; do not answer it at
  the pane`.
- **`ErrRelayFallenBack`**: the request's record is still open, its
  relay hook can no longer answer it, and the spawn is shown to be
  sitting on that request alone. The error's message tells the caller to
  answer at the pane with `send-keys`. It states no release time: by
  then the send-keys relay guard has already released on this request's
  account.

**Shown to be sitting on the request alone.** `decide` reads stored
records, not Claude Code's screen, and a record can stay open after its
dialog closed: the dialog was answered at the pane after the relay hook
was killed, or the hook returned its timeout deny without recording it.
So, for a request still open after the wait, `decide` reads the spawn
and all of its requests again and returns `ErrRelayFallenBack` only if
all of these hold; otherwise it returns `ErrNoOpenPermissionRequest`:

- The spawn is still in `check_permission`. Any other state was written
  by a later hook (for example the agent's `Stop`), after Claude Code
  left the request's dialog.
- No request of the spawn was recorded after this one. A later request
  comes from a later PermissionRequest hook, so `check_permission` may
  belong to it. Eviction at the cap keeps the spawn's newest request
  while this one is open, so the later request stays visible.
- No other request of the spawn is open. Claude Code shows the oldest
  pending dialog first, so an older open request's dialog would be the
  one a pane answer reaches.

If the request is decided between those reads, `decide` returns
`ErrAlreadyDecided`; if it is removed, `ErrNoOpenPermissionRequest`.
See "Known limitations" below for what these checks cannot see.

Guarantees when `ErrRelayFallenBack` is returned:

- **The decision was NOT recorded.** The row's `decision` stays NULL;
  no verdict is written into a void. Nothing about the request is lost.
- **The request's record is still open 2 s after its window ended.**
  Its relay hook neither delivered a verdict nor recorded its timeout
  deny, and is presumed dead. That rests on a live hook's deny landing
  within those 2 s, and on Claude Code having killed by then a hook that
  never reached its deadline (see "Residual race" below).
- **The spawn was shown to be sitting on this request alone** when
  `decide` last read it: still in `check_permission`, with no later
  request and no other open request. `decide` does not see Claude Code's
  screen, so this is not proof that the dialog is still up (see "Known
  limitations" below).
- **Recourse is the pane, through `send-keys`.** Because the relay hook
  can no longer deliver a decision, the operator answers Claude Code's
  native permission dialog directly — through the sanctioned, audited
  `send-keys` recovery path, never raw tmux. That path opens on the same
  time-based signal with the margin's sign reversed: on a request's
  account the send-keys relay guard releases 2 s *after* that request's
  window has elapsed, the instant `decide`'s wait ends. On the refused
  request's account it has therefore released when `ErrRelayFallenBack`
  is returned, so the guard does not
  refuse a `send-keys` that follows it on that request's account; the
  caller never waits out the margin itself. While the refused request
  stays open, no already-decided request of the spawn holds the guard
  either. If `send-keys` still refuses with `ErrSendKeysWhileRelayed`,
  another of the spawn's requests, recorded after `decide` read them and
  still pending in its window, holds the guard; the refusal names it:
  answer that one with `decide`. See "Send-keys interaction" below.

**The undeliverability signal is time-based, never dialog-based.** A
request is undeliverable once the elapsed time since its
`created_at` reaches the configured per-hook timeout
(`relay.timeout_seconds`) less the safety margin — a pure function of
stored row state, the configured window, and the clock. It never
consults whether the native permission dialog is on screen: dialog
visibility carries no information about relay-hook liveness (see "The
native dialog is not a hook-death signal" above). The same time-only
signal that the guarded write applies is the one that classifies the
refusal. The wait at the window's end and the send-keys guard's release
are counted from the same `created_at` with the same margin, plus 1 s for
the rounding of `created_at`, so the wait ends as the guard releases on
the request's account. That wait absorbs the span between `decide`'s
refusal and the guard's release, so neither error's message states a
release time.

**Precedence.** `ErrRelayFallenBack` applies **only to open rows**. A
row that already carries a decision returns `ErrAlreadyDecided`
regardless of its age — an old but already-decided request is never
reclassified as fallen-back. So the decided/undecided taxonomy stays
clean: decided rows → `ErrAlreadyDecided`; open-but-expired rows →
`ErrRelayFallenBack` while the spawn is shown to be sitting on that
request alone, otherwise `ErrNoOpenPermissionRequest`; absent rows →
`ErrNoOpenPermissionRequest`. A row the relay hook denies at its timeout
while `decide` waits is decided, so it is `ErrAlreadyDecided`.

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
  request <request_token>; answer it with decide`. `decide` answers it
  (near the window's end, after the wait described in "The wait at the
  window's end" above).
- **Already decided**, named only when no pending request holds the
  guard: `spawn <id>: the relayed permission verdict on request
  <request_token> is recorded and its relay hook may still be delivering
  it; retry send-keys later`. There is nothing left to answer (`decide`
  on it returns `ErrAlreadyDecided`). It holds the guard until the spawn
  leaves `check_permission` (normally once its relay hook has delivered
  the verdict), another of the spawn's requests falls back (see below),
  or 2 s have passed since its window ended, whichever is first.
  A request whose relay hook died after its verdict was recorded, with
  no other request fallen back, holds until then:
  nothing stored tells a dead hook from a slow one. A `send-keys`
  retried after that is delivered.

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
while the guard fails **late** (releases only 2 s after the window ends,
at `elapsed ≥ window + margin + 1 s` for the rounding of `created_at`,
the instant `decide`'s wait at the window's end ends, so it does not free
while a live poller could still emit a decision, but for the residual
race below). Both fail toward safety; it is one authority applied with the
sign that makes each caller safe. It evaluates every one of the spawn's
`permission_requests` rows, each row's window measured from its own
`created_at`, pending or decided (a row decided in-window may still
have a live poller about to deliver it). Concretely:

- **Refuse while any row holds the guard.** A row holds until 2 s after
  its window ends, pending or decided: the relay can still deliver its
  verdict or its timeout deny, so send-keys stays out of the way. A
  pending row thus holds until it has fallen back. The next bullet is the
  one exception.
- **A decided row stops holding once another request has fallen back.**
  A request has fallen back when its record is still open 2 s after its
  window ended: `decide` refuses it with `ErrRelayFallenBack`, or with
  `ErrNoOpenPermissionRequest` when the spawn is not shown to be sitting
  on it alone. Its open record keeps the spawn in `check_permission`,
  and while its dialog is on screen only a pane answer closes it, so a
  decided request of the same spawn no longer holds the guard; otherwise
  a `send-keys` retried as that request's refusal advises would be
  refused for up to its full relay window.
  Pending requests still in their windows keep holding, so a pane answer
  never overtakes a verdict `decide` can still record. The trade-off:
  the exception assumes Claude Code shows the oldest pending permission
  dialog first, so the fallen-back request's dialog (recorded before
  every request still in its window) is the one on screen. It gives up
  the span between a `decide` and Claude Code acting on the decided
  request's hook output: up to one poll sleep of its live relay hook
  (`relay.poll_base_ms` plus jitter up to `relay.poll_jitter_ms`; see
  "Flow" above) before the hook reads the verdict, plus the hook writing
  its output and exiting. The assumption fails when a fallen-back
  request's dialog is no longer on screen (its record was left open
  after the dialog closed): keys sent in that span can then land in the
  decided request's still-pending dialog. `decide` does not advise a
  pane answer for that stale request: the decided request was recorded
  after it, so `decide` refuses it with `ErrNoOpenPermissionRequest`.
  The guard does not enforce that advice (see "Known limitations"
  below).
- **Release once no row holds** — 2 s have passed since every row's
  window ended (2 s after the last window ends), a decided row's
  excepted once another request has fallen back. At that point no
  pending request's poller is presumed able to deliver a decision (but
  see "Residual race" below), the guard would be pure denial of service,
  and send-keys is the sanctioned recovery surface (below). The margin is
  internal: the refusal's message does not state it. A `send-keys`
  refused between `decide`'s cutoff (1 s before the window ends) and the
  guard's release on a pending request's account is told to use `decide`
  on that request; `decide`
  waits out that span (see "The wait at the window's end" above) and
  returns `ErrAlreadyDecided`, `ErrRelayFallenBack` or
  `ErrNoOpenPermissionRequest`.
- **Zero rows keep the guard held.** With no row there is no signal and
  no authority to release; the state is a real mid-insert transient, so
  the guard refuses rather than open a race.

**Residual race.** The guard's release 2 s after the window ends, and
`decide`'s `ErrRelayFallenBack` at the same instant, rest on a live relay
hook having closed the dialog with its verdict or its timeout deny, or
been killed by Claude Code, by then. A verdict `decide` records is
recorded more than 1 s before the window ends, so its hook has more than
3 s to deliver it. The guard can still release before a live hook's
verdict or deny closes the dialog (and, for an open request, `decide`
return `ErrRelayFallenBack` before the deny does) only if both:

- the hook's delivery of its verdict or timeout deny (reading the
  verdict, or noticing its deadline, recording the deny and moving the
  row to `working`; then writing its envelope and exiting) ends more
  than 2 s past its deadline, for example because its store writes each
  wait up to `[store] busy_timeout_ms` (10 s by default) for the store's
  lock, or the process stalls; and
- Claude Code kills the hook more than 1 s after its per-hook timeout.

Keys sent at the pane then can land in Claude's prompt. Nothing stored
shows a hook stuck delivering, so this race is not closed. All of these
instants assume the hook, the store and the caller share one wall clock.

#### Sanctioned recovery of a wedged relayed spawn

At the window's end the relay hook normally denies the request itself
(see "Override moves both boundaries in lockstep" above): its deny closes
the dialog, `decide` returns `ErrAlreadyDecided` (`decision_reason`
`timeout`) and there is nothing to recover. A relayed spawn is wedged
past its window when its relay hook was killed before its own timeout
deny (Claude Code's per-hook timeout came first, or the hook stalled or
died) and can no longer deliver. The operator then
recovers it end-to-end through sanctioned AD surface, with no dedicated
answer-the-dialog verb and **without ever touching raw tmux**:

1. `decide` returns the typed `ErrRelayFallenBack`: the verdict was not
   recorded, the request's record is still open, and delivery is no
   longer possible. `decide` refuses from 1 s before the request's
   window ends and, until 2 s after it, waits for the rest of that time
   before it answers (see "The wait at the window's end" above). If it returns
   `ErrAlreadyDecided` instead, the relay hook (or another caller)
   answered the request and there is nothing to recover. If it returns
   `ErrNoOpenPermissionRequest`, the request's dialog cannot be shown to
   be on screen: do not answer it at the pane.
2. The send-keys guard releases by the same time-based authority, but
   only once no row holds it — 2 s after the last pending request's
   window ends (see the asymmetric-margin note above; while the refused
   request stays open, decided requests no longer hold it). On the
   refused request's account it has released by the time `decide`
   returns `ErrRelayFallenBack`, so the operator sends at once. A
   request of the same spawn recorded after that `decide` and still
   pending may still hold it; `send-keys` then refuses with
   `ErrSendKeysWhileRelayed` naming that request, and the operator
   answers it with `decide`.
3. `send-keys` answers the native permission dialog directly.
4. The action is audited: it appears in the trail as
   `ad.send_keys.called` with `guard_evaluation=released`, so a recovery
   send is distinguishable from an ordinary send and from a guard
   refusal.

This is the in-band recovery surface referenced under `ErrRelayFallenBack`
above; it is now available.

### Known limitations

`decide` reads stored records, which do not show Claude Code's screen.
These cases are not covered:

- **A closed dialog the records still show as up.** While a request's
  record is open, the store holds the agent's moves to `working` and
  writes nothing. So when the request's dialog is answered at the pane
  after its relay hook was killed, or closed by a timeout deny the hook
  returned without recording it, the spawn still reads
  `check_permission`. That lasts until a later hook moves it out (the
  agent's `Stop` or `AskUserQuestion`) or records a later request. Until
  then `decide` still returns `ErrRelayFallenBack` for the request, and
  keys sent at the pane as it advises are typed into Claude's prompt.
  Nothing closes such a record while its spawn lives, so `get` keeps
  listing it as open.
- **A later request not yet recorded.** A PermissionRequest hook moves
  the spawn to `check_permission` just before its relay records the
  request. Take a stale record from the case above whose spawn has since
  left `check_permission` (to `waiting`, say). If `decide` on that stale
  request reads the spawn and its requests after a later request's hook
  moved the spawn back to `check_permission` but before that request is
  recorded, every check passes and `decide` returns
  `ErrRelayFallenBack`. The send-keys guard has released on the stale
  request's account and holds on the later request only once it is
  recorded, so keys sent in that gap can answer the later request's
  dialog. Nothing stored tells this case apart.
- **Stalls on a dialog that is up.** `decide` refuses a request with
  `ErrNoOpenPermissionRequest`, even while its dialog really is on
  screen, in two cases:
  - A subagent's request recorded while an earlier request's dialog is
    on screen looks the same as the agent's next request after that
    dialog closed. `decide` refuses the earlier request, and keeps
    refusing it after the later one is decided.
  - A stale record from the first case is another open request of its
    spawn, so `decide` refuses every later request of that spawn that
    falls back, for as long as the stale record stays open: the rest of
    the spawn's life. Requests answered with `decide` within their
    windows are not affected.

  The request stalls rather than risk a mistyped answer; no error
  advises a way out. A human at the pane answers it.
- **A timeout deny recorded but not returned.** If Claude Code kills the
  relay hook after the hook recorded its timeout deny but before it
  returned that deny, the dialog stays open while the record reads
  decided. `decide` returns `ErrAlreadyDecided`, which advises no pane
  answer, so the request stalls until a human answers it at the pane.
  No keys are typed into Claude's prompt. The hook's poll deadline
  normally comes up to 1 s before Claude Code's kill (see "Override
  moves both boundaries in lockstep" above), so a slow timeout path can
  be cut off this way.
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
