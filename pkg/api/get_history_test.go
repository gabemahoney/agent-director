package api_test

import (
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
	return openDB(t, dbPath), dbPath
}

// assertGetHistory checks status, prior_sessions (ids, paths, recorded_at,
// order) and that an empty prior_sessions encodes as [].
func assertGetHistory(t *testing.T, got api.SpawnRow, wantStatus string, want []getWantPrior) {
	t.Helper()
	if got.TranscriptStatus != wantStatus {
		t.Errorf("TranscriptStatus = %q; want %q", got.TranscriptStatus, wantStatus)
	}
	if len(got.PriorSessions) != len(want) {
		t.Fatalf("PriorSessions = %+v; want %d entries %v", got.PriorSessions, len(want), want)
	}
	for i, w := range want {
		p := got.PriorSessions[i]
		if p.ClaudeSessionID != w.id || p.JSONLPath != w.path || p.RecordedAt == "" {
			t.Errorf("PriorSessions[%d] = %+v; want (%q, %q) with a recorded_at", i, p, w.id, w.path)
		}
		if !w.at.IsZero() {
			at, err := time.Parse(time.RFC3339Nano, p.RecordedAt)
			if err != nil {
				at, err = time.Parse("2006-01-02 15:04:05", p.RecordedAt) // the store's timestamp text
			}
			if err != nil || !at.Equal(w.at) {
				t.Errorf("PriorSessions[%d].RecordedAt = %q; want %s", i, p.RecordedAt, w.at.Format(time.RFC3339))
			}
		}
	}
	if out := jsonOf(t, got); len(want) == 0 && !strings.Contains(out, `"prior_sessions":[]`) {
		t.Errorf("JSON does not encode empty prior_sessions as []; got %s", out)
	}
}

// TestGetHistoryVisible pins SR-8.7 on get through the real store and is the
// b.v2c AC8 regression: prior_sessions and transcript_status (no_session,
// present, never_written, rotated) cover only the visible history, the row's
// life minus the entry for its current, non-empty session id, newest first.
func TestGetHistoryVisible(t *testing.T) {
	t.Parallel()
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
			history:    []apitest.SessionHistorySeed{getHist("old", "/h/old.jsonl", 0, 1), getHist("cur", "/h/cur.jsonl", 0, 2)},
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
			name:       "no_session_drops_nothing",
			history:    []apitest.SessionHistorySeed{getHist("s1", "/h/s1.jsonl", 0, 1), getHist("s2", "", 0, 2)},
			wantStatus: "no_session",
			wantPrior:  []getWantPrior{{"s2", "", getHistAt(2)}, {"s1", "/h/s1.jsonl", getHistAt(1)}},
		},
		{
			name: "life1_row_lists_only_its_life_newest_first", life: 1, sessionID: "cur",
			history: []apitest.SessionHistorySeed{
				getHist("x0", "/h/x0.jsonl", 0, 1), getHist("y1", "/h/y1.jsonl", 1, 2), getHist("cur", "/h/cur.jsonl", 1, 3),
				getHist("z0", "/h/z0.jsonl", 0, 4), getHist("w1", "", 1, 5),
			},
			wantStatus: "rotated",
			wantPrior:  []getWantPrior{{"w1", "", getHistAt(5)}, {"y1", "/h/y1.jsonl", getHistAt(2)}},
		},
		{
			name: "life1_row_never_lists_life0_entries", life: 1, sessionID: "cur",
			history:    []apitest.SessionHistorySeed{getHist("x", "/h/x.jsonl", 0, 1)},
			wantStatus: "never_written",
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
			if all, err := apitest.ReadSessionHistoryAllLives(dbPath, id); err != nil || len(all) != len(tc.history) {
				t.Errorf("store holds %d history entries (%v); want all %d seeded (get must not delete)", len(all), err, len(tc.history))
			}
		})
	}
}

// TestGetHistoryHookRotationWithinLife pins AC-REUSE-24 (first sentence): an
// entry archived by a SessionStart rotation joins the row's life and is listed
// by get alongside that life's older entries; the new current id is not.
func TestGetHistoryHookRotationWithinLife(t *testing.T) {
	t.Parallel()
	const id, life = "id-get-hist-hook", 1
	s, dbPath := seedGetHistoryRow(t, id, "s-old", apitest.WithLifeNumber(life), apitest.WithJsonlPath("/h/s-old.jsonl"),
		apitest.WithSessionHistory(getHist("s-older", "/h/s-older.jsonl", life, 1)))
	// The new session's transcript is not on disk yet: the hook archives s-old
	// and leaves jsonl_path NULL. SR-22.9: it comes from the row's own pane process.
	if got := apitest.ApplyAgentHook(t, dbPath, id, "SessionStart", "s-new",
		apitest.HookTranscript("/h/s-new.jsonl", false)); !got.Applied {
		t.Fatalf("SessionStart = %+v; want applied", got)
	}
	got, err := api.Get(s, id)
	if err != nil || got.ClaudeSessionID != "s-new" {
		t.Fatalf("Get = %q, %v; want ClaudeSessionID s-new", got.ClaudeSessionID, err)
	}
	assertGetHistory(t, got, "rotated", []getWantPrior{{id: "s-old", path: "/h/s-old.jsonl"}, {"s-older", "/h/s-older.jsonl", getHistAt(1)}})
	all, err := apitest.ReadSessionHistoryAllLives(dbPath, id)
	if err != nil {
		t.Fatalf("ReadSessionHistoryAllLives: %v", err)
	}
	for _, e := range all {
		if e.LifeNumber != life {
			t.Errorf("stored entry %q in life %d; want %d (the row's life)", e.ClaudeSessionID, e.LifeNumber, life)
		}
	}
}
