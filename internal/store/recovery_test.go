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

// seedLivenessSet inserts a live-state spawn and pins both liveness columns via
// the guarded setter, returning the recorded since timestamp. The row is left in
// the given state so the caller can drive a specific clear path against it.
func seedLivenessSet(t *testing.T, s *Store, id, state string) string {
	t.Helper()
	if err := s.InsertPending(Spawn{
		ClaudeInstanceID: id, CWD: "/tmp", TmuxSessionName: "cd-" + id, RelayMode: "off",
	}); err != nil {
		t.Fatalf("seedLivenessSet: InsertPending(%q): %v", id, err)
	}
	if state != StatePending {
		if err := s.ApplyHookTransition(id, state, false, "test_seed"); err != nil {
			t.Fatalf("seedLivenessSet: ApplyHookTransition(%q, %q): %v", id, state, err)
		}
	}
	transitioned, err := s.SetLivenessUnverified(id, "probe eacces")
	if err != nil {
		t.Fatalf("seedLivenessSet: SetLivenessUnverified(%q): %v", id, err)
	}
	if !transitioned {
		t.Fatalf("seedLivenessSet: SetLivenessUnverified(%q) transitioned=false; want true (NULL→set)", id)
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

// TestSetLivenessUnverifiedFirstSetThenPreserve pins the guarded setter's
// NULL→set / preserve / transitioned semantics (SR-8.1). The first call on a
// clear live row sets both columns and returns transitioned=true; a repeat call
// preserves the ORIGINAL since timestamp and returns transitioned=false.
func TestSetLivenessUnverifiedFirstSetThenPreserve(t *testing.T) {
	s, _ := openTempStore(t)
	const id = "liveness-set-preserve-1"
	if err := s.InsertPending(Spawn{
		ClaudeInstanceID: id, CWD: "/tmp", TmuxSessionName: "cd-lsp", RelayMode: "off",
	}); err != nil {
		t.Fatalf("InsertPending: %v", err)
	}

	// First set: NULL→set, transitioned=true, timestamp non-empty.
	transitioned, err := s.SetLivenessUnverified(id, "first note")
	if err != nil {
		t.Fatalf("SetLivenessUnverified (first): %v", err)
	}
	if !transitioned {
		t.Fatalf("first SetLivenessUnverified transitioned=false; want true (NULL→set)")
	}
	firstSince, firstNote := readLivenessRaw(t, s, id)
	if !firstSince.Valid || firstSince.String == "" {
		t.Fatalf("liveness_unverified_since after first set = %+v; want non-empty", firstSince)
	}
	if !firstNote.Valid || firstNote.String != "first note" {
		t.Fatalf("liveness_note after first set = %+v; want (true, first note)", firstNote)
	}

	// Repeat: preserves original since timestamp, transitioned=false, note NOT
	// overwritten (the guarded UPDATE matches 0 rows).
	transitioned, err = s.SetLivenessUnverified(id, "second note")
	if err != nil {
		t.Fatalf("SetLivenessUnverified (repeat): %v", err)
	}
	if transitioned {
		t.Fatalf("repeat SetLivenessUnverified transitioned=true; want false (already set)")
	}
	secondSince, secondNote := readLivenessRaw(t, s, id)
	if secondSince.String != firstSince.String {
		t.Errorf("since timestamp changed on repeat: %q -> %q; want preserved", firstSince.String, secondSince.String)
	}
	if secondNote.String != "first note" {
		t.Errorf("liveness_note = %q on repeat; want preserved original (first note)", secondNote.String)
	}
}

// TestSetLivenessUnverifiedNoopOnAbsentAndTerminal pins the guarded setter's
// fail-open no-op discipline: an absent row and a terminal-state row (ended /
// missing) both return transitioned=false and leave the columns NULL.
func TestSetLivenessUnverifiedNoopOnAbsentAndTerminal(t *testing.T) {
	s, _ := openTempStore(t)

	// Absent row: no-op, transitioned=false, no error.
	transitioned, err := s.SetLivenessUnverified("no-such-row", "note")
	if err != nil {
		t.Fatalf("SetLivenessUnverified (absent): %v", err)
	}
	if transitioned {
		t.Errorf("absent-row SetLivenessUnverified transitioned=true; want false")
	}

	// Terminal (ended) row: guarded on the live-state set, so no-op.
	const endedID = "liveness-terminal-ended-1"
	if err := s.InsertPending(Spawn{
		ClaudeInstanceID: endedID, CWD: "/tmp", TmuxSessionName: "cd-end", RelayMode: "off",
	}); err != nil {
		t.Fatalf("InsertPending(ended): %v", err)
	}
	if err := s.ApplyHookTransition(endedID, StateEnded, false, "test_seed"); err != nil {
		t.Fatalf("transition to ended: %v", err)
	}
	transitioned, err = s.SetLivenessUnverified(endedID, "note")
	if err != nil {
		t.Fatalf("SetLivenessUnverified (ended): %v", err)
	}
	if transitioned {
		t.Errorf("ended-row SetLivenessUnverified transitioned=true; want false (terminal guard)")
	}
	assertLivenessCleared(t, s, endedID)

	// Missing (terminal) row: same no-op discipline.
	const missingID = "liveness-terminal-missing-1"
	if err := s.InsertPending(Spawn{
		ClaudeInstanceID: missingID, CWD: "/tmp", TmuxSessionName: "cd-mis", RelayMode: "off",
	}); err != nil {
		t.Fatalf("InsertPending(missing): %v", err)
	}
	if err := s.ApplyHookTransition(missingID, StateWorking, false, "test_seed"); err != nil {
		t.Fatalf("transition to working: %v", err)
	}
	if _, err := s.MarkSpawnMissing(missingID); err != nil {
		t.Fatalf("MarkSpawnMissing: %v", err)
	}
	transitioned, err = s.SetLivenessUnverified(missingID, "note")
	if err != nil {
		t.Fatalf("SetLivenessUnverified (missing): %v", err)
	}
	if transitioned {
		t.Errorf("missing-row SetLivenessUnverified transitioned=true; want false (terminal guard)")
	}
	assertLivenessCleared(t, s, missingID)
}

// TestClearLivenessUnverifiedIdempotent pins the clear primitive: it NULLs both
// columns and a second (or absent-row) clear is a no-op that returns no error.
func TestClearLivenessUnverifiedIdempotent(t *testing.T) {
	s, _ := openTempStore(t)
	const id = "liveness-clear-idem-1"
	seedLivenessSet(t, s, id, StateWorking)

	if err := s.ClearLivenessUnverified(id); err != nil {
		t.Fatalf("ClearLivenessUnverified (first): %v", err)
	}
	assertLivenessCleared(t, s, id)

	// Double-clear: idempotent, no error.
	if err := s.ClearLivenessUnverified(id); err != nil {
		t.Fatalf("ClearLivenessUnverified (double): %v", err)
	}
	assertLivenessCleared(t, s, id)

	// Absent row: idempotent no-op.
	if err := s.ClearLivenessUnverified("no-such-row"); err != nil {
		t.Fatalf("ClearLivenessUnverified (absent): %v", err)
	}
}

// TestListLiveSpawnIdentitiesReadShape pins ListLiveSpawnIdentities (SR-8.1):
// it returns one row per live-state spawn (including pending) carrying the
// recorded pid/proc_starttime (zero values when the identity columns are NULL),
// excludes terminal rows, and does not blow up when a row's liveness columns are
// set (the read deliberately carries no liveness fields).
func TestListLiveSpawnIdentitiesReadShape(t *testing.T) {
	s, _ := openTempStore(t)

	// pending row with recorded identity + liveness set.
	if err := s.InsertPending(Spawn{
		ClaudeInstanceID: "live-pending", CWD: "/tmp", TmuxSessionName: "cd-lp", RelayMode: "off",
	}); err != nil {
		t.Fatalf("InsertPending(live-pending): %v", err)
	}
	if err := s.RecordSessionStartIdentity("live-pending", "", "", 4242, "9988"); err != nil {
		t.Fatalf("RecordSessionStartIdentity(live-pending): %v", err)
	}
	if _, err := s.SetLivenessUnverified("live-pending", "probe eacces"); err != nil {
		t.Fatalf("SetLivenessUnverified(live-pending): %v", err)
	}

	// working row with NO recorded identity (columns NULL → zero values).
	if err := s.InsertPending(Spawn{
		ClaudeInstanceID: "live-working", CWD: "/tmp", TmuxSessionName: "cd-lw", RelayMode: "off",
	}); err != nil {
		t.Fatalf("InsertPending(live-working): %v", err)
	}
	if err := s.ApplyHookTransition("live-working", StateWorking, false, "test_seed"); err != nil {
		t.Fatalf("transition live-working: %v", err)
	}

	// terminal (ended) row — must be excluded.
	if err := s.InsertPending(Spawn{
		ClaudeInstanceID: "dead-ended", CWD: "/tmp", TmuxSessionName: "cd-de", RelayMode: "off",
	}); err != nil {
		t.Fatalf("InsertPending(dead-ended): %v", err)
	}
	if err := s.ApplyHookTransition("dead-ended", StateEnded, false, "test_seed"); err != nil {
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
		t.Fatalf("live identities = %d (%v); want 2 (pending + working, ended excluded)", len(byID), ids)
	}
	if _, ok := byID["dead-ended"]; ok {
		t.Errorf("terminal (ended) row leaked into live identities: %v", ids)
	}

	p, ok := byID["live-pending"]
	if !ok {
		t.Fatalf("pending row missing from live identities: %v", ids)
	}
	if p.PID != 4242 || p.ProcStarttime != "9988" {
		t.Errorf("live-pending identity = (pid=%d, starttime=%q); want (4242, 9988)", p.PID, p.ProcStarttime)
	}

	w, ok := byID["live-working"]
	if !ok {
		t.Fatalf("working row missing from live identities: %v", ids)
	}
	if w.PID != 0 || w.ProcStarttime != "" {
		t.Errorf("live-working identity = (pid=%d, starttime=%q); want zero values (NULL columns)", w.PID, w.ProcStarttime)
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
