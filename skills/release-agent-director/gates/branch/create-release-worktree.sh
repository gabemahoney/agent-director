#!/usr/bin/env bash
# gate: branch.worktree-create  /  branch.bump-commit
#
# Creates an isolated git worktree at origin/main HEAD, branches
# release/v<target>, and applies a single-line version bump commit to
# pkg/ts-bun-client/package.json.
#
# Usage (run from repo root):
#   bash skills/release-agent-director/gates/branch/create-release-worktree.sh <target-version>
#
# Stdout: worktree path on success  (.release-work/release-v<target>)
# Stderr: SR-14 JSON diagnostic on failure
# Exit 0 success | 1 gate failure | 2 bad args

set -euo pipefail

GATE_LIB="$(cd "$(dirname "$0")/../lib" && pwd)"
# shellcheck source=../lib/emit-diagnostic.sh
source "${GATE_LIB}/emit-diagnostic.sh"

# ---------------------------------------------------------------------------
# 1. Validate argument
# ---------------------------------------------------------------------------
TARGET="${1:-}"
if [[ -z "${TARGET}" ]] || ! [[ "${TARGET}" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9._-]+)?$ ]]; then
  printf '{"gate":"branch.worktree-create","offending_file_or_artifact":null,"description":"target version %s is not strict SemVer","corrective_action":"Pass a clean X.Y.Z value."}\n' \
    "${TARGET}" >&2
  exit 2
fi

REPO_ROOT="$(git rev-parse --show-toplevel)"
WORKTREE_REL=".release-work/release-v${TARGET}"
WORKTREE_ABS="${REPO_ROOT}/${WORKTREE_REL}"
BRANCH="release/v${TARGET}"
PKG_JSON_REL="pkg/ts-bun-client/package.json"
PKG_JSON_ABS="${WORKTREE_ABS}/${PKG_JSON_REL}"

# ---------------------------------------------------------------------------
# 2. Guard: worktree must not already exist
# ---------------------------------------------------------------------------
if [ -d "${WORKTREE_ABS}" ]; then
  emit_diagnostic \
    "branch.worktree-create" \
    "${WORKTREE_REL}" \
    "release worktree '${WORKTREE_REL}' already exists from a prior run" \
    "Remove it and retry: git worktree remove --force ${WORKTREE_REL} && git branch -D ${BRANCH}"
  exit 1
fi

# ---------------------------------------------------------------------------
# 3. Fetch origin/main (ensures origin/main ref is current)
# ---------------------------------------------------------------------------
git fetch origin main --quiet 2>/dev/null

# ---------------------------------------------------------------------------
# 4. Create worktree on a new release branch
# ---------------------------------------------------------------------------
GW_ERR_FILE="$(mktemp /tmp/gw_err.XXXXXX)"
if ! git worktree add -b "${BRANCH}" "${WORKTREE_ABS}" origin/main 2>"${GW_ERR_FILE}"; then
  GIT_ERR="$(cat "${GW_ERR_FILE}")"
  rm -f "${GW_ERR_FILE}"
  emit_diagnostic \
    "branch.worktree-create" \
    "${WORKTREE_REL}" \
    "git worktree add failed: ${GIT_ERR}" \
    "Resolve branch conflicts or remove a stale worktree, then retry."
  exit 1
fi
rm -f "${GW_ERR_FILE}"

# ---------------------------------------------------------------------------
# 5. Bump version field in package.json
#    Use sed for surgical in-place replacement to preserve existing formatting.
#    jq would reformat compact arrays ("os", "cpu") into multi-line blocks.
# ---------------------------------------------------------------------------
sed -i "s/\"version\": \"[^\"]*\"/\"version\": \"${TARGET}\"/" "${PKG_JSON_ABS}"

# ---------------------------------------------------------------------------
# 6. Stage and commit inside the worktree
# ---------------------------------------------------------------------------
git -C "${WORKTREE_ABS}" add "${PKG_JSON_REL}"
git -C "${WORKTREE_ABS}" commit -m "chore: release v${TARGET}"

# ---------------------------------------------------------------------------
# 7. Verify diff is exactly the version field change (no surprises)
# ---------------------------------------------------------------------------
DIFF_OUT="$(git -C "${WORKTREE_ABS}" diff origin/main -- "${PKG_JSON_REL}")"
# Lines starting with + or - excluding the --- / +++ file header lines
CHANGED_LINES="$(printf '%s\n' "${DIFF_OUT}" | grep '^[+-]' | grep -v '^---' | grep -v '^+++')"
# Any changed line that does NOT contain the version key is unexpected
UNEXPECTED_COUNT=0
while IFS= read -r line; do
  if [[ "${line}" != *'"version"'* ]]; then
    UNEXPECTED_COUNT=$(( UNEXPECTED_COUNT + 1 ))
  fi
done <<< "${CHANGED_LINES}"

if [ "${UNEXPECTED_COUNT}" -ne 0 ]; then
  emit_diagnostic \
    "branch.bump-commit" \
    "${PKG_JSON_REL}" \
    "bump diff contains ${UNEXPECTED_COUNT} unexpected changed line(s) beyond the version field" \
    "Inspect with: git -C ${WORKTREE_REL} diff origin/main -- ${PKG_JSON_REL}"
  exit 1
fi

# ---------------------------------------------------------------------------
# 8. Success — print worktree path for the caller
# ---------------------------------------------------------------------------
printf '%s\n' "${WORKTREE_REL}"
exit 0
