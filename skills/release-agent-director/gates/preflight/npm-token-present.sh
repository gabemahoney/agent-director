#!/bin/bash
# gate:        preflight.npm-token-present
# checks:      $NPM_TOKEN env var is set and non-empty
# pass:        silent exit 0
# fail:        emit SR-14 JSON diagnostic to stderr, exit 1
# depends on:  jq (via emit_diagnostic) — env check only

GATE_LIB="$(cd "$(dirname "$0")/../lib" && pwd)"
# shellcheck source=../lib/emit-diagnostic.sh
source "${GATE_LIB}/emit-diagnostic.sh"

if [ -z "${NPM_TOKEN:-}" ]; then
  emit_diagnostic \
    "preflight.npm-token-present" \
    "null" \
    "NPM_TOKEN environment variable is not set or is empty." \
    "Set NPM_TOKEN in your shell before invoking /release."
  exit 1
fi
