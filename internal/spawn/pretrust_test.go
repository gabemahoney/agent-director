package spawn

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// withStubClaudeJSON redirects claudeJSONPath to a file under t.TempDir() so
// pre-trust tests never touch the operator's real ~/.claude.json. The
// returned path is the location of the (initially absent) file; the
// test seeds it or leaves it absent as needed.
func withStubClaudeJSON(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude.json")
	saved := claudeJSONPath
	claudeJSONPath = func() (string, error) { return path, nil }
	t.Cleanup(func() { claudeJSONPath = saved })
	return path
}

// TestPreTrustCreatesEntryInExistingFile pins AC #3: when ~/.claude.json
// exists with no entry for the cwd, pre-trust adds projects.<cwd> with
// hasTrustDialogAccepted=true and leaves every other key untouched.
func TestPreTrustCreatesEntryInExistingFile(t *testing.T) {
	path := withStubClaudeJSON(t)

	// Seed with an existing projects entry for some other dir plus a
	// top-level key Claude Code owns. Both must survive untouched.
	initial := `{
  "projects": {
    "/home/op": {"hasTrustDialogAccepted": true, "hasCompletedProjectOnboarding": true}
  },
  "userID": "operator-uuid"
}`
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cwd := "/tmp/cd-smoke-new"
	if err := preTrustCwd(cwd); err != nil {
		t.Fatalf("preTrustCwd: %v", err)
	}

	got := readClaudeJSON(t, path)

	projects, ok := got["projects"].(map[string]any)
	if !ok {
		t.Fatalf("projects not an object: %T", got["projects"])
	}
	cwdEntry, ok := projects[cwd].(map[string]any)
	if !ok {
		t.Fatalf("projects[%q] not an object: %T", cwd, projects[cwd])
	}
	if v, _ := cwdEntry["hasTrustDialogAccepted"].(bool); !v {
		t.Errorf("hasTrustDialogAccepted = %v; want true", cwdEntry["hasTrustDialogAccepted"])
	}
	// We DO NOT set hasCompletedProjectOnboarding on the new entry —
	// that key has semantics beyond trust (bug b.f75 spec).
	if _, present := cwdEntry["hasCompletedProjectOnboarding"]; present {
		t.Errorf("preTrustCwd should not set hasCompletedProjectOnboarding on the new entry")
	}

	// Unrelated project entry survived.
	otherEntry, ok := projects["/home/op"].(map[string]any)
	if !ok {
		t.Fatalf("other project entry missing or wrong shape: %T", projects["/home/op"])
	}
	if v, _ := otherEntry["hasTrustDialogAccepted"].(bool); !v {
		t.Errorf("unrelated project's hasTrustDialogAccepted should still be true")
	}
	if v, _ := otherEntry["hasCompletedProjectOnboarding"].(bool); !v {
		t.Errorf("unrelated project's hasCompletedProjectOnboarding should be preserved")
	}

	// Unknown top-level key survived.
	if got["userID"] != "operator-uuid" {
		t.Errorf("userID top-level key lost: %v", got["userID"])
	}
}

// TestPreTrustUpdatesExistingEntry pins AC #3 for the case where the
// cwd already has an entry but with hasTrustDialogAccepted=false. The
// flip-to-true must not disturb sibling keys.
func TestPreTrustUpdatesExistingEntry(t *testing.T) {
	path := withStubClaudeJSON(t)

	initial := `{
  "projects": {
    "/tmp/cd-existing": {"hasTrustDialogAccepted": false, "exampleFiles": ["a.go", "b.go"]}
  }
}`
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := preTrustCwd("/tmp/cd-existing"); err != nil {
		t.Fatalf("preTrustCwd: %v", err)
	}

	got := readClaudeJSON(t, path)
	projects := got["projects"].(map[string]any)
	entry := projects["/tmp/cd-existing"].(map[string]any)

	if v, _ := entry["hasTrustDialogAccepted"].(bool); !v {
		t.Errorf("hasTrustDialogAccepted = %v; want true", entry["hasTrustDialogAccepted"])
	}
	// Sibling keys preserved.
	files, ok := entry["exampleFiles"].([]any)
	if !ok || len(files) != 2 {
		t.Errorf("exampleFiles lost or wrong shape: %v", entry["exampleFiles"])
	}
}

