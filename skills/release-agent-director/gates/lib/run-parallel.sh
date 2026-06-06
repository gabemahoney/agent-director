#!/usr/bin/env bash
# gates/lib/run-parallel.sh — Parallel gate executor, sibling-tolerant aggregator.
#
# USAGE:
#   bash skills/release-agent-director/gates/lib/run-parallel.sh <gates-config.json>
#
# INPUT: JSON config file with schema:
#   {
#     "phase_name": "coverage",
#     "gates": [
#       {"name": "coverage.go-root", "command": "go test ./...", "cwd": "."},
#       ...
#     ],
#     "max_parallel": 4
#   }
#
# OUTPUT (stdout): Consolidated JSON with all gate outcomes.
# EXIT CODE: 0 if all gates passed; 1 if any gate failed; 2 on usage/config error.
#
# BEHAVIOR:
#   - All gates run to completion regardless of siblings failing (SR-6.5: sibling-tolerant).
#   - Per-gate stdout/stderr captured in temp files; temp dir cleaned up on exit.
#   - SR-14 JSON diagnostics (one JSON object per line on stderr) are parsed and
#     included in the consolidated output under each gate's "diagnostics" array.
#   - Concurrency controlled via a FIFO semaphore (fd 3); at most max_parallel
#     gate subprocesses run simultaneously.

set -uo pipefail

