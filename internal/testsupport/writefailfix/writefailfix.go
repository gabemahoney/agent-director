// Package writefailfix is the single source of the triggers that make one
// named kind of store write fail in a test (SR-20.3, SRD-RR2 T4).
//
// This is a LEAF test-support package, like procstarttimefix: it imports
// nothing from internal/store (or any other agent-director package), so both
// installers can use it without an import cycle:
//
//   - storefix.InjectWriteFailure, for tests outside internal/store;
//   - the white-box helper in internal/store's migration_fixtures_test.go,
//     which cannot import storefix (storefix imports store).
//
// Every trigger's SQL text lives in this file and nowhere else. A trigger is
// fixed text per kind; the instance id it applies to is never spliced into the
// SQL. Install records (kind, instance id) in a bookkeeping table with bound
// parameters, and each trigger fires only when its row's instance id is
// recorded there for its kind, so seeding, other rows and other ids are
// unaffected. A matched write fails with the driver error of RAISE(ABORT),
// which the store method returns as its error: never a silent no-op.
//
// Install on a temp store only, after seeding the row: several kinds also
// match the seeding writes (see each Kind).
package writefailfix

import (
	"database/sql"
	"fmt"
)

// Kind names one kind of write to make fail. The zero value is not a kind.
// Each value is fixed: the triggers below name it in their WHEN clause.
type Kind int

const (
	// ReuseArchive fails an insert or update of the id's session_history
	// entry (the reuse archive). Also matched: the SessionStart rotation
	// archive, which is fail-open, so RecordSessionStartIdentity still
	// succeeds but archives nothing; and any seeding of history for the id.
	ReuseArchive Kind = 1
	// ReuseReset fails an update that moves the id's finished row (ended or
	// missing) to pending (the reuse reset). Also matched: resume's move to
	// pending, and any hook transition of a finished row to pending.
	ReuseReset Kind = 2
	// ReusePermissionDelete fails a delete from permission_requests of one of
	// the id's requests (the reuse deletion). Also matched: the cap eviction
	// in UpsertOpenPermissionRequest when it evicts one of the id's closed
	// requests, and a delete of the id's spawns row (delete, expire) while it
	// still has requests, through the cascade.
	ReusePermissionDelete Kind = 3
	// ReuseRestore fails an update that moves the id's pending row to ended
	// or missing (the reuse restore). Also matched: resume's restore, the
	// ended transition or find-missing's mark of a pending row, a plain
	// spawn's end write after "duplicate session", and SeedSpawn of a
	// finished row (install after seeding).
	ReuseRestore Kind = 4
	// LaunchIdentityWrite fails the launch identity write
	// (store.RecordLaunchIdentity, SR-3.6): an update of the id's spawns row
	// whose SET names the server and pane identity columns and leaves state
	// unchanged. It fires whether or not the values differ, and never on the
	// insert, a hook write or any other kind's write. Also matched: SeedSpawn's
	// option and default update (install after seeding). Later adoption writes
	// (SR-3.6) have the same shape and would also match.
	LaunchIdentityWrite Kind = 5
)

// String names the kind, for test failure messages.
func (k Kind) String() string {
	switch k {
	case ReuseArchive:
		return "reuse archive"
	case ReuseReset:
		return "reuse reset"
	case ReusePermissionDelete:
		return "reuse permission-request deletion"
	case ReuseRestore:
		return "reuse restore"
	case LaunchIdentityWrite:
		return "launch identity write"
	default:
		return fmt.Sprintf("writefailfix.Kind(%d)", int(k))
	}
}

// Kinds lists every kind, for table-driven tests.
func Kinds() []Kind {
	return []Kind{ReuseArchive, ReuseReset, ReusePermissionDelete, ReuseRestore, LaunchIdentityWrite}
}

// createTargetsTable creates the bookkeeping table: one row per installed
// failure, naming its kind and instance id. The state literals in the
// triggers mirror internal/store's State constants (this leaf package cannot
// import them).
const createTargetsTable = `CREATE TABLE IF NOT EXISTS ad_test_write_failure (
    id                  INTEGER PRIMARY KEY,
    kind                INTEGER NOT NULL,
    claude_instance_id  TEXT NOT NULL
)`

// trigger is one CREATE TRIGGER statement and the trigger's name.
type trigger struct {
	name   string
	create string
}

