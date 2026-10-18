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
- `ask` — patterns that prompt the operator for permission confirmation.
  `disable_ask_user` controls the agent's question tool; it does not silence
  permission confirmations caused by these entries.

The pattern grammar is Claude Code's; bare tool names match every
invocation of that tool (`AskUserQuestion` denies every AUQ — see the
empirical confirmation in
`reference/permissions-deny-tool-name-research.md`).

## Cursor spawns

A `cursor` call or template with any `allow`, `deny` or `ask` entry is refused
with `ErrPermissionsUnsupported`. This applies at template creation and at
launch after merging call/template input, including saved or hand-edited
templates. Empty arrays and `disable_ask_user` alone are accepted. The lists
are never silently ignored or translated; use Cursor's own permission rules.

Cursor's policy is **question tool off, enforced by denial**. The gateway does not
advertise question support and automatically denies direct questions and
questions arriving as permission requests before any caller or pane answer
path, with `disable_ask_user` either true or false. Malformed or ambiguous
authorization cannot enable a question. Cursor never waits in `ask_user` for
an answer through agent-director.

Validated Cursor permission and blocking plan requests use `check_permission`,
`get-permission`, the permission fields of `get`/`list`, and `decide`. Each has
its own `request_token`; answering one never answers another. Requests retain
their admitted conversation. An independent AD child's requests belong to that
child's row. Parent association grants no approval authority, and work or a
life change on the parent cannot answer the child's request or release its hold.
[Conversation rotation](usage.md#fresh-cursor-use)
does not transfer old choices or request identities to the fresh conversation.
Resume does not restore old choices or make replayed permission, plan or question
requests answerable. Load replay supplies no execution proof or readiness and
cannot release independent durable holds; question denial still applies with
either `disable_ask_user` value.
Cancellation, a new session id and turn completion cannot prove unknown-origin
requests gone or release independent permission or plan holds. `tool_name` is
the supplied action title or plan name, `tool_use_id` is its actual
`toolCallId`, and `tool_input` retains the complete supplied params JSON
byte for byte, including options and absent versus null fields. Internal
conversation context is not added to that input. A plan retains its supplied
name, overview, plan and optional fields; it has no invented option list or
session field. Accept and reject use the supported plan outcomes.

`decide --decision allow` selects only allow-once, or accepts this plan once;
`deny` selects rejection. It never grants a persistent rule. An explicitly
offered persistent permission choice can be selected deliberately at the pane;
it can affect later requests using that Cursor policy. See
[Cursor group policy limits](multi-account.md#cursor-configuration-and-data-directories)
for the shared-policy consequences. Read the actual request and displayed choices.
Unknown, malformed or ambiguous authorization shapes fail closed visibly within a bound, rather than becoming answerable
permissions. This does not promise approval schemas for operations Cursor
runs without asking.

With relay `on`, the decision window is `relay.timeout_seconds` from request
admission. `confirm_by` is fixed at admission to that deadline plus a
four-second settlement reserve. Per-request hook process fields and
`hook_alive` are null: the long-lived gateway is not a per-request hook.
Caller restarts do not extend the bound. `delivered` means durable relay
acknowledgement, not Cursor receipt, execution or dialog closure. For missing
acknowledgement, uncertain delivery and recovery, follow the
[caller contract](caller-contract.md#permission-relay).

A deliberate pane choice competes with an API decision through the same
request arbitration. Use the displayed request-specific key, once, without
Enter; keys differ across simultaneous menus and are not guessed upstream
option numbers. With relay `off`, requests immediately read `fallen_back`,
connected pane choices are available and `decide` is refused. Native choices
have a separate 30-minute lifetime: from admission with relay off, or from
relay settlement with relay on. After settlement, a fresh deliberate choice
can resolve a still-open connected native request, even with an unacknowledged
recorded decision. The [caller contract](caller-contract.md#permission-relay)
owns fallback actions, expired choices, uncertain output and disconnects.

Direct supported question cancellation rejects that question without resolving
independent genuine requests. Unsupported shapes can require bounded session
cancellation; this does not prove existing dialogs gone. Main/child turn
completion, interruption and plan acceptance do not release unknown-origin
requests. Validated proof for the exact matching shell or MCP invocation requires
the current launch and conversation authority. Shell proof requires a completed
or failed update with an integer exit code and non-null string stdout/stderr.
An admitted MCP request requires a completed tool update whose `rawOutput`
contains exactly `{"success":true}` or `{"error":"Tool execution error"}`.
The exact failed invocation proves only that its permission dialog is gone,
using `proven_gone_how` `tool_ran`; it does not establish that the underlying
server action ran or succeeded. It preserves decision, reason, decision time,
delivery and pane-answer fields, without inferring an allow or pane answer.
Status alone and other failed or ambiguous MCP output supply no proof.
Only this validated invocation proof, or the owning spawn ending, releases their hold.
Other execution evidence remains conservative. A terminal spawn close ends the
hold without establishing per-request proof: gateway or runtime death leaves `proven_gone_at` and
`proven_gone_how` unchanged, including on requests already reading `delivered`.
Existing validated execution proof is preserved. The shared [dialog hold](#the-dialog-hold) defines the
proof fields and relay-on prompt/pause guards; relay-off requests still block
ordinary prompt admission while awaiting their native answer. Accepted
[queued input](usage.md#fresh-cursor-use) waits for every durable decision
hold to resolve before dispatch; a turn ending or a local request disappearing
does not release it. Late exact-request invocation proof releases only that
request's hold. Pending-row, relay, pane-hash and pause safeguards still apply.
The hook flow,
hook envelope and hook timing below describe Claude and Codex; Cursor uses
the request lifetime described here.

## Codex spawns

The lists on this page apply to spawns whose agent is `claude`. A `codex`
spawn or template that carries any `allow`, `deny` or `ask` entry is refused
with `ErrPermissionsUnsupported` before anything is written (see the
[caller contract](caller-contract.md#spawn-and-template-input-refusals)). Set
a Codex agent's permission rules in the `rules/` folder of the group's Codex
folder, and use Codex's own permission controls. The `disable_ask_user`
setting below is not such an entry and never makes a spawn refused.

Codex's own permission controls, such as its approval and sandbox options, and
its hook-trust bypass, reach Codex unchanged through the spawn's arguments.
Every Codex launch also turns on approval of input to a running command (the
`write_stdin_approval` feature), so text sent to a command already running asks
as any command does, except that input to a command running inside Codex's
sandbox is never relayed (Codex asks nothing for it). A caller's
`--disable write_stdin_approval` turns it off. "Always allow" at a Codex pane adds a rule to the whole group's
Codex folder, and folders are not a security boundary against agents that can
call agent-director (the dialog's keys are in
[Answering a Codex approval at the pane](#answering-a-codex-approval-at-the-pane)).

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
| per-Spawn (via `deny`) | `["Bash(npm publish)"]` |

Effective `deny` for the Spawn:

```
["Bash(rm -rf /)", "WebFetch(http://example.com/*)", "Bash(npm publish)"]
```

A Spawn launched with `deny` `["Bash(npm publish)"]`
(`--deny "Bash(npm publish)"` on the CLI) adds that single entry. The operator's pre-existing rules continue to apply.

## `disable_ask_user` config

`config.toml`:

```toml
[defaults]
disable_ask_user = true
```

The setting controls Claude and Codex. For Cursor, either value retains
[question denial](#cursor-spawns).

**Claude agent.** When true, every `spawn` of a Claude agent adds
`"AskUserQuestion"` to the
synthesized `permissions.deny` array. The string `"AskUserQuestion"` is
a bare tool name — Claude Code's matcher treats it as "deny every use
of the AUQ tool" *and* removes the tool from the deferred-tool registry
(the model never sees its schema). Confirmed in
`reference/permissions-deny-tool-name-research.md`.

Recommended for orchestrator-driven setups where every agent should
make its own decisions rather than prompting a human. The deny is
additive — caller-supplied `deny` entries still concatenate, and the
user / project tiers still apply.

To re-enable AUQ for a specific Spawn while keeping the global config
off, either change the setting in the config (affects subsequent launches
that use it), or run that Spawn under the separate home and tmux server
described [below](#one-spawn-under-another-home-or-store).

**Codex agent.** Every launch of a Codex agent (`spawn`, a reuse and
`resume`) is affected as follows.

- With the setting on, Codex's question tool is off for the launch, whatever
  the user's, the group's, the profile's or the project's Codex config says,
  and its asynchronous question tool is denied as well, so a call to it is
  blocked with a reason the agent reads. The agent has to put its question in
  its reply.
- At `spawn`, an argument that turns the tool back on, or that sets a Codex
  config key at, above or under the tool's keys, is refused with
  `ErrSpawnDeniedFlag` (see the
  [caller contract](caller-contract.md#spawn-and-template-input-refusals)).
- At `resume`, the setting as it stands now wins over the stored arguments, and
  `resume` refuses nothing for them. A row spawned with the setting off and
  resumed with it on gets the tool off; the reverse gets it as Codex's config
  leaves it.
- With the setting off, agent-director adds nothing: the tool is as the user's,
  the group's and the project's config leave it. A question that waits does not
  move the row: no Codex tool reads `ask_user`, so the row reads `working`
  while it waits.

**A question tool turned on in a group's config.** With the setting off, a
group's Codex config can turn the question tool on, and an agent that uses it
sits at its question with the row reading `working`. Turn the setting on, or
keep the tool off in that group's config.

### One Spawn under another `HOME` or store

An alternate `HOME`, the global `--home` flag, `spawn --store-path`, and
the TypeScript client's `home` and `storePath` options (which pass
`--home` and `--store-path`) do not by themselves give one Spawn another
config or store. All of them must name the store of the tmux server the
spawn reaches, or the agent's hooks write to a different store. The
hooks load agent-director's config and store from the pane's `HOME`,
which on a running tmux server is the server's `HOME`, not the
spawner's. The spawn picks the server by `$TMUX`, else by `TMUX_TMPDIR`
(or `/tmp`) plus the uid, never by `HOME` (details in
[Reserved `HOME` in `extra_env`](architecture.md#reserved-home-in-extra_env-bnas)).

When the two stores differ, the agent's hook events go to the other
store and the row stays `pending`, with no error to the caller:
`find-missing` notes it `unreported` while the agent runs and marks it
`missing` once the agent's process is gone. If no server was running, a
spawn with an alternate `HOME` (or `--home`) starts the shared server
with that `HOME`, and every later Spawn on that server sends its hook
events to the alternate store.

The safe way is a separate tmux server for the alternate `HOME`: run the
spawn with that `HOME`, a `TMUX_TMPDIR` of its own that already exists,
no `TMUX` and no `--store-path`:

```sh
mkdir -p /path/to/alt-tmux
env -u TMUX HOME=/path/to/alt-home TMUX_TMPDIR=/path/to/alt-tmux agent-director spawn --cwd <dir> --agent claude
```

The spawn starts a new server whose `HOME` is the alternate one, so the
agent's hooks use the alternate store, and the agent's row lives in that
second store. Put that Spawn's config in
`/path/to/alt-home/.config/agent-director/config.toml`. Run every later call on
that store the same way. Manage that store's agents only with that
store's own agent-director (see
[A session of another agent-director store](operator-actions.md#a-session-of-another-agent-director-store)).
Claude Code in the new pane runs under the alternate `HOME` too, so it
reads its user files there: the user tier above is the alternate
`HOME`'s `~/.claude/settings.json`, and the alternate `HOME` needs
Claude Code's own login (`~/.claude/.credentials.json`, or an auth
variable passed through `extra_env`, see [Multi-Account](multi-account.md))
and first-run setup (`~/.claude.json`, which pre-trust also needs).
Without them the agent waits at a login or onboarding screen, where no
hook fires, and its row stays `pending`.

## `--dangerously-skip-permissions`

agent-director **does not strip** this flag. A caller passing
`--dangerously-skip-permissions` through `agent_args` bypasses Claude
Code's permission engine entirely — the per-Spawn `permissions` block
becomes irrelevant for that Spawn. This is intentional per the PRD
(the supervisor exposes the same trust boundary as the operator's own
shell).

Operators who want to restrict callers from using this flag should not
let untrusted parties spawn Claude instances through agent-director.

## Relay mode

A Spawn launched with `relay_mode` `on` (`--relay-mode=on` on the CLI)
hands every PermissionRequest
to the orchestrator instead of the agent's native consent dialog (Claude
Code's or Codex's). Useful when an unattended supervisor needs to decide
allow/deny based on policy rather than a human at the keyboard. The hook flow,
envelope and hook deadlines below describe Claude and Codex; shared decision,
delivery and pane fields also serve [Cursor requests](#cursor-spawns).
Differences on a Codex row are in [The relay on Codex rows](#the-relay-on-codex-rows).

Only the Spawn's own agent is relayed. A PermissionRequest whose
hook parent is not the row's recorded pane process (a nested agent, a
teammate pane), or one on a row that records no pane yet, opens no
request and returns no decision, so that process's agent asks as
it would with no relay; the hook writes one `ad.hook.ignored` trail
record (see [hooks.md](hooks.md#only-the-rows-own-agent-moves-the-row)).

### Flow

1. The agent fires the PermissionRequest hook with the tool name + tool
   input.
2. The hook uses the relay mode fixed at launch in
   `AGENT_DIRECTOR_RELAY_MODE`.
3. If the env var is `on`, the relay hook:
   - Mints a per-request UUIDv4 `request_token` and, in one store
     transaction, moves the Spawn to `check_permission` and records the
     open request in `permission_requests`, keyed by the composite
     `(instance_id, request_token)`: both are written or neither
     is. The request carries the hook's own process identity (pid, start
     time and pid namespace) and the payload's `tool_use_id`. Concurrent
     requests for the same Spawn each get their own row and are decided
     independently. Oldest *closed* rows (those no longer awaiting an
     answer) are evicted in the same transaction when the table exceeds
     `relay.permission_request_cap` (default 1000; `0` disables
     eviction), but only once the agent's own hooks have proven the request's dialog
     gone (`proven_gone_at` set) or its spawn is `ended` or `missing`: a
     closed request not yet proven gone on a live spawn still holds plain
     `send-keys` (see "The dialog hold" below), so it is kept, and the
     table can stay above the cap by the number of such requests. A
     spawn's newest request is kept as well while that spawn has a
     request that still awaits an answer, because `decide` relies on it
     for a request recorded by an older binary (see "Requests recorded by
     an older binary" below), so the table can stay above the cap by
     one more closed row per such spawn.
   - Polls its own row at
     `max(50ms, relay.poll_base_ms + uniform(0, relay.poll_jitter_ms))`
     intervals, until 3 s before the agent ends it (see "Relay timeout
     default and override" below).
   - On a decided row → confirms the verdict in the store first (it
     records `delivered_at`), then writes the decision envelope to stdout.
   - At the poll's end with no verdict → records the `timeout` deny and
     its confirmation in one statement, then writes a deny envelope.
   - On a failure → see "Fail-closed boundary" below: a deny before the
     request is recorded, no answer after.
4. The orchestrator calls `agent-director decide --instance-id
   <id> --request-token <token> --decision allow|deny [--max-wait-ms N]`
   to record the verdict. The token comes from the Spawn's
   `permission_requests` in `get` or `list` output.
5. The hook's polling loop sees the verdict on its next read, confirms it
   and writes the envelope. `decide` waits at most 1 s for that
   confirmation and reports it (see "Delivery" below).
6. A relay hook that ends with no confirmed verdict (it died, or the agent
   ended it) leaves its request fallen back: no answer from the relay
   reached the agent, `decide` refuses it with `ErrRelayFallenBack`, and
   only an answer at the pane, or a record of one, closes it (see
   "Answering at the pane" below).
7. From the request's first write until the agent's own hooks prove its
   dialog gone (its tool ran, on a Claude row; the agent's turn ended; or
   the agent ended), a plain
   `send-keys` without a pane hash to the spawn is refused with
   `ErrDialogMaybeOpen`, whatever agent-director's records say of the
   request (see "The dialog hold" below).

### Envelope wire format

SRD §6.3 / Claude Code 2.x nested shape (a Codex agent gets the same
envelope, unchanged):

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
PermissionRequest process.

Once the request is recorded, the hook writes only an answer it has first
confirmed in the store, and every failure ends with **no answer**: the hook
exits 0 with empty stdout, the agent asks in its own permission prompt,
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
| stdin payload read failure | silent exit 0 (event name unknowable) |
| Classify failure (unparseable payload) | silent exit 0 (event name unknowable) |
| The first write (the request and `check_permission`) fails, or its wait for the store's write lock reaches 2 s before the agent ends the hook | deny envelope; nothing recorded |
| The poll reaches its end (3 s before the agent ends the hook) | `timeout` deny recorded and confirmed, then a deny envelope |
| The hook's parent process is no longer the agent that started it | no answer |
| The confirmation or the timeout deny fails, or cannot commit by 2 s before the agent ends the hook | no answer |
| `ctx.Done()` during poll | no answer |
| Row preempted (`sql.ErrNoRows` during poll) | no answer |
| Read-retry budget exhausted (5 consecutive read errors) | no answer |

#### Relay timeout default and override

The relay window (`relay.timeout_seconds`) defaults to **86400 seconds (1 day)**,
long enough for human-paced approval flows — a human's approval that arrives
after a meeting or overnight still lands inside the window. Operators who want a
tighter bound can override it in `~/.config/agent-director/config.toml`:

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
the relay hook about 1 ms after it starts. The range applies to all three
agents; Cursor enforces its own request window as described in [Cursor spawns](#cursor-spawns).
A Codex launch lists the window as given: nothing here promises how Codex
behaves live over the whole range.

**How the window is enforced.** The window is real because agent-director
emits it into the per-Spawn synthesized settings (a Claude launch) or onto
Codex's command line (a Codex launch; see
[the Codex entry set](hooks.md#the-codex-entry-set)). Each PermissionRequest and
PreToolUse hook entry carries an explicit per-hook `timeout` field — in a
Claude launch's settings placed on the inner command object, sibling to
`type`/`command` — set to `relay.timeout_seconds`. Without that field
Claude Code kills any hook at its own 600-second default and discards its
output, so a late decision would be silently voided. The PermissionRequest
entry also passes the same value to the hook as its `--timeout` argument, so
the relay hook knows the instant the agent will end it: its own start plus
the window.

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
and at 2 s or less it gives no answer, so the agent asks in its own
permission prompt and the request falls back.

**One value for the hook and the agent.** `relay.timeout_seconds` is a
single value read through one accessor, and `spawn` writes it both as
the agent's per-hook `timeout` and as the hook's `--timeout`, so for a
given agent the hook's deadlines and the agent's kill can never
disagree. That holds because the config load bounds the value: inside the
range, Claude Code applies the per-hook `timeout` as written. An agent
keeps the value in force when it was launched (`spawn` or `resume`); a
changed value applies to agents launched after it. The hook counts from
its own start, the instant the agent arms its kill, so at the window's
end the hook normally records and confirms its timeout deny, writes it and
exits on its own, rather than being killed mid-flight by the agent.

The fail-closed boundary is scoped to PermissionRequest events. A
non-PermissionRequest event with `RELAY_MODE=on` (e.g. SessionStart)
still follows the regular state-tracking fail-open path and emits no deny
envelope. A deny emitted on another event can reach an in-flight permission
tool through descriptor routing; it is not harmless.

**Structural caveat.** Fail-closed requires the `agent-director`
binary to actually run. If the agent can't invoke it at all —
binary missing, PATH not set, settings JSON unparseable — no hook
runs and the agent decides the request through its native
permission dialog alone. From the policy view that is a hole the
operator must close at install time.

**The native dialog is not a hook-death signal.** When the relay
hook *is* running, Claude Code shows its native permission dialog
concurrently, as racing UI displayed alongside the live hook — not
as a fallback and not as evidence the hook has stopped. A decision
envelope arriving while the hook runs dismisses that dialog (Codex draws
no dialog while a relay hook runs; see
[The relay on Codex rows](#the-relay-on-codex-rows)). Whether a
request can still be answered through the relay is judged from the relay
hook's process and the request's record (see "Delivery" below); the
dialog's presence or absence in the TUI carries no information about hook
liveness, and agent-director never reads it.

### The relay on Codex rows

A `codex` row is relayed through the same hook, store records, `decide`,
delivery facts and pane answers as a `claude` row. A Codex request is
recorded with the Codex tool names below, and its hold differs in the ways
the last subsection lists. A Codex agent whose relay is off moves to
`check_permission` for each request and is answered at the pane.

#### What is relayed

| What Codex asks about | `tool_name` | `tool_input` |
|---|---|---|
| A shell command outside the sandbox, or one the sandbox refused and Codex asks to run again outside it | `Bash` | `command`, and `description`: the model's reason for the call |
| A file edit outside the writable roots (an edit inside them asks nothing) | `apply_patch` | `command`: the whole patch text |
| Input to a command running outside the sandbox | `write_stdin` | the input (`chars`), the command's session and the call's other fields |
| An MCP tool call | `mcp__<server>__<tool>` | the call's arguments |
| Network access through Codex's managed proxy | `Bash` | `command`: the command that tried; `description`: `network-access <scheme>://<host>:<port>` |

agent-director's own MCP tools are relayed as `mcp__<server>__<tool>`
approvals, with `<server>` the name the agent's Codex config gives the
server: Codex asks about them by default, so an agent's call to agent-director
over MCP is a request the orchestrator decides.

- A Codex request's `tool_use_id` is always `null`, and a field Codex does
  not send is `null` or empty.
- Codex's approval and sandbox settings (passed through the spawn's
  arguments; see [Codex spawns](#codex-spawns)) decide what asks. The relay
  sees only a call Codex would ask a person about, never one it runs inside
  its sandbox or refuses outright, and never input to a command running
  inside the sandbox (see [Codex spawns](#codex-spawns)).
- Several approvals can fire their relay hooks in the same second while
  Codex shows one dialog at a time, and the dialogs can come in another
  order than the requests were recorded. Before a pane answer, match the
  dialog on the pane to the request by its command or patch, not by the order
  the requests were recorded in.
- The envelope is the one above and reaches Codex unchanged.

#### Answering a Codex approval at the pane

A pane answer is the one under [Answering at the pane](#answering-at-the-pane);
only the keys and tool names are Codex's. Codex's dialog lists its keys in
parentheses. For a shell command, `y` runs it, `p` runs it and does not ask
again for commands that start the same, and `Escape` refuses; for a file edit
the second choice is `a`. To allow one command:

```sh
agent-director read-pane --instance-id <id>
agent-director send-keys --instance-id <id> --request-token <T> \
    --as allow --key y --expect-pane-sha256 <H>
```

Choosing "don't ask again" (`p` or `a`) is "Always allow", answered with
`--as allow` too; it adds a rule for the whole group (see
[Codex spawns](#codex-spawns)). `Escape` at a main-agent dialog also
interrupts the turn (see [How the hold differs on Codex
rows](#how-the-hold-differs-on-codex-rows)).

#### Startup files that print

A zsh `.zshenv`, or a file named by a `BASH_ENV` the caller set, that prints
to stdout puts that text ahead of the relay hook's verdict. Codex then
ignores the verdict and draws its own dialog, and the command waits for a
person, while agent-director records the verdict as delivered. Text on stderr
is harmless. agent-director launches Codex with `BASH_ENV` empty. What the
text does to the other hooks is in
[The shell between Codex and the hook](hooks.md#the-shell-between-codex-and-the-hook).
To check for it and to fix it, see
[A Codex agent shows its own approval dialog after a relayed verdict](operator-actions.md#a-codex-agent-shows-its-own-approval-dialog-after-a-relayed-verdict).

#### How the hold differs on Codex rows

[The dialog hold](#the-dialog-hold) applies to a Codex row, with these
differences:

- **Interrupt ends the turn.** `Escape` during a main-agent turn makes Codex
  send an Interrupt with no `agent_id`, which proves every eligible main-agent
  request recorded before its second gone (`proven_gone_how` `turn_end`), as
  a `Stop` does; it also moves the row to `waiting`. An Interrupt that
  carries an `agent_id` proves nothing; Codex sends none. This is safe only
  while Codex never reports a subagent's interrupt as the main agent's.
- **No tool-ran proof.** Codex requests carry no `tool_use_id`, and a
  Codex `PostToolUse` neither proves a request gone nor closes one. So after
  a main-agent allow the hold lasts until the turn ends (its `Stop` or
  Interrupt), not until the tool has run.
- **A subagent's approval holds until the agent ends.** A subagent's request,
  allowed or denied, is proven only by the end of the agent, so it holds a
  plain `send-keys` without a pane hash, and `pause`, until then.
- **A fallen-back allow never closes on its own.** No `PostToolUse` close
  exists, so a fallen-back request closes only through `record-pane-answer`
  or a pane answer (see [A request answered outside agent-director](#a-request-answered-outside-agent-director)).
- **Escape at a fallen-back main-agent dialog.** When a fallen-back
  main-agent request is answered with `Escape` (a pane answer, `--as deny
  --key Escape`), the Interrupt leaves the row `waiting` and the request
  proven gone, so a plain `send-keys` then works.
- **One end signal per turn.** Codex has no idle-prompt Notification, so
  the turn's `Stop` or Interrupt is the only proof by turn end. If that hook
  is lost, a plain `send-keys` without a pane hash stays held until someone
  looks and sends with the pane's hash, or the agent ends.
- **Escape while a relay hook polls.** When the request of a main agent is
  undecided and the agent's relay hook is polling, `Escape` makes Codex end
  the hook at once: the command does not run and Codex draws no dialog for
  it later. The request falls back, so `decide` refuses it
  (`ErrRelayFallenBack`) and a plain `send-keys` stays refused until it is
  closed. The Interrupt proves it gone, unless it was recorded in the same
  second the Interrupt hook started (see "A request recorded in the hook's
  start second" under [Known limitations](#known-limitations)), but does not
  close it: close it with `record-pane-answer --as deny` and the hash of a
  `read-pane` you looked at, once the hook has been gone 2 s.
- **A row left in `check_permission`.** `find-missing` repairs a Codex row
  to `working`, never to `waiting` (see
  [A relay row left in check_permission](hooks.md#a-relay-row-left-in-check_permission)).

`AGENT_DIRECTOR_RELAY_MODE` is fixed at launch. With relay mode `on`,
an unreachable store, schema mismatch or malformed configuration causes
a fail-closed deny.

### Race-freeness of `decide`

Only the first accepted verdict is recorded. Concurrent later calls receive
`ErrAlreadyDecided`. A request that has fallen back is refused with
`ErrRelayFallenBack`; a closed request cannot accept a verdict. Success
reports delivery separately from the recorded decision (see "Delivery"
below). For recovery, follow [Permission relay](caller-contract.md#permission-relay).

Depending on the request's current state, `decide` returns one of
these outcomes:

- No row at all → `ErrNoOpenPermissionRequest`.
- Request has a recorded decision → `ErrAlreadyDecided` (including a
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
- A request recorded by an older binary, still open past its relay
  window → `ErrRelayFallenBack`, `ErrAlreadyDecided` or
  `ErrNoOpenPermissionRequest` (see "Requests recorded by an older
  binary" below).

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

`decide` records no verdict if the spawn finishes before the decision is
recorded. `send-keys` refuses an `ended` or
`missing` spawn before checking its permission requests
(`ErrSpawnNotInteractive`).

A spawn's open requests close together with these changes:

- `find-missing`'s mark of the spawn `missing`;
- the agent's terminal SessionEnd, which moves the spawn to `ended`;
- the move of a Codex spawn to `ended` once its agent is gone with an end
  report, made by `find-missing`, or by `pause` before its keys or while it
  waits (see [How a Codex row ends](hooks.md#how-a-codex-row-ends));
- `resume`'s move of the finished spawn to `pending`, as a backstop for a
  request an older binary left open on the finished spawn.

For non-Cursor spawns, the same close proves every request gone, closed now or
before (`proven_gone_how` `agent_gone`; see "The dialog hold" below).
Cursor requests close without changing their proof fields; see [Cursor spawns](#cursor-spawns).

Every request that still awaits an answer is closed (`closed_at` is set).
The close also includes a decided request without a relay confirmation or
completed pane answer, even when recorded by an older binary:

- an undecided request gets `decision` `deny` with `decision_reason`
  `find_missing` (the mark) or `ended` (the other three), so a relay hook
  still polling for it reads that deny (and returns it, once confirmed, if
  its agent is still the one that started it). `decide` on the
  mark's deny returns `ErrAlreadyDecided`, and on the `ended` deny
  `ErrNoOpenPermissionRequest`;
- a decided request whose relay hook had not confirmed the verdict keeps
  it (a hook whose agent still runs may still confirm and return
  it); `decide` on it returns `ErrNoOpenPermissionRequest`, whose message
  says the request is closed (its spawn ended, or `find-missing` marked it
  missing, before its relay hook delivered a verdict), nothing was
  recorded, and not to answer it at the pane (`ErrAlreadyDecided` once its
  hook has confirmed it).

Each deny is written to the trail as one `ad.row_mutation.committed`
(`writer_process` `find_missing`, `hook` or `resume`). The mark's deny and the
Codex move's `ended` deny both carry `find_missing`, so that writer can come
from `pause`.

A closed request stays closed after a `resume`: it no longer awaits an
answer, so `get` and `list` do not show it, it no longer holds the spawn in
`check_permission` or off `working`. On non-Cursor spawns, it is also proven
gone, so it never refuses `send-keys`.
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
`proven_gone_how` are written by the agent's own hooks or validated Cursor
invocation proof, by the close of its spawn's requests and, for a spawn already
finished, by the upgrade to this release, never by a reader (see "The dialog hold" below):

The `delivery` and `confirm_by` descriptions below apply to requests
with a settle instant. Requests recorded by an older binary retain the
[legacy delivery rules](#requests-recorded-by-an-older-binary), even when closed.

| Field | Meaning |
|---|---|
| `delivery` | `delivered`: the relay durably acknowledged a verdict before output, without proving receipt. `not_confirmed`: no acknowledgement yet and the request relay may still answer. `fallen_back`: no confirmation, and the hook is gone, or cannot be checked and `confirm_by` has passed: the relay is no longer confirmed to answer. While `pane_answer` is `none` or `intent` the request awaits an answer unless valid execution proof or owning-spawn closure resolves it; a pane answer or `record-pane-answer` can also close it; with `sent`, `outside` or `tool_ran` it is closed and stays `fallen_back`. |
| `confirm_by` | The request's fixed settle instant: hook end plus 2 s for Claude/Codex, or the Cursor decision deadline plus 4 s. `not_confirmed` never lasts past it. |
| `hook_alive` | `true` or `false` by the check below; `null` when it cannot tell. |
| `hook_gone_at` | When a reader first found the request fallen back; `null` until then. |
| `attempted_decision`, `attempted_at` | The verdict a `decide` refused with `ErrRelayFallenBack` tried to record, and when; shown, never acted on. |
| `tool_use_id` | The `tool_use_id` of the agent's hook input, or Cursor's supplied `toolCallId`; `null` when it gave none (always, on a Codex row). |
| `pane_answer` | `none`: no pane answer recorded through agent-director (something outside it, such as a person at tmux, may still have answered). `intent`: a pane answer through `send-keys` was started, and whether its key was typed is unknown. `sent`: its key was sent or a Cursor native choice was recorded; neither establishes ACP delivery, receipt or proof. `outside`: a caller recorded it answered outside agent-director (`record-pane-answer`). `tool_ran`: Claude Code reported its tool ran, or the Cursor gateway validated matching execution; see [Cursor spawns](#cursor-spawns). `sent`, `outside` and `tool_ran` close the request. See "Answering at the pane" below. |
| `pane_as` | The verdict a pane answer claims: `allow`, `deny`, or `unknown` (a record whose caller did not see the answer); `null` when none was recorded. Public pane operations store the caller's claim without screen interpretation or verdict verification. Cursor additionally requires a sent claim to match the actual selected option before answering ACP; native choices derive the verdict from that option. An outside claim never supplies ACP approval authority or proof. |
| `proven_gone_at` | When the agent's own hooks or validated Cursor invocation proof established the request's dialog gone; `null` until then. agent-director's own records (`delivered`, a pane answer, `record-pane-answer`) are no proof. While it is `null` on a live relay-on spawn, a plain `send-keys` without a pane hash is refused with `ErrDialogMaybeOpen`. |
| `proven_gone_how` | `tool_ran`: a Claude `PostToolUse`/`PostToolUseFailure` carried its `tool_use_id`, or Cursor supplied validated matching invocation proof (never on a Codex row); the exact failed MCP invocation proves dialog closure only, as defined in [Cursor spawns](#cursor-spawns). `turn_end`: an eligible main request was written before the main turn ended; a missing `agent_id` alone does not establish eligibility. See [The dialog hold](#the-dialog-hold). `agent_gone`: a non-Cursor spawn was marked `missing`, ended or resumed, or was already `ended` or `missing` when the store was upgraded. Cursor terminal closes leave proof unchanged. `null` until proven. |
| `unproven_since` | For a request not proven gone that no longer awaits an answer in agent-director's records: when its record stopped awaiting one (its relay hook's ack, its pane answer, its `record-pane-answer`, or the close of its spawn's requests; for a request recorded by an older binary, its recorded verdict). `null` while it still awaits an answer, and once it is proven gone. |

**`decision` is not the outcome.** `decision` is the verdict recorded,
set before delivery is known: a request can read `decision` `allow` with
`delivery` `fallen_back`. Read `delivery` for the outcome, and
`pane_answer` for a request closed at the pane, whose `decision` is the
caller's claim (`null` for a record claiming `unknown`).

**`decision_reason`** annotates the verdict: `operator` (API deny),
`timeout` (relay timeout deny), `find_missing`/`ended` (owning-spawn closure),
`pane` (public `send-keys` claim or native Cursor choice), `pane_outside`
(outside claim), or `tool_ran` (tool execution). API allow initially has no
reason; later validated Cursor execution can fill it with `tool_ran` without
replacing an earlier verdict or non-null reason. Earlier deny/operator,
timeout, pane and outside reasons remain intact. Failed MCP invocation proof
does not fill or change this reason. An outside `unknown` record
initially has NULL decision and `pane_outside`. Read `decision` for the verdict
and `delivery`, `pane_answer` and proof fields for the outcome; a reason is
not an immutable writer identity. Treat unknown reasons as opaque annotations.

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

- Reads, the verdict write, cleanup and reporting share the original
  deadline. Expiry prevents starting new SQL; already entered database work
  can retain its connection while the caller returns. Zero permits
  immediately available work with no lock retries.
  Exact bounds, error meanings and recovery are in the
  [caller contract](caller-contract.md#permission-relay).
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
agent), and not closed with the spawn. A row can read `waiting`, `working`
or `ask_user` while one of its requests still awaits an answer, so a caller
follows each request it tracks with `get-permission`, not only rows in
`check_permission`. `get` (not `list`) also carries `unproven_requests`:
every request of the row not proven gone, in any state but `ended` and
`missing`, with the same fields, including those agent-director's records
read closed (see "The dialog hold" below).

**The row after the last answer.** While any of its requests still awaits
an answer and is not proven gone (see "The dialog hold" below), the
agent's moves to `working` are held, so the row stays in
`check_permission` while the agent works. Claude's `AskUserQuestion` can
move the row to `ask_user`; Claude and Codex `Stop` can move it to
`waiting` with a child request still open. Codex tool events target `working`
and remain subject to the request hold. Cursor working, Stop and Interrupt
reports preserve a genuine unanswered, unproven `check_permission` hold,
including a request recorded while the state update is in flight. A report
can refresh activity without declaring readiness; ownership, current session
and runtime checks still apply. Cursor admission and request lifetimes are
described in [Cursor spawns](#cursor-spawns). Once no request holds the row,
it moves on with the agent's next hook; when that hook is lost, the idle-prompt
Notification (Claude rows only) or a scheduled `find-missing` moves it to
`waiting` or `working` (see
[hooks.md](hooks.md#a-relay-row-left-in-check_permission)).
The relay hook's timeout deny itself leaves the row's state as it is. A
request answered at the pane outside agent-director still awaits an
answer, since nothing recorded the answer, but it stops holding the row
once the agent's hooks prove it gone: the main agent's `Stop` or idle-prompt
Notification after it, or (Claude rows) its tool's `PostToolUse`; on a Codex
row, its `Stop` or Interrupt. Main-turn proof applies only to the eligible
requests described in [The dialog hold](#the-dialog-hold). The request stays open
until `record-pane-answer` closes it, and once the row has left
`check_permission`, `get` and `list` no longer list it; `get-permission`
does. A subagent's request is not proven by the main agent's turn end, so
answered that way it holds the row's moves to `working` until
`record-pane-answer`, its tool's `PostToolUse` (Claude rows) or the spawn's
end.

### Requests recorded by an older binary

Automatic recovery of decided requests carrying `agent_gone` proof and
retained holds before SessionStart are described in the
[caller contract](caller-contract.md#permission-relay). Their reported
delivery still follows the rules below.

A request recorded by the relay hook of an older binary has no hook
identity, no confirmation and no settle instant. It is judged by time, as
that binary judged it:

- `confirm_by` is its `requested_at` plus the relay window plus 2 s, and
  `hook_alive` is `null`. Until `confirm_by`, `delivery` is
  `not_confirmed`; after it, `delivered` when a verdict other than a
  close's deny (`find_missing` or `ended`) is recorded, else `fallen_back`.
  Such a request awaits an answer while it is undecided and not closed.
- Fallen back and still undecided, such a request is open and fallen back
  like any other: a plain `send-keys` to its spawn, and `pause`, are
  refused with `ErrRelayFallenBack` until it is closed, by a pane answer, by
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
- At upgrade, every such request of a live spawn starts with no proof that
  its dialog is gone (`proven_gone_at` `null`), so it holds a plain
  `send-keys` without a pane hash, and `pause`, to its spawn
  (`ErrDialogMaybeOpen`; see "The dialog hold" below) until the main
  agent's next `Stop` or idle-prompt Notification, or the spawn's end.
  The upgrade itself proves
  gone (`agent_gone`) every request of a spawn already `ended` or
  `missing`. `record-pane-answer` does not lift that hold. An
  agent already idle at its prompt when you upgrade, its idle-prompt
  Notification already sent, sends neither until a new turn starts, so its
  first plain `send-keys` needs a person or an LLM to read the pane and
  send with that read's `pane_sha256`, and so does stopping it (see
  "`pause`" under "Send-keys interaction" below).
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
`waiting`, `working` or `ask_user` while one of its requests is still
open), `send-keys`
first reads every permission request of the spawn, judging each relay hook
as the readers do (see "How a reader judges the relay hook" above). It
refuses in this order, sending nothing:

| Call | Refused while | Error |
|---|---|---|
| any | a relay hook of the spawn may still answer its request: its process runs, or it cannot be checked and the request is open before its `confirm_by` | `ErrSendKeysWhileRelayed` |
| plain (no `request_token`) | an open request of the spawn is fallen back with `pane_answer` `none` or `intent` | `ErrRelayFallenBack`, with `err_details`, naming the oldest such request |
| plain, without `expect_pane_sha256` | a request of the spawn is not proven gone (`proven_gone_at` `null`), however agent-director's records say it closed | `ErrDialogMaybeOpen`, with `err_details`, naming the oldest such request (see "The dialog hold" below) |
| pane answer (`request_token` T) | the spawn has no request T | `ErrNoOpenPermissionRequest` |
| pane answer | T is not fallen back: confirmed by its relay hook, closed with its spawn, or already closed at the pane | `ErrAlreadyDecided` or `ErrNoOpenPermissionRequest` |
| pane answer | another pane answer to T is still being sent | `ErrPaneAnswerInProgress`, with `err_details` |
| either, with `expect_pane_sha256` | the agent's pane no longer has that hash | `ErrPaneChanged`, with `err_details` |

All but the last are decided from the store and process checks, before
any tmux call; the last needs a capture of the pane. A request closed with
a non-Cursor spawn is proven gone with it and refuses nothing. A decided request
recorded by an older binary with `agent_gone` proof is treated as closed
for these guards (see the [caller contract](caller-contract.md#permission-relay)).
A request closed at
the pane (`pane_answer` `sent`, `outside` or `tool_ran`) refuses nothing
but, until it is proven gone, a plain call without a pane hash
(`ErrDialogMaybeOpen`). A request its relay hook confirmed is held by the
first rule only while that hook's process is seen running (it is writing
its verdict to the agent), then by the dialog hold until it is proven
gone. There is no time window of the guard's own: the first rule ends when
the relay hook does, and a request recorded by an older binary, whose
hook recorded no identity, holds by it until its `confirm_by`, its relay
window plus 2 s (see "Residual race" below).

So only a call that names the request it answers can type on a spawn with
an open fallen-back request, a plain `send-keys` (an automatic command and
its Enter, say) cannot land on that request's prompt, and a plain
`send-keys` without a pane hash cannot land on a dialog agent-director
believes answered until the agent's hooks show it gone.

`ErrSendKeysWhileRelayed`'s message (advice; the error name is the
contract) names one request whose relay hook may still answer, one still
awaiting an answer before a confirmed one, then the oldest, and states no
release time:

- **Undecided**: `spawn <id> is awaiting a relayed permission decision on
  request <request_token>; answer it with decide`.
- **Verdict recorded**: `spawn <id>: the relayed permission verdict on
  request <request_token> is recorded and its relay hook may still be
  delivering it; retry send-keys later` (from `pause`, it ends `retry
  later`). There is nothing left to answer (`decide` on it returns
  `ErrAlreadyDecided`).

A plain retry after the relay hook has ended can get `ErrDialogMaybeOpen`
instead, until the agent's hooks prove the request gone: after an allow, when
its tool has run (on a Codex row, when an eligible main turn ends); after a
deny of an eligible main request, when the main agent's turn ends (its `Stop` or idle-prompt Notification, or a Codex
Interrupt).

`ErrRelayFallenBack` (from a plain `send-keys`, `pause` and `decide`)
carries `err_details`, an object with:

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

**`pause`.** `pause` types the line-clear key `C-u` (pressed enough times on a
Codex spawn to clear a long draft; see
[Pause and resume](usage.md#pause-and-resume)), `/exit` and Enter into the
agent's pane, as a plain `send-keys` without `expect_pane_sha256` would, so
on a spawn with `relay_mode=on` it is held as one. On a Codex spawn whose
agent already exited with an end report, `pause` ends the row first, before
the check below, and closes its requests (see
[How a Codex row ends](hooks.md#how-a-codex-row-ends)). On a `waiting` spawn
(the only state it types into), before any tmux call, it runs the same check with no
`request_token` and no pane hash, and refuses in the same order, with the
same `err_details`, typing nothing (no `C-u` either) and starting no wait:

- `ErrSendKeysWhileRelayed`: a relay hook of the spawn may still answer
  its request. The message is `send-keys`' own, except that for a request
  whose verdict is recorded it ends `retry later`.
- `ErrRelayFallenBack`: a request of the spawn is fallen back with
  `pane_answer` `none` or `intent`; its `hook_gone_at` is recorded first,
  as for `send-keys`. Close the request (see "Answering at the pane"
  below); `pause` then stays held by the next rule until the agent's hooks
  prove the request gone.
- `ErrDialogMaybeOpen`: a request of the spawn is not proven gone (see
  "The dialog hold" below).

A spawn with the relay off or no permission request pauses as before.
Otherwise, `pause` proceeds only after all relay, fallback and dialog
checks pass. Proof that every request is gone clears the dialog hold;
`turn_end` proof alone can still leave a relay or fallback hold. A finished
spawn is
still a no-op success, and a spawn in any other live state still gets
`ErrSpawnNotPausable`, except a Codex spawn whose agent already exited with an
end report (above).

`pause` takes no pane hash, so it has no way past the dialog hold, which
lasts until the agent's own hooks prove each request gone, whatever is
done at the pane. A subagent's request denied at the pane gets no
`PostToolUse`, and the main agent's `Stop` does not prove a subagent's
request, so it holds `pause` until the spawn's agent ends; on a Codex row
that holds for a subagent's request allowed as well (see
[How the hold differs on Codex rows](#how-the-hold-differs-on-codex-rows)). To stop a held
spawn, `kill` it, or have a person or an LLM do what `pause` would:

1. `read-pane`. First close any fallen-back request: a pane hash gets a
   plain `send-keys` past the dialog hold only, never past
   `ErrRelayFallenBack`. Answer its prompt with a pane answer naming its
   `request_token` if the prompt waits, or else record it with
   `record-pane-answer` (see "Answering at the pane" below). Then answer
   any other dialog on the pane (see "What a caller does" under "The
   dialog hold" below).
2. `read-pane` again and check that the agent's prompt is empty. Unlike
   `pause`, `send-keys` does not clear the input line first, so a draft
   left there would get `/exit` appended and be submitted as a prompt.
3. Send `/exit` with that read's `pane_sha256`:

   ```sh
   agent-director send-keys --instance-id <id> --text /exit \
       --expect-pane-sha256 <H>
   ```

4. Poll `get` or `status` until the row reads `ended`: `send-keys` does
   not wait for it as `pause` does. A Codex row reads `ended` only after
   `find-missing` or `pause` sees its agent gone (see the
   [caller contract](caller-contract.md#when-a-codex-agent-exits)).

Never pass the hash from an automatic flow (see "The look-and-send path"
below).

**Residual race.** A request whose relay hook cannot be checked is held
until its `confirm_by` and is then fallen back. For a request recorded
by an older binary, that is 2 s after its relay window ends, counted from
its `created_at`, and rests on a live relay hook having answered, or been
killed by the agent, by then. Keys can still reach the agent's prompt before
a live hook's verdict or deny does only if both:

- the hook's delivery of its verdict or timeout deny (reading the verdict,
  or noticing its deadline, and its store writes; then writing its
  envelope and exiting) ends more than 2 s past its window, for example
  because the process stalls (an older binary's relay hook could
  wait up to `[store] busy_timeout_ms`, 10 s by default, for each store
  write); and
- The agent kills the hook more than 1 s after its per-hook timeout.

A relay hook of this release commits its last store write at least 2 s
before the agent ends it, and its `confirm_by` is that end plus 2 s.
Nothing stored shows a hook stuck delivering, so this race is not closed
for a call that carries a pane hash (a pane answer, or a plain call that
passes the dialog hold). A plain call without one stays refused after
`confirm_by` too, with `ErrRelayFallenBack` or `ErrDialogMaybeOpen`, until
the request is closed and the agent's hooks prove it gone. All of these
instants assume the hook, the store and the caller share one wall clock.

### The dialog hold

agent-director's own records of a request say what agent-director did,
not what the agent shows. A request can read `delivered` while its
dialog is still on the pane (its relay hook acked the verdict, then died
before writing it), and so can one closed at the pane (the key landed on
an identical dialog of another request) or recorded answered with
`record-pane-answer` (its dialog was drawn late). An Enter typed then
answers that dialog. So from a request's first write, agent-director
treats its dialog as possibly on the pane until the agent's own hooks
prove it gone:

| `proven_gone_how` | Proof |
|---|---|
| `tool_ran` | A Claude agent's `PostToolUse` or `PostToolUseFailure` carrying the request's `tool_use_id`: the tool ran, so its dialog was answered. Any Claude request with that id, a subagent's included. Cursor requires current conversation/launch-bound invocation proof for the exact request and tool id; accepted shell and MCP output and the failed invocation's proof-only meaning are defined in [Cursor spawns](#cursor-spawns). Generic completed/failed status is insufficient. Never on a Codex row. |
| `turn_end` | The turn of the agent that asked ended after the request was written. An owned main-agent `Stop`, Claude idle-prompt Notification or Codex/Cursor main Interrupt proves only a request durably eligible for main-turn proof, with no child id, recorded before the second the report started. Missing child identity alone never establishes eligibility. |
| `agent_gone` | For non-Cursor spawns, `find-missing`'s mark of the spawn `missing`, the agent's terminal `SessionEnd`, or `resume`'s move of the finished spawn to `pending`: every request of the spawn. Upgrading the store proves every request of a non-Cursor spawn already `ended` or `missing` the same way. Cursor terminal closes leave proof unchanged; see [Cursor spawns](#cursor-spawns). |

Main-turn eligibility is stored independently of child identity and survives
restart. Historical non-Cursor main/child behavior is preserved: main
requests remain eligible and child requests do not, including supported
historical rows with missing or malformed stored agent values. Historical Cursor
requests and new unknown-origin requests remain ineligible, including
those with a null child id. An explicit ineligible value remains ineligible.
A known child cannot gain main-turn proof even with contradictory stored
metadata. Child or unknown-origin completion, interruption or session end
is not proof; only matching tool-invocation evidence or the owning spawn
ending can release an ineligible request. A decision, delivery acknowledgement,
pane answer or recorded outside answer never grants eligibility.

The ack (`delivered`), a pane answer recorded `sent` and
`record-pane-answer` prove nothing. Cursor invocation proof comes only from its
authenticated gateway; generic tool hooks cannot supply it. A hook proves
something only when it comes from the row's own agent (see
[hooks.md](hooks.md#only-the-rows-own-agent-moves-the-row)) on a spawn with
`relay_mode=on`. The proof is stored on the request as `proven_gone_at`
and `proven_gone_how`.

**The hold.** While any request of the spawn is not proven gone, a plain
`send-keys` without `expect_pane_sha256` is refused with
`ErrDialogMaybeOpen` and nothing is sent, in every live state of the
spawn. `ErrSendKeysWhileRelayed` and `ErrRelayFallenBack` are checked
first. In practice the hold lasts, after an allow, until the request's
tool has run (on a Codex row, until an eligible main turn ends), and after a
deny of an eligible main request, until the main agent's turn ends. Ineligible requests retain the child/unknown
protection described above. A spawn
with no unproven permission request has no dialog hold (one that never asked
for a permission included), but can still have a relay or fallback hold.
A spawn with the relay off has none of these holds.
`pause` is held the same way, with no way past the hold (see "`pause`"
under "Send-keys interaction" above). A request not proven gone that still
awaits an answer also holds the spawn's moves to `working`; once proven
gone it holds neither the dialog check nor those moves (see "The row
after the last answer" above). Proof alone does not release the earlier
relay or fallback checks.

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
`pane_sha256`. A held `pause` is stopped as "`pause`" under "Send-keys
interaction" above says.

### Answering at the pane

The public pane-send operation does not interpret screen text or pick a key.
The caller inspects `read-pane` and chooses. Alongside the request and ownership
guards, it checks the pane hash (`pane_sha256`, SHA-256 of exactly the returned
bytes), sends the named key and stores `as` as the caller's claim without
verifying its verdict. Cursor additionally matches a sent claim to the actual
selected option before answering ACP; see [Cursor spawns](#cursor-spawns).
A key send or native claim does not establish ACP delivery, receipt or proof;
an outside claim supplies no ACP approval authority.

#### A pane answer through `send-keys`

```sh
agent-director send-keys --instance-id <id> --request-token <T> \
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

For Cursor, a validated exact-request execution can also close an
unacknowledged request with `pane_answer` `tool_ran` and prove it gone. A
recorded outside answer sends nothing and cannot authorize an ACP approval;
plan acceptance and generic completion are not execution proof. The
PostToolUse close described below is Claude-only.

A genuinely unrecorded outside answer leaves its request fallen back and
a plain `send-keys` refused until it is closed. An attached Cursor menu
choice is recorded automatically; see [pane usage](usage.md#answer-a-request-at-the-pane).
Once closed, a plain `send-keys` without a pane hash
is still held (`ErrDialogMaybeOpen`) until the agent's hooks prove the request
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
   by an older binary), and, under the store's write lock, the pane still
   has hash H. It
   records `pane_answer` `outside`, `pane_as` and `decision` the claim
   (`decision` `null` for `unknown`) and `decision_reason` `pane_outside`,
   written to the trail as one `ad.row_mutation.committed`
   (`writer_process` `record_pane_answer`), and returns them.
   - **Why the 2 s.** The agent draws its prompt only after the relay
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
2. **The PostToolUse close** (Claude rows only: a Codex request carries no
   `tool_use_id`, so a fallen-back Codex request closes only by the first
   way). When the agent's `PostToolUse` or
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

The hook-specific sequence below describes Claude and Codex. For Cursor,
follow [the caller contract](caller-contract.md#permission-relay): a recorded
unacknowledged decision can still leave a connected native request answerable
after settlement. An `ErrAlreadyDecided` alone establishes no receipt.

At the window's end the relay hook normally denies the request itself
(see "Relay timeout default and override" above): `decide` then returns
`ErrAlreadyDecided` (`decision_reason` `timeout`) and there is nothing to
recover. A relayed spawn is wedged when its relay hook died or was killed
without answering: `decide`, a plain `send-keys` and `pause` return
`ErrRelayFallenBack`. Recover it through agent-director, **never through
raw tmux**:

1. Take the request's facts from the error's `err_details` (or
   `get-permission`). If `decide` returns `ErrAlreadyDecided` instead, the
   relay hook or another caller answered the request and there is nothing
   to recover; if it returns `ErrNoOpenPermissionRequest`, the request is
   closed or its prompt cannot be shown to wait: do not answer it at the
   pane. If a plain `send-keys` is still refused with `ErrRelayFallenBack`
   on that request (a stale record from an older binary), close it
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
   hash, and `pause`, stay held (`ErrDialogMaybeOpen`) until the agent's hooks
   prove it (after a deny of an eligible main request, normally the turn's `Stop`; on a Codex row, the
   turn's `Stop` or Interrupt, after an allow too). To type before that,
   read the pane again and send with that read's `pane_sha256`; to stop
   the spawn, see "`pause`" under "Send-keys interaction" above.

### Known limitations

`decide`, `send-keys`, `pause` and the readers judge stored records and
processes, never the agent's screen. These cases are not covered:

- **What fallen back does not say.** `fallen_back` and
  `ErrRelayFallenBack` mean no answer from the relay reached the agent and
  no pane answer is recorded through agent-director. They do not say
  whether the agent's permission prompt still waits: someone at the pane
  may already have answered it. Until such a request is closed
  (`record-pane-answer`, or the PostToolUse close of a Claude row's allow),
  a plain `send-keys` is refused, and `get` lists it while the spawn is in
  `check_permission`. Until the agent's hooks also prove it gone (the main
  agent's `Stop` or idle-prompt Notification after it, or a Claude row's tool's
  `PostToolUse`; on a Codex row its `Stop` or Interrupt), the spawn stays in
  `check_permission` until a later hook
  moves it out (`Stop` on Claude/Codex, or Claude's `AskUserQuestion`), its moves to
  `working` are held, and `find-missing` does not repair the row; after a
  deny at a Claude pane that interrupts the turn (Claude Code sends no `Stop`
  then), that lasts until the idle-prompt Notification or the next turn's
  `Stop`. Once it is closed, a plain `send-keys` without a pane hash stays
  held until the agent's hooks prove it gone.
- **A prompt agent-director has no record of.** A relay hook that dies, or
  is killed, before it records its request leaves a prompt that no request
  stands for, so a plain `send-keys` is not refused, and its Enter can
  answer that prompt. The dialog hold starts at the request's first write,
  so it does not cover this case.
- **A confirmed answer not returned.** The relay hook confirms its verdict
  or its timeout deny at least 2 s before the agent ends it, then writes
  it. A hook that dies or stalls past the agent's kill between the two
  leaves the request reading `delivered` while the agent never received
  the answer: `decide` on it returns `ErrAlreadyDecided`, which advises no
  pane answer. The request is not proven gone, so a plain `send-keys`
  without a pane hash is held (`ErrDialogMaybeOpen`) and `get` lists it in
  `unproven_requests` until someone looks at the pane.
- **Between the capture and the key.** The pane can change between
  agent-director's capture and the moment the agent reads the key, a
  window of milliseconds that no check closes. Likewise, a key already on
  its way when a dialog appears, before its request's first write, lands
  on that dialog.
- **Identical prompts on Claude/Codex.** Two prompts whose captured bytes are the same
  (same tool, same input, same surrounding lines) cannot be told apart: a
  pane answer meant for one can close the other, and the record then names
  the wrong request. The other request stays unproven, so a plain
  `send-keys` without a pane hash is held until someone looks.
- **A prompt drawn more than 2 s late.** `record-pane-answer` waits 2 s
  after the relay hook was found gone (or after its `confirm_by`, for a
  hook judged by time). An agent that stalls longer
  before drawing its prompt can let a caller record the request answered
  before the prompt appears. The request stays unproven, so a plain
  `send-keys` without a pane hash is held until the agent's hooks prove it gone.
- **A subagent's denied request.** A request carrying an `agent_id` (a
  subagent's or an in-process teammate's) is proven gone only by its
  tool's `PostToolUse` or by the spawn's end: the main agent's `Stop` and
  idle-prompt Notification do not cover it, since a background subagent
  can outlive the main turn, and the subagent's own stop hook
  (`SubagentStop`) proves no request gone. A deny runs no tool, so after a denied
  subagent request a plain `send-keys` without a pane hash, and `pause`,
  are held until the spawn's agent ends; only a person or an LLM who
  looked, sending with the pane's hash, gets keys through. A subagent's
  request answered at the pane outside agent-director also holds the
  spawn's moves to `working` until `record-pane-answer`, its tool's
  `PostToolUse` or the spawn's end. On a Codex row no tool-ran proof
  exists, so an allowed subagent request is held the same way (see
  [How the hold differs on Codex rows](#how-the-hold-differs-on-codex-rows)).
- **A request recorded in the hook's start second.** A `Stop`,
  idle-prompt Notification or Codex/Cursor main Interrupt proves only eligible
  main requests recorded
  before the second it started, so one recorded in that second stays unproven, holding plain
  `send-keys` (and, while it awaits an answer, the spawn's moves to
  `working`) until a later turn end.
- **Lost end-of-turn hooks.** After a denied eligible main request (on a Codex row, an
  allowed one too), if both the turn's `Stop` and the idle-prompt
  Notification are lost (on a Codex row, its one end hook: the `Stop` or
  Interrupt), a plain `send-keys` without a pane hash stays held until
  someone looks and sends with the pane's hash, or the spawn ends.
- **A key to stop a busy agent after a deny.** An `Escape` sent to stop the
  agent is a plain `send-keys`: right after a deny (on a Codex row, an allow
  too) it is held until the turn ends, so it needs a person or an LLM to look and send it with the
  pane's hash.
- **`pause` has no way past the hold.** `pause` takes no pane hash, so a
  relay-on spawn with a request not proven gone refuses it
  (`ErrDialogMaybeOpen`) until the agent's hooks prove the request gone: after
  a denied subagent request (on a Codex row, an allowed one too), until the
  spawn's agent ends; after a request
  recorded by an older binary on an agent already idle at its prompt when
  you upgraded, until its next turn ends. Stopping such a spawn needs
  `kill`, or a person or an LLM who looks at the pane and sends `/exit`
  with `send-keys` and the pane's hash, which neither clears a draft first
  nor waits for `ended` (see "`pause`" under "Send-keys interaction").
- **A pane answer whose sender died.** It leaves `pane_answer` `intent`,
  its key possibly typed. The request stays open and fallen back, so plain
  `send-keys` stays refused, until a retry with a fresh hash or
  `record-pane-answer` closes it.
- **Requests recorded by an older binary.** `decide`'s check that the
  spawn is shown sitting on such a request alone reads stored records
  only, so it can be wrong both ways. A record left open after its prompt
  closed (answered at the pane after the hook was killed, or closed by a
  timeout deny the hook returned without recording it) keeps the spawn in
  `check_permission` until a later hook moves it out (the main agent's
  next `Stop` or idle-prompt Notification also proves it gone, so it holds
  no later move to `working`), and until then
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
  token of a request recorded by an older binary is accepted once the
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
