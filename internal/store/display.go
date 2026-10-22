package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"modernc.org/sqlite"

	"github.com/gabemahoney/agent-director/internal/trail"
)

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
// 9999, or recorded before schema v7 with a zero created_at).
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

// DisplayDetail is committed report-only detail. State is the displayed
// state: needs_intelligence, idle_screen or an ordinary state. SinceMillis
// denotes the read that actually entered the Displayed State, never the
// eligibility boundary. RequestToken and ToolName belong to
// needs_intelligence, UnchangedMinutes to idle_screen.
type DisplayDetail struct {
	State            string `json:"state"`
	SinceMillis      int64  `json:"since_ms,omitempty"`
	RequestToken     string `json:"request_token,omitempty"`
	ToolName         string `json:"tool_name,omitempty"`
	UnchangedMinutes int64  `json:"unchanged_minutes,omitempty"`
}

type displayObservation struct {
	DisplayDetail
	// Candidate is the state settling in: a Displayed State or an ordinary one.
	Candidate      string `json:"candidate"`
	CandidateSince int64  `json:"candidate_since_ms"`
	ObservedAt     int64  `json:"observed_at_ms"`
	// PermissionEpoch is the raw-state epoch of the interval this observation
	// belongs to. A display of another epoch is a finished episode, unless the
	// other Displayed State moves in during this interval.
	PermissionEpoch int64 `json:"permission_epoch"`
}

func decodeDisplayObservation(raw string) (displayObservation, bool) {
	var o displayObservation
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if len(raw) > 16384 || d.Decode(&o) != nil || d.Decode(new(any)) != io.EOF ||
		!validDisplayState(o.State) || (o.Candidate != "" && !validDisplayState(o.Candidate)) ||
		(o.Candidate == "" && !isDisplayedState(o.State)) || o.CandidateSince < 0 || o.ObservedAt < 0 || o.PermissionEpoch < 0 ||
		(isDisplayedState(o.State) && (!validDisplayDetail(o.DisplayDetail) || o.SinceMillis <= 0)) {
		return displayObservation{}, false
	}
	return o, true
}

// validDisplayDetail reports whether d carries exactly its Displayed State's
// detail.
func validDisplayDetail(d DisplayDetail) bool {
	switch d.State {
	case StateNeedsIntelligence:
		return d.RequestToken != "" && d.UnchangedMinutes == 0
	case StateIdleScreen:
		return d.RequestToken == "" && d.ToolName == "" && d.UnchangedMinutes >= 0
	}
	return false
}

// isDisplayedState reports whether state is one of the two Displayed States,
// which reads derive and no write stores in state.
func isDisplayedState(state string) bool {
	return state == StateNeedsIntelligence || state == StateIdleScreen
}

func validDisplayState(state string) bool {
	return IsLiveState(state) || isTerminalState(state) || isDisplayedState(state)
}

// CommittedDisplay returns the history from the same row snapshot. A failed
// observation write must leave this display and detail together.
func (sp Spawn) CommittedDisplay() DisplayDetail {
	o, ok := decodeDisplayObservation(sp.DisplayObservation)
	if ok && isDisplayedState(o.State) && !isTerminalState(sp.State) && sp.State != StatePending {
		return o.DisplayDetail
	}
	return DisplayDetail{State: sp.ReportedState}
}

// committedDisplayStateFunc names the SQL function behind
// committedDisplayStateSQL, so a lifecycle write keeps, and records the exit
// of, exactly the display a read shows. SQL calls it only through
// committedDisplayStateSQL, whose BLOB casts functionText needs. init
// registers it on this binary's own connections only, so it must never appear
// in a trigger, view, index, CHECK or generated column: the sqlite3 CLI that
// skills/install-agent-director/install.sh and test/driver/sql.sh run on the
// store cannot call it.
const committedDisplayStateFunc = "committed_display_state"

