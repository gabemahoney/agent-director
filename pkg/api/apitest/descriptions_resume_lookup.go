package apitest

import (
	"strconv"
	"time"
)

// descriptions_resume_lookup.go holds the shared description helper's SR-1.4
// cases for resume's pre-launch check (Epic 16; SR-8.2), which refuses before
// any write: the Leftover refusal (DescPreLaunchLeftover), this id's own
// abandoned launch met in its place (DescAbandonedLaunch, b.1n6; reuse's
// lookup and both verbs' re-lookup too) and the overlay
// that makes the held-name holder cases (DescHeldNoValidID,
// DescHeldDifferentID, DescHeldOtherStore, DescHeldAmbiguous, and
// DescHeldLeftover met only defensively) the pre-launch holder refusals,
// selected by HeldName.BeforeLaunch. The check's other refusals reuse
// existing cases unchanged: DescStillStopping, DescStillStarting and
// DescOwnOldSession (descriptions_starting.go); DescConflictingLabels with
// NothingWasDone, DescDifferentServer, DescCallTimeout,
// DescUnrecognisedReply, DescSocketPermission and DescTmuxNotRun. After
// resume's "duplicate session" DescPreLaunchLeftover also words an old
// holder, with DescCase.AfterHeldName (descriptions_resume_held.go).

// The Leftover refusals' consequence sentences: the plain-spawn scan's,
// which creates no row, and the pre-launch check's, whose row exists.
const (
	nothingWritten      = "nothing was written"
	nothingWrittenNoRow = nothingWritten + " and no row was created"
)

// The phrases of the other verbs' refusals that a pre-launch refusal never
// says: "duplicate session" (no create was made), the held-name row
// sentences' "new row" (resume's row is not new) and kill's "no kill was
// sent".
const (
	duplicateSession = "duplicate session"
	newRow           = "new row"
	noKillSent       = "no kill was sent"
)

// earlierLifeCase is an ErrTmuxSessionConflict for sessions left over from an
// earlier life of instanceID, worded as the plain-spawn scan's refusal
// (SR-1.4): the instance id, "left over from an earlier life", each session
// named (namedSessions), written (what was not written), that ending such a
// session is a human's decision with the "Operator actions" pointer, and
// "list tmux_session_name"; never that a row was ended, nor an unnamed
// session's name.
func earlierLifeCase(name, instanceID string, sessions []DescSession, written string) DescCase {
	named, unnamed := namedSessions(sessions)
	req := append([]string{instanceID, scanLeftoverWord, written, "a human's decision", listSessionName}, named...)
	mustNot := append(append([]string(nil), rowEndedStatements...), unnamed...)
	return DescCase{Name: name, Require: req, MustNot: mustNot}.PointsToOperatorActions()
}

// DescPreLaunchLeftover is ErrTmuxSessionConflict for a pre-launch lookup
// whose verdict is Leftover (SR-1.4, SR-8.2; build-lead decision 9): the
// plain-spawn scan's wording (earlierLifeCase) with "nothing was written",
// sessions in the order the description names them (lowest $N first). It
// never says kill's "not this launch's session" or "no kill was sent", the
// spawn-only "no agent-director row described it before this spawn" or "no
// row was created", "duplicate session", nor the starting-session phrases
// (still stopping or starting, may be hung). It does not tell itself apart
// from DescOwnOldSession by "this row's own id", which both may say. Pass any
// other row's id as forbid.
func DescPreLaunchLeftover(instanceID string, sessions []DescSession) DescCase {
	c := earlierLifeCase("ErrTmuxSessionConflict, pre-launch leftover", instanceID, sessions, nothingWritten)
	c.MustNot = appendMissing(c.MustNot, notThisLaunch, noKillSent, spawnOnlyHolder, "no row was created",
		duplicateSession, stillStopping, stillStarting, mayBeHung)
	return c
}

// AbandonedLaunch parameterises DescAbandonedLaunch: the row's instance id;
// the sessions of earlier launches of the id that the description names;
// the effective starting-session bound; PastBound, the youngest of them has
// run for at least the bound; and SessionID, the row records a session id.
type AbandonedLaunch struct {
	InstanceID string
	Sessions   []DescSession
	Bound      time.Duration
	PastBound  bool
	SessionID  bool
}

