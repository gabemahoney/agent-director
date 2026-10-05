#!/usr/bin/env bash
# b.r3j install-mode regression suite.
#
# Runs inside the agent-director-test harness image (mounted at
# /opt/install-mode). Each scenario invokes the bundled install.sh
# under a per-scenario sandbox $HOME, then asserts the canonical
# ~/.agent-director/bin/agent-director ends up at literal mode 0755
# via `stat -c %a`, and the operator tool
# ~/.agent-director/admin/agent-director-admin at 0755 in a 0700
# directory (b.vqr). install.sh finds the admin binary through its
# in-repo fallback, the image's /opt/bin/agent-director-admin. Every
# install that should succeed must also exit 0 with no "Permission
# denied" on stderr (b.7j2): a mode check alone passed while install.sh
# exited 5 under umask 0777.
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
ADMIN_SOURCE=/opt/bin/agent-director-admin
INSTALL_SH=/opt/skills/install-agent-director/install.sh

[[ -x "$SOURCE_BINARY" ]] || { echo "FAIL: source not executable: $SOURCE_BINARY" >&2; exit 1; }
[[ -x "$ADMIN_SOURCE" ]]  || { echo "FAIL: admin source not executable: $ADMIN_SOURCE" >&2; exit 1; }
[[ -r "$INSTALL_SH" ]]    || { echo "FAIL: install.sh missing: $INSTALL_SH" >&2; exit 1; }

# Sanity: confirm the harness-staged source is 0755 going in.
src_mode=$(stat -c '%a' "$SOURCE_BINARY")
if [[ "$src_mode" != "755" ]]; then
    echo "FAIL: precondition: source binary mode=$src_mode; expected 755" >&2
    exit 1
fi

pass=0
fail=0

# report <name> <got> <want> [<label>]: <label> (default "mode") names <got>.
report() {
    local name="$1" got="$2" want="$3" label="${4:-mode}"
    if [[ "$got" == "$want" ]]; then
        pass=$((pass+1))
        printf '  PASS  %-40s %s=%s\n' "$name" "$label" "$got"
    else
        fail=$((fail+1))
        printf '  FAIL  %-40s %s=%s  want=%s\n' "$name" "$label" "$got" "$want"
    fi
}

# run_install <name> <home> <umask> <install.sh args...>: install.sh
# --no-hooks --no-symlink <args> against a sandbox HOME under <umask>.
# Reports that it exited 0 and printed no "Permission denied" (b.7j2);
# prints its stderr when either fails.
run_install() {
    local name="$1" home="$2" mask="$3"; shift 3
    local err rc=0
    err=$(mktemp)
    # The stderr file is created before the umask applies, so it stays
    # readable whatever <umask> is.
    (
        umask "$mask"
        HOME="$home" bash "$INSTALL_SH" --no-hooks --no-symlink "$@" >/dev/null
    ) 2>"$err" || rc=$?
    report "$name-exit" "$rc" "0" exit
    report "$name-permission-denied" "$(grep -c 'Permission denied' "$err")" "0" lines
    if [[ "$rc" -ne 0 ]] || grep -q 'Permission denied' "$err"; then
        sed 's/^/        stderr| /' "$err"
    fi
    rm -f "$err"
}

# canonical_mode <home>: the installed agent-director's literal mode bits,
# or "missing".
canonical_mode() {
    stat -c '%a' "$1/.agent-director/bin/agent-director" 2>/dev/null || echo missing
}