func init() {
	sqlite.MustRegisterDeterministicScalarFunction(committedDisplayStateFunc, 2, committedDisplayState)
}

// committedDisplayState is CommittedDisplay in SQL: the Displayed State a row
// whose raw state is args[0] and whose display_observation is args[1] shows,
// NULL when it shows none. It never fails, so report-only data cannot fail
// the write that asks.
func committedDisplayState(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	sp := Spawn{State: functionText(args[0]), DisplayObservation: functionText(args[1])}
	if d := sp.CommittedDisplay(); isDisplayedState(d.State) {
		return d.State, nil
	}
	return nil, nil
}

// functionText is the text a row read scans from the column v came from:
// modernc passes a TEXT argument only up to its first NUL but a BLOB whole,
// so committedDisplayStateSQL casts each column to BLOB and only a BLOB is
// read. NULL is "", as the reads' COALESCE gives. Any other value, such as
// TEXT from SQL that skips those casts, is "" too, so an observation passed
// that way shows no display.
func functionText(v driver.Value) string {
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	return ""
}

var errDisplaySampleChanged = errors.New("store: display sample changed")

// permissionCandidate reports whether pr, judged as sample at now, belongs to
// row's current permission interval with a fallback clock already started.
// The unlocked pre-check and the locked re-check share it so they cannot drift.
func permissionCandidate(row Spawn, pr PermissionRow, sample PermissionSample, now time.Time) bool {
	return row.State == StateCheckPermission && (row.Agent == AgentClaude || row.Agent == AgentCodex || row.Agent == AgentCursor) &&
		pr.OwnerLaunchToken == row.Identity.Token && pr.OwnerPermissionEpoch == row.PermissionEpoch &&
		pr.AwaitsAnswer() && !pr.ProvenGone() &&
		!sample.FallbackAt.IsZero() && !sample.FallbackAt.After(now)
}

// planPermission judges the samples of row at now: the fallback clocks to
// establish and the needs_intelligence target, whose detail is the
// qualifying request with the earliest fallback, then the lexically first
// token; the zero target when none qualifies.
func planPermission(row Spawn, samples []PermissionSample, now time.Time, timeout time.Duration) ([]PermissionSample, DisplayDetail) {
	var clocks []PermissionSample
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
			clocks = append(clocks, sample)
		}
		if now.Sub(start) < timeout {
			continue
		}
		if representative == nil || start.Before(earliest) || (start.Equal(earliest) && pr.RequestToken < representative.RequestToken) {
			selected := pr
			representative, earliest = &selected, start
		}
	}
	if representative == nil {
		return clocks, DisplayDetail{}
	}
	return clocks, DisplayDetail{State: StateNeedsIntelligence, RequestToken: representative.RequestToken, ToolName: representative.ToolName}
}

// observationPlan is the outcome of observing a row: the observation to
// store, the display and trail events it yields, and whether anything need be
// written at all.
type observationPlan struct {
	// raw is the observation to store; nil clears it. text is raw as stored
	// ("" for NULL), to recognise a commit that landed.
	raw    any
	text   string
	result DisplayDetail
	events []displayTransition
	write  bool
}

// refreshFamiliesAfterObservation keeps display and contribution changes in
// one transaction. Ordinary working members already contribute, so their
// entries, detail updates and pane samples do no aggregate work. Decode both
// snapshots through CommittedDisplay rather than interpreting report-only JSON.
// A contribution change also returns the families it can change, as
// observedFamilies reads them; nil otherwise.
func (s *Store) refreshFamiliesAfterObservation(ctx context.Context, conn *sql.Conn, row Spawn, plan observationPlan, settle SettleRead) (map[string]string, error) {
	if row.State != StateWaiting {
		return nil, nil
	}
	prior := row.CommittedDisplay()
	row.DisplayObservation = plan.text
	if isDisplayedState(prior.State) == isDisplayedState(row.CommittedDisplay().State) {
		return nil, nil
	}
	if err := s.refreshFamilies(connQuerier{ctx, conn}); err != nil {
		return nil, err
	}
	return observedFamilies(ctx, conn, row.InstanceID, settle)
}

