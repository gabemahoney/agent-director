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

# The tree-write lock (see "tree-write lock" below). Resolved to an absolute
# path before the cd, because $1 may be a relative worktree root.
TREE_WRITE_LOCK="$(cd "$WORKTREE_ROOT" && pwd)/.tree-write.lock"

cd "$PKG_DIR"

# ── Per-gate scratch HOME (b.3jn) ──────────────────────────────────────────
# The five coverage gates run concurrently inside one sandbox container, all
# sharing HOME=/home/sandbox. The bun tests here write $HOME-resolved paths
# such as ~/.agent-director/ad-trail.jsonl (their CLI subprocesses inherit
# $HOME unless a test overrides it). Sibling gate coverage.go-root runs
# test/smoke/go, whose TestMain canary snapshots the real ~/.agent-director via
# user.Current() (immune to $HOME) and fails the suite on ANY modification. So
# give this bun gate its own scratch HOME. Pin the go/bun caches to their real
# (HOME-derived) locations FIRST, before HOME is moved, so isolation does not
# cost warm-cache time.
export GOCACHE="${GOCACHE:-$(go env GOCACHE)}"
export GOMODCACHE="${GOMODCACHE:-$(go env GOMODCACHE)}"
export GOPATH="${GOPATH:-$(go env GOPATH)}"
export BUN_INSTALL_CACHE_DIR="${BUN_INSTALL_CACHE_DIR:-$HOME/.bun/install/cache}"
GATE_HOME="$(mktemp -d)"
export HOME="$GATE_HOME"
trap 'rm -rf "$GATE_HOME"' EXIT

# ── dist-pack lock: serialize against go-root's pack-first tests (b.3jn) ──
# Under go-root's parallel phase, the pack-first synthetic-regression tests
# (tarball-round-trip, tarball-coherence-drift, pack-first-version-mismatch,
# verify-restage) read/pack pkg/ts-bun-client/dist/. This gate's `bun run build`
# REWRITES dist/, which can race their packs (the b.aur failure mode).
#
# Those tests guard with an EXCLUSIVE flock on this path (acquireDistPackLock).
# Acquire the SAME lock here, EXCLUSIVE, for the gate's whole lifetime via a
# dedicated fd — EXCLUSIVE (not shared) because this gate is a dist/ WRITER, not
# merely a reader, so shared mode would still let its build race the pack tests'
# reads.
#
# The lock deliberately lives under ${TMPDIR:-/tmp}, NOT the scratch HOME above,
# to match Go's os.TempDir() resolution so both sides open the same file.
#
# Every run takes the lock, so no caller may run this gate while holding it:
# the gate would wait on its own caller. (coverage-bun-test-fires runs this gate
# on a fixture package and holds no lock — b.jct.)
#
# Hold-time tradeoff: this gate holds the lock ~20-35s (install+build+test),
# plus any wait for the tree-write lock below. That wait can last until no
# coverage.docker-epic-* child holds the tree-write lock, because Linux flock
# does not favour a waiting exclusive locker. The pack-first tests and
# coverage-bun-test-fires' fixture run of this gate may wait on it, stretching
# go-root's wall time. Acceptable: the SR-9 bound is relative to the longest
# gate, and dist/ correctness beats go-root parallelism.
#
# fd 9 is released automatically at script exit.
DIST_PACK_LOCK="${TMPDIR:-/tmp}/agent-director-ts-bun-dist-pack.lock"
exec 9>"$DIST_PACK_LOCK"
flock 9

# ── tree-write lock: install and build write inside the repo tree (b.k42) ──
# `bun install` rewrites pkg/ts-bun-client/node_modules/ (and populates it from
# nothing in a fresh release worktree), and `bun run build` deletes and recreates
# pkg/ts-bun-client/dist/. Both are inside the repo root that each
# coverage.docker-epic-* child collects as its docker build context while it
# holds this lock SHARED (docker-epics.sh), so each runs under an EXCLUSIVE
# hold, one short hold per command like the test preload's `make` builds.
#
# `bun test` must NOT run under this hold: its preload (test/setup.ts) takes the
# same lock exclusive through its own `flock`, which would wait on this gate
# forever. Hence a per-command `flock <file> <cmd>`, not an fd held across the
# script like the dist-pack lock above.
#
# Lock order: dist-pack (fd 9, held for the whole run), then tree-write. The
# preload's builds nest the same way, because `bun test` inherits fd 9. Nothing
# takes the dist-pack lock while holding the tree-write lock, so the two cannot
# deadlock.
#
# Like `flock 9` above, this calls flock unconditionally.
if ! flock "$TREE_WRITE_LOCK" bun install --frozen-lockfile 2>&1; then
  emit_diagnostic \
    "coverage.bun-test" \
    "pkg/ts-bun-client/bun.lockb" \
    "bun install --frozen-lockfile failed" \
    "Run 'bun install' locally and commit the updated lockfile."
  exit 1
fi

export PATH="$PWD/node_modules/.bin:$PATH"

if ! flock "$TREE_WRITE_LOCK" bun run build 2>&1; then
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
