#!/usr/bin/env bash
# Emits one line per gate job (the job dirs of submit-order.txt's gate set, as listed in
# job-ids.txt) when it reaches a terminal state, with its JOB-VERDICT. Exits when all are terminal.
set -u
HERE="$(cd "$(dirname "$0")" && pwd)"
JS=/home/horde/projects/horde_admin/job-scheduler/bin/jobsched
gate=$(sed -n '/^# Gate set/,$p' "$HERE/submit-order.txt" | grep -v '^#' | cut -f1)
declare -A seen
total=$(echo "$gate" | grep -c .)
while true; do
  n=0
  while IFS=$'\t' read -r dir id expect; do
    echo "$gate" | grep -qx "$dir" || continue
    if [ -n "${seen[$id]:-}" ]; then n=$((n+1)); continue; fi
    st=$("$JS" status "$id" 2>/dev/null | grep -m1 -E '^state:' | awk '{print $2}')
    case "$st" in
      succeeded|failed|cancelled|timeout|timed_out|rejected|error)
        seen[$id]=1; n=$((n+1))
        sleep 15
        v=$(grep -oE 'JOB-VERDICT.*' "$HOME/.job-scheduler/jobs/$id/run.log" 2>/dev/null | tail -1)
        echo "$dir $id state=$st expect=[$expect] ${v:-verdict-unread}"
        ;;
    esac
  done < "$HERE/job-ids.txt"
  [ "$n" -ge "$total" ] && { echo "ALL-DONE $n/$total"; exit 0; }
  sleep 60
done
