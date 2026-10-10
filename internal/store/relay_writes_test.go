package store

// The relay's store writes and reads of b.146 step 2 (relay_writes.go,
// lockwait.go): the relay hook's first write as one transaction (rule 1), its
// ack and timeout deny and their precondition (rule 3, problem 7), decide's
// guarded verdict, the readers' and decide's facts, every bounded wait for the
// write lock or the store's connection (problem 1, problem 4, decision 9 B),
// the "awaits an answer" rule (rule 9) and the remembered idle-prompt
// Notification (problem 3). find-missing's check_permission repair is
// find_missing_writes_test.go's and row_version_find_missing_test.go's; the
// close of relay requests (find-missing's mark, the terminal SessionEnd,
// resume's move) is relay_close_test.go's.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"
)

// relayID is the row every test here records requests on.
const relayID = "relay-row"

// testHook is the relay hook identity relayReq records.
var testHook = ProcessIdentity{PID: 4321, Starttime: "777", PIDNamespace: "pid:[4026531836]"}

// relayReq is a request for token as the relay hook records it: its own
// identity, the payload's tool_use_id and a settle instant an hour out.
func relayReq(token string) RelayRequest {
	return RelayRequest{RequestToken: token, ToolName: "Bash", ToolInput: `{"cmd":"ls"}`, ToolUseID: "toolu_" + token[:8],
		AgentID: "agent-" + token[:4], Hook: testHook, SettledAt: time.UnixMilli(time.Now().Add(time.Hour).UnixMilli())}
}

// newRelayRow opens a store holding relayID with the relay on, its agent's
// pane recorded, in state (pending when ""), and returns it with its path.
func newRelayRow(t *testing.T, relayMode, state string) (*Store, string) {
	t.Helper()
	s, path := openTempStore(t)
	seedSpawnForPerm(t, s, relayID, relayMode)
	if state != "" {
		if err := agentHook(s, relayID, state, false, "test_seed"); err != nil {
			t.Fatalf("to %s: %v", state, err)
		}
	}
	return s, path
}

// insertRelay records req on relayID from its own agent; a write that does
// not apply fails the test.
func insertRelay(t *testing.T, s *Store, req RelayRequest) {
	t.Helper()
	if _, applied, err := s.InsertRelayRequest(relayID, agentGate("PermissionRequest", ""), req, 0, DefaultLockWait); err != nil || !applied.Applied {
		t.Fatalf("InsertRelayRequest(%s) = %+v, %v; want applied", req.RequestToken, applied, err)
	}
}

// holdStoreLock takes the write lock of the store at path on a connection of
// its own (BEGIN IMMEDIATE), as another process's write holds it, until the
// returned release is called (the test's cleanup calls it too).
func holdStoreLock(t *testing.T, path string) (release func()) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open holder: %v", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("BEGIN IMMEDIATE"); err != nil {
		_ = db.Close()
		t.Fatalf("take the write lock: %v", err)
	}
	done := false
	release = func() {
		if !done {
			done = true
			_, _ = db.Exec("ROLLBACK")
			_ = db.Close()
		}
	}
	t.Cleanup(release)
	return release
}

// requestDump is every column of every request of relayID, quote()d.
func requestDump(t *testing.T, path string) []map[string]string {
	t.Helper()
	var cols []string
	withRaw(t, path, func(db *sql.DB) { cols = tableColumnNames(t, db, "permission_requests") })
	return readQuotedRows(t, path, "permission_requests", relayID, "request_id", cols)
}

// setPaneAnswer writes pane_answer on relayID's request token, as a step-2b
// pane answer would.
func setPaneAnswer(t *testing.T, path, token, value string) {
	t.Helper()
	withRaw(t, path, func(db *sql.DB) {
		mustExec(t, db, `UPDATE permission_requests SET pane_answer = ? WHERE claude_instance_id = ? AND request_token = ?`, value, relayID, token)
	})
}

