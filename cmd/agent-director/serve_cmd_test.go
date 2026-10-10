package main

import (
	"syscall"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/clisetup"
	pkgapi "github.com/gabemahoney/agent-director/pkg/api"
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

// TestMCPClientOptions: serve's MCP Client opens with run()'s --store-path and
// --tmux-command overrides, creates a missing store and has no logger (b.wb7, Pin H4).
func TestMCPClientOptions(t *testing.T) {
	for _, o := range []clisetup.Overrides{{}, {StorePath: "/stores/s.db", TmuxCommand: "/opt/bin/other-tmux"}} {
		want := pkgapi.Options{ConfigPath: clisetup.ConfigPath, CreateIfMissing: true,
			StorePath: o.StorePath, TmuxCommand: o.TmuxCommand}
		if got := mcpClientOptions(o); got != want {
			t.Errorf("mcpClientOptions(%+v) = %+v; want %+v", o, got, want)
		}
	}
}
