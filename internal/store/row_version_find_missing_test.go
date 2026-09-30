package store_test

// SR-5.2 versioning cases for find-missing's three guarded writes (SR-11.3,
// SR-11.6), appended to row_version_test.go's applied and no-op tables.

import (
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// rvGuarded is one of find-missing's guarded writes; prior is the mark's
// returned prior state and "" for the others.
type rvGuarded struct {
	name string
	run  func(s *store.Store, id string, examined store.RowSnapshot) (prior string, res store.CondResult, err error)
}

// rvNewNote is the note rvNote writes, unlike the liveness seed's.
const rvNewNote = "process_not_seen_tmux_unchecked"

var (
	rvMark = rvGuarded{"MarkMissingIfSameLife", func(s *store.Store, id string, e store.RowSnapshot) (string, store.CondResult, error) {
		return s.MarkMissingIfSameLife(id, e)
	}}
	rvNote = rvGuarded{"SetLivenessNoteIfSameLife", func(s *store.Store, id string, e store.RowSnapshot) (string, store.CondResult, error) {
		res, err := s.SetLivenessNoteIfSameLife(id, e, rvNewNote)
		return "", res, err
	}}
	rvClear = rvGuarded{"ClearLivenessIfSameLife", func(s *store.Store, id string, e store.RowSnapshot) (string, store.CondResult, error) {
		res, err := s.ClearLivenessIfSameLife(id, e)
		return "", res, err
	}}
)

// write returns g's write to target ("" = the seeded row) of the seeded row's
// snapshot read now, after mutate edits it (nil keeps it), expecting want and prior.
func (g rvGuarded) write(target string, mutate func(*store.RowSnapshot), want store.CondResult, prior string) func(*testing.T, *v5Store, string) {
	return func(t *testing.T, f *v5Store, id string) {
		examined := rvExamine(t, f, id).Snapshot
		if mutate != nil {
			mutate(&examined)
		}
		to := target
		if to == "" {
			to = id
		}
		gotPrior, got, err := g.run(f.s, to, examined)
		if err != nil || got != want || gotPrior != prior {
			t.Fatalf("%s = %q, %v, %v; want %q, %v, nil", g.name, gotPrior, got, err, prior, want)
		}
	}
}

// livenessCleared fails unless both liveness columns went from set to NULL.
func livenessCleared(t *testing.T, before, after apitest.SpawnColumns) {
	t.Helper()
	if before.LivenessUnverifiedSince == nil || before.LivenessNote == nil {
		t.Fatal("seed left a liveness column NULL; the clear check would be vacuous")
	}
	if after.LivenessUnverifiedSince != nil || after.LivenessNote != nil {
		t.Errorf("liveness_unverified_since, liveness_note = %#v, %#v; want NULL, NULL",
			after.LivenessUnverifiedSince, after.LivenessNote)
	}
}

// noteWritten fails unless the note is rvNewNote and the unverified time is
// kept, or set when the seed left it NULL.
func noteWritten(t *testing.T, before, after apitest.SpawnColumns) {
	t.Helper()
	if after.LivenessNote != rvNewNote {
		t.Errorf("liveness_note %#v -> %#v, want %q", before.LivenessNote, after.LivenessNote, rvNewNote)
	}
	switch {
	case after.LivenessUnverifiedSince == nil:
		t.Error("liveness_unverified_since = NULL, want set")
	case before.LivenessUnverifiedSince != nil && !reflect.DeepEqual(after.LivenessUnverifiedSince, before.LivenessUnverifiedSince):
		t.Errorf("liveness_unverified_since %#v -> %#v, want kept", before.LivenessUnverifiedSince, after.LivenessUnverifiedSince)
	}
}

// rvFindMissingWrites are the applied guarded writes: the mark folds in the
// liveness clear and the launch-start clear (SR-11.3); note and clear keep the launch start.
func rvFindMissingWrites() []rowVersionCase {
	applied := func(g rvGuarded, prior string) func(*testing.T, *v5Store, string) {
		return g.write("", nil, store.CondApplied, prior)
	}
	return []rowVersionCase{
		{name: "MarkMissingIfSameLife/applied, live row with a note", state: "waiting", opts: liveness,
			clears: true, wantState: "missing", write: applied(rvMark, "waiting"), check: livenessCleared},
		{name: "MarkMissingIfSameLife/applied, pending row with a note", state: "pending", opts: liveness,
			clears: true, wantState: "missing", write: applied(rvMark, "pending"), check: livenessCleared},
		{name: "SetLivenessNoteIfSameLife/applied, first note on pending row", state: "pending", wantState: "pending",
			write: applied(rvNote, ""), check: noteWritten},
		{name: "SetLivenessNoteIfSameLife/applied, note overwritten on live row", state: "waiting", opts: liveness,
			wantState: "waiting", write: applied(rvNote, ""), check: noteWritten},
		{name: "ClearLivenessIfSameLife/applied, note set on pending row", state: "pending", opts: liveness,
			wantState: "pending", write: applied(rvClear, ""), check: livenessCleared},
		{name: "ClearLivenessIfSameLife/applied, no note on live row", state: "waiting", wantState: "waiting",
			write: applied(rvClear, "")},
	}
}

// rvFindMissingNoOps are each guarded write refused: a stale snapshot, a
// finished row, an absent row. The seeded note keeps every refusal non-vacuous.
func rvFindMissingNoOps() []rowVersionCase {
	var cases []rowVersionCase
	for _, g := range []rvGuarded{rvMark, rvNote, rvClear} {
		cases = append(cases,
			rowVersionCase{name: g.name + "/stale snapshot, hook wrote first", state: "waiting", opts: liveness,
				setup: rvSoftRefresh,
				write: g.write("", func(s *store.RowSnapshot) { s.RowVersion-- }, store.CondChanged, "")},
			rowVersionCase{name: g.name + "/finished row", state: "ended", opts: liveness,
				write: g.write("", nil, store.CondChanged, "")},
			rowVersionCase{name: g.name + "/absent row", state: "waiting", opts: liveness,
				write: g.write("rv-absent", nil, store.CondAbsent, "")},
		)
	}
	return cases
}
