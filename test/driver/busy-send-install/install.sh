#!/usr/bin/env bash
# Install-only composition of the existing busy-send hook and stand-in fixtures.
set -Eeuo pipefail
trap 'echo "INSTALL_CASE_FAILURE line=$LINENO command=$BASH_COMMAND" >&2' ERR
driver=/opt/driver/busy-send-install
if [ "${1:-}" != inside ]; then
    build="$(node "$driver/build.cjs")"
    case_root="$(mktemp -d /tmp/bsi-case-XXXXXX)"
    mkdir -p "$case_root/home" "$case_root/tmux"
    trap 'rm -rf "$case_root"' EXIT
    HOME="$case_root/home" TMUX_TMPDIR="$case_root/tmux" timeout --kill-after=5s 240s bash "$0" inside "$1" "$build"
    exit
fi
mode="$2"; build="$3"
unset CODEX_HOME ANTHROPIC_API_KEY CLAUDE_CODE_OAUTH_TOKEN
export PATH="/opt/driver/stand-in-claude:/opt/driver/stand-in-codex:/opt/driver/delay-create-tmux:$PATH"
export DELAY_CREATE_SECONDS=0
root="$HOME/.config/agent-director"
installer=/opt/skills/install-agent-director/install.sh
# The no-layout install is completed before any runtime client or agent starts.
bash "$installer" --binary /usr/local/bin/agent-director --no-hooks --no-symlink >"$HOME/exempt.out" 2>"$HOME/exempt.err" \
    || { cat "$HOME/exempt.out" "$HOME/exempt.err"; exit 1; }
