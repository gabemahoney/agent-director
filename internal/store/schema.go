package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// schemaDDL is the canonical schema v9 DDL (v-current: fresh DBs are stamped
// directly at schemaVersion and never run a migration step). IF NOT EXISTS is
// defensive — ensureSchema only runs this inside a fresh-DB branch, but
// belt-and-suspenders avoids races on a re-open against a torn-down test.
//
// v2 changes vs v1: request_token column, composite UNIQUE(claude_instance_id,
// request_token), decided_at replaces updated_at, and two new indexes.
//
// v3 changes vs v2: five new spawns columns — pid, proc_starttime,
// liveness_unverified_since, liveness_note (all nullable) and extra_env
// (TEXT NOT NULL DEFAULT '{}'). See migrateV2toV3.
//
// v4 changes vs v3 (b.v2c): a new session_history table records every
// (claude_session_id, jsonl_path) pair a spawn has ever pointed at, so a
// session rotation when a caller restarts its agents no longer orphans the
// previous session's transcript — the prior pair is archived before the
// spawns row is overwritten with the new session id. session_history is the queryable link
// from a live row back to its earlier sessions (AC6/AC8). See migrateV3toV4.
//
// v5 changes vs v4 (b.fmk, SR-5.1/SR-5.4): twelve new spawns columns, appended
// after the v4 columns in this order — row_version (INTEGER NOT NULL DEFAULT
// 0), launch_started_at (INTEGER), life_number (INTEGER NOT NULL DEFAULT 0),
// no_pre_trust (INTEGER NOT NULL DEFAULT 0), and the launch identity
// launch_token (TEXT), tmux_socket (TEXT), tmux_server_pid (INTEGER),
// tmux_server_started (INTEGER), tmux_server_starttime (TEXT), pane_id (TEXT),
// pane_pid (INTEGER), pane_starttime (TEXT) — and one new session_history
// column, life_number (INTEGER NOT NULL DEFAULT 0), appended last. The column
// order and constraint text match migrateV4toV5 exactly, so a fresh store and
// a migrated store have identical column lists on both tables.
//
// v5 also adds one new table (SR-5.1, WD 2026-09-29 STORE): store_meta (key
// TEXT PRIMARY KEY, value TEXT NOT NULL), whose one Phase 1 row is store_id —
// 64 random bits as 16 lowercase hex, inserted once when the store is created
// (createSchema) or by the hop (migrateV4toV5), only when absent, and never
// changed (SR-5.4). Its CREATE text is identical here and in migrateV4toV5
// (the two-places rule), so both give the same PRAGMA table_info(store_meta).
//
// v6 changes vs v5 (b.kdf, b.146 rule 11): three new spawns columns, appended
// after pane_starttime in this order — the launch owner launch_owner_pid
// (INTEGER), launch_owner_starttime (TEXT) and launch_owner_pidns (TEXT), all
// nullable: the process that began the row's current launch (LaunchOwner).
// The column order and constraint text match migrateV5toV6 exactly, so a
// fresh store and a migrated store have identical column lists.
//
// v7 changes vs v6 (b.146 steps 2, 2b and 2c, one release, one migration):
// the columns v7Columns lists, in its order — on permission_requests,
// appended after created_at, the relay hook's identity, its delivery and
// settle instants, the reader and decide facts, the pane-answer columns, the
// close marker closed_at and the proof proven_gone_at, proven_gone_how; on
// spawns, idle_since after launch_owner_pidns. The column text matches
// v7Columns exactly (the two-places rule), so a fresh store and a migrated
// store have identical column lists.
//
// v8 changes vs v7 (b.3ie, the settle time): the columns v8Columns lists, in
// its order, on spawns after idle_since — settled_state (TEXT NOT NULL,
// default the empty string), the last settled state as of the row's last raw
// state change,
// and state_changed_ms (INTEGER NOT NULL DEFAULT 0), when the raw state last
// changed, in milliseconds since the epoch. state stays the raw state. Every
// write of state sets both in its own statement (settle.go), and status, get
// and list derive the state they report from the three (SettleRead). The
// column text matches v8Columns exactly (the two-places rule).
//
// v9 changes vs v8 (b.ssz, agent-generic columns): the six Claude-named
// columns are renamed, each keeping its type, position, constraints, keys,
// indexes and foreign keys (v9Renames: spawns.claude_instance_id to
// instance_id, claude_args to agent_args, claude_session_id to session_id;
// permission_requests.claude_instance_id and session_history.claude_instance_id
// to instance_id; session_history.claude_session_id to session_id), and spawns
// gains agent (TEXT NOT NULL, default the empty string, which is no agent: every
// write of a row names claude or codex) and then end_reported_at (nullable
// INTEGER, milliseconds since the epoch), appended in that order after
// state_changed_ms. The column text matches v9Columns exactly (the two-places
// rule).
//
// v9 also carries, folded into the same unreleased hop (b.xjn, SR-9, SR-10.1,
// SR-10.5; there is no separate v10): spawns.subagent_tracking (INTEGER NOT
// NULL DEFAULT 0), appended after end_reported_at, and two tables at the end
// of this text, subagents (one row per subagent entry) and subagent_launches
// (one row per claimable launch), both with a foreign key to spawns(instance_id)
// ON DELETE CASCADE. The same CREATE text, column for column, is v9SubagentTablesSQL,
// migrateV8toV9's own fixed copy (the two-places rule), so a fresh store and a
// migrated store have the same objects. Only internal/store reads or writes
// them.
//
// subagents holds one row per entry, a record (confirmed) or an observation
// (unconfirmed): created_seq grows with each insert and is never reused, so it
// orders creation; launch_token is the launch the entry belongs to; times are
// integer milliseconds since the epoch; opened_at is the latest time the entry
// became running (or its creation time) and activity_at the instant of the
// latest write that changed it; the three *_source columns say which source set
// a value that has more than one, NULL while the value is unknown. The
// UNIQUE(instance_id, subagent_id) constraint's implicit index serves the
// per-row read, the per-launch transitions and the cap, which all lead with
// instance_id.
//
// subagent_launches holds one row per open claimable launch: an internal note
// of an Agent tool call not yet matched to a subagent. UNIQUE(instance_id,
// tool_use_id) serves the closes; idx_subagent_launches_claim serves the claim
// (the current launch and type, oldest first).
//
// Both cascade from spawns, so expire's guarded delete and the admin delete
// remove a row's entries and claimable launches with the row.
const schemaDDL = `
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
    subagent_tracking          INTEGER NOT NULL DEFAULT 0
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
    UNIQUE(instance_id, subagent_id)
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
	{from: 2, apply: migrateV2toV3},
	{from: 3, apply: migrateV3toV4},
	{from: 4, apply: migrateV4toV5},
	{from: 5, apply: migrateV5toV6},
	{from: 6, apply: migrateV6toV7},
	{from: 7, apply: migrateV7toV8},
	{from: 8, apply: migrateV8toV9},
}

// ensureSchema enforces the schema-version contract on an opened *sql.DB.
//
//	user_version == 0             -> fresh DB; create tables/indexes and the
//	                                 store id in one tx, stamp user_version =
//	                                 schemaVersion.
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

// createSchema runs the v-current (schemaVersion) DDL, inserts the store's
// store_id when none exists (SR-5.4), and stamps user_version, in a single tx.
// It may run on a DB whose tables already exist (user_version stamped back to
// 0); the IF NOT EXISTS DDL and the guarded insert then keep the existing
// tables and id.
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
	if err := insertStoreIDOnce(tx); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("store: create schema: %w", err)
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

// migrateV2toV3 upgrades a v2 database to v3 inside a single transaction. It
// adds five new spawns columns — pid, proc_starttime, liveness_unverified_since,
// liveness_note (all nullable) and extra_env (TEXT NOT NULL DEFAULT '{}') — and
// stamps user_version = 3. ALTER TABLE ADD COLUMN backfills existing rows with
// NULL for the nullable columns and '{}' for extra_env (SR-5). A rollback on
// any error leaves user_version=2 intact.
func migrateV2toV3(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: begin v2→v3 migration tx: %w", err)
	}
	// SQLite has no ADD COLUMN IF NOT EXISTS (migration-guide §2). Guard each
	// ALTER by probing pragma_table_info('spawns') first and only adding the
	// column when it is absent, so the hop is idempotent on re-entry.
	v3SpawnsColumns := []struct{ name, ddl string }{
		{"pid", "ALTER TABLE spawns ADD COLUMN pid INTEGER"},
		{"proc_starttime", "ALTER TABLE spawns ADD COLUMN proc_starttime TEXT"},
		{"liveness_unverified_since", "ALTER TABLE spawns ADD COLUMN liveness_unverified_since TEXT"},
		{"liveness_note", "ALTER TABLE spawns ADD COLUMN liveness_note TEXT"},
		{"extra_env", "ALTER TABLE spawns ADD COLUMN extra_env TEXT NOT NULL DEFAULT '{}'"},
	}
	for _, col := range v3SpawnsColumns {
		var exists int
		err := tx.QueryRow(
			"SELECT 1 FROM pragma_table_info('spawns') WHERE name = ?",
			col.name,
		).Scan(&exists)
		switch {
		case err == nil:
			// Column already present — skip to stay idempotent.
			continue
		case errors.Is(err, sql.ErrNoRows):
			// Column absent — add it below.
		default:
			_ = tx.Rollback()
			return fmt.Errorf("store: v2→v3 probe spawns.%s: %w", col.name, err)
		}
		if _, err := tx.Exec(col.ddl); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("store: v2→v3 add spawns.%s: %w", col.name, err)
		}
	}
	if _, err := tx.Exec("PRAGMA user_version = 3"); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("store: v2→v3 stamp user_version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit v2→v3 migration tx: %w", err)
	}
	return nil
}

// migrateV3toV4 upgrades a v3 database to v4 inside a single transaction
// (b.v2c). It creates the session_history table and its index and stamps
// user_version = 4. There is no phase-3 backfill: pre-v4 rows have no recorded
// session history, and the current session pair is archived lazily on the next
// rotation. The CREATE statements use IF NOT EXISTS so the hop is idempotent on
// re-entry. A rollback on any error leaves user_version=3 intact.
func migrateV3toV4(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: begin v3→v4 migration tx: %w", err)
	}
	const v4SessionHistory = `
