#!/usr/bin/env bash
# test-notes-phase-heredoc.sh — regression test for the notes_phase backtick
# command-substitution bug (b.85s).
#
# Bug: bare backticks around `agent-director`, `@agent-director/linux-x64`,
# and `@agent-director/darwin-arm64` inside the unquoted NOTES heredoc in
# notes_phase caused bash to attempt command substitution, emitting two
# "No such file or directory" lines on stderr on every release run.
#
# Fix (b.85s Engineer): escaped backticks (\`) around those three identifiers
# at lines 425-427 of release.sh.
#
# This test:
#   1. Sources release.sh with _RELEASE_SH_SOURCE_ONLY=1 to load notes_phase
#      without running main().
#   2. Calls notes_phase directly with minimal env (VERSION, DRY_RUN) inside
#      the real git repo so git commands work.
#   3. Asserts stderr from notes_phase contains zero "No such file or
#      directory" lines.
#   4. Asserts the generated dist/release-notes.md contains the three literal
#      backtick-wrapped markdown code spans.
#
#   Part 2 self-validates by reverting the escape fix on a temp copy of
#   release.sh and confirming the same assertions FAIL — proving this test
#   has real regression coverage.
#
# Usage (from any directory inside the repository):
#   bash skills/release-agent-director/test-notes-phase-heredoc.sh
#
# Exit codes:
#   0  all assertions passed; buggy version correctly detected
#   1  assertion failure or self-validation failure

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(git -C "$SCRIPT_DIR" rev-parse --show-toplevel)"
RELEASE_SH="$SCRIPT_DIR/release.sh"

# Temp file for buggy copy of release.sh and temp notes dirs; cleaned up on EXIT.
BUGGY_RELEASE_SH=""
NOTES_TMP_DIR=""

cleanup() {
    [[ -n "$BUGGY_RELEASE_SH" && -f "$BUGGY_RELEASE_SH" ]] && rm -f "$BUGGY_RELEASE_SH"
    [[ -n "$NOTES_TMP_DIR" && -d "$NOTES_TMP_DIR" ]] && rm -rf "$NOTES_TMP_DIR"
}
trap cleanup EXIT

# --------------------------------------------------------------------
# run_notes_phase <release_sh_path>
#
# Sources release_sh with _RELEASE_SH_SOURCE_ONLY=1, sets the minimal
# env, then calls notes_phase inside the repo root so git commands work.
# Captures stderr into NOTES_STDERR.
#
# To avoid polluting the real dist/ directory, a git() shim is installed
# in the subshell that intercepts "git rev-parse --show-toplevel" and
# returns a per-invocation temp directory instead. notes_phase then writes
# dist/release-notes.md inside that temp dir. All other git calls pass
# through to the real git binary unmodified.
#
# Returns: exit code of the subshell (0 on success).
# Sets globals: NOTES_STDERR, NOTES_FILE_PATH, NOTES_TMP_DIR
# --------------------------------------------------------------------
NOTES_STDERR=""
NOTES_FILE_PATH=""

run_notes_phase() {
    local release_sh="$1"
    local stderr_capture
    stderr_capture="$(mktemp)"

    # Clean up any previous invocation's temp dir before creating a new one.
    [[ -n "$NOTES_TMP_DIR" && -d "$NOTES_TMP_DIR" ]] && rm -rf "$NOTES_TMP_DIR"
    # Create a fresh temp dir for this invocation's notes output.
    # Stored globally so the EXIT trap can clean it up.
    NOTES_TMP_DIR="$(mktemp -d)"
    local notes_tmp_dir="$NOTES_TMP_DIR"

    (
        # Clear positional parameters before sourcing. The subshell inherits
        # $@ from the calling function; release.sh's flag-parsing loop runs
        # at source time (before _RELEASE_SH_SOURCE_ONLY short-circuits it),
        # so function args would be misinterpreted as release flags.
        set --
        export VERSION="v9.9.9"
        export DRY_RUN=1
        # Source in the repo root so relative paths resolve correctly.
        cd "$REPO_ROOT"
        _RELEASE_SH_SOURCE_ONLY=1 source "$release_sh"
        # Silence the EXIT trap — we don't want report_phase summary noise.
        report_phase() { :; }

        # Shim: intercept "git rev-parse --show-toplevel" so notes_phase
        # writes to our temp dir instead of the real dist/ directory.
        # All other git invocations pass through to the real binary.
        git() {
            if [[ "$*" == "rev-parse --show-toplevel" ]]; then
                printf '%s\n' "$notes_tmp_dir"
            else
                command git "$@"
            fi
        }

        notes_phase
    ) 2>"$stderr_capture"
    local rc=$?

    NOTES_STDERR="$(cat "$stderr_capture")"
    rm -f "$stderr_capture"
    # notes_phase writes here; points into our temp dir, not the real dist/
    NOTES_FILE_PATH="$NOTES_TMP_DIR/dist/release-notes.md"
    return $rc
}