// TestInsertRelayRequestOneTransaction (b.146 rule 1, problem 1): the move to
// check_permission and the request insert are one transaction. Applied, the
// row reads check_permission with idle_since cleared and the request carries
// the hook's identity, tool_use_id, agent_id and settle instant, with one
// state transition to check_permission on the trail; a failed insert, a gate
// that does not hold, or a write lock held past the wait leaves neither and
// emits nothing, the last with ErrStoreBusy within the wait.
func TestInsertRelayRequestOneTransaction(t *testing.T) {
	failInsert := func(t *testing.T, path string) {
		withRaw(t, path, func(db *sql.DB) {
			mustExec(t, db, `CREATE TRIGGER ad_test_fail_insert BEFORE INSERT ON permission_requests
				BEGIN SELECT RAISE(ABORT, 'test: the hook dies at its insert'); END`)
		})
	}
	cases := []struct {
		name       string
		arrange    func(t *testing.T, path string)
		gate       HookGate
		maxWait    time.Duration
		wantErr    error // nil: none; errAnyStore: any error
		wantReason string
	}{
		{name: "applied", gate: agentGate("PermissionRequest", ""), maxWait: DefaultLockWait},
		{name: "insert fails after the state write", arrange: failInsert, gate: agentGate("PermissionRequest", ""),
			maxWait: DefaultLockWait, wantErr: errAnyStore},
		{name: "gate does not hold", gate: foreignGate("PermissionRequest", ""), maxWait: DefaultLockWait, wantReason: HookReasonPIDMismatch},
		{name: "write lock held past the wait", arrange: func(t *testing.T, path string) { holdStoreLock(t, path) },
			gate: agentGate("PermissionRequest", ""), maxWait: 200 * time.Millisecond, wantErr: ErrStoreBusy},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, path := newRelayRow(t, "on", StateWorking)
			withRaw(t, path, func(db *sql.DB) { mustExec(t, db, `UPDATE spawns SET idle_since = '2026-10-01 00:00:00'`) })
			before := mustGetSpawn(t, s, relayID)
			if tc.arrange != nil {
				tc.arrange(t, path)
			}
			req := relayReq(tokenA)
			mark := TrailMark(t)

			start := time.Now()
			outcome, applied, err := s.InsertRelayRequest(relayID, tc.gate, req, 0, tc.maxWait)
			elapsed := time.Since(start)

			switch {
			case tc.wantErr == errAnyStore && err == nil, tc.wantErr != errAnyStore && !errors.Is(err, tc.wantErr):
				t.Fatalf("InsertRelayRequest err = %v; want %v", err, tc.wantErr)
			case tc.wantErr == ErrStoreBusy && elapsed > 2*time.Second:
				t.Errorf("ErrStoreBusy after %v; want within the 200ms wait, not the store's busy timeout", elapsed)
			}
			if applied.Reason != tc.wantReason {
				t.Errorf("reason = %q; want %q", applied.Reason, tc.wantReason)
			}
			after := mustGetSpawn(t, s, relayID)
			reqs, rerr := s.PermissionRequestsForSpawn(relayID)
			if rerr != nil {
				t.Fatalf("PermissionRequestsForSpawn: %v", rerr)
			}
			if tc.name != "applied" {
				if after.State != StateWorking || after.RowVersion != before.RowVersion || after.IdleSince != before.IdleSince || len(reqs) != 0 {
					t.Errorf("after a write that did not commit: state %q version %d idle %q, %d requests; want working, %d, %q, none",
						after.State, after.RowVersion, after.IdleSince, len(reqs), before.RowVersion, before.IdleSince)
				}
				for _, row := range readStoreTrailLines(t)[mark:] {
					if row["claude_instance_id"] == relayID {
						t.Errorf("a write that did not commit emitted %v; want no trail line", row["event"])
					}
				}
				return
			}
			if outcome != UpsertInserted || !applied.Applied {
				t.Errorf("outcome = %v, %+v; want inserted, applied", outcome, applied)
			}
			if lines := TrailEventsSince(t, mark, "ad.spawn.state_transition", relayID); len(lines) != 1 {
				t.Errorf("state_transition lines = %d; want 1", len(lines))
			} else {
				assertSpawnStateTransitionFields(t, lines[0], relayID, StateWorking, StateCheckPermission, "PermissionRequest", false)
			}
			if after.State != StateCheckPermission || after.RowVersion != before.RowVersion+1 || after.IdleSince != "" {
				t.Errorf("row = state %q version %d idle %q; want check_permission, %d, cleared", after.State, after.RowVersion, after.IdleSince, before.RowVersion+1)
			}
			if len(reqs) != 1 {
				t.Fatalf("requests = %d; want 1", len(reqs))
			}
			got := reqs[0]
			if got.RequestToken != tokenA || got.Hook != req.Hook || got.ToolUseID != req.ToolUseID || got.AgentID != req.AgentID ||
				!got.SettledAt.Equal(req.SettledAt) || got.Decision != "" || !got.DeliveredAt.IsZero() || got.PaneAnswer != PaneAnswerNone || got.PreV7() {
				t.Errorf("request = %+v; want the hook's identity, tool_use_id, agent_id and settle instant, undecided, unacked", got)
			}
		})
	}
}

// errAnyStore stands for "any error" in a case table.
var errAnyStore = errors.New("any store error")

