#!/usr/bin/env bash
# E5 Task1 composes the existing installer and synthetic Cursor public helpers.
# Bound this collector's entire shell, including a blocked predicate or EXIT
# trap. The inner shell keeps the driver's command and diagnostic descriptors.
if [[ ${IR_CASE_GUARDED:-} != 1 ]]; then
 ir_guard_dir=$(mktemp -d /tmp/ir-guard-XXXXXX)
 export IR_CASE_GUARDED=1 IR_CASE_STAGE="$ir_guard_dir/stage"
 ir_guard_rc=0
 ir_guard_started=$SECONDS
 ir_guard_shell_flags=()
 [[ $- != *x* ]] || ir_guard_shell_flags+=(-x)
 if [[ -n ${BASH_EXECUTION_STRING:-} ]]; then
  ir_guard_command=(bash "${ir_guard_shell_flags[@]}" -c "$BASH_EXECUTION_STRING")
 else
  ir_guard_command=(bash "${ir_guard_shell_flags[@]}" "$0" "$@")
 fi
 # Keep the process-group leader alive until KILL even if its case shell
 # exits on TERM while a child still holds the driver's capture pipe open.
 timeout --kill-after=3s 240s bash -c 'trap "sleep 4" TERM; "$@" & child=$!; wait "$child"' ir-case-guard "${ir_guard_command[@]}" || ir_guard_rc=$?
 if (( ir_guard_rc == 124 || ir_guard_rc == 137 )); then
  ir_guard_elapsed=$((SECONDS-ir_guard_started))
  printf 'CASE_GUARD_EXIT collector=cursor-install-runtime limit_seconds=240 elapsed_seconds=%s exit=%s deadline_reached=%s\n' "$ir_guard_elapsed" "$ir_guard_rc" "$((ir_guard_elapsed>=240))" >&2
  ir_stage_rc=0
  timeout --kill-after=1s 1s cat "$IR_CASE_STAGE" >&2 || ir_stage_rc=$?
  printf 'CASE_GUARD_STAGE exit=%s\n' "$ir_stage_rc" >&2
  # Retain synthetic workspaces before the driver's next reset. Capture has
  # its own kill bound and cannot replace the original case failure.
  ir_items=()
  for ir_item in /tmp/co-*; do [[ ! -d $ir_item ]] || ir_items+=("${ir_item#/tmp/}"); done
  ir_capture_rc=0
  timeout --kill-after=1s 5s bash -c 'trap "sleep 2" TERM; "$@" & child=$!; wait "$child"' ir-evidence tar --exclude=.claude --exclude=.git --exclude=node_modules --exclude=.cache --exclude=auth.json --exclude=credentials.json --exclude=.env -C /tmp -czf "$ir_guard_dir/workspaces.tar.gz" "${ir_items[@]}" || ir_capture_rc=$?
  printf 'CASE_TIMEOUT_CAPTURE exit=%s dir=%s\n' "$ir_capture_rc" "$ir_guard_dir" >&2
 fi
 exit "$ir_guard_rc"
fi
set -T
trap 'printf "case=%s stage=%s dir=%s command=%q\n" "${id:-setup}" "${check_id:-setup}" "${case_dir:-}" "$BASH_COMMAND" > "$IR_CASE_STAGE"' DEBUG
ir_harness=${AGENT_DIRECTOR_TEST_HARNESS:-}
for ir_env_key in "${!AGENT_DIRECTOR_@}"; do unset "$ir_env_key"; done
unset TMUX TMUX_PANE GIT_CONFIG_GLOBAL
export AGENT_DIRECTOR_TEST_HARNESS=$ir_harness
source /work/source/test/driver/cursor-resume-life.sh
export GIT_CONFIG_GLOBAL="$case_dir/gitconfig"
: > "$GIT_CONFIG_GLOBAL"

