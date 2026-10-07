package api_test

// spawn_reuse_lookup_test.go covers reuse's old-row lookup at the finished-row
// cell of the decision table (SR-10.2, SR-3.3, SR-3.4, SR-3.6, SR-4.2, SR-10.5;
// AC-REUSE-06, AC-LKP-20): each lookup outcome on an ended and a missing row,
// the socket refusal, the recorded socket, no adoption and the Leftover lost
// race. The window and bound boundaries are spawn_reuse_window_test.go's.
// Fixture: spawn_reuse_fixture_test.go.

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// rulCase is one reuse of a seeded reusable row: its agent process, extra row
// options, the requested name (nil: the recorded one), the sessions seed
// places (returning the one the refusal names) and the refusal's error name
// and description; want "" is the launch.
type rulCase struct {
	name      string
	agent     agentState
	opts      func(*killEnv) []apitest.SpawnOption
	requested func(reuseRow) string
	seed      func(t *testing.T, e *killEnv, r *reuseRow, name string) tmuxfix.SeedSession
	want      string
	desc      func(e *killEnv, r reuseRow, name string, s tmuxfix.SeedSession) apitest.DescCase
}

// rulRun reuses tc's row in state (ended past the window and the bound) and checks
// the launch, or the refusal: one name, description, one lookup, nothing written, no adoption.
func rulRun(t *testing.T, tc rulCase, state string) {
	t.Helper()
	e := newKillEnv(t)
	spec := reuseRowSpec{State: state, Age: rlkSettled(e)}
	if tc.opts != nil {
		spec.Opts = tc.opts(e)
	}
	r := e.seedReusable(t, tc.agent, spec)
	name := r.Name
	if tc.requested != nil {
		name = tc.requested(r)
	}
	var s tmuxfix.SeedSession
	if tc.seed != nil {
		s = tc.seed(t, e, &r, name)
	}
	before := e.snapshotReuse(t, r)

	_, logs, err := e.reuse(t, reuseParams(t, r, reuseRequest{Name: name}))

	if tc.want == "" {
		e.rlkAssertLaunched(t, resumeSnapshot{writesSnapshot: before, r: r.resumeRow}, err)
		return
	}
	assertOneName(t, err, tc.want)
	other := s.Label.InstanceID
	if other == r.ID {
		other = ""
	}
	apitest.AssertDescription(t, err.Error(), tc.desc(e, r, name, s),
		other, r.Token, s.Label.Token, e.storeID, apitest.OtherStoreID(e.storeID))
	e.assertWroteNothing(t, before)
	e.rulAssertOneLookup(t, r, before)
	if n := adoptedRecords(t, "spawn", r.ID); n != 0 {
		t.Errorf("adopted records = %d; want 0 (log %q)", n, logs)
	}
}

// rulAssertOneLookup fails unless, since before, the reuse made exactly one
// tmux call: the lookup on r's socket.
func (e *killEnv) rulAssertOneLookup(t *testing.T, r reuseRow, before writesSnapshot) {
	t.Helper()
	if calls := e.rec.SocketCalls()[before.calls:]; len(calls) != 1 || calls[0].Call != tmux.CallLookup || calls[0].Socket != r.Socket {
		t.Errorf("tmux calls = %+v; want one lookup on %s", calls, r.Socket)
	}
}

// rulNewName is a requested name other than the row's recorded one.
func rulNewName(reuseRow) string { return "requested-" + uuid.NewString()[:8] }

// rulOwn seeds r's own session, created age before the rule's reading.
func rulOwn(age func(*killEnv) time.Duration) func(*testing.T, *killEnv, *reuseRow, string) tmuxfix.SeedSession {
	return func(t *testing.T, e *killEnv, r *reuseRow, _ string) tmuxfix.SeedSession {
		e.seedSession(t, &r.killRow, e.createdBefore(age(e)))
		return r.Session
	}
}

