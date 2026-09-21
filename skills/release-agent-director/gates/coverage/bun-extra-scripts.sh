#!/usr/bin/env bash
# gate:        coverage.bun-script-<name>
# checks:      extra bun scripts (smoke, envelope-diff, test:*) in pkg/ts-bun-client
# pass:        silent exit 0 (or prints "no extra scripts found" and exits 0 if none)
# fail:        emit one SR-14 JSON diagnostic per failing script to stderr, exit 1
#
# Usage: bun-extra-scripts.sh [worktree-root]
#   worktree-root defaults to the repo root (parent of this script's gate tree)

set -uo pipefail

GATE_LIB="$(cd "$(dirname "$0")/../lib" && pwd)"
# shellcheck source=../lib/emit-diagnostic.sh
source "${GATE_LIB}/emit-diagnostic.sh"

WORKTREE_ROOT="${1:-$(cd "$(dirname "$0")/../../../.." && pwd)}"
PKG_DIR="${WORKTREE_ROOT}/pkg/ts-bun-client"

if [ ! -d "$PKG_DIR" ]; then
  emit_diagnostic \
    "coverage.bun-extra-scripts" \
    "pkg/ts-bun-client" \
    "pkg/ts-bun-client directory not found at ${PKG_DIR}" \
    "Verify worktree root is the repo root, or pass the correct path as \$1."
  exit 1
fi

cd "$PKG_DIR" || { printf 'bun-extra-scripts.sh: cannot cd into package dir: %s\n' "$PKG_DIR" >&2; exit 1; }

# ── Per-gate scratch HOME (b.3jn) ──────────────────────────────────────────
# The five coverage gates run concurrently inside one sandbox container, all
# sharing HOME=/home/sandbox. The bun tests here (envelope-diff's REAL_HOME
# design: the FFI worker resolves os.UserHomeDir() from $HOME at spawn) write
# $HOME-resolved paths such as ~/.agent-director/ad-trail.jsonl. Sibling gate
# coverage.go-root runs test/smoke/go, whose TestMain canary snapshots the real
# ~/.agent-director via user.Current() (immune to $HOME) and fails the suite on
# ANY modification. So give this bun gate its own scratch HOME. Pin the go/bun
# caches to their real (HOME-derived) locations FIRST, before HOME is moved, so
# isolation does not cost warm-cache time.
export GOCACHE="${GOCACHE:-$(go env GOCACHE)}"
export GOMODCACHE="${GOMODCACHE:-$(go env GOMODCACHE)}"
export GOPATH="${GOPATH:-$(go env GOPATH)}"
export BUN_INSTALL_CACHE_DIR="${BUN_INSTALL_CACHE_DIR:-$HOME/.bun/install/cache}"
GATE_HOME="$(mktemp -d)"
export HOME="$GATE_HOME"
trap 'rm -rf "$GATE_HOME"' EXIT

# Discover extra scripts matching the gate pattern
mapfile -t SCRIPTS < <(
  jq -r '.scripts | keys[]' package.json 2>/dev/null \
    | grep -E '^(smoke|envelope-diff|test:.*)$' \
    || true
)

if [ ${#SCRIPTS[@]} -eq 0 ]; then
  echo "no extra scripts found" >&2
  exit 0
fi

FAILED=0

for script in "${SCRIPTS[@]}"; do
  if ! bun run "$script" 2>&1; then
    emit_diagnostic \
      "coverage.bun-script-${script}" \
      "pkg/ts-bun-client" \
      "bun run ${script} failed" \
      "Fix the '${script}' script in pkg/ts-bun-client before retrying."
    FAILED=1
  fi
done

exit "$FAILED"
