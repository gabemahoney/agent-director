package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/gabemahoney/agent-director/internal/trail"
)

const StuckPermissionUnanswered = "permission_unanswered"

const (
	FallbackProcess   = "process"
	FallbackDeadline  = "deadline"
	FallbackAdmission = "admission"
)

// PermissionSample includes the exact request facts judged before persistence.
// The locked observer compares all of them, including answer and proof fields.
type PermissionSample struct {
	Request    PermissionRow
	FallbackAt time.Time
	Basis      string
}

// FallbackObservation is what one read established about a fallen-back request:
// when delivery must be confirmed by, and the verdict of its relay hook.
type FallbackObservation struct {
	RequestID   int64
	ConfirmBy   time.Time
	ProcessGone bool
	// CantTell is set when the hook could not be checked (hookCantTell).
	CantTell bool
}

// FallbackClock is the one rule for the start of a fallen-back request's
// fallback clock (SR-2.4), shared by every reader and by the write that
// persists it, so a persisted fallback_at is never one a read rejects. agent
// and relayMode are the request's Spawn's. A clock already stored is kept. The
// zero time and "" mean no clock: the request is not fallen back by a basis
// the rule knows, or its time is untrustworthy (non-positive, beyond year
// 9999, or recorded before schema v7 with an unparseable created_at).
func FallbackClock(agent, relayMode string, pr PermissionRow, o FallbackObservation, now time.Time) (time.Time, string) {
	var start time.Time
	var basis string
	switch {
	case !pr.FallbackAt.IsZero():
		start, basis = pr.FallbackAt, pr.FallbackBasis
	case agent == AgentCursor && relayMode == "off":
		// Historical admission has second precision but is still an
		// established admission fact, independent of read time.
		start, basis = pr.AdmittedAt, FallbackAdmission
		if start.IsZero() {
			start = pr.CreatedAt
		}
	case agent == AgentCursor || o.CantTell || pr.PreV7():
		start, basis = o.ConfirmBy, FallbackDeadline
	case o.ProcessGone:
		start, basis = pr.HookGoneAt, FallbackProcess
		if start.IsZero() {
			start = now
		}
	}
	if (pr.PreV7() && pr.CreatedAt.IsZero()) || (!start.IsZero() && (start.UnixMilli() <= 0 || start.Year() > 9999)) {
		return time.Time{}, ""
	}
	return start, basis
}

// RecordPermissionFallbacks preserves the first established fallback basis as
// well as the existing public hook_gone_at observation. In particular a later
// process observation cannot replace an earlier deadline-based clock. The clock
// is FallbackClock's; a request it gives none keeps only hook_gone_at.
func (s *Store) RecordPermissionFallbacks(at time.Time, maxWait time.Duration, observations []FallbackObservation) (map[int64]time.Time, error) {
	stored := make(map[int64]time.Time)
	err := s.inWriteTx(maxWait, func(ctx context.Context, conn *sql.Conn) error {
		for _, observation := range observations {
			pr, err := scanPermissionRow(conn.QueryRowContext(ctx, `SELECT `+permissionColumns+` FROM permission_requests pr WHERE request_id=?`, observation.RequestID))
			if err == sql.ErrNoRows {
				continue
			}
			if err != nil {
				return err
			}
			var agent, relay string
			if err := conn.QueryRowContext(ctx, `SELECT agent,relay_mode FROM spawns WHERE instance_id=?`, pr.InstanceID).Scan(&agent, &relay); err != nil {
				return err
			}
			start, basis := FallbackClock(agent, relay, pr, observation, at)
			var basisArg any
			if basis != "" {
				basisArg = basis
			}
			var gone sql.NullInt64
			if err := conn.QueryRowContext(ctx, `UPDATE permission_requests SET
 hook_gone_at=COALESCE(hook_gone_at,?),
 fallback_at=COALESCE(fallback_at,?),
 fallback_basis=CASE WHEN fallback_at IS NULL THEN ? ELSE fallback_basis END
 WHERE request_id=? RETURNING hook_gone_at`, millisArg(at), millisArg(start), basisArg, pr.RequestID).Scan(&gone); err != nil {
				return err
			}
			stored[pr.RequestID] = millisTime(gone)
		}
		return nil
	})
	return stored, err
}

// StuckDisplay is committed report-only detail. SinceMillis denotes the read
// that actually entered the displayed state, never the eligibility boundary.
type StuckDisplay struct {
	State        string `json:"state"`
	Reason       string `json:"reason,omitempty"`
	SinceMillis  int64  `json:"since_ms,omitempty"`
	RequestToken string `json:"request_token,omitempty"`
	ToolName     string `json:"tool_name,omitempty"`
}

