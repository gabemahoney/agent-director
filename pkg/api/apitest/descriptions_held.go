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
// "nothing was done"), the four holder conflicts (DescHeldLeftover,
// DescHeldNoValidID, DescHeldDifferentID, DescHeldOtherStore), which also
// state whether the holder's label names this instance id, and the
// ambiguous holder (DescHeldAmbiguous). The re-lookup's other outcomes reuse
// the existing cases with AfterHeldName: DescSessionCreateFailed with
// Duplicate (the name vanished), DescConflictingLabels with NothingWasDone,
// DescDifferentServer, DescCallTimeout, DescUnrecognisedReply,
// DescSocketPermission and DescTmuxNotRun.

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

// heldNotPendingStatements are statements that the row stays pending or will
// heal, which a held-name description makes only for HeldRowStoreError.
var heldNotPendingStatements = []string{"stays pending", "will heal"}

// HeldName parameterises the held-name cases: Name is the requested session
// name; SessionID is the holder's tmux id ($N) when the re-lookup found one
// session holding it ("" otherwise: vanished, ambiguous, unreadable, tmux
// unavailable); Row is the end write's result.
type HeldName struct {
	Name      string
	SessionID string
	Row       HeldRow
}

// AfterHeldName returns c as an error a plain spawn returns after "duplicate
// session" (SR-1.4, SR-9.4) whose holder's label is not judged: the quoted
// requested name, p.SessionID when set, and exactly p.Row's row sentence, in
// place of "nothing was done", which it must not say; no label sentence;
// never that the row stays pending or will heal unless the end write failed;
// a statement that the row was ended only when it was. Use it on
// DescSessionCreateFailed (Duplicate), DescConflictingLabels
// (NothingWasDone), DescDifferentServer, DescCallTimeout,
// DescUnrecognisedReply, DescSocketPermission and DescTmuxNotRun; the
// DescHeld* cases apply it themselves.
func (c DescCase) AfterHeldName(p HeldName) DescCase {
	return c.afterHeldName(p, heldLabelNoClaim)
}

// afterHeldName is AfterHeldName with the label sentence label requires.
func (c DescCase) afterHeldName(p HeldName, label heldLabel) DescCase {
	row, ok := heldRowSentences[p.Row]
	if !ok {
		panic("apitest: HeldName with no end-write result")
	}
	c.Name += ", after duplicate session"
	req := slices.DeleteFunc(append([]string(nil), c.Require...), func(s string) bool { return s == nothingWasDone })
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
	switch label {
	case heldLabelNamesThisID:
		req = append(req, heldLabelNames)
		mustNot = appendMissing(mustNot, heldLabelNotNames)
	case heldLabelNotThisID:
		req = append(req, heldLabelNotNames)
		mustNot = appendMissing(mustNot, heldLabelNames)
	default:
		mustNot = appendMissing(mustNot, heldLabelNames, heldLabelNotNames)
	}
	c.Require = req
	c.MustNot = appendMissing(mustNot, nothingWasDone, rowStaysPending)
	return c
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
// req and "list --tmux-session-name", with the overlay and label sentence.
func heldConflict(class tmux.LabelClass, p HeldName, label heldLabel, req ...string) DescCase {
	if p.SessionID == "" {
		panic("apitest: held-name holder case with no SessionID")
	}
	words := class.CaseWords()
	return DescCase{
		Name:    "ErrTmuxSessionConflict, held name, " + words,
		Require: append([]string{words, "list --tmux-session-name"}, req...),
	}.afterHeldName(p, label)
}

// DescHeldLeftover is ErrTmuxSessionConflict for a name held by a session
// whose label names the new row's id with an earlier launch's token (SR-1.4;
// GAP 3; LFR S7): "left over from an earlier life", "its label names this
// instance id", that no agent-director row described it before this spawn,
// the holder's tmux id, that ending it is a human's decision with the
// "Operator actions" pointer, and "list --tmux-session-name". Pass any other
// row's id as forbid.
func DescHeldLeftover(p HeldName) DescCase {
	return heldConflict(tmux.ClassOld, p, heldLabelNamesThisID,
		"no agent-director row described it before this spawn", "a human's decision").PointsToOperatorActions()
}

// DescHeldNoValidID is ErrTmuxSessionConflict for a name held by a session
// with no valid label (SR-1.4, SR-3.10): "no valid instance id", "its label
// does not name this instance id" (agent-director reads only the label,
// SR-3.12), the "Operator actions" pointer and "list --tmux-session-name".
// Pass any id read from the session (its environment, a malformed label's)
// as forbid.
func DescHeldNoValidID(p HeldName) DescCase {
	return heldConflict(tmux.ClassNone, p, heldLabelNotThisID).PointsToOperatorActions()
}

// DescHeldDifferentID is ErrTmuxSessionConflict for a name held by another
// row's session (a foreign label; SR-1.4, SR-3.10): "a different instance
// id", "its label does not name this instance id", that the session is
// another row's agent and must not be ended, "list --tmux-session-name", and
// no "Operator actions" pointer. Pass the other id as forbid.
func DescHeldDifferentID(p HeldName) DescCase {
	return heldConflict(tmux.ClassForeign, p, heldLabelNotThisID,
		"another row's agent and must not be ended").withoutOperatorActions()
}

// DescHeldOtherStore is ErrTmuxSessionConflict for a name held by a session
// of another agent-director store (SR-1.4, SR-3.10; WD 2026-09-29 STORE):
// "another agent-director store", "its label does not name this instance id"
// (even when it names the same id), that the session is another store's
// agent and must not be ended, "list --tmux-session-name", and no
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
// matches it", the row sentence and "list --tmux-session-name"; no label
// sentence; never "dead" or "gone". p.SessionID must be empty: pass the
// matching sessions' tmux ids as forbid.
func DescHeldAmbiguous(p HeldName) DescCase {
	if p.SessionID != "" {
		panic("apitest: DescHeldAmbiguous names no holder")
	}
	return DescCase{
		Name:    "ErrTmuxUnresponsive, held name, ambiguous holder",
		Require: []string{"more than one tmux session's name matches it", "list --tmux-session-name"},
		MustNot: unresponsiveMustNot,
	}.afterHeldName(p, heldLabelNoClaim)
}
