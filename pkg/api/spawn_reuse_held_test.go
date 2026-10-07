package api_test

// spawn_reuse_held_test.go covers reuse after its create answers "duplicate
// session" (SR-10.4, SR-8.5, SR-14; AC-REUSE-07, AC-SPN-10): the restore and
// its row sentence, the trail (ad.launch.name_held after reused and
// reuse_restored, the re-lookup's disagree records). Each column's outcome is
// TestCallTableReuse's; following the advice is A12's.

import (
	"cmp"
	"reflect"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// ruhScene is one held-name reuse as arranged: the row as seeded, the call's
// parameters (a new requested name) and parent id, arrangeHeld's scene under
// the requested name, and the row when the re-lookup returned.
type ruhScene struct {
	r          reuseRow
	p          api.SpawnParams
	caller     string
	sc         *heldScene
	atRelookup apitest.SpawnColumns
}

// ruhArrange seeds a reusable row of state prior that ended age before
// heldInstant and arranges spec's "duplicate session" under a new requested
// name, with a caller parent other than the row's; call it last before the reuse.
func (e *killEnv) ruhArrange(t *testing.T, prior string, age time.Duration, spec heldSpec) *ruhScene {
	t.Helper()
	r := e.seedReusable(t, agentGone, reuseRowSpec{State: prior, Age: age, Held: true})
	s := &ruhScene{r: r, caller: e.seedRow(t, killRowSpec{State: store.StateEnded, Agent: agentGone, NoSession: true}).ID}
	s.p = reuseParams(t, r, reuseRequest{Name: "reuse-held-" + uuid.NewString()[:8], Parent: s.caller})
	requested := r.resumeRow
	requested.killRow = requested.killRow.withName(s.p.TmuxSessionName)
	lookups := 0
	e.rec.AfterCall(tmux.CallLookup, func(c tmuxfix.SocketCall, _ error) {
		if c.Socket == r.Socket {
			if lookups++; lookups == 2 {
				s.atRelookup, _ = e.rrcColumns(t, r.ID)
			}
		}
	})
	s.sc = e.arrangeHeld(t, requested, spec)
	return s
}

// ruhForbid is what no description of s's reuse may show: rhdForbid's, the
// reset's new token and the old recorded name, quoted.
func ruhForbid(e *killEnv, s *ruhScene) []string {
	out := append(rhdForbid(e, s.sc), strconv.Quote(s.r.Name))
	if tok, ok := s.sc.Moved.LaunchToken.(string); ok {
		out = append(out, tok)
	}
	return out
}

// rutHeldRun is one held-name reuse: its scene, requested name, error,
// captured log and the id's ad.launch.name_held records written by the call.
type rutHeldRun struct {
	sc   *heldScene
	name string
	err  error
	logs string
	recs []map[string]any
}

// rutHeld arranges spec on r under the requested name rutRequested and reuses
// r with that name through a hookedReuseStore, returning the run.
func (e *killEnv) rutHeld(t *testing.T, r reuseRow, spec heldSpec, w *hookedReuseStore) rutHeldRun {
	t.Helper()
	if w == nil {
		w = &hookedReuseStore{st: e.st}
	}
	sc := e.arrangeHeld(t, rutRenamed(r), spec)
	_, logs, err := e.reuseWith(t, w, reuseParams(t, r, reuseRequest{Name: rutRequested}))
	return rutHeldRun{sc: sc, name: rutRequested, err: err, logs: logs, recs: ptRecords(t, sc.before.mark, rutNameHeld, r.ID)}
}

// rutAssertNameHeld checks run's one reuse name_held: w's fields, rowResult,
// the restore WARN line's store error, the exact key set, no foreign content.
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
	var holder *tmuxfix.SeedSession
	if w.holder {
		h := run.sc.Holder()
		holder = &h
	}
	want := nameHeldFields("reuse", r.ID, run.name, r.Socket, e.storeID, holder, w.lookup, ptErrName(w.sentinel), rowResult,
		storeError, w.carries, w.current)
	assertTrailRecord(t, run.recs[0], ptKeys, want, rhtForbid(e, run.sc)...)
}

