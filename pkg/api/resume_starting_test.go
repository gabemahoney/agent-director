package api_test

// resume_starting_test.go tests the starting-session rule at resume's
// pre-launch check (SR-8.2, SR-4.2; AC-RES-03, AC-RES-04, AC-RES-17) on the
// kill fixture: the window before the age, the ended_at read before the move,
// Gone while the agent runs, and every refusal writing nothing. The rule's
// pure boundaries are internal/tmux's; the configured values are
// resume_client_test.go's.

import (
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// The default starting-session bound and stopping window.
var (
	defBound  = secs(config.DefaultStartingSessionSeconds)
	defWindow = secs(config.DefaultStoppingWindowSeconds)
)

// startingRow is a resumable row as the rule sees it at ruleInstant: its
// state, when it ended (endedAgo, negative in the future; noEndedAt for
// NULL), its agent, and its own session's age (noSession: Gone).
type startingRow struct {
	state     string
	endedAgo  time.Duration
	noEndedAt bool
	agent     agentState
	noSession bool
	age       time.Duration
}

// seedStarting seeds s's row, resumable, with its own session unless noSession.
func (e *killEnv) seedStarting(t *testing.T, s startingRow) resumeRow {
	t.Helper()
	spec := killRowSpec{State: s.state, Agent: s.agent, NoSession: true}
	if !s.noEndedAt {
		spec = e.resumableSpec(s.endedAgo, s.agent)
		spec.State = s.state
	}
	r := e.seedResumableRow(t, spec)
	if !s.noSession {
		e.seedSession(t, &r.killRow, e.createdBefore(s.age))
	}
	return r
}

// startingCase is r's starting-session description parameters under bound
// and window.
func (s startingRow) startingCase(r resumeRow, bound, window time.Duration) apitest.StartingSession {
	return apitest.StartingSession{InstanceID: r.ID, Name: r.Name, Window: window, Bound: bound,
		NoSession: s.noSession, WindowChecked: !s.noEndedAt, SessionID: true}
}

// assertStartingRefusal checks err, a resume refused by the rule with want,
// against p's description case, and that nothing was written since before.
func (e *killEnv) assertStartingRefusal(t *testing.T, before resumeSnapshot, err error, want tmux.StartingSessionOutcome, p apitest.StartingSession) {
	t.Helper()
	name, desc := "ErrTmuxUnresponsive", apitest.DescCase{}
	switch want {
	case tmux.StillStopping:
		desc = apitest.DescStillStopping(p)
	case tmux.StillStarting:
		desc = apitest.DescStillStarting(p)
	default:
		name, desc = "ErrTmuxSessionConflict", apitest.DescOwnOldSession(p)
	}
	assertOneName(t, err, name)
	apitest.AssertDescription(t, err.Error(), desc, before.r.Token, before.r.StoreID)
	e.assertResumeWroteNothing(t, before)
}

// resumeStarting seeds s's row on a new kill fixture, resumes it and checks
// the refusal is want's at the default bound and window.
func resumeStarting(t *testing.T, s startingRow, want tmux.StartingSessionOutcome) {
	t.Helper()
	e := newKillEnv(t)
	r := e.seedStarting(t, s)
	before := e.snapshotResume(t, r)
	_, err := e.resume(r.ID)
	e.assertStartingRefusal(t, before, err, want, s.startingCase(r, defBound, defWindow))
}

// TestResumeStartingSessionMatrix: ended and missing rows, ended window-1 s or
// the window ago, with a young or old own session; the window decides first (AC-RES-17).
func TestResumeStartingSessionMatrix(t *testing.T) {
	ended := []struct {
		name   string
		ago    time.Duration
		inside bool
	}{{"ended window-1 s ago", defWindow - time.Second, true}, {"ended the window ago", defWindow, false}}
	sessions := []struct {
		name  string
		age   time.Duration
		young bool
	}{{"session bound-1 s old", defBound - time.Second, true}, {"session the bound old", defBound, false}}
	for _, state := range []string{store.StateEnded, store.StateMissing} {
		for _, en := range ended {
			for _, s := range sessions {
				want := tmux.PastBoth
				switch {
				case en.inside:
					want = tmux.StillStopping
				case s.young:
					want = tmux.StillStarting
				}
				t.Run(state+", "+en.name+", "+s.name, func(t *testing.T) {
					resumeStarting(t, startingRow{state: state, endedAgo: en.ago, age: s.age}, want)
				})
			}
		}
	}
}

// TestResumeStartingSessionCases: the bound past the window, a future or NULL
// ended_at, a row with no pid, and Gone while the agent runs (AC-RES-03, AC-RES-17).
func TestResumeStartingSessionCases(t *testing.T) {
	longAgo := defWindow + defBound
	cases := []struct {
		name string
		row  startingRow
		want tmux.StartingSessionOutcome
	}{
		{"outside the window, session bound-1 s old",
			startingRow{state: store.StateEnded, endedAgo: longAgo, age: defBound - time.Second}, tmux.StillStarting},
		{"outside the window, session the bound old",
			startingRow{state: store.StateEnded, endedAgo: longAgo, age: defBound}, tmux.PastBoth},
		{"ended_at in the future, old session",
			startingRow{state: store.StateEnded, endedAgo: -time.Second, age: longAgo}, tmux.StillStopping},
		{"no ended_at, session bound-1 s old",
			startingRow{state: store.StateMissing, noEndedAt: true, age: defBound - time.Second}, tmux.StillStarting},
		{"no ended_at, session the bound old",
			startingRow{state: store.StateMissing, noEndedAt: true, age: defBound}, tmux.PastBoth},
		{"session id but no pid, inside the window, old session",
			startingRow{state: store.StateEnded, endedAgo: defWindow - time.Second, agent: agentNotRecorded, age: longAgo},
			tmux.StillStopping},
		{"Gone, agent running, inside the window",
			startingRow{state: store.StateEnded, endedAgo: defWindow - time.Second, noSession: true}, tmux.StillStopping},
		{"Gone, agent running, outside the window",
			startingRow{state: store.StateMissing, endedAgo: defWindow, noSession: true}, tmux.PastBoth},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedStarting(t, tc.row)
			cols := e.columns(t, r.ID)
			if tc.row.noEndedAt && cols.EndedAt != nil {
				t.Fatalf("precondition: ended_at = %v; want NULL", cols.EndedAt)
			}
			if tc.row.agent == agentNotRecorded && cols.PID != nil {
				t.Fatalf("precondition: pid = %v; want NULL", cols.PID)
			}
			before := e.snapshotResume(t, r)
			_, err := e.resume(r.ID)
			e.assertStartingRefusal(t, before, err, tc.want, tc.row.startingCase(r, defBound, defWindow))
		})
	}
}

// TestResumeFutureCreationTimeCrossesBound: a session created in the future is
// young, and stepping the virtual clock carries the same row past the bound (AC-RES-04).
func TestResumeFutureCreationTimeCrossesBound(t *testing.T) {
	e := newKillEnv(t)
	row := startingRow{state: store.StateEnded, endedAgo: defWindow + defBound, age: -time.Second}
	r := e.seedStarting(t, row)
	created := time.Unix(r.Session.Created, 0)
	steps := []struct {
		name string
		age  time.Duration // at the rule's instant
		want tmux.StartingSessionOutcome
	}{
		{"created 1 s in the future", -time.Second, tmux.StillStarting},
		{"bound-1 s old", defBound - time.Second, tmux.StillStarting},
		{"the bound old", defBound, tmux.PastBoth},
	}
	for _, s := range steps {
		t.Run(s.name, func(t *testing.T) {
			e.clock.Advance(created.Add(s.age).Sub(e.ruleInstant()))
			before := e.snapshotResume(t, r)
			_, err := e.resume(r.ID)
			e.assertStartingRefusal(t, before, err, s.want, row.startingCase(r, defBound, defWindow))
		})
	}
}
