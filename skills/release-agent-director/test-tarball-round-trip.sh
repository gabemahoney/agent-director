#!/usr/bin/env bash
# test-tarball-round-trip.sh — Epic 4 regression anchor for the tarball
# SHA-256 round-trip check (t3.xsh.s7.3f.j7).
#
# Asserts that publish_phase re-hashes every tarball produced by verify_phase
# before npm publish fires, and aborts with a clear diagnostic when byte-level
# drift is detected.  Also asserts that publish_phase fails fast when the
# manifest env var is absent.
#
# Cases:
#   1. Happy:   staged tarballs + valid manifest → publish_phase exits 0;
#               SHA-256 gate ran (check-version-coherence in log); 3
#               "publishing ... from *.tgz" log lines confirm file-path
#               publishing (not directory).
#   2. Drift:   append 1 byte to umbrella tarball after manifest is written →
#               publish_phase exits non-zero; no "publishing*.tgz" log line
#               (gate fired before npm publish); log contains actual + expected hex.
#   3. Missing: unset AGENT_DIRECTOR_RELEASE_SHASUMS → publish_phase exits
#               non-zero; log names the missing env var.
#
# Setup design note:
#   The full verify_phase smoke step (step 2/4) resolves "agent-director" via
#   Bun's standard module lookup starting from the SCRIPT FILE's directory
#   ($REPO_ROOT/pkg/ts-bun-client/scripts/), landing on the live node_modules
#   which hold the "0.0.0" sentinel.  Running verify_phase with any test
#   VERSION other than "0.0.0" causes the smoke step to fail with a version
#   mismatch; using "0.0.0" causes the site-dist-no-inline coherence check to
#   fail.  The verify→publish handoff (tarballs + SHA-256 manifest) is already
#   covered by test-release-postconditions.sh.  This test focuses exclusively
#   on the publish-side round-trip gate (SR-1.3 / SR-1.5) by constructing the
#   verify_phase artifacts directly — packing real tarballs from the live
#   source tree and writing the manifest exactly as verify_phase does.
#
# Usage (from any directory inside the repository):
#   bash skills/release-agent-director/test-tarball-round-trip.sh
#
# Exit codes:
#   0  all assertions passed
#   1  assertion failure or setup failure

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(git -C "$SCRIPT_DIR" rev-parse --show-toplevel)"
RELEASE_SH="$SCRIPT_DIR/release.sh"

# "0.0.1" is distinct from the live sentinel "0.0.0" and will not trigger
# site-dist-no-inline (which blocks "0.0.0" in dist/index.js) since
# site-dist-no-inline only runs in --scope verify (not --scope publish).
TEST_VERSION="v0.0.1"
TEST_VERSION_PLAIN="0.0.1"

# --------------------------------------------------------------------
# Cleanup tracking
# --------------------------------------------------------------------
_STAGE_DIR=""
_SHASUMS_TMPFILE=""
_STAGEDIR_TMPFILE=""

cleanup() {
    [[ -n "$_STAGE_DIR"        && -d "$_STAGE_DIR"        ]] && rm -rf "$_STAGE_DIR"
    [[ -n "$_SHASUMS_TMPFILE"  && -f "$_SHASUMS_TMPFILE"  ]] && rm -f "$_SHASUMS_TMPFILE"
    [[ -n "$_STAGEDIR_TMPFILE" && -f "$_STAGEDIR_TMPFILE" ]] && rm -f "$_STAGEDIR_TMPFILE"
}
trap cleanup EXIT

_SHASUMS_TMPFILE="$(mktemp)"
_STAGEDIR_TMPFILE="$(mktemp)"

# Portable SHA-256: mirrors the _sha256() helper in release.sh.
_local_sha256() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | awk '{print $1}'
    else
        shasum -a 256 "$1" | awk '{print $1}'
    fi
}

