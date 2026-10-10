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
// not what Claude Code shows; only Claude Code's own hooks, or the end of its
// agent, prove the dialog gone. Until then the request holds plain send-keys
// to its Spawn. The third proof, agent_gone, is written by the close of a
// Spawn's requests (closeOrphanedRequests).

// RequestProof is what one hook of a Spawn's agent proves gone (b.146 step
// 2c).
type RequestProof struct {
	// How is ProvenGoneToolRan (a PostToolUse or PostToolUseFailure: the
	// requests carrying ToolUseID) or ProvenGoneTurnEnd (the main agent's Stop
	// or idle-prompt Notification: every request with no agent_id).
	How string
	// ToolUseID is, for ProvenGoneToolRan, the hook's tool_use_id.
	ToolUseID string
	// At is the hook's clock reading, recorded as proven_gone_at; the zero
	// time reads time.Now.
	At time.Time
}

// errUnknownProof is ProveRequestsGone's error for a RequestProof whose How
// is neither ProvenGoneToolRan nor ProvenGoneTurnEnd.
var errUnknownProof = errors.New("store: prove requests gone: unknown proof")

// match returns the WHERE fragment, on a permission_requests row named pr,
// that selects the requests p proves gone, with its arguments; ok is false
// when p proves nothing (a tool_ran proof with no tool_use_id).
func (p RequestProof) match() (frag string, args []any, ok bool, err error) {
	switch p.How {
	case ProvenGoneToolRan:
		if p.ToolUseID == "" {
			return "", nil, false, nil
		}
		return `pr.tool_use_id = ?`, []any{p.ToolUseID}, true, nil
	case ProvenGoneTurnEnd:
		return `pr.agent_id IS NULL`, nil, true, nil
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
//   - ProvenGoneTurnEnd: the requests with no agent_id (the main agent's, and
//     every request recorded before schema v7), written before this hook: the
//     main agent's turn ended after them, so none of their dialogs is left. A
//     request of a subagent (agent_id set) is never proven by it: a
//     background subagent can outlive the main agent's turn.
//
// "Written before this hook" is decided first, by one read that takes the
// newest such request id before the write waits for the lock; the write then
// covers no request with a higher id, so a request recorded while this hook
// waits for the write lock (a later turn's) is not proven by it.
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
	var newest sql.NullInt64
	read := `SELECT MAX(pr.request_id) FROM permission_requests pr
	  WHERE pr.claude_instance_id = ? AND pr.proven_gone_at IS NULL AND ` + frag
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
	  WHERE pr.claude_instance_id = ? AND pr.proven_gone_at IS NULL AND ` + frag + ` AND pr.request_id <= ?
	    AND EXISTS (SELECT 1 FROM spawns WHERE claude_instance_id = ? AND ` + hookGateSQL + `)`
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
