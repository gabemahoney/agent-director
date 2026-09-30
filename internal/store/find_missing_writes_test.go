package store_test

// Store tests for find-missing's three guarded writes (SR-5.3, SR-11.3,
// SR-11.4, SR-11.6; Appendix F.4): the applied mark, note write and clear, and
// the refusals that write nothing. Rows are seeded through apitest (SR-20.2);
// the note write's and clear's store errors are covered through pkg/api's
// failing store (SR-20.3), so only the mark's is injected here.

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// fmRow is a seeded row as the sweep examined it.
type fmRow struct {
	id       string
	examined store.RowSnapshot    // GetSpawn's snapshot after seeding
	before   apitest.SpawnColumns // raw columns after seeding
}

// seedFMRow seeds a row in state with every snapshot component, the launch
// identity, launch start, life number and no_pre_trust set; opts come last.
func seedFMRow(t *testing.T, f *v5Store, state string, opts ...apitest.SpawnOption) fmRow {
	t.Helper()
	base := []apitest.SpawnOption{
		apitest.WithStartedAt("2026-03-04T05:06:07.250+02:00"),
		apitest.WithPID(4242),
		apitest.WithProcStarttime(apitest.LinuxProcStarttime),
		apitest.WithTmuxSessionName("fm-ts"),
		apitest.WithLaunchIdentity(moveIdentity()),
		apitest.WithLaunchStartedAt(1767225600999),
		apitest.WithLifeNumber(3),
		apitest.WithNoPreTrust(),
	}
	id := f.seed(state, "sess-fm", append(base, opts...)...)
	sp, err := f.s.GetSpawn(id)
	if err != nil {
		t.Fatalf("GetSpawn(%s): %v", id, err)
	}
	return fmRow{id: id, examined: sp.Snapshot, before: f.rawColumns(id)}
}

// withNote seeds both liveness columns.
func withNote(since, note string) []apitest.SpawnOption {
	return []apitest.SpawnOption{apitest.WithLivenessUnverifiedSince(since), apitest.WithLivenessNote(note)}
}

// fmWrite is one of the three guarded writes; prior is the mark's returned
// prior state and "" for the others.
type fmWrite struct {
	name string
	run  func(s *store.Store, id string, snap store.RowSnapshot) (prior string, res store.CondResult, err error)
}

var fmWrites = []fmWrite{
	{"mark", func(s *store.Store, id string, snap store.RowSnapshot) (string, store.CondResult, error) {
		return s.MarkMissingIfSameLife(id, snap)
	}},
	{"note", func(s *store.Store, id string, snap store.RowSnapshot) (string, store.CondResult, error) {
		res, err := s.SetLivenessNoteIfSameLife(id, snap, "fm new note")
		return "", res, err
	}},
	{"clear", func(s *store.Store, id string, snap store.RowSnapshot) (string, store.CondResult, error) {
		res, err := s.ClearLivenessIfSameLife(id, snap)
		return "", res, err
	}},
}

// wantAdvanced returns before with row_version advanced by one: the base
// every applied write's expected row starts from.
func wantAdvanced(t *testing.T, before apitest.SpawnColumns) apitest.SpawnColumns {
	t.Helper()
	v, ok := before.RowVersion.(int64)
	if !ok {
		t.Fatalf("row_version = %#v; want an integer", before.RowVersion)
	}
	before.RowVersion = v + 1
	return before
}

// assertRow fails unless id's row reads exactly want.
func assertRow(t *testing.T, f *v5Store, id string, want apitest.SpawnColumns) {
	t.Helper()
	if got := f.rawColumns(id); !reflect.DeepEqual(got, want) {
		t.Errorf("row:\n got  %+v\n want %+v", got, want)
	}
}

