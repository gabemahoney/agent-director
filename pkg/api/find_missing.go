package api

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// FindMissingStore is the narrow store surface find-missing needs (SRD
// Appendix F.4): the live-row read, the life-guarded mark, note write and
// clear (SR-11.6), the life-guarded adoption write of a lost create reply's
// identity (SR-3.6; LFR H2), the permission-request denial, the
// provisional-transcript healing, and this store's id, which every label the
// sweep's lookup accepts ends with. *store.Store satisfies it.
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
	// AdoptIdentityIfSameLife records the server and pane identity the
	// sweep's adoption found for a live row whose lookup is Ours (SR-3.6),
	// in one write guarded on a live state and examined, the snapshot the
	// sweep read; it advances row_version and, when it applied, returns the
	// row's new snapshot, which guards the sweep's later write for the row
	// (SR-11.6; LFR H2). Otherwise it returns CondChanged or CondAbsent with
	// a zero snapshot, and a store error as an error with a zero CondResult
	// (SR-5.8).
	AdoptIdentityIfSameLife(instanceID string, examined RowSnapshot, id LaunchIdentity) (res CondResult, now RowSnapshot, err error)
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
// JSON envelope is deterministic across runs).
type FindMissingResult struct {
	// Count is the number of rows this sweep marked missing: the length of
	// IDs.
	Count int `json:"count"`
	// IDs is the sorted slice of instance ids this sweep marked missing, on
	// process or tmux evidence: rows whose agent process (the SessionStart
	// one or the recorded pane's) is gone, and rows whose process could not
	// be checked and whose lookup found no session or pane of their current
	// launch; a pending row only past the pending grace period. A row whose
	// guarded mark found it changed or absent, or failed in the store, is not
	// listed. missing is the sweep's judgement on the evidence available to
	// it, not proof that the agent has exited. Always non-nil — encodes as []
	// when no rows were marked.
	IDs []string `json:"ids"`
	// Unverified is the number of live rows this sweep left live with a
	// liveness note: their agent process could not be checked (the
	// start-time reader could not tell, a pid-only identity read alive, or no
	// process identity is recorded) and either their recorded session name
	// cannot be used, so no tmux call was made for them (a note of its own,
	// SR-3.2), or the tmux lookup of the row's socket did not mark them (Ours,
	// Can't tell, or not called) (SR-11.1, SR-11.3, SR-11.4). It counts rows
	// whose note was already current. A row whose note write found it
	// changed, or failed in the store, is not counted.
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
const (
	findMissingActionMarked     = nameHeldRowMarkedMissing
	findMissingActionLeftLive   = "left_live"
	findMissingActionUnverified = "left_unverified"
	findMissingActionChanged    = "left_changed"
	findMissingActionStoreError = "store_error"
)

// findMissingRow is the outcome of judging one live row.
type findMissingRow struct {
	// write is the row's writer outcome (markMissingSameLife,
	// writeLivenessNote or clearLivenessNote), or, for a row whose adoption
	// write did not apply, that write's outcome (never Listed). For a mark,
	// write.Listed puts the row in ids; for a note, in unverified_ids.
	write findMissingWrite
	// marked reports that write came from the mark rather than a note write
	// or clear.
	marked bool
	// action is the ad.provenance.disagree action for a write that applied
	// or was not needed (findMissingActionMarked, findMissingActionLeftLive,
	// findMissingActionUnverified); findMissingAction turns a refused or
	// failed write into left_changed or store_error.
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

// FindMissing is the find-missing verb (SR-11, SR-16.2 item 5; Appendix
// F.4; LFR G5); (c *Client).FindMissing calls it with the Client's store,
// tmux client, start-time reader, effective settings, clock and logger.
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
//  1. List the live rows (every live state, pending included). A list error
//     fails the sweep. Rows are judged in instance-id order, so the per-socket
//     stop and the budget's cut-off fall on the same rows on every run.
//  2. A `pending` row inside its grace period, measured from its launch
//     start (store.InsidePendingGrace), is left untouched: no reader call, no
//     tmux call, no write, no event, and it is in neither result list. Such a
//     row may be a spawn's, a reuse's or a resume's launch. A `pending` row
//     with no readable launch start is past the grace period and judged at
//     once (SR-22.8).
//  3. Every other row is judged by its agent process (judgeLiveRow): alive
//     leaves it live and clears any note; dead marks it missing with reason
//     proc_absent. Neither makes a tmux call. A child process or another
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
//     never fail the sweep.
//  6. Every write is guarded on the row snapshot the sweep read, or on the
//     one its adoption write produced (SR-11.6): a write that finds the row
//     changed or absent, or fails in the store, leaves the row in neither
//     list with no tick. The start-time reader's unreadable answer and every
//     per-row store error are row outcomes, never a sweep error (SR-11.7,
//     SR-5.8). A row's ad.provenance.disagree records are written once its
//     write settled.
//  7. Provisional transcripts are healed (healProvisionalTranscripts).
//
// The result lists are sorted. The only error returned is the live-row
// read's.
//
// A row marked missing is the sweep's judgement on the evidence available to
// it, not proof that the agent has exited; neither ended nor missing means
// that the agent is dead or that its row is safe to delete (SR-18.2). The
// caller must run as the agents' user, in their tmux environment: run as
// another user, as root or against another tmux server, the sweep can mark
// live rows missing (SR-18.7).
//
// Errors:
//   - ErrProbeUnsupported: listed for find-missing, which no longer returns
//     it: the sweep reads only process start times, and a start time it
//     cannot read leaves that row unverified.
func FindMissing(ctx context.Context, s FindMissingStore, t FindMissingTmux, pc ProcChecker, pendingGrace, sweepBudget time.Duration, now func() time.Time, lg FindMissingLogger) (FindMissingResult, error) {
	identities, err := s.ListLiveSpawnIdentities()
	if err != nil {
		return FindMissingResult{}, err
	}
	identities = slices.Clone(identities)
	sort.SliceStable(identities, func(i, j int) bool {
		return identities[i].ClaudeInstanceID < identities[j].ClaudeInstanceID
	})
	sweepNow := now()
	r := &findMissingRun{s: s, t: t, pc: pc, sw: tmux.NewSweep(t, pc, now, sweepBudget), lg: lg}

	missing := make([]string, 0)
	unverified := make([]string, 0)
	for _, it := range identities {
		if store.InsidePendingGrace(it.State, it.LaunchStartedAtMillis, pendingGrace, sweepNow) {
			// A booting launch inside its grace period (SR-11.2): not judged.
			continue
		}
		row := r.judgeLiveRow(it)
		switch {
		case !row.write.Listed:
		case row.marked:
			missing = append(missing, it.ClaudeInstanceID)
		default:
			unverified = append(unverified, it.ClaudeInstanceID)
		}
		r.emitDisagree(it, row)
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
	switch state := tmux.JudgeProcess(r.pc, agent.Identity); state {
	case tmux.ProcAlive:
		return r.clearRow(findMissingRow{}, it, it.Snapshot)
	case tmux.ProcGone:
		return r.markRow(findMissingRow{}, it, it.Snapshot, reasonProcAbsent, nil)
	default: // tmux.ProcUnknown, tmux.ProcNone
		if note := unusableNameNote(tmux.Unusable(it.TmuxSessionName)); note != "" {
			return r.noteRow(findMissingRow{}, it, it.Snapshot, note)
		}
		return r.lookupRow(it, state)
	}
}

// clearRow completes row for a row whose agent process was found alive: the
// guarded clear of a note it carries (clearLivenessNote), guarded on guard.
func (r *findMissingRun) clearRow(row findMissingRow, it LiveSpawnIdentity, guard RowSnapshot) findMissingRow {
	row.write = clearLivenessNote(r.s, it.ClaudeInstanceID, guard, it.LivenessNote, r.lg)
	row.action = findMissingActionLeftLive
	return row
}

// markRow completes row with the guarded mark (markMissingSameLife), guarded
// on guard, with tick reason reason and the tick's extra fields.
func (r *findMissingRun) markRow(row findMissingRow, it LiveSpawnIdentity, guard RowSnapshot, reason string, extra map[string]any) findMissingRow {
	row.write = markMissingSameLife(r.s, it.ClaudeInstanceID, guard, reason, extra, r.lg)
	row.marked = true
	row.action = findMissingActionMarked
	return row
}

// noteRow completes row with the note write (writeLivenessNote), guarded on
// guard: the row is left live and unverified with note.
func (r *findMissingRun) noteRow(row findMissingRow, it LiveSpawnIdentity, guard RowSnapshot, note string) findMissingRow {
	row.write = writeLivenessNote(r.s, it.ClaudeInstanceID, guard, it.LivenessNote, note, r.lg)
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
		emitProvenanceDisagree(provenanceDisagree{
			Verb:               "find-missing",
			Source:             findMissingTickSource,
			InstanceID:         it.ClaudeInstanceID,
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

// FindMissing reconciles live rows against their agents' processes and,
// where a process cannot be checked, against tmux. Each live row's liveness
// comes only from its agent process, checked by its recorded start time: the
// SessionStart identity, else the recorded pane process, and the pane
// process when the two disagree. No process environment is read, and no
// child process or other process carrying the row's id keeps the row alive.
// A process alive with its recorded start time leaves the row live and
// clears any liveness note; a dead one marks the row missing whatever tmux
// shows, with no tmux call.
//
// A row whose process cannot be checked (unreadable, a pid-only identity
// that reads alive, or none recorded; a pid-only identity that reads gone is
// marked) and whose recorded tmux session name cannot be used (it is empty,
// contains a control character, or contains a character tmux stores
// differently) is left live and reported unverified with a liveness note of
// its own (tmux_session_name_empty, tmux_session_name_control_char or
// tmux_session_name_rewritten, the first fault in that order), with no tmux
// call; removing such a row is a human's decision (see "Operator actions" in
// the agent-director README).
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
// (see "Operator actions" in the agent-director README); an answer that
// cannot be used, or no answer, leaves it unverified with a liveness note.
// After an unreadable or unavailable answer on a socket, no more tmux calls
// are made on that socket, and the run's tmux time (sweep_budget_seconds
// from the loaded configuration, 15 s by default) caps all its calls; rows
// not reached are left unverified. tmux problems never fail the sweep.
//
// missing is the sweep's judgement on the evidence available to it, not
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
// (pending_grace_seconds from the loaded configuration, 60 s by default),
// measured from its launch start, is not judged: it is left as it is and is
// in neither result list (SR-11.2). Ages and tmux time are read from the
// Client's clock. Intended for periodic cron use.
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
	return FindMissing(ctx, c.st, c.tmuxClient, c.procChecker, c.cfg.Tmux.EffectivePendingGrace(), c.cfg.Tmux.EffectiveSweepBudget(), c.now, c.logger)
}
