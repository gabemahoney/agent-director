package apitest

import (
	"slices"
	"strconv"

	"github.com/gabemahoney/agent-director/internal/tmux"
)

// descriptions_held.go holds the shared description helper's SR-1.4 cases
// for a plain spawn whose session-creating call answered "duplicate session"
// (SR-9.4, SR-3.10; PO 2026-09-27 HELD): the overlay every such error carries
// (DescCase.AfterHeldName: the quoted requested name, the holder's tmux id,
// and exactly one row sentence by the end write's result, in place of
// "nothing was done", never "retry later", and on an ErrTmuxUnresponsive or
// a vanished holder's ErrTmuxSessionCreate the retry sentence by the same
// result, WD 2026-09-30d (a), b.1qq, the vanished holder's also naming the
// instance id), the four holder
// conflicts (DescHeldLeftover,
// DescHeldNoValidID, DescHeldDifferentID, DescHeldOtherStore), which also
// state whether the holder's label names this instance id, and the
// ambiguous holder (DescHeldAmbiguous). The re-lookup's other outcomes reuse
// the existing cases with AfterHeldName: DescSessionCreateFailed with
// Duplicate (the name vanished), DescConflictingLabels with NothingWasDone,
// DescDifferentServer, DescCallTimeout, DescUnrecognisedReply,
// DescSocketPermission and DescTmuxNotRun. DescSpawnHeldName and
// DescSpawnSessionNameParam are the same contract as spawn's manifest texts
// state it. With HeldName.BeforeLaunch, the DescHeld* cases are instead the
// holder refusals of a launch's pre-launch check (SR-8.2), without the
// overlay (descriptions_resume_lookup.go); with HeldName.Restore, resume's
// errors after "duplicate session" (descriptions_resume_held.go).

// HeldRow is the result of a plain spawn's conditional end write after
// "duplicate session" (SR-9.4, Appendix F.4), which picks the description's
// one row sentence.
type HeldRow int

// The end write's results.
const (
	HeldRowEnded      HeldRow = iota + 1 // applied: the new row was ended
	HeldRowLeftAsIs                      // not applied: the row changed after the insert
	HeldRowStoreError                    // store error: the row stays pending
)

// heldRowSentences are the row sentences of SR-1.4's "Every error a plain
// spawn returns after duplicate session" row, one per HeldRow.
var heldRowSentences = map[HeldRow]string{
	HeldRowEnded:      "the new row was ended",
	HeldRowLeftAsIs:   "the new row changed after this spawn inserted it and was left as it is",
	HeldRowStoreError: "the new row could not be ended and stays pending",
}

// The label sentences of the same row: whether the holder's label names this
// agent's instance id (never the id it names).
const (
	heldLabelNames    = "its label names this instance id"
	heldLabelNotNames = "its label does not name this instance id"
)

// heldLabel is the label sentence a held-name case requires: none (no single
// holder, or its label cannot be trusted), or one of the two above.
type heldLabel int

const (
	heldLabelNoClaim heldLabel = iota
	heldLabelNamesThisID
	heldLabelNotThisID
)

// The reuse opt-in's one spelling in advice every surface shows (b.c4u): its
// MCP and TypeScript name, then its CLI flag. reuseOptIn is how every
// plain-spawn retry sentence names it (the explicit id's launch timeout, and
// after "duplicate session" the unanswered re-lookup's, the vanished
// holder's and the unended row's); no resume, reuse or pre-launch
// description names reuseOptInName at all.
const (
	reuseOptInName     = "reuse_finished"
	reuseOptInSpelling = reuseOptInName + " (--reuse-finished on the CLI)"
	reuseOptIn         = "the reuse opt-in " + reuseOptInSpelling
)

// heldRetryFree is the retry guidance of SR-1.4's "after duplicate session"
// row for an ErrTmuxUnresponsive (the re-lookup could not answer), in place
// of "retry later", which no held-name description says, and for the
// ErrTmuxSessionCreate of a holder that vanished first (b.1qq): once the end
// write applied, a retry uses the reuse opt-in once the name is free;
// otherwise the launch-timeout rule (launchRetryRule) followed by the same
// opted-in retry, since a plain spawn of the id collides with its row once it
// is finished.
var heldRetryFree = []string{reuseOptIn, "once the name is free"}

// retryLater is the default retry sentence a held-name description replaces.
const retryLater = "retry later"

// heldNotPendingStatements are statements that the row stays pending or will
// heal, which a held-name description makes only for HeldRowStoreError.
var heldNotPendingStatements = []string{"stays pending", "will heal"}

