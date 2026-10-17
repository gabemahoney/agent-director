//go:build linux

package cursorhost

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
)

func advertisedCursorModes() json.RawMessage {
	return json.RawMessage(`{"currentModeId":"agent","availableModes":[{"id":"agent","name":"Agent"},{"id":"plan","name":"Plan"},{"id":"ask","name":"Ask"}]}`)
}

func TestCursorModeAcknowledgementBeforePrompt(t *testing.T) {
	for _, mode := range []string{"agent", "plan", "ask"} {
		t.Run(mode, func(t *testing.T) {
			initial := "--resume\n多行 🦋"
			before := []json.RawMessage{
				json.RawMessage(`{"id":1,"result":{}}`),
				mustE3JSON(t, map[string]any{"method": "session/update", "params": map[string]any{"sessionId": "synthetic-session-1", "update": map[string]any{"sessionUpdate": "current_mode_update", "currentModeId": mode}}}),
				json.RawMessage(`{"method":"session/update","params":{"sessionId":"synthetic-session-1","update":{"sessionUpdate":"current_mode_update","currentModeId":"child-mode","agentId":"child"}}}`),
			}
			opened := make(chan string, 1)
			opener := func(home string, binding store.CursorRuntime) (*transcript, error) {
				tr, err := openTranscript(home, binding)
				if err == nil {
					opened <- tr.path
				}
				return tr, err
			}
			c := beginCursorSessionWithOptions(t, Options{RequestedMode: mode, InitialPrompt: initial}, cursorfix.Scenario{Modes: advertisedCursorModes(), WaitForMode: true, BeforeModeReply: before}, opener, terminalLoop)
			var protectedPath string
			select {
			case protectedPath = <-opened:
			case <-time.After(3 * time.Second):
				t.Fatal("protected transcript not opened")
			}
			c.wait(t, "mode request", func() bool {
				for _, f := range c.e.runtime.Frames(t) {
					if string(f["method"]) == `"session/set_mode"` {
						return true
					}
				}
				return false
			})
			c.wait(t, "metadata and stale response quarantined", func() bool {
				raw, err := os.ReadFile(protectedPath)
				if err != nil {
					t.Fatal(err)
				}
				return bytes.Count(raw, []byte(`"kind":"session_configuration_update"`)) == 2 && bytes.Count(raw, []byte(`"kind":"retired_response"`)) == 1
			})
			row, err := c.e.s.GetSpawn(c.e.binding.InstanceID)
			if err != nil || row.SessionID != "" || len(c.prompts(t)) != 0 || bytes.Contains(c.pane.bytes(), []byte("Cursor ready")) {
				t.Fatalf("mode pending published readiness: row=%+v err=%v prompts=%q pane=%s", row, err, c.prompts(t), c.pane.bytes())
			}
			if err := os.WriteFile(c.e.runtime.Release, nil, 0600); err != nil {
				t.Fatal(err)
			}
			c.wait(t, "configured initial completed", func() bool { return bytes.Contains(c.pane.bytes(), []byte("Cursor turn complete")) })
			c.stop(t)
			var methods []string
			for _, f := range c.e.runtime.Frames(t) {
				if len(f["method"]) != 0 {
					methods = append(methods, string(f["method"]))
				}
				if string(f["method"]) == `"session/set_mode"` {
					var p struct {
						Session string `json:"sessionId"`
						Mode    string `json:"modeId"`
					}
					if err := json.Unmarshal(f["params"], &p); err != nil || p.Session != "synthetic-session-1" || p.Mode != mode {
						t.Fatalf("mode target=%s err=%v", f["params"], err)
					}
				}
			}
			if got := strings.Join(methods, ","); got != `"initialize","session/new","session/set_mode","session/prompt"` {
				t.Fatalf("mode/prompt order: %s", got)
			}
			if got := c.prompts(t); len(got) != 1 || got[0] != initial {
				t.Fatalf("initial replay=%q", got)
			}
		})
	}
}

