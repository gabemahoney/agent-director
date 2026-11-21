package store

// The busy-send permanent core (SR-2.1, SR-7.1, SR-7.2, SR-9.1): its
// declarations and stored codes, the records it holds (and delivery's
// hand-over count over them, SR-5.6), the exempt v20→v21 hop that is its only
// conversion, open's recognition of it, and its restore-only recovery.
// Fixtures: migration_fixtures_test.go (makeV20Fixture,
// preAddV21Core, bindV21Row, seedBusySendRecords, assertV21Backfill); the
// hop's rollback and step reentry rows are schema_test.go's.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// busySendCoreV1Digest is the SHA-256 of the permanent core's declarations as
// a store records them (busySendCoreDigest). The v20→v21 hop is the core's
// only conversion: every later release reads and writes these tables as
// declared, and open refuses any other declaration (SR-7.2). So once v21 is
// released this value never changes; a change to it strands every store
// already at v21. Before release it changed in place for the pause claim's
// refusal after its signal intent (b.kii E6 decision 3) and removal of the
// retired Cursor codes (E7), and immutable Launch capabilities (PM1), whose
// raw escape restriction preserves append-monotone measurement (repair 1).
// Existing v21 test stores are recreated.
const busySendCoreV1Digest = "1fa5ac4b4fee6639127fe725de0f4cd4fd3730e5dd72c3afd3a6b79c2f17e3e3"

// busySendCoreDigest hashes db's recorded core declarations, each object's
// type, name and SQL text (whitespace collapsed) in name order, and returns
// the digest with the text it hashed.
func busySendCoreDigest(t *testing.T, db *sql.DB) (digest, text string) {
	t.Helper()
	rows, err := db.Query(`SELECT type, name, sql FROM sqlite_master
		WHERE (name LIKE 'busy\_%' ESCAPE '\' OR name LIKE 'idx\_busy\_%' ESCAPE '\') AND sql IS NOT NULL ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	objects := 0
	for rows.Next() {
		var kind, name, ddl string
		if err := rows.Scan(&kind, &name, &ddl); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "%s %s: %s\n", kind, name, strings.Join(strings.Fields(ddl), " "))
		objects++
	}
	if objects != 27 {
		t.Errorf("the core has %d declared tables and indexes; want 13 tables and 14 indexes", objects)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:]), b.String()
}

// declaredCodes returns column's CHECK(column IN (...)) codes in the
// declaration ddl, or nil when the column has no such list.
func declaredCodes(ddl, column string) []string {
	m := regexp.MustCompile(`(?m)^\s*` + column + `\s+TEXT\b[^\n]*?CHECK\(` + column + ` IN \(([^)]*)\)\)`).FindStringSubmatch(ddl)
	if m == nil {
		return nil
	}
	var codes []string
	for _, c := range strings.Split(m[1], ",") {
		codes = append(codes, strings.Trim(strings.TrimSpace(c), "'"))
	}
	return codes
}

// codes is a code set's stored strings, in declaration order.
func codes[T ~string](values ...T) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = string(v)
	}
	return out
}

// assertSpawnsCoreChecks fails unless db's spawns refuses an unknown coverage
// code and a negative or non-integer Turn Start counter on row id (the
// columns' CHECK clauses); each refused write changes nothing.
func assertSpawnsCoreChecks(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	for _, set := range []string{"busy_send = 'maybe'", "turn_starts = -1", "turn_starts = 1.5", "turn_starts = 'x'"} {
		if _, err := db.Exec("UPDATE spawns SET "+set+" WHERE instance_id = ?", id); err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
			t.Errorf("spawns accepted %s: %v; want a CHECK refusal", set, err)
		}
	}
}

// TestBusySendCoreContract: a fresh store records exactly the permanent core
// (its declarations' digest, 13 tables and 14 indexes), its identity
// "busy-send-core-v1:<store id>", spawns' CHECK clauses, and each closed code
// set's CHECK list equal to its Go codes, while the extensible reason sets
// keep no list and their fixed strings (SR-2.1, SR-7.2).
func TestBusySendCoreContract(t *testing.T) {
	s, _ := openTempStore(t)
	if digest, text := busySendCoreDigest(t, s.db); digest != busySendCoreV1Digest {
		t.Errorf("core declarations digest %s; want the permanent %s. Declarations:\n%s", digest, busySendCoreV1Digest, text)
	}
	var identity string
	if err := s.db.QueryRow(`SELECT value FROM store_meta WHERE key = 'busy_send_core'`).Scan(&identity); err != nil || identity != "busy-send-core-v1:"+s.StoreID() {
		t.Errorf("core identity %q, %v; want busy-send-core-v1:%s", identity, err, s.StoreID())
	}
	if err := s.InsertPending(Spawn{InstanceID: "row", Agent: AgentClaude, CWD: "/tmp", TmuxSessionName: "ad-row", RelayMode: "off"}); err != nil {
		t.Fatal(err)
	}
	assertSpawnsCoreChecks(t, s.db, "row")

	ddl := map[string]string{}
	rows, err := s.db.Query(`SELECT name, sql FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name, text string
		if err := rows.Scan(&name, &text); err != nil {
			t.Fatal(err)
		}
		ddl[name] = text
	}
	rows.Close()
	for _, tc := range []struct {
		table, column string
		want          []string
	}{
		{"spawns", "busy_send", codes(BusySendNotCovered, BusySendCovered)},
		{"busy_conversations", "publication", codes(BusySendCandidate, BusySendPublished, BusySendRetired, BusySendAbandoned)},
		{"busy_conversations", "busy_send", codes(BusySendNotCovered, BusySendCovered)},
		{"busy_conversations", "default_send_mode", codes(BusySendWaitTurn, BusySendSteerTurn)},
		{"busy_conversations", "retired_reason", codes(BusySendConversationReplaced, BusySendRecordDeleted)},
		{"busy_send_launches", "busy_send", codes(BusySendNotCovered, BusySendCovered)},
		{"busy_send_launches", "kind", codes(BusySendLaunchSpawn, BusySendLaunchReuse, BusySendLaunchResume, BusySendLaunchMigrated)},
		{"busy_send_launches", "phase", codes(BusySendLaunchCandidate, BusySendLaunchUncertain, BusySendLaunchFailed, BusySendLaunchCurrent, BusySendLaunchSuperseded)},
		{"busy_send_launches", "route_kind", codes(BusySendRouteClaudeHooks, BusySendRouteCodexAppServer)},
		{"busy_send_launches", "override_send_mode", codes(BusySendWaitTurn, BusySendSteerTurn)},
		{"busy_send_launches", "end_cause", codes(BusySendProcessExited, BusySendLaunchPublished, BusySendCreationFailed, BusySendLaunchRecordDeleted, BusySendLaunchCandidateReplaced, busySendAgentMissing)},
		{"busy_send_launch_modes", "send_mode", codes(BusySendWaitTurn, BusySendSteerTurn)},
		{"busy_send_launch_modes", "text_kind", codes(BusySendTextUTF8TabLF)},
		{"busy_send_launch_modes", "measure_kind", codes(BusySendMeasureWrapperBudget, BusySendMeasureLiteralEscapes)},
		{"busy_send_launch_escapes", "send_mode", codes(BusySendWaitTurn, BusySendSteerTurn)},
		{"busy_send_launch_processes", "role", codes(BusySendProcessAgent, BusySendProcessAppServer, BusySendProcessTUI, BusySendProcessHelper)},
		{"busy_send_messages", "send_mode", codes(BusySendWaitTurn, BusySendSteerTurn)},
		{"busy_send_messages", "place", codes(BusySendHeld, BusySendClaimed, BusySendIssued, BusySendClosed)},
		{"busy_send_messages", "result_class", codes(BusySendClassHeld, BusySendClassQueued, BusySendClassDeliveredConfirmed, BusySendClassSentUnconfirmed, BusySendClassNotStarted)},
		{"busy_send_messages", "evidence_kind", codes(BusySendEvidenceCodexItemStarted, BusySendEvidenceCodexThreadItems)},
		{"busy_send_messages", "retention", codes(BusySendRetained, BusySendExpired)},
		{"busy_send_handovers", "route_kind", codes(BusySendRouteClaudeHooks, BusySendRouteCodexAppServer)},
		{"busy_send_handovers", "boundary", codes(BusySendBoundaryTurnEnd, BusySendBoundaryToolBoundary, BusySendBoundaryForce)},
		{"busy_send_handovers", "state", codes(BusySendHandoverOpen, BusySendHandoverClosed)},
		{"busy_send_prompts", "emission", codes(BusySendEmissionNone, BusySendEmissionIntent, BusySendEmissionIssued, BusySendEmissionReleased)},
		{"busy_send_prompts", "transport", codes(BusySendTransportWritten, BusySendTransportPartial, BusySendTransportUncertain, BusySendTransportUnwritten)},
		{"busy_send_prompts", "order_kind", codes(BusySendOrderOrderedAcceptance, BusySendOrderExactReceipt, BusySendOrderEmissionComplete)},
		{"busy_send_prompts", "admission_kind", codes(BusySendAdmissionPromptSighting, BusySendAdmissionRPCSuccess, BusySendAdmissionPromptResponse,
			BusySendAdmissionNativeSignal, BusySendAdmissionTranscriptMatch, BusySendAdmissionExactReceipt)},
		{"busy_send_prompts", "evidence_kind", codes(BusySendEvidenceCodexItemStarted, BusySendEvidenceCodexThreadItems)},
		{"busy_send_prompts", "negative_kind", codes(BusySendNegativeNotSubmitted, BusySendNegativeNoAdmission)},
		{"busy_send_forces", "outcome", codes(BusySendForceRunning, BusySendForcePending, BusySendForceHandedOver, BusySendForceRefused, BusySendForceFailed)},
		{"busy_send_forces", "retention", codes(BusySendRetained, BusySendExpired)},
		{"busy_send_pause_claims", "signal_kind", codes(BusySendSignalSIGTERM, BusySendSignalServerShutdown)},
		{"busy_send_pause_claims", "state", codes(BusySendPauseClaimStanding, BusySendPauseClaimReleased, BusySendPauseClaimEnded)},
		{"busy_send_pause_claims", "call_outcome", codes(BusySendPausePaused, BusySendPauseRefused, BusySendPauseExitUncertain, BusySendPauseFailed)},
		{"busy_send_pause_claims", "closed_by", codes(BusySendPauseRefusedBeforeSignal, BusySendPauseAbandonedBeforeSignal, BusySendPauseLaunchEnd, BusySendPauseLaunchPublished)},
	} {
		if got := declaredCodes(ddl[tc.table], tc.column); !slices.Equal(got, tc.want) {
			t.Errorf("%s.%s declares %v; want the stored codes %v", tc.table, tc.column, got, tc.want)
		}
	}
	// Reason catalogues grow without a conversion: no CHECK list, fixed codes.
	for _, tc := range []struct {
		table, column string
		got           []string
		want          string
	}{
		{"busy_send_messages", "wait_reason", codes(BusySendWaitTurnRunning, BusySendWaitDialog, BusySendWaitCompaction, BusySendWaitEarlierMessage,
			BusySendWaitHandoverInProgress, BusySendWaitAgentStarting, BusySendWaitLaunchEnded, BusySendWaitRouteUnavailable,
			BusySendWaitReadinessUnknown, BusySendWaitInputUnresolved, BusySendWaitAwaitingProof),
			"turn_running dialog compaction earlier_message handover_in_progress agent_starting launch_ended route_unavailable readiness_unknown input_unresolved awaiting_proof"},
		{"busy_send_messages", "result_reason", codes(BusySendReasonExactReceipt, BusySendReasonConfirmationOff, BusySendReasonHandedOverNotProven,
			BusySendReasonWaitTurnRaced, BusySendReasonLaunchEnded, BusySendReasonAdmissionUnknown, BusySendReasonRecordDeleted, BusySendReasonConversationReplaced),
			"exact_receipt confirmation_off handed_over_not_proven wait_turn_raced launch_ended admission_unknown record_deleted conversation_replaced"},
		{"busy_send_forces", "reason", codes(BusySendForceAdmitted, BusySendForceTranscriptMatch, BusySendForceAwaitingAdmission, BusySendForceNoHeldMessages,
			BusySendForceHandoverInProgress, BusySendForceEarlierMessage, BusySendForceInputUnresolved, BusySendForceAgentStarting, BusySendForceLaunchEnded,
			BusySendForceRouteUnavailable, BusySendForceNotSubmitted, BusySendForceNotIssued, BusySendForceAdmissionUnknown, BusySendForceAgentNotCovered,
			BusySendForceRecordDeleted, BusySendForceCompaction, BusySendForceTurnRunning, BusySendForceDialog),
			"admitted transcript_match awaiting_admission no_held_messages handover_in_progress earlier_message input_unresolved agent_starting launch_ended route_unavailable not_submitted not_issued admission_unknown agent_not_covered record_deleted compaction turn_running dialog"},
		{"busy_send_pause_claims", "call_cause", nil, ""},
		{"", "", codes(BusySendLookupFound, BusySendLookupUnknown, BusySendLookupRetentionExpired), "found unknown retention_expired"},
	} {
		if tc.table != "" && declaredCodes(ddl[tc.table], tc.column) != nil {
			t.Errorf("%s.%s has a CHECK list; want an extensible catalogue", tc.table, tc.column)
		}
		if want := strings.Fields(tc.want); !slices.Equal(tc.got, want) {
			t.Errorf("%s.%s codes %v; want the fixed %v", tc.table, tc.column, tc.got, want)
		}
	}
}

