package api

import (
	"context"
	"io"
	"log"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/internal/trail"
)

// This file holds the reuse path: spawn with the reuse opt-in
// (SpawnParams.ReuseFinished) and a caller-supplied id whose row is finished
// (SR-10). The decision before the change is built here from Epic 16's
// pre-launch helpers (decidePreLaunch, the starting-session rule, the
// holder-class wording), never a copy; the descriptions only reuse returns
// are in spawn_reuse_errors.go. After the change, the create's outcome, the
// restore and the "duplicate session" path are the code resume shares
// (finishedLaunch, finished_launch.go), with reuse's values
// (reuseLaunchVerb).

// reuse's trail events (SR-10.6), both with source ad_spawn and fail-open.
const (
	// reuseReusedEvent: once per applied change (reuseChangeAndLaunch).
	reuseReusedEvent = "ad.spawn.reused"
	// reuseRestoredEvent: once per restore attempt after a failed launch
	// (finishedLaunch.restore).
	reuseRestoredEvent = "ad.spawn.reuse_restored"
)

// reuseStore is the store surface the reuse path uses (SR-10.2 to SR-10.5;
// Appendix F.4): the pre-check read, the change, the restore after a failed
// launch and the identity write after a labelled create. *store.Store
// satisfies it; a white-box test wraps it (runSpawnWithReuseStore) to inject
// failures and interleavings.
type reuseStore interface {
	// ReadForReuse is the one pre-check read: the row as examined (state,
	// ended_at, snapshot, launch identity and the raw pre-reuse life), found
	// false with no error when no row has the id.
	ReadForReuse(instanceID string) (row store.ReuseRow, found bool, err error)
	// ResetForReuse is the single change (SR-10.3), applied only while the row
	// is finished and holds examined.
	ResetForReuse(instanceID string, examined RowSnapshot, fresh Spawn) (res CondResult, archivedSessionID string, resetVersion int64, err error)
	// RestoreAfterFailedReuse writes prior back after a failed launch
	// (SR-10.4), applied only while the row is pending at resetVersion.
	RestoreAfterFailedReuse(instanceID string, resetVersion int64, prior store.RawLife, failedAt time.Time) (CondResult, error)
	// RecordLaunchIdentity is the identity write after a labelled create
	// (SR-3.6), conditional on the reset's version and token.
	RecordLaunchIdentity(instanceID string, launchVersion int64, token string, id LaunchIdentity) (CondResult, error)
}

// The production store satisfies reuseStore.
var _ reuseStore = (*store.Store)(nil)

// reusePreCheck is the collision pre-check's reader on the reuse path: it
// answers spawn.ApplyDefaults from the one ReadForReuse already made (the
// row's state, whether it was found, the read's error), so that read is the
// only pre-check read (SR-10.2) and the live-row collision and the read
// failure keep their single mappings in internal/spawn. It reads nothing.
type reusePreCheck struct {
	row   store.ReuseRow
	found bool
	err   error
}

// SpawnState returns the ReadForReuse answer as the collision pre-check's.
func (p reusePreCheck) SpawnState(string) (string, bool, error) {
	return p.row.State, p.found, p.err
}

// reuseDeps is what the reuse path uses besides the request and the row: the
// reuse store, tmux client, start-time reader, configuration, this store's id
// (Launch.StoreID, which every lookup and label carries), clock and logger,
// and the caller identity collected once at entry.
type reuseDeps struct {
	rs      reuseStore
	t       spawnTmux
	pc      ProcChecker
	cfg     config.Config
	storeID string
	now     func() time.Time
	lg      *log.Logger
	who     caller
}

// reuseExamined is what the reuse path keeps from its decision, so the change,
// the launch and the "duplicate session" path re-read nothing: the row as
// read (row: its Snapshot is the examined snapshot the change is conditional
// on, its Identity the examined launch identity a re-lookup classifies
// against, its Life the pre-reuse life the restore writes back); the
// pre-launch facts (pre: the instance id, the recorded name, the requested
// name as the holder name, the ended_at as read, nil when NULL or
// unparseable, the pid and session-id presence, the prepared launch socket and
// the agent process); the lookup's verdict token; and the
// ad.provenance.disagree reasons the decision already wrote, so a later lookup
// in the same call writes none of them twice.
type reuseExamined struct {
	row             store.ReuseRow
	pre             preLaunchRow
	verdict         string
	disagreeWritten []string
}

