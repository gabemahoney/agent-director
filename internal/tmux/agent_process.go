package tmux

// This file holds agent-process selection and the shared judgement of a
// recorded process (SRD SR-3.8, SR-11.1, SR-12.2; LFR M8, C1): which of a
// row's recorded identities is its agent process, and whether a recorded
// process is alive, judged only by the start-time reader. The lookup's
// server check, kill's wait and its Gone path, expire's process_alive,
// find-missing's process path and resume's running-process check use them;
// reuse's will too.
// They take plain values (no store type), read no clock and no environment,
// and make no tmux call.

// ProcIdentity is a recorded process identity: a pid and its start time as
// the ProcChecker reports it (SR-3.8). It is recorded when PID is positive; a
// start time with no positive pid is not recorded, and a positive pid with no
// start time is a recorded pid-only identity.
type ProcIdentity struct {
	// PID is the recorded pid; zero or negative means none.
	PID int
	// Starttime is the recorded start time, compared byte for byte; "" when
	// none was recorded.
	Starttime string
}

// recorded reports whether id counts as recorded: its pid is positive.
func (id ProcIdentity) recorded() bool { return id.PID > 0 }

// AgentSource names which recorded identity is a row's agent process. The
// zero value, AgentNone, means neither is recorded.
type AgentSource int

// The agent-process sources of SR-3.8.
const (
	// AgentNone: neither identity is recorded; the row has no process
	// evidence.
	AgentNone AgentSource = iota
	// AgentSessionStart: the SessionStart identity (pid, proc_starttime).
	AgentSessionStart
	// AgentPane: the recorded pane identity (pane_pid, pane_starttime).
	AgentPane
)

// AgentProcess is the selected agent process of a row.
type AgentProcess struct {
	// Source is the identity selected.
	Source AgentSource
	// Identity is the selected identity; zero when Source is AgentNone.
	Identity ProcIdentity
}

// SelectAgentProcess selects a row's agent process from its recorded
// SessionStart identity and its recorded pane identity (SR-3.8, SR-11.1,
// SR-12.2; LFR M8):
//
//   - only one recorded: that one;
//   - neither recorded: AgentNone;
//   - both recorded and they disagree: the pane identity. They disagree when
//     the pids differ, or when both start times are recorded and differ;
//   - both recorded and they agree (equal pids, with equal start times or
//     only one side recording one): the SessionStart identity as recorded.
//
// The pane process is the agent itself, launched as argv with no shell, and
// since WD 2026-09-29c SessionStart records the hook's parent, the pane
// process, so the two can differ only on a hand-edited row; the pane
// identity then wins. A disagreement is not logged: the ad.provenance.disagree
// reason pid_mismatch of LFR M8 is retired (SR-3.8, SR-14).
func SelectAgentProcess(sessionStart, pane ProcIdentity) AgentProcess {
	switch {
	case !sessionStart.recorded() && !pane.recorded():
		return AgentProcess{}
	case !pane.recorded():
		return AgentProcess{Source: AgentSessionStart, Identity: sessionStart}
	case !sessionStart.recorded() || disagree(sessionStart, pane):
		return AgentProcess{Source: AgentPane, Identity: pane}
	}
	return AgentProcess{Source: AgentSessionStart, Identity: sessionStart}
}

// disagree reports whether two recorded identities name different processes:
// the pids differ, or both start times are recorded and differ.
func disagree(a, b ProcIdentity) bool {
	if a.PID != b.PID {
		return true
	}
	return a.Starttime != "" && b.Starttime != "" && a.Starttime != b.Starttime
}

// ProcState is the judgement of a recorded process (SR-3.8). The zero value,
// ProcNone, means nothing is recorded.
type ProcState int

// The recorded-process judgements.
const (
	// ProcNone: no identity is recorded (pid not positive); the reader was
	// not called.
	ProcNone ProcState = iota
	// ProcAlive: the reader reports the pid alive with exactly the recorded
	// start time.
	ProcAlive
	// ProcGone: the reader reports the pid gone (no such process, a zombie),
	// or alive with a start time other than the recorded one.
	ProcGone
	// ProcUnknown: the reader cannot tell (unreadable), or the pid is alive
	// while no start time was recorded, so nothing shows it is the recorded
	// process. Never alive.
	ProcUnknown
)

// JudgeProcess judges the recorded process id with the start-time reader pc
// (SR-3.8; LFR C1), calling pc.StartTime at most once and never when id is
// not recorded:
//
//   - not recorded: ProcNone;
//   - unreadable: ProcUnknown;
//   - gone (absent or a zombie): ProcGone;
//   - alive while id records no start time (pid-only): ProcUnknown;
//   - alive with exactly the recorded start time: ProcAlive;
//   - alive with another start time (the pid was reused): ProcGone.
//
// It never consults a process environment and reads no clock.
func JudgeProcess(pc ProcChecker, id ProcIdentity) ProcState {
	if !id.recorded() {
		return ProcNone
	}
	start, alive, known := pc.StartTime(id.PID)
	switch {
	case !known:
		return ProcUnknown
	case !alive:
		return ProcGone
	case id.Starttime == "":
		return ProcUnknown
	case start == id.Starttime:
		return ProcAlive
	}
	return ProcGone
}

// KnownStartTime returns pid's process start time when the start-time reader
// pc answers alive and known, and "" (none recorded) otherwise: pid not
// positive, unreadable, or gone (SR-3.6, SR-3.8). It is the start time the
// identity write and adoption record, calling pc.StartTime at most once.
func KnownStartTime(pc ProcChecker, pid int) string {
	if pid <= 0 {
		return ""
	}
	start, alive, known := pc.StartTime(pid)
	if !alive || !known {
		return ""
	}
	return start
}