ir_install() {
 trap ir_install_exit EXIT
 local mode=$1 h="$case_dir/install-home" toolbox="$case_dir/install-tools" name dir program rc
 mkdir -m 700 -p "$h/.config/agent-director" "$toolbox" "$case_dir/loose"
 # Only explicitly selected fixture agents can appear on this installer PATH.
 local -a dirs
 IFS=: read -r -a dirs <<< "$PATH"
 for dir in "${dirs[@]}"; do
  for program in "$dir"/*; do
   name=${program##*/}
   case "$name" in claude|codex|agent|cursor|curl|gh|agent-director) continue;; esac
   [[ -f $program && -x $program && ! -e $toolbox/$name ]] || continue
   ln -s "$program" "$toolbox/$name"
  done
 done
 program="$case_dir/agent with \"quotes\" --flag"
 printf '#!/bin/bash\necho runtime-probe >> "%s"\nexit 73\n' "$case_dir/installer-ran" > "$program"
 chmod 700 "$program"
 case "$mode" in
  path) ln -s "$program" "$toolbox/agent"; printf '[launch]\ncursor_path = ""\n' > "$h/.config/agent-director/config.toml";;
  configured) printf "[launch]\ncursor_path = '%s'\n" "$program" > "$h/.config/agent-director/config.toml";;
  editor) ln -s "$program" "$toolbox/cursor"; printf '[launch]\n' > "$h/.config/agent-director/config.toml";;
 esac
 cp "$h/.config/agent-director/config.toml" "$case_dir/config-before"
 cp "$SOURCE_DIR/skills/install-agent-director/install.sh" "$case_dir/loose/install.sh"
 cp -R "$SOURCE_DIR/skills/install-agent-director/schedule" "$case_dir/loose/schedule"
 rc=0
 HOME="$h" PATH="$toolbox" bash "$case_dir/loose/install.sh" --binary /usr/local/bin/agent-director --admin-binary /opt/bin/agent-director-admin --no-hooks --no-symlink --register-mcp > "$case_dir/install-out" 2> "$case_dir/install-err" || rc=$?
 check installer.never-probed test ! -e "$case_dir/installer-ran"
 check installer.config-preserved cmp "$case_dir/config-before" "$h/.config/agent-director/config.toml"
 if [[ $mode == editor ]]; then
  check installer.editor-refused test "$rc" = 2
  check installer.no-binary test ! -e "$h/.config/agent-director/bin/agent-director"
  check installer.three-agent-advice grep -qF 'no supported agent (Claude Code, Codex or Cursor)' "$case_dir/install-err"
 else
  check installer.success test "$rc" = 0
  check installer.binary test -x "$h/.config/agent-director/bin/agent-director"
  check installer.presence-only grep -qF '(presence only; version and compatibility not checked; agent was not run)' "$case_dir/install-out"
  check installer.skip-hooks grep -qF 'hooks   : skipped because Claude Code is not installed' "$case_dir/install-out"
  check installer.skip-mcp grep -qF 'mcp     : --register-mcp skipped because Claude Code is not installed' "$case_dir/install-out"
 fi
 jq -cn --arg case "$2" --arg mode "$mode" '{case:$case,status:"pass",checks:["presence-only","config-preserved","accurate-skips-or-no-mutation"],provenance:{runtime:"recording-standin-never-executed",authenticated_cursor:false},facts:{mode:$mode}}'
}

ir_install_exit() {
 local rc=$? evidence_rc=0 cleanup_rc=0 item
 trap - EXIT
 set +e
 artifact_dir="/tmp/cursor-resume-life-artifacts/$id"
 mkdir -m 700 -p "$artifact_dir" || evidence_rc=$?
 for item in install-out install-err config-before; do
  if [[ -f $case_dir/$item ]]; then cp "$case_dir/$item" "$artifact_dir/$item" || evidence_rc=$?; fi
 done
 if ((rc)); then
  printf 'INSTALLER_CASE_EXIT exit=%s check=%s dir=%s\n' "$rc" "$check_id" "$case_dir" >&2
  if [[ -f $case_dir/install-err ]]; then cat "$case_dir/install-err" >&2; fi
 fi
 # Installer-only cases never open the runtime override store. Preserve the
 # original error above and clean up without querying that nonexistent store.
 printf 'INSTALLER_EVIDENCE_EXIT exit=%s\n' "$evidence_rc" >&2
 cleanup_cursor_case || cleanup_rc=$?
 printf 'INSTALLER_CLEANUP_EXIT exit=%s\n' "$cleanup_rc" >&2
 exit "$rc"
}

ir_launch() {
 local surface=$1 call=$2 params=$3
 if [[ $surface != CLI ]]; then pc_public "$surface" "$call" "$params"
 elif [[ $call == resume ]]; then rl_public CLI resume
 elif [[ $(jq -r .reuse_finished <<<"$params") == true ]]; then rl_reuse_call CLI cursor "$(jq -c '.agent_args // []' <<<"$params")"
 else
  local values=()
  mapfile -d '' -t values < <(jq -j '.agent_args[]? | ., "\u0000"' <<<"$params")
  ad spawn --instance-id "$id" --agent cursor --cwd "$case_dir/cwd" --no-pre-trust -- "${values[@]}"
 fi
}

