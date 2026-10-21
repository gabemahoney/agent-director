package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"sync"
	"time"
)

type connectionCleanup struct {
	conn          *sql.Conn
	busyTimeoutMs int
	rollback      bool
	restore       bool
	discard       bool
	operation     *connectionOperation
}

type connectionCleanupJob struct {
	ctx     context.Context
	cleanup *connectionCleanup
	done    chan struct{}
}

// Each pool has one connection. Cleanup keeps that connection leased, so
// another operation cannot create additional cleanup work until it finishes.
// The lane has at most one worker, exits when drained, and is shared by all
// context-scoped handles. No worker is created per timeout or SQL statement.
type connectionCleanupLane struct {
	mu            sync.Mutex
	running       bool
	jobs          []connectionCleanupJob
	operationOnce sync.Once
	operationGate chan struct{}
	// hookAfterOperationForTest is a test seam, nil in production: when set,
	// each runConnectionOperation worker calls it last, after it has freed
	// the operation slot and released its caller, so a test can hold the
	// worker there and check from the caller that the slot is already free
	// (b.q5r). Set only by this package's tests, on their own Store and
	// before the call.
	hookAfterOperationForTest func()
}

// Admission precedes goroutine creation, including connection opening. A
// canceled native call retains this slot and its lease through disposal.
// Repeated callers cannot accumulate workers behind an unresponsive driver.
type connectionOperation struct {
	mu            sync.Mutex
	commitStarted bool
	abandoned     bool
	completed     bool
	err           error
}

func (s *Store) runConnectionOperation(ctx context.Context, maxWait time.Duration, fn func(*connectionOperation) error) error {
	lane := s.cleanup
	lane.operationOnce.Do(func() { lane.operationGate = make(chan struct{}, 1) })
	if err := lane.admitOperation(ctx, maxWait); err != nil {
		return fmt.Errorf("%w: connection operation: %w", ErrStoreBusy, err)
	}
	if err := ctx.Err(); err != nil {
		<-lane.operationGate
		return fmt.Errorf("%w: connection operation: %w", ErrStoreBusy, err)
	}
	op := &connectionOperation{}
	done := make(chan struct{})
	// Test seam (connectionCleanupLane.hookAfterOperationForTest; nil in
	// production), read by the caller so a test may set it between calls.
	afterForTest := lane.hookAfterOperationForTest
	go func() {
		err := fn(op)
		op.mu.Lock()
		op.err, op.completed = err, true
		op.mu.Unlock()
		// fn has disposed of its connection. Free the slot before closing
		// done, so a caller released by done finds the slot free on its next
		// call; a caller released by its deadline (returnCtx below) may return
		// while the slot is still held.
		<-lane.operationGate
		close(done)
		if afterForTest != nil {
			afterForTest()
		}
	}()
	// Cancellation stops SQL immediately through ctx. When an absolute bound
	// exists, allow a cooperative driver to publish its actual error within
	// that same bound instead of racing cancellation against a returned I/O
	// error. This never gives native work a later deadline or an extra reserve.
	returnCtx := ctx
	if deadline, bounded := ctx.Deadline(); bounded {
		var cancel context.CancelFunc
		returnCtx, cancel = context.WithDeadline(context.WithoutCancel(ctx), deadline)
		defer cancel()
	}
	select {
	case <-done:
		return op.err
	case <-returnCtx.Done():
		op.mu.Lock()
		defer op.mu.Unlock()
		if op.completed {
			return op.err
		}
		op.abandoned = true
		cause := ctx.Err()
		if cause == nil {
			cause = returnCtx.Err()
		}
		if op.commitStarted {
			return fmt.Errorf("%w (branch=operation_guard): %w", errCommitUnconfirmed, cause)
		}
		return fmt.Errorf("%w: connection operation: %w", ErrStoreBusy, cause)
	}
}

// admitOperation takes the operation slot. A free slot is taken however late
// the call reaches it. A held one is waited for until ctx ends or, with zero
// wait, until it has stayed held for connGrab since the call found it held.
// Zero-wait admission to the operation slot must use it; never pre-arm a
// connGrab timeout.
func (lane *connectionCleanupLane) admitOperation(ctx context.Context, maxWait time.Duration) error {
	select {
	case lane.operationGate <- struct{}{}:
		return nil
	default:
	}
	var grab <-chan time.Time
	if maxWait == 0 {
		timer := time.NewTimer(connGrab)
		defer timer.Stop()
		grab = timer.C
	}
	select {
	case lane.operationGate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-grab:
	}
	// A select picks at random among ready cases: look once more, so a slot
	// freed as connGrab ran out is still taken.
	select {
	case lane.operationGate <- struct{}{}:
		return nil
	default:
		return context.DeadlineExceeded
	}
}

func (s *Store) releaseConn(ctx context.Context, cleanup *connectionCleanup) {
	job := connectionCleanupJob{ctx: ctx, cleanup: cleanup, done: make(chan struct{})}
	lane := s.cleanup
	lane.mu.Lock()
	lane.jobs = append(lane.jobs, job)
	if !lane.running {
		lane.running = true
		go lane.run()
	}
	lane.mu.Unlock()
	select {
	case <-job.done:
	case <-ctx.Done():
		// Still leased: neither the transaction nor the shorter PRAGMA can
		// escape to another caller while noncancelable disposal finishes.
	}
}

func (lane *connectionCleanupLane) run() {
	for {
		lane.mu.Lock()
		if len(lane.jobs) == 0 {
			lane.running = false
			lane.mu.Unlock()
			return
		}
		job := lane.jobs[0]
		lane.jobs[0] = connectionCleanupJob{}
		lane.jobs = lane.jobs[1:]
		lane.mu.Unlock()
		job.cleanup.finish(job.ctx)
		close(job.done)
	}
}

func (c *connectionCleanup) finish(ctx context.Context) {
	if ctx.Err() != nil {
		c.discard = true
	}
	if c.rollback && !c.discard {
		if _, err := c.conn.ExecContext(ctx, "ROLLBACK"); err != nil {
			c.discard = true
		}
	}
	if c.restore && !c.discard {
		if _, err := c.conn.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout = %d", c.busyTimeoutMs)); err != nil {
			c.discard = true
		}
	}
	if c.discard || ctx.Err() != nil {
		_ = c.conn.Raw(func(any) error { return driver.ErrBadConn })
	}
	_ = c.conn.Close()
}
