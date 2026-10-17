#!/usr/bin/env bash
# Task 1 synthetic selection coverage; the scheduler owns execution.
if [[ ${CG_CASE_GUARDED:-} != 1 ]]; then
 export CG_CASE_GUARDED=1
 cg_guard_rc=0
 cg_guard_started=$SECONDS
 cg_guard_flags=()
 [[ $- != *x* ]] || cg_guard_flags+=(-x)
 if [[ -n ${BASH_EXECUTION_STRING:-} ]]; then cg_guard_command=(bash "${cg_guard_flags[@]}" -c "$BASH_EXECUTION_STRING"); else cg_guard_command=(bash "${cg_guard_flags[@]}" "$0" "$@"); fi
 timeout --kill-after=3s 240s bash -c 'trap "sleep 4" TERM; "$@" & child=$!; wait "$child"' cg-case-guard "${cg_guard_command[@]}" || cg_guard_rc=$?
 if (( cg_guard_rc == 124 || cg_guard_rc == 137 )); then printf 'CASE_GUARD_EXIT collector=cursor-groups limit_seconds=240 elapsed_seconds=%s exit=%s deadline_reached=%s\n' "$((SECONDS-cg_guard_started))" "$cg_guard_rc" "$((SECONDS-cg_guard_started>=240))" >&2; fi
 exit "$cg_guard_rc"
fi
source /work/source/test/driver/cursor-resume-life.sh
export GIT_CONFIG_GLOBAL="$case_dir/gitconfig" GIT_CONFIG_NOSYSTEM=1
: > "$GIT_CONFIG_GLOBAL"