// TestRelayAckAndTimeoutDeny (b.146 rules 3, 12): the ack commits delivered_at
// only on a decided, unacked request with no pane answer and returns that
// verdict, a request find-missing's mark closed included (its verdict, or the
// mark's deny); the timeout deny records deny, timeout and its own ack
// together, only on an undecided request with no pane answer. A write that
// matches nothing changes nothing.
func TestRelayAckAndTimeoutDeny(t *testing.T) {
	cases := []struct {
		name          string
		verdict       string // decide's verdict recorded first; "" none
		pane          string // pane_answer written first; "" leaves none
		mark          bool   // find-missing's mark closes the request next
		call          string // "ack" or "deny"
		wantDone      bool
		wantDecision  string
		wantReason    string
		wantDelivered bool
	}{
		{"ack, undecided: nothing to ack", "", "", false, "ack", false, "", "", false},
		{"ack an allow", "allow", "", false, "ack", true, "allow", "", true},
		{"ack a deny with its reason", "deny", "", false, "ack", true, "deny", DecisionReasonOperator, true},
		{"ack, a pane answer begun", "allow", PaneAnswerIntent, false, "ack", false, "allow", "", false},
		{"ack a verdict the mark closed", "allow", "", true, "ack", true, "allow", "", true},
		{"ack the mark's deny", "", "", true, "ack", true, "deny", DecisionReasonFindMissing, true},
		{"timeout deny, undecided", "", "", false, "deny", true, "deny", DecisionReasonTimeout, true},
		{"timeout deny, a verdict landed first", "allow", "", false, "deny", false, "allow", "", false},
		{"timeout deny, a pane answer begun", "", PaneAnswerIntent, false, "deny", false, "", "", false},
		{"timeout deny, the mark's deny first", "", "", true, "deny", false, "deny", DecisionReasonFindMissing, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, path := newRelayRow(t, "on", StateWorking)
			insertRelay(t, s, relayReq(tokenA))
			if tc.verdict != "" {
				reason := ""
				if tc.verdict == "deny" {
					reason = DecisionReasonOperator
				}
				if ok, err := s.DecideRelayRequest(relayID, tokenA, tc.verdict, reason, WriterProcessDecide, time.Time{}, DefaultLockWait); err != nil || !ok {
					t.Fatalf("decide = %v, %v", ok, err)
				}
			}
			if tc.pane != "" {
				setPaneAnswer(t, path, tokenA, tc.pane)
			}
			if tc.mark {
				markRelayRow(t, s)
			}
			at := time.UnixMilli(1790000000123)

			var done bool
			var err error
			switch tc.call {
			case "ack":
				var decision, reason string
				decision, reason, done, err = s.AckRelayDecision(relayID, tokenA, at, DefaultLockWait, nil)
				if done && (decision != tc.wantDecision || reason != tc.wantReason) {
					t.Errorf("acked (%q, %q); want (%q, %q)", decision, reason, tc.wantDecision, tc.wantReason)
				}
			case "deny":
				done, err = s.DenyRelayTimeout(relayID, tokenA, at, DefaultLockWait, nil)
			}

			if err != nil || done != tc.wantDone {
				t.Fatalf("%s = %v, %v; want %v, nil", tc.call, done, err, tc.wantDone)
			}
			pr, err := s.GetPermissionRequest(relayID, tokenA)
			if err != nil {
				t.Fatalf("GetPermissionRequest: %v", err)
			}
			if pr.Decision != tc.wantDecision || pr.DecisionReason != tc.wantReason {
				t.Errorf("request decision = (%q, %q); want (%q, %q)", pr.Decision, pr.DecisionReason, tc.wantDecision, tc.wantReason)
			}
			if delivered := !pr.DeliveredAt.IsZero(); delivered != tc.wantDelivered || delivered && !pr.DeliveredAt.Equal(at) {
				t.Errorf("delivered_at = %v; want set to %v: %v", pr.DeliveredAt, at, tc.wantDelivered)
			}
			if tc.wantDone {
				if _, _, again, err := s.AckRelayDecision(relayID, tokenA, at.Add(time.Second), DefaultLockWait, nil); err != nil || again {
					t.Errorf("a second ack = %v, %v; want nothing to ack", again, err)
				}
			}
		})
	}
}

// TestDecideRelayRequestGuards (b.146 rules 6, 16): decide's one guarded
// statement records a verdict only on an undecided, unacked request with no
// pane answer; a request recorded from v7 on is judged by its hook, never by
// the legacy created_at cutoff.
func TestDecideRelayRequestGuards(t *testing.T) {
	cases := []struct {
		name    string
		arrange func(t *testing.T, s *Store, path string)
		cutoff  time.Time // the legacy created_at cutoff passed
		want    bool
	}{
		{"undecided", nil, time.Time{}, true},
		{"undecided, the legacy cutoff after created_at", nil, time.Now().Add(time.Hour), true},
		{"decided", func(t *testing.T, s *Store, _ string) {
			if ok, err := s.DecideRelayRequest(relayID, tokenA, "deny", DecisionReasonOperator, WriterProcessDecide, time.Time{}, DefaultLockWait); err != nil || !ok {
				t.Fatalf("first decide = %v, %v", ok, err)
			}
		}, time.Time{}, false},
		{"its hook's timeout deny delivered", func(t *testing.T, s *Store, _ string) {
			if ok, err := s.DenyRelayTimeout(relayID, tokenA, time.Now(), DefaultLockWait, nil); err != nil || !ok {
				t.Fatalf("timeout deny = %v, %v", ok, err)
			}
		}, time.Time{}, false},
		{"a pane answer begun", func(t *testing.T, _ *Store, path string) { setPaneAnswer(t, path, tokenA, PaneAnswerIntent) }, time.Time{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, path := newRelayRow(t, "on", StateWorking)
			insertRelay(t, s, relayReq(tokenA))
			if tc.arrange != nil {
				tc.arrange(t, s, path)
			}
			before := requestDump(t, path)

			ok, err := s.DecideRelayRequest(relayID, tokenA, "allow", "", WriterProcessDecide, tc.cutoff, DefaultLockWait)

			if err != nil || ok != tc.want {
				t.Fatalf("DecideRelayRequest = %v, %v; want %v, nil", ok, err, tc.want)
			}
			if !tc.want && !slices.EqualFunc(requestDump(t, path), before, mapsEqual) {
				t.Errorf("a refused verdict changed the request")
			}
		})
	}
}

