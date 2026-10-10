#!/usr/bin/env bash
# install.sh — install or upgrade agent-director on this machine.
#
# Per SRD §16.2. The script is deliberately bash + jq + standard
# coreutils — no Go, no exotic deps. The Apiary skill harness invokes
# it; an operator can also run it directly from a checked-out tree.
#
# It installs two binaries from the same build: agent-director at
# ~/.agent-director/bin/agent-director, and the operator tool
# agent-director-admin at ~/.agent-director/admin/agent-director-admin
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
#                         ~/.agent-director/admin/agent-director-admin.
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
#                        ~/.agent-director/admin/agent-director-admin.
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
#   --register-mcp       Run `claude mcp add` for the stdio server.
#   --no-hooks           Skip the ~/.claude/settings.json hook
#                        injection step entirely. settings.json is
#                        left byte-identical (no .bak backup, no
#                        edit). Default OFF — defaulting to skip
#                        would defeat install.sh's main value over a
#                        bare binary copy.
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
#                        .prior files from the earlier run are kept.
#
# Exit codes:
#   0  success
#   2  pre-flight failure (claude/tmux missing, whitespace in path, a
#      bad flag, or only one of --sha256 and --admin-sha256)
#   3  binary source not found / not executable (including a
#      --from-release release before 0.11.0, which has no
#      agent-director-admin binary), or the two binaries' version
#      stamps differ or carry no commit stamp
#   4  hook merge failure (~/.claude/settings.json malformed, or holding
#      more than one JSON document; an empty one is merged as {}), or
#      (hooks on) a symlinked settings.json install.sh cannot resolve or
#      write through: its links loop, or the file they resolve to sits in
#      a directory that is missing or cannot be written in (refused in
#      pre-flight, before anything on disk changes)
#   5  store open / schema-migration failure (open failed, the config
#      file refused with ErrConfigMalformed, state.db not created, an
#      existing state.db's user_version unreadable before the open (or
#      after the probe open, when a migration sentinel written before
#      this install may have let it migrate state.db), no temp file for
#      the migration sentinel (mktemp failed), or post-open
#      user_version unreadable or != target when a migration was
#      expected), or a config file whose [store] db_path or
#      busy_timeout_ms install.sh cannot read, or (hooks on) one that
#      sets defaults as a key before any header (defaults = { ... }),
#      which the config.toml merge cannot extend, or (hooks on) a
#      symlinked config.toml install.sh cannot resolve or write through,
#      as for settings.json under exit 4 (all refused in pre-flight,
#      before anything on disk changes). state.db here is
#      the store database agent-director opens: ~/.agent-director/state.db,
#      or wherever [store] db_path in ~/.agent-director/config.toml puts it.
#      Every exit 5 ends with one line on stderr, its last, naming the
#      cause: `install.sh: err_name=<Name>`. Branch on <Name>, never on the
#      text above it: ErrVersionUnreadable (re-run), ErrConfigMalformed
#      (fix config.toml, then re-run), ErrSchemaMismatch (install a newer
#      agent-director), ErrSchemaVerifyFailed (needs a human), or, when the
#      store open fails another way, agent-director's own err_name.
#
# Idempotent: re-running it after a clean install, with the same
# binaries (a no-flag re-run outside a checkout picks the
# agent-director on PATH and the installed agent-director-admin, as
# above) and the same --no-hooks, symlink and --register-mcp choices,
# ends in the same state and returns 0 on success. Its steps run
# again: the binaries are copied again (with --keep-prior, not
# snapshotted: any earlier .prior files are kept), the store is opened
# (a state.db already at its target version is not migrated) and, with
# hooks on, settings.json and config.toml are merged again (no hook is
# added twice), each after a fresh timestamped .bak. With
# --register-mcp, `claude mcp add` refuses a name already registered in
# its scope, so a re-run from the same directory prints
# "registration failed (continuing anyway)" and still returns 0.

set -euo pipefail

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
# create in ~/.agent-director are usable by their owner too (b.7j2).
umask u=rwx

# --------------------------------------------------------------------
# Defaults + flag parsing
# --------------------------------------------------------------------

