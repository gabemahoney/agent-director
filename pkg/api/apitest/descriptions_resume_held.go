package apitest

import (
	"slices"
	"strconv"
)

// descriptions_resume_held.go holds the shared description helper's overlay
// for resume's and reuse's errors after their create answered "duplicate
// session" (Epics 16 and 17; SR-8.5, SR-10.4, SR-1.4, SR-3.10, SR-4.2),
// selected by HeldName.Restore, whose Launch picks the verb: the restore's
// row sentence (ResumeRestore) in place of "nothing was done" or "nothing
// was written". For reuse, p.Name is the requested name. It applies to the
// existing cases, never a copy:
//
//   - a holder with a foreign, another store's or no valid label:
//     DescHeldDifferentID, DescHeldOtherStore and DescHeldNoValidID, and the
//     ambiguous holder DescHeldAmbiguous, each given p;
//   - an old label (resume's Leftover wording, SR-8.5):
//     DescPreLaunchLeftover(id, the holder under the recorded name)
//     .AfterHeldName(p); DescHeldLeftover is plain spawn's wording only;
//   - the row's own session (the starting-session rule, SR-4.2):
//     DescStillStopping, DescStillStarting and DescOwnOldSession, each
//     .AfterHeldName(p) with p.SessionID empty (they name no tmux id);
//   - the other outcomes: DescSessionCreateFailed with Duplicate (vanished),
//     DescConflictingLabels with NothingWasDone, DescDifferentServer,
//     DescCallTimeout, DescUnrecognisedReply, DescSocketPermission and
//     DescTmuxNotRun, each .AfterHeldName(p).

// resumeRemovedRetry is resume's retry sentence after "duplicate session"
// once the restore found the row removed (b.gu6): a retried resume finds no
// row, and a later spawn of the id starts afresh.
const resumeRemovedRetry = "there is no row left to resume; later, a spawn of the id starts afresh"

// heldRetry is the retry sentence an unanswered or transient case ends with
// after "duplicate session", by the restore's result (b.gu6): "retry later"
// once the restore applied, and for reuse's removed row, whose opted-in retry
// inserts the id afresh; resume's removed row gives resumeRemovedRetry; a row
// that changed or stays pending gives the launch-timeout rule.
func (r ResumeRestore) heldRetry() string {
	switch {
	case r.Outcome == RestoreApplied, r.Outcome == RestoreRowRemoved && r.Launch == LaunchReuse:
		return retryLater
	case r.Outcome == RestoreRowRemoved:
		return resumeRemovedRetry
	}
	return launchRetryRule
}

// afterResumeHeld returns c as an error resume or reuse (p.Restore.Launch)
// returns after "duplicate session" (p.Restore set), its case named after the
// launch: the quoted name the create asked for, p.SessionID when set,
// the label sentence label requires and the restore's row sentence; never
// "nothing was done" or "nothing was written", the abandoned-launch conflict's
// re-issue clause (abandonedReissue, b.1n6), another restore result's
// sentence, "the row stays pending", that the row stays pending or will heal
// unless the restore failed, plain spawn's row sentences ("new row"), the
// reuse opt-in or old-holder sentence, or a label sentence that contradicts
// label. Where c is unanswered or transient it requires the restore's retry
// sentence (ResumeRestore.heldRetry) and no other of "retry later" and the
// launch-timeout rule; elsewhere neither. A must-not phrase the restore
// sentence itself contains (a prior state "ended", "stays pending") is
// dropped. p.Row must be zero and p.BeforeLaunch false.
func (c DescCase) afterResumeHeld(p HeldName, label heldLabel) DescCase {
	if p.Row != 0 || p.BeforeLaunch {
		panic("apitest: HeldName.Restore excludes Row and BeforeLaunch")
	}
	restored := p.Restore.restorePhrase()
	c.Name += ", " + p.Restore.Launch.name() + " after duplicate session"
	req := append(withoutPhrases(c.Require, nothingWasDone, nothingWritten, retryLater, abandonedReissue),
		strconv.Quote(p.Name), restored)
	if p.SessionID != "" {
		req = append(req, p.SessionID)
	}
	if s := label.sentence(); s != "" {
		req = append(req, s)
	}
	mustNot := appendMissing(append([]string(nil), c.MustNot...), nothingWasDone, nothingWritten, rowStaysPending,
		newRow, reuseOptInName, spawnOnlyHolder, retryLater, launchRetryRule, abandonedReissue)
	mustNot = appendMissing(mustNot, heldNotPendingStatements...)
	mustNot = appendMissing(mustNot, label.wrong()...)
	mustNot = appendMissing(mustNot, p.Restore.otherRestorePhrases()...)
	if c.unanswered || c.transient {
		retry := p.Restore.heldRetry()
		req = append(req, retry)
		mustNot = withoutPhrases(mustNot, retry)
	}
	c.Require = req
	c.MustNot = slices.DeleteFunc(mustNot, func(m string) bool { return mustNotPattern(m).MatchString(restored) })
	return c
}
