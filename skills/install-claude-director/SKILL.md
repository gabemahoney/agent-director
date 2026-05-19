---
name: install-claude-director
description: Install (or upgrade) claude-director on this machine. Runs the bundled install.sh against the user's ~/ — creates ~/.claude-director/ with the binary, warms up state.db, and injects two persistent `claude-director help` hooks into ~/.claude/settings.json (SessionStart + SessionEnd reason=compact). Use this skill when the user says "install claude-director", "set up claude-director", or "upgrade claude-director on this machine".
---

## When to invoke

Trigger phrases: "install claude-director", "set up claude-director",
"upgrade claude-director on this machine".

## What this skill does

This skill runs `install.sh` from the same directory. The script:

1. **Pre-flights.** Verifies `claude` and `tmux` are on PATH. Aborts
   with a clear message if either is missing — claude-director
   cannot do useful work without them.

2. **Creates `~/.claude-director/`** (mode 0700) if missing, plus
   `~/.claude-director/bin/` for the binary.

3. **Copies the binary** to `~/.claude-director/bin/claude-director`
   (mode 0755). The source is determined by:
   - `--binary <path>` if supplied, OR
   - `$(dirname $0)/../../bin/claude-director` (the in-repo build) if
     this skill was invoked from a checked-out tree, OR
   - the currently-running `claude-director` resolved via `command -v`.

   On upgrade (existing binary detected), the new binary is written
   to a version-suffixed path AND the canonical filename is swapped
   atomically — live Spawns' next hook invocation picks up the new
   binary cleanly without an exec-while-overwrite hazard.

4. **Rejects whitespace in the install path.** Per SRD §4.3 tmux's
   direct-argv invocation does not tolerate spaces in the binary
   path; the script aborts up front rather than failing mysteriously
   later.

5. **Warms up the database.** Runs `claude-director help` once so
   `internal/store.ensureSchema` creates `state.db` (mode 0600) and
   stamps the schema version.

6. **Injects persistent hooks** into `~/.claude/settings.json`:
   - `SessionStart` → `claude-director help`
   - `SessionEnd` with matcher `reason=compact` → `claude-director help`

   These re-inject the verb list into a new conversation so the
   model knows the supervision API surface after a `/compact` or
   fresh session. Mirrors how `bees sting` keeps its skill list
   alive across compacts.

   The merge is additive: existing user hooks are preserved. Re-running
   the install is idempotent — duplicate entries are detected and
   skipped.

7. **Optional MCP registration.** With `--register-mcp`, runs
   `claude mcp add claude-director ~/.claude-director/bin/claude-director serve --stdio`.
   Skipped by default — operators who don't want the MCP server
   never see it advertised inside Claude.

8. **Optional PATH symlink.** With `--symlink-dir <dir>`, drops a
   symlink at `<dir>/claude-director` pointing at the canonical
   binary. Default: `~/.local/bin` if it exists and is on PATH;
   otherwise no symlink (the operator can invoke the full path or
   add `~/.claude-director/bin` to PATH manually).

## What this skill does NOT do

- It does NOT modify any per-Spawn hooks. Those are injected inline
  via `--settings` at spawn time and are NOT persistent in the
  user's `settings.json`.
- It does NOT touch existing user hooks in any event.
- It does NOT install `claude` or `tmux` themselves. Those are
  pre-flight requirements.

## Uninstall

Run `uninstall.sh` from the same directory:

- Removes the two help hook entries (only the entries this skill
  added; other user hooks are preserved).
- Removes the binary at `~/.claude-director/bin/claude-director` and
  any versioned siblings.
- Unlinks the PATH symlink if one was created.
- With `--purge`: also removes `~/.claude-director/` entirely
  (including state.db + templates). Requires confirmation unless
  `--force` is supplied.
- With `--mcp-also`: runs `claude mcp remove claude-director`.

## Upgrade rollback

The previous binary is retained at its version-suffixed path. To
roll back: `ln -sfn ~/.claude-director/bin/claude-director.v<old>
~/.claude-director/bin/claude-director`. There's no automatic rollback
because v1 has no migration story (per SRD §19 Q11); a schema-incompat
upgrade means `rm state.db` and re-warm.

## ErrSchemaMismatch recovery

If `claude-director help` reports `ErrSchemaMismatch` after an
upgrade:

1. Inspect: `sqlite3 ~/.claude-director/state.db "PRAGMA user_version"`.
2. v1 has no migrations: `rm ~/.claude-director/state.db*`.
3. Re-run `claude-director help` to recreate at the current version.

Spawn history is lost, but live Spawns whose `claude_instance_id`
is in the operator's notes can be re-resumed via `claude-director
resume` — the JSONL transcripts persist in `~/.claude/projects/`
independently of our DB.