// TestMarkMissingIfSameLifeApplied checks the mark on every live state:
// prior state returned, the mark's columns written, version +1, all else kept.
func TestMarkMissingIfSameLifeApplied(t *testing.T) {
	f := newV5Store(t)
	states := []string{store.StatePending, store.StateWaiting, store.StateWorking, store.StateAskUser, store.StateCheckPermission}
	rows := make([]fmRow, len(states))
	for i, st := range states {
		rows[i] = seedFMRow(t, f, st, withNote("2026-09-28 10:00:00", "fm old note")...)
		requireSet(t, map[string]any{"launch_started_at": rows[i].before.LaunchStartedAt, "liveness_note": rows[i].before.LivenessNote})
	}
	// CURRENT_TIMESTAMP has whole seconds: move past the seeding's second so
	// the mark's last_seen_at differs from the seeded one.
	time.Sleep(time.Until(time.Now().Truncate(time.Second).Add(time.Second)))
	for i, st := range states {
		t.Run(st, func(t *testing.T) {
			r := rows[i]
			prior, res, err := f.s.MarkMissingIfSameLife(r.id, r.examined)
			if err != nil || res != store.CondApplied || prior != st {
				t.Fatalf("MarkMissingIfSameLife = %q, %v, %v; want %q, CondApplied, nil", prior, res, err, st)
			}
			after := f.rawColumns(r.id)
			if after.EndedAt == nil || after.EndedAt != after.LastSeenAt || after.LastSeenAt == r.before.LastSeenAt {
				t.Errorf("ended_at %#v, last_seen_at %#v (seeded %#v); want both the mark's time", after.EndedAt, after.LastSeenAt, r.before.LastSeenAt)
			}
			want := wantAdvanced(t, r.before)
			want.State = store.StateMissing
			want.EndedAt, want.LastSeenAt = after.EndedAt, after.LastSeenAt
			want.LivenessUnverifiedSince, want.LivenessNote, want.LaunchStartedAt = nil, nil, nil
			assertRow(t, f, r.id, want)
		})
	}
}

// TestLivenessIfSameLifeApplied checks the note write (NULL to set, overwrite,
// equal note) and the clear (set or not): the liveness columns, version +1,
// all else kept.
func TestLivenessIfSameLifeApplied(t *testing.T) {
	const seededSince = "2026-09-28 10:00:00"
	cases := []struct {
		name      string
		write     fmWrite
		opts      []apitest.SpawnOption
		wantNote  any
		wantSince any // nil = NULL; "" = a new CURRENT_TIMESTAMP
	}{
		{"note on a row with none", fmWrites[1], nil, "fm new note", ""},
		{"note overwrite keeps since", fmWrites[1], withNote(seededSince, "fm old note"), "fm new note", seededSince},
		{"equal note still written", fmWrites[1], withNote(seededSince, "fm new note"), "fm new note", seededSince},
		{"clear a set note", fmWrites[2], withNote(seededSince, "fm old note"), nil, nil},
		{"clear a row with no note", fmWrites[2], nil, nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newV5Store(t)
			r := seedFMRow(t, f, store.StateWaiting, tc.opts...)
			if _, res, err := tc.write.run(f.s, r.id, r.examined); err != nil || res != store.CondApplied {
				t.Fatalf("%s = %v, %v; want CondApplied, nil", tc.write.name, res, err)
			}
			want := wantAdvanced(t, r.before)
			want.LivenessNote, want.LivenessUnverifiedSince = tc.wantNote, tc.wantSince
			if tc.wantSince == "" {
				got := f.rawColumns(r.id).LivenessUnverifiedSince
				s, ok := got.(string)
				if _, err := time.Parse(time.DateTime, s); !ok || err != nil {
					t.Fatalf("liveness_unverified_since = %#v; want a CURRENT_TIMESTAMP", got)
				}
				want.LivenessUnverifiedSince = got
			}
			assertRow(t, f, r.id, want)
		})
	}
}

