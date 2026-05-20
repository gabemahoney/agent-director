package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// openTestStore opens a fresh on-disk SQLite store under t.TempDir().
// Tests use a real *sql.DB rather than mocks so the permission_requests
// invariants (FK, UNIQUE, transactional DELETE+INSERT) are exercised
// end to end. The store is cleaned up on test exit.
func openTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// seedSpawnForPerm inserts a minimal spawn row so permission_requests'
// FK has a target. The id is the only thing callers care about.
func seedSpawnForPerm(t *testing.T, s *Store, id, relayMode string) {
	t.Helper()
	if err := s.InsertPending(Spawn{
		ClaudeInstanceID: id,
		CWD:              "/tmp",
		TmuxSessionName:  "cd-test-" + id,
		RelayMode:        relayMode,
	}); err != nil {
		t.Fatalf("InsertPending(%s): %v", id, err)
	}
}

// readPermRow is a direct-DB read that returns the row's request_id,
// tool_name, tool_input, and the RAW decision/decision_reason columns
// (NULL-preserving). Used so tests can distinguish a NULL column from
// an empty-string column — the public GetPermissionRequest COALESCEs
// both to "" and would mask the difference.
func readPermRow(t *testing.T, s *Store, instanceID string) (requestID int64, toolName, toolInput string, decision, reason sql.NullString) {
	t.Helper()
	row := s.db.QueryRow(`
		SELECT request_id, tool_name, tool_input, decision, decision_reason
		  FROM permission_requests
		 WHERE claude_instance_id = ?
	`, instanceID)
	if err := row.Scan(&requestID, &toolName, &toolInput, &decision, &reason); err != nil {
		t.Fatalf("scan permission_requests for %s: %v", instanceID, err)
	}
	return
}

// countPermRows returns how many permission_requests rows match the
// given instance id — pins the UNIQUE invariant after an upsert.
func countPermRows(t *testing.T, s *Store, instanceID string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`
		SELECT COUNT(*) FROM permission_requests WHERE claude_instance_id = ?
	`, instanceID).Scan(&n); err != nil {
		t.Fatalf("count permission_requests for %s: %v", instanceID, err)
	}
	return n
}

// TestUpsertOpenPermissionRequestReplacesPriorRow pins the SRD §6.2
// invariant: at most one outstanding request per Spawn. A second
// upsert against the same claude_instance_id must replace (not stack)
// the prior row, and because the impl is DELETE-then-INSERT the new
// row gets a FRESH AUTOINCREMENT request_id — a future
// `INSERT OR REPLACE` refactor would land here too and the test still
// passes; what the test really catches is "two rows now exist" or
// "request_id didn't change" — the latter would mean the row was
// updated in place (which would silently let `decision` persist from
// a prior decide, breaking the polling loop's preemption detection).
func TestUpsertOpenPermissionRequestReplacesPriorRow(t *testing.T) {
	s := openTestStore(t)
	const id = "spawn-1"
	seedSpawnForPerm(t, s, id, "on")

	if err := s.UpsertOpenPermissionRequest(id, "tool_A", `{"a":1}`); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	reqIDA, _, _, _, _ := readPermRow(t, s, id)

	if err := s.UpsertOpenPermissionRequest(id, "tool_B", `{"b":2}`); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	if got := countPermRows(t, s, id); got != 1 {
		t.Fatalf("row count after upsert = %d; want exactly 1 (UNIQUE constraint must hold)", got)
	}

	reqIDB, toolName, toolInput, decision, reason := readPermRow(t, s, id)
	if reqIDB == reqIDA {
		t.Errorf("request_id unchanged across upsert (%d → %d); DELETE+INSERT replaced inserts should produce a new autoincrement id — an UPDATE-in-place refactor would silently retain a prior `decision` and break preemption detection", reqIDA, reqIDB)
	}
	if toolName != "tool_B" || toolInput != `{"b":2}` {
		t.Errorf("after second upsert: tool_name=%q tool_input=%q; want tool_B / {b:2}", toolName, toolInput)
	}
	if decision.Valid || reason.Valid {
		t.Errorf("upsert wrote non-NULL decision/reason (decision.Valid=%v reason.Valid=%v); a fresh row must start un-decided", decision.Valid, reason.Valid)
	}

	// FK cascade: deleting the spawn must drop the permission_requests
	// row too. Mirrors the schema's ON DELETE CASCADE on
	// permission_requests.claude_instance_id.
	if _, err := s.db.Exec(`DELETE FROM spawns WHERE claude_instance_id = ?`, id); err != nil {
		t.Fatalf("delete spawn: %v", err)
	}
	if got := countPermRows(t, s, id); got != 0 {
		t.Errorf("permission_requests row count after spawn delete = %d; want 0 (ON DELETE CASCADE must remove orphan rows)", got)
	}
}

