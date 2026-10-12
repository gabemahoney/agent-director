package store_test

// Resume's restore after a failed launch (SR-8.5, SR-5.3, SR-5.6, AC-RES-12):
// the applied restore leaves no trace but the move's parent id (NULL once that
// parent is deleted), the advanced version and subagent_tracking written back
// (the move set 1). The restore closes and reopens no subagent entry: what the
// move closed (SR-8.1) stays closed and no claimable launch comes back (SR-8.4).
// Each case drives the real move
// first (resume_move_test.go's seedMoveRow). Its refusals are
// row_version_test.go's no-op cases; its store errors, store_errors_test.go.

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// rsAssertRow fails with each column (and its stored Go type) where got
// differs from want, so a text/number or re-formatted ended_at shows.
func rsAssertRow(t *testing.T, got, want apitest.SpawnColumns) {
	t.Helper()
	gv, wv := reflect.ValueOf(got), reflect.ValueOf(want)
	for i := 0; i < gv.NumField(); i++ {
		if g, w := gv.Field(i).Interface(), wv.Field(i).Interface(); !reflect.DeepEqual(g, w) {
			t.Errorf("%s = %s; want %s", gv.Type().Field(i).Name, rsShow(g), rsShow(w))
		}
	}
}

// rsShow renders a stored value with its Go type; nil is NULL.
func rsShow(v any) string {
	if v == nil {
		return "NULL"
	}
	return fmt.Sprintf("%#v (%T)", v, v)
}

// TestRestoreAfterFailedResumeApplied checks an applied restore leaves the
// pre-move row byte for byte (ended_at text of any layout or NULL, no identity
// or pid included, and the end report the move cleared) except the move's
// parent, no launch start and version + 1; subagent_tracking returns to the
// prior's value, which the move had set by the row's agent (a claude row's 0 to
// 1, a codex row's 1 to 0), and no entry or claimable launch changes from what
// the move left.
func TestRestoreAfterFailedResumeApplied(t *testing.T) {
	cases := []struct {
		name         string
		spec         moveSpec
		deleteParent bool // delete the move's parent before the restore
		priorTracked bool // the prior carries SubagentTracking true over a claude row's seeded 0 (claude rows only)
	}{
		{name: "ended, RFC 3339 ended_at with a fraction"},
		{name: "ended, store-layout ended_at", spec: moveSpec{opts: []apitest.SpawnOption{apitest.WithEndedAt("2026-09-29 01:02:03")}}},
		{name: "missing, NULL ended_at", spec: moveSpec{state: store.StateMissing, opts: []apitest.SpawnOption{apitest.WithNoEndedAt()}}},
		{name: "missing, NULL pid, no token, socket or identity", spec: moveSpec{state: store.StateMissing,
			opts: []apitest.SpawnOption{apitest.WithNoPID(), apitest.WithNoLaunchToken()}}},
		{name: "the move's parent deleted, parent_id NULL", deleteParent: true},
		{name: "prior tracked, flag written back as 1", priorTracked: true},
	}
	for _, agent := range moveAgents {
		for _, tc := range cases {
			if tc.priorTracked && agent != store.AgentClaude {
				continue // a codex row is seeded tracked already, so its base case writes 1 back
			}
			t.Run(agent+", "+tc.name, func(t *testing.T) {
				tc.spec.agent = agent
				testRestoreAfterFailedResumeApplied(t, tc.spec, tc.deleteParent, tc.priorTracked)
			})
		}
	}
}

// testRestoreAfterFailedResumeApplied runs one case of TestRestoreAfterFailedResumeApplied.
func testRestoreAfterFailedResumeApplied(t *testing.T, spec moveSpec, deleteParent, priorTracked bool) {
	t.Helper()
	f := newV5Store(t)
	r := seedMoveRow(t, f, spec)
	mix := f.seedCloseMix(r.id)
	parent := f.seed(store.StateWaiting, "")
	res, v, err := f.move(r, parent)
	if err != nil || res != store.CondApplied {
		t.Fatalf("MoveToPending = %v, %d, %v; want CondApplied", res, v, err)
	}
	movedFlag := launchTracking(r.before.Agent.(string))
	if got := f.rawColumns(r.id).SubagentTracking; r.before.SubagentTracking != 1-movedFlag || got != movedFlag {
		t.Fatalf("subagent_tracking %v -> %v after the move; want %d -> %d, or the restore check is vacuous", r.before.SubagentTracking, got, 1-movedFlag, movedFlag)
	}
	moved := f.subagentRowsOf(r.id)
	if reflect.DeepEqual(moved, mix.rows) || !reflect.DeepEqual(moved, cmClosed(mix.rows, moveLaunchMillis)) {
		t.Fatalf("the move left the entries and launches %+v from %+v; want the close mix closed at the launch start, or the restore check is vacuous", moved, mix.rows)
	}
	if after := f.rawColumns(r.id); r.before.EndReportedAt != moveEndReported || after.EndReportedAt != nil {
		t.Fatalf("end_reported_at %v -> %v after the move; want %d -> NULL, or the restore check is vacuous", r.before.EndReportedAt, after.EndReportedAt, moveEndReported)
	}
	want := r.before
	want.ParentID, want.LaunchStartedAt, want.RowVersion = parent, nil, v+1
	if priorTracked {
		want.SubagentTracking = int64(1)
	}
	if deleteParent {
		if err := f.s.DeleteSpawn(parent); err != nil {
			t.Fatalf("DeleteSpawn(parent): %v", err)
		}
		want.ParentID = nil
	}
	e := r.examined
	prior := store.ResumePrior{State: e.State, EndedAtText: e.EndedAtText, PID: e.PID, ProcStarttime: e.ProcStarttime,
		LivenessUnverifiedSince: e.LivenessUnverifiedSince, LivenessNote: e.LivenessNote, EndReportedAtMillis: e.EndReportedAtMillis,
		SubagentTracking: e.SubagentTracking || priorTracked, Identity: e.Identity}
	if res, err := f.s.RestoreAfterFailedResume(r.id, v, prior); err != nil || res != store.CondApplied {
		t.Fatalf("RestoreAfterFailedResume = %v, %v; want CondApplied", res, err)
	}
	rsAssertRow(t, f.rawColumns(r.id), want)
	if got := f.historyAllLives(r.id); !reflect.DeepEqual(got, r.history) {
		t.Errorf("history %+v -> %+v; want unchanged", r.history, got)
	}
	if got := f.subagentRowsOf(r.id); !reflect.DeepEqual(got, moved) {
		t.Errorf("subagent rows %+v -> %+v; want unchanged by the restore", moved, got)
	}
	f.assertBystanderKept(mix)
}
