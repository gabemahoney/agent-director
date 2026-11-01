package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/inputdelivery"
	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// activeCheckOutcome records what actually happened, including work that could
// not run. It is internal evidence for later confirmation/completeness work;
// checking a row is never, by itself, a durable confirmation.
type activeCheckOutcome struct {
	Adapter  string
	Result   string
	Evidence string
	Err      error
	Sample   activeCheckSample
}

// Keep the examined identity even when a later stored projection changes.
// This is invocation evidence, not a persisted confirmation record.
type activeCheckSample struct {
	Snapshot           RowSnapshot
	Identity           LaunchIdentity
	LifeNumber         int64
	ObservationVersion int64
	PermissionEpoch    int64
	JSONLPath          string
}

func (checked *activeCheckedRow) record(outcome activeCheckOutcome) {
	row := checked.row
	outcome.Sample = activeCheckSample{row.Snapshot, row.Identity, row.LifeNumber, row.ObservationVersion, row.PermissionEpoch, row.JSONLPath}
	checked.outcomes = append(checked.outcomes, outcome)
}

type activeCheckedRow struct {
	row     Spawn
	display store.DisplayDetail
	// state and process are this invocation's unsuccessful or uncommitted
	// attempts for those main fields (nil when none): response-only, never a
	// durable record. A successful committed attempt is the stored record the
	// final projection reads instead.
	state, process *fieldAttempt
	judged         *judgedRequestSet
	permissionErr  error
	projectionErr  error
	cursorRejected bool
	outcomes       []activeCheckOutcome
	proofRow       *Spawn
	proof          *InputProofResult
	history        *activeSessionHistory
}

// Get captures validated history after recovery refreshes and before display
// can lease the store connection. Reuse it only with the same row revision,
// launch and life; a refreshed row must validate its own history instead.
type activeSessionHistory struct {
	snapshot RowSnapshot
	identity LaunchIdentity
	life     int64
	rows     []SessionHistoryEntry
	err      error
}

// An ordinary history reader cannot establish that its entries belong to an
// earlier selected row. Only this explicit capability permits prefetch reuse.
type sessionHistorySnapshotReader interface {
	ListSessionHistorySnapshot(Spawn, time.Duration) ([]SessionHistoryEntry, error)
}

// activeCheckProviders is invocation-owned. Narrow exported helpers have no
// tmux provider and do not manufacture one from the host environment.
type activeCheckProviders struct {
	tmux         FindMissingTmux
	pendingGrace time.Duration
	budget       time.Duration
	logger       FindMissingLogger
	deferred     func(string, error)
}

// activeReadStore holds the fixed selection and its checked projections.
// It deliberately exposes no delivery, restart, signal or enqueue operation.
type activeReadStore struct {
	source        any
	rows          []*activeCheckedRow
	byID          map[string]*activeCheckedRow
	view          RelayView
	settle        store.SettleRead
	refresh       func([]string, []store.CursorMissingProof) ([]Spawn, error)
	cursor        []store.CursorMissingProof
	missing       FindMissingResult
	projectionErr error
	ctx           context.Context
	emit          trailEmit
	fallbackBusy  bool
	fallbackTried map[int64]bool
	withHistory   bool
}

// fieldAttempt is one main-field attempt that did not durably confirm its
// field: when it ran, why not (an SR-4.5 reason) and what it examined, so it
// describes only that value and owner (withAttempt): the life and launch;
// for a state attempt, the raw state and raw-state interval
// (permission_epoch) it was about; for a process attempt, the process record
// it found and the agent process identity it judged (pid 0 when it judged
// none).
type fieldAttempt struct {
	atMillis int64
	reason   string
	life     int64
	token    string
	process  bool
	state    string
	epoch    int64
	record   string
	pid      int
	start    string
}

// describes reports whether a, an attempt for the field, was made for the
// value and owner row returns: the same life and launch and, for a state
// attempt, the same raw state in the same interval; for a process attempt,
// the same stored process record and a judged identity row still records. A
// concurrent commit that changed the value or its record has its own
// evidence, which an older attempt must not overlay.
func (a *fieldAttempt) describes(row Spawn) bool {
	if a.life != row.LifeNumber || a.token != row.Identity.Token {
		return false
	}
	if !a.process {
		return a.state == row.State && a.epoch == row.PermissionEpoch
	}
	if a.record != row.ProcessEvidence {
		return false
	}
	return a.pid == 0 || (a.pid == row.PID && a.start == row.ProcStarttime) ||
		(a.pid == row.Identity.PanePID && a.start == row.Identity.PaneStarttime)
}

// outcomeReason is the SR-4.5 reason of an unsuccessful outcome result, ""
// for any other result.
func outcomeReason(result string) string {
	switch result {
	case "stale":
		return store.ReasonStaleObservation
	case "unconfirmed":
		return store.ReasonCommitUnconfirmed
	case "cancelled":
		return store.ReasonDeadline
	case "busy":
		return store.ReasonBusy
	case "absent":
		return store.ReasonNoEvidence
	case "unreadable":
		return store.ReasonUnreadable
	}
	return ""
}

// attempt notes that this invocation's attempt for checked's lifecycle state,
// as checked's current row holds it, ended with reason; an empty reason notes
// nothing. A later attempt for the same field replaces it.
func (r *activeReadStore) attempt(checked *activeCheckedRow, reason string) {
	r.stateAttempt(checked, checked.row, checked.row.State, reason)
}