ir_runtime() {
 local surface=$1 verb=$2 selection=$3
 [[ $surface != Bun ]] || pc_install_bun
 rl_fixture
 jq --arg session "current-$id" '.+{sessionId:$session}' "$case_dir/scenario" > "$case_dir/scenario-next"
 mv "$case_dir/scenario-next" "$case_dir/scenario"
 local old_id=$id selected="$case_dir/current \"agent\" --flag" current_cfg="$HOME/.config/agent-director/config.toml"
 printf '#!/bin/bash\nprintf selected > "%s"\nexec "%s" "$@"\n' "$case_dir/selected" "$case_dir/index.js" > "$selected"
 chmod 700 "$selected"
 mkdir "$case_dir/path-agent"
 ln -s "$selected" "$case_dir/path-agent/agent"
 export PATH="$case_dir/path-agent:$PATH"
 case "$selection" in
  unset) selected='';;
  missing) selected="$case_dir/missing-runtime";;
  nonexec) selected="$case_dir/nonexec-runtime"; printf fixture > "$selected"; chmod 600 "$selected";;
 esac
 # Keep every other setting; this edits only this case's disposable current config.
 awk '!/^[[:space:]]*cursor_path[[:space:]]*=/' "$current_cfg" > "$case_dir/config-next"
 if [[ -n $selected ]]; then
  awk -v value="$selected" '{print; if ($0=="[launch]") print "cursor_path = \047" value "\047"}' "$case_dir/config-next" > "$current_cfg"
 else cp "$case_dir/config-next" "$current_cfg"; fi
 cp "$current_cfg" "$case_dir/current-config-before"
 local params call=spawn
 if [[ $verb == resume ]]; then
  call=resume
  params=$(jq -cn --arg id "$id" '{instance_id:$id}')
 else
  if [[ $verb == spawn ]]; then id="new-$id"; fi
  params=$(jq -cn --arg id "$id" --arg cwd "$case_dir/cwd" --arg verb "$verb" '{instance_id:$id,agent:"cursor",cwd:$cwd,no_pre_trust:true,reuse_finished:($verb=="reuse")}')
 fi
 ir_launch "$surface" "$call" "$params" > "$case_dir/public.json"
 check runtime.launch-result jq -e --arg id "$id" '.instance_id==$id and .pre_trust=="skipped"' "$case_dir/public.json" >/dev/null
 if [[ $selection == missing || $selection == nonexec ]]; then
  check runtime.process-backed-missing wait_until ir_missing
  check runtime.no-path-substitution test ! -e "$case_dir/selected"
  check runtime.no-protocol test ! -s "$case_dir/trace.jsonl"
  check runtime.failure-reported jq -se --arg id "$id" '[.[]|select(.event=="ad.cursor.failed" and .instance_id==$id)]|length==1' "$HOME/.config/agent-director/ad-trail.jsonl" >/dev/null
 else
  check runtime.selected-program wait_until test -s "$case_dir/selected"
  if [[ $verb == resume ]]; then
   check runtime.latest-load wait_until rl_load_seen
   check runtime.no-new rl_no_new
   check runtime.saved-values rl_saved_identity
   check runtime.loaded wait_until rl_loaded
   rl_send "$surface" 'current runtime effect' true >/dev/null
  else
   check runtime.new-ready wait_until pc_start_ready
   rl_send "$surface" 'current runtime effect' >/dev/null
  fi
  check runtime.prompt-completed wait_until ir_waiting
  rl_public "$surface" pause >/dev/null
 fi
 check runtime.config-not-rewritten cmp "$case_dir/current-config-before" "$current_cfg"
 pc_export_evidence
}
ir_missing() { [[ $(ad get --instance-id "$id" | jq -r .state) == missing ]]; }
ir_waiting() {
 local path
 path=$(sql "SELECT jsonl_path FROM spawns WHERE instance_id='$id';")
 # pc_ready reads this dynamically scoped current path, including fresh/reuse
 # launches whose transcript differs from the stopped seed's retained path.
 pc_ready 1
}

