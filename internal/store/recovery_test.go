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
// zero trail lines are emitted when no orphaned rows exist. The find-missing
// paths emitted by findMissingImpl (not the store layer) are tested in
// pkg/api/find_missing_trail_test.go.

import (
	"database/sql"
	"testing"
)

// readLivenessRaw reads the two liveness columns for a row as raw NullString
// values, so tests can distinguish SQL NULL (Valid==false) from a set-but-empty
// value — GetSpawn COALESCEs both to "" and cannot make that distinction.
func readLivenessRaw(t *testing.T, s *Store, id string) (since, note sql.NullString) {
	t.Helper()
	err := s.db.QueryRow(
		`SELECT liveness_unverified_since, liveness_note FROM spawns WHERE claude_instance_id = ?`,
		id,
	).Scan(&since, &note)
	if err != nil {
		t.Fatalf("readLivenessRaw(%q): %v", id, err)
	}
	return since, note
}

// recoveryExamined reads the row's snapshot through GetSpawn: the life a
// find-missing sweep examines before its guarded write (SR-11.6).
func recoveryExamined(t *testing.T, s *Store, id string) RowSnapshot {
	t.Helper()
	sp, err := s.GetSpawn(id)
	if err != nil {
		t.Fatalf("GetSpawn(%q): %v", id, err)
	}
	return sp.Snapshot
}

// recoveryMarkMissing marks a live row missing the way find-missing does: it
// reads the row's snapshot first, then applies the guarded mark on it
// (SR-11.3, SR-11.6). It fails unless the mark applied and returned the row's
// prior state wantPrior.
func recoveryMarkMissing(t *testing.T, s *Store, id, wantPrior string) {
	t.Helper()
	prior, res, err := s.MarkMissingIfSameLife(id, recoveryExamined(t, s, id))
	if err != nil {
		t.Fatalf("MarkMissingIfSameLife(%q): %v", id, err)
	}
	if res != CondApplied {
		t.Fatalf("MarkMissingIfSameLife(%q) = %v; want CondApplied (snapshot read just before)", id, res)
	}
	if prior != wantPrior {
		t.Errorf("MarkMissingIfSameLife(%q) prior state = %q; want %q", id, prior, wantPrior)
	}
}

// seedLivenessSet inserts a live-state spawn (its agent's pane recorded, so its
// own hooks apply, SR-22.9) and pins both liveness columns through
// find-missing's guarded note write on the row's snapshot, returning the
// recorded since timestamp. The row is left in the given state so the caller
// can drive a specific clear path against it.
func seedLivenessSet(t *testing.T, s *Store, id, state string) string {
	t.Helper()
	if err := insertAgentRow(s, Spawn{
		ClaudeInstanceID: id, CWD: "/tmp", TmuxSessionName: "cd-" + id, RelayMode: "off",
	}); err != nil {
		t.Fatalf("seedLivenessSet: InsertPending(%q): %v", id, err)
	}
	if state != StatePending {
		if err := agentHook(s, id, state, false, "test_seed"); err != nil {
			t.Fatalf("seedLivenessSet: ApplyHookTransition(%q, %q): %v", id, state, err)
		}
	}
	res, err := s.SetLivenessNoteIfSameLife(id, recoveryExamined(t, s, id), "probe eacces")
	if err != nil {
		t.Fatalf("seedLivenessSet: SetLivenessNoteIfSameLife(%q): %v", id, err)
	}
	if res != CondApplied {
		t.Fatalf("seedLivenessSet: SetLivenessNoteIfSameLife(%q) = %v; want CondApplied", id, res)
	}
	since, note := readLivenessRaw(t, s, id)
	if !since.Valid || since.String == "" {
		t.Fatalf("seedLivenessSet: liveness_unverified_since not set: %+v", since)
	}
	if !note.Valid || note.String != "probe eacces" {
		t.Fatalf("seedLivenessSet: liveness_note = %+v; want (true, probe eacces)", note)
	}
	return since.String
}

