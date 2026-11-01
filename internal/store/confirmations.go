package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// confirmations.go — the bounded main-field evidence of SR-4.1 to SR-4.8
// (schema v19). Three nullable JSON columns of a spawns row hold it, and no
// other table, history or per-member record does:
//
//   - state_evidence: the evidence that established the raw state in its
//     current raw-state interval (permission_epoch) of the row's current life
//     and launch (stateEvidenceSet);
//   - settled_evidence: the evidence of the value settled_state holds, moved
//     from state_evidence by the hook write that makes that value the settled
//     base and cleared by every immediate write (settle.go), so a settling read
//     reports the time of the value it actually shows;
//   - process_evidence: the latest process-liveness attempt for the recorded
//     agent process at its recorded start identity (ProcessEvidenceAfter),
//     with the time and source of the last attempt that established its value.
//     A terminal move writes it in its own statement: the exited record of
//     the process whose proven exit it acted on, NULL otherwise
//     (terminalProcessEvidenceSet, clearProcessEvidence).
//
// Each record names the life, launch token and, for state, the permission
// epoch, value or process identity it was made for. A read applies a record
// only to that exact owner and value (Spawn.StateConfirmation,
// Spawn.ProcessConfirmation), so a writer that records no evidence leaves the
// field honestly unknown rather than inheriting an earlier value's time. A
// same-value write keeps an applicable record as it is (a soft refresh, a held
// transition or a reread of old hook bytes never redates it), and nothing
// derives a record from the clock, a settle projection or an unrelated
// timestamp. Writing evidence advances neither row_version nor
// observation_version: like a display observation it is metadata of the row's
// current identity, never an authority a guard reads.

// The SR-4.5 confirmation statuses.
const (
	ConfirmationConfirmed    = "confirmed"
	ConfirmationNotConfirmed = "not_confirmed"
	ConfirmationUnknown      = "unknown"
	ConfirmationPending      = "pending"
)

// The SR-4.5 evidence sources. EvidenceStore confirms a durable
// agent-director record or intent, never provider receipt.
const (
	EvidenceAgentEvent    = "agent_event"
	EvidenceCallerCommand = "caller_command"
	EvidenceProcess       = "process"
	EvidenceTmux          = "tmux"
	EvidencePane          = "pane"
	EvidenceTranscript    = "transcript"
	EvidenceStore         = "store"
)

// The SR-4.5 reasons an attempt did not confirm its field.
const (
	ReasonNoEvidence        = "no_evidence"
	ReasonUnreadable        = "unreadable"
	ReasonBusy              = "busy"
	ReasonStaleObservation  = "stale_observation"
	ReasonDeadline          = "deadline"
	ReasonOperationPending  = "operation_pending"
	ReasonPendingGrace      = "pending_grace"
	ReasonUnsupported       = "unsupported"
	ReasonCommitUnconfirmed = "commit_unconfirmed"
)

// The process_liveness values (SR-4.1).
const (
	ProcessAlive   = "alive"
	ProcessExited  = "exited"
	ProcessUnknown = "unknown"
)

func validEvidenceSource(s string) bool {
	switch s {
	case EvidenceAgentEvent, EvidenceCallerCommand, EvidenceProcess, EvidenceTmux, EvidencePane, EvidenceTranscript, EvidenceStore:
		return true
	}
	return false
}

// ValidConfirmationReason reports whether r is one of the SR-4.5 reasons.
func ValidConfirmationReason(r string) bool {
	switch r {
	case ReasonNoEvidence, ReasonUnreadable, ReasonBusy, ReasonStaleObservation, ReasonDeadline,
		ReasonOperationPending, ReasonPendingGrace, ReasonUnsupported, ReasonCommitUnconfirmed:
		return true
	}
	return false
}

// Confirmation is one main field's record: its SR-4.6 status, the last
// evidence confirming the returned value and owner (ConfirmedAtMillis and
// Source, 0 and "" when none is known), the latest attempt for the field
// (CheckedAtMillis, 0 when none is known) and, unless that attempt
// confirmed it, why not (Reason). Times are milliseconds since the epoch.
type Confirmation struct {
	Status            string
	ConfirmedAtMillis int64
	Source            string
	CheckedAtMillis   int64
	Reason            string
}

