package store

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestProductionConnectorExplicitCallerContexts(t *testing.T) {
	for _, mode := range []string{"default_unbounded", "default_cancel_only", "zero_work", "zero_parent_deadline"} {
		t.Run(mode, func(t *testing.T) {
			s, path := newRelayRow(t, "on", StateWorking)
			insertRelay(t, s, relayReq(tokenA))
			o := installLazyPool(t, s, path)
			type key struct{}
			parent := context.WithValue(context.Background(), key{}, mode)
			ctx := parent
			cancel := func() {}
			if mode == "default_cancel_only" {
				ctx, cancel = context.WithCancel(parent)
			}
			if mode == "zero_parent_deadline" {
				ctx, cancel = context.WithTimeout(parent, 500*time.Millisecond)
			}
			defer cancel()
			var delayed atomic.Bool
			o.b.before = func(sqlCtx context.Context, q string) error {
				if sqlCtx.Value(key{}) != mode {
					t.Errorf("SQL lost explicit caller value: %s", q)
				}
				if strings.HasPrefix(q, "PRAGMA busy_timeout = ") && !delayed.Swap(true) {
					got, bounded := sqlCtx.Deadline()
					switch mode {
					case "default_unbounded", "default_cancel_only":
						if bounded {
							t.Errorf("default call acquired invented deadline: %v", got)
						}
					case "zero_work":
						if !bounded || time.Until(got) < 100*time.Millisecond || time.Until(got) > time.Second {
							t.Errorf("zero initialization got admission context: %v", got)
						}
					case "zero_parent_deadline":
						want, _ := ctx.Deadline()
						if !bounded || !got.Equal(want) {
							t.Errorf("zero call renewed parent deadline: %v want%v", got, want)
						}
					}
					time.Sleep(30 * time.Millisecond)
				}
				return nil
			}
			wait := DefaultLockWait
			if strings.HasPrefix(mode, "zero") {
				wait = 0
			}
			written, err := boundedDecision(s, ctx, wait)
			if err != nil || !written || !delayed.Load() {
				t.Fatalf("explicit %s decision=%v,%v", mode, written, err)
			}
			actual := o.nextPhysical(t)
			if actual.Value(key{}) != mode {
				t.Error("native initialization lost explicit caller")
			}
			if len(o.background) != 0 {
				t.Error("explicit caller incorrectly used background handoff")
			}
			o.b.stopObservingAfterCleanup(t, s)
			assertDeadlineDatabaseReusable(t, s, path, "allow")
		})
	}
}

func TestProductionLazyInitializationFailureIsNotLent(t *testing.T) {
	s, path := newRelayRow(t, "on", StateWorking)
	o := installLazyPool(t, s, path)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var initialized atomic.Int32
	o.b.before = func(_ context.Context, q string) error {
		if strings.HasPrefix(q, "PRAGMA ") {
			initialized.Add(1)
		}
		return nil
	}
	o.b.afterExec = func(_ context.Context, q string, err error) {
		if strings.HasPrefix(q, "PRAGMA busy_timeout = ") {
			if err != nil {
				t.Errorf("first initialization failed: %v", err)
			}
			cancel()
		}
	}
	// An unmarked cancelable caller gets the same unopened lease as the pool opener.
	conn, err := s.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if o.b.opens.Load() != 0 {
		t.Fatal("unmarked acquisition performed native I/O")
	}
	if err := conn.PingContext(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("partial initialization=%v", err)
	}
	_ = conn.Close()
	if initialized.Load() != 1 {
		t.Errorf("partial initialization statements=%d want1", initialized.Load())
	}
	// Pool acquisition serializes the failed connection's disposal before observer removal.
	recovery, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	fresh, err := s.db.Conn(recovery)
	if err != nil {
		t.Fatal(err)
	}
	o.b.before, o.b.afterExec = nil, nil
	requireLazySettings(t, fresh)
	_ = fresh.Close()
	if o.b.opens.Load() != 2 {
		t.Errorf("partially initialized connection reused: opens=%d", o.b.opens.Load())
	}
}
