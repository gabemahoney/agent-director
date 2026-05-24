#!/usr/bin/env bash
# toolchain-pin-diff.sh — emit a markdown section listing C toolchain
# pin changes since the previous release tag.
#
# Per SRD §SR-2.3, any change to a pinned C toolchain version (gcc on
# linux/amd64 and the Bun runtime version used for the TS smoke leg)
# must be surfaced in release notes. release.sh invokes this script
# after the notes phase templates the per-Epic git-log section and
# BEFORE gh-release reads the notes file.
#
# Pin sources:
#   - `.github/workflows/cabi-matrix.yml`
#       * `gcc-11`  — apt package name on the linux-amd64 leg.
#                     Token: `apt-get install -y --no-install-recommends gcc-NN`.
#       * `BUN_VERSION: '<x.y.z>'` — workflow env scalar.
#   - The darwin-arm64 leg's Xcode pin is operator-configured on the
#     self-hosted runner (not in the workflow file); see
#     `docs/self-hosted-runner-setup.md`. It is therefore NOT diffed
#     here — its changes are surfaced in the release notes manually
#     by the operator when bumping.
#   - darwin/amd64 was dropped from v1 on 2026-05-24, so its Xcode pin
#     no longer exists in the workflow file. The extraction below is
#     retained as a regex-no-op for backward compatibility with older
#     refs that still contained the pin.
#
# Extraction strategy: pure grep/sed regex over the workflow YAML at
# both the current commit and the previous release tag. We
# deliberately avoid `yq` (extra dependency) because the targets are
# well-formed scalars and string literals; the regex is documented
# inline below. If `yq` IS available the script could be retrofitted
# later — for now the regex form is the canonical form.
#
# Usage:
#   ./toolchain-pin-diff.sh [<prev-tag>] [<current-commit>]
#   PREV_PINS_FILE=/tmp/a CURRENT_PINS_FILE=/tmp/b ./toolchain-pin-diff.sh
#
# The first form reads pins from the named git refs in the current
# repo. The second form (env-var override) reads pre-extracted
# `<pin-name>=<value>` text files and is the smoke-test entry point.
#
# Output: markdown to stdout. Empty stdout if no pins differ.
# Exit:   0 always; non-zero only on script-internal errors.

set -euo pipefail

# --------------------------------------------------------------------
# extract_pins <git-ref-or-empty>
#
# Echo `<name>=<value>` lines for each pin found in the cabi-matrix
# workflow at the given git ref. Empty arg → read the working tree.
# --------------------------------------------------------------------

extract_pins() {
    local ref="$1"
    local wf=".github/workflows/cabi-matrix.yml"
    local content

    if [[ -z "$ref" ]]; then
        if [[ ! -f "$wf" ]]; then return 0; fi
        content=$(cat "$wf")
    else
        # `git show <ref>:<path>` returns the file content at that ref.
        # On a ref where the file did not yet exist git exits non-zero;
        # we surface that as "no pins" (empty output) rather than fail.
        if ! content=$(git show "${ref}:${wf}" 2>/dev/null); then
            return 0
        fi
    fi

    # gcc-NN pin. The apt-get line in the workflow is the source of
    # truth for the linux-amd64 leg's compiler pin.
    local gcc_pin
    gcc_pin=$(printf '%s' "$content" | grep -Eo 'gcc-[0-9]+' | head -n 1 || true)
    if [[ -n "$gcc_pin" ]]; then
        printf 'gcc(linux-amd64)=%s\n' "$gcc_pin"
    fi

    # Xcode_X.Y.app pin. The xcode-select -switch invocation on the
    # darwin-amd64 leg was the source of truth before darwin/amd64 was
    # dropped (2026-05-24). The extraction is retained so a diff
    # against an older release tag still surfaces the removal as a
    # toolchain-pin change.
    local xcode_pin
    xcode_pin=$(printf '%s' "$content" | grep -Eo 'Xcode_[0-9]+(\.[0-9]+)?(\.app)?' | head -n 1 || true)
    if [[ -n "$xcode_pin" ]]; then
        # Normalize to "Xcode X.Y" for human-readable diff output.
        xcode_pin=${xcode_pin%.app}
        xcode_pin=${xcode_pin/_/ }
        printf 'xcode(darwin-amd64)=%s\n' "$xcode_pin"
    fi

    # BUN_VERSION env scalar. Quoted single-line.
    local bun_pin
    bun_pin=$(printf '%s' "$content" | grep -E "^[[:space:]]*BUN_VERSION:[[:space:]]*'" | head -n 1 \
              | sed -E "s/.*BUN_VERSION:[[:space:]]*'([^']+)'.*/\1/" || true)
    if [[ -n "$bun_pin" ]]; then
        printf 'bun=%s\n' "$bun_pin"
    fi
}