# admin_modes <home>: "<dir mode>/<binary mode>" of the installed
# agent-director-admin, or "not-installed" when it is missing or is not
# the image's admin binary.
admin_modes() {
    local a="$1/.agent-director/admin/agent-director-admin"
    if ! cmp -s "$a" "$ADMIN_SOURCE"; then
        echo "not-installed"
        return
    fi
    echo "$(stat -c '%a' "${a%/*}")/$(stat -c '%a' "$a")"
}

echo "[b.r3j install-mode] start"

# -- scenario 1: default umask 022, fresh install ------------------------
H=$(mktemp -d)
run_install "fresh-umask-022" "$H" 022 --binary "$SOURCE_BINARY"
report "fresh-umask-022" "$(canonical_mode "$H")" "755"
report "fresh-umask-022-admin" "$(admin_modes "$H")" "700/755"

# -- scenario 2: restrictive umask 077, fresh install --------------------
# install.sh uses cp + chmod 0755; the chmod is an absolute mode set, so
# the operator umask must not bleed into the final bits.
H=$(mktemp -d)
run_install "fresh-umask-077" "$H" 077 --binary "$SOURCE_BINARY"
report "fresh-umask-077" "$(canonical_mode "$H")" "755"
report "fresh-umask-077-admin" "$(admin_modes "$H")" "700/755"

# -- scenario 3: paranoid umask 0777, fresh install ----------------------
# install.sh gives the owner back rwx (umask u=rwx, b.7j2), so it runs as
# under 077 above: the same modes, cp's new file at 0700 absent the
# explicit chmod. Before b.7j2 its files were created at 000, so every
# user_version read failed and the install exited 5.
H=$(mktemp -d)
run_install "fresh-umask-0777" "$H" 0777 --binary "$SOURCE_BINARY"
report "fresh-umask-0777" "$(canonical_mode "$H")" "755"
report "fresh-umask-0777-admin" "$(admin_modes "$H")" "700/755"

# -- scenario 4: --keep-prior upgrade flow -------------------------------
# First a fresh install of an older build (with no --keep-prior, so no
# .prior yet), then an upgrade to the source binary with --keep-prior —
# the second one must:
#   - copy the existing canonical to .prior at 0755
#   - copy the existing agent-director-admin to its .prior at 0755
#   - write the new canonical at 0755 via the temp+mv pattern
# Files asserted independently. The older build is the source binary
# with one byte appended, installed beside the same
# agent-director-admin: install.sh decides --keep-prior for the pair
# (b.2wk), snapshotting neither binary only when agent-director is
# already byte-identical to its source and agent-director-admin is too
# (or is not installed). Here agent-director differs, so both are
# snapshotted, agent-director-admin unchanged as it is.
H=$(mktemp -d)
OLD_PARENT=$(mktemp -d)
cp "$SOURCE_BINARY" "$OLD_PARENT/agent-director"
printf '\0' >>"$OLD_PARENT/agent-director"
chmod 0755 "$OLD_PARENT/agent-director"
run_install "keep-prior-older-build" "$H" 022 --binary "$OLD_PARENT/agent-director"
run_install "keep-prior-upgrade" "$H" 022 --binary "$SOURCE_BINARY" --keep-prior
report "keep-prior-canonical" "$(canonical_mode "$H")" "755"
report "keep-prior-admin" "$(admin_modes "$H")" "700/755"
if [[ -f "$H/.agent-director/bin/agent-director.prior" ]]; then
    mp=$(stat -c '%a' "$H/.agent-director/bin/agent-director.prior")
    report "keep-prior-snapshot" "$mp" "755"
else
    fail=$((fail+1))
    echo "  FAIL  keep-prior-snapshot                missing .prior"
fi
if [[ -f "$H/.agent-director/admin/agent-director-admin.prior" ]]; then
    mp=$(stat -c '%a' "$H/.agent-director/admin/agent-director-admin.prior")
    report "keep-prior-admin-snapshot" "$mp" "755"
else
    fail=$((fail+1))
    echo "  FAIL  keep-prior-admin-snapshot          missing admin .prior"
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
if [[ -e "$H/.agent-director/bin/agent-director" || -e "$H/.agent-director/admin/agent-director-admin" ]]; then
    fail=$((fail+1))
    echo "  FAIL  0644-source-no-partial-write    canonical or admin binary exists after exit 3"
else
    pass=$((pass+1))
    echo "  PASS  0644-source-no-partial-write    no canonical written"
fi

echo "[b.r3j install-mode] summary: $pass passed, $fail failed"

if [[ "$fail" -ne 0 ]]; then
    exit 1
fi
exit 0
