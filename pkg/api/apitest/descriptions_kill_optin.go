package apitest

import (
	"strconv"
	"time"
)

// descriptions_kill_optin.go holds the shared description helper's SR-1.4
// cases for kill's operator-only finished-row opt-in (SR-6.5, Epic 18): the
// live-row refusal (DescKillOptInLiveRow) and the two "never reported in"
// refusals, an Ours session past both the window and the bound
// (DescKillOptInNeverReportedIn) and a Leftover
// (DescKillOptInNeverReportedInLeftover). The opt-in's spellings, kill as a
// command and the tmux session-ending forms are rejected for every case by
// AssertDescription; a case adds only its own must-nots. The finished-row
// path's other rows reuse DescStillStopping, DescStillStarting,
// DescConflictingLabels, DescDifferentServer and kill's own cases as they
// are.

// DescKillOptInLiveRow is ErrSpawnNotResumable for kill's finished-row opt-in
// on a live row, pending included (SR-1.4, SR-6.5; PRD OQ13): the instance
// id; "the row is live" and its state; that the finished-row option applies
// only to an ended or missing row; that no lookup was made and nothing was
// sent. It names no other command (SR-6.8), such as find-missing or list
// with the session-name param in either spelling, and needs no "Operator
// actions" pointer.
func DescKillOptInLiveRow(instanceID, state string) DescCase {
	return DescCase{
		Name: "ErrSpawnNotResumable, kill's finished-row opt-in on a live row",
		Require: []string{
			instanceID, "the row is live", "(state " + state + ")",
			"the finished-row option applies only to an ended or missing row",
			"no lookup was made and nothing was sent",
		},
		MustNot: []string{"find-missing", "tmux_session_name", "tmux-session-name"},
	}
}

// The phrases both "never reported in" refusals share (SR-1.4, SR-6.5), with
// noKillSent (descriptions_resume_lookup.go).
const (
	neverReportedIn   = "never reported in"
	humanEndsFinished = "send-keys refuses a finished row, so ending the session is a human's decision"
	boundStatement    = "starting-session bound"
)

// neverReportedInMustNot is what neither "never reported in" refusal may
// say: "dead", "gone", that a kill was sent, that the session is still
// stopping or starting, or a live Leftover's "not this launch's session".
var neverReportedInMustNot = []string{"dead", "gone", "a kill was sent", stillStopping, stillStarting, notThisLaunch}

// neverReportedInCase is the part both "never reported in" cases share:
// "this row's own id", "never reported in", that no kill was sent, that
// send-keys refuses a finished row so ending the session is a human's
// decision, "list tmux_session_name" and the "Operator actions" pointer,
// plus the case's own req and mustNot phrases.
func neverReportedInCase(name string, req, mustNot []string) DescCase {
	return DescCase{
		Name:    "ErrTmuxSessionConflict, kill's finished-row opt-in, never reported in, " + name,
		Require: append([]string{thisRowsOwnID, neverReportedIn, noKillSent, humanEndsFinished, listSessionName}, req...),
		MustNot: append(append([]string(nil), neverReportedInMustNot...), mustNot...),
	}.PointsToOperatorActions()
}

// DescKillOptInNeverReportedIn is ErrTmuxSessionConflict for kill's
// finished-row opt-in on an Ours session past the stopping window and the
// starting-session bound that did not report in to this row (SR-1.4 row "a
// finished row's own session that never reported in"; SR-6.5, SR-6.7): the
// instance id, the quoted recorded name and that it has run for at least the
// bound (its effective value in seconds), with neverReportedInCase's phrases.
// Pass any other row's id as forbid.
func DescKillOptInNeverReportedIn(instanceID, name string, bound time.Duration) DescCase {
	return neverReportedInCase("Ours past both",
		[]string{instanceID, strconv.Quote(name), runForAtLeast + inSeconds(bound)}, nil)
}

// DescKillOptInNeverReportedInLeftover is ErrTmuxSessionConflict for kill's
// finished-row opt-in whose lookup is Leftover (SR-6.5; SR-1.4 has no row of
// its own): the instance id; each leftover session's quoted name and tmux id
// as namedSessions gives them (the rest's quoted names must not appear); that
// an earlier launch's session never reported in to this row's latest launch;
// with neverReportedInCase's phrases. No age was judged, so it never states
// the starting-session bound. Pass any other row's id as forbid.
func DescKillOptInNeverReportedInLeftover(instanceID string, sessions []DescSession) DescCase {
	named, unnamed := namedSessions(sessions)
	req := append([]string{instanceID, "an earlier launch's session never reported in to this row's latest launch"}, named...)
	return neverReportedInCase("Leftover", req, append([]string{boundStatement, runForAtLeast}, unnamed...))
}
