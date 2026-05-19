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

### How to phrase each question

**Assume the operator has never seen this project before.** Don't lead
with the flag name. Each `AskUserQuestion` must include four parts,
in this order:

1. **What this choice means** — one sentence of plain-English context.
   What is `--symlink-dir`? What is MCP? Don't assume.
2. **The options, with the trade-off** — not just "a, b, or c" but
   *why you'd pick each one*. Mark the recommended option.
3. **The default**, clearly labeled, and *why* it's the default given
   what was detected about this machine.
4. **What happens if they get it wrong** — one phrase. Is it
   reversible? Does it break things, or just produce a suboptimal
   setup the next uninstall can fix?

If a question reads like "Binary source (`--binary <path>`): use this,
or point elsewhere?" you have failed. That is a question for someone
who already knows what the script does. Rewrite it.

### The four questions

1. **Where should the binary come from? (`--from-release` / `--binary <path>`)**

   - *What this is:* claude-director is a single Go binary. The
     install script copies that binary into `~/.claude-director/bin/`
     and adds it to your PATH. We need to know where to copy *from*.
   - *Four options:*
     - **(a) Download a pre-built release from GitHub** *(recommended
       for new users)*. The script `curl -L`s the right asset for
       your OS/arch from
       `https://github.com/gabemahoney/claude-director/releases/latest`.
       No Go toolchain needed. Flag: `--from-release`.
     - **(b) Use a binary already built or downloaded locally.** If
       you've run `make build` in a checkout, point at `./bin/claude-director`.
       If you downloaded a tarball yourself, point at that. Flag:
       `--binary <path>`.
     - **(c) Use whatever `claude-director` is on `PATH` today.** Only
       makes sense if you're re-installing or upgrading an existing
       install. The script falls back to this automatically if
       neither (a) nor (b) is specified.
     - **(d) Build from source now, then install.** Run `make build` in
       this checkout to produce a fresh `./bin/claude-director`, then
       point install.sh at it. No install.sh change needed: the
       orchestrator runs `make build` first, then
       `install.sh --binary ./bin/claude-director`. Prereq: Go 1.22+ on
       PATH.
   - *Default:* depends on what the orchestrator detects about the
     launch environment.
     - **In a checked-out tree with Go available** → propose **(d)**
       *build from source*. This installs the binary that matches the
       operator's current source tree, which is almost always what they
       actually want.
     - **In a checked-out tree but no Go (or `make build` would fail)**
       → propose **(a)** *download from release* and SAY SO. Don't try
       to use a possibly-stale `./bin/claude-director`.
     - **Not in a checked-out tree** (install.sh was curled to a tmp
       path, or invoked from an arbitrary directory) → propose **(a)**
       *download from release*.
     - **`./bin/claude-director` exists but operator wants it
       specifically** → that's option (b); they ask for it explicitly.
       Never default to it.

   **Why we don't default to the existing local binary:** a binary at
   `./bin/claude-director` may have been built off a stale branch or a
   previous session's incomplete edit. Installing it silently
   substitutes "trust what was compiled before" for the operator's
   likely intent of "install the current source". Always force a fresh
   build or a release download unless the operator explicitly says "use
   the binary at this path".

   - *Reversibility:* picking wrong is cheap. The next
     `uninstall.sh --purge` resets to a clean slate, and you can
     re-run `install.sh` with a different source any time.

   If `--from-release` is selected but the repo has no releases yet,
   the script exits with a clear error. Don't paper over that —
   surface it to the operator and loop back to (b) or (c).

2. **Should the binary go on `PATH` via a symlink? (`--symlink-dir <dir>` or `--no-symlink`)**

   - *What this is:* the binary lives at
     `~/.claude-director/bin/claude-director`. For you to type
     `claude-director` from any shell, that directory needs to be on
     `PATH`, **or** we need to drop a symlink somewhere that already
     is. A symlink is a file that points at another file — running it
     runs the target.
   - *Options:*
     - **(a) Drop a symlink in `~/.local/bin`** *(recommended if it
       exists and is on PATH)*. This is the standard place for
       per-user binaries on Linux/macOS.
     - **(b) Drop a symlink in some other PATH directory** (e.g.
       `~/bin`, `/usr/local/bin`). Pass the directory.
     - **(c) Skip the symlink entirely (`--no-symlink`)**. You invoke
       claude-director via the full path
       `~/.claude-director/bin/claude-director`, or add that bin/
       directory to `PATH` yourself.
   - *Default:* (a) if `~/.local/bin` exists and is already on
     `PATH`. Otherwise ask explicitly — don't silently fall back to
     (c), because the operator probably wants a working command.
   - *Reversibility:* fully reversible. The uninstall script removes
     any symlink it created. If you skip and want one later, re-run
     `install.sh --symlink-dir <dir>`.

3. **Version label for this install (`--version v<N>`)**

   - *What this is:* on upgrade, the script keeps the previous
     binary at a *versioned* path
     (`claude-director.v0.1.0`, etc.) and atomically swaps the
     unversioned `claude-director` symlink to the new one. The
     `--version` flag controls the suffix used for that side-by-side
     copy. It's purely a label — it doesn't gate features.
   - *Options:*
     - **(a) Let the script auto-detect.** It runs `<binary> --version`
       and uses whatever that prints.
     - **(b) Supply a semver tag yourself**, e.g. `v0.1.0`. Useful if
       you're installing a specific tagged release and want the
       on-disk artifact to match.
   - *Default:* (a). For v1 the binary doesn't yet implement
     `--version`, so the script falls back to a `t<timestamp>`
     suffix automatically. **Say this in the question** — don't let
     the operator be surprised by `claude-director.t20260519-130000`
     showing up on disk.
   - *Reversibility:* completely cosmetic. Wrong label → next
     upgrade overwrites it. Rollback uses the versioned-path naming;
     if your label is gibberish, rollback still works but reads
     funny.