// observedFamilies reads, inside the observer's transaction, the family of
// instanceID and of each stored ancestor: every family one member's changed
// contribution can change. settle is the reader's own SettleRead, so the
// values are what that reader would see in the committed snapshot. Failure
// rolls the observation back rather than leaving a committed change the read
// cannot report.
func observedFamilies(ctx context.Context, conn *sql.Conn, instanceID string, settle SettleRead) (map[string]string, error) {
	family, familyArgs := settle.familyExpr()
	rows, err := conn.QueryContext(ctx, `WITH RECURSIVE chain(id) AS (
 SELECT ?
 UNION
 SELECT spawns.parent_id FROM chain
 JOIN spawns ON spawns.instance_id = chain.id
 WHERE spawns.parent_id IS NOT NULL
) SELECT instance_id, `+family+` FROM spawns WHERE instance_id IN (SELECT id FROM chain)`, append([]any{instanceID}, familyArgs...)...)
	if err != nil {
		return nil, fmt.Errorf("store: observed families: %w", err)
	}
	defer rows.Close()
	families := map[string]string{}
	for rows.Next() {
		var id, value string
		if err := rows.Scan(&id, &value); err != nil {
			return nil, fmt.Errorf("store: observed families: %w", err)
		}
		families[id] = value
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: observed families: %w", err)
	}
	return families, nil
}

// planObservation decides, from the row as examined and target, the
// Displayed State and detail the read found eligible (the zero DisplayDetail
// for none), everything an observer would commit to display_observation.
// extra is whether the observer has other sampled facts to write in the same
// commit. It is pure: the unlocked pre-check and the locked commit apply one
// plan, so they cannot drift. The locked commit proves the plan's inputs
// unchanged before applying it.
func planObservation(row Spawn, target DisplayDetail, extra bool, now time.Time, settle SettleRead) (observationPlan, error) {
	var p observationPlan
	stored, valid := decodeDisplayObservation(row.DisplayObservation)
	if valid && now.UnixMilli() < stored.ObservedAt {
		return p, errDisplaySampleChanged
	}
	targetState := row.State
	if target.State != "" {
		targetState = target.State
	}
	old := stored
	continuing := valid && isDisplayedState(stored.State)
	if !valid {
		old = displayObservation{}
	}
	if !continuing {
		// Not displayed: the display is the row's own settled state, never a
		// stale copy of it, so readers agree and an entry's prior is current.
		old.DisplayDetail = DisplayDetail{State: row.ReportedState}
	}
	if continuing && isDisplayedState(targetState) && old.PermissionEpoch != row.PermissionEpoch {
		if targetState == old.State {
			// A new interval never inherits the previous episode of its state:
			// that one ends with its own exit and the new one starts as a fresh
			// candidate.
			p.events = append(p.events, displayTransition{InstanceID: row.InstanceID, Prior: old.State, Next: row.ReportedState,
				AtMillis: now.UnixMilli()})
			old, continuing = displayObservation{DisplayDetail: DisplayDetail{State: row.ReportedState}}, false
		} else {
			// The other Displayed State settles in during this interval: the
			// display stays until it does.
			old.PermissionEpoch = row.PermissionEpoch
		}
	}
	if !isDisplayedState(old.State) && !isDisplayedState(targetState) {
		// Ordinary state settling continues to be derived by settle.go.
		p.result = DisplayDetail{State: row.ReportedState}
		p.write = row.DisplayObservation != "" || extra
		return p, nil
	}
	if !continuing {
		old.PermissionEpoch = row.PermissionEpoch
	}
	if old.Candidate != targetState {
		old.Candidate, old.CandidateSince = targetState, now.UnixMilli()
	}
	if now.UnixMilli()-old.CandidateSince >= max(settle.SettleMs, 0) && old.State != targetState {
		// Entry, exit and a move between the two states alike: the new state
		// shows with its own detail only, from this read.
		prior := old.State
		old.DisplayDetail = DisplayDetail{State: targetState}
		if isDisplayedState(targetState) {
			old.DisplayDetail = target
			old.SinceMillis = now.UnixMilli()
		}
		p.events = append(p.events, displayTransition{InstanceID: row.InstanceID, Prior: prior, Next: targetState, AtMillis: now.UnixMilli()})
	}
	if isDisplayedState(old.State) && old.State == targetState {
		// The representative request or the minutes change without settling.
		old.RequestToken, old.ToolName, old.UnchangedMinutes = target.RequestToken, target.ToolName, target.UnchangedMinutes
	}
	// A read that would change nothing writes nothing. The settle deadline
	// counts from the stored candidate_since, which such a read keeps, so
	// skipping the refresh never delays a due transition.
	unchanged := old
	if valid {
		unchanged.ObservedAt = stored.ObservedAt
	}
	if valid && unchanged == stored && !extra && len(p.events) == 0 {
		p.result = old.DisplayDetail
		return p, nil
	}
	old.ObservedAt = now.UnixMilli()
	p.write = true
	p.result = old.DisplayDetail
	if isDisplayedState(old.State) || isDisplayedState(targetState) {
		encoded, err := json.Marshal(old)
		if err != nil {
			return p, err
		}
		p.text = string(encoded)
		p.raw = p.text
	} else {
		p.result = DisplayDetail{State: targetState}
	}
	return p, nil
}

