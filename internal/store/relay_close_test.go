package store

// relay_close_test.go — find-missing's mark of a row with relay requests
// (b.146 rule 12, closed_at): it closes every request that still awaits an
// answer, denying an undecided one and keeping a recorded verdict, and once
// the row is resumed a closed request holds nothing: the move to working,
// the idle-prompt Notification, the check_permission repair and the cap
// eviction all go ahead. Seeds: relay_writes_test.go's relay row.

import (
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"
)

// Tokens of the requests a mark test records beside tokenA to tokenC.
const (
	tokenD = "dddddddd-dddd-4ddd-addd-dddddddddddd"
	tokenE = "eeeeeeee-eeee-4eee-aeee-eeeeeeeeeeee"
)

// mustRequest reads relayID's request token.
func mustRequest(t *testing.T, s *Store, token string) PermissionRow {
	t.Helper()
	pr, err := s.GetPermissionRequest(relayID, token)
	if err != nil {
		t.Fatalf("GetPermissionRequest(%s): %v", token, err)
	}
	return pr
}

// TestMarkMissingClosesRelayRequests (b.146 rule 12; SR-A-2.5): the mark
// closes, with one closed_at, every request of its row that still awaits an
// answer: an undecided one (from v7 on or before) is denied with reason
// find_missing, a verdict its relay hook has not acked keeps its decision,
// reason and decided_at. An acked request and a decided one from before v7
// are left as they were. After the commit, in request-id order, each denied
// request gets one ad.row_mutation.committed (writer find_missing) and every
// closed one a permission_orphan_closeout tick.
func TestMarkMissingClosesRelayRequests(t *testing.T) {
	s, _ := newRelayRow(t, "on", StateWorking)
	insertRelay(t, s, relayReq(tokenA)) // undecided
	insertRelay(t, s, relayReq(tokenB)) // decided, not acked
	if ok, err := s.DecideRelayRequest(relayID, tokenB, "deny", DecisionReasonOperator, WriterProcessDecide, time.Time{}, DefaultLockWait); err != nil || !ok {
		t.Fatalf("decide B = %v, %v", ok, err)
	}
	insertRelay(t, s, relayReq(tokenC)) // acked
	if ok, err := s.DecideRelayRequest(relayID, tokenC, "allow", "", WriterProcessDecide, time.Time{}, DefaultLockWait); err != nil || !ok {
		t.Fatalf("decide C = %v, %v", ok, err)
	}
	if _, _, ok, err := s.AckRelayDecision(relayID, tokenC, time.Now(), DefaultLockWait, nil); err != nil || !ok {
		t.Fatalf("ack C = %v, %v", ok, err)
	}
	for _, tok := range []string{tokenD, tokenE} { // before v7: D decided, E undecided
		if err := agentPermissionRequest(s, relayID, tok, "Bash", `{}`, 0, ""); err != nil {
			t.Fatalf("insert %s: %v", tok, err)
		}
	}
	if ok, err := s.DecidePermissionRequest(relayID, tokenD, "allow", "", WriterProcessDecide); err != nil || !ok {
		t.Fatalf("decide D = %v, %v", ok, err)
	}
	before := map[string]PermissionRow{}
	for _, tok := range []string{tokenA, tokenB, tokenC, tokenD, tokenE} {
		before[tok] = mustRequest(t, s, tok)
	}
	mark := TrailMark(t)
	markedFrom := time.Now().Truncate(time.Millisecond)

	if prior, res, err := s.MarkMissingIfSameLife(relayID, mustGetSpawn(t, s, relayID).Snapshot); err != nil || res != CondApplied || prior != StateCheckPermission {
		t.Fatalf("MarkMissingIfSameLife = %q, %v, %v; want check_permission, CondApplied", prior, res, err)
	}

	markedTo := time.Now()
	closedAt := mustRequest(t, s, tokenA).ClosedAt
	if closedAt.Before(markedFrom) || closedAt.After(markedTo) {
		t.Errorf("closed_at = %v; want the mark's time, in [%v, %v]", closedAt, markedFrom, markedTo)
	}
	for tok, want := range map[string]struct {
		closed           bool
		decision, reason string
	}{
		tokenA: {true, "deny", DecisionReasonFindMissing},
		tokenB: {true, "deny", DecisionReasonOperator},
		tokenC: {false, "allow", ""},
		tokenD: {false, "allow", ""},
		tokenE: {true, "deny", DecisionReasonFindMissing},
	} {
		got, was := mustRequest(t, s, tok), before[tok]
		if got.Decision != want.decision || got.DecisionReason != want.reason || got.Closed() != want.closed ||
			want.closed && !got.ClosedAt.Equal(closedAt) {
			t.Errorf("%s = decision %q (%q), closed_at %v; want %q (%q), closed %v at the mark's one instant %v",
				tok, got.Decision, got.DecisionReason, got.ClosedAt, want.decision, want.reason, want.closed, closedAt)
		}
		if was.Decision != "" && !got.DecidedAt.Equal(was.DecidedAt) || !got.DeliveredAt.Equal(was.DeliveredAt) {
			t.Errorf("%s: decided_at %v -> %v, delivered_at %v -> %v; want a recorded verdict's kept, no ack written",
				tok, was.DecidedAt, got.DecidedAt, was.DeliveredAt, got.DeliveredAt)
		}
		if !want.closed && !reflect.DeepEqual(got, was) {
			t.Errorf("%s changed:\n before %+v\n after  %+v", tok, was, got)
		}
	}
	if got := openTokens(t, s, relayID); len(got) != 0 {
		t.Errorf("requests awaiting an answer = %v; want none", got)
	}

	var got []string // event/token, in trail order
	for _, row := range readStoreTrailLines(t)[mark:] {
		if row["claude_instance_id"] != relayID || row["request_token"] == nil {
			continue
		}
		got = append(got, fmt.Sprintf("%v/%v", row["event"], row["request_token"]))
		fields := map[string]string{"source": "ad_find_missing", "reconciliation_reason": "permission_orphan_closeout"}
		if row["event"] == "ad.row_mutation.committed" {
			fields = map[string]string{"source": "ad_store", "writer_process": WriterProcessFindMissing,
				"mutation_kind": "update", "decision": "deny", "decision_reason": DecisionReasonFindMissing}
		}
		for key, want := range fields {
			assertTrailStr(t, row, key, want)
		}
	}
	want := []string{
		"ad.row_mutation.committed/" + tokenA, "ad.find_missing.tick/" + tokenA,
		"ad.find_missing.tick/" + tokenB,
		"ad.row_mutation.committed/" + tokenE, "ad.find_missing.tick/" + tokenE,
	}
	if !slices.Equal(got, want) {
		t.Errorf("trail after the mark = %v; want %v", got, want)
	}
}

