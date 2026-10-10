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
each). A re-install of the same pair (agent-director already
byte-identical to the one being installed, and agent-director-admin
byte-identical too or not installed) is not snapshotted, so re-running
the same install (for example after an exit 5) keeps the `.prior`
files from the earlier run; the `prior` and `admin prior` lines then
say `not snapshotted`. Any other install snapshots both, even when
only one of them changed. On an upgrade from a release before 0.11.0
there is no agent-director-admin to snapshot: install.sh removes any
stale admin `.prior`, prints an `admin prior: none` line, and rolling
back means removing `~/.agent-director/admin/agent-director-admin`.
Without `--keep-prior`, re-install the previous tag via
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
       makes sense if you're re-installing an existing install (to
       re-inject the hooks or register MCP, say). No binary flag: run
       from the installed skill, outside any checkout, install.sh pairs
       the `agent-director` on PATH with the installed
       `~/.agent-director/admin/agent-director-admin`. For the full
       lookup order, see "Where install.sh looks for each binary" in
       step 6 of "What this skill does".
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
       path, or invoked from an arbitrary directory), `agent-director`
       on PATH, `~/.agent-director/admin/agent-director-admin` present,
       and the operator wants to re-inject the hooks or register MCP →
       propose **(c)** *use what is on PATH* (no binary flag).
     - **Not in a checked-out tree** otherwise, or the operator wants
       to upgrade → propose **(a)** *download from release*.

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
   - If install.sh exits non-zero, see "When install.sh fails" below.
     On exit 5, act on its last stderr line,
     `install.sh: err_name=<Name>`, never on the text above it.

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
   and exit code `2`, or `3`, `4` or `5` where noted:

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
      tool is mandatory because step 7 below relies on it to probe
      `--binary` artifacts (never silent-skip per SR-2.2);
      install via `apt install file` / `brew install file-formula`
      / `dnf install file`. `sqlite3` is mandatory for the
      schema-migration flow (below): the script reads state.db's
      ACTUAL `user_version` through the WAL with
      `sqlite3 -batch -init /dev/null -cmd ".timeout <busy_timeout_ms>" <db> "PRAGMA user_version;"`,
      both to decide whether a migration sentinel is needed and to
      verify the post-open version. Raw header bytes are subtly wrong
      for a WAL-mode DB, `-init /dev/null` keeps your `~/.sqliterc` from
      changing the output, and `<busy_timeout_ms>` is the `[store]
      busy_timeout_ms` that step 4 reads. Install `sqlite3` via
      `apt install sqlite3` / `brew install sqlite` /
      `dnf install sqlite`. `curl` is also required when
      `--from-release` is supplied.
   4. **Store database (`[store] db_path` and `busy_timeout_ms`)** —
      reads `[store] db_path` from `~/.agent-director/config.toml` to
      find the database agent-director opens, the one the
      schema-migration flow (below) reads, authorizes and verifies. When
      `db_path` moves the store, the `pre-flight OK` block gains a line
      `store   : <path> ([store] db_path in <config>)`; with the default
      store the output is unchanged. A config file whose `db_path` the
      script cannot read stops the install here (exit `5`,
      `ErrConfigMalformed`), before anything is installed or changed.
      See "Which database install.sh checks" below for the accepted form
      and the refusal. It then reads `[store] busy_timeout_ms`, how long
      agent-director waits for a locked store, so that its own reads of
      that database wait as long. A `busy_timeout_ms` line the script
      cannot read stops the install here too (exit `5`,
      `ErrConfigMalformed`), before anything is installed or changed;
      see "How long install.sh waits for a locked state.db" below. With
      hooks on (no `--no-hooks`), a config that sets `defaults` as a key
      before any header (`defaults = { ... }`) stops the install here
      too (exit `5`, `ErrConfigMalformed`), before anything is installed
      or changed, because step 6's config merge cannot extend it; see
      step 6 below. Also with hooks on, a symlinked
      `~/.claude/settings.json` or `config.toml` that step 6's merges
      cannot write through stops the install here, before anything is
      installed or changed: `settings.json` with exit `4`, `config.toml`
      with exit `5` (`ErrConfigMalformed`); see step 6 below.
   5. **`--from-release` resolution** (if applicable) — downloads
      both matching assets for `$(uname -s)`/`$(uname -m)` from GitHub
      Releases (`agent-director-<os>-<arch>` and
      `agent-director-admin-<os>-<arch>`); `--sha256` verifies the first
      and `--admin-sha256` the second (both or neither, as above), and a
      mismatch aborts with exit 3, installing nothing. A release before
      0.11.0 has no `agent-director-admin` asset and is refused at once
      (exit 3), with no CDN retry: install 0.11.0 or later.
   6. **`--binary` / `--admin-binary` path/executability resolution** —
      settles `BINARY_SRC` and `ADMIN_SRC`, one source per binary, as
      "Where install.sh looks for each binary" below describes, and
      verifies each is an executable regular file. Pre-flight prints
      the two it settled as `  source  : <path>` and
      `  admin source: <path>`. These lines and every pre-flight
      message name the in-repo build as `<checkout>/bin/<binary>`,
      where `<checkout>` is the checkout's real path (symlinks
      resolved), with no `../..`. A source not found is
      refused with exit 3, naming every path tried and the flags to
      pass:
      - agent-director not found: "install.sh: no source binary
        found.", `Tried:` the checkout's `bin/agent-director` and
        `command -v agent-director`, and "Pass --binary <path> to
        override."
      - agent-director-admin not found, with agent-director given as
        `--binary` or found in the checkout: "install.sh: no
        agent-director-admin source binary found.", `Tried:` the
        checkout's `bin/agent-director-admin` and
        `~/.agent-director/admin/agent-director-admin`, and "Pass
        --admin-binary <path> to override."
      - neither found: one combined refusal, "install.sh: no source
        binaries found: ...", that names all four places it looked and
        ends "Pass --binary <path> --admin-binary <path> (both from the
        same build) to override." An `agent-director` found only on
        PATH counts as not found here, since nothing is there to pair
        with it; the refusal says "Found on PATH, not used: <path> (no
        agent-director-admin to pair with it)".

      **Where install.sh looks for each binary.** With
      `--from-release`, both come from the downloaded release assets.
      Otherwise each comes from the first of these it finds:

      | Binary | 1st | 2nd | 3rd |
      |---|---|---|---|
      | `agent-director` | `--binary <path>` | `bin/agent-director` of the checkout the script sits in | `command -v agent-director` |
      | `agent-director-admin` | `--admin-binary <path>` | `bin/agent-director-admin` of that checkout | the installed `~/.agent-director/admin/agent-director-admin` |

      `agent-director-admin` is never looked up on PATH, where it is
      never installed. So a re-run of the installed skill, outside any
      checkout, with no flags, reinstalls the `agent-director` on PATH
      with the installed `agent-director-admin`, and `--binary <path>`
      alone pairs that binary with the installed one. Like any pair,
      step 9 refuses them (exit 3) when their stamps differ.
   7. **Architecture probe (SR-2.2)**, for `--binary` and
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
   8. **Source-tree version check** — when the binary came from a
      local source (`--binary` or the in-repo build) AND install.sh
      lives inside a git checkout, the binary's embedded commit
      must match `HEAD`. Catches the "operator forgot to
      `make build` after pulling new code" footgun.
   9. **Version-stamp pairing** — `agent-director version` and
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
   way, from the source step 6 of the pre-flights settled. It is never
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
   for a one-step rollback of the pair. The decision covers the pair:
   a re-install of the same pair (agent-director byte-identical to its
   new binary, and agent-director-admin byte-identical too or not
   installed) snapshots neither and keeps the `.prior` files, and any
   other install snapshots both. On an upgrade from a release before
   0.11.0, which installed no admin binary, a stale admin `.prior` is
   removed instead.