// rulHolder seeds seedHolder's kind k under the requested name.
func rulHolder(k holderKind) func(*testing.T, *killEnv, *reuseRow, string) tmuxfix.SeedSession {
	return func(t *testing.T, e *killEnv, r *reuseRow, name string) tmuxfix.SeedSession {
		return e.seedHolder(t, r.killRow.withName(name), k)
	}
}

// rulHeld is the pre-check holder parameters naming s under the requested name.
func rulHeld(name string, s tmuxfix.SeedSession) apitest.HeldName {
	return apitest.HeldName{Name: name, SessionID: s.ID, BeforeLaunch: true}
}

// The description builders the tables share; the starting-session cases
// quote the recorded name.
var (
	rulOwnOld = func(noSession bool) func(*killEnv, reuseRow, string, tmuxfix.SeedSession) apitest.DescCase {
		return func(e *killEnv, r reuseRow, _ string, _ tmuxfix.SeedSession) apitest.DescCase {
			return apitest.DescOwnOldSession(rlkStarting(e, r.resumeRow, noSession))
		}
	}
	rulLeftover = func(_ *killEnv, r reuseRow, _ string, s tmuxfix.SeedSession) apitest.DescCase {
		return apitest.DescPreLaunchLeftover(r.ID, []apitest.DescSession{{Name: s.Name, ID: s.ID}})
	}
	rulNoValidID = func(_ *killEnv, _ reuseRow, name string, s tmuxfix.SeedSession) apitest.DescCase {
		return apitest.DescHeldNoValidID(rulHeld(name, s))
	}
)

// rulCantTell is one case per call-site table column whose shared cell
// refuses at the lookup (Can't tell, tmux unavailable): the column's world,
// its own old session first unless it has none, and the cell's description.
func rulCantTell() []rulCase {
	refusals := callTableLookupRefusals()
	var out []rulCase
	for _, col := range callTableColumns() {
		cell, ok := refusals[col.outcome]
		if !ok {
			continue
		}
		out = append(out, rulCase{name: string(col.outcome), agent: col.spec.Agent, want: cell.errName,
			seed: func(t *testing.T, e *killEnv, r *reuseRow, _ string) tmuxfix.SeedSession {
				if !col.spec.NoSession {
					e.seedSession(t, &r.killRow, e.createdBefore(rlkSettled(e)))
				}
				col.world(t, e, &r.killRow)
				return tmuxfix.SeedSession{}
			},
			desc: func(e *killEnv, r reuseRow, _ string, _ tmuxfix.SeedSession) apitest.DescCase {
				return cell.desc(e, r.killRow)
			}})
	}
	return out
}

