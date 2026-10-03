#!/usr/bin/env bash
# advice_follow.sh — b.fji literal-follow tests for install.sh's own advice
# (advice inventory J1-J8). Each test triggers one install.sh refusal, checks
# the advice text word for word, does exactly what the text says (re-runs the
# same command, runs the advised command, puts the missing tool on PATH) and
# checks the promised outcome.
#
# Sandbox only: it builds and runs agent-director binaries. make test-sandbox
# runs it through advice_follow_test.go; alone, run
#   make test-install-sh-advice
# or make sandbox CMD="bash test/install-sh/advice_follow.sh". Every
# install.sh run gets its own HOME under a private temp root, PATH holding only
# a toolbox of links and fakes (fake claude, file when the image lacks it, curl
# that never touches the network, an instant sleep) and `env -i`, so nothing
# can reach the real ~/.agent-director.
#
# Advice that does not work as written is a known-broken test: it is skipped
# unless AGENT_DIRECTOR_RUN_KNOWN_BROKEN_ADVICE=1, which runs it and shows it
# fail.
#
# The tests run install.sh unmodified. On a host install.sh refuses (it accepts
# only Linux/x86_64 and Darwin/arm64, so a Linux/aarch64 sandbox, Docker on
# Apple silicon, is refused) no J test can run: the script runs none, prints
# one "advice_follow.sh: SKIP: <reason>" line and exits SKIP_RC (77), which
# advice_follow_test.go reports as a skip, never a pass.

set -uo pipefail

if [[ "${AGENT_DIRECTOR_TEST_SANDBOX:-}" != 1 ]]; then
    echo "advice_follow.sh: refusing to run outside the sandbox (AGENT_DIRECTOR_TEST_SANDBOX=1 unset);" \
        "run: make sandbox CMD=\"bash test/install-sh/advice_follow.sh\"" >&2
    exit 1
fi

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$HERE/../.." && pwd)"
INSTALL_SRC="$REPO_ROOT/skills/install-agent-director/install.sh"
SQLITE="$(command -v sqlite3)"
ROOT="$(mktemp -d -t ad-advice-install.XXXXXX)"
trap 'rm -rf "$ROOT"' EXIT

die() { echo "advice_follow.sh: setup failed: $*" >&2; exit 1; }

# ---- host gate -------------------------------------------------------------

# Ask install.sh itself whether it accepts this host: a fresh HOME under the
# private root and a --binary that does not exist, so on a host it accepts the
# run stops at a later refusal and installs nothing.
SKIP_RC=77
HOST_ARCH="$(uname -s)/$(uname -m)"
mkdir -p "$ROOT/gate-home" "$ROOT/tmp"
gate_err="$(cd "$ROOT" && env -i HOME="$ROOT/gate-home" PATH="$PATH" TMPDIR="$ROOT/tmp" \
    bash "$INSTALL_SRC" --binary "$ROOT/no-such-binary" --no-hooks --no-symlink 2>&1 >/dev/null)"
gate_rc=$?
if [[ "$gate_rc" -eq 2 && "$gate_err" == *"install.sh: unsupported host: $HOST_ARCH."* ]]; then
    echo "advice_follow.sh: SKIP: install.sh refuses $HOST_ARCH hosts; J tests need a supported host"
    printf '%s\n' "$gate_err" | sed 's/^/    /'
    exit "$SKIP_RC"
fi

# The wrong-architecture binary (J4) is the current one with its ELF machine
# field set to the other of the two architectures the sandbox image is built for.
case "$HOST_ARCH" in
    Linux/x86_64) WRONG_MACHINE='\xb7\x00' WRONG_NAME=aarch64 ;; # EM_AARCH64
    Linux/aarch64) WRONG_MACHINE='\x3e\x00' WRONG_NAME=x86-64 ;; # EM_X86_64
    *) echo "advice_follow.sh: unsupported sandbox host $HOST_ARCH (want Linux/x86_64 or Linux/aarch64)" >&2; exit 1 ;;
esac

# ---- binaries -------------------------------------------------------------

