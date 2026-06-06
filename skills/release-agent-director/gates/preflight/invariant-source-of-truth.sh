#!/usr/bin/env bash
# gate:        preflight.invariant-source-of-truth
# checks:      SR-16 source-of-truth invariant (no stray version sites)
# delegates:   pkg/ts-bun-client/scripts/check-source-of-truth.ts
# pass:        silent exit 0
# fail:        SR-16 script emits its own JSON lines; this wrapper adds a
#              preflight-level SR-14 header line and exits 1

set -euo pipefail

GATE_LIB="$(cd "$(dirname "$0")/../lib" && pwd)"
# shellcheck source=../lib/emit-diagnostic.sh
source "${GATE_LIB}/emit-diagnostic.sh"

REPO_ROOT="$(git rev-parse --show-toplevel)"

SR16_EXIT=0
(cd "$REPO_ROOT" && bun run pkg/ts-bun-client/scripts/check-source-of-truth.ts) || SR16_EXIT=$?

if [ "$SR16_EXIT" -ne 0 ]; then
  emit_diagnostic \
    "preflight.invariant-source-of-truth" \
    "null" \
    "SR-16 gate fired — see preceding JSON line(s) for offending files" \
    "Fix the source-of-truth violations reported above, then retry."
  exit 1
fi
