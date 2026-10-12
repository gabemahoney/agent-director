# Settings

How agent-director's per-Spawn settings layer over the agent's own. For
Claude Code that is the `--settings` payload over Claude Code's settings
files; for Codex it is a per-launch block on Codex's command line (see
[What a Codex launch carries](#what-a-codex-launch-carries)). This document
is the operator's reference for predicting what permissions, hooks, and
scalars a Spawn will actually see: the agent does the merge itself, and
agent-director's behavior is a direct consequence of that merge.

For Claude Code's own description of settings, see the upstream docs:
<https://docs.claude.com/en/docs/claude-code/settings>.

The Claude Code merge sections below describe `claude` Spawns; the
template and `inject_help_hook` sections cover both agents.

## What `--settings` carries

Every `spawn` of a Claude agent synthesizes a single inline JSON document and hands
it to `claude` via `--settings <inline-json>`. The JSON is delivered as
a direct argv element to `tmux new-session` (no shell parsing — see
`reference/tmux-direct-argv-research.md`), so the payload arrives
byte-for-byte regardless of which shell the operator has configured.

The synthesized payload contains:

- **`hooks`** — eleven state-tracking entries (`SessionStart`,
  `UserPromptSubmit`, `PreToolUse`, `PostToolUse`, `PostToolUseFailure`,
  `Stop`, `Notification`, `SessionEnd`, `PermissionRequest`, `SubagentStart`,
  `SubagentStop`), each pointing at the running `agent-director` binary's
  absolute path. See `hooks.md` for the per-event mapping.
- **`permissions`** (optional) — `allow` / `deny` / `ask` arrays
  populated from the spawn's `allow` / `deny` / `ask` params
  (`--allow` / `--deny` / `--ask` on the CLI) plus the
  `disable_ask_user` config flag. See `permissions.md`.

agent-director never writes hooks or permissions to disk on behalf of a
Spawn. The synthesized JSON exists only for the lifetime of the tmux
session, and no cleanup is needed.

## What a Codex launch carries

Every launch of a Codex agent (`spawn`, a reuse and `resume`) adds a block of
settings to Codex's command line. The block exists only for that launch:
agent-director writes it to no Codex folder, no file in the Spawn's cwd and no
project `.codex/`, and it needs no cleanup. It holds:

- **No shared background service.** The agent runs alone; a shared Codex
  service that is already running is left alone and unused.
- **Hook entries and their trust.** Eight hook entries (see
  [Hooks](hooks.md#the-codex-entry-set)), each trusted by its own hash, so
  Codex shows no hook-review dialog for them in any launch, `no_pre_trust` and
  a failed pre-trust included.
- **The typing setting.** Enter submits at once, so a multi-line text arrives
  as one prompt.
- **The question tool, off.** While `disable_ask_user` is on, Codex's question
  tool is turned off for the launch (see
  [Permissions](permissions.md#disable_ask_user-config)).
- **Approval of input to a running command, on.** Text sent to a command that
  is already running asks for approval like any other command, except input to
  a command running inside Codex's sandbox, which asks nothing and is never
  relayed. A caller's own `--disable write_stdin_approval` turns it off again.
- **`BASH_ENV` set to empty.** The agent's shell then reads no startup file
  through `BASH_ENV`. A `BASH_ENV` in the spawn's `extra_env` (or its
  template's) wins. The agent's own commands run without a `BASH_ENV` file
  unless you set one; one that prints reaches the hooks too (see
  [The shell between Codex and the hook](hooks.md#the-shell-between-codex-and-the-hook)).
- **Folder trust and the hook-review bypass,** only when pre-trust is `ok`
  (see [Pre-trust for Codex](#pre-trust-for-codex)). The folder-trust setting
  replaces any `projects` table the caller passes for that launch.

### Codex's layers

Codex reads its own settings in layers, lowest first: the user's, the group's
Codex folder and the project's. agent-director's per-launch block is one more
layer after them and wins over every pass-through argument at `spawn` and every replayed
argument at `resume`: it stands after the last of those arguments and before
any `--`, so what follows a `--` is the prompt, as Codex reads it. At `resume`
the block comes after `resume <id>` and the replayed arguments.

agent-director never adds a second `--no-daemon`, a second
`--dangerously-bypass-hook-trust` (with pre-trust `ok`) or a second
`--enable write_stdin_approval` when the caller's own arguments already carry
one. Codex itself rejects a flag repeated in the caller's arguments.

The arguments that would fight the block are refused at `spawn` with
`ErrSpawnDeniedFlag` (see the
[caller contract](caller-contract.md#spawn-and-template-input-refusals)).

### Pre-trust for Codex

Pre-trust for a Codex agent marks only the Spawn's cwd, for that launch only.
It writes no file, takes no lock and touches no Codex folder. The outcome is
`pre_trust` on the result:

- `ok`: the launch carries the folder trust and the bypass of Codex's hook
  review, so Codex does not stop at its folder-trust or hook-review dialog.
- `skipped`: `no_pre_trust` is set, on the spawn or, for `resume`, on the row.
  The launch carries neither. Codex asks about the folder, and about the user's,
  the group's and the project's own hooks, as it finds them, with the trust it
  recorded for them. agent-director's own hooks stay trusted.
- `failed`: `CODEX_HOME` in `extra_env` is not an absolute path (see
  [Multi-Account](multi-account.md#codex-group-folders)). One warning line goes
  to stderr, the launch goes ahead as for `skipped`, and the agent may stop at
  Codex's folder-trust prompt.

`-C` / `--cd` or `--worktree` in the agent's arguments moves Codex's working
root away from the Spawn's cwd, so the trusted folder may not be the one Codex
works in and Codex may ask.

**Folder trust lasts one launch only.** A later Codex run by hand in that
folder asks for trust again. Claude Code's pre-trust, by contrast, persists in
its file. Answer Codex's prompt when running it by hand.

## `defaults.inject_help_hook` config

`config.toml`:

```toml
[defaults]
inject_help_hook = true
```

Off by default. When true, every Claude Spawn's synthesized `--settings`
appends one extra `SessionStart` hook entry whose command is the
absolute install path `~/.config/agent-director/bin/agent-director help`
(post `~` expansion). The state-tracking SessionStart entry is unchanged
and fires alongside it. Every Codex launch carries a help entry of its own in
its per-launch block while the setting is on (see
[Hooks](hooks.md#what-agent-director-help-actually-outputs)), and none when it
is off; the persistent pair described there is Claude-only. A hand-made hook or MCP entry that names the old
directory needs the new path (see [Upgrade](install.md#upgrade)).

The flag exists to cover the dynamic case the operator's static
`~/.claude/settings.json` injection cannot reach: a Spawn that inherits
(or is given) a fresh `CLAUDE_CONFIG_DIR` whose `settings.json` does not
yet carry the help hook. With this flag set, every Spawn this binary
creates is self-describing to its first turn regardless of the config
dir, Codex Spawns included.

`install.sh` sets the flag whether or not Claude Code is installed, so a
host with only Codex gets it too. The static hook write in
`~/.claude/settings.json` happens only with Claude Code installed (see
[Claude Code and Codex](install.md#claude-code-and-codex)). The Q4 "inject
persistent help hooks?" answer drives both, and `--no-hooks` skips both.
`uninstall.sh` reverses both.

The hook command is an absolute path rather than a bare
`agent-director` because the spawned Claude's PATH may not include
`~/.local/bin`, and the hook fires before any shell-rc manipulation
could amend PATH.

## The five-tier merge

Claude Code merges settings from up to five sources in this order
(lowest precedence first; higher tiers win on scalar conflicts):

1. `userSettings` — `~/.claude/settings.json`
2. `projectSettings` — `.claude/settings.json` in the Spawn's cwd
3. `localSettings` — `.claude/settings.local.json` in the cwd
4. `flagSettings` — what agent-director passes via `--settings`
5. `policySettings` — managed-policy file (organization-wide)

The merge rules depend on the value type at each key:

- **Arrays** (e.g. `permissions.deny`, `hooks.SessionStart`)
  — concatenated, lower tier first. *No tier replaces another.*
- **Objects** (e.g. `permissions`, `hooks`, `mcpServers`) — recursed
  into; each sub-key follows its own type's rule.
- **Primitives** (e.g. `theme`, `model`, `defaultMode`) — higher tier
  wins outright.

This is empirically verified — see `reference/claude-settings-research.md`
for the source read of Claude Code's `B4H` merger function.

### Worked example: `permissions.allow`

| Tier | `allow` entries |
| ---- | --- |
| user | `["Bash(npm test)"]` |
| project | `["Bash(go test)"]` |
| per-Spawn (via `allow`) | `["WebFetch"]` |

Effective `allow` for the Spawn: `["Bash(npm test)", "Bash(go test)", "WebFetch"]`.

Claude Code merges these for us; agent-director does not read the user
or project tiers itself. A change to `~/.claude/settings.json` between
Spawns reaches the next Spawn without any agent-director restart.

## Clean-slate Spawns

To suppress the user (and optionally project) tiers, pass
`--setting-sources` *through* `agent_args`:

```
agent-director spawn --cwd /tmp --agent claude -- --setting-sources project,local
```

- `--setting-sources` accepts a comma-separated subset of `user`,
  `project`, `local`.
- `flagSettings` (i.e., agent-director's `--settings`) and
  `policySettings` are unconditional — they always apply, regardless of
  `--setting-sources`.
- agent-director's denied-flag list explicitly excludes
  `--setting-sources` for this reason (`--settings` is denied because it
  would conflict with our hook synthesis; `--setting-sources` is safe).

The four-tier merge result of the above example would drop the user's
`Bash(npm test)` entry but keep the project's `Bash(go test)` and the
per-Spawn `WebFetch`.

## Hook fire order

Lower-precedence tiers are merged into the accumulator first, so the
final `hooks.<event>` array is:

```
[ ...userSettings.hooks.<event>,
  ...projectSettings.hooks.<event>,
  ...localSettings.hooks.<event>,
  ...flagSettings.hooks.<event>,
  ...policySettings.hooks.<event> ]
```

Claude Code executes entries in array order. Hooks the operator has
installed via `~/.claude/settings.json` fire *before*
agent-director's. This is fine for state tracking (agent-director's
hook records the row UPSERT regardless of what user hooks did) and is
the expected behavior for relay-mode permission decisions (the relay
hook fires last and emits the decision envelope Claude consumes).

A misbehaving user hook can still suppress agent-director's state
recording — see `hooks.md` for the caveat and mitigations.

## Templates and per-call merge

An agent-director **template** is a TOML file at
`~/.config/agent-director/templates/<name>.toml` that bakes a default
spawn-parameter set. A spawn's `template` param (`--template <name>` on
the CLI) layers the per-call params on top per a fixed merge contract (SRD §7.1).

Templates are not the same as Claude Code's `settings.json` tier
stack. The tier stack belongs to Claude Code; templates belong to
agent-director and feed *into* the `--settings` JSON the supervisor
synthesizes for each spawn.

### Merge rules

| Field shape | Rule |
|---|---|
| Scalar (`cwd`, `relay_mode`, `agent`) | Per-call non-empty REPLACES; per-call empty falls back to template. |
| Map (`extra_env`, `labels`) | Top-level merge. Template keys survive; per-call keys win on collision. |
| Permissions arrays (`permissions.allow` / `deny` / `ask`) | CONCAT. Template entries first, per-call appended. This mirrors how Claude Code itself merges its `permissions` block across settings tiers. |
| `agent_args` | A per-call list REPLACES the template's wholesale; otherwise the template's are inherited, if they belong to the spawn's agent (below). |

Omitting per-call permissions inherits the template's entries
unchanged.

A template that sets the arguments key under its old name is refused with
`ErrTemplateMalformed`, which names `agent_args`; the key is not ignored.

`spawn` validates the merged `extra_env`: an `AGENT_DIRECTOR_*` key,
`HOME` with any value, or a key that is not a valid env-var name (empty,
or containing `=` or a NUL byte, such as `"CLAUDE_CONFIG_DIR=/x"`), from
the template or the call, returns `ErrReservedEnvKey` and launches
nothing; the description says which. `make-template` runs the same check
on the template's `extra_env` and refuses such a key with the same error,
writing no template and replacing none. A template saved before that
check, or edited by hand, can still hold one; each `spawn` that uses it
is refused. To give a templated agent its own config, set an
absolute `CLAUDE_CONFIG_DIR` (Claude Code) or `CODEX_HOME` (Codex) under
`[extra_env]` instead of `HOME`.

### The agent and its arguments

A spawn needs an agent, `claude` or `codex`: the per-call `agent` (`--agent`
on the CLI), else the template's. With neither, `spawn` is refused with
`ErrAgentMissing`; any other value, with `ErrAgentInvalid` (the value is
case-sensitive). A template names its agent with `agent`.

A template's `agent_args` belong to the template's `agent`. A spawn inherits
them only when the agent it resolves to is that agent; when it is another,
`spawn` is refused with `ErrAgentArgsMismatch`. A per-call `agent_args` list
that the call supplies replaces the template's wholesale and inherits
nothing, so it is never refused for a mismatch. On MCP and in Go an explicit
empty list counts as supplied (the spawn gets no agent arguments, whatever
the template says). On the CLI and in TypeScript an empty list counts as
none: the template's are inherited when the agent is the same, and
otherwise `spawn` is refused with `ErrAgentArgsMismatch`. A template with `agent_args` and
no `agent` is refused with `ErrTemplateAgentMissing`, by `make-template` and
when a `spawn` loads it.

The refusals, and what to do for each, are in the
[caller contract](caller-contract.md#spawn-and-template-input-refusals).

### Reserved per-invocation params

`agent` is not one of these: a template may bake it. A template MUST NOT
bake any of:

- `template` (recursion would be ill-defined)
- `instance_id` (must be per-invocation for uniqueness)
- `tmux_session_name` (derived from the id + cwd)
- `reuse_finished` (applies to one `spawn` call only, and only with an
  explicit `instance_id`)

`make-template` refuses them with `ErrInvalidFlags`: on the CLI as
undefined flags, over MCP as unknown parameters. A hand-edited template
carrying any of them surfaces `ErrTemplateMalformed` on load.

### One spelling per key

agent-director matches table and key names regardless of letter case, so
a hand-edited template that sets one key under names differing only in
letter case (`RELAY_MODE` and `relay_mode`, or `allow` under both
`[permissions]` and `[Permissions]`) surfaces `ErrTemplateMalformed` on
load, naming each spelling. Keep one and remove the others. A single
spelling (`RELAY_MODE` alone) loads. Keys under `[extra_env]` and
`[labels]` keep their letter case: `FOO` and `foo` there are two entries.

### Example

```toml
# ~/.config/agent-director/templates/dev.toml
cwd        = "/home/me/repos/widget"
relay_mode = "off"
agent      = "claude"
agent_args = ["--model", "opus"]

[labels]
project = "widget"
env     = "dev"

[permissions]
allow = ["Bash(npm test)", "Bash(npm run lint)"]
deny  = ["Bash(rm)"]
```

```sh
agent-director spawn --template dev --label other=bar --allow 'Bash(jq)'
```

Resulting per-spawn merge:

- `cwd`            → `/home/me/repos/widget` (template)
- `relay_mode`     → `off` (template)
- `agent`          → `claude` (template; no per-call `--agent`)
- `agent_args`     → `["--model", "opus"]` (template; no per-call replacement)
- `labels` → `{project=widget, env=dev, other=bar}` (merged)
- `permissions.allow` → `["Bash(npm test)", "Bash(npm run lint)", "Bash(jq)"]` (concat)
- `permissions.deny`  → `["Bash(rm)"]` (template only)

## Where to go next

- `hooks.md` — per-event state-tracking + fail-open invariant, for either agent.
- `permissions.md` — how the `permissions` block accumulates, and the
  question-tool setting for either agent.
- `multi-account.md` — running Spawns against a different Claude or Codex
  account.
- `architecture.md` — package layout, state machine, parameter pipeline.

## References

- Claude Code settings: <https://docs.claude.com/en/docs/claude-code/settings>
- Claude Code CLI flags (incl. `--setting-sources`):
  <https://docs.claude.com/en/docs/claude-code/cli-reference>
- Empirical investigation (gitignored, in-repo):
  `reference/claude-settings-research.md`
