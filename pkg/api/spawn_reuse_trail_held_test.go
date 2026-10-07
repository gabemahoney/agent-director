package api_test

// spawn_reuse_trail_held_test.go covers reuse's ad.launch.name_held record
// after "duplicate session" (SR-14, SR-10.4; AC-SPN-10): exactly one per
// held-name reuse, launch reuse and source ad_spawn, every field per
// re-lookup outcome and restore result, written after ad.spawn.reused and
// ad.spawn.reuse_restored. It runs on arrangeHeld, with the holders under the
// requested name (rutRenamed).

import (
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// rutHeldRun is one held-name reuse: its scene, error, captured log and the
// id's ad.launch.name_held records written by the call.
type rutHeldRun struct {
	sc   *heldScene
	err  error
	logs string
	recs []map[string]any
}

// rutHeld arranges spec on r under the requested name, reuses r with that
// name through w (a plain hookedReuseStore when nil) and returns the run;
// atCreate, when set, runs as the create returns, after arrangeHeld's
// placement.
func (e *killEnv) rutHeld(t *testing.T, r reuseRow, spec heldSpec, w *hookedReuseStore, atCreate func()) rutHeldRun {
	t.Helper()
	if w == nil {
		w = &hookedReuseStore{st: e.st}
	}
	sc := e.arrangeHeld(t, rutRenamed(r), spec)
	if atCreate != nil {
		e.rec.AfterCall(tmux.CallCreate, func(tmuxfix.SocketCall, error) { atCreate() })
	}
	_, logs, err := e.reuseWith(t, w, reuseParams(t, r, reuseRequest{Name: rutRequested}))
	return rutHeldRun{sc: sc, err: err, logs: logs, recs: ptRecords(t, sc.before.mark, rutNameHeld, r.ID)}
}

// rutAssertNameHeld checks run wrote exactly one reuse record: every SR-14
// field against w (rhtWant's per-outcome fields), rowResult, the store error
// of the restore's WARN line (null when none), the exact key set and no
// label content, token, other id or session environment.
func (e *killEnv) rutAssertNameHeld(t *testing.T, run rutHeldRun, w rhtWant, rowResult string) {
	t.Helper()
	if len(run.recs) != 1 {
		t.Fatalf("%s records = %d; want 1: %v", rutNameHeld, len(run.recs), run.recs)
	}
	r := run.sc.r
	var storeError any
	if s := rutRestoreError(run.logs, r.ID); s != "" {
		storeError = s
	}
	want := map[string]any{
		"source": "ad_spawn", "claude_instance_id": r.ID, "launch": "reuse", "tmux_session_name": rutRequested,
		"tmux_socket": r.Socket, "store_id": e.storeID, "lookup_outcome": w.lookup, "outcome": ptErrName(w.sentinel),
		"row_result": rowResult, "store_error": storeError, "carries_this_id": w.carries, "current_launch": w.current,
		"tmux_session_id": nil, "session_created": nil, "attach_command": nil, "end_command": nil,
	}
	if w.holder {
		h := run.sc.Holder()
		q := func(s string) string { return "'" + s + "'" }
		want["tmux_session_id"], want["session_created"] = h.ID, float64(h.Created)
		want["attach_command"] = "tmux -u -S " + q(r.Socket) + " attach-session -r -t " + q(h.ID)
		want["end_command"] = "tmux -u -S " + q(r.Socket) + " kill-session -t " + q(h.ID)
	}
	for k, v := range ptCaller() {
		want[k] = v
	}
	rutAssertFields(t, run.recs[0], ptKeys, want)
	ktrAssertNoForeignContent(t, run.recs[0], rhtForbid(e, run.sc)...)
}

// TestSpawnReuseTrailNameHeldPerOutcome: one ad.launch.name_held (launch reuse) per
// re-lookup outcome, every field, after reused and one applied reuse_restored.
func TestSpawnReuseTrailNameHeldPerOutcome(t *testing.T) {
	t.Parallel()
	conflict, unresponsive, unavailable := api.ErrTmuxSessionConflict, api.ErrTmuxUnresponsive, api.ErrTmuxNotAvailable
	held := func(lookup string, sentinel error, carries, current any) rhtWant {
		return rhtWant{lookup: lookup, sentinel: sentinel, holder: true, carries: carries, current: current}
	}
	none := func(lookup string, sentinel error) rhtWant { return rhtWant{lookup: lookup, sentinel: sentinel} }
	relookup := func(f tmux.Failure) func(*killEnv) heldSpec {
		return func(*killEnv) heldSpec { return heldSpec{Holder: holderNone, Relookup: tmuxfix.Script{Failure: f}} }
	}
	cases := []struct {
		name string
		age  func(e *killEnv) time.Duration // the row's ended_at age at the re-lookup
		spec func(e *killEnv) heldSpec
		want rhtWant
	}{
		{name: "old label", spec: holderOnly(holderOld), want: held("leftover", conflict, true, false)},
		{name: "current label, still stopping",
			age:  func(e *killEnv) time.Duration { return e.cfg.EffectiveStoppingWindow() / 2 },
			spec: func(e *killEnv) heldSpec { return heldSpec{Holder: holderCurrent, Created: rceSettled(e)} },
			want: held("ours", unresponsive, true, true)},
		{name: "current label, still starting",
			spec: func(e *killEnv) heldSpec {
				return heldSpec{Holder: holderCurrent, Created: e.cfg.EffectiveStartingSession() / 2}
			},
			want: held("ours", unresponsive, true, true)},
		{name: "current label, this row's own id",
			spec: func(e *killEnv) heldSpec { return heldSpec{Holder: holderCurrent, Created: rceSettled(e)} },
			want: held("ours", conflict, true, true)},
		{name: "foreign label", spec: holderOnly(holderForeign), want: held("gone", conflict, false, nil)},
		{name: "another store's label", spec: holderOnly(holderOtherStore),
			want: held("gone", conflict, false, nil)},
		{name: "no label", spec: holderOnly(holderNone), want: held("gone", conflict, false, nil)},
		{name: "malformed label", spec: holderOnly(holderMalformed), want: held("gone", conflict, false, nil)},
		{name: "more than one entry matches", spec: holderOnly(holderAmbiguous), want: none("gone", unresponsive)},
		{name: "conflicting labels", spec: holderOnly(holderConflicting),
			want: held("provenance_conflict", conflict, nil, nil)},
		{name: "vanished", spec: holderOnly(holderVanished), want: none("gone", api.ErrTmuxSessionCreate)},
		{name: "unreadable", spec: relookup(tmux.FailTimeout), want: none("cant_tell", unresponsive)},
		{name: "tmux unavailable", spec: relookup(tmux.FailUnavailable), want: none("tmux_unavailable", unavailable)},
		{name: "different server, no holder", spec: func(*killEnv) heldSpec {
			return heldSpec{Holder: holderVanished, Server: heldServerRebound}
		}, want: none("different_server", unavailable)},
		{name: "different server, holder listed", spec: func(*killEnv) heldSpec {
			return heldSpec{Holder: holderNone, Server: heldServerRebound}
		}, want: held("different_server", unavailable, nil, nil)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			age := rceSettled(e)
			if tc.age != nil {
				age = tc.age(e)
			}
			r := e.seedReusable(t, agentGone, reuseRowSpec{Held: true, Age: age})

			run := e.rutHeld(t, r, tc.spec(e), nil, nil)

			assertOneSentinel(t, run.err, tc.want.sentinel)
			e.rutAssertNameHeld(t, run, tc.want, "restored")
			rutAssertRestored(t, run.sc.before.mark, r.ID, true, ptErrName(run.err), run.logs)
			rutAssertOrder(t, run.sc.before.mark, r.ID, rutReused, rutRestored, rutNameHeld)
		})
	}
}

