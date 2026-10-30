package cursorhost

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
)

type proofFlushKind string

const (
	proofWaiting     proofFlushKind = "waiting"
	proofRotation    proofFlushKind = "rotation"
	proofExit        proofFlushKind = "exit"
	proofFatal       proofFlushKind = "fatal"
	proofRuntimeGone proofFlushKind = "runtime_gone"
)

type proofFlushEvent struct {
	kind  proofFlushKind
	epoch uint64
}

// Every instance, including repeated events of the same kind/epoch, gets its
// own sequence. Workers own only immutable evidence and their completion bits.
type proofFlush struct {
	callbackMu           sync.Mutex
	c                    *requestController
	binding              store.CursorRuntime
	generation, sequence uint64
	event                proofFlushEvent
	proofs               []flushedProof
	turnOpen             bool
	ctx                  context.Context
	cancel               context.CancelFunc
}
type flushedProof struct {
	r           *pendingAuthorization
	token, tool string
	proof       deferredProof
	stored      bool
}

func (c *requestController) flushProofs(events ...proofFlushEvent) *proofFlush {
	event := proofFlushEvent{kind: proofWaiting}
	if len(events) > 0 {
		event = events[0]
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.nextFlush++
	f := &proofFlush{c: c, binding: c.rep.binding, generation: c.generation.Load(), sequence: c.nextFlush, event: event, turnOpen: c.isTurnOpen(), ctx: ctx, cancel: cancel}
	for _, r := range c.requests {
		if r.proof != nil && r.proof.binding == f.binding {
			r.proofWait.stop()
			r.proof.flush = f.sequence
			p := *r.proof
			p.event.Raw = append([]byte(nil), p.event.Raw...)
			f.proofs = append(f.proofs, flushedProof{r: r, token: r.token, tool: r.auth.toolID, proof: p})
		}
	}
	return f
}
func (f *proofFlush) abandon() {
	f.callbackMu.Lock()
	defer f.callbackMu.Unlock()
	f.cancel()
}

// Serialize registration with abandonment: a late report cannot reinstall an
// admission check after its observer has returned and disabled admission.
func (f *proofFlush) registerWaiting(waiting func(func() bool), check func() bool) {
	f.callbackMu.Lock()
	defer f.callbackMu.Unlock()
	if f.ctx.Err() == nil {
		waiting(check)
	}
}

// One immediate attempt is retained even for an expired snapshot. Only that
// proof's original window (and this event's earlier bound) allows retries.
func (f *proofFlush) write(until time.Time) error {
	switch f.event.kind {
	case proofWaiting, proofRotation, proofExit, proofFatal, proofRuntimeGone:
	default:
		return fmt.Errorf("Cursor proof flush event invalid")
	}
	var first error
	for i := range f.proofs {
		p := &f.proofs[i]
		if err := p.proof.checkSnapshot(p.token, p.tool); err != nil {
			return err
		}
		if p.stored {
			continue
		}
		end := p.proof.event.BusyUntil
		if !until.IsZero() && until.Before(end) {
			end = until
		}
		attemptUntil := end
		if !time.Now().Before(end) {
			attemptUntil = time.Time{} // one immediate attempt for an expired snapshot
		}
		for {
			if err := f.ctx.Err(); err != nil {
				return err
			}
			if !attemptUntil.IsZero() && !time.Now().Before(attemptUntil) {
				if first == nil {
					first = errProofDeadline
				}
				break
			}
			authority := f.c.checkProof(p.proof.binding, attemptUntil)
			check := func(row store.Spawn) error {
				if err := f.ctx.Err(); err != nil {
					return err
				}
				if err := authority(row); err != nil {
					return err
				}
				return f.ctx.Err()
			}
			e := f.c.rep.store.WithContext(f.ctx).CommitCursorProof(p.proof.event, p.proof.event.TurnOpen && f.turnOpen, check)
			attemptUntil = end
			if e == nil {
				p.stored = true
				break
			}
			if !errors.Is(e, store.ErrStoreBusy) || !time.Now().Before(end) || f.event.kind == proofRuntimeGone {
				if first == nil {
					first = e
				}
				break
			}
			timer := time.NewTimer(min(proofRetry, time.Until(end)))
			select {
			case <-timer.C:
			case <-f.ctx.Done():
				timer.Stop()
				return f.ctx.Err()
			}
		}
	}
	return first
}

// Only the exact snapshot installed by this flush may be disposed. An older
// worker cannot clear a later proof or another flush of the same proof.
func (f *proofFlush) settle() {
	if f.ctx.Err() != nil || f.c.rep.binding != f.binding {
		return
	}
	for _, p := range f.proofs {
		current := p.r.proof
		if p.stored && current != nil && current.event.ID == p.proof.event.ID && current.binding == p.proof.binding && current.flush == f.sequence {
			p.r.lastProof = append([]byte(nil), p.proof.event.Raw...)
			p.r.proof = nil
			p.r.proofWait.stop()
		}
	}
}

type turnEnd struct {
	c        *requestController
	binding  store.CursorRuntime
	retired  []*pendingAuthorization
	tokens   []string
	sequence uint64
	deadline time.Time
	ctx      context.Context
	cancel   context.CancelFunc
}

func (c *requestController) endTurn() *turnEnd {
	c.nextFlush++
	ctx, cancel := context.WithCancel(context.Background())
	t := &turnEnd{c: c, binding: c.rep.binding, sequence: c.nextFlush, deadline: time.Now().Add(time.Duration(c.rep.cfg.Store.EffectiveBusyTimeoutMs()) * time.Millisecond), ctx: ctx, cancel: cancel}
	for _, r := range c.requests {
		if r.binding == t.binding && !r.turnEnded && !r.queued && !r.sent.Load() && r.proof == nil {
			r.turnEndSequence = t.sequence
			t.retired = append(t.retired, r)
			t.tokens = append(t.tokens, r.token)
		}
	}
	return t
}
func (t *turnEnd) abandon() { t.cancel() }
func (t *turnEnd) write(until time.Time) error {
	if err := t.ctx.Err(); err != nil {
		return err
	}
	end := t.deadline
	if !until.IsZero() && until.Before(end) {
		end = until
	}
	check := func(row store.Spawn) error {
		if err := t.ctx.Err(); err != nil {
			return err
		}
		if err := t.c.checkFor(t.binding)(row); err != nil {
			return err
		}
		return t.ctx.Err()
	}
	return t.c.rep.store.WithContext(t.ctx).ProveCursorTurnEnded(t.binding, t.tokens, end, check)
}
func (t *turnEnd) settle() {
	if t.ctx.Err() != nil || t.c.rep.binding != t.binding {
		return
	}
	for _, r := range t.retired {
		if r.turnEndSequence != t.sequence || r.binding != t.binding {
			continue
		}
		r.turnEnded = true
		r.wait.stop()
		r.terminal.Store(true)
		r.retired.Store(true)
		t.c.removeKeys(r.token)
	}
}
