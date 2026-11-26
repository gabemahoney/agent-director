package api_test

import (
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// b.9ps F1: live-only membership cannot distinguish an orphan from a session
// owned by a finished row. Neither API spelling may claim it is unowned.
func TestFindMissingUnownedSessionsNarrowStore(t *testing.T) {
	t.Parallel()
	for _, canonical := range []bool{false, true} {
		e := newKillEnv(t)
		live := e.seedRow(t, killRowSpec{State: store.StatePending, NoPane: true, NoSession: true,
			Opts: []apitest.SpawnOption{apitest.WithLaunchStartedAt(e.clock.Now().UnixMilli())}})
		for _, state := range []string{store.StateEnded, store.StateMissing} {
			r := e.seedRow(t, killRowSpec{State: state, NoSession: true})
			socket := reportSocket()
			if state == store.StateMissing {
				socket = live.Socket
			}
			e.rec.SeedSessions(socket, tmuxfix.SeedSession{Name: r.ID,
				Label: tmuxfix.Valid(tmuxfix.Token, r.ID, e.st.StoreID())})
		}
		// Even a real orphan cannot be reported without complete membership.
		e.rec.SeedSessions(reportSocket(), tmuxfix.SeedSession{Name: "unknown",
			Label: tmuxfix.Valid(tmuxfix.Token, "unknown", e.st.StoreID())})
		narrow := struct{ api.FindMissingStore }{e.st}
		started := e.clock.Now()
		res := mustSweep(t, narrow, e.pc, fmSweep{canonical: canonical, tmux: e.rec, clock: e.clock})
		if got := unownedOf(t, res); got == nil || len(got) != 0 {
			t.Errorf("canonical=%v unowned_sessions = %v; want non-null empty array without all-status membership", canonical, got)
		}
		assertLists(t, res, nil, nil)
		if len(res.Checks) != 1 || res.Checks[0].State != store.StatePending {
			t.Errorf("checks = %+v; want only the protected live row", res.Checks)
		}
		e.assertNoTmuxCall(t)
		if e.clock.Now() != started {
			t.Error("narrow-store reporting consumed virtual tmux time")
		}
	}
}
