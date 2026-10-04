package api

import (
	"fmt"
	"strconv"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// This file holds kill's operator-only finished-row opt-in (SR-6.5): its
// entry points (killFinished, run only by agent-director-admin's
// kill-finished through internal/adminapi, b.vqr), the live-row refusal, the
// start of the finished-row path, its Ours rows (SR-4.2 steps 1 and 2, then
// the reported-in rule of SR-6.7) and its two "never reported in" refusals.
// Nothing here is exported or named in any text shown to agents (SR-6.8,
// SR-18.15): no description carries the opt-in or a session-ending command
// (SR-1.4).

// killFinished is Kill with the finished-row opt-in set (SR-6.5): on a
// finished row (ended, missing) it ends the row's own old session, if that
// session reported in to the row; a live row, pending included, gets
// liveRowRefusal. Its ad.kill.called record carries include_finished true.
// The parameters are Kill's, with the row's id in place of KillParams.
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
// "list --tmux-session-name".
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
// switch (finishedOurs before the kill sequence on Ours,
// neverReportedInLeftoverError on Leftover, the live row's Gone and Can't
// tell handling otherwise).
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

// leftoverRefusal is the refusal of a Leftover lookup: on the finished-row
// path neverReportedInLeftoverError (SR-6.5), on a live row leftoverError
// (SR-6.1). Either sends no kill.
func (k *killRun) leftoverRefusal(leftovers []tmux.Session) error {
	if k.rowFinished() {
		return neverReportedInLeftoverError(k.id, leftovers)
	}
	return leftoverError(k.id, leftovers)
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
// ErrTmuxSessionConflict for a Leftover lookup (SR-6.5; SR-1.4 has no row of
// its own, so it follows the Ours case's): the instance id; "never reported
// in"; each leftover session's quoted name and tmux id with "this row's own
// id" (leftoverSessions); that an earlier launch's session never reported in
// to this row's latest launch; then neverReportedInTail. No age was judged,
// so it states no bound. It names no other id and no session-ending command
// (SR-6.8).
func neverReportedInLeftoverError(instanceID string, leftovers []tmux.Session) error {
	return fmt.Errorf("%w: instance %s: never reported in: %s; an earlier launch's session never reported in to this row's latest launch; %s",
		tmux.ErrTmuxSessionConflict, instanceID, leftoverSessions(leftovers), neverReportedInTail)
}

// adopt is the kill sequence's adoption step (SR-3.6, SR-6.1 step 1). On a
// finished row (SR-6.5) the identity findAdoption finds is used for this call
// only: no write, and no adopted reason, since the row's every field stays
// unchanged there. On a live row adoptIdentity also writes it, and adopted is
// collected when that write applied.
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
