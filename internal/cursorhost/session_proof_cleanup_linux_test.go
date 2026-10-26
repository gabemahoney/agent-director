//go:build linux

package cursorhost

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// longBusyCursorSession starts a ready Run whose store busy timeout is 60 s,
// so a flush bounded only by a proof's busy window outlasts every limit below.
func longBusyCursorSession(t *testing.T, opener func(string, store.CursorRuntime) (*transcript, error)) *cursorSession {
	t.Helper()
	c := beginConfiguredCursorSession(t, Options{}, cursorfix.Scenario{SessionID: "synthetic-session-1", Requests: []json.RawMessage{}}, opener, terminalLoop, func(*requestEnv) {
		dir := filepath.Join(os.Getenv("HOME"), ".config", "agent-director")
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[store]\nbusy_timeout_ms = 60000\n"), 0600); err != nil {
			t.Fatal(err)
		}
	})
	c.wait(t, "confirmed ready", func() bool { return bytes.Contains(c.pane.bytes(), []byte("Cursor ready")) })
	return c
}

// loopResult waits for the controller loop's result, observed before any
// deferred cleanup runs.
func loopResult(t *testing.T, c *cursorSession, limit time.Duration, failure string) error {
	t.Helper()
	until := time.Now().Add(limit)
	for {
		if seen, err := c.controllerResult(); seen {
			return err
		}
		if time.Now().After(until) {
			t.Fatalf("%s within %s; pane=%s", failure, limit, c.pane.bytes())
		}
		time.Sleep(time.Millisecond)
	}
}

