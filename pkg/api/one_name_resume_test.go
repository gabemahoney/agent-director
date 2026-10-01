package api_test

// one_name_resume_test.go holds resume's SR-1.5 one-name rows for its
// pre-launch check (SR-8.2) and its re-lookup after "duplicate session"
// (SR-8.5; Epic 16): one per error each returns, driven through Client.Resume
// on the kill fixture (resume_lookup_fixture_test.go, arrangeHeld) and checked
// by assertOneName through oneNameRows (one_name_per_error_test.go).

import (
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
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

// oneNameResumeHeld is a row that runs Client.Resume on a resumable row whose
// agent is gone and that ended age before the re-lookup, its create answering
// "duplicate session" as arrangeHeld sets up spec.
func oneNameResumeHeld(name, want string, age time.Duration, spec heldSpec) oneNameRow {
	return oneNameRow{name: "resume/after duplicate session: " + name, want: want, run: func(t *testing.T) error {
		e := newKillEnv(t)
		r := e.seedHeldResumable(t, age, agentGone)
		e.arrangeHeld(t, r, spec)
		_, _, err := e.resumeClient(t, r.ID)
		return err
	}}
}

// oneNameResumeHeldRows are resume's errors after "duplicate session" (SR-8.5,
// SR-4.2, SR-3.10): each holder class, the starting-session rule's three
// outcomes, an ambiguous or unreadable re-lookup, tmux not available, and the
// vanished holder.
func oneNameResumeHeldRows() []oneNameRow {
	conflict, unresponsive, unavailable := "ErrTmuxSessionConflict", "ErrTmuxUnresponsive", "ErrTmuxNotAvailable"
	stopping := config.Tmux{}.EffectiveStoppingWindow() / 2
	relookup := func(s tmuxfix.Script) heldSpec { return heldSpec{Holder: holderForeign, Relookup: s} }
	return []oneNameRow{
		oneNameResumeHeld("leftover", conflict, time.Hour, heldSpec{Holder: holderOld}),
		oneNameResumeHeld("a different instance id", conflict, time.Hour, heldSpec{Holder: holderForeign}),
		oneNameResumeHeld("another agent-director store", conflict, time.Hour, heldSpec{Holder: holderOtherStore}),
		oneNameResumeHeld("no valid instance id", conflict, time.Hour, heldSpec{Holder: holderNone}),
		oneNameResumeHeld("conflicting labels", conflict, time.Hour, heldSpec{Holder: holderConflicting}),
		oneNameResumeHeld("own id", conflict, time.Hour, heldSpec{Holder: holderCurrent, Created: time.Hour}),
		oneNameResumeHeld("still stopping", unresponsive, stopping, heldSpec{Holder: holderCurrent, Created: time.Hour}),
		oneNameResumeHeld("still starting", unresponsive, time.Hour, heldSpec{Holder: holderCurrent}),
		oneNameResumeHeld("ambiguous holder", unresponsive, time.Hour, heldSpec{Holder: holderAmbiguous}),
		oneNameResumeHeld("re-lookup timeout", unresponsive, time.Hour, relookup(tmuxfix.Script{Failure: tmux.FailTimeout})),
		oneNameResumeHeld("re-lookup unrecognised reply", unresponsive, time.Hour, relookup(tmuxfix.Script{
			Failure: tmux.FailUnrecognized, FirstLine: callTableFirstLine(), ExitStatus: 1})),
		oneNameResumeHeld("different server", unavailable, time.Hour, heldSpec{Holder: holderForeign, Server: heldServerRebound}),
		oneNameResumeHeld("tmux unavailable", unavailable, time.Hour, relookup(tmuxfix.Script{Failure: tmux.FailUnavailable})),
		oneNameResumeHeld("socket permission", unavailable, time.Hour, relookup(tmuxfix.Script{Failure: tmux.FailSocketDenied})),
		oneNameResumeHeld("holder vanished", "ErrTmuxSessionCreate", time.Hour, heldSpec{Holder: holderVanished}),
	}
}
