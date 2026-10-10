// Package storefix provides reusable test helpers for the store layer.
// It is an internal test-support package; nothing in the production code
// graph imports it. Tests in pkg/api and test/smoke/go import it to get
// deterministic, isolated SQLite stores without touching ~/.agent-director.
package storefix

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/writefailfix"
)

// Canonical UUIDv4 test request_token values per SR-9.1 / SR-9.4. Tests should
// reference these named constants rather than inline magic strings so the
// vocabulary is consistent across packages.
const (
	TestRequestTokenA = "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa"
	TestRequestTokenB = "bbbbbbbb-bbbb-4bbb-bbbb-bbbbbbbbbbbb"
	TestRequestTokenC = "cccccccc-cccc-4ccc-accc-cccccccccccc"
)

// OpenTempStore opens a *store.Store under t.TempDir() and registers a
// Cleanup that closes it. It returns both the store and the resolved DB
// path so callers that need a raw sql.DB can re-open the file directly
// (e.g. schema tests that check PRAGMA values via database/sql).
//
// The store is always created via OpenOrInit, so the schema is applied on
// first call, and registered (RegisterStorePath) for the seeders that take
// only the handle. Helpers use t.TempDir() exclusively and never touch
// ~/.agent-director.
func OpenTempStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := store.OpenOrInit(path)
	if err != nil {
		t.Fatalf("storefix.OpenTempStore: OpenOrInit(%q): %v", path, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	RegisterStorePath(t, s, path)
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

// SeedCheckPermission inserts a Spawn in StateCheckPermission with relay_mode=on
// and writes an open permission_requests row for it. Use this as the precondition
// for the decide verb, which requires relay_mode=on and an undecided request.
// Both writes are gated hook writes played by the row's own agent through a
// seed pane (WithSeedPane), removed afterwards: the row records no pane and no
// process identity. s must come from OpenTempStore.
func SeedCheckPermission(t *testing.T, s *store.Store, id string) store.Spawn {
	t.Helper()
	sp := defaultSpawn(id)
	sp.RelayMode = "on"
	if err := s.InsertPending(sp); err != nil {
		t.Fatalf("storefix.SeedCheckPermission: InsertPending(%q): %v", id, err)
	}
	dbPath := StorePath(t, s, "storefix.SeedCheckPermission")
	if err := WithSeedPane(dbPath, id, func(gate store.HookGate) error {
		if err := SeedAgentWrites(s, gate, id, "", store.StateCheckPermission); err != nil {
			return err
		}
		applied, err := s.UpsertOpenPermissionRequest(id, gate, TestRequestTokenA, "Bash", `{"cmd":"echo hello"}`, 0, store.WriterProcessHook)
		return seedWriteErr("open permission request", applied, err)
	}); err != nil {
		t.Fatalf("storefix.SeedCheckPermission(%q): %v", id, err)
	}
	row, err := s.GetSpawn(id)
	if err != nil {
		t.Fatalf("storefix.SeedCheckPermission: GetSpawn(%q): %v", id, err)
	}
	return row
}

// SeedResumable inserts a Spawn in StateEnded with a claude_session_id
// (recorded by the agent's gated writes through a seed pane, removed
// afterwards, as for SeedCheckPermission; row_version 2) and a placeholder transcript at spawn.JsonlPath under HOME,
// which must be a temp directory, so resume's pre-flight passes. s must come
// from OpenTempStore.
func SeedResumable(t *testing.T, s *store.Store, id string) store.Spawn {
	t.Helper()
	sp := defaultSpawn(id)
	if err := s.InsertPending(sp); err != nil {
		t.Fatalf("storefix.SeedResumable: InsertPending(%q): %v", id, err)
	}
	sessionID := "sess-" + id
	dbPath := StorePath(t, s, "storefix.SeedResumable")
	if err := WithSeedPane(dbPath, id, func(gate store.HookGate) error {
		return SeedAgentWrites(s, gate, id, sessionID, store.StateEnded)
	}); err != nil {
		t.Fatalf("storefix.SeedResumable(%q): %v", id, err)
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

// SeedClosedPermissionRequests seeds n requests for instanceID (creating its
// row when absent) through the agent's gated insert, denies each
// (DecisionReasonOperator), and backdates row i's decided_at to
// baseTime+i*step through a raw connection to dbPath, for cap-eviction tests.
// It returns the tokens in insertion order.
func SeedClosedPermissionRequests(t *testing.T, s *store.Store, dbPath, instanceID string, n int, baseTime time.Time, step time.Duration) []string {
	t.Helper()

	// Ensure spawn row exists (permission_requests FK → spawns).
	if err := s.InsertPending(defaultSpawn(instanceID)); err != nil {
		if _, getErr := s.GetSpawn(instanceID); getErr != nil {
			t.Fatalf("storefix.SeedClosedPermissionRequests: ensure spawn %q: InsertPending: %v; GetSpawn: %v", instanceID, err, getErr)
		}
		// Spawn already exists — proceed.
	}

	tokens := make([]string, 0, n)
	for i := 0; i < n; i++ {
		// UUIDv4-shaped token: version nibble=4, variant nibble=a (10xx binary).
		tok := fmt.Sprintf("%08x-0000-4000-a000-%012x", i, i)

		if err := WithSeedPane(dbPath, instanceID, func(gate store.HookGate) error {
			applied, err := s.UpsertOpenPermissionRequest(instanceID, gate, tok, "Bash", `{"cmd":"echo"}`, 0, store.WriterProcessHook)
			return seedWriteErr("open permission request "+tok, applied, err)
		}); err != nil {
			t.Fatalf("storefix.SeedClosedPermissionRequests(%q, %q): %v", instanceID, tok, err)
		}
		updated, err := s.DecidePermissionRequest(instanceID, tok, "deny", store.DecisionReasonOperator, store.WriterProcessDecide)
		if err != nil {
			t.Fatalf("storefix.SeedClosedPermissionRequests: DecidePermissionRequest(%q, %q): %v", instanceID, tok, err)
		}
		if !updated {
			t.Fatalf("storefix.SeedClosedPermissionRequests: DecidePermissionRequest(%q, %q) returned updated=false", instanceID, tok)
		}
		tokens = append(tokens, tok)
	}

	// Backdate decided_at for all rows via a single raw connection — the only
	// way to set controlled timestamps without modifying production store methods.
	raw, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("storefix.SeedClosedPermissionRequests: open raw db %q: %v", dbPath, err)
	}
	defer func() { _ = raw.Close() }()

	for i, tok := range tokens {
		decidedAt := baseTime.Add(time.Duration(i) * step).UTC().Format("2006-01-02 15:04:05")
		if _, err := raw.Exec(
			`UPDATE permission_requests SET decided_at = ? WHERE claude_instance_id = ? AND request_token = ?`,
			decidedAt, instanceID, tok,
		); err != nil {
			t.Fatalf("storefix.SeedClosedPermissionRequests: backdate decided_at for (%q, %q): %v", instanceID, tok, err)
		}
	}

	return tokens
}

// SeedUndeliverablePermissionRequest backdates the created_at of the open
// request (instanceID, requestToken) to now-age through a raw connection to
// dbPath (the store exposes no created_at write), so
// api.RelayRequestUndeliverable reads it as undeliverable when age exceeds the
// effective relay timeout; other open rows of the spawn stay deliverable
// (SR-7.3). Seed the open row first (SeedCheckPermission uses
// TestRequestTokenA, or SeedOpenPermissionRequests).
func SeedUndeliverablePermissionRequest(t *testing.T, s *store.Store, dbPath, instanceID, requestToken string, age time.Duration) {
	t.Helper()

	// Confirm the target row exists and is still open before backdating, so a
	// mis-wired test fails with a clear message rather than silently updating
	// zero rows.
	pr, err := s.GetPermissionRequest(instanceID, requestToken)
	if err != nil {
		t.Fatalf("storefix.SeedUndeliverablePermissionRequest: GetPermissionRequest(%q, %q): %v (seed the open row first via SeedCheckPermission/SeedOpenPermissionRequests)", instanceID, requestToken, err)
	}
	if pr.Decision != "" {
		t.Fatalf("storefix.SeedUndeliverablePermissionRequest: (%q, %q) is already decided (%q); cannot make a closed row undeliverable", instanceID, requestToken, pr.Decision)
	}

	raw, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("storefix.SeedUndeliverablePermissionRequest: open raw db %q: %v", dbPath, err)
	}
	defer func() { _ = raw.Close() }()

	backdate := time.Now().UTC().Add(-age).Format("2006-01-02 15:04:05")
	res, err := raw.Exec(
		`UPDATE permission_requests SET created_at = ? WHERE claude_instance_id = ? AND request_token = ? AND decision IS NULL`,
		backdate, instanceID, requestToken,
	)
	if err != nil {
		t.Fatalf("storefix.SeedUndeliverablePermissionRequest: backdate created_at for (%q, %q): %v", instanceID, requestToken, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		t.Fatalf("storefix.SeedUndeliverablePermissionRequest: RowsAffected for (%q, %q): %v", instanceID, requestToken, err)
	}
	if affected != 1 {
		t.Fatalf("storefix.SeedUndeliverablePermissionRequest: backdate for (%q, %q) affected %d rows, want 1", instanceID, requestToken, affected)
	}
}

// SeedOpenPermissionRequests inserts one open request per token for
// instanceID through the agent's gated insert (WithSeedPane). s must come from
// OpenTempStore (or be registered).
func SeedOpenPermissionRequests(t *testing.T, s *store.Store, instanceID string, tokens []string) {
	t.Helper()
	dbPath := StorePath(t, s, "storefix.SeedOpenPermissionRequests")
	if err := WithSeedPane(dbPath, instanceID, func(gate store.HookGate) error {
		for _, tok := range tokens {
			applied, err := s.UpsertOpenPermissionRequest(instanceID, gate, tok, "Bash", `{"cmd":"echo"}`, 0, store.WriterProcessHook)
			if err := seedWriteErr("open permission request "+tok, applied, err); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("storefix.SeedOpenPermissionRequests(%q): %v", instanceID, err)
	}
}

// SeedRelayRequest records req on instanceID as the relay hook's first write
// does (b.146 rule 1): through the gated InsertRelayRequest of the row's own
// agent (WithSeedPane), which moves the row to check_permission and inserts
// the request with req's hook identity, tool_use_id, agent_id and settle
// instant. s must come from OpenTempStore (or be registered).
func SeedRelayRequest(t *testing.T, s *store.Store, instanceID string, req store.RelayRequest) {
	t.Helper()
	dbPath := StorePath(t, s, "storefix.SeedRelayRequest")
	if err := WithSeedPane(dbPath, instanceID, func(gate store.HookGate) error {
		_, applied, err := s.InsertRelayRequest(instanceID, gate, req, 0, store.DefaultLockWait)
		return seedWriteErr("relay request "+req.RequestToken, applied, err)
	}); err != nil {
		t.Fatalf("storefix.SeedRelayRequest(%q): %v", instanceID, err)
	}
}

// SeedAgentDirectorDir creates homeDir's .agent-director/templates directory
// (mode 0700) and returns it, for a test that needs it before make-template,
// which otherwise creates it itself.
func SeedAgentDirectorDir(t *testing.T, homeDir string) string {
	t.Helper()
	tmplDir := filepath.Join(homeDir, ".agent-director", "templates")
	if err := os.MkdirAll(tmplDir, 0o700); err != nil {
		t.Fatalf("storefix.SeedAgentDirectorDir: MkdirAll(%q): %v", tmplDir, err)
	}
	return tmplDir
}

// WriteFailureKind names one kind of store write InjectWriteFailure makes
// fail. It is writefailfix.Kind, whose constants document which other
// existing writes each kind also matches.
type WriteFailureKind = writefailfix.Kind

// The four reuse kinds of SR-20.3, the launch identity write kind (SR-3.6)
// and the permission decision kind (b.146 rule 12), re-exported from
// writefailfix. WriteFailReuseRestore also makes a plain spawn's end write
// after "duplicate session" (store.EndHeldLaunch, SR-5.8) fail;
// WriteFailPermissionDecision makes find-missing's close, and so its whole
// mark, fail, and a pane answer's sent write (b.146 rule 8), which records
// its decision once its key was sent.
const (
	WriteFailReuseArchive          = writefailfix.ReuseArchive
	WriteFailReuseReset            = writefailfix.ReuseReset
	WriteFailReusePermissionDelete = writefailfix.ReusePermissionDelete
	WriteFailReuseRestore          = writefailfix.ReuseRestore
	WriteFailLaunchIdentity        = writefailfix.LaunchIdentityWrite
	WriteFailPermissionDecision    = writefailfix.PermissionDecision
)

// InjectWriteFailure makes one kind of write to instanceID's rows fail with a
// store error (SR-20.3, SRD-RR2 T4): the only way tests, internal/store's
// own included, make the concrete *store.Store's writes fail by kind. Through a second raw
// connection to dbPath (the temp store file the test already holds) it
// installs writefailfix's trigger for kind, scoped to instanceID, so other
// rows, other ids and cleanup are unaffected; the test's cleanup removes it.
// Seed the row first: several kinds also match seeding writes.
func InjectWriteFailure(t *testing.T, dbPath string, kind WriteFailureKind, instanceID string) {
	t.Helper()
	raw := openRawStore(t, dbPath, "InjectWriteFailure")
	h, err := writefailfix.Install(raw, kind, instanceID)
	_ = raw.Close()
	if err != nil {
		t.Fatalf("storefix.InjectWriteFailure(%v, %q): %v", kind, instanceID, err)
	}
	t.Cleanup(func() {
		raw := openRawStore(t, dbPath, "InjectWriteFailure cleanup")
		defer func() { _ = raw.Close() }()
		if err := h.Remove(raw); err != nil {
			t.Errorf("storefix.InjectWriteFailure cleanup (%v, %q): %v", kind, instanceID, err)
		}
	})
}

// openRawStore opens a second raw connection to an existing store file,
// never creating one, with the store's busy timeout.
func openRawStore(t *testing.T, dbPath, caller string) *sql.DB {
	t.Helper()
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("storefix.%s: store file %q: %v", caller, dbPath, err)
	}
	raw, err := sql.Open("sqlite", fmt.Sprintf("%s?_pragma=busy_timeout(%d)", dbPath, store.DefaultBusyTimeoutMs))
	if err != nil {
		t.Fatalf("storefix.%s: open raw db %q: %v", caller, dbPath, err)
	}
	return raw
}
