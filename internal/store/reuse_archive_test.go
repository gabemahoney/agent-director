package store_test

// Store tests for reuse's keyed archive, permission-request deletion and
// children (SR-10.3, SR-5.9), over reuse_test.go's seedReuseRow and reuseFresh.

import (
	"database/sql"
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// splitHistory returns h's entry for sessionID (counted, to catch a
// duplicate) and every other entry, in order.
func splitHistory(h []apitest.HistoryEntry, sessionID string) (cur []apitest.HistoryEntry, others []apitest.HistoryEntry) {
	others = []apitest.HistoryEntry{}
	for _, e := range h {
		if e.ClaudeSessionID == sessionID {
			cur = append(cur, e)
		} else {
			others = append(others, e)
		}
	}
	return cur, others
}

// TestReuseResetKeyedArchive checks the archive keyed on (id, session id):
// a new entry, an update in place in the ending life, a move from an earlier
// life, nothing for a row with no session; other entries and ids unchanged.
func TestReuseResetKeyedArchive(t *testing.T) {
	const earlier = "/tmp/ad-reuse-test/earlier.jsonl"
	prior := func(path string, life int64) []apitest.SpawnOption {
		return []apitest.SpawnOption{apitest.WithSessionHistory(apitest.SessionHistorySeed{
			SessionID: reuseSessionID, JSONLPath: path, Life: life, RecordedAt: reuseOldRecordedAt,
		})}
	}
	cur := sql.NullString{String: reuseJsonl, Valid: true}
	cases := []struct {
		name     string
		spec     reuseSpec
		wantPath sql.NullString // the current session's entry after the reset
	}{
		{"no prior entry", reuseSpec{}, cur},
		{"same life, same path", reuseSpec{opts: prior(reuseJsonl, reuseLife)}, cur},
		{"same life, different path", reuseSpec{opts: prior(earlier, reuseLife)}, cur},
		{"same life, empty current path keeps the earlier", reuseSpec{noJsonl: true, opts: prior(earlier, reuseLife)},
			sql.NullString{String: earlier, Valid: true}},
		{"earlier life moves and takes the current path", reuseSpec{opts: prior(earlier, 1)}, cur},
		{"earlier life moves and takes NULL", reuseSpec{noJsonl: true, opts: prior(earlier, 1)}, sql.NullString{}},
		{"no session id archives nothing", reuseSpec{noSession: true}, sql.NullString{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newV5Store(t)
			other := f.seed(store.StateEnded, "sess-other-cur", prior(earlier, 1)...)
			otherHistory := f.historyAllLives(other)
			r := seedReuseRow(t, f, tc.spec)
			priorCur, priorOthers := splitHistory(r.history, reuseSessionID)

			res, archived, _, err := f.reuseReset(r, reuseFresh("", false))
			if err != nil || res != store.CondApplied {
				t.Fatalf("ResetForReuse = %v, %v; want CondApplied", res, err)
			}
			gotCur, gotOthers := splitHistory(f.historyAllLives(r.id), reuseSessionID)
			if !reflect.DeepEqual(gotOthers, priorOthers) {
				t.Errorf("other entries %+v -> %+v; want unchanged", priorOthers, gotOthers)
			}
			if got := f.historyAllLives(other); !reflect.DeepEqual(got, otherHistory) {
				t.Errorf("another id's history %+v -> %+v; want unchanged", otherHistory, got)
			}
			if tc.spec.noSession {
				if archived != "" || len(gotCur) != 0 {
					t.Errorf("archived %q, entries %+v; want nothing archived", archived, gotCur)
				}
				return
			}
			if archived != reuseSessionID || len(gotCur) != 1 {
				t.Fatalf("archived %q, entries %+v; want %q archived once", archived, gotCur, reuseSessionID)
			}
			e := gotCur[0]
			if e.LifeNumber != reuseLife || e.JSONLPath != tc.wantPath {
				t.Errorf("entry life %d, path %+v; want %d, %+v", e.LifeNumber, e.JSONLPath, reuseLife, tc.wantPath)
			}
			if len(priorCur) == 1 && e.RecordedAt == reuseOldRecordedText {
				t.Errorf("recorded_at %q; want refreshed", e.RecordedAt)
			}
		})
	}
}

// TestReuseResetPermissionRequestsAndChildren checks every request of the id
// goes, another id's stay, the child keeps its parent and the old history stays.
func TestReuseResetPermissionRequestsAndChildren(t *testing.T) {
	f := newV5Store(t)
	r := seedReuseRow(t, f, reuseSpec{})
	if open, err := f.s.OpenPermissionRequestsForSpawn(r.id); err != nil || len(r.requests) != 2 || len(open) != 1 {
		t.Fatalf("seeded requests %+v, open %d, %v; want one decided and one open", r.requests, len(open), err)
	}
	seedRequest(t, f, r.parent)
	otherRequests := f.reuseRequests(t, r.parent)

	if res, _, _, err := f.reuseReset(r, reuseFresh("", false)); err != nil || res != store.CondApplied {
		t.Fatalf("ResetForReuse = %v, %v; want CondApplied", res, err)
	}
	if got := f.reuseRequests(t, r.id); len(got) != 0 {
		t.Errorf("requests of the reused id after the reset: %+v; want none", got)
	}
	if got := f.reuseRequests(t, r.parent); !reflect.DeepEqual(got, otherRequests) {
		t.Errorf("another id's requests %+v -> %+v; want unchanged", otherRequests, got)
	}
	if got := f.rawColumns(r.child); got.ParentID != r.id || !reflect.DeepEqual(got, r.childBefore) {
		t.Errorf("child %+v -> %+v; want unchanged, parent_id %q", r.childBefore, got, r.id)
	}
	_, priorOthers := splitHistory(r.history, reuseSessionID)
	if _, got := splitHistory(f.historyAllLives(r.id), reuseSessionID); len(priorOthers) == 0 || !reflect.DeepEqual(got, priorOthers) {
		t.Errorf("earlier entries %+v -> %+v; want still attached, unchanged", priorOthers, got)
	}
}
