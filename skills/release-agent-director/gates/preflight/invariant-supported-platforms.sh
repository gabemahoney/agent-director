#!/usr/bin/env bash
# gate:        preflight.invariant-supported-platforms
# checks:      Makefile release-binaries cross-compile target list equals
#              exactly {linux/amd64, linux/arm64, darwin/arm64}
# pass:        silent exit 0
# fail:        SR-14 JSON to stderr, exit 1

set -euo pipefail

GATE_LIB="$(cd "$(dirname "$0")/../lib" && pwd)"
# shellcheck source=../lib/emit-diagnostic.sh
source "${GATE_LIB}/emit-diagnostic.sh"

REPO_ROOT="$(git rev-parse --show-toplevel)"
MAKEFILE="${REPO_ROOT}/Makefile"

EXPECTED_SORTED="linux/amd64 linux/arm64 darwin/arm64"

# Extract the `for target in ...` line from the release-binaries recipe.
# Recipe lines carry a leading tab + @ sigil so the pattern @for target in
# is unique to the recipe body (unlike bare `for target in` which also
# appears in smoke/test targets without the @).
TARGET_LINE="$(grep '@for target in' "$MAKEFILE" | head -1)"

if [ -z "$TARGET_LINE" ]; then
  emit_diagnostic \
    "preflight.invariant-supported-platforms" \
    "$MAKEFILE" \
    "Could not find 'for target in ...' loop in Makefile release-binaries recipe" \
    "Ensure the release-binaries recipe contains a 'for target in linux/amd64 linux/arm64 darwin/arm64' loop."
  exit 1
fi

# Strip everything up to and including "for target in ", then strip from ";" onward.
EXTRACTED="$(printf '%s' "$TARGET_LINE" | sed 's/.*for target in //; s/;.*//')"

# Sort both sides for comparison.
ACTUAL_SORTED="$(printf '%s\n' $EXTRACTED | sort | tr '\n' ' ' | sed 's/ $//')"
EXPECTED_SORTED_CMP="$(printf '%s\n' $EXPECTED_SORTED | sort | tr '\n' ' ' | sed 's/ $//')"

if [ "$ACTUAL_SORTED" = "$EXPECTED_SORTED_CMP" ]; then
  exit 0
fi

emit_diagnostic \
  "preflight.invariant-supported-platforms" \
  "$MAKEFILE" \
  "Makefile release-binaries target set [${ACTUAL_SORTED}] does not match declared supported platforms [${EXPECTED_SORTED_CMP}]" \
  "Update the Makefile release-binaries target list to match the declared supported platforms."

exit 1
