#!/usr/bin/env bash
# test-preflight-sentinel.sh — Epic 4 regression anchor for the preflight
# sentinel assertion (t3.xsh.s7.v1.1n).
#
# Asserts that preflight_phase enforces version="0.0.0" on all three
# package.json files before any staging occurs. Catches regressions where
# the sentinel stanza is silently removed or the wrong file list is checked.
#
# Cases:
#   1. Happy path: all three package.jsons at "0.0.0" → preflight_phase exits 0.
#   2. Negative — umbrella:         pkg/ts-bun-client/package.json mutated to
#      "1.2.3" (committed) → non-zero exit; stderr names file + actual + expected.
#   3. Negative — linux-x64:        pkg/ts-bun-client/platforms/linux-x64/package.json
#      mutated → same assertions.
#   4. Negative — darwin-arm64:     pkg/ts-bun-client/platforms/darwin-arm64/package.json
#      mutated → same assertions.
#
# All mutations are committed inside the detached worktree so the working-tree-
# clean check passes before the sentinel check fires. The live checkout is
# never modified.
#
# Usage (from any directory inside the repository):
#   bash skills/release-agent-director/test-preflight-sentinel.sh
#
# Exit codes:
#   0  all assertions passed
#   1  one or more assertions failed (ASSERT-FAIL printed to stderr)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(git -C "$SCRIPT_DIR" rev-parse --show-toplevel)"
# Source the live release.sh (same pattern as test-build-version-stamp.sh and
# test-notes-phase-heredoc.sh) so the test works against the current source,
# not a git-committed snapshot. The worktree provides a clean git environment
# for preflight_phase's git status / tag / branch checks.
RELEASE_SH="$SCRIPT_DIR/release.sh"

# Synthetic version: clearly non-existent tag so the tag-existence check passes.
TEST_VERSION="v99.98.97"

# The three package.json files the sentinel stanza checks, relative to repo root.
SENTINEL_FILES=(
    "pkg/ts-bun-client/package.json"
    "pkg/ts-bun-client/platforms/linux-x64/package.json"
    "pkg/ts-bun-client/platforms/darwin-arm64/package.json"
)

# --------------------------------------------------------------------
# Worktree setup + EXIT cleanup
# --------------------------------------------------------------------
worktree_dir=""

cleanup() {
    if [[ -n "$worktree_dir" && -d "$worktree_dir" ]]; then
        git -C "$REPO_ROOT" worktree remove --force "$worktree_dir" 2>/dev/null || true
    fi
}
trap cleanup EXIT

worktree_dir="$(mktemp -d "${TMPDIR:-/tmp}/preflight-sentinel.XXXXXX")"
rmdir "$worktree_dir"
git -C "$REPO_ROOT" worktree add --detach "$worktree_dir"

# Initialize: ensure all three sentinel files are at "0.0.0" in the worktree.
# The committed tree may have platform packages at a non-sentinel version if
# the engineer's reset commit hasn't landed yet. We commit any corrections so
# the worktree is clean (passing preflight's clean-tree check) with sentinels
# at the expected baseline before the test cases run.
_init_changed=0
for _sf in "${SENTINEL_FILES[@]}"; do
    _sfull="$worktree_dir/$_sf"
    if [[ "$(jq -r '.version' "$_sfull")" != "0.0.0" ]]; then
        jq '.version = "0.0.0"' "$_sfull" > "${_sfull}.tmp" && mv "${_sfull}.tmp" "$_sfull"
        git -C "$worktree_dir" add "$_sf"
        _init_changed=1
    fi
done
if [[ "$_init_changed" -eq 1 ]]; then
    git -C "$worktree_dir" commit --quiet -m "test: initialize sentinels to 0.0.0"
fi
unset _sf _sfull _init_changed

# --------------------------------------------------------------------
# run_preflight <worktree_dir>
#
# Sources release.sh in a subshell with the minimum env preflight_phase
# needs:
#   VERSION    — synthetic semver
#   DRY_RUN=1  — avoids live steps
#   BRANCH     — overridden to "HEAD" after source (detached worktree)
#
# All git commands inside preflight_phase resolve against the worktree
# because the subshell cd's there before sourcing.
#
# Captures stderr into PREFLIGHT_STDERR.
# Returns the exit code of preflight_phase (0 or 2).
# --------------------------------------------------------------------
PREFLIGHT_STDERR=""

run_preflight() {
    local wt="$1"
    local stderr_tmp
    stderr_tmp="$(mktemp)"
    local rc=0
    (
        set --
        export VERSION="$TEST_VERSION"
        export DRY_RUN=1
        # cd into the worktree so all git commands (status, tag list, branch)
        # and the relative sentinel paths resolve against the worktree, not the
        # live checkout.
        cd "$wt"
        # Source the LIVE release.sh so we test the current sentinel stanza,
        # not whatever snapshot is in the worktree at the detached HEAD commit.
        _RELEASE_SH_SOURCE_ONLY=1 source "$RELEASE_SH"
        # Override BRANCH after source: flag-parsing defaults BRANCH="main" but
        # a detached worktree reports "HEAD" from git rev-parse --abbrev-ref HEAD.
        BRANCH="HEAD"
        # Silence the EXIT-trap summary noise.
        report_phase() { :; }
        preflight_phase
    ) 2>"$stderr_tmp" || rc=$?
    PREFLIGHT_STDERR="$(cat "$stderr_tmp")"
    rm -f "$stderr_tmp"
    return "$rc"
}