func TestCursorModeRefusalNeverAdmitsInitial(t *testing.T) {
	for _, tc := range []struct {
		name         string
		modes, reply json.RawMessage
		before       []json.RawMessage
		want         string
	}{
		{"missing state", nil, nil, nil, "mode state unavailable"},
		{"unsupported mode", json.RawMessage(`{"currentModeId":"agent","availableModes":[{"id":"agent","name":"Agent"}]}`), nil, nil, "mode unavailable"},
		{"duplicate modes", json.RawMessage(`{"currentModeId":"plan","availableModes":[{"id":"plan","name":"Plan"},{"id":"plan","name":"Again"}]}`), nil, nil, "invalid Cursor session mode state"},
		{"null acknowledgement", advertisedCursorModes(), json.RawMessage(`{"result":null}`), nil, "invalid Cursor mode acknowledgement"},
		{"array acknowledgement", advertisedCursorModes(), json.RawMessage(`{"result":[]}`), nil, "invalid Cursor mode acknowledgement"},
		{"scalar acknowledgement", advertisedCursorModes(), json.RawMessage(`{"result":"plan"}`), nil, "invalid Cursor mode acknowledgement"},
		{"contradictory mode", advertisedCursorModes(), json.RawMessage(`{"result":{"modeId":"ask"}}`), nil, "acknowledgement mismatch"},
		{"contradictory current", advertisedCursorModes(), json.RawMessage(`{"result":{"currentModeId":"agent"}}`), nil, "acknowledgement mismatch"},
		{"contradictory session", advertisedCursorModes(), json.RawMessage(`{"result":{"sessionId":"child"}}`), nil, "acknowledgement mismatch"},
		{"RPC rejected", advertisedCursorModes(), json.RawMessage(`{"error":{"code":-32601,"message":"unsupported"}}`), nil, "requested mode configuration failed"},
		{"main contradiction", advertisedCursorModes(), nil, []json.RawMessage{json.RawMessage(`{"method":"session/update","params":{"sessionId":"synthetic-session-1","update":{"sessionUpdate":"current_mode_update","currentModeId":"ask"}}}`)}, "contradicts requested session mode"},
		{"uncorrelated response ID", advertisedCursorModes(), json.RawMessage(`{"id":999,"result":{}}`), nil, "uncorrelated ACP response"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := beginCursorSessionWithOptions(t, Options{RequestedMode: "plan", InitialPrompt: "never admit"}, cursorfix.Scenario{Modes: tc.modes, ModeReply: tc.reply, BeforeModeReply: tc.before}, openTranscript, terminalLoop)
			err := c.awaitFailure(t, 3*time.Second)
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("mode refusal=%v; want %q", err, tc.want)
			}
			if len(c.prompts(t)) != 0 || bytes.Contains(c.pane.bytes(), []byte("Cursor ready")) {
				t.Fatalf("failed configuration admitted prompt/readiness: %q %s", c.prompts(t), c.pane.bytes())
			}
			row, rowErr := c.e.s.GetSpawn(c.e.binding.InstanceID)
			if rowErr != nil || row.SessionID != "" {
				t.Fatalf("failed configuration published session: %+v %v", row, rowErr)
			}
		})
	}
}

