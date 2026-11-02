#!/usr/bin/env bash
# idle_screen acceptance in the Docker harness (idle-screen Epic): the shipped
# binary reads the configured [state] idle_screen_timeout_seconds, samples a
# stand-in agent's real tmux pane on reads and shows the display to
# independent processes, as displayed_state beside state working, which
# --state working still matches (b.zpv); a per-spawn override persists across
# calls and wins over the configuration; Cursor rows report idle_screen
# unavailable, and a fallen-back Cursor permission request reads
# check_permission (b.zpv). Each displayed entry writes exactly one
# ad.spawn.displayed_state_transition trail record.
#
# Expected wire values come from the shared hand-authored fixture
# (cmd/agent-director/testdata/display/idle_screen.json) the unit, CLI, MCP and
# TypeScript tests also use.
# Every store access goes through /opt/driver/sql.sh. The committed pane
# baseline has no public writer but a read, so a case backdates its start
# (pane_since_ms) with sql.sh instead of waiting out a timeout; the Cursor case
# seeds its rows and request with sql.sh, as needs-intelligence does. The
# binary creates the schema, starts every launch, makes every observation and
# commits every baseline. Agents are the stand-ins (no credentials); a hook is
# typed into the stand-in's pane with /opt/driver/pane-hook.sh.
#
# Observation writes are best effort, so each expected commit is confirmed by
# spawns.observation_version, re-reading a bounded number of times. The trail
# is not reset between cases, so its records are selected by the case's own
# instance ids. Stdout is one compact JSON evidence line (the harness keeps 200
# characters). Exit 77 with availability:unavailable means the binary lacks
# the schema, never a pass.
set -euo pipefail

[[ "${AGENT_DIRECTOR_TEST_HARNESS:-}" == 1 ]] || { echo "Docker harness required" >&2; exit 2; }
unset ANTHROPIC_API_KEY CLAUDE_CODE_OAUTH_TOKEN CODEX_HOME

mode="${1:?usage: idle-screen.sh public|override|cursor}"
dir="$HOME/.config/agent-director"
db="$dir/state.db"
config="$dir/config.toml"
trail="$dir/ad-trail.jsonl"
fixtures=/work/source/cmd/agent-director/testdata/display
started_ms="$(date +%s%3N)"
launched=()
work=()
last=""

# Exact key sets (docs/caller-contract.md): a status row showing no Displayed
# State and showing idle_screen; a list row's keys present on a row showing
# idle_screen and those present only on some.
status_plain='["confirmations","context","family_state","idle_screen_availability","process_liveness","state"]'
status_idle='["confirmations","context","displayed_state","family_state","idle_screen_availability","process_liveness","since","state","tmux_session_name","unchanged_minutes"]'
list_keys='["agent","confirmations","cwd","displayed_state","family_state","idle_screen_availability","instance_id","labels","last_seen_at","permission_requests","process_liveness","relay_mode","started_at","state","state_changed_at","tmux_session_name"]'
list_optional='["ended_at","launch_started_at","liveness_note","liveness_unverified_since","parent_id"]'

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
# no idle_screen to test.
mkdir -p "$dir"
agent-director list >/dev/null || fail "agent-director list could not open the store"
have="$(sqlr "SELECT group_concat(name) FROM pragma_table_info('spawns');")"
for c in display_evidence observation_version display_observation display_exit idle_screen_timeout_seconds pane_sha256 pane_since_ms pane_epoch; do
    case ",$have," in
    *",$c,"*) ;;
    *)
        jq -cn --arg c "$c" '{availability: "unavailable", capability: "idle_screen", missing: $c}'
        exit 77
        ;;
    esac
done

# write_config TIMEOUT [SETTLE_MS]: the reader's idle_screen timeout and the
# Codex stand-in as codex.
write_config() {
    printf '[state]\nsettle_ms = %s\nidle_screen_timeout_seconds = %s\nneeds_intelligence_timeout_seconds = 30\n[launch]\ncodex_path = "/opt/driver/stand-in-codex/codex"\n' "${2:-0}" "$1" >"$config"
}

