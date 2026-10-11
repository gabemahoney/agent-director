#!/usr/bin/env bash
# gate:     smoke (per-binary)
# checks:   magic-bytes, static-linkage, host-exec — for each release binary:
#           agent-director and agent-director-admin (b.vqr) per target. The
#           admin binary's host-exec check also asserts its help opens with the
#           human-approval statement.
# usage:    bash per-binary-smoke.sh [<worktree-root>]
# env:      SMOKE_DIST_DIR — directory holding the release binaries, relative to
#           the worktree root (default: dist). Point it at an isolated path
#           (matching the RELEASE_DIST_DIR passed to `make release-binaries`)
#           so concurrent invocations never share a binary directory (b.aur).
# pass:     consolidated JSON to stdout, exit 0
# fail:     SR-14 diagnostics to stderr, consolidated JSON to stdout, exit 1
# skipped:  sub-checks that cannot run on this host are marked "skipped" (not "failed")

set -uo pipefail

GATE_LIB="$(cd "$(dirname "$0")/../lib" && pwd)"
# shellcheck source=../lib/emit-diagnostic.sh
source "${GATE_LIB}/emit-diagnostic.sh"

# ─── argument parsing ─────────────────────────────────────────────────────────
WORKTREE_ROOT="${1:-.}"
cd "$WORKTREE_ROOT" || { printf 'per-binary-smoke.sh: cannot cd into worktree root: %s\n' "$WORKTREE_ROOT" >&2; exit 2; }

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

# ─── binary directory ─────────────────────────────────────────────────────────
# Resolved relative to the worktree root (we have already cd'd there). Defaults
# to dist/ for real release usage; tests set SMOKE_DIST_DIR to an isolated path.
DIST_DIR="${SMOKE_DIST_DIR:-dist}"

# ─── binary table ─────────────────────────────────────────────────────────────
# Parallel arrays: PLATS[i] (the check-name label), TRIPLES[i] (the binary's
# platform), FILES[i], MAGICS[i], OS_FOR[i], ADMIN[i] (1 for agent-director-admin)
PLATS=("linux-amd64"  "linux-arm64"  "darwin-arm64"
       "admin-linux-amd64" "admin-linux-arm64" "admin-darwin-arm64")
TRIPLES=("linux-amd64" "linux-arm64" "darwin-arm64"
         "linux-amd64" "linux-arm64" "darwin-arm64")
FILES=("${DIST_DIR}/agent-director-linux-amd64" "${DIST_DIR}/agent-director-linux-arm64" "${DIST_DIR}/agent-director-darwin-arm64"
       "${DIST_DIR}/agent-director-admin-linux-amd64" "${DIST_DIR}/agent-director-admin-linux-arm64" "${DIST_DIR}/agent-director-admin-darwin-arm64")
MAGICS=("7f454c46"    "7f454c46"     "cffaedfe"
        "7f454c46"    "7f454c46"     "cffaedfe")
OS_FOR=("linux"       "linux"        "darwin"
        "linux"       "linux"        "darwin")
ADMIN=(0 0 0 1 1 1)

# ADMIN_HELP_FIRST_LINE is the human-approval statement every help of
# agent-director-admin opens with (internal/adminapi.ApprovalStatement).
ADMIN_HELP_FIRST_LINE="agent-director-admin is an operator tool. Do not run any of its commands without explicit approval from a human for this specific run. Agents and automated callers must not run it."

# ─── helpers ──────────────────────────────────────────────────────────────────
_ms_since() {
  local start_s="$1"
  local end_s
  end_s=$(date +%s)
  echo $(( (end_s - start_s) * 1000 ))
}

