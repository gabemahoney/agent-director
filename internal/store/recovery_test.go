package store

// SR-9.2 + SR-5.4 + SR-A-2.5: find-missing reconciler tests.
//
// TestFindMissingMultiRow carries the runtime portion of
// TestDecisionReasonOnlyCanonicalValues for the find_missing path:
// each row's raw decision_reason column is verified to equal
// DecisionReasonFindMissing (not an uncontrolled string literal).
//
// Trail emission tests pin the ad.find_missing.tick events emitted by
// CloseOrphanedPermissionRequests (permission_orphan_closeout) and verify that
// zero trail lines are emitted when no orphaned rows exist. The proc_absent and
// degraded_mode_skip paths are tested in pkg/api/find_missing_trail_test.go
// (those events are emitted by findMissingImpl, not by the store layer).

import (
	"testing"
)

// findMissingTicksAt returns ad.find_missing.tick lines added to the store
// trail after prevCount total lines. Uses readStoreTrailLines (trail_emit_test.go)
// so the process-level singleton path set by TestMain is shared.
func findMissingTicksAt(t *testing.T, prevCount int) []map[string]any {
	t.Helper()
	all := readStoreTrailLines(t)
	var out []map[string]any
	for _, row := range all[prevCount:] {
		if row["event"] == "ad.find_missing.tick" {
			out = append(out, row)
		}
	}
	return out
}

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
	if err := s.ApplyHookTransition(id, StateCheckPermission, false, "test_seed"); err != nil {
		t.Fatalf("seedCheckPermissionSpawn: ApplyHookTransition(%q, check_permission): %v", id, err)
	}
}

// TestFindMissingMultiRow verifies SR-5.4 + SR-A-2.5: when a Spawn has N>1
// open permission_requests rows and CloseOrphanedPermissionRequests is called,
// every open row receives decision='deny' and decision_reason='find_missing'
// (verified against store.DecisionReasonFindMissing via raw DB column read).
// One ad.find_missing.tick(permission_orphan_closeout) trail event must be
// emitted per closed row.
//
// This carries the runtime portion of TestDecisionReasonOnlyCanonicalValues for
// the find_missing path.
func TestFindMissingMultiRow(t *testing.T) {
	s, _ := openTempStore(t)
	const id = "fm-multi-row-1"

	seedCheckPermissionSpawn(t, s, id)

	// Seed 3 open rows with distinct tokens.
	tokens := []string{tokenA, tokenB, tokenC}
	for _, tok := range tokens {
		if err := s.UpsertOpenPermissionRequest(id, tok, "Bash", `{"cmd":"echo"}`, 0, ""); err != nil {
			t.Fatalf("UpsertOpenPermissionRequest(%q): %v", tok, err)
		}
	}

	// Mark spawn missing, then close all orphaned rows.
	if _, err := s.MarkSpawnMissing(id); err != nil {
		t.Fatalf("MarkSpawnMissing: %v", err)
	}
	before := len(readStoreTrailLines(t))
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

	// SR-A-2.5: one ad.find_missing.tick(permission_orphan_closeout) per closed row.
	ticks := findMissingTicksAt(t, before)
	if len(ticks) != len(tokens) {
		t.Fatalf("want %d ad.find_missing.tick (permission_orphan_closeout); got %d", len(tokens), len(ticks))
	}
	for _, tick := range ticks {
		assertTrailStr(t, tick, "event", "ad.find_missing.tick")
		assertTrailStr(t, tick, "reconciliation_reason", "permission_orphan_closeout")
		assertTrailStr(t, tick, "source", "ad_find_missing")
		assertTrailStr(t, tick, "claude_instance_id", id)
		if _, ok := tick["request_token"].(string); !ok {
			t.Errorf("[request_token] = %v; want non-empty string", tick["request_token"])
		}
		ts, ok := tick["ts"].(string)
		if !ok || !storeTSRe.MatchString(ts) {
			t.Errorf("[ts] = %v; want RFC3339Nano timestamp", tick["ts"])
		}
	}
}

// TestFindMissingSingleRow verifies SR-5.4 + SR-A-2.5: a Spawn with one open
// row receives decision='deny' and one ad.find_missing.tick(permission_orphan_closeout)
// trail event.
func TestFindMissingSingleRow(t *testing.T) {
	s, _ := openTempStore(t)
	const id = "fm-single-row-1"

	seedCheckPermissionSpawn(t, s, id)
	if err := s.UpsertOpenPermissionRequest(id, tokenA, "Read", `{"file":"/etc/hosts"}`, 0, ""); err != nil {
		t.Fatalf("UpsertOpenPermissionRequest: %v", err)
	}

	if _, err := s.MarkSpawnMissing(id); err != nil {
		t.Fatalf("MarkSpawnMissing: %v", err)
	}
	before := len(readStoreTrailLines(t))
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

	// SR-A-2.5: exactly one ad.find_missing.tick(permission_orphan_closeout).
	ticks := findMissingTicksAt(t, before)
	if len(ticks) != 1 {
		t.Fatalf("want 1 ad.find_missing.tick (permission_orphan_closeout); got %d", len(ticks))
	}
	tick := ticks[0]
	assertTrailStr(t, tick, "event", "ad.find_missing.tick")
	assertTrailStr(t, tick, "reconciliation_reason", "permission_orphan_closeout")
	assertTrailStr(t, tick, "source", "ad_find_missing")
	assertTrailStr(t, tick, "claude_instance_id", id)
	if _, ok := tick["request_token"].(string); !ok {
		t.Errorf("[request_token] = %v; want non-empty string", tick["request_token"])
	}
	ts, ok := tick["ts"].(string)
	if !ok || !storeTSRe.MatchString(ts) {
		t.Errorf("[ts] = %v; want RFC3339Nano timestamp", tick["ts"])
	}
}

// TestFindMissingNoOpenRows verifies SR-5.4 + SR-A-2.5: when a Spawn has no
// open permission_requests rows, MarkSpawnMissing still transitions the Spawn
// to missing and CloseOrphanedPermissionRequests is a no-op that emits zero
// ad.find_missing.tick lines.
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
	if err := s.ApplyHookTransition(id, StateWorking, false, "test_seed"); err != nil {
		t.Fatalf("transition to working: %v", err)
	}

	if _, err := s.MarkSpawnMissing(id); err != nil {
		t.Fatalf("MarkSpawnMissing: %v", err)
	}
	before := len(readStoreTrailLines(t))
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

	// SR-A-2.5: no-op closeout must emit zero ad.find_missing.tick lines.
	if ticks := findMissingTicksAt(t, before); len(ticks) != 0 {
		t.Errorf("no-open-rows path emitted %d ad.find_missing.tick; want 0", len(ticks))
	}
}
