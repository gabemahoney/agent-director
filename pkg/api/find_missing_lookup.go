package api

import "github.com/gabemahoney/agent-director/internal/tmux"

// This file holds find-missing's tmux path (SR-11.3, SR-3.6, SR-3.15,
// SR-11.5, SR-13.5, SR-14): a row whose agent process cannot be checked is
// decided by its socket's lookup through the run's tmux.Sweep, and the
// outcome is written through the per-row writers of find_missing_writes.go.

// lookupRow decides a live row whose process evidence is unknown (state
// tmux.ProcUnknown: unreadable, or a pid-only identity reading alive) or
// absent (tmux.ProcNone: none recorded) by SR-11.3's table.
//
// The row's socket is its recorded one, or for a row that records none the
// caller's (sweepSockets.forRow); its lookup view is rowLaunch of its
// recorded launch identity with this store's id and that socket, and
// its recorded session name is the holder name. The lookup goes through the
// run's Sweep, which takes one lookup per socket and applies the stop rule
// and the budget. Then, on the typed Result:
//
//   - Skipped (not called: the socket stopped or the budget is spent), Can't
//     tell unreadable and tmux unavailable: unverified, note probe_eacces
//     (unknown) or process_not_seen_tmux_unchecked (absent). A Skipped result
//     carries no disagree reason.
//   - Ours: oursRow.
//   - Leftover and Gone: marked missing, whatever holds the recorded name
//     (lookupMarkRow).
//   - Can't tell, a different server: unverified, note tmux_server_changed.
//   - Can't tell, provenance_conflict: unverified, note provenance_conflict.
//
// The Result's disagree reasons, and name_changed for an Ours session whose
// stored name is not a stored form of the recorded name, join the row's set.
// Writes not preceded by an adoption write are guarded on the snapshot the
// sweep read.
func (r *findMissingRun) lookupRow(it LiveSpawnIdentity, state tmux.ProcState) findMissingRow {
	var (
		row    findMissingRow
		launch tmux.Launch
		ok     bool
	)
	row.socket, row.res, ok = r.sockets.forRow(it.Identity.Socket)
	if ok {
		launch = rowLaunch(it.ClaudeInstanceID, it.Identity, r.s.StoreID(), row.socket)
		row.res = r.sw.Lookup(launch, it.TmuxSessionName)
	}
	res := row.res
	notCalled := notCalledNote(state)
	if res.Skipped {
		return r.noteRow(row, it, it.Snapshot, notCalled)
	}
	row.disagree = append(row.disagree, res.Disagree...)
	switch res.Verdict {
	case tmux.Ours:
		if nameChanged(res, it.TmuxSessionName) {
			row.disagree = append(row.disagree, tmux.ReasonNameChanged)
		}
		return r.oursRow(row, it, state, launch)
	case tmux.Leftover, tmux.Gone:
		return r.lookupMarkRow(row, it, it.Snapshot)
	case tmux.CantTell:
		switch res.CantTell {
		case tmux.CantTellDifferentServer:
			return r.noteRow(row, it, it.Snapshot, noteTmuxServerChanged)
		case tmux.CantTellProvenanceConflict:
			return r.noteRow(row, it, it.Snapshot, noteProvenanceConflict)
		}
	}
	// Can't tell unreadable or tmux unavailable: the Sweep has stopped the
	// socket, so later rows of it are Skipped.
	return r.noteRow(row, it, it.Snapshot, notCalled)
}

// notCalledNote is SR-11.3's note for a row whose lookup did not answer
// (unreadable, tmux unavailable, or not called), by its process evidence:
// probe_eacces when it is unknown, process_not_seen_tmux_unchecked when none
// is recorded.
func notCalledNote(state tmux.ProcState) string {
	if state == tmux.ProcUnknown {
		return noteProbeEACCES
	}
	return noteProcessNotSeenTmuxUnchecked
}

// oursRow decides a row whose lookup is Ours (SR-11.3, SR-3.6, SR-11.6; LFR
// H2). The adoption step runs first (adoptInSweep): when due, a row that
// records no pane takes the socket's pane listing through the Sweep, and an
// identity that adds something is written, guarded on the snapshot the sweep
// read. A listing that did not answer keeps its own Result on the row
// (row.listing), so its disagree reasons (for example server_mismatch on a
// no-server reply while the recorded server is alive) are written with the
// listing's server and verdict, not the lookup's; the row's emitter writes
// each distinct reason once (SR-3.16, SR-14). A write that applied adds
// adopted to the row's disagree set and its snapshot guards the row's verdict
// write; a write that found the row changed or absent, or failed in the
// store, ends the row: no verdict write, no tick, in neither list.
//
// Then:
//
//   - evidence unknown: unverified, note probe_eacces, on the evidence the
//     sweep read;
//   - no evidence recorded, for a row that records no pane (a lost create
//     reply), the adoption listing decides: exactly one pane carrying the
//     launch token was adopted and the row is judged by that pane's process
//     (adoptedPaneRow); no pane carries it: the row counts as Gone and is
//     marked with tick reason tmux_absent, lookup_outcome the lookup's own
//     token, never tmux_name_held and with no ad.launch.name_held, although
//     its own session holds the name; the listing did not answer, or more
//     than one pane carries the token: unverified, note
//     process_not_seen_session_present;
//   - no evidence recorded for a row that records a pane id: unverified, note
//     process_not_seen_session_present.
func (r *findMissingRun) oursRow(row findMissingRow, it LiveSpawnIdentity, state tmux.ProcState, launch tmux.Launch) findMissingRow {
	sa := adoptInSweep(r.s, r.sw, r.t, it, launch, row.res, r.pc, r.lg)
	row.listing = sa.Listing
	if sa.Applied {
		row.disagree = append(row.disagree, tmux.ReasonAdopted)
	}
	if sa.Left {
		row.write = sa.Write
		return row
	}
	switch {
	case state == tmux.ProcUnknown:
		return r.noteRow(row, it, sa.Guard, noteProbeEACCES)
	case !adoptionNeedsListing(it.Identity), sa.Unlisted, sa.Pane == tmux.PaneMany:
		return r.noteRow(row, it, sa.Guard, noteProcessNotSeenSessionPresent)
	case sa.Pane == tmux.PaneNone:
		return r.markRow(row, it, sa.Guard, reasonTmuxAbsent, map[string]any{tickLookupOutcome: row.res.Token()})
	}
	return r.adoptedPaneRow(row, it, sa)
}

