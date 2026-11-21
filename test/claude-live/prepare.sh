#!/usr/bin/env bash
# Scheduler/container only. The host freezes source with the project's existing
# snapshot tooling; this entry consumes that immutable snapshot as data.
# Usage: prepare.sh SOURCE IDENTITY_JSON SOURCE_TAR OUT TARGET [VERSION]
set -euo pipefail
umask 077
[[ $# == 5 || $# == 6 ]] || { echo 'usage: prepare.sh SOURCE IDENTITY_JSON SOURCE_TAR OUT linux/amd64|linux/arm64 [VERSION]' >&2; exit 2; }
source_dir=$1 identity=$2 source_tar=$3 out=$4 target=$5 version=${6:-}
[[ ${AGENT_DIRECTOR_TEST_SANDBOX:-} == 1 ]] || { echo 'run only in the scheduler container' >&2; exit 2; }
for path in "$source_dir" "$identity" "$source_tar" "$out"; do
    [[ $path == /* && $path != *$'\n'* && $path != *$'\r'* ]] || { echo 'arguments must be absolute paths' >&2; exit 2; }
done
[[ $target == linux/amd64 || $target == linux/arm64 ]] || { echo 'unsupported target' >&2; exit 2; }
[[ ! -L $out ]] || { echo 'output is a symlink' >&2; exit 2; }
if [[ -e $out ]]; then
    [[ -d $out ]] || { echo 'output must be absent or empty' >&2; exit 2; }
    output_entry=$(find "$out" -mindepth 1 -maxdepth 1 -print -quit)
    [[ -z $output_entry ]] || { echo 'output must be absent or empty' >&2; exit 2; }
fi
base=$(jq -er '.revision | select(test("^[0-9a-f]{40}$"))' "$identity")
tree=$(jq -er '.tree | select(test("^[0-9a-f]{40}$"))' "$identity")
version=${version:-0.0.0-live.${tree:0:12}}
[[ $version =~ ^[0-9]+\.[0-9]+\.[0-9]+[-.+a-zA-Z0-9]*$ ]] || { echo 'invalid version stamp' >&2; exit 2; }
actual_base=$(git -C "$source_dir" rev-parse HEAD)
actual_tree=$(git -C "$source_dir" write-tree)
[[ $actual_base == "$base" && $actual_tree == "$tree" ]] || { echo 'snapshot git identity mismatch' >&2; exit 2; }

# Validate the inventory itself before consuming it. A process substitution's
# failing producer does not fail its while loop, even with errexit/pipefail.
jq -e '.files | type == "object" and length > 0 and
    all(to_entries[]; (.key | (split("/") | all(. != "" and . != "." and . != ".." and . != ".git")) and
        (test("[[:cntrl:]\\\\]") | not)) and
        (.value | type == "string" and test("^[0-9a-f]{64}$")))' "$identity" >/dev/null
validation=$(mktemp -d "${TMPDIR:-/tmp}/claude-live-prepare.XXXXXX")
trap 'rm -rf -- "$validation"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
jq -r '.files | to_entries[] | [.key,.value] | @tsv' "$identity" > "$validation/hashes"

# The snapshot archive intentionally omits ancillary trees while retaining its
# Git identity. Every other indexed path and every present non-directory must
# appear in the inventory; extra/untracked source must not reach the compiler.
git -C "$source_dir" ls-files -z > "$validation/indexed"
jq -Rs 'split("\u0000") | map(select(length > 0)) |
    map(select(test("^(\\.scratch|tickets|\\.ad-test-ssh|\\.release-work)(/|$)") | not)) | sort' \
    "$validation/indexed" > "$validation/indexed.json"
find "$source_dir" -mindepth 1 \( -path "$source_dir/.git" -o -path "$source_dir/.scratch" -o \
    -path "$source_dir/tickets" -o -path "$source_dir/.ad-test-ssh" -o -path "$source_dir/.release-work" \) \
    -prune -o ! -type d -printf '%P\0' > "$validation/present"
jq -Rs 'split("\u0000") | map(select(length > 0)) | sort' "$validation/present" > "$validation/present.json"
jq -e --slurpfile indexed "$validation/indexed.json" --slurpfile present "$validation/present.json" \
    '.files | keys == $indexed[0] and keys == $present[0]' "$identity" >/dev/null || \
    { echo 'snapshot file inventory is incomplete or contains extra paths' >&2; exit 2; }
# Each source file is checked, including uncommitted/new files staged only in
# the isolated snapshot by the existing snapshot tool, never in the worktree.
while IFS=$'\t' read -r name want; do
    case "$name" in /*|../*|*/../*|.git/*|.scratch/*|tickets/*|.ad-test-ssh/*|.release-work/*) echo 'unsafe snapshot path' >&2; exit 2;; esac
    [[ -f $source_dir/$name && ! -L $source_dir/$name ]] || { echo "missing source: $name" >&2; exit 2; }
    got=$(sha256sum < "$source_dir/$name"); got=${got%% *}
    [[ $got == "$want" ]] || { echo "source hash mismatch: $name" >&2; exit 2; }
done < "$validation/hashes"
# Hashes alone cannot make an unstaged edit part of the declared snapshot tree.
git -C "$source_dir" diff --no-ext-diff --quiet -- . ':(exclude).scratch' ':(exclude)tickets' \
    ':(exclude).ad-test-ssh' ':(exclude).release-work' || \
    { echo 'source differs from snapshot index' >&2; exit 2; }
rm -rf -- "$validation"
trap - EXIT INT TERM
exec bash "$source_dir/test/claude-live/build-kit.sh" "$source_dir" "$identity" "$source_tar" "$out" "$target" "$version"