readonly DEFAULT_INSTALL_ROOT="${HOME}/.agent-director"
readonly DEFAULT_BIN_DIR="${DEFAULT_INSTALL_ROOT}/bin"
# The operator tool's own directory, never on PATH (b.vqr).
readonly DEFAULT_ADMIN_DIR="${DEFAULT_INSTALL_ROOT}/admin"
readonly DEFAULT_ADMIN_PATH="${DEFAULT_ADMIN_DIR}/agent-director-admin"
readonly DEFAULT_SETTINGS_PATH="${HOME}/.claude/settings.json"

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
if [[ -n "$SHA256_EXPECTED" && ! "$SHA256_EXPECTED" =~ ^[0-9a-f]{64}$ ]]; then
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
if [[ -n "$ADMIN_SHA256_EXPECTED" && ! "$ADMIN_SHA256_EXPECTED" =~ ^[0-9a-f]{64}$ ]]; then
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
#                         install.sh's pre-flight readers, merge check or
#                         symlink check: fix it, then re-run.
#   ErrSchemaMismatch     state.db newer than this binary (step 4's open):
#                         install a newer agent-director.
#   ErrSchemaVerifyFailed step 5 found state.db missing, or not at the
#                         target version, after a successful open; or step 3
#                         could not create the sentinel's temp file: needs
#                         a human.
#   any other name        step 4's open failed with that agent-director
#                         err_name (ErrStoreOpen when its stderr held no
#                         error envelope).
# ErrVersionUnreadable and ErrSchemaVerifyFailed are install.sh's own
# names, not agent-director error names.
# --------------------------------------------------------------------

# ad_exit_5 <err_name> — print exit 5's cause line naming <err_name> on
# stderr, then exit 5.
ad_exit_5() {
    echo "install.sh: err_name=$1" >&2
    exit 5
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

# claude + tmux must be on PATH. `file` is required for the --binary
# architecture probe (SR-2.2) — hard requirement; never silent-skip.
# `sqlite3` is required for the schema-migration flow: it reads the DB's
# ACTUAL user_version through the WAL (raw header bytes are subtly wrong
# for a WAL-mode DB) both to decide whether a migration sentinel is
# needed and to verify the post-open version. Hard requirement — never
# silent-skip; a bad read would either skip a needed migration or pass a
# broken install.
required_tools=(claude tmux jq file sqlite3)
[[ "$FROM_RELEASE" -eq 1 ]] && required_tools+=(curl)
for tool in "${required_tools[@]}"; do
    if ! command -v "$tool" >/dev/null 2>&1; then
        echo "install.sh: required tool not found on PATH: $tool" >&2
        case "$tool" in
            claude)  echo "  Install Claude Code first: https://claude.com/claude-code" >&2 ;;
            tmux)    echo "  Install tmux via your package manager (apt/brew/dnf/etc.)." >&2 ;;
            jq)      echo "  Install jq via your package manager (we use it to safely edit settings.json)." >&2 ;;
            file)    echo "  Install file via your package manager (apt install file / brew install file-formula / dnf install file). Required for the --binary architecture probe." >&2 ;;
            sqlite3) echo "  Install sqlite3 via your package manager (apt install sqlite3 / brew install sqlite / dnf install sqlite). Required to read state.db's schema version for the migration flow." >&2 ;;
            curl)    echo "  --from-release downloads via curl; install it via your package manager." >&2 ;;
        esac
        exit 2
    fi
done

