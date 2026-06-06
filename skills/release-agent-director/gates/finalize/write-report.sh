#!/bin/bash
# SR-15 release report writer.
#
# Usage:
#   write-report.sh <invocation-timestamp-iso> <mode> <bump-kind> \
#                   <source-version> <target-version> \
#                   <phases-json-array> [<diagnostics-json-array>] \
#                   <elapsed-seconds>
#
# Arguments (positional):
#   $1  invocation_timestamp  ISO-8601 timestamp of the release invocation
#   $2  mode                  "dry-run" | "release"
#   $3  bump_kind             "patch" | "minor" | "major"
#   $4  source_version        semver string of the pre-release version
#   $5  target_version        semver string of the post-release version
#   $6  phases_json           JSON array of phase objects (SR-15 schema)
#   $7  diagnostics_json      JSON array of SR-14 diagnostic payloads; pass
#                             "[]" or omit to default to an empty array.
#                             If this argument is a valid number it is treated
#                             as elapsed_seconds (backward-compat shim).
#   $8  elapsed_seconds       Wall-clock seconds the release run took (float)
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
PHASES_JSON="${6:?phases_json required}"

# Argument 7 is optional: diagnostics array OR (legacy) elapsed_seconds.
# Detect by checking whether it looks like a number.
if [ "${7:-}" = "" ]; then
  DIAGNOSTICS_JSON="[]"
  ELAPSED="${8:-0}"
elif echo "${7}" | grep -qE '^[0-9]+(\.[0-9]+)?$'; then
  # Treat as elapsed_seconds (backward-compat: no diagnostics arg passed)
  DIAGNOSTICS_JSON="[]"
  ELAPSED="${7}"
else
  DIAGNOSTICS_JSON="${7}"
  ELAPSED="${8:-0}"
fi

# --------------------------------------------------------------------------
# Validate JSON inputs
# --------------------------------------------------------------------------
if ! echo "$PHASES_JSON" | jq 'if type=="array" then . else error("not an array") end' >/dev/null 2>&1; then
  echo "ERROR: phases_json is not a valid JSON array" >&2
  exit 2
fi

if ! echo "$DIAGNOSTICS_JSON" | jq 'if type=="array" then . else error("not an array") end' >/dev/null 2>&1; then
  echo "ERROR: diagnostics_json is not a valid JSON array" >&2
  exit 2
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
jq -n \
  --arg      invocation_timestamp "$INVOCATION_TS" \
  --arg      mode                 "$MODE" \
  --arg      bump_kind            "$BUMP_KIND" \
  --arg      source_version       "$SOURCE_VERSION" \
  --arg      target_version       "$TARGET_VERSION" \
  --argjson  phases               "$PHASES_JSON" \
  --argjson  diagnostics          "$DIAGNOSTICS_JSON" \
  --argjson  elapsed_seconds      "$ELAPSED" \
  '{
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