// spawnReuse is the reuse path for a finished row (SR-10.2 to SR-10.6): the
// decision before the change (reuseExamine), then the change and the launch.
// r is the resolved, validated and defaulted request (its TmuxSessionName is
// the requested name); row is the one pre-check read's answer.
func spawnReuse(d reuseDeps, r spawn.Resolved, row store.ReuseRow) (SpawnResult, error) {
	if d.lg == nil {
		d.lg = log.New(io.Discard, "", 0)
	}
	ex, err := reuseExamine(d, r, row)
	if err != nil {
		return SpawnResult{}, err
	}
	return reuseChangeAndLaunch(d, r, ex)
}

// reuseExamine is reuse's decision before anything changes (SR-10.2, SR-10.8,
// SR-4.2, SR-3.6, SR-22.6), for row, read by ReadForReuse as finished. In
// order, after the pre-check read and the live-row collision (runSpawn):
//
//  1. [Epic 19's unusable recorded-name guard (SR-3.2) goes here, on the
//     recorded name from row, with no second read.]
//  2. The launch socket (spawn.ResolveRowLaunchSocket): the row's recorded
//     socket, or the one a plain spawn resolves when it records none. A
//     refusal is ErrTmuxNotAvailable, with no tmux call and nothing written.
//  3. Exactly one tmux.Lookup on that socket for the row's examined launch
//     identity (rowLaunch: its instance id, launch token and recorded server
//     identity, this store's id), with the requested name as the holder name.
//     Any adoption the result offers is ignored (reuse writes no adoption,
//     SR-3.6), and no pane listing is made.
//  4. The decision (decidePreLaunch) on the row as examined: the recorded name
//     quoted at the old-row lookup (Can't tell, Ours, stopping, starting,
//     own id) and compared for name_changed; ended_at as read (nil when NULL
//     or unparseable, so the stopping window is skipped and the own-id text
//     says nothing about when the row ended); the pid and session-id presence
//     from the snapshot (neither skips the window); the configured bound and
//     window; the Client clock. Ours, or Gone while the agent process runs,
//     go through the starting-session rule; Leftover is "left over from an
//     earlier life"; Can't tell its error. Another store's label is never
//     Ours or Leftover. Gone otherwise is the new-name pre-check (SR-10.8) on
//     the same listing, quoting the requested name: no holder proceeds; an
//     old, foreign, other-store or no valid label is its conflict; more than
//     one matching entry is ErrTmuxUnresponsive. No second tmux call.
//  5. The decision's ad.provenance.disagree records, one per distinct reason
//     (never adopted), right after the decision and before any trust or store
//     write, on a refusal as on proceed (verb spawn, source ad_spawn, the
//     recorded name, the socket, the session concerned, the server value, the
//     verdict, action refused or proceeded, the caller identity). Fail-open.
//  6. A Leftover refusal alone re-reads the row once (reuseLostRace): when it
//     was removed or no longer holds the examined snapshot (a competing
//     reuse's reset or resume's move came first), the lost-race
//     ErrInstanceIdCollision is returned instead (SR-10.5).
//
// A refusal changes nothing: no archive, reset, permission-request deletion,
// trust entry or ad.spawn.* event, and no blocking session is touched. It
// never returns tmux.ErrTmuxSessionCreate. On proceed it returns what the
// change and the launch need (reuseExamined).
func reuseExamine(d reuseDeps, r spawn.Resolved, row store.ReuseRow) (reuseExamined, error) {
	id := r.ClaudeInstanceID
	recorded := row.Snapshot.TmuxSessionName

	// Epic 19: the unusable recorded-name guard (SR-3.2) on recorded goes
	// here, between the live-row collision and the socket.

	socket, err := spawn.ResolveRowLaunchSocket(row.Identity.Socket)
	if err != nil {
		return reuseExamined{}, err
	}

	lookup := tmux.Lookup(d.t, d.pc, rowLaunch(id, row.Identity, d.storeID, socket), r.TmuxSessionName)
	examined := reuseSpawnOf(id, row)
	pre := preLaunchRowOf(examined, recorded, socket)
	pre.HolderName = r.TmuxSessionName
	decision := decidePreLaunch(lookup, pre, d.pc, startingSessionLimitsOf(d.cfg.Tmux), d.now)
	emitFinishedRowDisagree(reuseLaunchVerb, examined, socket, d.who, preLaunchActionOf(decision.Err), decision)
	if decision.Err != nil {
		if lookup.Verdict == tmux.Leftover {
			if lost := reuseLostRace(d.rs, id, row.Snapshot); lost != nil {
				return reuseExamined{}, lost
			}
		}
		return reuseExamined{}, decision.Err
	}
	return reuseExamined{row: row, pre: pre, verdict: decision.Verdict, disagreeWritten: decision.Reasons}, nil
}

