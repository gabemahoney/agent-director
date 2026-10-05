#!/usr/bin/env bash
# advice_follow.sh — b.fji literal-follow tests for install.sh's own advice
# (advice inventory J1-J13). Each test triggers one install.sh refusal, checks
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
# The fake release --from-release installs: from 0.11.0 on, so it ships
# agent-director-admin (b.vqr).
REL_TAG=v0.11.0-fake
BIN="$ROOT/bin/agent-director"
BIN_OLD="$ROOT/bin/agent-director-old"
BIN_NEWER="$ROOT/bin/agent-director-newer"
WRONG_ARCH="$ROOT/bin/agent-director-$WRONG_NAME"
# The operator tool agent-director-admin (b.vqr), built from the same source
# with the same stamps (version and commit), as one build stamps both:
# ADMIN pairs with BIN (and BIN_NEWER, whose stamp is BIN's), ADMIN_OLD with
# BIN_OLD; install.sh installs a pair only when the stamps match and carry a
# commit. ADMIN_OTHER_COMMIT has BIN's version and another commit; NO_VERSION
# is a binary for this host with no version verb; BIN_PLAIN and ADMIN_PLAIN
# are plain `go build`s, stamped commit "unknown".
ADMIN="$ROOT/bin/agent-director-admin"
ADMIN_OLD="$ROOT/bin/agent-director-admin-old"
ADMIN_OTHER_COMMIT="$ROOT/bin/agent-director-admin-other-commit"
NO_VERSION="$ROOT/bin/no-version-verb"
BIN_PLAIN="$ROOT/bin/agent-director-plain"
ADMIN_PLAIN="$ROOT/bin/agent-director-admin-plain"
WRONG_ARCH_ADMIN="$ROOT/bin/agent-director-admin-$WRONG_NAME"
VERSION_PKG=github.com/gabemahoney/agent-director/internal/version
CUR_COMMIT=c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0
OLD_COMMIT=01d01d01d01d01d01d01d01d01d01d01d01d01d0
OTHER_COMMIT=0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e
STAMP="-X $VERSION_PKG.Version=0.0.2-advice -X $VERSION_PKG.Commit=$CUR_COMMIT"
STAMP_OLD="-X $VERSION_PKG.Version=0.0.1-advice-old -X $VERSION_PKG.Commit=$OLD_COMMIT"
STAMP_OTHER="-X $VERSION_PKG.Version=0.0.2-advice -X $VERSION_PKG.Commit=$OTHER_COMMIT"
# go_build <ldflags> <out> <package>: CGO_ENABLED=0 go build in the repo.
go_build() { (cd "$REPO_ROOT" && CGO_ENABLED=0 go build -ldflags "$1" -o "$2" "$3") || die "go build $2"; }
go_build "$STAMP" "$BIN" ./cmd/agent-director
go_build "$STAMP_OLD" "$BIN_OLD" ./cmd/agent-director
cmp -s "$BIN" "$BIN_OLD" && die "old and current binaries are identical"
go_build "$STAMP" "$ADMIN" ./cmd/agent-director-admin
go_build "$STAMP_OLD" "$ADMIN_OLD" ./cmd/agent-director-admin
go_build "$STAMP_OTHER" "$ADMIN_OTHER_COMMIT" ./cmd/agent-director-admin
go_build "" "$BIN_PLAIN" ./cmd/agent-director
go_build "" "$ADMIN_PLAIN" ./cmd/agent-director-admin
cp "$(type -P true)" "$NO_VERSION" || die "copy true(1)"
BIN_SHA="$(sha256sum "$BIN" | cut -d' ' -f1)"
ADMIN_SHA="$(sha256sum "$ADMIN" | cut -d' ' -f1)"
[[ "$BIN_SHA" =~ ^[0-9a-f]{64}$ && "$ADMIN_SHA" =~ ^[0-9a-f]{64}$ ]] || die "sha256sum the release assets"
# A wrong-arch binary as file(1) sees it: the ELF machine field says $WRONG_NAME.
cp "$BIN" "$WRONG_ARCH"
printf "$WRONG_MACHINE" | dd of="$WRONG_ARCH" bs=1 seek=18 conv=notrunc status=none || die "patch e_machine"
cp "$ADMIN" "$WRONG_ARCH_ADMIN"
printf "$WRONG_MACHINE" | dd of="$WRONG_ARCH_ADMIN" bs=1 seek=18 conv=notrunc status=none || die "patch e_machine (admin)"
# BIN_NUL and ADMIN_NUL: BIN and ADMIN with one NUL byte appended (different
# bytes, the same stamps), so either can replace its half of the pair alone (J11).
BIN_NUL="$ROOT/bin/agent-director-nul"
ADMIN_NUL="$ROOT/bin/agent-director-admin-nul"
{ cp "$BIN" "$BIN_NUL" && printf '\0' >>"$BIN_NUL" && cp "$ADMIN" "$ADMIN_NUL" && printf '\0' >>"$ADMIN_NUL"; } \
    || die "NUL-appended binaries"

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
(cd "$REPO_ROOT" && CGO_ENABLED=0 go build -overlay "$ROOT/overlay.json" -ldflags "$STAMP" -o "$BIN_NEWER" ./cmd/agent-director) \
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
# FAKE_CURL_BODY on 200, and for the agent-director-admin asset
# FAKE_CURL_ADMIN_STATUS (FAKE_CURL_STATUS when empty) with
# FAKE_CURL_ADMIN_BODY; the latest-release API answers FAKE_CURL_API_TAG or
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
    status="${FAKE_CURL_STATUS:-200}" body="$FAKE_CURL_BODY"
    if [[ "${url##*/}" == agent-director-admin-* ]]; then
        status="${FAKE_CURL_ADMIN_STATUS:-$status}" body="$FAKE_CURL_ADMIN_BODY"
    fi
    if [[ "$status" == 200 ]]; then cp "$body" "$out"; printf 200; exit 0; fi
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
out="" pattern=""
while [[ $# -gt 0 ]]; do case "$1" in -O) out="$2"; shift 2 ;; -p) pattern="$2"; shift 2 ;; *) shift ;; esac; done
body="$FAKE_CURL_BODY"
[[ "$pattern" == agent-director-admin-* ]] && body="$FAKE_CURL_ADMIN_BODY"
cp "$body" "$out"
EOF
chmod 0755 "$GH_FAKE"
# Directories a test puts before the toolbox on PATH (PATH_EXTRA): one with
# the fake gh, one with an agent-director (the old one) and no
# agent-director-admin, which is never on PATH (b.vqr).
GH_DIR="$ROOT/gh-on-path" ON_PATH="$ROOT/ad-on-path"
mkdir -p "$GH_DIR" "$ON_PATH"
ln -s "$GH_FAKE" "$GH_DIR/gh" && ln -s "$BIN_OLD" "$ON_PATH/agent-director" || die "PATH_EXTRA dirs"
# sqlite3 stand-in for J7: on call number FAKE_SQLITE3_FAIL_CALL it prints
# FAKE_SQLITE3_ANSWER (through printf %b, so \n breaks a line) when that is set
# (a wrong user_version, or output that is no version), and otherwise fails
# like sqlite3 whose busy timeout ran out under a lock, with SHIM_LOCK_ERR on
# stderr; every other call runs the real one.
SHIM_LOCK_ERR="Error: in prepare, database is locked (5)"
SQLITE_SHIM="$ROOT/sqlite3-shim"
cat >"$SQLITE_SHIM" <<EOF
#!/bin/bash
n=\$(( \$(cat "\$FAKE_SQLITE3_COUNT" 2>/dev/null || echo 0) + 1 ))
echo "\$n" >"\$FAKE_SQLITE3_COUNT"
if [[ "\$n" == "\${FAKE_SQLITE3_FAIL_CALL:-0}" ]]; then
    [[ -n "\${FAKE_SQLITE3_ANSWER:-}" ]] && { printf '%b\n' "\$FAKE_SQLITE3_ANSWER"; exit 0; }
    echo "$SHIM_LOCK_ERR" >&2; exit 5
fi
exec "$SQLITE" "\$@"
EOF
chmod 0755 "$SQLITE_SHIM"
# mktemp stand-in for J7 (a test puts MKTEMP_FAILS on PATH_EXTRA): for the
# template of install.sh's sqlite3 error file it fails as mktemp does in a full
# TMPDIR, logging the template to MKTEMP_REFUSALS; every other call runs the
# real one (b.wfe).
MKTEMP_FAILS="$ROOT/mktemp-fails" MKTEMP_REFUSALS="$ROOT/mktemp-refusals"
mkdir -p "$MKTEMP_FAILS" || die "mkdir $MKTEMP_FAILS"
cat >"$MKTEMP_FAILS/mktemp" <<EOF
#!/bin/bash
if [[ "\${!#}" == agent-director-sqlite3.* ]]; then
    echo "\${!#}" >>"$MKTEMP_REFUSALS"
    echo "mktemp: failed to create file via template '\${!#}': No space left on device" >&2
    exit 1
fi
exec "$(type -P mktemp)" "\$@"
EOF
chmod 0755 "$MKTEMP_FAILS/mktemp"

# ---- harness ---------------------------------------------------------------

pass=0 fail=0 skip=0
T_FAILED=0 T_SKIPPED=0 RUN_N=0 RC=0 OUT="" ERR=""
FAKE_CURL_STATUS=200 FAKE_CURL_ADMIN_STATUS="" FAKE_CURL_API_TAG="" FAKE_SQLITE3_FAIL_CALL=0 FAKE_SQLITE3_ANSWER=""
PATH_EXTRA=""

bad() { echo "    FAIL: $*"; T_FAILED=1; }

# known_broken <id> <why>: skip the rest of this test unless the gate is set.
known_broken() {
    if [[ "${AGENT_DIRECTOR_RUN_KNOWN_BROKEN_ADVICE:-}" != 1 ]]; then
        echo "    SKIP: b.fji $1: advice does not work as written (product bug to file): $2"
        T_SKIPPED=1
        return 1
    fi
}

# new_home: a new HOME under the private root; its name holds HOME_TAG when set.
new_home() {
    local h
    h="$(mktemp -d "$ROOT/home.${HOME_TAG:-}XXXXXX")"
    printf '%s' "$h"
}

# run_in <home> <cwd> <cmd...>: run cmd with only the install env (PATH the
# toolbox, after PATH_EXTRA when set); sets RC, OUT and ERR. Refuses any HOME
# outside the private root.
run_in() {
    local home="$1" cwd="$2"; shift 2
    [[ "$home" == "$ROOT"/home.* ]] || { echo "refusing HOME outside the test root: $home" >&2; exit 1; }
    RUN_N=$((RUN_N + 1))
    OUT="$ROOT/runs/$RUN_N.out" ERR="$ROOT/runs/$RUN_N.err"
    (cd "$cwd" && env -i HOME="$home" PATH="${PATH_EXTRA:+$PATH_EXTRA:}$TOOLBOX" TMPDIR="$ROOT/tmp" \
        INSTALL_SH_TEST_CURL_OVERRIDE="$TOOLBOX/curl" FAKE_CURL_BODY="$BIN" FAKE_CURL_ADMIN_BODY="$ADMIN" \
        FAKE_CURL_STATUS="$FAKE_CURL_STATUS" FAKE_CURL_ADMIN_STATUS="$FAKE_CURL_ADMIN_STATUS" \
        FAKE_CURL_API_TAG="$FAKE_CURL_API_TAG" \
        FAKE_SQLITE3_FAIL_CALL="$FAKE_SQLITE3_FAIL_CALL" FAKE_SQLITE3_ANSWER="$FAKE_SQLITE3_ANSWER" \
        FAKE_SQLITE3_COUNT="$home.sqlite3-calls" \
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

# expect_installed <home> <src> <admin-src>: src installed as agent-director,
# admin-src as agent-director-admin in its own 0700 directory (b.vqr), and a
# state.db.
expect_installed() {
    local home="$1" src="$2" admin_src="$3" c="$1/.agent-director/bin/agent-director"
    local a="$1/.agent-director/admin/agent-director-admin"
    [[ -x "$c" ]] || { bad "no installed binary at $c"; return; }
    cmp -s "$c" "$src" || bad "installed binary is not $src"
    [[ -x "$a" ]] || { bad "no installed agent-director-admin at $a"; return; }
    cmp -s "$a" "$admin_src" || bad "installed agent-director-admin is not $admin_src"
    [[ "$(stat -c %a "${a%/*}")" == 700 ]] || bad "${a%/*} has mode $(stat -c %a "${a%/*}"); want 700"
    [[ "$(stat -c %a "$a")" == 755 ]] || bad "$a has mode $(stat -c %a "$a"); want 755"
    [[ -f "$home/.agent-director/state.db" ]] || bad "no state.db after install"
}

# expect_nothing_installed <home>: neither binary was installed.
expect_nothing_installed() {
    [[ ! -e "$1/.agent-director/bin/agent-director" ]] || bad "agent-director installed after the refusal"
    [[ ! -e "$1/.agent-director/admin/agent-director-admin" ]] || bad "agent-director-admin installed after the refusal"
}
db_version() { "$SQLITE" "$1/.agent-director/state.db" 'PRAGMA user_version;'; }
sentinel() { printf '%s' "$1/.agent-director/migrate-authorized"; }

run_test() {
    T_FAILED=0 T_SKIPPED=0 FAKE_CURL_STATUS=200 FAKE_CURL_ADMIN_STATUS="" FAKE_CURL_API_TAG="" FAKE_SQLITE3_FAIL_CALL=0
    FAKE_SQLITE3_ANSWER="" PATH_EXTRA=""
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

# J1: "point at local binaries: bash $0 --binary <path> --admin-binary <path>"
test_J1_NoReleasePointAtLocalBinaries() {
    local h; h="$(new_home)"
    run "$h" bash "$LOOSE" --from-release --no-hooks --no-symlink
    expect_rc 3 "no release published" || return
    expect_advice "point at local binaries: bash $LOOSE --binary <path> --admin-binary <path>"
    local cmd; cmd="$(advice_after "point at local binaries: ")" || { bad "no advised command"; return; }
    cmd="${cmd/--binary <path>/--binary $BIN}"
    run_advised "$h" "$ROOT" "${cmd/--admin-binary <path>/--admin-binary $ADMIN}"
    expect_rc 0 "advised: $cmd" && expect_installed "$h" "$BIN" "$ADMIN"
}

# J1: "build from source: make build && bash $0"
test_J1_NoReleaseBuildFromSource() {
    local h; h="$(new_home)"
    run_in "$h" "$TREE" bash "$TREE_SH" --from-release --no-hooks --no-symlink
    expect_rc 3 "no release published" || return
    expect_advice "build from source: make build && bash $TREE_SH"
    local cmd; cmd="$(advice_after "build from source: ")" || { bad "no advised command"; return; }
    run_advised "$h" "$TREE" "$cmd"
    expect_rc 0 "advised: $cmd" && expect_installed "$h" "$TREE/bin/agent-director" "$TREE/bin/agent-director-admin"
}

# ---- J2: --from-release download failed after retries ------------------------

# J2: "wait a few minutes and re-run this command"
test_J2_DownloadFailedWaitAndRerun() {
    local h argv=(bash "$LOOSE" --from-release "$REL_TAG" --no-hooks --no-symlink)
    h="$(new_home)"
    FAKE_CURL_STATUS=404
    run "$h" "${argv[@]}"
    expect_rc 3 "asset 404 on every attempt" || return
    expect_advice "wait a few minutes and re-run this command"
    FAKE_CURL_STATUS=200 # the CDN caught up
    run "$h" "${argv[@]}"
    expect_rc 0 "re-run after the asset appeared" && expect_installed "$h" "$BIN" "$ADMIN"
}

# J2: "wait a few minutes and re-run this command" when only the
# agent-director-admin asset of a release from 0.11.0 on is not there yet:
# nothing is installed, and the re-run once it appears installs both (b.vqr).
test_J2_AdminAssetMissingWaitAndRerun() {
    local h argv=(bash "$LOOSE" --from-release "$REL_TAG" --no-hooks --no-symlink)
    h="$(new_home)"
    FAKE_CURL_ADMIN_STATUS=404
    run "$h" "${argv[@]}"
    expect_rc 3 "admin asset 404 on every attempt" || return
    expect_advice "asset : agent-director-admin-"
    expect_advice "wait a few minutes and re-run this command"
    expect_nothing_installed "$h"
    FAKE_CURL_ADMIN_STATUS="" # the CDN caught up
    run "$h" "${argv[@]}"
    expect_rc 0 "re-run after the admin asset appeared" && expect_installed "$h" "$BIN" "$ADMIN"
}

# J2: "install `gh` and re-run (gh's auth path propagates faster)"
test_J2_DownloadFailedInstallGhAndRerun() {
    local h argv=(bash "$LOOSE" --from-release "$REL_TAG" --no-hooks --no-symlink)
    h="$(new_home)"
    FAKE_CURL_STATUS=404
    run "$h" "${argv[@]}"
    expect_rc 3 "asset 404 on every attempt" || return
    expect_advice 'install `gh` and re-run'
    ln -s "$GH_FAKE" "$TOOLBOX/gh"
    run "$h" "${argv[@]}" # curl still gets 404
    rm -f "$TOOLBOX/gh"
    expect_rc 0 "re-run with gh on PATH" && expect_installed "$h" "$BIN" "$ADMIN"
    expect_advice 'trying `gh release download` fallback'
}

# J2: "run: bash $0 --binary <path-to-downloaded-agent-director> --admin-binary
# <path-to-downloaded-agent-director-admin>" (404/403) and "Suggested fallback:
# ... bash $0 --binary <...> --admin-binary <...>" (other).
test_J2_DownloadFailedRunWithBinaries() {
    local status h cmd
    local advised="bash $LOOSE --binary <path-to-downloaded-agent-director> --admin-binary <path-to-downloaded-agent-director-admin>"
    for status in 404 500; do
        h="$(new_home)"
        FAKE_CURL_STATUS=$status
        run "$h" bash "$LOOSE" --from-release "$REL_TAG" --no-hooks --no-symlink
        expect_rc 3 "asset HTTP $status" || return
        if [[ "$status" == 404 ]]; then
            expect_advice "download the binaries manually from"
            expect_advice "and run: $advised"
            cmd="$(advice_after "and run: ")" || { bad "no advised command"; return; }
        else
            expect_advice "Suggested fallback: download the assets manually and re-run with $advised"
            cmd="$(line_after "Suggested fallback: download the assets manually and re-run with")"
        fi
        cmd="${cmd//<path-to-downloaded-agent-director-admin>/$ADMIN}"
        run_advised "$h" "$ROOT" "${cmd//<path-to-downloaded-agent-director>/$BIN}"
        expect_rc 0 "HTTP $status advised: $cmd" && expect_installed "$h" "$BIN" "$ADMIN"
    done
}

# ---- J3: no source binary ------------------------------------------------------

# J3: "Pass --binary <path> to override." when only agent-director is missing
# (--admin-binary given; none beside the script or on PATH).
test_J3_NoSourceBinaryPassBinary() {
    local h argv=(bash "$LOOSE" --admin-binary "$ADMIN" --no-hooks --no-symlink)
    h="$(new_home)"
    run "$h" "${argv[@]}"
    expect_rc 3 "no agent-director source binary" || return
    expect_advice "install.sh: no source binary found."
    expect_advice "Pass --binary <path> to override."
    expect_nothing_installed "$h"
    run "$h" "${argv[@]}" --binary "$BIN"
    expect_rc 0 "re-run with --binary" && expect_installed "$h" "$BIN" "$ADMIN"
}

# j3_pass_both <home> <cmd...>: check the one refusal for both missing
# binaries advises "Pass --binary <path> --admin-binary <path> (both from the
# same build) to override.", re-run cmd with those flags, the paths one build's,
# and check both are installed (b.vqr).
j3_pass_both() {
    local h="$1" flags extra; shift
    expect_advice "Pass --binary <path> --admin-binary <path> (both from the same build) to override."
    expect_nothing_installed "$h"
    flags="$(advice_after "Pass ")" || { bad "no advised flags"; return; }
    flags="${flags% (both from the same build) to override.}"
    flags="${flags/--binary <path>/--binary $BIN}"
    read -r -a extra <<<"${flags/--admin-binary <path>/--admin-binary $ADMIN}"
    run "$h" "$@" "${extra[@]}"
    expect_rc 0 "re-run with ${extra[*]}" && expect_installed "$h" "$BIN" "$ADMIN"
}

# J3: neither binary beside the script and none on PATH: one refusal names
# both (b.vqr).
test_J3_NoSourceBinariesPassBoth() {
    local h argv=(bash "$LOOSE" --no-hooks --no-symlink)
    h="$(new_home)"
    run "$h" "${argv[@]}"
    expect_rc 3 "no source binaries" || return
    expect_advice "install.sh: no source binaries found: neither agent-director nor agent-director-admin is beside the script."
    expect_advice "Tried: command -v agent-director"
    j3_pass_both "$h" "${argv[@]}"
}

# J3: the same refusal when agent-director is found only on PATH, which is
# not used: agent-director-admin is never on PATH to pair with it (b.vqr).
test_J3_PathOnlyPassBoth() {
    local h argv=(bash "$LOOSE" --no-hooks --no-symlink)
    h="$(new_home)"
    PATH_EXTRA="$ON_PATH"
    run "$h" "${argv[@]}"
    expect_rc 3 "agent-director on PATH only" || return
    expect_advice "install.sh: no source binaries found: neither agent-director nor agent-director-admin is beside the script."
    expect_advice "Found on PATH, not used: $ON_PATH/agent-director (agent-director-admin is never on PATH to pair with it)"
    j3_pass_both "$h" "${argv[@]}"
}

# J3: "Pass --admin-binary <path> to override." (b.vqr)
test_J3_NoAdminBinaryPassAdminBinary() {
    local h argv=(bash "$LOOSE" --binary "$BIN" --no-hooks --no-symlink)
    h="$(new_home)"
    run "$h" "${argv[@]}"
    expect_rc 3 "no agent-director-admin source binary" || return
    expect_advice "install.sh: no agent-director-admin source binary found."
    expect_advice "Pass --admin-binary <path> to override."
    expect_nothing_installed "$h"
    run "$h" "${argv[@]}" --admin-binary "$ADMIN"
    expect_rc 0 "re-run with --admin-binary" && expect_installed "$h" "$BIN" "$ADMIN"
}

# ---- J4: wrong-architecture --binary ---------------------------------------------

# J4: "Did you pass the wrong --binary?"
test_J4_ArchMismatchRightBinary() {
    local h; h="$(new_home)"
    run "$h" bash "$LOOSE" --binary "$WRONG_ARCH" --admin-binary "$ADMIN" --no-hooks --no-symlink
    expect_rc 2 "$WRONG_NAME binary on $HOST_ARCH" || return
    expect_advice "Did you pass the wrong --binary?"
    run "$h" bash "$LOOSE" --binary "$BIN" --admin-binary "$ADMIN" --no-hooks --no-symlink
    expect_rc 0 "re-run with the right --binary" && expect_installed "$h" "$BIN" "$ADMIN"
}

# J4: "Did you pass the wrong --admin-binary?" (b.vqr)
test_J4_ArchMismatchRightAdminBinary() {
    local h; h="$(new_home)"
    run "$h" bash "$LOOSE" --binary "$BIN" --admin-binary "$WRONG_ARCH_ADMIN" --no-hooks --no-symlink
    expect_rc 2 "$WRONG_NAME agent-director-admin on $HOST_ARCH" || return
    expect_advice "install.sh: --admin-binary $WRONG_ARCH_ADMIN: architecture mismatch"
    expect_advice "Did you pass the wrong --admin-binary?"
    expect_nothing_installed "$h"
    run "$h" bash "$LOOSE" --binary "$BIN" --admin-binary "$ADMIN" --no-hooks --no-symlink
    expect_rc 0 "re-run with the right --admin-binary" && expect_installed "$h" "$BIN" "$ADMIN"
}

# ---- J5: source-tree version check -------------------------------------------------

# j5_stale: put a binary not built from the tree's HEAD at the tree's bin/ and
# run install.sh on it; leaves the J5 refusal in RC/ERR.
j5_stale() {
    mkdir -p "$TREE/bin" && cp "$BIN" "$TREE/bin/agent-director" && cp "$ADMIN" "$TREE/bin/agent-director-admin"
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
    expect_rc 0 "re-run after make build" && expect_installed "$h" "$TREE/bin/agent-director" "$TREE/bin/agent-director-admin"
}

# J5: "or download release: rerun with --from-release (omit --binary)"
test_J5_StaleBinaryFromRelease() {
    local h; h="$(new_home)"
    j5_stale "$h" || return
    FAKE_CURL_API_TAG="$REL_TAG"
    run_in "$h" "$TREE" bash "$TREE_SH" --no-hooks --no-symlink --from-release
    expect_rc 0 "rerun with --from-release, no --binary" && expect_installed "$h" "$BIN" "$ADMIN"
}

# ---- J6: store open failed after install ------------------------------------------

# j6_failing_migration [pre-0.11.0]: install BIN_OLD and ADMIN_OLD (with
# pre-0.11.0, then make that an install from before 0.11.0: j11_pre_admin),
# make the store one version older with a store_meta table the migration cannot
# write, and run J6ARGV, an upgrade with --keep-prior, into that failure;
# leaves HOME in J6H.
j6_failing_migration() {
    J6H="$(new_home)"
    run "$J6H" bash "$LOOSE" --binary "$BIN_OLD" --admin-binary "$ADMIN_OLD" --no-hooks --no-symlink
    expect_rc 0 "first install" || return 1
    if [[ "${1:-}" == pre-0.11.0 ]]; then j11_pre_admin "$J6H" || return 1; fi
    "$SQLITE" "$J6H/.agent-director/state.db" "PRAGMA user_version = $((SCHEMA - 1));
        DROP TABLE store_meta; CREATE TABLE store_meta (bogus TEXT);" || { bad "damage store"; return 1; }
    J6ARGV=(bash "$LOOSE" --binary "$BIN" --admin-binary "$ADMIN" --keep-prior --no-hooks --no-symlink)
    run "$J6H" "${J6ARGV[@]}"
    expect_rc 5 "migration step fails" || return 1
    expect_advice "If a migration was authorized above it was NOT consumed; re-running this install will retry it."
    [[ -f "$(sentinel "$J6H")" ]] || bad "the authorized migration's sentinel is gone after the failed open"
}

# J6: "If a migration was authorized above it was NOT consumed; re-running this
# install will retry it." The re-run's step-3 probe fails too, and says it could
# not tell, not that no migration is needed (b.7b4).
test_J6_MigrationFailedRerunRetries() {
    j6_failing_migration || return
    run "$J6H" "${J6ARGV[@]}" # the cause still holds: the same refusal
    expect_rc 5 "re-run while store_meta is still bad" || return
    expect_advice "re-running this install will retry it."
    local could="  schema  : state.db at v$((SCHEMA - 1)); could not tell whether a migration is needed (agent-director list failed: ErrStoreOpen)"
    grep -qxF "$could" "$OUT" || bad "no \"$could\" line: $(grep -F "  schema  : " "$OUT")"
    [[ "$(db_version "$J6H")" == $((SCHEMA - 1)) ]] || bad "store moved off v$((SCHEMA - 1)) on a failed re-run"
    "$SQLITE" "$J6H/.agent-director/state.db" "DROP TABLE store_meta;" || { bad "repair store"; return; }
    run "$J6H" "${J6ARGV[@]}"
    expect_rc 0 "re-run once the cause is gone" || return
    [[ "$(db_version "$J6H")" == "$SCHEMA" ]] || bad "store at v$(db_version "$J6H"); want v$SCHEMA"
    [[ ! -e "$(sentinel "$J6H")" ]] || bad "sentinel not consumed by the successful migration"
}

# J6 with --keep-prior: re-running as advised keeps the rollback copies of the
# pair installed before this install and says so (b.2wk); rolling both back
# then restores that pair, stamps matching.
test_J6_RerunKeepsPriorRollbackCopy() {
    j6_failing_migration || return
    j11_paths "$J6H"
    cmp -s "$J11C.prior" "$BIN_OLD" || bad "first run did not snapshot the old agent-director"
    cmp -s "$J11A.prior" "$ADMIN_OLD" || bad "first run did not snapshot the old agent-director-admin"
    run "$J6H" "${J6ARGV[@]}"
    expect_rc 5 "advised re-run while store_meta is still bad" || return
    j11_not_snapshotted "prior   " "kept $J11C.prior" "$J11_SAME_PAIR"
    j11_not_snapshotted "admin prior" "kept $J11A.prior" "$J11_SAME_PAIR"
    cmp -s "$J11C.prior" "$BIN_OLD" \
        || bad "after the advised re-run agent-director.prior is no longer the pre-install binary (rollback copy lost)"
    cmp -s "$J11A.prior" "$ADMIN_OLD" \
        || bad "after the advised re-run agent-director-admin.prior is no longer the pre-install binary (rollback copy lost)"
    j11_roll_back_both "$J6H" 0.0.1-advice-old
}

# J6 with --keep-prior on an upgrade from before 0.11.0
# (test_J11_KeepPriorNoAdminRemoveIt's setup): the advised re-run keeps
# agent-director.prior and still says "to roll back, remove
# <agent-director-admin>" (b.2wk); doing that restores the old install.
test_J6_RerunPreAdminUpgradeKeepsRemoveAdvice() {
    local got
    j6_failing_migration pre-0.11.0 || return
    j11_paths "$J6H"
    grep -qxF "  prior   : snapshotted to $J11C.prior" "$OUT" || bad "first run: no agent-director prior line: $(flat "$OUT")"
    grep -qxF "  admin prior: none (no agent-director-admin was installed); to roll back, remove $J11A" "$OUT" \
        || bad "first run: no remove-it admin prior line: $(flat "$OUT")"
    run "$J6H" "${J6ARGV[@]}"
    expect_rc 5 "advised re-run while store_meta is still bad" || return
    j11_not_snapshotted "prior   " "kept $J11C.prior" "$J11_SAME_PAIR"
    j11_not_snapshotted "admin prior" none "$J11_SAME_PAIR" "; to roll back, remove $J11A"
    cmp -s "$J11C.prior" "$BIN_OLD" \
        || bad "after the advised re-run agent-director.prior is no longer the pre-install binary (rollback copy lost)"
    [[ ! -e "$J11A.prior" ]] || bad "the advised re-run left $J11A.prior, which would pair wrongly"
    got="$(grep -m1 -F "to roll back, remove " "$OUT")" || return
    mv "$J11C.prior" "$J11C" || bad "mv $J11C.prior $J11C"
    rm "${got##*to roll back, remove }" || bad "remove ${got##*to roll back, remove }"
    cmp -s "$J11C" "$BIN_OLD" || bad "rolled-back agent-director is not the old one"
    [[ ! -e "$J11A" ]] || bad "agent-director-admin left after the rollback"
}

# J6: "If state.db is NEWER than this binary (ErrSchemaMismatch), install a
# newer agent-director instead."
test_J6_NewerStoreInstallNewer() {
    local h; h="$(new_home)"
    run "$h" bash "$LOOSE" --binary "$BIN" --admin-binary "$ADMIN" --no-hooks --no-symlink
    expect_rc 0 "first install" || return
    "$SQLITE" "$h/.agent-director/state.db" "PRAGMA user_version = $((SCHEMA + 1));"
    run "$h" bash "$LOOSE" --binary "$BIN" --admin-binary "$ADMIN" --no-hooks --no-symlink
    expect_rc 5 "store newer than the binary" || return
    local none="  schema  : state.db at v$((SCHEMA + 1)); no migration authorization needed"
    grep -qxF "$none" "$OUT" || bad "no \"$none\" line: $(grep -F "  schema  : " "$OUT")"
    expect_advice "ErrSchemaMismatch"
    expect_advice "If state.db is NEWER than this binary (ErrSchemaMismatch), install a newer agent-director instead."
    run "$h" bash "$LOOSE" --binary "$BIN_NEWER" --admin-binary "$ADMIN" --no-hooks --no-symlink
    expect_rc 0 "install a newer agent-director" && expect_installed "$h" "$BIN_NEWER" "$ADMIN"
    run "$h" "$h/.agent-director/bin/agent-director" list
    expect_rc 0 "list with the newer binary"
}

# ---- J7: migration verification failed, or state.db's version unreadable ------

J7ARGV=(bash "$LOOSE" --binary "$BIN" --admin-binary "$ADMIN" --no-hooks --no-symlink)

# j7_installed: a new HOME in J7H with J7ARGV installed.
j7_installed() {
    J7H="$(new_home)"
    run "$J7H" "${J7ARGV[@]}"
    expect_rc 0 "first install"
}

# j7_older_store: j7_installed, then the store set one version back, so the
# next install migrates it.
j7_older_store() {
    j7_installed || return 1
    "$SQLITE" "$J7H/.agent-director/state.db" "PRAGMA user_version = $((SCHEMA - 1));"
}

# j7_run <call> [<answer>]: run J7ARGV in J7H with sqlite3 call <call> (1 is
# step 2's read of an existing store, the next step 5's; 0 none) answering
# <answer>, or failing as when a lock outlasts its busy timeout; no sqlite3
# error file may be left in TMPDIR.
j7_run() {
    ln -sf "$SQLITE_SHIM" "$TOOLBOX/sqlite3"
    FAKE_SQLITE3_FAIL_CALL="$1" FAKE_SQLITE3_ANSWER="${2:-}"
    rm -f "$J7H.sqlite3-calls" "$ROOT"/tmp/agent-director-sqlite3.* # count from 1; a leftover below is this run's
    run "$J7H" "${J7ARGV[@]}"
    ln -sf "$SQLITE" "$TOOLBOX/sqlite3"
    FAKE_SQLITE3_FAIL_CALL=0 FAKE_SQLITE3_ANSWER=""
    if compgen -G "$ROOT/tmp/agent-director-sqlite3.*" >/dev/null; then
        bad "the sqlite3 error file left behind: $(compgen -G "$ROOT/tmp/agent-director-sqlite3.*")"
    fi
}

# j7_verify_fails <answer>: an older store, and an install whose verification
# read answers <answer> (empty: the read fails): exit 5, step 5's failure.
j7_verify_fails() {
    j7_older_store || return 1
    j7_run 2 "$1"
    expect_rc 5 "verification read answered \"$1\"" || return 1
    expect_advice "schema migration verification FAILED"
}

# j7_unreadable <advice> [<next>]: <next> (default: sqlite3's own error,
# indented) the line after the <unreadable> line, <advice> word for word, and no
# pointer to a human (time may resolve a lock, b.ady).
j7_unreadable() {
    local reason want="${2:-    $SHIM_LOCK_ERR}"
    reason="$(grep -xF -A1 "  actual   user_version: <unreadable>" "$ERR" | tail -n +2)"
    [[ "$reason" == "$want" ]] || bad "the line after \"<unreadable>\" is \"$reason\"; want \"$want\""
    expect_advice "$1"
    if grep -qF "contact the maintainers" "$ERR"; then
        bad "the unreadable-version failure tells the operator to contact the maintainers"
    fi
}

# j7_schema_unreadable: stdout's state.db line shows <unreadable>, never a bare
# "(schema v)" (b.n5a).
j7_schema_unreadable() {
    grep -qF "at $J7H/.agent-director/state.db (schema <unreadable>)" "$OUT" \
        || bad "no \"(schema <unreadable>)\" state.db line: $(flat "$OUT")"
}

# j7_unverified: j7_verify_fails with the verification read failing; checks
# its advice.
j7_unverified() {
    j7_verify_fails "" || return 1
    j7_schema_unreadable
    j7_unreadable "Reading state.db's user_version (sqlite3 PRAGMA user_version) failed, so the install could not check the migration. Re-running this install retries the read."
}

# j7_mismatch: j7_verify_fails with the verification read answering the
# pre-migration version; checks its advice (state.db's path shell-quoted).
j7_mismatch() {
    local t="$SCHEMA" a="$((SCHEMA - 1))" db
    j7_verify_fails "$a" || return 1
    db="$(printf %q "$J7H/.agent-director/state.db")"
    expect_advice "actual user_version: $a"
    expect_advice "The store open (agent-director list) succeeded, and a successful open leaves state.db at v$t: any migration this install authorized has run, and its sentinel is consumed. Yet the read after the open gives v$a: state.db changed after the open, or the read is wrong. Check its version now: sqlite3 -batch -init /dev/null -cmd \".timeout 10000\" $db \"PRAGMA user_version;\" A re-run of this install reads the version again: below v$t it brings state.db to v$t again, above v$t it stops at the store open (ErrSchemaMismatch), and at v$t it finishes the install. If a re-run fails this same way, contact the maintainers."
}

# j7_rerun_verified: re-run J7ARGV with the real sqlite3; it reads and verifies
# the migrated store.
j7_rerun_verified() {
    run "$J7H" "${J7ARGV[@]}"
    expect_rc 0 "$1" || return
    grep -qF "(schema v$SCHEMA)" "$OUT" || bad "the re-run did not read the store's version: $(flat "$OUT")"
    [[ "$(db_version "$J7H")" == "$SCHEMA" ]] || bad "store at v$(db_version "$J7H"); want v$SCHEMA"
}

# J7: "Re-running this install retries the read."
test_J7_VerificationFailedRerun() {
    j7_unverified || return
    j7_rerun_verified "re-run once the read works"
}

# J7: "A re-run of this install reads the version again: below v<T> it
# brings state.db to v<T> again, above v<T> it stops at the store open
# (ErrSchemaMismatch), and at v<T> it finishes the install." with state.db set
# to each after a readable user_version != target (b.wt9). Contacting the
# maintainers is a human step; not followed.
test_J7_VersionMismatchRerun() {
    local version
    for version in $((SCHEMA - 1)) $((SCHEMA + 1)) "$SCHEMA"; do
        j7_mismatch || continue
        "$SQLITE" "$J7H/.agent-director/state.db" "PRAGMA user_version = $version;"
        if ((version > SCHEMA)); then
            run "$J7H" "${J7ARGV[@]}"
            expect_rc 5 "v$version: re-run" || continue
            [[ "$(line_after "install.sh: store open (agent-director list) failed after install")" == *'"err_name":"ErrSchemaMismatch"'* ]] \
                || bad "v$version: the re-run did not stop at the store open with ErrSchemaMismatch: $(flat "$ERR")"
            [[ "$(db_version "$J7H")" == "$version" ]] || bad "v$version: store moved to v$(db_version "$J7H")"
            continue
        fi
        j7_rerun_verified "v$version: re-run" || continue
        if ((version < SCHEMA)); then
            grep -qF "authorized migration v$version→v$SCHEMA " "$OUT" || bad "v$version: the re-run did not authorize the migration: $(flat "$OUT")"
            [[ ! -e "$(sentinel "$J7H")" ]] || bad "v$version: sentinel left after the re-run"
        fi
    done
}

# J7: "a successful open leaves state.db at v<T>: any migration this install
# authorized has run, and its sentinel is consumed. ... Check its version now:
# <command>" (a readable user_version != target, b.wt9): run the command as
# printed, under a plain HOME and one whose path holds shell characters.
test_J7_VersionMismatchCheckVersion() {
    local HOME_TAG cmd
    for HOME_TAG in "" "\$x\`y\`'q\"z."; do
        j7_mismatch || continue
        grep -qF "authorized migration v$((SCHEMA - 1))→v$SCHEMA " "$OUT" || bad "HOME $J7H: the install authorized no migration: $(flat "$OUT")"
        if compgen -G "$(sentinel "$J7H")*" >/dev/null; then
            bad "HOME $J7H: the sentinel was not consumed: $(compgen -G "$(sentinel "$J7H")*")"
        fi
        cmd="$(line_after "Check its version now:")"
        [[ -n "$cmd" ]] || { bad "HOME $J7H: no advised check command"; continue; }
        run "$J7H" bash -c "$cmd"
        expect_rc 0 "advised: $cmd" || continue
        [[ "$(cat "$OUT")" == "$SCHEMA" ]] || bad "HOME $J7H: the advised check prints \"$(cat "$OUT")\"; want $SCHEMA (state.db at v$SCHEMA)"
    done
}

# J7: "Re-running this install retries the read." when step 2 cannot read an
# existing store's version: nothing authorized, the store left as it was (b.n5a).
test_J7_UnreadableBeforeOpenRerun() {
    j7_older_store || return
    j7_run 1
    expect_rc 5 "step 2's read failed" || return
    local want="install.sh: reading state.db's schema version FAILED"
    [[ "$(head -n 1 "$ERR")" == "$want" ]] || bad "first stderr line \"$(head -n 1 "$ERR")\"; want \"$want\""
    grep -qxF "  state.db: $J7H/.agent-director/state.db" "$ERR" || bad "the failure does not name state.db: $(flat "$ERR")"
    j7_unreadable "Reading state.db's user_version (sqlite3 PRAGMA user_version) failed, so the install could not tell whether state.db needs a migration. No migration was authorized. Re-running this install retries the read."
    grep -qF "no existing state.db" "$OUT" && bad "an existing state.db reported as a fresh create: $(flat "$OUT")"
    [[ ! -e "$(sentinel "$J7H")" ]] || bad "a migration was authorized without the store's version"
    [[ "$(db_version "$J7H")" == $((SCHEMA - 1)) ]] || bad "store moved off v$((SCHEMA - 1)): v$(db_version "$J7H")"
    j7_rerun_verified "re-run once the read works"
}

# J7: "Re-running this install retries the read." when step 5 cannot read the
# version of a fresh or already-current store, no migration expected (b.n5a).
test_J7_UnreadableAfterOpenRerun() {
    local store call want="install.sh: reading state.db's schema version after the store open FAILED"
    for store in fresh current; do
        if [[ "$store" == fresh ]]; then
            J7H="$(new_home)" call=1 # no state.db: step 5's read is the first
        else
            j7_installed || continue
            call=2
        fi
        j7_run "$call"
        expect_rc 5 "$store store, step 5's read failed" || continue
        [[ "$(head -n 1 "$ERR")" == "$want" ]] || bad "$store: first stderr line \"$(head -n 1 "$ERR")\"; want \"$want\""
        j7_schema_unreadable
        j7_unreadable "Reading state.db's user_version (sqlite3 PRAGMA user_version) failed, so the install could not check state.db's schema version. Re-running this install retries the read."
        j7_rerun_verified "$store store, re-run once the read works"
    done
}

# J7: "Re-running this install retries the read." when mktemp cannot create the
# reads' sqlite3 error file: a failed read at step 2 or 5 is reported without
# sqlite3's error, and the re-run, mktemp still failing, verifies the store
# (b.wfe).
test_J7_NoErrorFileUnreadableRerun() {
    local call want could refused
    for call in 1 2; do # step 2's read, then step 5's after the migrating open
        PATH_EXTRA=""
        j7_older_store || continue
        PATH_EXTRA="$MKTEMP_FAILS"
        rm -f "$MKTEMP_REFUSALS"
        j7_run "$call"
        expect_rc 5 "no error file, read $call failed" || continue
        want="install.sh: reading state.db's schema version FAILED"
        could="tell whether state.db needs a migration. No migration was authorized."
        if [[ "$call" == 2 ]]; then
            want="install.sh: schema migration verification FAILED" could="check the migration."
            j7_schema_unreadable
        fi
        grep -qxF "$want" "$ERR" || bad "read $call: no \"$want\" line: $(flat "$ERR")"
        j7_unreadable "Reading state.db's user_version (sqlite3 PRAGMA user_version) failed, so the install could not $could Re-running this install retries the read." \
            "  Reading state.db's user_version (sqlite3 PRAGMA user_version)"
        j7_rerun_verified "read $call: re-run, mktemp still failing" || continue
        if [[ "$call" == 1 ]]; then
            grep -qxF "  schema  : migration verified — state.db now at v$SCHEMA" "$OUT" \
                || bad "read 1: the re-run did not verify the migration: $(flat "$OUT")"
        fi
        refused="$(cat "$MKTEMP_REFUSALS" 2>/dev/null | wc -l)"
        [[ "$refused" == 2 ]] || bad "read $call: mktemp refused the sqlite3 error file $refused times; want 2, once per run"
    done
}

# j7_shown: the lines the report shows under "<unreadable>": the read's output,
# then sqlite3's error.
j7_shown() {
    awk '/^  Reading state\.db/ { f = 0 } f; $0 == "  actual   user_version: <unreadable>" { f = 1 }' "$ERR"
}

# j7_not_a_version <answer> <cause>: the report of a read that printed <answer>
# (printf %b) shows it under "<unreadable>", gives <cause> and the advice word
# for word, names the sqlite3 on PATH, and never feeds <answer> to a printf %d,
# the version compare or a sentinel (b.hk7).
j7_not_a_version() {
    local want
    want="$(printf '%b\n' "$1" | sed 's/^/    /')"
    [[ "$(j7_shown)" == "$want" ]] || bad "the report shows \"$(j7_shown)\" under \"<unreadable>\"; want the read's output \"$want\""
    expect_advice "$2 sqlite3 on PATH: $TOOLBOX/sqlite3 A re-run gets the same output unless that sqlite3 or state.db changes."
    if grep -qF -e "invalid number" -e "contact the maintainers" "$ERR"; then
        bad "the read's output reached a printf %d or the version compare: $(flat "$ERR")"
    fi
    if compgen -G "$(sentinel "$J7H")*" >/dev/null; then
        bad "left in ~/.agent-director: $(compgen -G "$(sentinel "$J7H")*")"
    fi
}

# j7_rerun_same <call> [<answer>]: re-run as the last j7_run did, with that
# sqlite3 and state.db: exit 5 again, showing the same output.
j7_rerun_same() {
    local shown; shown="$(j7_shown)"
    j7_run "$@"
    expect_rc 5 "re-run with that sqlite3 and state.db unchanged" || return 1
    [[ "$(j7_shown)" == "$shown" ]] || bad "the re-run shows \"$(j7_shown)\" under \"<unreadable>\"; the first run showed \"$shown\""
}

# J7: "A re-run gets the same output unless that sqlite3 or state.db changes."
# when step 2's read prints no whole number: a sqlite3 printing a header line
# (as a .headers on ~/.sqliterc does) or a leading zero (08, which printf %d
# rejects as octal; 010, which it reads as 8), or a store at user_version -1.
# Nothing is authorized; once the one named changes, the re-run migrates (b.hk7).
test_J7_NotAVersionBeforeOpenRerun() {
    local spec changed call answer stand_in version want="install.sh: reading state.db's schema version FAILED"
    for spec in "sqlite3|user_version\n$((SCHEMA - 1))" "sqlite3|08" "sqlite3|010" "state.db|-1"; do
        changed="${spec%%|*}" answer="${spec#*|}"
        j7_older_store || continue
        if [[ "$changed" == sqlite3 ]]; then
            version=$((SCHEMA - 1)) call=1 stand_in="$answer"
        else
            version="$answer" call=0 stand_in=""
            "$SQLITE" "$J7H/.agent-director/state.db" "PRAGMA user_version = $version;"
        fi
        j7_run "$call" "$stand_in"
        expect_rc 5 "step 2's read printed \"$answer\"" || continue
        [[ "$(head -n 1 "$ERR")" == "$want" ]] || bad "\"$answer\": first stderr line \"$(head -n 1 "$ERR")\"; want \"$want\""
        j7_not_a_version "$answer" "Reading state.db's user_version (sqlite3 PRAGMA user_version) printed the output above, not a whole number (0 or more), so the install could not tell whether state.db needs a migration. No migration was authorized."
        j7_rerun_same "$call" "$stand_in" || continue
        [[ "$(db_version "$J7H")" == "$version" ]] || bad "\"$answer\": store moved off v$version: v$(db_version "$J7H")"
        if [[ "$changed" == state.db ]]; then
            "$SQLITE" "$J7H/.agent-director/state.db" "PRAGMA user_version = $((SCHEMA - 1));"
        fi
        j7_rerun_verified "\"$answer\": re-run once $changed changed"
    done
}

# J7: "A re-run gets the same output unless that sqlite3 or state.db changes."
# when step 5's read prints no whole number (a sqlite3 printing JSON, as a
# .mode json ~/.sqliterc does), after an open that migrated an older store,
# created a fresh one or found it current, or the target version with a leading
# zero after a migration (which the version compare holds different from the
# target); the re-run with the real sqlite3 at that path verifies it (b.hk7).
test_J7_NotAVersionAfterOpenRerun() {
    local spec store call want could answer json="[{\"user_version\":$SCHEMA}]"
    for spec in "older|$json" "older|0$SCHEMA" "fresh|$json" "current|$json"; do
        store="${spec%%|*}" answer="${spec#*|}"
        want="install.sh: reading state.db's schema version after the store open FAILED" could="check state.db's schema version"
        case "$store" in
            older)
                j7_older_store || continue
                call=2 want="install.sh: schema migration verification FAILED" could="check the migration" ;;
            fresh) J7H="$(new_home)" call=1 ;; # no state.db: step 5's read is the first
            current) j7_installed || continue; call=2 ;;
        esac
        j7_run "$call" "$answer"
        expect_rc 5 "$store store, step 5's read printed \"$answer\"" || continue
        [[ "$(head -n 1 "$ERR")" == "$want" ]] || bad "$store store, \"$answer\": first stderr line \"$(head -n 1 "$ERR")\"; want \"$want\""
        j7_schema_unreadable
        j7_not_a_version "$answer" "Reading state.db's user_version (sqlite3 PRAGMA user_version) printed the output above, not a whole number (0 or more), so the install could not $could."
        j7_rerun_same 2 "$answer" || continue # state.db exists now: step 5's read is the second
        j7_rerun_verified "$store store, \"$answer\", re-run with the real sqlite3"
    done
}

# ---- J8: required tool missing ---------------------------------------------------

# J8: "Install <tool> via your package manager ..." and friends: put the tool
# on PATH and re-run the same command.
test_J8_MissingToolProvideAndRerun() {
    local tool want h argv
    while IFS='|' read -r tool want; do
        h="$(new_home)"
        argv=(bash "$LOOSE" --binary "$BIN" --admin-binary "$ADMIN" --no-hooks --no-symlink)
        [[ "$tool" == curl ]] && argv=(bash "$LOOSE" --from-release "$REL_TAG" --no-hooks --no-symlink)
        mv "$TOOLBOX/$tool" "$ROOT/aside/$tool"
        run "$h" "${argv[@]}"
        mv "$ROOT/aside/$tool" "$TOOLBOX/$tool"
        expect_rc 2 "$tool missing" || continue
        expect_advice "required tool not found on PATH: $tool"
        expect_advice "$want"
        run "$h" "${argv[@]}"
        expect_rc 0 "re-run with $tool on PATH" || continue
        grep -qx "install.sh: pre-flight OK" "$OUT" || bad "$tool: no \"pre-flight OK\" after the re-run"
        expect_installed "$h" "$BIN" "$ADMIN"
    done <<'EOF'
claude|Install Claude Code first: https://claude.com/claude-code
tmux|Install tmux via your package manager (apt/brew/dnf/etc.).
jq|Install jq via your package manager (we use it to safely edit settings.json).
file|Install file via your package manager (apt install file / brew install file-formula / dnf install file). Required for the --binary architecture probe.
sqlite3|Install sqlite3 via your package manager (apt install sqlite3 / brew install sqlite / dnf install sqlite). Required to read state.db's schema version for the migration flow.
curl|--from-release downloads via curl; install it via your package manager.
EOF
}

# ---- J9: agent-director and agent-director-admin stamps differ or carry no commit (b.vqr)

# J9: "rebuild both first:  make build": a fresh agent-director beside a stale
# agent-director-admin in the tree's bin/.
test_J9_StampMismatchMakeBuild() {
    local h; h="$(new_home)"
    run_advised "$h" "$TREE" "make build"
    expect_rc 0 "make build in the tree" || return
    cp "$ADMIN_OLD" "$TREE/bin/agent-director-admin"
    local argv=(bash "$TREE_SH" --binary "$TREE/bin/agent-director" --no-hooks --no-symlink)
    run_in "$h" "$TREE" "${argv[@]}"
    expect_rc 3 "stale agent-director-admin" || return
    expect_advice "install.sh: agent-director and agent-director-admin version stamps differ; refusing to install."
    expect_advice "(0.0.1-advice-old $OLD_COMMIT)"
    expect_advice "rebuild both first: make build"
    expect_nothing_installed "$h"
    local cmd; cmd="$(advice_after "rebuild both first:")" || { bad "no advised command"; return; }
    run_advised "$h" "$TREE" "$cmd"
    expect_rc 0 "advised: $cmd" || return
    run_in "$h" "$TREE" "${argv[@]}"
    expect_rc 0 "re-run after make build" && expect_installed "$h" "$TREE/bin/agent-director" "$TREE/bin/agent-director-admin"
}

# J9: "or download release: rerun with --from-release (omit --binary and
# --admin-binary)", for an agent-director-admin whose stamp differs from
# agent-director's in version and commit, in commit only, or that has no
# version verb at all: each refused, naming both stamps, with nothing
# installed.
test_J9_StampMismatchFromRelease() {
    local h i admins=("$ADMIN_OLD" "$ADMIN_OTHER_COMMIT" "$NO_VERSION")
    local stamps=("0.0.1-advice-old $OLD_COMMIT" "0.0.2-advice $OTHER_COMMIT" "<no version stamp>")
    for i in "${!admins[@]}"; do
        h="$(new_home)"
        FAKE_CURL_API_TAG=""
        run "$h" bash "$LOOSE" --binary "$BIN" --admin-binary "${admins[$i]}" --no-hooks --no-symlink
        expect_rc 3 "agent-director-admin stamped (${stamps[$i]})" || continue
        expect_advice "install.sh: agent-director and agent-director-admin version stamps differ; refusing to install."
        expect_advice "agent-director : $BIN (0.0.2-advice $CUR_COMMIT)"
        expect_advice "agent-director-admin: ${admins[$i]} (${stamps[$i]})"
        expect_advice "or download release: rerun with --from-release (omit --binary and --admin-binary)"
        expect_nothing_installed "$h"
        FAKE_CURL_API_TAG="$REL_TAG"
        run "$h" bash "$LOOSE" --no-hooks --no-symlink --from-release
        expect_rc 0 "(${stamps[$i]}) rerun with --from-release, no --binary or --admin-binary" && expect_installed "$h" "$BIN" "$ADMIN"
    done
}

# J9: "rebuild both first:  make build" when both binaries are plain `go
# build`s (commit "unknown"), which nothing shows come from one build: refused
# with nothing installed; make build in their checkout stamps both, and the
# same command then installs them.
test_J9_NoCommitStampMakeBuild() {
    local h; h="$(new_home)"
    mkdir -p "$TREE/bin" && cp "$BIN_PLAIN" "$TREE/bin/agent-director" && cp "$ADMIN_PLAIN" "$TREE/bin/agent-director-admin" \
        || { bad "put the plain builds in the tree's bin/"; return; }
    local argv=(bash "$LOOSE" --binary "$TREE/bin/agent-director" --admin-binary "$TREE/bin/agent-director-admin" --no-hooks --no-symlink)
    run "$h" "${argv[@]}"
    expect_rc 3 "no commit stamp" || return
    local want="install.sh: agent-director and agent-director-admin carry no commit stamp, so they cannot be shown to come from the same build; refusing to install."
    [[ "$(head -n 1 "$ERR")" == "$want" ]] || bad "first stderr line \"$(head -n 1 "$ERR")\"; want \"$want\""
    expect_advice "agent-director-admin: $TREE/bin/agent-director-admin (dev unknown)"
    expect_advice "rebuild both first: make build"
    expect_nothing_installed "$h"
    local cmd; cmd="$(advice_after "rebuild both first:")" || { bad "no advised command"; return; }
    run_advised "$h" "$TREE" "$cmd"
    expect_rc 0 "advised: $cmd" || return
    run "$h" "${argv[@]}"
    expect_rc 0 "re-run after make build" && expect_installed "$h" "$TREE/bin/agent-director" "$TREE/bin/agent-director-admin"
}

# ---- J10: --from-release of a release before 0.11.0 (b.vqr) ------------------

# J10: "install release 0.11.0 or later: bash $0 --from-release <tag of v0.11.0
# or later>": the missing agent-director-admin asset of a release before 0.11.0
# is refused at once, with no retry and no gh fallback (gh on PATH) and nothing
# installed; the advised release installs both.
test_J10_PreAdminReleaseInstallNewer() {
    local h cmd tag=v0.10.0
    h="$(new_home)"
    PATH_EXTRA="$GH_DIR"
    FAKE_CURL_ADMIN_STATUS=404
    run "$h" bash "$LOOSE" --from-release "$tag" --no-hooks --no-symlink
    expect_rc 3 "release $tag, no agent-director-admin asset" || return
    local want="install.sh: --from-release: release $tag has no agent-director-admin binary; refusing to install."
    [[ "$(head -n 1 "$ERR")" == "$want" ]] || bad "first stderr line \"$(head -n 1 "$ERR")\"; want \"$want\""
    grep -qF "asset not yet available" "$ERR" && bad "the agent-director-admin asset was retried"
    grep -qF "gh release download" "$ERR" && bad "the gh fallback was tried"
    expect_advice "install release 0.11.0 or later: bash $LOOSE --from-release <tag of v0.11.0 or later>"
    expect_nothing_installed "$h"
    cmd="$(advice_after "install release 0.11.0 or later: ")" || { bad "no advised command"; return; }
    FAKE_CURL_ADMIN_STATUS="" # a release from 0.11.0 on ships agent-director-admin
    run_advised "$h" "$ROOT" "${cmd/<tag of v0.11.0 or later>/$REL_TAG}"
    expect_rc 0 "advised: $cmd" && expect_installed "$h" "$BIN" "$ADMIN"
}

# ---- J11: --keep-prior rollback (b.vqr) ------------------------------------------

# j11_paths <home>: set J11C and J11A, the installed agent-director and
# agent-director-admin under home.
j11_paths() {
    J11C="$1/.agent-director/bin/agent-director" J11A="$1/.agent-director/admin/agent-director-admin"
}

# The reasons --keep-prior gives for snapshotting neither binary (b.2wk): the
# installed pair is already the one being installed, or agent-director is and
# no agent-director-admin is installed.
J11_SAME_PAIR="the installed agent-director and agent-director-admin are already the ones being installed"
J11_SAME_NO_ADMIN="the installed agent-director is already the one being installed, and no agent-director-admin is installed"

# j11_not_snapshotted <label> <outcome> <reason> [<advice>]: stdout has
# --keep-prior's <label> line for a pair it did not snapshot; outcome is
# "kept <target>.prior" or "none", advice what follows the parenthesis.
j11_not_snapshotted() {
    local line="  $1: $2 (not snapshotted: $3)${4:-}"
    grep -qxF "$line" "$OUT" || bad "no \"$line\" line: $(grep -F "prior" "$OUT" | tr '\n' '|')"
}

# j11_pre_admin <home>: make the install under home look like one from before
# 0.11.0: no agent-director-admin, and a stale agent-director-admin.prior.
j11_pre_admin() {
    j11_paths "$1"
    rm -f "$J11A" && cp "$ADMIN" "$J11A.prior" || { bad "make the pre-0.11.0 install"; return 1; }
}

# j11_roll_back_both <home> <version> [<context>]: roll both binaries back with
# `mv <target>.prior <target>`; the rolled-back pair must be a matching pair,
# both version stamps (version and commit) the same and naming <version>.
j11_roll_back_both() {
    local t stamp ctx="${3:+$3: }"
    for t in "$J11C" "$J11A"; do
        mv "$t.prior" "$t" || { bad "${ctx}mv $t.prior $t"; return 1; }
    done
    run "$1" "$J11C" version
    stamp="$(cat "$OUT")"
    [[ "$stamp" == *"\"$2\""* ]] || bad "${ctx}rolled-back agent-director version: $stamp; want $2"
    run "$1" "$J11A" version
    [[ "$(cat "$OUT")" == "$stamp" ]] || bad "${ctx}rolled-back stamps differ: agent-director $stamp, agent-director-admin $(cat "$OUT")"
}

# J11: "Roll back with `mv <target>.prior <target>`" (--keep-prior, install.sh
# --help): an upgrade with --keep-prior snapshots both binaries, and rolling
# both back restores the old pair, whose version stamps match.
test_J11_KeepPriorRollBackBoth() {
    local h t; h="$(new_home)"; j11_paths "$h"
    run "$h" bash "$LOOSE" --help
    expect_rc 0 "install.sh --help" || return
    [[ "$(flat "$OUT")" == *'Roll back with `mv <target>.prior <target>`.'* ]] || bad "--help lacks the rollback advice: $(flat "$OUT")"
    run "$h" bash "$LOOSE" --binary "$BIN_OLD" --admin-binary "$ADMIN_OLD" --no-hooks --no-symlink
    expect_rc 0 "install the old pair" || return
    run "$h" bash "$LOOSE" --binary "$BIN" --admin-binary "$ADMIN" --keep-prior --no-hooks --no-symlink
    expect_rc 0 "upgrade with --keep-prior" || return
    grep -qxF "  prior   : snapshotted to $J11C.prior" "$OUT" || bad "no agent-director prior line: $(flat "$OUT")"
    grep -qxF "  admin prior: snapshotted to $J11A.prior" "$OUT" || bad "no agent-director-admin prior line: $(flat "$OUT")"
    for t in "$J11C" "$J11A"; do
        [[ "$(stat -c %a "$t.prior")" == 755 ]] || bad "$t.prior has mode $(stat -c %a "$t.prior"); want 755"
    done
    j11_roll_back_both "$h" 0.0.1-advice-old || return
    cmp -s "$J11C" "$BIN_OLD" || bad "rolled-back agent-director is not the old one"
    cmp -s "$J11A" "$ADMIN_OLD" || bad "rolled-back agent-director-admin is not the old one"
}

# J11: "Both binaries are snapshotted ..., so rolling back both restores a
# matching pair" (install.sh --help) when only one of the pair changes: the
# snapshot is decided for the pair, not per binary (b.2wk). After an upgrade
# with --keep-prior, installing a pair that differs from the installed one in
# agent-director alone, or in agent-director-admin alone, snapshots both again,
# to the pair installed before this run, and rolling both back restores it.
test_J11_KeepPriorOneChangedSnapshotsBoth() {
    local spec c a changed h
    for spec in "$BIN_NUL|$ADMIN|agent-director" "$BIN|$ADMIN_NUL|agent-director-admin"; do
        IFS='|' read -r c a changed <<<"$spec"
        h="$(new_home)"; j11_paths "$h"
        run "$h" bash "$LOOSE" --binary "$BIN_OLD" --admin-binary "$ADMIN_OLD" --no-hooks --no-symlink
        expect_rc 0 "install the old pair" || continue
        run "$h" bash "$LOOSE" --binary "$BIN" --admin-binary "$ADMIN" --keep-prior --no-hooks --no-symlink
        expect_rc 0 "upgrade with --keep-prior" || continue
        run "$h" bash "$LOOSE" --binary "$c" --admin-binary "$a" --keep-prior --no-hooks --no-symlink
        expect_rc 0 "$changed alone changed, with --keep-prior" || continue
        expect_installed "$h" "$c" "$a"
        grep -qxF "  prior   : snapshotted to $J11C.prior" "$OUT" \
            || bad "$changed alone changed: no agent-director prior line: $(grep -F "prior" "$OUT" | tr '\n' '|')"
        grep -qxF "  admin prior: snapshotted to $J11A.prior" "$OUT" \
            || bad "$changed alone changed: no agent-director-admin prior line: $(grep -F "prior" "$OUT" | tr '\n' '|')"
        cmp -s "$J11C.prior" "$BIN" || bad "$changed alone changed: agent-director.prior is not the agent-director installed before"
        cmp -s "$J11A.prior" "$ADMIN" || bad "$changed alone changed: agent-director-admin.prior is not the agent-director-admin installed before"
        j11_roll_back_both "$h" 0.0.2-advice "$changed alone changed"
    done
}

# J11: "A re-install of the same pair (agent-director already byte-identical to
# the one being installed, and agent-director-admin either byte-identical too
# or not installed) is not snapshotted" (install.sh --help, b.2wk):
# re-installing the installed pair with --keep-prior and no .prior yet says so
# for both binaries and creates no .prior; so does a re-install with
# agent-director-admin gone, which installs it again.
test_J11_KeepPriorSamePairNotSnapshotted() {
    local h t; h="$(new_home)"; j11_paths "$h"
    local argv=(bash "$LOOSE" --binary "$BIN" --admin-binary "$ADMIN" --no-hooks --no-symlink)
    run "$h" bash "$LOOSE" --help
    expect_rc 0 "install.sh --help" || return
    [[ "$(flat "$OUT")" == *"A re-install of the same pair (agent-director already byte-identical to the one being installed, and agent-director-admin either byte-identical too or not installed) is not snapshotted, so the .prior files from the earlier run are kept."* ]] \
        || bad "--help lacks the not-snapshotted sentence: $(flat "$OUT")"
    run "$h" "${argv[@]}"
    expect_rc 0 "install the pair" || return
    run "$h" "${argv[@]}" --keep-prior
    expect_rc 0 "re-install the pair with --keep-prior" || return
    expect_installed "$h" "$BIN" "$ADMIN"
    j11_not_snapshotted "prior   " none "$J11_SAME_PAIR"
    j11_not_snapshotted "admin prior" none "$J11_SAME_PAIR"
    rm "$J11A" || { bad "rm $J11A"; return; }
    run "$h" "${argv[@]}" --keep-prior
    expect_rc 0 "re-install with --keep-prior, agent-director-admin gone" || return
    expect_installed "$h" "$BIN" "$ADMIN"
    j11_not_snapshotted "prior   " none "$J11_SAME_NO_ADMIN"
    j11_not_snapshotted "admin prior" none "$J11_SAME_NO_ADMIN"
    for t in "$J11C" "$J11A"; do
        [[ ! -e "$t.prior" ]] || bad "$t.prior created for a binary that was not replaced"
    done
}

# J11: "to roll back, remove <agent-director-admin>" (--keep-prior on an
# upgrade from before 0.11.0, with no agent-director-admin installed): a stale
# agent-director-admin.prior is removed, and rolling back as printed restores
# the old agent-director with no agent-director-admin.
test_J11_KeepPriorNoAdminRemoveIt() {
    local h line got; h="$(new_home)"; j11_paths "$h"
    run "$h" bash "$LOOSE" --binary "$BIN_OLD" --admin-binary "$ADMIN_OLD" --no-hooks --no-symlink
    expect_rc 0 "install the old pair" || return
    j11_pre_admin "$h" || return
    run "$h" bash "$LOOSE" --binary "$BIN" --admin-binary "$ADMIN" --keep-prior --no-hooks --no-symlink
    expect_rc 0 "upgrade with --keep-prior" || return
    expect_installed "$h" "$BIN" "$ADMIN"
    [[ ! -e "$J11A.prior" ]] || bad "the stale $J11A.prior is still there"
    line="  admin prior: none (no agent-director-admin was installed); to roll back, remove $J11A"
    grep -qxF "$line" "$OUT" || bad "no \"$line\" line: $(flat "$OUT")"
    got="$(grep -m1 -F "to roll back, remove " "$OUT")" || return
    mv "$J11C.prior" "$J11C" || bad "mv $J11C.prior $J11C"
    rm "${got##*to roll back, remove }" || bad "remove ${got##*to roll back, remove }"
    cmp -s "$J11C" "$BIN_OLD" || bad "rolled-back agent-director is not the old one"
    [[ ! -e "$J11A" ]] || bad "agent-director-admin left after the rollback"
}

# ---- J12: one of --sha256 and --admin-sha256 (b.vqr) -----------------------------

# j12_one_hash <home> <flag>: --from-release with only <flag> (the right hash):
# exit 2 with the refusal and its advice, and nothing installed. Leaves the
# command in J12ARGV.
j12_one_hash() {
    local h="$1" flag="$2" other=--sha256 unverified=agent-director hex="$ADMIN_SHA"
    if [[ "$flag" == --sha256 ]]; then
        other=--admin-sha256 unverified=agent-director-admin hex="$BIN_SHA"
    fi
    J12ARGV=(bash "$LOOSE" --from-release "$REL_TAG" --no-hooks --no-symlink "$flag" "$hex")
    run "$h" "${J12ARGV[@]}"
    expect_rc 2 "$flag alone" || return 1
    local want="install.sh: $flag without $other would install $unverified unverified; refusing to install."
    [[ "$(head -n 1 "$ERR")" == "$want" ]] || bad "first stderr line \"$(head -n 1 "$ERR")\"; want \"$want\""
    expect_advice "Pass $other <hex> too (the sha256 of the $unverified release asset), or neither flag to skip verification."
    expect_nothing_installed "$h"
}

# J12: "Pass --admin-sha256 <hex> too (the sha256 of the agent-director-admin
# release asset)" (and the mirror for --sha256): re-run with the advised flag
# and the named asset's sha256; both are verified and installed.
test_J12_OneHashPassTheOther() {
    local flag h rest asset hex
    for flag in --sha256 --admin-sha256; do
        h="$(new_home)"
        j12_one_hash "$h" "$flag" || continue
        rest="$(advice_after "Pass ")" || { bad "no advised flag"; continue; }
        asset="${rest#*(the sha256 of the }"
        case "${asset%% release asset)*}" in
            agent-director) hex="$BIN_SHA" ;;
            agent-director-admin) hex="$ADMIN_SHA" ;;
            *) bad "the advice names no release asset: $rest"; continue ;;
        esac
        run "$h" "${J12ARGV[@]}" "${rest%% *}" "$hex"
        expect_rc 0 "$flag, then the advised ${rest%% *}" || continue
        expect_installed "$h" "$BIN" "$ADMIN"
        [[ "$(grep -cxE '  (sha256  |admin sha256): verified' "$OUT")" == 2 ]] || bad "$flag: both assets not verified: $(flat "$OUT")"
    done
}

# J12: "or neither flag to skip verification": re-run without the hash flag;
# both are installed, unverified.
test_J12_OneHashPassNeither() {
    local flag h
    for flag in --sha256 --admin-sha256; do
        h="$(new_home)"
        j12_one_hash "$h" "$flag" || continue
        run "$h" "${J12ARGV[@]:0:${#J12ARGV[@]}-2}"
        expect_rc 0 "$flag dropped" || continue
        expect_installed "$h" "$BIN" "$ADMIN"
        grep -q ": verified$" "$OUT" && bad "$flag dropped: a verified line with no hash given: $(flat "$OUT")"
    done
}

# ---- J13: config file refused (b.7b4) ---------------------------------------------

# J13: "Fix what the error above names in the config file, then re-run this
# install." The envelope is the only per-key advice ("A missing key, or 0, gives
# the default." for a refused value), which the remove and zero fixes follow;
# install.sh's own blanket "A missing key gives that key's default; for a
# refused value, so does 0." contradicted it and is gone (b.xbh). A fresh, older or
# current store: the refusal speaks only of the config and authorizes nothing;
# fixing what the envelope names at the printed path and re-running installs,
# migrating an older store.
test_J13_ConfigRefusedFixAndRerun() {
    local spec store config named fix before path key gone
    local want="install.sh: agent-director refused its config file (ErrConfigMalformed)"
    local none="  schema  : state.db at v$SCHEMA; no migration authorization needed"
    for spec in \
        "fresh|[defaults]\nexpire_retention_days = -1|[defaults] expire_retention_days = -1|remove" \
        "older|[defaults]\nexpire_retention_days = -1|[defaults] expire_retention_days = -1|zero" \
        "current|[tmux]\nquery_timeout_ms = -5|[tmux] query_timeout_ms = -5|zero" \
        "older|[defaults|toml: line|syntax"; do
        IFS='|' read -r store config named fix <<<"$spec"
        case "$store" in
            fresh) J7H="$(new_home)" before=none; mkdir -p "$J7H/.agent-director" ;;
            older) j7_older_store || continue; before=$((SCHEMA - 1)) ;;
            current) j7_installed || continue; before="$SCHEMA" ;;
        esac
        printf '%b\n' "$config" >"$J7H/.agent-director/config.toml"
        run "$J7H" "${J7ARGV[@]}"
        expect_rc 5 "$store store, config \"$named\"" || continue
        [[ "$(head -n 1 "$ERR")" == "$want" ]] || bad "$store, \"$named\": first stderr line \"$(head -n 1 "$ERR")\"; want \"$want\""
        grep -qF '  {"err_name":"ErrConfigMalformed",' "$ERR" || bad "$store, \"$named\": no ErrConfigMalformed envelope: $(flat "$ERR")"
        expect_advice "$named"
        expect_advice "Fix what the error above names in the config file, then re-run this install."
        # The remove and zero fixes follow the envelope's own per-key sentence.
        [[ "$fix" == syntax ]] || expect_advice "A missing key, or 0, gives the default."
        for gone in "A missing key gives that key's default" "so does 0"; do
            [[ "$(flat "$ERR")" != *"$gone"* ]] || bad "$store, \"$named\": install.sh's removed advice \"$gone\" is back: $(flat "$ERR")"
        done
        if grep -qiE 'state\.db|migrat|ErrSchemaMismatch' "$ERR"; then
            bad "$store, \"$named\": the config refusal speaks of the store: $(flat "$ERR")"
        fi
        if grep -qE '^  schema  : (state\.db|authorized)' "$OUT"; then
            bad "$store, \"$named\": a step-3 verdict on a store the probe never opened: $(grep -F "  schema  : " "$OUT")"
        fi
        [[ ! -e "$(sentinel "$J7H")" ]] || bad "$store, \"$named\": a migration was authorized under a refused config"
        if [[ "$before" == none ]]; then
            [[ ! -e "$J7H/.agent-director/state.db" ]] || bad "$store, \"$named\": state.db created under a refused config"
        elif [[ "$(db_version "$J7H")" != "$before" ]]; then
            bad "$store, \"$named\": store moved off v$before: v$(db_version "$J7H")"
        fi
        path="$(advice_after "  config  : ")" || { bad "$store, \"$named\": no config line"; continue; }
        [[ "$path" == "$J7H/.agent-director/config.toml" ]] || bad "$store, \"$named\": config line names $path"
        key="${named#*] }"
        key="${key%% =*}"
        case "$fix" in
            remove) sed -i "/^$key = /d" "$path" ;;
            zero) sed -i "s/^$key = .*/$key = 0/" "$path" ;;
            syntax) sed -i 's/^\[defaults$/[defaults]/' "$path" ;;
        esac
        j7_rerun_verified "$store store, re-run after the $fix fix of \"$named\"" || continue
        expect_installed "$J7H" "$BIN" "$ADMIN"
        [[ ! -e "$(sentinel "$J7H")" ]] || bad "$store, \"$named\": sentinel left after the re-run"
        if [[ "$store" == older ]]; then
            grep -qF "authorized migration v$((SCHEMA - 1))→v$SCHEMA " "$OUT" \
                || bad "$store, \"$named\": the re-run did not authorize the pending migration: $(flat "$OUT")"
        elif [[ "$store" == current ]]; then
            # The probe opened the store (no err_name): step 3 says nothing to authorize.
            grep -qxF "$none" "$OUT" || bad "$store, \"$named\": no \"$none\" line: $(grep -F "  schema  : " "$OUT")"
        fi
    done
}

echo "[b.fji install-sh advice-follow] start (schema v$SCHEMA)"
for t in $(declare -F | awk '{print $3}' | grep '^test_'); do
    run_test "$t"
done
echo "[b.fji install-sh advice-follow] summary: $pass passed, $fail failed, $skip skipped (known-broken)"
[[ "$fail" -eq 0 ]]
