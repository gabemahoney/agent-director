#!/usr/bin/env bash
# gate:        compile (cross-platform)
# checks:      make release-binaries produces 3 cross-compiled binaries
# usage:       bash cross-compile.sh [--worktree-root <path>]
# pass:        consolidated JSON to stdout, exit 0
# fail:        SR-14 diagnostics to stderr, consolidated JSON to stdout, exit 1

set -uo pipefail

GATE_LIB="$(cd "$(dirname "$0")/../lib" && pwd)"
# shellcheck source=../lib/emit-diagnostic.sh
source "${GATE_LIB}/emit-diagnostic.sh"

# ─── argument parsing ─────────────────────────────────────────────────────────
WORKTREE_ROOT="."

while [[ $# -gt 0 ]]; do
  case "$1" in
    --worktree-root)
      WORKTREE_ROOT="$2"
      shift 2
      ;;
    *)
      printf 'cross-compile.sh: unknown argument: %s\n' "$1" >&2
      exit 2
      ;;
  esac
done

cd "$WORKTREE_ROOT"

# ─── version derivation ───────────────────────────────────────────────────────
# Honor caller override; otherwise derive from the canonical version source.
if [[ -z "${AGENT_DIRECTOR_BUILD_VERSION:-}" ]]; then
  PKG_JSON="pkg/ts-bun-client/package.json"
  if [[ ! -f "$PKG_JSON" ]]; then
    emit_diagnostic \
      "compile.version-derivation" \
      "$PKG_JSON" \
      "Canonical version file not found: ${PKG_JSON}" \
      "Ensure the worktree is complete and pkg/ts-bun-client/package.json exists."
    exit 1
  fi
  AGENT_DIRECTOR_BUILD_VERSION="$(jq -r .version "$PKG_JSON")"
  if [[ $? -ne 0 || -z "$AGENT_DIRECTOR_BUILD_VERSION" || "$AGENT_DIRECTOR_BUILD_VERSION" == "null" ]]; then
    emit_diagnostic \
      "compile.version-derivation" \
      "$PKG_JSON" \
      "Failed to parse .version from ${PKG_JSON}." \
      "Verify jq is installed and ${PKG_JSON} contains a valid .version field."
    exit 1
  fi
fi
export AGENT_DIRECTOR_BUILD_VERSION

# ─── targets ──────────────────────────────────────────────────────────────────
TARGETS=("linux/amd64" "linux/arm64" "darwin/arm64")
BINARIES=("dist/agent-director-linux-amd64" "dist/agent-director-linux-arm64" "dist/agent-director-darwin-arm64")
GATE_NAMES=("compile.linux-amd64" "compile.linux-arm64" "compile.darwin-arm64")

# ─── run make release-binaries ────────────────────────────────────────────────
MAKE_START_S=$(date +%s)

MAKE_OUTPUT_FILE="$(mktemp)"
trap 'rm -f "$MAKE_OUTPUT_FILE"' EXIT

make release-binaries 2>&1 | tee "$MAKE_OUTPUT_FILE"
MAKE_EXIT="${PIPESTATUS[0]}"

MAKE_END_S=$(date +%s)

# ─── portable mtime helper ────────────────────────────────────────────────────
# Returns modification time in seconds-since-epoch; falls back to MAKE_END_S.
_mtime() {
  local f="$1"
  stat -c %Y "$f" 2>/dev/null \
    || stat -f %m "$f" 2>/dev/null \
    || echo "$MAKE_END_S"
}

# ─── per-platform results ─────────────────────────────────────────────────────
overall_outcome="passed"
sub_check_jsons=()

for i in 0 1 2; do
  binary="${BINARIES[$i]}"
  gate="${GATE_NAMES[$i]}"
  target="${TARGETS[$i]}"

  # Binary is valid if the make run succeeded AND the file exists with non-zero size.
  bin_exit=0
  if [[ "$MAKE_EXIT" -ne 0 ]] || [[ ! -f "$binary" ]] || [[ ! -s "$binary" ]]; then
    bin_exit=1
  fi

  if [[ "$bin_exit" -ne 0 ]]; then
    overall_outcome="failed"
    emit_diagnostic \
      "$gate" \
      "$binary" \
      "Cross-compiled binary not produced for ${target}: ${binary} is missing or empty." \
      "Run 'make release-binaries' locally and verify GOOS=${target%/*} GOARCH=${target#*/} succeeds."
    gate_outcome="failed"
    duration_ms=$(( (MAKE_END_S - MAKE_START_S) * 1000 ))
  else
    gate_outcome="passed"
    bin_mtime=$(_mtime "$binary")
    duration_ms=$(( (bin_mtime - MAKE_START_S) * 1000 ))
    if [[ "$duration_ms" -lt 0 ]]; then
      duration_ms=$(( (MAKE_END_S - MAKE_START_S) * 1000 ))
    fi
  fi

  sub_check_jsons+=("$(jq -n \
    --arg     name        "$gate"         \
    --arg     outcome     "$gate_outcome" \
    --argjson exit_code   "$bin_exit"     \
    --argjson duration_ms "$duration_ms"  \
    '{name: $name, outcome: $outcome, exit_code: $exit_code, duration_ms: $duration_ms}')")
done

# ─── consolidated JSON output ─────────────────────────────────────────────────
all_sub_checks=$(printf '%s\n' "${sub_check_jsons[@]}" | jq -sc '.')

jq -n \
  --arg     phase_name "compile"          \
  --arg     outcome    "$overall_outcome" \
  --argjson sub_checks "$all_sub_checks"  \
  '{phase_name: $phase_name, outcome: $outcome, sub_checks: $sub_checks}'

if [[ "$overall_outcome" == "passed" ]]; then
  exit 0
else
  exit 1
fi