mkdir -p "$ROOT/bin" "$ROOT/tmp" "$ROOT/runs" "$ROOT/aside"
BIN="$ROOT/bin/agent-director"
BIN_OLD="$ROOT/bin/agent-director-old"
BIN_NEWER="$ROOT/bin/agent-director-newer"
WRONG_ARCH="$ROOT/bin/agent-director-$WRONG_NAME"
VERSION_PKG=github.com/gabemahoney/agent-director/internal/version
(cd "$REPO_ROOT" && CGO_ENABLED=0 go build -o "$BIN" ./cmd/agent-director) || die "go build"
(cd "$REPO_ROOT" && CGO_ENABLED=0 go build -ldflags "-X $VERSION_PKG.Version=0.0.1-advice-old" \
    -o "$BIN_OLD" ./cmd/agent-director) || die "go build (old)"
cmp -s "$BIN" "$BIN_OLD" && die "old and current binaries are identical"
# A wrong-arch binary as file(1) sees it: the ELF machine field says $WRONG_NAME.
cp "$BIN" "$WRONG_ARCH"
printf "$WRONG_MACHINE" | dd of="$WRONG_ARCH" bs=1 seek=18 conv=notrunc status=none || die "patch e_machine"

# SCHEMA is the schema version $BIN writes; BIN_NEWER is the same source with
# one more (store.go swapped in through a build overlay, the tree untouched).
mkdir -p "$ROOT/probe-home"
HOME="$ROOT/probe-home" "$BIN" list >/dev/null || die "probe list"
SCHEMA="$("$SQLITE" "$ROOT/probe-home/.agent-director/state.db" 'PRAGMA user_version;')"
[[ "$SCHEMA" =~ ^[0-9]+$ ]] || die "read schema version: $SCHEMA"
sed -E "s/^const schemaVersion = [0-9]+$/const schemaVersion = $((SCHEMA + 1))/" \
    "$REPO_ROOT/internal/store/store.go" >"$ROOT/store-newer.go"
grep -qx "const schemaVersion = $((SCHEMA + 1))" "$ROOT/store-newer.go" || die "no schemaVersion const in store.go"
printf '{"Replace":{"%s":"%s"}}\n' "$REPO_ROOT/internal/store/store.go" "$ROOT/store-newer.go" >"$ROOT/overlay.json"
(cd "$REPO_ROOT" && CGO_ENABLED=0 go build -overlay "$ROOT/overlay.json" -o "$BIN_NEWER" ./cmd/agent-director) \
    || die "go build (newer)"

# ---- install.sh copies ----------------------------------------------------

# install_copy <dest>: install.sh, byte for byte, at dest.
install_copy() {
    mkdir -p "$(dirname "$1")" || die "mkdir for $1"
    cp "$INSTALL_SRC" "$1" || die "copy install.sh to $1"
}

# LOOSE: install.sh outside any git checkout, with no bin/ beside it.
LOOSE="$ROOT/loose/skills/install-agent-director/install.sh"
install_copy "$LOOSE"
# TREE: a git checkout of the module (so `make build` and the source-tree
# version check work) with install.sh at its usual place.
TREE="$ROOT/tree"
TREE_SH="$TREE/skills/install-agent-director/install.sh"
mkdir -p "$TREE"
(cd "$REPO_ROOT" && tar -cf - --exclude=pkg/ts-bun-client go.mod go.sum Makefile cmd internal pkg) \
    | tar -xf - -C "$TREE" || die "copy module"
# The Makefile reads the package version when it is parsed.
mkdir -p "$TREE/pkg/ts-bun-client" && cp "$REPO_ROOT/pkg/ts-bun-client/package.json" "$TREE/pkg/ts-bun-client/"
install_copy "$TREE_SH"
git -C "$TREE" init -q && git -C "$TREE" add -A \
    && git -C "$TREE" -c user.name=advice -c user.email=advice@example.invalid -c commit.gpgsign=false \
        commit -qm tree || die "git init tree"

# ---- toolbox: the only PATH install.sh sees --------------------------------