// assertLivenessCleared fails unless both liveness columns are SQL NULL.
func assertLivenessCleared(t *testing.T, s *Store, id string) {
	t.Helper()
	since, note := readLivenessRaw(t, s, id)
	if since.Valid {
		t.Errorf("liveness_unverified_since = %q; want NULL", since.String)
	}
	if note.Valid {
		t.Errorf("liveness_note = %q; want NULL", note.String)
	}
}

// assertLivenessPreserved fails unless both liveness columns are still set,
// with the since column equal to wantSince (the pre-set timestamp).
func assertLivenessPreserved(t *testing.T, s *Store, id, wantSince string) {
	t.Helper()
	since, note := readLivenessRaw(t, s, id)
	if !since.Valid || since.String != wantSince {
		t.Errorf("liveness_unverified_since = %+v; want (true, %q) preserved", since, wantSince)
	}
	if !note.Valid || note.String == "" {
		t.Errorf("liveness_note = %+v; want a preserved non-empty note", note)
	}
}

// TestListLiveSpawnIdentitiesReadShape pins ListLiveSpawnIdentities (SR-8.1):
// it returns one row per live-state spawn (including pending) carrying the
// recorded pid/proc_starttime (zero values when the identity columns are NULL),
// excludes terminal rows, and carries the row's liveness note ("" when NULL)
// without failing when a row's liveness columns are set.
func TestListLiveSpawnIdentitiesReadShape(t *testing.T) {
	s, _ := openTempStore(t)

	// waiting row with recorded identity + liveness set. SR-22.9: SessionStart
	// records the identity (its parent, the pane process) and sets waiting in
	// one write, so the identity row is waiting and the pending row has none.
	if err := insertAgentRow(s, Spawn{
		ClaudeInstanceID: "live-waiting", CWD: "/tmp", TmuxSessionName: "cd-lwt", RelayMode: "off",
	}); err != nil {
		t.Fatalf("insertAgentRow(live-waiting): %v", err)
	}
	if err := agentSessionStart(s, "live-waiting", "", "", false); err != nil {
		t.Fatalf("SessionStart(live-waiting): %v", err)
	}
	if res, err := s.SetLivenessNoteIfSameLife("live-waiting", recoveryExamined(t, s, "live-waiting"), "probe eacces"); err != nil || res != CondApplied {
		t.Fatalf("SetLivenessNoteIfSameLife(live-waiting) = %v, %v; want CondApplied", res, err)
	}

	// pending row with NO recorded identity (columns NULL → zero values).
	if err := insertAgentRow(s, Spawn{
		ClaudeInstanceID: "live-pending", CWD: "/tmp", TmuxSessionName: "cd-lp", RelayMode: "off",
	}); err != nil {
		t.Fatalf("insertAgentRow(live-pending): %v", err)
	}

	// terminal (ended) row — must be excluded.
	if err := insertAgentRow(s, Spawn{
		ClaudeInstanceID: "dead-ended", CWD: "/tmp", TmuxSessionName: "cd-de", RelayMode: "off",
	}); err != nil {
		t.Fatalf("InsertPending(dead-ended): %v", err)
	}
	if err := agentHook(s, "dead-ended", StateEnded, false, "test_seed"); err != nil {
		t.Fatalf("transition dead-ended: %v", err)
	}

	ids, err := s.ListLiveSpawnIdentities()
	if err != nil {
		t.Fatalf("ListLiveSpawnIdentities: %v", err)
	}
	byID := make(map[string]LiveSpawnIdentity, len(ids))
	for _, it := range ids {
		byID[it.ClaudeInstanceID] = it
	}
	if len(byID) != 2 {
		t.Fatalf("live identities = %d (%v); want 2 (waiting + pending, ended excluded)", len(byID), ids)
	}
	if _, ok := byID["dead-ended"]; ok {
		t.Errorf("terminal (ended) row leaked into live identities: %v", ids)
	}

	w, ok := byID["live-waiting"]
	if !ok {
		t.Fatalf("waiting row missing from live identities: %v", ids)
	}
	if pane := testPane(); w.PID != pane.PanePID || w.ProcStarttime != pane.PaneStarttime {
		t.Errorf("live-waiting identity = (pid=%d, starttime=%q); want the pane process (%d, %q)", w.PID, w.ProcStarttime, pane.PanePID, pane.PaneStarttime)
	}
	if w.LivenessNote != "probe eacces" {
		t.Errorf("live-waiting LivenessNote = %q; want %q", w.LivenessNote, "probe eacces")
	}

	p, ok := byID["live-pending"]
	if !ok {
		t.Fatalf("pending row missing from live identities: %v", ids)
	}
	if p.PID != 0 || p.ProcStarttime != "" {
		t.Errorf("live-pending identity = (pid=%d, starttime=%q); want zero values (NULL columns)", p.PID, p.ProcStarttime)
	}
	if p.LivenessNote != "" {
		t.Errorf("live-pending LivenessNote = %q; want \"\" (NULL)", p.LivenessNote)
	}
}

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
// relay_mode=on and its agent's pane recorded, ready to receive the agent's
// gated permission_requests rows (SR-22.9).
func seedCheckPermissionSpawn(t *testing.T, s *Store, id string) {
	t.Helper()
	if err := insertAgentRow(s, Spawn{
		ClaudeInstanceID: id,
		CWD:              "/tmp",
		TmuxSessionName:  "cd-" + id,
		RelayMode:        "on",
	}); err != nil {
		t.Fatalf("seedCheckPermissionSpawn: InsertPending(%q): %v", id, err)
	}
	if err := agentHook(s, id, StateCheckPermission, false, "test_seed"); err != nil {
		t.Fatalf("seedCheckPermissionSpawn: ApplyHookTransition(%q, check_permission): %v", id, err)
	}
}

