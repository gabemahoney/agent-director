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
AD_CURSOR_FIX_SETTINGS=$(jq --arg dir "$case_dir" '.+{mcpTrace:($dir+"/mcp.jsonl"),mcpRelease:($dir+"/mcp-release")}' <<<"$AD_CURSOR_FIX_SETTINGS")
# Change only this synthetic fixture's help setting.
sed -i '/^\[defaults\]/a inject_help_hook = true' "$HOME/.config/agent-director/config.toml"
agent-director help > "$case_dir/canonical-help"
jq -cn --rawfile plain "$case_dir/canonical-help" --arg id "$id" '{mcpExpectedInstructions:$plain,mcpCalls:[{name:"get",arguments:{instance_id:$id}},{name:"read-pane",arguments:{instance_id:$id}}]}' > "$case_dir/scenario"
ch_peer_socket=''
ch_peer_gateway=''
ch_peer_runtime=''
ch_peer_server=''
ch_peer_retired=0
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
 local out="/tmp/cursor-callers-help-artifacts/$id" name peer_path
 pc_export_evidence || return
 mkdir -m 700 -p "$out" || return
 ch_peer_binding || return
 ch_peer_evidence "$out" || return
 for name in canonical-help mcp.jsonl mcp.jsonl.inventory.json scenario trace.jsonl raw peer-mcp.jsonl peer-mcp.jsonl.inventory.json peer-scenario peer-trace.jsonl peer-raw public.json peer-public.json peer-startup.log peer-cleanup.stderr peer-spawn.stderr peer-binding.jsonl peer-tmux-command; do
  if [[ -f $case_dir/$name ]]; then cp "$case_dir/$name" "$out/$name" || return; chmod 600 "$out/$name" || return; fi
 done
 pc_export_launch_records "$out" "$STORE" "$id" "${path:-}" || return
 if [[ -f $case_dir/peer.db ]]; then
  peer_path=$(/opt/driver/sql.sh "$case_dir/peer.db" -readonly "SELECT jsonl_path FROM spawns WHERE instance_id='peer-$id';") || return
  mkdir -m 700 -p "$out/peer" || return
  pc_export_launch_records "$out/peer" "$case_dir/peer.db" "peer-$id" "$peer_path" || return
 fi
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
 local rc=$? evidence_rc=0 cleanup_rc=0 helper_cleanup_rc=0
 trap - EXIT
 set +e
 ch_export || { evidence_rc=$?; printf 'HELP_EVIDENCE_FAILED stage=export exit=%s\n' "$evidence_rc" >&2; }
 # Gateway death is observed before tmux teardown on successful cases.
 if [[ -n $ch_peer_socket ]]; then ch_cleanup_peer || cleanup_rc=$?; fi
 # Retain the final server tail as well as the pre-teardown first failure.
 ch_peer_evidence "/tmp/cursor-callers-help-artifacts/$id" || { evidence_rc=$?; printf 'HELP_EVIDENCE_FAILED stage=peer-diagnostics exit=%s\n' "$evidence_rc" >&2; }
 if ((rc!=0 || evidence_rc!=0 || cleanup_rc!=0)) && [[ -n $ch_peer_socket ]]; then ch_peer_failure; fi
 if [[ -f $case_dir/peer-cleanup.stderr ]]; then
  if ! cp "$case_dir/peer-cleanup.stderr" "/tmp/cursor-callers-help-artifacts/$id/peer-cleanup.stderr"; then evidence_rc=1; fi
  if ! chmod 600 "/tmp/cursor-callers-help-artifacts/$id/peer-cleanup.stderr"; then evidence_rc=1; fi
 fi
 (exit "$rc"); cleanup_cursor_case || helper_cleanup_rc=$?
 if ((cleanup_rc==0)); then cleanup_rc=$helper_cleanup_rc; fi
 printf 'HELP_CASE_EXIT original=%s evidence=%s cleanup=%s\n' "$rc" "$evidence_rc" "$cleanup_rc" >&2
 if ((rc==0 && evidence_rc!=0)); then rc=$evidence_rc; fi
 if ((rc==0 && cleanup_rc!=0)); then rc=$cleanup_rc; fi
 exit "$rc"
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
 if [[ $surface == CLI ]]; then ad pause --instance-id "$id" >/dev/null; else pc_public "$surface" pause "$(jq -cn --arg id "$id" '{instance_id:$id}')" >/dev/null; fi
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
