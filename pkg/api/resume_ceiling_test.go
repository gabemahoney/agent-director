package api_test

// resume_ceiling_test.go proves resume's SR-13.2 ceiling in virtual time (the
// Recorder charges each call its class timeout, no pipe-close wait W): path
// (i) Q + C + 2A and path (ii), "duplicate session", 2Q + C. At the defaults
// TestResumeRestore, rhdRun and rlkRun charge every path.

import (
	"errors"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestResumeCeilingDuplicateSession: Q + C + 2A is 10.5 s and 2Q + C 8 s at
// the defaults; with Q configured above 2A, through Client.Resume, path (ii)
// charges 2Q + C, more than path (i)'s Q + C + 2A at the same settings.
func TestResumeCeilingDuplicateSession(t *testing.T) {
	t.Parallel()
	q, a, _ := ceilDefaults()
	c := config.Default().Tmux.EffectiveCreateTimeout()
	if p1, p2 := q+c+2*a, 2*q+c; p1 != 10500*time.Millisecond || p2 != 8*time.Second {
		t.Fatalf("Q + C + 2A, 2Q + C at the defaults = %v, %v; the Epic says 10.5 s, 8 s", p1, p2)
	}
	raised := config.Tmux{QueryTimeoutMs: 2*config.DefaultActionTimeoutMs + config.DefaultQueryTimeoutMs}
	rq, written := raised.EffectiveQueryTimeout(), []apitest.TmuxSetting{
		apitest.TmuxInt(config.TmuxQueryTimeoutMs, raised.QueryTimeoutMs)}
	if rq <= 2*a || 2*rq+c <= rq+c+2*a {
		t.Fatalf("raised Q = %v; want above 2A = %v, making path (ii) the larger", rq, 2*a)
	}
	for _, held := range []bool{true, false} {
		t.Run(map[bool]string{true: "path ii", false: "path i"}[held], func(t *testing.T) {
			e := newKillEnv(t)
			e.rec.WithVirtualTime(e.clock, tmux.Timeouts{Query: rq})
			r := e.seedHeldResumable(t, rlkSettled(e), agentGone)
			want, wantErr := rq+c+2*a, api.ErrTmuxSessionCreate
			calls := []tmux.Call{tmux.CallLookup, tmux.CallCreate, tmux.CallSetLabel, tmux.CallKillSession}
			var sc *heldScene
			if held {
				want, wantErr, calls = 2*rq+c, api.ErrTmuxSessionConflict, []tmux.Call{tmux.CallLookup, tmux.CallCreate, tmux.CallLookup}
				sc = e.arrangeHeld(t, r, heldSpec{Holder: holderForeign})
			} else {
				e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailLabel, Times: 1}, tmux.CallCreate).
					Script(r.Socket, tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 1}, tmux.CallSetLabel, tmux.CallKillSession)
			}
			start := e.clock.Now()

			_, _, err := e.resumeClient(t, r.ID, written...)

			if elapsed := e.clock.Now().Sub(start); elapsed != want {
				t.Errorf("virtual time = %v; want %v", elapsed, want)
			}
			if !errors.Is(err, wantErr) {
				t.Fatalf("err = %v; want %v", err, wantErr)
			}
			e.assertKillCalls(t, calls...)
			if sc != nil {
				e.assertHeldRestored(t, sc)
			}
		})
	}
}
