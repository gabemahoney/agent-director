#!/usr/bin/env bash
# fake-curl.sh — test fixture for install.sh's --from-release retry path.
#
# Behavior is controlled by two env vars set by the test harness:
#
#   FAKE_CURL_STATE_FILE  Path to a counter file. The fixture increments
#                         the integer in this file on each invocation
#                         (creating it as "0" if absent).
#   FAKE_CURL_FAIL_FIRST  Number of initial invocations that should
#                         simulate an HTTP 404 (matching the CDN
#                         propagation window). Invocation #N where
#                         N > FAKE_CURL_FAIL_FIRST returns the body
#                         from FAKE_CURL_BODY_SOURCE with HTTP 200.
#   FAKE_CURL_BODY_SOURCE Path to the file whose bytes should be served
#                         on the first 200 response.
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
body_source="${FAKE_CURL_BODY_SOURCE:?FAKE_CURL_BODY_SOURCE not set}"

if [[ ! -f "$state_file" ]]; then
    echo 0 > "$state_file"
fi
count=$(cat "$state_file")
count=$((count+1))
echo "$count" > "$state_file"

if [[ "$count" -le "$fail_first" ]]; then
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