// mapsEqual compares two quote()d rows.
func mapsEqual(a, b map[string]string) bool { return fmt.Sprint(a) == fmt.Sprint(b) }

// boundedCall is one store call whose waits are bounded, run on relayID with
// request A recorded (and decided first when decided).
type boundedCall struct {
	name    string
	read    bool // a read: a write lock another connection holds does not stop it (WAL)
	decided bool
	maxWait time.Duration
	call    func(s *Store, maxWait time.Duration) error
}

// boundedCalls are the store calls with a bounded wait (b.146 problems 1 and
// 4, decision 9 B): decide's reads under max_wait_ms, the relay hook's and
// decide's writes, and a reader's hook_gone_at write, which waits not at all.
var boundedCalls = []boundedCall{
	{"decide's read of the row", true, false, 150 * time.Millisecond, func(s *Store, w time.Duration) error {
		_, err := s.GetSpawnWithin(relayID, w)
		return err
	}},
	{"decide's read of the request", true, false, 150 * time.Millisecond, func(s *Store, w time.Duration) error {
		_, err := s.GetPermissionRequestWithin(relayID, tokenA, w)
		return err
	}},
	{"decide's read of the row's requests", true, false, 150 * time.Millisecond, func(s *Store, w time.Duration) error {
		_, err := s.PermissionRequestsForSpawnWithin(relayID, w)
		return err
	}},
	{"relay hook's first write", false, false, 150 * time.Millisecond, func(s *Store, w time.Duration) error {
		_, _, err := s.InsertRelayRequest(relayID, agentGate("PermissionRequest", ""), relayReq(tokenB), 0, w)
		return err
	}},
	{"relay hook's ack", false, true, 150 * time.Millisecond, func(s *Store, w time.Duration) error {
		_, _, _, err := s.AckRelayDecision(relayID, tokenA, time.Now(), w, nil)
		return err
	}},
	{"relay hook's timeout deny", false, false, 150 * time.Millisecond, func(s *Store, w time.Duration) error {
		_, err := s.DenyRelayTimeout(relayID, tokenA, time.Now(), w, nil)
		return err
	}},
	{"decide's verdict", false, false, 150 * time.Millisecond, func(s *Store, w time.Duration) error {
		_, err := s.DecideRelayRequest(relayID, tokenA, "allow", "", WriterProcessDecide, time.Time{}, w)
		return err
	}},
	{"decide's refused verdict", false, false, 150 * time.Millisecond, func(s *Store, w time.Duration) error {
		return s.RecordRefusedDecision(relayID, tokenA, "allow", time.Now(), time.Now(), w)
	}},
	{"a reader's hook_gone_at, no wait", false, false, 0, func(s *Store, w time.Duration) error {
		_, err := s.RecordHookGone(time.Now(), w, 1)
		return err
	}},
}

// holdConnection makes another goroutine take s's one connection, as another
// call of this process does, until release; it gives it back after 5 s
// regardless, so a call that waits for it shows as slow instead of hanging.
func holdConnection(t *testing.T, s *Store) (release func()) {
	t.Helper()
	held, free, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		conn, err := s.db.Conn(context.Background())
		close(held)
		if err != nil {
			t.Errorf("take the store's connection: %v", err)
			return
		}
		select {
		case <-free:
		case <-time.After(5 * time.Second):
		}
		_ = conn.Close()
	}()
	<-held
	var once sync.Once
	return func() { once.Do(func() { close(free); <-done }) }
}

// TestBoundedCallsUnderAHeldLockOrConnection (b.146 problems 1 and 4,
// decision 9 B): with another process holding the write lock, each bounded
// write gives up within its own wait, far below the store's busy timeout;
// with another call of this process holding the store's one connection, each
// bounded read and write does too, a reader's zero-wait hook_gone_at write at
// once. Each gives up with ErrStoreBusy and nothing written, and the
// connection waits the store's busy timeout again; once freed the same call
// goes ahead.
func TestBoundedCallsUnderAHeldLockOrConnection(t *testing.T) {
	holds := []struct {
		name  string
		reads bool // reads are stopped too
		hold  func(t *testing.T, s *Store, path string) (release func())
	}{
		{"write lock held by another process", false, func(t *testing.T, _ *Store, path string) func() { return holdStoreLock(t, path) }},
		{"connection held by another call", true, func(t *testing.T, s *Store, _ string) func() { return holdConnection(t, s) }},
	}
	for _, h := range holds {
		for _, c := range boundedCalls {
			if c.read && !h.reads {
				continue
			}
			t.Run(h.name+"/"+c.name, func(t *testing.T) {
				s, path := newRelayRow(t, "on", StateWorking)
				insertRelay(t, s, relayReq(tokenA))
				if c.decided {
					decideA(t, s, "allow")
				}
				rowBefore, reqsBefore := mustGetSpawn(t, s, relayID), requestDump(t, path)
				release := h.hold(t, s, path)

				start := time.Now()
				err := c.call(s, c.maxWait)
				elapsed := time.Since(start)
				release()

				if !errors.Is(err, ErrStoreBusy) {
					t.Fatalf("err = %v; want ErrStoreBusy", err)
				}
				if elapsed > c.maxWait+time.Second {
					t.Errorf("gave up after %v; want within its %v wait (the store waits %d ms)", elapsed, c.maxWait, DefaultBusyTimeoutMs)
				}
				if after := mustGetSpawn(t, s, relayID); after.RowVersion != rowBefore.RowVersion || after.State != rowBefore.State {
					t.Errorf("row changed: %+v; want %+v", after, rowBefore)
				}
				if !slices.EqualFunc(requestDump(t, path), reqsBefore, mapsEqual) {
					t.Errorf("requests changed by a call that gave up")
				}
				var busy int
				if err := s.db.QueryRow("PRAGMA busy_timeout").Scan(&busy); err != nil || busy != DefaultBusyTimeoutMs {
					t.Errorf("busy_timeout after the bounded call = %d, %v; want the store's %d back", busy, err, DefaultBusyTimeoutMs)
				}
				if err := c.call(s, c.maxWait); err != nil {
					t.Errorf("the same call once freed = %v; want it to go ahead", err)
				}
			})
		}
	}
}

