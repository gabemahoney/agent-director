#!/usr/bin/env bash
# E4 Task2 resume compositions; E3 helper remains the sole launch/cleanup owner.
source /work/source/test/driver/cursor-pane-controls.sh

rl_public() {
 local surface=$1 verb=$2; shift 2
 if [[ $surface == CLI ]]; then ad "$verb" --instance-id "$id" "$@"; else pc_public "$surface" "$verb" "$(jq -cn --arg id "$id" '{instance_id:$id}')"; fi
}
rl_send() { if [[ $1 == CLI ]]; then local flags=(); if [[ ${3:-false} == true ]]; then flags+=(--allow-pending); fi; ad send-keys --instance-id "$id" --text "$2" "${flags[@]}"; else pc_send "$1" "$2" "${3:-false}"; fi; }
rl_native() {
 local target=$1 dir="$case_dir/native/acp-sessions/$1"
 mkdir -m 700 -p "$dir"
 cursor-resume-native sql "$case_dir/cwd" "$target" | /opt/driver/sql.sh "$dir/store.db" >/dev/null
 cursor-resume-native metadata "$case_dir/cwd" "$target" > "$dir/meta.json"
 chmod 600 "$dir/store.db" "$dir/meta.json"
}
rl_fixture() {
 check_id=fixture.provenance
 (cd "$SOURCE_DIR"; sha256sum go.mod go.sum internal/testsupport/cursorfix/nativecheckpoint/encode.go test/driver/cursor-resume-native/main.go) > "$case_dir/native-source-sha256"
 check native.fixture-current cmp /opt/cursor-resume-fixture/source-sha256 "$case_dir/native-source-sha256"
 pc_start
 older=$session
 older_path="$(dirname "$path")/$(node -e 'console.log(require("crypto").randomUUID().replaceAll("-",""))').jsonl"
 cp "$path" "$older_path"
 chmod 600 "$older_path"
 pc_terminal /new
 check latest.rotation wait_until rl_rotated
 load_identity
 sql "UPDATE session_history SET jsonl_path='$older_path' WHERE instance_id='$id' AND session_id='$older';"
 rl_native "$older"
 rl_native "$session"
 latest_db="$case_dir/native/acp-sessions/$session/store.db"
 older_db="$case_dir/native/acp-sessions/$older/store.db"
 rl_public CLI pause >/dev/null
 check stopped.runtime wait_gone "$runtime"
 check stopped.gateway wait_gone "$gateway"
 check stopped.state test "$(row_state)" = ended
 sql -json "SELECT session_id,jsonl_path,life_number,agent_args,extra_env,no_pre_trust FROM spawns WHERE instance_id='$id';" > "$case_dir/saved.json"
 sql -json "SELECT * FROM session_history WHERE instance_id='$id';" > "$case_dir/history.json"
 cp "$path" "$case_dir/protected-before"
 sha256sum "$older_db" "${older_db%/*}/meta.json" "$latest_db" "${latest_db%/*}/meta.json" "$older_path" > "$case_dir/retained-sha256"
 : > "$case_dir/trace.jsonl"
 : > "$case_dir/raw"
 : > "$case_dir/sent.jsonl"
 jq -cn --arg root "$case_dir/native" '{loadSupport:true,nativeConfigRoot:$root}' > "$case_dir/scenario"
}
rl_rotated() { [[ $(sql "SELECT session_id FROM spawns WHERE instance_id='$id';") != "$older" ]] && pc_start_ready; }
rl_loaded() { ad read-pane --instance-id "$id" --n-lines 200 | jq -e '.pane|contains("conversation loaded")' >/dev/null; }
rl_load_seen() { frames | jq -se --arg session "$session" '[.[]|select(.method=="session/load")]|length==1 and .[0].params.sessionId==$session' >/dev/null; }
rl_no_new() { frames | jq -se '[.[]|select(.method=="session/new")]|length==0' >/dev/null; }
rl_resume() {
 local surface=$1 started elapsed
 started=$(date +%s%N)
 rl_public "$surface" resume > "$case_dir/resume.json"
 elapsed=$(( ($(date +%s%N)-started)/1000000 ))
 check resume.whole-call-default-bound test "$elapsed" -lt 30000
 check resume.result jq -e --arg id "$id" '.instance_id==$id and .pre_trust=="skipped"' "$case_dir/resume.json" >/dev/null
 jq -cn --arg surface "$surface" --argjson elapsed "$elapsed" '{surface:$surface,whole_call_ms:$elapsed,client_timeout_ms:30000,timeout_override:false}' > "$case_dir/public.json"
 check resume.latest-load wait_until rl_load_seen
 check resume.no-new rl_no_new
 resumed_gateway=$(sql "SELECT pane_pid FROM spawns WHERE instance_id='$id';")
}
rl_saved_identity() {
 local got
 got=$(sql -json "SELECT session_id,jsonl_path,life_number,agent_args,extra_env,no_pre_trust FROM spawns WHERE instance_id='$id';")
 [[ $got == "$(cat "$case_dir/saved.json")" ]]
 [[ $(sql -json "SELECT * FROM session_history WHERE instance_id='$id';") == "$(cat "$case_dir/history.json")" ]]
}
rl_preserved() {
 check native-and-older.unchanged sha256sum -c "$case_dir/retained-sha256" >/dev/null
 check resume.saved-identity rl_saved_identity
 check transcript.prefix cmp "$case_dir/protected-before" <(head -c "$(wc -c < "$case_dir/protected-before")" "$path")
}
rl_export_evidence() {
 artifact_dir="/tmp/cursor-resume-life-artifacts/$id"
 mkdir -m 700 -p "$artifact_dir"
 for item in saved.json history.json public.json artifact-sha256 source-sha256 native-source-sha256 retained-sha256 trace.jsonl sent.jsonl raw scenario resume.json error protected-before; do
  if [[ -f $case_dir/$item ]]; then cp "$case_dir/$item" "$artifact_dir/$item"; fi
 done
 if [[ -n ${path:-} && -f $path ]]; then cp "$path" "$artifact_dir/protected.jsonl"; fi
 sql -readonly -json "SELECT * FROM spawns WHERE instance_id='$id'; SELECT * FROM cursor_runtimes WHERE bound_instance_id='$id'; SELECT * FROM permission_requests WHERE instance_id='$id';" > "$artifact_dir/authority.jsonl"
 chmod -R go-rwx "$artifact_dir"
}
rl_finish() {
 local case_id=$1 checks=$2
 rl_export_evidence
 for decoy in "$case_dir/configured.db" "$HOME/.config/agent-director/state.db"; do
  check store.decoy test "$(/opt/driver/sql.sh "$decoy" 'SELECT count(*) FROM spawns; SELECT value FROM collector_decoy;')" = $'0\nuntouched'
 done
 check owned.sentinel kill -0 "$control_sentinel"
 jq -cn --arg case "$case_id" --arg id "$id" --arg session "$session" --arg older "$older" --arg artifacts "$artifact_dir" --argjson checks "$checks" '{case:$case,status:"pass",checks:$checks,provenance:{terminal:"actual-tmux-scripted",runtime:"synthetic-ACP",native:"source-derived synthetic Cursor 2026.09.28-64d2043",authenticated_cursor:false,native_capture:false},facts:{instance_id:$id,latest_session:$session,older_session:$older,artifacts:$artifacts}}'
}
rl_pending_parity() {
 local surface=$1
 rl_fixture
 jq '.+{waitForLoad:true}' "$case_dir/scenario" > "$case_dir/scenario-next"
 mv "$case_dir/scenario-next" "$case_dir/scenario"
 rl_resume "$surface"
 rl_public "$surface" get | jq -e --arg session "$session" '.state=="pending" and .session_id==$session' >/dev/null
 rl_public "$surface" list-subagents | jq -e '.tracked==false and .subagents==[]' >/dev/null
 check pending.no-prompt pc_prompts_is 0
 check pending.no-session-authority test "$(sql "SELECT count(*) FROM cursor_runtimes WHERE bound_instance_id='$id' AND bound_session_id='';")" = 1
 touch "$case_dir/new-release"
 check loaded.ready wait_until rl_loaded
 check loaded.still-pending test "$(row_state)" = pending
 rl_send "$surface" 'first real resumed prompt' true >/dev/null
 check real.completed wait_until pc_ready 1
 rl_preserved
 rl_public "$surface" pause >/dev/null
}
rl_missing() {
 local kind=$1
 rl_fixture
 if [[ $kind == native ]]; then
  rm "$latest_db"
  rl_resume CLI
  check load.refusal wait_until rl_gateway_gone
  check missing.not-recreated test ! -e "$latest_db"
  check missing.no-prompt pc_prompts_is 0
  check missing.no-fallback rl_no_new
  check missing.saved-identity rl_saved_identity
  check missing.no-authority test "$(sql "SELECT count(*) FROM cursor_runtimes WHERE bound_instance_id='$id' AND bound_session_id!='';")" = 0
  # Current runtime writes failure/launch only; never rewrites the retained prefix.
  sed "\|  $latest_db$|d" "$case_dir/retained-sha256" > "$case_dir/retained-without-latest"
  check older-and-sidecars.unchanged sha256sum -c "$case_dir/retained-without-latest" >/dev/null
  check transcript.prefix cmp "$case_dir/protected-before" <(head -c "$(wc -c < "$case_dir/protected-before")" "$path")
  check missing.no-publication test "$(jq -s '[.[]|select(.kind=="session_loaded")]|length' "$path")" = 0
 else
  rm "$path"
  sql -json "SELECT * FROM spawns WHERE instance_id='$id';" > "$case_dir/row-before.json"
  if rl_public CLI resume > "$case_dir/resume.json" 2> "$case_dir/error"; then return 1; fi
  check transcript.error jq -e '.err_name=="ErrJsonlMissing"' "$case_dir/error" >/dev/null
  check transcript.absent test ! -e "$path"
  check transcript.no-mutation test "$(sql -json "SELECT * FROM spawns WHERE instance_id='$id';")" = "$(cat "$case_dir/row-before.json")"
  check transcript.no-runtime test ! -s "$case_dir/trace.jsonl"
  check native-and-older.unchanged sha256sum -c "$case_dir/retained-sha256" >/dev/null
 fi
}
rl_gateway_gone() { [[ $resumed_gateway =~ ^[0-9]+$ ]] && proc_gone "$resumed_gateway"; }
rl_default_bun() {
 local hold=$1
 pc_install_bun
 rl_fixture
 sql "UPDATE spawns SET agent_args='[\"HOLD\"]' WHERE instance_id='$id';"
 sql -json "SELECT session_id,jsonl_path,life_number,agent_args,extra_env,no_pre_trust FROM spawns WHERE instance_id='$id';" > "$case_dir/saved.json"
 if [[ $hold == permission ]]; then
  relay_scenario permission
  jq --arg session "$session" --arg root "$case_dir/native" '.+{loadSupport:true,nativeConfigRoot:$root}|.requests[0].params.sessionId=$session' "$case_dir/scenario" > "$case_dir/scenario-next"
  mv "$case_dir/scenario-next" "$case_dir/scenario"
 fi
 rl_resume Bun
 check generation.held wait_until pc_prompts_is 1
 if [[ $hold == permission ]]; then check permission.held wait_until request_count_is 1; fi
 check generation.unreleased test ! -e "$case_dir/release"
 for surface in CLI MCP Bun; do
  rl_public "$surface" get | jq -e --arg session "$session" '.session_id==$session and (.state=="working" or .state=="check_permission")' >/dev/null
  rl_public "$surface" list-subagents | jq -e '.tracked==false and .subagents==[]' >/dev/null
 done
 rl_preserved
 # Exercise the active-row refusal through the same installed client; no new call timeout.
 if rl_public Bun resume > "$case_dir/refused" 2> "$case_dir/error"; then return 1; fi
 check resume.live-error grep -q 'ErrSpawnNotResumable' "$case_dir/error"
 check generation.not-replayed pc_prompts_is 1
}
rl_replay() {
 rl_fixture
 relay_scenario questions
 jq --arg session "$session" --arg root "$case_dir/native" '{loadSupport:true,waitForLoad:true,nativeConfigRoot:$root,beforeLoadReply:((.requests|map(.params.sessionId=$session)) + [{method:"session/update",params:{sessionId:$session,update:{sessionUpdate:"user_message_chunk",content:{type:"text",text:"historic resume user"}}}},{method:"session/update",params:{sessionId:$session,update:{sessionUpdate:"agent_message_chunk",content:{type:"text",text:"historic resume agent"}}}},{id:"historic-plan",method:"cursor/create_plan",params:{sessionId:$session}}])}' "$case_dir/scenario" > "$case_dir/scenario-next"
 mv "$case_dir/scenario-next" "$case_dir/scenario"
 rl_resume MCP
 check replay.denials wait_until rl_replay_denied
 check replay.no-durable-requests request_count_is 0
 check replay.pending test "$(row_state)" = pending
 check replay.no-prompts pc_prompts_is 0
 check replay.no-loaded test "$(pc_count session_loaded)" = 0
 if grep -Eq 'historic resume (user|agent)' "$path"; then return 1; fi
 touch "$case_dir/new-release"
 check loaded.ready wait_until rl_loaded
 check replay.still-no-prompts pc_prompts_is 0
 check replay.no-durable-requests request_count_is 0
 rl_send MCP 'new real prompt after replay' true >/dev/null
 check real.completed wait_until pc_ready 1
 check replay.only-real-user test "$(jq -s '[.[]|select(.kind=="user_input")|.data.text]' "$path")" = "$(jq -n '["new real prompt after replay"]')"
 rl_preserved
 rl_public CLI pause >/dev/null
}
rl_replay_denied() {
 frames | jq -se --slurpfile spec "$case_dir/scenario" '
  [$spec[0].beforeLoadReply[]|select(has("id"))|{id, outcome:(
   if .method=="cursor/create_plan" then {outcome:"rejected"}
   elif .method=="session/request_permission" then {outcome:"selected",optionId:(.params.options|map(select(.kind=="reject_once"))|.[0].optionId)}
   else {outcome:"cancelled"} end)}] as $want |
  [.[]|select(has("method")|not)|{id,outcome:.result.outcome}] as $actual |
  ($actual|sort_by(.id))==($want|sort_by(.id))' >/dev/null
}
rl_saved_transient() {
 rl_fixture
 # Produce real queue records in an earlier resumed launch, then lose its processes.
 rl_resume CLI
 check loaded.ready wait_until rl_loaded
 rl_send CLI HOLD true >/dev/null
 check old.generation wait_until pc_prompts_is 1
 rl_send CLI 'queued transient before crash' >/dev/null
 check old.queued wait_until rl_queued
 load_identity
 tmux -S "$socket" send-keys -t "$pane" -l -- 'editable transient before crash'
 kill -KILL "$runtime" "$gateway"
 check old.runtime-gone wait_gone "$runtime"
 check old.gateway-gone wait_gone "$gateway"
 ad find-missing >/dev/null
 check old.missing test "$(row_state)" = missing
 saved_outcomes=$(pc_count turn_outcome)
 sql "UPDATE spawns SET agent_args='[\"saved initial after crash\"]' WHERE instance_id='$id';"
 sql -json "SELECT session_id,jsonl_path,life_number,agent_args,extra_env,no_pre_trust FROM spawns WHERE instance_id='$id';" > "$case_dir/saved.json"
 cp "$path" "$case_dir/protected-before"
 : > "$case_dir/trace.jsonl"
 : > "$case_dir/raw"
 : > "$case_dir/sent.jsonl"
 rl_resume CLI
 check saved.completed wait_until rl_saved_complete
 check saved.exactly-once pc_prompts_is 1
 check saved.only-text test "$(frames | jq -sr '[.[]|select(.method=="session/prompt")|.params.prompt[0].text]|.[]')" = 'saved initial after crash'
 rl_preserved
 rl_public CLI pause >/dev/null
 check transient.no-late-dispatch pc_prompts_is 1
}
rl_queued() { jq -se 'any(.[]; .kind=="submission_accepted" and .data.text=="queued transient before crash")' "$path" >/dev/null; }
rl_saved_complete() { [[ $(row_state) == waiting ]] && pc_prompts_is 1 && [[ $(pc_count turn_outcome) == "$((saved_outcomes+1))" ]]; }
trap 'rc=$?; if ((rc)); then printf "ASSERTION_FAILED id=%s instance=%s dir=%s\n" "$check_id" "$id" "$case_dir" >&2; fi; set +e; rl_export_evidence; (exit "$rc"); cleanup_cursor_case; exit "$rc"' EXIT
