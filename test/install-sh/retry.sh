#!/usr/bin/env bash
# retry.sh — b.kym install.sh --from-release retry-on-404 regression.
#
# Exercises install.sh's CDN-propagation retry wrapper by injecting a
# fake `curl` (via the INSTALL_SH_TEST_CURL_OVERRIDE escape hatch) that
# returns HTTP 404 for the first N invocations and then HTTP 200 with a
# real ELF body. Asserts:
#
#   - install.sh retries the expected number of times
#   - install.sh ultimately succeeds (canonical binary lands at 0755)
#   - the retry log lines appear on stderr so a watching operator sees
#     the propagation window in action
#
# The fake-curl shells through to real curl for the api.github.com
# tag-resolve call earlier in install.sh; only the retry-wrapper
# invocation shape (`-o <path> -w '%{http_code}'`) is intercepted. To
# avoid hitting the live API entirely, the test passes an explicit tag
# (`v0.0.0-fake`) so install.sh skips the tag-resolve step.
#
# Backoffs are SHORT in the test path because we accept the real
# 2s/4s/8s/16s sleeps; aggregate test time is still bounded (the
# fail-first count is small).

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$HERE/../.." && pwd)"
INSTALL_SH="${REPO_ROOT}/skills/install-agent-director/install.sh"
FAKE_CURL="${HERE}/fake-curl.sh"

[[ -x "$INSTALL_SH" ]] || { echo "FAIL: install.sh missing or not executable: $INSTALL_SH" >&2; exit 1; }
[[ -x "$FAKE_CURL" ]]  || { echo "FAIL: fake-curl.sh missing or not executable: $FAKE_CURL" >&2; exit 1; }

# Host gate — install.sh hard-refuses anything outside the supported set.
case "$(uname -s)/$(uname -m)" in
    Linux/x86_64|Darwin/arm64) ;;
    *)
        echo "SKIP: install.sh test only supported on Linux/x86_64 or Darwin/arm64" >&2
        exit 0
        ;;
esac

# Body source: a small, real ELF/Mach-O on the host. /bin/true exists
# on both supported platforms and passes install.sh's arch probe.
BODY_SRC=/bin/true
[[ -x "$BODY_SRC" ]] || { echo "FAIL: body source missing: $BODY_SRC" >&2; exit 1; }

pass=0
fail=0
report() {
    local name="$1" got="$2" want="$3"
    if [[ "$got" == "$want" ]]; then
        pass=$((pass+1))
        printf '  PASS  %-50s got=%s\n' "$name" "$got"
    else
        fail=$((fail+1))
        printf '  FAIL  %-50s got=%s  want=%s\n' "$name" "$got" "$want"
    fi
}

run_install() {
    local home="$1" fail_first="$2" state_file="$3" stderr_file="$4"
    HOME="$home" \
    INSTALL_SH_TEST_CURL_OVERRIDE="$FAKE_CURL" \
    FAKE_CURL_STATE_FILE="$state_file" \
    FAKE_CURL_FAIL_FIRST="$fail_first" \
    FAKE_CURL_BODY_SOURCE="$BODY_SRC" \
        bash "$INSTALL_SH" --from-release v0.0.0-fake \
            --no-hooks --no-symlink >/dev/null 2>"$stderr_file"
}

echo "[b.kym install-sh retry] start"

# -- scenario 1: 0 failures (200 on first try) ----------------------------
# Baseline: the retry wrapper must not regress the happy path.
H=$(mktemp -d)
STATE=$(mktemp)
ERR=$(mktemp)
set +e
run_install "$H" 0 "$STATE" "$ERR"
rc=$?
set -e
report "happy-path-exit-code" "$rc" "0"
count=$(cat "$STATE")
report "happy-path-attempt-count" "$count" "1"
if [[ -x "$H/.agent-director/bin/agent-director" ]]; then
    report "happy-path-binary-installed" "yes" "yes"
else
    report "happy-path-binary-installed" "no" "yes"
fi

# -- scenario 2: 2 failures then success ----------------------------------
# Two 404s, third attempt succeeds. Should see two visible retry log
# lines on stderr.
H=$(mktemp -d)
STATE=$(mktemp)
ERR=$(mktemp)
set +e
run_install "$H" 2 "$STATE" "$ERR"
rc=$?
set -e
report "retry-2-exit-code" "$rc" "0"
count=$(cat "$STATE")
report "retry-2-attempt-count" "$count" "3"
retry_log_count=$(grep -c "asset not yet available" "$ERR" || true)
report "retry-2-log-line-count" "$retry_log_count" "2"

# -- scenario 3: 4 failures then success (last allowed retry) -------------
# All 5 attempts allowed; 4 fail, 5th succeeds. The 5th attempt is the
# last allowed, so we expect exactly 4 retry log lines (no log after
# the 5th attempt regardless of outcome).
H=$(mktemp -d)
STATE=$(mktemp)
ERR=$(mktemp)
set +e
run_install "$H" 4 "$STATE" "$ERR"
rc=$?
set -e
report "retry-4-exit-code" "$rc" "0"
count=$(cat "$STATE")
report "retry-4-attempt-count" "$count" "5"
retry_log_count=$(grep -c "asset not yet available" "$ERR" || true)
report "retry-4-log-line-count" "$retry_log_count" "4"

echo "[b.kym install-sh retry] summary: $pass passed, $fail failed"

if [[ "$fail" -ne 0 ]]; then
    # Dump the last stderr for triage on failure.
    echo "--- last stderr ---" >&2
    cat "$ERR" >&2 || true
    exit 1
fi
exit 0