// writeLockTaken reports whether another connection to the store at path
// finds the write lock taken: its BEGIN IMMEDIATE, waiting not at all, is
// busy.
func writeLockTaken(t *testing.T, path string) bool {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("BEGIN IMMEDIATE"); err != nil {
		if isBusy(err) {
			return true
		}
		t.Fatalf("BEGIN IMMEDIATE: %v", err)
	}
	_, _ = db.Exec("ROLLBACK")
	return false
}

// TestRelayWritePrecondition (b.146 problem 7): the ack's and the timeout
// deny's precondition runs inside their transaction once the write lock is
// taken, no other connection able to write while it runs: with the lock held
// past the write's wait it never runs; a failing one rolls the write back,
// writing nothing and freeing the lock, and is returned as it is; one that
// holds lets the write go ahead.
func TestRelayWritePrecondition(t *testing.T) {
	errParent := errors.New("test: the parent changed")
	writes := []struct {
		name    string
		decided bool
		write   func(s *Store, maxWait time.Duration, check func() error) (bool, error)
	}{
		{"ack", true, func(s *Store, w time.Duration, check func() error) (bool, error) {
			_, _, acked, err := s.AckRelayDecision(relayID, tokenA, time.Now(), w, check)
			return acked, err
		}},
		{"timeout deny", false, func(s *Store, w time.Duration, check func() error) (bool, error) {
			return s.DenyRelayTimeout(relayID, tokenA, time.Now(), w, check)
		}},
	}
	cases := []struct {
		name     string
		held     bool  // another process holds the write lock past the write's wait
		checkErr error // the precondition's answer
	}{
		{"lock held past the wait", true, nil},
		{"precondition fails", false, errParent},
		{"precondition holds", false, nil},
	}
	for _, w := range writes {
		for _, tc := range cases {
			t.Run(w.name+"/"+tc.name, func(t *testing.T) {
				s, path := newRelayRow(t, "on", StateWorking)
				insertRelay(t, s, relayReq(tokenA))
				if w.decided {
					decideA(t, s, "allow")
				}
				before := requestDump(t, path)
				var ran, lockTaken bool
				check := func() error {
					ran, lockTaken = true, writeLockTaken(t, path)
					return tc.checkErr
				}
				release := func() {}
				if tc.held {
					release = holdStoreLock(t, path)
				}

				done, err := w.write(s, 150*time.Millisecond, check)
				release()

				switch {
				case tc.held:
					if !errors.Is(err, ErrStoreBusy) || ran {
						t.Errorf("%s = %v, precondition run %v; want ErrStoreBusy, the precondition never run", w.name, err, ran)
					}
				case tc.checkErr != nil:
					if !errors.Is(err, tc.checkErr) || done || !ran || !lockTaken {
						t.Errorf("%s = %v, %v, precondition run %v with the lock taken %v; want its error, run inside the lock", w.name, done, err, ran, lockTaken)
					}
				default:
					if err != nil || !done || !ran || !lockTaken {
						t.Errorf("%s = %v, %v, precondition run %v with the lock taken %v; want written, the precondition run inside the lock", w.name, done, err, ran, lockTaken)
					}
					return
				}
				if !slices.EqualFunc(requestDump(t, path), before, mapsEqual) {
					t.Errorf("request changed by a %s that did not go ahead", w.name)
				}
				if writeLockTaken(t, path) {
					t.Errorf("the write lock is still taken after the %s", w.name)
				}
			})
		}
	}
}

