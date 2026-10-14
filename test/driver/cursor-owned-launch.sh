#!/usr/bin/env bash
# Shared isolated synthetic ACP setup; sourced only by cursor-owned-launch cases.
set -euo pipefail
[[ ${AGENT_DIRECTOR_TEST_HARNESS:-} == 1 ]] || { echo 'Docker harness required' >&2; return 2; }
[[ $(uname -s) == Linux ]] || { echo 'Linux lifetime proof required; other platforms pending' >&2; return 2; }
for dependency in node bun tmux jq; do command -v "$dependency" >/dev/null; done
SOURCE_DIR=/work/source
[[ -r $SOURCE_DIR/internal/testsupport/cursorfix/runtime.js ]]
case_dir=$(mktemp -d /tmp/co-XXXXXX)
export HOME="$case_dir/home" TMUX_TMPDIR="$case_dir/tmux"
unset TMUX TMUX_PANE AGENT_DIRECTOR_INSTANCE_ID AGENT_DIRECTOR_AGENT AGENT_DIRECTOR_CURSOR_STORE_PATH
mkdir -m 700 -p "$HOME/.config/agent-director" "$TMUX_TMPDIR" "$case_dir/cwd"
STORE="$case_dir/override.db"
id="co-$(node -e 'console.log(require("crypto").randomUUID())')"
export AD_CURSOR_FIX_SETTINGS
AD_CURSOR_FIX_SETTINGS=$(jq -cn --arg dir "$case_dir" '{trace:($dir+"/trace.jsonl"),pid:($dir+"/runtime-pid"),release:($dir+"/release"),worker:false,workerPID:($dir+"/worker-pid"),workerRelease:($dir+"/worker-release"),scenario:($dir+"/scenario")}')
export AD_CURSOR_FIX_SOURCE="$SOURCE_DIR/internal/testsupport/cursorfix/runtime.js"
cat > "$case_dir/index.js" <<'RUNTIME'
#!/bin/sh
exec node -e 'global.SETTINGS=JSON.parse(process.env.AD_CURSOR_FIX_SETTINGS);require(process.env.AD_CURSOR_FIX_SOURCE)' "$@"
RUNTIME
chmod 700 "$case_dir/index.js"
cat > "$HOME/.config/agent-director/config.toml" <<CONFIG
[launch]
cursor_path = "$case_dir/index.js"
[store]
db_path = "$case_dir/configured.db"
[pause]
timeout_seconds = 2
CONFIG
socket="$TMUX_TMPDIR/tmux-$(id -u)/default"
cleanup_cursor_case() { tmux -S "$socket" kill-server >/dev/null 2>&1 || true; }
trap cleanup_cursor_case EXIT
ad() { local verb=$1; shift; agent-director "$verb" --home "$HOME" --store-path "$STORE" "$@"; }
sql() { /opt/driver/sql.sh "$STORE" "$@"; }
row_state() { sql "SELECT state FROM spawns WHERE instance_id='$id';"; }
wait_state() { local want=$1 deadline=$((SECONDS+15)); while [[ $(row_state) != "$want" ]]; do (( SECONDS < deadline )); sleep 0.05; done; }
load_identity() {
 gateway=$(sql "SELECT pane_pid FROM spawns WHERE instance_id='$id';")
 pane=$(sql "SELECT pane_id FROM spawns WHERE instance_id='$id';")
 runtime=$(sql "SELECT bound_runtime_pid FROM cursor_runtimes WHERE bound_instance_id='$id';")
 session=$(sql "SELECT session_id FROM spawns WHERE instance_id='$id';")
 [[ $gateway =~ ^[0-9]+$ && $runtime =~ ^[0-9]+$ && $pane =~ ^%[0-9]+$ && -n $session ]]
 [[ $(sql "SELECT count(*) FROM spawns s JOIN cursor_runtimes c ON c.bound_instance_id=s.instance_id WHERE s.instance_id='$id' AND s.agent='cursor' AND c.bound_launch_token=s.launch_token AND c.bound_life_number=s.life_number AND c.bound_gateway_pid=s.pane_pid AND c.bound_gateway_start=s.pane_starttime AND c.bound_session_id=s.session_id;") == 1 ]]
}
proc_gone() {
 node -e 'const fs=require("fs");try{const s=fs.readFileSync("/proc/"+process.argv[1]+"/stat","utf8");process.exit(s.slice(s.lastIndexOf(")")+2).startsWith("Z ")?0:1)}catch(e){if(e.code==="ENOENT")process.exit(0);throw e}' "$1"
}
wait_gone() { local pid=$1 deadline=$((SECONDS+15)); until proc_gone "$pid"; do (( SECONDS < deadline )); sleep 0.05; done; }
spawn_cursor() { ad spawn --agent cursor --instance-id "$id" --cwd "$case_dir/cwd" --no-pre-trust "$@" | jq -e --arg id "$id" '.instance_id==$id and .pre_trust=="skipped"' >/dev/null; }
frames() { if [[ -f $case_dir/trace.jsonl ]]; then cat "$case_dir/trace.jsonl"; fi; }
evidence() {
 jq -cn --arg case "$1" --arg assertions "$2" '{case:$case,status:"pass",checks:$assertions,authenticated_cursor:false}'
}
