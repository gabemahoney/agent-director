#!/usr/bin/env bash
# test-release-postconditions-negative.sh — negative-path validation companion
# for test-release-postconditions.sh (T4C-S3).
#
# Synthetically injects two failure conditions into an isolated worktree to
# confirm that the postcondition assertion logic (mirrored from
# test-release-postconditions.sh) exits non-zero with an identifying
# ASSERT-FAIL diagnostic — i.e., the harness is not a vacuous pass.
#
# Experiment 1 — dirty-tree injection:
#   After release.sh v0.0.0 --dry-run, appends a space to the tracked file
#   pkg/ts-bun-client/package.json, then runs the clean-tree assertion
#   (git status --porcelain).  Confirms ASSERT-FAIL on stderr.
#
# Experiment 2 — mode-flip injection:
#   After restoring the worktree to clean state, strips the executable bit
#   from skills/release-agent-director/release.sh (100755→100644), then
#   runs the mode-bit assertion (git diff --raw HEAD).  Confirms ASSERT-FAIL
#   on stderr.
#
# Both experiments run in the SAME temp worktree to avoid a second Go
# cross-compile.  The dirty-tree from experiment 1 is reversed via
# `git checkout --` before experiment 2 so the mode-flip baseline is clean.
#
# No residue is left in the operator's working tree:
#   The worktree is removed via `git worktree remove --force` on EXIT.
#
# Usage (from any directory inside the repository):
#   bash skills/release-agent-director/test-release-postconditions-negative.sh
#
# Exit codes:
#   0  both experiments confirmed the assertions catch their respective failures
#   1  an experiment did NOT catch the failure (harness would miss the regression)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(git -C "$SCRIPT_DIR" rev-parse --show-toplevel)"

# Snapshot operator tree state before any experiment so we can detect NEW
# residue rather than requiring a pristine clean tree (other in-progress
# task branches may have legitimate uncommitted changes).
PRE_STATUS="$(git -C "$REPO_ROOT" status --porcelain)"

experiment_worktree=""

cleanup() {
    if [[ -n "$experiment_worktree" && -d "$experiment_worktree" ]]; then
        git -C "$REPO_ROOT" worktree remove --force "$experiment_worktree" 2>/dev/null || true
    fi
}
trap cleanup EXIT

# assert_no_operator_residue: confirm no NEW residue was added to the
# operator's working tree by the experiment.  Pre-existing uncommitted
# changes (other tasks' in-progress work) are not considered residue.
assert_no_operator_residue() {
    local post_status
    post_status="$(git -C "$REPO_ROOT" status --porcelain)"
    if [[ "$post_status" != "$PRE_STATUS" ]]; then
        printf '[neg-test] FAIL — new residue in operator tree after experiment\n' >&2
        printf '[neg-test]   before: %s\n' "$PRE_STATUS" >&2
        printf '[neg-test]   after : %s\n' "$post_status" >&2
        exit 1
    fi
}

# --------------------------------------------------------------------
# Create worktree and run dry-run (shared between both experiments)
# --------------------------------------------------------------------
experiment_worktree="$(mktemp -d "${TMPDIR:-/tmp}/release-neg.XXXXXX")"
rmdir "$experiment_worktree"
git -C "$REPO_ROOT" worktree add --detach "$experiment_worktree"

printf '[neg-test] running dry-run in temp worktree (tree state will be tested)\n'
dry_run_rc=0
(cd "$experiment_worktree" && ./skills/release-agent-director/release.sh v0.0.0 --dry-run) || dry_run_rc=$?
if [[ $dry_run_rc -ne 0 ]]; then
    printf '[neg-test] NOTE: dry-run exited %d — non-postcondition failure; tree state still valid for injection tests\n' "$dry_run_rc" >&2
fi

# -----------------------------------------------------------------------
# Experiment 1: Dirty-tree injection
# -----------------------------------------------------------------------
printf '[neg-test] experiment 1: dirty-tree injection\n'

# Baseline: tree must be clean before injection (T4A invariant).
baseline_status="$(git -C "$experiment_worktree" status --porcelain)"
if [[ -n "$baseline_status" ]]; then
    printf '[neg-test] FAIL — tree dirty before injection (T4A regression?): %s\n' "$baseline_status" >&2
    exit 1
fi
printf '[neg-test] baseline OK — tree is clean before injection\n'

# Inject: append a space to a tracked file so git status --porcelain is non-empty.
printf ' ' >> "$experiment_worktree/pkg/ts-bun-client/package.json"

# Run assertion (identical logic to harness postcondition 1).
assert1_output="$(git -C "$experiment_worktree" status --porcelain)"
if [[ -n "$assert1_output" ]]; then
    printf 'ASSERT-FAIL: clean-tree assertion: %s\n' "$assert1_output" >&2
    printf '[neg-test] experiment 1 PASS — dirty-tree correctly detected (ASSERT-FAIL, would exit non-zero in harness)\n'
else
    printf '[neg-test] FAIL — dirty-tree NOT detected by clean-tree assertion\n' >&2
    exit 1
fi

# Restore worktree to clean state before experiment 2.
git -C "$experiment_worktree" checkout -- pkg/ts-bun-client/package.json

# -----------------------------------------------------------------------
# Experiment 2: Mode-flip injection
# -----------------------------------------------------------------------
printf '[neg-test] experiment 2: mode-flip injection\n'

# Baseline: no mode-flip entries before injection.
baseline_modeflip="$(git -C "$experiment_worktree" diff --raw HEAD | grep -E '^:100755 100644|^:100644 100755' || true)"
if [[ -n "$baseline_modeflip" ]]; then
    printf '[neg-test] FAIL — mode-flip present before injection (unexpected): %s\n' "$baseline_modeflip" >&2
    exit 1
fi
printf '[neg-test] baseline OK — no mode-flip entries before injection\n'

# Inject: strip the executable bit from release.sh (tracked as 100755).
chmod -x "$experiment_worktree/skills/release-agent-director/release.sh"

# Run assertion (identical logic to harness postcondition 2).
assert2_output="$(git -C "$experiment_worktree" diff --raw HEAD | grep -E '^:100755 100644|^:100644 100755' || true)"
if [[ -n "$assert2_output" ]]; then
    printf 'ASSERT-FAIL: mode-bit assertion: %s\n' "$assert2_output" >&2
    printf '[neg-test] experiment 2 PASS — mode-flip correctly detected (ASSERT-FAIL, would exit non-zero in harness)\n'
else
    printf '[neg-test] FAIL — mode-flip NOT detected by mode-bit assertion\n' >&2
    exit 1
fi

# cleanup trap removes the worktree; also clear the var so the trap is a no-op
# (we want to confirm residue in operator tree AFTER cleanup).
git -C "$REPO_ROOT" worktree remove --force "$experiment_worktree" 2>/dev/null || true
experiment_worktree=""

assert_no_operator_residue

printf '[neg-test] both experiments PASSED — assertion logic correctly identifies both failure conditions with ASSERT-FAIL diagnostics\n'
