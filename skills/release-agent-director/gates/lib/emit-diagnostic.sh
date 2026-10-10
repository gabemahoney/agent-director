#!/bin/bash
# SR-14 structured diagnostic emitter.
#
# Source this file in a gate script:
#   source "$(dirname "$0")/../lib/emit-diagnostic.sh"
#
# Then call:
#   emit_diagnostic "preflight.worktree-clean" "path/to/file" "description" "corrective action"
#
# Emits a single compact JSON object, on one line, to stderr. Caller should
# follow with `exit 1`. offending_file_or_artifact may be passed as "null"
# (string) or left empty to produce a JSON null rather than a quoted string.
#
# Requires jq, which builds the object and so escapes every character JSON
# needs escaped, including TAB, CR and the other C0 control characters that
# raw command output (Go compiler errors, `go test` FAIL lines) carries (b.v46).
#
# The description reaches jq on stdin, never as an argument: it is the field
# that carries command output (`go test` tails, git stderr), and one argument
# over Linux's MAX_ARG_STRLEN (128 KiB) makes the exec of jq fail with "Argument
# list too long", so no diagnostic at all would be written. printf is a bash
# builtin, so the pipe has no such limit. jq -Rs reads all of stdin as one
# string, unchanged, trailing newlines included. The other three fields are a
# gate name, a path and fixed corrective text, so they stay small and go as
# --arg.

emit_diagnostic() {
  local gate="$1"
  local offending="${2:-null}"
  local description="$3"
  local corrective="$4"

  printf '%s' "$description" | jq -cRs \
    --arg gate "$gate" \
    --arg offending "$offending" \
    --arg corrective "$corrective" \
    '{
      gate: $gate,
      offending_file_or_artifact: (if $offending == "null" then null else $offending end),
      description: .,
      corrective_action: $corrective
    }' >&2
}
