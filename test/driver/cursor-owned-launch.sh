#!/usr/bin/env bash
# Shared isolated synthetic ACP setup; shared by cursor-owned-launch and cursor-relay cases.
set -euo pipefail
[[ ${AGENT_DIRECTOR_TEST_HARNESS:-} == 1 ]] || { echo 'Docker harness required' >&2; return 2; }
[[ $(uname -s) == Linux ]] || { echo 'Linux lifetime proof required; other platforms pending' >&2; return 2; }
for dependency in node bun tmux jq; do command -v "$dependency" >/dev/null; done
SOURCE_DIR=/work/source
[[ -r $SOURCE_DIR/internal/testsupport/cursorfix/runtime.js ]]
case_dir=$(mktemp -d /tmp/co-XXXXXX)
export HOME="$case_dir/home" TMUX_TMPDIR="$case_dir/tmux" CURSOR_CONFIG_DIR="$case_dir/cursor-config" CURSOR_DATA_DIR="$case_dir/cursor-data" TMPDIR="$case_dir/tmp"
unset TMUX TMUX_PANE AGENT_DIRECTOR_INSTANCE_ID AGENT_DIRECTOR_AGENT AGENT_DIRECTOR_CURSOR_STORE_PATH
mkdir -m 700 -p "$HOME/.config/agent-director" "$TMUX_TMPDIR" "$case_dir/cwd" "$CURSOR_CONFIG_DIR" "$CURSOR_DATA_DIR" "$TMPDIR"
STORE="$case_dir/override.db"
id="co-$(node -e 'console.log(require("crypto").randomUUID())')"
export AD_CURSOR_FIX_SETTINGS
AD_CURSOR_FIX_SETTINGS=$(jq -cn --arg dir "$case_dir" '{trace:($dir+"/trace.jsonl"),pid:($dir+"/runtime-pid"),release:($dir+"/release"),worker:false,workerPID:($dir+"/worker-pid"),workerRelease:($dir+"/worker-release"),scenario:($dir+"/scenario"),raw:($dir+"/raw"),observer:($dir+"/observer"),sent:($dir+"/sent.jsonl"),paused:($dir+"/paused")}')
export AD_CURSOR_FIX_SOURCE="$SOURCE_DIR/internal/testsupport/cursorfix/runtime.js"
cat > "$case_dir/index.js" <<'RUNTIME'
#!/bin/sh
exec node -e 'global.SETTINGS=JSON.parse(process.env.AD_CURSOR_FIX_SETTINGS);require(process.env.AD_CURSOR_FIX_SOURCE)' -- "$@"
RUNTIME
chmod 700 "$case_dir/index.js"
cat > "$HOME/.config/agent-director/config.toml" <<CONFIG
[defaults]
disable_ask_user = ${CURSOR_DISABLE_QUESTIONS:-false}
[relay]
timeout_seconds = ${CURSOR_RELAY_TIMEOUT:-20}
[launch]
cursor_path = "$case_dir/index.js"
[store]
db_path = "$case_dir/configured.db"
[pause]
timeout_seconds = 2
CONFIG
socket="$TMUX_TMPDIR/tmux-$(id -u)/default"
sleep 300 &
control_sentinel=$!
cleanup_cursor_case() {
 local rc=$? command=$BASH_COMMAND
 if (( rc != 0 )); then
  printf 'CASE_EXIT exit=%s command=%q context=%s\n' "$rc" "$command" "mode=${mode:-} relay=${relay:-} disabled=${disabled:-} seam=${seam:-} shape=${shape:-} case_dir=$case_dir" >&2
  # Read only this synthetic case's evidence before killing its processes.
  if declare -F sql >/dev/null; then
   sql -readonly -json "SELECT state,session_id,pane_pid FROM spawns WHERE instance_id='$id'; SELECT request_token,tool_use_id,decision,settled_at,delivered_at,proven_gone_at FROM permission_requests WHERE instance_id='$id';" >&2 || true
  fi
  for artifact in scenario trace.jsonl permission.json error pause-error; do
   if [[ -f $case_dir/$artifact ]]; then
    printf 'CASE_ARTIFACT %s\n' "$artifact" >&2
    cat "$case_dir/$artifact" >&2 || true
   fi
  done
 fi
 touch "$case_dir/gate-release"
 tmux -S "$socket" kill-server >/dev/null 2>&1 || true
 kill "$control_sentinel" >/dev/null 2>&1 || true
 wait "$control_sentinel" 2>/dev/null || true
 if [[ -n ${pipe_observer:-} ]]; then kill "$pipe_observer" 2>/dev/null || true; wait "$pipe_observer" 2>/dev/null || true; fi
 if [[ -n ${lock_owner:-} ]]; then touch "$case_dir/unlock"; kill "$lock_owner" 2>/dev/null || true; wait "$lock_owner" 2>/dev/null || true; fi
}
trap cleanup_cursor_case EXIT
ad() { local verb=$1; shift; agent-director "$verb" --home "$HOME" --store-path "$STORE" "$@"; }
sql() { /opt/driver/sql.sh "$STORE" "$@"; }
row_state() { sql "SELECT state FROM spawns WHERE instance_id='$id';"; }
wait_state() { local want=$1 deadline=$((SECONDS+15)); while [[ $(row_state) != "$want" ]]; do (( SECONDS < deadline )); sleep 0.05; done; }
load_identity() {
 gateway=$(sql "SELECT pane_pid FROM spawns WHERE instance_id='$id';")
 pane=$(sql "SELECT pane_id FROM spawns WHERE instance_id='$id';")
 runtime=$(sql "SELECT bound_runtime_pid FROM cursor_runtimes WHERE bound_instance_id='$id';")
 session=$(sql "SELECT session_id FROM spawns WHERE instance_id='$id';")
 [[ $gateway =~ ^[0-9]+$ && $runtime =~ ^[0-9]+$ && $pane =~ ^%[0-9]+$ && -n $session ]]
 [[ $(sql "SELECT count(*) FROM spawns s JOIN cursor_runtimes c ON c.bound_instance_id=s.instance_id WHERE s.instance_id='$id' AND s.agent='cursor' AND c.bound_launch_token=s.launch_token AND c.bound_life_number=s.life_number AND c.bound_gateway_pid=s.pane_pid AND c.bound_gateway_start=s.pane_starttime AND c.bound_session_id=s.session_id;") == 1 ]]
}
proc_gone() {
 node -e 'const fs=require("fs");try{const s=fs.readFileSync("/proc/"+process.argv[1]+"/stat","utf8");process.exit(s.slice(s.lastIndexOf(")")+2).startsWith("Z ")?0:1)}catch(e){if(e.code==="ENOENT")process.exit(0);throw e}' "$1"
}
wait_gone() { local pid=$1 deadline=$((SECONDS+15)); until proc_gone "$pid"; do (( SECONDS < deadline )); sleep 0.05; done; }
spawn_cursor() { ad spawn --agent cursor --instance-id "$id" --cwd "$case_dir/cwd" --no-pre-trust "$@" | jq -e --arg id "$id" '.instance_id==$id and .pre_trust=="skipped"' >/dev/null; }
frames() { if [[ -f $case_dir/trace.jsonl ]]; then cat "$case_dir/trace.jsonl"; fi; }
evidence() {
 jq -cn --arg case "$1" --arg assertions "$2" '{case:$case,status:"pass",checks:$assertions,authenticated_cursor:false}'
}

