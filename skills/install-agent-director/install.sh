#!/usr/bin/env bash
# install.sh — install or upgrade agent-director on this machine.
#
# Per SRD §16.2. The script is deliberately bash + jq + standard
# coreutils — no Go, no exotic deps. The install skill invokes
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
#                        byte-identical (no edit, and no config.toml.bak
#                        backup). Default OFF — defaulting to skip would
#                        defeat install.sh's main value over a bare
#                        binary copy.
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
#   --schedule           Schedule recovery and separate expire jobs for the
#                        user running this script, who must be the agents'
#                        user (see "The schedule" below). Default OFF: without
#                        it, an install with no schedule set up ends with a
#                        warning on stderr naming the lines to add.
#   --journal-maintenance-window
#                        Assert that old writers are excluded for the
#                        native-input journal upgrade.
#   --journal-roots-complete
#                        Assert that every legacy input root is accounted for.
#   --journal-backup <dir>
#                        A new absolute directory for the upgrade's
#                        stopped-writer backup.
#   --journal-legacy-root <dir>
#                        An additional absolute legacy input root; repeat for
#                        each. An upgrade of an existing store below schema
#                        v16 needs the first three journal flags, and this one
#                        for each additional root; pre-flight refuses without
#                        the three, changing nothing. See docs/operator-actions.md
#                        "Upgrading the native-input journal".
#   -h, --help           Print this text and exit 0.
#
# The schedule (b.wur). sweep runs shared recovery checks every 2 minutes;
# expire runs separate retention cleanup daily. State reads share the checks;
# absent or failed calls leave observations old. The installed binary's help
# selects sweep when supported, otherwise find-missing, without running either.
# Without --schedule, existing commands, cadence and enablement stay unchanged.
# The install ships the schedule as files in schedule/ beside this script (a crontab
# file, agent-director.cron, and a systemd user .service and .timer for each
# sweep; a note in each says how to copy it by hand), with placeholders for the
# binary's absolute path and for PATH, TMUX_TMPDIR (when set) and HOME (when it
# differs from the passwd home), which cron and systemd do not pass. --schedule installs from those files and nothing
# else, as the user running this script. When the script is piped to bash (no
# schedule/ beside it) it fetches the same files from the repository at the
# release tag (--from-release) or the binaries' full commit stamp, and keeps
# them in ~/.config/agent-director/schedule/ (see the schedule section near
# the end of this script). The install is:
#   - cron, when crontab is available and a cron daemon runs (a process named
#     cron or crond; checked on Linux only): the lines are added to the user's
#     crontab, each tagged with the marker comment `# agent-director-schedule`;
#     a re-run replaces the marked lines, so one copy of each stays, and
#     every other crontab line is kept;
#   - else systemd, when `systemctl --user` reaches the user manager: a
#     .service and .timer per sweep in ~/.config/systemd/user, both timers
#     enabled; a re-run rewrites them (one copy);
#   - else nothing: the warning below, with the reason. (crontab working but no
#     cron daemon running, and no systemd: the crontab lines are put in, the
#     install says on stderr that no daemon runs, and the warning fires.)
# The entries run the canonical binary, ~/.config/agent-director/bin/
# agent-director, so an upgrade needs no change; a re-run of --schedule
# re-points them at the binary installed now. uninstall.sh removes exactly the
# marked lines or the units this adds. Whenever neither this install's cron
# lines (with a cron daemon running) nor its units are present after the run,
# the install ends with a warning on stderr naming the exact lines (the cron
# file's, with the installed binary's path), saying to run them as the agents'
# user and naming the directory the files are in (beside the script, or the
# kept copy under ~/.config/agent-director/schedule/). It also prints this
# script's own command line with --schedule added, to run as it is. Exit codes
# are not changed by it or by --schedule failing.
# Run the sweeps as the agents' user, in their tmux environment: run as
# another user, as root or against another tmux server, they can wrongly mark
# live rows missing or delete rows whose agent still runs.
#
# Exit codes:
#   0  success
#   1  a write of the agent-rename migration failed after the binaries were
#      installed (a full disk, or permissions changed since pre-flight): see
#      "The agent-rename migration" below. The file is as it was, every copy
#      is kept, and the store is not opened.
#   2  pre-flight failure (required external commands missing, no
#      Claude Code, Codex or Cursor found (see "Agents" below), whitespace in path,
#      a bad flag, only one of --sha256 and --admin-sha256, or HOME unset,
#      empty or not an absolute path: checked first, before anything is read
#      or written; run it with HOME set to the user's home directory)
#   3  binary source not found / not executable (including a
#      --from-release release before 0.11.0, which has no
#      agent-director-admin binary), or the two binaries' version
#      stamps differ or carry no commit stamp, or a binary's `version`
#      output lacks config_dir or names a directory other than
#      ~/.config/agent-director (a binary from an earlier release: install
#      that release with its own install.sh (that release's install.sh must support this host), fetched from its tag; this
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
#      busy_timeout_ms or [launch] claude_path, codex_path or cursor_path install.sh
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
#      before anything on disk changes). A config.toml or template that
#      changes after pre-flight and that the migration then refuses is
#      refused by the migration step instead, after the binaries are
#      installed and before the store is opened: it prints the refusal, the
#      move's references (when a move ran) and exit 1's two ways on, then ends
#      with exit 5 and ErrConfigMalformed or ErrTemplateMalformed. state.db here is
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
#      An upgrade that crosses the native-input journal cutover adds three:
#      ErrJournalMigrationRefused (pre-flight refused, or the cutover
#      refused and the installer put the previous install back: the install
#      is as before the run; fix the reported cause, then re-run with a new
#      --journal-backup directory), ErrJournalMigrationIncomplete (the
#      install did not finish successfully, though the cutover may have
#      committed, and this release stays installed; follow the
#      message, then docs/operator-actions.md "install.sh stops with
#      ErrJournalMigrationIncomplete") and ErrSchemaVerifyFailed for a
#      restore that could not finish (needs a human; put the listed copies
#      back as docs/operator-actions.md "Putting back a kept install"
#      describes). ErrJournalMigrationIncomplete is install.sh's own name.
#   6  the MCP registration failed (--register-mcp): `claude mcp add` (or the
#      `claude mcp remove` before the re-add of a registration that names the
#      old directory's binary) exited non-zero. Every other step is done: the
#      binaries, the store, the hooks and the links are installed and stay.
#      stderr gets the command run, its exit status, its stderr and the
#      command to run by hand, e.g. `claude mcp add agent-director -- <path>
#      serve --stdio`; the install's last line says so. The check of an earlier
#      registration (`mcp get`) never fails the install.
#
# Agents (SR-11.1). agent-director launches Claude Code, Codex and Cursor agents,
# and an install needs at least one of them. Each agent is present when its
# launch path ([launch] claude_path, codex_path or cursor_path in config.toml, read in
# pre-flight) names an existing executable file, or its program name (claude,
# codex, agent) is on PATH; install.sh only looks, and writes no [launch] key.
# With none present the install stops, exit 2, naming all three agents, their
# [launch] keys and how to install one; nothing is changed. The Claude
# Code program is claude_path when it names an executable, else `claude` from
# PATH; every Claude Code command this script runs (the summary's --version,
# `mcp get`, `mcp add`, `mcp remove`) goes through it. Codex is never run: the
# summary prints its program's path, and says it did not run it, because
# `codex --version` writes files in the Codex folder; install.sh registers
# nothing with Codex and writes nothing in any Codex folder. Cursor is never
# run: standalone agent presence proves no version or compatibility. With Claude Code
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
# (docs/operator-actions.md). A file that changed since pre-flight and that the
# migration then refuses stops the install the same way, with exit 5 and
# ErrConfigMalformed or ErrTemplateMalformed. With no store, or a store at or above that
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
# The hooks merge rewrites settings.json with no backup copy, as its env can
# hold tokens (b.f3n). Each install that gets past the hooks step names, on
# one line and each shell-quoted, the
# ~/.claude/settings.json.bak.<YYYYmmdd-HHMMSS> copies earlier releases kept
# (they may hold those tokens and can be deleted), and never deletes them
# itself.
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
# added twice), config.toml after a fresh timestamped .bak. With
# --register-mcp, `claude mcp add` refuses a name already registered in
# its scope, so a re-run from a directory where `mcp get agent-director`
# already shows this install's binary with `serve --stdio` adds nothing, says
# so, and still returns 0. With --schedule, the entries are replaced, not
# added (see "The schedule" above): a re-run leaves one copy of each.

# Bash parses this whole block before running it (SR-2.16). A piped install
# has all its code before its first change, even if an interrupt ends curl
# in the same process group; a download cut off mid-block runs nothing.
# The body is deliberately not indented.
{
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
SCHEDULE=0
JOURNAL_MAINTENANCE=0
JOURNAL_ROOTS_COMPLETE=0
JOURNAL_ROOTS=()
JOURNAL_BACKUP=""

# GitHub repo slug used by --from-release. Matches go.mod's module path
# and the /release skill's asset naming.
readonly RELEASE_REPO_SLUG="gabemahoney/agent-director"

# Pick a sensible default symlink dir: ~/.local/bin if on PATH.
if [[ ":${PATH}:" == *":${HOME}/.local/bin:"* ]]; then
    SYMLINK_DEFAULT="${HOME}/.local/bin"
fi

# The arguments as given, for the schedule warning's re-run advice (b.wur).
ORIG_ARGS=("$@")
ORIG_PWD="$PWD"

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
        --journal-maintenance-window)
            JOURNAL_MAINTENANCE=1; shift ;;
        --journal-roots-complete)
            JOURNAL_ROOTS_COMPLETE=1; shift ;;
        --journal-legacy-root)
            JOURNAL_ROOTS+=("$2"); shift 2 ;;
        --journal-backup)
            JOURNAL_BACKUP="$2"; shift 2 ;;
        --schedule)
            SCHEDULE=1; shift ;;
        -h|--help)
            # Help must also work before any external command is available.
            { IFS= read -r help_line || true
              while IFS= read -r help_line; do
                  [[ -n "$help_line" ]] || break
                  help_line="${help_line#\#}"
                  printf '%s\n' "${help_line# }"
              done
            } <"$0"
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
#                         could not create the sentinel's temp file; or a
#                         cutover restore could not finish: needs a human.
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
#   ErrJournalMigrationRefused  the native-input cutover was refused: in
#                         pre-flight (the journal flags, or the store's
#                         cutover state) with nothing changed, or by
#                         step 4's migrate-journal with the install restored;
#                         the envelope above the
#                         cause line gives the reason and action.
#   ErrJournalMigrationIncomplete  a failed or uncertain cutover left this
#                         release installed: complete forward as reported.
# ErrVersionUnreadable, ErrSchemaVerifyFailed, ErrMoveConflict,
# ErrAgentsLive, ErrMoveFailed and ErrJournalMigrationIncomplete are install.sh's own names, not
# agent-director error names.
#
# Exit 1 is the agent-rename migration's failed write (see the header): it
# ends with no err_name line.
# --------------------------------------------------------------------

# ad_exit_5 <err_name> — print exit 5's cause line naming <err_name> on
# stderr, then exit 5.
ad_exit_5() {
    echo "install.sh: err_name=$1" >&2 || true
    # An outcome can be reported from inside EXIT (bash 3.2's errexit
    # ordering). In that case exit will not run the trap's remaining code.
    if [[ -n "${CUTOVER_PHASE:-}" ]]; then
        ad_exit_cleanup
    fi
    exit 5
}

# SR-2.16: a closed output stream must not interrupt cutover settlement.
# Shared reporters also run in pre-flight, so define this before their use;
# outside the cutover span a failed write keeps its ordinary failure.
ad_cutover_write_failed() {
    [[ -n "${CUTOVER_PHASE:-}" ]]
}

# ad_outcome_exit [--relayed] <err_name> <reason> <action> <cutover>
# [<extra-details-json>] — write install.sh's own outcome envelope as one
# compact JSON line on stderr, then exit 5 with <err_name>'s cause line, so
# the envelope and the cause line cannot name different errors:
#   {"err_name":<err_name>,"err_description":"install.sh: <reason>: <action>",
#    "details":{"refusal":{"reason":<reason>,"processes":[],"action":<action>},
#               "cutover":<cutover>} + <extra-details-json>}
# Every outcome envelope install.sh writes itself goes through this helper:
# Tasks t2.7tz.ab.hh, t2.7tz.ab.p5 and t2.7tz.ab.5w reuse it with their own
# err_names, cutover values and extra fields (copies, attempt_store, remove).
# Without --relayed (accepted only first), <reason> is one install.sh chose
# itself and must be a word of AD_OUTCOME_REASONS; otherwise this writes no
# envelope and no cause line and exits 1 with an internal error, so a test
# expecting the outcome fails. --relayed marks a reason taken from a relayed
# envelope (its details.refusal.reason or its err_name), never checked.
# Print English message lines before calling it, so the envelope sits just
# above the cause line. A failed envelope write never skips the cause line.
# ad_exit_5 also performs the span's cleanup before exiting, including when
# this outcome was reached from the EXIT trap itself.
ad_outcome_exit() {
    local relayed=0 extra='{}'
    if [[ "${1:-}" == --relayed ]]; then
        relayed=1
        shift
    fi
    local err_name="$1" reason="$2" action="$3" cutover="$4"
    if [[ $# -ge 5 ]]; then
        extra="$5"
    fi
    if [[ "$relayed" -eq 0 ]] && { [[ ! "$reason" =~ ^[a-z0-9_]+$ ]] || [[ " $AD_OUTCOME_REASONS " != *" $reason "* ]]; }; then
        echo "install.sh: internal error: outcome reason $reason is not in AD_OUTCOME_REASONS" >&2 || ad_cutover_write_failed
        exit 1
    fi
    jq -nc --arg err_name "$err_name" --arg reason "$reason" --arg action "$action" \
        --arg cutover "$cutover" --argjson extra "$extra" \
        '{err_name: $err_name, err_description: ("install.sh: " + $reason + ": " + $action),
          details: ({refusal: {reason: $reason, processes: [], action: $action}, cutover: $cutover} + $extra)}' >&2 || true
    ad_exit_5 "$err_name"
}

# The copies a restore needs (SR-2.16), which pre-flight makes on a crossing
# upgrade ("Restore preconditions", below). One entry per item, in this
# order: the binary, the administrator, the sentinel, the managed PATH link.
# RESTORE_BELONGS[i] is where the item belongs; RESTORE_COPY[i] is its copy,
# <what it copies>.restore-<RESTORE_TS>.<suffix> beside it (for a link, a
# directory holding the link under the item's last component; see
# ad_restore_copy), or empty when nothing was there before the run.
# RESTORE_KEEP_COPIES keeps remaining copies after an unfinished restore or
# an unestablished outcome; otherwise EXIT cleanup removes them.
RESTORE_TS=""
RESTORE_BELONGS=()
RESTORE_COPY=()
RESTORE_KEEP_COPIES=0
RESTORE_NOT_REMOVED=()
AD_EXIT_CLEANUP_DONE=0

# ad_restore_copies_remove — remove each copy this run made, by its recorded
# name only, so another run's copies are never touched. A link's copy is its
# mktemp -d directory: the link inside it, named as the item, is removed
# (never its target) only while it is a link, then the directory only while
# it is empty. Returns 1, listing each copy it could not remove in
# RESTORE_NOT_REMOVED, when one stays.
ad_restore_copies_remove() {
    local i c inner rc=0
    RESTORE_NOT_REMOVED=()
    for ((i = 0; i < ${#RESTORE_COPY[@]}; i++)); do
        c="${RESTORE_COPY[$i]}"
        if [[ -z "$c" ]]; then
            continue
        fi
        if [[ -d "$c" && ! -L "$c" ]]; then
            inner="$c/${RESTORE_BELONGS[$i]##*/}"
            if [[ -L "$inner" ]]; then
                rm -f -- "$inner" 2>/dev/null || true
            fi
            if ! rmdir -- "$c" 2>/dev/null; then
                RESTORE_NOT_REMOVED+=("$c")
                rc=1
            fi
        elif ! rm -f -- "$c" 2>/dev/null; then
            RESTORE_NOT_REMOVED+=("$c")
            rc=1
        fi
    done
    return "$rc"
}

# ad_exit_cleanup — the install's EXIT cleanup: remove every temp file this
# install made and has not moved into place (the staged binary copies, the
# --from-release downloads, the binary and admin temp copies, a migration
# sentinel's temp file that step 3 never moved into place, and a migration
# write's temp file and the managed link's temp link), and the restore's
# copies unless RESTORE_KEEP_COPIES is 1: after a success, a refusal, a
# post-swap failure and a failed move alike.
# A SIGKILL leaves the copies, under their documented name pattern. Every
# EXIT trap of this script runs it, the move's too (ad_move_on_exit), so the
# move's traps chain onto it rather than replace it.
# Before removing copies, settle a before-start interrupt even when bash
# exits on errexit before running the pending signal trap (bash 3.2).
ad_exit_cleanup() {
    local exit_rc=$?
    local f
    [[ "$AD_EXIT_CLEANUP_DONE" -eq 1 ]] && return 0
    if [[ "${CUTOVER_PHASE:-}" == before_start ]]; then
        if [[ -n "$CUTOVER_INTERRUPTED" || "$exit_rc" -eq 129 || "$exit_rc" -eq 130 || "$exit_rc" -eq 143 ]]; then
            if [[ -z "$CUTOVER_INTERRUPTED" ]]; then
                case "$exit_rc" in
                    129) CUTOVER_INTERRUPTED=HUP ;;
                    130) CUTOVER_INTERRUPTED=INT ;;
                    143) CUTOVER_INTERRUPTED=TERM ;;
                esac
            fi
            CUTOVER_INTERRUPTED_IN=before_start
            ad_cutover_interrupted_before_start
        fi
    fi
    # S1-S3 and unexpected exits keep the notes due by that point. Inline:
    # EXIT can run before the later helpers have been defined.
    if [[ -n "${MOVE_NOTES_DEFERRED:-}" ]]; then
        printf '%s' "$MOVE_NOTES_DEFERRED" 2>/dev/null || true
    fi
    for f in "${TMP:-}" "${ADMIN_TMP:-}" "${tmp_bin:-}" "${tmp_admin:-}" \
        "${tmp_sentinel:-}" "${stage_bin:-}" "${stage_admin:-}" "${migrate_tmp:-}" "${link_tmp:-}"; do
        if [[ -n "$f" ]]; then
            rm -f "$f"
        fi
    done
    if [[ "$RESTORE_KEEP_COPIES" -ne 1 ]]; then
        ad_restore_copies_remove || true
    fi
    # The schedule step's download directory (b.wur), when an interrupt left it.
    if [[ -n "${sched_tmp:-}" ]]; then
        rm -rf -- "$sched_tmp"
    fi
    AD_EXIT_CLEANUP_DONE=1
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

# tmux must be on PATH (Claude Code, Codex and Cursor are looked for after the
# [launch] reader, below: at least one of them is needed). `file` is required for the --binary
# architecture probe (SR-2.2) — hard requirement; never silent-skip.
# `sqlite3` is required for the schema-migration flow: it reads the DB's
# ACTUAL user_version through the WAL (raw header bytes are subtly wrong
# for a WAL-mode DB) both to decide whether a migration sentinel is
# needed and to verify the post-open version. Hard requirement — never
# silent-skip; a bad read would either skip a needed migration or pass a
# broken install.
# Standard utilities cover the install, migration, rollback and diagnostics.
# Only shell builtins may run before this whole inventory has been checked.
# Keep optional probes (git, ps/lsof, scheduler and passwd tools) optional.
required_tools=(tmux jq file sqlite3 uname basename cat chmod cmp cp date dirname
    find grep head id ln mkdir mktemp mv od readlink rm rmdir sed stat tail tr)
if [[ "$NO_HOOKS" -eq 0 || -n "$SHA256_EXPECTED" ]]; then
    required_tools+=(awk)
fi
if [[ "$FROM_RELEASE" -eq 1 ]]; then
    required_tools+=(curl sleep)
fi
missing_tools=()
for tool in "${required_tools[@]}"; do
    if ! command -v "$tool" >/dev/null 2>&1; then
        missing_tools+=("$tool")
    fi
done
if [[ -n "$SHA256_EXPECTED" ]] && ! command -v sha256sum >/dev/null 2>&1 \
    && ! command -v shasum >/dev/null 2>&1; then
    missing_tools+=("sha256sum or shasum")
fi
if [[ ${#missing_tools[@]} -gt 0 ]]; then
    echo "install.sh: required tools not found on PATH:" >&2
    for tool in "${missing_tools[@]}"; do
        printf '  %s\n' "$tool" >&2
        case "$tool" in
            tmux)    echo "  Install tmux via your package manager (apt/brew/dnf/etc.)." >&2 ;;
            jq)      echo "  Install jq via your package manager (we use it to safely edit settings.json)." >&2 ;;
            file)    echo "  Install file via your package manager (apt install file / brew install file-formula / dnf install file). Required for the --binary architecture probe." >&2 ;;
            sqlite3) echo "  Install sqlite3 via your package manager (apt install sqlite3 / brew install sqlite / dnf install sqlite). Required to read state.db's schema version for the migration flow." >&2 ;;
            curl)    echo "  --from-release downloads via curl; install it via your package manager." >&2 ;;
            awk)     echo "  Install an awk package (apt install mawk / brew install gawk / dnf install gawk)." >&2 ;;
            grep|sed) echo "  Install $tool via your package manager (apt/dnf install $tool; macOS supplies it)." >&2 ;;
            cmp)     echo "  Install diffutils via your package manager (apt/dnf install diffutils; macOS supplies cmp)." >&2 ;;
            find)    echo "  Install findutils via your package manager (apt/dnf install findutils; macOS supplies find)." >&2 ;;
            "sha256sum or shasum") echo "  Checksum verification needs either sha256sum (coreutils package) or shasum (Perl / libdigest-sha-perl package)." >&2 ;;
            *)       echo "  Install $tool from the coreutils package (apt/brew/dnf install coreutils; macOS supplies the standard utility)." >&2 ;;
        esac
    done
    exit 2
fi

# SRD §SR-2.1 / Idea Bee b.fg3: refuse with exit 2 any host outside the
# supported set {Linux/x86_64, Linux/aarch64, Darwin/arm64}, before anything is
# downloaded or written. install.sh is the only installer (the npm package installs nothing
# outside node_modules/), so this is the only host check that can refuse
# an install. ad_arch_probe (below) checks both source binaries against
# the pair read here.
uname_s="$(uname -s)"
uname_m="$(uname -m)"
case "${uname_s}/${uname_m}" in
    Linux/x86_64|Linux/aarch64|Darwin/arm64)
        ;;
    *)
        echo "install.sh: unsupported host: ${uname_s}/${uname_m}. Supported: Linux/x86_64, Linux/aarch64, Darwin/arm64. See b.fg3 for cross-platform expansion status." >&2
        exit 2
        ;;
esac

