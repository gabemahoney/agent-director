package api

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// FindMissingStore is the narrow store surface find-missing needs (SRD
// Appendix F.4): the live-row read, the life-guarded mark, note write and
// clear (SR-11.6), the permission-request denial, the provisional-transcript
// healing, and this store's id. *store.Store satisfies it.
type FindMissingStore interface {
	// ListLiveSpawnIdentities reads every row in a live state, pending
	// included, with its state, launch start, recorded session name,
	// liveness note, row snapshot and launch identity (SR-11.7).
	ListLiveSpawnIdentities() ([]LiveSpawnIdentity, error)
	// MarkMissingIfSameLife marks a live row missing in one write guarded on
	// examined, the row snapshot the sweep read or its adoption produced, with
	// the liveness clear and the launch-start clear folded in (SR-11.3,
	// SR-11.6; Appendix F.4). It returns the prior state and CondApplied when
	// it applied, CondChanged or CondAbsent (prior state "") when it did not,
	// and a store error as an error with a zero CondResult (SR-5.8).
	MarkMissingIfSameLife(instanceID string, examined RowSnapshot) (priorState string, res CondResult, err error)
	// SetLivenessNoteIfSameLife overwrites a live row's liveness note, keeping
	// its first unverified time, in one write guarded on examined (SR-11.4,
	// SR-11.6). It writes even a note equal to the stored one: skipping an
	// equal note is the caller's rule. Results as MarkMissingIfSameLife's.
	SetLivenessNoteIfSameLife(instanceID string, examined RowSnapshot, note string) (CondResult, error)
	// ClearLivenessIfSameLife NULLs a live row's liveness note and unverified
	// time in one write guarded on examined (SR-11.4, SR-11.6). It writes even
	// a row with no note: not writing one is the caller's rule. Results as
	// MarkMissingIfSameLife's.
	ClearLivenessIfSameLife(instanceID string, examined RowSnapshot) (CondResult, error)
	// CloseOrphanedPermissionRequests denies all open permission_requests rows
	// for a Spawn that has just been marked missing, so any relay polling loop
	// for that Spawn receives a fail-closed deny rather than spinning to its own
	// internal timeout (SR-5.4).
	CloseOrphanedPermissionRequests(instanceID string) error
	// ListProvisionalTranscripts returns live rows with a session id but a NULL
	// jsonl_path — sessions that started before their transcript was written
	// (b.v2c AC3). find-missing recomposes and stats each, healing rows whose
	// transcript has since appeared.
	ListProvisionalTranscripts() ([]ProvisionalTranscript, error)
	// HealJsonlPath records a now-present transcript path onto a provisional
	// row (jsonl_path NULL) for the given session id. Returns true when written.
	HealJsonlPath(instanceID, sessionID, jsonlPath string) (bool, error)
	// StoreID returns this store's store_meta.store_id, the id every label
	// the sweep's lookup accepts ends with and the store_id its
	// ad.launch.name_held records carry (SR-3.4, SR-14; WD 2026-09-29 STORE).
	StoreID() string
}

// ProvisionalTranscript is re-exported from internal/store so external
// consumers can name the type in the FindMissingStore interface without
// importing internal/store directly (b.v2c AC3).
type ProvisionalTranscript = store.ProvisionalTranscript

// FindMissingResult is the typed return shape. Count is the number of
// rows transitioned to `missing` on this sweep; IDs is the sorted
// list (sorted so the JSON envelope is deterministic across runs).
type FindMissingResult struct {
	// Count is the number of rows transitioned to missing on this sweep.
	Count int `json:"count"`
	// IDs is the sorted slice of instance ids transitioned to missing.
	// Always non-nil — encodes as [] when no rows were transitioned.
	IDs []string `json:"ids"`
	// Unverified is the number of live rows this sweep left live with a
	// liveness note because their agent process could not be checked: the
	// start-time reader could not tell, a pid-only identity read alive, or no
	// process identity is recorded (SR-11.1, SR-11.3, SR-11.4). A row whose
	// note write found it changed, or failed in the store, is not counted.
	Unverified int `json:"unverified"`
	// UnverifiedIDs is the sorted slice of instance ids left unverified.
	// Always non-nil — encodes as [] when no rows were unverified (same
	// discipline as IDs).
	UnverifiedIDs []string `json:"unverified_ids"`
}

// FindMissingLogger is the narrow log surface FindMissing uses to report
// per-row store errors. *log.Logger satisfies it; tests pass a fake to
// inspect the message without scraping stderr.
type FindMissingLogger interface {
	Printf(format string, v ...any)
}