cg_selection() {
 jq -e --arg config "$1" --arg data "$2" '.CURSOR_CONFIG_DIR==(if $config=="" then null else $config end) and .CURSOR_DATA_DIR==(if $data=="" then null else $data end) and .CURSOR_API_KEY==null and .AGENT_CLI_CREDENTIAL_STORE==null' "$case_dir/runtime-pid.selection.json" >/dev/null
}
cg_finish() {
 local artifacts="/tmp/cursor-groups-artifacts/$id"
 mkdir -m 700 -p "$artifacts"
 for item in runtime-pid.selection.json trace.jsonl scenario saved.json history.json error public.json resume.json original-before.json retained-sha256 protected-before; do if [[ -f $case_dir/$item ]]; then cp "$case_dir/$item" "$artifacts/$item"; fi; done
 sql -readonly -json "SELECT instance_id,agent,state,session_id,life_number,extra_env FROM spawns; SELECT * FROM session_history;" > "$artifacts/authority.jsonl"
 evidence "$1" "$2"
}
cg_unconfigured() {
 local server=$1 key
 export CURSOR_CONFIG_DIR=/synthetic/client-config CURSOR_DATA_DIR=/synthetic/client-data CURSOR_API_KEY=synthetic-client-key AGENT_CLI_CREDENTIAL_STORE=synthetic-client-store
 if [[ $server == existing ]]; then
  mkdir -m 700 -p "${socket%/*}"
  tmux -S "$socket" new-session -d -s unrelated 'sleep 300'
  for key in CURSOR_CONFIG_DIR CURSOR_DATA_DIR CURSOR_API_KEY AGENT_CLI_CREDENTIAL_STORE; do tmux -S "$socket" set-environment -g "$key" "synthetic-server-$key"; tmux -S "$socket" set-environment -t unrelated "$key" "synthetic-unrelated-$key"; tmux -S "$socket" show-environment -g "$key"; tmux -S "$socket" show-environment -t unrelated "$key"; done > "$case_dir/server-before"
 fi
 agent-director spawn --home "$HOME" --store-path "$STORE" --agent cursor --instance-id "$id" --cwd "$case_dir/cwd" --no-pre-trust > "$case_dir/public.json"
 check unconfigured.ready wait_until pc_start_ready
 check unconfigured.runtime cg_selection '' ''
 if [[ $server == existing ]]; then
  for key in CURSOR_CONFIG_DIR CURSOR_DATA_DIR CURSOR_API_KEY AGENT_CLI_CREDENTIAL_STORE; do tmux -S "$socket" show-environment -g "$key"; tmux -S "$socket" show-environment -t unrelated "$key"; done > "$case_dir/server-after"
  check server.unrelated-unchanged cmp "$case_dir/server-before" "$case_dir/server-after"
 fi
 cg_finish "unconfigured-$server" 'SR-2.2/SR-7.1 actual received absence; no account claim'
}
cg_explicit() {
 local surface=$1 selected_config="$case_dir/explicit config" selected_data="$case_dir/template-data"
 ad make-template --name selected --agent cursor --extra-env "CURSOR_CONFIG_DIR=$case_dir/template-config" --extra-env "CURSOR_DATA_DIR=$selected_data" >/dev/null
 export CURSOR_API_KEY=synthetic-client-key AGENT_CLI_CREDENTIAL_STORE=synthetic-client-store
 if [[ $surface == CLI ]]; then
  agent-director spawn --home "$HOME" --store-path "$STORE" --agent cursor --instance-id "$id" --cwd "$case_dir/cwd" --no-pre-trust --template selected --extra-env "CURSOR_CONFIG_DIR=$selected_config" > "$case_dir/public.json"
 else
  if [[ $surface == Bun ]]; then pc_install_bun; fi
  pc_public "$surface" spawn "$(jq -cn --arg id "$id" --arg cwd "$case_dir/cwd" --arg config "$selected_config" '{agent:"cursor",instance_id:$id,cwd:$cwd,no_pre_trust:true,template:"selected",extra_env:{CURSOR_CONFIG_DIR:$config}}')" > "$case_dir/public.json"
 fi
 check explicit.ready wait_until pc_start_ready
 check explicit.runtime cg_selection "$selected_config" "$selected_data"
 cg_finish "explicit-$surface" 'AC1/AC12 SR-2.2/SR-7.1 call/template exact selection through public surface'
}
cg_refused() {
 local key value before
 ad list >/dev/null
 before=$(sql -json 'SELECT * FROM spawns; SELECT * FROM session_history;')
 for key in CURSOR_API_KEY AGENT_CLI_CREDENTIAL_STORE; do
  for value in '' synthetic-secret-do-not-print; do
   if ad spawn --agent cursor --instance-id "$id" --cwd "$case_dir/cwd" --no-pre-trust --extra-env "$key=$value" > "$case_dir/refusal-out" 2> "$case_dir/error"; then echo 'profile accepted' >&2; return 1; fi
   check profile.named grep -q ErrReservedEnvKey "$case_dir/error"
   if grep -q synthetic-secret-do-not-print "$case_dir/refusal-out" "$case_dir/error"; then echo 'synthetic value leaked' >&2; return 1; fi
   check profile.no-runtime test ! -e "$case_dir/runtime-pid"
   check profile.no-protocol test ! -e "$case_dir/trace.jsonl"
   check profile.no-mutation test "$(sql -json 'SELECT * FROM spawns; SELECT * FROM session_history;')" = "$before"
  done
 done
 cg_finish profile-refusal 'AC12 SR-7.1 empty/incomplete profile refused before row/transport/runtime; sanitized'
}
cg_resume_reuse() {
 rl_fixture
 local saved_config=$CURSOR_CONFIG_DIR saved_data=$CURSOR_DATA_DIR
 export CURSOR_CONFIG_DIR="$case_dir/conflicting-caller-config" CURSOR_DATA_DIR="$case_dir/conflicting-caller-data" CURSOR_API_KEY=synthetic-client-key
 rl_resume CLI
 check saved.loaded wait_until rl_loaded
 check saved.runtime cg_selection "$saved_config" "$saved_data"
 rl_preserved
 # Loading keeps the row pending until its first real resumed turn completes.
 rl_send CLI 'saved selection resumed prompt' true >/dev/null
 check saved.completed wait_until pc_ready 1
 check saved.pause rl_public CLI pause >/dev/null
 check saved.stopped wait_gone "$resumed_gateway"
 ad spawn --agent cursor --instance-id "$id" --cwd "$case_dir/cwd" --no-pre-trust --reuse-finished --extra-env "CURSOR_CONFIG_DIR=$case_dir/new-config" --extra-env "CURSOR_DATA_DIR=$case_dir/new-data" > "$case_dir/public.json"
 check reuse.ready wait_until pc_start_ready
 check reuse.runtime cg_selection "$case_dir/new-config" "$case_dir/new-data"
 cg_finish saved-resume-new-reuse 'AC11/AC12 SR-2.2/SR-6.2 saved selection survives caller change; reuse new selection'
}

