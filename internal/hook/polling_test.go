package hook_test

import (
	"context"
	"database/sql"
	"errors"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/gabemahoney/claude-director/internal/config"
	"github.com/gabemahoney/claude-director/internal/hook"
	"github.com/gabemahoney/claude-director/internal/store"
)

// scriptedPollStore is the seam the Poll loop reads from. The
// sequence drives the per-iteration outcome: each call returns the
// next entry, with the LAST entry sticky (so a long-running test
// settles on a final state).
type scriptedPollStore struct {
	mu     sync.Mutex
	rows   []scriptedRow
	idx    int
	calls  int
}

type scriptedRow struct {
	row store.PermissionRow
	err error
}

func (s *scriptedPollStore) GetPermissionRequest(_ string) (store.PermissionRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.idx >= len(s.rows) {
		// Sticky last entry.
		last := s.rows[len(s.rows)-1]
		return last.row, last.err
	}
	r := s.rows[s.idx]
	s.idx++
	return r.row, r.err
}

// fastClock is a test sleeper that returns immediately. ctx cancel
// is still honored so the cancel-test case can observe it.
type fastClock struct{}

func (fastClock) Sleep(ctx context.Context, _ time.Duration) {
	select {
	case <-ctx.Done():
	default:
	}
}

func newRNG() *rand.Rand { return rand.New(rand.NewSource(1)) }

func TestPollReturnsDecisionWhenAvailable(t *testing.T) {
	st := &scriptedPollStore{
		rows: []scriptedRow{
			{row: store.PermissionRow{Decision: "allow", DecisionReason: "ok"}},
		},
	}
	res := hook.Poll(context.Background(), st, fastClock{},
		config.Relay{TimeoutSeconds: 5}, "id-1", newRNG())
	if res.Decision != "allow" || res.Reason != "ok" {
		t.Errorf("res = %+v; want allow/ok", res)
	}
}

func TestPollWaitsForDecision(t *testing.T) {
	// First two reads return an undecided row; third has the decision.
	st := &scriptedPollStore{
		rows: []scriptedRow{
			{row: store.PermissionRow{Decision: ""}},
			{row: store.PermissionRow{Decision: ""}},
			{row: store.PermissionRow{Decision: "deny", DecisionReason: "no"}},
		},
	}
	res := hook.Poll(context.Background(), st, fastClock{},
		config.Relay{TimeoutSeconds: 5}, "id-1", newRNG())
	if res.Decision != "deny" || res.Reason != "no" {
		t.Errorf("res = %+v; want deny/no", res)
	}
	if st.calls < 3 {
		t.Errorf("calls = %d; want >= 3 (one per row)", st.calls)
	}
}

func TestPollRowAbsentFailsClosed(t *testing.T) {
	st := &scriptedPollStore{
		rows: []scriptedRow{{err: sql.ErrNoRows}},
	}
	res := hook.Poll(context.Background(), st, fastClock{},
		config.Relay{TimeoutSeconds: 5}, "id-1", newRNG())
	if res.Decision != "" {
		t.Errorf("expected fail-closed (empty Decision); got %+v", res)
	}
	if res.Why == "" {
		t.Errorf("Why diagnostic empty")
	}
}

func TestPollReadRetryBudget(t *testing.T) {
	// 6 consecutive errors → exceeds the 5-retry budget → fail-closed.
	rows := make([]scriptedRow, 7)
	for i := range rows {
		rows[i] = scriptedRow{err: errors.New("flaky db")}
	}
	st := &scriptedPollStore{rows: rows}
	res := hook.Poll(context.Background(), st, fastClock{},
		config.Relay{TimeoutSeconds: 30}, "id-1", newRNG())
	if res.Decision != "" {
		t.Errorf("expected fail-closed after exhausting retries; got %+v", res)
	}
}

func TestPollTimeoutFailsClosed(t *testing.T) {
	// Row stays undecided forever; timeout=1s drives the loop to
	// give up. Use real clock to honor real-time deadline.
	st := &scriptedPollStore{
		rows: []scriptedRow{{row: store.PermissionRow{Decision: ""}}},
	}
	start := time.Now()
	res := hook.Poll(context.Background(), st, hook.DefaultPollClock(),
		config.Relay{TimeoutSeconds: 1, PollBaseMs: 0, PollJitterMs: 0},
		"id-1", newRNG())
	elapsed := time.Since(start)
	if res.Decision != "" {
		t.Errorf("expected timeout fail-closed; got %+v", res)
	}
	if elapsed < 800*time.Millisecond || elapsed > 3*time.Second {
		t.Errorf("elapsed = %v; want ~1s", elapsed)
	}
}

func TestPollCtxCancelFailsClosed(t *testing.T) {
	st := &scriptedPollStore{
		rows: []scriptedRow{{row: store.PermissionRow{Decision: ""}}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	// Cancel after a brief delay.
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	res := hook.Poll(ctx, st, hook.DefaultPollClock(),
		config.Relay{TimeoutSeconds: 60, PollBaseMs: 5, PollJitterMs: 5},
		"id-1", newRNG())
	if res.Decision != "" {
		t.Errorf("expected ctx-cancel fail-closed; got %+v", res)
	}
}

func TestPollFloorEnforced(t *testing.T) {
	// SRD §6.2 invariant: PollBaseMs=0 + PollJitterMs=0 must NOT pin
	// CPU. The 50ms floor is the safety net.
	st := &scriptedPollStore{
		rows: []scriptedRow{
			{row: store.PermissionRow{Decision: ""}},
			{row: store.PermissionRow{Decision: ""}},
			{row: store.PermissionRow{Decision: ""}},
			{row: store.PermissionRow{Decision: "allow", DecisionReason: ""}},
		},
	}
	start := time.Now()
	res := hook.Poll(context.Background(), st, hook.DefaultPollClock(),
		config.Relay{TimeoutSeconds: 30, PollBaseMs: 0, PollJitterMs: 0},
		"id-1", newRNG())
	elapsed := time.Since(start)
	if res.Decision != "allow" {
		t.Fatalf("expected allow after 4 polls; got %+v", res)
	}
	// 3 sleeps of >=50ms each → >= 150ms.
	if elapsed < 150*time.Millisecond {
		t.Errorf("elapsed = %v; want >= 150ms (3 sleeps × 50ms floor)", elapsed)
	}
}
