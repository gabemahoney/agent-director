package store

// permission_requests (SRD §6.2, SR-2.1, SR-5.4, SR-7.4, SR-9.2, SR-11): the
// gated insert, first-call-wins decide, the reads, cap eviction, find-missing's
// closeout in its mark's transaction and decide's refusal on a finished Spawn
// (b.146 rule 12). Real on-disk stores, so FK, UNIQUE and transactions are
// exercised end to end.

import (
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// UUIDv4 request tokens, storefix.TestRequestToken{A,B,C}'s values.
const (
	tokenA = "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa"
	tokenB = "bbbbbbbb-bbbb-4bbb-bbbb-bbbbbbbbbbbb"
	tokenC = "cccccccc-cccc-4ccc-accc-cccccccccccc"
)

// seedSpawnForPerm inserts a pending row with its agent's pane recorded, so
// the agent's gated request INSERT applies (SR-22.9).
func seedSpawnForPerm(t *testing.T, s *Store, id, relayMode string) {
	t.Helper()
	if err := insertAgentRow(s, Spawn{ClaudeInstanceID: id, CWD: "/tmp", TmuxSessionName: "cd-test-" + id, RelayMode: relayMode}); err != nil {
		t.Fatalf("insertAgentRow(%s): %v", id, err)
	}
}

// readPermRow reads one request's raw columns, NULL kept apart from "".
func readPermRow(t *testing.T, s *Store, instanceID, requestToken string) (requestID int64, toolName, toolInput string, decision, reason sql.NullString) {
	t.Helper()
	if err := s.db.QueryRow(`SELECT request_id, tool_name, tool_input, decision, decision_reason FROM permission_requests
		WHERE claude_instance_id = ? AND request_token = ?`, instanceID, requestToken).
		Scan(&requestID, &toolName, &toolInput, &decision, &reason); err != nil {
		t.Fatalf("read request (%s, %s): %v", instanceID, requestToken, err)
	}
	return
}

// countPermRows counts permission_requests rows matching where.
func countPermRows(t *testing.T, s *Store, where string, args ...any) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM permission_requests WHERE `+where, args...).Scan(&n); err != nil {
		t.Fatalf("count permission_requests where %s: %v", where, err)
	}
	return n
}

// openTokens returns the tokens of id's open requests, oldest first.
func openTokens(t *testing.T, s *Store, id string) []string {
	t.Helper()
	rows, err := s.OpenPermissionRequestsForSpawn(id)
	if err != nil {
		t.Fatalf("OpenPermissionRequestsForSpawn(%s): %v", id, err)
	}
	var out []string
	for _, r := range rows {
		out = append(out, r.RequestToken)
	}
	return out
}

// TestUpsertOpenPermissionRequest: distinct tokens of one spawn coexist as
// undecided rows that read back field for field (request id, token, created_at);
// a repeated (spawn, token) is ErrRequestTokenCollision and leaves the row
// intact; deleting the spawn cascades to its rows.
func TestUpsertOpenPermissionRequest(t *testing.T) {
	s, _ := openTempStore(t)
	const id = "spawn-append"
	seedSpawnForPerm(t, s, id, "on")
	if got := openTokens(t, s, id); len(got) != 0 {
		t.Fatalf("open requests before any insert = %v; want none", got)
	}
	before := time.Now().UTC().Add(-time.Second)
	for _, r := range [][3]string{{tokenA, "tool_A", `{"a":1}`}, {tokenB, "tool_B", `{"b":2}`}} {
		if err := agentPermissionRequest(s, id, r[0], r[1], r[2], 0, ""); err != nil {
			t.Fatalf("insert %s: %v", r[0], err)
		}
	}
	after := time.Now().UTC().Add(time.Second)
	if got := openTokens(t, s, id); len(got) != 2 || got[0] != tokenA || got[1] != tokenB {
		t.Errorf("open requests = %v; want [%s %s]", got, tokenA, tokenB)
	}
	rawID, _, _, _, _ := readPermRow(t, s, id, tokenA)
	got, err := s.GetPermissionRequest(id, tokenA)
	if err != nil {
		t.Fatalf("GetPermissionRequest: %v", err)
	}
	if got.RequestID == 0 || got.RequestID != rawID || got.RequestToken != tokenA || got.ToolName != "tool_A" ||
		got.ToolInput != `{"a":1}` || got.Decision != "" || got.DecisionReason != "" ||
		got.CreatedAt.Before(before) || got.CreatedAt.After(after) {
		t.Errorf("GetPermissionRequest = %+v; want request id %d, tool_A, undecided, created in [%v, %v]", got, rawID, before, after)
	}

	if err := agentPermissionRequest(s, id, tokenA, "tool_A2", `{"a":99}`, 0, ""); !errors.Is(err, ErrRequestTokenCollision) {
		t.Fatalf("repeated token: err = %v; want ErrRequestTokenCollision", err)
	}
	if _, name, input, decision, _ := readPermRow(t, s, id, tokenA); name != "tool_A" || input != `{"a":1}` || decision.Valid {
		t.Errorf("row after the collision = %q, %q, %v; want the original, undecided", name, input, decision)
	}
	if _, err := s.db.Exec(`DELETE FROM spawns WHERE claude_instance_id = ?`, id); err != nil {
		t.Fatalf("delete spawn: %v", err)
	}
	if n := countPermRows(t, s, "claude_instance_id = ?", id); n != 0 {
		t.Errorf("rows after the spawn's delete = %d; want 0 (ON DELETE CASCADE)", n)
	}
}

// TestDecidePermissionRequest: the first decide of an open row wins and later
// ones (a timeout then an allow included) report not updated; an empty reason
// is stored NULL and reads as ""; decided rows leave the open list.
func TestDecidePermissionRequest(t *testing.T) {
	s, _ := openTempStore(t)
	const id = "spawn-decide"
	seedSpawnForPerm(t, s, id, "on")
	for _, tok := range []string{tokenA, tokenB, tokenC} {
		if err := agentPermissionRequest(s, id, tok, "Bash", `{}`, 0, ""); err != nil {
			t.Fatalf("insert %s: %v", tok, err)
		}
	}
	decides := []struct {
		token, decision, reason string
		want                    bool
	}{
		{tokenA, "allow", "trusted", true},
		{tokenA, "deny", "second attempt", false},
		{tokenB, "deny", DecisionReasonTimeout, true},
		{tokenB, "allow", "user-approved", false},
		{tokenC, "deny", "", true},
	}
	for _, d := range decides {
		if updated, err := s.DecidePermissionRequest(id, d.token, d.decision, d.reason, ""); err != nil || updated != d.want {
			t.Errorf("decide %s %s = %v, %v; want %v", d.token, d.decision, updated, err, d.want)
		}
	}
	for tok, want := range map[string][2]sql.NullString{
		tokenA: {{String: "allow", Valid: true}, {String: "trusted", Valid: true}},
		tokenB: {{String: "deny", Valid: true}, {String: DecisionReasonTimeout, Valid: true}},
		tokenC: {{String: "deny", Valid: true}, {}},
	} {
		if _, _, _, decision, reason := readPermRow(t, s, id, tok); decision != want[0] || reason != want[1] {
			t.Errorf("%s: decision, reason = %+v, %+v; want %+v, %+v", tok, decision, reason, want[0], want[1])
		}
	}
	if got, err := s.GetPermissionRequest(id, tokenC); err != nil || got.Decision != "deny" || got.DecisionReason != "" {
		t.Errorf("GetPermissionRequest(C) = %+v, %v; want deny with reason \"\"", got, err)
	}
	if got := openTokens(t, s, id); len(got) != 0 {
		t.Errorf("open requests after deciding all = %v; want none", got)
	}
}

// TestAmbiguousDecide: an empty token with two open rows is ErrAmbiguousRequest
// and decides neither; with one open row it is not ambiguous.
func TestAmbiguousDecide(t *testing.T) {
	for _, tokens := range [][]string{{tokenA, tokenB}, {tokenA}} {
		s, _ := openTempStore(t)
		const id = "spawn-ambiguous"
		seedSpawnForPerm(t, s, id, "on")
		for _, tok := range tokens {
			if err := agentPermissionRequest(s, id, tok, "Bash", `{}`, 0, ""); err != nil {
				t.Fatalf("insert %s: %v", tok, err)
			}
		}
		updated, err := s.DecidePermissionRequest(id, "", "allow", "", "")
		if ambiguous := len(tokens) > 1; errors.Is(err, ErrAmbiguousRequest) != ambiguous || updated {
			t.Errorf("%d open rows: decide with no token = %v, %v; want ambiguous %v", len(tokens), updated, err, ambiguous)
		}
		if len(tokens) > 1 && len(openTokens(t, s, id)) != 2 {
			t.Errorf("an ambiguous decide decided a row")
		}
	}
}

// TestGetPermissionRequestByToken pins the projection by token: an open row's
// fields with empty decision and zero decided_at; a decided row's decision,
// canonical reason (SR-1.3) and decided_at; and a miss as
// ErrPermissionRequestNotFound, never sql.ErrNoRows (SR-7.4).
func TestGetPermissionRequestByToken(t *testing.T) {
	if DecisionReasonOperator != "operator" || DecisionReasonTimeout != "timeout" || DecisionReasonFindMissing != "find_missing" ||
		DecisionReasonEnded != "ended" {
		t.Errorf("DecisionReason constants = %q, %q, %q, %q; want operator, timeout, find_missing, ended",
			DecisionReasonOperator, DecisionReasonTimeout, DecisionReasonFindMissing, DecisionReasonEnded)
	}
	if WriterProcessResume != "resume" {
		t.Errorf("WriterProcessResume = %q; want resume", WriterProcessResume)
	}
	cases := []struct{ name, decision, reason string }{
		{"open", "", ""},
		{"allow", "allow", ""},
		{"deny operator", "deny", "operator"},
		{"deny timeout", "deny", "timeout"},
		{"deny find_missing", "deny", "find_missing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := openTempStore(t)
			id := "spawn-by-token-" + tc.name
			seedSpawnForPerm(t, s, id, "on")
			before := time.Now().UTC().Add(-time.Second)
			if err := agentPermissionRequest(s, id, tokenA, "Read", `{"file":"/tmp/x"}`, 0, ""); err != nil {
				t.Fatalf("insert: %v", err)
			}
			if tc.decision != "" {
				if updated, err := s.DecidePermissionRequest(id, tokenA, tc.decision, tc.reason, ""); err != nil || !updated {
					t.Fatalf("decide = %v, %v", updated, err)
				}
			}
			got, err := s.GetPermissionRequestByToken(tokenA)
			if err != nil {
				t.Fatalf("GetPermissionRequestByToken: %v", err)
			}
			if got.RequestID == 0 || got.ClaudeInstanceID != id || got.RequestToken != tokenA || got.ToolName != "Read" ||
				got.ToolInput != `{"file":"/tmp/x"}` || got.CreatedAt.Before(before) || got.CreatedAt.After(time.Now().UTC().Add(time.Second)) {
				t.Errorf("row = %+v; want the inserted request", got)
			}
			if got.Decision != tc.decision || got.DecisionReason != tc.reason || got.DecidedAt.IsZero() != (tc.decision == "") {
				t.Errorf("decision, reason, decided_at = %q, %q, %v; want %q, %q, set %v",
					got.Decision, got.DecisionReason, got.DecidedAt, tc.decision, tc.reason, tc.decision != "")
			}
		})
	}
	s, _ := openTempStore(t)
	if _, err := s.GetPermissionRequestByToken("deadbeef-dead-4dea-adea-deadbeefdead"); !errors.Is(err, ErrPermissionRequestNotFound) || errors.Is(err, sql.ErrNoRows) {
		t.Errorf("miss err = %v; want ErrPermissionRequestNotFound, not sql.ErrNoRows", err)
	}
}

// TestGetPermissionRequestReturnsErrNoRows: a lookup with no row, the token on
// another spawn, or another token on the spawn is sql.ErrNoRows.
func TestGetPermissionRequestReturnsErrNoRows(t *testing.T) {
	s, _ := openTempStore(t)
	seedSpawnForPerm(t, s, "real-instance", "on")
	if err := agentPermissionRequest(s, "real-instance", tokenA, "Bash", `{}`, 0, ""); err != nil {
		t.Fatalf("insert: %v", err)
	}
	for _, q := range [][2]string{{"absent", tokenB}, {"wrong-instance", tokenA}, {"real-instance", tokenB}} {
		if _, err := s.GetPermissionRequest(q[0], q[1]); !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("GetPermissionRequest(%s, %s) err = %v; want sql.ErrNoRows", q[0], q[1], err)
		}
	}
}

// TestGetPermissionRequestByTokenConcurrentReads: readers of an open and a
// decided row never fail and see the decided row unchanged while a writer
// inserts and decides (run under -race).
func TestGetPermissionRequestByTokenConcurrentReads(t *testing.T) {
	s, _ := openTempStore(t)
	const id = "spawn-concurrent-reads"
	seedSpawnForPerm(t, s, id, "on")
	for _, tok := range []string{tokenA, tokenB} {
		if err := agentPermissionRequest(s, id, tok, "Bash", `{}`, 0, ""); err != nil {
			t.Fatalf("insert %s: %v", tok, err)
		}
	}
	if updated, err := s.DecidePermissionRequest(id, tokenB, "deny", DecisionReasonOperator, ""); err != nil || !updated {
		t.Fatalf("decide B = %v, %v", updated, err)
	}
	closed, err := s.GetPermissionRequestByToken(tokenB)
	if err != nil {
		t.Fatalf("read B: %v", err)
	}
	var stop atomic.Bool
	var wg sync.WaitGroup
	errs := make(chan error, 9)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				if _, err := s.GetPermissionRequestByToken(tokenA); err != nil {
					errs <- fmt.Errorf("read A: %w", err)
					return
				}
				if cr, err := s.GetPermissionRequestByToken(tokenB); err != nil || cr.Decision != closed.Decision ||
					cr.DecisionReason != closed.DecisionReason || !cr.DecidedAt.Equal(closed.DecidedAt) {
					errs <- fmt.Errorf("read B = %+v, %v; want %+v", cr, err, closed)
					return
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for seq := 1; !stop.Load(); seq++ {
			tok := fmt.Sprintf("%08x-0000-4000-a000-%012x", seq, seq)
			if err := agentPermissionRequest(s, id, tok, "Bash", `{}`, 0, ""); err != nil {
				errs <- fmt.Errorf("insert %d: %w", seq, err)
				return
			}
			if _, err := s.DecidePermissionRequest(id, tokenA, "allow", "", ""); err != nil {
				errs <- fmt.Errorf("decide A: %w", err)
				return
			}
		}
	}()
	time.Sleep(250 * time.Millisecond)
	stop.Store(true)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// seedClosedPermRequests inserts n requests for instanceID (and its row when
// absent), decides each deny, and backdates decided_at to base+i*step through
// a raw connection; it returns the tokens, oldest decided first
// (storefix.SeedClosedPermissionRequests' white-box twin).
func seedClosedPermRequests(t *testing.T, s *Store, dbPath, instanceID string, n int, base time.Time, step time.Duration) []string {
	t.Helper()
	if _, err := s.GetSpawn(instanceID); errors.Is(err, ErrSpawnNotFound) {
		seedSpawnForPerm(t, s, instanceID, "off")
	}
	var tokens []string
	for i := 0; i < n; i++ {
		tok := fmt.Sprintf("%08x-0000-4000-a000-%012x", i, i)
		if err := agentPermissionRequest(s, instanceID, tok, "Bash", `{"cmd":"echo"}`, 0, ""); err != nil {
			t.Fatalf("insert %s: %v", tok, err)
		}
		if updated, err := s.DecidePermissionRequest(instanceID, tok, "deny", DecisionReasonOperator, ""); err != nil || !updated {
			t.Fatalf("decide %s = %v, %v", tok, updated, err)
		}
		tokens = append(tokens, tok)
	}
	withRaw(t, dbPath, func(db *sql.DB) {
		for i, tok := range tokens {
			mustExec(t, db, `UPDATE permission_requests SET decided_at = ? WHERE claude_instance_id = ? AND request_token = ?`,
				base.Add(time.Duration(i)*step).UTC().Format("2006-01-02 15:04:05"), instanceID, tok)
		}
	})
	return tokens
}

// TestPermissionRequestCapEviction pins SR-11.2 and SR-11.4: an insert over the
// cap evicts the oldest decided rows (by decided_at) in one pass, never an open
// row (no error when only open rows remain), and cap 0 disables eviction.
func TestPermissionRequestCapEviction(t *testing.T) {
	cases := []struct {
		name               string
		closed, open, cap  int
		wantEvicted, total int
	}{
		{"at cap: the oldest decided row goes", 10, 0, 10, 1, 10},
		{"over cap on entry: six go in one pass", 15, 0, 10, 6, 10},
		{"open rows only: none evicted", 0, 5, 5, 0, 6},
		{"open rows never evicted", 1, 4, 5, 1, 5},
		{"cap 0 disables eviction", 20, 0, 0, 0, 21},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, path := openTempStore(t)
			const id = "evict"
			seedSpawnForPerm(t, s, id, "on")
			closed := seedClosedPermRequests(t, s, path, id, tc.closed, time.Now().UTC().Add(-2*time.Hour), time.Minute)
			for i := 0; i < tc.open; i++ {
				if err := agentPermissionRequest(s, id, fmt.Sprintf("%08x-1111-4111-a111-%012x", i, i), "Bash", `{}`, 0, ""); err != nil {
					t.Fatalf("insert open %d: %v", i, err)
				}
			}
			const newTok = "ffffffff-ffff-4fff-afff-ffffffffffff"
			if err := agentPermissionRequest(s, id, newTok, "Bash", `{}`, tc.cap, ""); err != nil {
				t.Fatalf("insert with cap %d: %v", tc.cap, err)
			}
			if n := countPermRows(t, s, "1"); n != tc.total {
				t.Errorf("rows = %d; want %d", n, tc.total)
			}
			if n := countPermRows(t, s, "decision IS NULL"); n != tc.open+1 {
				t.Errorf("open rows = %d; want %d (none evicted)", n, tc.open+1)
			}
			for i, tok := range closed {
				if gone := countPermRows(t, s, "request_token = ?", tok) == 0; gone != (i < tc.wantEvicted) {
					t.Errorf("decided row %d evicted = %v; want %v", i, gone, i < tc.wantEvicted)
				}
			}
		})
	}
}

// TestPermissionRequestCapEvictionKeepsNewestWhileOpen pins b.t6e's exemption: while spawn S has open request R, another
// spawn's over-cap insert keeps S's newest request, past the cap if need be, until S records a later one or R closes.
func TestPermissionRequestCapEvictionKeepsNewestWhileOpen(t *testing.T) {
	s, path := openTempStore(t)
	const sid = "evict-s"
	seedSpawnForPerm(t, s, sid, "on")
	base := time.Now().UTC().Add(-2 * time.Hour)
	decide := func(tok string, decidedAt time.Time) { // S's request tok, its decided_at set to decidedAt unless zero
		t.Helper()
		if updated, err := s.DecidePermissionRequest(sid, tok, "allow", "", ""); err != nil || !updated {
			t.Fatalf("decide %s = %v, %v", tok, updated, err)
		}
		if !decidedAt.IsZero() {
			withRaw(t, path, func(db *sql.DB) {
				mustExec(t, db, `UPDATE permission_requests SET decided_at = ? WHERE claude_instance_id = ? AND request_token = ?`,
					decidedAt.Format("2006-01-02 15:04:05"), sid, tok)
			})
		}
	}
	for _, tok := range []string{tokenA, tokenB} { // R = A, then B
		if err := agentPermissionRequest(s, sid, tok, "Bash", `{}`, 0, ""); err != nil {
			t.Fatalf("insert %s: %v", tok, err)
		}
	}
	decide(tokenB, base.Add(-time.Hour)) // the oldest decided_at in the table
	o := seedClosedPermRequests(t, s, path, "evict-o", 3, base, time.Minute)

	steps := []struct {
		name       string
		before     func() // S's writes before the other spawn's insert
		cap, total int
		gone, kept []string
	}{
		{"B, S's newest, kept: the other spawn's oldest decided row goes instead", nil, 5, 5, []string{o[0]}, []string{tokenB, o[1]}},
		{"S's later C decided: B evictable, C kept", func() {
			if err := agentPermissionRequest(s, sid, tokenC, "Bash", `{}`, 0, ""); err != nil {
				t.Fatalf("insert C: %v", err)
			}
			decide(tokenC, base.Add(-30*time.Minute))
		}, 5, 5, []string{tokenB, o[1]}, []string{tokenC, o[2]}},
		{"C kept one row over the cap", nil, 4, 5, []string{o[2]}, []string{tokenC}},
		{"R decided: S's rows evictable", func() { decide(tokenA, time.Time{}) }, 4, 4, []string{tokenA, tokenC}, nil},
	}
	for i, st := range steps {
		if st.before != nil {
			st.before()
		}
		if err := agentPermissionRequest(s, "evict-o", fmt.Sprintf("%08x-1111-4111-a111-%012x", i, i), "Bash", `{}`, st.cap, ""); err != nil {
			t.Fatalf("%s: other spawn's insert with cap %d: %v", st.name, st.cap, err)
		}
		if n := countPermRows(t, s, "1"); n != st.total {
			t.Errorf("%s: rows = %d with cap %d; want %d", st.name, n, st.cap, st.total)
		}
		for _, tok := range st.gone {
			if countPermRows(t, s, "request_token = ?", tok) != 0 {
				t.Errorf("%s: %s kept; want it evicted", st.name, tok)
			}
		}
		for _, tok := range st.kept {
			if countPermRows(t, s, "request_token = ?", tok) != 1 {
				t.Errorf("%s: %s evicted; want it kept", st.name, tok)
			}
		}
	}
}

// TestMarkMissingClosesOpenRequests pins SR-5.4, SR-A-2.5 and b.146 rule 12:
// find-missing's mark denies every open request of its row with reason
// find_missing in the mark's own transaction and leaves a decided request as
// it was; after the commit it emits, per closed request in request-id order,
// one ad.row_mutation.committed (writer find_missing) and one
// permission_orphan_closeout tick. A row with no open request emits nothing.
func TestMarkMissingClosesOpenRequests(t *testing.T) {
	const decided = "dddddddd-dddd-4ddd-addd-dddddddddddd"
	for _, tokens := range [][]string{nil, {tokenA}, {tokenA, tokenB, tokenC}} {
		t.Run(fmt.Sprintf("%d open", len(tokens)), func(t *testing.T) {
			s, _ := openTempStore(t)
			const id = "fm-closeout"
			seedSpawnForPerm(t, s, id, "on")
			if err := agentHook(s, id, StateCheckPermission, false, "test_seed"); err != nil {
				t.Fatalf("to check_permission: %v", err)
			}
			for _, tok := range append([]string{decided}, tokens...) {
				if err := agentPermissionRequest(s, id, tok, "Bash", `{"cmd":"echo"}`, 0, ""); err != nil {
					t.Fatalf("insert %s: %v", tok, err)
				}
			}
			if ok, err := s.DecidePermissionRequest(id, decided, "allow", "", WriterProcessDecide); err != nil || !ok {
				t.Fatalf("decide %s = %v, %v", decided, ok, err)
			}
			mark := TrailMark(t)

			if prior, res, err := s.MarkMissingIfSameLife(id, mustGetSpawn(t, s, id).Snapshot); err != nil || res != CondApplied || prior != StateCheckPermission {
				t.Fatalf("MarkMissingIfSameLife = %q, %v, %v; want check_permission, CondApplied", prior, res, err)
			}

			if state, err := s.GetSpawnState(id); err != nil || state != StateMissing {
				t.Errorf("state = %q, %v; want missing", state, err)
			}
			for _, tok := range tokens {
				if _, _, _, decision, reason := readPermRow(t, s, id, tok); decision.String != "deny" || reason.String != DecisionReasonFindMissing {
					t.Errorf("%s: decision, reason = %+v, %+v; want deny, find_missing", tok, decision, reason)
				}
			}
			if _, _, _, decision, reason := readPermRow(t, s, id, decided); decision.String != "allow" || reason.Valid {
				t.Errorf("decided request = %+v, %+v; want allow, NULL kept", decision, reason)
			}
			if got := openTokens(t, s, id); len(got) != 0 {
				t.Errorf("open requests = %v; want none", got)
			}
			var got, want []string // event/token, in trail order
			for _, tok := range tokens {
				want = append(want, "ad.row_mutation.committed/"+tok, "ad.find_missing.tick/"+tok)
			}
			for _, row := range readStoreTrailLines(t)[mark:] {
				if row["claude_instance_id"] != id {
					continue
				}
				got = append(got, fmt.Sprintf("%v/%v", row["event"], row["request_token"]))
				fields := map[string]string{"source": "ad_find_missing", "reconciliation_reason": "permission_orphan_closeout"}
				if row["event"] == "ad.row_mutation.committed" {
					fields = map[string]string{"source": "ad_store", "writer_process": WriterProcessFindMissing,
						"mutation_kind": "update", "decision": "deny", "decision_reason": DecisionReasonFindMissing, "tool_name": "Bash"}
				}
				for key, want := range fields {
					assertTrailStr(t, row, key, want)
				}
				if ts, ok := row["ts"].(string); !ok || !storeTSRe.MatchString(ts) {
					t.Errorf("%v ts = %v; want a timestamp", row["event"], row["ts"])
				}
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("trail after the mark = %v; want %v", got, want)
			}
		})
	}
}

// TestDecideIfDeliverableRefusesFinishedSpawn pins b.146 rule 12 in decide's
// guarded write: on an open, deliverable request of an ended or missing Spawn
// (one the close of its requests did not reach) it records nothing and emits
// nothing; a live Spawn's request is recorded.
func TestDecideIfDeliverableRefusesFinishedSpawn(t *testing.T) {
	cases := []struct {
		name   string
		finish func(t *testing.T, s *Store, path, id string) // ends the Spawn, the request still open; nil: live
	}{
		{"live", nil},
		{"ended by its agent's SessionEnd, the request recorded after it", func(t *testing.T, s *Store, _, id string) {
			if err := agentHook(s, id, StateEnded, false, "SessionEnd"); err != nil {
				t.Fatalf("to ended: %v", err)
			}
			if err := agentPermissionRequest(s, id, tokenA, "Bash", `{}`, 0, ""); err != nil {
				t.Fatalf("insert: %v", err)
			}
		}},
		{"ended by an earlier release, the request left open", func(t *testing.T, s *Store, path, id string) {
			if err := agentPermissionRequest(s, id, tokenA, "Bash", `{}`, 0, ""); err != nil {
				t.Fatalf("insert: %v", err)
			}
			withRaw(t, path, func(db *sql.DB) {
				mustExec(t, db, `UPDATE spawns SET state = 'ended', ended_at = CURRENT_TIMESTAMP WHERE claude_instance_id = ?`, id)
			})
		}},
		{"missing, the request recorded after the mark", func(t *testing.T, s *Store, _, id string) {
			if _, res, err := s.MarkMissingIfSameLife(id, mustGetSpawn(t, s, id).Snapshot); err != nil || res != CondApplied {
				t.Fatalf("mark = %v, %v", res, err)
			}
			if err := agentPermissionRequest(s, id, tokenA, "Bash", `{}`, 0, ""); err != nil {
				t.Fatalf("insert: %v", err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, path := openTempStore(t)
			const id = "deliverable-finished"
			seedSpawnForPerm(t, s, id, "on")
			if err := agentHook(s, id, StateCheckPermission, false, "test_seed"); err != nil {
				t.Fatalf("to check_permission: %v", err)
			}
			if tc.finish == nil {
				if err := agentPermissionRequest(s, id, tokenA, "Bash", `{}`, 0, ""); err != nil {
					t.Fatalf("insert: %v", err)
				}
			} else {
				tc.finish(t, s, path, id)
			}
			mark := TrailMark(t)

			ok, err := s.DecideRelayRequest(id, tokenA, "allow", "", WriterProcessDecide, time.Now().Add(-time.Hour), DefaultLockWait)

			if live := tc.finish == nil; err != nil || ok != live {
				t.Fatalf("DecidePermissionRequestIfDeliverable = %v, %v; want %v, nil", ok, err, live)
			}
			if tc.finish == nil {
				return
			}
			if got := openTokens(t, s, id); len(got) != 1 || got[0] != tokenA {
				t.Errorf("open requests = %v; want [%s] kept open", got, tokenA)
			}
			if n := len(trailEventsSince(t, mark, "ad.row_mutation.committed")); n != 0 {
				t.Errorf("row_mutation lines = %d; want 0", n)
			}
		})
	}
}
