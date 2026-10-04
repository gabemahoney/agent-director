package tmux

// This file holds the clock-free server check of SR-3.3 (RN-4; LFR C1, H5).
// It compares the answering server's #{pid} and #{start_time} with the row's
// recorded ones exactly, and judges the recorded server process only through
// the shared recorded-process judgement (JudgeProcess, agent_process.go)
// against the recorded start time. Every lookup answer, an empty listing
// included, carries the answering server's identity from its identity line
// (LFR H5; b.47f). It reads no clock and no environment: a tmux server
// agent-director started carries no AGENT_DIRECTOR_* variable, and must read
// as running, never as restarted.

// serverReply is what the socket answered, as the server check needs it.
type serverReply int

// The reply shapes of SR-3.3, as the server check tells them apart. An empty
// listing (a server with tmux's exit-empty off) is a listing: its identity
// line names the answering server like any other (LFR H5; b.47f).
const (
	// replyListing: a lookup answer, with or without session lines, which
	// names the answering server.
	replyListing serverReply = iota + 1
	// replyNoServer: the no-server or no-socket reply (SR-2.5), which names
	// no server: nothing answered.
	replyNoServer
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
// answered: reply, and ans for a listing, empty or not. It calls pc at most
// once, and only when the row records a server identity (Launch.ServerPID not
// zero) that the answer does not match. The mapping (u8 review amendments;
// LFR H5; b.47f):
//
//   - No identity recorded: ServerUnknown; whichever server answers counts,
//     and an empty listing or a no-server reply is Gone. pc is not called.
//   - A listing, empty or not, whose pid and start equal the recorded ones:
//     ServerMatch, so an empty listing of the recorded server is Gone. pc is
//     not called.
//   - Otherwise, by the recorded server process, judged by the shared
//     JudgeProcess on (ServerPID, ServerStarttime): ProcGone gives
//     ServerRestarted, and the verdict is taken on the answering server (a
//     listing, so an empty one is Gone), or is Gone (a no-server reply);
//     ProcAlive and ProcUnknown (unreadable, or alive with no recorded start
//     time) give ServerDiffers, Can't tell (different_server), and so does
//     ProcNone (a negative ServerPID, which the reader never gets: it would
//     answer unreadable for it).
//   - Reasons: ReasonServerRestarted when a listing, empty or not, carried
//     another server identity (AC-LKP-19); ReasonServerMismatch for every
//     different server; none for a match, or for a Gone from a no-server
//     reply, where nothing answered.
func checkServer(row Launch, pc ProcChecker, reply serverReply, ans LookupAnswer) serverCheck {
	if row.ServerPID == 0 {
		return serverCheck{server: ServerUnknown}
	}
	if reply == replyListing && ans.ServerPID == row.ServerPID && ans.ServerStart == row.ServerStart {
		return serverCheck{server: ServerMatch}
	}
	if JudgeProcess(pc, ProcIdentity{PID: row.ServerPID, Starttime: row.ServerStarttime}) != ProcGone {
		return serverCheck{server: ServerDiffers, differs: true, reason: ReasonServerMismatch}
	}
	if reply == replyListing {
		return serverCheck{server: ServerRestarted, reason: ReasonServerRestarted}
	}
	return serverCheck{server: ServerRestarted}
}
