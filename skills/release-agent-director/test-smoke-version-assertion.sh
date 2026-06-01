#!/usr/bin/env bash
# test-smoke-version-assertion.sh — regression-anchor test for b.6oj.
#
# Bug (b.6oj): verify-installed-pkg.ts --smoke was a shape-only check; it
# never compared the binary's reported version against the expected release tag.
# A wrong-stamped binary (b.b3h ldflags regression or b.uys overwrite) would
# pass the smoke gate undetected.
#
# Fix (b.6oj Engineer):
#   1. runSmoke() reads EXPECTED_VERSION and throws
#      "verify-installed-pkg --smoke: version mismatch — got X, expected Y"
#      when client.version().version != EXPECTED_VERSION.
#   2. release.sh verify_phase passes EXPECTED_VERSION="$VERSION" into the
#      smoke invocation subshell (matching the npm package version stamped by
#      version-bump.ts, which version() returns via NPM_PACKAGE_VERSION).
#
# This test exercises the new value-assertion directly via the dev-mode env
# vars (AD_VERIFY_AGAINST + AD_CLI_PATH) to avoid the heavy tarball-pack
# pipeline. Two scenarios:
#
#   PART 1 — match passes:
#     Read the npm package version (what client.version() returns via
#     NPM_PACKAGE_VERSION). Set EXPECTED_VERSION to that value. Run smoke.
#     Confirm exit 0.
#
#   PART 2 — mismatch fails:
#     Reuse the same binary. Set EXPECTED_VERSION to a synthetic value that
#     can never match. Run smoke. Confirm non-zero exit AND the literal
#     substring "version mismatch" in stderr.
#
# Usage (from any directory inside the repository):
#   bash skills/release-agent-director/test-smoke-version-assertion.sh
#
# Exit codes:
#   0  all assertions passed
#   1  assertion failure

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(git -C "$SCRIPT_DIR" rev-parse --show-toplevel)"

AD_INDEX_TS="$REPO_ROOT/pkg/ts-bun-client/src/index.ts"
VERIFY_SCRIPT="$REPO_ROOT/pkg/ts-bun-client/scripts/verify-installed-pkg.ts"
DEV_BINARY="$REPO_ROOT/bin/agent-director"
PKG_JSON="$REPO_ROOT/pkg/ts-bun-client/package.json"

MISMATCH_VERSION="v999.888.777"

# ====================================================================
# Build step: ensure dev binary exists
# ====================================================================
printf '\n[setup] building dev binary via make agent-director\n'
(cd "$REPO_ROOT" && make agent-director) \
    > >(while IFS= read -r l; do printf '[make] %s\n' "$l"; done)

if [[ ! -x "$DEV_BINARY" ]]; then
    printf 'ASSERT-FAIL: dev binary not produced at %s\n' "$DEV_BINARY" >&2
    exit 1
fi

# Read the npm package version — this is what client.version() returns via
# NPM_PACKAGE_VERSION (b.6o1). In the production packed tarball, this is
# stamped to the release tag by version-bump.ts (e.g. "0.6.3" for v0.6.3).
# In the dev tree it is "0.0.0".
NPM_VERSION="$(jq -r '.version' "$PKG_JSON")"
printf '[setup] npm package version (what client.version() returns): %s\n' "$NPM_VERSION"

if [[ -z "$NPM_VERSION" || "$NPM_VERSION" == "null" ]]; then
    printf 'ASSERT-FAIL: could not read .version from %s\n' "$PKG_JSON" >&2
    exit 1
fi

# Sanity: synthetic mismatch value must not equal the npm version.
if [[ "$NPM_VERSION" == "$MISMATCH_VERSION" ]]; then
    printf 'SELF-VALIDATION FAIL: npm version coincidentally equals synthetic mismatch value %s\n' \
        "$MISMATCH_VERSION" >&2
    exit 1
fi

# ====================================================================
# PART 1 — match passes: EXPECTED_VERSION == client.version().version → exit 0
# ====================================================================
printf '\n=== Part 1: smoke passes when EXPECTED_VERSION matches client.version() ===\n'

part1_exit=0
part1_output="$(
    AD_VERIFY_AGAINST="$AD_INDEX_TS" \
    AD_CLI_PATH="$DEV_BINARY" \
    EXPECTED_VERSION="$NPM_VERSION" \
    bun run "$VERIFY_SCRIPT" --smoke 2>&1
)" || part1_exit=$?

if [[ "$part1_exit" -eq 0 ]]; then
    printf '[part1] smoke exited 0  OK\n'
    printf '[part1] output: %s\n' "$part1_output"
else
    printf 'ASSERT-FAIL [part1]: smoke exited %d (expected 0)\n' "$part1_exit" >&2
    printf 'ASSERT-FAIL [part1]: output: %s\n' "$part1_output" >&2
    exit 1
fi

printf '\nPart 1 PASSED — smoke exits 0 when EXPECTED_VERSION matches client.version()\n'

# ====================================================================
# PART 2 — mismatch fails: EXPECTED_VERSION != client.version().version
#           → non-zero exit AND "version mismatch" in stderr
# ====================================================================
printf '\n=== Part 2: smoke fails when EXPECTED_VERSION does not match client.version() ===\n'

part2_stderr_file="$(mktemp "${TMPDIR:-/tmp}/smoke-stderr.XXXXXX")"
trap 'rm -f "$part2_stderr_file"' EXIT

part2_exit=0
AD_VERIFY_AGAINST="$AD_INDEX_TS" \
AD_CLI_PATH="$DEV_BINARY" \
EXPECTED_VERSION="$MISMATCH_VERSION" \
    bun run "$VERIFY_SCRIPT" --smoke >/dev/null 2>"$part2_stderr_file" || part2_exit=$?
part2_stderr="$(cat "$part2_stderr_file")"

if [[ "$part2_exit" -ne 0 ]]; then
    printf '[part2] smoke exited non-zero (%d)  OK\n' "$part2_exit"
else
    printf 'ASSERT-FAIL [part2]: smoke exited 0 (expected non-zero)\n' >&2
    printf 'ASSERT-FAIL [part2]: npm version=%s mismatch=%s — EXPECTED_VERSION propagation broken\n' \
        "$NPM_VERSION" "$MISMATCH_VERSION" >&2
    exit 1
fi

if printf '%s' "$part2_stderr" | grep -qF "version mismatch"; then
    printf '[part2] stderr contains "version mismatch"  OK\n'
    printf '[part2] stderr: %s\n' "$part2_stderr"
else
    printf 'ASSERT-FAIL [part2]: "version mismatch" not found in stderr\n' >&2
    printf 'ASSERT-FAIL [part2]: stderr was: %s\n' "$part2_stderr" >&2
    exit 1
fi

printf '\nPart 2 PASSED — smoke correctly rejects mismatched EXPECTED_VERSION\n'

# ====================================================================
# Summary
# ====================================================================
printf '\n=== test-smoke-version-assertion: ALL CHECKS PASSED ===\n'
printf 'Regression coverage confirmed: EXPECTED_VERSION check catches wrong-stamped binaries.\n'
printf 'Covers b.b3h (wrong ldflags) and b.uys (overwrite) as upstream failure modes.\n'
