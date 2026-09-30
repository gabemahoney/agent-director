package tmux

// This file holds the clock-free server check of SR-3.3 (RN-4; LFR C1, H5).
// It compares the answering server's #{pid} and #{start_time} with the row's
// recorded ones exactly, and judges the recorded server process only through
// ProcChecker.StartTime against the recorded start time. It reads no clock
// and no environment: a tmux server agent-director started carries no
// AGENT_DIRECTOR_* variable, and must read as running, never as restarted.

// serverReply is what the socket answered, as the server check needs it.
type serverReply int

// The three reply shapes of SR-3.3.
const (
	// replyListing: a lookup answer with session lines, which carry the
	// answering server's identity.
	replyListing serverReply = iota + 1
	// replyEmptyListing: a lookup answer with no session line, which carries
	// no server identity (LFR H5).
	replyEmptyListing
	// replyNoServer: the no-server or no-socket reply (SR-2.5).
	replyNoServer
)

// recordedProcess is the state of the row's recorded server process as the
// start-time reader shows it.
type recordedProcess int

// The recorded server process's states.
const (
	// procGone: known and gone (absent, a zombie), or alive with a start time
	// other than the recorded one.
	procGone recordedProcess = iota + 1
	// procRunning: alive with the recorded start time.
	procRunning
	// procUnchecked: unreadable, or alive while no start time was recorded,
	// so nothing shows whether it is the recorded process.
	procUnchecked
)

// serverCheck is the outcome of the server check (SR-3.3).
type serverCheck struct {
	// server is Result.Server: ServerMatch, ServerRestarted, ServerDiffers or
	// ServerUnknown.
	server string
	// differs: a different server, Can't tell (different_server).
	differs bool
	// reason is the ad.provenance.disagree reason to log, or "".
	reason string
}

// checkServer runs the server check of SR-3.3 for row on what the socket
// answered: reply, and ans for a listing. It calls pc at most once, and only
// when the row records a server identity (Launch.ServerPID not zero) that the
// answer does not match. The mapping (u8 review amendments):
//
//   - No identity recorded: ServerUnknown; whichever server answers counts,
//     and an empty listing or a no-server reply is Gone. pc is not called.
//   - A listing whose pid and start equal the recorded ones: ServerMatch. pc
//     is not called.
//   - Otherwise, by the recorded process (recordedServer): gone gives
//     ServerRestarted, and the verdict is taken on the answering server (a
//     listing), or is Gone (an empty listing, a no-server reply); running or
//     unchecked gives ServerDiffers, Can't tell (different_server).
//   - Reasons: ReasonServerRestarted only when a listing carried another
//     server identity (AC-LKP-19); ReasonServerMismatch for every different
//     server; none for a Gone from an empty listing or a no-server reply.
func checkServer(row Launch, pc ProcChecker, reply serverReply, ans LookupAnswer) serverCheck {
	if row.ServerPID == 0 {
		return serverCheck{server: ServerUnknown}
	}
	if reply == replyListing && ans.ServerPID == row.ServerPID && ans.ServerStart == row.ServerStart {
		return serverCheck{server: ServerMatch}
	}
	if recordedServer(row, pc) != procGone {
		return serverCheck{server: ServerDiffers, differs: true, reason: ReasonServerMismatch}
	}
	if reply == replyListing {
		return serverCheck{server: ServerRestarted, reason: ReasonServerRestarted}
	}
	return serverCheck{server: ServerRestarted}
}

// recordedServer reads the row's recorded server process with the start-time
// reader, once: known and not alive is gone; alive with a start time other
// than the recorded ServerStarttime is gone (another process has the pid);
// alive with the recorded start time is running; unreadable, or alive while
// the row recorded no start time, cannot be checked (u8 review amendment:
// with no recorded start time only an absent or zombie process reads as
// gone).
func recordedServer(row Launch, pc ProcChecker) recordedProcess {
	start, alive, known := pc.StartTime(row.ServerPID)
	switch {
	case !known:
		return procUnchecked
	case !alive:
		return procGone
	case row.ServerStarttime == "":
		return procUnchecked
	case start == row.ServerStarttime:
		return procRunning
	}
	return procGone
}