# ====================================================================
# Setup: build verify_phase artifacts (tarballs + SHA-256 manifest)
#
# Mirrors the staging/packing sequence in verify_phase (release.sh) but
# skips the steps that require a running CLI binary (step 0/4 b.b3h
# anchor) and the smoke test (step 2/4) that triggers the module
# resolution issue described in the header comment.
#
# Stamp order:
#   1. umbrella-version + platform-version + skill-frontmatter (not opt-deps)
#      — leaves file: opt-dep paths so `bun install` resolves them locally
#   2. bun install (resolves file: deps from stage_dir/platforms/)
#   3. bun run build (embeds TEST_VERSION_PLAIN in dist/index.js)
#   4. stage-skill.ts (stages SKILL.md into dist/)
#   5. opt-deps target (stamps ^TEST_VERSION_PLAIN; must run after install)
#   6. bun pm pack --ignore-scripts × 3 (umbrella + two platform packs)
#   7. Write SHA-256 manifest (three-line coreutils format)
# ====================================================================
printf '\n=== Setup: staging tarballs + SHA-256 manifest ===\n'

_stage_dir="$(mktemp -d "${TMPDIR:-/tmp}/round-trip-stage.XXXXXX")"
_STAGE_DIR="$_stage_dir"

# Populate stage_dir with live ts-bun-client (includes updated scripts)
mkdir -p "$_stage_dir/pkg/ts-bun-client"
mkdir -p "$_stage_dir/skills"
cp -a "$REPO_ROOT/pkg/ts-bun-client/." "$_stage_dir/pkg/ts-bun-client/"
cp -a "$REPO_ROOT/skills/install-agent-director" "$_stage_dir/skills/"
mkdir -p "$_stage_dir/pkg/api/errnames"
cp "$REPO_ROOT/pkg/api/errnames/catalog.json" "$_stage_dir/pkg/api/errnames/catalog.json"
# Strip stray dev artifacts
rm -rf "$_stage_dir/pkg/ts-bun-client/node_modules"
rm -rf "$_stage_dir/pkg/ts-bun-client/skills"

# Dummy platform binaries — needed so bun pm pack produces non-empty tarballs.
# Must output {"version":"<TEST_VERSION>"} on stdout so check-version-coherence
# site-1 (publish scope) passes; a non-JSON stub causes site-1 to fail.
for _plat in linux-x64 darwin-arm64; do
    mkdir -p "$_stage_dir/pkg/ts-bun-client/platforms/$_plat/bin"
    # shellcheck disable=SC2016
    printf '#!/bin/sh\necho '"'"'{"version":"%s"}'"'"'\n' "$TEST_VERSION" \
        > "$_stage_dir/pkg/ts-bun-client/platforms/$_plat/bin/agent-director"
    chmod +x "$_stage_dir/pkg/ts-bun-client/platforms/$_plat/bin/agent-director"
done
unset _plat

# Step 1: stamp umbrella + platform versions + skill frontmatter
#         (skip opt-deps: bun install needs file: paths)
(cd "$_stage_dir/pkg/ts-bun-client" && bun run scripts/version-bump.ts \
    --version "$TEST_VERSION_PLAIN" \
    --target umbrella-version \
    --target platform-version \
    --target skill-frontmatter) \
    > >(while IFS= read -r l; do printf '[setup] %s\n' "$l"; done)

# Step 2: install devDeps (file: opt-dep paths resolve from stage_dir)
(cd "$_stage_dir/pkg/ts-bun-client" && bun install --no-progress >/dev/null 2>&1)
printf '[setup] bun install done\n'

# Step 3: build dist/ (embeds TEST_VERSION_PLAIN in bundle)
(cd "$_stage_dir/pkg/ts-bun-client" && bun run build >/dev/null 2>&1)
printf '[setup] bun run build done\n'

# Step 4: stage skill files into dist/
(cd "$_stage_dir/pkg/ts-bun-client" && bun run scripts/stage-skill.ts >/dev/null 2>&1)
printf '[setup] stage-skill done\n'

# Step 5: stamp opt-deps to ^TEST_VERSION_PLAIN (after bun install)
(cd "$_stage_dir/pkg/ts-bun-client" && bun run scripts/version-bump.ts \
    --version "$TEST_VERSION_PLAIN" \
    --target opt-deps) \
    > >(while IFS= read -r l; do printf '[setup] %s\n' "$l"; done)

