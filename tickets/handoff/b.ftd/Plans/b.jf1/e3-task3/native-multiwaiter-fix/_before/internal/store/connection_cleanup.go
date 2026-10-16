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
	admitCtx := ctx
	if maxWait == 0 {
		var cancel context.CancelFunc
		admitCtx, cancel = context.WithTimeout(ctx, connGrab)
		defer cancel()
	}
	select {
	case lane.operationGate <- struct{}{}:
	case <-admitCtx.Done():
		return fmt.Errorf("%w: connection operation: %w", ErrStoreBusy, admitCtx.Err())
	}
	if err := ctx.Err(); err != nil {
		<-lane.operationGate
		return fmt.Errorf("%w: connection operation: %w", ErrStoreBusy, err)
	}
	op := &connectionOperation{}
	done := make(chan struct{})
	go func() {
		defer func() { <-lane.operationGate }()
		err := fn(op)
		op.mu.Lock()
		op.err, op.completed = err, true
		op.mu.Unlock()
		close(done)
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
			return fmt.Errorf("store: commit outcome unconfirmed; read the request before retrying: %w", cause)
		}
		return fmt.Errorf("%w: connection operation: %w", ErrStoreBusy, cause)
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
