package api

import (
	"context"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/trail"
)

// This file holds find-missing's per-row outcome writers (SR-11.3, SR-11.4,
// SR-11.6, SR-5.8): the guarded mark, whose transaction also denies the row's
// open permission requests, with its tick; the guarded move of a Codex row
// with an end report to ended (markEndedSameLife, shared with pause); the note
// write with SR-11.4's tick rule; the note clear; and the unreported note of a live pending row, under
// the same tick rule (b.kdf). The
// process path uses them, and Task 2's tmux path reuses them unchanged for
// its marks (tmux_absent, tmux_name_held) and notes, passing the snapshot its
// adoption produced as the guard when it adopted (SR-3.6, SR-11.6). They read
// no clock and no environment and make no tmux call.

// The ad.find_missing.tick event, its source and the values these writers
// compare or write (SR-11.4, SR-14).
const (
	findMissingTickEvent = "ad.find_missing.tick"
	// findMissingTickSource is the sweep's one source value
	// (nameHeldSourceFindMissing), shared by its ticks, its
	// ad.provenance.disagree records and its ad.launch.name_held records.
	findMissingTickSource = nameHeldSourceFindMissing
	// noteProvenanceConflict is the liveness note whose every entry, from no
	// note or from a different one, ticks (SR-11.4). The unreported note never
	// overwrites it (b.kdf; aliveRow).
	noteProvenanceConflict = store.LivenessNoteProvenanceConflict
)

// findMissingWrite is the outcome of one find-missing per-row writer.
type findMissingWrite struct {
	// Listed reports whether the row belongs in the writer's result list:
	// ids for a mark, unverified_ids for a note; never for a move to ended
	// (markEndedSameLife), a clear or an unreported note.
	Listed bool
	// Wrote reports whether a guarded write was attempted; false when the
	// writer's rule needed none (an equal note, a row with no note to clear,
	// a row already noted unreported).
	Wrote bool
	// Res is the guarded write's result; zero when none was attempted or the
	// store failed.
	Res CondResult
	// Err is the guarded write's store error, already logged on the
	// FindMissingLogger; nil otherwise.
	Err error
}

// markMissingSameLife is find-missing's mark of one row (SR-11.3, SR-11.6,
// SR-5.8), in the order SR-11.3 pins:
//
//  1. the guarded mark (FindMissingStore.MarkMissingIfSameLife), which
//     applies only while the row is live and still holds examined, the
//     snapshot the sweep read or the one its adoption produced, and folds in
//     the liveness clear and the launch-start clear; in the same transaction
//     it denies the row's open permission requests (SR-5.4; b.146 rule 12),
//     so the mark and the close are both written or neither is, and it
//     writes each closed request's permission_orphan_closeout tick once the
//     transaction commits;
//  2. when it applied, exactly one ad.find_missing.tick: instance_id,
//     prior_state (the state the mark replaced), new_state missing,
//     reconciliation_reason reason and source ad_find_missing, plus every
//     field of extra (Task 2 passes lookup_outcome, and tmux_session_name for
//     tmux_name_held); extra never replaces one of those five. Fail-open: a
//     trail-write failure is discarded.
//
// A mark that finds the row changed or absent, or fails in the store (its
// close included), applies nothing: no tick, no permission-request close,
// and the row is in neither result list (Listed false). A store error is
// logged on lg (a nil lg writes nothing) and never fails the sweep. Listed is
// true exactly when the mark applied, so the row goes in ids.
func markMissingSameLife(s FindMissingStore, id string, examined RowSnapshot, reason string, extra map[string]any, lg FindMissingLogger) findMissingWrite {
	prior, res, err := s.MarkMissingIfSameLife(id, examined)
	w := findMissingWrite{Wrote: true, Res: res, Err: err}
	if err != nil {
		w.Res = 0
		logFindMissing(lg, "MarkMissingIfSameLife", id, err)
		return w
	}
	if res != CondApplied {
		return w
	}
	emitMarkTick(id, prior, "missing", reason, extra)
	w.Listed = true
	return w
}

// emitMarkTick writes the one ad.find_missing.tick of a mark or a move that
// applied: instance_id, prior_state (the state the write replaced), new_state
// (missing or ended), reconciliation_reason reason and source ad_find_missing,
// plus every field of extra; extra never replaces one of those five.
// Fail-open: a trail-write failure is discarded. The mark and the Codex end
// (markEndedSameLife) share it, so find-missing and pause emit the same record.
func emitMarkTick(id, prior, newState, reason string, extra map[string]any) {
	fields := make(map[string]any, len(extra)+5)
	for k, v := range extra {
		fields[k] = v
	}
	fields["instance_id"] = id
	fields["prior_state"] = prior
	fields["new_state"] = newState
	fields["reconciliation_reason"] = reason
	fields["source"] = findMissingTickSource
	_ = trail.Emit(context.Background(), findMissingTickEvent, fields)
}