// TestBusySendCoveredSet: this release's covered set is exactly Claude Code,
// and its two accessors agree on every agent kind, a legacy empty agent
// counting as Claude; a caller's copy of the set changes nothing (SR-9.1,
// SR-7.3). The expectations are written out here, not read from the set, so
// dropping Claude or covering another agent fails.
func TestBusySendCoveredSet(t *testing.T) {
	first := BusySendCoveredAgents()
	if want := []string{AgentClaude}; !slices.Equal(first, want) {
		t.Errorf("covered set %v; want exactly %v in this release", first, want)
	}
	copied := BusySendCoveredAgents()
	for i := range copied {
		copied[i] = "changed"
	}
	if got := BusySendCoveredAgents(); !slices.Equal(got, first) {
		t.Errorf("covered set after a caller changed its copy = %v; want %v", got, first)
	}
	for agent, want := range map[string]BusySendCoverage{
		AgentClaude: BusySendCovered, "": BusySendCovered,
		AgentCodex: BusySendNotCovered, AgentCursor: BusySendNotCovered, AgentDroid: BusySendNotCovered,
		AgentOpenCode: BusySendNotCovered, "unknown": BusySendNotCovered,
	} {
		if got := BusySendCoverageForAgent(agent); got != want {
			t.Errorf("BusySendCoverageForAgent(%q) = %s; want %s", agent, got, want)
		}
	}
}

// E7: exercise SQLite's six changed CHECKs on valid records, including every
// surviving code, so a rejection by an unrelated constraint cannot pass.
func TestBusySendCursorFreeChecks(t *testing.T) {
	for _, tc := range []struct {
		table, column, retired string
		kept                   []string
	}{
		{"busy_send_launches", "route_kind", "cursor_acp", []string{"claude_hooks", "codex_app_server"}},
		{"busy_send_handovers", "route_kind", "cursor_acp", []string{"claude_hooks", "codex_app_server"}},
		{"busy_send_launch_processes", "role", "gateway", []string{"agent", "app_server", "tui", "helper"}},
		{"busy_send_messages", "evidence_kind", "cursor_end_turn", []string{"codex_item_started", "codex_thread_items"}},
		{"busy_send_prompts", "evidence_kind", "cursor_end_turn", []string{"codex_item_started", "codex_thread_items"}},
		{"busy_send_pause_claims", "signal_kind", "gateway_exit", []string{"sigterm", "server_shutdown"}},
	} {
		t.Run(tc.table+"/"+tc.column, func(t *testing.T) {
			s, _ := seedBusySendRecordsRow(t)
			set := tc.column + "=?"
			if tc.table == "busy_send_prompts" {
				set += ", receipt_at_ms=4000"
			}
			q := "UPDATE " + tc.table + " SET " + set + " WHERE rowid=(SELECT min(rowid) FROM " + tc.table + ")"
			before := dbDump(t, s.filePath)
			if _, err := s.db.Exec(q, tc.retired); err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
				t.Fatalf("retired %s: %v; want CHECK refusal", tc.retired, err)
			}
			if !reflect.DeepEqual(before, dbDump(t, s.filePath)) {
				t.Fatal("rejected code changed the store")
			}
			for _, kept := range tc.kept {
				res, err := s.db.Exec(q, kept)
				if err != nil {
					t.Fatalf("surviving %s: %v", kept, err)
				}
				if n, err := res.RowsAffected(); err != nil || n != 1 {
					t.Fatalf("surviving %s changed %d rows: %v", kept, n, err)
				}
			}
		})
	}
}