# Step 6: pack umbrella
(cd "$_stage_dir/pkg/ts-bun-client" && bun pm pack --ignore-scripts >/dev/null 2>&1)
_tgz="$(ls "$_stage_dir/pkg/ts-bun-client/"agent-director-*.tgz 2>/dev/null | head -n 1)"
if [[ -z "$_tgz" || ! -f "$_tgz" ]]; then
    printf 'SETUP FAIL: umbrella tarball not produced\n' >&2; exit 1
fi
printf '[setup] packed umbrella: %s\n' "$_tgz"

# Pack linux-x64 platform sub-package
(cd "$_stage_dir/pkg/ts-bun-client/platforms/linux-x64" \
    && bun pm pack --ignore-scripts >/dev/null 2>&1)
_tgz_linux_x64="$(ls "$_stage_dir/pkg/ts-bun-client/platforms/linux-x64/"*.tgz 2>/dev/null | head -n 1)"
if [[ -z "$_tgz_linux_x64" || ! -f "$_tgz_linux_x64" ]]; then
    printf 'SETUP FAIL: linux-x64 tarball not produced\n' >&2; exit 1
fi
printf '[setup] packed linux-x64: %s\n' "$_tgz_linux_x64"

# Pack darwin-arm64 platform sub-package
(cd "$_stage_dir/pkg/ts-bun-client/platforms/darwin-arm64" \
    && bun pm pack --ignore-scripts >/dev/null 2>&1)
_tgz_darwin_arm64="$(ls "$_stage_dir/pkg/ts-bun-client/platforms/darwin-arm64/"*.tgz 2>/dev/null | head -n 1)"
if [[ -z "$_tgz_darwin_arm64" || ! -f "$_tgz_darwin_arm64" ]]; then
    printf 'SETUP FAIL: darwin-arm64 tarball not produced\n' >&2; exit 1
fi
printf '[setup] packed darwin-arm64: %s\n' "$_tgz_darwin_arm64"

# Step 7: write SHA-256 manifest (two-space coreutils format: <sha256>  <abs-path>)
_shasums_file="$_stage_dir/tarball-shasums.txt"
{
    printf '%s  %s\n' "$(_local_sha256 "$_tgz")"              "$_tgz"
    printf '%s  %s\n' "$(_local_sha256 "$_tgz_linux_x64")"   "$_tgz_linux_x64"
    printf '%s  %s\n' "$(_local_sha256 "$_tgz_darwin_arm64")" "$_tgz_darwin_arm64"
} > "$_shasums_file"

_manifest_lines="$(wc -l < "$_shasums_file" | tr -d ' ')"
if [[ "$_manifest_lines" -ne 3 ]]; then
    printf 'SETUP FAIL: manifest has %s line(s); expected 3\n' "$_manifest_lines" >&2
    cat "$_shasums_file" >&2; exit 1
fi
printf '[setup] manifest written (%s entries): %s\n' "$_manifest_lines" "$_shasums_file"

# Save artifact paths for parent-shell access
SHASUMS_PATH="$_shasums_file"
printf '%s\n' "$SHASUMS_PATH"  > "$_SHASUMS_TMPFILE"
printf '%s\n' "$_stage_dir"    > "$_STAGEDIR_TMPFILE"

# ====================================================================
# Helper: run_publish <with_shasums>
#
# Runs publish_phase in an isolated subshell against the staged artifacts.
#   with_shasums=yes  — AGENT_DIRECTOR_RELEASE_SHASUMS set to SHASUMS_PATH
#   with_shasums=no   — AGENT_DIRECTOR_RELEASE_SHASUMS left unset
#
# Sets globals PUBLISH_LOG (stdout+stderr combined) and PUBLISH_RC.
# ====================================================================
PUBLISH_LOG=""
PUBLISH_RC=0