// EndedMover is the one store write the Codex end needs: the guarded move of a
// live Codex row holding an end report to ended (store.MarkEndedIfSameLife).
// FindMissingStore and PauseStore both carry it, so find-missing's sweep and
// pause's pre-check and wait polls make the one move through one writer
// (markEndedSameLife).
type EndedMover interface {
	// MarkEndedIfSameLife moves a live Codex row that holds an end report to
	// ended in one write guarded on examined, the row snapshot the caller
	// read, and closes the row's permission requests (an undecided one denied
	// with decision_reason ended, every one proven agent_gone) and its running
	// subagent entries in the same transaction. It returns the prior state and
	// CondApplied when it applied; CondChanged (not a Codex row, no end report
	// or a cleared one, terminal, or another snapshot) or CondAbsent, prior
	// state "", having written nothing; and a store error as an error with a
	// zero CondResult (SR-5.8), having written nothing.
	MarkEndedIfSameLife(instanceID string, examined RowSnapshot) (priorState string, res CondResult, err error)
}

// markEndedSameLife is the shared outcome writer of the Codex end (SR-6.5,
// SR-7.3, SR-7.5): the guarded move of one live Codex row, whose agent process
// the caller proved gone and which holds an end report, to ended
// (EndedMover.MarkEndedIfSameLife), guarded on examined. Find-missing's two
// gone-process paths and pause (before its state rules and in each wait poll)
// call it, so each emits the same records, in order:
//
//  1. the move's transaction, which closes the row's permission requests and
//     subagent records and, once it commits, writes each denied request's
//     ad.row_mutation.committed (writer find_missing) and each closed
//     request's permission_orphan_closeout tick;
//  2. when it applied, exactly one ad.find_missing.tick: instance_id,
//     prior_state (the state the move replaced), new_state ended,
//     reconciliation_reason reason (proc_absent), source ad_find_missing, plus
//     every field of extra. It emits no subagent record (the close writes
//     none) and no ad.spawn.state_transition.
//
// A move that finds the row changed or absent, or fails in the store (its
// closes included), applies nothing: no tick, no close. A store error is logged
// on lg (a nil lg writes nothing) and returned in the outcome's Err. Listed is
// always false: a row moved to ended is in neither ids nor unverified_ids.
func markEndedSameLife(s EndedMover, id string, examined RowSnapshot, reason string, extra map[string]any, lg FindMissingLogger) findMissingWrite {
	prior, res, err := s.MarkEndedIfSameLife(id, examined)
	w := findMissingWrite{Wrote: true, Res: res, Err: err}
	if err != nil {
		w.Res = 0
		logFindMissing(lg, "MarkEndedIfSameLife", id, err)
		return w
	}
	if res == CondApplied {
		emitMarkTick(id, prior, "ended", reason, extra)
	}
	return w
}

// writeUnreportedNote reports one live pending row that no hook has reported
// since its launch (b.kdf, b.146 rule 10; SR-11.4, SR-11.6, SR-5.8). readNote
// is the note the sweep read on the row ("" = none); the caller never passes
// a row noted provenance_conflict, which keeps its note (aliveRow), and the
// store write refuses one too. The state stays pending.
//
//   - readNote already unreported: nothing is written (no version change, so
//     repeated sweeps leave the row, and the time it was first flagged, as
//     they are) and no tick.
//   - otherwise the guarded note write
//     (FindMissingStore.NoteUnreportedIfSameLife), which sets the note to
//     unreported, keeps liveness_unverified_since when it is set (the time a
//     note first flagged the row, kept across note changes, so a row noted
//     unreported, then probe_eacces, then unreported again keeps its first
//     time) and sets it to the time of this write when it is NULL, and
//     applies only while the row is pending and still holds examined, the
//     snapshot the sweep read or the one its adoption produced, so a hook
//     that wrote first wins.
//     When it applies, at most one ad.find_missing.tick (prior_state and
//     new_state null), by the note write's rule (noteTickReason): from no
//     note it ticks unreported; from another note it ticks nothing.
//     Fail-open: a trail-write failure is discarded.
//   - a write that finds the row changed or absent, or fails in the store,
//     applies nothing and ticks nothing. A store error is logged on lg (a nil
//     lg writes nothing) and never fails the sweep.
//
// The row is in neither result list whatever the outcome (Listed is always
// false): never ids, since it is not marked, and not unverified_ids, since
// its agent process was checked and is alive.
func writeUnreportedNote(s FindMissingStore, id string, examined RowSnapshot, readNote string, lg FindMissingLogger) findMissingWrite {
	if readNote == noteUnreported {
		return findMissingWrite{}
	}
	res, err := s.NoteUnreportedIfSameLife(id, examined)
	if err != nil {
		logFindMissing(lg, "NoteUnreportedIfSameLife", id, err)
		return findMissingWrite{Wrote: true, Err: err}
	}
	if res == CondApplied {
		emitNoteTick(id, readNote, noteUnreported)
	}
	return findMissingWrite{Wrote: true, Res: res}
}

