#!/usr/bin/env bash
# gate:     pack.second / byte-identical-normalized / sha256-manifest
# checks:   repacks the ts-bun-client tarball a second time and verifies the
#           two packs contain byte-identical file contents; then writes
#           dist/sha256sums covering the tarball + available platform binaries
# usage:    bash repack-and-verify.sh --first <path-to-first-tarball> [--worktree-root <path>] [--target-version <ver>]
# pass:     dist/sha256sums written, exit 0
# fail:     SR-14 diagnostic to stderr, exit 1

set -uo pipefail

GATE_LIB="$(cd "$(dirname "$0")/../lib" && pwd)"
# shellcheck source=../lib/emit-diagnostic.sh
source "${GATE_LIB}/emit-diagnostic.sh"

# ─── argument parsing ─────────────────────────────────────────────────────────
FIRST_TARBALL=""
WORKTREE_ROOT="."
TARGET_VERSION=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --first)
      FIRST_TARBALL="$2"
      shift 2
      ;;
    --worktree-root)
      WORKTREE_ROOT="$2"
      shift 2
      ;;
    --target-version)
      TARGET_VERSION="$2"
      shift 2
      ;;
    *)
      printf 'repack-and-verify.sh: unknown option: %s\n' "$1" >&2
      exit 2
      ;;
  esac
done

if [[ -z "$FIRST_TARBALL" ]]; then
  printf 'repack-and-verify.sh: --first <tarball> is required\n' >&2
  exit 2
fi

cd "$WORKTREE_ROOT"

# Resolve first tarball to absolute path (was possibly relative before cd)
if [[ "$FIRST_TARBALL" != /* ]]; then
  FIRST_TARBALL="$(pwd)/$FIRST_TARBALL"
fi

if [[ ! -f "$FIRST_TARBALL" ]]; then
  emit_diagnostic \
    "pack.byte-identical-normalized" \
    "$FIRST_TARBALL" \
    "first tarball does not exist: ${FIRST_TARBALL}" \
    "Run pack-first.sh before repack-and-verify.sh."
  exit 1
fi

# ─── pack second time ─────────────────────────────────────────────────────────
STAGING2="$(mktemp -d -p . pack-staging2.XXXXXX)"
trap 'rm -rf "$STAGING2" "${EXTRACT_DIR1:-}" "${EXTRACT_DIR2:-}"' EXIT

(cd pkg/ts-bun-client && bun pm pack --destination "../../$STAGING2") >/dev/null 2>&1

SECOND_TARBALL="$(ls "$STAGING2"/*.tgz 2>/dev/null | head -1)"
if [[ -z "$SECOND_TARBALL" ]]; then
  emit_diagnostic \
    "pack.byte-identical-normalized" \
    "null" \
    "second bun pm pack produced no .tgz file in staging directory" \
    "Run 'cd pkg/ts-bun-client && bun pm pack' manually to diagnose the failure."
  exit 1
fi

# ─── extract both tarballs and diff contents ──────────────────────────────────
EXTRACT_DIR1="$(mktemp -d)"
EXTRACT_DIR2="$(mktemp -d)"

tar -xzf "$FIRST_TARBALL"  -C "$EXTRACT_DIR1"
tar -xzf "$SECOND_TARBALL" -C "$EXTRACT_DIR2"

DIFF_OUTPUT="$(diff -rq "$EXTRACT_DIR1" "$EXTRACT_DIR2" 2>&1)"
if [[ -n "$DIFF_OUTPUT" ]]; then
  # Surface the first differing file for the diagnostic
  FIRST_DIFF="$(printf '%s' "$DIFF_OUTPUT" | head -1)"
  emit_diagnostic \
    "pack.byte-identical-normalized" \
    "$FIRST_TARBALL" \
    "second pack differs from first pack: ${FIRST_DIFF}" \
    "Ensure bun pm pack is deterministic. Check for timestamp injection or platform-specific artefacts in pkg/ts-bun-client."
  exit 1
fi

# ─── sha256 manifest ──────────────────────────────────────────────────────────
mkdir -p dist

MANIFEST_FILES=()

# Tarball (required — we just verified it)
MANIFEST_FILES+=("$FIRST_TARBALL")

# Platform binaries — include whatever is present, warn on missing
BINARIES=(
  dist/agent-director-linux-amd64
  dist/agent-director-linux-arm64
  dist/agent-director-darwin-arm64
)

for BIN in "${BINARIES[@]}"; do
  if [[ -f "$BIN" ]]; then
    MANIFEST_FILES+=("$BIN")
  else
    printf 'repack-and-verify.sh: warning: binary not found, skipping from manifest: %s\n' "$BIN" >&2
  fi
done

sha256sum "${MANIFEST_FILES[@]}" > dist/sha256sums

printf 'repack-and-verify.sh: sha256sums written to dist/sha256sums (%d entries)\n' "${#MANIFEST_FILES[@]}" >&2

exit 0
