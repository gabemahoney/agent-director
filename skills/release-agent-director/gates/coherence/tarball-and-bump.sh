#!/usr/bin/env bash
# gate:     coherence.tarball-version + coherence.bump-commit-integrity
# checks:   (1) embedded package.json in the packed tarball matches target
#           (2) pkg/ts-bun-client/package.json on the release branch matches target
# usage:    bash tarball-and-bump.sh --tarball <path> --target <version> [--worktree-root <path>]
# pass:     consolidated JSON to stdout, exit 0
# fail:     SR-14 diagnostics to stderr, consolidated JSON to stdout, exit 1

set -uo pipefail

GATE_LIB="$(cd "$(dirname "$0")/../lib" && pwd)"
# shellcheck source=../lib/emit-diagnostic.sh
source "${GATE_LIB}/emit-diagnostic.sh"

# ─── argument parsing ─────────────────────────────────────────────────────────
TARBALL=""
TARGET_VERSION=""
WORKTREE_ROOT="."

while [[ $# -gt 0 ]]; do
  case "$1" in
    --tarball)
      TARBALL="$2"
      shift 2
      ;;
    --target)
      TARGET_VERSION="$2"
      shift 2
      ;;
    --worktree-root)
      WORKTREE_ROOT="$2"
      shift 2
      ;;
    *)
      printf 'tarball-and-bump.sh: unknown option: %s\n' "$1" >&2
      exit 2
      ;;
  esac
done

if [[ -z "$TARBALL" ]]; then
  printf 'tarball-and-bump.sh: --tarball is required\n' >&2
  exit 2
fi

if [[ -z "$TARGET_VERSION" ]]; then
  printf 'tarball-and-bump.sh: --target is required\n' >&2
  exit 2
fi

# ─── sub-check helpers ────────────────────────────────────────────────────────
overall_outcome="passed"
sub_check_jsons=()

# ─── sub-check 1: coherence.tarball-version ──────────────────────────────────
gate_tv="coherence.tarball-version"
start_ms=$(( $(date +%s) * 1000 ))

if [[ ! -f "$TARBALL" ]]; then
  overall_outcome="failed"
  emit_diagnostic \
    "$gate_tv" \
    "$TARBALL" \
    "Tarball not found: ${TARBALL}. Cannot verify embedded package.json version." \
    "Run the pack gate first to produce the tarball before running coherence checks."
  end_ms=$(( $(date +%s) * 1000 ))
  duration_ms=$(( end_ms - start_ms ))
  sub_check_jsons+=("$(jq -n \
    --arg  name        "$gate_tv"  \
    --arg  outcome     "failed"    \
    --argjson exit_code   1        \
    --argjson duration_ms "$duration_ms" \
    '{name: $name, outcome: $outcome, exit_code: $exit_code, duration_ms: $duration_ms}')")
else
  observed_tv="$(tar -xzf "$TARBALL" --to-stdout package/package.json 2>/dev/null \
    | jq -r .version 2>/dev/null)" || observed_tv=""

  end_ms=$(( $(date +%s) * 1000 ))
  duration_ms=$(( end_ms - start_ms ))

  if [[ "$observed_tv" == "$TARGET_VERSION" ]]; then
    sub_check_jsons+=("$(jq -n \
      --arg  name        "$gate_tv"  \
      --arg  outcome     "passed"    \
      --argjson exit_code   0        \
      --argjson duration_ms "$duration_ms" \
      '{name: $name, outcome: $outcome, exit_code: $exit_code, duration_ms: $duration_ms}')")
  else
    overall_outcome="failed"
    emit_diagnostic \
      "$gate_tv" \
      "$TARBALL" \
      "tarball package.json version is \`${observed_tv}\`, expected \`${TARGET_VERSION}\`" \
      "Ensure the tarball was built after the version bump. Re-run the pack gate and retry."
    sub_check_jsons+=("$(jq -n \
      --arg  name        "$gate_tv"  \
      --arg  outcome     "failed"    \
      --argjson exit_code   1        \
      --argjson duration_ms "$duration_ms" \
      '{name: $name, outcome: $outcome, exit_code: $exit_code, duration_ms: $duration_ms}')")
  fi
fi

# ─── sub-check 2: coherence.bump-commit-integrity ────────────────────────────
gate_bc="coherence.bump-commit-integrity"
start_ms=$(( $(date +%s) * 1000 ))

PKG_JSON="${WORKTREE_ROOT}/pkg/ts-bun-client/package.json"

if [[ ! -f "$PKG_JSON" ]]; then
  overall_outcome="failed"
  emit_diagnostic \
    "$gate_bc" \
    "$PKG_JSON" \
    "pkg/ts-bun-client/package.json not found at worktree root \`${WORKTREE_ROOT}\`." \
    "Verify --worktree-root points to the release branch worktree."
  end_ms=$(( $(date +%s) * 1000 ))
  duration_ms=$(( end_ms - start_ms ))
  sub_check_jsons+=("$(jq -n \
    --arg  name        "$gate_bc"  \
    --arg  outcome     "failed"    \
    --argjson exit_code   1        \
    --argjson duration_ms "$duration_ms" \
    '{name: $name, outcome: $outcome, exit_code: $exit_code, duration_ms: $duration_ms}')")
else
  observed_bc="$(jq -r .version "$PKG_JSON" 2>/dev/null)" || observed_bc=""

  end_ms=$(( $(date +%s) * 1000 ))
  duration_ms=$(( end_ms - start_ms ))

  if [[ "$observed_bc" == "$TARGET_VERSION" ]]; then
    sub_check_jsons+=("$(jq -n \
      --arg  name        "$gate_bc"  \
      --arg  outcome     "passed"    \
      --argjson exit_code   0        \
      --argjson duration_ms "$duration_ms" \
      '{name: $name, outcome: $outcome, exit_code: $exit_code, duration_ms: $duration_ms}')")
  else
    overall_outcome="failed"
    emit_diagnostic \
      "$gate_bc" \
      "$PKG_JSON" \
      "pkg/ts-bun-client/package.json version is \`${observed_bc}\`, expected \`${TARGET_VERSION}\`" \
      "The bump commit may have been amended or reverted. Re-run the version-bump step and verify the commit is intact."
    sub_check_jsons+=("$(jq -n \
      --arg  name        "$gate_bc"  \
      --arg  outcome     "failed"    \
      --argjson exit_code   1        \
      --argjson duration_ms "$duration_ms" \
      '{name: $name, outcome: $outcome, exit_code: $exit_code, duration_ms: $duration_ms}')")
  fi
fi

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