// stateAttempt is attempt for value state on examined, the row the attempt
// examined: an attempt for a projected value (a Cursor row's projected
// missing) names the value it would establish, and a deferred write names the
// stored row it examined.
func (r *activeReadStore) stateAttempt(checked *activeCheckedRow, examined Spawn, state, reason string) {
	if reason == "" {
		return
	}
	checked.state = &fieldAttempt{atMillis: newRelayJudge(r.view).now().UnixMilli(), reason: reason,
		life: examined.LifeNumber, token: examined.Identity.Token, state: state, epoch: examined.PermissionEpoch}
}

// processAttempt notes that this invocation's process_liveness attempt for
// checked's current row, judging probe's identity (none when probe ran no
// judgement), ended with reason; an empty reason notes nothing.
func (r *activeReadStore) processAttempt(checked *activeCheckedRow, probe processProbe, reason string) {
	r.processAttemptOn(checked, checked.row, probe, reason)
}

// processAttemptOn is processAttempt for examined, the row the attempt
// examined when a later read replaced checked's row: the attempt describes
// examined's process record, never one a concurrent writer committed since.
func (r *activeReadStore) processAttemptOn(checked *activeCheckedRow, examined Spawn, probe processProbe, reason string) {
	if reason == "" {
		return
	}
	a := &fieldAttempt{atMillis: r.probeMillis(probe), reason: reason,
		life: examined.LifeNumber, token: examined.Identity.Token, process: true, record: examined.ProcessEvidence}
	if probe.judged && probe.identity.PID > 0 {
		a.pid, a.start = probe.identity.PID, probe.identity.Starttime
	}
	checked.process = a
}

// probeMillis is the time of probe's evidence: its judgement's instant, or
// the relay clock's now for an attempt that judged nothing, or for a
// judgement the clock gave no time (a run with no clock, which only tests use).
func (r *activeReadStore) probeMillis(probe processProbe) int64 {
	if probe.judged && !probe.at.IsZero() {
		return probe.at.UnixMilli()
	}
	return newRelayJudge(r.view).now().UnixMilli()
}

// recordProcess commits this invocation's process_liveness attempt for the
// recorded agent process of checked's current row, under the sampled
// snapshot, life, launch, interval, observations and earlier record
// (store.RecordProcessEvidence). A committed record, successful or not, is
// what the final projection reads. An attempt the store cannot hold (a narrow
// store, a process the row no longer records), a stale sample, a busy store,
// a deadline or an unconfirmed COMMIT stays response-only and never reads as
// a new confirmation. reason, when set, is why the attempt could not run to a
// durable write at all (the liveness write before it failed). A Cursor row
// records nothing: no check observes its runtime process, so its
// process_liveness is always unknown with reason unsupported (mainFields).
func (r *activeReadStore) recordProcess(checked *activeCheckedRow, probe processProbe, reason string) {
	if !probe.judged || checked.row.Agent == store.AgentCursor {
		return
	}
	attempt := probe.attempt()
	if reason != "" {
		r.processAttempt(checked, probe, reason)
		return
	}
	row := checked.row
	// The record is stamped with the judgement's time, the time of its
	// evidence (SR-4.5), not the time it is built.
	record, ok := row.ProcessEvidenceAfter(attempt, r.probeMillis(probe))
	s, writes := r.source.(interface {
		RecordProcessEvidence(Spawn, string) (CondResult, error)
	})
	switch {
	case !ok && attempt.Reason != "":
		r.processAttempt(checked, probe, attempt.Reason)
		return
	case !ok:
		// The judged identity is not a process the current row records, an
		// alive sample meets a finished row, or the clock gave no time (a
		// zero clock, which only tests use).
		r.processAttempt(checked, probe, store.ReasonStaleObservation)
		return
	case !writes:
		r.processAttempt(checked, probe, store.ReasonUnsupported)
		return
	}
	res, err := s.RecordProcessEvidence(row, record)
	outcome := activeOutcome("process_evidence", res == CondApplied, err)
	if err == nil && res == CondChanged {
		outcome.Result = "stale"
	}
	checked.record(outcome)
	if err == nil && res == CondApplied {
		checked.row.ProcessEvidence = record
		return
	}
	r.processAttempt(checked, probe, outcomeReason(outcome.Result))
}

// staleProcess notes, response-only, that probe's process_liveness attempt
// for examined, the row it examined, became a stale sample: the row moved on
// before its record could be committed. Like recordProcess, it notes nothing
// without a judgement or for a Cursor row.
func (r *activeReadStore) staleProcess(checked *activeCheckedRow, examined Spawn, probe processProbe) {
	if !probe.judged || examined.Agent == store.AgentCursor {
		return
	}
	r.processAttemptOn(checked, examined, probe, store.ReasonStaleObservation)
}

