#!/usr/bin/env bash
# tree-snapshot.sh — print a deterministic listing of a directory tree, so a
# case can compare two trees (or one tree before and after) with `diff`.
#
# Usage:
#   /opt/driver/tree-snapshot.sh <dir> [--skip <relative path>]...
#
# One line per entry, sorted bytewise by path relative to <dir> ("." is <dir>
# itself), fields separated by a tab:
#   <path> dir  <mode>
#   <path> file <mode> <sha256 of the bytes>
#   <path> link <target as readlink prints it, never followed>
#   <path> other <mode>
# <mode> is the permission bits in octal (stat %a). A directory's size, every
# timestamp and the owner are not listed. --skip drops that path and everything
# under it (give it relative to <dir>, repeatable).
#
# A SQLite store is not compared by its bytes: reading it, even read-only,
# creates or changes its -shm and may leave an empty -wal, so a file called
# state.db or state.db-shm is never listed, and state.db-wal is listed only
# when it holds bytes (an uncheckpointed write is real content). Compare a
# store by PRAGMA user_version, table and index structure (pragma_table_info) and its rows, through
# /opt/driver/sql.sh.
#
# Exit status: 0; 2 on bad usage or a missing <dir>.

set -euo pipefail
export LC_ALL=C

usage() {
    echo "usage: tree-snapshot.sh <dir> [--skip <relative path>]..." >&2
    exit 2
}

[[ $# -ge 1 ]] || usage
dir="$1"
shift
skips=()
while [[ $# -gt 0 ]]; do
    case "$1" in
        --skip)
            [[ $# -ge 2 ]] || usage
            skips+=("${2#./}")
            shift 2
            ;;
        *) usage ;;
    esac
done
[[ -d "$dir" ]] || { echo "tree-snapshot.sh: not a directory: $dir" >&2; exit 2; }

cd "$dir"
find . -print0 | sort -z | while IFS= read -r -d '' p; do
    rel="${p#./}"
    [[ "$p" != . ]] || rel="."
    for s in "${skips[@]+"${skips[@]}"}"; do
        if [[ "$rel" == "$s" || "$rel" == "$s"/* ]]; then
            continue 2
        fi
    done
    case "${rel##*/}" in
        state.db | state.db-shm) continue ;;
        state.db-wal) [[ -s "$p" ]] || continue ;;
    esac
    if [[ -L "$p" ]]; then
        printf '%s\tlink\t%s\n' "$rel" "$(readlink "$p")"
    elif [[ -d "$p" ]]; then
        printf '%s\tdir\t%s\n' "$rel" "$(stat -c %a "$p")"
    elif [[ -f "$p" ]]; then
        printf '%s\tfile\t%s\t%s\n' "$rel" "$(stat -c %a "$p")" "$(sha256sum <"$p" | cut -d' ' -f1)"
    else
        printf '%s\tother\t%s\n' "$rel" "$(stat -c %a "$p")"
    fi
done
