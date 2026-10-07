package api_test

// spawn_reuse_lookup_test.go covers reuse's old-row lookup and new-name
// pre-check (SR-10.2, SR-10.5, SR-10.8, SR-3.10, SR-4.2, SR-5.5, SR-14;
// AC-REUSE-06, 07, 14, AC-LKP-14, AC-LKP-20, AC-CFG-02): each outcome (the
// call-site table's Can't tell cells included), the socket refusal, the
// Leftover lost race, the requested name's holder, the starting-session rule
// and the disagree records. The recorded socket is TestSpawnReuseAppliedRow's.

import (
	"cmp"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// rulCase is one reuse of a seeded row: its agent, options, requested name
// (nil: the recorded one), seeded sessions (returning the one the refusal
// names) and the refusal's error name and description; want "" launches.
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
	rulAbandoned = func(past bool) func(*killEnv, reuseRow, string, tmuxfix.SeedSession) apitest.DescCase {
		return func(e *killEnv, r reuseRow, _ string, s tmuxfix.SeedSession) apitest.DescCase {
			return apitest.DescAbandonedLaunch(apitest.AbandonedLaunch{InstanceID: r.ID,
				Sessions: []apitest.DescSession{{Name: s.Name, ID: s.ID}}, Bound: e.cfg.EffectiveStartingSession(),
				PastBound: past, SessionID: true})
		}
	}
)

