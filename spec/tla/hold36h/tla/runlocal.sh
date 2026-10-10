#!/usr/bin/env bash
# Local, capped run of Hold36h cfgs with the scheduler's job script (tlcjob.sh), in
# eclipse-temurin:21-jre: container capped at 2 GB, TLC at -Xmx1g, 2 CPUs, no network.
# A sanity check of the small runs only, not the scheduler run.
# Usage: runlocal.sh <cfg>...
set -eu
HERE=$(cd "$(dirname "$0")" && pwd)
LIB=/home/horde/projects/agent-director-project/agent-director-lane-36h/spec/tla
ctx=$(mktemp -d "${SCRATCH:-/tmp}/h36.XXXXXX")
cp "$LIB/jobs/lib/tlcjob.sh" "$LIB/tla2tools.jar" "$HERE/Hold36h.tla" "$ctx/"
: > "$ctx/runs"
for c in "$@"; do cp "$HERE/cfg/$c.cfg" "$ctx/"; echo "Hold36h $c @${CAP:-120}" >> "$ctx/runs"; done
docker run --rm --user "$(id -u):$(id -g)" -e HOME=/work --memory=2g --cpus=2 -e XMX=1g -e DIRECT=256m \
  -e BUDGET=${BUDGET:-300} -e DISKCAP_MB=${DISKCAP_MB:-1000} \
  --network none -v "$ctx":/work -w /work eclipse-temurin:21-jre sh /work/tlcjob.sh
rm -rf "$ctx"
