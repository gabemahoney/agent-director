#!/usr/bin/env bash
# Runs only in the user-submitted scheduler container.
set -euo pipefail
commit=$(cat /opt/b.w21/SOURCE_COMMIT)
version=$(cat /opt/b.w21/SOURCE_VERSION)
slug=${HANDOFF_JOB_SLUG:?HANDOFF_JOB_SLUG is required}
[[ "$HOME" == /home/tester && "$(id -un)" == tester ]]
[[ "${AGENT_DIRECTOR_TEST_HARNESS:-}" == 1 ]]
[[ "${DRIVER_MODE:-}" == shell ]]
[[ -z "${ANTHROPIC_API_KEY:-}" && -z "${CLAUDE_CODE_OAUTH_TOKEN:-}" ]]
cd /work/source
sha256sum --quiet -c /opt/b.w21/source.sha256
cd /home/tester
main_stamp=$(agent-director version)
admin_stamp=$(/opt/bin/agent-director-admin version)
jq -e --arg c "$commit" --arg v "$version" '.commit == $c and .version == $v' <<<"$main_stamp" >/dev/null
jq -e --arg c "$commit" --arg v "$version" '.commit == $c and .version == $v' <<<"$admin_stamp" >/dev/null
jq -cn --arg commit "$commit" --arg version "$version" --arg slug "$slug" \
    --argjson main "$main_stamp" --argjson admin "$admin_stamp" \
    --slurpfile context /opt/b.w21/context-identity.json \
    '{source_identity:{commit:$commit,version:$version,job:$slug,main:$main,admin:$admin,context_payload_sha256:$context[0].payload_sha256}}'
kind=$(jq -er --arg slug "$slug" '.jobs[] | select(.slug == $slug) | .kind' /opt/b.w21/job-cases.json)
case "$kind" in
    epic)
        [[ "${EPIC:-}" == "$slug" ]]
        command=(/opt/driver/run-testplan.sh)
        ;;
    install-mode)
        [[ -z "${EPIC:-}" ]]
        command=(/opt/install-mode/run.sh)
        ;;
    *) exit 2 ;;
esac
log=$(mktemp /tmp/b.w21-integration.XXXXXX.log)
rc=0
"${command[@]}" >"$log" 2>&1 || rc=$?
cat "$log"
python3 /opt/b.w21/check-result.py /opt/b.w21/job-cases.json "$slug" "$log" "$rc"
