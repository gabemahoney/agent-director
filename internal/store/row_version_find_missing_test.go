package store_test

// SR-5.2 versioning cases for find-missing's guarded writes (SR-11.3,
// SR-11.6), its unreported note of a live pending row (b.kdf, b.146 rule 10),
// its adoption write (SR-3.6), a launch's release of its hold (b.kdf, rule
// 11) and its repair of a stale check_permission row (b.146 rule 9, problem
// 3), appended to row_version_test.go's applied and no-op tables.

import (
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// rvGuarded is one of find-missing's guarded writes; prior is the mark's
// returned prior state, the repair's written state, and "" for the others.
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
	rvUnreported = rvGuarded{"NoteUnreportedIfSameLife", func(s *store.Store, id string, e store.RowSnapshot) (string, store.CondResult, error) {
		res, err := s.NoteUnreportedIfSameLife(id, e)
		return "", res, err
	}}
	rvRepair = rvGuarded{"RepairCheckPermissionIfSameLife", func(s *store.Store, id string, e store.RowSnapshot) (string, store.CondResult, error) {
		return s.RepairCheckPermissionIfSameLife(id, e)
	}}
)

// idleKept fails unless idle_since is unchanged.
func idleKept(t *testing.T, before, after apitest.SpawnColumns) {
	t.Helper()
	if after.IdleSince != before.IdleSince {
		t.Errorf("idle_since %#v -> %#v; want kept", before.IdleSince, after.IdleSince)
	}
}

// unreportedNote seeds note unreported with a first noted time (b.kdf).
var unreportedNote = []apitest.SpawnOption{
	apitest.WithLivenessUnverifiedSince("2026-01-01 00:00:00"), apitest.WithLivenessNote("unreported"),
}

// conflictNote seeds note provenance_conflict, which unreported never overwrites (b.kdf).
var conflictNote = []apitest.SpawnOption{
	apitest.WithLivenessUnverifiedSince("2026-01-01 00:00:00"), apitest.WithLivenessNote("provenance_conflict"),
}

// rvOwner is a recorded launch owner (b.kdf).
var rvOwner = store.LaunchOwner{PID: 4300, Starttime: apitest.LinuxProcStarttime, PIDNamespace: "pid:[4026531836]"}

// withOwner seeds rvOwner as the row's launch owner.
var withOwner = []apitest.SpawnOption{apitest.WithLaunchOwner(rvOwner)}

// ownerColumns returns c's three launch-owner columns.
func ownerColumns(c apitest.SpawnColumns) []any {
	return []any{c.LaunchOwnerPID, c.LaunchOwnerStarttime, c.LaunchOwnerPIDNS}
}

// rvRelease returns a ReleaseLaunchOwner of target ("" = the seeded row) with
// token, expecting want.
func rvRelease(target, token string, want store.CondResult) func(*testing.T, *v5Store, string) {
	return func(t *testing.T, f *v5Store, id string) {
		if target == "" {
			target = id
		}
		if got, err := f.s.ReleaseLaunchOwner(target, token); err != nil || got != want {
			t.Fatalf("ReleaseLaunchOwner(%s) = %v, %v; want %v, nil", target, got, err, want)
		}
	}
}

// notedUnreported fails unless the note is unreported, the first noted time
// set (kept when any earlier note set it) and the launch owner kept.
func notedUnreported(t *testing.T, before, after apitest.SpawnColumns) {
	t.Helper()
	if after.LivenessNote != "unreported" || after.LivenessUnverifiedSince == nil {
		t.Errorf("liveness_note, liveness_unverified_since = %#v, %#v; want unreported, set",
			after.LivenessNote, after.LivenessUnverifiedSince)
	}
	if before.LivenessUnverifiedSince != nil && after.LivenessUnverifiedSince != before.LivenessUnverifiedSince {
		t.Errorf("liveness_unverified_since %#v -> %#v; want the first noted time kept",
			before.LivenessUnverifiedSince, after.LivenessUnverifiedSince)
	}
	if !reflect.DeepEqual(ownerColumns(after), ownerColumns(before)) || before.LaunchOwnerPID == nil {
		t.Errorf("launch owner %#v -> %#v; want a recorded owner kept", ownerColumns(before), ownerColumns(after))
	}
}

// ownerCleared fails unless the three launch-owner columns went from set to NULL.
func ownerCleared(t *testing.T, before, after apitest.SpawnColumns) {
	t.Helper()
	if before.LaunchOwnerPID == nil || before.LaunchOwnerStarttime == nil || before.LaunchOwnerPIDNS == nil {
		t.Fatal("seed left a launch-owner column NULL; the clear check would be vacuous")
	}
	if got := ownerColumns(after); !reflect.DeepEqual(got, []any{nil, nil, nil}) {
		t.Errorf("launch owner = %#v; want NULL, NULL, NULL", got)
	}
}

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

