#!/usr/bin/env bash
# gate:        coverage.go-consumer-dryrun
# checks:      go test ./... -race -count=1 in tools/consumer-dryrun/
# usage:       bash go-consumer-dryrun.sh [<worktree-root>]
# pass:        silent exit 0 (test output flows through unfiltered)
# fail:        emit SR-14 JSON diagnostic to stderr, exit 1

set -uo pipefail

GATE_LIB="$(cd "$(dirname "$0")/../lib" && pwd)"
# shellcheck source=../lib/emit-diagnostic.sh
source "${GATE_LIB}/emit-diagnostic.sh"

if [ -n "${1:-}" ]; then
  cd "$1/tools/consumer-dryrun"
else
  cd tools/consumer-dryrun
fi

TMPOUT="$(mktemp)"
trap 'rm -f "$TMPOUT"' EXIT

# Run tests; tee so caller sees progress, capture combined output for parsing.
go test ./... -race -count=1 2>&1 | tee "$TMPOUT"
TEST_EXIT="${PIPESTATUS[0]}"

if [ "$TEST_EXIT" -eq 0 ]; then
  exit 0
fi

# Parse first failing package from go test output.
FIRST_FAIL="$(grep -m1 '^FAIL' "$TMPOUT" | awk '{print $2}')"
if [ -z "$FIRST_FAIL" ]; then
  FIRST_FAIL="(unknown package — tools/consumer-dryrun)"
fi

STDERR_EXCERPT="$(tail -n 50 "$TMPOUT")"

DESCRIPTION="go test failed in package: ${FIRST_FAIL}"
CORRECTIVE="Fix failing tests in tools/consumer-dryrun/${FIRST_FAIL} and re-run the gate."

FULL_DESC="${DESCRIPTION}
--- last 50 lines ---
${STDERR_EXCERPT}"

emit_diagnostic \
  "coverage.go-consumer-dryrun" \
  "${FIRST_FAIL}" \
  "${FULL_DESC}" \
  "${CORRECTIVE}"

exit 1