cg_rejected_selection() {
 local selected_config=$CURSOR_CONFIG_DIR selected_data=$CURSOR_DATA_DIR
 jq -cn '{firstNewReply:{error:{code:-32000,message:"SYNTHETIC-PRIVATE"}}}' > "$case_dir/scenario"
 spawn_cursor
 check rejection.runtime-started wait_until test -s "$case_dir/runtime-pid"
 runtime=$(cat "$case_dir/runtime-pid")
 gateway=$(sql "SELECT pane_pid FROM spawns WHERE instance_id='$id';")
 check rejection.runtime-gone wait_gone "$runtime"
 check rejection.gateway-gone wait_gone "$gateway"
 check rejection.actual-selection cg_selection "$selected_config" "$selected_data"
 check rejection.public-missing ad get --instance-id "$id" > "$case_dir/public.json"
 check rejection.missing jq -e '.state=="missing" and .session_id==""' "$case_dir/public.json" >/dev/null
 check rejection.no-retry frames | jq -se '[.[]|select(has("method"))|.method]==["initialize","session/new"]' >/dev/null
 check rejection.no-secret bash -c '! grep -q SYNTHETIC-PRIVATE "$1"' cg "$HOME/.config/agent-director/ad-trail.jsonl"
 cg_finish selected-new-rejection 'AC12 SR-7.1 actual selected failure; launch admission is asynchronous; no prompt/account retry'
}
cg_rejected_latest() {
 rl_fixture
 local selected_config=$CURSOR_CONFIG_DIR selected_data=$CURSOR_DATA_DIR
 export CURSOR_CONFIG_DIR="$case_dir/changed-caller-config" CURSOR_DATA_DIR="$case_dir/changed-caller-data"
 jq -cn '{loadSupport:true,waitForLoad:true,loadReply:{error:{code:-32000,message:"SYNTHETIC-PRIVATE"}}}' > "$case_dir/scenario"
 rl_resume MCP
 check rejected-load.actual-selection cg_selection "$selected_config" "$selected_data"
 check rejected-load.pending test "$(row_state)" = pending
 touch "$case_dir/new-release"
 check rejected-load.gateway-gone wait_gone "$resumed_gateway"
 check rejected-load.final-latest-load rl_load_seen
 check rejected-load.public-get ad get --instance-id "$id" > "$case_dir/public.json"
 check rejected-load.missing jq -e --arg session "$session" '.state=="missing" and .session_id==$session' "$case_dir/public.json" >/dev/null
 check rejected-load.no-fresh rl_no_new
 check rejected-load.no-prompt pc_prompts_is 0
 rl_preserved
 cg_finish selected-latest-rejection 'AC11/12 SR-6.2/SR-7.1 saved runtime selection; latest-only rejection keeps older/artifacts; no fallback'
}
cg_recovery_intent() {
 pc_install_bun
 rl_fixture
 local selected_config=$CURSOR_CONFIG_DIR selected_data=$CURSOR_DATA_DIR original_id=$id fresh_id
 ad make-template --name recovery-intent --agent cursor --extra-env "CURSOR_CONFIG_DIR=$selected_config" --extra-env "CURSOR_DATA_DIR=$selected_data" >/dev/null
 # Fault injection only: lose the session pointer; never a user recovery step.
 sql "UPDATE spawns SET session_id=NULL WHERE instance_id='$id';"
 sql -json "SELECT * FROM spawns WHERE instance_id='$id';" > "$case_dir/original-before.json"
 if ad resume --instance-id "$id" > "$case_dir/refused" 2> "$case_dir/error"; then return 1; fi
 check recovery.named grep -q ErrNoSessionId "$case_dir/error"
 check recovery.public-advice jq -e '.err_description|contains("use agent-director get --instance-id <id>")' "$case_dir/error" >/dev/null
 check recovery.intent-advice jq -e '.err_description|contains("add caller-known supported extra_env")' "$case_dir/error" >/dev/null
 check recovery.human-bound jq -e '.err_description|contains("docs/operator-actions.md#inspect-and-end-a-session-by-hand before any fresh launch")' "$case_dir/error" >/dev/null
 ad get --instance-id "$id" | jq -e '.state=="ended"' >/dev/null
 ad kill --instance-id "$id" | jq -e '.kill_sent==false' >/dev/null
 # Finished kill is no death proof: the fixture already waited for both exact processes.
 check recovery.old-gateway-gone wait_gone "$gateway"
 check recovery.old-runtime-gone wait_gone "$runtime"
 export CURSOR_CONFIG_DIR="$case_dir/changed-caller-config" CURSOR_DATA_DIR="$case_dir/changed-caller-data"
 printf '{}\n' > "$case_dir/scenario"
 : > "$case_dir/trace.jsonl"
 pc_public Bun spawn "$(jq -cn --arg cwd "$case_dir/cwd" '{agent:"cursor",cwd:$cwd,no_pre_trust:true,template:"recovery-intent",extra_env:{}}')" > "$case_dir/public.json"
 fresh_id=$(jq -r '.instance_id' "$case_dir/public.json")
 check recovery.unique test -n "$fresh_id"
 check recovery.different test "$fresh_id" != "$original_id"
 id=$fresh_id
 check recovery.ready wait_until pc_start_ready
 check recovery.actual-selection cg_selection "$selected_config" "$selected_data"
 check recovery.no-load cg_recovery_no_load
 id=$original_id
 check recovery.original-unchanged test "$(sql -json "SELECT * FROM spawns WHERE instance_id='$id';")" = "$(cat "$case_dir/original-before.json")"
 check recovery.history-unchanged test "$(sql -json "SELECT * FROM session_history WHERE instance_id='$id';")" = "$(cat "$case_dir/history.json")"
 check recovery.native-and-older sha256sum -c "$case_dir/retained-sha256" >/dev/null
 check recovery.transcript-prefix cmp "$case_dir/protected-before" <(head -c "$(wc -c < "$case_dir/protected-before")" "$path")
 cg_finish recovery-known-template 'AC11/12 SR-2.2/SR-6.2 supported known template preserves intent/HOME; public get/kill and exact prior death; fresh unique id; originals retained'
}
cg_recovery_no_load() { frames | jq -se '[.[]|select(.method=="session/load")]|length==0' >/dev/null; }

