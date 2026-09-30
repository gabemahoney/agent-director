package api

import "github.com/gabemahoney/agent-director/internal/tmux"

// sweepSockets is a sweep run's socket rule for its rows (SR-3.3, SR-3.15,
// SR-13.5; LFR H1): the socket each row's lookup uses, with the caller's
// socket for rows that record none resolved at most once per run. find-missing
// holds one per run; expire (Epic 15) reuses it. The zero value is ready to
// use. It serves one run and is not for concurrent use.
type sweepSockets struct {
	// resolved reports that the caller's socket was resolved; socket and err
	// hold the outcome.
	resolved bool
	socket   string
	err      error
	// refused reports that a row with no recorded socket has already been
	// given the tmux-unavailable result of a refused resolution.
	refused bool
}

// forRow returns the socket a sweep row's lookup uses, given the row's
// recorded tmux_socket (SR-3.3):
//
//   - a recorded socket is used exactly as recorded; the caller's environment
//     is not consulted;
//   - for a row that records none (from before the release), the caller's
//     socket, resolved as tmux would through rowSocket
//     (spawn.ResolveQuerySocket), creating nothing (LFR H1), at most once per
//     run and only when such a row reaches the lookup. A missing per-user
//     directory is no refusal: its would-be socket path is returned, and the
//     lookup there reads tmux's no-socket reply (Gone).
//
// ok is false when the resolution refuses (an unusable socket directory): no
// tmux call may be made for the row, and res is its result, tmux unavailable
// (Can't tell) for the first such row of the run and Skipped (not_run) for
// every later one, so the run treats it as a socket that stopped (SR-3.15).
// res is the zero Result when ok is true. It makes no tmux call and charges
// nothing to the run's tmux time budget.
func (ss *sweepSockets) forRow(recorded string) (socket string, res tmux.Result, ok bool) {
	if recorded != "" {
		return recorded, tmux.Result{}, true
	}
	if !ss.resolved {
		ss.socket, ss.err = rowSocket("")
		ss.resolved = true
	}
	if ss.err == nil {
		return ss.socket, tmux.Result{}, true
	}
	if ss.refused {
		return "", tmux.Result{Skipped: true}, false
	}
	ss.refused = true
	return "", tmux.Result{Verdict: tmux.CantTell, CantTell: tmux.CantTellUnavailable}, false
}
