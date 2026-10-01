package api

import (
	"context"
	"log"
	"slices"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/internal/trail"
)

// This file holds what the two launches onto a row examined as finished share
// once the write that begins the launch has applied (resume's move to
// pending, SR-8.5; reuse's reset, SR-10.4): the create outcome's mapping, the
// restore after a failed launch with its row sentences, and the path after
// "duplicate session". The verb's own values (finishedLaunchVerb) are the
// only difference; each verb builds a finishedLaunch per call
// (resumeDeps.launchOnto, reuseDeps.launchOnto).

// finishedLaunchVerb is what differs between resume's and reuse's launch in
// the code they share. pkg/api declares the two values only here.
type finishedLaunchVerb struct {
	// Verb is the verb word: the launch-timeout description's verb, the
	// restore WARN line's prefix and the ad.provenance.disagree verb.
	Verb string
	// Source is the trail source of every record the launch writes.
	Source string
	// RestoredEvent is the trail event of each restore attempt.
	RestoredEvent string
	// NameHeldLaunch is ad.launch.name_held's launch value.
	NameHeldLaunch string
	// LaunchWrite names the write that began the launch in the restore's
	// changed and absent sentences (restoreResultOf).
	LaunchWrite string
	// TimeoutConsequence is the launch-timeout description's row sentence
	// (spawn.LaunchTimeoutError): what the write left, since the row stays
	// pending.
	TimeoutConsequence string
}

// resumeLaunchVerb is resume's (SR-8.5, SR-14): verb resume, source
// ad_resume, ad.resume.restored, launch resume, "resume moved it to
// pending", and "the row stays pending" on a timeout.
var resumeLaunchVerb = finishedLaunchVerb{
	Verb:               "resume",
	Source:             nameHeldSourceResume,
	RestoredEvent:      resumeRestoredEvent,
	NameHeldLaunch:     nameHeldLaunchResume,
	LaunchWrite:        "resume moved it to pending",
	TimeoutConsequence: spawn.RowStaysPending,
}

// reuseLaunchVerb is reuse's (SR-10.4, SR-10.6, SR-14): verb spawn, source
// ad_spawn, ad.spawn.reuse_restored, launch reuse, "this spawn reset it",
// and reuseRowResetStaysPending on a timeout.
var reuseLaunchVerb = finishedLaunchVerb{
	Verb:               "spawn",
	Source:             nameHeldSourceSpawn,
	RestoredEvent:      reuseRestoredEvent,
	NameHeldLaunch:     nameHeldLaunchReuse,
	LaunchWrite:        "this spawn reset it",
	TimeoutConsequence: reuseRowResetStaysPending,
}

// finishedLaunch is one launch onto a row examined as finished, after the
// write that began it applied and the create returned: the verb's values;
// the tmux client (its lookup, for the re-lookup after "duplicate session"),
// start-time reader, configuration, this store's id, clock, logger (never
// nil) and caller identity; the identity write (nil makes none); the
// restore write, which writes the prior life back on the row at version
// (nil error and CondApplied when it applied); row, the row as examined
// before the write and never re-read (its instance id, recorded name, state,
// ended_at, pid and session-id presence, launch token and recorded server
// identity); the ad.provenance.disagree reasons the pre-launch lookup already
// wrote; and version, the row_version the write produced, on which the
// identity write is conditional.
type finishedLaunch struct {
	v               finishedLaunchVerb
	t               tmux.LookupClient
	pc              ProcChecker
	cfg             config.Config
	storeID         string
	now             func() time.Time
	lg              *log.Logger
	who             caller
	identity        spawn.IdentityWriter
	restoreWrite    func() (CondResult, error)
	row             Spawn
	disagreeWritten []string
	version         int64
}