// ObserveNeedsIntelligence commits one nonwaiting observation. The write lock
// protects request facts as well as row ownership; row_version alone does not
// detect a concurrent pane answer, ack or proof. With the committed display it
// returns the families its commit refreshed, by instance id under settle (nil
// when the commit changed no contribution, or nothing committed), so a reader
// reports the display and its families from the same commit.
func (s *Store) ObserveNeedsIntelligence(examined Spawn, samples []PermissionSample, now time.Time, timeout time.Duration, settle SettleRead) (DisplayDetail, map[string]string, error) {
	previous := examined.CommittedDisplay()
	clocks, target := planPermission(examined, samples, now, timeout)
	plan, err := planObservation(examined, target, len(clocks) > 0, now, settle)
	if err != nil {
		return previous, nil, err
	}
	if !plan.write {
		return previous, nil, nil
	}
	var families map[string]string
	err = s.inWriteTx(0, func(ctx context.Context, conn *sql.Conn) error {
		row, err := lockedObservationRow(ctx, conn, examined)
		if err != nil {
			return err
		}
		current, err := queryPermissionRows(func(q string, args ...any) (*sql.Rows, error) { return conn.QueryContext(ctx, q, args...) },
			`SELECT `+permissionColumns+` FROM permission_requests pr WHERE instance_id=? ORDER BY request_id`, "store: needs_intelligence requests", row.InstanceID)
		if err != nil {
			return err
		}
		if len(current) != len(samples) {
			return errDisplaySampleChanged
		}
		byID := make(map[int64]PermissionRow, len(samples))
		for _, sample := range samples {
			byID[sample.Request.RequestID] = sample.Request
		}
		for _, pr := range current {
			if sampled, ok := byID[pr.RequestID]; !ok || pr != sampled {
				return errDisplaySampleChanged
			}
		}
		for _, sample := range clocks {
			if _, err := conn.ExecContext(ctx, `UPDATE permission_requests SET fallback_at=?,fallback_basis=?,
 hook_gone_at=COALESCE(hook_gone_at,?) WHERE request_id=?`, millisArg(sample.FallbackAt), sample.Basis, millisArg(now), sample.Request.RequestID); err != nil {
				return err
			}
		}
		_, err = conn.ExecContext(ctx, `UPDATE spawns SET display_observation=?,observation_version=observation_version+1 WHERE instance_id=?`, plan.raw, row.InstanceID)
		if err != nil {
			return err
		}
		families, err = s.refreshFamiliesAfterObservation(ctx, conn, row, plan, settle)
		return err
	})
	if err != nil {
		if !errors.Is(err, errCommitUnconfirmed) || !s.observationLanded(examined, plan, nil) {
			return previous, nil, err
		}
	}
	emitDisplayTransitions(plan.events)
	return plan.result, families, nil
}

