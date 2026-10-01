package api_test

// pane_verb_reuse_fixture_test.go adds a reuse's launch to the pane-verb
// fixture's pending launch kinds (pane_verb_fixture_test.go; SR-10, SR-20.2,
// AC-PANE-08): a pending row made by a real reuse through the reuse fixture
// (spawn_reuse_fixture_test.go), and a timed-out reuse. It holds no tests.

import (
	"errors"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// seedReusedPending makes a pending row of shape v by a real reuse of a
// finished row whose agent is gone, then drops the reuse's recorded tmux calls
// (Recorder.Reset), so a test's call checks see only its own verb's.
//   - pendingOurs: the labelled create recorded, its pane's agent alive (reusePending);
//   - pendingLostReply: the create made the labelled session but its reply was lost:
//     no server or pane recorded, the token pane's agent alive;
//   - pendingLeftover: the create timed out making nothing (reuseTimesOut), then a
//     session labelled with the earlier life's token came up under the recorded name.
func (e *killEnv) seedReusedPending(t *testing.T, v pendingShape) killRow {
	t.Helper()
	var r reuseRow
	switch v {
	case pendingLostReply:
		e.rec.Script(tmuxfix.AnySocket, tmuxfix.Script{Failure: tmux.FailUnrecognized, Applied: true, Times: 1}, tmux.CallCreate)
		r = e.reusePending(t, agentAlive, reuseRowSpec{}, reuseRequest{})
		e.adoptablePane(&r.killRow)
	case pendingLeftover:
		r = e.seedReusable(t, agentGone, reuseRowSpec{})
		earlier := r.Token
		r = e.reuseTimesOut(t, r, reuseRequest{}, false)
		r.Agent = agentNotRecorded
		e.seedSession(t, &r.killRow, tmuxfix.WithRowSessionLabel(tmuxfix.Valid(earlier, r.ID, r.StoreID), true))
	default:
		r = e.reusePending(t, agentAlive, reuseRowSpec{}, reuseRequest{})
	}
	e.rec.Reset()
	return r.killRow
}

// seedPendingNoSession is k's pending row with shape v and no session: a
// pendingSpec row seeded with NoSession, or a reuse's row with its session
// killed by id (the call dropped with Recorder.Reset).
func (e *killEnv) seedPendingNoSession(t *testing.T, k pendingKind, v pendingShape) killRow {
	t.Helper()
	if k != pendingReused {
		spec := e.pendingSpec(k, v)
		spec.NoSession = true
		return e.seedRow(t, spec)
	}
	r := e.seedReusedPending(t, v)
	if r.Session.ID != "" {
		if err := e.rec.KillSessionID(r.Socket, r.Session.ID); err != nil {
			t.Fatalf("KillSessionID(%s, %s): %v", r.Socket, r.Session.ID, err)
		}
	}
	e.rec.Reset()
	r.Session = tmuxfix.SeedSession{}
	return r
}

// reuseTimesOut reuses r (from seedReusable, its agent gone) with q through
// Client.Spawn, the create timing out (made: it made its labelled session
// anyway), failing unless that is ErrTmuxUnresponsive with the reset row left
// pending; it returns r as the reuse left it (reusedAs).
func (e *killEnv) reuseTimesOut(t *testing.T, r reuseRow, q reuseRequest, made bool) reuseRow {
	t.Helper()
	e.rec.Script(tmuxfix.AnySocket, tmuxfix.Script{Failure: tmux.FailTimeout, Applied: made, Times: 1}, tmux.CallCreate)
	if _, logs, err := e.reuse(t, reuseParams(t, r, q)); !errors.Is(err, api.ErrTmuxUnresponsive) {
		t.Fatalf("reuse of %s = %v (log %q); want ErrTmuxUnresponsive", r.ID, err, logs)
	}
	return e.reusedAs(t, r)
}
