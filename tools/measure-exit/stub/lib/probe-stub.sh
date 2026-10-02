#!/bin/bash
# probe-stub.sh MODE [claude args...] — the body of the version probe's two
# dry-run stubs (stub/probe-args-kept/claude, stub/probe-args-dropped/claude;
# Epic 21, t3.h98.w4.yu.sm). DRY RUNS ONLY.
#
# It fires SessionStart once, for every command hook in agent-director's
# --settings layer and the working directory's .claude/settings.local.json:
#   MODE keep:  an entry with args runs as `command args...` (exec form; a
#               Claude Code that runs exec-form hooks, 2.1.285)
#   MODE drop:  every entry runs as `/bin/sh -c command`, its args dropped
#               (a Claude Code that ignores them, 2.1.120; agent-director's
#               own hook then runs with no verb and writes no_exec_form)
# Each hook is its own direct child. Then it idles until its pane closes.

set -u
export LC_ALL=C

mode="$1"
shift
settings=""
while (($#)); do
    case "$1" in
        --settings) settings="${2:-}"; shift 2 || shift ;;
        --settings=*) settings="${1#*=}"; shift ;;
        *) shift ;;
    esac
done

instance="${AGENT_DIRECTOR_INSTANCE_ID:-no-instance}"
[[ "$instance" =~ ^[A-Za-z0-9._-]+$ ]] || instance="no-instance"
state="$HOME/.mx-stub/$instance/$$"
mkdir -p "$state"
printf '%s' "$settings" >"$state/settings.json"
sid="$(cat /proc/sys/kernel/random/uuid)"
tp="$HOME/.claude/projects/mx-stub/$sid.jsonl"
mkdir -p "${tp%/*}"
: >>"$tp"
jq -cn --arg s "$sid" --arg t "$tp" --arg c "$PWD" \
    '{session_id: $s, transcript_path: $t, cwd: $c, hook_event_name: "SessionStart", source: "startup"}' \
    >"$state/payload.json"

i=0
while IFS= read -r entry; do
    i=$((i + 1))
    cmd="$(jq -r '.command' <<<"$entry")"
    if [[ "$mode" == keep ]] && jq -e 'has("args")' >/dev/null <<<"$entry"; then
        mapfile -t args < <(jq -r '.args[]' <<<"$entry")
        "$cmd" "${args[@]}" <"$state/payload.json" >"$state/h$i.out" 2>"$state/h$i.err"
    else
        /bin/sh -c "$cmd" <"$state/payload.json" >"$state/h$i.out" 2>"$state/h$i.err"
    fi
done < <(for f in "$state/settings.json" "$PWD/.claude/settings.local.json"; do
    [[ -s "$f" ]] && jq -c '.hooks.SessionStart[]?.hooks[]? | select(.type == "command")' "$f" 2>/dev/null
done)

trap 'exit 0' HUP TERM
while IFS= read -r _; do :; done
exit 0
