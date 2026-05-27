#!/usr/bin/env bash
# b.r3j install-mode regression suite.
#
# Runs inside the agent-director-test harness image (mounted at
# /opt/install-mode). Each scenario invokes the bundled install.sh
# under a per-scenario sandbox $HOME, then asserts the canonical
# ~/.agent-director/bin/agent-director ends up at literal mode 0755
# via `stat -c %a`.
#
# Background: b.r3j reported the installed binary landing at 0644 on
# a fresh Horde DGXC VM despite install.sh's `chmod 0755 "$TMP"`
# before the atomic mv. This suite exercises install.sh under
# adversarial configurations (restrictive umask, --keep-prior dual-
# write, 0644 source) to confirm install.sh's mode handling is
# robust against the conditions the bug observed.
#
# A failing assertion means either (a) install.sh regressed, or
# (b) the in-container filesystem is masking mode bits — which
# would be evidence the bug lives outside install.sh.

set -euo pipefail

SOURCE_BINARY=/usr/local/bin/agent-director
INSTALL_SH=/opt/skills/install-agent-director/install.sh

[[ -x "$SOURCE_BINARY" ]] || { echo "FAIL: source not executable: $SOURCE_BINARY" >&2; exit 1; }
[[ -r "$INSTALL_SH" ]]    || { echo "FAIL: install.sh missing: $INSTALL_SH" >&2; exit 1; }

# Sanity: confirm the harness-staged source is 0755 going in.
src_mode=$(stat -c '%a' "$SOURCE_BINARY")
if [[ "$src_mode" != "755" ]]; then
    echo "FAIL: precondition: source binary mode=$src_mode; expected 755" >&2
    exit 1
fi

pass=0
fail=0

report() {
    local name="$1" got="$2" want="$3"
    if [[ "$got" == "$want" ]]; then
        pass=$((pass+1))
        printf '  PASS  %-40s mode=%s\n' "$name" "$got"
    else
        fail=$((fail+1))
        printf '  FAIL  %-40s mode=%s  want=%s\n' "$name" "$got" "$want"
    fi
}

# Run install.sh against a sandbox HOME with the given umask, echo the
# resulting canonical binary's literal mode bits.
install_canonical_mode() {
    local home="$1" mask="$2"; shift 2
    (
        umask "$mask"
        HOME="$home" bash "$INSTALL_SH" --binary "$SOURCE_BINARY" \
            --no-hooks --no-symlink "$@" >/dev/null
    )
    stat -c '%a' "$home/.agent-director/bin/agent-director"
}

echo "[b.r3j install-mode] start"

# -- scenario 1: default umask 022, fresh install ------------------------
H=$(mktemp -d)
m=$(install_canonical_mode "$H" 022)
report "fresh-umask-022" "$m" "755"

# -- scenario 2: restrictive umask 077, fresh install --------------------
# install.sh uses cp + chmod 0755; the chmod is an absolute mode set, so
# the operator umask must not bleed into the final bits.
H=$(mktemp -d)
m=$(install_canonical_mode "$H" 077)
report "fresh-umask-077" "$m" "755"

# -- scenario 3: paranoid umask 0777, fresh install ----------------------
# Extreme case: cp's default newly-created file would land at 000 absent
# the explicit chmod. Confirms the chmod is the load-bearing step.
H=$(mktemp -d)
m=$(install_canonical_mode "$H" 0777)
report "fresh-umask-0777" "$m" "755"

# -- scenario 4: --keep-prior upgrade flow -------------------------------
# First a fresh install (with no --keep-prior, so no .prior yet), then a
# second install with --keep-prior — the second one must:
#   - copy the existing canonical to .prior at 0755
#   - write the new canonical at 0755 via the temp+mv pattern
# Both files asserted independently.
H=$(mktemp -d)
HOME="$H" bash "$INSTALL_SH" --binary "$SOURCE_BINARY" --no-hooks --no-symlink >/dev/null
m=$(install_canonical_mode "$H" 022 --keep-prior)
report "keep-prior-canonical" "$m" "755"
if [[ -f "$H/.agent-director/bin/agent-director.prior" ]]; then
    mp=$(stat -c '%a' "$H/.agent-director/bin/agent-director.prior")
    report "keep-prior-snapshot" "$mp" "755"
else
    fail=$((fail+1))
    echo "  FAIL  keep-prior-snapshot                missing .prior"
fi

# -- scenario 5: 0644 source MUST be refused -----------------------------
# install.sh's preflight (line ~291) hard-rejects a non-executable source
# with exit 3. The bug ticket hypothesised an upstream copy step landing
# the source at 0644; this scenario pins the install.sh-side defensive
# behavior so future code can't silently propagate a 0644 source through
# to the canonical path.
H=$(mktemp -d)
SRC_PARENT=$(mktemp -d)
cp "$SOURCE_BINARY" "$SRC_PARENT/agent-director"
chmod 0644 "$SRC_PARENT/agent-director"
set +e
HOME="$H" bash "$INSTALL_SH" --binary "$SRC_PARENT/agent-director" \
    --no-hooks --no-symlink >/dev/null 2>&1
rc=$?
set -e
if [[ "$rc" -eq 3 ]]; then
    pass=$((pass+1))
    printf '  PASS  %-40s exit=%s\n' "0644-source-refused" "$rc"
else
    fail=$((fail+1))
    printf '  FAIL  %-40s exit=%s  want=3\n' "0644-source-refused" "$rc"
fi
# Canonical must NOT exist after a refused install (no partial write).
if [[ -e "$H/.agent-director/bin/agent-director" ]]; then
    fail=$((fail+1))
    echo "  FAIL  0644-source-no-partial-write    canonical exists after exit 3"
else
    pass=$((pass+1))
    echo "  PASS  0644-source-no-partial-write    no canonical written"
fi

echo "[b.r3j install-mode] summary: $pass passed, $fail failed"

if [[ "$fail" -ne 0 ]]; then
    exit 1
fi
exit 0
