package api

import (
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// This file holds kill's operator-only finished-row opt-in (SR-6.5): its
// entry points (killFinished, run only by agent-director-admin's
// kill-finished through internal/adminapi, b.vqr), the live-row refusal, the
// start of the finished-row path, its Ours rows (SR-4.2 steps 1 and 2, then
// the reported-in rule of SR-6.7), its Leftover rows (this id's own abandoned
// launch, ended past the starting-session bound, b.6sa; otherwise "never
// reported in") and its two "never reported in" refusals. Nothing here is
// exported or named in any text shown to agents (SR-6.8, SR-18.15): no
// description carries the opt-in or a session-ending command (SR-1.4). On
// every path the finished row's every field stays unchanged: an adoption is
// used for the call only, and nothing else here writes.

// killFinished is Kill with the finished-row opt-in set (SR-6.5): on a
// finished row (ended, missing) it ends the row's own old session, if that
// session reported in to the row, or, when the row's latest launch records no
// session of its own, this id's own abandoned launch once it has outlived the
// starting-session bound (abandonedLaunch, b.6sa); a live row, pending
// included, gets liveRowRefusal. Its ad.kill.called record carries
// include_finished true. The parameters are Kill's, with the row's id in
// place of KillParams.
func killFinished(s KillStore, t KillTmux, pc ProcChecker, startingSession, stoppingWindow, exitWait time.Duration,
	now func() time.Time, sleep func(time.Duration), claudeInstanceID string) (KillResult, error) {
	return runKill(s, t, pc, startingSession, stoppingWindow, exitWait, now, sleep, claudeInstanceID, true)
}

// killFinished runs killFinished on c's store, tmux client, start-time
// reader, clock and sleep with c's configured durations, as Client.Kill runs
// Kill.
func (c *Client) killFinished(claudeInstanceID string) (KillResult, error) {
	if err := c.checkClosed(); err != nil {
		return KillResult{}, err
	}
	t := c.cfg.Tmux
	return killFinished(c.st, c.tmuxClient, c.procChecker, t.EffectiveStartingSession(), t.EffectiveStoppingWindow(),
		t.EffectiveKillExitWait(), c.now, c.sleep, claudeInstanceID)
}

// neverReportedInTail ends both "never reported in" refusals (SR-1.4, SR-6.5):
// that no kill was sent, that send-keys refuses a finished row so ending the
// session is a human's decision, the "Operator actions" pointer and
// "list tmux_session_name".
const neverReportedInTail = "no kill was sent; send-keys refuses a finished row, so ending the session is a human's decision, " +
	operatorActionsPointer + "; " + listSessionNameHint

// rowFinished reports that the row as read is finished (ended, missing).
func (k *killRun) rowFinished() bool {
	return k.row.State == store.StateEnded || k.row.State == store.StateMissing
}

// withOptIn is the start of the kill flow when the opt-in is set, right after
// the row read (SR-6.5, SR-6.1): a live row, pending included, gets
// liveRowRefusal before the unusable-name check, any lookup or any tmux call.
// A finished row (ended, missing) takes the finished-row path, which never
// reaches the live row's unusable-name check in run. Its first row is SR-6.5's
// unusable recorded name (SR-3.2): unusableNameError's ErrInternal here,
// before the socket is resolved, with no tmux call. Otherwise nil, and run
// goes on to the socket, the one lookup and SR-6.5's table in its verdict
// switch (finishedOurs before the kill sequence on Ours; on Leftover, leftover:
// abandonedLaunch for a row whose latest launch records no session of its
// own, else neverReportedInLeftoverError; the live row's Gone and Can't tell
// handling otherwise).
func (k *killRun) withOptIn() error {
	if !k.rowFinished() {
		return liveRowRefusal(k.id, k.row.State)
	}
	if err := unusableNameError(k.row.TmuxSessionName); err != nil {
		return fmt.Errorf("instance %s: %w", k.id, err)
	}
	return nil
}

// liveRowRefusal is the opt-in's refusal of a live row (SR-6.5, SR-1.4; PRD
// OQ13): the instance id; "the row is live" and its state; that the
// finished-row option applies only to an ended or missing row; that no lookup
// was made and nothing was sent. It wraps only ErrSpawnNotResumable, which
// kill's manifest error list does not carry (SR-1.7).
func liveRowRefusal(instanceID, state string) error {
	return fmt.Errorf("%w: instance %s: the row is live (state %s); the finished-row option applies only to an ended or missing row; no lookup was made and nothing was sent",
		ErrSpawnNotResumable, instanceID, state)
}

// leftover answers a Leftover lookup of the row's current label on the
// row's socket, launch. On a live row it is leftoverError (SR-6.1). On the
// finished-row path (SR-6.5) it is abandonedLaunch when the row's latest
// launch records no session of its own (recordsNoLaunchSession: the
// leftovers are this id's own abandoned launch, b.6sa), and
// neverReportedInLeftoverError otherwise. Only abandonedLaunch can send a
// kill.
func (k *killRun) leftover(leftovers []tmux.Session, launch tmux.Launch) error {
	switch {
	case !k.rowFinished():
		return leftoverError(k.id, leftovers)
	case recordsNoLaunchSession(k.row.Identity):
		return k.abandonedLaunch(leftovers, launch)
	}
	return neverReportedInLeftoverError(k.id, leftovers)
}

// abandonedLaunch is the finished-row path's answer to a Leftover lookup
// while the row records a launch token but no tmux server or pane of that
// launch (recordsNoLaunchSession; b.6sa): the leftovers are this id's own
// abandoned launch, as resume's and reuse's lookups name them
// (abandonedLaunchError, b.1n6), and past the starting-session bound this is
// the one path that ends them. What qualifies is exactly what the lookup
// classes as Leftover: a session whose valid label carries this row's id and
// this store's id with another launch token. Another store's session and one
// with no valid label count toward Gone and are never listed, and
// conflicting labels are Can't tell, so each keeps its own answer. Such a
// launch can never report in (the hook gate needs a recorded pane), so
// SR-6.7's reported-in rule does not apply: the evidence that it is this
// id's is its label and the row recording no session of its latest launch.
// Ending it may end a claude that is working for this id untracked; a human
// chose to run the operator tool, as for every finished-row kill.
//
// The starting-session rule's age step alone runs on the youngest session
// (abandonedLaunchCheck: no ended_at, so no stopping window), with Kill's own
// bound as passed and the clock read once after the lookup. Still starting
// is abandonedStartingError's tmux.ErrTmuxUnresponsive ("nothing was done;
// retry later"), with no further tmux call and no kill sent. Past the bound
// the kill sequence runs on every one of the sessions: one pane listing (no
// server at the socket any more is decided as a Gone lookup, as on Ours, and
// a listing that cannot decide is its error, nothing sent), endAbandoned's
// kills, then the check, whose follow-up lookup, if the agent process cannot
// be checked, succeeds only once none of those sessions is listed. No
// adoption runs (the verdict is not Ours) and nothing is written.
func (k *killRun) abandonedLaunch(leftovers []tmux.Session, launch tmux.Launch) error {
	row := preLaunchRowOf(k.row, k.row.TmuxSessionName, k.socket).startingSessionRow
	lim := startingSessionLimits{Bound: k.startingSession, Window: k.stoppingWindow}
	if err := abandonedLaunchCheck(lim, k.now(), row, leftovers).abandonedStartingError(leftovers); err != nil {
		return err
	}
	panes, err := k.t.ListPanes(k.socket)
	if err != nil {
		return k.listingFailed(err, launch)
	}
	agent, listed, ended := k.endAbandoned(panes, leftovers)
	return k.check(agent, listed, launch, ended)
}

// endAbandoned sends the kill sequence's kills to the sessions of this id's
// own abandoned launch (abandonedLaunch), from one pane listing, and returns
// what the check needs: the agent process, the listed processes and the
// sessions' tmux ids.
//
// The row's recorded identities describe no session of that launch, so the
// agent process is never taken from them. Each session's agent pane is the
// one pane whose @ad_pane names that session's label token, wherever it now
// is (tmux.PaneByToken, as adoption and read-pane find a launch's pane): the
// pane that launch created, whose process is the agent itself (SR-3.8). None,
// or more than one, is no pane. Each agent pane's process has its start time
// read now, before any kill, as an adoption would record it (none when it
// cannot be read or is gone). The agent process the check judges is the
// youngest session's (the one the age step judged); with no agent pane there
// is none, so the check falls to the follow-up lookup. The listed processes
// are those of every pane the listing shows in the sessions
// (sessionProcesses), then each older session's agent pane process wherever
// its pane now is, so one moved out of those sessions is waited for too
// (appendProcesses: each pid once, never the agent's, and none whose start
// time could not be read, as sessionProcesses).
//
// Then, lowest $N first, for each session the pane kill of its agent pane
// (each pane once) and, always, the session kill by its tmux id; a failed or
// timed-out kill never stops it. It makes no other tmux call and no write.
func (k *killRun) endAbandoned(panes []tmux.Pane, sessions []tmux.Session) (agent tmux.AgentProcess, listed []tmux.ProcIdentity, ended []string) {
	youngest := youngestSession(sessions)
	sorted := sortedBySessionNumber(sessions)
	agentPanes := make([]string, len(sorted))
	var older []tmux.ProcIdentity
	for i, s := range sorted {
		ended = append(ended, s.ID)
		pane, match := tmux.PaneByToken(panes, s.Label.Token)
		if match != tmux.PaneOne {
			continue
		}
		agentPanes[i] = pane.ID
		own := tmux.ProcIdentity{PID: pane.PID, Starttime: tmux.KnownStartTime(k.pc, pane.PID)}
		if s.ID == youngest.ID {
			agent = tmux.SelectAgentProcess(tmux.ProcIdentity{}, own)
		} else {
			older = append(older, own)
		}
	}
	listed = appendProcesses(sessionProcesses(k.pc, panes, agent.Identity.PID, ended...), agent.Identity.PID, older)
	killed := map[string]bool{}
	for i, s := range sorted {
		if id := agentPanes[i]; id != "" && !killed[id] {
			killed[id] = true
			k.killPane(id)
		}
		_ = k.t.KillSessionID(k.socket, s.ID)
		k.killSent, k.sessionKilled = true, true
	}
	return agent, listed, ended
}

// appendProcesses appends to listed each process of more whose pid is not
// agentPID and not yet in listed, so each pid is waited for once. As in
// sessionProcesses, one with no pid or no start time (unreadable, or already
// gone) is not waited for (SR-18.12).
func appendProcesses(listed []tmux.ProcIdentity, agentPID int, more []tmux.ProcIdentity) []tmux.ProcIdentity {
	for _, p := range more {
		if p.PID <= 0 || p.Starttime == "" || p.PID == agentPID ||
			slices.ContainsFunc(listed, func(q tmux.ProcIdentity) bool { return q.PID == p.PID }) {
			continue
		}
		listed = append(listed, p)
	}
	return listed
}

// finishedOurs applies SR-6.5's Ours rows on the finished-row path, in
// resume's order, before the kill sequence; on a live row it returns nil at
// once. SR-4.2 steps 1 and 2 run through the shared starting-session rule,
// with Kill's own bound and window as passed (no configuration read, no
// fallback), the clock read once after the lookup and the row facts of kill's
// one row read; still stopping or still starting is its ErrTmuxUnresponsive.
// Past both, a session that did not report in to this row (reportedIn) gets
// neverReportedInError. nil means the caller runs the kill sequence. It makes
// no tmux call and no store read or write.
func (k *killRun) finishedOurs(res tmux.Result) error {
	if !k.rowFinished() {
		return nil
	}
	row := preLaunchRowOf(k.row, k.row.TmuxSessionName, k.socket).startingSessionRow
	lim := startingSessionLimits{Bound: k.startingSession, Window: k.stoppingWindow}
	c := checkStartingSession(lim, k.now(), row, &res.Session)
	if err := c.unavailableError(); err != nil {
		return err
	}
	if !reportedIn(row, res.Session) {
		return neverReportedInError(c)
	}
	return nil
}

// reportedIn is SR-6.7's reported-in rule (TLA+ v2 F2-1): whether the Ours
// session s, carrying the row's current label, reported in to this row. row
// must hold the facts of kill's one row read, made before the lookup, never a
// re-read; that is why a SessionStart landing after the lookup changes
// nothing (AC-KILL-15). Both must hold:
//
//  1. The row records a pid. Only SessionStart's identity write sets it; the
//     insert, reuse's reset and resume's move clear it, and a restore writes
//     the cleared value back. So on a finished row a pid means the row's
//     latest launch reported in. This relies on SessionStart being the row's
//     own agent's, which the hook gate guarantees: a hook applies only when
//     its parent process is the row's recorded pane process (SR-22.9; WD
//     2026-09-29 HOOK), so a leftover or a nested process can neither record
//     a pid on the row nor end it.
//  2. The row has an ended_at, and the session's creation time, epoch
//     seconds, is strictly earlier than ended_at in whole seconds: the
//     session was already running when the row finished.
//
// A recorded session id is not evidence: resume's move keeps it, so it can
// show only that an earlier launch reported in. Fails closed: no pid, no
// ended_at, or a creation time in the same second as ended_at or later is not
// reported in. It is pure: no clock, no tmux call, no store read, no write.
func reportedIn(row startingSessionRow, s tmux.Session) bool {
	return row.RecordsPID && row.EndedAt != nil && s.Created < row.EndedAt.Unix()
}

// neverReportedInError is the finished-row path's ErrTmuxSessionConflict for
// an Ours session past the stopping window and the starting-session bound
// that did not report in to this row (SR-6.5, SR-6.7; SR-1.4 row "a finished
// row's own session that never reported in"), built from the check that
// classified it: the instance id; the quoted recorded name; "this row's own
// id"; "never reported in"; that it has run for at least the starting-session
// bound (its effective value in seconds); then neverReportedInTail. It names
// no other id and no session-ending command (SR-6.8).
func neverReportedInError(c startingSessionCheck) error {
	return fmt.Errorf("%w: instance %s: session name %s: this row's own id: never reported in: it has run for at least the starting-session bound of %s, and no agent of this row's latest launch reported in from it before the row finished; %s",
		tmux.ErrTmuxSessionConflict, c.row.InstanceID, strconv.Quote(c.row.Name), inSeconds(c.class.Bound), neverReportedInTail)
}

// neverReportedInLeftoverError is the finished-row path's
// ErrTmuxSessionConflict for a Leftover lookup of a row whose latest launch
// records a session of its own (SR-6.5; SR-1.4 has no row of its own, so it
// follows the Ours case's); a row recordsNoLaunchSession matches takes
// abandonedLaunch instead (b.6sa). It gives the instance id; "never reported
// in"; each leftover session's quoted name and tmux id with "this row's own
// id" (leftoverSessions); that an earlier launch's session never reported in
// to this row's latest launch; then neverReportedInTail. No age was judged,
// so it states no bound. It names no other id and no session-ending command
// (SR-6.8).
func neverReportedInLeftoverError(instanceID string, leftovers []tmux.Session) error {
	return fmt.Errorf("%w: instance %s: never reported in: %s; an earlier launch's session never reported in to this row's latest launch; %s",
		tmux.ErrTmuxSessionConflict, instanceID, leftoverSessions(leftovers), neverReportedInTail)
}

// adopt is the kill sequence's adoption step on Ours (SR-3.6, SR-6.1 step 1).
// On a finished row (SR-6.5) the identity findAdoption finds is used for this
// call only: no write, and no adopted reason, since the row's every field
// stays unchanged there. On a live row adoptIdentity also writes it, and
// adopted is collected when that write applied. The kill sequence on this
// id's own abandoned launch (abandonedLaunch) never adopts: its sessions are
// not the row's current launch, so their identity is never the row's.
func (k *killRun) adopt(res tmux.Result, panes []tmux.Pane) adoption {
	if k.rowFinished() {
		return findAdoption(k.row.Identity, res, panes, k.pc)
	}
	a := adoptIdentity(k.s, k.row, res, panes, k.pc)
	if a.Applied {
		k.reasons = append(k.reasons, tmux.ReasonAdopted)
	}
	return a
}
