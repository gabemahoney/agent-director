#!/usr/bin/env bash
# test-release-postconditions.sh — post-dry-run assertion harness for release.sh
#
# Design note: This harness uses `git worktree add --detach` rather than
# the scratch-branch-commit-and-restore dance required by earlier T4A/T4B
# tests.  A detached worktree is a clean checkout by construction —
# release.sh's preflight (which requires a clean tree) passes without any
# staging dance, and the temp worktree is isolated from the operator's live
# checkout even if T4A regresses.  `--detach` is always used so the harness
# works whether the caller is on a branch or already inside another worktree.
# A detached HEAD reports "HEAD" for `git rev-parse --abbrev-ref HEAD`, so
# we pass `--branch HEAD` to release.sh to satisfy its branch-name check.
#
# Usage (from any directory inside the repository):
#   bash skills/release-agent-director/test-release-postconditions.sh
#
# Exit codes:
#   0  all four postconditions passed
#   1  one or more postconditions failed (ASSERT-FAIL line printed to stderr)

set -euo pipefail

# --------------------------------------------------------------------
# Resolve repo root (works when called from any CWD)
# --------------------------------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(git -C "$SCRIPT_DIR" rev-parse --show-toplevel)"

# --------------------------------------------------------------------
# Temp worktree + cleanup trap
# --------------------------------------------------------------------
worktree_dir=""

cleanup() {
    if [[ -n "$worktree_dir" && -d "$worktree_dir" ]]; then
        git -C "$REPO_ROOT" worktree remove --force "$worktree_dir" 2>/dev/null || true
    fi
}

trap cleanup EXIT

# mktemp -d creates the directory; git worktree add requires a non-existent
# path, so we rmdir it (it's empty) and let git recreate it.
worktree_dir="$(mktemp -d "${TMPDIR:-/tmp}/release-smoke.XXXXXX")"
rmdir "$worktree_dir"
git -C "$REPO_ROOT" worktree add --detach "$worktree_dir"

# --------------------------------------------------------------------
# Prime the worktree's bun environment
# --------------------------------------------------------------------
# A detached worktree has no untracked build artifacts.  The verify phase in
# release.sh resolves "@agent-director/linux-x64" via bun's module resolution
# starting from the smoke script's location inside the worktree, so the
# platform binary must exist in the worktree's node_modules at install time.
# Bun hard-links file: package contents; if the binary is absent when
# `bun install` runs, no hard link is created and the verify smoke fails.
#
# Fix: pre-build the Go binaries inside the worktree and stage them to
# platforms/*/bin/ so that `bun install` can hard-link them.  The full
# release.sh pipeline (including its own build phase) then runs without
# --no-build, rebuilding and re-staging over these pre-built artifacts.
# The hard link in node_modules points to the pre-staged inode, which
# retains valid binary content after release.sh's cp replaces the
# platforms/ path with a new inode.
#
# All of node_modules/ dist/ and platforms/*/bin/ are gitignored, so
# none of this setup affects the clean-tree or mode-bit postconditions.
(cd "$worktree_dir" && make release-binaries)

# Stage CLI binaries into per-platform npm sub-packages.
# Mirrors CLI_PLATFORMS in release.sh; update both if the mapping changes.
for mapping in "linux-amd64=linux-x64" "darwin-arm64=darwin-arm64"; do
    cross="${mapping%=*}"
    npm_subdir="${mapping#*=}"
    mkdir -p "$worktree_dir/pkg/ts-bun-client/platforms/$npm_subdir/bin"
    cp "$worktree_dir/dist/agent-director-${cross}" \
       "$worktree_dir/pkg/ts-bun-client/platforms/$npm_subdir/bin/agent-director"
    chmod 0755 \
       "$worktree_dir/pkg/ts-bun-client/platforms/$npm_subdir/bin/agent-director"
done

(cd "$worktree_dir/pkg/ts-bun-client" \
    && bun install --no-progress --frozen-lockfile \
    && bun run build)

# --------------------------------------------------------------------
# Run the dry-run release from inside the worktree
# --------------------------------------------------------------------
# --branch HEAD satisfies preflight's branch-name check in a detached
# worktree (git rev-parse --abbrev-ref HEAD returns "HEAD" when detached).
(cd "$worktree_dir" && \
    ./skills/release-agent-director/release.sh v0.0.0 --dry-run --branch HEAD)

# --------------------------------------------------------------------
# Postcondition assertions
# --------------------------------------------------------------------

# 1. Clean-tree: git status --porcelain must be empty
status_output="$(git -C "$worktree_dir" status --porcelain)"
if [[ -n "$status_output" ]]; then
    printf 'ASSERT-FAIL: clean-tree assertion: %s\n' "$status_output" >&2
    exit 1
fi

# 2. Mode-bit: git diff --raw HEAD must show no mode-flip entries
mode_flip_output="$(git -C "$worktree_dir" diff --raw HEAD | grep -E '^:100755 100644|^:100644 100755' || true)"
if [[ -n "$mode_flip_output" ]]; then
    printf 'ASSERT-FAIL: mode-bit assertion: %s\n' "$mode_flip_output" >&2
    exit 1
fi

# 3. Release notes: dist/release-notes.md must exist and be non-empty
if [[ ! -s "$worktree_dir/dist/release-notes.md" ]]; then
    printf 'ASSERT-FAIL: dist/release-notes.md assertion: file missing or empty\n' >&2
    exit 1
fi

# 4. No tarball: no .tgz file under pkg/ts-bun-client/
tgz_found="$(find "$worktree_dir/pkg/ts-bun-client" -name '*.tgz' 2>/dev/null || true)"
if [[ -n "$tgz_found" ]]; then
    printf 'ASSERT-FAIL: no-tgz assertion: %s\n' "$tgz_found" >&2
    exit 1
fi

printf '[smoke] all four postconditions passed\n'