// TestSpawnReuseHeldRowResult: the restore after "duplicate session" (applied
// byte for byte after the re-lookup, the reset before the create with the
// caller's parent; changed, removed or failed) decides the row sentence,
// row_result, store_error and applied; one error quoting the requested name,
// three calls, the holder untouched.
func TestSpawnReuseHeldRowResult(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID with t.Setenv; it checks every record written to the
	// shared trail since its mark.
	cases := []struct {
		name      string
		prior     string
		arrange   func(t *testing.T, e *killEnv, s *ruhScene, rs *hookedReuseStore)
		outcome   apitest.RestoreOutcome
		rowResult string
	}{
		{name: "applied, ended", prior: store.StateEnded, outcome: apitest.RestoreApplied, rowResult: "restored"},
		{name: "applied, missing", prior: store.StateMissing, outcome: apitest.RestoreApplied, rowResult: "restored"},
		{name: "parent id written after the reset", outcome: apitest.RestoreRowChanged, rowResult: "left_changed",
			arrange: func(t *testing.T, e *killEnv, s *ruhScene, rs *hookedReuseStore) {
				other := e.seedRow(t, killRowSpec{State: store.StateEnded, Agent: agentGone, NoSession: true}).ID
				rs.afterReset(func() {
					if err := e.st.SetParentID(s.r.ID, other); err != nil {
						t.Errorf("SetParentID: %v", err)
					}
				})
			}},
		{name: "row deleted at the create", outcome: apitest.RestoreRowRemoved, rowResult: "left_changed",
			arrange: func(t *testing.T, e *killEnv, s *ruhScene, _ *hookedReuseStore) {
				e.rec.AfterCall(tmux.CallCreate, func(tmuxfix.SocketCall, error) {
					if err := e.st.DeleteSpawn(s.r.ID); err != nil {
						t.Errorf("DeleteSpawn: %v", err)
					}
				})
			}},
		{name: "restore store error", outcome: apitest.RestoreStoreError, rowResult: "still_pending",
			arrange: func(t *testing.T, e *killEnv, s *ruhScene, _ *hookedReuseStore) {
				storefix.InjectWriteFailure(t, e.dbPath, storefix.WriteFailReuseRestore, s.r.ID)
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			s := e.ruhArrange(t, cmp.Or(tc.prior, store.StateEnded), rlkSettled(e), heldSpec{Holder: holderNone})
			rs := &hookedReuseStore{st: e.st}
			if tc.arrange != nil {
				tc.arrange(t, e, s, rs)
			}

			_, logs, err := e.reuseWith(t, rs, s.p)

			assertOneSentinel(t, err, api.ErrTmuxSessionConflict)
			if err == nil {
				t.Fatal("reuse err = nil; want the held-name refusal")
			}
			apitest.AssertDescription(t, err.Error(), apitest.DescHeldNoValidID(apitest.HeldName{
				Name: s.p.TmuxSessionName, SessionID: s.sc.Holder().ID,
				Restore: apitest.ResumeRestore{Outcome: tc.outcome, PriorState: tc.prior, Launch: apitest.LaunchReuse}}), ruhForbid(e, s)...)
			if !s.sc.Placed {
				t.Fatalf("the create on %s never answered %q", s.r.Socket, "duplicate session")
			}
			m := s.sc.Moved
			final, present := e.rrcColumns(t, s.r.ID)
			switch tc.outcome {
			case apitest.RestoreApplied:
				if m.State != store.StatePending || m.LifeNumber != reuseLife+1 || m.ParentID != s.caller || m.LaunchToken == s.r.Token {
					t.Errorf("row at the create {state %v, life %v, parent %v, token %v}; want pending, %d, %s, a new token",
						m.State, m.LifeNumber, m.ParentID, m.LaunchToken, reuseLife+1, s.caller)
				}
				if !reflect.DeepEqual(s.atRelookup, m) {
					t.Errorf("row at the re-lookup =\n  %+v\nwant as at the create (no restore yet)\n  %+v", s.atRelookup, m)
				}
				e.assertRowUnchanged(t, s.r.ID, rstRestored(resumableRow{Before: s.sc.before.cols}, s.sc.before.cols.ParentID))
			case apitest.RestoreRowRemoved:
				if present {
					t.Errorf("row present after the reuse; want it removed")
				}
			default:
				if !present || !reflect.DeepEqual(final, m) {
					t.Errorf("row after the reuse (present %v) =\n  %+v\nwant as at the create (nothing restored)\n  %+v", present, final, m)
				}
			}
			e.assertHolderUntouched(t, s.sc)
			var calls []tmux.Call
			for _, c := range e.rec.SocketCalls()[s.sc.before.calls:] {
				if c.Socket != s.r.Socket {
					t.Errorf("tmux call %v on %s; want every call on %s", c.Call, c.Socket, s.r.Socket)
				}
				calls = append(calls, c.Call)
			}
			if want := []tmux.Call{tmux.CallLookup, tmux.CallCreate, tmux.CallLookup}; !reflect.DeepEqual(calls, want) {
				t.Errorf("tmux calls = %q; want %q", calls, want)
			}
			if (tc.outcome == apitest.RestoreStoreError) != (rutRestoreError(logs, s.r.ID) != "") {
				t.Errorf("restore WARN line in %q; want one only for the store error", logs)
			}
			run := rutHeldRun{sc: s.sc, name: s.p.TmuxSessionName, err: err, logs: logs,
				recs: ptRecords(t, s.sc.before.mark, rutNameHeld, s.r.ID)}
			e.rutAssertNameHeld(t, run, rhtWant{lookup: "gone", sentinel: api.ErrTmuxSessionConflict, holder: true, carries: false},
				tc.rowResult)
			rutAssertRestored(t, s.sc.before.mark, s.r.ID, tc.outcome == apitest.RestoreApplied, "ErrTmuxSessionConflict", logs)
			rutAssertOrder(t, s.sc.before.mark, s.r.ID, rutReused, rutRestored, rutNameHeld)
		})
	}
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
			spec: func(e *killEnv) heldSpec { return heldSpec{Holder: holderCurrent, Created: rlkSettled(e)} },
			want: held("ours", unresponsive, true, true)},
		{name: "current label, still starting",
			spec: func(e *killEnv) heldSpec {
				return heldSpec{Holder: holderCurrent, Created: e.cfg.EffectiveStartingSession() / 2}
			},
			want: held("ours", unresponsive, true, true)},
		{name: "current label, this row's own id",
			spec: func(e *killEnv) heldSpec { return heldSpec{Holder: holderCurrent, Created: rlkSettled(e)} },
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
			age := rlkSettled(e)
			if tc.age != nil {
				age = tc.age(e)
			}
			r := e.seedReusable(t, agentGone, reuseRowSpec{Held: true, Age: age})

			run := e.rutHeld(t, r, tc.spec(e), nil)

			assertOneSentinel(t, run.err, tc.want.sentinel)
			e.rutAssertNameHeld(t, run, tc.want, "restored")
			rutAssertRestored(t, run.sc.before.mark, r.ID, true, ptErrName(run.err), run.logs)
			rutAssertOrder(t, run.sc.before.mark, r.ID, rutReused, rutRestored, rutNameHeld)
		})
	}
}