// writeLivenessNote records that one row was left unverified with note
// (SR-11.4, SR-11.6, SR-5.8). readNote is the note the sweep read on the row
// ("" = none).
//
//   - note equal to readNote: nothing is written (no version change) and no
//     tick; the row still belongs in unverified_ids.
//   - otherwise the guarded note write
//     (FindMissingStore.SetLivenessNoteIfSameLife), which overwrites the note,
//     keeps the first unverified time, and applies only while the row is live
//     and still holds examined. When it applies, at most one
//     ad.find_missing.tick (prior_state and new_state null), per
//     noteTickReason: entering provenance_conflict from no note or a
//     different note ticks provenance_conflict; going from no note to any
//     other note ticks that note's token; any other change ticks nothing.
//     Fail-open: a trail-write failure is discarded. The row belongs in
//     unverified_ids.
//   - a write that finds the row changed or absent, or fails in the store,
//     applies nothing, ticks nothing, and leaves the row in neither list. A
//     store error is logged on lg and never fails the sweep.
func writeLivenessNote(s FindMissingStore, id string, examined RowSnapshot, readNote, note string, lg FindMissingLogger) findMissingWrite {
	if note == readNote {
		return findMissingWrite{Listed: true}
	}
	res, err := s.SetLivenessNoteIfSameLife(id, examined, note)
	w := findMissingWrite{Wrote: true, Res: res, Err: err}
	if err != nil {
		w.Res = 0
		logFindMissing(lg, "SetLivenessNoteIfSameLife", id, err)
		return w
	}
	if res != CondApplied {
		return w
	}
	emitNoteTick(id, readNote, note)
	w.Listed = true
	return w
}

// emitNoteTick writes the ad.find_missing.tick of a note write that applied,
// from readNote to note, when noteTickReason says it ticks: instance_id,
// prior_state and new_state null, reconciliation_reason and source
// ad_find_missing. Fail-open: a trail-write failure is discarded.
func emitNoteTick(id, readNote, note string) {
	reason, ok := noteTickReason(readNote, note)
	if !ok {
		return
	}
	_ = trail.Emit(context.Background(), findMissingTickEvent, map[string]any{
		"instance_id":           id,
		"prior_state":           nil,
		"new_state":             nil,
		"reconciliation_reason": reason,
		"source":                findMissingTickSource,
	})
}

// noteTickReason is SR-11.4's tick rule for a note write that applied, from
// readNote (the note the sweep read, "" = none) to note (a different one). It
// returns the tick's reconciliation_reason and true when the write ticks:
// provenance_conflict when the row enters provenance_conflict, or note's
// token when the row had no note. Notes carry no quoted name (SR-11.4), so a
// note's token is the note itself. Any other change (between two notes other
// than provenance_conflict, unreported included, or from provenance_conflict
// to another note) returns false.
func noteTickReason(readNote, note string) (string, bool) {
	switch {
	case note == readNote:
		return "", false
	case note == noteProvenanceConflict:
		return noteProvenanceConflict, true
	case readNote == "":
		return note, true
	default:
		return "", false
	}
}

// clearLivenessNote clears the liveness note of one row whose agent process
// the sweep found alive (SR-11.4, SR-11.6, SR-5.8). readNote is the note the
// sweep read on the row ("" = none). A row with no note is not written, so an
// alive row's version does not advance on every sweep. Otherwise the guarded
// clear (FindMissingStore.ClearLivenessIfSameLife) NULLs the note and its
// unverified time, applying only while the row is live and still holds
// examined; a store error is logged on lg and never fails the sweep. A clear
// never ticks, and the row is in neither result list whatever the outcome
// (Listed is always false). A pending row the sweep notes unreported never
// comes here (aliveRow), so its note stays until the agent's next hook.
func clearLivenessNote(s FindMissingStore, id string, examined RowSnapshot, readNote string, lg FindMissingLogger) findMissingWrite {
	if readNote == "" {
		return findMissingWrite{}
	}
	res, err := s.ClearLivenessIfSameLife(id, examined)
	if err != nil {
		logFindMissing(lg, "ClearLivenessIfSameLife", id, err)
		return findMissingWrite{Wrote: true, Err: err}
	}
	return findMissingWrite{Wrote: true, Res: res}
}

// logFindMissing writes one per-row store-error line on lg, naming the store
// call and the row's instance id; a nil lg writes nothing. The sweep
// continues after it (SR-5.8).
func logFindMissing(lg FindMissingLogger, call, id string, err error) {
	if lg != nil {
		lg.Printf("find-missing: %s(%s): %v (continuing)", call, id, err)
	}
}
