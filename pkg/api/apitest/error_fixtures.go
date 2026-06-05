package apitest

// error_fixtures.go provides error-precondition seeders for the
// envelope-diff error-path harness (test/envelope-diff/error_cases.go).
//
// Each seeder named SeedErr<Name> establishes the exact DB state (or
// filesystem state) that triggers a specific documented error name when the
// corresponding verb is invoked. Seeders are self-contained: calling a seeder
// twice from independent sub-tests produces independent stores with no shared
// state.
//
// Return convention: (store, dbPath) where dbPath = <tempDir>/state.db and
// tempDir is also the srcDir for copyFixtureStore. The *store.Store is
// registered for t.Cleanup; callers do not need to close it themselves.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
)

// openErrStore creates a fresh temp store, registers a cleanup, and returns
// the open handle and its db path.
func openErrStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	s, err := store.OpenOrInit(dbPath)
	if err != nil {
		t.Fatalf("openErrStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, dbPath
}

// insertErrRow inserts a single spawn row at the given state and relay_mode.
func insertErrRow(t *testing.T, s *store.Store, id, state, relayMode string) {
	t.Helper()
	if err := s.InsertPending(store.Spawn{
		ClaudeInstanceID: id,
		CWD:              "/tmp",
		TmuxSessionName:  "cd-" + id,
		RelayMode:        relayMode,
	}); err != nil {
		t.Fatalf("insertErrRow: InsertPending %s: %v", id, err)
	}
	if state != store.StatePending {
		if err := s.ApplyHookTransition(id, state, false); err != nil {
			t.Fatalf("insertErrRow: ApplyHookTransition %s→%s: %v", id, state, err)
		}
	}
}

// SeedErrSpawnNotFound returns an empty store. Callers invoke verbs with a
// non-existent claude_instance_id to trigger ErrSpawnNotFound.
func SeedErrSpawnNotFound(t *testing.T) (*store.Store, string) {
	t.Helper()
	return openErrStore(t)
}

// SeedErrSpawnNotInteractive returns a store with one ended-state spawn
// (id="id-err-ni-1"). Invoking send-keys on this spawn triggers
// ErrSpawnNotInteractive because the state is not a live conversational
// state.
func SeedErrSpawnNotInteractive(t *testing.T) (*store.Store, string) {
	t.Helper()
	s, dbPath := openErrStore(t)
	insertErrRow(t, s, "id-err-ni-1", store.StateEnded, "off")
	return s, dbPath
}

// SeedErrJsonlMissing returns a store with one ended spawn whose
// claude_session_id is set (id="id-err-jm-1", sessionID="sess-err-jm-1") but
// whose JSONL transcript file does not exist on disk. resume on this spawn
// triggers ErrJsonlMissing because spawn.JsonlPath stat-fails on the
// absent file. The calling test must NOT create the JSONL under HOME.
func SeedErrJsonlMissing(t *testing.T) (*store.Store, string) {
	t.Helper()
	s, dbPath := openErrStore(t)
	const id = "id-err-jm-1"
	insertErrRow(t, s, id, store.StateEnded, "off")
	if err := s.SetSessionID(id, "sess-err-jm-1"); err != nil {
		t.Fatalf("SeedErrJsonlMissing: SetSessionID: %v", err)
	}
	return s, dbPath
}

// SeedErrSpawnNotResumable returns a store with one live (waiting) spawn
// (id="id-err-nr-1"). resume on this spawn triggers ErrSpawnNotResumable
// because only ended/missing rows are resumable.
func SeedErrSpawnNotResumable(t *testing.T) (*store.Store, string) {
	t.Helper()
	s, dbPath := openErrStore(t)
	insertErrRow(t, s, "id-err-nr-1", store.StateWaiting, "off")
	return s, dbPath
}

// SeedErrNoOpenPermissionRequest returns a store with one relay_mode=on spawn
// in check_permission state (id="id-err-nopr-1") with no open
// permission_requests row. decide on this spawn triggers
// ErrNoOpenPermissionRequest.
func SeedErrNoOpenPermissionRequest(t *testing.T) (*store.Store, string) {
	t.Helper()
	s, dbPath := openErrStore(t)
	insertErrRow(t, s, "id-err-nopr-1", store.StateCheckPermission, "on")
	return s, dbPath
}

// SeedErrAlreadyDecided returns a store with one relay_mode=on spawn in
// check_permission state (id="id-err-ad-1") with a permission_requests row
// that already carries a non-NULL decision. decide on this spawn triggers
// ErrAlreadyDecided because the first-call-wins UPDATE finds no open row.
func SeedErrAlreadyDecided(t *testing.T) (*store.Store, string) {
	t.Helper()
	s, dbPath := openErrStore(t)
	const id = "id-err-ad-1"
	insertErrRow(t, s, id, store.StateCheckPermission, "on")
	if err := s.UpsertOpenPermissionRequest(id, storefix.TestRequestTokenA, "Bash", `{"cmd":"echo"}`, 0, store.WriterProcessHook); err != nil {
		t.Fatalf("SeedErrAlreadyDecided: UpsertOpenPermissionRequest: %v", err)
	}
	// Pre-decide the row so the next decide() sees RowsAffected==0 and
	// follows the ErrAlreadyDecided branch.
	if _, err := s.DecidePermissionRequest(id, storefix.TestRequestTokenA, "allow", "pre-decided", store.WriterProcessDecide); err != nil {
		t.Fatalf("SeedErrAlreadyDecided: DecidePermissionRequest: %v", err)
	}
	return s, dbPath
}

// SeedErrRelayModeOff returns a store with one relay_mode=off spawn in
// check_permission state (id="id-err-rmo-1"). decide on this spawn triggers
// ErrRelayModeOff because only relay_mode=on spawns support the decide verb.
func SeedErrRelayModeOff(t *testing.T) (*store.Store, string) {
	t.Helper()
	s, dbPath := openErrStore(t)
	insertErrRow(t, s, "id-err-rmo-1", store.StateCheckPermission, "off")
	return s, dbPath
}

// SeedErrListInvalidLabel returns an empty store. list invoked with a label
// value that lacks a '=' separator triggers ErrListInvalidLabel before any
// DB read.
func SeedErrListInvalidLabel(t *testing.T) (*store.Store, string) {
	t.Helper()
	return openErrStore(t)
}

// SeedErrTemplateExists returns (srcDir, templateName). It creates a valid
// empty store at srcDir/state.db AND creates srcDir/templates/<name>.toml
// with a minimal valid TOML body. When copyFixtureStore copies srcDir into
// homeDir/.agent-director/, the template file lands at
// homeDir/.agent-director/templates/<name>.toml so that make-template with
// that name triggers ErrTemplateExists.
//
// Note on usage: ErrTemplateExists's err_description embeds the full absolute
// path of the pre-existing file. On Linux, paths contain no ':', so the
// prefix-match policy falls through to full-string equality. Because the CLI
// and Client copies live in independent temp homedirs the absolute paths
// differ and the envelope-diff assertion fails. See error_cases.go for the
// comment explaining why ErrTemplateExists is omitted from the error table
// while this seeder is retained for future use.
func SeedErrTemplateExists(t *testing.T) (srcDir, templateName string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	SeedStore(t, dbPath)
	// Create the pre-existing template inside srcDir so copyFixtureStore
	// propagates it to homeDir/.agent-director/templates/.
	const name = "err-tmpl"
	templDir := filepath.Join(filepath.Dir(dbPath), "templates")
	if err := os.MkdirAll(templDir, 0o700); err != nil {
		t.Fatalf("SeedErrTemplateExists: mkdir templates: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(templDir, name+".toml"),
		[]byte("cwd = \"/tmp\"\n"),
		0o600,
	); err != nil {
		t.Fatalf("SeedErrTemplateExists: write template file: %v", err)
	}
	return filepath.Dir(dbPath), name
}

// SeedEmptyStore creates a valid initialised store at a fresh temp path,
// closes it, and returns the dbPath. Use filepath.Dir(dbPath) as srcDir for
// copyFixtureStore when no DB rows are required.
//
// This is a convenience wrapper around SeedStore for callers that need only
// a path (not an open *store.Store handle).
func SeedEmptyStore(t *testing.T) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	SeedStore(t, dbPath)
	return dbPath
}

// SeedErrTemplateNameUnsafe returns an empty store. make-template invoked
// with name="../evil" (path-traversal) triggers ErrTemplateNameUnsafe during
// parameter validation before any file I/O.
func SeedErrTemplateNameUnsafe(t *testing.T) (*store.Store, string) {
	t.Helper()
	return openErrStore(t)
}

// SeedErrCwdMissing returns an empty store. spawn invoked with an empty cwd
// triggers ErrCwdMissing during parameter validation.
func SeedErrCwdMissing(t *testing.T) (*store.Store, string) {
	t.Helper()
	return openErrStore(t)
}

// SeedErrSpawnNotPausable returns a store with one pending-state spawn
// (id="id-err-np-1"). pause on this spawn triggers ErrSpawnNotPausable
// because only waiting-state spawns are pausable (ended/missing are no-op
// success; all other states reject). pending is the state immediately after
// InsertPending with no hook transition — the simplest non-pausable state.
func SeedErrSpawnNotPausable(t *testing.T) (*store.Store, string) {
	t.Helper()
	s, dbPath := openErrStore(t)
	// StatePending: InsertPending with no subsequent ApplyHookTransition.
	insertErrRow(t, s, "id-err-np-1", store.StatePending, "off")
	return s, dbPath
}