# --------------------------------------------------------------------
# Store database: [store] db_path (b.2io)
#
# agent-director opens the store at [store] db_path in
# ~/.agent-director/config.toml when that is set (pkg/api
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
# ~/.agent-director/state.db, a leading ~/ is joined onto $HOME, an
# absolute path is used unchanged, and anything else (~, ~user/x, ./x,
# ../x) is joined onto ~/.agent-director. Every join is cleaned as Go's
# filepath.Join cleans it (ad_clean_path); an absolute path is not.
# $VAR stays literal.
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
# Called only inside ad_store_db_path's subshell.
ad_db_path_refuse() {
    local config="$1" n="$2" line="$3" reason
    shift 3
    echo "install.sh: cannot tell which store database agent-director opens; refusing to install." >&2
    echo "  config  : $config" >&2
    if [[ "$n" -gt 0 ]]; then
        printf '  line %s  : %s\n' "$n" "$line" >&2
    fi
    for reason in "$@"; do
        echo "  $reason" >&2
    done
    if [[ "$n" -gt 0 ]]; then
        echo "  install.sh reads [store] db_path itself, to check, migrate and verify" >&2
        echo "  the database agent-director opens, and reads only this form of the" >&2
        echo "  file: blank lines, # comments, [name] headers and name = value lines," >&2
        echo "  a name being letters, digits, _ and -, with db_path under [store] set" >&2
        echo "  to a one-line \"...\" holding no backslash, or '...', and an optional" >&2
        echo "  # comment after it." >&2
    fi
    echo "  Nothing was installed or changed. Re-run this install after the change." >&2
    exit 1
}

