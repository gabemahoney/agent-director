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

# ── dist-pack lock: serialize against go-root's dist/-sensitive tests (b.3jn) ──
# Under go-root's parallel phase, several synthetic-regression tests contend with
# this gate over pkg/ts-bun-client's test sources AND dist/ artifacts:
#
#   • coverage-bun-test-fires appends `expect(1).toBe(2)` to
#     pkg/ts-bun-client/test/setup.test.ts, reruns THIS gate nested to prove the
#     SR-14 diagnostic fires, then restores the file via t.Cleanup. During that
#     mutation window this gate must not observe the planted failure.
#   • the pack-first tests (tarball-round-trip, tarball-coherence-drift,
#     pack-first-version-mismatch, verify-restage) read/pack pkg/ts-bun-client/
#     dist/. This gate's `bun run build` REWRITES dist/, which can race their
#     packs (the b.aur failure mode).
#
# All of those tests guard with an EXCLUSIVE flock on this path
# (acquireDistPackLock). The gate historically took no lock, so it read the
# planted failure and raced the packs. Acquire the SAME lock here, EXCLUSIVE,
# for the gate's whole lifetime via a dedicated fd — EXCLUSIVE (not shared)
# because this gate is a dist/ WRITER, not merely a reader, so shared mode would
# still let its build race the pack tests' reads.
#
# The lock deliberately lives under ${TMPDIR:-/tmp}, NOT the scratch HOME above,
# to match Go's os.TempDir() resolution so both sides open the same file.
#
# COVERAGE_BUN_TEST_NESTED guard: the nested invocation from
# coverage-bun-test-fires already holds this lock (it took it before mutating
# setup.test.ts). Re-acquiring here would self-deadlock — the nested gate would
# block on its own ancestor's lock while the ancestor waits on the nested gate.
# So the nested run skips the lock. (Mirrors b.2y5's COVERAGE_GO_ROOT_NESTED
# precedent; coverage-bun-test-fires sets this env on its nested run.)
#
# Hold-time tradeoff: this gate holds the lock ~20-35s (install+build+test). The
# five lock-holding go-root tests may wait on it, stretching go-root's wall
# time. Acceptable: the SR-9 bound is relative to the longest gate, and dist/
# correctness beats a few seconds of go-root parallelism.
#
# fd 9 is released automatically at script exit.
if [ "${COVERAGE_BUN_TEST_NESTED:-0}" != "1" ]; then
  DIST_PACK_LOCK="${TMPDIR:-/tmp}/agent-director-ts-bun-dist-pack.lock"
  exec 9>"$DIST_PACK_LOCK"
  flock 9
fi

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
