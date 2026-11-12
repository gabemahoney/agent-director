//go:build linux

package main_test

import (
	"context"
	"encoding/json"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/adminapi"
	"github.com/gabemahoney/agent-director/internal/probe"
)

// b.5pb: the explicit store's inventory must refuse before a migration can
// begin. A non-agent-director child holds each kind of file open; checking its
// identity prevents the test runner's own executable from satisfying the test.
func TestMigrateJournalCheckOnlyHolders(t *testing.T) {
	for _, kind := range []string{"store", "wal", "shm", "legacy-root"} {
		t.Run(kind, func(t *testing.T) {
			home, storeHome := poisonedHome(t), poisonedHome(t)
			db := stateDB(storeHome) // An explicit store outside the invoking HOME.
			root, backupParent := t.TempDir(), t.TempDir()
			if err := os.Chmod(root, 0o700); err != nil {
				t.Fatal(err)
			}
			backup := filepath.Join(backupParent, "backup")
			held := db
			switch kind {
			case "wal":
				held += "-wal"
			case "shm":
				held += "-shm"
			case "legacy-root":
				held = filepath.Join(root, "launch.json")
			}
			if held != db {
				if err := os.WriteFile(held, []byte("private evidence\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			checkOnlyUnchanged(t, home, storeHome, root, backupParent)

			child, start := journalInventoryProcess(t, held, []string{"/bin/cat"})
			args := checkOnlyArgs(root, backup)
			if kind == "store" {
				// Omitting the explicit path retains the argument-only contract,
				// even though a holder is already present.
				out, errOut, code := runAdmin(t, home, args...)
				if code != 0 || out != "{}\n" || errOut != "" {
					t.Errorf("argument-only: exit %d, stdout %q, stderr %q", code, out, errOut)
				}
				invalid := append([]string{"--store-path", db}, dropArg(args, "--maintenance-window")...)
				out, errOut, code = runAdmin(t, home, invalid...)
				if reason, _ := journalRefusal(t, out, errOut, code); reason != "maintenance_boundary_required" {
					t.Errorf("argument refusal = %q; want maintenance_boundary_required with no processes", reason)
				}
			}
			out, errOut, code := runAdmin(t, home, append([]string{"--store-path", db}, args...)...)
			assertOnlyEnvelope(t, out, errOut, code, "ErrJournalMigrationRefused")
			assertCommitDetails(t, errOut, "old_or_unverified_mutators", "not_committed", "false", false, "")
			var env journalRefusalEnvelope
			if err := json.Unmarshal([]byte(errOut), &env); err != nil {
				t.Fatal(err)
			}
			var refusal adminapi.JournalRefusal
			if err := json.Unmarshal(env.Details["refusal"], &refusal); err != nil {
				t.Fatal(err)
			}
			want := adminapi.JournalProcess{PID: child.Process.Pid, Starttime: start, Role: "agent_director_mutator", Action: "stop_and_restart_after_cutover"}
			if !slices.Contains(refusal.Processes, want) {
				t.Errorf("processes = %+v; want holder %+v", refusal.Processes, want)
			}
			if got, alive, known := probe.NewProcChecker().StartTime(child.Process.Pid); !alive || !known || got != start {
				t.Error("check-only stopped or replaced the holder")
			}
		})
	}
}

// b.5pb: an explicit store with no blockers succeeds without loading config
// or opening SQLite, with absent or present sidecars. The existing child
// runner's passing verifier avoids inventorying unrelated AD test processes.
// Real argv/executable discrimination is covered in internal/adminapi with
// its process-listing seam; real CLI holder detection is covered above.
func TestMigrateJournalCheckOnlyNoHolders(t *testing.T) {
	for _, kind := range []string{"absent-sidecars", "present-sidecars", "argv-only-mentions"} {
		t.Run(kind, func(t *testing.T) {
			home, storeHome := poisonedHome(t), poisonedHome(t)
			db := stateDB(storeHome)
			if kind == "present-sidecars" {
				for _, suffix := range []string{"-wal", "-shm"} {
					if err := os.WriteFile(db+suffix, []byte("unchanged sidecar\n"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			root, backupParent := t.TempDir(), t.TempDir()
			if err := os.Chmod(root, 0o700); err != nil {
				t.Fatal(err)
			}
			checkOnlyUnchanged(t, home, storeHome, root, backupParent)
			backup := filepath.Join(backupParent, "backup")
			var child *exec.Cmd
			var start string
			if kind == "argv-only-mentions" {
				// b.5pb: model a tmux server whose argv and launch environment
				// mention AD, but whose executable holds no migration input.
				child, start = journalInventoryProcess(t, "",
					[]string{"/bin/bash", "-c", `exec -a 'tmux: server agent-director serve' /bin/cat`},
					"AGENT_DIRECTOR_INSTANCE_ID=argv-only", "AGENT_DIRECTOR_LAUNCH_TOKEN=argv-only-launch")
			}
			args := append([]string{"--store-path", db}, checkOnlyArgs(root, backup)...)
			env := append(fakeTmuxEnv(t, home), "HOME="+home)
			out, errOut, code := adminChild(t, "verify-pass", env, args, false, nil)
			if code != 0 || out != "{}\n" || errOut != "" {
				t.Errorf("exit %d, stdout %q, stderr %q; want 0, {} and nothing", code, out, errOut)
			}
			if child != nil {
				if got, alive, known := probe.NewProcChecker().StartTime(child.Process.Pid); !alive || !known || got != start {
					t.Error("check-only stopped or replaced the argv-only process")
				}
			}
		})
	}
}

// journalInventoryProcess waits for cat to echo readiness after exec, with
// an optional inherited descriptor. No sleep or argv text establishes identity.
func journalInventoryProcess(t *testing.T, held string, argv []string, env ...string) (*exec.Cmd, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	child := exec.CommandContext(ctx, argv[0], argv[1:]...)
	child.Env = append(os.Environ(), env...)
	if held != "" {
		fd, err := os.Open(held)
		if err != nil {
			t.Fatal(err)
		}
		defer fd.Close()
		child.ExtraFiles = []*os.File{fd}
	}
	stdin, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = child.Process.Kill()
		_ = child.Wait()
	})
	if _, err := io.WriteString(stdin, "ready\n"); err != nil {
		t.Fatal(err)
	}
	ready := make([]byte, len("ready\n"))
	if _, err := io.ReadFull(stdout, ready); err != nil || string(ready) != "ready\n" {
		t.Fatalf("inventory process readiness = %q (%v)", ready, err)
	}
	start, alive, known := probe.NewProcChecker().StartTime(child.Process.Pid)
	if !alive || !known {
		t.Fatal("inventory process identity unavailable")
	}
	return child, start
}

// checkOnlyUnchanged checks names and bytes even when an envelope assertion
// fails. Flat fixture directories expose any store lock, sentinel or backup
// creation as well as changes to the store and its WAL/SHM presence.
func checkOnlyUnchanged(t *testing.T, home, storeHome, root, backupParent string) {
	t.Helper()
	for _, dir := range []string{filepath.Dir(stateDB(home)), filepath.Dir(stateDB(storeHome)), root, backupParent} {
		before := directoryEntries(t, dir)
		t.Cleanup(func() {
			if after := directoryEntries(t, dir); !maps.Equal(after, before) {
				t.Errorf("check-only changed %s: before %v, after %v", dir, before, after)
			}
		})
	}
}
