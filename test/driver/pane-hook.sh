#!/usr/bin/env bash
# pane-hook.sh — synthesise an agent-director hook from inside a row's pane
# (SRD SR-20.9, SR-22.9). Run it from the driver's shell.
#
# A hook moves a row only when the `agent-director hook` process is a direct
# child of the row's recorded pane process. So a case never pipes a hook
# from its own shell: it spawns with the stand-in claude
# (`PATH=/opt/driver/stand-in-claude:$PATH agent-director spawn ...`, which
# makes an interactive bash the pane process; see
# stand-in-claude/claude for why the PATH prefix and not --extra-env) and
# calls this helper, which types
#
#   [KEY=VALUE ...] agent-director hook < <dir>/payload.json > <dir>/out 2> <dir>/err; echo $? > <dir>/rc
#
# into the pane with `tmux send-keys -l --` and then Enter. That line is two
# simple commands, so the pane's bash forks `agent-director hook` directly
# (no sh -c, no subshell, no script) and the hook's parent is the pane
# process.
#
# Usage:
#   pane-hook.sh [options] <instance-id> <payload-json | ->
#       Type the hook, wait (bounded) for its exit status, print its stdout,
#       copy its stderr to stderr, exit with its exit status.
#   pane-hook.sh [options] --no-wait <instance-id> <payload-json | ->
#       Type the hook and return at once, printing the job directory (for a
#       hook that blocks, e.g. a relayed PermissionRequest waiting on decide).
#   pane-hook.sh [--timeout <secs>] --wait <job-dir>
#       Wait for a --no-wait job; print, copy and exit as the first form.
#
# Options:
#   --pane <target>    type into this pane (e.g. a pane id %7 from
#                      split-window -P -F '#{pane_id}') instead of the row's
#                      recorded pane_id; the tmux socket is still the row's.
#   --env KEY=VALUE    prefix the typed command with KEY=VALUE (repeatable),
#                      e.g. --env AGENT_DIRECTOR_RELAY_MODE=on. VALUE is
#                      limited to [A-Za-z0-9_./:@%+,=-] because it is typed
#                      into a shell.
#   --timeout <secs>   bound on the wait (default 20).
#
# The pane and socket come from one read-only sqlite3 SELECT of the row's
# pane_id and tmux_socket in $PANE_HOOK_DB (default
# $HOME/.agent-director/state.db). A payload of "-" is read from stdin.
#
# Exit status: the hook's own, or 2 for bad usage, 3 when the row records no
# pane, 124 when no exit status arrives in time (the pane's last lines are
# printed on stderr).

set -euo pipefail

die() {
    local rc="$1"
    shift
    printf 'pane-hook: %s\n' "$*" >&2
    exit "$rc"
}

timeout_s=20
mode=wait
pane=""
job=""
envs=()

while [[ $# -gt 0 ]]; do
    case "$1" in
        --pane) [[ $# -ge 2 ]] || die 2 "--pane needs a target"; pane="$2"; shift 2 ;;
        --env)
            [[ $# -ge 2 ]] || die 2 "--env needs KEY=VALUE"
            [[ "$2" =~ ^[A-Za-z_][A-Za-z0-9_]*=[A-Za-z0-9_./:@%+,=-]*$ ]] \
                || die 2 "--env $2: want KEY=VALUE with VALUE in [A-Za-z0-9_./:@%+,=-]"
            envs+=("$2"); shift 2 ;;
        --timeout)
            [[ $# -ge 2 && "$2" =~ ^[0-9]+$ ]] || die 2 "--timeout needs whole seconds"
            timeout_s="$2"; shift 2 ;;
        --no-wait) mode=nowait; shift ;;
        --wait) [[ $# -ge 2 ]] || die 2 "--wait needs a job directory"; mode=join; job="$2"; shift 2 ;;
        --) shift; break ;;
        -*) die 2 "unknown option $1" ;;
        *) break ;;
    esac
done

# finish waits for job's rc file, then prints the hook's stdout, copies its
# stderr and exits with its status.
finish() {
    local dir="$1" rc="" i
    for ((i = 0; i < timeout_s * 10; i++)); do
        if [[ -s "$dir/rc" ]]; then
            rc="$(<"$dir/rc")"
            [[ "$rc" =~ ^[0-9]+$ ]] && break
            rc=""
        fi
        sleep 0.1
    done
    if [[ -z "$rc" ]]; then
        local shown=""
        if [[ -r "$dir/target" ]]; then
            local sock tgt
            { read -r sock; read -r tgt; } <"$dir/target"
            shown="$(tmux ${sock:+-S "$sock"} capture-pane -p -t "$tgt" 2>&1 | grep -v '^$' | tail -n 5 | tr '\n' '|' || true)"
        fi
        die 124 "no exit status from the hook in ${dir} after ${timeout_s}s; pane shows: ${shown}"
    fi
    cat "$dir/out"
    if [[ -s "$dir/err" ]]; then
        cat "$dir/err" >&2
    fi
    exit "$rc"
}

if [[ "$mode" == join ]]; then
    [[ $# -eq 0 ]] || die 2 "--wait takes only the job directory"
    [[ -d "$job" ]] || die 2 "--wait $job: no such job directory"
    finish "$job"
fi

[[ $# -eq 2 ]] || die 2 "usage: pane-hook.sh [--pane <target>] [--env K=V]... [--timeout <secs>] [--no-wait] <instance-id> <payload-json | ->"
id="$1"
payload="$2"

db="${PANE_HOOK_DB:-$HOME/.agent-director/state.db}"
[[ -r "$db" ]] || die 3 "no store at $db"

# Read-only lookup of the row's pane and socket; the id's quotes are doubled
# for the SQL literal.
esc="${id//\'/\'\'}"
row="$(sqlite3 -readonly -separator '|' "$db" \
    "SELECT COALESCE(pane_id, ''), COALESCE(tmux_socket, '') FROM spawns WHERE claude_instance_id = '${esc}';")"
row_pane="${row%%|*}"
socket="${row#*|}"
[[ -n "$row" ]] || socket=""
if [[ -z "$pane" ]]; then
    [[ -n "$row_pane" ]] || die 3 "row ${id} records no pane (pane_id is empty or there is no row)"
    pane="$row_pane"
fi

job="$(mktemp -d /tmp/pane-hook.XXXXXX)"
if [[ "$payload" == "-" ]]; then
    cat >"$job/payload.json"
else
    printf '%s' "$payload" >"$job/payload.json"
fi
printf '%s\n%s\n' "$socket" "$pane" >"$job/target"

line=""
for kv in "${envs[@]}"; do
    line+="$kv "
done
line+="agent-director hook < $job/payload.json > $job/out 2> $job/err; echo \$? > $job/rc"

tmux ${socket:+-S "$socket"} send-keys -t "$pane" -l -- "$line"
tmux ${socket:+-S "$socket"} send-keys -t "$pane" Enter

if [[ "$mode" == nowait ]]; then
    printf '%s\n' "$job"
    exit 0
fi
finish "$job"