// outcome maps the launch's create-and-label outcome out, for the create
// request req, to the verb's error, nil on success (SR-8.5, SR-10.4), the one
// place this mapping lives (SR-1.8), using internal/spawn's shared
// description builders:
//
//   - a labelled session: the identity write (spawn.RecordLaunchIdentity, when
//     l.identity is set) with l.version and req's token, whose failure only
//     logs; success;
//   - a lost reply: success with no identity;
//   - a timeout, or a non-zero-exit unparseable reply: ErrTmuxUnresponsive
//     with the launch-timeout description and the verb's TimeoutConsequence,
//     no write, the row staying pending;
//   - "duplicate session": heldName (one re-lookup, the restore, the
//     classified error, one ad.launch.name_held);
//   - every other outcome: the restore (l.restore), then the launch error
//     carrying the restore's row sentence: ErrTmuxNotAvailable for tmux
//     unavailable, ErrTmuxSessionCreate for a session that could not be
//     labelled (after its kill by id, or saying it may still run) and for any
//     other launch failure.
//
// Every error matches exactly one catalogued sentinel (SR-1.5).
func (l finishedLaunch) outcome(out spawn.CreateOutcome, req spawn.CreateRequest) error {
	switch out.Kind {
	case spawn.CreateLabelled:
		if l.identity != nil {
			spawn.RecordLaunchIdentity(l.identity, l.pc, l.lg, req.InstanceID, l.version, req.Token, out.Reply)
		}
		return nil
	case spawn.CreateLostReply:
		return nil
	case spawn.CreateUnresponsive:
		return spawn.LaunchTimeoutError(out.Cause, l.v.Verb, req.InstanceID, l.v.TimeoutConsequence)
	case spawn.CreateDuplicate:
		return l.heldName(req)
	}

	_, err := l.restore(func(restored string) error {
		switch out.Kind {
		case spawn.CreateUnavailable:
			return spawn.TmuxUnavailableError(out.Cause, req.Socket, restored)
		case spawn.CreateUnlabelledEnded, spawn.CreateUnlabelledRunning:
			return spawn.UnlabelledSessionError(out, req.Name, restored)
		}
		// CreateFailed.
		return spawn.CreateFailedError(out.Cause, req.Name, restored)
	})
	return err
}

// The restore's row sentences (SR-1.4, SR-8.5, SR-10.4): every launch error
// resume or reuse returns after its restore attempt carries exactly one of
// them, and restoreResultOf is the one place that picks it and the one place
// pkg/api declares the wording. The changed and absent sentences name the
// write that began the launch (finishedLaunchVerb.LaunchWrite).
const (
	// restoreSentenceApplied: the restore applied; the prior state follows.
	restoreSentenceApplied = "the row was restored to its prior state, "
	// restoreSentenceStaysPending: the restore failed with a store error.
	restoreSentenceStaysPending = "the row could not be restored and stays pending"
)

// restoreResult is one restore attempt's result as the verb reports it:
// Sentence, the row sentence of the launch error; RowResult, the
// ad.launch.name_held row_result and the re-lookup's ad.provenance.disagree
// action (SR-14: nameHeldRowRestored, nameHeldRowLeftChanged or
// nameHeldRowStillPending); and StoreErr, the restore's store error (nil
// unless the write failed).
type restoreResult struct {
	Sentence  string
	RowResult string
	StoreErr  error
}

// restoreResultOf maps the restore's outcome (res and its store error rerr,
// as the restore write returned them) to its restoreResult, the one mapping
// of the restore's result (SR-1.4, SR-8.5, SR-10.4, SR-14): applied gives
// restoreSentenceApplied with priorState, and restored; changed gives "the row
// changed after <launchWrite> and was left as it is" and absent "the row was
// removed after <launchWrite>, so nothing was restored", both left_changed; a
// store error gives restoreSentenceStaysPending, still_pending and the error.
// launchWrite names the write that began the launch ("resume moved it to
// pending", "this spawn reset it"). It makes no call, writes nothing and logs
// nothing.
func restoreResultOf(res CondResult, rerr error, priorState, launchWrite string) restoreResult {
	switch {
	case rerr != nil:
		return restoreResult{Sentence: restoreSentenceStaysPending, RowResult: nameHeldRowStillPending, StoreErr: rerr}
	case res == CondApplied:
		return restoreResult{Sentence: restoreSentenceApplied + priorState, RowResult: nameHeldRowRestored}
	case res == CondAbsent:
		return restoreResult{Sentence: "the row was removed after " + launchWrite + ", so nothing was restored", RowResult: nameHeldRowLeftChanged}
	}
	return restoreResult{Sentence: "the row changed after " + launchWrite + " and was left as it is", RowResult: nameHeldRowLeftChanged}
}