// find-missing's mark reason and liveness notes on the process path (SR-11.1,
// SR-11.3, SR-11.4).
const (
	// reasonProcAbsent is the mark reason of a row whose agent process is
	// gone: absent, a zombie, or alive with another start time.
	reasonProcAbsent = "proc_absent"
	// noteProbeEACCES is the note of a row whose agent process evidence is
	// unknown: the start-time reader cannot tell, or a pid-only identity
	// reads alive.
	noteProbeEACCES = "probe_eacces"
	// noteProcessNotSeenTmuxUnchecked is the note of a row with no process
	// identity recorded whose tmux lookup was not called.
	noteProcessNotSeenTmuxUnchecked = "process_not_seen_tmux_unchecked"
)

// The ad.provenance.disagree action values a find-missing row's records
// carry (SR-14): what the sweep did to the row, known once its write settled.
const (
	findMissingActionMarked     = "marked_missing"
	findMissingActionLeftLive   = "left_live"
	findMissingActionUnverified = "left_unverified"
	findMissingActionChanged    = "left_changed"
	findMissingActionStoreError = "store_error"
)

// findMissingRow is the outcome of judging one live row.
type findMissingRow struct {
	// write is the row's writer outcome (markMissingSameLife,
	// writeLivenessNote or clearLivenessNote). For a mark, write.Listed puts
	// the row in ids; for a note, in unverified_ids.
	write findMissingWrite
	// marked reports that write came from the mark rather than a note write
	// or clear.
	marked bool
	// action is the ad.provenance.disagree action for a write that applied
	// or was not needed (findMissingActionMarked, findMissingActionLeftLive,
	// findMissingActionUnverified); findMissingAction turns a refused or
	// failed write into left_changed or store_error.
	action string
	// disagree is the row's distinct ad.provenance.disagree reasons,
	// collected over the row's judgement and written once after its write
	// settled. The process path reports none (pid_mismatch is retired,
	// SR-3.8); the tmux path adds the lookup's.
	disagree []string
}

// findMissingAction is the ad.provenance.disagree action of a judged row:
// store_error when its write failed in the store, left_changed when the write
// found the row changed or absent, and the row's own action otherwise.
func findMissingAction(r findMissingRow) string {
	switch {
	case r.write.Err != nil:
		return findMissingActionStoreError
	case r.write.Wrote && r.write.Res != CondApplied:
		return findMissingActionChanged
	}
	return r.action
}

// findMissingImpl is the unexported verb handler called by
// (c *Client).FindMissing; external consumers use the Client method.
//
// pc is the start-time reader (SR-3.8), the only source of liveness: the
// sweep never reads a process environment. pendingGrace is the pending grace
// period (SR-11.2) as a duration; the handler uses it exactly as given, with
// no default and no minimum check of its own (SR-4.1: the configuration's
// minimum, config.PendingGraceMinimumSeconds, is 30 s, rising with the create
// timeout and pipe-close wait, and is enforced when the configuration loads).
// now is the clock every age is measured against; the handler never reads
// time.Now itself (Appendix F.5).
//
// Behaviour (SR-11.1 to SR-11.7):
//
//  1. List the live rows (every live state, pending included). A list error
//     fails the sweep.
//  2. A `pending` row inside its grace period, measured from its launch
//     start (store.InsidePendingGrace), is left untouched: no reader call, no
//     write, no event, and it is in neither result list. Such a row may be a
//     spawn's, a reuse's or a resume's launch. A `pending` row with no
//     readable launch start is past the grace period and judged at once
//     (SR-22.8).
//  3. Every other row is judged by its agent process alone (judgeLiveRow):
//     alive leaves it live and clears any note; dead marks it missing with
//     reason proc_absent; unknown or no recorded identity leaves it
//     unverified with a note. A child process or another process carrying
//     the row's id never keeps it alive. Each write is guarded on the row
//     snapshot the sweep read (SR-11.6): a write that finds the row changed
//     or absent, or fails in the store, leaves the row in neither list with
//     no tick. The start-time reader's unreadable answer and every per-row
//     store error are row outcomes, never a sweep error (SR-11.7, SR-5.8).
//  4. Provisional transcripts are healed (healProvisionalTranscripts).
//
// The result lists are sorted. The only error is the live-row read's.
func findMissingImpl(ctx context.Context, s FindMissingStore, pc ProcChecker, pendingGrace time.Duration, now func() time.Time, lg FindMissingLogger) (FindMissingResult, error) {
	identities, err := s.ListLiveSpawnIdentities()
	if err != nil {
		return FindMissingResult{}, err
	}
	sweepNow := now()

	missing := make([]string, 0)
	unverified := make([]string, 0)
	for _, it := range identities {
		if store.InsidePendingGrace(it.State, it.LaunchStartedAtMillis, pendingGrace, sweepNow) {
			// A booting launch inside its grace period (SR-11.2): not judged.
			continue
		}
		row := judgeLiveRow(s, pc, it, lg)
		switch {
		case !row.write.Listed:
		case row.marked:
			missing = append(missing, it.ClaudeInstanceID)
		default:
			unverified = append(unverified, it.ClaudeInstanceID)
		}
		emitFindMissingDisagree(it, row)
	}

	// b.v2c AC3: lazy transcript healing. A session that started before its
	// first user turn had no .jsonl on disk when SessionStart fired, so its row
	// carries a NULL jsonl_path. Once the agent's first turn is written the
	// file appears; this sweep recomposes each provisional row's path, stats it, and
	// records it when present — no operator intervention required. Per-row
	// errors are logged and skipped; healing never aborts the sweep.
	healProvisionalTranscripts(s, lg)

	// Stable order in the result envelope.
	sort.Strings(missing)
	sort.Strings(unverified)
	return FindMissingResult{
		Count:         len(missing),
		IDs:           missing,
		Unverified:    len(unverified),
		UnverifiedIDs: unverified,
	}, nil
}

