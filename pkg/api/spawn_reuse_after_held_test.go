package api_test

// spawn_reuse_after_held_test.go tests reuse after a plain spawn's held-name
// end write and beside a leftover (SR-9.3, SR-9.4, SR-10.2, SR-11.3; AC-SPN-07,
// AC-SPN-09, AC-FM-18 reuse halves) on the kill fixture: refused while the
// leftover or another row's session holds the name, writing nothing and never
// touching it, and reused once the name is free. Rows and runners:
// spawn_reuse_fixture_test.go.

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// plainSpawnHeld runs a plain spawn of a new caller-supplied id whose create
// meets "duplicate session" from k's holder of the requested name, placed as
// the label scan's lookup returns when late (else before the spawn). It
// returns the row as a reusable one (its cwd, trust directory, socket and
// token), the holder as stored, and the spawn's error.
func (e *killEnv) plainSpawnHeld(t *testing.T, k holderKind, late bool) (reuseRow, tmuxfix.SeedSession, error) {
	t.Helper()
	kr := killRow{ID: "reuse-" + uuid.NewString()[:8], Name: "held-" + uuid.NewString()[:8], Socket: e.defaultSocket,
		StoreID: e.storeID}
	r := reuseRow{resumeRow: resumeRow{killRow: kr, CWD: t.TempDir(), Trust: seedTrustConfig(t, t.TempDir(), trustLacksEntry)}}
	e.ensureServer(&r.killRow)
	e.seedBystander(t, r.Socket)
	e.syncServers()
	holders := e.holderSessions(t, kr, k)
	var placed []tmuxfix.SeedSession
	if !late {
		placed = e.placeHolder(t, kr, k, holders)
	}
	e.rec.AfterCall(tmux.CallLookup, func(tmuxfix.SocketCall, error) {
		if late && placed == nil {
			placed = e.placeHolder(t, kr, k, holders)
		}
	})
	t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", "")
	c, _ := e.client(t)
	_, err := c.Spawn(api.SpawnParams{ClaudeInstanceID: r.ID, TmuxSessionName: r.Name, TmuxSessionNameSupplied: true,
		CWD: r.CWD, ExtraEnv: r.Trust.extraEnv()})
	if len(placed) == 0 {
		t.Fatalf("no holder of %q was placed on %s", r.Name, r.Socket)
	}
	r.Token, _ = e.columns(t, r.ID).LaunchToken.(string)
	return r, placed[0], err
}

// assertReused fails unless err is nil and, since calls, r's row went
// pending in the life after life through one lookup and one create on its
// socket, which made one session carrying the row's new token.
func (e *killEnv) assertReused(t *testing.T, r reuseRow, life any, calls int, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("reuse of %s: %v; want success", r.ID, err)
	}
	cols := e.columns(t, r.ID)
	prior, _ := life.(int64)
	if cols.State != store.StatePending || cols.LifeNumber != any(prior+1) {
		t.Errorf("state, life after the reuse = %v, %v; want pending, %d", cols.State, cols.LifeNumber, prior+1)
	}
	var kinds []tmux.Call
	for _, c := range e.rec.SocketCalls()[calls:] {
		if c.Socket != r.Socket {
			t.Errorf("tmux call %v on %s; want every call on %s", c.Call, c.Socket, r.Socket)
		}
		kinds = append(kinds, c.Call)
	}
	if want := []tmux.Call{tmux.CallLookup, tmux.CallCreate}; !reflect.DeepEqual(kinds, want) {
		t.Errorf("tmux calls = %q; want %q", kinds, want)
	}
	token, _ := cols.LaunchToken.(string)
	labelled := 0
	for _, s := range e.rec.Sessions(r.Socket) {
		if s.Label == tmuxfix.Valid(token, r.ID, e.storeID) {
			labelled++
		}
	}
	if token == r.Token || labelled != 1 {
		t.Errorf("token %q (was %q), %d sessions labelled with it; want a new token on one session", token, r.Token, labelled)
	}
}

