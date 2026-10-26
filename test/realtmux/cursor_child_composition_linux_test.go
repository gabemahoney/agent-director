//go:build linux

package realtmux_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// Reuse the caller-created child; no seeded row or second fixture stands in for it.
func cursorChildPermissionAcrossParentSwitch(t *testing.T, first *spawnFix, st *store.Store, public, cli *paneCaller, firstLaunch, beforeChild spawned, childPeer cursorfix.Runtime, parent apitest.SpawnColumns) func() {
	t.Helper()
	// A real independent child's permission survives unrelated parent work and a life switch.
	// The child's turn stays open until its tool runs, because a turn's end retires
	// its undecided requests (b.gwj).
	permissionScript := relayCapturedScenario(t)
	permissionScript.Requests = permissionScript.Requests[:1]
	permissionScript.WaitForAfterAnswersRelease = true
	var envelope struct {
		ToolCall struct {
			ID string `json:"toolCallId"`
		} `json:"toolCall"`
	}
	var requestFrame struct {
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(permissionScript.Requests[0], &requestFrame); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(requestFrame.Params, &envelope); err != nil {
		t.Fatal(err)
	}
	proof, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": permissionScript.SessionID, "update": map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": envelope.ToolCall.ID, "status": "completed", "rawOutput": map[string]any{"exitCode": 0, "stdout": "synthetic child execution", "stderr": ""}}}})
	if err != nil {
		t.Fatal(err)
	}
	permissionScript.AfterAnswers = []json.RawMessage{proof}
	childPeer.Configure(t, permissionScript)
	observed := observeRelayCommit(t, childPeer, first.DBPath)
	public.send(beforeChild.ID, "child permission across parent switch")
	request := relayRequests(t, first, beforeChild.ID, 1)[0]
	waitFor(t, "child turn open while permission remains held", func() bool {
		return paneRecordCount(t, readRow(t, first.DBPath, beforeChild.ID).JSONLPath.(string), "turn_outcome") == 1 && readRow(t, first.DBPath, beforeChild.ID).State == store.StateCheckPermission
	}, func() string { return fmt.Sprint(relayRequests(t, first, beforeChild.ID, 1)) })
	request = relayRequests(t, first, beforeChild.ID, 1)[0]
	var initialPermission api.GetPermissionResult
	if err := json.Unmarshal(public.must("get-permission", map[string]any{"request_token": request.RequestToken}), &initialPermission); err != nil {
		t.Fatal(err)
	}
	if request.InstanceID != beforeChild.ID || request.AgentID != "" || request.MainTurnEndEligible || !request.ProvenGoneAt.IsZero() || !request.DeliveredAt.IsZero() || request.Decision != "" || len(relayReplies(t, childPeer)) != 0 || initialPermission.Decision != nil || initialPermission.Delivery != "not_confirmed" || initialPermission.ConfirmBy.IsZero() || request.RequestToken == "" || initialPermission.RequestID != request.RequestID || initialPermission.ToolInput != string(requestFrame.Params) || initialPermission.HookAlive != nil {
		t.Fatalf("wrong child permission authority: %+v", request)
	}
	if _, err := first.API.Decide(api.DecideParams{InstanceID: firstLaunch.ID, RequestToken: request.RequestToken, Decision: "allow"}); !errors.Is(err, store.ErrNoOpenPermissionRequest) {
		t.Fatalf("parent decision on child token: %v", err)
	}
	public.send(firstLaunch.ID, "useful parent while child held")
	waitFor(t, "unrelated useful parent turn", func() bool {
		pane, err := first.API.ReadPane(api.ReadPaneParams{InstanceID: firstLaunch.ID, NLines: 100})
		return err == nil && strings.Contains(pane.Pane, "Synthetic reply: useful parent while child held") && readRow(t, first.DBPath, firstLaunch.ID).State == store.StateWaiting
	}, nil)
	if paneRecordCount(t, readRow(t, first.DBPath, beforeChild.ID).JSONLPath.(string), "turn_outcome") != 1 || readRow(t, first.DBPath, beforeChild.ID).State != store.StateCheckPermission {
		t.Fatal("parent work changed child's permission hold")
	}
	assertRelayPromptGuard(t, first, childPeer, beforeChild.ID, "parent work cannot release child")
	var cursorListing api.ListSubagentsResult
	if err := json.Unmarshal(public.must("list-subagents", map[string]any{"instance_id": firstLaunch.ID}), &cursorListing); err != nil || cursorListing.Tracked || cursorListing.Subagents == nil || len(cursorListing.Subagents) != 0 {
		t.Fatalf("Cursor listing: %+v %v", cursorListing, err)
	}
	childBeforeSwitch := readRow(t, first.DBPath, beforeChild.ID)
	childHistory, err := apitest.ReadSessionHistoryAllLives(first.DBPath, beforeChild.ID)
	if err != nil {
		t.Fatal(err)
	}
	childBeforeBinding, err := st.ReadCursorRuntime(beforeChild.ID)
	if err != nil {
		t.Fatal(err)
	}
	public.must("pause", map[string]any{"instance_id": firstLaunch.ID})
	codexProgram := program(t, "codex", codexStubScript)
	configPath := filepath.Join(os.Getenv("HOME"), ".config", "agent-director", "config.toml")
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	configBytes = []byte(strings.Replace(string(configBytes), "[launch]\n", fmt.Sprintf("[launch]\ncodex_path = %q\n", codexProgram), 1))
	if err := os.WriteFile(configPath, configBytes, 0600); err != nil {
		t.Fatal(err)
	}
	codexRecords := t.TempDir()
	cli.must("spawn", map[string]any{"instance_id": firstLaunch.ID, "agent": "codex", "cwd": first.CWD, "reuse_finished": true, "no_pre_trust": true, "extra_env": map[string]string{codexRecordDirEnv: codexRecords}})
	codexRecordOf(codexRecords, firstLaunch.ID).waitRecorded(t)
	switched := readRow(t, first.DBPath, firstLaunch.ID)
	if switched.Agent != "codex" || switched.LifeNumber != parent.LifeNumber.(int64)+1 || switched.SessionID != nil || switched.LaunchToken == parent.LaunchToken {
		t.Fatalf("legitimate parent switch: %+v", switched)
	}
	var codexListing api.ListSubagentsResult
	if err := json.Unmarshal(public.must("list-subagents", map[string]any{"instance_id": firstLaunch.ID}), &codexListing); err != nil || codexListing.Tracked || codexListing.Subagents == nil || len(codexListing.Subagents) != 0 {
		t.Fatalf("Codex listing: %+v %v", codexListing, err)
	}
	childAfterBinding, err := st.ReadCursorRuntime(beforeChild.ID)
	if err != nil || childAfterBinding != childBeforeBinding || readRow(t, first.DBPath, beforeChild.ID) != childBeforeSwitch {
		t.Fatal("parent switch changed independent child")
	}
	if !strings.Contains(string(public.must("list", map[string]any{"parent": firstLaunch.ID})), beforeChild.ID) {
		t.Fatal("parent switch lost child discovery")
	}
	public.must("get", map[string]any{"instance_id": beforeChild.ID})
	var heldPermission api.GetPermissionResult
	if err := json.Unmarshal(public.must("get-permission", map[string]any{"request_token": request.RequestToken}), &heldPermission); err != nil || heldPermission.ConfirmBy != initialPermission.ConfirmBy || heldPermission.Decision != nil || heldPermission.Delivery != "not_confirmed" {
		t.Fatalf("parent settled child: %+v %v", heldPermission, err)
	}
	held := relayRequests(t, first, beforeChild.ID, 1)[0]
	if held.RequestToken != request.RequestToken || held.Decision != "" || !held.ProvenGoneAt.IsZero() || !held.DeliveredAt.IsZero() || len(relayReplies(t, childPeer)) != 0 || paneRecordCount(t, childBeforeSwitch.JSONLPath.(string), "turn_outcome") != 1 || childBeforeSwitch.State != store.StateCheckPermission {
		t.Fatal("parent supplied child delivery/proof")
	}
	assertRelayPromptGuard(t, first, childPeer, beforeChild.ID, "parent switch cannot release child")
	public.must("decide", map[string]any{"instance_id": beforeChild.ID, "request_token": request.RequestToken, "decision": "allow"})
	var deliveredPermission api.GetPermissionResult
	if err := json.Unmarshal(public.must("get-permission", map[string]any{"request_token": request.RequestToken}), &deliveredPermission); err != nil || deliveredPermission.Delivery != "delivered" || deliveredPermission.ConfirmBy != initialPermission.ConfirmBy {
		t.Fatalf("child delivery/deadline: %+v %v", deliveredPermission, err)
	}
	waitFor(t, "actual child ACP answer", func() bool { return len(relayReplies(t, childPeer)) == 1 }, nil)
	if string(relayReplies(t, childPeer)[0]["id"]) != `"permission"` || !strings.Contains(string(relayReplies(t, childPeer)[0]["result"]), `"optionId":"allow-once"`) {
		t.Fatal("wrong child ACP answer")
	}
	assertRelayCommittedAtRawResponse(t, childPeer, observed, map[string]string{`"permission"`: request.RequestToken})
	delivered := relayRequests(t, first, beforeChild.ID, 1)[0]
	if !delivered.ProvenGoneAt.IsZero() || delivered.DeliveredAt.IsZero() || delivered.RequestToken != request.RequestToken || delivered.Decision != "allow" || paneRecordCount(t, childBeforeSwitch.JSONLPath.(string), "turn_outcome") != 1 || readRow(t, first.DBPath, beforeChild.ID).State != store.StateCheckPermission {
		t.Fatalf("delivered child request lost its hold before execution proof: %+v", delivered)
	}
	assertRelayPromptGuard(t, first, childPeer, beforeChild.ID, "delivered alone cannot release child")
	parentAfterSwitch := readRow(t, first.DBPath, firstLaunch.ID)
	if err := os.WriteFile(childPeer.Release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	// The in-turn execution proof retires the request; the turn's Stop then reports waiting.
	waitFor(t, "child correlated tool execution and completed outcomes", func() bool {
		got := relayRequests(t, first, beforeChild.ID, 1)[0]
		return got.InstanceID == beforeChild.ID && got.RequestToken == request.RequestToken && got.RequestID == request.RequestID && got.ToolUseID == envelope.ToolCall.ID && got.ToolInput == request.ToolInput && !got.ProvenGoneAt.IsZero() && got.ProvenGoneHow == "tool_ran" && !got.DeliveredAt.IsZero() && got.Decision == "allow" && paneRecordCount(t, childBeforeSwitch.JSONLPath.(string), "turn_outcome") == 2 && readRow(t, first.DBPath, beforeChild.ID).State == store.StateWaiting
	}, nil)
	var provenPermission api.GetPermissionResult
	if err := json.Unmarshal(public.must("get-permission", map[string]any{"request_token": request.RequestToken}), &provenPermission); err != nil || provenPermission.ConfirmBy != initialPermission.ConfirmBy || provenPermission.Delivery != "delivered" || provenPermission.Decision == nil || *provenPermission.Decision != "allow" {
		t.Fatalf("child proof delivery/deadline: %+v %v", provenPermission, err)
	}
	assertIndependent := func() {
		t.Helper()
		got := readRow(t, first.DBPath, beforeChild.ID)
		for _, pair := range [][2]any{{got.ParentID, childBeforeSwitch.ParentID}, {got.Agent, childBeforeSwitch.Agent}, {got.SessionID, childBeforeSwitch.SessionID}, {got.LifeNumber, childBeforeSwitch.LifeNumber}, {got.LaunchToken, childBeforeSwitch.LaunchToken}, {got.PanePID, childBeforeSwitch.PanePID}, {got.PaneStarttime, childBeforeSwitch.PaneStarttime}, {got.JSONLPath, childBeforeSwitch.JSONLPath}, {got.TmuxSocket, childBeforeSwitch.TmuxSocket}, {got.TmuxServerPID, childBeforeSwitch.TmuxServerPID}, {got.TmuxServerStarttime, childBeforeSwitch.TmuxServerStarttime}} {
			if !reflect.DeepEqual(pair[0], pair[1]) {
				t.Fatalf("child identity changed: %v", pair)
			}
		}
		current, err := st.ReadCursorRuntime(beforeChild.ID)
		if err != nil || current != childBeforeBinding || current.Terminated || current.SessionID != permissionScript.SessionID || got.State == store.StateEnded || got.State == store.StateMissing || got.State == store.StatePending || got.LaunchToken != current.Token || got.LifeNumber != current.Life || got.PanePID != int64(current.GatewayPID) || got.PaneStarttime != current.GatewayStart {
			t.Fatalf("child current authority changed: %+v %v", current, err)
		}
		history, err := apitest.ReadSessionHistoryAllLives(first.DBPath, beforeChild.ID)
		if err != nil || !reflect.DeepEqual(history, childHistory) || readRow(t, first.DBPath, firstLaunch.ID) != parentAfterSwitch {
			t.Fatalf("child history or switched parent changed: %+v %v", history, err)
		}
		public.must("get", map[string]any{"instance_id": beforeChild.ID})
		if !strings.Contains(string(public.must("list", map[string]any{"parent": firstLaunch.ID})), beforeChild.ID) {
			t.Fatal("independent child discovery lost")
		}
	}
	assertIndependent()
	childPeer.Configure(t, cursorfix.Scenario{})
	return assertIndependent
}

