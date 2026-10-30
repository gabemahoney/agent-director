//go:build linux

package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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

func TestJournalMigrationAuthorizationAndRoots(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, tc := range []struct {
		name, reason string
		opts         JournalMigrationOptions
	}{
		{"boundary", "maintenance_boundary_required", JournalMigrationOptions{}},
		{"roots", "legacy_roots_not_established", JournalMigrationOptions{MaintenanceWindow: true}},
		{"relative", "legacy_root_not_absolute", JournalMigrationOptions{MaintenanceWindow: true, RootsComplete: true, LegacyRoots: []string{"relative"}}},
		{"backup", "absolute_backup_required", JournalMigrationOptions{MaintenanceWindow: true, RootsComplete: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "absent.db")
			_, err := MigrateJournal(context.Background(), path, tc.opts)
			var refusal *JournalRefusal
			if !errors.As(err, &refusal) || refusal.Reason != tc.reason {
				t.Fatalf("refusal = %v", err)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("authorization created store: %v", err)
			}
		})
	}
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
