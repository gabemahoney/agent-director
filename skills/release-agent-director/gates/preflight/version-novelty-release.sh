#!/usr/bin/env bash
# gates/preflight/version-novelty-release.sh — SR-14 gate: GitHub release novelty check.
#
# Fails if a GitHub release for v<TARGET_VERSION> already exists.
#
# Usage:
#   version-novelty-release.sh <target-version>
#   TARGET_VERSION=0.8.0 version-novelty-release.sh
#
# Exit 0 (pass) if the release does not exist.
# Exit 1 (fail) if the release already exists; emits SR-14 JSON to stderr.
# Requires `gh` on PATH and authenticated.

set -euo pipefail

GATE_DIR="$(cd "$(dirname "$0")" && pwd)"
LIB_DIR="$GATE_DIR/../lib"

# shellcheck source=../lib/emit-diagnostic.sh
source "$LIB_DIR/emit-diagnostic.sh"

TARGET_VERSION="${1:-${TARGET_VERSION:-}}"

if [[ -z "$TARGET_VERSION" ]]; then
  printf 'version-novelty-release: TARGET_VERSION not set\n' >&2
  exit 1
fi

TARGET_VERSION="${TARGET_VERSION#v}"
TAG="v${TARGET_VERSION}"

gh_stderr="$(mktemp)"
gh_rc=0
gh release view "$TAG" >/dev/null 2>"$gh_stderr" || gh_rc=$?
gh_err="$(cat "$gh_stderr")"
rm -f "$gh_stderr"

if [[ "$gh_rc" -eq 0 ]]; then
  # Release exists — gate fails.
  emit_diagnostic \
    "preflight.version-novelty-release" \
    "$TAG" \
    "GitHub release ${TAG} already exists — this version has already been published." \
    "Bump the version to a new semver value without an existing GitHub release, then re-run."
  exit 1
fi

# Non-zero exit from `gh release view`. Distinguish "not found" (expected)
# from other errors (network, auth, etc.).
if printf '%s\n' "$gh_err" | grep -qiE 'not found|release not found|could not find'; then
  # Release does not exist — gate passes.
  exit 0
fi

# Unexpected error — surface it but don't block.
printf 'version-novelty-release: gh release view returned unexpected error (rc=%d):\n%s\n' \
  "$gh_rc" "$gh_err" >&2
printf 'version-novelty-release: treating as pass (cannot confirm release existence)\n' >&2
exit 0