# read_row VERB ID: the verb's JSON result for ID; the call must exit 0.
read_row() {
    local out
    out="$(agent-director "$1" --instance-id "$2")" || fail "$1 $2 exited non-zero"
    printf '%s' "$out"
}

version() { col "$1" observation_version; }

# observe ID: a status read that commits exactly one observation, re-read at
# most 5 times while a best-effort write is skipped; its result is in last.
observe() {
    local id="$1" before now n
    before="$(version "$id")"
    for n in 1 2 3 4 5; do
        last="$(read_row status "$id")"
        now="$(version "$id")"
        if [[ "$now" != "$before" ]]; then
            [[ "$now" == "$((before + 1))" ]] || fail "$id: observation_version $before -> $now; want one commit"
            return 0
        fi
    done
    fail "$id: status never committed an observation in 5 reads (observation_version $before)"
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
    local cwd sid tp home out end
    local spawn_extra=() hook_args=()
    cwd="$(mktemp -d /tmp/idle-screen-XXXXXX)"
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
    hook_sid="$sid" hook_tp="$tp" hook_cwd="$cwd" hook_agent_args=("${hook_args[@]}")
    hook "$id" SessionStart '{"source":"startup"}'
    hook "$id" UserPromptSubmit '{"prompt":"hello","turn_id":"turn-1"}'
    wait_raw "$id" working
}

# hook ID EVENT EXTRA: type EVENT's payload, with EXTRA's members, into ID's
# pane as the latest launch's agent and session (hook_sid, hook_tp, hook_cwd,
# hook_agent_args); the hook must exit 0.
hook() {
    local payload
    payload="$(jq -cn --arg e "$2" --arg sid "$hook_sid" --arg tp "$hook_tp" --arg cwd "$hook_cwd" --argjson x "$3" \
        '{hook_event_name: $e, session_id: $sid, transcript_path: $tp, cwd: $cwd, model: "stand-in", permission_mode: "default"} + $x')"
    /opt/driver/pane-hook.sh "${hook_agent_args[@]}" "$1" "$payload" >/dev/null || fail "$2 hook on $1 exited non-zero"
}

# settle_working ID: give the latest launch ID's working stay a settled base of
# working through its own hooks alone (hookStateSet, internal/store/settle.go):
# a Stop under settle_ms 0 makes working the settled value, and the next prompt
# under settle_ms 60000 keeps it, because the brief waiting never settled. A
# read settling for 60 s then reports working, with this last stay's own raw
# record, while the display's exit is still settling. Run it before the
# baseline, so the stay it ends in is the one every later read reports.
settle_working() {
    hook "$1" Stop '{"stop_hook_active":false,"turn_id":"turn-1"}'
    wait_raw "$1" waiting
    write_config 600 60000
    hook "$1" UserPromptSubmit '{"prompt":"hello again","turn_id":"turn-2"}'
    wait_raw "$1" working
    write_config 600
    [[ "$(col "$1" settled_state)" == working ]] || fail "$1: settled base [$(col "$1" settled_state)]; want working"
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

# no_old_member ROW WHAT: the get row has no member of the single-state shape.
no_old_member() {
    jq -e 'has("stuck_reason") | not' <<<"$1" >/dev/null || fail "$2 carries a stuck_reason member: $1"
}

# check_idle VERB ROW SESSION MINUTES: a status or get row displaying
# idle_screen: the fixture's state and displayed_state, MINUTES, the attach
# target, availability available, no request detail (status: its exact key
# set) and a since made during this case in the fixture's format.
check_idle() {
    local since_ms
    jq -e --slurpfile w "$fixtures/idle_screen.json" --arg s "$3" --argjson m "$4" \
        '.state == $w[0].state and .displayed_state == $w[0].displayed_state and .unchanged_minutes == $m and .tmux_session_name == $s
         and .idle_screen_availability == "available" and (has("request_token") | not) and (has("tool_name") | not)
         and (.since | test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}\\.[0-9]{3}Z$"))' <<<"$2" >/dev/null ||
        fail "$1: not the displayed idle_screen of $3 with $4 minutes: $2"
    if [[ "$1" == status ]]; then
        jq -e --argjson k "$status_idle" 'keys == $k' <<<"$2" >/dev/null || fail "status: keys are not $status_idle: $2"
    else
        no_old_member "$2" "$1 of $3"
    fi
    since_ms="$(date -d "$(jq -r .since <<<"$2")" +%s%3N)"
    ((since_ms >= started_ms)) || fail "since $(jq -r .since <<<"$2") precedes this case"
}

# records ID: the trail's ad.spawn.displayed_state_transition records for ID,
# as a JSON array.
records() {
    [[ -f "$trail" ]] || { echo '[]'; return 0; }
    jq -cs --arg id "$1" '[.[] | select(.event == "ad.spawn.displayed_state_transition" and .instance_id == $id)]' "$trail"
}

# one_record ID PRIOR NEW SINCE: the trail holds exactly one displayed record
# for ID, with exactly its five keys, PRIOR -> NEW and ts SINCE.
one_record() {
    local r
    r="$(records "$1")"
    jq -e --arg p "$2" --arg n "$3" --arg ts "$4" 'length == 1 and (.[0] | keys == ["event", "instance_id", "new_state", "prior_state", "ts"]
        and .prior_state == $p and .new_state == $n and .ts == $ts)' <<<"$r" >/dev/null ||
        fail "$1: want one displayed record $2 -> $3 at $4; trail has $r"
}

