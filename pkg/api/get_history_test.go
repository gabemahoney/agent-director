package api_test

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// getWantPrior is one expected prior_sessions element; zero at skips the
// recorded_at check (hook-archived entries are stamped "now").
type getWantPrior struct {
	id, path string
	at       time.Time
}

// getHistAt returns a fixed recorded_at, h hours after a base instant.
func getHistAt(h int) time.Time {
	return time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(h) * time.Hour)
}

// getHist builds a SessionHistorySeed.
func getHist(id, path string, life int64, h int) apitest.SessionHistorySeed {
	return apitest.SessionHistorySeed{SessionID: id, JSONLPath: path, Life: life, RecordedAt: getHistAt(h)}
}

// seedGetHistoryRow seeds one row into a fresh store and returns the opened
// real store and its path.
func seedGetHistoryRow(t *testing.T, id, sessionID string, opts ...apitest.SpawnOption) (*store.Store, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	if _, err := apitest.SeedSpawn(dbPath, id, store.StateWaiting, "/tmp", "off", sessionID, true, opts...); err != nil {
		t.Fatalf("SeedSpawn: %v", err)
	}
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, dbPath
}

// getParseRecordedAt accepts the store's timestamp text or RFC3339.
func getParseRecordedAt(t *testing.T, s string) time.Time {
	t.Helper()
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05"} {
		if at, err := time.Parse(layout, s); err == nil {
			return at
		}
	}
	t.Fatalf("recorded_at %q parses as neither RFC3339 nor the store layout", s)
	return time.Time{}
}

