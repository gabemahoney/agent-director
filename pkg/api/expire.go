package api

import (
	"slices"
	"sort"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// ExpireStore is the narrow store surface Expire needs (SRD Appendix F.4):
// the candidate read, the per-row conditional delete and this store's id,
// which every label the lookup accepts ends with (SR-3.4; WD 2026-09-29
// STORE). *store.Store satisfies it.
type ExpireStore interface {
	// ListExpireCandidates reads every finished row (ended or missing) whose
	// ended_at is set and older than cutoff, with its recorded session name,
	// SessionStart identity, launch identity and row snapshot, without
	// decoding any structured field (SR-12.1). A failure fails the verb.
	ListExpireCandidates(cutoff time.Time) ([]ExpireCandidate, error)
	// DeleteFinishedIfSameLife deletes the row only while it is still
	// finished and holds examined, the snapshot Expire read (SR-12.3,
	// SR-5.3). It returns CondApplied when it deleted the row, CondChanged
	// when the row exists but changed, CondAbsent when the row is already
	// gone, and a store error as an error with a zero CondResult (SR-5.8).
	DeleteFinishedIfSameLife(instanceID string, examined RowSnapshot) (CondResult, error)
	// StoreID returns this store's store_meta.store_id, the id every label
	// the lookup accepts ends with (SR-3.4; WD 2026-09-29 STORE).
	StoreID() string
}

// ExpireTmux is expire's tmux interface (Appendix F.4): only the one-call
// lookup on a socket. Expire takes it only through one tmux.Sweep per run:
// at most one lookup per socket, the per-socket stop rule and the run's tmux
// time budget (SR-3.15, SR-12.5, SR-13.5). It lists no panes, kills nothing
// and writes no label. TmuxClient, *tmux.Client and tmuxfix.Recorder satisfy
// it.
type ExpireTmux interface {
	TmuxLookup
}

// The production types satisfy expire's interfaces.
var (
	_ ExpireStore = (*store.Store)(nil)
	_ ExpireTmux  = TmuxClient(nil)
	_ ExpireTmux  = (*tmux.Client)(nil)
)

// ExpireResult is the typed return shape: the rows expire deleted and the
// rows it kept, each list sorted and non-nil, so JSON encodes `[]` even on a
// run that selected no row (SR-12.4). Every selected row is in exactly one of
// IDs and KeptIDs, except a row another caller removed first, which is in
// neither.
type ExpireResult struct {
	// Count is the number of rows deleted (the length of IDs): rows deleted
	// after tmux showed no session of the agent (Gone) and its recorded
	// process was not seen running. Zero is a legitimate result.
	Count int `json:"count"`
	// IDs is the sorted slice of instance ids deleted after tmux showed no
	// session of the agent (Gone) and its recorded agent process was not
	// seen running, each deleted only while the row still held the snapshot
	// expire examined. Always non-nil — encodes as [] when no row was
	// deleted.
	IDs []string `json:"ids"`
	// Kept is the number of selected rows kept rather than deleted: the
	// length of KeptIDs.
	Kept int `json:"kept"`
	// KeptIDs is the sorted slice of instance ids kept rather than deleted,
	// for example rows whose recorded session name cannot be used (kept on
	// every run, first, with no tmux call), whose agent process may still
	// run, whose agent still has a session (its own or one left from an
	// earlier launch), whose tmux could not be read, answered from a
	// different server or was not called, or which changed after expire
	// examined them or whose delete failed in the store. Each is reported
	// with its reason in an
	// ad.expire.kept trail record. Always non-nil — encodes as [] when no
	// row was kept.
	KeptIDs []string `json:"kept_ids"`
}

// ExpireLogger is the narrow log surface Expire writes the candidate read's
// failure and per-row store errors to. *log.Logger satisfies it.
type ExpireLogger interface {
	Printf(format string, v ...any)
}

// The kept reasons of SR-12.2 and SR-12.3, one per kept row, carried by its
// ad.expire.kept record (SR-12.5) and as the action of its
// ad.provenance.disagree records (SR-14). The three unusable-name reasons
// (keptEmptySessionName, keptControlCharSessionName,
// keptRewrittenSessionName) are checked first, before the process check.
const (
	// keptEmptySessionName, keptControlCharSessionName and
	// keptRewrittenSessionName: the row's recorded session name cannot be
	// used (SR-3.2, SR-12.2): empty, holding a control character, or holding
	// a character tmux stores differently (`.`, `:` or invalid UTF-8). The
	// row is kept on every run, ahead of every other check, with no
	// start-time reader call and no tmux call (unusableKeptReason); removing
	// it is a human's decision.
	keptEmptySessionName       = "empty_session_name"
	keptControlCharSessionName = "control_char_session_name"
	keptRewrittenSessionName   = "rewritten_session_name"
	// keptProcessAlive: the row's recorded agent process runs with its
	// recorded start time; no tmux call was made for the row.
	keptProcessAlive = "process_alive"
	// keptTmuxSkipped: no lookup was made for the row, because its socket
	// stopped after an unreadable or unavailable answer, the run's tmux time
	// budget was spent, or the caller's socket could not be resolved for an
	// earlier row that records none.
	keptTmuxSkipped = "tmux_skipped"
	// keptLeftoverRunning: the lookup found a session of an earlier launch
	// of this agent (Leftover).
	keptLeftoverRunning = "leftover_running"
	// keptOurs: the lookup found the agent's own session (Ours), however
	// recently the row ended.
	keptOurs = "ours"
	// keptTmuxServerChanged: Can't tell, the socket's server is not the one
	// the agent was launched on.
	keptTmuxServerChanged = "tmux_server_changed"
	// keptProvenanceConflict: Can't tell, conflicting labels (a scope value
	// or two sessions carrying the current label).
	keptProvenanceConflict = "provenance_conflict"
	// keptCantTell: Can't tell, an unreadable answer (a timeout, an
	// unrecognised reply or a malformed answer); the socket stops.
	keptCantTell = "cant_tell"
	// keptTmuxUnavailable: tmux could not be run, refused the socket to this
	// user, or the caller's socket could not be resolved for a row that
	// records none; the socket stops.
	keptTmuxUnavailable = "tmux_unavailable"
	// keptChangedSinceExamined: the lookup was Gone but the conditional
	// delete found the row changed since expire examined it.
	keptChangedSinceExamined = "changed_since_examined"
	// keptStoreError: the lookup was Gone but the conditional delete failed
	// in the store; the error was logged and the run continued.
	keptStoreError = "store_error"
)

// unusableKeptReason is the one mapping from the unusable-name guard's kind
// (tmux.Unusable) to expire's kept reason (SR-3.2, SR-12.2); "" for a usable
// name. The guard's precedence (empty, then control character, then
// rewritten) decides a name with several faults.
func unusableKeptReason(kind tmux.UnusableKind) string {
	switch kind {
	case tmux.UnusableEmpty:
		return keptEmptySessionName
	case tmux.UnusableControl:
		return keptControlCharSessionName
	case tmux.UnusableRewritten:
		return keptRewrittenSessionName
	}
	return ""
}

// farFutureCutoff is the cutoff of a zero or negative retention window: later
// than any ended_at the store writes, so every finished row with an ended_at
// is selected (SR-12.1).
var farFutureCutoff = time.Date(9999, time.December, 31, 23, 59, 59, 0, time.UTC)

// expireRow is the outcome of judging one selected row, and the facts its
// trail records carry (SR-12.2, SR-12.3, SR-12.5, SR-14).
type expireRow struct {
	// deleted reports that the conditional delete applied: the row is in ids.
	deleted bool
	// reason is the kept reason (keptProcessAlive, ...) of a kept row; ""
	// for a deleted row and for a row another caller removed first.
	reason string
	// socket is the socket the row's lookup used; "" when no lookup was made
	// (an unusable recorded name, process_alive) or the caller's socket could
	// not be resolved.
	socket string
	// res is the row's lookup Result: the Sweep's, or sweepSockets.forRow's
	// for a refused resolution; the zero Result for a row kept for an
	// unusable recorded name or process_alive.
	res tmux.Result
	// disagree is the row's ad.provenance.disagree reasons: the lookup
	// Result's and name_changed for an Ours session whose stored name is not
	// a stored form of the recorded name (nameChanged). A row without a
	// lookup, a Skipped one included, has none.
	disagree []string
}

// verdict is the row's lookup outcome token (tmux.Result.Token), not_run when
// no lookup ran for it.
func (r expireRow) verdict() string {
	if tok := r.res.Token(); tok != "" {
		return tok
	}
	return tmux.TokenNotRun
}

// Expire is the expire verb (SR-12, SR-16.2 item 6; Appendix F.4);
// (c *Client).Expire calls it with the Client's store, tmux client,
// start-time reader, retention setting, effective sweep budget, clock and
// logger.
//
// Selection (SR-12.1): the cutoff is now() minus the retention window, which
// is olderThan when it is non-nil and retentionDays days otherwise; a zero or
// negative window selects every finished row with an ended_at. The one
// candidate read (ExpireStore.ListExpireCandidates) is the only call that can
// fail the verb: its error is logged on lg and returned, with no tmux call.
// Expire reads time only through now, never time.Now.
//
// Each selected row is judged in instance-id order, so the per-socket stop
// and the budget's cut-off fall on the same rows on every run (SR-12.2):
//
//  0. its recorded session name (tmux.Unusable): an empty name keeps the row
//     empty_session_name, a control character control_char_session_name,
//     and a character tmux stores differently (`.`, `:` or invalid UTF-8)
//     rewritten_session_name, the first fault in that order, ahead of every
//     other check, with no process check and no tmux call; such a row takes
//     no lookup, stop or budget from its socket or the run (SR-3.2);
//  1. its recorded agent process (tmux.SelectAgentProcess: the SessionStart
//     identity, else the recorded pane's, the pane's when both are recorded
//     and disagree), judged once through pc (tmux.JudgeProcess): alive with
//     its recorded start time keeps the row process_alive, with no tmux
//     call; gone, unreadable or not recorded goes on to the lookup;
//  2. the lookup of its socket (the recorded one, or the caller's for a row
//     that records none, resolved at most once per run without creating
//     anything: sweepSockets.forRow) through the run's one tmux.Sweep, with
//     the row's lookup view (rowLaunch) and its recorded name as the holder
//     name. A Skipped result keeps the row tmux_skipped. Then: Gone deletes
//     it through the conditional delete; Leftover keeps it
//     leftover_running; Ours (however recently the row ended) keeps it ours;
//     Can't tell keeps it tmux_server_changed (a different server),
//     provenance_conflict, cant_tell (unreadable) or tmux_unavailable. After
//     an unreadable or unavailable answer the Sweep makes no more calls on
//     that socket, and once sweepBudget is spent none at all.
//
// The conditional delete (ExpireStore.DeleteFinishedIfSameLife, SR-12.3)
// applies only while the row is still finished with the snapshot expire
// examined: applied puts the row in IDs; changed keeps it
// changed_since_examined; already gone puts it in neither list; a store
// error is logged on lg (instance id and the error) and keeps it
// store_error, and the run goes on and succeeds.
//
// Once a row's outcome is final, a kept row gets one ad.expire.kept record,
// and a row whose lookup reported disagree reasons gets one
// ad.provenance.disagree record per distinct reason (emitRow). Trail writes
// are fail-open.
//
// Expire makes at most one lookup per socket and no other tmux call: it
// lists no panes, writes no adoption, never kills a session, never touches
// transcripts, reads no environment and decodes no structured field
// (SR-12.5, SR-3.15). The budget has no safe minimum: a short one keeps more
// rows tmux_skipped, and a non-positive one makes no tmux call. s, t, pc and
// now must not be nil; a nil lg writes no log line.
func Expire(s ExpireStore, t ExpireTmux, pc ProcChecker, retentionDays int, olderThan *time.Duration, sweepBudget time.Duration, now func() time.Time, lg ExpireLogger) (ExpireResult, error) {
	window := time.Duration(retentionDays) * 24 * time.Hour
	if olderThan != nil {
		window = *olderThan
	}
	cutoff := farFutureCutoff
	if window > 0 {
		cutoff = now().Add(-window)
	}

	candidates, err := s.ListExpireCandidates(cutoff)
	if err != nil {
		if lg != nil {
			lg.Printf("expire: ListExpireCandidates: %v", err)
		}
		return ExpireResult{}, err
	}
	candidates = slices.Clone(candidates)
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].ClaudeInstanceID < candidates[j].ClaudeInstanceID
	})

	r := &expireRun{s: s, pc: pc, sw: tmux.NewSweep(t, pc, now, sweepBudget), lg: lg}
	deleted := make([]string, 0)
	kept := make([]string, 0)
	for _, cand := range candidates {
		row := r.judgeRow(cand)
		switch {
		case row.deleted:
			deleted = append(deleted, cand.ClaudeInstanceID)
		case row.reason != "":
			kept = append(kept, cand.ClaudeInstanceID)
		}
		r.emitRow(cand, row)
	}

	sort.Strings(deleted)
	sort.Strings(kept)
	return ExpireResult{Count: len(deleted), IDs: deleted, Kept: len(kept), KeptIDs: kept}, nil
}

