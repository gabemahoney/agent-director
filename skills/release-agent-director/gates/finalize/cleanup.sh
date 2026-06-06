#!/usr/bin/env bash
# SR-13 finalize: worktree cleanup gate.
#
# Handles post-pipeline teardown based on release outcome.
# On success, removes the worktree, branch, and parent dir.
# On failure or dry-run, preserves all artifacts for operator inspection.
#
# Usage:
#   bash finalize/cleanup.sh --outcome <success|failed|dry-run> \
#                            --worktree <path> \
#                            [--target <version>]
#
# Arguments:
#   --outcome   success | failed | dry-run
#   --worktree  path to the release worktree (absolute or relative to repo root)
#   --target    semver string (required for success path to delete the branch)
#
# Exit codes:
#   0  outcome handled (all paths succeed with exit 0 when args are valid)
#   2  bad arguments

set -euo pipefail

# ---------------------------------------------------------------------------
# 1. Parse arguments
# ---------------------------------------------------------------------------
OUTCOME=""
WORKTREE=""
TARGET=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --outcome)
      OUTCOME="${2:?--outcome requires a value}"
      shift 2
      ;;
    --worktree)
      WORKTREE="${2:?--worktree requires a value}"
      shift 2
      ;;
    --target)
      TARGET="${2:?--target requires a value}"
      shift 2
      ;;
    *)
      printf 'ERROR: unknown option: %s\n' "$1" >&2
      exit 2
      ;;
  esac
done

if [[ -z "$OUTCOME" ]]; then
  printf 'ERROR: --outcome is required (success|failed|dry-run)\n' >&2
  exit 2
fi

if [[ -z "$WORKTREE" ]]; then
  printf 'ERROR: --worktree is required\n' >&2
  exit 2
fi

case "$OUTCOME" in
  success|failed|dry-run) ;;
  *)
    printf 'ERROR: --outcome must be one of: success, failed, dry-run (got: %s)\n' "$OUTCOME" >&2
    exit 2
    ;;
esac

# ---------------------------------------------------------------------------
# 2. Outcome: failed — preserve everything, inform operator
# ---------------------------------------------------------------------------
if [[ "$OUTCOME" == "failed" ]]; then
  printf 'release run failed; worktree preserved at %s; inspect or rm -rf when done\n' "$WORKTREE"
  exit 0
fi

# ---------------------------------------------------------------------------
# 3. Outcome: dry-run — preserve everything, point operator at artifacts
# ---------------------------------------------------------------------------
if [[ "$OUTCOME" == "dry-run" ]]; then
  printf 'dry-run complete; artifacts at %s/dist/ and %s\n' "$WORKTREE" "$WORKTREE"
  exit 0
fi

# ---------------------------------------------------------------------------
# 4. Outcome: success — remove worktree, branch, and parent dir if empty
# ---------------------------------------------------------------------------

# 4a. Remove the git worktree
if git worktree list --porcelain | grep -q "^worktree.*${WORKTREE}$" 2>/dev/null \
   || git worktree list | grep -qF "$WORKTREE" 2>/dev/null; then
  git worktree remove --force "$WORKTREE"
  printf 'worktree removed: %s\n' "$WORKTREE"
else
  # Worktree not registered (may have been removed already); clean the dir if it exists
  if [[ -d "$WORKTREE" ]]; then
    rm -rf "$WORKTREE"
    printf 'worktree directory removed (was not registered): %s\n' "$WORKTREE"
  else
    printf 'worktree already absent: %s\n' "$WORKTREE"
  fi
fi

# 4b. Delete the local release branch (requires --target)
if [[ -n "$TARGET" ]]; then
  BRANCH="release/v${TARGET}"
  if git show-ref --verify --quiet "refs/heads/${BRANCH}" 2>/dev/null; then
    git branch -D "$BRANCH"
    printf 'branch deleted: %s\n' "$BRANCH"
  else
    printf 'branch already absent: %s\n' "$BRANCH"
  fi
else
  printf 'WARNING: --target not supplied; skipping branch deletion\n' >&2
fi

# 4c. Remove the .release-work/ parent dir if now empty
REPO_ROOT="$(git rev-parse --show-toplevel)"
PARENT_DIR="${REPO_ROOT}/.release-work"
if [[ -d "$PARENT_DIR" ]]; then
  # rmdir only removes an empty directory; ignore non-empty
  if rmdir "$PARENT_DIR" 2>/dev/null; then
    printf '.release-work/ removed (was empty)\n'
  else
    printf '.release-work/ retained (not empty)\n'
  fi
fi

printf 'cleanup complete\n'
exit 0
