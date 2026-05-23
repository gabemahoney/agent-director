// Package storefix provides reusable test helpers for the store layer.
// It is an internal test-support package; nothing in the production code
// graph imports it. Tests in pkg/api and test/smoke/go import it to get
// deterministic, isolated SQLite stores without touching ~/.agent-director.
package storefix

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
)

// OpenTempStore opens a *store.Store under t.TempDir() and registers a
// Cleanup that closes it. It returns both the store and the resolved DB
// path so callers that need a raw sql.DB can re-open the file directly
// (e.g. schema tests that check PRAGMA values via database/sql).
//
// The store is always created via OpenOrInit, so the schema is applied on
// first call. Helpers use t.TempDir() exclusively and never touch
// ~/.agent-director.
func OpenTempStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := store.OpenOrInit(path)
	if err != nil {
		t.Fatalf("storefix.OpenTempStore: OpenOrInit(%q): %v", path, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, path
}

// defaultSpawn returns a minimal valid store.Spawn with the given id. The
// returned value is suitable for InsertPending. Callers that need non-
// default fields can mutate the struct before handing it to a seeder.
func defaultSpawn(id string) store.Spawn {
	return store.Spawn{
		ClaudeInstanceID: id,
		State:            store.StatePending,
		CWD:              "/tmp",
		TmuxSessionName:  "sess-" + id,
		RelayMode:        "off",
	}
}

// seed inserts a spawn row and then transitions it to targetState.
// It uses InsertPending followed by ApplyHookTransition so the row ends up
// in any desired state without exposing raw SQL to callers.
func seed(t *testing.T, s *store.Store, id, targetState string) store.Spawn {
	t.Helper()
	sp := defaultSpawn(id)
	if err := s.InsertPending(sp); err != nil {
		t.Fatalf("storefix.seed: InsertPending(%q): %v", id, err)
	}
	if targetState != store.StatePending {
		if err := s.ApplyHookTransition(id, targetState, false); err != nil {
			t.Fatalf("storefix.seed: ApplyHookTransition(%q, %q): %v", id, targetState, err)
		}
	}
	row, err := s.GetSpawn(id)
	if err != nil {
		t.Fatalf("storefix.seed: GetSpawn(%q): %v", id, err)
	}
	return row
}

// SeedSpawn inserts a Spawn in StateWorking — a live, interactive row
// suitable for send-keys, kill, and list examples. Returns the fetched row.
func SeedSpawn(t *testing.T, s *store.Store, id string) store.Spawn {
	t.Helper()
	return seed(t, s, id, store.StateWorking)
}

// SeedKilled inserts a Spawn in StateEnded — a terminal row representing a
// completed or killed instance. Useful for status/list examples that show
// the full lifecycle.
func SeedKilled(t *testing.T, s *store.Store, id string) store.Spawn {
	t.Helper()
	return seed(t, s, id, store.StateEnded)
}

// SeedPaused inserts a Spawn in StateWaiting — a live but idle row. Useful
// for list examples showing a mix of states, or for send-keys examples that
// need a waiting Spawn.
func SeedPaused(t *testing.T, s *store.Store, id string) store.Spawn {
	t.Helper()
	return seed(t, s, id, store.StateWaiting)
}

// SeedAskUser inserts a Spawn in StateAskUser — a row blocked on human input.
// Useful for list and status examples demonstrating the ask_user state.
func SeedAskUser(t *testing.T, s *store.Store, id string) store.Spawn {
	t.Helper()
	return seed(t, s, id, store.StateAskUser)
}

// SeedLiveSpawn inserts a Spawn in StateWorking — an active, fully-booted row
// suitable as a precondition for status, get, kill, send-keys, and read-pane.
// It is the standard "live Spawn" fixture when any interactive Spawn will do.
func SeedLiveSpawn(t *testing.T, s *store.Store, id string) store.Spawn {
	t.Helper()
	return seed(t, s, id, store.StateWorking)
}

// SeedCheckPermission inserts a Spawn in StateCheckPermission with relay_mode=on
// and writes an open permission_requests row for it. Use this as the precondition
// for the decide verb, which requires relay_mode=on and an undecided request.
func SeedCheckPermission(t *testing.T, s *store.Store, id string) store.Spawn {
	t.Helper()
	sp := defaultSpawn(id)
	sp.RelayMode = "on"
	if err := s.InsertPending(sp); err != nil {
		t.Fatalf("storefix.SeedCheckPermission: InsertPending(%q): %v", id, err)
	}
	if err := s.ApplyHookTransition(id, store.StateCheckPermission, false); err != nil {
		t.Fatalf("storefix.SeedCheckPermission: ApplyHookTransition(%q, check_permission): %v", id, err)
	}
	if err := s.UpsertOpenPermissionRequest(id, "Bash", `{"cmd":"echo hello"}`); err != nil {
		t.Fatalf("storefix.SeedCheckPermission: UpsertOpenPermissionRequest(%q): %v", id, err)
	}
	row, err := s.GetSpawn(id)
	if err != nil {
		t.Fatalf("storefix.SeedCheckPermission: GetSpawn(%q): %v", id, err)
	}
	return row
}

// SeedResumable inserts a Spawn in StateEnded with claude_session_id populated
// and writes a minimal JSONL placeholder so that the resume verb's pre-flight
// Stat check passes. The JSONL path is derived from HOME via spawn.JsonlPath;
// callers must ensure HOME points at a temp directory before calling this
// (smoke tests do this in TestMain).
//
// Returns the seeded Spawn (with ClaudeSessionID and EndedAt populated).
func SeedResumable(t *testing.T, s *store.Store, id string) store.Spawn {
	t.Helper()
	sp := defaultSpawn(id)
	if err := s.InsertPending(sp); err != nil {
		t.Fatalf("storefix.SeedResumable: InsertPending(%q): %v", id, err)
	}
	sessionID := "sess-" + id
	if err := s.SetSessionID(id, sessionID); err != nil {
		t.Fatalf("storefix.SeedResumable: SetSessionID(%q, %q): %v", id, sessionID, err)
	}
	if err := s.ApplyHookTransition(id, store.StateEnded, false); err != nil {
		t.Fatalf("storefix.SeedResumable: ApplyHookTransition(%q, ended): %v", id, err)
	}
	// Write a placeholder JSONL file so resume's os.Stat pre-flight passes.
	// spawn.JsonlPath resolves under HOME; TestMain must redirect HOME first.
	jsonlPath, err := spawn.JsonlPath(sp.CWD, sessionID)
	if err != nil {
		t.Fatalf("storefix.SeedResumable: JsonlPath(%q, %q): %v", sp.CWD, sessionID, err)
	}
	if err := os.MkdirAll(filepath.Dir(jsonlPath), 0o700); err != nil {
		t.Fatalf("storefix.SeedResumable: mkdir JSONL parent %q: %v", filepath.Dir(jsonlPath), err)
	}
	if err := os.WriteFile(jsonlPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("storefix.SeedResumable: write JSONL %q: %v", jsonlPath, err)
	}
	row, err := s.GetSpawn(id)
	if err != nil {
		t.Fatalf("storefix.SeedResumable: GetSpawn(%q): %v", id, err)
	}
	return row
}

// SeedExpiredCandidate inserts a Spawn in StateEnded and then backdates its
// ended_at column by age so the row qualifies for expiry under the default
// retention window. dbPath must be the SQLite file path returned by
// OpenTempStore — a second raw connection is used to issue the UPDATE since
// the store API does not expose a backdating method.
//
// Precondition: the expire verb uses ended_at < deadline; a duration of
// 8 * 24 * time.Hour exceeds the typical 7-day default retention.
func SeedExpiredCandidate(t *testing.T, s *store.Store, dbPath, id string, age time.Duration) store.Spawn {
	t.Helper()
	_ = seed(t, s, id, store.StateEnded)

	// Backdate ended_at via a raw connection — the only way to simulate
	// elapsed time without modifying production store methods.
	raw, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("storefix.SeedExpiredCandidate: open raw db %q: %v", dbPath, err)
	}
	defer func() { _ = raw.Close() }()

	backdate := time.Now().UTC().Add(-age).Format("2006-01-02 15:04:05")
	if _, err := raw.Exec(
		`UPDATE spawns SET ended_at = ? WHERE claude_instance_id = ?`,
		backdate, id,
	); err != nil {
		t.Fatalf("storefix.SeedExpiredCandidate: backdate ended_at for %q: %v", id, err)
	}

	// Re-fetch via the store so the returned row reflects the updated ended_at.
	row, err := s.GetSpawn(id)
	if err != nil {
		t.Fatalf("storefix.SeedExpiredCandidate: GetSpawn(%q): %v", id, err)
	}
	return row
}

// SeedAgentDirectorDir creates the ~/.agent-director/templates/ directory
// hierarchy under homeDir and returns the templates directory path. Use it as
// the make-template precondition when HOME is re-pointed to a temp directory
// (e.g. in smoke TestMain). The directory is created with mode 0700.
//
// Note: make-template itself calls config.EnsureTemplatesDir which also creates
// the directory lazily — this helper is only needed when you want the directory
// to exist before the verb runs (e.g. to verify the pre-condition separately
// from the verb under test).
func SeedAgentDirectorDir(t *testing.T, homeDir string) string {
	t.Helper()
	tmplDir := filepath.Join(homeDir, ".agent-director", "templates")
	if err := os.MkdirAll(tmplDir, 0o700); err != nil {
		t.Fatalf("storefix.SeedAgentDirectorDir: MkdirAll(%q): %v", tmplDir, err)
	}
	return tmplDir
}
