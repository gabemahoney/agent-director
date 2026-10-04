package apitest

import (
	"strconv"
	"time"
)

// descriptions_starting.go holds the shared description helper's SR-1.4 cases
// for the shared starting-session refusal (Epic 16) that resume, reuse and
// kill's finished-row opt-in give a row examined as finished whose session is
// Ours, or Gone while its agent process still runs: the row still stopping
// (DescStillStopping, its session present or not), its session still starting
// (DescStillStarting), and its own old session (DescOwnOldSession). The own
// old session is told apart from a Leftover refusal by its sentinel and by
// its own phrases, never by "this row's own id" alone, which both say. After
// resume's "duplicate session" (the holder is the row's own session) each
// takes DescCase.AfterHeldName with HeldName.Restore.

// StartingSession parameterises the starting-session cases: the row's
// instance id; Name, the session name the caller looked up or asked tmux to
// create (quoted, Go quoted-string form); the effective stopping window and
// starting-session bound; NoSession, Gone while the row's agent process still
// runs (no session of its launch was found); and, for DescOwnOldSession only,
// WindowChecked (the stopping window was checked, so the description states
// that the row ended at least the window ago) and SessionID (the row records a
// session id, so the conversation stays resumable).
type StartingSession struct {
	InstanceID    string
	Name          string
	Window        time.Duration
	Bound         time.Duration
	NoSession     bool
	WindowChecked bool
	SessionID     bool
}

// The starting-session phrases (SR-1.4).
const (
	stillStopping    = "appears to still be stopping"
	stillStarting    = "appears to still be starting"
	noLaunchSession  = "no session of its launch was found"
	agentProcessRuns = "agent process still runs"
	agentNotExited   = "its agent has not yet exited"
	runForAtLeast    = "it has run for at least the starting-session bound of "
	stoppingWindow   = "stopping window"
	mayBeHung        = "the agent may be hung or running on a row wrongly marked finished"
)

// DescStillStopping is ErrTmuxUnresponsive for a row that ended less than the
// stopping window ago (SR-1.4, SR-4.2 step 1): "appears to still be
// stopping"; that the row ended less than the stopping window (its effective
// value in seconds) ago; "retry later"; with its session, the quoted name and
// that its agent has not yet exited; with NoSession, the instance id and that
// its agent process still runs although no session of its launch was found.
// Never "dead" or "gone", never that it is starting or "this row's own id".
func DescStillStopping(p StartingSession) DescCase {
	req := []string{stillStopping, "the row ended less than the " + stoppingWindow + " of " + inSeconds(p.Window) + " ago", retryLater}
	mustNot := append([]string{stillStarting, thisRowsOwnID}, unresponsiveMustNot...)
	name := "ErrTmuxUnresponsive, still stopping"
	if p.NoSession {
		name += ", no session of its launch"
		req = append(req, p.InstanceID, agentProcessRuns, noLaunchSession)
		mustNot = append(mustNot, agentNotExited)
	} else {
		req = append(req, strconv.Quote(p.Name), agentNotExited)
		mustNot = append(mustNot, noLaunchSession)
	}
	return DescCase{Name: name, Require: req, MustNot: mustNot, transient: true}
}

// DescStillStarting is ErrTmuxUnresponsive for an own session younger than
// the starting-session bound (SR-1.4, SR-4.2 step 2): the quoted name,
// "appears to still be starting" and the bound's effective value in seconds.
// Never "dead" or "gone", never the stopping window, that it is stopping or
// "this row's own id". A NoSession p panics: only a session can be young.
func DescStillStarting(p StartingSession) DescCase {
	if p.NoSession {
		panic("apitest: DescStillStarting needs a session")
	}
	return DescCase{
		Name:      "ErrTmuxUnresponsive, still starting",
		Require:   []string{strconv.Quote(p.Name), stillStarting, "less than the starting-session bound of " + inSeconds(p.Bound)},
		MustNot:   append([]string{stillStopping, stoppingWindow, thisRowsOwnID}, unresponsiveMustNot...),
		transient: true,
	}
}

// DescOwnOldSession is ErrTmuxSessionConflict for a row past both the window
// and the bound (SR-1.4 "own old session", SR-4.2 step 3): the instance id;
// the quoted name; "this row's own id"; that it has run for at least the
// bound (seconds), or with NoSession that no session of its launch was found
// while its agent process still runs; with WindowChecked, that the row ended
// at least the window (seconds) ago, and otherwise no statement of when the
// row ended (SRD-RR2 T13); that the agent may be hung or running on a row
// wrongly marked finished, that no automated action on it is safe and that a
// human must look, with the "Operator actions" pointer; with SessionID, that
// the conversation stays resumable (otherwise never "resumable"); "list
// tmux_session_name". Never a Leftover's "not this launch's session", "left
// over from an earlier life" or "nothing was written", nor that it is
// stopping or starting. With the window skipped, "row ended" is forbidden as
// well as "ended", so the check holds where a restore sentence's prior state
// "ended" lifts the latter (afterResumeHeld).
func DescOwnOldSession(p StartingSession) DescCase {
	req := []string{
		p.InstanceID, strconv.Quote(p.Name), thisRowsOwnID,
		mayBeHung, "no automated action on it is safe", "a human must look", listSessionName,
	}
	mustNot := []string{stillStopping, stillStarting, notThisLaunch, scanLeftoverWord, nothingWritten}
	name := "ErrTmuxSessionConflict, own old session"
	if p.NoSession {
		name += ", no session of its launch"
		req = append(req, noLaunchSession, agentProcessRuns)
		mustNot = append(mustNot, runForAtLeast)
	} else {
		req = append(req, runForAtLeast+inSeconds(p.Bound))
		mustNot = append(mustNot, noLaunchSession)
	}
	if p.WindowChecked {
		name += ", window checked"
		req = append(req, "the row ended at least the "+stoppingWindow+" of "+inSeconds(p.Window)+" ago")
	} else {
		name += ", window skipped"
		mustNot = append(mustNot, "ended", "row ended", "ended_at", stoppingWindow)
	}
	if p.SessionID {
		name += ", with a session id"
		req = append(req, "the conversation stays resumable")
	} else {
		mustNot = append(mustNot, "resumable")
	}
	return DescCase{Name: name, Require: req, MustNot: mustNot}.PointsToOperatorActions()
}