// probeStillApplies reports whether current, the row read after this
// invocation's liveness write, is still the row the probe examined as that
// write left it: after this invocation's own mark, which stored no process
// record (ProcessRecorded false), a row with none; after any other write (a
// note, a clear, which never touch process_evidence) or none, a live row with
// examined's process record. Anything else is a concurrent writer's commit,
// a terminal move with its own record or a SessionEnd among them, which a
// sample taken before it must never overwrite (SR-4.7).
func probeStillApplies(examined, current Spawn, result findMissingRow) bool {
	if result.marked && result.write.Wrote && result.write.Err == nil && result.write.Res == CondApplied {
		return current.ProcessEvidence == ""
	}
	return store.IsLiveState(current.State) && current.ProcessEvidence == examined.ProcessEvidence
}

func activeOutcome(adapter string, applied bool, err error) activeCheckOutcome {
	result := "unchanged"
	switch {
	case store.RecoverySampleChanged(err), errors.Is(err, inputdelivery.ErrStale):
		result = "stale"
	case store.CommitUnconfirmed(err), errors.Is(err, inputdelivery.ErrUnconfirmed):
		result = "unconfirmed"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		result = "cancelled"
	case errors.Is(err, store.ErrStoreBusy):
		result = "busy"
	case errors.Is(err, store.ErrSpawnNotFound), errors.Is(err, sql.ErrNoRows):
		result = "absent"
	case err != nil:
		result = "unreadable"
	case applied:
		result = "committed"
	}
	return activeCheckOutcome{Adapter: adapter, Result: result, Err: err}
}

func liveIdentity(row Spawn) LiveSpawnIdentity {
	return LiveSpawnIdentity{InstanceID: row.InstanceID, State: row.State, PID: row.PID,
		ProcStarttime: row.ProcStarttime, TmuxSessionName: row.TmuxSessionName,
		LaunchStartedAtMillis: row.LaunchStartedAtMillis, LivenessNote: row.LivenessNote,
		Snapshot: row.Snapshot, Identity: row.Identity, LaunchOwner: row.LaunchOwner,
		Agent: row.Agent, EndReportedAtMillis: row.EndReportedAtMillis}
}

// checkActiveRows runs each adapter once for the selected IDs. Reads select
// first, and never reapply their filters or fill a vacated limit slot. The
// refresh callback is a stored projection only, never another check pass.
func checkActiveRows(ctx context.Context, source any, view RelayView, settle store.SettleRead, rows []Spawn,
	refresh func([]string, []store.CursorMissingProof) ([]Spawn, error), includeHistory bool) *activeReadStore {
	r := &activeReadStore{source: source, view: view, settle: settle, refresh: refresh, ctx: ctx, emit: recoveryTrail(ctx), withHistory: includeHistory,
		byID: map[string]*activeCheckedRow{}, fallbackTried: map[int64]bool{},
		missing: FindMissingResult{IDs: []string{}, UnverifiedIDs: []string{}}}
	for _, row := range rows {
		checked := &activeCheckedRow{row: row, display: row.CommittedDisplay()}
		for _, adapter := range []string{"liveness", "cursor_gateway", "codex_turn", "transcript", "permission_read", "permission_repair", "display", "permission_fallback", "native_proof"} {
			checked.record(activeCheckOutcome{Adapter: adapter, Result: "unattempted"})
		}
		r.rows = append(r.rows, checked)
		r.byID[row.InstanceID] = checked
	}
	var live *findMissingRun
	if r.view.active == nil {
		r.view.active = &activeCheckProviders{pendingGrace: (config.Tmux{}).EffectivePendingGrace(), budget: (config.Tmux{}).EffectiveSweepBudget()}
	}
	if p := r.view.active; view.Procs != nil {
		if s, ok := source.(FindMissingStore); ok {
			// Reuse production SQL with nonwaiting admission; wrappers that
			// intentionally supply only the legacy interface keep their seam.
			if bounded, ok := source.(*store.Store); ok {
				s = bounded.RecoveryWrites(rows...)
			}
			now := newRelayJudge(view).now
			live = &findMissingRun{s: s, t: p.tmux, pc: view.Procs, sw: tmux.NewSweep(p.tmux, view.Procs, now, p.budget), lg: p.logger, emit: r.emit, now: now}
			if capture, ok := p.tmux.(interface {
				CapturePaneID(string, string, int, bool) (string, error)
			}); ok {
				r.view.samplePane = func(row Spawn) store.PaneSample { return sampleActivePane(live, capture, row) }
			}
		}
	}
	for _, checked := range r.rows {
		if err := ctx.Err(); err != nil {
			checked.record(activeOutcome("remaining", false, err))
			continue
		}
		r.checkRow(checked, live)
		// Finished rows and adapters that stopped before display still need a
		// validated pair before the final projection can become unavailable.
		r.captureSessionHistory(checked)
	}
	// One final stored read gives every selected family the same view of all
	// committed member changes. It cannot add an unselected descendant.
	if refresh != nil {
		ids := make([]string, 0, len(r.rows))
		for _, checked := range r.rows {
			ids = append(ids, checked.row.InstanceID)
		}
		current, err := refresh(ids, r.cursor)
		var stale *store.CursorMissingReadError
		rowFailures := errors.As(err, &stale)
		if rowFailures {
			// Keep the failed row's earlier stored projection and its rejected
			// outcome. Neither is new liveness proof, and neither invalidates
			// the other selected rows returned by this same stored read.
			for _, id := range stale.InstanceIDs {
				if checked := r.byID[id]; checked != nil {
					checked.projectionErr = err
					checked.record(activeOutcome("projection", false, err))
					// The retained older facts carry no fresh lifecycle proof.
					r.attempt(checked, store.ReasonStaleObservation)
				}
			}
		}
		if err == nil || rowFailures {
			for _, row := range current {
				if checked := r.byID[row.InstanceID]; checked != nil {
					if checked.row.Snapshot != row.Snapshot || checked.row.PermissionEpoch != row.PermissionEpoch {
						checked.judged = nil
					}
					checked.row = row
					checked.display = row.CommittedDisplay()
				}
			}
		} else {
			if len(r.cursor) > 0 {
				r.projectionErr = err
			}
			for _, checked := range r.rows {
				checked.projectionErr = err
				checked.record(activeOutcome("projection", false, err))
			}
		}
	}
	r.view.checked = r.byID
	return r
}

