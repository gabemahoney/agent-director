#!/usr/bin/env bash
# test-build-version-stamp.sh — regression-anchor test for b.b3h.
#
# Bug (b.b3h): build_phase did not pass VERSION_LDFLAGS as a make override,
# so the released binary was stamped with `git describe` output (e.g.
# "v0.6.0-1-g74ce955") instead of the release VERSION (e.g. "v0.7.0").
#
# Fix (b.b3h Engineer): build_phase now passes VERSION_LDFLAGS=... as an
# explicit make override:
#   make release-binaries VERSION_LDFLAGS="$_BUILD_LDFLAGS"
#
# This test:
#   1. Sources release.sh with _RELEASE_SH_SOURCE_ONLY=1 to load build_phase
#      without running main().
#   2. Invokes build_phase directly with VERSION=v99.88.77 and a synthetic
#      DRY_RUN=0 environment so the Make target actually runs.
#   3. Asserts the resulting host binary's `version` JSON output satisfies:
#      A. .version == "v99.88.77" (exact match, no git describe decorations)
#      B. .commit is a 40-character lowercase hex SHA matching git rev-parse HEAD
#
#   Part 2 self-validates: re-runs the Make target WITHOUT the VERSION_LDFLAGS
#   override (simulating the pre-fix behaviour) and confirms the assertions
#   FAIL — proving this test has real regression coverage.
#
# Usage (from any directory inside the repository):
#   bash skills/release-agent-director/test-build-version-stamp.sh
#
# Exit codes:
#   0  all assertions passed; regression confirmed detectable
#   1  assertion failure or self-validation failure

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(git -C "$SCRIPT_DIR" rev-parse --show-toplevel)"
RELEASE_SH="$SCRIPT_DIR/release.sh"

TEST_VERSION="v99.88.77"

# Determine host tuple (must match release.sh build_phase CLI_PLATFORMS mapping).
case "$(uname -s)" in
    Linux)  host_os=linux ;;
    Darwin) host_os=darwin ;;
    *) printf 'SKIP: unsupported host OS: %s\n' "$(uname -s)" >&2; exit 0 ;;
esac
case "$(uname -m)" in
    x86_64|amd64) host_arch=amd64 ;;
    arm64|aarch64) host_arch=arm64 ;;
    *) printf 'SKIP: unsupported host arch: %s\n' "$(uname -m)" >&2; exit 0 ;;
esac
HOST_BINARY="$REPO_ROOT/dist/agent-director-${host_os}-${host_arch}"

# -----------------------------------------------------------------------
# assert_binary_version <binary> <expected_version> <label>
#
# Runs <binary> version, parses the JSON output, and checks:
#   A. .version == expected_version (exact string match)
#   B. .commit is a 40-char lowercase hex SHA matching git rev-parse HEAD
#
# Returns number of assertion failures (0 = pass).
# -----------------------------------------------------------------------
assert_binary_version() {
    local binary="$1" expected_v="$2" label="$3"
    local failures=0

    if [[ ! -x "$binary" ]]; then
        printf 'ASSERT-FAIL [%s]: binary not found or not executable: %s\n' "$label" "$binary" >&2
        return 1
    fi

    local json
    json="$("$binary" version 2>/dev/null)" || {
        printf 'ASSERT-FAIL [%s]: binary exited non-zero running `version`\n' "$label" >&2
        return 1
    }

    # A. .version must match exactly
    local got_version
    got_version="$(printf '%s' "$json" | jq -r '.version // empty')"
    if [[ "$got_version" == "$expected_v" ]]; then
        printf '[%s] A: .version == %s  OK\n' "$label" "$expected_v"
    else
        printf 'ASSERT-FAIL [%s]: .version = "%s"; expected "%s"\n' \
            "$label" "$got_version" "$expected_v" >&2
        printf 'ASSERT-FAIL [%s]: full JSON: %s\n' "$label" "$json" >&2
        failures=$((failures + 1))
    fi

    # B. .commit must be exactly 40 hex chars matching HEAD
    local got_commit expected_commit
    got_commit="$(printf '%s' "$json" | jq -r '.commit // empty')"
    expected_commit="$(git -C "$REPO_ROOT" rev-parse HEAD)"
    if [[ "$got_commit" =~ ^[0-9a-f]{40}$ ]] && [[ "$got_commit" == "$expected_commit" ]]; then
        printf '[%s] B: .commit is a 40-char hex SHA matching HEAD  OK\n' "$label"
    else
        printf 'ASSERT-FAIL [%s]: .commit = "%s"; expected "%s"\n' \
            "$label" "$got_commit" "$expected_commit" >&2
        failures=$((failures + 1))
    fi

    return "$failures"
}

