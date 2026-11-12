//go:build linux

package adminapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/journalfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
)

func TestAdviceFollow_G15_DarwinJournalEvidence(t *testing.T) {
	// Serial: the verifier, attempt timeout and HOME are process-wide seams.
	const retry = "keep the maintenance window, then re-run this release's installer with a new --journal-backup, which finishes any pending schema step, then run migrate-journal with a new --backup-dir until it exits 0 with authority \"sqlite\""
	for _, tc := range []struct{ kind, reason, action string }{
		{"privacy", "old_or_unverified_mutators", "stop each listed agent_director_mutator (an agent-director program or a process holding the store or a file under a legacy root); each may run again once the cutover is verified; " + retry},
		{"mount", "process_inventory_unreadable", "run it as the agents' user with every mounted file system reachable or unmounted (on Linux, with the proc file system mounted and readable), " + retry + "; report it if it persists"},
		{"consent", "inventory_deadline", "make every mounted file system reachable or unmount it, or answer any privacy consent prompt, " + retry},
		{"acl", "legacy_root_access_unestablished", "make the store and every legacy root owned by the user running the migration and private to it (chmod 0600 the store, chmod 0700 each root, and remove any access-control entry, for example chmod -N <path>), " + retry},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			h := newDarwinTestHost(t)
			dir := t.TempDir()
			t.Setenv("HOME", dir)
			h.db = journalfix.PreCutoverStore(t, dir, 15)
			journalfix.Authorize(t, h.db)
			fsys := &darwinTestFS{journalFS: osJournalFS{}}
			h.v.fs = fsys
			opts := JournalMigrationOptions{MaintenanceWindow: true, RootsComplete: true, LegacyRoots: []string{h.root}, BackupDir: filepath.Join(dir, "backup-refused")}
			var entered <-chan struct{}
			var follow func()
			switch tc.kind {
			case "privacy":
				// G15's retained row now follows "stop each listed mutator".
				h.processes[200].files = []lsofFile{h.file("4", h.db)}
				follow = func() { h.procs.Set(200, procfix.Gone()) }
			case "mount":
				// G15 "every mounted file system reachable or unmounted":
				// clear the system listing's inaccessible-mount warning.
				h.stderr = []byte("private mount unavailable")
				follow = func() { h.stderr = nil }
			case "consent":
				// G15 "answer any privacy consent prompt": release a bounded
				// kernel-path lookup, standing for answering the prompt.
				var release func()
				entered, release = blockDarwinLookup(t, fsys, "kernel", h.root)
				restore := store.SetInputMigrationFaultsForTest(store.InputMigrationFaults{AttemptTimeout: 2 * time.Second})
				t.Cleanup(restore)
				follow = func() { release(); fsys.before = nil; restore() }
			case "acl":
				// G15 "remove any access-control entry, for example chmod
				// -N <path>": the ACL seam drops the other user's allow.
				h.acl[h.root] = []byte("drwx------+ 1 owner staff 0 Nov 6 12:00 private-root\n 0: group:everyone allow read\n")
				follow = func() { delete(h.acl, h.root) }
			}
			t.Cleanup(SetJournalVerifierForTest(h.v.verify))
			before, err := os.ReadFile(h.db)
			if err != nil {
				t.Fatal(err)
			}
			_, err = MigrateJournal(context.Background(), h.db, opts)
			var r *JournalRefusal
			if !errors.As(err, &r) || r.Reason != tc.reason || r.Action != tc.action {
				t.Fatalf("refusal = %#v (%v); want %s, %q", r, err, tc.reason, tc.action)
			}
			if r.Commit.Cutover != "not_committed" || r.Commit.StepsCommitted == nil || *r.Commit.StepsCommitted {
				t.Fatalf("commit = %+v", r.Commit)
			}
			after, err := os.ReadFile(h.db)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("refusal changed store bytes")
			}
			for _, path := range []string{opts.BackupDir, h.db + ".input-migration.lock"} {
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatalf("refusal created %s: %v", path, err)
				}
			}
			if entered != nil {
				select {
				case <-entered:
				default:
					t.Fatal("deadline did not reach the consent prompt")
				}
			}
			follow()
			attempt := 0
			migrate := func() (store.InputMigrationResult, error) {
				attempt++
				opts.BackupDir = filepath.Join(dir, fmt.Sprintf("backup-follow-%d", attempt))
				return MigrateJournal(context.Background(), h.db, opts)
			}
			journalInstaller(t, h.db, migrate)
			if result, err := migrate(); err != nil || result.Authority != "sqlite" {
				t.Fatalf("followed Darwin advice: %+v, %v", result, err)
			}
		})
	}
}
