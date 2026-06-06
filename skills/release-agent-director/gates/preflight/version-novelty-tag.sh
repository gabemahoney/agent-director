#!/usr/bin/env bash
# gates/preflight/version-novelty-tag.sh — SR-14 gate: tag novelty check.
#
# Fails if a git tag v<TARGET_VERSION> already exists on origin.
#
# Usage:
#   version-novelty-tag.sh <target-version>
#   TARGET_VERSION=0.8.0 version-novelty-tag.sh
#
# Exit 0 (pass) if the tag does not exist on origin.
# Exit 1 (fail) if the tag already exists; emits SR-14 JSON to stderr.

set -euo pipefail

GATE_DIR="$(cd "$(dirname "$0")" && pwd)"
LIB_DIR="$GATE_DIR/../lib"

# shellcheck source=../lib/emit-diagnostic.sh
source "$LIB_DIR/emit-diagnostic.sh"

TARGET_VERSION="${1:-${TARGET_VERSION:-}}"

if [[ -z "$TARGET_VERSION" ]]; then
  printf 'version-novelty-tag: TARGET_VERSION not set\n' >&2
  exit 1
fi

# Strip leading 'v' for consistency, then re-add for tag name.
TARGET_VERSION="${TARGET_VERSION#v}"
TAG="v${TARGET_VERSION}"

remote_out="$(git ls-remote --tags origin "$TAG" 2>&1)" || {
  printf 'version-novelty-tag: git ls-remote failed: %s\n' "$remote_out" >&2
  exit 1
}

if [[ -n "$remote_out" ]]; then
  emit_diagnostic \
    "preflight.version-novelty-tag" \
    "$TAG" \
    "Git tag ${TAG} already exists on origin — this version has already been tagged." \
    "Bump the version to a new semver value that has not been tagged, then re-run the release."
  exit 1
fi

# Tag does not exist — gate passes.
exit 0
