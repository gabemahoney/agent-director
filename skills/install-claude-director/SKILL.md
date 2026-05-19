---
name: install-claude-director
description: Install (or upgrade) claude-director on this machine. Runs the bundled install.sh against the user's ~/ — creates ~/.claude-director/ with the binary, warms up state.db, and injects two persistent `claude-director help` hooks into ~/.claude/settings.json (SessionStart + SessionEnd reason=compact). Use this skill when the user says "install claude-director", "set up claude-director", or "upgrade claude-director on this machine".
---

## When to invoke

Trigger phrases: "install claude-director", "set up claude-director",
"upgrade claude-director on this machine".

## Operator dialog (do BEFORE running install.sh)

`install.sh` has flags that materially change the install. Do NOT pick
them silently. Walk the operator through each choice with
`AskUserQuestion`, echo the resolved flag set back for confirmation,
then execute. Keep it tight — four questions, then a confirm.

1. **Binary source (`--binary <path>`)**
   - In a checked-out tree, propose `./bin/claude-director` (resolved
     to an absolute path) as the default. Confirm the operator wants
     this binary, or accept an explicit path.
   - If neither the in-repo build nor `command -v claude-director`
     resolves, surface that to the operator instead of guessing.

2. **PATH symlink (`--symlink-dir <dir>` or `--no-symlink`)**
   - If `~/.local/bin` exists AND is on `PATH`, offer it as the
     default.
   - Otherwise ask explicitly. Offer `--no-symlink` (invoke via the
     full `~/.claude-director/bin/claude-director` path) as a
     first-class choice — don't bury it.

3. **Version tag (`--version v<N>`)**
   - The binary may not yet support `--version`. If `"$BINARY" --version`
     fails, the script falls back to a `t<timestamp>` suffix. SAY SO
     in the dialog — don't silently fall through.
   - Offer the operator the chance to supply a semver (e.g. `v1.0.0`)
     if they have one.

4. **MCP registration (`--register-mcp`)**
   - Default OFF. Ask: "register the stdio MCP server with `claude` so
     this binary's verbs are advertised inside Claude Code sessions?"
   - If yes, show the exact command that will be run:
     `claude mcp add claude-director <CANONICAL> serve --stdio`.

5. **Confirm and execute**
   - Display the assembled `bash install.sh <resolved flags>` command
     line back to the operator.
   - Ask "ready to run?" with `AskUserQuestion`. Only on an explicit
     "yes" execute the script. A "no" or any modification answer means
     loop back to the relevant question, not silently re-pick.

Do NOT skip this dialog because flags "look obvious from context".
The operator may want a non-default path, MCP off, or a specific
version label. Inferring intent is the failure mode this section
exists to prevent.

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

### Operator dialog (do BEFORE running uninstall.sh)

Uninstall has destructive flags that erase state and external
registrations. Drive an `AskUserQuestion` dialog for each before
invoking the script. Three questions, then a confirm.

1. **Purge state (`--purge`)**
   - Default OFF. Ask: "also `rm -rf ~/.claude-director/`? This
     deletes `state.db` (Spawn history, schema version) and any
     templates under that directory. Hook entries and the binary
     are removed regardless of this answer."
   - If yes, name the directory and the files at stake explicitly
     in the question — operators should know what they're losing.

2. **Skip the purge confirm prompt (`--force`)**
   - Only ask if the operator chose `--purge`. Default OFF.
   - Wording: "skip the script's interactive `[y/N]` confirm? The
     dialog you just answered already counts as confirmation."
   - Choosing `--force` here is fine, but require an explicit
     "yes, skip the prompt" — do NOT bundle it with `--purge`
     silently.

3. **Deregister MCP (`--mcp-also`)**
   - Default OFF. Ask: "also run `claude mcp remove claude-director`
     so it disappears from Claude Code's MCP server list?"
   - Only relevant if the operator originally installed with
     `--register-mcp`. Mention that explicitly.

4. **Confirm and execute**
   - Display the assembled `bash uninstall.sh <resolved flags>`.
   - Ask "ready to run?". Only on explicit "yes" execute.

### What uninstall.sh does

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
