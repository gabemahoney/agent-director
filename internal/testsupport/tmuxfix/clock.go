package tmuxfix

import (
	"sync"
	"time"
)

// Clock is the shared test clock (SRD SR-20.3, Appendix F.5): verbs under
// test read it (c.Now as their clock) and the Recorder advances it in
// virtual-time mode, so ceilings and budgets are tested without waiting.
// It is safe for concurrent use.
type Clock struct {
	mu  sync.Mutex
	now time.Time
}

// NewClock returns a Clock that reads start until it is advanced (Appendix
// F.5).
func NewClock(start time.Time) *Clock {
	return &Clock{now: start}
}

// Now returns the clock's current instant. The method value c.Now is a
// func() time.Time, usable as a verb's clock.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the clock by d; a negative d moves it back. The method value
// c.Advance is a func(time.Duration), usable as a verb's sleep.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}
