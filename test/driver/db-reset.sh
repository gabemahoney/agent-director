#!/usr/bin/env bash
# Per-t2 DB-reset fixture. Runs before each t2 case so every case starts
# from a clean state (SRD §15.2).
#
# Contract:
#   1. Remove ~/.agent-director/state.db and its WAL/SHM siblings if present.
#   2. Kill any tmux sessions whose names start with `cd-` (the harness's
#      reserved prefix for spawn sessions — see SRD §6).
#   3. Re-create the DB by calling `claude-director help`, which exercises
#      setupStore() in cmd/claude-director/main.go and rebuilds schema v1.
#
# Idempotent. Safe to call twice in a row. Exit 0 on success; non-zero only
# if claude-director help itself fails (which would mean the binary or
# config is broken).

set -euo pipefail

CD_DIR="${HOME}/.agent-director"
STATE_DB="${CD_DIR}/state.db"

# Drop the DB and its WAL/SHM sidecars.
rm -f "$STATE_DB" "${STATE_DB}-wal" "${STATE_DB}-shm"

# Kill any leftover tmux sessions from a previous case. The `cd-` prefix is
# the harness's reserved namespace; killing arbitrary sessions would risk
# clobbering whatever the host operator is doing.
if command -v tmux >/dev/null 2>&1; then
    if tmux ls 2>/dev/null | awk -F: '{print $1}' | grep -E '^cd-' >/tmp/.cd-sessions 2>/dev/null; then
        while IFS= read -r sess; do
            tmux kill-session -t "$sess" 2>/dev/null || true
        done </tmp/.cd-sessions
    fi
    rm -f /tmp/.cd-sessions
fi

# Rebuild the DB. `claude-director help` runs setupStore() which creates the
# dir at 0700 and the DB at 0600 with schema v1 stamped — Epic 1 AC #4.
# Stdout is silenced because the fixture's stderr is the only channel we
# expose to the driver.
if ! claude-director help >/dev/null; then
    echo "db-reset: claude-director help failed; binary or config is broken" >&2
    exit 1
fi
