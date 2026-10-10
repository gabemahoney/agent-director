package store

// relay_close_test.go — the close of a row's relay requests (b.146 rule 12,
// closed_at; closeOrphanedRequests) by each write that runs it: find-missing's
// mark, the terminal SessionEnd's move to ended and, as a backstop for a row
// an earlier release ended, resume's move to pending. Each closes every
// request that still awaits an answer, denying an undecided one and keeping a
// recorded verdict, with its own write or not at all; once the row is resumed
// a closed request holds nothing: the move to working, the idle-prompt
// Notification, the check_permission repair and the cap eviction all go
// ahead. Seeds: relay_writes_test.go's relay row.

import (
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// Tokens of the requests a close test records beside tokenA to tokenC.
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

// endRelayRow is the terminal SessionEnd of relayID's own agent; it must apply.
func endRelayRow(t *testing.T, s *Store) {
	t.Helper()
	if err := agentHook(s, relayID, StateEnded, false, "SessionEnd"); err != nil {
		t.Fatalf("SessionEnd: %v", err)
	}
}

// endRelayRowUnclosed ends relayID as a release before the ended close did:
// state ended with its ended_at, its requests left as they were.
func endRelayRowUnclosed(t *testing.T, path string) {
	t.Helper()
	withRaw(t, path, func(db *sql.DB) {
		mustExec(t, db, `UPDATE spawns SET state = 'ended', ended_at = CURRENT_TIMESTAMP WHERE claude_instance_id = ?`, relayID)
	})
}

// moveRelayRow is resume's move of relayID to pending as it reads now; it must apply.
func moveRelayRow(t *testing.T, s *Store) {
	t.Helper()
	trailMove(t, s, mustGetSpawn(t, s, relayID))
}

// closer is one write that closes relayID's requests still awaiting an
// answer, with what it writes and emits.
type closer struct {
	name       string
	unclosed   bool // an earlier release ended the row first, closing nothing
	close      func(t *testing.T, s *Store)
	reason     string // the deny's decision_reason
	writer     string // the deny's writer_process
	tick       bool   // a permission_orphan_closeout tick follows each closed request
	transition bool   // the row's ad.spawn.state_transition to ended follows the denies
	state      string // the row's state after the close
}

// closers are the three writes that run the close.
var closers = []closer{
	{name: "find-missing's mark", close: markRelayRow, reason: DecisionReasonFindMissing, writer: WriterProcessFindMissing,
		tick: true, state: StateMissing},
	{name: "the terminal SessionEnd", close: endRelayRow, reason: DecisionReasonEnded, writer: WriterProcessHook,
		transition: true, state: StateEnded},
	{name: "resume's move of a row an earlier release ended", unclosed: true, close: moveRelayRow, reason: DecisionReasonEnded,
		writer: WriterProcessResume, state: StatePending},
}

// closeTokens are the requests seedCloseRequests records, in request-id order.
var closeTokens = []string{tokenA, tokenB, tokenC, tokenD, tokenE}

// decideB records decide's deny (reason operator) on relayID's request B, not acked.
func decideB(t *testing.T, s *Store) {
	t.Helper()
	if ok, err := s.DecideRelayRequest(relayID, tokenB, "deny", DecisionReasonOperator, WriterProcessDecide, time.Time{}, DefaultLockWait); err != nil || !ok {
		t.Fatalf("decide B = %v, %v", ok, err)
	}
}

// seedCloseRequests records on relayID (left in check_permission): A
// undecided, B decided and not acked, C acked, and two from before v7, D
// decided and E undecided.
func seedCloseRequests(t *testing.T, s *Store) {
	t.Helper()
	insertRelay(t, s, relayReq(tokenA))
	insertRelay(t, s, relayReq(tokenB))
	decideB(t, s)
	insertRelay(t, s, relayReq(tokenC))
	if ok, err := s.DecideRelayRequest(relayID, tokenC, "allow", "", WriterProcessDecide, time.Time{}, DefaultLockWait); err != nil || !ok {
		t.Fatalf("decide C = %v, %v", ok, err)
	}
	if _, _, ok, err := s.AckRelayDecision(relayID, tokenC, time.Now(), DefaultLockWait, nil); err != nil || !ok {
		t.Fatalf("ack C = %v, %v", ok, err)
	}
	for _, tok := range []string{tokenD, tokenE} {
		if err := agentPermissionRequest(s, relayID, tok, "Bash", `{}`, 0, ""); err != nil {
			t.Fatalf("insert %s: %v", tok, err)
		}
	}
	if ok, err := s.DecidePermissionRequest(relayID, tokenD, "allow", "", WriterProcessDecide); err != nil || !ok {
		t.Fatalf("decide D = %v, %v", ok, err)
	}
}

// TestCloseClosesRelayRequests (b.146 rule 12; SR-A-2.5; b.59i, b.6qr): each
// close closes, with one closed_at, every request of its row that still awaits
// an answer: an undecided one (from v7 on or before) is denied with the
// close's reason, a verdict its relay hook has not acked keeps its decision,
// reason and decided_at; an acked request and a decided one from before v7
// are left as they were, and the row's version advances by one. The same
// close proves every request of the row gone (b.146 step 2c), closed now or
// before, agent_gone at the close's instant, except C, whose tool ran before:
// it keeps that proof. Another row's open request is left as it was,
// unproven. After the
// commit, in request-id order, each denied request gets one
// ad.row_mutation.committed with the close's writer, the mark's ticks follow
// each closed request, and the SessionEnd's state transition comes last; a
// request only closed gets no row mutation.
func TestCloseClosesRelayRequests(t *testing.T) {
	for _, c := range closers {
		t.Run(c.name, func(t *testing.T) {
			s, path := newRelayRow(t, "on", StateWorking)
			seedCloseRequests(t, s)
			addOtherRelayRow(t, s)
			insertOther(t, s, mainReq(tokenA), 0)
			other := otherRequest(t, s, tokenA)
			if n, err := s.ProveRequestsGone(relayID, agentGate("PostToolUse", ""),
				RequestProof{How: ProvenGoneToolRan, ToolUseID: "toolu_" + tokenC[:8], At: proofAt}); err != nil || n != 1 {
				t.Fatalf("C's tool ran = %d, %v; want C proven", n, err)
			}
			if c.unclosed {
				endRelayRowUnclosed(t, path)
			}
			before := map[string]PermissionRow{}
			for _, tok := range closeTokens {
				before[tok] = mustRequest(t, s, tok)
			}
			version := mustGetSpawn(t, s, relayID).RowVersion
			mark := TrailMark(t)
			from := time.Now().Truncate(time.Millisecond)

			c.close(t, s)

			to := time.Now()
			if sp := mustGetSpawn(t, s, relayID); sp.State != c.state || sp.RowVersion != version+1 {
				t.Errorf("row = %q at version %d; want %q at %d", sp.State, sp.RowVersion, c.state, version+1)
			}
			closedAt := mustRequest(t, s, tokenA).ClosedAt
			if closedAt.Before(from) || closedAt.After(to) {
				t.Errorf("closed_at = %v; want the close's time, in [%v, %v]", closedAt, from, to)
			}
			for tok, want := range map[string]struct {
				closed           bool
				decision, reason string
			}{
				tokenA: {true, "deny", c.reason},
				tokenB: {true, "deny", DecisionReasonOperator},
				tokenC: {false, "allow", ""},
				tokenD: {false, "allow", ""},
				tokenE: {true, "deny", c.reason},
			} {
				got, was := mustRequest(t, s, tok), before[tok]
				if got.Decision != want.decision || got.DecisionReason != want.reason || got.Closed() != want.closed ||
					want.closed && !got.ClosedAt.Equal(closedAt) {
					t.Errorf("%s = decision %q (%q), closed_at %v; want %q (%q), closed %v at the close's one instant %v",
						tok, got.Decision, got.DecisionReason, got.ClosedAt, want.decision, want.reason, want.closed, closedAt)
				}
				if was.Decision != "" && !got.DecidedAt.Equal(was.DecidedAt) || !got.DeliveredAt.Equal(was.DeliveredAt) {
					t.Errorf("%s: decided_at %v -> %v, delivered_at %v -> %v; want a recorded verdict's kept, no ack written",
						tok, was.DecidedAt, got.DecidedAt, was.DeliveredAt, got.DeliveredAt)
				}
				wantHow, wantAt := ProvenGoneAgentGone, closedAt
				if tok == tokenC {
					wantHow, wantAt = ProvenGoneToolRan, proofAt
				}
				if got.ProvenGoneHow != wantHow || !got.ProvenGoneAt.Equal(wantAt) {
					t.Errorf("%s = proven_gone_how %q at %v; want %q at %v", tok, got.ProvenGoneHow, got.ProvenGoneAt, wantHow, wantAt)
				}
				unproven := got
				unproven.ProvenGoneAt, unproven.ProvenGoneHow = was.ProvenGoneAt, was.ProvenGoneHow
				if !want.closed && !reflect.DeepEqual(unproven, was) {
					t.Errorf("%s changed beyond its proof:\n before %+v\n after  %+v", tok, was, got)
				}
			}
			if got := openTokens(t, s, relayID); len(got) != 0 {
				t.Errorf("requests awaiting an answer = %v; want none", got)
			}
			if got := otherRequest(t, s, tokenA); !reflect.DeepEqual(got, other) || got.ProvenGone() {
				t.Errorf("%s's request changed by the close:\n before %+v\n after  %+v", otherRelayID, other, got)
			}

			var want []string // event/token, in trail order
			for _, tok := range []string{tokenA, tokenB, tokenE} {
				if tok != tokenB {
					want = append(want, "ad.row_mutation.committed/"+tok)
				}
				if c.tick {
					want = append(want, "ad.find_missing.tick/"+tok)
				}
			}
			if c.transition {
				want = append(want, "ad.spawn.state_transition")
			}
			if got := closeTrail(t, mark, c); !slices.Equal(got, want) {
				t.Errorf("trail after the close = %v; want %v", got, want)
			}
		})
	}
}

// closeTrail is every trail line naming relayID since mark, as event/token
// (the state transition as its event alone), each checked against c's fields.
func closeTrail(t *testing.T, mark int, c closer) []string {
	t.Helper()
	var got []string
	for _, row := range readStoreTrailLines(t)[mark:] {
		if row["claude_instance_id"] != relayID {
			continue
		}
		var fields map[string]string
		switch row["event"] {
		case "ad.spawn.state_transition":
			assertSpawnStateTransitionFields(t, row, relayID, StateCheckPermission, StateEnded, "SessionEnd", false)
			got = append(got, "ad.spawn.state_transition")
			continue
		case "ad.row_mutation.committed":
			fields = map[string]string{"source": "ad_store", "writer_process": c.writer, "mutation_kind": "update",
				"decision": "deny", "decision_reason": c.reason, "tool_name": "Bash"}
		case "ad.find_missing.tick":
			fields = map[string]string{"source": "ad_find_missing", "reconciliation_reason": "permission_orphan_closeout"}
		}
		for key, want := range fields {
			assertTrailStr(t, row, key, want)
		}
		got = append(got, fmt.Sprintf("%v/%v", row["event"], row["request_token"]))
	}
	return got
}

// errTestClose is the failure failClose's trigger raises.
const errTestClose = "test: the close fails"

// failClose makes every update of a permission request on the store at path
// fail, as the close's first statement does after its write's own statement.
func failClose(t *testing.T, path string) {
	t.Helper()
	withRaw(t, path, func(db *sql.DB) {
		mustExec(t, db, `CREATE TRIGGER ad_test_fail_close BEFORE UPDATE ON permission_requests
			BEGIN SELECT RAISE(ABORT, '`+errTestClose+`'); END`)
	})
}

// lockStore takes the write lock of the store at path until the test ends.
func lockStore(t *testing.T, path string) {
	t.Helper()
	holdStoreLock(t, path)
}

// TestCloseWritesNothingUnlessItsWriteApplies (b.146 rule 12): the terminal
// SessionEnd and resume's move close the row's requests in their own write's
// transaction, both or neither: a gate that does not hold, a live or stale or
// absent row, a close that fails, or a write lock held past the busy timeout
// leaves the row and every request as they were and emits nothing. A held
// lock is a store error, never ErrStoreBusy (neither verb names it).
func TestCloseWritesNothingUnlessItsWriteApplies(t *testing.T) {
	type result struct {
		applied bool
		reason  string // the hook's not-applied reason
		cond    CondResult
		err     error
	}
	end := func(id string, gate HookGate) func(s *Store) result {
		return func(s *Store) result {
			_, applied, err := s.ApplyHookTransitionResult(id, gate, StateEnded, false, "SessionEnd", "", false)
			return result{applied: applied.Applied, reason: applied.Reason, err: err}
		}
	}
	move := func(id string, stale bool) func(s *Store) result {
		return func(s *Store) result {
			sp, err := s.GetSpawn(relayID)
			if err != nil {
				return result{err: err}
			}
			if stale {
				sp.Snapshot.RowVersion--
			}
			res, _, err := s.MoveToPending(id, sp.Snapshot, trailMoveStart, trailResumeToken, "/tmp/trail-sock", "", LaunchOwner{})
			return result{applied: res == CondApplied, cond: res, err: err}
		}
	}
	cases := []struct {
		name    string
		ended   bool                            // an earlier release ended the row first (the move's cases)
		arrange func(t *testing.T, path string) // before the write
		busyMs  int                             // the store's busy timeout; 0 the default
		write   func(s *Store) result
		want    result // err: nil none, errAnyStore any error
		wantMsg string // carried by the error
	}{
		{name: "SessionEnd/gate does not hold", write: end(relayID, foreignGate("SessionEnd", "")),
			want: result{reason: HookReasonPIDMismatch}},
		{name: "SessionEnd/absent row", write: end("relay-absent", agentGate("SessionEnd", ""))},
		{name: "SessionEnd/the close fails", arrange: failClose, write: end(relayID, agentGate("SessionEnd", "")),
			want: result{err: errAnyStore}, wantMsg: errTestClose},
		{name: "SessionEnd/write lock held past the busy timeout", arrange: lockStore, busyMs: 100,
			write: end(relayID, agentGate("SessionEnd", "")), want: result{err: errAnyStore}},
		{name: "resume's move/live row", write: move(relayID, false), want: result{cond: CondChanged}},
		{name: "resume's move/stale snapshot", ended: true, write: move(relayID, true), want: result{cond: CondChanged}},
		{name: "resume's move/absent row", ended: true, write: move("relay-absent", false), want: result{cond: CondAbsent}},
		{name: "resume's move/the close fails", ended: true, arrange: failClose, write: move(relayID, false),
			want: result{err: errAnyStore}, wantMsg: errTestClose},
		{name: "resume's move/write lock held past the busy timeout", ended: true, arrange: lockStore, busyMs: 100,
			write: move(relayID, false), want: result{err: errAnyStore}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, path := newRelayRow(t, "on", StateWorking)
			insertRelay(t, s, relayReq(tokenA))
			insertRelay(t, s, relayReq(tokenB))
			decideB(t, s)
			if tc.ended {
				endRelayRowUnclosed(t, path)
			}
			if tc.busyMs > 0 {
				short, err := OpenWithBusyTimeout(path, tc.busyMs)
				if err != nil {
					t.Fatalf("OpenWithBusyTimeout: %v", err)
				}
				t.Cleanup(func() { _ = short.Close() })
				s = short
			}
			rowBefore, reqsBefore := mustGetSpawn(t, s, relayID), requestDump(t, path)
			if tc.arrange != nil {
				tc.arrange(t, path)
			}
			mark := TrailMark(t)

			got := tc.write(s)

			if (tc.want.err == errAnyStore) != (got.err != nil) || errors.Is(got.err, ErrStoreBusy) {
				t.Fatalf("err = %v; want an error %v, never ErrStoreBusy", got.err, tc.want.err == errAnyStore)
			}
			if got.applied || got.reason != tc.want.reason || got.cond != tc.want.cond {
				t.Errorf("result = %+v; want not applied, reason %q, %v", got, tc.want.reason, tc.want.cond)
			}
			if tc.wantMsg != "" && !strings.Contains(got.err.Error(), tc.wantMsg) {
				t.Errorf("err = %v; want it to carry %q", got.err, tc.wantMsg)
			}
			if after := mustGetSpawn(t, s, relayID); !reflect.DeepEqual(after, rowBefore) {
				t.Errorf("row changed:\n before %+v\n after  %+v", rowBefore, after)
			}
			if !slices.EqualFunc(requestDump(t, path), reqsBefore, mapsEqual) {
				t.Errorf("requests changed by a write that did not apply")
			}
			for _, row := range readStoreTrailLines(t)[mark:] {
				if row["claude_instance_id"] == relayID {
					t.Errorf("a write that did not apply emitted %v; want no trail line", row["event"])
				}
			}
		})
	}
}

