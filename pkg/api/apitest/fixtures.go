package apitest

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
)

// SeedListFixture inserts 6 spawn rows with mixed states/labels/parents/cwds
// into a fresh temp store and returns the open store and its DB path.
// The store is registered for cleanup via t.Cleanup.
//
// Layout (each row's instance id reads as a quick identity):
//
//	row-a-wait-foo:        waiting, labels project=foo+env=dev, cwd /tmp,  no parent
//	row-b-wait-foo-other:  waiting, label  project=foo,         cwd /tmp,  parent=row-a-wait-foo
//	row-c-work-bar:        working, label  project=bar,         cwd /tmp,  no parent
//	row-d-ended-foo:       ended,   label  project=foo,         cwd /opt,  no parent
//	row-e-ask:             ask_user, no labels,                 cwd /tmp,  no parent
//	row-f-wait-no-label:   waiting, no labels,                  cwd /opt,  no parent
func SeedListFixture(t *testing.T) (*store.Store, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	s, err := store.OpenOrInit(dbPath)
	if err != nil {
		t.Fatalf("SeedListFixture: store.OpenOrInit: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	insert := func(id, parent, state, cwd string, labels map[string]string) {
		t.Helper()
		row := store.Spawn{
			ClaudeInstanceID: id,
			ParentID:         parent,
			CWD:              cwd,
			TmuxSessionName:  "cd-" + id,
			RelayMode:        "off",
			Labels:           labels,
		}
		if err := s.InsertPending(row); err != nil {
			t.Fatalf("SeedListFixture: InsertPending %s: %v", id, err)
		}
		if state != store.StatePending {
			if err := s.ApplyHookTransition(id, state, false); err != nil {
				t.Fatalf("SeedListFixture: ApplyHookTransition %s: %v", id, err)
			}
		}
	}

	insert("row-a-wait-foo", "", store.StateWaiting, "/tmp",
		map[string]string{"project": "foo", "env": "dev"})
	insert("row-b-wait-foo-other", "row-a-wait-foo", store.StateWaiting, "/tmp",
		map[string]string{"project": "foo"})
	insert("row-c-work-bar", "", store.StateWorking, "/tmp",
		map[string]string{"project": "bar"})
	insert("row-d-ended-foo", "", store.StateEnded, "/opt",
		map[string]string{"project": "foo"})
	insert("row-e-ask", "", store.StateAskUser, "/tmp", nil)
	insert("row-f-wait-no-label", "", store.StateWaiting, "/opt", nil)
	return s, dbPath
}

// SeedDeleteFixture seeds a store with a live row and an ended row.
// Returns the open store and its DB path.
// The store is registered for cleanup via t.Cleanup.
func SeedDeleteFixture(t *testing.T) (*store.Store, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	s, err := store.OpenOrInit(dbPath)
	if err != nil {
		t.Fatalf("SeedDeleteFixture: store.OpenOrInit: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	for _, sp := range []struct {
		id, state string
	}{
		{"row-live", store.StateWaiting},
		{"row-ended", store.StateEnded},
	} {
		row := store.Spawn{
			ClaudeInstanceID: sp.id,
			CWD:              "/tmp",
			TmuxSessionName:  "cd-" + sp.id,
			RelayMode:        "off",
		}
		if err := s.InsertPending(row); err != nil {
			t.Fatalf("SeedDeleteFixture: InsertPending %s: %v", sp.id, err)
		}
		if sp.state != store.StatePending {
			if err := s.ApplyHookTransition(sp.id, sp.state, false); err != nil {
				t.Fatalf("SeedDeleteFixture: transition %s: %v", sp.id, err)
			}
		}
	}
	return s, dbPath
}

// SeedDecideFixture opens a store and inserts a Spawn row with a configurable
// relay_mode in state check_permission. Returns the open store and its DB path.
// The store is registered for cleanup via t.Cleanup.
func SeedDecideFixture(t *testing.T, relayMode string) (*store.Store, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	s, err := store.OpenOrInit(dbPath)
	if err != nil {
		t.Fatalf("SeedDecideFixture: store.OpenOrInit: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.InsertPending(store.Spawn{
		ClaudeInstanceID: "id-d-1",
		CWD:              "/tmp",
		TmuxSessionName:  "cd-d-1",
		RelayMode:        relayMode,
	}); err != nil {
		t.Fatalf("SeedDecideFixture: InsertPending: %v", err)
	}
	// Transition into check_permission so it looks realistic.
	if err := s.ApplyHookTransition("id-d-1", store.StateCheckPermission, false); err != nil {
		t.Fatalf("SeedDecideFixture: transition: %v", err)
	}
	return s, dbPath
}

// SeedPermissionRow inserts an open permission request for id into s using
// TestRequestTokenA as the canonical single-row test token.
func SeedPermissionRow(t *testing.T, s *store.Store, id string) {
	t.Helper()
	if err := s.UpsertOpenPermissionRequest(id, storefix.TestRequestTokenA, "Bash", `{"cmd":"echo"}`); err != nil {
		t.Fatalf("SeedPermissionRow: UpsertOpenPermissionRequest: %v", err)
	}
}

// SeedExpireFixture builds a 5-row DB with mixed states and ages for use
// with the expire verb. Returns the open store and its DB path.
//
// The store is registered for cleanup via t.Cleanup. The caller may open
// a separate raw sql.DB against dbPath to backdate rows (as expire_test.go
// does); the returned *store.Store remains the primary handle.
func SeedExpireFixture(t *testing.T) (*store.Store, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	s, err := store.OpenOrInit(dbPath)
	if err != nil {
		t.Fatalf("SeedExpireFixture: store.OpenOrInit: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	insert := func(id, state, cwd string) {
		t.Helper()
		row := store.Spawn{
			ClaudeInstanceID: id,
			CWD:              cwd,
			TmuxSessionName:  "cd-" + id,
			RelayMode:        "off",
		}
		if err := s.InsertPending(row); err != nil {
			t.Fatalf("SeedExpireFixture: InsertPending %s: %v", id, err)
		}
		if state != store.StatePending {
			if err := s.ApplyHookTransition(id, state, false); err != nil {
				t.Fatalf("SeedExpireFixture: transition %s: %v", id, err)
			}
		}
	}

	insert("row-ended-old", store.StateEnded, "/tmp")
	insert("row-ended-fresh", store.StateEnded, "/tmp")
	insert("row-missing-old", store.StateMissing, "/tmp")
	insert("row-waiting-live", store.StateWaiting, "/tmp")
	insert("row-ended-null-at", store.StateEnded, "/tmp")

	// Backdate two terminal rows so they predate "now - 1h"; the
	// 3rd terminal row's ended_at stays at insert time (~now), the
	// live row has no ended_at, and the 5th has its ended_at NULLed
	// out to exercise the "ended_at IS NOT NULL" predicate.
	raw, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("SeedExpireFixture: raw open: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })

	backdate := time.Now().UTC().Add(-2 * time.Hour).Format("2006-01-02 15:04:05")
	if _, err := raw.Exec(
		`UPDATE spawns SET ended_at = ? WHERE claude_instance_id = ?`,
		backdate, "row-ended-old"); err != nil {
		t.Fatalf("SeedExpireFixture: backdate old: %v", err)
	}
	if _, err := raw.Exec(
		`UPDATE spawns SET ended_at = ? WHERE claude_instance_id = ?`,
		backdate, "row-missing-old"); err != nil {
		t.Fatalf("SeedExpireFixture: backdate missing: %v", err)
	}
	if _, err := raw.Exec(
		`UPDATE spawns SET ended_at = NULL WHERE claude_instance_id = ?`,
		"row-ended-null-at"); err != nil {
		t.Fatalf("SeedExpireFixture: null-at: %v", err)
	}

	return s, dbPath
}

// SeedJsonl writes a minimal placeholder JSONL file at the path
// spawn.JsonlPath(cwd, sessionID) resolves to. Returns the resolved path.
func SeedJsonl(t *testing.T, cwd, sessionID string) string {
	t.Helper()
	p, err := spawn.JsonlPath(cwd, sessionID)
	if err != nil {
		t.Fatalf("SeedJsonl: JsonlPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatalf("SeedJsonl: mkdir jsonl parent: %v", err)
	}
	if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("SeedJsonl: write jsonl: %v", err)
	}
	return p
}

// SeedStore creates a valid initialised store at path and immediately closes
// it, leaving a well-formed SQLite file on disk.
func SeedStore(t *testing.T, path string) {
	t.Helper()
	s, err := store.OpenOrInit(path)
	if err != nil {
		t.Fatalf("SeedStore OpenOrInit(%q): %v", path, err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("SeedStore Close: %v", err)
	}
}

// OpenStoreWithRow inserts a single Spawn row at the requested state and
// relay_mode into a fresh temp store and returns the open store and its DB path.
// The store is registered for cleanup via t.Cleanup.
func OpenStoreWithRow(t *testing.T, id, sessionName, state, relayMode string) (*store.Store, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	s, err := store.OpenOrInit(dbPath)
	if err != nil {
		t.Fatalf("OpenStoreWithRow: store.OpenOrInit: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.InsertPending(store.Spawn{
		ClaudeInstanceID: id,
		CWD:              "/tmp",
		TmuxSessionName:  sessionName,
		RelayMode:        relayMode,
	}); err != nil {
		t.Fatalf("OpenStoreWithRow: InsertPending: %v", err)
	}
	if state != store.StatePending {
		if err := s.ApplyHookTransition(id, state, false); err != nil {
			t.Fatalf("OpenStoreWithRow: ApplyHookTransition(%s→%s): %v", id, state, err)
		}
	}
	return s, dbPath
}
