#!/usr/bin/env bash
# no_screen_change acceptance in the Docker harness (pane-stuck Epic): the
# shipped binary samples a stand-in agent's real tmux pane on reads and shows
# the episode to independent processes; a per-spawn override persists across
# calls and wins over the configuration; Cursor rows report the capability
# unavailable while their permission requests still qualify.
#
# Expected wire values come from the shared hand-authored fixtures
# (cmd/agent-director/testdata/stuck/pane.json and permission.json) the unit,
# CLI, MCP and TypeScript tests also use. Every store access goes through
# /opt/driver/sql.sh. The committed pane baseline has no public writer but a
# read, so a case backdates its start (pane_since_ms) with sql.sh instead of
# waiting out a timeout; the Cursor case seeds its rows and request with
# sql.sh, as permission-stuck does. The binary creates the schema, starts every
# launch, makes every observation and commits every baseline. Agents are the
# stand-ins (no credentials); a hook is typed into the stand-in's pane with
# /opt/driver/pane-hook.sh.
#
# Observation writes are best effort, so each expected commit is confirmed by
# spawns.stuck_version, re-reading a bounded number of times. Stdout is one
# compact JSON evidence line (the harness keeps 200 characters). Exit 77 with
# availability:unavailable means the binary lacks the schema, never a pass.
set -euo pipefail

[[ "${AGENT_DIRECTOR_TEST_HARNESS:-}" == 1 ]] || { echo "Docker harness required" >&2; exit 2; }
unset ANTHROPIC_API_KEY CLAUDE_CODE_OAUTH_TOKEN CODEX_HOME

mode="${1:?usage: pane-stuck.sh public|override|cursor}"
dir="$HOME/.config/agent-director"
db="$dir/state.db"
config="$dir/config.toml"
fixtures=/work/source/cmd/agent-director/testdata/stuck
started_ms="$(date +%s%3N)"
launched=()
work=()
last=""

fail() { echo "FAIL: $*" >&2; exit 1; }
q() { printf "'%s'" "${1//\'/\'\'}"; }
sql() { /opt/driver/sql.sh "$db" "$1"; }
sqlr() { /opt/driver/sql.sh -readonly "$db" "$1"; }
col() { sqlr "SELECT $2 FROM spawns WHERE instance_id = $(q "$1");"; }

cleanup() {
    local id row sock pane s
    for id in "${launched[@]}"; do
        row="$(col "$id" "IFNULL(tmux_socket,'') || '|' || IFNULL(pane_id,'')" 2>/dev/null || true)"
        sock="${row%%|*}"
        pane="${row#*|}"
        if [[ -n "$pane" ]]; then
            s="$(tmux ${sock:+-S "$sock"} display-message -p -t "$pane" '#{session_id}' 2>/dev/null || true)"
            [[ -z "$s" ]] || tmux ${sock:+-S "$sock"} kill-session -t "$s" 2>/dev/null || true
        fi
        rm -rf "/tmp/stand-in-codex/$id"
    done
    rm -f "$config"
    rm -rf "${work[@]}"
}
trap cleanup EXIT

# The tested binary initializes the store; a binary without the columns has
# no no_screen_change to test.
mkdir -p "$dir"
agent-director list >/dev/null || fail "agent-director list could not open the store"
have="$(sqlr "SELECT group_concat(name) FROM pragma_table_info('spawns');")"
for c in no_screen_change_timeout_seconds pane_sha256 pane_since_ms pane_epoch; do
    case ",$have," in
    *",$c,"*) ;;
    *)
        jq -cn --arg c "$c" '{availability: "unavailable", capability: "no_screen_change", missing: $c}'
        exit 77
        ;;
    esac
done

# write_config TIMEOUT: no settling, the reader's no_screen_change timeout and
# the Codex stand-in as codex.
write_config() {
    printf '[state]\nsettle_ms = 0\nno_screen_change_timeout_seconds = %s\npermission_unanswered_timeout_seconds = 30\n[launch]\ncodex_path = "/opt/driver/stand-in-codex/codex"\n' "$1" >"$config"
}

# read_row VERB ID: the verb's JSON result for ID; the call must exit 0.
read_row() {
    local out
    out="$(agent-director "$1" --instance-id "$2")" || fail "$1 $2 exited non-zero"
    printf '%s' "$out"
}