// TestBusySendRecordsRepresentation: the core holds seedBusySendRecords'
// representative state and keeps it when the agent row is deleted (no core
// table refers to spawns); it admits the transitions its records need (a
// return to Held beside earlier membership, expiry to a no-text tombstone
// keeping its pair, a refused force with no captured target, a signalled
// claim's exit_uncertain outcome before its fence ends, a refusal proven
// before the signal keeping the claim's signal intent, one standing claim
// and one open force per scope) and refuses every state its keys and
// constraints rule out, a dangling reference at COMMIT and an Agent Default
// override on a Launch other than a resume (SR-2.1, SR-1.4, SR-6.5).
func TestBusySendRecordsRepresentation(t *testing.T) {
	seed := seedBusySendRecordsRow
	t.Run("deleting the agent keeps every record", func(t *testing.T) {
		s, r := seed(t)
		// Every record is kept: each core table holds as many rows after the
		// deletion. Covered deletion finalizes custody in place (SR-6.3), so
		// rows are counted, not compared.
		core := func() map[string]int {
			out := map[string]int{}
			for key, rows := range dbDump(t, s.filePath) {
				if strings.HasPrefix(key, "rows busy_") {
					out[key] = len(rows.([]string))
				}
			}
			return out
		}
		before := core()
		busyLaunchDeleteCovered(t, s, "rec")
		if after := core(); !reflect.DeepEqual(after, before) {
			t.Errorf("deleting the agent removed core records:\n before %v\n after  %v", before, after)
		}
		// Deletion retires publications while preserving every record and claim.
		busyLaunchWantCount(t, s, 1, `SELECT count(*) FROM busy_conversations WHERE conversation_id=? AND publication='retired' AND retired_reason='record_deleted'`, r.conversation)
		busyLaunchWantCount(t, s, 1, `SELECT count(*) FROM busy_send_launches WHERE launch_id=? AND phase='superseded' AND end_cause='record_deleted'`, r.launch)
		var retention string
		var text []byte
		err := s.db.QueryRow(`SELECT m.retention, m.text FROM busy_send_results p JOIN busy_send_messages m USING (message_id)
			WHERE p.caller_uuid = ? AND p.delivery_request_id = 'req-3'`, r.callerUUID).Scan(&retention, &text)
		if err != nil || retention != "expired" || text != nil {
			t.Errorf("expired pair: retention %q, text %q, %v; want its tombstone with no text", retention, text, err)
		}
	})
	for _, tc := range []struct {
		name  string
		stmts []string
		want  string // "" when admitted; else the refusing constraint
	}{
		{"a Message returns to Held beside its earlier membership", []string{
			`UPDATE busy_send_messages SET place = 'held', handover_id = NULL, wait_reason = 'handover_in_progress' WHERE message_id = '{issued}'`}, ""},
		{"expiry leaves a no-text tombstone keeping its pair", []string{
			`UPDATE busy_send_messages SET text = NULL, text_sha256 = NULL, place = 'closed', result_class = NULL, wait_reason = NULL,
				wait_since_ms = NULL, result_final = 1, final_at_ms = 5000, retention = 'expired', expired_at_ms = 9000 WHERE message_id = '{held}'`}, ""},
		{"a refused force captures no target or hand-over", []string{
			`INSERT INTO busy_send_forces (force_id, store_id, instance_id, owner_call_id, call_deadline_ms, outcome, reason, started_at_ms, ended_at_ms)
				VALUES ('refused', '{store}', 'rec', 'call-9', 10, 'refused', 'agent_not_covered', 5, 6)`}, ""},
		{"a standing claim on the earlier Launch beside the current one's", []string{
			`INSERT INTO busy_send_pause_claims (claim_id, store_id, instance_id, launch_id, owner_call_id, claimed_at_ms, deadline_ms, signal_kind, state)
				VALUES ('claim-old', '{store}', 'rec', '{oldLaunch}', 'call-8', 1, 2, 'sigterm', 'standing')`}, ""},
		{"a running force beside the ended one", []string{forceSQL("running-1")}, ""},
		{"a signalled claim records exit_uncertain standing, then ends at Launch End", pauseExitUncertainSQL("launch_end"), ""},
		{"a signalled claim records exit_uncertain standing, then ends at a new Launch", pauseExitUncertainSQL("launch_published"), ""},
		{"a refusal proven before the signal keeps the claim's signal intent", []string{pauseExitUncertainSQL("")[0],
			`UPDATE busy_send_pause_claims SET call_outcome = 'refused', call_cause = 'agent_process_unverifiable', call_outcome_at_ms = 3500,
				state = 'released', closed_by = 'refused_before_signal', closed_at_ms = 3500 WHERE claim_id = '{standingClaim}'`}, ""},

		{"an Agent Default override on a Launch that is not a resume", []string{
			`UPDATE busy_send_launches SET override_confirmed = 0 WHERE launch_id = '{oldLaunch}'`}, "CHECK"},
		{"logical deletion requires supersession", []string{`UPDATE busy_send_launches SET ended_at_ms=5000,end_cause='record_deleted' WHERE launch_id='{launch}'`}, "CHECK"},
		{"logical candidate replacement requires supersession", []string{`UPDATE busy_send_launches SET ended_at_ms=5000,end_cause='candidate_replaced' WHERE launch_id='{launch}'`}, "CHECK"},
		{"logical candidate replacement is representable", []string{`UPDATE busy_send_launches SET phase='superseded',ended_at_ms=5000,end_cause='candidate_replaced' WHERE launch_id='{launch}'`}, ""},
		{"a second published conversation", []string{
			`INSERT INTO busy_conversations (conversation_id, store_id, instance_id, generation, publication, provider, recorded_at_ms, published_at_ms)
				VALUES ('conv-3', '{store}', 'rec', 3, 'published', 'claude', 1, 1)`}, "UNIQUE"},
		{"a second current Launch", []string{
			`INSERT INTO busy_send_launches (launch_id, store_id, instance_id, conversation_id, life_number, provider, busy_send, held_count_limit, held_bytes_limit, kind, phase, recorded_at_ms)
				VALUES ('launch-3', '{store}', 'rec', '{conv}', 3, 'claude', 'covered', 1000, 8000000, 'reuse', 'current', 1)`}, "UNIQUE"},
		{"a second open hand-over", []string{
			`UPDATE busy_send_handovers SET state = 'open', closed_at_ms = NULL WHERE handover_id = '{oldHandover}'`}, "UNIQUE"},
		{"a second open force", []string{forceSQL("running-1"), forceSQL("running-2")}, "UNIQUE"},
		{"a second standing claim on one Launch", []string{
			`INSERT INTO busy_send_pause_claims (claim_id, store_id, instance_id, launch_id, owner_call_id, claimed_at_ms, deadline_ms, signal_kind, state)
				VALUES ('claim-3', '{store}', 'rec', '{launch}', 'call-8', 1, 2, 'sigterm', 'standing')`}, "UNIQUE"},
		{"a second pair for one Message", []string{
			`INSERT INTO busy_send_results (caller_uuid, delivery_request_id, store_id, message_id, instance_id)
				VALUES ('bbbbbbbb-bbbb-4bbb-bbbb-bbbbbbbbbbbb', 'req-9', '{store}', '{held}', 'rec')`}, "UNIQUE"},
		{"custody claimed outside any hand-over", []string{`UPDATE busy_send_messages SET place = 'claimed' WHERE message_id = '{held}'`}, "CHECK"},
		{"a retained Message without its text", []string{`UPDATE busy_send_messages SET text = NULL WHERE message_id = '{held}'`}, "CHECK"},
		{"an expired Message keeping its text", []string{
			`UPDATE busy_send_messages SET retention = 'expired', expired_at_ms = 9000 WHERE message_id = '{held}'`}, "CHECK"},
		{"a Final Result still waiting", []string{`UPDATE busy_send_messages SET result_final = 1, final_at_ms = 5000 WHERE message_id = '{held}'`}, "CHECK"},
		{"a forced hand-over with no force", []string{`UPDATE busy_send_handovers SET force_id = NULL WHERE handover_id = '{handover}'`}, "CHECK"},
		{"an ended pause claim with no closure", []string{
			`UPDATE busy_send_pause_claims SET state = 'ended' WHERE claim_id = '{standingClaim}'`}, "CHECK"},
		// A CHECK that evaluates to NULL passes, so these hold NULL operands.
		{"a refusal closure with no recorded call outcome", []string{
			`UPDATE busy_send_pause_claims SET state = 'released', closed_by = 'refused_before_signal', closed_at_ms = 3600
				WHERE claim_id = '{standingClaim}'`}, "CHECK"},
		{"a replacement named with no retired reason", []string{
			`INSERT INTO busy_conversations (conversation_id, store_id, instance_id, generation, publication, provider, recorded_at_ms, replaced_by)
				VALUES ('conv-3', '{store}', 'rec', 3, 'candidate', 'claude', 1, '{conv}')`}, "CHECK"},
		{"a captured Turn Start counter that is not an integer", []string{
			`UPDATE busy_send_handovers SET intent_turn_starts = 'five' WHERE handover_id = '{handover}'`}, "CHECK"},
		{"a pause claim released after its signal", []string{pauseExitUncertainSQL("")[0],
			`UPDATE busy_send_pause_claims SET state = 'released', closed_by = 'abandoned_before_signal', closed_at_ms = 3600
				WHERE claim_id = '{standingClaim}'`}, "CHECK"},
		{"a refused call outcome with a lifecycle closure", []string{
			`UPDATE busy_send_pause_claims SET call_outcome = 'refused', call_outcome_at_ms = 3500, state = 'ended', closed_by = 'launch_end',
				closed_at_ms = 3600 WHERE claim_id = '{standingClaim}'`}, "CHECK"},
		{"a caller UUID not in canonical form", []string{
			`INSERT INTO busy_send_results (caller_uuid, delivery_request_id, store_id, message_id, instance_id)
				VALUES ('AAAAAAAA-AAAA-4AAA-AAAA-AAAAAAAAAAAA', 'req-9', '{store}', '{held}', 'rec')`}, "CHECK"},
		{"a membership naming no Message", []string{
			`INSERT INTO busy_send_handover_members (handover_id, member_index, message_id, prompt_index, store_id)
				VALUES ('{handover}', 2, 'no-such-message', 0, '{store}')`}, "FOREIGN KEY"},
		{"covered limits cannot be absent", []string{`UPDATE busy_send_launches SET held_count_limit=NULL WHERE launch_id='{launch}'`}, "CHECK"},
		{"held capacity must be positive", []string{`UPDATE busy_send_launches SET held_bytes_limit=0 WHERE launch_id='{launch}'`}, "CHECK"},
		{"held capacity must be an integer", []string{`UPDATE busy_send_launches SET held_count_limit=1.5 WHERE launch_id='{launch}'`}, "CHECK"},
		{"uncovered limits must be absent", []string{`UPDATE busy_send_launches SET busy_send='not_covered' WHERE launch_id='{launch}'`}, "CHECK"},
		{"message limit must be positive", []string{`UPDATE busy_send_launch_modes SET message_bytes=0 WHERE launch_id='{launch}' AND send_mode='wait_turn'`}, "CHECK"},
		{"prompt limit must be an integer", []string{`UPDATE busy_send_launch_modes SET prompt_bytes=1.5 WHERE launch_id='{launch}' AND send_mode='wait_turn'`}, "CHECK"},
		{"wrapper budget must be nonnegative", []string{`UPDATE busy_send_launch_modes SET wrapper_bytes=-1 WHERE launch_id='{launch}' AND send_mode='wait_turn'`}, "CHECK"},
		{"unknown mode is refused", []string{`UPDATE busy_send_launch_modes SET send_mode='later_turn' WHERE launch_id='{launch}' AND send_mode='wait_turn'`}, "CHECK"},
		{"unknown text kind is refused", []string{`UPDATE busy_send_launch_modes SET text_kind='binary' WHERE launch_id='{launch}' AND send_mode='wait_turn'`}, "CHECK"},
		{"unknown measure is refused", []string{`UPDATE busy_send_launch_modes SET measure_kind='rewrite' WHERE launch_id='{launch}' AND send_mode='wait_turn'`}, "CHECK"},
		{"budget measure cannot carry literals", []string{`UPDATE busy_send_launch_modes SET measure_kind='wrapper_budget' WHERE launch_id='{launch}' AND send_mode='wait_turn'`}, "CHECK"},
		{"literal prefix must be text", []string{`UPDATE busy_send_launch_modes SET prefix=x'FF' WHERE launch_id='{launch}' AND send_mode='wait_turn'`}, "CHECK"},
		{"escape count must be an integer", []string{`UPDATE busy_send_launch_modes SET escape_count=1.5 WHERE launch_id='{launch}' AND send_mode='wait_turn'`}, "CHECK"},
		{"escape index must be nonnegative", []string{`UPDATE busy_send_launch_escapes SET escape_index=-1 WHERE launch_id='{launch}' AND send_mode='wait_turn' AND escape_index=0`}, "CHECK"},
		{"escape cannot match empty text", []string{`UPDATE busy_send_launch_escapes SET raw='' WHERE launch_id='{launch}' AND send_mode='wait_turn' AND escape_index=0`}, "CHECK"},
		{"escape cannot shrink bytes", []string{`UPDATE busy_send_launch_escapes SET raw='é',escaped='a' WHERE launch_id='{launch}' AND send_mode='wait_turn' AND escape_index=0`}, "CHECK"},
		{"mode must name a Launch", []string{`UPDATE busy_send_launch_modes SET launch_id='missing' WHERE launch_id='{launch}' AND send_mode='wait_turn'`}, "FOREIGN KEY"},
		{"escape must name a mode", []string{`UPDATE busy_send_launch_escapes SET launch_id='missing' WHERE launch_id='{launch}' AND send_mode='wait_turn' AND escape_index=0`}, "FOREIGN KEY"},
		{"mode deletion does not cascade escapes", []string{`DELETE FROM busy_send_launch_modes WHERE launch_id='{launch}' AND send_mode='wait_turn'`}, "FOREIGN KEY"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, r := seed(t)
			fill := strings.NewReplacer("{store}", s.StoreID(), "{conv}", r.conversation, "{oldLaunch}", r.oldLaunch, "{launch}", r.launch,
				"{held}", r.held, "{issued}", r.issued, "{oldHandover}", r.oldHandover, "{handover}", r.handover, "{standingClaim}", r.standingClaim)
			tx, err := s.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			for i, q := range tc.stmts {
				var res sql.Result
				if res, err = tx.Exec(fill.Replace(q)); err != nil {
					break
				}
				// Each admitted statement changes its one row; a WHERE that
				// matches nothing would pass silently.
				if n, _ := res.RowsAffected(); n != 1 {
					t.Fatalf("statement %d changed %d rows; want 1", i, n)
				}
			}
			if err == nil {
				err = tx.Commit()
			}
			if tc.want == "" && err != nil {
				t.Fatalf("the core refused it: %v", err)
			}
			if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want+" constraint failed")) {
				t.Fatalf("got %v; want a %s constraint refusal", err, tc.want)
			}
		})
	}
}

