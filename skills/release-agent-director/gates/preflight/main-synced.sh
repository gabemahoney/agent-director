#!/usr/bin/env bash
# preflight gate: main-synced
#
# Asserts:
#   1. Current branch is RELEASE_SOURCE_BRANCH (default: main).
#   2. Local branch is exactly in sync with origin/<branch> (no ahead, no behind, no diverge).
#
# Pass: silent exit 0.
# Fail: emits SR-14 JSON to stderr, exits 1.

set -euo pipefail

GATE_LIB="$(cd "$(dirname "$0")/../lib" && pwd)"
# shellcheck source=../lib/emit-diagnostic.sh
source "${GATE_LIB}/emit-diagnostic.sh"

RELEASE_SOURCE_BRANCH="${RELEASE_SOURCE_BRANCH:-main}"

# --- 1. Branch check ---
current_branch="$(git rev-parse --abbrev-ref HEAD 2>/dev/null)"

if [ "$current_branch" != "$RELEASE_SOURCE_BRANCH" ]; then
  emit_diagnostic \
    "preflight.main-synced" \
    "$current_branch" \
    "current branch is '${current_branch}', expected '${RELEASE_SOURCE_BRANCH}'" \
    "Switch to ${RELEASE_SOURCE_BRANCH} before releasing: git checkout ${RELEASE_SOURCE_BRANCH}"
  exit 1
fi

# --- 2. Fetch origin ---
git fetch origin "${RELEASE_SOURCE_BRANCH}" --quiet 2>/dev/null

# --- 3. Divergence check ---
local_sha="$(git rev-parse HEAD)"
remote_sha="$(git rev-parse "origin/${RELEASE_SOURCE_BRANCH}")"
merge_base="$(git merge-base HEAD "origin/${RELEASE_SOURCE_BRANCH}")"

if [ "$local_sha" = "$remote_sha" ]; then
  exit 0
fi

if [ "$merge_base" = "$remote_sha" ]; then
  # local is ahead
  ahead="$(git rev-list "origin/${RELEASE_SOURCE_BRANCH}..HEAD" --count)"
  emit_diagnostic \
    "preflight.main-synced" \
    "$current_branch" \
    "local ${RELEASE_SOURCE_BRANCH} is ${ahead} commit(s) ahead of origin/${RELEASE_SOURCE_BRANCH}" \
    "Push your commits: git push origin ${RELEASE_SOURCE_BRANCH}"
  exit 1
fi

if [ "$merge_base" = "$local_sha" ]; then
  # local is behind
  behind="$(git rev-list "HEAD..origin/${RELEASE_SOURCE_BRANCH}" --count)"
  emit_diagnostic \
    "preflight.main-synced" \
    "$current_branch" \
    "local ${RELEASE_SOURCE_BRANCH} is ${behind} commit(s) behind origin/${RELEASE_SOURCE_BRANCH}" \
    "Pull the latest: git pull --ff-only origin ${RELEASE_SOURCE_BRANCH}"
  exit 1
fi

# diverged
ahead="$(git rev-list "origin/${RELEASE_SOURCE_BRANCH}..HEAD" --count)"
behind="$(git rev-list "HEAD..origin/${RELEASE_SOURCE_BRANCH}" --count)"
emit_diagnostic \
  "preflight.main-synced" \
  "$current_branch" \
  "local ${RELEASE_SOURCE_BRANCH} has diverged from origin/${RELEASE_SOURCE_BRANCH} (${ahead} ahead, ${behind} behind)" \
  "Rebase or reset: git fetch origin && git reset --hard origin/${RELEASE_SOURCE_BRANCH}"
exit 1
