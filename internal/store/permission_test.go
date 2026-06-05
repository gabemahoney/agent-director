package store

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Canonical UUIDv4 request_token constants for white-box store tests.
// Mirrors storefix.TestRequestToken{A,B,C} (same values) but declared here
// to avoid a circular import (package store → storefix → store).
const (
	tokenA = "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa"
	tokenB = "bbbbbbbb-bbbb-4bbb-bbbb-bbbbbbbbbbbb"
	tokenC = "cccccccc-cccc-4ccc-accc-cccccccccccc"
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

// TestUpsertOpenPermissionRequestAppendsRow pins the v2 multi-row semantics
// per SRD §6.2: at most one outstanding row per (claude_instance_id,
// request_token). Distinct pairs produce distinct rows (parallel permission
// requests for the same Spawn coexist); a repeated pair surfaces
// ErrRequestTokenCollision; the original row is intact.
func TestUpsertOpenPermissionRequestAppendsRow(t *testing.T) {
	s := openTestStore(t)
	const id = "spawn-append"
	seedSpawnForPerm(t, s, id, "on")

	// Two distinct tokens → two rows.
	if err := s.UpsertOpenPermissionRequest(id, tokenA, "tool_A", `{"a":1}`, 0, ""); err != nil {
		t.Fatalf("upsert tokenA: %v", err)
	}
	if err := s.UpsertOpenPermissionRequest(id, tokenB, "tool_B", `{"b":2}`, 0, ""); err != nil {
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
	err := s.UpsertOpenPermissionRequest(id, tokenA, "tool_A2", `{"a":99}`, 0, "")
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

	if err := s.UpsertOpenPermissionRequest(id, tokenA, "original", `{"x":1}`, 0, ""); err != nil {
		t.Fatalf("initial upsert: %v", err)
	}
	if err := s.UpsertOpenPermissionRequest(id, tokenA, "collision", `{"x":2}`, 0, ""); !errors.Is(err, ErrRequestTokenCollision) {
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
	if err := s.UpsertOpenPermissionRequest(id, tokenA, "Read", `{"file":"/etc/hosts"}`, 0, ""); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	updated, err := s.DecidePermissionRequest(id, tokenA, "allow", "trusted", "")
	if err != nil {
		t.Fatalf("first decide: %v", err)
	}
	if !updated {
		t.Fatalf("first decide returned updated=false; want true (open row should be flipped)")
	}

	updated, err = s.DecidePermissionRequest(id, tokenA, "deny", "second attempt", "")
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
	if err := s.UpsertOpenPermissionRequest(id, tokenA, "Read", `{}`, 0, ""); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	updated, err := s.DecidePermissionRequest(id, tokenA, "deny", "", "")
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
	if err := s.UpsertOpenPermissionRequest(id, tokenA, "Read", `{"file":"/tmp/x"}`, 0, ""); err != nil {
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

	if err := s.UpsertOpenPermissionRequest(id, tokenA, "Bash", `{"command":"ls"}`, 0, ""); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	updated, err := s.DecidePermissionRequest(id, tokenA, "deny", DecisionReasonTimeout, "")
	if err != nil {
		t.Fatalf("timeout decide: %v", err)
	}
	if !updated {
		t.Fatalf("timeout decide returned updated=false; want true")
	}

	updated, err = s.DecidePermissionRequest(id, tokenA, "allow", "user-approved", "")
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
		if err := s.UpsertOpenPermissionRequest(id, tokenA, "Bash", `{}`, 0, ""); err != nil {
			t.Fatalf("upsert tokenA: %v", err)
		}
		if err := s.UpsertOpenPermissionRequest(id, tokenB, "Read", `{}`, 0, ""); err != nil {
			t.Fatalf("upsert tokenB: %v", err)
		}

		updated, err := s.DecidePermissionRequest(id, "", "allow", "", "")
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
		if err := s.UpsertOpenPermissionRequest(id, tokenA, "Bash", `{}`, 0, ""); err != nil {
			t.Fatalf("upsert tokenA: %v", err)
		}

		_, err := s.DecidePermissionRequest(id, "", "allow", "", "")
		if errors.Is(err, ErrAmbiguousRequest) {
			t.Fatalf("ErrAmbiguousRequest returned for single open row; must not trigger")
		}
	})
}

// TestGetPermissionRequestByTokenOpenRow pins the open-row projection: a row
// with NULL decision / decision_reason / decided_at surfaces as empty strings
// (via COALESCE) and a zero-value DecidedAt. The rest of the projection
// (RequestID, ClaudeInstanceID, RequestToken, ToolName, ToolInput, CreatedAt)
// must be populated end-to-end.
func TestGetPermissionRequestByTokenOpenRow(t *testing.T) {
	s := openTestStore(t)
	const id = "spawn-by-token-open"
	seedSpawnForPerm(t, s, id, "on")

	before := time.Now().UTC().Add(-1 * time.Second)
	if err := s.UpsertOpenPermissionRequest(id, tokenA, "Read", `{"file":"/tmp/x"}`, 0, ""); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	after := time.Now().UTC().Add(1 * time.Second)

	got, err := s.GetPermissionRequestByToken(tokenA)
	if err != nil {
		t.Fatalf("GetPermissionRequestByToken: %v", err)
	}
	if got.RequestID == 0 {
		t.Errorf("RequestID = 0; want non-zero autoincrement id")
	}
	if got.ClaudeInstanceID != id {
		t.Errorf("ClaudeInstanceID = %q; want %q", got.ClaudeInstanceID, id)
	}
	if got.RequestToken != tokenA {
		t.Errorf("RequestToken = %q; want %q", got.RequestToken, tokenA)
	}
	if got.ToolName != "Read" || got.ToolInput != `{"file":"/tmp/x"}` {
		t.Errorf("ToolName/ToolInput drifted: name=%q input=%q", got.ToolName, got.ToolInput)
	}
	if got.CreatedAt.IsZero() {
		t.Errorf("CreatedAt is zero; want populated TIMESTAMP")
	}
	if got.CreatedAt.Before(before) || got.CreatedAt.After(after) {
		t.Errorf("CreatedAt = %v; want in [%v, %v]", got.CreatedAt, before, after)
	}
	if got.Decision != "" {
		t.Errorf("Decision = %q; want \"\" (open row, COALESCE(NULL,'') )", got.Decision)
	}
	if got.DecisionReason != "" {
		t.Errorf("DecisionReason = %q; want \"\" (open row, COALESCE(NULL,'') )", got.DecisionReason)
	}
	if !got.DecidedAt.IsZero() {
		t.Errorf("DecidedAt = %v; want zero time (open row, decided_at IS NULL)", got.DecidedAt)
	}
}

// TestGetPermissionRequestByTokenClosedAllow pins the closed-allow projection:
// a decided row surfaces Decision="allow", an empty DecisionReason (we pass
// reason=""), and a non-zero DecidedAt sourced from the CURRENT_TIMESTAMP write.
func TestGetPermissionRequestByTokenClosedAllow(t *testing.T) {
	s := openTestStore(t)
	const id = "spawn-by-token-allow"
	seedSpawnForPerm(t, s, id, "on")

	if err := s.UpsertOpenPermissionRequest(id, tokenA, "Read", `{}`, 0, ""); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	updated, err := s.DecidePermissionRequest(id, tokenA, "allow", "", "")
	if err != nil {
		t.Fatalf("decide allow: %v", err)
	}
	if !updated {
		t.Fatalf("decide allow returned updated=false; want true")
	}

	got, err := s.GetPermissionRequestByToken(tokenA)
	if err != nil {
		t.Fatalf("GetPermissionRequestByToken: %v", err)
	}
	if got.Decision != "allow" {
		t.Errorf("Decision = %q; want allow", got.Decision)
	}
	if got.DecisionReason != "" {
		t.Errorf("DecisionReason = %q; want \"\" (empty reason stored as NULL via COALESCE)", got.DecisionReason)
	}
	if got.DecidedAt.IsZero() {
		t.Errorf("DecidedAt is zero; want non-zero after a successful decide")
	}
}

// TestGetPermissionRequestByTokenClosedDenyReasons pins the deny projection
// across all three canonical decision_reason values. Table-driven per SR-1.3.
func TestGetPermissionRequestByTokenClosedDenyReasons(t *testing.T) {
	cases := []struct {
		name   string
		reason string
	}{
		{"operator", DecisionReasonOperator},
		{"timeout", DecisionReasonTimeout},
		{"find_missing", DecisionReasonFindMissing},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := openTestStore(t)
			id := "spawn-by-token-deny-" + tc.name
			seedSpawnForPerm(t, s, id, "on")
			if err := s.UpsertOpenPermissionRequest(id, tokenA, "Bash", `{}`, 0, ""); err != nil {
				t.Fatalf("upsert: %v", err)
			}
			updated, err := s.DecidePermissionRequest(id, tokenA, "deny", tc.reason, "")
			if err != nil {
				t.Fatalf("decide deny: %v", err)
			}
			if !updated {
				t.Fatalf("decide deny returned updated=false; want true")
			}

			got, err := s.GetPermissionRequestByToken(tokenA)
			if err != nil {
				t.Fatalf("GetPermissionRequestByToken: %v", err)
			}
			if got.Decision != "deny" {
				t.Errorf("Decision = %q; want deny", got.Decision)
			}
			if got.DecisionReason != tc.reason {
				t.Errorf("DecisionReason = %q; want %q", got.DecisionReason, tc.reason)
			}
			if got.DecidedAt.IsZero() {
				t.Errorf("DecidedAt is zero; want non-zero after a successful decide")
			}
		})
	}
}