// expireRun is one expire run's state: the store, the start-time reader, the
// run's one tmux.Sweep (lookups, the per-socket stop rule and the budget),
// the socket rule for rows that record none, and the caller identity,
// collected at most once.
type expireRun struct {
	s       ExpireStore
	pc      ProcChecker
	sw      *tmux.Sweep
	lg      ExpireLogger
	sockets sweepSockets
	caller  lazyCaller
}

// judgeRow judges one selected row by SR-12.2's order: its recorded session
// name first (an unusable one keeps the row with unusableKeptReason's reason,
// with no reader call, no socket resolution, no Sweep charge and no
// disagree record), then its agent process, then its socket's lookup
// (lookupRow).
func (r *expireRun) judgeRow(cand ExpireCandidate) expireRow {
	if reason := unusableKeptReason(tmux.Unusable(cand.TmuxSessionName)); reason != "" {
		return expireRow{reason: reason}
	}
	agent := tmux.SelectAgentProcess(
		tmux.ProcIdentity{PID: cand.PID, Starttime: cand.ProcStarttime},
		tmux.ProcIdentity{PID: cand.Identity.PanePID, Starttime: cand.Identity.PaneStarttime},
	)
	if tmux.JudgeProcess(r.pc, agent.Identity) == tmux.ProcAlive {
		return expireRow{reason: keptProcessAlive}
	}
	return r.lookupRow(cand)
}

