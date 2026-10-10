package store

// request_proof_test.go — a hook's proof that permission requests' dialogs
// are gone (b.146 step 2c; ProveRequestsGone): which requests a PostToolUse's
// tool_ran and a main-agent end of turn's turn_end cover, the hook gate, and
// "written before the hook" decided by the read before the write. The close's
// agent_gone proof is relay_close_test.go's. Seeds: relay_writes_test.go's
// relay rows.

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// proofAt is the instant a test proof records as proven_gone_at.
var proofAt = time.UnixMilli(1_790_000_000_123)

// mainReq is relayReq(token) as the main agent's request (no agent_id).
func mainReq(token string) RelayRequest {
	r := relayReq(token)
	r.AgentID = ""
	return r
}

// seedProofRequests records on relayID: A the main agent's, B a subagent's
// (agent_id set), and C recorded before schema v7 (no tool_use_id, no agent_id).
func seedProofRequests(t *testing.T, s *Store) {
	t.Helper()
	insertRelay(t, s, mainReq(tokenA))
	insertRelay(t, s, relayReq(tokenB))
	if err := agentPermissionRequest(s, relayID, tokenC, "Bash", `{}`, 0, ""); err != nil {
		t.Fatalf("insert C: %v", err)
	}
}

// proven is how each of tokens reads now: its proven_gone_how, "" when unproven.
func proven(t *testing.T, s *Store, tokens ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, tok := range tokens {
		pr := mustRequest(t, s, tok)
		if pr.ProvenGone() != (pr.ProvenGoneHow != "") {
			t.Errorf("%s: proven_gone_at %v with proven_gone_how %q; want both or neither", tok, pr.ProvenGoneAt, pr.ProvenGoneHow)
		}
		out[tok] = pr.ProvenGoneHow
	}
	return out
}

// TestProveRequestsGone (b.146 step 2c rule 1): tool_ran proves the requests
// carrying its tool_use_id, a subagent's included; turn_end proves every
// request with no agent_id, one recorded before v7 included, never a
// subagent's; another or no tool_use_id, a foreign hook or an unknown proof
// proves nothing; a request already proven keeps its proof. Another row's
// main-agent request with A's tool_use_id is never touched.
func TestProveRequestsGone(t *testing.T) {
	toolRan := func(id string) RequestProof { return RequestProof{How: ProvenGoneToolRan, ToolUseID: id, At: proofAt} }
	turnEnd := RequestProof{How: ProvenGoneTurnEnd, At: proofAt}
	cases := []struct {
		name    string
		before  *RequestProof // an earlier proof, an hour before proofAt
		proof   RequestProof
		foreign bool
		wantErr bool
		want    map[string]string // A, B, C
	}{
		{name: "tool_ran, the main agent's tool_use_id", proof: toolRan("toolu_" + tokenA[:8]),
			want: map[string]string{tokenA: ProvenGoneToolRan, tokenB: "", tokenC: ""}},
		{name: "tool_ran, a subagent's tool_use_id", proof: toolRan("toolu_" + tokenB[:8]),
			want: map[string]string{tokenA: "", tokenB: ProvenGoneToolRan, tokenC: ""}},
		{name: "tool_ran, another tool_use_id", proof: toolRan("toolu_other"),
			want: map[string]string{tokenA: "", tokenB: "", tokenC: ""}},
		{name: "tool_ran, no tool_use_id", proof: toolRan(""),
			want: map[string]string{tokenA: "", tokenB: "", tokenC: ""}},
		{name: "turn_end", proof: turnEnd,
			want: map[string]string{tokenA: ProvenGoneTurnEnd, tokenB: "", tokenC: ProvenGoneTurnEnd}},
		{name: "turn_end after A's tool ran", before: ptrProof(toolRan("toolu_" + tokenA[:8])), proof: turnEnd,
			want: map[string]string{tokenA: ProvenGoneToolRan, tokenB: "", tokenC: ProvenGoneTurnEnd}},
		{name: "turn_end from another process", proof: turnEnd, foreign: true,
			want: map[string]string{tokenA: "", tokenB: "", tokenC: ""}},
		{name: "an unknown proof", proof: RequestProof{How: "agent_gone", At: proofAt}, wantErr: true,
			want: map[string]string{tokenA: "", tokenB: "", tokenC: ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newRelayRow(t, "on", StateWorking)
			seedProofRequests(t, s)
			addOtherRelayRow(t, s)
			insertOther(t, s, mainReq(tokenA), 0)
			other := otherRequest(t, s, tokenA)
			if tc.before != nil {
				early := *tc.before
				early.At = proofAt.Add(-time.Hour)
				if n, err := s.ProveRequestsGone(relayID, agentGate("PostToolUse", ""), early); err != nil || n != 1 {
					t.Fatalf("earlier proof = %d, %v; want 1", n, err)
				}
			}
			gate := agentGate("Stop", "")
			if tc.foreign {
				gate = foreignGate("Stop", "")
			}

			n, err := s.ProveRequestsGone(relayID, gate, tc.proof)

			if (err != nil) != tc.wantErr || tc.wantErr && !errors.Is(err, errUnknownProof) {
				t.Fatalf("err = %v; want errUnknownProof %v", err, tc.wantErr)
			}
			var wantN int64
			for _, how := range tc.want {
				if how == tc.proof.How {
					wantN++
				}
			}
			if n != wantN {
				t.Errorf("proved %d; want %d", n, wantN)
			}
			got := proven(t, s, tokenA, tokenB, tokenC)
			for tok, how := range tc.want {
				if got[tok] != how {
					t.Errorf("%s proven_gone_how = %q; want %q", tok, got[tok], how)
				}
				if pr := mustRequest(t, s, tok); how == tc.proof.How && !pr.ProvenGoneAt.Equal(proofAt) {
					t.Errorf("%s proven_gone_at = %v; want the proof's %v", tok, pr.ProvenGoneAt, proofAt)
				}
			}
			if tc.before != nil {
				if pr := mustRequest(t, s, tokenA); !pr.ProvenGoneAt.Equal(proofAt.Add(-time.Hour)) {
					t.Errorf("A proven_gone_at = %v; want its earlier proof's %v kept", pr.ProvenGoneAt, proofAt.Add(-time.Hour))
				}
			}
			if got := otherRequest(t, s, tokenA); !reflect.DeepEqual(got, other) {
				t.Errorf("%s's request changed:\n before %+v\n after  %+v", otherRelayID, other, got)
			}
		})
	}
}

