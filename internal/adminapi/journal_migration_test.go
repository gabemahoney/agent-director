//go:build linux

package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/journalfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/sandboxguard"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"golang.org/x/sys/unix"
)

// TestMain's --journal-fixed-helper child mode is must-use for re-executing
// this test binary as a fixed-path helper. Extend its one behaviour switch;
// all other invocations retain the sandbox guard.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "--journal-fixed-helper" {
		os.Exit(journalFixedHelperChild(os.Args[2:]))
	}
	sandboxguard.Require()
	os.Exit(m.Run())
}

// TestJournalMigrationAuthorizationAndRoots: CheckJournalMigration and
// MigrateJournal make the same argument checks, in order, each refusal with
// its own reason and action (their wording is pinned by
// cmd/agent-director-admin's TestAdviceFollow_G11_CheckOnlyArguments); the
// check changes nothing on disk, and a refused run creates no store and no
// lock.
func TestJournalMigrationAuthorizationAndRoots(t *testing.T) {
	// Each case gets a fresh tree: home is its HOME, root a declared legacy
	// root (a directory), file a regular file, backup a new absolute path.
	type tree struct{ home, root, file, backup string }
	valid := func(d tree) JournalMigrationOptions {
		return JournalMigrationOptions{MaintenanceWindow: true, RootsComplete: true, LegacyRoots: []string{d.root}, BackupDir: d.backup}
	}
	with := func(edit func(*JournalMigrationOptions, tree)) func(tree) JournalMigrationOptions {
		return func(d tree) JournalMigrationOptions {
			o := valid(d)
			edit(&o, d)
			return o
		}
	}
	for _, tc := range []struct {
		name, reason string
		home         func(tree) string
		opts         func(tree) JournalMigrationOptions
	}{
		{"boundary", "maintenance_boundary_required", nil, func(tree) JournalMigrationOptions { return JournalMigrationOptions{} }},
		{"roots", "legacy_roots_not_established", nil, with(func(o *JournalMigrationOptions, _ tree) { o.RootsComplete = false })},
		{"roots before home", "legacy_roots_not_established", func(tree) string { return "" }, with(func(o *JournalMigrationOptions, _ tree) { o.RootsComplete = false })},
		{"home unset", "invoking_home_unavailable", func(tree) string { return "" }, valid},
		{"relative home before roots", "invoking_home_unavailable", func(tree) string { return "relative-home" }, with(func(o *JournalMigrationOptions, _ tree) { o.LegacyRoots = []string{"relative"} })},
		{"relative root", "legacy_root_not_absolute", nil, with(func(o *JournalMigrationOptions, _ tree) { o.LegacyRoots = []string{"relative"} })},
		{"root before backup", "legacy_root_not_absolute", nil, with(func(o *JournalMigrationOptions, _ tree) {
			o.LegacyRoots = []string{"relative"}
			o.BackupDir = ""
		})},
		{"trailing slash root", "legacy_root_not_clean", nil, with(func(o *JournalMigrationOptions, d tree) { o.LegacyRoots = []string{d.root + "/"} })},
		{"dotdot root", "legacy_root_not_clean", nil, with(func(o *JournalMigrationOptions, d tree) {
			o.LegacyRoots = []string{d.root + "/../" + filepath.Base(d.root)}
		})},
		{"no backup", "absolute_backup_required", nil, with(func(o *JournalMigrationOptions, _ tree) { o.BackupDir = "" })},
		{"relative backup", "absolute_backup_required", nil, with(func(o *JournalMigrationOptions, _ tree) { o.BackupDir = "backup" })},
		{"backup is a root", "backup_inside_legacy_root", nil, with(func(o *JournalMigrationOptions, d tree) { o.BackupDir = d.root })},
		{"backup under a root", "backup_inside_legacy_root", nil, with(func(o *JournalMigrationOptions, d tree) { o.BackupDir = filepath.Join(d.root, "backup") })},
		{"backup under invoking home's root", "backup_inside_legacy_root", nil, with(func(o *JournalMigrationOptions, d tree) {
			o.BackupDir = filepath.Join(d.home, ".agent-director", "input", "backup")
		})},
		{"backup exists", "backup_exists", nil, with(func(o *JournalMigrationOptions, d tree) { o.BackupDir = filepath.Dir(d.root) })},
		{"backup is a dangling link", "backup_exists", nil, with(func(o *JournalMigrationOptions, d tree) {
			if err := os.Symlink(filepath.Join(d.home, "nowhere"), d.backup); err != nil {
				t.Fatal(err)
			}
		})},
		{"backup parent a file", "backup_unreadable", nil, with(func(o *JournalMigrationOptions, d tree) { o.BackupDir = filepath.Join(d.file, "backup") })},
		// MI decision Q1 (Task t2.7tz.ab.1h): the backup's exclusive mkdir makes
		// only its last element.
		{"backup parent missing", "backup_unreadable", nil, with(func(o *JournalMigrationOptions, d tree) { o.BackupDir = filepath.Join(d.home, "missing", "backup") })},
		{"valid", "", nil, valid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			d := tree{home: filepath.Join(base, "home"), root: filepath.Join(base, "roots", "extra"), file: filepath.Join(base, "file"), backup: filepath.Join(base, "backup")}
			for _, dir := range []string{d.home, d.root} {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(d.file, []byte("file"), 0o600); err != nil {
				t.Fatal(err)
			}
			home := d.home
			if tc.home != nil {
				home = tc.home(d)
			}
			t.Setenv("HOME", home)
			opts := tc.opts(d)
			before := treeState(t, base)
			err := CheckJournalMigration(opts)
			if after := treeState(t, base); !maps.Equal(after, before) {
				t.Errorf("CheckJournalMigration changed the tree:\n before %v\n after  %v", before, after)
			}
			if tc.reason == "" {
				if err != nil {
					t.Fatalf("CheckJournalMigration = %v; want nil", err)
				}
				return
			}
			assertJournalArgumentRefusal(t, "CheckJournalMigration", err, tc.reason)
			assertJournalCommit(t, "CheckJournalMigration", err, store.CutoverNotCommitted, false)
			path := filepath.Join(base, "state.db")
			_, err = MigrateJournal(context.Background(), path, opts)
			assertJournalArgumentRefusal(t, "MigrateJournal", err, tc.reason)
			// No administrator holds the lock, which the check finds without
			// creating it.
			assertJournalCommit(t, "MigrateJournal", err, store.CutoverNotCommitted, false)
			// The tree holds the store's path, so this also shows no store and
			// no <store>.input-migration.lock was made.
			if after := treeState(t, base); !maps.Equal(after, before) {
				t.Errorf("refused MigrateJournal changed the tree:\n before %v\n after  %v", before, after)
			}
		})
	}
}

