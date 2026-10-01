package api_test

// resume_ceiling_test.go proves resume's SR-13.2 ceiling Q + C + 2A in virtual
// time on the kill fixture (the Recorder charges every call its full class
// timeout and no pipe-close wait W, which SR-20.6 proves per call elsewhere,
// so this is SR-13.2's 10.9 s less W). Path (ii), the re-lookup after
// "duplicate session", is Task 3's.

import (
	"errors"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// TestResumeCeilingVirtualTime: path (i) (lookup, create whose label fails,
// label by id, kill by id) charges Q + C + 2A, 10.5 s at the defaults.
func TestResumeCeilingVirtualTime(t *testing.T) {
	q, a, _ := ceilDefaults()
	c := config.Default().Tmux.EffectiveCreateTimeout()
	want := q + c + 2*a
	if want != 10500*time.Millisecond {
		t.Fatalf("Q + C + 2A at the defaults = %v; the Epic says 10.5 s", want)
	}
	e := newKillEnv(t)
	r := e.seedResumable(t, rceSettled(e), agentGone)
	e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailLabel, Times: 1}, tmux.CallCreate).
		Script(r.Socket, tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 1}, tmux.CallSetLabel, tmux.CallKillSession)
	start := e.clock.Now()

	_, err := e.resume(r.ID)

	if elapsed := e.clock.Now().Sub(start); elapsed != want {
		t.Errorf("virtual time = %v; want Q + C + 2A = %v", elapsed, want)
	}
	if !errors.Is(err, api.ErrTmuxSessionCreate) {
		t.Fatalf("err = %v; want ErrTmuxSessionCreate", err)
	}
	e.assertKillCalls(t, tmux.CallLookup, tmux.CallCreate, tmux.CallSetLabel, tmux.CallKillSession)
}

// TestResumeCeilingRefusalChargesOnlyLookup: a refusal at the pre-launch
// lookup charges Q and makes no further call.
func TestResumeCeilingRefusalChargesOnlyLookup(t *testing.T) {
	q, _, _ := ceilDefaults()
	cases := []struct {
		name  string
		agent agentState
		setup func(*testing.T, *killEnv, *resumeRow)
		want  error
	}{
		{name: "leftover", agent: agentGone, want: api.ErrTmuxSessionConflict,
			setup: func(t *testing.T, e *killEnv, r *resumeRow) {
				e.seedSession(t, &r.killRow, tmuxfix.WithRowSessionLabel(r.old(), true))
			}},
		{name: "name held by another row", agent: agentGone, want: api.ErrTmuxSessionConflict,
			setup: func(t *testing.T, e *killEnv, r *resumeRow) { e.seedHolder(t, r.killRow, holderForeign) }},
		{name: "own session past the bound", agent: agentGone, want: api.ErrTmuxSessionConflict,
			setup: func(t *testing.T, e *killEnv, r *resumeRow) {
				e.seedSession(t, &r.killRow, e.createdBefore(rceSettled(e)))
			}},
		{name: "agent still stopping", agent: agentAlive, want: api.ErrTmuxUnresponsive},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			age := rceSettled(e)
			if tc.agent == agentAlive {
				age = 0
			}
			r := e.seedResumable(t, age, tc.agent)
			if tc.setup != nil {
				tc.setup(t, e, &r)
			}
			before := e.snapshotResume(t, r)
			start := e.clock.Now()

			_, err := e.resume(r.ID)

			if elapsed := e.clock.Now().Sub(start); elapsed != q {
				t.Errorf("virtual time = %v; want Q = %v", elapsed, q)
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v; want %v", err, tc.want)
			}
			e.assertKillCalls(t, tmux.CallLookup)
			e.assertResumeWroteNothing(t, before)
		})
	}
}

// rceSettled is an age past both the stopping window and the
// starting-session bound of e's [tmux] values.
func rceSettled(e *killEnv) time.Duration {
	return 2 * (e.cfg.EffectiveStoppingWindow() + e.cfg.EffectiveStartingSession())
}
