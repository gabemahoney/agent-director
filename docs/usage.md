# Usage

Every verb prints JSON on stdout and a typed error on stderr. The full verb and
flag list is in `cli-reference.md`.

## Naming the agent

A `spawn` names its agent, `claude`, `codex` or `cursor` (case-sensitive), with `--agent` on the call or
through its [template](#templates); with neither it is refused. The rules
for combining the two are in [Settings](settings.md#templates-and-per-call-merge).

Cursor supports fresh launches, finished-id reuse, agent switches and
latest-session resume; see
[Fresh Cursor use](#fresh-cursor-use) and [Pause and resume](#pause-and-resume).

For any agent, a `spawn --reuse-finished` on a finished id starts a fresh
conversation with the resolved agent; Cursor reuse never loads an earlier
conversation. Keeping the same agent keeps its current life, including
its session history and subagent records. Every agent switch starts a fresh
life and permanently surrenders agent-director resume access to the prior
life, including when switching back. Earlier lives' history and records stay
stored but are hidden from current-life reads. The append-only trail retains
events from all lives. A switch neither hides nor stops child spawns: `list --parent <id>` still lists them.
The record rules are in [Read an agent's subagent records](#read-an-agents-subagent-records).

For example, Claude runs and ends on an id, then Codex runs and ends on
that id, then `spawn --reuse-finished --agent claude` on the same id carries
`--resume` in its agent arguments. Claude starts fresh: the selector is
removed, resumes nothing, and never reopens the old Claude session. Switching
back always starts another new life. In the reverse direction, a Codex
`resume` or `fork` request on a finished Claude id starts Codex fresh while
keeping its prompt. Removed arguments appear in `dropped_args`; the policy
and reporting rules are in
[Caller contract](caller-contract.md#spawn-and-template-input-refusals).

Failed-reuse recovery is in
[Caller contract](caller-contract.md#transcript-recovery).

A Codex spawn passes Codex's own arguments after `--`. This one starts the
agent on a first prompt, one argument:

```sh
agent-director spawn --cwd "$PWD" --agent codex -- "Fix the failing test in pkg/widget"
```

A few arguments are refused, for Claude and Codex, because the supervisor must own
them. For Codex they include a subcommand as the first word that is neither
an option nor an option's value, `--remote`, and `-c` keys that touch its
hooks or typing setting; an option is refused even after the `--`. The full list is in
[Caller contract](caller-contract.md#spawn-and-template-input-refusals).

## Spawn states

A row is `pending` while its launch is in progress and its agent has not
reported in; it becomes `waiting` once it does. If the agent runs but never
reports in, `find-missing` notes the row `unreported` (see
[Maintenance](maintenance.md)).

A Codex agent reports in at its first prompt, not when its screen opens, so
its row stays `pending` until someone prompts it. Until then `pause` and
`send-keys` without `allow_pending` (`--allow-pending` on the CLI) are
refused, as for any `pending` row (once the agent has exited with an end
report, `pause` ends the row instead; see the
[caller contract](caller-contract.md#when-a-codex-agent-exits)). Pass the first prompt in the `spawn`
arguments, or read the pane with `read-pane` and then type with
`allow_pending`. After the first prompt the row moves
with Codex's own events: a turn's prompt and tools read `working`, its end
(`Stop`, or Esc) reads `waiting`, and a permission request reads
`check_permission`. No Codex tool reads `ask_user`. A session change in the
agent (`/clear`, `/new`, `/fork`, `/resume`) shows from the next prompt, and
the `SessionEnd` that `/exit` sends leaves the row's state as it is, until
`find-missing` or `pause` sees the agent gone (see
[How a Codex row ends](hooks.md#how-a-codex-row-ends)). After Codex's `/review` the row can
read `working` until the next prompt. The events and the limits are in
[Hooks](hooks.md#hooks-from-a-codex-agent).

`spawn` marks the folder as trusted so the agent does not stop at its
folder-trust prompt, Claude Code's or Codex's. `--no-pre-trust` keeps the
prompt, and holds until the next spawn or reuse of the id. Each `resume`
follows the latest spawn's choice: with pre-trust off, none marks the folder, so the
agent may wait at the prompt. What the mark is for each agent, and what the
reported `pre_trust` outcome means, is in
[Settings](settings.md#pre-trust-for-codex).

## Fresh Cursor use

Cursor runs through an interactive gateway in its own tmux pane. Fresh
launches currently require Linux; macOS support and a supported Cursor
version range are not established. agent-director does not set up Cursor
login or group accounts. Use an already configured Cursor runtime. The
optional program path is in [Configuration](configuration.md#keys).

```sh
id=$(agent-director spawn --cwd "$PWD" --agent cursor | jq -r .instance_id)
agent-director status --instance-id "$id"
agent-director read-pane --instance-id "$id"
agent-director send-keys --instance-id "$id" --text "Describe this project's entry points"
```

Wait for `waiting` before sending. Fresh startup reports when Cursor confirms
its new conversation, without a first prompt. Login or a runtime that has not
reported remains `pending`, and can be noted `unreported`. A submitted prompt
reads `working`; a correlated turn completion returns it to `waiting`
subject to [request holds](permissions.md#the-dialog-hold).

The existing `inject_help_hook` setting also enables Cursor guidance; its default
is off. When enabled, session setup supplies the current AD guidance before
publication and input admission, including latest-session load and every confirmed
`/clear` or `/new`. It adds no user prompt and does not replay the initial prompt.
See [Settings](settings.md#defaultsinject_help_hook-config) and the
[delivery and compaction limits](hooks.md#what-agent-director-help-actually-outputs).
Caller setup is in [Drive a Spawn from another agent](#drive-a-spawn-from-another-agent);
shell access and restart limits are in the
[caller contract](caller-contract.md#cursor-operation-boundaries).

To take over, read the row's `tmux_socket` and `tmux_session_name` with `get`,
then attach on that socket:

```sh
agent-director get --instance-id "$id"
tmux -u -S '<tmux_socket>' attach-session -t '<tmux_session_name>'
```

The pane accepts editable Unicode text and bracketed multiline paste. Enter
submits one complete prompt, preserving its Unicode text and newlines.
After confirmed startup, follow-ups wait in the order their submissions are
observed at the terminal. The queue holds at most 32 waiting submissions and
8 MiB of waiting text, in addition to one active prompt; each prompt is at
most 1 MiB. A saved initial prompt goes first. Accepted submissions stay bound
to that launch and confirmed conversation. One prompt runs at a time, after
the previous turn and all outstanding decision holds have resolved. See
[The dialog hold](permissions.md#the-dialog-hold).

Ctrl-C requests interruption and cancels affected queued submissions
individually. Controls take priority over queued input. `/clear` and `/new`
are aliases: cancel the current turn and accepted waiting work, then create a
fresh conversation within the same life. Cancelled accepted input never moves
to the new conversation. Repeated aliases while rotation is pending are
reported and ignored; they do not start another rotation or extend its deadline.

Complete input consumed during rotation is held provisionally, without admission
to either conversation. This separate holding capacity is 32 submissions and
8 MiB, with the same 1 MiB individual limit. A confirmed fresh identity alone
does not admit held input: publication must also record the current conversation
and outgoing history, and all remaining decision holds must resolve. Eligible
held input then joins the fresh conversation's queue in its original observed
order. See [The dialog hold](permissions.md#the-dialog-hold).

`/exit` is a local exit command. `pause` on an
eligible waiting row clears the draft and requests exit. Session termination
is reported only after the owned runtime is gone. Refusal, interruption and
shutdown recovery follow the
[Cursor caller contract](caller-contract.md#cursor-operation-boundaries).

Cursor arguments support model selection (`--model VALUE`, `-m VALUE`, or
either spelling with `=VALUE`), an explicit mode (`--mode agent`,
`--mode plan`, `--mode ask`, or the corresponding `=` form), and `--plan`
for plan mode. Model identifiers pass through unchanged; availability depends
on the configured Cursor account. Each new, loaded or rotated conversation
must confirm the requested mode before publication or prompt submission.
The requested mode is initial configuration; later plan approval may change it.

One initial prompt may be the final argument:

```sh
agent-director spawn --cwd "$PWD" --agent cursor -- "Describe this project's entry points"
```

For a prompt beginning with a flag or equal to `acp`, `chat` or `resume`,
put another `--` before the single prompt argument:

```sh
agent-director spawn --cwd "$PWD" --agent cursor -- -- "--explain this project's entry points"
```

The initial prompt preserves Unicode and newlines. It is submitted once per
launch after session configuration and decision holds permit it, including
saved-argument replay on resume; `/clear` and `/new` do not submit it again.

Other arguments and lifecycle limitations are in the
[caller contract](caller-contract.md#cursor-operation-boundaries). Permission
and question behavior is in [Permissions](permissions.md#cursor-spawns).

### Cursor transcripts

`get` exposes the confirmed conversation's gateway transcript in `jsonl_path`.
It is one ordered JSONL file, continued across resumes, beneath
`~/.config/agent-director/cursor-transcripts/`, with private access. It records
launch/conversation identity and submission identities linking accepted input,
dispatch starts, updates, cancellation and confirmed outcomes; refusals and
uncertain delivery or completion are recorded separately. A dispatch-start
record precedes prompt delivery and does not prove Cursor received it.
Recorded admission does not establish execution or durable resumability.
The transcript is separate from Cursor's native session storage.
After rotation, `get`'s current session and `prior_sessions` can reference this
same file within the current life. Same-agent reuse keeps prior references
inspectable; `prior_sessions` excludes the new current session. Individual records retain their known
conversation identity; provisionally held or refused input has no admitted
conversation. Rotation requested, fresh identity confirmed and publication are separate
records. Known retired traffic retains its outgoing attribution; unknown origin
stays unknown. Resume continues the current session's protected file; older
references remain available for inspection only. Load replay adds no duplicate
messages, decisions or history entries to that file.
A path appears only after it is recorded by an accepted startup report;
`transcript_status` describes the recorded reference, not a fresh file check.
Availability and persistence failures are covered by the
[caller contract](caller-contract.md#cursor-operation-boundaries).

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

An agent that supervises other agents calls agent-director over MCP:

- **A Claude Code caller:** install with `--register-mcp`. The verbs then appear
  as `mcp__agent-director__spawn`, `mcp__agent-director__send_keys`, etc.; `hook`,
  `serve`, `trail-emit` and the internal `cursor-host` gateway are CLI-only. Tool arguments use underscores
  (`--instance-id` is `instance_id`); the list is in
  `mcp-reference.md`.
- **A Codex caller:** follow [Set up agent-director in a Codex
  folder](settings.md#set-up-agent-director-in-a-codex-folder) in each Codex
  folder the caller runs in. Codex names the tools `mcp__<server>__<tool>`, with
  `<server>` the name its `config.toml` gives the server, and by default asks
  before each call (see [What is relayed](permissions.md#what-is-relayed)).
- **A managed Cursor caller:** enable `defaults.inject_help_hook` for the
  session-scoped ACP stdio MCP setup described in
  [Cursor launch settings](settings.md#cursor-launch-settings). It supplies the
  AD tools with explicit home, store, parent id and tmux routing.

A fresh child records `AGENT_DIRECTOR_INSTANCE_ID` from its caller as
`parent_id` and selects the caller's intended tmux server from `TMUX`, then
`TMUX_TMPDIR` or the normal per-user default when no `TMUX` socket is present.
Resume and finished-id reuse prefer the row's recorded socket. Inspect a child
with `get --instance-id <child-id>` and find associated children with
`list --parent <parent-id>`. Parent association does not authenticate a caller
or grant authority over its parent.

For shell CLI calls, preserve the caller id and intended tmux selection.
When using an override store, pass `--store-path` explicitly on every call;
`--home` selects HOME for configuration and default paths, and does not identify
an arbitrary override store. For example, from a caller shell with those
variables available:

```sh
agent-director --home "$HOME" --store-path /srv/ad/state.db spawn --cwd "$PWD" --agent cursor
agent-director --home "$HOME" --store-path /srv/ad/state.db list --parent "$AGENT_DIRECTOR_INSTANCE_ID"
```

Use the actual store path in place of `/srv/ad/state.db`. The private
`AGENT_DIRECTOR_CURSOR_STORE_PATH` bootstrap variable is not a public CLI store
override. Cursor shell access depends on its sandbox; see the
[caller contract](caller-contract.md#cursor-operation-boundaries).

MCP is the recommended path for a Codex caller. A Codex
agent's own shell reaches agent-director's CLI, and the Go and TypeScript
clients, only where Codex's sandbox lets it: in Codex's `workspace-write`
sandbox, with both of these in the folder's `config.toml`, or with full access:

```toml
[sandbox_workspace_write]
writable_roots = ["/home/you/.config/agent-director"]
network_access = true
```

`writable_roots` names agent-director's directory, plus any directory that
holds a `[store] db_path` or `[log] error_log_path` outside it (replace
`/home/you` with your home directory); `network_access` is needed for the tmux
socket. The shell must also pass the caller's variables: Codex's default
`shell_environment_policy` does, `inherit = "core"` and `"none"` can drop them,
and an `include_only` list must name `AGENT_DIRECTOR_*` and `TMUX*`.

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

`list-subagents` returns the subagents of an agent's current life, one record
each, and whether the agent is tracked. It is `mcp__agent-director__list_subagents`
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

`subagent_id` is unique among the records shown for an agent. Within the
same life, a caller can match records by `(instance_id, subagent_id)` across
reads and launches. A new life can record the same subagent id separately.

### When an agent is tracked

`tracked` is `true` when the agent's current launch registered its subagent
start and stop events, so its subagents are recorded. It is `false`, with
`subagents` `[]`, for an agent that predates subagent tracking; it becomes
`true` when that agent is next resumed or its id is reused. The hooks that
write the records are in [Hooks](hooks.md#what-the-hooks-write-for-subagent-records).
A Cursor row always reads `tracked: false` with `subagents: []`, including
after reuse, `/clear`, `/new` and switches. Retained records from earlier
launches are not exposed or fabricated by Cursor; these records are separate
from the independent AD child spawns returned by `list --parent`.
A Codex agent's hooks write no records, so its row reads `tracked: false` and
its `subagents` stays `[]`, from its spawn and after every resume or reuse of
its id.

`subagents` is never null. It holds the records across launches of the agent's
current life. Earlier lives' records remain stored but are hidden. The list is
ordered by `first_started_at` ascending (records with a null
`first_started_at` last), then by `subagent_id`.

### The record's fields

Every record carries all of these keys, in this order. A field that nothing
supplied is `null`, never a guess.

| Field | Type | Meaning |
|---|---|---|
| `subagent_id` | string | The subagent's id, unique among the agent's visible records. |
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
  [Hooks](hooks.md#the-eleven-state-tracking-hooks)). A Codex row becomes
  `ended` once `find-missing` or `pause` sees its agent gone (see
  [How a Codex row ends](hooks.md#how-a-codex-row-ends)).
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

An agent keeps at most 300 entries across all lives: its records plus starts
that nothing has confirmed, which are never shown. Hidden earlier-life entries
count toward this limit. When a new entry arrives at the limit, one is
evicted first: a closed unconfirmed start, else a closed record, else a
`running` entry only when nothing closed remains; within each, the one changed
longest ago goes first. A `resume` or reuse keeps every record, the `running`
ones closed as above, so none of an earlier launch reads `running`. Visibility
still depends on the row's current life. They are
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

The same inspection and decision commands serve validated Cursor permissions
and blocking plans; select the intended token by its actual request content,
rather than assuming the oldest request is the visible menu. Cursor's input
and lifetime rules are in [Permissions](permissions.md#cursor-spawns).

`decide` records the verdict, then waits at most 1 s for the relay
acknowledgement, and returns the request's `delivery`:

- `delivered`: the relay durably acknowledged the verdict before output;
  this does not establish runtime receipt, execution or dialog closure.
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

`decision_reason` annotates the recorded verdict: `operator` (API deny),
`timeout` (relay timeout deny), `find_missing`/`ended` (owning-spawn closure),
`pane` (`send-keys` or a native Cursor choice), `pane_outside` (outside claim),
or `tool_ran` (tool execution). API allow initially has no reason; later
validated Cursor execution can fill that missing reason with `tool_ran`,
while preserving an existing verdict and reason. Read `decision` for the
verdict and the [delivery and proof fields](permissions.md#delivery) for its
outcome; a reason is not an immutable writer identity.

`get` and `list` carry the same delivery fields on each request in a row's
`permission_requests`; they list only requests that still await an answer,
never a closed one. `hook_alive` is `null` when agent-director cannot check
the relay hook's process (for example from another pid namespace, for a
Cursor gateway request, or for a request recorded by an older agent-director);
such a request falls back by time, at `confirm_by`. A closed request's `delivery` reads the same way as
any other's: `delivered` only after durable relay acknowledgement. A row
can read `waiting`, `working` or `ask_user` while a request it recorded
still awaits an answer, so follow each request you track with
`get-permission`, not only rows in `check_permission`.

`proven_gone_at` and `proven_gone_how` say when and how the agent itself
showed the request's dialog gone: `tool_ran` (Claude tool execution or
validated matching Cursor shell execution), `turn_end` (an eligible main request
was written before the main turn ended; see [The dialog hold](permissions.md#the-dialog-hold)) or
`agent_gone` (the agent ended). A Codex agent's request carries
no `tool_use_id` (it reads `null`) and is never proven by `tool_ran`; see
[how the hold differs on Codex rows](permissions.md#how-the-hold-differs-on-codex-rows). `delivered` and a pane answer are
agent-director's own records, not proof. `get` lists every request not yet
proven gone in `unproven_requests`, with `unproven_since`. A request that
reads `delivered` or closed at the pane but stays unproven for a while is a
reason to look at the pane: its dialog may still be there.

## Answer a request at the pane

For a connected Cursor request, use its displayed key, without Enter. Each
menu names its `request_token` and supplies distinct keys; match that token
and option to the request you inspected. At the attached pane a deliberate
key competes with an active relay decision. A successful attached Cursor
choice automatically records `pane_answer=sent` and `decision_reason=pane`;
no `record-pane-answer` call is needed. That claim is not ACP delivery,
Cursor receipt or execution proof. Through `send-keys`, use the
existing guarded pane-answer command below once delivery has fallen back,
substituting the displayed key for `Escape` and the chosen verdict for `deny`.
Relay-off requests expose this path immediately, while `decide` remains
refused. API allow is once-only; a persistent native option requires an
explicit choice of that offered option. See [Cursor permissions](permissions.md#cursor-spawns)
for the fixed relay/native lifetimes and retirement rules.

A request with `delivery=fallen_back` has no confirmed relay answer.
A pane answer can close it; validated execution proof or owning-spawn closure
can also resolve it. To answer an open connected request, inspect its pane
and send the one key for that request:

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

If an outside answer has genuinely not been recorded, use
`record-pane-answer` (it types nothing). An attached Cursor menu choice is
already recorded automatically:

```sh
agent-director record-pane-answer --request-token "$token" \
    --as unknown --expect-pane-sha256 "$hash"
```

Pass `--as unknown` unless you saw how it was answered. On a Claude agent, a
request whose tool then runs is closed on its own (`decision_reason`
`tool_ran`); a Codex request is closed only by one of the two calls above.
A Cursor request can close on validated matching shell execution as well;
other tool/plan completion supplies no execution proof. `record-pane-answer`
types nothing and neither authorizes Cursor approval nor proves a dialog gone.
A Codex pane's keys and an example are in
[Answering a Codex approval at the pane](permissions.md#answering-a-codex-approval-at-the-pane). A
closed request keeps `delivery` `fallen_back`; its `pane_answer` (`sent`,
`outside` or `tool_ran`) says how it was closed.

For plain typing, `send-keys` also accepts `--no-enter`, `--key` and
`--expect-pane-sha256`. Permission holds and stopping a held agent are in
the [caller contract](caller-contract.md#permission-relay). Never pass a
pane hash from an automatic flow: it means a person or an LLM judged that
exact screen.

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

Cursor supports the eligible pause described in [Fresh Cursor use](#fresh-cursor-use)
and latest-session resume. Fresh reuse and switches follow the
[life rules](#naming-the-agent); recovery rules are in the
[caller contract](caller-contract.md#cursor-operation-boundaries).

`pause` asks a `waiting` agent to exit and keeps its row. `resume` relaunches
it under the same id with the last agent's latest reported session, so the
conversation carries on if that agent can reopen it. `resume` accepts no
agent or session choice. Older sessions of the current life remain inspectable
in `get`'s `prior_sessions`, but agent-director never resumes them, even when
their transcript files exist. Same-agent reuse starts a fresh conversation;
its next `resume` continues that new session. Agent-director adds no in-session
picker and does not control the agent's own picker; its resume guarantee applies
to launches it manages. See [Transcript recovery](caller-contract.md#transcript-recovery)
for refusals and recovery.

For Cursor, resume keeps the saved arguments, environment, cwd, group labels,
session and protected transcript, using the current configured `cursor_path`.
Retaining those values does not establish account or group isolation. Loading
the latest conversation can leave the row `pending` until its first real
prompt. After inspecting the pane, submit that prompt with the existing
`--allow-pending`; confirmation of the current conversation and all permission
holds still apply. Loading and replay do not manufacture `waiting`.
The saved initial prompt in `agent_args` is submitted once per launch after
successful load and hold checks. Transient queued, provisionally held and
editable draft text are not restored. Session-selector filtering is defined in
the [Cursor caller contract](caller-contract.md#cursor-operation-boundaries).

`resume` replays stored `agent_args` except recognized session selectors and
their values, and Claude's `-p` / `--print`, so those options cannot replace
the latest session or request print mode. A value or prompt that spells a
selector is preserved. When the original Claude arguments include every
recognized required value, surviving options keep their meaning and owned
values. Arguments keep their order and spelling unless removal would change
their meaning. Only then, prompt tokens move behind surviving options and
`--`, or an altered short-option cluster is split or re-encoded to preserve
its options and values. Prompt tokens keep their exact text and order.
If any recognized required Claude scalar or list option originally lacks its value,
filtering only deletes the selected options, owned values and cluster bytes;
it preserves the surviving order and spelling, without moving prompts,
inserting `--` or re-encoding clusters. The agent's own parser handles rejection.
Claude's `--forward-home-settings`, `--ref` and `--on-branch`, with their
values, are also removed when removing an actual `--cloud` / `--remote`
leaves no parsed `--environment` / `--pool` option with its required value.
They remain when that context survives, or when no cloud selector was removed.
The stored list is unchanged. Codex prompt text remains an original argument
after `--`, and today's per-launch settings take precedence over replayed
settings. `resume` does not apply spawn's argument refusals; see the
[argument recognition limits](caller-contract.md#spawn-and-template-input-refusals).
A resumed Claude row stays `pending` until Claude reports SessionStart,
then becomes `waiting`; startup or trust waits and missed hooks can prolong
`pending`. A resumed Codex row reports in
only at its first prompt, so it reads `pending` until then and `pause` and
`send-keys` without `allow_pending` are refused (see
[Spawn states](#spawn-states); once the agent has exited with an end report,
`pause` ends the row instead, see the
[caller contract](caller-contract.md#when-a-codex-agent-exits)).

```sh
agent-director pause --instance-id "$id"
agent-director resume --instance-id "$id"
```

On a Codex row `pause` clears a draft of up to 64 lines before it types
`/exit`, and ends the row itself once the agent exits (see the
[caller contract](caller-contract.md#when-a-codex-agent-exits), which also
covers drafts exceeding that limit).
