package store_test

// find-missing's guarded writes (SR-3.6, SR-5.3, SR-11.3, SR-11.6; Appendix
// F.4): each applied write's whole row (EndHeldLaunch's too), and every
// guarded write refused on each part of the row snapshot. All snapshot guards
// share snapshotMatchSQL, so this is the suite's per-field matrix; each
// write's own applied and stale cases are in row_version_find_missing_test.go.

import (
	"cmp"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// fmRow is a seeded row as the sweep examined it.
type fmRow struct {
	id       string
	examined store.RowSnapshot    // GetSpawn's snapshot after seeding
	before   apitest.SpawnColumns // raw columns after seeding
}

// seedFMRow seeds a row in state with every snapshot component, a launch
// identity, launch start, life, no_pre_trust and jsonl path set; opts come last.
func seedFMRow(t *testing.T, f *v5Store, state string, opts ...apitest.SpawnOption) fmRow {
	t.Helper()
	id := f.seed(state, "sess-fm", append([]apitest.SpawnOption{apitest.WithStartedAt("2026-03-04T05:06:07.250+02:00"),
		apitest.WithPID(4242), apitest.WithProcStarttime(apitest.LinuxProcStarttime), apitest.WithTmuxSessionName("fm-ts"),
		apitest.WithLaunchIdentity(fullIdentity()), apitest.WithLaunchStartedAt(1767225600999), apitest.WithLifeNumber(3),
		apitest.WithNoPreTrust(), apitest.WithJsonlPath("/tmp/ad-fm-test/cur.jsonl")}, opts...)...)
	return fmRow{id: id, examined: rvExamine(t, f, id).Snapshot, before: f.rawColumns(id)}
}

// TestAppliedWriteChangesOnlyItsColumns checks each applied write sets only
// its own columns and adds 1 to row_version, every other column kept: the
// mark on every live state (prior state returned), the note (first and
// overwrite), the clear, the adoption, EndHeldLaunch, the unreported note
// of a pending row (b.kdf), which keeps the state, launch_started_at (CSCB),
// last_seen_at, the launch owner, every identity and an earlier note's time,
// and the repair of a stale check_permission row (b.146 rule 9), which writes
// working, or waiting when idle_since is set, and keeps idle_since.
func TestAppliedWriteChangesOnlyItsColumns(t *testing.T) {
	type applied struct {
		name, state string
		opts        []apitest.SpawnOption // after seedFMRow's
		write       func(s *store.Store, r fmRow) (store.CondResult, error)
		own         func(t *testing.T, want *apitest.SpawnColumns, after apitest.SpawnColumns) // sets the write's columns
	}
	note := func(s *store.Store, r fmRow) (store.CondResult, error) {
		return s.SetLivenessNoteIfSameLife(r.id, r.examined, "fm new note")
	}
	unreported := func(s *store.Store, r fmRow) (store.CondResult, error) {
		return s.NoteUnreportedIfSameLife(r.id, r.examined)
	}
	repairTo := func(want string) func(s *store.Store, r fmRow) (store.CondResult, error) {
		return func(s *store.Store, r fmRow) (store.CondResult, error) {
			state, res, err := s.RepairCheckPermissionIfSameLife(r.id, r.examined)
			if err == nil && state != want {
				err = fmt.Errorf("repair wrote %q, want %q", state, want)
			}
			return res, err
		}
	}
	// newSince fails unless the write set liveness_unverified_since to a CURRENT_TIMESTAMP, then expects it.
	newSince := func(t *testing.T, w *apitest.SpawnColumns, a apitest.SpawnColumns) {
		s, _ := a.LivenessUnverifiedSince.(string)
		if _, err := time.Parse(time.DateTime, s); err != nil {
			t.Errorf("liveness_unverified_since = %#v; want a CURRENT_TIMESTAMP", a.LivenessUnverifiedSince)
		}
		w.LivenessUnverifiedSince = a.LivenessUnverifiedSince
	}
	cases := []applied{
		{"note, first", store.StateWaiting, nil, note, func(t *testing.T, w *apitest.SpawnColumns, a apitest.SpawnColumns) {
			newSince(t, w, a)
			w.LivenessNote = "fm new note"
		}},
		{"unreported, first", store.StatePending, withOwner, unreported, func(t *testing.T, w *apitest.SpawnColumns, a apitest.SpawnColumns) {
			newSince(t, w, a)
			w.LivenessNote = "unreported"
		}},
		// The seeded probe_eacces note's time (2026-01-01 00:00:00) is kept: the time a note first flagged the row.
		{"unreported, over another note", store.StatePending, append(slices.Clone(liveness), withOwner...), unreported,
			func(_ *testing.T, w *apitest.SpawnColumns, _ apitest.SpawnColumns) {
				w.LivenessNote = "unreported"
			}},
		{"unreported, already unreported", store.StatePending, append(slices.Clone(unreportedNote), withOwner...), unreported,
			func(*testing.T, *apitest.SpawnColumns, apitest.SpawnColumns) {}},
		{"note, overwrite", store.StateWaiting, liveness, note, func(_ *testing.T, w *apitest.SpawnColumns, _ apitest.SpawnColumns) {
			w.LivenessNote = "fm new note"
		}},
		{"clear", store.StateWaiting, liveness, func(s *store.Store, r fmRow) (store.CondResult, error) {
			return s.ClearLivenessIfSameLife(r.id, r.examined)
		}, func(_ *testing.T, w *apitest.SpawnColumns, _ apitest.SpawnColumns) {
			w.LivenessUnverifiedSince, w.LivenessNote = nil, nil
		}},
		{"adopt", store.StateWaiting, liveness, func(s *store.Store, r fmRow) (store.CondResult, error) {
			res, _, err := s.AdoptIdentityIfSameLife(r.id, r.examined, createdIdentity())
			return res, err
		}, func(_ *testing.T, w *apitest.SpawnColumns, _ apitest.SpawnColumns) {
			*w = withIdentity(*w, createdIdentity())
		}},
		{"EndHeldLaunch", store.StatePending, append([]apitest.SpawnOption{apitest.WithRowVersion(0)}, liveness...),
			func(s *store.Store, r fmRow) (store.CondResult, error) {
				return s.EndHeldLaunch(r.id, r.before.LaunchStartedAt.(int64), rvEndedAt)
			}, func(_ *testing.T, w *apitest.SpawnColumns, _ apitest.SpawnColumns) {
				w.State, w.EndedAt, w.LaunchStartedAt = store.StateEnded, rvEndedAtText, nil
			}},
		// b.146 rule 9, problem 3: the state, the launch start's clear and
		// the version only; idle_since, the liveness note and last_seen_at kept.
		{"repair, to working", store.StateCheckPermission, append([]apitest.SpawnOption{rvRelayOn}, liveness...), repairTo(store.StateWorking),
			func(_ *testing.T, w *apitest.SpawnColumns, _ apitest.SpawnColumns) {
				w.State, w.LaunchStartedAt = store.StateWorking, nil
			}},
		{"repair, to waiting", store.StateCheckPermission, append([]apitest.SpawnOption{rvRelayOn, apitest.WithIdleSince(rvIdleSince)}, liveness...),
			repairTo(store.StateWaiting), func(_ *testing.T, w *apitest.SpawnColumns, _ apitest.SpawnColumns) {
				w.State, w.LaunchStartedAt = store.StateWaiting, nil
			}},
	}
	for _, st := range []string{store.StatePending, store.StateWaiting, store.StateWorking, store.StateAskUser, store.StateCheckPermission} {
		cases = append(cases, applied{"mark/" + st, st, liveness, func(s *store.Store, r fmRow) (store.CondResult, error) {
			prior, res, err := s.MarkMissingIfSameLife(r.id, r.examined)
			if err == nil && prior != st {
				err = fmt.Errorf("prior state %q, want %q", prior, st)
			}
			return res, err
		}, func(t *testing.T, w *apitest.SpawnColumns, a apitest.SpawnColumns) {
			if a.EndedAt == nil || a.EndedAt != a.LastSeenAt || a.LastSeenAt == w.LastSeenAt {
				t.Errorf("ended_at %#v, last_seen_at %#v (seeded %#v); want both the mark's time", a.EndedAt, a.LastSeenAt, w.LastSeenAt)
			}
			w.State, w.EndedAt, w.LastSeenAt = store.StateMissing, a.EndedAt, a.LastSeenAt
			w.LivenessUnverifiedSince, w.LivenessNote, w.LaunchStartedAt = nil, nil, nil
		}})
	}
	f := newV5Store(t)
	rows := make([]fmRow, len(cases))
	for i, tc := range cases {
		rows[i] = seedFMRow(t, f, tc.state, tc.opts...)
	}
	// CURRENT_TIMESTAMP has whole seconds: pass the seeding's second so the
	// mark's last_seen_at differs from the seeded one.
	time.Sleep(time.Until(time.Now().Truncate(time.Second).Add(time.Second)))
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := rows[i]
			if res, err := tc.write(f.s, r); err != nil || res != store.CondApplied {
				t.Fatalf("write = %v, %v; want CondApplied, nil", res, err)
			}
			after, want := f.rawColumns(r.id), r.before
			want.RowVersion = r.before.RowVersion.(int64) + 1
			tc.own(t, &want, after)
			if !reflect.DeepEqual(after, want) {
				t.Errorf("row after the write:\n got  %+v\n want %+v", after, want)
			}
		})
	}
}

