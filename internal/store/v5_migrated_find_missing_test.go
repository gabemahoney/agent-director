package store_test

// b.kdf: a pending row from before schema v6 records no launch owner, so
// find-missing judges it by its pending grace period alone, as before the hop,
// over the v5 fixture migrated through the real sentinel flow
// (v5_fixture_export_test.go).

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestMigratedV5PendingRowJudgedByGraceAlone: the migrated pending row is left
// alone inside its pending grace period, and past it is judged by its pane
// process: alive leaves it pending, noted unreported, with its
// launch_started_at kept (CSCB); gone marks it missing.
func TestMigratedV5PendingRowJudgedByGraceAlone(t *testing.T) {
	grace := time.Duration(config.DefaultPendingGraceSeconds) * time.Second
	cases := []struct {
		name      string
		age       time.Duration // the sweep's now minus the launch start
		gone      bool          // the pane process is gone
		wantState string        // "": the row untouched
		wantNote  string
	}{
		{name: "inside grace", age: grace - time.Second},
		{name: "past grace, pane process alive", age: grace + time.Second, wantState: store.StatePending, wantNote: "unreported"},
		{name: "past grace, pane process gone", age: grace + time.Second, gone: true, wantState: store.StateMissing},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := store.MigrateV5Fixture(t)
			s, err := store.Open(f.Path)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			t.Cleanup(func() { _ = s.Close() })
			pc := procfix.New()
			if !tc.gone {
				pc.Set(f.PendingPanePID, procfix.Alive(f.PendingPaneStarttime))
			}
			before, err := apitest.ReadSpawnColumns(f.Path, f.Pending)
			if err != nil {
				t.Fatalf("ReadSpawnColumns: %v", err)
			}
			now := time.UnixMilli(f.PendingLaunchStartMillis).Add(tc.age)

			if _, err := api.FindMissing(context.Background(), s, tmuxfix.NewRecorder(), pc, grace,
				time.Duration(config.DefaultSweepBudgetSeconds)*time.Second, func() time.Time { return now }, nil); err != nil {
				t.Fatalf("FindMissing: %v", err)
			}

			after, err := apitest.ReadSpawnColumns(f.Path, f.Pending)
			if err != nil {
				t.Fatalf("ReadSpawnColumns: %v", err)
			}
			if tc.wantState == "" {
				if !reflect.DeepEqual(after, before) {
					t.Errorf("row inside grace:\n got  %+v\n want %+v", after, before)
				}
				return
			}
			note, _ := after.LivenessNote.(string)
			if after.State != tc.wantState || note != tc.wantNote || after.LaunchOwnerPID != nil {
				t.Errorf("state, note, owner pid = %v, %q, %v; want %s, %q, NULL", after.State, note, after.LaunchOwnerPID,
					tc.wantState, tc.wantNote)
			}
			if tc.wantNote != "" && after.LaunchStartedAt != f.PendingLaunchStartMillis {
				t.Errorf("launch_started_at = %v; want %d kept by the hop and the note", after.LaunchStartedAt, f.PendingLaunchStartMillis)
			}
		})
	}
}
