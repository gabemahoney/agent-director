#!/usr/bin/env bash
# gate:     pack.first
# checks:   tarball produced by `bun pm pack`; embedded package.json version
#           matches target version
# usage:    bash pack-first.sh [--worktree-root <path>] [--target-version <ver>]
# pass:     dist/<tarball> created, exit 0
# fail:     SR-14 diagnostic to stderr, exit 1

set -uo pipefail

GATE_LIB="$(cd "$(dirname "$0")/../lib" && pwd)"
# shellcheck source=../lib/emit-diagnostic.sh
source "${GATE_LIB}/emit-diagnostic.sh"

# ─── argument parsing ─────────────────────────────────────────────────────────
WORKTREE_ROOT="."
TARGET_VERSION=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --worktree-root)
      WORKTREE_ROOT="$2"
      shift 2
      ;;
    --target-version)
      TARGET_VERSION="$2"
      shift 2
      ;;
    *)
      printf 'pack-first.sh: unknown option: %s\n' "$1" >&2
      exit 2
      ;;
  esac
done

cd "$WORKTREE_ROOT"

# ─── derive target version from package.json if not supplied ──────────────────
if [[ -z "$TARGET_VERSION" ]]; then
  TARGET_VERSION="$(jq -r .version pkg/ts-bun-client/package.json)"
fi

# ─── staging directory (inside repo so bun can resolve paths) ─────────────────
STAGING="$(mktemp -d -p . pack-staging.XXXXXX)"
trap 'rm -rf "$STAGING"' EXIT

# ─── pack ─────────────────────────────────────────────────────────────────────
(cd pkg/ts-bun-client && bun pm pack --destination "../../$STAGING") >/dev/null 2>&1

# ─── detect produced tarball ──────────────────────────────────────────────────
TARBALL="$(ls "$STAGING"/*.tgz 2>/dev/null | head -1)"
if [[ -z "$TARBALL" ]]; then
  emit_diagnostic \
    "pack.first" \
    "null" \
    "bun pm pack produced no .tgz file in staging directory" \
    "Run 'cd pkg/ts-bun-client && bun pm pack' manually to diagnose the failure."
  exit 1
fi

TARBALL_NAME="$(basename "$TARBALL")"

# ─── move tarball to dist/ ────────────────────────────────────────────────────
mkdir -p dist
mv "$TARBALL" "dist/${TARBALL_NAME}"

# ─── embedded version assert ──────────────────────────────────────────────────
OBSERVED="$(tar -xzf "dist/${TARBALL_NAME}" --to-stdout package/package.json 2>/dev/null \
  | jq -r .version 2>/dev/null)"

if [[ "$OBSERVED" != "$TARGET_VERSION" ]]; then
  emit_diagnostic \
    "pack.first" \
    "dist/${TARBALL_NAME}" \
    "embedded package.json version is \`${OBSERVED}\`, expected \`${TARGET_VERSION}\`" \
    "Ensure the package.json version was bumped before packing. Run the version-bump step and retry."
  exit 1
fi

exit 0
