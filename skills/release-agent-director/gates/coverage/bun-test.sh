#!/usr/bin/env bash
# gate:        coverage.bun-test
# checks:      bun install, build, and test pass for pkg/ts-bun-client
# pass:        silent exit 0
# fail:        emit SR-14 JSON diagnostic to stderr, exit 1
#
# Usage: bun-test.sh [worktree-root]
#   worktree-root defaults to the repo root (parent of this script's gate tree)

set -euo pipefail

GATE_LIB="$(cd "$(dirname "$0")/../lib" && pwd)"
# shellcheck source=../lib/emit-diagnostic.sh
source "${GATE_LIB}/emit-diagnostic.sh"

WORKTREE_ROOT="${1:-$(cd "$(dirname "$0")/../../../.." && pwd)}"
PKG_DIR="${WORKTREE_ROOT}/pkg/ts-bun-client"

if [ ! -d "$PKG_DIR" ]; then
  emit_diagnostic \
    "coverage.bun-test" \
    "pkg/ts-bun-client" \
    "pkg/ts-bun-client directory not found at ${PKG_DIR}" \
    "Verify worktree root is the repo root, or pass the correct path as \$1."
  exit 1
fi

cd "$PKG_DIR"

if ! bun install --frozen-lockfile 2>&1; then
  emit_diagnostic \
    "coverage.bun-test" \
    "pkg/ts-bun-client/bun.lockb" \
    "bun install --frozen-lockfile failed" \
    "Run 'bun install' locally and commit the updated lockfile."
  exit 1
fi

export PATH="$PWD/node_modules/.bin:$PATH"

if ! bun run build 2>&1; then
  emit_diagnostic \
    "coverage.bun-test" \
    "pkg/ts-bun-client/build.ts" \
    "bun run build failed; dist/ artifacts are required by the test suite" \
    "Fix build errors in pkg/ts-bun-client before retrying."
  exit 1
fi

if ! bun test 2>&1; then
  emit_diagnostic \
    "coverage.bun-test" \
    "pkg/ts-bun-client" \
    "bun test failed" \
    "Fix failing tests in pkg/ts-bun-client before retrying."
  exit 1
fi
