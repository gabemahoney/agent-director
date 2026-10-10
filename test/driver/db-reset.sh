#!/usr/bin/env bash
# Per-t2 DB-reset fixture. Runs before each t2 case so every case starts
# from a clean state (SRD §15.2).
#
# Contract:
#   0. Run only in the Docker test harness container, which test/Dockerfile
#      marks with AGENT_DIRECTOR_TEST_HARNESS=1. Anywhere else it changes
#      nothing and exits 2. See below.
#   1. End every tmux server an earlier case may have started, and with it
#      every session on it whatever its name, then wait for the processes of
#      their panes to exit (killing any still running after a 5 s grace).
#      See below.
#   2. Remove ~/.agent-director/state.db and its WAL/SHM siblings if present.
#   3. Re-create the DB by calling `agent-director list`, which exercises
#      setupStore() in cmd/agent-director/main.go and rebuilds the schema.
#      (`help`/`version`/no-args are DB-free verbs as of b.93m Part D and no
#      longer bootstrap the store — `list` is the established replacement.)
#
# Idempotent. Safe to call twice in a row. Exit 0 on success; 2 outside the
# harness container; 1 if agent-director list itself fails (which would mean
# the binary or config is broken).

set -euo pipefail

# 0. The harness container only (b.8yq). On a host this script would end every
# tmux server the user runs, orchestrator sessions included, and delete the
# real ~/.agent-director/state.db. AGENT_DIRECTOR_TEST_HARNESS=1 is set by the
# harness image (test/Dockerfile), so every run of it has it, run-testplan.sh
# and the cases it runs included; the sandbox container does not set it. Like
# AGENT_DIRECTOR_TEST_SANDBOX it is an accident-prevention gate, not a
# security boundary.
if [[ "${AGENT_DIRECTOR_TEST_HARNESS:-}" != 1 ]]; then
    echo "db-reset: refusing to run outside the Docker test harness container (AGENT_DIRECTOR_TEST_HARNESS=1 unset; test/Dockerfile sets it)." >&2
    echo "db-reset: it ends every tmux server of its user and deletes ~/.agent-director/state.db, so it runs only there. Nothing was changed. Run the harness with: make test-docker EPIC=<slug>" >&2
    exit 2
fi

CD_DIR="${HOME}/.agent-director"
STATE_DB="${CD_DIR}/state.db"

# 1. End the tmux servers (b.8yq). This used to kill only sessions named
# `cd-*`, but a spawn's default session name is <cwd base>-<id8> and cases
# name their own sessions freely, so an earlier case's processes (a real
# Claude Code among them) kept running into later cases. It runs before the
# store goes, so nothing a dying pane runs can reach the next case's store.
#
# Which servers: every socket in a tmux-<uid> directory up to four levels
# under /tmp, $HOME or this process's TMUX_TMPDIR. tmux, and agent-director
# with it, puts a server's socket at <TMUX_TMPDIR, else /tmp>/tmux-<uid>/<name>
# (the default server, and any -L name), and a case may point TMUX_TMPDIR at
# a directory of its own under /tmp or $HOME (ze.us uses
# $HOME/tmuxsock-tmpl5). Step 0 keeps this to the harness container, which is
# the harness's own, and run-testplan.sh runs outside tmux, so ending every
# server clobbers nothing.
# Should this script run inside a tmux session (TMUX set), that server's other
# sessions are ended instead, and this script's own session is spared.
#
# Without tmux there is no tmux server and so no session to end, so skipping
# is correct (b.ug8 audit).

# tmux_q runs one tmux call against a socket that may have no server behind
# it: its errors are expected and dropped, and a server that does not answer
# within 5 s is given up on rather than hanging the run.
tmux_q() {
    timeout 5 tmux "$@" 2>/dev/null || true
}

# list_sockets prints the candidate sockets, one per line, this script's own
# server's included.
list_sockets() {
    local roots=(/tmp "$HOME")
    if [[ -n "${TMUX_TMPDIR:-}" ]]; then
        roots+=("$TMUX_TMPDIR")
    fi
    find "${roots[@]}" -maxdepth 4 -type s -path "*/tmux-$(id -u)/*" 2>/dev/null || true
    if [[ -n "$own_socket" ]]; then
        printf '%s\n' "$own_socket"
    fi
}

pane_pids=()
if command -v tmux >/dev/null 2>&1; then
    own_socket=""
    own_session=""
    if [[ -n "${TMUX:-}" ]]; then
        own_socket="${TMUX%%,*}"
        own_session="$(tmux_q display-message -p '#{session_id}')"
    fi
    mapfile -t sockets < <(list_sockets | sort -u)
    for sock in "${sockets[@]}"; do
        if [[ "$sock" != "$own_socket" ]]; then
            mapfile -t -O "${#pane_pids[@]}" pane_pids < <(tmux_q -S "$sock" list-panes -a -F '#{pane_pid}')
            tmux_q -S "$sock" kill-server
            continue
        fi
        # This script's own server: end every session but its own. When its
        # own session cannot be told, end none.
        while IFS= read -r sess; do
            if [[ -z "$own_session" || "$sess" == "$own_session" ]]; then
                continue
            fi
            mapfile -t -O "${#pane_pids[@]}" pane_pids < <(tmux_q -S "$sock" list-panes -s -t "$sess" -F '#{pane_pid}')
            tmux_q -S "$sock" kill-session -t "$sess"
        done < <(tmux_q -S "$sock" list-sessions -F '#{session_id}')
    done
fi

# Ending a server hangs up its panes, and a pane's process may take a moment
# to exit (a real Claude Code rewrites ~/.claude.json as it goes, which broke
# spawn-9). tmux starts each pane's process as the leader of its own session,
# so the pane pid is that session's id and `pgrep -s` finds everything the
# pane started. Whatever is still running after a grace of at least 5 s is
# killed. SECONDS counts whole seconds, so SECONDS + 5 could come round just
# over 4 s from now; + 6 makes the grace 5 to 6 s.
deadline=$((SECONDS + 6))
for pid in "${pane_pids[@]}"; do
    if [[ ! "$pid" =~ ^[1-9][0-9]*$ ]]; then
        continue
    fi
    while pgrep -s "$pid" >/dev/null 2>&1; do
        if ((SECONDS >= deadline)); then
            echo "db-reset: processes of tmux pane ${pid} still running 5 s after its server ended; killing them" >&2
            pkill -KILL -s "$pid" 2>/dev/null || true
            break
        fi
        sleep 0.1
    done
done

# 2. Drop the DB and its WAL/SHM sidecars.
rm -f "$STATE_DB" "${STATE_DB}-wal" "${STATE_DB}-shm"

# 3. Rebuild the DB. `agent-director list` runs setupStore() which creates the
# dir at 0700 and the DB at 0600 with the current schema stamped — Epic 1
# AC #4. (`help` was DB-free'd in b.93m Part D, so it no longer creates the
# store; `list` is the store-opening replacement.) Stdout is silenced
# because the fixture's stderr is the only channel we expose to the driver.
if ! agent-director list >/dev/null; then
    echo "db-reset: agent-director list failed; binary or config is broken" >&2
    exit 1
fi
