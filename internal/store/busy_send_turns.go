package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
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
//     (prompt_sighting). It releases the AD-owned-work gate and, as a force
//     prompt's last missing admission, finishes a pending force
//     (settleBusySendForceEvidence); it changes no custody, order, transport
//     or Message Result, is never delivered_confirmed and never a proof that
//     sibling hooks let the prompt through. Claude ordinary delivery goes only
//     through its asyncRewake wake (Main Agent decision 10), so no turn
//     counting stands in for a sighting. Input Claude took into its running
//     turn at a tool boundary has no sighting: its take evidence is the
//     parent Stop that ends that turn, under O4's turn-end premise, when the
//     prompt's completion was committed before that Stop's entry read and its
//     output observed before the Stop's start (SR-3.3;
//     ReadBusySendTurnEndPrompts, RecordBusySendTurnEnd), recorded as
//     native_signal. A wake or force prompt never takes this admission
//     evidence. O4/G3 separately releases a completed wake's order barrier
//     at that later parent Stop, whether or not it already has a sighting.

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
// awaiting obligation (SR-5.6's awaiting_turn_start), zero once the Launch's
// End is recorded: Launch End finalizes their Messages and no evidence of
// the ended Launch's intake is awaited any more (decision 11). Input issued
// into a running turn at a tool boundary counts, whatever its transport, until
// a parent turn end whose entry read found its completion committed
// (RecordBusySendTurnEnd). It only reads.
func readBusySendAwaiting(ctx context.Context, q busySendLaunchSQL, conversation, launchID string) (int64, error) {
	var n int64
	err := q.QueryRowContext(ctx, `SELECT COUNT(DISTINCT p.handover_id) FROM busy_send_prompts p
	    JOIN busy_send_handovers h ON h.handover_id = p.handover_id
	    JOIN busy_send_launches l ON l.launch_id = h.launch_id AND l.ended_at_ms IS NULL
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
// inert), which lifts the AD-owned-work gate and, when it is a pending
// force's last missing admission, makes that force handed_over
// (settleBusySendForceEvidence); no custody, transport, order or Message
// Result changes, and nothing is delivered_confirmed. Two or more
// equal bodies, or a candidate whose text is gone, bind none. A sighting that
// arrives before its prompt's completion record binds the same way; neither
// the sighting nor completion releases a wake's ordinary order barrier.
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

// BusySendTurnEnd is a route's own native turn end of the agent's main turn
// (SR-3.3): for Claude, its owned parent Stop hook, with no agent_id and a
// payload session that agrees with its transcript's.
type BusySendTurnEnd struct {
	InstanceID string
	// Gate is the reporting hook's parent gate; the turn end counts only
	// while it holds, judged in each read and write's own transaction.
	Gate HookGate
	// Destination is the current Launch's destination the hook authenticated
	// (its Launch, token, provider, session, route and connection
	// generation); each read and write compares it with the current facts
	// again.
	Destination BusySendDestination
	// At is the callback's own observation, its start, captured before any
	// store call. Zero, or a time at or before the epoch, grants nothing: no
	// later time stands in for it.
	At time.Time
	// Prompts are what the turn end's entry read found
	// (ReadBusySendTurnEndPrompts): the prompts whose completion was
	// committed before that read's snapshot, with output observed before At.
	// Only these, still at the same revision, can gain admission or wake order.
	Prompts []BusySendTurnEndPrompt
}

// BusySendTurnEndPrompt is one prompt a turn end's entry read found it could
// admit (steer) or order (wake), at its revision then.
type BusySendTurnEndPrompt struct {
	HandoverID  string
	PromptIndex int
	Revision    int64
}

// BusySendTurnEndOutcome is what a turn end established.
type BusySendTurnEndOutcome string

const (
	// BusySendTurnEndRecorded: admission or wake order was recorded for at
	// least one prompt (BusySendTurnEndResult.Recorded).
	BusySendTurnEndRecorded BusySendTurnEndOutcome = "recorded"
	// BusySendTurnEndUnchanged: the turn end is the current Launch's, and no
	// prompt of its entry read still qualifies, so nothing changed.
	BusySendTurnEndUnchanged BusySendTurnEndOutcome = "unchanged"
	// BusySendTurnEndStale: no observation, the hook's gate does not hold, or
	// the current Launch is not a covered, running Claude hooks Launch at
	// the turn end's destination.
	BusySendTurnEndStale BusySendTurnEndOutcome = "stale"
)

// BusySendTurnEndResult is a turn end's outcome and how many prompts gained
// admission or wake order.
type BusySendTurnEndResult struct {
	Outcome  BusySendTurnEndOutcome
	Recorded int
}

// busySendTurnEndWhere selects a conversation and Launch's prompts a turn end
// can admit (ordinary tool-boundary input) or order (wakes, O4/G3). After
// those two placeholders come eight more: the turn end's observation in
// milliseconds, then the captured destination's launch token, session,
// provider, route kind, route id and connection generation, which must be
// the turn end's, and the store. Every selected prompt has complete output
// (transport written) observed strictly before the turn end. A sighted wake
// still needs order evidence, so its admission does not exclude it.
const busySendTurnEndWhere = `h.conversation_id = ? AND h.launch_id = ? AND p.negative_kind IS NULL
    AND ((h.boundary = 'tool_boundary' AND h.forced = 0 AND h.intent_running_turn = 1 AND p.admission_kind IS NULL)
        OR (h.boundary IN ('turn_end', 'force') AND p.order_kind IS NULL))
    AND p.emission = 'issued' AND p.transport = 'written' AND p.transport_at_ms < ?
    AND COALESCE(h.launch_token, '') = ? AND COALESCE(h.session_id, '') = ? AND h.provider = ? AND h.route_kind = ?
    AND COALESCE(h.route_id, '') = ? AND h.connection_generation = ? AND h.store_id = ?`

// observed is the turn end's observation in milliseconds, and false when it
// has none or names no session, so it grants nothing.
func (in BusySendTurnEnd) observed() (int64, bool) {
	ms := in.At.UnixMilli()
	return ms, !in.At.IsZero() && ms > 0 && in.Destination.SessionID != ""
}

// ReadBusySendTurnEndPrompts is a turn end's entry read (SR-3.3; E4 Main
// Agent decision 2), made at its callback's entry before its own write and
// any wait on another writer. In one read transaction it checks in's authority
// as RecordBusySendTurnEnd does and returns the prompts that write could
// admit or order, each at its revision: those whose completion was committed before
// the read's SQLite snapshot, with an output observation strictly earlier
// than in.At, the callback's start, which was captured separately and before
// the snapshot. A read sees only committed transactions, so a completion that
// commits after the snapshot is never among them, even one whose output was
// observed earlier; it waits for a later turn end. That exclusion is the one
// unconditional guarantee. A completion committed between the callback's
// start and the snapshot is included when its output observation is earlier
// than in.At, and that comparison relies on the host's wall clock: a clock
// step can misorder an output against the callback's start. No observation,
// no authority or a missing row returns none. It writes nothing.
func (s *Store) ReadBusySendTurnEndPrompts(ctx context.Context, in BusySendTurnEnd) ([]BusySendTurnEndPrompt, error) {
	if _, ok := in.observed(); !ok {
		return nil, nil
	}
	var out []BusySendTurnEndPrompt
	err := s.WithContext(ctx).readWithin(DefaultLockWait, "", func(ctx context.Context, conn *sql.Conn) error {
		if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
			return err
		}
		defer rollbackConn(ctx, conn)
		out = nil
		f, ok, err := readBusySendTurnEndAuthority(ctx, conn, in)
		if err != nil || !ok {
			return err
		}
		out, err = readBusySendTurnEndPrompts(ctx, conn, s.StoreID(), f, in)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("store: read busy-send turn end: %w", err)
	}
	return out, nil
}

// RecordBusySendTurnEnd applies a route's own turn end as take evidence for
// the input it issued into that running turn, or order evidence for a wake
// (SR-3.3, SR-5.6, O4/G3; E4 Main Agent
// decisions 1 and 2), in one transaction on a WAL synchronous=FULL connection
// bounded by ctx. Inside it: the hook's gate must hold (hookGateSQL), and the
// published current Launch must be covered, running, route claude_hooks (the
// one route whose turn end is qualified evidence, under O4's turn-end premise)
// and exactly in.Destination. Then each prompt of in.Prompts, the turn end's
// entry read (ReadBusySendTurnEndPrompts), that still qualifies at the same
// revision gets its admission recorded as native_signal (ordinary tool-boundary
// input) or its order secured as ordered_acceptance (wake) at in.At. A prompt
// qualifies when it awaits the relevant evidence for that Launch, was captured for this same
// destination (a callback cannot admit another Launch's or session's input,
// even in a reused provider session), and its complete output (transport
// written) was observed strictly before in.At's millisecond.
//
// So two facts order the output before the turn end: the entry read, which
// saw the completion committed before its snapshot, and the output's
// observation, earlier than in.At, the callback's start. A completion that
// committed after the entry read's snapshot, output observed in the same
// millisecond or later, no completion yet, or partial or uncertain output
// gives nothing: such a prompt stays awaiting until a later turn end, or
// Launch End reads it zero. A repeat is inert. Only the exclusion of
// completions committed after the snapshot is unconditional, since it rests
// on commit order. The time comparison relies on the host's wall clock
// agreeing across hook processes, a known limit: a clock step can misorder
// an output against the callback's start, so an output observed after that
// start but committed before the snapshot can count, or one observed before
// it can be withheld.
//
// For tool-boundary input it changes only admission and the awaiting count.
// For a wake it releases issued custody and order, keeping Final Results,
// admission and force outcome unchanged: a sighting remains the wake's only
// admission evidence. Neither case grants output or replay authority. The
// ordinary Wait Turn AD-owned-work rule still checks the awaiting count after
// the order barrier clears. A failed read or write records nothing.
func (s *Store) RecordBusySendTurnEnd(ctx context.Context, in BusySendTurnEnd) (BusySendTurnEndResult, error) {
	out := BusySendTurnEndResult{Outcome: BusySendTurnEndStale}
	observed, ok := in.observed()
	if !ok {
		return out, nil
	}
	entry := make(map[BusySendTurnEndPrompt]bool, len(in.Prompts))
	for _, p := range in.Prompts {
		entry[p] = true
	}
	err := s.inDurableWriteTxContext(ctx, DefaultLockWait, func(ctx context.Context, conn *sql.Conn) error {
		out = BusySendTurnEndResult{Outcome: BusySendTurnEndStale}
		f, ok, err := readBusySendTurnEndAuthority(ctx, conn, in)
		if err != nil || !ok {
			return err
		}
		qualified, err := readBusySendTurnEndPrompts(ctx, conn, s.StoreID(), f, in)
		if err != nil {
			return err
		}
		out = BusySendTurnEndResult{Outcome: BusySendTurnEndUnchanged}
		for _, p := range qualified {
			if !entry[p] {
				continue
			}
			h, err := readBusySendHandover(ctx, conn, s.StoreID(), p.HandoverID)
			if err != nil {
				return err
			}
			if h.claudeWake() {
				// O4's fallback supplies order only. A force still needs its
				// own exact sighting for admission, and ordinary Wait Turn's
				// awaiting obligation is unchanged (SR-3.3, SR-4.3).
				result, err := recordBusySendEvidence(ctx, conn, s.StoreID(), BusySendPromptEvidence{
					HandoverID: p.HandoverID, PromptIndex: p.PromptIndex, LaunchID: f.launch,
					ConnectionGeneration: h.Destination.ConnectionGeneration,
					Order:                BusySendOrderOrderedAcceptance, UnprovenBoundary: true,
				}, observed)
				if err != nil {
					return err
				}
				if result.Recorded {
					out.Recorded++
				}
				continue
			}
			if err := busySendChange(ctx, conn, `UPDATE busy_send_prompts SET admission_kind = ?, admitted_at_ms = ?, revision = revision + 1
			    WHERE handover_id = ? AND prompt_index = ? AND revision = ? AND admission_kind IS NULL AND negative_kind IS NULL`,
				string(BusySendAdmissionNativeSignal), observed, p.HandoverID, p.PromptIndex, p.Revision); err != nil {
				return err
			}
			out.Recorded++
		}
		if out.Recorded > 0 {
			out.Outcome = BusySendTurnEndRecorded
		}
		return nil
	})
	if err != nil {
		return BusySendTurnEndResult{}, err
	}
	return out, nil
}

// readBusySendTurnEndAuthority is a turn end's authority, judged in its
// caller's transaction: the hook's gate holds, and the published current
// Launch is covered, running, route claude_hooks and exactly in.Destination.
// ok is false, with no error, for any of those that fails or a missing row.
func readBusySendTurnEndAuthority(ctx context.Context, conn *sql.Conn, in BusySendTurnEnd) (busySendIssueFacts, bool, error) {
	var gate bool
	err := conn.QueryRowContext(ctx, `SELECT CASE WHEN `+hookGateSQL+` THEN 1 ELSE 0 END FROM spawns WHERE instance_id = ?`,
		append(hookGateArgs(in.Gate), in.InstanceID)...).Scan(&gate)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !gate {
		return busySendIssueFacts{}, false, nil
	}
	if err != nil {
		return busySendIssueFacts{}, false, err
	}
	f, err := readBusySendIssueFacts(ctx, conn, in.InstanceID)
	if errors.Is(err, ErrSpawnNotFound) {
		return f, false, nil
	}
	if err != nil {
		return f, false, err
	}
	ok := f.launchCoverage == BusySendCovered && f.launchWait() == "" && f.routeKind == string(BusySendRouteClaudeHooks) &&
		f.destinationMatches(in.Destination)
	return f, ok, nil
}

// readBusySendTurnEndPrompts reads the prompts of f's Launch a turn end at
// in.At can admit (busySendTurnEndWhere), in hand-over and prompt order, each
// at its current revision.
func readBusySendTurnEndPrompts(ctx context.Context, conn *sql.Conn, storeID string, f busySendIssueFacts, in BusySendTurnEnd) ([]BusySendTurnEndPrompt, error) {
	d := in.Destination
	rows, err := conn.QueryContext(ctx, `SELECT p.handover_id, p.prompt_index, p.revision FROM busy_send_prompts p
	    JOIN busy_send_handovers h ON h.handover_id = p.handover_id
	    WHERE `+busySendTurnEndWhere+` ORDER BY h.handover_seq, p.prompt_index`,
		f.conversation, f.launch, in.At.UnixMilli(), d.LaunchToken, d.SessionID, d.Provider, string(d.RouteKind), d.RouteID,
		d.ConnectionGeneration, storeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BusySendTurnEndPrompt
	for rows.Next() {
		var p BusySendTurnEndPrompt
		if err := rows.Scan(&p.HandoverID, &p.PromptIndex, &p.Revision); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
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
