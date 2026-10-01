package api

import (
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// This file holds the tmux phase shared by the pane verbs that type keys
// into the agent's pane (SR-3.6, SR-3.7, SR-7.2, SR-7.3, SR-13.2): send-keys'
// text and pause's /exit. Each verb runs its own row read and state rules
// first, then this phase: one lookup, on Ours one pane listing and the
// adoption when due (paneRun), then the keys and Enter by the agent's pane
// id, with a failed send mapped in the "keys may have reached the pane" mode
// (paneActionFailure.Keys). The verbs differ only in their Leftover refusal
// (keysRun.leftover) and the keys typed.

// keysTmux is the tmux surface of the pane verbs that type keys
// (SendKeysTmux, PauseTmux): the lookup, the pane listing and the keys sent
// to one pane by its pane id.
type keysTmux interface {
	paneTmux
	// SendKeysPane types text literally into the pane paneID on socket and
	// then, only if that succeeded and pressEnter is set, sends Enter.
	SendKeysPane(socket, paneID, text string, pressEnter bool) error
}

// keysFacts is what one keys verb's tmux phase found and did, kept so the
// verb's trail reads it without re-deriving it (SR-7.4, SR-14).
type keysFacts struct {
	// Socket is the socket the row's calls used; "" when the phase did not
	// run.
	Socket string
	// LookupRan reports that the first lookup was made; Lookup is its Result
	// (zero when it was not made).
	LookupRan bool
	Lookup    tmux.Result
	// Listing is a failed pane listing's Result (tmux.ListingFailure); zero
	// when the listing answered or none was made.
	Listing tmux.Result
	// Adopted reports that the adoption write applied (SR-3.6).
	Adopted bool
	// Session is the session concerned: the Ours session the lookup found;
	// zero on every other verdict.
	Session tmux.Session
	// Sent reports that SendKeysPane was called (keys may have reached the
	// pane); SendErr is its error, nil when the keys and Enter both went
	// through. enterFailedAfterText tells a failed Enter after the text.
	Sent    bool
	SendErr error
	// FollowUp is the follow-up lookup after a failed send (Ran false when
	// none was made).
	FollowUp paneFollowUp
}

// keysRun is one keys verb's tmux phase: the pane verbs' shared run (with
// the verb's gone sentinel, what its refusals say was not done and, for the
// adoption write, its store as adopter), the keys surface, the verb's
// Leftover refusal and what the phase found.
type keysRun struct {
	paneRun
	kt keysTmux
	// leftover, when set, is the verb's refusal on a Leftover lookup
	// (send-keys' pending-row ErrSpawnNotInteractive); nil gives the pane
	// verbs' paneLeftoverError (ErrTmuxSessionConflict, "not this launch's
	// session").
	leftover func(leftovers []tmux.Session) error
	// found is what the phase found and did.
	found keysFacts
}

// newKeysRun is the keys phase for row on socket: t is the verb's tmux
// surface, pc the start-time reader, storeID this store's id and adopter the
// verb's store, which makes the adoption write (SR-3.6). Its gone sentinel is
// ErrTmuxSendKeys and its refusals say "nothing was sent" (SR-7.2).
func newKeysRun(t keysTmux, pc ProcChecker, row Spawn, storeID, socket string, adopter identityAdopter) keysRun {
	return keysRun{
		paneRun: paneRun{
			t: t, pc: pc, row: row, storeID: storeID, socket: socket,
			gone: tmux.ErrTmuxSendKeys, nothing: nothingSent, adopter: adopter,
		},
		kt: t,
	}
}

// deliver types text into the agent's pane of the row's current launch, by
// pane id, then Enter (SR-7.2), or returns the verb error. The calls are at
// most one lookup, one pane listing, the text call, the Enter call and, only
// after a send failure other than a timeout, one follow-up lookup (SR-13.2);
// every call uses the row's socket. Nothing is sent before the lookup
// returned Ours and the listing showed the agent's pane. A failed send is
// mapped in the "keys may have reached the pane" mode (SR-7.3): once the
// text call went through, no description says nothing was sent.
func (r *keysRun) deliver(text string) error {
	r.found.Socket = r.socket
	paneID, launch, err := r.target()
	r.found.Listing, r.found.Adopted = r.listing, r.adoption.Applied
	if err != nil {
		return err
	}

	r.found.Sent = true
	if err := r.kt.SendKeysPane(r.socket, paneID, text, true); err != nil {
		r.found.SendErr = err
		fu, verr := paneActionFailureError(err, paneActionFailure{
			Call:    tmux.CallSendText,
			Gone:    r.gone,
			Pane:    r.refusal(r.row.TmuxSessionName),
			Refusal: r.cantTellRefusal(tmux.CallSendText),
			Keys:    true,
		}, r.kt, r.pc, launch)
		r.found.FollowUp = fu
		return verr
	}
	return nil
}

// target makes the lookup (holder name the row's recorded name, as kill
// passes it) and, on Ours, the pane listing, and returns the agent's pane id
// with the launch view a failed send's follow-up lookup uses, or the verb
// error, with nothing sent: Leftover is the verb's leftover refusal, Gone
// the gone error, and Can't tell or tmux unavailable the single-row verbs'
// shared mapping.
func (r *keysRun) target() (string, tmux.Launch, error) {
	launch := r.launchFor(r.row.Identity)
	res := tmux.Lookup(r.t, r.pc, launch, r.row.TmuxSessionName)
	r.found.LookupRan, r.found.Lookup = true, res
	switch res.Verdict {
	case tmux.Ours:
		r.found.Session = res.Session
		return r.ours(res, launch)
	case tmux.Leftover:
		if r.leftover != nil {
			return "", launch, r.leftover(res.Leftovers)
		}
		return "", launch, paneLeftoverError(r.refusal(""), res.Leftovers, false)
	case tmux.Gone:
		return "", launch, r.goneError()
	}
	return "", launch, cantTellError(res, r.cantTellRefusal(tmux.CallLookup))
}

// emitDisagree writes the keys verb call's ad.provenance.disagree records,
// fail-open (SR-3.16, SR-7.4, SR-14): one per distinct reason collected
// during the call's tmux phase, none in the normal case and none when the
// phase did not run (found is zero). The reasons are collected as kill
// collects them: the first lookup's, a failed pane listing's and the
// follow-up lookup's Disagree reasons, name_changed when the Ours session
// carries another name, and adopted only when the adoption write applied.
// verb is the deciding verb (send-keys or pause); both keys verbs write
// source ad_send_keys, SR-14's keys-sending source. who is the caller
// identity the verb collected once for the call. Each verb calls it once per
// call on the path its Client method and its exported function share, so
// each entry point writes each reason at most once per call. No record
// carries a label's value, a launch token, the typed text, a
// session-environment value or another row's id (SR-15).
func (r *keysRun) emitDisagree(verb, instanceID string, who caller) {
	f := r.found
	var reasons []string
	var current string
	verdict := tmux.TokenNotRun
	if f.LookupRan {
		verdict = f.Lookup.Token()
		reasons = append(reasons, f.Lookup.Disagree...)
		if nameChanged(f.Lookup, r.row.TmuxSessionName) {
			reasons = append(reasons, tmux.ReasonNameChanged)
			current = f.Lookup.Session.Name
		}
	}
	reasons = append(reasons, f.Listing.Disagree...)
	if f.Adopted {
		reasons = append(reasons, tmux.ReasonAdopted)
	}
	if f.FollowUp.Ran {
		reasons = append(reasons, f.FollowUp.Result.Disagree...)
	}
	emitProvenanceDisagree(provenanceDisagree{
		Verb:               verb,
		Source:             "ad_send_keys",
		InstanceID:         instanceID,
		Socket:             f.Socket,
		SessionName:        r.row.TmuxSessionName,
		SessionID:          f.Session.ID,
		CurrentSessionName: current,
		Server:             f.Lookup.Server,
		Verdict:            verdict,
		Action:             sendKeysAction(f.Sent, f.SendErr),
		Caller:             who,
	}, reasons...)
}
