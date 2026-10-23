// Package writefailfix is the single source of the triggers that make one
// named kind of store write fail in a test (SR-20.3, SRD-RR2 T4).
//
// This is a LEAF test-support package, like procstarttimefix: it imports
// nothing from internal/store (or any other agent-director package). Its one
// installer is storefix.InjectWriteFailure, which every test that fails a
// write by kind uses, internal/store's own (package store_test, e.g.
// store_errors_test.go) included.
//
// Every trigger's SQL text lives in this file and nowhere else. A trigger is
// fixed text per kind; the instance id it applies to is never spliced into the
// SQL. Install records (kind, instance id) in a bookkeeping table with bound
// parameters, and each trigger fires only when its row's instance id is
// recorded there for its kind, so seeding, other rows and other ids are
// unaffected. A matched write fails with the driver error of RAISE(ABORT),
// which the store method returns as its error: never a silent no-op.
// MissingDeadlineOverrun's trigger first runs CPU-bound SQL far longer than a
// second, so a matched write with a deadline returns its deadline's error
// instead.
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
	// archive inside RecordSessionStartIdentity's transaction, which is
	// fail-open, so the SessionStart write still commits but archives
	// nothing; and any seeding of history for the id.
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
	// spawn's end write after "duplicate session" (store.EndHeldLaunch,
	// SR-9.4; the kind SR-5.8's store-error case uses, since no other write
	// runs on that no-pane row), and SeedSpawn of a finished row (install
	// after seeding).
	ReuseRestore Kind = 4
	// LaunchIdentityWrite fails the launch identity write
	// (store.RecordLaunchIdentity, SR-3.6): an update of the id's spawns row
	// whose SET names the server identity columns, pane_id or pane_pid and
	// leaves state unchanged. It fires whether or not the values differ, and
	// never on the insert, a hook write or any other kind's write: a gated hook
	// write names only pane_starttime of these columns (it records the pane
	// start time when NULL, SR-22.9), which the trigger does not list. Also
	// matched: SeedSpawn's option and default update and the seeders' seed
	// pane writes (storefix.WithSeedPane), so install after seeding. Later
	// adoption writes (SR-3.6) have the same shape and would also match.
	LaunchIdentityWrite Kind = 5
	// PermissionDecision fails a write of a decision on one of the id's open
	// permission requests: decide's, the relay hook's timeout deny,
	// find-missing's close inside its mark's transaction (b.146 rule 12),
	// which then rolls the mark back too, and the pane-answer writes that
	// record one (b.146 step 2b: a pane answer's sent write after its key,
	// record-pane-answer's outside record and the PostToolUse close), but not
	// a pane answer's intent or its release. A delete of a request (the spawn
	// delete cascade, the cap eviction) is not matched.
	PermissionDecision Kind = 6
	// SubagentRecord fails every insert, update and delete of the id's rows in
	// subagents and in subagent_launches (b.xjn): the writes that create,
	// change or remove a subagent entry or a claimable launch. Also matched:
	// the SR-8.1 closes, which all five closing writes carry (the ended write,
	// find-missing's mark, its move of a Codex row to ended, resume's move and
	// reuse's reset): a close updates
	// the id's running entries and deletes the id's claimable launches, and the
	// triggers fire per affected row, so a close fails only when the id has a
	// running entry or a claimable launch (with neither, it writes nothing and
	// succeeds). Also matched: the caps' evictions, a delete of the id's spawns row (delete, expire) while it
	// still has entries or claimable launches, through the cascade, and
	// apitest.SeedSubagent and apitest.SeedSubagentLaunch: install after
	// seeding. Writes to the id's spawns row itself, and every write to another
	// id's subagent rows, are not matched.
	SubagentRecord Kind = 7
	// MissingDeadlineOverrun makes an update of the id's spawns row to missing
	// outrun its deadline (b.6uf): the trigger runs CPU-bound SQL for far longer
	// than a second (about 10^9 rows), then RAISE(ABORT). A write with a
	// deadline is interrupted first and returns its deadline's error (wrapping
	// context.DeadlineExceeded), 1 s each: MarkCursorMissingIfSameLaunch (whose
	// save get, status and list then defer) and MarkFailedCursorRuntimeMissing
	// (cursorhost's failed-launch close, which returns the error). Also
	// matched, with no deadline, so each spins far longer before it fails:
	// find-missing's mark (MarkMissingIfSameLife), resume's and reuse's restore
	// of a missing row, and SeedSpawn of a missing row. Install it only where
	// none of these runs, after seeding.
	MissingDeadlineOverrun Kind = 8
	// DisplayObservation fails the final report write after request clocks are written.
	DisplayObservation Kind = 9
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
	case PermissionDecision:
		return "permission decision"
	case SubagentRecord:
		return "subagent record"
	case MissingDeadlineOverrun:
		return "missing deadline overrun"
	case DisplayObservation:
		return "display observation"
	default:
		return fmt.Sprintf("writefailfix.Kind(%d)", int(k))
	}
}