// assertJournalArgumentRefusal fails unless err is a JournalRefusal with
// reason and that reason's own action, naming no process: processes encodes
// as [], not null.
func assertJournalArgumentRefusal(t *testing.T, entry string, err error, reason string) {
	t.Helper()
	action, own := journalActions[reason]
	var r *JournalRefusal
	if !own || !errors.As(err, &r) || r.Reason != reason || r.Action != action {
		t.Errorf("%s = %#v (%v); want reason %s with its own action %q", entry, r, err, reason, action)
		return
	}
	if b, err := json.Marshal(r.Processes); err != nil || string(b) != "[]" {
		t.Errorf("%s processes encode as %s (%v); want []", entry, b, err)
	}
}

// assertJournalCommit fails unless err is a JournalRefusal whose commit state
// is cutover, with StepsCommitted equal to steps when not_committed and absent
// otherwise (SR-2.16 "Commit state").
func assertJournalCommit(t *testing.T, entry string, err error, cutover string, steps bool) {
	t.Helper()
	var r *JournalRefusal
	if !errors.As(err, &r) {
		t.Errorf("%s = %v; want a JournalRefusal", entry, err)
		return
	}
	c := r.Commit
	if c.Cutover != cutover || (cutover == store.CutoverNotCommitted) != (c.StepsCommitted != nil) || c.StepsCommitted != nil && *c.StepsCommitted != steps {
		t.Errorf("%s commit = %s, steps %v; want %s, steps %v", entry, c.Cutover, c.StepsCommitted, cutover, steps)
	}
}