// b.kii PM1 repair 1: unsafe Raw literals are rejected by the validator and
// SQLite independently; digits/LF in fixed text and escaped output stay legal.
func TestBusySendLaunchEscapeLiterals(t *testing.T) {
	cases := []struct {
		name, raw string
		valid     bool
	}{
		{"current Claude literals", "", true},
		{"digits and LF outside Raw", "<", true},
		{"non-ASCII decimal digit", "٢", true},
	}
	for _, literal := range invalidV21RawLiterals() {
		cases = append(cases, struct {
			name, raw string
			valid     bool
		}{literal.name, literal.raw, false})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := v21ClaudeCapabilities()
			w := &c.Modes[0].Wrapping
			if tc.raw != "" {
				w.Prefix, w.Suffix = "0\n", "\n9"
				w.Escapes[0] = BusySendEscape{Raw: tc.raw, Escaped: "0\n" + tc.raw}
			}
			t.Run("validator", func(t *testing.T) {
				err := validateBusySendLaunchCapabilities(c)
				if tc.valid && err != nil || !tc.valid && !errors.Is(err, errBusySendCapabilities) {
					t.Errorf("validate Raw %q = %v; valid=%v", tc.raw, err, tc.valid)
				}
				if !tc.valid && w.Measure([]byte("unmatched")) != -1 {
					t.Error("invalid Raw has a usable measure even without a matching body")
				}
			})
			t.Run("SQLite", func(t *testing.T) {
				s, r := seedBusySendRecordsRow(t)
				tx, err := s.db.Begin()
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				if _, err := tx.Exec(`UPDATE busy_send_launch_modes SET prefix=?,suffix=? WHERE launch_id=? AND send_mode='wait_turn'`, w.Prefix, w.Suffix, r.launch); err != nil {
					t.Fatal(err)
				}
				res, err := tx.Exec(`UPDATE busy_send_launch_escapes SET raw=?,escaped=? WHERE launch_id=? AND send_mode='wait_turn' AND escape_index=0`, w.Escapes[0].Raw, w.Escapes[0].Escaped, r.launch)
				if !tc.valid {
					if err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
						t.Fatalf("SQL Raw %q = %v; want CHECK refusal", tc.raw, err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if n, _ := res.RowsAffected(); n != 1 {
					t.Fatalf("escape setup changed %d rows; want 1", n)
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
				got, err := readBusySendLaunchCapabilities(context.Background(), s.db, s.StoreID(), r.launch)
				if err != nil || !reflect.DeepEqual(got, c) {
					t.Fatalf("accepted snapshot = %+v, %v; want %+v", got, err, c)
				}
			})
		})
	}
}

// PM1: readers reject corrupt snapshots as store errors, never as unsupported
// mode/provider fallbacks. Deliberate corruption bypasses DDL only in this test.
func TestBusySendLaunchCapabilitiesReadFailures(t *testing.T) {
	cases := []struct{ name, sql string }{
		{"missing all modes", `DELETE FROM busy_send_launch_modes WHERE launch_id='{launch}'`},
		{"missing Wait Turn", `DELETE FROM busy_send_launch_escapes WHERE launch_id='{launch}' AND send_mode='wait_turn'; DELETE FROM busy_send_launch_modes WHERE launch_id='{launch}' AND send_mode='wait_turn'`},
		{"missing trailing escape", `DELETE FROM busy_send_launch_escapes WHERE launch_id='{launch}' AND send_mode='wait_turn' AND escape_index=1`},
		{"escape gap", `UPDATE busy_send_launch_escapes SET escape_index=3 WHERE launch_id='{launch}' AND send_mode='wait_turn' AND escape_index=1`},
		{"Launch from another store", `UPDATE busy_send_launches SET store_id='other' WHERE launch_id='{launch}'`},
		{"mode from another store", `UPDATE busy_send_launch_modes SET store_id='other' WHERE launch_id='{launch}'`},
		{"escape from another store", `UPDATE busy_send_launch_escapes SET store_id='other' WHERE launch_id='{launch}'`},
		{"uncovered with children", `UPDATE busy_send_launches SET busy_send='not_covered',held_count_limit=NULL,held_bytes_limit=NULL WHERE launch_id='{launch}'`},
		{"missing held limit", `UPDATE busy_send_launches SET held_bytes_limit=NULL WHERE launch_id='{launch}'`},
		{"real held limit", `UPDATE busy_send_launches SET held_count_limit=2.5 WHERE launch_id='{launch}'`},
		{"nonpositive held limit", `UPDATE busy_send_launches SET held_count_limit=0 WHERE launch_id='{launch}'`},
		{"real message limit", `UPDATE busy_send_launch_modes SET message_bytes=1.5 WHERE launch_id='{launch}'`},
		{"nonpositive prompt limit", `UPDATE busy_send_launch_modes SET prompt_bytes=0 WHERE launch_id='{launch}'`},
		{"negative wrapper", `UPDATE busy_send_launch_modes SET wrapper_bytes=-1 WHERE launch_id='{launch}'`},
		{"unknown mode", `UPDATE busy_send_launch_modes SET send_mode='later_turn' WHERE launch_id='{launch}' AND send_mode='wait_turn'`},
		{"unknown text kind", `UPDATE busy_send_launch_modes SET text_kind='binary' WHERE launch_id='{launch}'`},
		{"unknown measure", `UPDATE busy_send_launch_modes SET measure_kind='rewrite' WHERE launch_id='{launch}'`},
		{"blob literal", `UPDATE busy_send_launch_modes SET prefix=x'FF' WHERE launch_id='{launch}'`},
		{"budget with literals", `UPDATE busy_send_launch_modes SET measure_kind='wrapper_budget' WHERE launch_id='{launch}'`},
		{"empty escape", `UPDATE busy_send_launch_escapes SET raw='' WHERE launch_id='{launch}'`},
		{"shrinking escape", `UPDATE busy_send_launch_escapes SET escaped='x' WHERE launch_id='{launch}'`},
	}
	for _, literal := range invalidV21RawLiterals() {
		cases = append(cases, struct{ name, sql string }{"invalid Raw " + literal.name,
			fmt.Sprintf(`UPDATE busy_send_launch_escapes SET raw=CAST(x'%x' AS TEXT) WHERE launch_id='{launch}' AND send_mode='wait_turn' AND escape_index=0`, literal.raw)})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, r := seedBusySendRecordsRow(t)
			ctx := context.Background()
			conn, err := s.db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, q := range []string{`PRAGMA foreign_keys=OFF`, `PRAGMA ignore_check_constraints=ON`,
				strings.ReplaceAll(tc.sql, "{launch}", r.launch), `PRAGMA ignore_check_constraints=OFF`, `PRAGMA foreign_keys=ON`} {
				if _, err := conn.ExecContext(ctx, q); err != nil {
					t.Fatal(err)
				}
			}
			_ = conn.Close()
			before := dbDump(t, s.filePath)
			if _, err := readBusySendLaunchCapabilities(ctx, s.db, s.StoreID(), r.launch); !errors.Is(err, errBusySendCapabilities) {
				t.Fatalf("snapshot read = %v; want invalid capabilities", err)
			}
			// Public reads intentionally short-circuit uncovered Launches;
			// the core reader above still validates their absent-facts rule.
			if tc.name != "uncovered with children" {
				if _, err := s.ReadBusySendObservation("rec", time.Second); !errors.Is(err, errBusySendCapabilities) {
					t.Fatalf("observation = %v; want store error", err)
				}
				if _, err := s.AdmitBusySend(BusySendAdmissionRequest{InstanceID: "rec", Text: []byte("new")}); !errors.Is(err, errBusySendCapabilities) {
					t.Fatalf("admission = %v; want store error", err)
				}
			}
			if !reflect.DeepEqual(before, dbDump(t, s.filePath)) {
				t.Fatal("invalid snapshot read/admission changed the store")
			}
		})
	}
}

// PM1 fixes byte arithmetic, including empty literal measures and original-body
// escape counting. Overflow is invalid, never a small admissible prompt.
func TestBusySendStoredMeasureSemantics(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode BusySendModeLimits
		body string
		want int64
	}{
		{"empty literal is not a budget", BusySendModeLimits{MeasureKind: BusySendMeasureLiteralEscapes, WrapperBytes: 999}, "é", 2},
		{"budget", BusySendModeLimits{MeasureKind: BusySendMeasureWrapperBudget, WrapperBytes: 7}, "é", 9},
		{"ordered escapes count the original", BusySendModeLimits{MeasureKind: BusySendMeasureLiteralEscapes,
			Wrapping: BusySendWrapping{Prefix: "é[", Suffix: "]", Escapes: []BusySendEscape{{"<", "&lt;"}, {"&", "&amp;"}}}}, "<&", 13},
		{"digits and LF outside Raw", BusySendModeLimits{MeasureKind: BusySendMeasureLiteralEscapes,
			Wrapping: BusySendWrapping{Prefix: "0\n", Suffix: "\n9", Escapes: []BusySendEscape{{"<", "0\n"}}}}, "<", 6},
		{"overflow", BusySendModeLimits{MeasureKind: BusySendMeasureWrapperBudget, WrapperBytes: math.MaxInt64}, "x", -1},
		{"invalid measure", BusySendModeLimits{MeasureKind: "future"}, "x", -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.mode.Measure([]byte(tc.body)); got != tc.want {
				t.Fatalf("Measure(%q) = %d; want %d", tc.body, got, tc.want)
			}
		})
	}
}

func TestBusySendLaunchCapabilitiesOverflow(t *testing.T) {
	s, r := seedBusySendRecordsRow(t)
	c := &BusySendLaunchCapabilities{HeldCount: math.MaxInt64, HeldBytes: math.MaxInt64,
		Modes: []BusySendModeLimits{{Mode: BusySendWaitTurn, TextKind: BusySendTextUTF8TabLF,
			MessageBytes: 1, PromptBytes: math.MaxInt64, MeasureKind: BusySendMeasureWrapperBudget, WrapperBytes: math.MaxInt64}}}
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	seedV21Capabilities(t, tx, r.launch, c)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	before := dbDump(t, s.filePath)
	if _, err := s.AdmitBusySend(BusySendAdmissionRequest{InstanceID: "rec", Text: []byte("x")}); !errors.Is(err, errBusySendCapabilities) {
		t.Fatalf("overflow admission = %v; want store error", err)
	}
	if !reflect.DeepEqual(before, dbDump(t, s.filePath)) {
		t.Fatal("overflow allocated custody or changed the snapshot")
	}
}

// PM1: capability insertion failures, including a deferred child reference at
// COMMIT, roll back schema, core rows and stamp, preserving the authorization.
func TestV21CapabilityInsertRollback(t *testing.T) {
	for _, stage := range []string{"mode", "escape", "commit"} {
		t.Run(stage, func(t *testing.T) {
			f := makeV20Fixture(t, t.TempDir())
			preAddV21Core(t, f.path, "busy_conversations", "busy_send_launches", "busy_send_launch_modes", "busy_send_launch_escapes")
			withRaw(t, f.path, func(db *sql.DB) {
				q := `CREATE TRIGGER fail_capability BEFORE INSERT ON busy_send_launch_modes BEGIN SELECT RAISE(ABORT,'capability insert failed'); END`
				if stage == "escape" {
					q = strings.Replace(q, "busy_send_launch_modes", "busy_send_launch_escapes", 1)
				} else if stage == "commit" {
					q = `CREATE TRIGGER fail_capability AFTER INSERT ON busy_send_launch_escapes BEGIN
					    UPDATE busy_send_launch_escapes SET launch_id=NEW.launch_id || '-missing' WHERE launch_id=NEW.launch_id AND send_mode=NEW.send_mode AND escape_index=NEW.escape_index; END`
				}
				mustExec(t, db, q)
			})
			before := dbDump(t, f.path)
			writeSentinel(t, f.dir, 20, schemaVersion)
			s, err := openMigrationTestStore(t, f.path)
			if s != nil {
				_ = s.Close()
			}
			want := "capability insert failed"
			if stage == "commit" {
				want = "FOREIGN KEY constraint failed"
			}
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("migration = %v; want %s", err, want)
			}
			if !reflect.DeepEqual(before, dbDump(t, f.path)) || readUserVersion(t, f.path) != 20 {
				t.Fatal("failed capability insert changed schema, records or stamp")
			}
			assertSentinel(t, f.dir, true)
		})
	}
}

// seedBusySendRecordsRow opens a store whose pending Claude row "rec" is bound,
// covered, to seedBusySendRecords' published conversation and current Launch.
func seedBusySendRecordsRow(t *testing.T) (*Store, busySendRecords) {
	t.Helper()
	s, _ := openTempStore(t)
	if err := s.InsertPending(Spawn{InstanceID: "rec", Agent: AgentClaude, CWD: "/tmp", TmuxSessionName: "ad-rec", RelayMode: "off"}); err != nil {
		t.Fatal(err)
	}
	// Replace the fresh insertion's empty core with the richer fixture before
	// exposing it to a test. No retained custody exists at this point.
	r := seedBusySendRecords(t, s.db, s.StoreID(), "rec")
	return s, r
}

