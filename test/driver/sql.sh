#!/usr/bin/env bash
# sql.sh — the sqlite3 shell with a busy timeout, for every direct read or
# write of a store from a Docker case or driver helper (b.ai5).
#
# A bare `sqlite3` has a busy timeout of 0: the first SQLITE_BUSY fails the
# statement at once with "Error: in prepare, database is locked (5)" and
# exit status 5, and a case under `set -e` ends there. In WAL mode a reader
# is normally not blocked by a writer, but it does get SQLITE_BUSY while
# another connection briefly holds the database's exclusive locks: when the
# last connection of an exiting agent-director process checkpoints and
# removes the -wal/-shm files, or when the next opener rebuilds the WAL
# index. A case that reads state.db right after one agent-director process
# exits and another starts can land in that window (b.ai5: relay-3 failed
# 1 run in 6). agent-director's own connections wait up to 10 s
# (busy_timeout(10000) in internal/store/store.go); this helper makes the
# case's connection wait too.
#
# Usage: exactly as sqlite3; every argument and stdin pass through unchanged.
#
#   /opt/driver/sql.sh -readonly "$HOME/.agent-director/state.db" "SELECT ..."
#   /opt/driver/sql.sh -readonly -separator '|' "$db" "SELECT ..."
#   /opt/driver/sql.sh "$db" "INSERT INTO spawns ..."
#
# SQL_BUSY_TIMEOUT_MS sets the wait in whole milliseconds (default 5000).
# The `.timeout` command it runs prints nothing, so the output is sqlite3's.
#
# Exit status: sqlite3's own, or 2 when SQL_BUSY_TIMEOUT_MS is not whole
# milliseconds.

set -euo pipefail

ms="${SQL_BUSY_TIMEOUT_MS:-5000}"
if [[ ! "$ms" =~ ^[0-9]+$ ]]; then
    printf 'sql.sh: SQL_BUSY_TIMEOUT_MS=%s: want whole milliseconds\n' "$ms" >&2
    exit 2
fi

exec sqlite3 -cmd ".timeout ${ms}" "$@"