# The one Claude Code program (SR-11.1): every Claude Code command this
# script runs (the summary's version line, `mcp get`, `mcp add` and `mcp
# remove`) goes through "$CLAUDE_PROG". It is `claude` (found on PATH) until
# the agent check below, after the [launch] reader, sets it to claude_path
# when that names an executable file. CLAUDE_PRESENT is 0 when Claude Code is
# found neither by claude_path nor on PATH; no Claude Code command runs then.
CLAUDE_PROG=claude
CLAUDE_PRESENT=0
CODEX_PRESENT=0
CURSOR_PRESENT=0

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
# codex_path and cursor_path (SR-5.0, SR-11.1), in the same line form and with the same
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
# the base directory. Keys under [launch] other than these three are ignored.
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
            echo "  install.sh reads [launch] claude_path, codex_path and cursor_path itself, to choose the" >&2
            echo "  Claude Code program it runs and to see which agents are installed, and" >&2
            echo "  reads only this form of the file: blank lines, # comments, [name]" >&2
            echo "  headers and name = value lines, a name being letters, digits, _ and -," >&2
            echo "  with claude_path, codex_path and cursor_path under [launch] set to a one-line \"...\"" >&2
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
#   launch  [launch] claude_path, then codex_path and cursor_path, resolved as above: each
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
    local have_claude=0 have_codex=0 have_cursor=0 claude_value="" codex_value="" cursor_value=""
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
    local re_cursor='^[Cc][Uu][Rr][Ss][Oo][Rr]_[Pp][Aa][Tt][Hh]$'
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
    # The launch table holds three keys, so is_key takes any of them there,
    # and key_of names the one the (dynamically scoped) name holds, claude_path
    # as the default; key_of also takes claude, codex and cursor, the three
    # launch keys by their agent name.
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
            launch) [[ "$1" =~ $re_claude || "$1" =~ $re_codex || "$1" =~ $re_cursor ]] ;;
        esac
    }
    key_of() {
        case "$1" in
            store) k=db_path ;;
            log) k=error_log_path ;;
            claude) k=claude_path ;;
            codex) k=codex_path ;;
            cursor) k=cursor_path ;;
            launch)
                if [[ "${name:-}" =~ $re_cursor ]]; then k=cursor_path
                elif [[ "${name:-}" =~ $re_codex ]]; then k=codex_path
                else k=claude_path; fi
                ;;
        esac
    }
    # set_about <table>: the refusal that follows is about [launch] when <table>
    # is launch (its headline and closing note then name [launch]).
    set_about() {
        if [[ "$1" == launch || "$1" == claude || "$1" == codex || "$1" == cursor ]]; then
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
            kdesc="claude_path, codex_path or cursor_path"
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
                if [[ "${line:2}" =~ $re_header ]]; then
                    name="${BASH_REMATCH[1]}"
                    for t in "${tables[@]}"; do
                        if [[ "$t" == launch ]] && is_table "$name" "$t"; then
                            set_about "$t"
                            break
                        fi
                    done
                fi
                ad_db_path_refuse "$config" "$n" "$line" \
                    "This line starts with the bytes FF FE or FE FF, a UTF-16 byte-order" \
                    "mark, which install.sh does not read. Remove those two bytes:" \
                    "agent-director skips them, so it reads the file the same without them."
            fi
            if [[ "$line" == *'"""'* || "$line" == *"'''"* ]]; then
                set_about "$table"
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
                    name="$(ad_toml_name "${BASH_REMATCH[1]}")"
                    for t in "${tables[@]}"; do
                        if is_table "$name" "$t"; then
                            set_about "$t"
                            break
                        fi
                    done
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
                    set_about "$table"
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
                elif [[ "$key" =~ $re_cursor ]]; then
                    which=cursor
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
                cursor) seen=$have_cursor ;;
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
                            "starting with ~/, or remove this line to launch ${which/cursor/agent} by its name" \
                            "on the PATH; an empty \"\" counts as unset."
                        ;;
                esac
                if [[ "$which" == claude ]]; then
                    claude_value="$parsed"
                    have_claude=1
                elif [[ "$which" == cursor ]]; then
                    cursor_value="$parsed"
                    have_cursor=1
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
        for value in "$claude_value" "$codex_value" "$cursor_value"; do
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

# The native-input cutover (SR-2.14 "Pre-flight"). INPUT_CUTOVER_VERSION is
# the user_version internal/store's cutover step (v15 to v16) produces: a
# store below it, above v0, crosses the cutover. STORE_SCHEMA_VERSION is this
# release's schema version (internal/store's schemaVersion), which no verb
# prints before the swap. A Go test fails when either differs from
# internal/store, so keep each assignment on a line of its own, in this form.
INPUT_CUTOVER_VERSION=16
STORE_SCHEMA_VERSION=20

# AD_OUTCOME_REASONS lists the reasons install.sh chooses itself for an
# outcome envelope (ad_outcome_exit), never a relayed one. Tasks
# t2.7tz.ab.hh, t2.7tz.ab.p5 and t2.7tz.ab.5w append their own installer
# reasons to this one line. A test reads it, so keep it on one line in this
# form, and ad_outcome_exit refuses a self-chosen reason missing from it.
AD_OUTCOME_REASONS='cutover_state_unreadable native_input_authority_missing check_only_unestablished previous_release_unknown restore_copies_unavailable backup_inside_install_directory outcome_unestablished restore_incomplete interrupted legacy_root_symlink agents_live agent_rows_unreadable'

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

# The store's place and the error log's place, while the move is due.
STORE_PHYS=""
STORE_CLASS=""
LOG_PHYS=""
LOG_CLASS=""
old_error_log=""
# [launch] claude_path, codex_path and cursor_path as the reader resolved them ("" when
# unset), read with the store's place from the same file (SR-11.1): the old
# directory's while the move is due, the new one's otherwise. The move
# rewrites only db_path and error_log_path, so these stay true after it.
LAUNCH_CLAUDE_PATH=""
LAUNCH_CODEX_PATH=""
LAUNCH_CURSOR_PATH=""
if [[ "$MOVE_DUE" -eq 1 ]]; then
    # While the move is due the reader also reads [log] error_log_path. Its
    # lines come by position (store, error log, claude_path, codex_path, cursor_path), an
    # empty one included, which command substitution would drop at the end.
    if ! reader_out="$(ad_store_db_path "$PREFLIGHT_CONFIG" "$HOME" "$PREFLIGHT_ROOT" log launch)"; then
        ad_exit_5 ErrConfigMalformed
    fi
    {
        IFS= read -r state_db || true
        IFS= read -r old_error_log || true
        IFS= read -r LAUNCH_CLAUDE_PATH || true
        IFS= read -r LAUNCH_CODEX_PATH || true
        IFS= read -r LAUNCH_CURSOR_PATH || true
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
else
    if ! reader_out="$(ad_store_db_path "$PREFLIGHT_CONFIG" "$HOME" "$PREFLIGHT_ROOT" launch)"; then
        ad_exit_5 ErrConfigMalformed
    fi
    {
        IFS= read -r state_db || true
        IFS= read -r LAUNCH_CLAUDE_PATH || true
        IFS= read -r LAUNCH_CODEX_PATH || true
        IFS= read -r LAUNCH_CURSOR_PATH || true
    } <<<"$reader_out"
fi
# Messages name the store "state.db" when it is the default one, as
# before, and by its path when [store] db_path moves it.
state_db_name="state.db"
if [[ "$state_db" != "$(ad_clean_path "${PREFLIGHT_ROOT}/state.db")" ]]; then
    state_db_name="$state_db"
fi

# --------------------------------------------------------------------
# Agents: Claude Code, Codex and Cursor (SR-11.1)
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
# <name> (claude, codex, agent) and whose [launch] path is <launch path> ("" when
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
ad_find_agent agent "$LAUNCH_CURSOR_PATH"
CURSOR_BAD="$agent_bad"
CURSOR_FOUND_PATH="$agent_path"
if [[ -n "$agent_prog" ]]; then
    CURSOR_PRESENT=1
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

# ad_refuse_no_agent — stop the install (exit 2) when no supported agent was found.
# Called after the last pre-flight config read and link check below, so every
# config-form refusal (exit 5) comes first. The advice names PREFLIGHT_CONFIG,
# the file pre-flight read (the old directory's while the move is due).
ad_refuse_no_agent() {
    if [[ "$CLAUDE_PRESENT" -eq 1 || "$CODEX_PRESENT" -eq 1 || "$CURSOR_PRESENT" -eq 1 ]]; then
        return 0
    fi
    echo "install.sh: no supported agent (Claude Code, Codex or Cursor) was found; refusing to install." >&2
    echo "  Claude Code: $(ad_agent_absent_reason claude claude_path "$LAUNCH_CLAUDE_PATH" "$CLAUDE_BAD")" >&2
    echo "  Codex      : $(ad_agent_absent_reason codex codex_path "$LAUNCH_CODEX_PATH" "$CODEX_BAD")" >&2
    echo "  Cursor     : $(ad_agent_absent_reason agent cursor_path "$LAUNCH_CURSOR_PATH" "$CURSOR_BAD")" >&2
    echo "  agent-director launches Claude Code, Codex and Cursor agents and needs at least one" >&2
    echo "  of them. Install one, then re-run this install:" >&2
    echo "    Claude Code: https://claude.com/claude-code, with claude on PATH, or its" >&2
    echo "      absolute path as claude_path under [launch] in ${PREFLIGHT_CONFIG}." >&2
    echo "    Codex: see Requirements in docs/install.md" >&2
    echo "      (https://github.com/${RELEASE_REPO_SLUG}/blob/main/docs/install.md), with codex on PATH," >&2
    echo "      or its absolute path as codex_path under [launch] in ${PREFLIGHT_CONFIG}." >&2
    echo "    Cursor: install the standalone agent program (https://cursor.com/install)," >&2
    echo "      with agent on PATH, or its absolute path as cursor_path under [launch]" >&2
    echo "      in ${PREFLIGHT_CONFIG}. The Cursor editor's cursor command does not count." >&2
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
    if [[ "$CURSOR_PRESENT" -eq 1 ]]; then
        if [[ "$CURSOR_BAD" -eq 1 ]]; then
            echo "  cursor  : [launch] cursor_path = \"$LAUNCH_CURSOR_PATH\" names no executable file; agent found on PATH for installation only (configured launch path unchanged)"
        fi
        echo "  cursor  : $CURSOR_FOUND_PATH (presence only; version and compatibility not checked; agent was not run)"
    else
        echo "  cursor  : not installed ($(ad_agent_absent_reason agent cursor_path "$LAUNCH_CURSOR_PATH" "$CURSOR_BAD"))"
    fi
}

# --------------------------------------------------------------------
# Store busy timeout: [store] busy_timeout_ms (b.c7f)
#
# Every agent-director connection to the store waits up to [store]
# busy_timeout_ms in ~/.config/agent-director/config.toml for a lock another
# connection holds (SQLite's busy timeout; internal/config
# Store.EffectiveBusyTimeoutMs). install.sh's sqlite3 reads of the store's
# user_version (ad_user_version after the swap, ad_store_state on
# pre-flight's snapshot) wait the same time, and so does the check
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

SOURCE_BINARIES_READY=0
ad_prepare_source_binaries() {
    [[ "$SOURCE_BINARIES_READY" -eq 0 ]] || return 0
# --------------------------------------------------------------------
# Source tree, and the build advice that names it
# --------------------------------------------------------------------

# SCRIPT_REAL is 1 when this script is a file on disk. Read from a pipe (`curl
# ... | bash -s -- ...`) or a process substitution there is no BASH_SOURCE[0]
# (or it is not a regular file), SCRIPT_DIR is then only the working
# directory, and nothing is shipped beside the script (the schedule step
# fetches its files, see "The schedule").
SCRIPT_REAL=0
if [[ -f "${BASH_SOURCE[0]:-}" ]]; then
    SCRIPT_REAL=1
fi
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]:-.}")" && pwd)"

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

    tmp_bin="$(mktemp /tmp/agent-director.XXXXXX)"
    tmp_admin="$(mktemp /tmp/agent-director-admin.XXXXXX)"
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
        echo "    - or install an earlier release with that release's own install.sh (that release's install.sh must support this host), fetched from its tag, rather than this one" >&2
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
# Supported pairs: Linux/x86_64, Linux/aarch64 and Darwin/arm64.
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
        Linux/aarch64)
            if grep -q "ELF 64-bit LSB" <<<"$file_out" \
                && grep -q "aarch64" <<<"$file_out"; then
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
    echo "  release's own install.sh installs (that release's install.sh must support this host): fetch that release's install.sh from its" >&2
    echo "  tag and run it, rather than this one. To install this release instead:" >&2
    echo "    - build both binaries from it: $build_advice" >&2
    echo "    - or download release:        rerun with --from-release (omit --binary and --admin-binary)" >&2
    exit 3
fi

    SOURCE_BINARIES_READY=1
}

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
# The version is read through a private locked snapshot (ad_store_state),
# including committed WAL frames. Even sqlite3 -readonly can create source
# WAL/SHM files, so no preflight SQLite connection opens the original; only
# the private copy is opened, read-write (ad_snapshot_sqlite says why). The
# candidate binaries are resolved and validated first to provide the snapshot
# helper, with the configured busy timeout. A store whose version cannot be
# read stops the install here, with exit 5 and ErrVersionUnreadable, before
# anything changes.
# --------------------------------------------------------------------

# ad_readonly_query <db> <SQL> [sqlite3 options...] — run <SQL> on a
# private, lock-protected snapshot of <db> (ad_with_store_snapshot), which
# is only read, and never by SQLite.
ad_readonly_query() {
    local db="$1" sql="$2"
    shift 2
    ad_with_store_snapshot "$db" ad_snapshot_sqlite "$sql" "$@"
}

# ad_with_store_snapshot <db> <reader> [args...] — the one snapshot
# implementation behind ad_readonly_query and ad_store_state: copy <db> into
# a private 0700 directory with the candidate's snapshot-store, run
# `<reader> <copy> [args...]` on the copy, and remove the directory. A
# failed snapshot prints its output and returns its status.
#
# No SQLite connection opens <db>, including the user_version, table-info
# and live-rows preflight reads: snapshot-store reads it only through
# O_RDONLY descriptors under read locks, copying state.db and any -wal (mode
# 0600; -shm and -journal are never copied) into the private directory.
# The admin source is resolved and validated before entering command
# substitutions; no new system utility or installed helper is required.
ad_with_store_snapshot() (
    local db="$1" reader="$2" tmpd snapshot_err rc=0
    shift 2
    # Use the system temp directory even if TMPDIR names the old install.
    tmpd="$(umask 077; mktemp -d "/tmp/agent-director.read.XXXXXX")" || return 1
    trap 'rm -rf -- "$tmpd"' EXIT
    snapshot_err="$("$ADMIN_SRC" --store-path "$db" snapshot-store --directory "$tmpd" --busy-timeout-ms "$store_busy_timeout_ms" 2>&1)" || rc=$?
    if [[ "$rc" -ne 0 ]]; then
        printf '%s\n' "$snapshot_err"
        return "$rc"
    fi
    "$reader" "$tmpd/state.db" "$@"
)

# ad_snapshot_sqlite <copy> <SQL> [sqlite3 options...] — run <SQL> on a
# snapshot copy, sqlite3's stderr joined to its stdout, with ad_user_version's
# busy timeout, -init /dev/null and -batch (its comment says why).
#
# The copy is opened through a read-write connection: a WAL-mode store's
# snapshot has no -shm. With no -wal either, Apple's sqlite3 3.51.0 cannot
# open it read-only (error 14), as ad_move_backup_store's comment explains
# for its own copy (b.up4); with a copied -wal the same failure is likely
# but unconfirmed, so every snapshot read uses the read-write connection.
# Only the copy is touched:
#   - the connection opens a copied -wal, creates only the -shm and any -wal
#     that is absent, and writes any checkpoint, all inside the private
#     directory, which ad_with_store_snapshot's EXIT trap removes;
#   - committed WAL frames in the copied -wal stay visible;
#   - the copy never has a rollback journal (snapshot-store refuses a hot
#     one and never copies one), so a read-write open cannot run journal
#     recovery.
ad_snapshot_sqlite() {
    local copy="$1" sql="$2"
    shift 2
    sqlite3 -batch -init /dev/null -cmd ".timeout ${store_busy_timeout_ms}" "$@" "$copy" "$sql" 2>&1
}

# ad_store_state <db> — read <db>'s user_version and, on a store past the
# native-input cutover but below this release (INPUT_CUTOVER_VERSION up to
# STORE_SCHEMA_VERSION), its native-input authority, both from one private
# snapshot (ad_with_store_snapshot). Status and output:
#   - the version read failed (snapshot or sqlite3): its nonzero status and
#     its own output, as a user_version read gives it;
#   - the read printed something that is no version: status 0 and that
#     output (b.hk7), so ad_fail_unreadable_version reports both kinds;
#   - otherwise status 0 and two lines (AD_STORE_STATE_FORM): the version,
#     then the authority: "-" (not read), "recorded" (a sqlite-v1:* value),
#     "missing" (no row) or "unreadable" (the query failed, store_meta
#     absent included, or any other value, which keeps pre-flight refusing).
# Task t2.7tz.ab.p5 reuses it for SR-2.16's version cross-check.
AD_STORE_STATE_FORM=$'^(0|[1-9][0-9]*)\n(-|recorded|missing|unreadable)$'
ad_store_state() {
    ad_with_store_snapshot "$1" ad_store_state_read
}

# ad_store_state_read <copy> — ad_store_state's two reads on the snapshot.
ad_store_state_read() {
    local copy="$1" version authority rc=0 state
    version="$(ad_snapshot_sqlite "$copy" "PRAGMA user_version;")" || rc=$?
    if [[ "$rc" -ne 0 ]] || ! ad_is_version "$version"; then
        printf '%s\n' "$version"
        return "$rc"
    fi
    if ((version < INPUT_CUTOVER_VERSION || version >= STORE_SCHEMA_VERSION)); then
        printf '%s\n-\n' "$version"
        return 0
    fi
    # The prefix tells a row with an empty value from no row.
    authority="$(ad_snapshot_sqlite "$copy" "SELECT 'row:' || coalesce(value, '') FROM store_meta WHERE key = 'native_input_authority';")" || rc=$?
    if [[ "$rc" -ne 0 ]]; then
        state=unreadable
    elif [[ -z "$authority" ]]; then
        state=missing
    elif [[ "$authority" == row:sqlite-v1:* && "$authority" != *$'\n'* ]]; then
        state=recorded
    else
        state=unreadable
    fi
    printf '%s\n%s\n' "$version" "$state"
}

# ad_user_version <db> — print the DB's user_version, read through the WAL,
# and exit with sqlite3's status. It opens <db> itself, so pre-flight, which
# must leave the store untouched (SR-11.7), reads the version through
# ad_store_state instead, on a locked private snapshot.
# sqlite3's stderr is printed with its stdout, so the caller holds the whole
# read in one variable and its status in another, with no file:
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
    sqlite3 -batch -init /dev/null -cmd ".timeout ${store_busy_timeout_ms}" "$1" "PRAGMA user_version;" 2>&1
}

# ad_err_name <stderr> — the err_name of the JSON error envelope
# ({"err_name":…,"err_description":…}) in a verb's captured stderr, or
# nothing when it holds none; lines that are not an envelope are skipped.
# The install branches on this name, never on the description's English
# text (b.7b4).
ad_err_name() {
    printf '%s\n' "$1" | jq -Rr '(fromjson | .err_name | strings)?' 2>/dev/null | tail -n1 || true
}

# ad_relay_output <captured> — relay a verb's captured output on stderr: a
# line that is a JSON envelope (ad_err_name's test) passes unchanged, so a
# caller can parse it, and every other line is indented by two spaces, so
# no relayed line starts `install.sh: err_name=`. A failed write never ends
# the run before its cause line.
ad_relay_output() {
    if [[ -z "$1" ]]; then
        return 0
    fi
    printf '%s\n' "$1" | jq -Rr 'if ([(fromjson | .err_name | strings)?] | length) > 0 then . else "  " + . end' >&2 2>/dev/null || true
}

# ad_journal_args — fill JOURNAL_ARGS with migrate-journal's journal flags
# from install.sh's --journal-* flags, for pre-flight's --check-only and
# step 4's run alike: --maintenance-window and --roots-complete only when
# given, --backup-dir always (empty when not given, which migrate-journal
# refuses as absolute_backup_required), then --legacy-root for each root.
JOURNAL_ARGS=()
ad_journal_args() {
    local journal_root
    JOURNAL_ARGS=()
    if [[ "$JOURNAL_MAINTENANCE" -eq 1 ]]; then
        JOURNAL_ARGS+=(--maintenance-window)
    fi
    if [[ "$JOURNAL_ROOTS_COMPLETE" -eq 1 ]]; then
        JOURNAL_ARGS+=(--roots-complete)
    fi
    JOURNAL_ARGS+=(--backup-dir "$JOURNAL_BACKUP")
    for journal_root in ${JOURNAL_ROOTS[@]+"${JOURNAL_ROOTS[@]}"}; do
        JOURNAL_ARGS+=(--legacy-root "$journal_root")
    done
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
    echo "  actual   user_version: <unreadable>" >&2 || ad_cutover_write_failed
    if [[ -n "$1" ]]; then
        printf '%s\n' "$1" | sed 's/^/    /' >&2 || ad_cutover_write_failed
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
    echo "  Reading ${state_db_name}'s user_version (sqlite3 PRAGMA user_version)" >&2 || ad_cutover_write_failed
    if [[ "$failed" -eq 1 ]]; then
        echo "  failed, so the install could not ${could_not}." >&2 || ad_cutover_write_failed
    else
        echo "  printed the output above, not a whole number (0 or more), so" >&2 || ad_cutover_write_failed
        echo "  the install could not ${could_not}." >&2 || ad_cutover_write_failed
    fi
    for line in "$@"; do
        echo "  $line" >&2 || ad_cutover_write_failed
    done
    if [[ "$failed" -eq 1 ]]; then
        echo "  Re-running this install retries the read." >&2 || ad_cutover_write_failed
    else
        echo "  sqlite3 on PATH: $(command -v sqlite3)" >&2 || ad_cutover_write_failed
        echo "  A re-run gets the same output unless that sqlite3 or ${state_db_name} changes." >&2 || ad_cutover_write_failed
    fi
    ad_exit_5 ErrVersionUnreadable
}

