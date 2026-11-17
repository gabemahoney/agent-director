package storefix

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
)

// BusySendResultSeed seeds one confirmed Message and its Result for reader
// tests (reusable fixture). Retention tests invoke the real expiry writer.
// Pair is canonical: a lowercase
// hyphenated Caller UUID and a nonempty Message ID. A non-Final Result waits
// on Wait, whose Earlier names an earlier seed's pair; a Final one has
// final_at set (FinalAt overrides its default fixture timestamp).
// Expired seeds SR-5.5's tombstone: the pair and non-content
// facts, with no text, Result or receipt identity. Transport, when set,
// records the Message's one hand-over on the instance's current Launch, a
// single prompt whose observed transport it is; a non-Final seed is then
// issued in that open hand-over, no longer Held. Empty strings are NULL.
type BusySendResultSeed struct {
	InstanceID   string
	Pair         store.BusySendPair
	SendMode     store.BusySendMode // default wait_turn
	Class        store.BusySendResultClass
	Final        bool
	FinalAt      *time.Time
	Reason       string
	Forced       bool
	Wait         *store.BusySendWait
	LaunchToken  string
	SessionID    string
	EvidenceKind store.BusySendEvidenceKind
	RecordID     string
	RecordSHA256 string
	Expired      bool
	Transport    store.BusySendTransport
}

