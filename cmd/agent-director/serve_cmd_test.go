package main

import (
	"syscall"
	"testing"
	"time"
)

// TestNewSignalCtxCancels proves the signal-aware ctx wired into `serve
// --stdio` cancels on SIGTERM and SIGINT, so the store's deferred Close (the
// SQLite WAL checkpoint) runs instead of the process dying outright.
func TestNewSignalCtxCancels(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT} {
		t.Run(sig.String(), func(t *testing.T) {
			ctx, stop := newSignalCtx()
			defer stop()
			if err := syscall.Kill(syscall.Getpid(), sig); err != nil {
				t.Fatalf("send %v to self: %v", sig, err)
			}
			select {
			case <-ctx.Done():
			case <-time.After(2 * time.Second):
				t.Fatalf("ctx not canceled within 2s of %v", sig)
			}
		})
	}
}