version() { col "$1" stuck_version; }

# observe ID: a status read that commits exactly one observation, re-read at
# most 5 times while a best-effort write is skipped; its result is in last.
observe() {
    local id="$1" before now n
    before="$(version "$id")"
    for n in 1 2 3 4 5; do
        last="$(read_row status "$id")"
        now="$(version "$id")"
        if [[ "$now" != "$before" ]]; then
            [[ "$now" == "$((before + 1))" ]] || fail "$id: stuck_version $before -> $now; want one commit"
            return 0
        fi
    done
    fail "$id: status never committed an observation in 5 reads (stuck_version $before)"
}

# wait_raw ID WANT: the row's raw state (read through sql.sh, so no read
# samples the pane) is WANT within 10 s.
wait_raw() {
    local end got=""
    end=$(($(date +%s) + 10))
    while :; do
        got="$(col "$1" state)"
        [[ "$got" != "$2" ]] || return 0
        (($(date +%s) < end)) || fail "$1 raw state is [$got]; want [$2] within 10 s"
        sleep 0.2
    done
}

# launch AGENT ID [SPAWN_ARGS...]: spawn the stand-in AGENT as ID and type its
# SessionStart and UserPromptSubmit into its pane, so the row is raw working
# on its own real pane.
launch() {
    local agent="$1" id="$2"
    shift 2
    local cwd sid tp home out event extra payload end
    local spawn_extra=() hook_args=()
    cwd="$(mktemp -d /tmp/pane-stuck-XXXXXX)"
    cwd="$(cd -P "$cwd" && pwd)"
    work+=("$cwd")
    sid="$(cat /proc/sys/kernel/random/uuid)"
    tp="$cwd/transcript.jsonl"
    if [[ "$agent" == codex ]]; then
        home="$(mktemp -d)"
        work+=("$home")
        tp="$home/sessions/2026/10/20/rollout-2026-10-20T00-00-00-$sid.jsonl"
        mkdir -p "$(dirname "$tp")"
        printf '{"type":"session_meta"}\n' >"$tp"
        spawn_extra=(--extra-env "CODEX_HOME=$home")
        hook_args=(--agent codex)
    fi
    launched+=("$id")
    out="$(PATH="/opt/driver/stand-in-claude:$PATH" agent-director spawn --agent "$agent" --cwd "$cwd" --instance-id "$id" \
        --tmux-session-name "cd-$id" --no-pre-trust "${spawn_extra[@]}" "$@" 2>/dev/null)" || fail "spawn $id exited non-zero"
    [[ "$(jq -r .instance_id <<<"$out")" == "$id" ]] || fail "spawn $id echoed [$out]"
    if [[ "$agent" == codex ]]; then
        end=$(($(date +%s) + 10))
        until [[ -s "/tmp/stand-in-codex/$id/pid" ]]; do
            (($(date +%s) < end)) || fail "no Codex stand-in record for $id within 10 s"
            sleep 0.1
        done
    fi
    for event in SessionStart UserPromptSubmit; do
        extra='{"source":"startup"}'
        [[ "$event" == SessionStart ]] || extra='{"prompt":"hello","turn_id":"turn-1"}'
        payload="$(jq -cn --arg e "$event" --arg sid "$sid" --arg tp "$tp" --arg cwd "$cwd" --argjson x "$extra" \
            '{hook_event_name: $e, session_id: $sid, transcript_path: $tp, cwd: $cwd, model: "stand-in", permission_mode: "default"} + $x')"
        /opt/driver/pane-hook.sh "${hook_args[@]}" "$id" "$payload" >/dev/null || fail "$event hook on $id exited non-zero"
    done
    wait_raw "$id" working
}

# baseline ID: status reads until the committed baseline is the fingerprint
# read-pane's default read reports for the same pane (the stand-in's prompt
# may still be drawing after its last hook), at most 5 commits.
baseline() {
    local id="$1" n sha
    for n in 1 2 3 4 5; do
        observe "$id"
        [[ "$(jq -r .state <<<"$last")" == working ]] || fail "$id: first reads show [$last]; want working"
        sha="$(read_row read-pane "$id" | jq -r .pane_sha256)"
        [[ "$(col "$id" pane_sha256)" != "$sha" ]] || return 0
    done
    fail "$id: the committed baseline never matched read-pane's pane_sha256"
}