// treeState returns every path under dir with its type, mode and contents
// (a link's target), so a test can show nothing under dir changed.
func treeState(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		state := info.Mode().String()
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			state += " -> " + target
		case info.Mode().IsRegular():
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			state += " " + string(body)
		}
		out[path] = state
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return out
}

// G9: stop each listed mutator, keep the maintenance window, then run the
// installer and migrate-journal with fresh backups until authority is sqlite.
// The non-dumpable unrelated process needs no action and stays alive throughout.
// Actual FD/start identities, no process mocks.
func TestAdviceFollow_G9_JournalInventory(t *testing.T) {
	for _, kind := range []string{"custom_database", "wal", "shm", "invoking_home_legacy_root", "untracked_mcp", "cursor_helper", "hook", "inflight_command", "launch_preparation", "unreadable_process"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			db := filepath.Join(root, "state.db")
			legacy := filepath.Join(root, "input")
			if err := os.Mkdir(legacy, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(db, []byte("private evidence"), 0600); err != nil {
				t.Fatal(err)
			}
			held := db
			switch kind {
			case "wal":
				held = db + "-wal"
			case "shm":
				held = db + "-shm"
			case "invoking_home_legacy_root":
				held = filepath.Join(legacy, "launch.json")
			}
			if err := os.WriteFile(held, []byte("private evidence"), 0600); err != nil {
				t.Fatal(err)
			}
			fd, err := os.Open(held)
			if err != nil {
				t.Fatal(err)
			}
			defer fd.Close()
			cat := "/bin/cat"
			if kind == "untracked_mcp" || kind == "cursor_helper" || kind == "hook" || kind == "inflight_command" || kind == "launch_preparation" {
				b, err := os.ReadFile(cat)
				if err != nil {
					t.Fatal(err)
				}
				cat = filepath.Join(root, "agent-director-"+kind)
				if err := os.WriteFile(cat, b, 0700); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, cat)
			role, action := "agent_director_mutator", "stop_and_restart_after_cutover"
			if cat == "/bin/cat" {
				cmd.ExtraFiles = []*os.File{fd}
			}
			if kind == "unreadable_process" {
				exe, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				cmd = exec.CommandContext(ctx, exe, "-test.run=^TestJournalInventoryUnreadableProcess$")
				cmd.Env = append(os.Environ(), "AD_JOURNAL_UNREADABLE_PROCESS=1")
			}
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = stdin.Close()
				if cmd.ProcessState == nil {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
			})
			if _, err := io.WriteString(stdin, "ready\n"); err != nil {
				t.Fatal(err)
			}
			ready := make([]byte, 6)
			if _, err := io.ReadFull(stdout, ready); err != nil {
				t.Fatal(err)
			}
			start, alive, known := probe.NewProcChecker().StartTime(cmd.Process.Pid)
			if !alive || !known {
				t.Fatal("child process identity unavailable")
			}
			if kind == "unreadable_process" {
				base := filepath.Join("/proc", strconv.Itoa(cmd.Process.Pid))
				if _, err := os.Readlink(filepath.Join(base, "exe")); !errors.Is(err, os.ErrPermission) {
					t.Fatalf("non-dumpable exe readable: %v", err)
				}
				if _, err := os.ReadDir(filepath.Join(base, "fd")); !errors.Is(err, os.ErrPermission) {
					t.Fatalf("non-dumpable descriptors readable: %v", err)
				}
			}
			for i := 0; i < 2; i++ {
				err := verifyJournalQuiescence(ctx, db, []string{legacy})
				if kind == "unreadable_process" {
					if err != nil {
						t.Fatalf("unrelated unreadable process blocked: %v", err)
					}
					continue
				}
				var r *JournalRefusal
				if !errors.As(err, &r) || r.Reason != "old_or_unverified_mutators" || r.Action != "stop each listed agent_director_mutator (an agent-director program or a process holding the store or a file under a legacy root); each may run again once the cutover is verified; keep the maintenance window, then re-run this release's installer with a new --journal-backup, which finishes any pending schema step, then run migrate-journal with a new --backup-dir until it exits 0 with authority \"sqlite\"" {
					t.Fatalf("inventory = %v", err)
				}
				found := false
				for _, p := range r.Processes {
					if p.PID == cmd.Process.Pid {
						found = true
						if p.Starttime != start || p.Role != role || p.Action != action {
							t.Fatalf("unsafe process identity: %+v", p)
						}
					}
				}
				if !found {
					t.Fatal("inventory omitted live writer")
				}
				wire, _ := json.Marshal(r)
				for _, secret := range []string{held, "private evidence"} {
					if strings.Contains(string(wire), secret) {
						t.Fatal("refusal leaked private path/data")
					}
				}
				if _, alive, known := probe.NewProcChecker().StartTime(cmd.Process.Pid); !alive || !known {
					t.Fatal("verification killed writer")
				}
			}
			// Stop only a listed mutator; the unrelated child stays alive.
			if kind != "unreadable_process" {
				if err := stdin.Close(); err != nil {
					t.Fatal(err)
				}
				if err := cmd.Wait(); err != nil {
					t.Fatal(err)
				}
			}
			home := filepath.Join(root, "home")
			if err := os.Mkdir(home, 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HOME", home)
			for _, p := range []string{db, db + "-wal", db + "-shm"} {
				if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
			}
			db = journalfix.PreCutoverStore(t, root, 15)
			backups := 0
			migrate := func() (store.InputMigrationResult, error) {
				backups++
				return MigrateJournal(ctx, db, JournalMigrationOptions{MaintenanceWindow: true, RootsComplete: true, LegacyRoots: []string{legacy}, BackupDir: filepath.Join(root, fmt.Sprintf("backup-%d", backups))})
			}
			journalInstaller(t, db, migrate)
			if r, err := migrate(); err != nil || r.Authority != "sqlite" {
				t.Fatalf("condition cleared but the migration still refused: %+v %v", r, err)
			}
			if kind == "unreadable_process" {
				if got, alive, known := probe.NewProcChecker().StartTime(cmd.Process.Pid); !alive || !known || got != start {
					t.Fatal("unrelated child did not stay alive through sqlite authority")
				}
			}
		})
	}
}

