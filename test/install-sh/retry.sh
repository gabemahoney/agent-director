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
# ~/.sqliterc (b.hk7): an upgrade under an rc file that changes how sqlite3
# prints (.headers on, .mode json) still authorizes, runs and verifies the
# migration. A step-3 mv that cannot move the migration sentinel into place
# leaves no sentinel temp file behind.
#
# Leftover sentinel (b.dzw): a migrate-authorized from before the install that
# the step-3 probe does not consume (a v0 store, a current one, an older one
# it does not match) changes nothing step 3 or step 5 prints. One the probe
# does consume is advice_follow.sh's J6 re-run.
#
# umask (b.7j2): under a umask that takes away the owner's own bits (0777,
# 0222), a --from-release install and an upgrade that migrates still exit 0,
# read and print state.db's schema version, print no "Permission denied", and
# leave no temp file. A hooks-on install that creates ~/.claude gives it and
# settings.json the owner's access, and group/other bits as the umask allows.
#
# [store] db_path (b.2io): with the store moved out of ~/.agent-director
# (written with ~/, as an absolute path that is not clean, or relative), a
# fresh install and an upgrade that migrates read, authorize and verify that
# store, the sentinel written beside it and consumed. A stale default state.db
# beside a moved store is left alone. The default store reads, and is named, as
# before. A db_path install.sh cannot read stops the install (exit 5) before
# anything on disk changes, on a fresh HOME and over an installed store.
#
# [store] busy_timeout_ms (b.c7f): an upgrade's two user_version reads pass
# sqlite3 the configured busy timeout (.timeout), or 10000 with no key or 0;
# with 1 ms the first read gives up on a briefly held store lock that the
# default waits out, and the install stops at step 2 (exit 5). A
# busy_timeout_ms install.sh cannot read is advice_follow.sh's J19.
#
# config.toml merge (b.onv): a hooks-on install, run twice, sets
# inject_help_hook = true inside the [defaults] table however its header is
# spelled (blanks inside or before the brackets, a trailing comment, a CRLF, a
# UTF-8 BOM), before an indented next header, rewriting an existing line and
# appending no second [defaults]; agent-director list then loads the config,
# and uninstall.sh takes the key out again. The header and the key match in any
# letter case (b.hhk): [Defaults] or INJECT_HELP_HOOK ends with one
# inject_help_hook = true, in the table that set the key, else in the first
# defaults table, and never a second spelling; INJECT_HELP_HOOKS is not the key,
# and the key under another table ([defaultsx]) is left as it is.
#
# Merge pre-check (b.whe): a hooks-on install over a config that sets defaults
# as a key before any header (an inline table, in any letter case, right after
# a UTF-8 BOM or after comments and blank lines, CRLF) stops in pre-flight
# (exit 5) before anything on disk changes, on a fresh HOME and over an
# installed store; with --no-hooks the same config installs, left as it was,
# and agent-director list loads it. defaults in a comment, as part of a longer
# key or under another table merges as before.
#
# Merge modes (b.ojn): a hooks-on re-install under umask 022 or 000 leaves
# settings.json (a symlinked one too) and config.toml at the modes they had,
# their .bak copies at the same, also over an earlier run's .new and .bak
# leftovers; each new file and copy is owner-only until its chmod, already has
# its mode when moved into place, and no cp -p is needed (NFS homes).
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
trap 'chmod -R u+rwX "$ROOT" 2>/dev/null; rm -rf "$ROOT"; [[ -z "${SQLITERC_OURS:-}" ]] || rm -f "$PW_SQLITERC"' EXIT

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
H="" RC=0 ERR="" OUT="" STATE="" PATH_PREFIX="" SQLITERC="" UMASK="" HOOKS=""
new_home() {
    H="$(mktemp -d "$ROOT/home.XXXXXX")"
    STATE="$ROOT/$1.count" ERR="$ROOT/$1.err" OUT="$ROOT/$1.out"
}

