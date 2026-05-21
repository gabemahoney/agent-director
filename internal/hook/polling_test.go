package hook_test

import (
	"context"
	"database/sql"
	"errors"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/store"
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

// advancingClock is a virtual-time sleeper. Each Sleep call records
// the requested duration and advances a *time.Time the test owns,
// so Poll's deadline math (against the injected nowFunc) progresses
// without any real wall-clock wait. cancelAfter triggers ctx
// cancellation after a fixed iteration count so the ctx-cancel test
// runs synchronously.
type advancingClock struct {
	now         *time.Time
	sleeps      []time.Duration
	cancel      context.CancelFunc
	cancelAfter int // ≥1 to enable; cancels after the Nth Sleep call.
}

func (c *advancingClock) Sleep(_ context.Context, d time.Duration) {
	c.sleeps = append(c.sleeps, d)
	*c.now = c.now.Add(d)
	if c.cancelAfter > 0 && len(c.sleeps) >= c.cancelAfter && c.cancel != nil {
		c.cancel()
		c.cancel = nil // one-shot
	}
}

// setupVirtualClock installs a fresh virtual time origin and the
// SetNowFunc restorer. Returns the virtual now pointer (callers pass
// to advancingClock) plus a cleanup that the test must defer.
func setupVirtualClock(t *testing.T) (*time.Time, func()) {
	t.Helper()
	now := time.Unix(0, 0)
	restore := hook.SetNowFunc(func() time.Time { return now })
	return &now, restore
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
	// give up. Drives the deadline math on virtual time so the test
	// pays zero real wall-clock for the 1s timeout.
	st := &scriptedPollStore{
		rows: []scriptedRow{{row: store.PermissionRow{Decision: ""}}},
	}
	now, restore := setupVirtualClock(t)
	defer restore()
	clock := &advancingClock{now: now}

	res := hook.Poll(context.Background(), st, clock,
		config.Relay{TimeoutSeconds: 1, PollBaseMs: 0, PollJitterMs: 0},
		"id-1", newRNG())

	if res.Decision != "" {
		t.Errorf("expected timeout fail-closed; got %+v", res)
	}
	if res.Why != "polling timeout exceeded" {
		t.Errorf("Why = %q; want 'polling timeout exceeded'", res.Why)
	}
	// Sleeps should sum to ≈ 1s (the timeout); the loop never sleeps
	// past the deadline so the last sleep is clamped.
	var total time.Duration
	for _, d := range clock.sleeps {
		total += d
	}
	if total < 900*time.Millisecond || total > 1100*time.Millisecond {
		t.Errorf("total virtual sleep = %v; want ≈ 1s (within the deadline)", total)
	}
}

func TestPollCtxCancelFailsClosed(t *testing.T) {
	st := &scriptedPollStore{
		rows: []scriptedRow{{row: store.PermissionRow{Decision: ""}}},
	}
	now, restore := setupVirtualClock(t)
	defer restore()

	ctx, cancel := context.WithCancel(context.Background())
	// The clock cancels ctx synchronously after the 3rd virtual
	// sleep — no goroutine, no wall-clock wait.
	clock := &advancingClock{now: now, cancel: cancel, cancelAfter: 3}

	res := hook.Poll(ctx, st, clock,
		config.Relay{TimeoutSeconds: 60, PollBaseMs: 5, PollJitterMs: 5},
		"id-1", newRNG())
	if res.Decision != "" {
		t.Errorf("expected ctx-cancel fail-closed; got %+v", res)
	}
	if res.Why == "" {
		t.Errorf("Why diagnostic empty for ctx-cancel exit")
	}
}

func TestPollFloorEnforced(t *testing.T) {
	// SRD §6.2 invariant: PollBaseMs=0 + PollJitterMs=0 must NOT pin
	// CPU. The 50ms floor is the safety net. Asserted by inspecting
	// each recorded virtual sleep — no real wall-clock time burned.
	st := &scriptedPollStore{
		rows: []scriptedRow{
			{row: store.PermissionRow{Decision: ""}},
			{row: store.PermissionRow{Decision: ""}},
			{row: store.PermissionRow{Decision: ""}},
			{row: store.PermissionRow{Decision: "allow", DecisionReason: ""}},
		},
	}
	now, restore := setupVirtualClock(t)
	defer restore()
	clock := &advancingClock{now: now}

	res := hook.Poll(context.Background(), st, clock,
		config.Relay{TimeoutSeconds: 30, PollBaseMs: 0, PollJitterMs: 0},
		"id-1", newRNG())
	if res.Decision != "allow" {
		t.Fatalf("expected allow after 4 polls; got %+v", res)
	}
	// 3 sleeps between 4 polls; every one must be ≥ pollFloor (50ms).
	if len(clock.sleeps) != 3 {
		t.Fatalf("recorded sleeps = %v; want exactly 3 between 4 polls", clock.sleeps)
	}
	for i, d := range clock.sleeps {
		if d < 50*time.Millisecond {
			t.Errorf("sleep[%d] = %v; want >= 50ms floor", i, d)
		}
	}
}
