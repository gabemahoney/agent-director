#!/bin/bash
# gate:        preflight.npm-token-present
# checks:      $NPM_TOKEN env var is set and non-empty
# pass:        silent exit 0
# fail:        emit SR-14 JSON diagnostic to stderr, exit 1
# depends on:  (none — env check only)

if [ -z "${NPM_TOKEN:-}" ]; then
  printf '{"gate":"preflight.npm-token-present","offending_file_or_artifact":null,"description":"NPM_TOKEN environment variable is not set or is empty.","corrective_action":"Set NPM_TOKEN in your shell before invoking /release."}\n' >&2
  exit 1
fi