// UnknownConfirmation is a field with no applicable evidence and no known
// attempt: status unknown, every time and source null, reason r.
func UnknownConfirmation(r string) Confirmation {
	return Confirmation{Status: ConfirmationUnknown, Reason: r}
}

// fieldEvidence is one stored record, decoded tolerantly: anything malformed
// is no evidence, so no stored value fails a read.
type fieldEvidence struct {
	Value   string  `json:"v"`
	Life    *int64  `json:"life"`
	Token   *string `json:"token"`
	Epoch   *int64  `json:"epoch"`
	PID     int     `json:"pid"`
	Start   string  `json:"start"`
	At      int64   `json:"at"`
	Source  string  `json:"src"`
	Checked int64   `json:"checked"`
	Reason  string  `json:"reason"`
}

func decodeFieldEvidence(raw string) (fieldEvidence, bool) {
	var ev fieldEvidence
	if raw == "" || json.Unmarshal([]byte(raw), &ev) != nil || ev.Life == nil {
		return fieldEvidence{}, false
	}
	if ev.At < 0 || ev.Checked < 0 || (ev.At > 0) != validEvidenceSource(ev.Source) || (ev.Source != "" && ev.At == 0) {
		return fieldEvidence{}, false
	}
	if ev.Reason != "" && !ValidConfirmationReason(ev.Reason) {
		return fieldEvidence{}, false
	}
	return ev, true
}

// owns reports whether ev was made for sp's current life and launch.
func (ev fieldEvidence) owns(sp Spawn) bool {
	token := ""
	if ev.Token != nil {
		token = decodeLaunchToken(*ev.Token)
	}
	return *ev.Life == sp.LifeNumber && token == sp.Identity.Token
}

// stateRecordFor returns the confirmed record of a state evidence that
// applies.
func stateRecordFor(ev fieldEvidence) Confirmation {
	return Confirmation{Status: ConfirmationConfirmed, ConfirmedAtMillis: ev.At, Source: ev.Source, CheckedAtMillis: max(ev.Checked, ev.At)}
}

// processIdentityOf reports whether ev names the agent process sp records:
// its SessionStart identity or its pane identity (tmux.SelectAgentProcess
// picks between the two), each a pid with its exact start time.
func (ev fieldEvidence) processIdentityOf(sp Spawn) bool {
	if ev.PID <= 0 || ev.Start == "" {
		return false
	}
	return (ev.PID == sp.PID && ev.Start == sp.ProcStarttime) ||
		(ev.PID == sp.Identity.PanePID && ev.Start == sp.Identity.PaneStarttime)
}

func validProcessValue(v string) bool {
	return v == ProcessAlive || v == ProcessExited || v == ProcessUnknown
}

// applicableProcessEvidence is the stored process record when it was made for
// sp's current life, launch and recorded process identity.
func (sp Spawn) applicableProcessEvidence() (fieldEvidence, bool) {
	ev, ok := decodeFieldEvidence(sp.ProcessEvidence)
	if !ok || !validProcessValue(ev.Value) || !ev.owns(sp) || !ev.processIdentityOf(sp) ||
		(ev.Value == ProcessUnknown && ev.At > 0) || (ev.Reason == "" && ev.At == 0) {
		return fieldEvidence{}, false
	}
	return ev, true
}

// ProcessConfirmation is the stored process_liveness value and record. A
// record applies only to the row's current life, launch and recorded process
// at its recorded start identity, never to a helper, tmux or a descendant. A
// latest attempt that failed keeps its last confirmed value and time as
// not_confirmed with its reason; no applicable record is unknown.
func (sp Spawn) ProcessConfirmation() (string, Confirmation) {
	ev, ok := sp.applicableProcessEvidence()
	if !ok {
		return ProcessUnknown, UnknownConfirmation(ReasonNoEvidence)
	}
	c := Confirmation{Status: ConfirmationConfirmed, ConfirmedAtMillis: ev.At, Source: ev.Source, CheckedAtMillis: max(ev.Checked, ev.At), Reason: ev.Reason}
	if ev.Reason != "" {
		c.Status = ConfirmationNotConfirmed
		if ev.At == 0 && ev.Reason == ReasonNoEvidence {
			c.Status = ConfirmationUnknown
		}
	}
	return ev.Value, c
}

