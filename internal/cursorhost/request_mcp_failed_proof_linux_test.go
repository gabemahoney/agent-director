//go:build linux

package cursorhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// assertFailedMCPProof permits only dialog proof to change, including on native or denied requests.
func assertFailedMCPProof(t *testing.T, before, after store.PermissionRow) {
	t.Helper()
	if !after.ProvenGone() || after.ProvenGoneHow != store.ProvenGoneToolRan {
		t.Fatalf("exact completed failed MCP invocation retained its hold: %+v", after)
	}
	after.ProvenGoneAt, after.ProvenGoneHow = before.ProvenGoneAt, before.ProvenGoneHow
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("failed invocation changed decision, delivery, pane or origin evidence:\nbefore=%+v\nafter=%+v", before, after)
	}
}

func TestCursorFailedMCPReleasesOnlyMatchingDeliveredHold(t *testing.T) {
	// Serial: shared verified pane fixture sets HOME and PATH.
	e := newRequestEnv(t, "on", "f455b0b8-0443-42cf-9592-d6ced68ca42f")
	wire := &observedWire{}
	c := e.controller(t, wire, io.Discard)
	var requests []*pendingAuthorization
	for i, tool := range []string{"toolu_0163fswiVjHKNqX9rydTkVDZ", "toolu_01Tti7rYCr5JjqCAeXtBHdd4", "toolu_01MYJdv5P1xscj2SXCjktcu9", "unrelated-shell"} {
		params := mcpPermission(e.binding.SessionID, tool)
		if i == 3 {
			params = cursorfix.Permission(e.binding.SessionID, tool, "execute")
		}
		r := e.admit(t, c, fmt.Sprint(i), "session/request_permission", params)
		requests = append(requests, r)
		e.decide(t, r.token, "allow")
		if err := c.tick(); err != nil {
			t.Fatal(err)
		}
		if err := completedWrite(t, c.protocol); err != nil {
			t.Fatal(err)
		}
		if got := e.request(t, r.token); got.DeliveredAt.IsZero() || got.Decision != "allow" || got.ProvenGone() {
			t.Fatalf("delivery must retain hold: %+v", got)
		}
	}
	if bytes.Count(wire.bytes(), []byte(`"optionId":"allow-once"`)) != 4 {
		t.Fatalf("missing exact delivered replies: %s", wire.bytes())
	}
	gate := store.HookGate{Event: "Stop", ParentPID: e.binding.GatewayPID, ParentStart: e.binding.GatewayStart, SessionID: e.binding.SessionID, Cursor: &e.binding}
	if result, err := e.s.ApplyHookTransition(e.binding.InstanceID, gate, store.StateWaiting, false, "Stop", e.tr.path, true); err != nil || !result.Applied {
		t.Fatalf("parent turn completion: %+v %v", result, err)
	}
	row, err := e.s.GetSpawn(e.binding.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	li := row.Identity
	tc := tmuxfix.NewRecorder().StartServer(li.Socket, tmuxfix.Server{PID: li.ServerPID, Start: li.ServerStart, ProcStart: li.ServerStarttime}).SeedSessions(li.Socket, tmuxfix.SeedSession{ID: "$0", Name: row.TmuxSessionName, Created: li.ServerStart, Label: tmux.Label{Kind: tmux.LabelValid, Token: li.Token, InstanceID: row.InstanceID, StoreID: e.s.StoreID()}, Panes: []tmuxfix.SeedPane{{ID: li.PaneID, PID: li.PanePID, AdPane: li.Token}}})
	relay := api.RelayView{Procs: probe.NewProcChecker()}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	checkHeld := func() {
		t.Helper()
		if _, err := api.SendKeys(e.s, tc, api.SendKeysEnv{Relay: relay}, api.SendKeysParams{InstanceID: e.binding.InstanceID, Text: "continue"}); !errors.Is(err, api.ErrDialogMaybeOpen) {
			t.Fatalf("held public send: %v", err)
		}
		if _, err := api.Pause(ctx, e.s, tc, api.PauseEnv{Relay: relay}, 5, api.PauseParams{InstanceID: e.binding.InstanceID}); !errors.Is(err, api.ErrDialogMaybeOpen) {
			t.Fatalf("held public pause: %v", err)
		}
		if calls := tc.SocketCalls(); len(calls) != 0 {
			t.Fatalf("held commands reached tmux: %+v", calls)
		}
	}
	checkHeld()
	for i, r := range requests[:3] {
		before := make([]store.PermissionRow, len(requests))
		for j, request := range requests {
			before[j] = e.request(t, request.token)
		}
		update := executionUpdate(e.binding.SessionID, r.auth.toolID, "completed", `{"error":"Tool execution error"}`)
		if err := c.execution(update); err != nil {
			t.Fatal(err)
		}
		got := e.request(t, r.token)
		assertFailedMCPProof(t, before[i], got)
		for j, request := range requests {
			if j != i && !reflect.DeepEqual(before[j], e.request(t, request.token)) {
				t.Fatalf("completion changed unrelated request %d", j)
			}
		}
		if err := c.execution(update); err != nil || !reflect.DeepEqual(got, e.request(t, r.token)) {
			t.Fatalf("duplicate failure changed proof: %v", err)
		}
		checkHeld()
	}
	shell := requests[3]
	if err := c.execution(executionUpdate(e.binding.SessionID, shell.auth.toolID, "completed", `{"exitCode":0,"stdout":"","stderr":""}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := api.SendKeys(e.s, tc, api.SendKeysEnv{Relay: relay}, api.SendKeysParams{InstanceID: e.binding.InstanceID, Text: "continue"}); err != nil {
		t.Fatalf("continuation after all proofs: %v", err)
	}
	// A cancelled pause wait proves command admission, without asserting gateway exit.
	if _, err := api.Pause(ctx, e.s, tc, api.PauseEnv{Relay: relay}, 5, api.PauseParams{InstanceID: e.binding.InstanceID}); !errors.Is(err, context.Canceled) {
		t.Fatalf("pause did not reach post-command wait: %v", err)
	}
	texts, keys, enters := tc.SocketCallsOf(tmux.CallSendText), tc.SocketCallsOf(tmux.CallSendKey), tc.SocketCallsOf(tmux.CallSendEnter)
	if len(texts) != 2 || texts[0].Text != "continue" || texts[1].Text != "/exit" || len(keys) != 1 || keys[0].Key != "C-u" || keys[0].Repeat != 127 || len(enters) != 2 {
		t.Fatalf("public commands: %+v", tc.SocketCalls())
	}
}

func TestCursorFailedMCPProofPreservesAnswerEvidence(t *testing.T) {
	// Serial: shared verified pane fixture sets HOME and PATH.
	for _, state := range []string{"undecided", "native allow", "native deny", "delivered deny"} {
		t.Run(state, func(t *testing.T) {
			relay := "off"
			if state == "delivered deny" {
				relay = "on"
			}
			e := newRequestEnv(t, relay, "synthetic-session")
			wire := &observedWire{}
			c := e.controller(t, wire, io.Discard)
			r := e.admit(t, c, `1`, "session/request_permission", mcpPermission(e.binding.SessionID, "mcp"))
			switch state {
			case "native allow":
				c.choose(r.token, "allow-once")
			case "native deny":
				c.choose(r.token, "reject-once")
			case "delivered deny":
				e.decide(t, r.token, "deny")
			}
			if state != "undecided" {
				if err := c.tick(); err != nil {
					t.Fatal(err)
				}
				if err := completedWrite(t, c.protocol); err != nil {
					t.Fatal(err)
				}
			}
			before, sent := e.request(t, r.token), wire.bytes()
			update := executionUpdate(e.binding.SessionID, "mcp", "completed", `{"error":"Tool execution error"}`)
			if err := c.execution(update); err != nil {
				t.Fatal(err)
			}
			got := e.request(t, r.token)
			assertFailedMCPProof(t, before, got)
			if err := c.execution(update); err != nil || !reflect.DeepEqual(got, e.request(t, r.token)) || !bytes.Equal(sent, wire.bytes()) {
				t.Fatalf("failure replay mutated proof or sent a reply: %v", err)
			}
		})
	}
}

func TestCursorFailedMCPProofRejectsAmbiguousEvidence(t *testing.T) {
	// Serial: shared verified pane fixture sets HOME and PATH.
	e := newRequestEnv(t, "off", "synthetic-session")
	c := e.controller(t, &observedWire{}, io.Discard)
	requests := []*pendingAuthorization{
		e.admit(t, c, `1`, "session/request_permission", mcpPermission(e.binding.SessionID, "mcp")),
		e.admit(t, c, `2`, "cursor/create_plan", []byte(`{"toolCallId":"plan","name":"Plan","overview":"Overview","plan":"Steps"}`)),
		e.admit(t, c, `3`, "session/request_permission", cursorfix.Permission(e.binding.SessionID, "shell", "execute")),
		e.admit(t, c, `4`, "session/request_permission", cursorfix.Permission(e.binding.SessionID, "read", "read")),
	}
	before := make([]store.PermissionRow, len(requests))
	for i, r := range requests {
		before[i] = e.request(t, r.token)
	}
	valid := executionUpdate(e.binding.SessionID, "mcp", "completed", `{"error":"Tool execution error"}`)
	cases := []struct {
		name string
		raw  json.RawMessage
	}{
		{"missing output", bytes.Replace(valid, []byte(`"rawOutput":{"error":"Tool execution error"},`), nil, 1)},
		{"status only", []byte(fmt.Sprintf(`{"sessionId":%q,"update":{"sessionUpdate":"tool_call_update","toolCallId":"mcp","status":"completed"}}`, e.binding.SessionID))},
		{"wrong session", executionUpdate("child-session", "mcp", "completed", `{"error":"Tool execution error"}`)},
		{"unrelated tool", executionUpdate(e.binding.SessionID, "child-tool", "completed", `{"error":"Tool execution error"}`)},
		{"child completion", bytes.Replace(valid, []byte(`"tool_call_update"`), []byte(`"task_completed"`), 1)},
		{"duplicate envelope", bytes.Replace(valid, []byte(`"sessionId":`), []byte(`"sessionId":"old","sessionId":`), 1)},
		{"malformed envelope", append(append(json.RawMessage(nil), valid...), '}')},
	}
	for _, output := range []string{`null`, `{}`, `[]`, `{"error":null}`, `{"error":true}`, `{"error":1}`, `{"error":{}}`, `{"error":""}`, `{"error":"Other failure"}`, `{"error":"other","error":"Tool execution error"}`, `{"error":"Tool execution error","success":false}`, `{"error":"Tool execution error","extra":null}`} {
		cases = append(cases, struct {
			name string
			raw  json.RawMessage
		}{output, executionUpdate(e.binding.SessionID, "mcp", "completed", output)})
	}
	for _, status := range []string{"failed", "pending", "in_progress"} {
		cases = append(cases, struct {
			name string
			raw  json.RawMessage
		}{status, executionUpdate(e.binding.SessionID, "mcp", status, `{"error":"Tool execution error"}`)})
	}
	for _, tool := range []string{"plan", "shell", "read"} {
		cases = append(cases, struct {
			name string
			raw  json.RawMessage
		}{tool, executionUpdate(e.binding.SessionID, tool, "completed", `{"error":"Tool execution error"}`)})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := c.execution(tc.raw); err != nil {
				t.Fatal(err)
			}
			for i, r := range requests {
				if !reflect.DeepEqual(before[i], e.request(t, r.token)) {
					t.Fatalf("ambiguous failure changed request %s", r.auth.toolID)
				}
			}
		})
	}
	for _, event := range []string{"Stop", "Interrupt", "PostToolUseFailure", "SubagentStop"} {
		gate := store.HookGate{Event: event, ParentPID: e.binding.GatewayPID, ParentStart: e.binding.GatewayStart, SessionID: e.binding.SessionID, Cursor: &e.binding}
		if _, err := e.s.ProveRequestsGone(e.binding.InstanceID, gate, store.RequestProof{How: store.ProvenGoneTurnEnd, RecordedBefore: time.Now().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		for i, r := range requests {
			if !reflect.DeepEqual(before[i], e.request(t, r.token)) {
				t.Fatalf("generic %s changed unknown-origin request", event)
			}
		}
	}
	if err := c.execution(valid); err != nil {
		t.Fatal(err)
	}
	assertFailedMCPProof(t, before[0], e.request(t, requests[0].token))
}

func TestCursorFailedMCPProofRejectsStaleAuthority(t *testing.T) {
	// Serial: shared verified pane fixture sets HOME and PATH.
	queries := map[string]string{
		"session": `UPDATE spawns SET session_id='replacement'`, "connection": `UPDATE cursor_runtimes SET bound_connection_id='replacement'`,
		"launch token": `UPDATE spawns SET launch_token='2222222222222222'`, "life": `UPDATE spawns SET life_number=life_number+1`,
		"runtime": `UPDATE cursor_runtimes SET bound_runtime_start='replacement'`, "retired": `UPDATE cursor_runtimes SET bound_terminated=1`,
		"input pipe": `UPDATE cursor_runtimes SET bound_input_pipe='replacement'`, "output pipe": `UPDATE cursor_runtimes SET bound_output_pipe='replacement'`,
		"input descriptor": `UPDATE cursor_runtimes SET bound_input_fd=bound_input_fd+1`, "output descriptor": `UPDATE cursor_runtimes SET bound_output_fd=bound_output_fd+1`,
		"pane": `UPDATE spawns SET pane_pid=pane_pid+1`,
	}
	for _, mutation := range []string{"session", "connection", "launch token", "life", "runtime", "retired", "input pipe", "output pipe", "input descriptor", "output descriptor", "pane", "request token", "tool identity", "kernel runtime death"} {
		t.Run(mutation, func(t *testing.T) {
			e := newRequestEnv(t, "off", "synthetic-session")
			c := e.controller(t, &observedWire{}, io.Discard)
			r := e.admit(t, c, `1`, "session/request_permission", mcpPermission(e.binding.SessionID, "mcp"))
			other := e.admit(t, c, `2`, "session/request_permission", mcpPermission(e.binding.SessionID, "unrelated"))
			token := r.token
			before, unrelated := e.request(t, token), e.request(t, other.token)
			switch mutation {
			case "request token":
				r.token = other.token
			case "tool identity":
				r.auth.toolID = "replacement-tool"
			case "kernel runtime death":
				e.process.terminate()
			default:
				e.exec(t, queries[mutation])
			}
			if err := c.execution(executionUpdate(e.binding.SessionID, r.auth.toolID, "completed", `{"error":"Tool execution error"}`)); err == nil && mutation != "request token" && mutation != "tool identity" {
				t.Fatal("stale failed proof reported success")
			}
			if !reflect.DeepEqual(before, e.request(t, token)) || !reflect.DeepEqual(unrelated, e.request(t, other.token)) {
				t.Fatal("stale failed completion changed a request")
			}
		})
	}
}

func TestCursorFailedMCPProofRejectsVerifiedSessionRotation(t *testing.T) {
	// Serial: shared verified runtime/pane fixture sets HOME and PATH.
	e := newRequestEnv(t, "off", "outgoing-session")
	old := e.binding
	stale := e.controller(t, &observedWire{}, io.Discard)
	r := e.admit(t, stale, `1`, "session/request_permission", mcpPermission(old.SessionID, "outgoing-mcp"))
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
		t.Fatalf("verified rotation %v %v", ok, err)
	}
	before := e.request(t, r.token)
	if err := stale.execution(executionUpdate(old.SessionID, "outgoing-mcp", "completed", `{"error":"Tool execution error"}`)); err == nil {
		t.Fatal("rotated failed proof reported success")
	}
	if !reflect.DeepEqual(before, e.request(t, r.token)) {
		t.Fatal("rotated failed proof changed outgoing request")
	}
	bound, err := e.s.ReadCursorRuntime(old.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	e.binding, e.rep.binding = bound, bound
	current := e.controller(t, &observedWire{}, io.Discard)
	next := e.admit(t, current, `2`, "session/request_permission", mcpPermission(bound.SessionID, "current-mcp"))
	nextBefore := e.request(t, next.token)
	if err := current.execution(executionUpdate(bound.SessionID, "current-mcp", "completed", `{"error":"Tool execution error"}`)); err != nil {
		t.Fatal(err)
	}
	assertFailedMCPProof(t, nextBefore, e.request(t, next.token))
	if !reflect.DeepEqual(before, e.request(t, r.token)) {
		t.Fatal("current failure cleared outgoing unknown-origin hold")
	}
}
