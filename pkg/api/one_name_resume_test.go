package api_test

// one_name_resume_test.go holds resume's pre-launch SR-1.5 one-name rows
// (SR-8.2, Epic 16): one per error its pre-launch check returns, each driven
// through Client.Resume on the kill fixture (resume_lookup_fixture_test.go)
// and checked by assertOneName through oneNameRows (one_name_per_error_test.go).

import (
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// oneNameResumeLookup is a row that runs Client.Resume on a resumable row
// that ended age before the rule's instant, its agent in state a, after
// setup (when set) shapes what the pre-launch lookup finds.
func oneNameResumeLookup(name, want string, age time.Duration, a agentState, setup func(t *testing.T, e *killEnv, r *killRow)) oneNameRow {
	return oneNameRow{name: "resume/pre-launch: " + name, want: want, run: func(t *testing.T) error {
		e := newKillEnv(t)
		r := e.seedResumable(t, age, a)
		if setup != nil {
			setup(t, e, &r.killRow)
		}
		_, _, err := e.resumeClient(t, r.ID)
		return err
	}}
}

// oneNameResumeLookupRows are resume's pre-launch refusals (SR-8.2, SR-4.2,
// SR-3.10): the own-id, Leftover, holder and conflicting-labels conflicts,
// still stopping or starting and an unreadable lookup, and tmux not available.
func oneNameResumeLookupRows() []oneNameRow {
	conflict, unresponsive, unavailable := "ErrTmuxSessionConflict", "ErrTmuxUnresponsive", "ErrTmuxNotAvailable"
	session := func(age time.Duration) func(*testing.T, *killEnv, *killRow) {
		return func(t *testing.T, e *killEnv, r *killRow) { e.seedSession(t, r, e.createdBefore(age)) }
	}
	holder := func(k holderKind) func(*testing.T, *killEnv, *killRow) {
		return func(t *testing.T, e *killEnv, r *killRow) { e.seedHolder(t, *r, k) }
	}
	lookup := func(s tmuxfix.Script) func(*testing.T, *killEnv, *killRow) { return scriptKill(tmux.CallLookup, s) }
	return []oneNameRow{
		oneNameResumeLookup("own id, its session", conflict, time.Hour, agentAlive, session(time.Hour)),
		oneNameResumeLookup("own id, no session and the process running", conflict, time.Hour, agentAlive, nil),
		oneNameResumeLookup("leftover", conflict, time.Hour, agentGone, holder(holderOld)),
		oneNameResumeLookup("name held, a different instance id", conflict, time.Hour, agentGone, holder(holderForeign)),
		oneNameResumeLookup("name held, another agent-director store", conflict, time.Hour, agentGone, holder(holderOtherStore)),
		oneNameResumeLookup("name held, no valid instance id", conflict, time.Hour, agentGone, holder(holderNone)),
		oneNameResumeLookup("conflicting labels", conflict, time.Hour, agentAlive, func(_ *testing.T, e *killEnv, r *killRow) {
			e.rec.SetScope(r.Socket, tmuxfix.ScopeGlobal, tmuxfix.ScopeValue{})
		}),
		oneNameResumeLookup("still stopping, its session", unresponsive, 0, agentAlive, session(time.Hour)),
		oneNameResumeLookup("still stopping, no session", unresponsive, 0, agentAlive, nil),
		oneNameResumeLookup("still starting", unresponsive, time.Hour, agentAlive, session(0)),
		oneNameResumeLookup("lookup timeout", unresponsive, time.Hour, agentAlive, lookup(tmuxfix.Script{Failure: tmux.FailTimeout})),
		oneNameResumeLookup("lookup unrecognised reply", unresponsive, time.Hour, agentAlive, lookup(tmuxfix.Script{
			Failure: tmux.FailUnrecognized, FirstLine: callTableFirstLine(), ExitStatus: 1})),
		oneNameResumeLookup("different server", unavailable, time.Hour, agentAlive, rebindServer),
		oneNameResumeLookup("tmux unavailable", unavailable, time.Hour, agentAlive, lookup(tmuxfix.Script{Failure: tmux.FailUnavailable})),
		oneNameResumeLookup("socket permission", unavailable, time.Hour, agentAlive, lookup(tmuxfix.Script{Failure: tmux.FailSocketDenied})),
	}
}