run_publish() {
    local with_shasums="$1"
    local _log_tmp
    _log_tmp="$(mktemp)"
    local _rc=0
    (
        set --
        export VERSION="$TEST_VERSION"
        export DRY_RUN=1
        export REPO_ROOT="$REPO_ROOT"
        export AGENT_DIRECTOR_RELEASE_STAGE_DIR="$_stage_dir"
        if [[ "$with_shasums" == "yes" ]]; then
            export AGENT_DIRECTOR_RELEASE_SHASUMS="$SHASUMS_PATH"
        else
            unset AGENT_DIRECTOR_RELEASE_SHASUMS
        fi
        cd "$REPO_ROOT"
        _RELEASE_SH_SOURCE_ONLY=1 source "$RELEASE_SH"
        # Override EXIT trap so report_phase summary/cleanup noise is suppressed.
        # The stage_dir is owned by this test's parent-shell cleanup trap.
        report_phase() { :; }
        publish_phase
    ) >"$_log_tmp" 2>&1 || _rc=$?
    PUBLISH_LOG="$(cat "$_log_tmp")"
    rm -f "$_log_tmp"
    PUBLISH_RC="$_rc"
}

# ====================================================================
# Case 1: Happy — valid tarballs + manifest → publish_phase exits 0
# ====================================================================
printf '\n=== Case 1: happy path — valid tarballs + manifest ===\n'

run_publish "yes"
case1_failures=0

# A. Exit 0.
if [[ "$PUBLISH_RC" -eq 0 ]]; then
    printf '[happy] A: publish_phase exited 0  OK\n'
else
    printf 'ASSERT-FAIL [happy]: A: publish_phase exited %d; expected 0\n' "$PUBLISH_RC" >&2
    printf 'ASSERT-FAIL [happy]: A: log:\n%s\n' "$PUBLISH_LOG" >&2
    case1_failures=$((case1_failures + 1))
fi

# B. SHA-256 round-trip gate ran: check-version-coherence was invoked for
#    --scope publish (its output is prefixed [publish] in the log).
if printf '%s\n' "$PUBLISH_LOG" | grep -qE '\[publish\].*check-version-coherence'; then
    printf '[happy] B: SHA-256 gate ran (check-version-coherence --scope publish in log)  OK\n'
else
    printf 'ASSERT-FAIL [happy]: B: check-version-coherence output not found in log\n' >&2
    printf 'ASSERT-FAIL [happy]: B: log:\n%s\n' "$PUBLISH_LOG" >&2
    case1_failures=$((case1_failures + 1))
fi

# C. "publishing ... from *.tgz" appears 3× — confirms file-path publish,
#    not directory-path publish (the pre-Epic-4 behaviour).
_pub_tgz_count=$(printf '%s\n' "$PUBLISH_LOG" | grep -cE 'publishing.*\.tgz' || true)
if [[ "$_pub_tgz_count" -ge 3 ]]; then
    printf '[happy] C: %d "publishing*.tgz" log line(s) — tarball-file publish confirmed  OK\n' \
        "$_pub_tgz_count"
else
    printf 'ASSERT-FAIL [happy]: C: expected ≥3 "publishing*.tgz" lines; got %d\n' \
        "$_pub_tgz_count" >&2
    printf 'ASSERT-FAIL [happy]: C: log:\n%s\n' "$PUBLISH_LOG" >&2
    case1_failures=$((case1_failures + 1))
fi

if [[ "$case1_failures" -gt 0 ]]; then
    printf '\nCase 1 FAILED (%d assertion(s))\n' "$case1_failures" >&2
    exit 1
fi
printf '\nCase 1 PASSED\n'

# ====================================================================
# Case 2: Drift — byte appended to umbrella tarball after manifest is
# written; gate must catch the SHA-256 mismatch before npm publish fires.
# ====================================================================
printf '\n=== Case 2: drift — 1 byte appended to umbrella tarball ===\n'

_drift_tarball="$(awk 'NR==1{print $NF}' "$SHASUMS_PATH")"
if [[ -z "$_drift_tarball" || ! -f "$_drift_tarball" ]]; then
    printf 'SETUP FAIL: cannot resolve umbrella tarball from manifest\n' >&2
    cat "$SHASUMS_PATH" >&2; exit 1
fi
printf '[drift] appending null byte to: %s\n' "$_drift_tarball"
dd if=/dev/zero bs=1 count=1 >> "$_drift_tarball" 2>/dev/null

run_publish "yes"   # manifest still has pre-mutation hash; tarball bytes changed
case2_failures=0

# A. Exit non-zero.
if [[ "$PUBLISH_RC" -ne 0 ]]; then
    printf '[drift] A: publish_phase exited non-zero (%d)  OK\n' "$PUBLISH_RC"