# trail_clean: the trail holds no record of the single-state event, and no
# ordinary state_transition record names a Displayed State.
trail_clean() {
    [[ -f "$trail" ]] || return 0
    jq -se '[.[] | select(.event == "ad.spawn.stuck_transition")] | length == 0' "$trail" >/dev/null ||
        fail "the trail holds an ad.spawn.stuck_transition record"
    jq -se '[.[] | select(.event == "ad.spawn.state_transition")
        | select([.. | strings] | any(. == "idle_screen" or . == "needs_intelligence"))] | length == 0' "$trail" >/dev/null ||
        fail "an ad.spawn.state_transition record names needs_intelligence or idle_screen"
}

case "$mode" in
public)
    # 900 s unchanged: past the configured 600, short of the 1200 default.
    write_config 600
    minutes=() entries=()
    mkdir -p /tmp/idle-screen-artifacts
    evidence="/tmp/idle-screen-artifacts/public.jsonl"
    : >"$evidence"
    for agent in claude codex; do
        id="pane-$agent-$(cat /proc/sys/kernel/random/uuid)"
        launch "$agent" "$id"
        settle_working "$id"
        baseline "$id"
        backdate "$id" 900000
        v="$(version "$id")"
        observe "$id"
        entries+=("$(($(version "$id") - v))")
        status="$last"
        check_idle status "$status" "cd-$id" 15
        get="$(read_row get "$id")"
        check_idle get "$get" "cd-$id" 15
        [[ "$(jq -r .since <<<"$get")" == "$(jq -r .since <<<"$status")" ]] || fail "$agent: get since differs from status"
        jq -e '(.permission_requests | type == "array") and (.unproven_requests | type == "array")' <<<"$get" >/dev/null ||
            fail "$agent: get lost its request arrays"
        v="$(version "$id")"
        # Either filter, the Displayed State or the real state (b.zpv), lists the row.
        for filter in idle_screen working; do
            listed="$(agent-director list --state "$filter")" || fail "list --state $filter exited non-zero"
            jq -e --arg id "$id" --arg first "$(jq -r .since <<<"$status")" --argjson k "$list_keys" --argjson o "$list_optional" \
                '[.spawns[] | select(.instance_id == $id)] | length == 1 and (.[0] | .state == "working" and .displayed_state == "idle_screen"
                and .idle_screen_availability == "available" and (keys - $o) == $k
                and (.confirmations | keys == ["displayed_state","process_liveness","state"])
                and all(.confirmations[]; keys == ["checked_at","confirmed_at","reason","source","status"])
                and .confirmations.displayed_state.status == "confirmed" and .confirmations.displayed_state.source == "pane"
                and .confirmations.displayed_state.confirmed_at == $first and .confirmations.displayed_state.reason == null
                and .confirmations.displayed_state.checked_at >= $first)' <<<"$listed" >/dev/null ||
                fail "$agent: list --state $filter row: $listed"
        done
        [[ "$(version "$id")" == "$v" ]] || fail "$agent: list re-committed the unchanged display"
        [[ "$(col "$id" state)" == working ]] || fail "$agent: the raw state moved"
        one_record "$id" working idle_screen "$(jq -r .since <<<"$status")"
        minutes+=("$(jq .unchanged_minutes <<<"$status")")
        # b.x9o: independently launched readers report real owned samples.
        for reading in "$status" "$get"; do
            jq -e '.confirmations | keys == ["displayed_state","process_liveness","state"]
                and all(.[]; keys == ["checked_at","confirmed_at","reason","source","status"])' <<<"$reading" >/dev/null || fail "confirmation shape: $reading"
            jq -e '.confirmations.displayed_state as $d | $d.status == "confirmed" and $d.source == "pane" and $d.confirmed_at == .since
                and $d.checked_at >= $d.confirmed_at and $d.reason == null
                and .process_liveness == "alive" and .confirmations.process_liveness.status == "confirmed"
                and .confirmations.state.source == "agent_event"' <<<"$reading" >/dev/null || fail "owned pane evidence: $reading"
        done
        owner="$(col "$id" "launch_token || '|' || pane_id || '|' || pid || '|' || proc_starttime")"
        state_record="$(jq -c .confirmations.state <<<"$get")"
        sock="$(col "$id" tmux_socket)"
        pane="$(col "$id" pane_id)"
        session="$(tmux ${sock:+-S "$sock"} display-message -p -t "$pane" '#{session_id}')"
        label="$(tmux ${sock:+-S "$sock"} show-options -v -t "$session" @ad_owner)"
        [[ -n "$label" ]] || fail "$agent: session $session carries no @ad_owner label"
        # Lose only the session's ownership label, so the lookup no longer
        # finds the launch's own session and the pane sample fails. The actual
        # agent process and lifecycle stay alive/working; the display's exit
        # is still settling.
        write_config 600 60000
        tmux ${sock:+-S "$sock"} set-option -u -t "$session" @ad_owner
        observe "$id"
        failed="$last"
        jq -e --argjson prior "$get" --argjson state "$state_record" '.state == "working" and .displayed_state == "idle_screen"
            and .process_liveness == "alive" and .confirmations.process_liveness.status == "confirmed" and .confirmations.state == $state
            and .confirmations.displayed_state == {status:"not_confirmed", confirmed_at:$prior.confirmations.displayed_state.confirmed_at,
                source:"pane", checked_at:.confirmations.displayed_state.checked_at, reason:"unreadable"}
            and .confirmations.displayed_state.checked_at >= $prior.confirmations.displayed_state.checked_at' <<<"$failed" >/dev/null || fail "lost pane borrowed fresh evidence: $failed"
        [[ "$(col "$id" "launch_token || '|' || pane_id || '|' || pid || '|' || proc_starttime")" == "$owner" ]] || fail "pane observation changed the agent identity"
        [[ "$(col "$id" "state || '|' || settled_state")" == "working|working" ]] || fail "pane observation moved the lifecycle"
        stored="$(col "$id" display_evidence)"
        confirmed_ms="$(date -d "$(jq -r .confirmations.displayed_state.confirmed_at <<<"$failed")" +%s%3N)"
        checked_ms="$(date -d "$(jq -r .confirmations.displayed_state.checked_at <<<"$failed")" +%s%3N)"
        jq -e --argjson at "$confirmed_ms" --argjson checked "$checked_ms" '.v == "idle_screen" and .reason == "unreadable" and .src == "pane"
            and .at == $at and .since == $at and .checked == $checked' <<<"$stored" >/dev/null || fail "failed attempt not durable: $stored"
        jq -cn --arg agent "$agent" --arg id "$id" --arg owner "$owner" --argjson success "$get" --argjson listed "$listed" --argjson failed "$failed" --argjson stored "$stored" \
            '{agent:$agent,id:$id,owner:$owner,success:$success,listed:$listed,failed:$failed,stored:$stored}' >>"$evidence"
        tmux ${sock:+-S "$sock"} set-option -t "$session" @ad_owner "$label"
        [[ "$(tmux ${sock:+-S "$sock"} show-options -v -t "$session" @ad_owner)" == "$label" ]] || fail "$agent: @ad_owner not restored"
        write_config 600

    done
    jq -se 'map(.agent) == ["claude", "codex"]' "$evidence" >/dev/null || fail "public evidence lacks an agent's line"
    trail_clean
    jq -cn --argjson m "$(printf '%s\n' "${minutes[@]}" | jq -sc .)" --argjson e "$(printf '%s\n' "${entries[@]}" | jq -sc .)" \
        '{case: "public", agents: ["claude", "codex"], timeout: 600, minutes: $m, entry_commits: $e, entry_records: [1, 1], pane_loss: "not_confirmed/unreadable", raw: "working"}'
    ;;
