#!/usr/bin/env bash
# The one local sanity run of B3ieSettle (b.3ie), memory-capped (< 1 GB) in the job image's base
# (eclipse-temurin:21-jre), through the scheduler's own job script (spec/tla/jobs/lib/tlcjob.sh).
# Everything else runs on the job scheduler (see ../job-README.md).
# Usage: runsanity.sh > sanity.log 2>&1
set -eu
B=/home/horde/projects/agent-director-project/tickets/Bugs/b.3ie
REPO=/home/horde/projects/agent-director-project/agent-director-b.3ie
ctx=${CTX:-$(mktemp -d)}
cp "$REPO/spec/tla/jobs/lib/tlcjob.sh" "$REPO/spec/tla/tla2tools.jar" "$B/B3ieSettle.tla" "$B/tla-logs/sanity.cfg" "$ctx/"
printf '# local sanity run. Expected: PASS\nB3ieSettle sanity @280\n' > "$ctx/runs"
docker run --rm --user "$(id -u):$(id -g)" -e HOME=/work --memory=900m --cpus=2 \
  -e XMX=600m -e DIRECT=128m -e BUDGET=300 -e DISKCAP_MB=500 \
  --network none -v "$ctx":/work -w /work eclipse-temurin:21-jre sh /work/tlcjob.sh
