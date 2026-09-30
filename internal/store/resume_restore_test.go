package store_test

// Store tests for resume's restore after a failed launch (SR-8.5, SR-5.3,
// SR-5.6, SR-20.6, AC-RES-12): the applied restore leaves no trace but the
// move's parent id and the advanced version; a restore after another write,
// with a stale version or on a deleted row writes nothing; a deleted parent
// reads NULL. Each test drives the real move first (resume_move_test.go's
// seedMoveRow and move* fixtures); the move's own columns are not re-tested.

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// rsPrior is the ResumePrior resume builds from the row it read before the move.
func rsPrior(s store.Spawn) store.ResumePrior {
	return store.ResumePrior{
		State:                   s.State,
		EndedAtText:             s.EndedAtText,
		PID:                     s.PID,
		ProcStarttime:           s.ProcStarttime,
		LivenessUnverifiedSince: s.LivenessUnverifiedSince,
		LivenessNote:            s.LivenessNote,
		Identity:                s.Identity,
	}
}

// rsMoved is a seeded row after an applied move.
type rsMoved struct {
	moveRow
	moveParent string               // the parent id the move wrote
	version    int64                // the version the move returned
	moved      apitest.SpawnColumns // raw columns right after the move
}

// rsMove moves r to pending under a fresh parent; it fails unless the move applied.
func rsMove(t *testing.T, f *v5Store, r moveRow) rsMoved {
	t.Helper()
	parent := f.seed(store.StateWaiting, "")
	res, v, err := f.move(r, parent)
	if err != nil || res != store.CondApplied {
		t.Fatalf("MoveToPending = %v, %d, %v; want CondApplied", res, v, err)
	}
	return rsMoved{moveRow: r, moveParent: parent, version: v, moved: f.rawColumns(r.id)}
}

// rsRestore runs the restore of m with the prior read before its move.
func (f *v5Store) rsRestore(m rsMoved, version int64) (store.CondResult, error) {
	return f.s.RestoreAfterFailedResume(m.id, version, rsPrior(m.examined))
}

// rsSeedMissingNoEndedAt seeds a missing row with a NULL ended_at and every
// other restored column set, read as resume would.
func rsSeedMissingNoEndedAt(t *testing.T, f *v5Store) moveRow {
	t.Helper()
	id := f.seed(store.StateMissing, moveSessionID,
		apitest.WithPID(5151),
		apitest.WithProcStarttime(apitest.LinuxProcStarttime),
		apitest.WithLivenessUnverifiedSince("2026-09-28T10:00:00.125Z"),
		apitest.WithLivenessNote("restore-test note"),
		apitest.WithLaunchIdentity(moveIdentity()),
		apitest.WithLaunchStartedAt(1767225600999),
		apitest.WithLifeNumber(4),
		apitest.WithNoPreTrust(),
		apitest.WithJsonlPath("/tmp/ad-restore-test/cur.jsonl"),
		apitest.WithSessionHistory(apitest.SessionHistorySeed{
			SessionID: "sess-restore-old", JSONLPath: "/tmp/ad-restore-test/old.jsonl", Life: 3,
			RecordedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		}),
	)
	parent := f.seed(store.StateWaiting, "")
	if err := apitest.SeedParentChild(f.path, parent, id); err != nil {
		t.Fatalf("SeedParentChild: %v", err)
	}
	examined, err := f.s.GetSpawn(id)
	if err != nil {
		t.Fatalf("GetSpawn(%s): %v", id, err)
	}
	return moveRow{id: id, parent: parent, examined: examined, before: f.rawColumns(id), history: f.historyAllLives(id)}
}

// rsRestored are the columns the restore writes back from the pre-move row.
func rsRestored(c apitest.SpawnColumns) map[string]any {
	return map[string]any{
		"state": c.State, "ended_at": c.EndedAt, "pid": c.PID, "proc_starttime": c.ProcStarttime,
		"liveness_unverified_since": c.LivenessUnverifiedSince, "liveness_note": c.LivenessNote,
		"launch_token": c.LaunchToken, "tmux_socket": c.TmuxSocket,
		"tmux_server_pid": c.TmuxServerPID, "tmux_server_started": c.TmuxServerStarted,
		"tmux_server_starttime": c.TmuxServerStarttime, "pane_id": c.PaneID,
		"pane_pid": c.PanePID, "pane_starttime": c.PaneStarttime,
	}
}