// TestRecordHookGoneAndRefusedDecision (b.146 rules 8, 15): hook_gone_at keeps
// the first reader's instant and is returned for every request found; a
// refused decide's verdict replaces an earlier attempt and sets hook_gone_at
// only when it is unset.
func TestRecordHookGoneAndRefusedDecision(t *testing.T) {
	s, _ := newRelayRow(t, "on", StateWorking)
	insertRelay(t, s, relayReq(tokenA))
	insertRelay(t, s, relayReq(tokenB))
	a, err := s.GetPermissionRequest(relayID, tokenA)
	if err != nil {
		t.Fatalf("GetPermissionRequest: %v", err)
	}
	first, later := time.UnixMilli(1790000000000).UTC(), time.UnixMilli(1790000009000).UTC()

	got, err := s.RecordHookGone(first, DefaultLockWait, a.RequestID, 9999)
	if err != nil || len(got) != 1 || !got[a.RequestID].Equal(first) {
		t.Fatalf("RecordHookGone = %v, %v; want {%d: %v}, an absent request left out", got, err, a.RequestID, first)
	}
	if got, err := s.RecordHookGone(later, DefaultLockWait, a.RequestID); err != nil || !got[a.RequestID].Equal(first) {
		t.Errorf("second RecordHookGone = %v, %v; want the first instant %v kept", got, err, first)
	}

	for i, d := range []string{"allow", "deny"} {
		at := later.Add(time.Duration(i) * time.Second)
		if err := s.RecordRefusedDecision(relayID, tokenB, d, at, at, DefaultLockWait); err != nil {
			t.Fatalf("RecordRefusedDecision(%s): %v", d, err)
		}
	}
	b, err := s.GetPermissionRequest(relayID, tokenB)
	if err != nil {
		t.Fatalf("GetPermissionRequest: %v", err)
	}
	if b.AttemptedDecision != "deny" || !b.AttemptedAt.Equal(later.Add(time.Second)) || !b.HookGoneAt.Equal(later) || b.Decision != "" {
		t.Errorf("request B = attempted %q at %v, gone %v, decision %q; want the latest attempt (deny), the first gone instant, no decision",
			b.AttemptedDecision, b.AttemptedAt, b.HookGoneAt, b.Decision)
	}
}

// TestRecordRefusedDecisionOnlyWhileAwaiting (b.146 rules 9, 15): a refused
// decide's attempt is stored only on a request that still awaits an answer;
// one acked, answered at the pane, closed, or decided before v7 is left as it
// was, with no error.
func TestRecordRefusedDecisionOnlyWhileAwaiting(t *testing.T) {
	at := time.UnixMilli(time.Now().UnixMilli()).UTC()
	for _, tc := range awaitCases {
		t.Run(tc.name, func(t *testing.T) {
			s, path := newRelayRow(t, "on", StateWorking)
			recordAwaitCase(t, s, path, tc)
			before := requestDump(t, path)

			if err := s.RecordRefusedDecision(relayID, tokenA, "deny", at, at, DefaultLockWait); err != nil {
				t.Fatalf("RecordRefusedDecision: %v; want nil", err)
			}

			if pr := mustRequest(t, s, tokenA); tc.awaits && (pr.AttemptedDecision != "deny" || !pr.AttemptedAt.Equal(at)) {
				t.Errorf("request = attempted %q at %v; want deny at %v stored", pr.AttemptedDecision, pr.AttemptedAt, at)
			}
			if after := requestDump(t, path); !tc.awaits && !mapsEqual(after[0], before[0]) {
				t.Errorf("request changed:\n got  %v\n want %v", after[0], before[0])
			}
		})
	}
}

// awaitCase is one request's history and whether it still awaits an answer
// (b.146 rule 9).
type awaitCase struct {
	name   string
	legacy bool // recorded before schema v7 (UpsertOpenPermissionRequest)
	after  func(t *testing.T, s *Store, path string)
	awaits bool
}

// decideA records verdict on tokenA through decide's write.
func decideA(t *testing.T, s *Store, verdict string) {
	t.Helper()
	if ok, err := s.DecideRelayRequest(relayID, tokenA, verdict, "", WriterProcessDecide, time.Time{}, DefaultLockWait); err != nil || !ok {
		t.Fatalf("decide %s = %v, %v", verdict, ok, err)
	}
}

