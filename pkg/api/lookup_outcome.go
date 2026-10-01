package api

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// This file holds the single-row verbs' shared mapping from a Can't tell
// lookup or pane-listing outcome to the verb error (SR-1.2, SR-1.4, SR-1.8,
// SR-3.3, SR-3.4), the socket a row's calls use, and the lookup's view of a
// row (rowLaunch). It is the one place these live: kill is their first
// live-row user; plain spawn's label scan uses the mapping; read-pane,
// send-keys and pause (Epic 11) and resume and reuse (Epic 16) reuse them
// unchanged, and find-missing builds its rows' lookup views with rowLaunch.

// nothingWasDone is the default consequence sentence of a Can't tell refusal
// (SR-1.4): no tmux action was sent and nothing was written.
const nothingWasDone = "nothing was done"

// operatorActionsPointer points a refusal that needs a human to the README's
// "Operator actions" section by its title (SR-1.4, SR-18.17).
const operatorActionsPointer = `see "Operator actions" in the agent-director README`

// listSessionNameHint names the command that shows whether a row uses a
// session name (SR-1.4); it ends no session.
const listSessionNameHint = "list --tmux-session-name <name> shows whether a row uses a session name"

// cantTellRefusal holds the verb-specific parts of a Can't tell refusal, so
// every single-row verb reuses cantTellError unchanged.
type cantTellRefusal struct {
	// InstanceID is the row's instance id, which every description names.
	InstanceID string
	// Context, when not empty, follows the instance id and says what was
	// being done (for example the label scan's "the label scan before the
	// spawn", or a kill's quoted recorded name).
	Context string
	// Socket is the socket the verb's calls used: named by the scope-value
	// text, and by the socket-permission text when the reply names none.
	Socket string
	// Call is the call whose outcome this is (tmux.CallLookup, or
	// tmux.CallListPanes for a pane listing), named when an unreadable
	// failure was not a *tmux.CallError.
	Call tmux.Call
	// Consequence is the sentence saying what the caller's state is: ""
	// means nothingWasDone. The plain-spawn label scan says nothing was
	// written; kill after a sent kill says the kill was sent and may or may
	// not have taken effect; a later verb its own row sentence.
	Consequence string
	// Retry is the unreadable refusal's closing retry sentence: "" means
	// retryLater. Only a caller whose retry of the same call cannot work
	// sets it: plain spawn after "duplicate session", whose row is already
	// ended or may still be pending (heldRetryReuse, heldRetryWait).
	Retry string
}

// retryLater is the unreadable refusal's default retry sentence (SR-1.4 rows
// "timeout" and "unrecognised reply").
const retryLater = "retry later"

// retry returns r's retry sentence, retryLater by default.
func (r cantTellRefusal) retry() string {
	if r.Retry == "" {
		return retryLater
	}
	return r.Retry
}

// consequence returns r's consequence sentence, nothingWasDone by default.
func (r cantTellRefusal) consequence() string {
	if r.Consequence == "" {
		return nothingWasDone
	}
	return r.Consequence
}

// lead returns the description's lead after the sentinel: the instance id
// and, when set, the context.
func (r cantTellRefusal) lead() string {
	if r.Context == "" {
		return "instance " + r.InstanceID
	}
	return "instance " + r.InstanceID + ": " + r.Context
}

