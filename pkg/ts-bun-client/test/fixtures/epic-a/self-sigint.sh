#!/bin/sh
# self-sigint.sh — sends SIGINT to its own process, then exits with signal.
# Used by subprocess-client.test.ts to verify ErrConsumerSignal.
# The subprocess exits via SIGINT; Bun sees subprocess.signalCode = "SIGINT".
#
# Probe-aware (b.ue3 / SR-1.3): when invoked with the `version` verb,
# emit a sentinel envelope so Client.create()'s probe passes; only
# self-SIGINT for non-version invocations (the verb under test).
if [ "$1" = "version" ]; then
  printf '{"version":"0.0.0-dev","commit":"deadbeef"}\n'
  exit 0
fi
kill -INT $$
