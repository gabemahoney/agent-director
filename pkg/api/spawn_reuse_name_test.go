package api_test

// spawn_reuse_name_test.go covers reuse's new-name pre-check (SR-10.8,
// SR-3.10; AC-REUSE-06, AC-REUSE-07, AC-LKP-14, AC-LKP-20): after Gone with
// no agent process running, the holder of the requested name, classified from
// the old-row lookup's own listing before anything changes. The runner is
// rulRun (spawn_reuse_lookup_test.go).

import (
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

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
		{"another store's label with this row's id and token", holderOtherStoreOwn, conflict,
			func(e *killEnv, _ reuseRow, name string, s tmuxfix.SeedSession) apitest.DescCase {
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

// TestSpawnReuseNameNotHeld: prefix neighbours, Gone with no server or socket, and the
// recorded name held while a new one is requested: each launches.
func TestSpawnReuseNameNotHeld(t *testing.T) {
	stopped := func(f tmux.Failure) func(*testing.T, *killEnv, *reuseRow, string) tmuxfix.SeedSession {
		return func(t *testing.T, e *killEnv, r *reuseRow, _ string) tmuxfix.SeedSession {
			callTableStop(f, true)(t, e, &r.killRow)
			return tmuxfix.SeedSession{}
		}
	}
	var cases []rulCase
	for _, n := range rnmNames {
		cases = append(cases,
			rulCase{name: "prefix neighbour, a longer name/" + n.name, requested: n.requested, seed: rulHolder(holderPrefixNeighbour)},
			rulCase{name: "prefix neighbour, a prefix of the name/" + n.name, requested: n.requested,
				seed: func(t *testing.T, e *killEnv, r *reuseRow, name string) tmuxfix.SeedSession {
					return e.seedOther(t, r.Socket, tmuxfix.SeedSession{Name: name[:len(name)-1]})
				}},
			rulCase{name: "no server/" + n.name, requested: n.requested, seed: stopped(tmux.FailNoServer)},
			rulCase{name: "no socket/" + n.name, requested: n.requested, seed: stopped(tmux.FailNoSocket)},
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
