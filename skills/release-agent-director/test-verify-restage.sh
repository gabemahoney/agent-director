#!/usr/bin/env bash
# test-verify-restage.sh — regression-anchor test for b.uys.
#
# Bug (b.uys): release.sh build_phase correctly stamps dist/ binaries with the
# release VERSION (via VERSION_LDFLAGS, fixed in b.b3h). However, between
# build_phase and the tarball pack, verify_phase runs `bun test` which
# invokes pkg/ts-bun-client/test/setup.ts. setup.ts calls `make agent-director`
# (a dev build with no ldflags) and copies the resulting bin/agent-director
# into pkg/ts-bun-client/platforms/<host>/bin/agent-director — silently
# overwriting the release-stamped binary that build_phase staged. The wrong
# (git-describe-decorated) binary gets packed into the npm tarball.
#
# Fix (b.uys Engineer): verify_phase now runs a "step 3.5/4" that calls
# stage_cli_into_platforms again after `bun test` and before the tarball pack,
# restoring the release-stamped binary into platforms/.
#
# This test:
#   PART 1 — overwrite is detectable (the regression itself):
#     1. Sources release.sh with _RELEASE_SH_SOURCE_ONLY=1 to load build_phase
#        and stage_cli_into_platforms without running main().
#     2. Runs build_phase with VERSION=v99.88.77 so it builds + stages with the
#        synthetic release stamp.
#     3. Asserts the staged platforms/<host>/bin/agent-director reports
#        .version == "v99.88.77".
#     4. Simulates the bun test overwrite: runs `make agent-director` (dev build)
#        and copies bin/agent-director over the staged path, exactly as setup.ts
#        does (without actually running `bun test` which would be too heavy).
#     5. Asserts the staged binary now reports .version != "v99.88.77" (it should
#        carry the git-describe decoration). If they happen to be equal (impossible
#        since v99.88.77 is a synthetic tag that does not exist) — SELF-VALIDATION
#        FAIL.
#
#   PART 2 — the fix restores the staged binary:
#     6. Re-invokes stage_cli_into_platforms (the fix in release.sh step 3.5/4).
#     7. Re-asserts the staged binary now reports .version == "v99.88.77" again.
#
# Cleanup: leaves the tree with a v99.88.77-stamped binary in platforms/ (same
# stamp build_phase produced). This is coherent — the staged binary matches
# dist/. A subsequent real release run will overwrite with the actual VERSION.
#
# Usage (from any directory inside the repository):
#   bash skills/release-agent-director/test-verify-restage.sh
#
# Exit codes:
#   0  all assertions passed; regression confirmed detectable and fix confirmed
#   1  assertion failure or self-validation failure

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(git -C "$SCRIPT_DIR" rev-parse --show-toplevel)"
RELEASE_SH="$SCRIPT_DIR/release.sh"

TEST_VERSION="v99.88.77"

# Determine host tuple (must match CLI_PLATFORMS in lib/stage-cli.sh).
# stage-cli.sh uses cross-compile tuples (linux-amd64, darwin-arm64) for dist/
# but npm sub-package dirs (linux-x64, darwin-arm64) for platforms/.
case "$(uname -s)" in
    Linux)  host_os=linux  ;;
    Darwin) host_os=darwin ;;
    *) printf 'SKIP: unsupported host OS: %s\n' "$(uname -s)" >&2; exit 0 ;;
esac
case "$(uname -m)" in
    x86_64|amd64) host_arch=amd64; host_npm_arch=x64 ;;
    arm64|aarch64) host_arch=arm64; host_npm_arch=arm64 ;;
    *) printf 'SKIP: unsupported host arch: %s\n' "$(uname -m)" >&2; exit 0 ;;
esac

# Only supported on linux-amd64 and darwin-arm64 (matches CLI_PLATFORMS).
if [[ "$host_os" == "linux" && "$host_arch" != "amd64" ]] || \
   [[ "$host_os" == "darwin" && "$host_arch" != "arm64" ]]; then
    printf 'SKIP: no CLI_PLATFORMS entry for %s-%s\n' "$host_os" "$host_arch" >&2
    exit 0
