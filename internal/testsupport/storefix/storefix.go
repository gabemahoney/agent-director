// Package storefix provides reusable test helpers for the store layer.
// It is an internal test-support package; nothing in the production code
// graph imports it. Tests in pkg/api and test/smoke/go import it to get
// deterministic, isolated SQLite stores without touching ~/.agent-director.
package storefix

import (
	"path/filepath"
	"testing"

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
