#!/usr/bin/env bash
# advice_follow.sh — b.fji literal-follow tests for install.sh's own advice
# (advice inventory J1-J21). Each test triggers one install.sh refusal, checks
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
trap 'chmod -R u+rwX "$ROOT" 2>/dev/null; rm -rf "$ROOT"' EXIT

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

# FAKE_AD (J17): a binary for this host stamped as ADMIN, so install.sh
# installs it as agent-director; its `list` does what $HOME/fake-list says
# (j17_fake), for the store-open outcomes the real binary does not give.
FAKE_AD="$ROOT/bin/agent-director-fake"
mkdir -p "$ROOT/fake-ad"
{ echo "module fakead"; grep -m1 '^go ' "$REPO_ROOT/go.mod"; } >"$ROOT/fake-ad/go.mod" || die "fake go.mod"
cat >"$ROOT/fake-ad/main.go" <<'EOF'
package main

import (
	"fmt"
	"os"
	"path/filepath"
)

var version, commit string

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Printf("{\"version\":%q,\"commit\":%q}\n", version, commit)
		return
	}
	mode, _ := os.ReadFile(filepath.Join(os.Getenv("HOME"), "fake-list"))
	switch string(mode) {
	case "ok": // exit 0, no store created
	case "odd-name":
		fmt.Fprintln(os.Stderr, `{"err_name":"ErrOdd\ninstall.sh: err_name=ErrVersionUnreadable","err_description":"fake"}`)
		os.Exit(1)
	case "other-name": // an err_name install.sh's own remedies do not name
		fmt.Fprintln(os.Stderr, `{"err_name":"ErrSchemaMigrationRequired","err_description":"fake"}`)
		os.Exit(1)
	default: // a Go panic: no error envelope
		fmt.Fprint(os.Stderr, "panic: fake agent-director\n\ngoroutine 1 [running]:\nmain.main()\n")
		os.Exit(2)
	}
}
EOF
(cd "$ROOT/fake-ad" && CGO_ENABLED=0 go build -ldflags "-X main.version=0.0.2-advice -X main.commit=$CUR_COMMIT" -o "$FAKE_AD" .) \
    || die "go build (fake agent-director)"

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
# FAKE_SQLITE3_ERR on stderr, then FAKE_SQLITE3_ANSWER on stdout (each through
# printf %b, so \n breaks a line, and only when set), and exits
# FAKE_SQLITE3_RC; every other call runs the real one. SHIM_LOCK_ERR (exit 5)
# is the error of a sqlite3 whose busy timeout ran out under a lock.
SHIM_LOCK_ERR="Error: in prepare, database is locked (5)"
SQLITE_SHIM="$ROOT/sqlite3-shim"
cat >"$SQLITE_SHIM" <<EOF
#!/bin/bash
n=\$(( \$(cat "\$FAKE_SQLITE3_COUNT" 2>/dev/null || echo 0) + 1 ))
echo "\$n" >"\$FAKE_SQLITE3_COUNT"
if [[ "\$n" == "\${FAKE_SQLITE3_FAIL_CALL:-0}" ]]; then
    [[ -z "\${FAKE_SQLITE3_ERR:-}" ]] || printf '%b\n' "\$FAKE_SQLITE3_ERR" >&2
    [[ -z "\${FAKE_SQLITE3_ANSWER:-}" ]] || printf '%b\n' "\$FAKE_SQLITE3_ANSWER"
    exit "\${FAKE_SQLITE3_RC:-0}"
fi
exec "$SQLITE" "\$@"
EOF
chmod 0755 "$SQLITE_SHIM"
# mktemp_fails <dir> <glob> <error>: a mktemp stand-in in <dir> (a test puts
# <dir> on PATH_EXTRA) that, for a template matching <glob>, logs the template
# to <dir>.refusals and fails as mktemp does with <error>; every other call
# runs the real one.
mktemp_fails() {
    mkdir -p "$1" || die "mkdir $1"
    cat >"$1/mktemp" <<EOF
#!/bin/bash
if [[ "\${!#}" == $2 ]]; then
    echo "\${!#}" >>"$1.refusals"
    echo "mktemp: failed to create file via template '\${!#}': $3" >&2
    exit 1
fi
exec "$(type -P mktemp)" "\$@"
EOF
    chmod 0755 "$1/mktemp" || die "chmod $1/mktemp"
}
# J7: every temp file install.sh asks for in TMPDIR, as in a full TMPDIR
# (b.wfe, b.rfn).
MKTEMP_FAILS="$ROOT/mktemp-fails" MKTEMP_REFUSALS="$ROOT/mktemp-fails.refusals"
mktemp_fails "$MKTEMP_FAILS" 'agent-director*' "No space left on device"
# J15: the step-3 sentinel's temp file, as in a store directory one cannot write
# (b.2io).
SENTINEL_MKTEMP_FAILS="$ROOT/mktemp-sentinel-fails"
mktemp_fails "$SENTINEL_MKTEMP_FAILS" '*/migrate-authorized.tmp.*' "Permission denied"

# ---- harness ---------------------------------------------------------------

