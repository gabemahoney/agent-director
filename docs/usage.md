# Usage

Every verb prints JSON on stdout and a typed error on stderr. The full verb and
flag list is in `cli-reference.md`.

## Spawn states

A row is `pending` while its launch is in progress and its agent has not
reported in; it becomes `waiting` once it does. If the agent runs but never
reports in, `find-missing` notes the row `unreported` (see
[Maintenance](maintenance.md)).

`spawn` marks the folder as trusted so the agent does not stop at Claude
Code's folder-trust prompt. `--no-pre-trust` keeps the prompt, and holds for
every `resume` of the id: none marks the folder, so the agent may wait at the
prompt.

## Naming the tmux session

`spawn --tmux-session-name <name>` replaces the default session name
`<basename(cwd)>-<id[:8]>`:

```sh
agent-director spawn --cwd "$PWD" --tmux-session-name status-agent
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

## Drive a Spawn from another Claude

Install with `--register-mcp`. The verbs then appear as
`mcp__agent-director__spawn`, `mcp__agent-director__send_keys`, etc.; `hook`,
`serve` and `trail-emit` are CLI-only. Tool arguments use underscores
(`--claude-instance-id` is `claude_instance_id`); the list is in
`mcp-reference.md`.

## Watch a Spawn's state

With a [settle time](configuration.md#settle-time-state) set, `state` ignores
flips shorter than it, so a script that polls `status` can act on the first
change it sees, with no count of identical polls:

```sh
last=""
while sleep 1; do
  state=$(agent-director status --claude-instance-id "$id" | jq -r .state)
  if [ "$state" != "$last" ]; then
    last=$state
    case "$state" in waiting|ask_user|check_permission|ended|missing) echo "$id: $state" ;; esac
  fi
done
```

It never sees a turn shorter than the settle time.

## Intercept permission prompts

With `--relay-mode=on`, a Claude permission prompt moves the row to
`check_permission` and waits for your `decide`:

```sh
id=$(agent-director spawn --cwd "$PWD" --relay-mode=on \
       | jq -r '.claude_instance_id')

token=$(agent-director get --claude-instance-id "$id" \
          | jq -r '.permission_requests[0].request_token')
# A caller that must answer within 3 s passes at most 2000 (its deadline minus 1 s).
agent-director decide --claude-instance-id "$id" \
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
pane=$(agent-director read-pane --claude-instance-id "$id")
echo "$pane" | jq -r '.pane'
hash=$(echo "$pane" | jq -r '.pane_sha256')

agent-director send-keys --claude-instance-id "$id" \
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
agent-director send-keys --claude-instance-id "$id" --text /exit \
    --expect-pane-sha256 "$hash"
```

Never pass `--expect-pane-sha256` from an automatic flow: it says that a
person or an LLM judged that exact screen, and passed blindly it lets
through the Enter the hold exists to stop.

## Templates

```sh
agent-director make-template --name dev --cwd /repos/widget \
    --label project=widget --allow 'Bash(npm test)'

agent-director spawn --template dev --label run=$(date +%s)
```

`--overwrite` replaces an existing template atomically.

## Pause and resume

`pause` asks a `waiting` agent to exit and keeps its row. `resume` relaunches
it under the same id with its transcript, so the conversation carries on.

```sh
agent-director pause --claude-instance-id "$id"
agent-director resume --claude-instance-id "$id"
```
