package realtmux_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// Bug b.vit (GitHub #37): tmux 3.3a starts a session whose -c directory
// cannot be entered in another directory and still exits 0, so resume of a
// finished row whose saved cwd was removed launched the agent elsewhere and
// reported success. Through the production pkg/api client on real tmux,
// resume must refuse it as spawn does, launching nothing.

// TestResumeRemovedCwdIsCwdNotFound resumes an ended and a missing row whose
// saved cwd was removed after the row finished: ErrCwdNotFound, the row
// unchanged, and no session on the row's socket labelled with its id.
func TestResumeRemovedCwdIsCwdNotFound(t *testing.T) {
	for _, state := range []string{store.StateEnded, store.StateMissing} {
		t.Run(state, func(t *testing.T) {
			f := newSpawnFix(t)
			cwd := filepath.Join(t.TempDir(), "ad-gone")
			if err := os.Mkdir(cwd, 0o700); err != nil {
				t.Fatal(err)
			}
			cwd, err := filepath.EvalSymlinks(cwd)
			if err != nil {
				t.Fatal(err)
			}
			id, _ := f.seedResumableIn(t, state, cwd, []string{})
			if err := os.RemoveAll(cwd); err != nil {
				t.Fatal(err)
			}
			before := readRow(t, f.DBPath, id)

			_, err = f.API.Resume(api.ResumeParams{InstanceID: id})

			if !errors.Is(err, spawn.ErrCwdNotFound) {
				t.Errorf("resume error = %s, want ErrCwdNotFound", describe(err))
			}
			f.reuseRows(t).assertRowUnchanged(t, id, before)
			f.assertNoSessionLabelled(t, id)
		})
	}
}

// assertNoSessionLabelled fails when a session on f's socket carries a label
// naming instance id; no server on the socket holds none.
func (f *spawnFix) assertNoSessionLabelled(t *testing.T, id string) {
	t.Helper()
	res := f.run(t, "list-sessions", "-F", "#{session_id}")
	if res.Exit != 0 {
		if !strings.Contains(res.Stderr, "no server running") && !strings.Contains(res.Stderr, "error connecting to") {
			t.Fatalf("list-sessions on %s: exit %d, stderr %q", f.Socket, res.Exit, redact(res.Stderr))
		}
		return
	}
	for _, sid := range strings.Fields(res.Stdout) {
		if label := f.label(t, sid); strings.Contains(label, " "+id+" ") {
			t.Errorf("session %s is labelled with instance %s; want no launch", sid, id)
		}
	}
}