// finishFailedRun releases the competing writer and waits for Run's failure.
func finishFailedRun(t *testing.T, c *cursorSession, release func()) {
	t.Helper()
	release()
	select {
	case err := <-c.done:
		c.ended, c.endErr = true, err
		if err == nil {
			t.Fatal("failed Run reported success")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run cleanup did not finish after the writer released")
	}
}

func killCursorRuntime(t *testing.T, c *cursorSession) {
	t.Helper()
	raw, err := os.ReadFile(c.e.runtime.PID)
	if err != nil {
		t.Fatal(err)
	}
	var pid int
	if _, err := fmt.Sscan(string(raw), &pid); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(3 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(until) {
			t.Fatal("runtime not reaped after SIGKILL")
		}
		time.Sleep(time.Millisecond)
	}
}

// b.o89 round 3: once the runtime has died after /exit, a deferred proof has
// no authority left to wait for. The exit flush makes one attempt instead of
// spinning out the busy window, and the failure names the lost proof.
func TestCursorExitAfterRuntimeDeathMakesOneFlushAttempt(t *testing.T) {
	// Serial: real Run, report subprocess and PTY with a synthetic ACP peer.
	barrier, opener := newMatchingRecordBarrier(t, "input_control", func(raw []byte) bool { return bytes.Contains(raw, []byte(`"control":"exit"`)) })
	idle, opener := idleProofBarrier(t, opener)
	c := longBusyCursorSession(t, opener)
	token, release := idleDeferredProof(t, c, idle)
	c.keys(t, "\x15/exit\r")
	select {
	case <-barrier.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("exit control record not reached")
	}
	// The held record keeps await from completing, so it selects the runtime's
	// exit. The tracer reports a reaped runtime within one 5 ms poll.
	killCursorRuntime(t, c)
	time.Sleep(250 * time.Millisecond)
	barrier.unblock()
	err := loopResult(t, c, 5*time.Second, "exit flush kept retrying after the runtime died")
	if err == nil || !strings.HasPrefix(err.Error(), "Cursor exit: a deferred tool proof was not stored: ") || !errors.Is(err, store.ErrStoreBusy) {
		t.Fatalf("exit with a lost proof = %v; want the wrapped ErrStoreBusy", err)
	}
	if got := c.e.request(t, token); got.ProvenGone() {
		t.Fatalf("proof stored while another writer held the lock: %+v", got)
	}
	finishFailedRun(t, c, release)
	if n := rotationRecordCount(c, t, "session_end"); n != 0 {
		t.Fatalf("exit that lost a proof recorded %d intentional ends", n)
	}
}

// busyDeferredProof leaves a running turn holding one natively answered
// request whose completed shell tool arrived while another writer holds the
// store's write lock, so its proof is deferred. The peer then asks a second
// permission in the same turn, whose admission waits transportWriteWait for
// the lock and fails the Run. A deferred proof's row is held, so no typed
// prompt can reach the controller's own store checks instead. It returns the
// deferred request, the turn's submission and the lock's release.
func busyDeferredProof(t *testing.T, c *cursorSession) (string, string, func()) {
	t.Helper()
	permission, completion := flushFrames(t)
	second, err := json.Marshal(frame{JSONRPC: "2.0", ID: json.RawMessage(`"flush-second"`), Method: "session/request_permission", Params: cursorfix.Permission("synthetic-session-1", "flush-tool-2", "execute")})
	if err != nil {
		t.Fatal(err)
	}
	c.e.runtime.Configure(t, cursorfix.Scenario{SessionID: "synthetic-session-1", Requests: []json.RawMessage{permission}, WaitForAfterAnswersRelease: true, AfterAnswers: []json.RawMessage{completion, second}})
	c.keys(t, "proof-turn\r")
	choice := regexp.MustCompile(`\[([^]]+)\] Allow once`)
	c.wait(t, "request offered in the running turn", func() bool { return choice.Match(c.pane.bytes()) })
	keys := choice.FindAllSubmatch(c.pane.bytes(), -1)
	if len(keys) != 1 {
		t.Fatalf("want one pane choice: %s", c.pane.bytes())
	}
	c.keys(t, "\x15"+string(keys[0][1]))
	c.wait(t, "peer received the pane answer", func() bool {
		for _, f := range c.e.runtime.Frames(t) {
			if len(f["method"]) == 0 && string(f["id"]) == `"flush-proof"` {
				return true
			}
		}
		return false
	})
	token := flushRequestToken(t, c)
	if got := c.e.request(t, token); got.PaneAnswer != store.PaneAnswerSent || got.Decision != "allow" || got.ProvenGone() {
		t.Fatalf("pane answer not recorded before the completion: %+v", got)
	}
	turn := ""
	for _, r := range c.records(t) {
		if r.Kind == "submission_accepted" && r.Data.Text == "proof-turn" {
			turn = r.Data.ID
		}
	}
	if turn == "" {
		t.Fatal("proof turn not accepted")
	}
	release := apitest.HoldWriteLock(t, c.e.path, time.Minute)
	rotationRelease(t, c.e.runtime.Release)
	return token, turn, release
}

// b.o89 round 3: with no cancellation running, a failed Run's best-effort
// flush of a deferred proof is bounded by cancelWait, not the busy timeout,
// so failure cleanup is not held for the busy window.
func TestCursorFailureFlushWithoutCancellationBoundedByCancelWait(t *testing.T) {
	// Serial: real Run, report subprocess and PTY with a synthetic ACP peer.
	c := longBusyCursorSession(t, openTranscript)
	token, turn, release := busyDeferredProof(t, c)
	err := loopResult(t, c, transportWriteWait+5*time.Second, "contended request admission did not fail the Run")
	if !strings.HasPrefix(fmt.Sprint(err), "Cursor authorization admission failed; no response sent: ") || !errors.Is(err, store.ErrStoreBusy) {
		t.Fatalf("contended request admission = %v; want the wrapped ErrStoreBusy", err)
	}
	failed := time.Now()
	if n := rotationRecordCount(c, t, "input_control"); n != 0 {
		t.Fatalf("%d controls recorded; the failure must come with no cancellation running", n)
	}
	if got := c.e.request(t, token); got.ProvenGone() {
		t.Fatalf("proof stored while another writer held the lock: %+v", got)
	}
	// Failure cleanup records the running turn's disposition after the flush.
	until := failed.Add(15 * time.Second)
	for {
		disposed := false
		for _, r := range c.records(t) {
			if r.Kind == "submission_unconfirmed" && r.Data.ID == turn && strings.HasPrefix(r.Data.Reason, "gateway stopped") {
				disposed = true
			}
		}
		if disposed {
			break
		}
		if time.Now().After(until) {
			t.Fatal("failure cleanup held by the deferred proof's busy window")
		}
		time.Sleep(time.Millisecond)
	}
	if elapsed := time.Since(failed); elapsed < cancelWait/2 {
		t.Fatalf("failure cleanup finished after %s; the flush did not retry the deferred proof", elapsed)
	}
	finishFailedRun(t, c, release)
}