// awaitCases are the request histories of the rule-9 table.
var awaitCases = []awaitCase{
	{"undecided", false, nil, true},
	{"decided, not acked", false, func(t *testing.T, s *Store, _ string) { decideA(t, s, "allow") }, true},
	{"acked", false, func(t *testing.T, s *Store, _ string) {
		decideA(t, s, "allow")
		if _, _, ok, err := s.AckRelayDecision(relayID, tokenA, time.Now(), DefaultLockWait, nil); err != nil || !ok {
			t.Fatalf("ack = %v, %v", ok, err)
		}
	}, false},
	{"timeout deny delivered", false, func(t *testing.T, s *Store, _ string) {
		if ok, err := s.DenyRelayTimeout(relayID, tokenA, time.Now(), DefaultLockWait, nil); err != nil || !ok {
			t.Fatalf("timeout deny = %v, %v", ok, err)
		}
	}, false},
	{"pane answer begun", false, func(t *testing.T, _ *Store, path string) { setPaneAnswer(t, path, tokenA, PaneAnswerIntent) }, true},
	{"pane answer sent", false, func(t *testing.T, _ *Store, path string) { setPaneAnswer(t, path, tokenA, PaneAnswerSent) }, false},
	{"answered outside agent-director", false, func(t *testing.T, _ *Store, path string) { setPaneAnswer(t, path, tokenA, PaneAnswerOutside) }, false},
	{"its tool ran", false, func(t *testing.T, _ *Store, path string) { setPaneAnswer(t, path, tokenA, PaneAnswerToolRan) }, false},
	// b.146 step 2b: record-pane-answer closes a request recorded before v7 too.
	{"before v7, undecided, answered outside agent-director", true, func(t *testing.T, _ *Store, path string) {
		setPaneAnswer(t, path, tokenA, PaneAnswerOutside)
	}, false},
	{"before v7, undecided, pane answer begun", true, func(t *testing.T, _ *Store, path string) {
		setPaneAnswer(t, path, tokenA, PaneAnswerIntent)
	}, true},
	{"undecided, closed by find-missing's mark", false, func(t *testing.T, s *Store, _ string) { markRelayRow(t, s) }, false},
	{"before v7, undecided", true, nil, true},
	{"before v7, decided", true, func(t *testing.T, s *Store, _ string) {
		if ok, err := s.DecidePermissionRequest(relayID, tokenA, "allow", "", WriterProcessDecide); err != nil || !ok {
			t.Fatalf("decide = %v, %v", ok, err)
		}
	}, false},
	{"decided, not acked, closed by find-missing's mark", false, func(t *testing.T, s *Store, _ string) {
		decideA(t, s, "allow")
		markRelayRow(t, s)
	}, false},
	{"before v7, undecided, closed by find-missing's mark", true, func(t *testing.T, s *Store, _ string) { markRelayRow(t, s) }, false},
	{"undecided, closed by its SessionEnd", false, func(t *testing.T, s *Store, _ string) { endRelayRow(t, s) }, false},
	{"decided, not acked, closed by its SessionEnd", false, func(t *testing.T, s *Store, _ string) {
		decideA(t, s, "allow")
		endRelayRow(t, s)
	}, false},
}

// markRelayRow is find-missing's mark of relayID as it reads now; it must apply.
func markRelayRow(t *testing.T, s *Store) {
	t.Helper()
	if _, res, err := s.MarkMissingIfSameLife(relayID, mustGetSpawn(t, s, relayID).Snapshot); err != nil || res != CondApplied {
		t.Fatalf("MarkMissingIfSameLife = %v, %v; want CondApplied", res, err)
	}
}

// recordAwaitCase records tc's request A on relayID (in check_permission) and
// plays its history.
func recordAwaitCase(t *testing.T, s *Store, path string, tc awaitCase) {
	t.Helper()
	if tc.legacy {
		if err := agentHook(s, relayID, StateCheckPermission, false, "PermissionRequest"); err != nil {
			t.Fatalf("to check_permission: %v", err)
		}
		if err := agentPermissionRequest(s, relayID, tokenA, "Bash", `{}`, 0, WriterProcessHook); err != nil {
			t.Fatalf("legacy insert: %v", err)
		}
	} else {
		insertRelay(t, s, relayReq(tokenA))
	}
	if tc.after != nil {
		tc.after(t, s, path)
	}
}

// TestAwaitingAnswerRule (b.146 rules 9, 12): a request from v7 on awaits an
// answer until its hook acks it or a completed pane answer, find-missing's
// mark or its Spawn's SessionEnd closes it, a recorded verdict
// notwithstanding; one from before v7 while
// it is undecided, not closed and not answered at the pane. Such a request is
// in OpenPermissionRequestsForSpawn and holds the row's move to working, and
// PermissionRow.AwaitsAnswer agrees with the SQL rule.
func TestAwaitingAnswerRule(t *testing.T) {
	for _, tc := range awaitCases {
		t.Run(tc.name, func(t *testing.T) {
			s, path := newRelayRow(t, "on", StateWorking)
			recordAwaitCase(t, s, path, tc)

			if got := len(openTokens(t, s, relayID)) == 1; got != tc.awaits {
				t.Errorf("listed open = %v; want %v", got, tc.awaits)
			}
			if got := mustRequest(t, s, tokenA).AwaitsAnswer(); got != tc.awaits {
				t.Errorf("PermissionRow.AwaitsAnswer = %v; want %v, as the SQL rule", got, tc.awaits)
			}
			if err := agentHook(s, relayID, StateWorking, false, "PostToolUse"); err != nil {
				t.Fatalf("PostToolUse: %v", err)
			}
			wantState := StateWorking
			if tc.awaits {
				wantState = StateCheckPermission
			}
			if got := mustGetSpawn(t, s, relayID).State; got != wantState {
				t.Errorf("state after the move to working = %q; want %q (held while a request awaits an answer)", got, wantState)
			}
		})
	}
}

