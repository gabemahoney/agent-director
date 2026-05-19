# claude-director

Supervise long-running Claude Code sessions ("Spawns") from a script,
another Claude, or an MCP-capable LLM client. One Go binary; one
SQLite file; everything else is tmux.

## What you get

- A **CLI** with one verb per supervision action — deterministic JSON
  on stdout, typed errors on stderr.
- An **MCP server** (`serve --stdio`) that exposes the same verbs to
  an LLM client.
- A **permission relay** (`relay_mode=on` + `decide`) so an orchestrator
  can intercept `PreToolUse` permission prompts and answer
  allow/deny out-of-band.
- A **persistent session model** — pause / resume preserves the
  JSONL transcript across Claude sessions.
- A **crash-recovery cron** — `find-missing` + `expire` reconcile the
  DB against actually-live processes.

## 5-minute install

### Prerequisites

- `claude` (Claude Code) on PATH — install per
  <https://claude.com/claude-code>.
- `tmux` 3.0+ on PATH.
- `jq` on PATH.

### Install

```sh
make build
skills/install-claude-director/install.sh
```

Optional flags:

- `--register-mcp` — register the stdio MCP server with Claude Code.
- `--symlink-dir <dir>` — override the default PATH-symlink directory.
- `--binary <path>` — install from an explicit source binary.
- `--from-release [tag]` — download a pre-built binary for this host's
  OS/arch from GitHub Releases (latest, or a specific tag) and install
  it. Pair with `--sha256 <hex>` to verify the download.

<!-- TODO(b.xor): once v0.1.0 is published to GitHub Releases, replace
     the lead `make build` snippet above with the curl one-liner:

       curl -fsSL https://raw.githubusercontent.com/gabemahoney/claude-director/main/skills/install-claude-director/install.sh \
         -o /tmp/install-claude-director.sh
       bash /tmp/install-claude-director.sh --from-release

     The from-source snippet stays as a secondary option for
     contributors and operators who want to build from a checkout. -->


### First spawn

```sh
id=$(claude-director spawn --cwd "$PWD" | jq -r '.claude_instance_id')

claude-director list --state waiting

claude-director send-keys --claude-instance-id "$id" --text "what is 2+2?"

claude-director read-pane --claude-instance-id "$id"

claude-director pause --claude-instance-id "$id"
```

## Common workflows

### Drive a Spawn from another Claude

Install with `--register-mcp`. Inside the orchestrating Claude the
verbs appear as `mcp__claude-director__spawn`,
`mcp__claude-director__send_keys`, etc.

### Intercept permission prompts

```sh
id=$(claude-director spawn --cwd "$PWD" --relay-mode=on \
       | jq -r '.claude_instance_id')

# When Claude hits a PermissionRequest, state goes to check_permission.
claude-director decide --claude-instance-id "$id" \
    --decision allow --reason "tool is on the allow-list"
```

### Templates

```sh
claude-director make-template --name dev --cwd /repos/widget \
    --label project=widget --allow 'Bash(npm test)'

claude-director spawn --template dev --label run=$(date +%s)
```

## Configuration

`~/.claude-director/config.toml` (created on first run; all fields
optional):

```toml
[defaults]
relay_mode = "off"
expire_retention_days = 31
disable_askuserquestion = false

[relay]
poll_base_ms = 100
poll_jitter_ms = 100
timeout_seconds = 600

[pause]
timeout_seconds = 30

[store]
db_path = "~/.claude-director/state.db"

[log]
error_log_path = "~/.claude-director/errors.log"
```

## Uninstall

```sh
skills/install-claude-director/uninstall.sh           # preserve state.db + templates
skills/install-claude-director/uninstall.sh --purge   # remove everything
```