# ad_agent_rename_gate [defer] — set AGENT_RENAME_DUE to 1 when a store
# exists at $state_db and its user_version is below AGENT_RENAME_HOP_VERSION,
# else 0. A store whose version cannot be read stops the install (exit 5,
# ErrVersionUnreadable); with "defer" it sets GATE_DEFERRED=1 and returns
# instead (see below). Its read (ad_store_state) also records the store's
# version and native-input authority in PREFLIGHT_STORE_VERSION and
# PREFLIGHT_AUTHORITY for the cutover decision, both empty with no store.
GATE_DEFERRED=0
AGENT_RENAME_DUE=0
PREFLIGHT_STORE_VERSION=""
PREFLIGHT_AUTHORITY=""
ad_agent_rename_gate() {
    local gate_rc=0 gate_read gate_version
    GATE_DEFERRED=0
    AGENT_RENAME_DUE=0
    PREFLIGHT_STORE_VERSION=""
    PREFLIGHT_AUTHORITY=""
    if [[ ! -e "$state_db" ]]; then
        return 0
    fi
    ad_prepare_source_binaries
    gate_read="$(ad_store_state "$state_db")" || gate_rc=$?
    gate_version="$gate_read"
    if [[ "$gate_rc" -eq 0 && "$gate_read" =~ $AD_STORE_STATE_FORM ]]; then
        gate_version="${BASH_REMATCH[1]}"
        PREFLIGHT_AUTHORITY="${BASH_REMATCH[2]}"
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
    PREFLIGHT_STORE_VERSION="$gate_version"
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
# installed and the store migrated, and, for config.toml, after its .bak
# is written), when the links loop, when the resolved file's directory is
# missing (a dotfiles repository not cloned yet, say), or when that
# directory cannot be written in (home-manager links into a read-only
# /nix/store, say). With
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
# The last pre-flight config read and check is done: no supported agent found
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
# The rows are read on a locked private snapshot (ad_readonly_query: no
# SQLite connection opens the original), waiting the store's busy timeout;
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
    cols="$(ad_readonly_query "$db" "SELECT name FROM pragma_table_info('spawns');")" || rc=$?
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
    LIVE_ROWS="$(ad_readonly_query "$db" "SELECT state, ${idcol} FROM spawns WHERE state IN ('pending','waiting','working','check_permission','ask_user') ORDER BY ${idcol};" -separator ' ')" || rc=$?
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

# --------------------------------------------------------------------
# The native-input cutover decision (SR-2.14 "Pre-flight")
#
# From the version and authority the gate read on its snapshot of the store
# pre-flight resolved (the old directory's while the move is due), decide
# whether this upgrade crosses the native-input cutover, before the move and
# the swap. Pre-flight only refuses: a check here never authorizes the
# cutover or stands in for a verification migrate-journal makes after the
# swap, and a refusal here leaves every file as it was.
#   - no store, or v0: not crossing;
#   - v1 up to INPUT_CUTOVER_VERSION - 1: crossing (CUTOVER_CROSSING=1);
#     with no move due the store must hold no live row
#     (ad_crossing_live_rows_check), and the candidate administrator's
#     migrate-journal --check-only must accept the run's journal flags;
#     then the restore preconditions (below) run;
#   - INPUT_CUTOVER_VERSION up to STORE_SCHEMA_VERSION - 1: not crossing
#     when the native-input authority is recorded; refused when it is
#     missing (native_input_authority_missing) or unreadable
#     (cutover_state_unreadable);
#   - STORE_SCHEMA_VERSION or newer: not crossing (step 4's list reports a
#     newer store).
# --------------------------------------------------------------------

# ad_cutover_refuse_state <reason> <what> <inspect> — refuse a store past the
# cutover whose native-input authority is <what>; the action names the human
# step: inspect <inspect>, or restore the pre-upgrade copy.
ad_cutover_refuse_state() {
    local reason="$1" what="$2" inspect="$3" action
    action="inspect ${inspect}, or restore ${state_db_name}'s pre-upgrade copy, as docs/operator-actions.md \"Upgrading the native-input journal\" says, then re-run this install"
    echo "install.sh: ${state_db_name} at v${PREFLIGHT_STORE_VERSION} is past the native-input cutover, but its native-input authority is ${what}" >&2
    echo "  state.db: $state_db" >&2
    echo "  Nothing was installed or changed." >&2
    ad_outcome_exit ErrJournalMigrationRefused "$reason" "$action" not_committed
}

# ad_crossing_live_rows_check — on a crossing upgrade with no move due,
# refuse in pre-flight while the store holds a row in a live state
# (agents_live), or when its rows cannot be read (agent_rows_unreadable),
# with nothing changed. Rows only: migrate-journal's inventory after the swap
# stays the process check. With a move due, ad_agents_live_check has already
# read the old store's rows. The advice runs the installed binary, which here
# is still the previous release's.
ad_crossing_live_rows_check() {
    local bin line state id cmd action
    printf -v bin '%q' "$CANONICAL"
    if ! ad_read_live_rows "$state_db"; then
        action="re-run the same install command once ${state_db_name} is no longer busy (a re-run reads its rows again); if it still cannot be read, inspect $state_db with the error this run reported"
        echo "install.sh: cannot read ${state_db_name}'s agent rows, so cannot tell whether an agent is live before the native-input cutover; refusing to upgrade." >&2
        echo "  state.db: $state_db" >&2
        echo "  The rows could not be read (the store was busy past its busy timeout of ${store_busy_timeout_ms} ms, or is unreadable). sqlite3 said:" >&2
        printf '%s\n' "$LIVE_ROWS_ERR" | sed 's/^/    /' >&2
        echo "  Nothing was installed or changed." >&2
        ad_outcome_exit ErrJournalMigrationRefused agent_rows_unreadable "$action" not_committed
    fi
    if [[ -z "$LIVE_ROWS" ]]; then
        return 0
    fi
    action="stop each live agent with $bin pause --instance-id <id> (a waiting row) or $bin kill --instance-id <id> (any other live row); only for an agent you have established is gone, clear its row with $bin find-missing, run as the agents' user in their tmux environment (a missing row does not prove the agent stopped: docs/maintenance.md \"Scheduling the sweeps\"); then re-run the same install command from a shell AD did not start"
    echo "install.sh: ${state_db_name} holds live agents, which the native-input cutover cannot cross; refusing to upgrade." >&2
    echo "  state.db: $state_db" >&2
    echo "  Rows in a live state (state, instance id), each followed by the command that stops its agent:" >&2
    while IFS= read -r line; do
        state="${line%% *}"
        id="${line#* }"
        if [[ "$state" == waiting ]]; then
            printf -v cmd '%s pause --instance-id %q' "$bin" "$id"
        else
            printf -v cmd '%s kill --instance-id %q' "$bin" "$id"
        fi
        echo "    $line" >&2
        echo "      $cmd" >&2
    done <<<"$LIVE_ROWS"
    echo "  Stop whatever starts agents (a scheduler, a loop, a caller) first, so no new one starts." >&2
    echo "  Only for an agent you have established is gone, clear its row with the installed binary's find-missing, run as the agents' user in their tmux environment:" >&2
    echo "    $bin find-missing" >&2
    echo "  A missing row does not prove the agent stopped: a run as another user or against another tmux server can mark a running agent missing (docs/maintenance.md \"Scheduling the sweeps\")." >&2
    echo "  Nothing was installed or changed. Then re-run the same install command from a shell AD did not start." >&2
    ad_outcome_exit ErrJournalMigrationRefused agents_live "$action" not_committed
}

# ad_journal_check_only — run the candidate administrator's migrate-journal
# --check-only with the run's journal flags (no --store-path: it reads no
# store), and refuse in pre-flight unless it prints exactly {} with status
# 0. $ADMIN_SRC is the candidate; $ADMIN_CANONICAL is still the previous
# install's binary here.
ad_journal_check_only() {
    local out rc=0 name action
    ad_prepare_source_binaries
    out="$("$ADMIN_SRC" migrate-journal --check-only "${JOURNAL_ARGS[@]}" 2>&1)" || rc=$?
    if [[ "$rc" -eq 0 && "$out" == "{}" ]]; then
        return 0
    fi
    name="$(ad_err_name "$out")"
    if [[ "$rc" -ne 0 && "$name" == ErrJournalMigrationRefused ]]; then
        action="$(printf '%s\n' "$out" | jq -Rr '(fromjson | select(.err_name == "ErrJournalMigrationRefused") | .details.refusal.action | strings)?' 2>/dev/null | tail -n1 || true)"
        echo "install.sh: pre-flight refused the journal flags (agent-director-admin migrate-journal --check-only)" >&2
        echo "  Nothing was installed or changed." >&2
        if [[ -n "$action" ]]; then
            echo "  To fix: $action" >&2
        fi
        echo "  Then re-run this release's installer, with a new --journal-backup directory." >&2
        ad_relay_output "$out"
        ad_exit_5 ErrJournalMigrationRefused
    fi
    action="run this release's agent-director-admin as the candidate (the same release as agent-director), then re-run this install; report it if it persists"
    echo "install.sh: pre-flight could not check the journal flags: agent-director-admin migrate-journal --check-only gave an unexpected result (status ${rc})" >&2
    echo "  Nothing was installed or changed." >&2
    ad_relay_output "$out"
    if [[ -n "$name" ]]; then
        ad_outcome_exit --relayed ErrJournalMigrationRefused "$name" "$action" not_committed
    fi
    ad_outcome_exit ErrJournalMigrationRefused check_only_unestablished "$action" not_committed
}

# --------------------------------------------------------------------
# Restore preconditions (SR-2.14 "Pre-flight", SR-2.16)
#
# On a crossing upgrade, after --check-only has passed and before the move
# and the swap, pre-flight settles what SR-2.16's restore needs, and refuses
# with nothing changed when it cannot:
#   - with a move due, the journal backup directory lies outside AD's old and
#     new directories (backup_inside_install_directory);
#   - whether the previous release predates the cutover
#     (previous_release_unknown when its probe gives no clear answer);
#   - for a release that predates it, the copies the restore puts back
#     (restore_copies_unavailable).
# PREV_RELEASE_PREDATES is the decision Task t2.7tz.ab.p5's "When to restore"
# reads. It describes the administrator binary probed here, never one
# installed later in the run. A non-crossing run leaves PREV_ADMIN and
# PREV_RELEASE_PREDATES empty and makes no copy.
# --------------------------------------------------------------------

# The installed binaries: pre-flight copies them, and the swap replaces them.
CANONICAL="${DEFAULT_BIN_DIR}/agent-director"
ADMIN_CANONICAL="$DEFAULT_ADMIN_PATH"

# ad_managed_link_target — print the managed PATH link the swap makes
# (${SYMLINK_DIR}/agent-director, when linking is on and SYMLINK_DIR is a
# directory whose path holds no whitespace), or nothing. Once
# ad_managed_link_decide has run, it prints that decision instead.
MANAGED_LINK_DECIDED=0
MANAGED_LINK_DECISION=""
MANAGED_LINK_RECORD=""
MANAGED_LINK_SKIP=""
ad_managed_link_target() {
    if [[ "$MANAGED_LINK_DECIDED" -eq 1 ]]; then
        if [[ -n "$MANAGED_LINK_DECISION" ]]; then
            printf '%s\n' "$MANAGED_LINK_DECISION"
        fi
        return 0
    fi
    if [[ "$NO_SYMLINK" -eq 0 && -n "$SYMLINK_DIR" && -d "$SYMLINK_DIR" && ! "$SYMLINK_DIR" =~ [[:space:]] ]]; then
        printf '%s\n' "${SYMLINK_DIR}/agent-director"
    fi
}

# ad_managed_link_decide — before the copies, fix whether and where the swap
# makes the managed link, so the swap and the restore's record agree
# (SR-2.16). While the move is due, a SYMLINK_DIR inside AD's old or new
# directory (ad_backup_inside: lexically, physically, or by identity on
# Darwin), or one that cannot be placed against them, gets none
# (MANAGED_LINK_SKIP says why): the move renames the old directory and
# replaces the new one, and the restore puts the pre-move directory back
# whole. An outside one is recorded at SYMLINK_DIR's physical place
# (MANAGED_LINK_RECORD), so the record names it after the rename; the link
# itself is never followed.
ad_managed_link_decide() {
    local t dir which root rc phys
    t="$(ad_managed_link_target)"
    MANAGED_LINK_DECIDED=1
    MANAGED_LINK_DECISION="$t"
    MANAGED_LINK_RECORD="$t"
    MANAGED_LINK_SKIP=""
    if [[ -z "$t" || "$MOVE_DUE" -ne 1 ]]; then
        return 0
    fi
    dir="$SYMLINK_DIR"
    if [[ "$dir" != /* ]]; then
        dir="$PWD/$dir"
    fi
    for which in old new; do
        if [[ "$which" == old ]]; then
            root="$OLD_INSTALL_ROOT"
        else
            root="$DEFAULT_INSTALL_ROOT"
        fi
        rc=0
        ad_backup_inside "$dir" "$root" || rc=$?
        if [[ "$rc" -eq 0 ]]; then
            MANAGED_LINK_SKIP="$SYMLINK_DIR is inside AD's $which directory $root, which the move renames or replaces"
        elif [[ "$rc" -eq 2 ]]; then
            MANAGED_LINK_SKIP="$SYMLINK_DIR cannot be placed against AD's $which directory $root ($INSIDE_CAUSE)"
        fi
        if [[ -n "$MANAGED_LINK_SKIP" ]]; then
            MANAGED_LINK_DECISION=""
            MANAGED_LINK_RECORD=""
            return 0
        fi
    done
    if phys="$(cd -P -- "$dir" 2>/dev/null && pwd -P)"; then
        MANAGED_LINK_RECORD="${phys%/}/agent-director"
    fi
}

# ad_deep_place <absolute path> — set DEEP_BASE to the physical path (cd -P,
# pwd -P) of the path's deepest existing ancestor-or-self, DEEP_REST to the
# names below it joined by / (empty when the path exists), and DEEP_PATH to
# the two joined. A component that is a dangling link is replaced by the
# path it names and the walk goes on, so the place is judged as the kernel
# would create it. Fails when that ancestor is not a directory or cannot be
# entered, or when links loop.
DEEP_BASE=""
DEEP_REST=""
DEEP_PATH=""
ad_deep_place() {
    local p rest="" hops=0
    DEEP_BASE=""
    DEEP_REST=""
    DEEP_PATH=""
    p="$(ad_clean_path "$1")"
    while [[ ! -e "$p" ]]; do
        if [[ "$p" != /* ]]; then
            return 1
        fi
        if [[ -L "$p" ]]; then
            if (( ++hops > 40 )); then
                return 1
            fi
            p="$(ad_resolve_link "$p" 2>/dev/null)" || return 1
            p="$(ad_clean_path "$p")"
        else
            rest="${p##*/}${rest:+/$rest}"
            p="${p%/*}"
            p="${p:-/}"
        fi
    done
    if [[ ! -d "$p" ]]; then
        return 1
    fi
    DEEP_BASE="$(cd -P -- "$p" 2>/dev/null && pwd -P)" || return 1
    DEEP_REST="$rest"
    if [[ -z "$rest" ]]; then
        DEEP_PATH="$DEEP_BASE"
    elif [[ "$DEEP_BASE" == / ]]; then
        DEEP_PATH="/$rest"
    else
        DEEP_PATH="$DEEP_BASE/$rest"
    fi
}

# ad_file_id <path> — print <path>'s file identity, device:inode in decimal
# (GNU stat, then BSD stat).
ad_file_id() {
    local id
    id="$(stat -L -c '%d:%i' "$1" 2>/dev/null || stat -L -f '%d:%i' "$1" 2>/dev/null)" || return 1
    [[ "$id" =~ ^[0-9]+:[0-9]+$ ]] || return 1
    printf '%s\n' "$id"
}

# ad_fold <text> — print <text> with ASCII letters lower-cased (bash 3.2 has
# no ${x,,}).
ad_fold() {
    printf '%s' "$1" | LC_ALL=C tr '[:upper:]' '[:lower:]'
}

# ad_id_chain — from ad_deep_place's DEEP_BASE and DEEP_REST, fill ID_CHAIN
# with the identity of DEEP_BASE and of each physical parent up to /, and
# NAME_CHAIN with the case-folded names below each one down to the judged
# path. Fails when an identity cannot be read.
ID_CHAIN=()
NAME_CHAIN=()
ad_id_chain() {
    local p="$DEEP_BASE" names id
    ID_CHAIN=()
    NAME_CHAIN=()
    names="$(ad_fold "$DEEP_REST")"
    while :; do
        id="$(ad_file_id "$p")" || return 1
        ID_CHAIN+=("$id")
        NAME_CHAIN+=("$names")
        if [[ "$p" == / ]]; then
            return 0
        fi
        names="$(ad_fold "${p##*/}")${names:+/$names}"
        p="${p%/*}"
        p="${p:-/}"
    done
}

# ad_backup_inside <backup> <dir> — 0 when <backup> is <dir> or under it,
# 1 when it is outside, 2 (the cause in INSIDE_CAUSE) when its place cannot
# be judged. Inside lexically (the cleaned paths), or physically (each
# path's deepest existing ancestor resolved, as the move places path keys:
# an inner link leading out still counts lexically, and a path reaching
# <dir> through a link counts physically). On Darwin also by file identity
# (SR-2.15 "Limits"): an element of <backup>'s chain with the identity of one
# of <dir>'s, whose folded names below it are <dir>'s names below its match
# or begin with them and /, so a case variant cannot escape the check, nor
# can a containing directory that does not exist yet. For status 2,
# INSIDE_SIDE says which path could not be judged: dir or backup.
INSIDE_CAUSE=""
INSIDE_SIDE=""
ad_backup_inside() {
    local b d dir_phys i j dn bn
    local -a dir_ids dir_names
    INSIDE_CAUSE=""
    INSIDE_SIDE=""
    b="$(ad_clean_path "$1")"
    d="$(ad_clean_path "$2")"
    if [[ "$b" == "$d" || "$b" == "$d"/* ]]; then
        return 0
    fi
    INSIDE_SIDE=dir
    if ! ad_deep_place "$d"; then
        INSIDE_CAUSE="$d cannot be placed: an existing part of it is not a directory or cannot be entered, or its links loop"
        return 2
    fi
    dir_phys="$DEEP_PATH"
    if [[ "$uname_s" == Darwin ]]; then
        if ! ad_id_chain; then
            INSIDE_CAUSE="the file identity of $DEEP_BASE, or of a directory above it, cannot be read"
            return 2
        fi
        dir_ids=("${ID_CHAIN[@]}")
        dir_names=("${NAME_CHAIN[@]}")
    fi
    INSIDE_SIDE=backup
    if ! ad_deep_place "$b"; then
        INSIDE_CAUSE="$b cannot be placed: an existing part of it is not a directory or cannot be entered, or its links loop"
        return 2
    fi
    if [[ "$DEEP_PATH" == "$dir_phys" || "$DEEP_PATH" == "$dir_phys"/* ]]; then
        return 0
    fi
    if [[ "$uname_s" != Darwin ]]; then
        return 1
    fi
    if ! ad_id_chain; then
        INSIDE_CAUSE="the file identity of $DEEP_BASE, or of a directory above it, cannot be read"
        return 2
    fi
    for ((i = 0; i < ${#ID_CHAIN[@]}; i++)); do
        for ((j = 0; j < ${#dir_ids[@]}; j++)); do
            if [[ "${ID_CHAIN[$i]}" != "${dir_ids[$j]}" ]]; then
                continue
            fi
            dn="${dir_names[$j]}"
            bn="${NAME_CHAIN[$i]}"
            if [[ -z "$dn" || "$bn" == "$dn" || "$bn" == "$dn"/* ]]; then
                return 0
            fi
        done
    done
    return 1
}

# ad_backup_install_dirs_check — with a move due, refuse a journal backup
# directory inside AD's old or new directory (SR-2.14,
# backup_inside_install_directory): step 4 creates it after the move, and a
# restore renames the pre-move directory back and sets the new one aside,
# which would move or hide it. When the install directory itself cannot be
# placed or identified, no other backup helps, so the action says to repair
# that path first.
ad_backup_install_dirs_check() {
    local which dir rc action
    for which in old new; do
        action="choose a --journal-backup directory outside AD's old and new directories (~/.agent-director and ~/.config/agent-director), then re-run this release's installer"
        if [[ "$which" == old ]]; then
            dir="$OLD_INSTALL_ROOT"
        else
            dir="$DEFAULT_INSTALL_ROOT"
        fi
        rc=0
        ad_backup_inside "$JOURNAL_BACKUP" "$dir" || rc=$?
        if [[ "$rc" -eq 1 ]]; then
            continue
        fi
        if [[ "$rc" -eq 0 ]]; then
            echo "install.sh: the journal backup directory $JOURNAL_BACKUP is inside AD's $which directory $dir; refusing to upgrade." >&2
        else
            echo "install.sh: cannot judge whether the journal backup directory $JOURNAL_BACKUP is inside AD's $which directory $dir; refusing to upgrade." >&2
            echo "  $INSIDE_CAUSE" >&2
            if [[ "$INSIDE_SIDE" == dir ]]; then
                action="repair the path of AD's directory that the message names (rename aside any existing part of it that is not a directory, make each existing part one the user running the install can enter, break any link loop in it, and make its file identity readable), then choose a --journal-backup directory outside AD's old and new directories (~/.agent-director and ~/.config/agent-director), then re-run this release's installer"
                echo "  To fix: repair the path $dir as the action below says; choosing another --journal-backup alone does not clear this." >&2
            fi
        fi
        echo "  This upgrade moves $OLD_INSTALL_ROOT to $DEFAULT_INSTALL_ROOT, and a restore renames those directories, which would move or hide a backup inside them." >&2
        echo "  Nothing was installed or changed." >&2
        ad_outcome_exit ErrJournalMigrationRefused backup_inside_install_directory "$action" not_committed
    done
}

# ad_prev_release_probe — decide whether the previous install's release
# predates the cutover (SR-2.14): its administrator (the old layout's while
# the move is due) absent, or answering migrate-journal --help with exit 1
# and ErrUnknownVerb, predates it (PREV_RELEASE_PREDATES=1); exit 0 does not
# (0). The probe passes no global flag, so no release opens a store or reads
# a config. Any other answer refuses (previous_release_unknown).
PREV_ADMIN=""
PREV_RELEASE_PREDATES=""
ad_prev_release_probe() {
    local out rc=0 action
    if [[ "$MOVE_DUE" -eq 1 ]]; then
        PREV_ADMIN="${OLD_INSTALL_ROOT}/admin/agent-director-admin"
    else
        PREV_ADMIN="$DEFAULT_ADMIN_PATH"
    fi
    if [[ ! -e "$PREV_ADMIN" && ! -L "$PREV_ADMIN" ]]; then
        PREV_RELEASE_PREDATES=1
        echo "  previous: $PREV_ADMIN predates the native-input cutover (absent)"
        return 0
    fi
    out="$("$PREV_ADMIN" migrate-journal --help </dev/null 2>&1)" || rc=$?
    if [[ "$rc" -eq 0 ]]; then
        PREV_RELEASE_PREDATES=0
        echo "  previous: $PREV_ADMIN does not predate the native-input cutover (it has migrate-journal)"
        return 0
    fi
    if [[ "$rc" -eq 1 && "$(ad_err_name "$out")" == ErrUnknownVerb ]]; then
        PREV_RELEASE_PREDATES=1
        echo "  previous: $PREV_ADMIN predates the native-input cutover (migrate-journal --help: ErrUnknownVerb)"
        return 0
    fi
    action="make the previous agent-director-admin runnable, or remove it if it is not that release's, then re-run this release's installer"
    echo "install.sh: cannot tell whether the previous release predates the native-input cutover; refusing to upgrade." >&2
    echo "  previous agent-director-admin: $PREV_ADMIN" >&2
    echo "  migrate-journal --help exited with status $rc, without exit 0 or exit 1 with ErrUnknownVerb. It said:" >&2
    if [[ -n "$out" ]]; then
        printf '%s\n' "$out" | sed 's/^/    /' >&2 || true
    fi
    echo "  Nothing was installed or changed." >&2
    ad_outcome_exit ErrJournalMigrationRefused previous_release_unknown "$action" not_committed
}

# ad_restore_copy <path> [regular] — record <path> as an item the restore
# puts back, with a copy beside it named <path>.restore-<RESTORE_TS>.<suffix>:
# for a link, a directory created exclusively by mktemp -d holding a new link,
# named as <path>'s last component, with the same raw target; for a regular
# file, one created exclusively by mktemp (which never follows a link at the
# name), with the same bytes and mode. Nothing at <path> records an empty
# copy. With "regular" (the sentinel) only a regular file is copied, so its
# bytes are never read or written through a link. Returns 1, the cause in
# RESTORE_CAUSE and any chosen name in RESTORE_COPY_NAME (for a link, the
# link inside its directory), when the copy cannot be made; a half-made copy
# is removed, and nothing this run did not create is overwritten, written
# through or removed.
RESTORE_CAUSE=""
RESTORE_COPY_NAME=""
ad_restore_copy() {
    local path="$1" only_regular="${2:-}" name raw out
    RESTORE_CAUSE=""
    RESTORE_COPY_NAME=""
    if [[ ! -e "$path" && ! -L "$path" ]]; then
        RESTORE_BELONGS+=("$path")
        RESTORE_COPY+=("")
        return 0
    fi
    if [[ -n "$only_regular" ]] && [[ -L "$path" || ! -f "$path" ]]; then
        RESTORE_CAUSE="$path is not a regular file"
        return 1
    fi
    if [[ -L "$path" ]]; then
        if ! raw="$(readlink "$path" 2>&1)"; then
            RESTORE_CAUSE="$raw"
            return 1
        fi
        # ln writes its link inside any directory at its destination, so the
        # link is made in a directory mktemp -d creates exclusively (mode
        # 0700, never an existing entry), never at a free-looking name.
        if ! name="$(mktemp -d "${path}.restore-${RESTORE_TS}.XXXXXX" 2>&1)"; then
            RESTORE_CAUSE="$name"
            return 1
        fi
        RESTORE_COPY_NAME="$name/${path##*/}"
        if ! out="$(ln -sn -- "$raw" "$RESTORE_COPY_NAME" 2>&1)"; then
            rmdir -- "$name" 2>/dev/null || true
            RESTORE_CAUSE="$out"
            return 1
        fi
        if [[ ! -L "$RESTORE_COPY_NAME" || "$(readlink "$RESTORE_COPY_NAME" 2>/dev/null)" != "$raw" ]]; then
            RESTORE_BELONGS+=("$path")
            RESTORE_COPY+=("$name")
            RESTORE_CAUSE="the copy $RESTORE_COPY_NAME is not a link to $raw"
            return 1
        fi
    elif [[ -f "$path" ]]; then
        if ! name="$(mktemp "${path}.restore-${RESTORE_TS}.XXXXXX" 2>&1)"; then
            RESTORE_CAUSE="$name"
            return 1
        fi
        RESTORE_COPY_NAME="$name"
        if ! out="$(cp -- "$path" "$name" 2>&1)" \
            || ! out="$(chmod "$(ad_mode_of "$path")" "$name" 2>&1)"; then
            rm -f -- "$name" 2>/dev/null || true
            RESTORE_CAUSE="$out"
            return 1
        fi
        if ! cmp -s -- "$path" "$name"; then
            rm -f -- "$name" 2>/dev/null || true
            RESTORE_CAUSE="the copy $name differs from $path"
            return 1
        fi
    else
        RESTORE_CAUSE="$path is neither a link nor a regular file"
        return 1
    fi
    RESTORE_BELONGS+=("$path")
    RESTORE_COPY+=("$name")
}

# ad_restore_item <item> <path> [regular] — ad_restore_copy, saying on
# stdout which copy it made, or refusing (restore_copies_unavailable) with
# every copy this run made removed.
ad_restore_item() {
    local c action
    if ad_restore_copy "$2" "${3:-}"; then
        if [[ -n "$RESTORE_COPY_NAME" ]]; then
            echo "  restore : copied $2 to $RESTORE_COPY_NAME (removed when the run ends unless reported as kept)"
        fi
        return 0
    fi
    action="make room or write access beside the installed binaries, the store and the managed PATH link; if the sentinel beside the store is not a regular file, or an installed binary or the managed PATH entry is neither a link nor a regular file, remove that entry; then re-run this release's installer"
    ad_restore_copies_remove || true
    echo "install.sh: cannot make the copies a restore needs; refusing to upgrade." >&2
    echo "  item : $1, $2" >&2
    if [[ -n "$RESTORE_COPY_NAME" ]]; then
        echo "  copy : $RESTORE_COPY_NAME" >&2
    fi
    echo "  cause:" >&2
    printf '%s\n' "$RESTORE_CAUSE" | sed 's/^/    /' >&2 || true
    for c in ${RESTORE_NOT_REMOVED[@]+"${RESTORE_NOT_REMOVED[@]}"}; do
        echo "  not removed: $c (an earlier copy this run made; remove it by hand)" >&2
    done
    echo "  Nothing was installed or changed." >&2
    ad_outcome_exit ErrJournalMigrationRefused restore_copies_unavailable "$action" not_committed
}

# ad_restore_copies_make — make the copies SR-2.16's restore needs, before
# the move and the swap. ad_exit_cleanup removes remaining copies unless
# the outcome keeps them. While the move is due the restore puts the whole
# pre-move directory back, so nothing inside it is copied and the new
# directory holds no pre-run binary:
# the binaries are recorded absent, and only a sentinel beside a store
# outside the old directory (which step 3 writes there) is copied, at its
# physical place. The managed link is copied where ad_managed_link_decide
# records it, and not at all when the swap will make none.
ad_restore_copies_make() {
    local sentinel
    trap ad_exit_cleanup EXIT
    RESTORE_TS="$(ad_ts)"
    if [[ "$MOVE_DUE" -eq 1 ]]; then
        RESTORE_BELONGS+=("$CANONICAL" "$ADMIN_CANONICAL")
        RESTORE_COPY+=("" "")
        if [[ "$STORE_CLASS" == out || "$STORE_CLASS" == warn ]]; then
            # A sentinel that is a link stays as it is, for the copy to refuse.
            sentinel="$(ad_sentinel_path "$state_db")"
            if [[ ! -L "$sentinel" ]]; then
                sentinel="$(ad_phys_place "$sentinel")" || sentinel="$(ad_sentinel_path "$state_db")"
            fi
            ad_restore_item "the migration sentinel" "$sentinel" regular
        fi
    else
        ad_restore_item agent-director "$CANONICAL"
        ad_restore_item agent-director-admin "$ADMIN_CANONICAL"
        ad_restore_item "the migration sentinel" "$(ad_sentinel_path "$state_db")" regular
    fi
    ad_managed_link_decide
    if [[ -n "$MANAGED_LINK_RECORD" ]]; then
        ad_restore_item "the managed PATH link" "$MANAGED_LINK_RECORD"
    fi
}

# --------------------------------------------------------------------
# Restore and forward completion (SR-2.16)
# --------------------------------------------------------------------

RESTORE_DONE=""
RESTORE_FAILED=""
RESTORE_ASIDE=""
RESTORE_PREMOVE_AT=""
RESTORE_FINISHED=()
MOVE_NOTES_DEFERRED=""
CUTOVER_PHASE=""
CUTOVER_INTERRUPTED=""
CUTOVER_INTERRUPTED_IN=""
CUTOVER_LATE_INTERRUPT=""

# SR-2.16: before-start signals restore; during the attempt they only record
# cancellation. migrate-journal receives the process group's signal itself.
ad_cutover_arm_traps() {
    CUTOVER_PHASE=before_start
    trap 'ad_cutover_on_signal INT' INT
    trap 'ad_cutover_on_signal TERM' TERM
    trap 'ad_cutover_on_signal HUP' HUP
    trap '' PIPE
}

# SR-2.16: settlement and its children ignore further interrupts, with no
# fork-to-trap window. Go helpers alone can still be ended by TERM.
ad_cutover_ignore_interrupts() {
    CUTOVER_PHASE=settling
    trap '' INT TERM HUP
}

# SR-2.16: an uninterrupted successful attempt ends the protected span.
ad_cutover_disarm_traps() {
    CUTOVER_PHASE=""
    trap - INT TERM HUP PIPE
}

ad_cutover_on_signal() {
    if [[ -z "$CUTOVER_INTERRUPTED" ]]; then
        CUTOVER_INTERRUPTED="$1"
        CUTOVER_INTERRUPTED_IN="$CUTOVER_PHASE"
    fi
    if [[ "$CUTOVER_PHASE" == before_start ]]; then
        ad_cutover_interrupted_before_start
    fi
    return 0
}

# No attempt ran: settle the explicit not_committed/no-step outcome through
# the same restore decision. Also called by EXIT before it removes copies.
ad_cutover_interrupted_before_start() {
    ad_cutover_ignore_interrupts
    set +e
    OUTCOME_ENVELOPE=""
    OUTCOME_ERR_NAME=""
    OUTCOME_OUTPUT=""
    OUTCOME_CUTOVER=not_committed
    OUTCOME_STEPS=false
    OUTCOME_REFUSAL="$(jq -nc --arg action "re-run this release's installer with a new --journal-backup directory, and let it finish" \
        '{reason: "interrupted", processes: [], action: $action}')"
    ad_cutover_settle
}

# SR-2.16: after an interrupted attempt succeeds and step 5 verifies it,
# stop before hooks/MCP/scheduling and report the committed forward row.
ad_cutover_interrupted_committed() {
    ad_cutover_ignore_interrupts
    set +e
    OUTCOME_ENVELOPE=""
    OUTCOME_ERR_NAME=""
    OUTCOME_OUTPUT=""
    OUTCOME_CUTOVER=committed
    OUTCOME_STEPS=unknown
    OUTCOME_REFUSAL="$(jq -nc --arg action "re-run this release's installer with a new --journal-backup directory, which finishes the install, then run the administrator's verification retry until it exits 0 with authority sqlite" \
        '{reason: "interrupted", processes: [], action: $action}')"
    ad_cutover_settle
}

# Only a crossing move can still be undone. Preserve trailing newlines in
# its notes while waiting for the outcome; other installs print in place.
ad_move_note() {
    local notes
    if [[ "$CUTOVER_CROSSING" -eq 1 && "$MOVED" -eq 1 ]]; then
        notes="$("$@"; printf '.')"
        MOVE_NOTES_DEFERRED+="${notes%.}"
    else
        "$@"
    fi
}

ad_move_notes_flush() {
    printf '%s' "$MOVE_NOTES_DEFERRED" 2>/dev/null || true
    MOVE_NOTES_DEFERRED=""
}

# A restore invalidates every note about keeping or pointing at the new
# layout. A partial restore's item report alone describes what remains.
ad_move_outcome_notes() {
    case "$1" in
        restored)
            MOVE_NOTES_DEFERRED=""
            if [[ "${MOVED:-0}" -eq 1 ]]; then
                if [[ -n "$RESTORE_ASIDE" ]]; then
                    echo "  move    : undone: $OLD_INSTALL_ROOT is back as it was before the run; this run's $DEFAULT_INSTALL_ROOT was set aside as $RESTORE_ASIDE; the re-run moves it again" >&2 || true
                else
                    echo "  move    : undone: $OLD_INSTALL_ROOT is back as it was before the run; this run's linked $DEFAULT_INSTALL_ROOT was emptied; the re-run moves it again" >&2 || true
                fi
            fi ;;
        restore_incomplete) MOVE_NOTES_DEFERRED="" ;;
        *) ad_move_notes_flush ;;
    esac
}

# A link copy is held inside the private directory pre-flight made. The
# put-back and its report name the link itself, never that container.
ad_restore_copy_path() {
    local i="$1" c="${RESTORE_COPY[$1]}"
    if [[ -n "$c" && -d "$c" && ! -L "$c" ]]; then
        c="$c/${RESTORE_BELONGS[$i]##*/}"
    fi
    printf '%s\n' "$c"
}

# Unlike ad_file_id, identify the link itself. A saved managed link may
# point to a directory; its successful put-back must not look like mv put
# the copy inside a late directory. Linux's and macOS's stat both default to lstat.
ad_restore_id() {
    local id
    id="$(stat -c '%d:%i' "$1" 2>/dev/null || stat -f '%d:%i' "$1" 2>/dev/null)" || return 1
    [[ "$id" =~ ^[0-9]+:[0-9]+$ ]] || return 1
    printf '%s\n' "$id"
}

# Restore only installation files, newest record first, and the old-layout
# directory last. Capture every diagnostic: reporting must not interrupt a
# restore, even with both output streams closed. Stop at the first failure.
ad_restore_run() {
    RESTORE_KEEP_COPIES=1
    RESTORE_DONE=""
    RESTORE_FAILED=""
    RESTORE_PREMOVE_AT="${MOVED_KEPT:-}"
    local i b c container err base n nested rc source_id
    if [[ "${MOVED:-0}" -eq 1 && ( -e "$OLD_INSTALL_ROOT" || -L "$OLD_INSTALL_ROOT" ) ]]; then
        RESTORE_FAILED="$OLD_INSTALL_ROOT is occupied"
        return 1
    fi
    for ((i = ${#RESTORE_BELONGS[@]} - 1; i >= 0; i--)); do
        b="${RESTORE_BELONGS[$i]}"
        if [[ "${MOVED:-0}" -eq 1 && "$b" == "$DEFAULT_INSTALL_ROOT/"* ]]; then
            continue
        fi
        c="$(ad_restore_copy_path "$i")"
        if [[ -n "$c" ]]; then
            # mv follows a destination directory (a link to one included).
            # Refuse it, and check afterwards for one appearing during mv.
            if [[ -d "$b" ]]; then
                RESTORE_FAILED="$b is a directory or a link to one"
                return 1
            fi
            container="${RESTORE_COPY[$i]}"
            if ! source_id="$(ad_restore_id "$c")"; then
                RESTORE_FAILED="cannot identify the copy $c before its rename"
                return 1
            fi
            rc=0
            err="$(mv -f -- "$c" "$b" 2>&1)" || rc=$?
            if [[ ! -e "$c" && ! -L "$c" && -d "$b" && "$(ad_restore_id "$b")" != "$source_id" ]]; then
                nested="$b/${c##*/}"
                RESTORE_COPY[$i]="$nested"
                RESTORE_FAILED="$b became a directory during the rename; the copy is at $nested${err:+: $err}"
                return 1
            fi
            if [[ "$rc" -ne 0 || -e "$c" || -L "$c" || ( ! -e "$b" && ! -L "$b" ) ]]; then
                RESTORE_FAILED="cannot rename $c to $b${err:+: $err}"
                return 1
            fi
            RESTORE_FINISHED[$i]=1
            RESTORE_DONE+="put back $b"$'\n'
            if [[ "$container" != "$c" ]]; then
                if ! err="$(rmdir -- "$container" 2>&1)"; then
                    RESTORE_FAILED="cannot remove the empty copy directory $container: $err"
                    return 1
                fi
            fi
        else
            if [[ -e "$b" || -L "$b" ]]; then
                # rm -f removes a link itself, even a link to a directory;
                # an actual directory (including one appearing late) fails.
                if [[ -d "$b" && ! -L "$b" ]]; then
                    RESTORE_FAILED="$b is a directory"
                    return 1
                fi
                if ! err="$(rm -f -- "$b" 2>&1)"; then
                    RESTORE_FAILED="cannot remove $b: $err"
                    return 1
                fi
                if [[ -e "$b" || -L "$b" ]]; then
                    RESTORE_FAILED="$b is still present after removal"
                    return 1
                fi
                RESTORE_DONE+="removed $b"$'\n'
            fi
            RESTORE_FINISHED[$i]=1
        fi
    done
    if [[ "${MOVED:-0}" -ne 1 ]]; then
        return 0
    fi
    if [[ "$MOVE_NEW_IS_LINK" -eq 1 ]]; then
        if ! err="$(find "${DEFAULT_INSTALL_ROOT}/" -mindepth 1 -maxdepth 1 -exec rm -rf -- {} + 2>&1)"; then
            RESTORE_FAILED="cannot empty $DEFAULT_INSTALL_ROOT: $err"
            return 1
        fi
        rc=0
        err="$(find "${DEFAULT_INSTALL_ROOT}/" -mindepth 1 -maxdepth 1 2>&1)" || rc=$?
        if [[ "$rc" -ne 0 || -n "$err" ]]; then
            RESTORE_FAILED="$DEFAULT_INSTALL_ROOT could not be verified empty${err:+: $err}"
            return 1
        fi
        # noclobber refuses a late file or link; a directory fails too. The
        # marker must never truncate an occupant or follow a planted link.
        if ! err="$( (umask 077; set -C; : >"$MOVE_MARKER") 2>&1)"; then
            RESTORE_FAILED="cannot write the move marker $MOVE_MARKER: $err"
            return 1
        fi
        RESTORE_DONE+="emptied $DEFAULT_INSTALL_ROOT and wrote its move marker"$'\n'
    else
        base="${DEFAULT_INSTALL_ROOT}.after-upgrade-$(ad_ts)"
        RESTORE_ASIDE="$base"
        n=1
        while [[ -e "$RESTORE_ASIDE" || -L "$RESTORE_ASIDE" ]]; do
            n=$((n + 1))
            RESTORE_ASIDE="${base}-${n}"
        done
        if ! source_id="$(ad_restore_id "$DEFAULT_INSTALL_ROOT")"; then
            RESTORE_FAILED="cannot identify $DEFAULT_INSTALL_ROOT before setting it aside"
            RESTORE_ASIDE=""
            return 1
        fi
        rc=0
        err="$(mv -- "$DEFAULT_INSTALL_ROOT" "$RESTORE_ASIDE" 2>&1)" || rc=$?
        nested="$RESTORE_ASIDE/${DEFAULT_INSTALL_ROOT##*/}"
        if [[ ! -e "$DEFAULT_INSTALL_ROOT" && ! -L "$DEFAULT_INSTALL_ROOT" && -d "$nested" && "$(ad_restore_id "$RESTORE_ASIDE")" != "$source_id" ]]; then
            RESTORE_ASIDE="$nested"
            RESTORE_FAILED="the set-aside path became occupied; the new directory is at $RESTORE_ASIDE${err:+: $err}"
            return 1
        fi
        if [[ "$rc" -ne 0 || -e "$DEFAULT_INSTALL_ROOT" || -L "$DEFAULT_INSTALL_ROOT" || ! -d "$RESTORE_ASIDE" ]]; then
            RESTORE_FAILED="cannot set $DEFAULT_INSTALL_ROOT aside at $RESTORE_ASIDE${err:+: $err}"
            RESTORE_ASIDE=""
            return 1
        fi
        RESTORE_DONE+="set $DEFAULT_INSTALL_ROOT aside at $RESTORE_ASIDE"$'\n'
    fi
    # A late directory can absorb mv's source on Linux and macOS. Locate that
    # retained directory and stop; never delete the occupant or retry mv.
    if [[ -e "$OLD_INSTALL_ROOT" || -L "$OLD_INSTALL_ROOT" ]]; then
        RESTORE_FAILED="$OLD_INSTALL_ROOT is occupied"
        return 1
    fi
    if ! source_id="$(ad_restore_id "$MOVED_KEPT")"; then
        RESTORE_FAILED="cannot identify $MOVED_KEPT before putting it back"
        return 1
    fi
    rc=0
    err="$(mv -- "$MOVED_KEPT" "$OLD_INSTALL_ROOT" 2>&1)" || rc=$?
    nested="$OLD_INSTALL_ROOT/${MOVED_KEPT##*/}"
    if [[ ! -e "$MOVED_KEPT" && ! -L "$MOVED_KEPT" && ( -e "$nested" || -L "$nested" ) && "$(ad_restore_id "$OLD_INSTALL_ROOT")" != "$source_id" ]]; then
        RESTORE_PREMOVE_AT="$nested"
        RESTORE_FAILED="$OLD_INSTALL_ROOT became occupied; the pre-move directory is at $nested${err:+: $err}"
        return 1
    fi
    if [[ "$rc" -ne 0 || -e "$MOVED_KEPT" || -L "$MOVED_KEPT" || ! -d "$OLD_INSTALL_ROOT" ]]; then
        RESTORE_FAILED="cannot put $MOVED_KEPT back at $OLD_INSTALL_ROOT${err:+: $err}"
        return 1
    fi
    RESTORE_PREMOVE_AT=""
    RESTORE_DONE+="put back $OLD_INSTALL_ROOT"$'\n'
    return 0
}

# All kept-copy outcomes use these builders, including one with no attempt
# envelope and thus no restore. jq alone quotes paths into machine records.
ad_restore_copies_json() {
    local i c
    {
        for ((i = 0; i < ${#RESTORE_COPY[@]}; i++)); do
            [[ "${RESTORE_FINISHED[$i]:-0}" -eq 1 ]] && continue
            c="$(ad_restore_copy_path "$i")"
            if [[ -n "$c" && ( -e "$c" || -L "$c" ) ]]; then
                jq -nc --arg c "$c" --arg b "${RESTORE_BELONGS[$i]}" '{copy: $c, belongs_at: $b}'
            fi
        done
        c="${RESTORE_PREMOVE_AT:-${MOVED_KEPT:-}}"
        if [[ "${MOVED:-0}" -eq 1 && -n "$c" && ( -e "$c" || -L "$c" ) ]]; then
            jq -nc --arg c "$c" --arg b "$OLD_INSTALL_ROOT" '{copy: $c, belongs_at: $b}'
        fi
    } | jq -sc .
}

ad_restore_remove_json() {
    local i b
    {
        for ((i = 0; i < ${#RESTORE_COPY[@]}; i++)); do
            [[ -n "${RESTORE_COPY[$i]}" || "${RESTORE_FINISHED[$i]:-0}" -eq 1 ]] && continue
            b="${RESTORE_BELONGS[$i]}"
            [[ "${MOVED:-0}" -eq 1 && "$b" == "$DEFAULT_INSTALL_ROOT/"* ]] && continue
            if [[ -e "$b" || -L "$b" ]]; then
                jq -nc --arg b "$b" '$b'
            fi
        done
    } | jq -sc .
}

ad_restore_attempt_store() {
    local db="$state_db"
    if [[ -n "$RESTORE_ASIDE" && "$db" == "$DEFAULT_INSTALL_ROOT/"* ]]; then
        db="${RESTORE_ASIDE}${db#"$DEFAULT_INSTALL_ROOT"}"
    fi
    printf '%s\n' "$db"
}

# The attempt's inputs are explicit globals so an interrupt before its start
# can use the same settle path without inventing an administrator envelope.
OUTCOME_RC=0
OUTCOME_OUTPUT=""
OUTCOME_ENVELOPE=""
OUTCOME_ERR_NAME=""
OUTCOME_CUTOVER=""
OUTCOME_STEPS=""
OUTCOME_REFUSAL=""
OUTCOME_REASON=""
OUTCOME_ACTION=""
RESTORE_WHY_NOT=()
RESTORE_REREAD_PATH=""
RESTORE_REREAD_OUTPUT=""
RESTORE_REREAD_VERSION=""
RESTORE_REREAD_FAILED=0

# Read only structured fields of the last error envelope. Missing commit
# evidence never becomes proof that the old binaries may run again.
ad_journal_outcome_read() {
    OUTCOME_RC="$1"
    OUTCOME_OUTPUT="$2"
    OUTCOME_REASON=""
    OUTCOME_ACTION=""
    OUTCOME_ENVELOPE="$(printf '%s\n' "$2" | jq -Rc '(fromjson | select(.err_name | strings))?' 2>/dev/null | tail -n1)" || true
    OUTCOME_ERR_NAME=""
    OUTCOME_REFUSAL=""
    OUTCOME_STEPS=unknown
    OUTCOME_CUTOVER=unestablished
    if [[ -z "$OUTCOME_ENVELOPE" ]]; then
        return 0
    fi
    OUTCOME_ERR_NAME="$(printf '%s\n' "$OUTCOME_ENVELOPE" | jq -r '.err_name')"
    OUTCOME_CUTOVER="$(printf '%s\n' "$OUTCOME_ENVELOPE" | jq -r '[.details.cutover?][0] | if . == "not_committed" or . == "committed" or . == "unknown" then . else "unreadable" end')"
    OUTCOME_STEPS="$(printf '%s\n' "$OUTCOME_ENVELOPE" | jq -r '[.details.steps_committed?][0] | if . == true then "true" elif . == false then "false" else "unknown" end')"
    OUTCOME_REFUSAL="$(printf '%s\n' "$OUTCOME_ENVELOPE" | jq -c '.details.refusal? // empty')"
}

# The cross-check reads the store the previous release would open, which
# can differ from the attempted store after a move. Failure to read is not
# evidence against a not_committed result (a full disk must allow restore).
ad_restore_reread() {
    local rc=0
    RESTORE_REREAD_PATH="$state_db"
    if [[ "${MOVED:-0}" -eq 1 ]]; then
        if [[ -n "$STORE_SRC_PHYS" ]]; then
            RESTORE_REREAD_PATH="${MOVED_KEPT}${STORE_SRC_PHYS#"$OLD_PHYS"}"
        else
            RESTORE_REREAD_PATH="$STORE_PHYS"
        fi
    fi
    RESTORE_REREAD_OUTPUT="$(ad_store_state "$RESTORE_REREAD_PATH" 2>&1)" || rc=$?
    if [[ "$rc" -eq 143 && "$CUTOVER_PHASE" == settling ]]; then
        CUTOVER_LATE_INTERRUPT=TERM
    fi
    RESTORE_REREAD_VERSION="${RESTORE_REREAD_OUTPUT%%$'\n'*}"
    if [[ "$rc" -ne 0 ]] || ! ad_is_version "$RESTORE_REREAD_VERSION"; then
        RESTORE_REREAD_FAILED=1
        RESTORE_REREAD_VERSION=""
        return 0
    fi
    [[ "$RESTORE_REREAD_VERSION" == "$PREFLIGHT_STORE_VERSION" ]]
}

# Every restore condition must hold. Collect the independent reasons that
# disallow it, but re-read only when the first four conditions already hold.
ad_restore_decide() {
    RESTORE_WHY_NOT=()
    RESTORE_REREAD_FAILED=0
    RESTORE_REREAD_OUTPUT=""
    RESTORE_REREAD_VERSION=""
    [[ "$CUTOVER_CROSSING" -eq 1 && "$OUTCOME_CUTOVER" == not_committed ]] || return 1
    if [[ "$OUTCOME_STEPS" != false ]] && ! [[ "${MOVED:-0}" -eq 1 && -n "$STORE_SRC_PHYS" ]]; then
        RESTORE_WHY_NOT+=(steps_committed)
    fi
    if [[ "$PREV_RELEASE_PREDATES" -ne 1 ]]; then
        RESTORE_WHY_NOT+=(previous_release_current)
    fi
    [[ ${#RESTORE_WHY_NOT[@]} -eq 0 ]] || return 1
    if ! ad_restore_reread; then
        RESTORE_WHY_NOT+=(version_changed)
        return 1
    fi
    return 0
}

# Keep the invocation's spelling, including links and .., when making a
# relative path absolute. Empty source values still select the defaults.
ad_rerun_absolute_path() {
    local p="$1"
    if [[ -n "$p" && "$p" != /* ]]; then
        p="${ORIG_PWD%/}/$p"
    fi
    printf '%s\n' "$p"
}

# Schedule advice keeps its exact command. Journal advice replaces every
# backup value with a deliberately non-executable placeholder, never reusing
# a backup. Forward advice anchors paths to the original logical cwd and
# rebases only that cwd after a move; moved installer/source files get a note.
RERUN_MOVED_FILES=0
ad_rerun_cmd() {
    local kind="$1" a has=0 q replace=0 path_arg="" forward=0 moved=0
    local cwd="$ORIG_PWD" script="${BASH_SOURCE[0]:-}"
    RERUN_MOVED_FILES=0
    if [[ "$kind" == journal ]]; then
        case "${2:-}" in
            not_restored|forward|unestablished) forward=1 ;;
        esac
    fi
    if [[ "$forward" -eq 1 ]]; then
        script="$(ad_rerun_absolute_path "$script")"
        if [[ "${MOVED:-0}" -eq 1 && -z "$RESTORE_ASIDE" ]]; then
            moved=1
            case "$cwd" in
                "$OLD_INSTALL_ROOT"|"$OLD_INSTALL_ROOT/"*)
                    cwd="${DEFAULT_INSTALL_ROOT}${cwd#"$OLD_INSTALL_ROOT"}" ;;
            esac
            case "$script" in
                "$OLD_INSTALL_ROOT/"*) RERUN_MOVED_FILES=1 ;;
            esac
        fi
    else
        script="${SCRIPT_DIR}/${script##*/}"
    fi
    printf -v SUBST 'cd %q && bash %q' "$cwd" "$script"
    for a in ${ORIG_ARGS[@]+"${ORIG_ARGS[@]}"}; do
        if [[ "$replace" -eq 1 ]]; then
            SUBST+=' <new-backup-directory>'
            replace=0
            continue
        fi
        if [[ -n "$path_arg" ]]; then
            a="$(ad_rerun_absolute_path "$a")"
            if [[ "$moved" -eq 1 && "$path_arg" != --symlink-dir && "$a" == "$OLD_INSTALL_ROOT/"* ]]; then
                RERUN_MOVED_FILES=1
            fi
            printf -v q ' %q' "$a"
            SUBST="${SUBST}${q}"
            path_arg=""
            continue
        fi
        printf -v q ' %q' "$a"
        SUBST="${SUBST}${q}"
        if [[ "$kind" == schedule && "$a" == --schedule ]]; then
            has=1
        elif [[ "$kind" == journal && "$a" == --journal-backup ]]; then
            has=1
            replace=1
        elif [[ "$forward" -eq 1 ]]; then
            # These are all flags accepting relative paths. Journal roots
            # passed --check-only as absolute paths; backup values are
            # replaced above. Release tags and hashes are not paths.
            case "$a" in
                --binary|--admin-binary|--symlink-dir) path_arg="$a" ;;
            esac
        fi
    done
    if [[ "$has" -eq 0 ]]; then
        if [[ "$kind" == schedule ]]; then
            SUBST+=' --schedule'
        else
            SUBST+=' --journal-backup <new-backup-directory>'
        fi
    fi
}

ad_rerun_advice() {
    ad_rerun_cmd "$1" "${2:-}"
    if [[ "$SCRIPT_REAL" -eq 1 ]]; then
        printf '    %s\n' "$SUBST" >&2 || true
    else
        echo "  run the same install command again, with --journal-backup naming a new directory" >&2 || true
    fi
    if [[ "$RERUN_MOVED_FILES" -eq 1 ]]; then
        echo "  Installer or source files named under $OLD_INSTALL_ROOT now sit under $DEFAULT_INSTALL_ROOT/...; re-run from wherever you keep the installer." >&2 || true
    fi
    echo "  The new backup directory must be absolute, must not exist yet, and must lie outside every legacy input root and AD's old and new directories." >&2 || true
}

# After a committed/unknown outcome, the installer's ordinary list route can
# finish schema steps but cannot replace migrate-journal's verification.
ad_verify_retry_cmd() {
    local a q replace=0
    printf -v SUBST '%q --store-path %q migrate-journal' "$ADMIN_CANONICAL" "$state_db"
    for a in ${JOURNAL_ARGS[@]+"${JOURNAL_ARGS[@]}"}; do
        if [[ "$replace" -eq 1 ]]; then
            SUBST+=' <new-verification-backup-directory>'
            replace=0
            continue
        fi
        printf -v q ' %q' "$a"
        SUBST="${SUBST}${q}"
        [[ "$a" == --backup-dir ]] && replace=1
    done
    return 0
}

# Human and machine reports share the same kept-item builders.
ad_restore_kept_message() {
    ad_restore_copies_json | jq -r '.[] | "  kept copy: " + .copy + " -> " + .belongs_at' >&2 || true
    ad_restore_remove_json | jq -r '.[] | "  still to remove: " + .' >&2 || true
    if [[ -n "$RESTORE_ASIDE" ]]; then
        echo "  set aside: $RESTORE_ASIDE" >&2 || true
    fi
}

# Messages run only after the install and copy-retention decision are final.
# Keep every write best-effort, including pipelines into a closed stderr.
ad_cutover_message() {
    local row="$1" why residual=0
    case "$CUTOVER_INTERRUPTED_IN" in
        before_start)
            echo "install.sh: the install was interrupted (SIG${CUTOVER_INTERRUPTED}) after the new binaries were installed and before migrate-journal started; migrate-journal never ran, so nothing was migrated." >&2 || true ;;
        running)
            if [[ -n "$OUTCOME_ENVELOPE" ]]; then
                echo "install.sh: the install was interrupted (SIG${CUTOVER_INTERRUPTED}) while migrate-journal ran; its own outcome follows." >&2 || true
            elif [[ "$OUTCOME_CUTOVER" == committed ]]; then
                echo "install.sh: the install was interrupted (SIG${CUTOVER_INTERRUPTED}) while migrate-journal ran. It had already begun the cutover commit, so it finished the cutover and exited 0; the install stopped after checking the store's version, before the hook, MCP and schedule steps." >&2 || true
            fi ;;
    esac
    if [[ -n "$CUTOVER_LATE_INTERRUPT" ]]; then
        echo "install.sh: an interrupt arrived after migrate-journal's attempt and was ignored, so that the outcome below could be settled; it is the attempt's own." >&2 || true
    fi
    case "$row" in
        restored) echo "install.sh: the cutover was refused before changing the previous install's store; the previous install was put back as it was before the run." >&2 || true ;;
        restore_incomplete) echo "install.sh: the restore could not finish; the install is partly restored: $RESTORE_FAILED" >&2 || true ;;
        not_restored) echo "install.sh: the cutover did not commit; this release stays installed." >&2 || true ;;
        forward) echo "install.sh: the cutover attempt did not finish successfully; this release stays installed." >&2 || true ;;
        unestablished) echo "install.sh: the installer could not establish how the attempt ended (migrate-journal exited $OUTCOME_RC with no error envelope); this release stays installed and the copies are kept." >&2 || true ;;
    esac
    echo "  reason: $OUTCOME_REASON" >&2 || true
    echo "  action: $OUTCOME_ACTION" >&2 || true
    case "$row" in
        restored)
            echo "  The binaries, migration sentinel and managed PATH link are as before the run." >&2 || true
            if [[ "$RESTORE_REREAD_FAILED" -eq 1 ]]; then
                echo "  The version cross-check at $RESTORE_REREAD_PATH could not be made; the attempt reported not_committed:" >&2 || true
                printf '%s\n' "$RESTORE_REREAD_OUTPUT" | sed 's/^/    /' >&2 || true
            fi
            echo "  Fix the reported cause, then re-run this release's installer with a new backup directory:" >&2 || true
            ad_rerun_advice journal "$row" ;;
        not_restored)
            for why in ${RESTORE_WHY_NOT[@]+"${RESTORE_WHY_NOT[@]}"}; do
                case "$why" in
                    steps_committed)
                        residual=1
                        if [[ "$OUTCOME_STEPS" == unknown ]]; then
                            echo "  The attempt did not establish whether earlier schema steps committed; the store may have moved past the previous release." >&2 || true
                        else
                            echo "  This attempt committed earlier schema steps before the cutover, so the store has moved past the previous release." >&2 || true
                        fi ;;
                    version_changed)
                        echo "  The store $RESTORE_REREAD_PATH was at v$PREFLIGHT_STORE_VERSION in pre-flight and reads v$RESTORE_REREAD_VERSION after the attempt." >&2 || true ;;
                    previous_release_current)
                        echo "  The administrator installed before the run ($PREV_ADMIN) does not predate the cutover, so there is no earlier release to put back." >&2 || true ;;
                esac
            done
            if [[ "$PREV_RELEASE_PREDATES" -eq 1 && ( "$residual" -eq 1 || "$OUTCOME_STEPS" == unknown ) ]]; then
                echo "  The installer cannot put the previous release back: the store has moved, or may have moved, past it." >&2 || true
                echo "  To go back instead, restore the store from the journal backup directory $JOURNAL_BACKUP (filled before any schema step), and install the previous release with its own installer (that release's install.sh must support this host), as docs/operator-actions.md \"Going back to the previous release\" describes." >&2 || true
            fi
            echo "  To complete forward, fix the reported cause, then re-run this release's installer with a new backup directory:" >&2 || true
            ad_rerun_advice journal "$row" ;;
        forward|unestablished)
            if [[ "$OUTCOME_CUTOVER" == committed ]]; then
                echo "  The cutover committed: the store's input is now in SQLite and the previous release cannot open it." >&2 || true
            else
                echo "  Whether the cutover committed is unknown." >&2 || true
            fi
            echo "  Never reset the schema or restore replayable input. Keep the maintenance boundary through verification." >&2 || true
            if [[ "$row" == unestablished ]]; then
                ad_restore_kept_message
                echo "  To go back instead, put the kept copies back only after the three checks in docs/operator-actions.md \"Putting back a kept install\"." >&2 || true
            fi
            echo "  Fix the reported cause; for migration_lock_held, let the other administrator run end." >&2 || true
            echo "  Re-run this release's installer with a new backup directory to finish any pending schema step:" >&2 || true
            ad_rerun_advice journal "$row"
            echo '  Then run the verification retry until it exits 0 and its result shows "authority": "sqlite", choosing a new verification backup directory for each attempt:' >&2 || true
            ad_verify_retry_cmd
            printf '    %s\n' "$SUBST" >&2 || true ;;
        restore_incomplete)
            if [[ -n "$RESTORE_DONE" ]]; then
                printf '%s' "$RESTORE_DONE" | sed 's/^/  /' >&2 || true
            fi
            ad_restore_kept_message
            echo '  Put the copies listed in details.copies back with mv, as docs/operator-actions.md "Putting back a kept install" describes.' >&2 || true
            if [[ "${MOVED:-0}" -eq 1 ]]; then
                echo "  Use its moved-install form, which puts the pre-move directory back before checking its list." >&2 || true
            fi ;;
    esac
}

