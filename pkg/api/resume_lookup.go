package api

import (
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/gabemahoney/agent-director/internal/tmux"
)

// This file holds the pre-launch decision for a row a launch examined as
// finished (SR-8.2, SR-10.2, SR-10.8, SR-3.10, SR-3.16, SR-4.2; LFR M6, H2):
// from the one lookup on the row's socket, either "proceed" or the one
// refusal, plus the facts the caller's ad.provenance.disagree records need.
// resume's pre-launch check and reuse's old-row lookup (spawn with the reuse
// opt-in, whose new-name pre-check is the name-holder check on Gone) are its
// users. Every case is routed to an existing builder: the Can't tell mapping
// (cantTellError), the starting-session rule (checkStartingSession), the
// holder-class wording (heldHolderError, ambiguousHolderError) and the shared
// Leftover words (leftoverSessions).

// The ad.provenance.disagree action values a launch onto a finished row
// writes at its pre-launch check (SR-14), shared by resume and reuse: whether
// the check refused the call or let it go on to the launch. Both are written
// before any write (resume's move to pending, reuse's reset).
const (
	// preLaunchActionRefused: the pre-launch check refused; nothing was
	// written.
	preLaunchActionRefused = "refused"
	// preLaunchActionProceeded: the pre-launch check let the call go on to
	// pre-trust, the move or reset, and the launch.
	preLaunchActionProceeded = "proceeded"
)

// preLaunchActionOf returns the pre-launch check's action value for its
// refusal err: preLaunchActionRefused when err is set, else
// preLaunchActionProceeded.
func preLaunchActionOf(err error) string {
	if err != nil {
		return preLaunchActionRefused
	}
	return preLaunchActionProceeded
}

// preLaunchRow is a finished row as the verb examined it before its move or
// reset, never a re-read: the starting-session rule's facts (whose Name is the
// row's recorded name, quoted by the Can't tell, Ours, stopping, starting and
// own-id descriptions), the name the lookup was given as its holder name
// (HolderName), the socket the lookup used, and the row's selected agent
// process (agentProcess over its recorded SessionStart and pane identities).
type preLaunchRow struct {
	startingSessionRow
	// HolderName is the name the lookup was given as its holder name, quoted
	// by the name-holder check's descriptions on Gone; "" means Name. resume
	// leaves it empty (its holder name is the recorded name); reuse sets the
	// requested name (SR-10.8).
	HolderName string
	// Socket is the socket of the lookup call.
	Socket string
	// Agent is the row's agent process (SR-3.8).
	Agent tmux.AgentProcess
}

// holderName returns the lookup's holder name: HolderName, or Name when it is
// empty.
func (r preLaunchRow) holderName() string {
	if r.HolderName == "" {
		return r.Name
	}
	return r.HolderName
}

// preLaunchRowOf returns row's pre-launch facts as read: name is the row's
// recorded name, quoted in every description and, unless the caller sets
// HolderName, given to the lookup as its holder name; socket the lookup's
// socket.
func preLaunchRowOf(row Spawn, name, socket string) preLaunchRow {
	return preLaunchRow{
		startingSessionRow: startingSessionRow{
			InstanceID:       row.ClaudeInstanceID,
			Name:             name,
			EndedAt:          row.EndedAt,
			RecordsPID:       row.PID > 0,
			RecordsSessionID: row.ClaudeSessionID != "",
		},
		Socket: socket,
		Agent:  agentProcess(row, row.Identity),
	}
}

// preLaunchDecision is the pre-launch check's outcome and its trail facts.
// The re-lookup after "duplicate session" (finishedLaunch.heldName) carries its own
// trail facts in the same fields (lookupTrailFacts), with Err unused.
type preLaunchDecision struct {
	// Err is the refusal; nil means proceed.
	Err error
	// Verdict is the lookup's outcome token (tmux.Result.Token).
	Verdict string
	// Server is the lookup's Server value ("" when no server check ran).
	Server string
	// SessionID is the tmux id of the session concerned: the Ours session,
	// the first leftover (lowest $N) or the name's one holder on Gone; ""
	// otherwise.
	SessionID string
	// CurrentName is the Ours session's stored name when Reasons holds
	// name_changed; "" otherwise.
	CurrentName string
	// Reasons are the distinct ad.provenance.disagree reasons: the lookup's
	// own, and name_changed when Ours was found under another name; never
	// adopted.
	Reasons []string
}

