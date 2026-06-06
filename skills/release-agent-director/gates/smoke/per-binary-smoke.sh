#!/usr/bin/env bash
# gate:     smoke (per-binary)
# checks:   magic-bytes, static-linkage, host-exec — for each release binary
# usage:    bash per-binary-smoke.sh [<worktree-root>]
# pass:     consolidated JSON to stdout, exit 0
# fail:     SR-14 diagnostics to stderr, consolidated JSON to stdout, exit 1
# skipped:  sub-checks that cannot run on this host are marked "skipped" (not "failed")

set -uo pipefail

GATE_LIB="$(cd "$(dirname "$0")/../lib" && pwd)"
# shellcheck source=../lib/emit-diagnostic.sh
source "${GATE_LIB}/emit-diagnostic.sh"

# ─── argument parsing ─────────────────────────────────────────────────────────
WORKTREE_ROOT="${1:-.}"
cd "$WORKTREE_ROOT"

# ─── host triple detection ─────────────────────────────────────────────────────
HOST_OS="$(uname -s)"   # Linux | Darwin
HOST_ARCH="$(uname -m)" # x86_64 | aarch64 | arm64

# Normalise to Go/release naming conventions
case "$HOST_OS" in
  Linux)  HOST_OS_NORM="linux" ;;
  Darwin) HOST_OS_NORM="darwin" ;;
  *)      HOST_OS_NORM="$(echo "$HOST_OS" | tr '[:upper:]' '[:lower:]')" ;;
esac

case "$HOST_ARCH" in
  x86_64)          HOST_ARCH_NORM="amd64" ;;
  aarch64 | arm64) HOST_ARCH_NORM="arm64" ;;
  *)               HOST_ARCH_NORM="$HOST_ARCH" ;;
esac

HOST_TRIPLE="${HOST_OS_NORM}-${HOST_ARCH_NORM}"

# ─── binary table ─────────────────────────────────────────────────────────────
# Parallel arrays: PLATS[i], FILES[i], EXPECTED_MAGIC[i], IS_LINUX[i]
PLATS=("linux-amd64"  "linux-arm64"  "darwin-arm64")
FILES=("dist/agent-director-linux-amd64" "dist/agent-director-linux-arm64" "dist/agent-director-darwin-arm64")
MAGICS=("7f454c46"    "7f454c46"     "cffaedfe")
OS_FOR=("linux"       "linux"        "darwin")

# ─── helpers ──────────────────────────────────────────────────────────────────
_esc_json() {
  printf '%s' "$1" \
    | sed -e 's/\\/\\\\/g' \
          -e 's/"/\\"/g' \
          -e ':a;N;$!ba;s/\n/\\n/g'
}

_ms_since() {
  local start_s="$1"
  local end_s
  end_s=$(date +%s)
  echo $(( (end_s - start_s) * 1000 ))
}

# Build a sub-check JSON object.
#   $1 name  $2 outcome  $3 exit_code  $4 duration_ms  [$5 reason]  [$6 detail]
_sub_check_json() {
  local name="$1" outcome="$2" exit_code="$3" duration_ms="$4"
  local reason="${5:-}" detail="${6:-}"

  local json
  json=$(jq -n \
    --arg     name        "$name"     \
    --arg     outcome     "$outcome"  \
    --argjson exit_code   "$exit_code" \
    --argjson duration_ms "$duration_ms" \
    '{name: $name, outcome: $outcome, exit_code: $exit_code, duration_ms: $duration_ms}')

  if [[ -n "$reason" ]]; then
    json=$(printf '%s' "$json" | jq --arg r "$reason" '. + {reason: $r}')
  fi
  if [[ -n "$detail" ]]; then
    json=$(printf '%s' "$json" | jq --arg d "$detail" '. + {detail: $d}')
  fi
  printf '%s' "$json"
}

# ─── main loop ────────────────────────────────────────────────────────────────
overall_outcome="passed"
sub_check_jsons=()

