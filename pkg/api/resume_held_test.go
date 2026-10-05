package api_test

// resume_held_test.go covers resume after its create answers "duplicate
// session" (SR-8.5, SR-3.10, SR-4.2, SR-13.2 path (ii); AC-RES-12, AC-CLS-05,
// AC-SPN-10, AC-PANE-10): per re-lookup outcome the one classified error with
// the restore sentence, the row restored exactly, only lookup, create and
// re-lookup, the holder untouched, and the re-issue. Fixture:
// resume_held_fixture_test.go.

import (
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// rhdCase is one "duplicate session" resume: the arrangement, whether the
// row ended inside the stopping window (else past it and the bound) and the
// placed sessions are past the bound (else created at the create), the
// error, whether its description names the holder's $N, and its case.
type rhdCase struct {
	name          string
	spec          heldSpec
	stopping, old bool
	want          error
	named         bool
	desc          func(e *killEnv, sc *heldScene, p apitest.HeldName) apitest.DescCase
}

// rhdRun resumes a row of prior state whose create meets tc's arrangement and
// checks the one sentinel, the description, the exact restore, the three
// calls and the untouched holder.
func rhdRun(t *testing.T, tc rhdCase, prior string) {
	t.Helper()
	e := newKillEnv(t)
	parent := e.seedRow(t, killRowSpec{State: store.StateEnded, Agent: agentGone, NoSession: true}).ID
	t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", parent)
	age := rlkSettled(e)
	if tc.stopping {
		age = e.cfg.EffectiveStoppingWindow() / 2
	}
	spec := e.heldResumableSpec(age, agentGone)
	spec.State = prior
	r := e.seedResumableRow(t, spec)
	if tc.old {
		tc.spec.Created = rlkSettled(e)
	}
	sc := e.arrangeHeld(t, r, tc.spec)

	_, err := e.resume(r.ID)

	assertOneSentinel(t, err, tc.want)
	if err == nil {
		t.Fatal("resume err = nil; want the held-name refusal")
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
}

// rhdForbid is what no description may show: the row's token, this and the
// other store's ids, and every placed session's label token and other id;
// with no holder named, the holders' tmux ids.
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

// rhdStarting is the starting-session case parameters for sc's row with its session.
func rhdStarting(e *killEnv, sc *heldScene) apitest.StartingSession {
	return rlkStarting(e, sc.r, false)
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
)

// rhdCases is every re-lookup outcome after "duplicate session".
func rhdCases() []rhdCase {
	conflict, unresponsive, unavailable := api.ErrTmuxSessionConflict, api.ErrTmuxUnresponsive, api.ErrTmuxNotAvailable
	relookup := func(s tmuxfix.Script) heldSpec { return heldSpec{Holder: holderNone, Relookup: s} }
	const line = "held: unexpected reply"
	return []rhdCase{
		{name: "old label", spec: heldSpec{Holder: holderOld}, want: conflict, named: true, desc: rhdLeftover},
		{name: "foreign label", spec: heldSpec{Holder: holderForeign}, want: conflict, named: true, desc: rhdDifferentID},
		{name: "another store's label, another id", spec: heldSpec{Holder: holderOtherStore}, want: conflict, named: true,
			desc: rhdOtherStore},
		{name: "another store's label, this id and token", spec: heldSpec{Holder: holderOtherStoreOwn}, want: conflict,
			named: true, desc: rhdOtherStore},
		{name: "no label", spec: heldSpec{Holder: holderNone}, want: conflict, named: true, desc: rhdNoValidID},
		{name: "malformed label", spec: heldSpec{Holder: holderMalformed}, want: conflict, named: true, desc: rhdNoValidID},
		{name: "conflicting labels", spec: heldSpec{Holder: holderConflicting}, want: conflict, named: true,
			desc: rhdOver(func(_ *killEnv, sc *heldScene) apitest.DescCase {
				return apitest.DescConflictingLabels(apitest.ConflictingLabels{InstanceID: sc.r.ID, Scope: true, NothingWasDone: true})
			})},
		{name: "ambiguous holder", spec: heldSpec{Holder: holderAmbiguous}, want: unresponsive,
			desc: func(_ *killEnv, _ *heldScene, p apitest.HeldName) apitest.DescCase {
				return apitest.DescHeldAmbiguous(p)
			}},
		{name: "vanished", spec: heldSpec{Holder: holderVanished}, want: api.ErrTmuxSessionCreate,
			desc: rhdOver(func(_ *killEnv, sc *heldScene) apitest.DescCase {
				return apitest.DescSessionCreateFailed(apitest.SessionCreateFailed{Name: sc.r.Name, Duplicate: true})
			})},
		{name: "unreadable: timeout", spec: relookup(tmuxfix.Script{Failure: tmux.FailTimeout}), want: unresponsive,
			desc: rhdOver(func(*killEnv, *heldScene) apitest.DescCase { return apitest.DescCallTimeout(tmux.CallLookup, boundQ) })},
		{name: "unreadable: unrecognised reply", want: unresponsive,
			spec: relookup(tmuxfix.Script{Failure: tmux.FailUnrecognized, FirstLine: line, ExitStatus: 1, HadStdout: true}),
			desc: rhdOver(func(*killEnv, *heldScene) apitest.DescCase {
				return apitest.DescUnrecognisedReply(tmux.CallLookup, line)
			})},
		{name: "different server", spec: heldSpec{Holder: holderNone, Server: heldServerRebound}, want: unavailable, named: true,
			desc: rhdOver(func(_ *killEnv, sc *heldScene) apitest.DescCase { return apitest.DescDifferentServer(sc.r.ID) })},
		{name: "tmux unavailable: missing binary", spec: relookup(tmuxfix.Script{Failure: tmux.FailUnavailable}), want: unavailable,
			desc: rhdOver(func(*killEnv, *heldScene) apitest.DescCase { return apitest.DescTmuxNotRun() })},
		{name: "tmux unavailable: socket permission", spec: relookup(tmuxfix.Script{Failure: tmux.FailSocketDenied}),
			want: unavailable,
			desc: rhdOver(func(_ *killEnv, sc *heldScene) apitest.DescCase { return apitest.DescSocketPermission(sc.r.Socket) })},
		// The current label: the examined ended_at (cleared by the move) and the holder's creation time decide.
		{name: "current label, inside the stopping window", spec: heldSpec{Holder: holderCurrent}, stopping: true, old: true,
			want: unresponsive, desc: rhdOver(func(e *killEnv, sc *heldScene) apitest.DescCase {
				return apitest.DescStillStopping(rhdStarting(e, sc))
			})},
		{name: "current label, young session", spec: heldSpec{Holder: holderCurrent}, want: unresponsive,
			desc: rhdOver(func(e *killEnv, sc *heldScene) apitest.DescCase { return apitest.DescStillStarting(rhdStarting(e, sc)) })},
		{name: "current label, session past the bound", spec: heldSpec{Holder: holderCurrent}, old: true, want: conflict,
			desc: rhdOver(func(e *killEnv, sc *heldScene) apitest.DescCase { return apitest.DescOwnOldSession(rhdStarting(e, sc)) })},
		// The holder's class decides, not the verdict: the row's own session runs under another name.
		{name: "ours renamed, old holder", spec: heldSpec{Holder: holderOld, OursRenamed: "renamed-held"}, want: conflict,
			named: true, desc: rhdLeftover},
		{name: "ours renamed, foreign holder", spec: heldSpec{Holder: holderForeign, OursRenamed: "renamed-held"},
			want: conflict, named: true, desc: rhdDifferentID},
	}
}

// rhdNotApplied is each restore result other than applied after "duplicate session".
var rhdNotApplied = []struct {
	name    string
	outcome apitest.RestoreOutcome
}{{"changed", apitest.RestoreRowChanged}, {"removed", apitest.RestoreRowRemoved}, {"store error", apitest.RestoreStoreError}}

// rhdSpoilRestore makes the restore after id's "duplicate session" give outcome: as the create returns, a
// write of parent other (changed) or the row's removal (removed); else failRestore's store error. Call it after arrangeHeld.
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

// TestResumeHeldRetryFollowsRestore (b.gu6): an ErrTmuxUnresponsive after "duplicate session" (unreadable,
// ambiguous, still stopping or starting) whose restore did not apply ends with the retry sentence its result picks.
func TestResumeHeldRetryFollowsRestore(t *testing.T) {
	for _, tc := range rhdCases() {
		if tc.want != api.ErrTmuxUnresponsive {
			continue
		}
		for _, o := range rhdNotApplied {
			t.Run(tc.name+"/"+o.name, func(t *testing.T) {
				e := newKillEnv(t)
				other := adviceOtherRow(t, e)
				age := rlkSettled(e)
				if tc.stopping {
					age = e.cfg.EffectiveStoppingWindow() / 2
				}
				r := e.seedHeldResumable(t, age, agentGone)
				if tc.old {
					tc.spec.Created = rlkSettled(e)
				}
				sc := e.arrangeHeld(t, r, tc.spec)
				w := &hookedResumeStore{st: e.st}
				rhdSpoilRestore(t, e, r.ID, other, o.outcome, func() { w.failRestore(nil) })

				_, err := e.resumeWith(w, r.ID)

				assertOneSentinel(t, err, api.ErrTmuxUnresponsive)
				if err == nil {
					t.Fatal("resume err = nil; want the held-name refusal")
				}
				p := apitest.HeldName{Name: r.Name, Restore: apitest.ResumeRestore{Outcome: o.outcome}}
				apitest.AssertDescription(t, err.Error(), tc.desc(e, sc, p), rhdForbid(e, sc)...)
			})
		}
	}
}

// TestResumeHeldRelookupOutcomes: per re-lookup outcome and prior state, one
// classified error with the applied restore sentence, the row restored, the holder untouched.
func TestResumeHeldRelookupOutcomes(t *testing.T) {
	for _, tc := range rhdCases() {
		for _, prior := range []string{store.StateEnded, store.StateMissing} {
			t.Run(tc.name+"/"+prior, func(t *testing.T) { rhdRun(t, tc, prior) })
		}
	}
}

// TestResumeHeldReissue (AC-RES-12, AC-PANE-10): after the restored refusal a
// re-issue is refused before its move while the holder runs; once it is gone one launches.
func TestResumeHeldReissue(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec heldSpec
	}{
		{"own old session", heldSpec{Holder: holderCurrent}},
		{"no valid label", heldSpec{Holder: holderNone}},
		{"another row's session", heldSpec{Holder: holderForeign}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedHeldResumable(t, rlkSettled(e), agentGone)
			tc.spec.Created = rlkSettled(e)
			sc := e.arrangeHeld(t, r, tc.spec)
			if _, err := e.resume(r.ID); err == nil {
				t.Fatal("first resume err = nil; want the held-name refusal")
			}
			e.assertHeldRestored(t, sc)
			r.Trust.reset(t)
			before := e.snapshotResume(t, r)

			_, err := e.resume(r.ID)

			assertOneSentinel(t, err, api.ErrTmuxSessionConflict)
			e.assertResumeWroteNothing(t, before)

			e.removeHolders(t, sc)
			before = e.snapshotResume(t, r)

			_, err = e.resume(r.ID)

			e.rlkAssertLaunched(t, before, err)
		})
	}
}
