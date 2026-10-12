#!/usr/bin/env bash
# install.sh — install or upgrade agent-director on this machine.
#
# Per SRD §16.2. The script is deliberately bash + jq + standard
# coreutils — no Go, no exotic deps. The Apiary skill harness invokes
# it; an operator can also run it directly from a checked-out tree.
#
# It installs two binaries from the same build: agent-director at
# ~/.config/agent-director/bin/agent-director, and the operator tool
# agent-director-admin at ~/.config/agent-director/admin/agent-director-admin
# (directory mode 0700), which is never put on PATH under any option
# (b.vqr). It refuses to install when the two binaries' version stamps
# (version and commit) differ, or carry no commit stamp (a plain
# `go build` reports commit "unknown"), since both open the same store.
#
# Without --from-release, each binary comes from the first of these it
# finds (b.azo):
#   agent-director        --binary; bin/agent-director of the checkout
#                         the script sits in; `command -v agent-director`.
#   agent-director-admin  --admin-binary; bin/agent-director-admin of
#                         that checkout; the installed
#                         ~/.config/agent-director/admin/agent-director-admin.
#                         Never PATH.
# So a re-run from the installed skill, outside any checkout and with
# no flags, reinstalls the agent-director on PATH with the installed
# agent-director-admin; like any other pair, they are refused (exit 3)
# when their version stamps differ. When a source is not found, the
# install is refused (exit 3), naming every path tried and the flags to
# pass.
#
# Flags:
#   --binary <path>      Source binary to install. Defaults to
#                        bin/agent-director of the checkout the script
#                        sits in, then to whatever `command -v
#                        agent-director` resolves to.
#   --admin-binary <path>
#                        Source agent-director-admin binary to install.
#                        Defaults to bin/agent-director-admin of the
#                        checkout the script sits in (`make build`
#                        builds both binaries), then to the installed
#                        ~/.config/agent-director/admin/agent-director-admin.
#                        Mutually exclusive with --from-release.
#   --from-release [tag] Download pre-built agent-director and
#                        agent-director-admin binaries for this host's
#                        OS/arch from GitHub Releases and install them.
#                        With no tag, resolves the latest release via
#                        `gh release view` (if available) or
#                        `curl + jq` against api.github.com. Mutually
#                        exclusive with --binary and --admin-binary.
#                        Releases before 0.11.0 have no
#                        agent-director-admin binary and are refused.
#   --sha256 <hex>       Verify the downloaded agent-director asset
#                        against this sha256 (lowercase hex, 64 chars).
#                        Only meaningful with --from-release, and only
#                        together with --admin-sha256: pass both or
#                        neither (neither skips verification).
#   --admin-sha256 <hex> Verify the downloaded agent-director-admin
#                        asset against this sha256, as --sha256 does for
#                        agent-director. Only meaningful with
#                        --from-release, and only together with --sha256.
#   --symlink-dir <dir>  Drop a PATH symlink at <dir>/agent-director.
#                        Default: ~/.local/bin if on PATH; otherwise
#                        no symlink.
#   --no-symlink         Suppress symlink creation regardless of dir.
#   --register-mcp       Run `claude mcp add agent-director -- <path> serve
#                        --stdio` for the stdio server (exit 6 if it fails). Also
#                        re-points a registration that names the old
#                        directory's binary (see "The move" below). Claude
#                        Code only: Codex gets no registration. With Claude
#                        Code not installed (see "Agents" below) it is skipped
#                        with a line saying so, and the install still succeeds.
#   --no-hooks           Skip the hook injection steps entirely: the
#                        ~/.claude/settings.json help hooks and the
#                        inject_help_hook = true merge into config.toml.
#                        settings.json and config.toml are left
#                        byte-identical (no .bak backup, no edit). Default
#                        OFF — defaulting to skip would defeat install.sh's
#                        main value over a bare binary copy.
#   --keep-prior         Before overwriting an existing binary,
#                        snapshot it to <target>.prior (overwriting
#                        any previous .prior). Roll back with
#                        `mv <target>.prior <target>`. Default OFF.
#                        Both binaries are snapshotted, agent-director
#                        and agent-director-admin, so rolling back both
#                        restores a matching pair; when no
#                        agent-director-admin was installed before (an
#                        upgrade from a release before 0.11.0), roll it
#                        back by removing it. A re-install of the same
#                        pair (agent-director already byte-identical to
#                        the one being installed, and
#                        agent-director-admin either byte-identical too
#                        or not installed) is not snapshotted, so the
#                        .prior files from the earlier run are kept. On an
#                        install that moves AD's directory (see "The
#                        move" below) nothing is snapshotted: the new bin
#                        and admin directories hold no earlier binary, and
#                        the install says so.
#
# Exit codes:
#   0  success
#   1  a write of the agent-rename migration failed after the binaries were
#      installed (a full disk, or permissions changed since pre-flight): see
#      "The agent-rename migration" below. The file is as it was, every copy
#      is kept, and the store is not opened.
#   2  pre-flight failure (tmux, jq, file or sqlite3 missing, neither
#      Claude Code nor Codex found (see "Agents" below), whitespace in path,
#      a bad flag, only one of --sha256 and --admin-sha256, or HOME unset,
#      empty or not an absolute path: checked first, before anything is read
#      or written; run it with HOME set to the user's home directory)
#   3  binary source not found / not executable (including a
#      --from-release release before 0.11.0, which has no
#      agent-director-admin binary), or the two binaries' version
#      stamps differ or carry no commit stamp, or a binary's `version`
#      output lacks config_dir or names a directory other than
#      ~/.config/agent-director (a binary from an earlier release: install
#      that release with its own install.sh, fetched from its tag; this
#      install reads no store for the check)
#   4  hook merge failure (~/.claude/settings.json malformed, or holding
#      more than one JSON document; an empty one is merged as {}), or
#      (hooks on, Claude Code installed) a symlinked settings.json
#      install.sh cannot resolve or write through: its links loop, or the
#      file they resolve to sits in a directory that is missing or cannot
#      be written in (refused in pre-flight, before anything on disk
#      changes). With Claude Code not installed settings.json is not
#      touched or checked.
#   5  store open / schema-migration failure (open failed, the config
#      file refused with ErrConfigMalformed, state.db not created, an
#      existing state.db's user_version unreadable before the open (or
#      after the probe open, when a migration sentinel written before
#      this install may have let it migrate state.db), no temp file for
#      the migration sentinel (mktemp failed), or post-open
#      user_version unreadable or != target when a migration was
#      expected), or a config file whose [store] db_path or
#      busy_timeout_ms or [launch] claude_path or codex_path install.sh
#      cannot read, or, on an install that crosses the agent rename (see
#      "The agent-rename migration" below), a config.toml that sets the
#      question-tool key (disable_askuserquestion, disable_ask_user) more
#      than once under [defaults] or sets defaults as a key on a line that
#      names disable_askuserquestion, or a template the migration refuses
#      (ErrTemplateMalformed), or a config.toml or template it will change
#      that is a symlink it cannot write through, or (hooks on) one that
#      sets defaults as a key before any header (defaults = { ... }),
#      which the config.toml merge cannot extend, or (hooks on) a
#      symlinked config.toml install.sh cannot resolve or write through,
#      as for settings.json under exit 4 (all refused in pre-flight,
#      before anything on disk changes). state.db here is
#      the store database agent-director opens: ~/.config/agent-director/state.db,
#      or wherever [store] db_path in ~/.config/agent-director/config.toml puts it.
#      Every exit 5 ends with one line on stderr, its last, naming the
#      cause: `install.sh: err_name=<Name>`. Branch on <Name>, never on the
#      text above it: ErrVersionUnreadable (re-run), ErrConfigMalformed
#      (fix config.toml, then re-run), ErrSchemaMismatch (install a newer
#      agent-director), ErrSchemaVerifyFailed (needs a human),
#      ErrMoveConflict (the directory layout cannot be moved as it is: the
#      message names the one cause and its remedy; change nothing else,
#      apply the remedy, re-run), ErrAgentsLive (an agent or an
#      agent-director process runs against the old ~/.agent-director, or
#      its live rows could not be read: stop whatever starts agents, then
#      every agent, every serve and every caller, and re-run; a re-run
#      reads the rows again), ErrMoveFailed (moving ~/.agent-director to
#      ~/.config/agent-director failed before the rename, a full disk
#      being the usual cause: the partial new directory was removed and
#      the old directory is untouched; fix the cause and re-run), or, when
#      the store open fails another way, agent-director's own err_name.
#      ErrTemplateMalformed is agent-director's own catalogued name
#      (errnames.Catalog); fix the template the message names, then re-run.
#      ErrMoveConflict, ErrAgentsLive and ErrMoveFailed come from the
#      move of AD's directory (SR-11.7), which this release makes once on
#      an upgrade from a release that kept its files in ~/.agent-director,
#      before any binary is replaced; ErrMoveConflict and ErrAgentsLive are
#      raised in pre-flight, changing nothing. They are install.sh's own
#      names, as ErrVersionUnreadable is, not agent-director's.
#   6  the MCP registration failed (--register-mcp): `claude mcp add` (or the
#      `claude mcp remove` before the re-add of a registration that names the
#      old directory's binary) exited non-zero. Every other step is done: the
#      binaries, the store, the hooks and the links are installed and stay.
#      stderr gets the command run, its exit status, its stderr and the
#      command to run by hand, e.g. `claude mcp add agent-director -- <path>
#      serve --stdio`; the install's last line says so. The check of an earlier
#      registration (`mcp get`) never fails the install.
#
# Agents (SR-11.1). agent-director launches Claude Code and Codex agents,
# and an install needs at least one of them. Each agent is present when its
# launch path ([launch] claude_path or codex_path in config.toml, read in
# pre-flight) names an existing executable file, or its program name (claude,
# codex) is on PATH; install.sh only looks, and writes no [launch] key.
# With neither present the install stops, exit 2, naming both agents, their
# [launch] keys and how to install either; nothing is changed. The Claude
# Code program is claude_path when it names an executable, else `claude` from
# PATH; every Claude Code command this script runs (the summary's --version,
# `mcp get`, `mcp add`, `mcp remove`) goes through it. Codex is never run: the
# summary prints its program's path, and says it did not run it, because
# `codex --version` writes files in the Codex folder; install.sh registers
# nothing with Codex and writes nothing in any Codex folder. With Claude Code
# not installed the settings.json hook step (the help hooks, and the removal
# of the old directory's, which --no-hooks would print instead) and
# --register-mcp are skipped with a line saying "skipped because Claude Code
# is not installed" (even under --no-hooks), no settings.json symlink
# check is made, and `mcp get` is not run; the inject_help_hook = true merge
# into config.toml still runs unless --no-hooks. The summary names each
# agent's program and version line, "not installed", or that a launch path
# set names no executable file.
#
# The agent-rename migration (SR-11.2). This release renames config.toml's
# [defaults] key disable_askuserquestion to disable_ask_user and a template's
# claude_args to agent_args, and gives each template an agent. On an install
# that crosses the rename (a state.db exists, at the path pre-flight resolves,
# and its user_version is below AGENT_RENAME_HOP_VERSION, the version of the
# v8 to v9 store hop), pre-flight checks config.toml and every template
# (exit 5, ErrConfigMalformed or ErrTemplateMalformed, nothing changed), and
# after the move and the binary swap, before the store is opened, each file
# that changes is copied to <file>.pre-agent-rename.<YYYYmmdd-HHMMSS> (mode
# kept) and replaced whole, byte for byte, with its mode kept and a symlink
# written through: the key is renamed in place, and a template with no agent
# gains agent = "claude" before its first [table] header (or last). A write
# that fails stops the install with exit 1, the file as it was, every copy
# kept: fix the cause and run this release's install.sh again (it completes
# the migration, and changes nothing already migrated), or take the way back
# (docs/operator-actions.md). With no store, or a store at or above that
# version, no file is read or changed. An unreadable store version stops the
# install in pre-flight (exit 5, ErrVersionUnreadable). It runs with or
# without --no-hooks.
#
# The move (SR-11.7). An earlier release kept AD's files in
# ~/.agent-director; this one keeps them in ~/.config/agent-director. When
# ~/.agent-director is present and ~/.config/agent-director is absent (or
# holds the marker .move-incomplete a failed move left), this install moves
# the directory first: it refuses while an agent or an agent-director
# process runs (exit 5, ErrAgentsLive) or on a layout it cannot move safely
# (exit 5, ErrMoveConflict), copies everything but bin/ and admin/ into the
# new directory (the store with SQLite's backup, every copy checked, the
# path keys in config.toml rewritten) and renames the old directory to
# ~/.agent-director.pre-move-<YYYYmmdd-HHMMSS>, to delete once the upgrade is
# confirmed. A failure before the rename removes the partial copy and ends
# the install with exit 5 (ErrMoveFailed), the old directory untouched. The
# install also re-points the PATH link it manages, removes help hooks in
# settings.json that name the old directory's binary (with --no-hooks it
# prints them), prints the other links and $PATH entries into the old bin
# directory with their fix, and checks Claude Code's registration of AD's MCP
# server (`claude mcp get agent-director`, from the directory the install
# runs in). Run the upgrade from a shell AD did not start.
#
# Idempotent: re-running it after a clean install, with the same
# binaries (a no-flag re-run outside a checkout picks the
# agent-director on PATH and the installed agent-director-admin, as
# above) and the same --no-hooks, symlink and --register-mcp choices,
# ends in the same state and returns 0 on success. Its steps run
# again: the binaries are copied again (with --keep-prior, not
# snapshotted: any earlier .prior files are kept), config.toml and the
# templates are migrated only while the store is below the rename's version
# (a re-run that crosses the rename again changes nothing already migrated
# and makes no new copy of it), the store is opened
# (a state.db already at its target version is not migrated) and, with
# hooks on, settings.json and config.toml are merged again (no hook is
# added twice), each after a fresh timestamped .bak. With
# --register-mcp, `claude mcp add` refuses a name already registered in
# its scope, so a re-run from a directory where `mcp get agent-director`
# already shows this install's binary with `serve --stdio` adds nothing, says
# so, and still returns 0.

set -euo pipefail

# HOME first, before anything is read or written: every path below is built
# from it, and an empty or relative HOME would put the install root somewhere
# else (an empty one gives /.config/agent-director). Exit 2, the pre-flight
# class.
case "${HOME-}" in
    /*) ;;
    *)
        echo "install.sh: HOME is unset, empty or not an absolute path (HOME='${HOME-}'). Run install.sh with HOME set to the user's home directory, for example HOME=/home/alice." >&2
        exit 2
        ;;
esac

# A umask that removes the owner's own bits (0777, 0222, 0200, ...) also
# strips them from every file and directory this script creates without a
# chmod. The step-3 migration sentinel's mktemp file comes out unwritable,
# so an upgrade cannot authorize its migration. The --from-release
# downloads fail too (curl cannot write its mktemp file), and so does
# settings.json when ~/.claude is missing (the new directory is not
# writable; exit 1). settings.json in an existing ~/.claude and a merged
# config.toml are written, but come out missing the same owner bits (mode
# 000, unreadable by their owner, under 0777). So allow the owner rwx. The
# group and other bits stay
# as the operator set them, and a umask that leaves the owner's bits alone
# is unchanged. The agent-director runs below inherit it, so the files they
# create in ~/.config/agent-director are usable by their owner too (b.7j2).
umask u=rwx

# --------------------------------------------------------------------
# Defaults + flag parsing
# --------------------------------------------------------------------

readonly DEFAULT_INSTALL_ROOT="${HOME}/.config/agent-director"
readonly DEFAULT_BIN_DIR="${DEFAULT_INSTALL_ROOT}/bin"
# The operator tool's own directory, never on PATH (b.vqr).
readonly DEFAULT_ADMIN_DIR="${DEFAULT_INSTALL_ROOT}/admin"
readonly DEFAULT_ADMIN_PATH="${DEFAULT_ADMIN_DIR}/agent-director-admin"
readonly DEFAULT_SETTINGS_PATH="${HOME}/.claude/settings.json"
# The directory an earlier release kept its files in (SR-11.7). Only the move
# and the messages around it name it.
readonly OLD_INSTALL_ROOT="${HOME}/.agent-director"
readonly OLD_BIN_DIR="${OLD_INSTALL_ROOT}/bin"
# The marker the move writes into the new directory first and removes last.
readonly MOVE_MARKER="${DEFAULT_INSTALL_ROOT}/.move-incomplete"

BINARY_SRC=""
ADMIN_SRC=""
FROM_RELEASE=0
FROM_RELEASE_TAG=""
SHA256_EXPECTED=""
ADMIN_SHA256_EXPECTED=""
SYMLINK_DIR=""
SYMLINK_DEFAULT=""
NO_SYMLINK=0
REGISTER_MCP=0
NO_HOOKS=0
KEEP_PRIOR=0

# GitHub repo slug used by --from-release. Matches go.mod's module path
# and the /release skill's asset naming.
readonly RELEASE_REPO_SLUG="gabemahoney/agent-director"

# Pick a sensible default symlink dir: ~/.local/bin if on PATH.
if printf '%s' ":${PATH}:" | grep -q ":${HOME}/.local/bin:"; then
    SYMLINK_DEFAULT="${HOME}/.local/bin"
fi

while [[ $# -gt 0 ]]; do
    case "$1" in
        --binary)
            BINARY_SRC="$2"; shift 2 ;;
        --admin-binary)
            ADMIN_SRC="$2"; shift 2 ;;
        --from-release)
            FROM_RELEASE=1
            # Optional tag argument: accept only if the next arg
            # doesn't look like another flag.
            if [[ $# -ge 2 && -n "${2:-}" && "${2:-}" != -* ]]; then
                FROM_RELEASE_TAG="$2"; shift 2
            else
                shift
            fi
            ;;
        --sha256)
            SHA256_EXPECTED="$2"; shift 2 ;;
        --admin-sha256)
            ADMIN_SHA256_EXPECTED="$2"; shift 2 ;;
        --symlink-dir)
            SYMLINK_DIR="$2"; shift 2 ;;
        --no-symlink)
            NO_SYMLINK=1; shift ;;
        --register-mcp)
            REGISTER_MCP=1; shift ;;
        --no-hooks)
            NO_HOOKS=1; shift ;;
        --keep-prior)
            KEEP_PRIOR=1; shift ;;
        -h|--help)
            sed -n '2,/^$/p' "$0" | sed 's/^# \{0,1\}//'
            exit 0 ;;
        *)
            echo "install.sh: unknown flag: $1" >&2
            exit 2 ;;
    esac
done

[[ -z "$SYMLINK_DIR" && "$NO_SYMLINK" -eq 0 ]] && SYMLINK_DIR="$SYMLINK_DEFAULT"

if [[ "$FROM_RELEASE" -eq 1 && -n "$BINARY_SRC" ]]; then
    echo "install.sh: --from-release and --binary are mutually exclusive" >&2
    exit 2
fi
if [[ -n "$SHA256_EXPECTED" && "$FROM_RELEASE" -eq 0 ]]; then
    echo "install.sh: --sha256 only applies with --from-release" >&2
    exit 2
fi
if [[ -n "$SHA256_EXPECTED" && ! "$SHA256_EXPECTED" =~ ^[0-9a-f]{40,}$ ]]; then
    echo "install.sh: --sha256 must be 64 lowercase hex characters" >&2
    exit 2
fi
if [[ "$FROM_RELEASE" -eq 1 && -n "$ADMIN_SRC" ]]; then
    echo "install.sh: --from-release and --admin-binary are mutually exclusive" >&2
    exit 2
fi
if [[ -n "$ADMIN_SHA256_EXPECTED" && "$FROM_RELEASE" -eq 0 ]]; then
    echo "install.sh: --admin-sha256 only applies with --from-release" >&2
    exit 2
fi
if [[ -n "$ADMIN_SHA256_EXPECTED" && ! "$ADMIN_SHA256_EXPECTED" =~ ^[0-9a-f]{40,}$ ]]; then
    echo "install.sh: --admin-sha256 must be 64 lowercase hex characters" >&2
    exit 2
fi
# --sha256 and --admin-sha256 go together (b.vqr): asking to verify one
# asset must not install the other unverified.
if [[ -n "$SHA256_EXPECTED" && -z "$ADMIN_SHA256_EXPECTED" ]]; then
    echo "install.sh: --sha256 without --admin-sha256 would install agent-director-admin unverified; refusing to install." >&2
    echo "  Pass --admin-sha256 <hex> too (the sha256 of the agent-director-admin release asset), or neither flag to skip verification." >&2
    exit 2
fi
if [[ -n "$ADMIN_SHA256_EXPECTED" && -z "$SHA256_EXPECTED" ]]; then
    echo "install.sh: --admin-sha256 without --sha256 would install agent-director unverified; refusing to install." >&2
    echo "  Pass --sha256 <hex> too (the sha256 of the agent-director release asset), or neither flag to skip verification." >&2
    exit 2
fi

# --------------------------------------------------------------------
# Exit 5's machine-readable cause (b.cfq)
#
# Exit 5 has causes needing different remedies, so every exit-5 path ends
# with one fixed line on stderr, its last, `install.sh: err_name=<Name>`,
# for a caller to branch on instead of the English above it:
#   ErrVersionUnreadable  a user_version read gave no version
#                         (ad_fail_unreadable_version): re-run.
#   ErrConfigMalformed    config.toml refused, by agent-director or by
#                         install.sh's pre-flight readers, merge check,
#                         symlink check or agent-rename migration checks
#                         (the question-tool key set twice, or on a line
#                         that sets defaults as a key): fix it, then re-run.
#   ErrTemplateMalformed  a stored template refused by the agent-rename
#                         migration's pre-flight checks (a top-level line
#                         outside the form, claude_args twice, claude_args
#                         with agent_args, or a symlink it cannot write
#                         through): fix the template the message names,
#                         then re-run. agent-director's own catalogued name
#                         (errnames.Catalog, config.ErrTemplateMalformed).
#   ErrSchemaMismatch     state.db newer than this binary (step 4's open):
#                         install a newer agent-director.
#   ErrSchemaVerifyFailed step 5 found state.db missing, or not at the
#                         target version, after a successful open; or step 3
#                         could not create the sentinel's temp file: needs
#                         a human.
#   any other name        step 4's open failed with that agent-director
#                         err_name (ErrStoreOpen when its stderr held no
#                         error envelope).
#   ErrMoveConflict       AD's old and new directories cannot be moved as
#                         they are (both present, a link where the move
#                         copies, a path key it cannot place, ...): the
#                         message names the cause and the remedy.
#   ErrAgentsLive         the old store holds a live row, a process runs
#                         the old binary, or the rows could not be read:
#                         stop them, then re-run.
#   ErrMoveFailed         the move failed before its rename: the partial
#                         new directory was removed, the old one is
#                         untouched; fix the cause (a full disk), re-run.
# ErrVersionUnreadable, ErrSchemaVerifyFailed, ErrMoveConflict,
# ErrAgentsLive and ErrMoveFailed are install.sh's own names, not
# agent-director error names.
#
# Exit 1 is the agent-rename migration's failed write (see the header): it
# ends with no err_name line.
# --------------------------------------------------------------------

# ad_exit_5 <err_name> — print exit 5's cause line naming <err_name> on
# stderr, then exit 5.
ad_exit_5() {
    echo "install.sh: err_name=$1" >&2
    exit 5
}

# ad_exit_cleanup — the install's EXIT cleanup: remove every temp file this
# install made and has not moved into place (the staged binary copies, the
# --from-release downloads, the binary and admin temp copies, a migration
# sentinel's temp file that step 3 never moved into place, and a migration
# write's temp file). Every EXIT trap of this script runs it, the move's too
# (ad_move_on_exit), so the move's traps chain onto it rather than replace it.
ad_exit_cleanup() {
    local f
    for f in "${TMP:-}" "${ADMIN_TMP:-}" "${tmp_bin:-}" "${tmp_admin:-}" \
        "${tmp_sentinel:-}" "${stage_bin:-}" "${stage_admin:-}" "${migrate_tmp:-}"; do
        if [[ -n "$f" ]]; then
            rm -f "$f"
        fi
    done
}

# --------------------------------------------------------------------
# Pre-flight
# --------------------------------------------------------------------

# SRD §4.3: tmux's direct-argv invocation requires shell-safe paths.
# Reject any whitespace in the install root up front so an operator
# whose $HOME has a space sees the error immediately, not at the
# first spawn.
if [[ "$DEFAULT_INSTALL_ROOT" =~ [[:space:]] ]]; then
    echo "install.sh: install path contains whitespace: $DEFAULT_INSTALL_ROOT" >&2
    echo "  SRD §4.3 requires a whitespace-free install path." >&2
    exit 2
fi

# SRD §SR-2.1 / Idea Bee b.fg3: refuse with exit 2 any host outside the
# supported set {Linux/x86_64, Darwin/arm64}, before anything is
# downloaded or written. Linux/aarch64 is refused too; b.fg3 deferred
# it. install.sh is the only installer (the npm package installs nothing
# outside node_modules/), so this is the only host check that can refuse
# an install. ad_arch_probe (below) checks both source binaries against
# the pair read here.
uname_s="$(uname -s)"
uname_m="$(uname -m)"
case "${uname_s}/${uname_m}" in
    Linux/x86_64|Darwin/arm64)
        ;;
    *)
        echo "install.sh: unsupported host: ${uname_s}/${uname_m}. Supported: Linux/x86_64, Darwin/arm64. See b.fg3 for cross-platform expansion status." >&2
        exit 2
        ;;
esac

# tmux must be on PATH (Claude Code and Codex are looked for after the
# [launch] reader, below: at least one of them is needed). `file` is required for the --binary
# architecture probe (SR-2.2) — hard requirement; never silent-skip.
# `sqlite3` is required for the schema-migration flow: it reads the DB's
# ACTUAL user_version through the WAL (raw header bytes are subtly wrong
# for a WAL-mode DB) both to decide whether a migration sentinel is
# needed and to verify the post-open version. Hard requirement — never
# silent-skip; a bad read would either skip a needed migration or pass a
# broken install.
required_tools=(tmux jq file sqlite3)
[[ "$FROM_RELEASE" -eq 1 ]] && required_tools+=(curl)
for tool in "${required_tools[@]}"; do
    if ! command -v "$tool" >/dev/null 2>&1; then
        echo "install.sh: required tool not found on PATH: $tool" >&2
        case "$tool" in
            tmux)    echo "  Install tmux via your package manager (apt/brew/dnf/etc.)." >&2 ;;
            jq)      echo "  Install jq via your package manager (we use it to safely edit settings.json)." >&2 ;;
            file)    echo "  Install file via your package manager (apt install file / brew install file-formula / dnf install file). Required for the --binary architecture probe." >&2 ;;
            sqlite3) echo "  Install sqlite3 via your package manager (apt install sqlite3 / brew install sqlite / dnf install sqlite). Required to read state.db's schema version for the migration flow." >&2 ;;
            curl)    echo "  --from-release downloads via curl; install it via your package manager." >&2 ;;
        esac
        exit 2
    fi
done

# The one Claude Code program (SR-11.1): every Claude Code command this
# script runs (the summary's version line, `mcp get`, `mcp add` and `mcp
# remove`) goes through "$CLAUDE_PROG". It is `claude` (found on PATH) until
# the agent check below, after the [launch] reader, sets it to claude_path
# when that names an executable file. CLAUDE_PRESENT is 0 when Claude Code is
# found neither by claude_path nor on PATH; no Claude Code command runs then.
CLAUDE_PROG=claude
CLAUDE_PRESENT=0
CODEX_PRESENT=0

# --------------------------------------------------------------------
# Store database: [store] db_path (b.2io)
#
# agent-director opens the store at [store] db_path in
# ~/.config/agent-director/config.toml when that is set (pkg/api
# resolveStorePath), and looks for the migration sentinel beside it
# (internal/store sentinelPath). The schema-migration steps below must
# read, authorize and verify that same database, so install.sh reads
# db_path itself; no agent-director verb prints the path. The reader is
# narrow and fails closed: on anything but the common form it stops the
# install (exit 5) here, before anything on disk changes.
#
# It reads config.toml line by line and accepts only:
#   - blank lines and # comment lines;
#   - table headers naming one bare key, [name] (spaces inside the
#     brackets and a trailing # comment allowed);
#   - name = value lines whose name is one bare key. Values are not
#     read, except db_path's under [store]: a single-line "..." holding
#     no backslash, or '...', optionally followed by a # comment.
# A bare key is letters, digits, _ and -. Everything else is refused,
# naming the line: a dotted or quoted key or table name, [[name]], a
# top-level store key (store = { ... }), a line continuing a value
# from an earlier line (a multi-line array or inline table; any """ or
# ''' is refused as a multi-line string), [store] or db_path in other
# letter case (agent-director's TOML decoder matches names regardless of
# case), a second [store] or db_path, a db_path value holding a control
# character, and one holding a '?' (the store's SQLite driver reads
# everything from the first '?' on as options, so agent-director would
# open a different file). Some refused lines would not move the store;
# refusing them is the price of never guessing. Each refusal says what to
# change, in words that, followed as written, keep the store where
# agent-director puts it for the refused file: a dotted store.db_path key
# becomes db_path under [store], never a top-level db_path agent-director
# ignores; a line before any header that belongs under one moves to its
# table's header, or to one added at the end of the file, never to one added
# in its place, which would take in the lines after it too; and lines
# agent-director ignores are removed with their header, never left to fall
# under another one. Where that cannot be done (a control character in
# db_path), the refusal says so. A line that is not TOML at
# all (an unclosed [header], say) is refused here too, before
# agent-director sees it; a file the reader accepts but agent-director
# refuses (a bad value) still stops at step 3 or 4 (ErrConfigMalformed).
#
# The value resolves exactly as Go resolves it (internal/config
# resolvePathField, pkg/api resolveStorePath): empty or unset gives
# state.db in the base directory, a leading ~/ is joined onto $HOME, an
# absolute path is used unchanged, and anything else (~, ~user/x, ./x,
# ../x) is joined onto the base directory. The caller names the base
# directory: the directory the config file is read in, which is where Go
# joins relative paths once the file is in place. Every join is cleaned as
# Go's filepath.Join cleans it (ad_clean_path); an absolute path is not.
# $VAR stays literal.
#
# When asked, the reader also reads [log] error_log_path the same way, in
# the same line form and with the same refusals, except that a '?' is
# accepted there (it is no SQLite path); an unset or empty value gives no
# path.
#
# When asked ("launch"), the reader also reads [launch] claude_path and
# codex_path (SR-5.0, SR-11.1), in the same line form and with the same
# refusals: one lower-case [launch] header, each key once and in lower case,
# a one-line "..." holding no backslash, or '...', optionally followed by a
# # comment, and no control character; a '?' is accepted (it is no SQLite
# path). A refused launch line gets its own headline ("cannot tell which
# programs agent-director launches") and says what to change for [launch].
# A value resolves as Go's config load resolves it, which is not as a path
# field resolves: empty is unset (printed as an empty line), an absolute path
# is used unchanged, "~/..." is joined onto $HOME and cleaned, and anything
# else (a blank value, a relative path, ~ alone, ~user/...) is refused, as
# Go refuses it with ErrConfigMalformed: a launch path is never joined onto
# the base directory. Keys under [launch] other than these two are ignored.
#
# The block between the >>> and <<< marker lines is self-contained (bash
# 3.2 or later, builtins only), so a test can extract it with sed and
# call ad_store_db_path alone.
# --------------------------------------------------------------------

# >>> ad_store_db_path (b.2io) >>>

# ad_clean_path <path> — print <path> cleaned as Go's filepath.Clean cleans
# it on Unix: repeated slashes and . elements go, each .. removes the
# element before it (and is dropped at the root), a trailing slash goes,
# and an empty result is ".".
ad_clean_path() {
    local rest="$1" out="" seg root=""
    if [[ "$rest" == /* ]]; then
        root=/
    fi
    while [[ -n "$rest" ]]; do
        seg="${rest%%/*}"
        if [[ "$seg" == "$rest" ]]; then rest=""; else rest="${rest#*/}"; fi
        case "$seg" in
            ""|.) ;;
            ..)
                if [[ -n "$out" && "$out" != .. && "$out" != */.. ]]; then
                    if [[ "$out" == */* ]]; then out="${out%/*}"; else out=""; fi
                elif [[ -z "$root" ]]; then
                    out="${out:+$out/}.."
                fi
                ;;
            *) out="${out:+$out/}$seg" ;;
        esac
    done
    out="${root}${out}"
    printf '%s\n' "${out:-.}"
}

# ad_sentinel_path <db> — print where the store looks for the migration
# sentinel that authorizes <db>: Go's filepath.Join(filepath.Dir(<db>),
# "migrate-authorized") (internal/store sentinelPath). Unlike dirname, both
# clean the directory, which matters for an absolute db_path that is not
# clean.
ad_sentinel_path() {
    if [[ "$1" == */* ]]; then
        ad_clean_path "${1%/*}/migrate-authorized"
    else
        printf '%s\n' migrate-authorized
    fi
}

