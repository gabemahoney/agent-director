#!/bin/sh
# b.i5y fixture: version-v2 — emits a distinct version token so tests can
# confirm which binary was actually invoked.
printf '{"version":"b-i5y-v2","commit":"v2commit"}\n'