func (r *activeReadStore) refreshRow(checked *activeCheckedRow) bool {
	if r.refresh == nil {
		return false
	}
	rows, err := r.refresh([]string{checked.row.InstanceID}, nil)
	checked.record(activeOutcome("refresh", false, err))
	if err != nil || len(rows) != 1 {
		return false
	}
	old := checked.row
	checked.row = rows[0]
	checked.display = rows[0].CommittedDisplay()
	// A changed launch ends this row's pass. A later call can check it.
	return old.Identity.Token == rows[0].Identity.Token && old.LifeNumber == rows[0].LifeNumber
}

func (r *activeReadStore) checkRow(checked *activeCheckedRow, live *findMissingRun) {
	row := checked.row
	if r.view.provisionalOnly[row.InstanceID] {
		r.checkTranscript(checked)
		return
	}
	if !store.IsLiveState(row.State) {
		// Each adapter a finished row admits none of is recorded on its own.
		for _, adapter := range []string{"liveness", "cursor_gateway", "codex_turn", "transcript", "permission_read", "permission_repair", "display", "permission_fallback"} {
			checked.record(activeCheckOutcome{Adapter: adapter, Result: "inapplicable"})
		}
		checked.proofRow = &row
		return
	}
	if p := r.view.active; p != nil {
		now := newRelayJudge(r.view).now()
		inside := store.InsidePendingGrace(row.State, row.LaunchStartedAtMillis, p.pendingGrace, now)
		if inside || (row.State == store.StatePending && r.view.Procs != nil && spawn.LaunchOwnerAlive(r.view.Procs, row.LaunchOwner)) {
			checked.record(activeCheckOutcome{Adapter: "pending", Result: "inapplicable"})
			// The pending-launch protection, not missing evidence, keeps the
			// process unchecked; its lifecycle admission keeps its own record.
			if r.view.Procs != nil && row.Agent != store.AgentCursor {
				reason := store.ReasonOperationPending
				if inside {
					reason = store.ReasonPendingGrace
				}
				r.processAttempt(checked, processProbe{}, reason)
			}
			r.checkTranscript(checked)
			return
		}
	}
	var cursorProcess string
	if row.Agent == store.AgentCursor && r.view.Procs != nil {
		proof, outcome := cursorGatewayProof(r.source, r.view.Procs, row)
		checked.record(outcome)
		err := outcome.Err
		if err != nil {
			r.attempt(checked, outcomeReason(outcome.Result))
			return
		}
		if proof != nil {
			r.cursor = append(r.cursor, *proof)
			checked.outcomes[len(checked.outcomes)-1].Result = "observed"
			// The projected missing has no durable record until the deferred
			// terminal write, which this invocation still owns, commits. The
			// attempt is for that projected value in the examined interval.
			r.stateAttempt(checked, row, store.StateMissing, store.ReasonOperationPending)
			return // project the exact loss before the deferred terminal write
		}
		cursorProcess = outcome.Evidence
	}
	if live != nil {
		// The probe examines this row; a refresh after the liveness write may
		// read a later one.
		examined := checked.row
		var result findMissingRow
		switch cursorProcess {
		case "alive":
			// Reuse the sampled gateway verdict for ordinary note handling.
			result = live.aliveRow(findMissingRow{}, liveIdentity(row), row.Snapshot, row.Identity)
		case "unknown":
			// An uncertain exact gateway is not terminal authority. Keep its
			// nonterminal bookkeeping without falling through to a tmux mark.
			result = live.noteRow(findMissingRow{}, liveIdentity(row), row.Snapshot, noteProbeEACCES)
		default:
			// Narrow providers without a gateway adapter keep the original
			// process/tmux seam, including its same-life write guards.
			result = live.judgeLiveRow(liveIdentity(row))
		}
		live.emitDisagree(liveIdentity(row), result)
		outcome := activeOutcome("liveness", result.write.Wrote && result.write.Res == CondApplied, result.write.Err)
		outcome.Evidence = result.action
		if result.write.Wrote && result.write.Res != CondApplied && result.write.Err == nil {
			outcome.Result = "stale"
		}
		checked.record(outcome)
		if result.write.Err != nil || (result.write.Wrote && result.write.Res != CondApplied) {
			if result.marked {
				r.attempt(checked, outcomeReason(outcome.Result))
			}
			r.recordProcess(checked, result.probe, outcomeReason(outcome.Result))
			return
		}
		// A no-write note still needs current identity before entering counters.
		if !result.write.Wrote {
			if reader, ok := r.source.(interface {
				RecoveryRows([]string, store.SettleRead) ([]Spawn, error)
			}); ok {
				current, err := reader.RecoveryRows([]string{row.InstanceID}, r.settle)
				if err != nil || len(current) != 1 || current[0].Snapshot != result.guard || current[0].ObservationVersion != row.ObservationVersion {
					outcome := activeOutcome("liveness_revalidation", false, err)
					if err == nil {
						outcome.Result = "stale"
					}
					checked.record(outcome)
					r.recordProcess(checked, result.probe, outcomeReason(outcome.Result))
					return
				}
				// This projection validates evidence only. Keep the complete
				// response snapshot if the final full refresh is unavailable.
			}
		}
		if result.write.Listed {
			if result.marked {
				r.missing.IDs = append(r.missing.IDs, row.InstanceID)
			} else {
				r.missing.UnverifiedIDs = append(r.missing.UnverifiedIDs, row.InstanceID)
			}
		}
		if result.write.Wrote && !r.refreshRow(checked) {
			r.staleProcess(checked, examined, result.probe)
			return
		}
		// The mark, when one applied, already committed its state record, and
		// a From-form mark on a proven exit its process record too
		// (ProcessRecorded). A store without MarkMissingIfSameLifeFrom or
		// MarkEndedIfSameLifeFrom makes the plain mark, which records no
		// process record, so the switch below follows it up. Any other
		// follow-up record is bound to the row the probe examined: the
		// refreshed row must still be what this invocation's own write left
		// of it.
		switch {
		case result.write.ProcessRecorded:
		case probeStillApplies(examined, checked.row, result):
			r.recordProcess(checked, result.probe, "")
		default:
			r.staleProcess(checked, examined, result.probe)
		}
		row = checked.row
		if !store.IsLiveState(row.State) {
			checked.proofRow = &row
			return
		}
	} else {
		checked.record(activeCheckOutcome{Adapter: "liveness", Result: "unavailable"})
	}
	if !r.checkTurn(checked) || !r.checkTranscript(checked) {
		return
	}
	r.checkPermissionDisplay(checked)
	proofRow := checked.row
	checked.proofRow = &proofRow
}

