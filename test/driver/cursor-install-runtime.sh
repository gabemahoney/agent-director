#!/usr/bin/env bash
# E5 Task1 composes the existing installer and synthetic Cursor public helpers.
source /work/source/test/driver/cursor-resume-life.sh

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
 elif [[ $(jq -r .reuse_finished <<<"$params") == true ]]; then rl_reuse_call CLI cursor
 else ad spawn --instance-id "$id" --agent cursor --cwd "$case_dir/cwd" --no-pre-trust
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
ir_finish() {
 jq -cn --arg case "$1" --arg id "$id" '{case:$case,status:"pass",checks:["current-runtime-effect","no-config-rewrite","saved-resume-values-or-fresh-conversation","bad-path-never-falls-back"],provenance:{runtime:"synthetic-ACP",terminal:"actual-tmux-scripted",authenticated_cursor:false,native_capture:false},facts:{instance_id:$id}}'
}
