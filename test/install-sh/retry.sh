#!/usr/bin/env bash
# retry.sh — b.kym install.sh --from-release retry-on-404 regression, for
# both release assets, agent-director and agent-director-admin (b.vqr), and
# the rest of a --from-release install of the pair (b.vqr): hash
# verification and the PATH symlink.
#
# Exercises install.sh's CDN-propagation retry wrapper by injecting a
# fake `curl` (via the INSTALL_SH_TEST_CURL_OVERRIDE escape hatch) that
# returns HTTP 404 for the first N download invocations of one asset and
# then HTTP 200 with the asset's body: agent-director and
# agent-director-admin built from this tree with the same version and commit
# stamp, so install.sh's version pairing and store open pass. Asserts per
# scenario:
#
#   - install.sh ultimately succeeds and both binaries land (0755)
#   - the download count: N failed attempts plus one per asset
#   - one retry log line per failed attempt on stderr, so a watching
#     operator sees the propagation window in action
#
# And for a release before 0.11.0, which has no agent-director-admin
# asset (b.vqr): the admin asset's 404 is refused at once, exit 3, with
# no retry, nothing installed.
#
# Hash verification (b.vqr): --sha256 and --admin-sha256, both right,
# install both; either wrong refuses the install (exit 3) and installs
# nothing; either without --from-release is refused (exit 2). One without the
# other is advice_follow.sh's J12.
#
# PATH symlink (b.vqr): with --symlink-dir, and with the default
# ~/.local/bin on PATH, the directory gets agent-director and nothing else;
# agent-director-admin is never put on PATH.
#
# Store lock (b.ady): an upgrade whose two user_version reads (before the
# migrating open, and verifying it) each start while another sqlite3 process
# briefly holds state.db's exclusive lock still authorizes, runs and verifies
# the migration: both reads wait the lock out (each takes 0.5 s or more).
#
# The test passes an explicit tag (`v0.11.0-fake`, a release that ships
# agent-director-admin) so install.sh skips the tag-resolve step and
# nothing reaches the network.
#
# Sandbox only: it builds and runs agent-director binaries (b.8dr). make
# test-sandbox runs it through retry_test.go; alone, run
#   make sandbox CMD="bash test/install-sh/retry.sh"
# Every install.sh run gets its own HOME under a private temp root, `env -i`
# and a PATH with fakes first (claude, an instant sleep so the backoffs cost
# no wall time, and file(1) when the image lacks it). On a host install.sh
# refuses, the script prints one "retry.sh: SKIP: <reason>" line and exits
# 77.

set -uo pipefail

if [[ "${AGENT_DIRECTOR_TEST_SANDBOX:-}" != 1 ]]; then
    echo "retry.sh: refusing to run outside the sandbox (AGENT_DIRECTOR_TEST_SANDBOX=1 unset);" \
        "run: make sandbox CMD=\"bash test/install-sh/retry.sh\"" >&2
    exit 1
fi

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$HERE/../.." && pwd)"
INSTALL_SH="${REPO_ROOT}/skills/install-agent-director/install.sh"
FAKE_CURL="${HERE}/fake-curl.sh"
ROOT="$(mktemp -d -t ad-install-retry.XXXXXX)"
trap 'rm -rf "$ROOT"' EXIT

die() { echo "retry.sh: setup failed: $*" >&2; exit 1; }

[[ -x "$INSTALL_SH" ]] || die "install.sh missing or not executable: $INSTALL_SH"
[[ -x "$FAKE_CURL" ]] || die "fake-curl.sh missing or not executable: $FAKE_CURL"

# Host gate — install.sh hard-refuses anything outside the supported set.
case "$(uname -s)/$(uname -m)" in
    Linux/x86_64|Darwin/arm64) ;;
    *)
        echo "retry.sh: SKIP: install.sh only supports Linux/x86_64 and Darwin/arm64 hosts, not $(uname -s)/$(uname -m)"
        exit 77
        ;;
esac

