#!/usr/bin/env bash
# platform-env.sh — the Environment Record (reusable driver helper): one JSON
# line saying which platform and tool versions produced a run's results
# (SRD SR-4.2, SR-2.6).
#
# Usage:
#   /opt/driver/platform-env.sh     in the Docker test harness
#   test/driver/platform-env.sh     from the source tree (unit and check jobs)
#
# Every Environment Record comes from this script. A consumer elsewhere, such
# as the Release Check Kit, copies it together with the sql.sh beside it; it
# never forks or reimplements it.
#
# Output: exactly one line on stdout,
#
#   {"platform_env":{"debian_version":"12.12","os_id":"debian",...}}
#
# Each field is a JSON string, or null when its source is absent, unreadable
# or empty, or its program is not on PATH or fails. Member order is not part
# of the contract. The fields:
#
#   debian_version        /etc/debian_version, trimmed
#   os_id                 ID from /etc/os-release, unquoted
#   os_version_id         VERSION_ID from /etc/os-release, unquoted
#   os_version_codename   VERSION_CODENAME from /etc/os-release, unquoted
#   dpkg_architecture     dpkg --print-architecture
#   uname_machine         uname -m
#   kernel_release        uname -r
#   tmux_version          tmux -V
#   sqlite3_version       sql.sh -version (the sqlite3 shell's own line)
#   bash_version          bash --version
#   git_version           git --version
#   node_version          node --version
#   go_version            go version
#   bun_version           bun --version
#
# Each *_version is the first line of the program's own version output,
# trimmed and otherwise verbatim. sqlite3_version is read only through the
# sql.sh beside this file, like every sqlite3 command in a driver script
# (b.ai5). Without it the field is null and stderr says to copy sql.sh too.
#
# It only reads: it compares nothing, refuses nothing, starts no agent or
# tmux server, opens no store, writes no file and needs no network. JSON is
# encoded in bash itself, so it needs no jq, Python or Node.
#
# Exit status: always 0.

driver_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# sql.sh rejects a malformed SQL_BUSY_TIMEOUT_MS before it runs anything; its
# default is fine for a version line.
unset SQL_BUSY_TIMEOUT_MS

fields=""

# trim sets REPLY to $1 without leading or trailing whitespace.
trim() {
    local s="$1"
    s="${s#"${s%%[![:space:]]*}"}"
    s="${s%"${s##*[![:space:]]}"}"
    REPLY="$s"
}

# json_escape sets REPLY to $1 escaped for a JSON string: backslash, double
# quote and every control character. Bytes are handled one by one (LC_ALL=C)
# so other UTF-8 text passes through unchanged.
json_escape() {
    local LC_ALL=C
    local s="$1" r="" c code i
    s="${s//\\/\\\\}"
    s="${s//\"/\\\"}"
    s="${s//$'\t'/\\t}"
    s="${s//$'\n'/\\n}"
    s="${s//$'\r'/\\r}"
    if [[ "$s" == *[[:cntrl:]]* ]]; then
        for ((i = 0; i < ${#s}; i++)); do
            c="${s:i:1}"
            if [[ "$c" == [[:cntrl:]] ]]; then
                printf -v code '%d' "'$c"
                printf -v c '\\u%04x' "$code"
            fi
            r+="$c"
        done
        s="$r"
    fi
    REPLY="$s"
}

# field appends "<key>":<value> to the record: $2 as a string, or null when
# it is missing or empty.
field() {
    if [[ $# -lt 2 || -z "$2" ]]; then
        fields+=",\"$1\":null"
        return
    fi
    json_escape "$2"
    fields+=",\"$1\":\"$REPLY\""
}

# probe <key> <command> [args...] records the first line of the command's
# stdout, trimmed, or null when the command is not found or exits non-zero.
probe() {
    local key="$1" out
    shift
    if ! command -v "$1" >/dev/null 2>&1; then
        field "$key"
        return
    fi
    if ! out="$("$@" </dev/null 2>/dev/null)"; then
        field "$key"
        return
    fi
    trim "${out%%$'\n'*}"
    field "$key" "$REPLY"
}

debian_version=""
if [[ -r /etc/debian_version ]]; then
    trim "$(</etc/debian_version)"
    debian_version="$REPLY"
fi
field debian_version "$debian_version"

# /etc/os-release is KEY=value lines, read as data and never sourced.
os_id="" os_version_id="" os_version_codename=""
if [[ -r /etc/os-release ]]; then
    while IFS='=' read -r key value || [[ -n "$key" ]]; do
        if [[ "$value" =~ ^\"(.*)\"$ || "$value" =~ ^\'(.*)\'$ ]]; then
            value="${BASH_REMATCH[1]}"
        fi
        case "$key" in
            ID) os_id="$value" ;;
            VERSION_ID) os_version_id="$value" ;;
            VERSION_CODENAME) os_version_codename="$value" ;;
        esac
    done </etc/os-release
fi
field os_id "$os_id"
field os_version_id "$os_version_id"
field os_version_codename "$os_version_codename"

probe dpkg_architecture dpkg --print-architecture
probe uname_machine uname -m
probe kernel_release uname -r
probe tmux_version tmux -V

if [[ -f "$driver_dir/sql.sh" ]]; then
    probe sqlite3_version "$driver_dir/sql.sh" -version
else
    field sqlite3_version
    printf 'platform-env: no sql.sh beside this script in %s, so sqlite3_version is null; copy sql.sh with platform-env.sh\n' "$driver_dir" >&2
fi

probe bash_version bash --version
probe git_version git --version
probe node_version node --version
probe go_version go version
probe bun_version bun --version

printf '{"platform_env":{%s}}\n' "${fields#,}"
exit 0