pass=0 fail=0 skip=0
T_FAILED=0 T_SKIPPED=0 RUN_N=0 RC=0 OUT="" ERR=""
FAKE_CURL_STATUS=200 FAKE_CURL_ADMIN_STATUS="" FAKE_CURL_API_TAG="" FAKE_SQLITE3_FAIL_CALL=0 FAKE_SQLITE3_ANSWER=""
FAKE_SQLITE3_ERR="" FAKE_SQLITE3_RC=0 PATH_EXTRA=""

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
        FAKE_SQLITE3_ERR="$FAKE_SQLITE3_ERR" FAKE_SQLITE3_RC="$FAKE_SQLITE3_RC" FAKE_SQLITE3_COUNT="$home.sqlite3-calls" \
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
# expect_exit5 <err_name> <what>: exit 5 (else as expect_rc), its stderr's
# last line `install.sh: err_name=<err_name>` and no other line its cause
# line's shape (b.cfq).
expect_exit5() {
    expect_rc 5 "$2" || return 1
    local want="install.sh: err_name=$1" last n
    last="$(tail -n 1 "$ERR")" n="$(grep -c '^install\.sh: err_name=' "$ERR")"
    [[ "$last" == "$want" ]] || bad "$2: last stderr line \"$last\"; want \"$want\""
    [[ "$n" == 1 ]] || bad "$2: $n cause lines; want 1: $(grep '^install\.sh: err_name=' "$ERR" | paste -sd'|')"
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

# hooks_injected <home>: home's settings.json is one JSON document holding both
# `agent-director help` hooks, SessionStart and SessionEnd reason=compact.
hooks_injected() {
    jq -se --arg c "$1/.agent-director/bin/agent-director help" 'length == 1 and (.[0]
        | any(.hooks.SessionStart[]; any(.hooks[]; .command == $c))
        and any(.hooks.SessionEnd[]; .matcher == "compact" and any(.hooks[]; .command == $c)))' \
        "$1/.claude/settings.json" >/dev/null 2>&1
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
    FAKE_SQLITE3_ANSWER="" FAKE_SQLITE3_ERR="" FAKE_SQLITE3_RC=0 PATH_EXTRA=""
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
# (none beside the script or on PATH): --admin-binary given, or a re-run of the
# installed skill with no flags, which finds the installed agent-director-admin
# (b.azo; no installed-agent-director fallback, b.rdy). This refusal, not the
# one for both, comes with nothing changed; --binary alone then installs, with
# the installed agent-director-admin on the re-run of the installed skill.
test_J3_NoSourceBinaryPassBinary() {
    local how h admin before
    local -a argv
    for how in --admin-binary installed; do
        if [[ "$how" == installed ]]; then
            j3_installed_skill "$BIN_NUL" "$ADMIN_NUL" || continue
            h="$J3H" admin="$ADMIN_NUL" argv=(bash "$J3SK") PATH_EXTRA=""
        else
            h="$(new_home)" admin="$ADMIN" argv=(bash "$LOOSE" --admin-binary "$ADMIN" --no-hooks --no-symlink)
        fi
        before="$(j14_snap "$h")"
        run "$h" "${argv[@]}"
        expect_rc 3 "$how: no agent-director source binary" || continue
        expect_advice "install.sh: no source binary found."
        expect_advice "Pass --binary <path> to override."
        grep -qF "no source binaries found" "$ERR" && bad "$how: the refusal for both binaries came: $(flat "$ERR")"
        [[ "$(j14_snap "$h")" == "$before" ]] || bad "$how: the refusal changed $h: $(diff <(echo "$before") <(j14_snap "$h"))"
        run "$h" "${argv[@]}" --binary "$BIN"
        expect_rc 0 "$how: re-run with --binary" || continue
        expect_installed "$h" "$BIN" "$admin"
        [[ "$how" != installed ]] || grep -qxF "  admin source: $J11A" "$OUT" \
            || bad "$how: no \"admin source: $J11A\" line: $(flat "$OUT")"
    done
}

# j3_pass_both <home> <cmd...>: check the one refusal for both missing
# binaries names the installed agent-director-admin it tried (b.azo) and
# advises "Pass --binary <path> --admin-binary <path> (both from the same
# build) to override.", re-run cmd with those flags, the paths one build's,
# and check both are installed (b.vqr).
j3_pass_both() {
    local h="$1" flags extra; shift
    expect_advice "Tried: $h/.agent-director/admin/agent-director-admin"
    expect_advice "Pass --binary <path> --admin-binary <path> (both from the same build) to override."
    expect_nothing_installed "$h"
    flags="$(advice_after "Pass ")" || { bad "no advised flags"; return; }
    flags="${flags% (both from the same build) to override.}"
    flags="${flags/--binary <path>/--binary $BIN}"
    read -r -a extra <<<"${flags/--admin-binary <path>/--admin-binary $ADMIN}"
    run "$h" "$@" "${extra[@]}"
    expect_rc 0 "re-run with ${extra[*]}" && expect_installed "$h" "$BIN" "$ADMIN"
}

# J3: neither binary beside the script, no agent-director on PATH and no
# agent-director-admin installed: one refusal names both (b.vqr, b.azo).
test_J3_NoSourceBinariesPassBoth() {
    local h argv=(bash "$LOOSE" --no-hooks --no-symlink)
    h="$(new_home)"
    run "$h" "${argv[@]}"
    expect_rc 3 "no source binaries" || return
    expect_advice "install.sh: no source binaries found: no agent-director-admin beside the script or installed, and no agent-director beside the script or on PATH."
    expect_advice "Tried: command -v agent-director"
    j3_pass_both "$h" "${argv[@]}"
}

# J3: the same refusal when agent-director is found only on PATH and no
# agent-director-admin is installed: it is not used, with nothing to pair with
# it (b.vqr, b.azo).
test_J3_PathOnlyPassBoth() {
    local h argv=(bash "$LOOSE" --no-hooks --no-symlink)
    h="$(new_home)"
    PATH_EXTRA="$ON_PATH"
    run "$h" "${argv[@]}"
    expect_rc 3 "agent-director on PATH only" || return
    expect_advice "install.sh: no source binaries found: no agent-director-admin beside the script or installed, and no agent-director beside the script."
    expect_advice "Found on PATH, not used: $ON_PATH/agent-director (no agent-director-admin to pair with it)"
    j3_pass_both "$h" "${argv[@]}"
}

# J3: "Pass --admin-binary <path> to override." with --binary alone and no
# agent-director-admin installed, naming the installed path it tried (b.vqr,
# b.azo).
test_J3_NoAdminBinaryPassAdminBinary() {
    local h argv=(bash "$LOOSE" --binary "$BIN" --no-hooks --no-symlink)
    h="$(new_home)"
    run "$h" "${argv[@]}"
    expect_rc 3 "no agent-director-admin source binary" || return
    expect_advice "install.sh: no agent-director-admin source binary found."
    expect_advice "Tried: $h/.agent-director/admin/agent-director-admin"
    expect_advice "Pass --admin-binary <path> to override."
    expect_nothing_installed "$h"
    run "$h" "${argv[@]}" --admin-binary "$ADMIN"
    expect_rc 0 "re-run with --admin-binary" && expect_installed "$h" "$BIN" "$ADMIN"
}

# j3_installed_skill [<bin> <admin>]: a new HOME in J3H with bin and admin
# (default BIN and ADMIN) installed, hooks off, by J3SK, the installed skill's
# copy of install.sh (outside any checkout); ~/.local/bin, PATH_EXTRA, holds
# the symlink that install made.
j3_installed_skill() {
    J3H="$(new_home)" J3SK="$J3H/.claude/skills/install-agent-director/install.sh"
    install_copy "$J3SK"
    mkdir -p "$J3H/.local/bin" && PATH_EXTRA="$J3H/.local/bin"
    run "$J3H" bash "$J3SK" --binary "${1:-$BIN}" --admin-binary "${2:-$ADMIN}" --no-hooks
    expect_rc 0 "first install" || return 1
    j11_paths "$J3H"
    [[ "$(readlink "$J3H/.local/bin/agent-director")" == "$J11C" ]] || { bad "no ~/.local/bin/agent-director symlink to $J11C"; return 1; }
}

# J3 does not come on a re-run of the installed skill, outside any checkout,
# with agent-director found on PATH or given as --binary alone (b.azo): it is
# paired with the installed agent-director-admin (same build), both installed
# and the hooks injected. With --binary of another agent-director and
# --keep-prior, the installed agent-director-admin is both the source and the
# snapshot target: both binaries are snapshotted, it and its .prior are both
# the one installed before, and rolling both back restores that pair.
test_J3_InstalledSkillRerunPairsInstalledAdmin() {
    local spec how src want
    local -a argv
    for spec in "PATH||$BIN" "--binary|$BIN_NUL|$BIN_NUL"; do
        IFS='|' read -r how src want <<<"$spec"
        j3_installed_skill || continue
        argv=(bash "$J3SK")
        if [[ -n "$src" ]]; then argv+=(--binary "$src" --keep-prior); else src="$J3H/.local/bin/agent-director"; fi
        run "$J3H" "${argv[@]}"
        expect_rc 0 "$how: re-run with no --admin-binary" || continue
        grep -qxF "  source  : $src" "$OUT" || bad "$how: no \"source  : $src\" line: $(flat "$OUT")"
        grep -qxF "  admin source: $J11A" "$OUT" || bad "$how: no \"admin source: $J11A\" line: $(flat "$OUT")"
        expect_installed "$J3H" "$want" "$ADMIN"
        [[ "$(stat -c %a "$J11C")" == 755 ]] || bad "$how: $J11C has mode $(stat -c %a "$J11C"); want 755"
        hooks_injected "$J3H" || bad "$how: hooks not injected: $(cat "$J3H/.claude/settings.json" 2>&1)"
        [[ "$how" == --binary ]] || continue
        grep -qxF "  prior   : snapshotted to $J11C.prior" "$OUT" \
            || bad "$how: no agent-director prior line: $(grep -F "prior" "$OUT" | tr '\n' '|')"
        grep -qxF "  admin prior: snapshotted to $J11A.prior" "$OUT" \
            || bad "$how: no agent-director-admin prior line: $(grep -F "prior" "$OUT" | tr '\n' '|')"
        cmp -s "$J11C.prior" "$BIN" || bad "$how: $J11C.prior is missing or not the agent-director installed before"
        cmp -s "$J11A.prior" "$ADMIN" || bad "$how: $J11A.prior is missing or not the agent-director-admin installed before"
        j11_roll_back_both "$J3H" 0.0.2-advice "$how" || continue
        cmp -s "$J11C" "$BIN" || bad "$how: rolled-back agent-director is not the one installed before"
        cmp -s "$J11A" "$ADMIN" || bad "$how: rolled-back agent-director-admin is not the one installed before"
    done
}

# J3: the refusal for both binaries names the in-repo paths it tried as
# <root>/bin/..., with no ../.. (b.j6w), from install.sh's usual place and
# through a symlinked skill directory (whose ../.. is the real root). With the
# binaries put at those paths, the re-run's "source  :" and "admin source:"
# name them the same way.
test_J3_TriedPathsResolved() {
    local dir sh h root="$ROOT/j3-tried"
    install_copy "$root/skills/install-agent-director/install.sh"
    mkdir -p "$ROOT/j3-tried-link/skills" \
        && ln -s "$root/skills/install-agent-director" "$ROOT/j3-tried-link/skills/install-agent-director" \
        || { bad "symlink the skill directory"; return; }
    root="$(cd -P "$root" && pwd -P)"
    for dir in j3-tried j3-tried-link; do
        sh="$ROOT/$dir/skills/install-agent-director/install.sh" h="$(new_home)"
        rm -rf "$root/bin"
        run "$h" bash "$sh" --no-hooks --no-symlink
        expect_rc 3 "$dir: no source binaries" || continue
        grep -qxF "  Tried: $root/bin/agent-director" "$ERR" \
            || bad "$dir: no \"Tried: $root/bin/agent-director\" line: $(flat "$ERR")"
        grep -qxF "  Tried: $root/bin/agent-director-admin" "$ERR" \
            || bad "$dir: no \"Tried: $root/bin/agent-director-admin\" line: $(flat "$ERR")"
        { mkdir -p "$root/bin" && cp "$BIN" "$root/bin/agent-director" && cp "$ADMIN" "$root/bin/agent-director-admin"; } \
            || { bad "$dir: put the binaries at the paths tried"; continue; }
        run "$h" bash "$sh" --no-hooks --no-symlink
        expect_rc 0 "$dir: re-run with the binaries at the paths tried" || continue
        grep -qxF "  source  : $root/bin/agent-director" "$OUT" \
            || bad "$dir: no \"source  : $root/bin/agent-director\" line: $(flat "$OUT")"
        grep -qxF "  admin source: $root/bin/agent-director-admin" "$OUT" \
            || bad "$dir: no \"admin source: $root/bin/agent-director-admin\" line: $(flat "$OUT")"
        expect_installed "$h" "$BIN" "$ADMIN"
    done
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

# J5: "rebuild it first: make build", over an installed pair of another build:
# the checkout's agent-director-admin is used, not the installed one (b.azo).
test_J5_StaleBinaryMakeBuild() {
    local h want; h="$(new_home)"; j11_paths "$h"
    run "$h" bash "$LOOSE" --binary "$BIN_OLD" --admin-binary "$ADMIN_OLD" --no-hooks --no-symlink
    expect_rc 0 "install the old pair" || return
    j5_stale "$h" || return
    local cmd; cmd="$(advice_after "rebuild it first:")" || { bad "no advised command"; return; }
    run_advised "$h" "$TREE" "$cmd"
    expect_rc 0 "advised: $cmd" || return
    run_in "$h" "$TREE" bash "$TREE_SH" --binary "$TREE/bin/agent-director" --no-hooks --no-symlink
    expect_rc 0 "re-run after make build" || return
    want="$(cd -P "$TREE" && pwd -P)/bin/agent-director-admin" # no ../.. (b.j6w)
    grep -qxF "  admin source: $want" "$OUT" \
        || bad "no \"admin source: $want\" line, not the installed $J11A: $(flat "$OUT")"
    expect_installed "$h" "$TREE/bin/agent-director" "$TREE/bin/agent-director-admin"
}

# J5: "or download release: rerun with --from-release (omit --binary)"
test_J5_StaleBinaryFromRelease() {
    local h; h="$(new_home)"
    j5_stale "$h" || return
    FAKE_CURL_API_TAG="$REL_TAG"
    run_in "$h" "$TREE" bash "$TREE_SH" --no-hooks --no-symlink --from-release
    expect_rc 0 "rerun with --from-release, no --binary" && expect_installed "$h" "$BIN" "$ADMIN"
}

# j5_worktree <worktree> <what> [<skill dir>]: J5 from a git worktree of TREE,
# with the in-repo build (b.go9), running install.sh from skill dir (default
# the worktree's own). A stale pair in its bin/ is refused, naming the
# worktree's own HEAD and physical path (b.1rs); after the advised make build
# there, the same command installs the rebuilt pair.
j5_worktree() {
    local wt="$1" what="$2" h head cmd root
    local -a argv=(bash "${3:-$wt/skills/install-agent-director}/install.sh" --no-hooks --no-symlink)
    h="$(new_home)"
    head="$(git -C "$wt" rev-parse HEAD)" || { bad "$what: git rev-parse HEAD"; return; }
    root="$(cd -P "$wt" && pwd -P)" || { bad "$what: resolve $wt"; return; }
    mkdir -p "$wt/bin" && cp "$BIN" "$wt/bin/agent-director" && cp "$ADMIN" "$wt/bin/agent-director-admin" \
        || { bad "$what: put the stale pair in bin/"; return; }
    run_in "$h" "$wt" "${argv[@]}"
    expect_rc 3 "$what: stale binary" || return
    grep -qxF "install.sh: source-tree version check failed." "$ERR" || bad "$what: no check-failed line: $(flat "$ERR")"
    grep -qxF "  HEAD    : $head ($root)" "$ERR" \
        || bad "$what: \"$(grep -m1 '^  HEAD    : ' "$ERR")\"; want \"  HEAD    : $head ($root)\""
    expect_nothing_installed "$h"
    cmd="$(advice_after "rebuild it first:")" || { bad "$what: no advised command"; return; }
    run_advised "$h" "$wt" "$cmd"
    expect_rc 0 "$what: advised: $cmd" || return
    run_in "$h" "$wt" "${argv[@]}"
    expect_rc 0 "$what: re-run after make build" || return
    grep -qxF "  version-check: binary commit matches HEAD ($head)" "$OUT" \
        || bad "$what: no version-check line for $head: $(grep -F 'version-check' "$OUT")"
    expect_installed "$h" "$wt/bin/agent-director" "$wt/bin/agent-director-admin"
}

# J5: "rebuild it first: make build", in a linked worktree outside any other
# checkout, whose .git is a file (b.go9): the stale pair is refused, not skipped.
test_J5_LinkedWorktreeMakeBuild() {
    local wt="$ROOT/tree-linked"
    git -C "$TREE" worktree add -q "$wt" || { bad "git worktree add $wt"; return; }
    j5_worktree "$wt" "linked worktree"
}

# J5: "rebuild it first: make build", in a worktree nested inside TREE and one
# commit ahead of it (b.go9): the check uses the worktree's HEAD, not TREE's.
test_J5_NestedWorktreeMakeBuild() {
    local wt="$TREE/.claude/worktrees/nested"
    git -C "$TREE" worktree add -q "$wt" \
        && git -C "$wt" -c user.name=advice -c user.email=advice@example.invalid -c commit.gpgsign=false \
            commit -q --allow-empty -m nested || { bad "nested worktree $wt"; return; }
    j5_worktree "$wt" "nested worktree"
}

# J5: "rebuild it first: make build", through a symlink to a worktree's skill
# directory (b.1rs): the check uses the checkout install.sh takes bin/ from,
# though no checkout encloses the link.
test_J5_SymlinkedSkillMakeBuild() {
    local wt="$ROOT/tree-symlinked" link="$ROOT/j5-link/skills/install-agent-director"
    git -C "$TREE" worktree add -q "$wt" && mkdir -p "${link%/*}" \
        && ln -s "$wt/skills/install-agent-director" "$link" || { bad "worktree $wt linked at $link"; return; }
    j5_worktree "$wt" "symlinked skill" "$link"
}

# j5_dotfiles <home> <layout>: a dotfiles repo with one commit and a copy of
# install.sh in it, whose path it prints. Layouts: home (~ is the repo, the
# installed skill under ~/.claude), claude (~/.claude is), claude-worktree
# (~/.claude is a linked worktree of a repo outside ~, its .git a file),
# source (~ is, and holds an agent-director tree ~/src/ad with no .git),
# source-empty-git (as source, ~/src/ad/.git an empty directory, not a repo).
j5_dotfiles() {
    local h="$1" repo="$1" skill="$1/.claude/skills/install-agent-director"
    case "$2" in
        claude) repo="$h/.claude" ;;
        claude-worktree) repo="$h.dotfiles" ;;
        source*) skill="$h/src/ad/skills/install-agent-director" ;;
    esac
    mkdir -p "$repo" && git -C "$repo" init -q \
        && git -C "$repo" -c user.name=advice -c user.email=advice@example.invalid -c commit.gpgsign=false \
            commit -q --allow-empty -m dotfiles || return 1
    case "$2" in
        claude-worktree) git -C "$repo" worktree add -q --detach "$h/.claude" >/dev/null || return 1 ;;
        source) mkdir -p "$h/src/ad/cmd/agent-director" || return 1 ;;
        source-empty-git) mkdir -p "$h/src/ad/cmd/agent-director" "$h/src/ad/.git" || return 1 ;;
    esac
    install_copy "$skill/install.sh"
    printf '%s' "$skill/install.sh"
}

# J5 does not come from a copy of install.sh in a dotfiles repo (b.1rs): not
# the installed skill, and not an agent-director tree in it whose own .git is
# missing or not a repo. Neither is a checkout of agent-director's source, so a
# pair the repo's HEAD did not build, given as --binary and --admin-binary,
# installs.
test_J5_DotfilesSkillNotChecked() {
    local layout h sh
    for layout in home claude claude-worktree source source-empty-git; do
        h="$(new_home)"
        sh="$(j5_dotfiles "$h" "$layout")" || { bad "$layout: dotfiles repo"; continue; }
        run "$h" bash "$sh" --binary "$BIN" --admin-binary "$ADMIN" --no-hooks --no-symlink
        expect_rc 0 "$layout: --binary from $sh" || continue
        expect_installed "$h" "$BIN" "$ADMIN"
    done
}

# ---- J6: store open failed after install ------------------------------------------

# j6_break_hop <state.db>: make the newest migration step, v5→v6
# (migrateV5toV6, b.kdf), fail part-way on a store stamped v5 that keeps its
# v6 columns: launch_owner_pidns is re-added as LAUNCH_OWNER_PIDNS, a name
# SQLite's columns treat as the same but the step's probe does not, so the
# step's ADD COLUMN launch_owner_pidns fails as a duplicate and rolls the step
# back. j6_repair_hop removes that cause. Both name the v5→v6 step: give them
# the next step's failure when the schema moves on.
j6_break_hop() {
    [[ "$SCHEMA" == 6 ]] || { bad "j6_break_hop breaks the v5→v6 step; update it for v$SCHEMA"; return 1; }
    "$SQLITE" "$1" "ALTER TABLE spawns DROP COLUMN launch_owner_pidns;
        ALTER TABLE spawns ADD COLUMN LAUNCH_OWNER_PIDNS TEXT;"
}
j6_repair_hop() { "$SQLITE" "$1" "ALTER TABLE spawns DROP COLUMN LAUNCH_OWNER_PIDNS;"; }