# Settle before printing anything. All envelopes use ad_outcome_exit; the
# only relay-only row is a complete restore of ErrJournalMigrationRefused.
ad_cutover_settle() {
    local row cause=ErrJournalMigrationIncomplete cutover="$OUTCOME_CUTOVER" extra='{}'
    local copies remove attempt
    local -a relayed
    relayed=()
    if ad_restore_decide; then
        if ad_restore_run; then
            row=restored
            cause=ErrJournalMigrationRefused
        else
            row=restore_incomplete
            cause=ErrSchemaVerifyFailed
        fi
    elif [[ "$OUTCOME_CUTOVER" == unestablished ]]; then
        row=unestablished
        RESTORE_KEEP_COPIES=1
    elif [[ "$OUTCOME_CUTOVER" == not_committed ]]; then
        row=not_restored
    else
        row=forward
    fi
    case "$cutover" in
        not_committed|committed|unknown) ;;
        *) cutover=unknown ;;
    esac
    case "$row" in
        restored|not_restored)
            OUTCOME_ACTION="fix the reported cause, then re-run this release's installer with a new --journal-backup directory" ;;
        restore_incomplete)
            OUTCOME_ACTION='put the copies in details.copies back with mv as docs/operator-actions.md "Putting back a kept install" describes' ;;
        *)
            OUTCOME_ACTION="fix the reported cause (let another administrator holding the migration lock end), re-run this release's installer with a new --journal-backup, then run migrate-journal with a new --backup-dir until it exits 0 with authority \"sqlite\""
            if [[ "$row" == unestablished ]]; then
                OUTCOME_ACTION+='; or put the kept copies back only after the three checks in docs/operator-actions.md "Putting back a kept install"'
            fi ;;
    esac
    if [[ -n "$OUTCOME_ENVELOPE" ]]; then
        relayed=(--relayed)
        if [[ -n "$OUTCOME_REFUSAL" ]]; then
            OUTCOME_REASON="$(printf '%s\n' "$OUTCOME_REFUSAL" | jq -r --arg name "$OUTCOME_ERR_NAME" '(.reason? | strings) // $name')"
            OUTCOME_ACTION="$(printf '%s\n' "$OUTCOME_REFUSAL" | jq -r --arg action "$OUTCOME_ACTION" '(.action? | strings) // $action')"
            extra="$(jq -nc --argjson refusal "$OUTCOME_REFUSAL" '{refusal: $refusal}')"
        else
            OUTCOME_REASON="$OUTCOME_ERR_NAME"
        fi
    else
        case "$row" in
            unestablished) OUTCOME_REASON=outcome_unestablished ;;
            restore_incomplete) OUTCOME_REASON=restore_incomplete ;;
            *)
                OUTCOME_REASON=interrupted
                OUTCOME_ACTION="$(printf '%s\n' "$OUTCOME_REFUSAL" | jq -r '.action')" ;;
        esac
    fi
    if [[ "$RESTORE_KEEP_COPIES" -eq 1 && "$row" != restored ]]; then
        copies="$(ad_restore_copies_json)"
        remove="$(ad_restore_remove_json)"
        attempt="$(ad_restore_attempt_store)"
        extra="$(jq -nc --argjson extra "$extra" --argjson copies "$copies" --argjson remove "$remove" \
            --arg store "$attempt" '$extra + {attempt_store: $store, copies: $copies, remove: $remove}')"
    fi
    ad_relay_output "$OUTCOME_OUTPUT" || true
    ad_move_outcome_notes "$row" || true
    ad_cutover_message "$row" || true
    if [[ "$row" == restored && "$OUTCOME_ERR_NAME" == "$cause" ]]; then
        ad_exit_5 "$cause"
    fi
    ad_outcome_exit ${relayed[@]+"${relayed[@]}"} "$cause" "$OUTCOME_REASON" "$OUTCOME_ACTION" "$cutover" "$extra" || true
}

