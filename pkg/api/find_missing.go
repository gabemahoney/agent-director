package api

import (
	"context"
	"slices"
	"sort"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// FindMissingStore is the narrow store surface find-missing needs (SRD
// Appendix F.4): the live-row read, the life-guarded mark (with the denial of
// the row's open permission requests in the same transaction) and the
// life-guarded move of a Codex row with an end report to ended (EndedMover),
// note write and
// clear (SR-11.6), the life-guarded unreported note of a live pending row
// (b.kdf), the life-guarded adoption write of a lost create reply's identity
// (SR-3.6; LFR H2), the provisional-transcript healing, and this store's id,
// which every label the sweep's lookup accepts ends with. *store.Store
// satisfies it.
type FindMissingStore interface {
	// ListLiveSpawnIdentities reads every row in a live state, pending
	// included, with its state, launch start, recorded session name,
	// liveness note, row snapshot, launch identity, launch owner, agent and
	// end report (SR-11.7; b.kdf).
	ListLiveSpawnIdentities() ([]LiveSpawnIdentity, error)
	// MarkMissingIfSameLife marks a live row missing in one write guarded on
	// examined, the row snapshot the sweep read or its adoption produced, with
	// the liveness clear and the launch-start clear folded in (SR-11.3,
	// SR-11.6; Appendix F.4), and denies the row's open permission requests
	// (SR-5.4) in the same transaction: both or neither (b.146 rule 12), so
	// any relay polling for the row reads a fail-closed deny and a request of
	// a missing row is never left open. It returns the prior state and
	// CondApplied when it applied, CondChanged or CondAbsent (prior state "")
	// when it did not, and a store error as an error with a zero CondResult
	// (SR-5.8), having written nothing.
	MarkMissingIfSameLife(instanceID string, examined RowSnapshot) (priorState string, res CondResult, err error)
	// EndedMover: MarkEndedIfSameLife moves a live Codex row that holds an end
	// report (the SessionEnd of its agent) to ended instead, once the sweep
	// proved its agent process gone: the same write guarded on examined, with
	// the same request close, the row's subagent close and the same results.
	// Used only where the process is gone; tmux evidence alone still marks
	// missing.
	EndedMover
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
	// NoteUnreportedIfSameLife writes liveness note unreported on a pending
	// row, keeping its first flagged time (liveness_unverified_since, set by
	// whichever note came first and kept across note changes), or setting it
	// when the row has none, in one write guarded on pending and
	// examined, the snapshot the sweep read or its adoption produced, so a
	// hook that writes first wins (b.kdf, b.146 rule 10). The state stays
	// pending. It checks nothing else: whether the row is noted is the
	// sweep's decision (aliveRow), and so is skipping a row already noted.
	// Results as MarkMissingIfSameLife's.
	NoteUnreportedIfSameLife(instanceID string, examined RowSnapshot) (CondResult, error)
	// AdoptIdentityIfSameLife records the server and pane identity the
	// sweep's adoption found for a live row whose lookup is Ours (SR-3.6),
	// in one write guarded on a live state and examined, the snapshot the
	// sweep read; it advances row_version and, when it applied, returns the
	// row's new snapshot, which guards the sweep's later write for the row
	// (SR-11.6; LFR H2). Otherwise it returns CondChanged or CondAbsent with
	// a zero snapshot, and a store error as an error with a zero CondResult
	// (SR-5.8).
	AdoptIdentityIfSameLife(instanceID string, examined RowSnapshot, id LaunchIdentity) (res CondResult, now RowSnapshot, err error)
	// ListProvisionalTranscripts returns live Claude rows (never a Codex row)
	// with a session id but a NULL jsonl_path — sessions that started before
	// their transcript was written (b.v2c AC3). find-missing recomposes and
	// stats each, healing rows whose transcript has since appeared.
	ListProvisionalTranscripts() ([]ProvisionalTranscript, error)
	// HealJsonlPath records a now-present transcript path onto a provisional
	// row (jsonl_path NULL) for the given session id. Returns true when written.
	HealJsonlPath(instanceID, sessionID, jsonlPath string) (bool, error)
	// StoreID returns this store's store_meta.store_id, the id every label
	// the sweep's lookup accepts ends with and the store_id its
	// ad.launch.name_held records carry (SR-3.4, SR-14; WD 2026-09-29 STORE).
	StoreID() string
}

// FindMissingTmux is find-missing's tmux interface (Appendix F.4): the
// one-call lookup on a socket and, for the adoption of a lost create reply's
// pane (SR-3.6), the pane listing. The sweep takes both only through one
// tmux.Sweep per run: at most one lookup and one pane listing per socket, the
// per-socket stop rule and the run's tmux time budget (SR-3.15, SR-13.3,
// SR-13.5). Every method takes the row's socket (SR-3.3) and reports a
// failure as *TmuxCallError. TmuxClient, *tmux.Client and tmuxfix.Recorder
// satisfy it.
type FindMissingTmux interface {
	TmuxLookup
	// ListPanes lists every pane of the server at socket, each with its pane
	// label's token (TmuxPane.AdPane).
	ListPanes(socket string) ([]TmuxPane, error)
}

// The production types satisfy find-missing's interfaces, and those carry
// what the sweep's adoption step and pane listing need.
var (
	_ FindMissingStore = (*store.Store)(nil)
	_ FindMissingTmux  = TmuxClient(nil)
	_ FindMissingTmux  = (*tmux.Client)(nil)
	_ sameLifeAdopter  = FindMissingStore(nil)
	_ tmux.PaneLister  = FindMissingTmux(nil)
)

// ProvisionalTranscript is re-exported from internal/store so external
// consumers can name the type in the FindMissingStore interface without
// importing internal/store directly (b.v2c AC3).
type ProvisionalTranscript = store.ProvisionalTranscript

// FindMissingResult is the typed return shape. Count is the number of
// rows this sweep marked `missing`; IDs is the sorted list (sorted so the
// JSON envelope is deterministic across runs). A Codex row the sweep moved to
// `ended` (its agent process gone, with an end report) is in neither.
type FindMissingResult struct {
	// Checks retains every selected agent, with independent evidence and coverage.
	Checks []SweepCheck `json:"checks"`
	// Count is the number of rows this sweep marked missing: the length of
	// IDs. A Codex row moved to ended is not counted.
	Count int `json:"count"`
	// IDs is the sorted slice of instance ids this sweep marked missing, on
	// process or tmux evidence: rows whose agent process (the SessionStart
	// one or the recorded pane's) is gone, except a Codex row holding an end
	// report, which the sweep moves to ended instead and does not list, and rows whose process could not
	// be checked and whose lookup found no session or pane of their current
	// launch; a pending row only past the pending grace period, never while
	// its launch owner holds it (except a Cursor row whose recorded gateway is
	// exactly gone, which neither protects), and never for being noted
	// unreported. A row whose guarded mark found it changed or absent, or
	// failed in the store, is not listed. missing is a check's judgement on
	// available evidence, not proof that the agent has exited. Always non-nil
	// — encodes as [] when no rows were marked.
	IDs []string `json:"ids"`
	// Unverified is the number of live rows this sweep left live with a
	// liveness note: their agent process could not be checked (the
	// start-time reader could not tell, a pid-only identity read alive, or no
	// process identity is recorded) and either their recorded session name
	// cannot be used, so no tmux call was made for them (a note of its own,
	// SR-3.2), or the tmux lookup of the row's socket did not mark them (Ours,
	// Can't tell, or not called) (SR-11.1, SR-11.3, SR-11.4). It counts rows
	// whose note was already current. A row whose note write found it
	// changed, or failed in the store, is not counted, and neither is a
	// pending row noted unreported (b.kdf): its agent process was checked and
	// is alive, so it is in neither IDs nor UnverifiedIDs; get and list show
	// its note.
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

// find-missing's mark reasons and liveness notes (SR-11.1, SR-11.3, SR-11.4).
// noteProvenanceConflict is declared beside the writers that compare it.
const (
	// reasonProcAbsent is the mark reason of a row whose agent process is
	// gone: absent, a zombie, or alive with another start time.
	reasonProcAbsent = "proc_absent"
	// reasonTmuxAbsent is the mark reason of a row whose lookup is Gone or
	// Leftover while no session holds its recorded name, and of a row that
	// records no pane whose Ours session has no pane carrying its launch
	// token.
	reasonTmuxAbsent = "tmux_absent"
	// reasonTmuxNameHeld is the mark reason of a row whose lookup is Gone or
	// Leftover while a session holds its recorded name (one holder, or more
	// than one listing entry matching it).
	reasonTmuxNameHeld = "tmux_name_held"
	// noteProbeEACCES is the note of a row whose agent process evidence is
	// unknown (the start-time reader cannot tell, or a pid-only identity
	// reads alive) and whose lookup is Ours, unreadable, tmux unavailable or
	// not called.
	noteProbeEACCES = "probe_eacces"
	// noteProcessNotSeenSessionPresent is the note of a row with no process
	// identity recorded whose lookup is Ours, unless the adoption listing
	// decided otherwise (SR-11.3).
	noteProcessNotSeenSessionPresent = "process_not_seen_session_present"
	// noteProcessNotSeenTmuxUnchecked is the note of a row with no process
	// identity recorded whose lookup was unreadable, tmux unavailable or not
	// called.
	noteProcessNotSeenTmuxUnchecked = "process_not_seen_tmux_unchecked"
	// noteTmuxServerChanged is the note of a row whose lookup is Can't tell,
	// a different server.
	noteTmuxServerChanged = "tmux_server_changed"
	// noteTmuxSessionNameEmpty, noteTmuxSessionNameControlChar and
	// noteTmuxSessionNameRewritten are the notes of a row whose agent process
	// evidence is unknown or absent and whose recorded session name cannot be
	// used (SR-3.2, SR-11.3, SR-11.4): empty, holding a control character, or
	// holding a character tmux stores differently (`.`, `:` or invalid
	// UTF-8). No tmux call is made for such a row, so this note wins over
	// every tmux-path note (unusableNameNote).
	noteTmuxSessionNameEmpty       = "tmux_session_name_empty"
	noteTmuxSessionNameControlChar = "tmux_session_name_control_char"
	noteTmuxSessionNameRewritten   = "tmux_session_name_rewritten"
	// noteUnreported is the note of a pending row past its grace period,
	// held by no live launch owner, whose pane is recorded (or was just
	// adopted) and whose agent process is alive, but which no hook has
	// reported since its launch (b.kdf, b.146 rule 10; aliveRow). The agent
	// may sit at a Claude Code startup screen or idle at its prompt; the row
	// stays pending, and whoever looks at its pane decides what to type. A
	// later sweep that finds the agent alive keeps it; a sweep that cannot
	// check the process may overwrite it with an unverified note. Every note
	// change keeps the time a note first flagged the row
	// (liveness_unverified_since), and the agent's next hook clears both
	// (every applied hook does). It never overwrites provenance_conflict.
	noteUnreported = store.LivenessNoteUnreported
)

// unusableNameNote is the one mapping from the unusable-name guard's kind
// (tmux.Unusable) to find-missing's note (SR-3.2, SR-11.4); "" for a usable
// name. The guard's precedence (empty, then control character, then
// rewritten) decides a name with several faults.
func unusableNameNote(kind tmux.UnusableKind) string {
	switch kind {
	case tmux.UnusableEmpty:
		return noteTmuxSessionNameEmpty
	case tmux.UnusableControl:
		return noteTmuxSessionNameControlChar
	case tmux.UnusableRewritten:
		return noteTmuxSessionNameRewritten
	}
	return ""
}

// The extra ad.find_missing.tick fields of a mark the lookup decided
// (SR-11.4, SR-14).
const (
	// tickLookupOutcome carries the lookup Result's token (tmux.Result.Token)
	// on every tmux_absent and tmux_name_held tick.
	tickLookupOutcome = "lookup_outcome"
	// tickSessionName carries the row's recorded session name on a
	// tmux_name_held tick.
	tickSessionName = "tmux_session_name"
)

// The ad.provenance.disagree action values a find-missing row's records
// carry (SR-14): what the sweep did to the row, known once its write settled.
// findMissingActionMarked is also the action of a Codex row moved to ended on
// a gone process with an end report (goneRow): the adopted-pane path keeps its
// existing value and the trail gains no new one.
const (
	findMissingActionMarked     = nameHeldRowMarkedMissing
	findMissingActionLeftLive   = "left_live"
	findMissingActionUnreported = "left_unreported"
	findMissingActionUnverified = "left_unverified"
	findMissingActionChanged    = "left_changed"
	findMissingActionStoreError = "store_error"
)

// findMissingRow is the outcome of judging one live row.
type findMissingRow struct {
	guard RowSnapshot
	// write is the row's writer outcome (markMissingSameLife,
	// writeLivenessNote, clearLivenessNote or writeUnreportedNote), or, for
	// a row whose adoption write did not apply, that write's outcome (never
	// Listed). For a mark, write.Listed puts the row in ids; for a note, in
	// unverified_ids; an unreported note is never listed.
	write findMissingWrite
	// marked reports that write came from the mark rather than a note write
	// or clear.
	marked bool
	// action is the ad.provenance.disagree action for a write that applied
	// or was not needed (findMissingActionMarked, findMissingActionLeftLive,
	// findMissingActionUnreported, findMissingActionUnverified);
	// findMissingAction turns a refused or failed write into left_changed or
	// store_error.
	action string
	// disagree is the row's ad.provenance.disagree reasons that came from
	// its lookup, collected over the row's judgement and written once its
	// write settled, each record carrying the lookup's server and verdict
	// (res). The process path reports none (pid_mismatch is retired,
	// SR-3.8); the tmux path adds the lookup Result's, name_changed and
	// adopted (SR-3.16, SR-14).
	disagree []string
	// socket is the socket the row's lookup used; "" when the row was judged
	// without one, or its socket could not be resolved.
	socket string
	// res is the row's lookup Result; the zero Result when the row was judged
	// without a lookup.
	res tmux.Result
	// probe is the row's one judgement of its recorded agent process at its
	// recorded start identity (judgeLiveRow), the process_liveness attempt the
	// shared checks record; the zero probe when no judgement ran.
	probe processProbe
	// listing is the Result of the row's adoption pane listing when it did
	// not answer (sweepAdoption.Listing); the zero Result otherwise. Its
	// Disagree reasons (for example server_mismatch on a no-server reply
	// while the recorded server is alive) are written with the listing's own
	// session, server and verdict, except a reason the lookup already
	// reported, which is written once with the lookup's (SR-3.16, SR-14).
	listing tmux.Result
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

// FindMissing is the compatibility alias of Sweep (SR-11, SR-16.2 item 5;
// Appendix F.4; LFR G5). Both use the same checker and result contract.
//
// The start-time reader pc (SR-3.8; LFR C1) is the only source of liveness,
// and the sweep never reads a process environment. t is the lookup and the pane listing (FindMissingTmux), called
// only through the run's one tmux.Sweep and only for rows whose agent process
// cannot be checked. pendingGrace is the pending grace period (SR-11.2),
// sweepBudget the run's tmux time budget (SR-13.5) and now the clock every
// age and every tmux call's time is measured against; FindMissing never reads
// time.Now itself (Appendix F.5).
//
// FindMissing uses the values it is given, with no fallback and no minimum
// check (SR-4.1): the configuration file enforces the minimums when it loads,
// and a direct caller passes values at or above them. The grace period's
// safe minimum is the derived config.PendingGraceMinimumSeconds (30 s at the
// default create timeout and pipe-close wait); a shorter one can mark a
// booting agent's row. The budget has no safe minimum: a short one leaves
// more rows unverified, and a non-positive one makes no tmux call. s, t, pc
// and now must not be nil; a nil lg writes no log line.
//
// Behaviour (SR-11.1 to SR-11.7, SR-3.15, SR-13.3, SR-13.5):
//
//  1. Select all stored rows in one snapshot. Finished rows admit only
//     late native proof; live rows enter liveness checks. A selection error
//     fails the sweep. Rows are judged in instance-id order, so the per-socket
//     stop and the budget's cut-off fall on the same rows on every run.
//  2. A `pending` row inside its grace period, measured from its launch
//     start (store.InsidePendingGrace), receives no liveness check and is in
//     neither result list. Provisional transcript healing remains eligible. Such a
//     row may be a spawn's, a reuse's or a resume's launch. A `pending` row
//     with no readable launch start is past the grace period and judged at
//     once (SR-22.8). A `pending` row past its grace period whose launch
//     still holds it is left untouched in the same way (b.kdf, b.146 rule
//     11): its recorded launch owner is provably alive
//     (spawn.LaunchOwnerAlive: this process's pid namespace equals the
//     owner's, and the owner's pid is alive with its recorded start time,
//     one start-time read). A row with no owner recorded (from before schema
//     v6, or a launch whose hold has ended), an owner in another or
//     unreadable pid namespace, and an owner gone or unreadable are judged as
//     before, by the grace period alone. Neither protects a Cursor row whose
//     recorded gateway is exactly gone: it is marked missing as in 5; any
//     other gateway verdict leaves it protected and unnoted.
//  3. Every other row is judged by its agent process (judgeLiveRow): alive
//     leaves it live and clears any note it carries, except that a `pending`
//     row whose pane is recorded (or was just adopted) stays pending and is
//     noted unreported (b.kdf, b.146 rule 10; aliveRow,
//     writeUnreportedNote): its agent runs, but no hook has reported since
//     its launch. Such a row is in neither result list; a row already noted
//     unreported is not written again, a note write keeps the time a note
//     first flagged the row (liveness_unverified_since), and a
//     row noted provenance_conflict keeps that note, which unreported never
//     overwrites. Dead marks it missing with
//     reason proc_absent, denying its open permission requests in the same
//     transaction; a dead Codex row that holds an end report is moved to
//     ended instead (goneRow; same tick reason, same request close, the new
//     state ended), and is in neither result list. Neither makes a tmux call. A child process or another
//     process carrying the row's id never keeps it alive.
//  4. A row whose process evidence is unknown or absent and whose recorded
//     session name cannot be used (tmux.Unusable: empty, then a control
//     character, then a character tmux stores differently) is left
//     unverified with note tmux_session_name_empty,
//     tmux_session_name_control_char or tmux_session_name_rewritten, ahead
//     of every tmux-path note: no tmux call, no socket resolution, and it
//     takes no lookup, stop or budget from its socket or the run (SR-3.2,
//     SR-11.3, SR-11.4). Removing such a row is a human's decision.
//  5. Any other row whose process evidence is unknown or absent is decided
//     by the lookup of its socket, per SR-11.3's table (lookupRow): Ours
//     leaves it unverified (after adoption, a row that records no pane is
//     judged by the adopted pane's process, or marked when no pane carries
//     its launch token); Leftover and Gone mark it missing, with tick reason
//     tmux_name_held and one ad.launch.name_held when a session holds its
//     recorded name, tmux_absent otherwise; Can't tell and not called leave
//     it unverified with a note. One tmux.Sweep serves the run: one lookup
//     and at most one pane listing per socket, taken at the first row that
//     needs it, no more calls on a socket after an unreadable or unavailable
//     result there, and none at all once sweepBudget is spent. tmux problems
//     never fail the sweep. A Cursor row with a recorded gateway (pid and
//     start time) never comes here: an unknown gateway verdict leaves it
//     unverified (probe_eacces) with no tmux call, and only an exact gone
//     verdict marks it missing.
//  6. Every write is guarded on the row snapshot the sweep read, or on the
//     one its adoption write produced (SR-11.6): a write that finds the row
//     changed or absent, or fails in the store, leaves the row in neither
//     list with no tick. The start-time reader's unreadable answer and every
//     per-row store error are row outcomes, never a sweep error (SR-11.7,
//     SR-5.8). A row's ad.provenance.disagree records are written once its
//     write settled.
//  7. The selected live rows also receive eligible missed-turn, permission,
//     owned-pane and provisional Claude transcript checks. Late exact native
//     proof uses only its persistence seam; nothing sends input or answers.
//
// The result lists are sorted. The only error FindMissing returns is the
// selection read's, and (c *Client).FindMissing adds one, ErrClientClosed on a
// closed Client, before it calls FindMissing. Every failure returns an empty
// FindMissingResult (counts 0, IDs and UnverifiedIDs non-nil and empty), as
// a sweep that judged no row does (b.hbt).
//
// A row marked missing is a check's judgement on available evidence,
// not proof that the agent has exited; neither ended nor missing means
// that the agent is dead or that its row is safe to delete (SR-18.2). The
// caller must run as the agents' user, in their tmux environment: run as
// another user, as root or against another tmux server, the sweep can mark
// live rows missing (SR-18.7).
//
// Errors:
//   - ErrProbeUnsupported: never returned; catalogued for client mapping:
//     the sweep reads only process start times, and a start time it
//     cannot read leaves that row unverified.
//   - [ErrStoreBusy]: the store's zero-wait read selecting the agents did not
//     finish within its bound: another call of this process held the store's
//     operation slot or connection (an earlier call's operation keeps them
//     until it is disposed), another connection held a lock the read needed,
//     or the read ran out of time. Nothing was checked.
func FindMissing(ctx context.Context, s FindMissingStore, t FindMissingTmux, pc ProcChecker, pendingGrace, sweepBudget time.Duration, now func() time.Time, lg FindMissingLogger) (FindMissingResult, error) {
	return Sweep(ctx, s, t, pc, pendingGrace, sweepBudget, now, lg)
}

func findMissingChecked(ctx context.Context, s FindMissingStore, view RelayView, settle store.SettleRead) (FindMissingResult, error) {
	if concrete, ok := s.(*store.Store); ok {
		s = concrete.WithRecoveryTickSource(findMissingTickSource)
	}
	// Copy invocation providers: reporting must not mutate a caller-shared view.
	providers := activeCheckProviders{}
	if view.active != nil {
		providers = *view.active
	}
	providers.tickSource = findMissingTickSource
	view.active = &providers
	empty := emptySweepResult()
	var rows []Spawn
	var refresh func([]string, []store.CursorMissingProof) ([]Spawn, error)
	if reader, ok := s.(interface {
		RecoveryRows([]string, store.SettleRead) ([]Spawn, error)
	}); ok {
		var err error
		rows, err = reader.RecoveryRows(nil, settle)
		if err != nil {
			return empty, err
		}
		refresh = activeRefresh(s, settle, true)
	} else {
		// A legacy narrow store exposes only its live identities. It cannot
		// promise all-agent snapshot selection or the richer adapters.
		identities, err := s.ListLiveSpawnIdentities()
		if err != nil {
			return empty, err
		}
		for _, it := range identities {
			rows = append(rows, Spawn{InstanceID: it.InstanceID, State: it.State, ReportedState: it.State,
				PID: it.PID, ProcStarttime: it.ProcStarttime, TmuxSessionName: it.TmuxSessionName,
				LaunchStartedAtMillis: it.LaunchStartedAtMillis, LivenessNote: it.LivenessNote,
				Snapshot: it.Snapshot, Identity: it.Identity, LaunchOwner: it.LaunchOwner, Agent: it.Agent, EndReportedAtMillis: it.EndReportedAtMillis})
		}
	}
	if refresh == nil {
		// Legacy adapters expose transcript candidates separately. Capture
		// them before external sampling and keep that fixed scope too.
		view.provisional = map[string]ProvisionalTranscript{}
		view.provisionalOnly = map[string]bool{}
		present := map[string]bool{}
		for _, row := range rows {
			present[row.InstanceID] = true
		}
		candidates, err := s.ListProvisionalTranscripts()
		if err != nil {
			logFindMissing(view.active.logger, "ListProvisionalTranscripts", "", err)
		}
		for _, pt := range candidates {
			view.provisional[pt.InstanceID] = pt
			if !present[pt.InstanceID] {
				rows = append(rows, Spawn{InstanceID: pt.InstanceID, SessionID: pt.SessionID, State: store.StateWaiting, ReportedState: store.StateWaiting})
				view.provisionalOnly[pt.InstanceID] = true
			}
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].InstanceID < rows[j].InstanceID })
	run := checkActiveRows(ctx, s, view, settle, rows, refresh, false)
	// A sweep returns committed counts; its per-agent errors remain in the
	// pass and never suppress independent selected rows.
	_ = run.finish()
	for _, proof := range run.cursor {
		checked := run.byID[proof.Row.InstanceID]
		if checked.cursorRejected && checked.projectionErr == nil && checked.row.State == store.StateMissing && checked.row.Snapshot == proof.Row.Snapshot {
			// Retain the selected stored value on a refused terminal write,
			// with its own evidence rather than the rejected projection.
			family := checked.row.FamilyState
			checked.row = proof.Row
			checked.row.FamilyState = family
			checked.display, checked.displayEvidence = proof.Row.CommittedDisplay(), proof.Row.DisplayEvidence
		}
	}
	run.missing.Checks = run.sweepChecks()
	return run.missing, nil
}

// findMissingRun is one find-missing run's state: the store, the tmux client
// and start-time reader, the run's one tmux.Sweep (lookups, adoption pane
// listings, the per-socket stop rule and the budget), the socket rule for
// rows that record none, and the caller identity, collected at most once.
type findMissingRun struct {
	s       FindMissingStore
	t       FindMissingTmux
	pc      ProcChecker
	sw      *tmux.Sweep
	lg      FindMissingLogger
	sockets sweepSockets
	// caller is the invoking process's identity, collected on first use and
	// shared by every trail record of the run.
	caller lazyCaller
	// emit writes the run's trail records: recoveryTrail under the operation
	// context of the check.
	emit trailEmit
	// now is the run's clock, the time of a proven exit's process record
	// (provenExit); nil records none.
	now func() time.Time
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
//     shows (aliveRow): a pending row whose pane is recorded is noted
//     unreported (b.kdf), and any other row's note is cleared (no tick; a
//     row with no note is not written);
//   - gone (absent, a zombie, or another start time): marked missing with
//     reason proc_absent, with no tmux call (a Codex row holding an end
//     report is moved to ended with the same reason, goneRow);
//   - unknown (unreadable, or a pid-only identity reading alive) or no
//     identity recorded: a row whose recorded session name cannot be used
//     (tmux.Unusable) is left unverified with its own note
//     (unusableNameNote), guarded on the snapshot the sweep read, with no
//     tmux call, no socket resolution and no charge to the Sweep (SR-3.2,
//     SR-11.3); otherwise the lookup decides (lookupRow).
func (r *findMissingRun) judgeLiveRow(it LiveSpawnIdentity) findMissingRow {
	agent := tmux.SelectAgentProcess(
		tmux.ProcIdentity{PID: it.PID, Starttime: it.ProcStarttime},
		tmux.ProcIdentity{PID: it.Identity.PanePID, Starttime: it.Identity.PaneStarttime},
	)
	state := tmux.JudgeProcess(r.pc, agent.Identity)
	// One instant stamps the judgement: the probe's evidence time and a gone
	// process's exited record.
	at := clockNow(r.now)
	var out findMissingRow
	switch state {
	case tmux.ProcAlive:
		out = r.aliveRow(findMissingRow{}, it, it.Snapshot, it.Identity)
	case tmux.ProcGone:
		out = r.goneRow(findMissingRow{}, it, it.Snapshot, agent.Identity, at)
	default: // tmux.ProcUnknown, tmux.ProcNone
		if note := unusableNameNote(tmux.Unusable(it.TmuxSessionName)); note != "" {
			out = r.noteRow(findMissingRow{}, it, it.Snapshot, note)
		} else {
			out = r.lookupRow(it, state)
		}
	}
	// tmux's later verdict never changes what the process judgement found.
	out.probe = processProbe{judged: true, state: state, identity: agent.Identity, at: at}
	return out
}

// processProbe is one judgement of a row's recorded agent process
// (tmux.JudgeProcess on the identity tmux.SelectAgentProcess chose), made at
// at on the run's clock (zero when the run has none): the time of its
// evidence, however late the record of it is built.
type processProbe struct {
	judged   bool
	state    tmux.ProcState
	identity tmux.ProcIdentity
	at       time.Time
}

// attempt is the probe as a process_liveness attempt: alive or exited when
// the judgement established it at the recorded start identity, otherwise
// unknown with unreadable (a start time that could not be read, or a pid-only
// identity, whatever its pid showed: SR-4.8) or no_evidence (none recorded).
// A Helper, tmux or a descendant never stands in for it.
func (p processProbe) attempt() store.ProcessAttempt {
	a := store.ProcessAttempt{PID: p.identity.PID, Start: p.identity.Starttime, Value: store.ProcessUnknown}
	switch {
	case p.state == tmux.ProcNone:
		a.Reason = store.ReasonNoEvidence
	case p.identity.Starttime == "":
		a.Reason = store.ReasonUnreadable
	case p.state == tmux.ProcAlive:
		a.Value = store.ProcessAlive
	case p.state == tmux.ProcGone:
		a.Value = store.ProcessExited
	default:
		a.Reason = store.ReasonUnreadable
	}
	return a
}

// provenExit is the exited process a move on a gone agent process acts on:
// identity, judged gone at at, recorded in the move's own statement
// (store.ProcessExit). A pid-only identity, which cannot establish the exit of
// the recorded start identity (SR-4.8), and a judgement with no time (a run
// with no clock) name none, so the move clears the process record instead.
func provenExit(identity tmux.ProcIdentity, at time.Time) store.ProcessExit {
	if at.IsZero() || identity.PID <= 0 || identity.Starttime == "" {
		return store.ProcessExit{}
	}
	return store.ProcessExit{PID: identity.PID, Start: identity.Starttime, AtMillis: at.UnixMilli()}
}

// clockNow is now's time, the zero time for no clock.
func clockNow(now func() time.Time) time.Time {
	if now == nil {
		return time.Time{}
	}
	return now()
}

// aliveRow completes row for a row whose agent process was found alive,
// guarded on guard. ident is the launch identity the process was selected
// from: the one the sweep read, or the one its adoption just found.
//
// A pending row whose ident records its pane is noted unreported
// (unreportedRow; b.kdf, b.146 rule 10) and stays pending. The row is past
// its grace period and not held by a live launch owner (the caller skipped it
// otherwise), and its agent is alive, but no hook has reported since its
// launch. The agent may sit at a Claude Code startup screen, shown before any
// hook runs, or idle at its prompt, for example after its SessionStart hook
// died before its write; only something that reads the pane can tell
// these apart, so the sweep reports the row rather than moving it to waiting.
// Such a row never reaches clearRow, so the sweep never clears its note.
//
// Note precedence: provenance_conflict wins over unreported. Such a pending
// row whose note is provenance_conflict (more than one session carries its
// launch's label, so which session is its own is in doubt) keeps that note:
// nothing is written, no tick, in neither result list, action left_live.
// Telling a caller to read "the" pane and type would not help while the
// row's session is in doubt. The store write refuses that note too
// (NoteUnreportedIfSameLife).
//
// Any other row gets clearRow: a pending row that records no pane stays
// pending, judged as before b.kdf.
func (r *findMissingRun) aliveRow(row findMissingRow, it LiveSpawnIdentity, guard RowSnapshot, ident LaunchIdentity) findMissingRow {
	row.guard = guard
	if it.State == store.StatePending && ident.PanePID > 0 {
		if it.LivenessNote == noteProvenanceConflict {
			row.action = findMissingActionLeftLive
			return row
		}
		return r.unreportedRow(row, it, guard)
	}
	return r.clearRow(row, it, guard)
}

// unreportedRow completes row with the guarded unreported note of a live
// pending row (writeUnreportedNote), guarded on guard.
func (r *findMissingRun) unreportedRow(row findMissingRow, it LiveSpawnIdentity, guard RowSnapshot) findMissingRow {
	row.guard = guard
	row.write = writeUnreportedNote(r.emit, r.s, it.InstanceID, guard, it.LivenessNote, r.lg)
	row.action = findMissingActionUnreported
	return row
}

// clearRow completes row for a row whose agent process was found alive: the
// guarded clear of a note it carries (clearLivenessNote), guarded on guard.
func (r *findMissingRun) clearRow(row findMissingRow, it LiveSpawnIdentity, guard RowSnapshot) findMissingRow {
	row.guard = guard
	row.write = clearLivenessNote(r.s, it.InstanceID, guard, it.LivenessNote, r.lg)
	row.action = findMissingActionLeftLive
	return row
}

// markRow completes row with the guarded mark (markMissingSameLife), guarded
// on guard, with tick reason reason and the tick's extra fields.
func (r *findMissingRun) markRow(row findMissingRow, it LiveSpawnIdentity, guard RowSnapshot, reason string, extra map[string]any) findMissingRow {
	return r.markRowFrom(row, it, guard, reason, extra, store.ProcessExit{})
}

// markRowFrom is markRow for a mark that acted on exit, the proven exit of
// the row's recorded agent process (goneRow), the zero ProcessExit for none.
func (r *findMissingRun) markRowFrom(row findMissingRow, it LiveSpawnIdentity, guard RowSnapshot, reason string, extra map[string]any, exit store.ProcessExit) findMissingRow {
	row.guard = guard
	row.write = markMissingSameLife(r.emit, r.s, it.InstanceID, guard, reason, extra, exit, r.lg)
	row.marked = true
	row.action = findMissingActionMarked
	return row
}

// goneRow completes row for a row whose agent process was proven gone (the
// recorded identity's, or the adopted pane's), guarded on guard, with tick
// reason proc_absent. A Codex row that holds an end report (SR-6.5) gets the
// guarded move to ended through the shared outcome writer (markEndedSameLife):
// it is in neither ids nor unverified_ids, and its ad.provenance.disagree
// action is still marked_missing (no new action value). Any other row,
// whatever its agent and a Codex row without an end report included, is marked
// missing (markRow), exactly as before. Marks on tmux evidence alone (tmux
// absent, name held, no pane) never come here and stay missing whatever the end
// report.
//
// gone is the agent process identity judged gone at at: the move records its
// exited record in the same statement (provenExit), so the row's
// process_liveness and its terminal state commit together (SR-4.7).
func (r *findMissingRun) goneRow(row findMissingRow, it LiveSpawnIdentity, guard RowSnapshot, gone tmux.ProcIdentity, at time.Time) findMissingRow {
	row.guard = guard
	exit := provenExit(gone, at)
	if it.Agent != store.AgentCodex || it.EndReportedAtMillis <= 0 {
		return r.markRowFrom(row, it, guard, reasonProcAbsent, nil, exit)
	}
	row.write = markEndedSameLife(r.emit, r.s, it.InstanceID, guard, exit, reasonProcAbsent, nil, r.lg)
	row.marked = true
	row.action = findMissingActionMarked
	return row
}

// noteRow completes row with the note write (writeLivenessNote), guarded on
// guard: the row is left live and unverified with note.
func (r *findMissingRun) noteRow(row findMissingRow, it LiveSpawnIdentity, guard RowSnapshot, note string) findMissingRow {
	row.guard = guard
	row.write = writeLivenessNote(r.emit, r.s, it.InstanceID, guard, it.LivenessNote, note, r.lg)
	row.action = findMissingActionUnverified
	return row
}

// emitDisagree writes a judged row's ad.provenance.disagree records once its
// write settled (SR-14, SR-3.16, SR-11.3): one per distinct reason in
// row.disagree and row.listing.Disagree, in the order of disagreeReasons, so
// each reason is written at most once per row per sweep; nothing when both
// hold none (a normal row, a row judged without a lookup, a Skipped one).
// Each record carries the fields of the observation that produced its reason:
// a reason from the lookup (row.disagree) carries row.res, and one only the
// adoption listing reported carries row.listing; a reason both reported is
// written once, with the lookup's. From that Result each record takes the
// session's tmux id (null when none; for the lookup, the Ours session's),
// its stored name on the name_changed record, the Result's server value
// (unknown when no server check ran) and its token as the verdict (not_run
// when no call was made). Every record also carries verb find-missing,
// source ad_find_missing, the socket the lookup used (the row's recorded
// socket when none), the recorded session name, the row's action
// (findMissingAction) and the run's caller identity. Fail-open: a
// trail-write failure is discarded and never changes the sweep's result.
func (r *findMissingRun) emitDisagree(it LiveSpawnIdentity, row findMissingRow) {
	if len(row.disagree) == 0 && len(row.listing.Disagree) == 0 {
		return
	}
	socket := row.socket
	if socket == "" {
		socket = it.Identity.Socket
	}
	action := findMissingAction(row)
	for _, reason := range disagreeReasons {
		from := row.res
		switch {
		case slices.Contains(row.disagree, reason):
		case slices.Contains(row.listing.Disagree, reason):
			from = row.listing
		default:
			continue
		}
		verdict := from.Token()
		if verdict == "" {
			verdict = tmux.TokenNotRun
		}
		writeProvenanceDisagree(r.emit, provenanceDisagree{
			Verb:               "find-missing",
			Source:             nameHeldSourceFindMissing,
			InstanceID:         it.InstanceID,
			Socket:             socket,
			SessionName:        it.TmuxSessionName,
			SessionID:          from.Session.ID,
			CurrentSessionName: from.Session.Name,
			Server:             from.Server,
			Verdict:            verdict,
			Action:             action,
			Caller:             r.caller.get(),
		}, reason)
	}
}

// FindMissing delegates to Client.Sweep, including its fixed all-agent
// checks and completeness contract. Its legacy counters reconcile live rows
// against their agents' processes and,
// where a process cannot be checked, against tmux. Each live row's liveness
// comes only from its agent process, checked by its recorded start time: the
// SessionStart identity, else the recorded pane process, and the pane
// process when the two disagree. No process environment is read, and no
// child process or other process carrying the row's id keeps the row alive.
// A process alive with its recorded start time leaves the row live and
// clears any liveness note; a dead one marks the row missing whatever tmux
// shows, with no tmux call, except that a Codex row holding an end report (its
// agent's SessionEnd) whose process is gone is moved to ended instead and is
// not in ids or count; a Codex row without an end report, or judged on tmux
// evidence alone, is marked missing as any row. A `pending` row whose recorded (or just adopted)
// pane process is alive instead stays pending with liveness note unreported:
// the agent is alive, but no hook has reported since its launch. It may sit
// at a Claude Code startup screen or idle at its prompt, for example after
// its SessionStart hook died. The caller looks at the pane (ReadPane reads a
// row in any state) and, only having looked, types (SendKeys with
// AllowPending); the agent's next hook clears the note. A mark denies the row's open
// permission requests in the same transaction, so a row marked missing never
// keeps an open request.
//
// A row whose process cannot be checked (unreadable, a pid-only identity
// that reads alive, or none recorded; a pid-only identity that reads gone is
// marked) and whose recorded tmux session name cannot be used (it is empty,
// contains a control character, or contains a character tmux stores
// differently) is left live and reported unverified with a liveness note of
// its own (tmux_session_name_empty, tmux_session_name_control_char or
// tmux_session_name_rewritten, the first fault in that order), with no tmux
// call; removing such a row is a human's decision (see "Operator actions" in
// agent-director's docs/operator-actions.md).
//
// Every other row whose process cannot be checked is looked up in tmux: one
// lookup of its recorded socket, else the caller's, shared by every row of
// that socket; a row with an unusable name takes none of it. Its own
// labelled session leaves it live and reported unverified with a liveness
// note (a row whose create reply was lost first adopts the one pane carrying
// its launch token and is judged by that pane's process, and is marked when
// no pane carries it); a session left from an earlier life, or no session of
// its current launch, marks it missing whatever holds its name, and a
// session holding its recorded name is named in the trail and never touched
// (see "Operator actions" in agent-director's docs/operator-actions.md); an answer that
// cannot be used, or no answer, leaves it unverified with a liveness note.
// After an unreadable or unavailable answer on a socket, no more tmux calls
// are made on that socket, and the run's tmux time (sweep_budget_seconds
// from the loaded configuration, 15 s by default) caps all its calls; rows
// not reached are left unverified. tmux problems never fail the sweep. A
// Cursor row with a recorded gateway (pid and start time) is judged by that
// gateway alone: one that cannot be checked is left unverified (probe_eacces)
// with no tmux lookup, and only an exact gone verdict marks the row missing.
//
// missing is a check's judgement on available evidence, not
// proof that the agent has exited; neither ended nor missing means that the
// agent is dead or that its row is safe to delete (SR-18.2). The caller must
// run as the same user and in the same tmux environment as the agents (the
// caller's environment decides the socket of a row that records none): a
// run as another user, as root or against another tmux server can mark live
// rows missing (SR-18.7). Two consequences: Kill's success on a finished row
// is not verification that the agent exited; and on the wrong tmux server, a
// row wrongly marked missing, Kill's no-op success and a reuse together start
// a second agent for the same id.
//
// Every write applies only while the row still holds the snapshot the sweep
// read, or the one its adoption produced. A `pending` row (a spawn's, a
// reuse's or a resume's launch) inside the pending grace period
// (pending_grace_seconds from the loaded configuration, 60 s by default; an
// unset grace is larger when a raised create_timeout_ms or pipe_close_wait_ms
// puts its derived minimum above 60 s, see config.Tmux.EffectivePendingGrace),
// measured from its launch start, is not judged: it is left as it is and is
// in neither result list (SR-11.2). Nor is a `pending` row past it whose
// launch is still in progress: the spawn, reuse or resume that began it
// records itself on the row and ends that hold when its launch ends, and the
// row is not judged while that process is alive in this caller's pid
// namespace. Neither protects a Cursor row whose exact recorded gateway is
// gone: it is marked missing. A row noted unreported is in neither result list. Ages and tmux time are
// read from the Client's clock. Recovery runs only when called; status/get/list
// share the same checks for their fixed selected rows. No schedule is added.
//
// The same pass repairs stale check_permission rows (b.146 rule 9): a row with relay_mode on in check_permission none
// of whose permission requests still awaits an answer and none of whose relay
// hooks may still run (a request find-missing's mark closed is not judged) is
// moved to waiting when the agent's idle-prompt Notification was the last
// hook it recorded, and to working otherwise (a Codex row always to working). Repaired rows are in neither
// result list.
//
// CLI: agent-director find-missing
//
// Errors:
//   - ErrProbeUnsupported: never returned; catalogued for client mapping:
//     the sweep reads only process start times, and a start time it
//     cannot read leaves that row unverified.
//   - [ErrStoreBusy]: as for [Client.Sweep], its selection read did not
//     finish within its bound. Nothing was checked.
//
// Nondeterminism: none.
func (c *Client) FindMissing(ctx context.Context) (FindMissingResult, error) {
	return c.Sweep(ctx)
}