// TestIdlePromptNotificationRemembered (b.146 problem 3): the main agent's
// idle-prompt Notification moves a working row, or a relay-on check_permission
// row none of whose requests awaits an answer, to waiting; any other row is
// soft refreshed. Every applied one records idle_since.
func TestIdlePromptNotificationRemembered(t *testing.T) {
	cases := []struct {
		name      string
		relay     string
		state     string
		request   *awaitCase // recorded first (the row then reads check_permission); nil none
		wantState string
	}{
		{"working", "on", StateWorking, nil, StateWaiting},
		{"check_permission, its request acked", "on", StateWorking, &awaitCases[2], StateWaiting},
		{"check_permission, its timeout deny delivered", "on", StateWorking, &awaitCases[3], StateWaiting},
		{"check_permission, a request awaiting an answer", "on", StateWorking, &awaitCases[1], StateCheckPermission},
		{"check_permission, relay off", "off", StateCheckPermission, nil, StateCheckPermission},
		{"waiting", "on", StateWaiting, nil, StateWaiting},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, path := newRelayRow(t, tc.relay, tc.state)
			if tc.request != nil {
				recordAwaitCase(t, s, path, *tc.request)
			}
			prior := mustGetSpawn(t, s, relayID)
			mark := TrailMark(t)

			if _, applied, err := s.ApplyHookWaitingIfWorking(relayID, agentGate("Notification", ""), "Notification", "", false); err != nil || !applied.Applied {
				t.Fatalf("ApplyHookWaitingIfWorking = %+v, %v", applied, err)
			}

			after := mustGetSpawn(t, s, relayID)
			if after.State != tc.wantState || after.IdleSince == "" {
				t.Errorf("row = state %q idle_since %q; want %q with idle_since set", after.State, after.IdleSince, tc.wantState)
			}
			lines := TrailEventsSince(t, mark, "ad.spawn.state_transition", relayID)
			if len(lines) != 1 {
				t.Fatalf("state_transition lines = %d; want 1", len(lines))
			}
			assertSpawnStateTransitionFields(t, lines[0], relayID, prior.State, tc.wantState, "Notification", prior.State == tc.wantState)
		})
	}
}

// TestLaterHookClearsIdleSince (b.146 problem 3): any hook after the idle
// Notification clears idle_since, a held move to working and the relay hook's
// first write included.
func TestLaterHookClearsIdleSince(t *testing.T) {
	cases := []struct {
		name string
		hook func(t *testing.T, s *Store)
	}{
		{"a transition", func(t *testing.T, s *Store) {
			if err := agentHook(s, relayID, StateWaiting, false, "Stop"); err != nil {
				t.Fatal(err)
			}
		}},
		{"a soft refresh", func(t *testing.T, s *Store) {
			if err := agentHook(s, relayID, "", true, "Notification"); err != nil {
				t.Fatal(err)
			}
		}},
		{"a move to working held by a request", func(t *testing.T, s *Store) {
			if err := agentHook(s, relayID, StateWorking, false, "PreToolUse"); err != nil {
				t.Fatal(err)
			}
			if got := mustGetSpawn(t, s, relayID).State; got != StateCheckPermission {
				t.Fatalf("state = %q; want check_permission held", got)
			}
		}},
		{"the relay hook's first write", func(t *testing.T, s *Store) { insertRelay(t, s, relayReq(tokenB)) }},
		{"SessionStart", func(t *testing.T, s *Store) {
			if err := agentSessionStart(s, relayID, "sess-later", "", false); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newRelayRow(t, "on", StateWorking)
			insertRelay(t, s, relayReq(tokenA)) // awaits an answer: the Notification leaves check_permission
			if _, _, err := s.ApplyHookWaitingIfWorking(relayID, agentGate("Notification", ""), "Notification", "", false); err != nil {
				t.Fatalf("idle Notification: %v", err)
			}
			if got := mustGetSpawn(t, s, relayID).IdleSince; got == "" {
				t.Fatal("idle_since not set by the idle Notification")
			}

			tc.hook(t, s)

			if got := mustGetSpawn(t, s, relayID).IdleSince; got != "" {
				t.Errorf("idle_since = %q after %s; want cleared", got, tc.name)
			}
		})
	}
}

// TestCapEvictionKeepsRequestsAwaitingAnAnswer (b.146 rule 9): the cap
// eviction removes acked requests but never a verdict its hook has not acked.
func TestCapEvictionKeepsRequestsAwaitingAnAnswer(t *testing.T) {
	s, _ := newRelayRow(t, "on", StateWorking)
	insertRelay(t, s, relayReq(tokenA))
	decideA(t, s, "allow") // decided, not acked: awaits an answer
	insertRelay(t, s, relayReq(tokenB))
	if ok, err := s.DenyRelayTimeout(relayID, tokenB, time.Now(), DefaultLockWait, nil); err != nil || !ok {
		t.Fatalf("timeout deny of B = %v, %v", ok, err)
	}
	if _, applied, err := s.InsertRelayRequest(relayID, agentGate("PermissionRequest", ""), relayReq(tokenC), 2, DefaultLockWait); err != nil || !applied.Applied {
		t.Fatalf("insert C with cap 2 = %+v, %v", applied, err)
	}
	var tokens []string
	reqs, err := s.PermissionRequestsForSpawn(relayID)
	if err != nil {
		t.Fatalf("PermissionRequestsForSpawn: %v", err)
	}
	for _, r := range reqs {
		tokens = append(tokens, r.RequestToken)
	}
	slices.Sort(tokens)
	if want := []string{tokenA, tokenC}; !slices.Equal(tokens, want) {
		t.Errorf("requests after the eviction = %v; want %v (B acked and evicted, A's unacked verdict kept)", tokens, want)
	}
}
