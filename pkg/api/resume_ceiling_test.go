package api_test

// resume_ceiling_test.go proves resume's SR-13.2 ceiling in virtual time on
// the kill fixture (the Recorder charges every call its full class timeout
// and no pipe-close wait W, which SR-20.6 proves per call elsewhere): path
// (i) Q + C + 2A (SR-13.2's 10.9 s less W) and path (ii), the re-lookup after
// "duplicate session", 2Q + C (SR-13.2's 8.3 s less its three W).

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

// TestResumeCeilingVirtualTime: path (i) (lookup, create whose label fails,
// label by id, kill by id) charges Q + C + 2A, 10.5 s at the defaults.
func TestResumeCeilingVirtualTime(t *testing.T) {
	t.Parallel()
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
	// Serial: it checks every record written to the shared trail since its mark.
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

// TestResumeCeilingDuplicateSession: path (ii) (lookup, create answering
// "duplicate session", re-lookup) charges 2Q + C, 8 s at the defaults, and
// with Q raised above 2A more than path (i)'s Q + C + 2A at the same settings.
func TestResumeCeilingDuplicateSession(t *testing.T) {
	t.Parallel()
	q, a, _ := ceilDefaults()
	c := config.Default().Tmux.EffectiveCreateTimeout()
	if got := 2*q + c; got != 8*time.Second {
		t.Fatalf("2Q + C at the defaults = %v; the Epic says 8 s", got)
	}
	raised := config.Tmux{QueryTimeoutMs: 2*config.DefaultActionTimeoutMs + config.DefaultQueryTimeoutMs}
	rq, written := raised.EffectiveQueryTimeout(), []apitest.TmuxSetting{
		apitest.TmuxInt(config.TmuxQueryTimeoutMs, raised.QueryTimeoutMs)}
	if rq <= 2*a || 2*rq+c <= rq+c+2*a {
		t.Fatalf("raised Q = %v; want above 2A = %v, making path (ii) the larger", rq, 2*a)
	}
	cases := []struct {
		name   string
		q      time.Duration
		config []apitest.TmuxSetting // also written for api.New
		held   bool                  // path (ii); false: path (i), a create whose label fails
	}{
		{"default Q/path ii", q, nil, true},
		{"Q above 2A/path ii", rq, written, true},
		{"Q above 2A/path i", rq, written, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			e.rec.WithVirtualTime(e.clock, tmux.Timeouts{Query: tc.q})
			r := e.seedHeldResumable(t, rceSettled(e), agentGone)
			want, wantErr := tc.q+c+2*a, api.ErrTmuxSessionCreate
			calls := []tmux.Call{tmux.CallLookup, tmux.CallCreate, tmux.CallSetLabel, tmux.CallKillSession}
			var sc *heldScene
			if tc.held {
				want, wantErr, calls = 2*tc.q+c, api.ErrTmuxSessionConflict, []tmux.Call{tmux.CallLookup, tmux.CallCreate, tmux.CallLookup}
				sc = e.arrangeHeld(t, r, heldSpec{Holder: holderForeign})
			} else {
				e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailLabel, Times: 1}, tmux.CallCreate).
					Script(r.Socket, tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 1}, tmux.CallSetLabel, tmux.CallKillSession)
			}
			start := e.clock.Now()

			_, _, err := e.resumeClient(t, r.ID, tc.config...)

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

// rceSettled is an age past both the stopping window and the
// starting-session bound of e's [tmux] values.
func rceSettled(e *killEnv) time.Duration {
	return 2 * (e.cfg.EffectiveStoppingWindow() + e.cfg.EffectiveStartingSession())
}