// TestSpawnReuseTrailNameHeldRowResult: row_result and store_error follow the
// restore, as reuse_restored's applied and restore_error do.
func TestSpawnReuseTrailNameHeldRowResult(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		arrange   func(t *testing.T, e *killEnv, r reuseRow, w *hookedReuseStore) // before arrangeHeld
		atCreate  func(t *testing.T, e *killEnv, r reuseRow)                      // after arrangeHeld's placement
		rowResult string
		applied   bool
	}{
		{name: "applied", rowResult: "restored", applied: true},
		{name: "versioned write after the reset", rowResult: "left_changed",
			arrange: func(t *testing.T, e *killEnv, r reuseRow, w *hookedReuseStore) {
				parent := e.seedRelative(t, r.ID, false)
				w.afterReset(func() {
					if err := e.st.SetParentID(r.ID, parent); err != nil {
						t.Errorf("SetParentID: %v", err)
					}
				})
			}},
		{name: "row removed on the failing create", rowResult: "left_changed",
			atCreate: func(t *testing.T, e *killEnv, r reuseRow) {
				if err := e.st.DeleteSpawn(r.ID); err != nil {
					t.Errorf("DeleteSpawn: %v", err)
				}
			}},
		{name: "restore store error", rowResult: "still_pending",
			arrange: func(t *testing.T, e *killEnv, r reuseRow, _ *hookedReuseStore) {
				storefix.InjectWriteFailure(t, e.dbPath, storefix.WriteFailReuseRestore, r.ID)
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			r := e.seedReusable(t, agentGone, reuseRowSpec{Held: true, Age: rceSettled(e)})
			w := &hookedReuseStore{st: e.st}
			if tc.arrange != nil {
				tc.arrange(t, e, r, w)
			}
			var atCreate func()
			if tc.atCreate != nil {
				atCreate = func() { tc.atCreate(t, e, r) }
			}

			run := e.rutHeld(t, r, heldSpec{Holder: holderNone}, w, atCreate)

			assertOneSentinel(t, run.err, api.ErrTmuxSessionConflict)
			if (tc.rowResult == "still_pending") != (rutRestoreError(run.logs, r.ID) != "") {
				t.Errorf("restore WARN line in %q; want one only for still_pending", run.logs)
			}
			want := rhtWant{lookup: "gone", sentinel: api.ErrTmuxSessionConflict, holder: true, carries: false}
			e.rutAssertNameHeld(t, run, want, tc.rowResult)
			rutAssertRestored(t, run.sc.before.mark, r.ID, tc.applied, "ErrTmuxSessionConflict", run.logs)
			rutAssertOrder(t, run.sc.before.mark, r.ID, rutReused, rutRestored, rutNameHeld)
		})
	}
}
