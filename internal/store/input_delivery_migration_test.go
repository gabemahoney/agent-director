package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/inputdelivery"
	"github.com/gabemahoney/agent-director/internal/testsupport/procstarttimefix"
	"golang.org/x/sys/unix"
)

// journalMigrationFixture extends the canonical released-schema fixture. It
// holds synthetic evidence for the already-supported Claude version; it does
// not qualify a new provider version or replace native transport evidence.
type journalMigrationFixture struct {
	path, root, dir string
	id              inputdelivery.Identity
	cap             inputdelivery.Capability
	attempt         *inputdelivery.Attempt
}

func newJournalMigrationFixture(t *testing.T) *journalMigrationFixture {
	t.Helper()
	var from int
	for _, step := range migrationSteps {
		if step.inputCutover {
			from = step.from
			break
		}
	}
	if from == 0 {
		t.Fatal("missing journal cutover step")
	}
	f := &journalMigrationFixture{path: makeVersionedDB(t, t.TempDir(), from), root: t.TempDir()}
	seedPreMigrationRow(t, f.path, from)
	f.id = inputdelivery.Identity{StoreID: assertOneStoreID(t, f.path), Token: strings.Repeat("a", 16), InstanceID: "pre", Agent: "claude", CWD: "/tmp", Socket: "/tmp/journal-test.sock", PaneID: "%7", PID: 123, Starttime: procstarttimefix.LinuxProcStarttime, LifeNumber: 3}
	withRaw(t, f.path, func(db *sql.DB) {
		mustExec(t, db, `UPDATE spawns SET state='waiting', settled_state='waiting', life_number=?, launch_token=?, tmux_socket=?, pane_id=?, pane_pid=?, pane_starttime=?, row_version=0 WHERE instance_id='pre'`, f.id.LifeNumber, f.id.Token, f.id.Socket, f.id.PaneID, f.id.PID, f.id.Starttime)
	})
	f.dir = filepath.Join(f.root, f.id.StoreID, f.id.Token)
	if err := os.MkdirAll(f.dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.dir+".lock", nil, 0600); err != nil {
		t.Fatal(err)
	}
	f.cap = inputdelivery.Capability{Agent: f.id.Agent, Version: "2.1.280", SessionID: "session", CWD: f.id.CWD, Socket: f.id.Socket, Token: f.id.Token, InstanceID: f.id.InstanceID, Transcript: filepath.Join(f.dir, "provider.jsonl"), Created: time.Unix(100, 0), Safe: true}
	raw := []byte(`{ "type":"user", "uuid":"9c8b2ab4-1880-48df-b2f7-8fc0a7e49311", "sessionId":"session", "cwd":"/tmp", "version":"2.1.280", "message":{"role":"user", "content":"hello <&>"} }`)
	f.attempt = &inputdelivery.Attempt{RequestID: "current", Identity: f.id, Payload: "hello <&>", PayloadSHA256: inputdelivery.Hash([]byte("hello <&>")), Phase: "submitted", BytesAttempted: len("hello <&>"), EnterAttempted: true, Watermark: map[string]inputdelivery.Watermark{f.cap.Transcript: {Size: 0, PrefixSHA256: inputdelivery.Hash(nil)}}, Proof: &inputdelivery.Proof{Path: f.cap.Transcript, SessionID: f.cap.SessionID, RecordID: "9c8b2ab4-1880-48df-b2f7-8fc0a7e49311", Raw: raw, RawSHA256: inputdelivery.Hash(raw)}}
	// Legacy evidence did not record the life number.
	f.attempt.Identity.LifeNumber = 0
	f.write(t, "launch.json", f.cap)
	f.write(t, "attempt.json", f.attempt)
	if err := os.WriteFile(f.cap.Transcript, append(append([]byte(nil), raw...), '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	writeSentinel(t, filepath.Dir(f.path), from, schemaVersion)
	return f
}

func (f *journalMigrationFixture) write(t *testing.T, name string, value any) {
	t.Helper()
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, name), b, 0600); err != nil {
		t.Fatal(err)
	}
}

