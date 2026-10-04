package api

import (
	"log"
	"time"

	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// This file holds plain spawn's held-name path (SR-9.4; PO 2026-09-27 HELD
// and REVIEW): what runSpawn does when spawn.Launch reports that the
// session-creating call answered "duplicate session" (*spawn.HeldNameError).

// spawnHeldName is plain spawn's held-name path (SR-9.4, SR-5.8, SR-13.2,
// SR-14, SR-3.16). held carries the new pending row's instance id, the
// requested name, the launch socket, the launch token and the insert's launch
// start. In this order it:
//
//  1. ends the new row at once with the conditional end write
//     (store.EndHeldLaunch; ended_at is now's current time), before anything
//     else, so a competing write has as little time as possible to change
//     the row first. Applied gives heldRowEnded (row_result ended); changed
//     or absent gives heldRowLeftAsIs (left_changed), the row having been
//     changed by another versioned write or deleted and inserted afresh; a
//     store error leaves the row pending and gives heldRowStaysPending
//     (still_pending) and exactly one WARN line on lg naming the instance id
//     (never a token, label or environment value; a nil lg writes none);
//  2. makes exactly one tmux.Lookup on the launch socket for the new row's
//     launch identity (instance id, token, this store's id; no server
//     identity, since the create recorded none) with the requested name as
//     the holder name;
//  3. builds the classified error with heldNameOutcome, carrying exactly one
//     row sentence and, for an ErrTmuxUnresponsive (the re-lookup could not
//     answer), in place of "retry later", and for the ErrTmuxSessionCreate of
//     a holder that vanished first, the retry sentence chosen by the end
//     write's result: when it applied, heldRetryFree for both, else
//     heldRetryWait for both (SR-1.4; WD 2026-09-30d (a); b.1qq, b.c4u); each of
//     those descriptions names the instance id the sentence's "this id"
//     means;
//  4. writes the lookup's ad.provenance.disagree records (a scope value is
//     the one reason this lookup can report: no server identity is recorded
//     and no session carries the fresh token), then exactly one
//     ad.launch.name_held (source ad_spawn, launch spawn), both fail-open,
//     with the caller identity collected here, inside agent-director.
//
// It always returns an error. The end write makes no tmux call, so after the
// insert the path adds only the one lookup to the create; it never kills,
// captures, sends to or labels the holding session, and writes nothing else
// to the store.
func spawnHeldName(s *store.Store, t tmux.LookupClient, pc ProcChecker, now func() time.Time, lg *log.Logger, held *spawn.HeldNameError) error {
	sentence, rowResult, storeErr := endHeldRow(s, lg, held, now())

	res := tmux.Lookup(t, pc, tmux.Launch{
		InstanceID: held.InstanceID,
		Token:      held.Token,
		StoreID:    s.StoreID(),
		Socket:     held.Socket,
	}, held.Name)
	retry := heldRetrySentences{Unanswered: heldRetryWait, Vanished: heldRetryWait}
	if rowResult == nameHeldRowEnded {
		retry = heldRetrySentences{Unanswered: heldRetryFree, Vanished: heldRetryFree}
	}
	holder, err := heldNameOutcome(res, held.InstanceID, held.Name, held.Socket, sentence, retry, nil)

	who := callerIdentity()
	var holderID string
	if res.Holder != nil {
		holderID = res.Holder.ID
	}
	emitProvenanceDisagree(provenanceDisagree{
		Verb:        "spawn",
		Source:      nameHeldSourceSpawn,
		InstanceID:  held.InstanceID,
		Socket:      held.Socket,
		SessionName: held.Name,
		SessionID:   holderID,
		Server:      res.Server,
		Verdict:     res.Token(),
		Action:      rowResult,
		Caller:      who,
	}, res.Disagree...)
	emitNameHeld(nameHeld{
		Source:        nameHeldSourceSpawn,
		Launch:        nameHeldLaunchSpawn,
		InstanceID:    held.InstanceID,
		Name:          held.Name,
		Socket:        held.Socket,
		StoreID:       s.StoreID(),
		Holder:        holder,
		LookupOutcome: res.Token(),
		Err:           err,
		RowResult:     rowResult,
		StoreError:    storeErr,
		Caller:        who,
	})
	return err
}

// endHeldRow makes the held-name path's end write (store.EndHeldLaunch with
// the insert's launch start and endedAt) and returns the row sentence, the
// ad.launch.name_held row_result and the store error (nil unless the write
// failed). A store error leaves the row pending and writes one WARN line on
// lg naming only the instance id and the store's error (SR-5.8).
func endHeldRow(s *store.Store, lg *log.Logger, held *spawn.HeldNameError, endedAt time.Time) (sentence, rowResult string, storeErr error) {
	res, err := s.EndHeldLaunch(held.InstanceID, held.LaunchStartedAtMillis, endedAt)
	switch {
	case err != nil:
		if lg != nil {
			lg.Printf("WARN: spawn: ending instance %s after \"duplicate session\" failed; the row stays pending: %v", held.InstanceID, err)
		}
		return heldRowStaysPending, nameHeldRowStillPending, err
	case res == store.CondApplied:
		return heldRowEnded, nameHeldRowEnded, nil
	default:
		// CondChanged or CondAbsent: the row changed after the insert (another
		// versioned write, or deleted and inserted afresh); nothing was
		// written.
		return heldRowLeftAsIs, nameHeldRowLeftChanged, nil
	}
}