ad_cutover_outcome() {
    ad_journal_outcome_read "$1" "$2"
    ad_cutover_settle
}

CUTOVER_CROSSING=0
ad_journal_args
if [[ -n "$PREFLIGHT_STORE_VERSION" && "$PREFLIGHT_STORE_VERSION" -ne 0 ]]; then
    if ((PREFLIGHT_STORE_VERSION < INPUT_CUTOVER_VERSION)); then
        CUTOVER_CROSSING=1
        # The real verifier always checks both HOME input roots, even
        # while a move is due; --check-only does not reject link components.
        if [[ -L "$DEFAULT_INSTALL_ROOT" ]]; then
            echo "install.sh: $DEFAULT_INSTALL_ROOT is a link; refusing the native-input cutover." >&2 || true
            echo "  Nothing was installed or changed." >&2 || true
            echo "  Replace the link with the directory it points to, then re-run this release's installer with a new backup directory:" >&2 || true
            ad_rerun_advice journal
            ad_outcome_exit ErrJournalMigrationRefused legacy_root_symlink \
                "replace the link at $DEFAULT_INSTALL_ROOT with the directory it points to, then re-run this release's installer with a new --journal-backup directory" not_committed
        fi
        # A compatibility link counts as no old directory for the move, but
        # the cutover verifier still checks its input root.
        if [[ -L "$OLD_INSTALL_ROOT" ]]; then
            printf -v compat_link_quoted "'%s'" "${OLD_INSTALL_ROOT//\'/\'\\\'\'}"
            echo "install.sh: $OLD_INSTALL_ROOT is a link; refusing the native-input cutover." >&2 || true
            echo "  Nothing was installed or changed." >&2 || true
            echo "  The cutover always checks $OLD_INSTALL_ROOT/input and refuses any symbolic link in its path." >&2 || true
            echo "  To fix: first check [store] db_path in config.toml, ~/.claude/settings.json hooks, MCP registrations, scheduled sweeps, PATH links and entries, and scripts for paths under $OLD_INSTALL_ROOT and point them at $DEFAULT_INSTALL_ROOT." >&2 || true
            echo "  Then remove only the link (no trailing slash):" >&2 || true
            printf '    rm -- %s\n' "$compat_link_quoted" >&2 || true
            echo "  Then re-run this release's installer with a new backup directory:" >&2 || true
            ad_rerun_advice journal
            ad_outcome_exit ErrJournalMigrationRefused legacy_root_symlink \
                "first check [store] db_path in config.toml, ~/.claude/settings.json hooks, MCP registrations, scheduled sweeps, PATH links and entries, and scripts for paths under $OLD_INSTALL_ROOT and point them at $DEFAULT_INSTALL_ROOT, then remove only the link with rm -- $compat_link_quoted (no trailing slash), then re-run this release's installer with a new --journal-backup directory" not_committed
        fi
        if [[ "$MOVE_DUE" -ne 1 ]]; then
            ad_crossing_live_rows_check
        fi
        ad_journal_check_only
        if [[ "$MOVE_DUE" -eq 1 ]]; then
            ad_backup_install_dirs_check
        fi
        ad_prev_release_probe
        if [[ "$PREV_RELEASE_PREDATES" -eq 1 ]]; then
            ad_restore_copies_make
        fi
    elif ((PREFLIGHT_STORE_VERSION < STORE_SCHEMA_VERSION)); then
        case "$PREFLIGHT_AUTHORITY" in
            recorded) ;;
            missing)
                ad_cutover_refuse_state native_input_authority_missing "not recorded" \
                    "the native_input_authority row of ${state_db_name}'s store_meta table" ;;
            *)
                ad_cutover_refuse_state cutover_state_unreadable "unreadable" \
                    "${state_db_name}'s store_meta table and its native_input_authority row" ;;
        esac
    fi
fi

echo "install.sh: pre-flight OK"
ad_print_agent_summary
echo "  tmux    : $(tmux -V 2>/dev/null || echo '<unknown>')"
if [[ "$state_db_name" != state.db ]]; then
    echo "  store   : $state_db ([store] db_path in ${PREFLIGHT_CONFIG})"
fi
if [[ "$CUTOVER_CROSSING" -eq 1 ]]; then
    echo "  cutover : $state_db at v${PREFLIGHT_STORE_VERSION} crosses the native-input cutover"
fi