// resumeRelayRow is resume of relayID, which is finished: the move to pending
// for a new launch, its identity write recording the agent's pane, then the
// agent's SessionStart, to waiting.
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

// TestClosedRequestHoldsNothingAfterResume (b.146 rules 9, 12, problem 3;
// b.59i, b.6qr): request A, undecided or a verdict its relay hook never acked,
// on a row find-missing marked missing, its agent's SessionEnd ended, or an
// earlier release ended without closing it, no longer awaits an answer once
// the row is resumed: the row's move to working is not held, the idle-prompt
// Notification and find-missing's repair move a check_permission row on, and
// the cap eviction removes the request.
func TestClosedRequestHoldsNothingAfterResume(t *testing.T) {
	actions := []struct {
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
	for _, c := range closers {
		for _, decided := range []bool{false, true} {
			for _, a := range actions {
				t.Run(fmt.Sprintf("%s/decided %v/%s", c.name, decided, a.name), func(t *testing.T) {
					s, path := newRelayRow(t, "on", StateWorking)
					insertRelay(t, s, relayReq(tokenA))
					if decided {
						decideA(t, s, "allow")
					}
					if c.unclosed { // the resume's own move is the close
						endRelayRowUnclosed(t, path)
					} else {
						c.close(t, s)
					}
					resumeRelayRow(t, s)
					if got := openTokens(t, s, relayID); len(got) != 0 {
						t.Fatalf("requests awaiting an answer after the resume = %v; want none", got)
					}

					a.do(t, s)
				})
			}
		}
	}
}
