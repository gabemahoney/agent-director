#!/usr/bin/env bash
# gate:        coverage.go-root
# checks:      go test ./... -race -count=1 at the repo root
# usage:       bash go-root.sh [<worktree-root>]
# pass:        silent exit 0 (test output flows through unfiltered)
# fail:        emit SR-14 JSON diagnostic to stderr, exit 1

set -uo pipefail

GATE_LIB="$(cd "$(dirname "$0")/../lib" && pwd)"
# shellcheck source=../lib/emit-diagnostic.sh
source "${GATE_LIB}/emit-diagnostic.sh"

if [ -n "${1:-}" ]; then
  cd "$1" || { printf 'go-root.sh: cannot cd into worktree root: %s\n' "$1" >&2; exit 1; }
fi

# Keep failed runs outside the parallel executor's disposable capture directory.
# A unique name preserves the evidence when the operator retries the gate.
LOG_DIR="$(pwd -P)/dist/release-logs"
if ! mkdir -p "$LOG_DIR"; then
  emit_diagnostic \
    "coverage.go-root" "$LOG_DIR" \
    "Cannot create the go test log directory: ${LOG_DIR}. No tests ran." \
    "Make the worktree's dist/release-logs directory writable and re-run the gate."
  exit 1
fi
if ! TEST_LOG="$(mktemp "${LOG_DIR}/coverage.go-root.XXXXXX")"; then
  emit_diagnostic \
    "coverage.go-root" "$LOG_DIR" \
    "Cannot create a go test log in: ${LOG_DIR}. No tests ran." \
    "Check disk space and permissions in dist/release-logs and re-run the gate."
  exit 1
fi

# Run tests; tee so caller sees progress, capture combined output for parsing.
go test ./... -race -count=1 -timeout 90m 2>&1 | tee "$TEST_LOG"
RUN_STATUS=("${PIPESTATUS[@]}")
TEST_EXIT="${RUN_STATUS[0]}"
LOG_EXIT="${RUN_STATUS[1]}"

if [ "$LOG_EXIT" -ne 0 ] || [ ! -f "$TEST_LOG" ] || [ ! -r "$TEST_LOG" ]; then
  emit_diagnostic \
    "coverage.go-root" "$TEST_LOG" \
    "Could not retain the complete go test output at: ${TEST_LOG}. The log may be incomplete or missing (go test exit ${TEST_EXIT}, tee exit ${LOG_EXIT})." \
    "Check disk space and permissions in dist/release-logs before re-running the gate."
  exit 1
fi

if [ "$TEST_EXIT" -eq 0 ]; then
  if ! rm -f "$TEST_LOG"; then
    emit_diagnostic \
      "coverage.go-root" "$TEST_LOG" \
      "go test passed, but its temporary success log could not be removed: ${TEST_LOG}." \
      "Check permissions in dist/release-logs and remove the success log."
    exit 1
  fi
  exit 0
fi

# Parse first failing package from go test output.
#
# Go emits package-level failures as tab-separated "FAIL\t<import/path>\t<time>"
# lines (the import path always contains a "/"). A naive `grep '^FAIL' | awk
# '{print $2}'` also matches free-text "FAIL"-prefixed content — e.g. the
# trail-leak canary's failure dump — and would report a stray word like "a" as
# the failing package (b.93m). Anchor on the real shape: FAIL, then a tab, then
# a package import path, and take that path field.
FIRST_FAIL="$(grep -m1 -E '^FAIL[[:space:]]+[^[:space:]]+/' "$TEST_LOG" | awk -F'\t' '{print $2}')"
if [ -z "$FIRST_FAIL" ]; then
  FIRST_FAIL="(unknown package)"
fi

# Scan the entire log, keeping every failure header (including subtests) and
# up to 20 following indented message lines per header. Long messages remain
# available verbatim in TEST_LOG rather than crowding out later failures.
FAILURE_SUMMARY="$(awk '
  /^[[:space:]]*--- FAIL:/ { print; messages = 0; in_failure = 1; next }
  in_failure && /^[[:space:]]/ {
    if (messages < 20) print
    else if (messages == 20) print "    [additional message lines omitted; see full log]"
    messages++
    next
  }
  { in_failure = 0 }
' "$TEST_LOG")"

STDERR_EXCERPT="$(tail -n 50 "$TEST_LOG")"

DESCRIPTION="go test failed in package: ${FIRST_FAIL}"
CORRECTIVE="Inspect the saved log, fix the failing tests, and re-run the gate."

# Build description with excerpt appended (newline-escaped by emit_diagnostic).
FULL_DESC="${DESCRIPTION}
Full go test output saved to: ${TEST_LOG}
--- failing tests (up to 20 message lines each) ---
${FAILURE_SUMMARY}
--- last 50 lines ---
${STDERR_EXCERPT}"

emit_diagnostic \
  "coverage.go-root" \
  "${FIRST_FAIL}" \
  "${FULL_DESC}" \
  "${CORRECTIVE}"

exit 1
