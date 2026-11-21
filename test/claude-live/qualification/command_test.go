package qualification

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"
)

// t3.xis.w3.an.yx: even a broken command seam cannot hold the producer beyond
// its context. Release it explicitly so this negative control leaks no worker.
func TestBackgroundCommandContextBoundsJoin(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	release := make(chan struct{})
	call := startCommand(ctx, func(context.Context) commandResult {
		<-release
		return commandResult{}
	})
	defer func() {
		close(release)
		select {
		case <-call.done:
		case <-time.After(time.Second):
			t.Error("test command did not exit after release")
		}
	}()
	started := time.Now()
	if got := call.wait(ctx).err; !errors.Is(got, context.DeadlineExceeded) {
		t.Fatalf("wait lost caller deadline: %v", got)
	}
	if got := call.stop(ctx); !errors.Is(got, context.DeadlineExceeded) {
		t.Fatalf("join lost caller deadline: %v", got)
	}
	if time.Since(started) >= time.Second {
		t.Error("join waited beyond cancelled context")
	}
}

// t3.xis.w3.an.yx repair 1: an early error has a live phase context. A
// noncooperative command must not extend cleanup to that parent's deadline.
func TestBackgroundCommandLiveParentCleanupBound(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	release, cancelled := make(chan struct{}), make(chan struct{})
	call := startCommand(ctx, func(ctx context.Context) commandResult {
		<-ctx.Done()
		close(cancelled)
		<-release // Remain outstanding even after the child was cancelled.
		return commandResult{err: ctx.Err()}
	})
	t.Cleanup(func() {
		close(release)
		select {
		case <-call.done:
		case <-time.After(time.Second):
			t.Error("noncooperative test command did not exit after release")
		}
	})
	started := time.Now()
	if err := call.stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cleanup did not time out its outstanding command: %v", err)
	}
	elapsed := time.Since(started)
	if ctx.Err() != nil || elapsed >= 3*time.Second {
		t.Errorf("cleanup waited %s for live parent deadline: %v", elapsed, ctx.Err())
	}
	select {
	case <-cancelled:
	default:
		t.Error("cleanup did not cancel its child context")
	}
	t.Logf("cleanup returned after %s with parent error %v", elapsed, ctx.Err())
}

func TestBackgroundCommandAbandonedResult(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	call := startCommand(ctx, func(context.Context) commandResult {
		runtime.Goexit()
		return commandResult{}
	})
	defer call.stop(ctx)
	if got := call.wait(ctx).err; got == nil || errors.Is(got, context.DeadlineExceeded) {
		t.Fatalf("abandoned command did not publish its failure: %v", got)
	}
}
