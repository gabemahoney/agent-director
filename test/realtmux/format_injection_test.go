package realtmux_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/pkg/api"
)

// Bug b.dsx: tmux expands the create's -c value as a format, so a cwd
// holding "#(...)" ran a shell command on the server and "#{...}", "#S" or
// "##" changed the path. Spawn and resume, through the production pkg/api
// client on real tmux, must start the agent in the cwd exactly as given and
// run nothing.

// formatDirs are cwd base names holding format sequences. MARKER stands for
// the absolute path a "#(touch ...)" job would create; its "/" nests the
// directory, as a real attacker's path may.
var formatDirs = []struct{ name, dir string }{
	{"job", "x#(touch MARKER)"},
	{"style-then-job", "x#[y]#(touch MARKER)"},
	{"variable", "x#{session_name}"},
	{"alias", "x#S"},
	{"double-hash", "x##"},
	{"trailing-hash", "x#"},
	{"hash-semicolon", "x#;"},
	{"style", "x#[y"},
	{"double-hash-style", "x##[y"},
	{"leading-hash", "#lead"},
}

// jobWindow is how long a test watches for a "#(...)" job's marker: tmux runs
// the job in the background, so it may land after the create returns.
const jobWindow = 500 * time.Millisecond

// formatCwd makes the directory dir names (MARKER replaced by a fresh marker
// path) and returns its canonical path and the marker path.
func formatCwd(t *testing.T, dir string) (cwd, marker string) {
	t.Helper()
	marker = filepath.Join(t.TempDir(), "pwned")
	made := filepath.Join(t.TempDir(), strings.ReplaceAll(dir, "MARKER", marker))
	if err := os.MkdirAll(made, 0o700); err != nil {
		t.Fatalf("make cwd: %v", err)
	}
	cwd, err := filepath.EvalSymlinks(made)
	if err != nil {
		t.Fatalf("canonical cwd: %v", err)
	}
	return cwd, marker
}

// assertStartedIn checks the row records cwd, the session path and the
// pane's current path are cwd exactly, and no job created marker (watched
// for jobWindow when watch is set).
func assertStartedIn(t *testing.T, s spawned, cwd, marker string, watch bool) {
	t.Helper()
	if s.Row.CWD != cwd {
		t.Errorf("row cwd = %v, want %q", s.Row.CWD, cwd)
	}
	paneID := s.Row.PaneID.(string)
	waitExeced(t, s.rt.formatInt(t, paneID, "#{pane_pid}"), "sleep")
	for _, f := range []string{"#{session_path}", "#{pane_current_path}"} {
		if got := s.rt.format(t, paneID, f); got != cwd {
			t.Errorf("%s = %q, want %q", f, got, cwd)
		}
	}
	deadline := time.Now().Add(jobWindow)
	for {
		if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("stat %s: %v; want no such file (a #(...) job in the cwd ran)", marker, err)
			return
		}
		if !watch || time.Now().After(deadline) {
			return
		}
		time.Sleep(pollInterval)
	}
}

// TestSpawnCwdFormatSequencesAreLiteral spawns in each formatDirs directory:
// the spawn is labelled as usual and the agent starts in that directory.
func TestSpawnCwdFormatSequencesAreLiteral(t *testing.T) {
	for _, tc := range formatDirs {
		t.Run(tc.name, func(t *testing.T) {
			f := newSpawnFix(t)
			cwd, marker := formatCwd(t, tc.dir)
			s := f.mustLaunchWith(t, api.SpawnParams{CWD: cwd})
			f.assertSpawned(t, s, f.Socket, 1)
			assertStartedIn(t, s, cwd, marker, strings.Contains(tc.dir, "MARKER"))
		})
	}
}

// TestResumeCwdFormatSequencesAreLiteral resumes an ended row stored with
// each formatDirs directory as its cwd: the agent starts in that directory.
func TestResumeCwdFormatSequencesAreLiteral(t *testing.T) {
	for _, tc := range formatDirs {
		t.Run(tc.name, func(t *testing.T) {
			f := newSpawnFix(t)
			cwd, marker := formatCwd(t, tc.dir)
			id, _ := f.seedResumable(t, cwd, []string{})
			if _, err := f.API.Resume(api.ResumeParams{ClaudeInstanceID: id}); err != nil {
				t.Fatalf("resume: %s", describe(err))
			}
			assertStartedIn(t, f.launched(t, id), cwd, marker, strings.Contains(tc.dir, "MARKER"))
		})
	}
}
