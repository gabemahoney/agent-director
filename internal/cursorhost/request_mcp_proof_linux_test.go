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

// mcpPermission uses the captured MCP title shape with synthetic fixture identities.
func mcpPermission(session, tool string) json.RawMessage {
	return bytes.Replace(cursorfix.Permission(session, tool, "other"), []byte(`"Synthetic action"`), []byte(`"agent-director-spawn: spawn"`), 1)
}

func TestCursorCompletedMCPReleasesOnlyMatchingDeliveredHold(t *testing.T) {
	// Serial: shared verified pane fixture sets HOME and PATH.
	e := newRequestEnv(t, "on", "98040ced-8fdd-478c-8f7d-7719b7b58ec1")
	wire := &observedWire{}
	c := e.controller(t, wire, io.Discard)
	var requests []*pendingAuthorization
	for i, tool := range []string{"toolu_01VtjQdJGpqo4pUjG3S82Xxb", "toolu_01SjL1UBDe4Wjhf8BbrRiMGd", "unrelated-shell"} {
		params := mcpPermission(e.binding.SessionID, tool)
		if i == 2 {
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
			t.Fatalf("allow delivery must retain the permission hold: %+v", got)
		}
	}
	if bytes.Count(wire.bytes(), []byte(`"optionId":"allow-once"`)) != 3 {
		t.Fatalf("missing independent allow-once replies: %s", wire.bytes())
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
	tc := tmuxfix.NewRecorder().StartServer(li.Socket, tmuxfix.Server{PID: li.ServerPID, Start: li.ServerStart, ProcStart: li.ServerStarttime}).
		SeedSessions(li.Socket, tmuxfix.SeedSession{ID: "$0", Name: row.TmuxSessionName, Created: li.ServerStart,
			Label: tmux.Label{Kind: tmux.LabelValid, Token: li.Token, InstanceID: row.InstanceID, StoreID: e.s.StoreID()},
			Panes: []tmuxfix.SeedPane{{ID: li.PaneID, PID: li.PanePID, AdPane: li.Token}}})
	relay := api.RelayView{Procs: probe.NewProcChecker()}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	checkHeld := func() {
		t.Helper()
		if _, err := api.SendKeys(e.s, tc, api.SendKeysEnv{Relay: relay}, api.SendKeysParams{InstanceID: e.binding.InstanceID, Text: "continue"}); !errors.Is(err, api.ErrDialogMaybeOpen) {
			t.Fatalf("held public send-keys: %v", err)
		}
		if _, err := api.Pause(ctx, e.s, tc, api.PauseEnv{Relay: relay}, 5, api.PauseParams{InstanceID: e.binding.InstanceID}); !errors.Is(err, api.ErrDialogMaybeOpen) {
			t.Fatalf("held public pause: %v", err)
		}
		if calls := tc.SocketCalls(); len(calls) != 0 {
			t.Fatalf("held public input reached tmux: %+v", calls)
		}
	}
	checkHeld()
	for i, r := range requests {
		output := `{"success":true}`
		if i == 2 {
			output = `{"exitCode":0,"stdout":"","stderr":""}`
		}
		before := e.request(t, r.token)
		update := executionUpdate(e.binding.SessionID, r.auth.toolID, "completed", output)
		if err := c.execution(update); err != nil {
			t.Fatal(err)
		}
		got := e.request(t, r.token)
		wantReason := before.DecisionReason
		if wantReason == "" {
			wantReason = store.DecisionReasonToolRan
		}
		if !got.ProvenGone() || got.ProvenGoneHow != store.ProvenGoneToolRan || got.Decision != before.Decision || got.DecisionReason != wantReason || got.DeliveredAt != before.DeliveredAt || got.MainTurnEndEligible || got.AgentID != "" {
			t.Fatalf("matching completed tool did not retire only its hold: %+v", got)
		}
		if err := c.execution(update); err != nil || !reflect.DeepEqual(got, e.request(t, r.token)) {
			t.Fatalf("duplicate completion changed proof: %v", err)
		}
		for _, other := range requests[i+1:] {
			if got := e.request(t, other.token); got.ProvenGone() || got.MainTurnEndEligible || got.AgentID != "" {
				t.Fatalf("completion released unrelated request: %+v", got)
			}
		}
		if i < len(requests)-1 {
			checkHeld()
		}
	}
	if _, err := api.SendKeys(e.s, tc, api.SendKeysEnv{Relay: relay}, api.SendKeysParams{InstanceID: e.binding.InstanceID, Text: "continue"}); err != nil {
		t.Fatalf("public continuation after all proofs: %v", err)
	}
	// A cancelled wait proves pause sent its commands without inventing gateway exit.
	if _, err := api.Pause(ctx, e.s, tc, api.PauseEnv{Relay: relay}, 5, api.PauseParams{InstanceID: e.binding.InstanceID}); !errors.Is(err, context.Canceled) {
		t.Fatalf("public pause did not reach its post-delivery wait: %v", err)
	}
	texts, keys, enters := tc.SocketCallsOf(tmux.CallSendText), tc.SocketCallsOf(tmux.CallSendKey), tc.SocketCallsOf(tmux.CallSendEnter)
	if len(texts) != 2 || texts[0].Text != "continue" || texts[1].Text != "/exit" || len(keys) != 1 || keys[0].Key != "C-u" || keys[0].Repeat != 127 || len(enters) != 2 {
		t.Fatalf("public continuation/pause commands: %+v", tc.SocketCalls())
	}
}

func TestCursorMCPProofRejectsAmbiguousOutputAndLifecycle(t *testing.T) {
	// Serial: shared verified pane fixture sets HOME and PATH.
	e := newRequestEnv(t, "off", "synthetic-session")
	c := e.controller(t, &observedWire{}, io.Discard)
	r := e.admit(t, c, `1`, "session/request_permission", mcpPermission(e.binding.SessionID, "mcp"))
	plan := e.admit(t, c, `2`, "cursor/create_plan", []byte(`{"toolCallId":"plan","name":"Plan","overview":"Overview","plan":"Steps"}`))
	shell := e.admit(t, c, `3`, "session/request_permission", cursorfix.Permission(e.binding.SessionID, "shell", "execute"))
	read := e.admit(t, c, `4`, "session/request_permission", cursorfix.Permission(e.binding.SessionID, "read", "read"))
	valid := executionUpdate(e.binding.SessionID, "mcp", "completed", `{"success":true}`)
	for _, tc := range []struct{ name, status, output string }{
		{"missing output", "completed", ""}, {"null output", "completed", `null`}, {"empty output", "completed", `{}`},
		{"false success", "completed", `{"success":false}`}, {"null success", "completed", `{"success":null}`},
		{"string success", "completed", `{"success":"true"}`}, {"number success", "completed", `{"success":1}`},
		{"array output", "completed", `[true]`}, {"duplicate success", "completed", `{"success":false,"success":true}`},
		{"extra output", "completed", `{"success":true,"error":null}`},
		{"shell output", "completed", `{"exitCode":0,"stdout":"","stderr":""}`},
		{"failed status", "failed", `{"success":true}`}, {"pending status", "pending", `{"success":true}`},
		{"running status", "in_progress", `{"success":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var raw json.RawMessage
			if tc.output == "" {
				raw = bytes.Replace(valid, []byte(`"rawOutput":{"success":true},`), nil, 1)
			} else {
				raw = executionUpdate(e.binding.SessionID, "mcp", tc.status, tc.output)
			}
			if err := c.execution(raw); err != nil {
				t.Fatal(err)
			}
			if got := e.request(t, r.token); got.ProvenGone() || got.Closed() {
				t.Fatalf("ambiguous MCP output released hold: %+v", got)
			}
		})
	}
	for _, tc := range []struct {
		name string
		raw  json.RawMessage
	}{
		{"wrong session", executionUpdate("child-session", "mcp", "completed", `{"success":true}`)},
		{"wrong tool", executionUpdate(e.binding.SessionID, "child-tool", "completed", `{"success":true}`)},
		{"plan", executionUpdate(e.binding.SessionID, "plan", "completed", `{"success":true}`)},
		{"shell authorization", executionUpdate(e.binding.SessionID, "shell", "completed", `{"success":true}`)},
		{"read authorization", executionUpdate(e.binding.SessionID, "read", "completed", `{"success":true}`)},
		{"child completion", bytes.Replace(valid, []byte(`"tool_call_update"`), []byte(`"task_completed"`), 1)},
		{"duplicate envelope", bytes.Replace(valid, []byte(`"sessionId":`), []byte(`"sessionId":"old","sessionId":`), 1)},
		{"malformed envelope", append(append(json.RawMessage(nil), valid...), '}')},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := c.execution(tc.raw); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, event := range []string{"Stop", "Interrupt", "PostToolUse", "SubagentStop"} {
		gate := store.HookGate{Event: event, ParentPID: e.binding.GatewayPID, ParentStart: e.binding.GatewayStart, SessionID: e.binding.SessionID, Cursor: &e.binding}
		if _, err := e.s.ProveRequestsGone(e.binding.InstanceID, gate, store.RequestProof{How: store.ProvenGoneTurnEnd, RecordedBefore: time.Now().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	for _, request := range []*pendingAuthorization{r, plan, shell, read} {
		if got := e.request(t, request.token); got.ProvenGone() || got.Closed() || got.MainTurnEndEligible || got.AgentID != "" {
			t.Fatalf("generic evidence fabricated authority: %+v", got)
		}
	}
	if err := c.execution(valid); err != nil || !e.request(t, r.token).ProvenGone() {
		t.Fatalf("exact successful MCP completion failed after rejected evidence: %v", err)
	}
}

func TestCursorMCPProofRejectsStaleAuthority(t *testing.T) {
	// Serial: shared verified pane fixture sets HOME and PATH.
	queries := map[string]string{
		"session": `UPDATE spawns SET session_id='replacement'`, "connection": `UPDATE cursor_runtimes SET bound_connection_id='replacement'`,
		"launch token": `UPDATE spawns SET launch_token='2222222222222222'`, "life": `UPDATE spawns SET life_number=life_number+1`,
		"runtime": `UPDATE cursor_runtimes SET bound_runtime_start='replacement'`, "retired": `UPDATE cursor_runtimes SET bound_terminated=1`,
	}
	for _, mutation := range []string{"session", "connection", "launch token", "life", "runtime", "retired", "request token", "tool identity"} {
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
			default:
				e.exec(t, queries[mutation])
			}
			if err := c.execution(executionUpdate(e.binding.SessionID, r.auth.toolID, "completed", `{"success":true}`)); err == nil && mutation != "request token" && mutation != "tool identity" {
				t.Fatal("stale MCP proof reported success")
			}
			if !reflect.DeepEqual(before, e.request(t, token)) || !reflect.DeepEqual(unrelated, e.request(t, other.token)) {
				t.Fatal("stale MCP proof changed a permission request")
			}
		})
	}
}

func TestCursorMCPProofRejectsVerifiedSessionRotation(t *testing.T) {
	// Serial: shared verified runtime/pane fixture owns HOME and PATH.
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
	if err := stale.execution(executionUpdate(old.SessionID, "outgoing-mcp", "completed", `{"success":true}`)); err == nil {
		t.Fatal("rotated MCP proof reported success")
	}
	if !reflect.DeepEqual(before, e.request(t, r.token)) {
		t.Fatal("rotated MCP proof changed outgoing request")
	}
	bound, err := e.s.ReadCursorRuntime(old.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	e.binding, e.rep.binding = bound, bound
	current := e.controller(t, &observedWire{}, io.Discard)
	next := e.admit(t, current, `2`, "session/request_permission", mcpPermission(bound.SessionID, "current-mcp"))
	if err := current.execution(executionUpdate(bound.SessionID, "current-mcp", "completed", `{"success":true}`)); err != nil || !e.request(t, next.token).ProvenGone() {
		t.Fatalf("current MCP proof failed: %v", err)
	}
	if !reflect.DeepEqual(before, e.request(t, r.token)) {
		t.Fatal("current MCP proof cleared outgoing unknown-origin hold")
	}
}
