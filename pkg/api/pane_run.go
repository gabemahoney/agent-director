package api

import (
	"strconv"

	"github.com/gabemahoney/agent-director/internal/tmux"
)

// This file holds the pane verbs' shared per-call run (SR-3.6, SR-3.7,
// SR-7.2): the row, the socket its calls use, the lookup's view of it, the
// one pane listing and the agent's pane on Ours. read-pane is its first user
// and send-keys reuses it; each verb keeps its own lookup verdict handling
// (Leftover differs per verb) and its own action.

// paneTmux is the lookup and the pane listing every pane verb's tmux surface
// has (ReadPaneTmux, SendKeysTmux).
type paneTmux interface {
	TmuxLookup
	// ListPanes lists every pane of the server at socket.
	ListPanes(socket string) ([]TmuxPane, error)
}

// paneRun is one pane-verb call's row and the socket its calls use, with the
// verb's gone sentinel and what its pane refusals say was not done, and what
// the run found for the verbs that record it.
type paneRun struct {
	t       paneTmux
	pc      ProcChecker
	row     Spawn
	storeID string
	socket  string
	// gone is the verb's gone sentinel: tmux.ErrTmuxCaptureFailed
	// (read-pane) or tmux.ErrTmuxSendKeys (send-keys).
	gone error
	// nothing is what the verb's pane refusals say was not done.
	nothing paneNothing
	// adopter, when set, writes a due adoption through adoptIdentity
	// (send-keys); nil takes what findAdoption found for this call only, with
	// no write (read-pane, SR-7.5).
	adopter identityAdopter

	// listing is a failed pane listing's Result (tmux.ListingFailure); the
	// zero Result when no listing failed.
	listing tmux.Result
	// adoption is what the Ours step's adoption found, and whether its write
	// applied; the zero adoption when no Ours step ran.
	adoption adoption
}

// launchFor is the lookup's view of the row with identity id (rowLaunch),
// with this store's id and the row's socket.
func (r *paneRun) launchFor(id LaunchIdentity) tmux.Launch {
	return rowLaunch(r.row.ClaudeInstanceID, id, r.storeID, r.socket)
}

// ours finds the agent's pane in one pane listing (SR-3.7) on an Ours lookup
// res of launch, and returns its pane id with the launch view a failed
// action's follow-up lookup uses, or the verb error with nothing done. A row
// that records no pane takes the pane findAdoption finds; with an adopter it
// is written once by adoptIdentity (SR-3.6), otherwise it is used for this
// call only. The follow-up's launch view carries what adoption found, so its
// server check uses the answering server. No agent's pane in the listing:
// paneNotFoundError, saying the pane was not adopted when a lost create
// reply's pane could not be taken.
func (r *paneRun) ours(res tmux.Result, launch tmux.Launch) (string, tmux.Launch, error) {
	panes, err := r.listPanes(launch)
	if err != nil {
		return "", launch, err
	}
	if r.adopter != nil {
		r.adoption = adoptIdentity(r.adopter, r.row, res, panes, r.pc)
	} else {
		r.adoption = findAdoption(r.row.Identity, res, panes, r.pc)
	}
	a := r.adoption
	followUp := r.launchFor(a.Identity)
	if pane, ok := agentPane(panes, a.Identity.PaneID, a.Identity.PanePID); ok {
		return pane.ID, followUp, nil
	}
	notAdopted := a.Due && adoptionNeedsListing(r.row.Identity) && a.Pane != tmux.PaneOne
	return "", followUp, paneNotFoundError(r.refusal(res.Session.Name), notAdopted)
}

// listPanes makes the one pane listing. A failed listing is classified as a
// lookup with the same failure would be (SR-3.7), and its Result kept in
// r.listing: no server or no socket is the gone error, anything else Can't
// tell's error; nothing is done.
func (r *paneRun) listPanes(launch tmux.Launch) ([]tmux.Pane, error) {
	panes, err := r.t.ListPanes(r.socket)
	if err == nil {
		return panes, nil
	}
	r.listing = tmux.ListingFailure(err, r.pc, launch)
	if r.listing.Verdict == tmux.Gone {
		return nil, r.goneError()
	}
	return nil, cantTellError(r.listing, r.cantTellRefusal(tmux.CallListPanes))
}

// goneError is the verb's gone error: its gone sentinel, "the row's session
// is not there", naming the recorded name, with the verb's nothing sentence.
func (r *paneRun) goneError() error {
	return paneGoneError(r.gone, r.refusal(r.row.TmuxSessionName), "")
}

// refusal is the pane refusal's inputs for this row with the session name
// name, saying what the verb did not do.
func (r *paneRun) refusal(name string) paneRefusal {
	return paneRefusal{InstanceID: r.row.ClaudeInstanceID, Name: name, Nothing: r.nothing}
}

// cantTellRefusal is the shared mapping's inputs for call on this row: the
// recorded name as context and the row's socket. Its consequence is the
// default "nothing was done" (SR-1.4 rows timeout, unrecognised reply,
// different server, tmux unavailable and conflicting labels); only the pane
// refusals (refusal) say what the verb did not do.
func (r *paneRun) cantTellRefusal(call tmux.Call) cantTellRefusal {
	return cantTellRefusal{
		InstanceID: r.row.ClaudeInstanceID,
		Context:    "tmux session " + strconv.Quote(r.row.TmuxSessionName),
		Socket:     r.socket,
		Call:       call,
	}
}