# --------------------------------------------------------------------
# mutate_sentinel <worktree_dir> <rel_path> <version>
#
# Rewrites package.json::version to <version> via jq, then stages and
# commits the change. Committing makes the working tree clean so the
# clean-tree check in preflight_phase passes before the sentinel check.
# --------------------------------------------------------------------
mutate_sentinel() {
    local wt="$1" rel_path="$2" ver="$3"
    local full_path="$wt/$rel_path"
    local tmp
    tmp="$(mktemp)"
    jq --arg v "$ver" '.version = $v' "$full_path" > "$tmp"
    mv "$tmp" "$full_path"
    git -C "$wt" add "$rel_path"
    git -C "$wt" commit --quiet -m "test: set ${rel_path}::version to ${ver}"
}

# --------------------------------------------------------------------
# restore_sentinel <worktree_dir> <rel_path>
#
# Restores package.json::version to "0.0.0" and commits, leaving the
# worktree ready for the next test case.
# --------------------------------------------------------------------
restore_sentinel() {
    local wt="$1" rel_path="$2"
    mutate_sentinel "$wt" "$rel_path" "0.0.0"
}

# ====================================================================
# Case 1 — Happy: all three sentinels at "0.0.0"
# ====================================================================
printf '\n=== Case 1: happy path — all three sentinels at "0.0.0" ===\n'

happy_rc=0
run_preflight "$worktree_dir" || happy_rc=$?

if [[ "$happy_rc" -ne 0 ]]; then
    printf 'ASSERT-FAIL [happy]: preflight_phase exited %d; expected 0\n' "$happy_rc" >&2
    printf 'ASSERT-FAIL [happy]: stderr:\n%s\n' "$PREFLIGHT_STDERR" >&2
    exit 1
fi
printf '[happy] preflight_phase exited 0  OK\n'

# ====================================================================
# Cases 2-4 — Negative: each file mutated to "1.2.3" independently
# ====================================================================
total_failures=0

for sentinel_rel in "${SENTINEL_FILES[@]}"; do
    printf '\n=== Negative: %s mutated to "1.2.3" ===\n' "$sentinel_rel"

    mutate_sentinel "$worktree_dir" "$sentinel_rel" "1.2.3"

    neg_rc=0
    run_preflight "$worktree_dir" || neg_rc=$?

    case_failures=0

    # A. Must exit non-zero (sentinel check exits 2).
    if [[ "$neg_rc" -ne 0 ]]; then
        printf '[%s] A: preflight_phase exited non-zero (%d)  OK\n' "$sentinel_rel" "$neg_rc"
    else
        printf 'ASSERT-FAIL [%s]: A: preflight_phase exited 0; expected non-zero\n' \
            "$sentinel_rel" >&2
        case_failures=$((case_failures + 1))
    fi

    # B. Stderr must name the violating file path.
    if printf '%s\n' "$PREFLIGHT_STDERR" | grep -qF "$sentinel_rel"; then
        printf '[%s] B: stderr names violating file  OK\n' "$sentinel_rel"
    else
        printf 'ASSERT-FAIL [%s]: B: stderr does not mention "%s"\n' \
            "$sentinel_rel" "$sentinel_rel" >&2
        printf 'ASSERT-FAIL [%s]: B: captured stderr:\n%s\n' "$sentinel_rel" "$PREFLIGHT_STDERR" >&2
        case_failures=$((case_failures + 1))
    fi

    # C. Stderr must contain the actual (injected) version.
    if printf '%s\n' "$PREFLIGHT_STDERR" | grep -qF '"1.2.3"'; then
        printf '[%s] C: stderr contains actual "1.2.3"  OK\n' "$sentinel_rel"
    else
        printf 'ASSERT-FAIL [%s]: C: stderr does not contain actual "1.2.3"\n' \
            "$sentinel_rel" >&2
        printf 'ASSERT-FAIL [%s]: C: captured stderr:\n%s\n' "$sentinel_rel" "$PREFLIGHT_STDERR" >&2
        case_failures=$((case_failures + 1))
    fi

    # D. Stderr must contain the expected sentinel value.
    if printf '%s\n' "$PREFLIGHT_STDERR" | grep -qF '"0.0.0"'; then
        printf '[%s] D: stderr contains expected "0.0.0"  OK\n' "$sentinel_rel"
    else
        printf 'ASSERT-FAIL [%s]: D: stderr does not contain expected "0.0.0"\n' \
            "$sentinel_rel" >&2
        printf 'ASSERT-FAIL [%s]: D: captured stderr:\n%s\n' "$sentinel_rel" "$PREFLIGHT_STDERR" >&2
        case_failures=$((case_failures + 1))
    fi

    total_failures=$((total_failures + case_failures))

    # Restore to "0.0.0" so the next case starts from a clean sentinel state.
    restore_sentinel "$worktree_dir" "$sentinel_rel"
done

# ====================================================================
# Summary
# ====================================================================
if [[ "$total_failures" -gt 0 ]]; then
    printf '\n=== test-preflight-sentinel: FAILED (%d assertion(s)) ===\n' "$total_failures" >&2
    exit 1
fi

printf '\n=== test-preflight-sentinel: ALL CHECKS PASSED ===\n'
printf 'Happy path and all three per-file negative sentinel cases confirmed.\n'