// judgeLiveRow judges one live row past its grace period by its agent process
// (SR-3.8, SR-11.1, SR-11.4) and applies the outcome through the guarded
// writers, guarded on the snapshot the sweep read (SR-11.6).
//
// The agent process is selected from the row's SessionStart identity (PID,
// ProcStarttime) and its recorded pane identity (Identity.PanePID,
// Identity.PaneStarttime) by tmux.SelectAgentProcess: the one recorded, or
// the pane identity when both are recorded and disagree. It is judged once,
// by tmux.JudgeProcess through pc:
//
//   - alive with its recorded start time: the row stays live whatever tmux
//     shows, and a note it carries is cleared (no tick; a row with no note is
//     not written);
//   - gone (absent, a zombie, or another start time): marked missing with
//     reason proc_absent, with no tmux call;
//   - unknown (unreadable, or a pid-only identity reading alive) or no
//     identity recorded: tmuxUncheckedRow.
func judgeLiveRow(s FindMissingStore, pc ProcChecker, it LiveSpawnIdentity, lg FindMissingLogger) findMissingRow {
	agent := tmux.SelectAgentProcess(
		tmux.ProcIdentity{PID: it.PID, Starttime: it.ProcStarttime},
		tmux.ProcIdentity{PID: it.Identity.PanePID, Starttime: it.Identity.PaneStarttime},
	)
	switch state := tmux.JudgeProcess(pc, agent.Identity); state {
	case tmux.ProcAlive:
		return findMissingRow{
			write:  clearLivenessNote(s, it.ClaudeInstanceID, it.Snapshot, it.LivenessNote, lg),
			action: findMissingActionLeftLive,
		}
	case tmux.ProcGone:
		return findMissingRow{
			write:  markMissingSameLife(s, it.ClaudeInstanceID, it.Snapshot, reasonProcAbsent, nil, lg),
			marked: true,
			action: findMissingActionMarked,
		}
	default: // tmux.ProcUnknown, tmux.ProcNone
		return tmuxUncheckedRow(s, it, state, lg)
	}
}

// tmuxUncheckedRow is the outcome of a row whose agent process cannot decide
// its liveness: state is tmux.ProcUnknown (the evidence is unknown) or
// tmux.ProcNone (no identity recorded). SR-11.3's tmux path decides such a
// row; until that path is built, every such row takes SR-11.3's "Not called
// (tmux already skipped)" outcome: it is left live and unverified with note
// probe_eacces (evidence unknown) or process_not_seen_tmux_unchecked (no
// evidence recorded), written by writeLivenessNote, and is never marked. No
// tmux call is made. This is the one place the tmux lookup replaces.
func tmuxUncheckedRow(s FindMissingStore, it LiveSpawnIdentity, state tmux.ProcState, lg FindMissingLogger) findMissingRow {
	note := noteProcessNotSeenTmuxUnchecked
	if state == tmux.ProcUnknown {
		note = noteProbeEACCES
	}
	return findMissingRow{
		write:  writeLivenessNote(s, it.ClaudeInstanceID, it.Snapshot, it.LivenessNote, note, lg),
		action: findMissingActionUnverified,
	}
}

