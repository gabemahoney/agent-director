#!/usr/bin/env bash
# Submits the fast set of submit-order.txt to the job scheduler (channel C0B3X876XSB).
# Writes "job-dir<TAB>job-id<TAB>expected" lines to job-ids.txt.
set -u
HERE="$(cd "$(dirname "$0")" && pwd)"
JS=/home/horde/projects/sysadmin-project/box_admin_dev/box_admin_horde/job-scheduler/bin/jobsched
OUT="$HERE/job-ids.txt"
: > "$OUT"
sed -n '/^# Fast set/,/^# Optional/p' "$HERE/submit-order.txt" | grep -v '^#' | while IFS=$'\t' read -r dir timeout mem expect; do
  [ -z "$dir" ] && continue
  name="bssz-${dir#job-}"
  id=$("$JS" submit --context "$HERE/$dir" --name "$name" --channel C0B3X876XSB --network off --memory "$mem" --timeout "$timeout" | head -1)
  printf '%s\t%s\t%s\n' "$dir" "$id" "$expect" | tee -a "$OUT"
done