// TestGetPermissionRequestByTokenMissReturnsSentinel pins SR-7.4: a token never
// written returns ErrPermissionRequestNotFound and the underlying sql.ErrNoRows
// MUST NOT leak across the store boundary. The "miss" and "no-sql-leak" cases
// share the same call site so the regression pin is meaningful.
func TestGetPermissionRequestByTokenMissReturnsSentinel(t *testing.T) {
	s := openTestStore(t)
	// Random UUIDv4 (RFC 4122 layout, 4xxx variant) never UPSERTed.
	const missingToken = "deadbeef-dead-4dea-adea-deadbeefdead"

	_, err := s.GetPermissionRequestByToken(missingToken)
	if !errors.Is(err, ErrPermissionRequestNotFound) {
		t.Fatalf("err = %v; want ErrPermissionRequestNotFound", err)
	}
	if errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("err = %v; sql.ErrNoRows MUST NOT leak across the store boundary (SR-7.4)", err)
	}
}

// TestGetPermissionRequestByTokenConcurrentReads exercises the read path
// under concurrent reader + writer pressure. Readers tight-loop on a seeded
// open row and a seeded closed row; a single writer interleaves transient
// UPSERTs (with unique fresh tokens) and decides against the seeded open row.
// The contract: reads of the seeded tokens must always succeed (never
// surface an error of any kind); the closed row's projection must be stable
// across the full run (its decision/decision_reason/decided_at columns are
// terminal, no writer touches them). The test is bounded by a 250 ms
// timer and must pass under `go test -race`.
func TestGetPermissionRequestByTokenConcurrentReads(t *testing.T) {
	s := openTestStore(t)
	const id = "spawn-concurrent-reads"
	seedSpawnForPerm(t, s, id, "on")

	if err := s.UpsertOpenPermissionRequest(id, tokenA, "Read", `{"file":"/a"}`, 0, ""); err != nil {
		t.Fatalf("upsert seeded open: %v", err)
	}
	if err := s.UpsertOpenPermissionRequest(id, tokenB, "Bash", `{"cmd":"ls"}`, 0, ""); err != nil {
		t.Fatalf("upsert seeded closed (pre-decide): %v", err)
	}
	updated, err := s.DecidePermissionRequest(id, tokenB, "deny", DecisionReasonOperator, "")
	if err != nil {
		t.Fatalf("decide seeded closed: %v", err)
	}
	if !updated {
		t.Fatalf("decide seeded closed updated=false; want true")
	}
	closedSnapshot, err := s.GetPermissionRequestByToken(tokenB)
	if err != nil {
		t.Fatalf("read closed snapshot: %v", err)
	}

	const readers = 8
	var stop atomic.Bool
	var wg sync.WaitGroup
	readerErrs := make(chan error, readers)
	closedDrift := make(chan string, readers)

	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				if _, err := s.GetPermissionRequestByToken(tokenA); err != nil {
					readerErrs <- fmt.Errorf("seeded open token: %w", err)
					return
				}
				cr, err := s.GetPermissionRequestByToken(tokenB)
				if err != nil {
					readerErrs <- fmt.Errorf("seeded closed token: %w", err)
					return
				}
				if cr.Decision != closedSnapshot.Decision ||
					cr.DecisionReason != closedSnapshot.DecisionReason ||
					!cr.DecidedAt.Equal(closedSnapshot.DecidedAt) {
					closedDrift <- fmt.Sprintf("decision=%q reason=%q decided_at=%v (want %q/%q/%v)",
						cr.Decision, cr.DecisionReason, cr.DecidedAt,
						closedSnapshot.Decision, closedSnapshot.DecisionReason, closedSnapshot.DecidedAt)
					return
				}
			}
		}()
	}

	writerErrs := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		var seq uint64
		for !stop.Load() {
			seq++
			// Unique transient token, UUIDv4-shape (8-4-4-4-12 hex, RFC 4122 4xxx
			// version marker) so it doesn't collide with the seeded tokens.
			transient := fmt.Sprintf("%08x-%04x-4%03x-a%03x-%012x",
				seq, seq&0xffff, seq&0xfff, seq&0xfff, seq)
			if err := s.UpsertOpenPermissionRequest(id, transient, "Bash", `{"cmd":"echo"}`, 0, ""); err != nil {
				writerErrs <- fmt.Errorf("transient upsert (seq=%d): %w", seq, err)
				return
			}
			// First call may flip the seeded open row; subsequent calls no-op
			// (first-call-wins). Either outcome is fine — DecidePermissionRequest
			// returns (false, nil) on the no-op path, never an error.
			if _, err := s.DecidePermissionRequest(id, tokenA, "allow", "", ""); err != nil {
				writerErrs <- fmt.Errorf("decide seeded open (seq=%d): %w", seq, err)
				return
			}
		}
	}()

	time.Sleep(250 * time.Millisecond)
	stop.Store(true)
	wg.Wait()
	close(readerErrs)
	close(closedDrift)
	close(writerErrs)

	for err := range readerErrs {
		t.Errorf("reader of seeded token returned error: %v", err)
	}
	for delta := range closedDrift {
		t.Errorf("closed row reader observed drift: %s", delta)
	}
	for err := range writerErrs {
		t.Errorf("writer error: %v", err)
	}
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