CREATE TABLE IF NOT EXISTS session_history (
    history_id          INTEGER PRIMARY KEY AUTOINCREMENT,
    claude_instance_id  TEXT NOT NULL
                        REFERENCES spawns(claude_instance_id) ON DELETE CASCADE,
    claude_session_id   TEXT NOT NULL,
    jsonl_path          TEXT,
    recorded_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(claude_instance_id, claude_session_id)
);
CREATE INDEX IF NOT EXISTS idx_session_history_instance ON session_history(claude_instance_id);
`
	if _, err := tx.Exec(v4SessionHistory); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("store: v3→v4 create session_history: %w", err)
	}
	if _, err := tx.Exec("PRAGMA user_version = 4"); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("store: v3→v4 stamp user_version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit v3→v4 migration tx: %w", err)
	}
	return nil
}

// migrateV4toV5 upgrades a v4 database to v5 inside a single transaction
// (b.fmk, SR-5.1/SR-5.4). It adds the twelve spawns columns first, then
// session_history.life_number, in SR-5.1's final-list order — the same order
// and constraint text schemaDDL uses, so fresh and migrated stores converge —
// and stamps user_version = 5 as the last statement.
//
// There is no phase 3 and no backfill (PO 2026-09-26 UPG): ADD COLUMN gives
// every existing row and history entry the column's ordinary default — row
// version 0, life 0, no_pre_trust 0 (pre-trust allowed), and NULL for the
// launch start, launch token, socket and server/pane identity. No existing
// value is rewritten.
//
// SQLite has no ADD COLUMN IF NOT EXISTS (migration-guide §2), so each ALTER
// is guarded by a pragma_table_info probe of its own table and skipped when
// the column is already present, making the hop idempotent on re-entry.
//
// After the thirteen columns the hop creates store_meta (CREATE TABLE IF NOT
// EXISTS, with schemaDDL's exact text: the two-places rule) and inserts one
// new random store_id only when no store_id row exists (SR-5.1, SR-5.4; WD
// 2026-09-29 STORE), so a second run, or a run on a store that already holds
// an id, keeps that id. Any probe, ALTER, CREATE or insert failure rolls the
// whole hop back, leaving user_version=4, neither table with any of the new
// columns, and no store_id.
func migrateV4toV5(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: begin v4→v5 migration tx: %w", err)
	}
	v5Columns := []struct{ table, name, ddl string }{
		{"spawns", "row_version", "ALTER TABLE spawns ADD COLUMN row_version INTEGER NOT NULL DEFAULT 0"},
		{"spawns", "launch_started_at", "ALTER TABLE spawns ADD COLUMN launch_started_at INTEGER"},
		{"spawns", "life_number", "ALTER TABLE spawns ADD COLUMN life_number INTEGER NOT NULL DEFAULT 0"},
		{"spawns", "no_pre_trust", "ALTER TABLE spawns ADD COLUMN no_pre_trust INTEGER NOT NULL DEFAULT 0"},
		{"spawns", "launch_token", "ALTER TABLE spawns ADD COLUMN launch_token TEXT"},
		{"spawns", "tmux_socket", "ALTER TABLE spawns ADD COLUMN tmux_socket TEXT"},
		{"spawns", "tmux_server_pid", "ALTER TABLE spawns ADD COLUMN tmux_server_pid INTEGER"},
		{"spawns", "tmux_server_started", "ALTER TABLE spawns ADD COLUMN tmux_server_started INTEGER"},
		{"spawns", "tmux_server_starttime", "ALTER TABLE spawns ADD COLUMN tmux_server_starttime TEXT"},
		{"spawns", "pane_id", "ALTER TABLE spawns ADD COLUMN pane_id TEXT"},
		{"spawns", "pane_pid", "ALTER TABLE spawns ADD COLUMN pane_pid INTEGER"},
		{"spawns", "pane_starttime", "ALTER TABLE spawns ADD COLUMN pane_starttime TEXT"},
		{"session_history", "life_number", "ALTER TABLE session_history ADD COLUMN life_number INTEGER NOT NULL DEFAULT 0"},
	}
	for _, col := range v5Columns {
		// The table name comes from the trusted literal list above, never
		// from input; pragma_table_info takes it as a bound argument.
		var exists int
		err := tx.QueryRow(
			"SELECT 1 FROM pragma_table_info(?) WHERE name = ?",
			col.table, col.name,
		).Scan(&exists)
		switch {
		case err == nil:
			// Column already present — skip to stay idempotent.
			continue
		case errors.Is(err, sql.ErrNoRows):
			// Column absent — add it below.
		default:
			_ = tx.Rollback()
			return fmt.Errorf("store: v4→v5 probe %s.%s: %w", col.table, col.name, err)
		}
		if _, err := tx.Exec(col.ddl); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("store: v4→v5 add %s.%s: %w", col.table, col.name, err)
		}
	}
	const v5StoreMeta = `CREATE TABLE IF NOT EXISTS store_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);`
	if _, err := tx.Exec(v5StoreMeta); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("store: v4→v5 create store_meta: %w", err)
	}
	if err := insertStoreIDOnce(tx); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("store: v4→v5 %w", err)
	}
	if _, err := tx.Exec("PRAGMA user_version = 5"); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("store: v4→v5 stamp user_version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit v4→v5 migration tx: %w", err)
	}
	return nil
}

// migrateV5toV6 upgrades a v5 database to v6 inside a single transaction
// (b.kdf, b.146 rule 11). It adds the three launch-owner spawns columns,
// launch_owner_pid (INTEGER), launch_owner_starttime (TEXT) and
// launch_owner_pidns (TEXT), in that order — the same order and constraint
// text schemaDDL uses, so fresh and migrated stores converge — and stamps
// user_version = 6 as the last statement.
//
// There is no phase 3 and no backfill: ADD COLUMN gives every existing row
// NULL in the three columns, so every row, a pending one included, records no
// launch owner: find-missing judges it by its pending grace period alone. No
// existing value is rewritten, and store_meta and its store id are kept.
//
// SQLite has no ADD COLUMN IF NOT EXISTS (migration-guide §2), so each ALTER
// is guarded by a pragma_table_info probe and skipped when the column is
// already present, making the hop idempotent on re-entry. Any probe or ALTER
// failure rolls the whole hop back, leaving user_version=5 and none of the
// new columns.
func migrateV5toV6(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: begin v5→v6 migration tx: %w", err)
	}
	v6Columns := []struct{ name, ddl string }{
		{"launch_owner_pid", "ALTER TABLE spawns ADD COLUMN launch_owner_pid INTEGER"},
		{"launch_owner_starttime", "ALTER TABLE spawns ADD COLUMN launch_owner_starttime TEXT"},
		{"launch_owner_pidns", "ALTER TABLE spawns ADD COLUMN launch_owner_pidns TEXT"},
	}
	for _, col := range v6Columns {
		var exists int
		err := tx.QueryRow(
			"SELECT 1 FROM pragma_table_info('spawns') WHERE name = ?",
			col.name,
		).Scan(&exists)
		switch {
		case err == nil:
			// Column already present — skip to stay idempotent.
			continue
		case errors.Is(err, sql.ErrNoRows):
			// Column absent — add it below.
		default:
			_ = tx.Rollback()
			return fmt.Errorf("store: v5→v6 probe spawns.%s: %w", col.name, err)
		}
		if _, err := tx.Exec(col.ddl); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("store: v5→v6 add spawns.%s: %w", col.name, err)
		}
	}
	if _, err := tx.Exec("PRAGMA user_version = 6"); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("store: v5→v6 stamp user_version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit v5→v6 migration tx: %w", err)
	}
	return nil
}

// v7Columns is every column migrateV6toV7 adds, in order, with the exact
// column text schemaDDL uses (the two-places rule). v7 is the one migration
// of b.146 steps 2, 2b and 2c, which ship in one release: a later step adds
// its columns by appending entries here and the same text to schemaDDL.
//
// On permission_requests (b.146 rules 2, 3, 5, 14, 15, 16):
//   - hook_pid, hook_starttime, hook_pidns: the relay hook that recorded the
//     request, read at its start (its pid, /proc/self/stat field 22 and the
//     target of /proc/self/ns/pid), so a reader can tell whether it is gone;
//   - tool_use_id, agent_id: from the hook input;
//   - delivered_at: the hook's ack, committed before it writes its answer
//     (milliseconds since the epoch, as the other three instants below);
//   - settled_at: the hook's kill instant plus the reserve, the time a reader
//     falls back to when it cannot check the hook process; NULL on a request
//     recorded before v7, which falls back by its created_at and the relay
//     window as before;
//   - hook_gone_at: when a reader first found the hook gone;
//   - attempted_decision, attempted_at: a refused decide's verdict, shown and
//     never acted on;
//   - pane_answer ('none' until a pane answer is recorded), pane_as, the
//     pane answer's sender identity (pane_sender_pid, pane_sender_starttime,
//     pane_sender_pidns) and pane_intent_at, when its intent was written
//     (milliseconds since the epoch; NULL once the sender released it),
//     written by step 2b's pane answers (send-keys --request-token,
//     record-pane-answer, and the PostToolUse close);
//   - closed_at: when a close of its Spawn's requests closed the request
//     (b.146 rule 12): find-missing's mark of the Spawn missing, the terminal
//     SessionEnd's move to ended, or resume's move of a finished Spawn to
//     pending as a backstop; a request that still awaited an answer then,
//     decided or not, one recorded before v7 included (milliseconds since the
//     epoch). A closed request no longer awaits an answer. NULL on every
//     request no close closed;
//   - proven_gone_at, proven_gone_how (b.146 step 2c): when a hook of Claude
//     Code, or its Spawn's end, proved the request's permission dialog gone
//     (milliseconds since the epoch), and how: tool_ran (the PostToolUse or
//     PostToolUseFailure with its tool_use_id), turn_end (the turn of the
//     agent that asked ended after it: the main agent's Stop or idle-prompt
//     Notification, for a request with no agent_id) or agent_gone (a close
//     of its Spawn's requests, or the hop on a finished row). NULL until
//     then; while NULL, plain send-keys to the Spawn is refused
//     (ErrDialogMaybeOpen) unless it carries a matching pane hash. A request
//     recorded before v7 has NULL in both until its next proof, except on a
//     row already ended or missing, whose requests the hop proves agent_gone
//     (migrateV6toV7's backfill).
//
// On spawns (b.146 problem 3): idle_since, the time the main agent's
// idle-prompt Notification last landed, NULLed by any later hook.
var v7Columns = []struct{ table, name, ddl string }{
	{"permission_requests", "hook_pid", "ALTER TABLE permission_requests ADD COLUMN hook_pid INTEGER"},
	{"permission_requests", "hook_starttime", "ALTER TABLE permission_requests ADD COLUMN hook_starttime TEXT"},
	{"permission_requests", "hook_pidns", "ALTER TABLE permission_requests ADD COLUMN hook_pidns TEXT"},
	{"permission_requests", "tool_use_id", "ALTER TABLE permission_requests ADD COLUMN tool_use_id TEXT"},
	{"permission_requests", "agent_id", "ALTER TABLE permission_requests ADD COLUMN agent_id TEXT"},
	{"permission_requests", "delivered_at", "ALTER TABLE permission_requests ADD COLUMN delivered_at INTEGER"},
	{"permission_requests", "settled_at", "ALTER TABLE permission_requests ADD COLUMN settled_at INTEGER"},
	{"permission_requests", "hook_gone_at", "ALTER TABLE permission_requests ADD COLUMN hook_gone_at INTEGER"},
	{"permission_requests", "attempted_decision", "ALTER TABLE permission_requests ADD COLUMN attempted_decision TEXT"},
	{"permission_requests", "attempted_at", "ALTER TABLE permission_requests ADD COLUMN attempted_at INTEGER"},
	{"permission_requests", "pane_answer", "ALTER TABLE permission_requests ADD COLUMN pane_answer TEXT NOT NULL DEFAULT 'none'"},
	{"permission_requests", "pane_as", "ALTER TABLE permission_requests ADD COLUMN pane_as TEXT"},
	{"permission_requests", "pane_sender_pid", "ALTER TABLE permission_requests ADD COLUMN pane_sender_pid INTEGER"},
	{"permission_requests", "pane_sender_starttime", "ALTER TABLE permission_requests ADD COLUMN pane_sender_starttime TEXT"},
	{"permission_requests", "pane_sender_pidns", "ALTER TABLE permission_requests ADD COLUMN pane_sender_pidns TEXT"},
	{"permission_requests", "pane_intent_at", "ALTER TABLE permission_requests ADD COLUMN pane_intent_at INTEGER"},
	{"permission_requests", "closed_at", "ALTER TABLE permission_requests ADD COLUMN closed_at INTEGER"},
	{"permission_requests", "proven_gone_at", "ALTER TABLE permission_requests ADD COLUMN proven_gone_at INTEGER"},
	{"permission_requests", "proven_gone_how", "ALTER TABLE permission_requests ADD COLUMN proven_gone_how TEXT"},
	{"spawns", "idle_since", "ALTER TABLE spawns ADD COLUMN idle_since TEXT"},
}

// v7ProveFinishedSQL is migrateV6toV7's one backfill (phase 3): every request
// of a finished row (ended or missing) not yet proven gone is proven gone,
// agent_gone, at the migration's instant. Its placeholders are that instant,
// ProvenGoneAgentGone, then finishedStateGuardArgs. It writes only where
// proven_gone_at is NULL, so a second run rewrites nothing.
const v7ProveFinishedSQL = `UPDATE permission_requests
    SET proven_gone_at = ?, proven_gone_how = ?
  WHERE proven_gone_at IS NULL
    AND claude_instance_id IN (SELECT claude_instance_id FROM spawns WHERE ` + finishedStateGuardSQL + `)`

// migrateV6toV7 upgrades a v6 database to v7 inside a single transaction
// (b.146 steps 2, 2b and 2c): it adds v7Columns, in order, proves the
// requests of finished rows gone (v7ProveFinishedSQL) and stamps
// user_version = 7 as the last statement.
//
// ADD COLUMN gives every existing request NULL in every new column but
// pane_answer, which takes its default 'none' (no pane answer recorded), and
// every row NULL idle_since. A request recorded before the upgrade therefore
// has no hook identity and no settled_at: readers fall back by its created_at
// and the relay window, as before the upgrade.
//
// Phase 3 backfills one fact: the proof of a finished row's requests (b.146
// step 2c). A row already ended or missing has no agent left, so no dialog of
// it is on a pane: each of its requests is proven gone, proven_gone_how
// agent_gone and proven_gone_at the migration's instant, as the close of a
// Spawn's requests proves them when the row ends from v7 on. So a dead
// agent's requests neither read unproven (get-permission's unproven_since)
// nor pin the cap eviction, which keeps an unproven request of a live row. A
// request of a live row keeps proven_gone_at NULL: it holds plain send-keys
// and pause to its Spawn until the next proof (its agent's next Stop or
// idle-prompt Notification, as it has no agent_id, or the Spawn's end). No
// other existing value is rewritten (an undecided request of a finished row stays undecided
// until resume's move closes it), and store_meta and its store id are kept.
//
// SQLite has no ADD COLUMN IF NOT EXISTS (migration-guide §2), so each ALTER
// is guarded by a pragma_table_info probe of its own table and skipped when
// the column is already present, and the backfill writes only unproven
// requests, making the hop idempotent on re-entry. Any probe, ALTER or
// backfill failure rolls the whole hop back, leaving user_version=6, none of
// the new columns and no request proven.
func migrateV6toV7(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: begin v6→v7 migration tx: %w", err)
	}
	for _, col := range v7Columns {
		// The table name comes from the trusted literal list above, never
		// from input; pragma_table_info takes it as a bound argument.
		var exists int
		err := tx.QueryRow(
			"SELECT 1 FROM pragma_table_info(?) WHERE name = ?",
			col.table, col.name,
		).Scan(&exists)
		switch {
		case err == nil:
			// Column already present — skip to stay idempotent.
			continue
		case errors.Is(err, sql.ErrNoRows):
			// Column absent — add it below.
		default:
			_ = tx.Rollback()
			return fmt.Errorf("store: v6→v7 probe %s.%s: %w", col.table, col.name, err)
		}
		if _, err := tx.Exec(col.ddl); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("store: v6→v7 add %s.%s: %w", col.table, col.name, err)
		}
	}
	proveArgs := append([]any{millisArg(time.Now()), ProvenGoneAgentGone}, finishedStateGuardArgs()...)
	if _, err := tx.Exec(v7ProveFinishedSQL, proveArgs...); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("store: v6→v7 prove finished rows' requests gone: %w", err)
	}
	if _, err := tx.Exec("PRAGMA user_version = 7"); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("store: v6→v7 stamp user_version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit v6→v7 migration tx: %w", err)
	}
	return nil
}

// v8Columns is every column migrateV7toV8 adds (b.3ie, the settle time), in
// order, with the exact column text schemaDDL uses (the two-places rule), both
// on spawns after idle_since:
//   - settled_state: the last settled state as of the row's last raw state
//     change (the base a hook write keeps while its change has not settled);
//     the empty string only on a row no write has given one, which every
//     reader takes as its raw state;
//   - state_changed_ms: when the row's raw state last changed, in
//     milliseconds since the epoch, from SQLite's own clock inside the
//     writing statement; 0 is older than any settle time.
var v8Columns = []struct{ table, name, ddl string }{
	{"spawns", "settled_state", "ALTER TABLE spawns ADD COLUMN settled_state TEXT NOT NULL DEFAULT ''"},
	{"spawns", "state_changed_ms", "ALTER TABLE spawns ADD COLUMN state_changed_ms INTEGER NOT NULL DEFAULT 0"},
}

// v8BackfillSQL is migrateV7toV8's one backfill (phase 3): every row gets its
// own state as its settled state. With state_changed_ms at its default 0 a row
// then reads settled at once for every settle time, as it did before the hop.
// It writes only where settled_state is still the empty string, so a second
// run rewrites nothing.
const v8BackfillSQL = `UPDATE spawns SET settled_state = state WHERE settled_state = ''`

// migrateV7toV8 upgrades a v7 database to v8 inside a single transaction
// (b.3ie): it adds v8Columns, in order, backfills every row's settled state
// (v8BackfillSQL) and stamps user_version = 8 as the last statement.
//
// ADD COLUMN gives every existing row an empty settled_state and
// state_changed_ms 0; the backfill then sets settled_state to the row's
// state. So every row
// that existed at the migration reads as settled: status, get and list report
// its raw state at once whatever [state] settle_ms is, until its next raw
// change. No other existing value is rewritten (row_version included), and
// store_meta and its store id are kept.
//
// SQLite has no ADD COLUMN IF NOT EXISTS (migration-guide §2), so each ALTER
// is guarded by a pragma_table_info probe and skipped when the column is
// already present, and the backfill writes only rows not yet backfilled,
// making the hop idempotent on re-entry. Any probe, ALTER or backfill failure
// rolls the whole hop back, leaving user_version=7 and neither column.
func migrateV7toV8(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: begin v7→v8 migration tx: %w", err)
	}
	for _, col := range v8Columns {
		// The table name comes from the trusted literal list above, never
		// from input; pragma_table_info takes it as a bound argument.
		var exists int
		err := tx.QueryRow(
			"SELECT 1 FROM pragma_table_info(?) WHERE name = ?",
			col.table, col.name,
		).Scan(&exists)
		switch {
		case err == nil:
			// Column already present — skip to stay idempotent.
			continue
		case errors.Is(err, sql.ErrNoRows):
			// Column absent — add it below.
		default:
			_ = tx.Rollback()
			return fmt.Errorf("store: v7→v8 probe %s.%s: %w", col.table, col.name, err)
		}
		if _, err := tx.Exec(col.ddl); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("store: v7→v8 add %s.%s: %w", col.table, col.name, err)
		}
	}
	if _, err := tx.Exec(v8BackfillSQL); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("store: v7→v8 backfill settled state: %w", err)
	}
	if _, err := tx.Exec("PRAGMA user_version = 8"); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("store: v7→v8 stamp user_version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit v7→v8 migration tx: %w", err)
	}
	return nil
}

// v9Renames is every column migrateV8toV9 renames (b.ssz, SR-3.1), in order:
// the table and the old and new column names. A rename keeps the column's
// type, position, constraints, primary key, unique constraints, indexes and the
// foreign keys that refer to it: SQLite's ALTER TABLE ... RENAME COLUMN rewrites
// them all, including the REFERENCES clauses of the other tables. spawns comes
// first because the other two tables refer to its instance id.
var v9Renames = []struct{ table, oldName, newName string }{
	{"spawns", "claude_instance_id", "instance_id"},
	{"spawns", "claude_args", "agent_args"},
	{"spawns", "claude_session_id", "session_id"},
	{"permission_requests", "claude_instance_id", "instance_id"},
	{"session_history", "claude_instance_id", "instance_id"},
	{"session_history", "claude_session_id", "session_id"},
}

// v9Columns is every column migrateV8toV9 adds (b.ssz), in order, with the
// exact column text schemaDDL uses (the two-places rule), both on spawns after
// state_changed_ms:
//   - agent: the row's agent, claude or codex. NOT NULL with the empty string
//     as the column default, which an added not-null column needs and which is
//     no agent: the hop backfills every existing row to claude
//     (v9BackfillSQL), and every write that inserts or resets a row names its
//     agent, so nothing relies on the default;
//   - end_reported_at: when a Codex row's verified agent process sent a
//     SessionEnd, in milliseconds since the epoch (NULL until the Codex end
//     report, ApplyHookEndReport, sets it; the SessionStart identity write
//     clears it. Always NULL on a Claude row);
//   - subagent_tracking (b.xjn, SR-9): 1 when the settings the row's current
//     launch was given register the agent's subagent start and stop events.
//     NOT NULL DEFAULT 0 and no backfill: a row from before the hop reads 0
//     (untracked) until its next launch write sets 1; only InsertPending,
//     MoveToPending and ResetForReuse write it.
var v9Columns = []struct{ table, name, ddl string }{
	{"spawns", "agent", "ALTER TABLE spawns ADD COLUMN agent TEXT NOT NULL DEFAULT ''"},
	{"spawns", "end_reported_at", "ALTER TABLE spawns ADD COLUMN end_reported_at INTEGER"},
	{"spawns", "subagent_tracking", "ALTER TABLE spawns ADD COLUMN subagent_tracking INTEGER NOT NULL DEFAULT 0"},
}

// v9BackfillSQL is migrateV8toV9's one backfill (phase 3): every row that
// existed before the hop is a Claude row. It writes only where agent is still
// the empty string, so a second run rewrites nothing and a row that already
// holds an agent keeps it.
const v9BackfillSQL = `UPDATE spawns SET agent = 'claude' WHERE agent = ''`

// v9SubagentTablesSQL is migrateV8toV9's own, fixed copy of the CREATE text of
// the two subagent tables and the claim index (b.xjn, SR-10.1, SR-10.5), run in
// phase 2 after the renames. Like v9Columns it carries the exact column text
// schemaDDL uses (the two-places rule) and, as a shipped hop's text, never
// changes once released: a later change to either table is a later hop's, not
// an edit here. IF NOT EXISTS keeps a second run a no-op. Both tables cascade
// from spawns(instance_id). schemaDDL's own copy documents what each table and
// column holds.
const v9SubagentTablesSQL = `
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
    UNIQUE(instance_id, subagent_id)
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
`

// v9HasColumn reports whether table has a column called name, by a
// pragma_table_info probe inside tx. The table name is bound as an argument and
// comes from the trusted literal lists above, never from input.
func v9HasColumn(tx *sql.Tx, table, name string) (bool, error) {
	var exists int
	err := tx.QueryRow(
		"SELECT 1 FROM pragma_table_info(?) WHERE name = ?",
		table, name,
	).Scan(&exists)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	default:
		return false, err
	}
}

// migrateV8toV9 upgrades a v8 database to v9 inside a single transaction
// (b.ssz, SR-3.3), in the migration guide's four phases:
//
//  1. Validate, read-only: for each of v9Renames, a table that holds both the
//     old and the new name of the column, or neither, fails the hop naming the
//     table and the columns; nothing has been changed yet.
//  2. Rename and add: ALTER TABLE ... RENAME COLUMN for each rename whose old
//     name is still there (one whose new name is present and old name absent
//     was done by an earlier run and is skipped), then ADD COLUMN for each of
//     v9Columns not already there, in order (the last is subagent_tracking),
//     then CREATE the subagents and subagent_launches tables and their indexes
//     (v9SubagentTablesSQL, IF NOT EXISTS, after the renames so their foreign
//     keys name spawns(instance_id)).
//  3. Backfill: every row whose agent is still the empty string gets claude
//     (v9BackfillSQL); a row that already holds an agent is not touched.
//  4. Stamp user_version = 9, the last statement.
//
// No existing value is rewritten but agent, and store_meta and its store id are
// kept. The subagent tables start empty and every existing row reads
// subagent_tracking 0: no backfill (b.xjn, SR-10.2 as folded into this hop).
// Every other table, index and constraint keeps its shape. Any probe, rename,
// ADD COLUMN, CREATE or backfill failure rolls the whole hop back, leaving
// user_version=8, every column under its old name, no new column and neither
// subagent table.
func migrateV8toV9(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: begin v8→v9 migration tx: %w", err)
	}
	// Phase 1: validate.
	type renameState struct{ hasOld, hasNew bool }
	states := make([]renameState, len(v9Renames))
	for i, r := range v9Renames {
		hasOld, err := v9HasColumn(tx, r.table, r.oldName)
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("store: v8→v9 probe %s.%s: %w", r.table, r.oldName, err)
		}
		hasNew, err := v9HasColumn(tx, r.table, r.newName)
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("store: v8→v9 probe %s.%s: %w", r.table, r.newName, err)
		}
		switch {
		case hasOld && hasNew:
			_ = tx.Rollback()
			return fmt.Errorf("store: v8→v9 table %s holds both %s and %s: refusing to rename",
				r.table, r.oldName, r.newName)
		case !hasOld && !hasNew:
			_ = tx.Rollback()
			return fmt.Errorf("store: v8→v9 table %s holds neither %s nor %s",
				r.table, r.oldName, r.newName)
		}
		states[i] = renameState{hasOld: hasOld, hasNew: hasNew}
	}
	// Phase 2: rename, then add.
	for i, r := range v9Renames {
		if !states[i].hasOld {
			// Already renamed by an earlier run: skip to stay idempotent.
			continue
		}
		// The table and column names come from the trusted literal list above,
		// never from input.
		ddl := fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s", r.table, r.oldName, r.newName)
		if _, err := tx.Exec(ddl); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("store: v8→v9 rename %s.%s: %w", r.table, r.oldName, err)
		}
	}
	for _, col := range v9Columns {
		exists, err := v9HasColumn(tx, col.table, col.name)
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("store: v8→v9 probe %s.%s: %w", col.table, col.name, err)
		}
		if exists {
			// Column already present: skip to stay idempotent.
			continue
		}
		if _, err := tx.Exec(col.ddl); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("store: v8→v9 add %s.%s: %w", col.table, col.name, err)
		}
	}
	if _, err := tx.Exec(v9SubagentTablesSQL); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("store: v8→v9 create subagent tables: %w", err)
	}
	// Phase 3: backfill.
	if _, err := tx.Exec(v9BackfillSQL); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("store: v8→v9 backfill agent: %w", err)
	}
	// Phase 4: stamp.
	if _, err := tx.Exec("PRAGMA user_version = 9"); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("store: v8→v9 stamp user_version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit v8→v9 migration tx: %w", err)
	}
	return nil
}
