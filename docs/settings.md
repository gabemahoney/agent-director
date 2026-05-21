# Settings

How agent-director's per-Spawn `--settings` payload layers over Claude
Code's settings files. This document is the operator's reference for
predicting what permissions, hooks, and scalars a Spawn will actually
see — Claude Code does the merge itself, and agent-director's behavior
is a direct consequence of that merge.

For Claude Code's own description of settings, see the upstream docs:
<https://docs.claude.com/en/docs/claude-code/settings>.

## What `--settings` carries

Every `spawn` call synthesizes a single inline JSON document and hands
it to `claude` via `--settings <inline-json>`. The JSON is delivered as
a direct argv element to `tmux new-session` (no shell parsing — see
`reference/tmux-direct-argv-research.md`), so the payload arrives
byte-for-byte regardless of which shell the operator has configured.

The synthesized payload contains:

- **`hooks`** — eight state-tracking entries (`SessionStart`,
  `UserPromptSubmit`, `PreToolUse`, `PostToolUse`, `Stop`,
  `Notification`, `SessionEnd`, `PermissionRequest`), each pointing at
  the running `agent-director` binary's absolute path. See `hooks.md`
  for the per-event mapping.
- **`permissions`** (optional) — `allow` / `deny` / `ask` arrays
  populated from the `--allow` / `--deny` / `--ask` flags plus the
  `disable_askuserquestion` config flag. See `permissions.md`.

agent-director never writes hooks or permissions to disk on behalf of a
Spawn. The synthesized JSON exists only for the lifetime of the tmux
session, and no cleanup is needed.

## `defaults.inject_help_hook` config

`config.toml`:

```toml
[defaults]
inject_help_hook = true
```

Off by default. When true, every Spawn's synthesized `--settings`
appends one extra `SessionStart` hook entry whose command is the
absolute install path `~/.agent-director/bin/agent-director help`
(post `~` expansion). The state-tracking SessionStart entry is unchanged
and fires alongside it.

The flag exists to cover the dynamic case the operator's static
`~/.claude/settings.json` injection cannot reach: a Spawn that inherits
(or is given) a fresh `CLAUDE_CONFIG_DIR` whose `settings.json` does not
yet carry the help hook. With this flag set, every Spawn this binary
creates is self-describing to its first Claude turn regardless of the
config dir.

`install.sh` toggles the flag in lockstep with the static hook write —
the Q4 "inject persistent help hooks?" answer drives both halves
together. `--no-hooks` skips both. `uninstall.sh` reverses both.

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
| per-Spawn (via `--allow`) | `["WebFetch"]` |

Effective `allow` for the Spawn: `["Bash(npm test)", "Bash(go test)", "WebFetch"]`.

Claude Code merges these for us; agent-director does not read the user
or project tiers itself. A change to `~/.claude/settings.json` between
Spawns reaches the next Spawn without any agent-director restart.

## Clean-slate Spawns

To suppress the user (and optionally project) tiers, pass
`--setting-sources` *through* `claude_args`:

```
agent-director spawn --cwd /tmp -- --setting-sources project,local
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

A agent-director **template** is a TOML file at
`~/.agent-director/templates/<name>.toml` that bakes a default
spawn-parameter set. Per-call `--template <name>` layers the per-call
params on top per a fixed merge contract (SRD §7.1).

Templates are not the same as Claude Code's `settings.json` tier
stack. The tier stack belongs to Claude Code; templates belong to
agent-director and feed *into* the `--settings` JSON the supervisor
synthesizes for each spawn.

### Merge rules

| Field shape | Rule |
|---|---|
| Scalar (`cwd`, `relay_mode`) | Per-call non-empty REPLACES; per-call empty falls back to template. |
| Map (`extra_env`, `labels`) | Top-level merge. Template keys survive; per-call keys win on collision. |
| Permissions arrays (`permissions.allow` / `deny` / `ask`) | CONCAT. Template entries first, per-call appended. This mirrors how Claude Code itself merges its `permissions` block across settings tiers. |
| `claude_args` | Per-call non-nil REPLACES the template's slice wholesale. Per-call nil falls back to the template. Explicit empty (`[]`) replaces with empty. |

Omitting per-call permissions inherits the template's entries
unchanged.

### Reserved per-invocation params

A template MUST NOT bake any of:

- `template` (recursion would be ill-defined)
- `claude_instance_id` (must be per-invocation for uniqueness)
- `tmux_session_name` (derived from the id + cwd)

`make-template` rejects these at the CLI flag layer; a hand-edited
template carrying them surfaces `ErrTemplateMalformed` on load.

### Example

```toml
# ~/.agent-director/templates/dev.toml
cwd        = "/home/me/repos/widget"
relay_mode = "off"
claude_args = ["--model", "opus"]

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
- `claude_args`    → `["--model", "opus"]` (template; no per-call replacement)
- `labels` → `{project=widget, env=dev, other=bar}` (merged)
- `permissions.allow` → `["Bash(npm test)", "Bash(npm run lint)", "Bash(jq)"]` (concat)
- `permissions.deny`  → `["Bash(rm)"]` (template only)

## Where to go next

- `hooks.md` — per-event state-tracking + fail-open invariant.
- `permissions.md` — how the `permissions` block accumulates.
- `multi-account.md` — running Spawns against a different Claude account.
- `architecture.md` — package layout, state machine, parameter pipeline.

## References

- Claude Code settings: <https://docs.claude.com/en/docs/claude-code/settings>
- Claude Code CLI flags (incl. `--setting-sources`):
  <https://docs.claude.com/en/docs/claude-code/cli-reference>
- Empirical investigation (gitignored, in-repo):
  `reference/claude-settings-research.md`