// TestUpsertOpenPermissionRequestProducesNoRowMidTx pins the
// transactional atomicity of the DELETE+INSERT pair. A concurrent
// reader iterating GetPermissionRequest while a writer repeatedly
// upserts must NEVER observe a moment when the row is gone — that
// would be the regression signature of someone splitting the tx into
// two auto-committed statements.
//
// The asymmetry to a regression: if the writer ran DELETE + INSERT
// outside a tx, the reader thread would see sql.ErrNoRows in the
// window between the two statements. We assert no reader iteration
// ever returns sql.ErrNoRows after the initial seed.
func TestUpsertOpenPermissionRequestProducesNoRowMidTx(t *testing.T) {
	s := openTestStore(t)
	const id = "spawn-tx"
	seedSpawnForPerm(t, s, id, "on")
	if err := s.UpsertOpenPermissionRequest(id, "seed", `{}`); err != nil {
		t.Fatalf("seed upsert: %v", err)
	}

	const writes = 200
	var (
		wg          sync.WaitGroup
		stop        atomic.Bool
		noRowSeen   atomic.Bool
		readerReads atomic.Int64
	)

	// Reader: hammer GetPermissionRequest until the writer signals stop.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for !stop.Load() {
			_, err := s.GetPermissionRequest(id)
			readerReads.Add(1)
			if errors.Is(err, sql.ErrNoRows) {
				noRowSeen.Store(true)
				return
			}
			if err != nil {
				t.Errorf("reader: unexpected err: %v", err)
				return
			}
		}
	}()

	// Writer: keep replacing the row.
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer stop.Store(true)
		for i := 0; i < writes; i++ {
			if err := s.UpsertOpenPermissionRequest(id, "tool", `{}`); err != nil {
				t.Errorf("writer iter %d: %v", i, err)
				return
			}
		}
	}()

	wg.Wait()

	if noRowSeen.Load() {
		t.Fatalf("reader observed sql.ErrNoRows during concurrent upsert sequence — DELETE+INSERT is not atomic; was the surrounding tx removed?")
	}
	if readerReads.Load() == 0 {
		t.Fatalf("reader observed zero iterations; concurrency test didn't actually run")
	}
}

// TestDecidePermissionRequestOnlyAffectsOpenRow pins the race-free
// first-call-wins behavior the api.Decide verb depends on. The first
// decide flips decision from NULL → "allow"/"deny" and returns
// updated=true; a second decide on the same row finds decision IS NOT
// NULL, matches no rows, and returns updated=false. The verb layer
// uses that signal to disambiguate ErrAlreadyDecided from
// ErrNoOpenPermissionRequest via a follow-up SELECT.
func TestDecidePermissionRequestOnlyAffectsOpenRow(t *testing.T) {
	s := openTestStore(t)
	const id = "spawn-decide"
	seedSpawnForPerm(t, s, id, "on")
	if err := s.UpsertOpenPermissionRequest(id, "Read", `{"file":"/etc/hosts"}`); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	updated, err := s.DecidePermissionRequest(id, "allow", "trusted")
	if err != nil {
		t.Fatalf("first decide: %v", err)
	}
	if !updated {
		t.Fatalf("first decide returned updated=false; want true (open row should be flipped)")
	}

	updated, err = s.DecidePermissionRequest(id, "deny", "second attempt")
	if err != nil {
		t.Fatalf("second decide: %v", err)
	}
	if updated {
		t.Fatalf("second decide returned updated=true; want false (already-decided row must NOT be re-flipped — first-call-wins per SRD §6.2)")
	}

	// And the row's persisted decision must be the FIRST one — the
	// guard `WHERE decision IS NULL` must prevent the second UPDATE
	// from clobbering reason too.
	_, _, _, decision, reason := readPermRow(t, s, id)
	if !decision.Valid || decision.String != "allow" {
		t.Errorf("decision = (%v, %q); want (valid, allow) — first-call-wins violated", decision.Valid, decision.String)
	}
	if !reason.Valid || reason.String != "trusted" {
		t.Errorf("reason = (%v, %q); want (valid, trusted) — second decide leaked into reason column", reason.Valid, reason.String)
	}
}

