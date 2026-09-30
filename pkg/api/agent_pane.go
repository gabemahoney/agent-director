package api

import (
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// This file holds the pane helpers the single-row verbs that act on a pane
// share (kill, read-pane, send-keys and pause): the agent's pane in a
// pane listing (SR-3.7) and the adoption of a lost create reply's identity
// (SR-3.6). The adoption rules live once, in findAdoption; the single-row
// verbs write through adoptIdentity with their own listing, and find-missing
// through adoptInSweep with the Sweep's listing (SR-3.6, SR-3.15, SR-11.6;
// LFR H2). The single-row helpers make no tmux call of their own (they take
// the verb's listing), read no environment and write no log line (SR-6.3);
// adoptInSweep's only tmux call is the Sweep's listing, and its only log line
// a store error's.

// agentPane finds the agent's pane in one pane listing (SR-3.7): an entry
// whose pane id is paneID and whose pane pid is panePID, wherever that pane
// now is. A shared pane (a grouped session, a linked window) is listed once
// per session showing its window; any one of those entries is the agent's
// pane, and the first in listing order is returned. A pane with the recorded
// id but another pid (respawned) is not the agent's pane. found is false when
// no entry matches, and always when paneID is empty or panePID is not
// positive (the row records no pane). Absence is decided from the listing
// only.
func agentPane(panes []tmux.Pane, paneID string, panePID int) (pane tmux.Pane, found bool) {
	if paneID == "" || panePID <= 0 {
		return tmux.Pane{}, false
	}
	for _, p := range panes {
		if p.ID == paneID && p.PID == panePID {
			return p, true
		}
	}
	return tmux.Pane{}, false
}

// identityAdopter is the adoption write's narrow store capability (SR-3.6;
// Appendix F.3): KillStore gains it, and Epic 11's SendKeysStore and
// PauseStore reuse it. *store.Store implements it.
type identityAdopter interface {
	AdoptIdentityIfUnchanged(instanceID string, examined RowSnapshot, id LaunchIdentity) (CondResult, error)
}

// adoption is what findAdoption found for one row, and whether its write
// applied (adoptIdentity, adoptInSweep).
type adoption struct {
	// Identity is the launch identity to use for this call: the row's
	// recorded one, with what adoption found filled in whatever the write's
	// outcome. Its pane identity (PaneID, PanePID, PaneStarttime) counts as
	// the row's recorded pane identity for agent-process selection in this
	// call (SR-3.8).
	Identity LaunchIdentity
	// Due reports that adoption was due: the lookup was Ours for a row that
	// records no server identity (Result.Adopt) or no pane.
	Due bool
	// Pane is the pane-by-token outcome when the row records no pane
	// (tmux.PaneByToken on the row's launch token): PaneOne adopted that
	// pane; PaneNone and PaneMany adopted none. It is PaneNone when the row
	// already records a pane or adoption was not due. find-missing's SR-11.3
	// check of a row that records no pane reads it (PaneNone counts as Gone,
	// PaneMany is unverified).
	Pane tmux.PaneMatch
	// Applied reports that the one adoption write applied; only then does
	// the verb or the sweep record adopted on ad.provenance.disagree (SR-3.6,
	// SR-14).
	Applied bool
}

// adoptionDue reports whether adoption is due for a row whose recorded launch
// identity is recorded, given its lookup's Result (SR-3.6): the lookup is
// Ours and the row records no server identity (res.Adopt) or no pane.
func adoptionDue(recorded LaunchIdentity, res tmux.Result) bool {
	return res.Verdict == tmux.Ours && (res.Adopt || recorded.PaneID == "")
}

// adoptionNeedsListing reports whether a due adoption needs a pane listing:
// the row records no pane, so its pane is looked for by token (SR-3.6).
func adoptionNeedsListing(recorded LaunchIdentity) bool {
	return recorded.PaneID == ""
}

// findAdoption is SR-3.6's identity decision and construction, without the
// write, over the row's recorded launch identity, the lookup's Result, a pane
// listing of the same socket and the start-time reader. It is the one place
// the adoption rules live; adoptIdentity (kill, send-keys, pause) and
// adoptInSweep (find-missing) add their own listing source and write, and
// they are the only writers of an adoption (SR-3.6; LFR H2). read-pane calls
// findAdoption directly with its own listing and uses what it found for that
// call only, with no write (SR-3.6, SR-3.7, SR-7.5); it takes no store
// capability, so it cannot write:
//
//   - It is due only when adoptionDue. Otherwise the recorded identity is
//     returned with Due false.
//   - The server identity, when the row records none, is the lookup's
//     answering server (res.ServerPID, res.ServerStart) with the server
//     process's start time from pc; a server identity the row records is
//     kept.
//   - The pane, when the row records none, is the one pane whose @ad_pane
//     names the row's launch token (tmux.PaneByToken; never a pane index),
//     with its pid's start time from pc. No such pane, or more than one,
//     adopts no pane: only the server identity is taken. panes is read only
//     then.
//   - A start time pc cannot read, or reads for a process that is gone, is
//     recorded as none.
//
// Applied is always false: the caller writes. It makes no tmux call, reads
// no clock or environment and writes no log line.
func findAdoption(recorded LaunchIdentity, res tmux.Result, panes []tmux.Pane, pc ProcChecker) adoption {
	a := adoption{Identity: recorded}
	if !adoptionDue(recorded, res) {
		return a
	}
	a.Due = true
	if res.Adopt {
		a.Identity.ServerPID = res.ServerPID
		a.Identity.ServerStart = res.ServerStart
		a.Identity.ServerStarttime = tmux.KnownStartTime(pc, res.ServerPID)
	}
	if adoptionNeedsListing(recorded) {
		var p tmux.Pane
		p, a.Pane = tmux.PaneByToken(panes, recorded.Token)
		if a.Pane == tmux.PaneOne {
			a.Identity.PaneID = p.ID
			a.Identity.PanePID = p.PID
			a.Identity.PaneStarttime = tmux.KnownStartTime(pc, p.PID)
		}
	}
	return a
}

// adoptIdentity is the adoption step of SR-3.6 for kill, send-keys and
// pause, over the row as the verb read it, the lookup's Result, the verb's
// own pane listing of the same socket and the start-time reader. The identity
// is decided and built by findAdoption. When it adds nothing the row lacks
// (adoption not due, or nothing found), no write is made. Otherwise one
// conditional write, AdoptIdentityIfUnchanged guarded on the snapshot the verb
// examined (row.Snapshot, SR-5.3), records it (LFR H2).
//
// Whatever the write's outcome (applied, changed, absent or a store error),
// the identity found is used for this call only and the verb's result does
// not change because of the write; Applied is true only when the write
// applied. No log line is written (SR-6.3). It makes no tmux call.
func adoptIdentity(s identityAdopter, row Spawn, res tmux.Result, panes []tmux.Pane, pc ProcChecker) adoption {
	a := findAdoption(row.Identity, res, panes, pc)
	if a.Identity == row.Identity {
		return a
	}
	cr, err := s.AdoptIdentityIfUnchanged(row.ClaudeInstanceID, row.Snapshot, a.Identity)
	a.Applied = err == nil && cr == CondApplied
	return a
}

// sameLifeAdopter is find-missing's adoption write (SR-3.6, SR-11.6; LFR H2;
// Appendix F.4): AdoptIdentityIfSameLife, guarded on a live state and the
// snapshot the sweep read, returning the row's new snapshot when it applied.
// *store.Store implements it.
type sameLifeAdopter interface {
	AdoptIdentityIfSameLife(instanceID string, examined RowSnapshot, id LaunchIdentity) (CondResult, RowSnapshot, error)
}

var _ sameLifeAdopter = (*store.Store)(nil)

// sweepAdoption is what adoptInSweep found and did for one find-missing row.
type sweepAdoption struct {
	// adoption is findAdoption's decision: Identity (the recorded identity
	// with what was found filled in; the recorded one when Unlisted), Due,
	// Pane (meaningful only when the row records no pane and Unlisted is
	// false) and Applied (the adoption write applied; only then does the
	// sweep record adopted on ad.provenance.disagree, SR-14).
	adoption
	// Unlisted reports that adoption was due for a row that records no pane
	// and the Sweep's pane listing of its socket did not answer (skipped,
	// failed, or its call spent the run budget): nothing was adopted, not
	// even the server identity, and no write was made. The listing's failure
	// has already stopped the socket through the Sweep; the row is still
	// judged as Ours (SR-3.6, SR-3.15, SR-11.3).
	Unlisted bool
	// Listing is the pane listing's own Result when Unlisted (PaneListing.Result:
	// Skipped, or what the listing's failure means for the row, such as Can't
	// tell (different_server) with server_mismatch on a no-server reply while
	// the recorded server is alive); the zero Result otherwise.
	// Its Disagree reasons are written on ad.provenance.disagree with its own
	// server and verdict (findMissingRow.listing; SR-3.16, SR-14).
	Listing tmux.Result
	// Guard is the snapshot the row's verdict write (note, clear or mark) is
	// guarded on (SR-11.6): the one the adoption write returned when it
	// applied, otherwise the one the sweep read. Unused when Left.
	Guard RowSnapshot
	// Write is the adoption write's outcome as a find-missing writer outcome
	// (Listed always false): Wrote false when no write was made; Res and Err
	// as the write returned, a store error already logged. findMissingAction
	// turns a refused or failed write into left_changed or store_error.
	Write findMissingWrite
	// Left reports that the adoption write found the row changed or absent,
	// or failed in the store: the row gets no verdict write and no tick and
	// is in neither result list (SR-11.6, SR-5.8).
	Left bool
}

// adoptInSweep is find-missing's adoption step (SR-3.6, SR-3.15, SR-11.6;
// LFR H2), taken for a row whose lookup Result res the sweep has, before the
// row is judged, with the rules of findAdoption:
//
//   - Not due (res is not Ours, or the row records its server identity and
//     its pane): no listing, no write; Guard is the snapshot read.
//   - Due for a row that records no pane: the socket's pane listing comes
//     only through sw.ListPanes(pl, launch), so the run takes at most one
//     listing per socket, under the socket's stop rule and the run budget
//     (SR-3.15, SR-13.5). A listing that is not Listed gives Unlisted, with
//     the listing's Result in Listing: no write, whatever res found, and no
//     log line. A due row that records its
//     pane (only its server identity is missing) needs no listing.
//   - When the identity found adds nothing the row lacks (no or several
//     token panes, server identity recorded), no write is made and Guard is
//     the snapshot read.
//   - Otherwise one AdoptIdentityIfSameLife guarded on it.Snapshot, the
//     snapshot the sweep read. Applied: Applied is set and Guard is the
//     returned snapshot. Changed or absent: Left. A store error: Left, logged
//     once on lg naming the instance id (a nil lg writes nothing), never a
//     sweep error.
//
// launch is the row's lookup view (its socket is the one the lookup used).
// It reads no clock and no environment, and makes no tmux call except the
// Sweep's listing.
func adoptInSweep(s sameLifeAdopter, sw *tmux.Sweep, pl tmux.PaneLister, it LiveSpawnIdentity, launch tmux.Launch, res tmux.Result, pc ProcChecker, lg FindMissingLogger) sweepAdoption {
	out := sweepAdoption{adoption: adoption{Identity: it.Identity}, Guard: it.Snapshot}
	if !adoptionDue(it.Identity, res) {
		return out
	}
	var panes []tmux.Pane
	if adoptionNeedsListing(it.Identity) {
		l := sw.ListPanes(pl, launch)
		if !l.Listed {
			out.Due, out.Unlisted, out.Listing = true, true, l.Result
			return out
		}
		panes = l.Panes
	}
	out.adoption = findAdoption(it.Identity, res, panes, pc)
	if out.Identity == it.Identity {
		return out
	}
	cr, now, err := s.AdoptIdentityIfSameLife(it.ClaudeInstanceID, it.Snapshot, out.Identity)
	out.Write = findMissingWrite{Wrote: true, Res: cr, Err: err}
	switch {
	case err != nil:
		out.Write.Res = 0
		logFindMissing(lg, "AdoptIdentityIfSameLife", it.ClaudeInstanceID, err)
		out.Left = true
	case cr != CondApplied:
		out.Left = true
	default:
		out.Applied = true
		out.Guard = now
	}
	return out
}
