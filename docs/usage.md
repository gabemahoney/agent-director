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

## Intercept permission prompts

With `--relay-mode=on`, a Claude permission prompt moves the row to
`check_permission` and waits for your `decide`:

```sh
id=$(agent-director spawn --cwd "$PWD" --relay-mode=on \
       | jq -r '.claude_instance_id')

token=$(agent-director get --claude-instance-id "$id" \
          | jq -r '.permission_requests[0].request_token')
agent-director decide --claude-instance-id "$id" \
    --request-token "$token" \
    --decision allow --reason "tool is on the allow-list"
```

After the row is closed, fetch the verdict by `request_token` (`tool_name`,
`tool_input`, `decision`, `decision_reason` and the request and decision
times):

```sh
agent-director get-permission --request-token "$token"
```

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
