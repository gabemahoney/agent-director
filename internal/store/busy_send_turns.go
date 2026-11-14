package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"time"
)

// Turn evidence and AD-owned work (SR-3.3, SR-3.5, SR-5.6; M2 P-C4.2;
// b.7si t1.7si.nc SR-1.4). Three facts the store already keeps carry them,
// with no record of their own:
//
//   - The Turn Start counter, spawns.turn_starts: an owned main-agent Claude
//     UserPromptSubmit advances it in the same statement as its applied or
//     permission-held write (claudeTurnStartSet, claudeHeldTurnSQL). A claim
//     captures it and the issue commit compares it, so a turn started since
//     the claim refuses the issue (turn_changed).
//   - The awaiting obligation: a prompt of a Launch whose emission intent is
//     persisted (CommitBusySendIssue writes it, in the transaction that may
//     authorize output) and that has neither admission nor negative evidence.
//     It exists before any output can, whatever its transport turns out to
//     be, so a crash between the issue commit and the output, or between the
//     output and its completion record, never reads as zero. Its count for the
//     current Launch is delivery.awaiting_turn_start (readBusySendAwaiting);
//     it is zero once the Launch has ended. While it is not zero, an ordinary
//     Wait Turn claim is refused with readiness_unknown (SR-3.3's AD-owned-work
//     rule): turn-end evidence is not idleness while AD's own input may still
//     be pending at the provider.
//   - Take evidence: for Claude, AD's own UserPromptSubmit sighting of a wake
//     prompt's exact body, uniquely bound to one awaiting prompt of the
//     current Launch, session and connection generation
//     (RecordBusySendSighting), recorded as that prompt's admission
//     (prompt_sighting). It releases only the AD-owned-work gate: it is never
//     delivered_confirmed, never a proof that sibling hooks let the prompt
//     through, and it changes no Result. Claude ordinary delivery goes only
//     through its asyncRewake wake (Main Agent decision 10), so no turn
//     counting stands in for a sighting.

// claudeTurnStartSQL is the condition, on a spawns row, under which an owned
// main-agent Claude UserPromptSubmit advances the Turn Start counter: the row
// is a Claude row (a legacy empty agent is Claude's) and the hook's session
// (its one placeholder) is the row's recorded session, or the row records
// none yet. Another provider's row, or a report from another session, never
// advances it.
const claudeTurnStartSQL = `agent IN ('claude', '') AND (COALESCE(session_id, '') = '' OR session_id = ?)`

// claudeTurnStartSet is the SET fragment, with its argument, that advances
// the Turn Start counter in a gated hook write's own statement
// (gatedHookUpdate) for an owned main-agent Claude UserPromptSubmit
// (HookGate.ClaudeTurnStart), or "" for any other hook. It reads the row's
// values before the statement's other assignments, as every SET term does,
// so a session recorded by the same statement does not decide it. The
// fragment ends with a comma.
func claudeTurnStartSet(g HookGate) (string, []any) {
	if !g.ClaudeTurnStart {
		return "", nil
	}
	return `turn_starts = turn_starts + CASE WHEN ` + claudeTurnStartSQL + ` THEN 1 ELSE 0 END, `, []any{g.SessionID}
}

// claudeHeldTurnSQL is the one write of a held working transition
// (reportWorkingHeld) for an owned main-agent Claude UserPromptSubmit:
// idle_since NULLed and the Turn Start counter advanced as an applied write
// advances it (claudeTurnStartSet), with the version advance, only on a row
// whose gate holds and that has idle_since set or counts this turn start. The
// state, its settle columns and permission_epoch are kept. Its placeholders
// take the hook's session id, the instance id, the session id again and
// hookGateArgs.
const claudeHeldTurnSQL = `UPDATE spawns
    SET ` + idleClear + `, turn_starts = turn_starts + CASE WHEN ` + claudeTurnStartSQL + ` THEN 1 ELSE 0 END, ` + rowVersionAdvance + `
  WHERE instance_id = ? AND (idle_since IS NOT NULL OR (` + claudeTurnStartSQL + `)) AND ` + hookGateSQL

// busySendAwaitingWhere selects a Launch's awaiting obligations among its
// conversation's prompts (two placeholders: the conversation and the
// Launch): emission intent persisted, and no admission or negative evidence
// recorded. Its conversation term uses the hand-overs' (conversation_id,
// handover_seq) index.
const busySendAwaitingWhere = `h.conversation_id = ? AND h.launch_id = ? AND p.emission IN ('intent', 'issued')
    AND p.admission_kind IS NULL AND p.negative_kind IS NULL`