// lookupRow decides a row whose agent process was not seen running by its
// socket's lookup through the run's Sweep (SR-12.2): a Skipped result keeps
// it tmux_skipped, Gone goes to the conditional delete (deleteRow), and every
// other outcome keeps it with expireKeptReason's reason. A refused resolution
// of the caller's socket makes no call: its first row is kept
// tmux_unavailable and later rows that record none tmux_skipped.
func (r *expireRun) lookupRow(cand ExpireCandidate) expireRow {
	var (
		row expireRow
		ok  bool
	)
	row.socket, row.res, ok = r.sockets.forRow(cand.Identity.Socket)
	if ok {
		launch := rowLaunch(cand.ClaudeInstanceID, cand.Identity, r.s.StoreID(), row.socket)
		row.res = r.sw.Lookup(launch, cand.TmuxSessionName)
	}
	if row.res.Skipped {
		row.reason = keptTmuxSkipped
		return row
	}
	row.disagree = append(row.disagree, row.res.Disagree...)
	if nameChanged(row.res, cand.TmuxSessionName) {
		row.disagree = append(row.disagree, tmux.ReasonNameChanged)
	}
	if row.res.Verdict == tmux.Gone {
		return r.deleteRow(row, cand)
	}
	row.reason = expireKeptReason(row.res)
	return row
}

