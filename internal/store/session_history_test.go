package store

// session_history_test.go — b.v2c store-level coverage for the session-rotation
// archive, lazy transcript heal, and provisional-transcript listing. Tests drive
// the real *Store against a temp SQLite DB (openTempStore) and read back through
// the store API. The exceptions are the b.5jm/4 upsert helpers (countHistoryRows,
// historyRecordedAt, and the backdate helper) which query s.db directly: row
// count and recorded_at are not exposed by the store API but are exactly what the
// upsert semantics must be pinned on.

import (
	"fmt"
	"testing"
)

// countHistoryRows returns how many session_history rows exist for the given
// (instance, session) pair — used to prove the upsert never duplicates.
func countHistoryRows(t *testing.T, s *Store, instanceID, sessionID string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM session_history
		  WHERE claude_instance_id = ? AND claude_session_id = ?`,
		instanceID, sessionID,
	).Scan(&n); err != nil {
		t.Fatalf("count history rows: %v", err)
	}
	return n
}

// historyJsonl reads back the archived jsonl_path for one (instance, session)
// pair through the store API (empty string when the column is NULL).
func historyJsonl(t *testing.T, s *Store, instanceID, sessionID string) string {
	t.Helper()
	hist, err := s.ListSessionHistory(instanceID)
	if err != nil {
		t.Fatalf("ListSessionHistory: %v", err)
	}
	for _, h := range hist {
		if h.ClaudeSessionID == sessionID {
			return h.JSONLPath
		}
	}
	t.Fatalf("no history entry for session %q", sessionID)
	return ""
}

// backdateHistoryRecordedAt rewinds one (instance, session) entry's recorded_at
// by the given whole seconds, so a same-run re-archive (SQLite CURRENT_TIMESTAMP
// is second-granular) produces a strictly newer timestamp the refresh assertion
// can observe.
func backdateHistoryRecordedAt(t *testing.T, s *Store, instanceID, sessionID string, seconds int) {
	t.Helper()
	if _, err := s.db.Exec(
		`UPDATE session_history
		    SET recorded_at = datetime(recorded_at, ?)
		  WHERE claude_instance_id = ? AND claude_session_id = ?`,
		fmt.Sprintf("-%d seconds", seconds), instanceID, sessionID,
	); err != nil {
		t.Fatalf("backdate recorded_at: %v", err)
	}
}

// historyRecordedAt reads the recorded_at for one (instance, session) pair.
func historyRecordedAt(t *testing.T, s *Store, instanceID, sessionID string) string {
	t.Helper()
	var at string
	if err := s.db.QueryRow(
		`SELECT recorded_at FROM session_history
		  WHERE claude_instance_id = ? AND claude_session_id = ?`,
		instanceID, sessionID,
	).Scan(&at); err != nil {
		t.Fatalf("read recorded_at: %v", err)
	}
	return at
}

// seedWaitingRow inserts one pending row and transitions it to waiting (a live
// state ListProvisionalTranscripts scans), returning nothing extra.
func seedWaitingRow(t *testing.T, s *Store, id string) {
	t.Helper()
	if err := s.InsertPending(Spawn{
		ClaudeInstanceID: id, CWD: "/tmp", TmuxSessionName: "cd-" + id, RelayMode: "off",
	}); err != nil {
		t.Fatalf("InsertPending(%q): %v", id, err)
	}
	if err := s.ApplyHookTransition(id, StateWaiting, false, "test_seed"); err != nil {
		t.Fatalf("ApplyHookTransition(%q, waiting): %v", id, err)
	}
}

// TestSessionRotationArchivesPriorTranscript is the b.v2c AC6 REGRESSION test
// for bug mode (b) at the store layer: when a SessionStart arrives with a
// session id different from the one already recorded, the prior
// (claude_session_id, jsonl_path) pair is archived into session_history — a
// queryable link back to the transcript a rotation would otherwise orphan.
//
// PRE-FIX there is no session_history and the prior pair is silently overwritten;
// POST-FIX ListSessionHistory returns the archived prior session with its path.
func TestSessionRotationArchivesPriorTranscript(t *testing.T) {
	s, _ := openTempStore(t)
	const id = "rot-archive-1"
	seedWaitingRow(t, s, id)

	// First session with a real transcript path.
	if err := s.RecordSessionStartIdentity(id, "session-A", "/x/session-A.jsonl", true, 100, "1"); err != nil {
		t.Fatalf("record session A: %v", err)
	}
	// Rotation: a different session id must archive the A pair before overwriting.
	if err := s.RecordSessionStartIdentity(id, "session-B", "/x/session-B.jsonl", true, 200, "2"); err != nil {
		t.Fatalf("record session B: %v", err)
	}

	hist, err := s.ListSessionHistory(id)
	if err != nil {
		t.Fatalf("ListSessionHistory: %v", err)
	}
	if len(hist) != 1 {
		t.Fatalf("len(history) = %d; want 1 (the archived prior session)", len(hist))
	}
	if hist[0].ClaudeSessionID != "session-A" {
		t.Errorf("archived session id = %q; want session-A", hist[0].ClaudeSessionID)
	}
	if hist[0].JSONLPath != "/x/session-A.jsonl" {
		t.Errorf("archived jsonl_path = %q; want /x/session-A.jsonl", hist[0].JSONLPath)
	}

	// The live row now holds session B — the prior pointer is not orphaned, it is
	// queryable via history.
	row, _ := s.GetSpawn(id)
	if row.ClaudeSessionID != "session-B" {
		t.Errorf("current session id = %q; want session-B", row.ClaudeSessionID)
	}
}

// TestSessionRotationNoArchiveWhenSameSession pins the guard: a same-session
// re-fire (identical session id) and a fresh spawn's first SessionStart (empty
// prior id) both archive nothing — history is written only on a genuine change.
func TestSessionRotationNoArchiveWhenSameSession(t *testing.T) {
	s, _ := openTempStore(t)
	const id = "rot-noarchive-1"
	seedWaitingRow(t, s, id)

	// Fresh spawn's first SessionStart: empty prior id → no archive.
	if err := s.RecordSessionStartIdentity(id, "session-X", "/x/session-X.jsonl", true, 1, "1"); err != nil {
		t.Fatalf("record fresh: %v", err)
	}
	// Same-session re-fire → no archive.
	if err := s.RecordSessionStartIdentity(id, "session-X", "/x/session-X.jsonl", true, 2, "2"); err != nil {
		t.Fatalf("record re-fire: %v", err)
	}

	hist, err := s.ListSessionHistory(id)
	if err != nil {
		t.Fatalf("ListSessionHistory: %v", err)
	}
	if len(hist) != 0 {
		t.Fatalf("len(history) = %d; want 0 (no rotation happened)", len(hist))
	}
}

// TestRecordSessionStartIdentityReportedButAbsentNullsPath is the b.v2c AC1
// store-layer REGRESSION test for bug mode (a): when the hook reports a
// transcript path but the file is not on disk (jsonlPresent=false), the store
// must NOT record it — the column is forced NULL so the row never asserts a dead
// pointer. A previously-recorded path is likewise cleared.
//
// PRE-FIX the store wrote the reported path unconditionally; POST-FIX a
// reported-but-absent path lands as NULL (read back as "").
func TestRecordSessionStartIdentityReportedButAbsentNullsPath(t *testing.T) {
	s, _ := openTempStore(t)
	const id = "ab-null-1"
	seedWaitingRow(t, s, id)

	// A real, present transcript is recorded.
	if err := s.RecordSessionStartIdentity(id, "session-1", "/x/present.jsonl", true, 1, "1"); err != nil {
		t.Fatalf("record present: %v", err)
	}
	if row, _ := s.GetSpawn(id); row.JSONLPath != "/x/present.jsonl" {
		t.Fatalf("precondition: JSONLPath = %q; want /x/present.jsonl", row.JSONLPath)
	}

	// Same session, now the reported path is NOT present on disk → force NULL.
	if err := s.RecordSessionStartIdentity(id, "session-1", "/x/gone.jsonl", false, 1, "1"); err != nil {
		t.Fatalf("record reported-but-absent: %v", err)
	}
	if row, _ := s.GetSpawn(id); row.JSONLPath != "" {
		t.Errorf("JSONLPath = %q; want \"\" (NULL — reported path not on disk)", row.JSONLPath)
	}
}

// TestHealJsonlPathRecordsOnlyWhenNull pins the lazy-heal store primitive
// (b.v2c AC3): HealJsonlPath writes a path only onto a row whose jsonl_path is
// currently NULL and whose session id matches, and reports whether it wrote.
func TestHealJsonlPathRecordsOnlyWhenNull(t *testing.T) {
	s, _ := openTempStore(t)
	const id = "heal-1"
	seedWaitingRow(t, s, id)

	// Provisional row: session id set, jsonl_path NULL (reported-but-absent).
	if err := s.RecordSessionStartIdentity(id, "session-1", "/x/notyet.jsonl", false, 1, "1"); err != nil {
		t.Fatalf("record provisional: %v", err)
	}

	wrote, err := s.HealJsonlPath(id, "session-1", "/x/appeared.jsonl")
	if err != nil {
		t.Fatalf("HealJsonlPath: %v", err)
	}
	if !wrote {
		t.Fatalf("HealJsonlPath reported no write; want true (row had NULL path)")
	}
	if row, _ := s.GetSpawn(id); row.JSONLPath != "/x/appeared.jsonl" {
		t.Errorf("JSONLPath = %q; want /x/appeared.jsonl", row.JSONLPath)
	}

	// A second heal must NOT clobber the now-present path (guard: no-op, false).
	wrote, err = s.HealJsonlPath(id, "session-1", "/x/other.jsonl")
	if err != nil {
		t.Fatalf("HealJsonlPath (second): %v", err)
	}
	if wrote {
		t.Errorf("HealJsonlPath clobbered an already-present path; want no-op")
	}
	if row, _ := s.GetSpawn(id); row.JSONLPath != "/x/appeared.jsonl" {
		t.Errorf("JSONLPath = %q; want unchanged /x/appeared.jsonl", row.JSONLPath)
	}
}

// TestListProvisionalTranscripts pins the find-missing input query (b.v2c AC3):
// only LIVE rows with a session id and a NULL jsonl_path are returned, and the
// row's CLAUDE_CONFIG_DIR is surfaced so the sweep can recompose the path.
func TestListProvisionalTranscripts(t *testing.T) {
	s, _ := openTempStore(t)

	// Provisional live row (NULL path) — should be listed.
	seedWaitingRow(t, s, "prov-1")
	if err := s.RecordSessionStartIdentity("prov-1", "session-prov", "/x/none.jsonl", false, 1, "1"); err != nil {
		t.Fatalf("record prov-1: %v", err)
	}

	// Live row WITH a present path — must NOT be listed.
	seedWaitingRow(t, s, "present-1")
	if err := s.RecordSessionStartIdentity("present-1", "session-present", "/x/here.jsonl", true, 1, "1"); err != nil {
		t.Fatalf("record present-1: %v", err)
	}

	got, err := s.ListProvisionalTranscripts()
	if err != nil {
		t.Fatalf("ListProvisionalTranscripts: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(provisional) = %d; want 1 (only the NULL-path live row)", len(got))
	}
	if got[0].ClaudeInstanceID != "prov-1" || got[0].ClaudeSessionID != "session-prov" {
		t.Errorf("provisional = %+v; want prov-1/session-prov", got[0])
	}
}

// TestReArchiveFillsNullPathAndRefreshesRecordedAt is the b.5jm/4 (AC7)
// REGRESSION test: when a session id that already has a NULL-path history entry
// re-enters the row and rotates out again with a now-known transcript path, the
// archive must UPDATE the existing entry — filling in the path and refreshing
// recorded_at — rather than ignoring the write (the old INSERT OR IGNORE).
//
// PRE-FIX (INSERT OR IGNORE) the second archive of session-A is discarded: its
// jsonl_path stays NULL and recorded_at stale, so this test's path assertion
// fails. POST-FIX the upsert fills the path and there is still exactly one A row.
func TestReArchiveFillsNullPathAndRefreshesRecordedAt(t *testing.T) {
	s, _ := openTempStore(t)
	const id = "rearchive-fill-1"
	seedWaitingRow(t, s, id)

	// Session A starts with a reported-but-absent path → row=A, jsonl_path NULL.
	if err := s.RecordSessionStartIdentity(id, "session-A", "/x/A.jsonl", false, 1, "1"); err != nil {
		t.Fatalf("record A (absent): %v", err)
	}
	// Rotate A→B: archives (A, NULL) — the row's current path was NULL.
	if err := s.RecordSessionStartIdentity(id, "session-B", "/x/B.jsonl", true, 2, "2"); err != nil {
		t.Fatalf("record B: %v", err)
	}
	if got := historyJsonl(t, s, id, "session-A"); got != "" {
		t.Fatalf("precondition: archived A path = %q; want empty (NULL)", got)
	}
	// Backdate the archived A entry so the re-archive's CURRENT_TIMESTAMP is
	// strictly newer (SQLite timestamps are second-granular; without this the
	// same-run re-archive lands on an equal second and the refresh is unobservable).
	backdateHistoryRecordedAt(t, s, id, "session-A", 10)
	firstRecordedAt := historyRecordedAt(t, s, id, "session-A")

	// Session A re-enters the row, this time with a present transcript path.
	// Rotate B→A archives (B, …); the row now holds A with a known path.
	if err := s.RecordSessionStartIdentity(id, "session-A", "/x/A-found.jsonl", true, 3, "3"); err != nil {
		t.Fatalf("record A (found): %v", err)
	}
	// Rotate A→B again: re-archives (A, /x/A-found.jsonl) — must UPSERT the
	// existing NULL-path A entry, filling the path.
	if err := s.RecordSessionStartIdentity(id, "session-B", "/x/B.jsonl", true, 4, "4"); err != nil {
		t.Fatalf("re-archive A via A→B: %v", err)
	}

	if got := historyJsonl(t, s, id, "session-A"); got != "/x/A-found.jsonl" {
		t.Errorf("archived A path = %q; want /x/A-found.jsonl (upsert filled the NULL)", got)
	}
	if n := countHistoryRows(t, s, id, "session-A"); n != 1 {
		t.Errorf("session-A history rows = %d; want 1 (upsert must not duplicate)", n)
	}
	// The upsert's recorded_at = CURRENT_TIMESTAMP must strictly advance past the
	// backdated original. PRE-FIX (OR IGNORE) the re-archive is discarded, so
	// recorded_at stays the backdated value and this assertion goes red.
	if at := historyRecordedAt(t, s, id, "session-A"); at <= firstRecordedAt {
		t.Errorf("recorded_at = %q; want strictly newer than backdated %q (upsert must refresh)", at, firstRecordedAt)
	}
}

// TestReArchiveWithNullDoesNotClobberKnownPath is the b.5jm/4 (AC7) COALESCE
// guard: re-archiving a session id that already has a NON-NULL path entry, this
// time with a NULL path, must KEEP the recorded path (COALESCE(excluded, existing)).
func TestReArchiveWithNullDoesNotClobberKnownPath(t *testing.T) {
	s, _ := openTempStore(t)
	const id = "rearchive-keep-1"
	seedWaitingRow(t, s, id)

	// Session A starts with a present path → row=A with /x/A.jsonl.
	if err := s.RecordSessionStartIdentity(id, "session-A", "/x/A.jsonl", true, 1, "1"); err != nil {
		t.Fatalf("record A (present): %v", err)
	}
	// Rotate A→B archives (A, /x/A.jsonl).
	if err := s.RecordSessionStartIdentity(id, "session-B", "/x/B.jsonl", true, 2, "2"); err != nil {
		t.Fatalf("record B: %v", err)
	}
	if got := historyJsonl(t, s, id, "session-A"); got != "/x/A.jsonl" {
		t.Fatalf("precondition: archived A path = %q; want /x/A.jsonl", got)
	}

	// Session A re-enters reported-but-absent → row=A with NULL path.
	if err := s.RecordSessionStartIdentity(id, "session-A", "/x/A.jsonl", false, 3, "3"); err != nil {
		t.Fatalf("record A (absent re-entry): %v", err)
	}
	// Rotate A→B re-archives (A, NULL) — COALESCE must keep the known path.
	if err := s.RecordSessionStartIdentity(id, "session-B", "/x/B.jsonl", true, 4, "4"); err != nil {
		t.Fatalf("re-archive A (NULL) via A→B: %v", err)
	}

	if got := historyJsonl(t, s, id, "session-A"); got != "/x/A.jsonl" {
		t.Errorf("archived A path = %q; want /x/A.jsonl kept (COALESCE must not clobber)", got)
	}
	if n := countHistoryRows(t, s, id, "session-A"); n != 1 {
		t.Errorf("session-A history rows = %d; want 1", n)
	}
}
