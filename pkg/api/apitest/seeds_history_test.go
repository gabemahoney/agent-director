package apitest

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// wantHistory is one expected ReadSessionHistoryAllLives entry; path nil =
// NULL, recordedAt "" = the column default (the time of seeding).
type wantHistory struct {
	session    string
	path       any
	life       int64
	recordedAt string
}

// TestSeedSpawn_WithSessionHistory seeds history entries through the option and
// reads them back from every life, newest first.
func TestSeedSpawn_WithSessionHistory(t *testing.T) {
	t.Parallel()
	t1 := time.Date(2021, 3, 4, 5, 6, 7, 0, time.UTC)
	t2 := time.Date(2022, 6, 7, 9, 9, 10, 900_000_000, time.FixedZone("UTC+1", 3600))
	t3 := time.Date(2023, 1, 2, 3, 4, 5, 0, time.UTC)

	cases := []struct {
		name    string
		opts    []SpawnOption
		wantErr string
		want    []wantHistory
	}{
		{
			name: "no option writes no history",
			want: nil,
		},
		{
			name: "lives, NULL path, current id, stated times",
			opts: []SpawnOption{
				WithLifeNumber(1),
				// Seeded out of time order: stated recorded_at, not seeding order, decides.
				WithSessionHistory(SessionHistorySeed{SessionID: "cur", JSONLPath: "/tmp/h/cur.jsonl", Life: 1, RecordedAt: t3}),
				WithSessionHistory(SessionHistorySeed{SessionID: "a", Life: 0, RecordedAt: t1}),
				WithSessionHistory(SessionHistorySeed{SessionID: "b", JSONLPath: "/tmp/h/b.jsonl", Life: 2, RecordedAt: t2}),
			},
			want: []wantHistory{
				{"cur", "/tmp/h/cur.jsonl", 1, "2023-01-02 03:04:05"},
				{"b", "/tmp/h/b.jsonl", 2, "2022-06-07 08:09:10"}, // UTC, whole seconds
				{"a", nil, 0, "2021-03-04 05:06:07"},
			},
		},
		{
			name: "unstated times read as now, later-seeded first",
			opts: []SpawnOption{
				WithSessionHistory(SessionHistorySeed{SessionID: "old", JSONLPath: "/tmp/h/old.jsonl", RecordedAt: t1}),
				WithSessionHistory(SessionHistorySeed{SessionID: "n1", JSONLPath: "/tmp/h/n1.jsonl"}),
				WithSessionHistory(SessionHistorySeed{SessionID: "n2"}),
			},
			want: []wantHistory{
				{"n2", nil, 0, ""},
				{"n1", "/tmp/h/n1.jsonl", 0, ""},
				{"old", "/tmp/h/old.jsonl", 0, "2021-03-04 05:06:07"},
			},
		},
		{
			name: "duplicate session id is rejected across lives and paths",
			opts: []SpawnOption{
				WithSessionHistory(SessionHistorySeed{SessionID: "dup", JSONLPath: "/tmp/h/dup.jsonl", Life: 0}),
				WithSessionHistory(SessionHistorySeed{SessionID: "other"}),
				WithSessionHistory(SessionHistorySeed{SessionID: "dup", Life: 1}),
			},
			wantErr: `duplicate session id "dup"`,
			want:    nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dbPath := filepath.Join(t.TempDir(), "state.db")
			const id = "hist-seed"
			before := time.Now().UTC().Truncate(time.Second)
			_, err := SeedSpawn(dbPath, id, "waiting", "/tmp", "off", "cur", true, tc.opts...)
			after := time.Now().UTC()
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("SeedSpawn: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("SeedSpawn err = %v; want one containing %q", err, tc.wantErr)
			}

			got, err := ReadSessionHistoryAllLives(dbPath, id)
			if err != nil {
				t.Fatalf("ReadSessionHistoryAllLives: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d entries %+v; want %d", len(got), got, len(tc.want))
			}
			for i, w := range tc.want {
				e := got[i]
				var path any
				if e.JSONLPath.Valid {
					path = e.JSONLPath.String
				}
				if e.ClaudeSessionID != w.session || path != w.path || e.LifeNumber != w.life {
					t.Errorf("entry %d = {%q, %#v, life %d}; want {%q, %#v, life %d}",
						i, e.ClaudeSessionID, path, e.LifeNumber, w.session, w.path, w.life)
				}
				if w.recordedAt != "" {
					if e.RecordedAt != w.recordedAt {
						t.Errorf("entry %d recorded_at = %q; want %q", i, e.RecordedAt, w.recordedAt)
					}
					continue
				}
				at, err := time.Parse(storeTimestampLayout, e.RecordedAt)
				if err != nil || at.Before(before) || at.After(after) {
					t.Errorf("entry %d recorded_at = %q (%v); want the seeding time in [%v, %v]",
						i, e.RecordedAt, err, before, after)
				}
			}
		})
	}
}
