# Usage

Every verb prints JSON on stdout and a typed error on stderr. The full verb and
flag list is in `cli-reference.md`.

## Naming the agent

A `spawn` names its agent, `claude` or `codex`, with `--agent` on the call or
through its [template](#templates); with neither it is refused. The rules
for combining the two are in [Settings](settings.md#templates-and-per-call-merge).

## Spawn states

A row is `pending` while its launch is in progress and its agent has not
reported in; it becomes `waiting` once it does. If the agent runs but never
reports in, `find-missing` notes the row `unreported` (see
[Maintenance](maintenance.md)).

A Codex agent reports in at its first prompt, not when its screen opens, so
its row stays `pending` until someone prompts it. Until then `pause` and
`send-keys` without `allow_pending` (`--allow-pending` on the CLI) are
refused, as for any `pending` row. Pass the first prompt in the `spawn`
arguments, or read the pane with `read-pane` and then type with
`allow_pending`. After the first prompt the row moves
with Codex's own events: a turn's prompt and tools read `working`, its end
(`Stop`, or Esc) reads `waiting`, and a permission request reads
`check_permission`. No Codex tool reads `ask_user`. A session change in the
agent (`/clear`, `/new`, `/fork`, `/resume`) shows from the next prompt, and
the `SessionEnd` that `/exit` sends leaves the row's state as it is. After Codex's `/review` the row can
read `working` until the next prompt. The events and the limits are in
[Hooks](hooks.md#hooks-from-a-codex-agent).

`spawn` marks the folder as trusted so the agent does not stop at Claude
Code's folder-trust prompt. `--no-pre-trust` keeps the prompt, and holds for
every `resume` of the id: none marks the folder, so the agent may wait at the
prompt.

## Naming the tmux session

`spawn --tmux-session-name <name>` replaces the default session name
`<basename(cwd)>-<id[:8]>`:

```sh
agent-director spawn --cwd "$PWD" --agent claude --tmux-session-name status-agent
```

The name must be non-empty, at most 64 bytes, valid UTF-8, with none of `#`,
`:`, `.`, `$`, `\` or ASCII control bytes (`\x00`-`\x1f`, `\x7f`). Ended
spawns can reuse a name.

## Finding a Spawn by its tmux session name

```sh
agent-director list --tmux-session-name status-agent
```

The filter is exact-match, combines with `--state`, `--label`, `--parent`,
`--cwd` and `--limit`, and returns live and ended rows, so one name can match
several rows.

## Drive a Spawn from another agent

Install with `--register-mcp`. The verbs then appear as
`mcp__agent-director__spawn`, `mcp__agent-director__send_keys`, etc.; `hook`,
`serve` and `trail-emit` are CLI-only. Tool arguments use underscores
(`--instance-id` is `instance_id`); the list is in
`mcp-reference.md`.

## Watch a Spawn's state

With a [settle time](configuration.md#settle-time-state) set, `state` ignores
flips shorter than it, so a script that polls `status` can act on the first
change it sees, with no count of identical polls:

```sh
last=""
while sleep 1; do
  state=$(agent-director status --instance-id "$id" | jq -r .state)
  if [ "$state" != "$last" ]; then
    last=$state
    case "$state" in waiting|ask_user|check_permission|ended|missing) echo "$id: $state" ;; esac
  fi
done
```

It never sees a turn shorter than the settle time.

## Read an agent's subagent records

`list-subagents` returns the subagents an agent has launched, one record each,
and whether the agent is tracked. It is `mcp__agent-director__list_subagents`
over MCP, `Client.ListSubagents` in Go and `listSubagents` in TypeScript (see
[pkg/api/README.md](../pkg/api/README.md#listsubagents) and
[pkg/ts-bun-client/README.md](../pkg/ts-bun-client/README.md#listsubagents)).
Reading changes nothing.

```sh
agent-director list-subagents --instance-id "$id"
```

The command prints one line; this is one running foreground record, laid out
for reading:

```json
{
  "tracked": true,
  "subagents": [
    {
      "subagent_id": "ad5f0c1b7e2a94316",
      "instance_id": "claude_2026-10-11T15-16-58",
      "session_id": "0b3c2f1e-6d4a-4c1b-9a57-2f8e1d7c5a90",
      "subagent_type": "Explore",
      "description": null,
      "model": null,
      "parent_type": null,
      "parent_id": null,
      "tool_use_id": null,
      "run_mode": null,
      "first_started_at": "2026-10-11T15:17:03.12Z",
      "started_at": "2026-10-11T15:17:03.12Z",
      "stopped_at": null,
      "lost_at": null,
      "state": "running",
      "close_reason": null
    }
  ]
}
```

`(instance_id, subagent_id)` identifies a record for good: a caller that
reads the list again can match records by it.

### When an agent is tracked

`tracked` is `true` when the agent's current launch registered its subagent
start and stop events, so its subagents are recorded. It is `false`, with
`subagents` `[]`, for an agent that predates subagent tracking; it becomes
`true` when that agent is next resumed or its id is reused. The hooks that
write the records are in [Hooks](hooks.md#what-the-hooks-write-for-subagent-records).
A Codex agent's hooks write no records, so its `subagents` stays `[]`
whatever `tracked` reads.

`subagents` is never null. It holds the records of every launch the agent has
had, ordered by `first_started_at` ascending (records with a null
`first_started_at` last), then by `subagent_id`.

### The record's fields

Every record carries all of these keys, in this order. A field that nothing
supplied is `null`, never a guess.

| Field | Type | Meaning |
|---|---|---|
| `subagent_id` | string | The subagent's id, unique within its agent. |
| `instance_id` | string | The id of the agent the subagent belongs to. |
| `session_id` | string or null | The session of the first event that recorded the subagent: its start, its launch result or an agent stop that listed it (see [Which subagents show as records](#which-subagents-show-as-records)). It never changes, so after a conversation clear it depends on which event came first. |
| `subagent_type` | string or null | The subagent's type. |
| `description` | string or null | The short description the launching call gave it, taken only from that call's own result. Never the prompt text. |
| `model` | string or null | The model it runs. |
| `parent_type` | string or null | What launched it: `agent` or `subagent`. |
| `parent_id` | string or null | The launcher's id: the agent's `instance_id` for `agent`, the launching subagent's `subagent_id` for `subagent`. A null parent belongs to the record's `instance_id`. |
| `tool_use_id` | string or null | The id of the tool call that launched it. |
| `run_mode` | string or null | `background` or `foreground`. A subagent that is listed as running in the background gets `background` when it has none. |
| `first_started_at` | time or null | When it first started. |
| `started_at` | time or null | When its latest run started. |
| `stopped_at` | time or null | When it last stopped; null while it never has. A record that runs again keeps this time until its next stop. |
| `lost_at` | time or null | When it was judged lost; null unless it was. |
| `state` | string | `running`, `stopped` or `lost`. |
| `close_reason` | string or null | Why it is no longer running; null exactly while `state` is `running`. |

The values of the enumerated fields are fixed:

- `state`: `running` (started, neither stopped nor judged lost), `stopped`
  (its agent reported it finished) or `lost` (judged gone without a stop
  report; see [When a record turns lost](#when-a-record-turns-lost) and
  [When an agent ends, goes missing or relaunches](#when-an-agent-ends-goes-missing-or-relaunches)).
- `close_reason`: `stop_event` (its agent reported it finished),
  `absent_from_snapshot` (the agent's list of running background tasks at a
  stop did not name it) or `agent_gone` (its agent ended, was marked missing or
  began a new launch; see
  [When an agent ends, goes missing or relaunches](#when-an-agent-ends-goes-missing-or-relaunches)).
  It is not the `agent_gone` of a permission request's `proven_gone_how` (see
  [Intercept permission prompts](#intercept-permission-prompts)).
- `parent_type`: `agent` (the agent itself launched it) or `subagent` (another
  subagent launched it, or an in-process teammate, which has no record; see
  [Subagents and in-process teammates](hooks.md#subagents-and-in-process-teammates)).
- `run_mode`: `background` (it runs beside the agent's own turn) or
  `foreground` (the agent waits for it).

Until a foreground subagent finishes, its record holds only what its start
carried: `session_id`, `subagent_type` and the start times. `description`,
`model`, `parent_type`, `parent_id`, `tool_use_id` and `run_mode` stay null
until its launch result arrives, and no other launch's value fills them. A
background subagent whose launch result was lost keeps a null `run_mode` until
a stop of the agent lists it as running in the background, so until then it
reads like a running foreground one. A foreground subagent known only from its
finished launch result has no start times.

### Times

A time is the moment agent-director received the event, never a time from the
event's payload. It is RFC 3339 in UTC, ending in `Z`, with millisecond
precision: trailing zeros are dropped and a whole second has no fraction (for
example `2026-10-11T15:17:03.12Z` and `2026-10-11T15:17:03Z`). A pending row's
`launch_started_at` has the same form. Hooks that race can put a time off by a
few milliseconds. A stop event's time replaces the time of a finished launch
result.

### Which subagents show as records

A subagent shows as a record once a launch result, a launch it matches or an
agent stop that lists it confirms it. A start that never showed (one that
matched no launch) is not a record until then; any stop that lists it turns it
into one, whether the agent's own main-turn stop or any subagent's stop.
These cases show a record late or oddly:

- A foreground subagent shows only when it finishes if its launching call named
  it, if it was launched with no type, or if its start finds no open launch of
  its type.
- A start confirms through any open launch of its type, not necessarily its own
  call; the record's fields still come from the subagent's own launch result.
- A teammate whose type equals that of a running launch can show as one
  record.
- A subagent launched by an in-process teammate has `parent_type` `subagent`
  and the teammate's id as `parent_id`, with no record of that teammate.
- A background subagent whose start and launch result were both missed shows at
  the agent's next main-turn stop that lists it as running (a subagent's stop
  never creates a record). The record is `running`, its
  `run_mode` is `background`, its `subagent_type` is the type the list gave and
  its `session_id` is that stop's session. Its `description`, `first_started_at`
  and `started_at` are null until its launch result or a start supplies them.
  Nothing from the list's own description is kept.
- A stop that lists a subagent already known, whether the agent's main-turn
  stop or any subagent's stop, confirms it (a start that never showed
  becomes a record) and gives it a `background` `run_mode` if it has none. It
  changes no state, no time and no type.

### How long a record reads running

A record stays `running` until a stop signal arrives: a stop event, or for a
foreground subagent its finished launch result. It also stays `running` when
its subagent ends with no stop event (for example when the user interrupts it),
and when its stop event and its launch result are both over the 1 MiB payload
limit (see [Fail-open invariant](hooks.md#fail-open-invariant)). A later stop
turns a record `lost` only in one case, described next: a `running` background
record of the agent's current launch whose stop event was dropped or never
sent. A foreground record, or one whose run mode is unknown, is never turned
`lost` by a stop, so a foreground or unknown-mode record that gets no stop
signal (for example a foreground subagent whose stop event and launch result
were both over the limit) keeps reading `running` until its agent's row ends,
goes missing or the agent begins a new launch (see
[When an agent ends, goes missing or relaunches](#when-an-agent-ends-goes-missing-or-relaunches)).

A start for a `stopped` subagent runs its record again. While another stop
hook blocks a stop, the record reads `stopped` although the subagent keeps
running; the real stop then updates `stopped_at`.

### When a record turns lost

When the agent's main turn ends and its stop carries the list of its running
background tasks, every `running` background record of the agent's current
launch that the list does not name turns `lost`: `close_reason` is
`absent_from_snapshot` and `lost_at` is the time of that stop. This is how a
background subagent that ended without its stop event reaching agent-director
stops reading `running`. The rules:

- A record that began running no more than 2 s before the stop is left alone,
  so a subagent that has only just started is not lost for missing the list.
- It applies to every session of the agent. After a conversation clear, the new
  session's stop closes an old session's background record only when the list
  no longer names it.
- A stop that carries no list closes nothing. A list that names no subagent is
  a list, and closes every record it can.
- A listing never reopens a `stopped` or `lost` record; only a new start does.
  A later stop event for a `lost` record turns it `stopped`: `close_reason`
  becomes `stop_event`, `stopped_at` is that stop's time and `lost_at` is null.
- Records of an earlier launch are left as they are.

What the hooks read and write for the list is in
[Hooks](hooks.md#what-the-hooks-write-for-subagent-records).

### When an agent ends, goes missing or relaunches

Every `running` record of the agent turns `lost` with `close_reason`
`agent_gone`, whatever its run mode and launch, when one of these happens.
`lost_at` is the time of that change:

- The agent exits and its row becomes `ended`. A session end that only clears
  or compacts the conversation closes nothing (see
  [Hooks](hooks.md#the-eleven-state-tracking-hooks)).
- `find-missing` marks the row `missing` (see
  [Maintenance](maintenance.md) for scheduling it). A row it leaves live, with
  an unverified result or the `unreported` note, closes nothing.
- The agent begins a new launch, by `resume` or by reuse of its id. `lost_at`
  is the new launch's start, the `launch_started_at` of its `pending` row.

`kill` alone changes no record: a killed agent's `running` records turn `lost`
once its row is marked `missing` or ends, as the row itself changes.

A row that comes back to a live state through its own agent's hooks after it
ended or was marked `missing` keeps its closed records closed.

### Limits and retention

Each string field holds at most 1 KiB, cut at a whole character. Only the
agent's own hooks write records, and no prompt, message or tool output is
stored.

An agent keeps at most 300 entries: its records plus starts that nothing has
confirmed, which are never shown. When a new entry arrives at the limit, one is
evicted first: a closed unconfirmed start, else a closed record, else a
`running` entry only when nothing closed remains; within each, the one changed
longest ago goes first. A `resume` or reuse keeps every record, the `running`
ones closed as above, so none of an earlier launch reads `running`. They are
deleted with the row (see [Maintenance](maintenance.md) for `expire` and
[Operator actions](operator-actions.md) for `agent-director-admin delete`).

## Intercept permission prompts

With `--relay-mode=on`, an agent permission prompt moves the row to
`check_permission` and waits for your `decide`:

```sh
id=$(agent-director spawn --cwd "$PWD" --agent claude --relay-mode=on \
       | jq -r '.instance_id')

token=$(agent-director get --instance-id "$id" \
          | jq -r '.permission_requests[0].request_token')
# A caller that must answer within 3 s passes at most 2000 (its deadline minus 1 s).
agent-director decide --instance-id "$id" \
    --request-token "$token" \
    --decision allow --max-wait-ms 2000
```

`decide` records the verdict, then waits at most 1 s for the agent's relay
hook to confirm it took the verdict, and returns the request's `delivery`:

- `delivered`: the relay hook confirmed the verdict before handing it to
  Claude Code.
- `not_confirmed`: no confirmation yet. Poll `get-permission` until
  `delivery` reads `delivered` or `fallen_back`; it does by `confirm_by`.

`decision` is the verdict recorded, not the outcome: read `delivery`. A
request can read `decision` `allow` with `delivery` `fallen_back`. When
`decide` refuses, it records no verdict; what each refusal means is in the
[caller contract](caller-contract.md#permission-relay).

`--max-wait-ms` bounds the whole call, its reads and its write, and has no
default. Without it, `decide` can wait up to `[store] busy_timeout_ms` for a
busy store. Once the verdict is recorded, `decide` never returns
`ErrStoreBusy`: a bound reached while it waits for the confirmation returns
`not_confirmed`.

Follow a request by `request_token`:

```sh
agent-director get-permission --request-token "$token"
```

The response is a single permission row:

```json
{
  "request_token": "9b1d8a8e-3a3d-4f4f-8a8e-3a3d4f4f8a8e",
  "request_id": 42,
  "tool_name": "Bash",
  "tool_input": "{\"command\":\"npm test\"}",
  "requested_at": "2026-05-31T12:00:00Z",
  "decision": "allow",
  "decision_reason": null,
  "decided_at": "2026-05-31T12:00:01Z",
  "delivery": "delivered",
  "confirm_by": "2026-06-01T12:00:02.120Z",
  "hook_alive": false,
  "hook_gone_at": null,
  "attempted_decision": null,
  "attempted_at": null,
  "tool_use_id": "toolu_01ABCDEF",
  "pane_answer": "none",
  "pane_as": null,
  "proven_gone_at": "2026-05-31T12:00:04.310Z",
  "proven_gone_how": "tool_ran",
  "unproven_since": null
}
```

`decision_reason` says who recorded the verdict: `operator` (a `decide`
deny; a `decide` allow has none), `timeout` (the relay hook's own deny),
`find_missing` and `ended` (a deny written when the request was closed with
its agent), `pane` (a pane answer through `send-keys`), `pane_outside`
(`record-pane-answer`) and `tool_ran` (the request's tool ran, so it was
allowed).

`get` and `list` carry the same delivery fields on each request in a row's
`permission_requests`; they list only requests that still await an answer,
never a closed one. `hook_alive` is `null` when agent-director cannot check
the relay hook's process (for example from another pid namespace, or for a
request recorded by an older agent-director); such a request falls back by
time, at `confirm_by`. A closed request's `delivery` reads the same way as
any other's: `delivered` only if its relay hook confirmed a verdict. A row
can read `waiting`, `working` or `ask_user` while a request it recorded
still awaits an answer, so follow each request you track with
`get-permission`, not only rows in `check_permission`.

`proven_gone_at` and `proven_gone_how` say when and how Claude Code itself
showed the request's dialog gone: `tool_ran` (its tool ran), `turn_end`
(the turn of the agent that asked ended after it was written) or
`agent_gone` (the agent ended). `delivered` and a pane answer are
agent-director's own records, not proof. `get` lists every request not yet
proven gone in `unproven_requests`, with `unproven_since`. A request that
reads `delivered` or closed at the pane but stays unproven for a while is a
reason to look at the pane: its dialog may still be there.

## Answer a request at the pane

No answer from the relay reached a request whose relay hook is gone
(`delivery` `fallen_back`); only an answer at the agent's pane can close
it. Look at the pane, then send the one key that answers it, naming the
request:

```sh
pane=$(agent-director read-pane --instance-id "$id")
echo "$pane" | jq -r '.pane'
hash=$(echo "$pane" | jq -r '.pane_sha256')

agent-director send-keys --instance-id "$id" \
    --request-token "$token" --as deny --key Escape \
    --expect-pane-sha256 "$hash"
```

With `--request-token`, `send-keys` sends exactly that one key (a named
key `Escape`, `Enter`, `Up`, `Down`, `Tab`, or one character) and never
Enter, then records the request answered: `pane_answer` `sent`, `decision`
your `--as`, `decision_reason` `pane`. `--as`, `--key` and
`--expect-pane-sha256` are required; pass the same `--n-lines` you read
with. agent-director never reads the pane: it compares the hash byte for
byte, and `--as` is stored as your claim. What each refusal means is in the
[caller contract](caller-contract.md#permission-relay); on a refusal nothing
is sent.

If the request was answered outside agent-director, for example by a
person at tmux, record that instead (it types nothing):

```sh
agent-director record-pane-answer --request-token "$token" \
    --as unknown --expect-pane-sha256 "$hash"
```

Pass `--as unknown` unless you saw how it was answered. A request whose
tool then runs is closed on its own (`decision_reason` `tool_ran`). A
closed request keeps `delivery` `fallen_back`; its `pane_answer` (`sent`,
`outside` or `tool_ran`) says how it was closed.

Plain `send-keys` (no `--request-token`) also takes `--no-enter` (type
`--text` with no Enter) and `--key` (send one key alone), and
`--expect-pane-sha256` optionally. While a permission request of the agent
may still have its dialog open, a plain `send-keys` and `pause` are held
(see the [caller contract](caller-contract.md#permission-relay)). To stop a
held agent, `kill` it, or close any fallen-back request (above), then have
a person or an LLM read the pane, answer any dialog, check the prompt is
empty, send `/exit` with that read's hash and poll `status` until the row
is `ended`:

```sh
agent-director send-keys --instance-id "$id" --text /exit \
    --expect-pane-sha256 "$hash"
```

Never pass `--expect-pane-sha256` from an automatic flow: it says that a
person or an LLM judged that exact screen, and passed blindly it lets
through the Enter the hold exists to stop.

## Templates

```sh
agent-director make-template --name dev --cwd /repos/widget --agent claude \
    --agent-args --model --agent-args opus \
    --label project=widget --allow 'Bash(npm test)'

agent-director spawn --template dev --label run=$(date +%s)
```

The template names its agent, so that `spawn` needs no `--agent`.
`--overwrite` replaces an existing template atomically.

## Pause and resume

`pause` asks a `waiting` agent to exit and keeps its row. `resume` relaunches
it under the same id with its transcript, so the conversation carries on.
A resumed Claude row reads `waiting` at once. A resumed Codex row reports in
only at its first prompt, so it reads `pending` until then and `pause` and
`send-keys` without `allow_pending` are refused (see
[Spawn states](#spawn-states)).

```sh
agent-director pause --instance-id "$id"
agent-director resume --instance-id "$id"
```
