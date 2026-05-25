#!/bin/sh
# success.sh — emits a minimal valid JSON envelope on stdout, exits 0.
# Used by spawner.test.ts to verify success-path drain + return.
printf '{"result":"ok","data":"hello-from-fixture"}\n'