// TestBusySendHandoverCount (SR-5.6): delivery's handover_count counts
// distinct non-Final hand-overs, each at its Messages' latest attempt only.
// seedBusySendRecords' open hand-over of Messages 2 and 4 counts once, and
// Message 1, Held again after the closed hand-over, adds nothing. With
// Message 1's custody closed unfinished after that hand-over, the two
// separate hand-overs count 2; with Message 1 handed over again in the open
// one, its earlier hand-over no longer counts.
func TestBusySendHandoverCount(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stmts []string
		want  int64
	}{
		{"a multi-Message hand-over", nil, 1},
		{"two separate hand-overs", []string{`UPDATE busy_send_messages SET place = 'closed', result_class = 'queued',
			wait_reason = 'awaiting_proof' WHERE message_id = '{held}'`}, 2},
		{"a Message handed over again", []string{
			`INSERT INTO busy_send_handover_members (handover_id, member_index, message_id, prompt_index, store_id)
				VALUES ('{handover}', 2, '{held}', 0, '{store}')`,
			`UPDATE busy_send_messages SET place = 'issued', handover_id = '{handover}', wait_reason = 'awaiting_proof'
				WHERE message_id = '{held}'`}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, r := seedBusySendRecordsRow(t)
			fill := strings.NewReplacer("{store}", s.StoreID(), "{held}", r.held, "{handover}", r.handover)
			for _, q := range tc.stmts {
				mustExec(t, s.db, fill.Replace(q))
			}
			o, err := s.ReadBusySendObservation("rec", DefaultLockWait)
			if err != nil || o == nil || o.HandoverCount != tc.want {
				t.Errorf("observation %+v, %v; want handover_count %d", o, err, tc.want)
			}
		})
	}
}

// pauseExitUncertainSQL signals seedBusySendRecords' standing claim, records
// the call's exit_uncertain outcome while the fence still stands (SR-6.4,
// SR-6.5), then, unless closedBy is "", ends the claim by closedBy.
func pauseExitUncertainSQL(closedBy string) []string {
	stmts := []string{
		`UPDATE busy_send_pause_claims SET signal_intent_at_ms = 3450 WHERE claim_id = '{standingClaim}'`,
		`UPDATE busy_send_pause_claims SET call_outcome = 'exit_uncertain', call_cause = 'exit_not_observed', call_outcome_at_ms = 3500
			WHERE claim_id = '{standingClaim}' AND state = 'standing'`,
	}
	if closedBy != "" {
		stmts = append(stmts, `UPDATE busy_send_pause_claims SET state = 'ended', closed_by = '`+closedBy+`', closed_at_ms = 3600
			WHERE claim_id = '{standingClaim}' AND call_outcome = 'exit_uncertain'`)
	}
	return stmts
}

// forceSQL inserts a running force with no hand-over yet on
// seedBusySendRecords' conversation, as the placeholders fill it.
func forceSQL(id string) string {
	return `INSERT INTO busy_send_forces (force_id, store_id, instance_id, conversation_id, launch_id, launch_token, provider, owner_call_id,
		call_deadline_ms, outcome, started_at_ms) VALUES ('` + id + `', '{store}', 'rec', '{conv}', '{launch}', '` + recToken + `', 'claude', 'call-` + id + `', 9000, 'running', 8000)`
}