// adoptedPaneRow judges a row that recorded no process evidence by the pane
// its adoption found (SR-11.3, SR-11.1): the agent process is selected from
// the row's SessionStart identity and the adopted pane identity
// (tmux.SelectAgentProcess) and judged once (tmux.JudgeProcess). Alive: a
// note the row carries is cleared; gone: marked missing with reason
// proc_absent; unknown: unverified, note probe_eacces. Every write is guarded
// on sa.Guard, the adoption write's snapshot when it applied.
func (r *findMissingRun) adoptedPaneRow(row findMissingRow, it LiveSpawnIdentity, sa sweepAdoption) findMissingRow {
	agent := tmux.SelectAgentProcess(
		tmux.ProcIdentity{PID: it.PID, Starttime: it.ProcStarttime},
		tmux.ProcIdentity{PID: sa.Identity.PanePID, Starttime: sa.Identity.PaneStarttime},
	)
	switch tmux.JudgeProcess(r.pc, agent.Identity) {
	case tmux.ProcAlive:
		return r.clearRow(row, it, sa.Guard)
	case tmux.ProcGone:
		return r.markRow(row, it, sa.Guard, reasonProcAbsent, nil)
	case tmux.ProcUnknown:
		return r.noteRow(row, it, sa.Guard, noteProbeEACCES)
	}
	return r.noteRow(row, it, sa.Guard, noteProcessNotSeenSessionPresent)
}

// lookupMarkRow marks a row whose lookup is Leftover or Gone, whatever holds
// its recorded name (SR-11.3; PO 2026-09-27 LABEL; AC-FM-02, AC-FM-17,
// AC-FM-18), guarded on guard. The tick reason is tmux_name_held when a
// session holds the recorded name (one holder, or more than one listing
// entry matching it), with lookup_outcome the Result's token and
// tmux_session_name the recorded name; tmux_absent otherwise, with
// lookup_outcome. After a mark attempt with tmux_name_held, exactly one
// ad.launch.name_held follows (emitRowNameHeld), whatever the mark's
// outcome. The holding session is never touched.
func (r *findMissingRun) lookupMarkRow(row findMissingRow, it LiveSpawnIdentity, guard RowSnapshot) findMissingRow {
	extra := map[string]any{tickLookupOutcome: row.res.Token()}
	if row.res.Holder == nil && !row.res.HolderAmbiguous {
		return r.markRow(row, it, guard, reasonTmuxAbsent, extra)
	}
	extra[tickSessionName] = it.TmuxSessionName
	row = r.markRow(row, it, guard, reasonTmuxNameHeld, extra)
	r.emitRowNameHeld(it, row)
	return row
}

// emitRowNameHeld writes the one ad.launch.name_held record of a row whose
// mark attempt had tick reason tmux_name_held (SR-11.3, SR-14), through the
// shared emitter: source ad_find_missing, launch and outcome null, the
// recorded name, the socket the lookup used, this store's id, the holder
// facts of the row's lookup Result (heldHolderFacts: null holder fields when
// more than one entry matches the name), lookup_outcome the Result's token,
// the run's caller identity, and the row result from the mark: marked_missing
// when it applied, left_changed when the row was changed or absent, and
// still_pending with the store error's text on a store error. Fail-open.
func (r *findMissingRun) emitRowNameHeld(it LiveSpawnIdentity, row findMissingRow) {
	rec := nameHeld{
		Source:        nameHeldSourceFindMissing,
		InstanceID:    it.ClaudeInstanceID,
		Name:          it.TmuxSessionName,
		Socket:        row.socket,
		StoreID:       r.s.StoreID(),
		Holder:        heldHolderFacts(row.res),
		LookupOutcome: row.res.Token(),
		Caller:        r.callerOnce(),
	}
	switch {
	case row.write.Err != nil:
		rec.RowResult, rec.StoreError = nameHeldRowStillPending, row.write.Err
	case row.write.Res == CondApplied:
		rec.RowResult = nameHeldRowMarkedMissing
	default:
		rec.RowResult = nameHeldRowLeftChanged
	}
	emitNameHeld(rec)
}