func TestCursorModeRotationConfirmsBeforePromotionWithoutInitialReplay(t *testing.T) {
	input := &completeInput{ready: make(chan struct{})}
	c := beginCursorSessionWithOptions(t, Options{RequestedMode: "plan", InitialPrompt: "initial once"}, cursorfix.Scenario{Modes: advertisedCursorModes()}, openTranscript, input.start)
	c.wait(t, "initial controller completion", func() bool { return bytes.Contains(c.pane.bytes(), []byte("Cursor turn complete")) })
	before, err := c.e.s.GetSpawn(c.e.binding.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	c.e.runtime.Configure(t, cursorfix.Scenario{Modes: advertisedCursorModes(), WaitForMode: true})
	input.emit(terminalInput{text: "/new"})
	c.wait(t, "rotation mode pending", func() bool {
		count := 0
		for _, f := range c.e.runtime.Frames(t) {
			if string(f["method"]) == `"session/set_mode"` {
				count++
			}
		}
		return count == 2
	})
	c.keys(t, "held after rotation\r")
	c.wait(t, "rotation input held", func() bool { return rotationRecordCount(c, t, "input_held") == 1 })
	row, err := c.e.s.GetSpawn(c.e.binding.InstanceID)
	if err != nil || row.SessionID != before.SessionID || len(rotationPrompts(c, t)) != 1 || rotationNewCount(c, t) != 2 {
		t.Fatalf("mode pending published/replayed: row=%+v err=%v prompts=%v new=%d", row, err, rotationPrompts(c, t), rotationNewCount(c, t))
	}
	rotationRelease(t, c.e.runtime.Release)
	c.wait(t, "held prompt controller completion", func() bool { return bytes.Count(c.pane.bytes(), []byte("Cursor turn complete")) == 2 })
	c.stop(t)
	got := rotationPrompts(c, t)
	if len(got) != 2 || got[0] != (rotationPrompt{"synthetic-session-1", "initial once"}) || got[1] != (rotationPrompt{"synthetic-session-2", "held after rotation"}) {
		t.Fatalf("rotation prompt ownership/replay: %+v", got)
	}
	var methods []string
	for _, f := range c.e.runtime.Frames(t) {
		if len(f["method"]) > 0 {
			methods = append(methods, string(f["method"]))
		}
	}
	if got := strings.Join(methods, ","); got != `"initialize","session/new","session/set_mode","session/prompt","session/new","session/set_mode","session/prompt"` {
		t.Fatalf("rotation mode ordering/creation count: %s", got)
	}
}

func TestCursorModeInterruptDuringFinalVerification(t *testing.T) {
	input := &completeInput{ready: make(chan struct{})}
	var calls atomic.Int32
	options := Options{RequestedMode: "plan", InitialPrompt: "never admit"}
	options.verifyModeRuntime = func(binding store.CursorRuntime, terminated bool) error {
		err := VerifyRuntime(binding, terminated)
		if err == nil && calls.Add(1) == 2 {
			input.emit(terminalInput{interrupt: true})
		}
		return err
	}
	c := beginCursorSessionWithOptions(t, options, cursorfix.Scenario{Modes: advertisedCursorModes()}, openTranscript, input.start)
	select {
	case <-c.done:
		c.ended = true
	case <-time.After(4 * time.Second):
		t.Fatal("interrupted configuration exceeded original operation bound")
	}
	if calls.Load() < 2 {
		t.Fatalf("final verification not reached: %d", calls.Load())
	}
	for _, f := range c.e.runtime.Frames(t) {
		if string(f["method"]) == `"session/set_mode"` {
			t.Fatalf("mode frame escaped after interrupt during final verification: %s", f["params"])
		}
	}
	raw, err := os.ReadFile(c.e.runtime.Raw)
	if err != nil || bytes.Contains(raw, []byte(`"method":"session/set_mode"`)) {
		t.Fatalf("retired mode escaped onto raw transport: %s %v", raw, err)
	}
	if len(c.prompts(t)) != 0 || bytes.Contains(c.pane.bytes(), []byte("Cursor ready")) {
		t.Fatal("retired mode configuration admitted initial/readiness")
	}
}

func acceptedCursorPlan(t *testing.T, options Options) (*cursorSession, cursorfix.Authorization) {
	t.Helper()
	plan := e3Plan(t)
	scenario := cursorfix.Scenario{Modes: advertisedCursorModes(), Requests: []json.RawMessage{mustE3JSON(t, frame{JSONRPC: "2.0", ID: plan.ID, Method: plan.Method, Params: plan.Params})}, AfterAnswers: []json.RawMessage{json.RawMessage(`{"method":"session/update","params":{"sessionId":"synthetic-session-1","update":{"sessionUpdate":"current_mode_update","currentModeId":"agent"}}}`)}}
	options.RequestedMode, options.InitialPrompt = "plan", "plan approval"
	c := beginCursorSessionWithOptions(t, options, scenario, openTranscript, terminalLoop)
	t.Cleanup(func() {
		if t.Failed() {
			c.captureFailure(t, "plan mode approval/shutdown")
		}
	})
	keyPattern := regexp.MustCompile(`\[([^]]+)\] Accept plan`)
	var key string
	c.wait(t, "current plan choice", func() bool {
		match := keyPattern.FindSubmatch(c.pane.bytes())
		if len(match) != 2 {
			return false
		}
		key = string(match[1])
		return true
	})
	c.keys(t, key)
	c.wait(t, "approved plan completed after mode change", func() bool { return bytes.Contains(c.pane.bytes(), []byte("Cursor turn complete")) })
	return c, plan
}

func TestCursorPlanApprovalMayChangeModeAfterStartup(t *testing.T) {
	c, plan := acceptedCursorPlan(t, Options{})
	c.stop(t)
	modes, accepted := 0, false
	for _, f := range c.e.runtime.Frames(t) {
		if string(f["method"]) == `"session/set_mode"` {
			modes++
		}
		if string(f["id"]) == string(plan.ID) && bytes.Contains(f["result"], []byte(`"outcome":"accepted"`)) {
			accepted = true
		}
	}
	if modes != 1 || !accepted || len(c.prompts(t)) != 1 {
		t.Fatalf("plan approval/change re-forced startup mode or lost authority: set_mode=%d accepted=%v prompts=%q", modes, accepted, c.prompts(t))
	}
}

func TestCursorPlanRetirementExpiryKeepsMissingWithoutRequestProof(t *testing.T) {
	entered, release, released := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	c, _ := acceptedCursorPlan(t, Options{afterRuntimeRetired: func() {
		close(entered)
		<-release
		close(released)
	}})
	before, err := c.e.s.PermissionRequestsForSpawn(c.e.binding.InstanceID)
	if err != nil || len(before) != 1 {
		t.Fatalf("pre-exit plan request: %+v %v", before, err)
	}
	db, err := sql.Open("sqlite", c.e.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rawState := func() string {
		var state string
		if err := db.QueryRow("SELECT state FROM spawns WHERE instance_id=?", c.e.binding.InstanceID).Scan(&state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	start := time.Now()
	c.keys(t, "\x15/exit\r")
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("actual runtime retirement not reached")
	}
	b, err := c.e.s.ReadCursorRuntime(c.e.binding.InstanceID)
	if err != nil || !b.Terminated || VerifyRuntime(b, true) != nil {
		t.Fatalf("retirement lacks durable/kernel death: %+v %v", b, err)
	}
	err = c.awaitFailure(t, time.Until(start.Add(cancelWait+time.Second)))
	c.endErr = err
	seen, primary := c.controllerResult()
	row, rowErr := c.e.s.GetSpawn(b.InstanceID)
	t.Logf("retirement expiry primary=%v final=%v raw state=%s elapsed=%s", primary, err, rawState(), time.Since(start))
	if !seen || primary == nil || !strings.Contains(primary.Error(), "Cursor ACP operation timed out") || !strings.Contains(err.Error(), "Cursor ACP operation timed out") {
		t.Errorf("expiry lost primary timeout: observed=%v primary=%v final=%v", seen, primary, err)
	}
	if strings.Contains(err.Error(), "changed during close") || rowErr != nil || row.State != store.StateMissing || rawState() != store.StateMissing {
		t.Errorf("confirmed dead launch cleanup: state=%s read=%v final=%v", row.State, rowErr, err)
	}
	if time.Since(start) < cancelWait || time.Since(start) > cancelWait+time.Second {
		t.Errorf("exit exceeded original control bound: %s", time.Since(start))
	}
	if !strings.Contains(err.Error(), "phase=idle, report=SessionEnd") {
		t.Errorf("timeout phase/report missing: %v", err)
	}
	unblock()
	<-released
	row, rowErr = c.e.s.GetSpawn(b.InstanceID)
	if rowErr != nil || row.State != store.StateMissing || rawState() != store.StateMissing {
		t.Errorf("released worker changed missing: %+v %v", row, rowErr)
	}
	reports, reportErr := os.ReadFile(c.e.path + ".reports")
	if reportErr != nil || bytes.Contains(reports, []byte("SessionEnd")) || bytes.Contains(c.pane.bytes(), []byte("Cursor runtime terminated")) {
		t.Errorf("expired shutdown started helper or reported success: reports=%s err=%v pane=%s", reports, reportErr, c.pane.bytes())
	}
	after, proofErr := c.e.s.PermissionRequestsForSpawn(b.InstanceID)
	if proofErr != nil || !reflect.DeepEqual(before, after) {
		t.Errorf("runtime death changed plan proof/receipt: before=%+v after=%+v err=%v", before, after, proofErr)
	}
}