fi

HOST_NPM_SUBDIR="${host_os}-${host_npm_arch}"
STAGED_BINARY="$REPO_ROOT/pkg/ts-bun-client/platforms/$HOST_NPM_SUBDIR/bin/agent-director"
DEV_BINARY="$REPO_ROOT/bin/agent-director"

# -----------------------------------------------------------------------
# assert_staged_version <expected_version> <label>
#
# Runs the staged platforms/<host>/bin/agent-director, parses JSON, and
# checks .version == expected_version (exact string match).
#
# Returns number of assertion failures (0 = pass).
# -----------------------------------------------------------------------
assert_staged_version() {
    local expected_v="$1" label="$2"
    local failures=0

    if [[ ! -x "$STAGED_BINARY" ]]; then
        printf 'ASSERT-FAIL [%s]: staged binary not found or not executable: %s\n' \
            "$label" "$STAGED_BINARY" >&2
        return 1
    fi

    local json
    json="$("$STAGED_BINARY" version 2>/dev/null)" || {
        printf 'ASSERT-FAIL [%s]: staged binary exited non-zero running `version`\n' "$label" >&2
        return 1
    }

    local got_version
    got_version="$(printf '%s' "$json" | jq -r '.version // empty')"
    if [[ "$got_version" == "$expected_v" ]]; then
        printf '[%s]: .version == %s  OK\n' "$label" "$expected_v"
    else
        printf 'ASSERT-FAIL [%s]: .version = "%s"; expected "%s"\n' \
            "$label" "$got_version" "$expected_v" >&2
        printf 'ASSERT-FAIL [%s]: full JSON: %s\n' "$label" "$json" >&2
        failures=$((failures + 1))
    fi

    return "$failures"
}

# -----------------------------------------------------------------------
# assert_staged_version_ne <unexpected_version> <label>
#
# Asserts staged binary .version is NOT equal to unexpected_version.
# Returns number of assertion failures (0 = pass).
# -----------------------------------------------------------------------
assert_staged_version_ne() {
    local unexpected_v="$1" label="$2"

    if [[ ! -x "$STAGED_BINARY" ]]; then
        printf 'ASSERT-FAIL [%s]: staged binary not found or not executable: %s\n' \
            "$label" "$STAGED_BINARY" >&2
        return 1
    fi

    local json
    json="$("$STAGED_BINARY" version 2>/dev/null)" || {
        printf 'ASSERT-FAIL [%s]: staged binary exited non-zero running `version`\n' "$label" >&2
        return 1
    }

    local got_version
    got_version="$(printf '%s' "$json" | jq -r '.version // empty')"
    if [[ "$got_version" != "$unexpected_v" ]]; then
        printf '[%s]: .version = "%s" (not "%s" — overwrite confirmed)  OK\n' \
            "$label" "$got_version" "$unexpected_v"
        return 0
    else
        printf 'SELF-VALIDATION FAIL [%s]: staged binary .version="%s" equals unexpected_v="%s"\n' \
            "$label" "$got_version" "$unexpected_v" >&2
        printf 'This means the dev build is coincidentally stamped with the test version.\n' >&2
        printf 'Check that %s is not an exact git tag.\n' "$unexpected_v" >&2
        return 1
    fi
}

# ====================================================================
# PART 1 — overwrite is detectable (regression)
# ====================================================================
printf '\n=== Part 1: build_phase stages release-stamped binary ===\n'

# Source release.sh in library mode to get build_phase + stage_cli_into_platforms.
(
    set --
    export VERSION="$TEST_VERSION"
    export DRY_RUN=0
    export NO_BUILD=0
    export REPO_ROOT="$REPO_ROOT"
    cd "$REPO_ROOT"
    _RELEASE_SH_SOURCE_ONLY=1 source "$RELEASE_SH"
    # Silence phase-result noise from report_phase registered by release.sh.
    report_phase() { :; }

    printf '[part1] running build_phase VERSION=%s\n' "$VERSION"
    build_phase > >(while IFS= read -r l; do printf '[build] %s\n' "$l"; done)
)

