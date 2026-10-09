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

// advancingClock is a concurrency-safe virtual-time sleeper: each Sleep records d and
// advances *now; cancelAfter/runAt fire cancel/run once at the Nth Sleep.
type advancingClock struct {
	mu          sync.Mutex
	now         *time.Time
	sleeps      []time.Duration
	cancel      context.CancelFunc
	cancelAfter int // ≥1 to enable; cancels after the Nth Sleep call.
	runAt       int // ≥1 to enable; calls run after the Nth Sleep call.
	run         func()
}

func (c *advancingClock) Sleep(_ context.Context, d time.Duration) {
	c.mu.Lock()
	c.sleeps = append(c.sleeps, d)
	*c.now = c.now.Add(d)
	var cancel context.CancelFunc
	if c.cancelAfter > 0 && len(c.sleeps) >= c.cancelAfter && c.cancel != nil {
		cancel, c.cancel = c.cancel, nil // one-shot
	}
	var run func()
	if c.runAt > 0 && len(c.sleeps) >= c.runAt && c.run != nil {
		run, c.run = c.run, nil // one-shot
	}
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if run != nil {
		run()
	}
}

// Now reads the virtual time; it is HandleConfig.Now for hookConfig's clock.
func (c *advancingClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return *c.now
}

// Sleeps returns a copy of the durations slept so far.
func (c *advancingClock) Sleeps() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.sleeps...)
}

// Advance moves the virtual time on by d without a sleep (a slow store call).
func (c *advancingClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	*c.now = c.now.Add(d)
}

// setupVirtualClock returns a fresh virtual time origin (for an
// advancingClock, whose Now is Poll's clock) and a no-op restorer.
func setupVirtualClock(t *testing.T) (*time.Time, func()) {
	t.Helper()
	now := time.Unix(0, 0)
	return &now, func() {}
}

// TestPoll pins Poll's exits (SRD §6.2) on virtual time: a decided row
// returns its decision; an undecided one is re-read, sleeping at least the
// 50 ms floor even with no base or jitter, never past the deadline it is
// given (the relay hook's own, b.146 rule 4); at that deadline it returns
// TimedOut, without one more read; an absent row, a sixth consecutive read
// error and a cancelled context end it with no decision and not TimedOut (the
// relay hook then answers nothing, rule 3). CreatedAt is the last read row's.
func TestPoll(t *testing.T) {
	start := time.Unix(0, 0) // setupVirtualClock's origin: Poll's start
	created := start.Add(-700 * time.Millisecond)
	undecided := store.PermissionRow{CreatedAt: created}
	cases := []struct {
		name        string
		rows        []store.PermissionRow
		errs        []error       // per row; nil = none
		deadline    time.Duration // after start
		cfg         config.Relay
		cancelAfter int
		decision    string
		reason      string
		timedOut    bool
		why         string        // "" = any, when there is no decision
		sleeps      int           // -1 = unchecked
		waited      time.Duration // total virtual sleep; 0 = unchecked
		created     time.Time     // CreatedAt; zero = none read
	}{
		{name: "decided", rows: []store.PermissionRow{{Decision: "allow", DecisionReason: "ok", CreatedAt: created}},
			deadline: 5 * time.Second, decision: "allow", reason: "ok", created: created},
		{name: "decided on the fourth read, floor sleeps", rows: []store.PermissionRow{undecided, undecided, undecided,
			{Decision: "deny", DecisionReason: "no", CreatedAt: created}},
			deadline: 30 * time.Second, decision: "deny", reason: "no", sleeps: 3, created: created},
		{name: "row absent", rows: []store.PermissionRow{undecided}, errs: []error{sql.ErrNoRows},
			deadline: 5 * time.Second, sleeps: 0},
		{name: "read-retry budget", rows: make([]store.PermissionRow, 7), errs: repeatErr(7, errors.New("flaky db")),
			deadline: 30 * time.Second, sleeps: -1},
		{name: "deadline", rows: []store.PermissionRow{undecided}, deadline: time.Second,
			timedOut: true, why: "polling timeout exceeded", sleeps: -1, waited: time.Second, created: created},
		{name: "deadline already passed: no read", rows: []store.PermissionRow{undecided}, deadline: -time.Second,
			timedOut: true, why: "polling timeout exceeded", sleeps: 0},
		{name: "reads failing, then the deadline", rows: []store.PermissionRow{{}, {}, undecided},
			errs: []error{errors.New("flaky db"), errors.New("flaky db"), nil}, deadline: time.Second,
			timedOut: true, why: "polling timeout exceeded", sleeps: -1, waited: time.Second, created: created},
		{name: "context cancelled", rows: []store.PermissionRow{undecided}, deadline: 60 * time.Second,
			cfg: config.Relay{PollBaseMs: 5, PollJitterMs: 5}, cancelAfter: 3, sleeps: 3, created: created},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs := tc.errs
			if errs == nil {
				errs = make([]error, len(tc.rows))
			}
			st := &flakyRelayStore{getRows: tc.rows, getErrs: errs}
			now, restore := setupVirtualClock(t)
			defer restore()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			clock := &advancingClock{now: now, cancel: cancel, cancelAfter: tc.cancelAfter}

			res := hook.Poll(ctx, st, clock, clock.Now, start.Add(tc.deadline), tc.cfg, "id-1", "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa", rand.New(rand.NewSource(1)))

			if res.Decision != tc.decision || res.Reason != tc.reason || res.TimedOut != tc.timedOut || !res.CreatedAt.Equal(tc.created) {
				t.Errorf("res = %+v; want decision %q reason %q, TimedOut %v, CreatedAt %v", res, tc.decision, tc.reason, tc.timedOut, tc.created)
			}
			if tc.decision == "" && (res.Why == "" || tc.why != "" && res.Why != tc.why) {
				t.Errorf("Why = %q; want %q (non-empty)", res.Why, tc.why)
			}
			if tc.name == "deadline already passed: no read" && st.idx.Load() != 0 {
				t.Errorf("reads = %d; want none past the deadline", st.idx.Load())
			}
			sleeps := clock.Sleeps()
			if tc.sleeps >= 0 && len(sleeps) != tc.sleeps {
				t.Errorf("sleeps = %v; want %d", sleeps, tc.sleeps)
			}
			var waited time.Duration
			for i, d := range sleeps {
				waited += d
				if d < 50*time.Millisecond && tc.waited == 0 {
					t.Errorf("sleep[%d] = %v; want >= the 50ms floor", i, d)
				}
			}
			if tc.waited != 0 && waited != tc.waited {
				t.Errorf("total virtual sleep = %v; want %v (never past the deadline)", waited, tc.waited)
			}
		})
	}
}

// repeatErr is n copies of err.
func repeatErr(n int, err error) []error {
	out := make([]error, n)
	for i := range out {
		out[i] = err
	}
	return out
}
