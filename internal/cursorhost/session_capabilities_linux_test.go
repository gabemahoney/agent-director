//go:build linux

package cursorhost

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
	"github.com/gabemahoney/agent-director/pkg/api"
)

func TestCursorInitializeRefusesBeforeDependentWork(t *testing.T) {
	for _, reply := range []string{
		`{"result":{"protocolVersion":1}}`,
		`{"result":{"protocolVersion":2,"agentCapabilities":{}}}`,
		`{"result":{"protocolVersion":1,"agentCapabilities":null,"AGENTCAPABILITIES":{}}}`,
		`{"result":{"protocolVersion":null,"PROTOCOLVERSION":1,"agentCapabilities":{}}}`,
		`{"error":{"code":-32000,"message":"SYNTHETIC-SECRET\n\u001b","data":"SYNTHETIC-SECRET"}}`,
	} {
		for _, load := range []bool{false, true} {
			t.Run(reply+map[bool]string{false: "/fresh", true: "/load"}[load], func(t *testing.T) {
				scenario := cursorfix.Scenario{InitializeReply: json.RawMessage(reply), Modes: advertisedCursorModes()}
				input := &completeInput{ready: make(chan struct{})}
				var c *cursorSession
				if load {
					c = beginCursorLoadWithOptions(t, scenario, Options{InitialPrompt: "never dispatch", RequestedMode: "plan"}, input.start, nil)
				} else {
					c = beginCursorSessionWithOptions(t, Options{InitialPrompt: "never dispatch", RequestedMode: "plan"}, scenario, openTranscript, input.start)
				}
				err := c.awaitFailure(t, 3*time.Second)
				if strings.Contains(err.Error(), "SYNTHETIC-SECRET") || strings.ContainsAny(err.Error(), "\n\r\x1b") {
					t.Fatalf("unsafe diagnostic: %q", err)
				}
				if input.admit() || bytes.Contains(c.pane.bytes(), []byte("Cursor ready")) {
					t.Fatal("invalid initialize published readiness/admission")
				}
				for _, f := range c.e.runtime.Frames(t) {
					if string(f["method"]) != `"initialize"` {
						t.Fatalf("dependent work escaped: %s", f["method"])
					}
				}
				for _, r := range c.records(t) {
					if r.Kind == "session_start" || r.Kind == "session_loaded" || r.Kind == "submission_dispatched" || r.Kind == "turn_outcome" {
						t.Fatalf("invalid initialize published %s", r.Kind)
					}
				}
				requests, err := c.e.s.PermissionRequestsForSpawn(c.e.binding.InstanceID)
				if err != nil || len(requests) != 0 {
					t.Fatalf("initialize granted permission authority: %+v %v", requests, err)
				}
			})
		}
	}
}

func TestCursorMinimalCapabilitiesAllowFreshPrompt(t *testing.T) {
	for _, caps := range []string{`{}`, `{"loadSession":false}`, `{"sessionCapabilities":{"fork":true,"systemPrompt":[]}}`} {
		t.Run(caps, func(t *testing.T) {
			c := beginCursorSessionWithOptions(t, Options{InitialPrompt: "minimal prompt"}, cursorfix.Scenario{InitializeReply: json.RawMessage(`{"result":{"protocolVersion":1,"PROTOCOLVERSION":null,"agentCapabilities":` + caps + `,"AGENTCAPABILITIES":null}}`)}, openTranscript, terminalLoop)
			c.wait(t, "minimal baseline prompt completed", func() bool { return bytes.Contains(c.pane.bytes(), []byte("Cursor turn complete")) })
			c.stop(t)
			if got := c.prompts(t); len(got) != 1 || got[0] != "minimal prompt" {
				t.Fatalf("minimal prompt: %v", got)
			}
		})
	}
}