# Task2 assembled effects stay in this collector; token/protocol edge matrices are units.
ir_task2() {
 local surface=$1 args params saved_args prompt=$'--resume\n多行 🦋'
 id="mode-$id"
 jq -cn --arg session "session-$id" '{sessionId:$session,loadSupport:true,modes:{currentModeId:"agent",availableModes:[{id:"agent",name:"Agent"},{id:"plan",name:"Plan"},{id:"ask",name:"Ask"}]}}' > "$case_dir/scenario"
 : > "$case_dir/trace.jsonl"
 : > "$case_dir/raw"
 : > "$case_dir/sent.jsonl"
 args=$(jq -cn --arg prompt "$prompt" '["--model=opaque/account","--plan","--",$prompt]')
 params=$(jq -cn --arg id "$id" --arg cwd "$case_dir/cwd" --argjson args "$args" '{instance_id:$id,agent:"cursor",cwd:$cwd,no_pre_trust:true,agent_args:$args}')
 ir_launch "$surface" spawn "$params" > "$case_dir/public.json"
 check task2.initial-completion wait_until ir_task2_completed 1
 check task2.fresh-mode-order frames_task2_order fresh
 saved_args=$(sql "SELECT agent_args FROM spawns WHERE instance_id='$id';")
 rl_public "$surface" pause >/dev/null
 ir_launch "$surface" resume "$(jq -cn --arg id "$id" '{instance_id:$id}')" > "$case_dir/public.json"
 check task2.saved-initial-completion wait_until ir_task2_completed 2
 check task2.resume-mode-order frames_task2_order resume
 check task2.saved-args-retained test "$(sql "SELECT agent_args FROM spawns WHERE instance_id='$id';")" = "$saved_args"
 rl_public "$surface" pause >/dev/null
 jq --arg session "reuse-$id" '.sessionId=$session' "$case_dir/scenario" > "$case_dir/scenario-next"
 mv "$case_dir/scenario-next" "$case_dir/scenario"
 args='["--mode=ask","replacement prompt"]'
 params=$(jq -cn --arg id "$id" --arg cwd "$case_dir/cwd" --argjson args "$args" '{instance_id:$id,agent:"cursor",cwd:$cwd,no_pre_trust:true,reuse_finished:true,agent_args:$args}')
 ir_launch "$surface" spawn "$params" > "$case_dir/public.json"
 check task2.reuse-completion wait_until ir_task2_completed 1
 check task2.exact-prompts frames_task2_prompts "$prompt"
 check task2.authoritative-modes frames_task2_modes
 rl_public "$surface" pause >/dev/null
 local accepted_id=$id
 id="refused-$id"
 params=$(jq -cn --arg id "$id" --arg cwd "$case_dir/cwd" '{instance_id:$id,agent:"cursor",cwd:$cwd,no_pre_trust:true,agent_args:["acp","--"]}')
 if ir_launch "$surface" spawn "$params" > "$case_dir/refusal-out" 2> "$case_dir/refusal-error"; then echo 'malformed Cursor delimiter launched' >&2; return 1; fi
 check task2.static-named-refusal grep -qF ErrSpawnDeniedFlag "$case_dir/refusal-error"
 check task2.static-no-row test "$(sql "SELECT count(*) FROM spawns WHERE instance_id='$id';")" = 0
 id=$accepted_id
 ir_task2_ran=true
 pc_export_evidence
}
ir_task2_completed() {
 local path
 path=$(sql "SELECT jsonl_path FROM spawns WHERE instance_id='$id';")
 [[ -n $path && -f $path && $(row_state) == waiting ]] || return 1
 [[ $(jq -s '[.[]|select(.kind=="turn_outcome")]|length' "$path") == "$1" ]] || return 1
 ad read-pane --instance-id "$id" --n-lines 200 | jq -e '.pane|contains("Cursor turn complete")' >/dev/null
}
frames_task2_order() {
 local want='["initialize","session/new","session/set_mode","session/prompt"]'
 if [[ $1 == resume ]]; then want='["initialize","session/new","session/set_mode","session/prompt","initialize","session/load","session/set_mode","session/prompt"]'; fi
 frames | jq -se --argjson want "$want" '[.[]|select(has("method"))|.method]==$want' >/dev/null
}
frames_task2_prompts() { frames | jq -se --arg prompt "$1" '[.[]|select(.method=="session/prompt")|.params.prompt[0].text]==[$prompt,$prompt,"replacement prompt"]' >/dev/null; }
frames_task2_modes() { frames | jq -se '[.[]|select(.method=="session/set_mode")|.params.modeId]==["plan","plan","ask"] and ([.[]|select(.method=="session/new")]|length)==2 and ([.[]|select(.method=="session/load")]|length)==1' >/dev/null; }
ir_finish() {
 jq -cn --arg case "$1" --arg id "$id" --argjson task2 "${ir_task2_ran:-false}" '{case:$case,status:"pass",checks:(["current-runtime-effect","no-config-rewrite","saved-resume-values-or-fresh-conversation","bad-path-never-falls-back"]+(if $task2 then ["Task2-AC2-AC3-AC7-SR1.2","authoritative-fresh-load-reuse-modes","saved-initial-exact-once-per-launch","static-refusal-no-row"] else [] end)),provenance:{runtime:"synthetic-ACP",terminal:"actual-tmux-scripted",authenticated_cursor:false,native_capture:false},facts:{instance_id:$id,task2:$task2}}'
}