override)
    write_config 60
    refused=0
    for bad in -1 1.5 x 9223372037; do
        id="override-refused-$refused"
        if err="$(PATH="/opt/driver/stand-in-claude:$PATH" agent-director spawn --agent claude --cwd /tmp --instance-id "$id" \
            --no-pre-trust --idle-screen-timeout-seconds "$bad" 2>&1 >/dev/null)"; then
            fail "spawn accepted --idle-screen-timeout-seconds $bad"
        fi
        [[ "$(tail -n 1 <<<"$err" | jq -r .err_name)" == ErrInvalidFlags ]] || fail "$bad: refusal [$err]; want ErrInvalidFlags"
        [[ "$(sqlr "SELECT COUNT(*) FROM spawns WHERE instance_id = $(q "$id");")" == 0 ]] || fail "$bad: the refusal wrote a row"
        refused=$((refused + 1))
    done
    id="override-$(cat /proc/sys/kernel/random/uuid)"
    launch claude "$id" --idle-screen-timeout-seconds 7200
    [[ "$(col "$id" idle_screen_timeout_seconds)" == 7200 ]] || fail "the override was not recorded"
    baseline "$id"
    # An hour unchanged: the config's 60 s would qualify; the row's 2 h does not.
    backdate "$id" 3600000
    early="$(read_row status "$id")"
    jq -e --argjson k "$status_plain" '.state == "working" and keys == $k' <<<"$early" >/dev/null ||
        fail "an hour unchanged under a 2 h override reads [$early]"
    backdate "$id" 7200000
    observe "$id"
    status="$last"
    check_idle status "$status" "cd-$id" 180
    get="$(read_row get "$id")"
    check_idle get "$get" "cd-$id" 180
    [[ "$(col "$id" idle_screen_timeout_seconds)" == 7200 ]] || fail "reads changed the override"
    one_record "$id" working idle_screen "$(jq -r .since <<<"$status")"
    trail_clean
    jq -cn --argjson r "$refused" --arg e "$(jq -r .state <<<"$early")" --argjson m "$(jq .unchanged_minutes <<<"$get")" \
        '{case: "override", refused: $r, override: 7200, config: 60, at_1h: $e, at_3h_minutes: $m, records: 1}'
    ;;
