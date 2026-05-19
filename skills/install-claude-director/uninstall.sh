#!/usr/bin/env bash
# uninstall.sh — reverse a claude-director install.
#
# Per SRD §16.2. By default:
#   - Remove the two help hook entries from ~/.claude/settings.json
#     (preserving any other user hooks).
#   - Remove the canonical binary symlink + every versioned sibling
#     under ~/.claude-director/bin/.
#   - Remove the PATH symlink (if found at any of the standard
#     locations or at --symlink-dir).
#   - Leave ~/.claude-director/ intact (the operator may want to
#     keep their templates / state.db history).
#
# Flags:
#   --purge              Also rm -rf ~/.claude-director (templates +
#                        state.db). Requires --force or an interactive
#                        confirmation.
#   --force              Skip the --purge confirmation prompt.
#   --mcp-also           Also run `claude mcp remove claude-director`.
#   --symlink-dir <dir>  Look for the PATH symlink at <dir>; default
#                        is ~/.local/bin.

set -euo pipefail

readonly DEFAULT_INSTALL_ROOT="${HOME}/.claude-director"
readonly DEFAULT_BIN_DIR="${DEFAULT_INSTALL_ROOT}/bin"
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

# --------------------------------------------------------------------
# Remove hook entries from ~/.claude/settings.json.
# Match by command suffix " help" + path prefix matching the install
# root, so the script only removes ITS entries — other user hooks
# survive verbatim.
# --------------------------------------------------------------------

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
            --arg prefix "${DEFAULT_BIN_DIR}/claude-director" '
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
        tmp="${DEFAULT_SETTINGS_PATH}.new"
        printf '%s\n' "$new" > "$tmp"
        mv -f "$tmp" "$DEFAULT_SETTINGS_PATH"
        echo "uninstall.sh: removed help hook entries from $DEFAULT_SETTINGS_PATH"
    fi
fi

# --------------------------------------------------------------------
# Remove binaries (canonical + versioned siblings).
# --------------------------------------------------------------------

if [[ -d "$DEFAULT_BIN_DIR" ]]; then
    for f in "$DEFAULT_BIN_DIR"/claude-director "$DEFAULT_BIN_DIR"/claude-director.*; do
        [[ -e "$f" || -L "$f" ]] || continue
        rm -f "$f"
    done
    echo "uninstall.sh: removed binaries under $DEFAULT_BIN_DIR"
fi

# --------------------------------------------------------------------
# Remove PATH symlink (if any).
# --------------------------------------------------------------------

if [[ -L "${SYMLINK_DIR}/claude-director" ]]; then
    rm -f "${SYMLINK_DIR}/claude-director"
    echo "uninstall.sh: removed symlink ${SYMLINK_DIR}/claude-director"
fi

# --------------------------------------------------------------------
# Optional MCP deregistration.
# --------------------------------------------------------------------

if [[ "$MCP_ALSO" -eq 1 ]]; then
    if command -v claude >/dev/null 2>&1; then
        claude mcp remove claude-director 2>/dev/null || true
        echo "uninstall.sh: deregistered claude-director from MCP"
    fi
fi

# --------------------------------------------------------------------
# --purge: full directory removal.
# --------------------------------------------------------------------

if [[ "$PURGE" -eq 1 ]]; then
    if [[ "$FORCE" -eq 0 ]]; then
        printf "uninstall.sh: --purge will rm -rf %s — proceed? [y/N] " "$DEFAULT_INSTALL_ROOT"
        read -r answer
        case "$answer" in
            y|Y|yes|YES) ;;
            *) echo "uninstall.sh: --purge aborted"; exit 0 ;;
        esac
    fi
    rm -rf "$DEFAULT_INSTALL_ROOT"
    echo "uninstall.sh: purged $DEFAULT_INSTALL_ROOT"
fi

echo "uninstall.sh: done"