// HeldName parameterises the held-name cases: Name is the requested session
// name; SessionID is the holder's tmux id ($N) when the re-lookup found one
// session holding it ("" otherwise: vanished, ambiguous, unreadable, tmux
// unavailable); Row is the end write's result. BeforeLaunch selects the
// holder found by a pre-launch lookup instead (resume, SR-8.2; see
// BeforeLaunch's overlay): Name is then the recorded name, SessionID the
// holder's tmux id, and Row stays zero. Restore selects resume's or reuse's
// (Restore.Launch) error after "duplicate session" instead (SR-8.5, SR-10.4;
// afterResumeHeld): Name is then the name the create asked for (resume's
// recorded name, reuse's requested name), SessionID the holder's tmux id,
// Restore the restore's result, and Row stays zero. InstanceID is plain
// spawn's new row's id, which its vanished holder's description
// (DescSessionCreateFailed with Duplicate) names as "instance <id>" (b.1qq);
// left empty, that case requires the "instance " lead alone. The other cases
// ignore it.
type HeldName struct {
	Name         string
	SessionID    string
	Row          HeldRow
	BeforeLaunch bool
	Restore      ResumeRestore
	InstanceID   string
}

// AfterHeldName returns c as an error a plain spawn returns after "duplicate
// session" (SR-1.4, SR-9.4) whose holder's label is not judged: the quoted
// requested name, p.SessionID when set, and exactly p.Row's row sentence, in
// place of "nothing was done", which it must not say; no label sentence;
// never that the row stays pending or will heal unless the end write failed;
// a statement that the row was ended only when it was; never "retry later".
// An unanswered case (DescCallTimeout, DescUnrecognisedReply,
// DescHeldAmbiguous) and the vanished holder (DescSessionCreateFailed with
// Duplicate) also require the retry guidance by p.Row: for HeldRowEnded the
// reuse opt-in (reuseOptIn) "once the name is free" and not the
// launch-timeout rule; otherwise the launch-timeout rule followed by the same
// opted-in retry (b.1qq, b.c4u). Any other case states neither. The vanished
// holder also requires "instance " and p.InstanceID, the id that retry
// sentence's "this id" means (b.1qq). Use it on
// DescSessionCreateFailed (Duplicate), DescConflictingLabels
// (NothingWasDone), DescDifferentServer, DescCallTimeout,
// DescUnrecognisedReply, DescSocketPermission and DescTmuxNotRun; the
// DescHeld* cases apply it themselves. With p.Restore set it is resume's
// overlay instead (afterResumeHeld), also for DescPreLaunchLeftover,
// DescStillStopping, DescStillStarting and DescOwnOldSession.
func (c DescCase) AfterHeldName(p HeldName) DescCase {
	if p.Restore.Outcome != RestoreNone {
		return c.afterResumeHeld(p, heldLabelNoClaim)
	}
	return c.afterHeldName(p, heldLabelNoClaim)
}

// afterHeldName is AfterHeldName with the label sentence label requires.
func (c DescCase) afterHeldName(p HeldName, label heldLabel) DescCase {
	row, ok := heldRowSentences[p.Row]
	if !ok {
		panic("apitest: HeldName with no end-write result")
	}
	c.Name += ", after duplicate session"
	req := slices.DeleteFunc(append([]string(nil), c.Require...), func(s string) bool {
		return s == nothingWasDone || s == retryLater
	})
	req = append(req, strconv.Quote(p.Name), row)
	if p.SessionID != "" {
		req = append(req, p.SessionID)
	}
	mustNot := append([]string(nil), c.MustNot...)
	if p.Row == HeldRowEnded {
		mustNot = slices.DeleteFunc(mustNot, func(s string) bool { return slices.Contains(rowEndedStatements, s) })
	} else {
		mustNot = appendMissing(mustNot, rowEndedStatements...)
	}
	if p.Row != HeldRowStoreError {
		mustNot = appendMissing(mustNot, heldNotPendingStatements...)
	}
	for _, r := range []HeldRow{HeldRowEnded, HeldRowLeftAsIs, HeldRowStoreError} {
		if r != p.Row {
			mustNot = appendMissing(mustNot, heldRowSentences[r])
		}
	}
	switch {
	case (c.unanswered || c.heldRetry) && p.Row == HeldRowEnded:
		req = append(req, heldRetryFree...)
		mustNot = appendMissing(mustNot, launchRetryRule)
	case c.unanswered || c.heldRetry:
		req = append(append(req, launchRetryRule), heldRetryFree...)
	default:
		mustNot = appendMissing(mustNot, launchRetryRule, heldRetryFree[1])
	}
	if c.heldRetry {
		req = append(req, "instance "+p.InstanceID)
	}
	if s := label.sentence(); s != "" {
		req = append(req, s)
	}
	mustNot = appendMissing(mustNot, label.wrong()...)
	c.Require = req
	c.MustNot = appendMissing(mustNot, nothingWasDone, rowStaysPending, retryLater)
	return c
}

