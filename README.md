# agent-director

Supervise long-running Claude Code sessions ("Spawns") from a script,
another agent, or an MCP-capable LLM client. One Go binary, one SQLite
file, and tmux.

## What you get

- A **CLI** with one verb per supervision action: JSON on stdout, typed
  errors on stderr.
- An **MCP server** (`serve --stdio`) that gives the same verbs to an LLM
  client.
- A **Go library** and a **TypeScript/Bun client** with the same API.
- A **permission relay**: answer a Spawn's permission prompts allow/deny
  from outside, and learn whether the answer reached the agent.
- **Pause and resume** that keep the conversation.
- **Sweep verbs** you schedule, `find-missing` and `expire`, that keep the
  row states honest.

## Prerequisites

- Linux x86_64 or Apple Silicon (arm64) macOS.
- `claude` (Claude Code) 2.1.280 or later on PATH: the native binary, or a
  wrapper that `exec`s it.
- `tmux` 3.2 or later on PATH.
- `jq`, `sqlite3` and `file` on PATH.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/gabemahoney/agent-director/main/skills/install-agent-director/install.sh | bash -s -- --from-release
```

This installs `agent-director` on your PATH and creates
`~/.agent-director/`. Add `--register-mcp` to also register the MCP server
with Claude Code.

## First spawn

```sh
id=$(agent-director spawn --cwd "$PWD" | jq -r '.instance_id')

agent-director list --state waiting

agent-director send-keys --instance-id "$id" --text "what is 2+2?"

agent-director read-pane --instance-id "$id"

agent-director pause --instance-id "$id"
```

The row is `pending` until the agent reports in, then `waiting`.

## Keep it healthy

Schedule these with cron, a systemd timer or launchd, as the agents' user:

```sh
agent-director find-missing   # often, e.g. every 2 minutes
agent-director expire
```