// openTempStoreWithPath opens a fresh on-disk SQLite store under t.TempDir()
// and returns both the store and the DB file path. Used by cap-eviction tests
// that need a raw sql.DB connection to backdate decided_at timestamps.
// (Cannot use storefix.OpenTempStore here: storefix imports store, which would
// create a circular import from package store test files.)
func openTempStoreWithPath(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := OpenOrInit(path)
	if err != nil {
		t.Fatalf("OpenOrInit(%q): %v", path, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, path
}

// seedClosedPermRequests seeds n decided (closed) permission_requests rows for
// instanceID with deterministic decided_at values. The spawn row is created if
// absent. Returns tokens in insertion order (index 0 = oldest decided_at).
// Mirrors storefix.SeedClosedPermissionRequests without the cross-package import.
func seedClosedPermRequests(t *testing.T, s *Store, dbPath, instanceID string, n int, baseTime time.Time, step time.Duration) []string {
	t.Helper()

	// Ensure spawn row exists.
	if err := s.InsertPending(Spawn{
		ClaudeInstanceID: instanceID,
		CWD:              "/tmp",
		TmuxSessionName:  "sess-" + instanceID,
		RelayMode:        "off",
	}); err != nil {
		// Already exists — check it's really there.
		if _, getErr := s.GetSpawn(instanceID); getErr != nil {
			t.Fatalf("seedClosedPermRequests: ensure spawn %q: InsertPending: %v; GetSpawn: %v", instanceID, err, getErr)
		}
	}

	tokens := make([]string, 0, n)
	for i := 0; i < n; i++ {
		tok := fmt.Sprintf("%08x-0000-4000-a000-%012x", i, i)
		if err := s.UpsertOpenPermissionRequest(instanceID, tok, "Bash", `{"cmd":"echo"}`, 0, ""); err != nil {
			t.Fatalf("seedClosedPermRequests: UpsertOpenPermissionRequest(%q, %q): %v", instanceID, tok, err)
		}
		updated, err := s.DecidePermissionRequest(instanceID, tok, "deny", DecisionReasonOperator, "")
		if err != nil {
			t.Fatalf("seedClosedPermRequests: DecidePermissionRequest(%q, %q): %v", instanceID, tok, err)
		}
		if !updated {
			t.Fatalf("seedClosedPermRequests: DecidePermissionRequest(%q, %q) returned updated=false", instanceID, tok)
		}
		tokens = append(tokens, tok)
	}

	// Backdate decided_at via a raw connection to get controlled timestamps.
	raw, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("seedClosedPermRequests: open raw db %q: %v", dbPath, err)
	}
	defer func() { _ = raw.Close() }()

	for i, tok := range tokens {
		decidedAt := baseTime.Add(time.Duration(i) * step).UTC().Format("2006-01-02 15:04:05")
		if _, err := raw.Exec(
			`UPDATE permission_requests SET decided_at = ? WHERE claude_instance_id = ? AND request_token = ?`,
			decidedAt, instanceID, tok,
		); err != nil {
			t.Fatalf("seedClosedPermRequests: backdate decided_at for %q tok %q: %v", instanceID, tok, err)
		}
	}
	return tokens
}

// countAllPermRows returns the total number of permission_requests rows
// across all instance IDs — used to pin global cap-eviction semantics.
func countAllPermRows(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM permission_requests`).Scan(&n); err != nil {
		t.Fatalf("count all permission_requests: %v", err)
	}
	return n
}

// TestUpsertEvictsOldestClosedRowsOverCap pins SR-11.4's eviction selector and
// Epic AC #6. Table-driven across cap values {10, 100, 1000}; per N: (a) at-cap
// sub-case — 1 row evicted; (b) over-cap sub-case — 6 rows evicted in one pass.
// Evicted rows are always the oldest by decided_at ASC.
func TestUpsertEvictsOldestClosedRowsOverCap(t *testing.T) {
	for _, N := range []int{10, 100, 1000} {
		N := N
		t.Run(fmt.Sprintf("cap_%d", N), func(t *testing.T) {
			t.Run("at_cap", func(t *testing.T) {
				s, dbPath := openTempStoreWithPath(t)
				base := time.Now().UTC().Add(-2 * time.Hour)
				tokens := seedClosedPermRequests(t, s, dbPath, "evict-at", N, base, time.Minute)

				const newTok = "ffffffff-ffff-4fff-afff-ffffffffffff"
				if err := s.UpsertOpenPermissionRequest("evict-at", newTok, "Bash", `{}`, N, ""); err != nil {
					t.Fatalf("upsert: %v", err)
				}

				// Total count = N (N seeded + 1 new open - 1 evicted = N).
				if got := countAllPermRows(t, s); got != N {
					t.Errorf("post-call count = %d; want %d", got, N)
				}
				// Oldest seeded row (tokens[0]) must be absent.
				var c int
				if err := s.db.QueryRow(`SELECT COUNT(*) FROM permission_requests WHERE request_token = ?`, tokens[0]).Scan(&c); err != nil {
					t.Fatalf("check oldest row: %v", err)
				}
				if c != 0 {
					t.Errorf("oldest closed row (tokens[0]) still present; want evicted")
				}
				// New open row present.
				if err := s.db.QueryRow(`SELECT COUNT(*) FROM permission_requests WHERE request_token = ?`, newTok).Scan(&c); err != nil {
					t.Fatalf("check new open row: %v", err)
				}
				if c != 1 {
					t.Errorf("new open row absent; want present")
				}
				// N-1 closed rows preserved.
				if err := s.db.QueryRow(`SELECT COUNT(*) FROM permission_requests WHERE decision IS NOT NULL`).Scan(&c); err != nil {
					t.Fatalf("count closed: %v", err)
				}
				if c != N-1 {
					t.Errorf("closed row count = %d; want %d (all but oldest preserved)", c, N-1)
				}
			})

			t.Run("over_cap_on_entry", func(t *testing.T) {
				s, dbPath := openTempStoreWithPath(t)
				base := time.Now().UTC().Add(-2 * time.Hour)
				tokens := seedClosedPermRequests(t, s, dbPath, "evict-over", N+5, base, time.Minute)

				const newTok = "eeeeeeee-eeee-4eee-aeee-eeeeeeeeeeee"
				if err := s.UpsertOpenPermissionRequest("evict-over", newTok, "Bash", `{}`, N, ""); err != nil {
					t.Fatalf("upsert: %v", err)
				}

				// Total = N (N+5 seeded + 1 new open - 6 evicted = N).
				if got := countAllPermRows(t, s); got != N {
					t.Errorf("post-call count = %d; want %d (6 evicted in one pass)", got, N)
				}
				// 6 oldest closed rows must be absent.
				for i := 0; i < 6; i++ {
					var c int
					if err := s.db.QueryRow(`SELECT COUNT(*) FROM permission_requests WHERE request_token = ?`, tokens[i]).Scan(&c); err != nil {
						t.Fatalf("check evicted row[%d]: %v", i, err)
					}
					if c != 0 {
						t.Errorf("evicted row[%d] (token %s) still present", i, tokens[i])
					}
				}
				// New open row present.
				var c int
				if err := s.db.QueryRow(`SELECT COUNT(*) FROM permission_requests WHERE request_token = ?`, newTok).Scan(&c); err != nil {
					t.Fatalf("check new open: %v", err)
				}
				if c != 1 {
					t.Errorf("new open row absent")
				}
			})
		})
	}
}

// TestUpsertNeverEvictsOpenRows pins SR-11.4's open-row exclusion and Epic AC #7.
// Open rows (decision IS NULL) are never eviction candidates; upsert returns nil
// when no closed rows exist to evict (SR-3.1).
func TestUpsertNeverEvictsOpenRows(t *testing.T) {
	const cap = 5

	t.Run("all_open", func(t *testing.T) {
		s := openTestStore(t)
		const id = "open-only"
		seedSpawnForPerm(t, s, id, "on")

		// Seed cap open rows.
		for i := 0; i < cap; i++ {
			tok := fmt.Sprintf("%08x-0000-4000-a000-%012x", i, i)
			if err := s.UpsertOpenPermissionRequest(id, tok, "Bash", `{}`, cap, ""); err != nil {
				t.Fatalf("seed open row %d: %v", i, err)
			}
		}

		// One more upsert: cap+1 rows, all open — no closed rows to evict.
		const extraTok = "ffffffff-ffff-4fff-afff-ffffffffffff"
		if err := s.UpsertOpenPermissionRequest(id, extraTok, "Bash", `{}`, cap, ""); err != nil {
			t.Errorf("upsert with all-open: err = %v; want nil (SR-3.1: never errors when no closed rows)", err)
		}

		// All cap+1 rows present.
		if got := countPermRows(t, s, id); got != cap+1 {
			t.Errorf("post-call count = %d; want %d", got, cap+1)
		}
		// All rows have decision IS NULL.
		var closed int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM permission_requests WHERE claude_instance_id = ? AND decision IS NOT NULL`, id).Scan(&closed); err != nil {
			t.Fatalf("count closed: %v", err)
		}
		if closed != 0 {
			t.Errorf("closed rows = %d; want 0 (open rows must never be evicted)", closed)
		}
	})

	t.Run("one_closed", func(t *testing.T) {
		s, dbPath := openTempStoreWithPath(t)
		const id = "open-plus-closed"

		// Use seedClosedPermRequests with n=1 to create spawn and one closed row,
		// then seed cap-1 open rows (spawn already exists).
		base := time.Now().UTC().Add(-1 * time.Hour)
		closedTokens := seedClosedPermRequests(t, s, dbPath, id, 1, base, time.Minute)

		// Seed cap-1 open rows (spawn already exists).
		for i := 0; i < cap-1; i++ {
			tok := fmt.Sprintf("%08x-1111-4111-a111-%012x", i, i)
			if err := s.UpsertOpenPermissionRequest(id, tok, "Bash", `{}`, 0, ""); err != nil {
				t.Fatalf("seed open row %d: %v", i, err)
			}
		}
		// State: 1 closed + (cap-1) open = cap rows total.

		// Upsert one more open row with cap=cap → total = cap+1 → evict 1 closed.
		const newTok = "dddddddd-dddd-4ddd-addd-dddddddddddd"
		if err := s.UpsertOpenPermissionRequest(id, newTok, "Bash", `{}`, cap, ""); err != nil {
			t.Fatalf("upsert: %v", err)
		}

		// Post-call: cap rows (1 closed evicted, (cap-1) open + 1 new open remain).
		if got := countAllPermRows(t, s); got != cap {
			t.Errorf("post-call count = %d; want %d", got, cap)
		}
		// The closed row is gone.
		var c int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM permission_requests WHERE request_token = ?`, closedTokens[0]).Scan(&c); err != nil {
			t.Fatalf("check closed row: %v", err)
		}
		if c != 0 {
			t.Errorf("closed row still present; want evicted")
		}
		// All remaining rows are open.
		var openCount int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM permission_requests WHERE decision IS NULL`).Scan(&openCount); err != nil {
			t.Fatalf("count open: %v", err)
		}
		if openCount != cap {
			t.Errorf("open row count = %d; want %d", openCount, cap)
		}
	})
}