type stuckObservation struct {
	StuckDisplay
	Candidate      string `json:"candidate"`
	CandidateSince int64  `json:"candidate_since_ms"`
	ObservedAt     int64  `json:"observed_at_ms"`
	// PermissionEpoch is the raw-state epoch of the interval this observation
	// belongs to. A stuck observation of another epoch is a finished episode.
	PermissionEpoch int64 `json:"permission_epoch"`
}

func decodeStuckObservation(raw string) (stuckObservation, bool) {
	var o stuckObservation
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if len(raw) > 16384 || d.Decode(&o) != nil || d.Decode(new(any)) != io.EOF ||
		!validDisplayState(o.State) || (o.Candidate != "" && !validDisplayState(o.Candidate)) ||
		(o.Candidate == "" && o.State != StateStuck) || o.CandidateSince < 0 || o.ObservedAt < 0 || o.PermissionEpoch < 0 ||
		(o.State == StateStuck && (o.Reason != StuckPermissionUnanswered || o.SinceMillis <= 0 || o.RequestToken == "")) {
		return stuckObservation{}, false
	}
	return o, true
}

func validDisplayState(state string) bool {
	return IsLiveState(state) || isTerminalState(state) || state == StateStuck
}

// CommittedStuckDisplay returns the history from the same row snapshot. A
// failed observation write must leave this display and detail together.
func (sp Spawn) CommittedStuckDisplay() StuckDisplay {
	o, ok := decodeStuckObservation(sp.StuckObservation)
	if ok && o.State == StateStuck && !isTerminalState(sp.State) && sp.State != StatePending {
		return o.StuckDisplay
	}
	return StuckDisplay{State: sp.ReportedState}
}

var errStuckSampleChanged = errors.New("store: stuck sample changed")

// permissionCandidate reports whether pr, judged as sample at now, belongs to
// row's current permission interval with a fallback clock already started.
// The unlocked pre-check and the locked re-check share it so they cannot drift.
func permissionCandidate(row Spawn, pr PermissionRow, sample PermissionSample, now time.Time) bool {
	return row.State == StateCheckPermission && (row.Agent == AgentClaude || row.Agent == AgentCodex || row.Agent == AgentCursor) &&
		pr.OwnerLaunchToken == row.Identity.Token && pr.OwnerPermissionEpoch == row.PermissionEpoch &&
		pr.AwaitsAnswer() && !pr.ProvenGone() &&
		!sample.FallbackAt.IsZero() && !sample.FallbackAt.After(now)
}

// observationPlan is the outcome of observing samples on a row: the fallback
// clocks to establish, the observation to store, the display and trail events
// it yields, and whether anything need be written at all.
type observationPlan struct {
	clocks []PermissionSample
	// raw is the observation to store; nil clears it. text is raw as stored
	// ("" for NULL), to recognise a commit that landed.
	raw    any
	text   string
	result StuckDisplay
	events []stuckTransition
	write  bool
}