# ad_toml_name <part> — print the name agent-director's TOML decoder matches
# for one key or table-name part, bare, "..." or '...' (re_part below): the
# part without its quotes, with each U+017F (long s) and U+212A (Kelvin sign)
# written as the s and k they match, since the decoder compares names with
# Go's strings.EqualFold. Called only inside ad_store_db_path's subshell.
ad_toml_name() {
    local name="$1" long_s=$'\xc5\xbf' kelvin=$'\xe2\x84\xaa'
    case "$name" in
        \"*\"|\'*\') name="${name:1:$((${#name} - 2))}" ;;
    esac
    name="${name//$long_s/s}"
    name="${name//$kelvin/k}"
    printf '%s\n' "$name"
}

# ad_db_path_refuse <config> <line number> <line> <reason>... — report a
# config file the reader cannot read, with the offending line (none when
# <line number> is 0) and <reason>s saying what to change, then exit 1.
# Called only inside ad_store_db_path's subshell, which sets want_log and
# about_launch (1 while the refusal is about a [launch] header, key or value,
# whose headline and closing note name [launch], not the store).
ad_db_path_refuse() {
    local config="$1" n="$2" line="$3" reason
    shift 3
    if [[ "${about_launch:-0}" -eq 1 ]]; then
        echo "install.sh: cannot tell which programs agent-director launches (the [launch] table); refusing to install." >&2
    elif [[ "${want_log:-0}" -eq 1 ]]; then
        echo "install.sh: cannot tell which store database or error log agent-director opens; refusing to install." >&2
    else
        echo "install.sh: cannot tell which store database agent-director opens; refusing to install." >&2
    fi
    echo "  config  : $config" >&2
    if [[ "$n" -gt 0 ]]; then
        printf '  line %s  : %s\n' "$n" "$line" >&2
    fi
    for reason in "$@"; do
        echo "  $reason" >&2
    done
    if [[ "$n" -gt 0 ]]; then
        if [[ "${about_launch:-0}" -eq 1 ]]; then
            echo "  install.sh reads [launch] claude_path and codex_path itself, to choose the" >&2
            echo "  Claude Code program it runs and to see which agents are installed, and" >&2
            echo "  reads only this form of the file: blank lines, # comments, [name]" >&2
            echo "  headers and name = value lines, a name being letters, digits, _ and -," >&2
            echo "  with claude_path and codex_path under [launch] set to a one-line \"...\"" >&2
            echo "  holding no backslash, or '...', and an optional # comment after it. A" >&2
            echo "  value is an absolute path or starts with ~/; an empty \"\" counts as unset." >&2
        elif [[ "${want_log:-0}" -eq 1 ]]; then
            echo "  install.sh reads [store] db_path and [log] error_log_path itself, to" >&2
            echo "  check, migrate and verify the database agent-director opens and to move" >&2
            echo "  the paths with it, and reads only this form of the" >&2
            echo "  file: blank lines, # comments, [name] headers and name = value lines," >&2
            echo "  a name being letters, digits, _ and -, with db_path under [store] and" >&2
            echo "  error_log_path under [log] set to a one-line \"...\" holding no" >&2
            echo "  backslash, or '...', and an optional # comment after it." >&2
        else
            echo "  install.sh reads [store] db_path itself, to check, migrate and verify" >&2
            echo "  the database agent-director opens, and reads only this form of the" >&2
            echo "  file: blank lines, # comments, [name] headers and name = value lines," >&2
            echo "  a name being letters, digits, _ and -, with db_path under [store] set" >&2
            echo "  to a one-line \"...\" holding no backslash, or '...', and an optional" >&2
            echo "  # comment after it." >&2
        fi
    fi
    echo "  Nothing was installed or changed. Re-run this install after the change." >&2
    exit 1
}

# ad_store_db_path <config> <home> <base> [log] [launch] — print the store
# database agent-director opens for config file <config>, home directory
# <home> (non-empty) and base directory <base> (the directory the config
# file's relative paths are joined onto; the caller says which directory that
# is, so the same reader serves a config file before and after it moves),
# resolved as above; a missing <config> gives the default. After <base>, the
# words "log" and "launch" (either, both, in either order) ask for more
# lines, printed after the store's in this order:
#   log     [log] error_log_path, resolved the same way: empty when the file
#           does not set it or sets it empty (there is then nothing to move
#           or check);
#   launch  [launch] claude_path, then codex_path, resolved as above: each
#           empty when unset or empty.
# Every line is printed, an empty one too, so a caller reads them by
# position (command substitution drops trailing empty lines: read from a
# here-string with `|| true` after each read). A '?' is refused in db_path
# only. On a file the reader cannot read, print why on stderr and return 1,
# printing nothing on stdout. The body is a subshell under the C locale, so
# the matching is bytewise whatever the caller's locale.
ad_store_db_path() (
    LC_ALL=C
    local config="$1" home="$2" base="$3" want_log=0 want_launch=0 n=0 line table=""
    local about_launch=0 seen_store=0 seen_log=0 seen_launch=0 have_value=0 have_log_value=0
    local have_claude=0 have_codex=0 claude_value="" codex_value=""
    local value="" log_value="" key rest resolved name t k which parsed seen arg
    local -a tables=(store)
    shift 3
    for arg in "$@"; do
        case "$arg" in
            log) want_log=1; tables+=(log) ;;
            launch) want_launch=1; tables+=(launch) ;;
        esac
    done
    local re_skip='^[[:blank:]]*(#.*)?$'
    local re_header='^[[:blank:]]*\[[[:blank:]]*([A-Za-z0-9_-]+)[[:blank:]]*\][[:blank:]]*(#.*)?$'
    local re_bracket='^[[:blank:]]*\['
    local re_keyval='^[[:blank:]]*([A-Za-z0-9_-]+)[[:blank:]]*=[[:blank:]]*(.*)$'
    # A "..." value's body runs to the next unescaped ", so a backslash
    # before the first " after the opening one is an escape.
    local re_escape='^[[:blank:]]*"[^"]*\\'
    local re_basic='^[[:blank:]]*"([^"]*)"[[:blank:]]*(#.*)?$'
    local re_literal="^[[:blank:]]*'([^']*)'[[:blank:]]*(#.*)?\$"
    local re_store='^[Ss][Tt][Oo][Rr][Ee]$'
    local re_log='^[Ll][Oo][Gg]$'
    local re_db_path='^[Dd][Bb]_[Pp][Aa][Tt][Hh]$'
    local re_elp='^[Ee][Rr][Rr][Oo][Rr]_[Ll][Oo][Gg]_[Pp][Aa][Tt][Hh]$'
    local re_launch='^[Ll][Aa][Uu][Nn][Cc][Hh]$'
    local re_claude='^[Cc][Ll][Aa][Uu][Dd][Ee]_[Pp][Aa][Tt][Hh]$'
    local re_codex='^[Cc][Oo][Dd][Ee][Xx]_[Pp][Aa][Tt][Hh]$'
    local re_cntrl='[[:cntrl:]]'
    # Only to word a refusal: the line forms the reader refuses, as TOML
    # writes them. A key or table-name part is a bare name, a "..." holding no
    # backslash, or a '...' (first part in BASH_REMATCH[1]).
    local re_part='([A-Za-z0-9_-]+|"[^"\\]*"|'"'[^']*'"')'
    local re_dot='[[:blank:]]*\.[[:blank:]]*'
    local re_end='[[:blank:]]*(#.*)?$'
    local re_bare='^[A-Za-z0-9_-]+$'
    local re_aot='^[[:blank:]]*\[\[[[:blank:]]*'"$re_part"'('"$re_dot$re_part"')*[[:blank:]]*\]\]'"$re_end"
    local re_aot_one='^[[:blank:]]*\[\[[[:blank:]]*'"$re_part"'[[:blank:]]*\]\]'"$re_end"
    local re_header_one='^[[:blank:]]*\[[[:blank:]]*'"$re_part"'[[:blank:]]*\]'"$re_end"
    local re_header_dotted='^[[:blank:]]*\[[[:blank:]]*'"$re_part"'('"$re_dot$re_part"')+[[:blank:]]*\]'"$re_end"
    local re_key_one='^[[:blank:]]*'"$re_part"'[[:blank:]]*='
    local re_key_two='^[[:blank:]]*'"$re_part$re_dot$re_part"'[[:blank:]]*='
    local re_key_dotted='^[[:blank:]]*'"$re_part"'('"$re_dot$re_part"')+[[:blank:]]*='
    # Ends the advice for a line before any header that belongs under one. The
    # line goes under its table's header, or under one added at the end of the
    # file: a header added in its place would take in every line after it, up
    # to the next header, and agent-director would then read those lines
    # differently (ignore a store.db_path = ... line under [defaults], say).
    local -a not_here=(
        "Add no header in this line's place: the lines below it, up to the next"
        "header, would fall under that header too."
    )

    # is_table <name> <table>: is <name> the table's name in some letter
    # case; key_of <table>: the key the reader reads under it.
    # The launch table holds two keys, so is_key takes either of them there,
    # and key_of names the one the (dynamically scoped) name holds, claude_path
    # when it is neither; key_of also takes claude and codex, the two launch
    # keys by their program.
    is_table() {
        case "$2" in
            store) [[ "$1" =~ $re_store ]] ;;
            log) [[ "$1" =~ $re_log ]] ;;
            launch) [[ "$1" =~ $re_launch ]] ;;
        esac
    }
    is_key() {
        case "$2" in
            store) [[ "$1" =~ $re_db_path ]] ;;
            log) [[ "$1" =~ $re_elp ]] ;;
            launch) [[ "$1" =~ $re_claude || "$1" =~ $re_codex ]] ;;
        esac
    }
    key_of() {
        case "$1" in
            store) k=db_path ;;
            log) k=error_log_path ;;
            claude) k=claude_path ;;
            codex) k=codex_path ;;
            launch)
                if [[ "${name:-}" =~ $re_codex ]]; then k=codex_path; else k=claude_path; fi
                ;;
        esac
    }
    # set_about <table>: the refusal that follows is about [launch] when <table>
    # is launch (its headline and closing note then name [launch]).
    set_about() {
        if [[ "$1" == launch || "$1" == claude || "$1" == codex ]]; then
            about_launch=1
        else
            about_launch=0
        fi
    }
    # The advice for a table key before any header (store = { ... }), bare or
    # quoted.
    refuse_top() {
        local kdesc="$k" kset="set $k"
        set_about "$1"
        if [[ "$1" == launch ]]; then
            kdesc="claude_path or codex_path"
            kset="set it"
        fi
        local -a reasons=(
            "This sets $1 as a key (an inline table, say) rather than under a"
            "[$1] header. Remove this line. If it sets $kdesc, $kset under"
            "the file's [$1] header instead, adding that header at the end of the"
            "file if the file has none."
            "${not_here[@]}"
        )
        ad_db_path_refuse "$config" "$n" "$line" "${reasons[@]}"
    }

    if [[ -e "$config" ]]; then
        if [[ ! -f "$config" || ! -r "$config" ]]; then
            ad_db_path_refuse "$config" 0 "" \
                "It is not a readable file (a directory, say, or a file without read" \
                "permission), so agent-director cannot load it either. Make it one."
        fi
        while IFS= read -r line || [[ -n "$line" ]]; do
            n=$((n + 1))
            # The TOML decoder skips a UTF-8 byte-order mark and a CRLF's CR too.
            if [[ "$n" -eq 1 ]]; then
                line="${line#$'\xef\xbb\xbf'}"
            fi
            line="${line%$'\r'}"
            if [[ "$line" =~ $re_skip ]]; then
                continue
            fi
            # The decoder also skips a UTF-16 byte-order mark; the reader
            # does not.
            if [[ "$n" -eq 1 && ( "$line" == $'\xff\xfe'* || "$line" == $'\xfe\xff'* ) ]]; then
                ad_db_path_refuse "$config" "$n" "$line" \
                    "This line starts with the bytes FF FE or FE FF, a UTF-16 byte-order" \
                    "mark, which install.sh does not read. Remove those two bytes:" \
                    "agent-director skips them, so it reads the file the same without them."
            fi
            if [[ "$line" == *'"""'* || "$line" == *"'''"* ]]; then
                ad_db_path_refuse "$config" "$n" "$line" \
                    "This line holds \"\"\" or ''', which install.sh reads as the start of a" \
                    "multi-line string. Write each value on one line, as a \"...\" or '...'" \
                    "string, with no \"\"\" or ''' anywhere on the line."
            fi
            if [[ "$line" =~ $re_header ]]; then
                table="${BASH_REMATCH[1]}"
                for t in "${tables[@]}"; do
                    if is_table "$table" "$t"; then
                        set_about "$t"
                        if [[ "$table" != "$t" ]]; then
                            ad_db_path_refuse "$config" "$n" "$line" \
                                "agent-director reads this header as [$t]: its TOML decoder matches" \
                                "names regardless of letter case. Write it as [$t]."
                        fi
                        seen=$seen_store
                        if [[ "$t" == log ]]; then seen=$seen_log; fi
                        if [[ "$t" == launch ]]; then seen=$seen_launch; fi
                        if [[ "$seen" -eq 1 ]]; then
                            ad_db_path_refuse "$config" "$n" "$line" \
                                "This is a second [$t] header, and agent-director refuses a table" \
                                "defined twice. Move the lines under it, up to the next header, to" \
                                "under the first [$t] header, then remove this header."
                        fi
                        case "$t" in
                            log) seen_log=1 ;;
                            launch) seen_launch=1 ;;
                            *) seen_store=1 ;;
                        esac
                        about_launch=0
                    fi
                done
                continue
            fi
            # A refused header is never to be removed alone: the lines under it
            # would fall under the header before it, [store] perhaps.
            if [[ "$line" =~ $re_bracket ]]; then
                if [[ "$line" =~ $re_aot ]]; then
                    name="$(ad_toml_name "${BASH_REMATCH[1]}")"
                    if [[ "$line" =~ $re_aot_one ]]; then
                        for t in "${tables[@]}"; do
                            if is_table "$name" "$t"; then
                                set_about "$t"
                                ad_db_path_refuse "$config" "$n" "$line" \
                                    "This makes $t an array of tables ([[name]]), and agent-director" \
                                    "reads [$t] only as one table, so it refuses the file. Write it" \
                                    "as [$t]."
                            fi
                        done
                    fi
                    ad_db_path_refuse "$config" "$n" "$line" \
                        "This is an array of tables ([[name]]), which agent-director does not" \
                        "read: it ignores the lines under this header, or refuses the file." \
                        "Remove the header and the lines under it, up to the next header."
                fi
                if [[ "$line" =~ $re_header_one ]]; then
                    name="$(ad_toml_name "${BASH_REMATCH[1]}")"
                    for t in "${tables[@]}"; do
                        if is_table "$name" "$t"; then
                            set_about "$t"
                            ad_db_path_refuse "$config" "$n" "$line" \
                                "agent-director reads this header as [$t]: a quoted name is the" \
                                "same as the bare one, and its TOML decoder matches names regardless" \
                                "of letter case. Write it as [$t]."
                        fi
                    done
                    if [[ "$name" =~ $re_bare ]]; then
                        ad_db_path_refuse "$config" "$n" "$line" \
                            "A quoted table name is the same as the bare one. Write it as" \
                            "[$name], without the quotes."
                    fi
                    ad_db_path_refuse "$config" "$n" "$line" \
                        "agent-director reads no table with this name, so it ignores the lines" \
                        "under this header. Remove the header and the lines under it, up to" \
                        "the next header."
                fi
                if [[ "$line" =~ $re_header_dotted ]]; then
                    ad_db_path_refuse "$config" "$n" "$line" \
                        "This header names a table within a table ([a.b]), which" \
                        "agent-director does not read: it ignores the lines under this header," \
                        "or refuses the file. Remove the header and the lines under it, up to" \
                        "the next header."
                fi
                ad_db_path_refuse "$config" "$n" "$line" \
                    "This line starts with [ but is not a table header install.sh can read:" \
                    "a missing ], say, a quoted name holding an escape (\\), or part of an" \
                    "array value that spans lines. Correct the header to [name], with the" \
                    "name bare, or write the array on one line."
            fi
            if [[ ! "$line" =~ $re_keyval ]]; then
                if [[ "$line" =~ $re_key_one ]]; then
                    name="$(ad_toml_name "${BASH_REMATCH[1]}")"
                    for t in "${tables[@]}"; do
                        key_of "$t"
                        if [[ -z "$table" ]] && is_table "$name" "$t"; then
                            refuse_top "$t"
                        fi
                        if [[ "$table" == "$t" ]] && is_key "$name" "$t"; then
                            set_about "$t"
                            ad_db_path_refuse "$config" "$n" "$line" \
                                "agent-director reads this key as $k: a quoted key is the same" \
                                "as the bare one, and its TOML decoder matches names regardless of" \
                                "letter case. Write it as $k."
                        fi
                    done
                    if [[ "$name" =~ $re_bare ]]; then
                        ad_db_path_refuse "$config" "$n" "$line" \
                            "A quoted key is the same as the bare one. Write it as $name, without" \
                            "the quotes."
                    fi
                    ad_db_path_refuse "$config" "$n" "$line" \
                        "agent-director reads no key with this name, so it ignores this line." \
                        "Remove it."
                fi
                # Before any header, a.b = value sets b under [a]; under one, a
                # dotted key sets a key in a table within that table.
                if [[ -z "$table" && "$line" =~ $re_key_two ]]; then
                    name="$(ad_toml_name "${BASH_REMATCH[1]}")"
                    for t in "${tables[@]}"; do
                        key_of "$t"
                        if is_table "$name" "$t"; then
                            set_about "$t"
                            ad_db_path_refuse "$config" "$n" "$line" \
                                "This sets a $t key as a dotted key ($t.$k = ..., say)" \
                                "rather than under a [$t] header. Move this line, without the" \
                                "$t. prefix, to under the file's [$t] header, adding that header" \
                                "at the end of the file if the file has none." \
                                "${not_here[@]}"
                        fi
                    done
                    if [[ "$name" =~ $re_bare ]]; then
                        ad_db_path_refuse "$config" "$n" "$line" \
                            "Before any header, a dotted key a.b = value sets b under [a]. Move this" \
                            "line, without the $name. prefix, to under the file's [$name] header," \
                            "adding that header at the end of the file if the file has none." \
                            "${not_here[@]}"
                    fi
                    ad_db_path_refuse "$config" "$n" "$line" \
                        "agent-director reads no table with this key's first name, so it" \
                        "ignores this line. Remove it."
                fi
                if [[ "$line" =~ $re_key_dotted ]]; then
                    ad_db_path_refuse "$config" "$n" "$line" \
                        "This dotted key sets a key in a table within a table, which" \
                        "agent-director does not read: it ignores this line, or refuses the" \
                        "file. Remove it."
                fi
                ad_db_path_refuse "$config" "$n" "$line" \
                    "This line is not a blank line, a # comment, a [name] header or a" \
                    "name = value line: part of a value that spans lines (an array, say), a" \
                    "quoted name holding an escape (\\), or a typo. Write each value on one" \
                    "line, write the name bare, without quotes or escapes, or correct the" \
                    "typo."
            fi
            key="${BASH_REMATCH[1]}"
            rest="${BASH_REMATCH[2]}"
            for t in "${tables[@]}"; do
                key_of "$t"
                if [[ -z "$table" ]] && is_table "$key" "$t"; then
                    refuse_top "$t"
                fi
            done
            which=""
            if [[ "$table" == store ]] && is_key "$key" store; then
                which=store
            elif [[ "$want_log" -eq 1 && "$table" == log ]] && is_key "$key" log; then
                which=log
            elif [[ "$want_launch" -eq 1 && "$table" == launch ]]; then
                if [[ "$key" =~ $re_claude ]]; then
                    which=claude
                elif [[ "$key" =~ $re_codex ]]; then
                    which=codex
                fi
            fi
            if [[ -z "$which" ]]; then
                continue
            fi
            set_about "$which"
            key_of "$which"
            if [[ "$key" != "$k" ]]; then
                ad_db_path_refuse "$config" "$n" "$line" \
                    "agent-director reads this key as $k: its TOML decoder matches" \
                    "names regardless of letter case. Write it as $k."
            fi
            seen=$have_value
            case "$which" in
                log) seen=$have_log_value ;;
                claude) seen=$have_claude ;;
                codex) seen=$have_codex ;;
            esac
            if [[ "$seen" -eq 1 ]]; then
                ad_db_path_refuse "$config" "$n" "$line" \
                    "This sets $k a second time. Keep one."
            fi
            if [[ "$rest" =~ $re_escape ]]; then
                ad_db_path_refuse "$config" "$n" "$line" \
                    "$k's \"...\" value holds a backslash, which starts an escape." \
                    "Write the path without escapes; in single quotes ('...') a backslash" \
                    "or a double quote is literal."
            elif [[ "$rest" =~ $re_basic || "$rest" =~ $re_literal ]]; then
                parsed="${BASH_REMATCH[1]}"
            else
                ad_db_path_refuse "$config" "$n" "$line" \
                    "$k's value is not a one-line \"...\" or '...' string, optionally" \
                    "followed by a # comment. Write it in that form."
            fi
            if [[ "$parsed" =~ $re_cntrl ]]; then
                if [[ "$about_launch" -eq 1 ]]; then
                    ad_db_path_refuse "$config" "$n" "$line" \
                        "$k's value holds a control character, such as a tab. install.sh" \
                        "cannot install with one in the program's path, so remove it." \
                        "agent-director then launches the path without it."
                elif [[ "$which" == log ]]; then
                    ad_db_path_refuse "$config" "$n" "$line" \
                        "$k's value holds a control character, such as a tab. install.sh" \
                        "cannot install with one in the path, so remove it. agent-director" \
                        "then uses the path without it, not the file it already keeps at" \
                        "this one."
                else
                    ad_db_path_refuse "$config" "$n" "$line" \
                        "$k's value holds a control character, such as a tab. install.sh" \
                        "cannot install with one in the store path, so remove it. agent-director" \
                        "then opens the path without it, not any store it already keeps at this" \
                        "one."
                fi
            fi
            if [[ "$which" == store ]]; then
                if [[ "$parsed" == *\?* ]]; then
                    ad_db_path_refuse "$config" "$n" "$line" \
                        "db_path's value holds a '?'. agent-director's store open reads" \
                        "everything from the first '?' on as SQLite options, so it would not" \
                        "open this path. Use a path without '?'."
                fi
                value="$parsed"
                have_value=1
            elif [[ "$which" == log ]]; then
                log_value="$parsed"
                have_log_value=1
            else
                # Go's config load refuses any launch value but empty, an
                # absolute path and ~/...: a blank value, a relative path, ~
                # alone and ~user/... included.
                case "$parsed" in
                    ""|/*|"~/"*) ;;
                    *)
                        ad_db_path_refuse "$config" "$n" "$line" \
                            "$k's value is not an absolute path or a path starting with ~/, which" \
                            "agent-director refuses (a blank value, a relative path, ~ alone and" \
                            "~user/... are refused too). Write the program's absolute path, or one" \
                            "starting with ~/, or remove this line to launch ${which} by its name" \
                            "on the PATH; an empty \"\" counts as unset."
                        ;;
                esac
                if [[ "$which" == claude ]]; then
                    claude_value="$parsed"
                    have_claude=1
                else
                    codex_value="$parsed"
                    have_codex=1
                fi
            fi
            about_launch=0
        done <"$config"
    fi

    case "$value" in
        "")    resolved="$(ad_clean_path "${base}/state.db")" ;;
        "~/"*) resolved="$(ad_clean_path "${home}/${value:2}")" ;;
        /*)    resolved="$value" ;;
        *)     resolved="$(ad_clean_path "${base}/${value}")" ;;
    esac
    printf '%s\n' "$resolved"
    if [[ "$want_log" -eq 1 ]]; then
        case "$log_value" in
            "")    resolved="" ;;
            "~/"*) resolved="$(ad_clean_path "${home}/${log_value:2}")" ;;
            /*)    resolved="$log_value" ;;
            *)     resolved="$(ad_clean_path "${base}/${log_value}")" ;;
        esac
        printf '%s\n' "$resolved"
    fi
    if [[ "$want_launch" -eq 1 ]]; then
        # Only "", an absolute path or ~/... got here: an absolute path stays
        # as written and ~/... is joined onto the home directory.
        for value in "$claude_value" "$codex_value"; do
            case "$value" in
                "~/"*) resolved="$(ad_clean_path "${home}/${value:2}")" ;;
                *)     resolved="$value" ;;
            esac
            printf '%s\n' "$resolved"
        done
    fi
)

# <<< ad_store_db_path (b.2io) <<<

# --------------------------------------------------------------------
# The agent-rename migration (SR-11.2, b.ssz)
#
# This release renames config.toml's [defaults] key disable_askuserquestion
# to disable_ask_user, and a template's claude_args to agent_args, and gives
# every template an agent (SR-1.2). An install that crosses the rename (a
# store whose user_version is below the version the rename's store hop
# produces) rewrites config.toml and every stored template; any other
# install reads and changes neither.
#
# AGENT_RENAME_HOP_VERSION is that version: internal/store's
# agentRenameHopVersion (the v8 to v9 hop, migrateV8toV9). A Go test beside
# install_sh_sentinel_path_test.go fails when the two differ, so keep the
# assignment below on a line of its own, in this form.
# --------------------------------------------------------------------
AGENT_RENAME_HOP_VERSION=9

# >>> ad_agent_rename_migrate (b.ssz) >>>
#
# The migration's reader and rewriter, for one file. A block of its own (bash
# 3.2 or later, builtins only, no ad_* helper outside it), so a test can
# extract it with sed and run it alone, as it does ad_store_db_path's.
#
#   ad_agent_rename_migrate <config|template> <file>
#       config   : <file> is config.toml. The key disable_askuserquestion
#                  (any letter case) under a [defaults] header (any letter
#                  case) is renamed disable_ask_user, in place.
#       template : <file> is a template. Only its top-level part is read,
#                  the lines before its first table header (the first line
#                  whose first non-blank character, after a byte-order mark
#                  on line 1, is "["). A claude_args line (any letter case)
#                  is renamed agent_args in place, and when the part has no
#                  agent key (any letter case) the line agent = "claude" is
#                  added immediately before the first header, or last when
#                  the file has none (after a missing final line end is
#                  added). Its tables are never read or changed.
#       Status 0: the file changes. Its migrated bytes, exact, are all that
#                 is on stdout (redirect it to a file: a command
#                 substitution would drop the trailing line ends).
#       Status 3: nothing to change (also a <file> that is missing). Nothing
#                 on stdout.
#       Status 2: refused. The message is on stderr, and the caller exits 5
#                 with the name ad_agent_rename_err_name prints. Nothing on
#                 stdout.
#       Status 4: a write to stdout failed (a full disk, say); the error is
#                 on stderr. Any other status (1, a redirect that failed)
#                 is the caller's own write failing: it is never "no change".
#   ad_agent_rename_err_name <config|template>
#       prints the err_name of that kind's refusal: ErrConfigMalformed or
#       ErrTemplateMalformed.
#
# The accepted line form is the one ad_store_db_path accepts: blank lines and
# "#" comment lines; table headers naming one bare key ("[name]", blanks
# around and inside the brackets, a trailing "#" comment); "name = value"
# lines whose name is one bare key (letters, digits, "_", "-") and whose
# value ends on the line, optionally followed by a "#" comment. A UTF-8
# byte-order mark on line 1 and CRLF line ends are kept. Names match
# regardless of ASCII letter case. A template's top-level line outside the
# form is refused; config.toml is held to the form by ad_store_db_path
# already, so this reads it for the two keys only.
#
# The appended or inserted line ends with the file's line end: CRLF when the
# first line end in the file is CRLF, LF otherwise.

# ad_rn_ci <lowercase word> — set REPLY to a regular expression matching the
# word in any ASCII letter case ("agent" gives "[aA][gG][eE][nN][tT]").
ad_rn_ci() {
    local s="$1" i c pre low=abcdefghijklmnopqrstuvwxyz upper=ABCDEFGHIJKLMNOPQRSTUVWXYZ
    REPLY=""
    for ((i = 0; i < ${#s}; i++)); do
        c="${s:i:1}"
        pre="${low%%"$c"*}"
        if [[ "$pre" != "$low" ]]; then
            REPLY="${REPLY}[${c}${upper:${#pre}:1}]"
        else
            REPLY="${REPLY}${c}"
        fi
    done
}

# ad_rn_value_ends <text after the "="> — succeed when the value in <text>
# ends on its line: every string, array and inline table it opens is closed
# on it, a trailing "#" comment aside. A """ or ''' string closed on the line
# counts as ended.
ad_rn_value_ends() {
    local v="$1" i=0 n c depth=0 state="" rest
    n=${#v}
    while [[ "$i" -lt "$n" ]]; do
        c="${v:i:1}"
        case "$state" in
            d)
                if [[ "$c" == '\' ]]; then
                    i=$((i + 2))
                    continue
                elif [[ "$c" == '"' ]]; then
                    state=""
                fi
                ;;
            s)
                if [[ "$c" == "'" ]]; then
                    state=""
                fi
                ;;
            *)
                case "$c" in
                    '"')
                        if [[ "${v:i:3}" == '"""' ]]; then
                            rest="${v:i+3}"
                            [[ "$rest" == *'"""'* ]] || return 1
                            rest="${rest%%'"""'*}"
                            i=$((i + 6 + ${#rest}))
                            continue
                        fi
                        state=d
                        ;;
                    "'")
                        if [[ "${v:i:3}" == "'''" ]]; then
                            rest="${v:i+3}"
                            [[ "$rest" == *"'''"* ]] || return 1
                            rest="${rest%%"'''"*}"
                            i=$((i + 6 + ${#rest}))
                            continue
                        fi
                        state=s
                        ;;
                    '[' | '{') depth=$((depth + 1)) ;;
                    ']' | '}') depth=$((depth - 1)) ;;
                    '#') break ;;
                esac
                ;;
        esac
        i=$((i + 1))
    done
    [[ -z "$state" && "$depth" -le 0 ]]
}

ad_agent_rename_err_name() {
    case "$1" in
        config) echo ErrConfigMalformed ;;
        template) echo ErrTemplateMalformed ;;
    esac
}

ad_agent_rename_migrate() (
    LC_ALL=C
    local kind="$1" file="$2" bom=$'\xef\xbb\xbf' cr=$'\r' eol=$'\n'
    local count=0 i j line cls key val hname lbom body blanks changed=0 refused=0
    local in_defaults=0 seen_header=0 hits=0 olds=0 news=0 top_end=0 has_agent=0 insert=0
    local claude_n=0 agent_args_n=0 claude_at=0
    local re_skip='^[[:blank:]]*(#.*)?$'
    local re_header='^[[:blank:]]*\[[[:blank:]]*([A-Za-z0-9_-]+)[[:blank:]]*\][[:blank:]]*(#.*)?$'
    local re_bracket='^[[:blank:]]*\['
    local re_keyval='^[[:blank:]]*([A-Za-z0-9_-]+)[[:blank:]]*=[[:blank:]]*(.*)$'
    local re_defaults re_old re_old_any re_new re_claude re_agent_args re_agent
    local -a raw term hit_at
    ad_rn_ci defaults; re_defaults="^${REPLY}\$"
    ad_rn_ci disable_askuserquestion; re_old="^${REPLY}\$"; re_old_any="${REPLY}"
    ad_rn_ci disable_ask_user; re_new="^${REPLY}\$"
    ad_rn_ci claude_args; re_claude="^${REPLY}\$"
    ad_rn_ci agent_args; re_agent_args="^${REPLY}\$"
    ad_rn_ci agent; re_agent="^${REPLY}\$"

    if [[ ! -f "$file" ]]; then
        return 3
    fi
    # The lines, raw (a byte-order mark and a CR kept), and whether each had
    # its line end.
    while :; do
        if IFS= read -r line; then
            term[count]=1
        elif [[ -n "$line" ]]; then
            term[count]=0
        else
            break
        fi
        raw[count]="$line"
        count=$((count + 1))
    done <"$file"
    for ((i = 0; i < count; i++)); do
        if [[ "${term[i]}" -eq 1 ]]; then
            if [[ "${raw[i]}" == *"$cr" ]]; then
                eol=$'\r\n'
            fi
            break
        fi
    done

    # classify <i> — sets lbom (the byte-order mark on line 1), body (the line
    # without it and without its CR), cls (skip, header, bracket for a header
    # of another form, keyval, bad), and key, val, hname.
    classify() {
        local l="${raw[$1]}"
        lbom=""
        if [[ "$1" -eq 0 && "$l" == "$bom"* ]]; then
            lbom="$bom"
            l="${l#"$bom"}"
        fi
        l="${l%"$cr"}"
        body="$l"
        key="" val="" hname=""
        if [[ "$l" =~ $re_skip ]]; then
            cls=skip
        elif [[ "$l" =~ $re_header ]]; then
            cls=header
            hname="${BASH_REMATCH[1]}"
        elif [[ "$l" =~ $re_bracket ]]; then
            cls=bracket
        elif [[ "$l" =~ $re_keyval ]]; then
            cls=keyval
            key="${BASH_REMATCH[1]}"
            val="${BASH_REMATCH[2]}"
        else
            cls=bad
        fi
    }
    # rename_key <i> <length of the key now> <new key> — put <new key> in
    # place of the key of line <i>, every other byte kept.
    rename_key() {
        local l="${raw[$1]}" b=""
        if [[ "$1" -eq 0 && "$l" == "$bom"* ]]; then
            b="$bom"
            l="${l#"$bom"}"
        fi
        [[ "$l" =~ ^[[:blank:]]* ]]
        blanks="${BASH_REMATCH[0]}"
        raw[$1]="${b}${blanks}${3}${l:$((${#blanks} + $2))}"
    }
    # show_line <i> — one "line N  : text" line of a message, on stderr.
    show_line() {
        classify "$1"
        printf '  line %s  : %s\n' "$(($1 + 1))" "$body" >&2
    }
    refuse_tail() {
        echo '  See "`install.sh` refuses config.toml or a template" in docs/operator-actions.md.' >&2
        echo "  Nothing was installed or changed. Re-run this install after the change." >&2
    }

    if [[ "$kind" == config ]]; then
        for ((i = 0; i < count; i++)); do
            classify "$i"
            case "$cls" in
                header)
                    seen_header=1
                    in_defaults=0
                    if [[ "$hname" =~ $re_defaults ]]; then
                        in_defaults=1
                    fi
                    ;;
                bracket)
                    seen_header=1
                    in_defaults=0
                    ;;
                keyval)
                    if [[ "$seen_header" -eq 0 ]]; then
                        # defaults set as a key before any header, on a line
                        # that names the old key: the rename cannot reach it.
                        if [[ "$key" =~ $re_defaults && "$body" =~ $re_old_any ]]; then
                            if [[ "$refused" -eq 0 ]]; then
                                echo "install.sh: config.toml sets defaults as a key and names disable_askuserquestion in it; refusing to install." >&2
                            else
                                echo "install.sh: config.toml also sets defaults as a key and names disable_askuserquestion in it; refusing to install." >&2
                            fi
                            echo "  config  : $file" >&2
                            show_line "$i"
                            echo "  This release renames disable_askuserquestion only on its own line under a" >&2
                            echo "  [defaults] header, and rewrites no table set as a key (an inline table, say)," >&2
                            echo "  so the file would be refused after the upgrade. Remove this line, and set" >&2
                            echo "  disable_ask_user on its own line under the file's [defaults] header instead," >&2
                            echo "  adding that header at the end of the file if the file has none, together" >&2
                            echo "  with each other key this line sets." >&2
                            echo "  Add no header in this line's place: the lines below it, up to the next" >&2
                            echo "  header, would fall under that header too." >&2
                            refuse_tail
                            refused=1
                        fi
                    elif [[ "$in_defaults" -eq 1 ]]; then
                        if [[ "$key" =~ $re_old ]]; then
                            olds=$((olds + 1))
                            hit_at[hits]="$i"
                            hits=$((hits + 1))
                        elif [[ "$key" =~ $re_new ]]; then
                            news=$((news + 1))
                            hit_at[hits]="$i"
                            hits=$((hits + 1))
                        fi
                    fi
                    ;;
            esac
        done
        if [[ "$hits" -gt 1 ]]; then
            echo "install.sh: config.toml sets the question-tool key more than once under [defaults]; refusing to install." >&2
            echo "  config  : $file" >&2
            for ((j = 0; j < hits; j++)); do
                show_line "${hit_at[j]}"
            done
            echo "  This release renamed the key disable_askuserquestion to disable_ask_user and" >&2
            echo "  reads it once, under the new name. Keep one of these lines, named" >&2
            echo "  disable_ask_user, and delete the others." >&2
            refuse_tail
            refused=1
        fi
        if [[ "$refused" -eq 1 ]]; then
            return 2
        fi
        if [[ "$olds" -eq 1 ]]; then
            rename_key "${hit_at[0]}" 23 disable_ask_user
            changed=1
        fi
    elif [[ "$kind" == template ]]; then
        top_end=$count
        for ((i = 0; i < count; i++)); do
            classify "$i"
            case "$cls" in
                skip) ;;
                header | bracket)
                    top_end=$i
                    break
                    ;;
                keyval)
                    if ! ad_rn_value_ends "$val"; then
                        cls=bad
                    elif [[ "$key" =~ $re_claude ]]; then
                        if [[ "$claude_n" -eq 0 ]]; then
                            claude_at=$i
                        fi
                        claude_n=$((claude_n + 1))
                        hit_at[hits]="$i"
                        hits=$((hits + 1))
                    elif [[ "$key" =~ $re_agent_args ]]; then
                        agent_args_n=$((agent_args_n + 1))
                        hit_at[hits]="$i"
                        hits=$((hits + 1))
                    elif [[ "$key" =~ $re_agent ]]; then
                        has_agent=1
                    fi
                    ;;
            esac
            if [[ "$cls" == bad ]]; then
                echo "install.sh: template $file holds a line the migration cannot read; refusing to install." >&2
                echo "  template: $file" >&2
                printf '  line %s  : %s\n' "$((i + 1))" "$body" >&2
                echo "  The migration reads the lines of a template before its first [table] header" >&2
                echo "  as blank lines, # comments and name = value lines (a bare name, a value that" >&2
                echo "  ends on the line), and this line is none of those (a quoted or dotted name," >&2
                echo "  or a value that goes on to the next line, say). Rewrite it as one bare" >&2
                echo "  name = value line, then re-run." >&2
                refuse_tail
                return 2
            fi
        done
        if [[ "$claude_n" -gt 1 ]]; then
            echo "install.sh: template $file sets claude_args more than once; refusing to install." >&2
            echo "  template: $file" >&2
            for ((j = 0; j < hits; j++)); do
                classify "${hit_at[j]}"
                if [[ "$key" =~ $re_claude ]]; then
                    show_line "${hit_at[j]}"
                fi
            done
            echo "  This release renamed claude_args to agent_args and reads it once. Keep one of" >&2
            echo "  these lines, named agent_args, and delete the others." >&2
            refuse_tail
            refused=1
        fi
        if [[ "$claude_n" -ge 1 && "$agent_args_n" -ge 1 ]]; then
            echo "install.sh: template $file sets both claude_args and agent_args; refusing to install." >&2
            echo "  template: $file" >&2
            for ((j = 0; j < hits; j++)); do
                show_line "${hit_at[j]}"
            done
            echo "  This release renamed claude_args to agent_args and reads one of them. Keep one" >&2
            echo "  line, named agent_args, and delete the other." >&2
            refuse_tail
            refused=1
        fi
        if [[ "$refused" -eq 1 ]]; then
            return 2
        fi
        if [[ "$claude_n" -eq 1 ]]; then
            rename_key "$claude_at" 11 agent_args
            changed=1
        fi
        if [[ "$has_agent" -eq 0 ]]; then
            insert=1
            changed=1
        fi
    else
        echo "ad_agent_rename_migrate: unknown kind: $kind" >&2
        return 2
    fi

    if [[ "$changed" -eq 0 ]]; then
        return 3
    fi
    # emit — the migrated bytes on stdout; it fails when a write does.
    emit() {
        for ((i = 0; i < count; i++)); do
            if [[ "$insert" -eq 1 && "$i" -eq "$top_end" ]]; then
                if [[ "$i" -eq 0 && "${raw[0]}" == "$bom"* ]]; then
                    printf '%s' "$bom" || return 1
                    raw[0]="${raw[0]#"$bom"}"
                fi
                printf 'agent = "claude"%s' "$eol" || return 1
            fi
            printf '%s' "${raw[i]}" || return 1
            if [[ "${term[i]}" -eq 1 ]]; then
                printf '\n' || return 1
            fi
        done
        if [[ "$insert" -eq 1 && "$top_end" -eq "$count" ]]; then
            if [[ "$count" -gt 0 && "${term[count - 1]}" -eq 0 ]]; then
                printf '%s' "$eol" || return 1
            fi
            printf 'agent = "claude"%s' "$eol" || return 1
        fi
    }
    if ! emit; then
        return 4
    fi
    return 0
)

# <<< ad_agent_rename_migrate (b.ssz) <<<

# --------------------------------------------------------------------
# AD's directory moves to ~/.config/agent-director (SR-11.7, b.nx4)
#
# An earlier release kept its files in ~/.agent-director. This release keeps
# them in ~/.config/agent-director, and this installer moves the old
# directory there, once, in the install's middle (below, after both binary
# sources are found and checked): the old directory is copied, checked and
# renamed to ~/.agent-director.pre-move-<YYYYmmdd-HHMMSS>, kept for the
# operator to delete once the upgrade is confirmed.
#
# This section decides, before every other pre-flight reader, which layout
# is there, and stops with exit 5 (ErrMoveConflict) on a layout it will not
# move, changing nothing:
#   - ~/.agent-director is "present" when anything is at that name (a
#     dangling link included), except a compatibility link: a link whose raw
#     target, read against $HOME when relative and cleaned, is
#     ~/.config/agent-director. That counts as no old directory.
#   - ~/.config/agent-director that is a link to a directory counts as that
#     directory.
#   - The move is due when the old directory is present and the new one is
#     absent or holds the marker .move-incomplete (a move that did not
#     finish). While it is due the pre-flight reads resolve in the old
#     directory (PREFLIGHT_ROOT), as SR-11.2's gate does; otherwise in the
#     new one.
# The two causes that need the config keys are checked after the reader has
# read them (below).
# --------------------------------------------------------------------

# ad_resolve_link <file> — print the path <file> resolves to through its
# chain of symlinks, or <file> itself when it is not a link (b.nw5). Each
# relative link target is read against its own link's directory. A loop
# over readlink, as readlink -f is missing from older macOS. A dangling
# link gives the missing path it names. More than 40 links (a loop) fails,
# as the kernel does.
ad_resolve_link() {
    local path="$1" link hops=0
    while [[ -L "$path" ]]; do
        if (( ++hops > 40 )); then
            echo "install.sh: $1: too many levels of symbolic links" >&2
            return 1
        fi
        link=$(readlink "$path") || return 1
        case "$link" in
            /*) path="$link" ;;
            *) path="$(dirname "$path")/$link" ;;
        esac
    done
    printf '%s\n' "$path"
}

# ad_mode_of <file> — print <file>'s permission bits in octal. stat -L
# reads the mode of the file a symlinked <file> points at, not the link's
# own 777; -c is GNU stat, -f BSD stat.
ad_mode_of() {
    stat -L -c '%a' "$1" 2>/dev/null || stat -L -f '%Lp' "$1"
}

OLD_ROOT_CLEAN="$(ad_clean_path "$OLD_INSTALL_ROOT")"
NEW_ROOT_CLEAN="$(ad_clean_path "$DEFAULT_INSTALL_ROOT")"
HOME_CLEAN="$(ad_clean_path "$HOME")"

# ad_ts — the timestamp the installer puts in the names it makes.
ad_ts() {
    date +%Y%m%d-%H%M%S
}

# ad_mtime_text <path> — print <path>'s last change as YYYY-mm-dd HH:MM:SS
# (GNU or BSD stat and date), or nothing when it cannot be read.
ad_mtime_text() {
    local e
    e="$(stat -L -c '%Y' "$1" 2>/dev/null || stat -L -f '%m' "$1" 2>/dev/null)" || return 0
    if [[ ! "$e" =~ ^[0-9]+$ ]]; then
        return 0
    fi
    date -d "@${e}" '+%Y-%m-%d %H:%M:%S' 2>/dev/null \
        || date -r "$e" '+%Y-%m-%d %H:%M:%S' 2>/dev/null \
        || true
}

# ad_store_changed <dir> — print when the default store in <dir> (state.db,
# or its -wal, whichever changed last) last changed, or "no store".
ad_store_changed() {
    local best="" f t
    for f in "$1/state.db" "$1/state.db-wal"; do
        if [[ -f "$f" ]]; then
            t="$(ad_mtime_text "$f")"
            if [[ "$t" > "$best" ]]; then
                best="$t"
            fi
        fi
    done
    printf '%s' "${best:-no store}"
}

# ad_move_conflict <headline> <line>... — report one ErrMoveConflict cause
# on stderr (the headline, each line indented under it, a line saying
# nothing was changed and where the remedy is documented) and exit 5 with
# its err_name. A line may hold several lines. Nothing was changed.
ad_move_conflict() {
    local head="$1" l
    shift
    echo "install.sh: ${head}" >&2
    for l in "$@"; do
        printf '%s\n' "$l" | sed 's/^/  /' >&2
    done
    echo "  Nothing was installed or changed. Re-run this install after the change." >&2
    echo "  (operator-actions.md, \"\`install.sh\` refuses with \`ErrMoveConflict\`\")" >&2
    ad_exit_5 ErrMoveConflict
}

# ad_is_compat_link <path> — true when <path> is a link whose raw target,
# read against $HOME when relative and cleaned, is AD's new directory (the
# compatibility link of SR-11.7, whether or not the target exists).
# Keep in step with uninstall.sh, which inlines this test and the OLD_PRESENT
# rule below ("Which AD directory this run works on"): change both together.
ad_is_compat_link() {
    local t
    if [[ ! -L "$1" ]]; then
        return 1
    fi
    t="$(readlink "$1")" || return 1
    case "$t" in
        /*) ;;
        *) t="${HOME}/${t}" ;;
    esac
    [[ "$(ad_clean_path "$t")" == "$NEW_ROOT_CLEAN" ]]
}

# ad_link_copy_lines <link> — print the remedy for a link the move will not
# follow: replace it with a copy of its target (a dangling link, with
# nothing to copy, is removed), commands with %q-quoted paths.
ad_link_copy_lines() {
    local link="$1" target
    if target="$(ad_resolve_link "$link" 2>/dev/null)" && [[ -e "$target" ]]; then
        echo "Replace the link with a copy of what it points at, re-run, and make the link again in the new directory afterwards if you want it:"
        if [[ -d "$target" ]]; then
            printf '  cp -R -- %q %q\n' "$target" "${link}.copy"
            printf '  rm -- %q\n' "$link"
            printf '  mv -- %q %q\n' "${link}.copy" "$link"
        else
            printf '  cp -- %q %q\n' "$target" "${link}.copy"
            printf '  mv -f -- %q %q\n' "${link}.copy" "$link"
        fi
    else
        echo "It is a dangling link (it points at nothing). Remove it, re-run, and make the link again in the new directory afterwards if you want it:"
        printf '  rm -- %q\n' "$link"
    fi
}

# ad_phys_place <path> — print <path>'s physical place: its directory
# resolved with links followed (cd -P, pwd -P) joined with its last element;
# a <path> that is itself a link is followed first. Fails when a link loops
# or the directory is missing or unreadable.
ad_phys_place() {
    local p="$1" d b
    if [[ -L "$p" ]]; then
        p="$(ad_resolve_link "$p" 2>/dev/null)" || return 1
    fi
    d="$(dirname "$p")"
    b="$(basename "$p")"
    d="$(cd -P -- "$d" 2>/dev/null && pwd -P)" || return 1
    if [[ "$d" == / ]]; then
        printf '/%s\n' "$b"
    else
        printf '%s/%s\n' "$d" "$b"
    fi
}

# Keep in step with uninstall.sh, which inlines this rule and
# ad_is_compat_link ("Which AD directory this run works on").
OLD_PRESENT=0
if [[ -e "$OLD_INSTALL_ROOT" || -L "$OLD_INSTALL_ROOT" ]] && ! ad_is_compat_link "$OLD_INSTALL_ROOT"; then
    OLD_PRESENT=1
fi

# A new path that is a dangling link or not a directory: on every install.
if [[ ( -e "$DEFAULT_INSTALL_ROOT" || -L "$DEFAULT_INSTALL_ROOT" ) && ! -d "$DEFAULT_INSTALL_ROOT" ]]; then
    if [[ -L "$DEFAULT_INSTALL_ROOT" && ! -e "$DEFAULT_INSTALL_ROOT" ]]; then
        ad_move_conflict "$DEFAULT_INSTALL_ROOT is a dangling link; refusing to install." \
            "It points at $(readlink "$DEFAULT_INSTALL_ROOT"), which does not exist." \
            "Remove it, or fix it so it names a directory, then re-run:" \
            "  rm -- $(printf '%q' "$DEFAULT_INSTALL_ROOT")"
    fi
    ad_move_conflict "$DEFAULT_INSTALL_ROOT is not a directory; refusing to install." \
        "AD keeps its files in a directory there (or a link to one)." \
        "Rename it aside, or fix it so it is a directory, then re-run:" \
        "  mv -- $(printf '%q' "$DEFAULT_INSTALL_ROOT") $(printf '%q' "${DEFAULT_INSTALL_ROOT}.aside-$(ad_ts)")"
fi

# An old path that is a dangling link other than a compatibility link: on
# every install.
if [[ "$OLD_PRESENT" -eq 1 && -L "$OLD_INSTALL_ROOT" && ! -e "$OLD_INSTALL_ROOT" ]]; then
    ad_move_conflict "$OLD_INSTALL_ROOT is a dangling link; refusing to install." \
        "It points at $(readlink "$OLD_INSTALL_ROOT"), which does not exist, and is not the compatibility link to $NEW_ROOT_CLEAN." \
        "Remove it, then re-run:" \
        "  rm -- $(printf '%q' "$OLD_INSTALL_ROOT")"
fi

# A marker, left by a move that did not finish, decides what the re-run does
# (SR-11.7 "A failed move"): when the kept directory it names exists the
# move finished and only the marker's removal did not, so the marker goes
# (a present old directory is then the both-present conflict below);
# otherwise, with an old directory present, the new one is a partial copy,
# deleted by the move, which is due again; otherwise the marker goes and the
# install goes on.
MOVE_PARTIAL=0
if [[ -f "$MOVE_MARKER" || -L "$MOVE_MARKER" ]]; then
    marker_kept=""
    IFS= read -r marker_kept <"$MOVE_MARKER" || true
    if [[ -n "$marker_kept" && ( -e "$marker_kept" || -L "$marker_kept" ) ]]; then
        rm -f "$MOVE_MARKER"
    elif [[ "$OLD_PRESENT" -eq 1 ]]; then
        MOVE_PARTIAL=1
    else
        rm -f "$MOVE_MARKER"
    fi
fi

# Both directories present, and the new one holds no marker.
if [[ "$OLD_PRESENT" -eq 1 && -d "$DEFAULT_INSTALL_ROOT" && "$MOVE_PARTIAL" -eq 0 ]]; then
    aside_ts="$(ad_ts)"
    ad_move_conflict "both $OLD_INSTALL_ROOT and $DEFAULT_INSTALL_ROOT exist; refusing to install." \
        "old: $OLD_INSTALL_ROOT (store last changed: $(ad_store_changed "$OLD_INSTALL_ROOT"))" \
        "new: $DEFAULT_INSTALL_ROOT (store last changed: $(ad_store_changed "$DEFAULT_INSTALL_ROOT"))" \
        "This release moves the old directory into the new place, so only one of them may be there. Set one aside, then re-run." \
        "If the old directory holds the install in use, set the new one aside; the re-run moves the old directory in:" \
        "  mv -- $(printf '%q' "$DEFAULT_INSTALL_ROOT") $(printf '%q' "${DEFAULT_INSTALL_ROOT}.aside-${aside_ts}")" \
        "Otherwise (the new one holds the install in use), set the old one aside:" \
        "  mv -- $(printf '%q' "$OLD_INSTALL_ROOT") $(printf '%q' "${OLD_INSTALL_ROOT}.aside-${aside_ts}")" \
        "A new directory holding only what make-template created (a templates directory, say) next to a real old install is the first case: set it aside, and copy any template you want from it into the moved directory afterwards."
fi

MOVE_DUE=0
if [[ "$OLD_PRESENT" -eq 1 ]] && { [[ ! -e "$DEFAULT_INSTALL_ROOT" && ! -L "$DEFAULT_INSTALL_ROOT" ]] || [[ "$MOVE_PARTIAL" -eq 1 ]]; }; then
    MOVE_DUE=1
fi

# While the move is due: the old directory must be a real directory with no
# link at the places the move copies by content.
OLD_PHYS=""
if [[ "$MOVE_DUE" -eq 1 ]]; then
    if [[ -L "$OLD_INSTALL_ROOT" && -d "$OLD_INSTALL_ROOT" ]]; then
        ad_move_conflict "$OLD_INSTALL_ROOT is a link; refusing to install." \
            "The move copies AD's old directory and will not follow a link to it." \
            "$(ad_link_copy_lines "$OLD_INSTALL_ROOT")"
    fi
    if [[ ! -d "$OLD_INSTALL_ROOT" ]]; then
        ad_move_conflict "$OLD_INSTALL_ROOT is not a directory; refusing to install." \
            "AD's old directory must be a directory for the move to copy it." \
            "Rename it aside and re-run; this install then makes a fresh install in $NEW_ROOT_CLEAN, and nothing of the old one is moved:" \
            "  mv -- $(printf '%q' "$OLD_INSTALL_ROOT") $(printf '%q' "${OLD_INSTALL_ROOT}.aside-$(ad_ts)")"
    fi
    for old_entry in "${OLD_INSTALL_ROOT}/config.toml" "${OLD_INSTALL_ROOT}/templates"; do
        if [[ -L "$old_entry" ]]; then
            ad_move_conflict "$old_entry is a link; refusing to install." \
                "The move copies AD's old directory and will not follow a link inside it." \
                "$(ad_link_copy_lines "$old_entry")"
        fi
    done
    for old_entry in "${OLD_INSTALL_ROOT}/templates"/*; do
        if [[ -L "$old_entry" ]]; then
            ad_move_conflict "template $old_entry is a link; refusing to install." \
                "The move copies AD's old directory and will not follow a link inside it." \
                "$(ad_link_copy_lines "$old_entry")"
        fi
    done
    OLD_PHYS="$(cd -P -- "$OLD_INSTALL_ROOT" 2>/dev/null && pwd -P)" || OLD_PHYS="$OLD_ROOT_CLEAN"
    PREFLIGHT_ROOT="$OLD_INSTALL_ROOT"
else
    PREFLIGHT_ROOT="$DEFAULT_INSTALL_ROOT"
fi
PREFLIGHT_CONFIG="${PREFLIGHT_ROOT}/config.toml"

# ad_key_class <resolved path> — place a config path key's resolved path
# (what ad_store_db_path printed) against the old directory, as SR-11.7's
# "Path keys" does. Sets KEY_PHYS, the physical place of the path (links
# resolved, ad_phys_place), and KEY_CLASS:
#   in    lexically inside the old directory and physically inside it
#   link  lexically outside, physically inside: it reaches the old
#         directory only through a link
#   warn  lexically inside, physically outside: an inner link leads out
#   out   outside both ways
# Fails (KEY_CLASS empty) when the place cannot be resolved: its directory
# is missing or unreadable. Needs OLD_PHYS, so only while the move is due.
KEY_PHYS=""
KEY_CLASS=""
ad_key_class() {
    local p="$1" lex_in=0 phys_in=0
    KEY_PHYS=""
    KEY_CLASS=""
    KEY_PHYS="$(ad_phys_place "$p")" || return 1
    if [[ "$p" == "$OLD_ROOT_CLEAN"/* ]]; then
        lex_in=1
    fi
    if [[ "$KEY_PHYS" == "$OLD_PHYS"/* ]]; then
        phys_in=1
    fi
    if [[ "$lex_in" -eq 1 && "$phys_in" -eq 1 ]]; then
        KEY_CLASS=in
    elif [[ "$lex_in" -eq 0 && "$phys_in" -eq 1 ]]; then
        KEY_CLASS=link
    elif [[ "$lex_in" -eq 1 ]]; then
        KEY_CLASS=warn
    else
        KEY_CLASS=out
    fi
}

# ad_key_unresolvable <key> <path> — report a path key whose place
# ad_key_class could not resolve (ErrMoveConflict) and exit 5.
ad_key_unresolvable() {
    ad_move_conflict "cannot place $1 $2 against the old directory; refusing to install." \
        "Its directory, $(dirname "$2"), is missing or cannot be read, so install.sh cannot tell whether the path is inside $OLD_INSTALL_ROOT, which the move needs to know." \
        "Create the directory, or correct the value in ${PREFLIGHT_CONFIG}, then re-run:" \
        "  mkdir -p -- $(printf '%q' "$(dirname "$2")")"
}

# ad_raw_path_value <config> <table> <key> — print the value of <key> under
# [<table>] in <config> as written, without its quotes (nothing when the file
# or the key is missing, or the value is not a one-line "..." or '...' string).
# Call it only after ad_store_db_path accepted <config>: that reader has
# refused every other form, so a bare name = value line is all there is.
ad_raw_path_value() {
    local cfg="$1" want_table="$2" want_key="$3" line n=0 table="" rest val=""
    local re_header='^[[:blank:]]*\[[[:blank:]]*([A-Za-z0-9_-]+)[[:blank:]]*\][[:blank:]]*(#.*)?$'
    local re_keyval='^[[:blank:]]*([A-Za-z0-9_-]+)[[:blank:]]*=[[:blank:]]*(.*)$'
    local re_dq='^"([^"]*)"'
    local re_sq="^'([^']*)'"
    if [[ ! -f "$cfg" ]]; then
        return 0
    fi
    while IFS= read -r line || [[ -n "$line" ]]; do
        n=$((n + 1))
        if [[ "$n" -eq 1 ]]; then
            line="${line#$'\xef\xbb\xbf'}"
        fi
        line="${line%$'\r'}"
        if [[ "$line" =~ $re_header ]]; then
            table="${BASH_REMATCH[1]}"
        elif [[ "$table" == "$want_table" && "$line" =~ $re_keyval && "${BASH_REMATCH[1]}" == "$want_key" ]]; then
            rest="${BASH_REMATCH[2]}"
            if [[ "$rest" =~ $re_dq ]]; then
                val="${BASH_REMATCH[1]}"
            elif [[ "$rest" =~ $re_sq ]]; then
                val="${BASH_REMATCH[1]}"
            fi
        fi
    done <"$cfg"
    printf '%s' "$val"
}

# ad_move_refuse_outward_path <table> <key> <resolved path> — the one place
# that decides what the move does with a path key written as an absolute or
# `~/` path that is inside the old directory by its text but reaches outside
# it through a link inside it (class warn, see ad_key_class). SR-11.7 says
# such a key stays as written, but the move renames the old directory away,
# so the path as written would name nothing afterwards and the store open
# would recreate an empty directory there. Pending the User's decision
# (rewrite or refuse), the move REFUSES it here, in pre-flight: ErrMoveConflict
# (exit 5), nothing changed, the cause naming the key, its value and the
# physical place it reaches, the remedy being to set the key to that absolute
# physical path.
# TO SWITCH TO A REWRITE: delete the two calls of this function in the
# pre-flight below. ad_move_key_newvalue's warn case already writes every
# form of such a key as its absolute physical path ($KEY_PHYS), as it does
# for a relative value today. Nothing else changes.
ad_move_refuse_outward_path() {
    local table="$1" key="$2" resolved="$3" raw label
    label="[${table}] ${key}"
    raw="$(ad_raw_path_value "$PREFLIGHT_CONFIG" "$table" "$key")"
    case "$raw" in
        "~/"*|/*) ;;
        *) return 0 ;;
    esac
    resolved="$(ad_clean_path "$resolved")"
    if ! ad_key_class "$resolved"; then
        ad_key_unresolvable "$label" "$resolved"
    fi
    if [[ "$KEY_CLASS" != warn ]]; then
        return 0
    fi
    ad_move_conflict "$label $raw reaches outside $OLD_INSTALL_ROOT through a link inside it; refusing to install." \
        "$label = $raw is inside $OLD_INSTALL_ROOT as written, but a link inside it leads out, so it reaches $KEY_PHYS today." \
        "The move renames $OLD_INSTALL_ROOT away, so the path as written would no longer reach $KEY_PHYS, and agent-director would create an empty one there." \
        "Set the key to the absolute physical path it reaches, in ${PREFLIGHT_CONFIG}, then re-run:" \
        "  ${key} = \"${KEY_PHYS}\""
}

# The store's place and the error log's place, while the move is due.
STORE_PHYS=""
STORE_CLASS=""
LOG_PHYS=""
LOG_CLASS=""
old_error_log=""
# [launch] claude_path and codex_path as the reader resolved them ("" when
# unset), read with the store's place from the same file (SR-11.1): the old
# directory's while the move is due, the new one's otherwise. The move
# rewrites only db_path and error_log_path, so these stay true after it.
LAUNCH_CLAUDE_PATH=""
LAUNCH_CODEX_PATH=""
if [[ "$MOVE_DUE" -eq 1 ]]; then
    # While the move is due the reader also reads [log] error_log_path. Its
    # lines come by position (store, error log, claude_path, codex_path), an
    # empty one included, which command substitution would drop at the end.
    if ! reader_out="$(ad_store_db_path "$PREFLIGHT_CONFIG" "$HOME" "$PREFLIGHT_ROOT" log launch)"; then
        ad_exit_5 ErrConfigMalformed
    fi
    {
        IFS= read -r state_db || true
        IFS= read -r old_error_log || true
        IFS= read -r LAUNCH_CLAUDE_PATH || true
        IFS= read -r LAUNCH_CODEX_PATH || true
    } <<<"$reader_out"
    # The two ErrMoveConflict causes that need the keys: a path key whose
    # place cannot be resolved, and a store inside the old directory that is
    # a link ("inside" judged by the store's path before links resolve).
    if ! ad_key_class "$state_db"; then
        ad_key_unresolvable "[store] db_path" "$state_db"
    fi
    STORE_PHYS="$KEY_PHYS"
    STORE_CLASS="$KEY_CLASS"
    if [[ -n "$old_error_log" ]]; then
        if ! ad_key_class "$old_error_log"; then
            ad_key_unresolvable "[log] error_log_path" "$old_error_log"
        fi
        LOG_PHYS="$KEY_PHYS"
        LOG_CLASS="$KEY_CLASS"
    fi
    if [[ ( "$STORE_CLASS" == in || "$STORE_CLASS" == link || "$STORE_CLASS" == warn ) && -L "$state_db" ]]; then
        ad_move_conflict "the store $state_db is a link; refusing to install." \
            "The move copies the store inside AD's old directory with SQLite's backup and will not follow a link to it." \
            "$(ad_link_copy_lines "$state_db")"
    fi
    ad_move_refuse_outward_path store db_path "$state_db"
    if [[ -n "$old_error_log" ]]; then
        ad_move_refuse_outward_path log error_log_path "$old_error_log"
    fi
else
    if ! reader_out="$(ad_store_db_path "$PREFLIGHT_CONFIG" "$HOME" "$PREFLIGHT_ROOT" launch)"; then
        ad_exit_5 ErrConfigMalformed
    fi
    {
        IFS= read -r state_db || true
        IFS= read -r LAUNCH_CLAUDE_PATH || true
        IFS= read -r LAUNCH_CODEX_PATH || true
    } <<<"$reader_out"
fi
# Messages name the store "state.db" when it is the default one, as
# before, and by its path when [store] db_path moves it.
state_db_name="state.db"
if [[ "$state_db" != "$(ad_clean_path "${PREFLIGHT_ROOT}/state.db")" ]]; then
    state_db_name="$state_db"
fi

# --------------------------------------------------------------------
# Agents: Claude Code and Codex (SR-11.1)
#
# Found here, right after the [launch] reader, before anything on disk
# changes. An agent is present when its launch path names an existing
# executable file, or its program name is on PATH. Presence only: nothing is
# run, and no [launch] key is written. At least one agent must be present, or
# the install stops (exit 2); that stop (ad_refuse_no_agent) comes after the
# last pre-flight config read and check, so a config.toml refused for its
# form stops the install first, exit 5.
# --------------------------------------------------------------------

# ad_find_agent <name> <launch path> — look for the agent whose program is
# <name> (claude, codex) and whose [launch] path is <launch path> ("" when
# unset): its launch path when that names an existing executable file, else
# <name> on PATH. Sets agent_prog (what to run: the launch path, or the bare
# name; empty when absent), agent_path (where it is, for messages), agent_how
# (launch, path or none) and agent_bad (1 when a launch path is set and names
# no executable file, whether or not PATH then supplies the program).
ad_find_agent() {
    local name="$1" lpath="$2" found
    agent_prog="" agent_path="" agent_how=none agent_bad=0
    if [[ -n "$lpath" ]]; then
        if [[ -f "$lpath" && -x "$lpath" ]]; then
            agent_prog="$lpath"
            agent_path="$lpath"
            agent_how=launch
            return 0
        fi
        agent_bad=1
    fi
    if found="$(command -v "$name" 2>/dev/null)" && [[ -n "$found" ]]; then
        agent_prog="$name"
        agent_path="$found"
        agent_how=path
    fi
    return 0
}

ad_find_agent claude "$LAUNCH_CLAUDE_PATH"
CLAUDE_HOW="$agent_how"
CLAUDE_BAD="$agent_bad"
CLAUDE_FOUND_PATH="$agent_path"
if [[ -n "$agent_prog" ]]; then
    CLAUDE_PRESENT=1
    CLAUDE_PROG="$agent_prog"
fi
ad_find_agent codex "$LAUNCH_CODEX_PATH"
CODEX_HOW="$agent_how"
CODEX_BAD="$agent_bad"
CODEX_FOUND_PATH="$agent_path"
if [[ -n "$agent_prog" ]]; then
    CODEX_PRESENT=1
fi

# ad_agent_absent_reason <name> <key> <launch path> <bad> — one phrase saying
# why the agent was not found: its launch path names no executable file (when
# one is set), and its program is not on PATH.
ad_agent_absent_reason() {
    if [[ "$4" -eq 1 ]]; then
        printf '[launch] %s = "%s" names no executable file, and %s is not on PATH' "$2" "$3" "$1"
    else
        printf '%s is not on PATH, and [launch] %s is not set' "$1" "$2"
    fi
}

# ad_refuse_no_agent — stop the install (exit 2) when neither agent was found.
# Called after the last pre-flight config read and link check below, so every
# config-form refusal (exit 5) comes first. The advice names PREFLIGHT_CONFIG,
# the file pre-flight read (the old directory's while the move is due).
ad_refuse_no_agent() {
    if [[ "$CLAUDE_PRESENT" -eq 1 || "$CODEX_PRESENT" -eq 1 ]]; then
        return 0
    fi
    echo "install.sh: neither Claude Code nor Codex was found; refusing to install." >&2
    echo "  Claude Code: $(ad_agent_absent_reason claude claude_path "$LAUNCH_CLAUDE_PATH" "$CLAUDE_BAD")" >&2
    echo "  Codex      : $(ad_agent_absent_reason codex codex_path "$LAUNCH_CODEX_PATH" "$CODEX_BAD")" >&2
    echo "  agent-director launches Claude Code and Codex agents and needs at least one" >&2
    echo "  of them. Install either, then re-run this install:" >&2
    echo "    Claude Code: https://claude.com/claude-code, with claude on PATH, or its" >&2
    echo "      absolute path as claude_path under [launch] in ${PREFLIGHT_CONFIG}." >&2
    echo "    Codex: see Requirements in docs/install.md" >&2
    echo "      (https://github.com/${RELEASE_REPO_SLUG}/blob/main/docs/install.md), with codex on PATH," >&2
    echo "      or its absolute path as codex_path under [launch] in ${PREFLIGHT_CONFIG}." >&2
    echo "  Nothing was installed or changed. Re-run this install after the change." >&2
    exit 2
}

# ad_print_agent_summary — the pre-flight summary's line for each agent: the
# program found and its version line, "not installed", or that a launch path
# set names no executable file. Claude Code's version comes from running its
# program. Codex is never run: `codex --version` writes files in the Codex
# folder (RN-24), so its line gives the program's path and says it was not
# run, and install.sh touches nothing in any Codex folder.
ad_print_agent_summary() {
    if [[ "$CLAUDE_PRESENT" -eq 1 ]]; then
        if [[ "$CLAUDE_BAD" -eq 1 ]]; then
            echo "  claude  : [launch] claude_path = \"$LAUNCH_CLAUDE_PATH\" names no executable file; using claude from PATH"
        fi
        echo "  claude  : $("$CLAUDE_PROG" --version 2>/dev/null || echo '<unknown>') (program: $CLAUDE_FOUND_PATH)"
    else
        echo "  claude  : not installed ($(ad_agent_absent_reason claude claude_path "$LAUNCH_CLAUDE_PATH" "$CLAUDE_BAD"))"
    fi
    if [[ "$CODEX_PRESENT" -eq 1 ]]; then
        if [[ "$CODEX_BAD" -eq 1 ]]; then
            echo "  codex   : [launch] codex_path = \"$LAUNCH_CODEX_PATH\" names no executable file; using codex from PATH"
        fi
        echo "  codex   : $CODEX_FOUND_PATH (version not checked: codex --version writes files in the Codex folder, so install.sh does not run it; check it yourself with: $CODEX_FOUND_PATH --version)"
    else
        echo "  codex   : not installed ($(ad_agent_absent_reason codex codex_path "$LAUNCH_CODEX_PATH" "$CODEX_BAD"))"
    fi
}

# --------------------------------------------------------------------
# Store busy timeout: [store] busy_timeout_ms (b.c7f)
#
# Every agent-director connection to the store waits up to [store]
# busy_timeout_ms in ~/.config/agent-director/config.toml for a lock another
# connection holds (SQLite's busy timeout; internal/config
# Store.EffectiveBusyTimeoutMs). install.sh's sqlite3 reads of the store's
# user_version (ad_user_version) wait the same time, and so does the check
# command it prints, so install.sh reads the key itself, from the file
# ad_store_db_path has just accepted. That reader refuses every way to set
# a [store] key but a bare name = value line under one [store] header, so
# this reader looks only at those lines.
#
# The value resolves as Go resolves it: a missing file or key, or 0, gives
# the default (internal/config DefaultStoreBusyTimeoutMs); 1 to 2147483647
# (MaxStoreBusyTimeoutMs) is used as written. A value agent-director
# refuses, a negative one or one above 2147483647, gives the default here
# too, for the reads before the binary loads the config only: the binary
# then refuses the file itself (ErrConfigMalformed) at step 3, or at step 4
# on a fresh install, before any migration is authorized. The reader reads
# the value only in TOML's decimal integer form (an optional sign, no
# leading zero, _ only between two digits), optionally followed by a #
# comment. It refuses the key in other letter case (agent-director's TOML
# decoder matches names regardless of case), a second busy_timeout_ms, and
# any other form of value (quoted, hexadecimal, octal, binary or with a
# decimal point, say), naming the line and what to write instead. As for
# db_path, a refusal stops the install (exit 5) here, before anything on
# disk changes.
#
# The block between the >>> and <<< marker lines is self-contained (bash
# 3.2 or later, builtins only), so a test can extract it with sed and call
# ad_store_busy_timeout_ms alone.
# --------------------------------------------------------------------

# >>> ad_store_busy_timeout_ms (b.c7f) >>>

# ad_busy_timeout_refuse <config> <line number> <line> <reason>... — report
# a busy_timeout_ms line the reader cannot read, with <reason>s saying what
# to change, then exit 1. Called only inside ad_store_busy_timeout_ms's
# subshell.
ad_busy_timeout_refuse() {
    local config="$1" n="$2" line="$3" reason
    shift 3
    echo "install.sh: cannot tell how long agent-director waits for a locked store database; refusing to install." >&2
    echo "  config  : $config" >&2
    printf '  line %s  : %s\n' "$n" "$line" >&2
    for reason in "$@"; do
        echo "  $reason" >&2
    done
    echo "  install.sh reads [store] busy_timeout_ms itself, to wait as long as" >&2
    echo "  agent-director does for a locked store when it reads the store's schema" >&2
    echo "  version, and reads it only as a whole number of milliseconds in decimal" >&2
    echo "  digits (optionally signed, with _ only between two digits) and an" >&2
    echo "  optional # comment after it." >&2
    echo "  Nothing was installed or changed. Re-run this install after the change." >&2
    exit 1
}

# ad_store_busy_timeout_ms <config> — print the busy timeout, in whole
# milliseconds, that agent-director's store connections use for config file
# <config>, resolved as above; a missing <config> gives the default. On a
# busy_timeout_ms line the reader cannot read, print why on stderr and
# return 1, printing nothing on stdout. Call it only after ad_store_db_path
# accepted <config>. The body is a subshell under the C locale, as that
# reader's is.
ad_store_busy_timeout_ms() (
    LC_ALL=C
    # DefaultStoreBusyTimeoutMs and MaxStoreBusyTimeoutMs in internal/config.
    local default_ms=10000 max_ms=2147483647
    local config="$1" n=0 line table="" have_value=0 ms="" key rest sign digits
    local re_header='^[[:blank:]]*\[[[:blank:]]*([A-Za-z0-9_-]+)[[:blank:]]*\][[:blank:]]*(#.*)?$'
    local re_keyval='^[[:blank:]]*([A-Za-z0-9_-]+)[[:blank:]]*=[[:blank:]]*(.*)$'
    local re_store='^[Ss][Tt][Oo][Rr][Ee]$'
    local re_key='^[Bb][Uu][Ss][Yy]_[Tt][Ii][Mm][Ee][Oo][Uu][Tt]_[Mm][Ss]$'
    # TOML's decimal integer: an optional sign, then 0, or digits with no
    # leading zero and _ only between two of them.
    local re_int='^([+-]?)(0|[1-9](_?[0-9])*)[[:blank:]]*(#.*)?$'

    if [[ -f "$config" && -r "$config" ]]; then
        while IFS= read -r line || [[ -n "$line" ]]; do
            n=$((n + 1))
            if [[ "$n" -eq 1 ]]; then
                line="${line#$'\xef\xbb\xbf'}"
            fi
            line="${line%$'\r'}"
            if [[ "$line" =~ $re_header ]]; then
                table="${BASH_REMATCH[1]}"
                continue
            fi
            if [[ ! "$table" =~ $re_store || ! "$line" =~ $re_keyval ]]; then
                continue
            fi
            key="${BASH_REMATCH[1]}"
            rest="${BASH_REMATCH[2]}"
            if [[ ! "$key" =~ $re_key ]]; then
                continue
            fi
            if [[ "$key" != busy_timeout_ms ]]; then
                ad_busy_timeout_refuse "$config" "$n" "$line" \
                    "agent-director reads this key as busy_timeout_ms: its TOML decoder" \
                    "matches names regardless of letter case. Write it as busy_timeout_ms."
            fi
            if [[ "$have_value" -eq 1 ]]; then
                ad_busy_timeout_refuse "$config" "$n" "$line" \
                    "This sets busy_timeout_ms a second time. Keep one."
            fi
            if [[ ! "$rest" =~ $re_int ]]; then
                ad_busy_timeout_refuse "$config" "$n" "$line" \
                    "busy_timeout_ms's value is not a whole number in decimal digits, such as" \
                    "${default_ms}, optionally followed by a # comment. Write it in that form," \
                    "without quotes, a decimal point or a 0x, 0o or 0b prefix."
            fi
            sign="${BASH_REMATCH[1]}"
            digits="${BASH_REMATCH[2]//_/}"
            have_value=1
            # 0 gives the default, and so does a value agent-director refuses
            # (negative, or above max_ms), which the binary reports itself.
            # The length check keeps the compare within bash's integers.
            if [[ "$digits" == 0 || "$sign" == - || ${#digits} -gt ${#max_ms} ]] || ((digits > max_ms)); then
                ms=""
            else
                ms="$digits"
            fi
        done <"$config"
    fi
    printf '%s\n' "${ms:-$default_ms}"
)

# <<< ad_store_busy_timeout_ms (b.c7f) <<<

if ! store_busy_timeout_ms="$(ad_store_busy_timeout_ms "$PREFLIGHT_CONFIG")"; then
    ad_exit_5 ErrConfigMalformed
fi

# --------------------------------------------------------------------
# The agent-rename gate (SR-11.2)
#
# The migration of config.toml and the templates (the block above, and the
# pre-flight checks and the apply step below) runs only on an install that
# crosses the rename: a store exists at the path pre-flight resolved
# ($state_db: the old directory's while SR-11.7's move is due, the new one's
# otherwise) and its user_version is below AGENT_RENAME_HOP_VERSION. With no
# store, or a store at or above that version, config.toml's question-tool
# key and every template are neither read nor changed, so a template made
# after the upgrade survives every later install byte for byte.
#
# The version is read from the database file's header when that holds the
# committed value and reading it touches nothing (ad_header_user_version, just
# above the gate: a SQLite file with no -wal frames and no hot -journal).
# Otherwise it is read with the version step's read (ad_user_version, which
# waits the configured busy timeout), as sqlite3 -readonly: required while
# the move is due (SR-11.7), and kept always, because a read that is not
# read-only recovers a store's -wal and removes it, a change on disk that a
# pre-flight refusal below must not leave. A store whose version cannot be
# read stops the install here, with exit 5 and ErrVersionUnreadable, before
# anything changes.
# --------------------------------------------------------------------

# ad_user_version <db> [readonly] — print the DB's user_version, read through
# the WAL, and exit with sqlite3's status. With "readonly" the read is
# sqlite3 -readonly (the agent-rename gate's: the store is opened for reading
# only, as SR-11.7 requires while the move is due). sqlite3's stderr
# is printed with its stdout, so the caller holds the whole read in one
# variable and its status in another, with no file:
#
#     rc=0; out="$(ad_user_version "$db")" || rc=$?
#
# A nonzero status means the read failed, and the output is sqlite3's own
# reason, for the report to show (b.n5a). Status 0 with output that is no
# version means the read printed something else (b.hk7). Use the output as
# a version only when ad_got_version holds. Call it only on a DB that
# exists: a failed read never means "no DB".
#
# The read waits for a lock up to $store_busy_timeout_ms, the busy timeout
# every agent-director connection uses ([store] busy_timeout_ms, read in
# pre-flight, b.c7f; internal/store/store.go openDB). A plain sqlite3 waits
# 0 ms, so any agent-director process briefly holding state.db's locks
# (opening, exiting, checkpointing or recovering the WAL) would fail the
# read at once and the install would misread the store (b.ady).
#
# -init /dev/null: the sqlite3 shell otherwise runs the operator's
# ~/.sqliterc first, and a `.headers on` or `.mode` there changes the
# output (b.hk7). -batch: with a terminal on stdin, -init makes sqlite3
# print "-- Loading resources from /dev/null" to stderr, which would join
# the read's output and make it no version.
ad_user_version() {
    if [[ "${2:-}" == readonly ]]; then
        sqlite3 -readonly -batch -init /dev/null -cmd ".timeout ${store_busy_timeout_ms}" "$1" "PRAGMA user_version;" 2>&1
    else
        sqlite3 -batch -init /dev/null -cmd ".timeout ${store_busy_timeout_ms}" "$1" "PRAGMA user_version;" 2>&1
    fi
}

# ad_is_version <value> — true when <value> is a user_version as sqlite3
# prints one: a whole number, 0 or more, with no leading zero. Nothing else
# may reach a printf %d or a version compare (b.hk7). A leading zero would:
# printf %d reads "010" as octal 8 and fails on "08", and the step-5 string
# compare holds "04" and "4" different.
ad_is_version() {
    [[ "$1" =~ ^(0|[1-9][0-9]*)$ ]]
}

# ad_got_version <status> <output> — true when a user_version read
# (ad_user_version) exited 0 and printed a version (ad_is_version). Only
# then may <output> be used as one.
ad_got_version() {
    [[ "$1" -eq 0 ]] && ad_is_version "$2"
}

# ad_show_unreadable_version <output> — print on stderr the <unreadable>
# line of a user_version read that gave no version, with <output>, all the
# read printed (sqlite3's own error when it failed), indented under it.
ad_show_unreadable_version() {
    echo "  actual   user_version: <unreadable>" >&2
    if [[ -n "$1" ]]; then
        printf '%s\n' "$1" | sed 's/^/    /' >&2
    fi
}

# ad_fail_unreadable_version <status> <output> <what the install could not
# do> [<line>...] — finish a user_version read's failure report and exit 5
# (ErrVersionUnreadable).
# <status> and <output> are the read's (ad_user_version). It failed when
# <status> is nonzero or it printed nothing, and otherwise printed <output>,
# which is not a version (ad_is_version). The caller has already printed
# the headline. This prints the <unreadable> line, <output> indented under
# it (on a failed read, sqlite3's own error), the cause, any further
# <line>s, and the advice.
#
# The script cannot tell why a read failed (a lock held past the busy
# timeout, a broken sqlite3, permissions, a corrupt file), so it shows
# sqlite3's error and says that a re-run reads again. A lock is a
# condition time may resolve, so this never sends the operator to a human
# (b.ady, b.n5a). Output that is not a version is no lock or timing
# failure, and ~/.sqliterc cannot cause it (ad_user_version): the sqlite3
# on PATH printed it for this state.db, so a re-run gets the same output
# until one of the two changes (b.hk7).
ad_fail_unreadable_version() {
    local status="$1" output="$2" could_not="$3" failed=0 line
    shift 3
    if [[ "$status" -ne 0 || -z "$output" ]]; then
        failed=1
    fi
    ad_show_unreadable_version "$output"
    echo "  Reading ${state_db_name}'s user_version (sqlite3 PRAGMA user_version)" >&2
    if [[ "$failed" -eq 1 ]]; then
        echo "  failed, so the install could not ${could_not}." >&2
    else
        echo "  printed the output above, not a whole number (0 or more), so" >&2
        echo "  the install could not ${could_not}." >&2
    fi
    for line in "$@"; do
        echo "  $line" >&2
    done
    if [[ "$failed" -eq 1 ]]; then
        echo "  Re-running this install retries the read." >&2
    else
        echo "  sqlite3 on PATH: $(command -v sqlite3)" >&2
        echo "  A re-run gets the same output unless that sqlite3 or ${state_db_name} changes." >&2
    fi
    ad_exit_5 ErrVersionUnreadable
}

# ad_header_user_version <db> — print <db>'s user_version read straight from
# the database file's header (bytes 60-63, big-endian) and return 0, when
# that is the committed value and reading it touches nothing: the file starts
# with SQLite's magic string, no -wal holds frames (none, or an empty one) and
# no hot -journal waits, and the value is not negative. Otherwise print
# nothing and return 1: the caller then reads through sqlite3. Opening a
# WAL-mode store with sqlite3, even -readonly, leaves an empty -wal and a
# -shm behind it when none was there, and a pre-flight refusal must leave the
# tree as it found it.
ad_header_user_version() {
    local db="$1" bytes magic
    [[ -f "$db" && -r "$db" ]] || return 1
    [[ ! -s "${db}-wal" && ! -s "${db}-journal" ]] || return 1
    bytes=($(od -An -v -tx1 -N64 "$db" 2>/dev/null)) || return 1
    [[ "${#bytes[@]}" -eq 64 ]] || return 1
    magic="${bytes[0]}${bytes[1]}${bytes[2]}${bytes[3]}${bytes[4]}${bytes[5]}${bytes[6]}${bytes[7]}${bytes[8]}${bytes[9]}${bytes[10]}${bytes[11]}${bytes[12]}${bytes[13]}${bytes[14]}${bytes[15]}"
    [[ "$magic" == 53514c69746520666f726d6174203300 ]] || return 1
    (( 16#${bytes[60]} < 128 )) || return 1
    printf '%d\n' "$((16#${bytes[60]}${bytes[61]}${bytes[62]}${bytes[63]}))"
}

# ad_agent_rename_gate [defer] — set AGENT_RENAME_DUE to 1 when a store
# exists at $state_db and its user_version is below AGENT_RENAME_HOP_VERSION,
# else 0. A store whose version cannot be read stops the install (exit 5,
# ErrVersionUnreadable); with "defer" it sets GATE_DEFERRED=1 and returns
# instead (see below).
GATE_DEFERRED=0
AGENT_RENAME_DUE=0
ad_agent_rename_gate() {
    local gate_rc=0 gate_version
    GATE_DEFERRED=0
    AGENT_RENAME_DUE=0
    if [[ ! -e "$state_db" ]]; then
        return 0
    fi
    if ! gate_version="$(ad_header_user_version "$state_db")"; then
        gate_version="$(ad_user_version "$state_db" readonly)" || gate_rc=$?
    fi
    if ! ad_got_version "$gate_rc" "$gate_version"; then
        if [[ "${1:-}" == defer ]]; then
            GATE_DEFERRED=1
            return 0
        fi
        echo "install.sh: reading ${state_db_name}'s schema version FAILED" >&2
        echo "  state.db: $state_db" >&2
        ad_fail_unreadable_version "$gate_rc" "$gate_version" \
            "tell whether config.toml and the templates need the agent-rename migration" \
            "Nothing was installed or changed."
    fi
    if [[ "$gate_version" -lt "$AGENT_RENAME_HOP_VERSION" ]]; then
        AGENT_RENAME_DUE=1
    fi
}

# While the move is due (SR-11.7), a store whose version cannot be read is
# left to the move's live-agents check below, which reads the same store and
# refuses first (ErrAgentsLive, "the live rows could not be read"): the
# gate then defers, and runs again once that check has passed.
if [[ "$MOVE_DUE" -eq 1 ]]; then
    ad_agent_rename_gate defer
else
    ad_agent_rename_gate
fi

# --------------------------------------------------------------------
# config.toml merge pre-check (b.whe)
#
# With hooks on, install.sh sets inject_help_hook = true under
# config.toml's [defaults] header, adding that header at the end of the
# file when there is none (the merge below, after the store steps). A
# file that sets defaults as a key before any header (defaults = { ... },
# an inline table, or any other value) defines that table already, and
# TOML refuses a table defined twice, so the added header would leave a
# file agent-director refuses (ErrConfigMalformed) after an install that
# exits 0. With hooks on, such a file is refused here instead, before
# anything on disk changes. ad_store_db_path has already refused every
# other way to set defaults before any header (defaults.x = ..., a
# quoted "defaults"), as it refuses every line that is not blank, a
# comment, a [name] header or a bare name = value line, so a bare key
# is the only form left. It is matched regardless of letter case, as
# agent-director's TOML decoder matches names. With --no-hooks the file
# is never merged and agent-director loads it, so it is not refused.
# --------------------------------------------------------------------

# ad_config_merge_check <config> — return 0 when the hooks-on config
# merge can set inject_help_hook in <config> (or <config> is missing).
# When <config> sets defaults as a key before any header, print why on
# stderr, naming the line, and return 1. Call it only after
# ad_store_db_path accepted <config>. Its body is a subshell under the C
# locale, as that reader's is.
ad_config_merge_check() (
    LC_ALL=C
    local config="$1" n=0 line key
    local re_header='^[[:blank:]]*\['
    local re_defaults='^[[:blank:]]*([Dd][Ee][Ff][Aa][Uu][Ll][Tt][Ss])[[:blank:]]*='
    if [[ ! -f "$config" ]]; then
        return 0
    fi
    while IFS= read -r line || [[ -n "$line" ]]; do
        n=$((n + 1))
        if [[ "$n" -eq 1 ]]; then
            line="${line#$'\xef\xbb\xbf'}"
        fi
        line="${line%$'\r'}"
        if [[ "$line" =~ $re_header ]]; then
            return 0
        fi
        if [[ ! "$line" =~ $re_defaults ]]; then
            continue
        fi
        key="${BASH_REMATCH[1]}"
        echo "install.sh: cannot merge inject_help_hook = true into config.toml's [defaults] table; refusing to install." >&2
        echo "  config  : $config" >&2
        printf '  line %s  : %s\n' "$n" "$line" >&2
        if [[ "$key" != defaults ]]; then
            echo "  agent-director reads $key as defaults: its TOML decoder matches names" >&2
            echo "  regardless of letter case." >&2
        fi
        echo "  This sets defaults as a key (an inline table, say) rather than under a" >&2
        echo "  [defaults] header. With hooks on, install.sh sets inject_help_hook = true" >&2
        echo "  under a [defaults] header and edits no table set as a key: the header it" >&2
        echo "  would add can leave a file agent-director refuses. Remove this line, and" >&2
        echo "  set each key it sets under the file's [defaults] header instead, adding" >&2
        echo "  that header at the end of the file if the file has none." >&2
        echo "  Add no header in this line's place: the lines below it, up to the next" >&2
        echo "  header, would fall under that header too." >&2
        echo "  Nothing was installed or changed. Re-run this install after the change." >&2
        exit 1
    done <"$config"
)

if [[ "$NO_HOOKS" -eq 0 ]] && ! ad_config_merge_check "$PREFLIGHT_CONFIG"; then
    ad_exit_5 ErrConfigMalformed
fi

# --------------------------------------------------------------------
# The agent-rename migration's pre-flight checks (SR-11.2)
#
# With the gate open, the migration block runs over config.toml and every
# templates/*.toml in the directory pre-flight resolves (the old one while
# the move is due), with or without --no-hooks, before anything on disk
# changes. A refusal stops the install with exit 5 and its file's err_name:
# ErrConfigMalformed for config.toml, ErrTemplateMalformed for a template.
# What will change is kept for the link check below and the apply step.
# --------------------------------------------------------------------

# ad_migrate_preflight_file <config|template> <file> — run the migration
# block over <file>, discarding its output. Return 0 when the migration
# will change <file>, 3 when it will not (or <file> is no file). Exit 5
# when the block refuses it, or <file> cannot be read.
ad_migrate_preflight_file() {
    local kind="$1" file="$2" rc=0
    if [[ ! -f "$file" ]]; then
        return 3
    fi
    if [[ ! -r "$file" ]]; then
        echo "install.sh: cannot read $file, which the agent-rename migration reads; refusing to install." >&2
        echo "  Make it readable by you, then re-run this install." >&2
        echo "  Nothing was installed or changed. Re-run this install after the change." >&2
        ad_exit_5 "$(ad_agent_rename_err_name "$kind")"
    fi
    ad_agent_rename_migrate "$kind" "$file" >/dev/null || rc=$?
    case "$rc" in
        0 | 3) return "$rc" ;;
        2) ad_exit_5 "$(ad_agent_rename_err_name "$kind")" ;;
        *)
            echo "install.sh: reading $file for the agent-rename migration failed (status $rc); refusing to install." >&2
            echo "  Nothing was installed or changed. Re-run this install after the change." >&2
            ad_exit_5 "$(ad_agent_rename_err_name "$kind")"
            ;;
    esac
}

# ad_migrate_link_refuse <file> — report a symlinked <file> the migration
# will change that ad_link_write_check refused, from link_target and
# link_why, on stderr. The caller exits 5. It never offers --no-hooks: the
# migration runs with it too.
ad_migrate_link_refuse() {
    echo "install.sh: cannot migrate $1 through its symlink; refusing to install." >&2
    echo "  link    : $1" >&2
    if [[ -n "$link_target" ]]; then
        echo "  target  : $link_target" >&2
    fi
    echo "  $link_why" >&2
    echo "  The agent-rename migration rewrites this file (config.toml's" >&2
    echo "  disable_askuserquestion key, a template's claude_args and agent), with or" >&2
    echo "  without --no-hooks, writing into the file a symlink resolves to and keeping" >&2
    echo "  the link. Fix the link so it reaches a file in a directory you can write in." >&2
    echo "  Or make the rename yourself where the file comes from (your dotfiles or" >&2
    echo "  home-manager configuration, say): in config.toml, disable_askuserquestion" >&2
    echo "  becomes disable_ask_user; in a template, claude_args becomes agent_args and" >&2
    echo '  a line agent = "claude" goes among the lines before its first [table] header' >&2
    echo "  when it has no agent." >&2
    echo '  See "`install.sh` refuses config.toml or a template" in docs/operator-actions.md.' >&2
    echo "  Nothing was installed or changed. Re-run this install after the change." >&2
}

MIGRATE_CONFIG_CHANGES=0
MIGRATE_TEMPLATES=()
# ad_migrate_preflight_all — run the migration block over config.toml and
# every template (when the gate is open), filling MIGRATE_CONFIG_CHANGES and
# MIGRATE_TEMPLATES.
ad_migrate_preflight_all() {
    local mig_rc mig_f
    if [[ "$AGENT_RENAME_DUE" -ne 1 ]]; then
        return 0
    fi
    mig_rc=0
    ad_migrate_preflight_file config "$PREFLIGHT_CONFIG" || mig_rc=$?
    if [[ "$mig_rc" -eq 0 ]]; then
        MIGRATE_CONFIG_CHANGES=1
    fi
    for mig_f in "${PREFLIGHT_ROOT}/templates"/*.toml; do
        mig_rc=0
        ad_migrate_preflight_file template "$mig_f" || mig_rc=$?
        if [[ "$mig_rc" -eq 0 ]]; then
            MIGRATE_TEMPLATES+=("$mig_f")
        fi
    done
}
ad_migrate_preflight_all

# --------------------------------------------------------------------
# Symlinked settings.json and config.toml pre-check (b.nw5)
#
# With hooks on, the merges below write through a symlinked
# ~/.claude/settings.json or ~/.config/agent-director/config.toml to the file
# its chain of links resolves to, keeping the link
# (ad_replace_keeping_mode). The write puts a .new beside that file and
# moves it over the file, so it fails, late (after the binaries are
# installed, the store migrated and a .bak written), when the links
# loop, when the resolved file's directory is missing (a dotfiles
# repository not cloned yet, say), or when that directory cannot be
# written in (home-manager links into a read-only /nix/store, say). With
# hooks on, such a link is refused here instead, before anything on disk
# changes: settings.json with exit 4 and config.toml with exit 5
# (ErrConfigMalformed), as each file's other refusals. A plain file, and
# a link that resolves to a file, existing or not, in a directory that
# can be written in, pass. With --no-hooks neither file is written, so
# neither is checked. uninstall.sh runs the same check before its edits.
# --------------------------------------------------------------------

# (ad_resolve_link is defined above, in the section on AD's directory move,
# which needs it first.)

# ad_link_write_check <file> — return 0 when <file> is not a symlink, or
# is one ad_replace_keeping_mode can write through: its links end, and
# the directory of the file they resolve to exists and can be written in
# (write and search permission, on a file system mounted read-write).
# Otherwise set link_target to the file the links resolve to (empty when
# they loop) and link_why to one sentence saying why the link cannot be
# written through, and return 1.
ad_link_write_check() {
    local dir
    link_target="" link_why=""
    if [[ ! -L "$1" ]]; then
        return 0
    fi
    if ! link_target=$(ad_resolve_link "$1" 2>/dev/null); then
        link_target=""
        link_why="The links loop: following them never reaches a file (more than 40 links)."
        return 1
    fi
    dir=$(dirname "$link_target")
    if [[ ! -d "$dir" ]]; then
        link_why="There is no directory $dir to hold the target (a dotfiles repository not cloned yet, say)."
        return 1
    fi
    if [[ ! -w "$dir" || ! -x "$dir" ]]; then
        link_why="The target's directory, $dir, cannot be written in (a read-only file system, say, such as home-manager's /nix/store)."
        return 1
    fi
}

# ad_link_refuse <file> — report a symlinked <file> that
# ad_link_write_check refused, from link_target and link_why, with the
# ways out, on stderr. The caller exits.
ad_link_refuse() {
    echo "install.sh: cannot merge into $1 through its symlink; refusing to install." >&2
    echo "  link    : $1" >&2
    if [[ -n "$link_target" ]]; then
        echo "  target  : $link_target" >&2
    fi
    echo "  $link_why" >&2
    echo "  With hooks on, install.sh writes its merges into the file a symlinked" >&2
    echo "  settings.json or config.toml resolves to, keeping the link. Fix the link" >&2
    echo "  so it reaches a file in a directory you can write in. Or re-run this" >&2
    echo "  install with --no-hooks, which edits neither file, and add what the" >&2
    echo "  merges add where the files come from (your dotfiles or home-manager" >&2
    echo "  configuration, say): in settings.json, a SessionStart hook and a" >&2
    echo "  SessionEnd hook with matcher \"compact\", each a command hook running" >&2
    echo "  \"${DEFAULT_BIN_DIR}/agent-director help\"; in config.toml," >&2
    echo "  inject_help_hook = true under [defaults]." >&2
    echo "  Nothing was installed or changed. Re-run this install after the change." >&2
}

# settings.json is edited only with Claude Code installed, so it is checked
# only then.
if [[ "$NO_HOOKS" -eq 0 && "$CLAUDE_PRESENT" -eq 1 ]] && ! ad_link_write_check "$DEFAULT_SETTINGS_PATH"; then
    ad_link_refuse "$DEFAULT_SETTINGS_PATH"
    exit 4
fi
# The agent-rename migration's own link check (SR-11.2), with or without
# --no-hooks: it rewrites config.toml (when it holds the old key) and each
# template it will change, so a link to one that cannot be written through
# stops the install here. For config.toml it runs before the merge's check
# below, so the merge's --no-hooks advice is never given for a file the
# migration writes anyway.
ad_migrate_link_checks() {
    local mig_f
    if [[ "$MIGRATE_CONFIG_CHANGES" -eq 1 ]] && ! ad_link_write_check "$PREFLIGHT_CONFIG"; then
        ad_migrate_link_refuse "$PREFLIGHT_CONFIG"
        ad_exit_5 ErrConfigMalformed
    fi
    for mig_f in ${MIGRATE_TEMPLATES[@]+"${MIGRATE_TEMPLATES[@]}"}; do
        if ! ad_link_write_check "$mig_f"; then
            ad_migrate_link_refuse "$mig_f"
            ad_exit_5 ErrTemplateMalformed
        fi
    done
}
ad_migrate_link_checks
if [[ "$NO_HOOKS" -eq 0 ]] && ! ad_link_write_check "$PREFLIGHT_CONFIG"; then
    ad_link_refuse "$PREFLIGHT_CONFIG"
    ad_exit_5 ErrConfigMalformed
fi
# The last pre-flight config read and check is done: neither agent found
# stops the install now (exit 2), after every config-form refusal (exit 5).
ad_refuse_no_agent

# --------------------------------------------------------------------
# Links and PATH entries into the old bin directory; nothing running
# (SR-11.7)
#
# With the move due, pre-flight finds what points into the old bin
# directory: the links at ~/.local/bin, at --symlink-dir and where `command
# -v agent-director` leads, and the $PATH entries naming that directory
# (OLD_BIN_LINKS, OLD_BIN_PATH_ENTRIES). The installer re-points the link it
# manages after the move and prints the rest with their fix; the macOS
# process check below names those links too.
#
# It then stops (exit 5, ErrAgentsLive), changing nothing, while the old
# store holds a row in a live state (pending, waiting, working,
# check_permission, ask_user) or a process runs the old bin/agent-director.
# The rows are read with sqlite3 -readonly, waiting the store's busy timeout;
# a read that fails stops the install the same way, saying so. A process
# counts on Linux when its /proc/<pid>/exe, any " (deleted)" suffix dropped,
# is the old binary's resolved path; on macOS when its executable path
# (ps -axo pid=,comm=, or lsof's txt entry) is the old binary or a link into
# the old bin directory found here. Where neither way is available the
# installer says the process check did not run. The same check runs again as
# the move's first step.
#
# Test-only switch INSTALL_SH_TEST_PROC_CHECK (not in --help): "macos" runs
# the ps/lsof branch on any host (so a test puts stand-ins for ps and lsof on
# PATH), "none" runs neither branch.
# --------------------------------------------------------------------

OLD_BIN_LINKS=()
OLD_BIN_PATH_ENTRIES=()

# ad_in_old_bin <path> — true when <path> lies inside the old bin directory,
# lexically or by its physical place.
ad_in_old_bin() {
    local c ph
    c="$(ad_clean_path "$1")"
    if [[ "$c" == "${OLD_ROOT_CLEAN}/bin" || "$c" == "${OLD_ROOT_CLEAN}/bin"/* ]]; then
        return 0
    fi
    if ph="$(ad_phys_place "$1" 2>/dev/null)" \
        && [[ "$ph" == "${OLD_PHYS}/bin" || "$ph" == "${OLD_PHYS}/bin"/* ]]; then
        return 0
    fi
    return 1
}

# ad_discover_old_bin_refs — fill OLD_BIN_LINKS and OLD_BIN_PATH_ENTRIES.
ad_discover_old_bin_refs() {
    local cands c t e seen
    local -a parts
    OLD_BIN_LINKS=()
    OLD_BIN_PATH_ENTRIES=()
    cands=("${HOME}/.local/bin/agent-director")
    if [[ -n "$SYMLINK_DIR" ]]; then
        cands+=("${SYMLINK_DIR}/agent-director")
    fi
    t="$(command -v agent-director 2>/dev/null || true)"
    if [[ "$t" == /* ]]; then
        cands+=("$t")
    fi
    for c in "${cands[@]}"; do
        if [[ ! -L "$c" ]]; then
            continue
        fi
        t="$(ad_resolve_link "$c" 2>/dev/null)" || continue
        if ! ad_in_old_bin "$t"; then
            continue
        fi
        seen=0
        for e in ${OLD_BIN_LINKS[@]+"${OLD_BIN_LINKS[@]}"}; do
            if [[ "$e" == "$c" ]]; then seen=1; fi
        done
        if [[ "$seen" -eq 0 ]]; then
            OLD_BIN_LINKS+=("$c")
        fi
    done
    IFS=: read -r -a parts <<<"$PATH"
    for e in ${parts[@]+"${parts[@]}"}; do
        if [[ -z "$e" || "$e" != /* ]]; then
            continue
        fi
        if ad_in_old_bin "$e" && [[ "$(ad_clean_path "$e")" == "${OLD_ROOT_CLEAN}/bin" || "$(ad_phys_place "$e" 2>/dev/null)" == "${OLD_PHYS}/bin" ]]; then
            OLD_BIN_PATH_ENTRIES+=("$e")
        fi
    done
    return 0
}

# ad_old_binary_names — print each name the old binary's own process can
# show on Linux: its place under the old directory's physical path, and
# where its links lead.
ad_old_binary_names() {
    local b="${OLD_PHYS}/bin/agent-director" t r
    printf '%s\n' "$b"
    if t="$(ad_resolve_link "$b" 2>/dev/null)" && r="$(ad_phys_place "$t" 2>/dev/null)"; then
        printf '%s\n' "$r"
    fi
}

# ad_scan_old_processes — fill LIVE_PROCS with "<pid> <path>" for each
# process running the old binary. Return 0 when a process check ran, 1 when
# none could (no /proc and no usable ps or lsof).
LIVE_PROCS=()
ad_scan_old_processes() {
    local mode="${INSTALL_SH_TEST_PROC_CHECK:-}" n d pid exe line comm ps_out lsof_out
    local -a names
    LIVE_PROCS=()
    names=()
    while IFS= read -r n; do
        names+=("$n")
    done < <(ad_old_binary_names)
    if [[ "$mode" == none ]]; then
        return 1
    fi
    if [[ "$mode" != macos && -d /proc/self ]]; then
        for d in /proc/[0-9]*; do
            pid="${d#/proc/}"
            exe="$(readlink "$d/exe" 2>/dev/null)" || continue
            exe="${exe% (deleted)}"
            for n in "${names[@]}"; do
                if [[ "$exe" == "$n" ]]; then
                    LIVE_PROCS+=("$pid $exe")
                    break
                fi
            done
        done
        return 0
    fi
    # macOS: the old binary, or a link into the old bin directory that the
    # discovery above found, named as the process's executable path.
    for n in ${OLD_BIN_LINKS[@]+"${OLD_BIN_LINKS[@]}"}; do
        names+=("$n")
    done
    names+=("${OLD_BIN_DIR}/agent-director")
    if command -v ps >/dev/null 2>&1 && ps_out="$(ps -axo pid=,comm= 2>/dev/null)"; then
        while IFS= read -r line; do
            line="${line#"${line%%[![:space:]]*}"}"
            pid="${line%%[[:space:]]*}"
            comm="${line#"$pid"}"
            comm="${comm#"${comm%%[![:space:]]*}"}"
            if [[ ! "$pid" =~ ^[0-9]+$ ]]; then
                continue
            fi
            for n in "${names[@]}"; do
                if [[ "$comm" == "$n" ]]; then
                    LIVE_PROCS+=("$pid $comm")
                    break
                fi
            done
        done <<<"$ps_out"
        return 0
    fi
    if command -v lsof >/dev/null 2>&1 && lsof_out="$(lsof -d txt -Fpn 2>/dev/null)"; then
        pid=""
        while IFS= read -r line; do
            case "$line" in
                p*) pid="${line#p}" ;;
                n*)
                    comm="${line#n}"
                    for n in "${names[@]}"; do
                        if [[ "$comm" == "$n" && "$pid" =~ ^[0-9]+$ ]]; then
                            LIVE_PROCS+=("$pid $comm")
                            break
                        fi
                    done
                    ;;
            esac
        done <<<"$lsof_out"
        return 0
    fi
    return 1
}

# ad_read_live_rows <db> — fill LIVE_ROWS with one "<state> <id>" line per
# row of <db>'s spawns table in a live state, and return 0; return 1 with
# the reason in LIVE_ROWS_ERR when the rows could not be read (busy past the
# store's busy timeout, or unreadable). No store file, or no spawns table,
# holds no rows. The id column is instance_id (the current schema) or
# claude_instance_id (the previous release's), found first.
LIVE_ROWS=""
LIVE_ROWS_ERR=""
ad_read_live_rows() {
    local db="$1" cols idcol rc=0
    LIVE_ROWS=""
    LIVE_ROWS_ERR=""
    if [[ ! -f "$db" ]]; then
        return 0
    fi
    cols="$(sqlite3 -readonly -batch -init /dev/null -cmd ".timeout ${store_busy_timeout_ms}" "$db" "SELECT name FROM pragma_table_info('spawns');" 2>&1)" || rc=$?
    if [[ "$rc" -ne 0 ]]; then
        LIVE_ROWS_ERR="$cols"
        return 1
    fi
    if [[ -z "$cols" ]]; then
        return 0
    fi
    case $'\n'"$cols"$'\n' in
        *$'\ninstance_id\n'*) idcol=instance_id ;;
        *$'\nclaude_instance_id\n'*) idcol=claude_instance_id ;;
        *) idcol=rowid ;;
    esac
    LIVE_ROWS="$(sqlite3 -readonly -batch -init /dev/null -separator ' ' -cmd ".timeout ${store_busy_timeout_ms}" "$db" "SELECT state, ${idcol} FROM spawns WHERE state IN ('pending','waiting','working','check_permission','ask_user') ORDER BY ${idcol};" 2>&1)" || rc=$?
    if [[ "$rc" -ne 0 ]]; then
        LIVE_ROWS_ERR="$LIVE_ROWS"
        LIVE_ROWS=""
        return 1
    fi
    return 0
}

# ad_agents_live_check — exit 5 (ErrAgentsLive) when the old store holds a
# live row or a process runs the old binary, or when the rows could not be
# read; say so on stdout, once, when the process check could not run (it runs
# again as the move's first step). Nothing is changed. Run in pre-flight and
# again as the move's first step.
PROC_CHECK_NOTED=0
ad_agents_live_check() {
    local rows_failed=0 proc_ran=0 found=0 l
    if ! ad_read_live_rows "$state_db"; then
        rows_failed=1
    fi
    if ad_scan_old_processes; then
        proc_ran=1
    fi
    if [[ "$rows_failed" -eq 0 && -z "$LIVE_ROWS" && "${#LIVE_PROCS[@]}" -eq 0 ]]; then
        if [[ "$proc_ran" -eq 0 && "$PROC_CHECK_NOTED" -eq 0 ]]; then
            PROC_CHECK_NOTED=1
            echo "  process check: did not run (this host has no /proc and no usable ps or lsof), so no agent-director process was looked for"
        fi
        return 0
    fi
    echo "install.sh: AD's old directory is in use; refusing to upgrade while an agent or agent-director process runs." >&2
    echo "  old binary: ${OLD_BIN_DIR}/agent-director" >&2
    echo "  old store : $state_db" >&2
    if [[ "$rows_failed" -eq 1 ]]; then
        echo "  The live rows could not be read (the store was busy past its busy timeout of ${store_busy_timeout_ms} ms, or is unreadable), so the install cannot tell whether an agent runs. A re-run reads them again. sqlite3 said:" >&2
        printf '%s\n' "$LIVE_ROWS_ERR" | sed 's/^/    /' >&2
    fi
    if [[ -n "$LIVE_ROWS" ]]; then
        echo "  Rows in a live state (state, instance id):" >&2
        printf '%s\n' "$LIVE_ROWS" | sed 's/^/    /' >&2
        found=1
    fi
    if [[ "${#LIVE_PROCS[@]}" -gt 0 ]]; then
        echo "  Processes running the old binary (pid, executable):" >&2
        for l in "${LIVE_PROCS[@]}"; do
            echo "    $l" >&2
        done
        echo "  A Claude Code session's own MCP server (agent-director serve --stdio) is one such process: it runs the old binary until that session ends." >&2
        found=1
    fi
    if [[ "$proc_ran" -eq 0 ]]; then
        echo "  The process check did not run (this host has no /proc and no usable ps or lsof)." >&2
    fi
    if [[ "$found" -eq 1 ]]; then
        echo "  Stop whatever starts agents (a scheduler, a loop, a caller), then every agent, every serve and every caller, and re-run this install from a shell AD did not start." >&2
        echo "  A row whose agent is already gone is cleared by the old binary's find-missing, which a still-running agent never matches:" >&2
        printf '    %q find-missing\n' "${OLD_BIN_DIR}/agent-director" >&2
    fi
    echo "  Nothing was installed or changed. Re-run this install after the change." >&2
    echo "  (operator-actions.md, \"\`install.sh\` refuses with \`ErrAgentsLive\`\")" >&2
    ad_exit_5 ErrAgentsLive
}

if [[ "$MOVE_DUE" -eq 1 ]]; then
    ad_discover_old_bin_refs
    ad_agents_live_check
fi

# A gate that deferred to the check above (the store's version was unreadable
# while the move is due) runs again now that the check read the rows: a
# version that still cannot be read stops the install, as above, and one that
# can be read sends the migration's checks through late.
if [[ "$GATE_DEFERRED" -eq 1 ]]; then
    ad_agent_rename_gate
    ad_migrate_preflight_all
    ad_migrate_link_checks
fi

echo "install.sh: pre-flight OK"
ad_print_agent_summary
echo "  tmux    : $(tmux -V 2>/dev/null || echo '<unknown>')"
if [[ "$state_db_name" != state.db ]]; then
    echo "  store   : $state_db ([store] db_path in ${PREFLIGHT_CONFIG})"
fi

# --------------------------------------------------------------------
# Source tree, and the build advice that names it
# --------------------------------------------------------------------

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# skills/install-agent-director sits two levels under the repo root;
# bin/ is at the root. The root is resolved here, physically as the
# kernel resolves ../.., so every message names <root>/bin/... with no
# ../.. (b.j6w); a failed cd keeps the raw path. Without --from-release,
# install.sh prefers the binaries in its bin/ (below), and the
# source-tree version check holds a binary to this same root's HEAD
# (b.1rs).
source_root="$(cd -P "${SCRIPT_DIR}/../.." 2>/dev/null && pwd -P)" \
    || source_root="${SCRIPT_DIR}/../.."

# source_head: source_root's HEAD when source_root is a git checkout of
# agent-director's source; empty otherwise. Read once, here, for the
# build advice below and the source-tree version check.
#
# source_root is that checkout only when its own `.git` entry is a repo:
# a directory in a plain clone, a `gitdir:` file in a linked worktree
# (b.go9), so a worktree nested inside another checkout is held to its
# own HEAD. --git-dir takes that entry as given and never searches
# upward, as git -C would: a `.git` that is not a repo (empty,
# half-copied) fails rev-parse, leaving source_head empty, rather than
# letting git find a repo enclosing source_root (b.1rs); nor is CWD's
# repo used. --git-dir also overrides an exported GIT_DIR. It is
# agent-director's source when it has cmd/agent-director, which `make
# build` builds into its bin/. A failed rev-parse (no git, an unborn
# HEAD) leaves source_head empty whatever it printed.
source_head=""
if [[ -e "$source_root/.git" && -d "$source_root/cmd/agent-director" ]]; then
    source_head=$(git --git-dir="$source_root/.git" rev-parse HEAD 2>/dev/null) \
        || source_head=""
fi

# The refusals that advise a build (b.3qt) name where to run it, so the
# advice works from any directory: the installed skill is often a
# symlink into a clone, run from ~, where a bare `make build` fails.
# They name source_root, with make -C, only when it is a git checkout of
# agent-director's source (source_head set) with a Makefile, where `make
# build` stamps both binaries with the checkout's commit, as the checks
# below require. Otherwise (a curled tarball, an installed copy of the
# skill in some other repo) install.sh knows no checkout, so the advice
# names a placeholder for the operator's own, and the re-run after the
# build is that checkout's install.sh, which takes the binaries from its
# bin/. Paths are quoted with %q, as the sqlite3 commands below are, so
# the line can be pasted into a shell as is: a plain path prints
# unchanged.
build_advice="make -C <path-to-agent-director-checkout> build"
build_rerun="bash <path-to-agent-director-checkout>/skills/install-agent-director/install.sh"
if [[ -n "$source_head" && -f "$source_root/Makefile" ]]; then
    build_advice="make -C $(printf '%q' "$source_root") build"
    build_rerun="bash $(printf '%q' "$0")"
fi

# --------------------------------------------------------------------
# --from-release: resolve tag, download asset for this OS/arch, hand
# the temp path to the rest of the install flow as if --binary had
# been passed.
# --------------------------------------------------------------------

if [[ "$FROM_RELEASE" -eq 1 ]]; then
    case "$(uname -s)" in
        Linux)  rel_os="linux" ;;
        Darwin) rel_os="darwin" ;;
        *)
            echo "install.sh: --from-release: unsupported OS $(uname -s)" >&2
            echo "  the /release skill only publishes linux and darwin builds." >&2
            exit 3 ;;
    esac
    case "$(uname -m)" in
        x86_64|amd64)   rel_arch="amd64" ;;
        arm64|aarch64)  rel_arch="arm64" ;;
        *)
            echo "install.sh: --from-release: unsupported arch $(uname -m)" >&2
            exit 3 ;;
    esac
    asset="agent-director-${rel_os}-${rel_arch}"
    admin_asset="agent-director-admin-${rel_os}-${rel_arch}"

    # Resolve the tag if the operator didn't supply one. Prefer `gh`
    # (carries the operator's auth, avoids the unauthenticated API
    # rate limit); fall back to curl + jq against the public API.
    if [[ -z "$FROM_RELEASE_TAG" ]]; then
        if command -v gh >/dev/null 2>&1; then
            FROM_RELEASE_TAG=$(gh release view --repo "$RELEASE_REPO_SLUG" \
                --json tagName -q .tagName 2>/dev/null || true)
        fi
        if [[ -z "$FROM_RELEASE_TAG" ]]; then
            api_url="https://api.github.com/repos/${RELEASE_REPO_SLUG}/releases/latest"
            FROM_RELEASE_TAG=$(curl -fsSL "$api_url" 2>/dev/null \
                | jq -r '.tag_name // empty' 2>/dev/null || true)
        fi
        if [[ -z "$FROM_RELEASE_TAG" || "$FROM_RELEASE_TAG" == "null" ]]; then
            echo "install.sh: --from-release: no releases published for $RELEASE_REPO_SLUG yet" >&2
            echo "  options:" >&2
            echo "    - build from source: $build_advice && $build_rerun" >&2
            echo "    - point at local binaries: bash $(printf '%q' "$0") --binary <path> --admin-binary <path>" >&2
            exit 3
        fi
    fi
    echo "  release : $RELEASE_REPO_SLUG @ $FROM_RELEASE_TAG ($asset, $admin_asset)"

    tmp_bin="$(mktemp -t agent-director.XXXXXX)"
    tmp_admin="$(mktemp -t agent-director-admin.XXXXXX)"
    # Defer-cleanup the tempfiles on any exit path. The install steps
    # later in the script copy their contents into place, so the
    # tempfiles being cleaned up at script exit is fine.
    trap ad_exit_cleanup EXIT

    # ----------------------------------------------------------------
    # CDN-propagation retry (b.kym).
    #
    # For ~30 minutes after the /release skill finishes, GitHub's release-
    # asset CDN can return 404 (or 403 while the asset is mid-publish)
    # to unauthenticated curl, even though the asset is on the release
    # page in the web UI and `gh release download` (auth path) works
    # fine. Treating the first 404 as fatal aborts every install that
    # lands inside that window.
    #
    # Strategy:
    #   - 5 attempts at 2s/4s/8s/16s/32s backoff
    #   - retry ONLY on HTTP 404/403; every other curl failure (DNS,
    #     network unreachable, TLS, …) fails fast as before
    #   - log each retry visibly — propagation lag is the most likely
    #     cause and an operator should see it happening
    #   - after retries exhaust, fall back to `gh release download` if
    #     `gh` is on PATH (auth path uses different URL surface that
    #     typically propagates faster); if `gh` is unavailable or
    #     fails, emit the improved failure message and exit 3.
    #   - except for the agent-director-admin asset of a release before
    #     0.11.0 (b.vqr): such a release has none and never will, so
    #     waiting cannot help. Its 404/403 is refused at once, with no
    #     retry or gh fallback and no wait-and-retry advice (exit 3).
    #
    # INSTALL_SH_TEST_CURL_OVERRIDE: test-only escape hatch. When set,
    # the named executable replaces real `curl` for this download
    # wrapper. Exists because exercising the retry path against the
    # live CDN is non-deterministic; the override lets the shell test
    # suite inject a fake curl that returns 404 N times then 200. Not
    # a public flag — undocumented in --help on purpose.
    # ----------------------------------------------------------------

    _ad_curl_cmd="curl"
    if [[ -n "${INSTALL_SH_TEST_CURL_OVERRIDE:-}" ]]; then
        _ad_curl_cmd="$INSTALL_SH_TEST_CURL_OVERRIDE"
    fi

    # ad_release_predates_admin: succeed when $FROM_RELEASE_TAG names a
    # release before 0.11.0, the first that ships agent-director-admin
    # (b.vqr): its X.Y.Z (after an optional leading "v"; a -prerelease
    # or +build suffix ignored) is below 0.11.0. A tag of any other
    # shape is not judged to predate it.
    ad_release_predates_admin() {
        [[ "$FROM_RELEASE_TAG" =~ ^v?([0-9]+)\.([0-9]+)\.([0-9]+)([-+].*)?$ ]] || return 1
        (( 10#${BASH_REMATCH[1]} == 0 && 10#${BASH_REMATCH[2]} < 11 ))
    }

    # ad_refuse_admin_absent <asset> <url> <HTTP status>: refuse a
    # release before 0.11.0, whose agent-director-admin asset <asset> is
    # missing because the release has none, and exit 3. Re-running can
    # never find it, so the advice is another release, not waiting.
    ad_refuse_admin_absent() {
        echo "install.sh: --from-release: release $FROM_RELEASE_TAG has no agent-director-admin binary; refusing to install." >&2
        echo "  asset   : $1" >&2
        echo "  url     : $2" >&2
        echo "  tag     : $FROM_RELEASE_TAG" >&2
        echo "  repo    : $RELEASE_REPO_SLUG" >&2
        echo "  last HTTP status: $3" >&2
        echo "" >&2
        echo "  Releases before 0.11.0 ship no agent-director-admin binary, so" >&2
        echo "  re-running this command will never find one. Instead:" >&2
        echo "    - install this release or later: bash $0 --from-release <tag of this release or later>" >&2
        echo "    - or install an earlier release with that release's own install.sh, fetched from its tag, rather than this one" >&2
        exit 3
    }

    # ad_download_asset <asset> <dest> <manual-install advice> [<admin>]:
    # download the release asset <asset> to <dest> with the retry and gh
    # fallback above; on failure print the diagnosis and exit 3. <admin>
    # is 1 for the agent-director-admin asset: a 404/403 for it from a
    # release before 0.11.0 goes to ad_refuse_admin_absent at once.
    ad_download_asset() {
        local asset="$1" dest="$2" manual_advice="$3" is_admin="${4:-0}"
        local asset_url="https://github.com/${RELEASE_REPO_SLUG}/releases/download/${FROM_RELEASE_TAG}/${asset}"
        local download_ok=0 attempt=1 max_attempts=5
        local backoff_delays=(2 4 8 16 32)
        local last_http_code="" http_code delay
        while [[ "$attempt" -le "$max_attempts" ]]; do
            # -w '%{http_code}' surfaces the HTTP status even on -f's
            # 22-exit; -o writes the body (or nothing, on 4xx with -f).
            http_code=$("$_ad_curl_cmd" -sSL --retry 0 \
                -w '%{http_code}' -o "$dest" \
                "$asset_url" 2>/dev/null || true)
            last_http_code="$http_code"

            if [[ "$http_code" == "200" ]]; then
                download_ok=1
                break
            fi

            if [[ "$http_code" != "404" && "$http_code" != "403" ]]; then
                # DNS/network/TLS/etc. — fail fast, do not retry.
                break
            fi

            if [[ "$is_admin" -eq 1 ]] && ad_release_predates_admin; then
                # Not propagation lag: the release has no such asset.
                ad_refuse_admin_absent "$asset" "$asset_url" "$http_code"
            fi

            if [[ "$attempt" -lt "$max_attempts" ]]; then
                delay=${backoff_delays[$((attempt-1))]}
                echo "install.sh: --from-release: asset not yet available (HTTP $http_code), retrying in ${delay}s (attempt $attempt/$max_attempts)" >&2
                sleep "$delay"
            fi
            attempt=$((attempt+1))
        done

        if [[ "$download_ok" -ne 1 ]]; then
            # gh-fallback: the API path uses different URL surface that
            # propagates faster after a fresh release. Only attempt if
            # `gh` is on PATH; failure here falls through to the original
            # error message so the operator sees what actually broke.
            if command -v gh >/dev/null 2>&1; then
                echo "install.sh: --from-release: curl path exhausted retries; trying \`gh release download\` fallback" >&2
                if gh release download "$FROM_RELEASE_TAG" \
                        -R "$RELEASE_REPO_SLUG" \
                        -p "$asset" \
                        -O "$dest" \
                        --clobber 2>/dev/null; then
                    download_ok=1
                else
                    echo "install.sh: --from-release: gh release download fallback also failed (gh may not be authenticated for $RELEASE_REPO_SLUG)" >&2
                fi
            fi
        fi

        if [[ "$download_ok" -ne 1 ]]; then
            echo "install.sh: --from-release: failed to download asset after ${max_attempts} attempts" >&2
            echo "  asset   : $asset" >&2
            echo "  url     : $asset_url" >&2
            echo "  tag     : $FROM_RELEASE_TAG" >&2
            echo "  repo    : $RELEASE_REPO_SLUG" >&2
            if [[ -n "$last_http_code" && "$last_http_code" != "000" ]]; then
                echo "  last HTTP status: $last_http_code" >&2
            fi
            if [[ "$last_http_code" == "404" || "$last_http_code" == "403" ]]; then
                echo "" >&2
                echo "  GitHub's release-asset CDN can return 404/403 for ~30 minutes" >&2
                echo "  after a fresh release while the asset propagates. Options:" >&2
                echo "    - wait a few minutes and re-run this command" >&2
                echo "    - install \`gh\` and re-run (gh's auth path propagates faster)" >&2
                echo "    - download the binaries manually from $RELEASE_REPO_SLUG's releases page" >&2
                echo "      and run: $manual_advice" >&2
            else
                echo "" >&2
                echo "  Suggested fallback: download the assets manually and re-run with" >&2
                echo "    $manual_advice" >&2
            fi
            exit 3
        fi
    }

    # ad_verify_sha256 <file> <expected> <flag> <label>: when <expected>
    # is set, check <file>'s sha256 against it, naming the check <label>
    # in the output; exit 3 on a mismatch.
    ad_verify_sha256() {
        local file="$1" expected="$2" flag="$3" label="$4" actual
        [[ -n "$expected" ]] || return 0
        if command -v sha256sum >/dev/null 2>&1; then
            actual=$(sha256sum "$file" | awk '{print $1}')
        elif command -v shasum >/dev/null 2>&1; then
            actual=$(shasum -a 256 "$file" | awk '{print $1}')
        else
            echo "install.sh: $flag: neither sha256sum nor shasum available" >&2
            exit 3
        fi
        if [[ "$actual" != "$expected" ]]; then
            echo "install.sh: --from-release: $label mismatch" >&2
            echo "  expected: $expected" >&2
            echo "  actual  : $actual" >&2
            exit 3
        fi
        printf '  %-8s: verified\n' "$label"
    }

    manual_advice="bash $0 --binary <path-to-downloaded-agent-director> --admin-binary <path-to-downloaded-agent-director-admin>"
    ad_download_asset "$asset" "$tmp_bin" "$manual_advice"
    ad_verify_sha256 "$tmp_bin" "$SHA256_EXPECTED" "--sha256" "sha256"
    ad_download_asset "$admin_asset" "$tmp_admin" "$manual_advice" 1
    ad_verify_sha256 "$tmp_admin" "$ADMIN_SHA256_EXPECTED" "--admin-sha256" "admin sha256"

    chmod +x "$tmp_bin" "$tmp_admin"
    BINARY_SRC="$tmp_bin"
    ADMIN_SRC="$tmp_admin"
fi

# --------------------------------------------------------------------
# Locate source binary
# --------------------------------------------------------------------

# Defaults to 1; cleared to 0 only when BINARY_SRC came from PATH
# (option (c)), since "whatever's on PATH" makes no claim about the
# operator's source tree.
VERSION_CHECK_REQUIRED=1

# Prefer the in-repo builds: bin/ of source_root, resolved above.
candidate="${source_root}/bin/agent-director"
admin_candidate="${source_root}/bin/agent-director-admin"

if [[ -z "$BINARY_SRC" ]]; then
    if [[ -x "$candidate" ]]; then
        BINARY_SRC="$candidate"
    elif command -v agent-director >/dev/null 2>&1; then
        BINARY_SRC="$(command -v agent-director)"
        VERSION_CHECK_REQUIRED=0
    fi
fi
if [[ -n "$BINARY_SRC" && ! -x "$BINARY_SRC" ]]; then
    echo "install.sh: source binary not executable: $BINARY_SRC" >&2
    exit 3
fi

# The operator tool agent-director-admin (b.vqr): --admin-binary, the
# downloaded release asset, the in-repo build beside bin/agent-director,
# or else the one an earlier install put in place (b.azo), so a re-run
# from the installed skill, outside any checkout, pairs it with the
# agent-director on PATH. The version-stamp check below refuses it when
# it is from another build than agent-director. It is never looked up
# on PATH, where it is never installed. Installing it over itself is
# safe: it is copied to a temp file beside it, which is moved into
# place, and the --keep-prior snapshot to its .prior only reads it.
if [[ -z "$ADMIN_SRC" ]]; then
    if [[ -x "$admin_candidate" ]]; then
        ADMIN_SRC="$admin_candidate"
    elif [[ -x "$DEFAULT_ADMIN_PATH" ]]; then
        ADMIN_SRC="$DEFAULT_ADMIN_PATH"
    fi
fi

# A missing source is refused once, naming every option the re-run
# needs (b.vqr): with neither binary found, both --binary and
# --admin-binary, never one refusal per binary. An agent-director found
# only on PATH counts as missing then, since no agent-director-admin was
# found, beside the script or installed, to pair with it.
if [[ -z "$BINARY_SRC" || -z "$ADMIN_SRC" ]]; then
    if [[ -n "$ADMIN_SRC" ]]; then
        echo "install.sh: no source binary found." >&2
        echo "  Tried: $candidate" >&2
        echo "  Tried: command -v agent-director" >&2
        echo "  Pass --binary <path> to override." >&2
    elif [[ -n "$BINARY_SRC" && "$VERSION_CHECK_REQUIRED" -eq 1 ]]; then
        echo "install.sh: no agent-director-admin source binary found." >&2
        echo "  Tried: $admin_candidate" >&2
        echo "  Tried: $DEFAULT_ADMIN_PATH" >&2
        echo "  Pass --admin-binary <path> to override." >&2
    else
        # Here no agent-director-admin was found, and agent-director was
        # either not found at all or found only on PATH. The headline says
        # "or on PATH" only when none was there; the found-on-PATH case is
        # named by its own line below.
        bin_looked="beside the script"
        [[ -n "$BINARY_SRC" ]] || bin_looked="beside the script or on PATH"
        echo "install.sh: no source binaries found: no agent-director-admin beside the script or installed, and no agent-director ${bin_looked}." >&2
        echo "  Tried: $candidate" >&2
        if [[ -n "$BINARY_SRC" ]]; then
            echo "  Found on PATH, not used: $BINARY_SRC (no agent-director-admin to pair with it)" >&2
        else
            echo "  Tried: command -v agent-director" >&2
        fi
        echo "  Tried: $admin_candidate" >&2
        echo "  Tried: $DEFAULT_ADMIN_PATH" >&2
        echo "  Pass --binary <path> --admin-binary <path> (both from the same build) to override." >&2
    fi
    exit 3
fi
echo "  source  : $BINARY_SRC"
if [[ ! -x "$ADMIN_SRC" ]]; then
    echo "install.sh: admin source binary not executable: $ADMIN_SRC" >&2
    exit 3
fi
echo "  admin source: $ADMIN_SRC"

# --------------------------------------------------------------------
# --binary architecture probe (SR-2.2, preflight step 6)
#
# Catches the case where a supported host receives a wrong-arch binary
# (e.g. operator passes a darwin-arm64 artifact on a Linux/x86_64 host).
# Runs file(1) against $BINARY_SRC, and against $ADMIN_SRC (b.vqr), and
# pattern-matches against the host pair captured by the OS/CPU gate (T1).
# On mismatch: exit 2 with the SR-2.2 message. file(1) is a hard
# preflight requirement (T2 + required_tools); never silent-skip.
#
# Multiple substring matches joined by && rather than a single regex —
# file's output format varies subtly across distros (`x86-64` vs
# `x86_64`), and a brittle regex would silently misclassify a valid
# binary on a future toolchain.
# --------------------------------------------------------------------

# ad_arch_probe <path> <flag>: exit 2 with the SR-2.2 message, naming
# <flag>, unless file(1) shows <path> is a binary for this host pair.
ad_arch_probe() {
    local path="$1" flag="$2" file_out detected arch_ok=0
    file_out="$(file -L -b "$path")"
    case "${uname_s}/${uname_m}" in
        Linux/x86_64)
            if grep -q "ELF 64-bit LSB" <<<"$file_out" \
                && { grep -q "x86-64" <<<"$file_out" || grep -q "x86_64" <<<"$file_out"; }; then
                arch_ok=1
            fi
            ;;
        Darwin/arm64)
            if grep -q "Mach-O" <<<"$file_out" \
                && { grep -q "arm64e" <<<"$file_out" || grep -q "arm64" <<<"$file_out"; }; then
                arch_ok=1
            fi
            ;;
    esac

    if [[ "$arch_ok" -ne 1 ]]; then
        # Distil the diagnostic excerpt from file's output — first ~60 chars
        # is plenty to surface "Mach-O arm64" or "ELF 64-bit LSB x86-64".
        detected="$(printf '%s' "$file_out" | head -c 80 | tr '\n' ' ')"
        echo "install.sh: $flag $path: architecture mismatch (binary appears to be ${detected}; host is ${uname_s}/${uname_m}). Did you pass the wrong $flag?" >&2
        exit 2
    fi
}

ad_arch_probe "$BINARY_SRC" "--binary"
ad_arch_probe "$ADMIN_SRC" "--admin-binary"

# --------------------------------------------------------------------
# Source-tree version check
#
# When the operator points install.sh at a local binary (either via
# --binary or via the in-repo bin/agent-director fallback) AND the tree
# install.sh takes bin/ from (source_root: two levels above the script,
# resolved physically) is a git checkout of agent-director's source,
# refuse to install a binary whose embedded commit doesn't match that
# checkout's HEAD. Catches the "operator forgot to `make build` after
# pulling new code" footgun — installing a stale artifact silently is
# exactly what b.qag flagged.
#
# That checkout and its HEAD are source_head, read above with the
# source root: only source_root's own `.git` counts (a directory in a
# plain clone, a `gitdir:` file in a linked worktree, b.go9), so a
# worktree nested inside another checkout is held to its own HEAD; a
# repo merely enclosing the script is never used (b.1rs), nor is CWD's.
# Unlike the build advice, the check does not need a Makefile.
#
# Skipped when:
#   - --from-release was used (the asset is by construction not the
#     operator's source tree)
#   - source_root is not a git checkout of agent-director's source
#     (b.1rs): a curled tarball, or an installed copy of the skill in
#     some other repo, such as a dotfiles ~ or ~/.claude, whose HEAD no
#     agent-director binary is built from
#   - BINARY_SRC came from `command -v` (option (c)): there's no
#     promise it was built from this tree, and the user explicitly
#     asked for "whatever's on PATH"
# --------------------------------------------------------------------

if [[ "$FROM_RELEASE" -eq 0 && "${VERSION_CHECK_REQUIRED:-1}" -eq 1 ]]; then
    # An empty source_head (no `.git` of source_root's own that git can
    # read, or no cmd/agent-director) skips the check (b.1rs).
    if [[ -n "$source_head" ]]; then
        # Run the binary's `version` verb. An older binary without the
        # verb will exit non-zero / emit an err_name envelope; jq -e
        # returns non-zero if .commit is absent or null. Either way we
        # land in the mismatch path with bin_commit empty.
        bin_commit=$("$BINARY_SRC" version 2>/dev/null \
            | jq -er '.commit // empty' 2>/dev/null \
            || true)

        if [[ -z "$bin_commit" || "$bin_commit" == "unknown" || "$bin_commit" != "$source_head" ]]; then
            echo "install.sh: source-tree version check failed." >&2
            echo "  binary  : $BINARY_SRC" >&2
            if [[ -z "$bin_commit" ]]; then
                echo "  built from: <no version stamp — binary is older than this verb, or built without ldflags>" >&2
            elif [[ "$bin_commit" == "unknown" ]]; then
                echo "  built from: <unstamped — likely a plain 'go build' without -ldflags>" >&2
            else
                echo "  built from: $bin_commit" >&2
            fi
            echo "  HEAD    : $source_head ($source_root)" >&2
            echo "" >&2
            echo "  The binary at $BINARY_SRC was not built from this checkout's" >&2
            echo "  current HEAD. Installing it would silently substitute stale code" >&2
            echo "  for the source you're sitting on. Either:" >&2
            echo "    - rebuild it first:    $build_advice" >&2
            echo "    - or download release: rerun with --from-release (omit --binary)" >&2
            exit 3
        fi
        echo "  version-check: binary commit matches HEAD ($source_head)"
    fi
fi

# --------------------------------------------------------------------
# Version-stamp pairing (b.vqr)
#
# agent-director and agent-director-admin open the same store, so they
# must come from the same build: refuse to install unless both
# binaries' `version` verbs report the same version and commit, and
# that commit is a real one. A binary with no readable stamp (no
# `version` verb, or no version or commit in its output) cannot be
# shown to match and is refused too. So is a pair whose commit is
# empty or "unknown", as the source-tree check above treats it: a
# plain `go build` reports {"version":"dev","commit":"unknown"}, so two
# such builds from different trees would otherwise count as a pair.
# --------------------------------------------------------------------

# ad_version_stamp <binary>: print "<version> <commit>" from the
# binary's `version` verb, or nothing when either is missing.
ad_version_stamp() {
    "$1" version 2>/dev/null \
        | jq -er 'select((.version | type) == "string" and (.commit | type) == "string") | "\(.version) \(.commit)"' 2>/dev/null \
        || true
}

main_stamp="$(ad_version_stamp "$BINARY_SRC")"
admin_stamp="$(ad_version_stamp "$ADMIN_SRC")"
if [[ "$main_stamp" != "$admin_stamp" ]]; then
    echo "install.sh: agent-director and agent-director-admin version stamps differ; refusing to install." >&2
    echo "  agent-director      : $BINARY_SRC (${main_stamp:-<no version stamp>})" >&2
    echo "  agent-director-admin: $ADMIN_SRC (${admin_stamp:-<no version stamp>})" >&2
    echo "" >&2
    echo "  Both binaries open the same store, so they must come from the same" >&2
    echo "  build (the same version and commit). Either:" >&2
    echo "    - rebuild both first:  $build_advice" >&2
    echo "    - or download release: rerun with --from-release (omit --binary and --admin-binary)" >&2
    exit 3
fi
# The stamps are equal; the commit is the stamp's last word.
stamp_commit="${main_stamp##* }"
if [[ -z "$main_stamp" || -z "$stamp_commit" || "$stamp_commit" == "unknown" ]]; then
    echo "install.sh: agent-director and agent-director-admin carry no commit stamp, so they cannot be shown to come from the same build; refusing to install." >&2
    echo "  agent-director      : $BINARY_SRC (${main_stamp:-<no version stamp>})" >&2
    echo "  agent-director-admin: $ADMIN_SRC (${admin_stamp:-<no version stamp>})" >&2
    echo "" >&2
    echo "  Both binaries open the same store, so they must come from the same" >&2
    echo "  build, and only a commit stamp can show that: binaries built without" >&2
    echo "  one (a plain 'go build' reports commit \"unknown\") match any other" >&2
    echo "  such build, from any tree. 'make build' in a git checkout stamps" >&2
    echo "  both with the checkout's commit. Either:" >&2
    echo "    - rebuild both first:  $build_advice" >&2
    echo "    - or download release: rerun with --from-release (omit --binary and --admin-binary)" >&2
    exit 3
fi
echo "  version-check: agent-director-admin stamp matches ($main_stamp)"

# --------------------------------------------------------------------
# Capability check (SR-11.7)
#
# Both binaries' `version` results carry config_dir, the directory each
# keeps AD's files in: ~/.config/agent-director. A binary whose version
# output lacks it, or names another directory, is from an earlier release,
# which keeps its files in a directory this install does not manage, so it
# is refused (exit 3). The check reads no store: it runs `version`, which is
# handle-free, on each binary.
# --------------------------------------------------------------------

# ad_config_dir_of <binary> — print the config_dir its `version` result
# holds, or nothing when there is none.
ad_config_dir_of() {
    "$1" version 2>/dev/null \
        | jq -er 'select((.config_dir | type) == "string") | .config_dir' 2>/dev/null \
        || true
}

main_config_dir="$(ad_config_dir_of "$BINARY_SRC")"
admin_config_dir="$(ad_config_dir_of "$ADMIN_SRC")"
if [[ "$main_config_dir" != "~/.config/agent-director" || "$admin_config_dir" != "~/.config/agent-director" ]]; then
    echo "install.sh: a binary to install is from an earlier release (its version output lacks config_dir ~/.config/agent-director); refusing to install." >&2
    echo "  agent-director      : $BINARY_SRC (config_dir: ${main_config_dir:-<none>})" >&2
    echo "  agent-director-admin: $ADMIN_SRC (config_dir: ${admin_config_dir:-<none>})" >&2
    echo "" >&2
    echo "  This release keeps AD's files in ~/.config/agent-director, and both of its" >&2
    echo "  binaries report that directory as config_dir in their version result." >&2
    echo "  A binary that does not is from an earlier release, which an earlier" >&2
    echo "  release's own install.sh installs: fetch that release's install.sh from its" >&2
    echo "  tag and run it, rather than this one. To install this release instead:" >&2
    echo "    - build both binaries from it: $build_advice" >&2
    echo "    - or download release:        rerun with --from-release (omit --binary and --admin-binary)" >&2
    exit 3
fi

# --------------------------------------------------------------------
# The move of AD's directory (SR-11.7)
#
# With the move due, and every pre-flight check, both binary sources and the
# checks on them behind us, this copies ~/.agent-director into
# ~/.config/agent-director and renames the old directory to
# ~/.agent-director.pre-move-<YYYYmmdd-HHMMSS> (or a free variant of that
# name), before the binary swap, SR-11.2's migration and the store steps:
#
#   0. Both binary sources are copied to temp files outside both
#      directories (a source may live inside the old one, where `command -v
#      agent-director` can lead), and the copies are used from here on. The
#      EXIT cleanup removes them.
#   1. The live-row and process checks run again (ErrAgentsLive, nothing
#      changed).
#   2. A stale partial new directory (one holding .move-incomplete) is
#      deleted; ~/.config is created (mode 0700) if missing, then the new
#      directory.
#   3. The marker .move-incomplete is written into the new directory.
#   4. The old directory's contents are copied, except bin/, admin/ and a
#      store inside it: each file and directory written owner-only (umask
#      077) and then given the original's mode (never cp -p or cp -a), with
#      commands that run on GNU and BSD systems, a link copied as the same
#      link. Anything that is not a file, directory or link (a fifo, a
#      socket, a device) fails the move, named.
#   5. The store inside the old directory is copied with SQLite's backup,
#      from the original opened with sqlite3 -readonly, its -wal and -shm
#      never copied as files; the copy must have the same user_version and
#      .sha3sum --schema.
#   6. Every other copied file is checked byte for byte.
#   7. The path keys in the new config.toml are rewritten (SR-11.7 "Path
#      keys"); only the new copy is changed.
#   8. The kept directory's name is written into the marker.
#   9. The old directory is renamed to that name.
#  10. The marker is removed.
# The move makes no backup of its own: the kept directory holds the
# original.
#
# A failure before the rename, an interrupt included, removes the partial new
# directory (marker included; a linked new directory has its contents
# cleared) and, with rmdir only, a ~/.config the move created; it ends the
# install with exit 5 (ErrMoveFailed), the old directory untouched. The
# move's EXIT, INT, TERM and HUP traps chain onto the install's existing
# EXIT trap. After the rename nothing is removed: a signal that arrives after
# mv completed but before the traps are put back finds the rename done (the
# old path gone, the kept directory there), removes nothing, says the move
# finished, and ends the install with exit 5; the re-run continues from the
# new directory.
# --------------------------------------------------------------------

MOVED=0
MOVED_KEPT=""
MOVE_ACTIVE=0
MOVE_CREATED_BASE=0
MOVE_NEW_IS_LINK=0
MOVE_CAUSE=""
MOVE_PREV_EXIT=""
MOVE_NOTES=""
MOVE_KEPT_NAME=""
STORE_SRC_PHYS=""
stage_bin=""
stage_admin=""

# ad_move_rename_done — true when the rename (step 9) has happened: the move
# has chosen the kept directory's name, the old path is gone and the kept
# directory exists (the name was free when chosen, so only the move's mv made
# it). A signal can arrive after mv completes and before the traps are put
# back, and nothing may be removed after the rename, so the failure path asks
# this state, not a flag set after mv.
ad_move_rename_done() {
    [[ -n "$MOVE_KEPT_NAME" && ! -e "$OLD_INSTALL_ROOT" && ! -L "$OLD_INSTALL_ROOT" ]] \
        && [[ -e "$MOVE_KEPT_NAME" || -L "$MOVE_KEPT_NAME" ]]
}

# ad_move_cleanup — remove what the move made, when it is still active:
# the partial new directory (a linked one has its contents cleared) and,
# with rmdir only, a ~/.config the move created. Safe to run twice. Removes
# nothing once the rename is done.
ad_move_cleanup() {
    local was_active="$MOVE_ACTIVE"
    MOVE_ACTIVE=0
    if [[ "$was_active" -ne 1 ]]; then
        return 0
    fi
    if ad_move_rename_done; then
        return 0
    fi
    if [[ "$MOVE_NEW_IS_LINK" -eq 1 ]]; then
        find "${DEFAULT_INSTALL_ROOT}/" -mindepth 1 -maxdepth 1 -exec rm -rf -- {} + 2>/dev/null || true
    else
        rm -rf -- "$DEFAULT_INSTALL_ROOT" 2>/dev/null || true
    fi
    if [[ "$MOVE_CREATED_BASE" -eq 1 ]]; then
        rmdir -- "$(dirname "$DEFAULT_INSTALL_ROOT")" 2>/dev/null || true
    fi
}

# ad_move_fail <cause> — end the install for a failure before the rename:
# clean up, say why, exit 5 (ErrMoveFailed). When the rename has already
# happened (a signal arrived after mv completed), nothing is removed and the
# message says so.
ad_move_fail() {
    local was_active="$MOVE_ACTIVE" renamed=0
    trap - INT TERM HUP
    if ad_move_rename_done; then
        renamed=1
    fi
    ad_move_cleanup
    if [[ "$renamed" -eq 1 ]]; then
        MOVE_ACTIVE=0
        echo "install.sh: the install was stopped after the move had renamed $OLD_INSTALL_ROOT: $1" >&2
        echo "  The move itself finished: $OLD_INSTALL_ROOT is now $MOVE_KEPT_NAME, and $DEFAULT_INSTALL_ROOT holds the complete copy. Nothing was removed." >&2
        echo "  Re-run this install: it continues from $DEFAULT_INSTALL_ROOT, and no move is due. $MOVE_KEPT_NAME keeps the old directory as it was." >&2
        echo "  (operator-actions.md, \"\`install.sh\` stops with \`ErrMoveFailed\`\")" >&2
        ad_exit_5 ErrMoveFailed
    fi
    echo "install.sh: moving $OLD_INSTALL_ROOT to $DEFAULT_INSTALL_ROOT failed: $1" >&2
    if [[ "$was_active" -eq 1 ]]; then
        echo "  The partial $DEFAULT_INSTALL_ROOT was removed (and so was ~/.config, when the move had created it); $OLD_INSTALL_ROOT was not changed." >&2
    else
        echo "  Nothing was changed: $OLD_INSTALL_ROOT is as it was." >&2
    fi
    echo "  A full disk is the usual cause: free some space (or fix the cause above), then re-run this install." >&2
    echo "  (operator-actions.md, \"\`install.sh\` stops with \`ErrMoveFailed\`\")" >&2
    ad_exit_5 ErrMoveFailed
}

# ad_move_on_exit — the move's EXIT trap: clean up a move that is still
# active (the safety net for a stop nothing reported), then run the EXIT
# trap the install had before.
ad_move_on_exit() {
    ad_move_cleanup
    if [[ -n "$MOVE_PREV_EXIT" ]]; then
        eval "$MOVE_PREV_EXIT"
    fi
}

# ad_move_on_signal <name> — the move's INT, TERM and HUP trap.
ad_move_on_signal() {
    ad_move_fail "the install was interrupted (SIG$1)"
}

# ad_move_arm_traps — chain the move's traps onto the install's EXIT trap.
ad_move_arm_traps() {
    eval "set -- $(trap -p EXIT)"
    MOVE_PREV_EXIT="${3:-}"
    trap ad_move_on_exit EXIT
    trap 'ad_move_on_signal INT' INT
    trap 'ad_move_on_signal TERM' TERM
    trap 'ad_move_on_signal HUP' HUP
}

# ad_move_disarm_traps — after the rename, put the install's EXIT trap back
# and drop the signal traps.
ad_move_disarm_traps() {
    MOVE_ACTIVE=0
    trap - INT TERM HUP
    if [[ -n "$MOVE_PREV_EXIT" ]]; then
        trap "$MOVE_PREV_EXIT" EXIT
    else
        trap - EXIT
    fi
}

# ad_stage_binary <source> <var name> — copy <source> to a temp file outside
# both directories and name it in <var name> (so the EXIT cleanup removes
# it). Sets MOVE_CAUSE and fails when it cannot.
ad_stage_binary() {
    local t
    t="$(mktemp -t agent-director.stage.XXXXXX)" || {
        MOVE_CAUSE="cannot make a temp file to hold a copy of $1"
        return 1
    }
    printf -v "$2" '%s' "$t"
    case "$t" in
        "$OLD_ROOT_CLEAN"/*|"$NEW_ROOT_CLEAN"/*)
            MOVE_CAUSE="the temp directory ($t) is inside AD's directories; set TMPDIR to a directory outside them"
            return 1
            ;;
    esac
    if ! cp -- "$1" "$t" || ! chmod 0755 "$t" || ! cmp -s "$1" "$t"; then
        MOVE_CAUSE="cannot copy $1 to the temp file $t"
        return 1
    fi
}

# ad_move_walk <copy|verify> <src dir> <dst dir> <depth> — copy, or check
# byte for byte, the entries of <src dir> (a real directory) in <dst dir>,
# except, at depth 0, bin and admin, and the store inside the old directory
# (STORE_SRC_PHYS) with its -wal and -shm. Sets MOVE_CAUSE and fails on the
# first entry it cannot copy or that differs.
ad_move_walk() {
    local mode="$1" src="$2" dst="$3" depth="$4" e name d t
    if [[ ! -r "$src" || ! -x "$src" ]]; then
        MOVE_CAUSE="cannot read the directory $src"
        return 1
    fi
    while IFS= read -r -d '' e; do
        name="${e##*/}"
        d="${dst}/${name}"
        if [[ "$depth" -eq 0 && ( "$name" == bin || "$name" == admin ) ]]; then
            continue
        fi
        if [[ -n "$STORE_SRC_PHYS" && ( "$e" == "$STORE_SRC_PHYS" || "$e" == "${STORE_SRC_PHYS}-wal" || "$e" == "${STORE_SRC_PHYS}-shm" ) ]]; then
            continue
        fi
        if [[ -L "$e" ]]; then
            t="$(readlink "$e")" || {
                MOVE_CAUSE="cannot read the link $e"
                return 1
            }
            if [[ "$mode" == copy ]]; then
                ln -s -- "$t" "$d" || {
                    MOVE_CAUSE="cannot copy the link $e to $d"
                    return 1
                }
            elif [[ ! -L "$d" || "$(readlink "$d")" != "$t" ]]; then
                MOVE_CAUSE="the copy $d of the link $e is not the same link"
                return 1
            fi
        elif [[ -d "$e" ]]; then
            if [[ "$mode" == copy ]]; then
                (umask 077; mkdir -- "$d") || {
                    MOVE_CAUSE="cannot make the directory $d"
                    return 1
                }
            elif [[ ! -d "$d" || -L "$d" ]]; then
                MOVE_CAUSE="the copy $d of the directory $e is missing"
                return 1
            fi
            ad_move_walk "$mode" "$e" "$d" $((depth + 1)) || return 1
            if [[ "$mode" == copy ]]; then
                chmod "$(ad_mode_of "$e")" "$d" || {
                    MOVE_CAUSE="cannot set the mode of the directory $d"
                    return 1
                }
            fi
        elif [[ -f "$e" ]]; then
            if [[ "$mode" == copy ]]; then
                (umask 077; cp -- "$e" "$d") || {
                    MOVE_CAUSE="cannot copy $e to $d"
                    return 1
                }
                chmod "$(ad_mode_of "$e")" "$d" || {
                    MOVE_CAUSE="cannot set the mode of $d"
                    return 1
                }
            elif ! cmp -s "$e" "$d"; then
                MOVE_CAUSE="the copy $d of $e differs from it"
                return 1
            fi
        else
            MOVE_CAUSE="cannot copy $e: it is not a regular file, a directory or a link (a fifo, a socket or a device, say). Remove it or move it out of $OLD_INSTALL_ROOT, then re-run"
            return 1
        fi
    done < <(find "$src" -mindepth 1 -maxdepth 1 -print0)
}