// restore makes the one restore attempt after a failed launch (SR-8.5,
// SR-10.4): l.restoreWrite, conditional on the row being pending at the
// version the write produced. It returns the launch error launchErr builds
// from the restore's row sentence (restoreResultOf): restored to the prior
// state; left as it is because the row changed or was removed (nothing
// written); or, on a store error, that the row could not be restored and
// stays pending, with one WARN line on the client logger naming the verb and
// the instance id (no token, label or environment value). It then emits the
// verb's restore event (ad.resume.restored or ad.spawn.reuse_restored:
// claude_instance_id, applied, launch_error named through errorName,
// restore_error, source), fail-open, and returns the restore's result too,
// for a trail record that follows it (ad.launch.name_held after "duplicate
// session").
func (l finishedLaunch) restore(launchErr func(restored string) error) (restoreResult, error) {
	id := l.row.ClaudeInstanceID
	res, rerr := l.restoreWrite()
	if rerr != nil {
		l.lg.Printf("WARN: %s: restoring instance %s to its prior state after a failed launch failed: %v", l.v.Verb, id, rerr)
	}
	restored := restoreResultOf(res, rerr, l.row.State, l.v.LaunchWrite)
	err := launchErr(restored.Sentence)

	var restoreError any
	if rerr != nil {
		restoreError = rerr.Error()
	}
	_ = trail.Emit(context.Background(), l.v.RestoredEvent, map[string]any{
		"claude_instance_id": id,
		"applied":            rerr == nil && res == CondApplied,
		"launch_error":       errorName(err),
		"restore_error":      restoreError,
		"source":             l.v.Source,
	})
	return restored, err
}