// triggersByKind holds every trigger's SQL: the single source (SR-20.3).
// The kind number in each WHEN clause is the Kind constant's fixed value.
var triggersByKind = map[Kind][]trigger{
	ReuseArchive: {
		{
			name: "ad_test_fail_reuse_archive_insert",
			create: `CREATE TRIGGER IF NOT EXISTS ad_test_fail_reuse_archive_insert
    BEFORE INSERT ON session_history
    WHEN EXISTS (SELECT 1 FROM ad_test_write_failure
                  WHERE kind = 1 AND claude_instance_id = NEW.claude_instance_id)
BEGIN
    SELECT RAISE(ABORT, 'injected write failure: reuse archive');
END`,
		},
		{
			name: "ad_test_fail_reuse_archive_update",
			create: `CREATE TRIGGER IF NOT EXISTS ad_test_fail_reuse_archive_update
    BEFORE UPDATE ON session_history
    WHEN EXISTS (SELECT 1 FROM ad_test_write_failure
                  WHERE kind = 1 AND claude_instance_id = OLD.claude_instance_id)
BEGIN
    SELECT RAISE(ABORT, 'injected write failure: reuse archive');
END`,
		},
	},
	ReuseReset: {
		{
			name: "ad_test_fail_reuse_reset",
			create: `CREATE TRIGGER IF NOT EXISTS ad_test_fail_reuse_reset
    BEFORE UPDATE ON spawns
    WHEN OLD.state IN ('ended', 'missing') AND NEW.state = 'pending'
     AND EXISTS (SELECT 1 FROM ad_test_write_failure
                  WHERE kind = 2 AND claude_instance_id = OLD.claude_instance_id)
BEGIN
    SELECT RAISE(ABORT, 'injected write failure: reuse reset');
END`,
		},
	},
	ReusePermissionDelete: {
		{
			name: "ad_test_fail_reuse_permission_delete",
			create: `CREATE TRIGGER IF NOT EXISTS ad_test_fail_reuse_permission_delete
    BEFORE DELETE ON permission_requests
    WHEN EXISTS (SELECT 1 FROM ad_test_write_failure
                  WHERE kind = 3 AND claude_instance_id = OLD.claude_instance_id)
BEGIN
    SELECT RAISE(ABORT, 'injected write failure: reuse permission-request deletion');
END`,
		},
	},
	ReuseRestore: {
		{
			name: "ad_test_fail_reuse_restore",
			create: `CREATE TRIGGER IF NOT EXISTS ad_test_fail_reuse_restore
    BEFORE UPDATE ON spawns
    WHEN OLD.state = 'pending' AND NEW.state IN ('ended', 'missing')
     AND EXISTS (SELECT 1 FROM ad_test_write_failure
                  WHERE kind = 4 AND claude_instance_id = OLD.claude_instance_id)
BEGIN
    SELECT RAISE(ABORT, 'injected write failure: reuse restore');
END`,
		},
	},
	// UPDATE OF fires when the statement's SET names any listed column, so a
	// write of the same values still fails. NEW.state IS OLD.state excludes
	// the writes that begin a launch or restore one, which change the state.
	LaunchIdentityWrite: {
		{
			name: "ad_test_fail_launch_identity_write",
			create: `CREATE TRIGGER IF NOT EXISTS ad_test_fail_launch_identity_write
    BEFORE UPDATE OF tmux_server_pid, tmux_server_started, tmux_server_starttime,
                     pane_id, pane_pid, pane_starttime ON spawns
    WHEN NEW.state IS OLD.state
     AND EXISTS (SELECT 1 FROM ad_test_write_failure
                  WHERE kind = 5 AND claude_instance_id = OLD.claude_instance_id)
BEGIN
    SELECT RAISE(ABORT, 'injected write failure: launch identity write');
END`,
		},
	},
}

// Handle identifies one installed failure, for Remove.
type Handle struct {
	kind  Kind
	rowID int64
}

// Install makes kind's writes on instanceID fail, in one transaction on db
// (a connection to the test's temp store). The failure stays until Remove;
// installing the same kind for several ids, or twice for one id, is allowed.
func Install(db *sql.DB, kind Kind, instanceID string) (Handle, error) {
	triggers, ok := triggersByKind[kind]
	if !ok {
		return Handle{}, fmt.Errorf("writefailfix: unknown kind %v", kind)
	}
	tx, err := db.Begin()
	if err != nil {
		return Handle{}, fmt.Errorf("writefailfix: install %v: begin: %w", kind, err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit
	if _, err := tx.Exec(createTargetsTable); err != nil {
		return Handle{}, fmt.Errorf("writefailfix: install %v: create table: %w", kind, err)
	}
	for _, tr := range triggers {
		if _, err := tx.Exec(tr.create); err != nil {
			return Handle{}, fmt.Errorf("writefailfix: install %v: create trigger %s: %w", kind, tr.name, err)
		}
	}
	res, err := tx.Exec(
		`INSERT INTO ad_test_write_failure (kind, claude_instance_id) VALUES (?, ?)`,
		int(kind), instanceID,
	)
	if err != nil {
		return Handle{}, fmt.Errorf("writefailfix: install %v: record target: %w", kind, err)
	}
	rowID, err := res.LastInsertId()
	if err != nil {
		return Handle{}, fmt.Errorf("writefailfix: install %v: target id: %w", kind, err)
	}
	if err := tx.Commit(); err != nil {
		return Handle{}, fmt.Errorf("writefailfix: install %v: commit: %w", kind, err)
	}
	return Handle{kind: kind, rowID: rowID}, nil
}

// Remove undoes one Install, in one transaction on db. It drops the kind's
// triggers once no id is recorded for the kind, and the bookkeeping table once
// no failure is installed at all, so the store is left as it was.
func (h Handle) Remove(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("writefailfix: remove %v: begin: %w", h.kind, err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit
	if _, err := tx.Exec(`DELETE FROM ad_test_write_failure WHERE id = ?`, h.rowID); err != nil {
		return fmt.Errorf("writefailfix: remove %v: forget target: %w", h.kind, err)
	}
	var sameKind, total int
	if err := tx.QueryRow(
		`SELECT COUNT(*) FILTER (WHERE kind = ?), COUNT(*) FROM ad_test_write_failure`, int(h.kind),
	).Scan(&sameKind, &total); err != nil {
		return fmt.Errorf("writefailfix: remove %v: count targets: %w", h.kind, err)
	}
	if sameKind == 0 {
		for _, tr := range triggersByKind[h.kind] {
			if _, err := tx.Exec(`DROP TRIGGER IF EXISTS ` + tr.name); err != nil {
				return fmt.Errorf("writefailfix: remove %v: drop trigger %s: %w", h.kind, tr.name, err)
			}
		}
	}
	if total == 0 {
		if _, err := tx.Exec(`DROP TABLE IF EXISTS ad_test_write_failure`); err != nil {
			return fmt.Errorf("writefailfix: remove %v: drop table: %w", h.kind, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("writefailfix: remove %v: commit: %w", h.kind, err)
	}
	return nil
}