# Source resolution can already have run for readonly preflight.
ad_prepare_source_binaries

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
#      never copied as files; the copy, read through a read-write connection
#      (b.up4), must have the same user_version and .sha3sum --schema.
#   6. Every other copied file is checked byte for byte.
#   7. The path keys in the new config.toml are rewritten (SR-11.7 "Path
#      keys"); only the new copy is changed.
#   8. The kept directory's name is written into the marker.
#   9. The old directory is renamed to that name.
#  10. The marker is removed.
# The move makes no backup of its own: the kept directory holds the
# original. On a crossing upgrade its kept-directory, link and reference
# notes wait for the cutover outcome: a restore can undo this move.
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
    local t tmp_choice="${TMPDIR:-/tmp}" tmp_dir root root_phys
    # Resolve the directory itself before mktemp: even creating and removing
    # a rejected file would change an original directory's mtime. Appending
    # /. makes ad_phys_place follow directory aliases and resolve .. first.
    tmp_dir="$(ad_phys_place "$tmp_choice/." 2>/dev/null)" || {
        MOVE_CAUSE="cannot make a temp file to hold a copy of $1"
        return 1
    }
    tmp_dir="$(ad_clean_path "$tmp_dir")"
    for root in "$OLD_INSTALL_ROOT" "$DEFAULT_INSTALL_ROOT"; do
        root_phys="$root"
        if [[ -d "$root" ]]; then
            root_phys="$(ad_phys_place "$root/." 2>/dev/null)" || {
                MOVE_CAUSE="cannot resolve the directory $root before staging $1"
                return 1
            }
        fi
        root_phys="$(ad_clean_path "$root_phys")"
        root="$(ad_clean_path "$root")"
        if [[ "$tmp_choice" == "$root" || "$tmp_choice" == "$root"/* \
            || "$tmp_dir" == "$root_phys" || "$tmp_dir" == "$root_phys"/* ]]; then
            MOVE_CAUSE="the temp directory ($tmp_choice) is inside AD's directories; set TMPDIR to a directory outside them"
            return 1
        fi
    done
    t="$(TMPDIR="$tmp_dir" mktemp -t agent-director.stage.XXXXXX)" || {
        MOVE_CAUSE="cannot make a temp file to hold a copy of $1"
        return 1
    }
    printf -v "$2" '%s' "$t"
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
#
# <source> is only ever opened with sqlite3 -readonly (SR-11.7). The copy is
# read through a read-write connection instead (b.up4): it is the installer's
# own new file, and the reads change none of its content. A WAL-mode store's
# backup is a WAL-mode file with no -wal or -shm, which a read-only
# connection on some SQLite builds, Apple's 3.51.0 among them, cannot open
# ("unable to open database file (14)"); a read-write connection makes them
# on a copy its owner can write (it has the original's mode by then).
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
    v_dst="$(sqlite3 -batch -init /dev/null -cmd ".timeout ${store_busy_timeout_ms}" "$dst" "PRAGMA user_version;" 2>&1)" || true
    if [[ ! "$v_src" =~ ^[0-9]+$ || "$v_src" != "$v_dst" ]]; then
        MOVE_CAUSE="the copy $dst of the store has user_version '${v_dst}', the original's is '${v_src}'"
        return 1
    fi
    h_src="$(sqlite3 -readonly -batch -init /dev/null -cmd ".timeout ${store_busy_timeout_ms}" "$src" ".sha3sum --schema" 2>&1)" || true
    h_dst="$(sqlite3 -batch -init /dev/null -cmd ".timeout ${store_busy_timeout_ms}" "$dst" ".sha3sum --schema" 2>&1)" || true
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
# keys"). Sets REWRITE_NEWVAL (empty: leave the line as written). Sets
# MOVE_CAUSE and fails when the place cannot be resolved.
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
            # a key by its physical place). The move renames the old directory
            # away, so the value as written would name nothing afterwards (a
            # relative one would resolve against the new directory, where a
            # copied relative link points one level deeper). Every form is
            # written as the absolute physical path it reaches today.
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
# migration's own copies (*.pre-agent-rename.*) are not searched. The ordinary
# call defers these notes on a crossing upgrade, since it may restore; S1's
# stop reports them directly on stderr as before.
ad_move_print_references() {
    local hits old_re boundary="([/\"']|$)"
    # Escape the absolute path so only the trailing boundary is a pattern.
    old_re="$(printf '%s\n' "$OLD_INSTALL_ROOT" | sed 's/[][\\.^$*+?(){}|]/\\&/g')"
    local -a patterns=(
        -e "${old_re}${boundary}"
        -e '~/\.agent-director'"$boundary"
        -e '\$HOME/\.agent-director'"$boundary"
        -e '\$\{HOME\}/\.agent-director'"$boundary"
    )
    hits="$( { grep -n -E -I "${patterns[@]}" "${DEFAULT_INSTALL_ROOT}/config.toml" 2>/dev/null | sed "s|^|${DEFAULT_INSTALL_ROOT}/config.toml:|"; grep -r -n -E -I --exclude='*.pre-agent-rename.*' "${patterns[@]}" "${DEFAULT_INSTALL_ROOT}/templates" 2>/dev/null; } || true)"
    if [[ -n "$hits" ]]; then
        echo "  references: these lines still name the old directory. They were left as written; change each to the same place under $NEW_ROOT_CLEAN (or remove it) by hand, since the old directory is kept only until you delete it ($1):" || ad_cutover_write_failed
        printf '%s\n' "$hits" | sed 's/^/    /' || ad_cutover_write_failed
    fi
}

ad_move_kept_notes() {
    echo "  kept    : $MOVED_KEPT (the old directory as it was before the move, old binaries included; delete it once the upgrade is confirmed)"
    if [[ "$KEEP_PRIOR" -eq 1 ]]; then
        echo "  prior   : --keep-prior made no snapshot on this install: the new bin and admin directories hold no earlier binary. The way back is operator-actions.md, \"Going back to the previous release\""
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
    ad_move_note ad_move_kept_notes

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

MANAGED_LINK=""
PRIOR="${CANONICAL}.prior"
TMP="${CANONICAL}.tmp.$$"
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
# SR-2.16 starts at the first replacement. Keep INT/HUP catchable until
# migrate-journal starts, so it can cancel rather than inherit an ignore.
if [[ "$CUTOVER_CROSSING" -eq 1 ]]; then
    ad_cutover_arm_traps
fi
mv "$TMP" "$CANONICAL"
mv "$ADMIN_TMP" "$ADMIN_CANONICAL"

echo "  binary  : $CANONICAL" || ad_cutover_write_failed

# --------------------------------------------------------------------
# Optional PATH symlink — agent-director only. agent-director-admin never
# gets one, under any option (b.vqr).
# --------------------------------------------------------------------

target="$(ad_managed_link_target)"
if [[ -n "$target" ]]; then
    link_tmp="${target}.new"
    ln -sfn "$CANONICAL" "$link_tmp"
    mv -f "$link_tmp" "$target"
    link_tmp=""
    MANAGED_LINK="$target"
    echo "  symlink : $target → $CANONICAL" || ad_cutover_write_failed
elif [[ -n "$MANAGED_LINK_SKIP" ]]; then
    echo "  symlink : skipped — $MANAGED_LINK_SKIP" || ad_cutover_write_failed
elif [[ "$NO_SYMLINK" -eq 0 && -n "$SYMLINK_DIR" ]]; then
    if [[ ! -d "$SYMLINK_DIR" ]]; then
        echo "  symlink : skipped — $SYMLINK_DIR does not exist" || ad_cutover_write_failed
    else
        echo "  symlink : skipped — $SYMLINK_DIR contains whitespace" || ad_cutover_write_failed
    fi
fi

# After a move, every other link into the old bin directory that pre-flight
# found (OLD_BIN_LINKS; the one this install manages was re-pointed above),
# and each $PATH entry naming that directory, is printed with its fix. The
# installer changes neither: a link elsewhere, and a shell startup file, are
# the operator's (SR-11.7).
ad_move_link_notes() {
    local old_link old_entry
    for old_link in ${OLD_BIN_LINKS[@]+"${OLD_BIN_LINKS[@]}"}; do
        if [[ "$old_link" == "$MANAGED_LINK" ]]; then
            continue
        fi
        echo "  old link: $old_link still leads into the old directory's bin, which is kept only until you delete it. Re-point it:" || ad_cutover_write_failed
        printf '    ln -sfn %q %q\n' "$CANONICAL" "$old_link" || ad_cutover_write_failed
    done
    for old_entry in ${OLD_BIN_PATH_ENTRIES[@]+"${OLD_BIN_PATH_ENTRIES[@]}"}; do
        echo "  old PATH: \$PATH names $old_entry, the old directory's bin, which is kept only until you delete it. In your shell startup file (~/.bashrc, ~/.zshrc, ...), replace it with ${DEFAULT_BIN_DIR}, then open a new shell." || ad_cutover_write_failed
    done
}
if [[ "$MOVED" -eq 1 ]]; then
    ad_move_note ad_move_link_notes
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
    echo "  The new binaries are already installed, and the store is not opened. Either:" >&2 || ad_cutover_write_failed
    echo "    - fix the cause, then run this release's install.sh again. It completes the" >&2 || ad_cutover_write_failed
    echo "      migration: it changes nothing in a file already migrated and makes no new" >&2 || ad_cutover_write_failed
    echo "      backup of one, and then it migrates the store. Or" >&2 || ad_cutover_write_failed
    echo "    - take the way back to the previous release: see \"Going back to the" >&2 || ad_cutover_write_failed
    echo "      previous release\" and \"A migration write failed after the binary swap\" in" >&2 || ad_cutover_write_failed
    echo "      docs/operator-actions.md." >&2 || ad_cutover_write_failed
}

# ad_migrate_fail_write <file> <error> — a migration write failed: remove
# the temp file, report the file and the error and the two ways on, and exit
# 1, as the installer's other failed writes do. <file> is as it was before.
ad_migrate_fail_write() {
    if [[ -n "$migrate_tmp" ]]; then
        rm -f "$migrate_tmp"
        migrate_tmp=""
    fi
    echo "install.sh: the agent-rename migration could not write $1; stopping before the store is opened." >&2 || ad_cutover_write_failed
    echo "  file    : $1" >&2 || ad_cutover_write_failed
    printf '%s\n' "$2" | sed '1s/^/  error   : /;2,$s/^/            /' >&2 || ad_cutover_write_failed
    echo "  $1 is as it was, and every copy this install made is kept." >&2 || ad_cutover_write_failed
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
        ad_move_print_references "$MOVED_KEPT" >&2 || ad_cutover_write_failed
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
    echo "install.sh: the agent-rename migration refuses $2, which pre-flight accepted; stopping before the store is opened." >&2 || ad_cutover_write_failed
    printf '%s\n' "$3" | sed '$d' >&2 || ad_cutover_write_failed
    echo "  $2 changed since pre-flight." >&2 || ad_cutover_write_failed
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
    echo "  migrate : $file migrated for the agent rename (copy: $bak)" || ad_cutover_write_failed
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
        echo "  migrate : config.toml and the templates need no agent-rename change" || ad_cutover_write_failed
    fi
fi

# The move's other references into the old directory, printed after the
# migration so their line numbers are those of the files as they now are.
if [[ "$MOVED" -eq 1 ]]; then
    ad_move_note ad_move_print_references "$MOVED_KEPT"
fi

# --------------------------------------------------------------------
# Schema migration flow — see SKILL.md's six-step sentinel flow.
#
# The binary swap has completed. Read the actual SQLite/WAL user_version,
# probe for the required transition, and write its exact authorization
# sentinel beside the resolved $state_db. A native-input cutover cannot run
# through an ordinary opener, even with a preexisting sentinel: it requires
# the administrator's maintained writer exclusion, complete root inventory
# and exclusive pre-hop backup. Invoking-HOME roots apply even to a custom DB.
#
# Step 4 invokes migrate-journal exactly when pre-flight's cutover decision
# found a crossing upgrade (CUTOVER_CROSSING); every other store takes the
# ordinary list. Every quiescence verification migrate-journal makes stays
# here, after the swap: pre-flight only refused. Step 5 checks the target
# version. Keep incompatible hooks, helpers, servers and launchers excluded
# through committed verification. A failed attempt restores only when the
# previous release's store is shown unchanged; all other outcomes keep this
# release. A committed or uncertain refusal requires the installer re-run
# and direct administrator-verification retry in docs/operator-actions.md;
# rerunning this installer on a current schema takes the ordinary list path.
# SQLite keeps authority after commit even if legacy retirement is pending.
# --------------------------------------------------------------------

# (ad_user_version, ad_is_version, ad_got_version, ad_show_unreadable_version,
# ad_fail_unreadable_version and ad_err_name are defined above, in the
# pre-flight section on the agent-rename gate, which reads the store's
# version first.)

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

# ad_fail_config_refused <stderr> — agent-director refused its config file
# (ErrConfigMalformed) at a store-opening verb: report it and exit 5.
# <stderr> is that verb's stderr, whose envelope names the file and what is
# wrong with it. The binary loads the config before it opens the store, so
# the refusal says nothing about state.db: the store was not opened, no
# migration ran, and none can be authorized until the config loads. A
# re-run after the fix starts over, and its step-3 probe then authorizes any
# pending migration (b.7b4).
ad_fail_config_refused() {
    echo "install.sh: agent-director refused its config file (ErrConfigMalformed)" >&2 || ad_cutover_write_failed
    echo "  config  : ${DEFAULT_INSTALL_ROOT}/config.toml" >&2 || ad_cutover_write_failed
    printf '%s\n' "$1" | sed 's/^/  /' >&2 || ad_cutover_write_failed
    echo "  Fix what the error above names in the config file, then re-run this" >&2 || ad_cutover_write_failed
    echo "  install." >&2 || ad_cutover_write_failed
    ad_exit_5 ErrConfigMalformed
}

# ---- Step 2: read the DB's ACTUAL current schema version ----
if [[ ! -e "$state_db" ]]; then
    # Fresh install: no DB on disk. No sentinel is needed — the step-4
    # open fresh-creates state.db at the binary's current schemaVersion.
    db_version_before=""
    echo "  schema  : no existing ${state_db_name} — fresh create on first open" || ad_cutover_write_failed
else
    db_version_before_rc=0
    db_version_before="$(ad_user_version "$state_db")" || db_version_before_rc=$?
    if ! ad_got_version "$db_version_before_rc" "$db_version_before"; then
        # Without the version no migration can be authorized, and the
        # step-4 open would refuse an older store anyway: stop here.
        echo "install.sh: reading ${state_db_name}'s schema version FAILED" >&2 || ad_cutover_write_failed
        echo "  state.db: $state_db" >&2 || ad_cutover_write_failed
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
            echo "install.sh: reading ${state_db_name}'s schema version FAILED" >&2 || ad_cutover_write_failed
            echo "  state.db: $state_db" >&2 || ad_cutover_write_failed
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
        echo "  schema  : migration v${db_version_before}→v${target_version} ran at the probe (agent-director list), authorized by a sentinel written before this install (sentinel $sentinel)" || ad_cutover_write_failed
    elif [[ -z "$probe_name" || "$probe_name" == ErrSchemaMismatch ]]; then
        echo "  schema  : ${state_db_name} at v${db_version_before}; no migration authorization needed" || ad_cutover_write_failed
    elif [[ -z "$target_version" ]]; then
        echo "  schema  : ${state_db_name} at v${db_version_before}; could not tell whether a migration is needed (agent-director list failed: ${probe_name})" || ad_cutover_write_failed
    elif [[ "$db_version_before" == "$target_version" ]]; then
        # Defensive: probe reported a target equal to current. Nothing to do.
        echo "  schema  : ${state_db_name} already at target v${target_version}; no sentinel written" || ad_cutover_write_failed
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
            echo "install.sh: writing the migration sentinel FAILED" >&2 || ad_cutover_write_failed
            echo "  sentinel: $sentinel" >&2 || ad_cutover_write_failed
            echo "  mktemp could not create a temp file beside it (its error is above), so" >&2 || ad_cutover_write_failed
            echo "  no migration was authorized: ${state_db_name} is still at v${db_version_before}." >&2 || ad_cutover_write_failed
            echo "  The new agent-director does not open it until it is at v${target_version}." >&2 || ad_cutover_write_failed
            echo "  Fix what mktemp's error names (a directory you cannot write, say, or a" >&2 || ad_cutover_write_failed
            echo "  full disk), then re-run this install: it authorizes the migration again." >&2 || ad_cutover_write_failed
            # A re-run alone does not fix it: someone must make the
            # directory writable or free space first.
            ad_exit_5 ErrSchemaVerifyFailed
        fi
        printf '{"from": %d, "to": %d}\n' "$db_version_before" "$target_version" > "$tmp_sentinel"
        chmod 0600 "$tmp_sentinel" 2>/dev/null || true
        mv -f "$tmp_sentinel" "$sentinel"
        echo "  schema  : authorized migration v${db_version_before}→v${target_version} (sentinel $sentinel)" || ad_cutover_write_failed
    fi
fi

# ---- Step 4: run the selected open once and retain its status/details ----
# The route follows pre-flight's cutover decision: a crossing upgrade runs
# the administrator's migrate-journal with the journal flags pre-flight's
# --check-only accepted (the cutover needs it even with an exact sentinel),
# and every other store the ordinary list, including a store past the
# cutover with recorded authority whose probe asked for a sentinel.
# help/version stay DB-free. Capture one invocation's status and diagnostics
# so they describe the same attempt. Keep the maintenance boundary through
# verification; a failed crossing attempt settles through SR-2.16 before
# reporting. exec leaves no intermediate capture shell that a group signal
# could end before the attempt finishes. The non-crossing list route keeps
# its existing failure rules.
open_command=("$CANONICAL" list)
if [[ "$CUTOVER_CROSSING" -eq 1 ]]; then
    open_command=("$ADMIN_CANONICAL" --store-path "$state_db" migrate-journal "${JOURNAL_ARGS[@]}")
fi
open_rc=0
if [[ "${open_command[0]}" == "$ADMIN_CANONICAL" ]]; then
    CUTOVER_PHASE=running
    open_err="$(exec "${open_command[@]}" 2>&1)" || open_rc=$?
else
    open_err="$("${open_command[@]}" 2>&1 >/dev/null)" || open_rc=$?
fi
if [[ "$open_rc" -eq 0 ]]; then
    if [[ "${open_command[0]}" == "$ADMIN_CANONICAL" ]]; then
        if [[ -z "$CUTOVER_INTERRUPTED" ]]; then
            ad_cutover_disarm_traps
        fi
        printf "  journal : %s\n" "$open_err" || ad_cutover_write_failed
        ad_move_notes_flush
    fi
else
    if [[ "$CUTOVER_CROSSING" -eq 1 ]]; then
        ad_cutover_ignore_interrupts
        ad_cutover_outcome "$open_rc" "$open_err"
    fi
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
    echo "install.sh: ${state_db_name} was not created by the store open" >&2 || ad_cutover_write_failed
    echo "  state.db: $state_db" >&2 || ad_cutover_write_failed
    echo "  config  : ${DEFAULT_INSTALL_ROOT}/config.toml" >&2 || ad_cutover_write_failed
    echo "  The store open (agent-director list) succeeded, yet there is no store" >&2 || ad_cutover_write_failed
    echo "  at the state.db path above, where install.sh expected it. agent-director" >&2 || ad_cutover_write_failed
    echo "  opens its store at [store] db_path in the config file above" >&2 || ad_cutover_write_failed
    echo "  (~/.config/agent-director/state.db when unset), and install.sh read that file" >&2 || ad_cutover_write_failed
    echo "  before the open. So either db_path changed since and agent-director" >&2 || ad_cutover_write_failed
    echo "  opened a store somewhere else, or something removed or moved the store" >&2 || ad_cutover_write_failed
    echo "  after the open." >&2 || ad_cutover_write_failed
    echo "  Check db_path in the config file, and whether anything (a cleanup, say)" >&2 || ad_cutover_write_failed
    echo "  removed or moved the store. To keep a moved store's sessions, put it" >&2 || ad_cutover_write_failed
    echo "  back at the state.db path above first: with no store there, a re-run" >&2 || ad_cutover_write_failed
    echo "  creates a new, empty one. Then re-run this install: it reads db_path" >&2 || ad_cutover_write_failed
    echo "  again and opens the store there. If a re-run fails this same way, the" >&2 || ad_cutover_write_failed
    echo "  installed agent-director does not open its store where this install.sh" >&2 || ad_cutover_write_failed
    echo "  expects it: contact the maintainers." >&2 || ad_cutover_write_failed
    ad_exit_5 ErrSchemaVerifyFailed
fi
chmod 0600 "$state_db" 2>/dev/null || true
db_version_after_rc=0
db_version_after="$(ad_user_version "$state_db")" || db_version_after_rc=$?
schema_shown="v${db_version_after}"
ad_got_version "$db_version_after_rc" "$db_version_after" || schema_shown="<unreadable>"
echo "  state.db: $(stat -c '%a' "$state_db" 2>/dev/null || stat -f '%Lp' "$state_db") at $state_db (schema ${schema_shown})" || ad_cutover_write_failed

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
        echo "install.sh: schema migration verification FAILED" >&2 || ad_cutover_write_failed
        echo "  expected user_version: $target_version" >&2 || ad_cutover_write_failed
        ad_fail_unreadable_version "$db_version_after_rc" "$db_version_after" "check the migration"
    fi
    echo "install.sh: warning: ${state_db_name}'s schema version is unreadable after the store open" >&2 || ad_cutover_write_failed
    ad_show_unreadable_version "$db_version_after"
    echo "  The store open (agent-director list) succeeded and no migration was" >&2 || ad_cutover_write_failed
    echo "  authorized, so the install carries on. Check the version later with:" >&2 || ad_cutover_write_failed
    printf '    sqlite3 -batch -init /dev/null -cmd ".timeout %s" %q "PRAGMA user_version;"\n' "$store_busy_timeout_ms" "$state_db" >&2 || ad_cutover_write_failed
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
        echo "install.sh: schema migration verification FAILED" >&2 || ad_cutover_write_failed
        echo "  expected user_version: $target_version" >&2 || ad_cutover_write_failed
        echo "  actual   user_version: $db_version_after" >&2 || ad_cutover_write_failed
        echo "  The store open (agent-director list) succeeded, and a successful" >&2 || ad_cutover_write_failed
        echo "  open leaves ${state_db_name} at v${target_version}: any authorized migration" >&2 || ad_cutover_write_failed
        echo "  has run, and its sentinel is consumed. Yet the read after the open" >&2 || ad_cutover_write_failed
        echo "  gives v${db_version_after}: ${state_db_name} changed after the open, or the read" >&2 || ad_cutover_write_failed
        echo "  is wrong. Check its version now:" >&2 || ad_cutover_write_failed
        printf '    sqlite3 -batch -init /dev/null -cmd ".timeout %s" %q "PRAGMA user_version;"\n' "$store_busy_timeout_ms" "$state_db" >&2 || ad_cutover_write_failed
        echo "  A re-run of this install reads the version again: below v${target_version} it" >&2 || ad_cutover_write_failed
        echo "  brings ${state_db_name} to v${target_version} again, above v${target_version} it stops" >&2 || ad_cutover_write_failed
        echo "  at the store open (ErrSchemaMismatch), and at v${target_version} it finishes" >&2 || ad_cutover_write_failed
        echo "  the install. If a re-run fails this same way, contact the maintainers." >&2 || ad_cutover_write_failed
        ad_exit_5 ErrSchemaVerifyFailed
    fi
    echo "  schema  : migration verified — ${state_db_name} now at v${db_version_after}" || ad_cutover_write_failed
fi

if [[ "$CUTOVER_PHASE" == running && -n "$CUTOVER_INTERRUPTED" ]]; then
    ad_cutover_interrupted_committed
fi

# --------------------------------------------------------------------
# Hook injection — additive merge into ~/.claude/settings.json
#
# Skipped entirely under --no-hooks: settings.json is not written,
# leaving it byte-identical to its pre-install state. With hooks on it is
# rewritten with no backup copy (b.f3n; see the write below).
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
    # Code reads as no settings, is merged as {}. A
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

    # Atomic write: tempfile + mv, keeping an existing file's mode, and
    # writing through a symlinked settings.json to its target. No backup
    # copy is taken (b.f3n): settings.json's env can hold tokens, and each
    # run's timestamped copy of them was kept for good. A file the merge
    # cannot take was refused above, before any write, and a failed write
    # leaves the file as it was.
    ad_replace_keeping_mode "$DEFAULT_SETTINGS_PATH" "$new_settings"

    echo "  hooks   : injected into $DEFAULT_SETTINGS_PATH"
    if [[ "$old_hook_count" =~ ^[0-9]+$ && "$old_hook_count" -gt 0 ]]; then
        echo "  hooks   : removed $old_hook_count hook entries that named the old directory's binary (${OLD_HOOK_PREFIX})"
    fi
fi

# Copies of settings.json earlier releases kept (b.f3n): their install.sh
# with hooks on, and their uninstall.sh, copied it to
# settings.json.bak.<YYYYmmdd-HHMMSS> beside it before each edit, and nothing
# removed them. settings.json's env can hold tokens, so each run that gets
# here names every file of that name, all on one line, each path quoted with
# %q, as the other advice here quotes paths: a plain path prints unchanged,
# and the list splits into its paths as the shell reads it under any HOME (one
# holding a space, say). Only that name matches, so a settings.json.bak.<other>
# the user keeps is not named. None is deleted: even a file of that name may
# be the user's own. uninstall.sh does the same.
old_settings_baks=""
for old_settings_bak in "${DEFAULT_SETTINGS_PATH}".bak.[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]-[0-9][0-9][0-9][0-9][0-9][0-9]; do
    [[ -e "$old_settings_bak" || -L "$old_settings_bak" ]] || continue
    printf -v old_settings_baks '%s %q' "$old_settings_baks" "$old_settings_bak"
done
if [[ -n "$old_settings_baks" ]]; then
    echo "  settings: copies of settings.json that earlier releases kept may hold its secrets (tokens, say) and can be deleted:${old_settings_baks}"
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

# (ad_backup_keeping_mode is defined above, with the agent-rename migration's
# apply step, which copies files first too.)

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
#                                       leaves it unchanged too
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
                # A local add could shadow custom arguments or environment
                # in another scope, or fail on an existing local name.
                plain_add=0
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

# --------------------------------------------------------------------
# The schedule for the sweeps (b.wur)
#
# sweep runs the same shared checks as state reads, across all agents. An
# external cadence can refresh rows even when no caller reads them; failed or
# absent runs leave observations old. expire is separate, operator-selected
# cleanup of finished rows past expire_retention_days. The schedule ships as
# files in schedule/ beside this script: a crontab file and a systemd user
# .service/.timer pair for each command. The installed recovery unit filenames
# stay agent-director-find-missing.* for upgrade and uninstall compatibility.
# Rendering chooses sweep only when the installed binary's help advertises it,
# otherwise find-missing; it never runs recovery to discover support. This also
# handles older release assets and the fallback to main for older binaries.
# Printed cron advice uses that same renderer.
#
# Where the files come from. Beside this script when it is a file on disk (the
# skill's directory, a checkout) and schedule/ there holds all five. Otherwise
# (the script was piped to bash, as the README's `curl ... | bash -s --
# --from-release`, or the skill was copied without schedule/) the step fetches
# the same five files from the repository, never generating them: from
# https://raw.githubusercontent.com/<RELEASE_REPO_SLUG>/<ref>/skills/
# install-agent-director/schedule/<file>, with the same curl (and the same
# INSTALL_SH_TEST_CURL_OVERRIDE) the --from-release download uses. The ref is:
#   --from-release: the resolved release tag (FROM_RELEASE_TAG);
#   otherwise: the commit stamp of the binaries being installed (their
#     `version` result, already required to be a real stamp), when it is a
#     full 40-hex commit id; any other stamp gives no reliable ref, so nothing
#     is fetched.
# The fetched files are kept in ~/.config/agent-director/schedule/ (five files,
# replaced by each fetch), so the warning can always name a real directory an
# operator can copy from; uninstall.sh removes it. Nothing is fetched, and
# nothing is written there, when the files are beside the script, or when the
# schedule is already in place and --schedule was not given. A fetch that
# fails (no curl, no network, a ref the repository does not have, a reply that
# is not a schedule file) writes nothing, never changes an exit status, and
# the warning gives the reason and points at the repository's copy.
#
# The files hold three placeholders this script fills from the install: the
# binary's absolute path (@@AGENT_DIRECTOR@@, the canonical path; written in
# double quotes in a unit's ExecStart when it holds a character a bare word
# cannot), and the environment a cron or systemd job does not get
# (@@ENV_PREFIX@@ in the cron file, @@ENV_LINES@@ in the units): PATH, always
# written, set to <the directory of the tmux found on the install's PATH>:
# /usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin (no tmux directory when tmux is
# not on the install's PATH); TMUX_TMPDIR, only when it is set and not empty in
# the install's environment, so the sweep reaches the tmux server the agents
# use; and HOME, only when the install's $HOME differs from the passwd entry's
# home directory (getent passwd <uid>, field 6; dscl on macOS), because cron
# and systemd set HOME from the passwd entry and the binary keeps its files in
# $HOME/.config/agent-director: a sweep that got another HOME would open
# another store. (HOME is carried only when it differs, so the usual lines stay
# short; with the passwd home unknown it is carried.) The binary reads no
# XDG_CONFIG_HOME or other variable to find its directory, so there is nothing
# else to carry. A line of a file that starts with two # is a note for the
# reader and is not installed. Every installed line carries the marker
# agent-director-schedule, which is how a re-run and uninstall.sh find them.
# After the placeholders are filled, a line that still holds an @@ placeholder
# (a file fetched or copied from another version than this script) makes the
# install refuse, with that reason, and write nothing.
#
# --schedule installs it, for the user running this script (the agents' user):
#   1. cron, when crontab is available, `crontab -l` works and a cron daemon
#      (a process named cron or crond; checked on Linux only) is running: the
#      marked lines of the user's crontab are replaced by the file's lines,
#      every other line kept as it is; a re-run leaves exactly one copy;
#   2. else a systemd user timer and service per sweep, in
#      the user manager's unit directory, enabled, when the manager answers
#      `systemctl --user show-environment`. The directory follows that
#      answer's environment, not this shell's: its XDG_CONFIG_HOME's
#      systemd/user when that is an absolute path, else its HOME's
#      .config/systemd/user (a HOME carried into this run can differ from the
#      manager's); with neither in the answer, this shell's rule, an absolute
#      $XDG_CONFIG_HOME's systemd/user, else $HOME/.config/systemd/user;
#   3. else nothing: the warning below, with the reason. When crontab works but
#      no cron daemon runs (WSL, a container, a minimal VM) and systemd does
#      not work either, the cron lines are still put in the crontab, the install
#      says on stderr that no cron daemon runs, and the host counts as not
#      scheduled: the warning fires and gives that reason.
# Piped to bash (no schedule/ beside the script) the five files are fetched at
# the release tag (--from-release) or the binaries' commit; when that ref has no
# schedule/ (HTTP 404: a release before it shipped), once more at main, which
# the fetch line says; if that fails too the warning's link points at main.
# Whichever of 1 and 2 is used, the other's entries this install made are
# removed, so one copy is scheduled. A run as a user other than HOME's owner
# installs nothing (the sweeps must run as the agents' user).
#
# Whenever no schedule is set up after the run (both sweeps absent from this
# install's cron lines, or the cron lines present with no cron daemon running,
# and no units), the install ends with a warning on stderr naming the lines to
# add. It never changes the exit status. Its re-run advice is this script's
# own command line, every argument as given (shell-quoted) plus --schedule,
# after a `cd` to the directory the install ran in (a relative path and
# --register-mcp depend on it), so running it verbatim repeats the same install
# with the schedule; a script
# piped to bash cannot know its command line and says to run the same install
# command again with --schedule added.
#
# uninstall.sh holds copies of this section's marker (SCHEDULE_MARK), the unit
# names (SCHEDULE_UNIT_FILES, SCHEDULE_TIMERS), the unit directory rule and the
# two test switches it reads (INSTALL_SH_TEST_CRONTAB and
# INSTALL_SH_TEST_SYSTEMCTL, below): change one here, change it there.
#
# Test-only switches (not in --help), as INSTALL_SH_TEST_PROC_CHECK above:
# INSTALL_SH_TEST_CRONTAB and INSTALL_SH_TEST_SYSTEMCTL name the program run in
# place of `crontab` and `systemctl`. A stand-in for crontab takes `-l`, `-`
# (the new crontab on stdin) and, in uninstall.sh, `-r`.
# INSTALL_SH_TEST_CRON_RUNNING=1|0 stands in for the cron daemon check: 1 says a
# daemon runs, 0 says none does (without it the check looks for a process named
# cron or crond, with pgrep or /proc; the Docker harness image runs none).
# INSTALL_SH_TEST_PASSWD_HOME, when set (even empty = unknown), stands in for the
# passwd entry's home directory that $HOME is compared with.
# --------------------------------------------------------------------

readonly SCHEDULE_MARK="agent-director-schedule"
SCHEDULE_DIR=""   # the directory the five files are read from, set by ad_sched_locate
SCHEDULE_LOCAL_DIR="${DEFAULT_INSTALL_ROOT}/schedule"
SCHEDULE_RAW_BASE="https://raw.githubusercontent.com/${RELEASE_REPO_SLUG}"
SCHEDULE_UNIT_FILES=(
    agent-director-find-missing.service agent-director-find-missing.timer
    agent-director-expire.service agent-director-expire.timer
)
SCHEDULE_TIMERS=(agent-director-find-missing.timer agent-director-expire.timer)
SCHEDULE_ALL_FILES=(agent-director.cron "${SCHEDULE_UNIT_FILES[@]}")

SCHED_MGR_READ=0     # 1 once ad_sched_mgr_read has asked the user manager
SCHED_MGR_OK=0       # 1 when the systemd user manager answered
SCHED_MGR_WHY=""     # why it did not
SCHED_MGR_HOME=""    # its HOME (absolute) or empty
SCHED_MGR_XDG=""     # its XDG_CONFIG_HOME (absolute) or empty
SCHED_FETCH_404=0    # 1 when the last ad_sched_download stopped on an HTTP 404
SCHED_FETCH_NOTE=""  # set when the files came from main, not the release tag
SCHED_FETCHED=0      # 1 when the files in SCHEDULE_DIR were fetched
SCHED_REF=""         # the repository ref the files are (to be) fetched at
SCHED_FETCH_WHY=""   # why the files could not be found or fetched
sched_tmp=""         # the fetch's temp directory (removed by ad_exit_cleanup)
SCHED_WHY=""         # why a mechanism or the install could not be used
SCHED_CRON_TEXT=""   # the user's crontab, as read by ad_sched_cron_read
SCHED_CRON_WHY=""    # why ad_sched_cron_read failed
SCHED_CRON_PREFIX="" # @@ENV_PREFIX@@'s value
SCHED_CRON_BIN=""    # @@AGENT_DIRECTOR@@'s value in the cron file
SCHED_UNIT_BIN=""    # @@AGENT_DIRECTOR@@'s value in the units
SCHED_ENV_LINES=""   # @@ENV_LINES@@'s value
SCHED_RESULT=""      # what the install did: installed or unchanged
SCHED_FOUND=""       # cron or systemd, set by ad_sched_present
SCHED_WARN=0
SCHED_REASON=""
SCHED_NODAEMON=0     # 1: this install's cron lines are in the crontab and no cron daemon runs
SCHED_CRON_PUT=0     # 1: ad_sched_install put the cron lines in with no daemon running
SCHED_RENDERED=""    # ad_sched_render's output
SCHED_VERB=""        # recovery verb supported by the installed executable
SUBST=""

ad_sched_crontab_cmd() { printf '%s' "${INSTALL_SH_TEST_CRONTAB:-crontab}"; }
ad_sched_systemctl_cmd() { printf '%s' "${INSTALL_SH_TEST_SYSTEMCTL:-systemctl}"; }

# ad_sched_mgr_read — ask the systemd user manager, once per run, whether it
# answers and what its HOME and XDG_CONFIG_HOME are (`systemctl --user
# show-environment`). Sets SCHED_MGR_OK (1 when it answers), SCHED_MGR_WHY (why
# not), SCHED_MGR_HOME and SCHED_MGR_XDG (each empty unless an absolute path).
# Called in the shell itself, never in a $( ) subshell, so the result is kept.
ad_sched_mgr_read() {
    local sc env line
    [[ "$SCHED_MGR_READ" -eq 0 ]] || return 0
    SCHED_MGR_READ=1
    sc="$(ad_sched_systemctl_cmd)"
    if ! command -v "$sc" >/dev/null 2>&1; then
        SCHED_MGR_WHY="systemctl is not installed"
        return 0
    fi
    if ! env="$("$sc" --user show-environment 2>/dev/null </dev/null)"; then
        SCHED_MGR_WHY="the systemd user manager is not reachable (systemctl --user show-environment failed)"
        return 0
    fi
    SCHED_MGR_OK=1
    while IFS= read -r line; do
        case "$line" in
            HOME=/*) SCHED_MGR_HOME="${line#HOME=}" ;;
            XDG_CONFIG_HOME=/*) SCHED_MGR_XDG="${line#XDG_CONFIG_HOME=}" ;;
        esac
    done <<<"$env"
}

# ad_sched_unit_dir — the directory the systemd user manager reads the user's
# units from: its own XDG_CONFIG_HOME's systemd/user when that is an absolute
# path, else its HOME's .config/systemd/user (the manager's environment can
# differ from this shell's, e.g. a HOME carried into this run). When the manager
# answers with neither, or does not answer, this shell's rule: an absolute
# XDG_CONFIG_HOME's systemd/user, else $HOME/.config/systemd/user. Reads the
# manager's environment on its first call; call it once in the shell (not in a
# $( )) first, as ad_sched_step does, so later calls in subshells see it.
ad_sched_unit_dir() {
    ad_sched_mgr_read
    if [[ "$SCHED_MGR_OK" -eq 1 ]]; then
        if [[ -n "$SCHED_MGR_XDG" ]]; then
            printf '%s' "${SCHED_MGR_XDG}/systemd/user"
            return 0
        elif [[ -n "$SCHED_MGR_HOME" ]]; then
            printf '%s' "${SCHED_MGR_HOME%/}/.config/systemd/user"
            return 0
        fi
    fi
    case "${XDG_CONFIG_HOME:-}" in
        /*) printf '%s' "${XDG_CONFIG_HOME}/systemd/user" ;;
        *) printf '%s' "${HOME}/.config/systemd/user" ;;
    esac
}

# ad_sched_subst <text> <from> <to> — set SUBST to <text> with every <from>
# replaced by <to>, literally (no pattern or & meaning in either, in any bash).
ad_sched_subst() {
    local rest="$1" out="" pre
    while [[ "$rest" == *"$2"* ]]; do
        pre="${rest%%"$2"*}"
        out="${out}${pre}${3}"
        rest="${rest#*"$2"}"
    done
    SUBST="${out}${rest}"
}

# ad_sched_cron_word <text> — set SUBST to <text> as one word of a crontab
# command: left as it is when it holds only characters a shell never reads,
# else in single quotes; a % is written \% (cron reads a bare % as a newline).
ad_sched_cron_word() {
    local w="$1"
    if [[ ! "$w" =~ ^[A-Za-z0-9_./:@+=,-]+$ ]]; then
        ad_sched_subst "$w" "'" "'\\''"
        w="'${SUBST}'"
    fi
    ad_sched_subst "$w" "%" "\\%"
}

# ad_sched_unit_value <text> — set SUBST to <text> as the inside of a systemd
# double-quoted word (an Environment="NAME=value" assignment): \ and "
# backslash-escaped and % written %% (a specifier). A $ is left alone:
# Environment= does no variable expansion (systemd.exec), so $$ would put two
# dollar signs in the value.
ad_sched_unit_value() {
    ad_sched_subst "$1" "\\" "\\\\"
    ad_sched_subst "$SUBST" '"' '\"'
    ad_sched_subst "$SUBST" "%" "%%"
}

# ad_sched_unit_exec <path> — set SUBST to <path> as the program word of an
# ExecStart line: left as it is when it holds only characters a bare word keeps,
# else in double quotes (a space would split the word). \ and " are escaped
# and % written %%, as in a value. A $ is left as it is, and not doubled: only
# the arguments after the program are variable-expanded (systemd.service: "the
# first argument ... may not be a variable"), and a $$ in the program word is
# taken literally, which would name another file.
ad_sched_unit_exec() {
    if [[ "$1" =~ ^[A-Za-z0-9_./:@+=,-]+$ ]]; then
        SUBST="$1"
        return 0
    fi
    ad_sched_unit_value "$1"
    SUBST="\"${SUBST}\""
}

# ad_sched_passwd_home — print the home directory of this user's passwd entry
# (what cron and systemd set HOME to), or nothing when it cannot be told.
ad_sched_passwd_home() {
    local uid line home="" _f
    if [[ -n "${INSTALL_SH_TEST_PASSWD_HOME+x}" ]]; then
        printf '%s' "$INSTALL_SH_TEST_PASSWD_HOME"
        return 0
    fi
    uid="$(id -u)"
    if command -v getent >/dev/null 2>&1; then
        line="$(getent passwd "$uid" 2>/dev/null </dev/null || true)"
        IFS=: read -r _f _f _f _f _f home _f <<<"$line" || true
    fi
    if [[ -z "$home" && "$(uname -s 2>/dev/null)" == Darwin ]] && command -v dscl >/dev/null 2>&1; then
        line="$(dscl . -read "/Users/$(id -un)" NFSHomeDirectory 2>/dev/null </dev/null || true)"
        home="${line#NFSHomeDirectory: }"
        [[ "$line" == "NFSHomeDirectory: "* ]] || home=""
    fi
    if [[ -z "$home" && -r /etc/passwd ]]; then
        while IFS=: read -r _f _f line _f _f home _f; do
            if [[ "$line" == "$uid" ]]; then
                printf '%s' "$home"
                return 0
            fi
        done </etc/passwd
        home=""
    fi
    printf '%s' "$home"
}

# ad_sched_env — fill the placeholders' values from this install. PATH is the
# directory of the tmux on this PATH, then /usr/local/bin:/usr/bin:/bin:
# /usr/sbin:/sbin; TMUX_TMPDIR is added only when set and not empty; HOME only
# when it differs from the passwd entry's home (or that is unknown). Returns 1
# with SCHED_WHY when a value holds a character a schedule file cannot carry.
ad_sched_env() {
    local tmux_path tmux_dir path_val tmpdir home_val="" pw_home cron_path cron_tmp cron_home unit_path unit_tmp unit_home
    tmux_path="$(command -v tmux 2>/dev/null || true)"
    path_val="/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
    if [[ "$tmux_path" == /* ]]; then
        tmux_dir="${tmux_path%/*}"
        path_val="${tmux_dir:-/}:${path_val}"
    fi
    tmpdir="${TMUX_TMPDIR:-}"
    pw_home="$(ad_sched_passwd_home)"
    if [[ -z "$pw_home" || "${HOME%/}" != "${pw_home%/}" ]]; then
        home_val="$HOME"
    fi
    if [[ "$path_val" == *[[:cntrl:]]* || "$tmpdir" == *[[:cntrl:]]* || "$home_val" == *[[:cntrl:]]* || "$CANONICAL" == *[[:cntrl:]]* ]]; then
        SCHED_WHY="a path to put in the schedule (the tmux directory, TMUX_TMPDIR, HOME or the binary) holds a control character"
        return 1
    fi
    if [[ "$CANONICAL" == *[\"\'\\]* ]]; then
        SCHED_WHY="the binary's path holds a quote or a backslash: $CANONICAL"
        return 1
    fi
    ad_sched_cron_word "$path_val"; cron_path="$SUBST"
    SCHED_CRON_PREFIX="PATH=${cron_path}"
    ad_sched_unit_value "$path_val"; unit_path="$SUBST"
    SCHED_ENV_LINES="Environment=\"PATH=${unit_path}\""$'\n'
    if [[ -n "$tmpdir" ]]; then
        ad_sched_cron_word "$tmpdir"; cron_tmp="$SUBST"
        SCHED_CRON_PREFIX="${SCHED_CRON_PREFIX} TMUX_TMPDIR=${cron_tmp}"
        ad_sched_unit_value "$tmpdir"; unit_tmp="$SUBST"
        SCHED_ENV_LINES="${SCHED_ENV_LINES}Environment=\"TMUX_TMPDIR=${unit_tmp}\""$'\n'
    fi
    if [[ -n "$home_val" ]]; then
        ad_sched_cron_word "$home_val"; cron_home="$SUBST"
        SCHED_CRON_PREFIX="${SCHED_CRON_PREFIX} HOME=${cron_home}"
        ad_sched_unit_value "$home_val"; unit_home="$SUBST"
        SCHED_ENV_LINES="${SCHED_ENV_LINES}Environment=\"HOME=${unit_home}\""$'\n'
    fi
    ad_sched_cron_word "$CANONICAL"; SCHED_CRON_BIN="$SUBST"
    ad_sched_unit_exec "$CANONICAL"; SCHED_UNIT_BIN="$SUBST"
}

# Help is store-free. Probe once only when rendering or warning is needed;
# an existing schedule without --schedule needs neither a probe nor a fetch.
# Older binaries and unusable help retain the supported legacy command.
ad_sched_verb() {
    local help support
    [[ -z "$SCHED_VERB" ]] || return 0
    SCHED_VERB=find-missing
    if help="$("$CANONICAL" help 2>/dev/null </dev/null)" &&
        support="$(jq -e 'any(.verbs[]?; .name == "sweep")' <<<"$help" 2>/dev/null)" &&
        [[ "$support" == true ]]; then
        SCHED_VERB=sweep
    fi
}

# ad_sched_render <file> <cron|unit> — set SCHED_RENDERED to the shipped file as
# it is installed: its notes (lines starting with two #) dropped, the
# placeholders filled. Returns 1, with SCHED_WHY, when a line still holds an @@
# placeholder once the three known ones are filled (a file from another version
# than this script); SCHED_RENDERED is then empty.
ad_sched_render() {
    local line bin left known out=""
    ad_sched_verb
    SCHED_RENDERED=""
    while IFS= read -r line || [[ -n "$line" ]]; do
        case "$line" in
            "##"*) continue ;;
        esac
        if [[ "$2" == cron ]]; then
            ad_sched_subst "$line" "@@ENV_PREFIX@@" ""
            known="@@AGENT_DIRECTOR@@ and @@ENV_PREFIX@@"
        else
            if [[ "$line" == "@@ENV_LINES@@" ]]; then
                out="${out}${SCHED_ENV_LINES}"
                continue
            fi
            SUBST="$line"
            known="@@AGENT_DIRECTOR@@ and a line of just @@ENV_LINES@@"
        fi
        ad_sched_subst "$SUBST" "@@AGENT_DIRECTOR@@" ""
        left="$SUBST"
        if [[ "$left" == *"@@"* ]]; then
            SCHED_WHY="${1##*/} still holds an unfilled @@ placeholder once ${known} are filled, so it comes from another version than this install.sh: ${line}"
            return 1
        fi
        # Change only the template command, before inserting any user path or
        # environment. Keep the three-placeholder format and stable unit names.
        ad_sched_subst "$line" "@@AGENT_DIRECTOR@@ find-missing" "@@AGENT_DIRECTOR@@ ${SCHED_VERB}"
        ad_sched_subst "$SUBST" "@@AGENT_DIRECTOR@@ sweep" "@@AGENT_DIRECTOR@@ ${SCHED_VERB}"
        line="$SUBST"
        if [[ "$2" == cron ]]; then
            bin="$SCHED_CRON_BIN"
            ad_sched_subst "$line" "@@ENV_PREFIX@@" "$SCHED_CRON_PREFIX"
            line="$SUBST"
        else
            bin="$SCHED_UNIT_BIN"
        fi
        ad_sched_subst "$line" "@@AGENT_DIRECTOR@@" "$bin"
        out="${out}${SUBST}"$'\n'
    done <"$1"
    SCHED_RENDERED="$out"
}

