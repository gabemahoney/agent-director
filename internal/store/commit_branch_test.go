package store

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// Both branches wrap errCommitUnconfirmed and their cause, and retain the
// inspection advice even when SQLite committed durably.
func TestDecisionUnconfirmedCommitBranch(t *testing.T) {
	for _, branch := range []string{"commit_exec", "operation_guard"} {
		t.Run(branch, func(t *testing.T) {
			s, path := newRelayRow(t, "on", StateWorking)
			insertRelay(t, s, relayReq(tokenA))
			b := installDeadlineBoundary(t, s, path)
			var ctx context.Context
			var cancel context.CancelFunc
			if branch == "commit_exec" {
				ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
			} else {
				ctx, cancel = context.WithCancel(context.Background())
			}
			reached, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			cause := context.DeadlineExceeded
			if branch == "operation_guard" {
				cause = context.Canceled
			}
			b.afterCommit = func(context.Context) error {
				close(reached)
				if branch == "operation_guard" {
					<-release // Keep the native COMMIT response held after the caller returns.
				}
				return cause
			}
			t.Cleanup(func() {
				cancel()
				unblock()
				select {
				case <-finished:
				case <-time.After(2 * time.Second):
					t.Fatal("decision caller did not finish after releasing COMMIT")
				}
				b.stopObservingAfterCleanup(t, s)
				select {
				case s.cleanup.operationGate <- struct{}{}:
					<-s.cleanup.operationGate
				case <-time.After(2 * time.Second):
					t.Fatal("decision worker did not relinquish operation admission")
				}
				assertDeadlineDatabaseReusable(t, s, path, "allow")
			})
			type outcome struct {
				written bool
				err     error
			}
			result := make(chan outcome, 1)
			go func() {
				defer close(finished)
				wait := 5 * time.Second
				if branch == "operation_guard" {
					wait = DefaultLockWait // Preserve cancellation without adding an absolute deadline.
				}
				written, err := boundedDecision(s, ctx, wait)
				result <- outcome{written, err}
			}()
			select {
			case <-reached:
			case <-time.After(2 * time.Second):
				t.Fatal("actual native COMMIT did not complete")
			}
			if branch == "operation_guard" {
				cancel()
			}
			var got outcome
			select {
			case got = <-result:
			case <-time.After(2 * time.Second):
				t.Fatal("decision did not return within its bound")
			}
			if branch == "commit_exec" && ctx.Err() != nil {
				t.Fatalf("outer context expired before worker published COMMIT error: %v", ctx.Err())
			}
			if got.written || !errors.Is(got.err, errCommitUnconfirmed) || !errors.Is(got.err, cause) || errors.Is(got.err, ErrStoreBusy) {
				t.Fatalf("decision=%v, %v; want errCommitUnconfirmed wrapping %v, never busy", got.written, got.err, cause)
			}
			advice := "store: commit outcome unconfirmed; read the request before retrying"
			if !strings.HasPrefix(got.err.Error(), advice) {
				t.Errorf("inspection advice changed: %v", got.err)
			}
			if !strings.Contains(got.err.Error(), "(branch="+branch+")") {
				t.Errorf("missing distinct branch=%s marker: %v", branch, got.err)
			}
			opposite := "operation_guard"
			if branch == opposite {
				opposite = "commit_exec"
			}
			if strings.Contains(got.err.Error(), "(branch="+opposite+")") {
				t.Errorf("incorrect opposite branch marker: %v", got.err)
			}
		})
	}
}
