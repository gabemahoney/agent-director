#!/usr/bin/env bash
# Only cursor-pane-controls owns these compositions. Reuse the isolated E1/E2 harness.
source /work/source/test/driver/cursor-owned-launch.sh
export AD_CURSOR_FIX_SETTINGS
AD_CURSOR_FIX_SETTINGS=$(jq --arg p "$case_dir/new-release" '.+{newRelease:$p}' <<<"$AD_CURSOR_FIX_SETTINGS")
check_id=setup
check() { check_id=$1; shift; if ! "$@"; then printf 'ASSERTION_FAILED id=%s instance=%s session=%s dir=%s\n' "$check_id" "$id" "${session:-}" "$case_dir" >&2; return 1; fi; }
# Preserve the existing cleanup and driver's private ERR/xtrace channels. Print
# the stable check before its evidence/owned-process cleanup on any shell error.
trap 'rc=$?; if ((rc)); then printf "ASSERTION_FAILED id=%s instance=%s dir=%s\n" "$check_id" "$id" "$case_dir" >&2; fi; set +e; pc_export_evidence || true; (exit "$rc"); cleanup_cursor_case; exit "$rc"' EXIT
pc_export_evidence() {
 # The scheduler can bind-mount this fixed test-only path for raw receipt
 # retention; the standard driver's short details field is not a raw archive.
 artifact_dir="/tmp/cursor-pane-controls-artifacts/$id"
 mkdir -m 700 -p "$artifact_dir" || return
 for name in trace.jsonl sent.jsonl raw raw-before-guard scenario want.json initial.json artifact-sha256 source-sha256 inspection.json public.json task3-timing.jsonl held-ids refused-ids; do
  if [[ -f $case_dir/$name ]]; then cp "$case_dir/$name" "$artifact_dir/$name" || return; chmod 600 "$artifact_dir/$name" || return; fi
 done
 pc_export_launch_records "$artifact_dir" "$STORE" "$id" "${path:-}"
}
pc_export_launch_records() {
 local out=$1 store=$2 instance=$3 transcript=$4
 if [[ -n $transcript && -f $transcript ]]; then cp "$transcript" "$out/protected.jsonl" || return; chmod 600 "$out/protected.jsonl" || return; fi
 if [[ -f $store ]]; then
  /opt/driver/sql.sh "$store" -readonly -json "SELECT * FROM spawns WHERE instance_id='$instance'; SELECT * FROM cursor_runtimes WHERE bound_instance_id='$instance'; SELECT * FROM permission_requests WHERE instance_id='$instance'; SELECT * FROM session_history WHERE instance_id='$instance';" > "$out/authority.jsonl" || return
  chmod 600 "$out/authority.jsonl" || return
 fi
}
pc_start() {
 spawn_cursor --relay-mode on
 wait_state waiting
 load_identity
 # SessionStart publishes waiting before pane input is ready. Observe the same
 # explicit ready notice used by newCursorPaneScenario before any case input.
 check startup.ready wait_until pc_start_ready
 path=$(sql "SELECT jsonl_path FROM spawns WHERE instance_id='$id';")
 initial=$(sql -json "SELECT session_id,life_number,launch_token,pane_pid,pane_starttime,jsonl_path FROM spawns WHERE instance_id='$id';")
 printf '%s\n' "$initial" > "$case_dir/initial.json"
 # Decoys have valid current schema but distinct stores; callers must never
 # route to either the configured path or the default instead of the override.
 for decoy in "$case_dir/configured.db" "$HOME/.config/agent-director/state.db"; do
  agent-director list --home "$HOME" --store-path "$decoy" >/dev/null
  /opt/driver/sql.sh "$decoy" 'CREATE TABLE collector_decoy(value TEXT); INSERT INTO collector_decoy VALUES ("untouched");'
 done
}
pc_assert_stores_untouched() {
 for decoy in "$case_dir/configured.db" "$HOME/.config/agent-director/state.db"; do
  check store.decoy test "$(/opt/driver/sql.sh "$decoy" 'SELECT count(*) FROM spawns; SELECT value FROM collector_decoy;')" = $'0\nuntouched'
 done
 check owned.sentinel kill -0 "$control_sentinel"
}
pc_finish() {
 pc_assert_stores_untouched
 pc_export_evidence
 jq -cn --arg case "$1" --argjson checks "$2" --arg id "$id" --arg session "$session" --arg path "$path" --argjson runtime "$runtime" --argjson gateway "$gateway" \
  '{case:$case,status:"pass",checks:$checks,provenance:{runtime:"synthetic-ACP",terminal:"actual-tmux-scripted",clients:"shipped-CLI/MCP-or-explicit-installed-Bun",authenticated_cursor:false,human_terminal:false},facts:{instance_id:$id,initial_session_id:$session,transcript:$path,runtime_pid:$runtime,gateway_pid:$gateway,raw_artifacts:("/tmp/cursor-pane-controls-artifacts/"+$id)}}'
}
pc_public() { local surface=$1 verb=$2; shift 2; local params=$1; if [[ $verb == spawn ]]; then params=$(jq -c --arg config "$CURSOR_CONFIG_DIR" --arg data "$CURSOR_DATA_DIR" 'if has("extra_env") then . else .extra_env={CURSOR_CONFIG_DIR:$config,CURSOR_DATA_DIR:$data} end' <<<"$params"); fi; timeout 35s bun --no-install "$SOURCE_DIR/test/driver/cursor-pane-public.mjs" "$surface" "$verb" "$HOME" "$STORE" "$params"; }
pc_send() { pc_public "$1" send-keys "$(jq -cn --arg id "$id" --arg text "$2" --argjson pending "${3:-false}" '{instance_id:$id,text:$text,allow_pending:$pending}')"; }
pc_terminal() { tmux -S "$socket" send-keys -t "$pane" -l -- "$1"; tmux -S "$socket" send-keys -t "$pane" Enter; }
pc_count() { jq -s --arg kind "$1" '[.[]|select(.kind==$kind)]|length' "$path"; }
pc_count_is() { [[ $(pc_count "$1") == "$2" ]]; }
pc_prompts_is() { [[ $(frames | jq -s '[.[]|select(.method=="session/prompt")]|length') == "$1" ]]; }
pc_start_ready() { ad read-pane --instance-id "$id" --n-lines 100 | jq -e '.pane|contains("Cursor ready")' >/dev/null; }
pc_rotation_published() {
 [[ $(pc_count rotation_published) == "$1" ]] || return 1
 ad read-pane --instance-id "$id" --n-lines 200 | jq -e --argjson n "$1" '(.pane|split("Cursor fresh conversation published")|length)-1==$n' >/dev/null
}
# Only after owned runtime/gateway death: the raw observer runs before readline.
# Exact equality accounts for every allowed complete control/permission frame
# and rejects even one unparsed partial byte, which frame counts cannot detect.
pc_raw_complete() { cmp "$case_dir/raw" "$case_dir/trace.jsonl"; }
pc_ready() {
 [[ $(row_state) == waiting && $(pc_count turn_outcome) == "$1" ]] || return 1
 ad read-pane --instance-id "$id" --n-lines 200 | jq -e --argjson n "$1" '(.pane|split("Cursor turn complete")|length)-1==$n' >/dev/null
}
pc_identity() { [[ $(sql -json "SELECT session_id,life_number,launch_token,pane_pid,pane_starttime,jsonl_path FROM spawns WHERE instance_id='$id';") == "$initial" ]]; }
pc_correlate() {
 # JSONL complete-line parsing; exact text, unique accepted/start/outcome IDs,
 # original session, launch/life attribution and raw protocol IDs are independent.
 check_id=prompt.correlation
 node "$SOURCE_DIR/test/driver/cursor-pane-records.cjs" "$path" "$case_dir/trace.jsonl" "$case_dir/raw" "$case_dir/want.json" "$initial" "$id"
}
pc_install_bun() {
 check_id=installed.package.provenance
 # Image build performed the real frozen install, canonical build (with tsc),
 # pack and tarball install. Compare all inputs, including added/removed src
 # files, before using its consumer; no package manager runs while offline.
 (cd "$SOURCE_DIR/pkg/ts-bun-client"; { printf '%s\0' package.json bun.lock bunfig.toml build.ts tsconfig.json tsconfig.typecheck.json version-floor.json README.md ../api/errnames/catalog.json; find src -type f -print0; } | sort -z | xargs -0 sha256sum) > "$case_dir/source-sha256"
 check installed.current-source cmp /opt/cursor-pane-bun/source-sha256 "$case_dir/source-sha256"
 mkdir -m 700 "$case_dir/consumer"
 cp -R /opt/cursor-pane-bun/consumer/. "$case_dir/consumer/"
 check installed.javascript test -s "$case_dir/consumer/node_modules/agent-director/dist/index.js"
 check installed.declarations test -s "$case_dir/consumer/node_modules/agent-director/dist/index.d.ts"
 mapfile -t tarballs < <(find /opt/cursor-pane-bun/pack -maxdepth 1 -name '*.tgz')
 check installed.one-tarball test "${#tarballs[@]}" = 1
 cp "$SOURCE_DIR/test/driver/cursor-pane-public.mjs" "$case_dir/consumer/caller.mjs"
 export PC_INSTALLED_CALLER="$case_dir/consumer/caller.mjs"
 sha256sum "${tarballs[0]}" "$(command -v agent-director)" > "$case_dir/artifact-sha256"
}