func TestJournalInventoryUnreadableProcess(t *testing.T) {
	if os.Getenv("AD_JOURNAL_UNREADABLE_PROCESS") != "1" {
		return
	}
	// Real Linux access denial, even for our UID: no invented /proc fixture.
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(os.Stdout, os.Stdin); err != nil {
		t.Fatal(err)
	}
}

func TestJournalInventoryPartialProcessEvidence(t *testing.T) {
	// Serial: journalProcRoot is the shared listing seam. Start identities
	// still come from the real, live parent process, not this temporary tree.
	for _, kind := range []string{"owner-unreadable", "unreadable-only", "program", "program-unlistable-fds", "store", "wal", "shm", "root"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			db, root := filepath.Join(dir, "store.db"), filepath.Join(dir, "root")
			if err := os.WriteFile(db, nil, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			previous := journalProcRoot
			journalProcRoot = t.TempDir()
			t.Cleanup(func() { journalProcRoot = previous })
			pid := os.Getppid()
			base := filepath.Join(journalProcRoot, strconv.Itoa(pid))
			if kind == "owner-unreadable" {
				// Reuse G12's self-link fixture: Stat cannot establish an owner.
				if err := os.Symlink(filepath.Base(base), base); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(base); !errors.Is(err, unix.ELOOP) {
					t.Fatalf("owner lookup = %v", err)
				}
				assertDarwinRefusal(t, verifyJournalQuiescence(context.Background(), db, []string{root}), "")
				return
			}
			start, alive, known := probe.NewProcChecker().StartTime(pid)
			if !known || !alive {
				t.Fatal("parent process identity unavailable")
			}
			fds := filepath.Join(base, "fd")
			if err := os.MkdirAll(fds, 0700); err != nil {
				t.Fatal(err)
			}
			// Non-links on either side of the readable entry exercise both
			// continuing after an unreadable link and retaining a finding.
			for _, name := range []string{"0", "9"} {
				if err := os.WriteFile(filepath.Join(fds, name), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if strings.HasPrefix(kind, "program") {
				if err := os.Symlink(filepath.Join(dir, "agent-director-helper"), filepath.Join(base, "exe")); err != nil {
					t.Fatal(err)
				}
				if kind == "program-unlistable-fds" {
					if err := os.Chmod(fds, 0); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = os.Chmod(fds, 0700) })
				}
			} else {
				if err := os.WriteFile(filepath.Join(base, "exe"), nil, 0600); err != nil {
					t.Fatal(err)
				}
				if kind != "unreadable-only" {
					target := db
					switch kind {
					case "wal":
						target += "-wal"
					case "shm":
						target += "-shm"
					case "root":
						target = filepath.Join(root, "held")
					}
					if err := os.Symlink(target, filepath.Join(fds, "5")); err != nil {
						t.Fatal(err)
					}
				}
			}
			err := verifyJournalQuiescence(context.Background(), db, []string{root})
			if kind == "unreadable-only" {
				assertDarwinRefusal(t, err, "")
			} else {
				assertDarwinRefusal(t, err, "old_or_unverified_mutators", JournalProcess{PID: pid, Starttime: start, Role: "agent_director_mutator", Action: "stop_and_restart_after_cutover"})
			}
		})
	}
}

