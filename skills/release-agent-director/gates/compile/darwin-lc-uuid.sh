#!/usr/bin/env bash
# gate:        compile (darwin LC_UUID)
# checks:      each darwin release binary (agent-director and
#              agent-director-admin) has an LC_UUID load command, which
#              macOS 26's dyld requires before it loads a binary (b.jh2).
#              Inspects the binaries cross-compile.sh just built, so run it
#              after that gate; it builds nothing it releases.
# usage:       bash darwin-lc-uuid.sh [--worktree-root <path>]
# env:         RELEASE_DIST_DIR — dir holding the release binaries, relative
#                   to the worktree root (default: dist); the same value
#                   cross-compile.sh was given.
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
      printf 'darwin-lc-uuid.sh: unknown argument: %s\n' "$1" >&2
      exit 2
      ;;
  esac
done

cd "$WORKTREE_ROOT" || { printf 'darwin-lc-uuid.sh: cannot cd into worktree root: %s\n' "$WORKTREE_ROOT" >&2; exit 2; }

DIST_DIR="${RELEASE_DIST_DIR:-dist}"
BINARIES=("${DIST_DIR}/agent-director-darwin-arm64" "${DIST_DIR}/agent-director-admin-darwin-arm64")
GATE_NAMES=("compile.darwin-arm64.lc-uuid" "compile.admin-darwin-arm64.lc-uuid")

_ms_since() {
  echo $(( ($(date +%s) - $1) * 1000 ))
}

# Build a sub-check JSON object.
#   $1 name  $2 outcome  $3 exit_code  $4 duration_ms  [$5 detail]
# detail is checker output, so it reaches jq on stdin (see gates/README.md).
_sub_check_json() {
  printf '%s' "${5:-}" | jq -cRs \
    --arg     name        "$1" \
    --arg     outcome     "$2" \
    --argjson exit_code   "$3" \
    --argjson duration_ms "$4" \
    '{name: $name, outcome: $outcome, exit_code: $exit_code, duration_ms: $duration_ms}
     + (if . == "" then {} else {detail: .} end)'
}

# ─── build the checker ────────────────────────────────────────────────────────
CHECKER_DIR="$(mktemp -d)"
trap 'rm -rf "$CHECKER_DIR"' EXIT
CHECKER="${CHECKER_DIR}/check-lcuuid"

overall_outcome="passed"
sub_check_jsons=()

t0=$(date +%s)
if ! build_out=$(go build -o "$CHECKER" ./tools/check-lcuuid 2>&1); then
  overall_outcome="failed"
  for i in "${!BINARIES[@]}"; do
    emit_diagnostic \
      "${GATE_NAMES[$i]}" \
      "tools/check-lcuuid" \
      "Could not build the LC_UUID checker: ${build_out}" \
      "Fix the build of tools/check-lcuuid, then rerun this gate."
    sub_check_jsons+=("$(_sub_check_json "${GATE_NAMES[$i]}" "failed" 1 "$(_ms_since "$t0")" "$build_out")")
  done
fi

# ─── per-binary results ───────────────────────────────────────────────────────
if [[ "$overall_outcome" == "passed" ]]; then
  for i in "${!BINARIES[@]}"; do
    binary="${BINARIES[$i]}"
    gate="${GATE_NAMES[$i]}"
    t0=$(date +%s)

    if [[ ! -f "$binary" ]]; then
      overall_outcome="failed"
      emit_diagnostic \
        "$gate" \
        "$binary" \
        "Binary not found: ${binary}" \
        "Run the cross-compile gate (make release-binaries) with the same RELEASE_DIST_DIR first."
      sub_check_jsons+=("$(_sub_check_json "$gate" "failed" 1 "$(_ms_since "$t0")" "file not found")")
      continue
    fi

    check_out=$("$CHECKER" "$binary" 2>&1)
    check_rc=$?
    case "$check_rc" in
      0)
        sub_check_jsons+=("$(_sub_check_json "$gate" "passed" 0 "$(_ms_since "$t0")" "$check_out")")
        ;;
      1)
        overall_outcome="failed"
        emit_diagnostic \
          "$gate" \
          "$binary" \
          "Darwin binary has no LC_UUID load command, so macOS 26's dyld refuses to load it: ${check_out}" \
          "Build the release binaries with Go 1.24 or later (the go.mod go line and test/sandbox/Dockerfile GO_VERSION), whose linker writes LC_UUID for darwin."
        sub_check_jsons+=("$(_sub_check_json "$gate" "failed" 1 "$(_ms_since "$t0")" "$check_out")")
        ;;
      *)
        overall_outcome="failed"
        emit_diagnostic \
          "$gate" \
          "$binary" \
          "Could not read the binary as a Mach-O file: ${check_out}" \
          "Verify make release-binaries builds a darwin/arm64 Mach-O executable at ${binary}."
        sub_check_jsons+=("$(_sub_check_json "$gate" "failed" "$check_rc" "$(_ms_since "$t0")" "$check_out")")
        ;;
    esac
  done
fi

# ─── consolidated JSON output ─────────────────────────────────────────────────
printf '%s\n' "${sub_check_jsons[@]}" | jq -s \
  --arg phase_name "compile"          \
  --arg outcome    "$overall_outcome" \
  '{phase_name: $phase_name, outcome: $outcome, sub_checks: .}'

if [[ "$overall_outcome" == "passed" ]]; then
  exit 0
else
  exit 1
fi
