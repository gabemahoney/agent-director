package api

import "github.com/gabemahoney/agent-director/internal/tmux"

// This file holds the pane helpers the single-row verbs that act on a pane
// share (kill, and in Epic 11 send-keys and pause): the agent's pane in a
// pane listing (SR-3.7) and the adoption of a lost create reply's identity
// (SR-3.6). They make no tmux call of their own (they take the verb's
// listing), read no environment and write no log line (SR-6.3).

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

// adoption is what adoptIdentity found and did for one verb call.
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
	// already records a pane or adoption was not due.
	Pane tmux.PaneMatch
	// Applied reports that the one adoption write applied; only then does
	// the verb record adopted on ad.provenance.disagree (SR-3.6, SR-14).
	Applied bool
}

// adoptIdentity is the adoption step of SR-3.6, shared by kill, send-keys
// and pause, over the row as the verb read it, the lookup's Result, the
// verb's pane listing of the same socket and the start-time reader:
//
//   - It is due only when res is Ours and the row records no server identity
//     (res.Adopt) or no pane (row.Identity.PaneID empty). Otherwise the
//     row's recorded identity is returned and nothing is written.
//   - The server identity, when the row records none, is the lookup's
//     answering server (res.ServerPID, res.ServerStart) with the server
//     process's start time from pc; a server identity the row records is
//     kept.
//   - The pane, when the row records none, is the one pane whose @ad_pane
//     names the row's launch token (tmux.PaneByToken; never a pane index),
//     with its pid's start time from pc. No such pane, or more than one,
//     adopts no pane: only the server identity is taken.
//   - A start time pc cannot read, or reads for a process that is gone, is
//     recorded as none.
//   - When the identity found adds nothing the row lacks, no write is made.
//     Otherwise one conditional write, AdoptIdentityIfUnchanged guarded on
//     the snapshot the verb examined (row.Snapshot, SR-5.3), records it.
//
// Whatever the write's outcome (applied, changed, absent or a store error),
// the identity found is used for this call only and the verb's result does
// not change because of the write; Applied is true only when the write
// applied. No log line is written (SR-6.3). It makes no tmux call.
func adoptIdentity(s identityAdopter, row Spawn, res tmux.Result, panes []tmux.Pane, pc ProcChecker) adoption {
	a := adoption{Identity: row.Identity}
	if res.Verdict != tmux.Ours || (!res.Adopt && row.Identity.PaneID != "") {
		return a
	}
	a.Due = true
	if res.Adopt {
		a.Identity.ServerPID = res.ServerPID
		a.Identity.ServerStart = res.ServerStart
		a.Identity.ServerStarttime = tmux.KnownStartTime(pc, res.ServerPID)
	}
	if row.Identity.PaneID == "" {
		var p tmux.Pane
		p, a.Pane = tmux.PaneByToken(panes, row.Identity.Token)
		if a.Pane == tmux.PaneOne {
			a.Identity.PaneID = p.ID
			a.Identity.PanePID = p.PID
			a.Identity.PaneStarttime = tmux.KnownStartTime(pc, p.PID)
		}
	}
	if a.Identity == row.Identity {
		return a
	}
	cr, err := s.AdoptIdentityIfUnchanged(row.ClaudeInstanceID, row.Snapshot, a.Identity)
	a.Applied = err == nil && cr == CondApplied
	return a
}
