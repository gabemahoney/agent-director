#!/usr/bin/env bash
# gates/coverage/docker-epics.sh — coverage.docker-epic-<slug> gate enumerator
#
# Enumerates Docker harness EPIC slugs via `make list-test-docker-epics` and
# either emits a gates-config.json (--dry-run) or builds bin/ once under the
# exclusive tree-write lock and then runs all coverage gates in parallel via
# run-parallel.sh.
#
# USAGE:
#   bash skills/release-agent-director/gates/coverage/docker-epics.sh [--dry-run]
#
# --dry-run: Emit the would-be gates-config.json to stdout and exit 0.
#            No `make build` or `make test-docker` invocations occur.
#
# EXIT CODES:
#   0   — all gates passed (or --dry-run succeeded)
#   1   — one or more gates failed, the bin/ pre-build failed, or SR-19.3
#         empty-set blocker
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
# One entry per slug:
#   {"name":"coverage.docker-epic-<slug>","command":"flock -s .tree-write.lock make test-docker EPIC=<slug>","cwd":"."}
#
# Each child collects the whole repo root as its docker build context, so it
# holds the repo-root tree-write lock (b.k42) SHARED for its whole run.
# Rule: tree writers take it exclusive; context collectors take it shared (-s).
# The race and every holder: gates/README.md "Tree-write lock". cwd is "." =
# repo root, so the relative lock path resolves; /usr/bin/flock exists in the
# sandbox image.
GATES_JSON=$(
  printf '%s\n' "$SLUG_LIST" | while IFS= read -r slug; do
    [[ -z "$slug" ]] && continue
    jq -n \
      --arg name "coverage.docker-epic-${slug}" \
      --arg cmd  "flock -s .tree-write.lock make test-docker EPIC=${slug}" \
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

# ─── live run: pre-build bin/ once, under the EXCLUSIVE tree-write lock ───────
# Each child's `make test-docker` runs `make build` first (test-image's
# prerequisite), and it runs it under the child's SHARED hold. When bin/ is
# stale, that build deletes and rewrites bin/agent-director and
# bin/agent-director-admin while sibling children collect the tree as their
# build context: the race the lock exists to close. In a release run bin/ is
# always stale at this point, because `make build` stamps the HEAD commit
# (COMMIT_SHA) into both binaries and branch-and-bump commits just before the
# coverage phase. The test preload's exclusive build does not reliably come
# first either: Linux flock does not favour a waiting exclusive locker, so it
# usually queues behind the children's shared holds.
#
# So build bin/ here, once, as a tree writer: EXCLUSIVE, before run-parallel
# starts any child. No child holds the lock yet, so this waits only on other
# exclusive writers (the preload's builds, coverage.bun-test's install and
# build). Each child's own `make build` then finds both binaries current, and
# go build only updates their mtimes, which a concurrent context collection
# tolerates. If the pre-build fails, the gate fails without starting a child:
# every child would retry the same build under a shared hold.
PREBUILD_OUT="$(flock .tree-write.lock make build 2>&1)"
PREBUILD_EXIT=$?
if [[ "$PREBUILD_EXIT" -ne 0 ]]; then
  emit_diagnostic \
    "coverage.docker-epic-prebuild" \
    "bin/" \
    "make build under the exclusive tree-write lock (.tree-write.lock), run once before any docker epic starts, failed with exit ${PREBUILD_EXIT}; no docker epic ran. Output tail: $(printf '%s' "$PREBUILD_OUT" | tail -c 500)" \
    "Run 'make build' at the repo root to reproduce, fix the build, then rerun the coverage phase."
  exit 1
fi

# ─── live run: write config to temp file, delegate to run-parallel.sh ─────────
CONFIG_FILE=$(mktemp /tmp/docker-coverage-config.XXXXXX.json)

printf '%s\n' "$CONFIG_JSON" > "$CONFIG_FILE"

# ─── run children, but do NOT lose their diagnostics on failure ───────────────
# b.3jn: run-parallel emits its consolidated JSON (with each child's outcome,
# stderr_excerpt and SR-14 diagnostics) to STDOUT, but the outer phase executor
# discards a failing gate's stdout — so on failure this gate would exit 1 with
# an EMPTY stderr and diagnostics:[]. A gate that fails with empty stderr and no
# diagnostics is untrustworthy in a release run: the operator has nothing to act
# on. We therefore capture run-parallel's stdout, always re-emit it unchanged to
# our own stdout, and on non-zero exit additionally surface ONE SR-14 diagnostic
# per FAILED sub_check to stderr (where the executor preserves it), then exit
# with run-parallel's original code.
RP_STDOUT="$(mktemp "${TMPDIR:-/tmp}/docker-epics-rp.XXXXXX")"
trap 'rm -f "$CONFIG_FILE" "$RP_STDOUT"' EXIT

bash "$RUN_PARALLEL" "$CONFIG_FILE" > "$RP_STDOUT"
RP_EXIT=$?

# Always re-emit the consolidated JSON unchanged on stdout.
cat "$RP_STDOUT"

if [[ "$RP_EXIT" -ne 0 ]]; then
  # For each failed sub_check, emit one SR-14 diagnostic to stderr. Prefer the
  # child's own diagnostics array (joined) if non-empty; otherwise fall back to
  # the tail of its stderr_excerpt. Bound each description to the last ~500
  # chars so a runaway log can't swamp the release report.
  while IFS= read -r sub; do
    [[ -z "$sub" ]] && continue
    child_name="$(printf '%s' "$sub" | jq -r '.name')"
    slug="${child_name#coverage.docker-epic-}"

    diag_join="$(printf '%s' "$sub" | jq -r '
      if (.diagnostics | length) > 0
      then (.diagnostics | map(tostring) | join(" | "))
      else (.stderr_excerpt // "")
      end')"
    # Bound to last ~500 chars.
    desc="$(printf '%s' "$diag_join" | tail -c 500)"
    [[ -z "$desc" ]] && desc="child gate failed with no captured diagnostics or stderr"

    emit_diagnostic \
      "$child_name" \
      "test/docker-epics.txt" \
      "$desc" \
      "Rerun this EPIC alone to reproduce: make test-docker EPIC=${slug}"
  done < <(jq -c '.sub_checks[]? | select(.outcome != "passed")' "$RP_STDOUT" 2>/dev/null)
fi

exit "$RP_EXIT"