else
    printf 'ASSERT-FAIL [drift]: A: publish_phase exited 0; expected non-zero\n' >&2
    printf 'ASSERT-FAIL [drift]: A: log:\n%s\n' "$PUBLISH_LOG" >&2
    case2_failures=$((case2_failures + 1))
fi

# B. No "publishing*.tgz" lines — the gate fired before npm publish.
_drift_pub_count=$(printf '%s\n' "$PUBLISH_LOG" | grep -cE 'publishing.*\.tgz' || true)
if [[ "$_drift_pub_count" -eq 0 ]]; then
    printf '[drift] B: no "publishing*.tgz" lines — gate aborted before npm publish  OK\n'
else
    printf 'ASSERT-FAIL [drift]: B: found %d "publishing*.tgz" line(s) — gate did not fire first\n' \
        "$_drift_pub_count" >&2
    printf 'ASSERT-FAIL [drift]: B: log:\n%s\n' "$PUBLISH_LOG" >&2
    case2_failures=$((case2_failures + 1))
fi

# C. Log contains "actual" (re-computed hash of the mutated tarball).
if printf '%s\n' "$PUBLISH_LOG" | grep -qiE 'actual[[:space:]]*:'; then
    printf '[drift] C: "actual" hash found in log  OK\n'
else
    printf 'ASSERT-FAIL [drift]: C: "actual" SHA-256 not found in log\n' >&2
    printf 'ASSERT-FAIL [drift]: C: log:\n%s\n' "$PUBLISH_LOG" >&2
    case2_failures=$((case2_failures + 1))
fi

# D. Log contains "expected" (hash from the pre-mutation manifest).
if printf '%s\n' "$PUBLISH_LOG" | grep -qiE 'expected[[:space:]]*:'; then
    printf '[drift] D: "expected" hash found in log  OK\n'
else
    printf 'ASSERT-FAIL [drift]: D: "expected" SHA-256 not found in log\n' >&2
    printf 'ASSERT-FAIL [drift]: D: log:\n%s\n' "$PUBLISH_LOG" >&2
    case2_failures=$((case2_failures + 1))
fi

if [[ "$case2_failures" -gt 0 ]]; then
    printf '\nCase 2 FAILED (%d assertion(s))\n' "$case2_failures" >&2
    exit 1
fi
printf '\nCase 2 PASSED\n'

# ====================================================================
# Case 3: Missing manifest — AGENT_DIRECTOR_RELEASE_SHASUMS unset →
# publish_phase must fail fast with a clear diagnostic.
# ====================================================================
printf '\n=== Case 3: missing manifest — AGENT_DIRECTOR_RELEASE_SHASUMS unset ===\n'

run_publish "no"
case3_failures=0

# A. Exit non-zero.
if [[ "$PUBLISH_RC" -ne 0 ]]; then
    printf '[missing] A: publish_phase exited non-zero (%d)  OK\n' "$PUBLISH_RC"
else
    printf 'ASSERT-FAIL [missing]: A: publish_phase exited 0; expected non-zero\n' >&2
    case3_failures=$((case3_failures + 1))
fi

# B. Log names the missing env var.
if printf '%s\n' "$PUBLISH_LOG" | grep -qF 'AGENT_DIRECTOR_RELEASE_SHASUMS'; then
    printf '[missing] B: log names AGENT_DIRECTOR_RELEASE_SHASUMS  OK\n'
else
    printf 'ASSERT-FAIL [missing]: B: log does not mention AGENT_DIRECTOR_RELEASE_SHASUMS\n' >&2
    printf 'ASSERT-FAIL [missing]: B: log:\n%s\n' "$PUBLISH_LOG" >&2
    case3_failures=$((case3_failures + 1))
fi

if [[ "$case3_failures" -gt 0 ]]; then
    printf '\nCase 3 FAILED (%d assertion(s))\n' "$case3_failures" >&2
    exit 1
fi
printf '\nCase 3 PASSED\n'

# ====================================================================
# Summary
# ====================================================================
printf '\n=== test-tarball-round-trip: ALL CHECKS PASSED ===\n'
printf 'SHA-256 round-trip confirmed: happy, drift-gate, and missing-manifest cases.\n'