// readBusySendAwaiting counts launchID's hand-overs with at least one
// awaiting obligation (SR-5.6's awaiting_turn_start before Launch End; the
// caller reads zero for an ended Launch). It only reads.
func readBusySendAwaiting(ctx context.Context, q busySendLaunchSQL, conversation, launchID string) (int64, error) {
	var n int64
	err := q.QueryRowContext(ctx, `SELECT COUNT(DISTINCT p.handover_id) FROM busy_send_prompts p
	    JOIN busy_send_handovers h ON h.handover_id = p.handover_id
	    WHERE `+busySendAwaitingWhere, conversation, launchID).Scan(&n)
	return n, err
}

// BusySendSighting is a route's own sighting, in the agent's input, of a
// prompt body AD may have issued (SR-3.3, SR-3.5): for Claude, its owned
// main-agent UserPromptSubmit hook, with the route's measured wrapping and
// escaping already undone exactly. A text whose undoing is not exact gives no
// sighting at all.
type BusySendSighting struct {
	InstanceID string
	// Gate is the reporting hook's parent gate; the sighting counts only
	// while it holds, judged in the sighting's own transaction.
	Gate HookGate
	// SessionID is the provider session the prompt was seen in.
	SessionID string
	// Body is the exact prompt body seen.
	Body []byte
	// At is the sighting's time; zero is now.
	At time.Time
}

// BusySendSightingOutcome is what a sighting established.
type BusySendSightingOutcome string

const (
	// BusySendSightingAdmitted: the body is that of exactly one awaiting
	// prompt of the current Launch, captured for this session and connection
	// generation; its admission is recorded.
	BusySendSightingAdmitted BusySendSightingOutcome = "admitted"
	// BusySendSightingUnmatched: no awaiting prompt has this body.
	BusySendSightingUnmatched BusySendSightingOutcome = "unmatched"
	// BusySendSightingAmbiguous: two or more awaiting prompts have this body,
	// or one cannot be compared (its text is gone), so the sighting binds to
	// none (SR-3.5: AD never guesses).
	BusySendSightingAmbiguous BusySendSightingOutcome = "ambiguous"
	// BusySendSightingStale: the hook's gate does not hold, or the current
	// Launch is not covered or running, or the sighting's session or the
	// bound prompt's destination is not the current one.
	BusySendSightingStale BusySendSightingOutcome = "stale"
)

// BusySendSightingResult names the prompt an admitted sighting bound to.
type BusySendSightingResult struct {
	Outcome     BusySendSightingOutcome
	HandoverID  string
	PromptIndex int
}