// reuseSpawnOf returns row, as ReadForReuse read it for instanceID, as the
// Spawn the pre-launch helpers take (preLaunchRowOf, agentProcess): the
// instance id, state, recorded name, session id, pid and its start time from
// the snapshot, ended_at as parsed and as stored, the snapshot and the launch
// identity. Every other field is zero: labels, Claude args and extra env are
// never decoded (SR-10.3).
func reuseSpawnOf(instanceID string, row store.ReuseRow) Spawn {
	return Spawn{
		ClaudeInstanceID: instanceID,
		State:            row.State,
		TmuxSessionName:  row.Snapshot.TmuxSessionName,
		ClaudeSessionID:  row.Snapshot.ClaudeSessionID,
		PID:              row.Snapshot.PID,
		ProcStarttime:    row.Snapshot.ProcStarttime,
		EndedAt:          row.EndedAt,
		EndedAtText:      row.EndedAtText,
		Snapshot:         row.Snapshot,
		Identity:         row.Identity,
	}
}

// reuseLostRace is the one re-read after the old-row lookup refused a
// Leftover (SR-10.5): one ReadForReuse and no write, decided by
// leftoverLostRace, which resume shares (resumeLostRace). The row removed, or
// no longer holding examined, gives the lost-race ErrInstanceIdCollision
// (reuseLostRaceError); an unchanged snapshot, or a re-read that fails,
// gives nil, and the Leftover refusal stands.
func reuseLostRace(rs reuseStore, instanceID string, examined RowSnapshot) error {
	again, found, err := rs.ReadForReuse(instanceID)
	if leftoverLostRace(examined, again.Snapshot, found, err) != 0 {
		return reuseLostRaceError(instanceID)
	}
	return nil
}

// reuseChangeAndLaunch is the change and the launch after the decision
// passed (SR-22.6, SR-10.3, SR-10.4, SR-10.6, SR-13.2, SR-13.4). r is the
// resolved request (its TmuxSessionName the requested name, its NoPreTrust
// the call's own choice); ex what reuseExamine kept. The whole order of the
// reuse path, with this function's part from step 3:
//
//  1. The decision (reuseExamine): the socket, the one old-row lookup with the
//     new-name pre-check on the same listing, proceed or the refusal.
//  2. Its ad.provenance.disagree records, on a refusal as on proceed.
//  3. A new launch token (spawn.NewLaunchToken); a failure is ErrInternal,
//     with nothing written.
//  4. The composition (spawn.ComposeLaunch): the create request (name, cwd,
//     environment, settings and argv) and the request fields the reset
//     writes, with the parent id from the caller's AGENT_DIRECTOR_INSTANCE_ID.
//     A failure writes nothing.
//  5. Pre-trust (spawn.PreTrust) for the new request's cwd and extra env, by
//     the call's own NoPreTrust, after every check that refuses without a
//     write and before the reset. Best effort: it never refuses, and its
//     outcome is the result's pre_trust.
//  6. One reading of the Client clock: the fresh row's started_at and
//     last_seen_at, and its launch start in milliseconds.
//  7. The reset (ResetForReuse), conditional on the row still being finished
//     with the examined snapshot: the fresh row is the composition's request
//     fields with that reading, the new token, the prepared socket and the
//     call's NoPreTrust. Changed or removed gives the lost-race
//     ErrInstanceIdCollision (reuseLostRaceError); a store error gives the
//     archive or reuse-change ErrInternal (reuseChangeError). Nothing changed
//     and nothing is launched in any of these.
//  8. The create (spawn.CreateAndLabel), the next step after the reset's
//     commit, with only in-process work between them: the requested name on
//     the prepared socket, the composed environment and command, the new
//     token, the instance id and this store's id, so the label is five
//     fields ending with it (SR-3.5). Once it returns, ad.spawn.reused is
//     emitted (claude_instance_id, prior_state as examined,
//     archived_session_id null when nothing was archived, lookup_outcome the
//     decision's verdict token, source ad_spawn), fail-open, before any
//     restore.
//  9. The outcome, through the code resume shares (finishedLaunch.outcome,
//     reuseDeps.launchOnto): a labelled session gets the identity write with
//     the reset's version and the new token (a failed write logs one WARN
//     line and does not fail the call) and a lost reply none, both returning
//     the id and pre_trust; a timeout, or a non-zero-exit unparseable reply,
//     is ErrTmuxUnresponsive saying the row was reset and stays pending, with
//     no restore and no identity write; "duplicate session" makes one
//     re-lookup of the requested name against the examined launch identity,
//     restores the row and returns the holder's classified error, then
//     writes the re-lookup's disagree reasons not already written and one
//     ad.launch.name_held (launch reuse); every other failure (tmux
//     unavailable: ErrTmuxNotAvailable; a session that could not be
//     labelled, any other launch failure: ErrTmuxSessionCreate) restores the
//     row and returns its launch error. The restore (RestoreAfterFailedReuse
//     with the reset's version, the examined raw life and a failure time from
//     the Client clock) is attempted exactly once on those paths; its result
//     (restored, left as it is because the row changed or was removed after
//     this spawn reset it, or, on a store error, still pending with one WARN
//     line) is the launch error's row sentence, and ad.spawn.reuse_restored
//     records it. The launch error is returned whatever the restore did.
//
// Success does not say whether a reset happened (SR-10.7).
func reuseChangeAndLaunch(d reuseDeps, r spawn.Resolved, ex reuseExamined) (SpawnResult, error) {
	id := r.ClaudeInstanceID
	socket := ex.pre.Socket

	token, err := spawn.NewLaunchToken()
	if err != nil {
		return SpawnResult{}, err
	}
	c, err := spawn.ComposeLaunch(r, d.cfg)
	if err != nil {
		return SpawnResult{}, err
	}

	// A failure never fails the spawn; the outcome goes into the result.
	preTrust := spawn.PreTrust(r.CWD, r.ExtraEnv, r.NoPreTrust)

	now := d.now()
	fresh := c.Row
	fresh.StartedAt = now
	fresh.LaunchStartedAtMillis = now.UnixMilli()
	fresh.Identity = LaunchIdentity{Token: token, Socket: socket}
	res, archived, resetVersion, err := d.rs.ResetForReuse(id, ex.row.Snapshot, fresh)
	if err != nil {
		return SpawnResult{}, reuseChangeError(id, err)
	}
	if res != CondApplied {
		return SpawnResult{}, reuseLostRaceError(id)
	}

	req := c.Create
	req.Socket = socket
	req.Token = token
	req.StoreID = d.storeID
	out := spawn.CreateAndLabel(d.t, req)
	emitReused(id, ex, archived)
	if err := d.launchOnto(ex, resetVersion).outcome(out, req); err != nil {
		return SpawnResult{}, err
	}
	return SpawnResult{ClaudeInstanceID: id, PreTrust: string(preTrust)}, nil
}

