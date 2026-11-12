package adminapi

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
)

func TestDarwinJournalContainmentArguments(t *testing.T) {
	// Serial: each case selects the containment host and sets HOME.
	for _, kind := range []string{"equal", "case", "case-child", "absent-root", "unrelated", "backup-refused", "root-refused"} {
		t.Run(kind, func(t *testing.T) {
			h := newDarwinTestHost(t)
			base := filepath.Dir(h.root)
			home := filepath.Join(base, "home")
			if err := os.Mkdir(home, 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HOME", home)
			fsys := &darwinTestFS{journalFS: osJournalFS{}, fold: true}
			opts := JournalMigrationOptions{MaintenanceWindow: true, RootsComplete: true, LegacyRoots: []string{h.root}, BackupDir: filepath.Join(base, "backup")}
			reason := "backup_inside_legacy_root"
			switch kind {
			case "equal":
				opts.BackupDir = h.root
			case "case":
				opts.BackupDir = filepath.Join(base, "ROOT")
			case "case-child":
				opts.BackupDir = filepath.Join(base, "ROOT", "backup")
			case "absent-root":
				parent := filepath.Join(base, "parent")
				if err := os.Mkdir(parent, 0700); err != nil {
					t.Fatal(err)
				}
				opts.LegacyRoots = []string{filepath.Join(parent, "root")}
				opts.BackupDir = filepath.Join(base, "Parent", "ROOT", "new")
			case "unrelated":
				reason = ""
			case "backup-refused":
				reason = "backup_unreadable"
				fsys.before = func(op, p string) error {
					if op == "eval" && p == opts.BackupDir {
						return syscall.EACCES
					}
					return nil
				}
			case "root-refused":
				reason = "legacy_root_unreadable"
				fsys.before = func(op, p string) error {
					if op == "eval" && p == h.root {
						return syscall.EPERM
					}
					return nil
				}
			}
			previous := journalContainmentHost
			journalContainmentHost.byIdentity, journalContainmentHost.fs = true, fsys
			t.Cleanup(func() { journalContainmentHost = previous })
			// On a case-insensitive host, ROOT already names the real root.
			// Preserve existing roots; other backup paths must remain absent.
			backupBefore, err := os.Lstat(opts.BackupDir)
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			for _, call := range []string{"check-only", "migrate"} {
				path := filepath.Join(base, "not-created.db")
				var err error
				if call == "check-only" {
					err = CheckJournalMigration(opts)
				} else {
					if reason == "" {
						continue
					}
					_, err = MigrateJournal(context.Background(), path, opts)
				}
				r := assertDarwinRefusal(t, err, reason)
				if r != nil && (r.Commit.Cutover != "not_committed" || r.Commit.StepsCommitted == nil || *r.Commit.StepsCommitted) {
					t.Fatalf("%s commit = %+v", call, r.Commit)
				}
				for _, absent := range []string{path, path + ".input-migration.lock"} {
					if _, err := os.Lstat(absent); !os.IsNotExist(err) {
						t.Fatalf("%s created %s: %v", call, absent, err)
					}
				}
				backupAfter, err := os.Lstat(opts.BackupDir)
				if backupBefore == nil {
					if !os.IsNotExist(err) {
						t.Fatalf("%s created backup: %v", call, err)
					}
				} else if err != nil || !os.SameFile(backupBefore, backupAfter) || backupBefore.Mode() != backupAfter.Mode() || backupBefore.ModTime() != backupAfter.ModTime() {
					t.Fatalf("%s changed existing backup path: %v", call, err)
				}
			}
		})
	}
	if runtime.GOOS == "linux" {
		t.Run("linux-stays-lexical", func(t *testing.T) {
			h := newDarwinTestHost(t)
			t.Setenv("HOME", filepath.Dir(h.root))
			if journalContainmentHost.byIdentity {
				t.Fatal("Linux default selected identity containment")
			}
			opts := JournalMigrationOptions{MaintenanceWindow: true, RootsComplete: true, LegacyRoots: []string{h.root}, BackupDir: filepath.Join(filepath.Dir(h.root), "ROOT")}
			if err := CheckJournalMigration(opts); err != nil {
				t.Fatal(err)
			}
			// With Linux's actual FS (no folding), the case-variant parent
			// also has no identity relationship with the declared root.
			if inside, err := pathWithinByIdentity(osJournalFS{}, opts.BackupDir, h.root); err != nil || inside {
				t.Fatalf("negative control: %v, %v", inside, err)
			}
		})
	}
}

func TestDarwinJournalContainmentIdentityEdges(t *testing.T) {
	t.Parallel()
	h := newDarwinTestHost(t)
	for _, tc := range []struct {
		name, path, dir string
		inside          bool
	}{
		{"not-a-directory", filepath.Join(h.image, "ROOT", "new"), filepath.Join(h.image, "root"), true},
		{"different-absent-name", filepath.Join(h.root, "other", "new"), filepath.Join(h.root, "absent"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fsys := &darwinTestFS{journalFS: osJournalFS{}, fold: true}
			inside, err := pathWithinByIdentity(fsys, tc.path, tc.dir)
			if err != nil || inside != tc.inside {
				t.Fatalf("inside = %v, %v; want %v", inside, err, tc.inside)
			}
		})
	}
}
