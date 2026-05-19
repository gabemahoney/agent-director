package main

import (
	"syscall"
	"testing"
	"time"
)

// TestNewSignalCtxCancelsOnSIGTERM proves the signal-aware ctx wired
// into `serve --stdio` actually cancels on SIGTERM — the on-disk SQLite
// WAL must get a chance to checkpoint via deferred Close before the
// process exits. Without this wiring, SIGTERM kills the process outright
// and defers never run (the bug this test pins).
func TestNewSignalCtxCancelsOnSIGTERM(t *testing.T) {
	ctx, stop := newSignalCtx()
	defer stop()

	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM to self: %v", err)
	}

	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("ctx not canceled within 2s of SIGTERM")
	}
}

// TestNewSignalCtxCancelsOnSIGINT mirrors the SIGTERM case for the
// interactive (ctrl-C) shutdown path.
func TestNewSignalCtxCancelsOnSIGINT(t *testing.T) {
	ctx, stop := newSignalCtx()
	defer stop()

	if err := syscall.Kill(syscall.Getpid(), syscall.SIGINT); err != nil {
		t.Fatalf("send SIGINT to self: %v", err)
	}

	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("ctx not canceled within 2s of SIGINT")
	}
}