func TestJournalMigrationRefusesUnestablishedPaths(t *testing.T) {
	for _, kind := range []string{"symlink", "public_root", "unreadable_parent", "through_a_regular_file"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "input")
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
			want := "legacy_root_access_unestablished"
			switch kind {
			case "symlink":
				link := filepath.Join(root, "linked")
				if err := os.Symlink(path, link); err != nil {
					t.Fatal(err)
				}
				path = link
				want = "legacy_root_symlink"
			case "public_root":
				if err := os.Chmod(path, 0755); err != nil {
					t.Fatal(err)
				}
			case "unreadable_parent":
				if err := os.Chmod(path, 0000); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(path, 0700) })
				path = filepath.Join(path, "missing")
				want = "legacy_root_unreadable"
			case "through_a_regular_file":
				// Nothing can exist under a file: the path is absent.
				file := filepath.Join(root, "file")
				if err := os.WriteFile(file, nil, 0600); err != nil {
					t.Fatal(err)
				}
				path, want = filepath.Join(file, "input"), ""
			}
			err := verifyJournalPath(path)
			if want == "" {
				if err != nil {
					t.Fatalf("absent path refused: %v", err)
				}
				return
			}
			var r *JournalRefusal
			if !errors.As(err, &r) || r.Reason != want {
				t.Fatalf("path refusal %v; want %s", err, want)
			}
		})
	}
}

// A path through a regular file is absent to the real verifier and to the
// attempt alike (the R2-2 sweep): a store path through one is refused
// store_open_failed, whose action points at the store, and a legacy root
// through one, or a file where the root's store-id directory belongs, holds
// no evidence, so the migration passes with no retirement pending and the
// backup keeping that file.
func TestMigrateJournalPathsThroughAFile(t *testing.T) {
	const evidence = "not launch evidence"
	for _, tc := range []struct {
		name string
		// lay returns the store path and the declared legacy root.
		lay func(t *testing.T, dir string) (db, root string)
	}{
		{"store_path", func(t *testing.T, dir string) (string, string) {
			return filepath.Join(dir, "file", "state.db"), filepath.Join(dir, "roots", "extra")
		}},
		{"legacy_root", func(t *testing.T, dir string) (string, string) {
			return journalfix.PreCutoverStore(t, filepath.Join(dir, "store"), 15), filepath.Join(dir, "file", "input")
		}},
		{"store_id_entry", func(t *testing.T, dir string) (string, string) {
			db := journalfix.PreCutoverStore(t, filepath.Join(dir, "store"), 15)
			root := filepath.Join(dir, "roots", "extra")
			id, err := apitest.ReadStoreID(db)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, id), []byte(evidence), 0o600); err != nil {
				t.Fatal(err)
			}
			return db, root
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, _, opts := journalAttempt(t)
			for _, sub := range []string{"store", filepath.Join("roots", "extra")} {
				if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(dir, "file"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			db, root := tc.lay(t, dir)
			if _, err := os.Lstat(db); err == nil {
				journalfix.Authorize(t, db)
			}
			opts.LegacyRoots = []string{root}
			before := treeState(t, dir)
			result, err := MigrateJournal(context.Background(), db, opts)
			if tc.name == "store_path" {
				var r *JournalRefusal
				if !errors.As(err, &r) || r.Reason != "store_open_failed" {
					t.Fatalf("store path through a file = %+v, %v; want store_open_failed", result, err)
				}
				assertJournalCommit(t, "MigrateJournal", err, store.CutoverNotCommitted, false)
				if after := treeState(t, dir); !maps.Equal(after, before) {
					t.Errorf("refusal changed the tree:\n before %v\n after  %v", before, after)
				}
				return
			}
			if err != nil || result.Authority != "sqlite" || result.RetirementPending {
				t.Fatalf("migration with %s through a file = %+v, %v; want authority sqlite, no retirement pending", tc.name, result, err)
			}
			if _, err := os.Stat(filepath.Join(result.Backup, "COMPLETE")); err != nil {
				t.Fatalf("backup incomplete: %v", err)
			}
			if tc.name == "store_id_entry" {
				kept, err := filepath.Glob(filepath.Join(result.Backup, "legacy-*", result.StoreID))
				if err != nil || len(kept) != 1 {
					t.Fatalf("backup copies of the file: %v %v", kept, err)
				}
				if got, err := os.ReadFile(kept[0]); err != nil || string(got) != evidence {
					t.Fatalf("backup did not keep the file: %q %v", got, err)
				}
			}
		})
	}
}