// TestPreTrustMissingFileReturnsSentinel pins AC #5: when ~/.claude.json
// does not exist (truly-fresh-machine case), preTrustCwd returns
// ErrClaudeJSONMissing. Launch's caller treats that as a soft warning
// and continues.
func TestPreTrustMissingFileReturnsSentinel(t *testing.T) {
	withStubClaudeJSON(t) // file does not exist; helper just sets the path.

	err := preTrustCwd("/tmp/some-cwd")
	if !errors.Is(err, ErrClaudeJSONMissing) {
		t.Fatalf("err = %v; want ErrClaudeJSONMissing", err)
	}
}

// TestPreTrustAtomicRenameLeavesNoTempFile pins the temp+rename atomic
// write contract — after a successful pre-trust, the target file is
// the only ".claude.json*" inode in the directory.
func TestPreTrustAtomicRenameLeavesNoTempFile(t *testing.T) {
	path := withStubClaudeJSON(t)
	if err := os.WriteFile(path, []byte(`{"projects":{}}`), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := preTrustCwd("/tmp/x"); err != nil {
		t.Fatalf("preTrustCwd: %v", err)
	}

	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != ".claude.json" {
			t.Errorf("stray file after atomic rename: %s", e.Name())
		}
	}
}

// TestPreTrustConcurrentSpawnsDoNotCorrupt pins AC #4: concurrent
// pre-trust calls for different cwds end with all of those cwds
// flipped to true and the file still parseable. The bug spec allows
// last-writer-wins for the file as a whole, so we cannot assert
// "every concurrent caller's value wins" — we assert that *at least
// one* of the racing keys lands (i.e. the file is parseable and
// contains real entries), and that the file is never torn.
func TestPreTrustConcurrentSpawnsDoNotCorrupt(t *testing.T) {
	path := withStubClaudeJSON(t)
	if err := os.WriteFile(path, []byte(`{"projects":{}}`), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	const N = 20
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer wg.Done()
			_ = preTrustCwd(filepath.Join("/tmp/concurrent", string(rune('a'+i))))
		}()
	}
	wg.Wait()

	got := readClaudeJSON(t, path)
	projects, ok := got["projects"].(map[string]any)
	if !ok {
		t.Fatalf("projects missing or wrong shape after concurrent writes: %T", got["projects"])
	}
	if len(projects) == 0 {
		t.Errorf("no projects landed after %d concurrent pre-trusts; last-writer-wins should leave at least one", N)
	}
	for k, v := range projects {
		entry, ok := v.(map[string]any)
		if !ok {
			t.Errorf("projects[%q] not an object: %T", k, v)
			continue
		}
		if b, _ := entry["hasTrustDialogAccepted"].(bool); !b {
			t.Errorf("projects[%q].hasTrustDialogAccepted not true: %v", k, entry["hasTrustDialogAccepted"])
		}
	}
}

// TestPreTrustEmptyFileTreatedAsEmptyObject pins the edge case where
// ~/.claude.json is zero bytes (Claude Code can leave it that way
// briefly during its own writes). preTrustCwd should treat that as
// an empty top-level object and seed the projects entry from scratch.
func TestPreTrustEmptyFileTreatedAsEmptyObject(t *testing.T) {
	path := withStubClaudeJSON(t)
	if err := os.WriteFile(path, []byte{}, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := preTrustCwd("/tmp/empty-case"); err != nil {
		t.Fatalf("preTrustCwd: %v", err)
	}

	got := readClaudeJSON(t, path)
	projects := got["projects"].(map[string]any)
	entry := projects["/tmp/empty-case"].(map[string]any)
	if v, _ := entry["hasTrustDialogAccepted"].(bool); !v {
		t.Errorf("hasTrustDialogAccepted not true after seeding empty file: %v", entry)
	}
}

// readClaudeJSON parses the stub claude.json file as a generic JSON
// object for assertion. Used by every test that inspects the result.
func readClaudeJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read claude.json: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("parse claude.json: %v (raw=%q)", err, string(raw))
	}
	return out
}
