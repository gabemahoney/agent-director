#!/bin/sh
# non-json.sh — writes non-JSON to stdout, exits 0.
# Used by spawner.test.ts to verify subprocess-crash throw on parse failure.
printf 'not json\n'
