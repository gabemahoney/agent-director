package api_test

// kill_optin_unusable_test.go: kill with the finished-row opt-in on an ended or
// missing row whose recorded name is unusable (SR-3.2) refuses with that
// name's ErrInternal before the socket and the lookup, sending nothing and
// changing nothing (SR-6.5). Without the opt-in a missing such row stays a
// no-op; with it a pending such row keeps the live-row refusal.

import (
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// kouRow seeds a state row recording name, its agent running and no session.
func (e *killEnv) kouRow(t *testing.T, state, name string) killRow {
	t.Helper()
	return e.seedRow(t, killRowSpec{State: state, NoSession: true,
		Opts: []apitest.SpawnOption{apitest.WithTmuxSessionName(name)}})
}

// TestKillIncludeFinishedUnusableName: each unusable name on an ended and a
// missing row gets its ErrInternal with no tmux call or process check, nothing sent or changed.
func TestKillIncludeFinishedUnusableName(t *testing.T) {
	t.Parallel()
	for _, state := range kftStates {
		for _, f := range unusableNameFixtures() {
			t.Run(state+", "+f.label, func(t *testing.T) {
				t.Parallel()
				e := newKillEnv(t)
				r := resumeRow{killRow: e.kouRow(t, state, f.raw)}
				before := e.kftSnap(t, r)

				res, err := e.killOptIn(r.ID)

				if res.KillSent {
					t.Error("kill_sent = true; want false")
				}
				e.kftCheck(t, r, before, res, err, kftOutcome{errName: "ErrInternal", desc: f.desc, lookup: tmux.TokenNotRun})
				if n, m := len(e.pc.StartTimeCalls()), e.pc.EnvReads(); n != 0 || m != 0 {
					t.Errorf("process checker consulted (%d start-time calls, %d env reads); want none", n, m)
				}
			})
		}
	}
}

// TestKillUnusableNameMissingWithoutOptIn: each unusable name on a missing row
// without the opt-in is a no-op success with no tmux call.
func TestKillUnusableNameMissingWithoutOptIn(t *testing.T) {
	t.Parallel()
	for _, f := range unusableNameFixtures() {
		t.Run(f.label, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			r := e.kouRow(t, store.StateMissing, f.raw)
			before := e.columns(t, r.ID)

			res, err := e.kill(r.ID)

			if err != nil || res.KillSent {
				t.Fatalf("kill = %+v, %v; want kill_sent false, nil", res, err)
			}
			e.assertKillCalls(t)
			e.assertRowUnchanged(t, r.ID, before)
			kolAssertCalled(t, r.ID, map[string]any{"include_finished": false, "outcome": "ok",
				"lookup_outcome": tmux.TokenNotRun, "kill_sent": false})
		})
	}
}

// TestKillIncludeFinishedUnusableNamePending: a pending row recording the
// pre-b.gqe default name gets the live-row refusal, not ErrInternal.
func TestKillIncludeFinishedUnusableNamePending(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	r := e.kouRow(t, store.StatePending, preGqeDefaultName)
	before, sessions := e.columns(t, r.ID), e.rec.Sessions(r.Socket)

	res, err := e.killOptIn(r.ID)

	kolAssertRefused(t, e, r, store.StatePending, res, err, before, sessions)
}
