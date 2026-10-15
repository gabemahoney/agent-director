# agent-director

Supervise long-running Claude Code and Codex sessions ("Spawns") from a
script, another agent, or an MCP-capable LLM client. One Go binary, one SQLite
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
- **Sweep verbs**, `find-missing` and `expire`, that keep the row states
  honest. The install can schedule them for you.

## Prerequisites

- Linux x86_64 or Apple Silicon (arm64) macOS.
- At least one of Claude Code and Codex, each on PATH or named by `[launch]
  claude_path` / `codex_path` in `config.toml`. For Claude Code, `claude` (Claude
  Code) 2.1.280 or later: the native binary, or a wrapper that `exec`s it. For
  Codex, `codex` (Codex) 0.159.2 or later: its native program, or a wrapper that
  `exec`s it. An npm-installed `codex` does not qualify: set `[launch]
  codex_path` in `~/.config/agent-director/config.toml` to the native program.
- `tmux` 3.2 or later on PATH.
- `jq`, `sqlite3` and `file` on PATH.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/gabemahoney/agent-director/main/skills/install-agent-director/install.sh | bash -s -- --from-release
```

This installs `agent-director` on your PATH and creates
`~/.config/agent-director/`. Add `--register-mcp` to also register the MCP server
with Claude Code, and `--schedule` to also schedule the two sweeps (see
"Keep it healthy"). The install registers nothing with Codex.

## First spawn

```sh
agent=claude   # or codex
id=$(agent-director spawn --cwd "$PWD" --agent "$agent" -- "what is 2+2?" | jq -r '.instance_id')

agent-director list --state waiting

agent-director read-pane --instance-id "$id"

agent-director pause --instance-id "$id"
```

The first prompt is one argument after `--`. The row is `pending` until the
agent reports in (a Codex agent does at its first prompt), `working` while it
answers, and `waiting` once it has answered.

## Keep it healthy

Two sweeps need a schedule, as the agents' user; the install's `--schedule` sets
both up:

```sh
agent-director find-missing   # often, e.g. every 2 minutes
agent-director expire         # e.g. daily
```

`find-missing` keeps row states honest when an agent is killed, crashes, or is
lost in a reboot: it marks the row `missing` (or `ended` for a Codex agent that
reported its exit) instead of leaving it `working` or `waiting`. Run it often.

`expire` deletes finished rows older than the retention window (31 days by
default), so the store does not grow forever. Running it daily is enough.

See [Maintenance](docs/maintenance.md) for details.