// planObservation decides, from the row as examined and the judged samples,
// everything ObservePermissionStuck would commit. It is pure: the unlocked
// pre-check and the locked commit apply one plan, so they cannot drift. The
// locked commit proves the plan's inputs unchanged before applying it.
func planObservation(row Spawn, samples []PermissionSample, now time.Time, timeout time.Duration, settle SettleRead) (observationPlan, error) {
	var p observationPlan
	var representative *PermissionRow
	var earliest time.Time
	for _, sample := range samples {
		pr := sample.Request
		if !permissionCandidate(row, pr, sample, now) {
			continue
		}
		start := sample.FallbackAt
		// Clock and display are one sample: failed persistence publishes
		// neither a newly observed process death nor its derived eligibility.
		if pr.FallbackAt.IsZero() {
			p.clocks = append(p.clocks, sample)
		}
		if now.Sub(start) < timeout {
			continue
		}
		if representative == nil || start.Before(earliest) || (start.Equal(earliest) && pr.RequestToken < representative.RequestToken) {
			selected := pr
			representative, earliest = &selected, start
		}
	}
	stored, valid := decodeStuckObservation(row.StuckObservation)
	if valid && now.UnixMilli() < stored.ObservedAt {
		return p, errStuckSampleChanged
	}
	target := row.State
	if representative != nil {
		target = StateStuck
	}
	old := stored
	continuing := valid && stored.State == StateStuck
	if !valid {
		old = stuckObservation{}
	}
	if !continuing {
		// Not displayed stuck: the display is the row's own settled state, never
		// a stale copy of it, so readers agree and an entry's prior is current.
		old.State = row.ReportedState
	}
	if continuing && target == StateStuck && old.PermissionEpoch != row.PermissionEpoch {
		// A new permission interval never inherits the previous episode: that
		// one ends with its own exit and the new one starts as a fresh candidate.
		p.events = append(p.events, stuckTransition{InstanceID: row.InstanceID, Prior: StateStuck, Next: row.ReportedState,
			Reason: old.Reason, AtMillis: now.UnixMilli()})
		old, continuing = stuckObservation{StuckDisplay: StuckDisplay{State: row.ReportedState}}, false
	}
	if old.State != StateStuck && target != StateStuck {
		// Ordinary state settling continues to be derived by settle.go.
		p.result = StuckDisplay{State: row.ReportedState}
		p.write = row.StuckObservation != "" || len(p.clocks) > 0
		return p, nil
	}
	if !continuing {
		old.PermissionEpoch = row.PermissionEpoch
	}
	if old.Candidate != target {
		old.Candidate, old.CandidateSince = target, now.UnixMilli()
	}
	prior := old.State
	if now.UnixMilli()-old.CandidateSince >= max(settle.SettleMs, 0) && old.State != target {
		old.State = target
		if target == StateStuck {
			old.SinceMillis = now.UnixMilli()
			old.Reason = StuckPermissionUnanswered
		}
		p.events = append(p.events, stuckTransition{InstanceID: row.InstanceID, Prior: prior, Next: target, Reason: old.Reason, AtMillis: now.UnixMilli()})
	}
	if old.State == StateStuck && representative != nil {
		old.RequestToken, old.ToolName = representative.RequestToken, representative.ToolName
	}
	// A read that would change nothing writes nothing. The settle deadline
	// counts from the stored candidate_since, which such a read keeps, so
	// skipping the refresh never delays a due transition.
	unchanged := old
	if valid {
		unchanged.ObservedAt = stored.ObservedAt
	}
	if valid && unchanged == stored && len(p.clocks) == 0 && len(p.events) == 0 {
		p.result = old.StuckDisplay
		return p, nil
	}
	old.ObservedAt = now.UnixMilli()
	p.write = true
	p.result = old.StuckDisplay
	if old.State == StateStuck || target == StateStuck {
		encoded, err := json.Marshal(old)
		if err != nil {
			return p, err
		}
		p.text = string(encoded)
		p.raw = p.text
	} else {
		p.result = StuckDisplay{State: target}
	}
	return p, nil
}

// ObservePermissionStuck commits one nonwaiting observation. The write lock
// protects request facts as well as row ownership; row_version alone does not
// detect a concurrent pane answer, ack or proof.
func (s *Store) ObservePermissionStuck(examined Spawn, samples []PermissionSample, now time.Time, timeout time.Duration, settle SettleRead) (StuckDisplay, error) {
	previous := examined.CommittedStuckDisplay()
	plan, err := planObservation(examined, samples, now, timeout, settle)
	if err != nil {
		return previous, err
	}
	if !plan.write {
		return previous, nil
	}
	err = s.inWriteTx(0, func(ctx context.Context, conn *sql.Conn) error {
		row, err := scanSpawnStatus(conn.QueryRowContext(ctx, `SELECT `+statusRowColumns+` FROM spawns WHERE instance_id=?`, examined.InstanceID), false)
		if err != nil {
			return err
		}
		if row.Snapshot != examined.Snapshot || row.Identity != examined.Identity || row.Agent != examined.Agent || row.PermissionEpoch != examined.PermissionEpoch || row.StuckVersion != examined.StuckVersion || row.State != examined.State {
			return errStuckSampleChanged
		}
		current, err := queryPermissionRows(func(q string, args ...any) (*sql.Rows, error) { return conn.QueryContext(ctx, q, args...) },
			`SELECT `+permissionColumns+` FROM permission_requests pr WHERE instance_id=? ORDER BY request_id`, "store: stuck requests", row.InstanceID)
		if err != nil {
			return err
		}
		if len(current) != len(samples) {
			return errStuckSampleChanged
		}
		byID := make(map[int64]PermissionRow, len(samples))
		for _, sample := range samples {
			byID[sample.Request.RequestID] = sample.Request
		}
		for _, pr := range current {
			if sampled, ok := byID[pr.RequestID]; !ok || pr != sampled {
				return errStuckSampleChanged
			}
		}
		for _, sample := range plan.clocks {
			if _, err := conn.ExecContext(ctx, `UPDATE permission_requests SET fallback_at=?,fallback_basis=?,
 hook_gone_at=COALESCE(hook_gone_at,?) WHERE request_id=?`, millisArg(sample.FallbackAt), sample.Basis, millisArg(now), sample.Request.RequestID); err != nil {
				return err
			}
		}
		_, err = conn.ExecContext(ctx, `UPDATE spawns SET stuck_observation=?,stuck_version=stuck_version+1 WHERE instance_id=?`, plan.raw, row.InstanceID)
		return err
	})
	if err != nil {
		if !strings.Contains(err.Error(), "commit outcome unconfirmed") || !s.observationLanded(examined, plan) {
			return previous, err
		}
	}
	emitStuckTransitions(plan.events)
	return plan.result, nil
}