// expireKeptReason is the one mapping from a lookup Result that is neither
// Skipped nor Gone to its kept reason (SR-12.2). An unknown verdict or Can't
// tell kind keeps the row cant_tell.
func expireKeptReason(res tmux.Result) string {
	switch res.Verdict {
	case tmux.Leftover:
		return keptLeftoverRunning
	case tmux.Ours:
		return keptOurs
	case tmux.CantTell:
		switch res.CantTell {
		case tmux.CantTellDifferentServer:
			return keptTmuxServerChanged
		case tmux.CantTellProvenanceConflict:
			return keptProvenanceConflict
		case tmux.CantTellUnavailable:
			return keptTmuxUnavailable
		}
	}
	return keptCantTell
}

// deleteRow is a Gone row's conditional delete (SR-12.3, SR-5.8), guarded on
// the snapshot the candidate read returned: applied marks the row deleted;
// changed keeps it changed_since_examined; absent leaves it in neither list;
// a store error is logged on lg with the instance id and the error only, and
// keeps it store_error.
func (r *expireRun) deleteRow(row expireRow, cand ExpireCandidate) expireRow {
	res, err := r.s.DeleteFinishedIfSameLife(cand.ClaudeInstanceID, cand.Snapshot)
	switch {
	case err != nil:
		if r.lg != nil {
			r.lg.Printf("expire: DeleteFinishedIfSameLife(%s): %v (continuing)", cand.ClaudeInstanceID, err)
		}
		row.reason = keptStoreError
	case res == CondApplied:
		row.deleted = true
	case res == CondAbsent:
	default: // CondChanged
		row.reason = keptChangedSinceExamined
	}
	return row
}

