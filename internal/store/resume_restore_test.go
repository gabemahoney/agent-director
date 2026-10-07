package store_test

// Resume's restore after a failed launch (SR-8.5, SR-5.3, SR-5.6, AC-RES-12):
// the applied restore leaves no trace but the move's parent id (NULL once that
// parent is deleted) and the advanced version. Each case drives the real move
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
// or pid included) except the move's parent, no launch start and version + 1.
func TestRestoreAfterFailedResumeApplied(t *testing.T) {
	cases := []struct {
		name         string
		spec         moveSpec
		deleteParent bool // delete the move's parent before the restore
	}{
		{name: "ended, RFC 3339 ended_at with a fraction"},
		{name: "ended, store-layout ended_at", spec: moveSpec{opts: []apitest.SpawnOption{apitest.WithEndedAt("2026-09-29 01:02:03")}}},
		{name: "missing, NULL ended_at", spec: moveSpec{state: store.StateMissing, opts: []apitest.SpawnOption{apitest.WithNoEndedAt()}}},
		{name: "missing, NULL pid, no token, socket or identity", spec: moveSpec{state: store.StateMissing,
			opts: []apitest.SpawnOption{apitest.WithNoPID(), apitest.WithNoLaunchToken()}}},
		{name: "the move's parent deleted, parent_id NULL", deleteParent: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newV5Store(t)
			r := seedMoveRow(t, f, tc.spec)
			parent := f.seed(store.StateWaiting, "")
			res, v, err := f.move(r, parent)
			if err != nil || res != store.CondApplied {
				t.Fatalf("MoveToPending = %v, %d, %v; want CondApplied", res, v, err)
			}
			want := r.before
			want.ParentID, want.LaunchStartedAt, want.RowVersion = parent, nil, v+1
			if tc.deleteParent {
				if err := f.s.DeleteSpawn(parent); err != nil {
					t.Fatalf("DeleteSpawn(parent): %v", err)
				}
				want.ParentID = nil
			}
			e := r.examined
			prior := store.ResumePrior{State: e.State, EndedAtText: e.EndedAtText, PID: e.PID, ProcStarttime: e.ProcStarttime,
				LivenessUnverifiedSince: e.LivenessUnverifiedSince, LivenessNote: e.LivenessNote, Identity: e.Identity}
			if res, err := f.s.RestoreAfterFailedResume(r.id, v, prior); err != nil || res != store.CondApplied {
				t.Fatalf("RestoreAfterFailedResume = %v, %v; want CondApplied", res, err)
			}
			rsAssertRow(t, f.rawColumns(r.id), want)
			if got := f.historyAllLives(r.id); !reflect.DeepEqual(got, r.history) {
				t.Errorf("history %+v -> %+v; want unchanged", r.history, got)
			}
		})
	}
}
