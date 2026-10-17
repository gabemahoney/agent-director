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
 for item in runtime-pid.selection.json trace.jsonl scenario saved.json history.json error; do if [[ -f $case_dir/$item ]]; then cp "$case_dir/$item" "$artifacts/$item"; fi; done
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
