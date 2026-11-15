#!/usr/bin/env bash
# busy-send-claude.sh — the shared steps of the busy-send-claude-wait-turn
# cases (tickets/testplans/b.75s/t1.75s.cw; Idea b.kii, Epic t1.xis.oo). A
# case's bash block sources it right after `set -euo pipefail`:
#
#   . /opt/driver/busy-send-claude.sh <name>
#
# It sets db; tag and id (<name>-<uuid>); sid and transcript, the main
# agent's session id and the transcript its hooks name ($work/$sid.jsonl);
# caller, the case's Caller UUID; work, a private directory; and an EXIT trap
# that ends the row's tmux session and removes work. It puts the stand-in
# Claude (stand-in-claude/claude) and the tmux call recorder
# (delay-create-tmux/tmux, with TMUX_CALL_LOG=$work/tmux-calls and
# DELAY_CREATE_SECONDS=0) first on PATH, so every launch runs the stand-in and
# every tmux call is logged with its caller's command name (no_product_input).
#
# The case plays Claude Code's part. It types each hook into the row's pane
# with pane-hook.sh --stderr-pipe, so the hook is the pane process's direct
# child with a pipe for its stderr, as Claude Code gives it, and runs the owned
# FileChanged hook itself where Claude Code would once the watched wake
# endpoint changed. That makes no claim about Claude Code's own intake, watcher
# or wrapping, which live qualification on the AD test VM covers.

unset ANTHROPIC_API_KEY CLAUDE_CODE_OAUTH_TOKEN

db="$HOME/.config/agent-director/state.db"
uuid="$(cat /proc/sys/kernel/random/uuid)"
tag="${uuid:0:8}"
id="$1-$uuid"
sid="$(cat /proc/sys/kernel/random/uuid)"
caller="$(cat /proc/sys/kernel/random/uuid)"
work="$(mktemp -d /tmp/bsc-XXXXXX)"
work="$(cd -P "$work" && pwd)"
transcript="$work/$sid.jsonl"
printf '{}\n' >"$transcript"
export TMUX_CALL_LOG="$work/tmux-calls" DELAY_CREATE_SECONDS=0
export PATH="/opt/driver/stand-in-claude:/opt/driver/delay-create-tmux:$PATH"
old=1000000000
wake=""
changed=""
rc=0
stop='{"stop_hook_active":false,"last_assistant_message":"done"}'
raced='["sent_unconfirmed",true,"wait_turn_raced"]'

fail() { echo "FAIL: $*"; exit 1; }

config_toml="$HOME/.config/agent-director/config.toml"
wrote_config=""

# write_config TOML: the case's ~/.config/agent-director/config.toml, which
# the EXIT trap removes.
write_config() {
    mkdir -p "$(dirname "$config_toml")"
    printf '%s\n' "$1" >"$config_toml"
    wrote_config=1
}

bsc_cleanup() {
    local row sock pane s
    [ -z "$wrote_config" ] || rm -f "$config_toml"
    row="$(/opt/driver/sql.sh -readonly "$db" \
        "SELECT IFNULL(tmux_socket,'') || '|' || IFNULL(pane_id,'') FROM spawns WHERE instance_id = '$id';" 2>/dev/null || true)"
    sock="${row%%|*}"
    pane="${row#*|}"
    if [ -n "$pane" ]; then
        s="$(tmux ${sock:+-S "$sock"} display-message -p -t "$pane" '#{session_id}' 2>/dev/null || true)"
        [ -z "$s" ] || tmux ${sock:+-S "$sock"} kill-session -t "$s" 2>/dev/null || true
    fi
    rm -rf "$work"
}
trap bsc_cleanup EXIT