// RecordBusySendSighting applies a route's sighting as take evidence (SR-3.3,
// SR-3.5) in one transaction on a WAL synchronous=FULL connection bounded by
// ctx. Inside it: the hook's gate must hold (hookGateSQL) and the published
// current Launch must be covered, running and in the sighting's session; the
// candidates are that Launch's awaiting prompts, each body rendered again from
// its Messages' retained text. Exactly one equal body binds: if its captured
// session and connection generation are the current ones, its admission is
// recorded (prompt_sighting, through the evidence writer, so a repeat is
// inert), which only lifts the AD-owned-work gate; no custody, transport,
// order or Result changes, and nothing is delivered_confirmed. Two or more
// equal bodies, or a candidate whose text is gone, bind none. A sighting that
// arrives before its prompt's completion record binds the same way; the
// completion still secures order when it lands.
func (s *Store) RecordBusySendSighting(ctx context.Context, in BusySendSighting) (BusySendSightingResult, error) {
	out := BusySendSightingResult{Outcome: BusySendSightingUnmatched}
	if len(in.Body) == 0 {
		return out, nil
	}
	if in.SessionID == "" {
		out.Outcome = BusySendSightingStale
		return out, nil
	}
	err := s.inDurableWriteTxContext(ctx, DefaultLockWait, func(ctx context.Context, conn *sql.Conn) error {
		now := busySendAt(in.At)
		out = BusySendSightingResult{Outcome: BusySendSightingStale}
		var gate bool
		err := conn.QueryRowContext(ctx, `SELECT CASE WHEN `+hookGateSQL+` THEN 1 ELSE 0 END FROM spawns WHERE instance_id = ?`,
			append(hookGateArgs(in.Gate), in.InstanceID)...).Scan(&gate)
		if errors.Is(err, sql.ErrNoRows) || err == nil && !gate {
			return nil
		}
		if err != nil {
			return err
		}
		f, err := readBusySendIssueFacts(ctx, conn, in.InstanceID)
		if errors.Is(err, ErrSpawnNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if f.launchCoverage != BusySendCovered || f.launchWait() != "" || f.sessionID != in.SessionID {
			return nil
		}
		candidates, err := readBusySendAwaitingPrompts(ctx, conn, s.StoreID(), f.conversation, f.launch)
		if err != nil {
			return err
		}
		var hit *busySendAwaitingPrompt
		for i := range candidates {
			c := &candidates[i]
			switch {
			case !c.comparable:
				out.Outcome = BusySendSightingAmbiguous
				return nil
			case !bytes.Equal(c.body, in.Body):
			case hit != nil:
				out.Outcome = BusySendSightingAmbiguous
				return nil
			default:
				hit = c
			}
		}
		if hit == nil {
			out.Outcome = BusySendSightingUnmatched
			return nil
		}
		if hit.destination.SessionID != in.SessionID || hit.destination.ConnectionGeneration != f.connectionGeneration {
			return nil
		}
		if _, err := recordBusySendEvidence(ctx, conn, s.StoreID(), BusySendPromptEvidence{HandoverID: hit.handoverID,
			PromptIndex: hit.index, LaunchID: f.launch, ConnectionGeneration: hit.destination.ConnectionGeneration,
			Admission: BusySendAdmissionPromptSighting}, now); err != nil {
			return err
		}
		out = BusySendSightingResult{Outcome: BusySendSightingAdmitted, HandoverID: hit.handoverID, PromptIndex: hit.index}
		return nil
	})
	if err != nil {
		return BusySendSightingResult{}, err
	}
	return out, nil
}

// busySendAwaitingPrompt is one awaiting prompt as a sighting compares it:
// its identity, its hand-over's captured destination and its body rendered
// from its Messages' retained text (comparable false when a text is gone).
type busySendAwaitingPrompt struct {
	handoverID  string
	index       int
	destination BusySendDestination
	body        []byte
	comparable  bool
}

// readBusySendAwaitingPrompts reads launchID's awaiting prompts in hand-over
// and prompt order, each with its rendered body.
func readBusySendAwaitingPrompts(ctx context.Context, conn *sql.Conn, storeID, conversation, launchID string) ([]busySendAwaitingPrompt, error) {
	rows, err := conn.QueryContext(ctx, `SELECT p.handover_id, p.prompt_index FROM busy_send_prompts p
	    JOIN busy_send_handovers h ON h.handover_id = p.handover_id
	    WHERE `+busySendAwaitingWhere+` ORDER BY h.handover_seq, p.prompt_index`, conversation, launchID)
	if err != nil {
		return nil, err
	}
	var out []busySendAwaitingPrompt
	for rows.Next() {
		var p busySendAwaitingPrompt
		if err := rows.Scan(&p.handoverID, &p.index); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range out {
		h, err := readBusySendHandover(ctx, conn, storeID, out[i].handoverID)
		if err != nil {
			return nil, err
		}
		out[i].destination = h.Destination
		if out[i].body, out[i].comparable, err = readBusySendPromptBody(ctx, conn, out[i].handoverID, out[i].index); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// readBusySendPromptBody renders one prompt's body from its Messages'
// retained text in membership order (RenderBusySendPromptBody); comparable is
// false when any Message's text is gone.
func readBusySendPromptBody(ctx context.Context, conn *sql.Conn, handoverID string, index int) ([]byte, bool, error) {
	rows, err := conn.QueryContext(ctx, `SELECT m.text FROM busy_send_handover_members hm
	    JOIN busy_send_messages m ON m.message_id = hm.message_id
	    WHERE hm.handover_id = ? AND hm.prompt_index = ? ORDER BY hm.member_index`, handoverID, index)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var texts [][]byte
	comparable := true
	for rows.Next() {
		var text []byte
		if err := rows.Scan(&text); err != nil {
			return nil, false, err
		}
		if text == nil {
			comparable = false
		}
		texts = append(texts, text)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if !comparable || len(texts) == 0 {
		return nil, false, nil
	}
	return RenderBusySendPromptBody(texts), true, nil
}