TOOLBOX="$ROOT/toolbox"
mkdir -p "$TOOLBOX"
IFS=: read -r -a path_dirs <<<"$PATH"
for d in "${path_dirs[@]}"; do
    for f in "$d"/*; do
        n="${f##*/}"
        [[ -f "$f" && -x "$f" ]] || continue
        [[ -e "$TOOLBOX/$n" || -L "$TOOLBOX/$n" ]] || ln -s "$f" "$TOOLBOX/$n"
    done
done
rm -f "$TOOLBOX/agent-director" "$TOOLBOX/gh" "$TOOLBOX/claude" "$TOOLBOX/curl" "$TOOLBOX/sleep"

fake() { cat >"$TOOLBOX/$1"; chmod 0755 "$TOOLBOX/$1"; }
fake claude <<'EOF'
#!/bin/bash
echo "0.0.0 (fake claude, b.fji install-sh tests)"
EOF
fake sleep <<'EOF'
#!/bin/bash
exit 0
EOF
# curl: install.sh's download wrapper (-w ... -o) answers FAKE_CURL_STATUS with
# FAKE_CURL_BODY on 200; the latest-release API answers FAKE_CURL_API_TAG or
# 404s. Nothing reaches the network.
fake curl <<'EOF'
#!/bin/bash
out="" url="" has_w=0
while [[ $# -gt 0 ]]; do
    case "$1" in
        -o) out="$2"; shift 2 ;;
        -w) has_w=1; shift 2 ;;
        --retry) shift 2 ;;
        -*) shift ;;
        *) url="$1"; shift ;;
    esac
done
if [[ "$has_w" -eq 1 ]]; then
    status="${FAKE_CURL_STATUS:-200}"
    if [[ "$status" == 200 ]]; then cp "$FAKE_CURL_BODY" "$out"; printf 200; exit 0; fi
    : >"$out"; printf '%s' "$status"; exit 22
fi
case "$url" in
    https://api.github.com/*/releases/latest)
        if [[ -n "${FAKE_CURL_API_TAG:-}" ]]; then printf '{"tag_name":"%s"}\n' "$FAKE_CURL_API_TAG"; exit 0; fi
        echo "curl: (22) The requested URL returned error: 404" >&2; exit 22 ;;
esac
echo "fake curl: unexpected call: $url" >&2
exit 7
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
        bits=32; [[ "$(hex 4 1)" == 02 ]] && bits=64
        endian=LSB; [[ "$(hex 5 1)" == 02 ]] && endian=MSB
        case "$(hex 18 2)" in 3e00) m=x86-64 ;; b700) m="ARM aarch64" ;; *) m="machine 0x$(hex 18 2)" ;; esac
        echo "ELF ${bits}-bit ${endian} executable, ${m}, version 1 (SYSV), statically linked" ;;
    cffaedfe) echo "Mach-O 64-bit executable" ;;
    *) echo "data" ;;
