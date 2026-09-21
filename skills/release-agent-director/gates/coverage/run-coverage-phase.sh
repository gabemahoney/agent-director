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
# max_parallel = 5 (RATIONALE — MEASURED):
#   Measured under the t1.2mt.z4 protocol, re-run under b.3jn on 2026-09-20 after
#   the b.3jn gate-isolation fixes. Live sandbox sweep, 2 reps per candidate at
#   max_parallel = 5, 3, and 2: ALL SIX runs were all-green. Phase wall time was
#   61.2–66.0s at every candidate; the longest gate is coverage.go-root
#   (61.0–65.8s) in every run, giving a wall/longest ratio of ~1.004 everywhere —
#   SR-9 (wall <= longest * 1.10) is met at every candidate on an all-green
#   baseline. Because go-root dominates the phase, max_parallel buys essentially
#   nothing on wall time between 2 and 5; the value 5 (the gate count) is kept for
#   STABILITY and simplicity — no queueing of one gate behind another, at no
#   measured cost — not for speed.
#   coverage.docker-epics internally fans out its OWN run-parallel.sh with
#   max_parallel:4; that oversubscription was measured harmless on the 16-CPU host
#   (all-green at phase mp=5).
#   This value is only valid with the cross-gate isolation preconditions in place
#   (b.3jn): scratch HOME for the bun gates, the seeds/dist-pack flock protocol,
#   out-of-tree pack staging, and the child-scoped no-leak count — all documented
#   in gates/README.md "Coverage phase (parallel)".

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

# Without -e, jq failure is silent; an empty config would let --dry-run print a
# blank line and exit 0 (false success). Fail as a configuration error instead.
if [ -z "$CONFIG_JSON" ]; then
  echo "run-coverage-phase.sh: failed to build gates-config JSON (jq missing or failed)" >&2
  exit 2
fi

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
