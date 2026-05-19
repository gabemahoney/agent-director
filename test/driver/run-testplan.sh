#!/usr/bin/env bash
# Driver entrypoint. Reads a testplan from /work/tickets/testplans/<EPIC>/
# (read-only mount), launches a Claude Code instance with the driver prompt
# plus every t2 case body concatenated in, captures the JSONL pass/fail
# stream the driver-Claude emits, and exits 0 iff every t2 reports pass.
#
# Contract (Subtask 1.2 + 3.1):
#   - EPIC env var names the testplan slug (a t1 collector under
#     tickets/testplans/<slug>/).
#   - One JSON object per line on stdout: {"case": "<id>", "status": "pass|fail",
#     "details": "..."}. Tail line is the summary: {"summary": {...}}.
#   - Exit 0 iff every case reports pass.

set -euo pipefail

err() {
    printf '%s\n' "driver: $*" >&2
}

# Emit a single JSON object on stdout. Compact, one-per-line — anything that
# wants to scrape the run reads this format.
emit() {
    printf '%s\n' "$1"
}

emit_summary() {
    local total="$1" pass="$2" fail="$3"
    jq -cn \
        --argjson total "$total" \
        --argjson pass "$pass" \
        --argjson fail "$fail" \
        '{summary:{total:$total, pass:$pass, fail:$fail}}'
}

emit_case() {
    local case_id="$1" status="$2" details="$3"
    jq -cn \
        --arg case "$case_id" \
        --arg status "$status" \
        --arg details "$details" \
        '{case:$case, status:$status, details:$details}'
}

if [[ -z "${EPIC:-}" ]]; then
    err "EPIC env var is required (name of the testplan slug under tickets/testplans/)"
    exit 2
fi

TESTPLAN_ROOT="${TESTPLAN_ROOT:-/work/tickets/testplans}"
EPIC_DIR="${TESTPLAN_ROOT}/${EPIC}"

if [[ ! -d "$EPIC_DIR" ]]; then
    err "no such testplan: ${EPIC_DIR} not found"
    emit "$(emit_summary 0 0 0)"
    exit 3
fi

# Each t1 collector lives at testplans/<epic>/<bee>/<t1>/. Find every t2 case
# file under any nested t1 collector beneath EPIC_DIR.
mapfile -t CASE_FILES < <(find "$EPIC_DIR" -type f -name 't2.*.md' | sort)

if [[ "${#CASE_FILES[@]}" -eq 0 ]]; then
    err "testplan ${EPIC} has no t2 cases"
    emit "$(emit_summary 0 0 0)"
    exit 4
fi

err "loaded ${#CASE_FILES[@]} t2 case(s) from ${EPIC_DIR}"

DRIVER_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROMPT_TEMPLATE="${DRIVER_DIR}/prompt.md"
DB_RESET="${DRIVER_DIR}/db-reset.sh"

# Pick driver mode. "shell" runs each case as a self-contained shell script
# extracted from the t2 body — used by the harness-smoke testplan to keep
# Epic 2's gate free of external API calls. "claude" launches the real
# driver-Claude (full prompt + every case). Defaults to "claude" so the
# contract for functional Epics is the API path; harness-smoke overrides.
DRIVER_MODE="${DRIVER_MODE:-claude}"

run_case_shell() {
    # In shell mode the t2 body's fenced ```bash block is the script to
    # execute. Pass iff exit 0; fail otherwise. The DB-reset fixture runs
    # before each case to enforce isolation.
    local case_file="$1"
    local case_id
    case_id="$(basename "$(dirname "$case_file")")"

    bash "$DB_RESET" >&2 || true

    local script
    script="$(awk '/^```bash$/{flag=1;next}/^```$/{flag=0}flag' "$case_file")"

    if [[ -z "$script" ]]; then
        emit "$(emit_case "$case_id" fail 'no bash block in t2 body')"
        return 1
    fi

    local out rc
    out="$(bash -c "$script" 2>&1)" && rc=0 || rc=$?

    if [[ "$rc" -eq 0 ]]; then
        emit "$(emit_case "$case_id" pass "${out:0:200}")"
        return 0
    else
        emit "$(emit_case "$case_id" fail "exit=${rc}: ${out:0:400}")"
        return 1
    fi
}

run_case_claude() {
    local case_file="$1"
    local case_id
    case_id="$(basename "$(dirname "$case_file")")"

    bash "$DB_RESET" >&2 || true

    if [[ ! -r "$PROMPT_TEMPLATE" ]]; then
        emit "$(emit_case "$case_id" fail "missing driver prompt: $PROMPT_TEMPLATE")"
        return 1
    fi

    local prompt
    prompt="$(cat "$PROMPT_TEMPLATE")
---
Case file: ${case_file}
$(cat "$case_file")"

    local reply rc
    reply="$(claude --print --output-format json "$prompt" 2>&1)" && rc=0 || rc=$?

    if [[ "$rc" -ne 0 ]]; then
        emit "$(emit_case "$case_id" fail "claude exit=${rc}: ${reply:0:400}")"
        return 1
    fi

    # Driver-Claude is asked to return a single JSON object with a "verdict"
    # field of "pass" or "fail" and a brief "details" string.
    local verdict details
    verdict="$(printf '%s' "$reply" | jq -r '.result // empty' | jq -r '.verdict // "fail"' 2>/dev/null || echo fail)"
    details="$(printf '%s' "$reply" | jq -r '.result // empty' | jq -r '.details // ""' 2>/dev/null || true)"

    if [[ "$verdict" == "pass" ]]; then
        emit "$(emit_case "$case_id" pass "$details")"
        return 0
    fi
    emit "$(emit_case "$case_id" fail "$details")"
    return 1
}

total=0
pass=0
fail=0

for case_file in "${CASE_FILES[@]}"; do
    total=$((total + 1))
    if [[ "$DRIVER_MODE" == "shell" ]]; then
        if run_case_shell "$case_file"; then
            pass=$((pass + 1))
        else
            fail=$((fail + 1))
        fi
    else
        if run_case_claude "$case_file"; then
            pass=$((pass + 1))
        else
            fail=$((fail + 1))
        fi
    fi
done

emit "$(emit_summary "$total" "$pass" "$fail")"

if [[ "$fail" -ne 0 ]]; then
    exit 1
fi
exit 0
