# claude-director

Supervise long-running Claude Code sessions ("Spawns") from a script,
another Claude, or an MCP-capable LLM client. One Go binary; one
SQLite file; everything else is tmux.

> *Status: in development.* Core verbs (spawn, status, send-keys,
> read-pane, kill, pause, list, decide, resume, find-missing,
> expire, delete, make-template, serve) are implemented and gated
> by an in-container Docker harness. v1 release skill (Epic 13) is
> next.

## What you get

- A **CLI** with one verb per supervision action — readable from a
  shell script, deterministic JSON on stdout, typed errors on stderr.
- An **MCP server** (`serve --stdio`) that exposes the same verbs to
  an LLM client. Adding a verb to the manifest exposes it through
  both surfaces — drift is structurally impossible.
- A **permission relay** (`relay_mode=on` + `decide`) so an
  orchestrator can intercept `PreToolUse` permission prompts and
  decide allow/deny out-of-band. Fail-closed by design — see
  [docs/permissions.md](docs/permissions.md).
- A **persistent session model** — pause → resume preserves the
  JSONL transcript context across Claude sessions (Epic 9).
- A **crash-recovery cron** — `find-missing` + `expire` reconcile
  the DB against actually-live processes (Epic 8). The degraded-mode
  guard refuses to write when the cron is running as the wrong user.

## 5-minute install

### Prerequisites

- `claude` (Claude Code) on PATH — install per
  <https://claude.com/claude-code>.
- `tmux` 3.0+ on PATH.
- `jq` on PATH (used only by the install script to safely edit
  `~/.claude/settings.json`).

### Install

**From a release** (recommended once `v0.1.0` ships):

```sh
# Pick the binary for your platform:
#   linux-amd64, linux-arm64, darwin-amd64, darwin-arm64
curl -L -o claude-director \
    https://github.com/<owner>/<repo>/releases/latest/download/claude-director-linux-amd64
chmod +x claude-director

# Run the install skill with --binary pointing at the download:
skills/install-claude-director/install.sh --binary ./claude-director
```

**From source** (during development):

```sh
# Build the binary (Go 1.22+):
make build

# Run the install skill:
skills/install-claude-director/install.sh
```

The four supported targets (`linux/amd64`, `linux/arm64`,
`darwin/amd64`, `darwin/arm64`) ship as statically-linked binaries —
no glibc dependency on Linux, no library dance on macOS. Windows
is not supported.

The script:

- Creates `~/.claude-director/` (mode 0700) with `bin/` for the
  binary and a fresh `state.db` (mode 0600).
- Copies the binary to `~/.claude-director/bin/claude-director` via
  a versioned-path + symlink swap (so upgrades don't break live
  Spawns).
- Drops a `~/.local/bin/claude-director` symlink if `~/.local/bin`
  is on PATH.
- Injects two persistent hook entries into
  `~/.claude/settings.json`:
  - On `SessionStart`: runs `claude-director help` so a fresh
    conversation knows the supervision API surface.
  - On `SessionEnd` with `matcher: compact`: same, so the verb list
    survives `/compact`.

Existing user hooks in those events are preserved — the merge is
strictly additive, idempotent on re-runs.

Optional flags:

- `--register-mcp` adds the stdio MCP server to Claude Code's MCP
  config (`claude mcp add claude-director … serve --stdio`).
- `--symlink-dir <dir>` overrides the default symlink directory.
- `--binary <path>` installs from an explicit source binary.

### First spawn

```sh
# Launch a Spawn.
id=$(claude-director spawn --cwd "$PWD" | jq -r '.claude_instance_id')
echo "spawned: $id"

# Find it in the list.
claude-director list --state waiting

# Drive it.
claude-director send-keys --claude-instance-id "$id" --text "what is 2+2?"

# Read what came back.
claude-director read-pane --claude-instance-id "$id"

# Tear it down.
claude-director pause --claude-instance-id "$id"
```

That's the whole conversational loop — no human-in-the-pane required.

## Common workflows

### Drive a Spawn from another Claude

Set up the MCP server once (`install.sh --register-mcp`); inside
Claude, the verbs appear as `mcp__claude-director__spawn`,
`mcp__claude-director__send_keys`, etc. The orchestrating Claude
gets the same JSON envelope a shell script would, with typed
err_name on failure.

### Intercept permission prompts

```sh
# Launch with relay mode on.
id=$(claude-director spawn --cwd "$PWD" --relay-mode=on \
       | jq -r '.claude_instance_id')

# When Claude hits a PermissionRequest, state goes to check_permission.
# The orchestrator decides:
claude-director decide --claude-instance-id "$id" \
    --decision allow --reason "tool is on the allow-list"
```

The relay's fail-closed boundary (SRD §6.4) means every failure
mode — DB unreachable, polling timeout, ctx cancel — emits a deny
envelope. Claude never hangs.

### Cron hygiene