// rulNoLaunchSession records a launch token but no session of that launch (no server or pane identity).
func rulNoLaunchSession(e *killEnv) []apitest.SpawnOption {
	return []apitest.SpawnOption{apitest.WithLaunchIdentity(store.LaunchIdentity{Token: newToken(), Socket: e.defaultSocket})}
}

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
	cases := []rulCase{
		{name: "ours, old session: own id", agent: agentGone, seed: rulOwn(settled), want: conflict, desc: rulOwnOld(false)},
		{name: "ours, young session: still starting", agent: agentGone, want: unresponsive,
			seed: rulOwn(func(*killEnv) time.Duration { return 0 }),
			desc: func(e *killEnv, r reuseRow, _ string, _ tmuxfix.SeedSession) apitest.DescCase {
				return apitest.DescStillStarting(rlkStarting(e, r.resumeRow, false))
			}},
		{name: "ours with no recorded identity is not adopted", agent: agentGone, opts: rulNoLaunchSession,
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
		// b.1n6: with no session of the row's latest launch recorded, a leftover is this id's own abandoned launch.
		{name: "no session recorded, leftover young: abandoned launch still starting", agent: agentGone,
			opts: rulNoLaunchSession, seed: rulHolder(holderOld), want: unresponsive, desc: rulAbandoned(false)},
		{name: "no session recorded, leftover past the bound: abandoned launch", agent: agentGone, opts: rulNoLaunchSession,
			seed: func(t *testing.T, e *killEnv, r *reuseRow, _ string) tmuxfix.SeedSession {
				return e.seedLeftover(t, r.killRow, newToken(), settled(e))
			}, want: conflict, desc: rulAbandoned(true)},
	}
	for _, tc := range append(cases, rulCantTell()...) {
		for _, state := range finishedStates {
			t.Run(tc.name+"/"+state, func(t *testing.T) { rulRun(t, tc, state) })
		}
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

// TestSpawnReuseLookupLeftoverLostRace (SR-10.5): a Leftover after a competing write changed or removed the
// row is the lost race, decided by the one re-read, also when the row records no session of its latest launch, so
// the young leftover is this id's own abandoned launch (b.1n6); an unchanged row's refusal is TestSpawnReuseLookupOutcomes'.
func TestSpawnReuseLookupLeftoverLostRace(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	for _, tc := range []struct {
		name    string
		compete func(e *killEnv, id string) error // after the pre-check read
		removed bool
	}{
		{name: "row changed", compete: func(e *killEnv, id string) error { return e.st.SetParentID(id, "") }},
		{name: "row removed", compete: func(e *killEnv, id string) error { return e.st.DeleteSpawn(id) }, removed: true},
	} {
		for _, row := range []struct {
			name string
			opts func(*killEnv) []apitest.SpawnOption
		}{{"leftover", nil}, {"no session recorded, young abandoned session", rulNoLaunchSession}} {
			for _, state := range finishedStates {
				t.Run(tc.name+"/"+row.name+"/"+state, func(t *testing.T) {
					e := newKillEnv(t)
					spec := reuseRowSpec{State: state, Age: rlkSettled(e)}
					if row.opts != nil {
						spec.Opts = row.opts(e)
					}
					r := e.seedReusable(t, agentGone, spec)
					s := e.seedHolder(t, r.killRow, holderOld)
					rs := &hookedReuseStore{st: e.st}
					before := e.snapshotReuse(t, r)
					rs.afterRead(func() {
						if err := tc.compete(e, r.ID); err != nil {
							t.Fatalf("competing write: %v", err)
						}
						if !tc.removed {
							before = e.snapshotReuse(t, r) // the competitor's row is what must stay
						}
					})

					_, _, err := e.reuseWith(t, rs, reuseParams(t, r, reuseRequest{}))

					if n := rs.readCount(); n != 2 {
						t.Errorf("ReadForReuse calls = %d; want 2 (the pre-check and the one re-read)", n)
					}
					e.rulAssertOneLookup(t, r, before)
					assertOneName(t, err, "ErrInstanceIdCollision")
					apitest.AssertDescription(t, err.Error(), apitest.DescReuseLostRace(r.ID), r.Token, s.Label.Token)
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
}

// rnmNames are the requested names a pre-check case runs with.
var rnmNames = []struct {
	name      string
	requested func(reuseRow) string
}{
	{"recorded name", nil},
	{"new name", rulNewName},
}

// rnmAmbiguous is DescHeldAmbiguous for the requested name, forbidding the
// tmux ids of the sessions listed under its stored form.
func rnmAmbiguous(e *killEnv, r reuseRow, name string, _ tmuxfix.SeedSession) apitest.DescCase {
	c := apitest.DescHeldAmbiguous(apitest.HeldName{Name: name, BeforeLaunch: true})
	for _, s := range e.rec.Sessions(r.Socket) {
		if s.Name == storedFormOf(name) || s.Name == name {
			c.Forbid = append(c.Forbid, s.ID)
		}
	}
	return c
}

// TestSpawnReuseNameHolders: per holder class of the requested name (the
// recorded or a new one), the class's refusal from the one listing, or the launch.
func TestSpawnReuseNameHolders(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	const conflict = "ErrTmuxSessionConflict"
	for _, h := range []struct {
		name string
		kind holderKind
		want string
		desc func(*killEnv, reuseRow, string, tmuxfix.SeedSession) apitest.DescCase
	}{
		{"no holder", holderVanished, "", nil},
		{"foreign label", holderForeign, conflict, func(_ *killEnv, _ reuseRow, name string, s tmuxfix.SeedSession) apitest.DescCase {
			return apitest.DescHeldDifferentID(rulHeld(name, s))
		}},
		{"another store's label", holderOtherStore, conflict, func(e *killEnv, _ reuseRow, name string, s tmuxfix.SeedSession) apitest.DescCase {
			return apitest.DescHeldOtherStore(rulHeld(name, s), e.storeID)
		}},
		{"no valid label", holderNone, conflict, rulNoValidID},
		{"malformed label", holderMalformed, conflict, rulNoValidID},
		{"more than one entry matches", holderAmbiguous, "ErrTmuxUnresponsive", rnmAmbiguous},
	} {
		for _, n := range rnmNames {
			tc := rulCase{name: h.name, agent: agentGone, requested: n.requested, seed: rulHolder(h.kind), want: h.want, desc: h.desc}
			t.Run(h.name+"/"+n.name, func(t *testing.T) { rulRun(t, tc, store.StateEnded) })
		}
	}
}

// TestSpawnReuseNameNotHeld: prefix neighbours, and the recorded name held while a new
// one is requested: each launches. Gone with no server or socket is TestCallTableReuse's.
func TestSpawnReuseNameNotHeld(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	var cases []rulCase
	for _, n := range rnmNames {
		cases = append(cases,
			rulCase{name: "prefix neighbour, a longer name/" + n.name, requested: n.requested, seed: rulHolder(holderPrefixNeighbour)},
			rulCase{name: "prefix neighbour, a prefix of the name/" + n.name, requested: n.requested,
				seed: func(t *testing.T, e *killEnv, r *reuseRow, name string) tmuxfix.SeedSession {
					return e.seedOther(t, r.Socket, tmuxfix.SeedSession{Name: name[:len(name)-1]})
				}},
		)
	}
	cases = append(cases, rulCase{name: "recorded name held, new name requested", requested: rulNewName,
		seed: func(t *testing.T, e *killEnv, r *reuseRow, _ string) tmuxfix.SeedSession {
			return e.seedHolder(t, r.killRow, holderNone)
		}})
	for _, tc := range cases {
		tc.agent = agentGone
		t.Run(tc.name, func(t *testing.T) { rulRun(t, tc, store.StateEnded) })
	}
}

// TestSpawnReuseNameDollarAndBackslash (AC-LKP-14): per catalogued $ or \ name, either
// stored form blocks, both listed is ambiguous, a form matching neither is not held.
func TestSpawnReuseNameDollarAndBackslash(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	named := func(name string) func(*testing.T, *killEnv, *reuseRow, string) tmuxfix.SeedSession {
		return func(t *testing.T, e *killEnv, r *reuseRow, _ string) tmuxfix.SeedSession {
			return e.seedOther(t, r.Socket, tmuxfix.SeedSession{Name: name})
		}
	}
	for _, n := range tmuxfix.StoredNames() {
		if !n.LabelByID || strings.ContainsAny(n.Raw, ".:") { // '.' and ':' names are unusable (Epic 19)
			continue
		}
		cases := []rulCase{
			{name: "held in the stored form", seed: named(n.Stored), want: "ErrTmuxSessionConflict", desc: rulNoValidID},
			{name: "no stored form matches", seed: named(`\` + n.Stored)},
		}
		if n.Raw != n.Stored {
			cases = append(cases,
				rulCase{name: "held in the raw form", seed: named(n.Raw), want: "ErrTmuxSessionConflict", desc: rulNoValidID},
				rulCase{name: "listed in both forms", want: "ErrTmuxUnresponsive", desc: rnmAmbiguous,
					seed: func(t *testing.T, e *killEnv, r *reuseRow, name string) tmuxfix.SeedSession {
						named(n.Raw)(t, e, r, name)
						return named(n.Stored)(t, e, r, name)
					}})
		}
		for _, tc := range cases {
			tc.agent = agentGone
			tc.opts = func(*killEnv) []apitest.SpawnOption {
				return []apitest.SpawnOption{apitest.WithTmuxSessionName(n.Raw)}
			}
			t.Run(strings.ReplaceAll(n.Raw, "/", "_")+"/"+tc.name, func(t *testing.T) { rulRun(t, tc, store.StateEnded) })
		}
	}
}

// TestSpawnReuseNameAfterOldRowLookup: with the new name held, the old-row outcome
// refuses first and the requested name is never quoted.
func TestSpawnReuseNameAfterOldRowLookup(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	heldAnd := func(then func(*testing.T, *killEnv, *reuseRow, string) tmuxfix.SeedSession) func(*testing.T, *killEnv, *reuseRow, string) tmuxfix.SeedSession {
		return func(t *testing.T, e *killEnv, r *reuseRow, name string) tmuxfix.SeedSession {
			rulHolder(holderForeign)(t, e, r, name)
			if then == nil {
				return tmuxfix.SeedSession{}
			}
			return then(t, e, r, name)
		}
	}
	notQuoting := func(desc func(*killEnv, reuseRow, string, tmuxfix.SeedSession) apitest.DescCase) func(*killEnv, reuseRow, string, tmuxfix.SeedSession) apitest.DescCase {
		return func(e *killEnv, r reuseRow, name string, s tmuxfix.SeedSession) apitest.DescCase {
			c := desc(e, r, name, s)
			c.Forbid = append(c.Forbid, name)
			return c
		}
	}
	for _, tc := range []rulCase{
		{name: "leftover under the recorded name", agent: agentGone, desc: notQuoting(rulLeftover),
			seed: heldAnd(func(t *testing.T, e *killEnv, r *reuseRow, _ string) tmuxfix.SeedSession {
				return e.seedHolder(t, r.killRow, holderOld)
			})},
		{name: "ours, old session", agent: agentGone, seed: heldAnd(rulOwn(rlkSettled)), desc: notQuoting(rulOwnOld(false))},
		{name: "gone, agent process running", agent: agentAlive, seed: heldAnd(nil), desc: notQuoting(rulOwnOld(true))},
	} {
		tc.requested, tc.want = rulNewName, "ErrTmuxSessionConflict"
		t.Run(tc.name, func(t *testing.T) { rulRun(t, tc, store.StateEnded) })
	}
}

// reuseStartingRow is a reusable row as the rule sees it at ruleInstant: its
// seed spec (ended_at Age ago), its agent, and its own session's age
// (noSession: Gone while the agent runs).
type reuseStartingRow struct {
	spec      reuseRowSpec
	agent     agentState
	noSession bool
	age       time.Duration
}

// windowSkipped reports whether the rule skips the stopping window for s: no
// parseable ended_at, or neither a pid nor a session id.
func (s reuseStartingRow) windowSkipped() bool {
	return s.spec.EndedAt != endedAged || (s.agent == agentNotRecorded && s.spec.NoSessionID)
}

// seedReuseStarting seeds s's reusable row with its own session unless
// noSession; a raw row (GetSpawn refuses it) gets its current-labelled
// session through placeHolder.
func (e *killEnv) seedReuseStarting(t *testing.T, s reuseStartingRow) reuseRow {
	t.Helper()
	r := e.seedReusable(t, s.agent, s.spec)
	switch {
	case s.noSession:
	case s.spec.EndedAt == endedUnparseable:
		own := e.holderSessions(t, r.killRow, holderCurrent)
		own[0].Created = e.ruleInstant().Add(-s.age).Unix()
		e.placeHolder(t, r.killRow, holderCurrent, own)
	default:
		e.seedSession(t, &r.killRow, e.createdBefore(s.age))
	}
	return r
}

// reuseStarting reuses s's row (requesting its recorded name) through a Client
// configured with settings and checks the refusal is want's under bound and
// window, that nothing was written and that the Client logged nothing.
func reuseStarting(t *testing.T, e *killEnv, r reuseRow, s reuseStartingRow, want tmux.StartingSessionOutcome,
	bound, window time.Duration, settings ...apitest.TmuxSetting) {
	t.Helper()
	before := e.snapshotReuse(t, r)
	_, logs, err := e.reuse(t, reuseParams(t, r, reuseRequest{}), settings...)
	p := apitest.StartingSession{InstanceID: r.ID, Name: r.Name, Window: window, Bound: bound, NoSession: s.noSession,
		WindowChecked: !s.windowSkipped(), SessionID: !s.spec.NoSessionID}
	e.assertStartingRefusal(t, resumeSnapshot{writesSnapshot: before, r: r.resumeRow}, err, want, p)
	if logs != "" {
		t.Errorf("Client log = %q; want none (a refusal is reported by its error alone)", logs)
	}
}

// TestSpawnReuseStartingCases (SR-4.2, SR-5.5; AC-REUSE-14, AC-CFG-02): the
// starting-session rule on reuse's raw row (a NULL or unparseable ended_at is
// none; no pid nor session id skips the window), and each setting configured
// on its own, around the window and the bound. Rows shared with resume are
// TestResumeStartingSessionCases', TestResumeStartingSettings' and
// TestStartingSessionSettings'.
func TestSpawnReuseStartingCases(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	longAgo, inside := defWindow+defBound, defWindow-time.Second
	type startingCase struct {
		name          string
		row           reuseStartingRow
		want          tmux.StartingSessionOutcome
		settings      []apitest.TmuxSetting
		bound, window time.Duration // 0: the default
	}
	cases := []startingCase{
		{name: "ended long ago, session younger than the bound",
			row: reuseStartingRow{spec: reuseRowSpec{Age: longAgo}, age: defBound - time.Second}, want: tmux.StillStarting},
		{name: "NULL ended_at, session bound-1 s old", want: tmux.StillStarting,
			row: reuseStartingRow{spec: reuseRowSpec{State: store.StateMissing, EndedAt: endedNull}, age: defBound - time.Second}},
		{name: "unparseable ended_at, session bound-1 s old", want: tmux.StillStarting,
			row: reuseStartingRow{spec: reuseRowSpec{EndedAt: endedUnparseable}, age: defBound - time.Second}},
		{name: "unparseable ended_at, session the bound old", want: tmux.PastBoth,
			row: reuseStartingRow{spec: reuseRowSpec{EndedAt: endedUnparseable}, age: defBound}},
		{name: "neither pid nor session id, inside the window, session bound-1 s old", want: tmux.StillStarting,
			row: reuseStartingRow{spec: reuseRowSpec{Age: inside, NoSessionID: true}, agent: agentNotRecorded, age: defBound - time.Second}},
		{name: "neither pid nor session id, inside the window, session the bound old", want: tmux.PastBoth,
			row: reuseStartingRow{spec: reuseRowSpec{Age: inside, NoSessionID: true}, agent: agentNotRecorded, age: defBound}},
		{name: "no session id, outside the window, session the bound old", want: tmux.PastBoth,
			row: reuseStartingRow{spec: reuseRowSpec{Age: defWindow, NoSessionID: true}, age: defBound}},
		{name: "Gone, agent running, no session id, outside the window", want: tmux.PastBoth,
			row: reuseStartingRow{spec: reuseRowSpec{Age: longAgo, NoSessionID: true}, noSession: true}},
	}
	minB, minW := int64(config.MinStartingSessionSeconds), int64(config.MinStoppingWindowSeconds)
	for _, c := range []startingCase{
		{name: "window at its safe minimum", settings: []apitest.TmuxSetting{apitest.TmuxInt(config.TmuxStoppingWindowSeconds, minW)},
			bound: defBound, window: secs(minW)},
		{name: "bound at its safe minimum", settings: []apitest.TmuxSetting{apitest.TmuxInt(config.TmuxStartingSessionSeconds, minB)},
			bound: secs(minB), window: defWindow},
	} {
		for _, p := range []struct {
			name     string
			ago, age time.Duration
			want     tmux.StartingSessionOutcome
		}{
			{"ended window-1 s ago, session the bound old", c.window - time.Second, c.bound, tmux.StillStopping},
			{"ended window-1 s ago, session bound-1 s old", c.window - time.Second, c.bound - time.Second, tmux.StillStopping},
			{"ended the window ago, session bound-1 s old", c.window, c.bound - time.Second, tmux.StillStarting},
			{"ended the window ago, session the bound old", c.window, c.bound, tmux.PastBoth},
		} {
			pc := c
			pc.name, pc.row, pc.want = c.name+"/"+p.name, reuseStartingRow{spec: reuseRowSpec{Age: p.ago}, age: p.age}, p.want
			cases = append(cases, pc)
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedReuseStarting(t, tc.row)
			cols := e.columns(t, r.ID)
			if tc.row.spec.EndedAt == endedNull && cols.EndedAt != nil || tc.row.agent == agentNotRecorded && cols.PID != nil {
				t.Fatalf("precondition: ended_at %v, pid %v; want NULL", cols.EndedAt, cols.PID)
			}
			if sid, _ := cols.ClaudeSessionID.(string); tc.row.spec.NoSessionID && sid != "" {
				t.Fatalf("precondition: claude_session_id = %v; want NULL", cols.ClaudeSessionID)
			}
			reuseStarting(t, e, r, tc.row, tc.want, cmp.Or(tc.bound, defBound), cmp.Or(tc.window, defWindow), tc.settings...)
		})
	}
}

// rupCase is one old-row lookup arrangement and the records one reuse writes.
// held seeds a foreign-label holder of the requested name, whose session
// tmux_session_id then names; proceeds says the reuse reaches its reset.
type rupCase struct {
	name     string
	setup    []rpvSetup
	held     bool
	want     []disagreeWant
	proceeds bool
}

// rupCases are reuse's own rows of the reason table (each reason's rule is
// resume's, TestResumeProvenanceDisagree): none written, one written before
// the reset, a refusal at the old-row lookup and at the new-name pre-check,
// and a renamed session holding the requested name.
func rupCases() []rupCase {
	restarted := func(action string, ours bool) []disagreeWant {
		return []disagreeWant{{reason: "server_restarted", server: "restarted", verdict: "gone", action: action, ours: ours}}
	}
	return []rupCase{
		{name: "normal gone, names free, writes none", proceeds: true},
		{name: "server_restarted, gone, reuse proceeds", setup: []rpvSetup{rpvRestart}, want: restarted("proceeded", false),
			proceeds: true},
		{name: "server_restarted, requested name held: refused at the pre-check", setup: []rpvSetup{rpvRestart}, held: true,
			want: restarted("refused", true)},
		{name: "server_mismatch, recorded server runs", setup: []rpvSetup{rpvServer("rebind", nil), rpvBystander},
			want: []disagreeWant{{reason: "server_mismatch", server: "differs", verdict: "different_server", action: "refused"}}},
		{name: "name_changed to the requested name", setup: []rpvSetup{rpvOwn(rutRequested)},
			want: []disagreeWant{{reason: "name_changed", server: "match", verdict: "ours", action: "refused",
				current: rutRequested, ours: true}}},
	}
}

// seedRUPCase seeds tc's row beside another row's label, runs tc's setups and
// places tc's holder; it returns the row, the records' row and the other id.
func (e *killEnv) seedRUPCase(t *testing.T, tc rupCase) (reuseRow, killRow, string) {
	t.Helper()
	r := e.seedReusable(t, agentGone, reuseRowSpec{Age: rlkSettled(e)})
	other := "other-" + uuid.NewString()[:8]
	e.rec.SeedSessions(r.Socket, tmuxfix.SeedSession{Name: "foreign-" + uuid.NewString()[:8], Label: r.foreign(other)})
	for _, s := range tc.setup {
		s(t, e, &r.resumeRow)
	}
	named := r.killRow
	if tc.held {
		named.Session = e.seedHolder(t, r.withName(rutRequested), holderForeign)
	}
	return r, named, other
}

// TestSpawnReuseProvenanceDisagree (SR-14, SR-3.16): the old-row lookup's
// reasons are written once with verb spawn, source ad_spawn and the recorded
// name, before the reset or on a refusal; never adopted.
func TestSpawnReuseProvenanceDisagree(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	for _, tc := range rupCases() {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r, named, other := e.seedRUPCase(t, tc)
			w := &hookedReuseStore{st: e.st}
			atReset := -1
			w.beforeReset(func() { atReset = len(verbDisagrees(t, "spawn", r.ID)) })
			before := e.snapshotReuse(t, r)

			_, logs, err := e.reuseWith(t, w, reuseParams(t, r, reuseRequest{Name: rutRequested}))

			if (err == nil) != tc.proceeds {
				t.Fatalf("reuse err = %v (log %q); want proceeds %t", err, logs, tc.proceeds)
			}
			recs := verbDisagrees(t, "spawn", r.ID)
			if len(recs) != len(tc.want) {
				t.Fatalf("ad.provenance.disagree records = %d; want %d: %v", len(recs), len(tc.want), recs)
			}
			newTok, _ := e.columns(t, r.ID).LaunchToken.(string)
			for i, want := range tc.want {
				assertDisagreeRecord(t, recs[i], named, "spawn", "ad_spawn", want)
				ktrAssertNoForeignContent(t, recs[i], rupNonEmpty(r.Token, newTok, e.storeID, other)...)
			}
			if n := adoptedRecords(t, "spawn", r.ID); n != 0 {
				t.Errorf("adopted records = %d; want none", n)
			}
			switch {
			case tc.proceeds && atReset != len(tc.want):
				t.Errorf("records written before the reset = %d; want all %d", atReset, len(tc.want))
			case !tc.proceeds && atReset >= 0:
				t.Errorf("the refused reuse reached the reset")
			case !tc.proceeds:
				e.assertWroteNothing(t, before)
			}
		})
	}
}

// rupNonEmpty is values without the empty ones.
func rupNonEmpty(values ...string) []string {
	var out []string
	for _, v := range values {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}
