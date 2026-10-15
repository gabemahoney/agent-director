package cursorhost

import (
	"sync"
	"sync/atomic"
)

// promptAdmission reserves one idle turn at Enter consumption. During a waiting
// report, consumption verifies the actual commit rather than waiting for the
// report to finish and turning earlier pending/busy input into a future turn.
type promptAdmission struct {
	mu      sync.Mutex
	ready   atomic.Bool
	check   func() bool
	claimed bool
}

func (a *promptAdmission) take() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ready.CompareAndSwap(true, false) {
		return true
	}
	if a.check == nil || a.claimed || !a.check() {
		return false
	}
	a.claimed = true
	return true
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
