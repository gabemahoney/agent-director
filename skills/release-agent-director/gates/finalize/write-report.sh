#!/bin/bash
# SR-15 release report writer.
#
# Usage:
#   write-report.sh <invocation-timestamp-iso> <mode> <bump-kind> \
#                   <source-version> <target-version> \
#                   <phases-json-file> [<diagnostics-json-file>] \
#                   <elapsed-seconds>
#
# Arguments (positional):
#   $1  invocation_timestamp  ISO-8601 timestamp of the release invocation
#   $2  mode                  "dry-run" | "release"
#   $3  bump_kind             "patch" | "minor" | "major"
#   $4  source_version        semver string of the pre-release version
#   $5  target_version        semver string of the post-release version
#   $6  phases_file           path of a file holding the JSON array of phase
#                             objects (SR-15 schema)
#   $7  diagnostics_file      path of a file holding the JSON array of SR-14
#                             diagnostic payloads; pass "" or omit for an
#                             empty array. If this argument is a valid number
#                             it is treated as elapsed_seconds (backward-compat
#                             shim).
#   $8  elapsed_seconds       Wall-clock seconds the release run took (float)
#
# The phases and diagnostics arrays are passed as files, not as arguments
# (b.2wr): they carry gate output verbatim (stderr_excerpt, SR-14 payloads),
# and Linux caps one argument at 128 KiB (MAX_ARG_STRLEN), over which neither
# this script nor jq could even be started. Each file must be a readable
# regular file holding exactly one JSON array; relative paths resolve against
# the caller's working directory. The arrays reach jq on stdin, so they may be
# any size.
#
# Output schema written to dist/release-report.json:
# {
#   "invocation_timestamp": "ISO8601",
#   "mode": "dry-run" | "release",
#   "bump_kind": "patch" | "minor" | "major",
#   "source_version": "string",
#   "target_version": "string",
#   "phases": [
#     { "name": "string", "outcome": "passed"|"failed"|"skipped",
#       "started_at": "ISO8601", "elapsed_ms": int,
#       "sub_checks": [
#         {"name": "string", "outcome": "...", "diagnostic": "<SR-14 payload or null>"}
#       ]
#     }
#   ],
#   "publish_substeps": [],
#   "diagnostics": ["<SR-14 payload>", "..."],
#   "elapsed_seconds": float
# }
#
# Exits 0 on success, non-zero on validation or file-write error.

set -euo pipefail

INVOCATION_TS="${1:?invocation_timestamp required}"
MODE="${2:?mode required}"
BUMP_KIND="${3:?bump_kind required}"
SOURCE_VERSION="${4:?source_version required}"
TARGET_VERSION="${5:?target_version required}"
PHASES_FILE="${6:?phases_file required}"

# Argument 7 is optional: diagnostics file OR (legacy) elapsed_seconds.
# Detect by checking whether it looks like a number. An empty
# DIAGNOSTICS_FILE means no diagnostics (an empty array).
if [ "${7:-}" = "" ]; then
  DIAGNOSTICS_FILE=""
  ELAPSED="${8:-0}"
elif echo "${7}" | grep -qE '^[0-9]+(\.[0-9]+)?$'; then
  # Treat as elapsed_seconds (backward-compat: no diagnostics arg passed)
  DIAGNOSTICS_FILE=""
  ELAPSED="${7}"
else
  DIAGNOSTICS_FILE="${7}"
  ELAPSED="${8:-0}"
fi

# --------------------------------------------------------------------------
# Validate JSON inputs
# --------------------------------------------------------------------------
# require_json_array_file <label> <path> exits 2 unless <path> is a readable
# regular file holding exactly one JSON array. A regular file is required
# because the file is read twice (here and when the report is assembled),
# which a pipe cannot be. At most 200 characters of <path> are echoed, so a
# caller that still passes the array itself gets a short message.
require_json_array_file() {
  local label="$1" path="$2"
  if [ ! -f "$path" ] || [ ! -r "$path" ]; then
    printf 'ERROR: %s is not a readable regular file: %.200s\n' "$label" "$path" >&2
    printf 'Pass the path of a file holding the JSON array, not the array itself.\n' >&2
    exit 2
  fi
  if ! jq -se 'length == 1 and (.[0] | type) == "array"' "$path" >/dev/null; then
    printf 'ERROR: %s does not hold exactly one JSON array: %s\n' "$label" "$path" >&2
    exit 2
  fi
}

require_json_array_file phases_file "$PHASES_FILE"
if [ -n "$DIAGNOSTICS_FILE" ]; then
  require_json_array_file diagnostics_file "$DIAGNOSTICS_FILE"
fi

# --------------------------------------------------------------------------
# Build output directory
# --------------------------------------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# Walk up to the skill root (gates/finalize -> gates -> release-agent-director)
SKILL_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
DIST_DIR="$SKILL_ROOT/dist"

mkdir -p "$DIST_DIR"

REPORT_PATH="$DIST_DIR/release-report.json"

# --------------------------------------------------------------------------
# Assemble and write the report
# --------------------------------------------------------------------------
# The two arrays reach jq on stdin as two JSON values, never as --argjson
# (b.2wr); each file holds exactly one array, checked above.
{
  cat "$PHASES_FILE"
  printf '\n'
  if [ -n "$DIAGNOSTICS_FILE" ]; then
    cat "$DIAGNOSTICS_FILE"
    printf '\n'
  else
    printf '[]\n'
  fi
} | jq -s \
  --arg      invocation_timestamp "$INVOCATION_TS" \
  --arg      mode                 "$MODE" \
  --arg      bump_kind            "$BUMP_KIND" \
  --arg      source_version       "$SOURCE_VERSION" \
  --arg      target_version       "$TARGET_VERSION" \
  --argjson  elapsed_seconds      "$ELAPSED" \
  '. as [$phases, $diagnostics]
  | {
    invocation_timestamp: $invocation_timestamp,
    mode:                 $mode,
    bump_kind:            $bump_kind,
    source_version:       $source_version,
    target_version:       $target_version,
    phases:               $phases,
    publish_substeps:     [],
    diagnostics:          $diagnostics,
    elapsed_seconds:      $elapsed_seconds
  }' > "$REPORT_PATH"

echo "release-report written → $REPORT_PATH"