// ptrProof is p's address.
func ptrProof(p RequestProof) *RequestProof { return &p }

// TestTurnEndCoversOnlyRequestsWrittenBeforeIt (b.146 step 2c rule 1, "written
// after the request"): the newest request a Stop may prove is read before its
// write, so request B, committed by another process between that read and the
// write (as while the write waits for the lock), stays unproven; A, recorded
// before, is proven.
func TestTurnEndCoversOnlyRequestsWrittenBeforeIt(t *testing.T) {
	s, path := newRelayRow(t, "on", StateWorking)
	insertRelay(t, s, mainReq(tokenA))
	landed := false
	beforeEachExec(t, s, path, func(query string) {
		if landed || !strings.Contains(query, "SET proven_gone_at") {
			return
		}
		landed = true
		withRaw(t, path, func(db *sql.DB) {
			mustExec(t, db, `INSERT INTO permission_requests (claude_instance_id, request_token, tool_name, tool_input)
				VALUES (?, ?, 'Bash', '{}')`, relayID, tokenB)
		})
	})

	n, err := s.ProveRequestsGone(relayID, agentGate("Stop", ""), RequestProof{How: ProvenGoneTurnEnd, At: proofAt})

	if !landed || err != nil || n != 1 {
		t.Fatalf("ProveRequestsGone = %d, %v, B committed before its write %v; want 1 (A only), B committed", n, err, landed)
	}
	if got := proven(t, s, tokenA, tokenB); got[tokenA] != ProvenGoneTurnEnd || got[tokenB] != "" {
		t.Errorf("proven = %v; want A turn_end, B (committed after its read) unproven", got)
	}
}

// beforeEachExec replaces s's connection with one to path on which every Exec
// runs hook(query) first, on the caller's goroutine, before the statement
// reaches SQLite: another process's write lands between s's reads and its
// next write.
func beforeEachExec(t *testing.T, s *Store, path string, hook func(query string)) {
	t.Helper()
	dsn := fmt.Sprintf("%s?_pragma=busy_timeout(%d)&_pragma=foreign_keys(1)", path, DefaultBusyTimeoutMs)
	db := sql.OpenDB(execHookConnector{drv: s.db.Driver(), dsn: dsn, hook: hook})
	db.SetMaxOpenConns(1)
	_ = s.db.Close()
	s.db = db
	t.Cleanup(func() { _ = db.Close() })
}

// execHookConnector opens connections to dsn through drv that run hook before
// each Exec.
type execHookConnector struct {
	drv  driver.Driver
	dsn  string
	hook func(query string)
}

func (c execHookConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.drv.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return execHookConn{Conn: conn, hook: c.hook}, nil
}

func (c execHookConnector) Driver() driver.Driver { return c.drv }

// execHookConn is a driver connection that runs hook before each Exec.
type execHookConn struct {
	driver.Conn
	hook func(query string)
}

func (c execHookConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.hook(query)
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}

func (c execHookConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}