// rsWantRestored is the row an applied restore of m must leave: the pre-move
// row with parentID, no launch start and the moved version plus one.
func rsWantRestored(m rsMoved, parentID any) apitest.SpawnColumns {
	want := m.before
	want.ParentID = parentID
	want.LaunchStartedAt = nil
	want.RowVersion = m.version + 1
	return want
}

// rsAssertRow fails with each column (and its stored Go type) where got
// differs from want, so a text/number or re-formatted ended_at shows.
func rsAssertRow(t *testing.T, got, want apitest.SpawnColumns) {
	t.Helper()
	gv, wv := reflect.ValueOf(got), reflect.ValueOf(want)
	for i := 0; i < gv.NumField(); i++ {
		g, w := gv.Field(i).Interface(), wv.Field(i).Interface()
		if !reflect.DeepEqual(g, w) {
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

// TestRestoreAfterFailedResumeApplied checks an applied restore leaves the pre-move
// row byte for byte except the move's parent id, no launch start and version+1.
func TestRestoreAfterFailedResumeApplied(t *testing.T) {
	identityNull := []string{"launch_token", "tmux_socket", "tmux_server_pid", "tmux_server_started",
		"tmux_server_starttime", "pane_id", "pane_pid", "pane_starttime"}
	cases := []struct {
		name     string
		seed     func(t *testing.T, f *v5Store) moveRow
		wantNull []string // restored columns the seed leaves NULL; every other is set
	}{
		{name: "ended, RFC3339 ended_at with a fraction",
			seed: func(t *testing.T, f *v5Store) moveRow { return seedMoveRow(t, f, moveSpec{}) }},
		{name: "missing, RFC3339 ended_at with a fraction",
			seed: func(t *testing.T, f *v5Store) moveRow {
				return seedMoveRow(t, f, moveSpec{state: store.StateMissing})
			}},
		{name: "ended, store-layout ended_at",
			seed: func(t *testing.T, f *v5Store) moveRow {
				return seedMoveRow(t, f, moveSpec{opts: []apitest.SpawnOption{apitest.WithEndedAt("2026-09-29 01:02:03")}})
			}},
		{name: "missing, NULL ended_at", seed: rsSeedMissingNoEndedAt, wantNull: []string{"ended_at"}},
		{name: "ended, no token, socket or identity",
			seed: func(t *testing.T, f *v5Store) moveRow {
				return seedMoveRow(t, f, moveSpec{opts: []apitest.SpawnOption{apitest.WithNoLaunchToken()}})
			}, wantNull: identityNull},
		{name: "missing, NULL pid, no token, socket or identity",
			seed: func(t *testing.T, f *v5Store) moveRow {
				return seedMoveRow(t, f, moveSpec{state: store.StateMissing, noPID: true,
					opts: []apitest.SpawnOption{apitest.WithNoLaunchToken()}})
			}, wantNull: append([]string{"pid"}, identityNull...)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newV5Store(t)
			r := tc.seed(t, f)
			restored := rsRestored(r.before)
			for _, col := range tc.wantNull {
				if restored[col] != nil {
					t.Fatalf("seed stored %s = %s; want NULL", col, rsShow(restored[col]))
				}
				delete(restored, col)
			}
			requireSet(t, restored)
			requireSet(t, moveKept(r.before))
			if r.before.LaunchStartedAt == nil || len(r.history) == 0 {
				t.Fatal("seed left launch_started_at NULL or no history; the checks would be vacuous")
			}
			m := rsMove(t, f, r)
			if m.moved.LaunchToken != moveToken || m.moved.TmuxSocket != moveSocket || m.moved.EndedAt != nil {
				t.Fatalf("move left token %s, socket %s, ended_at %s; want the move's token and socket, no ended_at",
					rsShow(m.moved.LaunchToken), rsShow(m.moved.TmuxSocket), rsShow(m.moved.EndedAt))
			}

			if res, err := f.rsRestore(m, m.version); err != nil || res != store.CondApplied {
				t.Fatalf("RestoreAfterFailedResume = %v, %v; want CondApplied", res, err)
			}
			after := f.rawColumns(r.id)
			if !reflect.DeepEqual(after.EndedAt, r.before.EndedAt) {
				t.Errorf("ended_at = %s; want the pre-move %s byte for byte", rsShow(after.EndedAt), rsShow(r.before.EndedAt))
			}
			rsAssertRow(t, after, rsWantRestored(m, m.moveParent))
			if got := f.historyAllLives(r.id); !reflect.DeepEqual(got, r.history) {
				t.Errorf("history %+v -> %+v; want unchanged", r.history, got)
			}
		})
	}
}

// TestRestoreAfterFailedResumeChangedWritesNothing checks a restore after another
// (non-hook) write, with a wrong version or repeated, gives CondChanged and writes nothing.
func TestRestoreAfterFailedResumeChangedWritesNothing(t *testing.T) {
	cases := []struct {
		name    string
		spec    moveSpec
		write   func(t *testing.T, f *v5Store, m rsMoved)                  // a write after the move; nil for none
		version func(m rsMoved) int64                                      // the moved version the restore passes
		check   func(t *testing.T, m rsMoved, before apitest.SpawnColumns) // confirms the write's effect
	}{
		{name: "HealJsonlPath after the move, row stays pending", spec: moveSpec{noJsonl: true},
			write: func(t *testing.T, f *v5Store, m rsMoved) {
				ok, err := f.s.HealJsonlPath(m.id, moveSessionID, "/tmp/ad-restore-test/healed.jsonl")
				wantBool(t, "HealJsonlPath", ok, err, true)
			},
			version: func(m rsMoved) int64 { return m.version },
			check:   rsWantPendingAdvanced},
		{name: "SetParentID after the move, row stays pending",
			write: func(t *testing.T, f *v5Store, m rsMoved) {
				if err := f.s.SetParentID(m.id, f.seed(store.StateWaiting, "")); err != nil {
					t.Fatalf("SetParentID: %v", err)
				}
			},
			version: func(m rsMoved) int64 { return m.version },
			check:   rsWantPendingAdvanced},
		{name: "stale moved version", version: func(m rsMoved) int64 { return m.version - 1 }},
		{name: "moved version ahead", version: func(m rsMoved) int64 { return m.version + 1 }},
		{name: "zero moved version", version: func(rsMoved) int64 { return 0 }},
		{name: "second restore after an applied one",
			write: func(t *testing.T, f *v5Store, m rsMoved) {
				if res, err := f.rsRestore(m, m.version); err != nil || res != store.CondApplied {
					t.Fatalf("first RestoreAfterFailedResume = %v, %v; want CondApplied", res, err)
				}
			},
			version: func(m rsMoved) int64 { return m.version },
			check: func(t *testing.T, m rsMoved, before apitest.SpawnColumns) {
				t.Helper()
				if before.State != m.before.State {
					t.Fatalf("first restore left state %s; want %s", rsShow(before.State), rsShow(m.before.State))
				}
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newV5Store(t)
			m := rsMove(t, f, seedMoveRow(t, f, tc.spec))
			if tc.write != nil {
				tc.write(t, f, m)
			}
			before, history := f.rawColumns(m.id), f.historyAllLives(m.id)
			if tc.check != nil {
				tc.check(t, m, before)
			}
			if res, err := f.rsRestore(m, tc.version(m)); err != nil || res != store.CondChanged {
				t.Fatalf("RestoreAfterFailedResume = %v, %v; want CondChanged", res, err)
			}
			assertMoveWroteNothing(t, f, m.id, before, history)
		})
	}
}

// rsWantPendingAdvanced fails unless the intervening write left the row
// pending at a version past the move's.
func rsWantPendingAdvanced(t *testing.T, m rsMoved, before apitest.SpawnColumns) {
	t.Helper()
	if before.State != store.StatePending || before.RowVersion != m.version+1 {
		t.Fatalf("after the write: state %s, row_version %s; want pending at %d",
			rsShow(before.State), rsShow(before.RowVersion), m.version+1)
	}
}

// TestRestoreAfterFailedResumeAbsent checks a row deleted after the move
// gives CondAbsent and the restore creates no row.
func TestRestoreAfterFailedResumeAbsent(t *testing.T) {
	f := newV5Store(t)
	m := rsMove(t, f, seedMoveRow(t, f, moveSpec{}))
	if err := f.s.DeleteSpawn(m.id); err != nil {
		t.Fatalf("DeleteSpawn: %v", err)
	}
	if res, err := f.rsRestore(m, m.version); err != nil || res != store.CondAbsent {
		t.Fatalf("RestoreAfterFailedResume = %v, %v; want CondAbsent", res, err)
	}
	if _, err := apitest.ReadSpawnColumns(f.path, m.id); !errors.Is(err, store.ErrSpawnNotFound) {
		t.Errorf("ReadSpawnColumns after the restore: %v; want ErrSpawnNotFound", err)
	}
}

// TestRestoreAfterFailedResumeParentDeleted checks a restore after the move's
// parent row was deleted (ON DELETE SET NULL) applies, with parent_id NULL.
func TestRestoreAfterFailedResumeParentDeleted(t *testing.T) {
	f := newV5Store(t)
	m := rsMove(t, f, seedMoveRow(t, f, moveSpec{}))
	if err := f.s.DeleteSpawn(m.moveParent); err != nil {
		t.Fatalf("DeleteSpawn(parent): %v", err)
	}
	if got := f.rawColumns(m.id); got.ParentID != nil {
		t.Fatalf("parent_id after deleting the parent = %s; want NULL", rsShow(got.ParentID))
	}
	if res, err := f.rsRestore(m, m.version); err != nil || res != store.CondApplied {
		t.Fatalf("RestoreAfterFailedResume = %v, %v; want CondApplied", res, err)
	}
	rsAssertRow(t, f.rawColumns(m.id), rsWantRestored(m, nil))
	if got := f.historyAllLives(m.id); !reflect.DeepEqual(got, m.history) {
		t.Errorf("history %+v -> %+v; want unchanged", m.history, got)
	}
}

// TestRestoreAfterFailedResumeErrors checks a closed store or a non-finished prior
// state returns an error, no CondResult, and leaves the row as the move left it.
func TestRestoreAfterFailedResumeErrors(t *testing.T) {
	cases := []struct {
		name  string
		state *string                                     // prior.State; nil keeps the pre-move state
		store func(t *testing.T, f *v5Store) *store.Store // the store the restore runs on; nil for f.s
	}{
		{name: "closed store", store: func(t *testing.T, f *v5Store) *store.Store {
			s, err := store.Open(f.path)
			if err != nil {
				t.Fatalf("store.Open: %v", err)
			}
			if err := s.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			return s
		}},
		{name: "prior state pending", state: rsStr(store.StatePending)},
		{name: "prior state waiting", state: rsStr(store.StateWaiting)},
		{name: "prior state empty", state: rsStr("")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newV5Store(t)
			m := rsMove(t, f, seedMoveRow(t, f, moveSpec{}))
			s := f.s
			if tc.store != nil {
				s = tc.store(t, f)
			}
			prior := rsPrior(m.examined)
			if tc.state != nil {
				prior.State = *tc.state
			}
			res, err := s.RestoreAfterFailedResume(m.id, m.version, prior)
			if err == nil || res != 0 {
				t.Fatalf("RestoreAfterFailedResume = %v, %v; want an error with no CondResult", res, err)
			}
			assertMoveWroteNothing(t, f, m.id, m.moved, m.history)
		})
	}
}

// rsStr returns a pointer to v.
func rsStr(v string) *string { return &v }