# The two asset bodies, stamped as one build of a 0.11.0 release (the same
# version and a commit), as the /release skill builds them.
VERSION_PKG=github.com/gabemahoney/agent-director/internal/version
STAMP="-X $VERSION_PKG.Version=0.11.0-fake -X $VERSION_PKG.Commit=5eedfa4e5eedfa4e5eedfa4e5eedfa4e5eedfa4e"
BIN="$ROOT/bin/agent-director"
ADMIN="$ROOT/bin/agent-director-admin"
(cd "$REPO_ROOT" && CGO_ENABLED=0 go build -ldflags "$STAMP" -o "$BIN" ./cmd/agent-director) || die "go build"
(cd "$REPO_ROOT" && CGO_ENABLED=0 go build -ldflags "$STAMP" -o "$ADMIN" ./cmd/agent-director-admin) || die "go build (admin)"
BIN_SHA="$(sha256sum "$BIN" | cut -d' ' -f1)"
ADMIN_SHA="$(sha256sum "$ADMIN" | cut -d' ' -f1)"
WRONG_SHA="$(printf '0%.0s' {1..64})"
[[ "$BIN_SHA" =~ ^[0-9a-f]{64}$ && "$ADMIN_SHA" =~ ^[0-9a-f]{64}$ ]] || die "sha256sum the assets"

# Fakes, first on install.sh's PATH.
FAKES="$ROOT/fakes"
mkdir -p "$FAKES" "$ROOT/tmp"
fake() { cat >"$FAKES/$1"; chmod 0755 "$FAKES/$1"; }
fake claude <<'EOF'
#!/bin/bash
echo "0.0.0 (fake claude, b.kym install-sh retry)"
EOF
fake sleep <<'EOF'
#!/bin/bash
exit 0
EOF
if ! command -v file >/dev/null 2>&1; then
    # The sandbox image has no file(1): describe ELF the way it does, enough
    # for install.sh's architecture probe.
    fake file <<'EOF'
#!/bin/bash
p="${!#}"
hex() { od -An -tx1 -j "$1" -N "$2" "$p" | tr -d ' \n'; }
case "$(hex 0 4)" in
    7f454c46)
        case "$(hex 18 2)" in 3e00) m=x86-64 ;; b700) m="ARM aarch64" ;; *) m="machine 0x$(hex 18 2)" ;; esac
        echo "ELF 64-bit LSB executable, ${m}, version 1 (SYSV), statically linked" ;;
    cffaedfe) echo "Mach-O 64-bit executable" ;;
    *) echo "data" ;;
esac
EOF
fi

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

# new_home <name>: a fresh HOME in H, and the files of the run named <name>:
# ERR (its stderr), OUT (its stdout) and STATE (the download count).
H="" RC=0 ERR="" OUT="" STATE="" PATH_PREFIX=""
new_home() {
    H="$(mktemp -d "$ROOT/home.XXXXXX")"
    STATE="$ROOT/$1.count" ERR="$ROOT/$1.err" OUT="$ROOT/$1.out"
}

# run_install <fail-first> <fail-match> <install.sh args...>: install.sh
# --no-hooks <args> under H, with the first <fail-first> downloads of the
# assets whose name matches the glob <fail-match> answering 404, and
# PATH_PREFIX (when set) first on its PATH; sets RC.
run_install() {
    local fail_first="$1" fail_match="$2"; shift 2
    env -i HOME="$H" PATH="${PATH_PREFIX:+$PATH_PREFIX:}$FAKES:$PATH" TMPDIR="$ROOT/tmp" \
        INSTALL_SH_TEST_CURL_OVERRIDE="$FAKE_CURL" \
        FAKE_CURL_STATE_FILE="$STATE" \
        FAKE_CURL_FAIL_FIRST="$fail_first" \
        FAKE_CURL_FAIL_MATCH="$fail_match" \
        FAKE_CURL_BODY_SOURCE="$BIN" \
        FAKE_CURL_ADMIN_BODY_SOURCE="$ADMIN" \
        bash "$INSTALL_SH" --no-hooks "$@" >"$OUT" 2>"$ERR"
    RC=$?
}

# report_installed <name>: BIN and ADMIN are installed under H, at 0755.
report_installed() {
    local c="$H/.agent-director/bin/agent-director" a="$H/.agent-director/admin/agent-director-admin"
    report "$1-binary-installed" "$(cmp -s "$c" "$BIN" && stat -c %a "$c")" "755"
    report "$1-admin-binary-installed" "$(cmp -s "$a" "$ADMIN" && stat -c %a "$a")" "755"
}

# report_refused <name> <want-rc> <want-first-stderr-line>: the run exited
# <want-rc> with that refusal first on stderr, installed nothing under H and
# left no download in TMPDIR.
report_refused() {
    report "$1-exit-code" "$RC" "$2"
    report "$1-first-stderr-line" "$(head -n 1 "$ERR")" "$3"
    report "$1-nothing-installed" "$(ls -A "$H")" ""
    report "$1-no-download-left" "$(compgen -G "$ROOT/tmp/agent-director*")" ""
}

