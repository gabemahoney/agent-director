//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// steerStallBudget is the short context a stalled steer runs under, in place
// of the route's one-minute Job.
const steerStallBudget = 2 * time.Second

// TestHookStdoutBoundsAStalledSteer (SR-3.2; Subtask t3.xis.wm.hg.k1): the
// stdout hookStdout makes for a PostToolBatch hook from a blocking fd 1 on a
// real pipe takes the steer route's write deadline. With the reader open and
// never reading, the envelope fills the one-page pipe, and Handle returns when
// a short injected context ends, having written a proper prefix of the
// envelope; no writer outlives it, so once fd 1 and the test's write end close
// the reader reaches EOF. Linux only: the hook reads /proc and the pipe is
// shrunk with F_SETPIPE_SZ.
func TestHookStdoutBoundsAStalledSteer(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	const id, session = "id-steer-stall", "3a5c7e9b-1d2f-4b6a-8c0e-2f4a6c8e0b1d"
	db := filepath.Join(home, "state.db")
	pid := os.Getpid() // the row's pane process and the hook's parent
	start, alive, known := probe.NewProcChecker().StartTime(pid)
	if !known || !alive || start == "" {
		t.Fatalf("StartTime(test pid %d) = (%q, alive=%v, known=%v); want the live start time", pid, start, alive, known)
	}
	if _, err := apitest.SeedSpawn(db, id, store.StateWorking, "", "off", session, true, apitest.WithLaunchIdentity(store.LaunchIdentity{
		Token: "5eed0000000000d4", Socket: apitest.TestSocket, PaneID: apitest.TestPaneID, PanePID: pid, PaneStarttime: start})); err != nil {
		t.Fatalf("SeedSpawn: %v", err)
	}
	if err := apitest.SeedBusySendCoverage(db, id, store.BusySendCovered, store.BusySendCovered); err != nil {
		t.Fatal(err)
	}
	if err := apitest.SeedBusySendRoute(db, id, string(store.BusySendRouteClaudeHooks), filepath.Join(home, "busy-send-wake")); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	steer, body := store.BusySendSteerTurn, strings.Repeat("s", 7_900)
	if _, err := st.AdmitBusySend(store.BusySendAdmissionRequest{InstanceID: id, Text: []byte(body), SendMode: &steer}); err != nil {
		t.Fatalf("AdmitBusySend: %v", err)
	}
	want, err := json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": "PostToolBatch",
		"additionalContext": "[agent-director: message 1 of 1]\n" + body}})
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	payload := `{"hook_event_name":"PostToolBatch","session_id":"` + session + `","transcript_path":"/x/` + session + `.jsonl","cwd":"/x"}`
	hc := hook.HandleConfig{
		Env: func(k string) string {
			if k == "AGENT_DIRECTOR_INSTANCE_ID" {
				return id
			}
			return ""
		},
		ParentPID:           os.Getpid,
		ParentProc:          hookParentProc(),
		Now:                 time.Now,
		Start:               time.Now(),
		Self:                hookSelf(probe.NewProcChecker()),
		ClaudeStartupPrompt: spawn.HasClaudeStartupPrompt,
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	wfd := int(w.Fd()) // Fd leaves the write end blocking, as a hook inherits it.
	if size, err := unix.FcntlInt(uintptr(wfd), unix.F_SETPIPE_SZ, 4096); err != nil || size >= len(want) {
		t.Skipf("pipe size %d, %v: cannot hold the write below the envelope's size", size, err)
	}
	elapsed, handled, stalled, err := onHookStdout(wfd, r, func(ctx context.Context, out io.Writer) error {
		return hook.Handle(ctx, strings.NewReader(payload), out, st, hc, log.New(io.Discard, "", 0))
	})
	if err != nil {
		t.Fatalf("replace fd 1: %v", err)
	}
	if stalled {
		t.Fatalf("Handle still writing %s after its %s context began; want the write cut at that deadline", elapsed, steerStallBudget)
	}
	if handled != nil || elapsed < steerStallBudget || elapsed > steerStallBudget+5*time.Second {
		t.Errorf("Handle = %v after %s; want nil once its %s context ended, not sooner", handled, elapsed, steerStallBudget)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := r.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	if err != nil || len(got) == 0 || len(got) >= len(want) || !bytes.HasPrefix(want, got) {
		t.Errorf("the pipe held %d bytes (%v), %.80q…; want a proper prefix of the %d-byte envelope, then EOF with no writer left",
			len(got), err, got, len(want))
	}
}

// onHookStdout runs run under a context of steerStallBudget with fd 1 replaced
// by wfd and run's output the stream hookStdout makes there for PostToolBatch,
// and returns how long run took and its error. A watchdog closes r at the
// budget plus 10 s, so a write the deadline does not cut fails the test
// instead of hanging it (stalled). It restores fd 1, which the test log
// writes to, so it logs nothing itself, and the SIGPIPE handling hookStdout
// changed, so its caller must not run in parallel.
func onHookStdout(wfd int, r *os.File, run func(context.Context, io.Writer) error) (elapsed time.Duration, runErr error, stalled bool, err error) {
	saved, err := unix.Dup(1)
	if err != nil {
		return 0, nil, false, err
	}
	if err := unix.Dup3(wfd, 1, 0); err != nil {
		_ = unix.Close(saved)
		return 0, nil, false, err
	}
	out := hookStdout(hook.EventNamePostToolBatch, os.Stdout)
	defer func() {
		if f, ok := out.(*os.File); ok && f != os.Stdout {
			_ = f.Close() // fd 1, before its finalizer could close the restored one
		}
		_ = unix.Dup3(saved, 1, 0)
		_ = unix.Close(saved)
		// signal.Ignore is undone by Notify; Stop then leaves Go's default.
		c := make(chan os.Signal, 1)
		signal.Notify(c, syscall.SIGPIPE)
		signal.Stop(c)
	}()
	began := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), steerStallBudget)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, out) }()
	select {
	case runErr = <-done:
	case <-time.After(steerStallBudget + 10*time.Second):
		_ = r.Close() // the stalled write ends with EPIPE, which the hook's stdout ignores
		stalled, runErr = true, <-done
	}
	return time.Since(began), runErr, stalled, nil
}
