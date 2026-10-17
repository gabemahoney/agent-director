//go:build linux

package cursorhost

import (
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
)

func executionUpdate(session, tool, status, output string) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"sessionId": session, "update": map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": tool, "status": status, "rawOutput": json.RawMessage(output)}})
	return raw
}

func TestCursorExecutionProofRequiresMatchingShellResult(t *testing.T) {
	// Serial: shared verified pane fixture sets HOME and PATH.
	cases := []struct {
		name, kind, status, output string
		valid                      bool
	}{
		{"captured shell result", "execute", "completed", `{"exitCode":0,"stdout":"FTD_SHELL_ALLOWED","stderr":""}`, true},
		{"nonzero shell result synthetic", "execute", "failed", `{"exitCode":9,"stdout":"","stderr":"failure"}`, true},
		{"generic completion", "execute", "completed", `null`, false},
		{"generic failure", "execute", "failed", `{}`, false},
		{"still pending", "execute", "pending", `{"exitCode":0,"stdout":"","stderr":""}`, false},
		{"wrong authorization kind", "read", "completed", `{"exitCode":0,"stdout":"","stderr":""}`, false},
		{"null exit", "execute", "completed", `{"exitCode":null,"stdout":"","stderr":""}`, false},
		{"string exit", "execute", "completed", `{"exitCode":"0","stdout":"","stderr":""}`, false},
		{"fractional exit", "execute", "completed", `{"exitCode":0.5,"stdout":"","stderr":""}`, false},
		{"missing stdout", "execute", "completed", `{"exitCode":0,"stderr":""}`, false},
		{"null stdout", "execute", "completed", `{"exitCode":0,"stdout":null,"stderr":""}`, false},
		{"array stdout", "execute", "completed", `{"exitCode":0,"stdout":[],"stderr":""}`, false},
		{"null stderr", "execute", "completed", `{"exitCode":0,"stdout":"","stderr":null}`, false},
		{"duplicate output", "execute", "completed", `{"exitCode":0,"exitCode":1,"stdout":"","stderr":""}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newRequestEnv(t, "off", "synthetic-session")
			c := e.controller(t, &observedWire{}, io.Discard)
			target := e.admit(t, c, `1`, "session/request_permission", cursorfix.Permission(e.binding.SessionID, "shell", tc.kind))
			other := e.admit(t, c, `2`, "session/request_permission", cursorfix.Permission(e.binding.SessionID, "independent", "execute"))
			before, _ := e.s.GetSpawn(e.binding.InstanceID)
			if err := c.execution(executionUpdate(e.binding.SessionID, "shell", tc.status, tc.output)); err != nil {
				t.Fatal(err)
			}
			got := e.request(t, target.token)
			if got.ProvenGone() != tc.valid || tc.valid && got.ProvenGoneHow != store.ProvenGoneToolRan {
				t.Fatalf("execution proof mismatch %+v", got)
			}
			if other := e.request(t, other.token); other.ProvenGone() || other.Closed() || other.Decision != "" {
				t.Fatal("unrelated request lost its hold")
			}
			after, _ := e.s.GetSpawn(e.binding.InstanceID)
			if got.MainTurnEndEligible || got.AgentID != "" || after.SessionID != before.SessionID || after.Identity != before.Identity {
				t.Fatal("proof fabricated origin or changed main identity")
			}
		})
	}
}

func TestCursorProofAndAdmissionRejectStaleAuthority(t *testing.T) {
	// Serial: shared verified pane fixture sets HOME and PATH.
	for _, mutation := range []string{"session", "connection", "token", "life", "runtime", "retired"} {
		t.Run(mutation, func(t *testing.T) {
			e := newRequestEnv(t, "off", "synthetic-session")
			wire := &observedWire{}
			c := e.controller(t, wire, io.Discard)
			r := e.admit(t, c, `1`, "session/request_permission", cursorfix.Permission(e.binding.SessionID, "shell", "execute"))
			queries := map[string]string{"session": `UPDATE spawns SET session_id='replacement'`, "connection": `UPDATE cursor_runtimes SET bound_connection_id='replacement'`, "token": `UPDATE spawns SET launch_token='2222222222222222'`, "life": `UPDATE spawns SET life_number=life_number+1`, "runtime": `UPDATE cursor_runtimes SET bound_runtime_start='replacement'`, "retired": `UPDATE cursor_runtimes SET bound_terminated=1`}
			e.exec(t, queries[mutation])
			if err := c.execution(executionUpdate(e.binding.SessionID, "shell", "completed", `{"exitCode":0,"stdout":"","stderr":""}`)); err == nil {
				t.Fatal("stale proof reported success")
			}
			if e.request(t, r.token).ProvenGone() {
				t.Fatal("stale proof committed")
			}
			err := c.admit(frame{ID: []byte(`2`), Method: "session/request_permission", Params: cursorfix.Permission(e.binding.SessionID, "new", "execute")}, e.binding.SessionID)
			if err == nil || len(c.requests) != 1 || len(wire.bytes()) != 0 {
				t.Fatal("stale admission answered or mutated")
			}
		})
	}
}

func TestCursorRotatedAuthorityRejectsStaleReportAndPermissionProof(t *testing.T) {
	// Serial: shared verified runtime/pane fixture owns HOME and PATH.
	e := newRequestEnv(t, "off", "outgoing-session")
	old := e.binding
	stale := e.controller(t, &observedWire{}, io.Discard)
	request := e.admit(t, stale, `1`, "session/request_permission", cursorfix.Permission(old.SessionID, "outgoing-shell", "execute"))
	row, err := e.s.GetSpawn(old.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	gate := store.HookGate{Event: "SessionStart", ParentPID: old.GatewayPID, ParentStart: old.GatewayStart, SessionID: old.SessionID, Cursor: &old}
	check := func(row store.Spawn) error {
		if err := VerifyRuntime(old, false); err != nil {
			return err
		}
		return VerifyPane(e.s, row, config.Default())
	}
	if ok, err := e.s.RotateCursorSession(row, gate, "current-session", e.tr.path, time.Now().Add(time.Second), check); err != nil || !ok {
		t.Fatalf("verified real rotation %v %v", ok, err)
	}
	before, err := e.s.GetSpawn(old.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := e.s.ReadCursorRuntime(old.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	previous := e.request(t, request.token)
	// The captured outgoing binding reaches the real SQL report/permission guards.
	gate.Event = "UserPromptSubmit"
	if _, result, err := e.s.ApplyHookTransitionResult(old.InstanceID, gate, store.StateWorking, false, "UserPromptSubmit", e.tr.path, true); err != nil || result.Applied {
		t.Fatalf("stale report applied %+v %v", result, err)
	}
	if err := stale.execution(executionUpdate(old.SessionID, "outgoing-shell", "completed", `{"exitCode":0,"stdout":"obsolete","stderr":""}`)); err == nil {
		t.Fatal("stale proof accepted")
	}
	if err := stale.admit(frame{ID: []byte(`2`), Method: "session/request_permission", Params: cursorfix.Permission(old.SessionID, "obsolete", "execute")}, old.SessionID); err == nil {
		t.Fatal("stale permission admitted")
	}
	after, _ := e.s.GetSpawn(old.InstanceID)
	current, _ := e.s.ReadCursorRuntime(old.InstanceID)
	if !reflect.DeepEqual(before, after) || current != bound || !reflect.DeepEqual(previous, e.request(t, request.token)) {
		t.Fatal("stale authority mutated current target/request")
	}
	e.binding = bound
	e.rep.binding = bound
	legitimate := e.controller(t, &observedWire{}, io.Discard)
	next := e.admit(t, legitimate, `3`, "session/request_permission", cursorfix.Permission(bound.SessionID, "current-shell", "execute"))
	if err := legitimate.execution(executionUpdate(bound.SessionID, "current-shell", "completed", `{"exitCode":0,"stdout":"current","stderr":""}`)); err != nil {
		t.Fatal(err)
	}
	if !e.request(t, next.token).ProvenGone() {
		t.Fatal("current useful proof failed")
	}
}

func TestCursorUnknownRequestRejectsGenericLifecycleProof(t *testing.T) {
	// Serial: shared verified pane fixture sets HOME and PATH.
	e := newRequestEnv(t, "off", "synthetic-session")
	c := e.controller(t, &observedWire{}, io.Discard)
	r := e.admit(t, c, `1`, "session/request_permission", cursorfix.Permission(e.binding.SessionID, "shell", "execute"))
	plan := e.admit(t, c, `2`, "cursor/create_plan", []byte(`{"toolCallId":"plan","name":"Plan","overview":"Overview","plan":"Steps"}`))
	for _, event := range []string{"Stop", "Interrupt", "PostToolUse", "SubagentStop"} {
		gate := store.HookGate{Event: event, ParentPID: e.binding.GatewayPID, ParentStart: e.binding.GatewayStart, SessionID: e.binding.SessionID, Cursor: &e.binding}
		if _, err := e.s.ProveRequestsGone(e.binding.InstanceID, gate, store.RequestProof{How: store.ProvenGoneTurnEnd, RecordedBefore: time.Now().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		if got := e.request(t, r.token); got.ProvenGone() || got.PaneAnswered() {
			t.Fatalf("generic %s fabricated Cursor execution proof: %+v", event, got)
		}
	}
	for _, bad := range []json.RawMessage{
		executionUpdate("old-session", "shell", "completed", `{"exitCode":0,"stdout":"","stderr":""}`),
		executionUpdate(e.binding.SessionID, "different", "completed", `{"exitCode":0,"stdout":"","stderr":""}`),
		executionUpdate(e.binding.SessionID, "plan", "completed", `{"exitCode":0,"stdout":"","stderr":""}`),
		[]byte(fmt.Sprintf(`{"sessionId":%q,"update":{"sessionUpdate":"task_completed","toolCallId":"shell","status":"completed","rawOutput":{"exitCode":0,"stdout":"","stderr":""}}}`, e.binding.SessionID)),
	} {
		if err := c.execution(bad); err != nil {
			t.Fatal(err)
		}
	}
	for _, token := range []string{r.token, plan.token} {
		if got := e.request(t, token); got.ProvenGone() || got.Closed() || got.MainTurnEndEligible || got.AgentID != "" {
			t.Fatalf("generic lifecycle fabricated authority %+v", got)
		}
	}
}

func TestCursorGenericToolProofCannotBypassControllerValidation(t *testing.T) {
	// Serial: shared verified pane fixture sets HOME and PATH.
	for _, path := range []string{"PostToolUse close", "generic tool proof"} {
		t.Run(path, func(t *testing.T) {
			e := newRequestEnv(t, "off", "synthetic-session")
			c := e.controller(t, &observedWire{}, io.Discard)
			r := e.admit(t, c, `1`, "session/request_permission", cursorfix.Permission(e.binding.SessionID, "shell", "execute"))
			gate := store.HookGate{Event: "PostToolUse", ParentPID: e.binding.GatewayPID, ParentStart: e.binding.GatewayStart, SessionID: e.binding.SessionID, Cursor: &e.binding}
			var err error
			if path == "PostToolUse close" {
				_, err = e.s.CloseToolRanRequests(e.binding.InstanceID, gate, "shell", time.Now(), func(store.PermissionRow) bool { return true })
			} else {
				_, err = e.s.ProveRequestsGone(e.binding.InstanceID, gate, store.RequestProof{How: store.ProvenGoneToolRan, ToolUseID: "shell"})
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := e.request(t, r.token); got.ProvenGone() || got.PaneAnswered() {
				t.Fatalf("generic %s bypassed validated Cursor shell output: %+v", path, got)
			}
		})
	}
}