// TestFindMissingIfSameLifeRefused checks each guarded write (the adoption, the
// unreported note and the check_permission repair included) is refused,
// writing nothing, for each
// differing snapshot component, a repeat from the same snapshot, a finished
// row and a deleted one.
func TestFindMissingIfSameLifeRefused(t *testing.T) {
	writes := map[string]func(s *store.Store, id string, snap store.RowSnapshot) (string, store.CondResult, error){
		"mark": func(s *store.Store, id string, snap store.RowSnapshot) (string, store.CondResult, error) {
			return s.MarkMissingIfSameLife(id, snap)
		},
		"note": func(s *store.Store, id string, snap store.RowSnapshot) (string, store.CondResult, error) {
			res, err := s.SetLivenessNoteIfSameLife(id, snap, "fm new note")
			return "", res, err
		},
		"clear": func(s *store.Store, id string, snap store.RowSnapshot) (string, store.CondResult, error) {
			res, err := s.ClearLivenessIfSameLife(id, snap)
			return "", res, err
		},
		"adopt": func(s *store.Store, id string, snap store.RowSnapshot) (string, store.CondResult, error) {
			res, now, err := s.AdoptIdentityIfSameLife(id, snap, createdIdentity())
			if err == nil && res != store.CondApplied && now != (store.RowSnapshot{}) {
				err = fmt.Errorf("snapshot %+v on %v; want the zero value", now, res)
			}
			return "", res, err
		},
		"unreported": func(s *store.Store, id string, snap store.RowSnapshot) (string, store.CondResult, error) {
			res, err := s.NoteUnreportedIfSameLife(id, snap)
			return "", res, err
		},
		"repair": func(s *store.Store, id string, snap store.RowSnapshot) (string, store.CondResult, error) {
			_, res, err := s.RepairCheckPermissionIfSameLife(id, snap)
			return "", res, err
		},
	}
	// liveState is the state a write's refusal cases start from: the unreported
	// note applies only to a pending row, so its cases start there (b.kdf);
	// the repair only to a relay-on check_permission row (b.146 rule 9), which
	// writeOpts seeds with the relay on.
	liveState := map[string]string{"unreported": store.StatePending, "repair": store.StateCheckPermission}
	writeOpts := map[string][]apitest.SpawnOption{"repair": {rvRelayOn}}
	cases := []struct {
		name   string
		state  string                   // "" = waiting
		mutate func(*store.RowSnapshot) // the examined snapshot's difference
		first  bool                     // apply the same write first
		delete bool                     // delete the row after examination
		want   store.CondResult
	}{
		{name: "row_version differs", mutate: func(s *store.RowSnapshot) { s.RowVersion++ }, want: store.CondChanged},
		{name: "started_at same instant, other text", mutate: func(s *store.RowSnapshot) { s.StartedAt = "2026-03-04 03:06:07.25" }, want: store.CondChanged},
		{name: "claude_session_id differs", mutate: func(s *store.RowSnapshot) { s.ClaudeSessionID = "sess-other" }, want: store.CondChanged},
		{name: "claude_session_id empty", mutate: func(s *store.RowSnapshot) { s.ClaudeSessionID = "" }, want: store.CondChanged},
		{name: "pid differs", mutate: func(s *store.RowSnapshot) { s.PID++ }, want: store.CondChanged},
		{name: "pid zero", mutate: func(s *store.RowSnapshot) { s.PID = 0 }, want: store.CondChanged},
		{name: "proc_starttime differs", mutate: func(s *store.RowSnapshot) { s.ProcStarttime = apitest.DarwinProcStarttime }, want: store.CondChanged},
		{name: "tmux_session_name differs", mutate: func(s *store.RowSnapshot) { s.TmuxSessionName = "fm-ts-2" }, want: store.CondChanged},
		{name: "second write from the same snapshot", first: true, want: store.CondChanged},
		{name: "ended row", state: store.StateEnded, want: store.CondChanged},
		{name: "missing row", state: store.StateMissing, want: store.CondChanged},
		{name: "deleted row", delete: true, want: store.CondAbsent},
	}
	for name, write := range writes {
		f := newV5Store(t)
		for _, tc := range cases {
			t.Run(name+"/"+tc.name, func(t *testing.T) {
				state := tc.state
				if state == "" {
					state = cmp.Or(liveState[name], store.StateWaiting)
				}
				r := seedFMRow(t, f, state, append(slices.Clone(liveness), writeOpts[name]...)...)
				if tc.mutate != nil {
					tc.mutate(&r.examined)
				}
				if tc.first {
					if _, res, err := write(f.s, r.id, r.examined); err != nil || res != store.CondApplied {
						t.Fatalf("first %s = %v, %v; want CondApplied", name, res, err)
					}
					r.before = f.rawColumns(r.id)
				}
				if tc.delete {
					if err := f.s.DeleteSpawn(r.id); err != nil {
						t.Fatalf("DeleteSpawn: %v", err)
					}
				}
				if prior, res, err := write(f.s, r.id, r.examined); err != nil || res != tc.want || prior != "" {
					t.Fatalf("%s = %q, %v, %v; want \"\", %v, nil", name, prior, res, err, tc.want)
				}
				after, err := apitest.ReadSpawnColumns(f.path, r.id)
				if tc.delete && !errors.Is(err, store.ErrSpawnNotFound) || !tc.delete && !reflect.DeepEqual(after, r.before) {
					t.Errorf("row after a refused %s (%v):\n got  %+v\n want %+v", name, err, after, r.before)
				}
			})
		}
	}
}