func TestCursorCorrelatedOperationFailureSanitized(t *testing.T) {
	reply := json.RawMessage(`{"error":{"code":-32603,"CODE":-32000,"message":"SYNTHETIC-SECRET\n\u001b login required","data":{"token":"SYNTHETIC-SECRET"}}}`)
	for _, operation := range []string{"initialize", "session/new", "session/load", "session/set_mode", "session/prompt"} {
		t.Run(operation, func(t *testing.T) {
			scenario := cursorfix.Scenario{Modes: advertisedCursorModes()}
			options := Options{InitialPrompt: "initial"}
			switch operation {
			case "initialize":
				scenario.InitializeReply = reply
			case "session/new":
				scenario.FirstNewReply = reply
			case "session/load":
				scenario.LoadSupport = true
				scenario.LoadReply = reply
			case "session/set_mode":
				options.RequestedMode = "plan"
				scenario.ModeReply = reply
			case "session/prompt":
				scenario.PromptReply = reply
			}
			var c *cursorSession
			selected := map[string]string{"CURSOR_CONFIG_DIR": "/selected/failure config", "CURSOR_DATA_DIR": "/selected/failure data"}
			before := func(e *requestEnv) {
				raw, _ := json.Marshal(selected)
				e.exec(t, `UPDATE spawns SET extra_env=? WHERE instance_id=?`, string(raw), e.binding.InstanceID)
			}
			if operation == "session/load" {
				c = beginCursorLoad(t, scenario, "initial", terminalLoop, before)
			} else {
				c = beginConfiguredCursorSession(t, options, scenario, openTranscript, terminalLoop, before)
			}
			if operation == "session/prompt" {
				// b.ivj D1/D2: one correlated error ends the turn; a second
				// consecutive error ends the session, with no prompt replay.
				waitCursorFailedTurn(t, c, 1)
				cursorFailureView(t, c, store.StateWaiting, cursorPromptFailureReason)
				assertCursorFailedOutcomes(t, c, 1)
				c.idleObservationBarrier(t, "after first prompt failure")
				if got := c.prompts(t); !reflect.DeepEqual(got, []string{"initial"}) {
					t.Fatalf("failed prompt replayed: %v", got)
				}
				c.keys(t, "second failure\r")
			}
			err := c.awaitFailure(t, 3*time.Second)
			if !strings.Contains(err.Error(), operation+" failed:") || !strings.Contains(err.Error(), "internal error; cause unknown") || strings.Contains(err.Error(), "SYNTHETIC-SECRET") {
				t.Fatalf("wrong failure: %v", err)
			}
			if bytes.Contains(c.pane.bytes(), []byte("SYNTHETIC-SECRET")) || bytes.Contains(c.pane.bytes(), []byte("authentication required")) {
				t.Fatalf("runtime text or alias acquired authority: %q", c.pane.bytes())
			}
			if operation != "session/prompt" && len(c.prompts(t)) != 0 {
				t.Fatal("startup refusal dispatched prompt")
			}
			// b.ivj D2: all these ACP replies follow registration, so fatal
			// cleanup records the exact sanitized cause on both public readers.
			cursorFailureView(t, c, store.StateMissing, "Cursor ACP "+operation+" failed: runtime internal error; cause unknown")
			if operation == "session/prompt" {
				assertCursorFailedOutcomes(t, c, 2)
				if got := c.prompts(t); !reflect.DeepEqual(got, []string{"initial", "second failure"}) {
					t.Fatalf("failed prompt replayed: %v", got)
				}
			}
			for key, want := range selected {
				got := c.e.runtime.Selection(t)[key]
				if got == nil || *got != want {
					t.Fatalf("received %s=%v; want %q", key, got, want)
				}
			}
			wantMethods := []string{`"initialize"`}
			if operation != "initialize" {
				next := "session/new"
				if operation == "session/load" {
					next = operation
				}
				wantMethods = append(wantMethods, `"`+next+`"`)
				if operation == "session/set_mode" || operation == "session/prompt" {
					wantMethods = append(wantMethods, `"`+operation+`"`)
				}
				if operation == "session/prompt" {
					wantMethods = append(wantMethods, `"session/prompt"`)
				}
			}
			var methods []string
			for _, frame := range c.e.runtime.Frames(t) {
				if len(frame["method"]) != 0 {
					methods = append(methods, string(frame["method"]))
				}
			}
			if !reflect.DeepEqual(methods, wantMethods) {
				t.Fatalf("failure retried/dependent work escaped: %v want %v", methods, wantMethods)
			}
		})
	}
	t.Run("before-registration/load-validation", func(t *testing.T) {
		// b.ivj A1: continuity validation fails before registration; the
		// gateway cannot mark missing or record a reason through fatal cleanup.
		c := beginCursorLoad(t, cursorfix.Scenario{LoadSupport: true}, "never dispatch", terminalLoop, func(e *requestEnv) {
			e.exec(t, `UPDATE spawns SET session_id='changed-before-load' WHERE instance_id=?`, e.binding.InstanceID)
		})
		if err := c.awaitFailure(t, 3*time.Second); err.Error() != "Cursor latest conversation changed" {
			t.Fatalf("unexpected pre-registration failure: %v", err)
		}
		cursorFailureView(t, c, store.StatePending, nil)
		if frames := c.e.runtime.Frames(t); len(frames) != 0 {
			t.Fatalf("pre-registration refusal sent ACP work: %+v", frames)
		}
	})
}

