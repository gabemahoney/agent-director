#!/usr/bin/env bash
# gate:        coherence.binary-version.<plat>
# checks:      each host-executable binary reports a version field matching target
# usage:       bash binary-version.sh [<target-version>]
#              $1 — expected version string; if omitted, derived from
#                   <RELEASE_PKG_DIR>/package.json
# env:         RELEASE_PKG_DIR — dir holding the canonical package.json used for
#                   version derivation (default: pkg/ts-bun-client), relative to
#                   the worktree root.
#              COHERENCE_DIST_DIR — dir holding the release binaries to inspect
#                   (default: dist), relative to the worktree root. Point both at
#                   isolated paths for concurrent test runs (b.aur).
# pass:        consolidated JSON to stdout, exit 0
# fail:        SR-14 diagnostics to stderr, consolidated JSON to stdout, exit 1

set -uo pipefail

GATE_LIB="$(cd "$(dirname "$0")/../lib" && pwd)"
# shellcheck source=../lib/emit-diagnostic.sh
source "${GATE_LIB}/emit-diagnostic.sh"

# ─── argument parsing ─────────────────────────────────────────────────────────
TARGET_VERSION=""
WORKTREE_ROOT="."

while [[ $# -gt 0 ]]; do
  case "$1" in
    --worktree-root)
      WORKTREE_ROOT="$2"
      shift 2
      ;;
    -*)
      printf 'binary-version.sh: unknown option: %s\n' "$1" >&2
      exit 2
      ;;
    *)
      TARGET_VERSION="$1"
      shift
      ;;
  esac
done

cd "$WORKTREE_ROOT" || { printf 'binary-version.sh: cannot cd into worktree root: %s\n' "$WORKTREE_ROOT" >&2; exit 2; }

# ─── derive target from package.json if not supplied ─────────────────────────
PKG_DIR="${RELEASE_PKG_DIR:-pkg/ts-bun-client}"
if [[ -z "$TARGET_VERSION" ]]; then
  TARGET_VERSION="$(jq -r .version "${PKG_DIR}/package.json")"
fi

# ─── host platform detection ─────────────────────────────────────────────────
HOST_OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
HOST_ARCH_RAW="$(uname -m)"
case "$HOST_ARCH_RAW" in
  x86_64)  HOST_ARCH="amd64" ;;
  aarch64|arm64) HOST_ARCH="arm64" ;;
  *) HOST_ARCH="$HOST_ARCH_RAW" ;;
esac

# Normalize Darwin → darwin
case "$HOST_OS" in
  darwin) HOST_OS="darwin" ;;
  linux)  HOST_OS="linux"  ;;
esac

HOST_PLAT="${HOST_OS}-${HOST_ARCH}"

# ─── binary directory ─────────────────────────────────────────────────────────
# Resolved relative to the worktree root (already cd'd). Defaults to dist/ for
# real release usage; tests set COHERENCE_DIST_DIR to an isolated path (b.aur).
DIST_DIR="${COHERENCE_DIST_DIR:-dist}"

# ─── platform table ───────────────────────────────────────────────────────────
PLATFORMS=("linux-amd64" "linux-arm64" "darwin-arm64")
BINARIES=(
  "${DIST_DIR}/agent-director-linux-amd64"
  "${DIST_DIR}/agent-director-linux-arm64"
  "${DIST_DIR}/agent-director-darwin-arm64"
)
GATE_NAMES=(
  "coherence.binary-version.linux-amd64"
  "coherence.binary-version.linux-arm64"
  "coherence.binary-version.darwin-arm64"
)

# ─── per-platform checks ──────────────────────────────────────────────────────
overall_outcome="passed"
sub_check_jsons=()

for i in 0 1 2; do
  plat="${PLATFORMS[$i]}"
  binary="${BINARIES[$i]}"
  gate="${GATE_NAMES[$i]}"
  start_ms=$(( $(date +%s) * 1000 ))

  # Non-host platforms: skip (cannot execute cross-arch binaries)
  if [[ "$plat" != "$HOST_PLAT" ]]; then
    end_ms=$(( $(date +%s) * 1000 ))
    duration_ms=$(( end_ms - start_ms ))
    sub_check_jsons+=("$(jq -n \
      --arg  name        "$gate"             \
      --arg  outcome     "skipped"           \
      --arg  reason      "host-cannot-exec"  \
      --argjson exit_code   0               \
      --argjson duration_ms "$duration_ms"  \
      '{name: $name, outcome: $outcome, reason: $reason, exit_code: $exit_code, duration_ms: $duration_ms}')")
    continue
  fi

  # Verify binary exists
  if [[ ! -f "$binary" ]]; then
    overall_outcome="failed"
    emit_diagnostic \
      "$gate" \
      "$binary" \
      "Binary not found: ${binary}. Cannot check version coherence." \
      "Run 'make release-binaries' to build the binary before running this gate."
    end_ms=$(( $(date +%s) * 1000 ))
    duration_ms=$(( end_ms - start_ms ))
    sub_check_jsons+=("$(jq -n \
      --arg  name        "$gate"    \
      --arg  outcome     "failed"   \
      --argjson exit_code   1       \
      --argjson duration_ms "$duration_ms" \
      '{name: $name, outcome: $outcome, exit_code: $exit_code, duration_ms: $duration_ms}')")
    continue
  fi

  # Run `<binary> version` and parse .version field
  version_json="$("$binary" version 2>/dev/null)" || true
  observed="$(printf '%s' "$version_json" | jq -r '.version // empty' 2>/dev/null)" || true

  end_ms=$(( $(date +%s) * 1000 ))
  duration_ms=$(( end_ms - start_ms ))

  if [[ "$observed" == "$TARGET_VERSION" ]]; then
    sub_check_jsons+=("$(jq -n \
      --arg  name        "$gate"   \
      --arg  outcome     "passed"  \
      --argjson exit_code   0      \
      --argjson duration_ms "$duration_ms" \
      '{name: $name, outcome: $outcome, exit_code: $exit_code, duration_ms: $duration_ms}')")
  else
    overall_outcome="failed"
    emit_diagnostic \
      "$gate" \
      "$binary" \
      "binary reports \`${observed}\`, expected \`${TARGET_VERSION}\`" \
      "Verify the Makefile's RELEASE_VERSION derivation is intact and the binary was rebuilt cleanly."
    sub_check_jsons+=("$(jq -n \
      --arg  name        "$gate"   \
      --arg  outcome     "failed"  \
      --argjson exit_code   1      \
      --argjson duration_ms "$duration_ms" \
      '{name: $name, outcome: $outcome, exit_code: $exit_code, duration_ms: $duration_ms}')")
  fi
done

# ─── consolidated JSON output ─────────────────────────────────────────────────
all_sub_checks=$(printf '%s\n' "${sub_check_jsons[@]}" | jq -sc '.')

jq -n \
  --arg     phase_name "coherence"         \
  --arg     outcome    "$overall_outcome"  \
  --argjson sub_checks "$all_sub_checks"   \
  '{phase_name: $phase_name, outcome: $outcome, sub_checks: $sub_checks}'

if [[ "$overall_outcome" == "passed" ]]; then
  exit 0
else
  exit 1
fi
