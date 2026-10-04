---
name: install-agent-director
description: Install (or upgrade) agent-director on this machine. Runs the bundled install.sh against the user's ~/ — creates ~/.agent-director/ with the binary and the off-PATH operator tool agent-director-admin, migrates and opens state.db (authorizing any older-schema upgrade via a one-shot sentinel), and injects two persistent `agent-director help` hooks into ~/.claude/settings.json (SessionStart + SessionEnd reason=compact). Use this skill when the user says "install agent-director", "set up agent-director", or "upgrade agent-director on this machine".
---

## First-time install on a brand-new machine

If this skill isn't yet on the machine (no checked-out repo, no
cached `install.sh`), bootstrap the CLI directly from `main` with the
one-liner advertised in the customer-facing README:

```sh
curl -fsSL https://raw.githubusercontent.com/gabemahoney/agent-director/main/skills/install-agent-director/install.sh | bash -s -- --from-release
```

The rest of this skill assumes `install.sh` is already on disk (in a
checkout or alongside SKILL.md). Use the one-liner above to bridge the
gap on a fresh host, then run the interactive flow below on subsequent
upgrades.

## When to invoke

Trigger phrases: "install agent-director", "set up agent-director",
"upgrade agent-director on this machine".

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

### How the install writes the binaries

Every install writes two binaries from the same build, because both
open the same store:

- `agent-director`, at `~/.agent-director/bin/agent-director` (and,
  optionally, on PATH through a symlink; question 2);
- the operator tool `agent-director-admin`, at
  `~/.agent-director/admin/agent-director-admin` (directory mode 0700,
  binary 0755). It is **never** put on PATH and never symlinked, under
  any option. It is for a human's repair actions only; its help opens
  with the rule that nobody runs it without a human's explicit approval
  for that run, and agents never run it. **Never run it yourself while
  installing**: install.sh checks its version stamp on its own. Tell the
  operator where it is (install.sh prints its path once, at the end).

install.sh refuses to install (exit 3) unless both binaries' `version`
stamps (version and commit) are the same and carry a real commit. A
plain `go build` stamps commit `unknown`, which cannot show that two
binaries come from one build, so such a pair is refused even when the
stamps match; `make build` in a git checkout stamps both with the
checkout's commit, and release assets are stamped.

The install script first copies both new binaries into sibling temp
files in their directories (staging), and only once both are staged
`mv`s each over its canonical path, back to back. A failure while
staging (a full disk, an unwritable admin directory) replaces neither
binary, so an install never leaves a new `agent-director` beside an old
`agent-director-admin`. `mv` within one filesystem is atomic at the
inode level, so concurrent readers see either the old binary or the
new — never a half-written file. A running process (say, an in-flight
Spawn whose hooks reference this binary) holds the old inode, so its
current exec is unaffected by the swap.

The previous binaries are *not* retained by default. Pass `--keep-prior`
on upgrade to have install.sh snapshot both existing binaries before
either new one is staged, to `~/.agent-director/bin/agent-director.prior` and
`~/.agent-director/admin/agent-director-admin.prior`. That gives you a
one-step rollback of the matching pair (`mv .prior canonical` for
each). On an upgrade from a release before 0.11.0 there is no
agent-director-admin to snapshot: install.sh removes any stale admin
`.prior`, prints an `admin prior: none` line, and rolling back means
removing `~/.agent-director/admin/agent-director-admin`. Without
`--keep-prior`, re-install the previous tag via
`install.sh --from-release v<old>` (this install.sh refuses a tag before
0.11.0, which has no agent-director-admin).

### The four questions

