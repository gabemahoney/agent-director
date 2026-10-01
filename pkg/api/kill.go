package api

import (
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// KillStore is the narrow store surface Kill needs (SRD Appendix F.3):
// the row read, the adoption write of SR-3.6 (a lost create reply's server
// and pane identity, applied only if the row still has the snapshot Kill
// examined) and this store's id, which every label the lookup accepts ends
// with (SR-3.4; WD 2026-09-29 STORE). *store.Store satisfies it.
type KillStore interface {
	// GetSpawn reads the row; an unknown id is ErrSpawnNotFound.
	GetSpawn(instanceID string) (Spawn, error)
	// AdoptIdentityIfUnchanged records a found launch identity when the
	// row is still as examined (SR-3.6).
	AdoptIdentityIfUnchanged(instanceID string, examined RowSnapshot, id LaunchIdentity) (CondResult, error)
	// StoreID returns this store's store_meta.store_id.
	StoreID() string
}

// TmuxLookup is the lookup subset every narrow tmux interface embeds
// (Appendix F.3): the one-call lookup on a socket (SR-3.4).
type TmuxLookup interface {
	// Lookup makes the one-call lookup on socket.
	Lookup(socket string) (TmuxLookupAnswer, error)
}

// KillTmux is the narrow tmux surface Kill needs (Appendix F.3): the lookup,
// the pane listing, and the pane and session kills by id. TmuxClient,
// *tmux.Client and tmuxfix.Recorder satisfy it. Every method takes the
// row's socket (SR-3.3) and reports a failure as *TmuxCallError.
type KillTmux interface {
	TmuxLookup
	// ListPanes lists every pane of the server at socket.
	ListPanes(socket string) ([]TmuxPane, error)
	// KillPane kills the pane paneID on socket.
	KillPane(socket, paneID string) error
	// KillSessionID kills the session sessionID on socket.
	KillSessionID(socket, sessionID string) error
}

// The production types satisfy Kill's interfaces.
var (
	_ KillStore = (*store.Store)(nil)
	_ KillTmux  = TmuxClient(nil)
)

// KillParams is the typed parameter shape for the kill verb.
type KillParams struct {
	// ClaudeInstanceID identifies the Spawn whose agent kill ends.
	ClaudeInstanceID string `json:"claude_instance_id"`
	// Operator-only: see "Operator actions" in the agent-director README.
	IncludeFinished bool `json:"-"`
}

// KillResult is the typed return shape of the kill verb (SR-6.6).
type KillResult struct {
	// KillSent is true exactly when a pane kill or a session kill was sent.
	KillSent bool `json:"kill_sent"`
}

// killPollInterval is the pause between two readings of kill's process wait
// (SR-6.1): a named constant, not a setting.
const killPollInterval = 100 * time.Millisecond

// killSentConsequence is the consequence sentence of a follow-up lookup
// that cannot answer after a kill was sent (SR-1.4).
const killSentConsequence = "the kill was sent and may or may not have taken effect"

// The process_check values of ad.kill.called besides not_run (SR-6.4).
const (
	processCheckGone        = "gone"
	processCheckAlive       = "alive"
	processCheckUnreadable  = "unreadable"
	processCheckNotRecorded = "not_recorded"
)

// Kill ends the current launch's agent of a live row (SRD SR-6.1 to SR-6.4,
// SR-3.6, SR-3.7, SR-3.8, SR-3.11):
//
//   - Unknown id: ErrSpawnNotFound. A finished row (ended, missing): success,
//     kill_sent false, no tmux call. A live row (pending included) whose
//     recorded session name is unusable (SR-3.2): an ErrInternal-class
//     error, no tmux call.
//   - Otherwise one lookup by the row's current label on its recorded socket.
//     Ours: the pane listing (adopting a lost reply's identity first), the
//     pane kill by the agent's pane id, the session kill by the labelled
//     session's id, then the check. Leftover: ErrTmuxSessionConflict, no
//     kill. Gone: success with kill_sent false when the agent process is gone,
//     none is recorded or it cannot be checked; when it still runs, its pane
//     wherever it now is is killed and the check runs, or, with no pane of it
//     found, ErrTmuxKillFailed with no kill sent. Can't tell: its error.
//   - The check, never both: when the agent process can be checked, it and
//     every pane process the listing showed in the labelled session are
//     polled every killPollInterval, through now and sleep, for up to
//     exitWait; all gone is success with kill_sent true, anything still
//     running ErrTmuxKillFailed naming it. When it cannot, one follow-up
//     lookup decides.
//
// Kill never signals a process, never changes the row's state (only the
// adoption writes to the row) and writes no log line. It writes exactly one
// ad.kill.called trail event on every return path, and at most one
// ad.provenance.disagree per reason, both fail-open. The contract is per
// call: a later call checks only the processes it lists itself, so a process
// an earlier call's ErrTmuxKillFailed named is not checked again.
//
// Kill applies no fallback and no minimum: every duration is used as given.
// startingSession is the starting-session bound (safe minimum 60 s) and
// stoppingWindow the stopping window (safe minimum 30 s) of SR-4.2; exitWait
// is the kill exit wait, which has no safe minimum (too short a wait returns
// ErrTmuxKillFailed for an agent still exiting).
// The configuration file enforces the minimums (SR-4.1); a direct caller
// passes values at or above them. pc, now and sleep must not be nil, and
// sleep must advance the clock now reads: the wait ends only once now shows
// exitWait has passed, so a sleep that does not advance now loops forever.
func Kill(s KillStore, t KillTmux, pc ProcChecker, startingSession, stoppingWindow, exitWait time.Duration,
	now func() time.Time, sleep func(time.Duration), params KillParams) (result KillResult, err error) {
	k := &killRun{
		s: s, t: t, pc: pc, exitWait: exitWait, now: now, sleep: sleep,
		startingSession: startingSession, stoppingWindow: stoppingWindow,
		id:           params.ClaudeInstanceID,
		optIn:        params.IncludeFinished,
		lookup:       tmux.TokenNotRun,
		followup:     tmux.TokenNotRun,
		processCheck: tmux.TokenNotRun,
	}
	who := callerIdentity()
	defer func() { k.emit(who, err) }()
	if err = k.run(); err != nil {
		return KillResult{}, err
	}
	return KillResult{KillSent: k.killSent}, nil
}

// killRun is one Kill call: its inputs, the row and socket it acts on, and
// the per-call facts the trail events record (SR-6.4, SR-14).
type killRun struct {
	s        KillStore
	t        KillTmux
	pc       ProcChecker
	exitWait time.Duration
	now      func() time.Time
	sleep    func(time.Duration)
	id       string
	optIn    bool // the operator-only finished-row opt-in (SR-6.5)

	startingSession, stoppingWindow time.Duration // as passed (SR-4.2)

	row     Spawn
	socket  string
	context string // the quoted recorded name, as descriptions lead with it

	// Facts for ad.kill.called.
	lookup, followup, processCheck string
	killSent, paneKilled           bool
	sessionKilled                  bool
	agentPID                       int
	survivorPIDs                   []int

	// Facts for ad.provenance.disagree.
	reasons     []string
	server      string
	sessionID   string
	currentName string
}

// run is the kill flow; the returned error is Kill's.
func (k *killRun) run() error {
	row, err := k.s.GetSpawn(k.id)
	if err != nil {
		return err
	}
	k.row = row
	if k.optIn { // the live-row refusal, or the finished-row path (SR-6.5)
		if err := k.withOptIn(); err != nil {
			return err
		}
	} else if k.rowFinished() {
		return nil
	} else if err := unusableNameError(row.TmuxSessionName); err != nil {
		return fmt.Errorf("instance %s: %w", k.id, err)
	}
	socket, err := rowSocket(row.Identity.Socket)
	if err != nil {
		return fmt.Errorf("instance %s: %w", k.id, err)
	}
	k.socket = socket
	k.context = "tmux session " + strconv.Quote(row.TmuxSessionName)

	launch := k.launchFor(row.Identity)
	res := tmux.Lookup(k.t, k.pc, launch, row.TmuxSessionName)
	k.lookup, k.server = res.Token(), res.Server
	k.reasons = append(k.reasons, res.Disagree...)
	switch res.Verdict {
	case tmux.Ours:
		k.noteOurs(res)
		if err := k.finishedOurs(res); err != nil {
			return err
		}
		return k.ours(res, launch)
	case tmux.Leftover:
		return k.leftoverRefusal(res.Leftovers)
	case tmux.Gone:
		return k.gone(launch, true)
	}
	return k.cantTell(res, tmux.CallLookup, "")
}

// launchFor is the lookup's view of the row with identity id (rowLaunch),
// with this store's id and the row's socket.
func (k *killRun) launchFor(id LaunchIdentity) tmux.Launch {
	return rowLaunch(k.row.ClaudeInstanceID, id, k.s.StoreID(), k.socket)
}

// ours is the kill sequence on the labelled session (SR-6.1 steps 1 to 4).
// A failed or timed-out kill never stops it; the check decides.
func (k *killRun) ours(res tmux.Result, launch tmux.Launch) error {
	panes, err := k.t.ListPanes(k.socket)
	if err != nil {
		lres := tmux.ListingFailure(err, k.pc, launch)
		k.reasons = append(k.reasons, lres.Disagree...)
		if lres.Verdict == tmux.Gone {
			// No server at the socket any more: decided as for a Gone lookup,
			// with no pane to find.
			return k.gone(launch, false)
		}
		return k.cantTell(lres, tmux.CallListPanes, "")
	}
	a := k.adopt(res, panes)
	agent := agentProcess(k.row, a.Identity)
	listed := sessionProcesses(k.pc, panes, res.Session.ID, agent.Identity.PID)
	if pane, ok := agentPane(panes, a.Identity.PaneID, a.Identity.PanePID); ok {
		k.killPane(pane.ID)
	}
	_ = k.t.KillSessionID(k.socket, res.Session.ID)
	k.killSent, k.sessionKilled = true, true
	return k.check(agent, listed, k.launchFor(a.Identity))
}

// gone applies SR-6.1's Gone rows: the agent process gone, none recorded or
// not checkable is success with nothing sent; still running, its pane is
// looked for in one pane listing (when listPanes is set; a listing that
// already showed no server is not repeated) and killed, else
// ErrTmuxKillFailed with no kill sent.
func (k *killRun) gone(launch tmux.Launch, listPanes bool) error {
	agent := agentProcess(k.row, k.row.Identity)
	state := tmux.JudgeProcess(k.pc, agent.Identity)
	k.noteCheck(agent, state)
	if state != tmux.ProcAlive {
		return nil
	}
	if !listPanes {
		return k.noPaneError()
	}
	panes, err := k.t.ListPanes(k.socket)
	if err != nil {
		lres := tmux.ListingFailure(err, k.pc, launch)
		k.reasons = append(k.reasons, lres.Disagree...)
		if lres.Verdict == tmux.Gone {
			return k.noPaneError()
		}
		return k.cantTell(lres, tmux.CallListPanes, "")
	}
	pane, ok := agentPane(panes, k.row.Identity.PaneID, k.row.Identity.PanePID)
	if !ok {
		return k.noPaneError()
	}
	k.killPane(pane.ID)
	return k.check(agent, nil, launch)
}

// killPane sends the pane kill by id; its failure is ignored (SR-6.1).
func (k *killRun) killPane(paneID string) {
	_ = k.t.KillPane(k.socket, paneID)
	k.killSent, k.paneKilled = true, true
}

// check is SR-6.1 step 4 after a kill was sent: the first reading of the
// agent process decides whether it can be checked. Checkable (alive or gone):
// the process wait over it and listed. Not (none recorded, or unreadable):
// one follow-up lookup of launch, no wait.
func (k *killRun) check(agent tmux.AgentProcess, listed []tmux.ProcIdentity, launch tmux.Launch) error {
	first := tmux.JudgeProcess(k.pc, agent.Identity)
	k.noteCheck(agent, first)
	if first == tmux.ProcNone || first == tmux.ProcUnknown {
		return k.followUp(launch)
	}
	return k.wait(agent.Identity, first, listed)
}

// wait polls the agent process and the listed pane processes every
// killPollInterval for up to exitWait, reading now at each poll and pausing
// through sleep; the last pause is cut to end exactly at exitWait. A zombie
// counts as gone; a reading that cannot tell counts as still running. All
// gone: success. Otherwise ErrTmuxKillFailed naming every pid still running.
func (k *killRun) wait(agent tmux.ProcIdentity, state tmux.ProcState, listed []tmux.ProcIdentity) error {
	start := k.now()
	for {
		listed = slices.DeleteFunc(listed, func(p tmux.ProcIdentity) bool {
			return tmux.JudgeProcess(k.pc, p) == tmux.ProcGone
		})
		if state == tmux.ProcGone && len(listed) == 0 {
			k.processCheck = processCheckGone
			return nil
		}
		elapsed := k.now().Sub(start)
		if elapsed >= k.exitWait {
			break
		}
		k.sleep(min(killPollInterval, k.exitWait-elapsed))
		if state != tmux.ProcGone {
			state = tmux.JudgeProcess(k.pc, agent)
		}
	}
	agentRuns := state != tmux.ProcGone
	k.processCheck = processCheckGone
	if agentRuns {
		k.processCheck = processCheckAlive
	}
	for _, p := range listed {
		k.survivorPIDs = append(k.survivorPIDs, p.PID)
	}
	return k.waitExpiredError(agentRuns)
}

// followUp is the one follow-up lookup when the agent process cannot be
// checked after a kill (SR-6.1, SR-3.11): the current label gone (Gone or
// Leftover) is success; still there is ErrTmuxKillFailed; Can't tell is its
// error, saying the kill may or may not have taken effect.
func (k *killRun) followUp(launch tmux.Launch) error {
	res := tmux.Lookup(k.t, k.pc, launch, "")
	k.followup = res.Token()
	k.reasons = append(k.reasons, res.Disagree...)
	switch res.Verdict {
	case tmux.Gone, tmux.Leftover:
		return nil
	case tmux.Ours:
		return k.uncheckableError()
	}
	return k.cantTell(res, tmux.CallLookup, killSentConsequence)
}

// cantTell maps a Can't tell lookup or pane-listing result through the
// single-row verbs' shared mapping, with the recorded name as context.
// consequence "" means nothing was done.
func (k *killRun) cantTell(res tmux.Result, call tmux.Call, consequence string) error {
	return cantTellError(res, cantTellRefusal{
		InstanceID:  k.id,
		Context:     k.context,
		Socket:      k.socket,
		Call:        call,
		Consequence: consequence,
	})
}

// noteCheck records a process check's first reading and the checked pid.
func (k *killRun) noteCheck(agent tmux.AgentProcess, state tmux.ProcState) {
	k.processCheck = processCheckToken(state)
	k.agentPID = agent.Identity.PID
}

// processCheckToken is ad.kill.called's process_check value of a reading.
func processCheckToken(state tmux.ProcState) string {
	switch state {
	case tmux.ProcAlive:
		return processCheckAlive
	case tmux.ProcGone:
		return processCheckGone
	case tmux.ProcUnknown:
		return processCheckUnreadable
	}
	return processCheckNotRecorded
}

// agentProcess selects row's agent process (SR-3.8) from its SessionStart
// identity and the pane identity of id: the row's recorded identity, or the
// one adoption found for this call.
func agentProcess(row Spawn, id LaunchIdentity) tmux.AgentProcess {
	return tmux.SelectAgentProcess(
		tmux.ProcIdentity{PID: row.PID, Starttime: row.ProcStarttime},
		tmux.ProcIdentity{PID: id.PanePID, Starttime: id.PaneStarttime},
	)
}

// sessionProcesses returns the processes of every pane the listing shows in
// the session sessionID (linked windows included), each once by pid, with
// its start time read now (SR-6.1 step 1; WD 2026-09-29c). The agent's pid is
// left out, since the agent is judged by its recorded identity; a process
// whose start time cannot be read, or that is already gone, is not waited
// for (SR-18.12).
func sessionProcesses(pc ProcChecker, panes []tmux.Pane, sessionID string, agentPID int) []tmux.ProcIdentity {
	var out []tmux.ProcIdentity
	seen := map[int]bool{agentPID: true}
	for _, p := range panes {
		if p.SessionID != sessionID || p.PID <= 0 || seen[p.PID] {
			continue
		}
		seen[p.PID] = true
		if start := tmux.KnownStartTime(pc, p.PID); start != "" {
			out = append(out, tmux.ProcIdentity{PID: p.PID, Starttime: start})
		}
	}
	return out
}

// Kill ends the agent of a live row's current launch (pending included): the
// agent in the tmux session that carries the row's current launch label, on
// the row's recorded socket. It kills the agent's pane and that session by
// their tmux ids and succeeds only once the agent process is gone, waiting up
// to the configured kill exit wait (kill_exit_wait_ms). [KillResult.KillSent]
// reports whether a kill was sent. When no session of the launch is found,
// the agent process decides: gone, or none recorded, is success with
// KillSent false; still running, its pane is killed if another session still
// shows it, and otherwise the call fails with no kill sent.
//
// A finished row (ended, missing) is a no-op success with KillSent false and
// no tmux call. That is not verification, and not proof, that the agent
// exited: missing is the sweep's judgement on the evidence available to it,
// not proof that the agent has exited, and neither ended nor missing means
// that the agent is dead or that its row is safe to delete. On a pending row Kill aborts only the current launch; a call made
// before the launch created its session returns KillSent false and does not
// stop the launch. Kill never ends a session an earlier launch left behind.
//
// Kill never changes the row's state: find-missing marks the row once its
// agent process is gone. Kill never signals a process itself, so success
// means the agent process exited, not every process it started. Kill writes
// no log lines: the returned error is the report, and the ad.kill.called
// trail event is the audit.
//
// Success is judged per call: Kill succeeds when the agent process and every
// other process it found in the panes of the agent's session are gone. If
// ErrTmuxKillFailed named another process that outlived the kill (its pid is
// in the error), that process is not the agent and later calls do not track
// it: a retried call checks only the agent process, so once the agent is gone
// it succeeds with KillSent false whether or not that process still runs. A
// retry's success means only that the agent is gone; the named process needs
// a human (see "Operator actions" in the agent-director README). If the row
// finishes while Kill waits and the agent outlives the wait, Kill returns
// ErrTmuxKillFailed, and a retried call is a finished-row no-op.
//
// Error classes: ErrTmuxKillFailed and ErrTmuxUnresponsive are UNAVAILABLE
// (retry later), ErrTmuxSessionConflict is CONFLICT (permanent until a human
// looks; see "Operator actions" in the agent-director README) and
// ErrTmuxNotAvailable is ENVIRONMENT; for Kill, GONE is success. A repeated
// Kill right after the last session on its tmux server ends can get
// ErrTmuxUnresponsive or ErrTmuxNotAvailable while the server exits; the
// caller waits and checks again. None of these errors means that the agent is
// dead, and a caller never deletes a row after a Kill that did not succeed.
// The live-row sequence in the agent-director README's caller-contract
// summary says what a caller does next.
//
// The caller must run as the same user and in the same tmux environment as
// the agents. Two consequences: Kill's success on a finished row is not
// verification that the agent exited; and on the wrong tmux server, a row
// wrongly marked missing, Kill's no-op success and a reuse together start a
// second agent for the same id.
//
// A live row whose recorded tmux session name cannot be used (it is empty,
// contains a control character, or contains a character tmux stores
// differently) gets ErrInternal (an error matching no catalogued sentinel)
// with no tmux call; removing the row is a human's decision (see "Operator
// actions" in the agent-director README).
//
// CLI: agent-director kill
//
// Errors:
//   - [ErrSpawnNotFound]: no row exists for the instance id.
//   - [ErrTmuxNotAvailable]: the tmux binary could not be run, the socket is
//     not accessible to this user, or this is not the tmux server the agent
//     was launched on. When the lookup or the pane listing hit it, no kill
//     was sent; when the follow-up lookup after a sent kill hit it, the kill
//     may or may not have taken effect.
//   - [ErrTmuxKillFailed]: the agent process still runs after kill: a kill
//     was sent and the agent process (or another process of a pane of its
//     session) outlived the kill exit wait, or the process cannot be checked
//     and its labelled session is still there, or no session or pane of this
//     launch was found while the process runs and no kill was sent.
//   - [ErrTmuxUnresponsive]: tmux did not answer usably (the lookup could not
//     be read, or a kill was sent but its follow-up could not answer).
//   - [ErrTmuxSessionConflict]: the session found is not this launch's
//     session, or tmux holds conflicting labels; no kill was sent.
//
// Nondeterminism: none.
func (c *Client) Kill(params KillParams) (KillResult, error) {
	if err := c.checkClosed(); err != nil {
		return KillResult{}, err
	}
	t := c.cfg.Tmux
	return Kill(c.st, c.tmuxClient, c.procChecker, t.EffectiveStartingSession(), t.EffectiveStoppingWindow(),
		t.EffectiveKillExitWait(), c.now, c.sleep, params)
}
