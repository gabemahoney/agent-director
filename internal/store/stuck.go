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

type FallbackObservation struct {
	RequestID   int64
	ConfirmBy   time.Time
	ProcessGone bool
}

// RecordPermissionFallbacks preserves the first established fallback basis as
// well as the existing public hook_gone_at observation. In particular a later
// process observation cannot replace an earlier deadline-based clock.
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
			start, basis := observation.ConfirmBy, FallbackDeadline
			switch {
			case agent == AgentCursor && relay == "off":
				start, basis = pr.AdmittedAt, FallbackAdmission
				if start.IsZero() {
					start = pr.CreatedAt
				}
			case agent != AgentCursor && !pr.PreV7() && observation.ProcessGone:
				start, basis = pr.HookGoneAt, FallbackProcess
				if start.IsZero() {
					start = at
				}
			}
			var gone sql.NullInt64
			if err := conn.QueryRowContext(ctx, `UPDATE permission_requests SET
 hook_gone_at=COALESCE(hook_gone_at,?),
 fallback_at=COALESCE(fallback_at,?),
 fallback_basis=CASE WHEN fallback_at IS NULL THEN ? ELSE fallback_basis END
 WHERE request_id=? RETURNING hook_gone_at`, millisArg(at), millisArg(start), basis, pr.RequestID).Scan(&gone); err != nil {
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
}

func decodeStuckObservation(raw string) (stuckObservation, bool) {
	var o stuckObservation
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if len(raw) > 16384 || d.Decode(&o) != nil || d.Decode(new(any)) != io.EOF ||
		!validDisplayState(o.State) || (o.Candidate != "" && !validDisplayState(o.Candidate)) ||
		(o.Candidate == "" && o.State != StateStuck) || o.CandidateSince < 0 || o.ObservedAt < 0 ||
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
		pr.SupersededAt.IsZero() && pr.AwaitsAnswer() && !pr.ProvenGone() &&
		!sample.FallbackAt.IsZero() && !sample.FallbackAt.After(now)
}

// observationWrites reports whether observing samples on examined can commit
// anything: a retained observation to advance or clear, a fallback clock to
// establish, or a request past its timeout. Otherwise the read is answered
// from examined alone and takes no write lock.
func observationWrites(examined Spawn, samples []PermissionSample, now time.Time, timeout time.Duration) bool {
	if examined.StuckObservation != "" {
		return true
	}
	for _, sample := range samples {
		if permissionCandidate(examined, sample.Request, sample, now) &&
			(sample.Request.FallbackAt.IsZero() || now.Sub(sample.FallbackAt) >= timeout) {
			return true
		}
	}
	return false
}