func (r *activeReadStore) checkTurn(checked *activeCheckedRow) bool {
	row := checked.row
	s, ok := r.source.(interface {
		CodexTurnSamples(string) ([]store.CodexTurnSample, error)
		EndCodexTurnSample(store.CodexTurnSample, time.Duration) (bool, error)
	})
	if !ok || row.Agent != store.AgentCodex || row.State != store.StateWorking {
		return true
	}
	samples, err := s.CodexTurnSamples(row.InstanceID)
	if err != nil {
		outcome := activeOutcome("codex_turn", false, err)
		checked.record(outcome)
		r.attempt(checked, outcomeReason(outcome.Result))
		return false
	}
	for _, sample := range samples {
		if sample.Snapshot != row.Snapshot || sample.ObservationVersion != row.ObservationVersion {
			checked.record(activeCheckOutcome{Adapter: "codex_turn", Result: "stale"})
			r.attempt(checked, store.ReasonStaleObservation)
			return false
		}
		if !codexRolloutEndsTurn(sample.SessionID, sample.JSONLPath, sample.Turn) {
			checked.record(activeCheckOutcome{Adapter: "codex_turn", Result: "unconfirmed"})
			return true
		}
		applied, err := s.EndCodexTurnSample(sample, readerLockWait)
		outcome := activeOutcome("codex_turn", applied, err)
		outcome.Evidence = sample.Turn
		if !applied && err == nil {
			outcome.Result = "stale"
		}
		checked.record(outcome)
		if err != nil || !applied {
			// The rollout established the end, but no durable record did.
			r.attempt(checked, outcomeReason(outcome.Result))
			return false
		}
		return r.refreshRow(checked)
	}
	return true
}

func (r *activeReadStore) checkTranscript(checked *activeCheckedRow) bool {
	row := checked.row
	s, ok := r.source.(interface {
		ProvisionalTranscriptsFor(string) ([]store.ProvisionalTranscript, error)
		HealProvisionalTranscript(store.ProvisionalTranscript, string) (bool, error)
	})
	if !ok {
		if pt, selected := r.view.provisional[row.InstanceID]; selected {
			path, err := provisionalTranscriptPath(pt)
			if err == nil {
				_, err = os.Stat(path)
			}
			if err != nil {
				return true
			}
			applied, err := r.source.(FindMissingStore).HealJsonlPath(pt.InstanceID, pt.SessionID, path)
			checked.record(activeOutcome("transcript", applied, err))
			return err == nil
		}
		return true
	}
	if (row.Agent != "" && row.Agent != store.AgentClaude) || row.JSONLPath != "" || row.SessionID == "" {
		return true
	}
	samples, err := s.ProvisionalTranscriptsFor(row.InstanceID)
	if err != nil {
		checked.record(activeOutcome("transcript", false, err))
		return false
	}
	for _, sample := range samples {
		if sample.Snapshot != row.Snapshot || sample.ObservationVersion != row.ObservationVersion {
			checked.record(activeCheckOutcome{Adapter: "transcript", Result: "stale"})
			return false
		}
		path, err := provisionalTranscriptPath(sample)
		if err == nil {
			_, err = os.Stat(path)
		}
		if err != nil {
			checked.record(activeOutcome("transcript", false, err))
			return true
		}
		applied, err := s.HealProvisionalTranscript(sample, path)
		checked.record(activeOutcome("transcript", applied, err))
		if err != nil || !applied {
			return false
		}
		return r.refreshRow(checked)
	}
	return true
}

