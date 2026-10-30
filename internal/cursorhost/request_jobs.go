package cursorhost

import (
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
)

const requestRetry = 50 * time.Millisecond

// Request time belongs to one controller. Replacements are installed before
// admission; callbacks and Now may run concurrently with guarded writes.
type requestClock interface {
	Now() time.Time
	AfterFunc(time.Duration, func()) requestTimer
}

type requestTimer interface{ Stop() bool }

type realRequestClock struct{}

func (realRequestClock) Now() time.Time { return time.Now() }
func (realRequestClock) AfterFunc(d time.Duration, fn func()) requestTimer {
	return time.AfterFunc(d, fn)
}

type requestWakeKind uint8

const (
	permissionWait requestWakeKind = iota
	proofWait
	responseCompleted
)

// A wake carries one admitted operation's original owner and phase. The
// sequence retires duplicate/already-dispatched timers, including same-session
// replacements. No wake enumerates the controller's retained requests.
type requestWake struct {
	storeID    string
	token      string
	binding    store.CursorRuntime
	generation uint64
	kind       requestWakeKind
	sequence   uint64
	deadline   time.Time
	proofID    string
	err        error
}
type requestWait struct {
	timer    requestTimer
	sequence uint64
	deadline time.Time
}

func (w *requestWait) stop() {
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
	w.sequence++
}
func (c *requestController) deliver(w requestWake) {
	select {
	case c.events <- w:
	case <-c.closed:
	}
}
func (c *requestController) close() {
	c.closeOnce.Do(func() {
		close(c.closed)
		c.cancel()
		for _, r := range c.requests {
			r.proofWait.stop()
		}
	})
}
func (c *requestController) scheduleRequest(r *pendingAuthorization, at time.Time) {
	r.wait.stop()
	if r.terminal.Load() || r.sent.Load() || r.generation != c.generation.Load() {
		return
	}
	end := r.nativeUntil
	if r.relay {
		end = r.settle
		if c.clock.Now().Before(r.timeout) {
			end = r.timeout
		}
	}
	if end.Before(at) {
		at = end
	}
	r.wait.deadline = end
	w := requestWake{storeID: r.storeID, token: r.token, binding: r.binding, generation: r.generation, kind: permissionWait, sequence: r.wait.sequence, deadline: end}
	r.wait.timer = c.clock.AfterFunc(max(at.Sub(c.clock.Now()), 0), func() { c.deliver(w) })
}
func (c *requestController) scheduleNextRequest(r *pendingAuthorization) {
	if r.terminal.Load() || r.sent.Load() || r.generation != c.generation.Load() {
		r.wait.stop()
		return
	}
	at := r.nativeUntil
	if r.relay {
		at = r.settle
		if !r.queued {
			at = c.clock.Now().Add(requestRetry)
		}
	} else if r.choice != nil && !r.queued {
		at = c.clock.Now().Add(requestRetry)
	}
	c.scheduleRequest(r, at)
}
func (c *requestController) scheduleProof(r *pendingAuthorization, p *deferredProof) {
	r.proofWait.stop()
	end := c.proofDeadline(*p)
	r.proofWait.deadline = end
	w := requestWake{storeID: r.storeID, token: r.token, binding: p.binding, generation: p.event.Generation, kind: proofWait, sequence: r.proofWait.sequence, deadline: end, proofID: p.event.ID}
	r.proofWait.timer = c.clock.AfterFunc(max(min(proofRetry, end.Sub(c.clock.Now())), 0), func() { c.deliver(w) })
}

// continueRequest returns true only for an actual prerequisite completion or
// phase change. A stale wake cannot restart prompt timing or promote input.
func (c *requestController) continueRequest(w requestWake) (bool, error) {
	r := c.requests[w.token]
	if r == nil || r.storeID != w.storeID || w.storeID != c.rep.store.StoreID() || r.binding != w.binding || w.binding != c.rep.binding {
		return false, nil
	}
	switch w.kind {
	case proofWait:
		p := r.proof
		if p == nil || p.event.Owner.StoreID != w.storeID || p.event.Token != w.token || p.binding != w.binding || p.event.ID != w.proofID || p.event.Generation != w.generation || r.proofWait.sequence != w.sequence || !w.deadline.Equal(p.event.BusyUntil) {
			return false, nil
		}
		r.proofWait.stop()
		err := c.prove(r)
		return err == nil && r.proof == nil, err
	case responseCompleted:
		if r.generation != w.generation || r.generation != c.generation.Load() || !r.queued || !r.sent.Load() && !r.terminal.Load() || r.responseDone || !r.responseDeadline.Equal(w.deadline) {
			return false, nil
		}
		r.responseDone = true
		r.wait.stop()
		return true, nil
	case permissionWait:
		if r.generation != w.generation || r.generation != c.generation.Load() || r.wait.sequence != w.sequence || !r.wait.deadline.Equal(w.deadline) || r.terminal.Load() || r.sent.Load() {
			return false, nil
		}
		r.wait.stop()
		wasRelay := r.relay
		err := c.advanceRequest(r)
		if err == nil {
			c.scheduleNextRequest(r)
		}
		return wasRelay != r.relay || r.terminal.Load() || r.sent.Load(), err
	}
	return false, nil
}