// resumeRelayRow is resume of relayID, which find-missing's mark finished: the
// move to pending for a new launch, its identity write recording the agent's
// pane, then the agent's SessionStart, to waiting.
func resumeRelayRow(t *testing.T, s *Store) {
	t.Helper()
	version := trailMove(t, s, mustGetSpawn(t, s, relayID))
	if err := recordPaneAt(s, relayID, version, trailResumeToken); err != nil {
		t.Fatalf("identity write: %v", err)
	}
	if err := agentSessionStart(s, relayID, "sess-resumed", "", false); err != nil {
		t.Fatalf("SessionStart: %v", err)
	}
	if got := mustGetSpawn(t, s, relayID).State; got != StateWaiting {
		t.Fatalf("state after resume = %q; want waiting", got)
	}
}

// TestClosedRequestHoldsNothingAfterResume (b.146 rules 9, 12, problem 3): a
// verdict its relay hook never acked, closed by the mark, no longer awaits an
// answer once the row is resumed: the row's move to working is not held, the
// idle-prompt Notification and find-missing's repair move a check_permission
// row on, and the cap eviction removes the request.
func TestClosedRequestHoldsNothingAfterResume(t *testing.T) {
	cases := []struct {
		name string
		do   func(t *testing.T, s *Store) // after the resume; checks its own result
	}{
		{"move to working", func(t *testing.T, s *Store) {
			if err := agentHook(s, relayID, StateWorking, false, "PreToolUse"); err != nil {
				t.Fatal(err)
			}
			if got := mustGetSpawn(t, s, relayID).State; got != StateWorking {
				t.Errorf("state = %q; want working, not held", got)
			}
		}},
		{"idle-prompt Notification", func(t *testing.T, s *Store) {
			if err := agentHook(s, relayID, StateCheckPermission, false, "PermissionRequest"); err != nil {
				t.Fatal(err)
			}
			if _, _, err := s.ApplyHookWaitingIfWorking(relayID, agentGate("Notification", ""), "Notification", "", false); err != nil {
				t.Fatalf("idle Notification: %v", err)
			}
			if sp := mustGetSpawn(t, s, relayID); sp.State != StateWaiting || sp.IdleSince == "" {
				t.Errorf("row = %q, idle_since %q; want waiting, idle_since set", sp.State, sp.IdleSince)
			}
		}},
		{"check_permission repair", func(t *testing.T, s *Store) {
			if err := agentHook(s, relayID, StateCheckPermission, false, "PermissionRequest"); err != nil {
				t.Fatal(err)
			}
			if state, res, err := s.RepairCheckPermissionIfSameLife(relayID, mustGetSpawn(t, s, relayID).Snapshot); err != nil ||
				res != CondApplied || state != StateWorking {
				t.Errorf("repair = %q, %v, %v; want working, applied", state, res, err)
			}
		}},
		{"cap eviction", func(t *testing.T, s *Store) {
			if _, applied, err := s.InsertRelayRequest(relayID, agentGate("PermissionRequest", ""), relayReq(tokenB), 1, DefaultLockWait); err != nil || !applied.Applied {
				t.Fatalf("insert B with cap 1 = %+v, %v", applied, err)
			}
			reqs, err := s.PermissionRequestsForSpawn(relayID)
			if err != nil || len(reqs) != 1 || reqs[0].RequestToken != tokenB {
				t.Errorf("requests after the eviction = %+v, %v; want B alone (A evicted)", reqs, err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newRelayRow(t, "on", StateWorking)
			insertRelay(t, s, relayReq(tokenA))
			decideA(t, s, "allow")
			markRelayRow(t, s)
			resumeRelayRow(t, s)
			if got := openTokens(t, s, relayID); len(got) != 0 {
				t.Fatalf("requests awaiting an answer after the resume = %v; want none", got)
			}

			tc.do(t, s)
		})
	}
}
