package api

import (
	"slices"

	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// This file holds resume's held-name path (SR-8.5): what resumeLaunchOutcome
// does when the session-creating call answered "duplicate session".

// resumeHeldName is resume's "duplicate session" path (SR-8.5, SR-1.4,
// SR-3.10, SR-4.2, SR-13.2 path (ii), SR-14, SR-3.16). ex is what the call
// examined before its move, req the create's request and movedVersion the
// version the move produced. In this order it:
//
//  1. makes exactly one tmux.Lookup on the launch socket for the row as
//     examined (rowLaunch over its instance id, its earlier launch token and
//     recorded server identity, this store's id), with the recorded name as
//     the holder name, then reads the clock once. The move cleared the
//     row's identity columns, so nothing is re-read. Any adoption the result
//     offers is ignored, and no agent process is read;
//  2. restores the row (resumeRestore, with the prior values of the row as
//     read, its WARN line on a store error and its ad.resume.restored, whose
//     launch_error names the error below);
//  3. builds the error with heldNameOutcome, judged against the examined row
//     (heldExaminedRow: the examined ended_at, pid and session-id presence,
//     the configured bound and window, the clock reading of step 1), with
//     the restore's sentence as the row sentence and retryLater as the retry
//     sentence, so the ambiguous holder's ErrTmuxUnresponsive ends with
//     "retry later" as the pre-launch one does (SR-18.1);
//  4. writes the re-lookup's ad.provenance.disagree records
//     (emitResumeDisagree, action the restore's row result), skipping every
//     reason the pre-launch check already wrote (ex.disagreeWritten), so each
//     is written at most once per call, each naming the session it is about
//     (name_changed the row's own Ours session, every other reason the
//     name's holder, else the Ours session); then exactly one ad.launch.name_held
//     (source ad_resume, launch resume, the recorded name, the launch
//     socket, this store's id, the holder facts, the re-lookup's token, the
//     error, the restore's row result and store error, and the caller
//     identity). Both are fail-open.
//
// The path adds only the one lookup to the create (lookup, create,
// re-lookup); it never labels, kills, captures or sends to the holding
// session. It always returns an error; only a holder that vanished before
// the re-lookup gives tmux.ErrTmuxSessionCreate.
func resumeHeldName(d resumeDeps, ex resumeExamined, req spawn.CreateRequest, movedVersion int64) error {
	res := tmux.Lookup(d.t, d.pc, rowLaunch(req.InstanceID, ex.row.Identity, d.storeID, req.Socket), req.Name)
	examined := heldExaminedRow{
		startingSessionRow: preLaunchRowOf(ex.row, req.Name, req.Socket).startingSessionRow,
		Limits:             startingSessionLimitsOf(d.cfg.Tmux),
		Now:                d.now(),
	}

	var holder heldNameHolder
	restored, err := resumeRestore(d, req.InstanceID, movedVersion, resumePriorOf(ex.row), func(sentence string) error {
		var herr error
		holder, herr = heldNameOutcome(res, req.InstanceID, req.Name, req.Socket, sentence, retryLater, &examined)
		return herr
	})

	// Each record names the session it is about (SR-14): name_changed, which
	// only an Ours verdict yields, the row's own session; every other reason
	// the name's holder, else the Ours session. name_changed is the last of
	// the emitter's order, so writing it second keeps that order.
	facts := lookupTrailFacts(res, req.Name)
	facts.Reasons = slices.DeleteFunc(facts.Reasons, func(reason string) bool {
		return slices.Contains(ex.disagreeWritten, reason)
	})
	renamed := slices.Contains(facts.Reasons, tmux.ReasonNameChanged)
	facts.Reasons = slices.DeleteFunc(facts.Reasons, func(reason string) bool {
		return reason == tmux.ReasonNameChanged
	})
	switch {
	case res.Holder != nil:
		facts.SessionID = res.Holder.ID
	case res.Verdict == tmux.Ours:
		facts.SessionID = res.Session.ID
	}
	emitResumeDisagree(d, ex.row, req.Socket, restored.RowResult, facts)
	if renamed {
		facts.SessionID = res.Session.ID
		facts.Reasons = []string{tmux.ReasonNameChanged}
		emitResumeDisagree(d, ex.row, req.Socket, restored.RowResult, facts)
	}
	emitNameHeld(nameHeld{
		Source:        nameHeldSourceResume,
		Launch:        nameHeldLaunchResume,
		InstanceID:    req.InstanceID,
		Name:          req.Name,
		Socket:        req.Socket,
		StoreID:       d.storeID,
		Holder:        holder,
		LookupOutcome: res.Token(),
		Err:           err,
		RowResult:     restored.RowResult,
		StoreError:    restored.StoreErr,
		Caller:        d.who,
	})
	return err
}
