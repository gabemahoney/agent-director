#!/usr/bin/env bash
# gate:     notes.write-preview
# checks:   runs generate-release-notes.ts and writes output to
#           dist/release-notes-preview.md (dry-run only — no commit/push)
# usage:    bash write-preview.sh [--from <prev-tag>]
# pass:     dist/release-notes-preview.md written (or overwritten), exit 0
# fail:     SR-14 diagnostic to stderr, exit 1

set -uo pipefail

GATE_LIB="$(cd "$(dirname "$0")/../lib" && pwd)"
# shellcheck source=../lib/emit-diagnostic.sh
source "${GATE_LIB}/emit-diagnostic.sh"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# Walk up to skill root (gates/notes -> gates -> release-agent-director)
SKILL_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
# Walk up to repo root (release-agent-director -> skills -> .)
REPO_ROOT="$(cd "${SKILL_ROOT}/../.." && pwd)"

DIST_DIR="${SKILL_ROOT}/dist"
PREVIEW_PATH="${DIST_DIR}/release-notes-preview.md"
GENERATOR="pkg/ts-bun-client/scripts/generate-release-notes.ts"

# ─── argument parsing ─────────────────────────────────────────────────────────
FROM_REF=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --from)
      FROM_REF="$2"
      shift 2
      ;;
    *)
      printf 'write-preview.sh: unknown option: %s\n' "$1" >&2
      exit 2
      ;;
  esac
done

# ─── derive --from from latest v* tag if not supplied ─────────────────────────
if [[ -z "$FROM_REF" ]]; then
  FROM_REF="$(git -C "${REPO_ROOT}" tag --list 'v*.*.*' --sort=-version:refname | head -1)"
  if [[ -z "$FROM_REF" ]]; then
    emit_diagnostic \
      "notes.write-preview" \
      "null" \
      "No v*.*.* tags found in repository; cannot derive --from ref." \
      "Create at least one semver tag (e.g. git tag v0.0.0) or pass --from <ref> explicitly."
    exit 1
  fi
fi

# ─── ensure dist/ exists ──────────────────────────────────────────────────────
mkdir -p "${DIST_DIR}"

# ─── run generator ────────────────────────────────────────────────────────────
if ! bun run --cwd "${REPO_ROOT}" "${GENERATOR}" \
       --from "${FROM_REF}" \
       --output "${PREVIEW_PATH}" 2>/dev/null; then
  emit_diagnostic \
    "notes.write-preview" \
    "null" \
    "generate-release-notes.ts failed for range ${FROM_REF}..HEAD" \
    "Run 'bun run ${GENERATOR} --from ${FROM_REF}' manually to diagnose."
  exit 1
fi

# ─── verify file was actually written ─────────────────────────────────────────
if [[ ! -f "${PREVIEW_PATH}" ]]; then
  emit_diagnostic \
    "notes.write-preview" \
    "${PREVIEW_PATH}" \
    "Generator exited 0 but ${PREVIEW_PATH} was not created." \
    "Check disk space and write permissions on ${DIST_DIR}."
  exit 1
fi

exit 0
