#!/usr/bin/env bash
# host-shim.sh (reusable driver helper): run one genuine Earlier Release's
# installer to build a test's starting state on Linux/aarch64 (SR-3.5).
# Never use it for a way back, the candidate installer or later commands.
#
# Usage:
#   host-shim.sh <command> [<arg>...]
#   host-shim.sh <bash> <install.sh> --binary <path> --admin-binary <path> ...
#   host-shim.sh --installs <binary> <admin-binary> -- <command> [<arg>...]
#
# --installs names the actual pair when it is absent from the command's args,
# such as the pair a test's curl stand-in serves to --from-release. It is a
# shim option, never passed to the command. Explicit --binary/--admin-binary
# command flags take precedence for their respective paths.
#
# Resolve real uname and file only in /usr/local/bin:/usr/bin:/bin, outside
# the caller's PATH stand-ins. On Linux/aarch64, both explicit binary paths
# must be little-endian 64-bit aarch64 ELF according to that real file tool.
# Missing paths, a missing real file or a failed binary check exit 1 before
# running the command. Other hosts exec the command unchanged, without file.
#
# Only that one installer sees uname -m as x86_64 and aarch64 ELF file output
# as x86-64 ELF. Every other uname call and file output/status passes through.
# Its private stand-ins are removed afterwards; the caller's PATH is unchanged.
# Exit status: the command's status, or 1 for a shim refusal/setup failure.
#
# Callers: moveInstallReleaseSetup in test/install-sh/release_fixture_test.go
# (InstallRelease and CurrentLayoutRollback's setup), moveInstallReleasePipedSetup
# there (TestScheduleReleasedCompatibility's released variant), and old-layout.sh.

set -euo pipefail

die() {
    echo "host-shim: $*" >&2
    exit 1
}

[[ $# -gt 0 ]] || die "usage: host-shim.sh <command> [<arg>...]"
binary=""
admin_binary=""
if [[ "$1" == --installs ]]; then
    [[ $# -ge 5 && -n "$2" && -n "$3" && "$4" == -- ]] \
        || die "usage: host-shim.sh --installs <binary> <admin-binary> -- <command> [<arg>...]"
    binary="$2"
    admin_binary="$3"
    shift 4
fi
real_uname="$(PATH=/usr/local/bin:/usr/bin:/bin type -P uname)" || die "no real uname in /usr/local/bin:/usr/bin:/bin"
real_file="$(PATH=/usr/local/bin:/usr/bin:/bin type -P file)" || real_file=""
host_os="$("$real_uname" -s)" || die "real uname -s failed"
host_arch="$("$real_uname" -m)" || die "real uname -m failed"
if [[ "$host_os/$host_arch" != Linux/aarch64 ]]; then
    exec "$@"
fi

[[ -n "$real_file" ]] || die "no real file in /usr/local/bin:/usr/bin:/bin"
command_args=("$@")
while [[ $# -gt 0 ]]; do
    case "$1" in
        --binary|--admin-binary)
            [[ $# -ge 2 && -n "$2" ]] || die "$1 needs a binary path"
            if [[ "$1" == --binary ]]; then binary="$2"; else admin_binary="$2"; fi
            shift 2
            ;;
        *) shift ;;
    esac
done
[[ -n "$binary" && -n "$admin_binary" ]] || die "both --binary and --admin-binary, or --installs, must name the binaries; downloads would select amd64 assets"
for path in "$binary" "$admin_binary"; do
    description="$("$real_file" -L -b "$path")" || die "$path: file failed: $description"
    [[ "$description" == *"ELF 64-bit LSB"* && "$description" == *aarch64* ]] \
        || die "$path: expected aarch64 little-endian ELF; file reports: $description"
done

shim_dir="$(mktemp -d "${TMPDIR:-/tmp}/ad-host-shim.XXXXXX")" || die "cannot create private stand-ins"
trap 'rm -rf -- "$shim_dir"' EXIT
{
    printf '#!/bin/bash\n'
    cat <<'SH'
if [[ $# -eq 1 && "$1" == -m ]]; then
    printf 'x86_64\n'
    exit 0
fi
SH
    printf 'exec %q "$@"\n' "$real_uname"
} >"$shim_dir/uname"
{
    printf '#!/bin/bash\nreal_file=%q\n' "$real_file"
    cat <<'SH'
# The sentinel preserves trailing newlines in unchanged output.
status=0
output="$("$real_file" "$@"; status=$?; printf '.'; exit "$status")" || status=$?
output="${output%.}"
if [[ "$output" == *"ELF 64-bit LSB"* && "$output" == *aarch64* ]]; then
    printf 'ELF 64-bit LSB executable, x86-64, version 1 (SYSV), statically linked\n'
else
    printf '%s' "$output"
fi
exit "$status"
SH
} >"$shim_dir/file"
chmod 0700 "$shim_dir/uname" "$shim_dir/file"
status=0
PATH="$shim_dir:$PATH" "${command_args[@]}" || status=$?
exit "$status"