// The abandoned-launch phrases (b.1n6). abandonedReissue is the conflict's
// re-issue clause, which only a refusal before any write says: after
// "duplicate session" (afterResumeHeld) it must not.
const (
	abandonedLaunch    = "this id's own abandoned launch"
	abandonedUntracked = "launched by agent-director for this id but not tracked by the row, whose latest launch records no session of its own"
	abandonedReissue   = "after which the refused call can be re-issued"
)

// DescAbandonedLaunch is the refusal of sessions of earlier launches of the
// id while the row's latest launch records no session of its own (b.1n6),
// which resume and reuse give at their pre-launch lookup and, with
// DescCase.AfterHeldName, after "duplicate session": the instance id, "this
// id's own abandoned launch", each session named (namedSessions), that
// agent-director launched it for this id but the row does not track it
// (never that the row "no longer tracks" it), and "nothing was done".
// Younger than the bound it is ErrTmuxUnresponsive: "appears to still be
// starting", the bound in seconds and "retry later", never a human. Past the
// bound it is ErrTmuxSessionConflict: that it has run for at least the
// bound, that agent-director ends no launch the row does not track, so
// ending it is a human's decision (the "Operator actions" pointer), then the
// re-issue clause (abandonedReissue; AfterHeldName forbids it instead); with
// SessionID that the conversation stays resumable (otherwise never
// "resumable"); "list tmux_session_name". Neither says "left over from an
// earlier life", the stopping window, "may be hung", "nothing was written",
// "dead" or "gone".
func DescAbandonedLaunch(p AbandonedLaunch) DescCase {
	named, unnamed := namedSessions(p.Sessions)
	req := append([]string{p.InstanceID, abandonedLaunch, abandonedUntracked, nothingWasDone}, named...)
	mustNot := append([]string{scanLeftoverWord, stillStopping, stoppingWindow, mayBeHung, nothingWritten, notThisLaunch,
		spawnOnlyHolder, duplicateSession, "no longer tracks"}, unresponsiveMustNot...)
	mustNot = append(mustNot, unnamed...)
	if !p.PastBound {
		return DescCase{
			Name:      "ErrTmuxUnresponsive, abandoned launch still starting",
			Require:   append(req, stillStarting, "less than the starting-session bound of "+inSeconds(p.Bound), retryLater),
			MustNot:   append(mustNot, "human", OperatorActionsTitle, "for at least", abandonedReissue),
			transient: true,
		}
	}
	req = append(req, "for at least the starting-session bound of "+inSeconds(p.Bound),
		"agent-director ends no launch the row does not track, so ending it is a human's decision",
		abandonedReissue, listSessionName)
	mustNot = append(mustNot, stillStarting, retryLater)
	name := "ErrTmuxSessionConflict, abandoned launch past the bound"
	if p.SessionID {
		name += ", with a session id"
		req = append(req, "the conversation stays resumable")
	} else {
		mustNot = append(mustNot, "resumable")
	}
	return DescCase{Name: name, Require: req, MustNot: mustNot}.PointsToOperatorActions()
}

// beforeLaunch returns c as a holder refusal of a pre-launch lookup (SR-8.2,
// SR-3.10): the quoted name, p.SessionID when set, and "nothing was done";
// for an unanswered case (DescHeldAmbiguous) also "retry later", which a
// conflict never says; never "duplicate session", a held-name row sentence
// ("new row") or retry guidance (reuse_finished, the launch-timeout rule),
// a statement that a row was ended, stays pending or will heal, the plain
// spawn's "no agent-director row described it before this spawn", or a label
// sentence that contradicts label. It requires no label sentence (SR-1.4
// asks none of a pre-launch refusal). p.Row must be zero.
func (c DescCase) beforeLaunch(p HeldName, label heldLabel) DescCase {
	if p.Row != 0 {
		panic("apitest: a pre-launch holder case has no end-write result")
	}
	c.Name += ", before launch"
	req := append(append([]string(nil), c.Require...), strconv.Quote(p.Name), nothingWasDone)
	if p.SessionID != "" {
		req = append(req, p.SessionID)
	}
	mustNot := appendMissing(append([]string(nil), c.MustNot...),
		duplicateSession, newRow, reuseOptInName, launchRetryRule, spawnOnlyHolder)
	mustNot = appendMissing(mustNot, rowEndedStatements...)
	mustNot = appendMissing(mustNot, heldNotPendingStatements...)
	mustNot = appendMissing(mustNot, label.wrong()...)
	if c.unanswered {
		req = append(req, retryLater)
	} else {
		mustNot = appendMissing(mustNot, retryLater)
	}
	c.Require, c.MustNot = req, mustNot
	return c
}
