# Usage

Every verb prints JSON on stdout and a typed error on stderr. The full verb
and flag list is in `cli-reference.md`.

## Spawn states

A row is `pending` while its launch is in progress and its agent has not
reported in yet (it may be loading or waiting at a startup prompt); it
becomes `waiting` once the agent reports in. If the agent runs but never
reports in, the row stays `pending` and a scheduled `find-missing` notes
it `unreported` (see [Maintenance](maintenance.md)).

`spawn` marks the folder as trusted so the agent does not stop at Claude
Code's folder-trust prompt; add `--no-pre-trust` to keep the prompt.

## Naming the tmux session

`spawn --tmux-session-name <name>` picks a readable session name instead
of the default `<basename(cwd)>-<id[:8]>`:

```sh
agent-director spawn --cwd "$PWD" --tmux-session-name status-agent
```

The name must be non-empty, ≤ 64 bytes, valid UTF-8, and contain none of
`#`, `:`, `.`, `$`, `\`, or ASCII control bytes (`\x00`–`\x1f`, `\x7f`);
nothing is rewritten. Name reuse across **ended** spawns is supported. A
name already held by a tmux session makes `spawn` return an error naming
the blocking session and whether its label carries this instance id; the
new row is ended and agent-director never touches the holder.

## Finding a Spawn by its tmux session name

```sh
agent-director list --tmux-session-name status-agent
```

The filter is exact-match, AND-combines with `--state`, `--label`,
`--parent`, `--cwd` and `--limit`, and returns both live and ended rows
whose `tmux_session_name` matches, so one name can match several rows.

## Drive a Spawn from another Claude

Install with `--register-mcp`. Inside the orchestrating Claude the
verbs appear as `mcp__agent-director__spawn`,
`mcp__agent-director__send_keys`, etc. The process-internal verbs `hook`,
`serve` and `trail-emit` are CLI-only; the tool list is in
`mcp-reference.md`.

Tool arguments are the parameter names with underscores
(`--claude-instance-id` → `claude_instance_id`, `--reuse-finished` →
`reuse_finished`). A tool refuses any other argument, or a value of the
wrong type, with `ErrInvalidFlags` and does nothing.

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

After the row is closed, retrieve the verdict by `request_token`:

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
  "decided_at": "2026-05-31T12:00:01Z"
}
```

## Templates

```sh
agent-director make-template --name dev --cwd /repos/widget \
    --label project=widget --allow 'Bash(npm test)'

agent-director spawn --template dev --label run=$(date +%s)
```

Pass `--overwrite` to replace an existing template atomically; without it,
an existing template causes `ErrTemplateExists`.

## Pause and resume

`pause` asks a `waiting` agent to exit and keeps its row. `resume`
relaunches it under the same id with its JSONL transcript, so the
conversation carries on.

```sh
agent-director pause --claude-instance-id "$id"
agent-director resume --claude-instance-id "$id"
```