# run_install <fail-first> <fail-match> <install.sh args...>: install.sh
# <args> under H, with --no-hooks unless HOOKS is set, the first <fail-first>
# downloads of the assets whose name matches the glob <fail-match> answering
# 404, PATH_PREFIX (when set) first on its PATH, SQLITERC (when set) as the
# ~/.sqliterc its sqlite3 sees (sqliterc_on), and UMASK (when set) as its
# umask; sets RC. OUT and ERR are created before UMASK applies.
run_install() {
    local fail_first="$1" fail_match="$2"; shift 2
    [[ -n "$HOOKS" ]] || set -- --no-hooks "$@"
    (
        [[ -z "$UMASK" ]] || umask "$UMASK"
        env -i HOME="$H" PATH="${PATH_PREFIX:+$PATH_PREFIX:}$FAKES:$PATH" TMPDIR="$ROOT/tmp" \
            ${SQLITERC:+"AGENT_DIRECTOR_TEST_SQLITERC=$SQLITERC"} \
            INSTALL_SH_TEST_CURL_OVERRIDE="$FAKE_CURL" \
            FAKE_CURL_STATE_FILE="$STATE" \
            FAKE_CURL_FAIL_FIRST="$fail_first" \
            FAKE_CURL_FAIL_MATCH="$fail_match" \
            FAKE_CURL_BODY_SOURCE="$BIN" \
            FAKE_CURL_ADMIN_BODY_SOURCE="$ADMIN" \
            bash "$INSTALL_SH" "$@"
    ) >"$OUT" 2>"$ERR"
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

# older_store <name>: a first install under a new HOME, then its store set one
# version back so the next install migrates it; sets db and schema (the
# version this build writes).
older_store() {
    new_home "$1"
    db="$H/.agent-director/state.db"
    run_install 0 '*' --from-release v0.11.0-fake --no-symlink
    report "$1-first-install-exit-code" "$RC" "0"
    schema="$(sqlite3 "$db" 'PRAGMA user_version;')"
    sqlite3 "$db" "PRAGMA user_version = $((schema - 1));"
}

# report_upgraded <name>: the upgrade of an older_store exited 0, its step-2
# read authorized v<schema-1>→v<schema>, its step-5 read verified v<schema>,
# and no migrate-authorized file is left.
report_upgraded() {
    report "$1-exit-code" "$RC" "0"
    report "$1-step2-read" "$(grep -cF "authorized migration v$((schema - 1))→v$schema " "$OUT")" "1"
    report "$1-step5-read" "$(grep -cF "migration verified — state.db now at v$schema" "$OUT")" "1"
    report "$1-no-sentinel-left" "$(ls -A "$H/.agent-director" | grep '^migrate-authorized')" ""
}

# An upgrade (the store set one version back) whose user_version reads each
# meet a briefly held store lock reads the right versions, before and after
# the migrating open (b.ady).
older_store locked-store
locking_sqlite3 "$db"
PATH_PREFIX="$ROOT/locking"
run_install 0 '*' --from-release v0.11.0-fake --no-symlink
PATH_PREFIX=""
report locked-store-reads "$(sed 's/ [0-9]*$//' "$ROOT/locks.log" | paste -sd,)" "held,held"
# Each read started with the lock held for about 1 s more, so one that returned
# in under 0.5 s never waited for it.
echo "  info  locked-store read times (ms): $(awk '{print $NF}' "$ROOT/locks.log" | paste -sd,)"
report locked-store-reads-waited "$(awk '{print ($NF >= 500 ? "waited" : "returned after " $NF " ms")}' "$ROOT/locks.log" | paste -sd,)" "waited,waited"
report_upgraded locked-store

# The sandbox user's ~/.sqliterc: sqlite3 looks for it in the passwd home
# before $HOME, so no test HOME hides it (b.hk7).
pw_home="$(getent passwd "$(id -u)" | cut -d: -f6)"
[[ -n "$pw_home" ]] || die "uid $(id -u) has no passwd entry, or one with no home directory (getent passwd); the ~/.sqliterc checks need the passwd home sqlite3 reads"
PW_SQLITERC="$pw_home/.sqliterc"
SQLITERC_OURS=""

# sqliterc_on: under the lock readme_store_id_test.go also takes, write
# PW_SQLITERC so that it runs $AGENT_DIRECTOR_TEST_SQLITERC; a sqlite3 started
# without that variable (in the test packages running beside this one) sees no
# change. Never replaces a file already there. sqliterc_off undoes it.
sqliterc_on() {
    exec {SQLITERC_LOCK}>>"${TMPDIR:-/tmp}/agent-director-sqliterc.lock" && flock "$SQLITERC_LOCK" \
        || die "lock ${TMPDIR:-/tmp}/agent-director-sqliterc.lock"
    (set -o noclobber; cat >"$PW_SQLITERC") <<'EOF' || die "cannot create $PW_SQLITERC (it exists, which this never replaces, or cannot be written; reason above)"
.read '|printf "%s\n" "$AGENT_DIRECTOR_TEST_SQLITERC"'
EOF
    SQLITERC_OURS=1
}
sqliterc_off() {
    rm -f "$PW_SQLITERC"
    SQLITERC_OURS=""
    exec {SQLITERC_LOCK}>&-
}

# An upgrade under a ~/.sqliterc that changes what sqlite3 prints reads the
# right versions, before and after the migrating open (b.hk7). <bare> is what
# a plain sqlite3 read prints under it (V the version), showing it applies.
for spec in '.headers on|user_version,V' '.mode json|[{"user_version":V}]'; do
    rc="${spec%%|*}" bare="${spec#*|}"
    name="${rc#.}" && name="sqliterc-${name// /-}"
    older_store "$name"
    sqliterc_on
    SQLITERC="$rc"
    report "$name-bare-read" "$(AGENT_DIRECTOR_TEST_SQLITERC="$rc" sqlite3 "$db" 'PRAGMA user_version;' | paste -sd,)" \
        "${bare//V/$((schema - 1))}"
    run_install 0 '*' --from-release v0.11.0-fake --no-symlink
    SQLITERC=""
    sqliterc_off
    report_upgraded "$name"
done

# report_clean_run <name>: the last run printed state.db's schema version as
# v<schema>, no "Permission denied", and left no temp file (b.7j2).
report_clean_run() {
    report "$1-schema-shown" "$(grep -c "^  state.db: .* (schema v$schema)\$" "$OUT")" "1"
    report "$1-permission-denied" "$(grep -c "Permission denied" "$ERR")" "0"
    report "$1-no-temp-left" "$(compgen -G "$ROOT/tmp/agent-director*")" ""
}

# Under a umask that takes away the owner's own bits, a fresh --from-release
# install and an upgrade that migrates both succeed: the downloads land, and
# steps 2 and 5 read state.db's version (b.7j2).
for mask in 0777 0222; do
    older_store "umask-$mask-upgrade"
    UMASK="$mask"
    run_install 0 '*' --binary "$BIN" --admin-binary "$ADMIN" --no-symlink
    UMASK=""
    report_upgraded "umask-$mask-upgrade"
    report "umask-$mask-upgrade-user-version" "$(sqlite3 "$db" 'PRAGMA user_version;')" "$schema"
    report_clean_run "umask-$mask-upgrade"

    new_home "umask-$mask-fresh"
    UMASK="$mask"
    run_install 0 '*' --from-release v0.11.0-fake --no-symlink
    UMASK=""
    report "umask-$mask-fresh-exit-code" "$RC" "0"
    report_installed "umask-$mask-fresh"
    report_clean_run "umask-$mask-fresh"
done

# A hooks-on install with no ~/.claude creates it and settings.json with the
# owner's access under any umask, and group/other bits as the operator's umask
# allows: <umask>:<~/.claude mode>/<settings.json mode> (b.7j2). Only the
# permission bits count: ~/.claude inherits a setgid bit from a setgid TMPDIR.
HOOKS=1
for spec in 0777:700/600 077:700/600 0227:750/640; do
    mask="${spec%%:*}"
    new_home "umask-$mask-hooks"
    UMASK="$mask"
    run_install 0 '*' --binary "$BIN" --admin-binary "$ADMIN" --no-symlink
    UMASK=""
    report "umask-$mask-hooks-exit-code" "$RC" "0"
    report "umask-$mask-hooks-claude-modes" \
        "$(stat -c %a "$H/.claude" "$H/.claude/settings.json" 2>/dev/null | sed 's/^.*\(...\)$/\1/' | paste -sd/)" \
        "${spec#*:}"
done
HOOKS=""

# An upgrade whose step-3 mv cannot move the migration sentinel into place
# stops there, and leaves no sentinel temp file behind (b.hk7).
older_store sentinel-mv-fails
mkdir -p "$ROOT/mv-fails"
cat >"$ROOT/mv-fails/mv" <<EOF
#!/bin/bash
[[ "\${!#}" == */migrate-authorized ]] && { echo "mv: b.hk7 stand-in cannot move to \${!#}" >&2; exit 1; }
exec $(type -P mv) "\$@"
EOF
chmod 0755 "$ROOT/mv-fails/mv"
PATH_PREFIX="$ROOT/mv-fails"
run_install 0 '*' --from-release v0.11.0-fake --no-symlink
PATH_PREFIX=""
report sentinel-mv-fails-stopped-at-mv "$RC $(grep -c "b.hk7 stand-in" "$ERR")" "1 1"
report sentinel-mv-fails-nothing-left "$(ls -A "$H/.agent-director" | grep '^migrate-authorized')" ""

# A sentinel left beside the store from before that the step-3 probe does not
# consume (b.dzw): at a v0 store (created, not migrated), a current one (never
# read) and, not matching, an older one (the probe refuses; step 3 writes its
# own). Step 3 and step 5 read as with no sentinel.
for name in v0 current older-mismatched; do
    older_store "leftover-sentinel-$name"
    sentinel="$H/.agent-director/migrate-authorized" from=$((schema - 1)) left=migrate-authorized
    lines="  schema  : state.db at v$schema; no migration authorization needed"
    case "$name" in
        v0)
            rm -f "$db"* && : >"$db"
            lines="  schema  : state.db at v0; no migration authorization needed" ;;
        current) sqlite3 "$db" "PRAGMA user_version = $schema;" ;;
        older-mismatched)
            from=$((schema - 2)) left=""
            lines="  schema  : authorized migration v$((schema - 1))→v$schema (sentinel $sentinel)|  schema  : migration verified — state.db now at v$schema" ;;
    esac
    printf '{"from": %d, "to": %d}\n' "$from" "$schema" >"$sentinel"
    run_install 0 '*' --from-release v0.11.0-fake --no-symlink
    report "leftover-sentinel-$name-exit-code" "$RC" "0"
    report "leftover-sentinel-$name-schema-lines" "$(grep '^  schema  : ' "$OUT" | paste -sd'|')" "$lines"
    report "leftover-sentinel-$name-user-version" "$(sqlite3 "$db" 'PRAGMA user_version;')" "$schema"
    report "leftover-sentinel-$name-sentinel" "$(ls -A "$H/.agent-director" | grep '^migrate-authorized')" "$left"
