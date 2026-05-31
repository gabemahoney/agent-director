package store

import (
	"database/sql"
	"fmt"
)

// schemaDDL is the canonical schema v2 DDL. IF NOT EXISTS is defensive —
// ensureSchema only runs this inside a fresh-DB branch, but belt-and-suspenders
// avoids races on a re-open against a torn-down test.
//
// v2 changes vs v1: request_token column, composite UNIQUE(claude_instance_id,
// request_token), decided_at replaces updated_at, and two new indexes.
const schemaDDL = `
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
`

// ensureSchema enforces the schema-version contract on an opened *sql.DB.
//
//	user_version == 0           -> fresh DB; create tables/indexes in one tx,
//	                               stamp user_version = schemaVersion.
//	user_version == 1           -> migrate v1→v2 in one tx.
//	user_version == schemaVersion -> nothing to do.
//	anything else                 -> ErrSchemaMismatch, no DDL executed.
//
// Splitting the fresh-DB write into a transaction means a crash mid-creation
// leaves user_version at 0, so the next Open will retry cleanly.
func ensureSchema(db *sql.DB) error {
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("store: read user_version: %w", err)
	}

	switch version {
	case schemaVersion:
		return nil
	case 0:
		return createSchema(db)
	case 1:
		return migrateV1toV2(db)
	default:
		return fmt.Errorf("%w: found user_version=%d, want %d",
			ErrSchemaMismatch, version, schemaVersion)
	}
}

// createSchema runs the v2 DDL and stamps user_version in a single tx.
// PRAGMA user_version cannot take a bound parameter, so the version is
// interpolated from a trusted package constant — never user input.
func createSchema(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: begin schema tx: %w", err)
	}
	if _, err := tx.Exec(schemaDDL); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("store: apply schema: %w", err)
	}
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("store: set user_version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit schema tx: %w", err)
	}
	return nil
}

// migrateV1toV2 upgrades a v1 database to v2 inside a single transaction.
// The permission_requests table is DROP+CREATE — v1 rows are not preserved
// (SR-2.6 single-version DB invariant / SR-2.3 no-backfill). A rollback on
// any error leaves user_version=1 intact.
func migrateV1toV2(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: begin v1→v2 migration tx: %w", err)
	}
	if _, err := tx.Exec(`DROP TABLE permission_requests`); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("store: v1→v2 drop permission_requests: %w", err)
	}
	const v2PermissionRequests = `
CREATE TABLE permission_requests (
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
CREATE INDEX idx_permission_requests_instance_decision   ON permission_requests(claude_instance_id, decision);
CREATE INDEX idx_permission_requests_decision_decided_at ON permission_requests(decision, decided_at);
`
	if _, err := tx.Exec(v2PermissionRequests); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("store: v1→v2 create permission_requests: %w", err)
	}
	if _, err := tx.Exec("PRAGMA user_version = 2"); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("store: v1→v2 stamp user_version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit v1→v2 migration tx: %w", err)
	}
	return nil
}
