#!/usr/bin/env bash
# gate:    compile.no-literal-version-constant
# checks:  internal/version/*.go contains no SemVer literal assigned to Version
#          (SR-7.2 — the ldflags-injectable default must stay as the dev sentinel)
# pass:    silent exit 0
# fail:    SR-14 JSON to stderr for each violation, exit 1

set -euo pipefail

GATE_LIB="$(cd "$(dirname "$0")/../lib" && pwd)"
# shellcheck source=../lib/emit-diagnostic.sh
source "${GATE_LIB}/emit-diagnostic.sh"

REPO_ROOT="$(git rev-parse --show-toplevel)"

# SemVer-like: starts with DIGIT.DIGIT.DIGIT
SEMVER_RE='^[0-9]+\.[0-9]+\.[0-9]+'

FAILED=0

while IFS= read -r gofile; do
  # Extract line number + literal for lines like:
  #   var Version = "..."
  #   const Version = "..."
  while IFS=: read -r lineno literal; do
    [ -z "$literal" ] && continue
    if printf '%s' "$literal" | grep -qE "$SEMVER_RE"; then
      emit_diagnostic \
        "compile.no-literal-version-constant" \
        "${gofile}:${lineno}" \
        "Version is set to a SemVer literal \"${literal}\" in ${gofile}:${lineno}; this overrides ldflags injection at runtime (SR-7.2)" \
        "Change the Version assignment back to the dev sentinel: var Version = \"dev\""
      FAILED=1
    fi
  done < <(grep -nE '^(var|const)[[:space:]]+Version[[:space:]]*=[[:space:]]*"' "$gofile" \
             | sed -E 's/^([0-9]+):.*"([^"]+)".*/\1:\2/')
done < <(find "${REPO_ROOT}/internal/version" -type f -name '*.go' ! -name '*_test.go')

[ "$FAILED" -eq 0 ] || exit 1