// The evidence column fragment every full-row and status-row read selects,
// last before any reported columns, in evidenceScan.dest's order.
const evidenceColumns = `state_evidence, settled_evidence, process_evidence`

// evidenceScan receives one row's evidenceColumns; every stored value reads,
// a non-text one as none.
type evidenceScan struct {
	state, settled, process any
}

func (e *evidenceScan) dest() []any { return []any{&e.state, &e.settled, &e.process} }

func (e *evidenceScan) apply(sp *Spawn) {
	sp.StateEvidence, _ = e.state.(string)
	sp.SettledStateEvidence, _ = e.settled.(string)
	sp.ProcessEvidence, _ = e.process.(string)
}

// evidenceText is col's text, NULL for any other storage class, so the JSON
// functions below never see a BLOB or number.
func evidenceText(col string) string {
	return `CASE WHEN typeof(` + col + `) = 'text' AND json_valid(` + col + `) THEN ` + col + ` END`
}

// evidenceKey is col's key as SQL, NULL when col holds no valid record.
func evidenceKey(col, key string) string {
	return `json_extract(` + evidenceText(col) + `, '$.` + key + `')`
}

// stateEvidenceCurrentSQL is true while state_evidence is the applicable
// record of the row's raw state in its current interval, life and launch.
var stateEvidenceCurrentSQL = `(` + `COALESCE(` + evidenceKey("state_evidence", "v") + ` = state, 0)
        AND COALESCE(` + evidenceKey("state_evidence", "epoch") + ` = permission_epoch, 0)
        AND COALESCE(` + evidenceKey("state_evidence", "life") + ` = life_number, 0)
        AND ` + evidenceKey("state_evidence", "token") + ` IS launch_token
        AND ` + evidenceKey("state_evidence", "reason") + ` IS NULL)`

// settledEvidenceMove is the SET assignment of a hook write's settled base
// (hookStateSet): exactly where settled_state takes the old raw value, the
// old raw record goes with it, applicable or not at all; where settled_state
// is kept, its record is kept. Its placeholders are V, Tw, Tw.
var settledEvidenceMove = `settled_evidence = CASE WHEN state = ? THEN settled_evidence
        WHEN ? = 0 OR ` + nowMsSQL + ` - state_changed_ms >= ? OR NULLIF(settled_state, '') IS NULL
        THEN CASE WHEN ` + stateEvidenceCurrentSQL + ` THEN state_evidence END
        ELSE settled_evidence END`

// stateEvidenceSet returns the SET assignment recording that the statement's
// write of value establishes it from src, for the life and launch token the
// row holds after the statement (lifeExpr, tokenExpr: SQL over the old row,
// with their arguments in lifeArgs and tokenArgs). The record's epoch is the
// one the statement's state fragment produces. A write of the raw value the
// row already holds keeps an applicable record unchanged, so only a raw-state
// change or a row with no applicable record is stamped, with the statement's
// own clock. An empty src records no evidence: the field then has no record
// (NULL) for the new value. Place it in the SET list after the state
// fragment, its arguments after that fragment's.
func stateEvidenceSet(value, src, lifeExpr, tokenExpr string, lifeArgs, tokenArgs []any) (string, []any) {
	set := `state_evidence = CASE WHEN state = ? AND ` + stateEvidenceCurrentSQL + ` THEN state_evidence
        WHEN ? IS NULL THEN NULL
        ELSE json_object('v', ?, 'life', ` + lifeExpr + `, 'token', ` + tokenExpr + `,
          'epoch', permission_epoch + CASE WHEN state = ? THEN 0 ELSE 1 END,
          'at', ` + nowMsSQL + `, 'src', ?, 'checked', ` + nowMsSQL + `) END`
	args := []any{value, nullableStringArg(src), value}
	args = append(args, lifeArgs...)
	args = append(args, tokenArgs...)
	return set, append(args, value, src)
}

// currentStateEvidenceSet is stateEvidenceSet for a statement that keeps the
// row's life and launch.
func currentStateEvidenceSet(value, src string) (string, []any) {
	return stateEvidenceSet(value, src, "life_number", "launch_token", nil, nil)
}

