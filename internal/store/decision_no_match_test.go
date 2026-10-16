package store

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A real guarded nonmatch must not enter the uncertain-COMMIT path.
func TestDecisionNoMatchRollsBackWithoutCommit(t *testing.T) {
	for _, expired := range []bool{false, true} {
		name := "timely refusal"
		if expired {
			name = "deadline at empty SQLite result"
		}
		t.Run(name, func(t *testing.T) {
			s, path := newRelayRow(t, "on", StateWorking)
			recordAwaitCase(t, s, path, awaitCase{legacy: true})
			before := requestDump(t, path)
			mark := len(readStoreTrailLines(t))
			b := installDeadlineBoundary(t, s, path)
			var begins, updates, emptyResults, rollbacks, commits atomic.Int32
			commitFailure := errors.New("fixture: unmatched decision must never COMMIT")
			b.before = func(_ context.Context, q string) error {
				switch q {
				case "BEGIN IMMEDIATE":
					begins.Add(1)
				case "ROLLBACK":
					rollbacks.Add(1)
				case "COMMIT":
					commits.Add(1)
					return commitFailure
				}
				if strings.Contains(q, "UPDATE permission_requests") {
					updates.Add(1)
				}
				return nil
			}
			b.afterNext = func(ctx context.Context, q string, err error) {
				if strings.Contains(q, "UPDATE permission_requests") && err == io.EOF {
					emptyResults.Add(1)
					if expired {
						<-ctx.Done()
					}
				}
			}
			budget := 5 * time.Second
			if expired {
				budget = 150 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), budget)
			defer cancel()
			start := time.Now()
			written, err := s.WithContext(ctx).DecideRelayRequest(relayID, tokenA, "allow", "", WriterProcessDecide, time.Now().Add(time.Hour), budget)
			if written || expired && !errors.Is(err, ErrStoreBusy) || !expired && err != nil {
				t.Errorf("no-match decision=%v, %v; want false, busy=%v", written, err, expired)
			}
			if elapsed := time.Since(start); elapsed > budget+time.Second {
				t.Errorf("no-match exceeded original budget allowance: %v", elapsed)
			}
			b.stopObservingAfterCleanup(t, s)
			if begins.Load() != 1 || updates.Load() != 1 || emptyResults.Load() != 1 || commits.Load() != 0 {
				t.Errorf("BEGIN/UPDATE/empty/COMMIT=%d/%d/%d/%d; want 1/1/1/0", begins.Load(), updates.Load(), emptyResults.Load(), commits.Load())
			}
			if !expired && (rollbacks.Load() != 1 || b.opens.Load() != 1) {
				t.Errorf("timely cleanup ROLLBACK/opens=%d/%d; want 1/1", rollbacks.Load(), b.opens.Load())
			}
			if expired && b.opens.Load() != 2 {
				t.Errorf("expired lease reused: opens=%d; want replacement", b.opens.Load())
			}
			assertDeadlineDatabaseReusable(t, s, path, "")
			if !reflect.DeepEqual(requestDump(t, path), before) {
				t.Error("no-match changed durable request, including decision/attempt fields")
			}
			if got := trailEventsSince(t, mark, "ad.row_mutation.committed"); len(got) != 0 {
				t.Errorf("no-match emitted committed mutations: %v", got)
			}
		})
	}
}