const cursorPromptFailureReason = "Cursor ACP session/prompt failed: runtime internal error; cause unknown"

// b.ivj: correlation alone does not make a malformed error a recoverable turn failure.
func TestCursorMalformedPromptErrorStaysFatal(t *testing.T) {
	for _, payload := range []string{
		`{}`,
		`{"code":"-32603","message":"SYNTHETIC-SECRET"}`,
		`{"code":-32603,"message":null}`,
	} {
		t.Run(payload, func(t *testing.T) {
			c := beginCursorSessionWithOptions(t, Options{InitialPrompt: "malformed rejection"}, cursorfix.Scenario{PromptReply: json.RawMessage(`{"error":` + payload + `}`)}, openTranscript, terminalLoop)
			err := c.awaitFailure(t, 3*time.Second)
			want := "Cursor ACP session/prompt failed: malformed error response"
			if err.Error() != want {
				t.Fatalf("malformed error returned %q; want %q", err, want)
			}
			cursorFailureView(t, c, store.StateMissing, want)
			if bytes.Contains(c.pane.bytes(), []byte("Cursor turn failed:")) || rotationRecordCount(c, t, "turn_outcome") != 0 {
				t.Fatalf("malformed error acquired turn-completion authority: %s", c.pane.bytes())
			}
			if got := c.prompts(t); !reflect.DeepEqual(got, []string{"malformed rejection"}) {
				t.Fatalf("malformed rejection replayed input: %v", got)
			}
		})
	}
}