// sentence is the label sentence label requires ("" for none).
func (label heldLabel) sentence() string {
	switch label {
	case heldLabelNamesThisID:
		return heldLabelNames
	case heldLabelNotThisID:
		return heldLabelNotNames
	}
	return ""
}

// wrong is the label sentences a description with label must not say: the
// other one, or both when it makes no claim.
func (label heldLabel) wrong() []string {
	switch label {
	case heldLabelNamesThisID:
		return []string{heldLabelNotNames}
	case heldLabelNotThisID:
		return []string{heldLabelNames}
	}
	return []string{heldLabelNames, heldLabelNotNames}
}

// holderOverlay applies p's overlay: the pre-launch one (beforeLaunch) when
// p.BeforeLaunch, resume's after "duplicate session" (afterResumeHeld) when
// p.Restore is set, else plain spawn's (afterHeldName).
func (c DescCase) holderOverlay(p HeldName, label heldLabel) DescCase {
	switch {
	case p.BeforeLaunch && p.Restore.Outcome != RestoreNone:
		panic("apitest: HeldName.BeforeLaunch excludes Restore")
	case p.BeforeLaunch:
		return c.beforeLaunch(p, label)
	case p.Restore.Outcome != RestoreNone:
		return c.afterResumeHeld(p, label)
	}
	return c.afterHeldName(p, label)
}

// appendMissing appends each of ps not already in list.
func appendMissing(list []string, ps ...string) []string {
	for _, p := range ps {
		if !slices.Contains(list, p) {
			list = append(list, p)
		}
	}
	return list
}

// withoutOperatorActions returns c also requiring the pointer's absence: the
// conflicts for another row's or another store's session point nowhere
// (SR-1.4).
func (c DescCase) withoutOperatorActions() DescCase {
	c.MustNot = append(append([]string(nil), c.MustNot...), strconv.Quote(OperatorActionsTitle))
	return c
}

// heldConflict is an ErrTmuxSessionConflict for a single holder of the
// requested name of class class: its case words (tmux.LabelClass.CaseWords),
// req and "list tmux_session_name", with p's overlay (holderOverlay) and,
// after "duplicate session", the label sentence.
func heldConflict(class tmux.LabelClass, p HeldName, label heldLabel, req ...string) DescCase {
	if p.SessionID == "" {
		panic("apitest: held-name holder case with no SessionID")
	}
	words := class.CaseWords()
	return DescCase{
		Name:    "ErrTmuxSessionConflict, held name, " + words,
		Require: append([]string{words, listSessionName}, req...),
	}.holderOverlay(p, label)
}

// spawnOnlyHolder is the old holder's sentence only a plain spawn's
// held-name conflict says (SR-1.4): its row is new, so no row described the
// holder.
const spawnOnlyHolder = "no agent-director row described it before this spawn"

// DescHeldLeftover is ErrTmuxSessionConflict for a name held by a session
// whose label names the new row's id with an earlier launch's token (SR-1.4;
// GAP 3; LFR S7): "left over from an earlier life", "its label names this
// instance id", that no agent-director row described it before this spawn,
// the holder's tmux id, that ending it is a human's decision with the
// "Operator actions" pointer, and "list tmux_session_name". Pass any other
// row's id as forbid. With BeforeLaunch (a pre-launch holder, which the
// lookup makes Leftover, so met only defensively) it never says that no row
// described it. A Restore p panics: resume words an old holder after
// "duplicate session" as its Leftover (DescPreLaunchLeftover with
// AfterHeldName).
func DescHeldLeftover(p HeldName) DescCase {
	if p.Restore.Outcome != RestoreNone {
		panic("apitest: resume's old holder is DescPreLaunchLeftover(...).AfterHeldName(p)")
	}
	req := []string{"a human's decision"}
	if !p.BeforeLaunch {
		req = append([]string{spawnOnlyHolder}, req...)
	}
	return heldConflict(tmux.ClassOld, p, heldLabelNamesThisID, req...).PointsToOperatorActions()
}

// DescHeldNoValidID is ErrTmuxSessionConflict for a name held by a session
// with no valid label (SR-1.4, SR-3.10): "no valid instance id", "its label
// does not name this instance id" (agent-director reads only the label,
// SR-3.12), the "Operator actions" pointer and "list tmux_session_name".
// Pass any id read from the session (its environment, a malformed label's)
// as forbid.
func DescHeldNoValidID(p HeldName) DescCase {
	return heldConflict(tmux.ClassNone, p, heldLabelNotThisID).PointsToOperatorActions()
}