# payload EVENT [EXTRA_JSON]: a Claude hook payload of the main agent's session.
payload() {
    local extra='{}'
    [ $# -lt 2 ] || extra="$2"
    jq -cn --arg e "$1" --arg sid "$sid" --arg tp "$transcript" --arg cwd "$work" --argjson x "$extra" \
        '{hook_event_name: $e, session_id: $sid, transcript_path: $tp, cwd: $cwd} + $x'
}

# hook EVENT [EXTRA_JSON]: type the hook into the row's pane (its parent is the
# pane process) with a pipe for its stderr; its stdout lands in $work/out, its
# stderr in $work/err and its exit status in rc.
hook() {
    local body pane_pid
    body="$(payload "$@")"
    set +e
    /opt/driver/pane-hook.sh --stderr-pipe "$id" "$body" >"$work/out" 2>"$work/err"
    rc=$?
    set -e
    if [ "$rc" -eq 124 ]; then
        # No exit status in time: show what still runs under the pane's shell.
        pane_pid="$(/opt/driver/sql.sh -readonly "$db" "SELECT IFNULL(pane_pid, '') FROM spawns WHERE instance_id = '$id';")"
        [ -z "$pane_pid" ] || ps -o pid=,stat=,etime=,args= --ppid "$pane_pid" >>"$work/err" 2>&1 || true
    fi
}

# quiet EVENT [EXTRA_JSON]: the typed hook exits 0 and writes nothing.
quiet() {
    hook "$@"
    [ "$rc" -eq 0 ] && [ ! -s "$work/out" ] && [ ! -s "$work/err" ] \
        || fail "$1 exited $rc, stdout [$(cat "$work/out")], stderr [$(cat "$work/err")]; want 0 and nothing"
}

# wakes WANT: the typed FileChanged for the wake endpoint exits 2 with nothing
# on stdout and exactly WANT on stderr, which it keeps in $work/prompt.
wakes() {
    hook FileChanged "$changed"
    [ "$rc" -eq 2 ] && [ ! -s "$work/out" ] || fail "FileChanged exited $rc, stdout [$(cat "$work/out")]; want 2 and nothing"
    printf '%s' "$1" | cmp -s - "$work/err" || fail "FileChanged wrote [$(cat -A "$work/err")]; want [$1]"
    cp "$work/err" "$work/prompt"
}

# sighting: type the UserPromptSubmit whose prompt is $work/prompt as Claude
# Code shows a FileChanged hook's exit-2 text: in its measured wrapping, with
# its two reserved closing tags escaped (claudeWakeWrapping in
# internal/store/busy_send_capabilities.go).
sighting() {
    local prefix suffix
    prefix=$'<task-notification>\n<summary>Stop hook feedback</summary>\n</task-notification>\n<system-reminder>\nStop hook blocking error from command "FileChanged": '
    suffix=$'\n</system-reminder>'
    quiet UserPromptSubmit "$(jq -cn --rawfile b "$work/prompt" --arg pre "$prefix" --arg suf "$suffix" \
        '{prompt: ($pre + ($b | gsub("</system-reminder>"; "&lt;/system-reminder>") | gsub("</task-notification>"; "<\\/task-notification>")) + $suf)}')"
}

# send NAME TEXT WAIT: a confirmed send of TEXT as Message NAME (the case's
# Caller UUID); it returns Held, waiting WAIT, with that pair.
send() {
    local out
    out="$(agent-director send-keys --instance-id "$id" --text "$2" --confirmed-delivery \
        --caller-uuid "$caller" --delivery-request-id "$1")" || fail "send-keys $1 exited non-zero"
    [ "$(jq -c '[.class, .final, .wait.reason, .request_id]' <<<"$out")" = "[\"held\",false,\"$3\",\"$1\"]" ] \
        || fail "send $1 returned [$out]; want Held $3"
}

# result NAME: Message NAME's class, finality and reason (get-delivery).
result() {
    agent-director get-delivery --caller-uuid "$caller" --delivery-request-id "$1" | jq -c '.result | [.class, .final, .reason]'
}
state() { agent-director get --instance-id "$id" | jq -r '.state'; }
delivery() { agent-director get --instance-id "$id" | jq -c "[.delivery | $1]"; }
rewind() { touch -m -d "@$old" "$wake"; }
touched() { [ "$(stat -c %Y "$wake")" != "$old" ]; }

# launched KIND SOURCE: the newest Launch spawn, reuse or resume recorded,
# from this binary's covered set (no coverage is seeded), is a covered Launch
# of KIND with route A at its own wake endpoint, which does not exist yet. Its
# SessionStart (source SOURCE) publishes it, returns that endpoint to watch and
# creates it, an empty file of mode 600 owned by the case's user, and the row
# then waits. It sets wake and changed.
launched() {
    local facts want
    facts="$(/opt/driver/sql.sh -readonly -separator '|' "$db" \
        "SELECT busy_send, kind, IFNULL(route_kind, ''), IFNULL(wake_path, '') FROM busy_send_launches
         WHERE instance_id = '$id' AND phase IN ('current', 'candidate') ORDER BY phase = 'candidate' DESC LIMIT 1;")"
    wake="${facts##*|}"
    [ "${facts%|*}" = "covered|$1|claude_hooks" ] && [ -n "$wake" ] && [ ! -e "$wake" ] \
        || fail "the newest Launch records [$facts]; want a covered $1 Launch with route claude_hooks at a wake endpoint not yet created"
    changed="$(jq -cn --arg p "$wake" '{file_path: $p, event: "change"}')"
    hook SessionStart "$(jq -cn --arg s "$2" '{source: $s, model: "claude-stand-in"}')"
    want="$(jq -cn --arg p "$wake" '{hookSpecificOutput: {hookEventName: "SessionStart", watchPaths: [$p]}}')"
    [ "$rc" -eq 0 ] && [ "$(cat "$work/out")" = "$want" ] && [ ! -s "$work/err" ] \
        || fail "SessionStart exited $rc, stdout [$(cat "$work/out")], stderr [$(cat "$work/err")]; want 0 and [$want]"
    [ "$(stat -c '%F %a %U' "$wake")" = "regular empty file 600 $(whoami)" ] || fail "wake endpoint is [$(stat -c '%F %a %U' "$wake")]"
    facts="$(/opt/driver/sql.sh -readonly "$db" \
        "SELECT s.busy_send || '|' || l.phase FROM spawns s JOIN busy_send_launches l ON l.instance_id = s.instance_id
         WHERE s.instance_id = '$id' AND l.wake_path = '$wake';")"
    [ "$facts" = "covered|current" ] || fail "after SessionStart the row|Launch read [$facts]; want covered|current"
    [ "$(state)" = waiting ] || fail "row is [$(state)] after SessionStart; want waiting"
}

# no_product_input TEXT...: agent-director typed nothing. No tmux call of its
# own (comm agent-director in the call log, which shows it made some) sent
# keys or pasted a buffer into a pane, its input journal holds no attempt, and
# no TEXT reached the pane it last read. Only pane-hook.sh's hook lines and the
# case's own direct tmux recovery typed anything.
no_product_input() {
    local calls typed pane text
    calls="$(awk -F'\t' '$1 == "agent-director" { print $2 }' "$TMUX_CALL_LOG")"
    grep -q 'new-session' <<<"$calls" || fail "the tmux call log holds no create by agent-director: [$calls]"
    typed="$(grep -E '(^| )(send-keys|send|paste-buffer|pasteb|load-buffer|loadb|set-buffer|setb) ' <<<"$calls" || true)"
    [ -z "$typed" ] || fail "agent-director sent input to a pane: $typed"
    [ "$(/opt/driver/sql.sh -readonly "$db" 'SELECT COUNT(*) FROM input_attempts;')" = 0 ] || fail "the store records an input attempt"
    [ $# -eq 0 ] && return
    pane="$(agent-director read-pane --instance-id "$id" --n-lines 200 | jq -r '.pane')"
    for text in "$@"; do
        [[ "$pane" != *"$text"* ]] || fail "[$text] reached the pane: $pane"
    done
}
