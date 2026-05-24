package api_test

import (
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"testing"
)

// TestMain is the package-level entry point for pkg/api tests.
//
// It performs two HOME-isolation duties before and after m.Run():
//
//  1. Defense-in-depth redirect: sets $HOME to a fresh temp directory before
//     any test or example runs.  This catches paths that use os.Getenv("HOME")
//     or os.UserHomeDir() — for example, internal/spawn/pretrust.go calls
//     os.UserHomeDir() when pre-trusting a CWD.  The redirect ensures those
//     paths land under the temp dir, not the real home directory.
//
//  2. Load-bearing canary: records the state of the REAL ~/.agent-director
//     (via user.Current().HomeDir, which reads /etc/passwd on Linux and is
//     unaffected by the $HOME redirect) before and after the run.  Any new
//     file or modification under that directory fails the suite.  This
//     catches any code path that uses user.Current() to bypass the $HOME
//     redirect and writes to the real home directory.
//
// The canary exists so future examples cannot accidentally write to
// ~/.agent-director/state.db even if they forget to supply Options.StorePath.
func TestMain(m *testing.M) {
	// ── Step 1: locate real ~/.agent-director via user.Current() ─────────────
	// user.Current().HomeDir reads /etc/passwd (or equivalent) on Linux and
	// is NOT affected by the $HOME env var, so this gives the real home even
	// after we redirect $HOME below.
	u, err := user.Current()
	if err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: user.Current: %v\n", err)
		os.Exit(2)
	}
	realAgentDir := filepath.Join(u.HomeDir, ".agent-director")

	// ── Step 2: snapshot real ~/.agent-director BEFORE any test runs ─────────
	before := snapshotAgentDir(realAgentDir)

	// ── Step 3: redirect $HOME (defense-in-depth) ─────────────────────────────
	// Catches paths that use os.Getenv("HOME") / os.UserHomeDir().
	tmpHome, err := os.MkdirTemp("", "pkg-api-home-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: MkdirTemp: %v\n", err)
		os.Exit(2)
	}
	os.Setenv("HOME", tmpHome) // nolint:errcheck — os.Setenv never errors on non-nil key

	// ── Step 3b: clear AGENT_DIRECTOR_INSTANCE_ID ────────────────────────────
	// spawn.Launch reads this env var and uses it as parent_id for the new
	// store row. When tests run inside an active agent-director session the
	// var is set to a real UUID that does not exist in the freshly-created
	// test database, causing an FK constraint failure. Clearing it ensures
	// all test spawns are created as roots (NULL parent_id).
	os.Unsetenv("AGENT_DIRECTOR_INSTANCE_ID") // nolint:errcheck

	// ── Step 4: run all tests and examples ───────────────────────────────────
	code := m.Run()

	// ── Step 5: clean up the fake home ───────────────────────────────────────
	_ = os.RemoveAll(tmpHome)

	// ── Step 6: canary — assert real ~/.agent-director is unchanged ──────────
	after := snapshotAgentDir(realAgentDir)
	if msgs := agentDirViolations(before, after); len(msgs) > 0 {
		fmt.Fprintln(os.Stderr, "FAIL: pkg/api tests wrote to the real ~/.agent-director:")
		for _, msg := range msgs {
			fmt.Fprintln(os.Stderr, "  "+msg)
		}
		os.Exit(1)
	}

	os.Exit(code)
}

// ── snapshot helpers ──────────────────────────────────────────────────────────

// fileSnap captures the stable identity of a single file for change detection.
type fileSnap struct {
	Size    int64
	Mode    fs.FileMode
	ModTime int64 // UnixNano — sub-second precision catches fast writes
}

// agentDirSnap is a map from absolute path to fileSnap for every file under
// ~/.agent-director.  A nil value means the directory did not exist at
// snapshot time.
type agentDirSnap map[string]fileSnap

// snapshotAgentDir walks root and returns a snapshot of every file.
// Returns nil if the directory does not exist (not an error).
func snapshotAgentDir(root string) agentDirSnap {
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		// Unexpected stat error — return a sentinel so the canary can report it.
		return agentDirSnap{"__stat_error__": {Size: -1}}
	}

	snap := agentDirSnap{}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			snap[path] = fileSnap{Size: -1} // record inaccessible entries
			return nil
		}
		if d.IsDir() {
			return nil // directories themselves are not tracked
		}
		info, err := d.Info()
		if err != nil {
			snap[path] = fileSnap{Size: -1}
			return nil
		}
		snap[path] = fileSnap{
			Size:    info.Size(),
			Mode:    info.Mode(),
			ModTime: info.ModTime().UnixNano(),
		}
		return nil
	})
	return snap
}

// agentDirViolations returns a non-empty slice of human-readable violation
// messages if any new files appeared or existing files were modified under
// ~/.agent-director between the before and after snapshots.
//
// Deletions are not considered violations — we only guard against writes.
// If the directory was absent before and now exists, every new file is a
// violation.
func agentDirViolations(before, after agentDirSnap) []string {
	// Directory was absent before and is still absent — clean.
	if before == nil && after == nil {
		return nil
	}
	// Directory appeared during the test run.
	if before == nil && after != nil {
		msgs := []string{"~/.agent-director was created during the test run"}
		for path := range after {
			msgs = append(msgs, "  created: "+path)
		}
		return msgs
	}
	// Directory was removed during the test run — deletions are allowed.
	if after == nil {
		return nil
	}

	var msgs []string
	for path, afterSnap := range after {
		if afterSnap.Size == -1 {
			// Inaccessible entry — treat conservatively as a violation.
			msgs = append(msgs, "inaccessible: "+path)
			continue
		}
		beforeSnap, existed := before[path]
		if !existed {
			msgs = append(msgs, "new file: "+path)
		} else if afterSnap != beforeSnap {
			msgs = append(msgs, fmt.Sprintf("modified: %s (size %d→%d, mtime %d→%d)",
				path, beforeSnap.Size, afterSnap.Size,
				beforeSnap.ModTime, afterSnap.ModTime))
		}
	}
	return msgs
}