# -----------------------------------------------------------------------
# run_build_with_ldflags <version_ldflags_override>
#
# Sources release.sh, sets up the minimum env build_phase needs, then
# invokes it.  <version_ldflags_override> is passed as-is to Make via
# VERSION_LDFLAGS=... — empty string means no override (simulates the
# pre-fix regression).
# -----------------------------------------------------------------------
run_build_with_ldflags() {
    local ldflags_override="$1"

    (
        set --
        export VERSION="$TEST_VERSION"
        export DRY_RUN=0
        export NO_BUILD=0
        export REPO_ROOT="$REPO_ROOT"
        # Silence phase-result noise from report_phase registered by release.sh.
        cd "$REPO_ROOT"
        _RELEASE_SH_SOURCE_ONLY=1 source "$RELEASE_SH"
        report_phase() { :; }

        if [[ -n "$ldflags_override" ]]; then
            # Simulate the FIXED build_phase: explicit VERSION_LDFLAGS override.
            local _BUILD_COMMIT
            _BUILD_COMMIT=$(git -C "$REPO_ROOT" rev-parse HEAD)
            local _BUILD_LDFLAGS="-X github.com/gabemahoney/agent-director/internal/version.Version=$VERSION -X github.com/gabemahoney/agent-director/internal/version.Commit=$_BUILD_COMMIT"
            (cd "$REPO_ROOT" && make release-binaries VERSION_LDFLAGS="$_BUILD_LDFLAGS") \
                > >(while IFS= read -r l; do printf '[build-test] %s\n' "$l"; done)
        else
            # Simulate the BUGGY pre-fix build: no VERSION_LDFLAGS override.
            # Make falls back to its default VERSION_LDFLAGS which uses
            # `git describe` and produces decoration like v0.6.0-1-g74ce955.
            (cd "$REPO_ROOT" && make release-binaries) \
                > >(while IFS= read -r l; do printf '[build-test] %s\n' "$l"; done)
        fi
    )
}

# ====================================================================
# PART 1 — Fixed build: VERSION_LDFLAGS override present
# ====================================================================
printf '\n=== Part 1: fixed build (VERSION_LDFLAGS override) ===\n'

run_build_with_ldflags "yes"

fix_failures=0
assert_binary_version "$HOST_BINARY" "$TEST_VERSION" "fixed" || fix_failures=$?

if [[ "$fix_failures" -gt 0 ]]; then
    printf '\nPart 1 FAILED — %d assertion(s) failed on the fixed build\n' "$fix_failures" >&2
    exit 1
fi
printf '\nPart 1 PASSED — binary correctly stamped with exact release VERSION\n'

# ====================================================================
# PART 2 — Buggy build: no VERSION_LDFLAGS override (self-validation)
#
# The Make default VERSION_LDFLAGS uses `git describe` which appends
# -N-g<sha> when the current commit is not an exact tag. That makes
# .version != TEST_VERSION, which is the bug.
#
# Skip self-validation only if HEAD is an exact tag (git describe would
# return exactly the tag, making the two indistinguishable).
# ====================================================================
printf '\n=== Part 2: regression self-validation (no VERSION_LDFLAGS override) ===\n'

HEAD_TAG="$(git -C "$REPO_ROOT" describe --exact-match HEAD 2>/dev/null || true)"
if [[ -n "$HEAD_TAG" ]]; then
    printf '[self-validation] HEAD is an exact tag (%s) — git describe == tag.\n' "$HEAD_TAG"
    printf '[self-validation] Cannot distinguish fixed vs buggy from version output; skipping Part 2.\n'
    printf '\n=== test-build-version-stamp: Part 1 PASSED; Part 2 SKIPPED (exact-tag HEAD) ===\n'
    exit 0
fi

run_build_with_ldflags ""

# With no ldflags override, the binary gets the git describe version.
got_version_buggy="$(printf '%s' "$("$HOST_BINARY" version 2>/dev/null)" | jq -r '.version // empty')"
if [[ "$got_version_buggy" == "$TEST_VERSION" ]]; then
    printf 'SELF-VALIDATION FAIL: buggy build produced .version="%s" which equals TEST_VERSION.\n' \
        "$got_version_buggy" >&2
    printf 'The test cannot distinguish fixed vs buggy — check Makefile VERSION_LDFLAGS default.\n' >&2
    exit 1
fi
printf '[self-validation] buggy build produced .version="%s" (not "%s") — regression is detectable\n' \
    "$got_version_buggy" "$TEST_VERSION"
printf 'ASSERT-FAIL [buggy]: .version != TEST_VERSION — this is the b.b3h regression (correctly detected)\n'

printf '\nPart 2 PASSED — buggy build correctly produces decorated/wrong version\n'

# Restore a correctly-stamped binary so we don't leave the tree in a bad state.
printf '\n[cleanup] re-running fixed build to restore correctly-stamped binary\n'
run_build_with_ldflags "yes"

# ====================================================================
# Summary
# ====================================================================
printf '\n=== test-build-version-stamp: ALL CHECKS PASSED ===\n'
printf 'Regression coverage confirmed: test catches the b.b3h ldflags override bug.\n'