# scenario <name> <fail-first> <fail-match> <want-downloads> [<tag>]: one
# install, then its exit code, download count, retry lines and installed
# binaries.
scenario() {
    local name="$1" fail_first="$2" fail_match="$3" want_downloads="$4" tag="${5:-v0.11.0-fake}"
    new_home "$name"
    run_install "$fail_first" "$fail_match" --from-release "$tag" --no-symlink
    report "$name-exit-code" "$RC" "0"
    report "$name-download-count" "$(cat "$STATE" 2>/dev/null)" "$want_downloads"
    report "$name-retry-log-line-count" "$(grep -c "asset not yet available" "$ERR")" "$fail_first"
    report_installed "$name"
}

# refused <name> <tag>: --from-release <tag>, a release before 0.11.0, whose
# agent-director-admin asset 404s: exit 3 after one download per asset, no
# retry line or gh fallback, the refusal as the first stderr line, and
# nothing installed (b.vqr).
refused() {
    local name="$1" tag="$2"
    new_home "$name"
    run_install 5 'agent-director-admin-*' --from-release "$tag" --no-symlink
    report_refused "$name" 3 "install.sh: --from-release: release $tag has no agent-director-admin binary; refusing to install."
    report "$name-download-count" "$(cat "$STATE" 2>/dev/null)" "2"
    report "$name-retry-log-line-count" "$(grep -c "asset not yet available" "$ERR")" "0"
    report "$name-gh-fallback-line-count" "$(grep -c "gh release download" "$ERR")" "0"
}

# hash_refused <name> <want-downloads> <want-first-stderr-line> <hash flags...>:
# --from-release with a wrong hash: exit 3 after <want-downloads> downloads,
# that mismatch first on stderr, nothing installed (b.vqr).
hash_refused() {
    local name="$1" want_downloads="$2" want_line="$3"; shift 3
    new_home "$name"
    run_install 0 '*' --from-release v0.11.0-fake --no-symlink "$@"
    report_refused "$name" 3 "$want_line"
    report "$name-download-count" "$(cat "$STATE" 2>/dev/null)" "$want_downloads"
}

# symlinked <name> <dir>: after a successful --from-release install under H,
# <dir> holds agent-director, linked to the installed binary, and nothing
# else, so no agent-director-admin (b.vqr).
symlinked() {
    report "$1-exit-code" "$RC" "0"
    report_installed "$1"
    report "$1-path-dir-entries" "$(ls -A "$2")" "agent-director"
    report "$1-symlink-target" "$(readlink "$2/agent-director")" "$H/.agent-director/bin/agent-director"
}

echo "[b.kym install-sh retry] start"

# Baseline: the retry wrapper must not regress the happy path; one
# download per asset.
scenario happy-path 0 '*' 2
# Two 404s of agent-director, then success: two visible retry lines.
scenario retry-2 2 'agent-director-[!a]*' 4
# All 5 attempts allowed; 4 fail, the 5th succeeds: exactly 4 retry lines
# (no log after the last allowed attempt).
scenario retry-4 4 'agent-director-[!a]*' 6
# The admin asset propagates later than agent-director (b.vqr): its own
# downloads retry the same way, for a release from 0.11.0 on and for a
# tag install.sh cannot read a version from.
scenario admin-retry-2 2 'agent-director-admin-*' 4
scenario admin-retry-2-v1 2 'agent-director-admin-*' 4 v1.0.0
scenario admin-retry-2-unparsed 2 'agent-director-admin-*' 4 nightly-20261003
# A release before 0.11.0 has no admin asset to wait for (b.vqr); v0.0.x
# counts as one.
refused admin-refused-v0.10.0 v0.10.0
refused admin-refused-0.9.12 0.9.12
refused admin-refused-v0.0.0 v0.0.0-fake

# Both hashes right: both assets verified and installed (b.vqr).
new_home sha-both-right
run_install 0 '*' --from-release v0.11.0-fake --no-symlink --sha256 "$BIN_SHA" --admin-sha256 "$ADMIN_SHA"
report sha-both-right-exit-code "$RC" "0"
report sha-both-right-verified-lines "$(grep -cxE '  (sha256  |admin sha256): verified' "$OUT")" "2"
report_installed sha-both-right
# A wrong hash for either asset installs neither; a wrong agent-director hash
# is caught before agent-director-admin is downloaded.
hash_refused sha-wrong-admin 2 "install.sh: --from-release: admin sha256 mismatch" --sha256 "$BIN_SHA" --admin-sha256 "$WRONG_SHA"
hash_refused sha-wrong-main 1 "install.sh: --from-release: sha256 mismatch" --sha256 "$WRONG_SHA" --admin-sha256 "$ADMIN_SHA"
# Either hash flag without --from-release is a bad flag (exit 2).
for flag in --sha256 --admin-sha256; do
    new_home "no-release$flag"
    run_install 0 '*' --binary "$BIN" --admin-binary "$ADMIN" --no-symlink "$flag" "$ADMIN_SHA"
    report_refused "no-release$flag" 2 "install.sh: $flag only applies with --from-release"