// cantTellError maps a Can't tell lookup or pane-listing Result (from
// tmux.Lookup, tmux.Classify or tmux.ListingFailure) to its verb error, the
// one mapping every single-row verb uses (SR-1.2, SR-1.4, SR-1.8, SR-3.3,
// SR-3.4). It returns nil for any other verdict. Each error matches exactly
// one catalogued sentinel under errors.Is and wraps no other (SR-1.5):
//
//   - different server: tmux.ErrTmuxNotAvailable, "this is not the tmux
//     server the agent was launched on", the consequence, and that the caller
//     must run as the agents' user in their tmux environment (SR-18.7);
//   - provenance_conflict: tmux.ErrTmuxSessionConflict, "conflicting
//     labels", either the scope value or the quoted names and tmux ids of the
//     sessions carrying this launch's label (Result.Conflicting), the
//     consequence, that a human must look with the pointer to "Operator
//     actions", and "list --tmux-session-name";
//   - unreadable: tmux.ErrTmuxUnresponsive, which call timed out and its
//     effective timeout in seconds, or which call gave a reply agent-director
//     does not recognise with its first line (trimmed to 200 bytes by the
//     client), the consequence, and the retry sentence ("retry later" unless
//     the caller sets cantTellRefusal.Retry);
//   - tmux unavailable: tmux.ErrTmuxNotAvailable through
//     spawn.TmuxUnavailableError (a missing binary, or the socket-permission
//     reply naming the socket and "not accessible to this user").
//
// No description carries a label's value, another row's id, a
// session-environment value, "dead" or "gone", or a session-ending command.
func cantTellError(res tmux.Result, r cantTellRefusal) error {
	if res.Verdict != tmux.CantTell {
		return nil
	}
	switch res.CantTell {
	case tmux.CantTellDifferentServer:
		return fmt.Errorf("%w: %s: this is not the tmux server the agent was launched on (socket %s); %s; the caller must run as the agents' user in their tmux environment",
			tmux.ErrTmuxNotAvailable, r.lead(), r.Socket, r.consequence())
	case tmux.CantTellProvenanceConflict:
		return conflictingLabelsError(res, r)
	case tmux.CantTellUnavailable:
		if res.Cause != nil {
			return spawn.TmuxUnavailableError(res.Cause, r.Socket, r.consequence())
		}
		return fmt.Errorf("%w: %s: tmux %s failed: the tmux binary could not be run; %s",
			tmux.ErrTmuxNotAvailable, r.lead(), r.Call, r.consequence())
	}
	return unreadableError(res.Cause, r)
}

// conflictingLabelsError is the "conflicting labels" refusal (SR-1.4 row
// provenance_conflict): a duplicate label names the sessions carrying this
// launch's label, quoted with their tmux ids; otherwise it is a scope value.
func conflictingLabelsError(res tmux.Result, r cantTellRefusal) error {
	what := "an @ad_owner value is set at the global, server or global-window scope on the tmux server at " + r.Socket +
		", so no session's label can be trusted"
	if slices.Contains(res.Disagree, tmux.ReasonDuplicateLabel) && len(res.Conflicting) > 0 {
		named := make([]string, 0, len(res.Conflicting))
		for _, s := range res.Conflicting {
			named = append(named, strconv.Quote(s.Name)+" ("+s.ID+")")
		}
		what = "more than one session carries this launch's label: " + strings.Join(named, ", ")
	}
	return fmt.Errorf("%w: %s: conflicting labels: %s; %s; a human must look, %s; %s",
		tmux.ErrTmuxSessionConflict, r.lead(), what, r.consequence(), operatorActionsPointer, listSessionNameHint)
}

// unreadableError is the unreadable refusal (SR-1.4 rows "timeout" and
// "unrecognised reply"): cause names the call and its effective timeout, or
// the unrecognised reply's first line; a cause that is nil (an error that was
// not a *tmux.CallError) is an unrecognised reply of r.Call. It ends with r's
// retry sentence.
func unreadableError(cause *tmux.CallError, r cantTellRefusal) error {
	what := "tmux " + string(r.Call) + " failed: unrecognized reply"
	if cause != nil {
		what = cause.Error()
	}
	if cause == nil || cause.Failure != tmux.FailTimeout {
		what += "; tmux gave a reply agent-director does not recognise"
	}
	return fmt.Errorf("%w: %s: %s; %s; %s", tmux.ErrTmuxUnresponsive, r.lead(), what, r.consequence(), r.retry())
}

// rowSocket returns the socket every call for a row uses (SR-3.3): its
// recorded tmux_socket exactly as recorded, without consulting the caller's
// environment; for a row that records none (from before the release), the
// caller's socket resolved as tmux would, creating nothing
// (spawn.ResolveQuerySocket; LFR H1). A resolution refusal is
// tmux.ErrTmuxNotAvailable with the unusable-socket-directory description,
// ending "nothing was done" (SR-1.4). It makes no tmux call.
func rowSocket(recorded string) (string, error) {
	if recorded != "" {
		return recorded, nil
	}
	return spawn.ResolveQuerySocket(nothingWasDone)
}

// rowLaunch is the lookup's view of a row (SR-3.4): its instance id; the
// launch token and recorded server identity of id (the row's recorded launch
// identity, or the one adoption found for this call); this store's id,
// storeID; and socket, the socket the row's calls use (rowSocket). kill,
// read-pane, send-keys and find-missing build every lookup's Launch with it.
func rowLaunch(instanceID string, id LaunchIdentity, storeID, socket string) tmux.Launch {
	return tmux.Launch{
		InstanceID:      instanceID,
		Token:           id.Token,
		StoreID:         storeID,
		Socket:          socket,
		ServerPID:       id.ServerPID,
		ServerStart:     id.ServerStart,
		ServerStarttime: id.ServerStarttime,
	}
}
