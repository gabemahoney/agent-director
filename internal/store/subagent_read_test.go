package store_test

// ListSubagentRecords' tracked flag (b.xjn, SR-9, SR-11.3): a hand-edited
// subagent_tracking never fails the read, with or without records, and only the
// stored integer 1 reads tracked. The same rule for GetSpawn and ListSpawns is
// TestSubagentTrackingReadsNeverFail's (spawns_test.go).

import (
	"database/sql"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// storeTrackingRaw writes v to id's subagent_tracking as it is, bypassing the
// store's writes, so a value only a hand edit can store is expressible.
func (f *v5Store) storeTrackingRaw(id string, v any) {
	f.t.Helper()
	db, err := sql.Open("sqlite", f.path)
	if err != nil {
		f.t.Fatalf("raw sql.Open(%q): %v", f.path, err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`UPDATE spawns SET subagent_tracking = ? WHERE instance_id = ?`, v, id); err != nil {
		f.t.Fatalf("set subagent_tracking = %v: %v", v, err)
	}
}

// TestListSubagentRecordsTrackedDecoding checks that only the stored integer 1
// reads tracked, whatever else is stored (0, other integers, text), with no
// error, for a row with no record and for a row with a record: the record is
// returned either way.
func TestListSubagentRecordsTrackedDecoding(t *testing.T) {
	cases := []struct {
		name        string
		stored      any
		wantTracked bool
	}{
		{"0", int64(0), false},
		{"1", int64(1), true},
		{"7", int64(7), false},
		{"-1", int64(-1), false},
		{"text", "weird", false},
	}
	for _, tc := range cases {
		for _, withRecord := range []bool{false, true} {
			name := tc.name + "/no record"
			if withRecord {
				name = tc.name + "/one record"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				f := newV5Store(t)
				id := f.seed(store.StateWaiting, "sess-tracked")
				if withRecord {
					if err := apitest.SeedSubagent(f.path, id, "sa-rec"); err != nil {
						t.Fatalf("SeedSubagent: %v", err)
					}
				}
				f.storeTrackingRaw(id, tc.stored)

				tracked, rows, err := f.s.ListSubagentRecords(id)

				if err != nil || tracked != tc.wantTracked {
					t.Fatalf("ListSubagentRecords with %v stored = tracked %v, %v; want tracked %v and no error", tc.stored, tracked, err, tc.wantTracked)
				}
				wantRows := 0
				if withRecord {
					wantRows = 1
				}
				if len(rows) != wantRows || (withRecord && rows[0].SubagentID != "sa-rec") {
					t.Errorf("records = %+v; want %d", rows, wantRows)
				}
			})
		}
	}
}
