package api

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/gabemahoney/agent-director/internal/tmux"
)

// This file holds the pane verbs' shared refusal descriptions (SR-1.4,
// SR-1.5, SR-3.7, SR-7.2, SR-7.3): the agent's pane was not found, a pane
// verb on Leftover, and the verb's gone error "the row's session is not
// there". read-pane is their first user; send-keys and pause reuse them
// unchanged, each saying what it did not do through paneNothing. send-keys'
// two pending-row ErrSpawnNotInteractive refusals (no launch start or token,
// and Leftover) live here too. kill's own Leftover refusal (kill_errors.go)
// keeps its own wording around the shared leftoverSessions. No description
// carries a label's value, a session-environment value, another row's id,
// "dead" or "gone", a session-ending command or a tmux attach command.

// paneNothing is the sentence a pane-verb refusal gives for what was not
// done: read-pane's nothingRead, send-keys' and pause's nothingSent; after a
// keys action whose text went through, textNotSubmitted instead
// (keysReached).
type paneNothing string

const (
	// nothingRead is read-pane's sentence: no pane was captured.
	nothingRead paneNothing = "nothing was read"
	// nothingSent is send-keys' and pause's sentence: no keys were sent.
	nothingSent paneNothing = "nothing was sent"
)

// paneRefusal holds what every pane-verb refusal names.
type paneRefusal struct {
	// InstanceID is the row's instance id.
	InstanceID string
	// Name is the tmux session name the refusal quotes (Go quoted-string
	// form): the session carrying a label with the row's id for the
	// pane-not-found conflict, the row's recorded name for the gone error.
	// The gone error omits it when empty.
	Name string
	// Nothing says what was not done (nothingRead, nothingSent, or a verb's
	// own sentence); "" says "nothing was sent or read".
	Nothing paneNothing
}

// nothing returns r's sentence for what was not done.
func (r paneRefusal) nothing() string {
	if r.Nothing == "" {
		return "nothing was sent or read"
	}
	return string(r.Nothing)
}

// paneNotAdopted is the pane-not-found conflict's lost-reply clause (SR-3.6,
// SR-3.7; WD 2026-09-29c): no single pane carries the row's launch token in
// its @ad_pane, so no pane was adopted.
const paneNotAdopted = "the agent's pane was not adopted (no pane carries this launch's pane label)"

// paneNotFoundError is the pane verbs' ErrTmuxSessionConflict for "the
// agent's pane was not found" (SR-1.4, SR-3.7, SR-7.2): the lookup found a
// session carrying a label with this row's id (Ours, or a lone leftover whose
// token no pane carries), but no pane of the listing is the agent's. It
// names the instance id; the quoted session name; "this row's own id"; "the
// agent's pane was not found"; what was not done; when notAdopted (a lost
// create reply whose pane findAdoption could not take: PaneNone or PaneMany),
// "the agent's pane was not adopted (no pane carries this launch's pane
// label)"; that a human can look, with the pointer to "Operator actions"
// (SR-18.17); and "list --tmux-session-name". It wraps only
// ErrTmuxSessionConflict (SR-1.5).
func paneNotFoundError(r paneRefusal, notAdopted bool) error {
	what := "the agent's pane was not found"
	if notAdopted {
		what += "; " + paneNotAdopted
	}
	return fmt.Errorf("%w: instance %s: tmux session %s carries a label with this row's own id, but %s; %s; a human can look, %s; %s",
		tmux.ErrTmuxSessionConflict, r.InstanceID, strconv.Quote(r.Name), what, r.nothing(), operatorActionsPointer, listSessionNameHint)
}

// paneLeftoverError is the pane verbs' ErrTmuxSessionConflict on Leftover
// (SR-1.4, SR-3.4, SR-7.2): the instance id; "not this launch's session";
// each leftover session's quoted name and tmux id, lowest $N first, up to
// killLeftoversNamed, then the rest as a count (namedSessions); "this row's
// own id"; when moreThanOne (read-pane, which reads a lone leftover's pane),
// that more than one leftover session exists; what was not done; the pointer
// to "Operator actions"; and "list --tmux-session-name". It never names a
// session-ending command and wraps only ErrTmuxSessionConflict (SR-1.5). A
// lone leftover whose token no pane carries is paneNotFoundError's case, not
// this one.
func paneLeftoverError(r paneRefusal, leftovers []tmux.Session, moreThanOne bool) error {
	what := leftoverSessions(leftovers)
	if moreThanOne {
		what += "; more than one leftover session exists"
	}
	return fmt.Errorf("%w: instance %s: not this launch's session: %s; %s; a human can look, %s; %s",
		tmux.ErrTmuxSessionConflict, r.InstanceID, what, r.nothing(), operatorActionsPointer, listSessionNameHint)
}

