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

// peerAnswered reports whether the peer has received an answer to the
// request whose JSON-RPC id is id.
func peerAnswered(t *testing.T, c *cursorSession, id string) bool {
	t.Helper()
	for _, f := range c.e.runtime.Frames(t) {
		if len(f["method"]) == 0 && string(f["id"]) == id {
			return true
		}
	}
	return false
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
	c.wait(t, "peer received the pane answer", func() bool { return peerAnswered(t, c, `"flush-proof"`) })
	token := flushRequestToken(t, c)
	if got := c.e.request(t, token); got.PaneAnswer != store.PaneAnswerSent || got.Decision != "allow" {
		t.Fatalf("pane answer not recorded: %+v", got)
	}
	return token
}

// idleProofBarrier wraps open so the gateway stops once it has recorded the
// flush-tool completion as an idle update, before it tries that tool's proof.
func idleProofBarrier(t *testing.T, open func(string, store.CursorRuntime) (*transcript, error)) (*recordBarrier, func(string, store.CursorRuntime) (*transcript, error)) {
	t.Helper()
	f, _ := newMatchingRecordBarrier(t, "startup_or_idle_update", func(raw []byte) bool { return bytes.Contains(raw, []byte(`"flush-tool"`)) })
	return f, f.around(open)
}

// idleDeferredProof leaves an idle session holding one request answered at
// the pane while its turn was open, whose completed shell tool arrived after
// the turn's end while another writer holds the store's write lock, so its
// proof is deferred. idle is the session's idleProofBarrier. It returns the
// request and the lock's release.
func idleDeferredProof(t *testing.T, c *cursorSession, idle *recordBarrier) (string, func()) {
	t.Helper()
	permission, completion := flushFrames(t)
	// b.gwj: the turn's end retires a request still unanswered, so the pane
	// answer comes first; the peer then ends the turn and sends the completion.
	c.e.runtime.Configure(t, cursorfix.Scenario{SessionID: "synthetic-session-1", Requests: []json.RawMessage{permission}, CompleteBeforeAnswers: true, WaitForRelease: true, WaitForAfterAnswersRelease: true, AfterAnswers: []json.RawMessage{completion}, WaitForNew: true})
	c.keys(t, "proof-turn\r")
	c.wait(t, "request admitted", func() bool { return rotationRecordCount(c, t, "authorization_admitted") == 1 })
	token := answerFlushRequestAtPane(t, c)
	rotationRelease(t, c.e.runtime.Release)
	c.wait(t, "idle completion recorded", func() bool {
		select {
		case <-idle.entered:
			return true
		default:
			return false
		}
	})
	// The peer sends the completion after the turn's end, which the gateway
	// prints first; the test's pane reader can lag behind it.
	c.wait(t, "turn complete before the idle completion", func() bool { return bytes.Contains(c.pane.bytes(), []byte("Cursor turn complete")) })
	release := apitest.HoldWriteLock(t, c.e.path, time.Minute)
	idle.unblock()
	if got := c.e.request(t, token); got.ProvenGone() {
		t.Fatalf("request proven before its deferred completion: %+v", got)
	}
	return token, release
}

