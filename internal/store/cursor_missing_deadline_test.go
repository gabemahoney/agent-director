package store_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
)

func TestCursorMissingNativeCommitDeadline(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "COMMIT entry held", true: "COMMIT response held"}[committed], func(t *testing.T) {
			e := newCursorTrailEnv(t)
			e.insert(t, "deadline-request")
			row, err := e.f.s.GetSpawn(e.b.InstanceID)
			if err != nil {
				t.Fatal(err)
			}
			before := e.read(t, "deadline-request")
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			timer := time.AfterFunc(3*time.Second, unblock)
			defer func() { unblock(); timer.Stop() }()
			var commits atomic.Int32
			block := func() { close(entered); <-release }
			store.ObserveDecisionBoundaryForTest(t, e.f.s, e.f.path, func(ctx context.Context, q string) error {
				if q == "COMMIT" {
					commits.Add(1)
					if !committed {
						block()
						return ctx.Err()
					}
				}
				return nil
			}, func(context.Context) error {
				if committed {
					block()
				}
				return nil
			})
			done := make(chan error, 1)
			start := time.Now()
			go func() { _, _, err := e.f.s.MarkCursorMissingIfSameLaunch(row, e.b); done <- err }()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("native COMMIT not reached")
			}
			select {
			case err = <-done:
				if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, store.ErrStoreBusy) || !strings.Contains(err.Error(), "branch=operation_guard") {
					t.Errorf("native terminal result=%v", err)
				}
				if !e.f.s.CanDeferCursorMissingWrite(err) {
					t.Errorf("write timeout cannot defer: %v", err)
				}
			case <-time.After(2*time.Second - time.Since(start)):
				t.Error("native terminal blocked beyond original 1s execution + 1s allowance")
				unblock()
				<-done
			}
			if time.Since(start) < time.Second {
				t.Error("COMMIT did not remain held through original execution budget")
			}
			unblock()
			// Readback has its own context, only after releasing native work; never replay.
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			after, err := e.f.s.WithContext(ctx).GetSpawnWithin(row.InstanceID, 2*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			request := e.read(t, "deadline-request")
			if committed {
				if after.State != store.StateMissing || after.RowVersion != row.RowVersion+1 || request.AwaitsAnswer() {
					t.Errorf("confirmed durable terminal facts: %+v %+v", after, request)
				}
			} else if after.State != row.State || after.RowVersion != row.RowVersion || request.Decision != before.Decision || !request.AwaitsAnswer() {
				t.Errorf("late COMMIT entry changed facts: %+v %+v", after, request)
			}
			if commits.Load() != 1 {
				t.Errorf("COMMIT attempts=%d; want exactly one", commits.Load())
			}
		})
	}
}