// Kinds lists every kind, for table-driven tests.
func Kinds() []Kind {
	return []Kind{ReuseArchive, ReuseReset, ReusePermissionDelete, ReuseRestore, LaunchIdentityWrite, PermissionDecision, SubagentRecord, MissingDeadlineOverrun, DisplayObservation}
}

// createTargetsTable creates the bookkeeping table: one row per installed
// failure, naming its kind and instance id. The state literals in the
// triggers mirror internal/store's State constants (this leaf package cannot
// import them).
const createTargetsTable = `CREATE TABLE IF NOT EXISTS ad_test_write_failure (
    id                  INTEGER PRIMARY KEY,
    kind                INTEGER NOT NULL,
    instance_id         TEXT NOT NULL
)`

// trigger is one CREATE TRIGGER statement and the trigger's name. setup,
// when set, creates and fills (idempotently) a table only the trigger reads,
// before it; teardown drops that table, after the trigger is dropped.
type trigger struct {
	name     string
	create   string
	setup    []string
	teardown string
}

// triggersByKind holds every trigger's SQL: the single source (SR-20.3).
// The kind number in each WHEN clause is the Kind constant's fixed value.
var triggersByKind = map[Kind][]trigger{
	DisplayObservation: {{name: "ad_test_fail_display_observation", create: `CREATE TRIGGER IF NOT EXISTS ad_test_fail_display_observation
 BEFORE UPDATE OF display_observation ON spawns
 WHEN NEW.row_version IS OLD.row_version AND NEW.permission_epoch IS OLD.permission_epoch
 AND EXISTS (SELECT 1 FROM ad_test_write_failure WHERE kind=9 AND instance_id=OLD.instance_id)
 BEGIN SELECT RAISE(ABORT, 'injected write failure: display observation'); END`}},
	ReuseArchive: {
		{
			name: "ad_test_fail_reuse_archive_insert",
			create: `CREATE TRIGGER IF NOT EXISTS ad_test_fail_reuse_archive_insert
    BEFORE INSERT ON session_history
    WHEN EXISTS (SELECT 1 FROM ad_test_write_failure
                  WHERE kind = 1 AND instance_id = NEW.instance_id)
BEGIN
    SELECT RAISE(ABORT, 'injected write failure: reuse archive');
END`,
		},
		{
			name: "ad_test_fail_reuse_archive_update",
			create: `CREATE TRIGGER IF NOT EXISTS ad_test_fail_reuse_archive_update
    BEFORE UPDATE ON session_history
    WHEN EXISTS (SELECT 1 FROM ad_test_write_failure
                  WHERE kind = 1 AND instance_id = OLD.instance_id)
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
                  WHERE kind = 2 AND instance_id = OLD.instance_id)
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
                  WHERE kind = 3 AND instance_id = OLD.instance_id)
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
                  WHERE kind = 4 AND instance_id = OLD.instance_id)
BEGIN
    SELECT RAISE(ABORT, 'injected write failure: reuse restore');
END`,
		},
	},
	// UPDATE OF fires when the statement's SET names any listed column, so a
	// write of the same values still fails. NEW.state IS OLD.state excludes
	// the writes that begin a launch or restore one, which change the state.
	// pane_starttime is not listed: every gated hook write sets it (to itself
	// when already recorded, SR-22.9), and the trigger must never fire on a
	// hook write; the launch identity write still names five listed columns.
	LaunchIdentityWrite: {
		{
			name: "ad_test_fail_launch_identity_write",
			create: `CREATE TRIGGER IF NOT EXISTS ad_test_fail_launch_identity_write
    BEFORE UPDATE OF tmux_server_pid, tmux_server_started, tmux_server_starttime,
                     pane_id, pane_pid ON spawns
    WHEN NEW.state IS OLD.state
     AND EXISTS (SELECT 1 FROM ad_test_write_failure
                  WHERE kind = 5 AND instance_id = OLD.instance_id)
BEGIN
    SELECT RAISE(ABORT, 'injected write failure: launch identity write');
END`,
		},
	},
	// UPDATE OF decision fires on every decision write; OLD.decision IS NULL
	// limits it to an open request, the only kind any decision write changes.
	PermissionDecision: {
		{
			name: "ad_test_fail_permission_decision",
			create: `CREATE TRIGGER IF NOT EXISTS ad_test_fail_permission_decision
    BEFORE UPDATE OF decision ON permission_requests
    WHEN OLD.decision IS NULL
     AND EXISTS (SELECT 1 FROM ad_test_write_failure
                  WHERE kind = 6 AND instance_id = OLD.instance_id)
BEGIN
    SELECT RAISE(ABORT, 'injected write failure: permission decision');
END`,
		},
	},
	// Every insert, update and delete of the id's rows in either subagent
	// table, a cascaded delete included.
	SubagentRecord: {
		{
			name: "ad_test_fail_subagent_entry_insert",
			create: `CREATE TRIGGER IF NOT EXISTS ad_test_fail_subagent_entry_insert
    BEFORE INSERT ON subagents
    WHEN EXISTS (SELECT 1 FROM ad_test_write_failure
                  WHERE kind = 7 AND instance_id = NEW.instance_id)
BEGIN
    SELECT RAISE(ABORT, 'injected write failure: subagent record');
END`,
		},
		{
			name: "ad_test_fail_subagent_entry_update",
			create: `CREATE TRIGGER IF NOT EXISTS ad_test_fail_subagent_entry_update
    BEFORE UPDATE ON subagents
    WHEN EXISTS (SELECT 1 FROM ad_test_write_failure
                  WHERE kind = 7 AND instance_id = OLD.instance_id)
BEGIN
    SELECT RAISE(ABORT, 'injected write failure: subagent record');
END`,
		},
		{
			name: "ad_test_fail_subagent_entry_delete",
			create: `CREATE TRIGGER IF NOT EXISTS ad_test_fail_subagent_entry_delete
    BEFORE DELETE ON subagents
    WHEN EXISTS (SELECT 1 FROM ad_test_write_failure
                  WHERE kind = 7 AND instance_id = OLD.instance_id)
BEGIN
    SELECT RAISE(ABORT, 'injected write failure: subagent record');
END`,
		},
		{
			name: "ad_test_fail_subagent_launch_insert",
			create: `CREATE TRIGGER IF NOT EXISTS ad_test_fail_subagent_launch_insert
    BEFORE INSERT ON subagent_launches
    WHEN EXISTS (SELECT 1 FROM ad_test_write_failure
                  WHERE kind = 7 AND instance_id = NEW.instance_id)
BEGIN
    SELECT RAISE(ABORT, 'injected write failure: subagent record');
END`,
		},
		{
			name: "ad_test_fail_subagent_launch_update",
			create: `CREATE TRIGGER IF NOT EXISTS ad_test_fail_subagent_launch_update
    BEFORE UPDATE ON subagent_launches
    WHEN EXISTS (SELECT 1 FROM ad_test_write_failure
                  WHERE kind = 7 AND instance_id = OLD.instance_id)
BEGIN
    SELECT RAISE(ABORT, 'injected write failure: subagent record');
END`,
		},
		{
			name: "ad_test_fail_subagent_launch_delete",
			create: `CREATE TRIGGER IF NOT EXISTS ad_test_fail_subagent_launch_delete
    BEFORE DELETE ON subagent_launches
    WHEN EXISTS (SELECT 1 FROM ad_test_write_failure
                  WHERE kind = 7 AND instance_id = OLD.instance_id)
BEGIN
    SELECT RAISE(ABORT, 'injected write failure: subagent record');
END`,
		},
	},
	// UPDATE OF state with NEW.state 'missing'. Triggers cannot use WITH, so
	// the CPU-bound SQL cross joins a 1000-row pad table three times; the
	// driver interrupts it when the write's context ends.
	MissingDeadlineOverrun: {
		{
			name: "ad_test_fail_missing_deadline_overrun",
			create: `CREATE TRIGGER IF NOT EXISTS ad_test_fail_missing_deadline_overrun
    BEFORE UPDATE OF state ON spawns
    WHEN NEW.state = 'missing'
     AND EXISTS (SELECT 1 FROM ad_test_write_failure
                  WHERE kind = 8 AND instance_id = OLD.instance_id)
BEGIN
    SELECT max(a.n + b.n + c.n)
      FROM ad_test_overrun_pad a, ad_test_overrun_pad b, ad_test_overrun_pad c;
    SELECT RAISE(ABORT, 'injected write failure: missing deadline overrun');
END`,
			setup: []string{
				`CREATE TABLE IF NOT EXISTS ad_test_overrun_pad (n INTEGER PRIMARY KEY)`,
				`WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM seq WHERE n < 1000)
INSERT OR IGNORE INTO ad_test_overrun_pad (n) SELECT n FROM seq`,
			},
			teardown: `DROP TABLE IF EXISTS ad_test_overrun_pad`,
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
		for _, q := range tr.setup {
			if _, err := tx.Exec(q); err != nil {
				return Handle{}, fmt.Errorf("writefailfix: install %v: set up trigger %s: %w", kind, tr.name, err)
			}
		}
		if _, err := tx.Exec(tr.create); err != nil {
			return Handle{}, fmt.Errorf("writefailfix: install %v: create trigger %s: %w", kind, tr.name, err)
		}
	}
	res, err := tx.Exec(
		`INSERT INTO ad_test_write_failure (kind, instance_id) VALUES (?, ?)`,
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
// triggers, and any table only they read, once no id is recorded for the
// kind, and the bookkeeping table once no failure is installed at all, so the
// store is left as it was.
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
			if tr.teardown == "" {
				continue
			}
			if _, err := tx.Exec(tr.teardown); err != nil {
				return fmt.Errorf("writefailfix: remove %v: tear down trigger %s: %w", h.kind, tr.name, err)
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
