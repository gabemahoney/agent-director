package api_test

// resume_held_test.go covers resume after its create answers "duplicate
// session" (SR-8.5, SR-3.10, SR-4.2, SR-13.2 path (ii), SR-14, SR-15;
// AC-RES-12, AC-CLS-05, AC-SPN-10, AC-PANE-10) per re-lookup outcome and per
// restore result: the error and its sentences, the row, the calls, the holder
// and the trail records. A re-issue after ErrTmuxUnresponsive is
// advice_follow_resume_launch_test.go's B10; after ErrTmuxSessionConflict,
// rhdRun's exact restore plus TestAdviceFollow_HO3_NameHolderClears.
// Fail-open is TestResumeTrailFailOpen.

import (
	"reflect"
	"slices"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// rhdCase is one "duplicate session" resume: the arrangement, whether the row
// ended inside the stopping window and the placed sessions are past the
// bound, whether the row records no session of its latest launch (no server
// or pane identity), the error, whether its description names the holder's
// $N, and its ad.launch.name_held fields (rec; a zero lookup leaves the record
// unchecked).
type rhdCase struct {
	name          string
	spec          heldSpec
	stopping, old bool
	noSession     bool
	want          error
	named         bool
	desc          func(e *killEnv, sc *heldScene, p apitest.HeldName) apitest.DescCase
	rec           rhtWant
}

// seed seeds tc's resumable row in state prior (ended age before heldInstant).
func (tc rhdCase) seed(t *testing.T, e *killEnv, prior string) resumeRow {
	t.Helper()
	age := rlkSettled(e)
	if tc.stopping {
		age = e.cfg.EffectiveStoppingWindow() / 2
	}
	spec := e.heldResumableSpec(age, agentGone)
	spec.State, spec.NoServerIdentity, spec.NoPane = prior, tc.noSession, tc.noSession
	return e.seedResumableRow(t, spec)
}

// rhdRun resumes a row of prior state whose create meets tc's arrangement and
// checks the one sentinel, the description, the exact restore, the three
// calls charging 2Q + C, the untouched holder and the trail.
func rhdRun(t *testing.T, tc rhdCase, prior string) {
	t.Helper()
	e := newKillEnv(t)
	parent := e.seedRow(t, killRowSpec{State: store.StateEnded, Agent: agentGone, NoSession: true}).ID
	t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", parent)
	r := tc.seed(t, e, prior)
	if tc.old {
		tc.spec.Created = rlkSettled(e)
	}
	sc := e.arrangeHeld(t, r, tc.spec)
	start := e.clock.Now()

	_, err := e.resume(r.ID)

	assertOneSentinel(t, err, tc.want)
	if err == nil {
		t.Fatal("resume err = nil; want the held-name refusal")
	}
	if elapsed := e.clock.Now().Sub(start); elapsed != resumeHeldAt {
		t.Errorf("virtual time = %v; want SR-13.2 path (ii)'s 2Q + C = %v", elapsed, resumeHeldAt)
	}
	p := apitest.HeldName{Name: r.Name, Restore: apitest.ResumeRestore{Outcome: apitest.RestoreApplied, PriorState: prior}}
	if tc.named {
		p.SessionID = sc.Holder().ID
	}
	apitest.AssertDescription(t, err.Error(), tc.desc(e, sc, p), rhdForbid(e, sc)...)
	e.assertHeldRestored(t, sc)
	if sc.Moved.ParentID != any(parent) {
		t.Errorf("parent_id at the move = %v; want %q, which the restore keeps", sc.Moved.ParentID, parent)
	}
	e.assertHolderUntouched(t, sc)
	var calls []tmux.Call
	for _, c := range e.rec.SocketCalls()[sc.before.calls:] {
		if c.Socket != r.Socket {
			t.Errorf("tmux call %v on %s; want every call on %s", c.Call, c.Socket, r.Socket)
		}
		calls = append(calls, c.Call)
	}
	if want := []tmux.Call{tmux.CallLookup, tmux.CallCreate, tmux.CallLookup}; !reflect.DeepEqual(calls, want) {
		t.Errorf("tmux calls = %q; want %q", calls, want)
	}
	assertResumeEvents(t, sc.before.mark, r.ID, "ad.resume.moved_to_pending", "ad.resume.restored", "ad.launch.name_held")
	rstAssertRestoredTrail(t, r.ID, true, ptErrName(tc.want), nil)
	if tc.rec.lookup != "" {
		w := tc.rec
		w.sentinel = tc.want
		e.rhtAssertRecord(t, rhtRun{sc: sc, err: err, recs: ptRecords(t, sc.before.mark, "ad.launch.name_held", r.ID)}, w, "restored", nil)
	}
}

// rhdForbid is what no description may show: tokens, store ids, other ids.
func rhdForbid(e *killEnv, sc *heldScene) []string {
	out := []string{sc.r.Token, e.storeID, apitest.OtherStoreID(e.storeID)}
	for _, s := range append(append([]tmuxfix.SeedSession(nil), sc.Holders...), sc.Ours) {
		if s.Label.Token != "" {
			out = append(out, s.Label.Token)
		}
		if id := s.Label.InstanceID; id != "" && id != sc.r.ID {
			out = append(out, id)
		}
	}
	return out
}

// rhdHolders is the holders' names and tmux ids as the Leftover case lists them.
func rhdHolders(sc *heldScene) []apitest.DescSession {
	var out []apitest.DescSession
	for _, s := range sc.Holders {
		out = append(out, apitest.DescSession{Name: s.Name, ID: s.ID})
	}
	return out
}

// The description builders the table shares.
var (
	rhdLeftover = func(_ *killEnv, sc *heldScene, p apitest.HeldName) apitest.DescCase {
		return apitest.DescPreLaunchLeftover(sc.r.ID, rhdHolders(sc)).AfterHeldName(p)
	}
	rhdDifferentID = func(_ *killEnv, _ *heldScene, p apitest.HeldName) apitest.DescCase {
		return apitest.DescHeldDifferentID(p)
	}
	rhdOtherStore = func(e *killEnv, _ *heldScene, p apitest.HeldName) apitest.DescCase {
		return apitest.DescHeldOtherStore(p, e.storeID)
	}
	rhdNoValidID = func(_ *killEnv, _ *heldScene, p apitest.HeldName) apitest.DescCase {
		return apitest.DescHeldNoValidID(p)
	}
	rhdOver = func(c func(*killEnv, *heldScene) apitest.DescCase) func(*killEnv, *heldScene, apitest.HeldName) apitest.DescCase {
		return func(e *killEnv, sc *heldScene, p apitest.HeldName) apitest.DescCase { return c(e, sc).AfterHeldName(p) }
	}
	rhdDifferentServer = rhdOver(func(_ *killEnv, sc *heldScene) apitest.DescCase { return apitest.DescDifferentServer(sc.r.ID) })
	rhdAbandoned       = func(past bool) func(*killEnv, *heldScene, apitest.HeldName) apitest.DescCase {
		return rhdOver(func(e *killEnv, sc *heldScene) apitest.DescCase {
			return apitest.DescAbandonedLaunch(apitest.AbandonedLaunch{InstanceID: sc.r.ID, Sessions: rhdHolders(sc),
				Bound: e.cfg.EffectiveStartingSession(), PastBound: past, SessionID: true})
		})
	}
)

// rhtHeld and rhtNone are a record's fields with a holder identified (nil: null) or none.
func rhtHeld(lookup string, carries, current any) rhtWant {
	return rhtWant{lookup: lookup, holder: true, carries: carries, current: current}
}
func rhtNone(lookup string) rhtWant { return rhtWant{lookup: lookup} }

// rhdCases is every re-lookup outcome after "duplicate session".
func rhdCases() []rhdCase {
	conflict, unresponsive, unavailable := api.ErrTmuxSessionConflict, api.ErrTmuxUnresponsive, api.ErrTmuxNotAvailable
	relookup := func(s tmuxfix.Script) heldSpec { return heldSpec{Holder: holderNone, Relookup: s} }
	const line = "held: unexpected reply"
	gone, ours := rhtHeld("gone", false, nil), rhtHeld("ours", true, true)
	return []rhdCase{
		{name: "old label", spec: heldSpec{Holder: holderOld}, want: conflict, named: true, desc: rhdLeftover,
			rec: rhtHeld("leftover", true, false)},
		{name: "foreign label", spec: heldSpec{Holder: holderForeign}, want: conflict, named: true, desc: rhdDifferentID, rec: gone},
		{name: "another store's label, another id", spec: heldSpec{Holder: holderOtherStore}, want: conflict, named: true,
			desc: rhdOtherStore, rec: gone},
		{name: "another store's label, this id and token", spec: heldSpec{Holder: holderOtherStoreOwn}, want: conflict,
			named: true, desc: rhdOtherStore, rec: gone},
		{name: "no label", spec: heldSpec{Holder: holderNone}, want: conflict, named: true, desc: rhdNoValidID, rec: gone},
		{name: "malformed label", spec: heldSpec{Holder: holderMalformed}, want: conflict, named: true, desc: rhdNoValidID, rec: gone},
		{name: "conflicting labels", spec: heldSpec{Holder: holderConflicting}, want: conflict, named: true,
			desc: rhdOver(func(_ *killEnv, sc *heldScene) apitest.DescCase {
				return apitest.DescConflictingLabels(apitest.ConflictingLabels{InstanceID: sc.r.ID, Scope: true, NothingWasDone: true})
			}), rec: rhtHeld("provenance_conflict", nil, nil)},
		{name: "ambiguous holder", spec: heldSpec{Holder: holderAmbiguous}, want: unresponsive,
			desc: func(_ *killEnv, _ *heldScene, p apitest.HeldName) apitest.DescCase {
				return apitest.DescHeldAmbiguous(p)
			}, rec: rhtNone("gone")},
		{name: "vanished", spec: heldSpec{Holder: holderVanished}, want: api.ErrTmuxSessionCreate,
			desc: rhdOver(func(_ *killEnv, sc *heldScene) apitest.DescCase {
				return apitest.DescSessionCreateFailed(apitest.SessionCreateFailed{Name: sc.r.Name, Duplicate: true})
			}), rec: rhtNone("gone")},
		{name: "unreadable: timeout", spec: relookup(tmuxfix.Script{Failure: tmux.FailTimeout}), want: unresponsive,
			desc: rhdOver(func(*killEnv, *heldScene) apitest.DescCase { return apitest.DescCallTimeout(tmux.CallLookup, boundQ) }),
			rec:  rhtNone("cant_tell")},
		{name: "unreadable: unrecognised reply", want: unresponsive,
			spec: relookup(tmuxfix.Script{Failure: tmux.FailUnrecognized, FirstLine: line, ExitStatus: 1, HadStdout: true}),
			desc: rhdOver(func(*killEnv, *heldScene) apitest.DescCase {
				return apitest.DescUnrecognisedReply(tmux.CallLookup, line)
			}), rec: rhtNone("cant_tell")},
		{name: "different server", spec: heldSpec{Holder: holderNone, Server: heldServerRebound}, want: unavailable, named: true,
			desc: rhdDifferentServer, rec: rhtHeld("different_server", nil, nil)},
		{name: "tmux unavailable: missing binary", spec: relookup(tmuxfix.Script{Failure: tmux.FailUnavailable}), want: unavailable,
			desc: rhdOver(func(*killEnv, *heldScene) apitest.DescCase { return apitest.DescTmuxNotRun() }), rec: rhtNone("tmux_unavailable")},
		{name: "tmux unavailable: socket permission", spec: relookup(tmuxfix.Script{Failure: tmux.FailSocketDenied}),
			want: unavailable, rec: rhtNone("tmux_unavailable"),
			desc: rhdOver(func(_ *killEnv, sc *heldScene) apitest.DescCase { return apitest.DescSocketPermission(sc.r.Socket) })},
		// The current label: the examined ended_at (cleared by the move) and the holder's creation time decide.
		{name: "current label, inside the stopping window", spec: heldSpec{Holder: holderCurrent}, stopping: true, old: true,
			want: unresponsive, rec: ours, desc: rhdOver(func(e *killEnv, sc *heldScene) apitest.DescCase {
				return apitest.DescStillStopping(rlkStarting(e, sc.r, false))
			})},
		{name: "current label, young session", spec: heldSpec{Holder: holderCurrent}, want: unresponsive, rec: ours,
			desc: rhdOver(func(e *killEnv, sc *heldScene) apitest.DescCase {
				return apitest.DescStillStarting(rlkStarting(e, sc.r, false))
			})},
		{name: "current label, session past the bound", spec: heldSpec{Holder: holderCurrent}, old: true, want: conflict, rec: ours,
			desc: rhdOver(func(e *killEnv, sc *heldScene) apitest.DescCase {
				return apitest.DescOwnOldSession(rlkStarting(e, sc.r, false))
			})},
		// The holder's class decides, not the verdict: the row's own session runs under another name.
		{name: "ours renamed, old holder", spec: heldSpec{Holder: holderOld, OursRenamed: "renamed-held"}, want: conflict,
			named: true, desc: rhdLeftover},
		{name: "ours renamed, foreign holder", spec: heldSpec{Holder: holderForeign, OursRenamed: "renamed-held"},
			want: conflict, named: true, desc: rhdDifferentID},
		// The row records no session of its latest launch: an old holder is this id's own abandoned launch (b.1n6).
		{name: "old label, no session recorded, young holder", spec: heldSpec{Holder: holderOld}, noSession: true,
			want: unresponsive, named: true, desc: rhdAbandoned(false), rec: rhtHeld("leftover", true, false)},
		{name: "old label, no session recorded, holder past the bound", spec: heldSpec{Holder: holderOld}, noSession: true,
			old: true, want: conflict, named: true, desc: rhdAbandoned(true), rec: rhtHeld("leftover", true, false)},
	}
}

// rhdNotApplied is each restore result other than applied after "duplicate session".
var rhdNotApplied = []struct {
	name    string
	outcome apitest.RestoreOutcome
}{{"changed", apitest.RestoreRowChanged}, {"removed", apitest.RestoreRowRemoved}, {"store error", apitest.RestoreStoreError}}

// rhdSpoilRestore makes the restore give outcome: a write (changed) or the row's removal as the create
// returns, else failRestore's store error. Call it after arrangeHeld.
func rhdSpoilRestore(t *testing.T, e *killEnv, id, other string, outcome apitest.RestoreOutcome, failRestore func()) {
	t.Helper()
	var write func() error
	switch outcome {
	case apitest.RestoreRowChanged:
		write = func() error { return e.st.SetParentID(id, other) }
	case apitest.RestoreRowRemoved:
		write = func() error { return e.st.DeleteSpawn(id) }
	default:
		failRestore()
		return
	}
	adviceOnceAfter(e.rec, tmux.CallCreate, func() {
		if err := write(); err != nil {
			t.Errorf("write to %s as the create returned: %v", id, err)
		}
	})
}

// TestResumeHeldRelookupOutcomes: per re-lookup outcome and prior state (rhdRun).
func TestResumeHeldRelookupOutcomes(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID with t.Setenv.
	cases := append(rhdCases(), rhdCase{name: "different server, no holder", want: api.ErrTmuxNotAvailable,
		spec: heldSpec{Holder: holderVanished, Server: heldServerRebound}, desc: rhdDifferentServer, rec: rhtNone("different_server")})
	for _, tc := range cases {
		for _, prior := range []string{store.StateEnded, store.StateMissing} {
			t.Run(tc.name+"/"+prior, func(t *testing.T) { rhdRun(t, tc, prior) })
		}
	}
}

// TestResumeHeldRetryFollowsRestore (b.gu6): the restore's result picks the
// description's row sentence and, for ErrTmuxUnresponsive, its retry
// sentence; ad.resume.restored and ad.launch.name_held carry it; the error's
// class stays the holder's. A hook before the restore is ignored (SR-22.9).
func TestResumeHeldRetryFollowsRestore(t *testing.T) {
	t.Parallel()
	hooksIgnored := struct {
		name    string
		outcome apitest.RestoreOutcome
	}{"applied, hooks before the restore ignored", apitest.RestoreApplied}
	for _, tc := range rhdCases() {
		outcomes := rhdNotApplied
		switch {
		case tc.name == "no label":
			outcomes = append([]struct {
				name    string
				outcome apitest.RestoreOutcome
			}{hooksIgnored}, outcomes...)
		case tc.want != api.ErrTmuxUnresponsive:
			continue
		}
		for _, o := range outcomes {
			t.Run(tc.name+"/"+o.name, func(t *testing.T) {
				t.Parallel()
				e := newKillEnv(t)
				other := adviceOtherRow(t, e)
				r := tc.seed(t, e, store.StateEnded)
				// A copy: tc is shared with the sibling parallel subtests of each restore outcome.
				spec := tc.spec
				if tc.old {
					spec.Created = rlkSettled(e)
				}
				sc := e.arrangeHeld(t, r, spec)
				w := &hookedResumeStore{st: e.st}
				if o.outcome == apitest.RestoreApplied {
					w.afterMove(func() {
						for _, ev := range []string{"Stop", "SessionStart"} {
							got := apitest.ApplyAgentHook(t, e.dbPath, r.ID, ev, r.Spawn.ClaudeSessionID, apitest.HookTranscript(r.JSONLPath, true))
							if got != (store.HookApplied{Reason: store.HookReasonNoPaneRecorded}) {
								t.Errorf("%s before the restore = %+v; want ignored, %s", ev, got, store.HookReasonNoPaneRecorded)
							}
						}
					})
				} else {
					rhdSpoilRestore(t, e, r.ID, other, o.outcome, func() { w.failRestore(nil) })
				}

				_, err := e.resumeWith(w, r.ID)

				assertOneSentinel(t, err, tc.want)
				if err == nil {
					t.Fatal("resume err = nil; want the held-name refusal")
				}
				p := apitest.HeldName{Name: r.Name, Restore: apitest.ResumeRestore{Outcome: o.outcome}}
				rowResult, storeErr := "left_changed", any(nil)
				switch o.outcome {
				case apitest.RestoreApplied:
					p.Restore.PriorState, rowResult = store.StateEnded, "restored"
					e.assertHeldRestored(t, sc)
				case apitest.RestoreStoreError:
					rowResult, storeErr = "still_pending", errInjectedStore.Error()
				}
				if tc.named {
					p.SessionID = sc.Holder().ID
				}
				apitest.AssertDescription(t, err.Error(), tc.desc(e, sc, p), rhdForbid(e, sc)...)
				w2 := tc.rec
				w2.sentinel = tc.want
				run := rhtRun{sc: sc, err: err, recs: ptRecords(t, sc.before.mark, "ad.launch.name_held", r.ID)}
				e.rhtAssertRecord(t, run, w2, rowResult, storeErr)
				rstAssertRestoredTrail(t, r.ID, o.outcome == apitest.RestoreApplied, ptErrName(tc.want), storeErr)
				assertResumeEvents(t, sc.before.mark, r.ID, "ad.resume.moved_to_pending", "ad.resume.restored", "ad.launch.name_held")
			})
		}
	}
}

// rhtWant is one record's per-outcome fields (see rhtHeld, rhtNone).
type rhtWant struct {
	lookup           string
	sentinel         error
	holder           bool
	carries, current any
}

// rhtRun is one held-name resume: its scene, error and name_held records.
type rhtRun struct {
	sc   *heldScene
	err  error
	recs []map[string]any
}

// rhtResume arranges spec on r and resumes it through w (nil: a plain one);
// atCreate runs as the create returns, after arrangeHeld's placement.
func (e *killEnv) rhtResume(t *testing.T, r resumeRow, spec heldSpec, w *hookedResumeStore, atCreate func()) rhtRun {
	t.Helper()
	if w == nil {
		w = &hookedResumeStore{st: e.st}
	}
	sc := e.arrangeHeld(t, r, spec)
	if atCreate != nil {
		e.rec.AfterCall(tmux.CallCreate, func(tmuxfix.SocketCall, error) { atCreate() })
	}
	_, err := e.resumeWith(w, r.ID)
	return rhtRun{sc: sc, err: err, recs: ptRecords(t, sc.before.mark, "ad.launch.name_held", r.ID)}
}

// rhtAssertRecord checks run wrote exactly one record: every SR-14 field, the
// exact key set, and no label content, token, other id or session environment.
func (e *killEnv) rhtAssertRecord(t *testing.T, run rhtRun, w rhtWant, rowResult string, storeError any) {
	t.Helper()
	if len(run.recs) != 1 {
		t.Fatalf("ad.launch.name_held records = %d; want 1: %v", len(run.recs), run.recs)
	}
	rec, r := run.recs[0], run.sc.r
	want := map[string]any{
		"source": "ad_resume", "claude_instance_id": r.ID, "launch": "resume", "tmux_session_name": r.Name,
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
	for k, v := range want {
		if got, ok := rec[k]; !ok || got != v {
			t.Errorf("name_held[%q] = %v (present %t); want %v", k, got, ok, v)
		}
	}
	keys := make([]string, 0, len(rec))
	for k := range rec {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	wantKeys := slices.Clone(ptKeys)
	slices.Sort(wantKeys)
	if !slices.Equal(keys, wantKeys) {
		t.Errorf("record keys = %q; want %q", keys, wantKeys)
	}
	ktrAssertNoForeignContent(t, rec, rhtForbid(e, run.sc)...)
}

// rhtForbid is what no record of sc's resume may carry: tokens, another
// store's id, label values, other ids and the row's config directory.
func rhtForbid(e *killEnv, sc *heldScene) []string {
	out := []string{sc.r.Token, tmuxfix.OtherToken, apitest.OtherStoreID(e.storeID), sc.r.Trust.dir}
	if tok, ok := sc.Moved.LaunchToken.(string); ok {
		out = append(out, tok)
	}
	for _, s := range append(slices.Clone(sc.Holders), sc.Ours) {
		if l := s.Label; l.Kind == tmux.LabelValid {
			out = append(out, l.Token, tmuxfix.LabelValue(l.Token, s.ID, l.InstanceID, l.StoreID))
			if l.InstanceID != sc.r.ID {
				out = append(out, l.InstanceID)
			}
		}
	}
	return out
}

// holderOnly is the heldSpec placing only k's holder.
func holderOnly(k holderKind) func(*killEnv) heldSpec {
	return func(*killEnv) heldSpec { return heldSpec{Holder: k} }
}