```cron
# Once an hour, reconcile killed/missing Spawns:
0 * * * * claude-director find-missing >/dev/null
# Once a day, age out terminal rows older than 31 days:
0 3 * * * claude-director expire >/dev/null
```

Run these as the same user that launches the Spawns. The
degraded-mode guard refuses to write if it can't see any Claude
processes — protecting against the cron-user-mismatch corruption
pattern.

### Pause and resume across sessions

```sh
# Pause a waiting Spawn.
claude-director pause --claude-instance-id "$id"

# ... days later ...

# Resume — same id, fresh tmux session, same JSONL transcript.
claude-director resume --claude-instance-id "$id"
```

`parent_id` is re-derived on every resume from the caller's
`CLAUDE_DIRECTOR_INSTANCE_ID` env var, so a Spawn moved between
orchestrators carries the current ownership graph.

### Templates

```sh
# Bake a preset.
claude-director make-template --name dev --cwd /repos/widget \
    --label project=widget --allow 'Bash(npm test)'

# Apply it.
claude-director spawn --template dev --label run=$(date +%s)
```

Per-call labels merge with the template's; per-call `permissions.allow`
concats; `--cwd` overrides. See [docs/settings.md](docs/settings.md).

## Configuration

`~/.claude-director/config.toml` (created on first run; all fields
optional):

```toml
[defaults]
relay_mode = "off"           # default for spawn; can be "on"
expire_retention_days = 31   # default age for expire's cutoff
disable_askuserquestion = false  # prepend AskUserQuestion to deny

[relay]
poll_base_ms = 100           # base sleep between polls
poll_jitter_ms = 100         # uniform jitter on top
timeout_seconds = 600        # how long to wait before deny

[pause]
timeout_seconds = 30         # how long to wait for /exit to land

[store]
db_path = "~/.claude-director/state.db"

[log]
error_log_path = "~/.claude-director/errors.log"
```

The MCP server reads this once at startup — edits don't take effect
until the next `serve --stdio` invocation (SRD §3.3).

## Uninstall

```sh
skills/install-claude-director/uninstall.sh           # preserve state.db + templates
skills/install-claude-director/uninstall.sh --purge   # remove everything
```

Uninstall only removes the entries `install.sh` added; other user
hooks in `~/.claude/settings.json` stay intact.

## Hooks and your settings.json

claude-director installs **two persistent hooks** into your
`~/.claude/settings.json`:

| event | matcher | what it runs |
|---|---|---|
| `SessionStart` | (any) | `claude-director help` |
| `SessionEnd` | `reason=compact` | `claude-director help` |

These re-inject the verb list into a new conversation so the model
knows the supervision API after a fresh session or a `/compact`.
Mirrors the pattern `bees sting` uses.

claude-director **also** installs hooks on every Spawn it launches,
inline via the synthesized `--settings` JSON — these are not
persistent in `~/.claude/settings.json`. The per-Spawn hooks are
what state tracking and relay-mode permissions actually need; they
fire ON the Spawn, not on the operator's interactive sessions.

If you have existing hooks in those events, Claude Code merges them
with claude-director's. Your hooks fire first; claude-director's
fire after. A user hook that exits non-zero can suppress
claude-director's state recording for that event — see
[docs/hooks.md](docs/hooks.md) for the full caveat list.

## Recommended for orchestrators: disable AskUserQuestion

If you're using claude-director as an orchestrator (an LLM driving
multiple Spawns, or any unattended automation), you almost certainly
want Spawns to make decisions autonomously rather than block waiting
for a human to answer an `AskUserQuestion` modal.

```toml
[defaults]
disable_askuserquestion = true
```

With this set, every Spawn is configured to deny Claude's
`AskUserQuestion` tool entirely (it's added to the synthesized
`permissions.deny` block). Claude will proceed without ever asking
the user — which is what you want for any setup where there isn't a
human watching the pane.

Leave it off (the default) when you're using claude-director
interactively from a terminal and want Claude to pause and ask when
it's genuinely unsure.

## Where to dig next

- [docs/architecture.md](docs/architecture.md) — package layout,
  state machine, the relay's fail-closed boundary, the MCP server's
  drift-free schema generation.
- [docs/permissions.md](docs/permissions.md) — relay mode, decision
  envelope shape, fail-closed semantics, race-free `decide` UPDATE.
- [docs/hooks.md](docs/hooks.md) — per-Spawn vs persistent hooks,
  PermissionRequest relay implementation, hook payload classification.
- [docs/settings.md](docs/settings.md) — how the per-Spawn
  `--settings` merges with Claude Code's settings tier stack.
- [docs/cli-reference.md](docs/cli-reference.md) /
  [docs/mcp-reference.md](docs/mcp-reference.md) — auto-generated
  from `internal/api/manifest`. Adding a verb regenerates both.
- [docs/multi-account.md](docs/multi-account.md) — running Spawns
  against a different Claude account.
