#!/bin/sh
# nonzero-exit.sh — writes garbage to stdout + a message to stderr, exits 1.
# Used by spawner.test.ts to verify subprocess-crash throw on non-zero exit.
printf 'garbage output not json\n'
printf 'something went wrong in subprocess\n' >&2
exit 1
