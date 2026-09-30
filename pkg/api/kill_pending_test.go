package api_test

// kill_pending_test.go: kill on a pending row (SR-6.1; AC-KILL-17, AC-KILL-18,
// AC-LKP-20): aborting a launch through its own session, a kill before the
// session exists, Gone beside a session that is not the launch's, and the
// Leftover refusal. Kill never changes the row.

import (
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// killPendKind is a pending row's shape: a fresh spawn's, a reuse's (a new
// life, the earlier life archived) or a resumed row's (a later life with its
// transcript and a current-life history entry).
type killPendKind struct {
	name string
	opts []apitest.SpawnOption
}

// killPendKinds returns the three pending row shapes of AC-KILL-17/18.
func killPendKinds() []killPendKind {
	return []killPendKind{
		{"fresh spawn", nil},
		{"reuse", []apitest.SpawnOption{apitest.WithLifeNumber(1),
			apitest.WithSessionHistory(apitest.SessionHistorySeed{SessionID: "sess-life-0", Life: 0})}},
		{"resumed", []apitest.SpawnOption{apitest.WithLifeNumber(2), apitest.WithJsonlPath("/tmp/sess-resumed.jsonl"),
			apitest.WithSessionHistory(apitest.SessionHistorySeed{SessionID: "sess-earlier", Life: 2})}},
	}
}

// killPendSpec is a pending row as a spawn's insert leaves it before its create
// returns: token and socket, no server, pane or process identity, no session.
func killPendSpec(state string, opts ...apitest.SpawnOption) killRowSpec {
	return killRowSpec{State: state, NoSession: true, NoPane: true, NoServerIdentity: true,
		Agent: agentNotRecorded, Opts: opts}
}

// killPendAssertRow fails unless id's row still reads before (state pending or
// as seeded, launch start and row_version included).
func killPendAssertRow(t *testing.T, e *killEnv, id string, before apitest.SpawnColumns) {
	t.Helper()
	if after := e.columns(t, id); !reflect.DeepEqual(after, before) {
		t.Errorf("row %s changed:\n got %+v\nwant %+v", id, after, before)
	}
}

// killPendAssertTrail fails unless id has exactly one ad.kill.called record with kill_sent want.
func killPendAssertTrail(t *testing.T, id string, want bool) {
	t.Helper()
	recs := killCalled(t, id)
	if len(recs) != 1 {
		t.Fatalf("ad.kill.called records = %d; want 1", len(recs))
	}
	if recs[0]["kill_sent"] != want {
		t.Errorf("ad.kill.called kill_sent = %v; want %v", recs[0]["kill_sent"], want)
	}
}

// TestKillPendingOwnSession: kill of a pending row whose own labelled session
// exists ends its pane, then its session, by id; the row stays pending.
func TestKillPendingOwnSession(t *testing.T) {
	for _, k := range killPendKinds() {
		t.Run(k.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedRow(t, killRowSpec{State: store.StatePending, Opts: k.opts})
			e.setAfterCall(tmux.CallKillPane, procfix.Gone(), r.AgentPID)
			before := e.columns(t, r.ID)
			if before.State != store.StatePending || before.LaunchStartedAt == nil {
				t.Fatalf("seeded state %v, launch start %v; want pending with a launch start", before.State, before.LaunchStartedAt)
			}
			res, err := e.kill(r.ID)
			if err != nil || !res.KillSent {
				t.Fatalf("kill = %+v, %v; want kill_sent true, nil", res, err)
			}
			e.assertKillCalls(t, tmux.CallLookup, tmux.CallListPanes, tmux.CallKillPane, tmux.CallKillSession)
			if got := e.rec.SocketCallsOf(tmux.CallKillPane)[0].Target; got != r.Spawn.Identity.PaneID {
				t.Errorf("pane kill target = %q; want the agent's pane %q", got, r.Spawn.Identity.PaneID)
			}
			if got := e.rec.SocketCallsOf(tmux.CallKillSession)[0].Target; got != r.Session.ID {
				t.Errorf("session kill target = %q; want the row's session %q", got, r.Session.ID)
			}
			for _, s := range e.rec.Sessions(r.Socket) {
				if s.ID == r.Session.ID {
					t.Errorf("session %s still in the table after kill", s.ID)
				}
			}
			killPendAssertRow(t, e, r.ID, before)
			killPendAssertTrail(t, r.ID, true)
		})
	}
}