# Build a sub-check JSON object.
#   $1 name  $2 outcome  $3 exit_code  $4 duration_ms  [$5 reason]  [$6 detail]
# reason and detail are added only when non-empty.
#
# detail is passed raw (ldd or `help` output, unescaped): jq does the one and
# only JSON escaping, so the decoded field equals the raw output (b.nsa). It
# reaches jq on stdin, not as an argument, because it is command output and
# Linux caps one argument at 128 KiB; -Rs keeps it unchanged.
_sub_check_json() {
  local name="$1" outcome="$2" exit_code="$3" duration_ms="$4"
  local reason="${5:-}" detail="${6:-}"

  printf '%s' "$detail" | jq -cRs \
    --arg     name        "$name"     \
    --arg     outcome     "$outcome"  \
    --argjson exit_code   "$exit_code" \
    --argjson duration_ms "$duration_ms" \
    --arg     reason      "$reason"   \
    '{name: $name, outcome: $outcome, exit_code: $exit_code, duration_ms: $duration_ms}
     + (if $reason == "" then {} else {reason: $reason} end)
     + (if . == "" then {} else {detail: .} end)'
}

# ─── main loop ────────────────────────────────────────────────────────────────
overall_outcome="passed"
sub_check_jsons=()

for i in "${!FILES[@]}"; do
  plat="${PLATS[$i]}"
  triple="${TRIPLES[$i]}"
  file="${FILES[$i]}"
  expected_magic="${MAGICS[$i]}"
  binary_os="${OS_FOR[$i]}"
  is_admin="${ADMIN[$i]}"

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
      sub_check_jsons+=("$(_sub_check_json "$check_name" "failed" 1 "$(_ms_since "$t0")" "" "$ldd_out")")
    fi
  fi

  # ── 3. host-exec ────────────────────────────────────────────────────────────
  check_name="smoke.${plat}.host-exec"
  t0=$(date +%s)

  if [[ "${triple}" != "${HOST_TRIPLE}" ]]; then
    sub_check_jsons+=("$(_sub_check_json "$check_name" "skipped" 0 "$(_ms_since "$t0")" "host-cannot-exec")")
  elif [[ ! -f "$file" ]]; then
    overall_outcome="failed"
    emit_diagnostic \
      "$check_name" \
      "$file" \
      "Binary not found: ${file}" \
      "Run 'make release-binaries' to produce the artifact."
    sub_check_jsons+=("$(_sub_check_json "$check_name" "failed" 1 "$(_ms_since "$t0")" "" "file not found")")
  elif [[ "$is_admin" -eq 1 ]]; then
    # Capture the whole help, then its first line: no pipe into head, so a
    # SIGPIPE cannot turn a good run into a failure under pipefail.
    exec_out=$("$file" help 2>&1)
    exec_rc=$?
    exec_out="${exec_out%%$'\n'*}"
    if [[ "$exec_rc" -eq 0 && "$exec_out" == "$ADMIN_HELP_FIRST_LINE" ]]; then
      sub_check_jsons+=("$(_sub_check_json "$check_name" "passed" 0 "$(_ms_since "$t0")")")
    else
      overall_outcome="failed"
      emit_diagnostic \
        "$check_name" \
        "$file" \
        "Host-exec check failed: '${file} help' exited ${exec_rc} or does not open with the human-approval statement." \
        "Ensure the binary runs on this host and every help it prints opens with internal/adminapi.ApprovalStatement."
      sub_check_jsons+=("$(_sub_check_json "$check_name" "failed" "$exec_rc" "$(_ms_since "$t0")" "" "$exec_out")")
    fi
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
      sub_check_jsons+=("$(_sub_check_json "$check_name" "failed" "$exec_rc" "$(_ms_since "$t0")" "" "$exec_out")")
    fi
  fi

done

# ─── consolidated JSON output ─────────────────────────────────────────────────
# The sub-checks carry raw command output in detail, so they reach jq on stdin,
# not as a --argjson that Linux would cap at 128 KiB (b.nsa).
printf '%s\n' "${sub_check_jsons[@]}" | jq -s \
  --arg     phase_name "smoke"          \
  --arg     outcome    "$overall_outcome" \
  '{phase_name: $phase_name, outcome: $outcome, sub_checks: .}'

if [[ "$overall_outcome" == "passed" ]]; then
  exit 0
else
  exit 1
fi
