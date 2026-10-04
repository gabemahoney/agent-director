package api

import (
	"fmt"
	"strconv"
	"time"

	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// This file holds the shared held-name outcome of a launch whose
// session-creating call answered "duplicate session" (SR-1.2, SR-1.4,
// SR-3.10, SR-9.4): the re-lookup's classified verb error and the holder
// facts its ad.launch.name_held record needs. It is verb-agnostic: plain
// spawn passes one of its three row sentences, its retry sentences below
// (heldRetrySentences) and no examined row; resume and reuse
// (finishedLaunch.heldName) pass their restore's sentence, retryLater for an
// unanswered re-lookup and the row they examined before their move or reset
// (heldExaminedRow).
// resume's pre-launch check (resume_lookup.go) builds its holder conflicts
// with the same class wording (heldHolderError, ambiguousHolderError) under
// heldBeforeLaunch, which makes no "duplicate session" claim.

// Plain spawn's row sentences after "duplicate session" (SR-1.4 row "Every
// error a plain spawn returns after duplicate session"; SR-5.8, SR-9.4), one
// per result of its conditional end write. Each held-name description
// carries exactly one of them, in place of the Can't tell "nothing was done".
const (
	// heldRowEnded: the end write applied.
	heldRowEnded = "the new row was ended"
	// heldRowLeftAsIs: the end write did not apply, because the row changed
	// after the insert (another versioned write, or deleted and inserted
	// afresh).
	heldRowLeftAsIs = "the new row changed after this spawn inserted it and was left as it is"
	// heldRowStaysPending: the end write failed with a store error.
	heldRowStaysPending = "the new row could not be ended and stays pending"
)

// Plain spawn's retry sentences for an ErrTmuxUnresponsive after "duplicate
// session" (the re-lookup could not answer: a timeout, an unrecognised reply,
// or more than one session matching the name), in place of "retry later",
// and for the ErrTmuxSessionCreate of a holder that vanished before the
// re-lookup (SR-1.4 row "Every error a plain spawn returns after duplicate
// session"; WD 2026-09-30d (a); b.1qq). A plain spawn of the same id cannot
// simply be retried: its row is already ended, or may still be pending, and
// once finished it collides with a plain spawn of the id
// (ErrInstanceIdCollision), so each names the opted-in retry.
const (
	// heldRetryFree: the end write applied, and either the re-lookup could
	// not answer or the holder vanished, so a plain spawn of the id now
	// collides with its ended row: the opted-in retry once the name is free,
	// the opt-in in its one spelling (spawn.ReuseRetry; b.c4u).
	heldRetryFree = spawn.ReuseRetry + " once the name is free, " + spawn.PlainSpawnCollides
	// heldRetryWait: the end write did not apply, or failed: the
	// launch-timeout rule (SR-1.4), then heldRetryFree's opted-in retry.
	heldRetryWait = spawn.LaunchRetryRule + "; then " + heldRetryFree
)

// heldRetrySentences is a verb's retry sentences for heldNameOutcome's
// errors, each following the row sentence: Unanswered for the
// ErrTmuxUnresponsive errors (the re-lookup could not answer, or more than
// one session matched the name), "" meaning "retry later" for the Can't tell
// and none for the ambiguous holder; Vanished for the ErrTmuxSessionCreate of
// a holder that vanished before the re-lookup, "" meaning none (set, the
// description also names the instance id, which the sentence's "this id"
// means).
type heldRetrySentences struct {
	Unanswered string
	Vanished   string
}

// The label sentences of SR-1.4's "after duplicate session" row: whether the
// holder's label names this agent's instance id, read only from the label
// (never the environment, SR-3.12) and never naming the id it carries.
const (
	labelNamesThisID      = "its label names this instance id"
	labelNotNamesThisID   = "its label does not name this instance id"
	heldNameHumanDecision = "ending the session is a human's decision"
)

// heldNameHolder is what the re-lookup identified of the requested name's
// holder, for the ad.launch.name_held record (SR-14). Identified is false,
// and the other fields zero, when no single holder was identified: none holds
// the name (vanished), more than one listing entry matches it (ambiguous),
// or no answer was read (unreadable, tmux unavailable). Class is the
// holder's label class, zero when it cannot be trusted (a Can't tell
// verdict: conflicting labels, or a different server), so the record's
// carries_this_id and current_launch are then null.
type heldNameHolder struct {
	Identified bool
	SessionID  string
	Created    int64
	Class      tmux.LabelClass
}

// heldExaminedRow is the row a launch examined before its move or reset,
// given to heldNameOutcome by a verb whose row existed before its create
// (resume after "duplicate session", SR-8.5; reuse, SR-10.4): the
// starting-session rule's facts as examined (Name is the recorded name, the
// holder name and the name every description quotes; EndedAt, RecordsPID and
// RecordsSessionID are the pre-move values, never a re-read; Consequence is
// ignored, as heldNameOutcome sets it to the row sentence), the effective
// bound and window (startingSessionLimitsOf) and Now, the verb's instant read
// once after the re-lookup returned.
type heldExaminedRow struct {
	startingSessionRow
	Limits startingSessionLimits
	Now    time.Time
}

// heldNameOutcome maps the re-lookup after a create's "duplicate session" to
// the verb error and the holder facts (SR-1.2, SR-1.4, SR-1.5, SR-3.10,
// SR-4.2, SR-8.5, SR-9.4). res is the one tmux.Lookup on the launch socket
// with name as the holder name, for plain spawn's new row (its instance id
// and launch token, this store's id, no recorded server identity) or, for
// resume and reuse, the row as examined before the move or reset (its
// instance id, earlier launch token and recorded server identity, this
// store's id); socket is
// that launch socket; rowSentence is the caller's row sentence (plain spawn:
// heldRowEnded, heldRowLeftAsIs or heldRowStaysPending; resume and reuse: the
// restore's, restoreResultOf); retry is the caller's retry sentences
// (heldRetrySentences; plain spawn: heldRetryFree for both when its end
// write applied, else heldRetryWait for both;
// resume and reuse: Unanswered retryLater, so their ambiguous holder also
// ends with "retry later", and no Vanished); examined is the row the verb examined before
// its move or reset (heldExaminedRow), nil for plain spawn, whose row did not
// exist before. Each error matches exactly one catalogued
// sentinel under errors.Is (SR-1.5) and carries the row sentence exactly once,
// never "nothing was done" or "nothing was written":
//
//   - Can't tell, first, through the single-row verbs' shared cantTellError
//     with the row sentence as its consequence (so no "nothing was done"):
//     unreadable is tmux.ErrTmuxUnresponsive, ending with retry.Unanswered
//     in place of "retry later" when it is set ("retry later" otherwise);
//     provenance_conflict is tmux.ErrTmuxSessionConflict ("conflicting
//     labels", no label sentence, since the holder's class cannot be
//     trusted); a different server and tmux unavailable, socket permission
//     included, are tmux.ErrTmuxNotAvailable (a different server is
//     unreachable for plain spawn, whose new row records no server identity,
//     and live for resume and reuse, which carry the examined one). The consequence
//     also names the quoted requested name and, when one session holds it,
//     its tmux id, so every variant carries them.
//   - More than one listing entry matching the name: Can't tell for the
//     holder check (SR-3.10), tmux.ErrTmuxUnresponsive, saying the holder
//     cannot be told, with no tmux id and no label sentence, followed by
//     retry.Unanswered when it is set (by default no retry sentence).
//   - No holder (the session vanished before the re-lookup):
//     tmux.ErrTmuxSessionCreate through spawn.CreateFailedError ("session
//     creation failed: duplicate session", the quoted name) with the row
//     sentence; resume and reuse set no retry.Vanished. Plain spawn sets
//     one (b.1qq), since a plain spawn of the id collides with its row once
//     it is finished: the description then goes through
//     spawn.InstanceCreateFailedError, led by "instance <id>" as the
//     unanswered re-lookup's is, so the id that sentence's "this id" means
//     is named (a minted one included), and the row sentence is followed by
//     retry.Vanished.
//   - A holder, by its label class (SR-3.10): old ("left over from an
//     earlier life"; its label names this instance id, no agent-director row
//     described it before this spawn, ending it is a human's decision, the
//     "Operator actions" pointer), foreign ("a different instance id";
//     another row's agent that must not be ended; no pointer), another
//     store's ("another agent-director store"; another store's agent that
//     must not be ended; its label counts as not naming this instance id
//     even when it names the same id; no pointer) and none ("no valid
//     instance id"; a human must look, the pointer) are all
//     tmux.ErrTmuxSessionConflict, naming the quoted requested name, the
//     holder's tmux id and the label sentence, and ending with "list
//     --tmux-session-name".
//   - A holder with this launch's current label cannot occur for plain spawn
//     (the token is new and the create made nothing); it is handled
//     defensively, never as a success: tmux.ErrTmuxSessionConflict saying a
//     human must look, with the pointer.
//   - With examined set, the holder is judged against that row by its label
//     class, whatever the verdict: an old label gets resume's Leftover
//     wording (preLaunchLeftoverError, quoting name with the holder's tmux
//     id, the row sentence in place of "nothing was written"), never the
//     spawn-only sentence; a current label (the row's own session for the
//     examined token) gets the starting-session rule with the holder's
//     creation time, stopping window first (checkStartingSession(...).
//     refusal() with the row sentence as Consequence): "appears to still be
//     stopping" or "appears to still be starting" (tmux.ErrTmuxUnresponsive),
//     else "this row's own id" (tmux.ErrTmuxSessionConflict, with the
//     pointer). Foreign, other-store and no-valid-label holders are worded as
//     above.
//
// No description carries a label's value, the id a label names, another
// row's id, a store id, a session-environment value, "dead" or "gone", or a
// session-ending command (SR-1.4). It makes no call and writes nothing.
func heldNameOutcome(res tmux.Result, instanceID, name, socket, rowSentence string, retry heldRetrySentences, examined *heldExaminedRow) (heldNameHolder, error) {
	holder := heldHolderFacts(res)
	if res.Verdict == tmux.CantTell {
		return holder, cantTellError(res, cantTellRefusal{
			InstanceID:  instanceID,
			Socket:      socket,
			Call:        tmux.CallLookup,
			Consequence: heldNameClause(name, res.Holder) + "; " + rowSentence,
			Retry:       retry.Unanswered,
		})
	}
	if res.HolderAmbiguous {
		rest := rowSentence
		if retry.Unanswered != "" {
			rest += "; " + retry.Unanswered
		}
		return holder, ambiguousHolderError(heldAfterDuplicate, instanceID, name, rest)
	}
	if res.Holder == nil {
		ce := &tmux.CallError{Call: tmux.CallCreate, Failure: tmux.FailDuplicate}
		consequence := "no session held the name when it was looked up again; " + rowSentence
		if retry.Vanished == "" {
			return holder, spawn.CreateFailedError(ce, name, consequence)
		}
		return holder, spawn.InstanceCreateFailedError(ce, instanceID, name, consequence+"; "+retry.Vanished)
	}
	if examined != nil {
		switch res.HolderClass {
		case tmux.ClassOld:
			held := *res.Holder
			held.Name = name
			return holder, preLaunchLeftoverError(instanceID, []tmux.Session{held}, rowSentence)
		case tmux.ClassCurrent:
			row := examined.startingSessionRow
			row.Consequence = rowSentence
			session := *res.Holder
			return holder, checkStartingSession(examined.Limits, examined.Now, row, &session).refusal()
		}
	}
	return holder, heldHolderError(heldAfterDuplicate, res.HolderClass, instanceID, name, res.Holder.ID, rowSentence)
}

// heldHolderFacts returns res's holder facts (heldNameHolder): identified
// when exactly one session holds the name, with its class only when the
// verdict is not Can't tell. It is the one derivation of the holder/null rules
// for ad.launch.name_held (SR-14), builds no error and makes no call: plain
// spawn reaches it through heldNameOutcome, and find-missing calls it on a
// row's lookup Result directly (SR-11.3).
func heldHolderFacts(res tmux.Result) heldNameHolder {
	if res.Holder == nil || res.HolderAmbiguous {
		return heldNameHolder{}
	}
	h := heldNameHolder{Identified: true, SessionID: res.Holder.ID, Created: res.Holder.Created}
	if res.Verdict != tmux.CantTell {
		h.Class = res.HolderClass
	}
	return h
}

// heldNameClause names the requested name, quoted, as held after "duplicate
// session", with the holder's tmux id when one session holds it: the part of
// a Can't tell consequence that makes every held-name description name them
// (SR-1.4). It makes no claim about the holder's label.
func heldNameClause(name string, holder *tmux.Session) string {
	if holder == nil {
		return "tmux session " + strconv.Quote(name) + " already exists (duplicate session)"
	}
	return "tmux session " + strconv.Quote(name) + " (" + holder.ID + ") already exists (duplicate session)"
}

// holderPhrasing is the wording of a name-holder description that depends on
// when the holder was found: after a create's "duplicate session"
// (heldAfterDuplicate) or by a lookup before the launch (heldBeforeLaunch,
// resume's pre-launch check). The class words, label sentences and pointers
// are shared; only these parts differ.
type holderPhrasing struct {
	// Exists follows the quoted name (and, for one holder, its tmux id).
	Exists string
	// Holds follows Exists when one session holds the name.
	Holds string
	// OldDetail is the old class's own sentence; "" gives none.
	OldDetail string
	// CurrentWords stand in for the case words of the defensive current class.
	CurrentWords string
}

// heldAfterDuplicate is the wording after a create answered "duplicate
// session" (SR-1.4 row "Every error a plain spawn returns after duplicate
// session"); plain spawn's descriptions use it.
var heldAfterDuplicate = holderPhrasing{
	Exists:       "already exists (duplicate session)",
	Holds:        " and holds the requested name",
	OldDetail:    "no agent-director row described it before this spawn",
	CurrentWords: "it carries this launch's own label although this spawn created no session",
}

// heldBeforeLaunch is the wording of a holder found by the lookup before a
// launch (SR-8.2): no create was made, so no "duplicate session" claim. An
// old or current label holding the name is the lookup's Leftover or Ours, so
// the holder check meets them only defensively.
var heldBeforeLaunch = holderPhrasing{
	Exists:       "already exists",
	Holds:        " and holds the name",
	CurrentWords: "it carries this launch's own label although the lookup did not find it as this launch's session",
}

// ambiguousHolderError is the ErrTmuxUnresponsive for more than one listing
// entry matching name (Can't tell for the holder check, SR-3.10): the
// instance id, the quoted name, that the session holding it cannot be told,
// rest (the caller's consequence and retry sentences) and "list
// --tmux-session-name". It names no tmux id and no label sentence.
func ambiguousHolderError(p holderPhrasing, instanceID, name, rest string) error {
	return fmt.Errorf("%w: instance %s: tmux session %q %s, and more than one tmux session's name matches it, so the session holding it cannot be told; %s; %s",
		tmux.ErrTmuxUnresponsive, instanceID, name, p.Exists, rest, listSessionNameHint)
}

// heldHolderError is the conflict for a single identified holder of class c
// (SR-1.4 rows for an old, foreign, other-store or no valid label, and the
// "after duplicate session" row), worded by p: the instance id; the quoted
// name and the holder's tmux id; the case words (tmux.LabelClass.CaseWords);
// the label sentence; the class's own sentence; the row sentence (for a
// pre-launch holder, what was done); for the old and no-valid-label cases (and
// the defensive current one) the "Operator actions" pointer; and "list
// --tmux-session-name". It wraps tmux.ErrTmuxSessionConflict only.
func heldHolderError(p holderPhrasing, c tmux.LabelClass, instanceID, name, sessionID, rowSentence string) error {
	var words, label, detail, human string
	switch c {
	case tmux.ClassOld:
		words, label = c.CaseWords(), labelNamesThisID
		detail = p.OldDetail
		human = heldNameHumanDecision + ", " + operatorActionsPointer
	case tmux.ClassForeign:
		words, label = c.CaseWords(), labelNotNamesThisID
		detail = "the session is another row's agent and must not be ended"
	case tmux.ClassOtherStore:
		words, label = c.CaseWords(), labelNotNamesThisID
		detail = "the session is another agent-director store's agent and must not be ended"
	case tmux.ClassNone:
		words, label = c.CaseWords(), labelNotNamesThisID
		detail = "agent-director cannot tell whose session it is"
		human = "a human must look, " + operatorActionsPointer
	default:
		// ClassCurrent cannot occur here: after plain spawn's "duplicate
		// session" the new row's token is fresh, so no session can carry this
		// launch's label (a verb with an examined row decides it by the
		// starting-session rule in heldNameOutcome instead); before a launch
		// such a session is the lookup's Ours. Should the listing say
		// otherwise, it is a conflict for a human, never a success.
		words, label = p.CurrentWords, labelNamesThisID
		detail = "agent-director cannot tell how"
		human = "a human must look, " + operatorActionsPointer
	}
	desc := fmt.Sprintf("instance %s: tmux session %q (%s) %s%s: %s: %s", instanceID, name, sessionID, p.Exists, p.Holds, words, label)
	if detail != "" {
		desc += "; " + detail
	}
	desc += "; " + rowSentence
	if human != "" {
		desc += "; " + human
	}
	return fmt.Errorf("%w: %s; %s", tmux.ErrTmuxSessionConflict, desc, listSessionNameHint)
}