// decidePreLaunch maps the one pre-launch lookup res for row to "proceed" or
// the refusal (SR-8.2, SR-3.10, SR-3.16, SR-4.2; LFR M6, H2), in this order:
//
//   - Can't tell, by its kind, through cantTellError with the quoted name as
//     context and "nothing was done": a different server and tmux unavailable
//     (socket permission included) are tmux.ErrTmuxNotAvailable,
//     provenance_conflict is tmux.ErrTmuxSessionConflict ("conflicting
//     labels"), unreadable is tmux.ErrTmuxUnresponsive (a session line with a
//     non-decimal creation field is a malformed reply, so it is unreadable and
//     never reaches the starting-session rule).
//   - Ours: the starting-session rule with the Ours session, stopping window
//     first, then the bound, then the own-id conflict ("this row's own id",
//     with the "Operator actions" pointer).
//   - Leftover: tmux.ErrTmuxSessionConflict ("left over from an earlier
//     life"), naming each leftover by quoted name and tmux id.
//   - Gone with the agent process alive (tmux.JudgeProcess): the rule with no
//     session ("appears to still be stopping" inside the window, else the
//     own-id conflict; never "appears to still be starting").
//   - Gone otherwise (the process gone, none recorded, or one that cannot be
//     checked, so the lookup alone decides; LFR M6): the name-holder check of
//     the holder name (row.holderName(), which these descriptions quote;
//     reuse's new-name pre-check, SR-10.8). More than one entry matching the
//     name is tmux.ErrTmuxUnresponsive (Can't tell for the holder check,
//     SR-3.10); one holder is tmux.ErrTmuxSessionConflict by its label class
//     (foreign, another store's or no valid label; old and current only
//     defensively, as such a session, under any name, makes the lookup
//     Leftover or Ours); no holder proceeds, as does a Gone from a no-server
//     or no-socket reply, which lists nothing. A prefix neighbour and a `$` or `\` name matching no
//     stored form hold nothing (tmux.StoredForms), so the create decides.
//
// Another store's session is never Ours or Leftover (the lookup counts it
// toward Gone), so it is met only as the name's holder. The decision never
// returns tmux.ErrTmuxSessionCreate, and every error matches exactly one
// catalogued sentinel under errors.Is (SR-1.5). An Ours for a row with no
// recorded server identity is not adopted (Result.Adopt is ignored; SR-3.6,
// LFR H2). pc is the start-time reader, called at most once and only on Gone;
// now is the verb's clock, read once, only when the starting-session rule
// runs, so after the lookup returned. It makes no tmux call, no store read or
// write, no trail write and no log line, and reads no environment.
func decidePreLaunch(res tmux.Result, row preLaunchRow, pc ProcChecker, lim startingSessionLimits, now func() time.Time) preLaunchDecision {
	d := lookupTrailFacts(res, row.Name)
	switch res.Verdict {
	case tmux.Ours:
		d.SessionID = res.Session.ID
		session := res.Session
		d.Err = checkStartingSession(lim, now(), row.startingSessionRow, &session).refusal()
	case tmux.Leftover:
		leftovers := sortedBySessionNumber(res.Leftovers)
		d.SessionID = leftovers[0].ID
		d.Err = preLaunchLeftoverError(row.InstanceID, leftovers, preLaunchNothingWritten)
	case tmux.Gone:
		if res.Holder != nil {
			d.SessionID = res.Holder.ID
		}
		if tmux.JudgeProcess(pc, row.Agent.Identity) == tmux.ProcAlive {
			d.Err = checkStartingSession(lim, now(), row.startingSessionRow, nil).refusal()
		} else {
			d.Err = preLaunchHolderError(res, row.InstanceID, row.holderName())
		}
	default:
		d.Err = cantTellError(res, cantTellRefusal{
			InstanceID: row.InstanceID,
			Context:    "tmux session " + strconv.Quote(row.Name),
			Socket:     row.Socket,
			Call:       tmux.CallLookup,
		})
	}
	return d
}