export PATH="$root/bin:$PATH"
. /opt/driver/busy-send-claude.sh "install-$mode"
client_pid=""; install_pid=""; admin_pid=""
cleanup() {
    status=$?
    trap - EXIT
    # /out is the scheduler's disposable result volume. Preserve only this
    # synthetic case's regular evidence files, never executables or credentials.
    evidence="/tmp/busy-send-install-artifacts/$mode-$tag"
    if [ -d /out ] && [ -w /out ]; then evidence="/out/busy-send-install-$mode-$tag"; fi
    mkdir -p "$evidence"
    find "$work" -maxdepth 1 -type f -exec cp -t "$evidence" -- {} +
    printf 'exit=%s\n' "$status" >"$evidence/status.txt"
    if [ "$status" -ne 0 ]; then
        for f in "$work"/*.err "$work"/install*.out "$work"/callers.out; do
            [ ! -f "$f" ] || { echo "EVIDENCE $f"; cat "$f"; }
        done
    fi
    [ -z "$admin_pid" ] || kill -KILL "$admin_pid" 2>/dev/null || true
    for p in "$client_pid" "$install_pid"; do
        [ -z "$p" ] || kill -TERM "$p" 2>/dev/null || true
        [ -z "$p" ] || wait "$p" 2>/dev/null || true
    done
    # This case's TMUX_TMPDIR is private. Ending it reaps the three stand-ins
    # and any failed relay-hook child; it never sees the harness's server.
    tmux kill-server 2>/dev/null || true
    rm -rf "$work"
    exit "$status"
}
trap cleanup EXIT
printf '[store]\nbusy_timeout_ms = 50\n[launch]\ncodex_path = "/opt/driver/stand-in-codex/codex"\n[relay]\ntimeout_seconds = 30\n' >"$root/config.toml"
cp "$root/config.toml" "$work/config.expected"
initial="$(agent-director version | jq -r .version)"
initial_path="$(readlink -f "$root/bin/agent-director")"
admin="$root/admin/agent-director-admin"
later=9.0.0-kx-instrumented
refused=9.0.0-kx-refused
uid="install-codex-$uuid"; rid="install-relay-$uuid"
mkdir -p "$work/codex" "$work/records/$uid"
export AD_CODEX_RECORD_DIR="$work/records"
agent-director spawn --agent claude --cwd "$work" --instance-id "$id" --tmux-session-name "cd-install-c-$tag" >/dev/null
launched spawn startup
quiet UserPromptSubmit '{"prompt":"hold the eligible delivery boundary during installation"}'
agent-director spawn --agent codex --cwd "$work" --instance-id "$uid" --tmux-session-name "cd-install-u-$tag" --extra-env "CODEX_HOME=$work/codex" --extra-env "AD_CODEX_RECORD_DIR=$work/records" >/dev/null
node "$driver/wait.cjs" file "$work/records/$uid/pid" >/dev/null
usid="$(cat /proc/sys/kernel/random/uuid)"
rollout="$work/codex/sessions/2026/11/17/rollout-2026-11-17T00-00-00-$usid.jsonl"
mkdir -p "$(dirname "$rollout")"
printf '{"type":"session_meta"}\n' >"$rollout"
/opt/driver/pane-hook.sh --agent codex "$uid" "$(jq -cn --arg s "$usid" --arg p "$rollout" --arg w "$work" '{hook_event_name:"SessionStart",session_id:$s,transcript_path:$p,cwd:$w,source:"startup",model:"codex-stand-in"}')" >/dev/null
agent-director get --instance-id "$uid" | jq -e '.state == "waiting" and .busy_send == "not_covered"' >/dev/null
agent-director spawn --agent claude --cwd "$work" --instance-id "$rid" --relay-mode on --tmux-session-name "cd-install-r-$tag" >/dev/null
rsid="$(cat /proc/sys/kernel/random/uuid)"
printf '{}\n' >"$work/$rsid.jsonl"
relay_payload() { jq -cn --arg e "$1" --arg s "$rsid" --arg p "$work/$rsid.jsonl" --arg w "$work" '{hook_event_name:$e,session_id:$s,transcript_path:$p,cwd:$w,source:"startup",model:"claude-stand-in",tool_name:"Bash",tool_input:{command:"true"}}'; }
/opt/driver/pane-hook.sh --stderr-pipe "$rid" "$(relay_payload SessionStart)" >/dev/null
agent-director get --instance-id "$rid" | jq -e '.state == "waiting"' >/dev/null

# MCP and the real Go Client are constructed before the no-install control.
# They remain alive across staging, rename, process death, recovery and rollback.
coproc CALLERS { timeout --kill-after=3s 200s bun "$driver/callers.ts" "$build" "$work" "$id" "$uid" "$caller" 2>"$work/callers.err"; }
client_pid="$CALLERS_PID"
client_in="${CALLERS[1]}"; client_out="${CALLERS[0]}"
read -r -t 20 clients_ready <&"$client_out" || fail 'callers did not initialize'
printf '%s\n' "$clients_ready" >"$work/clients.json"
jq -e '.ready == true' "$work/clients.json" >/dev/null
printf '%s\n' "$clients_ready"

phase() {
    local phase="$1" token relay_job started elapsed out
    cmp "$work/config.expected" "$root/config.toml" || fail 'installer changed caller settings/budgets'
    # Exactly the same supported zero-wait decision in control and overlap.
    relay_job="$(/opt/driver/pane-hook.sh --no-wait "$rid" "$(relay_payload PermissionRequest)")"
    token="$(node "$driver/wait.cjs" sql "$db" "SELECT request_token FROM permission_requests WHERE instance_id='$rid' AND decision IS NULL ORDER BY request_id DESC LIMIT 1;")"
    started="$(date +%s%N)"
    agent-director decide --instance-id "$rid" --request-token "$token" --decision allow --max-wait-ms 0 >"$work/decide-$phase.json"
    elapsed=$(( ($(date +%s%N)-started)/1000000 ))
    [ "$elapsed" -lt 2000 ] || fail "zero-wait decide exceeded 1s product allowance plus 1s process overhead: $elapsed ms"
    /opt/driver/pane-hook.sh --wait "$relay_job" >"$work/ack-$phase.json"
    jq -e '. == {hookSpecificOutput:{hookEventName:"PermissionRequest",decision:{behavior:"allow"}}}' "$work/ack-$phase.json" >/dev/null
    agent-director get-permission --request-token "$token" >"$work/permission-$phase.json"
    jq -e '.decision == "allow" and .delivery == "delivered"' "$work/permission-$phase.json" >/dev/null
    printf 'DECIDE phase=%s max_wait_ms=0 elapsed_ms=%s acknowledgement=delivered\n' "$phase" "$elapsed" | tee -a "$work/events.log"
    /opt/driver/pane-hook.sh --stderr-pipe "$rid" "$(relay_payload Stop)" >/dev/null
    printf '{"phase":"%s"}\n' "$phase" >&"$client_in"
    read -r -t 45 out <&"$client_out" || fail "callers failed in $phase"
    printf '%s\n' "$out" | tee -a "$work/callers.out"
    jq -e --arg p "$phase" '.phase == $p and (.rows|length)==8' <<<"$out" >/dev/null
    jq -c '.rows[] | select(.target=="covered")' <<<"$out" >>"$work/accepted.jsonl"
    for surface in cli mcp go ts; do node "$driver/wait.cjs" file "$work/fence-$phase-$surface" >/dev/null; printf '%s-%s\n' "$phase" "$surface" >>"$work/uncovered.want"; done
    cmp "$work/uncovered.want" "$work/uncovered" || fail 'uncovered input differed or was duplicated'
    agent-director get --instance-id "$id" | jq -e '.state=="working" and .busy_send=="covered"' >/dev/null
    agent-director version | jq -e --arg a "$initial" --arg b "$later" '.version==$a or .version==$b' >/dev/null
    "$initial_path" version | jq -e --arg a "$initial" '.version==$a' >/dev/null
}

# An admin-only window has idle clients and no eligible delivery event. A
# logical dump is a no-mutation observation, not the T2 attempted-open proof.
observe() {
    /opt/driver/sql.sh -readonly "$db" .dump >"$work/$1.db"
    cp "$TMUX_CALL_LOG" "$work/$1.tmux-all"
    # Installer preflight asks only tmux's version. Retain its raw trace;
    # every other call (including read/coordination calls) stays forbidden.
    awk -F'\t' '!($1=="bash" && $2=="-V ")' "$TMUX_CALL_LOG" >"$work/$1.tmux"
    /opt/driver/sql.sh -readonly "$db" 'SELECT wake_path FROM busy_send_launches WHERE wake_path IS NOT NULL ORDER BY wake_path;' |
        while IFS= read -r p; do [ ! -e "$p" ] || stat -c '%n %i %s %y' "$p"; done >"$work/$1.wakes"
    /opt/driver/sql.sh -readonly "$db" 'SELECT pane_pid FROM spawns ORDER BY instance_id;' |
        while read -r p; do kill -0 "$p"; awk '{print $1,$22}' "/proc/$p/stat"; done >"$work/$1.processes"
}
unchanged() {
    observe "$2"
    for ext in db tmux wakes processes; do cmp "$work/$1.$ext" "$work/$2.$ext" || fail "admin-only $2 changed $ext"; done
    echo "ADMIN_ONLY $2 no_runtime_mutation no_tmux_input_or_control no_wake same_live_processes" | tee -a "$work/events.log"
}
install_later() { bash "$installer" --binary "$build/later/agent-director" --admin-binary "$build/later/agent-director-admin" --no-hooks --no-symlink; }
selection() {
    local want="$1"
    [ "$(readlink "$root/bin/agent-director")" = ../current/agent-director ] || fail 'runtime permanent entry changed'
    [ "$(readlink "$root/admin/agent-director-admin")" = ../current/agent-director-admin ] || fail 'admin permanent entry changed'
    agent-director version | jq -e --arg v "$want" '.version==$v' >/dev/null
    "$admin" version-facts | jq -e --arg v "$want" '.version==$v' >/dev/null
    "$initial_path" version | jq -e --arg v "$initial" '.version==$v' >/dev/null
}
phase control
if [ "$mode" = load ]; then point=before:stage.agent-director.file-sync; else point=after:current.rename; fi
mkfifo "$work/release"
jq -n --arg p "$point" --arg r "$work/ready" --arg f "$work/release" '{Point:$p,Ready:$r,Release:$f}' >"$root/.install-test-barrier.json"
observe before
install_later >"$work/install.out" 2>"$work/install.err" & install_pid=$!
node "$driver/wait.cjs" file "$work/ready" >/dev/null
admin_pid="$(jq -r .pid "$work/ready")"
kill -0 "$admin_pid"
jq -e --arg p "$point" '.point==$p' "$work/ready" >/dev/null
unchanged before held
echo "BARRIER $point admin_pid=$admin_pid installer_pid=$install_pid" | tee -a "$work/events.log"
if [ "$mode" = load ]; then selection "$initial"; else selection "$later"; fi
phase held
kill -0 "$admin_pid" # All eight runtime calls completed while this admin still held the barrier.
observe after_calls
if [ "$mode" = load ]; then
    printf G >"$work/release"
    wait "$install_pid" || fail 'later install failed'
    install_pid=""; admin_pid=""
    unchanged after_calls published
else
    kill -KILL "$admin_pid"; admin_pid=""
    code=0; wait "$install_pid" || code=$?; install_pid=""
    [ "$code" -eq 5 ] || fail "interrupted installer exit=$code; want 5"
    grep -q 'publication_state.*unknown' "$work/install.err" || fail 'installer lost unknown outcome'
    unchanged after_calls crashed
    intents=("$root/publications/"*.intent.json)
    interrupted_intent="${intents[${#intents[@]}-1]}"
    interrupted_completion="${interrupted_intent%.intent.json}.complete.json"
    later_id="$(printf %s "$later" | sha256sum | cut -d' ' -f1)"
    jq -e --arg v "$later_id" '.operation=="publish" and .version_id==$v' "$interrupted_intent" >/dev/null
    cp "$interrupted_intent" "$work/interrupted-intent.json"
    [ ! -e "$interrupted_completion" ] || fail 'crash did not leave the publication incomplete'
    selection "$later"
    phase dead
    rm "$root/.install-test-barrier.json"
    observe recovery_before
    install_later >"$work/install-rerun.out" 2>"$work/install-rerun.err" || fail 'same-request forward rerun failed'
    printf '  version : selected="%s" reused=true recovered_version="%s" publication_state=published durable=true initialization_complete=true\n' \
        "$later" "$later" >"$work/rerun-summary.want"
    grep '^  version :' "$work/install-rerun.out" >"$work/rerun-summary"
    cmp "$work/rerun-summary.want" "$work/rerun-summary" || fail 'forward rerun publication summary differs'
    # Verify the interrupted intent itself is completed before rollback could
    # recover it. Bind the existing completion format to the original bytes.
    cmp "$work/interrupted-intent.json" "$interrupted_intent" || fail 'forward recovery changed its intent'
    cp "$interrupted_completion" "$work/recovered-completion.json"
    intent_digest="$(sha256sum "$work/interrupted-intent.json" | cut -d' ' -f1)"
    jq -e --slurpfile intent "$work/interrupted-intent.json" --arg digest "$intent_digest" \
        '. == {format_version:$intent[0].format_version,sequence:$intent[0].sequence,intent_sha256:$digest}' \
        "$work/recovered-completion.json" >/dev/null || fail 'forward recovery completion does not bind its intent'
    echo "FORWARD_RECOVERY exact_summary completed_intent=$intent_digest before_rollback" | tee -a "$work/events.log"
    unchanged recovery_before recovered
fi
rm -f "$root/.install-test-barrier.json"
selection "$later"
phase published
if [ "$mode" = crash ]; then
    later_path="$(readlink -f "$root/bin/agent-director")"
    /opt/bin/agent-director-admin --home "$HOME" list-versions >"$work/versions-before.json"
    jq -e --arg a "$initial" --arg b "$later" '([.versions[].version]|sort)==([$a,$b]|sort)' "$work/versions-before.json" >/dev/null
    find "$root/publications" -type f -exec sha256sum {} + >"$work/history.sha256"
    # The unmodified shipped admin executes the documented rollback command.
    observe rollback_before
    /opt/bin/agent-director-admin --home "$HOME" rollback-version --version "$initial" >"$work/rollback.json"
    jq -e --arg v "$initial" '.published_version==$v and .durable==true and .publication_state=="published"' "$work/rollback.json" >/dev/null
    unchanged rollback_before rolled_back
    selection "$initial"
    observe refusal_before
    code=0
    bash "$installer" --binary "$build/refused/agent-director" --admin-binary "$build/refused/agent-director-admin" --no-hooks --no-symlink >"$work/install-refused.out" 2>"$work/install-refused.err" || code=$?
    [ "$code" -eq 5 ] || fail "shrinking-coverage install exit=$code; want 5"
    awk '/^\{/{print}' "$work/install-refused.err" | jq -e '.err_name=="ErrVersionInstallRefused" and .details.reason=="coverage_regression"' >/dev/null
    unchanged refusal_before refused
    selection "$initial"
    rejected_id="$(printf %s "$refused" | sha256sum | cut -d' ' -f1)"
    [ ! -e "$root/versions/$rejected_id/ready" ] || fail 'refused candidate became ready'
    phase rolledback
    /opt/bin/agent-director-admin --home "$HOME" list-versions >"$work/versions-after.json"
    jq -e --slurpfile before "$work/versions-before.json" --arg v "$initial" \
        '.published_version==$v and .versions==$before[0].versions' "$work/versions-after.json" >/dev/null
    "$later_path" version | jq -e --arg v "$later" '.version==$v' >/dev/null
    sha256sum --check "$work/history.sha256"
fi

# Compare admission facts against caller results before supplying ANY delivery
# event; then assert whole bodies, accepted order, exact qualified Results and
# no replay using the shared owned Stop/FileChanged/UserPromptSubmit protocol.
covered_pane="$(/opt/driver/sql.sh -readonly "$db" "SELECT pane_id FROM spawns WHERE instance_id='$id';")"
typed="$(awk -F'\t' '$1=="agent-director" || $1=="callers" {print $2}' "$TMUX_CALL_LOG" | grep -E '(send-keys|paste-buffer|load-buffer|set-buffer) ' | grep -F -- "-t $covered_pane " || true)"
[ -z "$typed" ] || fail "runtime typed into covered pane: $typed"
agent-director read-pane --instance-id "$id" --n-lines 200 >"$work/covered-pane.json"
! grep -q 'whole message' "$work/covered-pane.json" || fail 'covered Message reached the pane as typed input'
/opt/driver/sql.sh -readonly -json "$db" "SELECT m.accepted_seq,CAST(m.text AS TEXT) AS text,r.delivery_request_id AS request_id FROM busy_send_messages m JOIN busy_send_results r USING(message_id) WHERE m.instance_id='$id' ORDER BY m.accepted_seq;" >"$work/messages.json"
node - "$work" <<'NODE'
const fs=require('node:fs'),assert=require('node:assert/strict'),w=process.argv[2];
const expected=fs.readFileSync(w+'/accepted.jsonl','utf8').trim().split('\n').map(JSON.parse);
const actual=JSON.parse(fs.readFileSync(w+'/messages.json','utf8'));
assert.deepEqual(actual.map(x=>({text:x.text,request_id:x.request_id})),expected.map(x=>({text:x.text,request_id:x.request_id})));
assert.equal(new Set(actual.map(x=>x.accepted_seq)).size,actual.length);
const prompt=expected.map((x,i)=>`[agent-director: message ${i+1} of ${expected.length}]\n${x.text}`).join('\n\n');
assert(Buffer.byteLength(prompt)<9600);fs.writeFileSync(w+'/expected-prompt',prompt);
NODE
quiet Stop "$stop"
wakes "$(cat "$work/expected-prompt")"
quiet FileChanged "$changed"
while IFS= read -r message; do
    request="$(jq -r .request_id <<<"$message")"
    [ "$(result "$request")" = "$raced" ] || fail "wrong final Result for $request"
done <"$work/accepted.jsonl"
sighting
quiet Stop "$stop"
quiet FileChanged "$changed"
[ "$(delivery .held_count)" = '[0]' ] || fail 'accepted Message was left undelivered'
echo "DELIVERY $mode accepted=$(wc -l <"$work/accepted.jsonl") whole_ordered_once result=sent_unconfirmed/wait_turn_raced synthetic_owned_hook_sighting" | tee -a "$work/events.log"
exec {client_in}>&-
wait "$client_pid" || fail 'persistent caller cleanup failed'
client_pid=""
echo "busy-send-install-$mode PASS"
