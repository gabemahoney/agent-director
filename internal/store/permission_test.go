package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// Canonical UUIDv4 request_token constants for white-box store tests.
// Mirrors storefix.TestRequestToken{A,B,C} (same values) but declared here
// to avoid a circular import (package store → storefix → store).
const (
	tokenA = "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa"
	tokenB = "bbbbbbbb-bbbb-4bbb-bbbb-bbbbbbbbbbbb"
	tokenC = "cccccccc-cccc-4ccc-cccc-cccccccccccc"
)

// openTestStore opens a fresh on-disk SQLite store under t.TempDir().
// Tests use a real *sql.DB rather than mocks so permission_requests
// invariants (FK, UNIQUE, transactional INSERT) are exercised end to end.
func openTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := OpenOrInit(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// seedSpawnForPerm inserts a minimal spawn row so permission_requests' FK
// has a target. The id is the only thing callers care about.
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

// readPermRow reads one permission_requests row by (instanceID, requestToken).
// Returns raw NULL-preserving decision/decision_reason columns so tests can
// distinguish NULL from "".
func readPermRow(t *testing.T, s *Store, instanceID, requestToken string) (requestID int64, toolName, toolInput string, decision, reason sql.NullString) {
	t.Helper()
	row := s.db.QueryRow(`
		SELECT request_id, tool_name, tool_input, decision, decision_reason
		  FROM permission_requests
		 WHERE claude_instance_id = ? AND request_token = ?
	`, instanceID, requestToken)
	if err := row.Scan(&requestID, &toolName, &toolInput, &decision, &reason); err != nil {
		t.Fatalf("scan permission_requests for (%s, %s): %v", instanceID, requestToken, err)
	}
	return
}

// countPermRows returns the number of permission_requests rows for the given
// instance (all tokens combined) — used to pin the multi-row append semantics.
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

// TestUpsertOpenPermissionRequestAppendsRow pins the v2 multi-row semantics:
// distinct (instance_id, request_token) pairs produce distinct rows; a
// repeated pair surfaces ErrRequestTokenCollision; the original row is intact.
func TestUpsertOpenPermissionRequestAppendsRow(t *testing.T) {
	s := openTestStore(t)
	const id = "spawn-append"
	seedSpawnForPerm(t, s, id, "on")

	// Two distinct tokens → two rows.
	if err := s.UpsertOpenPermissionRequest(id, tokenA, "tool_A", `{"a":1}`); err != nil {
		t.Fatalf("upsert tokenA: %v", err)
	}
	if err := s.UpsertOpenPermissionRequest(id, tokenB, "tool_B", `{"b":2}`); err != nil {
		t.Fatalf("upsert tokenB: %v", err)
	}
	if got := countPermRows(t, s, id); got != 2 {
		t.Fatalf("row count after two distinct upserts = %d; want 2", got)
	}

	// Each row carries correct fields.
	_, toolName, toolInput, decision, _ := readPermRow(t, s, id, tokenA)
	if toolName != "tool_A" || toolInput != `{"a":1}` {
		t.Errorf("tokenA: tool_name=%q tool_input=%q; want tool_A/{\"a\":1}", toolName, toolInput)
	}
	if decision.Valid {
		t.Errorf("tokenA: decision.Valid=true; fresh row must start undecided")
	}

	_, toolName, toolInput, decision, _ = readPermRow(t, s, id, tokenB)
	if toolName != "tool_B" || toolInput != `{"b":2}` {
		t.Errorf("tokenB: tool_name=%q tool_input=%q; want tool_B/{\"b\":2}", toolName, toolInput)
	}
	if decision.Valid {
		t.Errorf("tokenB: decision.Valid=true; fresh row must start undecided")
	}

	// Repeated (instance_id, request_token) → ErrRequestTokenCollision.
	err := s.UpsertOpenPermissionRequest(id, tokenA, "tool_A2", `{"a":99}`)
	if !errors.Is(err, ErrRequestTokenCollision) {
		t.Fatalf("third upsert (same token): err = %v; want ErrRequestTokenCollision", err)
	}
	// Original row unmodified after collision.
	_, toolName, toolInput, _, _ = readPermRow(t, s, id, tokenA)
	if toolName != "tool_A" || toolInput != `{"a":1}` {
		t.Errorf("tokenA after collision: tool_name=%q tool_input=%q; want original values", toolName, toolInput)
	}

	// FK cascade: spawn delete drops all permission_requests rows.
	if _, err := s.db.Exec(`DELETE FROM spawns WHERE claude_instance_id = ?`, id); err != nil {
		t.Fatalf("delete spawn: %v", err)
	}
	if got := countPermRows(t, s, id); got != 0 {
		t.Errorf("permission_requests rows after spawn delete = %d; want 0 (ON DELETE CASCADE)", got)
	}
}

// TestUpsertOpenPermissionRequestCollisionLeavesRowIntact asserts that a
// UNIQUE-constraint collision leaves the original row intact and the error
// wraps ErrRequestTokenCollision.
func TestUpsertOpenPermissionRequestCollisionLeavesRowIntact(t *testing.T) {
	s := openTestStore(t)
	const id = "spawn-collision"
	seedSpawnForPerm(t, s, id, "on")

	if err := s.UpsertOpenPermissionRequest(id, tokenA, "original", `{"x":1}`); err != nil {
		t.Fatalf("initial upsert: %v", err)
	}
	if err := s.UpsertOpenPermissionRequest(id, tokenA, "collision", `{"x":2}`); !errors.Is(err, ErrRequestTokenCollision) {
		t.Fatalf("collision upsert: err = %v; want ErrRequestTokenCollision", err)
	}
	_, toolName, toolInput, decision, _ := readPermRow(t, s, id, tokenA)
	if toolName != "original" || toolInput != `{"x":1}` {
		t.Errorf("row after collision: tool_name=%q tool_input=%q; want original values", toolName, toolInput)
	}
	if decision.Valid {
		t.Errorf("row after collision: decision.Valid=true; must remain undecided")
	}
}

// TestDecidePermissionRequestOnlyAffectsOpenRow pins first-call-wins behavior.
func TestDecidePermissionRequestOnlyAffectsOpenRow(t *testing.T) {
	s := openTestStore(t)
	const id = "spawn-decide"
	seedSpawnForPerm(t, s, id, "on")
	if err := s.UpsertOpenPermissionRequest(id, tokenA, "Read", `{"file":"/etc/hosts"}`); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	updated, err := s.DecidePermissionRequest(id, tokenA, "allow", "trusted")
	if err != nil {
		t.Fatalf("first decide: %v", err)
	}
	if !updated {
		t.Fatalf("first decide returned updated=false; want true (open row should be flipped)")
	}

	updated, err = s.DecidePermissionRequest(id, tokenA, "deny", "second attempt")
	if err != nil {
		t.Fatalf("second decide: %v", err)
	}
	if updated {
		t.Fatalf("second decide returned updated=true; want false (first-call-wins per SRD §6.2)")
	}

	_, _, _, decision, reason := readPermRow(t, s, id, tokenA)
	if !decision.Valid || decision.String != "allow" {
		t.Errorf("decision = (%v, %q); want (valid, allow)", decision.Valid, decision.String)
	}
	if !reason.Valid || reason.String != "trusted" {
		t.Errorf("reason = (%v, %q); want (valid, trusted)", reason.Valid, reason.String)
	}
}

// TestDecidePermissionRequestEmptyReasonStored pins that an empty reason
// surfaces as "" via GetPermissionRequest's COALESCE (not as a special sentinel).
func TestDecidePermissionRequestEmptyReasonStored(t *testing.T) {
	s := openTestStore(t)
	const id = "spawn-empty-reason"
	seedSpawnForPerm(t, s, id, "on")
	if err := s.UpsertOpenPermissionRequest(id, tokenA, "Read", `{}`); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	updated, err := s.DecidePermissionRequest(id, tokenA, "deny", "")
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if !updated {
		t.Fatalf("decide returned updated=false; want true")
	}

	got, err := s.GetPermissionRequest(id, tokenA)
	if err != nil {
		t.Fatalf("GetPermissionRequest: %v", err)
	}
	if got.Decision != "deny" {
		t.Errorf("Decision = %q; want deny", got.Decision)
	}
	if got.DecisionReason != "" {
		t.Errorf("DecisionReason = %q; want \"\" (COALESCE must map NULL to empty string)", got.DecisionReason)
	}
}

// TestGetPermissionRequestExposesRequestIDAndCreatedAt pins SR-2.1:
// GetPermissionRequest projects request_id, created_at, and request_token.
func TestGetPermissionRequestExposesRequestIDAndCreatedAt(t *testing.T) {
	s := openTestStore(t)
	const id = "spawn-fields"
	seedSpawnForPerm(t, s, id, "on")

	before := time.Now().UTC().Add(-1 * time.Second)
	if err := s.UpsertOpenPermissionRequest(id, tokenA, "Read", `{"file":"/tmp/x"}`); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	after := time.Now().UTC().Add(1 * time.Second)

	rawReqID, _, _, _, _ := readPermRow(t, s, id, tokenA)

	got, err := s.GetPermissionRequest(id, tokenA)
	if err != nil {
		t.Fatalf("GetPermissionRequest: %v", err)
	}
	if got.RequestID == 0 {
		t.Errorf("RequestID = 0; want non-zero autoincrement id")
	}
	if got.RequestID != rawReqID {
		t.Errorf("RequestID = %d; raw read returned %d (SELECT projection may be out of order)", got.RequestID, rawReqID)
	}
	if got.RequestToken != tokenA {
		t.Errorf("RequestToken = %q; want %q", got.RequestToken, tokenA)
	}
	if got.CreatedAt.IsZero() {
		t.Errorf("CreatedAt is zero; want populated TIMESTAMP")
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

// TestDecidePermissionRequestTimeoutThenAllowIsRejected pins that a timeout
// decide is not clobberable by a subsequent allow (first-call-wins per SRD §6.2).
func TestDecidePermissionRequestTimeoutThenAllowIsRejected(t *testing.T) {
	s := openTestStore(t)
	const id = "spawn-timeout-seq"
	seedSpawnForPerm(t, s, id, "on")

	if err := s.UpsertOpenPermissionRequest(id, tokenA, "Bash", `{"command":"ls"}`); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	updated, err := s.DecidePermissionRequest(id, tokenA, "deny", DecisionReasonTimeout)
	if err != nil {
		t.Fatalf("timeout decide: %v", err)
	}
	if !updated {
		t.Fatalf("timeout decide returned updated=false; want true")
	}

	updated, err = s.DecidePermissionRequest(id, tokenA, "allow", "user-approved")
	if err != nil {
		t.Fatalf("subsequent allow decide: %v", err)
	}
	if updated {
		t.Fatalf("subsequent allow returned updated=true; timeout decision must not be clobberable")
	}

	_, _, _, decision, reason := readPermRow(t, s, id, tokenA)
	if !decision.Valid || decision.String != "deny" {
		t.Errorf("decision = (%v, %q); want (valid, deny)", decision.Valid, decision.String)
	}
	if !reason.Valid || reason.String != DecisionReasonTimeout {
		t.Errorf("reason = (%v, %q); want (valid, %q)", reason.Valid, reason.String, DecisionReasonTimeout)
	}
}

// TestAmbiguousDecide asserts ErrAmbiguousRequest is returned when empty
// token + N>1 open rows, and is NOT returned when only 1 open row exists.
func TestAmbiguousDecide(t *testing.T) {
	t.Run("two_open_rows", func(t *testing.T) {
		s := openTestStore(t)
		const id = "spawn-ambiguous"
		seedSpawnForPerm(t, s, id, "on")
		if err := s.UpsertOpenPermissionRequest(id, tokenA, "Bash", `{}`); err != nil {
			t.Fatalf("upsert tokenA: %v", err)
		}
		if err := s.UpsertOpenPermissionRequest(id, tokenB, "Read", `{}`); err != nil {
			t.Fatalf("upsert tokenB: %v", err)
		}

		updated, err := s.DecidePermissionRequest(id, "", "allow", "")
		if !errors.Is(err, ErrAmbiguousRequest) {
			t.Fatalf("err = %v; want ErrAmbiguousRequest", err)
		}
		if updated {
			t.Errorf("updated = true on ErrAmbiguousRequest; want false")
		}
		// Both rows must remain undecided.
		_, _, _, decA, _ := readPermRow(t, s, id, tokenA)
		if decA.Valid {
			t.Errorf("tokenA decided after ErrAmbiguousRequest; want NULL decision")
		}
		_, _, _, decB, _ := readPermRow(t, s, id, tokenB)
		if decB.Valid {
			t.Errorf("tokenB decided after ErrAmbiguousRequest; want NULL decision")
		}
	})

	t.Run("one_open_row", func(t *testing.T) {
		s := openTestStore(t)
		const id = "spawn-single"
		seedSpawnForPerm(t, s, id, "on")
		if err := s.UpsertOpenPermissionRequest(id, tokenA, "Bash", `{}`); err != nil {
			t.Fatalf("upsert tokenA: %v", err)
		}

		_, err := s.DecidePermissionRequest(id, "", "allow", "")
		if errors.Is(err, ErrAmbiguousRequest) {
			t.Fatalf("ErrAmbiguousRequest returned for single open row; must not trigger")
		}
	})
}

// TestDecisionReasonCanonicalConstants pins the exact string values of the
// three DecisionReason* constants per SR-1.3.
func TestDecisionReasonCanonicalConstants(t *testing.T) {
	if DecisionReasonOperator != "operator" {
		t.Errorf("DecisionReasonOperator = %q; want %q", DecisionReasonOperator, "operator")
	}
	if DecisionReasonTimeout != "timeout" {
		t.Errorf("DecisionReasonTimeout = %q; want %q", DecisionReasonTimeout, "timeout")
	}
	if DecisionReasonFindMissing != "find_missing" {
		t.Errorf("DecisionReasonFindMissing = %q; want %q", DecisionReasonFindMissing, "find_missing")
	}
}
