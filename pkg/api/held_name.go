package api

import (
	"fmt"
	"strconv"

	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// This file holds the shared held-name outcome of a launch whose
// session-creating call answered "duplicate session" (SR-1.2, SR-1.4,
// SR-3.10, SR-9.4): the re-lookup's classified verb error and the holder
// facts its ad.launch.name_held record needs. It is verb-agnostic: plain
// spawn is its first caller, passing one of its three row sentences and one
// of its two retry sentences below; resume (Epic 16) and reuse (Epic 17) will
// call it unchanged with their own restore sentence and the default retry.

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
// or more than one session matching the name), in place of "retry later"
// (SR-1.4 row "Every error a plain spawn returns after duplicate session";
// WD 2026-09-30d (a)). A plain spawn of the same id cannot simply be retried:
// its row is already ended, or may still be pending.
const (
	// heldRetryReuse: the end write applied, so a plain spawn of the id now
	// collides with its ended row.
	heldRetryReuse = "a retry with this id uses the reuse opt-in (reuse_finished) once the name is free, since a plain spawn of the id now collides"
	// heldRetryWait: the end write did not apply, or failed (the launch-timeout
	// rule, SR-1.4).
	heldRetryWait = "do not retry until get shows the row ended or missing"
)

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

// heldNameOutcome maps the re-lookup after a create's "duplicate session" to
// the verb error and the holder facts (SR-1.2, SR-1.4, SR-1.5, SR-3.10,
// SR-9.4). res is the one tmux.Lookup on the launch socket with the new
// row's instance id and launch token, this store's id, no recorded server
// identity, and name as the holder name; socket is that launch socket;
// rowSentence is the caller's row sentence (plain spawn: heldRowEnded,
// heldRowLeftAsIs or heldRowStaysPending; resume and reuse, Epics 16 and 17:
// their restore result); retry is the caller's retry sentence for the
// ErrTmuxUnresponsive errors below, "" meaning the default (plain spawn:
// heldRetryReuse when its end write applied, else heldRetryWait; resume and
// reuse keep the default). Each error matches exactly one catalogued sentinel
// under errors.Is (SR-1.5) and carries the row sentence exactly once:
//
//   - Can't tell, first, through the single-row verbs' shared cantTellError
//     with the row sentence as its consequence (so no "nothing was done"):
//     unreadable is tmux.ErrTmuxUnresponsive, ending with retry in place of
//     "retry later" when retry is set ("retry later" otherwise);
//     provenance_conflict is tmux.ErrTmuxSessionConflict ("conflicting
//     labels", no label sentence, since the holder's class cannot be
//     trusted); a different server (unreachable here, as the new row records
//     no server identity; mapped for safety) and tmux unavailable, socket
//     permission included, are tmux.ErrTmuxNotAvailable. The consequence
//     also names the quoted requested name and, when one session holds it,
//     its tmux id, so every variant carries them.
//   - More than one listing entry matching the name: Can't tell for the
//     holder check (SR-3.10), tmux.ErrTmuxUnresponsive, saying the holder
//     cannot be told, with no tmux id and no label sentence, followed by
//     retry when it is set (by default no retry sentence).
//   - No holder (the session vanished before the re-lookup):
//     tmux.ErrTmuxSessionCreate through spawn.CreateFailedError ("session
//     creation failed: duplicate session", the quoted name).
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
//   - A holder with this launch's current label cannot occur (the token is
//     new and the create made nothing); it is handled defensively, never as
//     a success: tmux.ErrTmuxSessionConflict saying a human must look, with
//     the pointer.
//
// No description carries a label's value, the id a label names, another
// row's id, a store id, a session-environment value, "dead" or "gone", or a
// session-ending command (SR-1.4). It makes no call and writes nothing.
func heldNameOutcome(res tmux.Result, instanceID, name, socket, rowSentence, retry string) (heldNameHolder, error) {
	holder := heldHolderFacts(res)
	if res.Verdict == tmux.CantTell {
		return holder, cantTellError(res, cantTellRefusal{
			InstanceID:  instanceID,
			Socket:      socket,
			Call:        tmux.CallLookup,
			Consequence: heldNameClause(name, res.Holder) + "; " + rowSentence,
			Retry:       retry,
		})
	}
	if res.HolderAmbiguous {
		rest := rowSentence
		if retry != "" {
			rest += "; " + retry
		}
		return holder, fmt.Errorf("%w: instance %s: tmux session %q already exists (duplicate session), and more than one tmux session's name matches it, so the session holding it cannot be told; %s; %s",
			tmux.ErrTmuxUnresponsive, instanceID, name, rest, listSessionNameHint)
	}
	if res.Holder == nil {
		return holder, spawn.CreateFailedError(&tmux.CallError{Call: tmux.CallCreate, Failure: tmux.FailDuplicate}, name,
			"no session held the name when it was looked up again; "+rowSentence)
	}
	return holder, heldHolderError(res.HolderClass, instanceID, name, res.Holder.ID, rowSentence)
}

// heldHolderFacts returns res's holder facts (heldNameHolder): identified
// when exactly one session holds the name, with its class only when the
// verdict is not Can't tell.
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

// heldHolderError is the conflict for a single identified holder of class c
// (SR-1.4 rows for an old, foreign, other-store or no valid label, and the
// "after duplicate session" row): the instance id; the quoted name and the
// holder's tmux id; the case words (tmux.LabelClass.CaseWords); the label
// sentence; the class's own sentence; the row sentence; for the old and
// no-valid-label cases (and the defensive current one) the "Operator
// actions" pointer; and "list --tmux-session-name". It wraps
// tmux.ErrTmuxSessionConflict only.
func heldHolderError(c tmux.LabelClass, instanceID, name, sessionID, rowSentence string) error {
	var words, label, detail, human string
	switch c {
	case tmux.ClassOld:
		words, label = c.CaseWords(), labelNamesThisID
		detail = "no agent-director row described it before this spawn"
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
		// ClassCurrent cannot occur: the new row's token is fresh and the
		// create answered "duplicate session", so no session can carry this
		// launch's label. Should the listing say otherwise, it is a conflict
		// for a human, never a success.
		words, label = "it carries this launch's own label although this spawn created no session", labelNamesThisID
		detail = "agent-director cannot tell how"
		human = "a human must look, " + operatorActionsPointer
	}
	desc := fmt.Sprintf("instance %s: tmux session %q (%s) already exists (duplicate session) and holds the requested name: %s: %s; %s; %s",
		instanceID, name, sessionID, words, label, detail, rowSentence)
	if human != "" {
		desc += "; " + human
	}
	return fmt.Errorf("%w: %s; %s", tmux.ErrTmuxSessionConflict, desc, listSessionNameHint)
}
