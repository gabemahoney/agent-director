-- AD v0.11.1's state.db, schema v5: the DDL is internal/store/schema.go's
-- schemaDDL at the v0.11.1 tag, verbatim, followed by a few rows that are in
-- no live state. Load it with: sqlite3 state.db < v0.11.1-store.sql
PRAGMA journal_mode=WAL;

CREATE TABLE IF NOT EXISTS spawns (
    claude_instance_id         TEXT PRIMARY KEY,
    parent_id                  TEXT REFERENCES spawns(claude_instance_id) ON DELETE SET NULL,
    state                      TEXT NOT NULL,
    cwd                        TEXT NOT NULL,
    tmux_session_name          TEXT NOT NULL,
    claude_args                TEXT NOT NULL DEFAULT '[]',
    relay_mode                 TEXT NOT NULL,
    jsonl_path                 TEXT,
    claude_session_id          TEXT,
    labels                     TEXT NOT NULL DEFAULT '{}',
    started_at                 TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_seen_at               TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    ended_at                   TIMESTAMP,
    pid                        INTEGER,
    proc_starttime             TEXT,
    liveness_unverified_since  TEXT,
    liveness_note              TEXT,
    extra_env                  TEXT NOT NULL DEFAULT '{}',
    row_version                INTEGER NOT NULL DEFAULT 0,
    launch_started_at          INTEGER,
    life_number                INTEGER NOT NULL DEFAULT 0,
    no_pre_trust               INTEGER NOT NULL DEFAULT 0,
    launch_token               TEXT,
    tmux_socket                TEXT,
    tmux_server_pid            INTEGER,
    tmux_server_started        INTEGER,
    tmux_server_starttime      TEXT,
    pane_id                    TEXT,
    pane_pid                   INTEGER,
    pane_starttime             TEXT
);
CREATE INDEX IF NOT EXISTS idx_spawns_state     ON spawns(state);
CREATE INDEX IF NOT EXISTS idx_spawns_last_seen ON spawns(last_seen_at);
CREATE INDEX IF NOT EXISTS idx_spawns_parent    ON spawns(parent_id);

CREATE TABLE IF NOT EXISTS permission_requests (
    request_id          INTEGER PRIMARY KEY AUTOINCREMENT,
    claude_instance_id  TEXT NOT NULL
                        REFERENCES spawns(claude_instance_id) ON DELETE CASCADE,
    request_token       TEXT NOT NULL,
    tool_name           TEXT NOT NULL,
    tool_input          TEXT NOT NULL,
    decision            TEXT,
    decision_reason     TEXT,
    decided_at          TIMESTAMP,
    created_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(claude_instance_id, request_token)
);
CREATE INDEX IF NOT EXISTS idx_permission_requests_instance_decision   ON permission_requests(claude_instance_id, decision);
CREATE INDEX IF NOT EXISTS idx_permission_requests_decision_decided_at ON permission_requests(decision, decided_at);

CREATE TABLE IF NOT EXISTS session_history (
    history_id          INTEGER PRIMARY KEY AUTOINCREMENT,
    claude_instance_id  TEXT NOT NULL
                        REFERENCES spawns(claude_instance_id) ON DELETE CASCADE,
    claude_session_id   TEXT NOT NULL,
    jsonl_path          TEXT,
    recorded_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    life_number         INTEGER NOT NULL DEFAULT 0,
    UNIQUE(claude_instance_id, claude_session_id)
);
CREATE INDEX IF NOT EXISTS idx_session_history_instance ON session_history(claude_instance_id);

CREATE TABLE IF NOT EXISTS store_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);

INSERT INTO store_meta (key, value) VALUES ('store_id', '0123456789abcdef');

INSERT INTO spawns (claude_instance_id, state, cwd, tmux_session_name, relay_mode, claude_session_id, jsonl_path, ended_at)
VALUES
    ('ended-1', 'ended', '/work/one', 'ad-ended-1', 'off', 'sess-ended-1', '/work/one/.claude/ended-1.jsonl', '2024-03-01 10:00:00'),
    ('ended-2', 'ended', '/work/two', 'ad-ended-2', 'off', 'sess-ended-2', NULL, '2024-03-02 10:00:00'),
    ('missing-1', 'missing', '/work/three', 'ad-missing-1', 'off', NULL, NULL, NULL);

INSERT INTO session_history (claude_instance_id, claude_session_id, jsonl_path)
VALUES ('ended-1', 'sess-ended-0', '/work/one/.claude/ended-0.jsonl');

INSERT INTO permission_requests (claude_instance_id, request_token, tool_name, tool_input, decision, decided_at)
VALUES ('ended-1', 'tok-1', 'Bash', '{"command":"ls"}', 'allow', '2024-03-01 09:00:00');

PRAGMA user_version = 5;