func provisionalTranscriptPath(pt ProvisionalTranscript) (string, error) {
	if spawn.ConfigDirUsable(pt.ConfigDir) {
		return spawn.JsonlPathIn(pt.ConfigDir, pt.CWD, pt.SessionID)
	}
	return spawn.JsonlPath(pt.CWD, pt.SessionID)
}

func (r *activeReadStore) checkPermissionDisplay(checked *activeCheckedRow) {
	row := checked.row
	j := newRelayJudge(r.view)
	if reader := activePermissionReader(r.source); reader != nil {
		checked.judged, checked.permissionErr = readJudgedRequests(reader, j, row.InstanceID)
		checked.record(activeOutcome("permission_read", false, checked.permissionErr))
	}
	if checked.permissionErr != nil {
		return
	}
	if row.State == store.StateCheckPermission && checked.judged != nil {
		_, result, err := repairCheckPermissionRow(r.emit, r.source, row, checked.judged, j)
		repair := activeOutcome("permission_repair", result == CondApplied, err)
		checked.record(repair)
		if err != nil {
			r.attempt(checked, outcomeReason(repair.Result))
			r.fallbackBusy = r.fallbackBusy || errors.Is(err, store.ErrStoreBusy)
			return
		}
		if result == CondApplied {
			if !r.refreshRow(checked) {
				return
			}
			row = checked.row
		}
	}
	r.captureSessionHistory(checked)
	var displayOutcome activeCheckOutcome
	display, families, judged, busy := observeDisplay(r.source, r.view, r.settle, row, permissionRead{requests: checked.judged, outcome: &displayOutcome})
	checked.display = display
	if judged != nil {
		checked.judged = judged
	}
	// The observation returns the families from its own commit. Retain those
	// values for every selected ancestor even if the final read is busy. This
	// uses no extra read and grants no checks of unselected family members.
	for id, family := range families {
		if member := r.byID[id]; member != nil {
			member.row.FamilyState = family
		}
	}
	checked.record(displayOutcome)
	r.fallbackBusy = r.fallbackBusy || busy
	if !busy && checked.judged != nil {
		open, unproven := judgedSpawnInfos(checked.judged, j, row)
		slots := goneSlotsOf(nil, open, row.Agent, row.RelayMode, j.now())
		attempted, _, err := r.recordGone(j.now(), readerLockWait, goneSlotsOf(slots, unproven, row.Agent, row.RelayMode, j.now()))
		if attempted && err == nil {
			gone := map[int64]*time.Time{}
			for _, info := range open {
				gone[info.RequestID] = info.HookGoneAt
			}
			for _, info := range unproven {
				gone[info.RequestID] = info.HookGoneAt
			}
			for i, pr := range checked.judged.rows {
				if at := gone[pr.RequestID]; at != nil {
					checked.judged.rows[i].HookGoneAt = *at
				}
			}
		}
	}
}

// recordGone retains the original provider's optional recorder, including for
// requests first judged after the final refresh. Each request gets at most one
// eligible attempt in this invocation; a busy observation or fallback write
// suppresses subsequent fallback writes. This never repairs or answers a row.
func (r *activeReadStore) recordGone(at time.Time, maxWait time.Duration, slots []goneSlot) (bool, bool, error) {
	if r.fallbackBusy {
		return false, false, nil
	}
	eligible := make([]goneSlot, 0, len(slots))
	rows := map[string]*activeCheckedRow{}
	deliveries := map[int64]*RequestDelivery{}
	for _, slot := range slots {
		id := slot.d.fallbackRequest.InstanceID
		if checked := r.byID[id]; checked != nil && !r.fallbackTried[slot.id] && deliveries[slot.id] == nil {
			eligible = append(eligible, slot)
			rows[id] = checked
			deliveries[slot.id] = slot.d
		}
	}
	attempted, committed, err := recordGoneOutcome(r.source, at, maxWait, eligible)
	if attempted {
		for _, slot := range eligible {
			r.fallbackTried[slot.id] = true
		}
		for _, checked := range rows {
			outcome := activeOutcome("permission_fallback", committed, err)
			if committed && len(rows) > 1 {
				// The legacy recorder reports a batch result. It does not prove
				// that every selected row's observation was persisted.
				outcome.Result = "observed"
			}
			checked.record(outcome)
		}
		// Get can include one request in both open and unproven lists. Write
		// it once, but carry the recorded timestamp into both returned copies.
		for _, slot := range slots {
			if delivery := deliveries[slot.id]; delivery != nil {
				slot.d.HookGoneAt = delivery.HookGoneAt
			}
		}
	}
	r.fallbackBusy = errors.Is(err, store.ErrStoreBusy)
	return attempted, committed, err
}