1. **Where should the binaries come from? (`--from-release` / `--binary <path>` with `--admin-binary <path>`)**

   - *What this is:* agent-director is a Go binary that comes with a
     second one from the same build, the operator tool
     `agent-director-admin`. The install script copies
     `agent-director` into `~/.agent-director/bin/` (and onto your
     PATH) and `agent-director-admin` into
     `~/.agent-director/admin/`, never on PATH. We need to know where to
     copy both *from*; they must come from the same build.
   - *Four options:*
     - **(a) Download a pre-built release from GitHub** *(recommended
       for new users)*. The script `curl -L`s both assets for your
       OS/arch (`agent-director-<os>-<arch>` and
       `agent-director-admin-<os>-<arch>`) from
       `https://github.com/gabemahoney/agent-director/releases/latest`.
       No Go toolchain needed. Flag: `--from-release [tag]`. To verify
       the two downloads, pass both `--sha256 <hex>` (the
       `agent-director` asset) and `--admin-sha256 <hex>` (the
       `agent-director-admin` asset), or neither: exactly one is refused
       before anything is downloaded (exit 2, "--sha256 without
       --admin-sha256 would install agent-director-admin unverified;
       refusing to install.", or the mirror), so ask for both hashes or
       none. Only releases 0.11.0 and later ship
       `agent-director-admin`: a tag before 0.11.0 is refused at once
       (exit 3, "release <tag> has no agent-director-admin binary"),
       with no retry; pick 0.11.0 or later.
     - **(b) Use binaries already built or downloaded locally.** If
       you've run `make build` in a checkout, point at
       `./bin/agent-director` and `./bin/agent-director-admin` (`make
       build` builds both; install.sh also finds the admin binary
       there on its own). If you downloaded the two release assets
       yourself, point at both. Flags: `--binary <path>
       --admin-binary <path>`.
     - **(c) Use whatever `agent-director` is on `PATH` today.** Only
       makes sense if you're re-installing an existing install, and
       only together with a matching admin binary
       (`--admin-binary ~/.agent-director/admin/agent-director-admin`
       for a re-install): `agent-director-admin` is never looked up on
       PATH, so it must come from `--admin-binary` (or the checkout's
       `bin/`). With no admin binary given and neither binary beside
       the script, install.sh refuses once (exit 3), naming both
       `--binary` and `--admin-binary`.
     - **(d) Build from source now, then install.** Run `make build` in
       this checkout to produce fresh `./bin/agent-director` and
       `./bin/agent-director-admin`, then point install.sh at them. No
       install.sh change needed: the orchestrator runs `make build`
       first, then
       `install.sh --binary ./bin/agent-director --admin-binary ./bin/agent-director-admin`.
       Prereq: Go 1.22+ on PATH.
   - *Default:* depends on what the orchestrator detects about the
     launch environment. As of b.q3b, install.sh refuses option (b)
     when `./bin/agent-director`'s embedded commit doesn't match
     `git rev-parse HEAD` — so "use the local binary" is only proposed
     when it's provably fresh.
     - **In a checked-out tree, Go available, `./bin/agent-director`
       and `./bin/agent-director-admin` both exist AND
       `./bin/agent-director version` reports a `commit` that
       matches `git rev-parse HEAD`** → propose **(b)** *use the
       local binaries*. They're the artifacts of this exact source
       tree; no rebuild needed. (Read the stamp with
       `./bin/agent-director version` only; install.sh compares the
       admin binary's stamp itself.)
     - **In a checked-out tree with Go available but no fresh local
       binary** (missing, or `commit` doesn't match HEAD) → propose
       **(d)** *build from source*. The orchestrator runs `make build`
       to produce a binary that matches the current tree, then points
       install.sh at it.
     - **In a checked-out tree but no Go (or `make build` would fail)**
       → propose **(a)** *download from release* and SAY SO. Don't try
       to use a possibly-stale `./bin/agent-director`.
     - **Not in a checked-out tree** (install.sh was curled to a tmp
       path, or invoked from an arbitrary directory) → propose **(a)**
       *download from release*.

   **Why the version-check matters:** absent a check, a binary at
   `./bin/agent-director` may have been built off a stale branch or a
   previous session's incomplete edit. Installing it silently
   substitutes "trust what was compiled before" for the operator's
   likely intent of "install the current source". install.sh now reads
   the binary's `version` verb and refuses option (b) unless the
   embedded commit matches HEAD — so the orchestrator can safely
   default to (b) when fresh, and falls through to (d)/(a) when not.
   Separately, for every option, install.sh refuses (exit 3) unless
   `agent-director` and `agent-director-admin` report the same
   `version` stamp (version and commit), with a real commit: mixing
   binaries from two builds, which both open the same store, is never
   installed ("version stamps differ"), and neither is a pair whose
   commit is `unknown` or empty, as a plain `go build` reports ("carry
   no commit stamp, so they cannot be shown to come from the same
   build"). The fix for both is `make build` in a git checkout (both
   binaries again, stamped) or `--from-release`.

   - *Reversibility:* picking wrong is cheap. The next
     `uninstall.sh --purge` resets to a clean slate, and you can
     re-run `install.sh` with a different source any time.

   If `--from-release` is selected but the repo has no releases yet,
   the script exits with a clear error. Don't paper over that —
   surface it to the operator and loop back to (b) or (d).

2. **Should the binary go on `PATH` via a symlink? (`--symlink-dir <dir>` or `--no-symlink`)**

   - *What this is:* the binary lives at
     `~/.agent-director/bin/agent-director`. For you to type
     `agent-director` from any shell, that directory needs to be on
     `PATH`, **or** we need to drop a symlink somewhere that already
     is. A symlink is a file that points at another file — running it
     runs the target. This question is about `agent-director` only:
     `agent-director-admin` never gets a symlink and is never put on
     PATH, whatever you pick; don't add `~/.agent-director/admin/` to
     PATH either.
   - *Options:*
     - **(a) Drop a symlink in `~/.local/bin`** *(recommended if it
       exists and is on PATH)*. This is the standard place for
       per-user binaries on Linux/macOS.
     - **(b) Drop a symlink in some other PATH directory** (e.g.
       `~/bin`, `/usr/local/bin`). Pass the directory.
     - **(c) Skip the symlink entirely (`--no-symlink`)**. You invoke
       agent-director via the full path
       `~/.agent-director/bin/agent-director`, or add that bin/
       directory to `PATH` yourself.
   - *Default:* (a) if `~/.local/bin` exists and is already on
     `PATH`. Otherwise ask explicitly — don't silently fall back to
     (c), because the operator probably wants a working command.
   - *Reversibility:* fully reversible. The uninstall script removes
     any symlink it created. If you skip and want one later, re-run
     `install.sh --symlink-dir <dir>`.

3. **Register the MCP server with Claude Code? (`--register-mcp`)**

   - *What this is:* MCP (Model Context Protocol) is how Claude Code
     learns about external tool servers and exposes their operations
     as first-class typed tools. Registering agent-director as an
     MCP server makes its verbs (`spawn`, `send-keys`, `read-pane`,
     etc.) appear in any Claude session's tool list as
     `mcp__agent-director__spawn` and friends — typed inputs,
     structured outputs, schema-validated, no shell-escaping
     headaches. *This does NOT affect whether Claudes can drive
     agent-director* — they can call the CLI via their `Bash` tool
     either way. It only changes whether agent-director's API is
     exposed as a typed tool surface or has to be shelled out to.
   - *Options:*
     - **(a) Register now (`--register-mcp`).** The script runs
       `claude mcp add agent-director -- <path> serve --stdio`.
       agent-director's verbs become typed MCP tools in every
       Claude session. Pick this for orchestrator setups, or any
       workflow where you want Claudes to discover agent-director's
       API automatically.
     - **(b) Skip.** agent-director stays a CLI: a Claude can still
       call it via `Bash` (e.g. `agent-director spawn ...`), and
       humans use it from the shell. Pick this if you don't want it
       cluttering every session's MCP tool list, or you'll only call
       it from scripts/cron.
   - *Default:* (b) — OFF. MCP registration is the right call for
     orchestrator setups, but off-by-default keeps the tool list
     lean for operators who don't need it. Easy to flip on later.
   - *Reversibility:* fully reversible. To add it later:
     `claude mcp add agent-director -- ~/.agent-director/bin/agent-director serve --stdio`.
     To remove: `claude mcp remove agent-director` or
     `uninstall.sh --mcp-also`.

4. **Inject persistent help hooks into `~/.claude/settings.json`? (`--no-hooks`)**

   - *What this is:* agent-director ships a `help` verb that prints
     its entire manifest (every verb, every error code) as JSON. The
     install adds two hook entries to `~/.claude/settings.json` that
     fire `agent-director help` at two moments:
       - **SessionStart** — when any Claude Code session starts under
         your user, the manifest is piped into the new session's
         context, so the model knows the verb surface from turn 1.
       - **SessionEnd with matcher=compact** — fires just before
         `/compact` truncates context, so the post-compact Claude
         still knows what verbs exist.
     Without these hooks, every Claude that wants to drive
     agent-director has to be hand-told what verbs exist — either by
     pasting `agent-director help` into a prompt, or by relying on
     the operator-Claude to introduce the API surface.
   - *Options:*
     - **(a) Inject the hooks** *(recommended for almost all
       installs)*. The merge is additive — existing user hooks at
       those events are preserved, and re-running the install is
       idempotent (duplicate entries are detected and skipped). The
       install also snapshots `~/.claude/settings.json` to a
       timestamped `.bak` first.
     - **(b) Skip the hook injection (`--no-hooks`)**. Pick this if
       you want to manage how Claudes learn about agent-director
       yourself (e.g. via per-project CLAUDE.md, hand-paste, or some
       other mechanism). With this flag, settings.json is left
       byte-identical to its pre-install state — no edit, no .bak.
   - *Default:* (a) — inject. This is the single biggest reason
     install.sh exists over a bare binary copy. Defaulting to skip
     would make the install almost useless for the orchestrator use
     case.
   - *Reversibility:* fully reversible. `uninstall.sh` removes the
     two entries (preserving any other user hooks at those events),
     and the pre-edit `.bak` snapshot is retained for manual
     rollback. Re-injecting later is a re-run of `install.sh`
     without `--no-hooks`.

5. **Confirm and execute**
   - Display the assembled `bash install.sh <resolved flags>` command
     line back to the operator.
   - Ask "ready to run?" with `AskUserQuestion`. Only on an explicit
     "yes" execute the script. A "no" or any modification answer means
     loop back to the relevant question, not silently re-pick.
   - If this is an upgrade and the operator wants a single-step
     rollback path, add `--keep-prior` (it snapshots both binaries, so
     the rollback restores a matching pair). Otherwise leave it off;
     a re-install with `--from-release v<old>` is the fallback.
   - After the run, tell the operator where `agent-director-admin` was
     installed (install.sh's `admin   :` line, just before
     `install.sh: done`) and that it is not on PATH. Do not run it.

Do NOT skip this dialog because flags "look obvious from context".
The operator may want a non-default path, MCP off, or a `--keep-prior`
rollback snapshot. Inferring intent is the failure mode this section
exists to prevent.

## What this skill does

This skill runs `install.sh` from the same directory. The script:

1. **Pre-flights.** First, while parsing flags, it refuses (exit `2`)
   an unknown flag; `--from-release` with `--binary` or
   `--admin-binary`; `--sha256` or `--admin-sha256` without
   `--from-release` ("only applies with --from-release") or not 64
   lowercase hex characters; and exactly one of `--sha256` and
   `--admin-sha256` ("would install <the other binary> unverified;
   refusing to install"). Then it runs the
   following checks in order; any failure aborts with a clear message
   and exit code `2`, or `3` where noted:

   1. **Whitespace-in-install-path** — `$HOME` must not contain
      whitespace (SRD §4.3; tmux's direct-argv invocation requires
      shell-safe paths).
   2. **OS/CPU gate (SR-2.1).** `uname -s` / `uname -m` must report
      one of the supported tuples: `Linux/x86_64` or `Darwin/arm64`.
      Every other combination (Windows, FreeBSD, `Linux/aarch64`,
      `Darwin/x86_64`, etc.) is hard-refused with a message naming
      the supported set and referencing Idea Bee `b.fg3` for
      cross-platform expansion status.
   3. **Required tools on PATH.** `claude`, `tmux`, `jq`, `file`,
      and `sqlite3` must all resolve via `command -v`. The `file(1)`
      tool is mandatory because step 6 below relies on it to probe
      `--binary` artifacts (never silent-skip per SR-2.2);
      install via `apt install file` / `brew install file-formula`
      / `dnf install file`. `sqlite3` is mandatory for the
      schema-migration flow (below): the script reads state.db's
      ACTUAL `user_version` through the WAL with
      `sqlite3 "PRAGMA user_version"` — raw header bytes are subtly
      wrong for a WAL-mode DB — both to decide whether a migration
      sentinel is needed and to verify the post-open version;
      install via `apt install sqlite3` / `brew install sqlite` /
      `dnf install sqlite`. `curl` is also required when
      `--from-release` is supplied.
   4. **`--from-release` resolution** (if applicable) — downloads
      both matching assets for `$(uname -s)`/`$(uname -m)` from GitHub
      Releases (`agent-director-<os>-<arch>` and
      `agent-director-admin-<os>-<arch>`); `--sha256` verifies the first
      and `--admin-sha256` the second (both or neither, as above), and a
      mismatch aborts with exit 3, installing nothing. A release before
      0.11.0 has no `agent-director-admin` asset and is refused at once
      (exit 3), with no CDN retry: install 0.11.0 or later.
   5. **`--binary` / `--admin-binary` path/executability resolution** —
      settles `BINARY_SRC` from `--binary <path>`, the in-repo build, or
      `command -v agent-director`, and `ADMIN_SRC` from
      `--admin-binary <path>`, the downloaded release asset, or the
      in-repo `bin/agent-director-admin` (never from PATH); verifies
      each is an executable regular file. A missing binary is refused
      with exit 3; with neither binary beside the script, one combined
      refusal names both `--binary` and `--admin-binary`.
   6. **Architecture probe (SR-2.2)**, for `--binary` and
      `--admin-binary` alike. Runs `file(1)` against `BINARY_SRC` and
      `ADMIN_SRC` and pattern-matches against the host pair captured by
      step 2:
      - `Linux/x86_64`: file output must contain `ELF 64-bit LSB`
        AND (`x86-64` OR `x86_64`).
      - `Darwin/arm64`: file output must contain `Mach-O` AND
        (`arm64` OR `arm64e`).

      Mismatch (e.g. operator passes a darwin-arm64 artifact on
      Linux/x86_64) aborts with exit `2` and a message naming the
      flag, the binary, the detected architecture excerpt, and the
      host pair.
      The probe is independent of the OS/CPU gate above: even on a
      supported host, a wrong-arch binary is refused here.
   7. **Source-tree version check** — when the binary came from a
      local source (`--binary` or the in-repo build) AND install.sh
      lives inside a git checkout, the binary's embedded commit
      must match `HEAD`. Catches the "operator forgot to
      `make build` after pulling new code" footgun.
   8. **Version-stamp pairing** — `agent-director version` and
      `agent-director-admin version` must report the same version and
      commit; otherwise (or when one stamp cannot be read) the install
      is refused with exit 3 ("version stamps differ"). Equal stamps
      whose commit is `unknown` or empty (a plain `go build`), or two
      unreadable stamps, are refused with exit 3 too ("carry no commit
      stamp, so they cannot be shown to come from the same build").
      Both refusals advise `make build` (in a git checkout it stamps
      both binaries with the checkout's commit) or `--from-release`.
      Both binaries open the same store, so they must come from one
      build.

2. **Creates `~/.agent-director/`** (mode 0700) if missing, plus
   `~/.agent-director/bin/` for the binary and
   `~/.agent-director/admin/` (mode 0700) for the operator tool.

3. **Copies the binary** to `~/.agent-director/bin/agent-director`
   (mode 0755). The source is determined by:
   - `--from-release [tag]` if supplied — the script downloads the
     asset for this host's OS/arch from GitHub Releases (resolving
     the latest tag via `gh` or `curl + jq` if none was given), OR
   - `--binary <path>` if supplied, OR
   - `$(dirname $0)/../../bin/agent-director` (the in-repo build) if
     this skill was invoked from a checked-out tree, OR
   - the currently-running `agent-director` resolved via `command -v`.

   With `--from-release`, `--sha256 <hex>` and `--admin-sha256 <hex>`
   (both or neither) verify the two downloaded assets against their
   expected hashes before install.

   It then copies `agent-director-admin` to
   `~/.agent-director/admin/agent-director-admin` (mode 0755) the same
   way, from the source step 5 of the pre-flights settled. It is never
   put on PATH.

   Both new binaries are staged first: each is written to a sibling
   temp file (`agent-director.tmp.$$`, `agent-director-admin.tmp.$$`)
   with its mode, and only once both are staged is each `mv`'d over
   its canonical path, back to back. A failure while staging leaves
   both old binaries in place. `mv` within one filesystem is atomic at
   the inode level — concurrent readers see either the old binary or
   the new, never half; a running process holds the old inode, so an
   in-flight exec is unaffected by the swap. With `--keep-prior` both
   prior binaries are snapshotted (`agent-director.prior`,
   `agent-director-admin.prior`) before either new binary is staged,
   for a one-step rollback of the pair; on an upgrade from a release
   before 0.11.0, which installed no admin binary, a stale admin
   `.prior` is removed instead.

4. **Rejects whitespace in the install path.** Per SRD §4.3 tmux's
   direct-argv invocation does not tolerate spaces in the binary
   path; the script aborts up front rather than failing mysteriously
   later.

5. **Migrates and opens the database — the six-step schema flow
   (SR-1.7).** An older-than-binary `state.db` opens ONLY when an
   administrator has authorized exactly that schema transition; the
   install runs on the end-user's machine as an admin action, so it
   is the legitimate authorizer. See the **"Schema migration: the
   six-step sentinel flow"** section below for the full detail. In
   brief, in order:

   1. **Install the new binary** — already done by step 3/the atomic
      `mv` above.
   2. **Read the DB's ACTUAL `user_version`** via
      `sqlite3 ~/.agent-director/state.db "PRAGMA user_version"`
      (through the WAL — never assume the version, and never read raw
      header bytes). No DB yet (fresh install) → nothing to authorize;
      step 4 fresh-creates it.
   3. **Write the authorization sentinel** — a file
      `~/.agent-director/migrate-authorized` (a sibling of state.db)
      containing `{"from": <actual>, "to": <target>}`, where
      `<target>` is the schema version this binary requires. **Skipped
      when `from == target`** (the DB is already current) and on a
      fresh install (no DB).
   4. **Trigger exactly one store-opening open** — runs
      `agent-director list` once. `list` opens the store, so the
      authorized migration runs and the sentinel is consumed on
      success; on a fresh install this creates `state.db` (mode 0600)
      at the current schema version. **NOT `help`/`version`** — those
      verbs become DB-free (SR-4), so a help-based warmup would never
      open the store and step 5 would fail on every upgrade.
   5. **Verify and fail loudly** — re-reads `user_version` and
      confirms it equals the target. On any mismatch (or if state.db
      wasn't created) the install **aborts non-zero (exit 5)** with a
      clear message; the sentinel, if written, is left unconsumed so a
      re-run retries the migration.
   6. **Brief hook-failure window (accepted).** Between the binary
      swap (step 1/3) and the successful step-4 open there is a short
      (seconds, install-controlled) window in which a concurrently
      firing hook that runs a store-opening verb against the not-yet-
      migrated DB gets the admin migration error. This is expected and
      accepted; it clears the moment step 4 completes.

6. **Injects persistent hooks** into `~/.claude/settings.json`
   (unless `--no-hooks` was passed):
   - `SessionStart` → `agent-director help`
   - `SessionEnd` with matcher `reason=compact` → `agent-director help`

   These re-inject the verb list into a new conversation so the
   model knows the supervision API surface after a `/compact` or
   fresh session. Mirrors how `bees sting` keeps its skill list
   alive across compacts.

   The merge is additive: existing user hooks are preserved. Re-running
   the install is idempotent — duplicate entries are detected and
   skipped. The pre-edit contents of `settings.json` are snapshotted
   to a timestamped `.bak` sibling before the merge writes.

   With `--no-hooks`, this step is skipped entirely: settings.json is
   not read, not backed up, not written — left byte-identical to its
   pre-install state. The post-install summary reports
   `hooks   : skipped (--no-hooks)`.

7. **Optional MCP registration.** With `--register-mcp`, runs
   `claude mcp add agent-director ~/.agent-director/bin/agent-director serve --stdio`.
   Skipped by default — operators who don't want the MCP server
   never see it advertised inside Claude.

8. **Optional PATH symlink.** With `--symlink-dir <dir>`, drops a
   symlink at `<dir>/agent-director` pointing at the canonical
   binary. Default: `~/.local/bin` if it exists and is on PATH;
   otherwise no symlink (the operator can invoke the full path or
   add `~/.agent-director/bin` to PATH manually). `agent-director-admin`
   never gets a symlink, under any option.

9. **Prints the operator tool's path** once, at the end
   (`admin   : ~/.agent-director/admin/agent-director-admin (operator
   tool, not on PATH; ...)`), for the human.

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
`~/.agent-director/bin/`, the operator tool
`~/.agent-director/admin/agent-director-admin` with its `admin/`
directory, and the PATH symlink if one exists. State
the baseline up front so the questions are only about the
destructive *additions*.

1. **Also delete `state.db` and templates? (`--purge`)**

   - *What this is:* by default uninstall removes the binary and
     hooks but leaves `~/.agent-director/` itself in place — so
     `state.db` (the SQLite database with every Spawn's id,
     transcript pointer, and history) and any templates you've
     created with `make-template` survive. `--purge` adds
     `rm -rf ~/.agent-director/` on top.
   - *Options:*
     - **(a) Keep state (default).** Re-installing later picks up
       your existing Spawn history and templates.
     - **(b) Purge everything (`--purge`).** Clean-slate
       uninstall. Good if you're done with agent-director for
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
     `--purge will rm -rf ~/.agent-director/ — proceed? [y/N]`
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
     Claude Code remembers agent-director in its MCP server
     list. Without `--mcp-also`, that registration outlives the
     uninstall — Claude Code will still list agent-director but
     fail to connect to it.
   - *Options:*
     - **(a) Leave the MCP registration alone (default).** Pick
       this if you never registered MCP, or you want to keep the
       registration for a later re-install.
     - **(b) Also run `claude mcp remove agent-director`.** Pick
       this if you registered MCP and want a clean
       no-agent-director-at-all state.
   - *Default:* (a). The `claude mcp remove` command is harmless
     if there's no registration, but defaulting it on would imply
     the operator registered MCP — which they may not have.
   - *Reversibility:* completely reversible. Re-register at any
     time with
     `claude mcp add agent-director ~/.agent-director/bin/agent-director serve --stdio`.

4. **Confirm and execute**
   - Display the assembled `bash uninstall.sh <resolved flags>`.
   - Ask "ready to run?". Only on explicit "yes" execute.

### What uninstall.sh does

- Removes the two help hook entries (only the entries this skill
  added; other user hooks are preserved).
- Removes the binary at `~/.agent-director/bin/agent-director` and
  the `.prior` snapshot if one is present.
- Removes `~/.agent-director/admin/agent-director-admin`, its `.prior`
  snapshot if one is present, and the `~/.agent-director/admin/`
  directory (left in place, with a note, if it holds other files).
- Unlinks the PATH symlink if one was created.
- With `--purge`: also removes `~/.agent-director/` entirely
  (including state.db + templates). Requires confirmation unless
  `--force` is supplied.
- With `--mcp-also`: runs `claude mcp remove agent-director`.

## Schema migration: the six-step sentinel flow

This is the ONE admin-facing place the migration sentinel is
documented. It appears nowhere in any agent-facing surface (help
text, MCP tool descriptions, npm README, or the migration error
message) — those route the operator here, to the install process,
and nowhere else.

### Why a sentinel exists

`state.db` carries a schema `user_version`. When the installed binary
is NEWER than the DB (an upgrade), the store refuses to touch the DB
on its own — it will not silently migrate under an agent. Instead it
requires an administrator to authorize exactly that one transition by
placing a sentinel file next to state.db. `install.sh` is that
administrator action, so the install writes the sentinel for you.

### The sentinel

- **Path:** `~/.agent-director/migrate-authorized` — always a *sibling
  of state.db*, so a custom `--store-path` install authorizes the
  right DB.
- **Shape:** a single JSON object, exactly
  `{"from": <current user_version>, "to": <target schema version>}`.
  It authorizes precisely that one `from → to` transition and nothing
  wider. Unknown fields or trailing data are rejected.
- **One-shot consumption:** the store honors the sentinel only when
  **both** ends match (the DB is really at `from` and the binary
  really wants `to`), runs the migration, and then **deletes the
  sentinel** once the migration commits. A refused open (missing,
  malformed, or mismatched sentinel) executes zero DDL and leaves both
  state.db and the sentinel byte-identical, as admin evidence.

### The six steps `install.sh` performs

1. **Install the new binaries** (atomic `mv` into
   `~/.agent-director/bin/`, and `agent-director-admin` into
   `~/.agent-director/admin/`).
2. **Read the ACTUAL `user_version`** via
   `sqlite3 ~/.agent-director/state.db "PRAGMA user_version"` (through
   the WAL). No DB → fresh install, skip to step 4.
3. **Write the sentinel** `{"from":<actual>,"to":<target>}` beside
   state.db — **skipped when `from == to`** (already current) and on a
   fresh install. `<target>` is the schema version the new binary
   requires; the install learns it from the binary's own migration
   refusal message.
4. **Open the store once** with `agent-director list` (a store-opening
   verb — *not* `help`/`version`, which are DB-free per SR-4). The
   authorized migration runs and the sentinel is consumed; a fresh
   install creates state.db at the current version.
5. **Verify** the post-open `user_version` equals the target;
   otherwise **fail the install loudly** (exit 5), leaving any written
   sentinel unconsumed for a retry.
6. **A brief hook-failure window is accepted.** For the few seconds
   between the binary swap and the successful step-4 open, a hook that
   opens the store sees the migration error; it clears once step 4
   finishes.

### Recovering an older-than-binary DB (the migration path)

If a Claude session reports `ErrSchemaMigrationRequired` — i.e.
state.db is OLDER than the installed binary — the recovery is simply
**re-run this install skill** (or `bash install.sh` with your usual
flags). The install reads the current version, writes the one-shot
sentinel, opens the store to migrate, and verifies the result. There
is no `rm state.db` step and no data loss: the migration preserves
your Spawn history. If the install's step 5 fails verification, do NOT
delete state.db — capture the error and the leftover
`migrate-authorized` sentinel and contact the maintainers.

An operator can also author the sentinel by hand (write the JSON
above, then run any store-opening verb once), but re-running the
install is the supported path and does the version reads and
verification for you.

## Upgrade rollback

If you used `install.sh --keep-prior` on the previous install, the
previous binaries are at `~/.agent-director/bin/agent-director.prior`
and `~/.agent-director/admin/agent-director-admin.prior`. To roll back
the *binaries*, roll back both, so the pair still matches:

    mv ~/.agent-director/bin/agent-director.prior \
       ~/.agent-director/bin/agent-director
    mv ~/.agent-director/admin/agent-director-admin.prior \
       ~/.agent-director/admin/agent-director-admin

If the previous install was a release before 0.11.0, there is no
`agent-director-admin.prior` (install.sh said `admin prior: none`):
roll back by removing `~/.agent-director/admin/agent-director-admin`.

If you didn't pass `--keep-prior`, re-install the previous version via
`install.sh --from-release v<old-tag>` (0.11.0 or later: this install.sh
refuses an older tag, which has no agent-director-admin).

Note a caveat that did not exist before schema migrations: once an
upgrade has migrated state.db forward (newer `user_version`), rolling
the *binary* back to an older version makes that older binary NEWER-
than-DB in reverse — it will report `ErrSchemaMismatch` (see below),
because migrations are forward-only. Roll the binary back only if you
have not yet let the new binary migrate the DB, or be prepared to
restore an older state.db from your own backup.

## ErrSchemaMismatch recovery

`ErrSchemaMismatch` is not the migration case, and the sentinel cannot
fix it. It has two causes you can meet; the error message says which.
Never delete state.db to clear it: that loses every Spawn row and the
store id.

**state.db is NEWER than the binary** (the error says "found
user_version=N, want M" with N greater than M). Migrations only run
forward, and the install will not downgrade a DB.

1. Inspect: `sqlite3 ~/.agent-director/state.db "PRAGMA user_version"`
   and compare against the version the binary expects (shown in the
   error).
2. **Install the agent-director release that matches this schema.**
   This loses nothing; you likely rolled the binary back below the DB.
   Re-run this install skill with `--from-release` (latest) or point
   it at a newer binary.

**state.db has no valid store id** (the error says "store has no
valid store id"): the DB is at the binary's version but its
`store_meta` table or `store_id` row is missing or malformed. Only a
hand edit of state.db leaves it that way. Restore the copy of
state.db (with its `-wal` and `-shm` files) taken before the install
(install.sh does not make one), then re-run the install so it
migrates that copy. Writes made since the install are lost.

The JSONL transcripts under `~/.claude/projects/` persist
independently of state.db in every case.