// TestSpawnReuseProvenanceAfterDuplicateSession (SR-14): the re-lookup writes,
// after the create, only reasons the old-row lookup did not, with verb spawn
// and the recorded name; never adopted. Each reason's rule and
// action is TestResumeProvenanceAfterDuplicateSession's.
func TestSpawnReuseProvenanceAfterDuplicateSession(t *testing.T) {
	t.Parallel()
	holder := func(sc *heldScene) string { return sc.Holder().ID }
	cases := []struct {
		name      string
		spec      heldSpec
		pre, post []disagreeWant
		session   func(sc *heldScene) string // the re-lookup records' tmux_session_id (nil: null)
	}{
		{name: "server_restarted at both lookups is written once", spec: heldSpec{Holder: holderOld, Server: heldServerRestarted},
			pre: []disagreeWant{{reason: "server_restarted", server: "restarted", verdict: "gone", action: "proceeded"}}},
		{name: "name_changed: the row's own session holds the requested name", spec: heldSpec{Holder: holderCurrent},
			post: []disagreeWant{{reason: "name_changed", server: "match", verdict: "ours", action: "restored",
				current: rutRequested}}, session: holder},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			r := e.seedReusable(t, agentGone, reuseRowSpec{Held: true, Age: rlkSettled(e)})
			atCreate := -1
			e.rec.AfterCall(tmux.CallCreate, func(tmuxfix.SocketCall, error) {
				if atCreate < 0 {
					atCreate = len(verbDisagrees(t, "spawn", r.ID))
				}
			})

			run := e.rutHeld(t, r, tc.spec, nil)

			if !run.sc.Placed {
				t.Fatalf("the create never answered %q", "duplicate session")
			}
			if atCreate != len(tc.pre) {
				t.Errorf("records written by the create = %d; want the old-row lookup's %d", atCreate, len(tc.pre))
			}
			recs := verbDisagrees(t, "spawn", r.ID)
			if len(recs) != len(tc.pre)+len(tc.post) {
				t.Fatalf("ad.provenance.disagree records = %d; want %d: %v", len(recs), len(tc.pre)+len(tc.post), recs)
			}
			post := r.killRow
			post.Session = tmuxfix.SeedSession{}
			if tc.session != nil {
				post.Session.ID = tc.session(run.sc)
			}
			for i, want := range append(slices.Clone(tc.pre), tc.post...) {
				row := r.killRow
				if i >= len(tc.pre) {
					row, want.ours = post, post.Session.ID != ""
				}
				assertDisagreeRecord(t, recs[i], row, "spawn", "ad_spawn", want)
				ktrAssertNoForeignContent(t, recs[i], rhtForbid(e, run.sc)...)
			}
			if n := adoptedRecords(t, "spawn", r.ID); n != 0 {
				t.Errorf("adopted records = %d; want none", n)
			}
		})
	}
}