// finish writes proof/terminal evidence only after all response reads. No read
// then queues behind an abandoned native COMMIT. Each row retains its outcome;
// one failed row never suppresses another selected row's persistence attempt.
func (r *activeReadStore) finish() error {
	first := r.projectionErr
	var staleCursorIDs []string
	for _, proof := range r.cursor {
		checked := r.byID[proof.Row.InstanceID]
		if checked.projectionErr != nil {
			continue
		}
		s, ok := r.source.(interface {
			MarkCursorMissingIfSameLaunch(Spawn, store.CursorRuntime) (string, CondResult, error)
			CanDeferCursorMissingWrite(error) bool
		})
		if !ok {
			continue
		}
		_, result, err := s.MarkCursorMissingIfSameLaunch(proof.Row, proof.Binding)
		outcome := activeOutcome("cursor_commit", result == CondApplied, err)
		if err == nil && result != CondApplied {
			checked.cursorRejected = true
			outcome.Result = "stale"
			if result == CondAbsent {
				outcome.Result = "absent"
			}
		}
		checked.record(outcome)
		checked.outcomes[len(checked.outcomes)-1].Sample = activeCheckSample{proof.Row.Snapshot, proof.Row.Identity, proof.Row.LifeNumber, proof.Row.ObservationVersion, proof.Row.PermissionEpoch, proof.Row.JSONLPath}
		// A rejected terminal write is the latest attempt for the stored row
		// list reports in place of the projected missing (cursorListFallbacks);
		// an applied one committed its own record. A failed write notes
		// nothing: status and get already projected, and list falls back only
		// after a rejection.
		if err == nil && result != CondApplied {
			r.stateAttempt(checked, proof.Row, proof.Row.State, outcomeReason(outcome.Result))
		}
		if err != nil {
			if s.CanDeferCursorMissingWrite(err) {
				if p := r.view.active; p != nil && p.deferred != nil {
					p.deferred(proof.Row.InstanceID, err)
				}
			} else if first == nil {
				first = err
			}
		} else if result == CondApplied {
			r.missing.IDs = append(r.missing.IDs, proof.Row.InstanceID)
			emitMarkTick(r.emit, proof.Row.InstanceID, proof.Row.State, store.StateMissing, reasonProcAbsent, nil)
		} else if result == CondChanged {
			if reader, ok := r.source.(interface {
				RecoveryRows([]string, store.SettleRead) ([]Spawn, error)
			}); ok {
				rows, readErr := reader.RecoveryRows([]string{proof.Row.InstanceID}, r.settle)
				if readErr != nil && first == nil {
					first = readErr
				}
				if len(rows) == 1 && store.IsLiveState(rows[0].State) && rows[0].Identity.Token == proof.Row.Identity.Token && rows[0].LifeNumber == proof.Row.LifeNumber {
					staleCursorIDs = append(staleCursorIDs, proof.Row.InstanceID)
				}
			}
		}
	}
	if s, ok := r.source.(interface{ CursorMissingReadContextErr() error }); ok && first == nil {
		first = s.CursorMissingReadContextErr()
	}
	if s, ok := r.source.(InputProofStore); ok {
		for _, checked := range r.rows {
			if checked.proofRow == nil {
				continue
			}
			row := *checked.proofRow
			if row.Agent == store.AgentCursor || row.Identity.Token == "" {
				continue
			}
			proof, committed, err := reconcileInputProof(r.ctx, s, row)
			checked.proof = &proof
			checked.record(activeOutcome("native_proof", committed, err))
		}
	}
	sort.Strings(r.missing.IDs)
	sort.Strings(r.missing.UnverifiedIDs)
	r.missing.Count, r.missing.Unverified = len(r.missing.IDs), len(r.missing.UnverifiedIDs)
	if first == nil && len(staleCursorIDs) > 0 {
		// Preserve single-row conflict errors without turning a rejected
		// proof into a selection-wide failure. A real read/write or caller
		// context error above still takes precedence over these local races.
		return fmt.Errorf("Cursor lost gateway changed during reconciliation: %w", &store.CursorMissingReadError{InstanceIDs: staleCursorIDs})
	}
	return first
}

func (r *activeReadStore) GetSpawn(id string) (Spawn, error) {
	if r.projectionErr != nil {
		return Spawn{}, r.projectionErr
	}
	if row := r.byID[id]; row != nil {
		// A single-row read still reports its own rejected Cursor projection.
		// List assembles the fixed selection directly, retaining older facts
		// for this row without suppressing independent selected results.
		var stale *store.CursorMissingReadError
		if errors.As(row.projectionErr, &stale) {
			return Spawn{}, row.projectionErr
		}
		return row.row, nil
	}
	return Spawn{}, fmt.Errorf("%w: %s", store.ErrSpawnNotFound, id)
}
func (r *activeReadStore) GetSpawnState(id string) (string, error) {
	row, err := r.GetSpawn(id)
	return row.State, err
}
func (r *activeReadStore) GetSpawnSettled(id string, _ store.SettleRead) (Spawn, error) {
	return r.GetSpawn(id)
}
func (r *activeReadStore) SpawnStatusRow(id string, _ store.SettleRead) (Spawn, error) {
	return r.GetSpawn(id)
}
func (r *activeReadStore) ListSpawns(_ ListFilters) ([]Spawn, error) {
	if r.projectionErr != nil {
		return nil, r.projectionErr
	}
	out := make([]Spawn, 0, len(r.rows))
	for _, row := range r.rows {
		out = append(out, row.row)
	}
	return out, nil
}
func (r *activeReadStore) PermissionRequestsForSpawn(id string) ([]PermissionRow, error) {
	checked := r.byID[id]
	if checked == nil {
		return nil, store.ErrSpawnNotFound
	}
	if checked.permissionErr != nil {
		return nil, checked.permissionErr
	}
	if checked.judged != nil {
		return checked.judged.rows, nil
	}
	if reader := activePermissionReader(r.source); reader != nil {
		return reader(id)
	}
	return nil, nil
}