# ─── argument validation ──────────────────────────────────────────────────────
if [[ $# -lt 1 ]]; then
  printf 'usage: run-parallel.sh <gates-config.json>\n' >&2
  exit 2
fi

CONFIG_FILE="$1"

if [[ ! -f "$CONFIG_FILE" ]]; then
  printf 'run-parallel.sh: config file not found: %s\n' "$CONFIG_FILE" >&2
  exit 2
fi

if ! jq empty "$CONFIG_FILE" 2>/dev/null; then
  printf 'run-parallel.sh: invalid JSON in config file: %s\n' "$CONFIG_FILE" >&2
  exit 2
fi

# ─── parse config ─────────────────────────────────────────────────────────────
PHASE_NAME=$(jq -r '.phase_name // "unknown"' "$CONFIG_FILE")
MAX_PARALLEL=$(jq -r '(.max_parallel // 4) | tonumber | floor' "$CONFIG_FILE")
GATE_COUNT=$(jq '.gates | length' "$CONFIG_FILE")

# ─── edge case: empty gates array ─────────────────────────────────────────────
if [[ "$GATE_COUNT" -eq 0 ]]; then
  jq -n --arg phase "$PHASE_NAME" \
    '{"phase_name": $phase, "outcome": "passed", "sub_checks": []}'
  exit 0
fi

# ─── work directory (cleaned up on exit) ──────────────────────────────────────
WORK_DIR=$(mktemp -d)
trap 'rm -rf "$WORK_DIR"' EXIT

# ─── semaphore: FIFO + fd 3 ───────────────────────────────────────────────────
# Open FIFO for both read and write so the read end never sees EOF.
# Pre-fill with MAX_PARALLEL tokens; child processes write back a token on
# completion, allowing the parent to launch the next gate.
FIFO="$WORK_DIR/.semaphore"
mkfifo "$FIFO"
exec 3<>"$FIFO"

for ((i = 0; i < MAX_PARALLEL; i++)); do
  printf 'x' >&3
done

# ─── launch all gates ─────────────────────────────────────────────────────────
declare -a GATE_NAMES=()
declare -a GATE_PIDS=()

for ((idx = 0; idx < GATE_COUNT; idx++)); do
  name=$(jq -r ".gates[$idx].name"    "$CONFIG_FILE")
  cmd=$(jq  -r ".gates[$idx].command" "$CONFIG_FILE")
  cwd=$(jq  -r ".gates[$idx].cwd"     "$CONFIG_FILE")

  GATE_NAMES+=("$name")

  # Build a filesystem-safe name prefix for temp files.
  safe_name=$(printf '%s' "$name" | tr '/ ' '__')
  prefix="$WORK_DIR/${idx}_${safe_name}"

  stdout_file="${prefix}.stdout"
  stderr_file="${prefix}.stderr"
  exit_code_file="${prefix}.exit"
  start_ns_file="${prefix}.start_ns"
  end_ns_file="${prefix}.end_ns"

  # Acquire a semaphore token — blocks when MAX_PARALLEL jobs are already running.
  read -r -n1 -u3 _token

  (
    printf '%s' "$(date +%s%N)" > "$start_ns_file"

    exit_code=0

    if [[ ! -d "$cwd" ]]; then
      # Emit an SR-14 style diagnostic for missing cwd; mark as failed.
      printf '{"gate":"%s","offending_file_or_artifact":"%s","description":"gate cwd does not exist: %s","corrective_action":"Fix the cwd field in the gates-config.json for this gate."}\n' \
        "$name" "$cwd" "$cwd" >&2
      exit_code=1
    else
      (cd "$cwd" && bash -c "$cmd") || exit_code=$?
    fi

    printf '%s' "$(date +%s%N)" > "$end_ns_file"
    printf '%d'  "$exit_code"    > "$exit_code_file"

    # Release the semaphore token so the next gate can launch.
    printf 'x' >&3

    exit "$exit_code"
  ) > "$stdout_file" 2> "$stderr_file" &

  GATE_PIDS+=($!)
done

# ─── wait for ALL gates (SR-6.5 sibling-tolerant: never cancel) ───────────────
for pid in "${GATE_PIDS[@]}"; do
  wait "$pid" 2>/dev/null || true
done

# Close the semaphore fd — no longer needed.
exec 3>&-

# ─── assemble consolidated JSON ───────────────────────────────────────────────
overall_outcome="passed"
sub_check_files=()

for ((idx = 0; idx < GATE_COUNT; idx++)); do
  name="${GATE_NAMES[$idx]}"
  safe_name=$(printf '%s' "$name" | tr '/ ' '__')
  prefix="$WORK_DIR/${idx}_${safe_name}"

  stderr_file="${prefix}.stderr"
  exit_code_file="${prefix}.exit"
  start_ns_file="${prefix}.start_ns"
  end_ns_file="${prefix}.end_ns"

  # Exit code (default to 1 if file wasn't written, e.g. process killed).
  exit_code=1
  if [[ -f "$exit_code_file" ]]; then
    exit_code=$(cat "$exit_code_file")
  fi

  # Gate outcome.
  if [[ "$exit_code" -eq 0 ]]; then
    gate_outcome="passed"
  else
    gate_outcome="failed"
    overall_outcome="failed"
  fi

  # Duration in milliseconds.
  duration_ms=0
  if [[ -f "$start_ns_file" && -f "$end_ns_file" ]]; then
    start_ns=$(cat "$start_ns_file")
    end_ns=$(cat "$end_ns_file")
    duration_ms=$(( (end_ns - start_ns) / 1000000 ))
    if [[ "$duration_ms" -lt 0 ]]; then
      duration_ms=0  # Guard against clock skew.
    fi
  fi

  # stderr excerpt: last 50 lines.
  stderr_excerpt=""
  if [[ -f "$stderr_file" ]]; then
    stderr_excerpt=$(tail -n50 "$stderr_file")
  fi

  # SR-14 diagnostics: lines from stderr that are JSON objects (start with '{').
  diagnostics_json="[]"
  if [[ -f "$stderr_file" ]] && grep -q '^{' "$stderr_file" 2>/dev/null; then
    diagnostics_json=$(grep '^{' "$stderr_file" | jq -sc '.' 2>/dev/null || printf '[]')
  fi

  # Write this gate's sub_check JSON to a temp file.
  sub_check_file="${prefix}.sub_check.json"
  jq -n \
    --arg     name           "$name"           \
    --arg     outcome        "$gate_outcome"   \
    --argjson duration_ms    "$duration_ms"    \
    --argjson exit_code      "$exit_code"      \
    --arg     stderr_excerpt "$stderr_excerpt" \
    --argjson diagnostics    "$diagnostics_json" \
    '{
      name:           $name,
      outcome:        $outcome,
      duration_ms:    $duration_ms,
      exit_code:      $exit_code,
      stderr_excerpt: $stderr_excerpt,
      diagnostics:    $diagnostics
    }' > "$sub_check_file"

  sub_check_files+=("$sub_check_file")
done

# Combine all sub_check JSON objects into a single array.
all_sub_checks=$(jq -sc '.' "${sub_check_files[@]}")

# Emit consolidated JSON to stdout.
jq -n \
  --arg     phase_name  "$PHASE_NAME"      \
  --arg     outcome     "$overall_outcome" \
  --argjson sub_checks  "$all_sub_checks"  \
  '{phase_name: $phase_name, outcome: $outcome, sub_checks: $sub_checks}'

# Exit 1 if any gate failed; 0 if all passed.
if [[ "$overall_outcome" == "passed" ]]; then
  exit 0
else
  exit 1
fi