// TestRelayConfigCapZeroDisablesEviction pins SR-11.2's cap=0 semantics and
// Epic AC #9. cap=0 (config.Relay{PermissionRequestCap: 0}) disables eviction
// entirely; closed rows accumulate without bound.
func TestRelayConfigCapZeroDisablesEviction(t *testing.T) {
	s, dbPath := openTempStoreWithPath(t)
	const n = 2000
	base := time.Now().UTC().Add(-1 * time.Hour)
	seedClosedPermRequests(t, s, dbPath, "zero-cap", n, base, time.Second)

	// Upsert with cap=0 (from config.Relay{PermissionRequestCap: 0}).
	const newTok = "00000000-0000-4000-a000-000000000001"
	if err := s.UpsertOpenPermissionRequest("zero-cap", newTok, "Bash", `{}`, 0, ""); err != nil {
		t.Fatalf("upsert with cap=0: %v", err)
	}

	// All 2000 closed + 1 new open = 2001 rows; zero evicted.
	if got := countAllPermRows(t, s); got != n+1 {
		t.Errorf("post-call count = %d; want %d (cap=0 disables eviction entirely)", got, n+1)
	}
	// Confirm no eviction: exactly 1 open row, 2000 closed.
	var openCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM permission_requests WHERE decision IS NULL`).Scan(&openCount); err != nil {
		t.Fatalf("count open: %v", err)
	}
	if openCount != 1 {
		t.Errorf("open row count = %d; want 1", openCount)
	}
}
