#!/usr/bin/env bash
# gates/coverage/run-coverage-phase.sh — coverage-phase runner (NOT a gate)
#
# Drives the five coverage gates concurrently through the unchanged
# lib/run-parallel.sh executor. This is phase orchestration, not a sixth gate:
# the five gates below are the coverage phase's checks; this script only builds
# their gates-config.json and delegates to the executor.
#
# USAGE (from the repo root):
#   bash skills/release-agent-director/gates/coverage/run-coverage-phase.sh [--dry-run]
#
# --dry-run: Emit the would-be gates-config.json to stdout and exit 0.
#            No gate is executed.
#
# EXIT CODES (propagated unchanged from run-parallel.sh on a live run):
#   0   — all gates passed (or --dry-run succeeded)
#   1   — one or more gates failed
#   2   — usage / configuration error
#
# max_parallel = 5 (RATIONALE):
#   The sandbox (`make sandbox` / `make test-sandbox`) runs `docker run` with no
#   --cpus / --memory limits, so the container inherits the full host: 16 CPUs
#   and ~700 GiB RAM. Memory is a non-constraint; the only real concern is CPU
#   oversubscription. Setting max_parallel to the gate count (5) lets all five
#   coverage gates start at once, so the phase's wall-time is bound by its single
#   longest gate — the point of parallelizing. The dominant compounding risk is
#   coverage.docker-epics, which internally fans out its OWN run-parallel.sh with
#   max_parallel:4; combined with the four sibling coverage gates the theoretical
#   peak concurrent CPU demand (docker-epics' up-to-4 children + go-root's
#   `-race` run + the two bun gates + consumer-dryrun) still time-slices
#   gracefully on 16 cores — gates slow under contention but do not error, so no
#   gate passes or fails from resource starvation without a diagnosable error. A
#   lower cap would needlessly queue one coverage gate behind another for no
#   memory benefit. Downstream Epic t1.2mt.z4 owns the binding wall-time
#   measurement and may revise this value.

set -uo pipefail

# ─── resolve paths ────────────────────────────────────────────────────────────
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
GATE_LIB="${SCRIPT_DIR}/../lib"
RUN_PARALLEL="${GATE_LIB}/run-parallel.sh"

# ─── flags ────────────────────────────────────────────────────────────────────
DRY_RUN=0
for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY_RUN=1 ;;
    *)
      printf 'usage: run-coverage-phase.sh [--dry-run]\n' >&2
      exit 2
      ;;
  esac
done

# ─── build gates-config.json ──────────────────────────────────────────────────
# Five coverage gates, invoked BARE (no worktree-root argument, no flags) by
# repo-root-relative path; the runner is documented as invoked from the repo
# root, so every cwd is ".".
CONFIG_JSON=$(jq -n \
  --arg     phase_name   "coverage" \
  --argjson max_parallel 5          \
  '{
    "phase_name": $phase_name,
    "gates": [
      {"name": "coverage.go-root",           "command": "bash skills/release-agent-director/gates/coverage/go-root.sh",           "cwd": "."},
      {"name": "coverage.bun-test",          "command": "bash skills/release-agent-director/gates/coverage/bun-test.sh",          "cwd": "."},
      {"name": "coverage.docker-epics",      "command": "bash skills/release-agent-director/gates/coverage/docker-epics.sh",      "cwd": "."},
      {"name": "coverage.bun-extra-scripts", "command": "bash skills/release-agent-director/gates/coverage/bun-extra-scripts.sh", "cwd": "."},
      {"name": "coverage.go-consumer-dryrun","command": "bash skills/release-agent-director/gates/coverage/go-consumer-dryrun.sh","cwd": "."}
    ],
    "max_parallel": $max_parallel
  }')

# ─── dry-run: emit config and exit ────────────────────────────────────────────
if [[ "$DRY_RUN" -eq 1 ]]; then
  printf '%s\n' "$CONFIG_JSON"
  exit 0
fi

# ─── live run: write config to temp file, delegate to run-parallel.sh ─────────
CONFIG_FILE=$(mktemp /tmp/coverage-phase-config.XXXXXX.json)
trap 'rm -f "$CONFIG_FILE"' EXIT

printf '%s\n' "$CONFIG_JSON" > "$CONFIG_FILE"

bash "$RUN_PARALLEL" "$CONFIG_FILE"
exit $?
