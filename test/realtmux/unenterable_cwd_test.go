package realtmux_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// Bug b.qwo: a cwd that is a directory but cannot be entered passed spawn's
// and resume's stat checks, and tmux 3.3a, unable to enter it, started the
// pane in the caller's directory and exited 0. Through the production pkg/api
// client on real tmux, both verbs must refuse it with ErrCwdNotFound,
// launching nothing, for a directory with mode 000 ("self000") and for one
// under a mode-000 parent ("parent000"). Skipped as root, which mode 000 does
// not stop.

// unenterableCwd makes a directory of shape (self000 or parent000) and
// returns it with lock, which sets the shape's mode 000 and restores 0700 at
// cleanup.
func unenterableCwd(t *testing.T, shape string) (dir string, lock func()) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir = filepath.Join(base, "self000")
	target := dir
	if shape == "parent000" {
		dir = filepath.Join(base, "parent000", "child")
		target = filepath.Dir(dir)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir, func() {
		if err := os.Chmod(target, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(target, 0o700) })
	}
}

// TestSpawnUnenterableCwdIsCwdNotFound spawns into an unenterable cwd:
// ErrCwdNotFound, no row and no session labelled with the id.
func TestSpawnUnenterableCwdIsCwdNotFound(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores mode 000")
	}
	for _, shape := range []string{"self000", "parent000"} {
		t.Run(shape, func(t *testing.T) {
			f := newSpawnFix(t)
			dir, lock := unenterableCwd(t, shape)
			lock()
			id := newInstanceID("qwo")

			_, err := f.spawnWith(api.SpawnParams{InstanceID: id, CWD: dir})

			if !errors.Is(err, spawn.ErrCwdNotFound) {
				t.Errorf("spawn error = %s, want ErrCwdNotFound", describe(err))
			}
			if _, err := apitest.ReadSpawnColumns(f.DBPath, id); !errors.Is(err, store.ErrSpawnNotFound) {
				t.Errorf("read row %s after the refused spawn: %v; want ErrSpawnNotFound", id, err)
			}
			f.assertNoSessionLabelled(t, id)
		})
	}
}

// TestResumeUnenterableCwdIsCwdNotFound resumes an ended row whose saved cwd
// became unenterable after the row finished: ErrCwdNotFound, the row
// unchanged, and no session labelled with its id.
func TestResumeUnenterableCwdIsCwdNotFound(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores mode 000")
	}
	for _, shape := range []string{"self000", "parent000"} {
		t.Run(shape, func(t *testing.T) {
			f := newSpawnFix(t)
			dir, lock := unenterableCwd(t, shape)
			id, _ := f.seedResumable(t, dir, []string{})
			lock()
			before := readRow(t, f.DBPath, id)

			_, err := f.API.Resume(api.ResumeParams{InstanceID: id})

			if !errors.Is(err, spawn.ErrCwdNotFound) {
				t.Errorf("resume error = %s, want ErrCwdNotFound", describe(err))
			}
			f.reuseRows(t).assertRowUnchanged(t, id, before)
			f.assertNoSessionLabelled(t, id)
		})
	}
}
