#!/bin/sh
# success.sh — emits a minimal valid JSON envelope on stdout, exits 0.
# Used by spawner.test.ts to verify success-path drain + return.
#
# Probe-aware (b.ue3 / SR-1.3): when invoked with the `version` verb,
# emit the dev sentinel envelope so Client.create()'s construction-time
# probe passes.
if [ "$1" = "version" ]; then
  printf '{"version":"0.0.0-dev","commit":"deadbeef"}\n'
  exit 0
fi
printf '{"result":"ok","data":"hello-from-fixture"}\n'
