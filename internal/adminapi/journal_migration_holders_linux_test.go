//go:build linux

package adminapi

import (
	"context"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/probe"
)

// b.5pb: argv and launch-environment mentions alone never identify a holder.
// Serial: journalProcRoot selects only our child; exe, fd and start identity
// still come from real /proc, regardless of other packages' live AD programs.
func TestJournalInventoryArgvOnlyMentions(t *testing.T) {
	dir := t.TempDir()
	home, root, db := filepath.Join(dir, "home"), filepath.Join(dir, "input"), filepath.Join(dir, "state.db")
	for _, path := range []string{home, root} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	if err := os.WriteFile(db, []byte("not a database\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := treeState(t, dir)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	child := exec.CommandContext(ctx, "/bin/bash", "-c", `exec -a 'tmux: server agent-director serve' /bin/cat`)
	child.Env = append(os.Environ(), "AGENT_DIRECTOR_INSTANCE_ID=argv-only", "AGENT_DIRECTOR_LAUNCH_TOKEN=argv-only-launch")
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
	// Echoed input proves cat has exec'd with the chosen argv before inventory.
	if _, err := io.WriteString(stdin, "ready\n"); err != nil {
		t.Fatal(err)
	}
	ready := make([]byte, len("ready\n"))
	if _, err := io.ReadFull(stdout, ready); err != nil || string(ready) != "ready\n" {
		t.Fatalf("argv-only process readiness = %q (%v)", ready, err)
	}
	pid := child.Process.Pid
	start, alive, known := probe.NewProcChecker().StartTime(pid)
	if !alive || !known {
		t.Fatal("argv-only process identity unavailable")
	}
	previous := journalProcRoot
	journalProcRoot = t.TempDir()
	t.Cleanup(func() { journalProcRoot = previous })
	if err := os.Symlink(filepath.Join("/proc", strconv.Itoa(pid)), filepath.Join(journalProcRoot, strconv.Itoa(pid))); err != nil {
		t.Fatal(err)
	}
	if err := CheckJournalMigrationHolders(ctx, db, JournalMigrationOptions{
		MaintenanceWindow: true, RootsComplete: true, LegacyRoots: []string{root}, BackupDir: filepath.Join(dir, "backup"),
	}); err != nil {
		t.Errorf("argv/environment-only mention blocked real inventory: %v", err)
	}
	if got, alive, known := probe.NewProcChecker().StartTime(pid); !alive || !known || got != start {
		t.Error("inventory stopped or replaced the argv-only process")
	}
	if after := treeState(t, dir); !maps.Equal(after, before) {
		t.Errorf("inventory changed fixture files: before %v, after %v", before, after)
	}
}
