//go:build linux

package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/testsupport/sandboxguard"
	"golang.org/x/sys/unix"
)

func TestMain(m *testing.M) { sandboxguard.Require(); os.Exit(m.Run()) }

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
			path := filepath.Join(base, "state.db")
			_, err = MigrateJournal(context.Background(), path, opts)
			assertJournalArgumentRefusal(t, "MigrateJournal", err, tc.reason)
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

// G9: "keep the maintenance boundary, stop incompatible mutators, and restart
// administrator verification". Actual FD/start identities, no process mocks.
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
				role, action = "unverified_process", "stop_or_make_inventory_readable"
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
			for i := 0; i < 2; i++ {
				err := verifyJournalQuiescence(ctx, db, []string{legacy})
				var r *JournalRefusal
				if !errors.As(err, &r) || r.Reason != "old_or_unverified_mutators" || r.Action != "keep the maintenance boundary, stop incompatible mutators, and restart administrator verification" {
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
			// Literally stop our incompatible writer, await exit, and verify again.
			if err := stdin.Close(); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Wait(); err != nil {
				t.Fatal(err)
			}
			if err := verifyJournalQuiescence(ctx, db, []string{legacy}); err != nil {
				t.Fatalf("condition cleared but verification still refused: %v", err)
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

func TestJournalMigrationRefusesUnestablishedPaths(t *testing.T) {
	for _, kind := range []string{"symlink", "public_root", "unreadable_parent"} {
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
			}
			var r *JournalRefusal
			if err := verifyJournalPath(path); !errors.As(err, &r) || r.Reason != want {
				t.Fatalf("path refusal %v; want %s", err, want)
			}
		})
	}
}
