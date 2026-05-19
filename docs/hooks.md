# Hooks

How claude-director's per-Spawn state-tracking hooks coexist with the
operator's own Claude Code hooks. Each Spawn gets eight hook entries
synthesized into its `--settings`; they fire on every Claude lifecycle
event and write the row UPSERT that powers `status` / `get` / `list`.

For Claude Code's own hooks reference, see:
<https://docs.claude.com/en/docs/claude-code/hooks>.

## The eight state-tracking hooks

claude-director registers one entry per event listed below. Each entry
runs the same command — `<abs-path>/claude-director hook` — and Claude
Code feeds it the payload JSON on stdin.

| Event | Tool matcher | Resulting state (SRD §5.2) |
| --- | --- | --- |
| `SessionStart` | — | `waiting` (also writes `claude_session_id`) |
| `UserPromptSubmit` | — | `working` |
| `PreToolUse` | `*` (all tools) | `working` |
| `PreToolUse` | tool=`AskUserQuestion` | `ask_user` |
| `PostToolUse` | — | `working` |
| `Stop` | — | `waiting` |
| `Notification` | — | `waiting` |
| `PermissionRequest` | `*` (all tools) | `check_permission` (Epic 10 relay path is a stub today) |
| `SessionEnd` | reason ∈ {`clear`, `compact`} | soft refresh: bumps `last_seen_at`, state unchanged |
| `SessionEnd` | any other reason | `ended` (also sets `ended_at`) |

Unknown event names are treated as soft refreshes — the row's
`last_seen_at` updates and an info-level log line records the unknown
name so operators can spot new Claude Code events that need a classifier
update.

## Fail-open invariant

State-tracking hooks **must never block Claude Code**. The `hook` verb
exits 0 with empty stdout on every internal failure:

- Missing `CLAUDE_DIRECTOR_INSTANCE_ID` env → exit 0, log entry.
- Malformed JSON payload → exit 0, log entry.
- Payload over 1 MiB → exit 0, log entry.
- Config malformed → exit 0, log entry.
- Store open failure → exit 0, log entry.
- DB write failure → exit 0, log entry.
- Unknown event name → exit 0, soft refresh, log entry.

All log entries land in `~/.claude-director/errors.log` (configurable
via `[log] error_log_path` in `config.toml`). A missed state update is
annoying but never breaks a Claude session.

The relay-mode `PermissionRequest` path will be fail-*closed* (default
to `deny` on any internal error) once Epic 10 lands. Until then,
PermissionRequest events take the same fail-open route as every other
state-tracking event.

## Fire order

Claude Code merges hook entries across tiers and runs them in array
order (see `settings.md`). Lower tiers fire first:

```
user (~/.claude/settings.json)
  → project (.claude/settings.json in cwd)
    → local (.claude/settings.local.json)
      → claude-director (per-Spawn --settings)
        → policy (managed)
```

claude-director's hook is *last among user-installed tiers*. For state
tracking this is fine — by the time our hook runs, every user hook has
already had its turn; our UPSERT lands on top with the most recent
`last_seen_at`. For relay-mode permission decisions (Epic 10) this
ordering will matter: our decision envelope on stdout is the one Claude
consumes, regardless of what earlier-running user hooks emit.

## Misbehaving user hooks

A user hook can stall (long-running tool call), exit non-zero, or write
malformed stdout. Each of these has a different blast radius:

- **Slow user hook** — Claude Code waits for every hook entry to return
  before consuming the event. A 30-second user-script hook stalls the
  entire pipeline; claude-director's state UPSERT doesn't land until
  the user hook returns. The `status` verb will keep showing the
  pre-event state during the stall.
- **Non-zero user hook** — for `PreToolUse`, a non-zero exit can block
  the tool call (Claude Code's policy). claude-director's hook still
  runs and writes its UPSERT either way; the row state is independent
  of whether the tool was actually executed.
- **Malformed user hook stdout** — irrelevant for state-tracking hooks
  (they don't emit stdout), but a poorly-formed permission-decision
  envelope from a user hook could confuse the relay path in Epic 10.
  claude-director's relay hook is always last, so it wins the decision
  channel.

The recommended mitigation is to put hooks the operator does NOT want
running inside claude-director Spawns behind a `CLAUDE_DIRECTOR_INSTANCE_ID`
env-var guard:

```bash
# Inside ~/.claude/settings.json hook command:
if [ -n "$CLAUDE_DIRECTOR_INSTANCE_ID" ]; then
  exit 0  # skip user hook for claude-director Spawns
fi
# ... normal user-hook body ...
```

This lets the operator opt-out per Spawn surface without flipping
`--setting-sources project,local` at every `spawn` call.

## Persistent hooks (the `help`-on-SessionStart pair)

The install skill (Epic 12) adds two persistent hook entries to the
user's `~/.claude/settings.json`:

- `claude-director help` on `SessionStart`
- `claude-director help` on `SessionEnd reason=compact`

These are *not* per-Spawn; they fire on every Claude session the
operator runs, regardless of whether claude-director launched it. They
inject the verb list into the new conversation so the model knows the
supervision API surface. See `architecture.md` and the install skill's
README for details — Epic 12 owns this.

## References

- Claude Code hooks: <https://docs.claude.com/en/docs/claude-code/hooks>
- Empirical investigation (gitignored, in-repo):
  `reference/claude-settings-research.md`