// Preserve only the request readers the original provider offers. All paths
// feed readJudgedRequests, retaining its check-before-read hook sampling.
func activePermissionReader(source any) func(string) ([]PermissionRow, error) {
	if s, ok := source.(interface {
		PermissionRequestsForSpawnWithin(string, time.Duration) ([]PermissionRow, error)
	}); ok {
		return func(id string) ([]PermissionRow, error) { return s.PermissionRequestsForSpawnWithin(id, 0) }
	}
	if s, ok := source.(interface {
		PermissionRequestsForSpawn(string) ([]PermissionRow, error)
	}); ok {
		return s.PermissionRequestsForSpawn
	}
	if s, ok := source.(interface {
		OpenPermissionRequestsForSpawn(string) ([]PermissionRow, error)
	}); ok {
		return s.OpenPermissionRequestsForSpawn
	}
	return nil
}
func (r *activeReadStore) PermissionRequestsForSpawnWithin(id string, _ time.Duration) ([]PermissionRow, error) {
	return r.PermissionRequestsForSpawn(id)
}
func (r *activeReadStore) OpenPermissionRequestsForSpawn(id string) ([]PermissionRow, error) {
	rows, err := r.PermissionRequestsForSpawn(id)
	var out []PermissionRow
	for _, row := range rows {
		if row.AwaitsAnswer() {
			out = append(out, row)
		}
	}
	return out, err
}
func (r *activeReadStore) captureSessionHistory(checked *activeCheckedRow) {
	if !r.withHistory || checked.history != nil {
		return
	}
	if s, ok := r.source.(sessionHistorySnapshotReader); ok {
		row := checked.row
		history, err := s.ListSessionHistorySnapshot(row, 0)
		checked.history = &activeSessionHistory{row.Snapshot, row.Identity, row.LifeNumber, history, err}
		checked.record(activeOutcome("history", false, err))
	}
}

func (r *activeReadStore) ListSessionHistory(id string, life int64) ([]SessionHistoryEntry, error) {
	if checked := r.byID[id]; checked != nil && checked.row.LifeNumber == life {
		if history := checked.history; history != nil {
			if history.snapshot == checked.row.Snapshot && history.identity == checked.row.Identity && history.life == life {
				return history.rows, history.err
			}
		}
		if s, ok := r.source.(sessionHistorySnapshotReader); ok {
			return s.ListSessionHistorySnapshot(checked.row, 0)
		}
	}
	return readActiveSessionHistory(r.source, id, life)
}

func readActiveSessionHistory(source any, id string, life int64) ([]SessionHistoryEntry, error) {
	if s, ok := source.(interface {
		ListSessionHistoryWithin(string, int64, time.Duration) ([]SessionHistoryEntry, error)
	}); ok {
		return s.ListSessionHistoryWithin(id, life, 0)
	}
	return source.(GetStore).ListSessionHistory(id, life)
}
func (r *activeReadStore) ObserveNeedsIntelligence(row Spawn, _ []store.PermissionSample, _ time.Time, _ store.SettleRead) (store.DisplayDetail, map[string]string, error) {
	return r.byID[row.InstanceID].display, nil, nil
}
func (r *activeReadStore) ObserveIdleScreen(row Spawn, _ store.PaneSample, _ time.Time, _ time.Duration, _ store.SettleRead) (store.DisplayDetail, map[string]string, error) {
	return r.byID[row.InstanceID].display, nil, nil
}

// activeRefresh never selects membership: IDs were fixed by the caller.
func activeRefresh(source any, settle store.SettleRead, partial bool) func([]string, []store.CursorMissingProof) ([]Spawn, error) {
	return func(ids []string, proofs []store.CursorMissingProof) ([]Spawn, error) {
		s := source
		if projected, ok := source.(interface {
			WithCursorMissingReads([]store.CursorMissingProof) *store.CursorMissingReads
		}); ok && len(proofs) > 0 {
			s = projected.WithCursorMissingReads(proofs)
		}
		if partial {
			if reader, ok := s.(interface {
				RecoveryRows([]string, store.SettleRead) ([]Spawn, error)
			}); ok {
				return reader.RecoveryRows(ids, settle)
			}
		}
		if reader, ok := s.(ListStore); ok {
			return reader.ListSpawns(ListFilters{IDs: ids, Settle: settle})
		}
		out := []Spawn{}
		for _, id := range ids {
			if reader, ok := s.(GetStore); ok {
				row, _, err := readSpawnSettled(reader, settle, id)
				if err != nil {
					return nil, err
				}
				out = append(out, row)
			} else if reader, ok := s.(permissionStatusReader); ok {
				row, err := reader.SpawnStatusRow(id, settle)
				if err != nil {
					return nil, err
				}
				out = append(out, row)
			}
		}
		return out, nil
	}
}
