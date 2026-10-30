-- Genuine pre-cutover fresh-schema DDL from f069ce5230be5715e385771d59b81bc164e853c4.
-- Copied verbatim from schemaDDL and cursorRuntimeDDL; deterministic fixture store identity.
PRAGMA user_version=15;
CREATE TABLE IF NOT EXISTS cursor_runtimes (
 bound_instance_id TEXT PRIMARY KEY,
 bound_launch_token TEXT NOT NULL,
 bound_life_number INTEGER NOT NULL,
 bound_gateway_pid INTEGER NOT NULL,
 bound_gateway_start TEXT NOT NULL,
 bound_runtime_pid INTEGER NOT NULL,
 bound_runtime_start TEXT NOT NULL,
 bound_connection_id TEXT NOT NULL,
 bound_input_pipe TEXT NOT NULL,
 bound_output_pipe TEXT NOT NULL,
 bound_input_fd INTEGER NOT NULL,
 bound_output_fd INTEGER NOT NULL,
 bound_session_id TEXT NOT NULL DEFAULT '',
 bound_terminated INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS spawns (
    instance_id                TEXT PRIMARY KEY,
    parent_id                  TEXT REFERENCES spawns(instance_id) ON DELETE SET NULL,
    state                      TEXT NOT NULL,
    cwd                        TEXT NOT NULL,
    tmux_session_name          TEXT NOT NULL,
    agent_args                 TEXT NOT NULL DEFAULT '[]',
    relay_mode                 TEXT NOT NULL,
    jsonl_path                 TEXT,
    session_id                 TEXT,
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
    pane_starttime             TEXT,
    launch_owner_pid           INTEGER,
    launch_owner_starttime     TEXT,
    launch_owner_pidns         TEXT,
    idle_since                 TEXT,
    settled_state              TEXT NOT NULL DEFAULT '',
    state_changed_ms           INTEGER NOT NULL DEFAULT 0,
    agent                      TEXT NOT NULL DEFAULT '',
    end_reported_at            INTEGER,
    subagent_tracking          INTEGER NOT NULL DEFAULT 0,
    family_state               TEXT NOT NULL DEFAULT '',
    family_settled_state       TEXT NOT NULL DEFAULT '',
    family_changed_ms          INTEGER NOT NULL DEFAULT 0,
    codex_startup              TEXT,
    claude_capacity            TEXT,
    permission_epoch           INTEGER NOT NULL DEFAULT 0,
    observation_version        INTEGER NOT NULL DEFAULT 0,
    display_observation        TEXT,
    display_exit               TEXT,
    idle_screen_timeout_seconds INTEGER,
    pane_sha256                TEXT,
    pane_since_ms              INTEGER,
    pane_epoch                 INTEGER,
    codex_turn                 TEXT,
    codex_turn_ended           TEXT
);
CREATE INDEX IF NOT EXISTS idx_spawns_state     ON spawns(state);
CREATE INDEX IF NOT EXISTS idx_spawns_last_seen ON spawns(last_seen_at);
CREATE INDEX IF NOT EXISTS idx_spawns_parent    ON spawns(parent_id);

CREATE TABLE IF NOT EXISTS permission_requests (
    request_id          INTEGER PRIMARY KEY AUTOINCREMENT,
    instance_id         TEXT NOT NULL
                        REFERENCES spawns(instance_id) ON DELETE CASCADE,
    request_token       TEXT NOT NULL,
    tool_name           TEXT NOT NULL,
    tool_input          TEXT NOT NULL,
    decision            TEXT,
    decision_reason     TEXT,
    decided_at          TIMESTAMP,
    created_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    hook_pid            INTEGER,
    hook_starttime      TEXT,
    hook_pidns          TEXT,
    tool_use_id         TEXT,
    agent_id            TEXT,
    delivered_at        INTEGER,
    settled_at          INTEGER,
    hook_gone_at        INTEGER,
    attempted_decision  TEXT,
    attempted_at        INTEGER,
    pane_answer         TEXT NOT NULL DEFAULT 'none',
    pane_as             TEXT,
    pane_sender_pid       INTEGER,
    pane_sender_starttime TEXT,
    pane_sender_pidns     TEXT,
    pane_intent_at      INTEGER,
    closed_at           INTEGER,
    proven_gone_at      INTEGER,
    proven_gone_how     TEXT,
    main_turn_end_eligible INTEGER NOT NULL DEFAULT 0,
    owner_launch_token TEXT,
    owner_permission_epoch INTEGER,
    admitted_at INTEGER,
    fallback_at INTEGER,
    fallback_basis TEXT,
    UNIQUE(instance_id, request_token)
);
CREATE INDEX IF NOT EXISTS idx_permission_requests_instance_decision   ON permission_requests(instance_id, decision);
CREATE INDEX IF NOT EXISTS idx_permission_requests_decision_decided_at ON permission_requests(decision, decided_at);

CREATE TABLE IF NOT EXISTS session_history (
    history_id          INTEGER PRIMARY KEY AUTOINCREMENT,
    instance_id         TEXT NOT NULL
                        REFERENCES spawns(instance_id) ON DELETE CASCADE,
    session_id          TEXT NOT NULL,
    jsonl_path          TEXT,
    recorded_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    life_number         INTEGER NOT NULL DEFAULT 0,
    UNIQUE(instance_id, session_id)
);
CREATE INDEX IF NOT EXISTS idx_session_history_instance ON session_history(instance_id);

CREATE TABLE IF NOT EXISTS store_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);

CREATE TABLE IF NOT EXISTS subagents (
    created_seq          INTEGER PRIMARY KEY AUTOINCREMENT,
    instance_id          TEXT NOT NULL
                         REFERENCES spawns(instance_id) ON DELETE CASCADE,
    subagent_id          TEXT NOT NULL,
    launch_token         TEXT NOT NULL,
    confirmed            INTEGER NOT NULL,
    session_id           TEXT,
    subagent_type        TEXT,
    description          TEXT,
    model                TEXT,
    parent_type          TEXT,
    parent_id            TEXT,
    tool_use_id          TEXT,
    run_mode             TEXT,
    first_started_at     INTEGER,
    started_at           INTEGER,
    stopped_at           INTEGER,
    lost_at              INTEGER,
    state                TEXT NOT NULL,
    close_reason         TEXT,
    opened_at            INTEGER NOT NULL,
    activity_at          INTEGER NOT NULL,
    subagent_type_source TEXT,
    run_mode_source      TEXT,
    stopped_at_source    TEXT,
    life_number          INTEGER NOT NULL,
    UNIQUE(instance_id, life_number, subagent_id)
);

CREATE TABLE IF NOT EXISTS subagent_launches (
    created_seq   INTEGER PRIMARY KEY AUTOINCREMENT,
    instance_id   TEXT NOT NULL
                  REFERENCES spawns(instance_id) ON DELETE CASCADE,
    launch_token  TEXT NOT NULL,
    tool_use_id   TEXT NOT NULL,
    subagent_type TEXT NOT NULL,
    opened_at     INTEGER NOT NULL,
    UNIQUE(instance_id, tool_use_id)
);
CREATE INDEX IF NOT EXISTS idx_subagent_launches_claim ON subagent_launches(instance_id, launch_token, subagent_type, opened_at);

INSERT INTO store_meta(key,value) VALUES ('store_id','0123456789abcdef');
