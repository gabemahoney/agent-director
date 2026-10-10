#!/usr/bin/env bash
# uninstall.sh — reverse an agent-director install.
#
# Per SRD §16.2. By default:
#   - Remove the two help hook entries from ~/.claude/settings.json
#     (preserving any other user hooks).
#   - Remove the canonical binary and any `.prior` rollback snapshot
#     under ~/.agent-director/bin/.
#   - Remove the operator tool ~/.agent-director/admin/agent-director-admin,
#     any `.prior` rollback snapshot of it, and its ~/.agent-director/admin/
#     directory (b.vqr).
#   - Remove the PATH symlink (if found at any of the standard
#     locations or at --symlink-dir).
#   - Leave ~/.agent-director/ intact (the operator may want to
#     keep their templates / state.db history).
#
# Flags:
#   --purge              Also rm -rf ~/.agent-director (templates +
#                        state.db). Requires --force or an interactive
#                        confirmation, asked before anything is removed
#                        or changed.
#   --force              Skip the --purge confirmation prompt.
#   --mcp-also           Also run `claude mcp remove agent-director`.
#   --symlink-dir <dir>  Look for the PATH symlink at <dir>; default
#                        is ~/.local/bin.

set -euo pipefail

readonly DEFAULT_INSTALL_ROOT="${HOME}/.agent-director"
readonly DEFAULT_BIN_DIR="${DEFAULT_INSTALL_ROOT}/bin"
readonly DEFAULT_ADMIN_DIR="${DEFAULT_INSTALL_ROOT}/admin"
readonly DEFAULT_SETTINGS_PATH="${HOME}/.claude/settings.json"

PURGE=0
FORCE=0
MCP_ALSO=0
SYMLINK_DIR="${HOME}/.local/bin"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --purge)       PURGE=1; shift ;;
        --force)       FORCE=1; shift ;;
        --mcp-also)    MCP_ALSO=1; shift ;;
        --symlink-dir) SYMLINK_DIR="$2"; shift 2 ;;
        -h|--help)
            sed -n '2,/^$/p' "$0" | sed 's/^# \{0,1\}//'
            exit 0 ;;
        *)
            echo "uninstall.sh: unknown flag: $1" >&2
            exit 2 ;;
    esac
done

# ad_mode_of, ad_resolve_link, ad_link_write_check, ad_replace_keeping_mode
# and ad_backup_keeping_mode are install.sh's functions of the same names
# (b.ojn, b.nw5); keep them in step.
#
# ad_mode_of <file> — print <file>'s permission bits in octal (stat -L: a
# symlink's target, not the link; -c is GNU stat, -f BSD stat).
ad_mode_of() {
    stat -L -c '%a' "$1" 2>/dev/null || stat -L -f '%Lp' "$1"
}