// TestKillPendingGone: a pending row whose launch has no session finds Gone:
// success with kill_sent false, no kill, every session and the row untouched.
func TestKillPendingGone(t *testing.T) {
	cases := []struct {
		name  string
		spec  killRowSpec
		setup func(t *testing.T, e *killEnv, r *killRow)
		// thenCreate runs the launch's create after the kill (AC-KILL-17, FR1 C7(l)).
		thenCreate bool
	}{
		{"before the launch created its session", killPendSpec(store.StatePending),
			func(t *testing.T, e *killEnv, r *killRow) {
				e.ensureServer(r)
				e.seedBystander(t, r.Socket)
				e.syncServers()
			}, true},
		{"no token, an unlabelled session holds its name",
			killPendSpec(store.StatePending, apitest.WithLaunchIdentity(store.LaunchIdentity{Socket: apitest.TestSocket}),
				apitest.WithNoLaunchStartedAt()),
			func(t *testing.T, e *killEnv, r *killRow) {
				e.seedSession(t, r, tmuxfix.WithRowSessionLabel(tmux.Label{}, false))
			}, false},
		{"another store's session carries this id under its name", killPendSpec(store.StatePending),
			func(t *testing.T, e *killEnv, r *killRow) {
				e.seedSession(t, r, tmuxfix.WithRowSessionLabel(r.otherStore(tmuxfix.OtherToken), true))
			}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedRow(t, tc.spec)
			tc.setup(t, e, &r)
			before, sessions := e.columns(t, r.ID), e.rec.Sessions(r.Socket)
			res, err := e.kill(r.ID)
			if err != nil || res.KillSent {
				t.Fatalf("kill = %+v, %v; want kill_sent false, nil", res, err)
			}
			e.assertKillCalls(t, tmux.CallLookup)
			if got := e.rec.Sessions(r.Socket); !reflect.DeepEqual(got, sessions) {
				t.Errorf("sessions after kill = %+v; want untouched %+v", got, sessions)
			}
			killPendAssertRow(t, e, r.ID, before)
			killPendAssertTrail(t, r.ID, false)
			if !tc.thenCreate {
				return
			}
			if _, err := e.rec.NewSession(r.Socket, r.Name, "/tmp", nil, nil, r.Token, r.ID, r.StoreID); err != nil {
				t.Fatalf("create after kill: %v", err)
			}
			created := false
			for _, s := range e.rec.Sessions(r.Socket) {
				created = created || (s.Name == r.Name && s.Label == r.current())
			}
			if !created {
				t.Errorf("no session %q with the row's label after the create", r.Name)
			}
			killPendAssertRow(t, e, r.ID, before)
		})
	}
}

// TestKillPendingBesideLeftover: a live row (pending of each kind, pending
// with no launch start, or revived to waiting) beside an earlier launch's
// session gets ErrTmuxSessionConflict naming it, and nothing is killed.
func TestKillPendingBesideLeftover(t *testing.T) {
	type leftoverCase struct {
		name    string
		spec    killRowSpec
		renamed bool // the leftover runs under another name than the recorded one
	}
	var cases []leftoverCase
	for _, k := range killPendKinds() {
		cases = append(cases,
			leftoverCase{k.name + ", recorded name", killPendSpec(store.StatePending, k.opts...), false},
			leftoverCase{k.name + ", another name", killPendSpec(store.StatePending, k.opts...), true})
	}
	revived := killPendSpec(store.StateWaiting)
	revived.Agent = agentAlive
	revived.Opts = []apitest.SpawnOption{apitest.WithPID(apitest.TestPanePID + 50),
		apitest.WithProcStarttime(apitest.LinuxProcStarttime)}
	cases = append(cases,
		leftoverCase{"pending with no launch start and no token", killPendSpec(store.StatePending,
			apitest.WithLaunchIdentity(store.LaunchIdentity{Socket: apitest.TestSocket}), apitest.WithNoLaunchStartedAt()), false},
		leftoverCase{"revived to waiting by the leftover's hooks", revived, false})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedRow(t, tc.spec)
			opts := []tmuxfix.RowSessionOption{tmuxfix.WithRowSessionLabel(r.old(), true)}
			if tc.renamed {
				opts = append(opts, tmuxfix.WithRowSessionName("left-"+uuid.NewString()[:8]))
			}
			e.seedSession(t, &r, opts...)
			before, sessions := e.columns(t, r.ID), e.rec.Sessions(r.Socket)
			res, err := e.kill(r.ID)
			if !errors.Is(err, api.ErrTmuxSessionConflict) || res.KillSent {
				t.Fatalf("kill = %+v, %v; want ErrTmuxSessionConflict with kill_sent false", res, err)
			}
			assertOneName(t, err, "ErrTmuxSessionConflict")
			forbid := []string{tmuxfix.OtherToken, r.StoreID}
			if r.Token != "" {
				forbid = append(forbid, r.Token)
			}
			apitest.AssertDescription(t, err.Error(),
				apitest.DescKillLeftover([]apitest.DescSession{{Name: r.Session.Name, ID: r.Session.ID}}), forbid...)
			e.assertKillCalls(t, tmux.CallLookup)
			if got := e.rec.Sessions(r.Socket); !reflect.DeepEqual(got, sessions) {
				t.Errorf("sessions after kill = %+v; want the leftover untouched %+v", got, sessions)
			}
			killPendAssertRow(t, e, r.ID, before)
			killPendAssertTrail(t, r.ID, false)
		})
	}
}
