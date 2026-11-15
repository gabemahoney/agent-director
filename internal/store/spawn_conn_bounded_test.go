package store

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/testsupport/sqlitelockfix"
)

// TestGetSpawnConnBounded (b.kii repair sf2o-5 R4-1, sf2o-6 R5-1): pause's
// first read bounds only its wait for the store's one connection. With the
// connection free it is GetSpawn: the row, or ErrSpawnNotFound, queried. With
// the connection held by another call, a deadline or a cancellation ends the
// wait with no SQL run: not queried, the context's error, never ErrStoreBusy.
// With a lock another connection holds past the store's busy timeout, the
// read itself (here the setup of the new connection it opens, as GetSpawn
// opens one) waits that whole timeout, not cut by a shorter ctx, and fails
// with GetSpawn's error, queried. A returned, unopened lazy connection
// (TestProductionFreshLazyBorrowerMethods "returned_unused") is set up by the
// pool's reacquisition, inside the bounded wait: its context cancelled or its
// deadline crossed after the first setup statement, that setup and the read
// still run unbounded and answer as GetSpawn does, the row, or under a lock
// GetSpawn's error after the store's busy timeout.
func TestGetSpawnConnBounded(t *testing.T) {
	t.Run("connection free", func(t *testing.T) {
		s, _ := openTempStore(t)
		id := busyLaunchInsert(t, s, AgentCodex)
		want := busyLaunchRow(t, s, id)

		got, queried, err := s.GetSpawnConnBounded(context.Background(), id)

		if err != nil || !queried || !reflect.DeepEqual(got, want) {
			t.Fatalf("GetSpawnConnBounded = %+v, %t, %v; want GetSpawn's %+v, queried", got, queried, err, want)
		}
		if _, queried, err := s.GetSpawnConnBounded(context.Background(), "no-such-id"); !queried || !errors.Is(err, ErrSpawnNotFound) {
			t.Errorf("unknown id = queried %t, %v; want ErrSpawnNotFound, queried", queried, err)
		}
	})
	t.Run("connection held", func(t *testing.T) {
		s, _ := openTempStore(t)
		id := busyLaunchInsert(t, s, AgentCodex)
		release := holdConnection(t, s)
		defer release()
		deadline, cancelDeadline := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancelDeadline()
		cancelled, cancel := context.WithCancel(context.Background())
		cancel()
		for _, tc := range []struct {
			name string
			ctx  context.Context
			want error
		}{{"deadline", deadline, context.DeadlineExceeded}, {"cancelled", cancelled, context.Canceled}} {
			_, queried, err := s.GetSpawnConnBounded(tc.ctx, id)
			if queried || !errors.Is(err, tc.want) || errors.Is(err, ErrStoreBusy) {
				t.Errorf("%s: GetSpawnConnBounded = queried %t, %v; want %v, not queried, not ErrStoreBusy", tc.name, queried, err, tc.want)
			}
		}
	})
	t.Run("lock held past the busy timeout", func(t *testing.T) {
		const busy = time.Second
		s, path := openTempStore(t)
		id := busyLaunchInsert(t, s, AgentCodex)
		ls, err := OpenWithBusyTimeout(path, int(busy.Milliseconds()))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = ls.Close() })
		// No idle connection keeps its shared lock: the holder needs the store alone.
		s.db.SetMaxIdleConns(0)
		ls.db.SetMaxIdleConns(0)
		sqlitelockfix.HoldExclusive(t, path)
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		start := time.Now()

		_, queried, err := ls.GetSpawnConnBounded(ctx, id)

		elapsed := time.Since(start)
		_, legacy := ls.GetSpawn(id)
		if legacy == nil || !strings.Contains(legacy.Error(), "database is locked") {
			t.Fatalf("GetSpawn under the lock = %v; want SQLite's busy error", legacy)
		}
		if !queried || err == nil || err.Error() != legacy.Error() || errors.Is(err, ErrStoreBusy) {
			t.Errorf("GetSpawnConnBounded = queried %t, %v; want GetSpawn's %v, queried", queried, err, legacy)
		}
		if elapsed < busy*3/4 {
			t.Errorf("GetSpawnConnBounded failed after %v; want the store's whole %v busy timeout, not its 100 ms ctx", elapsed, busy)
		}
	})
	for _, tc := range []struct {
		name   string
		armed  bool // a real deadline, armed at the barrier 50 ms on and crossed there; else a cancel there
		locked bool
	}{
		{"cancelled after the first setup statement", false, false},
		{"deadline crossed after the first setup statement", true, false},
		{"setup lock, cancelled after the first setup statement", false, true},
	} {
		t.Run("reused lazy connection, "+tc.name, func(t *testing.T) {
			s, path := newRelayRow(t, "on", StateWorking)
			o := installLazyPool(t, s, path) // its connections set busy_timeout 137 ms
			returned, cancelReturned := context.WithCancel(context.Background())
			lease, err := s.db.Conn(returned)
			if err != nil {
				t.Fatal(err)
			}
			_ = lease.Close() // returned unused: its next reacquisition sets it up
			cancelReturned()
			if n := o.b.opens.Load(); n != 0 {
				t.Fatalf("the returned lease opened SQLite %d times; want it unopened", n)
			}
			if tc.locked {
				sqlitelockfix.HoldExclusive(t, path)
			}
			var ctx context.Context
			var cancel, cut context.CancelFunc
			if tc.armed {
				d := newArmedDeadline()
				ctx, cancel = d, d.cancel
				cut = func() { d.arm(50 * time.Millisecond); <-d.Done() }
			} else {
				ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
				cut = cancel
			}
			defer cancel()
			var bounded []string
			var once sync.Once
			var live bool
			o.b.before = func(sctx context.Context, q string) error {
				if sctx.Done() != nil {
					bounded = append(bounded, q)
				}
				return nil
			}
			o.b.afterExec = func(_ context.Context, q string, _ error) {
				if strings.HasPrefix(q, "PRAGMA busy_timeout") {
					once.Do(func() { live = ctx.Err() == nil; cut() })
				}
			}
			start := time.Now()

			got, queried, err := s.GetSpawnConnBounded(ctx, relayID)

			elapsed := time.Since(start)
			o.b.before, o.b.afterExec = nil, nil
			if !live || ctx.Err() == nil {
				t.Fatalf("acquisition context live at the setup barrier %t, then %v; want it live there and cut during the connection's setup", live, ctx.Err())
			}
			if tc.armed && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
				t.Fatalf("acquisition context ended with %v; want its deadline crossed", ctx.Err())
			}
			if setup := o.nextPhysical(t); setup.Done() != nil || len(bounded) != 0 {
				t.Errorf("setup context bounded %t, statements run under a bounded context %q; want GetSpawn's unbounded context for all", setup.Done() != nil, bounded)
			}
			want, legacy := s.GetSpawn(relayID)
			if !tc.locked {
				if err != nil || !queried || !reflect.DeepEqual(got, want) || legacy != nil {
					t.Fatalf("GetSpawnConnBounded = %+v, queried %t, %v; want GetSpawn's %+v, %v, queried", got, queried, err, want, legacy)
				}
				return
			}
			if legacy == nil || !strings.Contains(legacy.Error(), "database is locked") {
				t.Fatalf("GetSpawn under the lock = %v; want SQLite's busy error", legacy)
			}
			if !queried || err == nil || err.Error() != legacy.Error() {
				t.Errorf("GetSpawnConnBounded = queried %t, %v; want GetSpawn's %v, queried", queried, err, legacy)
			}
			if elapsed < 100*time.Millisecond {
				t.Errorf("GetSpawnConnBounded failed after %v; want the store's whole 137 ms busy timeout", elapsed)
			}
		})
	}
}

// armedDeadline is a context with no deadline until arm, which gives it a real
// one d from then: a timer ends it, and Err is then context.DeadlineExceeded.
// A test so crosses the deadline after a barrier, never before the call
// reaches it, however the call is scheduled (TF11).
type armedDeadline struct {
	context.Context
	cancel   context.CancelFunc
	deadline atomic.Pointer[time.Time]
}

func newArmedDeadline() *armedDeadline {
	ctx, cancel := context.WithCancel(context.Background())
	return &armedDeadline{Context: ctx, cancel: cancel}
}

// arm sets the deadline d from now and ends the context then.
func (c *armedDeadline) arm(d time.Duration) {
	at := time.Now().Add(d)
	c.deadline.Store(&at)
	time.AfterFunc(d, c.cancel)
}

func (c *armedDeadline) Deadline() (time.Time, bool) {
	if at := c.deadline.Load(); at != nil {
		return *at, true
	}
	return time.Time{}, false
}

func (c *armedDeadline) Err() error {
	switch {
	case c.Context.Err() == nil:
		return nil
	case c.deadline.Load() != nil:
		return context.DeadlineExceeded
	}
	return context.Canceled
}