# ad_resolve_link <file> — print the path <file> resolves to through its
# chain of symlinks, or <file> itself when it is not a link: each relative
# target read against its own link's directory, by a loop over readlink
# (older macOS has no readlink -f). Fails after 40 links (a loop).
ad_resolve_link() {
    local path="$1" link hops=0
    while [[ -L "$path" ]]; do
        if (( ++hops > 40 )); then
            echo "uninstall.sh: $1: too many levels of symbolic links" >&2
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
# the directory of the file they resolve to exists and can be written in.
# Otherwise set link_target to the file the links resolve to (empty when
# they loop) and link_why to one sentence saying why, and return 1.
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

# ad_replace_keeping_mode <file> <text> — replace the existing <file> with
# <text> and a newline, keeping its permission bits: write <file>.new
# owner-only (umask 077), give it <file>'s mode, then mv it over <file>. A
# symlinked <file> is written through (b.nw5): the .new goes beside the
# file its link chain resolves to and replaces that file, so the link
# survives and its target gets the edit. A link this cannot write through
# was refused (ad_link_write_check) before any edit; ad_resolve_link's own
# loop failure stays as a backstop for a link changed since.
ad_replace_keeping_mode() {
    local target tmp
    target=$(ad_resolve_link "$1") || return
    tmp="${target}.new"
    rm -f "$tmp"
    (umask 077; printf '%s\n' "$2" >"$tmp")
    chmod "$(ad_mode_of "$target")" "$tmp"
    mv -f "$tmp" "$target"
}

# ad_backup_keeping_mode <file> <bak> — copy <file> to <bak> with <file>'s
# permission bits: remove any <bak> first, copy owner-only (umask 077),
# then chmod. cp reads through a symlinked <file>, so <bak> is a regular
# copy of its target, beside the link. Not cp -p, which also copies ACLs
# and xattrs and fails where the file system cannot take them (NFS homes;
# Ubuntu LP#2087769).
ad_backup_keeping_mode() {
    rm -f "$2"
    (umask 077; cp -f "$1" "$2")
    chmod "$(ad_mode_of "$1")" "$2"
}

# --------------------------------------------------------------------
# Remove hook entries from ~/.claude/settings.json.
# Match by command suffix " help" + path prefix matching the install
# root, so the script only removes ITS entries — other user hooks
# survive verbatim. The new contents are worked out here and written
# after the symlink check below (b.nw5), as config.toml's are.
# --------------------------------------------------------------------

hook_prefix="${DEFAULT_BIN_DIR}/agent-director"
settings_edit=0
if [[ -f "$DEFAULT_SETTINGS_PATH" ]]; then
    if ! command -v jq >/dev/null 2>&1; then
        echo "uninstall.sh: jq is required to safely edit settings.json" >&2
        exit 2
    fi
    existing=$(<"$DEFAULT_SETTINGS_PATH")
    if ! printf '%s' "$existing" | jq empty >/dev/null 2>&1; then
        echo "uninstall.sh: ~/.claude/settings.json is not valid JSON; leaving it alone" >&2
    else
        new=$(printf '%s' "$existing" | jq \
            --arg prefix "$hook_prefix" '
            .hooks //= {}
            | .hooks.SessionStart //= []
            | .hooks.SessionEnd //= []
            | .hooks.SessionStart |= [
                .[] | select(
                  (.hooks | type) != "array"
                  or all(.hooks[]?; (.command // "") | startswith($prefix) | not)
                )
              ]
            | .hooks.SessionEnd |= [
                .[] | select(
                  (.hooks | type) != "array"
                  or all(.hooks[]?; (.command // "") | startswith($prefix) | not)
                )
              ]
        ')
        settings_edit=1
    fi
fi

# --------------------------------------------------------------------
# Reverse install.sh's defaults.inject_help_hook merge: drop that key
# from config.toml. If [defaults] is left empty (no other keys, only
# blank lines or comments), drop the section header too. Symmetric
# with install.sh's Q4=yes config merge, and headers are matched as
# that merge matches them (b.onv): blanks before, inside and after the
# brackets, a trailing # comment, a CRLF's CR, and a UTF-8 byte-order
# mark on line 1. A header missed here leaves the key install wrote
# under it. The header name and the key are matched regardless of
# ASCII letter case too, as that merge matches them ([Defaults],
# INJECT_HELP_HOOK; b.hhk): the merge rewrites a key in any case to
# inject_help_hook, under whichever such header set it, and adds it
# under the first such header when none did. As for settings.json, the
# new contents are written after the symlink check below.
# --------------------------------------------------------------------

CONFIG_TOML="${DEFAULT_INSTALL_ROOT}/config.toml"
config_edit=0
if [[ -f "$CONFIG_TOML" ]]; then
    cleaned=$(LC_ALL=C awk '
        function flush_defaults() {
            has_content = 0
            for (i = 1; i <= n; i++) {
                stripped = lines[i]
                sub(/^[[:space:]]+/, "", stripped)
                if (stripped != "" && stripped !~ /^#/) {
                    has_content = 1
                    break
                }
            }
            if (has_content) {
                print header
                for (i = 1; i <= n; i++) print lines[i]
            }
            delete lines
            n = 0
            header = ""
        }
        BEGIN { in_defaults = 0; n = 0; header = "" }
        { line = $0 }
        NR == 1 { sub(/^\357\273\277/, "", line) }
        line ~ /^[[:blank:]]*\[/ {
            if (in_defaults) {
                flush_defaults()
                in_defaults = 0
            }
            if (line ~ /^[[:blank:]]*\[[[:blank:]]*[Dd][Ee][Ff][Aa][Uu][Ll][Tt][Ss][[:blank:]]*\][[:space:]]*(#.*)?$/) {
                in_defaults = 1
                header = $0
                next
            }
            print
            next
        }
        in_defaults {
            if ($0 ~ /^[[:space:]]*[Ii][Nn][Jj][Ee][Cc][Tt]_[Hh][Ee][Ll][Pp]_[Hh][Oo][Oo][Kk][[:space:]]*=/) {
                next
            }
            lines[++n] = $0
            next
        }
        { print }
        END {
            if (in_defaults) flush_defaults()
        }
    ' "$CONFIG_TOML")
    original=$(<"$CONFIG_TOML")
    if [[ "$cleaned" != "$original" ]]; then
        config_edit=1
    fi
fi

# --------------------------------------------------------------------
# Symlinked settings.json and config.toml (b.nw5)
#
# The edits write through a symlinked settings.json or config.toml to
# the file its links resolve to, keeping the link
# (ad_replace_keeping_mode). That write fails when that file's directory
# cannot be written in (home-manager links into a read-only /nix/store,
# say), so each file about to be edited is checked first, before
# anything is removed or changed, as install.sh checks them in
# pre-flight: settings.json here, config.toml after the --purge
# confirmation below, which decides what its check does. A file that
# cannot be written through is refused (exit 2), naming what to remove
# where it comes from; this check never sees a loop or a missing
# directory, as each edited file resolved to a regular file (-f) above.
# A settings.json that cannot be written through and holds none of
# agent-director's hook entries is left alone instead: it would be
# rewritten (every valid settings.json is) with nothing removed, and a
# refusal would then stop every uninstall until the link went.
# --------------------------------------------------------------------

# ad_link_refuse <file> <what> <remedy>... — report a symlinked <file>
# that ad_link_write_check refused, from link_target and link_why, while
# uninstall.sh has <what> to do to it, with <remedy> lines saying what to
# remove where the file comes from, then exit 2.
ad_link_refuse() {
    local file="$1" what="$2" remedy
    shift 2
    echo "uninstall.sh: cannot $what $file through its symlink; refusing to uninstall." >&2
    echo "  link    : $file" >&2
    if [[ -n "$link_target" ]]; then
        echo "  target  : $link_target" >&2
    fi
    echo "  $link_why" >&2
    echo "  uninstall.sh writes its edits into the file a symlinked settings.json or" >&2
    echo "  config.toml resolves to, keeping the link. Fix the link so it reaches a" >&2
    echo "  file in a directory you can write in, or, where the file comes from (your" >&2
    echo "  dotfiles or home-manager configuration, say), remove:" >&2
    for remedy in "$@"; do
        echo "  $remedy" >&2
    done
    echo "  Nothing was removed or changed. Re-run this uninstall after the change." >&2
    exit 2
}

if [[ "$settings_edit" -eq 1 ]] && ! ad_link_write_check "$DEFAULT_SETTINGS_PATH"; then
    # The entries the filter above removes: those it does not keep.
    removed=$(printf '%s' "$existing" | jq --arg prefix "$hook_prefix" '
        .hooks //= {}
        | [(.hooks.SessionStart // []), (.hooks.SessionEnd // []) | .[]
           | select(
               (.hooks | type) == "array"
               and any(.hooks[]?; (.command // "") | startswith($prefix))
             )]
        | length
    ')
    if [[ "$removed" -eq 0 ]]; then
        settings_edit=0
        echo "uninstall.sh: left $DEFAULT_SETTINGS_PATH alone: it holds no agent-director hook entries, and its symlink cannot be written through"
    else
        ad_link_refuse "$DEFAULT_SETTINGS_PATH" "remove agent-director's hook entries from" \
            "each SessionStart and SessionEnd entry holding a hook whose command starts" \
            "with $hook_prefix."
    fi
fi

# --------------------------------------------------------------------
# --purge confirmation (b.nw5). Asked here, before anything is removed
# or changed, as config.toml's symlink check below depends on it; and
# after settings.json's check, which a purge does not waive (that file
# lives outside ~/.agent-director), so no refusal follows a "y". From
# here PURGE is 1 only for a confirmed purge. An aborted purge is a plain
# uninstall: it goes on, and ends with "--purge aborted" (exit 0) where
# the purge would have run.
# --------------------------------------------------------------------

purge_aborted=0
if [[ "$PURGE" -eq 1 && "$FORCE" -eq 0 ]]; then
    printf "uninstall.sh: --purge will rm -rf %s — proceed? [y/N] " "$DEFAULT_INSTALL_ROOT"
    read -r answer
    case "$answer" in
        y|Y|yes|YES) ;;
        *) PURGE=0; purge_aborted=1 ;;
    esac
fi

# config.toml's symlink check (see "Symlinked settings.json and
# config.toml" above). Under a confirmed purge a config.toml that cannot
# be written through is left alone, with a note, instead of refused: the
# purge's rm -rf removes the link (never the file it resolves to), so a
# refusal would only stop the purge. That file keeps inject_help_hook,
# and the note names it. A config.toml that can be written through is
# edited as without --purge, so a target outside ~/.agent-director (a
# dotfiles repo, say) loses the key install.sh wrote there.
if [[ "$config_edit" -eq 1 ]] && ! ad_link_write_check "$CONFIG_TOML"; then
    if [[ "$PURGE" -eq 1 ]]; then
        config_edit=0
        echo "uninstall.sh: left $CONFIG_TOML alone: its symlink cannot be written through, and --purge removes the link; its target, $link_target, keeps inject_help_hook"
    else
        ad_link_refuse "$CONFIG_TOML" "clear inject_help_hook from" \
            "inject_help_hook from [defaults], and the [defaults] header too when only" \
            "blank lines and comments are left under it."
    fi
fi

if [[ "$settings_edit" -eq 1 ]]; then
    # Backup-before-edit (symmetric with install.sh) so a regressed
    # jq filter is recoverable from a timestamped .bak.
    backup_settings="${DEFAULT_SETTINGS_PATH}.bak.$(date +%Y%m%d-%H%M%S)"
    ad_backup_keeping_mode "$DEFAULT_SETTINGS_PATH" "$backup_settings"
    ad_replace_keeping_mode "$DEFAULT_SETTINGS_PATH" "$new"
    echo "uninstall.sh: backed up prior settings to $backup_settings"
    echo "uninstall.sh: removed help hook entries from $DEFAULT_SETTINGS_PATH"
fi
if [[ "$config_edit" -eq 1 ]]; then
    backup_cfg="${CONFIG_TOML}.bak.$(date +%Y%m%d-%H%M%S)"
    ad_backup_keeping_mode "$CONFIG_TOML" "$backup_cfg"
    ad_replace_keeping_mode "$CONFIG_TOML" "$cleaned"
    echo "uninstall.sh: cleared inject_help_hook from $CONFIG_TOML (backup $backup_cfg)"
fi

# --------------------------------------------------------------------
# Remove binaries (canonical + .prior snapshot + any legacy
# versioned-binary siblings from pre-b.43y installs).
# --------------------------------------------------------------------

if [[ -d "$DEFAULT_BIN_DIR" ]]; then
    for f in "$DEFAULT_BIN_DIR"/agent-director "$DEFAULT_BIN_DIR"/agent-director.*; do
        [[ -e "$f" || -L "$f" ]] || continue
        rm -f "$f"
    done
    echo "uninstall.sh: removed binaries under $DEFAULT_BIN_DIR"
fi

# --------------------------------------------------------------------
# Remove the operator tool agent-director-admin (and its .prior
# rollback snapshot and any install tempfile beside it), then its
# directory (b.vqr). A directory someone put other files in is left in
# place, with a note.
# --------------------------------------------------------------------

if [[ -d "$DEFAULT_ADMIN_DIR" ]]; then
    for f in "$DEFAULT_ADMIN_DIR"/agent-director-admin "$DEFAULT_ADMIN_DIR"/agent-director-admin.*; do
        [[ -e "$f" || -L "$f" ]] || continue
        rm -f "$f"
    done
    if rmdir "$DEFAULT_ADMIN_DIR" 2>/dev/null; then
        echo "uninstall.sh: removed $DEFAULT_ADMIN_DIR"
    else
        echo "uninstall.sh: removed agent-director-admin; left $DEFAULT_ADMIN_DIR, which holds other files"
    fi
fi

# --------------------------------------------------------------------
# Remove PATH symlink (if any).
# --------------------------------------------------------------------

if [[ -L "${SYMLINK_DIR}/agent-director" ]]; then
    rm -f "${SYMLINK_DIR}/agent-director"
    echo "uninstall.sh: removed symlink ${SYMLINK_DIR}/agent-director"
fi

# --------------------------------------------------------------------
# Optional MCP deregistration.
# --------------------------------------------------------------------

if [[ "$MCP_ALSO" -eq 1 ]]; then
    if command -v claude >/dev/null 2>&1; then
        claude mcp remove agent-director 2>/dev/null || true
        echo "uninstall.sh: deregistered agent-director from MCP"
    fi
fi

# --------------------------------------------------------------------
# --purge: full directory removal, confirmed above. rm -rf removes a
# symlink under ~/.agent-director, not the file it points to.
# --------------------------------------------------------------------

if [[ "$purge_aborted" -eq 1 ]]; then
    echo "uninstall.sh: --purge aborted"
    exit 0
fi
if [[ "$PURGE" -eq 1 ]]; then
    rm -rf "$DEFAULT_INSTALL_ROOT"
    echo "uninstall.sh: purged $DEFAULT_INSTALL_ROOT"
fi

echo "uninstall.sh: done"
