#!/bin/bash
# SR-14 structured diagnostic emitter.
#
# Source this file in a gate script:
#   source "$(dirname "$0")/../lib/emit-diagnostic.sh"
#
# Then call:
#   emit_diagnostic "preflight.worktree-clean" "path/to/file" "description" "corrective action"
#
# Emits a single JSON object to stderr. Caller should follow with `exit 1`.
# offending_file_or_artifact may be passed as "null" (string) to produce a
# JSON null rather than a quoted string.

emit_diagnostic() {
  local gate="$1"
  local offending="${2:-null}"
  local description="$3"
  local corrective="$4"

  # Basic JSON-escape: backslashes, double-quotes, newlines.
  _esc() {
    printf '%s' "$1" \
      | sed -e 's/\\/\\\\/g' \
            -e 's/"/\\"/g' \
            -e ':a;N;$!ba;s/\n/\\n/g'
  }

  if [ "$offending" = "null" ]; then
    printf '{"gate":"%s","offending_file_or_artifact":null,"description":"%s","corrective_action":"%s"}\n' \
      "$(_esc "$gate")" "$(_esc "$description")" "$(_esc "$corrective")" >&2
  else
    printf '{"gate":"%s","offending_file_or_artifact":"%s","description":"%s","corrective_action":"%s"}\n' \
      "$(_esc "$gate")" "$(_esc "$offending")" "$(_esc "$description")" "$(_esc "$corrective")" >&2
  fi
}