// TestFindMissingIfSameLifeRefused checks each write is refused, writing
// nothing, for a snapshot differing in one component, an earlier write from
// the same snapshot, a finished row and a deleted row.
func TestFindMissingIfSameLifeRefused(t *testing.T) {
	type refusal struct {
		name   string
		state  string                   // "" = waiting
		mutate func(*store.RowSnapshot) // the examined snapshot's difference
		first  bool                     // apply the same write first
		delete bool                     // delete the row after examination
		want   store.CondResult
	}
	cases := []refusal{
		{name: "row_version differs", mutate: func(s *store.RowSnapshot) { s.RowVersion++ }, want: store.CondChanged},
		{name: "started_at differs", mutate: func(s *store.RowSnapshot) { s.StartedAt = "2026-03-04 03:06:07" }, want: store.CondChanged},
		{name: "claude_session_id differs", mutate: func(s *store.RowSnapshot) { s.ClaudeSessionID = "sess-other" }, want: store.CondChanged},
		{name: "pid differs", mutate: func(s *store.RowSnapshot) { s.PID++ }, want: store.CondChanged},
		{name: "proc_starttime differs", mutate: func(s *store.RowSnapshot) { s.ProcStarttime = apitest.DarwinProcStarttime }, want: store.CondChanged},
		{name: "tmux_session_name differs", mutate: func(s *store.RowSnapshot) { s.TmuxSessionName = "fm-ts-2" }, want: store.CondChanged},
		{name: "second write from the same snapshot", first: true, want: store.CondChanged},
		{name: "ended row", state: store.StateEnded, want: store.CondChanged},
		{name: "missing row", state: store.StateMissing, want: store.CondChanged},
		{name: "deleted row", delete: true, want: store.CondAbsent},
	}
	for _, w := range fmWrites {
		t.Run(w.name, func(t *testing.T) {
			f := newV5Store(t)
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					state := tc.state
					if state == "" {
						state = store.StateWaiting
					}
					r := seedFMRow(t, f, state, withNote("2026-09-28 10:00:00", "fm old note")...)
					if tc.mutate != nil {
						tc.mutate(&r.examined)
					}
					if tc.first {
						if _, res, err := w.run(f.s, r.id, r.examined); err != nil || res != store.CondApplied {
							t.Fatalf("first %s = %v, %v; want CondApplied", w.name, res, err)
						}
						r.before = f.rawColumns(r.id)
					}
					if tc.delete {
						if err := f.s.DeleteSpawn(r.id); err != nil {
							t.Fatalf("DeleteSpawn: %v", err)
						}
					}
					prior, res, err := w.run(f.s, r.id, r.examined)
					if err != nil || res != tc.want || prior != "" {
						t.Fatalf("%s = %q, %v, %v; want \"\", %v, nil", w.name, prior, res, err, tc.want)
					}
					if tc.delete {
						if _, err := apitest.ReadSpawnColumns(f.path, r.id); !errors.Is(err, store.ErrSpawnNotFound) {
							t.Errorf("ReadSpawnColumns after the write: %v; want ErrSpawnNotFound", err)
						}
						return
					}
					assertRow(t, f, r.id, r.before)
				})
			}
		})
	}
}

// TestMarkMissingIfSameLifeStoreError checks a failing mark returns an error,
// no prior state or CondResult, and leaves the row unchanged (SR-5.8).
func TestMarkMissingIfSameLifeStoreError(t *testing.T) {
	f := newV5Store(t)
	r := seedFMRow(t, f, store.StatePending)
	storefix.InjectWriteFailure(t, f.path, storefix.WriteFailReuseRestore, r.id)
	prior, res, err := f.s.MarkMissingIfSameLife(r.id, r.examined)
	if err == nil || res != 0 || prior != "" {
		t.Fatalf("MarkMissingIfSameLife = %q, %v, %v; want no prior, no CondResult and an error", prior, res, err)
	}
	assertRow(t, f, r.id, r.before)
}
