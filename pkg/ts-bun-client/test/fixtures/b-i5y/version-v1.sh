#!/bin/sh
# b.i5y fixture: version-v1 — emits a distinct version token so tests can
# confirm which binary was actually invoked.
printf '{"version":"b-i5y-v1","commit":"v1commit"}\n'