// SeedBusySendResult stores seed's Message in the instance's published
// conversation, next in its accepted order, and its pair ownership, in one
// raw transaction, and returns the Message's internal id. The instance row
// must exist; the records outlive it. Schema and migration tests own the
// tables' shape: this is fixture setup, not a second storage authority.
func SeedBusySendResult(t *testing.T, dbPath string, seed BusySendResultSeed) string {
	t.Helper()
	raw := openRawStore(t, dbPath, "SeedBusySendResult")
	defer func() { _ = raw.Close() }()
	tx, err := raw.Begin()
	if err != nil {
		t.Fatalf("storefix.SeedBusySendResult: begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	var conversation, storeID string
	var seq int64
	err = tx.QueryRow(`SELECT c.conversation_id, c.store_id, c.accepted_seq + 1 FROM spawns s
	    JOIN busy_conversations c ON c.conversation_id = s.busy_conversation_id WHERE s.instance_id = ?`,
		seed.InstanceID).Scan(&conversation, &storeID, &seq)
	if err != nil {
		t.Fatalf("storefix.SeedBusySendResult(%s): published conversation: %v", seed.InstanceID, err)
	}
	mode := seed.SendMode
	if mode == "" {
		mode = store.BusySendWaitTurn
	}
	const text = "fixture message"
	sum := sha256.Sum256([]byte(text))
	at := int64(1_700_000_000_000) + seq
	content := []any{[]byte(text), hex.EncodeToString(sum[:]), string(seed.Class), seed.Reason}
	place, retention, expiredAt, final := "held", string(store.BusySendRetained), any(nil), any(nil)
	if seed.Final || seed.Expired {
		place, final = "closed", at
		if seed.FinalAt != nil {
			final = seed.FinalAt.UnixMilli()
		}
	}
	if seed.Expired {
		content = []any{nil, nil, nil, nil}
		retention, expiredAt = string(store.BusySendExpired), at
	}
	var handover, current string
	if seed.Transport != "" {
		handover = uuid.NewString()
		if final == nil {
			place, current = "issued", handover
		}
	}
	var waitReason, waitSince, earlier any
	if w := seed.Wait; w != nil {
		waitReason, waitSince = string(w.Reason), w.SinceMillis
		if w.Earlier != nil {
			var id string
			if err := tx.QueryRow(`SELECT message_id FROM busy_send_results WHERE caller_uuid = ? AND delivery_request_id = ?`,
				w.Earlier.CallerUUID, w.Earlier.DeliveryRequestID).Scan(&id); err != nil {
				t.Fatalf("storefix.SeedBusySendResult: earlier pair %+v: %v", *w.Earlier, err)
			}
			earlier = id
		}
	}
	messageID := uuid.NewString()
	_, err = tx.Exec(`INSERT INTO busy_send_messages (message_id, store_id, conversation_id, instance_id, accepted_seq,
	        accepted_at_ms, send_mode, confirmed, text, text_bytes, text_sha256, place, handover_id, reserved, forced, result_class,
	        result_final, result_reason, wait_reason, wait_since_ms, wait_earlier_message_id, bound_launch_token,
	        bound_session_id, evidence_kind, record_id, record_sha256, final_at_ms, retention, expired_at_ms)
	    VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		messageID, storeID, conversation, seed.InstanceID, seq, at, string(mode), content[0], len(text), content[1],
		place, nullIfEmpty(current), seed.Forced, nullIfEmpty(content[2]), final != nil, nullIfEmpty(content[3]), waitReason, waitSince, earlier,
		nullIfEmpty(seed.LaunchToken), nullIfEmpty(seed.SessionID), nullIfEmpty(string(seed.EvidenceKind)),
		nullIfEmpty(seed.RecordID), nullIfEmpty(seed.RecordSHA256), final, retention, expiredAt)
	if err == nil && handover != "" {
		err = seedBusySendHandover(tx, storeID, conversation, seed.InstanceID, handover, messageID, at, len(text), seed.Transport, current != "")
	}
	if err == nil {
		_, err = tx.Exec(`INSERT INTO busy_send_results (caller_uuid, delivery_request_id, store_id, message_id, instance_id)
		    VALUES (?, ?, ?, ?, ?)`, seed.Pair.CallerUUID, seed.Pair.DeliveryRequestID, storeID, messageID, seed.InstanceID)
	}
	if err == nil {
		_, err = tx.Exec(`UPDATE busy_conversations SET accepted_seq = ? WHERE conversation_id = ?`, seq, conversation)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		t.Fatalf("storefix.SeedBusySendResult(%+v): %v", seed.Pair, err)
	}
	return messageID
}

// seedBusySendHandover records messageID's one-prompt hand-over, numbered next
// in conversation, on instanceID's current Launch, its prompt issued at at with
// transport observed; open while the Message is issued in it, else closed.
func seedBusySendHandover(tx *sql.Tx, storeID, conversation, instanceID, handover, messageID string, at int64, textBytes int,
	transport store.BusySendTransport, open bool) error {
	state, closed := "closed", any(at)
	if open {
		state, closed = "open", nil
	}
	r, err := tx.Exec(`INSERT INTO busy_send_handovers (handover_id, store_id, conversation_id, handover_seq, instance_id,
	        launch_id, launch_token, provider, route_kind, connection_generation, owner_revision, cutoff_seq, boundary,
	        race_exposed, forced, expected_turn_starts, permission_epoch, prompt_count, prompt_cursor, state,
	        claimed_at_ms, issue_deadline_ms, job_deadline_ms, closed_at_ms)
	    SELECT ?, ?, ?, (SELECT COUNT(*) + 1 FROM busy_send_handovers WHERE conversation_id = ?), instance_id,
	        launch_id, launch_token, provider, 'claude_hooks', 0, 1, 1, 'turn_end', 0, 0, 0, 0, 1, 1, ?, ?, ?, ?, ?
	    FROM busy_send_launches WHERE instance_id = ? AND phase = 'current'`,
		handover, storeID, conversation, conversation, state, at, at, at, closed, instanceID)
	if err == nil {
		var n int64
		if n, err = r.RowsAffected(); err == nil && n != 1 {
			err = fmt.Errorf("%d current Launches; want 1", n)
		}
	}
	if err == nil {
		rendered := len(store.BusySendSeparator(1, 1)) + textBytes
		_, err = tx.Exec(`INSERT INTO busy_send_prompts (handover_id, prompt_index, store_id, message_count, rendered_bytes,
		        emission, intent_at_ms, issued_at_ms, transport, transport_at_ms)
		    VALUES (?, 0, ?, 1, ?, 'issued', ?, ?, ?, ?)`, handover, storeID, rendered, at, at, string(transport), at)
	}
	if err == nil {
		_, err = tx.Exec(`INSERT INTO busy_send_handover_members (handover_id, member_index, message_id, prompt_index, store_id)
		    VALUES (?, 0, ?, 0, ?)`, handover, messageID, storeID)
	}
	return err
}

// ExpireBusySendForce seeds an ended force's expired lookup shape (reusable
// fixture). Tests of retention itself use Store.ExpireBusySendRetention.
func ExpireBusySendForce(t *testing.T, dbPath, forceID string, atMillis int64) {
	t.Helper()
	raw := openRawStore(t, dbPath, "ExpireBusySendForce")
	defer func() { _ = raw.Close() }()
	r, err := raw.Exec(`UPDATE busy_send_forces SET retention = 'expired', expired_at_ms = ?
	    WHERE force_id = ? AND ended_at_ms IS NOT NULL`, atMillis, forceID)
	if err == nil {
		var n int64
		if n, err = r.RowsAffected(); err == nil && n != 1 {
			t.Fatalf("storefix.ExpireBusySendForce(%s): %d ended attempts; want 1", forceID, n)
		}
	}
	if err != nil {
		t.Fatalf("storefix.ExpireBusySendForce(%s): %v", forceID, err)
	}
}

// SeedBusySendPauseClaim records a standing pause claim on instanceID's
// current Launch, as SR-6.5's pause call claims it before its signal, so the
// Launch's issue commits refuse (BusySendGuardPause) until the claim ends
// (reusable fixture). It writes raw, skipping the claim's checks, for a test
// that needs a fence on a Launch in any state; the real writer is
// store.ClaimBusySendPauseWithin, which claims only a waiting row with no
// turn pending.
func SeedBusySendPauseClaim(t *testing.T, dbPath, instanceID string) {
	t.Helper()
	raw := openRawStore(t, dbPath, "SeedBusySendPauseClaim")
	defer func() { _ = raw.Close() }()
	now := time.Now().UnixMilli()
	r, err := raw.Exec(`INSERT INTO busy_send_pause_claims (claim_id, store_id, instance_id, launch_id, owner_call_id,
	        claimed_at_ms, deadline_ms, signal_kind, state)
	    SELECT ?, store_id, instance_id, launch_id, ?, ?, ?, 'sigterm', 'standing'
	    FROM busy_send_launches WHERE instance_id = ? AND phase = 'current'`,
		uuid.NewString(), uuid.NewString(), now, now+(15*time.Second).Milliseconds(), instanceID)
	if err == nil {
		var n int64
		if n, err = r.RowsAffected(); err == nil && n != 1 {
			err = fmt.Errorf("%d current Launches; want 1", n)
		}
	}
	if err != nil {
		t.Fatalf("storefix.SeedBusySendPauseClaim(%s): %v", instanceID, err)
	}
}

// UnbindBusySend clears instanceID's published conversation binding
// (spawns.busy_conversation_id), as a damaged store would leave it, so a
// call about the current Launch must fail rather than decide (reusable
// fixture).
func UnbindBusySend(t *testing.T, dbPath, instanceID string) {
	t.Helper()
	raw := openRawStore(t, dbPath, "UnbindBusySend")
	defer func() { _ = raw.Close() }()
	if _, err := raw.Exec(`UPDATE spawns SET busy_conversation_id = NULL WHERE instance_id = ?`, instanceID); err != nil {
		t.Fatalf("storefix.UnbindBusySend(%s): %v", instanceID, err)
	}
}

// BusySendCounts is how many Messages, owned pairs, force attempts and input
// journal attempts a store holds.
type BusySendCounts struct{ Messages, Pairs, Forces, InputAttempts int }

// ReadBusySendCounts reads the store's BusySendCounts, so a refused call can
// show it stored no Message, pair, force attempt or journal acceptance
// (reusable fixture).
func ReadBusySendCounts(t *testing.T, dbPath string) BusySendCounts {
	t.Helper()
	raw := openRawStore(t, dbPath, "ReadBusySendCounts")
	defer func() { _ = raw.Close() }()
	var c BusySendCounts
	err := raw.QueryRow(`SELECT (SELECT count(*) FROM busy_send_messages), (SELECT count(*) FROM busy_send_results),
	    (SELECT count(*) FROM busy_send_forces), (SELECT count(*) FROM input_attempts)`).Scan(
		&c.Messages, &c.Pairs, &c.Forces, &c.InputAttempts)
	if err != nil {
		t.Fatalf("storefix.ReadBusySendCounts: %v", err)
	}
	return c
}

// ExitEvidence is instanceID's busy-send exit scope as s reads it, taken as
// positive exit evidence: the test judges every owned process in it gone, as
// resume, reuse and administrator deletion establish before they replace or
// retire a covered conversation (SR-6.2, SR-6.3). It is nil when the row has
// no scope to read (absent, or no published binding); an uncovered
// conversation ignores it (reusable fixture).
func ExitEvidence(t *testing.T, s *store.Store, instanceID string) *store.BusySendExitEvidence {
	t.Helper()
	return CoveredExit(s, instanceID)
}

// CoveredExit is ExitEvidence for a caller with no test handle, such as a
// store write a table of proxies runs (reusable fixture).
func CoveredExit(s *store.Store, instanceID string) *store.BusySendExitEvidence {
	scope, err := s.ReadBusySendExitScope(instanceID)
	if err != nil {
		return nil
	}
	return &store.BusySendExitEvidence{Scope: scope}
}

// WithExit is fresh carrying instanceID's ExitEvidence as its BusySendExit,
// unless it already carries one, as reuse hands it to ResetForReuse
// (reusable fixture).
func WithExit(t *testing.T, s *store.Store, instanceID string, fresh store.Spawn) store.Spawn {
	t.Helper()
	if fresh.BusySendExit == nil {
		fresh.BusySendExit = ExitEvidence(t, s, instanceID)
	}
	return fresh
}

// DeleteWithExit deletes instanceID's row as administrator deletion does:
// with ExitEvidence when its conversation is covered, else plainly
// (Store.DeleteSpawn). It returns the deletion's error (reusable fixture).
func DeleteWithExit(t *testing.T, s *store.Store, instanceID string) error {
	t.Helper()
	return DeleteCovered(s, instanceID)
}

// DeleteCovered is DeleteWithExit for a caller with no test handle
// (reusable fixture).
func DeleteCovered(s *store.Store, instanceID string) error {
	ev := CoveredExit(s, instanceID)
	if ev == nil || ev.Scope.Coverage != store.BusySendCovered {
		return s.DeleteSpawn(instanceID)
	}
	row, err := s.GetSpawn(instanceID)
	if err != nil {
		return err
	}
	res, err := s.DeleteBusySendCovered(instanceID, row.Snapshot, *ev)
	if err == nil && res != store.CondApplied {
		err = fmt.Errorf("storefix.DeleteWithExit(%s): %v; want applied", instanceID, res)
	}
	return err
}

// nullIfEmpty is v, or NULL for an empty string.
func nullIfEmpty(v any) any {
	if s, ok := v.(string); ok && s == "" {
		return nil
	}
	return v
}

// LaunchFence is one Launch's fence facts as its durable records hold them,
// for tests to inspect (repair sf2o-3 R2-2; production reads only the exit
// facts it judges): whether its Launch End is recorded, every process it owns
// (busy_send_launch_processes, in role order), and every intent-bearing
// prompt of its open hand-overs with no recorded order or negative outcome,
// in hand-over and prompt order.
type LaunchFence struct {
	Ended     bool
	Processes []store.BusySendLaunchProcess
	Prompts   []FencedPrompt
}

// FencedPrompt is one prompt of LaunchFence: its hand-over, index, captured
// destination, emission and transport, and whether its fence is recorded.
type FencedPrompt struct {
	HandoverID  string
	PromptIndex int
	Destination store.BusySendDestination
	Emission    store.BusySendEmission
	Transport   store.BusySendTransport
	Fenced      bool
}

// ReadLaunchFence reads launchID's LaunchFence from s's database through a
// separate raw connection (reusable fixture). An unknown Launch fails the
// test.
func ReadLaunchFence(t *testing.T, s *store.Store, launchID string) LaunchFence {
	t.Helper()
	raw := openRawStore(t, s.Path(), "ReadLaunchFence")
	defer func() { _ = raw.Close() }()
	fail := func(err error) {
		t.Helper()
		t.Fatalf("storefix.ReadLaunchFence(%s): %v", launchID, err)
	}
	var out LaunchFence
	var ended sql.NullInt64
	if err := raw.QueryRow(`SELECT ended_at_ms FROM busy_send_launches WHERE launch_id = ?`, launchID).Scan(&ended); err != nil {
		fail(err)
	}
	out.Ended = ended.Valid
	rows, err := raw.Query(`SELECT role, pid, start_time, pid_namespace, endpoint FROM busy_send_launch_processes
	    WHERE launch_id = ? ORDER BY role`, launchID)
	if err != nil {
		fail(err)
	}
	for rows.Next() {
		var p store.BusySendLaunchProcess
		var role string
		var namespace, endpoint sql.NullString
		if err := rows.Scan(&role, &p.PID, &p.StartTime, &namespace, &endpoint); err != nil {
			fail(err)
		}
		p.Role, p.PIDNamespace, p.Endpoint = store.BusySendProcessRole(role), namespace.String, endpoint.String
		out.Processes = append(out.Processes, p)
	}
	if err := rows.Err(); err != nil {
		fail(err)
	}
	_ = rows.Close()
	type handover struct {
		id      string
		dest    store.BusySendDestination
		prompts int
	}
	var open []handover
	hrows, err := raw.Query(`SELECT handover_id, launch_id, launch_token, provider, session_id, route_kind, route_id,
	        connection_generation, expected_turn_id, prompt_count
	    FROM busy_send_handovers WHERE launch_id = ? AND state = 'open' ORDER BY handover_seq`, launchID)
	if err != nil {
		fail(err)
	}
	for hrows.Next() {
		var h handover
		var token, session, routeID, turnID sql.NullString
		var routeKind string
		if err := hrows.Scan(&h.id, &h.dest.LaunchID, &token, &h.dest.Provider, &session, &routeKind, &routeID,
			&h.dest.ConnectionGeneration, &turnID, &h.prompts); err != nil {
			fail(err)
		}
		h.dest.LaunchToken, h.dest.SessionID, h.dest.RouteID, h.dest.TurnID = token.String, session.String, routeID.String, turnID.String
		h.dest.RouteKind = store.BusySendRouteKind(routeKind)
		open = append(open, h)
	}
	if err := hrows.Err(); err != nil {
		fail(err)
	}
	_ = hrows.Close()
	for _, h := range open {
		for i := 0; i < h.prompts; i++ {
			var emission string
			var transport, order, negative sql.NullString
			var fenced sql.NullInt64
			if err := raw.QueryRow(`SELECT emission, transport, order_kind, negative_kind, fenced_at_ms FROM busy_send_prompts
			    WHERE handover_id = ? AND prompt_index = ?`, h.id, i).Scan(&emission, &transport, &order, &negative, &fenced); err != nil {
				fail(err)
			}
			e := store.BusySendEmission(emission)
			if (e == store.BusySendEmissionIntent || e == store.BusySendEmissionIssued) && !order.Valid && !negative.Valid {
				out.Prompts = append(out.Prompts, FencedPrompt{HandoverID: h.id, PromptIndex: i, Destination: h.dest,
					Emission: e, Transport: store.BusySendTransport(transport.String), Fenced: fenced.Valid})
			}
		}
	}
	return out
}