// restoredStateEvidenceSet returns the SET assignment of a failed launch's
// restore: the prior life's record, exactly as stored, rebound to the epoch
// the restore's immediate write of prior produces, or NULL when no record
// applied to prior. Its placeholders are the record and prior.
func restoredStateEvidenceSet(record, prior string) (string, []any) {
	return `state_evidence = CASE WHEN ? IS NULL THEN NULL
        ELSE json_set(?, '$.epoch', permission_epoch + CASE WHEN state = ? THEN 0 ELSE 1 END) END`,
		[]any{nullableStringArg(record), nullableStringArg(record), prior}
}

// PriorStateEvidence is the state record a failed launch's restore may write
// back for sp, the row the launch examined: sp's raw record when it applied to
// sp's raw state, "" otherwise. A record of another value, interval or owner
// is never carried into the restored life.
func (sp Spawn) PriorStateEvidence() string {
	if ev, ok := decodeFieldEvidence(sp.StateEvidence); ok && ev.Value == sp.State && ev.Epoch != nil &&
		*ev.Epoch == sp.PermissionEpoch && ev.owns(sp) && ev.Reason == "" && ev.At > 0 {
		return sp.StateEvidence
	}
	return ""
}

// PriorProcessEvidence is the process record a failed launch's restore may
// write back for sp: its applicable record, "" otherwise.
func (sp Spawn) PriorProcessEvidence() string {
	if _, ok := sp.applicableProcessEvidence(); ok {
		return sp.ProcessEvidence
	}
	return ""
}

// ProcessAttempt is one Active Check's attempt to establish process_liveness
// for the recorded agent process PID at its start identity Start: Value is
// what the attempt established (alive or exited) with Reason "", or the
// value it could not establish, unknown, with the reason it failed.
type ProcessAttempt struct {
	PID    int
	Start  string
	Value  string
	Reason string
}

// ProcessEvidenceAfter returns the record sp holds after attempt, made at
// atMillis: a successful attempt stamps its value with source process; a
// failed one keeps the last applicable value, time and source and records
// only its own time and reason. ok is false when the attempt names no
// process the row records, so nothing may be stored; for a Cursor row,
// whose runtime process no check observes (process_liveness unsupported);
// and for an alive attempt on a finished row, which only a sample taken
// before the row's terminal move can be (a finished row never reports its
// last alive observation).
func (sp Spawn) ProcessEvidenceAfter(attempt ProcessAttempt, atMillis int64) (string, bool) {
	probe := fieldEvidence{PID: attempt.PID, Start: attempt.Start}
	if sp.Agent == AgentCursor || !probe.processIdentityOf(sp) || atMillis <= 0 || !validProcessValue(attempt.Value) ||
		(attempt.Value == ProcessAlive && !IsLiveState(sp.State)) ||
		(attempt.Reason == "") == (attempt.Value == ProcessUnknown) ||
		(attempt.Reason != "" && !ValidConfirmationReason(attempt.Reason)) {
		return "", false
	}
	life := sp.LifeNumber
	var token *string
	if sp.Identity.Token != "" {
		token = &sp.Identity.Token
	}
	next := fieldEvidence{Value: attempt.Value, Life: &life, Token: token, PID: attempt.PID, Start: attempt.Start, Checked: atMillis, Reason: attempt.Reason}
	if attempt.Reason == "" {
		next.At, next.Source = atMillis, EvidenceProcess
	} else if old, ok := sp.applicableProcessEvidence(); ok && old.PID == attempt.PID && old.Start == attempt.Start && old.At > 0 {
		next.Value, next.At, next.Source = old.Value, old.At, old.Source
	}
	type stored struct {
		Value   string  `json:"v"`
		Life    *int64  `json:"life"`
		Token   *string `json:"token"`
		PID     int     `json:"pid"`
		Start   string  `json:"start"`
		At      *int64  `json:"at"`
		Source  *string `json:"src"`
		Checked int64   `json:"checked"`
		Reason  *string `json:"reason"`
	}
	out := stored{Value: next.Value, Life: next.Life, Token: next.Token, PID: next.PID, Start: next.Start, Checked: next.Checked}
	if next.At > 0 {
		out.At, out.Source = &next.At, &next.Source
	}
	if next.Reason != "" {
		out.Reason = &next.Reason
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return "", false
	}
	return string(raw), true
}