# ad_store_db_path <config> <home> — print the store database agent-director
# opens for config file <config> and home directory <home> (non-empty),
# resolved as above; a missing <config> gives the default. On a file the
# reader cannot read, print why on stderr and return 1, printing nothing on
# stdout. The body is a subshell under the C locale, so the matching is
# bytewise whatever the caller's locale.
ad_store_db_path() (
    LC_ALL=C
    local config="$1" home="$2" n=0 line table="" seen_store=0 have_value=0
    local value="" key rest resolved name
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
    local re_db_path='^[Dd][Bb]_[Pp][Aa][Tt][Hh]$'
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
    # The advice for a store key before any header (store = { ... }), bare or
    # quoted.
    local -a refuse_top_store=(
        "This sets store as a key (an inline table, say) rather than under a"
        "[store] header. Remove this line. If it sets db_path, set db_path under"
        "the file's [store] header instead, adding that header at the end of the"
        "file if the file has none."
        "${not_here[@]}"
    )

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
                if [[ "$table" =~ $re_store ]]; then
                    if [[ "$table" != store ]]; then
                        ad_db_path_refuse "$config" "$n" "$line" \
                            "agent-director reads this header as [store]: its TOML decoder matches" \
                            "names regardless of letter case. Write it as [store]."
                    fi
                    if [[ "$seen_store" -eq 1 ]]; then
                        ad_db_path_refuse "$config" "$n" "$line" \
                            "This is a second [store] header, and agent-director refuses a table" \
                            "defined twice. Move the lines under it, up to the next header, to" \
                            "under the first [store] header, then remove this header."
                    fi
                    seen_store=1
                fi
                continue
            fi
            # A refused header is never to be removed alone: the lines under it
            # would fall under the header before it, [store] perhaps.
            if [[ "$line" =~ $re_bracket ]]; then
                if [[ "$line" =~ $re_aot ]]; then
                    name="$(ad_toml_name "${BASH_REMATCH[1]}")"
                    if [[ "$line" =~ $re_aot_one && "$name" =~ $re_store ]]; then
                        ad_db_path_refuse "$config" "$n" "$line" \
                            "This makes store an array of tables ([[name]]), and agent-director" \
                            "reads [store] only as one table, so it refuses the file. Write it" \
                            "as [store]."
                    fi
                    ad_db_path_refuse "$config" "$n" "$line" \
                        "This is an array of tables ([[name]]), which agent-director does not" \
                        "read: it ignores the lines under this header, or refuses the file." \
                        "Remove the header and the lines under it, up to the next header."
                fi
                if [[ "$line" =~ $re_header_one ]]; then
                    name="$(ad_toml_name "${BASH_REMATCH[1]}")"
                    if [[ "$name" =~ $re_store ]]; then
                        ad_db_path_refuse "$config" "$n" "$line" \
                            "agent-director reads this header as [store]: a quoted name is the" \
                            "same as the bare one, and its TOML decoder matches names regardless" \
                            "of letter case. Write it as [store]."
                    fi
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
                    if [[ -z "$table" && "$name" =~ $re_store ]]; then
                        ad_db_path_refuse "$config" "$n" "$line" "${refuse_top_store[@]}"
                    fi
                    if [[ "$table" == store && "$name" =~ $re_db_path ]]; then
                        ad_db_path_refuse "$config" "$n" "$line" \
                            "agent-director reads this key as db_path: a quoted key is the same" \
                            "as the bare one, and its TOML decoder matches names regardless of" \
                            "letter case. Write it as db_path."
                    fi
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
                    if [[ "$name" =~ $re_store ]]; then
                        ad_db_path_refuse "$config" "$n" "$line" \
                            "This sets a store key as a dotted key (store.db_path = ..., say)" \
                            "rather than under a [store] header. Move this line, without the" \
                            "store. prefix, to under the file's [store] header, adding that header" \
                            "at the end of the file if the file has none." \
                            "${not_here[@]}"
                    fi
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
            if [[ -z "$table" && "$key" =~ $re_store ]]; then
                ad_db_path_refuse "$config" "$n" "$line" "${refuse_top_store[@]}"
            fi
            if [[ "$table" != store || ! "$key" =~ $re_db_path ]]; then
                continue
            fi
            if [[ "$key" != db_path ]]; then
                ad_db_path_refuse "$config" "$n" "$line" \
                    "agent-director reads this key as db_path: its TOML decoder matches" \
                    "names regardless of letter case. Write it as db_path."
            fi
            if [[ "$have_value" -eq 1 ]]; then
                ad_db_path_refuse "$config" "$n" "$line" \
                    "This sets db_path a second time. Keep one."
            fi
            if [[ "$rest" =~ $re_escape ]]; then
                ad_db_path_refuse "$config" "$n" "$line" \
                    "db_path's \"...\" value holds a backslash, which starts an escape." \
                    "Write the path without escapes; in single quotes ('...') a backslash" \
                    "or a double quote is literal."
            elif [[ "$rest" =~ $re_basic || "$rest" =~ $re_literal ]]; then
                value="${BASH_REMATCH[1]}"
            else
                ad_db_path_refuse "$config" "$n" "$line" \
                    "db_path's value is not a one-line \"...\" or '...' string, optionally" \
                    "followed by a # comment. Write it in that form."
            fi
            if [[ "$value" =~ $re_cntrl ]]; then
                ad_db_path_refuse "$config" "$n" "$line" \
                    "db_path's value holds a control character, such as a tab. install.sh" \
                    "cannot install with one in the store path, so remove it. agent-director" \
                    "then opens the path without it, not any store it already keeps at this" \
                    "one."
            fi
            if [[ "$value" == *\?* ]]; then
                ad_db_path_refuse "$config" "$n" "$line" \
                    "db_path's value holds a '?'. agent-director's store open reads" \
                    "everything from the first '?' on as SQLite options, so it would not" \
                    "open this path. Use a path without '?'."
            fi
            have_value=1
        done <"$config"
    fi

    case "$value" in
        "")    resolved="$(ad_clean_path "${home}/.agent-director/state.db")" ;;
        "~/"*) resolved="$(ad_clean_path "${home}/${value:2}")" ;;
        /*)    resolved="$value" ;;
        *)     resolved="$(ad_clean_path "${home}/.agent-director/${value}")" ;;
    esac
    printf '%s\n' "$resolved"
)

# <<< ad_store_db_path (b.2io) <<<

if ! state_db="$(ad_store_db_path "${DEFAULT_INSTALL_ROOT}/config.toml" "$HOME")"; then
    ad_exit_5 ErrConfigMalformed
fi
# Messages name the store "state.db" when it is the default one, as
# before, and by its path when [store] db_path moves it.
state_db_name="state.db"
if [[ "$state_db" != "$(ad_clean_path "${HOME}/.agent-director/state.db")" ]]; then
    state_db_name="$state_db"
fi

# --------------------------------------------------------------------
# Store busy timeout: [store] busy_timeout_ms (b.c7f)
#
# Every agent-director connection to the store waits up to [store]
# busy_timeout_ms in ~/.agent-director/config.toml for a lock another
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

if ! store_busy_timeout_ms="$(ad_store_busy_timeout_ms "${DEFAULT_INSTALL_ROOT}/config.toml")"; then
    ad_exit_5 ErrConfigMalformed
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

if [[ "$NO_HOOKS" -eq 0 ]] && ! ad_config_merge_check "${DEFAULT_INSTALL_ROOT}/config.toml"; then
    ad_exit_5 ErrConfigMalformed
fi

# --------------------------------------------------------------------
# Symlinked settings.json and config.toml pre-check (b.nw5)
#
# With hooks on, the merges below write through a symlinked
# ~/.claude/settings.json or ~/.agent-director/config.toml to the file
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

if [[ "$NO_HOOKS" -eq 0 ]] && ! ad_link_write_check "$DEFAULT_SETTINGS_PATH"; then
    ad_link_refuse "$DEFAULT_SETTINGS_PATH"
    exit 4
fi
if [[ "$NO_HOOKS" -eq 0 ]] && ! ad_link_write_check "${DEFAULT_INSTALL_ROOT}/config.toml"; then
    ad_link_refuse "${DEFAULT_INSTALL_ROOT}/config.toml"
    ad_exit_5 ErrConfigMalformed
fi

echo "install.sh: pre-flight OK"
echo "  claude  : $(claude --version 2>/dev/null || echo '<unknown>')"
echo "  tmux    : $(tmux -V 2>/dev/null || echo '<unknown>')"
if [[ "$state_db_name" != state.db ]]; then
    echo "  store   : $state_db ([store] db_path in ${DEFAULT_INSTALL_ROOT}/config.toml)"
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
    trap 'rm -f "$tmp_bin" "$tmp_admin"' EXIT

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
        echo "    - install release 0.11.0 or later: bash $0 --from-release <tag of v0.11.0 or later>" >&2
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
# Create install root + bin dir
# --------------------------------------------------------------------

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
if [[ "$KEEP_PRIOR" -eq 1 ]]; then
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
# never ~/.agent-director/bin/ and never on PATH: no option creates a
# symlink for it. Its path is printed once, at the end.
# --------------------------------------------------------------------

trap 'rm -f "$TMP" "$ADMIN_TMP" ${tmp_bin:+"$tmp_bin"} ${tmp_admin:+"$tmp_admin"} ${tmp_sentinel:+"$tmp_sentinel"}' EXIT

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
        echo "  symlink : $target → $CANONICAL"
    fi
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
# ~/.agent-director/state.db, or wherever [store] db_path puts it (read in
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

# ad_user_version <db> — print the DB's user_version, read through the WAL,
# and exit with sqlite3's status. sqlite3's stderr is printed with its
# stdout, so the caller holds the whole read in one variable and its status
# in another, with no file:
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
# [store] db_path in $HOME/.agent-director/config.toml (pkg/api
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
    echo "  (~/.agent-director/state.db when unset), and install.sh read that file" >&2
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

# ad_mode_of <file> — print <file>'s permission bits in octal. stat -L
# reads the mode of the file a symlinked <file> points at, not the link's
# own 777; -c is GNU stat, -f BSD stat.
ad_mode_of() {
    stat -L -c '%a' "$1" 2>/dev/null || stat -L -f '%Lp' "$1"
}

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
    (umask 077; cp -f "$1" "$2")
    chmod "$(ad_mode_of "$1")" "$2"
}

if [[ "$NO_HOOKS" -eq 1 ]]; then
    echo "  hooks   : skipped (--no-hooks)"
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
        --arg cmd "$help_cmd" '
            .hooks //= {}
            | .hooks.SessionStart //= []
            | .hooks.SessionEnd //= []
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
# Optional MCP registration
# --------------------------------------------------------------------

if [[ "$REGISTER_MCP" -eq 1 ]]; then
    if claude mcp add agent-director "$CANONICAL" serve --stdio 2>/dev/null; then
        echo "  mcp     : registered with claude mcp"
    else
        echo "  mcp     : registration failed (continuing anyway)" >&2
    fi
fi

echo "  admin   : $ADMIN_CANONICAL (operator tool, not on PATH; run it only with a human's explicit approval for that run)"
echo "install.sh: done. Try: $CANONICAL help"