esac
EOF
fi
GH_FAKE="$ROOT/gh-fake"
cat >"$GH_FAKE" <<'EOF'
#!/bin/bash
[[ "${1:-} ${2:-}" == "release download" ]] || { echo "fake gh: unsupported: $*" >&2; exit 1; }
out=""
while [[ $# -gt 0 ]]; do case "$1" in -O) out="$2"; shift 2 ;; *) shift ;; esac; done
cp "$FAKE_CURL_BODY" "$out"
EOF
chmod 0755 "$GH_FAKE"
# sqlite3 stand-in for J7: answers like sqlite3 under a lock on call number
# FAKE_SQLITE3_FAIL_CALL, otherwise runs the real one.
SQLITE_SHIM="$ROOT/sqlite3-shim"
cat >"$SQLITE_SHIM" <<EOF
#!/bin/bash
n=\$(( \$(cat "\$FAKE_SQLITE3_COUNT" 2>/dev/null || echo 0) + 1 ))
echo "\$n" >"\$FAKE_SQLITE3_COUNT"
if [[ "\$n" == "\${FAKE_SQLITE3_FAIL_CALL:-0}" ]]; then
    echo "Error: in prepare, database is locked (5)" >&2; exit 5
fi
exec "$SQLITE" "\$@"
EOF
chmod 0755 "$SQLITE_SHIM"

# ---- harness ---------------------------------------------------------------

pass=0 fail=0 skip=0
T_FAILED=0 T_SKIPPED=0 RUN_N=0 RC=0 OUT="" ERR=""
FAKE_CURL_STATUS=200 FAKE_CURL_API_TAG="" FAKE_SQLITE3_FAIL_CALL=0

bad() { echo "    FAIL: $*"; T_FAILED=1; }

# known_broken <id> <why>: skip the rest of this test unless the gate is set.
known_broken() {
    if [[ "${AGENT_DIRECTOR_RUN_KNOWN_BROKEN_ADVICE:-}" != 1 ]]; then
        echo "    SKIP: b.fji $1: advice does not work as written (product bug to file): $2"
        T_SKIPPED=1
        return 1
    fi
}

new_home() {
    local h
    h="$(mktemp -d "$ROOT/home.XXXXXX")"
    printf '%s' "$h"
}

# run_in <home> <cwd> <cmd...>: run cmd with only the install env; sets RC,
# OUT and ERR. Refuses any HOME outside the private root.
run_in() {
    local home="$1" cwd="$2"; shift 2
    [[ "$home" == "$ROOT"/home.* ]] || { echo "refusing HOME outside the test root: $home" >&2; exit 1; }
    RUN_N=$((RUN_N + 1))
    OUT="$ROOT/runs/$RUN_N.out" ERR="$ROOT/runs/$RUN_N.err"
    (cd "$cwd" && env -i HOME="$home" PATH="$TOOLBOX" TMPDIR="$ROOT/tmp" \
        INSTALL_SH_TEST_CURL_OVERRIDE="$TOOLBOX/curl" FAKE_CURL_BODY="$BIN" \
        FAKE_CURL_STATUS="$FAKE_CURL_STATUS" FAKE_CURL_API_TAG="$FAKE_CURL_API_TAG" \
        FAKE_SQLITE3_FAIL_CALL="$FAKE_SQLITE3_FAIL_CALL" FAKE_SQLITE3_COUNT="$home.sqlite3-calls" \
        "$@") >"$OUT" 2>"$ERR"
    RC=$?
}
run() { local home="$1"; shift; run_in "$home" "$ROOT" "$@"; }

# run_advised <home> <cwd> <command text>: run an advised command as written;
# "a && b" runs b only if a succeeds. make runs in the caller's own env (it
# needs the Go toolchain and caches); everything else in the install env.
run_advised() {
    local home="$1" cwd="$2" part rest="$3" words
    while [[ -n "$rest" ]]; do
        part="${rest%% && *}"
        [[ "$part" == "$rest" ]] && rest="" || rest="${rest#* && }"
        read -r -a words <<<"$part"
        if [[ "${words[0]}" == make ]]; then
            RUN_N=$((RUN_N + 1)); OUT="$ROOT/runs/$RUN_N.out" ERR="$ROOT/runs/$RUN_N.err"
            (cd "$cwd" && "${words[@]}") >"$OUT" 2>"$ERR"; RC=$?
        else
            run_in "$home" "$cwd" "${words[@]}"
        fi
        [[ "$RC" -eq 0 ]] || return 0
    done
}

flat() { tr '\n' ' ' <"$1" | tr -s ' '; }

expect_rc() {
    [[ "$RC" == "$1" ]] && return 0
    bad "exit $RC; want $1 ($2)"
    sed 's/^/      stderr: /' "$ERR" | tail -n 15
    return 1
}
expect_advice() {
    local text
    text="$(flat "$ERR")"
    [[ "$text" == *"$1"* ]] || bad "stderr lacks the advice \"$1\": $text"
}

# advice_after <marker>: the rest of the first stderr line holding marker
# (fails when there is none).
advice_after() {
    local line
    line="$(grep -m1 -F -- "$1" "$ERR")" || return 1
    printf '%s' "${line#*"$1"}"
}
# line_after <marker>: the stderr line after the first one holding marker, trimmed.
line_after() {
    local line
    line="$(grep -m1 -F -A1 -- "$1" "$ERR" | tail -n1)"
    line="${line#"${line%%[![:space:]]*}"}"
    printf '%s' "$line"
}

expect_installed() {
    local home="$1" src="$2" c="$1/.agent-director/bin/agent-director"
    [[ -x "$c" ]] || { bad "no installed binary at $c"; return; }
    cmp -s "$c" "$src" || bad "installed binary is not $src"
    [[ -f "$home/.agent-director/state.db" ]] || bad "no state.db after install"
}
db_version() { "$SQLITE" "$1/.agent-director/state.db" 'PRAGMA user_version;'; }
sentinel() { printf '%s' "$1/.agent-director/migrate-authorized"; }

run_test() {
    T_FAILED=0 T_SKIPPED=0 FAKE_CURL_STATUS=200 FAKE_CURL_API_TAG="" FAKE_SQLITE3_FAIL_CALL=0
    echo "=== RUN   $1"
    "$1"
    if [[ "$T_FAILED" -ne 0 ]]; then
        fail=$((fail + 1)); echo "--- FAIL: $1"
    elif [[ "$T_SKIPPED" -ne 0 ]]; then
        skip=$((skip + 1)); echo "--- SKIP: $1"
    else
        pass=$((pass + 1)); echo "--- PASS: $1"
    fi
}

# ---- J1: --from-release, no release published -------------------------------

# J1: "point at a local binary: bash $0 --binary <path>"
test_J1_NoReleasePointAtLocalBinary() {
    local h; h="$(new_home)"
    run "$h" bash "$LOOSE" --from-release --no-hooks --no-symlink
    expect_rc 3 "no release published" || return
    expect_advice "point at a local binary: bash $LOOSE --binary <path>"
    local cmd; cmd="$(advice_after "point at a local binary: ")" || { bad "no advised command"; return; }
    run_advised "$h" "$ROOT" "${cmd//<path>/$BIN}"
    expect_rc 0 "advised: $cmd" && expect_installed "$h" "$BIN"
}

# J1: "build from source: make build && bash $0"
test_J1_NoReleaseBuildFromSource() {
    local h; h="$(new_home)"
    run_in "$h" "$TREE" bash "$TREE_SH" --from-release --no-hooks --no-symlink
    expect_rc 3 "no release published" || return
    expect_advice "build from source: make build && bash $TREE_SH"
    local cmd; cmd="$(advice_after "build from source: ")" || { bad "no advised command"; return; }
    run_advised "$h" "$TREE" "$cmd"
    expect_rc 0 "advised: $cmd" && expect_installed "$h" "$TREE/bin/agent-director"
}

# ---- J2: --from-release download failed after retries ------------------------

# J2: "wait a few minutes and re-run this command"
test_J2_DownloadFailedWaitAndRerun() {
    local h argv=(bash "$LOOSE" --from-release v0.0.0-fake --no-hooks --no-symlink)
    h="$(new_home)"
    FAKE_CURL_STATUS=404
    run "$h" "${argv[@]}"
    expect_rc 3 "asset 404 on every attempt" || return
    expect_advice "wait a few minutes and re-run this command"
    FAKE_CURL_STATUS=200 # the CDN caught up
    run "$h" "${argv[@]}"
    expect_rc 0 "re-run after the asset appeared" && expect_installed "$h" "$BIN"
}

# J2: "install `gh` and re-run (gh's auth path propagates faster)"
test_J2_DownloadFailedInstallGhAndRerun() {
    local h argv=(bash "$LOOSE" --from-release v0.0.0-fake --no-hooks --no-symlink)
    h="$(new_home)"
    FAKE_CURL_STATUS=404
    run "$h" "${argv[@]}"
    expect_rc 3 "asset 404 on every attempt" || return
    expect_advice 'install `gh` and re-run'
    ln -s "$GH_FAKE" "$TOOLBOX/gh"
    run "$h" "${argv[@]}" # curl still gets 404
    rm -f "$TOOLBOX/gh"
    expect_rc 0 "re-run with gh on PATH" && expect_installed "$h" "$BIN"
    expect_advice 'trying `gh release download` fallback'
}

# J2: "run: bash $0 --binary <path-to-downloaded-binary>" (404/403) and
# "Suggested fallback: ... bash $0 --binary <path-to-downloaded-binary>" (other).
test_J2_DownloadFailedRunWithBinary() {
    local status h cmd
    for status in 404 500; do
        h="$(new_home)"
        FAKE_CURL_STATUS=$status
        run "$h" bash "$LOOSE" --from-release v0.0.0-fake --no-hooks --no-symlink
        expect_rc 3 "asset HTTP $status" || return
        if [[ "$status" == 404 ]]; then
            expect_advice "and run: bash $LOOSE --binary <path-to-downloaded-binary>"
            cmd="$(advice_after "and run: ")" || { bad "no advised command"; return; }
        else
            expect_advice "Suggested fallback: download the asset manually and re-run with bash $LOOSE --binary <path-to-downloaded-binary>"
            cmd="$(line_after "Suggested fallback: download the asset manually and re-run with")"
        fi
        run_advised "$h" "$ROOT" "${cmd//<path-to-downloaded-binary>/$BIN}"
        expect_rc 0 "HTTP $status advised: $cmd" && expect_installed "$h" "$BIN"
    done
}

# ---- J3: no source binary ------------------------------------------------------

# J3: "Pass --binary <path> to override."
test_J3_NoSourceBinaryPassBinary() {
    local h argv=(bash "$LOOSE" --no-hooks --no-symlink)
    h="$(new_home)"
    run "$h" "${argv[@]}"
    expect_rc 3 "no source binary" || return
    expect_advice "Pass --binary <path> to override."
    run "$h" "${argv[@]}" --binary "$BIN"
    expect_rc 0 "re-run with --binary" && expect_installed "$h" "$BIN"
}

# ---- J4: wrong-architecture --binary ---------------------------------------------

# J4: "Did you pass the wrong --binary?"
test_J4_ArchMismatchRightBinary() {
    local h; h="$(new_home)"
    run "$h" bash "$LOOSE" --binary "$WRONG_ARCH" --no-hooks --no-symlink
    expect_rc 2 "$WRONG_NAME binary on $HOST_ARCH" || return
    expect_advice "Did you pass the wrong --binary?"
    run "$h" bash "$LOOSE" --binary "$BIN" --no-hooks --no-symlink
    expect_rc 0 "re-run with the right --binary" && expect_installed "$h" "$BIN"
}

# ---- J5: source-tree version check -------------------------------------------------

# j5_stale: put a binary not built from the tree's HEAD at the tree's bin/ and
# run install.sh on it; leaves the J5 refusal in RC/ERR.
j5_stale() {
    mkdir -p "$TREE/bin" && cp "$BIN" "$TREE/bin/agent-director"
    run_in "$1" "$TREE" bash "$TREE_SH" --binary "$TREE/bin/agent-director" --no-hooks --no-symlink
    expect_rc 3 "stale binary" || return 1
    expect_advice "rebuild it first: make build"
    expect_advice "or download release: rerun with --from-release (omit --binary)"
}

# J5: "rebuild it first: make build"
test_J5_StaleBinaryMakeBuild() {
    local h; h="$(new_home)"
    j5_stale "$h" || return
    local cmd; cmd="$(advice_after "rebuild it first:")" || { bad "no advised command"; return; }
    run_advised "$h" "$TREE" "$cmd"
    expect_rc 0 "advised: $cmd" || return
    run_in "$h" "$TREE" bash "$TREE_SH" --binary "$TREE/bin/agent-director" --no-hooks --no-symlink
    expect_rc 0 "re-run after make build" && expect_installed "$h" "$TREE/bin/agent-director"
}

# J5: "or download release: rerun with --from-release (omit --binary)"
test_J5_StaleBinaryFromRelease() {
    local h; h="$(new_home)"
    j5_stale "$h" || return
    FAKE_CURL_API_TAG=v0.0.0-fake
    run_in "$h" "$TREE" bash "$TREE_SH" --no-hooks --no-symlink --from-release
    expect_rc 0 "rerun with --from-release, no --binary" && expect_installed "$h" "$BIN"
}

# ---- J6: store open failed after install ------------------------------------------

# j6_failing_migration: install BIN_OLD, then make the store one version older
# with a store_meta table the migration cannot write; leaves HOME in J6H.
j6_failing_migration() {
    J6H="$(new_home)"
    run "$J6H" bash "$LOOSE" --binary "$BIN_OLD" --no-hooks --no-symlink
    expect_rc 0 "first install" || return 1
    "$SQLITE" "$J6H/.agent-director/state.db" "PRAGMA user_version = $((SCHEMA - 1));
        DROP TABLE store_meta; CREATE TABLE store_meta (bogus TEXT);" || { bad "damage store"; return 1; }
    J6ARGV=(bash "$LOOSE" --binary "$BIN" --keep-prior --no-hooks --no-symlink)
    run "$J6H" "${J6ARGV[@]}"
    expect_rc 5 "migration step fails" || return 1
    expect_advice "If a migration was authorized above it was NOT consumed; re-running this install will retry it."
    [[ -f "$(sentinel "$J6H")" ]] || bad "the authorized migration's sentinel is gone after the failed open"
}

# J6: "If a migration was authorized above it was NOT consumed; re-running this
# install will retry it."
test_J6_MigrationFailedRerunRetries() {
    j6_failing_migration || return
    run "$J6H" "${J6ARGV[@]}" # the cause still holds: the same refusal
    expect_rc 5 "re-run while store_meta is still bad" || return
    expect_advice "re-running this install will retry it."
    [[ "$(db_version "$J6H")" == $((SCHEMA - 1)) ]] || bad "store moved off v$((SCHEMA - 1)) on a failed re-run"
    "$SQLITE" "$J6H/.agent-director/state.db" "DROP TABLE store_meta;" || { bad "repair store"; return; }
    run "$J6H" "${J6ARGV[@]}"
    expect_rc 0 "re-run once the cause is gone" || return
    [[ "$(db_version "$J6H")" == "$SCHEMA" ]] || bad "store at v$(db_version "$J6H"); want v$SCHEMA"
    [[ ! -e "$(sentinel "$J6H")" ]] || bad "sentinel not consumed by the successful migration"
}

# J6 with --keep-prior: re-running as advised must keep the rollback copy of
# the binary that was installed before this install.
test_J6_RerunKeepsPriorRollbackCopy() {
    j6_failing_migration || return
    cmp -s "$J6H/.agent-director/bin/agent-director.prior" "$BIN_OLD" || bad "first run did not snapshot the old binary"
    known_broken J6 "a --keep-prior re-run snapshots the failed new binary over agent-director.prior" || return
    run "$J6H" "${J6ARGV[@]}"
    cmp -s "$J6H/.agent-director/bin/agent-director.prior" "$BIN_OLD" \
        || bad "after the advised re-run agent-director.prior is no longer the pre-install binary (rollback copy lost)"
}

# J6: "If state.db is NEWER than this binary (ErrSchemaMismatch), install a
# newer agent-director instead."
test_J6_NewerStoreInstallNewer() {
    local h; h="$(new_home)"
    run "$h" bash "$LOOSE" --binary "$BIN" --no-hooks --no-symlink
    expect_rc 0 "first install" || return
    "$SQLITE" "$h/.agent-director/state.db" "PRAGMA user_version = $((SCHEMA + 1));"
    run "$h" bash "$LOOSE" --binary "$BIN" --no-hooks --no-symlink
    expect_rc 5 "store newer than the binary" || return
    expect_advice "ErrSchemaMismatch"
    expect_advice "If state.db is NEWER than this binary (ErrSchemaMismatch), install a newer agent-director instead."
    run "$h" bash "$LOOSE" --binary "$BIN_NEWER" --no-hooks --no-symlink
    expect_rc 0 "install a newer agent-director" && expect_installed "$h" "$BIN_NEWER"
    run "$h" "$h/.agent-director/bin/agent-director" list
    expect_rc 0 "list with the newer binary"
}

# ---- J7: migration verification failed -----------------------------------------

# j7_unverified: a valid one-version-older store, and an install whose
# post-open user_version read fails (sqlite3 meets a lock); leaves HOME in J7H.
j7_unverified() {
    J7H="$(new_home)"
    run "$J7H" bash "$LOOSE" --binary "$BIN" --no-hooks --no-symlink
    expect_rc 0 "first install" || return 1
    "$SQLITE" "$J7H/.agent-director/state.db" "PRAGMA user_version = $((SCHEMA - 1));"
    J7ARGV=(bash "$LOOSE" --binary "$BIN" --no-hooks --no-symlink)
    ln -sf "$SQLITE_SHIM" "$TOOLBOX/sqlite3"
    FAKE_SQLITE3_FAIL_CALL=2 # 1: step-2 read, 2: step-5 verification read
    run "$J7H" "${J7ARGV[@]}"
    ln -sf "$SQLITE" "$TOOLBOX/sqlite3"
    FAKE_SQLITE3_FAIL_CALL=0
    expect_rc 5 "verification read failed" || return 1
    expect_advice "schema migration verification FAILED"
    expect_advice "The migration sentinel (if written) has NOT been consumed; re-run this install to retry, or contact the maintainers."
}

# J7: "re-run this install to retry"
test_J7_VerificationFailedRerun() {
    j7_unverified || return
    run "$J7H" "${J7ARGV[@]}"
    expect_rc 0 "re-run once the read works" || return
    [[ "$(db_version "$J7H")" == "$SCHEMA" ]] || bad "store at v$(db_version "$J7H"); want v$SCHEMA"
}

# J7: "The migration sentinel (if written) has NOT been consumed"
test_J7_SentinelNotConsumed() {
    j7_unverified || return
    known_broken J7 "the open migrated the store and consumed the sentinel before the failed verification read" || return
    [[ -f "$(sentinel "$J7H")" ]] || bad "the sentinel was consumed, contrary to the text (store at v$(db_version "$J7H"))"
}

# ---- J8: required tool missing ---------------------------------------------------

# J8: "Install <tool> via your package manager ..." and friends: put the tool
# on PATH and re-run the same command.
test_J8_MissingToolProvideAndRerun() {
    local tool want h argv
    while IFS='|' read -r tool want; do
        h="$(new_home)"
        argv=(bash "$LOOSE" --binary "$BIN" --no-hooks --no-symlink)
        [[ "$tool" == curl ]] && argv=(bash "$LOOSE" --from-release v0.0.0-fake --no-hooks --no-symlink)
        mv "$TOOLBOX/$tool" "$ROOT/aside/$tool"
        run "$h" "${argv[@]}"
        mv "$ROOT/aside/$tool" "$TOOLBOX/$tool"
        expect_rc 2 "$tool missing" || continue
        expect_advice "required tool not found on PATH: $tool"
        expect_advice "$want"
        run "$h" "${argv[@]}"
        expect_rc 0 "re-run with $tool on PATH" || continue
        grep -qx "install.sh: pre-flight OK" "$OUT" || bad "$tool: no \"pre-flight OK\" after the re-run"
        expect_installed "$h" "$BIN"
    done <<'EOF'
claude|Install Claude Code first: https://claude.com/claude-code
tmux|Install tmux via your package manager (apt/brew/dnf/etc.).
jq|Install jq via your package manager (we use it to safely edit settings.json).
file|Install file via your package manager (apt install file / brew install file-formula / dnf install file). Required for the --binary architecture probe.
sqlite3|Install sqlite3 via your package manager (apt install sqlite3 / brew install sqlite / dnf install sqlite). Required to read state.db's schema version for the migration flow.
curl|--from-release downloads via curl; install it via your package manager.
EOF
}

echo "[b.fji install-sh advice-follow] start (schema v$SCHEMA)"
for t in $(declare -F | awk '{print $3}' | grep '^test_'); do
    run_test "$t"
done
echo "[b.fji install-sh advice-follow] summary: $pass passed, $fail failed, $skip skipped (known-broken)"
[[ "$fail" -eq 0 ]]