// TestSpawnReuseAfterHeldNameEndWrite (AC-SPN-07, AC-SPN-09): after a plain spawn's held-name end write,
// reuse is refused while the holder runs, writing nothing, and succeeds once the name is free.
func TestSpawnReuseAfterHeldNameEndWrite(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID with t.Setenv; it checks every record written to the
	// shared trail since its mark.
	cases := []struct {
		name   string
		holder holderKind
		late   bool // placed as the scan's lookup returns
		desc   func(r reuseRow, h tmuxfix.SeedSession) apitest.DescCase
	}{
		{"leftover carrying this id", holderOld, true, func(r reuseRow, h tmuxfix.SeedSession) apitest.DescCase {
			return apitest.DescPreLaunchLeftover(r.ID, []apitest.DescSession{{Name: h.Name, ID: h.ID}})
		}},
		{"another row's session", holderForeign, false, func(r reuseRow, h tmuxfix.SeedSession) apitest.DescCase {
			return apitest.DescHeldDifferentID(apitest.HeldName{Name: r.Name, SessionID: h.ID, BeforeLaunch: true})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r, holder, err := e.plainSpawnHeld(t, tc.holder, tc.late)
			assertOneSentinel(t, err, api.ErrTmuxSessionConflict)
			cols := e.columns(t, r.ID)
			if sid, _ := cols.ClaudeSessionID.(string); cols.State != store.StateEnded || sid != "" || cols.PID != nil {
				t.Fatalf("row after the plain spawn: state %v, session id %v, pid %v; want ended with neither",
					cols.State, cols.ClaudeSessionID, cols.PID)
			}
			r.Trust.reset(t) // the plain spawn pre-trusted the cwd
			before := e.snapshotReuse(t, r)

			_, _, err = e.reuse(t, reuseParams(t, r, reuseRequest{}))

			assertOneName(t, err, "ErrTmuxSessionConflict")
			forbid := []string{r.Token, e.storeID, holder.Label.Token}
			if other := holder.Label.InstanceID; other != r.ID {
				forbid = append(forbid, other)
			}
			apitest.AssertDescription(t, err.Error(), tc.desc(r, holder), forbid...)
			e.assertWroteNothing(t, before)

			if err := e.rec.KillSessionID(r.Socket, holder.ID); err != nil {
				t.Fatalf("KillSessionID(%s, %s): %v", r.Socket, holder.ID, err)
			}
			calls := len(e.rec.SocketCalls())
			_, _, err = e.reuse(t, reuseParams(t, r, reuseRequest{}))
			e.assertReused(t, r, cols.LifeNumber, calls, err)
		})
	}
}

// TestSpawnReusePendingBesideLeftover (AC-SPN-07, AC-FM-18): a pending row whose launch stopped before its create
// collides; once find-missing marks it missing past grace, reuse is refused while the leftover runs, then succeeds.
func TestSpawnReusePendingBesideLeftover(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	e := newKillEnv(t)
	r := reuseRow{resumeRow: e.seedOnServer(t, killRowSpec{State: store.StatePending, NoPane: true, NoServerIdentity: true,
		NoSession: true, Opts: []apitest.SpawnOption{apitest.WithLaunchStartedAt(e.clock.Now().UnixMilli())}}, e.seedRow)}
	leftover := e.seedHolder(t, r.killRow, holderOld)
	life := e.columns(t, r.ID).LifeNumber

	before := e.snapshotReuse(t, r)
	_, _, err := e.reuse(t, reuseParams(t, r, reuseRequest{}))
	assertOneName(t, err, "ErrInstanceIdCollision")
	if calls := e.rec.SocketCalls()[before.calls:]; len(calls) != 0 {
		t.Errorf("tmux calls on the pending row = %+v; want none", calls)
	}
	e.assertWroteNothing(t, before)

	sessions := e.rec.Sessions(r.Socket)
	e.clock.Advance(hnPast)
	c, _ := e.client(t)
	res, err := c.FindMissing(context.Background())
	if err != nil || !slices.Contains(res.IDs, r.ID) {
		t.Fatalf("FindMissing past grace = %+v, %v; want %s marked", res, err, r.ID)
	}
	if got := e.columns(t, r.ID).State; got != store.StateMissing {
		t.Fatalf("state after the sweep = %v; want missing", got)
	}
	if got := e.rec.Sessions(r.Socket); !reflect.DeepEqual(got, sessions) {
		t.Errorf("sessions changed by the sweep:\n got %+v\nwant %+v", got, sessions)
	}

	before = e.snapshotReuse(t, r)
	_, _, err = e.reuse(t, reuseParams(t, r, reuseRequest{}))
	assertOneName(t, err, "ErrTmuxSessionConflict")
	apitest.AssertDescription(t, err.Error(),
		apitest.DescPreLaunchLeftover(r.ID, []apitest.DescSession{{Name: leftover.Name, ID: leftover.ID}}), r.Token, e.storeID)
	e.assertWroteNothing(t, before)

	if err := e.rec.KillSessionID(r.Socket, leftover.ID); err != nil {
		t.Fatalf("KillSessionID(%s, %s): %v", r.Socket, leftover.ID, err)
	}
	calls := len(e.rec.SocketCalls())
	_, _, err = e.reuse(t, reuseParams(t, r, reuseRequest{}))
	e.assertReused(t, r, life, calls, err)
}