// TestSpawnReuseLookupOutcomes: per lookup outcome on an ended and a missing
// row, the starting-session step, the conflict, Can't tell's error or the launch.
func TestSpawnReuseLookupOutcomes(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	const conflict, unresponsive = "ErrTmuxSessionConflict", "ErrTmuxUnresponsive"
	settled := rlkSettled
	elsewhere := func(t *testing.T, e *killEnv, r *reuseRow, k holderKind) tmuxfix.SeedSession {
		return e.seedHolder(t, r.killRow.withName("elsewhere-"+uuid.NewString()[:8]), k)
	}
	noIdentity := func(e *killEnv) []apitest.SpawnOption {
		return []apitest.SpawnOption{apitest.WithLaunchIdentity(store.LaunchIdentity{Token: newToken(), Socket: e.defaultSocket})}
	}
	cases := []rulCase{
		{name: "ours, old session: own id", agent: agentGone, seed: rulOwn(settled), want: conflict, desc: rulOwnOld(false)},
		{name: "ours, young session: still starting", agent: agentGone, want: unresponsive,
			seed: rulOwn(func(*killEnv) time.Duration { return 0 }),
			desc: func(e *killEnv, r reuseRow, _ string, _ tmuxfix.SeedSession) apitest.DescCase {
				return apitest.DescStillStarting(rlkStarting(e, r.resumeRow, false))
			}},
		{name: "ours with no recorded identity is not adopted", agent: agentGone, opts: noIdentity,
			seed: rulOwn(settled), want: conflict, desc: rulOwnOld(false)},
		{name: "leftover under the recorded name", agent: agentGone, seed: rulHolder(holderOld), want: conflict, desc: rulLeftover},
		{name: "leftover under the requested name", agent: agentGone, requested: rulNewName, seed: rulHolder(holderOld),
			want: conflict, desc: rulLeftover},
		{name: "leftover, agent process running", agent: agentAlive, seed: rulHolder(holderOld), want: conflict, desc: rulLeftover},
		{name: "gone, agent process running: the rule with no session", agent: agentAlive, want: conflict, desc: rulOwnOld(true)},
		{name: "gone, agent process dead: launches", agent: agentGone},
		{name: "gone, agent process unreadable: launches", agent: agentUnreadable},
		{name: "another store's label, row's token, recorded name, new name requested", agent: agentGone,
			requested: rulNewName, seed: func(t *testing.T, e *killEnv, r *reuseRow, _ string) tmuxfix.SeedSession {
				return e.seedHolder(t, r.killRow, holderOtherStoreOwn)
			}},
		{name: "another store's label, row's token, another name", agent: agentGone,
			seed: func(t *testing.T, e *killEnv, r *reuseRow, _ string) tmuxfix.SeedSession {
				return elsewhere(t, e, r, holderOtherStoreOwn)
			}},
		{name: "another store's label, another token, recorded name, new name requested", agent: agentGone,
			requested: rulNewName, seed: func(t *testing.T, e *killEnv, r *reuseRow, _ string) tmuxfix.SeedSession {
				return e.seedHolder(t, r.killRow, holderOtherStoreOldToken)
			}},
		{name: "another store's label, another token, another name", agent: agentGone,
			seed: func(t *testing.T, e *killEnv, r *reuseRow, _ string) tmuxfix.SeedSession {
				return elsewhere(t, e, r, holderOtherStoreOldToken)
			}},
		{name: "AC-REUSE-06: unlabelled session under the recorded name", agent: agentGone, seed: rulHolder(holderNone),
			want: conflict, desc: rulNoValidID},
	}
	for _, tc := range append(cases, rulCantTell()...) {
		for _, state := range finishedStates {
			t.Run(tc.name+"/"+state, func(t *testing.T) { rulRun(t, tc, state) })
		}
	}
}

// TestSpawnReuseLookupOnRecordedSocket: the one lookup goes to the recorded socket
// before pre-trust and the reset; a holder on the default socket is not consulted.
func TestSpawnReuseLookupOnRecordedSocket(t *testing.T) {
	t.Parallel()
	for _, state := range finishedStates {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			recorded := filepath.Join(filepath.Dir(e.defaultSocket), "recorded-"+uuid.NewString()[:8])
			r := e.seedReusable(t, agentGone, reuseRowSpec{State: state, Age: rlkSettled(e),
				Opts: []apitest.SpawnOption{apitest.WithTmuxSocket(recorded)}})
			e.seedHolder(t, killRow{Name: r.Name, Socket: e.defaultSocket}, holderNone)
			before := e.snapshotReuse(t, r)
			e.rec.AfterCall(tmux.CallLookup, func(tmuxfix.SocketCall, error) {
				if st := e.columns(t, r.ID).State; st != state {
					t.Errorf("state when the lookup returned = %v; want %s (not yet reset)", st, state)
				}
				r.Trust.check(t, r.CWD, false, "when the lookup returned")
			})

			_, _, err := e.reuse(t, reuseParams(t, r, reuseRequest{}))

			e.rlkAssertLaunched(t, resumeSnapshot{writesSnapshot: before, r: r.resumeRow}, err)
		})
	}
}

