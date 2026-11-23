package inputdelivery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/gabemahoney/agent-director/internal/inputjournal"
)

// ErrBusy means an observer deferred to existing input admission or proof work.
// It is not unreadable evidence and never grants input authority.
var ErrBusy = errors.New("input delivery: observation deferred by input admission")

// LockPolicy supplies bounded admission waits and their clock. Nil on Native
// selects production defaults; fixtures can shorten waits without sleeping.
type LockPolicy struct {
	GateTimeout  time.Duration
	ProofTimeout time.Duration
	Interval     time.Duration
	Now          func() time.Time
	Wait         func(context.Context, time.Duration) error
}

func (n Native) lockPolicy() LockPolicy {
	p := LockPolicy{GateTimeout: 100 * time.Millisecond, ProofTimeout: 2 * time.Second, Interval: 5 * time.Millisecond, Now: time.Now, Wait: waitInputLock}
	if n.Locks != nil {
		if n.Locks.GateTimeout > 0 {
			p.GateTimeout = n.Locks.GateTimeout
		}
		if n.Locks.ProofTimeout > 0 {
			p.ProofTimeout = n.Locks.ProofTimeout
		}
		if n.Locks.Interval > 0 {
			p.Interval = n.Locks.Interval
		}
		if n.Locks.Now != nil {
			p.Now = n.Locks.Now
		}
		if n.Locks.Wait != nil {
			p.Wait = n.Locks.Wait
		}
	}
	return p
}

func waitInputLock(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (p LockPolicy) acquire(ctx context.Context, f *os.File, budget time.Duration, reason string) error {
	end := p.Now().Add(budget)
	// The attempt bound also terminates a fixture whose clock fails to advance.
	var last error
	for remaining := budget; ; {
		if err := ctx.Err(); err != nil {
			return err
		}
		left := min(remaining, end.Sub(p.Now()))
		if last != nil && left <= 0 {
			return fmt.Errorf("%s for %s: %w", reason, budget, last)
		}
		err := inputjournal.LockWriter(f)
		if err == nil {
			return ctx.Err()
		}
		if !inputjournal.LockBusy(err) {
			return err
		}
		last = err
		left = min(remaining, end.Sub(p.Now()))
		if left <= 0 {
			return fmt.Errorf("%s for %s: %w", reason, budget, err)
		}
		pause := min(p.Interval, left)
		if err := p.Wait(ctx, pause); err != nil {
			return err
		}
		remaining -= pause
	}
}

func openInputLock(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, inputFileError(err)
	}
	if err == nil && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: input lock is not a regular file", ErrInvalidState)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	return f, inputFileError(err)
}

// File operations need their operation and cause, not private lock filenames.
// Keep the underlying kind available to errors.Is, including during cleanup.
func inputFileError(err error) error {
	if e, ok := err.(*os.PathError); ok {
		return fmt.Errorf("%s: %w", e.Op, e.Err)
	}
	return err
}

// openLocks gives a writer priority over future observers while it drains the
// one existing proof pass. Both lock inodes remain synchronization only.
func (n Native) openLocks(ctx context.Context, dir string) (lock, intent *os.File, resultErr error) {
	gate, err := openInputLock(dir + ".intent.lock")
	if err != nil {
		return nil, nil, fmt.Errorf("open input admission gate: %w", err)
	}
	defer func() {
		if resultErr != nil {
			if lock != nil {
				resultErr = errors.Join(resultErr, inputFileError(lock.Close()))
				lock = nil
			}
			resultErr = errors.Join(resultErr, inputFileError(gate.Close()))
		}
	}()
	policy := n.lockPolicy()
	if n.Guard.ProofOnly {
		if err = inputjournal.LockObserver(gate); err != nil {
			if inputjournal.LockBusy(err) {
				err = fmt.Errorf("%w: %w", ErrBusy, err)
			}
			return nil, nil, err
		}
	} else {
		if err = policy.acquire(ctx, gate, policy.GateTimeout, "another input writer is active or input admission stayed busy"); err != nil {
			return nil, nil, err
		}
	}
	lock, err = openInputLock(dir + ".lock")
	if err != nil {
		return nil, nil, fmt.Errorf("open input writer lock: %w", err)
	}
	if n.Guard.ProofOnly {
		err = inputjournal.LockWriter(lock)
		if err == nil {
			// Close the shared gate before loading/scanning proof. An announced
			// writer can now wait on our input lock and exclude later readers.
			err = inputFileError(gate.Close())
			if err == nil {
				return lock, nil, nil
			}
		} else if inputjournal.LockBusy(err) {
			err = fmt.Errorf("%w: %w", ErrBusy, err)
		}
	} else {
		err = policy.acquire(ctx, lock, policy.ProofTimeout, "input lock stayed busy")
		if err == nil {
			return lock, gate, nil
		}
	}
	return lock, nil, err
}