// emitFindMissingDisagree writes a judged row's ad.provenance.disagree
// records once its write settled (SR-14, SR-3.16): one per distinct reason in
// row.disagree, nothing when it holds none. For a row judged without a lookup
// the records carry verb find-missing, source ad_find_missing, the row's
// recorded socket (Identity.Socket) and session name, tmux_session_id and
// current_session_name null, server unknown, verdict not_run, and the row's
// action (findMissingAction). Fail-open: a trail-write failure is discarded
// and never changes the sweep's result.
func emitFindMissingDisagree(it LiveSpawnIdentity, row findMissingRow) {
	if len(row.disagree) == 0 {
		return
	}
	emitProvenanceDisagree(provenanceDisagree{
		Verb:        "find-missing",
		Source:      findMissingTickSource,
		InstanceID:  it.ClaudeInstanceID,
		Socket:      it.Identity.Socket,
		SessionName: it.TmuxSessionName,
		Verdict:     tmux.TokenNotRun,
		Action:      findMissingAction(row),
		Caller:      callerIdentity(),
	}, row.disagree...)
}

// healProvisionalTranscripts sweeps live rows whose jsonl_path is NULL (a
// session that started before its transcript was written) and records the path
// for any whose transcript has since appeared on disk (b.v2c AC3). The path is
// recomposed the same way resume's fallback does: under the row's
// CLAUDE_CONFIG_DIR when set and absolute, else ~/.claude. Per-row store errors
// are logged and skipped; a compose or stat miss is a silent no-op (the row
// stays provisional until the transcript actually appears).
func healProvisionalTranscripts(s FindMissingStore, lg FindMissingLogger) {
	provisional, err := s.ListProvisionalTranscripts()
	if err != nil {
		if lg != nil {
			lg.Printf("find-missing: ListProvisionalTranscripts: %v (continuing)", err)
		}
		return
	}
	for _, pt := range provisional {
		var (
			path string
			cerr error
		)
		if pt.ConfigDir != "" && filepath.IsAbs(pt.ConfigDir) {
			path, cerr = spawn.JsonlPathIn(pt.ConfigDir, pt.CWD, pt.ClaudeSessionID)
		} else {
			path, cerr = spawn.JsonlPath(pt.CWD, pt.ClaudeSessionID)
		}
		if cerr != nil {
			continue
		}
		if _, statErr := os.Stat(path); statErr != nil {
			// Still not written — leave the row provisional.
			continue
		}
		if _, herr := s.HealJsonlPath(pt.ClaudeInstanceID, pt.ClaudeSessionID, path); herr != nil && lg != nil {
			lg.Printf("find-missing: HealJsonlPath(%s): %v (continuing)", pt.ClaudeInstanceID, herr)
		}
	}
}

// FindMissing reconciles live rows against their agents' processes. Each
// live row's liveness comes only from its agent process, read by the
// start-time reader: the SessionStart identity, else the recorded pane
// process, and the pane process when the two disagree; no process
// environment is read. A process alive with its recorded start time leaves
// the row live and clears any liveness note; a dead one marks the row
// missing. A row whose process cannot be checked (unreadable, a pid-only
// identity, or none recorded) is left live and reported unverified with a
// liveness note. Every write applies only while the row still holds the
// snapshot the sweep read. A `pending` row (a spawn's, a reuse's or a
// resume's launch) inside the pending grace period (pending_grace_seconds
// from the loaded configuration, 60 s by default), measured from its launch
// start, is not judged: it is left as it is and is in neither result list
// (SR-11.2). Ages are read from the Client's clock. Intended for periodic
// cron use.
//
// CLI: agent-director find-missing
//
// Errors:
//   - ErrProbeUnsupported: listed for find-missing, which no longer returns
//     it: the sweep reads only process start times, and a start time it
//     cannot read leaves that row unverified.
//
// Nondeterminism: none.
func (c *Client) FindMissing(ctx context.Context) (FindMissingResult, error) {
	if err := c.checkClosed(); err != nil {
		return FindMissingResult{}, err
	}
	return findMissingImpl(ctx, c.st, c.procChecker, c.cfg.Tmux.EffectivePendingGrace(), c.now, c.logger)
}
