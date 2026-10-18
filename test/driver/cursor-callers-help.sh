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
ch_export() {
 local out="/tmp/cursor-callers-help-artifacts/$id" name peer_path
 pc_export_evidence || return
 mkdir -m 700 -p "$out" || return
 for name in canonical-help mcp.jsonl mcp.jsonl.inventory.json scenario trace.jsonl raw peer-mcp.jsonl peer-mcp.jsonl.inventory.json peer-scenario peer-trace.jsonl peer-raw public.json peer-public.json; do
  if [[ -f $case_dir/$name ]]; then cp "$case_dir/$name" "$out/$name" || return; chmod 600 "$out/$name" || return; fi
 done
 pc_export_launch_records "$out" "$STORE" "$id" "${path:-}" || return
 if [[ -f $case_dir/peer.db ]]; then
  peer_path=$(/opt/driver/sql.sh "$case_dir/peer.db" -readonly "SELECT jsonl_path FROM spawns WHERE instance_id='peer-$id';") || return
  mkdir -m 700 -p "$out/peer" || return
  pc_export_launch_records "$out/peer" "$case_dir/peer.db" "peer-$id" "$peer_path" || return
 fi
}
ch_exit() {
 local rc=$? evidence_rc=0 cleanup_rc=0 helper_cleanup_rc=0
 trap - EXIT
 set +e
 ch_export || evidence_rc=$?
 # Gateway death is observed before tmux teardown on successful cases.
 if [[ -n $ch_peer_socket ]]; then tmux -S "$ch_peer_socket" kill-server >/dev/null 2>&1 || cleanup_rc=$?; fi
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
 TMUX_TMPDIR="$case_dir/peer-tmux" AD_CURSOR_FIX_SETTINGS="$peer_settings" agent-director spawn --home "$HOME" --store-path "$peer_store" --agent cursor --instance-id "$peer_id" --tmux-session-name "$peer_id" --cwd "$case_dir/cwd" --no-pre-trust --extra-env "CURSOR_CONFIG_DIR=$CURSOR_CONFIG_DIR" --extra-env "CURSOR_DATA_DIR=$CURSOR_DATA_DIR" > "$case_dir/peer-public.json"
 check guidance.peer wait_until ch_exchange "$case_dir/peer-mcp.jsonl" "$peer_id"
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
 for ch_pid in $(ch_mcp_pids "$case_dir/mcp.jsonl"); do check primary.mcp-survives kill -0 "$ch_pid"; done
 ad send-keys --instance-id "$id" --text 'foreign store stop leaves me useful' >/dev/null
 check primary.continuation wait_until pc_ready 1
 ch_finish CLI "$1"
}
