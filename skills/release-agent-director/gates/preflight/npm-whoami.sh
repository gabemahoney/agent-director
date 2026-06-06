#!/bin/bash
# gate:        preflight.npm-whoami
# checks:      npm whoami exits 0 (token is valid against the registry)
# pass:        silent exit 0
# fail:        emit SR-14 JSON diagnostic to stderr, exit 1
# depends on:  npm; honours $NPM_REGISTRY if set

if [ -n "${NPM_REGISTRY:-}" ]; then
  NPM_STDERR=$(npm whoami --registry "$NPM_REGISTRY" 2>&1 1>/dev/null)
  NPM_EXIT=$?
else
  NPM_STDERR=$(npm whoami 2>&1 1>/dev/null)
  NPM_EXIT=$?
fi

if [ $NPM_EXIT -ne 0 ]; then
  DESCRIPTION=$(printf '%s' "$NPM_STDERR" | head -1 | sed 's/"/\\"/g')
  printf '{"gate":"preflight.npm-whoami","offending_file_or_artifact":null,"description":"%s","corrective_action":"Token may be expired — generate a new one and update NPM_TOKEN."}\n' \
    "$DESCRIPTION" >&2
  exit 1
fi