// Expire removes finished rows (ended or missing) whose ended_at is older than
// the retention window, and keeps any whose agent, own session or leftover
// may still run, or for which it cannot tell. When olderThan is nil the window comes from defaults.expire_retention_days in
// config.toml; a non-nil value overrides it, and a zero or negative duration
// selects every finished row. Live rows and rows with a NULL ended_at are
// never selected.
//
// A selected row whose recorded tmux session name cannot be used (it is
// empty, contains a control character, or contains a character tmux stores
// differently) is kept on every run, before any other check and with no
// process check or tmux call, with reason empty_session_name,
// control_char_session_name or rewritten_session_name (the first fault in
// that order); removing it is a human's decision (see "Operator actions" in
// the agent-director README).
//
// For every other row, expire reads tmux to decide. A selected row whose
// recorded agent process still runs is kept with no tmux call; otherwise one
// lookup of its socket (its recorded one, else the caller's), shared by every
// row of that socket, decides it. The row is deleted only when tmux shows no
// session of the agent and the row is unchanged since expire examined it. A
// row whose agent still has a session (its own, or one left from an earlier
// launch), whose tmux could not be read, or which changed or failed to delete
// is kept and listed in KeptIDs, with its reason in the trail. After an
// unreadable or unavailable answer on a socket no more tmux calls are made on
// that socket, and the run's tmux time (sweep_budget_seconds from the loaded
// configuration) caps all its calls; rows not reached are kept. expire never
// kills a session, never touches JSONL transcripts, and tmux problems never
// fail the run.
//
// Finished rows are removed by an operator-scheduled expire at the default
// retention, run as the same user and in the same tmux environment as the
// agents; a row recorded without a socket is judged in the caller's own tmux
// environment. A run as another user, as root or against another tmux server
// can wrongly delete rows whose agent still runs. Agents never run expire,
// least of all with a zero window.
//
// A finished row is not proof that its agent is dead: missing is the sweep's
// judgement on the evidence available to it, not proof that the agent has
// exited, and neither ended nor missing means that the agent is dead or that
// its row is safe to delete.
//
// CLI: agent-director expire
//
// Errors: none.
//
// Nondeterminism: none.
func (c *Client) Expire(olderThan *time.Duration) (ExpireResult, error) {
	if err := c.checkClosed(); err != nil {
		return ExpireResult{}, err
	}
	return Expire(c.st, c.tmuxClient, c.procChecker, c.cfg.Defaults.ExpireRetentionDays, olderThan, c.cfg.Tmux.EffectiveSweepBudget(), c.now, c.logger)
}