part1_failures=0
assert_staged_version "$TEST_VERSION" "after-build_phase" || part1_failures=$?

if [[ "$part1_failures" -gt 0 ]]; then
    printf '\nPart 1 FAILED — staged binary not stamped with %s after build_phase\n' \
        "$TEST_VERSION" >&2
    exit 1
fi
printf '\nPart 1a PASSED — staged binary correctly stamped %s by build_phase\n' "$TEST_VERSION"

# ----------------------------------------------------------------
# Simulate the bun test setup.ts overwrite:
#   setup.ts does: make agent-director → copyFileSync(bin/agent-director, staged)
# We replicate only the overwrite, not the full test run.
# ----------------------------------------------------------------
printf '\n=== Part 1b: simulate bun test setup.ts overwrite ===\n'
printf '[overwrite] running: make -C %s agent-director\n' "$REPO_ROOT"
(cd "$REPO_ROOT" && make agent-director) \
    > >(while IFS= read -r l; do printf '[make] %s\n' "$l"; done)

if [[ ! -f "$DEV_BINARY" ]]; then
    printf 'ASSERT-FAIL: dev binary not produced at %s\n' "$DEV_BINARY" >&2
    exit 1
fi

# Mirror exactly what setup.ts does (copyFileSync + chmod 0o755).
cp "$DEV_BINARY" "$STAGED_BINARY"
chmod 0755 "$STAGED_BINARY"
printf '[overwrite] copied %s → %s\n' "$DEV_BINARY" "$STAGED_BINARY"

# Assert the staged binary is now overwritten with dev stamp (!= TEST_VERSION).
sv_failures=0
assert_staged_version_ne "$TEST_VERSION" "after-overwrite" || sv_failures=$?

if [[ "$sv_failures" -gt 0 ]]; then
    printf '\nSELF-VALIDATION FAIL — overwrite not detectable; cannot confirm regression coverage\n' >&2
    exit 1
fi
printf '\nPart 1b PASSED — overwrite confirmed; staged binary now carries dev (git-describe) version\n'

# ====================================================================
# PART 2 — the fix restores the staged binary
# ====================================================================
printf '\n=== Part 2: stage_cli_into_platforms restores release-stamped binary ===\n'

# Re-invoke stage_cli_into_platforms in isolation — this is exactly what
# release.sh verify_phase step 3.5/4 does after bun test completes.
(
    set --
    export VERSION="$TEST_VERSION"
    export DRY_RUN=0
    export NO_BUILD=0
    export REPO_ROOT="$REPO_ROOT"
    cd "$REPO_ROOT"
    _RELEASE_SH_SOURCE_ONLY=1 source "$RELEASE_SH"
    report_phase() { :; }

    printf '[part2] calling stage_cli_into_platforms\n'
    stage_cli_into_platforms > >(while IFS= read -r l; do printf '[stage] %s\n' "$l"; done)
)

part2_failures=0
assert_staged_version "$TEST_VERSION" "after-restage" || part2_failures=$?

if [[ "$part2_failures" -gt 0 ]]; then
    printf '\nPart 2 FAILED — staged binary not restored to %s after stage_cli_into_platforms\n' \
        "$TEST_VERSION" >&2
    exit 1
fi
printf '\nPart 2 PASSED — re-running stage_cli_into_platforms restores release-stamped binary\n'

# ====================================================================
# Cleanup note
#
# The tree is left with platforms/<host>/bin/agent-director stamped
# v99.88.77 (the test VERSION) and dist/ also stamped v99.88.77 —
# both produced by the same build_phase invocation, so they are
# consistent. A subsequent real release run will overwrite both with
# the actual release VERSION.
# ====================================================================

# ====================================================================
# Summary
# ====================================================================
printf '\n=== test-verify-restage: ALL CHECKS PASSED ===\n'
printf 'Regression coverage confirmed: test catches the b.uys platforms/ overwrite bug.\n'
printf 'Fix confirmed: stage_cli_into_platforms correctly restores the release-stamped binary.\n'
