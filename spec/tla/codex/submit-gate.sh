#!/usr/bin/env bash
# Submits the gate set of submit-order.txt (the Q2 /review gate) to the job scheduler (channel
# C0B3X876XSB, --network off), APPENDING "job-dir<TAB>job-id<TAB>expected" lines to job-ids.txt
# (never truncating it). A job dir already listed in job-ids.txt is skipped. With arguments
# (job dir names), only those are submitted. Run mkjobs.sh first.
set -u
HERE="$(cd "$(dirname "$0")" && pwd)"
JS=/home/horde/projects/horde_admin/job-scheduler/bin/jobsched
OUT="$HERE/job-ids.txt"
sed -n '/^# Gate set/,$p' "$HERE/submit-order.txt" | grep -v '^#' | while IFS=$'\t' read -r dir timeout mem expect; do
  [ -z "$dir" ] && continue
  if [ $# -gt 0 ]; then case " $* " in *" $dir "*) ;; *) continue ;; esac; fi
  grep -q "^$dir	" "$OUT" 2>/dev/null && { echo "skip $dir (already submitted)"; continue; }
  name="bssz-${dir#job-}"
  id=$("$JS" submit --context "$HERE/$dir" --name "$name" --channel C0B3X876XSB --network off --memory "$mem" --timeout "$timeout" | head -1)
  printf '%s\t%s\t%s\n' "$dir" "$id" "$expect" | tee -a "$OUT"
done
