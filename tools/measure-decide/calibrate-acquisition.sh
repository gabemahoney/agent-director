#!/usr/bin/env bash
# Acquisition calibration uses the same capped supervisor as all diagnostics.
set -euo pipefail
attempt=${1:?supply a fresh attempt name}
case "$attempt" in *[!a-zA-Z0-9_-]*|'') exit 2;; esac
bash tools/measure-decide/diagnose.sh build on store "race-acquisition-$attempt"
bash tools/measure-decide/diagnose.sh run "acquisition-$attempt" store on "race-acquisition-$attempt" "^TestDecisionZeroWaitAllowsSQLBeyondAcquisitionAllowance$" calibration