// emitReused writes the one ad.spawn.reused record of an applied change
// (SR-10.6), fail-open: claude_instance_id; prior_state, the state examined
// before the reset; archived_session_id, the session id the reset archived,
// null when it archived none (""); lookup_outcome, the old-row lookup's
// verdict token as the decision kept it (never a literal); source ad_spawn.
func emitReused(instanceID string, ex reuseExamined, archivedSessionID string) {
	var archived any
	if archivedSessionID != "" {
		archived = archivedSessionID
	}
	_ = trail.Emit(context.Background(), reuseReusedEvent, map[string]any{
		"claude_instance_id":  instanceID,
		"prior_state":         ex.row.State,
		"archived_session_id": archived,
		"lookup_outcome":      ex.verdict,
		"source":              nameHeldSourceSpawn,
	})
}

// launchOnto returns reuse's launch after its reset applied (finishedLaunch,
// shared with resume): the row as examined (reuseSpawnOf over ex.row: its
// instance id, recorded name, state, ended_at as parsed, pid and session-id
// presence, pre-reset launch token and server identity), the disagree
// reasons the decision already wrote, and resetVersion, the version the reset
// produced. The identity write is the reuse store's; the restore is
// RestoreAfterFailedReuse with resetVersion, ex.row.Life unchanged and a
// failure time read from the Client clock when the restore is made.
func (d reuseDeps) launchOnto(ex reuseExamined, resetVersion int64) finishedLaunch {
	row := reuseSpawnOf(ex.pre.InstanceID, ex.row)
	return finishedLaunch{
		v:        reuseLaunchVerb,
		t:        d.t,
		pc:       d.pc,
		cfg:      d.cfg,
		storeID:  d.storeID,
		now:      d.now,
		lg:       d.lg,
		who:      d.who,
		identity: d.rs,
		restoreWrite: func() (CondResult, error) {
			return d.rs.RestoreAfterFailedReuse(row.ClaudeInstanceID, resetVersion, ex.row.Life, d.now())
		},
		row:             row,
		disagreeWritten: ex.disagreeWritten,
		version:         resetVersion,
	}
}
