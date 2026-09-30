package apitest

import (
	"strconv"

	"github.com/gabemahoney/agent-director/internal/tmux"
)

// descriptions_pane.go holds the shared description helper's SR-1.4 cases
// that the pane verbs introduce (Epic 11), parameterised by verb so that
// read-pane, send-keys and pause share them: the agent's pane was not found
// (DescPaneNotFound, with its lost-reply variant; a lone leftover whose
// launch token no pane carries uses it too), a pane verb on Leftover
// (DescPaneLeftover, built from kill's Leftover blocks, leftoverCase), and
// the verb's gone error (DescPaneGone); only these say what the verb did
// not do ("nothing was read", "nothing was sent"). A pane verb's tmux
// trouble (a timeout or unrecognised reply of the lookup, the pane listing
// or the action, a different server, conflicting labels, tmux unavailable,
// and the follow-up after a failed action) says "nothing was done", like
// kill, so it uses DescCallTimeout, DescUnrecognisedReply,
// DescDifferentServer, DescConflictingLabels (with NothingWasDone),
// DescSocketPermission and DescTmuxNotRun unchanged. No pane-verb case may
// give the retired "no pane 0.0" clause or a base-index hint
// (WD 2026-09-29c).

// PaneVerb is a pane verb whose refusals the pane cases check.
type PaneVerb string

// The pane verbs.
const (
	PaneReadPane PaneVerb = "read-pane"
	PaneSendKeys PaneVerb = "send-keys"
	PanePause    PaneVerb = "pause"
)

// nothing returns the sentence v's pane refusals give for what was not
// done: read-pane's "nothing was read", send-keys' and pause's "nothing was
// sent".
func (v PaneVerb) nothing() string {
	switch v {
	case PaneReadPane:
		return "nothing was read"
	case PaneSendKeys, PanePause:
		return "nothing was sent"
	}
	panic("apitest: unknown pane verb " + strconv.Quote(string(v)))
}

// The pane-verb phrases (SR-1.4, SR-3.4, SR-3.6, SR-3.7): the agent's pane
// not found and its lost-reply clause, read-pane's leftover count, and the
// gone error's "the row's session is not there".
const (
	paneNotFound        = "the agent's pane was not found"
	paneNotAdopted      = "the agent's pane was not adopted"
	noPaneCarriesLabel  = "no pane carries this launch's pane label"
	moreThanOneLeftover = "more than one leftover session exists"
	rowSessionNotThere  = "the row's session is not there"
)

// paneMustNot is what no pane-verb description may say: the retired "no
// pane 0.0" clause and any base-index hint (base-index, pane-base-index;
// WD 2026-09-29c).
var paneMustNot = []string{"no pane 0.0", "base-index"}

// PaneNotFound parameterises DescPaneNotFound: the verb; the row's instance
// id; Name, the session carrying a label with the row's id (Ours's session,
// or the lone leftover's when no pane carries its launch token); and
// LostReply, a lost create reply whose pane could not be adopted (SR-3.6).
type PaneNotFound struct {
	Verb       PaneVerb
	InstanceID string
	Name       string
	LostReply  bool
}

// DescPaneNotFound is ErrTmuxSessionConflict for "the agent's pane was not
// found" (pane verbs; SR-1.4, SR-3.7): the instance id; the quoted name;
// "this row's own id"; "the agent's pane was not found"; the verb's sentence
// for what was not done; with LostReply, that the agent's pane was not
// adopted because no pane carries this launch's pane label (which it must
// not say otherwise); the "Operator actions" pointer; and "list
// --tmux-session-name". It never says "not this launch's session" or that
// more than one leftover session exists.
func DescPaneNotFound(p PaneNotFound) DescCase {
	req := []string{p.InstanceID, strconv.Quote(p.Name), thisRowsOwnID, paneNotFound, p.Verb.nothing(), listSessionName}
	mustNot := append(append([]string(nil), paneMustNot...), notThisLaunch, moreThanOneLeftover)
	name := "ErrTmuxSessionConflict, " + string(p.Verb) + ", the agent's pane was not found"
	if p.LostReply {
		name += " after a lost reply"
		req = append(req, paneNotAdopted, noPaneCarriesLabel)
	} else {
		mustNot = append(mustNot, "not adopted", noPaneCarriesLabel)
	}
	return DescCase{Name: name, Require: req, MustNot: mustNot}.PointsToOperatorActions()
}

// PaneLeftover parameterises DescPaneLeftover: the verb; the row's instance
// id; and the leftover sessions found, in the order the description names
// them (lowest $N first; up to three, then their count), whose number is the
// leftover count.
type PaneLeftover struct {
	Verb       PaneVerb
	InstanceID string
	Sessions   []DescSession
}

// DescPaneLeftover is ErrTmuxSessionConflict for a pane verb on Leftover
// (SR-1.4, SR-3.4): kill's Leftover blocks (each session's quoted name and
// tmux id, "this row's own id", "not this launch's session", the "Operator
// actions" pointer, "list --tmux-session-name"; leftoverCase), the instance
// id and the verb's sentence for what was not done. read-pane refuses only
// more than one leftover (it reads a lone leftover's pane), so for it the
// case requires "more than one leftover session exists" and panics on fewer
// than two sessions; the other verbs must not say it. Never "the agent's
// pane was not found".
func DescPaneLeftover(p PaneLeftover) DescCase {
	req := []string{p.InstanceID, p.Verb.nothing()}
	mustNot := append(append([]string(nil), paneMustNot...), paneNotFound)
	if p.Verb == PaneReadPane {
		if len(p.Sessions) < 2 {
			panic("apitest: read-pane refuses Leftover only with more than one leftover session")
		}
		req = append(req, moreThanOneLeftover)
	} else {
		mustNot = append(mustNot, moreThanOneLeftover)
	}
	return leftoverCase("ErrTmuxSessionConflict, "+string(p.Verb)+" on Leftover", p.Sessions, req, mustNot)
}

// PaneGone parameterises DescPaneGone: the verb; the row's instance id; its
// recorded name (quoted when set); and FailedCall, the action call whose
// failure's follow-up lookup was Gone or Leftover ("" after a Gone lookup).
type PaneGone struct {
	Verb       PaneVerb
	InstanceID string
	Name       string
	FailedCall tmux.Call
}

// DescPaneGone is a pane verb's gone error (ErrTmuxCaptureFailed for
// read-pane, ErrTmuxSendKeys for send-keys and pause; SR-7.2, SR-7.3): the
// instance id, "the row's session is not there", the quoted recorded name
// when set, "tmux <call> failed" for FailedCall when set, and the verb's
// sentence for what was not done; never "dead" or "gone". SR-1.4 has no row
// for it; the case keeps its phrases in the helper.
func DescPaneGone(p PaneGone) DescCase {
	req := []string{p.InstanceID, rowSessionNotThere, p.Verb.nothing()}
	if p.Name != "" {
		req = append(req, strconv.Quote(p.Name))
	}
	if p.FailedCall != "" {
		req = append(req, "tmux "+string(p.FailedCall)+" failed")
	}
	return DescCase{
		Name:    "gone error, " + string(p.Verb) + ", the row's session is not there",
		Require: req,
		MustNot: append(append([]string(nil), paneMustNot...), unresponsiveMustNot...),
	}
}