// journalAttempt is a store path in a private directory, under a private HOME,
// with valid migrate-journal options whose backup does not exist yet.
func journalAttempt(t *testing.T) (dir, db string, opts JournalMigrationOptions) {
	t.Helper()
	dir = t.TempDir()
	home := filepath.Join(dir, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	return dir, filepath.Join(dir, "state.db"), JournalMigrationOptions{MaintenanceWindow: true, RootsComplete: true, BackupDir: filepath.Join(dir, "backup")}
}

// MigrateJournal's reason precedence over an absent store: a caller's
// cancellation before the cutover commit is interrupted, whatever the verifier
// then returns; a verifier error that is not a refusal is
// process_inventory_unreadable; a refusal keeps its own reason and processes.
// Each is not_committed and creates nothing.
func TestMigrateJournalReasonPrecedence(t *testing.T) {
	listed := []JournalProcess{{PID: 42, Starttime: "7", Role: "agent_director_mutator", Action: "stop_and_restart_after_cutover"}}
	for _, tc := range []struct {
		name, reason string
		cancelled    bool
		verify       error
		processes    []JournalProcess
	}{
		{"cancelled then deadline", "interrupted", true, journalRefusal("inventory_deadline"), []JournalProcess{}},
		{"cancelled then error", "interrupted", true, errors.New("inventory failed"), []JournalProcess{}},
		{"verifier error", "process_inventory_unreadable", false, errors.New("inventory failed"), []JournalProcess{}},
		{"verifier refusal", "old_or_unverified_mutators", false, journalRefusal("old_or_unverified_mutators", listed...), listed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, db, opts := journalAttempt(t)
			t.Cleanup(SetJournalVerifierForTest(func(context.Context, string, []string) error { return tc.verify }))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancelled {
				cancel()
			}
			before := treeState(t, dir)
			_, err := MigrateJournal(ctx, db, opts)
			var r *JournalRefusal
			if !errors.As(err, &r) || r.Reason != tc.reason || r.Action != journalActions[tc.reason] || !reflect.DeepEqual(r.Processes, tc.processes) {
				t.Fatalf("refusal = %#v (%v); want %s with processes %v", r, err, tc.reason, tc.processes)
			}
			assertJournalCommit(t, "MigrateJournal", err, store.CutoverNotCommitted, false)
			if after := treeState(t, dir); !maps.Equal(after, before) {
				t.Errorf("refusal changed the tree:\n before %v\n after  %v", before, after)
			}
		})
	}
}

