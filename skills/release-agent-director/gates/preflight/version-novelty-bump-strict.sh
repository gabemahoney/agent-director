#!/usr/bin/env bash
# gates/preflight/version-novelty-bump-strict.sh — SR-14 gate: strict bump check.
#
# Computes the target version by applying BUMP_KIND to SOURCE_VERSION via
# bump-semver.sh, then verifies target > latest vX.Y.Z git tag.
#
# Usage:
#   SOURCE_VERSION=0.7.4 BUMP_KIND=patch version-novelty-bump-strict.sh
#   version-novelty-bump-strict.sh <source-version> <bump-kind>
#
# Exit 0 (pass) if computed target > latest tag.
# Exit 1 (fail) if computed target ≤ latest tag; emits SR-14 JSON to stderr.

set -euo pipefail

GATE_DIR="$(cd "$(dirname "$0")" && pwd)"
LIB_DIR="$GATE_DIR/../lib"

# shellcheck source=../lib/emit-diagnostic.sh
source "$LIB_DIR/emit-diagnostic.sh"
# shellcheck source=../lib/bump-semver.sh
source "$LIB_DIR/bump-semver.sh"

SOURCE_VERSION="${1:-${SOURCE_VERSION:-}}"
BUMP_KIND="${2:-${BUMP_KIND:-}}"

if [[ -z "$SOURCE_VERSION" || -z "$BUMP_KIND" ]]; then
  printf 'version-novelty-bump-strict: SOURCE_VERSION and BUMP_KIND must be set\n' >&2
  exit 1
fi

SOURCE_VERSION="${SOURCE_VERSION#v}"

# Compute target version.
TARGET="$(bump_semver "$SOURCE_VERSION" "$BUMP_KIND")" || {
  printf 'version-novelty-bump-strict: bump_semver failed\n' >&2
  exit 1
}

# Find the latest vX.Y.Z tag in the repo.
latest_tag="$(git tag --list 'v*.*.*' --sort=-v:refname | head -1)"

if [[ -z "$latest_tag" ]]; then
  # No tags yet — any bump is novel.
  exit 0
fi

latest_ver="${latest_tag#v}"

# Compare using sort -V: if target sorts <= latest, the bump is not strictly novel.
# `sort -V` sorts in version order; we check if target comes before or equal to latest.
lower="$(printf '%s\n%s\n' "$TARGET" "$latest_ver" | sort -V | head -1)"

if [[ "$lower" != "$TARGET" ]]; then
  # target > latest — gate passes (target sorts after latest).
  exit 0
fi

# target <= latest — gate fails.
emit_diagnostic \
  "preflight.version-novelty-bump-strict" \
  "v${TARGET}" \
  "Computed target v${TARGET} (${SOURCE_VERSION} + ${BUMP_KIND}) is not greater than latest tag ${latest_tag}." \
  "Ensure SOURCE_VERSION reflects the current unreleased version and BUMP_KIND is appropriate, or manually set TARGET_VERSION to a value greater than ${latest_tag}."
exit 1
