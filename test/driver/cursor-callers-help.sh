#!/usr/bin/env bash
# User scheduler only. Bound the entire case and preserve its original result.
if [[ ${CH_CASE_GUARDED:-} != 1 ]]; then
 export CH_CASE_GUARDED=1
 ch_rc=0
 ch_flags=()
 [[ $- != *x* ]] || ch_flags+=(-x)
 if [[ -n ${BASH_EXECUTION_STRING:-} ]]; then ch_command=(bash "${ch_flags[@]}" -c "$BASH_EXECUTION_STRING"); else ch_command=(bash "${ch_flags[@]}" "$0" "$@"); fi
 timeout --kill-after=3s 240s bash -c 'trap "sleep 4" TERM; "$@" & child=$!; wait "$child"' ch-case-guard "${ch_command[@]}" || ch_rc=$?
 if ((ch_rc==124 || ch_rc==137)); then printf 'CASE_GUARD_EXIT collector=cursor-callers-help limit_seconds=240 exit=%s\n' "$ch_rc" >&2; fi
 exit "$ch_rc"
fi
for ch_key in ${!AGENT_DIRECTOR_@}; do [[ $ch_key == AGENT_DIRECTOR_TEST_HARNESS ]] || unset "$ch_key"; done
unset TMUX TMUX_PANE GIT_CONFIG_GLOBAL
umask 077
source /work/source/test/driver/cursor-pane-controls.sh
export GIT_CONFIG_GLOBAL="$case_dir/gitconfig" GIT_CONFIG_NOSYSTEM=1
: > "$GIT_CONFIG_GLOBAL"
AD_CURSOR_FIX_SETTINGS=$(jq --arg dir "$case_dir" '.+{mcpTrace:($dir+"/mcp.jsonl"),mcpRelease:($dir+"/mcp-release"),mcpControl:($dir+"/mcp-control.json")}' <<<"$AD_CURSOR_FIX_SETTINGS")
# Change only this synthetic fixture's help setting.
sed -i '/^\[defaults\]/a inject_help_hook = true' "$HOME/.config/agent-director/config.toml"
agent-director help > "$case_dir/canonical-help"
jq -cn --rawfile plain "$case_dir/canonical-help" --arg id "$id" '{mcpExpectedInstructions:$plain,mcpCalls:[{name:"get",arguments:{instance_id:$id}},{name:"read-pane",arguments:{instance_id:$id}}]}' > "$case_dir/scenario"
ch_peer_socket=''
ch_peer_gateway=''
ch_peer_runtime=''
ch_peer_server=''
ch_peer_retired=0
ch_known_children=()
ch_peer_evidence() {
 local out=$1
 [[ -d $case_dir/peer-diagnostics ]] || return 0
 mkdir -m 700 -p "$out/peer-diagnostics" || return
 chmod 00700 "$out/peer-diagnostics" || return
 cp -R "$case_dir/peer-diagnostics/." "$out/peer-diagnostics/" || return
 chmod -R go-rwx "$out/peer-diagnostics" || return
}
ch_peer_binding() {
 [[ -f $case_dir/peer.db ]] || return 0
 # Startup bindings survive an exit before session/transcript publication.
 /opt/driver/sql.sh "$case_dir/peer.db" -readonly -json "SELECT instance_id,state,session_id,jsonl_path,launch_token,life_number,pane_pid,pane_starttime,tmux_socket,tmux_server_pid,tmux_server_starttime FROM spawns WHERE instance_id='peer-$id'; SELECT bound_instance_id,bound_launch_token,bound_life_number,bound_gateway_pid,bound_gateway_start,bound_runtime_pid,bound_runtime_start,bound_session_id,bound_terminated FROM cursor_runtimes WHERE bound_instance_id='peer-$id';" > "$case_dir/peer-binding.jsonl"
}
ch_peer_failure() {
 local name
 printf 'PEER_FAILURE instance=peer-%s socket=%s evidence=/tmp/cursor-callers-help-artifacts/%s/peer-diagnostics\n' "$id" "$ch_peer_socket" "$id" >&2
 for name in "$case_dir/peer-startup.log" "$case_dir/peer-spawn.stderr" "$case_dir/peer-binding.jsonl" "$case_dir"/peer-diagnostics/tmux-server-*.log "$case_dir"/peer-diagnostics/tmux-out-*.log; do
  [[ -f $name ]] || continue
  printf 'PEER_DIAGNOSTIC file=%s tail_bytes=4096\n' "${name##*/}" >&2
  tail -c 4096 "$name" >&2
  printf '\n' >&2
  if [[ ${name##*/} == tmux-server-*.log ]]; then
   # Detached tmux-out can be empty. Verbose server input records retain
   # the gateway's own pane diagnostics before the pane disappears.
   printf 'PEER_PANE_DIAGNOSTIC file=%s tail_bytes=4096\n' "${name##*/}" >&2
   { grep -a 'input_parse_buffer:' "$name" || true; } | tail -c 4096 >&2
   printf '\n' >&2
  fi
 done
}
ch_export() {
 local out="/tmp/cursor-callers-help-artifacts/$id" name peer_path children child_id child_path
 pc_export_evidence || return
 mkdir -m 700 -p "$out" || return
 ch_peer_binding || return
 ch_peer_evidence "$out" || return
 for name in canonical-help mcp.jsonl mcp.jsonl.inventory.json scenario trace.jsonl raw commits.jsonl peer-mcp.jsonl peer-mcp.jsonl.inventory.json peer-scenario peer-trace.jsonl peer-raw public.json peer-public.json peer-startup.log peer-cleanup.stderr peer-spawn.stderr peer-binding.jsonl peer-tmux-command route-before.json route-after.json mcp-control.json; do
  if [[ -f $case_dir/$name ]]; then cp "$case_dir/$name" "$out/$name" || return; chmod 600 "$out/$name" || return; fi
 done
 for name in "$case_dir"/child-*; do
  [[ -f $name ]] || continue
  cp "$name" "$out/" || return
  chmod 600 "$out/${name##*/}" || return
 done
 pc_export_launch_records "$out" "$STORE" "$id" "${path:-}" || return
 if [[ -f $STORE ]]; then
  children=$(sql -readonly -separator '|' "SELECT instance_id,jsonl_path FROM spawns WHERE parent_id='$id' ORDER BY instance_id;") || return
  if [[ -n $children ]]; then
   mkdir -m 700 -p "$out/children" || return
   while IFS='|' read -r child_id child_path; do
    mkdir -m 700 -p "$out/children/$child_id" || return
    pc_export_launch_records "$out/children/$child_id" "$STORE" "$child_id" "$child_path" || return
   done <<< "$children"
  fi
 fi
 if [[ -f $case_dir/peer.db ]]; then
  peer_path=$(/opt/driver/sql.sh "$case_dir/peer.db" -readonly "SELECT jsonl_path FROM spawns WHERE instance_id='peer-$id';") || return
  mkdir -m 700 -p "$out/peer" || return
  pc_export_launch_records "$out/peer" "$case_dir/peer.db" "peer-$id" "$peer_path" || return
 fi
}

# Retained handle controls are independent of ACP prompt admission and use the
# exact registration captured by cursorfix. No scope/nonce is rewritten here.
ch_has_control() { jq -se --arg id "$1" 'any(.[]; .event=="control_result" and .control==$id)' "$case_dir/mcp.jsonl" >/dev/null; }
ch_control() {
 local command=$1 operation=$2 calls=${3:-'[]'}
 jq -cn --arg id "$command" --arg operation "$operation" --argjson calls "$calls" '{id:$id,operation:$operation,calls:$calls}' > "$case_dir/mcp-control.json.tmp"
 mv "$case_dir/mcp-control.json.tmp" "$case_dir/mcp-control.json"
 check configured.control-received wait_until ch_has_control "$command"
 jq -se --arg id "$command" '([.[]|select(.event=="control_result" and .control==$id)]|length)==1 and all(.[]|select(.control==$id); (has("error")|not) and (if .event=="tool_response" then .response.error==null and .response.result.isError!=true else true end))' "$case_dir/mcp.jsonl" >/dev/null
}
ch_route_snapshot() {
 sql -readonly -json "SELECT session_id,life_number,launch_token,pane_pid,pane_starttime,tmux_socket,tmux_server_pid,tmux_server_starttime,jsonl_path FROM spawns WHERE instance_id='$id'; SELECT * FROM cursor_runtimes WHERE bound_instance_id='$id'; SELECT * FROM session_history WHERE instance_id='$id';"
}
ch_child() {
 local surface=$1 label=$2 settings params child
 child="child-$label-$id"
 ch_known_children+=("$label")
 if [[ -n ${3:-} ]]; then cp "$3" "$case_dir/child-$label-scenario"; else
  jq -cn --arg session "$child-session" --rawfile plain "$case_dir/canonical-help" '{sessionId:$session,loadSupport:true,mcpExpectedInstructions:$plain}' > "$case_dir/child-$label-scenario"
 fi
 settings=$(jq --arg dir "$case_dir" --arg child_label "$label" '.+{trace:($dir+"/child-"+$child_label+"-trace.jsonl"),pid:($dir+"/child-"+$child_label+"-pid"),scenario:($dir+"/child-"+$child_label+"-scenario"),raw:($dir+"/child-"+$child_label+"-raw"),sent:($dir+"/child-"+$child_label+"-sent.jsonl"),release:($dir+"/child-"+$child_label+"-release"),observer:($dir+"/child-"+$child_label+"-observer"),mcpTrace:($dir+"/child-"+$child_label+"-mcp.jsonl"),mcpControl:($dir+"/child-"+$child_label+"-control.json")}' <<<"$AD_CURSOR_FIX_SETTINGS")
 printf '%s\n' "$settings" > "$case_dir/child-$label-settings.json"
 params=$(jq -cn --arg child "$child" --arg cwd "$case_dir/cwd" --arg settings "$settings" --arg config "$CURSOR_CONFIG_DIR" --arg data "$CURSOR_DATA_DIR" '{instance_id:$child,tmux_session_name:$child,agent:"cursor",cwd:$cwd,no_pre_trust:true,relay_mode:"on",extra_env:{AD_CURSOR_FIX_SETTINGS:$settings,CURSOR_CONFIG_DIR:$config,CURSOR_DATA_DIR:$data}}')
 if [[ $surface == configured ]]; then
  ch_control "child-$label" calls "$(jq -cn --argjson params "$params" '[{name:"spawn",arguments:$params}]')"
 else
  AGENT_DIRECTOR_INSTANCE_ID="$id" AGENT_DIRECTOR_CURSOR_STORE_PATH="$case_dir/configured.db" TMUX="$socket,$gateway,0" agent-director spawn --home "$HOME" --store-path "$STORE" --instance-id "$child" --tmux-session-name "$child" --agent cursor --cwd "$case_dir/cwd" --no-pre-trust --extra-env "AD_CURSOR_FIX_SETTINGS=$settings" --extra-env "CURSOR_CONFIG_DIR=$CURSOR_CONFIG_DIR" --extra-env "CURSOR_DATA_DIR=$CURSOR_DATA_DIR" > "$case_dir/child-$label-public.json"
 fi
 check child.public-get ad get --instance-id "$child" > "$case_dir/child-$label-get.json"
 check child.parent-list ad list --parent "$id" > "$case_dir/child-$label-list.json"
 check child.parent-route test "$(sql "SELECT parent_id||'|'||tmux_socket||'|'||tmux_server_pid FROM spawns WHERE instance_id='$child';")" = "$id|$socket|$(tmux -S "$socket" display-message -p '#{pid}')"
 local child_pane
 child_pane=$(sql "SELECT pane_id FROM spawns WHERE instance_id='$child';")
 check child.actual-server test "$(tmux -S "$socket" display-message -p -t "$child_pane" '#{pid}')" = "$(sql "SELECT tmux_server_pid FROM spawns WHERE instance_id='$id';")"
 jq -e --arg child "$child" --arg parent "$id" '.instance_id==$child and .parent_id==$parent' "$case_dir/child-$label-get.json" >/dev/null
 jq -e --arg child "$child" 'any(.spawns[]; .instance_id==$child)' "$case_dir/child-$label-list.json" >/dev/null
 pc_assert_stores_untouched
}
ch_child_guard() {
 local child=$1 text=$2
 if ad send-keys --instance-id "$child" --text "$text" > "$case_dir/child-guard.json" 2> "$case_dir/child-guard.stderr"; then
  check child.gateway-refusal wait_until ch_child_refused "$child"
 else
  jq -e '.err_name=="ErrSendKeysWhileRelayed" or .err_name=="ErrRelayFallenBack" or .err_name=="ErrDialogMaybeOpen"' "$case_dir/child-guard.stderr" >/dev/null
 fi
 jq -se --arg text "$text" 'all(.[]|select(.method=="session/prompt"); .params.prompt[0].text!=$text)' "$case_dir/child-composed-trace.jsonl" >/dev/null
}
ch_child_refused() { ad read-pane --instance-id "$1" --n-lines 100 | jq -e '.pane|gsub("\n";"")|contains("Cursor input refused while starting or busy (starting, held, stale or cancellation); nothing sent")' >/dev/null; }
ch_composed_admitted() { [[ $(sql "SELECT count(*) FROM permission_requests WHERE instance_id='child-composed-$id';") == 1 ]]; }
ch_composed_completed() { [[ $(sql "SELECT state FROM spawns WHERE instance_id='child-composed-$id';") == waiting && $(jq -s '[.[]|select(.kind=="turn_outcome")]|length' "$ch_child_path") == "$1" ]]; }
# One outcome: the held request's turn is still open, because a turn's end
# retires its undecided requests (b.gwj).
ch_composed_held() { [[ $(sql "SELECT count(*) FROM spawns s JOIN permission_requests p ON p.instance_id=s.instance_id WHERE s.instance_id='child-composed-$id' AND s.state='check_permission' AND p.request_token='$1' AND p.proven_gone_at IS NULL AND COALESCE(p.decision,'')='$2' AND (p.delivered_at IS NOT NULL)=$3;") == 1 && $(jq -s '[.[]|select(.kind=="turn_outcome")]|length' "$ch_child_path") == 1 ]]; }
ch_composed_identity() {
 sql -readonly -json "SELECT parent_id,agent,session_id,life_number,launch_token,pane_pid,pane_starttime,jsonl_path,tmux_socket,tmux_server_pid,tmux_server_starttime FROM spawns WHERE instance_id='child-composed-$id'; SELECT * FROM cursor_runtimes WHERE bound_instance_id='child-composed-$id'; SELECT * FROM session_history WHERE instance_id='child-composed-$id';"
}
ch_composed_independent() {
 ch_composed_identity > "$case_dir/child-current.json" || return
 cmp "$case_dir/child-switch-before.json" "$case_dir/child-current.json" || return
 [[ $(sql "SELECT count(*) FROM spawns s JOIN cursor_runtimes r ON r.bound_instance_id=s.instance_id WHERE s.instance_id='child-composed-$id' AND s.parent_id='$id' AND s.agent='cursor' AND s.session_id='$child_session' AND s.state NOT IN ('pending','ended','missing') AND r.bound_session_id=s.session_id AND r.bound_launch_token=s.launch_token AND r.bound_life_number=s.life_number AND r.bound_gateway_pid=s.pane_pid AND r.bound_gateway_start=s.pane_starttime AND r.bound_terminated=0;") == 1 ]]
}
# The in-turn execution proof retires the request; the turn's Stop then reports waiting.
ch_composed_proven() {
 [[ $(sql "SELECT count(*) FROM permission_requests p JOIN spawns s ON s.instance_id=p.instance_id WHERE p.instance_id='child-composed-$id' AND p.request_token='$token' AND p.tool_use_id='$child_tool' AND s.session_id='$child_session' AND s.state='waiting' AND p.proven_gone_how='tool_ran' AND p.proven_gone_at>0 AND p.delivered_at>0 AND p.decision='allow';") == 1 && $(jq -s '[.[]|select(.kind=="turn_outcome")]|length' "$ch_child_path") == 2 ]] || return
 ch_composed_independent
}
ch_composed_reply() { jq -se '[.[]|select(has("method")|not)]|length==1 and .[0].id=="child-permission" and .[0].result.outcome.optionId=="allow-once"' "$case_dir/child-composed-trace.jsonl" >/dev/null; }
ch_composed() {
 pc_start
 check guidance.actual-exchange wait_until ch_exchange "$case_dir/mcp.jsonl" "$id"
 local child="child-composed-$id" capture="$SOURCE_DIR/internal/testsupport/cursorfix/testdata/authorizations.json" child_session child_tool parent_life token confirm
 child_session=$(jq -r '.[]|select(.name=="execute")|.params.sessionId' "$capture")
 child_tool=$(jq -r '.[]|select(.name=="execute")|.params.toolCall.toolCallId' "$capture")
 jq -cn --arg session "$child_session" --rawfile plain "$case_dir/canonical-help" '{sessionId:$session,mcpExpectedInstructions:$plain}' > "$case_dir/child-initial-scenario"
 ch_route_snapshot > "$case_dir/route-before.json"
 cp "$case_dir/route-before.json" "$case_dir/child-parent-initial.json"
 cp "$path" "$case_dir/child-parent-prefix"
 ch_child configured composed "$case_dir/child-initial-scenario"
 check child.ready wait_until ch_child_ready composed
 ch_route_snapshot > "$case_dir/route-after.json"
 check child.start-parent-unchanged cmp "$case_dir/route-before.json" "$case_dir/route-after.json"
 cp "$case_dir/route-after.json" "$case_dir/child-parent-start.json"
 local connection
 connection=$(sql "SELECT bound_connection_id FROM cursor_runtimes WHERE bound_instance_id='$id';")
 jq -cn --arg program "$(command -v agent-director)" --arg parent "$id" --arg store "$STORE" --arg session "$session" --arg connection "$connection" --arg path "$path" '{promptHook:{program:$program,args:["hook","--agent","cursor"],env:{AGENT_DIRECTOR_INSTANCE_ID:$parent,AGENT_DIRECTOR_AGENT:"cursor",AGENT_DIRECTOR_CURSOR_STORE_PATH:$store},payload:{hook_event_name:"UserPromptSubmit",session_id:$session,cursor_connection:$connection,origin:"main",transcript_path:$path}}}' > "$case_dir/child-composed-scenario"
 ad send-keys --instance-id "$child" --text 'child legitimate work and wrong destination hook' >/dev/null
 ch_child_path=$(sql "SELECT jsonl_path FROM spawns WHERE instance_id='$child';")
 check child.first-outcome wait_until ch_composed_completed 1
 jq -se --argjson runtime "$(sql "SELECT bound_runtime_pid FROM cursor_runtimes WHERE bound_instance_id='$child';")" 'any(.[]; .event=="prompt_hook" and .runtime==$runtime and .status==0)' "$case_dir/child-composed-mcp.jsonl" >/dev/null || { cat "$case_dir/child-composed-mcp.jsonl" >&2; return 1; }
 check child.different-authority test "$(sql "SELECT count(DISTINCT bound_gateway_pid)||'|'||count(DISTINCT bound_connection_id) FROM cursor_runtimes WHERE bound_instance_id IN ('$id','$child');")" = '2|2'
 ch_route_snapshot > "$case_dir/route-after.json"
 check child.report-parent-unchanged cmp "$case_dir/route-before.json" "$case_dir/route-after.json"
 cp "$case_dir/route-after.json" "$case_dir/child-parent-report.json"
 cmp "$path" "$case_dir/child-parent-prefix"
 jq -cn --slurpfile captures "$capture" '($captures[0][]|select(.name=="execute")) as $p | {sessionId:$p.params.sessionId,waitForAfterAnswersRelease:true,requests:[{jsonrpc:"2.0",id:"child-permission",method:$p.method,params:$p.params}],afterAnswers:[{jsonrpc:"2.0",method:"session/update",params:{sessionId:$p.params.sessionId,update:{sessionUpdate:"tool_call_update",toolCallId:$p.params.toolCall.toolCallId,status:"completed",rawOutput:{exitCode:0,stdout:"synthetic child execution",stderr:""}}}}]}' > "$case_dir/child-composed-scenario"
 relay_observe
 cp "$case_dir/observer" "$case_dir/child-composed-observer"
 ad send-keys --instance-id "$child" --text 'held child across parent transition' >/dev/null
 check child.permission-admitted wait_until ch_composed_admitted
 token=$(sql "SELECT request_token FROM permission_requests WHERE instance_id='$child';")
 check child.open-turn-held wait_until ch_composed_held "$token" '' 0
 ad get-permission --request-token "$token" > "$case_dir/child-permission-before.json"
 confirm=$(jq -r '.confirm_by' "$case_dir/child-permission-before.json")
 check child.unknown-origin test "$(sql "SELECT main_turn_end_eligible||'|'||COALESCE(agent_id,'') FROM permission_requests WHERE instance_id='$child';")" = '0|'
 jq -e --arg token "$token" --slurpfile captures "$capture" '.request_token==$token and .decision==null and .delivery=="not_confirmed" and .confirm_by!=null and (.tool_input|fromjson)==($captures[0][]|select(.name=="execute")|.params)' "$case_dir/child-permission-before.json" >/dev/null
 ad get --instance-id "$child" | jq -e --arg token "$token" 'any(.permission_requests[];.request_token==$token)' >/dev/null
 if ad decide --instance-id "$id" --request-token "$token" --decision allow > "$case_dir/child-parent-decide.json" 2> "$case_dir/child-parent-decide.stderr"; then return 1; fi
 jq -e '.err_name=="ErrNoOpenPermissionRequest"' "$case_dir/child-parent-decide.stderr" >/dev/null
 ad send-keys --instance-id "$id" --text 'useful parent while child held' >/dev/null
 check parent.useful wait_until pc_ready 1
 check child.parent-work-hold ch_composed_held "$token" '' 0
 ch_child_guard "$child" 'parent work cannot release child'
 ad list-subagents --instance-id "$id" | jq -e '.tracked==false and .subagents==[]' >/dev/null
 ch_composed_identity > "$case_dir/child-switch-before.json"
 parent_life=$(sql "SELECT life_number FROM spawns WHERE instance_id='$id';")
 ad pause --instance-id "$id" >/dev/null
 check parent.runtime-death wait_gone "$runtime"
 check parent.gateway-death wait_gone "$gateway"
 printf '#!/bin/sh\nexec sleep 300\n' > "$case_dir/codex-standin"
 chmod 700 "$case_dir/codex-standin"
 sed -i "/^cursor_path = /a codex_path = \"$case_dir/codex-standin\"" "$HOME/.config/agent-director/config.toml"
 ad spawn --instance-id "$id" --agent codex --cwd "$case_dir/cwd" --no-pre-trust --reuse-finished >/dev/null
 check parent.legitimate-switch test "$(sql "SELECT agent||'|'||life_number||'|'||COALESCE(session_id,'') FROM spawns WHERE instance_id='$id';")" = "codex|$((parent_life+1))|"
 ad list-subagents --instance-id "$id" | jq -e '.tracked==false and .subagents==[]' >/dev/null
 ad get --instance-id "$child" | jq -e --arg parent "$id" '.parent_id==$parent' >/dev/null
 ad list --parent "$id" | jq -e --arg child "$child" 'any(.spawns[];.instance_id==$child)' >/dev/null
 ch_composed_identity > "$case_dir/child-switch-after.json"
 cmp "$case_dir/child-switch-before.json" "$case_dir/child-switch-after.json"
 ad get-permission --request-token "$token" > "$case_dir/child-permission-after.json"
 jq -e --arg confirm "$confirm" '.confirm_by==$confirm and .decision==null and .delivery=="not_confirmed"' "$case_dir/child-permission-after.json" >/dev/null
 check child.no-parent-proof test "$(sql "SELECT COALESCE(proven_gone_at,'')||'|'||COALESCE(delivered_at,'') FROM permission_requests WHERE request_token='$token';")" = '|'
 check child.parent-switch-hold ch_composed_held "$token" '' 0
 ch_child_guard "$child" 'parent switch cannot release child'
 ad decide --instance-id "$child" --request-token "$token" --decision allow >/dev/null
 check child.actual-answer wait_until ch_composed_reply
 jq -se --arg token "$token" 'any(.[][];.request_token==$token and .decision=="allow" and .delivered_at!=null)' "$case_dir/commits.jsonl" >/dev/null
 ad get-permission --request-token "$token" | jq -e --arg confirm "$confirm" '.confirm_by==$confirm and .delivery=="delivered" and .decision=="allow"' >/dev/null
 check child.delivered-open-turn-held ch_composed_held "$token" allow 1
 ch_child_guard "$child" 'delivered alone cannot release child'
 ch_route_snapshot > "$case_dir/child-parent-proof-before.json"
 touch "$case_dir/child-composed-release"
 check child.correlated-proof wait_until ch_composed_proven
 ad get-permission --request-token "$token" | jq -e --arg token "$token" --arg confirm "$confirm" '.request_token==$token and .confirm_by==$confirm and .delivery=="delivered" and .decision=="allow"' >/dev/null
 ch_route_snapshot > "$case_dir/child-parent-followup-before.json"
 cmp "$case_dir/child-parent-proof-before.json" "$case_dir/child-parent-followup-before.json"
 printf '{}\n' > "$case_dir/child-composed-scenario"
 ad send-keys --instance-id "$child" --text 'useful child after parent life switch' >/dev/null
 check child.post-switch-useful wait_until ch_composed_completed 3
 ad read-pane --instance-id "$child" --n-lines 100 | jq -e '.pane|contains("Synthetic reply: useful child after parent life switch")' >/dev/null
 check child.post-followup-independent ch_composed_independent
 ad get --instance-id "$child" | jq -e --arg parent "$id" '.parent_id==$parent' >/dev/null
 ad list --parent "$id" | jq -e --arg child "$child" 'any(.spawns[];.instance_id==$child)' >/dev/null
 ch_route_snapshot > "$case_dir/child-parent-followup-after.json"
 cmp "$case_dir/child-parent-followup-before.json" "$case_dir/child-parent-followup-after.json"
 ch_route_snapshot > "$case_dir/route-before.json"
 sql -readonly -json "SELECT * FROM cursor_runtimes WHERE bound_instance_id='$child';" > "$case_dir/child-end-binding-before.json"
 ch_child_retire composed
 check child.end-state test "$(sql "SELECT state FROM spawns WHERE instance_id='$child';")" = ended
 check child.end-timestamp test "$(sql "SELECT ended_at IS NOT NULL FROM spawns WHERE instance_id='$child';")" = 1
 sql -readonly -json "SELECT * FROM cursor_runtimes WHERE bound_instance_id='$child';" > "$case_dir/child-end-binding-after.json"
 check child.end-runtime jq -e --slurpfile before "$case_dir/child-end-binding-before.json" 'length==1 and ($before[0]|length)==1 and $before[0][0].bound_terminated==0 and .==($before[0]|map(.bound_terminated=1))' "$case_dir/child-end-binding-after.json"
 check child.end-record jq -se --arg child "$child" --slurpfile binding "$case_dir/child-end-binding-before.json" '[.[]|select(.kind=="session_end")]|length==1 and .[0].instance_id==$child and .[0].session_id==$binding[0][0].bound_session_id and .[0].launch_token==$binding[0][0].bound_launch_token and .[0].life_number==$binding[0][0].bound_life_number and .[0].connection_id==$binding[0][0].bound_connection_id and .[0].data.intentional==true and .[0].data.runtime_gone==true' "$ch_child_path"
 check child.end-row-identity test "$(sql "SELECT count(*) FROM spawns s JOIN cursor_runtimes r ON r.bound_instance_id=s.instance_id WHERE s.instance_id='$child' AND s.session_id=r.bound_session_id AND s.launch_token=r.bound_launch_token AND s.life_number=r.bound_life_number AND s.pane_pid=r.bound_gateway_pid AND s.pane_starttime=r.bound_gateway_start;")" = 1
 ch_route_snapshot > "$case_dir/route-after.json"
 check child.end-parent-preserved cmp "$case_dir/route-before.json" "$case_dir/route-after.json"
 local codex_gateway
 codex_gateway=$(sql "SELECT pane_pid FROM spawns WHERE instance_id='$id';")
 ad kill --instance-id "$id" | jq -e '.kill_sent==true' >/dev/null
 check parent.codex-death wait_gone "$codex_gateway"
 ch_finish CLI "$1" retired
}
ch_child_ready() { ad read-pane --instance-id "child-$1-$id" --n-lines 100 | jq -e '.pane|contains("Cursor ready")' >/dev/null; }
ch_child_resume_ready() {
 local child="child-cli-$id" child_path current
 child_path=$(sql "SELECT jsonl_path FROM spawns WHERE instance_id='$child';") || return
 current=$(sql -readonly -json "SELECT * FROM cursor_runtimes WHERE bound_instance_id='$child';") || return
 # Loaded launches use the pane gateway and protected ACP binding, not the
 # generic hook-parent PID columns written only by SessionStart.
 [[ $(sql "SELECT count(*) FROM spawns s JOIN cursor_runtimes r ON s.instance_id=r.bound_instance_id WHERE s.instance_id='$child' AND s.agent='cursor' AND s.session_id='$original_session' AND r.bound_session_id=s.session_id AND r.bound_launch_token=s.launch_token AND r.bound_life_number=s.life_number AND r.bound_gateway_pid=s.pane_pid AND r.bound_gateway_start=s.pane_starttime AND r.bound_terminated=0;") == 1 ]] || return 1
 node -e '
  const fs=require("fs"),b=JSON.parse(process.argv[1])[0];
  const stat=pid=>{
   const s=fs.readFileSync("/proc/"+pid+"/stat","utf8");
   return s.slice(s.lastIndexOf(")")+2).split(" ");
  };
  const g=stat(b.bound_gateway_pid),r=stat(b.bound_runtime_pid);
  if(g[0]==="Z"||r[0]==="Z"||g[19]!==b.bound_gateway_start||r[19]!==b.bound_runtime_start||Number(r[1])!==b.bound_gateway_pid||fs.readFileSync(process.argv[2],"utf8")!==String(b.bound_runtime_pid))process.exit(1);
  const status=pid=>fs.readFileSync("/proc/"+pid+"/status","utf8");
  const tracer=Number(status(b.bound_runtime_pid).match(/^TracerPid:\s+(\d+)$/m)?.[1]);
  if(!(tracer>0)||Number(status(tracer).match(/^Tgid:\s+(\d+)$/m)?.[1])!==b.bound_gateway_pid)process.exit(1);
  for(const [pid,fd,pipe] of [[b.bound_gateway_pid,b.bound_input_fd,b.bound_input_pipe],[b.bound_gateway_pid,b.bound_output_fd,b.bound_output_pipe],[b.bound_runtime_pid,0,b.bound_input_pipe],[b.bound_runtime_pid,1,b.bound_output_pipe]]) {
   if(!pipe.startsWith("pipe:[")||fs.readlinkSync("/proc/"+pid+"/fd/"+fd)!==pipe)process.exit(1);
  }
 ' "$current" "$case_dir/child-cli-pid" || return
 if [[ $ch_resume_sent == 0 ]]; then
  [[ $(sql "SELECT state||'|'||session_id FROM spawns WHERE instance_id='$child';") == "pending|$original_session" ]] || return 1
  ad read-pane --instance-id "$child" --n-lines 100 | jq -e '.pane|contains("conversation loaded; awaiting a prompt")' >/dev/null || return
  jq -se --arg child "$child" --arg session "$original_session" --argjson binding "$current" 'any(.[]; .kind=="session_loaded" and .instance_id==$child and .session_id==$session and .life_number==$binding[0].bound_life_number and .launch_token==$binding[0].bound_launch_token and .connection_id==$binding[0].bound_connection_id) and $binding[0].bound_session_id==$session and $binding[0].bound_terminated==0' "$child_path" >/dev/null || return
  ch_resume_binding=$current
  # Exactly one useful prompt; keep the existing wait_until budget.
  ch_resume_sent=1
  ad send-keys --instance-id "$child" --text 'useful child after retained resume' --allow-pending >/dev/null || exit $?
  return 1
 fi
 [[ $current == "$ch_resume_binding" ]] || return 1
 ch_child_result cli 'useful child after retained resume' || return
 jq -se --arg session "$original_session" --argjson binding "$current" 'all(.[]|select(.kind=="turn_outcome"); .session_id==$session and .life_number==$binding[0].bound_life_number and .launch_token==$binding[0].bound_launch_token and .connection_id==$binding[0].bound_connection_id)' "$child_path" >/dev/null
}
ch_child_retire() {
 local child="child-$1-$id" child_gateway child_runtime child_trace="$case_dir/child-$1-mcp.jsonl"
 child_gateway=$(sql "SELECT bound_gateway_pid FROM cursor_runtimes WHERE bound_instance_id='$child';")
 child_runtime=$(sql "SELECT bound_runtime_pid FROM cursor_runtimes WHERE bound_instance_id='$child';")
 ad pause --instance-id "$child" >/dev/null
 check child.gateway-death wait_gone "$child_gateway"
 check child.runtime-death wait_gone "$child_runtime"
 mapfile -t child_mcp < <(ch_mcp_pids "$child_trace")
 jq -s '[.[]|select(.event=="started")|{pid,start}]' "$child_trace" > "$child_trace.inventory.json"
 check child.nonempty-inventory test "${#child_mcp[@]}" -gt 0
 for ch_pid in "${child_mcp[@]}"; do check child.mcp-reaped wait_until ch_reaped "$ch_pid"; done
}
ch_routes() {
 pc_start
 check guidance.actual-exchange wait_until ch_exchange "$case_dir/mcp.jsonl" "$id"
 ch_route_snapshot > "$case_dir/route-before.json"
 ch_child CLI cli
 ch_child configured configured
 check child.cli-ready wait_until ch_child_ready cli
 check child.configured-ready wait_until ch_child_ready configured
 local child="child-cli-$id" original_session foreign_pane foreign_pid foreign_start verb ch_resume_sent=0 ch_resume_binding=''
 original_session=$(sql "SELECT session_id FROM spawns WHERE instance_id='$child';")
 ch_peer_socket="$case_dir/foreign.sock"
 tmux -S "$ch_peer_socket" new-session -d -s "$child" 'exec sleep 300'
 ch_peer_server=$(tmux -S "$ch_peer_socket" display-message -p '#{pid}')
 foreign_pane=$(tmux -S "$ch_peer_socket" display-message -p '#{pane_id}')
 foreign_pid=$(tmux -S "$ch_peer_socket" display-message -p '#{pane_pid}')
 foreign_start=$(node -e 'const fs=require("fs");const s=fs.readFileSync("/proc/"+process.argv[1]+"/stat","utf8");console.log(s.slice(s.lastIndexOf(")")+2).split(" ")[19]);' "$foreign_pid")
 for verb in resume spawn; do
  ch_child_retire cli
  if [[ $verb == spawn ]]; then
   jq --arg session "$child-reused" '.sessionId=$session' "$case_dir/child-cli-scenario" > "$case_dir/child-cli-scenario.tmp"
   mv "$case_dir/child-cli-scenario.tmp" "$case_dir/child-cli-scenario"
   TMUX="$ch_peer_socket,$ch_peer_server,0" AGENT_DIRECTOR_INSTANCE_ID="$id" ad spawn --instance-id "$child" --agent cursor --cwd "$case_dir/cwd" --no-pre-trust --reuse-finished --extra-env "AD_CURSOR_FIX_SETTINGS=$(cat "$case_dir/child-cli-settings.json")" --extra-env "CURSOR_CONFIG_DIR=$CURSOR_CONFIG_DIR" --extra-env "CURSOR_DATA_DIR=$CURSOR_DATA_DIR" >/dev/null
  else
   TMUX="$ch_peer_socket,$ch_peer_server,0" AGENT_DIRECTOR_INSTANCE_ID="$id" ad resume --instance-id "$child" >/dev/null
  fi
  if [[ $verb == resume ]]; then
   check child.resumed-useful wait_until ch_child_resume_ready
  else
   check child.relaunched-ready wait_until ch_child_ready cli
  fi
  check child.recorded-socket test "$(sql "SELECT tmux_socket FROM spawns WHERE instance_id='$child';")" = "$socket"
  if [[ $verb == resume ]]; then check child.latest-session test "$(sql "SELECT session_id FROM spawns WHERE instance_id='$child';")" = "$original_session"; else check child.fresh-session test "$(sql "SELECT session_id FROM spawns WHERE instance_id='$child';")" != "$original_session"; fi
  check foreign.same-pane test "$(tmux -S "$ch_peer_socket" display-message -p '#{pane_id}')" = "$foreign_pane"
  node -e 'const fs=require("fs");const s=fs.readFileSync("/proc/"+process.argv[1]+"/stat","utf8");if(s.slice(s.lastIndexOf(")")+2).split(" ")[19]!==process.argv[2])throw Error("foreign sentinel changed");' "$foreign_pid" "$foreign_start"
 done
 ch_route_snapshot > "$case_dir/route-after.json"
 check parent.unchanged cmp "$case_dir/route-before.json" "$case_dir/route-after.json"
 ch_child_retire cli
 ch_child_retire configured
 ch_finish CLI "$1"
}
ch_restart() {
 pc_start
 check guidance.actual-exchange wait_until ch_exchange "$case_dir/mcp.jsonl" "$id"
 ch_route_snapshot > "$case_dir/route-before.json"
 ch_child configured before
 check child.before-ready wait_until ch_child_ready before
 # Wait only for the original observer expiry. This does not renew a product
 # deadline or change its serialized scope, and precedes the active turn.
 node "$SOURCE_DIR/test/driver/cursor-callers-help-records.cjs" "$case_dir/mcp.jsonl" "$case_dir/canonical-help" "$id" "$case_dir/mcp.jsonl.inventory.json" wait-expiry
 ad send-keys --instance-id "$id" --text HOLD >/dev/null
 check parent.active wait_until pc_prompts_is 1
 ch_control stop stop
 mapfile -t retired_mcp < <(ch_mcp_pids "$case_dir/mcp.jsonl")
 check stopped.nonempty-inventory test "${#retired_mcp[@]}" -eq 1
 check stopped.actual-reaped wait_until ch_reaped "${retired_mcp[0]}"
 ch_control restart restart "$(jq -cn --arg id "$id" '[{name:"get",arguments:{instance_id:$id}}]')"
 ch_child configured after
 check child.after-ready wait_until ch_child_ready after
 touch "$case_dir/release"
 check parent.held-complete wait_until pc_ready 1
 ad send-keys --instance-id "$id" --text 'useful parent after configured restart' >/dev/null
 check parent.continuation wait_until pc_ready 2
 check parent.exact-prompts pc_prompts_is 2
 ch_route_snapshot > "$case_dir/route-after.json"
 check parent.binding-history-preserved cmp "$case_dir/route-before.json" "$case_dir/route-after.json"
 node "$SOURCE_DIR/test/driver/cursor-callers-help-records.cjs" "$case_dir/mcp.jsonl" "$case_dir/canonical-help" "$id" "$case_dir/mcp.jsonl.inventory.json" restarted
 ad pause --instance-id "$id" >/dev/null
 check parent.runtime-death wait_gone "$runtime"
 check parent.gateway-death wait_gone "$gateway"
 for label in before after; do
  ad send-keys --instance-id "child-$label-$id" --text 'independent child after parent' >/dev/null
  check child.independent-useful wait_until ch_child_result "$label"
  ch_child_retire "$label"
 done
 ch_finish CLI "$1"
}
ch_child_result() {
 local child="child-$1-$id" child_path
 [[ $(sql "SELECT state FROM spawns WHERE instance_id='$child';") == waiting ]] || return 1
 child_path=$(sql "SELECT jsonl_path FROM spawns WHERE instance_id='$child';") || return
 jq -se --arg child "$child" '[.[]|select(.kind=="turn_outcome")]|length==1 and all(.[]; .instance_id==$child and .data.stop_reason=="end_turn" and .data.cancellation_requested==false)' "$child_path" >/dev/null || return
 ad read-pane --instance-id "$child" --n-lines 100 | jq -e --arg prompt "${2:-independent child after parent}" '(.pane|contains("Synthetic reply: "+$prompt)) and ((.pane|split("Cursor turn complete")|length)-1==1)' >/dev/null
}
ch_cleanup_peer() {
 local rc
 tmux -S "$ch_peer_socket" kill-server > /dev/null 2> "$case_dir/peer-cleanup.stderr" && return 0
 rc=$?
 # A sole-pane server normally exits with its retired gateway. Accept only
 # that known absence after independently observing every owned process die.
 if [[ $ch_peer_retired == 1 && $ch_peer_server =~ ^[1-9][0-9]*$ ]] && proc_gone "$ch_peer_server" &&
    { [[ $(cat "$case_dir/peer-cleanup.stderr") == "no server running on "* ]] || [[ $(cat "$case_dir/peer-cleanup.stderr") == "error connecting to "*" (No such file or directory)" ]]; }; then
  printf 'PEER_CLEANUP already-exited server=%s owned-death=proven\n' "$ch_peer_server" >&2
  return 0
 fi
 printf 'CLEANUP_FAILED stage=peer-tmux exit=%s server=%s retired=%s\n' "$rc" "$ch_peer_server" "$ch_peer_retired" >&2
 cat "$case_dir/peer-cleanup.stderr" >&2
 return "$rc"
}
ch_exit() {
 local rc=$? evidence_rc=0 cleanup_rc=0 helper_cleanup_rc=0 diagnostic_rc=0 diagnostic_emitted=0
 trap - EXIT
 set +e
 # No probe/startup capture: snapshot only at terminal exit, while children and
 # their tmux server still exist. Keep passing stdout quiet, but retain this
 # pre-cleanup observation for failures discovered during export or cleanup.
 if timeout --kill-after=1s 9s node "$SOURCE_DIR/test/driver/cursor-callers-help-diagnostics.cjs" "$case_dir" "$STORE" "$id" "$socket" "${check_id:-terminal-exit}" "$ch_peer_socket" "${ch_known_children[*]}" > "$case_dir/help-failure-metadata.jsonl" 2> "$case_dir/help-failure-metadata.stderr"; then :; else diagnostic_rc=$?; fi
 if ((rc!=0)); then ch_print_failure_metadata before-cleanup "$diagnostic_rc"; diagnostic_emitted=1; fi
 ch_export || { evidence_rc=$?; printf 'HELP_EVIDENCE_FAILED stage=export exit=%s\n' "$evidence_rc" >&2; }
 if ((evidence_rc!=0 && diagnostic_emitted==0)); then ch_print_failure_metadata before-cleanup "$diagnostic_rc"; diagnostic_emitted=1; fi
 # Gateway death is observed before tmux teardown on successful cases.
 if [[ -n $ch_peer_socket ]]; then ch_cleanup_peer || cleanup_rc=$?; fi
 # Retain the final server tail as well as the pre-teardown first failure.
 ch_peer_evidence "/tmp/cursor-callers-help-artifacts/$id" || { evidence_rc=$?; printf 'HELP_EVIDENCE_FAILED stage=peer-diagnostics exit=%s\n' "$evidence_rc" >&2; }
 if ((rc!=0 || evidence_rc!=0 || cleanup_rc!=0)) && [[ -n $ch_peer_socket ]]; then ch_peer_failure; fi
 if [[ -f $case_dir/peer-cleanup.stderr ]]; then
  if ! cp "$case_dir/peer-cleanup.stderr" "/tmp/cursor-callers-help-artifacts/$id/peer-cleanup.stderr"; then evidence_rc=1; fi
  if ! chmod 600 "/tmp/cursor-callers-help-artifacts/$id/peer-cleanup.stderr"; then evidence_rc=1; fi
 fi
 if (( (evidence_rc!=0 || cleanup_rc!=0) && diagnostic_emitted==0 )); then ch_print_failure_metadata deferred-after-peer-cleanup "$diagnostic_rc"; diagnostic_emitted=1; fi
 (exit "$rc"); cleanup_cursor_case || helper_cleanup_rc=$?
 if ((cleanup_rc==0)); then cleanup_rc=$helper_cleanup_rc; fi
 if (( (evidence_rc!=0 || cleanup_rc!=0) && diagnostic_emitted==0 )); then ch_print_failure_metadata deferred-after-cleanup "$diagnostic_rc"; fi
 printf 'HELP_CASE_EXIT original=%s evidence=%s cleanup=%s\n' "$rc" "$evidence_rc" "$cleanup_rc" >&2
 if ((rc==0 && evidence_rc!=0)); then rc=$evidence_rc; fi
 if ((rc==0 && cleanup_rc!=0)); then rc=$cleanup_rc; fi
 exit "$rc"
}
ch_print_failure_metadata() {
 # Expected diagnostic failures never invoke the driver's ERR trap or change
 # the original case result. Raw helper stderr is deliberately not published.
 printf 'HELP_FAILURE_METADATA_BEGIN case=%s stage=%s snapshot=before-cleanup emission=%s collector_exit=%s\n' "$id" "${check_id:-terminal-exit}" "$1" "$2"
 if [[ -s $case_dir/help-failure-metadata.jsonl ]]; then cat "$case_dir/help-failure-metadata.jsonl" || true; fi
 printf 'HELP_FAILURE_METADATA_END case=%s incomplete_collector=%s\n' "$id" "$2"
 return 0
}
trap ch_exit EXIT
ch_exchange() {
 local trace=$1 own=$2
 node "$SOURCE_DIR/test/driver/cursor-callers-help-records.cjs" "$trace" "$case_dir/canonical-help" "$own" "$trace.inventory.json"
}
ch_mcp_pids() { jq -r 'select(.event=="started")|.pid' "$1"; }
ch_reaped() { [[ ! -e /proc/$1 ]]; }
ch_finish() {
 local surface=$1
 mapfile -t ch_pids < <(ch_mcp_pids "$case_dir/mcp.jsonl")
 check owned.nonempty-inventory test "${#ch_pids[@]}" -gt 0
 if [[ ${3:-} != retired ]]; then
  if [[ $surface == CLI ]]; then ad pause --instance-id "$id" >/dev/null; else pc_public "$surface" pause "$(jq -cn --arg id "$id" '{instance_id:$id}')" >/dev/null; fi
 fi
 check owned.runtime-death wait_gone "$runtime"
 check owned.gateway-death wait_gone "$gateway"
 for ch_pid in "${ch_pids[@]}"; do check owned.mcp-reaped wait_until ch_reaped "$ch_pid"; done
 pc_assert_stores_untouched
 ch_export || return
 jq -cn --arg case "$2" --arg id "$id" --arg dir "/tmp/cursor-callers-help-artifacts/$id" '{case:$case,status:"pass",checks:["canonical-actual-MCP","ordinary-continuation","owned-MCP-reaped","foreign-sentinel"],provenance:{runtime:"synthetic-ACP",transport:"actual-scoped-MCP",terminal:"real-tmux",native_consumption:false},facts:{instance_id:$id,artifacts:$dir}}'
}
ch_fresh() {
 local surface=$1
 [[ $surface != Bun ]] || pc_install_bun
 pc_start
 check guidance.actual-exchange wait_until ch_exchange "$case_dir/mcp.jsonl" "$id"
 if [[ $surface == CLI ]]; then ad send-keys --instance-id "$id" --text 'ordinary after canonical guidance' >/dev/null; else pc_send "$surface" 'ordinary after canonical guidance' >/dev/null; fi
 check continuation.completed wait_until pc_ready 1
 check continuation.exact-one pc_prompts_is 1
 ch_finish "$surface" "$2"
}
ch_isolation() {
 pc_start
 check guidance.primary wait_until ch_exchange "$case_dir/mcp.jsonl" "$id"
 local peer_id="peer-$id" peer_store="$case_dir/peer.db" primary_settings="$AD_CURSOR_FIX_SETTINGS"
 ch_peer_socket="$case_dir/peer-tmux/tmux-$(id -u)/default"
 mkdir -m 700 "$case_dir/peer-tmux"
 jq -cn --rawfile plain "$case_dir/canonical-help" --arg id "$peer_id" '{mcpExpectedInstructions:$plain,mcpCalls:[{name:"get",arguments:{instance_id:$id}},{name:"read-pane",arguments:{instance_id:$id}}]}' > "$case_dir/peer-scenario"
 local peer_settings
 peer_settings=$(jq --arg dir "$case_dir" '.+{trace:($dir+"/peer-trace.jsonl"),pid:($dir+"/peer-pid"),scenario:($dir+"/peer-scenario"),raw:($dir+"/peer-raw"),mcpTrace:($dir+"/peer-mcp.jsonl")}' <<<"$primary_settings")
 local peer_tmux
 peer_tmux=$(command -v tmux)
 mkdir -m 700 "$case_dir/peer-diagnostics"
 chmod 00700 "$case_dir/peer-diagnostics"
 # new-session already passes the explicit -c cwd. Only tmux's log cwd
 # changes; exec preserves its argv and the actual AD pane command/lifetime.
 {
  printf '#!/bin/bash\nset -euo pipefail\numask 077\ncd %q\nexec %q -vv "$@"\n' "$case_dir/peer-diagnostics" "$peer_tmux"
 } > "$case_dir/peer-tmux-command"
 chmod 700 "$case_dir/peer-tmux-command"
 printf 'peer-spawn-start utc=%s socket=%s store=%s\n' "$(date -u +%s.%N)" "$ch_peer_socket" "$peer_store" >> "$case_dir/peer-startup.log"
 TMUX_TMPDIR="$case_dir/peer-tmux" AD_CURSOR_FIX_SETTINGS="$peer_settings" agent-director spawn --home "$HOME" --store-path "$peer_store" --tmux-command "$case_dir/peer-tmux-command" --agent cursor --instance-id "$peer_id" --tmux-session-name "$peer_id" --cwd "$case_dir/cwd" --no-pre-trust --extra-env "CURSOR_CONFIG_DIR=$CURSOR_CONFIG_DIR" --extra-env "CURSOR_DATA_DIR=$CURSOR_DATA_DIR" > "$case_dir/peer-public.json" 2> "$case_dir/peer-spawn.stderr"
 printf 'peer-spawn-return utc=%s\n' "$(date -u +%s.%N)" >> "$case_dir/peer-startup.log"
 ch_peer_server=$(tmux -S "$ch_peer_socket" display-message -p '#{pid}' 2>> "$case_dir/peer-startup.log") || true
 if [[ $ch_peer_server =~ ^[1-9][0-9]*$ ]]; then cat "/proc/$ch_peer_server/stat" >> "$case_dir/peer-startup.log" || true; fi
 ch_peer_binding
 check guidance.peer wait_until ch_exchange "$case_dir/peer-mcp.jsonl" "$peer_id"
 [[ $ch_peer_server =~ ^[1-9][0-9]*$ ]]
 ch_peer_gateway=$(/opt/driver/sql.sh "$peer_store" "SELECT pane_pid FROM spawns WHERE instance_id='$peer_id';")
 ch_peer_runtime=$(/opt/driver/sql.sh "$peer_store" "SELECT bound_runtime_pid FROM cursor_runtimes WHERE bound_instance_id='$peer_id';")
 check stores.primary-only test "$(sql 'SELECT count(*) FROM spawns;')" = 1
 check stores.peer-only test "$(/opt/driver/sql.sh "$peer_store" 'SELECT count(*) FROM spawns;')" = 1
 mapfile -t peer_mcp < <(ch_mcp_pids "$case_dir/peer-mcp.jsonl")
 check peer.nonempty-inventory test "${#peer_mcp[@]}" -gt 0
 agent-director pause --home "$HOME" --store-path "$peer_store" --instance-id "$peer_id" >/dev/null
 check peer.runtime-death wait_gone "$ch_peer_runtime"
 check peer.gateway-death wait_gone "$ch_peer_gateway"
 for ch_pid in "${peer_mcp[@]}"; do check peer.mcp-reaped wait_until ch_reaped "$ch_pid"; done
 check peer.server-death wait_gone "$ch_peer_server"
 ch_peer_retired=1
 for ch_pid in $(ch_mcp_pids "$case_dir/mcp.jsonl"); do check primary.mcp-survives kill -0 "$ch_pid"; done
 ad send-keys --instance-id "$id" --text 'foreign store stop leaves me useful' >/dev/null
 check primary.continuation wait_until pc_ready 1
 ch_finish CLI "$1"
}