4. **Register the MCP server with Claude Code? (`--register-mcp`)**

   - *What this is:* MCP (Model Context Protocol) is how Claude Code
     learns about external tool servers it can call. Registering
     claude-director as an MCP server makes its verbs (`spawn`,
     `send-keys`, `read-pane`, etc.) callable *from inside Claude
     Code sessions* as `mcp__claude-director__spawn` and friends —
     i.e., an orchestrating Claude can drive a Spawn without
     shelling out. Without this, you can still use claude-director
     by typing `claude-director ...` in a shell.
   - *Options:*
     - **(a) Register now (`--register-mcp`).** The script runs
       `claude mcp add claude-director <path> serve --stdio`. Pick
       this if you plan to have one Claude orchestrate other
       Claudes.
     - **(b) Skip.** Pick this if you'll only invoke claude-director
       from scripts or your shell.
   - *Default:* (b) — OFF. Registering MCP is a power-user feature;
     defaulting it on would clutter the MCP list of operators who
     don't need it.
   - *Reversibility:* fully reversible. To add it later:
     `claude mcp add claude-director ~/.claude-director/bin/claude-director serve --stdio`.
     To remove: `claude mcp remove claude-director` or
     `uninstall.sh --mcp-also`.

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
   - `--from-release [tag]` if supplied — the script downloads the
     asset for this host's OS/arch from GitHub Releases (resolving
     the latest tag via `gh` or `curl + jq` if none was given), OR
   - `--binary <path>` if supplied, OR
   - `$(dirname $0)/../../bin/claude-director` (the in-repo build) if
     this skill was invoked from a checked-out tree, OR
   - the currently-running `claude-director` resolved via `command -v`.

   With `--from-release`, an optional `--sha256 <hex>` flag verifies
   the downloaded asset against an expected hash before install.

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

Same content-shape rule as the install dialog: each question must
include (1) what the choice means in plain English, (2) the
trade-off between the options, (3) the default and why, (4) what's
at stake if you pick wrong — and here especially, **what is
irreversible**. Uninstall deletes things; the operator needs to know
which deletes are recoverable from the filesystem and which are not.

What the script removes *unconditionally* (the operator does not need
to opt into these): the two help-hooks injected into
`~/.claude/settings.json`, the binary under
`~/.claude-director/bin/`, and the PATH symlink if one exists. State
the baseline up front so the questions are only about the
destructive *additions*.

1. **Also delete `state.db` and templates? (`--purge`)**

   - *What this is:* by default uninstall removes the binary and
     hooks but leaves `~/.claude-director/` itself in place — so
     `state.db` (the SQLite database with every Spawn's id,
     transcript pointer, and history) and any templates you've
     created with `make-template` survive. `--purge` adds
     `rm -rf ~/.claude-director/` on top.
   - *Options:*
     - **(a) Keep state (default).** Re-installing later picks up
       your existing Spawn history and templates.
     - **(b) Purge everything (`--purge`).** Clean-slate
       uninstall. Good if you're done with claude-director for
       good, or troubleshooting a corrupt state.db.
   - *Default:* (a). Don't delete user data without an explicit
     ask.
   - *Reversibility:* **(b) is destructive and irreversible.**
     `state.db` is not backed up. Templates are not backed up. The
     JSONL transcripts of each Spawn live under
     `~/.claude/projects/` and survive — but their mapping to
     claude_instance_ids is in `state.db`, so a purge means you'd
     have to grep transcripts by hand to find a specific session.

2. **Skip the script's `[y/N]` safety prompt? (`--force`)** *(only ask if (b) above was chosen)*

   - *What this is:* with `--purge`, `uninstall.sh` prints
     `--purge will rm -rf ~/.claude-director/ — proceed? [y/N]`
     and waits for a reply. `--force` suppresses that prompt.
   - *Options:*
     - **(a) Keep the prompt (default).** One more chance to back
       out at the shell.
     - **(b) Skip it (`--force`).** The `AskUserQuestion` you just
       answered counts as confirmation; the extra prompt is
       redundant.
   - *Default:* (a). Belt-and-suspenders by default; the operator
     can opt into (b) explicitly.
   - *Reversibility:* once `--force` plus `--purge` runs, the
     directory is gone with no further chance to abort. The
     `--force` flag itself does nothing without `--purge`.

3. **Also deregister the MCP server? (`--mcp-also`)**

   - *What this is:* if you installed with `--register-mcp`,
     Claude Code remembers claude-director in its MCP server
     list. Without `--mcp-also`, that registration outlives the
     uninstall — Claude Code will still list claude-director but
     fail to connect to it.
   - *Options:*
     - **(a) Leave the MCP registration alone (default).** Pick
       this if you never registered MCP, or you want to keep the
       registration for a later re-install.
     - **(b) Also run `claude mcp remove claude-director`.** Pick
       this if you registered MCP and want a clean
       no-claude-director-at-all state.
   - *Default:* (a). The `claude mcp remove` command is harmless
     if there's no registration, but defaulting it on would imply
     the operator registered MCP — which they may not have.
   - *Reversibility:* completely reversible. Re-register at any
     time with
     `claude mcp add claude-director ~/.claude-director/bin/claude-director serve --stdio`.

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