// leftoverSessions is what every Leftover refusal says of the leftover
// sessions (kill's, the pane verbs' and send-keys' on a pending row; SR-1.4):
// each one's quoted name and tmux id, lowest $N first, up to
// killLeftoversNamed, then the rest as a count (namedSessions), and that they
// carry the label of an earlier launch with this row's own id.
func leftoverSessions(leftovers []tmux.Session) string {
	sorted := sortedBySessionNumber(leftovers)
	found := namedSessions(sorted, killLeftoversNamed)
	if len(sorted) > 1 {
		return "tmux sessions " + found + " carry labels of earlier launches with this row's own id"
	}
	return "tmux session " + found + " carries the label of an earlier launch with this row's own id"
}

// pendingNoLaunchError is send-keys' ErrSpawnNotInteractive for a pending
// row, with allow_pending, whose launch start or launch token is not recorded
// (SR-1.4, SR-7.1, SR-22.8): the instance id; that the launch start or launch
// token is not recorded, so agent-director cannot show that a session belongs
// to the current launch; that nothing was sent and no tmux call was made;
// and the pointer to "Operator actions" (SR-18.17). It wraps only
// ErrSpawnNotInteractive (SR-1.5).
func pendingNoLaunchError(instanceID string) error {
	return fmt.Errorf("%w: instance %s: the row's launch start or launch token is not recorded, so agent-director cannot show that a session belongs to the current launch; %s and no tmux call was made; %s",
		ErrSpawnNotInteractive, instanceID, nothingSent, operatorActionsPointer)
}

// pendingLeftoverError is send-keys' ErrSpawnNotInteractive for a pending
// row, with allow_pending, whose lookup is Leftover (SR-1.4, SR-7.1, SR-7.2):
// the instance id; "not this launch's session"; each leftover session by
// quoted name and tmux id (leftoverSessions), an earlier launch's by its
// label; and that nothing was sent. It never names a session-ending command
// and wraps only ErrSpawnNotInteractive (SR-1.5). A live row's Leftover is
// paneLeftoverError's ErrTmuxSessionConflict instead.
func pendingLeftoverError(instanceID string, leftovers []tmux.Session) error {
	return fmt.Errorf("%w: instance %s: not this launch's session: %s; %s",
		ErrSpawnNotInteractive, instanceID, leftoverSessions(leftovers), nothingSent)
}

// paneGoneError is a pane verb's gone error (SR-1.4, SR-7.2, SR-7.3): "the
// row's session is not there", after a Gone lookup or an action failure whose
// follow-up lookup is Gone or Leftover. gone is the verb's gone sentinel,
// ErrTmuxCaptureFailed (read-pane) or ErrTmuxSendKeys (send-keys, pause), and
// the error matches only it under errors.Is (SR-1.5). The description names
// the instance id; the row's recorded name, quoted, when r.Name is set;
// detail, when not empty (such as the failed action); and what was not done.
// It never says "dead" or "gone".
func paneGoneError(gone error, r paneRefusal, detail string) error {
	what := "the row's session is not there: no tmux session of this launch was found"
	if r.Name != "" {
		what += " (recorded name " + strconv.Quote(r.Name) + ")"
	}
	if detail != "" {
		what = detail + "; " + what
	}
	return fmt.Errorf("%w: instance %s: %s; %s", gone, r.InstanceID, what, r.nothing())
}

// namedSessions names sessions, in the order given, by quoted name and tmux
// id ("\"name\" ($N)"), comma-separated, up to limit, then the rest as a
// count ("and 2 more"). The refusals that list sessions (kill's and the pane
// verbs' Leftover, the plain-spawn scan's leftover) share it.
func namedSessions(sessions []tmux.Session, limit int) string {
	named := make([]string, 0, min(len(sessions), limit))
	for _, s := range sessions[:min(len(sessions), limit)] {
		named = append(named, strconv.Quote(s.Name)+" ("+s.ID+")")
	}
	found := strings.Join(named, ", ")
	if more := len(sessions) - len(named); more > 0 {
		found += fmt.Sprintf(" and %d more", more)
	}
	return found
}