// JournalCommitBeforeAttempt checks the lock without creating it: free is
// not_committed with no step committed; held by another administrator, or no
// store path to check, is unknown.
func TestJournalCommitBeforeAttempt(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "state.db")
	lock := db + ".input-migration.lock"
	if got := JournalCommitBeforeAttempt(db); got.Cutover != store.CutoverNotCommitted || got.StepsCommitted == nil || *got.StepsCommitted {
		t.Fatalf("free lock: %+v", got)
	}
	if _, err := os.Lstat(lock); !os.IsNotExist(err) {
		t.Fatalf("lock check created the lock file: %v", err)
	}
	holder, err := os.OpenFile(lock, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	if err := unix.Flock(int(holder.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if got := JournalCommitBeforeAttempt(db); got.Cutover != store.CutoverUnknown || got.StepsCommitted != nil {
		t.Fatalf("held lock: %+v", got)
	}
	if got := JournalCommitBeforeAttempt(""); got.Cutover != store.CutoverUnknown || got.StepsCommitted != nil {
		t.Fatalf("no store path: %+v", got)
	}
}

// The one reason table: every reason any non-test file of this package
// refuses with (a new host's verifier file included), and every store cause
// (internal/store's TestInputMigrationCausesComplete keeps that list whole),
// has its own action, and no action carries a host path or OS text.
func TestJournalReasonTable(t *testing.T) {
	reasons := append([]string(nil), store.InputMigrationCauses...)
	refusal := regexp.MustCompile(`journalRefusal\("([a-z_]+)"`)
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range refusal.FindAllStringSubmatch(string(src), -1) {
			reasons = append(reasons, m[1])
		}
	}
	for _, reason := range reasons {
		if journalActions[reason] == "" {
			t.Errorf("reason %s has no action", reason)
		}
	}
	path := regexp.MustCompile(`/[\w.-]`)
	// b.jpo: home-relative advice paths are allowed; host paths remain forbidden.
	homePath := regexp.MustCompile(`~/[\w./-]*`)
	for reason, action := range journalActions {
		lower := strings.ToLower(action)
		if path.MatchString(homePath.ReplaceAllString(action, "")) {
			t.Errorf("%s's action carries a path: %s", reason, action)
		}
		for _, leak := range []string{"no such file", "permission denied", "not a directory", "too many levels", "operation not permitted", "file exists", "errno"} {
			if strings.Contains(lower, leak) {
				t.Errorf("%s's action carries %q: %s", reason, leak, action)
			}
		}
	}
}

// G12: each verifier-level refusal's own action, followed literally, e.g.
// "make each legacy root and its parent directories readable and searchable
// by the user running the migration, keep the maintenance window, then re-run
// this release's installer with a new --journal-backup, …, then run
// migrate-journal with a new --backup-dir until it exits 0 with authority
// "sqlite"": the fix, the installer (its stand-in, journalInstaller), then the
// direct run, over a real store. The refusal changes nothing.
func TestAdviceFollow_G12_VerifierReasons(t *testing.T) {
	const retry = ", keep the maintenance window, then re-run this release's installer with a new --journal-backup, which finishes any pending schema step, then run migrate-journal with a new --backup-dir until it exits 0 with authority \"sqlite\""
	const symlinkAction = "make the store path, HOME and every --legacy-root (install.sh: --journal-legacy-root) real paths with no symbolic link in them; the invoking HOME's ~/.agent-director/input root is always checked, so if ~/.agent-director links to ~/.config/agent-director, first check [store] db_path in config.toml, ~/.claude/settings.json hooks, MCP registrations, scheduled sweeps, PATH links and entries, and scripts for paths under ~/.agent-director and point them at ~/.config/agent-director, then remove only the link with rm -- ~/.agent-director (no trailing slash)" + retry
	for _, tc := range []struct {
		name, reason, action string
		// refuse makes the attempt over db with opts refuse with reason, and
		// returns the action's own step.
		refuse func(t *testing.T, db string, opts *JournalMigrationOptions) (follow func())
	}{
		{"process_inventory_unreadable", "process_inventory_unreadable", "run it as the agents' user with every mounted file system reachable or unmounted (on Linux, with the proc file system mounted and readable)" + retry + "; report it if it persists",
			func(t *testing.T, _ string, _ *JournalMigrationOptions) func() {
				file := filepath.Join(t.TempDir(), "proc")
				if err := os.WriteFile(file, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				journalProcRoot = file
				return func() { journalProcRoot = "/proc" }
			}},
		{"inventory_deadline", "inventory_deadline", "make every mounted file system reachable or unmount it, or answer any privacy consent prompt" + retry,
			func(t *testing.T, db string, _ *JournalMigrationOptions) func() {
				expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				defer cancel()
				assertJournalArgumentRefusal(t, "verifier", verifyJournalQuiescence(expired, db, nil), "inventory_deadline")
				restore := store.SetInputMigrationFaultsForTest(store.InputMigrationFaults{AttemptTimeout: -time.Second})
				t.Cleanup(restore)
				return restore
			}},
		{"process_inventory_unsupported", "process_inventory_unsupported", "this host cannot verify that old mutators are stopped; run the migration on a supported install host (Linux x86_64, Linux aarch64 or Darwin arm64); nothing was changed",
			func(t *testing.T, _ string, _ *JournalMigrationOptions) func() {
				restore := SetJournalVerifierForTest(func(context.Context, string, []string) error { return journalUnsupportedHost() })
				t.Cleanup(restore)
				return restore // this Linux host is a supported one
			}},
		{"legacy_root_symlink/declared_root", "legacy_root_symlink", symlinkAction,
			func(t *testing.T, _ string, opts *JournalMigrationOptions) func() {
				real := opts.LegacyRoots[0]
				link := filepath.Join(filepath.Dir(real), "linked")
				if err := os.Symlink(real, link); err != nil {
					t.Fatal(err)
				}
				opts.LegacyRoots = []string{link}
				return func() { opts.LegacyRoots = []string{real} }
			}},
		// b.jpo: the always-checked HOME root needs explicit compatibility-link
		// advice even when no --legacy-root names it.
		{"legacy_root_symlink/home_compatibility_link", "legacy_root_symlink", symlinkAction,
			func(t *testing.T, _ string, _ *JournalMigrationOptions) func() {
				real := filepath.Join(os.Getenv("HOME"), ".config", "agent-director")
				if err := os.MkdirAll(real, 0o700); err != nil {
					t.Fatal(err)
				}
				link := filepath.Join(os.Getenv("HOME"), ".agent-director")
				if err := os.Symlink(".config/agent-director", link); err != nil {
					t.Fatal(err)
				}
				return func() {
					if err := os.Remove(link); err != nil {
						t.Fatal(err)
					}
				}
			}},
		{"legacy_root_unreadable", "legacy_root_unreadable", "make each legacy root and its parent directories readable and searchable by the user running the migration" + retry,
			func(t *testing.T, _ string, opts *JournalMigrationOptions) func() {
				parent := filepath.Dir(opts.LegacyRoots[0])
				if err := os.Chmod(parent, 0); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
				return func() {
					if err := os.Chmod(parent, 0o700); err != nil {
						t.Fatal(err)
					}
				}
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(func() { journalProcRoot = "/proc" }) // a failed row never leaks its seam
			dir, db, opts := journalAttempt(t)
			if _, err := apitest.InitStore(db); err != nil {
				t.Fatal(err)
			}
			opts.LegacyRoots = []string{filepath.Join(t.TempDir(), "roots", "extra")}
			if err := os.MkdirAll(opts.LegacyRoots[0], 0o700); err != nil {
				t.Fatal(err)
			}
			follow := tc.refuse(t, db, &opts)
			before := treeState(t, dir)
			_, err := MigrateJournal(context.Background(), db, opts)
			var r *JournalRefusal
			if !errors.As(err, &r) || r.Reason != tc.reason || r.Action != tc.action {
				t.Fatalf("refusal = %#v (%v); want %s with %q", r, err, tc.reason, tc.action)
			}
			assertJournalArgumentRefusal(t, "MigrateJournal", err, tc.reason) // processes []
			assertJournalCommit(t, "MigrateJournal", err, store.CutoverNotCommitted, false)
			if after := treeState(t, dir); !maps.Equal(after, before) {
				t.Errorf("refusal changed the store's directory:\n before %v\n after  %v", before, after)
			}
			follow()
			backups := 1
			migrate := func() (store.InputMigrationResult, error) {
				backups++
				opts.BackupDir = filepath.Join(dir, fmt.Sprintf("backup-%d", backups))
				return MigrateJournal(context.Background(), db, opts)
			}
			if strings.Contains(tc.action, "then re-run this release's installer with a new --journal-backup") {
				journalInstaller(t, db, migrate)
			}
			if result, err := migrate(); err != nil || result.Authority != "sqlite" {
				t.Fatalf("followed advice, migrate-journal = %+v, %v; want authority sqlite", result, err)
			}
		})
	}
}