# backdate ID MS: move the committed baseline's start MS milliseconds back.
backdate() { sql "UPDATE spawns SET pane_since_ms = pane_since_ms - $2 WHERE instance_id = $(q "$1");"; }

# check_pane ROW SESSION MINUTES: a displayed no_screen_change status or get
# row: the fixture's state and reason, MINUTES, no permission detail, the
# attach target and a since made during this case in the fixture's format.
check_pane() {
    local since_ms
    jq -e --slurpfile w "$fixtures/pane.json" --arg s "$2" --argjson m "$3" \
        '.state == $w[0].state and .stuck_reason == $w[0].stuck_reason and .unchanged_minutes == $m and .tmux_session_name == $s
         and .no_screen_change_availability == "available" and (has("request_token") | not) and (has("tool_name") | not)
         and (.since | test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}\\.[0-9]{3}Z$"))' <<<"$1" >/dev/null ||
        fail "not the displayed no_screen_change of $2 with $3 minutes: $1"
    since_ms="$(date -d "$(jq -r .since <<<"$1")" +%s%3N)"
    ((since_ms >= started_ms)) || fail "since $(jq -r .since <<<"$1") precedes this case"
}

case "$mode" in
public)
    write_config 1200
    minutes=() entries=()
    for agent in claude codex; do
        id="pane-$agent"
        launch "$agent" "$id"
        baseline "$id"
        backdate "$id" 7200000
        v="$(version "$id")"
        observe "$id"
        entries+=("$(($(version "$id") - v))")
        status="$last"
        check_pane "$status" "cd-$id" 120
        get="$(read_row get "$id")"
        check_pane "$get" "cd-$id" 120
        [[ "$(jq -r .since <<<"$get")" == "$(jq -r .since <<<"$status")" ]] || fail "$agent: get since differs from status"
        jq -e '(.permission_requests | type == "array") and (.unproven_requests | type == "array")' <<<"$get" >/dev/null ||
            fail "$agent: get lost its request arrays"
        v="$(version "$id")"
        listed="$(agent-director list --state stuck)" || fail "list exited non-zero"
        jq -e --arg id "$id" '[.spawns[] | select(.instance_id == $id)] | length == 1 and (.[0] | .state == "stuck"
            and .stuck_reason == "no_screen_change" and .no_screen_change_availability == "available"
            and (has("since") | not) and (has("unchanged_minutes") | not))' <<<"$listed" >/dev/null ||
            fail "$agent: list row: $listed"
        [[ "$(version "$id")" == "$v" ]] || fail "$agent: list re-committed the unchanged episode"
        [[ "$(col "$id" state)" == working ]] || fail "$agent: the raw state moved"
        minutes+=("$(jq .unchanged_minutes <<<"$status")")
    done
    jq -cn --argjson m "$(printf '%s\n' "${minutes[@]}" | jq -sc .)" --argjson e "$(printf '%s\n' "${entries[@]}" | jq -sc .)" \
        '{case: "public", agents: ["claude", "codex"], minutes: $m, entry_stuck_version_delta: $e, raw: "working"}'
    ;;
override)
    write_config 60
    refused=0
    for bad in -1 1.5 x 9223372037; do
        id="override-refused-$refused"
        if err="$(PATH="/opt/driver/stand-in-claude:$PATH" agent-director spawn --agent claude --cwd /tmp --instance-id "$id" \
            --no-pre-trust --no-screen-change-timeout-seconds "$bad" 2>&1 >/dev/null)"; then
            fail "spawn accepted --no-screen-change-timeout-seconds $bad"
        fi
        [[ "$(tail -n 1 <<<"$err" | jq -r .err_name)" == ErrInvalidFlags ]] || fail "$bad: refusal [$err]; want ErrInvalidFlags"
        [[ "$(sqlr "SELECT COUNT(*) FROM spawns WHERE instance_id = $(q "$id");")" == 0 ]] || fail "$bad: the refusal wrote a row"
        refused=$((refused + 1))
    done
    id=override
    launch claude "$id" --no-screen-change-timeout-seconds 7200
    [[ "$(col "$id" no_screen_change_timeout_seconds)" == 7200 ]] || fail "the override was not recorded"
    baseline "$id"
    # An hour unchanged: the config's 60 s would qualify; the row's 2 h does not.
    backdate "$id" 3600000
    early="$(jq -r '[.state, .stuck_reason // "-"] | join("/")' <<<"$(read_row status "$id")")"
    [[ "$early" == working/- ]] || fail "an hour unchanged under a 2 h override reads [$early]"
    backdate "$id" 7200000
    observe "$id"
    check_pane "$last" "cd-$id" 180
    get="$(read_row get "$id")"
    check_pane "$get" "cd-$id" 180
    [[ "$(col "$id" no_screen_change_timeout_seconds)" == 7200 ]] || fail "reads changed the override"
    jq -cn --argjson r "$refused" --arg e "$early" --argjson m "$(jq .unchanged_minutes <<<"$get")" \
        '{case: "override", refused: $r, override: 7200, at_1h: $e, at_3h_minutes: $m, config: 60}'
    ;;
