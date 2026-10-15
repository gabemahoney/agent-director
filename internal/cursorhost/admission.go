package cursorhost

import (
	"sync"
	"sync/atomic"
	"time"
)

// promptAdmission reserves one idle turn at Enter consumption. During a waiting
// report, consumption verifies the actual commit rather than waiting for the
// report to finish and turning earlier pending/busy input into a future turn.
const admissionCheckWait = 100 * time.Millisecond

type promptAdmission struct {
	mu       sync.Mutex
	ready    atomic.Bool
	check    func() bool
	claimed  bool
	queued   bool
	validate func() bool
	checking atomic.Bool
}

func (a *promptAdmission) take() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.queued && a.ready.Load() {
		return a.validate == nil || a.verify(a.validate)
	}
	if a.ready.CompareAndSwap(true, false) {
		return true
	}
	if a.check == nil || a.claimed || !a.verify(a.check) {
		return false
	}
	a.claimed = true
	return true
}

// A slow authority read must not hold the terminal behind ordinary input.
// At most one check can outlive its consumption deadline; its late result is
// discarded rather than granting eligibility to later input.
func (a *promptAdmission) verify(check func() bool) bool {
	if !a.checking.CompareAndSwap(false, true) {
		return false
	}
	result := make(chan bool, 1)
	go func() { defer a.checking.Store(false); result <- check() }()
	timer := time.NewTimer(admissionCheckWait)
	defer timer.Stop()
	select {
	case ok := <-result:
		return ok
	case <-timer.C:
		return false
	}
}

func (a *promptAdmission) waiting(check func() bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ready.Store(false)
	a.check = check
	a.claimed = false
}

func (a *promptAdmission) confirm() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.check = nil
	a.ready.Store(!a.claimed)
}

func (a *promptAdmission) disable() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ready.Store(false)
	a.check = nil
}

// enableQueue retains the consumption-time startup reservation, then allows
// complete follow-ups while a turn is running. Capacity belongs to inputMailbox.
func (a *promptAdmission) enableQueue() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.queued = true
	a.check = nil
	a.ready.Store(true)
}