# Shared relay actions. Only public callers create/admit requests; SQL is observation/fault injection.
wait_until() {
 local deadline=$((SECONDS+20))
 until "$@"; do
  if (( SECONDS >= deadline )); then
   printf 'WAIT_TIMEOUT case=%s stage=%s predicate=' "${id:-}" "${check_id:-}" >&2
   printf '%q ' "$@" >&2
   printf '\n' >&2
   return 1
  fi
  sleep 0.02
 done
}
request_count_is() { [[ $(sql "SELECT count(*) FROM permission_requests WHERE instance_id='$id';") == "$1" ]]; }
request_token() { sql "SELECT request_token FROM permission_requests WHERE instance_id='$id' AND tool_use_id='$1';"; }
raw_size() { if [[ -f $case_dir/raw ]]; then wc -c < "$case_dir/raw"; else echo 0; fi; }
reply_count_is() { [[ $(frames | jq -sc '[.[]|select(has("method")|not)]|length') == "$1" ]]; }
delivery_is() { ad get-permission --request-token "$1" | jq -e --arg want "$2" '.delivery==$want' >/dev/null; }
relay_start() {
 spawn_cursor --relay-mode "${1:-on}"
 wait_state waiting
 load_identity
 ad send-keys --instance-id "$id" --text 'scripted relay acceptance' >/dev/null
}
relay_observe() {
 cat > "$case_dir/observer" <<OBSERVER
#!/bin/bash
set -euo pipefail
/opt/driver/sql.sh -readonly -json '$STORE' 'SELECT request_token,decision,delivered_at FROM permission_requests ORDER BY request_id;' | jq -c 'select(length>0)' >> '$case_dir/commits.jsonl'
OBSERVER
 chmod 700 "$case_dir/observer"
}
relay_fault_ack() {
 sql "CREATE TRIGGER refuse_ack BEFORE UPDATE OF delivered_at ON permission_requests WHEN NEW.delivered_at IS NOT NULL BEGIN SELECT RAISE(ABORT,'collector ack fault'); END;"
}
relay_decide() { ad decide --instance-id "$id" --request-token "$1" --decision "$2" --max-wait-ms 1000; }
relay_guard() {
 local before
 before=$(frames | jq -sc '[.[]|select(.method=="session/prompt")]|length')
 ad send-keys --instance-id "$id" --text 'held prompt must not arrive' >"$case_dir/held-out" 2>"$case_dir/held-error" || jq -e '.err_name=="ErrSendKeysWhileRelayed" or .err_name=="ErrRelayFallenBack" or .err_name=="ErrDialogMaybeOpen"' "$case_dir/held-error" >/dev/null
 # Pause must refuse, independently of whether tmux accepted the ordinary keys.
 if ad pause --instance-id "$id" >"$case_dir/pause-out" 2>"$case_dir/pause-error"; then return 1; fi
 jq -e '.err_name=="ErrSpawnNotPausable" or .err_name=="ErrSendKeysWhileRelayed" or .err_name=="ErrRelayFallenBack" or .err_name=="ErrDialogMaybeOpen"' "$case_dir/pause-error" >/dev/null
 [[ $(frames | jq -sc '[.[]|select(.method=="session/prompt")]|length') == "$before" ]]
 kill -0 "$control_sentinel"
}
relay_receipt() {
 local case_id=$1 checks=$2
 kill -0 "$control_sentinel"
 jq -cn --arg case "$case_id" --argjson checks "$checks" --arg id "$id" --arg session "$session" --argjson gateway "$gateway" --argjson runtime "$runtime" --argjson sentinel "$control_sentinel" \
 --arg mode "${mode:-}" --arg verdict "${verdict:-}" --arg fault "${fault:-}" --arg relay "${relay:-}" --arg disabled "${disabled:-}" --arg cut "${cut:-}" --arg seam "${seam:-}" \
 '{case:$case,status:"pass",checks:$checks,evidence:"scripted-ACP-through-shipped-processes",authenticated_cursor:false,facts:{instance_id:$id,session_id:$session,gateway_pid:$gateway,runtime_pid:$runtime,control_sentinel_pid:$sentinel,parameters:{mode:$mode,verdict:$verdict,fault:$fault,relay:$relay,disable_ask_user:$disabled,cut:$cut,seam:$seam}}}'
}
relay_scenario() {
 node - "$SOURCE_DIR/internal/testsupport/cursorfix/testdata/authorizations.json" "$case_dir/scenario" "$@" <<'SCENARIO'
const fs=require('fs');
const [capturePath,dest,mode='permission']=process.argv.slice(2);
const captures=JSON.parse(fs.readFileSync(capturePath));
const execute=captures.find(c=>c.name==='execute');
const session=execute.params.sessionId;
const frames=[];
function add(id,method,params){frames.push({jsonrpc:'2.0',id,method,params});}
if(mode==='captured' || mode==='coverage') for(const c of captures) add(c.name,c.method,c.params);
else if(mode==='plan') {
 const plan=captures.find(c=>c.name==='plan');add('permission',execute.method,execute.params);add('plan',plan.method,plan.params);
} else if(mode==='questions') {
 add('permission',execute.method,execute.params);
 const questions=JSON.parse(fs.readFileSync(capturePath.replace('authorizations.json','questions.json')));
 for(const q of questions){const p=structuredClone(q.params);if(p.sessionId==='SOURCE_DERIVED_SESSION')p.sessionId=session;add(q.id,q.method,p);}
} else if(mode==='direct-malformed') add('question-direct','cursor/ask_question',{questions:null,multiSelect:'ambiguous'});
else if(mode==='persistent-fallback' || mode==='ambiguous') {
 const p=structuredClone(execute.params);p.toolCall.title= mode==='ambiguous' ? 'Unvalidated authorization' : 'AskUserQuestion';p.toolCall.kind='other';p.toolCall.rawInput={questions:[{question:'synthetic'}]};p.options=p.options.filter(o=>o.kind==='allow_always');add('question-fallback',execute.method,p);
} else if(mode==='unsupported') add('unknown','cursor/unknown_authorization',{});
else add('permission',execute.method,execute.params);
if(mode==='coverage') for(const kind of ['read','delete','fetch']) {
 const p=structuredClone(execute.params);p.toolCall={...p.toolCall,toolCallId:'synthetic-'+kind,title:'Synthetic '+kind,kind,rawInput:null};add('synthetic-'+kind,execute.method,p);
}
// Captures came from independent sessions; rebind only the synthetic session
// identity to this connection while retaining each original authorization body.
for (const frame of frames) if ('sessionId' in frame.params) frame.params.sessionId=session;
// An unsupported authorization's cancellation must remain pending. An ordinary
// end_turn from this peer would cancel the supervisor's cancellation deadline.
fs.writeFileSync(dest,JSON.stringify({sessionId:session,requests:frames,...(mode==='unsupported'?{waitForRelease:true}:{})}));
SCENARIO
}
relay_retain_pipe() {
 cc -Wall -Wextra -Werror -o "$case_dir/pipe-observer" "$SOURCE_DIR/test/driver/cursor-pipe.c"
 "$case_dir/pipe-observer" "$runtime" "$case_dir/pipe-ready" "$case_dir/fill-pipe" "$case_dir/drain-pipe" "$case_dir/pipe-raw" &
 pipe_observer=$!
 wait_until test -f "$case_dir/pipe-ready"
}
relay_gate_after_ack() {
 gate_check=${1:-first}
 real_tmux=$(command -v tmux)
 mkdir -m 700 "$case_dir/gate-bin"
 cat > "$case_dir/gate-bin/tmux" <<GATE
#!/bin/bash
set -euo pipefail
if [[ -f '$case_dir/gate-armed' ]] && [[ \$(/opt/driver/sql.sh -readonly '$STORE' 'SELECT count(*) FROM permission_requests WHERE delivered_at IS NOT NULL;' 2>/dev/null || echo 0) != 0 ]]; then
 if [[ '$gate_check' == final ]]; then
  # Each CheckCursorAnswer ends in one list-panes. Allow the worker's
  # initial guard; hold the second guard running under the transport timer.
  if [[ " \$* " != *' list-panes '* ]]; then exec '$real_tmux' "\$@"; fi
  if [[ ! -f '$case_dir/gate-first-check' ]]; then
   touch '$case_dir/gate-first-check'
   exec '$real_tmux' "\$@"
  fi
 fi
 touch '$case_dir/gate-ready'
 while [[ ! -f '$case_dir/gate-release' ]]; do sleep 0.01; done
fi
exec '$real_tmux' "\$@"
GATE
 chmod 700 "$case_dir/gate-bin/tmux"
 export PATH="$case_dir/gate-bin:$PATH"
}
