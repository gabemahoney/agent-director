package trail

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"
)

type deadlineEncodingGate struct{ entered, release chan struct{} }

func (g *deadlineEncodingGate) MarshalJSON() ([]byte, error) {
	close(g.entered)
	<-g.release
	return []byte(`"released"`), nil
}

func TestBoundedWriterPendingCapacityFIFOAndDrain(t *testing.T) {
	w, path, _ := newTestWriter(t)
	g := &deadlineEncodingGate{make(chan struct{}), make(chan struct{})}
	var once sync.Once
	release := func() { once.Do(func() { close(g.release) }) }
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := make(chan error, 1)
	go func() { first <- w.Emit(ctx, "ad.test", map[string]any{"index": 0, "gate": g}) }()
	select {
	case <-g.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not enter real writer")
	}
	cancel()
	select {
	case err := <-first:
		if !errors.Is(err, ErrPending) {
			t.Fatalf("accepted blocked event: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled caller blocked on writer")
	}
	for i := 1; i <= 64; i++ {
		if err := w.Emit(ctx, "ad.test", map[string]any{"index": i, "tool_input": "secret"}); !errors.Is(err, ErrPending) {
			t.Fatalf("accepted event %d: %v", i, err)
		}
	}
	if err := w.Emit(ctx, "ad.test.rejected", nil); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("saturated admission: %v", err)
	}
	if got := w.Status(); got.Pending != 65 || got.Rejected != 1 || got.Failed != 0 || got.LastError != ErrQueueFull.Error() {
		t.Errorf("blocked status=%+v", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("pending encoding claimed physical output: %v", err)
	}
	if err := w.Close(ctx); !errors.Is(err, ErrPending) {
		t.Errorf("blocked drain: %v", err)
	}
	if err := w.Emit(ctx, "ad.test.closed", nil); !errors.Is(err, ErrClosed) {
		t.Errorf("closed admission: %v", err)
	}
	release()
	drain, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	if err := w.Close(drain); err == nil || errors.Is(err, ErrPending) {
		t.Errorf("drain must expose rejected events: %v", err)
	}
	rows := readLines(t, path)
	next, rejected := 0, 0
	for _, row := range rows {
		if _, found := row["tool_input"]; found {
			t.Error("queued tool_input leaked")
		}
		switch row["event"] {
		case "ad.test":
			if row["index"] != float64(next) {
				t.Errorf("FIFO index=%v; want %d", row["index"], next)
			}
			next++
		case "ad.trail_meta.emit_failed":
			if row["error_class"] != "admission_rejected" {
				t.Errorf("rejection mislabeled: %v", row)
			}
			rejected += int(row["rejected_events"].(float64))
		default:
			t.Errorf("rejected event was written: %v", row)
		}
	}
	if next != 65 || rejected != 2 {
		t.Errorf("drained accepted=%d, rejection meta=%d; want 65, 2", next, rejected)
	}
	if got := w.Status(); got.Pending != 0 || got.Rejected != 2 || got.Failed != 0 {
		t.Errorf("drained status=%+v", got)
	}
	select {
	case <-w.drained:
	default:
		t.Error("Close returned before worker exited")
	}
	if err := w.Emit(context.Background(), "ad.test.legacy", nil); !errors.Is(err, ErrClosed) {
		t.Errorf("legacy write after worker close: %v", err)
	}
}

func TestBoundedWriterFailureIsObservableAndCloses(t *testing.T) {
	w, path, _ := newTestWriter(t)
	// A directory at the actual output path makes the real open fail deterministically.
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := w.Emit(ctx, "ad.test.failure", nil); err == nil || errors.Is(err, ErrPending) {
		t.Fatalf("write failure=%v", err)
	}
	if got := w.Status(); got.Pending != 0 || got.Failed != 1 || got.LastError == "" {
		t.Errorf("failure status=%+v", got)
	}
	if err := w.Close(ctx); err == nil || errors.Is(err, ErrPending) {
		t.Errorf("failed drain=%v", err)
	}
}
