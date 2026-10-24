package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// This file holds a hook's proof that permission requests' dialogs are gone
// (b.146 step 2c). agent-director's own records of a request (its relay
// hook's ack, a pane answer, record-pane-answer) say what agent-director did,
// not what the agent shows; only the agent's own hooks, or the end of its
// agent, prove the dialog gone. Until then the request holds plain send-keys
// to its Spawn, and, while it awaits an answer, its Spawn's row
// (the writes and reads holdsRowSQL lists; b.omt). The third proof,
// agent_gone, is written by the close of a Spawn's requests
// (closeOrphanedRequests).

// RequestProof is what one hook of a Spawn's agent proves gone (b.146 step
// 2c).
type RequestProof struct {
	// How is ProvenGoneToolRan (a Claude PostToolUse or PostToolUseFailure:
	// the requests carrying ToolUseID; never written for a Codex row, whose
	// requests carry no tool_use_id) or ProvenGoneTurnEnd (the main agent's
	// Stop or idle-prompt Notification, or a Codex main-agent Interrupt:
	// every durably eligible request recorded before RecordedBefore).
	How string
	// ToolUseID is, for ProvenGoneToolRan, the hook's tool_use_id.
	ToolUseID string
	// RecordedBefore is, for ProvenGoneTurnEnd, the instant the hook started
	// (the hook package's HandleConfig.Start, read first thing in the hook
	// process): the proof covers only requests whose created_at is strictly
	// before its whole second (b.omt). It is required for ProvenGoneTurnEnd
	// (ProveRequestsGone returns errTurnEndNoStart when it is zero) and
	// unused for ProvenGoneToolRan.
	RecordedBefore time.Time
	// At is the hook's clock reading, recorded as proven_gone_at; the zero
	// time reads time.Now.
	At time.Time
}

// errUnknownProof is ProveRequestsGone's error for a RequestProof whose How
// is neither ProvenGoneToolRan nor ProvenGoneTurnEnd.
var errUnknownProof = errors.New("store: prove requests gone: unknown proof")

// errTurnEndNoStart is ProveRequestsGone's error for a ProvenGoneTurnEnd
// proof with no RecordedBefore: without the hook's start it cannot tell the
// requests its turn end covers from a later turn's (b.omt), so it proves
// nothing.
var errTurnEndNoStart = errors.New("store: prove requests gone: turn_end proof without the hook's start")

// match returns the WHERE fragment, on a permission_requests row named pr,
// that selects the requests p proves gone, with its arguments; ok is false
// when p proves nothing (a tool_ran proof with no tool_use_id).
//
// A turn_end proof's fragment carries the cut pr.created_at < ?, its argument
// p.RecordedBefore in the store's CURRENT_TIMESTAMP text (storeTimestamp: UTC,
// truncated to the whole second), the form every permission_requests insert
// leaves in created_at (its DEFAULT CURRENT_TIMESTAMP), so the text
// comparison is a time comparison. A request created in the second the hook
// started, or later, is not covered: one recorded just before the hook in
// that second stays unproven until a later proof (the safe direction).
func (p RequestProof) match() (frag string, args []any, ok bool, err error) {
	switch p.How {
	case ProvenGoneToolRan:
		if p.ToolUseID == "" {
			return "", nil, false, nil
		}
		return `pr.tool_use_id = ?`, []any{p.ToolUseID}, true, nil
	case ProvenGoneTurnEnd:
		if p.RecordedBefore.IsZero() {
			return "", nil, false, errTurnEndNoStart
		}
		return `pr.main_turn_end_eligible = 1 AND pr.agent_id IS NULL AND pr.created_at < ?`, []any{storeTimestamp(p.RecordedBefore)}, true, nil
	}
	return "", nil, false, fmt.Errorf("%w %q", errUnknownProof, p.How)
}

// ProveRequestsGone records proof on instanceID's permission requests (b.146
// step 2c): proven_gone_at proof.At and proven_gone_how proof.How on every
// request not yet proven gone that proof covers:
//
//   - ProvenGoneToolRan: the requests whose tool_use_id is proof.ToolUseID
//     (the tool ran, so its dialog was answered and is gone), whether or not
//     agent-director's records say they were answered, and whatever their
//     agent_id. An empty ToolUseID proves nothing.
//   - ProvenGoneTurnEnd: requests with MainTurnEndEligible set and no child
//     identity, written before this hook started. Legacy non-Cursor main
//     requests preserve their eligibility. Cursor unknown origin and child
//     requests remain ineligible even without an AgentID: missing identity is
//     no proof of main origin, and background work can outlive the main turn.
//
// "Written before this hook started" is two cuts, both in the read and in
// the write. The first is the hook's start (proof.RecordedBefore): only a
// request whose created_at is strictly before the second the hook started
// is covered (match; b.omt). A hook can run late: an idle-prompt
// Notification whose read lands after the next turn's PermissionRequest
// would otherwise prove that request gone while its dialog is up, and, as a
// request proven gone holds no working transition (holdsRowSQL), a
// subagent's working hook would then move the row to working with the
// dialog on screen. The second is the newest such request id, taken by one
// read before the write waits for the lock; the write then covers no request
// with a higher id, so a request recorded while this hook waits for the
// write lock (a later turn's) is not proven by it. A turn_end proof with no
// RecordedBefore is an error (errTurnEndNoStart) and proves nothing.
//
// The write is one statement, gated as every hook write is (SR-22.9;
// hookGateSQL on the Spawn's row in its own WHERE): a hook whose parent is not
// the row's recorded pane process proves nothing. It waits for the write lock
// as every hook write does (the store's busy timeout), and writes nothing when
// the read found no such request. It returns how many requests it proved. It
// emits no trail event: the hook's ad.hook.fired records the hook.
func (s *Store) ProveRequestsGone(instanceID string, gate HookGate, proof RequestProof) (int64, error) {
	const errPrefix = "store: prove requests gone"
	frag, matchArgs, ok, err := proof.match()
	if err != nil || !ok {
		return 0, err
	}
	// Cursor tool execution requires validated ACP output and exact request
	// authority through ProveCursorToolRan, never a generic hook tool id.
	toolGate := ""
	if proof.How == ProvenGoneToolRan {
		toolGate = "agent IS NOT 'cursor' AND "
	}
	var newest sql.NullInt64
	read := `SELECT MAX(pr.request_id) FROM permission_requests pr
	  WHERE pr.instance_id = ? AND pr.proven_gone_at IS NULL AND ` + frag
	if err := s.db.QueryRow(read, append([]any{instanceID}, matchArgs...)...).Scan(&newest); err != nil {
		return 0, fmt.Errorf("%s: read: %w", errPrefix, err)
	}
	if !newest.Valid {
		return 0, nil
	}
	at := proof.At
	if at.IsZero() {
		at = time.Now()
	}
	write := `UPDATE permission_requests AS pr
	    SET proven_gone_at = ?, proven_gone_how = ?
	  WHERE pr.instance_id = ? AND pr.proven_gone_at IS NULL AND ` + frag + ` AND pr.request_id <= ?
	    AND EXISTS (SELECT 1 FROM spawns WHERE instance_id = ? AND ` + toolGate + hookGateSQL + `)`
	args := append([]any{millisArg(at), proof.How, instanceID}, matchArgs...)
	args = append(args, newest.Int64, instanceID)
	args = append(args, hookGateArgs(gate)...)
	res, err := s.db.Exec(write, args...)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", errPrefix, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("%s rows affected: %w", errPrefix, err)
	}
	return n, nil
}
