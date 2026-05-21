# agent-director

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

> **install agent-director**

That triggers the `install-agent-director` skill, which walks you
through four choices interactively (binary source, PATH symlink,
MCP registration, persistent help hooks) and then runs `install.sh`
with the resolved flags. Each question explains itself — assume no
prior knowledge — so a new user can answer without reading anything
else first.

The skill ships in this repo at
[`skills/install-agent-director/`](skills/install-agent-director/SKILL.md)
and is auto-discoverable by Claude Code if you've cloned the repo
under a directory it indexes. If not, point Claude Code at the
SKILL.md once and from then on the trigger phrase works.

#### From a shell (no Claude Code)

`install.sh --from-release` auto-detects your OS/arch and downloads
the matching binary from GitHub Releases:

```sh
curl -fsSL https://raw.githubusercontent.com/gabemahoney/agent-director/main/skills/install-agent-director/install.sh \
  -o /tmp/install-agent-director.sh
bash /tmp/install-agent-director.sh --from-release
```

The script sets up `~/.agent-director/`, drops a PATH symlink, warms
up `state.db`, and installs the SessionStart/SessionEnd help hooks.
Optionally pass `--register-mcp` to also register the stdio MCP
server, or `--no-hooks` to leave `~/.claude/settings.json` untouched.

If you'd rather download the binary yourself first (and then point
the installer at it), grab the asset for your platform from the
[latest release](https://github.com/gabemahoney/agent-director/releases/latest):

```sh
# Linux amd64:
curl -L -o agent-director https://github.com/gabemahoney/agent-director/releases/latest/download/agent-director-linux-amd64
# Linux arm64:
curl -L -o agent-director https://github.com/gabemahoney/agent-director/releases/latest/download/agent-director-linux-arm64
# macOS Intel:
curl -L -o agent-director https://github.com/gabemahoney/agent-director/releases/latest/download/agent-director-darwin-amd64
# macOS Apple Silicon:
curl -L -o agent-director https://github.com/gabemahoney/agent-director/releases/latest/download/agent-director-darwin-arm64

chmod +x agent-director
bash skills/install-agent-director/install.sh --binary ./agent-director
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
bash skills/install-agent-director/install.sh --binary ./bin/agent-director
```

`install.sh` version-checks the local binary against `git rev-parse HEAD`
and refuses option `--binary` if the artifact is stale — re-run
`make build` to refresh it.


### First spawn

```sh
id=$(agent-director spawn --cwd "$PWD" | jq -r '.claude_instance_id')

agent-director list --state waiting

agent-director send-keys --claude-instance-id "$id" --text "what is 2+2?"

agent-director read-pane --claude-instance-id "$id"

agent-director pause --claude-instance-id "$id"
```

#### Naming the tmux session yourself

`spawn` accepts `--tmux-session-name <name>` so Slack-channel bots,
test harnesses, or manual-debug operators can pick a readable session
name instead of the default `<basename(cwd)>-<id[:8]>`:

```sh
agent-director spawn --cwd "$PWD" --tmux-session-name bot-claude-status
```

Rules (validated app-side, no silent rewrite): the name must be
non-empty, ≤ 64 bytes, valid UTF-8, and contain none of `#`, `:`,
`.`, or ASCII control bytes (`\x00`–`\x1f`, `\x7f`). There is no DB
uniqueness check — name reuse across **ended** spawns is supported.
A collision against a currently-live tmux session surfaces as
tmux's own `new-session` error (no app-layer sentinel). Omitting
the flag preserves today's `composeSessionName` default.

#### Finding a Spawn by its tmux session name

`list` accepts `--tmux-session-name <name>` to narrow rows by the
column verbatim — useful when the operator picked the name (above)
and wants to round-trip back from `tmux ls` to the persisted Spawn
without consulting the id:

```sh
agent-director list --tmux-session-name bot-claude-status
```

The filter is exact-match, AND-combines with `--state`, `--label`,
`--parent`, `--cwd`, and `--limit`, and returns both live and ended
rows whose `tmux_session_name` matches — name reuse across ended
spawns is supported, so a single name can correlate to multiple
historic rows. Omitting the flag preserves today's permissive
behavior.

## Common workflows

### Drive a Spawn from another Claude

Install with `--register-mcp`. Inside the orchestrating Claude the
verbs appear as `mcp__agent-director__spawn`,
`mcp__agent-director__send_keys`, etc.

### Intercept permission prompts

```sh
id=$(agent-director spawn --cwd "$PWD" --relay-mode=on \
       | jq -r '.claude_instance_id')

# When Claude hits a PermissionRequest, state goes to check_permission.
agent-director decide --claude-instance-id "$id" \
    --decision allow --reason "tool is on the allow-list"
```

### Templates

```sh
agent-director make-template --name dev --cwd /repos/widget \
    --label project=widget --allow 'Bash(npm test)'

agent-director spawn --template dev --label run=$(date +%s)
```

## Configuration

`~/.agent-director/config.toml` (created on first run; all fields
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
db_path = "~/.agent-director/state.db"

[log]
error_log_path = "~/.agent-director/errors.log"
```

## Uninstall

```sh
skills/install-agent-director/uninstall.sh           # preserve state.db + templates
skills/install-agent-director/uninstall.sh --purge   # remove everything
```
