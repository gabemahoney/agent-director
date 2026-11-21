#!/usr/bin/env bash
# Docker lifecycle fixtures use apitest.SeedSpawn through the existing driver.
# Fixture catalogue: docs/architecture.md, "Docker lifecycle seed fixture".
# Cache only the executable from the image's immutable source; each call seeds
# the caller's reset store. No schema SQL or test case definitions live here.
set -euo pipefail
[[ ${AGENT_DIRECTOR_TEST_HARNESS:-} == 1 ]] || { echo 'Docker harness required' >&2; exit 1; }
[[ $# == 2 ]] || { echo 'usage: seed-spawn.sh DB PARAMS_JSON' >&2; exit 1; }
export AGENT_DIRECTOR_TEST_SANDBOX=1 GOMAXPROCS=2 GOFLAGS=-p=1
export GOMODCACHE=/opt/mixed-version-modules GOPROXY=off GOSUMDB=off
driver=/tmp/agent-director-seed-spawn/driver
if [[ ! -x "$driver" ]]; then
    mkdir -p "${driver%/*}"
    go -C /opt/mixed-version-source build -buildvcs=false -o "$driver" ./test/driver/busy-send-mixed
fi
printf '{"op":"seed-spawn","params":%s}\n' "$2" | "$driver" store "$1" |
    jq -er -s '.[-1] | if .error then error(.error) else .result end'