# j6_failing_migration [pre-0.11.0]: install BIN_OLD and ADMIN_OLD (with
# pre-0.11.0, then make that an install from before 0.11.0: j11_pre_admin),
# make the store one version older with a column the migration step cannot
# add (j6_break_hop), and run J6ARGV, an upgrade with --keep-prior, into that
# failure; leaves HOME in J6H.
j6_failing_migration() {
    J6H="$(new_home)"
    run "$J6H" bash "$LOOSE" --binary "$BIN_OLD" --admin-binary "$ADMIN_OLD" --no-hooks --no-symlink
    expect_rc 0 "first install" || return 1
    if [[ "${1:-}" == pre-0.11.0 ]]; then j11_pre_admin "$J6H" || return 1; fi
    "$SQLITE" "$J6H/.agent-director/state.db" "PRAGMA user_version = $((SCHEMA - 1));" || { bad "damage store"; return 1; }
    j6_break_hop "$J6H/.agent-director/state.db" || { bad "damage store"; return 1; }
    J6ARGV=(bash "$LOOSE" --binary "$BIN" --admin-binary "$ADMIN" --keep-prior --no-hooks --no-symlink)
    run "$J6H" "${J6ARGV[@]}"
    expect_exit5 ErrStoreOpen "migration step fails" || return 1
    expect_advice "If this install authorized a migration above, it was NOT consumed; re-running this install will retry it."
    [[ -f "$(sentinel "$J6H")" ]] || bad "the authorized migration's sentinel is gone after the failed open"
}

