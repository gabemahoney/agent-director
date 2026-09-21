package store

// session_history_test.go — b.v2c store-level coverage for the session-rotation
// archive, lazy transcript heal, provisional-transcript listing, and the
// one-shot repair path. All tests drive the real *Store against a temp SQLite
// DB (openTempStore) and read back through the store API, never internals.

import (
	"errors"
	"path/filepath"
	"testing"
)

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

// TestRepairTranscriptReassociatesAndArchives is the b.v2c AC7 store-layer
// REGRESSION test: the one-shot operator repair path re-associates an orphaned
// transcript with a row and archives the pointer it overwrites (so a repair
// never silently discards history). It also proves an absent instance yields
// ErrSpawnNotFound.
func TestRepairTranscriptReassociatesAndArchives(t *testing.T) {
	s, _ := openTempStore(t)
	const id = "repair-1"
	seedWaitingRow(t, s, id)

	// Current (dead) pointer.
	if err := s.RecordSessionStartIdentity(id, "session-dead", "/x/session-dead.jsonl", true, 1, "1"); err != nil {
		t.Fatalf("record current: %v", err)
	}

	// Repair to the recovered orphaned transcript.
	recovered := filepath.Join(t.TempDir(), "session-recovered.jsonl")
	if err := s.RepairTranscript(id, "session-recovered", recovered); err != nil {
		t.Fatalf("RepairTranscript: %v", err)
	}

	row, _ := s.GetSpawn(id)
	if row.ClaudeSessionID != "session-recovered" || row.JSONLPath != recovered {
		t.Errorf("row = (%q, %q); want (session-recovered, %q)", row.ClaudeSessionID, row.JSONLPath, recovered)
	}

	// The overwritten pair is archived, not discarded.
	hist, err := s.ListSessionHistory(id)
	if err != nil {
		t.Fatalf("ListSessionHistory: %v", err)
	}
	if len(hist) != 1 || hist[0].ClaudeSessionID != "session-dead" {
		t.Fatalf("history = %+v; want the archived session-dead pair", hist)
	}

	// Absent instance → ErrSpawnNotFound.
	if err := s.RepairTranscript("ghost", "s", "/x/p.jsonl"); !errors.Is(err, ErrSpawnNotFound) {
		t.Errorf("RepairTranscript(ghost) err = %v; want ErrSpawnNotFound", err)
	}
}