// heldName is the path after "duplicate session" (SR-8.5, SR-10.4, SR-1.4,
// SR-3.10, SR-4.2, SR-13.2 path (ii), SR-14, SR-3.16). req is the create's
// request: its name is the name the create asked for (resume's recorded
// name, reuse's requested name) and its socket the launch socket. In this
// order it:
//
//  1. makes exactly one tmux.Lookup on the launch socket for the row as
//     examined (rowLaunch over its instance id, its earlier launch token and
//     recorded server identity, this store's id, never the new launch's
//     token), with req's name as the holder name, then reads the clock once.
//     The write that began the launch cleared the row's identity columns, so
//     nothing is re-read. Any adoption the result offers is ignored, and no
//     agent process is read;
//  2. restores the row (restore, with its WARN line on a store error and its
//     restore event, whose launch_error names the error below);
//  3. builds the error with heldNameOutcome, judged against the examined row
//     (heldExaminedRow: req's name to quote, the examined ended_at, pid and
//     session-id presence, the configured bound and window, the clock
//     reading of step 1), with the restore's sentence as the row sentence
//     and retryLater as the retry sentence, so the ambiguous holder's
//     ErrTmuxUnresponsive ends with "retry later" as the pre-launch one does
//     (SR-18.1);
//  4. writes the re-lookup's ad.provenance.disagree records (emitDisagree,
//     action the restore's row result), name_changed compared against the
//     recorded name, skipping every reason the pre-launch lookup already
//     wrote (l.disagreeWritten), so each is written at most once per call,
//     each naming the session it is about (name_changed the row's own Ours
//     session, every other reason the name's holder, else the Ours session);
//     then exactly one ad.launch.name_held (the verb's source and launch,
//     req's name, the launch socket, this store's id, the holder facts, the
//     re-lookup's token, the error, the restore's row result and store error,
//     and the caller identity). Both are fail-open.
//
// The path adds only the one lookup to the create (lookup, create,
// re-lookup); it never labels, kills, captures or sends to the holding
// session. It always returns an error; only a holder that vanished before
// the re-lookup gives tmux.ErrTmuxSessionCreate.
func (l finishedLaunch) heldName(req spawn.CreateRequest) error {
	res := tmux.Lookup(l.t, l.pc, rowLaunch(req.InstanceID, l.row.Identity, l.storeID, req.Socket), req.Name)
	examined := heldExaminedRow{
		startingSessionRow: preLaunchRowOf(l.row, req.Name, req.Socket).startingSessionRow,
		Limits:             startingSessionLimitsOf(l.cfg.Tmux),
		Now:                l.now(),
	}

	var holder heldNameHolder
	restored, err := l.restore(func(sentence string) error {
		var herr error
		holder, herr = heldNameOutcome(res, req.InstanceID, req.Name, req.Socket, sentence, retryLater, &examined)
		return herr
	})

	// Each record names the session it is about (SR-14): name_changed, which
	// only an Ours verdict yields, the row's own session; every other reason
	// the name's holder, else the Ours session. name_changed is the last of
	// the emitter's order, so writing it second keeps that order.
	facts := lookupTrailFacts(res, l.row.TmuxSessionName)
	facts.Reasons = slices.DeleteFunc(facts.Reasons, func(reason string) bool {
		return slices.Contains(l.disagreeWritten, reason)
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
	l.emitDisagree(req.Socket, restored.RowResult, facts)
	if renamed {
		facts.SessionID = res.Session.ID
		facts.Reasons = []string{tmux.ReasonNameChanged}
		l.emitDisagree(req.Socket, restored.RowResult, facts)
	}
	emitNameHeld(nameHeld{
		Source:        l.v.Source,
		Launch:        l.v.NameHeldLaunch,
		InstanceID:    req.InstanceID,
		Name:          req.Name,
		Socket:        req.Socket,
		StoreID:       l.storeID,
		Holder:        holder,
		LookupOutcome: res.Token(),
		Err:           err,
		RowResult:     restored.RowResult,
		StoreError:    restored.StoreErr,
		Caller:        l.who,
	})
	return err
}

// emitDisagree writes one of the launch's lookups' ad.provenance.disagree
// records (emitFinishedRowDisagree) for l's verb and examined row.
func (l finishedLaunch) emitDisagree(socket, action string, pre preLaunchDecision) {
	emitFinishedRowDisagree(l.v, l.row, socket, l.who, action, pre)
}

// emitFinishedRowDisagree writes one lookup's ad.provenance.disagree records
// for a launch onto row, examined as finished, through the shared emitter
// (emitLookupDisagree; SR-14, SR-3.16), one per distinct reason in
// pre.Reasons and none in the normal case: the verb and source of v, the
// row's instance id and recorded name (never a requested one), the lookup's
// socket, the session concerned, the server value, the verdict token, action
// and the caller identity who. The pre-launch lookup's records (action
// refused, or proceeded to the launch) are written right after its decision,
// before any write, so no trail write falls between the write that begins
// the launch and the create (SR-8.3, SR-13.4); the re-lookup's after
// "duplicate session" follow the restore, with its row result as action
// (heldName). Fail-open.
func emitFinishedRowDisagree(v finishedLaunchVerb, row Spawn, socket string, who caller, action string, pre preLaunchDecision) {
	emitLookupDisagree(provenanceDisagree{
		Verb:        v.Verb,
		Source:      v.Source,
		InstanceID:  row.ClaudeInstanceID,
		Socket:      socket,
		SessionName: row.TmuxSessionName,
		Caller:      who,
	}, action, pre)
}