// TestV21MigrationRecordsEveryInstance (SR-7.1, SR-7.2, SR-9.1): an ordinary
// open of makeV20Fixture's genuine v20 store refuses unchanged without an
// exact sentinel; with {"from":20,"to":21} it migrates, consumes the
// authorization with one ad.schema.migrated line, keeps every v20 value and
// the store identity, records the permanent core and one conversation and
// current Launch per row (assertV21Backfill) and nothing else. Reopening with
// no sentinel or a stale one, and re-running the hop, change nothing; the
// migrated spawns keep the columns' CHECK clauses.
func TestV21MigrationRecordsEveryInstance(t *testing.T) {
	f := makeV20Fixture(t, t.TempDir())
	snapshot := snapshotDBBytes(t, f.path)
	if s, err := Open(f.path); !errors.Is(err, ErrSchemaMigrationRequired) {
		if s != nil {
			s.Close()
		}
		t.Fatalf("unauthorized open: %v; want ErrSchemaMigrationRequired", err)
	}
	assertDBBytesUnchanged(t, f.path, snapshot)
	writeSentinel(t, f.dir, 20, schemaVersion)
	mark := TrailMark(t)
	s, err := Open(f.path)
	if err != nil {
		t.Fatalf("authorized ordinary open: %v", err)
	}
	if s.StoreID() != f.storeID || userVersion(t, s.db) != schemaVersion {
		t.Errorf("store id %s at v%d; want %s at v%d", s.StoreID(), userVersion(t, s.db), f.storeID, schemaVersion)
	}
	if digest, text := busySendCoreDigest(t, s.db); digest != busySendCoreV1Digest {
		t.Errorf("migrated core declarations differ from the permanent contract:\n%s", text)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	assertSentinel(t, f.dir, false)
	lines := trailEventsSince(t, mark, "ad.schema.migrated")
	if len(lines) != 1 {
		t.Fatalf("ad.schema.migrated lines = %d; want 1", len(lines))
	}
	assertTrailStr(t, lines[0], "message", fmt.Sprintf("migrated 20→%d, consumed authorization", schemaVersion))
	if got := dumpBeforeV21(t, f.path); !reflect.DeepEqual(got, f.before) {
		t.Errorf("the hop changed a v20 value:\n before %v\n after  %v", f.before, got)
	}
	assertV21Backfill(t, f.path, f.ids...)
	withRaw(t, f.path, func(db *sql.DB) {
		for table, want := range map[string]int{"busy_conversations": len(f.ids), "busy_send_launches": len(f.ids),
			"busy_send_launch_modes": 4, "busy_send_launch_escapes": 8,
			"busy_send_launch_processes": 0, "busy_send_messages": 0, "busy_send_results": 0, "busy_send_handovers": 0,
			"busy_send_prompts": 0, "busy_send_handover_members": 0, "busy_send_forces": 0, "busy_send_force_members": 0, "busy_send_pause_claims": 0} {
			var n int
			if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil || n != want {
				t.Errorf("%s rows = %d, %v; want %d (nothing invented)", table, n, err, want)
			}
		}
	})

	after := dbDump(t, f.path)
	for _, stale := range []bool{false, true} {
		if stale {
			writeSentinel(t, f.dir, 20, schemaVersion)
		}
		mark := TrailMark(t)
		s, err := Open(f.path)
		if err != nil {
			t.Fatalf("reopen (stale sentinel %v): %v", stale, err)
		}
		s.Close()
		if !reflect.DeepEqual(after, dbDump(t, f.path)) {
			t.Errorf("reopen (stale sentinel %v) changed the store", stale)
		}
		assertSentinel(t, f.dir, stale)
		for _, ev := range []string{"ad.schema.migrated", "ad.schema.authorization_mismatch", "ad.schema.authorization_delete_failed"} {
			if n := len(trailEventsSince(t, mark, ev)); n != 0 {
				t.Errorf("reopen (stale sentinel %v) emitted %d %s; want 0", stale, n, ev)
			}
		}
	}
	reapplyHops(t, f.path, 20)
	if !reflect.DeepEqual(after, dbDump(t, f.path)) {
		t.Error("the hop run again changed the migrated store")
	}
	s, err = Open(f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	assertSpawnsCoreChecks(t, s.db, v20ClaudeID)
}

// F5 (Main Agent decision, 2026-11-13): a historical row with no agent, NULL
// or empty, migrates; the hop leaves spawns.agent as it was and records its
// conversation and Launch with provider claude, as readers take such a row,
// and claude's coverage, covered.
func TestV21MigrationLegacyAgentRows(t *testing.T) {
	dir := t.TempDir()
	path := makeVersionedDB(t, dir, 20)
	rewriteTableDDL(t, path, "spawns", "agent TEXT NOT NULL", "agent TEXT")
	withRaw(t, path, func(db *sql.DB) {
		for id, agent := range map[string]any{"legacy-null": nil, "legacy-empty": ""} {
			insertRow(t, db, "spawns", map[string]any{"instance_id": id, "agent": agent, "state": StateEnded, "cwd": "/tmp",
				"tmux_session_name": "ad-" + id, "relay_mode": "off", "life_number": 1, "launch_token": "0123456789abcdef"})
		}
	})
	writeSentinel(t, dir, 20, schemaVersion)
	s, err := openMigrationTestStore(t, path)
	if err != nil {
		t.Fatalf("migration of historical agent rows: %v", err)
	}
	s.Close()
	assertV21Backfill(t, path, "legacy-null", "legacy-empty")
	for id, want := range map[string]string{"legacy-null": "NULL", "legacy-empty": "''"} {
		if got := readRawSpawn(t, path, id, []string{"agent"})["agent"]; got != want {
			t.Errorf("%s: agent %s; want %s kept", id, got, want)
		}
	}
}

// TestV21MigrationHeldUntilRoute (SR-7.2, SR-9.1, SR-6.3; Task t2.xis.oo.2o):
// the authorized hop records the existing waiting Claude Launch covered with
// no route or wake endpoint, and a Codex one uncovered. The Claude row then
// admits Messages Held route_unavailable (the Codex row refuses them); they
// survive close and reopen, and neither the open nor any read writes or
// hands anything over. A guarded resume stages a new Launch whose own route
// and wake endpoint the publication records: the Messages keep their order
// and options, and their wait leaves route_unavailable (agent_starting while
// the row is pending). No startup input is stored to replay.
func TestV21MigrationHeldUntilRoute(t *testing.T) {
	f := makeV20Fixture(t, t.TempDir())
	writeSentinel(t, f.dir, 20, schemaVersion)
	s, err := Open(f.path)
	if err != nil {
		t.Fatalf("authorized open: %v", err)
	}
	assertV21Backfill(t, f.path, v20ClaudeID, v20CodexID)
	const caller = "3b9e1c4a-7d2f-4e8b-9a6c-1f0e2d3c4b5a"
	confirmed := true
	admit := func(id, text string, pair bool) error {
		req := BusySendAdmissionRequest{InstanceID: id, Text: []byte(text)}
		if pair {
			req.CallerUUID, req.DeliveryRequestID, req.Confirmed = caller, "m-"+text, &confirmed
		}
		_, err := s.AdmitBusySend(req)
		return err
	}
	var refusal *BusySendRefusal
	if err := admit(v20CodexID, "codex", false); !errors.As(err, &refusal) || refusal.Reason != BusySendRefuseAgentNotCovered {
		t.Errorf("Codex admission = %v; want refused agent_not_covered", err)
	}
	for i, text := range []string{"first", "second ✓\nline", "third"} {
		if err := admit(v20ClaudeID, text, i != 1); err != nil {
			t.Fatalf("admit %q: %v", text, err)
		}
	}
	wait := func(st *Store, want BusySendWaitReason) {
		t.Helper()
		o, err := st.ReadBusySendObservation(v20ClaudeID, time.Second)
		if err != nil || o == nil || o.HeldCount != 3 || o.FirstWait == nil || o.FirstWait.Reason != want {
			t.Fatalf("observation = %+v, %v; want 3 Held, the first waiting %s", o, err, want)
		}
		r, err := st.ReadBusySendResult(caller, "m-first")
		if err != nil || r.Result == nil || r.Result.Class != BusySendClassHeld || r.Result.Final {
			t.Fatalf("m-first Result = %+v, %v; want Held", r, err)
		}
	}
	held := func() [][3]string {
		t.Helper()
		var out [][3]string
		withRaw(t, f.path, func(db *sql.DB) {
			rows, err := db.Query(`SELECT text, send_mode, confirmed FROM busy_send_messages WHERE instance_id = ? AND place = 'held'
			    ORDER BY accepted_seq`, v20ClaudeID)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			for rows.Next() {
				var m [3]string
				if err := rows.Scan(&m[0], &m[1], &m[2]); err != nil {
					t.Fatal(err)
				}
				out = append(out, m)
			}
		})
		return out
	}
	wait(s, BusySendWaitRouteUnavailable)
	accepted := held()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	before := dbDump(t, f.path)
	s, err = Open(f.path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	wait(s, BusySendWaitRouteUnavailable)
	if _, err := s.ReadBusySendRouteFacts(context.Background(), v20ClaudeID, HookGate{}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, dbDump(t, f.path)) {
		t.Fatal("reopen and reads changed the store")
	}
	busyLaunchWantCount(t, s, 0, `SELECT count(*) FROM busy_send_handovers`)

	busyLaunchFinish(t, s, v20ClaudeID)
	const token = "a1b2c3d4e5f60718"
	row := busyLaunchRow(t, s, v20ClaudeID)
	res, version, err := s.MoveToPendingWithExit(v20ClaudeID, row.Snapshot, 2000, token, "", "", LaunchOwner{}, BusySendDefaultsChoice{},
		busyLaunchExit(t, s, v20ClaudeID))
	if err != nil || res != CondApplied {
		t.Fatalf("resume = %v, %v; want applied", res, err)
	}
	if res, err := s.RecordLaunchIdentity(v20ClaudeID, version, token, LaunchIdentity{}); err != nil || res != CondApplied {
		t.Fatalf("publication = %v, %v; want applied", res, err)
	}
	wake, err := BusySendWakePath(s.StoreID(), token)
	if err != nil {
		t.Fatal(err)
	}
	facts, err := s.ReadBusySendRouteFacts(context.Background(), v20ClaudeID, HookGate{})
	if err != nil || facts.Kind != BusySendLaunchResume || !facts.Covered || facts.WakePath != wake ||
		facts.Destination.RouteKind != BusySendRouteClaudeHooks {
		t.Fatalf("resumed route facts = %+v, %v; want the covered resume Launch's own route and wake %s", facts, err, wake)
	}
	wait(s, BusySendWaitAgentStarting)
	if got := held(); !reflect.DeepEqual(got, accepted) {
		t.Errorf("Held after the resume = %q; want %q, order and options kept", got, accepted)
	}
	busyLaunchWantCount(t, s, 0, `SELECT count(*) FROM busy_send_handovers`)
}

// TestV21MigrationCompatibleReentry: over a compatible partial core holding
// 'pre' bound by an earlier run (NULL token on both sides), 'rec' bound to
// seedBusySendRecords' covered records, v20CodexID unbound beside a retired
// first conversation and v20DroidID bound with a complete synthetic snapshot, the
// hop keeps every recorded core row and bound row exactly, records only the
// unbound rows (Codex's next generation). Different established capability
// numbers and ordered literals survive, even for a locally unknown provider.
func TestV21MigrationCompatibleReentry(t *testing.T) {
	f := makeV20Fixture(t, t.TempDir())
	preAddV21Core(t, f.path, allV21Core()...)
	bindV21Row(t, f.path, "pre", 1, BusySendNotCovered, nil)
	other := v21ClaudeCapabilities()
	other.HeldCount, other.HeldBytes = 17, 123456
	other.Modes[0].MessageBytes, other.Modes[0].PromptBytes = 321, 4321
	other.Modes[0].Wrapping = BusySendWrapping{Prefix: "0\n<", Suffix: ">\n9", Escapes: []BusySendEscape{{"<", "&lt;0\n"}, {"&", "&amp;"}}}
	bindV21Row(t, f.path, v20DroidID, 1, BusySendCovered, nil, other)
	var rec busySendRecords
	withRaw(t, f.path, func(db *sql.DB) {
		insertRow(t, db, "spawns", map[string]any{"instance_id": "rec", "agent": AgentClaude, "state": StateWaiting, "cwd": "/work/rec",
			"tmux_session_name": "ad-rec", "relay_mode": "on", "life_number": recLife, "launch_token": recToken, "busy_send": "covered", "turn_starts": 3})
		rec = seedBusySendRecords(t, db, f.storeID, "rec")
		mustExec(t, db, `UPDATE spawns SET busy_conversation_id = ? WHERE instance_id = 'rec'`, rec.conversation)
		insertRow(t, db, "busy_conversations", map[string]any{"conversation_id": "codex-retired", "store_id": f.storeID, "instance_id": v20CodexID,
			"generation": 1, "publication": "retired", "provider": AgentCodex, "recorded_at_ms": 100, "published_at_ms": 100,
			"retired_at_ms": 200, "retired_reason": "record_deleted"})
	})
	var cols []string
	withRaw(t, f.path, func(db *sql.DB) { cols = tableColumnNames(t, db, "spawns") })
	boundRows := map[string]map[string]string{"pre": readRawSpawn(t, f.path, "pre", cols), "rec": readRawSpawn(t, f.path, "rec", cols), v20DroidID: readRawSpawn(t, f.path, v20DroidID, cols)}
	before := dbDump(t, f.path)
	writeSentinel(t, f.dir, 20, schemaVersion)
	s, err := openMigrationTestStore(t, f.path)
	if err != nil {
		t.Fatalf("compatible reentry: %v", err)
	}
	s.Close()
	after := dbDump(t, f.path)
	withRaw(t, f.path, func(db *sql.DB) {
		got, err := readBusySendLaunchCapabilities(context.Background(), db, f.storeID, "launch-conv-"+v20DroidID+"-1")
		if err != nil || !reflect.DeepEqual(got, other) {
			t.Fatalf("retained unknown-provider snapshot = %+v, %v; want %+v", got, err, other)
		}
		if got.Modes[0].Measure([]byte("<&")) != 17 {
			t.Fatal("stored ordered escapes did not retain original-body byte semantics")
		}
	})
	for key, value := range before {
		if strings.HasPrefix(key, "rows busy_") {
			rows, got := value.([]string), after[key].([]string)
			if len(got) < len(rows) || !reflect.DeepEqual(got[:len(rows)], rows) {
				t.Errorf("%s: recorded core rows changed:\n before %v\n after  %v", key, rows, got)
			}
		}
	}
	for id, want := range boundRows {
		if got := readRawSpawn(t, f.path, id, cols); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: bound row changed:\n got  %v\n want %v", id, got, want)
		}
	}
	assertV21Backfill(t, f.path, v20ClaudeID, v20CursorID, v20OpenCodeID)
	withRaw(t, f.path, func(db *sql.DB) {
		var generation int
		var published, launches int
		err := db.QueryRow(`SELECT c.generation, c.publication = 'published', (SELECT count(*) FROM busy_send_launches l
			WHERE l.conversation_id = c.conversation_id AND l.phase = 'current') FROM busy_conversations c JOIN spawns s
			ON s.busy_conversation_id = c.conversation_id WHERE s.instance_id = ?`, v20CodexID).Scan(&generation, &published, &launches)
		if err != nil || generation != 2 || published != 1 || launches != 1 {
			t.Errorf("%s: bound to generation %d (published %d, current Launches %d), %v; want the published generation 2 with its Launch",
				v20CodexID, generation, published, launches, err)
		}
		var conv, launch, row string
		err = db.QueryRow(`SELECT c.busy_send, l.busy_send, s.busy_send FROM spawns s JOIN busy_conversations c ON c.conversation_id = s.busy_conversation_id
			JOIN busy_send_launches l ON l.conversation_id = c.conversation_id WHERE s.instance_id = ?`, v20DroidID).Scan(&conv, &launch, &row)
		if err != nil || conv != "covered" || launch != "covered" || row != "covered" {
			t.Errorf("%s: coverage %s/%s/%s, %v; want its established covered kept on all three", v20DroidID, conv, launch, row, err)
		}
	})
	reapplyHops(t, f.path, 20)
	if !reflect.DeepEqual(after, dbDump(t, f.path)) {
		t.Error("a second run of the hop changed the store")
	}
}

// TestV21MigrationRefusesBeforeMutation: an incompatible core object or
// spawns column (wrong case, type, default or CHECK, another declaration), an
// invalid existing value, an inconsistent or ambiguous binding (F1: a bound
// conversation or Launch disagreeing with the row on provider, life or
// launch token, NULL-safe), or core facts of another store refuse the hop
// with ErrSchemaMismatch before any change: the v20 schema, data, stamp and
// sentinel stay (SR-7.2).
func TestV21MigrationRefusesBeforeMutation(t *testing.T) {
	exec := func(stmts ...string) func(*testing.T, string) {
		return func(t *testing.T, path string) {
			withRaw(t, path, func(db *sql.DB) {
				db.SetMaxOpenConns(1) // keep ignore_check_constraints on the connection that writes
				for _, q := range stmts {
					mustExec(t, db, q)
				}
			})
		}
	}
	core := allV21Core()
	columns := core[len(busySendCoreObjects):]
	withCore := func(then ...string) func(*testing.T, string) {
		return func(t *testing.T, path string) {
			preAddV21Core(t, path, core...)
			exec(then...)(t, path)
		}
	}
	bound := func(coverage BusySendCoverage, change func(conv, launch map[string]any), first ...string) func(*testing.T, string) {
		return func(t *testing.T, path string) {
			exec(first...)(t, path)
			preAddV21Core(t, path, core...)
			bindV21Row(t, path, "pre", 1, coverage, change)
		}
	}
	other := "0000000000000000"
	cases := []struct {
		name    string
		arrange func(*testing.T, string)
		want    string
	}{
		{"wrong-case table", exec(strings.Replace(busyConversationsDDL, "busy_conversations (", "BUSY_CONVERSATIONS (", 1)), "incompatible busy_conversations"},
		{"another table declaration", exec(`CREATE TABLE busy_send_messages (message_id TEXT PRIMARY KEY)`), "incompatible busy_send_messages"},
		{"another index declaration", func(t *testing.T, path string) {
			preAddV21Core(t, path, "busy_send_forces")
			exec(`CREATE INDEX idx_busy_send_forces_instance ON busy_send_forces(conversation_id)`)(t, path)
		}, "incompatible idx_busy_send_forces_instance"},
		{"wrong-case coverage column", exec(`ALTER TABLE spawns ADD COLUMN Busy_Send TEXT NOT NULL DEFAULT 'not_covered' CHECK(Busy_Send IN ('not_covered', 'covered'))`), "incompatible spawns.busy_send"},
		{"coverage with another default", exec(`ALTER TABLE spawns ADD COLUMN busy_send TEXT NOT NULL DEFAULT 'covered' CHECK(busy_send IN ('not_covered', 'covered'))`), "incompatible spawns.busy_send"},
		{"coverage without its CHECK", exec(`ALTER TABLE spawns ADD COLUMN busy_send TEXT NOT NULL DEFAULT 'not_covered'`), "incompatible spawns.busy_send"},
		{"coverage with another CHECK", exec(`ALTER TABLE spawns ADD COLUMN busy_send TEXT NOT NULL DEFAULT 'not_covered' CHECK(busy_send IN ('not_covered'))`), "incompatible spawns.busy_send"},
		{"Turn Start counter of another type", exec(`ALTER TABLE spawns ADD COLUMN turn_starts TEXT NOT NULL DEFAULT 0`), "incompatible spawns.turn_starts"},
		{"Turn Start counter without its CHECK", exec(`ALTER TABLE spawns ADD COLUMN turn_starts INTEGER NOT NULL DEFAULT 0`), "incompatible spawns.turn_starts"},
		{"Turn Start counter with another CHECK", exec(`ALTER TABLE spawns ADD COLUMN turn_starts INTEGER NOT NULL DEFAULT 0 CHECK(turn_starts >= 0)`), "incompatible spawns.turn_starts"},
		{"an unknown coverage code", func(t *testing.T, path string) {
			preAddV21Core(t, path, columns...)
			exec(`PRAGMA ignore_check_constraints = ON`, `UPDATE spawns SET busy_send = 'maybe'`)(t, path)
		}, "invalid spawns.busy_send"},
		{"a negative Turn Start counter", func(t *testing.T, path string) {
			preAddV21Core(t, path, columns...)
			exec(`PRAGMA ignore_check_constraints = ON`, `UPDATE spawns SET turn_starts = -1`)(t, path)
		}, "invalid spawns.turn_starts"},
		{"a negative life", exec(`UPDATE spawns SET life_number = -1`), "spawns.life_number"},
		{"a binding with nothing recorded", func(t *testing.T, path string) {
			preAddV21Core(t, path, columns...)
			exec(`UPDATE spawns SET busy_conversation_id = 'orphan'`)(t, path)
		}, "ambiguous spawns.busy_conversation_id"},
		{"a binding to an unpublished conversation", bound(BusySendNotCovered, func(conv, _ map[string]any) {
			conv["publication"], conv["published_at_ms"] = "candidate", nil
		}), "ambiguous"},
		{"a bound conversation of another provider", bound(BusySendNotCovered, func(conv, _ map[string]any) { conv["provider"] = AgentCursor }), "spawns.busy_conversation_id"},
		{"a bound Launch of another provider", bound(BusySendNotCovered, func(_, launch map[string]any) { launch["provider"] = AgentCursor }), "spawns.busy_conversation_id"},
		{"a bound Launch of another life", bound(BusySendNotCovered, func(_, launch map[string]any) { launch["life_number"] = 1 }), "spawns.busy_conversation_id"},
		{"a bound Launch with a token the row lacks", bound(BusySendNotCovered, func(_, launch map[string]any) { launch["launch_token"] = "0123456789abcdef" }), "spawns.busy_conversation_id"},
		{"a bound Launch lacking the row's token", bound(BusySendNotCovered, func(_, launch map[string]any) { launch["launch_token"] = nil },
			`UPDATE spawns SET launch_token = '0123456789abcdef' WHERE instance_id = 'pre'`), "spawns.busy_conversation_id"},
		{"a bound Launch with another token", bound(BusySendNotCovered, func(_, launch map[string]any) { launch["launch_token"] = "fedcba9876543210" },
			`UPDATE spawns SET launch_token = '0123456789abcdef' WHERE instance_id = 'pre'`), "spawns.busy_conversation_id"},
		{"a bound row covered beyond its conversation", bound(BusySendCovered, func(conv, _ map[string]any) { conv["busy_send"] = "not_covered" }), "spawns.busy_conversation_id"},
		{"a published conversation no row binds", func(t *testing.T, path string) {
			bound(BusySendNotCovered, nil)(t, path)
			exec(`UPDATE spawns SET busy_conversation_id = NULL`)(t, path)
		}, "ambiguous busy_conversations"},
		{"a pending Launch", withCore(`INSERT INTO busy_conversations (conversation_id, store_id, instance_id, generation, publication, provider,
			recorded_at_ms, published_at_ms, retired_at_ms, retired_reason) SELECT 'retired', value, 'pre', 1, 'retired', 'claude', 1, 1, 2,
			'record_deleted' FROM store_meta WHERE key = 'store_id'`,
			`INSERT INTO busy_send_launches (launch_id, store_id, instance_id, conversation_id, life_number, provider, busy_send, kind, phase,
			recorded_at_ms) SELECT 'pending', value, 'pre', 'retired', 0, 'claude', 'not_covered', 'spawn', 'uncertain', 1
			FROM store_meta WHERE key = 'store_id'`), "ambiguous busy_send_launches"},
		{"unbound covered provider has no author", withCore(`UPDATE spawns SET agent='droid',busy_send='covered' WHERE instance_id='pre'`), "no covering capability table row"},
		{"covered snapshot lacks modes", func(t *testing.T, path string) {
			bound(BusySendCovered, nil)(t, path)
			exec(`DELETE FROM busy_send_launch_escapes`, `DELETE FROM busy_send_launch_modes`)(t, path)
		}, "invalid busy-send Launch capabilities"},
		{"snapshot lacks its trailing escape", func(t *testing.T, path string) {
			bound(BusySendCovered, nil)(t, path)
			exec(`DELETE FROM busy_send_launch_escapes WHERE send_mode='wait_turn' AND escape_index=1`)(t, path)
		}, "invalid busy-send Launch capabilities"},
		{"a core identity of another store", exec(`INSERT INTO store_meta (key, value) VALUES ('busy_send_core', 'busy-send-core-v1:` + other + `')`), "incompatible busy-send core identity"},
		{"core records of another store", bound(BusySendNotCovered, func(conv, launch map[string]any) {
			conv["store_id"], launch["store_id"] = other, other
		}), "busy_conversations names another store"},
		{"a core record naming a missing one", func(t *testing.T, path string) {
			bound(BusySendNotCovered, nil)(t, path)
			exec(`INSERT INTO busy_send_launch_processes (launch_id, role, store_id, pid, start_time, recorded_at_ms)
				SELECT 'no-such-launch', 'agent', value, 1, 'x', 0 FROM store_meta WHERE key = 'store_id'`)(t, path)
		}, "busy_send_launch_processes refers to a missing record"},
	}
	// b.kii PM1 repair 1: a recognized partial schema can still contain unsafe
	// data. Validate it before the exempt hop writes anything or consumes consent.
	for _, literal := range invalidV21RawLiterals() {
		cases = append(cases, struct {
			name    string
			arrange func(*testing.T, string)
			want    string
		}{"invalid Raw " + literal.name, func(t *testing.T, path string) {
			bound(BusySendCovered, nil)(t, path)
			exec(`PRAGMA ignore_check_constraints=ON`, fmt.Sprintf(`UPDATE busy_send_launch_escapes SET raw=CAST(x'%x' AS TEXT) WHERE send_mode='wait_turn' AND escape_index=0`, literal.raw))(t, path)
		}, "invalid busy-send Launch capabilities"})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := makeVersionedDB(t, dir, 20)
			seedV16PermissionHistory(t, path)
			tc.arrange(t, path)
			before := dbDump(t, path)
			ddlBefore := schemaDeclarations(t, path)
			writeSentinel(t, dir, 20, schemaVersion)
			sentinelBefore, err := os.ReadFile(filepath.Join(dir, sentinelFilename))
			if err != nil {
				t.Fatal(err)
			}
			s, err := openMigrationTestStore(t, path)
			if s != nil {
				s.Close()
			}
			if !errors.Is(err, ErrSchemaMismatch) || errors.Is(err, ErrSchemaMigrationRequired) || !strings.Contains(err.Error(), "v20→v21") ||
				!strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Open = %v; want a v20→v21 schema mismatch naming %q", err, tc.want)
			}
			if readUserVersion(t, path) != 20 || !reflect.DeepEqual(before, dbDump(t, path)) || !slices.Equal(ddlBefore, schemaDeclarations(t, path)) {
				t.Error("the refused hop changed the schema, data or stamp")
			}
			assertSentinel(t, dir, true)
			if got, err := os.ReadFile(filepath.Join(dir, sentinelFilename)); err != nil || string(got) != string(sentinelBefore) {
				t.Errorf("the refused hop changed the sentinel: %q, %v; want %q", got, err, sentinelBefore)
			}
		})
	}
}

