#!/bin/bash
# gate:        preflight.gh-auth
# checks:      gh CLI is authenticated (gh auth status exits 0)
# pass:        silent exit 0
# fail:        emit SR-14 JSON diagnostic to stderr, exit 1
# depends on:  gh (GitHub CLI)

GH_STDERR=$(gh auth status 2>&1 1>/dev/null)
GH_EXIT=$?

if [ $GH_EXIT -ne 0 ]; then
  DESCRIPTION=$(printf '%s' "$GH_STDERR" | head -1 | sed 's/"/\\"/g')
  printf '{"gate":"preflight.gh-auth","offending_file_or_artifact":null,"description":"%s","corrective_action":"Run gh auth login and retry."}\n' \
    "$DESCRIPTION" >&2
  exit 1
fi
