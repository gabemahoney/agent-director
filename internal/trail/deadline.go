package trail

import (
	"context"
	"errors"
	"fmt"
	"maps"
)

var (
	// ErrPending means the event was accepted, but its write and fsync have
	// not been confirmed to this caller. The worker still owns the event.
	ErrPending = errors.New("trail: delivery pending")
	// ErrQueueFull means the event was not accepted. DeliveryStatus records
	// every rejection; an error must never imply that this event was durable.
	ErrQueueFull = errors.New("trail: pending event capacity reached")
	ErrClosed    = errors.New("trail: writer closed")
)

const pendingCapacity = 64

// DeliveryStatus distinguishes queued/in-flight work, failed emission attempts
// and rejected events. Pending events are memory-resident, not crash durable.
// LastError describes the latest failure or rejection, not a delivery receipt.
type DeliveryStatus struct {
	Pending   int
	Failed    uint64
	Rejected  uint64
	LastError string
}

type emission struct {
	event  string
	fields map[string]any
	done   chan error
}

// emitWithin waits only on ctx. A single worker owns at most one active
// write and a fixed-size queue; cancellation never spawns replacement workers
// or drops accepted audit events. Write/Sync cannot be canceled portably.
func (w *Writer) emitWithin(ctx context.Context, event string, fields map[string]any) error {
	job := emission{event: event, fields: maps.Clone(fields), done: make(chan error, 1)}
	delete(job.fields, "tool_input")
	w.queueMu.Lock()
	if w.stopping {
		w.delivery.Rejected++
		w.delivery.LastError = ErrClosed.Error()
		w.rejectedEvent = event
		w.queueMu.Unlock()
		return ErrClosed
	}
	w.startWorkerLocked()
	select {
	case w.queue <- job:
		w.delivery.Pending++
	default:
		w.delivery.Rejected++
		w.delivery.LastError = ErrQueueFull.Error()
		w.rejectedEvent = event
		w.queueMu.Unlock()
		return ErrQueueFull
	}
	w.queueMu.Unlock()
	select {
	case err := <-job.done:
		return err
	case <-ctx.Done():
		select {
		case err := <-job.done:
			return err
		default:
		}
		return fmt.Errorf("%w: %s: %w", ErrPending, event, ctx.Err())
	}
}

func (w *Writer) startWorkerLocked() {
	if w.queue != nil {
		return
	}
	w.queue = make(chan emission, pendingCapacity)
	w.drained = make(chan struct{})
	go w.runWorker()
}

func (w *Writer) runWorker() {
	for job := range w.queue {
		err := w.emit(job.event, job.fields)
		w.queueMu.Lock()
		w.delivery.Pending--
		if err != nil {
			w.delivery.Failed++
			w.delivery.LastError = err.Error()
		}
		w.queueMu.Unlock()
		job.done <- err
		if err == nil {
			w.reportRejections()
		}
	}
	w.mu.Lock()
	w.closed = true
	if w.f != nil {
		if err := w.f.Close(); err != nil {
			w.queueMu.Lock()
			w.delivery.Failed++
			w.delivery.LastError = err.Error()
			w.queueMu.Unlock()
		}
		w.f = nil
	}
	w.mu.Unlock()
	close(w.drained)
}

// Rejections cannot wait on I/O in the submitting call. Retain the loss count
// immediately in Status, then attempt a durable meta record on this same
// worker when the writer becomes available again.
func (w *Writer) reportRejections() {
	w.queueMu.Lock()
	count := w.delivery.Rejected - w.reportedRejections
	event := w.rejectedEvent
	w.reportedRejections = w.delivery.Rejected
	w.queueMu.Unlock()
	if count == 0 || event == "" {
		return
	}
	if err := w.emit("ad.trail_meta.emit_failed", map[string]any{
		"original_event":  event,
		"error_class":     "admission_rejected",
		"rejected_events": count,
		"source":          "ad_trail_meta",
	}); err != nil {
		w.queueMu.Lock()
		w.delivery.Failed++
		w.delivery.LastError = err.Error()
		w.queueMu.Unlock()
	}
}

// Status is nonblocking with respect to file I/O and the synchronous writer
// mutex. It exposes failures even when a fail-open caller discards Emit's error.
func (w *Writer) Status() DeliveryStatus {
	w.queueMu.Lock()
	defer w.queueMu.Unlock()
	return w.delivery
}

// Close stops admission and drains accepted events in FIFO order, then closes
// the file and worker. If ctx expires, draining remains pending on the same
// worker; the caller may wait again. It never reports pending writes as durable.
func (w *Writer) Close(ctx context.Context) error {
	w.queueMu.Lock()
	w.startWorkerLocked()
	if !w.stopping {
		w.stopping = true
		close(w.queue)
	}
	drained := w.drained
	w.queueMu.Unlock()
	select {
	case <-drained:
		status := w.Status()
		if status.Failed != 0 || status.Rejected != 0 {
			return fmt.Errorf("trail: drain completed with failures: %s", status.LastError)
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("%w: close: %w", ErrPending, ctx.Err())
	}
}
