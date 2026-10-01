package api

import (
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/gabemahoney/agent-director/internal/tmux"
)

// This file holds the pre-launch decision for a row a launch examined as
// finished (SR-8.2, SR-3.10, SR-3.16, SR-4.2; LFR M6, H2): from the one lookup
// on the row's socket, either "proceed" or the one refusal, plus the facts the
// caller's ad.provenance.disagree records need. resume's pre-launch check is
// its user. Every case is routed to an existing builder: the Can't tell
// mapping (cantTellError), the starting-session rule (checkStartingSession),
// the holder-class wording (heldHolderError, ambiguousHolderError) and the
// shared Leftover words (leftoverSessions).

// The ad.provenance.disagree action values resume writes at its pre-launch
// check (SR-14): whether the check refused the call or let it go on to the
// launch. Both are written before the move to pending.
const (
	// resumeActionRefused: the pre-launch check refused; nothing was written.
	resumeActionRefused = "refused"
	// resumeActionProceeded: the pre-launch check let the call go on to
	// pre-trust, the move and the launch.
	resumeActionProceeded = "proceeded"
)

// preLaunchRow is a finished row as the verb examined it before its move or
// reset, never a re-read: the starting-session rule's facts (whose Name is
// both the holder name the lookup was given and the name every description
// quotes), the socket the lookup used, and the row's selected agent process
// (agentProcess over its recorded SessionStart and pane identities).
type preLaunchRow struct {
	startingSessionRow
	// Socket is the socket of the lookup call.
	Socket string
	// Agent is the row's agent process (SR-3.8).
	Agent tmux.AgentProcess
}

// preLaunchRowOf returns row's pre-launch facts as read: name is the name
// given to the lookup as its holder name and quoted in every description,
// socket the lookup's socket.
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
//     checked, so the lookup alone decides; LFR M6): the name-holder check.
//     More than one entry matching the name is tmux.ErrTmuxUnresponsive (Can't
//     tell for the holder check, SR-3.10); one holder is
//     tmux.ErrTmuxSessionConflict by its label class (foreign, another store's
//     or no valid label; old and current only defensively, as such a session
//     makes the lookup Leftover or Ours); no holder proceeds. A prefix
//     neighbour and a `$` or `\` name matching no stored form hold nothing
//     (tmux.StoredForms), so the create decides.
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
	d := preLaunchDecision{Verdict: res.Token(), Server: res.Server}
	d.Reasons = append(d.Reasons, res.Disagree...)
	switch res.Verdict {
	case tmux.Ours:
		d.SessionID = res.Session.ID
		if nameChanged(res, row.Name) && !slices.Contains(d.Reasons, tmux.ReasonNameChanged) {
			d.Reasons = append(d.Reasons, tmux.ReasonNameChanged)
			d.CurrentName = res.Session.Name
		}
		session := res.Session
		d.Err = checkStartingSession(lim, now(), row.startingSessionRow, &session).refusal()
	case tmux.Leftover:
		leftovers := sortedBySessionNumber(res.Leftovers)
		d.SessionID = leftovers[0].ID
		d.Err = preLaunchLeftoverError(row.InstanceID, leftovers)
	case tmux.Gone:
		if res.Holder != nil {
			d.SessionID = res.Holder.ID
		}
		if tmux.JudgeProcess(pc, row.Agent.Identity) == tmux.ProcAlive {
			d.Err = checkStartingSession(lim, now(), row.startingSessionRow, nil).refusal()
		} else {
			d.Err = preLaunchHolderError(res, row.InstanceID, row.Name)
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

// preLaunchHolderError is the name-holder check on Gone (SR-3.10, SR-8.2):
// nil when no session holds name; the ambiguous holder's
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

// preLaunchLeftoverError is the pre-launch Leftover refusal (SR-1.4, SR-8.2),
// worded as the plain-spawn scan's (scanLeftoverError): the instance id; "left
// over from an earlier life"; each leftover's quoted name and tmux id, lowest
// $N first, up to three, then the rest as a count (leftoverSessions); that
// nothing was written; that ending such a session is a human's decision, with
// the pointer to "Operator actions"; and "list --tmux-session-name". It never
// carries a label value, a token or a store id, and wraps only
// tmux.ErrTmuxSessionConflict.
func preLaunchLeftoverError(instanceID string, leftovers []tmux.Session) error {
	return fmt.Errorf("%w: instance %s: %s: %s; nothing was written; ending such a session is a human's decision, %s; %s",
		tmux.ErrTmuxSessionConflict, instanceID, tmux.ClassOld.CaseWords(), leftoverSessions(leftovers), operatorActionsPointer, listSessionNameHint)
}