// TestSpawnReuseSocketRefused: a recorded socket whose directory cannot be
// made is ErrTmuxNotAvailable before any tmux call, with nothing written.
func TestSpawnReuseSocketRefused(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	for _, state := range finishedStates {
		t.Run(state, func(t *testing.T) {
			e := newKillEnv(t)
			sock := filepath.Join(userSocketDir(filepath.Join(t.TempDir(), "gone")), "default")
			refuse := &tmux.SocketDirError{Socket: sock, Dir: filepath.Dir(sock), Reason: tmux.SocketDirNotCreatable}
			r := e.seedReusable(t, agentGone, reuseRowSpec{State: state, Age: rlkSettled(e),
				Opts: []apitest.SpawnOption{apitest.WithTmuxSocket(sock)}})
			before := e.snapshotReuse(t, r)

			_, _, err := e.reuse(t, reuseParams(t, r, reuseRequest{}))

			assertOneName(t, err, "ErrTmuxNotAvailable")
			apitest.AssertDescription(t, err.Error(), apitest.DescSocketDir(sock, refuse.Dir, refuse.Error()), r.Token)
			e.assertWroteNothing(t, before)
			if calls := e.rec.SocketCalls()[before.calls:]; len(calls) != 0 {
				t.Errorf("tmux calls = %+v; want none", calls)
			}
		})
	}
}

// TestSpawnReuseLookupLeftoverLostRace (SR-10.5): a Leftover after a competing write
// changed or removed the row is the lost race; unchanged, the Leftover refusal stands.
func TestSpawnReuseLookupLeftoverLostRace(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	for _, tc := range []struct {
		name    string
		compete func(e *killEnv, id string) error // after the pre-check read; nil: none
		removed bool
	}{
		{name: "row changed", compete: func(e *killEnv, id string) error { return e.st.SetParentID(id, "") }},
		{name: "row removed", compete: func(e *killEnv, id string) error { return e.st.DeleteSpawn(id) }, removed: true},
		{name: "row unchanged"},
	} {
		for _, state := range finishedStates {
			t.Run(tc.name+"/"+state, func(t *testing.T) {
				e := newKillEnv(t)
				r := e.seedReusable(t, agentGone, reuseRowSpec{State: state, Age: rlkSettled(e)})
				s := e.seedHolder(t, r.killRow, holderOld)
				rs := &hookedReuseStore{st: e.st}
				before := e.snapshotReuse(t, r)
				if tc.compete != nil {
					rs.afterRead(func() {
						if err := tc.compete(e, r.ID); err != nil {
							t.Fatalf("competing write: %v", err)
						}
						if !tc.removed {
							before = e.snapshotReuse(t, r) // the competitor's row is what must stay
						}
					})
				}

				_, _, err := e.reuseWith(t, rs, reuseParams(t, r, reuseRequest{}))

				if n := rs.readCount(); n != 2 {
					t.Errorf("ReadForReuse calls = %d; want 2 (the pre-check and the one re-read)", n)
				}
				e.rulAssertOneLookup(t, r, before)
				if tc.compete == nil {
					assertOneName(t, err, "ErrTmuxSessionConflict")
					apitest.AssertDescription(t, err.Error(), rulLeftover(e, r, r.Name, s), r.Token, s.Label.Token)
				} else {
					assertOneName(t, err, "ErrInstanceIdCollision")
					apitest.AssertDescription(t, err.Error(), apitest.DescReuseLostRace(r.ID), r.Token, s.Label.Token)
				}
				if !tc.removed {
					e.assertWroteNothing(t, before)
					return
				}
				if _, err := apitest.ReadSpawnColumns(e.dbPath, r.ID); err == nil {
					t.Errorf("row %s exists after the reuse; want it still removed", r.ID)
				}
				r.Trust.check(t, "", false, "after the lost race")
				for _, l := range readAPITrailLines(t)[before.mark:] {
					if l["event"] != "ad.provenance.disagree" {
						t.Errorf("trail record %v for %v; want none but ad.provenance.disagree", l["event"], l["claude_instance_id"])
					}
				}
			})
		}
	}
}