4. **Rejects whitespace in the install path.** Per SRD §4.3 tmux's
   direct-argv invocation does not tolerate spaces in the binary
   path; the script aborts up front rather than failing mysteriously
   later.

5. **Migrates and opens the database — the six-step schema flow
   (SR-1.7).** An older-than-binary `state.db` opens ONLY when an
   administrator has authorized exactly that schema transition; the
   install runs on the end-user's machine as an admin action, so it
   is the legitimate authorizer. See the **"Schema migration: the
   six-step sentinel flow"** section below for the full detail.
   `state.db` here is the database agent-director opens:
   `~/.agent-director/state.db`, or wherever `[store] db_path` puts it
   (step 4 of the pre-flights); the install's messages call it
   `state.db` for the default store and name it by its path otherwise.
   In brief, in order:

   1. **Install the new binary** — already done by step 3/the atomic
      `mv` above.
   2. **Read the DB's ACTUAL `user_version`** via
      `sqlite3 -batch -init /dev/null -cmd ".timeout <busy_timeout_ms>" <state.db> "PRAGMA user_version;"`
      (through the WAL, waiting up to `[store] busy_timeout_ms` for a
      lock, ignoring `~/.sqliterc` — never assume the version, and never
      read raw header bytes). No `state.db` on disk (fresh install) →
      nothing to authorize; step 4 fresh-creates it. A `state.db` that
      exists but whose version cannot be read, or reads as anything but
      a whole number (0 or more), stops the install here (**exit 5**): no
      sentinel is written and the store is not opened. See "An
      unreadable schema version" below.
   3. **Write the authorization sentinel** — a file
      `migrate-authorized` beside state.db
      (`~/.agent-director/migrate-authorized` for the default store)
      containing `{"from": <actual>, "to": <target>}`, where
      `<target>` is the schema version this binary requires. **Skipped
      when `from == target`** (the DB is already current), on a
      fresh install (no DB), and when the probe below has already run
      the migration. The install decides with one probe
      `agent-director list` against the existing store, by the error
      name (`err_name`) it fails with, never by its message text:
      - it opens, or fails with `ErrSchemaMismatch`: nothing to
        authorize (`schema  : state.db at vN; no migration
        authorization needed`); an `ErrSchemaMismatch` fails step 4's
        open again, with the store advice.
      - it opens, and a `migrate-authorized` was already beside
        state.db before it (left by an earlier run whose store open
        failed, or written by hand), with state.db above v0: when that
        sentinel matches, the probe's open runs its migration and
        consumes it, so the install reads `user_version` again. If the
        version rose, it prints `schema  : migration vN→vT ran at the
        probe (agent-director list), authorized by a sentinel written
        before this install (sentinel <path>)`, and step 5 verifies vT.
        If not, it prints the line above (`no migration authorization
        needed`). If that read gives no version, the install stops here
        (**exit 5**); see "An unreadable schema version" below.
      - `ErrSchemaMigrationRequired`: its message names `<target>`,
        and the sentinel authorizes that migration.
      - `ErrConfigMalformed`: the config file was refused, so the probe
        never reached state.db. The install stops here (**exit 5**)
        with no sentinel written; see "A refused config file" below.
      - any other error: no sentinel is written; the install prints
        `schema  : state.db at vN; could not tell whether a migration
        is needed (agent-director list failed: <err_name>)` (`<no
        err_name>` when the probe's output holds no error envelope)
        and goes on to step 4, which reports the failure if it
        persists.

      The sentinel is written to a temp file that `mktemp` creates
      beside it (`migrate-authorized.tmp.XXXXXX`), then moved into
      place. If `mktemp` cannot create that file, the install stops here
      (**exit 5**, `install.sh: writing the migration sentinel FAILED`)
      with no migration authorized; see "No temp file for the sentinel"
      below.
   4. **Trigger exactly one store-opening open** — runs
      `agent-director list` once. `list` opens the store, so the
      authorized migration runs and the sentinel is consumed on
      success; on a fresh install this creates `state.db` (mode 0600)
      at the current schema version. **NOT `help`/`version`** — those
      verbs become DB-free (SR-4), so a help-based warmup would never
      open the store and step 5 would fail on every upgrade. If it
      fails with `ErrConfigMalformed`, the install stops (**exit 5**)
      with the config advice, not the store advice, and a fresh install
      creates no `state.db`; see "A refused config file" below.
   5. **Verify and fail loudly** — re-reads `user_version` and, when
      step 3's probe reported a pending migration or ran one, confirms
      it equals the target. On a mismatch (or if state.db wasn't
      created) the install **aborts non-zero (exit 5)** with a clear
      message; for a readable mismatch see "A version mismatch after
      the store open" below. An unreadable version also exits 5 when a migration was
      expected; with none expected (a fresh install or an
      already-current store) it is a warning and the install carries
      on. See "An unreadable schema version" below.
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
   skipped. An entry counts as already there only where Claude Code
   runs it: in the event's list, with the hook in the entry's own
   `hooks` list. An entry whose `hooks` is an object does not count, so
   the install adds a proper entry beside it (b.zbg). The pre-edit
   contents of `settings.json` are snapshotted to a timestamped `.bak`
   sibling before the merge writes.

   An empty `settings.json`, or one holding only whitespace (a file
   made with `touch`, say), is merged as `{}`: it is backed up and gets
   both hooks (b.zbg).

   A `settings.json` the merge cannot use stops the install with
   **exit 4** and is left as it was, with no `.bak`:
   - not valid JSON: `install.sh: ~/.claude/settings.json is not valid JSON`;
   - more than one JSON document (`{} {}`, say), each valid JSON on
     its own (b.zbg). `N` is how many:

         install.sh: cannot merge the hooks into ~/.claude/settings.json: it holds N JSON documents
           Each is valid JSON, but the file must hold one JSON object, the shape
           Claude Code reads. Fix it, then re-run this install.

     To fix it, rewrite the file as one JSON object holding the keys
     to keep from each document (ask the operator which);
   - valid JSON of another shape: not an object (an array, say), a
     `hooks` that is not an object, or a `SessionStart` or `SessionEnd`
     under it that is not a list. jq's error comes first, then:

         install.sh: cannot merge the hooks into ~/.claude/settings.json (jq's error is above)
           It is valid JSON, but not an object whose hooks hold event lists, the
           shape Claude Code reads. Fix it, then re-run this install.

   By then the binaries (and the PATH symlink, if any) are in place
   and state.db is at the binary's version; config.toml was not merged
   and MCP was not registered. Fix the file, then re-run the install
   with the same flags.

   The same step sets `inject_help_hook = true` in the `[defaults]`
   table of `~/.agent-director/config.toml`, so every Spawn also gets
   the help hook whatever its `CLAUDE_CONFIG_DIR`. An existing
   `inject_help_hook` line there is rewritten, a missing one is added
   to the table, and a `[defaults]` header holding the key is added at
   the end of the file when it has no `[defaults]` header; every other
   line is left as written, and the file is snapshotted to a timestamped
   `.bak` first. The
   `[defaults]` header is found however it is spaced: `[ defaults ]`,
   `[defaults] # comment`, an indented header, CRLF line ends and a
   UTF-8 byte-order mark are all fine (b.onv). The header and the key
   are also found in any letter case, as agent-director reads them, so
   the file never ends with the key set under two spellings, which
   agent-director refuses (b.hhk): the line that sets the key
   (`INJECT_HELP_HOOK`, say, under `[Defaults]`) becomes
   `inject_help_hook = true`; when no such line exists, the key is added
   at the end of the first such table, and a `[defaults]` header is
   added only when the file has none in any letter case. The summary
   line is
   `config  : merged inject_help_hook=true into <path> (backup <path>)`,
   or `config  : created <path> with inject_help_hook=true` when there
   was no config.toml (created at mode 0600).

   The merge edits no table set as a key. A config that sets `defaults`
   as a key before any header (an inline table such as
   `defaults = { relay_mode = "off" }`, in any letter case) already
   defines that table, and the header the merge would add would leave a
   file agent-director refuses. With hooks on, the install refuses such
   a file in pre-flight (exit 5, `ErrConfigMalformed`), before anything
   is installed or changed (b.whe):

       install.sh: cannot merge inject_help_hook = true into config.toml's [defaults] table; refusing to install.
         config  : /home/<you>/.agent-director/config.toml
         line 1  : defaults = { relay_mode = "off" }
         This sets defaults as a key (an inline table, say) rather than under a
         [defaults] header. With hooks on, install.sh sets inject_help_hook = true
         under a [defaults] header and edits no table set as a key: the header it
         would add can leave a file agent-director refuses. Remove this line, and
         set each key it sets under the file's [defaults] header instead, adding
         that header at the end of the file if the file has none.
         Add no header in this line's place: the lines below it, up to the next
         header, would fall under that header too.
         Nothing was installed or changed. Re-run this install after the change.
       install.sh: err_name=ErrConfigMalformed

   Make that change, then re-run the install with the same flags. With
   `--no-hooks` the file is not merged, so it is not refused.

   A merged `settings.json` or `config.toml` keeps the mode it had (a
   0600 file stays 0600 whatever your umask), and each `.bak` has the
   same mode as the file it copies (b.ojn).

   When either file is a symlink (into a dotfiles repo, say), the merge
   writes through it, following a chain of links too: the link is kept
   and the file at its end gets the edit (b.nw5). The `.bak` is a regular
   copy of that file, beside the link. A link to a file that does not
   exist yet, in a directory that does, creates that file.

   With hooks on, pre-flight refuses a link the merge cannot write
   through, before anything is installed or changed: its links loop
   (more than 40), the directory of the file it points to is missing (a
   dotfiles repository not cloned yet, say), or that directory cannot be
   written in (home-manager's links into the read-only `/nix/store`,
   say). A `settings.json` link exits **4**; a `config.toml` link exits
   **5** with `install.sh: err_name=ErrConfigMalformed` last. For
   example:

       install.sh: cannot merge into /home/<you>/.claude/settings.json through its symlink; refusing to install.
         link    : /home/<you>/.claude/settings.json
         target  : /nix/store/<hash>-settings.json
         The target's directory, /nix/store, cannot be written in (a read-only file system, say, such as home-manager's /nix/store).
         With hooks on, install.sh writes its merges into the file a symlinked
         settings.json or config.toml resolves to, keeping the link. Fix the link
         so it reaches a file in a directory you can write in. Or re-run this
         install with --no-hooks, which edits neither file, and add what the
         merges add where the files come from (your dotfiles or home-manager
         configuration, say): in settings.json, a SessionStart hook and a
         SessionEnd hook with matcher "compact", each a command hook running
         "/home/<you>/.agent-director/bin/agent-director help"; in config.toml,
         inject_help_hook = true under [defaults].
         Nothing was installed or changed. Re-run this install after the change.

   A loop has no `target` line. Show the message to the operator and ask
   which way out they want:
   - **Fix the link** so it reaches a file in a directory they can write
     in, then re-run the install with the same flags.
   - **Re-run with `--no-hooks` added**, then add the entries in the
     dotfiles or home-manager source. In `settings.json`, add to each
     event's list (beside any entries already there):

         {"hooks": {
           "SessionStart": [{"hooks": [{"type": "command", "command": "/home/<you>/.agent-director/bin/agent-director help"}]}],
           "SessionEnd": [{"matcher": "compact", "hooks": [{"type": "command", "command": "/home/<you>/.agent-director/bin/agent-director help"}]}]
         }}

     In `config.toml`, set `inject_help_hook = true` under `[defaults]`.
     Keep passing `--no-hooks` on later installs while the link points
     into a directory install.sh cannot write in: the check does not read
     the file, so a hooks-on install refuses the link even when it
     already holds these entries.

   With `--no-hooks`, this step is skipped entirely: settings.json is
   not read, not backed up, not written — left byte-identical to its
   pre-install state — and config.toml is not touched; neither file's
   symlink is checked in pre-flight. The post-install
   summary reports `hooks   : skipped (--no-hooks)`.

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

**Your umask (b.7j2).** install.sh runs `umask u=rwx` before anything
else, so your umask never takes away the owner's own permission bits
on what the install creates. That covers files the install's own
`agent-director` runs create in `~/.agent-director/`, since they
inherit the umask. The group and other bits stay as your umask sets
them, so an ordinary umask (022, 077, 027, 002) is unchanged. The
modes above are set with explicit `chmod`s and do not depend on the
umask.

## What this skill does NOT do

- It does NOT modify any per-Spawn hooks. Those are injected inline
  via `--settings` at spawn time and are NOT persistent in the
  user's `settings.json`.
- It does NOT touch existing user hooks in any event.
- It does NOT install `claude` or `tmux` themselves. Those are
  pre-flight requirements.

## When install.sh fails

install.sh exits 0 on success. Its own failures exit 2 to 5:

| Exit | Cause |
|---|---|
| 2 | Pre-flight: a bad flag or flag pair, whitespace in `$HOME`, an unsupported OS/CPU, a missing tool, or a binary built for another architecture. |
| 3 | The binaries: one not found or not executable, a `--from-release` that found no release or could not download one, a hash mismatch, a release before 0.11.0, a local binary not built from `HEAD`, or two version stamps that differ or carry no commit. |
| 4 | The `~/.claude/settings.json` hook merge (step 6 of "What this skill does"): the file is not valid JSON, holds more than one JSON document, or is valid JSON of another shape. Or, with hooks on, a symlinked `settings.json` the merge cannot write through (refused in pre-flight, before anything was installed or changed; see step 6). |
| 5 | The config file, or the store open and schema migration. The cause line below names which. |
| any other non-zero | A command install.sh does not check failed (for example `mkdir` could not create `~/.agent-director`), and the script stopped there with that command's status, usually 1. The command's own error is on stderr above. |

Each message on stderr says what went wrong and what to do; show it to
the operator. When this skill ran the install, re-run it only on the
operator's explicit "yes", as for the first run.

### Exit 5's cause line

Exit 5 has causes needing different remedies, so every exit 5 ends with
one line on stderr, its last, naming the cause:

    install.sh: err_name=<Name>

Branch on `<Name>`, never on the text above it: that text is advice for
a human and its wording can change. Take the last line of stderr; no
other line has that form. The exit status stays 5 for every cause, and
no other exit status has a cause line.

| `<Name>` | Cause | Remedy |
|---|---|---|
| `ErrVersionUnreadable` | A read of state.db's `user_version` gave no version: step 2's read, step 3's read after the probe, or step 5's read when a migration was expected. | Re-run the install with the same flags. A read that failed (a lock held past `[store] busy_timeout_ms`, say) can succeed on a re-run. A read that printed something other than a whole number prints it again until the sqlite3 on PATH or state.db changes, so cap the re-runs, then show the operator the report. See "An unreadable schema version". |
| `ErrConfigMalformed` | `~/.agent-director/config.toml` was refused: by install.sh's pre-flight readers of `[store] db_path` and `[store] busy_timeout_ms`, or, with hooks on, by its checks that the `[defaults]` merge can extend the file and can write through a symlinked config.toml (all before anything was installed or changed); or by agent-director at step 3's probe or step 4's store open. | Fix what the message names in config.toml (or its symlink), then re-run the install with the same flags; a symlink also has a `--no-hooks` way out. See "Which database install.sh checks", "How long install.sh waits for a locked state.db", step 6 of "What this skill does", and "A refused config file". |
| `ErrSchemaMismatch` | Step 4's store open: state.db is newer than this binary. | Install a newer agent-director (`--from-release`, or a newer `--binary`). The same name also covers a state.db with no valid store id, which a newer binary does not fix; only the error envelope above the cause line says which (open as b.o9t). See "ErrSchemaMismatch recovery". |
| `ErrSchemaVerifyFailed` | One of install.sh's own checks failed: after a migration, step 5 read a whole-number `user_version` that is not the target; or state.db is missing after a store open that succeeded (`state.db was not created by the store open`); or step 3's `mktemp` could not create the sentinel's temp file. | Needs a human. Stop, show the operator the report, and follow its advice with them. See "A version mismatch after the store open" and "No temp file for the sentinel". |
| any other name | Step 4's store open failed with that agent-director `err_name` (`ErrSchemaMigrationRequired`, say). It is `ErrStoreOpen` when the open's output held no error envelope, or an envelope whose `err_name` is not a plain `Err…` name. | The message advises a re-run: a migration this install authorized was not consumed, and a re-run retries it. If the re-run fails with the same name, it needs a human: show the operator the error above the cause line. |

`ErrVersionUnreadable` and `ErrSchemaVerifyFailed` are install.sh's own
names: no agent-director verb returns them. Whatever the name, do NOT
delete state.db.

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
     `uninstall.sh` does not read `[store] db_path`: a store that
     `db_path` puts outside `~/.agent-director/` survives `--purge`,
     and the operator deletes it by hand if they want it gone.

2. **Skip the script's `[y/N]` safety prompt? (`--force`)** *(only ask if (b) above was chosen)*

   - *What this is:* with `--purge`, `uninstall.sh` prints
     `--purge will rm -rf ~/.agent-director/ — proceed? [y/N]`
     before it removes or changes anything, and reads the reply from
     its stdin. `--force` suppresses that prompt. Your tool call gives
     the script no stdin, so the prompt gets no reply: the script then
     exits 1, with nothing removed or changed.
   - *Options:*
     - **(a) Keep the prompt (default).** One more chance to back
       out, at the script's own prompt. The reply has to reach the
       script's stdin: show the operator the prompt line above, ask
       them `y` or `n`, and run the command with their reply on stdin,
       `bash uninstall.sh --purge <other flags> <<< y` (or `<<< n`;
       the reply needs its newline, which `<<<` adds).
       Or the operator runs the command in their own terminal and
       answers the prompt there.
     - **(b) Skip it (`--force`).** The `AskUserQuestion` you just
       answered counts as confirmation; the extra prompt is
       redundant. You run the command as assembled.
   - *Default:* (a). Belt-and-suspenders by default; the operator
     can opt into (b) explicitly.
   - *Reversibility:* once `--force` plus `--purge` runs, the
     directory is gone with no further chance to abort. A reply
     other than `y`, `Y`, `yes` or `YES` (`n`, say) cancels only the
     purge: the plain uninstall still runs, and the script ends
     `uninstall.sh: --purge aborted` with exit 0. The `--force` flag
     itself does nothing without `--purge`.

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
   - With `--purge` but no `--force`, run it as question 2's (a)
     says (the operator's reply on stdin, or the operator runs it),
     never as a bare tool call: that stops at the prompt with exit 1,
     having done nothing. If that happens, ask the operator for the
     reply, or whether to add `--force`, and run it again.

### What uninstall.sh does

- Removes the two help hook entries (only the entries this skill
  added; other user hooks are preserved), snapshotting
  `settings.json` to a timestamped `.bak` first. A `settings.json`
  that is empty or holds only whitespace has no entries to remove and
  is left alone, with no `.bak` and no note. One that is not valid
  JSON, or holds more than one JSON document, is left alone with a
  note on stderr, and the uninstall goes on (b.zbg):
  `uninstall.sh: ~/.claude/settings.json is not valid JSON; leaving it alone`,
  or
  `uninstall.sh: ~/.claude/settings.json holds N JSON documents, not one; leaving it alone`.
  Tell the operator: the file may still hold agent-director's hook
  entries, to remove by hand.
- Removes `inject_help_hook` from `config.toml`'s `[defaults]` table
  (the header and the key found however they are spaced and in any
  letter case, as install finds them), and the `[defaults]` header too
  when only blank lines and comments are left under it. When that
  changes the file, it is snapshotted to a timestamped `.bak` first.
- Both rewrites keep the file's mode, and each `.bak` has the same
  mode as the file it copies (b.ojn). A symlinked file is written
  through: the link is kept and the file it points to gets the edit
  (b.nw5). A link that points nowhere, or whose links loop, is left
  alone.
- It works out both edits before it changes anything. A symlinked file
  it must edit but cannot write through, because the directory of the
  file it points to cannot be written in (home-manager's read-only
  `/nix/store`, say), stops the uninstall with **exit 2** before
  anything is removed or changed (except a `config.toml` under a
  confirmed `--purge`; see `--purge` below). With `--purge` and no
  `--force`, a `settings.json` refusal comes before the purge prompt,
  and a `config.toml` refusal after a reply that declines it. Stderr
  names the link, its target and
  why, then gives two ways out: fix the link so it reaches a file in a
  directory you can write in, or remove agent-director's entries where
  the file comes from (your dotfiles or home-manager configuration):
  - `settings.json`: each `SessionStart` and `SessionEnd` entry holding
    a hook whose command starts with
    `~/.agent-director/bin/agent-director` (the path spelled out);
  - `config.toml`: `inject_help_hook` from `[defaults]`, and the
    `[defaults]` header too when only blank lines and comments are left
    under it.

  It ends
  `Nothing was removed or changed. Re-run this uninstall after the change.`
  Show it to the operator, and re-run the uninstall with the same flags
  after the change.
- Such a `settings.json` that holds none of agent-director's hook
  entries is left alone, and the uninstall goes on, printing
  `uninstall.sh: left <path> alone: it holds no agent-director hook entries, and its symlink cannot be written through`.
  Such a `config.toml` without `inject_help_hook` needs no edit and is
  not checked.
- Removes the binary at `~/.agent-director/bin/agent-director` and
  the `.prior` snapshot if one is present.
- Removes `~/.agent-director/admin/agent-director-admin`, its `.prior`
  snapshot if one is present, and the `~/.agent-director/admin/`
  directory (left in place, with a note, if it holds other files).
- Unlinks the PATH symlink if one was created.
- With `--purge`: also removes `~/.agent-director/` entirely
  (including state.db + templates). A store `[store] db_path` puts
  outside `~/.agent-director/` is not removed. Unless `--force` is
  supplied, it first prints
  `--purge will rm -rf ~/.agent-director/ — proceed? [y/N]` and reads
  one reply line from stdin, before anything is removed or changed
  (but after the `settings.json` symlink check above). Then:
  - `y`, `Y`, `yes` or `YES` (or `--force`): the purge runs. A
    symlinked `config.toml` it cannot write through is not refused:
    the purge removes the link, never the file it points to, so that
    file is left alone, still holding `inject_help_hook`, and the
    script prints
    `uninstall.sh: left <path> alone: its symlink cannot be written through, and --purge removes the link; its target, <target>, keeps inject_help_hook`.
    Tell the operator, so they can remove the key where the file
    comes from if they want it gone. A symlinked `config.toml` it can
    write through is edited through first (the file it points to, in
    a dotfiles repo say, loses `inject_help_hook`), then the link is
    removed; that edit's `.bak`, beside the link, goes with the purge.
  - Any other reply: no purge. The rest runs as a plain uninstall
    (so a symlinked `config.toml` it cannot write through is refused
    with exit 2, as above) and ends `uninstall.sh: --purge aborted`,
    exit 0.
  - No reply (stdin at its end, as for a bare tool call): exit 1 after
    the prompt, with nothing removed or changed. See question 2 of
    the operator dialog for how to pass the reply, or use `--force`.
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

- **Path:** always a *sibling of state.db*, the database
  agent-director opens: `~/.agent-director/migrate-authorized` for the
  default store, or `migrate-authorized` in the directory of the file
  `[store] db_path` names (see "Which database install.sh checks"
  below), so the install authorizes the right DB.
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

### Which database install.sh checks

install.sh reads, authorizes and verifies the database agent-director
itself opens. That is `~/.agent-director/state.db` unless `[store]
db_path` in `~/.agent-director/config.toml` moves it. install.sh reads
`db_path` itself, in pre-flight, and resolves it as agent-director
does:

| `db_path` | Store database |
|---|---|
| unset, or `""` | `~/.agent-director/state.db` |
| `"~/x"` | `x` under `$HOME` |
| `"/abs/x"` | `/abs/x`, as written |
| anything else (`"x"`, `"./x"`, `"../x"`, `"~"`, `"~user/x"`) | relative to `~/.agent-director/` |

`$VAR` is not expanded: `"$HOME/x"` is a relative path. Step 2 below
reads that database's `user_version`, step 3 writes the sentinel beside
it, and step 5 verifies it. When it is not the default, the pre-flight
block names it:

    install.sh: pre-flight OK
      claude  : ...
      tmux    : ...
      store   : /home/<you>/custom/state.db ([store] db_path in /home/<you>/.agent-director/config.toml)

and later messages name it by that path where they would say
`state.db`. With the default store there is no `store` line and the
output is unchanged. A `~/.agent-director/state.db` left behind after
`db_path` moved the store is neither read nor changed. No
agent-director command prints the store path; the `store` line is where
the install shows it.

**The form install.sh reads.** The reader is narrow and fails closed.
It accepts only blank lines, `#` comment lines, `[name]` table headers
and `name = value` lines, a name being letters, digits, `_` and `-`,
written bare. It reads no value but `db_path`'s and `busy_timeout_ms`'s
(see "How long install.sh waits for a locked state.db" below). `db_path`
must be under `[store]`, on one line, as a `"..."` holding no backslash or
a `'...'`, optionally followed by a `#` comment:

```toml
[store]
db_path = "~/custom/state.db"   # optional comment
```

Anything else stops the install with **exit 5** (`ErrConfigMalformed`)
before anything is installed or changed, even a line that would not
move the store: the
reader never guesses. For example: a dotted key
(`store.db_path = "..."`), a quoted key or table name
(`"db_path" = ...`, `["store"]`), `[[name]]`, a table within a table (`[store.x]`),
an inline table (`store = { db_path = "..." }`), a value spanning lines
(a `"""` or `'''` string, a multi-line array), `[Store]` or `DB_PATH`
(agent-director matches names regardless of letter case), a second
`[store]` or `db_path`, a `db_path` value holding an escape, a control
character (a tab, say) or a `?`, a UTF-16 byte-order mark, or a
`config.toml` that is not a readable file. The report names the file,
the line (none for a file it cannot read) and what to change:

    install.sh: cannot tell which store database agent-director opens; refusing to install.
      config  : /home/<you>/.agent-director/config.toml
      line 1  : store.db_path = "~/custom/state.db"
      This sets a store key as a dotted key (store.db_path = ..., say)
      rather than under a [store] header. Move this line, without the
      store. prefix, to under the file's [store] header, adding that header
      at the end of the file if the file has none.
      Add no header in this line's place: the lines below it, up to the next
      header, would fall under that header too.
      install.sh reads [store] db_path itself, to check, migrate and verify
      the database agent-director opens, and reads only this form of the
      file: ...
      Nothing was installed or changed. Re-run this install after the change.
    install.sh: err_name=ErrConfigMalformed

Make the change it names, then re-run the install with the same flags.
Followed as written, the change keeps the store where agent-director
puts it for the refused file. A setting written outside its table's
header (a dotted `store.db_path`, say) moves to under the file's header
for that table, which you add at the end of the file if it has none,
never in the line's place, where the header would take in the lines
below it too. A header agent-director ignores is removed with the lines under
it, never alone, so they cannot fall under another header (`[store]`,
say). The one change that moves the store, removing a control character
from `db_path`, says so. A file the reader accepts but agent-director
refuses (a bad value elsewhere in it, or a key of another table set under
two letter cases) passes pre-flight and stops at step 3 or 4 instead; see
"A refused config file" below. The reader accepts a `defaults` key set
before any header (`defaults = { ... }`); with hooks on, the check that
follows it refuses that line in pre-flight (exit 5); see step 6 of "What
this skill does" above.

### How long install.sh waits for a locked state.db

agent-director waits up to `[store] busy_timeout_ms` for a store database
another process holds locked, then fails the call. install.sh reads that
key itself, in pre-flight, from the same `~/.agent-director/config.toml`,
right after `db_path`. Each sqlite3 read of state.db's `user_version`
(`.timeout <busy_timeout_ms>`) waits up to that long for a lock, and the
`sqlite3` check commands the install prints carry the same value.

| `busy_timeout_ms` under `[store]` | install.sh waits |
|---|---|
| unset, or `0` | 10000 ms, agent-director's default |
| `1` to `2147483647` | that many milliseconds |
| negative, or above `2147483647` | the default; agent-director refuses the file at step 3 or 4 (see "A refused config file" below) |

install.sh reads the value only as a whole number in decimal digits
(optionally signed, with `_` only between two digits), optionally followed
by a `#` comment:

```toml
[store]
busy_timeout_ms = 30000   # optional comment
```

Anything else on that line stops the install with **exit 5**
(`ErrConfigMalformed`) before anything is installed or changed: a quoted value (`"30000"`), a leading
zero (`030`), a `0x`, `0o` or `0b` prefix, a decimal point (`1.5`), the key
in another letter case (`BUSY_TIMEOUT_MS`; agent-director matches names
regardless of letter case), or a second `busy_timeout_ms`. The report
names the file, the line and what to change:

    install.sh: cannot tell how long agent-director waits for a locked store database; refusing to install.
      config  : /home/<you>/.agent-director/config.toml
      line 2  : BUSY_TIMEOUT_MS = 30000
      agent-director reads this key as busy_timeout_ms: its TOML decoder
      matches names regardless of letter case. Write it as busy_timeout_ms.
      install.sh reads [store] busy_timeout_ms itself, to wait as long as
      ...
      Nothing was installed or changed. Re-run this install after the change.
    install.sh: err_name=ErrConfigMalformed

Make the change it names, then re-run the install with the same flags.

### The six steps `install.sh` performs

`state.db` below is the database the section above names.

1. **Install the new binaries** (atomic `mv` into
   `~/.agent-director/bin/`, and `agent-director-admin` into
   `~/.agent-director/admin/`).
2. **Read the ACTUAL `user_version`** via
   `sqlite3 -batch -init /dev/null -cmd ".timeout <busy_timeout_ms>" <state.db> "PRAGMA user_version;"`
   (through the WAL, waiting up to `[store] busy_timeout_ms` for a
   lock, ignoring `~/.sqliterc`). No `state.db` on disk → fresh
   install, skip to step 4. An existing `state.db` whose version cannot
   be read, or reads as anything but a whole number (0 or more), fails
   the install (exit 5) before any sentinel is written or the store is
   opened; see below.
3. **Write the sentinel** `{"from":<actual>,"to":<target>}` beside
   state.db — **skipped when `from == to`** (already current), on a
   fresh install, and when the probe below has already run the
   migration. `<target>` is the schema version the new binary
   requires; the install learns it from the binary's own migration
   refusal message. A probe `agent-director list` against the existing
   store decides, by its `err_name`: an open or `ErrSchemaMismatch`
   needs no authorization; but when a sentinel was already beside a
   state.db above v0 before an open, the probe's open may have run its
   migration, so step 3 reads the version again and, if it rose,
   reports `migration vN→vT ran at the probe (agent-director list), ...`
   as the migration step 5 verifies (a read that gives no version stops
   the install, exit 5); `ErrSchemaMigrationRequired` names
   `<target>` and gets the sentinel; `ErrConfigMalformed` stops the
   install (exit 5, no sentinel; see "A refused config file" below);
   any other error writes no sentinel, prints `could not tell whether a
   migration is needed (agent-director list failed: <err_name>)` and
   goes on to step 4. The sentinel goes through a temp file that
   `mktemp` creates beside it (`migrate-authorized.tmp.XXXXXX`) and is
   moved into place; if `mktemp` cannot create that file, the install
   stops (exit 5, no migration authorized; see "No temp file for the
   sentinel" below).
4. **Open the store once** with `agent-director list` (a store-opening
   verb — *not* `help`/`version`, which are DB-free per SR-4). The
   authorized migration runs and the sentinel is consumed; a fresh
   install creates state.db at the current version. `ErrConfigMalformed`
   here stops the install (exit 5) with the config advice; see "A
   refused config file" below.
5. When a migration was expected (step 3 authorized one, or its probe
   ran one), **verify** the post-open
   `user_version` equals the target, and **fail the install loudly**
   (exit 5) if it does not; see "A version mismatch after the store
   open" below. An unreadable version also fails (exit 5) when a
   migration was expected; with none expected it is a warning and the
   install carries on. See below.
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
delete state.db. If it reports `actual user_version: <unreadable>`,
see "An unreadable schema version" below. If it reports a readable
version that differs from the expected one, see "A version mismatch
after the store open" below.

An operator can also author the sentinel by hand (write the JSON
above, then run any store-opening verb once), but re-running the
install is the supported path and does the version reads and
verification for you.

### An unreadable schema version

The install reads state.db's `user_version` at step 2, before the
store open, and at step 5, after it. When a `migrate-authorized` was
already beside a state.db above v0 before step 3's probe and the probe
opened, it also reads it at step 3, after the probe. If any read fails,
or prints anything but a whole number (0 or more), the install reports
`actual user_version: <unreadable>` and exits 5, its last line
`install.sh: err_name=ErrVersionUnreadable`, except at step 5
with no migration expected, where it warns and carries on (see "Which
read it was" below). Do NOT delete state.db. The rest of an exit 5's
report depends on which happened. Either way, everything sqlite3
printed for the read (its errors and its output together) is indented
under that line.

- **The read failed** (sqlite3 exited nonzero, or printed nothing):
  what sqlite3 printed shows why (for example a lock held longer than
  `[store] busy_timeout_ms`). A version printed before the failure is
  not used. Re-running the install retries the read.
- **The read printed something else** (sqlite3 exited 0; "printed the
  output above, not a whole number (0 or more)"): the report names the
  `sqlite3 on PATH: <path>`. Anything sqlite3 printed counts, including
  a message on stderr beside the version. The read ignores
  `~/.sqliterc`, so the sqlite3 at that path printed it for this
  state.db: for example a wrapper that changes sqlite3's output, or a
  `user_version` below 0. A re-run gets the same output unless that
  sqlite3 or state.db changes, so a caller that re-runs on
  `ErrVersionUnreadable` caps its re-runs.

Which read it was:

- **Step 2** (`reading state.db's schema version FAILED`, naming
  `state.db`): the install could not tell whether state.db needs a
  migration, so it authorized none and did not open the store;
  state.db is as it was. The new binaries are already in place, so an
  older state.db is refused with `ErrSchemaMigrationRequired` until a
  re-run succeeds.
- **Step 3, after the probe** (the same headline, but the install
  could not "tell whether the probe (agent-director list) ran a
  migration", and "A sentinel written before this install was beside
  state.db, and it may have authorized one."): the probe's store open
  succeeded and may have migrated state.db, so step 3 printed no
  `schema  :` line and the install stopped before step 4. A re-run
  reads the version again. If the probe did migrate state.db, it
  consumed the sentinel, so the re-run finds state.db current (`no
  migration authorization needed`) and its step-5 read only reports
  the version: one that still gives no version is then the warning
  below, not a failure.
- **Step 5, a migration expected** (`schema migration verification
  FAILED`): the store open succeeded, and the install's `state.db:`
  status line shows `(schema <unreadable>)`.
- **Step 5, no migration expected** (a fresh install or an
  already-current store; `warning: state.db's schema version is
  unreadable after the store open`): not a failure. The store open
  succeeded, so the read only reports the version. The warning shows
  what sqlite3 printed for the read under the `<unreadable>`
  line, then `Check the version later with:` and the `sqlite3 ...
  "PRAGMA user_version;"` command for this state.db. The install
  carries on (hooks, the config.toml merge, MCP registration) and
  exits 0 unless a later step fails; its `state.db:` status line shows
  `(schema <unreadable>)`.

### No temp file for the sentinel

At step 3 the install has `mktemp` create the sentinel's temp file
beside state.db. If it cannot (a directory you cannot write, say, or a
full disk), mktemp's own error comes first, then the install exits 5
with:

    install.sh: writing the migration sentinel FAILED
      sentinel: /home/<you>/.agent-director/migrate-authorized
      mktemp could not create a temp file beside it (its error is above), so
      no migration was authorized: state.db is still at v<N>.
      The new agent-director does not open it until it is at v<T>.
      Fix what mktemp's error names (a directory you cannot write, say, or a
      full disk), then re-run this install: it authorizes the migration again.
    install.sh: err_name=ErrSchemaVerifyFailed

The cause line names `ErrSchemaVerifyFailed`, which needs a human: a
re-run alone fails the same way until someone fixes what mktemp's
error names. Nothing was done to state.db, and no sentinel or temp file is left
beside it. The new binaries are already in place (and the PATH symlink,
if any), but the hooks were not merged and MCP was not registered.

1. Fix what mktemp's error names.
2. Re-run the install with the same flags. It authorizes the migration
   again, migrates state.db and verifies it.

Until the re-run, an older state.db is refused with
`ErrSchemaMigrationRequired`.

### A version mismatch after the store open

When a migration was expected and step 5's read gives a whole number
that is not the target, the install exits 5 with `schema migration
verification FAILED`, the `expected user_version: <T>` and `actual
user_version: <A>` lines, advice, and last the cause line
`install.sh: err_name=ErrSchemaVerifyFailed`: a human decides what
follows. The store open (`agent-director
list`) succeeded, and a successful open leaves state.db at `v<T>`: any
authorized migration has run, and its sentinel is consumed. Yet the
read after the open gives `v<A>`, so state.db changed after the open,
or the read is wrong. Do NOT delete state.db.

1. Check its version now, with the command the report prints. It names
   your store's path and its `[store] busy_timeout_ms`; for the default
   store it is:

       sqlite3 -batch -init /dev/null -cmd ".timeout <busy_timeout_ms>" ~/.agent-director/state.db "PRAGMA user_version;"

2. Re-run the install with the same flags. It reads the version again:
   below `v<T>` it brings state.db to `v<T>` again, above `v<T>` it
   stops at the store open (`ErrSchemaMismatch`, exit 5; see
   "ErrSchemaMismatch recovery" below), and at `v<T>` it finishes the
   install.
3. If a re-run fails this same way, capture the error and contact the
   maintainers.

### A refused config file

agent-director loads `~/.agent-director/config.toml` before it opens
state.db, so a config it refuses (`ErrConfigMalformed`: for example a
refused `[tmux]` timing, `[defaults] expire_retention_days` or `[store]
busy_timeout_ms` value, a TOML syntax error in a value, such as
`relay_mode = off` unquoted, or a key set under names that differ only
in letter case, such as `relay_mode` under both `[Defaults]` and
`[defaults]`) fails every store-opening verb, the install's own
included. The install stops (exit 5) at its first store-opening verb:
step 3's probe when state.db exists, step 4's open on a fresh install.
(A line install.sh's own `db_path` reader cannot read, an unclosed
`[header]` say, stops the install earlier, in pre-flight; see "Which
database install.sh checks" above. So does a `busy_timeout_ms` line its
own reader cannot read; see "How long install.sh waits for a locked
state.db" above.) It reports:

    install.sh: agent-director refused its config file (ErrConfigMalformed)
      config  : /home/<you>/.agent-director/config.toml
      {"err_name":"ErrConfigMalformed","err_description":"config <path>: refused [defaults] values: ..."}
      Fix what the error above names in the config file, then re-run this
      install.
    install.sh: err_name=ErrConfigMalformed

Nothing was done to state.db: no sentinel was written, no migration
ran, and a fresh install created no state.db. The new binaries are
already in place (and the PATH symlink, if any), but the hooks were
not merged and MCP was not registered. Do NOT delete state.db: the
refusal says nothing about it.

1. Fix the value(s) the envelope names in the `config  :` file: set
   each to a value in its range, or remove it or set it to 0 to get its
   default. A removed or 0 key always loads; for `[tmux]
   pending_grace_seconds` it gives the default or the key's safe
   minimum, whichever is larger (the envelope's `below its safe minimum
   <m> s (computed from the effective create_timeout_ms ... and
   pipe_close_wait_ms ...)` names that minimum). Or fix the syntax
   error. For a key set under names that differ only in letter case,
   keep one of the names the envelope lists for it and remove the
   others.
2. Re-run the install with the same flags. It probes the store again
   and authorizes any pending migration.

Until the config is fixed, every store-backed verb fails with
`ErrConfigMalformed`; after the fix and before the re-run, an older
state.db is refused with `ErrSchemaMigrationRequired`.

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

Re-running the same install (for example after an exit 5) does not
snapshot again, because the installed pair is already the one being
installed: its `prior` and `admin prior` lines say `kept <path>` or
`none`, then `(not snapshotted: ...)`, and the `.prior` files are
still the binaries from before the upgrade. On a re-run of an upgrade
from a release before 0.11.0, the `admin prior: none` line still ends
`to roll back, remove <path>`: roll back as above. `none` on the
`prior` line means the earlier run kept no rollback copy of
`agent-director`: re-install the previous version as below.

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
store id. An install that meets it stops at step 4's store open
(exit 5) with the cause line `install.sh: err_name=ErrSchemaMismatch`
for either cause; the error envelope printed above that line says
which.

**state.db is NEWER than the binary** (the error says "found
user_version=N, want M" with N greater than M). Migrations only run
forward, and the install will not downgrade a DB.

1. Inspect: `sqlite3 -cmd ".timeout <busy_timeout_ms>" ~/.agent-director/state.db "PRAGMA user_version"`
   (with `[store] db_path` set, use the path install.sh's `store` line
   shows; for `<busy_timeout_ms>`, use your `[store] busy_timeout_ms`,
   or the default if unset, as "How long install.sh waits for a locked
   state.db" above gives it) and compare against the version the binary
   expects (shown in the error).
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