# ad_sched_files_ok — 0 when SCHEDULE_DIR is set and holds every schedule file.
ad_sched_files_ok() {
    local f
    [[ -n "$SCHEDULE_DIR" ]] || return 1
    for f in "${SCHEDULE_ALL_FILES[@]}"; do
        [[ -r "${SCHEDULE_DIR}/${f}" ]] || return 1
    done
    return 0
}

# ad_sched_ref — set SCHED_REF to the repository ref that matches what is being
# installed: the release tag with --from-release, else the binaries' full commit
# stamp. Returns 1 with SCHED_FETCH_WHY when there is none to rely on.
ad_sched_ref() {
    SCHED_REF=""
    if [[ "$FROM_RELEASE" -eq 1 ]]; then
        if [[ -n "$FROM_RELEASE_TAG" ]]; then
            SCHED_REF="$FROM_RELEASE_TAG"
            return 0
        fi
        SCHED_FETCH_WHY="no release tag was resolved"
        return 1
    fi
    if [[ "${stamp_commit:-}" =~ ^[0-9a-f]{40}$ ]]; then
        SCHED_REF="$stamp_commit"
        return 0
    fi
    SCHED_FETCH_WHY="the installed binaries' commit stamp (${stamp_commit:-none}) is not a full commit id, so there is no reliable repository ref to fetch the files at"
    return 1
}

# ad_sched_download <dir> <ref> — GET the five schedule files at <ref> into
# <dir>; each must be a schedule file (it carries the marker). Returns 1 with
# SCHED_FETCH_WHY on the first failure, and SCHED_FETCH_404=1 when that was an
# HTTP 404.
ad_sched_download() {
    local curl_cmd="${INSTALL_SH_TEST_CURL_OVERRIDE:-curl}" f url code
    SCHED_FETCH_404=0
    for f in "${SCHEDULE_ALL_FILES[@]}"; do
        url="${SCHEDULE_RAW_BASE}/${2}/skills/install-agent-director/schedule/${f}"
        code="$("$curl_cmd" -sSL --retry 0 --connect-timeout 10 --max-time 30 \
            -w '%{http_code}' -o "${1}/${f}" "$url" 2>/dev/null </dev/null || true)"
        if [[ "$code" != "200" ]]; then
            SCHED_FETCH_WHY="GET ${url} gave ${code:+HTTP }${code:-no HTTP status}"
            [[ "$code" != "404" ]] || SCHED_FETCH_404=1
            return 1
        fi
        if [[ ! -s "${1}/${f}" ]] || ! grep -q -F "$SCHEDULE_MARK" "${1}/${f}"; then
            SCHED_FETCH_WHY="GET ${url} did not return a schedule file"
            return 1
        fi
    done
    return 0
}

# ad_sched_fetch — fetch the five schedule files at ad_sched_ref's ref into
# SCHEDULE_LOCAL_DIR and set SCHEDULE_DIR to it (SCHED_FETCHED=1). When that
# ref answers 404 (a release tag older than the schedule/ directory, as with
# --from-release on an older tag), it tries once more at main (SCHED_REF is then
# main and SCHED_FETCH_NOTE says so); a file from another version is caught
# later, by the placeholder check of ad_sched_render. All five come down into a
# temp directory first and each must be a schedule file (it carries the marker);
# only then are they moved into place, so a failure writes nothing. Returns 1
# with SCHED_FETCH_WHY on any failure; never exits.
ad_sched_fetch() {
    local curl_cmd="${INSTALL_SH_TEST_CURL_OVERRIDE:-curl}" f tmpd first_ref first_why
    SCHED_FETCH_WHY=""
    SCHED_FETCH_NOTE=""
    ad_sched_ref || return 1
    if ! command -v "$curl_cmd" >/dev/null 2>&1; then
        SCHED_FETCH_WHY="${curl_cmd} is not installed"
        return 1
    fi
    if ! tmpd="$(mktemp -d "${DEFAULT_INSTALL_ROOT}/.schedule.XXXXXX" 2>/dev/null)"; then
        SCHED_FETCH_WHY="cannot make a temp directory in ${DEFAULT_INSTALL_ROOT}"
        return 1
    fi
    sched_tmp="$tmpd"
    if ! ad_sched_download "$tmpd" "$SCHED_REF" && [[ "$SCHED_FETCH_404" -eq 1 && "$SCHED_REF" != main ]]; then
        first_ref="$SCHED_REF"
        first_why="$SCHED_FETCH_WHY"
        for f in "${SCHEDULE_ALL_FILES[@]}"; do
            rm -f "${tmpd}/${f}"
        done
        SCHED_REF=main
        SCHED_FETCH_WHY=""
        if ad_sched_download "$tmpd" main; then
            SCHED_FETCH_NOTE="${first_ref} has no schedule files (${first_why}), so they come from main, which may be newer than this install"
        else
            SCHED_FETCH_WHY="${first_why}; and at main: ${SCHED_FETCH_WHY}"
        fi
    fi
    if [[ -z "$SCHED_FETCH_WHY" ]]; then
        if ! mkdir -p "$SCHEDULE_LOCAL_DIR" 2>/dev/null; then
            SCHED_FETCH_WHY="cannot create ${SCHEDULE_LOCAL_DIR}"
        else
            for f in "${SCHEDULE_ALL_FILES[@]}"; do
                if ! mv -f "${tmpd}/${f}" "${SCHEDULE_LOCAL_DIR}/${f}" 2>/dev/null; then
                    SCHED_FETCH_WHY="cannot write ${SCHEDULE_LOCAL_DIR}/${f}"
                    break
                fi
            done
            if [[ -n "$SCHED_FETCH_WHY" ]]; then
                for f in "${SCHEDULE_ALL_FILES[@]}"; do
                    rm -f "${SCHEDULE_LOCAL_DIR}/${f}"
                done
            fi
        fi
    fi
    rm -rf -- "$tmpd"
    sched_tmp=""
    [[ -z "$SCHED_FETCH_WHY" ]] || return 1
    SCHEDULE_DIR="$SCHEDULE_LOCAL_DIR"
    SCHED_FETCHED=1
    return 0
}

# ad_sched_locate — find the schedule files: beside this script when it is a
# file with all five in schedule/, else fetched (ad_sched_fetch). Returns 1,
# with SCHEDULE_DIR empty and SCHED_FETCH_WHY set, when there are none.
ad_sched_locate() {
    SCHEDULE_DIR=""
    SCHED_FETCHED=0
    SCHED_FETCH_WHY=""
    if [[ "$SCRIPT_REAL" -eq 1 ]]; then
        SCHEDULE_DIR="${SCRIPT_DIR}/schedule"
        if ad_sched_files_ok; then
            return 0
        fi
        SCHEDULE_DIR=""
    fi
    ad_sched_fetch
}

