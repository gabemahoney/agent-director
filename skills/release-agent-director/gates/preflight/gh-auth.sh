#!/bin/bash
# gate:        preflight.gh-auth
# checks:      gh CLI is authenticated (gh auth status exits 0)
# pass:        silent exit 0
# fail:        emit SR-14 JSON diagnostic to stderr, exit 1
# depends on:  gh (GitHub CLI), jq (via emit_diagnostic)

GATE_LIB="$(cd "$(dirname "$0")/../lib" && pwd)"
# shellcheck source=../lib/emit-diagnostic.sh
source "${GATE_LIB}/emit-diagnostic.sh"

GH_STDERR=$(gh auth status 2>&1 1>/dev/null)
GH_EXIT=$?

if [ $GH_EXIT -ne 0 ]; then
  emit_diagnostic \
    "preflight.gh-auth" \
    "null" \
    "$(printf '%s' "$GH_STDERR" | head -1)" \
    "Run gh auth login and retry."
  exit 1
fi