// ObserveIdleScreen commits one nonwaiting pane observation of examined: the
// baseline sample yields (planPane) and the display it derives, through the
// same settling and trail rules as ObserveNeedsIntelligence. timeout is the
// row's effective idle_screen timeout. Like that observer it never waits for
// the write lock: a sample that cannot commit, a failed sample's reset
// included, changes nothing and returns the previously committed display, so
// a later sample may continue the committed baseline. A row changed since
// examined (its launch, turn, pane ownership or stored observations) refuses
// the sample, so an older sample never overwrites a newer one. Its families
// are as ObserveNeedsIntelligence returns them.
func (s *Store) ObserveIdleScreen(examined Spawn, sample PaneSample, now time.Time, timeout time.Duration, settle SettleRead) (DisplayDetail, map[string]string, error) {
	previous := examined.CommittedDisplay()
	baseline, target := planPane(examined, sample, now, timeout)
	plan, err := planObservation(examined, target, baseline != committedPaneBaseline(examined), now, settle)
	if err != nil {
		return previous, nil, err
	}
	if !plan.write {
		return previous, nil, nil
	}
	var families map[string]string
	err = s.inWriteTx(0, func(ctx context.Context, conn *sql.Conn) error {
		if _, err := lockedObservationRow(ctx, conn, examined); err != nil {
			return err
		}
		args := append([]any{plan.raw}, paneBaselineArgs(baseline, examined.PermissionEpoch)...)
		_, err := conn.ExecContext(ctx, `UPDATE spawns SET display_observation=?,observation_version=observation_version+1,
 pane_sha256=?,pane_since_ms=?,pane_epoch=? WHERE instance_id=?`, append(args, examined.InstanceID)...)
		if err != nil {
			return err
		}
		families, err = s.refreshFamiliesAfterObservation(ctx, conn, examined, plan, settle)
		return err
	})
	if err != nil {
		if !errors.Is(err, errCommitUnconfirmed) || !s.observationLanded(examined, plan, &baseline) {
			return previous, nil, err
		}
	}
	emitDisplayTransitions(plan.events)
	return plan.result, families, nil
}

// lockedObservationRow re-reads examined's row inside an observer's write
// transaction, refusing with errDisplaySampleChanged when anything the plan
// was made from has changed since: its snapshot, launch identity, agent, raw
// state, permission epoch or stored observations (observation_version).
func lockedObservationRow(ctx context.Context, conn *sql.Conn, examined Spawn) (Spawn, error) {
	row, err := scanSpawnStatus(conn.QueryRowContext(ctx, `SELECT `+statusRowColumns+` FROM spawns WHERE instance_id=?`, examined.InstanceID), false)
	if err != nil {
		return Spawn{}, err
	}
	if row.Snapshot != examined.Snapshot || row.Identity != examined.Identity || row.Agent != examined.Agent || row.PermissionEpoch != examined.PermissionEpoch || row.ObservationVersion != examined.ObservationVersion || row.State != examined.State {
		return Spawn{}, errDisplaySampleChanged
	}
	return row, nil
}

