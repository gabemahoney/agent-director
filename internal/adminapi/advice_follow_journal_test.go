//go:build linux

package adminapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/inputjournal"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/journalfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"golang.org/x/sys/unix"
)

// g13Case is one G13 attempt: a store in a private directory under a private
// HOME, one declared legacy root, and a verifier stub that passes unless
// verify says otherwise. Each run backs up to a new directory.
type g13Case struct {
	dir, db, root string
	ctx           context.Context
	opts          JournalMigrationOptions
	verify        func(ctx context.Context, call int) error
	calls         int
	backupParent  string
	backups       int
}

func newG13Case(t *testing.T, version int) *g13Case {
	t.Helper()
	c := &g13Case{dir: t.TempDir(), ctx: context.Background()}
	c.backupParent = c.dir
	for _, dir := range []string{"home", "store", filepath.Join("roots", "extra")} {
		if err := os.MkdirAll(filepath.Join(c.dir, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", filepath.Join(c.dir, "home"))
	c.root = filepath.Join(c.dir, "roots", "extra")
	if version == 0 {
		db, err := apitest.InitStore(filepath.Join(c.dir, "store", "state.db"))
		if err != nil {
			t.Fatal(err)
		}
		c.db = db
	} else {
		c.db = journalfix.PreCutoverStore(t, filepath.Join(c.dir, "store"), version)
		journalfix.Authorize(t, c.db)
	}
	c.opts = JournalMigrationOptions{MaintenanceWindow: true, RootsComplete: true, LegacyRoots: []string{c.root}}
	t.Cleanup(SetJournalVerifierForTest(func(ctx context.Context, _ string, _ []string) error {
		c.calls++
		if c.verify != nil {
			return c.verify(ctx, c.calls)
		}
		return nil
	}))
	return c
}

func (c *g13Case) run() (store.InputMigrationResult, error) {
	c.backups++
	c.opts.BackupDir = filepath.Join(c.backupParent, fmt.Sprintf("backup-%d", c.backups))
	return MigrateJournal(c.ctx, c.db, c.opts)
}

// exec runs statements on the store through a raw connection.
func (c *g13Case) exec(t *testing.T, statements ...string) {
	t.Helper()
	raw, err := sql.Open("sqlite", c.db)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	for _, s := range statements {
		if _, err := raw.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

// journalInstaller stands in for "re-run this release's installer with a new
// --journal-backup" (SR-2.16's next step), by the route install.sh takes for
// the store at db: before the native-input cutover (no authority recorded) it
// writes the authorization for the store's version, then runs migrate-journal
// with its journal flags and the new backup (migrate); after it, it writes
// the authorization while the store is older than the release, then opens the
// store as its `agent-director list` does, which never verifies. The step's
// own direct migrate-journal run follows it in the caller. It is this
// package's one installer stand-in (a reusable test fixture, registered in
// docs/architecture.md): a literal follow of that step uses it, never a
// second.
func journalInstaller(t *testing.T, db string, migrate func() (store.InputMigrationResult, error)) {
	t.Helper()
	reference, err := apitest.InitStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	current, _ := journalStoreAuthority(t, reference)
	version, authority := journalStoreAuthority(t, db)
	if version < current {
		journalfix.Authorize(t, db)
	}
	if authority == "" {
		if result, err := migrate(); err != nil || result.Authority != "sqlite" {
			t.Fatalf("the installer's migrate-journal = %+v, %v; want authority sqlite", result, err)
		}
		return
	}
	s, err := store.Open(db)
	if err != nil {
		t.Fatalf("the installer's store open: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

// journalFaults installs store faults until the returned restore, or the test's end.
func journalFaults(t *testing.T, f store.InputMigrationFaults) (restore func()) {
	restore = store.SetInputMigrationFaultsForTest(f)
	t.Cleanup(restore)
	return restore
}

// journalStoreDump is the store's version, schema and rows, its bytes when it
// is no SQLite database, or "absent", so a test can show a refusal changed
// nothing.
func journalStoreDump(t *testing.T, db string) string {
	t.Helper()
	if _, err := os.Lstat(db); os.IsNotExist(err) {
		return "absent" // an open would create it
	}
	raw, err := sql.Open("sqlite", db)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var version int
	if err := raw.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		data, err := os.ReadFile(db)
		if err != nil {
			t.Fatal(err)
		}
		return "bytes " + string(data)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "user_version %d\n", version)
	rows, err := raw.Query(`SELECT type, name, COALESCE(sql, '') FROM sqlite_master ORDER BY type, name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var kind, name, ddl string
		if err := rows.Scan(&kind, &name, &ddl); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintln(&b, kind, name, ddl)
		if kind == "table" {
			tables = append(tables, name)
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		rows, err := raw.Query(fmt.Sprintf(`SELECT * FROM %q`, table))
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintln(&b, table, values)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			t.Fatal(err)
		}
	}
	return b.String()
}

// journalStoreAuthority is the store's version and recorded native-input
// authority ("" when none is recorded).
func journalStoreAuthority(t *testing.T, db string) (version int, authority string) {
	t.Helper()
	raw, err := sql.Open("sqlite", db)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if err := raw.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	err = raw.QueryRow(`SELECT value FROM store_meta WHERE key = 'native_input_authority'`).Scan(&authority)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	return version, authority
}

// G13: each reason a store cause becomes, and interrupted,
// migration_incomplete_or_commit_unknown and committed_verification_incomplete,
// with its own action followed literally, e.g. "wait for the other
// administrator's migrate-journal to finish, then re-run this release's
// installer with a new --journal-backup, which finishes any pending schema
// step, then run migrate-journal with a new --backup-dir until it exits 0 with
// authority "sqlite"": the installer (its stand-in, journalInstaller), then the
// direct run. A human step (a newer release, a store changed by hand) is
// followed by the human-only rule instead: re-issued while the condition
// holds, the same refusal and nothing changed; once a human clears it, the
// action's next step passes.
func TestAdviceFollow_G13_StoreCauseReasons(t *testing.T) {
	const nextStep = "re-run this release's installer with a new --journal-backup, which finishes any pending schema step, then run migrate-journal with a new --backup-dir until it exits 0 with authority \"sqlite\""
	const retry = "then " + nextStep
	const storeOpenFailed = "point --store-path (install.sh: the config's [store] db_path) at the existing store, in a directory the user running the migration can write, and make the store a readable and writable SQLite database owned by that user; if it already is one, its metadata is damaged: inspect it or restore it from its pre-upgrade backup; " + retry
	reference, err := apitest.InitStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	current, _ := journalStoreAuthority(t, reference)
	listed := JournalProcess{PID: 4242, Starttime: "1", Role: "agent_director_mutator", Action: "stop_and_restart_after_cutover"}
	for _, tc := range []struct {
		name, reason, action string
		// version is the store's: 14 or 15 (pre-cutover, authorized) or 0
		// (a current store).
		version   int
		cutover   string
		authority string // result.Authority, where the attempt read it
		noBackup  bool   // refused before taking any backup
		human     bool   // follow clears a human-only condition
		processes []JournalProcess
		// arm sets the failure up; it returns the action's step, or nil
		// where another test follows this reason's action.
		arm   func(t *testing.T, c *g13Case) (follow func())
		check func(t *testing.T, c *g13Case)
	}{
		{name: "migration_lock_held", reason: "migration_lock_held", version: 15, cutover: store.CutoverUnknown, noBackup: true,
			action: "wait for the other administrator's migrate-journal to finish, " + retry,
			arm: func(t *testing.T, c *g13Case) func() {
				lock := c.db + ".input-migration.lock"
				holder, err := os.OpenFile(lock, os.O_CREATE|os.O_RDWR, 0o600)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = holder.Close() })
				if err := unix.Flock(int(holder.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
					t.Fatal(err)
				}
				// The other administrator finishes: it unlinks its lock, then releases it.
				return func() {
					if err := errors.Join(os.Remove(lock), holder.Close()); err != nil {
						t.Fatal(err)
					}
				}
			}},
		{name: "migration_lock_unestablished", reason: "migration_lock_unestablished", version: 15, cutover: store.CutoverUnknown, noBackup: true,
			action: "only after confirming no administrator is running, remove <store>.input-migration.lock, " + retry,
			arm: func(t *testing.T, c *g13Case) func() {
				lock := c.db + ".input-migration.lock"
				if err := os.WriteFile(lock, []byte("operator evidence\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				return func() {
					// No administrator holds it: the lock is free to take.
					f, err := os.Open(lock)
					if err != nil {
						t.Fatal(err)
					}
					if err := errors.Join(unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB), f.Close(), os.Remove(lock)); err != nil {
						t.Fatal(err)
					}
				}
			}},
		{name: "authorization_missing", reason: "authorization_missing", version: 15, cutover: store.CutoverNotCommitted, noBackup: true,
			action: "the installer writes the migration authorization, which migrate-journal never does: " + nextStep + "; the store was not changed",
			arm: func(t *testing.T, c *g13Case) func() {
				if err := os.Remove(filepath.Join(filepath.Dir(c.db), "migrate-authorized")); err != nil {
					t.Fatal(err)
				}
				return func() {} // the next step alone; G14 follows it with the real installer
			}},
		{name: "store_version_unreadable", reason: "store_version_unreadable", version: 15, cutover: store.CutoverNotCommitted, noBackup: true,
			action: "stop every process that holds the store open, make it a readable SQLite database and leave room in the temp directory (TMPDIR) for a private copy of it, " + retry + "; the store was not changed",
			arm: func(t *testing.T, c *g13Case) func() {
				good, err := os.ReadFile(c.db)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(c.db, []byte("not a database\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				return func() {
					if err := os.WriteFile(c.db, good, 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}},
		{name: "store_newer_than_release", reason: "store_newer_than_release", cutover: store.CutoverNotCommitted, noBackup: true, human: true,
			action: "a newer release made this store: install that release or a later one instead of this one; the store was not changed",
			arm: func(t *testing.T, c *g13Case) func() {
				c.exec(t, fmt.Sprintf("PRAGMA user_version = %d", current+1))
				// The release and the store agree again.
				return func() { c.exec(t, fmt.Sprintf("PRAGMA user_version = %d", current)) }
			}},
		{name: "store_open_failed absent store", reason: "store_open_failed", version: 15, cutover: store.CutoverNotCommitted, noBackup: true,
			action: storeOpenFailed,
			arm: func(t *testing.T, c *g13Case) func() {
				existing := c.db
				c.db = filepath.Join(filepath.Dir(existing), "missing.db")
				return func() { c.db = existing } // point --store-path at the existing store
			}},
		// R2-3: the lock cannot be created in the store's directory.
		{name: "store_open_failed unwritable directory", reason: "store_open_failed", version: 15, cutover: store.CutoverNotCommitted, noBackup: true,
			action: storeOpenFailed,
			arm: func(t *testing.T, c *g13Case) func() {
				dir := filepath.Dir(c.db)
				t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
				if err := os.Chmod(dir, 0o500); err != nil {
					t.Fatal(err)
				}
				return func() { // a directory the user running the migration can write
					if err := os.Chmod(dir, 0o700); err != nil {
						t.Fatal(err)
					}
				}
			}},
		// "its metadata is damaged: inspect it": the inspection puts back the
		// store id a hand change removed.
		{name: "store_open_failed damaged metadata", reason: "store_open_failed", cutover: store.CutoverNotCommitted, human: true,
			action: storeOpenFailed,
			arm: func(t *testing.T, c *g13Case) func() {
				id, err := apitest.ReadStoreID(c.db)
				if err != nil {
					t.Fatal(err)
				}
				c.exec(t, `DELETE FROM store_meta WHERE key = 'store_id'`)
				return func() { c.exec(t, fmt.Sprintf(`INSERT INTO store_meta(key, value) VALUES ('store_id', '%s')`, id)) }
			}},
		{name: "store_busy", reason: "store_busy", version: 15, cutover: store.CutoverNotCommitted,
			action: "stop every process that holds the store open, keep the maintenance window, " + retry,
			arm: func(t *testing.T, c *g13Case) func() {
				restore := journalFaults(t, store.InputMigrationFaults{BusyTimeoutMs: 1})
				release := apitest.HoldWriteLock(t, c.db, time.Minute)
				return func() { release(); restore() }
			}},
		{name: "schema_step_failed cutover", reason: "schema_step_failed", version: 15, cutover: store.CutoverNotCommitted,
			action: "fix what stopped the schema step, such as disk space, write access or a schema changed by hand, keep the maintenance window, " + retry,
			arm: func(t *testing.T, c *g13Case) func() {
				return storefix.InjectWriteFailure(t, c.db, storefix.WriteFailInputAuthority, "native_input_authority")
			}},
		{name: "schema_step_failed ordinary step", reason: "schema_step_failed", version: 14, cutover: store.CutoverNotCommitted,
			action: "fix what stopped the schema step, such as disk space, write access or a schema changed by hand, keep the maintenance window, " + retry,
			arm: func(t *testing.T, c *g13Case) func() {
				// Step 14→15 adds this column; one added by hand conflicts.
				c.exec(t, `ALTER TABLE spawns ADD COLUMN codex_turn_ended TEXT GENERATED ALWAYS AS (NULL) VIRTUAL`)
				return func() { c.exec(t, `ALTER TABLE spawns DROP COLUMN codex_turn_ended`) }
			}},
		// The verifier's own site gives the same reason and action; G12 follows it.
		{name: "legacy_root_unreadable", reason: "legacy_root_unreadable", version: 15, cutover: store.CutoverNotCommitted,
			action: "make each legacy root and its parent directories readable and searchable by the user running the migration, keep the maintenance window, " + retry,
			arm: func(t *testing.T, c *g13Case) func() {
				t.Cleanup(func() { _ = os.Chmod(c.root, 0o700) })
				c.verify = func(_ context.Context, call int) error {
					if call == 2 { // inside the cutover step, after the backup
						return os.Chmod(c.root, 0)
					}
					return nil
				}
				return nil
			}},
		{name: "legacy_writer_active", reason: "legacy_writer_active", version: 15, cutover: store.CutoverNotCommitted,
			action: "stop the old agent-director process that holds a legacy launch lock, keep the maintenance window, " + retry,
			arm: func(t *testing.T, c *g13Case) func() {
				id, err := apitest.ReadStoreID(c.db)
				if err != nil {
					t.Fatal(err)
				}
				launch := filepath.Join(c.root, id, strings.Repeat("a", 16))
				if err := os.MkdirAll(launch, 0o700); err != nil {
					t.Fatal(err)
				}
				writer, err := os.OpenFile(launch+".lock", os.O_CREATE|os.O_RDWR, 0o600)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = writer.Close() })
				if err := inputjournal.LockWriter(writer); err != nil {
					t.Fatal(err)
				}
				return func() {
					if err := writer.Close(); err != nil {
						t.Fatal(err)
					}
				}
			}},
		{name: "legacy_evidence_unestablished", reason: "legacy_evidence_unestablished", version: 15, cutover: store.CutoverNotCommitted,
			action: "make the launch evidence under each legacy root readable and writable by the user running the migration and remove what is not this store's launch evidence; if the store itself records one launch token twice, inspect it or restore it from its pre-upgrade backup; keep the maintenance window, " + retry,
			arm: func(t *testing.T, c *g13Case) func() {
				id, err := apitest.ReadStoreID(c.db)
				if err != nil {
					t.Fatal(err)
				}
				stray := filepath.Join(c.root, id, "stray")
				if err := os.MkdirAll(filepath.Dir(stray), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(stray, []byte("not launch evidence"), 0o600); err != nil {
					t.Fatal(err)
				}
				return func() {
					if err := os.Remove(stray); err != nil {
						t.Fatal(err)
					}
				}
			}},
		{name: "native_input_state_inconsistent", reason: "native_input_state_inconsistent", cutover: store.CutoverNotCommitted, human: true,
			action: "the store's native-input state was changed by hand: inspect the store or restore it from its pre-upgrade backup, keep the maintenance window, " + retry,
			arm: func(t *testing.T, c *g13Case) func() {
				_, authority := journalStoreAuthority(t, c.db)
				c.exec(t, `DELETE FROM store_meta WHERE key = 'native_input_authority'`)
				// Inspection puts back what the hand change removed.
				return func() {
					c.exec(t, fmt.Sprintf(`INSERT INTO store_meta(key, value) VALUES ('native_input_authority', '%s')`, authority))
				}
			}},
		{name: "backup_failed", reason: "backup_failed", version: 15, cutover: store.CutoverNotCommitted, noBackup: true,
			action: "make the store and everything under the legacy roots readable regular files and directories, and choose a new backup directory in a writable directory with room for their copy, " + retry + "; the store was not changed",
			arm: func(t *testing.T, c *g13Case) func() {
				c.backupParent = filepath.Join(c.dir, "read-only")
				if err := os.Mkdir(c.backupParent, 0o500); err != nil {
					t.Fatal(err)
				}
				return func() { c.backupParent = c.dir }
			}},
		// The argument check's own reason and action; G11 follows it.
		{name: "backup_exists", reason: "backup_exists", version: 15, cutover: store.CutoverNotCommitted,
			action: "choose a --backup-dir (install.sh: --journal-backup) that does not exist yet; keep the existing one",
			arm: func(t *testing.T, c *g13Case) func() {
				c.verify = func(_ context.Context, call int) error {
					if call == 1 { // made after the argument checks found it absent
						return os.Mkdir(c.opts.BackupDir, 0o700)
					}
					return nil
				}
				return nil
			},
			check: func(t *testing.T, c *g13Case) {
				assertJournalArgumentRefusal(t, "CheckJournalMigration", CheckJournalMigration(c.opts), "backup_exists")
			}},
		{name: "attempt_deadline", reason: "attempt_deadline", version: 15, cutover: store.CutoverNotCommitted, noBackup: true,
			action: "make the attempt finish within its time limit (a less busy disk for the store and the backup), keep the maintenance window, " + retry,
			arm: func(t *testing.T, _ *g13Case) func() {
				return journalFaults(t, store.InputMigrationFaults{AttemptTimeout: -time.Second})
			}},
		{name: "interrupted", reason: "interrupted", version: 15, cutover: store.CutoverNotCommitted,
			action: "the attempt was interrupted before its cutover commit; keep the maintenance window, " + retry,
			arm: func(t *testing.T, c *g13Case) func() {
				ctx, cancel := context.WithCancel(context.Background())
				t.Cleanup(cancel)
				c.ctx = ctx
				var work context.Context
				c.verify = func(ctx context.Context, _ int) error {
					work = ctx // the attempt's own work context
					return nil
				}
				restore := journalFaults(t, store.InputMigrationFaults{BeforeCutoverCommit: func() {
					cancel()
					<-work.Done()
				}})
				return func() {
					restore()
					c.ctx = context.Background()
				}
			}},
		{name: "migration_incomplete_or_commit_unknown", reason: "migration_incomplete_or_commit_unknown", version: 15, cutover: store.CutoverUnknown,
			action: "keep the maintenance window; re-run this release's installer with a new --journal-backup, which finishes any pending schema step, then run migrate-journal with a new --backup-dir until it exits 0 with authority \"sqlite\"; details.cutover says whether the cutover committed",
			arm: func(t *testing.T, c *g13Case) func() {
				restore := journalFaults(t, store.InputMigrationFaults{CutoverCommit: func(commit func() error) error {
					if err := commit(); err != nil {
						return err
					}
					return errors.New("commit result lost")
				}})
				return restore
			},
			check: func(t *testing.T, c *g13Case) {
				if _, authority := journalStoreAuthority(t, c.db); authority == "" {
					t.Error("the cutover's commit landed, but no authority is recorded")
				}
			}},
		{name: "committed_verification_incomplete", reason: "committed_verification_incomplete", version: 15, cutover: store.CutoverCommitted, authority: "sqlite",
			processes: []JournalProcess{listed},
			action:    "the cutover committed; keep the maintenance window, stop any listed process, then run migrate-journal again with a new --backup-dir until it exits 0 with authority \"sqlite\"",
			arm: func(t *testing.T, c *g13Case) func() {
				stopped := false
				c.verify = func(_ context.Context, call int) error {
					if call == 3 && !stopped { // after the cutover committed
						return journalRefusal("old_or_unverified_mutators", listed)
					}
					return nil
				}
				return func() { stopped = true }
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newG13Case(t, tc.version)
			follow := tc.arm(t, c)
			before := journalStoreDump(t, c.db)
			refused := func(result store.InputMigrationResult, err error) {
				t.Helper()
				var r *JournalRefusal
				if !errors.As(err, &r) || r.Reason != tc.reason || r.Action != tc.action {
					t.Fatalf("refusal = %#v (%v); want %s with %q", r, err, tc.reason, tc.action)
				}
				processes := tc.processes
				if processes == nil {
					processes = []JournalProcess{}
				}
				if !reflect.DeepEqual(r.Processes, processes) {
					t.Errorf("processes = %+v; want %+v", r.Processes, processes)
				}
				assertJournalCommit(t, "MigrateJournal", err, tc.cutover, false)
				if result.Authority != tc.authority {
					t.Errorf("result.authority = %q; want %q", result.Authority, tc.authority)
				}
				if tc.cutover == store.CutoverNotCommitted {
					if after := journalStoreDump(t, c.db); after != before {
						t.Errorf("a not_committed refusal changed the store:\n before %s\n after  %s", before, after)
					}
				}
				if _, err := os.Lstat(c.opts.BackupDir); tc.noBackup && !os.IsNotExist(err) {
					t.Errorf("refused before the backup, but made one: %v", err)
				}
			}
			result, err := c.run()
			refused(result, err)
			if tc.check != nil {
				tc.check(t, c)
			}
			if follow == nil {
				return
			}
			if tc.human {
				refused(c.run()) // the condition still holds
			}
			follow()
			if strings.Contains(tc.action, nextStep) {
				journalInstaller(t, c.db, c.run)
			}
			if result, err := c.run(); err != nil || result.Authority != "sqlite" {
				t.Fatalf("followed advice, migrate-journal = %+v, %v; want authority sqlite", result, err)
			}
			id, err := apitest.ReadStoreID(c.db)
			if err != nil {
				t.Fatal(err)
			}
			if version, authority := journalStoreAuthority(t, c.db); version != current || authority != "sqlite-v1:"+id {
				t.Errorf("migrated store at v%d with authority %q; want v%d with sqlite-v1:%s", version, authority, current, id)
			}
		})
	}
}
