package store

// SR-9.2 + SR-5.4: find-missing reconciler tests.
//
// TestFindMissingMultiRow carries the runtime portion of
// TestDecisionReasonOnlyCanonicalValues for the find_missing path:
// each row's raw decision_reason column is verified to equal
// DecisionReasonFindMissing (not an uncontrolled string literal).

import (
	"testing"
)

// seedCheckPermissionSpawn inserts a Spawn in check_permission state with
// relay_mode=on, ready to receive permission_requests rows.
func seedCheckPermissionSpawn(t *testing.T, s *Store, id string) {
	t.Helper()
	if err := s.InsertPending(Spawn{
		ClaudeInstanceID: id,
		CWD:              "/tmp",
		TmuxSessionName:  "cd-" + id,
		RelayMode:        "on",
	}); err != nil {
		t.Fatalf("seedCheckPermissionSpawn: InsertPending(%q): %v", id, err)
	}
	if err := s.ApplyHookTransition(id, StateCheckPermission, false); err != nil {
		t.Fatalf("seedCheckPermissionSpawn: ApplyHookTransition(%q, check_permission): %v", id, err)
	}
}

// TestFindMissingMultiRow verifies SR-5.4: when a Spawn has N>1 open
// permission_requests rows and CloseOrphanedPermissionRequests is called, every
// open row receives decision='deny' and decision_reason='find_missing'
// (verified against store.DecisionReasonFindMissing via raw DB column read).
// This carries the runtime portion of TestDecisionReasonOnlyCanonicalValues for
// the find_missing path.
func TestFindMissingMultiRow(t *testing.T) {
	s, _ := openTempStore(t)
	const id = "fm-multi-row-1"

	seedCheckPermissionSpawn(t, s, id)

	// Seed 3 open rows with distinct tokens.
	tokens := []string{tokenA, tokenB, tokenC}
	for _, tok := range tokens {
		if err := s.UpsertOpenPermissionRequest(id, tok, "Bash", `{"cmd":"echo"}`, 0); err != nil {
			t.Fatalf("UpsertOpenPermissionRequest(%q): %v", tok, err)
		}
	}

	// Mark spawn missing, then close all orphaned rows.
	if err := s.MarkSpawnMissing(id); err != nil {
		t.Fatalf("MarkSpawnMissing: %v", err)
	}
	if err := s.CloseOrphanedPermissionRequests(id); err != nil {
		t.Fatalf("CloseOrphanedPermissionRequests: %v", err)
	}

	// Spawn must be in missing state.
	state, err := s.GetSpawnState(id)
	if err != nil {
		t.Fatalf("GetSpawnState: %v", err)
	}
	if state != StateMissing {
		t.Errorf("spawn state = %q; want missing", state)
	}

	// Every row must carry decision='deny', decision_reason='find_missing'.
	// Use readPermRow to read raw NullString values and confirm the column
	// is non-NULL and equals the canonical constant (not a free-form string).
	for _, tok := range tokens {
		_, _, _, decision, reason := readPermRow(t, s, id, tok)
		if !decision.Valid || decision.String != "deny" {
			t.Errorf("token %q: decision = (%v, %q); want (true, deny)", tok, decision.Valid, decision.String)
		}
		if !reason.Valid || reason.String != DecisionReasonFindMissing {
			t.Errorf("token %q: decision_reason = (%v, %q); want (true, %q)",
				tok, reason.Valid, reason.String, DecisionReasonFindMissing)
		}
	}
}

// TestFindMissingSingleRow verifies the same reconciler on a Spawn with one
// open row: the single row is denied with decision_reason='find_missing'.
func TestFindMissingSingleRow(t *testing.T) {
	s, _ := openTempStore(t)
	const id = "fm-single-row-1"

	seedCheckPermissionSpawn(t, s, id)
	if err := s.UpsertOpenPermissionRequest(id, tokenA, "Read", `{"file":"/etc/hosts"}`, 0); err != nil {
		t.Fatalf("UpsertOpenPermissionRequest: %v", err)
	}

	if err := s.MarkSpawnMissing(id); err != nil {
		t.Fatalf("MarkSpawnMissing: %v", err)
	}
	if err := s.CloseOrphanedPermissionRequests(id); err != nil {
		t.Fatalf("CloseOrphanedPermissionRequests: %v", err)
	}

	state, err := s.GetSpawnState(id)
	if err != nil {
		t.Fatalf("GetSpawnState: %v", err)
	}
	if state != StateMissing {
		t.Errorf("spawn state = %q; want missing", state)
	}

	_, _, _, decision, reason := readPermRow(t, s, id, tokenA)
	if !decision.Valid || decision.String != "deny" {
		t.Errorf("decision = (%v, %q); want (true, deny)", decision.Valid, decision.String)
	}
	if !reason.Valid || reason.String != DecisionReasonFindMissing {
		t.Errorf("decision_reason = (%v, %q); want (true, %q)", reason.Valid, reason.String, DecisionReasonFindMissing)
	}
}

// TestFindMissingNoOpenRows verifies the reconciler completes without error when
// a Spawn has no open permission_requests rows. MarkSpawnMissing must still
// transition the Spawn to missing; CloseOrphanedPermissionRequests is a no-op.
func TestFindMissingNoOpenRows(t *testing.T) {
	s, _ := openTempStore(t)
	const id = "fm-no-open-rows-1"

	// Spawn in working state — no permission_requests rows at all.
	if err := s.InsertPending(Spawn{
		ClaudeInstanceID: id,
		CWD:              "/tmp",
		TmuxSessionName:  "cd-" + id,
		RelayMode:        "off",
	}); err != nil {
		t.Fatalf("InsertPending: %v", err)
	}
	if err := s.ApplyHookTransition(id, StateWorking, false); err != nil {
		t.Fatalf("transition to working: %v", err)
	}

	if err := s.MarkSpawnMissing(id); err != nil {
		t.Fatalf("MarkSpawnMissing: %v", err)
	}
	if err := s.CloseOrphanedPermissionRequests(id); err != nil {
		t.Fatalf("CloseOrphanedPermissionRequests (no open rows): %v", err)
	}

	state, err := s.GetSpawnState(id)
	if err != nil {
		t.Fatalf("GetSpawnState: %v", err)
	}
	if state != StateMissing {
		t.Errorf("spawn state = %q; want missing", state)
	}

	// No permission_requests rows should exist.
	openRows, err := s.OpenPermissionRequestsForSpawn(id)
	if err != nil {
		t.Fatalf("OpenPermissionRequestsForSpawn: %v", err)
	}
	if len(openRows) != 0 {
		t.Errorf("open rows after CloseOrphanedPermissionRequests = %d; want 0", len(openRows))
	}
}
