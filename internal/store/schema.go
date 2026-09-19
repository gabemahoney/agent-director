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

// migrationStep upgrades a database from `from` to `from+1` inside a single
// transaction. Each step is individually transactional (per the migrateV1toV2
// pattern): a rollback on any error leaves user_version at `from` intact, so a
// crash mid-chain never strands the DB at an intermediate, un-stamped version.
type migrationStep struct {
	// from is the user_version this step upgrades away from (it produces
	// from+1).
	from int
	// apply runs the DDL and stamps user_version = from+1 in one tx.
	apply func(db *sql.DB) error
}

// migrationSteps is the ordered registry of single-version upgrades. Steps are
// keyed by their `from` version and applied in ascending order until the DB
// reaches schemaVersion. Adding a new schema version means bumping
// schemaVersion (store.go), evolving schemaDDL for fresh-create, and appending
// one migrationStep{from: N, apply: migrateVNtoVN+1} here — the chain engine
// then upgrades any authorized older DB to current in a single open.
var migrationSteps = []migrationStep{
	{from: 1, apply: migrateV1toV2},
}

// ensureSchema enforces the schema-version contract on an opened *sql.DB.
//
//	user_version == 0             -> fresh DB; create tables/indexes in one tx,
//	                                 stamp user_version = schemaVersion.
//	0 < user_version < schemaVersion (older-than-binary)
//	                              -> gated: migrate ONLY when an administrator
//	                                 authorization sentinel exact-matches;
//	                                 otherwise ErrSchemaMigrationRequired and
//	                                 no DB write. When authorized, apply the
//	                                 chained single-version steps in one open.
//	user_version == schemaVersion -> nothing to do.
//	user_version > schemaVersion  -> ErrSchemaMismatch, no DDL executed.
//
// Splitting the fresh-DB write into a transaction means a crash mid-creation
// leaves user_version at 0, so the next Open will retry cleanly. dbPath is the
// resolved path of the DB file; it is used only to locate the sibling
// authorization sentinel (see authorizeMigration) — the DB itself is never
// touched on a refused open, preserving byte-identity.
func ensureSchema(db *sql.DB, dbPath string) error {
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("store: read user_version: %w", err)
	}

	switch {
	case version == schemaVersion:
		return nil
	case version == 0:
		return createSchema(db)
	case version > schemaVersion:
		return fmt.Errorf("%w: found user_version=%d, want %d",
			ErrSchemaMismatch, version, schemaVersion)
	default:
		// Older-than-binary: never auto-migrate. Require an administrator
		// authorization sentinel that exact-matches this DB's actual version
		// and this binary's schemaVersion; a refused open executes zero DB
		// writes. authorizeMigration returns the refusal error verbatim on
		// any miss.
		if err := authorizeMigration(dbPath, version); err != nil {
			return err
		}
		if err := runMigrationChain(db, version); err != nil {
			return err
		}
		// Migration committed. Consume the authorization as part of the same
		// logical operation and record the audit line. consumeAuthorization
		// is fail-open: a delete failure emits a loud trail event but never
		// fails the (already-committed) open.
		consumeAuthorization(dbPath, version, schemaVersion)
		return nil
	}
}

// runMigrationChain applies single-version migration steps in ascending order
// until the DB reaches schemaVersion. It is only reached after authorization
// has been granted. Each step is individually transactional; if a step fails
// the chain aborts with the DB stamped at the last successfully-committed
// version, and the open fails.
func runMigrationChain(db *sql.DB, from int) error {
	version := from
	for version < schemaVersion {
		step, ok := migrationStepFrom(version)
		if !ok {
			// No registered step for this version but we are still below
			// schemaVersion — a gap in the registry. Fail loudly rather than
			// silently leave the DB behind.
			return fmt.Errorf("%w: no migration step from user_version=%d toward %d",
				ErrSchemaMismatch, version, schemaVersion)
		}
		if err := step.apply(db); err != nil {
			return err
		}
		version++
	}
	return nil
}

// migrationStepFrom returns the registered step that upgrades away from the
// given version, if one exists.
func migrationStepFrom(from int) (migrationStep, bool) {
	for _, s := range migrationSteps {
		if s.from == from {
			return s, true
		}
	}
	return migrationStep{}, false
}

// buildMigrationRefusal constructs the SR-1.4 dead-end ErrSchemaMigrationRequired
// error. The message names no command, flag, file path, or environment
// variable — it routes the operator to their administrator and nowhere else.
func buildMigrationRefusal(current int) error {
	return fmt.Errorf("%w: state.db is schema v%d; this binary requires v%d. "+
		"Migration must be performed by an administrator via the agent-director install process.",
		ErrSchemaMigrationRequired, current, schemaVersion)
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
