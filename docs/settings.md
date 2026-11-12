# Settings

agent-director adds launch settings without rewriting your agent's configuration
files. This page covers that layer and agent-director templates. For native
settings, see the [Claude Code settings reference](https://docs.claude.com/en/docs/claude-code/settings)
and [Codex configuration reference](https://developers.openai.com/codex/config-reference).

## What `--settings` carries

Claude launches receive inline JSON through `--settings` containing:

- State-tracking `hooks`, described in [Hooks](hooks.md#the-eleven-state-tracking-hooks).
- Optional `permissions.allow`, `deny` and `ask` from spawn parameters, plus
  `disable_ask_user`. See [Permissions](permissions.md).
- `statusLine` and `subagentStatusLine` callbacks for
  [experimental context fullness](caller-contract.md#experimental-context-fullness).
  They forward the input, output and exit status of status-line commands in
  user, project and local settings. A status line added only by a
  `[launch] claude_path` wrapper's own `--settings` is not forwarded.
  Set `[defaults] disable_context_fullness = true` to omit both callbacks
  and leave Claude Code's status lines alone.

This JSON lasts for the launch and needs no cleanup. Claude Code merges it
with its own settings; see [Fire order](hooks.md#fire-order) for hook layering
and [Permissions](permissions.md#per-tier-concatenation) for permission lists.

## Cursor launch settings

Cursor uses managed session setup rather than Claude's `--settings`.
With `inject_help_hook` enabled, fresh, latest-loaded and confirmed replacement
conversations receive guidance through a session-scoped stdio MCP registration.
No dashboard registration or native-home edit is required. Delivery and
compaction limits are in [Hooks](hooks.md#what-agent-director-help-actually-outputs).

`disable_ask_user` accepts either value but neither enables questions.
[Cursor permissions](permissions.md#cursor-spawns) owns enforced behavior and
permission-list handling.

Cursor pre-trust grants no trust: `no_pre_trust` reports `skipped`; otherwise
the unsupported attempt reports `failed` with a warning and launch continues.
Neither establishes readiness or changes permission policy. It edits no Cursor
authentication or configuration files and approves no dialog. Spawn or reuse
saves its own `no_pre_trust`; resume retains it. See
[Fresh Cursor use](usage.md#fresh-cursor-use) for supported launch and account limits.

## What a Codex launch carries

Every Codex spawn, reuse and resume adds a command-line settings block.
It writes nothing in the Codex folder, project `.codex/` or spawn's cwd.

- Runs without a shared background service; existing services are unused.
- Adds the [Codex hook entries](hooks.md#the-codex-entry-set), trusted even
  with `no_pre_trust` or failed pre-trust. Your own hooks remain separate.
- Disables the paste-burst delay. See
  [Ordinary text submission](caller-contract.md#ordinary-text-submission).
- Disables the question tool while `disable_ask_user` is on; see
  [Permissions](permissions.md#disable_ask_user-config).
- Enables `write_stdin_approval`: input to a running command asks for approval,
  except inside Codex's sandbox, where it is not relayed. Your
  `--disable write_stdin_approval` turns this off.
- Sets `BASH_ENV` empty unless the call or template's `extra_env` overrides it.
  Keep [shell startup output](hooks.md#the-shell-between-codex-and-the-hook) silent.
- Adds folder trust and a hook-review bypass only when
  [pre-trust](#pre-trust-for-codex) is `ok`. Folder trust replaces any
  `projects` table in the caller's launch arguments.

### Ordinary delivery launch settings

A fresh launch using the arguments supported by
[Ordinary text submission](caller-contract.md#ordinary-text-submission), with
exactly Codex 0.159.2 or Claude Code 2.1.280, receives these additions.
Resumed conversations, unsupported versions and other agent arguments do not.
Native confirmed delivery requires Linux; literal input remains available
without that capability.

- **Claude Code:** a generated `--session-id`,
  `--debug-file <evidence-dir>/claude-debug.log` and `-d hooks`.
  The launch receives agent-director's `HOME` and the selected
  [`CLAUDE_CONFIG_DIR`](multi-account.md). A relative config directory or
  an existing or unreadable `keybindings.json` disables native confirmed
  delivery, but these additions still apply.
- **Codex:** `-c log_dir="<evidence-dir>"`, `RUST_LOG=off,codex_tui=info`,
  the complete built-in Codex 0.159.2 keymap through `-c tui.keymap=…`, and
  `-c tui.vim_mode_default=false`. These override user, project and cloud
  keybindings and start with Vim disabled. The logging setting overrides
  `extra_env.RUST_LOG`. `CODEX_HOME` follows
  [Codex group folders](multi-account.md#codex-group-folders).
  A higher-priority `/etc/codex/managed_config.toml` containing keymap or Vim
  settings, or one that cannot be read or parsed, disables native confirmed delivery.

Diagnostics persist without automatic cleanup in
`~/.config/agent-director/input/<store_id>/<launch_token>/`, under
agent-director's `HOME`, even with a custom database or agent config path.
The directory is private (0700); evidence files and its adjacent lock are 0600.
Provider transcripts stay at provider paths. Prompts and retained input proof
are private database records. For inspection and recovery, see
[Unresolved ordinary input](operator-actions.md#unresolved-ordinary-input).

### Codex's layers

agent-director's command-line block overrides Codex's native settings and the
caller arguments supplied at spawn or replayed at resume. It appears before
any `--` separating the prompt.

agent-director does not duplicate a caller's `--no-daemon`,
`--dangerously-bypass-hook-trust` (with pre-trust `ok`) or
`--enable write_stdin_approval`. Codex rejects repetitions within the caller's
own arguments. Conflicting arguments and their refusals are in the
[caller contract](caller-contract.md#spawn-and-template-input-refusals).

### Pre-trust for Codex

Pre-trust applies only to the spawn's cwd for that launch. It writes no Codex
file. The result's `pre_trust` is:

| Result | Launch behavior |
| --- | --- |
| `ok` | Adds folder trust and bypasses hook review |
| `skipped` | `no_pre_trust` is set; Codex uses its recorded trust for your folders and hooks |
| `failed` | Launch continues without folder trust or hook-review bypass; see [input handling](caller-contract.md#spawn-and-template-input-refusals) |

agent-director's own hooks stay trusted in every case. Use an absolute
`CODEX_HOME` when selecting a [group folder](multi-account.md#codex-group-folders).
`-C`, `--cd` or `--worktree` can move Codex away from the trusted cwd.
Folder trust does not persist to later manual runs; Claude Code's pre-trust does.

## `defaults.inject_help_hook` config

```toml
[defaults]
inject_help_hook = true
```

The default is `false`. `install.sh` sets it to `true` unless `--no-hooks`;
the interactive persistent-help choice controls it too. `uninstall.sh`
reverses that setting. See [Installation](install.md#claude-code-and-codex).

When enabled, managed Claude and Codex launches add a `SessionStart` help
entry using the absolute installed binary path
`~/.config/agent-director/bin/agent-director`. This works with a separate
`CLAUDE_CONFIG_DIR` that has no persistent help hook. Cursor receives
session-scoped MCP guidance instead. Turning this off does not disable
state reporting.

The installer also adds [persistent Claude help hooks](hooks.md#persistent-hooks-the-help-on-sessionstart-pair).
Manual Codex sessions need the entry below.
[Hooks](hooks.md#what-agent-director-help-actually-outputs) describes delivery.

## Set up agent-director in a Codex folder

Add these two entries by hand to `config.toml` in the default Codex folder
(`~/.codex`) or each [group folder](multi-account.md#codex-group-folders).
Replace `/home/you` with your home directory.

The MCP entry forwards the caller id and tmux environment so child spawns
have the correct parent and tmux server:

```toml
[mcp_servers.agent-director]
command = "/home/you/.config/agent-director/bin/agent-director"
args = ["serve", "--stdio"]
env_vars = ["AGENT_DIRECTOR_INSTANCE_ID", "TMUX", "TMUX_TMPDIR"]
```

The help entry supplies the verb list at session start.
`AGENT_DIRECTOR_AGENT=codex` selects Codex's structured response;
`additionalContextLimit = 4000` allows the whole list:

```toml
[[hooks.SessionStart]]

[[hooks.SessionStart.hooks]]
type = "command"
command = 'AGENT_DIRECTOR_AGENT=codex "$HOME/.config/agent-director/bin/agent-director" help'
additionalContextLimit = 4000
```

Codex asks to trust this help entry when it is added or changed.
With this entry and `inject_help_hook` on, a managed launch receives the list
twice. MCP approvals are covered by
[Codex permissions](permissions.md#codex-spawns), and calling options by
[Drive a spawn from another agent](usage.md#drive-a-spawn-from-another-agent).

To remove the setup, delete the `[mcp_servers.agent-director]` table and the
help entry's `[[hooks.SessionStart]]` pair. `uninstall.sh` leaves both in place,
along with Codex's own trust record. Entries must name the current installed
binary path; see [Upgrade](install.md#upgrade).

## Clean-slate Spawns

Pass Claude Code's `--setting-sources` through `agent_args` to exclude selected
user, project or local settings:

```sh
agent-director spawn --cwd /tmp --agent claude -- --setting-sources project,local
```

This excludes user settings but keeps project and local settings.
agent-director's `--settings` and managed policy still apply. Native flag
details are in the [Claude Code CLI reference](https://docs.claude.com/en/docs/claude-code/cli-reference).

## Templates and per-call merge

A template at `~/.config/agent-director/templates/<name>.toml` supplies spawn
defaults. `--template <name>` selects it. This merge is separate from the
agent's native settings merge.

### Merge rules

| Field | Rule |
| --- | --- |
| Scalars (`cwd`, `relay_mode`, `agent`) | Non-empty per-call value replaces the template; empty falls back |
| Maps (`extra_env`, `labels`) | Merge keys; per-call keys win |
| `permissions.allow`, `deny`, `ask` | Concatenate template entries then per-call entries |
| `agent_args` | Supplied per-call list replaces the template's entire list; otherwise inherit for the same agent |

Omitted permission lists inherit unchanged. `extra_env` keys must be valid
environment names; `HOME` and `AGENT_DIRECTOR_*` are reserved.
For account selection, use [Multi-Account](multi-account.md).
Input refusals belong to the [caller contract](caller-contract.md#spawn-and-template-input-refusals).

### The agent and its arguments

Choose the case-sensitive `claude`, `codex` or `cursor` through the call's
`agent`, otherwise the template's. A template with `agent_args` must name
its `agent`; those arguments are inherited only for that agent.

An explicit empty `agent_args` list replaces the template on MCP and Go.
On CLI and TypeScript an empty list counts as omitted. For another agent,
supply replacement arguments or use its own template. See
[input refusals](caller-contract.md#spawn-and-template-input-refusals).

### Reserved per-invocation params

Templates cannot set `template`, `instance_id`, `tmux_session_name` or
`reuse_finished`. `agent` is allowed.

### One spelling per key

Table and key names are case-insensitive. Keep one spelling per key, including
across tables such as `[permissions]` and `[Permissions]`.
Keys within `[extra_env]` and `[labels]` remain case-sensitive:
`FOO` and `foo` are distinct.

### Example

```toml
# ~/.config/agent-director/templates/dev.toml
cwd        = "/home/me/repos/widget"
relay_mode = "off"
agent      = "claude"
agent_args = ["--model", "opus"]

[labels]
project = "widget"

[permissions]
allow = ["Bash(npm test)"]
deny  = ["Bash(rm)"]
```

```sh
agent-director spawn --template dev --label env=dev --allow 'Bash(jq)'
```

The spawn inherits the cwd, agent, arguments and deny list. Its labels are
`project=widget` and `env=dev`; its allow list contains `Bash(npm test)` and
`Bash(jq)`.
