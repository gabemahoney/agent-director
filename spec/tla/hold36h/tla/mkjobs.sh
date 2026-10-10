#!/usr/bin/env bash
# Builds one self-contained scheduler job directory per run of suite.tsv:
# ../job-<run>/ with the Dockerfile, the unchanged tlcjob.sh and tla2tools.jar
# (spec/tla), Hold36h.tla, the run's cfg and a one-line runs file.
set -eu
HERE=$(cd "$(dirname "$0")" && pwd)
LIB=/home/horde/projects/agent-director-project/agent-director-lane-36h/spec/tla
tail -n +2 "$HERE/suite.tsv" | while IFS=$'\t' read -r run expected what; do
  d="$HERE/../job-$run"
  rm -rf "$d"
  mkdir -p "$d"
  cp "$LIB/jobs/lib/tlcjob.sh" "$LIB/tla2tools.jar" "$HERE/Hold36h.tla" "$HERE/cfg/$run.cfg" "$d/"
  printf '# b.36h: one run per job. Expected: %s\nHold36h %s @3300\n' "$expected" "$run" > "$d/runs"
  cat > "$d/Dockerfile" <<'EOF'
# b.36h: one run of Hold36h.tla (tickets/Bugs/b.36h/tla), for the job scheduler only.
# The run script is tlcjob.sh (spec/tla/jobs/lib/tlcjob.sh, unchanged); it reads /work/runs and
# its LAST line is the one-line verdict "JOB-VERDICT <cfg>=<result>/<distinct>".
FROM eclipse-temurin:21-jre
WORKDIR /work
COPY . /work/
ENV XMX=12g DIRECT=2g BUDGET=3400 DISKCAP_MB=20000
RUN chmod 0755 /work/tlcjob.sh && java -version
CMD ["/work/tlcjob.sh"]
EOF
done