// observationLanded is the one nonwaiting re-read after an unconfirmed COMMIT
// (errCommitUnconfirmed): whether this plan's write, with baseline when the
// observer stored one, is what the row now holds. Any doubt, a busy store
// included, answers false, and the caller keeps the previous view. It does not
// wait for the write's own abandoned operation either: a caller released at
// its deadline while the COMMIT still runs (runConnectionOperation's
// operation_guard branch) usually finds that operation still holding the
// store's operation slot, so a COMMIT that lands is then answered as not
// landed and its transitions are not emitted by this read.
func (s *Store) observationLanded(examined Spawn, plan observationPlan, baseline *paneBaseline) bool {
	var row Spawn
	err := s.readWithin(0, "", func(ctx context.Context, conn *sql.Conn) error {
		var err error
		row, err = scanSpawnStatus(conn.QueryRowContext(ctx, `SELECT `+statusRowColumns+` FROM spawns WHERE instance_id=?`, examined.InstanceID), false)
		return err
	})
	return err == nil && row.Snapshot == examined.Snapshot && row.PermissionEpoch == examined.PermissionEpoch &&
		row.ObservationVersion == examined.ObservationVersion+1 && row.DisplayObservation == plan.text &&
		(baseline == nil || committedPaneBaseline(row) == *baseline)
}

// displayTransition is one committed change of Displayed State. Its JSON is
// the exit marker a lifecycle write stores in display_exit.
type displayTransition struct {
	InstanceID string `json:"-"`
	Prior      string `json:"prior_state"`
	Next       string `json:"new_state"`
	AtMillis   int64  `json:"at_ms"`
}

// decodeDisplayExit accepts only an exit from a Displayed State to an
// ordinary one.
func decodeDisplayExit(raw string) (displayTransition, bool) {
	var e displayTransition
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if len(raw) > 1024 || d.Decode(&e) != nil || d.Decode(new(any)) != io.EOF ||
		!isDisplayedState(e.Prior) || !validDisplayState(e.Next) || isDisplayedState(e.Next) || e.AtMillis <= 0 {
		return displayTransition{}, false
	}
	return e, true
}

// emitContext writes e's ad.spawn.displayed_state_transition trail event
// under ctx, so a caller whose other post-commit trail writes share one bound
// can pass it.
func (e displayTransition) emitContext(ctx context.Context) {
	_ = trail.Emit(ctx, "ad.spawn.displayed_state_transition", map[string]any{
		"instance_id": e.InstanceID, "prior_state": e.Prior, "new_state": e.Next,
		"ts": time.UnixMilli(e.AtMillis).UTC().Format("2006-01-02T15:04:05.000Z"),
	})
}

// takeDisplayExits consumes immediate lifecycle exits under the existing
// writer transaction. Its caller emits only after that same transaction
// commits. This marker is never background work and cannot produce a
// duplicate attempt. It reads only instanceID, the row the lifecycle write
// changed: every exit is written and drained by the same transaction, so no
// other row holds one. Report-only data never blocks a lifecycle write: an
// invalid exit is cleared without an event rather than failing the
// transaction.
func takeDisplayExits(ctx context.Context, conn *sql.Conn, instanceID string) ([]displayTransition, error) {
	const where = `instance_id=? AND display_exit IS NOT NULL`
	args := []any{instanceID}
	rows, err := conn.QueryContext(ctx, `SELECT instance_id,display_exit FROM spawns WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	var events []displayTransition
	found := false
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		found = true
		if event, ok := decodeDisplayExit(raw); ok {
			event.InstanceID = id
			events = append(events, event)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if found {
		if _, err := conn.ExecContext(ctx, `UPDATE spawns SET display_exit=NULL WHERE `+where, args...); err != nil {
			return nil, err
		}
	}
	return events, nil
}

func emitDisplayTransitions(events []displayTransition) {
	emitDisplayTransitionsContext(context.Background(), events)
}

func emitDisplayTransitionsContext(ctx context.Context, events []displayTransition) {
	for _, event := range events {
		event.emitContext(ctx)
	}
}
