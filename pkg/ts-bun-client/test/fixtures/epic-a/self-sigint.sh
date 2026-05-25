#!/bin/sh
# self-sigint.sh — sends SIGINT to its own process, then exits with signal.
# Used by subprocess-client.test.ts to verify ErrConsumerSignal.
# The subprocess exits via SIGINT; Bun sees subprocess.signalCode = "SIGINT".
kill -INT $$