// b.o89 finding 3: when a tool completion's proof is deferred and the turn
// ends right after it, the Stop report must follow the stored proof. For an
// undelivered request, the tool_ran proof is what lets the row report waiting
// instead of check_permission.
func TestCursorStopReportFollowsDeferredProof(t *testing.T) {
	// Serial: real Run, report subprocess and PTY with a synthetic ACP peer.
	c := postBootstrapCursorSession(t, "off", "")
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
	c := postBootstrapCursorSession(t, "off", "")
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

// cancelTurnFrames returns a relayed permission request and the stream error
// cursor-agent prints as an agent message before it ends a turn with
// stopReason end_turn.
func cancelTurnFrames(t *testing.T) (permission, cancelChunk json.RawMessage) {
	t.Helper()
	permission, err := json.Marshal(frame{JSONRPC: "2.0", ID: json.RawMessage(`"cancel-proof"`), Method: "session/request_permission", Params: cursorfix.Permission("synthetic-session-1", "cancel-tool", "execute")})
	if err != nil {
		t.Fatal(err)
	}
	update, err := json.Marshal(map[string]any{"sessionId": "synthetic-session-1", "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "\n\nError: RetriableError: [canceled] http/2 stream closed with error code CANCEL (0x8)"}}})
	if err != nil {
		t.Fatal(err)
	}
	cancelChunk, err = json.Marshal(frame{JSONRPC: "2.0", Method: "session/update", Params: update})
	if err != nil {
		t.Fatal(err)
	}
	return permission, cancelChunk
}

// b.gwj: cursor-agent can end a turn with stopReason end_turn while a relayed
// request is still undecided, and never acts on that request afterwards. The
// turn's end proves the request gone, so the row reports waiting, and a later
// API decision sends nothing to the peer and leaves the row waiting.
func TestCursorTurnEndRetiresUndeliveredRequest(t *testing.T) {
	// Serial: real Run, report subprocess and PTY with a synthetic ACP peer.
	cases := []struct {
		name string
		// interrupt presses Ctrl-C while the request is undecided, and only
		// then lets the peer complete the prompt.
		interrupt bool
	}{
		{"end_turn with the request undecided", false},
		// b.gwj: the synthetic peer ignores session/cancel and replies
		// end_turn, so this covers session.go's cancellation path (the
		// turn-end write bounded by cancelAt+cancelWait, then the idle
		// control's recovery), not a literal stopReason cancelled.
		{"Ctrl-C with the request undecided", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := postBootstrapCursorSession(t, "on", "")
			permission, cancelChunk := cancelTurnFrames(t)
			c.e.runtime.Configure(t, cursorfix.Scenario{SessionID: "synthetic-session-1", Requests: []json.RawMessage{permission}, CompleteBeforeAnswers: true, WaitForRelease: tc.interrupt, AfterAnswers: []json.RawMessage{cancelChunk}})
			c.keys(t, "cancel-turn\r")
			ended := "Cursor turn complete"
			if tc.interrupt {
				c.wait(t, "request admitted", func() bool { return rotationRecordCount(c, t, "authorization_admitted") == 1 })
				c.keys(t, "\x03")
				c.wait(t, "cancellation requested", func() bool { return bytes.Contains(c.pane.bytes(), []byte("Cancellation requested")) })
				rotationRelease(t, c.e.runtime.Release)
				ended = "submit a new prompt to continue"
			}
			c.wait(t, "turn ended with the request undecided", func() bool { return bytes.Contains(c.pane.bytes(), []byte(ended)) })
			token := flushRequestToken(t, c)
			// The Stop's report commits before the pane shows the turn's end.
			if state := c.e.state(t); state != store.StateWaiting {
				t.Fatalf("turn ended with its request undecided, but the row reads %s, not waiting: %+v", state, c.e.request(t, token))
			}
			got := c.e.request(t, token)
			if got.ProvenGoneHow != store.ProvenGoneTurnEnd || got.HoldsDialog(store.AgentCursor, c.e.binding.Token) || got.Decision != "" || !got.DeliveredAt.IsZero() {
				t.Fatalf("turn's end did not prove the undelivered request gone: %+v", got)
			}
			if tc.interrupt && sessionCancelCount(t, c) != 1 {
				t.Fatalf("Ctrl-C sent %d session/cancel frames, want 1", sessionCancelCount(t, c))
			}
			lateDecisionSendsNothing(t, c, token)
			c.stop(t)
		})
	}
}

func sessionCancelCount(t *testing.T, c *cursorSession) int {
	t.Helper()
	n := 0
	for _, f := range c.e.runtime.Frames(t) {
		if string(f["method"]) == `"session/cancel"` {
			n++
		}
	}
	return n
}

// lateDecisionSendsNothing allows the cancel-proof request through the API,
// which records the verdict as not_confirmed, and checks that nothing reaches
// the peer and the row stays waiting.
func lateDecisionSendsNothing(t *testing.T, c *cursorSession, token string) {
	t.Helper()
	c.e.decide(t, token, "allow")
	if got := c.e.request(t, token); got.Decision != "allow" {
		t.Fatalf("late decision not stored: %+v", got)
	}
	// No gateway event marks a controller tick, so this watches a window, as
	// TestCursorExitStoresDeferredProofBeforeTermination does. The ticks run
	// every 50 ms; TestCursorRowStateAfterLastDeliveredDecision checks the
	// same at the controller with one explicit tick.
	quiet := time.Now().Add(time.Second)
	for time.Now().Before(quiet) {
		if peerAnswered(t, c, `"cancel-proof"`) {
			t.Fatalf("decision on a request proven gone reached the peer: %+v", c.e.request(t, token))
		}
		time.Sleep(10 * time.Millisecond)
	}
	c.e.wantState(t, "after the late decision", store.StateWaiting)
}

// b.gwj: a turn that /new ends while its relayed request is undecided retires
// that request too, so the rotation publishes waiting rather than
// check_permission, and a later API decision sends nothing.
func TestCursorTurnEndDuringRotationRetiresUndeliveredRequest(t *testing.T) {
	// Serial: real Run, report subprocess and PTY with a synthetic ACP peer.
	c := postBootstrapCursorSession(t, "on", "")
	permission, _ := cancelTurnFrames(t)
	c.e.runtime.Configure(t, cursorfix.Scenario{SessionID: "synthetic-session-1", Requests: []json.RawMessage{permission}, CompleteBeforeAnswers: true, WaitForRelease: true, WaitForNew: true})
	c.keys(t, "rotation-turn\r")
	c.wait(t, "request admitted", func() bool { return rotationRecordCount(c, t, "authorization_admitted") == 1 })
	token := flushRequestToken(t, c)
	c.keys(t, "\x15/new\r")
	c.wait(t, "rotation requested mid-turn", func() bool { return bytes.Contains(c.pane.bytes(), []byte("Cursor rotation requested")) })
	rotationRelease(t, c.e.runtime.Release)
	c.wait(t, "fresh session requested", func() bool { return rotationNewCount(c, t) == 2 })
	rotationRelease(t, c.e.runtime.NewRelease)
	c.wait(t, "rotation published", func() bool { return rotationRecordCount(c, t, "rotation_published") == 1 })
	if state := c.e.state(t); state != store.StateWaiting {
		t.Fatalf("rotation published after a turn that ended with its request undecided, but the row reads %s, not waiting: %+v", state, c.e.request(t, token))
	}
	if got := c.e.request(t, token); got.ProvenGoneHow != store.ProvenGoneTurnEnd || got.HoldsDialog(store.AgentCursor, c.e.binding.Token) || got.Decision != "" || !got.DeliveredAt.IsZero() {
		t.Fatalf("outgoing turn's end did not prove the undelivered request gone: %+v", got)
	}
	lateDecisionSendsNothing(t, c, token)
	c.stop(t)
}

// b.gwj control: a relayed request decided and delivered before the turn ends
// keeps its delivered decision, and the turn's end moves the row to waiting.
func TestCursorTurnEndAfterDeliveredDecisionReportsWaiting(t *testing.T) {
	// Serial: real Run, report subprocess and PTY with a synthetic ACP peer.
	c := postBootstrapCursorSession(t, "on", "")
	permission, cancelChunk := cancelTurnFrames(t)
	c.e.runtime.Configure(t, cursorfix.Scenario{SessionID: "synthetic-session-1", Requests: []json.RawMessage{permission}, AfterAnswers: []json.RawMessage{cancelChunk}})
	c.keys(t, "cancel-turn\r")
	c.wait(t, "request admitted", func() bool { return rotationRecordCount(c, t, "authorization_admitted") == 1 })
	token := flushRequestToken(t, c)
	c.e.decide(t, token, "allow")
	c.wait(t, "peer received the decision", func() bool { return peerAnswered(t, c, `"cancel-proof"`) })
	c.wait(t, "turn complete", func() bool { return bytes.Contains(c.pane.bytes(), []byte("Cursor turn complete")) })
	if state := c.e.state(t); state != store.StateWaiting {
		t.Fatalf("turn ended after its request was delivered, but the row reads %s: %+v", state, c.e.request(t, token))
	}
	if got := c.e.request(t, token); got.Decision != "allow" || got.DeliveredAt.IsZero() || got.ProvenGoneHow == store.ProvenGoneTurnEnd {
		t.Fatalf("turn's end changed a delivered request: %+v", got)
	}
	c.stop(t)
}

// b.eoe: a rotation requested mid-turn leaves the outgoing turn busy, but a
// proof of its tool must not move the row, because the turn's end reports no
// Stop while rotating and RotateCursorSession settles the row instead.
func TestCursorProofDuringMidTurnRotationLeavesRowHeld(t *testing.T) {
	// Serial: real Run, report subprocess and PTY with a synthetic ACP peer.
	c := postBootstrapCursorSession(t, "off", "")
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
	idle, opener := idleProofBarrier(t, openTranscript)
	c := postBootstrapCursorSessionWithInput(t, "off", "", opener, terminalLoop)
	c.wait(t, "confirmed ready", func() bool { return bytes.Contains(c.pane.bytes(), []byte("Cursor ready")) })
	token, release := idleDeferredProof(t, c, idle)
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
	idle, opener := idleProofBarrier(t, openTranscript)
	c := postBootstrapCursorSessionWithInput(t, "off", "", opener, terminalLoop)
	c.wait(t, "confirmed ready", func() bool { return bytes.Contains(c.pane.bytes(), []byte("Cursor ready")) })
	token, release := idleDeferredProof(t, c, idle)
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
