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
 check recovery.public-advice grep -Fq 'use agent-director get --instance-id <id>' "$case_dir/error"
 check recovery.intent-advice grep -Fq 'add caller-known supported extra_env' "$case_dir/error"
 check recovery.human-bound grep -Fq 'docs/operator-actions.md#inspect-and-end-a-session-by-hand before any fresh launch' "$case_dir/error"
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