# J6: "If this install authorized a migration above, it was NOT consumed;
# re-running this install will retry it." The re-run's step-3 probe fails too, and says it could
# not tell, not that no migration is needed (b.7b4). Once the cause is gone, the
# re-run's probe runs the migration the left sentinel authorizes: step 3 says
# so, and step 5 verifies it (b.dzw).
test_J6_MigrationFailedRerunRetries() {
    j6_failing_migration || return
    run "$J6H" "${J6ARGV[@]}" # the cause still holds: the same refusal
    expect_exit5 ErrStoreOpen "re-run while the step's cause still holds" || return
    expect_advice "re-running this install will retry it."
    local could="  schema  : state.db at v$((SCHEMA - 1)); could not tell whether a migration is needed (agent-director list failed: ErrStoreOpen)"
    grep -qxF "$could" "$OUT" || bad "no \"$could\" line: $(grep -F "  schema  : " "$OUT")"
    [[ "$(db_version "$J6H")" == $((SCHEMA - 1)) ]] || bad "store moved off v$((SCHEMA - 1)) on a failed re-run"
    j6_repair_hop "$J6H/.agent-director/state.db" || { bad "repair store"; return; }
    run "$J6H" "${J6ARGV[@]}"
    expect_rc 0 "re-run once the cause is gone" || return
    [[ "$(db_version "$J6H")" == "$SCHEMA" ]] || bad "store at v$(db_version "$J6H"); want v$SCHEMA"
    [[ ! -e "$(sentinel "$J6H")" ]] || bad "sentinel not consumed by the successful migration"
    local ran="  schema  : migration v$((SCHEMA - 1))→v$SCHEMA ran at the probe (agent-director list), authorized by a sentinel written before this install (sentinel $(sentinel "$J6H"))"
    grep -qxF "$ran" "$OUT" || bad "no \"$ran\" line: $(grep -F "  schema  : " "$OUT")"
    grep -qF "no migration authorization needed" "$OUT" && bad "step 3 says no migration was needed after its probe ran one"
    grep -qxF "  schema  : migration verified — state.db now at v$SCHEMA" "$OUT" \
        || bad "step 5 did not verify the migration the probe ran: $(flat "$OUT")"
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
    expect_exit5 ErrStoreOpen "advised re-run while the step's cause still holds" || return
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
    expect_exit5 ErrStoreOpen "advised re-run while the step's cause still holds" || return
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
    expect_exit5 ErrSchemaMismatch "store newer than the binary" || return
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
# J7FULL: J7ARGV with the steps after step 5 that it skips run too: hook
# injection, the config.toml merge and MCP registration. A test makes it its
# J7ARGV with `local -a J7ARGV=("${J7FULL[@]}")`.
J7FULL=(bash "$LOOSE" --binary "$BIN" --admin-binary "$ADMIN" --no-symlink --register-mcp)

# j7_config: when J7_BUSY_MS is set, J7H's config.toml sets [store]
# busy_timeout_ms to it, the wait the reads and the check commands install.sh
# prints use (b.c7f).
j7_config() {
    [[ -n "${J7_BUSY_MS:-}" ]] || return 0
    mkdir -p "$J7H/.agent-director" && printf '[store]\nbusy_timeout_ms = %s\n' "$J7_BUSY_MS" >"$J7H/.agent-director/config.toml"
}

# j7_timeout: the busy timeout install.sh's check commands wait (.timeout):
# J7_BUSY_MS, or the default.
j7_timeout() { printf '%s' "${J7_BUSY_MS:-10000}"; }

# j7_installed: a new HOME in J7H (with j7_config's config) with J7ARGV installed.
j7_installed() {
    J7H="$(new_home)"
    j7_config
    run "$J7H" "${J7ARGV[@]}"
    expect_rc 0 "first install"
}

# j7_older_store: j7_installed, then the store set one version back, so the
# next install migrates it.
j7_older_store() {
    j7_installed || return 1
    "$SQLITE" "$J7H/.agent-director/state.db" "PRAGMA user_version = $((SCHEMA - 1));"
}

# j7_run <call> [<answer> [<stderr> [<status>]]]: run J7ARGV in J7H with
# sqlite3 call <call> (1 is step 2's read of an existing store, the next step
# 5's; 0 none) printing <stderr> on stderr, then <answer>, and exiting <status>
# (default 0). Given no <stderr> and an empty or no <answer>, it fails as when a
# lock outlasts its busy timeout.
j7_run() {
    ln -sf "$SQLITE_SHIM" "$TOOLBOX/sqlite3"
    FAKE_SQLITE3_FAIL_CALL="$1" FAKE_SQLITE3_ANSWER="${2:-}" FAKE_SQLITE3_ERR="" FAKE_SQLITE3_RC=0
    if [[ $# -ge 3 ]]; then
        FAKE_SQLITE3_ERR="$3" FAKE_SQLITE3_RC="${4:-0}"
    elif [[ -z "$FAKE_SQLITE3_ANSWER" ]]; then
        FAKE_SQLITE3_ERR="$SHIM_LOCK_ERR" FAKE_SQLITE3_RC=5
    fi
    rm -f "$J7H.sqlite3-calls" # count from 1
    run "$J7H" "${J7ARGV[@]}"
    ln -sf "$SQLITE" "$TOOLBOX/sqlite3"
    FAKE_SQLITE3_FAIL_CALL=0 FAKE_SQLITE3_ANSWER="" FAKE_SQLITE3_ERR="" FAKE_SQLITE3_RC=0
}

# j7_verify_fails <version>: an older store, and an install whose verification
# read answers <version>: exit 5, step 5's failure, ErrSchemaVerifyFailed.
j7_verify_fails() {
    j7_older_store || return 1
    j7_run 2 "$1"
    expect_exit5 ErrSchemaVerifyFailed "verification read answered \"$1\"" || return 1
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

# j7_warned <next>: step 5's warning for a read that gave no version with no
# migration expected, its advice word for word, <next> (as for j7_unreadable)
# the line after "<unreadable>", no failure wording, stdout's state.db line at
# <unreadable>, and the install carried on past step 5: hooks, this run's
# config.toml merge and MCP registration done (J7FULL) and the install's last
# line printed (b.xd9).
j7_warned() {
    local want="install.sh: warning: state.db's schema version is unreadable after the store open" db line
    db="$(printf %q "$J7H/.agent-director/state.db")"
    [[ "$(head -n 1 "$ERR")" == "$want" ]] || bad "first stderr line \"$(head -n 1 "$ERR")\"; want \"$want\""
    j7_unreadable "The store open (agent-director list) succeeded and no migration was authorized, so the install carries on. Check the version later with: sqlite3 -batch -init /dev/null -cmd \".timeout $(j7_timeout)\" $db \"PRAGMA user_version;\"" "$1"
    if grep -qiE 'FAILED|could not|re-run' "$ERR"; then
        bad "the warning speaks as if the install failed: $(flat "$ERR")"
    fi
    j7_schema_unreadable
    for line in "  hooks   : injected into $J7H/.claude/settings.json" "  mcp     : registered with claude mcp" \
        "install.sh: done. Try: $J7H/.agent-director/bin/agent-director help"; do
        grep -qxF "$line" "$OUT" || bad "no \"$line\" line: the install stopped at step 5: $(flat "$OUT")"
    done
    grep -qE '^  config  : (merged|created) ' "$OUT" \
        || bad "no \"config  : merged/created\" line: this run skipped the config merge: $(flat "$OUT")"
    expect_installed "$J7H" "$BIN" "$ADMIN"
}

# j7_mismatch: j7_verify_fails with the verification read answering the
# pre-migration version; checks its advice (state.db's path shell-quoted).
j7_mismatch() {
    local t="$SCHEMA" a="$((SCHEMA - 1))" db
    j7_verify_fails "$a" || return 1
    db="$(printf %q "$J7H/.agent-director/state.db")"
    expect_advice "actual user_version: $a"
    expect_advice "The store open (agent-director list) succeeded, and a successful open leaves state.db at v$t: any authorized migration has run, and its sentinel is consumed. Yet the read after the open gives v$a: state.db changed after the open, or the read is wrong. Check its version now: sqlite3 -batch -init /dev/null -cmd \".timeout $(j7_timeout)\" $db \"PRAGMA user_version;\" A re-run of this install reads the version again: below v$t it brings state.db to v$t again, above v$t it stops at the store open (ErrSchemaMismatch), and at v$t it finishes the install. If a re-run fails this same way, contact the maintainers."
}

# j7_rerun_verified: re-run J7ARGV with the real sqlite3; it reads and verifies
# the migrated store.
j7_rerun_verified() {
    run "$J7H" "${J7ARGV[@]}"
    expect_rc 0 "$1" || return
    grep -qF "(schema v$SCHEMA)" "$OUT" || bad "the re-run did not read the store's version: $(flat "$OUT")"
    [[ "$(db_version "$J7H")" == "$SCHEMA" ]] || bad "store at v$(db_version "$J7H"); want v$SCHEMA"
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
            expect_exit5 ErrSchemaMismatch "v$version: re-run" || continue
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

# J7: "a successful open leaves state.db at v<T>: any authorized migration has
# run, and its sentinel is consumed. ... Check its version now:
# <command>" (a readable user_version != target, b.wt9): run the command as
# printed, under a plain HOME and one whose path holds shell characters, and
# with [store] busy_timeout_ms set, whose wait the command takes (b.c7f).
test_J7_VersionMismatchCheckVersion() {
    local HOME_TAG J7_BUSY_MS spec cmd
    for spec in "|" "\$x\`y\`'q\"z.|" "|1234"; do
        IFS='|' read -r HOME_TAG J7_BUSY_MS <<<"$spec"
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

# J7: "Check the version later with: <command>" when step 5's read of a fresh
# or already-current store, no migration expected, gives no version: it fails
# (a lock outlasting its busy timeout) or prints no whole number (JSON, b.hk7).
# The install warns and carries on (b.xd9). Once the read works, the command
# as printed prints the store's version: each case under a plain HOME, and a
# fresh store's failed read under one whose path holds shell characters (the
# quoting depends on neither the store nor the read's output), and with
# [store] busy_timeout_ms set, whose wait the command takes (b.c7f). A current
# store's JSON read: test_J7_NotAVersionAfterOpenRerun's re-run.
test_J7_UnreadableAfterOpenCheckVersion() {
    local -a J7ARGV=("${J7FULL[@]}") # the steps after step 5 run too
    local HOME_TAG J7_BUSY_MS spec store answer call what cmd json="[{\"user_version\":$SCHEMA}]"
    for spec in "|fresh||" "|current||" "|fresh|$json|" "\$x\`y\`'q\"z.|fresh||" "|fresh||1234" "|current||2500"; do
        IFS='|' read -r HOME_TAG store answer J7_BUSY_MS <<<"$spec"
        case "$store" in
            fresh) J7H="$(new_home)" call=1; j7_config ;; # no state.db: step 5's read is the first
            current) j7_installed || continue; call=2 ;;
        esac
        what="$store store, step 5's read failed"
        [[ -n "$answer" ]] && what="$store store, step 5's read printed \"$answer\""
        j7_run "$call" "$answer"
        expect_rc 0 "$what" || continue
        j7_warned "    ${answer:-$SHIM_LOCK_ERR}"
        cmd="$(line_after "Check the version later with:")"
        [[ -n "$cmd" ]] || { bad "HOME $J7H, $what: no advised check command"; continue; }
        run "$J7H" bash -c "$cmd"
        expect_rc 0 "HOME $J7H, $what: advised: $cmd" || continue
        [[ "$(cat "$OUT")" == "$SCHEMA" ]] || bad "HOME $J7H, $what: the advised check prints \"$(cat "$OUT")\"; want $SCHEMA (state.db at v$SCHEMA)"
    done
}

# J7: "Re-running this install retries the read." when step 2's or step 5's
# read fails: it exits nonzero (under a lock, or after printing a version, which
# then counts for nothing, alone or after an error) or exits 0 printing nothing.
# The report shows all the read printed, in order, and no pointer to a human
# (time may resolve a lock, b.ady). A failed step-2 read names state.db, does not
# take it for a fresh create and authorizes nothing, the store left as it was
# (b.n5a). TMPDIR is full throughout: the reads need no temp file, so mktemp is
# never asked for one there, sqlite3's error is still shown, and the re-run
# verifies the store (b.wfe, b.rfn). Per case <call>|<stdout>|<stderr>|<status>.
test_J7_FailedReadRerun() {
    local call out err rc what want could printed
    while IFS='|' read -r call out err rc <&3; do
        what="read $call (stdout \"$out\", stderr \"$err\", exit $rc)"
        PATH_EXTRA=""
        j7_older_store || continue
        PATH_EXTRA="$MKTEMP_FAILS"
        rm -f "$MKTEMP_REFUSALS"
        j7_run "$call" "$out" "$err" "$rc"
        expect_exit5 ErrVersionUnreadable "$what" || continue
        want="install.sh: reading state.db's schema version FAILED"
        could="tell whether state.db needs a migration. No migration was authorized."
        if [[ "$call" == 2 ]]; then
            want="install.sh: schema migration verification FAILED" could="check the migration."
            j7_schema_unreadable
        else
            grep -qxF "  state.db: $J7H/.agent-director/state.db" "$ERR" || bad "$what: the failure does not name state.db: $(flat "$ERR")"
            if grep -qF "no existing state.db" "$OUT"; then
                bad "$what: an existing state.db reported as a fresh create: $(flat "$OUT")"
            fi
            [[ ! -e "$(sentinel "$J7H")" ]] || bad "$what: a migration was authorized without the store's version"
            [[ "$(db_version "$J7H")" == $((SCHEMA - 1)) ]] || bad "$what: store moved off v$((SCHEMA - 1)): v$(db_version "$J7H")"
        fi
        [[ "$(head -n 1 "$ERR")" == "$want" ]] || bad "$what: first stderr line \"$(head -n 1 "$ERR")\"; want \"$want\""
        grep -qxF "  actual   user_version: <unreadable>" "$ERR" || bad "$what: no <unreadable> line: $(flat "$ERR")"
        printed="$({ [[ -z "$err" ]] || echo "$err"; [[ -z "$out" ]] || echo "$out"; } | sed 's/^/    /')"
        [[ "$(j7_shown)" == "$printed" ]] \
            || bad "$what: the report shows \"$(j7_shown)\" under \"<unreadable>\"; want all the read printed, \"$printed\""
        expect_advice "Reading state.db's user_version (sqlite3 PRAGMA user_version) failed, so the install could not $could Re-running this install retries the read."
        if grep -qF "contact the maintainers" "$ERR"; then
            bad "$what: the failure tells the operator to contact the maintainers"
        fi
        j7_rerun_verified "$what: re-run, TMPDIR still full" || continue
        if [[ "$call" == 1 ]]; then
            grep -qxF "  schema  : migration verified — state.db now at v$SCHEMA" "$OUT" \
                || bad "$what: the re-run did not verify the migration: $(flat "$OUT")"
        fi
        if [[ -s "$MKTEMP_REFUSALS" ]]; then
            bad "$what: install.sh asked mktemp for a temp file in TMPDIR: $(paste -sd' ' "$MKTEMP_REFUSALS")"
        fi
    done 3<<EOF
1||$SHIM_LOCK_ERR|5
1|$((SCHEMA - 1))||10
1|$((SCHEMA - 1))|Error: stepping, disk I/O error (10)|10
2||$SHIM_LOCK_ERR|5
2|$SCHEMA||10
2|||0
EOF
}

# j7_shown: the lines the report (a failure's, or step 5's warning) shows under
# "<unreadable>": all the read printed, stderr and stdout as they came.
j7_shown() {
    awk '/^  (Reading state\.db|The store open)/ { f = 0 } f; $0 == "  actual   user_version: <unreadable>" { f = 1 }' "$ERR"
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

# j7_rerun_same <want> <call> [<answer> [<stderr> [<status>]]]: re-run j7_run
# with the arguments after <want>, those the last j7_run had, so with that
# sqlite3 and state.db: exit 0 when <want> is 0, else exit 5 with cause <want>,
# showing the same output.
j7_rerun_same() {
    local want="$1" shown what="re-run with that sqlite3 and state.db unchanged"
    shown="$(j7_shown)"
    shift
    j7_run "$@"
    if [[ "$want" == 0 ]]; then
        expect_rc 0 "$what" || return 1
    else
        expect_exit5 "$want" "$what" || return 1
    fi
    [[ "$(j7_shown)" == "$shown" ]] || bad "the re-run shows \"$(j7_shown)\" under \"<unreadable>\"; the first run showed \"$shown\""
}

# J7: "A re-run gets the same output unless that sqlite3 or state.db changes."
# when step 2's read prints no whole number: a sqlite3 printing a header line
# (as a .headers on ~/.sqliterc does) or a leading zero (08, which printf %d
# rejects as octal; 010, which it reads as 8), one exiting 0 that prints a
# notice on stderr before the version (as -init does without -batch; it joins
# the output, b.rfn), or a store at user_version -1. Nothing is authorized;
# once the one named changes, the re-run migrates (b.hk7). Per case
# <changed>|<stdout>|<stderr>.
test_J7_NotAVersionBeforeOpenRerun() {
    local spec changed call answer err printed stand_in version want="install.sh: reading state.db's schema version FAILED"
    for spec in "sqlite3|user_version\n$((SCHEMA - 1))|" "sqlite3|08|" "sqlite3|010|" \
        "sqlite3|$((SCHEMA - 1))|-- Loading resources from /dev/null" "state.db|-1|"; do
        IFS='|' read -r changed answer err <<<"$spec"
        printed="${err:+$err\n}$answer" # stderr first, as the stand-in prints them
        j7_older_store || continue
        if [[ "$changed" == sqlite3 ]]; then
            version=$((SCHEMA - 1)) call=1 stand_in="$answer"
        else
            version="$answer" call=0 stand_in=""
            "$SQLITE" "$J7H/.agent-director/state.db" "PRAGMA user_version = $version;"
        fi
        j7_run "$call" "$stand_in" "$err" 0
        expect_exit5 ErrVersionUnreadable "step 2's read printed \"$printed\"" || continue
        [[ "$(head -n 1 "$ERR")" == "$want" ]] || bad "\"$printed\": first stderr line \"$(head -n 1 "$ERR")\"; want \"$want\""
        j7_not_a_version "$printed" "Reading state.db's user_version (sqlite3 PRAGMA user_version) printed the output above, not a whole number (0 or more), so the install could not tell whether state.db needs a migration. No migration was authorized."
        j7_rerun_same ErrVersionUnreadable "$call" "$stand_in" "$err" 0 || continue
        [[ "$(db_version "$J7H")" == "$version" ]] || bad "\"$printed\": store moved off v$version: v$(db_version "$J7H")"
        if [[ "$changed" == state.db ]]; then
            "$SQLITE" "$J7H/.agent-director/state.db" "PRAGMA user_version = $((SCHEMA - 1));"
        fi
        j7_rerun_verified "\"$printed\": re-run once $changed changed"
    done
}

# J7: "A re-run gets the same output unless that sqlite3 or state.db changes."
# when step 5's read prints no whole number (a sqlite3 printing JSON, as a
# .mode json ~/.sqliterc does, or the target version with a leading zero,
# which the version compare holds different from the target) after an open
# that migrated an older store. That open left state.db at the target, so the
# re-run expects no migration: it shows the same output in step 5's warning
# and finishes the install (b.xd9). The re-run with the real sqlite3 at that
# path verifies it (b.hk7). Fresh and current stores:
# test_J7_UnreadableAfterOpenCheckVersion.
test_J7_NotAVersionAfterOpenRerun() {
    local -a J7ARGV=("${J7FULL[@]}") # the re-run's steps after step 5 run too
    local answer want="install.sh: schema migration verification FAILED"
    for answer in "[{\"user_version\":$SCHEMA}]" "0$SCHEMA"; do
        j7_older_store || continue
        j7_run 2 "$answer"
        expect_exit5 ErrVersionUnreadable "older store, step 5's read printed \"$answer\"" || continue
        [[ "$(head -n 1 "$ERR")" == "$want" ]] || bad "\"$answer\": first stderr line \"$(head -n 1 "$ERR")\"; want \"$want\""
        j7_schema_unreadable
        j7_not_a_version "$answer" "Reading state.db's user_version (sqlite3 PRAGMA user_version) printed the output above, not a whole number (0 or more), so the install could not check the migration."
        j7_rerun_same 0 2 "$answer" || continue # step 2's read is the first
        j7_warned "    $answer"
        j7_rerun_verified "\"$answer\", re-run with the real sqlite3"
    done
}

# J7: "Re-running this install retries the read." and "A re-run gets the same
# output unless that sqlite3 or state.db changes." when step 3's read after a
# probe that a sentinel left from before let migrate (the J6 re-run, cause
# gone) gives no version: exit 5 with no step-3 verdict. The probe consumed the
# sentinel, so the re-run finds the store current: a failed read's re-run
# verifies it, a not-a-number read's shows that output in step 5's warning
# until the sqlite3 changes (b.dzw).
test_J7_UnreadableAfterProbeRerun() {
    local -a J7ARGV
    local answer could="tell whether the probe (agent-director list) ran a migration. A sentinel written before this install was beside state.db, and it may have authorized one."
    for answer in "" "[{\"user_version\":$SCHEMA}]"; do
        j6_failing_migration || continue
        j6_repair_hop "$J6H/.agent-director/state.db" || { bad "repair store"; continue; }
        J7H="$J6H"
        J7ARGV=("${J6ARGV[@]}")
        j7_run 2 "$answer" # step 2's read, then step 3's after the probe
        expect_exit5 ErrVersionUnreadable "step 3's read after the probe answered \"$answer\"" || continue
        [[ "$(head -n 1 "$ERR")" == "install.sh: reading state.db's schema version FAILED" ]] \
            || bad "\"$answer\": first stderr line \"$(head -n 1 "$ERR")\""
        grep -qxF "  state.db: $J7H/.agent-director/state.db" "$ERR" || bad "\"$answer\": the failure does not name state.db: $(flat "$ERR")"
        if grep -qF "  schema  : " "$OUT"; then
            bad "\"$answer\": a step-3 verdict after its read gave no version: $(grep -F "  schema  : " "$OUT")"
        fi
        if [[ -z "$answer" ]]; then
            j7_unreadable "Reading state.db's user_version (sqlite3 PRAGMA user_version) failed, so the install could not $could Re-running this install retries the read."
            j7_rerun_verified "re-run once the read works"
        else
            j7_not_a_version "$answer" "Reading state.db's user_version (sqlite3 PRAGMA user_version) printed the output above, not a whole number (0 or more), so the install could not $could"
            j7_rerun_same 0 2 "$answer" || continue # no sentinel now: step 5's read is the second
            j7_rerun_verified "\"$answer\", re-run with the real sqlite3"
        fi
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

# j9_installed_mismatch <how>: j3_installed_skill with BIN_NUL and ADMIN_NUL
# (BIN and ADMIN's stamps, other bytes), then a re-run of J3SK, J9ARGV, that
# pairs the installed agent-director-admin with BIN_OLD, from another build,
# found on PATH (<how> PATH) or given as --binary (<how> --binary) (b.azo):
# check it is refused, naming both stamps, with nothing changed on disk.
j9_installed_mismatch() {
    local src="$ON_PATH/agent-director" before
    j3_installed_skill "$BIN_NUL" "$ADMIN_NUL" || return 1
    J9ARGV=(bash "$J3SK")
    if [[ "$1" == PATH ]]; then PATH_EXTRA="$ON_PATH:$PATH_EXTRA"; else J9ARGV+=(--binary "$BIN_OLD") src="$BIN_OLD"; fi
    before="$(j14_snap "$J3H")"
    run "$J3H" "${J9ARGV[@]}"
    expect_rc 3 "$1: agent-director from another build" || return 1
    expect_advice "install.sh: agent-director and agent-director-admin version stamps differ; refusing to install."
    expect_advice "agent-director : $src (0.0.1-advice-old $OLD_COMMIT)"
    expect_advice "agent-director-admin: $J11A (0.0.2-advice $CUR_COMMIT)"
    [[ "$(j14_snap "$J3H")" == "$before" ]] || bad "$1: the refusal changed $J3H: $(diff <(echo "$before") <(j14_snap "$J3H"))"
}

# J9: "or download release: rerun with --from-release (omit --binary and
# --admin-binary)" in j9_installed_mismatch's refusal: the advised re-run
# installs the release's pair over the installed one.
test_J9_InstalledAdminStampMismatchFromRelease() {
    local how
    for how in PATH --binary; do
        j9_installed_mismatch "$how" || continue
        expect_advice "or download release: rerun with --from-release (omit --binary and --admin-binary)"
        FAKE_CURL_API_TAG="$REL_TAG"
        run "$J3H" bash "$J3SK" --from-release
        FAKE_CURL_API_TAG=""
        expect_rc 0 "$how: rerun with --from-release, no --binary" && expect_installed "$J3H" "$BIN" "$ADMIN"
    done
}

# J9: "rebuild both first:  make build" in j9_installed_mismatch's refusal,
# run where install.sh ran, outside any checkout; the same command then
# installs (a pair: install.sh refuses any other). Known broken: there is no
# checkout to build in, and the re-run would pair the same two binaries (b.oo9).
test_J9_InstalledAdminStampMismatchMakeBuild() {
    local how cmd
    for how in PATH --binary; do
        j9_installed_mismatch "$how" || continue
        expect_advice "rebuild both first: make build"
        cmd="$(advice_after "rebuild both first:")" || { bad "$how: no advised command"; continue; }
        known_broken J9 "filed as b.oo9: no checkout to run \"make build\" in, re-run of the installed skill" || return
        run_advised "$J3H" "$ROOT" "$cmd"
        expect_rc 0 "$how: advised: $cmd" || continue
        run "$J3H" "${J9ARGV[@]}"
        expect_rc 0 "$how: re-run after make build"
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
# migrating an older store. The syntax error is a bad value, which install.sh's
# own [store] db_path reader passes over (b.2io), so the binary's refusal is the
# one reached; so is a [store] busy_timeout_ms the binary refuses, which
# install.sh's own reader reads as the default (b.c7f).
test_J13_ConfigRefusedFixAndRerun() {
    local spec store config named fix before path key gone
    local want="install.sh: agent-director refused its config file (ErrConfigMalformed)"
    local none="  schema  : state.db at v$SCHEMA; no migration authorization needed"
    for spec in \
        "fresh|[defaults]\nexpire_retention_days = -1|[defaults] expire_retention_days = -1|remove" \
        "older|[defaults]\nexpire_retention_days = -1|[defaults] expire_retention_days = -1|zero" \
        "current|[tmux]\nquery_timeout_ms = -5|[tmux] query_timeout_ms = -5|zero" \
        "older|[store]\nbusy_timeout_ms = -5|[store] busy_timeout_ms = -5|zero" \
        "fresh|[store]\nbusy_timeout_ms = 2147483648|[store] busy_timeout_ms = 2147483648|remove" \
        "older|[defaults]\nrelay_mode = off|toml: line 2 (last key|syntax"; do
        IFS='|' read -r store config named fix <<<"$spec"
        case "$store" in
            fresh) J7H="$(new_home)" before=none; mkdir -p "$J7H/.agent-director" ;;
            older) j7_older_store || continue; before=$((SCHEMA - 1)) ;;
            current) j7_installed || continue; before="$SCHEMA" ;;
        esac
        printf '%b\n' "$config" >"$J7H/.agent-director/config.toml"
        run "$J7H" "${J7ARGV[@]}"
        expect_exit5 ErrConfigMalformed "$store store, config \"$named\"" || continue
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
            syntax) sed -i 's/^relay_mode = off$/relay_mode = "off"/' "$path" ;;
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

# ---- J14: [store] db_path install.sh cannot read (b.2io) ---------------------------

# j14_snap <home>: every path under home with its type, mode, size and mtime,
# and every file's sha256.
j14_snap() {
    (cd "$1" && find . -printf '%p %y %m %s %T@\n' | sort && find . -type f -exec sha256sum {} + | sort)
}

# j14_config <home> <config> <fixed>: write home's config.toml: <config>
# through printf %b, or a directory for <directory>, or for <unreadable> <fixed>
# with no read permission.
j14_config() {
    local cfg="$1/.agent-director/config.toml"
    case "$2" in
        '<directory>') mkdir "$cfg" ;;
        '<unreadable>') printf '%b\n' "$3" >"$cfg" && chmod 000 "$cfg" ;;
        *) printf '%b\n' "$2" >"$cfg" ;;
    esac
}

# keep_older <db>: mark the store at <db>, so a later check can tell it is the
# same one, and set it one version back.
keep_older() {
    "$SQLITE" "$1" "CREATE TABLE kept_store (x); PRAGMA user_version = $((SCHEMA - 1));" || bad "mark the store at $1"
}
# is_kept <db>: 1 when the store at <db> is one keep_older marked, else 0.
is_kept() { "$SQLITE" "$1" "SELECT count(*) FROM sqlite_master WHERE name = 'kept_store';"; }

# expect_store <home> <db> <context> [migrated]: after a successful install, the
# store at <db> is at v$SCHEMA and the only one under <home>, with no sentinel
# left beside it or in ~/.agent-director; with "migrated", it is the one
# keep_older marked, migrated in place by a migration the run authorized beside it.
expect_store() {
    local h="$1" db="$2" ctx="$3" dir="${2%/*}"
    [[ "$("$SQLITE" "$db" 'PRAGMA user_version;')" == "$SCHEMA" ]] || bad "$ctx: no store at v$SCHEMA at $db"
    [[ "$(find "$h" -type f -name '*.db')" == "$db" ]] || bad "$ctx: stores under HOME: $(find "$h" -type f -name '*.db' | tr '\n' ' '); want only $db"
    if compgen -G "$dir/migrate-authorized*" >/dev/null || compgen -G "$h/.agent-director/migrate-authorized*" >/dev/null; then
        bad "$ctx: sentinel left: $(compgen -G "$dir/migrate-authorized*"; compgen -G "$h/.agent-director/migrate-authorized*")"
    fi
    [[ "${4:-}" == migrated ]] || return 0
    [[ "$(is_kept "$db")" == 1 ]] || bad "$ctx: the store at $db is not the one there before"
    grep -qxF "  schema  : authorized migration v$((SCHEMA - 1))→v$SCHEMA (sentinel $dir/migrate-authorized)" "$OUT" \
        || bad "$ctx: the run did not authorize the migration beside $db: $(flat "$OUT")"
}

# J14: each reason install.sh gives for a config.toml its [store] db_path
# reader cannot read, word for word, then "Nothing was installed or changed.
# Re-run this install after the change.": the refusal changes nothing under
# HOME; making the change the reason names (one row per alternative it offers)
# and re-running installs, with the store at <db> and no other. A row's store:
#   fresh       none before the refusal;
#   older       one version back, installed under the changed config (for a
#               config agent-director refuses);
#   kept        the store agent-director itself made under the refused config,
#               set one version back: the change must keep it there, migrated
#               in place;
#   kept:<path> as kept, made at <path> (printf %b), where the reason says
#               the change leaves it: it stays as it was, and <db> is new.
test_J14_DbPathRefusedFixAndRerun() {
    local store broken advice fixed db h cfg before kept kept_sum
    local want="install.sh: cannot tell which store database agent-director opens; refusing to install."
    while IFS='|' read -r store broken advice fixed db <&3; do
        h="$(new_home)" cfg="$h/.agent-director/config.toml" db="$h/$db" kept=""
        mkdir -p "$h/.agent-director"
        case "$store" in
            older)
                printf '%b\n' "$fixed" >"$cfg"
                run "$h" "${J7ARGV[@]}"
                expect_rc 0 "\"$broken\": first install" || continue
                kept="$db" ;;
            kept|kept:*)
                j14_config "$h" "$broken" "$fixed"
                run "$h" "$BIN" list
                expect_rc 0 "\"$broken\": agent-director list under the refused config" || continue
                kept="$db"
                [[ "$store" == kept ]] || kept="$h/$(printf '%b' "${store#kept:}")"
                if [[ ! -f "$kept" ]]; then
                    bad "\"$broken\": under the refused config agent-director made no store at $kept: $(cd "$h" && find . -type f | tr '\n' ' ')"
                    continue
                fi ;;
        esac
        if [[ -n "$kept" ]]; then
            keep_older "$kept"
            kept_sum="$(sha256sum "$kept")"
        fi
        j14_config "$h" "$broken" "$fixed"
        if [[ "$broken" == '<unreadable>' && -r "$cfg" ]]; then
            bad "\"$broken\": a mode-000 config.toml is readable here (running as root?); the row cannot run"
            continue
        fi
        before="$(j14_snap "$h")"
        run "$h" "${J7ARGV[@]}"
        expect_exit5 ErrConfigMalformed "config \"$broken\"" || continue
        [[ "$(head -n 1 "$ERR")" == "$want" ]] || bad "\"$broken\": first stderr line \"$(head -n 1 "$ERR")\"; want \"$want\""
        expect_advice "$advice"
        expect_advice "Nothing was installed or changed. Re-run this install after the change."
        [[ "$(j14_snap "$h")" == "$before" ]] || bad "\"$broken\": the refusal changed $h: $(diff <(echo "$before") <(j14_snap "$h"))"
        case "$broken" in
            '<directory>') rmdir "$cfg" && printf '%b\n' "$fixed" >"$cfg" ;;
            '<unreadable>') chmod 600 "$cfg" ;;
            *) printf '%b\n' "$fixed" >"$cfg" ;;
        esac
        run "$h" "${J7ARGV[@]}"
        expect_rc 0 "\"$broken\": re-run after the change to \"$fixed\"" || continue
        if [[ -n "$kept" && "$kept" == "$db" ]]; then
            expect_store "$h" "$db" "\"$broken\"" migrated
            continue
        fi
        expect_store "$h" "$db" "\"$broken\""
        if [[ -n "$kept" ]]; then
            [[ "$(sha256sum "$kept")" == "$kept_sum" ]] || bad "\"$broken\": the store agent-director kept at $kept changed"
            [[ "$(is_kept "$db")" == 0 ]] || bad "\"$broken\": the store at $db is the one kept at $kept"
        fi
    done 3<<'EOF'