for i in 0 1 2; do
  plat="${PLATS[$i]}"
  file="${FILES[$i]}"
  expected_magic="${MAGICS[$i]}"
  binary_os="${OS_FOR[$i]}"

  # ── 1. magic-bytes ──────────────────────────────────────────────────────────
  check_name="smoke.${plat}.magic-bytes"
  t0=$(date +%s)

  if [[ ! -f "$file" ]]; then
    overall_outcome="failed"
    emit_diagnostic \
      "$check_name" \
      "$file" \
      "Binary not found: ${file}" \
      "Run 'make release-binaries' to produce the artifact."
    sub_check_jsons+=("$(_sub_check_json "$check_name" "failed" 1 "$(_ms_since "$t0")" "" "file not found")")
  else
    observed_magic=$(od -A n -t x1 -N 4 "$file" | tr -d ' \n')
    if [[ "$observed_magic" == "$expected_magic" ]]; then
      sub_check_jsons+=("$(_sub_check_json "$check_name" "passed" 0 "$(_ms_since "$t0")")")
    else
      overall_outcome="failed"
      emit_diagnostic \
        "$check_name" \
        "$file" \
        "Unexpected magic bytes: got '${observed_magic}', expected '${expected_magic}' for ${plat}." \
        "Verify the build target emits a ${binary_os} binary for ${plat}."
      sub_check_jsons+=("$(_sub_check_json "$check_name" "failed" 1 "$(_ms_since "$t0")" "" "got=${observed_magic} expected=${expected_magic}")")
    fi
  fi

  # ── 2. static-linkage ───────────────────────────────────────────────────────
  check_name="smoke.${plat}.static-linkage"
  t0=$(date +%s)

  if [[ "$binary_os" == "darwin" ]]; then
    # Cannot run otool on a Linux host — skip.
    sub_check_jsons+=("$(_sub_check_json "$check_name" "skipped" 0 "$(_ms_since "$t0")" "host-cannot-introspect")")
  elif [[ ! -f "$file" ]]; then
    # Already failed magic-bytes; report as failed here too rather than skip.
    overall_outcome="failed"
    emit_diagnostic \
      "$check_name" \
      "$file" \
      "Binary not found: ${file}" \
      "Run 'make release-binaries' to produce the artifact."
    sub_check_jsons+=("$(_sub_check_json "$check_name" "failed" 1 "$(_ms_since "$t0")" "" "file not found")")
  else
    ldd_out=$(ldd "$file" 2>&1)
    if echo "$ldd_out" | grep -q "not a dynamic executable"; then
      sub_check_jsons+=("$(_sub_check_json "$check_name" "passed" 0 "$(_ms_since "$t0")")")
    else
      overall_outcome="failed"
      emit_diagnostic \
        "$check_name" \
        "$file" \
        "Binary is not statically linked: ldd output does not contain 'not a dynamic executable'." \
        "Ensure CGO_ENABLED=0 is set and only pure-Go dependencies are used."
      sub_check_jsons+=("$(_sub_check_json "$check_name" "failed" 1 "$(_ms_since "$t0")" "" "$(_esc_json "$ldd_out")")")
    fi
  fi

  # ── 3. host-exec ────────────────────────────────────────────────────────────
  check_name="smoke.${plat}.host-exec"
  t0=$(date +%s)

  if [[ "${plat}" != "${HOST_TRIPLE}" ]]; then
    sub_check_jsons+=("$(_sub_check_json "$check_name" "skipped" 0 "$(_ms_since "$t0")" "host-cannot-exec")")
  elif [[ ! -f "$file" ]]; then
    overall_outcome="failed"
    emit_diagnostic \
      "$check_name" \
      "$file" \
      "Binary not found: ${file}" \
      "Run 'make release-binaries' to produce the artifact."
    sub_check_jsons+=("$(_sub_check_json "$check_name" "failed" 1 "$(_ms_since "$t0")" "" "file not found")")
  else
    exec_out=$("$file" help 2>&1 | head -5)
    exec_rc=$?
    if [[ "$exec_rc" -eq 0 && -n "$exec_out" ]]; then
      sub_check_jsons+=("$(_sub_check_json "$check_name" "passed" 0 "$(_ms_since "$t0")")")
    else
      overall_outcome="failed"
      emit_diagnostic \
        "$check_name" \
        "$file" \
        "Host-exec check failed: '${file} help' exited ${exec_rc} or produced no output." \
        "Ensure the binary runs on this host and 'help' is a valid verb."
      sub_check_jsons+=("$(_sub_check_json "$check_name" "failed" "$exec_rc" "$(_ms_since "$t0")" "" "$(_esc_json "$exec_out")")")
    fi
  fi

done

# ─── consolidated JSON output ─────────────────────────────────────────────────
all_sub_checks=$(printf '%s\n' "${sub_check_jsons[@]}" | jq -sc '.')

jq -n \
  --arg     phase_name "smoke"          \
  --arg     outcome    "$overall_outcome" \
  --argjson sub_checks "$all_sub_checks"  \
  '{phase_name: $phase_name, outcome: $outcome, sub_checks: $sub_checks}'

if [[ "$overall_outcome" == "passed" ]]; then
  exit 0
else
  exit 1
fi
