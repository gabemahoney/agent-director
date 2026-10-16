package store_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
)

func TestCursorPromptLateCountCannotPublishEligibility(t *testing.T) {
	e := newCursorTrailEnv(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once, enter sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	timer := time.AfterFunc(3*time.Second, unblock)
	defer func() { unblock(); timer.Stop() }()
	var commits, counts atomic.Int32
	stop := store.ObservePromptRowsForTest(t, e.f.s, e.f.path, func(_ context.Context, q string) error {
		if q == "COMMIT" {
			commits.Add(1)
		}
		return nil
	}, func(_ context.Context, q string, err error) {
		if strings.Contains(q, "SELECT COUNT(*) FROM permission_requests") && err == nil {
			counts.Add(1)
			enter.Do(func() { close(entered); <-release })
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	type result struct {
		eligible bool
		err      error
	}
	done := make(chan result, 1)
	start := time.Now()
	go func() {
		ok, err := e.f.s.WithContext(ctx).CheckCursorPrompt(e.b, e.until, cursorTrailCheck)
		done <- result{ok, err}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("actual prompt COUNT row not reached")
	}
	select {
	case got := <-done:
		if got.eligible || !errors.Is(got.err, store.ErrStoreBusy) {
			t.Errorf("late prompt result=%+v", got)
		}
	case <-time.After(1500*time.Millisecond - time.Since(start)):
		t.Error("COUNT blocked original 500ms caller past 1.5s guard")
	}
	if !time.Now().Before(e.until) {
		t.Fatal("separate authority deadline expired; original-context boundary not isolated")
	}
	if commits.Load() != 0 {
		t.Error("late prompt initiated COMMIT while rows held")
	}
	unblock()
	stop()
	if commits.Load() != 0 {
		t.Error("late prompt initiated COMMIT after finite release")
	}
	if counts.Load() != 1 {
		t.Errorf("actual COUNT rows=%d", counts.Load())
	}
	if ok, err := e.f.s.CheckCursorPrompt(e.b, e.until, cursorTrailCheck); err != nil || !ok {
		t.Errorf("safe fresh authority verification=%v,%v", ok, err)
	}
}