// observationLanded is the one nonwaiting re-read after an unconfirmed COMMIT:
// whether this plan's write is what the row now holds. Any doubt, a busy store
// included, answers false, and the caller keeps the previous view.
func (s *Store) observationLanded(examined Spawn, plan observationPlan) bool {
	var row Spawn
	err := s.readWithin(0, "", func(ctx context.Context, conn *sql.Conn) error {
		var err error
		row, err = scanSpawnStatus(conn.QueryRowContext(ctx, `SELECT `+statusRowColumns+` FROM spawns WHERE instance_id=?`, examined.InstanceID), false)
		return err
	})
	return err == nil && row.Snapshot == examined.Snapshot && row.PermissionEpoch == examined.PermissionEpoch &&
		row.StuckVersion == examined.StuckVersion+1 && row.StuckObservation == plan.text
}

type stuckTransition struct {
	InstanceID string `json:"-"`
	Prior      string `json:"-"`
	Next       string `json:"new_state"`
	Reason     string `json:"reason"`
	AtMillis   int64  `json:"at_ms"`
}

func decodeStuckExit(raw string) (stuckTransition, bool) {
	var e stuckTransition
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if len(raw) > 1024 || d.Decode(&e) != nil || d.Decode(new(any)) != io.EOF ||
		!validDisplayState(e.Next) || e.Next == StateStuck || e.Reason != StuckPermissionUnanswered || e.AtMillis <= 0 {
		return stuckTransition{}, false
	}
	return e, true
}

func (e stuckTransition) emit() {
	e.emitContext(context.Background())
}

// emitContext is emit under ctx, for a caller whose other post-commit trail
// writes share one bound.
func (e stuckTransition) emitContext(ctx context.Context) {
	_ = trail.Emit(ctx, "ad.spawn.stuck_transition", map[string]any{
		"instance_id": e.InstanceID, "prior_state": e.Prior, "new_state": e.Next,
		"stuck_reason": e.Reason, "ts": time.UnixMilli(e.AtMillis).UTC().Format("2006-01-02T15:04:05.000Z"),
	})
}

// takeStuckExits consumes immediate lifecycle exits under the existing writer
// transaction. Its caller emits only after that same transaction commits.
// This marker is never background work and cannot produce a duplicate attempt.
// It reads only instanceID, the row the lifecycle write changed: every exit is
// written and drained by the same transaction, so no other row holds one.
// Report-only data never blocks a lifecycle write: an invalid exit is cleared
// without an event rather than failing the transaction.
func takeStuckExits(ctx context.Context, conn *sql.Conn, instanceID string) ([]stuckTransition, error) {
	const where = `instance_id=? AND stuck_exit IS NOT NULL`
	args := []any{instanceID}
	rows, err := conn.QueryContext(ctx, `SELECT instance_id,stuck_exit FROM spawns WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	var events []stuckTransition
	found := false
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		found = true
		if event, ok := decodeStuckExit(raw); ok {
			event.InstanceID, event.Prior = id, StateStuck
			events = append(events, event)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if found {
		if _, err := conn.ExecContext(ctx, `UPDATE spawns SET stuck_exit=NULL WHERE `+where, args...); err != nil {
			return nil, err
		}
	}
	return events, nil
}

func emitStuckTransitions(events []stuckTransition) {
	emitStuckTransitionsContext(context.Background(), events)
}

func emitStuckTransitionsContext(ctx context.Context, events []stuckTransition) {
	for _, event := range events {
		event.emitContext(ctx)
	}
}
