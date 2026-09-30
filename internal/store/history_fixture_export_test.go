package store

// Export-for-test shim: hands the v4 history fixture (migration_fixtures_test.go)
// to the external history_migrated_verbs_test.go, migrated through the real
// {4→schemaVersion} sentinel flow. It seeds and migrates nothing itself.

import "testing"

// V4HistoryEntry is one pre-migration session_history entry of a fixture row.
type V4HistoryEntry struct {
	SessionID, JSONLPath, RecordedAt string // "" for NULL
}

// MigratedV4History is the migrated fixture store plus its pre-migration description.
type MigratedV4History struct {
	Dir, Path string

	Rotated, NeverWritten, HoldsCurrent, InterleavedA, InterleavedB, Pending string

	// CurrentSessionID and CWD are each row's pre-migration values ("" for NULL).
	CurrentSessionID, CWD map[string]string
	// History is each row's pre-migration entries, newest first.
	History map[string][]V4HistoryEntry
}

// MigrateV4HistoryFixture builds the v4 history fixture in a temp dir, migrates
// it via writeSentinel(4, schemaVersion) + Open, and returns it closed.
func MigrateV4HistoryFixture(t *testing.T) MigratedV4History {
	t.Helper()
	f := makeV4HistoryFixture(t, t.TempDir())
	migrateV4Fixture(t, f)
	if v := readUserVersion(t, f.path); v != schemaVersion {
		t.Fatalf("MigrateV4HistoryFixture: user_version = %d; want %d", v, schemaVersion)
	}
	assertSentinelAbsent(t, f.dir)

	out := MigratedV4History{
		Dir: f.dir, Path: f.path,
		Rotated: f.rotated, NeverWritten: f.neverWritten, HoldsCurrent: f.holdsCurrent,
		InterleavedA: f.interleavedA, InterleavedB: f.interleavedB, Pending: f.pending,
		CurrentSessionID: make(map[string]string, len(f.ids)),
		CWD:              make(map[string]string, len(f.ids)),
		History:          make(map[string][]V4HistoryEntry, len(f.ids)),
	}
	for _, id := range f.ids {
		out.CurrentSessionID[id] = unquote(f.rows[id]["claude_session_id"])
		out.CWD[id] = unquote(f.rows[id]["cwd"])
		entries := []V4HistoryEntry{}
		for _, h := range f.history[id] {
			entries = append(entries, V4HistoryEntry{
				SessionID:  unquote(h["claude_session_id"]),
				JSONLPath:  unquote(h["jsonl_path"]),
				RecordedAt: unquote(h["recorded_at"]),
			})
		}
		out.History[id] = entries
	}
	return out
}
