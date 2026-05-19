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

#### From inside Claude Code (recommended)

If you already have Claude Code running, just say:

> **install claude-director**

That triggers the `install-claude-director` skill, which walks you
through four choices interactively (binary source, PATH symlink,
MCP registration, persistent help hooks) and then runs `install.sh`
with the resolved flags. Each question explains itself — assume no
prior knowledge — so a new user can answer without reading anything
else first.

The skill ships in this repo at
[`skills/install-claude-director/`](skills/install-claude-director/SKILL.md)
and is auto-discoverable by Claude Code if you've cloned the repo
under a directory it indexes. If not, point Claude Code at the
SKILL.md once and from then on the trigger phrase works.

#### From a shell (no Claude Code)

`install.sh --from-release` auto-detects your OS/arch and downloads
the matching binary from GitHub Releases:

```sh
curl -fsSL https://raw.githubusercontent.com/gabemahoney/claude-director/main/skills/install-claude-director/install.sh \
  -o /tmp/install-claude-director.sh
bash /tmp/install-claude-director.sh --from-release
```

The script sets up `~/.claude-director/`, drops a PATH symlink, warms
up `state.db`, and installs the SessionStart/SessionEnd help hooks.
Optionally pass `--register-mcp` to also register the stdio MCP
server, or `--no-hooks` to leave `~/.claude/settings.json` untouched.

If you'd rather download the binary yourself first (and then point
the installer at it), grab the asset for your platform from the
[latest release](https://github.com/gabemahoney/claude-director/releases/latest):

```sh
# Linux amd64:
curl -L -o claude-director https://github.com/gabemahoney/claude-director/releases/latest/download/claude-director-linux-amd64
# Linux arm64:
curl -L -o claude-director https://github.com/gabemahoney/claude-director/releases/latest/download/claude-director-linux-arm64
# macOS Intel:
curl -L -o claude-director https://github.com/gabemahoney/claude-director/releases/latest/download/claude-director-darwin-amd64
# macOS Apple Silicon:
curl -L -o claude-director https://github.com/gabemahoney/claude-director/releases/latest/download/claude-director-darwin-arm64

chmod +x claude-director
bash skills/install-claude-director/install.sh --binary ./claude-director
```

Optional flags:

- `--register-mcp` — register the stdio MCP server with Claude Code.
- `--symlink-dir <dir>` — override the default PATH-symlink directory.
- `--binary <path>` — install from an explicit source binary.
- `--from-release [tag]` — download a pre-built binary for this host's
  OS/arch from GitHub Releases (latest, or a specific tag) and install
  it. Pair with `--sha256 <hex>` to verify the download.

#### From source (contributors)

If you've cloned this repo and want to install the binary you just
built:

```sh
make build
bash skills/install-claude-director/install.sh --binary ./bin/claude-director
```

`install.sh` version-checks the local binary against `git rev-parse HEAD`
and refuses option `--binary` if the artifact is stale — re-run
`make build` to refresh it.


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
