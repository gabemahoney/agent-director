#!/bin/bash
# gate:        preflight.npm-whoami
# checks:      npm whoami exits 0 (token is valid against the registry)
# pass:        silent exit 0
# fail:        emit SR-14 JSON diagnostic to stderr, exit 1
# depends on:  npm, jq (via emit_diagnostic); honours $NPM_REGISTRY if set

GATE_LIB="$(cd "$(dirname "$0")/../lib" && pwd)"
# shellcheck source=../lib/emit-diagnostic.sh
source "${GATE_LIB}/emit-diagnostic.sh"

if [ -n "${NPM_REGISTRY:-}" ]; then
  NPM_STDERR=$(npm whoami --registry "$NPM_REGISTRY" 2>&1 1>/dev/null)
  NPM_EXIT=$?
else
  NPM_STDERR=$(npm whoami 2>&1 1>/dev/null)
  NPM_EXIT=$?
fi

if [ $NPM_EXIT -ne 0 ]; then
  emit_diagnostic \
    "preflight.npm-whoami" \
    "null" \
    "$(printf '%s' "$NPM_STDERR" | head -1)" \
    "Token may be expired — generate a new one and update NPM_TOKEN."
  exit 1
fi