kept|[store]\ndb_path = """~/custom/s.db"""|This line holds """ or ''', which install.sh reads as the start of a multi-line string. Write each value on one line, as a "..." or '...' string, with no """ or ''' anywhere on the line.|[store]\ndb_path = "~/custom/s.db"|custom/s.db
kept|\xff\xfe[store]\ndb_path = "~/custom/s.db"|This line starts with the bytes FF FE or FE FF, a UTF-16 byte-order mark, which install.sh does not read. Remove those two bytes: agent-director skips them, so it reads the file the same without them.|[store]\ndb_path = "~/custom/s.db"|custom/s.db
kept|[Store]\ndb_path = "~/custom/s.db"|agent-director reads this header as [store]: its TOML decoder matches names regardless of letter case. Write it as [store].|[store]\ndb_path = "~/custom/s.db"|custom/s.db
fresh|[store]\n[defaults]\nrelay_mode = "off"\n[store]\ndb_path = "~/custom/s.db"|This is a second [store] header, and agent-director refuses a table defined twice. Move the lines under it, up to the next header, to under the first [store] header, then remove this header.|[store]\ndb_path = "~/custom/s.db"\n[defaults]\nrelay_mode = "off"|custom/s.db
fresh|[[store]]\ndb_path = "~/custom/s.db"|This makes store an array of tables ([[name]]), and agent-director reads [store] only as one table, so it refuses the file. Write it as [store].|[store]\ndb_path = "~/custom/s.db"|custom/s.db
kept|[store]\n[[hooks]]\ndb_path = "~/custom/s.db"\n[defaults]\nrelay_mode = "off"|This is an array of tables ([[name]]), which agent-director does not read: it ignores the lines under this header, or refuses the file. Remove the header and the lines under it, up to the next header.|[store]\n[defaults]\nrelay_mode = "off"|.agent-director/state.db
kept|["store"]\ndb_path = "~/custom/s.db"|agent-director reads this header as [store]: a quoted name is the same as the bare one, and its TOML decoder matches names regardless of letter case. Write it as [store].|[store]\ndb_path = "~/custom/s.db"|custom/s.db
kept|["defaults"]\nrelay_mode = "off"\n[store]\ndb_path = "~/custom/s.db"|A quoted table name is the same as the bare one. Write it as [defaults], without the quotes.|[defaults]\nrelay_mode = "off"\n[store]\ndb_path = "~/custom/s.db"|custom/s.db
kept|[store]\n["a b"]\ndb_path = "~/custom/s.db"\n[defaults]\nrelay_mode = "off"|agent-director reads no table with this name, so it ignores the lines under this header. Remove the header and the lines under it, up to the next header.|[store]\n[defaults]\nrelay_mode = "off"|.agent-director/state.db
kept|[store]\n[store.extra]\ndb_path = "~/custom/s.db"|This header names a table within a table ([a.b]), which agent-director does not read: it ignores the lines under this header, or refuses the file. Remove the header and the lines under it, up to the next header.|[store]|.agent-director/state.db
older|[defaults\nrelay_mode = "off"\n[store]\ndb_path = "~/custom/s.db"|This line starts with [ but is not a table header install.sh can read: a missing ], say, a quoted name holding an escape (\), or part of an array value that spans lines. Correct the header to [name], with the name bare, or write the array on one line.|[defaults]\nrelay_mode = "off"\n[store]\ndb_path = "~/custom/s.db"|custom/s.db
kept|["st\\u006fre"]\ndb_path = "~/custom/s.db"|This line starts with [ but is not a table header install.sh can read: a missing ], say, a quoted name holding an escape (\), or part of an array value that spans lines. Correct the header to [name], with the name bare, or write the array on one line.|[store]\ndb_path = "~/custom/s.db"|custom/s.db
kept|[defaults]\nnotes = [\n  ["a"],\n]\n[store]\ndb_path = "~/custom/s.db"|This line starts with [ but is not a table header install.sh can read: a missing ], say, a quoted name holding an escape (\), or part of an array value that spans lines. Correct the header to [name], with the name bare, or write the array on one line.|[defaults]\nnotes = [["a"]]\n[store]\ndb_path = "~/custom/s.db"|custom/s.db
kept|"store" = { db_path = "~/custom/s.db" }|This sets store as a key (an inline table, say) rather than under a [store] header. Remove this line. If it sets db_path, set db_path under the file's [store] header instead, adding that header at the end of the file if the file has none. Add no header in this line's place: the lines below it, up to the next header, would fall under that header too.|[store]\ndb_path = "~/custom/s.db"|custom/s.db
kept|[store]\n"db_path" = "~/custom/s.db"|agent-director reads this key as db_path: a quoted key is the same as the bare one, and its TOML decoder matches names regardless of letter case. Write it as db_path.|[store]\ndb_path = "~/custom/s.db"|custom/s.db
kept|[defaults]\n"relay_mode" = "off"\n[store]\ndb_path = "~/custom/s.db"|A quoted key is the same as the bare one. Write it as relay_mode, without the quotes.|[defaults]\nrelay_mode = "off"\n[store]\ndb_path = "~/custom/s.db"|custom/s.db
kept|[store]\n"db path" = "~/custom/s.db"|agent-director reads no key with this name, so it ignores this line. Remove it.|[store]|.agent-director/state.db
kept|store.db_path = "~/custom/s.db"|This sets a store key as a dotted key (store.db_path = ..., say) rather than under a [store] header. Move this line, without the store. prefix, to under the file's [store] header, adding that header at the end of the file if the file has none. Add no header in this line's place: the lines below it, up to the next header, would fall under that header too.|[store]\ndb_path = "~/custom/s.db"|custom/s.db
kept|defaults.relay_mode = "off"\n[store]\ndb_path = "~/custom/s.db"|Before any header, a dotted key a.b = value sets b under [a]. Move this line, without the defaults. prefix, to under the file's [defaults] header, adding that header at the end of the file if the file has none. Add no header in this line's place: the lines below it, up to the next header, would fall under that header too.|[store]\ndb_path = "~/custom/s.db"\n[defaults]\nrelay_mode = "off"|custom/s.db
kept|"a b".c = 1\n[store]\ndb_path = "~/custom/s.db"|agent-director reads no table with this key's first name, so it ignores this line. Remove it.|[store]\ndb_path = "~/custom/s.db"|custom/s.db
kept|[store]\nx.db_path = "~/custom/s.db"|This dotted key sets a key in a table within a table, which agent-director does not read: it ignores this line, or refuses the file. Remove it.|[store]|.agent-director/state.db
kept|[defaults]\nnotes = [\n  "a",\n]\n[store]\ndb_path = "~/custom/s.db"|This line is not a blank line, a # comment, a [name] header or a name = value line: part of a value that spans lines (an array, say), a quoted name holding an escape (\), or a typo. Write each value on one line, write the name bare, without quotes or escapes, or correct the typo.|[defaults]\nnotes = ["a"]\n[store]\ndb_path = "~/custom/s.db"|custom/s.db
kept|[store]\n"db\\u005fpath" = "~/custom/s.db"|This line is not a blank line, a # comment, a [name] header or a name = value line: part of a value that spans lines (an array, say), a quoted name holding an escape (\), or a typo. Write each value on one line, write the name bare, without quotes or escapes, or correct the typo.|[store]\ndb_path = "~/custom/s.db"|custom/s.db
fresh|[store]\ndb_path "~/custom/s.db"|This line is not a blank line, a # comment, a [name] header or a name = value line: part of a value that spans lines (an array, say), a quoted name holding an escape (\), or a typo. Write each value on one line, write the name bare, without quotes or escapes, or correct the typo.|[store]\ndb_path = "~/custom/s.db"|custom/s.db
kept|store = { db_path = "~/custom/s.db" }|This sets store as a key (an inline table, say) rather than under a [store] header. Remove this line. If it sets db_path, set db_path under the file's [store] header instead, adding that header at the end of the file if the file has none. Add no header in this line's place: the lines below it, up to the next header, would fall under that header too.|[store]\ndb_path = "~/custom/s.db"|custom/s.db
kept|[store]\nDB_PATH = "~/custom/s.db"|agent-director reads this key as db_path: its TOML decoder matches names regardless of letter case. Write it as db_path.|[store]\ndb_path = "~/custom/s.db"|custom/s.db
fresh|[store]\ndb_path = "~/custom/s.db"\ndb_path = "~/other/s.db"|This sets db_path a second time. Keep one.|[store]\ndb_path = "~/custom/s.db"|custom/s.db
kept|[store]\ndb_path = "~/custom/\\u0073.db"|db_path's "..." value holds a backslash, which starts an escape. Write the path without escapes; in single quotes ('...') a backslash or a double quote is literal.|[store]\ndb_path = "~/custom/s.db"|custom/s.db
kept|[store]\ndb_path = "~/cus\\\\tom/s.db"|db_path's "..." value holds a backslash, which starts an escape. Write the path without escapes; in single quotes ('...') a backslash or a double quote is literal.|[store]\ndb_path = '~/cus\\tom/s.db'|cus\tom/s.db
fresh|[store]\ndb_path = ~/custom/s.db|db_path's value is not a one-line "..." or '...' string, optionally followed by a # comment. Write it in that form.|[store]\ndb_path = '~/custom/s.db' # moved|custom/s.db
kept:custom/s.db\t|[store]\ndb_path = "~/custom/s.db\t"|db_path's value holds a control character, such as a tab. install.sh cannot install with one in the store path, so remove it. agent-director then opens the path without it, not any store it already keeps at this one.|[store]\ndb_path = "~/custom/s.db"|custom/s.db
fresh|[store]\ndb_path = "~/custom/s.db?mode=ro"|db_path's value holds a '?'. agent-director's store open reads everything from the first '?' on as SQLite options, so it would not open this path. Use a path without '?'.|[store]\ndb_path = "~/custom/s.db"|custom/s.db
fresh|<directory>|It is not a readable file (a directory, say, or a file without read permission), so agent-director cannot load it either. Make it one.|[store]\ndb_path = "~/custom/s.db"|custom/s.db
fresh|<unreadable>|It is not a readable file (a directory, say, or a file without read permission), so agent-director cannot load it either. Make it one.|[store]\ndb_path = "~/custom/s.db"|custom/s.db
EOF
}

# named_line <config>: set NAMED_N to the number of the line ERR's
# "  line N  : " names and NAMED_LINE to that line of <config>; fails when ERR
# names none.
named_line() {
    local -a lines
    NAMED_N="$(sed -n 's/^  line \([0-9][0-9]*\)  : .*/\1/p' "$ERR")"
    [[ "$NAMED_N" =~ ^[0-9]+$ ]] || { bad "the refusal names no line: $(flat "$ERR")"; return 1; }
    mapfile -t lines <"$1"
    NAMED_LINE="${lines[NAMED_N - 1]}"
}

# move_under <config> <n> <table> <line>...: take line <n> out of <config> and
# put the <line>s right after its [<table>] header, or after a [<table>] added
# at the end of the file when it has none.
move_under() {
    local cfg="$1" n="$2" table="$3" line placed=0
    local -a lines
    shift 3
    mapfile -t lines <"$cfg"
    unset 'lines[n - 1]'
    for line in "${lines[@]}"; do
        printf '%s\n' "$line"
        if [[ "$placed" -eq 0 && "$line" == "[$table]" ]]; then
            [[ "$#" -eq 0 ]] || printf '%s\n' "$@"
            placed=1
        fi
    done >"$cfg"
    [[ "$placed" -eq 1 ]] || printf '%s\n' "[$table]" "$@" >>"$cfg"
}

# j14_move_line <config>: do to <config> what ERR's "Move this line, without
# the <a>. prefix, to under the file's [<t>] header, adding that header at the
# end of the file if the file has none. Add no header in this line's place"
# says, for the line ERR names: take it out, drop <a>., and put the rest right
# after the [<t>] header, or after a [<t>] added at the end of the file.
j14_move_line() {
    local cfg="$1" prefix table moved
    local re="Move this line, without the ([^ ]+) prefix, to under the file's \\[([^]]+)\\] header, adding that header at the end of the file if the file has none\\. Add no header in this line's place"
    [[ "$(flat "$ERR")" =~ $re ]] || { bad "no move-this-line advice: $(flat "$ERR")"; return 1; }
    prefix="${BASH_REMATCH[1]}" table="${BASH_REMATCH[2]}"
    named_line "$cfg" || return 1
    moved="${NAMED_LINE#"$prefix"}"
    [[ "$moved" != "$NAMED_LINE" ]] || { bad "line $NAMED_N, \"$NAMED_LINE\", does not start with $prefix"; return 1; }
    move_under "$cfg" "$NAMED_N" "$table" "$moved"
}

# J14: "Move this line, without the <a>. prefix, to under the file's [<a>]
# header, adding that header at the end of the file if the file has none. Add
# no header in this line's place ..." followed in turn for two dotted keys
# before any header, defaults.relay_mode then store.db_path, over the older
# store agent-director keeps at that db_path. Each refusal changes nothing; the
# install after the last move migrates that store in place, and makes no
# default store.
test_J14_DottedKeysMoveInTurn() {
    local h cfg db line advice before
    h="$(new_home)" cfg="$h/.agent-director/config.toml" db="$h/custom/s.db"
    mkdir -p "$h/.agent-director"
    printf '%s\n' 'defaults.relay_mode = "on"' 'store.db_path = "~/custom/s.db"' >"$cfg"
    run "$h" "$BIN" list
    expect_rc 0 "agent-director list under the refused config" || return
    [[ -f "$db" ]] || { bad "under the refused config agent-director made no store at $db"; return; }
    keep_older "$db"
    while IFS='|' read -r line advice <&3; do
        before="$(j14_snap "$h")"
        run "$h" "${J7ARGV[@]}"
        expect_exit5 ErrConfigMalformed "config line 1 \"$line\"" || return
        expect_advice "line 1 : $line"
        expect_advice "$advice"
        [[ "$(j14_snap "$h")" == "$before" ]] || bad "\"$line\": the refusal changed $h: $(diff <(echo "$before") <(j14_snap "$h"))"
        j14_move_line "$cfg" || return
    done 3<<'EOF'
defaults.relay_mode = "on"|Before any header, a dotted key a.b = value sets b under [a]. Move this line, without the defaults. prefix, to under the file's [defaults] header, adding that header at the end of the file if the file has none. Add no header in this line's place: the lines below it, up to the next header, would fall under that header too.
store.db_path = "~/custom/s.db"|This sets a store key as a dotted key (store.db_path = ..., say) rather than under a [store] header. Move this line, without the store. prefix, to under the file's [store] header, adding that header at the end of the file if the file has none. Add no header in this line's place: the lines below it, up to the next header, would fall under that header too.
EOF
    run "$h" "${J7ARGV[@]}"
    expect_rc 0 "re-run after both moves, config: $(flat "$cfg")" || return
    expect_store "$h" "$db" "after both moves" migrated
}

# ---- J15: no temp file for the migration sentinel (b.2io) -------------------------

# J15: "Fix what mktemp's error names (a directory you cannot write, say, or a
# full disk), then re-run this install: it authorizes the migration again." An
# upgrade of the default store, and of one [store] db_path moves, whose mktemp
# cannot create the sentinel's temp file beside the store: nothing authorized,
# nothing left beside the store and the store unchanged; with mktemp working
# again, the re-run migrates it.
test_J15_SentinelTempFailedFixAndRerun() {
    local spec h db shown sentinel sum
    for spec in '|.agent-director/state.db' '[store]\ndb_path = "~/custom/s.db"|custom/s.db'; do
        PATH_EXTRA=""
        h="$(new_home)" db="$h/${spec#*|}" shown=state.db
        sentinel="${db%/*}/migrate-authorized"
        mkdir -p "$h/.agent-director"
        if [[ -n "${spec%%|*}" ]]; then
            printf '%b\n' "${spec%%|*}" >"$h/.agent-director/config.toml"
            shown="$db"
        fi
        run "$h" "${J7ARGV[@]}"
        expect_rc 0 "$shown: first install" || continue
        keep_older "$db"
        sum="$(sha256sum "$db")"
        PATH_EXTRA="$SENTINEL_MKTEMP_FAILS"
        run "$h" "${J7ARGV[@]}"
        expect_exit5 ErrSchemaVerifyFailed "$shown: mktemp fails beside the sentinel" || continue
        # Its error is above: mktemp's own, naming a template no one can predict.
        [[ "$(head -n 2 "$ERR")" == "mktemp: failed to create file via template '$sentinel.tmp.XXXXXX': Permission denied"$'\n'"install.sh: writing the migration sentinel FAILED" ]] \
            || bad "$shown: stderr does not open with mktemp's error for $sentinel.tmp.XXXXXX, then the failure: $(head -n 2 "$ERR" | tr '\n' '|')"
        expect_advice "sentinel: $sentinel mktemp could not create a temp file beside it (its error is above), so no migration was authorized: $shown is still at v$((SCHEMA - 1)). The new agent-director does not open it until it is at v$SCHEMA. Fix what mktemp's error names (a directory you cannot write, say, or a full disk), then re-run this install: it authorizes the migration again."
        if compgen -G "$sentinel*" >/dev/null; then
            bad "$shown: left beside the store: $(compgen -G "$sentinel*")"
        fi
        [[ "$(sha256sum "$db")" == "$sum" ]] || bad "$shown: the store changed"
        PATH_EXTRA="" # what mktemp's error named is fixed
        run "$h" "${J7ARGV[@]}"
        expect_rc 0 "$shown: re-run with mktemp working" || continue
        expect_store "$h" "$db" "$shown" migrated
        grep -qxF "  schema  : migration verified — $shown now at v$SCHEMA" "$OUT" \
            || bad "$shown: the re-run did not verify the migration: $(flat "$OUT")"
    done
}

# ---- J16: config.toml sets defaults as a key, hooks on (b.whe) --------------------

# j16_move_keys <config>: do to <config> what ERR's "Remove this line, and set
# each key it sets under the file's [defaults] header instead, adding that
# header at the end of the file if the file has none." says, for the inline
# table on the line ERR names: take the line out, and put each key = value it
# holds right after the [defaults] header, or after a [defaults] added at the
# end of the file.
j16_move_keys() {
    local body pair
    local -a pairs keys=()
    named_line "$1" || return 1
    body="${NAMED_LINE#*\{}" && body="${body%\}*}"
    [[ "$body" != "$NAMED_LINE" ]] || { bad "line $NAMED_N, \"$NAMED_LINE\", holds no inline table"; return 1; }
    IFS=, read -r -a pairs <<<"$body"
    for pair in "${pairs[@]}"; do
        pair="${pair#"${pair%%[![:space:]]*}"}" && pair="${pair%"${pair##*[![:space:]]}"}"
        [[ -z "$pair" ]] || keys+=("$pair")
    done
    move_under "$1" "$NAMED_N" defaults "${keys[@]}"
}

# J16: "Remove this line, and set each key it sets under the file's [defaults]
# header instead, adding that header at the end of the file if the file has
# none. Add no header in this line's place ..." With hooks on, a config setting
# defaults as an inline table before any header, in any letter case, is refused
# and left as it was; doing that, the re-run installs, merges
# inject_help_hook = true into [defaults], and agent-director list loads the
# config. A quoted "defaults" gets the db_path reader's "Write it as defaults,
# without the quotes." first, which, followed, leads here. Per case
# <config>|<name the case sentence gives>|<config after the re-run> (printf %b).
test_J16_DefaultsKeyMoveUnderHeader() {
    local -a J7ARGV=("${J7FULL[@]}") # hooks on: the config.toml merge runs
    local h cfg config reads_as merged before
    local want="install.sh: cannot merge inject_help_hook = true into config.toml's [defaults] table; refusing to install."
    while IFS='|' read -r config reads_as merged <&3; do
        h="$(new_home)" cfg="$h/.agent-director/config.toml"
        mkdir -p "$h/.agent-director"
        printf '%b\n' "$config" >"$cfg"
        if [[ "$config" == '"defaults"'* ]]; then
            before="$(j14_snap "$h")"
            run "$h" "${J7ARGV[@]}"
            expect_exit5 ErrConfigMalformed "config \"$config\"" || continue
            expect_advice "A quoted key is the same as the bare one. Write it as defaults, without the quotes."
            [[ "$(j14_snap "$h")" == "$before" ]] || bad "\"$config\": the refusal changed $h: $(diff <(echo "$before") <(j14_snap "$h"))"
            named_line "$cfg" || continue
            sed -i "${NAMED_N}s/\"defaults\"/defaults/" "$cfg"
        fi
        before="$(j14_snap "$h")"
        run "$h" "${J7ARGV[@]}"
        expect_exit5 ErrConfigMalformed "config \"$(flat "$cfg")\"" || continue
        [[ "$(head -n 1 "$ERR")" == "$want" ]] || bad "\"$config\": first stderr line \"$(head -n 1 "$ERR")\"; want \"$want\""
        expect_advice "line 1 : $(head -n 1 "$cfg")"
        [[ -z "$reads_as" ]] \
            || expect_advice "agent-director reads $reads_as as defaults: its TOML decoder matches names regardless of letter case."
        expect_advice "This sets defaults as a key (an inline table, say) rather than under a [defaults] header. With hooks on, install.sh sets inject_help_hook = true under a [defaults] header and edits no table set as a key: the header it would add can leave a file agent-director refuses. Remove this line, and set each key it sets under the file's [defaults] header instead, adding that header at the end of the file if the file has none. Add no header in this line's place: the lines below it, up to the next header, would fall under that header too."
        expect_advice "Nothing was installed or changed. Re-run this install after the change."
        [[ "$(j14_snap "$h")" == "$before" ]] || bad "\"$config\": the refusal changed $h: $(diff <(echo "$before") <(j14_snap "$h"))"
        j16_move_keys "$cfg" || continue
        run "$h" "${J7ARGV[@]}"
        expect_rc 0 "\"$config\": re-run after the move, config: $(flat "$cfg")" || continue
        expect_installed "$h" "$BIN" "$ADMIN"
        [[ "$(<"$cfg")" == "$(printf '%b' "$merged")" ]] \
            || bad "\"$config\": config after the re-run: $(paste -sd'|' "$cfg"); want $(printf '%b' "$merged" | paste -sd'|')"
        run "$h" "$h/.agent-director/bin/agent-director" list
        expect_rc 0 "\"$config\": agent-director list after the re-run"
    done 3<<'EOF'
defaults = { relay_mode = "off" }\n[relay]\npoll_base_ms = 100||[relay]\npoll_base_ms = 100\n[defaults]\nrelay_mode = "off"\ninject_help_hook = true
Defaults = { inject_help_hook = false, relay_mode = "off" }\n[defaults]\nexpire_retention_days = 7|Defaults|[defaults]\ninject_help_hook = true\nrelay_mode = "off"\nexpire_retention_days = 7
"defaults" = { relay_mode = "off" }||[defaults]\nrelay_mode = "off"\ninject_help_hook = true
EOF
}

# ---- J17: exit 5's cause line (b.cfq) ----------------------------------------------

# j17_remedy <err_name>: what a caller of install.sh does after an exit 5 whose
# cause line names <err_name>, from the name alone, as SKILL.md's "Exit 5's
# cause line" maps it: rerun, fix-config, newer-binary, stop for a human, or,
# for any other name, rerun-once (stop if the re-run fails with that name).
j17_remedy() {
    case "$1" in
        ErrVersionUnreadable) echo rerun ;;
        ErrConfigMalformed) echo fix-config ;;
        ErrSchemaMismatch) echo newer-binary ;;
        ErrSchemaVerifyFailed) echo stop ;;
        *) echo rerun-once ;;
    esac
}

# The setups for test_J17's cases: each leaves an exit 5 of J7ARGV in J7H.
# j17_lock: step 2's read of an older store fails as under a held lock.
j17_lock() { j7_older_store && j7_run 1; }
# j17_config_refused: an older store under a config agent-director refuses.
j17_config_refused() {
    j7_older_store || return 1
    printf '[defaults]\nexpire_retention_days = -1\n' >"$J7H/.agent-director/config.toml"
    run "$J7H" "${J7ARGV[@]}"
}
# j17_newer_store: a store one version newer than BIN.
j17_newer_store() {
    j7_installed || return 1
    "$SQLITE" "$J7H/.agent-director/state.db" "PRAGMA user_version = $((SCHEMA + 1));"
    run "$J7H" "${J7ARGV[@]}"
}
# j17_mismatch: an older store whose step-5 read gives the pre-migration version.
j17_mismatch() { j7_older_store && j7_run 2 "$((SCHEMA - 1))"; }
# j17_fake <mode>: a fresh install of FAKE_AD, J7ARGV with FAKE_AD as
# --binary, whose `list` does <mode>; J17_RELAY is that list's stderr with
# every line indented two spaces, as step 4 relays a failed open's.
j17_fake() {
    J7H="$(new_home)"
    printf '%s' "$1" >"$J7H/fake-list"
    J7ARGV=(bash "$LOOSE" --binary "$FAKE_AD" --admin-binary "$ADMIN" --no-hooks --no-symlink)
    J17_RELAY="$(env -i HOME="$J7H" "$FAKE_AD" list 2>&1 >/dev/null | sed 's/^/  /')"
    run "$J7H" "${J7ARGV[@]}"
}

# J17: "Every exit 5 ends with one line on stderr, its last, naming the cause:
# `install.sh: err_name=<Name>`. Branch on <Name>, never on the text above it"
# (install.sh --help). Per case <setup>|<headline>|<Name>|<config fix>: the
# exit 5 <setup> leaves has <headline> on stderr (the site the case is for) and
# one cause line, its last, naming <Name>. The remedy is chosen from that line
# alone (j17_remedy) and followed: rerun runs J7ARGV again with the lock gone,
# fix-config applies the sed <config fix> to config.toml and re-runs,
# newer-binary installs BIN_NEWER; each then installs. rerun-once runs J7ARGV
# again, which, the cause unchanged, fails with the same name: then, as for
# stop, it is a human's turn. The FAKE_AD cases: no store after an open that
# succeeded, and a failed open whose stderr (relayed below <headline>, every
# line indented) holds no envelope (a multi-line panic), one whose err_name is
# no Err... name (one holding a cause line, which must stay inside the
# envelope), or one with an err_name install.sh's own remedies do not name.
test_J17_ExitFiveCauseLineDecidesRemedy() {
    local setup headline name fix cause remedy want_bin want_v relayed J17_RELAY
    local -a words argv0=("${J7ARGV[@]}")
    local -a J7ARGV # j17_fake sets its own
    run "$(new_home)" bash "$LOOSE" --help
    expect_rc 0 "install.sh --help" || return
    [[ "$(flat "$OUT")" == *'Every exit 5 ends with one line on stderr, its last, naming the cause: `install.sh: err_name=<Name>`. Branch on <Name>, never on the text above it: ErrVersionUnreadable (re-run), ErrConfigMalformed (fix config.toml, then re-run), ErrSchemaMismatch (install a newer agent-director), ErrSchemaVerifyFailed (needs a human), or, when the store open fails another way, agent-director'"'"'s own err_name.'* ]] \
        || bad "--help lacks the cause-line contract: $(flat "$OUT")"
    while IFS='|' read -r setup headline name fix <&3; do
        J7ARGV=("${argv0[@]}") J17_RELAY=""
        read -r -a words <<<"$setup"
        "${words[@]}" || { bad "$setup: setup failed"; continue; }
        expect_exit5 "$name" "$setup" || continue
        grep -qxF "$headline" "$ERR" || bad "$setup: no \"$headline\" line: $(flat "$ERR")"
        if [[ -n "$J17_RELAY" ]]; then
            relayed="$(grep -m1 -xF -A"$(wc -l <<<"$J17_RELAY")" "$headline" "$ERR" | tail -n +2)"
            [[ "$relayed" == "$J17_RELAY" ]] \
                || bad "$setup: below \"$headline\": $(paste -sd'|' <<<"$relayed"); want the open's stderr, every line indented two spaces: $(paste -sd'|' <<<"$J17_RELAY")"
        fi
        cause="$(tail -n 1 "$ERR")"
        remedy="$(j17_remedy "${cause#install.sh: err_name=}")"
        want_bin="$BIN" want_v="$SCHEMA"
        case "$remedy" in
            rerun) run "$J7H" "${J7ARGV[@]}" ;;
            fix-config)
                sed -i "$fix" "$J7H/.agent-director/config.toml"
                run "$J7H" "${J7ARGV[@]}" ;;
            newer-binary)
                want_bin="$BIN_NEWER" want_v=$((SCHEMA + 1))
                run "$J7H" bash "$LOOSE" --binary "$BIN_NEWER" --admin-binary "$ADMIN" --no-hooks --no-symlink ;;
            rerun-once)
                expect_advice "re-running this install will retry it."
                run "$J7H" "${J7ARGV[@]}"
                expect_exit5 "${cause#install.sh: err_name=}" "$setup: re-run once after \"$cause\""
                continue ;;
            stop) continue ;;
        esac
        expect_rc 0 "$setup: $remedy after \"$cause\"" || continue
        expect_installed "$J7H" "$want_bin" "$ADMIN"
        [[ "$(db_version "$J7H")" == "$want_v" ]] || bad "$setup: store at v$(db_version "$J7H") after $remedy; want v$want_v"
    done 3<<'EOF'
j17_lock|install.sh: reading state.db's schema version FAILED|ErrVersionUnreadable|
j17_config_refused|install.sh: agent-director refused its config file (ErrConfigMalformed)|ErrConfigMalformed|/^expire_retention_days = /d
j17_newer_store|install.sh: store open (agent-director list) failed after install|ErrSchemaMismatch|
j17_mismatch|install.sh: schema migration verification FAILED|ErrSchemaVerifyFailed|
j17_fake ok|install.sh: state.db was not created by the store open|ErrSchemaVerifyFailed|
j17_fake panic|install.sh: store open (agent-director list) failed after install|ErrStoreOpen|
j17_fake odd-name|install.sh: store open (agent-director list) failed after install|ErrStoreOpen|
j17_fake other-name|install.sh: store open (agent-director list) failed after install|ErrSchemaMigrationRequired|
EOF
}

# ---- J18: settings.json of another shape, hooks on (b.cfq) -------------------------

# J18: "It is valid JSON, but not an object whose hooks hold event lists, the
# shape Claude Code reads. Fix it, then re-run this install." A hooks-on
# install over such a settings.json exits 4, the hook merge failure, with jq's
# error on the line above its headline and no exit-5 cause line, and leaves the
# file as it was; rewritten to that shape, the re-run injects both hooks. Per
# case <settings.json>|<fixed>, @CMD@ the help hook's command: a SessionStart
# object holding that hook is not the hook already there (b.zbg).
test_J18_SettingsShapeFixAndRerun() {
    local -a argv=(bash "$LOOSE" --binary "$BIN" --admin-binary "$ADMIN" --no-symlink) # hooks on
    local h sj settings fixed above
    local want="install.sh: cannot merge the hooks into ~/.claude/settings.json (jq's error is above)"
    while IFS='|' read -r settings fixed <&3; do
        h="$(new_home)" sj="$h/.claude/settings.json"
        settings="${settings//@CMD@/$h/.agent-director/bin/agent-director help}"
        fixed="${fixed//@CMD@/$h/.agent-director/bin/agent-director help}"
        mkdir -p "$h/.claude" && printf '%s\n' "$settings" >"$sj"
        run "$h" "${argv[@]}"
        expect_rc 4 "settings.json $settings" || continue
        expect_advice "$want It is valid JSON, but not an object whose hooks hold event lists, the shape Claude Code reads. Fix it, then re-run this install."
        above="$(grep -m1 -xF -B1 "$want" "$ERR" | head -n 1)"
        [[ "$above" == "jq: error"* ]] || bad "$settings: the line above \"$want\" is not jq's error: $(paste -sd'|' "$ERR")"
        if grep -q '^install\.sh: err_name=' "$ERR"; then
            bad "$settings: an exit-5 cause line on exit 4: $(flat "$ERR")"
        fi
        [[ "$(<"$sj")" == "$settings" ]] || bad "$settings: settings.json changed: $(<"$sj")"
        if compgen -G "$sj.*" >/dev/null; then
            bad "$settings: left beside settings.json: $(compgen -G "$sj.*")"
        fi
        printf '%s\n' "$fixed" >"$sj"
        run "$h" "${argv[@]}"
        expect_rc 0 "$settings fixed to $fixed: re-run" || continue
        hooks_injected "$h" || bad "$settings fixed to $fixed: hooks not injected: $(<"$sj")"
    done 3<<'EOF'
[]|{}
{"hooks":"x"}|{"hooks":{}}
{"hooks":{"SessionEnd":{}}}|{"hooks":{"SessionEnd":[]}}
{"hooks":{"SessionStart":{"x":{"hooks":[{"type":"command","command":"@CMD@"}]}}}}|{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"@CMD@"}]}]}}
EOF
}

# ---- J19: [store] busy_timeout_ms install.sh cannot read (b.c7f) -------------------

# J19: each reason install.sh gives for a [store] busy_timeout_ms line it cannot
# read, word for word, naming the line, then "Nothing was installed or changed.
# Re-run this install after the change.": the refusal changes nothing under
# HOME; making the change the reason names and re-running installs, and
# agent-director loads the changed config. Per row
# <store>|<config>|<line>|<reason>|<changed config> (printf %b): <store> fresh,
# or older (installed under the changed config, then set one version back,
# which the re-run migrates in place).
test_J19_BusyTimeoutRefusedFixAndRerun() {
    local store broken line advice fixed h cfg db before
    local want="install.sh: cannot tell how long agent-director waits for a locked store database; refusing to install."
    while IFS='|' read -r store broken line advice fixed <&3; do
        h="$(new_home)" cfg="$h/.agent-director/config.toml" db="$h/.agent-director/state.db"
        mkdir -p "$h/.agent-director"
        if [[ "$store" == older ]]; then
            printf '%b\n' "$fixed" >"$cfg"
            run "$h" "${J7ARGV[@]}"
            expect_rc 0 "\"$broken\": first install" || continue
            keep_older "$db"
        fi
        printf '%b\n' "$broken" >"$cfg"
        before="$(j14_snap "$h")"
        run "$h" "${J7ARGV[@]}"
        expect_exit5 ErrConfigMalformed "config \"$broken\"" || continue
        [[ "$(head -n 1 "$ERR")" == "$want" ]] || bad "\"$broken\": first stderr line \"$(head -n 1 "$ERR")\"; want \"$want\""
        expect_advice "config : $cfg line $line"
        expect_advice "$advice"
        expect_advice "Nothing was installed or changed. Re-run this install after the change."
        [[ "$(j14_snap "$h")" == "$before" ]] || bad "\"$broken\": the refusal changed $h: $(diff <(echo "$before") <(j14_snap "$h"))"
        printf '%b\n' "$fixed" >"$cfg"
        run "$h" "${J7ARGV[@]}"
        expect_rc 0 "\"$broken\": re-run after the change to \"$fixed\"" || continue
        if [[ "$store" == older ]]; then
            expect_store "$h" "$db" "\"$broken\"" migrated
        else
            expect_store "$h" "$db" "\"$broken\""
        fi
        run "$h" "$h/.agent-director/bin/agent-director" list
        expect_rc 0 "\"$broken\": agent-director list after the change"
    done 3<<'EOF'
fresh|[store]\nBUSY_TIMEOUT_MS = 2500|2 : BUSY_TIMEOUT_MS = 2500|agent-director reads this key as busy_timeout_ms: its TOML decoder matches names regardless of letter case. Write it as busy_timeout_ms.|[store]\nbusy_timeout_ms = 2500
older|[defaults]\nrelay_mode = "off"\n[store]\nbusy_timeout_ms = 2500\nbusy_timeout_ms = 3000|5 : busy_timeout_ms = 3000|This sets busy_timeout_ms a second time. Keep one.|[defaults]\nrelay_mode = "off"\n[store]\nbusy_timeout_ms = 2500
older|[store]\nbusy_timeout_ms = "2500"|2 : busy_timeout_ms = "2500"|busy_timeout_ms's value is not a whole number in decimal digits, such as 10000, optionally followed by a # comment. Write it in that form, without quotes, a decimal point or a 0x, 0o or 0b prefix.|[store]\nbusy_timeout_ms = 2500
fresh|[store]\nbusy_timeout_ms = 2.5e3|2 : busy_timeout_ms = 2.5e3|busy_timeout_ms's value is not a whole number in decimal digits, such as 10000, optionally followed by a # comment. Write it in that form, without quotes, a decimal point or a 0x, 0o or 0b prefix.|[store]\nbusy_timeout_ms = 2500
fresh|[store]\nbusy_timeout_ms = 0x9c4 # ms|2 : busy_timeout_ms = 0x9c4 # ms|busy_timeout_ms's value is not a whole number in decimal digits, such as 10000, optionally followed by a # comment. Write it in that form, without quotes, a decimal point or a 0x, 0o or 0b prefix.|[store]\nbusy_timeout_ms = 2500 # ms
EOF
}

# ---- J20: a symlinked settings.json or config.toml install.sh cannot write through (b.nw5)

# J20: "Fix the link so it reaches a file in a directory you can write in. Or
# re-run this install with --no-hooks, which edits neither file, and add what
# the merges add where the files come from ..." With hooks on, a settings.json
# (exit 4) or config.toml (exit 5) linked to a file in a 0555 directory (a
# stand-in for a read-only /nix/store) is refused, changing nothing under HOME.
# Per file, each way out: fix-link points the link at a copy in a directory one
# can write in, and the re-run merges through it, keeping the link; no-hooks
# re-runs with --no-hooks, which leaves the link and its file alone, then adds
# to that file what the advice names, after which a hooks-on install over it
# finds nothing to add.
test_J20_LinkUnwritableFixOrNoHooks() {
    local -a argv=(bash "$LOOSE" --binary "$BIN" --admin-binary "$ADMIN" --no-symlink) # hooks on
    local file follow h dir link store plain before added now
    for file in settings.json config.toml; do
        for follow in fix-link no-hooks; do
            h="$(new_home)" store="$h/store" dir="$h/.claude" plain='{"theme":"dark"}'
            [[ "$file" == settings.json ]] || dir="$h/.agent-director" plain='[defaults]\nrelay_mode = "off"'
            link="$dir/$file"
            mkdir -p "$dir" "$store" && printf '%b\n' "$plain" >"$store/$file" && ln -s "$store/$file" "$link" \
                && chmod 0555 "$store" || { bad "$file, $follow: setup failed"; continue; }
            before="$(j14_snap "$h")"
            run "$h" "${argv[@]}"
            if [[ "$file" == settings.json ]]; then
                expect_rc 4 "$file linked into a read-only directory" || continue
            else
                expect_exit5 ErrConfigMalformed "$file linked into a read-only directory" || continue
            fi
            [[ "$(head -n 1 "$ERR")" == "install.sh: cannot merge into $link through its symlink; refusing to install." ]] \
                || bad "$file: first stderr line \"$(head -n 1 "$ERR")\""
            expect_advice "link : $link target : $store/$file The target's directory, $store, cannot be written in (a read-only file system, say, such as home-manager's /nix/store). With hooks on, install.sh writes its merges into the file a symlinked settings.json or config.toml resolves to, keeping the link. Fix the link so it reaches a file in a directory you can write in. Or re-run this install with --no-hooks, which edits neither file, and add what the merges add where the files come from (your dotfiles or home-manager configuration, say): in settings.json, a SessionStart hook and a SessionEnd hook with matcher \"compact\", each a command hook running \"$h/.agent-director/bin/agent-director help\"; in config.toml, inject_help_hook = true under [defaults]. Nothing was installed or changed. Re-run this install after the change."
            [[ "$(j14_snap "$h")" == "$before" ]] || bad "$file: the refusal changed $h: $(diff <(echo "$before") <(j14_snap "$h"))"
            if [[ "$follow" == fix-link ]]; then
                mkdir "$h/dotfiles" && cp "$store/$file" "$h/dotfiles/$file" && ln -sfn "$h/dotfiles/$file" "$link"
                run "$h" "${argv[@]}"
                expect_rc 0 "$file: re-run after fixing the link" || continue
                expect_installed "$h" "$BIN" "$ADMIN"
                [[ "$(readlink "$link")" == "$h/dotfiles/$file" ]] || bad "$file: the re-run left the link naming \"$(readlink "$link")\""
                if [[ "$file" == settings.json ]]; then
                    hooks_injected "$h" || bad "$file: hooks not merged through the fixed link: $(<"$h/dotfiles/$file")"
                else
                    grep -qx 'inject_help_hook = true' "$h/dotfiles/$file" || bad "$file: key not merged through the fixed link: $(<"$h/dotfiles/$file")"
                fi
                continue
            fi
            run "$h" "${argv[@]}" --no-hooks
            expect_rc 0 "$file: re-run with --no-hooks" || continue
            expect_installed "$h" "$BIN" "$ADMIN"
            [[ "$(readlink "$link")" == "$store/$file" && "$(<"$store/$file")" == "$(printf '%b' "$plain")" ]] \
                || bad "$file: --no-hooks changed the link or its file: $(readlink "$link"), $(<"$store/$file")"
            # Add what the advice names where the file comes from: the store,
            # rebuilt, and left writable so the install below can show it adds
            # nothing (settings.json compared as JSON, as the merge rewrites it).
            chmod 0755 "$store"
            if [[ "$file" == settings.json ]]; then
                added="$(jq --arg c "$h/.agent-director/bin/agent-director help" '
                    .hooks.SessionStart += [{hooks: [{type: "command", command: $c}]}]
                    | .hooks.SessionEnd += [{matcher: "compact", hooks: [{type: "command", command: $c}]}]' "$store/$file")"
                printf '%s\n' "$added" >"$store/$file"
                added="$(jq -S . "$store/$file")"
            else
                printf 'inject_help_hook = true\n' >>"$store/$file"
                added="$(<"$store/$file")"
            fi
            run "$h" "${argv[@]}"
            expect_rc 0 "$file: hooks-on install over what the advice added" || continue
            [[ "$file" == settings.json ]] && now="$(jq -S . "$store/$file")" || now="$(<"$store/$file")"
            [[ "$now" == "$added" ]] || bad "$file: the merge added to what the advice named: $(<"$store/$file")"
            run "$h" "$h/.agent-director/bin/agent-director" list
            expect_rc 0 "$file: agent-director list after the advice's additions"
        done
    done
}

# ---- J21: settings.json holding several JSON documents, hooks on (b.zbg) -----------

# J21: "Each is valid JSON, but the file must hold one JSON object, the shape
# Claude Code reads. Fix it, then re-run this install." A hooks-on install over
# a settings.json holding several JSON documents exits 4, naming how many, with
# no exit-5 cause line and no "hooks : injected", and leaves the file byte for
# byte with nothing beside it; rewritten as one object, the re-run injects both
# hooks. Per case <settings.json>|<documents>|<fixed> (printf %b).
test_J21_SettingsDocumentsFixAndRerun() {
    local -a argv=(bash "$LOOSE" --binary "$BIN" --admin-binary "$ADMIN" --no-symlink) # hooks on
    local h sj settings docs fixed want
    while IFS='|' read -r settings docs fixed <&3; do
        h="$(new_home)" sj="$h/.claude/settings.json"
        want="install.sh: cannot merge the hooks into ~/.claude/settings.json: it holds $docs JSON documents"
        mkdir -p "$h/.claude" && printf '%b\n' "$settings" >"$sj"
        run "$h" "${argv[@]}"
        expect_rc 4 "settings.json $settings" || continue
        grep -qxF "$want" "$ERR" || bad "$settings: no stderr line \"$want\": $(paste -sd'|' "$ERR")"
        expect_advice "$want Each is valid JSON, but the file must hold one JSON object, the shape Claude Code reads. Fix it, then re-run this install."
        if grep -q '^install\.sh: err_name=' "$ERR"; then
            bad "$settings: an exit-5 cause line on exit 4: $(flat "$ERR")"
        fi
        if grep -q 'hooks   : injected' "$OUT"; then
            bad "$settings: \"hooks : injected\" printed on the refusal"
        fi
        cmp -s "$sj" <(printf '%b\n' "$settings") || bad "$settings: settings.json changed: $(<"$sj")"
        if compgen -G "$sj.*" >/dev/null; then
            bad "$settings: left beside settings.json: $(compgen -G "$sj.*")"
        fi
        printf '%b\n' "$fixed" >"$sj"
        run "$h" "${argv[@]}"
        expect_rc 0 "$settings fixed to $fixed: re-run" || continue
        hooks_injected "$h" || bad "$settings fixed to $fixed: hooks not injected: $(<"$sj")"
    done 3<<'EOF'
{} {}|2|{}
{"theme":"dark"}\n{"hooks":{}}\n{}|3|{"theme":"dark","hooks":{}}
EOF
}

echo "[b.fji install-sh advice-follow] start (schema v$SCHEMA)"
for t in $(declare -F | awk '{print $3}' | grep '^test_'); do
    run_test "$t"
done
echo "[b.fji install-sh advice-follow] summary: $pass passed, $fail failed, $skip skipped (known-broken)"
[[ "$fail" -eq 0 ]]