done

# agent-director-admin never goes on PATH (b.vqr): not with --symlink-dir,
# and not with the default, ~/.local/bin when it is on PATH.
new_home symlink-dir
mkdir "$H/links"
run_install 0 '*' --from-release v0.11.0-fake --symlink-dir "$H/links"
symlinked symlink-dir "$H/links"
new_home symlink-default
mkdir -p "$H/.local/bin"
PATH_PREFIX="$H/.local/bin"
run_install 0 '*' --from-release v0.11.0-fake
PATH_PREFIX=""
symlinked symlink-default "$H/.local/bin"

# locking_sqlite3 <db>: a sqlite3 in $ROOT/locking that first has another
# sqlite3 process take <db>'s exclusive lock and hold it for 1 s, then runs the
# real one; each call logs "held <ms>" (or "no lock <ms>") to $ROOT/locks.log,
# <ms> being how long the real one took (b.ady). The holder runs with -bail, so
# it marks the lock held only once BEGIN EXCLUSIVE has succeeded.
locking_sqlite3() {
    mkdir -p "$ROOT/locking"
    {
        printf '#!/bin/bash\nSQLITE=%q SLEEP=%q DB=%q LOG=%q\n' \
            "$(type -P sqlite3)" "$(type -P sleep)" "$1" "$ROOT/locks.log"
        cat <<'EOF'
mark="$LOG.$$" lock=held
{ printf 'PRAGMA locking_mode=EXCLUSIVE;\nBEGIN EXCLUSIVE;\nSELECT 1;\n.system touch %s\n' "$mark"; "$SLEEP" 1; echo 'COMMIT;'; } \
    | "$SQLITE" -bail "$DB" >/dev/null 2>&1 &
for _ in {1..300}; do [[ -e "$mark" ]] && break; "$SLEEP" 0.01; done
[[ -e "$mark" ]] || lock="no lock"
start="${EPOCHREALTIME//[!0-9]/}"
"$SQLITE" "$@"
rc=$?
echo "$lock $(( (${EPOCHREALTIME//[!0-9]/} - start) / 1000 ))" >>"$LOG"
exit "$rc"
EOF
    } >"$ROOT/locking/sqlite3"
    chmod 0755 "$ROOT/locking/sqlite3"
}

# An upgrade (the store set one version back) whose user_version reads each
# meet a briefly held store lock reads the right versions, before and after
# the migrating open (b.ady).
new_home locked-store
db="$H/.agent-director/state.db"
run_install 0 '*' --from-release v0.11.0-fake --no-symlink
report locked-store-first-install-exit-code "$RC" "0"
schema="$(sqlite3 "$db" 'PRAGMA user_version;')"
sqlite3 "$db" "PRAGMA user_version = $((schema - 1));"
locking_sqlite3 "$db"
PATH_PREFIX="$ROOT/locking"
run_install 0 '*' --from-release v0.11.0-fake --no-symlink
PATH_PREFIX=""
report locked-store-exit-code "$RC" "0"
report locked-store-reads "$(sed 's/ [0-9]*$//' "$ROOT/locks.log" | paste -sd,)" "held,held"
# Each read started with the lock held for about 1 s more, so one that returned
# in under 0.5 s never waited for it.
echo "  info  locked-store read times (ms): $(awk '{print $NF}' "$ROOT/locks.log" | paste -sd,)"
report locked-store-reads-waited "$(awk '{print ($NF >= 500 ? "waited" : "returned after " $NF " ms")}' "$ROOT/locks.log" | paste -sd,)" "waited,waited"
report locked-store-step2-read "$(grep -cF "authorized migration v$((schema - 1))→v$schema " "$OUT")" "1"
report locked-store-step5-read "$(grep -cF "migration verified — state.db now at v$schema" "$OUT")" "1"

echo "[b.kym install-sh retry] summary: $pass passed, $fail failed"

if [[ "$fail" -ne 0 ]]; then
    # Dump the last stderr for triage on failure.
    echo "--- last stderr ---" >&2
    cat "$ERR" >&2 || true
    exit 1
fi
exit 0