// rvAdopt returns an AdoptIdentityIfSameLife write of createdIdentity, as
// rvGuarded.write runs it; an applied write must also return the row's
// snapshot after it (GetSpawn's), a refused one the zero snapshot.
func rvAdopt(target string, mutate func(*store.RowSnapshot), want store.CondResult) func(*testing.T, *v5Store, string) {
	var now store.RowSnapshot
	write := rvGuarded{"AdoptIdentityIfSameLife", func(s *store.Store, id string, e store.RowSnapshot) (string, store.CondResult, error) {
		res, n, err := s.AdoptIdentityIfSameLife(id, e, createdIdentity())
		now = n
		return "", res, err
	}}.write(target, mutate, want, "")
	return func(t *testing.T, f *v5Store, id string) {
		write(t, f, id)
		var wantNow store.RowSnapshot
		if want == store.CondApplied {
			wantNow = rvExamine(t, f, id).Snapshot
		}
		if now != wantNow {
			t.Errorf("AdoptIdentityIfSameLife returned snapshot %+v, want %+v", now, wantNow)
		}
	}
}

// livenessKept fails unless both liveness columns were set and are unchanged.
func livenessKept(t *testing.T, before, after apitest.SpawnColumns) {
	t.Helper()
	if before.LivenessUnverifiedSince == nil || before.LivenessNote == nil {
		t.Fatal("seed left a liveness column NULL; the keep check would be vacuous")
	}
	if after.LivenessUnverifiedSince != before.LivenessUnverifiedSince || after.LivenessNote != before.LivenessNote {
		t.Errorf("liveness_unverified_since, liveness_note %#v, %#v -> %#v, %#v; want kept",
			before.LivenessUnverifiedSince, before.LivenessNote, after.LivenessUnverifiedSince, after.LivenessNote)
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
// liveness clear and the launch-start clear (SR-11.3); note and clear keep the
// launch start. The adoption writes the six identity columns only (SR-3.6).
func rvFindMissingWrites() []rowVersionCase {
	applied := func(g rvGuarded, prior string) func(*testing.T, *v5Store, string) {
		return g.write("", nil, store.CondApplied, prior)
	}
	created := createdIdentity()
	return []rowVersionCase{
		{name: "AdoptIdentityIfSameLife/applied, pending row with no pane (lost reply)", state: "pending",
			opts: []apitest.SpawnOption{apitest.WithNoPane()}, wantState: "pending", identity: &created,
			write: rvAdopt("", nil, store.CondApplied)},
		{name: "AdoptIdentityIfSameLife/applied, live row with a note", state: "waiting", opts: liveness,
			wantState: "waiting", identity: &created, write: rvAdopt("", nil, store.CondApplied), check: livenessKept},
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
		// b.kdf: the unreported note keeps the state pending, the launch
		// start (CSCB) and the earlier note's time; the owner is not its to write.
		{name: "NoteUnreportedIfSameLife/applied, pending row noted probe_eacces with an owner, its time kept", state: "pending",
			opts: append(slices.Clone(liveness), withOwner...), wantState: "pending",
			write: applied(rvUnreported, ""), check: notedUnreported},
		{name: "NoteUnreportedIfSameLife/applied, pending row already noted unreported", state: "pending",
			opts: append(slices.Clone(unreportedNote), withOwner...), wantState: "pending",
			write: applied(rvUnreported, ""), check: notedUnreported},
		{name: "ReleaseLaunchOwner/applied, pending row with an owner", state: "pending", opts: withOwner,
			wantState: "pending", write: rvRelease("", goodToken, store.CondApplied), check: ownerCleared},
		// b.146 rule 9, problem 3: the repair of a stale check_permission row
		// picks waiting when idle_since is set, else working, and keeps it.
		{name: "RepairCheckPermissionIfSameLife/applied, relay-on row with no request awaiting: working", state: "check_permission",
			opts: []apitest.SpawnOption{rvRelayOn}, clears: true, wantState: "working",
			write: applied(rvRepair, "working"), check: idleKept},
		{name: "RepairCheckPermissionIfSameLife/applied, relay-on row with idle_since set: waiting", state: "check_permission",
			opts: []apitest.SpawnOption{rvRelayOn, apitest.WithIdleSince(rvIdleSince)}, clears: true, wantState: "waiting",
			write: applied(rvRepair, "waiting"), check: idleKept},
	}
}

// rvFindMissingNoOps are each guarded write and the adoption refused: a
// stale snapshot, a finished row, an absent row. The seeded note keeps every
// refusal non-vacuous.
func rvFindMissingNoOps() []rowVersionCase {
	cases := []rowVersionCase{
		{name: "AdoptIdentityIfSameLife/stale snapshot, hook wrote first", state: "waiting", opts: liveness,
			setup: rvSoftRefresh,
			write: rvAdopt("", func(s *store.RowSnapshot) { s.RowVersion-- }, store.CondChanged)},
		{name: "AdoptIdentityIfSameLife/ended row", state: "ended", opts: liveness,
			write: rvAdopt("", nil, store.CondChanged)},
		{name: "AdoptIdentityIfSameLife/missing row", state: "missing", opts: liveness,
			write: rvAdopt("", nil, store.CondChanged)},
		{name: "AdoptIdentityIfSameLife/absent row", state: "waiting", opts: liveness,
			write: rvAdopt("rv-absent", nil, store.CondAbsent)},
	}
	cases = append(cases,
		rowVersionCase{name: "NoteUnreportedIfSameLife/stale snapshot, hook wrote first", state: "pending", opts: liveness,
			setup: rvSoftRefresh,
			write: rvUnreported.write("", func(s *store.RowSnapshot) { s.RowVersion-- }, store.CondChanged, "")},
		rowVersionCase{name: "NoteUnreportedIfSameLife/pending row noted provenance_conflict", state: "pending", opts: conflictNote,
			write: rvUnreported.write("", nil, store.CondChanged, "")},
		rowVersionCase{name: "NoteUnreportedIfSameLife/live row", state: "waiting", opts: liveness,
			write: rvUnreported.write("", nil, store.CondChanged, "")},
		rowVersionCase{name: "NoteUnreportedIfSameLife/finished row", state: "missing", opts: liveness,
			write: rvUnreported.write("", nil, store.CondChanged, "")},
		rowVersionCase{name: "NoteUnreportedIfSameLife/absent row", state: "pending", opts: liveness,
			write: rvUnreported.write("rv-absent", nil, store.CondAbsent, "")},
		rowVersionCase{name: "ReleaseLaunchOwner/another launch's token", state: "pending", opts: withOwner,
			write: rvRelease("", rvMoveToken, store.CondChanged)},
		rowVersionCase{name: "ReleaseLaunchOwner/row no longer pending", state: "waiting", opts: withOwner,
			write: rvRelease("", goodToken, store.CondChanged)},
		rowVersionCase{name: "ReleaseLaunchOwner/no owner recorded", state: "pending",
			write: rvRelease("", goodToken, store.CondChanged)},
		rowVersionCase{name: "ReleaseLaunchOwner/absent row", state: "pending", opts: withOwner,
			write: rvRelease("rv-absent", goodToken, store.CondAbsent)},
		rowVersionCase{name: "RepairCheckPermissionIfSameLife/stale snapshot, hook wrote first", state: "check_permission",
			opts: []apitest.SpawnOption{rvRelayOn}, setup: rvSoftRefresh,
			write: rvRepair.write("", func(s *store.RowSnapshot) { s.RowVersion-- }, store.CondChanged, "")},
		rowVersionCase{name: "RepairCheckPermissionIfSameLife/a request still awaiting an answer", state: "check_permission",
			opts: []apitest.SpawnOption{rvRelayOn}, setup: func(t *testing.T, f *v5Store, id string) { seedRequest(t, f, id) },
			write: rvRepair.write("", nil, store.CondChanged, "")},
		rowVersionCase{name: "RepairCheckPermissionIfSameLife/a relay request's verdict not yet acked", state: "working",
			opts: []apitest.SpawnOption{rvRelayOn}, setup: func(t *testing.T, f *v5Store, id string) {
				rvInsertRelay(t, f, id)
				ok, err := f.s.DecideRelayRequest(id, rvRelayToken, "allow", "", store.WriterProcessDecide, time.Time{}, store.DefaultLockWait)
				wantBool(t, "DecideRelayRequest", ok, err, true)
			},
			write: rvRepair.write("", nil, store.CondChanged, "")},
		rowVersionCase{name: "RepairCheckPermissionIfSameLife/relay off", state: "check_permission",
			write: rvRepair.write("", nil, store.CondChanged, "")},
		rowVersionCase{name: "RepairCheckPermissionIfSameLife/row not in check_permission", state: "working",
			opts: []apitest.SpawnOption{rvRelayOn}, write: rvRepair.write("", nil, store.CondChanged, "")},
		rowVersionCase{name: "RepairCheckPermissionIfSameLife/absent row", state: "check_permission",
			opts: []apitest.SpawnOption{rvRelayOn}, write: rvRepair.write("rv-absent", nil, store.CondAbsent, "")},
	)
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
