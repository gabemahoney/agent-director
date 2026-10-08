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

// setupVirtualClock installs a fresh virtual time origin as Poll's wall clock
// and returns it (for an advancingClock) and the restorer the test must defer.
func setupVirtualClock(t *testing.T) (*time.Time, func()) {
	t.Helper()
	now := time.Unix(0, 0)
	restore := hook.SetNowFunc(func() time.Time { return now })
	return &now, restore
}

// TestPoll pins Poll's exits (SRD §6.2, §6.4) on virtual time: a decided row
// returns its decision; an undecided one is re-read, sleeping at least the
// 50 ms floor even with no base or jitter; an absent row, a sixth consecutive
// read error, the timeout and a cancelled context fail closed (no decision,
// a Why). The timeout counts the window from the stored created_at, never
// later than from Poll's start (b.z6g).
func TestPoll(t *testing.T) {
	start := time.Unix(0, 0) // setupVirtualClock's origin: Poll's start
	undecided := store.PermissionRow{CreatedAt: start}
	createdAt := func(offset time.Duration) []store.PermissionRow {
		return []store.PermissionRow{{CreatedAt: start.Add(offset)}}
	}
	cases := []struct {
		name        string
		rows        []store.PermissionRow
		errs        []error // per row; nil = none
		cfg         config.Relay
		cancelAfter int
		decision    string
		reason      string
		why         string        // "" = any, when there is no decision
		sleeps      int           // -1 = unchecked
		waited      time.Duration // total virtual sleep; 0 = unchecked
	}{
		{name: "decided", rows: []store.PermissionRow{{Decision: "allow", DecisionReason: "ok", CreatedAt: start}},
			cfg: config.Relay{TimeoutSeconds: 5}, decision: "allow", reason: "ok"},
		{name: "decided on the fourth read, floor sleeps", rows: []store.PermissionRow{undecided, undecided, undecided,
			{Decision: "deny", DecisionReason: "no", CreatedAt: start}},
			cfg: config.Relay{TimeoutSeconds: 30}, decision: "deny", reason: "no", sleeps: 3},
		{name: "row absent", rows: []store.PermissionRow{undecided}, errs: []error{sql.ErrNoRows},
			cfg: config.Relay{TimeoutSeconds: 5}, sleeps: -1},
		{name: "read-retry budget", rows: make([]store.PermissionRow, 7), errs: repeatErr(7, errors.New("flaky db")),
			cfg: config.Relay{TimeoutSeconds: 30}, sleeps: -1},
		{name: "timeout", rows: []store.PermissionRow{undecided}, cfg: config.Relay{TimeoutSeconds: 1},
			why: "polling timeout exceeded", sleeps: -1, waited: time.Second},
		// created_at keeps whole seconds, so it is up to a second before Poll's start.
		{name: "timeout counted from created_at", rows: createdAt(-900 * time.Millisecond), cfg: config.Relay{TimeoutSeconds: 1},
			why: "polling timeout exceeded", sleeps: -1, waited: 100 * time.Millisecond},
		{name: "created_at after Poll's start (clock stepped back), timeout counted from the start",
			rows: createdAt(10 * time.Second), cfg: config.Relay{TimeoutSeconds: 1},
			why: "polling timeout exceeded", sleeps: -1, waited: time.Second},
		// A failed read's zero row has no created_at to count from.
		{name: "reads failing before the first success, timeout counted from the start",
			rows: []store.PermissionRow{{}, {}, undecided}, errs: []error{errors.New("flaky db"), errors.New("flaky db"), nil},
			cfg: config.Relay{TimeoutSeconds: 1}, why: "polling timeout exceeded", sleeps: -1, waited: time.Second},
		{name: "context cancelled", rows: []store.PermissionRow{undecided}, cfg: config.Relay{TimeoutSeconds: 60, PollBaseMs: 5, PollJitterMs: 5},
			cancelAfter: 3, sleeps: 3},
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

			res := hook.Poll(ctx, st, clock, tc.cfg, "id-1", "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa", rand.New(rand.NewSource(1)))

			if res.Decision != tc.decision || res.Reason != tc.reason {
				t.Errorf("res = %+v; want decision %q reason %q", res, tc.decision, tc.reason)
			}
			if tc.decision == "" && (res.Why == "" || tc.why != "" && res.Why != tc.why) {
				t.Errorf("Why = %q; want %q (non-empty)", res.Why, tc.why)
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
