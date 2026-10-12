#!/usr/bin/env bash
# b.ssz: builds one self-contained scheduler job directory per row of suite.tsv that has no job
# directory yet: job-<run>/ with the Dockerfile, the unchanged tlcjob.sh and tla2tools.jar (taken
# from agent-director-bssz/spec/tla, byte-identical to the 21 earlier jobs' copies), the run's
# spec (src/), its cfg (src/cfg/) and a one-line runs file. It never touches an existing job
# directory (those are the "as submitted" record), and it runs no TLC. Regenerate the cfgs first
# with src/gencfg.sh.
# Usage: bash mkjobs.sh
set -eu
HERE=$(cd "$(dirname "$0")" && pwd)
LIB=/home/horde/projects/agent-director-project/agent-director-bssz/spec/tla
tail -n +2 "$HERE/suite.tsv" | while IFS=$'\t' read -r run spec expected budget xmx direct disk tmo mem tier what; do
  d="$HERE/job-$run"
  if [ -e "$d" ]; then echo "keep $d (already built)"; continue; fi
  mkdir -p "$d"
  cp "$LIB/jobs/lib/tlcjob.sh" "$LIB/tla2tools.jar" "$HERE/src/$spec.tla" "$HERE/src/cfg/$run.cfg" "$d/"
  cap=$((budget - 100))
  printf '# b.ssz: one run per job (%s tier). Expected: %s\n%s %s @%s\n' "$tier" "$expected" "$spec" "$run" "$cap" > "$d/runs"
  cat > "$d/Dockerfile" <<EOT
# b.ssz: one run of $spec.tla (tickets/Ideas/b.ssz/tla/src), for the job scheduler only
# (submit with --timeout $tmo --memory $mem). The run script is tlcjob.sh
# (spec/tla/jobs/lib/tlcjob.sh, unchanged); it reads /work/runs and its LAST line is the
# one-line verdict "JOB-VERDICT <cfg>=<result>/<distinct>". Expected: $expected
FROM eclipse-temurin:21-jre
WORKDIR /work
COPY . /work/
ENV XMX=$xmx DIRECT=$direct BUDGET=$budget DISKCAP_MB=$disk
RUN chmod 0755 /work/tlcjob.sh && java -version
CMD ["/work/tlcjob.sh"]
EOT
  echo "built $d"
done