// TestDecidePermissionRequestEmptyReasonStored pins the consumer-side
// behavior for decide-with-empty-reason: GetPermissionRequest reports
// DecisionReason as the empty string, never as a special "absent"
// sentinel. The underlying column is intentionally NULL (the envelope
// default "Denied by orchestrator" is applied at envelope-encode time,
// not at DB-write time — see internal/api/decide_test.go for the
// matching api-layer pin), and GetPermissionRequest COALESCEs NULL to
// "" so callers don't have to NULL-check.
func TestDecidePermissionRequestEmptyReasonStored(t *testing.T) {
	s := openTestStore(t)
	const id = "spawn-empty-reason"
	seedSpawnForPerm(t, s, id, "on")
	if err := s.UpsertOpenPermissionRequest(id, "Read", `{}`); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	updated, err := s.DecidePermissionRequest(id, "deny", "")
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if !updated {
		t.Fatalf("decide returned updated=false; want true")
	}

	got, err := s.GetPermissionRequest(id)
	if err != nil {
		t.Fatalf("GetPermissionRequest: %v", err)
	}
	if got.Decision != "deny" {
		t.Errorf("Decision = %q; want deny", got.Decision)
	}
	if got.DecisionReason != "" {
		t.Errorf("DecisionReason = %q; want \"\" (empty reason must surface as empty string via the COALESCE in GetPermissionRequest)", got.DecisionReason)
	}
}

// TestGetPermissionRequestExposesRequestIDAndCreatedAt pins SRD §SR-2.1
// for Epic t1.jm1.61: GetPermissionRequest projects request_id and
// created_at into the returned PermissionRow so api.Get can render them
// on the `permission_request` field of the `get` verb's response.
//
// Cross-checks RequestID against the raw read via readPermRow (the
// shared helper above) to catch a silent drift where the SELECT scans
// the columns in the wrong order. Verifies CreatedAt is non-zero and
// within a sane window of the seed time — pins that the TIMESTAMP
// column scans into time.Time rather than landing as the zero value.
func TestGetPermissionRequestExposesRequestIDAndCreatedAt(t *testing.T) {
	s := openTestStore(t)
	const id = "spawn-fields"
	seedSpawnForPerm(t, s, id, "on")

	before := time.Now().UTC().Add(-1 * time.Second)
	if err := s.UpsertOpenPermissionRequest(id, "Read", `{"file":"/tmp/x"}`); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	after := time.Now().UTC().Add(1 * time.Second)

	rawReqID, _, _, _, _ := readPermRow(t, s, id)

	got, err := s.GetPermissionRequest(id)
	if err != nil {
		t.Fatalf("GetPermissionRequest: %v", err)
	}
	if got.RequestID == 0 {
		t.Errorf("RequestID = 0; want non-zero autoincrement id")
	}
	if got.RequestID != rawReqID {
		t.Errorf("RequestID = %d; raw read returned %d (SELECT projection may be out of order)", got.RequestID, rawReqID)
	}
	if got.CreatedAt.IsZero() {
		t.Errorf("CreatedAt is zero; want a populated TIMESTAMP scanned from the column")
	}
	if got.CreatedAt.Before(before) || got.CreatedAt.After(after) {
		t.Errorf("CreatedAt = %v; want in [%v, %v]", got.CreatedAt, before, after)
	}
	if got.ToolName != "Read" || got.ToolInput != `{"file":"/tmp/x"}` {
		t.Errorf("ToolName/ToolInput drifted: name=%q input=%q", got.ToolName, got.ToolInput)
	}
	if got.Decision != "" || got.DecisionReason != "" {
		t.Errorf("open row surfaced as decided: decision=%q reason=%q", got.Decision, got.DecisionReason)
	}
}
