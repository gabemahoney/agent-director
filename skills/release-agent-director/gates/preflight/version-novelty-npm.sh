#!/usr/bin/env bash
# gates/preflight/version-novelty-npm.sh — SR-14 gate: npm version novelty check.
#
# Fails if agent-director@<TARGET_VERSION> is already published on npm.
#
# Usage:
#   version-novelty-npm.sh <target-version>
#   TARGET_VERSION=0.8.0 version-novelty-npm.sh
#
# Exit 0 (pass) if the version is not on npm (E404) or unreachable (network error).
# Exit 1 (fail) if the version is already published; emits SR-14 JSON to stderr.

set -euo pipefail

GATE_DIR="$(cd "$(dirname "$0")" && pwd)"
LIB_DIR="$GATE_DIR/../lib"

# shellcheck source=../lib/emit-diagnostic.sh
source "$LIB_DIR/emit-diagnostic.sh"

TARGET_VERSION="${1:-${TARGET_VERSION:-}}"

if [[ -z "$TARGET_VERSION" ]]; then
  printf 'version-novelty-npm: TARGET_VERSION not set\n' >&2
  exit 1
fi

TARGET_VERSION="${TARGET_VERSION#v}"
PKG_SPEC="agent-director@${TARGET_VERSION}"

npm_stdout="$(mktemp)"
npm_stderr="$(mktemp)"
npm_rc=0
npm view "$PKG_SPEC" version >"$npm_stdout" 2>"$npm_stderr" || npm_rc=$?
npm_out="$(cat "$npm_stdout")"
npm_err="$(cat "$npm_stderr")"
rm -f "$npm_stdout" "$npm_stderr"

if [[ "$npm_rc" -eq 0 && -n "$npm_out" ]]; then
  # Version exists on npm — gate fails.
  emit_diagnostic \
    "preflight.version-novelty-npm" \
    "$PKG_SPEC" \
    "npm package agent-director@${TARGET_VERSION} is already published — this version cannot be re-published." \
    "Bump the version to a new semver value not present on npm, then re-run the release."
  exit 1
fi

# Exit non-zero from npm view.
if printf '%s\n' "$npm_err" | grep -q 'E404'; then
  # Package version not found on npm — gate passes.
  exit 0
fi

# Some other error (network failure, registry unreachable, etc.).
# Pass through with a diagnostic message but do not block the release.
printf 'version-novelty-npm: npm view returned unexpected error (rc=%d):\n%s\n' \
  "$npm_rc" "$npm_err" >&2
printf 'version-novelty-npm: treating as pass (cannot confirm npm publication status)\n' >&2
exit 0