# ad_sched_cron_read — read the user's crontab into SCHED_CRON_TEXT. Returns 1
# with SCHED_CRON_WHY when there is no crontab program or `crontab -l` fails
# for a reason other than the user having no crontab yet ("no crontab for
# <user>", or busybox's "can't open '<user>': No such file or directory").
ad_sched_cron_read() {
    local cmd rc=0 err
    cmd="$(ad_sched_crontab_cmd)"
    SCHED_CRON_TEXT=""
    if ! command -v "$cmd" >/dev/null 2>&1; then
        SCHED_CRON_WHY="crontab is not installed"
        return 1
    fi
    SCHED_CRON_TEXT="$("$cmd" -l 2>/dev/null </dev/null)" || rc=$?
    if [[ "$rc" -ne 0 ]]; then
        SCHED_CRON_TEXT=""
        err="$("$cmd" -l 2>&1 >/dev/null </dev/null || true)"
        case "$err" in
            *"no crontab"*|*"No such file or directory"*) return 0 ;;
        esac
        SCHED_CRON_WHY="crontab -l failed (exit $rc): ${err:-no output}"
        return 1
    fi
    return 0
}

# ad_sched_cron_has <verb> — 0 for this install's marked line. The recovery
# entry accepts either released spelling; expire remains a separate entry.
ad_sched_cron_has() {
    local l
    while IFS= read -r l; do
        case "$l" in
            "#"*) continue ;;
        esac
        [[ "$l" == *"$SCHEDULE_MARK"* ]] || continue
        case "$1" in
            sweep|find-missing)
                if [[ "$l" == *" sweep "* || "$l" == *" find-missing "* ]]; then return 0; fi ;;
            *) if [[ "$l" == *" $1 "* ]]; then return 0; fi ;;
        esac
    done <<<"$SCHED_CRON_TEXT"
    return 1
}

# ad_sched_units_present — 0 when all four of this install's unit files exist.
ad_sched_units_present() {
    local dir f
    dir="$(ad_sched_unit_dir)"
    for f in "${SCHEDULE_UNIT_FILES[@]}"; do
        [[ -f "${dir}/${f}" ]] || return 1
    done
    return 0
}

# ad_sched_cron_daemon_running — 0 when a cron daemon runs on this machine, or
# when that cannot be told (not Linux, no pgrep and no /proc): a process named
# cron (Debian, Ubuntu) or crond (Red Hat, Alpine, busybox). A crontab that
# `crontab -l` reads is no proof: WSL, a container and a minimal VM have the
# program and no daemon, and the lines then never run. Stubbed by
# INSTALL_SH_TEST_CRON_RUNNING=1|0.
ad_sched_cron_daemon_running() {
    local d name rc found=0 unknown=0
    case "${INSTALL_SH_TEST_CRON_RUNNING:-}" in
        1) return 0 ;;
        0) return 1 ;;
    esac
    [[ "$(uname -s 2>/dev/null)" == Linux ]] || return 0
    if command -v pgrep >/dev/null 2>&1; then
        for name in cron crond; do
            rc=0
            pgrep -x "$name" >/dev/null 2>&1 </dev/null || rc=$?
            if [[ "$rc" -eq 0 ]]; then
                return 0
            elif [[ "$rc" -ne 1 ]]; then
                unknown=1
            fi
        done
        [[ "$unknown" -eq 1 ]] || return 1
    fi
    [[ -d /proc/self ]] || return 0
    for d in /proc/[0-9]*; do
        name=""
        { read -r name <"${d}/comm"; } 2>/dev/null || true
        if [[ "$name" == cron || "$name" == crond ]]; then
            found=1
            break
        fi
    done
    [[ "$found" -eq 1 ]]
}

# ad_sched_present — 0 when both sweeps have an entry of this install's that
# can run: its lines in the user's crontab with a cron daemon running, or its
# unit files. Sets SCHED_FOUND. The lines in the crontab with no cron daemon
# running do not count (SCHED_NODAEMON=1): they never fire.
ad_sched_present() {
    SCHED_FOUND=""
    SCHED_NODAEMON=0
    if ad_sched_cron_read && ad_sched_cron_has find-missing && ad_sched_cron_has expire; then
        if ad_sched_cron_daemon_running; then
            SCHED_FOUND=cron
            return 0
        fi
        SCHED_NODAEMON=1
    fi
    if ad_sched_units_present; then
        SCHED_FOUND=systemd
        SCHED_NODAEMON=0
        return 0
    fi
    return 1
}

# ad_sched_systemd_usable — 0 when systemctl exists and the user manager
# answers; else 1 with SCHED_WHY.
ad_sched_systemd_usable() {
    ad_sched_mgr_read
    if [[ "$SCHED_MGR_OK" -ne 1 ]]; then
        SCHED_WHY="$SCHED_MGR_WHY"
        return 1
    fi
    return 0
}

# ad_sched_remove_units — remove this install's systemd units (the timers
# disabled first when the manager answers). Returns 1 when none was present.
# Only a file that carries the marker is removed.
ad_sched_remove_units() {
    local dir sc f t found=0
    dir="$(ad_sched_unit_dir)"
    for f in "${SCHEDULE_UNIT_FILES[@]}"; do
        if [[ -f "${dir}/${f}" ]] && grep -q -F "$SCHEDULE_MARK" "${dir}/${f}"; then
            found=1
        fi
    done
    [[ "$found" -eq 1 ]] || return 1
    sc="$(ad_sched_systemctl_cmd)"
    if ad_sched_systemd_usable; then
        "$sc" --user disable --now "${SCHEDULE_TIMERS[@]}" >/dev/null 2>&1 </dev/null || true
    fi
    for f in "${SCHEDULE_UNIT_FILES[@]}"; do
        if [[ -f "${dir}/${f}" ]] && grep -q -F "$SCHEDULE_MARK" "${dir}/${f}"; then
            rm -f "${dir}/${f}"
        fi
    done
    for t in "${SCHEDULE_TIMERS[@]}"; do
        if [[ -L "${dir}/timers.target.wants/${t}" ]]; then
            rm -f "${dir}/timers.target.wants/${t}"
        fi
    done
    if ad_sched_systemd_usable; then
        "$sc" --user daemon-reload >/dev/null 2>&1 </dev/null || true
    fi
    return 0
}

# ad_sched_cron_remove_lines — remove this install's marked lines from the
# user's crontab (every other line kept; the crontab removed when nothing else
# is in it). Quiet, and never fails: used when systemd took over from cron.
ad_sched_cron_remove_lines() {
    local cmd l kept="" had=0
    ad_sched_cron_read || return 0
    [[ -n "$SCHED_CRON_TEXT" ]] || return 0
    cmd="$(ad_sched_crontab_cmd)"
    while IFS= read -r l; do
        if [[ "$l" == *"$SCHEDULE_MARK"* ]]; then
            had=1
        else
            kept="${kept}${l}"$'\n'
        fi
    done <<<"$SCHED_CRON_TEXT"
    [[ "$had" -eq 1 ]] || return 0
    if [[ -z "${kept//[[:space:]]/}" ]]; then
        "$cmd" -r >/dev/null 2>&1 </dev/null || true
    else
        printf '%s' "$kept" | "$cmd" - >/dev/null 2>&1 || true
    fi
    return 0
}

# ad_sched_install_cron [keep-units] — put the rendered cron file's lines in the
# user's crontab in place of the marked ones, then remove this install's systemd
# units (not with an argument). Sets SCHED_RESULT; returns 1 with SCHED_WHY when
# cron cannot be used.
ad_sched_install_cron() {
    local cmd l kept="" new err rc=0
    cmd="$(ad_sched_crontab_cmd)"
    if ! ad_sched_cron_read; then
        SCHED_WHY="$SCHED_CRON_WHY"
        return 1
    fi
    if [[ -n "$SCHED_CRON_TEXT" ]]; then
        while IFS= read -r l; do
            if [[ "$l" != *"$SCHEDULE_MARK"* ]]; then
                kept="${kept}${l}"$'\n'
            fi
        done <<<"$SCHED_CRON_TEXT"
    fi
    if ! ad_sched_render "${SCHEDULE_DIR}/agent-director.cron" cron; then
        return 1
    fi
    new="${kept}${SCHED_RENDERED}"
    if [[ "$new" == "${SCHED_CRON_TEXT}"$'\n' ]]; then
        SCHED_RESULT=unchanged
    else
        err="$(printf '%s' "$new" | "$cmd" - 2>&1 >/dev/null)" || rc=$?
        if [[ "$rc" -ne 0 ]]; then
            SCHED_WHY="crontab - refused the new crontab (exit $rc): ${err:-no output}"
            return 1
        fi
        SCHED_RESULT=installed
    fi
    if ! ad_sched_cron_read || ! ad_sched_cron_has find-missing || ! ad_sched_cron_has expire; then
        SCHED_WHY="crontab accepted the lines but crontab -l does not show them"
        return 1
    fi
    if [[ $# -eq 0 ]]; then
        ad_sched_remove_units >/dev/null 2>&1 || true
    fi
    return 0
}

# ad_sched_install_systemd — write the rendered units, enable the two timers.
# Sets SCHED_RESULT; returns 1 with SCHED_WHY when systemd cannot be used. A
# failed daemon-reload, enable or restart puts back what was there before: a
# unit file this run replaced is restored from its backup, one it created is
# removed (with its timer's enable link); units an earlier run installed and
# this run did not touch stay.
ad_sched_install_systemd() {
    local dir sc f t tmp bak had_prev=" " before=0 mv_failed=0
    if ! ad_sched_systemd_usable; then
        return 1
    fi
    sc="$(ad_sched_systemctl_cmd)"
    dir="$(ad_sched_unit_dir)"
    if ! mkdir -p "$dir" 2>/dev/null; then
        SCHED_WHY="cannot create $dir"
        return 1
    fi
    # Render every unit first, then move them into place, so a failure while
    # rendering or writing changes none.
    for f in "${SCHEDULE_UNIT_FILES[@]}"; do
        tmp="${dir}/.${f}.tmp.$$"
        if ! ad_sched_render "${SCHEDULE_DIR}/${f}" unit; then
            rm -f "${dir}"/.agent-director-*.tmp."$$"
            return 1
        fi
        if ! printf '%s' "$SCHED_RENDERED" >"$tmp" 2>/dev/null; then
            rm -f "${dir}"/.agent-director-*.tmp."$$"
            SCHED_WHY="cannot write $tmp"
            return 1
        fi
    done
    # Back up the unit files that are there now, for the rollback.
    for f in "${SCHEDULE_UNIT_FILES[@]}"; do
        if [[ -f "${dir}/${f}" ]]; then
            bak="${dir}/.${f}.prev.$$"
            if ! cp -p "${dir}/${f}" "$bak" 2>/dev/null; then
                rm -f "${dir}"/.agent-director-*.tmp."$$" "${dir}"/.agent-director-*.prev."$$"
                SCHED_WHY="cannot back up ${dir}/${f}"
                return 1
            fi
            had_prev="${had_prev}${f} "
        fi
    done
    for f in "${SCHEDULE_UNIT_FILES[@]}"; do
        tmp="${dir}/.${f}.tmp.$$"
        if [[ -f "${dir}/${f}" ]] && cmp -s "$tmp" "${dir}/${f}"; then
            before=$((before + 1))
        fi
        if ! mv -f "$tmp" "${dir}/${f}" 2>/dev/null; then
            mv_failed=1
            break
        fi
    done
    if [[ "$before" -eq "${#SCHEDULE_UNIT_FILES[@]}" ]]; then
        SCHED_RESULT=unchanged
    else
        SCHED_RESULT=installed
    fi
    if [[ "$mv_failed" -eq 1 ]] \
        || ! "$sc" --user daemon-reload >/dev/null 2>&1 </dev/null \
        || ! "$sc" --user enable "${SCHEDULE_TIMERS[@]}" >/dev/null 2>&1 </dev/null \
        || ! "$sc" --user restart "${SCHEDULE_TIMERS[@]}" >/dev/null 2>&1 </dev/null; then
        if [[ "$mv_failed" -eq 1 ]]; then
            SCHED_WHY="cannot write the unit files in $dir"
        else
            SCHED_WHY="systemctl --user could not enable the timers"
        fi
        rm -f "${dir}"/.agent-director-*.tmp."$$"
        for f in "${SCHEDULE_UNIT_FILES[@]}"; do
            if [[ "$had_prev" == *" ${f} "* ]]; then
                mv -f "${dir}/.${f}.prev.$$" "${dir}/${f}" 2>/dev/null || true
            else
                rm -f "${dir}/${f}"
            fi
        done
        for t in "${SCHEDULE_TIMERS[@]}"; do
            if [[ "$had_prev" != *" ${t} "* ]]; then
                rm -f "${dir}/timers.target.wants/${t}"
            fi
        done
        "$sc" --user daemon-reload >/dev/null 2>&1 </dev/null || true
        return 1
    fi
    rm -f "${dir}"/.agent-director-*.prev."$$"
    return 0
}

# ad_sched_install — the --schedule step: cron when crontab works and a cron
# daemon runs, else systemd, else nothing. With crontab working and no cron
# daemon, and systemd unusable too, the cron lines are still put in (SCHED_CRON_PUT=1)
# and the run fails: the host is not scheduled, and SCHED_WHY says why.
# Sets SCHED_HOW (cron or systemd) and SCHED_RESULT; returns 1 with SCHED_WHY.
ad_sched_install() {
    local owner why_cron why_systemd cron_usable=0
    SCHED_HOW=""
    SCHED_CRON_PUT=0
    if ! ad_sched_files_ok; then
        SCHED_WHY="the schedule files are not beside this script and could not be fetched: ${SCHED_FETCH_WHY:-not found}"
        return 1
    fi
    owner="$(stat -L -c '%u' "$HOME" 2>/dev/null || stat -L -f '%u' "$HOME" 2>/dev/null || true)"
    if [[ -n "$owner" && "$owner" != "$(id -u)" ]]; then
        SCHED_WHY="this run is user $(id -un) but $HOME belongs to another user (uid $owner), and the sweeps must run as the agents' user: run install.sh --schedule as that user"
        return 1
    fi
    if ! ad_sched_env; then
        return 1
    fi
    if ad_sched_cron_read; then
        cron_usable=1
        if ad_sched_cron_daemon_running; then
            if ad_sched_install_cron; then
                SCHED_HOW=cron
                return 0
            fi
            why_cron="$SCHED_WHY"
        else
            why_cron="no cron daemon (a process named cron or crond) is running on this machine, so crontab lines would never run"
        fi
    else
        why_cron="$SCHED_CRON_WHY"
    fi
    if ad_sched_install_systemd; then
        SCHED_HOW=systemd
        if [[ "$cron_usable" -eq 1 ]]; then
            ad_sched_cron_remove_lines
        fi
        return 0
    fi
    why_systemd="$SCHED_WHY"
    if [[ "$cron_usable" -eq 1 ]] && ! ad_sched_cron_daemon_running && ! ad_sched_units_present; then
        # The lines go in anyway: they run as soon as a cron daemon is started.
        # (Not with units already in place, an earlier run's or ones just put
        # back by the rollback: they and the lines would both run.)
        if ad_sched_install_cron keep-units; then
            SCHED_CRON_PUT=1
            SCHED_WHY="no cron daemon (a process named cron or crond) is running on this machine, so the crontab lines just added do not run until one is started; systemd: ${why_systemd}"
            return 1
        fi
        why_cron="$SCHED_WHY"
    fi
    SCHED_WHY="cron: ${why_cron}; systemd: ${why_systemd}"
    return 1
}

# ad_sched_rerun_cmd — set SUBST to this script's own command line, every
# argument as given and shell-quoted, with --schedule added when it was not
# among them, so that running it verbatim repeats the same install (same
# --binary, --no-hooks, --symlink-dir and the rest) plus the schedule. It starts
# with `cd <the directory the install ran in> &&`: a relative path argument and
# --register-mcp (claude mcp add at local scope belongs to the working
# directory) both depend on it.
ad_sched_rerun_cmd() {
    ad_rerun_cmd schedule
}

# ad_sched_warn <reason> — the loud warning, on stderr: no schedule is set up.
# <reason> (may be empty) is why --schedule did not install one. The cron lines
# are the shipped file's, rendered, and the directory named is where they were
# read from: beside this script, or ~/.config/agent-director/schedule/ when
# the step fetched them (a script read from a pipe). With no files at all
# (the fetch failed) the warning gives the reason and points at them in the
# repository.
ad_sched_warn() {
    local l ref
    ad_sched_verb
    {
        echo
        echo "install.sh: WARNING: nothing schedules agent-director's ${SCHED_VERB} and expire jobs on this machine."
        echo "  Recovery runs only when called; state reads also perform the shared checks on supporting versions."
        echo "  Absent or failed checks leave observations old. expire separately removes eligible finished rows."
        echo "  Choose an external cadence for recovery and retention cleanup; neither runs an internal daemon."
        if [[ "$SCHED_CRON_PUT" -eq 1 ]]; then
            echo "  --schedule was asked for and could not schedule the sweeps: $1"
        elif [[ -n "$1" ]]; then
            echo "  --schedule was asked for and installed nothing: $1"
        elif [[ "$SCHED_NODAEMON" -eq 1 ]]; then
            echo "  This user's crontab holds this install's lines for both sweeps, but no cron daemon (a process named"
            echo "  cron or crond) is running on this machine, so they never run: start the cron service, or use systemd"
            echo "  user timers (the files below)."
        fi
        echo "  To schedule them, run as the agents' user (the user the agents run as, in their tmux environment). Run as"
        echo "  another user, as root or against another tmux server, a sweep can wrongly mark live rows missing or"
        echo "  delete rows whose agent still runs."
        if ad_sched_files_ok && ad_sched_env && ad_sched_render "${SCHEDULE_DIR}/agent-director.cron" cron; then
            echo "  These crontab lines (crontab -e, as that user) run this install's binary:"
            while IFS= read -r l; do
                case "$l" in
                    "#"*|"") ;;
                    *) printf '    %s\n' "$l" ;;
                esac
            done <<<"$SCHED_RENDERED"
            echo "  They ship, with a systemd user timer and service for each sweep, as files in:"
            echo "    ${SCHEDULE_DIR}/"
            echo "    (agent-director.cron, agent-director-find-missing.service and .timer, agent-director-expire.service and .timer)"
            if [[ "$SCHED_FETCHED" -eq 1 ]]; then
                echo "  This script was not run from a file, so it fetched them from the repository at ${SCHED_REF}"
                echo "  (skills/install-agent-director/schedule/ at ${SCHEDULE_RAW_BASE}/${SCHED_REF}/) and kept the copy there.${SCHED_FETCH_NOTE:+ (${SCHED_FETCH_NOTE})}"
            fi
            echo "  Copy the rendered cron lines above. For a unit, fill its placeholders and use ${SCHED_VERB} as the"
            echo "  recovery command in ExecStart (keep the agent-director-find-missing filenames). Or let this install"
            echo "  script install them, as the user the agents run as:"
            if [[ "$SCRIPT_REAL" -eq 1 ]]; then
                ad_sched_rerun_cmd
                echo "    ${SUBST}"
            else
                echo "    run the same install command again with --schedule added"
            fi
            echo "  (--schedule uses cron when crontab works here and a cron daemon runs, else systemd user timers, else it installs nothing.)"
        else
            ref="${SCHED_REF:-main}"
            if [[ -n "$SCHEDULE_DIR" ]]; then
                echo "  Could not build the crontab lines from the schedule files in ${SCHEDULE_DIR}/: ${SCHED_WHY:-a value to put in them cannot be written to a schedule file}."
            elif [[ -n "$SCHED_FETCH_WHY" ]]; then
                echo "  This script has no schedule files beside it and could not fetch them: ${SCHED_FETCH_WHY}."
            fi
            echo "  The schedule files (a crontab file and a systemd user timer and service for each sweep) are in the"
            echo "  agent-director repository, in skills/install-agent-director/schedule/"
            echo "  (https://github.com/${RELEASE_REPO_SLUG}/tree/${ref}/skills/install-agent-director/schedule),"
            echo "  and in the install skill's directory. Copy them by hand as the agents' user (fill the @@...@@"
            echo "  placeholders: the notes in each file say how), using ${SCHED_VERB} as the recovery command, or run"
            echo "  an install.sh that has them beside it with"
            echo "  --schedule, as that user."
        fi
        echo "  Already scheduled another way (launchd, your own cron line or unit)? This check sees only the entries"
        echo "  --schedule adds, so it warns anyway: ignore this, or run --schedule and drop your own entries."
    } >&2
}

# ad_sched_step — the schedule step of the install: --schedule installs it;
# afterwards, with none of this install's entries present, SCHED_WARN is set
# (SCHED_REASON: why --schedule failed, if it ran).
ad_sched_step() {
    local dir
    ad_sched_mgr_read
    dir="$(ad_sched_unit_dir)"
    # The schedule files are needed to install (--schedule) and to warn; with
    # the schedule already in place and no --schedule, neither happens, and
    # nothing is looked for or fetched.
    if [[ "$SCHEDULE" -eq 1 ]] || ! ad_sched_present; then
        if ad_sched_locate; then
            if [[ "$SCHED_FETCHED" -eq 1 ]]; then
                echo "  schedule: the schedule files are not beside this script; fetched them from ${SCHEDULE_RAW_BASE}/${SCHED_REF}/skills/install-agent-director/schedule/ to ${SCHEDULE_DIR}${SCHED_FETCH_NOTE:+ (${SCHED_FETCH_NOTE})}"
            fi
        fi
    fi
    if [[ "$SCHEDULE" -eq 1 ]]; then
        if ad_sched_install; then
            if [[ "$SCHED_HOW" == cron ]]; then
                if [[ "$SCHED_RESULT" == unchanged ]]; then
                    echo "  schedule: cron: this user's crontab already holds these entries (${SCHED_VERB} every 2 minutes, expire daily at 03:17); nothing changed. The cron daemon must be running."
                else
                    echo "  schedule: cron: ${SCHED_VERB} every 2 minutes and expire daily at 03:17 added to this user's crontab, each line tagged # $SCHEDULE_MARK. The cron daemon must be running."
                fi
            else
                if [[ "$SCHED_RESULT" == unchanged ]]; then
                    echo "  schedule: systemd: the user timers already held these units; enabled again, nothing changed (${dir})"
                else
                    echo "  schedule: systemd: user timers ${SCHEDULE_TIMERS[0]} (every 2 minutes) and ${SCHEDULE_TIMERS[1]} (daily at 03:17) installed in ${dir} and enabled."
                fi
                echo "  schedule: a user timer runs only while this user's systemd manager does: on a host where the user is not always logged in, run loginctl enable-linger $(id -un) once."
            fi
            return 0
        fi
        SCHED_REASON="$SCHED_WHY"
        if [[ "$SCHED_CRON_PUT" -eq 1 ]]; then
            echo "  schedule: cron: ${SCHED_VERB} every 2 minutes and expire daily at 03:17 added to this user's crontab, each line tagged # $SCHEDULE_MARK, but NO CRON DAEMON (a process named cron or crond) is running on this machine, so they do not run; start the cron service" >&2
        fi
    fi
    if ad_sched_present; then
        echo "  schedule: found this install's ${SCHED_FOUND} entries for recovery and expire; left as they are"
        if [[ -n "$SCHED_REASON" ]]; then
            echo "  schedule: --schedule could not re-point them at this install: $SCHED_REASON" >&2
        fi
        return 0
    fi
    SCHED_WARN=1
}

SCHED_HOW=""
ad_sched_step

echo "  admin   : $ADMIN_CANONICAL (operator tool, not on PATH; run it only with a human's explicit approval for that run)"
if [[ "$MCP_FAILED" -eq 1 ]]; then
    if [[ "$SCHED_WARN" -eq 1 ]]; then
        ad_sched_warn "$SCHED_REASON"
    fi
    echo "install.sh: installed, but the MCP registration failed (see above); exit 6" >&2
    exit 6
fi
echo "install.sh: done. Try: $CANONICAL help"
if [[ "$SCHED_WARN" -eq 1 ]]; then
    ad_sched_warn "$SCHED_REASON"
fi
} ; exit
