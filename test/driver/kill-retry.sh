#!/usr/bin/env bash
# Reusable Docker testplan helper (b.69m). Source /opt/driver/kill-retry.sh,
# then call kill_with_retry with kill's arguments. Only ErrTmuxKillFailed
# grants retry authority; print every failed answer to stderr for diagnosis.
# Six attempts, with 1/2/4/8/8 s backoff (23 s total), give a slow agent
# several times the observed ~5.2 s exit time before the case gives up.
# Successful stdout and the final exit code belong to the last kill call.

kill_with_retry() {
    local attempt=1 max_attempts=6 delay=1 output rc error_name
    while :; do
        if output="$(agent-director kill "$@" 2>&1)"; then
            printf '%s\n' "$output"
            return 0
        else
            rc=$?
        fi
        printf 'kill attempt %s/%s (exit %s):\n%s\n' "$attempt" "$max_attempts" "$rc" "$output" >&2
        # CLI errors are the last JSON line, after any diagnostic text.
        error_name="$(printf '%s\n' "$output" | awk '/^\{/{line=$0} END{print line}' | jq -r '.err_name // empty' 2>/dev/null)" || error_name=""
        if [[ "$error_name" != ErrTmuxKillFailed || "$attempt" -ge "$max_attempts" ]]; then
            return "$rc"
        fi
        sleep "$delay" || return "$?"
        attempt=$((attempt + 1))
        if [[ "$delay" -lt 8 ]]; then
            delay=$((delay * 2))
        fi
    done
}