cursor)
    write_config 60
    old=1577836800000 # 2020-01-01, long before any timeout
    sql "INSERT INTO spawns(instance_id,state,settled_state,state_changed_ms,cwd,agent,relay_mode,tmux_session_name,launch_token,permission_epoch,no_screen_change_timeout_seconds,pane_sha256,pane_since_ms,pane_epoch)
         VALUES('cursor-quiet','working','working',0,'/tmp','cursor','on','ts-cursor-quiet','2222222222222222',7,1,'$(printf 'ab%.0s' {1..32})',$old,7)"
    before="$(col cursor-quiet "stuck_version || '|' || IFNULL(stuck_observation,'') || '|' || pane_since_ms")"
    for verb in status get; do
        row="$(read_row "$verb" cursor-quiet)"
        jq -e '.state == "working" and .no_screen_change_availability == "unavailable" and (has("stuck_reason") | not)
            and (has("unchanged_minutes") | not)' <<<"$row" >/dev/null || fail "Cursor quiet $verb: $row"
    done
    [[ "$(col cursor-quiet "stuck_version || '|' || IFNULL(stuck_observation,'') || '|' || pane_since_ms")" == "$before" ]] ||
        fail "a Cursor row was observed by its pane"
    token="$(jq -r .request_token "$fixtures/permission.json")"
    tool="$(jq -r .tool_name "$fixtures/permission.json")"
    sql "INSERT INTO spawns(instance_id,state,settled_state,state_changed_ms,cwd,agent,relay_mode,tmux_session_name,launch_token,permission_epoch)
         VALUES('cursor-asks','check_permission','check_permission',0,'/tmp','cursor','on','ts-cursor-asks','1111111111111111',7)"
    sql "INSERT INTO permission_requests(instance_id,request_token,tool_name,tool_input,created_at,settled_at,owner_launch_token,owner_permission_epoch,admitted_at,fallback_at,fallback_basis)
         VALUES('cursor-asks',$(q "$token"),$(q "$tool"),'{}','2019-12-31 23:59:00',$old,'1111111111111111',7,$((old - 1000)),$old,'deadline')"
    observe cursor-asks
    jq -e --slurpfile w "$fixtures/permission.json" '.state == $w[0].state and .stuck_reason == $w[0].stuck_reason
        and .request_token == $w[0].request_token and .no_screen_change_availability == "unavailable"
        and (has("unchanged_minutes") | not)' <<<"$last" >/dev/null || fail "Cursor permission: $last"
    sql "INSERT INTO spawns(instance_id,state,settled_state,state_changed_ms,cwd,agent,relay_mode,tmux_session_name,launch_token)
         VALUES('codex-ended','ended','ended',0,'/tmp','codex','off','ts-codex-ended','3333333333333333')"
    listed="$(agent-director list)" || fail "list exited non-zero"
    jq -e '[.spawns[] | [.instance_id, .no_screen_change_availability]] | sort ==
        [["codex-ended", "available"], ["cursor-asks", "unavailable"], ["cursor-quiet", "unavailable"]]' <<<"$listed" >/dev/null ||
        fail "availability on every row: $listed"
    jq -cn --arg q "$(jq -r '[.state, .no_screen_change_availability] | join("/")' <<<"$(read_row status cursor-quiet)")" \
        --arg a "$(jq -r '[.stuck_reason, .no_screen_change_availability] | join("/")' <<<"$last")" \
        '{case: "cursor", quiet_since_2020: $q, asks: $a, list: 3}'
    ;;
*) fail "unknown mode $mode" ;;
esac
