#!/usr/bin/env bash
# gate:     pack.first
# checks:   tarball produced by `bun pm pack`; embedded package.json version
#           matches target version
# usage:    bash pack-first.sh [--worktree-root <path>] [--target-version <ver>]
# env:      PACK_OUTPUT_DIR — output dir for the tarball, relative to the
#           worktree root (default: dist). Set to an isolated path so
#           concurrent invocations never share an output directory.
#           RELEASE_PKG_DIR — dir holding the ts-bun-client package to pack and
#           to derive the target version from (default: pkg/ts-bun-client),
#           relative to the worktree root. It is an isolation hook: point it at
#           an isolated copy of pkg/ts-bun-client so a concurrent rewrite of the
#           real package.json cannot race this pack and produce an empty
#           embedded version (b.aur).
# pass:     <PACK_OUTPUT_DIR>/<tarball> created, exit 0
# fail:     SR-14 diagnostic to stderr, exit 1

set -uo pipefail

GATE_LIB="$(cd "$(dirname "$0")/../lib" && pwd)"
# shellcheck source=../lib/emit-diagnostic.sh
source "${GATE_LIB}/emit-diagnostic.sh"

# ─── argument parsing ─────────────────────────────────────────────────────────
WORKTREE_ROOT="."
TARGET_VERSION=""
OUTPUT_DIR="${PACK_OUTPUT_DIR:-dist}"
PKG_DIR="${RELEASE_PKG_DIR:-pkg/ts-bun-client}"

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
  TARGET_VERSION="$(jq -r .version "${PKG_DIR}/package.json")"
fi

# ─── staging directory (OUTSIDE the repo tree) ────────────────────────────────
# Staging MUST live outside the worktree: sibling gates (coverage.docker-epic-*)
# tar the repo root as the docker build context, and a pack-staging dir created
# here and removed in the EXIT trap would vanish mid-tar, failing the docker
# build with "file not found or excluded by .dockerignore" (b.3jn). Placing it
# under $TMPDIR removes that whole race class. `bun pm pack --destination` takes
# the absolute path fine (we already pass STAGING_ABS below), so nothing depends
# on repo-relative placement.
STAGING="$(mktemp -d "${TMPDIR:-/tmp}/pack-staging.XXXXXX")"
STAGING_ABS="$STAGING"
trap 'rm -rf "$STAGING"' EXIT

# ─── pack ─────────────────────────────────────────────────────────────────────
(cd "$PKG_DIR" && bun pm pack --destination "$STAGING_ABS") >/dev/null 2>&1

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

# ─── move tarball to output dir ───────────────────────────────────────────────
mkdir -p "$OUTPUT_DIR"
mv "$TARBALL" "${OUTPUT_DIR}/${TARBALL_NAME}"

# ─── embedded version assert ──────────────────────────────────────────────────
OBSERVED="$(tar -xzf "${OUTPUT_DIR}/${TARBALL_NAME}" --to-stdout package/package.json 2>/dev/null \
  | jq -r .version 2>/dev/null)"

if [[ "$OBSERVED" != "$TARGET_VERSION" ]]; then
  emit_diagnostic \
    "pack.first" \
    "${OUTPUT_DIR}/${TARBALL_NAME}" \
    "embedded package.json version is \`${OBSERVED}\`, expected \`${TARGET_VERSION}\`" \
    "Ensure the package.json version was bumped before packing. Run the version-bump step and retry."
  exit 1
fi

exit 0
