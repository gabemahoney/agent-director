package store_test

// Life-tagged session history (SR-5.9, SR-8.7): the rotation archive tags each
// entry with the row's life, a re-archive from another life moves the entry,
// and ListSessionHistory reads one life. Rows and history are seeded through
// apitest and read back through the store or apitest's every-life read.

import (
	"reflect"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// historyAllLives returns id's history entries from every life.
func (f *v5Store) historyAllLives(id string) []apitest.HistoryEntry {
	f.t.Helper()
	h, err := apitest.ReadSessionHistoryAllLives(f.path, id)
	if err != nil {
		f.t.Fatalf("ReadSessionHistoryAllLives: %v", err)
	}
	return h
}

// historyIDs returns the session ids ListSessionHistory gives for id in life;
// t is explicit so subtests sharing one store fail on their own t.
func (f *v5Store) historyIDs(t *testing.T, id string, life int64) []string {
	t.Helper()
	h, err := f.s.ListSessionHistory(id, life)
	if err != nil {
		t.Fatalf("ListSessionHistory(%s, %d): %v", id, life, err)
	}
	if h == nil {
		t.Fatalf("ListSessionHistory(%s, %d) = nil; want non-nil", id, life)
	}
	ids := make([]string, 0, len(h))
	for _, e := range h {
		ids = append(ids, e.ClaudeSessionID)
	}
	return ids
}

// rotate records the agent's SessionStart with a new session id, archiving
// the current one; the seeded live row records the agent's pane (SR-22.9).
func (f *v5Store) rotate(id, newSessionID string) {
	f.t.Helper()
	path := storefix.HookTranscript("/x/"+newSessionID+".jsonl", true)
	if got := storefix.ApplyAgentHook(f.t, f.s, id, "SessionStart", newSessionID, path); !got.Applied {
		f.t.Fatalf("SessionStart(%s, %s) = %+v; want applied", id, newSessionID, got)
	}
}

// TestSessionHistoryRotationTagsRowLife: rows in different lives each archive
// their outgoing session into their own life, and the rotation keeps the life.
func TestSessionHistoryRotationTagsRowLife(t *testing.T) {
	f := newV5Store(t)
	for _, life := range []int64{0, 2} {
		id := f.seed(store.StateWaiting, "A", apitest.WithLifeNumber(life), apitest.WithJsonlPath("/x/A.jsonl"))
		f.rotate(id, "B")

		got := f.historyAllLives(id)
		if len(got) != 1 {
			t.Fatalf("life %d: %d history entries; want 1 (the archived A)", life, len(got))
		}
		if e := got[0]; e.ClaudeSessionID != "A" || e.LifeNumber != life || e.JSONLPath.String != "/x/A.jsonl" {
			t.Errorf("life %d: entry = %+v; want A at /x/A.jsonl in life %d", life, e, life)
		}
		row, err := f.s.GetSpawn(id)
		if err != nil {
			t.Fatalf("GetSpawn: %v", err)
		}
		if row.LifeNumber != life || row.ClaudeSessionID != "B" {
			t.Errorf("life %d: row life/session = %d/%q; want %d/B (rotation keeps the life)", life, row.LifeNumber, row.ClaudeSessionID, life)
		}
	}
}

// TestSessionHistoryReArchiveAcrossLives: re-archiving X from a row whose own
// life is 1 moves an earlier life's entry to life 1 with the row's path (NULL
// included); a same-life re-archive keeps today's COALESCE rule.
func TestSessionHistoryReArchiveAcrossLives(t *testing.T) {
	cases := []struct {
		name      string
		priorLife int64
		rowPath   string // "" leaves the row's jsonl_path NULL
		wantPath  string // "" = NULL
	}{
		{"cross-life path known", 0, "/x/X-life1.jsonl", "/x/X-life1.jsonl"},
		{"cross-life path NULL", 0, "", ""},
		{"same-life path known", 1, "/x/X-life1.jsonl", "/x/X-life1.jsonl"},
		{"same-life path NULL keeps known path", 1, "", "/x/X-old.jsonl"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newV5Store(t)
			seededAt := time.Now().Add(-time.Hour)
			opts := []apitest.SpawnOption{
				apitest.WithLifeNumber(1),
				apitest.WithSessionHistory(apitest.SessionHistorySeed{
					SessionID: "X", JSONLPath: "/x/X-old.jsonl", Life: tc.priorLife, RecordedAt: seededAt,
				}),
			}
			if tc.rowPath != "" {
				opts = append(opts, apitest.WithJsonlPath(tc.rowPath))
			}
			id := f.seed(store.StateWaiting, "X", opts...)
			before := f.historyAllLives(id)[0].RecordedAt

			f.rotate(id, "Y")

			got := f.historyAllLives(id)
			if len(got) != 1 {
				t.Fatalf("%d history entries; want 1 (one entry per session id)", len(got))
			}
			e := got[0]
			if e.ClaudeSessionID != "X" || e.LifeNumber != 1 {
				t.Errorf("entry session/life = %q/%d; want X/1", e.ClaudeSessionID, e.LifeNumber)
			}
			if e.JSONLPath.Valid != (tc.wantPath != "") || e.JSONLPath.String != tc.wantPath {
				t.Errorf("entry path = %+v; want %q (\"\" = NULL)", e.JSONLPath, tc.wantPath)
			}
			if e.RecordedAt <= before {
				t.Errorf("recorded_at = %q; want strictly newer than seeded %q", e.RecordedAt, before)
			}
			if ids := f.historyIDs(t, id, 0); len(ids) != 0 {
				t.Errorf("life 0 read = %v; want empty (entry moved to life 1)", ids)
			}
			if ids := f.historyIDs(t, id, 1); !reflect.DeepEqual(ids, []string{"X"}) {
				t.Errorf("life 1 read = %v; want [X]", ids)
			}
		})
	}
}

// TestSessionHistoryReadFiltersByLife: each life's read returns only that
// life's entries of that id, newest first; an empty life or absent id reads empty.
func TestSessionHistoryReadFiltersByLife(t *testing.T) {
	f := newV5Store(t)
	base := time.Now().Add(-10 * time.Hour)
	entry := func(sid string, life int64, hours int) apitest.SpawnOption {
		return apitest.WithSessionHistory(apitest.SessionHistorySeed{
			SessionID: sid, JSONLPath: "/x/" + sid + ".jsonl", Life: life,
			RecordedAt: base.Add(time.Duration(hours) * time.Hour),
		})
	}
	id := f.seed(store.StateWaiting, "cur", apitest.WithLifeNumber(1),
		entry("A0", 0, 1), entry("B1", 1, 3), entry("C0", 0, 5), entry("D1", 1, 7))
	// A second id whose entries interleave in time with the first id's.
	other := f.seed(store.StateWaiting, "cur", apitest.WithLifeNumber(1),
		entry("O0", 0, 2), entry("O1", 1, 6))

	cases := []struct {
		name string
		id   string
		life int64
		want []string
	}{
		{"life 0", id, 0, []string{"C0", "A0"}},
		{"life 1", id, 1, []string{"D1", "B1"}},
		{"other id life 1", other, 1, []string{"O1"}},
		{"life with no entries", id, 2, []string{}},
		{"absent instance", "no-such-id", 0, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := f.historyIDs(t, tc.id, tc.life); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ListSessionHistory(%s, %d) = %v; want %v", tc.id, tc.life, got, tc.want)
			}
		})
	}
}
