package store_test

// Store tests for find-missing's guarded writes (SR-3.6, SR-5.3, SR-11.3,
// SR-11.4, SR-11.6; Appendix F.4): the applied mark, note write, clear and
// adoption, and the refusals that write nothing. Rows are seeded through
// apitest (SR-20.2); the note write's and clear's store errors are covered
// through pkg/api's failing store (SR-20.3), so only the mark's and the
// adoption's are injected here.

import (
	"errors"
	"fmt"
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

// fmAdopted is the identity the adoption writes: every field differs from
// moveIdentity's, so "written" and "unchanged" are never vacuous.
func fmAdopted() store.LaunchIdentity {
	return store.LaunchIdentity{
		Token: "ffffffffffffffff", Socket: "/tmp/ad-fm-test/other-sock", // never written
		ServerPID: 555, ServerStart: 1767225900, ServerStarttime: apitest.DarwinProcStarttime,
		PaneID: "%11", PanePID: 666, PaneStarttime: apitest.LinuxProcStarttime,
	}
}

// fmWrite is one of the guarded writes; prior is the mark's returned prior
// state and "" for the others. The adoption reports a non-zero snapshot on a
// refusal as an error.
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
	{"adopt", func(s *store.Store, id string, snap store.RowSnapshot) (string, store.CondResult, error) {
		res, now, err := s.AdoptIdentityIfSameLife(id, snap, fmAdopted())
		if err == nil && res != store.CondApplied && now != (store.RowSnapshot{}) {
			err = fmt.Errorf("snapshot %+v on %v; want the zero value", now, res)
		}
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

// TestFindMissingIfSameLifeRefused checks each write (adoption included) is refused,
// writing nothing, for a differing snapshot component, an earlier write, a finished or deleted row.
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

// TestIfSameLifeStoreError checks a failing mark or adoption returns an error,
// a zero CondResult and zero prior state or snapshot, row unchanged (SR-5.8).
func TestIfSameLifeStoreError(t *testing.T) {
	cases := []struct {
		name string
		kind storefix.WriteFailureKind
		run  func(s *store.Store, r fmRow) (extra any, res store.CondResult, err error)
		zero any
	}{
		{"mark", storefix.WriteFailReuseRestore, func(s *store.Store, r fmRow) (any, store.CondResult, error) {
			return s.MarkMissingIfSameLife(r.id, r.examined)
		}, ""},
		{"adopt", storefix.WriteFailLaunchIdentity, func(s *store.Store, r fmRow) (any, store.CondResult, error) {
			res, now, err := s.AdoptIdentityIfSameLife(r.id, r.examined, fmAdopted())
			return now, res, err
		}, store.RowSnapshot{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newV5Store(t)
			r := seedFMRow(t, f, store.StatePending)
			storefix.InjectWriteFailure(t, f.path, tc.kind, r.id)
			extra, res, err := tc.run(f.s, r)
			if err == nil || res != 0 || extra != tc.zero {
				t.Fatalf("%s = %#v, %v, %v; want %#v, no CondResult and an error", tc.name, extra, res, err, tc.zero)
			}
			assertRow(t, f, r.id, r.before)
		})
	}
}

// seedAdoptRow seeds a live row recording only its launch token and socket,
// a lost create reply, with the liveness columns set.
func seedAdoptRow(t *testing.T, f *v5Store, state string) fmRow {
	t.Helper()
	prev := moveIdentity()
	lost := store.LaunchIdentity{Token: prev.Token, Socket: prev.Socket}
	return seedFMRow(t, f, state, append(withNote("2026-09-28 10:00:00", "fm old note"), apitest.WithLaunchIdentity(lost))...)
}

// TestAdoptIdentityIfSameLifeApplied checks the adoption writes the six identity
// columns (zero as NULL), version +1, all else kept, and returns a fresh read's snapshot.
func TestAdoptIdentityIfSameLifeApplied(t *testing.T) {
	serverOnly := fmAdopted()
	serverOnly.PaneID, serverOnly.PanePID, serverOnly.PaneStarttime = "", 0, ""
	cases := []struct {
		name  string
		state string
		adopt store.LaunchIdentity
	}{
		{"waiting row, server and pane", store.StateWaiting, fmAdopted()},
		{"pending row, server and pane", store.StatePending, fmAdopted()},
		{"server only leaves pane columns NULL", store.StateWaiting, serverOnly},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newV5Store(t)
			r := seedAdoptRow(t, f, tc.state)
			b := r.before
			requireSet(t, map[string]any{"launch_token": b.LaunchToken, "tmux_socket": b.TmuxSocket,
				"launch_started_at": b.LaunchStartedAt, "liveness_note": b.LivenessNote,
				"liveness_unverified_since": b.LivenessUnverifiedSince, "life_number": b.LifeNumber, "no_pre_trust": b.NoPreTrust})
			if !reflect.DeepEqual(identityColumns(b), wantIdentityColumns(store.LaunchIdentity{})) {
				t.Fatalf("seeded identity %v; want all NULL", identityColumns(b))
			}
			mark := store.TrailMark(t)
			res, now, err := f.s.AdoptIdentityIfSameLife(r.id, r.examined, tc.adopt)
			if err != nil || res != store.CondApplied {
				t.Fatalf("AdoptIdentityIfSameLife = %v, %v; want CondApplied, nil", res, err)
			}
			assertRow(t, f, r.id, withIdentity(wantAdvanced(t, r.before), tc.adopt))
			if fresh := rvExamine(t, f, r.id).Snapshot; now != fresh || now.RowVersion != r.examined.RowVersion+1 {
				t.Errorf("returned snapshot %+v; want the fresh read %+v, version %d", now, fresh, r.examined.RowVersion+1)
			}
			adoptNoTrail(t, mark, r.id)
		})
	}
}

// TestAdoptIdentityIfSameLifeChainsGuard checks the adoption's returned snapshot
// guards each verdict write, while the pre-adoption snapshot is refused.
func TestAdoptIdentityIfSameLifeChainsGuard(t *testing.T) {
	for _, w := range fmWrites[:3] {
		t.Run(w.name, func(t *testing.T) {
			f := newV5Store(t)
			r := seedAdoptRow(t, f, store.StateWaiting)
			res, now, err := f.s.AdoptIdentityIfSameLife(r.id, r.examined, fmAdopted())
			if err != nil || res != store.CondApplied {
				t.Fatalf("AdoptIdentityIfSameLife = %v, %v; want CondApplied, nil", res, err)
			}
			adopted := f.rawColumns(r.id)
			if _, res, err := w.run(f.s, r.id, r.examined); err != nil || res != store.CondChanged {
				t.Fatalf("%s from the pre-adoption snapshot = %v, %v; want CondChanged, nil", w.name, res, err)
			}
			assertRow(t, f, r.id, adopted)
			if _, res, err := w.run(f.s, r.id, now); err != nil || res != store.CondApplied {
				t.Fatalf("%s from the returned snapshot = %v, %v; want CondApplied, nil", w.name, res, err)
			}
		})
	}
}
