#!/usr/bin/env bash
# preflight gate: worktree-clean
#
# Asserts the working tree has no modified, staged, or untracked files.
# Pass: silent exit 0.
# Fail: emits SR-14 JSON to stderr, exits 1.

set -euo pipefail

GATE_LIB="$(cd "$(dirname "$0")/../lib" && pwd)"
# shellcheck source=../lib/emit-diagnostic.sh
source "${GATE_LIB}/emit-diagnostic.sh"

dirty="$(git status --porcelain 2>/dev/null)"

if [ -z "$dirty" ]; then
  exit 0
fi

count="$(printf '%s\n' "$dirty" | grep -c .)"
first_path="$(printf '%s\n' "$dirty" | head -n1 | awk '{print $NF}')"

emit_diagnostic \
  "preflight.worktree-clean" \
  "$first_path" \
  "working tree has uncommitted changes: ${count} file(s) (first: ${first_path})" \
  "Commit, stash, or discard before retrying."

exit 1