done

# with_config <content>: H's ~/.agent-director/config.toml holds <content>
# (printf %b).
with_config() {
    mkdir -p "$H/.agent-director"
    printf '%b\n' "$1" >"$H/.agent-director/config.toml"
}

# local_install: install BIN and ADMIN under H; sets RC.
local_install() { run_install 0 '*' --binary "$BIN" --admin-binary "$ADMIN" --no-symlink "$@"; }

# db_path_upgrade <name> <db> <shown> <dir>: under H's config, a fresh install
# then an upgrade with the store one version back; <db> is the store install.sh
# must read, authorize and verify (b.2io), <shown> how its messages name it
# ("state.db" for the default, otherwise its path) and <dir> the clean directory
# whose migrate-authorized authorizes it. Only a store outside the default
# prints a "store" pre-flight line, and no other state.db appears.
db_path_upgrade() {
    local name="$1" db="$2" shown="$3" dir="$4" v store_line="" default="$H/.agent-director/state.db"
    [[ "$shown" == state.db ]] || store_line="  store   : $db ([store] db_path in $H/.agent-director/config.toml)"
    local_install
    report "$name-fresh-exit-code" "$RC" "0"
    report "$name-fresh-store-line" "$(grep '^  store   : ' "$OUT")" "$store_line"
    report "$name-fresh-create-read" "$(grep -cxF "  schema  : no existing $shown — fresh create on first open" "$OUT")" "1"
    v="$(sqlite3 "$db" 'PRAGMA user_version;')"
    report "$name-fresh-read" "$(grep '^  state\.db: ' "$OUT" | sed 's/^  state\.db: [0-7]* at //')" "$db (schema v$v)"
    sqlite3 "$db" "PRAGMA user_version = $((v - 1));"
    local_install
    report "$name-upgrade-exit-code" "$RC" "0"
    report "$name-upgrade-store-line" "$(grep '^  store   : ' "$OUT")" "$store_line"
    report "$name-upgrade-sentinel-beside-db" \
        "$(grep -cxF "  schema  : authorized migration v$((v - 1))→v$v (sentinel $dir/migrate-authorized)" "$OUT")" "1"
    report "$name-upgrade-verified" "$(grep -cxF "  schema  : migration verified — $shown now at v$v" "$OUT")" "1"
    report "$name-upgrade-user-version" "$(sqlite3 "$db" 'PRAGMA user_version;')" "$v"
    report "$name-upgrade-sentinel-consumed" "$(compgen -G "$dir/migrate-authorized*"; compgen -G "$H/.agent-director/migrate-authorized*")" ""
    if [[ "$db" -ef "$default" ]]; then
        report "$name-store-is-default" "$(compgen -G "$H/.agent-director/*.db")" "$default"
    else
        report "$name-no-default-store" "$(compgen -G "$default*")" ""
    fi
}

# [store] db_path moves the store out of ~/.agent-director (b.2io): written
# with ~/, as an absolute path that is not clean (used as written, as the
# binary does), or relative to ~/.agent-director; each installs fresh and
# then upgrades with a migration authorized beside that store.
new_home db-path-tilde
with_config '[store]\ndb_path = "~/custom/agents.db"'
db_path_upgrade db-path-tilde "$H/custom/agents.db" "$H/custom/agents.db" "$H/custom"
new_home db-path-absolute
with_config "[store]\ndb_path = '$H//custom/./agents.db' # moved"
db_path_upgrade db-path-absolute "$H//custom/./agents.db" "$H//custom/./agents.db" "$H/custom"
new_home db-path-relative
with_config '[defaults]\nrelay_mode = "off"\n\n[store]\ndb_path = "../custom/agents.db"'
db_path_upgrade db-path-relative "$H/custom/agents.db" "$H/custom/agents.db" "$H/custom"
# The default store reads, and is named, as before, with no config, an empty
# db_path or one that cleans to the default.
for spec in "none|" 'empty|[store]\ndb_path = ""' 'unclean|[store]\ndb_path = "~/.agent-director/./state.db"'; do
    new_home "db-path-default-${spec%%|*}"
    [[ "${spec%%|*}" == none ]] || with_config "${spec#*|}"
    db_path_upgrade "db-path-default-${spec%%|*}" "$H/.agent-director/state.db" state.db "$H/.agent-director"
done

# A stale default state.db beside the db_path store (b.2io): the upgrade
# migrates the db_path store and verifies it, and leaves state.db alone.
new_home db-path-stale-default
with_config '[store]\ndb_path = "real.db"'
local_install
report db-path-stale-default-first-install-exit-code "$RC" "0"
real="$H/.agent-director/real.db" stale="$H/.agent-director/state.db"
v="$(sqlite3 "$real" 'PRAGMA user_version;')"
sqlite3 "$real" "PRAGMA user_version = $((v - 1));"
sqlite3 "$stale" "CREATE TABLE stale (x); PRAGMA user_version = $((v - 1));"
stale_sum="$(sha256sum "$stale")"
local_install
report db-path-stale-default-exit-code "$RC" "0"
report db-path-stale-default-no-false-failure "$(grep -c "FAILED" "$ERR")" "0"
report db-path-stale-default-verified "$(grep -cxF "  schema  : migration verified — $real now at v$v" "$OUT")" "1"
report db-path-stale-default-real-version "$(sqlite3 "$real" 'PRAGMA user_version;')" "$v"
report db-path-stale-default-stale-untouched "$(sha256sum "$stale")" "$stale_sum"
report db-path-stale-default-sentinel-consumed "$(compgen -G "$H/.agent-director/migrate-authorized*")" ""

# A sqlite3 in $ROOT/timeout-log that logs the value of each -cmd it is given
# to $ROOT/timeouts.log, then runs the real one (b.c7f).
mkdir -p "$ROOT/timeout-log"
{
    printf '#!/bin/bash\nSQLITE=%q LOG=%q\n' "$(type -P sqlite3)" "$ROOT/timeouts.log"
    cat <<'EOF'
prev=""
for a; do [[ "$prev" == -cmd ]] && printf '%s\n' "$a" >>"$LOG"; prev="$a"; done
exec "$SQLITE" "$@"
EOF
} >"$ROOT/timeout-log/sqlite3"
chmod 0755 "$ROOT/timeout-log/sqlite3"

# [store] busy_timeout_ms (b.c7f): an upgrade's two user_version reads wait as
# long as agent-director's store connections do: the configured busy timeout,
# or the default for no key or 0. Per case <name>|<config>|<ms>.
while IFS='|' read -r -u 3 name config ms; do
    name="busy-timeout-$name"
    older_store "$name"
    [[ -z "$config" ]] || with_config "$config"
    : >"$ROOT/timeouts.log"
    PATH_PREFIX="$ROOT/timeout-log"
    local_install
    PATH_PREFIX=""
    report_upgraded "$name"
    report "$name-read-timeouts" "$(paste -sd, "$ROOT/timeouts.log")" ".timeout $ms,.timeout $ms"
done 3<<'EOF'
none||10000
zero|[store]\nbusy_timeout_ms = 0|10000
set|[store]\nbusy_timeout_ms = 1_234 # ms|1234
crlf-beside-db-path|[store]\r\ndb_path = "~/.agent-director/state.db"\r\nbusy_timeout_ms = 2500\r|2500
EOF

# With busy_timeout_ms = 1, the upgrade's first read gives up on a briefly held
# store lock that the default waits out (locked-store above): the install stops
# at step 2 (exit 5), authorizing nothing (b.c7f).
older_store busy-timeout-short-locked
with_config '[store]\nbusy_timeout_ms = 1'
rm -f "$ROOT/locks.log"
locking_sqlite3 "$db"
PATH_PREFIX="$ROOT/locking"
local_install
PATH_PREFIX=""
report busy-timeout-short-locked-exit-code "$RC" "5"
report busy-timeout-short-locked-first-stderr-line "$(head -n 1 "$ERR")" "install.sh: reading state.db's schema version FAILED"
report busy-timeout-short-locked-reads "$(sed 's/ [0-9]*$//' "$ROOT/locks.log" | paste -sd,)" "held"
report busy-timeout-short-locked-nothing-authorized "$(ls -A "$H/.agent-director" | grep '^migrate-authorized')" ""
# The stand-in's holder may still hold the lock: wait it out.
report busy-timeout-short-locked-user-version "$(sqlite3 -cmd '.timeout 5000' "$db" 'PRAGMA user_version;')" "$((schema - 1))"

# snap: every path under H with its type, mode, size and mtime, and every
# file's sha256.
snap() {
    (cd "$H" && find . -printf '%p %y %m %s %T@\n' | sort && find . -type f -exec sha256sum {} + | sort)
}

# preflight_refused <name> <config> <line> [<first stderr line>]: install.sh
# (hooks on, --keep-prior) under H's <config> stops in pre-flight with exit 5,
# that refusal (the db_path reader's when not given) first on stderr, naming
# the config file and its line <line>, and leaves H and TMPDIR as they were
# (b.2io, b.whe).
preflight_refused() {
    local name="$1" before
    with_config "$2"
    before="$(snap)"
    HOOKS=1
    local_install --keep-prior
    HOOKS=""
    report "$name-exit-code" "$RC" "5"
    report "$name-first-stderr-line" "$(head -n 1 "$ERR")" \
        "${4:-install.sh: cannot tell which store database agent-director opens; refusing to install.}"
    report "$name-names-config" "$(grep -cxF "  config  : $H/.agent-director/config.toml" "$ERR")" "1"
    report "$name-names-line" "$(grep -cxF "  line $3" "$ERR")" "1"
    report "$name-no-pre-flight-ok" "$(grep -c "pre-flight OK" "$OUT")" "0"
    report "$name-home-unchanged" "$(diff <(echo "$before") <(snap) >/dev/null && echo same)" "same"
    report "$name-no-temp-left" "$(compgen -G "$ROOT/tmp/agent-director*")" ""
}

# A db_path install.sh cannot read stops the install before anything on disk
# changes: on a fresh HOME, and over an installed store (b.2io).
new_home db-path-refused-fresh
preflight_refused db-path-refused-fresh '[store]\ndb_path = "C:\\\\agents\\\\state.db"' '2  : db_path = "C:\\agents\\state.db"'
new_home db-path-refused-installed
local_install
report db-path-refused-installed-first-install-exit-code "$RC" "0"
preflight_refused db-path-refused-installed '[defaults]\nrelay_mode = "off"\n[Store]\ndb_path = "/elsewhere/agents.db"' '3  : [Store]'

# shown <file>: <file>'s bytes on one line, as cat -A shows them (^M a CR,
# M-oM-;M-? a UTF-8 BOM, $ a line end), its lines joined by |.
shown() { cat -A "$1" | paste -sd'|'; }

# ad_list: H's installed agent-director list; prints 0, or its exit code and
# output.
ad_list() {
    local out rc
    out="$(env -i HOME="$H" PATH="$FAKES:$PATH" TMPDIR="$ROOT/tmp" "$H/.agent-director/bin/agent-director" list 2>&1)"
    rc=$?
    if [[ "$rc" -eq 0 ]]; then echo 0; else echo "$rc $out"; fi
}

# A hooks-on install merges inject_help_hook = true into the [defaults] table
# however its header is spelled, in any letter case, and uninstall.sh takes it
# out again (b.onv, b.hhk). Per case <name>|<config>|<merged>|<uninstalled>
# (printf %b, a newline added): two installs both leave <merged>,
# agent-director list loads it, and uninstall.sh leaves <uninstalled> (<config>
# when empty, an empty line when -).
UNINSTALL_SH="${REPO_ROOT}/skills/install-agent-director/uninstall.sh"
HOOKS=1
while IFS='|' read -r -u 3 name config merged uninstalled; do
    name="merge-$name"
    new_home "$name"
    with_config "$config"
    cfg="$H/.agent-director/config.toml"
    local_install
    rc="$RC" first="$(shown "$cfg")"
    local_install
    want="$(shown <(printf '%b\n' "$merged"))"
    report "$name-exit-codes" "$rc $RC" "0 0"
    report "$name-config" "$first" "$want"
    report "$name-config-reinstalled" "$(shown "$cfg")" "$want"
    report "$name-list" "$(ad_list)" "0"
    env -i HOME="$H" PATH="$FAKES:$PATH" bash "$UNINSTALL_SH" >"$OUT" 2>"$ERR"
    report "$name-uninstall-exit-code" "$?" "0"
    left="${uninstalled:-$config}"
    [[ "$uninstalled" == - ]] && left=""
    report "$name-config-uninstalled" "$(shown "$cfg")" "$(shown <(printf '%b\n' "$left"))"
done 3<<'EOF'
spaced|[ defaults ]\nrelay_mode = "off"|[ defaults ]\nrelay_mode = "off"\ninject_help_hook = true|
comment|[defaults] # mine\nrelay_mode = "off"|[defaults] # mine\nrelay_mode = "off"\ninject_help_hook = true|
indented-key-false|\t [defaults]\t# mine\ninject_help_hook = false\nrelay_mode = "off"|\t [defaults]\t# mine\ninject_help_hook = true\nrelay_mode = "off"|\t [defaults]\t# mine\nrelay_mode = "off"
then-table|[ defaults ] # c\n[relay]\npoll_base_ms = 100|[ defaults ] # c\ninject_help_hook = true\n[relay]\npoll_base_ms = 100|[relay]\npoll_base_ms = 100
then-indented-table|[defaults]\nrelay_mode = "off"\n  [relay]\n  poll_base_ms = 100|[defaults]\nrelay_mode = "off"\ninject_help_hook = true\n  [relay]\n  poll_base_ms = 100|
bom|\xef\xbb\xbf[defaults]\nrelay_mode = "off"|\xef\xbb\xbf[defaults]\nrelay_mode = "off"\ninject_help_hook = true|
crlf|[ defaults ]\r\nrelay_mode = "off"\r|[ defaults ]\r\nrelay_mode = "off"\r\ninject_help_hook = true|
exact|[defaults]\nrelay_mode = "off"|[defaults]\nrelay_mode = "off"\ninject_help_hook = true|
no-defaults|[defaultsx]\nrelay_mode = "off"|[defaultsx]\nrelay_mode = "off"\n\n[defaults]\ninject_help_hook = true|
key-in-other-table|[defaultsx]\nINJECT_HELP_HOOK = false\nrelay_mode = "off"|[defaultsx]\nINJECT_HELP_HOOK = false\nrelay_mode = "off"\n\n[defaults]\ninject_help_hook = true|
defaults-key-elsewhere|# defaults = { relay_mode = "on" }\ndefaults_x = 1\n[relay]\ndefaults = 1|# defaults = { relay_mode = "on" }\ndefaults_x = 1\n[relay]\ndefaults = 1\n\n[defaults]\ninject_help_hook = true|
case-header|[Defaults]\ninject_help_hook = false|[Defaults]\ninject_help_hook = true|-
case-key|[defaults]\nINJECT_HELP_HOOK = false|[defaults]\ninject_help_hook = true|-
case-header-no-key|[Defaults]\nrelay_mode = "off"\n[relay]\npoll_base_ms = 100|[Defaults]\nrelay_mode = "off"\ninject_help_hook = true\n[relay]\npoll_base_ms = 100|
case-first-table|[ DEFAULTS ] # mine\nrelay_mode = "off"\n[defaults]\nexpire_retention_days = 7|[ DEFAULTS ] # mine\nrelay_mode = "off"\ninject_help_hook = true\n[defaults]\nexpire_retention_days = 7|
case-key-in-later-table|[Defaults]\nrelay_mode = "off"\n[relay]\npoll_base_ms = 100\n[defaults]\nInject_Help_Hook = false|[Defaults]\nrelay_mode = "off"\n[relay]\npoll_base_ms = 100\n[defaults]\ninject_help_hook = true|[Defaults]\nrelay_mode = "off"\n[relay]\npoll_base_ms = 100
case-longer-key|[Defaults]\nINJECT_HELP_HOOKS = 1|[Defaults]\nINJECT_HELP_HOOKS = 1\ninject_help_hook = true|
EOF
HOOKS=""

# A hooks-on install over a config that sets defaults as a key before any
# header (an inline table, in any letter case, right after a UTF-8 BOM or
# indented after a comment and a blank line, CRLF) stops in pre-flight (exit 5)
# and changes nothing: the [defaults] the merge would add leaves a file
# agent-director refuses (b.whe). The refusal names the line without its BOM
# or CR. Per case <name>|<store>|<config>|<line>: <store> fresh or installed (a
# --no-hooks install first). The plain inline table is J16's first case in
# advice_follow.sh.
while IFS='|' read -r -u 3 name store config line; do
    name="merge-refused-$name"
    new_home "$name"
    if [[ "$store" == installed ]]; then
        local_install
        report "$name-first-install-exit-code" "$RC" "0"
    fi
    preflight_refused "$name" "$config" "$line" \
        "install.sh: cannot merge inject_help_hook = true into config.toml's [defaults] table; refusing to install."
done 3<<'EOF'
bom-crlf|fresh|\xef\xbb\xbfdefaults = { relay_mode = "off" }\r\n[relay]\r\npoll_base_ms = 100\r|1  : defaults = { relay_mode = "off" }
case|installed|Defaults = { inject_help_hook = false }\n[relay]\npoll_base_ms = 100|1  : Defaults = { inject_help_hook = false }
indented-crlf|fresh|# mine\r\n\r\n  defaults={}\r\n[relay]\r\npoll_base_ms = 100\r|3  :   defaults={}
EOF

# With --no-hooks the config is never merged and agent-director loads it, so
# the same config installs and is left as it was (b.whe).
new_home merge-no-hooks-inline
with_config 'defaults = { relay_mode = "off" }\n[relay]\npoll_base_ms = 100'
cfg="$H/.agent-director/config.toml"
before="$(shown "$cfg")"
local_install
report merge-no-hooks-inline-exit-code "$RC" "0"
report merge-no-hooks-inline-config "$(shown "$cfg")" "$before"
report merge-no-hooks-inline-no-backup "$(compgen -G "$cfg.*")" ""
report merge-no-hooks-inline-list "$(ad_list)" "0"

# Stand-ins for the b.ojn re-installs: an mv that logs "<target name> <mode>"
# of each new settings.json or config.toml it moves into place, a chmod that
# logs "<target name> <mode>" of each of their .new and .bak files before
# changing it, a cp that fails on -p or -a as on an NFS home (LP#2087769), and a
# date that names every .bak copy with BAK_STAMP.
KEEP="$ROOT/keep-mode" BAK_STAMP=20260101-000000
mkdir -p "$KEEP"
{
    printf '#!/bin/bash\nMV=%q LOG=%q\n' "$(type -P mv)" "$KEEP/mv.log"
    cat <<'EOF'
t="${!#}"
case "$t" in */settings.json|*/config.toml) echo "${t##*/} $(stat -c %a "${@: -2:1}")" >>"$LOG" ;; esac
exec "$MV" "$@"
EOF
} >"$KEEP/mv"
{
    printf '#!/bin/bash\nCHMOD=%q LOG=%q\n' "$(type -P chmod)" "$KEEP/chmod.log"
    cat <<'EOF'
t="${!#}"
case "$t" in */settings.json.new|*/config.toml.new|*/settings.json.bak.*|*/config.toml.bak.*) echo "${t##*/} $(stat -c %a "$t")" >>"$LOG" ;; esac
exec "$CHMOD" "$@"
EOF
} >"$KEEP/chmod"
{
    printf '#!/bin/bash\nCP=%q\n' "$(type -P cp)"
    cat <<'EOF'
for a; do [[ "$a" =~ ^(-[^-]*[ap]|--preserve|--archive) ]] && { echo "cp: b.ojn stand-in cannot preserve attributes here" >&2; exit 1; }; done
exec "$CP" "$@"
EOF
} >"$KEEP/cp"
{
    printf '#!/bin/bash\nDATE=%q BAK_STAMP=%q\n' "$(type -P date)" "$BAK_STAMP"
    cat <<'EOF'
[[ "$*" == '+%Y%m%d-%H%M%S' ]] && exec echo "$BAK_STAMP"
exec "$DATE" "$@"
EOF
} >"$KEEP/date"
chmod 0755 "$KEEP/mv" "$KEEP/chmod" "$KEEP/cp" "$KEEP/date"