// TestFindMissingMultiRow verifies SR-5.4 + SR-A-2.5: when a Spawn has N>1
// open permission_requests rows, is marked missing through the guarded mark
// (MarkMissingIfSameLife on the snapshot read first, SR-11.6) and
// CloseOrphanedPermissionRequests is called,
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
		if err := agentPermissionRequest(s, id, tok, "Bash", `{"cmd":"echo"}`, 0, ""); err != nil {
			t.Fatalf("UpsertOpenPermissionRequest(%q): %v", tok, err)
		}
	}

	// Mark spawn missing through the guarded mark on the snapshot read just
	// before it, then close all orphaned rows.
	recoveryMarkMissing(t, s, id, StateCheckPermission)
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
// row, marked missing through the guarded mark, receives decision='deny' and one ad.find_missing.tick(permission_orphan_closeout)
// trail event.
func TestFindMissingSingleRow(t *testing.T) {
	s, _ := openTempStore(t)
	const id = "fm-single-row-1"

	seedCheckPermissionSpawn(t, s, id)
	if err := agentPermissionRequest(s, id, tokenA, "Read", `{"file":"/etc/hosts"}`, 0, ""); err != nil {
		t.Fatalf("UpsertOpenPermissionRequest: %v", err)
	}

	recoveryMarkMissing(t, s, id, StateCheckPermission)
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
// open permission_requests rows, the guarded mark still transitions the Spawn
// to missing and CloseOrphanedPermissionRequests is a no-op that emits zero
// ad.find_missing.tick lines.
func TestFindMissingNoOpenRows(t *testing.T) {
	s, _ := openTempStore(t)
	const id = "fm-no-open-rows-1"

	// Spawn in working state — no permission_requests rows at all.
	if err := insertAgentRow(s, Spawn{
		ClaudeInstanceID: id,
		CWD:              "/tmp",
		TmuxSessionName:  "cd-" + id,
		RelayMode:        "off",
	}); err != nil {
		t.Fatalf("InsertPending: %v", err)
	}
	if err := agentHook(s, id, StateWorking, false, "test_seed"); err != nil {
		t.Fatalf("transition to working: %v", err)
	}

	recoveryMarkMissing(t, s, id, StateWorking)
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