// DescHeldDifferentID is ErrTmuxSessionConflict for a name held by another
// row's session (a foreign label; SR-1.4, SR-3.10): "a different instance
// id", "its label does not name this instance id", that the session is
// another row's agent and must not be ended, "list tmux_session_name", and
// no "Operator actions" pointer. Pass the other id as forbid.
func DescHeldDifferentID(p HeldName) DescCase {
	return heldConflict(tmux.ClassForeign, p, heldLabelNotThisID,
		"another row's agent and must not be ended").withoutOperatorActions()
}

// DescHeldOtherStore is ErrTmuxSessionConflict for a name held by a session
// of another agent-director store (SR-1.4, SR-3.10; WD 2026-09-29 STORE):
// "another agent-director store", "its label does not name this instance id"
// (even when it names the same id), that the session is another store's
// agent and must not be ended, "list tmux_session_name", and no
// "Operator actions" pointer. storeID is this store's id: neither it nor the
// other store's id (OtherStoreID) may appear. Pass any other id read from the
// session as forbid.
func DescHeldOtherStore(p HeldName, storeID string) DescCase {
	c := heldConflict(tmux.ClassOtherStore, p, heldLabelNotThisID,
		"store's agent and must not be ended").withoutOperatorActions()
	c.Forbid = append(append([]string(nil), c.Forbid...), storeID, OtherStoreID(storeID))
	return c
}

// DescHeldAmbiguous is ErrTmuxUnresponsive for a re-lookup in which more
// than one listing entry matches the requested name, so its holder cannot be
// told (SR-3.10): the quoted name, "more than one tmux session's name
// matches it", the row sentence, the retry guidance by p.Row (see
// AfterHeldName) and "list tmux_session_name"; no label sentence; never
// "dead" or "gone". p.SessionID must be empty: pass the
// matching sessions' tmux ids as forbid. With BeforeLaunch it requires
// "nothing was done" and "retry later" instead of a row sentence and retry
// guidance; with Restore, the restore's sentence and "retry later".
func DescHeldAmbiguous(p HeldName) DescCase {
	if p.SessionID != "" {
		panic("apitest: DescHeldAmbiguous names no holder")
	}
	req := []string{"more than one tmux session's name matches it", listSessionName}
	if p.Restore.Outcome != RestoreNone {
		req = append(req, retryLater)
	}
	return DescCase{
		Name:       "ErrTmuxUnresponsive, held name, ambiguous holder",
		Require:    req,
		MustNot:    unresponsiveMustNot,
		unanswered: true,
	}.holderOverlay(p, heldLabelNoClaim)
}

// DescSpawnHeldName is the held-name contract as the spawn manifest
// description states it (SR-9.4, SR-18; WD 2026-09-29 STORE): a held name
// ends the new row at once, ErrTmuxSessionConflict names the holder, the
// class statement, another row's or store's session must not be ended, the
// "Operator actions" pointer and spawning the id again with the reuse opt-in
// in its one spelling (reuseOptInSpelling, b.c4u). It never quotes a
// held-name error's row sentence. Check it with AssertAgentTextCase.
func DescSpawnHeldName() DescCase {
	return DescCase{
		Name: "spawn manifest, held name",
		Require: []string{
			"already held", "ends its new row at once", "ErrTmuxSessionConflict naming the holder",
			"holder vanished", "ErrTmuxSessionConflict is CONFLICT (permanent until a human looks)",
			"another row's", "another agent-director store", "must not be ended", "no valid instance id",
			strconv.Quote(OperatorActionsTitle), "spawn the id again with " + reuseOptInSpelling,
		},
		MustNot: []string{
			heldRowSentences[HeldRowEnded], heldRowSentences[HeldRowLeftAsIs], heldRowSentences[HeldRowStoreError],
		},
	}
}

// DescSpawnSessionNameParam is spawn's tmux_session_name parameter text on a
// held name (SR-9.4): it ends the new row and names ErrTmuxSessionConflict,
// never the old claim that a live collision surfaces as the wrapped tmux
// new-session error. Check it with AssertAgentTextCase.
func DescSpawnSessionNameParam() DescCase {
	return DescCase{
		Name:    "spawn manifest, tmux_session_name param, held name",
		Require: []string{"already held", "ends the new row at once", "ErrTmuxSessionConflict"},
		MustNot: []string{"live-collision", "wrapped tmux new-session error"},
	}
}
