package api_test

// spawn_reuse_held_test.go covers reuse after its create answers "duplicate
// session" (SR-10.4, SR-8.5, SR-9.4, SR-4.2; AC-REUSE-07, AC-SPN-10): one
// re-lookup of the requested name against the row as examined before the
// reset, the restore, the classified error quoting the requested name, the
// holder untouched, and the re-issue. Fixtures: spawn_reuse_fixture_test.go,
// resume_held_fixture_test.go; the outcome table is resume's (rhdCases).

import (
	"reflect"
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

// ruhAssertOrder fails unless the reset came before the create (the row then
// pending in its new life with the caller's parent) and the restore after the
// re-lookup (the row unchanged until it returned).
func (e *killEnv) ruhAssertOrder(t *testing.T, s *ruhScene) {
	t.Helper()
	if !s.sc.Placed {
		t.Fatalf("the create on %s never answered %q", s.r.Socket, "duplicate session")
	}
	m := s.sc.Moved
	if m.State != store.StatePending || m.LifeNumber != reuseLife+1 || m.ParentID != s.caller || m.LaunchToken == s.r.Token {
		t.Errorf("row at the create {state %v, life %v, parent %v, token %v}; want pending, %d, %s, a new token",
			m.State, m.LifeNumber, m.ParentID, m.LaunchToken, reuseLife+1, s.caller)
	}
	if !reflect.DeepEqual(s.atRelookup, m) {
		t.Errorf("row at the re-lookup =\n  %+v\nwant as at the create (no restore yet)\n  %+v", s.atRelookup, m)
	}
}

// ruhAssertCalls fails unless, since arrangeHeld, the calls were the lookup,
// the create and the one re-lookup, all on the row's socket.
func (e *killEnv) ruhAssertCalls(t *testing.T, s *ruhScene) {
	t.Helper()
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
}

// ruhRestored is s's row restored byte for byte: every column as seeded,
// its own parent, row_version two past (reset and restore).
func ruhRestored(s *ruhScene) apitest.SpawnColumns {
	return rstRestored(resumableRow{Before: s.sc.before.cols}, s.sc.before.cols.ParentID)
}

// ruhRun reuses a row of state prior whose create meets tc's arrangement and
// checks the one sentinel, the description, the order, the exact restore,
// the three calls and the untouched holder.
func ruhRun(t *testing.T, tc rhdCase, prior string) {
	t.Helper()
	e := newKillEnv(t)
	age := rlkSettled(e)
	if tc.stopping {
		age = e.cfg.EffectiveStoppingWindow() / 2
	}
	if tc.old {
		tc.spec.Created = rlkSettled(e)
	}
	s := e.ruhArrange(t, prior, age, tc.spec)

	_, _, err := e.reuse(t, s.p)

	assertOneSentinel(t, err, tc.want)
	if err == nil {
		t.Fatal("reuse err = nil; want the held-name refusal")
	}
	p := apitest.HeldName{Name: s.p.TmuxSessionName,
		Restore: apitest.ResumeRestore{Outcome: apitest.RestoreApplied, PriorState: prior, Launch: apitest.LaunchReuse}}
	if tc.named {
		p.SessionID = s.sc.Holder().ID
	}
	apitest.AssertDescription(t, err.Error(), tc.desc(e, s.sc, p), ruhForbid(e, s)...)
	e.ruhAssertOrder(t, s)
	e.assertRowUnchanged(t, s.r.ID, ruhRestored(s))
	e.assertHolderUntouched(t, s.sc)
	e.ruhAssertCalls(t, s)
}

// TestSpawnReuseHeldRelookupOutcomes: per re-lookup outcome and prior state,
// one classified error quoting the requested name, the row restored, the holder untouched.
func TestSpawnReuseHeldRelookupOutcomes(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID with t.Setenv.
	for _, tc := range rhdCases() {
		for _, prior := range []string{store.StateEnded, store.StateMissing} {
			t.Run(tc.name+"/"+prior, func(t *testing.T) { ruhRun(t, tc, prior) })
		}
	}
}

// TestSpawnReuseHeldRetryFollowsRestore (b.gu6): an ErrTmuxUnresponsive after reuse's "duplicate session" (unreadable,
// ambiguous, still stopping or starting) whose restore did not apply ends with the retry sentence its result picks.
func TestSpawnReuseHeldRetryFollowsRestore(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID with t.Setenv.
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
				if tc.old {
					tc.spec.Created = rlkSettled(e)
				}
				s := e.ruhArrange(t, store.StateEnded, age, tc.spec)
				rhdSpoilRestore(t, e, s.r.ID, other, o.outcome, func() {
					storefix.InjectWriteFailure(t, e.dbPath, storefix.WriteFailReuseRestore, s.r.ID)
				})

				_, _, err := e.reuse(t, s.p)

				assertOneSentinel(t, err, api.ErrTmuxUnresponsive)
				if err == nil {
					t.Fatal("reuse err = nil; want the held-name refusal")
				}
				p := apitest.HeldName{Name: s.p.TmuxSessionName,
					Restore: apitest.ResumeRestore{Outcome: o.outcome, Launch: apitest.LaunchReuse}}
				apitest.AssertDescription(t, err.Error(), tc.desc(e, s.sc, p), ruhForbid(e, s)...)
			})
		}
	}
}

