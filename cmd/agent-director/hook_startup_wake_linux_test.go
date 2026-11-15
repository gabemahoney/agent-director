package main_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// Q5 / t2.xis.oo.67: SessionStart must leave a later wake for a Claude
// starting without a prompt. No send, Stop or Notification rescues this test.
// This uses only pre-fix symbols so it can reproduce the behavioral gap on 6ea0df4e.
func TestHookCLIStartupWakeAfterWatchPaths(t *testing.T) {
	h := newGateHome(t)
	const id, session = "id-startup-wake", "7d1e2f3a-4b5c-4d6e-8f70-91a2b3c4d5e6"
	h.seed(t, id, "off", withTestProcessPane(t))
	st := openDBForRead(t, h.home)
	launch, err := st.GetBusySendPublishedLaunch(id)
	if err != nil {
		t.Fatal(err)
	}
	wake := h.home + "/.config/agent-director/launch/" + st.StoreID() + "/" + launch.LaunchToken + "/" + store.BusySendWakeFile
	if err := apitest.SeedBusySendCoverage(stateDB(h.home), id, store.BusySendCovered, store.BusySendCovered); err != nil {
		t.Fatal(err)
	}
	if err := apitest.SeedBusySendRoute(stateDB(h.home), id, string(store.BusySendRouteClaudeHooks), wake); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AdmitBusySend(store.BusySendAdmissionRequest{InstanceID: id, Text: []byte("held during startup")}); err != nil {
		t.Fatal(err)
	}
	// TR1: register before launching the hook, so descheduling this process
	// cannot lose the helper's only touch. The preparer preserves this file
	// without changing its times; post-output ordering is tested with virtual time.
	if err := os.MkdirAll(filepath.Dir(wake), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wake, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if _, err := unix.InotifyAddWatch(fd, wake, unix.IN_ATTRIB); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), gateHookDeadline)
	defer cancel()
	cmd := h.hookCmd(ctx, t, id, "", `{"hook_event_name":"SessionStart","source":"startup","session_id":"`+session+`","transcript_path":"`+h.transcript(t, session)+`"}`)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	var out struct {
		HookSpecificOutput struct {
			HookEventName string
			WatchPaths    []string
		}
	}
	if err := json.NewDecoder(stdout).Decode(&out); err != nil {
		_ = cmd.Wait()
		t.Fatalf("watchPaths: %v; stderr %q", err, stderr.String())
	}
	if out.HookSpecificOutput.HookEventName != "SessionStart" || len(out.HookSpecificOutput.WatchPaths) != 1 || out.HookSpecificOutput.WatchPaths[0] != wake {
		t.Fatalf("SessionStart output = %+v; want its recorded watch path %q", out, wake)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("SessionStart: %v; stderr %q", err, stderr.String())
	}
	if n, err := unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}, 6000); err != nil || n != 1 {
		t.Fatalf("no wake after SessionStart watchPaths and hook exit within 6s: poll=%d, %v", n, err)
	}
	if info, err := os.Stat(wake); err != nil || info.Size() != 0 {
		t.Errorf("wake = %v, %v; want content-free hint", info, err)
	}
	if o, err := st.ReadBusySendObservation(id, time.Second); err != nil || o == nil || o.HeldCount != 1 || o.HandoverCount != 0 {
		t.Errorf("after startup hint = %+v, %v; want one Held Message and no hand-over", o, err)
	}
	assertNoTmuxCall(t, h.home)
}
