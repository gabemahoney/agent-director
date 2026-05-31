-- schema_v1.sql — canonical v1 DDL fixture used by migration tests.
-- Load this into a fresh SQLite file to construct a pre-migration (user_version=1) database.
CREATE TABLE IF NOT EXISTS spawns (
    claude_instance_id   TEXT PRIMARY KEY,
    parent_id            TEXT REFERENCES spawns(claude_instance_id) ON DELETE SET NULL,
    state                TEXT NOT NULL,
    cwd                  TEXT NOT NULL,
    tmux_session_name    TEXT NOT NULL,
    claude_args          TEXT NOT NULL DEFAULT '[]',
    relay_mode           TEXT NOT NULL,
    jsonl_path           TEXT,
    claude_session_id    TEXT,
    labels               TEXT NOT NULL DEFAULT '{}',
    started_at           TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_seen_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    ended_at             TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_spawns_state     ON spawns(state);
CREATE INDEX IF NOT EXISTS idx_spawns_last_seen ON spawns(last_seen_at);
CREATE INDEX IF NOT EXISTS idx_spawns_parent    ON spawns(parent_id);

CREATE TABLE IF NOT EXISTS permission_requests (
    request_id          INTEGER PRIMARY KEY AUTOINCREMENT,
    claude_instance_id  TEXT NOT NULL UNIQUE
                        REFERENCES spawns(claude_instance_id) ON DELETE CASCADE,
    tool_name           TEXT NOT NULL,
    tool_input          TEXT NOT NULL,
    decision            TEXT,
    decision_reason     TEXT,
    created_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
PRAGMA user_version = 1;