# ad_move_backup_store <source> <destination> — copy the store with SQLite's
# backup, from <source> opened read-only, to <destination> (through a temp
# name beside the new directory's top, then a rename), give the copy the
# original's mode, and check that it has the same user_version and
# .sha3sum --schema. Sets MOVE_CAUSE and fails when it cannot.
ad_move_backup_store() {
    local src="$1" dst="$2" tmp="${DEFAULT_INSTALL_ROOT}/.state-backup.$$" out v_src v_dst h_src h_dst
    if [[ "$tmp" == *[\"\\]* ]]; then
        MOVE_CAUSE="the path $tmp holds a quote or a backslash, which SQLite's backup command cannot take"
        return 1
    fi
    rm -f -- "$tmp"
    out="$(umask 077; sqlite3 -readonly -batch -init /dev/null -cmd ".timeout ${store_busy_timeout_ms}" "$src" ".backup \"${tmp}\"" 2>&1)" || {
        MOVE_CAUSE="SQLite's backup of the store $src failed: $out"
        return 1
    }
    mkdir -p -- "$(dirname "$dst")" || {
        MOVE_CAUSE="cannot make the directory for the store's copy $dst"
        return 1
    }
    mv -f -- "$tmp" "$dst" || {
        MOVE_CAUSE="cannot move the store's copy to $dst"
        return 1
    }
    chmod "$(ad_mode_of "$src")" "$dst" || {
        MOVE_CAUSE="cannot set the mode of the store's copy $dst"
        return 1
    }
    v_src="$(sqlite3 -readonly -batch -init /dev/null -cmd ".timeout ${store_busy_timeout_ms}" "$src" "PRAGMA user_version;" 2>&1)" || true
    v_dst="$(sqlite3 -readonly -batch -init /dev/null -cmd ".timeout ${store_busy_timeout_ms}" "$dst" "PRAGMA user_version;" 2>&1)" || true
    if [[ ! "$v_src" =~ ^[0-9]+$ || "$v_src" != "$v_dst" ]]; then
        MOVE_CAUSE="the copy $dst of the store has user_version '${v_dst}', the original's is '${v_src}'"
        return 1
    fi
    h_src="$(sqlite3 -readonly -batch -init /dev/null -cmd ".timeout ${store_busy_timeout_ms}" "$src" ".sha3sum --schema" 2>&1)" || true
    h_dst="$(sqlite3 -readonly -batch -init /dev/null -cmd ".timeout ${store_busy_timeout_ms}" "$dst" ".sha3sum --schema" 2>&1)" || true
    if [[ ! "$h_src" =~ ^[0-9a-f]{40,}$ || "$h_src" != "$h_dst" ]]; then
        MOVE_CAUSE="the schema of the copy $dst of the store could not be shown to match the original's (.sha3sum --schema gave '${h_dst}' for the copy and '${h_src}' for the original)"
        return 1
    fi
}

# ad_move_quote <value> <quote char> — print <value> as a TOML string,
# keeping <quote char> (" or ') when the value allows it and the other when
# it does not. Fails when neither quoting can hold the value.
ad_move_quote() {
    if [[ "$2" == '"' && "$1" != *[\"\\]* ]]; then
        printf '"%s"' "$1"
    elif [[ "$1" != *"'"* ]]; then
        printf "'%s'" "$1"
    elif [[ "$1" != *[\"\\]* ]]; then
        printf '"%s"' "$1"
    else
        return 1
    fi
}

# ad_move_key_newvalue <key label> <value as written> — decide the new value
# of a path key from the old directory's point of view (SR-11.7 "Path
# keys"). Sets REWRITE_NEWVAL (empty: leave the line as written) and may add
# a note to MOVE_NOTES. Sets MOVE_CAUSE and fails when the place cannot be
# resolved.
REWRITE_NEWVAL=""
ad_move_key_newvalue() {
    local label="$1" v="$2" form="rel" resolved
    REWRITE_NEWVAL=""
    case "$v" in
        "~/"*) form=tilde; resolved="${HOME}/${v:2}" ;;
        /*) form=abs; resolved="$v" ;;
        *) resolved="${OLD_INSTALL_ROOT}/${v}" ;;
    esac
    resolved="$(ad_clean_path "$resolved")"
    if ! ad_key_class "$resolved"; then
        MOVE_CAUSE="cannot place $label $v against the old directory (its directory is missing or unreadable)"
        return 1
    fi
    case "$KEY_CLASS" in
        in)
            case "$form" in
                abs) REWRITE_NEWVAL="${NEW_ROOT_CLEAN}/${resolved#"$OLD_ROOT_CLEAN"/}" ;;
                tilde) REWRITE_NEWVAL="~/${NEW_ROOT_CLEAN#"$HOME_CLEAN"/}/${resolved#"$OLD_ROOT_CLEAN"/}" ;;
            esac
            ;;
        link)
            REWRITE_NEWVAL="${NEW_ROOT_CLEAN}/${KEY_PHYS#"$OLD_PHYS"/}"
            ;;
        warn)
            # Inside by its text, outside by its physical place (SR-11.7 places
            # a key by its physical place): a relative value would resolve
            # against the new directory, where a copied relative link points one
            # level deeper, so it is written as the absolute physical path it
            # reaches today. An absolute or ~/ value never gets here while
            # pre-flight refuses it (ad_move_refuse_outward_path, which says
            # how to switch that to this rewrite).
            REWRITE_NEWVAL="$KEY_PHYS"
            ;;
        out)
            if [[ "$form" == rel ]]; then
                REWRITE_NEWVAL="$KEY_PHYS"
            fi
            ;;
    esac
    return 0
}

# ad_move_rewrite_config <config> — rewrite [store] db_path and [log]
# error_log_path in the new <config> per SR-11.7 "Path keys", changing no
# other byte (a byte-order mark, CRLF line ends and a missing final newline
# are kept; a value's quoting and trailing comment are kept, switching quote
# only when the new value needs it). Written through a temp file with cat,
# so the file keeps its mode. Sets MOVE_CAUSE and fails when it cannot.
ad_move_rewrite_config() {
    local cfg="$1" tmp="${DEFAULT_INSTALL_ROOT}/.config.toml.rewrite" line n=0 table="" cr bom noeol changed=0
    local indent key eq rest q v tail newq
    local re_header='^[[:blank:]]*\[[[:blank:]]*([A-Za-z0-9_-]+)[[:blank:]]*\][[:blank:]]*(#.*)?$'
    local re_keyval='^([[:blank:]]*)(db_path|error_log_path)([[:blank:]]*=[[:blank:]]*)(.*)$'
    local re_dq='^"([^"]*)"(.*)$'
    local re_sq="^'([^']*)'(.*)\$"
    if [[ ! -f "$cfg" ]]; then
        return 0
    fi
    {
        while :; do
            noeol=0
            if ! IFS= read -r line; then
                if [[ -z "$line" ]]; then
                    break
                fi
                noeol=1
            fi
            n=$((n + 1))
            bom=""
            if [[ "$n" -eq 1 && "$line" == $'\xef\xbb\xbf'* ]]; then
                bom=$'\xef\xbb\xbf'
                line="${line#"$bom"}"
            fi
            cr=""
            if [[ "$line" == *$'\r' ]]; then
                cr=$'\r'
                line="${line%$'\r'}"
            fi
            if [[ "$line" =~ $re_header ]]; then
                table="${BASH_REMATCH[1]}"
            elif [[ ( "$table" == store || "$table" == log ) && "$line" =~ $re_keyval ]]; then
                indent="${BASH_REMATCH[1]}"
                key="${BASH_REMATCH[2]}"
                eq="${BASH_REMATCH[3]}"
                rest="${BASH_REMATCH[4]}"
                if { [[ "$table" == store && "$key" == db_path ]] || [[ "$table" == log && "$key" == error_log_path ]]; }; then
                    q=""
                    if [[ "$rest" =~ $re_dq ]]; then
                        q='"'
                    elif [[ "$rest" =~ $re_sq ]]; then
                        q="'"
                    fi
                    if [[ -n "$q" ]]; then
                        v="${BASH_REMATCH[1]}"
                        tail="${BASH_REMATCH[2]}"
                        if [[ -n "$v" ]]; then
                            ad_move_key_newvalue "[${table}] ${key}" "$v" || return 1
                            if [[ -n "$REWRITE_NEWVAL" ]]; then
                                newq="$(ad_move_quote "$REWRITE_NEWVAL" "$q")" || {
                                    MOVE_CAUSE="the new value $REWRITE_NEWVAL of [${table}] ${key} holds both a single and a double quote (or a backslash with a single quote), which no TOML string can write here"
                                    return 1
                                }
                                line="${indent}${key}${eq}${newq}${tail}"
                                changed=1
                            fi
                        fi
                    fi
                fi
            fi
            printf '%s%s%s' "$bom" "$line" "$cr"
            if [[ "$noeol" -eq 0 ]]; then
                printf '\n'
            fi
        done <"$cfg"
    } >"$tmp" || {
        rm -f -- "$tmp"
        if [[ -z "$MOVE_CAUSE" ]]; then
            MOVE_CAUSE="cannot rewrite the path keys in $cfg"
        fi
        return 1
    }
    if [[ "$changed" -eq 1 ]]; then
        cat "$tmp" >"$cfg" || {
            MOVE_CAUSE="cannot write the rewritten $cfg"
            return 1
        }
    fi
    rm -f -- "$tmp"
    return 0
}

# ad_move_print_references <kept dir> — print every other reference into the
# old directory the installer finds in the new config.toml (a [launch]
# value) or a template (an extra_env value, cwd), each with its fix, left as
# written. Called after the agent-rename migration, which inserts lines, so
# the file:line printed is right for the files as the install leaves them; the
# migration's own copies (*.pre-agent-rename.*) are not searched.
ad_move_print_references() {
    local hits
    hits="$( { grep -n -F -I -e "$OLD_INSTALL_ROOT" -e '~/.agent-director' -e '$HOME/.agent-director' -e '${HOME}/.agent-director' "${DEFAULT_INSTALL_ROOT}/config.toml" 2>/dev/null | sed "s|^|${DEFAULT_INSTALL_ROOT}/config.toml:|"; grep -r -n -F -I --exclude='*.pre-agent-rename.*' -e "$OLD_INSTALL_ROOT" -e '~/.agent-director' -e '$HOME/.agent-director' -e '${HOME}/.agent-director' "${DEFAULT_INSTALL_ROOT}/templates" 2>/dev/null; } || true)"
    if [[ -n "$hits" ]]; then
        echo "  references: these lines still name the old directory. They were left as written; change each to the same place under $NEW_ROOT_CLEAN (or remove it) by hand, since the old directory is kept only until you delete it ($1):"
        printf '%s\n' "$hits" | sed 's/^/    /'
    fi
}

# Step 0: stage both binary sources outside both directories.
if [[ "$MOVE_DUE" -eq 1 ]]; then
    trap ad_exit_cleanup EXIT
    ad_stage_binary "$BINARY_SRC" stage_bin || ad_move_fail "$MOVE_CAUSE"
    ad_stage_binary "$ADMIN_SRC" stage_admin || ad_move_fail "$MOVE_CAUSE"
    BINARY_SRC="$stage_bin"
    ADMIN_SRC="$stage_admin"

    echo "  move    : $OLD_INSTALL_ROOT -> $DEFAULT_INSTALL_ROOT"
    ad_move_arm_traps

    # Step 1: nothing running, again.
    ad_agents_live_check

    # Step 2: the stale partial new directory goes; ~/.config and the new
    # directory are made.
    if [[ -L "$DEFAULT_INSTALL_ROOT" ]]; then
        MOVE_NEW_IS_LINK=1
    fi
    if [[ "$MOVE_PARTIAL" -eq 1 ]]; then
        MOVE_ACTIVE=1
        ad_move_cleanup
        MOVE_ACTIVE=1
    fi
    config_base="$(dirname "$DEFAULT_INSTALL_ROOT")"
    if [[ ! -d "$config_base" ]]; then
        MOVE_ACTIVE=1
        MOVE_CREATED_BASE=1
        mkdir -- "$config_base" || ad_move_fail "cannot make $config_base"
        chmod 00700 "$config_base" || ad_move_fail "cannot set the mode of $config_base"
    fi
    MOVE_ACTIVE=1
    if [[ ! -d "$DEFAULT_INSTALL_ROOT" ]]; then
        mkdir -- "$DEFAULT_INSTALL_ROOT" || ad_move_fail "cannot make $DEFAULT_INSTALL_ROOT"
    fi
    chmod 00700 "$DEFAULT_INSTALL_ROOT" || ad_move_fail "cannot set the mode of $DEFAULT_INSTALL_ROOT"

    # Step 3: the marker.
    (umask 077; : >"$MOVE_MARKER") || ad_move_fail "cannot write the marker $MOVE_MARKER"

    # Step 4: copy everything but bin/, admin/ and the store inside.
    if [[ "$STORE_CLASS" == in || "$STORE_CLASS" == link ]]; then
        STORE_SRC_PHYS="$STORE_PHYS"
    fi
    ad_move_walk copy "$OLD_PHYS" "$DEFAULT_INSTALL_ROOT" 0 || ad_move_fail "$MOVE_CAUSE"

    # Step 5: the store, with SQLite's backup.
    if [[ -n "$STORE_SRC_PHYS" && -f "$STORE_SRC_PHYS" ]]; then
        ad_move_backup_store "$STORE_SRC_PHYS" "${DEFAULT_INSTALL_ROOT}${STORE_SRC_PHYS#"$OLD_PHYS"}" || ad_move_fail "$MOVE_CAUSE"
    fi

    # Step 6: every other copied file, byte for byte.
    ad_move_walk verify "$OLD_PHYS" "$DEFAULT_INSTALL_ROOT" 0 || ad_move_fail "$MOVE_CAUSE"

    # Step 7: the path keys in the new config.toml.
    ad_move_rewrite_config "${DEFAULT_INSTALL_ROOT}/config.toml" || ad_move_fail "$MOVE_CAUSE"

    # Step 8: the kept directory's name, into the marker.
    kept="${OLD_INSTALL_ROOT}.pre-move-$(ad_ts)"
    kept_base="$kept"
    kept_n=1
    while [[ -e "$kept" || -L "$kept" ]]; do
        kept_n=$((kept_n + 1))
        kept="${kept_base}-${kept_n}"
    done
    MOVE_KEPT_NAME="$kept"
    printf '%s\n' "$kept" >"$MOVE_MARKER" || ad_move_fail "cannot write the kept directory's name into $MOVE_MARKER"

    # Step 9: the rename. After it nothing is removed.
    mv -- "$OLD_INSTALL_ROOT" "$kept" || ad_move_fail "cannot rename $OLD_INSTALL_ROOT to $kept"
    MOVED=1
    MOVED_KEPT="$kept"
    ad_move_disarm_traps

    # Step 10: the marker.
    if ! rm -f -- "$MOVE_MARKER"; then
        echo "  warning: could not remove the marker $MOVE_MARKER; the next install removes it" >&2
    fi

    echo "  moved   : $OLD_INSTALL_ROOT -> $DEFAULT_INSTALL_ROOT (store, config.toml, templates and the rest, checked)"
    echo "  kept    : $MOVED_KEPT (the old directory as it was before the move, old binaries included; delete it once the upgrade is confirmed)"
    if [[ "$KEEP_PRIOR" -eq 1 ]]; then
        echo "  prior   : --keep-prior made no snapshot on this install: the new bin and admin directories hold no earlier binary. The way back is operator-actions.md, \"Going back to the previous release\""
    fi
    if [[ -n "$MOVE_NOTES" ]]; then
        printf '%s' "$MOVE_NOTES"
    fi

    # Every step from here works in the new directory (SR-11.7).
    if ! state_db="$(ad_store_db_path "${DEFAULT_INSTALL_ROOT}/config.toml" "$HOME" "$DEFAULT_INSTALL_ROOT")"; then
        ad_exit_5 ErrConfigMalformed
    fi
    state_db_name="state.db"
    if [[ "$state_db" != "$(ad_clean_path "${DEFAULT_INSTALL_ROOT}/state.db")" ]]; then
        state_db_name="$state_db"
    fi
    if ! store_busy_timeout_ms="$(ad_store_busy_timeout_ms "${DEFAULT_INSTALL_ROOT}/config.toml")"; then
        ad_exit_5 ErrConfigMalformed
    fi
fi

# --------------------------------------------------------------------
# Create install root + bin dir
# --------------------------------------------------------------------

# ~/.config is the directory the install root lives under (always under HOME;
# XDG_CONFIG_HOME is never read); make it (mode 0700) only when it is missing,
# and leave an existing one's mode alone.
config_base="$(dirname "$DEFAULT_INSTALL_ROOT")"
if [[ ! -d "$config_base" ]]; then
    mkdir -p "$config_base"
    chmod 00700 "$config_base"
fi
mkdir -p "$DEFAULT_INSTALL_ROOT"
# Five-digit mode required to clear inherited setuid/setgid on a directory:
# GNU coreutils clears a dir's setuid/setgid bits only when the numeric mode
# has five or more octal digits; a shorter mode (e.g. four-digit `0700`)
# PRESERVES a setgid bit already on the dir. Under a setgid parent (e.g.
# debian:bookworm-slim ships /tmp as 3777), mkdir -p yields a 2700 dir, and
# `chmod 0700` leaves it 2700 — only `chmod 00700` clears it. See bee b.29h.
chmod 00700 "$DEFAULT_INSTALL_ROOT"
mkdir -p "$DEFAULT_BIN_DIR"
chmod 00755 "$DEFAULT_BIN_DIR"

# --------------------------------------------------------------------
# Atomic install: write to a sibling temp path, then mv over the target.
#
# `mv` within the same filesystem is atomic at the inode level —
# concurrent readers see either the old binary or the new, never half.
# A running process holds the old inode reference, so an in-flight
# exec is unaffected by the swap.
#
# This is the standard pattern for single-binary CLI installers
# (gh, kubectl, terraform). The version-manager pattern (canonical
# symlink → versioned files) is only worth the complexity when you
# actually manage multiple concurrent versions; we don't.
# --------------------------------------------------------------------

CANONICAL="${DEFAULT_BIN_DIR}/agent-director"
MANAGED_LINK=""
PRIOR="${CANONICAL}.prior"
TMP="${CANONICAL}.tmp.$$"
ADMIN_CANONICAL="$DEFAULT_ADMIN_PATH"
ADMIN_PRIOR="${ADMIN_CANONICAL}.prior"
ADMIN_TMP="${ADMIN_CANONICAL}.tmp.$$"

# --keep-prior snapshots agent-director-admin too (b.vqr), before either
# binary is replaced, so rolling both back restores a matching pair.
# With no agent-director-admin installed yet but an agent-director
# being replaced (an upgrade from a release before 0.11.0), there is
# nothing to snapshot: a stale .prior, which would pair wrongly, is
# removed, and the rollback is removing agent-director-admin.
#
# Whether to snapshot is decided once, for the pair (b.2wk). Neither
# binary is snapshotted when agent-director is already byte-identical
# to its source and agent-director-admin is too or is not installed:
# such an install replaces nothing, and snapshotting would overwrite the
# earlier run's rollback copies with the binaries being installed
# again, as a re-run after step 4's exit 5 (which advises one) would.
# Otherwise both are snapshotted as above. Deciding per binary could
# leave .prior files from different builds: installs of the pairs
# (A0,M0), (A1,M1) and (A2,M1) would leave A1 beside M0. A cmp that
# cannot compare (missing, or a file it cannot read) counts as
# different, so both are snapshotted.
if [[ "$KEEP_PRIOR" -eq 1 && "$MOVED" -eq 0 ]]; then
    if [[ -f "$CANONICAL" ]] && cmp -s "$CANONICAL" "$BINARY_SRC" \
        && { [[ ! -f "$ADMIN_CANONICAL" ]] || cmp -s "$ADMIN_CANONICAL" "$ADMIN_SRC"; }; then
        if [[ -f "$ADMIN_CANONICAL" ]]; then
            not_snapshotted="not snapshotted: the installed agent-director and agent-director-admin are already the ones being installed"
        else
            not_snapshotted="not snapshotted: the installed agent-director is already the one being installed, and no agent-director-admin is installed"
        fi
        if [[ -f "$PRIOR" ]]; then
            echo "  prior   : kept $PRIOR ($not_snapshotted)"
        else
            echo "  prior   : none ($not_snapshotted)"
        fi
        if [[ -f "$ADMIN_PRIOR" ]]; then
            echo "  admin prior: kept $ADMIN_PRIOR ($not_snapshotted)"
        elif [[ -f "$PRIOR" ]]; then
            # An agent-director.prior with no admin .prior beside it is
            # from an upgrade from before 0.11.0, which removed the admin
            # .prior: rolling back still means removing
            # agent-director-admin, as that run said.
            echo "  admin prior: none ($not_snapshotted); to roll back, remove $ADMIN_CANONICAL"
        else
            echo "  admin prior: none ($not_snapshotted)"
        fi
    else
        if [[ -f "$CANONICAL" ]]; then
            cp -f "$CANONICAL" "$PRIOR"
            chmod 0755 "$PRIOR"
            echo "  prior   : snapshotted to $PRIOR"
        fi
        if [[ -f "$ADMIN_CANONICAL" ]]; then
            cp -f "$ADMIN_CANONICAL" "$ADMIN_PRIOR"
            chmod 0755 "$ADMIN_PRIOR"
            echo "  admin prior: snapshotted to $ADMIN_PRIOR"
        elif [[ -f "$CANONICAL" ]]; then
            rm -f "$ADMIN_PRIOR"
            echo "  admin prior: none (no agent-director-admin was installed); to roll back, remove $ADMIN_CANONICAL"
        fi
    fi
fi

# --------------------------------------------------------------------
# Both binaries are staged before either is replaced (b.vqr): the
# admin directory is created and both temp copies made, with their
# modes, first; only then do the two mvs run, back to back. A failure
# while staging (a full disk, an unwritable admin directory) so
# replaces neither binary, rather than leaving a new agent-director
# beside an old agent-director-admin. The EXIT trap removes a staged
# copy that was never moved into place (and the --from-release
# downloads, as before, and a migration sentinel's temp file that step 3
# never moved into place).
#
# The operator tool agent-director-admin goes into its own directory
# (mode 0700, five digits to clear an inherited setgid bit as above),
# never ~/.config/agent-director/bin/ and never on PATH: no option creates a
# symlink for it. Its path is printed once, at the end.
# --------------------------------------------------------------------

trap ad_exit_cleanup EXIT

mkdir -p "$DEFAULT_ADMIN_DIR"
chmod 00700 "$DEFAULT_ADMIN_DIR"
cp "$BINARY_SRC" "$TMP"
chmod 0755 "$TMP"
cp "$ADMIN_SRC" "$ADMIN_TMP"
chmod 0755 "$ADMIN_TMP"
mv "$TMP" "$CANONICAL"
mv "$ADMIN_TMP" "$ADMIN_CANONICAL"

echo "  binary  : $CANONICAL"

# --------------------------------------------------------------------
# Optional PATH symlink — agent-director only. agent-director-admin never
# gets one, under any option (b.vqr).
# --------------------------------------------------------------------

if [[ "$NO_SYMLINK" -eq 0 && -n "$SYMLINK_DIR" ]]; then
    if [[ ! -d "$SYMLINK_DIR" ]]; then
        echo "  symlink : skipped — $SYMLINK_DIR does not exist"
    elif [[ "$SYMLINK_DIR" =~ [[:space:]] ]]; then
        echo "  symlink : skipped — $SYMLINK_DIR contains whitespace"
    else
        target="${SYMLINK_DIR}/agent-director"
        ln -sfn "$CANONICAL" "${target}.new"
        mv -f "${target}.new" "$target"
        MANAGED_LINK="$target"
        echo "  symlink : $target → $CANONICAL"
    fi
fi

# After a move, every other link into the old bin directory that pre-flight
# found (OLD_BIN_LINKS; the one this install manages was re-pointed above),
# and each $PATH entry naming that directory, is printed with its fix. The
# installer changes neither: a link elsewhere, and a shell startup file, are
# the operator's (SR-11.7).
if [[ "$MOVED" -eq 1 ]]; then
    for old_link in ${OLD_BIN_LINKS[@]+"${OLD_BIN_LINKS[@]}"}; do
        if [[ "$old_link" == "$MANAGED_LINK" ]]; then
            continue
        fi
        echo "  old link: $old_link still leads into the old directory's bin, which is kept only until you delete it. Re-point it:"
        printf '    ln -sfn %q %q\n' "$CANONICAL" "$old_link"
    done
    for old_entry in ${OLD_BIN_PATH_ENTRIES[@]+"${OLD_BIN_PATH_ENTRIES[@]}"}; do
        echo "  old PATH: \$PATH names $old_entry, the old directory's bin, which is kept only until you delete it. In your shell startup file (~/.bashrc, ~/.zshrc, ...), replace it with ${DEFAULT_BIN_DIR}, then open a new shell."
    done
fi

# --------------------------------------------------------------------
# The agent-rename migration, applied (SR-11.2)
#
# With the gate open (pre-flight), after SR-11.7's move and the binary swap
# and before the first step that opens the store, config.toml and every
# templates/*.toml in AD's directory (DEFAULT_INSTALL_ROOT, the new one
# after a move) are migrated, each change re-derived here by the migration
# block from the file as it is now. Each file whose bytes change is first
# copied beside itself, with its mode, as
# <file>.pre-agent-rename.<YYYYmmdd-HHMMSS> (a name config.toml's merge
# backups, config.toml.bak.<ts>, never take, and one that does not end in
# .toml, so no template lookup reads it), and then replaced whole, byte for
# byte, with its mode kept and a symlink written through (a 0600 template
# stays 0600): the new bytes go to <target>.new, written owner-only (umask
# 077) and then given the target's mode, and mv puts them over the target.
# A file that needs no change gets neither a write nor a backup, so a re-run
# that crosses the rename again changes nothing already migrated.
#
# A write that fails (a full disk, permissions changed since pre-flight)
# leaves that file as it was, keeps every backup, removes its temp file and
# stops the install before the store is opened, with exit 1 and the two ways
# on (ad_migrate_fail_write). The writes go through the cp (the backup), the
# chmod and the mv found on PATH, which is how a test fails one of them.
# --------------------------------------------------------------------

migrate_tmp=""

# ad_backup_keeping_mode <file> <bak> — copy <file> to <bak> with <file>'s
# permission bits, the same way (b.ojn): remove any <bak> first (an earlier
# run's copy of the same second, at whatever mode it has), copy owner-only
# (umask 077), then chmod. cp reads through a symlinked <file>, so <bak> is
# a regular copy of the link's target, beside the link. Not cp -p: GNU cp
# -p also copies ACLs and xattrs, and fails where the file system cannot
# take them (NFS homes; Ubuntu LP#2087769), which would stop the install
# here.
ad_backup_keeping_mode() {
    rm -f "$2"
    (umask 077; cp -f "$1" "$2") || return 1
    chmod "$(ad_mode_of "$1")" "$2"
}

# ad_migrate_ways_on — the two ways on after a migration step failed, on
# stderr.
ad_migrate_ways_on() {
    echo "  The new binaries are already installed, and the store is not opened. Either:" >&2
    echo "    - fix the cause, then run this release's install.sh again. It completes the" >&2
    echo "      migration: it changes nothing in a file already migrated and makes no new" >&2
    echo "      backup of one, and then it migrates the store. Or" >&2
    echo "    - take the way back to the previous release: see \"Going back to the" >&2
    echo "      previous release\" and \"A migration write failed after the binary swap\" in" >&2
    echo "      docs/operator-actions.md." >&2
}

# ad_migrate_fail_write <file> <error> — a migration write failed: remove
# the temp file, report the file and the error and the two ways on, and exit
# 1, as the installer's other failed writes do. <file> is as it was before.
ad_migrate_fail_write() {
    if [[ -n "$migrate_tmp" ]]; then
        rm -f "$migrate_tmp"
        migrate_tmp=""
    fi
    echo "install.sh: the agent-rename migration could not write $1; stopping before the store is opened." >&2
    echo "  file    : $1" >&2
    printf '%s\n' "$2" | sed '1s/^/  error   : /;2,$s/^/            /' >&2
    echo "  $1 is as it was, and every copy this install made is kept." >&2
    ad_migrate_stop_references
    ad_migrate_ways_on
    exit 1
}

# ad_migrate_stop_references — on a stop after the move (MOVED=1), print the
# move's references into the old directory on stderr, before the stop
# message's last lines: a re-run has no move due, so it would never print them
# (SR-11.7). Nothing when no move ran.
ad_migrate_stop_references() {
    if [[ "$MOVED" -eq 1 ]]; then
        ad_move_print_references "$MOVED_KEPT" >&2
    fi
}

# ad_migrate_refuse_applied <kind> <file> <error> — the migration refused
# <file> at apply time, which pre-flight accepted: remove the temp file, report
# the refusal, the move's references (when moved) and the two ways on, and
# exit 5 with the kind's err_name. <error> is the migration's output, its last
# line being the status line.
ad_migrate_refuse_applied() {
    if [[ -n "$migrate_tmp" ]]; then
        rm -f "$migrate_tmp"
        migrate_tmp=""
    fi
    echo "install.sh: the agent-rename migration refuses $2, which pre-flight accepted; stopping before the store is opened." >&2
    printf '%s\n' "$3" | sed '$d' >&2
    echo "  $2 changed since pre-flight." >&2
    ad_migrate_stop_references
    ad_migrate_ways_on
    ad_exit_5 "$(ad_agent_rename_err_name "$1")"
}

# ad_migrate_apply_file <config|template> <file> — migrate <file>. Return 0
# when it changed, 3 when it needed no change. Exit 1 on a failed write, and
# exit 5 on a refusal (the file changed since pre-flight).
ad_migrate_apply_file() {
    local kind="$1" file="$2" rc=0 target err bak
    if [[ ! -f "$file" ]]; then
        return 3
    fi
    if ! target="$(ad_resolve_link "$file" 2>&1)"; then
        ad_migrate_fail_write "$file" "$target"
    fi
    # Decide first, with stdout thrown away, whether the file needs a change:
    # nothing is created beside the target for a file that needs none (it may
    # sit behind a link into a directory this install cannot write).
    err="$(ad_agent_rename_migrate "$kind" "$file" 2>&1 >/dev/null)" || rc=$?
    case "$rc" in
        0) ;;
        3) return 3 ;;
        2)
            ad_migrate_refuse_applied "$kind" "$file" "$err"
            ;;
        *)
            ad_migrate_fail_write "$file" "$err"
            ;;
    esac
    rc=0
    migrate_tmp="${target}.new"
    rm -f "$migrate_tmp" 2>/dev/null || true
    err="$( (umask 077; ad_agent_rename_migrate "$kind" "$file" >"$migrate_tmp") 2>&1 )" || rc=$?
    case "$rc" in
        0) ;;
        3)
            rm -f "$migrate_tmp"
            migrate_tmp=""
            return 3
            ;;
        2)
            ad_migrate_refuse_applied "$kind" "$file" "$err"
            ;;
        *)
            ad_migrate_fail_write "$file" "$err"
            ;;
    esac
    bak="${file}.pre-agent-rename.${migrate_ts}"
    if ! err="$(ad_backup_keeping_mode "$file" "$bak" 2>&1)"; then
        ad_migrate_fail_write "$file" "copying it to $bak failed: $err"
    fi
    if ! err="$(chmod "$(ad_mode_of "$target")" "$migrate_tmp" 2>&1)"; then
        ad_migrate_fail_write "$file" "$err"
    fi
    if ! err="$(mv -f "$migrate_tmp" "$target" 2>&1)"; then
        ad_migrate_fail_write "$file" "$err"
    fi
    migrate_tmp=""
    echo "  migrate : $file migrated for the agent rename (copy: $bak)"
}

if [[ "$AGENT_RENAME_DUE" -eq 1 ]]; then
    migrate_ts="$(ad_ts)"
    migrate_changed=0
    mig_rc=0
    ad_migrate_apply_file config "${DEFAULT_INSTALL_ROOT}/config.toml" || mig_rc=$?
    if [[ "$mig_rc" -eq 0 ]]; then
        migrate_changed=1
    fi
    for mig_f in "${DEFAULT_INSTALL_ROOT}/templates"/*.toml; do
        mig_rc=0
        ad_migrate_apply_file template "$mig_f" || mig_rc=$?
        if [[ "$mig_rc" -eq 0 ]]; then
            migrate_changed=1
        fi
    done
    if [[ "$migrate_changed" -eq 0 ]]; then
        echo "  migrate : config.toml and the templates need no agent-rename change"
    fi
fi

# The move's other references into the old directory, printed after the
# migration so their line numbers are those of the files as they now are.
if [[ "$MOVED" -eq 1 ]]; then
    ad_move_print_references "$MOVED_KEPT"
fi

# --------------------------------------------------------------------
# Schema migration flow (SR-1.7) — six steps.
#
# The new binary is already in place (step 1, the atomic mv above). An
# older-than-binary state.db opens ONLY when a valid `migrate-authorized`
# sentinel (a sibling of state.db, JSON {"from":<actual>,"to":<target>})
# authorizes exactly that transition; the store consumes it on the
# successful migration. install.sh runs on the end-user's machine as an
# admin action, so it is the legitimate writer of that sentinel.
#
# state.db is the store database agent-director opens, $state_db:
# ~/.config/agent-director/state.db, or wherever [store] db_path puts it (read in
# pre-flight, b.2io). Its sentinel is a sibling of that database
# (ad_sentinel_path), wherever it is. Messages call it $state_db_name:
# "state.db" for the default, otherwise its path.
#
#   Step 2 — read the DB's ACTUAL user_version via
#            `sqlite3 PRAGMA user_version` (reads through the WAL; raw
#            header bytes are wrong for a WAL-mode DB). Fresh install →
#            no DB yet → nothing to authorize; step 4 fresh-creates it.
#            An existing DB whose version cannot be read, or reads as
#            anything but a whole number, stops the install (exit 5)
#            before any migration is authorized and before
#            agent-director opens the store.
#   Step 3 — write the sentinel {"from":<actual>,"to":<target>} beside
#            state.db, SKIPPING when from==target (already current).
#            <target> comes from a probe open of the existing store,
#            decided by the probe's err_name. A config the binary
#            refuses (ErrConfigMalformed) stops the install here (exit 5):
#            the probe never reached state.db. When a sentinel written
#            before this install lets the probe itself run the migration,
#            step 3 reads the version again and reports that migration
#            instead; its new version is the target step 5 verifies.
#   Step 4 — trigger exactly one store-opening open (`$CANONICAL list`)
#            so the store runs the migration and consumes the sentinel.
#            NOT `help`/`version`: SR-4 (Part D) makes those DB-free, and
#            Parts A/D land in either order, so a help-warmup would never
#            open the store and step 5 would fail on every upgrade. A
#            refused config stops the install with the config advice, not
#            the store advice (exit 5).
#   Step 5 — verify user_version == target; FAIL the install loudly
#            (non-zero exit) on any mismatch, or when the version cannot
#            be read, if a migration was expected. With none expected, an
#            unreadable version is a warning and the install carries on.
#   Step 6 — between the binary swap and the successful step-4 open there
#            is a brief (seconds, install-controlled) window where a
#            concurrent hook firing `$CANONICAL list` against the not-yet-
#            migrated DB gets the admin migration error. This is accepted;
#            it is documented in SKILL.md, not worked around in code.
# --------------------------------------------------------------------

# (ad_user_version, ad_is_version, ad_got_version, ad_show_unreadable_version
# and ad_fail_unreadable_version are defined above, in the pre-flight section
# on the agent-rename gate, which reads the store's version first.)

# ad_target_version <stderr> — the schema version THIS binary requires.
# There is no public "print my schema version" verb (and SR-4 forbids
# leaning on help/version for DB facts), so step 3 reads it from the
# binary's own authoritative refusal: opening an older DB with no valid
# sentinel refuses with ErrSchemaMigrationRequired, whose description says
# "... this binary requires v<N>.". <stderr> is that refusal's stderr; this
# prints <N>, or nothing when it names none. If the open does NOT refuse
# (DB already current, fresh create, or a migration a sentinel written
# before this install authorized), the target equals the DB's post-open
# user_version, which the install reads directly — so this helper is only
# consulted when a migration is actually pending.
ad_target_version() {
    printf '%s' "$1" | grep -oE 'requires v[0-9]+' | head -n1 | grep -oE '[0-9]+' || true
}

# ad_err_name <stderr> — the err_name of the JSON error envelope
# ({"err_name":…,"err_description":…}) in a verb's captured stderr, or
# nothing when it holds none; lines that are not an envelope are skipped.
# The install branches on this name, never on the description's English
# text (b.7b4).
ad_err_name() {
    printf '%s\n' "$1" | jq -Rr '(fromjson | .err_name | strings)?' 2>/dev/null | tail -n1 || true
}

# ad_fail_config_refused <stderr> — agent-director refused its config file
# (ErrConfigMalformed) at a store-opening verb: report it and exit 5.
# <stderr> is that verb's stderr, whose envelope names the file and what is
# wrong with it. The binary loads the config before it opens the store, so
# the refusal says nothing about state.db: the store was not opened, no
# migration ran, and none can be authorized until the config loads. A
# re-run after the fix starts over, and its step-3 probe then authorizes any
# pending migration (b.7b4).
ad_fail_config_refused() {
    echo "install.sh: agent-director refused its config file (ErrConfigMalformed)" >&2
    echo "  config  : ${DEFAULT_INSTALL_ROOT}/config.toml" >&2
    printf '%s\n' "$1" | sed 's/^/  /' >&2
    echo "  Fix what the error above names in the config file, then re-run this" >&2
    echo "  install." >&2
    ad_exit_5 ErrConfigMalformed
}

# ---- Step 2: read the DB's ACTUAL current schema version ----
if [[ ! -e "$state_db" ]]; then
    # Fresh install: no DB on disk. No sentinel is needed — the step-4
    # open fresh-creates state.db at the binary's current schemaVersion.
    db_version_before=""
    echo "  schema  : no existing ${state_db_name} — fresh create on first open"
else
    db_version_before_rc=0
    db_version_before="$(ad_user_version "$state_db")" || db_version_before_rc=$?
    if ! ad_got_version "$db_version_before_rc" "$db_version_before"; then
        # Without the version no migration can be authorized, and the
        # step-4 open would refuse an older store anyway: stop here.
        echo "install.sh: reading ${state_db_name}'s schema version FAILED" >&2
        echo "  state.db: $state_db" >&2
        ad_fail_unreadable_version "$db_version_before_rc" "$db_version_before" \
            "tell whether ${state_db_name} needs a migration" \
            "No migration was authorized."
    fi

    # ---- Step 3: write the migration sentinel (skip when already current) ----
    # The probe: one store-opening verb against the existing store, with no
    # sentinel written yet. Its err_name decides what step 3 does (b.7b4):
    #   - none (it opened): the DB is current, or a sentinel written before
    #     this install let the probe migrate it (below); nothing to
    #     authorize.
    #   - ErrSchemaMismatch: the DB is newer than this binary (or has no
    #     valid store id); nothing to authorize, and step 4 surfaces it.
    #   - ErrSchemaMigrationRequired: the DB is older; its message names the
    #     target, and the sentinel authorizes that migration.
    #   - ErrConfigMalformed: the config, loaded before the store, was
    #     refused, so the probe never reached state.db. Stop here: a re-run
    #     after the config is fixed probes again and authorizes any pending
    #     migration.
    #   - anything else: the probe could not tell. Step 4's open reports
    #     the failure if it persists.
    #
    # "No sentinel written yet" means none by this install. One written
    # before it (by an earlier run whose open failed and advised a re-run,
    # or by an operator) may be there, and when its from and to match, the
    # probe's open runs the migration, consumes it and succeeds: an open
    # alone does not mean the DB was current (b.dzw). So when a sentinel
    # was there before the probe and the probe opened a store above v0,
    # read the version again. A higher one means the probe ran the
    # migration, and is the target step 5 verifies: a successful open
    # leaves the store at the binary's version. A v0 store is created at
    # the target without the sentinel being read, so its rise is no
    # migration. A sentinel that does not match needs nothing here: at an
    # older store the probe refuses (ErrSchemaMigrationRequired) and the
    # sentinel written below replaces it, and an open at a current or
    # newer store never reads it.
    sentinel="$(ad_sentinel_path "$state_db")"
    sentinel_before=0
    if [[ -e "$sentinel" ]]; then
        sentinel_before=1
    fi
    probe_name=""
    if ! probe_err="$("$CANONICAL" list 2>&1 >/dev/null)"; then
        probe_name="$(ad_err_name "$probe_err")"
        probe_name="${probe_name:-<no err_name>}"
    fi
    target_version=""
    if [[ "$probe_name" == ErrConfigMalformed ]]; then
        ad_fail_config_refused "$probe_err"
    elif [[ "$probe_name" == ErrSchemaMigrationRequired ]]; then
        target_version="$(ad_target_version "$probe_err")"
    elif [[ -z "$probe_name" && "$sentinel_before" -eq 1 && "$db_version_before" != 0 ]]; then
        db_version_probed_rc=0
        db_version_probed="$(ad_user_version "$state_db")" || db_version_probed_rc=$?
        if ! ad_got_version "$db_version_probed_rc" "$db_version_probed"; then
            # The probe may have migrated the store, so neither "no
            # migration authorization needed" nor a skipped step 5 would
            # be true: stop here, as step 2 does.
            echo "install.sh: reading ${state_db_name}'s schema version FAILED" >&2
            echo "  state.db: $state_db" >&2
            ad_fail_unreadable_version "$db_version_probed_rc" "$db_version_probed" \
                "tell whether the probe (agent-director list) ran a migration" \
                "A sentinel written before this install was beside ${state_db_name}, and it may" \
                "have authorized one."
        fi
        if ((db_version_probed > db_version_before)); then
            target_version="$db_version_probed"
        fi
    fi

    if [[ -z "$probe_name" && -n "$target_version" ]]; then
        echo "  schema  : migration v${db_version_before}→v${target_version} ran at the probe (agent-director list), authorized by a sentinel written before this install (sentinel $sentinel)"
    elif [[ -z "$probe_name" || "$probe_name" == ErrSchemaMismatch ]]; then
        echo "  schema  : ${state_db_name} at v${db_version_before}; no migration authorization needed"
    elif [[ -z "$target_version" ]]; then
        echo "  schema  : ${state_db_name} at v${db_version_before}; could not tell whether a migration is needed (agent-director list failed: ${probe_name})"
    elif [[ "$db_version_before" == "$target_version" ]]; then
        # Defensive: probe reported a target equal to current. Nothing to do.
        echo "  schema  : ${state_db_name} already at target v${target_version}; no sentinel written"
    else
        # The temp file sits beside the sentinel, in the store's directory,
        # which [store] db_path may put in a directory other users can write.
        # mktemp gives it a name no one can predict and creates it itself
        # (mode 0600, refusing any file or symlink already at that name), so
        # nothing planted there in advance receives the write below. A
        # failure stops the install (exit 5) rather than fall back to a
        # predictable name.
        if ! tmp_sentinel="$(mktemp "${sentinel}.tmp.XXXXXX")"; then
            tmp_sentinel=""
            echo "install.sh: writing the migration sentinel FAILED" >&2
            echo "  sentinel: $sentinel" >&2
            echo "  mktemp could not create a temp file beside it (its error is above), so" >&2
            echo "  no migration was authorized: ${state_db_name} is still at v${db_version_before}." >&2
            echo "  The new agent-director does not open it until it is at v${target_version}." >&2
            echo "  Fix what mktemp's error names (a directory you cannot write, say, or a" >&2
            echo "  full disk), then re-run this install: it authorizes the migration again." >&2
            # A re-run alone does not fix it: someone must make the
            # directory writable or free space first.
            ad_exit_5 ErrSchemaVerifyFailed
        fi
        printf '{"from": %d, "to": %d}\n' "$db_version_before" "$target_version" > "$tmp_sentinel"
        chmod 0600 "$tmp_sentinel" 2>/dev/null || true
        mv -f "$tmp_sentinel" "$sentinel"
        echo "  schema  : authorized migration v${db_version_before}→v${target_version} (sentinel $sentinel)"
    fi
fi

# ---- Step 4: trigger exactly one store-opening open ----
# `list` remains store-opening after SR-4 (unlike help/version). This is
# the open that runs any authorized migration and consumes the sentinel;
# on a fresh install it creates state.db at the current schemaVersion.
# Capture stderr from a SINGLE invocation while preserving its exit status.
# Two separate `list` calls (one for the status check, one for the diagnostic)
# could observe different DB state — a concurrent hook migrating in between
# would yield exit 5 with an empty/misleading diagnostic. Route stdout to
# /dev/null and capture stderr to a var in one run.
if open_err="$("$CANONICAL" list 2>&1 >/dev/null)"; then
    :
else
    # A refused config stopped the open before it reached state.db, so the
    # store advice below does not apply (b.7b4).
    open_name="$(ad_err_name "$open_err")"
    if [[ "$open_name" == ErrConfigMalformed ]]; then
        ad_fail_config_refused "$open_err"
    fi
    # "If this install authorized a migration above", not "if a migration
    # was authorized": a migration that a sentinel written before this
    # install authorized ran at step 3's probe, which consumed it (b.dzw).
    echo "install.sh: store open (agent-director list) failed after install" >&2
    if [[ -n "$open_err" ]]; then
        printf '%s\n' "$open_err" | sed 's/^/  /' >&2
    fi
    echo "  The new binary could not open ${state_db_name}. If this install" >&2
    echo "  authorized a migration above, it was NOT consumed; re-running this" >&2
    echo "  install will retry it. If ${state_db_name} is NEWER than this binary" >&2
    echo "  (ErrSchemaMismatch), install a newer agent-director instead." >&2
    # The cause line relays the open's own err_name (b.cfq). Stderr with no
    # envelope, or one whose err_name is not a bare Err... name, gets
    # ErrStoreOpen, the name agent-director gives a store open that fails
    # for no cause it names (clisetup.Open).
    if [[ ! "$open_name" =~ ^Err[A-Za-z0-9]+$ ]]; then
        open_name=ErrStoreOpen
    fi
    ad_exit_5 "$open_name"
fi

# ---- Step 5: verify the post-open schema version, fail loudly on mismatch ----
# The open succeeded, so a missing state.db means install.sh and
# agent-director disagree on where the store is, or it was removed since:
# needs a human (ErrSchemaVerifyFailed). agent-director opens its store at
# [store] db_path in $HOME/.config/agent-director/config.toml (pkg/api
# resolveStorePath; its --store-path and --home flags aside, which
# install.sh never passes), and no AGENT_DIRECTOR_* variable moves it.
# install.sh read that file in pre-flight (ad_store_db_path), so the two
# disagree only when db_path changed since or the installed binary resolves
# it differently. On an upgrade step 2 read $state_db, so there it was
# removed or moved: a changed db_path would have left it in place.
#
# A re-run reads db_path again and opens the store there, so it finishes
# after a changed db_path. With no store at $state_db its step-4 open
# creates a new, empty one, so the advice says to put a moved store back
# first. A re-run that fails the same way means the binary resolves the
# store differently from ad_store_db_path: the maintainers' turn.
if [[ ! -f "$state_db" ]]; then
    echo "install.sh: ${state_db_name} was not created by the store open" >&2
    echo "  state.db: $state_db" >&2
    echo "  config  : ${DEFAULT_INSTALL_ROOT}/config.toml" >&2
    echo "  The store open (agent-director list) succeeded, yet there is no store" >&2
    echo "  at the state.db path above, where install.sh expected it. agent-director" >&2
    echo "  opens its store at [store] db_path in the config file above" >&2
    echo "  (~/.config/agent-director/state.db when unset), and install.sh read that file" >&2
    echo "  before the open. So either db_path changed since and agent-director" >&2
    echo "  opened a store somewhere else, or something removed or moved the store" >&2
    echo "  after the open." >&2
    echo "  Check db_path in the config file, and whether anything (a cleanup, say)" >&2
    echo "  removed or moved the store. To keep a moved store's sessions, put it" >&2
    echo "  back at the state.db path above first: with no store there, a re-run" >&2
    echo "  creates a new, empty one. Then re-run this install: it reads db_path" >&2
    echo "  again and opens the store there. If a re-run fails this same way, the" >&2
    echo "  installed agent-director does not open its store where this install.sh" >&2
    echo "  expects it: contact the maintainers." >&2
    ad_exit_5 ErrSchemaVerifyFailed
fi
chmod 0600 "$state_db" 2>/dev/null || true
db_version_after_rc=0
db_version_after="$(ad_user_version "$state_db")" || db_version_after_rc=$?
schema_shown="v${db_version_after}"
ad_got_version "$db_version_after_rc" "$db_version_after" || schema_shown="<unreadable>"
echo "  state.db: $(stat -c '%a' "$state_db" 2>/dev/null || stat -f '%Lp' "$state_db") at $state_db (schema ${schema_shown})"

migration_expected=0
[[ -n "$db_version_before" && -n "${target_version:-}" ]] && migration_expected=1

if ! ad_got_version "$db_version_after_rc" "$db_version_after"; then
    # The read gave no version, which says nothing about the store: the
    # step-4 open succeeded. Never silent, migration expected or not.
    # With no migration to check, the read only reports the version: warn,
    # show how to read it later (%q as below), and carry on (b.xd9). The
    # warning says none was authorized, not none expected: step 3 may not
    # have been able to tell whether one was needed.
    if [[ "$migration_expected" -eq 1 ]]; then
        echo "install.sh: schema migration verification FAILED" >&2
        echo "  expected user_version: $target_version" >&2
        ad_fail_unreadable_version "$db_version_after_rc" "$db_version_after" "check the migration"
    fi
    echo "install.sh: warning: ${state_db_name}'s schema version is unreadable after the store open" >&2
    ad_show_unreadable_version "$db_version_after"
    echo "  The store open (agent-director list) succeeded and no migration was" >&2
    echo "  authorized, so the install carries on. Check the version later with:" >&2
    printf '    sqlite3 -batch -init /dev/null -cmd ".timeout %s" %q "PRAGMA user_version;"\n' "$store_busy_timeout_ms" "$state_db" >&2
fi

if [[ "$migration_expected" -eq 1 ]]; then
    # A migration was expected. Verify it actually landed.
    #
    # Step 5 runs only after step 4's open succeeded, and a successful open
    # leaves state.db at the binary's version: it runs any authorized
    # migration and consumes the sentinel (internal/store/schema.go). So a
    # readable mismatch means state.db changed after the open or the read is
    # wrong, never a sentinel left for a retry. A re-run does not need one:
    # its steps 2-3 read the version again and authorize afresh (b.wt9).
    #
    # The sentinel delete after a committed migration is fail-open: if it
    # fails, consumeAuthorization (internal/store/migrate_auth.go) logs
    # ad.schema.authorization_delete_failed and the open still succeeds, so
    # the file can outlive its use. The text below need not hedge for that.
    # The migration it authorized has committed, and a leftover is inert: an
    # open at the target version never reads it, a later binary's migration
    # from the target mismatches its `from` and refuses, and any later
    # install that authorizes a migration overwrites it in step 3. No advice
    # below, the check or the re-run, depends on whether the file is there.
    #
    # A re-run below the target reaches it by one of three paths: at v0 the
    # step-3 probe's open creates the schema at the target with no sentinel;
    # above v0 step 3 authorizes the migration and step 4 runs it, or, when
    # a sentinel left from before already authorizes it, the probe runs it.
    # So the text says the re-run brings state.db to the target, not that
    # it authorizes a migration.
    #
    # target_version comes from the probe's refusal when step 3 authorized
    # the migration, and from step 3's re-read when the probe ran it
    # (b.dzw). The check below is the same for both.
    #
    # The check command is a line to copy into a shell, so the path is
    # quoted with %q: a plain path prints unchanged, and one holding shell
    # characters ($, `, quotes) prints escaped.
    if [[ "$db_version_after" != "$target_version" ]]; then
        echo "install.sh: schema migration verification FAILED" >&2
        echo "  expected user_version: $target_version" >&2
        echo "  actual   user_version: $db_version_after" >&2
        echo "  The store open (agent-director list) succeeded, and a successful" >&2
        echo "  open leaves ${state_db_name} at v${target_version}: any authorized migration" >&2
        echo "  has run, and its sentinel is consumed. Yet the read after the open" >&2
        echo "  gives v${db_version_after}: ${state_db_name} changed after the open, or the read" >&2
        echo "  is wrong. Check its version now:" >&2
        printf '    sqlite3 -batch -init /dev/null -cmd ".timeout %s" %q "PRAGMA user_version;"\n' "$store_busy_timeout_ms" "$state_db" >&2
        echo "  A re-run of this install reads the version again: below v${target_version} it" >&2
        echo "  brings ${state_db_name} to v${target_version} again, above v${target_version} it stops" >&2
        echo "  at the store open (ErrSchemaMismatch), and at v${target_version} it finishes" >&2
        echo "  the install. If a re-run fails this same way, contact the maintainers." >&2
        ad_exit_5 ErrSchemaVerifyFailed
    fi
    echo "  schema  : migration verified — ${state_db_name} now at v${db_version_after}"
fi

# --------------------------------------------------------------------
# Hook injection — additive merge into ~/.claude/settings.json
#
# Skipped entirely under --no-hooks: settings.json is not read, not
# backed up, not written. There's no edit, so there's nothing to back
# up — leaving settings.json byte-identical to its pre-install state.
# --------------------------------------------------------------------

# (ad_mode_of is defined above, in the section on AD's directory move, which
# copies modes too.)

# ad_replace_keeping_mode <file> <text> — replace <file> with <text> and a
# newline: write <file>.new beside it, then mv it over <file>. A symlinked
# <file> is written through (b.nw5): the file its link chain resolves to
# (ad_resolve_link, in pre-flight above) is the one replaced, from a .new
# beside that file, so the link survives and the file it points at (a
# dotfiles copy, say) gets the edit. A mv over the link itself would
# replace it with a regular file and leave its target stale. A dangling
# link gets the file it names created, as > through the link would.
# Pre-flight (ad_link_write_check) refused a link this cannot write
# through; ad_resolve_link's own loop failure stays as a backstop for a
# link changed since. An existing <file> keeps its permission bits
# (b.ojn): the new file is written owner-only (umask 077) and only then
# given them, so no one who could not read the old contents can read the
# new ones, even before the mv. A new <file> takes the umask's mode.
ad_replace_keeping_mode() {
    local target tmp
    target=$(ad_resolve_link "$1") || return
    tmp="${target}.new"
    rm -f "$tmp"
    if [[ -f "$target" ]]; then
        (umask 077; printf '%s\n' "$2" >"$tmp")
        chmod "$(ad_mode_of "$target")" "$tmp"
    else
        printf '%s\n' "$2" >"$tmp"
    fi
    mv -f "$tmp" "$target"
}

# (ad_backup_keeping_mode is defined above, with the agent-rename migration's
# apply step, which copies files first too.)

# The help hooks an earlier release's install.sh wrote name the old
# directory's binary (SR-11.7). An entry is "ours" the way uninstall.sh tests
# it: an object whose hooks list holds an object whose command is a string
# starting with the old directory's bin/agent-director path. Other shapes are
# never indexed, so they pass through untouched.
OLD_HOOK_PREFIX="${OLD_BIN_DIR}/agent-director"
OLD_HOOK_OURS_JQ='
    def ours:
      type == "object"
      and (.hooks | type) == "array"
      and any(.hooks[]; type == "object"
        and (.command | type) == "string"
        and (.command | startswith($oldprefix)));
'

if [[ "$CLAUDE_PRESENT" -eq 0 ]]; then
    # No Claude Code: settings.json is not touched, the old directory's help
    # hooks are neither removed nor printed, and this line wins over
    # --no-hooks' (SR-11.1).
    echo "  hooks   : skipped because Claude Code is not installed (settings.json is not touched)"
elif [[ "$NO_HOOKS" -eq 1 ]]; then
    echo "  hooks   : skipped (--no-hooks)"
    # settings.json stays byte-identical; entries naming the old directory's
    # binary are printed with their fix.
    if [[ -f "$DEFAULT_SETTINGS_PATH" ]]; then
        old_hook_lines=$(jq -r --arg oldprefix "$OLD_HOOK_PREFIX" "$OLD_HOOK_OURS_JQ"'
            (.hooks | objects) as $h
            | ("SessionStart", "SessionEnd") as $e
            | ($h[$e] | arrays | .[] | select(ours) | .hooks[] | objects | .command | strings | select(startswith($oldprefix)))
            | "\($e): \(.)"
        ' "$DEFAULT_SETTINGS_PATH" 2>/dev/null || true)
        if [[ -n "$old_hook_lines" ]]; then
            echo "  hooks   : $DEFAULT_SETTINGS_PATH holds hook entries that name the old directory's binary (${OLD_HOOK_PREFIX}), which this upgrade replaced; they were left as they are:"
            sed 's/^/    /' <<<"$old_hook_lines"
            echo "    Fix: re-run this install without --no-hooks (it removes them and adds the new ones), or edit each command to start with ${CANONICAL} (or delete the entry)."
        fi
    fi
else
    mkdir -p "$(dirname "$DEFAULT_SETTINGS_PATH")"

    # Read existing settings or start from {}. The merge below writes one
    # result per JSON document it reads, so the file's documents are
    # counted first (b.zbg): with none it would write no hooks, and with
    # several it would write several merged documents back. A file holding
    # none (empty, or only whitespace: a touched file, say), which Claude
    # Code reads as no settings, is merged as {}, after the usual backup. A
    # file holding several is refused here, exit 4, and left as it was,
    # as one that is not valid JSON is.
    if [[ -f "$DEFAULT_SETTINGS_PATH" ]]; then
        existing=$(<"$DEFAULT_SETTINGS_PATH")
        if ! settings_docs=$(printf '%s' "$existing" | jq -n '[inputs] | length' 2>/dev/null); then
            echo "install.sh: ~/.claude/settings.json is not valid JSON" >&2
            exit 4
        fi
        if [[ "$settings_docs" -eq 0 ]]; then
            existing='{}'
        elif [[ "$settings_docs" -gt 1 ]]; then
            echo "install.sh: cannot merge the hooks into ~/.claude/settings.json: it holds $settings_docs JSON documents" >&2
            echo "  Each is valid JSON, but the file must hold one JSON object, the shape" >&2
            echo "  Claude Code reads. Fix it, then re-run this install." >&2
            exit 4
        fi
    else
        existing='{}'
    fi

    # Our hook entries are uniquely identified by the command string
    # (the canonical binary path + " help"). Idempotency check: only add
    # if the command isn't already present in that event's hook list.
    help_cmd="${CANONICAL} help"

    # Merge logic (jq):
    #   - Ensure hooks.SessionStart is an array; append our entry if not
    #     already there (matched by command).
    #   - Ensure hooks.SessionEnd is an array; append our compact-matcher
    #     entry if not already there.
    # "Already there" reads only lists, where Claude Code reads hooks
    # (b.zbg): []? would read an object's values too, and count an entry
    # Claude Code never runs. So a merge that succeeds, of the one
    # document counted above, is one object holding both entries in
    # lists, and "hooks : injected" below is true; an event list that is
    # an object fails the append instead.
    # Valid JSON of another shape fails the merge with jq's runtime-error
    # status, 5, which set -e would make the install's exit status: a hook
    # merge failure is exit 4 (b.cfq). The shape is wrong either outside
    # the event lists (an array, or hooks a string, say) or inside them:
    # "already there" indexes each entry it reads (.hooks, .matcher) and
    # each hook in an entry's hooks list (.command), so one that is
    # neither an object nor null (a string, say) fails the merge too. A
    # null one indexes to null and passes.
    if ! new_settings=$(printf '%s' "$existing" | jq \
        --arg cmd "$help_cmd" --arg oldprefix "$OLD_HOOK_PREFIX" "$OLD_HOOK_OURS_JQ"'
            .hooks //= {}
            | .hooks.SessionStart //= []
            | .hooks.SessionEnd //= []
            | .hooks.SessionStart |= (if type == "array" then [.[] | select(ours | not)] else . end)
            | .hooks.SessionEnd |= (if type == "array" then [.[] | select(ours | not)] else . end)
            | (
                if any(.hooks.SessionStart | arrays | .[]; .hooks | arrays | any(.[]; .command == $cmd))
                  then .
                  else .hooks.SessionStart += [{"hooks":[{"type":"command","command":$cmd}]}]
                end
            )
            | (
                if any(.hooks.SessionEnd | arrays | .[]; .matcher == "compact" and (.hooks | arrays | any(.[]; .command == $cmd)))
                  then .
                  else .hooks.SessionEnd += [{"matcher":"compact","hooks":[{"type":"command","command":$cmd}]}]
                end
            )
        '); then
        echo "install.sh: cannot merge the hooks into ~/.claude/settings.json (jq's error is above)" >&2
        # Which side of the event lists is wrong (b.dzu): with the merge's
        # own fill-ins, an outer shape it takes yields the path of each
        # entry, and each hook in an entry's hooks list, that is neither an
        # object nor null, whether or not the merge read it. An outer shape
        # it does not take yields nothing, or a jq error, and keeps the
        # outer-shape message.
        if odd_hook_values=$(printf '%s' "$existing" | jq -r '
                def odd: type != "object" and type != "null";
                .hooks //= {}
                | .hooks.SessionStart //= []
                | .hooks.SessionEnd //= []
                | .hooks
                | select((.SessionStart | type) == "array" and (.SessionEnd | type) == "array")
                | ("SessionStart", "SessionEnd") as $e
                | .[$e]
                | range(length) as $i
                | .[$i]
                | if odd then ".hooks.\($e)[\($i)]"
                  else .hooks | arrays | range(length) as $j
                    | select(.[$j] | odd)
                    | ".hooks.\($e)[\($i)].hooks[\($j)]"
                  end
            ' 2>/dev/null) && [[ -n "$odd_hook_values" ]]; then
            echo "  It is valid JSON, and its hooks hold event lists, but an entry in an" >&2
            echo "  event list, or a hook in an entry's hooks list, is not an object, the" >&2
            echo "  shape Claude Code reads. Not an object:" >&2
            sed 's/^/    /' <<<"$odd_hook_values" >&2
            echo "  Fix it, then re-run this install." >&2
        else
            echo "  It is valid JSON, but not an object whose hooks hold event lists, the" >&2
            echo "  shape Claude Code reads. Fix it, then re-run this install." >&2
        fi
        exit 4
    fi

    # How many entries the merge removed as naming the old directory's binary.
    old_hook_count=$(printf '%s' "$existing" | jq -r --arg oldprefix "$OLD_HOOK_PREFIX" "$OLD_HOOK_OURS_JQ"'
        [(.hooks | objects) as $h | ("SessionStart", "SessionEnd") as $e | $h[$e] | arrays | .[] | select(ours)] | length
    ' 2>/dev/null || echo 0)

    # Backup-before-edit: snapshot the prior settings.json (if any) into a
    # timestamped .bak alongside the original so a regressed jq filter is
    # recoverable. Only the *prior* contents are backed up; in-place
    # re-runs of the install will keep the most recent pre-edit copy.
    if [[ -f "$DEFAULT_SETTINGS_PATH" ]]; then
        backup_settings="${DEFAULT_SETTINGS_PATH}.bak.$(date +%Y%m%d-%H%M%S)"
        ad_backup_keeping_mode "$DEFAULT_SETTINGS_PATH" "$backup_settings"
        echo "  backup  : $backup_settings"
    fi

    # Atomic write: tempfile + mv, keeping an existing file's mode, and
    # writing through a symlinked settings.json to its target.
    ad_replace_keeping_mode "$DEFAULT_SETTINGS_PATH" "$new_settings"

    echo "  hooks   : injected into $DEFAULT_SETTINGS_PATH"
    if [[ "$old_hook_count" =~ ^[0-9]+$ && "$old_hook_count" -gt 0 ]]; then
        echo "  hooks   : removed $old_hook_count hook entries that named the old directory's binary (${OLD_HOOK_PREFIX})"
    fi
fi

# --------------------------------------------------------------------
# inject_help_hook config flag — opt-in dynamic per-Spawn help hook.
#
# Driven by the same Q4 (inject persistent help hooks?) signal: when
# the operator picked "yes" (i.e. did NOT pass --no-hooks),
# agent-director should also tag its own Spawns with a help hook
# regardless of the Spawn's CLAUDE_CONFIG_DIR. install.sh sets the
# flag here; the binary reads it at spawn-synth time.
#
# Q4=no (--no-hooks) leaves config.toml untouched — the flag stays at
# its zero-value default of false.
# --------------------------------------------------------------------

if [[ "$NO_HOOKS" -eq 0 ]]; then
    CONFIG_TOML="${DEFAULT_INSTALL_ROOT}/config.toml"
    if [[ -f "$CONFIG_TOML" ]]; then
        backup_cfg="${CONFIG_TOML}.bak.$(date +%Y%m%d-%H%M%S)"
        ad_backup_keeping_mode "$CONFIG_TOML" "$backup_cfg"
        # awk merge: rewrite an existing inject_help_hook line under
        # [defaults] to =true; if [defaults] exists but lacks the key,
        # append it inside the section; if the file has no [defaults]
        # header, add one at end of file. That added header would define
        # [defaults] a second time in a file that sets defaults as a key
        # before any header (defaults = { ... }), a file agent-director
        # refuses; ad_config_merge_check refused such a file in
        # pre-flight (b.whe). Preserves every other key
        # and section verbatim. Headers are matched as TOML writes them
        # and as ad_store_db_path accepted them above (b.onv): blanks
        # before, inside and after the brackets, a trailing # comment,
        # a CRLF's CR, and a UTF-8 byte-order mark on line 1. A header
        # missed here gets a second [defaults] appended, a file
        # agent-director refuses. ad_store_db_path has refused every
        # other line starting with [, so any such line is a header.
        #
        # The header name and the key are matched regardless of letter
        # case ([Defaults], INJECT_HELP_HOOK), as agent-director's TOML
        # decoder matches them; a spelling missed here gets a second
        # spelling of the key added, a file agent-director refuses
        # (b.hhk). ASCII case is all there is to match: ad_store_db_path
        # has refused every header and key name that is not letters,
        # digits, _ and -, so no ſ (U+017F) or Kelvin sign (U+212A),
        # which the decoder also matches to s and k, reaches here. TOML
        # takes [Defaults] and [defaults] as two tables, which
        # agent-director both reads as [defaults], so the awk reads the
        # file twice: the first pass only notes whether any of them
        # sets the key, and the second rewrites that line or, when none
        # does, adds the key at the end of the first of them. Adding it
        # there in one pass would set the key twice when a later one of
        # them sets it. The store open (step 4) has already stopped the
        # install on a file that sets the key twice.
        merged=$(LC_ALL=C awk '
            BEGIN { pass = 0; has_key = 0 }
            FNR == 1 { pass++; in_defaults = 0; written = has_key }
            { line = $0 }
            FNR == 1 { sub(/^\357\273\277/, "", line) }
            line ~ /^[[:blank:]]*\[/ {
                if (pass == 2 && in_defaults && !written) {
                    print "inject_help_hook = true"
                    written = 1
                }
                in_defaults = (line ~ /^[[:blank:]]*\[[[:blank:]]*[Dd][Ee][Ff][Aa][Uu][Ll][Tt][Ss][[:blank:]]*\][[:space:]]*(#.*)?$/) ? 1 : 0
                if (pass == 2) print
                next
            }
            in_defaults && /^[[:space:]]*[Ii][Nn][Jj][Ee][Cc][Tt]_[Hh][Ee][Ll][Pp]_[Hh][Oo][Oo][Kk][[:space:]]*=/ {
                has_key = 1
                if (pass == 2) print "inject_help_hook = true"
                next
            }
            pass == 2 { print }
            END {
                if (in_defaults && !written) {
                    print "inject_help_hook = true"
                    written = 1
                }
                if (!written) {
                    print ""
                    print "[defaults]"
                    print "inject_help_hook = true"
                }
            }
        ' "$CONFIG_TOML" "$CONFIG_TOML")
        ad_replace_keeping_mode "$CONFIG_TOML" "$merged"
        echo "  config  : merged inject_help_hook=true into $CONFIG_TOML (backup $backup_cfg)"
    else
        printf '[defaults]\ninject_help_hook = true\n' > "$CONFIG_TOML"
        chmod 0600 "$CONFIG_TOML"
        echo "  config  : created $CONFIG_TOML with inject_help_hook=true"
    fi
fi

# --------------------------------------------------------------------
# MCP registration: the optional `mcp add` (--register-mcp) and the check of
# an earlier registration
#
# AD's MCP registration (SR-11.7). On every install, at this step (after the
# store steps, never in pre-flight), Claude Code's `mcp get agent-director`
# runs through "$CLAUDE_PROG" in the directory the install runs in (a `local`
# registration belongs to that directory only). Its outcomes are told apart by
# markers, never by the whole text: the exit status, a `Command:` line, an
# `Args:` line, an `Environment:` block (an `Environment:` line with at least one
# indented KEY=value line under it; counted only for the re-add) and the
# `-s <scope>` in the line that says how to remove the server. `Status:`,
# `Issue:` and `Timeout:` lines are ignored. The marker shapes below are
# derived from SR-11.7's description of the output; SR-16.1 checks them on
# real Claude Code.
#
#   non-zero exit                     none found: nothing changes; one line
#                                     says so; --register-mcp adds as before
#   exit 0, Command: the old binary,  with --register-mcp: remove and add
#     Args: exactly `serve --stdio`,    again in that scope, then no plain
#     no Environment: block             add; without it: both commands
#                                       printed
#   exit 0, Command: in the old       "another form": nothing changes; the
#     directory with other Args or      installer says it could not read the
#     an Environment: block, or no            registration, and prints the commands
#     Command: line, or no scope        that re-point one; --register-mcp
#                                       adds as before
#   exit 0, Command: this install's   nothing changes; with --register-mcp one
#     binary, Args: `serve --stdio`     line says it is already registered and
#                                       no add runs (Claude Code refuses a
#                                       name already registered in its scope)
#   exit 0, any other Command:        nothing changes, nothing printed;
#                                     --register-mcp adds as before
#
# Every add runs as `mcp add agent-director [-s <scope>] -- <path> serve
# --stdio`: the `--` is needed because Claude Code 2.1.280 and later refuses
# `--stdio` after the path (unknown option, exit 1, nothing registered). The
# stderr of every `mcp add` and `mcp remove` the install runs is kept: a
# failure reports the command run, its exit status and its stderr, prints the
# command to run by hand, and the install ends with exit 6 after every other
# step is done.
# --------------------------------------------------------------------

MCP_KIND=""
MCP_SCOPE=""

# ad_mcp_classify — run `mcp get agent-director` and set MCP_KIND (none,
# old_plain, old_other, other, unreadable) and MCP_SCOPE.
#
# The Environment block counts only when at least one indented KEY=value line
# follows the `Environment:` line (have_env). Checked against these capture
# shapes of `claude mcp get` (Claude Code 2.1.280):
#   - a registration added with no -e: prints an `  Environment:` line with
#     nothing under it (the stored env is `{}`); NOT an environment block
#   - a registration added with -e KEY=value: `  Environment:` followed by
#     one indented `    KEY=value` line per variable; an environment block
#   - no `Environment:` line at all (older output): not an environment block
# The block ends at the first line that is not an indented KEY=value (a blank
# line, `To remove this server, run: ...`).
ad_mcp_classify() {
    local out rc=0 line raw cmd="" args="" have_cmd=0 have_env=0 in_env=0
    MCP_KIND=none
    MCP_SCOPE=""
    out="$("$CLAUDE_PROG" mcp get agent-director 2>&1 </dev/null)" || rc=$?
    if [[ "$rc" -ne 0 ]]; then
        return 0
    fi
    while IFS= read -r raw; do
        raw="${raw%$'\r'}"
        line="${raw#"${raw%%[![:space:]]*}"}"
        if [[ "$in_env" -eq 1 ]]; then
            if [[ "$raw" =~ ^[[:space:]]+[A-Za-z_][A-Za-z0-9_]*= ]]; then
                have_env=1
                continue
            fi
            in_env=0
        fi
        case "$line" in
            Command:*)
                have_cmd=1
                cmd="${line#Command:}"
                cmd="${cmd#"${cmd%%[![:space:]]*}"}"
                cmd="${cmd%"${cmd##*[![:space:]]}"}"
                ;;
            Args:*)
                args="${line#Args:}"
                args="${args#"${args%%[![:space:]]*}"}"
                args="${args%"${args##*[![:space:]]}"}"
                ;;
            Environment:*)
                in_env=1
                ;;
            *"mcp remove"*)
                if [[ "$line" =~ [[:space:]]-s[[:space:]]+([A-Za-z]+) ]]; then
                    MCP_SCOPE="${BASH_REMATCH[1]}"
                fi
                ;;
        esac
    done <<<"$out"
    if [[ "$have_cmd" -eq 0 ]]; then
        MCP_KIND=unreadable
    elif [[ "$cmd" == "${OLD_BIN_DIR}/agent-director" && "$args" == "serve --stdio" && "$have_env" -eq 0 && -n "$MCP_SCOPE" ]]; then
        MCP_KIND=old_plain
    elif [[ "$cmd" == "${OLD_INSTALL_ROOT}/"* ]]; then
        MCP_KIND=old_other
    elif [[ "$cmd" == "$CANONICAL" && "$args" == "serve --stdio" ]]; then
        MCP_KIND=current
    else
        MCP_KIND=other
    fi
}

# ad_mcp_print_repoint <scope> — the two commands that re-point a
# registration in <scope>.
ad_mcp_print_repoint() {
    printf '    %q mcp remove agent-director -s %s\n' "$CLAUDE_PROG" "$1"
    ad_mcp_print_add "$1"
}

# ad_mcp_print_add [scope] — the command that adds the registration, in
# <scope> or, with none, in Claude Code's default (local) scope. The `--` ends
# Claude Code's own options: without it Claude Code 2.1.280 and later reads
# `--stdio` as an option of `mcp add` and refuses (error: unknown option
# '--stdio', exit 1, nothing registered). -s goes after the name, before `--`.
ad_mcp_print_add() {
    if [[ -n "${1:-}" ]]; then
        printf '    %q mcp add agent-director -s %s -- %q serve --stdio\n' "$CLAUDE_PROG" "$1" "$CANONICAL"
    else
        printf '    %q mcp add agent-director -- %q serve --stdio\n' "$CLAUDE_PROG" "$CANONICAL"
    fi
}

MCP_RC=0
MCP_ERR=""
MCP_FAILED=0

# ad_mcp_exec <command> [args...] — run the command with stdin closed and
# stdout dropped, and keep its exit status in MCP_RC and its stderr in MCP_ERR.
ad_mcp_exec() {
    MCP_RC=0
    MCP_ERR="$("$@" 2>&1 >/dev/null </dev/null)" || MCP_RC=$?
}

# ad_mcp_report_failure <headline> <command line as run> — report the failed
# command last run through ad_mcp_exec on stderr: the headline, the command
# run, its exit status and its stderr, and mark the install as failed (the
# script exits 6 at its end; every other step is done by then).
ad_mcp_report_failure() {
    local l
    MCP_FAILED=1
    echo "  mcp     : $1" >&2
    echo "    command run: $2" >&2
    echo "    exit status: $MCP_RC" >&2
    echo "    stderr:" >&2
    if [[ -z "$MCP_ERR" ]]; then
        echo "      (none)" >&2
    else
        while IFS= read -r l; do
            printf '      %s\n' "$l" >&2
        done <<<"$MCP_ERR"
    fi
}

ad_mcp_step() {
    local plain_add="$REGISTER_MCP" scope cmdline=""
    if [[ "$CLAUDE_PRESENT" -eq 0 ]]; then
        # No Claude Code: no `mcp get`, no `mcp add`. --register-mcp says it
        # was skipped and the install goes on to success.
        if [[ "$REGISTER_MCP" -eq 1 ]]; then
            echo "  mcp     : --register-mcp skipped because Claude Code is not installed"
        fi
        return 0
    fi
    if command -v "$CLAUDE_PROG" >/dev/null 2>&1; then
        ad_mcp_classify
        scope="${MCP_SCOPE:-<scope>}"
        case "$MCP_KIND" in
            none)
                echo "  mcp     : no agent-director registration found from this directory"
                ;;
            old_plain)
                if [[ "$REGISTER_MCP" -eq 1 ]]; then
                    plain_add=0
                    printf -v cmdline '%q mcp remove agent-director -s %s' "$CLAUDE_PROG" "$MCP_SCOPE"
                    ad_mcp_exec "$CLAUDE_PROG" mcp remove agent-director -s "$MCP_SCOPE"
                    if [[ "$MCP_RC" -ne 0 ]]; then
                        ad_mcp_report_failure "the registration (scope $MCP_SCOPE) names the old directory's binary, and removing it failed; nothing was added. The binaries, the store and the other install steps are done and stay in place. Re-point it by hand:" "$cmdline"
                        ad_mcp_print_repoint "$MCP_SCOPE" >&2
                    else
                        printf -v cmdline '%q mcp add agent-director -s %s -- %q serve --stdio' "$CLAUDE_PROG" "$MCP_SCOPE" "$CANONICAL"
                        ad_mcp_exec "$CLAUDE_PROG" mcp add agent-director -s "$MCP_SCOPE" -- "$CANONICAL" serve --stdio
                        if [[ "$MCP_RC" -eq 0 ]]; then
                            echo "  mcp     : re-registered with claude mcp (scope $MCP_SCOPE): it named the old directory's binary"
                        else
                            ad_mcp_report_failure "the old registration (scope $MCP_SCOPE) was removed, but adding it again failed; nothing is registered now. The binaries, the store and the other install steps are done and stay in place. Add it by hand:" "$cmdline"
                            ad_mcp_print_add "$MCP_SCOPE" >&2
                        fi
                    fi
                else
                    echo "  mcp     : the registration from this directory (scope $MCP_SCOPE) names the old directory's binary, which is kept only until you delete it. Re-point it:"
                    ad_mcp_print_repoint "$MCP_SCOPE"
                fi
                ;;
            old_other|unreadable)
                echo "  mcp     : could not read the registration from this directory as one this installer can re-point (it names the old directory's binary with other arguments or an environment, or its text was not as expected); it was left as it is. To re-point one that names the old directory (use the scope that 'mcp get agent-director' shows, and keep any arguments or environment you set):"
                ad_mcp_print_repoint "$scope"
                ;;
            current)
                # Already registered with this binary: `mcp add` would refuse
                # the name, so a re-run with --register-mcp adds nothing.
                plain_add=0
                if [[ "$REGISTER_MCP" -eq 1 ]]; then
                    echo "  mcp     : already registered with claude mcp for this binary; nothing to add"
                fi
                ;;
            *)
                ;;
        esac
    fi
    if [[ "$plain_add" -eq 1 ]]; then
        printf -v cmdline '%q mcp add agent-director -- %q serve --stdio' "$CLAUDE_PROG" "$CANONICAL"
        ad_mcp_exec "$CLAUDE_PROG" mcp add agent-director -- "$CANONICAL" serve --stdio
        if [[ "$MCP_RC" -eq 0 ]]; then
            echo "  mcp     : registered with claude mcp"
        else
            ad_mcp_report_failure "registration failed; nothing was registered. The binaries, the store and the other install steps are done and stay in place. Register it by hand:" "$cmdline"
            ad_mcp_print_add >&2
        fi
    fi
}

ad_mcp_step

echo "  admin   : $ADMIN_CANONICAL (operator tool, not on PATH; run it only with a human's explicit approval for that run)"
if [[ "$MCP_FAILED" -eq 1 ]]; then
    echo "install.sh: installed, but the MCP registration failed (see above); exit 6" >&2
    exit 6
fi
echo "install.sh: done. Try: $CANONICAL help"