// ProcessExit names the recorded agent process whose proven exit a terminal
// move acted on: its pid at its exact start identity, judged gone at
// AtMillis on the caller's clock (the clock ProcessEvidenceAfter is given).
// The zero ProcessExit names none: the move was not proven by that process
// being gone, or the judgement had no start identity (SR-4.8).
type ProcessExit struct {
	PID      int
	Start    string
	AtMillis int64
}

// terminalProcessEvidenceSet is the process_evidence assignment of every
// guarded terminal move (missing or ended) that takes a ProcessExit: the
// exited record of exit, bound in the move's own statement to the life and
// launch the row holds and stored only while the row still records that pid
// at that start identity (its SessionStart or its pane identity), so the
// field's evidence commits with the state and family change it proved
// (SR-4.7). Every other terminal move, and one whose exit names no process
// the row records or a Cursor row, stores NULL: a finished row never keeps an
// earlier alive record of a process the move did not observe, and its
// process_liveness reads unknown rather than confirmed alive. Its
// placeholders are terminalProcessEvidenceArgs(exit).
var terminalProcessEvidenceSet = `process_evidence = CASE WHEN ? IS NULL OR agent = '` + AgentCursor + `'
          OR NOT ((COALESCE(pid, 0) = ? AND COALESCE(proc_starttime, '') = ?)
            OR (COALESCE(pane_pid, 0) = ? AND COALESCE(pane_starttime, '') = ?)) THEN NULL
        ELSE json_object('v', '` + ProcessExited + `', 'life', COALESCE(life_number, 0), 'token', launch_token,
          'pid', ?, 'start', ?, 'at', ?, 'src', '` + EvidenceProcess + `', 'checked', ?, 'reason', NULL) END`

// terminalProcessEvidenceArgs returns terminalProcessEvidenceSet's arguments
// for exit; an exit with no pid, start identity or time stores NULL.
func terminalProcessEvidenceArgs(exit ProcessExit) []any {
	if exit.PID <= 0 || exit.Start == "" || exit.AtMillis <= 0 {
		return []any{nil, 0, "", 0, "", nil, nil, nil, nil}
	}
	return []any{exit.PID, exit.PID, exit.Start, exit.PID, exit.Start, exit.PID, exit.Start, exit.AtMillis, exit.AtMillis}
}

// clearProcessEvidence is the process_evidence assignment of a terminal
// write that proves nothing about the agent process (a SessionEnd, a held
// launch's end): its finished row reads process_liveness unknown, never an
// earlier alive record.
const clearProcessEvidence = `process_evidence = NULL`

// recordProcessEvidenceSQL stores a process record only while the row still
// holds the snapshot, life, launch, raw-state interval, observations and
// process record the attempt examined, so a stale attempt never overwrites a
// newer record or applies to a later process.
const recordProcessEvidenceSQL = `UPDATE spawns SET process_evidence = ?
  WHERE instance_id = ? AND ` + snapshotMatchSQL + ` AND COALESCE(life_number, 0) = ? AND launch_token IS ?
    AND permission_epoch = ? AND observation_version = ? AND process_evidence IS ?`

// RecordProcessEvidence commits record (ProcessEvidenceAfter) for sampled,
// the row the attempt examined, with nonwaiting admission: it writes only
// process_evidence, which no family contribution reads, and advances no
// version. CondChanged means the row moved on and nothing was written; a
// busy store, a canceled caller or an unconfirmed COMMIT returns the error,
// and the attempt is then not a durable confirmation.
func (s *Store) RecordProcessEvidence(sampled Spawn, record string) (CondResult, error) {
	args := append([]any{record, sampled.InstanceID}, snapshotMatchArgs(sampled.Snapshot)...)
	args = append(args, sampled.LifeNumber, nullableStringArg(sampled.Identity.Token), sampled.PermissionEpoch,
		sampled.ObservationVersion, nullableStringArg(sampled.ProcessEvidence))
	result := CondChanged
	ctx, cancel := s.operationContext(0)
	defer cancel()
	err := s.inWriteTxContext(ctx, 0, func(ctx context.Context, conn *sql.Conn) error {
		res, err := conn.ExecContext(ctx, recordProcessEvidenceSQL, args...)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n > 0 {
			result = CondApplied
			return nil
		}
		var exists bool
		if err := conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM spawns WHERE instance_id=?)`, sampled.InstanceID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			result = CondAbsent
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("store: record process evidence: %w", err)
	}
	return result, nil
}
