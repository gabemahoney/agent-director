#!/usr/bin/env bash
# gates/coverage/docker-epics.sh — coverage.docker-epic-<slug> gate enumerator
#
# Enumerates Docker harness EPIC slugs via `make list-test-docker-epics` and
# either emits a gates-config.json (--dry-run) or runs all coverage gates in
# parallel via run-parallel.sh.
#
# USAGE:
#   bash skills/release-agent-director/gates/coverage/docker-epics.sh [--dry-run]
#
# --dry-run: Emit the would-be gates-config.json to stdout and exit 0.
#            No `make test-docker` invocations occur.
#
# EXIT CODES:
#   0   — all gates passed (or --dry-run succeeded)
#   1   — one or more gates failed, or SR-19.3 empty-set blocker
#   2   — usage / configuration error

set -uo pipefail

# ─── resolve paths ────────────────────────────────────────────────────────────
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
GATE_LIB="${SCRIPT_DIR}/../lib"
RUN_PARALLEL="${GATE_LIB}/run-parallel.sh"

# shellcheck source=../lib/emit-diagnostic.sh
source "${GATE_LIB}/emit-diagnostic.sh"

# ─── flags ────────────────────────────────────────────────────────────────────
DRY_RUN=0
for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY_RUN=1 ;;
    *)
      printf 'usage: docker-epics.sh [--dry-run]\n' >&2
      exit 2
      ;;
  esac
done

# ─── enumerate slugs ──────────────────────────────────────────────────────────
SLUGS=$(make list-test-docker-epics 2>/dev/null) || {
  emit_diagnostic \
    "coverage.docker-epic-discovery" \
    "test/docker-epics.txt" \
    "make list-test-docker-epics failed — release blocker per SR-19.3" \
    "Verify test/docker-epics.txt is present and non-empty."
  exit 1
}

# Strip blank lines (defensive; make target already does this, but be safe).
SLUG_LIST=$(printf '%s\n' "$SLUGS" | grep -v '^[[:space:]]*$' || true)
SLUG_COUNT=$(printf '%s\n' "$SLUG_LIST" | grep -c . || true)

# ─── SR-19.3: empty-set is a release blocker ─────────────────────────────────
if [[ "$SLUG_COUNT" -eq 0 ]]; then
  emit_diagnostic \
    "coverage.docker-epic-discovery" \
    "test/docker-epics.txt" \
    "empty set returned by make list-test-docker-epics — release blocker per SR-19.3" \
    "Verify test/docker-epics.txt is present and non-empty."
  exit 1
fi

# ─── build gates-config.json ──────────────────────────────────────────────────
# One entry per slug: {"name":"coverage.docker-epic-<slug>","command":"make test-docker EPIC=<slug>","cwd":"."}
GATES_JSON=$(
  printf '%s\n' "$SLUG_LIST" | while IFS= read -r slug; do
    [[ -z "$slug" ]] && continue
    jq -n \
      --arg name "coverage.docker-epic-${slug}" \
      --arg cmd  "make test-docker EPIC=${slug}" \
      '{"name": $name, "command": $cmd, "cwd": "."}'
  done | jq -sc '.'
)

CONFIG_JSON=$(jq -n \
  --arg     phase_name   "coverage"    \
  --argjson gates        "$GATES_JSON" \
  --argjson max_parallel 4             \
  '{"phase_name": $phase_name, "gates": $gates, "max_parallel": $max_parallel}')

# ─── dry-run: emit config and exit ────────────────────────────────────────────
if [[ "$DRY_RUN" -eq 1 ]]; then
  printf '%s\n' "$CONFIG_JSON"
  exit 0
fi

# ─── live run: write config to temp file, delegate to run-parallel.sh ─────────
CONFIG_FILE=$(mktemp /tmp/docker-coverage-config.XXXXXX.json)
trap 'rm -f "$CONFIG_FILE"' EXIT

printf '%s\n' "$CONFIG_JSON" > "$CONFIG_FILE"

bash "$RUN_PARALLEL" "$CONFIG_FILE"
exit $?