# A hooks-on re-install keeps the modes of the settings.json and config.toml it
# merges into, and gives their .bak copies the same (b.ojn); each .new is 600
# and each .bak its original's owner bits until chmod gives it its mode. Per case
# <name>|<umask>|<config mode>|<settings mode>|<setup>: after a first install,
# both files get unmerged contents at those modes, and a re-install under
# <umask> merges them. <setup> symlink makes settings.json a link to a file
# elsewhere; leftovers adds 0666 junk at both .new and .bak.BAK_STAMP names.
HOOKS=1
while IFS='|' read -r -u 3 name mask cmode smode setup; do
    name="keep-mode-$name"
    new_home "$name"
    local_install
    rc="$RC" cfg="$H/.agent-director/config.toml" sj="$H/.claude/settings.json"
    if [[ "$setup" == symlink ]]; then
        mkdir "$H/dotfiles" && mv "$sj" "$H/dotfiles/settings.json" && ln -s "$H/dotfiles/settings.json" "$sj"
    fi
    printf '[defaults]\nrelay_mode = "off"\n' >"$cfg"
    printf '{"theme":"dark"}\n' >"$sj"
    chmod "$cmode" "$cfg" && chmod "$smode" "$sj"
    if [[ "$setup" == leftovers ]]; then
        for f in "$cfg.new" "$cfg.bak.$BAK_STAMP" "$sj.new" "$sj.bak.$BAK_STAMP"; do
            echo junk >"$f" && chmod 0666 "$f"
        done
    fi
    : >"$KEEP/mv.log"
    : >"$KEEP/chmod.log"
    UMASK="$mask" PATH_PREFIX="$KEEP"
    local_install
    UMASK="" PATH_PREFIX=""
    report "$name-exit-codes" "$rc $RC" "0 0"
    report "$name-no-cp-p" "$(grep -c 'b.ojn stand-in' "$ERR")" "0"
    report "$name-modes" "$(stat -L -c %a "$cfg" "$sj" "$cfg.bak.$BAK_STAMP" "$sj.bak.$BAK_STAMP" 2>/dev/null | paste -sd/)" \
        "$cmode/$smode/$cmode/$smode"
    report "$name-written-owner-only" "$(sort "$KEEP/chmod.log" | paste -sd,)" \
        "config.toml.bak.$BAK_STAMP ${cmode:0:1}00,config.toml.new 600,settings.json.bak.$BAK_STAMP ${smode:0:1}00,settings.json.new 600"
    report "$name-new-file-modes" "$(sort "$KEEP/mv.log" | paste -sd,)" "config.toml $cmode,settings.json $smode"
    report "$name-no-temp-left" "$(compgen -G "$cfg.new"; compgen -G "$sj.new")" ""
    report "$name-merged" \
        "$(grep -cx 'inject_help_hook = true' "$cfg") $(jq -c '[.theme, (.hooks.SessionStart | length), (.hooks.SessionEnd | length)]' "$sj" 2>&1)" \
        '1 ["dark",1,1]'
    report "$name-backups" "$(cat "$cfg.bak.$BAK_STAMP" "$sj.bak.$BAK_STAMP" 2>&1 | paste -sd'|')" '[defaults]|relay_mode = "off"|{"theme":"dark"}'
done 3<<'EOF'
private|022|600|600|
group|022|640|660|
read-only|000|400|400|
symlink|022|600|600|symlink
leftovers|022|600|600|leftovers
EOF
HOOKS=""

echo "[b.kym install-sh retry] summary: $pass passed, $fail failed"

if [[ "$fail" -ne 0 ]]; then
    # Dump the last stderr for triage on failure.
    echo "--- last stderr ---" >&2
    cat "$ERR" >&2 || true
    exit 1
fi
exit 0
