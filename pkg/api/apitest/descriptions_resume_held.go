package apitest

import (
	"slices"
	"strconv"
)

// descriptions_resume_held.go holds the shared description helper's overlay
// for resume's errors after its create answered "duplicate session" (Epic 16;
// SR-8.5, SR-1.4, SR-3.10, SR-4.2), selected by HeldName.Restore: the
// restore's row sentence (ResumeRestore) in place of "nothing was done" or
// "nothing was written". It applies to the existing cases, never a copy:
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

// afterResumeHeld returns c as an error resume returns after "duplicate
// session" (p.Restore set): the quoted recorded name, p.SessionID when set,
// the label sentence label requires and the restore's row sentence; never
// "nothing was done" or "nothing was written", another restore result's
// sentence, "the row stays pending", that the row stays pending or will heal
// unless the restore failed, plain spawn's row sentences ("new row"), retry
// guidance (reuse_finished, the launch-timeout rule) or old-holder sentence,
// or a label sentence that contradicts label; "retry later" only where c is
// unanswered or transient (resume keeps the default retry). A
// must-not phrase the restore sentence itself contains (a prior state
// "ended", "stays pending") is dropped. p.Row must be zero and
// p.BeforeLaunch false.
func (c DescCase) afterResumeHeld(p HeldName, label heldLabel) DescCase {
	if p.Row != 0 || p.BeforeLaunch {
		panic("apitest: HeldName.Restore excludes Row and BeforeLaunch")
	}
	restored := p.Restore.restorePhrase()
	c.Name += ", resume after duplicate session"
	req := append(withoutPhrases(c.Require, nothingWasDone, nothingWritten), strconv.Quote(p.Name), restored)
	if p.SessionID != "" {
		req = append(req, p.SessionID)
	}
	if s := label.sentence(); s != "" {
		req = append(req, s)
	}
	mustNot := appendMissing(append([]string(nil), c.MustNot...), nothingWasDone, nothingWritten, rowStaysPending,
		newRow, heldRetryReuse[0], launchRetryRule, spawnOnlyHolder)
	mustNot = appendMissing(mustNot, heldNotPendingStatements...)
	mustNot = appendMissing(mustNot, label.wrong()...)
	mustNot = appendMissing(mustNot, p.Restore.otherRestorePhrases()...)
	if !c.unanswered && !c.transient {
		mustNot = appendMissing(mustNot, retryLater)
	}
	c.Require = req
	c.MustNot = slices.DeleteFunc(mustNot, func(m string) bool { return mustNotPattern(m).MatchString(restored) })
	return c
}
