//go:build linux

package cursorhost

import (
	"bytes"
	"encoding/json"
	"regexp"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

func flushFrames(t *testing.T) (permission, completion json.RawMessage) {
	t.Helper()
	permission, err := json.Marshal(frame{JSONRPC: "2.0", ID: json.RawMessage(`"flush-proof"`), Method: "session/request_permission", Params: cursorfix.Permission("synthetic-session-1", "flush-tool", "execute")})
	if err != nil {
		t.Fatal(err)
	}
	completion, err = json.Marshal(frame{JSONRPC: "2.0", Method: "session/update", Params: executionUpdate("synthetic-session-1", "flush-tool", "completed", flushShellOutput)})
	if err != nil {
		t.Fatal(err)
	}
	return permission, completion
}

func flushRequestToken(t *testing.T, c *cursorSession) string {
	t.Helper()
	rows, err := c.e.s.PermissionRequestsForSpawn(c.e.binding.InstanceID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("want one permission request: %+v %v", rows, err)
	}
	return rows[0].RequestToken
}

func flushPaneProof(t *testing.T, got store.PermissionRow) {
	t.Helper()
	if got.ProvenGoneHow != store.ProvenGoneToolRan || got.PaneAnswer != store.PaneAnswerSent || got.Decision != "allow" || got.DecisionReason != "pane" || got.Closed() {
		t.Fatalf("deferred proof of a pane-answered request not stored as tool_ran: %+v", got)
	}
}

// answerFlushRequestAtPane allows the one flush-proof request at the pane,
// waits until the peer has the answer and returns the request's token. A
// peer that does not hold the completion may send it at once, so callers
// that need the request still unproven check that themselves.
func answerFlushRequestAtPane(t *testing.T, c *cursorSession) string {
	t.Helper()
	keys := regexp.MustCompile(`\[([^]]+)\] Allow once`).FindAllSubmatch(c.pane.bytes(), -1)
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
	if got := c.e.request(t, token); got.PaneAnswer != store.PaneAnswerSent || got.Decision != "allow" {
		t.Fatalf("pane answer not recorded: %+v", got)
	}
	return token
}

// idleDeferredProof leaves an idle session holding one natively answered
// request whose completed shell tool arrived while another writer holds the
// store's write lock, so its proof is deferred. It returns the request and the
// lock's release.
func idleDeferredProof(t *testing.T, c *cursorSession) (string, func()) {
	t.Helper()
	permission, completion := flushFrames(t)
	c.e.runtime.Configure(t, cursorfix.Scenario{SessionID: "synthetic-session-1", Requests: []json.RawMessage{permission}, CompleteBeforeAnswers: true, WaitForAfterAnswersRelease: true, AfterAnswers: []json.RawMessage{completion}, WaitForNew: true})
	c.keys(t, "proof-turn\r")
	c.wait(t, "turn ended with the request open", func() bool { return bytes.Contains(c.pane.bytes(), []byte("Cursor turn complete")) })
	token := answerFlushRequestAtPane(t, c)
	if got := c.e.request(t, token); got.ProvenGone() {
		t.Fatalf("pane answer not recorded before the completion: %+v", got)
	}
	idle := rotationRecordCount(c, t, "startup_or_idle_update")
	release := apitest.HoldWriteLock(t, c.e.path, time.Minute)
	rotationRelease(t, c.e.runtime.Release)
	c.wait(t, "idle completion received while the writer holds the lock", func() bool { return rotationRecordCount(c, t, "startup_or_idle_update") > idle })
	if got := c.e.request(t, token); got.ProvenGone() {
		t.Fatalf("proof stored while another writer held the lock: %+v", got)
	}
	return token, release
}

// b.o89 finding 3: when a tool completion's proof is deferred and the turn
// ends right after it, the Stop report must follow the stored proof. For an
// undelivered request, the tool_ran proof is what lets the row report waiting
// instead of check_permission.
func TestCursorStopReportFollowsDeferredProof(t *testing.T) {
	// Serial: real Run, report subprocess and PTY with a synthetic ACP peer.
	c := postBootstrapCursorSession(t, "")
	permission, completion := flushFrames(t)
	c.e.runtime.Configure(t, cursorfix.Scenario{SessionID: "synthetic-session-1", Requests: []json.RawMessage{permission}, CompleteBeforeAnswers: true, WaitForRelease: true, AfterAnswers: []json.RawMessage{completion}})
	c.keys(t, "proof-turn\r")
	c.wait(t, "request admitted", func() bool { return rotationRecordCount(c, t, "authorization_admitted") == 1 })
	token := flushRequestToken(t, c)
	release := apitest.HoldWriteLock(t, c.e.path, time.Minute)
	rotationRelease(t, c.e.runtime.Release)
	c.wait(t, "turn ended while the writer holds the lock", func() bool { return rotationRecordCount(c, t, "turn_outcome") == 1 })
	release()
	c.wait(t, "turn complete", func() bool { return bytes.Contains(c.pane.bytes(), []byte("Cursor turn complete")) })
	got := c.e.request(t, token)
	row, err := c.e.s.GetSpawn(c.e.binding.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ProvenGoneHow != store.ProvenGoneToolRan || got.PaneAnswer != store.PaneAnswerToolRan || row.State != store.StateWaiting {
		t.Fatalf("Stop reported before the deferred proof: state=%s request=%+v", row.State, got)
	}
	c.stop(t)
}

// b.eoe: the live session loop hands its open turn to the controller, so an
// allowed tool's proof moves the row to working while the turn goes on, and
// the turn's Stop then moves it to waiting.
func TestCursorProofDuringOpenTurnMovesRowToWorking(t *testing.T) {
	// Serial: real Run, report subprocess and PTY with a synthetic ACP peer.
	c := postBootstrapCursorSession(t, "")
	permission, completion := flushFrames(t)
	c.e.runtime.Configure(t, cursorfix.Scenario{SessionID: "synthetic-session-1", Requests: []json.RawMessage{permission}, AfterAnswers: []json.RawMessage{completion}})
	// HOLD keeps the turn open after the completion until the runtime's release.
	c.keys(t, "HOLD\r")
	c.wait(t, "request admitted", func() bool { return rotationRecordCount(c, t, "authorization_admitted") == 1 })
	c.e.wantState(t, "admitted request", store.StateCheckPermission)
	token := answerFlushRequestAtPane(t, c)
	c.wait(t, "proof moved the row to working", func() bool {
		return c.e.request(t, token).ProvenGoneHow == store.ProvenGoneToolRan && c.e.state(t) == store.StateWorking
	})
	if rotationRecordCount(c, t, "turn_outcome") != 0 || bytes.Contains(c.pane.bytes(), []byte("Cursor turn complete")) {
		t.Fatal("turn ended before the runtime's release; the proof did not run with the turn open")
	}
	rotationRelease(t, c.e.runtime.Release)
	c.wait(t, "turn complete", func() bool { return bytes.Contains(c.pane.bytes(), []byte("Cursor turn complete")) })
	c.e.wantState(t, "after the turn's Stop", store.StateWaiting)
	c.stop(t)
}

// b.eoe: a rotation requested mid-turn leaves the outgoing turn busy, but a
// proof of its tool must not move the row, because the turn's end reports no
// Stop while rotating and RotateCursorSession settles the row instead.
func TestCursorProofDuringMidTurnRotationLeavesRowHeld(t *testing.T) {
	// Serial: real Run, report subprocess and PTY with a synthetic ACP peer.
	c := postBootstrapCursorSession(t, "")
	permission, completion := flushFrames(t)
	c.e.runtime.Configure(t, cursorfix.Scenario{SessionID: "synthetic-session-1", Requests: []json.RawMessage{permission}, WaitForAfterAnswersRelease: true, AfterAnswers: []json.RawMessage{completion}, WaitForNew: true})
	c.keys(t, "rotation-proof-turn\r")
	c.wait(t, "request admitted", func() bool { return rotationRecordCount(c, t, "authorization_admitted") == 1 })
	token := answerFlushRequestAtPane(t, c)
	if got := c.e.request(t, token); got.ProvenGone() {
		t.Fatalf("pane answer not recorded before the completion: %+v", got)
	}
	c.keys(t, "\x15/new\r")
	c.wait(t, "rotation requested mid-turn", func() bool { return bytes.Contains(c.pane.bytes(), []byte("Cursor rotation requested")) })
	if rotationRecordCount(c, t, "turn_outcome") != 0 {
		t.Fatal("outgoing turn ended before the rotation was requested")
	}
	rotationRelease(t, c.e.runtime.Release)
	// The fresh session is held, so no report follows the outgoing tool's proof.
	c.wait(t, "fresh session requested", func() bool { return rotationNewCount(c, t) == 2 })
	if got := c.e.request(t, token); got.ProvenGoneHow != store.ProvenGoneToolRan {
		t.Fatalf("outgoing tool's proof not stored during the rotation: %+v", got)
	}
	c.e.wantState(t, "during the rotation", store.StateCheckPermission)
	rotationRelease(t, c.e.runtime.NewRelease)
	c.wait(t, "rotation published", func() bool { return rotationRecordCount(c, t, "rotation_published") == 1 })
	c.stop(t)
}

// b.o89 finding 1: rotation must store a deferred proof under the outgoing
// binding before publishing the fresh session, after which that binding can
// no longer prove anything and a retry would end the controller.
func TestCursorRotationStoresDeferredProofBeforePublication(t *testing.T) {
	// Serial: real Run, report subprocess and PTY with a synthetic ACP peer.
	c := postBootstrapCursorSession(t, "")
	token, release := idleDeferredProof(t, c)
	c.keys(t, "\x15/new\r")
	c.wait(t, "fresh session requested", func() bool { return rotationNewCount(c, t) == 2 })
	rotationRelease(t, c.e.runtime.NewRelease)
	c.wait(t, "rotation confirmed while the writer holds the lock", func() bool { return rotationRecordCount(c, t, "rotation_confirmed") == 1 })
	if got := c.e.request(t, token); got.ProvenGone() {
		t.Fatalf("proof stored while another writer held the lock: %+v", got)
	}
	release()
	c.wait(t, "rotation published", func() bool { return rotationRecordCount(c, t, "rotation_published") == 1 })
	flushPaneProof(t, c.e.request(t, token))
	if bound, err := c.e.s.ReadCursorRuntime(c.e.binding.InstanceID); err != nil || bound.SessionID != "synthetic-session-2" {
		t.Fatalf("fresh session not published: %+v %v", bound, err)
	}
	c.stop(t)
}

// b.o89 finding 2: an intentional /exit must store a deferred proof before
// terminating the runtime, whose termination retires the proof's authority.
func TestCursorExitStoresDeferredProofBeforeTermination(t *testing.T) {
	// Serial: real Run, report subprocess and PTY with a synthetic ACP peer.
	c := postBootstrapCursorSession(t, "")
	token, release := idleDeferredProof(t, c)
	c.keys(t, "\x15/exit\r")
	held := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(held) {
		select {
		case err := <-c.done:
			c.ended, c.endErr = true, err
			t.Fatalf("Run ended (%v) while the deferred proof could still be stored: %+v", err, c.e.request(t, token))
		default:
		}
		if rotationRecordCount(c, t, "session_end") != 0 {
			t.Fatalf("runtime terminated with its tool proof still deferred: %+v", c.e.request(t, token))
		}
		time.Sleep(time.Millisecond)
	}
	release()
	select {
	case err := <-c.done:
		c.ended, c.endErr = true, err
		if err != nil {
			t.Fatalf("exit after the writer released: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("exit timed out after the writer released")
	}
	flushPaneProof(t, c.e.request(t, token))
	row, err := c.e.s.GetSpawn(c.e.binding.InstanceID)
	if err != nil || row.State != store.StateEnded || rotationRecordCount(c, t, "session_end") != 1 {
		t.Fatalf("exit not intentional: %+v %v", row, err)
	}
}