// TestSpawnReuseHeldRowResult: the description's row sentence follows the
// restore: changed after the reset, removed at the create, or a failed write.
func TestSpawnReuseHeldRowResult(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID with t.Setenv.
	cases := []struct {
		name    string
		arrange func(t *testing.T, e *killEnv, s *ruhScene, rs *hookedReuseStore)
		outcome apitest.RestoreOutcome
	}{
		{name: "parent id written after the reset", outcome: apitest.RestoreRowChanged,
			arrange: func(t *testing.T, e *killEnv, s *ruhScene, rs *hookedReuseStore) {
				other := e.seedRow(t, killRowSpec{State: store.StateEnded, Agent: agentGone, NoSession: true}).ID
				rs.afterReset(func() {
					if err := e.st.SetParentID(s.r.ID, other); err != nil {
						t.Errorf("SetParentID: %v", err)
					}
				})
			}},
		{name: "row deleted at the create", outcome: apitest.RestoreRowRemoved,
			arrange: func(t *testing.T, e *killEnv, s *ruhScene, _ *hookedReuseStore) {
				e.rec.AfterCall(tmux.CallCreate, func(tmuxfix.SocketCall, error) {
					if err := e.st.DeleteSpawn(s.r.ID); err != nil {
						t.Errorf("DeleteSpawn: %v", err)
					}
				})
			}},
		{name: "restore store error", outcome: apitest.RestoreStoreError,
			arrange: func(t *testing.T, e *killEnv, s *ruhScene, _ *hookedReuseStore) {
				storefix.InjectWriteFailure(t, e.dbPath, storefix.WriteFailReuseRestore, s.r.ID)
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			s := e.ruhArrange(t, store.StateEnded, rlkSettled(e), heldSpec{Holder: holderNone})
			rs := &hookedReuseStore{st: e.st}
			tc.arrange(t, e, s, rs)

			_, _, err := e.reuseWith(t, rs, s.p)

			assertOneSentinel(t, err, api.ErrTmuxSessionConflict)
			if err == nil {
				t.Fatal("reuse err = nil; want the held-name refusal")
			}
			apitest.AssertDescription(t, err.Error(), apitest.DescHeldNoValidID(apitest.HeldName{
				Name: s.p.TmuxSessionName, SessionID: s.sc.Holder().ID,
				Restore: apitest.ResumeRestore{Outcome: tc.outcome, Launch: apitest.LaunchReuse}}), ruhForbid(e, s)...)
			final, present := e.rrcColumns(t, s.r.ID)
			if want := tc.outcome != apitest.RestoreRowRemoved; present != want {
				t.Errorf("row present after the reuse = %v; want %v", present, want)
			}
			if present && !reflect.DeepEqual(final, s.sc.Moved) {
				t.Errorf("row after the reuse =\n  %+v\nwant as at the create (nothing restored)\n  %+v", final, s.sc.Moved)
			}
			e.assertHolderUntouched(t, s.sc)
			e.ruhAssertCalls(t, s)
		})
	}
}

// TestSpawnReuseHeldReissue (AC-RES-12's reuse half): after the restored
// refusal a re-issue is refused before its reset while the holder runs; once it is gone one launches.
func TestSpawnReuseHeldReissue(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID with t.Setenv; it checks every record written to the
	// shared trail since its mark.
	e := newKillEnv(t)
	s := e.ruhArrange(t, store.StateEnded, rlkSettled(e), heldSpec{Holder: holderForeign, Created: rlkSettled(e)})
	if _, _, err := e.reuse(t, s.p); err == nil {
		t.Fatal("first reuse err = nil; want the held-name refusal")
	}
	e.assertRowUnchanged(t, s.r.ID, ruhRestored(s))
	s.r.Trust.reset(t)
	before := e.snapshotReuse(t, s.r)

	_, _, err := e.reuse(t, s.p)

	assertOneSentinel(t, err, api.ErrTmuxSessionConflict)
	e.assertWroteNothing(t, before)

	e.removeHolders(t, s.sc)
	launch := resumeSnapshot{writesSnapshot: e.snapshotReuse(t, s.r), r: s.r.resumeRow}

	_, _, err = e.reuse(t, s.p)

	e.rlkAssertLaunched(t, launch, err)
	if cols := e.columns(t, s.r.ID); cols.TmuxSessionName != s.p.TmuxSessionName || cols.LifeNumber != reuseLife+1 {
		t.Errorf("row {name %v, life %v}; want %q, %d", cols.TmuxSessionName, cols.LifeNumber, s.p.TmuxSessionName, reuseLife+1)
	}
}