// lookupTrailFacts returns the ad.provenance.disagree facts of one lookup res
// of a launch onto a finished row (SR-14, SR-3.16), name being the row's
// recorded name: the verdict token, the Server value, and the distinct
// reasons, the lookup's own plus name_changed (with the Ours session's stored
// name as CurrentName) when Ours was found under another name than the
// recorded one (nameChanged); never adopted. SessionID and Err are the
// caller's. The pre-launch check (decidePreLaunch, for resume and reuse) and
// the re-lookup after "duplicate session" (finishedLaunch.heldName) derive their
// reasons here.
func lookupTrailFacts(res tmux.Result, name string) preLaunchDecision {
	d := preLaunchDecision{Verdict: res.Token(), Server: res.Server}
	d.Reasons = append(d.Reasons, res.Disagree...)
	if nameChanged(res, name) && !slices.Contains(d.Reasons, tmux.ReasonNameChanged) {
		d.Reasons = append(d.Reasons, tmux.ReasonNameChanged)
		d.CurrentName = res.Session.Name
	}
	return d
}

// emitLookupDisagree writes one lookup's ad.provenance.disagree records for a
// launch onto a finished row (SR-14, SR-3.16) through the shared emitter, one
// per distinct reason in pre.Reasons and none in the normal case. call holds
// the verb's own fields (Verb, Source, InstanceID, Socket, SessionName = the
// row's recorded name, Caller); the session concerned, the current name, the
// server value and the verdict come from pre, and action is the caller's.
// resume and reuse both write through it (emitFinishedRowDisagree).
// Fail-open.
func emitLookupDisagree(call provenanceDisagree, action string, pre preLaunchDecision) {
	call.SessionID = pre.SessionID
	call.CurrentSessionName = pre.CurrentName
	call.Server = pre.Server
	call.Verdict = pre.Verdict
	call.Action = action
	emitProvenanceDisagree(call, pre.Reasons...)
}

// leftoverLostRace decides the one re-read a launch onto a finished row makes
// after its pre-launch check refused a Leftover (SR-8.6, SR-10.5): a
// competing launch (a resume's move or a reuse's reset) that followed this
// call's read leaves a session whose label carries a token this call did not
// examine, which the lookup sees as left over. examined is the snapshot the
// call read; again, found and err are the re-read's answer. It returns
// CondAbsent when the re-read found no row, CondChanged when the row no
// longer holds examined, and 0 when it still does or the re-read failed, so
// the Leftover refusal stands. Each verb maps the result to its own lost-race
// error: resume through resumeMoveError (resumeLostRace), reuse through
// reuseLostRaceError (reuseLostRace). It makes no call and writes nothing.
func leftoverLostRace(examined, again RowSnapshot, found bool, err error) CondResult {
	switch {
	case err != nil:
		return 0
	case !found:
		return CondAbsent
	case again == examined:
		return 0
	}
	return CondChanged
}

// preLaunchHolderError is the name-holder check on Gone (SR-3.10, SR-8.2,
// SR-10.8): nil when no session holds name; the ambiguous holder's
// tmux.ErrTmuxUnresponsive; or the holder's class conflict. Both refusals
// say nothing was done.
func preLaunchHolderError(res tmux.Result, instanceID, name string) error {
	switch {
	case res.HolderAmbiguous:
		return ambiguousHolderError(heldBeforeLaunch, instanceID, name, nothingWasDone+"; "+retryLater)
	case res.Holder == nil:
		return nil
	}
	return heldHolderError(heldBeforeLaunch, res.HolderClass, instanceID, name, res.Holder.ID, nothingWasDone)
}

// preLaunchNothingWritten is the pre-launch Leftover refusal's consequence:
// it is returned before the move, so nothing was written.
const preLaunchNothingWritten = "nothing was written"

// preLaunchLeftoverError is resume's Leftover refusal (SR-1.4, SR-8.2,
// SR-8.5), worded as the plain-spawn scan's (scanLeftoverError): the instance
// id; "left over from an earlier life"; each leftover's quoted name and tmux
// id, lowest $N first, up to three, then the rest as a count
// (leftoverSessions); consequence, what the caller's state is
// (preLaunchNothingWritten at the pre-launch check; the restore's row
// sentence for an old-label holder after "duplicate session",
// heldNameOutcome); that ending such a session is a human's decision, with
// the pointer to "Operator actions"; and "list tmux_session_name". It never
// carries a label value, a token or a store id, and wraps only
// tmux.ErrTmuxSessionConflict.
func preLaunchLeftoverError(instanceID string, leftovers []tmux.Session, consequence string) error {
	return fmt.Errorf("%w: instance %s: %s: %s; %s; ending such a session is a human's decision, %s; %s",
		tmux.ErrTmuxSessionConflict, instanceID, tmux.ClassOld.CaseWords(), leftoverSessions(leftovers), consequence, operatorActionsPointer, listSessionNameHint)
}