# --------------------------------------------------------------------
# assert_notes_phase_clean <label>
#
# Asserts (using $NOTES_STDERR and $NOTES_FILE_PATH set by run_notes_phase):
#   A. Zero stderr lines matching "No such file or directory"
#   B. dist/release-notes.md contains literal `agent-director`
#   C. dist/release-notes.md contains literal `@agent-director/linux-x64`
#   D. dist/release-notes.md contains literal `@agent-director/darwin-arm64`
#
# Prints ASSERT-FAIL for each failed assertion to stderr.
# Returns count of failures (0 = all passed).
# --------------------------------------------------------------------
assert_notes_phase_clean() {
    local label="$1"
    local failures=0

    # A. No "No such file or directory" on stderr
    local nofile_count
    nofile_count=$(printf '%s\n' "$NOTES_STDERR" | grep -c "No such file or directory" || true)
    if [[ "$nofile_count" -gt 0 ]]; then
        printf 'ASSERT-FAIL [%s]: stderr has %d "No such file or directory" line(s)\n' \
            "$label" "$nofile_count" >&2
        printf 'ASSERT-FAIL [%s]: captured stderr:\n%s\n' "$label" "$NOTES_STDERR" >&2
        failures=$((failures + 1))
    else
        printf '[%s] A: stderr clean (zero "No such file or directory")\n' "$label"
    fi

    # B-D. Literal backtick code spans in release-notes.md
    local span label_suffix
    for span in 'agent-director' '@agent-director/linux-x64' '@agent-director/darwin-arm64'; do
        if grep -qF "\`${span}\`" "$NOTES_FILE_PATH" 2>/dev/null; then
            printf '[%s] found literal `%s` in release-notes.md\n' "$label" "$span"
        else
            printf 'ASSERT-FAIL [%s]: literal `%s` not found in release-notes.md\n' \
                "$label" "$span" >&2
            failures=$((failures + 1))
        fi
    done

    return "$failures"
}

# --------------------------------------------------------------------
# assert_notes_phase_buggy <label>
#
# Inverse of assert_notes_phase_clean: confirms at least one bug symptom
# is present. Used to prove the test has real regression coverage.
# Returns 0 if a bug symptom was detected, 1 if none were (vacuous pass).
# --------------------------------------------------------------------
assert_notes_phase_buggy() {
    local label="$1"
    local bug_detected=0

    # Primary symptom: stderr noise
    local nofile_count
    nofile_count=$(printf '%s\n' "$NOTES_STDERR" | grep -c "No such file or directory" || true)
    if [[ "$nofile_count" -gt 0 ]]; then
        printf '[%s] bug detected: %d "No such file or directory" line(s) on stderr\n' \
            "$label" "$nofile_count"
        bug_detected=1
    fi

    # Secondary symptom: missing code spans (bash ate the backtick content)
    if ! grep -qF '`agent-director`' "$NOTES_FILE_PATH" 2>/dev/null; then
        printf '[%s] bug detected: literal `agent-director` missing from release-notes.md\n' "$label"
        bug_detected=1
    fi

    if [[ "$bug_detected" -eq 0 ]]; then
        printf 'SELF-VALIDATION FAIL [%s]: buggy version showed no bug symptoms — test has no regression coverage\n' \
            "$label" >&2
        return 1
    fi
    return 0
}

# ====================================================================
# PART 1 — Fixed release.sh: all assertions must pass
# ====================================================================
printf '\n=== Part 1: assertions against the FIXED release.sh ===\n'

phase1_rc=0
run_notes_phase "$RELEASE_SH" || phase1_rc=$?

if [[ $phase1_rc -ne 0 ]]; then
    printf 'ASSERT-FAIL: notes_phase exited non-zero (%d) with fixed release.sh\n' "$phase1_rc" >&2
    exit 1
fi

clean_failures=0
assert_notes_phase_clean "fixed" || clean_failures=$?

if [[ $clean_failures -gt 0 ]]; then
    printf '\nPart 1 FAILED — %d assertion(s) failed on the fixed release.sh\n' "$clean_failures" >&2
    exit 1
fi

printf '\nPart 1 PASSED — fixed release.sh: clean stderr + all code spans present\n'

# ====================================================================
# PART 2 — Buggy release.sh: self-validation
#
# Create a temp copy with the engineer's escape fix reverted:
#   \`agent-director\` → `agent-director` (bare backticks = original bug)
# Then confirm assertions detect the bug.
# ====================================================================
printf '\n=== Part 2: self-validation against a BUGGY release.sh (escape fix reverted) ===\n'

BUGGY_RELEASE_SH="$(mktemp /tmp/release-sh-buggy-$$.XXXXXX.sh)"
cp "$RELEASE_SH" "$BUGGY_RELEASE_SH"
chmod +x "$BUGGY_RELEASE_SH"

# Revert the three escaped backtick identifiers.
# Fixed form:  \`agent-director\`  (in file: literal backslash-backtick)
# Buggy form:  `agent-director`   (bare backtick, triggers command substitution)
sed -i \
    -e 's/\\`agent-director\\`/`agent-director`/g' \
    -e 's/\\`@agent-director\/linux-x64\\`/`@agent-director\/linux-x64`/g' \
    -e 's/\\`@agent-director\/darwin-arm64\\`/`@agent-director\/darwin-arm64`/g' \
    "$BUGGY_RELEASE_SH"

# Sanity check: the revert must have changed the file.
if diff -q "$RELEASE_SH" "$BUGGY_RELEASE_SH" >/dev/null 2>&1; then
    printf 'SELF-VALIDATION FAIL: buggy copy is identical to fixed — sed revert did not apply\n' >&2
    exit 1
fi
printf '[self-validation] revert applied: buggy copy differs from fixed\n'

NOTES_STDERR=""
NOTES_FILE_PATH=""

buggy_phase_rc=0
run_notes_phase "$BUGGY_RELEASE_SH" || buggy_phase_rc=$?
# The bug was stderr noise, not a fatal exit. Proceed regardless of exit code.

if ! assert_notes_phase_buggy "buggy"; then
    printf '\nPart 2 FAILED — buggy version did not trigger detectable symptoms\n' >&2
    exit 1
fi

printf '\nPart 2 PASSED — buggy version correctly triggers detectable bug symptoms\n'

# ====================================================================
# Summary
# ====================================================================
printf '\n=== test-notes-phase-heredoc: ALL CHECKS PASSED ===\n'
printf 'Regression coverage confirmed: test catches the b.85s backtick command-substitution bug.\n'