cursor)
    write_config 60
    old=1577836800000 # 2020-01-01, long before any timeout
    # Keep the fixture's agent identity alive across active reads.
    writer_start="$(python3 -c 'import pathlib,sys; print(pathlib.Path("/proc/"+sys.argv[1]+"/stat").read_text().rsplit(")",1)[1].split()[19])' "$$")"
    sql "INSERT INTO spawns(instance_id,state,settled_state,state_changed_ms,cwd,agent,relay_mode,tmux_session_name,launch_token,permission_epoch,idle_screen_timeout_seconds,pane_sha256,pane_since_ms,pane_epoch,pid,proc_starttime)
         VALUES('cursor-quiet','working','working',0,'/tmp','cursor','on','ts-cursor-quiet','2222222222222222',7,1,'$(printf 'ab%.0s' {1..32})',$old,7,$$,'$writer_start')"
    before="$(col cursor-quiet "observation_version || '|' || IFNULL(display_observation,'') || '|' || pane_since_ms")"
    quiet="$(read_row status cursor-quiet)"
    jq -e --argjson k "$status_plain" '.state == "working" and .idle_screen_availability == "unavailable" and keys == $k' <<<"$quiet" >/dev/null ||
        fail "Cursor quiet status: $quiet"
    row="$(read_row get cursor-quiet)"
    jq -e '.state == "working" and .idle_screen_availability == "unavailable" and (has("since") | not)
        and (has("unchanged_minutes") | not)' <<<"$row" >/dev/null || fail "Cursor quiet get: $row"
    no_old_member "$row" "Cursor quiet get"
    [[ "$(col cursor-quiet "observation_version || '|' || IFNULL(display_observation,'') || '|' || pane_since_ms")" == "$before" ]] ||
        fail "a Cursor row was observed by its pane"
    sql "INSERT INTO spawns(instance_id,state,settled_state,state_changed_ms,cwd,agent,relay_mode,tmux_session_name,launch_token,permission_epoch,pid,proc_starttime)
         VALUES('cursor-asks','check_permission','check_permission',0,'/tmp','cursor','on','ts-cursor-asks','1111111111111111',7,$$,'$writer_start')"
    sql "INSERT INTO permission_requests(instance_id,request_token,tool_name,tool_input,created_at,settled_at,owner_launch_token,owner_permission_epoch,admitted_at,fallback_at,fallback_basis)
         VALUES('cursor-asks','aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa','Bash','{}','2019-12-31 23:59:00',$old,'1111111111111111',7,$((old - 1000)),$old,'deadline')"
    # b.zpv: fallen back since 2020, the request keeps its row check_permission.
    last="$(read_row status cursor-asks)"
    jq -e --argjson k "$status_plain" '.state == "check_permission" and .idle_screen_availability == "unavailable" and keys == $k' <<<"$last" >/dev/null ||
        fail "Cursor permission: $last"
    sql "INSERT INTO spawns(instance_id,state,settled_state,state_changed_ms,cwd,agent,relay_mode,tmux_session_name,launch_token)
         VALUES('codex-ended','ended','ended',0,'/tmp','codex','off','ts-codex-ended','3333333333333333')"
    listed="$(agent-director list)" || fail "list exited non-zero"
    jq -e '[.spawns[] | [.instance_id, .idle_screen_availability]] | sort ==
        [["codex-ended", "available"], ["cursor-asks", "unavailable"], ["cursor-quiet", "unavailable"]]' <<<"$listed" >/dev/null ||
        fail "availability on every row: $listed"
    [[ "$(records cursor-asks)" == "[]" ]] || fail "the fallen-back Cursor request wrote a record: $(records cursor-asks)"
    [[ "$(records cursor-quiet)" == "[]" ]] || fail "the never-sampled Cursor row wrote a record: $(records cursor-quiet)"
    for reading in "$quiet" "$last"; do
        jq -e '.confirmations.displayed_state == {status:"unknown",confirmed_at:null,source:null,checked_at:null,reason:"unsupported"}' <<<"$reading" >/dev/null || fail "Cursor display evidence: $reading"
    done
    [[ "$(col cursor-quiet 'display_evidence IS NULL')" == 1 ]] || fail "Cursor stored display evidence"
    trail_clean
    jq -cn --arg q "$(jq -r '[.state, .idle_screen_availability] | join("/")' <<<"$quiet")" \
        --arg a "$(jq -r '[.state, .idle_screen_availability] | join("/")' <<<"$last")" \
        '{case: "cursor", quiet_since_2020: $q, asks: $a, list: 3, records: {asks: 0, quiet: 0}}'
    ;;
*) fail "unknown mode $mode" ;;
esac
