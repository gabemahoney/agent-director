#!/bin/sh
# argv-recorder.sh — writes its arguments, one per line, to $ARGV_FILE when
# set, then emits the dev sentinel envelope, which also passes Client.create()'s
# probe (whose scrubbed env drops ARGV_FILE). Used by subprocess-client.test.ts
# to observe the argv a Client forwards (b.38a).
if [ -n "$ARGV_FILE" ]; then
  printf '%s\n' "$@" > "$ARGV_FILE"
fi
printf '{"version":"0.0.0-dev","commit":"deadbeef"}\n'