// ObservePermissionStuck commits one nonwaiting observation. The write lock
// protects request facts as well as row ownership; row_version alone does not
// detect a concurrent pane answer, ack, proof or explicit supersession.
func (s *Store) ObservePermissionStuck(examined Spawn, samples []PermissionSample, now time.Time, timeout time.Duration, settle SettleRead) (StuckDisplay, error) {
	previous := examined.CommittedStuckDisplay()
	if !observationWrites(examined, samples, now, timeout) {
		return previous, nil
	}
	var result StuckDisplay
	var event *stuckTransition
	err := s.inWriteTx(0, func(ctx context.Context, conn *sql.Conn) error {
		row, err := scanSpawnStatus(conn.QueryRowContext(ctx, `SELECT `+statusRowColumns+` FROM spawns WHERE instance_id=?`, examined.InstanceID), false)
		if err != nil {
			return err
		}
		if row.Snapshot != examined.Snapshot || row.Identity != examined.Identity || row.PermissionEpoch != examined.PermissionEpoch || row.StuckVersion != examined.StuckVersion || row.State != examined.State {
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
		byID := make(map[int64]PermissionSample, len(samples))
		for _, sample := range samples {
			byID[sample.Request.RequestID] = sample
		}
		var representative *PermissionRow
		var earliest time.Time
		clocked := false
		for _, pr := range current {
			sample, ok := byID[pr.RequestID]
			if !ok || pr != sample.Request {
				return errStuckSampleChanged
			}
			if !permissionCandidate(row, pr, sample, now) {
				continue
			}
			start := sample.FallbackAt
			// Clock and display are one sample: failed persistence publishes
			// neither a newly observed process death nor its derived eligibility.
			if pr.FallbackAt.IsZero() {
				if _, err := conn.ExecContext(ctx, `UPDATE permission_requests SET fallback_at=?,fallback_basis=?,
 hook_gone_at=COALESCE(hook_gone_at,?) WHERE request_id=?`, millisArg(start), sample.Basis, millisArg(now), pr.RequestID); err != nil {
					return err
				}
				clocked = true
			}
			if now.Sub(start) < timeout {
				continue
			}
			if representative == nil || start.Before(earliest) || (start.Equal(earliest) && pr.RequestToken < representative.RequestToken) {
				selected := pr
				representative, earliest = &selected, start
			}
		}
		old, valid := decodeStuckObservation(examined.StuckObservation)
		if valid && now.UnixMilli() < old.ObservedAt {
			return errStuckSampleChanged
		}
		target := row.State
		if representative != nil {
			target = StateStuck
		}
		if !valid {
			old = stuckObservation{StuckDisplay: StuckDisplay{State: examined.ReportedState}}
		}
		if old.State != StateStuck && target != StateStuck {
			// Ordinary state settling continues to be derived by settle.go.
			result = StuckDisplay{State: examined.ReportedState}
			if examined.StuckObservation == "" && !clocked {
				// Nothing to clear: only a race since the pre-check reaches here.
				return nil
			}
			_, err := conn.ExecContext(ctx, `UPDATE spawns SET stuck_observation=NULL,stuck_version=stuck_version+1 WHERE instance_id=?`, row.InstanceID)
			return err
		}
		if old.Candidate != target {
			old.Candidate, old.CandidateSince = target, now.UnixMilli()
		}
		prior := old.State
		if now.UnixMilli()-old.CandidateSince >= max(settle.SettleMs, 0) {
			if old.State != target {
				old.State = target
				if target == StateStuck {
					old.SinceMillis = now.UnixMilli()
					old.Reason = StuckPermissionUnanswered
				}
				event = &stuckTransition{InstanceID: row.InstanceID, Prior: prior, Next: target, Reason: old.Reason, AtMillis: now.UnixMilli()}
			}
		}
		if old.State == StateStuck && representative != nil {
			old.RequestToken, old.ToolName = representative.RequestToken, representative.ToolName
		}
		old.ObservedAt = now.UnixMilli()
		result = old.StuckDisplay
		var raw any
		if old.State == StateStuck || target == StateStuck {
			encoded, err := json.Marshal(old)
			if err != nil {
				return err
			}
			raw = string(encoded)
		} else {
			result = StuckDisplay{State: target}
		}
		_, err = conn.ExecContext(ctx, `UPDATE spawns SET stuck_observation=?,stuck_version=stuck_version+1 WHERE instance_id=?`, raw, row.InstanceID)
		return err
	})
	if err != nil {
		return previous, err
	}
	if event != nil {
		event.emit()
	}
	return result, nil
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
	_ = trail.Emit(context.Background(), "ad.spawn.stuck_transition", map[string]any{
		"instance_id": e.InstanceID, "prior_state": e.Prior, "new_state": e.Next,
		"stuck_reason": e.Reason, "ts": time.UnixMilli(e.AtMillis).UTC().Format("2006-01-02T15:04:05.000Z"),
	})
}

// takeStuckExits consumes immediate lifecycle exits under the existing writer
// transaction. Its caller emits only after that same transaction commits.
// This marker is never background work and cannot produce a duplicate attempt.
// It reads only instanceID, the row the lifecycle write changed: every exit is
// written and drained by the same transaction, so no other row holds one.
func takeStuckExits(ctx context.Context, conn *sql.Conn, instanceID string) ([]stuckTransition, error) {
	const where = `instance_id=? AND stuck_exit IS NOT NULL`
	args := []any{instanceID}
	rows, err := conn.QueryContext(ctx, `SELECT instance_id,stuck_exit FROM spawns WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	var events []stuckTransition
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		event, ok := decodeStuckExit(raw)
		if !ok {
			rows.Close()
			return nil, errors.New("store: invalid immediate stuck exit")
		}
		event.InstanceID, event.Prior = id, StateStuck
		events = append(events, event)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(events) > 0 {
		if _, err := conn.ExecContext(ctx, `UPDATE spawns SET stuck_exit=NULL WHERE `+where, args...); err != nil {
			return nil, err
		}
	}
	return events, nil
}

func emitStuckTransitions(events []stuckTransition) {
	for _, event := range events {
		event.emit()
	}
}
