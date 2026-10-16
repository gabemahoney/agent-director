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
 mkdir -m 700 -p "$artifact_dir"
 for name in trace.jsonl sent.jsonl raw raw-before-guard scenario want.json initial.json artifact-sha256 inspection.json public.json held-ids refused-ids; do
  if [[ -f $case_dir/$name ]]; then cp "$case_dir/$name" "$artifact_dir/$name"; chmod 600 "$artifact_dir/$name"; fi
 done
 if [[ -n ${path:-} && -f $path ]]; then cp "$path" "$artifact_dir/protected.jsonl"; chmod 600 "$artifact_dir/protected.jsonl"; fi
 if [[ -f $STORE ]]; then
  sql -readonly -json "SELECT * FROM spawns WHERE instance_id='$id'; SELECT * FROM cursor_runtimes WHERE bound_instance_id='$id'; SELECT * FROM permission_requests WHERE instance_id='$id'; SELECT * FROM session_history WHERE instance_id='$id';" > "$artifact_dir/authority.jsonl"
  chmod 600 "$artifact_dir/authority.jsonl"
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
pc_finish() {
 for decoy in "$case_dir/configured.db" "$HOME/.config/agent-director/state.db"; do
  check store.decoy test "$(/opt/driver/sql.sh "$decoy" 'SELECT count(*) FROM spawns; SELECT value FROM collector_decoy;')" = $'0\nuntouched'
 done
 check owned.sentinel kill -0 "$control_sentinel"
 pc_export_evidence
 jq -cn --arg case "$1" --argjson checks "$2" --arg id "$id" --arg session "$session" --arg path "$path" --argjson runtime "$runtime" --argjson gateway "$gateway" \
  '{case:$case,status:"pass",checks:$checks,provenance:{runtime:"synthetic-ACP",terminal:"actual-tmux-scripted",clients:"shipped-CLI/MCP-or-explicit-installed-Bun",authenticated_cursor:false,human_terminal:false},facts:{instance_id:$id,initial_session_id:$session,transcript:$path,runtime_pid:$runtime,gateway_pid:$gateway,raw_artifacts:("/tmp/cursor-pane-controls-artifacts/"+$id)}}'
}
pc_public() { local surface=$1 verb=$2; shift 2; timeout 35s bun --no-install "$SOURCE_DIR/test/driver/cursor-pane-public.mjs" "$surface" "$verb" "$HOME" "$STORE" "$@"; }
pc_send() { pc_public "$1" send-keys "$(jq -cn --arg id "$id" --arg text "$2" '{instance_id:$id,text:$text}')"; }
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
 check_id=installed.package.build
 mkdir -m 700 "$case_dir/pkg" "$case_dir/consumer" "$case_dir/pack"
 # Copy package inputs only; no writes to the read-only source mount or shared dist.
 for entry in package.json bun.lock bunfig.toml build.ts tsconfig.json tsconfig.typecheck.json version-floor.json README.md src; do cp -R "$SOURCE_DIR/pkg/ts-bun-client/$entry" "$case_dir/pkg/"; done
 (cd "$case_dir/pkg"; bun install --frozen-lockfile --no-progress >&2; bun run build >&2; bun pm pack --destination "$case_dir/pack" >&2)
 printf '{"name":"pane-controls-consumer","type":"module"}\n' > "$case_dir/consumer/package.json"
 mapfile -t tarballs < <(find "$case_dir/pack" -maxdepth 1 -name '*.tgz')
 check installed.one-tarball test "${#tarballs[@]}" = 1
 (cd "$case_dir/consumer"; bun add "${tarballs[0]}" --no-progress >&2)
 cp "$SOURCE_DIR/test/driver/cursor-pane-public.mjs" "$case_dir/consumer/caller.mjs"
 export PC_INSTALLED_CALLER="$case_dir/consumer/caller.mjs"
 sha256sum "${tarballs[0]}" "$(command -v agent-director)" > "$case_dir/artifact-sha256"
}