// TestV21MigrationUncertainCommit: a hop whose COMMIT landed but went
// unconfirmed (InputMigrationFaults.AfterStepCommit) fails the open as a
// migration incomplete, never a refusal, and keeps the now-stale sentinel. The
// next ordinary open finds v21, recognizes the committed core and opens with
// no migration, audit or consumption: no backfill repeats, a fact written
// after it is never overwritten, and the stale sentinel authorizes nothing.
func TestV21MigrationUncertainCommit(t *testing.T) {
	f := makeV20Fixture(t, t.TempDir())
	writeSentinel(t, f.dir, 20, schemaVersion)
	window := migrationTestWindow(t, f.path)
	lost := errors.New("injected unconfirmed commit")
	window.faults = InputMigrationFaults{AfterStepCommit: func(from int) error {
		if from == 20 {
			return lost
		}
		return nil
	}}
	s, err := openDBWithInputWindow(f.path, DefaultBusyTimeoutMs, window)
	if s != nil {
		s.Close()
	}
	if !errors.Is(err, lost) || !errors.Is(err, errCommitUnconfirmed) || errors.Is(err, ErrSchemaMigrationRequired) {
		t.Fatalf("Open = %v; want the unconfirmed commit of the hop", err)
	}
	assertSentinel(t, f.dir, true)
	if v := readUserVersion(t, f.path); v != schemaVersion {
		t.Fatalf("user_version %d; want the committed %d", v, schemaVersion)
	}
	bound := assertV21Backfill(t, f.path, f.ids...)
	committed := dbDump(t, f.path)
	for i := 0; i < 2; i++ {
		mark := TrailMark(t)
		s, err := Open(f.path)
		if err != nil {
			t.Fatalf("reopen %d: %v", i, err)
		}
		if i == 0 {
			// A later writer's fact on the committed core.
			mustExec(t, s.db, `UPDATE busy_conversations SET accepted_seq = 7, revision = 2 WHERE instance_id = ?`, v20ClaudeID)
		}
		s.Close()
		if got := assertV21Recorded(t, f.path, v20ClaudeID); got.conversation != bound[v20ClaudeID].conversation || got.acceptedSeq != 7 {
			t.Errorf("reopen %d: conversation %+v; want %s with the later accepted_seq 7 kept", i, got, bound[v20ClaudeID].conversation)
		}
		for _, table := range []string{"busy_send_launches", "busy_send_launch_modes", "busy_send_launch_escapes"} {
			if got := dbDump(t, f.path)["rows "+table]; !reflect.DeepEqual(got, committed["rows "+table]) {
				t.Errorf("reopen %d changed the committed %s", i, table)
			}
		}
		assertSentinel(t, f.dir, true)
		if n := len(trailEventsSince(t, mark, "ad.schema.migrated")); n != 0 {
			t.Errorf("reopen %d emitted %d ad.schema.migrated; want 0", i, n)
		}
	}
}