// b.ivj: idle-control recovery's duplicate Stop must retain the failed turn's reason.
func TestCursorPromptFailureAfterCancellationRetainsReason(t *testing.T) {
	c := startCursorSession(t, "HOLD")
	c.wait(t, "held prompt dispatched", func() bool { return len(c.prompts(t)) == 1 })
	var id json.RawMessage
	for _, f := range c.e.runtime.Frames(t) {
		if string(f["method"]) == `"session/prompt"` {
			id = f["id"]
		}
	}
	c.keys(t, "\x03")
	c.wait(t, "peer received cancellation", func() bool { return sessionCancelCount(t, c) == 1 })
	c.e.runtime.Emit(t, "failure-after-cancel", fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"error":{"code":-32603,"message":"SYNTHETIC-SECRET","data":"SYNTHETIC-SECRET"}}`, id)+"\n")
	waitCursorFailedTurn(t, c, 1)
	// This marker follows idleControl's report, so the assertion cannot race
	// the second Stop that used to erase the first Stop's failure_reason.
	c.wait(t, "idle-control recovery completed", func() bool {
		return bytes.Contains(c.pane.bytes(), []byte("Cursor input cancelled; submit a new prompt to continue\r\n"))
	})
	c.idleObservationBarrier(t, "after cancelled failed turn recovery")
	cursorFailureView(t, c, store.StateWaiting, cursorPromptFailureReason)
	outcomes := 0
	for _, r := range c.records(t) {
		if r.Kind != "turn_outcome" {
			continue
		}
		outcomes++
		if r.Data.Outcome != "failed" || r.Data.Reason != cursorPromptFailureReason || r.Data.Request != string(id) || r.Data.CancellationRequested == nil || !*r.Data.CancellationRequested {
			t.Fatalf("cancelled failed turn lost outcome/correlation: %+v", r)
		}
	}
	if outcomes != 1 || !reflect.DeepEqual(c.prompts(t), []string{"HOLD"}) {
		t.Fatalf("cancellation replayed/invented work: outcomes=%d prompts=%v", outcomes, c.prompts(t))
	}
	c.stop(t)
}

func waitCursorFailedTurn(t *testing.T, c *cursorSession, count int) {
	t.Helper()
	c.wait(t, "failed turn returned to waiting", func() bool {
		return bytes.Count(c.pane.bytes(), []byte("Cursor turn failed: "+cursorPromptFailureReason+"\r\n")) == count && c.e.state(t) == store.StateWaiting
	})
}

// Decode the public JSON so these regressions compile before D2 adds its field.
func cursorFailureView(t *testing.T, c *cursorSession, state string, reason any) {
	t.Helper()
	view, err := api.Get(c.e.s, api.RelayView{}, c.e.binding.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	status, err := api.Status(c.e.s, api.RelayView{}, c.e.binding.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	for name, result := range map[string]any{"get": view, "status": status} {
		raw, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		got, present := fields["failure_reason"]
		if fields["state"] != state || !present || got != reason {
			t.Fatalf("%s state=%v failure_reason=%v (present=%v); want %s / %v", name, fields["state"], got, present, state, reason)
		}
		if bytes.Contains(raw, []byte("SYNTHETIC-SECRET")) || bytes.Contains(raw, []byte("login required")) {
			t.Fatalf("peer diagnostic leaked through %s: %s", name, raw)
		}
	}
	transcript, err := os.ReadFile(view.JSONLPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, surface := range [][]byte{transcript, c.pane.bytes()} {
		if bytes.Contains(surface, []byte("SYNTHETIC-SECRET")) || bytes.Contains(surface, []byte("login required")) {
			t.Fatalf("peer diagnostic leaked: %s", surface)
		}
	}
}

func assertCursorFailedOutcomes(t *testing.T, c *cursorSession, want int) {
	t.Helper()
	started := map[string]string{}
	seen := map[string]bool{}
	for _, r := range c.records(t) {
		if r.Kind == "submission_started" {
			started[r.Data.ID] = r.Data.Request
		}
		if r.Kind == "submission_unconfirmed" {
			t.Fatalf("known failed turn recorded as unconfirmed: %+v", r)
		}
		if r.Kind != "turn_outcome" {
			continue
		}
		if r.Data.Outcome != "failed" || r.Data.Reason != cursorPromptFailureReason || r.Data.StopReason != "" || r.Data.CancellationRequested == nil || *r.Data.CancellationRequested || r.Data.ID == "" || r.Data.Request == "" || started[r.Data.ID] != r.Data.Request || seen[r.Data.ID] {
			t.Fatalf("wrong failed-turn outcome: %+v", r)
		}
		seen[r.Data.ID] = true
		input := c.capturedInput(t, r)
		if input.State != "failed" || input.Detail != cursorPromptFailureReason || input.RequestID != r.Data.Request {
			t.Fatalf("failed input lost its disposition/correlation: %+v", input)
		}
	}
	if len(seen) != want {
		t.Fatalf("failed turn outcomes=%d, want %d", len(seen), want)
	}
}

// b.ivj D1: any valid completion, including cancellation, breaks the failure streak.
func TestCursorPromptFailureCountResetsOnCompletion(t *testing.T) {
	for _, completion := range []string{"end_turn", "cancelled"} {
		t.Run(completion, func(t *testing.T) {
			c := startCursorSession(t, "")
			failure := cursorfix.Scenario{PromptReply: json.RawMessage(`{"error":{"code":-32603,"message":"SYNTHETIC-SECRET","data":"SYNTHETIC-SECRET"}}`)}
			c.e.runtime.Configure(t, failure)
			c.keys(t, "first failure\r")
			waitCursorFailedTurn(t, c, 1)
			cursorFailureView(t, c, store.StateWaiting, cursorPromptFailureReason)

			c.e.runtime.Configure(t, cursorfix.Scenario{PromptReply: json.RawMessage(fmt.Sprintf(`{"result":{"stopReason":%q}}`, completion))})
			c.keys(t, "valid completion\r")
			c.wait(t, "successful completion returned to waiting", func() bool {
				return bytes.Contains(c.pane.bytes(), []byte("Cursor turn complete\r\n")) && c.e.state(t) == store.StateWaiting
			})
			cursorFailureView(t, c, store.StateWaiting, nil)

			c.e.runtime.Configure(t, failure)
			c.keys(t, "failure after completion\r")
			waitCursorFailedTurn(t, c, 2)
			cursorFailureView(t, c, store.StateWaiting, cursorPromptFailureReason)
			c.stop(t)
			if got := c.prompts(t); !reflect.DeepEqual(got, []string{"first failure", "valid completion", "failure after completion"}) {
				t.Fatalf("prompt history: %v", got)
			}
		})
	}
}

// b.ivj D1: an error buffered before runtime death must never recover to waiting.
func TestCursorPromptFailureWithDeadRuntimeStaysFatal(t *testing.T) {
	barrier, open := newRecordBarrier(t, "update")
	c := beginCursorSessionWithOptions(t, Options{}, cursorfix.Scenario{}, open, terminalLoop)
	defer barrier.unblock()
	c.wait(t, "confirmed ready", func() bool { return bytes.Contains(c.pane.bytes(), []byte("Cursor ready")) })
	c.keys(t, "HOLD\r")
	c.wait(t, "prompt dispatched", func() bool { return len(c.prompts(t)) == 1 })
	var id json.RawMessage
	for _, f := range c.e.runtime.Frames(t) {
		if string(f["method"]) == `"session/prompt"` {
			id = f["id"]
		}
	}
	c.e.runtime.Emit(t, "error-before-death", `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"synthetic-session-1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"barrier"}}}}`+"\n"+fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"error":{"code":-32603,"message":"SYNTHETIC-SECRET"}}`, id)+"\n")
	c.wait(t, "update blocks error processing", func() bool {
		select {
		case <-barrier.entered:
			return true
		default:
			return false
		}
	})
	binding, err := c.e.s.ReadCursorRuntime(c.e.binding.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(binding.RuntimePID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	awaitProcessGone(t, binding.RuntimePID)
	barrier.unblock()
	err = c.awaitFailure(t, 5*time.Second)
	cursorFailureView(t, c, store.StateMissing, err.Error())
	if bytes.Contains(c.pane.bytes(), []byte("Cursor turn failed:")) || len(c.prompts(t)) != 1 {
		t.Fatalf("dead runtime recovered/replayed: %s", c.pane.bytes())
	}
}

// b.ivj: failed turn teardown proves an outstanding permission gone without answering it.
func TestCursorPromptFailureRetiresPendingPermission(t *testing.T) {
	c := postBootstrapCursorSession(t, "on", "")
	permission, _ := flushFrames(t)
	c.e.runtime.Configure(t, cursorfix.Scenario{Requests: []json.RawMessage{permission}})
	c.keys(t, "pending permission\r")
	c.wait(t, "permission admitted", func() bool { return rotationRecordCount(c, t, "authorization_admitted") == 1 })
	token := flushRequestToken(t, c)
	before := c.e.request(t, token)
	if before.ProvenGone() || before.Decision != "" {
		t.Fatalf("request not pending: %+v", before)
	}
	var id json.RawMessage
	for _, f := range c.e.runtime.Frames(t) {
		if string(f["method"]) == `"session/prompt"` {
			id = f["id"]
		}
	}
	c.e.runtime.Emit(t, "fail-with-request", fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"error":{"code":-32603,"message":"SYNTHETIC-SECRET"}}`, id)+"\n")
	waitCursorFailedTurn(t, c, 1)
	cursorFailureView(t, c, store.StateWaiting, cursorPromptFailureReason)
	r := c.e.request(t, token)
	if r.ProvenGoneHow != store.ProvenGoneTurnEnd || r.ProvenGoneAt.IsZero() || r.Decision != "" || !r.DeliveredAt.IsZero() || r.ResponseState != "" {
		t.Fatalf("failed turn fabricated response authority: %+v", r)
	}
	c.idleObservationBarrier(t, "failed permission turn settled")
	if peerAnswered(t, c, `"flush-proof"`) {
		t.Fatal("failed turn answered the retired permission")
	}
	c.stop(t)
}

func TestCursorCorrelatedModeAdviceAuthority(t *testing.T) {
	for _, tc := range []struct {
		name, reply, cause string
		remedy             bool
	}{
		{"authentication", `{"error":{"code":-32000,"message":"SYNTHETIC-SECRET"}}`, "authentication required", false},
		{"unknown", `{"error":{"code":-32099,"message":"unsupported SYNTHETIC-SECRET"}}`, "cause unknown", false},
		{"internal", `{"error":{"code":-32603,"message":"SYNTHETIC-SECRET"}}`, "internal error", false},
		{"format", `{"error":{"code":-32602,"message":"SYNTHETIC-SECRET"}}`, "format or parameters", false},
		{"cancelled", `{"error":{"code":-32800,"message":"SYNTHETIC-SECRET"}}`, "cancelled", false},
		{"resource", `{"error":{"code":-32002,"message":"SYNTHETIC-SECRET"}}`, "resource not found", false},
		{"malformed", `{"error":{"code":"-32601","message":"SYNTHETIC-SECRET"}}`, "malformed", false},
		{"unsupported", `{"error":{"code":-32601,"message":"SYNTHETIC-SECRET"}}`, "unsupported", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := beginCursorSessionWithOptions(t, Options{RequestedMode: "plan", InitialPrompt: "never admit"}, cursorfix.Scenario{Modes: advertisedCursorModes(), ModeReply: json.RawMessage(tc.reply)}, openTranscript, terminalLoop)
			err := c.awaitFailure(t, 3*time.Second)
			if !strings.Contains(err.Error(), "session/set_mode failed:") || !strings.Contains(err.Error(), tc.cause) || strings.Contains(err.Error(), "SYNTHETIC-SECRET") {
				t.Fatalf("wrong sanitized cause: %v", err)
			}
			if strings.Contains(err.Error(), "remove --plan, or --mode and its value, then retry") != tc.remedy {
				t.Fatalf("mode-removal authority=%v: %v", tc.remedy, err)
			}
			if len(c.prompts(t)) != 0 {
				t.Fatal("mode failure admitted prompt")
			}
			if bytes.Contains(c.pane.bytes(), []byte("SYNTHETIC-SECRET")) {
				t.Fatal("raw runtime diagnostic leaked")
			}
		})
	}
}