# --------------------------------------------------------------------
# diff_pins <prev-file> <current-file>
#
# Read two `<name>=<value>` files and emit a markdown section
# describing changes. Silent when there are no differences.
# --------------------------------------------------------------------

diff_pins() {
    local prev_file="$1" cur_file="$2"

    # Build a name → old value map and name → new value map.
    local -A prev_map=() cur_map=()
    local line name val
    if [[ -s "$prev_file" ]]; then
        while IFS= read -r line; do
            name="${line%%=*}"; val="${line#*=}"
            [[ -n "$name" ]] && prev_map["$name"]="$val"
        done < "$prev_file"
    fi
    if [[ -s "$cur_file" ]]; then
        while IFS= read -r line; do
            name="${line%%=*}"; val="${line#*=}"
            [[ -n "$name" ]] && cur_map["$name"]="$val"
        done < "$cur_file"
    fi

    # Compute the union of pin names so adds / removes / changes all show.
    # When prev_map or cur_map is empty, bash's ${!array[@]} expansion is
    # a fatal error under `set -u` even with the `:-` fallback, so we
    # guard each iteration explicitly.
    local -A union=()
    if (( ${#prev_map[@]} > 0 )); then
        for name in "${!prev_map[@]}"; do union["$name"]=1; done
    fi
    if (( ${#cur_map[@]} > 0 )); then
        for name in "${!cur_map[@]}"; do union["$name"]=1; done
    fi

    if (( ${#union[@]} == 0 )); then
        return 0
    fi

    # Walk the union deterministically (sorted) and collect changes.
    local -a changes=()
    local sorted_name old new
    while IFS= read -r sorted_name; do
        old="${prev_map[$sorted_name]:-(none)}"
        new="${cur_map[$sorted_name]:-(removed)}"
        if [[ "$old" != "$new" ]]; then
            changes+=("- \`$sorted_name\`: $old → $new")
        fi
    done < <(printf '%s\n' "${!union[@]}" | sort)

    if (( ${#changes[@]} == 0 )); then
        return 0
    fi

    # Caller resolves the prev-tag label; we just print the section
    # heading using ${TOOLCHAIN_DIFF_PREV_LABEL:-previous release}.
    printf '\n## Toolchain pin changes since %s\n\n' \
        "${TOOLCHAIN_DIFF_PREV_LABEL:-previous release}"
    printf '%s\n' "${changes[@]}"
}

# --------------------------------------------------------------------
# main
# --------------------------------------------------------------------

main() {
    local prev_file cur_file cleanup=0

    if [[ -n "${PREV_PINS_FILE:-}" && -n "${CURRENT_PINS_FILE:-}" ]]; then
        # Smoke-test path: caller pre-extracted the pin sets.
        prev_file="$PREV_PINS_FILE"
        cur_file="$CURRENT_PINS_FILE"
    else
        local prev_ref="${1:-}" cur_ref="${2:-}"
        # If no explicit prev tag given, derive it the same way
        # release.sh does: newest v*.*.* tag.
        if [[ -z "$prev_ref" ]]; then
            prev_ref=$(git tag --list "v*.*.*" --sort=-version:refname | head -n 1 || true)
        fi
        prev_file=$(mktemp)
        cur_file=$(mktemp)
        cleanup=1
        if [[ -n "$prev_ref" ]]; then
            extract_pins "$prev_ref" > "$prev_file" || true
        fi
        extract_pins "$cur_ref" > "$cur_file" || true
        if [[ -n "$prev_ref" ]]; then
            TOOLCHAIN_DIFF_PREV_LABEL="$prev_ref"
        fi
    fi

    diff_pins "$prev_file" "$cur_file"

    if [[ "$cleanup" -eq 1 ]]; then
        rm -f "$prev_file" "$cur_file"
    fi
}

main "$@"