// assertGetHistory checks status, prior_sessions (ids, paths, recorded_at,
// order) and that an empty prior_sessions encodes as [].
func assertGetHistory(t *testing.T, got api.SpawnRow, wantStatus string, want []getWantPrior) {
	t.Helper()
	if got.TranscriptStatus != wantStatus {
		t.Errorf("TranscriptStatus = %q; want %q", got.TranscriptStatus, wantStatus)
	}
	var ids []string
	for _, p := range got.PriorSessions {
		ids = append(ids, p.ClaudeSessionID)
	}
	if len(got.PriorSessions) != len(want) {
		t.Fatalf("PriorSessions ids = %v; want %d entries %v", ids, len(want), want)
	}
	for i, w := range want {
		p := got.PriorSessions[i]
		if p.ClaudeSessionID != w.id || p.JSONLPath != w.path {
			t.Errorf("PriorSessions[%d] = (%q, %q); want (%q, %q) (all ids %v)", i, p.ClaudeSessionID, p.JSONLPath, w.id, w.path, ids)
		}
		if p.RecordedAt == "" {
			t.Errorf("PriorSessions[%d].RecordedAt is empty", i)
		} else if !w.at.IsZero() && !getParseRecordedAt(t, p.RecordedAt).Equal(w.at) {
			t.Errorf("PriorSessions[%d].RecordedAt = %q; want %s", i, p.RecordedAt, w.at.Format(time.RFC3339))
		}
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if len(want) == 0 && !strings.Contains(string(raw), `"prior_sessions":[]`) {
		t.Errorf("JSON does not encode empty prior_sessions as []; got %s", raw)
	}
}

// TestGetHistoryVisible pins SR-8.7 on get through the real store:
// prior_sessions and transcript_status cover only the visible history (the
// row's life, minus the entry for its current, non-empty session id).
func TestGetHistoryVisible(t *testing.T) {
	cases := []struct {
		name       string
		life       int64
		sessionID  string
		jsonlPath  string
		history    []apitest.SessionHistorySeed
		wantStatus string
		wantPrior  []getWantPrior
	}{
		{
			name: "current_plus_older_lists_only_older", sessionID: "cur",
			history: []apitest.SessionHistorySeed{
				getHist("old", "/h/old.jsonl", 0, 1),
				getHist("cur", "/h/cur.jsonl", 0, 2),
			},
			wantStatus: "rotated",
			wantPrior:  []getWantPrior{{"old", "/h/old.jsonl", getHistAt(1)}},
		},
		{
			name: "only_current_no_path_is_never_written", sessionID: "cur",
			history:    []apitest.SessionHistorySeed{getHist("cur", "/h/cur.jsonl", 0, 1)},
			wantStatus: "never_written",
		},
		{
			name: "only_current_with_path_is_present", sessionID: "cur", jsonlPath: "/h/cur.jsonl",
			history:    []apitest.SessionHistorySeed{getHist("cur", "/h/cur.jsonl", 0, 1)},
			wantStatus: "present",
		},
		{
			name: "no_session_drops_nothing",
			history: []apitest.SessionHistorySeed{
				getHist("s1", "/h/s1.jsonl", 0, 1),
				getHist("s2", "", 0, 2),
			},
			wantStatus: "no_session",
			wantPrior:  []getWantPrior{{"s2", "", getHistAt(2)}, {"s1", "/h/s1.jsonl", getHistAt(1)}},
		},
		{
			name: "several_older_keep_newest_first", sessionID: "cur",
			history: []apitest.SessionHistorySeed{
				getHist("a", "/h/a.jsonl", 0, 1),
				getHist("c", "/h/c.jsonl", 0, 4),
				getHist("cur", "/h/cur.jsonl", 0, 3),
				getHist("b", "", 0, 2),
			},
			wantStatus: "rotated",
			wantPrior: []getWantPrior{
				{"c", "/h/c.jsonl", getHistAt(4)}, {"b", "", getHistAt(2)}, {"a", "/h/a.jsonl", getHistAt(1)},
			},
		},
		{
			name: "life1_row_never_lists_life0_entries", life: 1, sessionID: "cur",
			history: []apitest.SessionHistorySeed{
				getHist("x", "/h/x.jsonl", 0, 1),
				getHist("y", "/h/y.jsonl", 0, 2),
			},
			wantStatus: "never_written",
		},
		{
			name: "life1_row_lists_only_life1_interleaved", life: 1, sessionID: "cur",
			history: []apitest.SessionHistorySeed{
				getHist("x0", "/h/x0.jsonl", 0, 1),
				getHist("y1", "/h/y1.jsonl", 1, 2),
				getHist("z0", "/h/z0.jsonl", 0, 3),
				getHist("w1", "", 1, 4),
			},
			wantStatus: "rotated",
			wantPrior:  []getWantPrior{{"w1", "", getHistAt(4)}, {"y1", "/h/y1.jsonl", getHistAt(2)}},
		},
		{
			name: "life1_row_drops_current_and_other_life", life: 1, sessionID: "cur",
			history: []apitest.SessionHistorySeed{
				getHist("old1", "/h/old1.jsonl", 1, 1),
				getHist("cur", "/h/cur.jsonl", 1, 2),
				getHist("old0", "/h/old0.jsonl", 0, 3),
			},
			wantStatus: "rotated",
			wantPrior:  []getWantPrior{{"old1", "/h/old1.jsonl", getHistAt(1)}},
		},
		{
			name: "life0_contrast_lists_every_entry", sessionID: "cur",
			history: []apitest.SessionHistorySeed{
				getHist("x0", "/h/x0.jsonl", 0, 1),
				getHist("y1", "/h/y1.jsonl", 0, 2),
				getHist("z0", "/h/z0.jsonl", 0, 3),
				getHist("w1", "", 0, 4),
			},
			wantStatus: "rotated",
			wantPrior: []getWantPrior{
				{"w1", "", getHistAt(4)}, {"z0", "/h/z0.jsonl", getHistAt(3)},
				{"y1", "/h/y1.jsonl", getHistAt(2)}, {"x0", "/h/x0.jsonl", getHistAt(1)},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const id = "id-get-hist"
			opts := []apitest.SpawnOption{apitest.WithLifeNumber(tc.life)}
			if tc.jsonlPath != "" {
				opts = append(opts, apitest.WithJsonlPath(tc.jsonlPath))
			}
			for _, h := range tc.history {
				opts = append(opts, apitest.WithSessionHistory(h))
			}
			s, dbPath := seedGetHistoryRow(t, id, tc.sessionID, opts...)

			got, err := api.Get(s, id)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			assertGetHistory(t, got, tc.wantStatus, tc.wantPrior)

			// Every seeded entry, of every life, is still in the store.
			all, err := apitest.ReadSessionHistoryAllLives(dbPath, id)
			if err != nil {
				t.Fatalf("ReadSessionHistoryAllLives: %v", err)
			}
			if len(all) != len(tc.history) {
				t.Errorf("store holds %d history entries; want all %d seeded (get must not delete)", len(all), len(tc.history))
			}
		})
	}
}

// TestGetHistoryHookRotationWithinLife pins AC-REUSE-24 (first sentence): an
// entry archived by a SessionStart rotation joins the row's life and is listed
// by get alongside that life's older entries; the new current id is not.
func TestGetHistoryHookRotationWithinLife(t *testing.T) {
	for _, life := range []int64{0, 1} {
		t.Run(fmt.Sprintf("life%d", life), func(t *testing.T) {
			const id = "id-get-hist-hook"
			s, dbPath := seedGetHistoryRow(t, id, "s-old",
				apitest.WithLifeNumber(life),
				apitest.WithJsonlPath("/h/s-old.jsonl"),
				apitest.WithSessionHistory(getHist("s-older", "/h/s-older.jsonl", life, 1)),
			)
			// New session reported but its transcript not yet on disk: the
			// hook archives s-old and leaves jsonl_path NULL.
			if err := s.RecordSessionStartIdentity(id, "s-new", "/h/s-new.jsonl", false, 0, ""); err != nil {
				t.Fatalf("RecordSessionStartIdentity: %v", err)
			}

			got, err := api.Get(s, id)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if got.ClaudeSessionID != "s-new" {
				t.Fatalf("ClaudeSessionID = %q; want s-new", got.ClaudeSessionID)
			}
			assertGetHistory(t, got, "rotated", []getWantPrior{
				{id: "s-old", path: "/h/s-old.jsonl"},
				{"s-older", "/h/s-older.jsonl", getHistAt(1)},
			})

			all, err := apitest.ReadSessionHistoryAllLives(dbPath, id)
			if err != nil {
				t.Fatalf("ReadSessionHistoryAllLives: %v", err)
			}
			for _, e := range all {
				if e.LifeNumber != life {
					t.Errorf("stored entry %q in life %d; want %d (the row's life)", e.ClaudeSessionID, e.LifeNumber, life)
				}
			}
		})
	}
}