func (f *journalMigrationFixture) migrate(t *testing.T, verify func(context.Context, string, []string) error) (InputMigrationResult, error) {
	t.Helper()
	if verify == nil {
		verify = func(context.Context, string, []string) error { return nil }
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	r, err := MigrateInputJournal(ctx, f.path, []string{f.root}, filepath.Join(t.TempDir(), "backup"), verify)
	if time.Since(start) > 5*time.Second {
		t.Fatalf("migration exceeded its one-call 5s budget: %v", err)
	}
	if _, statErr := os.Lstat(f.path + ".input-migration.lock"); !os.IsNotExist(statErr) {
		t.Fatalf("finished administrator left a per-store migration lock: %v", statErr)
	}
	return r, err
}

// migrationFailure is err as the *InputMigrationError every failed attempt
// returns, checked to carry cause and cutover (SR-2.16 "Commit state").
func migrationFailure(t *testing.T, err error, cause, cutover string) *InputMigrationError {
	t.Helper()
	var failure *InputMigrationError
	if !errors.As(err, &failure) {
		t.Fatalf("error is not an *InputMigrationError: %T %v", err, err)
	}
	if failure.Cause != cause || failure.Commit.Cutover() != cutover {
		t.Fatalf("cause %q, cutover %q (%+v); want %q, %q", failure.Cause, failure.Commit.Cutover(), failure.Commit, cause, cutover)
	}
	return failure
}

// R4: concurrent administrators cannot split ownership, and finishing either
// outcome removes only the temporary store lock, never a legacy launch inode.
func TestInputJournalMigrationConcurrentAdministrators(t *testing.T) {
	f := newJournalMigrationFixture(t)
	legacy, err := os.Stat(f.dir + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	before := dbDump(t, f.path)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	entered, resume, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	type completion struct {
		result InputMigrationResult
		err    error
	}
	done := make(chan completion, 1)
	backup := filepath.Join(t.TempDir(), "first")
	go func() {
		defer close(finished)
		checks := 0
		r, err := MigrateInputJournal(ctx, f.path, []string{f.root}, backup, func(context.Context, string, []string) error {
			checks++
			if checks == 2 { // The schema-step inventory is inside the lock.
				close(entered)
				select {
				case <-resume:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return nil
		})
		done <- completion{r, err}
	}()
	defer func() { cancel(); <-finished }()
	select {
	case <-entered:
	case result := <-done:
		t.Fatalf("first administrator missed held boundary: %+v", result)
	case <-ctx.Done():
		t.Fatal("first administrator never reached held boundary")
	}
	lockPath := f.path + ".input-migration.lock"
	owner, err := os.Stat(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	// Retain an opener across the owner's unlink/close. It must never become
	// a second administrator on that obsolete inode after a new owner enters.
	stale, err := os.OpenFile(lockPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer stale.Close()
	otherBackup := filepath.Join(t.TempDir(), "contender")
	r, err := MigrateInputJournal(ctx, f.path, []string{f.root}, otherBackup, func(context.Context, string, []string) error { return nil })
	if err == nil || err.Error() != "another administrator owns the journal migration" || r.Authority != "" {
		t.Fatalf("concurrent administrator entered: %+v %v", r, err)
	}
	if failure := migrationFailure(t, err, CauseMigrationLockHeld, CutoverUnknown); failure.Commit.Lock != InputMigrationLockHeldByOther {
		t.Fatalf("contender's lock state: %q", failure.Commit.Lock)
	}
	if _, err := os.Stat(otherBackup); !os.IsNotExist(err) {
		t.Fatalf("refused contender began backup: %v", err)
	}
	stillOwned, err := os.Stat(lockPath)
	if err != nil || !os.SameFile(owner, stillOwned) {
		t.Fatalf("refused contender replaced/unlinked owner inode: %v", err)
	}
	if !reflect.DeepEqual(before, dbDump(t, f.path)) {
		t.Fatal("held administrator boundary changed authority")
	}
	close(resume)
	result := <-done
	if result.err != nil || result.result.Authority != "sqlite" {
		t.Fatalf("owning administrator failed: %+v", result)
	}
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("successful administrator left lock behind: %v", err)
	}
	current, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	if err := acquireInputMigrationLock(current); err != nil {
		t.Fatalf("new pathname owner could not acquire: %v", err)
	}
	currentInfo, err := current.Stat()
	if err != nil || os.SameFile(owner, currentInfo) {
		t.Fatalf("test did not establish a new lock inode: %v", err)
	}
	if err := acquireInputMigrationLock(stale); err == nil || err.Error() != "journal migration lock changed; restart administrator verification" {
		t.Fatalf("obsolete descriptor admitted beside new owner: %v", err)
	}
	if err := stale.Close(); err != nil {
		t.Fatal(err)
	}
	stillOwned, err = os.Stat(lockPath)
	if err != nil || !os.SameFile(currentInfo, stillOwned) {
		t.Fatalf("stale descriptor refusal disturbed new owner: %v", err)
	}
	// End this directly-held fixture owner before the next real admin call.
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
	if err := current.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.migrate(t, nil); err != nil {
		t.Fatalf("later independent administrator refused: %v", err)
	}
	after, err := os.Stat(f.dir + ".lock")
	if err != nil || !os.SameFile(legacy, after) {
		t.Fatalf("migration replaced/unlinked the legacy launch inode: %v", err)
	}
}

func TestInputJournalMigrationPreservesUnexpectedLockContent(t *testing.T) {
	f := newJournalMigrationFixture(t)
	lockPath := f.path + ".input-migration.lock"
	want := []byte("operator evidence: do not discard\n")
	if err := os.WriteFile(lockPath, want, 0640); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := os.Stat(f.dir + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	data := dbDump(t, f.path)
	backup := filepath.Join(t.TempDir(), "backup")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, err := MigrateInputJournal(ctx, f.path, []string{f.root}, backup, func(context.Context, string, []string) error { return nil })
	if err == nil || err.Error() != "journal migration lock is not an empty regular file; restart administrator verification" || r.Authority != "" {
		t.Fatalf("unexpected lock content did not refuse safely: %+v %v", r, err)
	}
	if failure := migrationFailure(t, err, CauseMigrationLockUnestablished, CutoverUnknown); failure.Commit.Lock != InputMigrationLockUnreadable {
		t.Fatalf("unexpected content's lock state: %q", failure.Commit.Lock)
	}
	got, err := os.ReadFile(lockPath)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("refusal lost unexpected content: %q %v", got, err)
	}
	after, err := os.Stat(lockPath)
	if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
		t.Fatalf("refusal replaced or changed unexpected lock: %v", err)
	}
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Fatalf("unexpected lock began backup: %v", err)
	}
	if !reflect.DeepEqual(data, dbDump(t, f.path)) {
		t.Fatal("unexpected lock refusal changed schema/data")
	}
	afterLegacy, err := os.Stat(f.dir + ".lock")
	if err != nil || !os.SameFile(legacy, afterLegacy) {
		t.Fatalf("unexpected lock refusal disturbed legacy inode: %v", err)
	}
}

// Ordinary refinement of migration_retained row 12: all seven fault classes
// finish or refuse in one bounded call. This is not a temporal-model result.
func TestInputJournalMigrationSevenClasses(t *testing.T) {
	for _, tc := range []struct {
		name, blocker string
		refused       bool
	}{
		{"valid", "", false}, {"ambiguous", "unresolved_life", false},
		{"corrupt", "corrupt_current", false}, {"unreadable", "", true},
		{"conflict", "conflicting_process", false}, {"bad_proof", "invalid_attempt_or_proof", false},
		{"wrong_identity", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newJournalMigrationFixture(t)
			switch tc.name {
			case "ambiguous":
				withRaw(t, f.path, func(db *sql.DB) { mustExec(t, db, "DELETE FROM spawns") })
			case "corrupt":
				if err := os.WriteFile(filepath.Join(f.dir, "attempt.json"), []byte("{broken"), 0600); err != nil {
					t.Fatal(err)
				}
			case "unreadable":
				if err := os.Chmod(filepath.Join(f.dir, "attempt.json"), 0000); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(filepath.Join(f.dir, "attempt.json"), 0600) })
			case "conflict":
				withRaw(t, f.path, func(db *sql.DB) { mustExec(t, db, "UPDATE spawns SET pane_pid=pane_pid+1") })
			case "bad_proof":
				f.attempt.Proof.RawSHA256 = strings.Repeat("0", 64)
				f.write(t, "attempt.json", f.attempt)
			case "wrong_identity":
				f.attempt.Identity.StoreID = strings.Repeat("b", 16)
				f.write(t, "attempt.json", f.attempt)
			}
			before := dbDump(t, f.path)
			lock, err := os.Stat(f.dir + ".lock")
			if err != nil {
				t.Fatal(err)
			}
			transcript, err := os.ReadFile(f.cap.Transcript)
			if err != nil {
				t.Fatal(err)
			}
			r, err := f.migrate(t, nil)
			if tc.refused {
				if err == nil || r.Authority != "" {
					t.Fatalf("unsafe cutover: %+v, %v", r, err)
				}
				if !reflect.DeepEqual(before, dbDump(t, f.path)) {
					t.Fatal("refusal changed schema/data")
				}
				// Clear only the cause and restart verification, preserving authorization.
				if tc.name == "unreadable" {
					if err := os.Chmod(filepath.Join(f.dir, "attempt.json"), 0600); err != nil {
						t.Fatal(err)
					}
				} else {
					f.attempt.Identity.StoreID = f.id.StoreID
					f.write(t, "attempt.json", f.attempt)
				}
				r, err = f.migrate(t, nil)
			}
			if err != nil || r.Authority != "sqlite" {
				t.Fatalf("cutover: %+v, %v", r, err)
			}
			afterLock, err := os.Stat(f.dir + ".lock")
			if err != nil || !os.SameFile(lock, afterLock) {
				t.Fatal("lock inode replaced")
			}
			if got, err := os.ReadFile(f.cap.Transcript); err != nil || !bytes.Equal(got, transcript) {
				t.Fatal("provider evidence changed")
			}
			s, err := Open(f.path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			l, err := s.ReadInputLaunch(context.Background(), f.id.Token)
			if err != nil || l == nil || l.Blocker != tc.blocker {
				t.Fatalf("launch: %+v, %v; want blocker %q", l, err, tc.blocker)
			}
			if tc.blocker != "" {
				if l.CurrentRequestID != "" {
					t.Fatal("blocker gained current request")
				}
				var evidence []byte
				if err := s.db.QueryRow("SELECT evidence FROM input_imports").Scan(&evidence); err != nil {
					t.Fatal(err)
				}
				var files map[string][]byte
				if err := json.Unmarshal(evidence, &files); err != nil {
					t.Fatal(err)
				}
				if len(files["attempt.json"]) == 0 {
					t.Fatal("blocker lost source evidence")
				}
			} else {
				a, err := s.ReadInputAttempt(context.Background(), *l, "current")
				if err != nil || a == nil || a.Proof == nil || !bytes.Equal(a.Proof.Raw, f.attempt.Proof.Raw) || a.Identity != f.id {
					t.Fatalf("exact imported proof/identity: %+v, %v", a, err)
				}
			}
		})
	}
}

func TestInputJournalMigrationCurrentPrecedenceAndRecovery(t *testing.T) {
	f := newJournalMigrationFixture(t)
	nested := filepath.Join(f.dir, "diagnostics", "sessions", "retained", "provider.log")
	if err := os.MkdirAll(filepath.Dir(nested), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nested, []byte("nested provider evidence\x00\xff"), 0600); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(f.dir, "diagnostics", "empty", "nested")
	if err := os.MkdirAll(empty, 0700); err != nil {
		t.Fatal(err)
	}
	// Current wins even over malformed older history for that same request.
	if err := os.WriteFile(filepath.Join(f.dir, "request-"+inputdelivery.Hash([]byte("current"))+".json"), []byte("{older"), 0600); err != nil {
		t.Fatal(err)
	}
	old := *f.attempt
	old.RequestID = "older"
	old.Phase = "not_started"
	old.Proof = nil
	old.BytesAttempted = 0
	old.EnterAttempted = false
	f.write(t, "request-"+inputdelivery.Hash([]byte(old.RequestID))+".json", &old)
	before := dbDump(t, f.path)
	checks := 0
	r, err := f.migrate(t, func(context.Context, string, []string) error {
		checks++
		// The third boundary is after committed authority verification.
		if checks == 3 {
			return errors.New("inventory lost after commit")
		}
		return nil
	})
	if err == nil || r.Authority != "sqlite" || !strings.Contains(err.Error(), "SQLite cutover committed; keep the maintenance boundary and restart administrator verification") {
		t.Fatalf("postcommit uncertainty: %+v %v", r, err)
	}
	migrationFailure(t, err, CauseCommittedVerificationIncomplete, CutoverCommitted)
	if _, err := os.Stat(filepath.Join(r.Backup, "COMPLETE")); err != nil {
		t.Fatal(err)
	}
	for _, original := range []string{nested, empty} {
		rel, err := filepath.Rel(f.root, original)
		if err != nil {
			t.Fatal(err)
		}
		copied := filepath.Join(r.Backup, "legacy-0", rel)
		info, err := os.Stat(copied)
		if err != nil {
			t.Fatal(err)
		}
		if original == empty {
			if !info.IsDir() {
				t.Fatal("empty nested directory missing")
			}
			continue
		}
		want, err := os.ReadFile(original)
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(copied)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("nested evidence not preserved: %v", err)
		}
	}
	// Inspect a copy: even a read through SQLite can checkpoint/remove sidecars.
	// Keep the stopped-writer backup untouched for the literal restore below.
	inspection := filepath.Join(t.TempDir(), "state.db")
	backupDB := filepath.Join(r.Backup, "database")
	for p, b := range readDBFileSet(t, backupDB) {
		if err := os.WriteFile(inspection+strings.TrimPrefix(p, backupDB), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(before, dbDump(t, inspection)) {
		t.Fatal("backup changed pre-upgrade data")
	}
	s, err := Open(f.path)
	if err != nil {
		t.Fatal(err)
	}
	l, err := s.ReadInputLaunch(context.Background(), f.id.Token)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"current", "older"} {
		a, err := s.ReadInputAttempt(context.Background(), *l, id)
		if err != nil || a == nil || a.RequestID != id {
			t.Fatalf("history %s: %+v %v", id, a, err)
		}
	}
	// A later committed write must survive an admin retry over leftover files.
	_, err = s.db.Exec("UPDATE spawns SET labels='later-write' WHERE instance_id='pre'")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	f.write(t, "attempt.json", old)
	if r, err := f.migrate(t, nil); err != nil || r.Authority != "sqlite" || !r.RetirementPending {
		t.Fatalf("retry: %+v %v", r, err)
	}
	s, err = Open(f.path)
	if err != nil {
		t.Fatal(err)
	}
	l, err = s.ReadInputLaunch(context.Background(), f.id.Token)
	if err != nil || l.CurrentRequestID != "current" {
		t.Fatalf("retry overwrote current: %+v %v", l, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// Restore only: an isolated copy of the stopped-writer backup is old
	// authority again. Later writes disappear; no downgrade journal is made.
	restored := filepath.Join(t.TempDir(), "state.db")
	for _, suffix := range []string{"", "-wal", "-shm"} {
		b, e := os.ReadFile(filepath.Join(r.Backup, "database"+suffix))
		if os.IsNotExist(e) && suffix != "" {
			continue
		}
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(restored+suffix, b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	if !reflect.DeepEqual(before, dbDump(t, restored)) {
		t.Fatal("restore failed to recover prior state / lose later writes")
	}
	if _, err := Open(restored); !errors.Is(err, ErrSchemaMigrationRequired) {
		t.Fatalf("restored old store must require administrator again: %v", err)
	}
}

func TestInputJournalMigrationBackupFailurePreventsCutover(t *testing.T) {
	for fault, cause := range map[string]string{"unreadable_nested_file": CauseBackupFailed, "nonregular_nested_file": CauseBackupFailed, "existing_destination": CauseBackupExists, "cancelled": ""} {
		t.Run(fault, func(t *testing.T) {
			f := newJournalMigrationFixture(t)
			nested := filepath.Join(f.dir, "logs", "deep", "record")
			if err := os.MkdirAll(filepath.Dir(nested), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(nested, []byte("retained evidence"), 0600); err != nil {
				t.Fatal(err)
			}
			backup := filepath.Join(t.TempDir(), "backup")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch fault {
			case "unreadable_nested_file":
				if err := os.Chmod(nested, 0000); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(nested, 0600) })
			case "nonregular_nested_file":
				if err := os.Symlink(nested, filepath.Join(filepath.Dir(nested), "link")); err != nil {
					t.Fatal(err)
				}
			case "existing_destination":
				if err := os.Mkdir(backup, 0700); err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				cancel()
			}
			before := dbDump(t, f.path)
			r, err := MigrateInputJournal(ctx, f.path, []string{f.root}, backup, func(context.Context, string, []string) error { return nil })
			if err == nil || r.Authority != "" {
				t.Fatalf("failed backup authorized cutover: %+v %v", r, err)
			}
			if fault == "cancelled" {
				// The interrupt, not the stage's cause, names a cancelled attempt.
				var failure *InputMigrationError
				if !errors.As(err, &failure) || !failure.Commit.Interrupted || failure.Commit.Cutover() != CutoverNotCommitted {
					t.Fatalf("cancelled attempt: %+v", failure)
				}
				// The checkpoint after the lock sees the cancellation itself,
				// so no backup starts.
				if _, err := os.Lstat(backup); !os.IsNotExist(err) {
					t.Fatalf("cancelled attempt began a backup: %v", err)
				}
			} else {
				migrationFailure(t, err, cause, CutoverNotCommitted)
			}
			if _, err := os.Lstat(f.path + ".input-migration.lock"); !os.IsNotExist(err) {
				t.Fatalf("backup refusal left a per-store migration lock: %v", err)
			}
			if !reflect.DeepEqual(before, dbDump(t, f.path)) {
				t.Fatal("backup failure changed SQLite authority")
			}
			if _, err := os.Stat(filepath.Join(backup, "COMPLETE")); !os.IsNotExist(err) {
				t.Fatalf("failed backup marked COMPLETE: %v", err)
			}
			if _, err := os.Stat(filepath.Join(f.dir, "attempt.json")); err != nil {
				t.Fatal("backup failure retired input evidence")
			}
		})
	}
}

// verifierFailure is a verifier's own error type, which callers find through
// the attempt's *InputMigrationError.
type verifierFailure struct{}

func (*verifierFailure) Error() string { return "verifier refused the boundary" }

// SR-2.13 validate-first: the verifier, a missing authorization, a newer
// store, an unreadable version and a lock that cannot be taken refuse before
// any backup, and leave no lock file of theirs and no private snapshot
// directory (TMPDIR stays empty).
func TestInputJournalMigrationRefusesBeforeBackup(t *testing.T) {
	for _, tc := range []struct {
		name, cause string
		cutover     string // not_committed when empty
		keptLock    bool   // a lock file there before stays as it was
		damage      func(t *testing.T, f *journalMigrationFixture)
		path        func(f *journalMigrationFixture) string // the store path given; f.path when nil
		verify      func(context.Context, string, []string) error
		check       func(t *testing.T, f *journalMigrationFixture, err error)
	}{
		{name: "verifier_refusal", verify: func(context.Context, string, []string) error { return &verifierFailure{} },
			check: func(t *testing.T, _ *journalMigrationFixture, err error) {
				var own *verifierFailure
				if !errors.As(err, &own) || err.Error() != "verifier refused the boundary" {
					t.Fatalf("verifier's own error lost: %v", err)
				}
			}},
		{name: "authorization_missing", cause: CauseAuthorizationMissing,
			damage: func(t *testing.T, f *journalMigrationFixture) {
				if err := os.Remove(filepath.Join(filepath.Dir(f.path), sentinelFilename)); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, f *journalMigrationFixture, err error) {
				_, openErr := Open(f.path)
				if !errors.Is(err, ErrSchemaMigrationRequired) || openErr == nil || err.Error() != openErr.Error() {
					t.Fatalf("authorization refusal %v; the open gives %v", err, openErr)
				}
			}},
		{name: "store_newer_than_release", cause: CauseStoreNewerThanRelease,
			damage: func(t *testing.T, f *journalMigrationFixture) { stampUserVersion(t, f.path, schemaVersion+1) },
			check: func(t *testing.T, _ *journalMigrationFixture, err error) {
				if want := fmt.Sprintf("%v: found user_version=%d, want %d", ErrSchemaMismatch, schemaVersion+1, schemaVersion); !errors.Is(err, ErrSchemaMismatch) || err.Error() != want {
					t.Fatalf("newer store: %v; want %s", err, want)
				}
			}},
		{name: "store_version_unreadable", cause: CauseStoreVersionUnreadable,
			damage: func(t *testing.T, f *journalMigrationFixture) {
				for _, suffix := range []string{"-wal", "-shm"} {
					if err := os.Remove(f.path + suffix); err != nil && !os.IsNotExist(err) {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(f.path, []byte("not a database\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}},
		// R2-2/R2-3: a lock that cannot be created in the store's directory is
		// store_open_failed; an existing lock file that cannot be opened keeps
		// the lock's reason; a store path through a regular file is absent.
		{name: "store_directory_not_writable", cause: CauseStoreOpenFailed,
			damage: func(t *testing.T, f *journalMigrationFixture) {
				t.Cleanup(func() { _ = os.Chmod(filepath.Dir(f.path), 0700) })
				if err := os.Chmod(filepath.Dir(f.path), 0500); err != nil {
					t.Fatal(err)
				}
			}},
		{name: "lock_file_unopenable", cause: CauseMigrationLockUnestablished, cutover: CutoverUnknown, keptLock: true,
			damage: func(t *testing.T, f *journalMigrationFixture) {
				if err := os.WriteFile(f.path+".input-migration.lock", nil, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(f.path+".input-migration.lock", 0); err != nil {
					t.Fatal(err)
				}
			}},
		{name: "store_path_through_a_regular_file", cause: CauseStoreOpenFailed,
			path: func(f *journalMigrationFixture) string { return filepath.Join(f.path, "state.db") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newJournalMigrationFixture(t)
			if tc.damage != nil {
				tc.damage(t, f)
			}
			if tc.verify == nil {
				tc.verify = func(context.Context, string, []string) error { return nil }
			}
			path, cutover := f.path, CutoverNotCommitted
			if tc.path != nil {
				path = tc.path(f)
			}
			if tc.cutover != "" {
				cutover = tc.cutover
			}
			lock := f.path + ".input-migration.lock"
			heldBefore, _ := os.Lstat(lock)
			backup := filepath.Join(t.TempDir(), "backup")
			before := snapshotDBBytes(t, f.path)
			tmp := t.TempDir()
			t.Setenv("TMPDIR", tmp)
			r, err := MigrateInputJournal(context.Background(), path, []string{f.root}, backup, tc.verify)
			if err == nil || r.Authority != "" {
				t.Fatalf("refusal migrated: %+v %v", r, err)
			}
			if failure := migrationFailure(t, err, tc.cause, cutover); failure.Commit.StepsCommitted {
				t.Fatal("a refusal before the backup committed a step")
			}
			if _, err := os.Lstat(backup); !os.IsNotExist(err) {
				t.Fatalf("refusal made a backup directory: %v", err)
			}
			if entries, err := os.ReadDir(tmp); err != nil || len(entries) != 0 {
				t.Fatalf("refusal left the private snapshot behind: %v %v", entries, err)
			}
			if after, err := os.Lstat(lock); tc.keptLock {
				if err != nil || !os.SameFile(heldBefore, after) || after.Mode() != heldBefore.Mode() {
					t.Fatalf("refusal changed the existing lock file: %v", err)
				}
			} else if !os.IsNotExist(err) {
				t.Fatalf("refusal left a migration lock: %v", err)
			}
			assertDBBytesUnchanged(t, f.path, before)
			if tc.check != nil {
				tc.check(t, f, err)
			}
		})
	}
}

// The lock check never creates, changes or removes the lock file, and tells
// a free lock from one another administrator holds and one it cannot read.
func TestInputMigrationLockHolder(t *testing.T) {
	lockFile := func(content []byte) func(*testing.T, string) {
		return func(t *testing.T, lock string) {
			if err := os.WriteFile(lock, content, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, lock string)
		want  InputMigrationLockState
	}{
		{"absent", func(*testing.T, string) {}, InputMigrationLockFree},
		{"empty_unheld", lockFile(nil), InputMigrationLockFree},
		{"held_by_other", func(t *testing.T, lock string) {
			lockFile(nil)(t, lock)
			holder, err := os.Open(lock)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = holder.Close() })
			if err := unix.Flock(int(holder.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
				t.Fatal(err)
			}
		}, InputMigrationLockHeldByOther},
		{"non_empty", lockFile([]byte("operator evidence\n")), InputMigrationLockUnreadable},
		{"directory", func(t *testing.T, lock string) {
			if err := os.Mkdir(lock, 0700); err != nil {
				t.Fatal(err)
			}
		}, InputMigrationLockUnreadable},
		{"symlink", func(t *testing.T, lock string) {
			lockFile(nil)(t, lock+".target")
			if err := os.Symlink(lock+".target", lock); err != nil {
				t.Fatal(err)
			}
		}, InputMigrationLockUnreadable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := filepath.Join(t.TempDir(), "state.db")
			lock := db + ".input-migration.lock"
			tc.setup(t, lock)
			state := func() (os.FileInfo, []byte) {
				info, err := os.Lstat(lock)
				if os.IsNotExist(err) {
					return nil, nil
				}
				if err != nil {
					t.Fatal(err)
				}
				var content []byte
				if info.Mode().IsRegular() {
					if content, err = os.ReadFile(lock); err != nil {
						t.Fatal(err)
					}
				}
				return info, content
			}
			before, content := state()
			if got := InputMigrationLockHolder(db); got != tc.want {
				t.Fatalf("lock holder %q; want %q", got, tc.want)
			}
			after, afterContent := state()
			if (before == nil) != (after == nil) {
				t.Fatalf("lock check created or removed the lock file: before %v after %v", before, after)
			}
			if before != nil && (!os.SameFile(before, after) || before.Mode() != after.Mode() || !bytes.Equal(content, afterContent)) {
				t.Fatal("lock check changed the lock file")
			}
		})
	}
}

// Every Cause* constant of this package is in InputMigrationCauses, the list
// the administrator checks for an action, so a new cause cannot miss one.
func TestInputMigrationCausesComplete(t *testing.T) {
	listed := map[string]bool{}
	for _, cause := range InputMigrationCauses {
		listed[cause] = true
	}
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset, found := token.NewFileSet(), 0
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			if gen, ok := decl.(*ast.GenDecl); ok && gen.Tok == token.CONST {
				for _, spec := range gen.Specs {
					values := spec.(*ast.ValueSpec)
					for i, id := range values.Names {
						if !strings.HasPrefix(id.Name, "Cause") {
							continue
						}
						found++
						var value string
						if i < len(values.Values) {
							if lit, ok := values.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
								value, _ = strconv.Unquote(lit.Value)
							}
						}
						if !listed[value] {
							t.Errorf("%s (%s) is not in InputMigrationCauses", id.Name, fset.Position(id.Pos()))
						}
					}
				}
			}
		}
	}
	if found != len(InputMigrationCauses) {
		t.Errorf("%d Cause constants, %d listed causes", found, len(InputMigrationCauses))
	}
}

// 3j E3: a caller's context cancelled just before the cutover gate, with no
// wait for the AfterFunc that also sees it, always rolls the cutover back,
// because the gate reads the caller's context under its mutex. Under -race.
func TestInputJournalMigrationGateSeesCallerCancellation(t *testing.T) {
	var cancelCaller context.CancelFunc
	t.Cleanup(SetInputMigrationFaultsForTest(InputMigrationFaults{BeforeCutoverCommit: func() { cancelCaller() }}))
	// Without that read the AfterFunc loses nearly every time, so ten
	// iterations catch it.
	for i := 0; i < 10; i++ {
		f := newJournalMigrationFixture(t)
		before := dbDump(t, f.path)
		var ctx context.Context
		ctx, cancelCaller = context.WithCancel(context.Background())
		_, err := MigrateInputJournal(ctx, f.path, []string{f.root}, filepath.Join(t.TempDir(), "backup"), func(context.Context, string, []string) error { return nil })
		cancelCaller()
		var failure *InputMigrationError
		if !errors.As(err, &failure) || !failure.Commit.Interrupted || failure.Commit.CutoverBegan || failure.Commit.Cutover() != CutoverNotCommitted {
			t.Fatalf("iteration %d: cancellation before the gate did not interrupt: %+v %v", i, failure, err)
		}
		if !reflect.DeepEqual(before, dbDump(t, f.path)) {
			t.Fatalf("iteration %d: the cutover was not rolled back", i)
		}
		if _, err := os.Lstat(f.path + ".input-migration.lock"); !os.IsNotExist(err) {
			t.Fatalf("iteration %d: interrupted attempt left its lock: %v", i, err)
		}
	}
}

func TestInputJournalMigrationRetirementFailureKeepsAuthority(t *testing.T) {
	f := newJournalMigrationFixture(t)
	checks := 0
	r, err := f.migrate(t, func(context.Context, string, []string) error {
		checks++
		if checks == 3 {
			return os.Chmod(f.dir, 0500)
		}
		return nil
	})
	t.Cleanup(func() { _ = os.Chmod(f.dir, 0700) })
	if err != nil || r.Authority != "sqlite" || !r.RetirementPending {
		t.Fatalf("retirement failure: %+v %v", r, err)
	}
	if _, err := os.Stat(filepath.Join(f.dir, "attempt.json")); err != nil {
		t.Fatal("failed retirement lost original evidence")
	}
	s, err := Open(f.path)
	if err != nil {
		t.Fatal(err)
	}
	l, err := s.ReadInputLaunch(context.Background(), f.id.Token)
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.ReadInputAttempt(context.Background(), *l, "current")
	if err != nil || a == nil || a.Proof == nil || !bytes.Equal(a.Proof.Raw, f.attempt.Proof.Raw) {
		t.Fatalf("retirement revoked authority: %+v %v", a, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(f.dir, 0700); err != nil {
		t.Fatal(err)
	}
	// Retry cannot make leftover evidence authoritative even after it changes.
	if err := os.WriteFile(filepath.Join(f.dir, "attempt.json"), []byte("{now corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if retry, err := f.migrate(t, nil); err != nil || retry.Authority != "sqlite" || !retry.RetirementPending {
		t.Fatalf("retirement restart: %+v %v", retry, err)
	}
}

func TestInputJournalMigrationBackupIncludesWALAndSHM(t *testing.T) {
	f := newJournalMigrationFixture(t)
	db := openRaw(t, f.path)
	mustExec(t, db, "PRAGMA wal_autocheckpoint=0")
	mustExec(t, db, `UPDATE spawns SET labels='{"backup":"wal-only"}' WHERE instance_id='pre'`)
	files := map[string][]byte{}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		b, err := os.ReadFile(f.path + suffix)
		if err != nil {
			t.Fatal(err)
		}
		files[suffix] = b
	}
	r, err := f.migrate(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	for suffix, want := range files {
		got, err := os.ReadFile(filepath.Join(r.Backup, "database"+suffix))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("stopped-writer backup bytes %q changed: %v", suffix, err)
		}
	}
	restored := filepath.Join(t.TempDir(), "state.db")
	for suffix, b := range files {
		if err := os.WriteFile(restored+suffix, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	withRaw(t, restored, func(db *sql.DB) {
		var labels string
		if err := db.QueryRow("SELECT labels FROM spawns WHERE instance_id='pre'").Scan(&labels); err != nil || labels != `{"backup":"wal-only"}` {
			t.Fatalf("restored WAL content: %q %v", labels, err)
		}
	})
}

func TestInputJournalMigrationSentinelCannotAuthorizeRuntime(t *testing.T) {
	// The journal cutover is v16. Later schema-only hops may consume their
	// own sentinel, but cannot authorize importing a pre-v16 journal.
	for _, from := range []int{1, 15} {
		path := makeVersionedDB(t, t.TempDir(), from)
		writeSentinel(t, filepath.Dir(path), from, schemaVersion)
		before := snapshotDBBytes(t, path)
		if _, err := Open(path); !errors.Is(err, ErrSchemaMigrationRequired) {
			t.Fatalf("runtime consumed old sentinel: %v", err)
		}
		assertDBBytesUnchanged(t, path, before)
		assertSentinel(t, filepath.Dir(path), true)
	}
}

func TestInputJournalMigrationCrashProcess(t *testing.T) {
	if path := os.Getenv("AD_TEST_JOURNAL_CRASH_PATH"); path != "" {
		checks := 0
		_, _ = MigrateInputJournal(context.Background(), path, []string{os.Getenv("AD_TEST_JOURNAL_ROOT")}, os.Getenv("AD_TEST_JOURNAL_BACKUP"), func(context.Context, string, []string) error {
			checks++
			if checks == 2 && os.Getenv("AD_TEST_JOURNAL_CRASH_SIDE") == "before" || checks == 3 && os.Getenv("AD_TEST_JOURNAL_CRASH_SIDE") == "after" {
				os.Exit(77)
			}
			return nil
		})
		t.Fatal("crash boundary was not reached")
	}
	for _, side := range []string{"before", "after"} {
		t.Run(side, func(t *testing.T) {
			f := newJournalMigrationFixture(t)
			before := dbDump(t, f.path)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestInputJournalMigrationCrashProcess$")
			cmd.Env = append(os.Environ(), "AD_TEST_JOURNAL_CRASH_PATH="+f.path, "AD_TEST_JOURNAL_ROOT="+f.root, "AD_TEST_JOURNAL_BACKUP="+filepath.Join(t.TempDir(), "backup"), "AD_TEST_JOURNAL_CRASH_SIDE="+side)
			out, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 77 {
				t.Fatalf("crash process: %v %s", err, out)
			}
			if side == "before" {
				if !reflect.DeepEqual(before, dbDump(t, f.path)) {
					t.Fatal("precommit crash changed authority")
				}
			} else {
				s, err := Open(f.path)
				if err != nil {
					t.Fatal(err)
				}
				l, err := s.ReadInputLaunch(context.Background(), f.id.Token)
				if err != nil {
					t.Fatal(err)
				}
				a, err := s.ReadInputAttempt(context.Background(), *l, "current")
				if err != nil || a == nil || a.Proof == nil || !bytes.Equal(a.Proof.Raw, f.attempt.Proof.Raw) {
					t.Fatalf("crash lost exact committed proof: %+v %v", a, err)
				}
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if r, err := f.migrate(t, nil); err != nil || r.Authority != "sqlite" {
				t.Fatalf("crash restart: %+v %v", r, err)
			}
		})
	}
}