# One shared HOME/store/cwd/server, separate runtime evidence and explicit session names.
cg_enter() {
 case_dir="$cg_base/$1"; id="$cg_id-$1"
 path=''
 if [[ -f $STORE ]]; then path=$(sql "SELECT jsonl_path FROM spawns WHERE instance_id='$id';"); fi
 export CURSOR_CONFIG_DIR="$cg_base/shared-config" CURSOR_DATA_DIR="$cg_base/shared-data"
 if [[ $1 == c ]]; then export CURSOR_CONFIG_DIR="$cg_base/distinct-config" CURSOR_DATA_DIR="$cg_base/distinct-data"; fi
}
cg_snapshot() {
 sql -json "SELECT * FROM spawns WHERE instance_id='$id'; SELECT * FROM cursor_runtimes WHERE bound_instance_id='$id'; SELECT * FROM permission_requests WHERE instance_id='$id'; SELECT * FROM session_history WHERE instance_id='$id';"
}
cg_observe_work() {
 local kind=$1 op=$2 text=$3 pid=$runtime
 if [[ $kind == worker ]]; then pid=$cg_worker; fi
 node "$SOURCE_DIR/test/driver/cursor-groups-records.cjs" "$kind" "$case_dir" "$cg_base/cwd" "$CURSOR_CONFIG_DIR" "$CURSOR_DATA_DIR" "$op" "$text" "$pid"
}
cg_turn() {
 local text=$1 count=$2
 rl_send CLI "$text" >/dev/null
 check composed.completed wait_until pc_ready "$count"
 check composed.received ad read-pane --instance-id "$id" --n-lines 100 | jq -e --arg text "Synthetic reply: $text" '.pane|contains($text)' >/dev/null
 check composed.useful cg_observe_work runtime unused "$text" > "$case_dir/observed-$count.json"
}
cg_worker_op() {
 local op=$1 text=$2 claim=${3:-null}
 jq -cn --arg op "$op" --arg input "$text" --argjson hook "$claim" '{operation:$op,input:$input,hook:$hook}' > "$case_dir/worker-pid.request.json.tmp"
 mv "$case_dir/worker-pid.request.json.tmp" "$case_dir/worker-pid.request.json"
 check worker.result wait_until cg_worker_ready "$op"
 check worker.useful cg_observe_work worker "$op" "$text" > "$case_dir/observed-$op.json"
}
cg_worker_ready() { [[ -s $case_dir/worker-pid.result.json ]] && jq -e --arg op "$1" '.operation==$op' "$case_dir/worker-pid.result.json" >/dev/null; }
cg_composed_cleanup() {
 local rc=$?
 set +e
 if [[ -d $cg_base/a ]]; then
  touch "$cg_base/a/worker-release"
  if [[ -s $cg_base/a/worker-pid ]]; then wait_gone "$(cat "$cg_base/a/worker-pid")"; fi
 fi
 cg_archive
 (exit "$rc"); cleanup_cursor_case
 exit "$rc"
}
cg_archive() {
 local artifacts="/tmp/cursor-groups-artifacts/$cg_id"
 mkdir -m 700 -p "$artifacts"
 local label
 for label in a b c; do if [[ -d $cg_base/$label ]]; then cp -R "$cg_base/$label" "$artifacts/"; fi; done
 sql -readonly -json 'SELECT * FROM spawns; SELECT * FROM cursor_runtimes; SELECT * FROM permission_requests; SELECT * FROM session_history;' > "$artifacts/composed-authority.jsonl"
}
cg_composed() {
 local outcome=$1 label settings program oldpath claim saved_b cg_worker_start
 cg_base=$case_dir; cg_id=$id
 trap cg_composed_cleanup EXIT
 for label in a b c; do
  cg_enter "$label"
  mkdir -m 700 -p "$case_dir"
  settings=$(jq -cn --arg dir "$case_dir" --argjson worker "$([[ $label == a ]] && echo true || echo false)" '{trace:($dir+"/trace.jsonl"),pid:($dir+"/runtime-pid"),release:($dir+"/release"),worker:$worker,workerPID:($dir+"/worker-pid"),workerRelease:($dir+"/worker-release"),scenario:($dir+"/scenario"),raw:($dir+"/raw"),sent:($dir+"/sent.jsonl"),newRelease:($dir+"/new-release")}')
  program="$case_dir/index.js"
  { printf '#!%s\nconst SETTINGS = %s;\n' "$(command -v node)" "$settings"; cat "$AD_CURSOR_FIX_SOURCE"; } > "$program"
  chmod 700 "$program"
  sed -i "s|^cursor_path = .*|cursor_path = \"$program\"|" "$HOME/.config/agent-director/config.toml"
  jq -cn --arg session "composed-$label" '{sessionId:$session,recordArtifacts:true}' > "$case_dir/scenario"
  # Reuse public launch selection plumbing, with the single shared cwd explicit.
  ad spawn --agent cursor --instance-id "$id" --tmux-session-name "$id" --cwd "$cg_base/cwd" --no-pre-trust --relay-mode on > "$case_dir/public.json"
  check composed.ready wait_until pc_start_ready
  load_identity
  path=$(sql "SELECT jsonl_path FROM spawns WHERE instance_id='$id';")
  sql -json "SELECT session_id,life_number,launch_token,pane_pid,pane_starttime,jsonl_path FROM spawns WHERE instance_id='$id';" > "$case_dir/initial.json"
  sql -json "SELECT * FROM cursor_runtimes WHERE bound_instance_id='$id';" > "$case_dir/binding-before.json"
  cg_proc_start "$runtime" > "$case_dir/runtime-start-before"
  cg_turn "before-$label" 1
 done
 # All three current bindings still verify simultaneously before the affected event.
 for label in a b c; do cg_enter "$label"; load_identity; check composed.overlap kill -0 "$runtime"; done
 cg_enter a; load_identity
 cg_worker=$(cat "$case_dir/worker-pid")
 cg_worker_start=$(cg_proc_start "$cg_worker")
 cg_worker_op worker-before 'independent before'
 # Retain an actual outstanding permission while the detached worker claims B.
 cg_enter b; load_identity
 jq -cn --arg session "$session" '{recordArtifacts:true,requests:[{jsonrpc:"2.0",id:"foreign-held",method:"session/request_permission",params:{sessionId:$session,toolCall:{toolCallId:"foreign-held",title:"Synthetic action",kind:"execute",status:"pending",rawInput:null},options:[{optionId:"allow-once",name:"Allow once",kind:"allow_once"},{optionId:"allow-always",name:"Allow always",kind:"allow_always"},{optionId:"reject-once",name:"Reject",kind:"reject_once"}]}}],afterAnswers:[{method:"session/update",params:{sessionId:$session,update:{sessionUpdate:"tool_call_update",toolCallId:"foreign-held",status:"completed",rawOutput:{exitCode:0,stdout:"synthetic current result",stderr:""}}}}]}' > "$case_dir/scenario"
 rl_send CLI held-target >/dev/null
 check foreign.request wait_until request_count_is 1
 saved_b=$(cg_snapshot)
 claim=$(jq -cn --arg id "$id" --arg store "$STORE" --arg session "$session" --arg connection "$(sql "SELECT bound_connection_id FROM cursor_runtimes WHERE bound_instance_id='$id';")" --arg transcript "$(sql "SELECT jsonl_path FROM spawns WHERE instance_id='$id';")" '{program:"/usr/local/bin/agent-director",args:["hook","--agent","cursor"],env:{AGENT_DIRECTOR_INSTANCE_ID:$id,AGENT_DIRECTOR_AGENT:"cursor",AGENT_DIRECTOR_CURSOR_STORE_PATH:$store},payload:{hook_event_name:"UserPromptSubmit",session_id:$session,cursor_connection:$connection,origin:"main",transcript_path:$transcript}}')
 cg_enter a
 cg_worker_op foreign-report 'foreign current claim' "$claim"
 cg_enter b
 check foreign.unchanged test "$(cg_snapshot)" = "$saved_b"
 ad decide --instance-id "$id" --request-token "$(request_token foreign-held)" --decision allow >/dev/null
 check foreign.legitimate-completion wait_until pc_ready 2
 printf '{"recordArtifacts":true}\n' > "$case_dir/scenario"
 cg_enter a; load_identity
 if [[ $outcome == stop ]]; then ad pause --instance-id "$id" >/dev/null; else
  jq -cn '{promptReply:{error:{code:-32000,message:"Authentication required"}}}' > "$case_dir/scenario"
  rl_send CLI affected-rejection >/dev/null
 fi
 check affected.runtime-gone wait_gone "$runtime"
 check affected.gateway-gone wait_gone "$gateway"
 local affected_state=ended; [[ $outcome != rejection ]] || affected_state=missing
 check affected.truthful-state test "$(row_state)" = "$affected_state"
 check affected.no-fallback frames | jq -se --arg outcome "$outcome" '[.[]|select(has("method"))|.method] == (["initialize","session/new","session/prompt"] + (if $outcome=="stop" then [] else ["session/prompt"] end))' >/dev/null
 for label in b c; do
  cg_enter "$label"; load_identity
  check peer.binding cmp "$case_dir/binding-before.json" <(sql -json "SELECT * FROM cursor_runtimes WHERE bound_instance_id='$id';")
  check peer.kernel-start test "$(cg_proc_start "$runtime")" = "$(cat "$case_dir/runtime-start-before")"
  local count=2; [[ $label != b ]] || count=3
  cg_turn "after-$label" "$count"
 done
 cg_enter a; cg_worker_op worker-after 'independent after'
 check worker.same-identity test "$(cg_proc_start "$cg_worker")" = "$cg_worker_start"
 # Stale session claim is retained across a real public /new publication.
 cg_enter b; load_identity; oldpath=$session
 path=$(sql "SELECT jsonl_path FROM spawns WHERE instance_id='$id';")
 pc_terminal /new
 check stale.rotation wait_until cg_rotated "$oldpath"
 check stale.publication wait_until pc_rotation_published 1
 saved_b=$(cg_snapshot)
 cg_enter a; cg_worker_op stale-report 'obsolete session claim' "$claim"
 cg_enter b
 check stale.unchanged test "$(cg_snapshot)" = "$saved_b"
 cg_turn current-after-stale 4
 for label in b c; do
  cg_enter "$label"; load_identity
  ad pause --instance-id "$id" >/dev/null
  check peer.gateway-gone wait_gone "$gateway"
  check peer.runtime-gone wait_gone "$runtime"
  check peer.exact-frames cg_trace "$label"
 done
 cg_enter a; touch "$case_dir/worker-release"
 check worker.released wait_gone "$cg_worker"
 for label in a b c; do
  cg_enter "$label"; runtime=$(cat "$case_dir/runtime-pid")
  local other_config="$cg_base/distinct-config" other_data="$cg_base/distinct-data"
  if [[ $label == c ]]; then other_config="$cg_base/shared-config"; other_data="$cg_base/shared-data"; fi
  check artifacts.coexist-and-no-cross node "$SOURCE_DIR/test/driver/cursor-groups-records.cjs" inventory "$case_dir" "$cg_base/cwd" "$CURSOR_CONFIG_DIR" "$CURSOR_DATA_DIR" "$other_config" "$other_data" "$runtime" > "$case_dir/artifact-inventory.json"
 done
 cg_archive
 case_dir=$cg_base; id=$cg_id
 cg_finish "composed-$outcome" 'AC12 SR-7.1/SR-9 synthetic simultaneous selections/useful work, exact peer identities, detached useful worker, real foreign/stale hook parent refusal; no credential/native-account proof'
}
cg_proc_start() { node -e 'const s=require("fs").readFileSync("/proc/"+process.argv[1]+"/stat","utf8");const f=s.slice(s.lastIndexOf(")")+2).split(" ");if(f[0]==="Z")process.exit(1);console.log(f[19])' "$1"; }
cg_rotated() { [[ $(sql "SELECT session_id FROM spawns WHERE instance_id='$id';") != "$1" ]] && pc_start_ready; }
cg_trace() {
 frames | jq -se --arg peer "$1" '[.[]|select(has("method"))|.method] == (if $peer=="b" then ["initialize","session/new","session/prompt","session/prompt","session/prompt","session/new","session/prompt"] else ["initialize","session/new","session/prompt","session/prompt"] end)' >/dev/null
}