// v21Recorded is one instance's conversation as the store records it.
type v21Recorded struct {
	conversation string
	count        int
	acceptedSeq  int
}

// assertV21Recorded reads id's conversations in the store at path, failing
// unless it has exactly one.
func assertV21Recorded(t *testing.T, path, id string) v21Recorded {
	t.Helper()
	var r v21Recorded
	withRaw(t, path, func(db *sql.DB) {
		if err := db.QueryRow(`SELECT max(conversation_id), count(*), max(accepted_seq) FROM busy_conversations WHERE instance_id = ?`, id).
			Scan(&r.conversation, &r.count, &r.acceptedSeq); err != nil || r.count != 1 {
			t.Errorf("%s: %d conversations, %v; want one", id, r.count, err)
		}
	})
	return r
}

// TestV21OpenRefusesUnrecognizedCore: at v21, a missing or foreign identity,
// a missing core table, index or spawns column, another table declaration, or
// a spawns column whose CHECK is missing or altered (F2) refuses the ordinary
// open with ErrSchemaMismatch "busy-send core not recognized", and open
// rebuilds and repairs nothing (SR-7.2).
func TestV21OpenRefusesUnrecognizedCore(t *testing.T) {
	exec := func(q string) func(*testing.T, string) {
		return func(t *testing.T, path string) { withRaw(t, path, func(db *sql.DB) { mustExec(t, db, q) }) }
	}
	rewrite := func(table, old, new string) func(*testing.T, string) {
		return func(t *testing.T, path string) { rewriteTableDDL(t, path, table, old, new) }
	}
	for _, tc := range []struct {
		name    string
		arrange func(*testing.T, string)
	}{
		{"identity missing", exec(`DELETE FROM store_meta WHERE key = 'busy_send_core'`)},
		{"identity of another store", exec(`UPDATE store_meta SET value = 'busy-send-core-v1:0000000000000000' WHERE key = 'busy_send_core'`)},
		{"a core table missing", exec(`DROP TABLE busy_send_pause_claims`)},
		{"pre-capability v21 is never converted", func(t *testing.T, path string) {
			withRaw(t, path, func(db *sql.DB) {
				mustExec(t, db, `DROP TABLE busy_send_launch_escapes`)
				mustExec(t, db, `DROP TABLE busy_send_launch_modes`)
				mustExec(t, db, `DROP TABLE busy_send_launches`)
				mustExec(t, db, preCapabilitiesV21LaunchDDL)
				for _, o := range busySendCoreObjects {
					if o.kind == "index" && strings.Contains(o.ddl, " ON busy_send_launches(") {
						mustExec(t, db, o.ddl)
					}
				}
			})
		}},
		{"a core index missing", exec(`DROP INDEX idx_busy_send_forces_open`)},
		{"pre-monotone v21 is never converted", func(t *testing.T, path string) {
			withRaw(t, path, func(db *sql.DB) {
				mustExec(t, db, `DROP TABLE busy_send_launch_escapes`)
				mustExec(t, db, preMonotoneV21EscapesDDL)
			})
		}},
		{"a spawns column missing", exec(`ALTER TABLE spawns DROP COLUMN busy_conversation_id`)},
		{"a table declared otherwise", rewrite("busy_send_pause_claims", "'standing', 'released', 'ended')", "'standing', 'released', 'ended', 'lost')")},
		{"unreleased Cursor launch route", rewrite("busy_send_launches", "'claude_hooks', 'codex_app_server'", "'claude_hooks', 'codex_app_server', 'cursor_acp'")},
		{"unreleased Cursor handover route", rewrite("busy_send_handovers", "'claude_hooks', 'codex_app_server'", "'claude_hooks', 'codex_app_server', 'cursor_acp'")},
		{"unreleased gateway process", rewrite("busy_send_launch_processes", "'agent', 'app_server', 'tui', 'helper'", "'agent', 'app_server', 'tui', 'helper', 'gateway'")},
		{"unreleased Cursor message evidence", rewrite("busy_send_messages", "'codex_item_started', 'codex_thread_items'", "'codex_item_started', 'codex_thread_items', 'cursor_end_turn'")},
		{"unreleased Cursor prompt evidence", rewrite("busy_send_prompts", "'codex_item_started', 'codex_thread_items'", "'codex_item_started', 'codex_thread_items', 'cursor_end_turn'")},
		{"unreleased gateway exit", rewrite("busy_send_pause_claims", "'sigterm', 'server_shutdown'", "'sigterm', 'server_shutdown', 'gateway_exit'")},
		{"coverage CHECK missing", rewrite("spawns", " CHECK(busy_send IN ('not_covered', 'covered'))", "")},
		{"Turn Start CHECK altered", rewrite("spawns", "CHECK(typeof(turn_starts) = 'integer' AND turn_starts >= 0)", "CHECK(turn_starts >= 0)")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fresh, path := openTempStore(t)
			fresh.Close()
			tc.arrange(t, path)
			before := dbDump(t, path)
			ddlBefore := schemaDeclarations(t, path)
			s, err := Open(path)
			if s != nil {
				s.Close()
			}
			if !errors.Is(err, errBusySendCoreUnrecognized) || !errors.Is(err, ErrSchemaMismatch) || !strings.Contains(err.Error(), "busy-send core not recognized") {
				t.Fatalf("Open = %v; want ErrSchemaMismatch, busy-send core not recognized", err)
			}
			if readUserVersion(t, path) != schemaVersion || !reflect.DeepEqual(before, dbDump(t, path)) || !slices.Equal(ddlBefore, schemaDeclarations(t, path)) {
				t.Error("the refused open changed the store")
			}
			assertSentinel(t, filepath.Dir(path), false)
		})
	}
}

// failingReads is a coreSQL over db whose read number fail (from 1) fails, as
// on a busy or failing store (errInjectedRead, or a cancelled single-row
// read), recording that cause in injected; its other reads reach db.
type failingReads struct {
	db          *sql.DB
	fail, reads int
	injected    error
}

var errInjectedRead = errors.New("injected read failure: database is locked")

func (f *failingReads) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	if f.reads++; f.reads == f.fail {
		f.injected = errInjectedRead
		return nil, errInjectedRead
	}
	return f.db.QueryContext(ctx, q, args...)
}

func (f *failingReads) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	if f.reads++; f.reads == f.fail {
		ctx, cancel := context.WithCancel(ctx)
		cancel() // a *sql.Row carries its error only from a real query
		f.injected = context.Canceled
		return f.db.QueryRowContext(ctx, q, args...)
	}
	return f.db.QueryRowContext(ctx, q, args...)
}

// F3: a read that fails while open recognizes the core (its identity,
// declarations or spawns columns) or while the hop validates is reported as
// that failure, never as ErrSchemaMismatch, the incompatibility callers take
// to need an administrator; the same store recognizes once reads succeed.
func TestBusySendCoreReadFailuresAreNotMismatch(t *testing.T) {
	s, _ := openTempStore(t)
	for fail := 1; ; fail++ {
		q := &failingReads{db: s.db, fail: fail}
		err := recognizeBusySendCore(context.Background(), q, s.StoreID())
		if q.reads < fail { // every read succeeded
			if err != nil {
				t.Fatalf("recognition with every read succeeding: %v", err)
			}
			break
		}
		if !errors.Is(err, q.injected) || errors.Is(err, ErrSchemaMismatch) {
			t.Errorf("read %d failing: %v; want its cause %v, never ErrSchemaMismatch", fail, err, q.injected)
		}
	}
	path := makeVersionedDB(t, t.TempDir(), 20)
	db := openRaw(t, path)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := validateV20toV21(ctx, tx); err == nil || errors.Is(err, ErrSchemaMismatch) || !errors.Is(err, context.Canceled) {
		t.Errorf("hop validation with its reads failing: %v; want the read failure, never ErrSchemaMismatch", err)
	}
}

// TestV21RestoreOnlyRecovery follows docs/migration-guide.md §5 for v21: no
// reverse DDL; the stopped-writer pre-upgrade DB/WAL/SHM set restores the v20
// store with its identity and loses every later write, the restored copy
// needs exact authorization again, and its remigration records new
// conversations and Launches and no Message, pair or force: missing proof in
// the restored copy grants nothing. The upgraded copy, with its accepted and
// uncertain custody and legacy input evidence, stays intact for
// reconciliation.
func TestV21RestoreOnlyRecovery(t *testing.T) {
	f := makeV20Fixture(t, t.TempDir())
	backup := readDBFileSet(t, f.path)
	writeSentinel(t, f.dir, 20, schemaVersion)
	s, err := Open(f.path)
	if err != nil {
		t.Fatal(err)
	}
	seedBusySendRecords(t, s.db, s.StoreID(), "rec")
	mustExec(t, s.db, `UPDATE spawns SET labels = '{"after":"upgrade"}' WHERE instance_id = ?`, v20ClaudeID)
	s.Close()
	upgradedBound := assertV21Backfill(t, f.path, f.ids...)
	upgraded := dbDump(t, f.path)

	restoredDir := t.TempDir()
	restored := filepath.Join(restoredDir, "state.db")
	for name, data := range backup {
		if err := os.WriteFile(restored+strings.TrimPrefix(name, f.path), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(f.before, dbDump(t, restored)) || assertOneStoreID(t, restored) != f.storeID {
		t.Fatal("the restored set is not the pre-upgrade store")
	}
	snapshot := snapshotDBBytes(t, restored)
	if opened, err := Open(restored); !errors.Is(err, ErrSchemaMigrationRequired) {
		if opened != nil {
			opened.Close()
		}
		t.Fatalf("restored v20 opened without authorization: %v", err)
	}
	assertDBBytesUnchanged(t, restored, snapshot)
	writeSentinel(t, restoredDir, 20, schemaVersion)
	reopened, err := Open(restored)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.StoreID() != f.storeID {
		t.Error("restore and remigration replaced the store id")
	}
	reopened.Close()
	for id, got := range assertV21Backfill(t, restored, f.ids...) {
		if was := upgradedBound[id]; got.conversation == was.conversation || got.launch == was.launch {
			t.Errorf("%s: remigration recorded %+v beside the upgraded copy's %+v; want a new conversation and a new Launch", id, got, was)
		}
	}
	if got := readRawSpawn(t, restored, v20ClaudeID, []string{"labels"})["labels"]; got == `'{"after":"upgrade"}'` {
		t.Error("the restored store kept a write made after the upgrade")
	}
	withRaw(t, restored, func(db *sql.DB) {
		for _, table := range []string{"busy_send_messages", "busy_send_results", "busy_send_forces", "busy_send_handovers"} {
			var n int
			if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil || n != 0 {
				t.Errorf("restored %s rows = %d, %v; want none", table, n, err)
			}
		}
	})
	if !reflect.DeepEqual(upgraded, dbDump(t, f.path)) {
		t.Error("recovery changed the upgraded copy kept for reconciliation")
	}
}