// Invoke the shipped hook from the actual child runtime and observe guarded parent state.
func assertCursorChildWrongDestination(t *testing.T, first *spawnFix, st *store.Store, public *paneCaller, parentID string, child spawned, childPeer cursorfix.Runtime, parent apitest.SpawnColumns, binding store.CursorRuntime, bin string) {
	t.Helper()
	childPeer.Configure(t, cursorfix.Scenario{PromptHook: map[string]any{"program": bin, "args": []string{"hook", "--agent", "cursor"}, "env": map[string]string{"AGENT_DIRECTOR_INSTANCE_ID": parentID, "AGENT_DIRECTOR_AGENT": "cursor", "AGENT_DIRECTOR_CURSOR_STORE_PATH": first.DBPath}, "payload": map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": binding.SessionID, "cursor_connection": binding.Connection, "origin": "main", "transcript_path": parent.JSONLPath}}})
	parentBeforeClaim := readRow(t, first.DBPath, parentID)
	public.send(child.ID, "child work with wrong destination report")
	waitFor(t, "child actual report attempt and useful outcome", func() bool {
		return paneRecordCount(t, readRow(t, first.DBPath, child.ID).JSONLPath.(string), "turn_outcome") == 1 && readRow(t, first.DBPath, child.ID).State == store.StateWaiting
	}, func() string { return fmt.Sprint(childPeer.MCPEvents(t)) })
	var attempted bool
	childBinding, err := st.ReadCursorRuntime(child.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range childPeer.MCPEvents(t) {
		if string(event["event"]) == `"prompt_hook"` && string(event["runtime"]) == fmt.Sprint(childBinding.RuntimePID) && string(event["status"]) == "0" {
			attempted = true
		}
	}
	if !attempted || parentBeforeClaim != readRow(t, first.DBPath, parentID) || childBinding.GatewayPID == binding.GatewayPID || childBinding.Connection == binding.Connection {
		t.Fatalf("missing successful fail-open child-origin hook invocation or parent mutated: events=%v", childPeer.MCPEvents(t))
	}
}
