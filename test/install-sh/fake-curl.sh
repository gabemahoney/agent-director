#!/usr/bin/env bash
# fake-curl.sh — test fixture for install.sh's --from-release retry path.
#
# Behavior is controlled by env vars set by the test harness:
#
#   FAKE_CURL_STATE_FILE  Path to a counter file. The fixture increments
#                         the integer in this file on each download
#                         invocation (creating it as "0" if absent).
#   FAKE_CURL_FAIL_FIRST  Number of initial downloads of a matching asset
#                         that should simulate an HTTP 404 (matching the
#                         CDN propagation window). Later downloads, and
#                         every download of an asset that does not match,
#                         return the asset's body with HTTP 200.
#   FAKE_CURL_FAIL_MATCH  Glob the asset name (the URL's last segment) must
#                         match to count toward FAKE_CURL_FAIL_FIRST.
#                         Default: every asset.
#   FAKE_CURL_BODY_SOURCE Path to the file whose bytes are served for the
#                         agent-director asset.
#   FAKE_CURL_ADMIN_BODY_SOURCE
#                         Path to the file whose bytes are served for the
#                         agent-director-admin asset (b.vqr). Default:
#                         FAKE_CURL_BODY_SOURCE.
#
# Only the `-o <path>` + `-w '%{http_code}'` invocation shape that
# install.sh's retry wrapper uses is supported — other invocations
# (e.g. the api.github.com tag-resolve call earlier in install.sh) are
# routed through to real curl so the rest of the script's behavior is
# unaffected.

set -euo pipefail

# Parse only the bits we care about: the trailing URL, the -o path,
# and whether -w is present (signals the retry wrapper).
out_path=""
url=""
has_w=0
args=("$@")
i=0
while [[ $i -lt ${#args[@]} ]]; do
    case "${args[$i]}" in
        -o) out_path="${args[$((i+1))]}"; i=$((i+2)) ;;
        -w) has_w=1; i=$((i+2)) ;;
        --retry) i=$((i+2)) ;;
        -*) i=$((i+1)) ;;
        *)  url="${args[$i]}"; i=$((i+1)) ;;
    esac
done

# If this isn't the retry-wrapper invocation, defer to real curl so the
# tag-resolve and any other curl uses in install.sh keep working.
if [[ "$has_w" -ne 1 ]]; then
    exec /usr/bin/curl "$@"
fi

state_file="${FAKE_CURL_STATE_FILE:?FAKE_CURL_STATE_FILE not set}"
fail_first="${FAKE_CURL_FAIL_FIRST:-0}"
fail_match="${FAKE_CURL_FAIL_MATCH:-*}"
body_source="${FAKE_CURL_BODY_SOURCE:?FAKE_CURL_BODY_SOURCE not set}"
asset="${url##*/}"
if [[ "$asset" == agent-director-admin-* ]]; then
    body_source="${FAKE_CURL_ADMIN_BODY_SOURCE:-$body_source}"
fi

# counter_next <file>: increment the integer in file and print it.
counter_next() {
    local n=0
    [[ -f "$1" ]] && n=$(cat "$1")
    n=$((n+1))
    echo "$n" > "$1"
    echo "$n"
}

counter_next "$state_file" >/dev/null
# shellcheck disable=SC2053 # fail_match is a glob on purpose
if [[ "$asset" == $fail_match ]] && [[ "$(counter_next "${state_file}.match")" -le "$fail_first" ]]; then
    # Mimic curl -fsSL on a 404: empty body file, print '404' to stdout
    # (that's what -w '%{http_code}' yields), and exit 22 (curl's
    # HTTP-error exit code). install.sh's wrapper inspects the printed
    # status, not the exit code.
    : > "$out_path"
    printf '404'
    exit 22
fi

cp "$body_source" "$out_path"
printf '200'
exit 0
