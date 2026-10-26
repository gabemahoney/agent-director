//go:build linux

package realtmux_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
)

func assertPaneNoPromptBytes(t *testing.T, r cursorfix.Runtime, texts ...string) {
	t.Helper()
	raw := rawInput(t, r)
	complete, err := os.ReadFile(r.Trace)
	if err != nil {
		t.Fatal(err)
	}
	// Call after owned termination: readline cannot hide a partial prompt tail.
	if !bytes.Equal(raw, complete) {
		t.Fatalf("raw ACP differs from complete frames (partial or extra bytes): raw=%q frames=%q", raw, complete)
	}
	for _, text := range texts {
		encoded, _ := json.Marshal(text)
		if bytes.Contains(raw, encoded) {
			t.Fatalf("unadmitted/cancelled input reached raw ACP: %q raw=%s", text, raw)
		}
	}
}
func panePublicRow(t *testing.T, c *paneCaller, id string) api.SpawnRow {
	t.Helper()
	raw := c.must("get", map[string]any{"instance_id": id})
	var row api.SpawnRow
	if err := json.Unmarshal(raw, &row); err != nil {
		t.Fatalf("public inspection: %v %s", err, raw)
	}
	return row
}

// paneErrName is the err_name of c's failed verb call, or its whole response
// when it names none. The CLI and Bun envelopes carry err_name at the top;
// MCP carries it in the JSON-RPC error's data (docs/architecture.md "Error
// envelope").
func paneErrName(c *paneCaller, verb string, p map[string]any) string {
	raw, err := c.call(verb, p)
	var failure struct {
		Name  string `json:"err_name"`
		Error struct {
			Data struct {
				Name string `json:"err_name"`
			} `json:"data"`
		} `json:"error"`
	}
	json.Unmarshal(raw, &failure)
	for _, name := range []string{failure.Name, failure.Error.Data.Name} {
		if name != "" {
			return name
		}
	}
	return fmt.Sprintf("no err_name: response=%s err=%v", raw, err)
}
func paneRuntimePID(t *testing.T, r cursorfix.Runtime) int {
	t.Helper()
	var pid int
	if _, err := fmt.Sscan(readFileTrim(t, r.PID), &pid); err != nil {
		t.Fatal(err)
	}
	return pid
}

// Mixed public/terminal admissions are retired visibly before a control can dispatch them.
func TestCursorPaneControlsPublicMixedQueueRetirement(t *testing.T) {
	if testing.Short() {
		t.Skip("real pane public controls")
	}
	for _, tc := range []struct{ surface, control string }{{"MCP", "interrupt"}, {"Bun", "/new"}, {"CLI", "/exit"}} {
		t.Run(tc.surface+tc.control, func(t *testing.T) {
			f, r, launch := newCursorPane(t, false)
			id := launch.ID
			c := newPaneCaller(t, f, tc.surface, buildHookCLI(t, t.TempDir()))
			before := readRow(t, f.DBPath, id)
			path := before.JSONLPath.(string)
			pid := paneRuntimePID(t, r)
			sentinel := launch.rt.startSession(t, uniqueName())
			diag := func() string {
				return fmt.Sprintf("instance=%s control=%s records=%+v prompts=%+v row=%+v", id, tc.control, paneRecords(t, path), panePrompts(t, r), readRow(t, f.DBPath, id))
			}
			c.send(id, "HOLD")
			waitFor(t, "active prompt receipt", func() bool { return len(panePrompts(t, r)) == 1 }, diag)
			texts := []string{"terminal-cancelled 🐝", "public-cancelled\n別の行"}
			launch.rt.must(t, "send-keys", "-t", launch.Row.PaneID.(string), "-l", "--", texts[0])
			launch.rt.must(t, "send-keys", "-t", launch.Row.PaneID.(string), "Enter")
			waitFor(t, "terminal queue admitted", func() bool { return paneRecordCount(t, path, "submission_accepted") == 2 }, diag)
			c.send(id, texts[1])
			waitFor(t, "public queue admitted", func() bool { return paneRecordCount(t, path, "submission_accepted") == 3 }, diag)
			if tc.control == "interrupt" {
				launch.rt.must(t, "send-keys", "-t", launch.Row.PaneID.(string), "C-c")
			} else {
				c.send(id, tc.control)
			}
			waitForObserved(t, 3*time.Second, "both queued identities cancelled", func() bool { return paneRecordCount(t, path, "submission_cancelled") == 2 }, diag)
			accepted, cancelled := map[string]string{}, map[string]int{}
			for _, rec := range paneRecords(t, path) {
				if rec.Kind == "submission_accepted" {
					accepted[rec.Data.ID] = rec.Data.Text
				}
				if rec.Kind == "submission_cancelled" {
					cancelled[rec.Data.ID]++
				}
			}
			for submission, text := range accepted {
				if text == "HOLD" {
					continue
				}
				if cancelled[submission] != 1 {
					t.Fatalf("missing unique cancellation id=%s text=%q counts=%v", submission, text, cancelled)
				}
				waitFor(t, "visible cancellation "+submission, func() bool {
					var pane api.ReadPaneResult
					json.Unmarshal(c.must("read-pane", map[string]any{"instance_id": id, "n_lines": 200}), &pane)
					return strings.Contains(pane.Pane, "submission "+submission+" cancelled before dispatch")
				}, diag)
			}
			if err := os.WriteFile(r.Release, nil, 0600); err != nil {
				t.Fatal(err)
			}
			if tc.control == "/exit" {
				waitForObserved(t, 3*time.Second, "ended only after owned runtime death", func() bool { return pidGone(pid) && readRow(t, f.DBPath, id).State == store.StateEnded }, diag)
			} else {
				waitFor(t, "control confirmation", func() bool {
					row := readRow(t, f.DBPath, id)
					if row.State != store.StateWaiting {
						return false
					}
					return tc.control != "/new" || row.SessionID != before.SessionID
				}, diag)
				c.must("pause", map[string]any{"instance_id": id})
			}
			if got := panePrompts(t, r); len(got) != 1 || got[0].Text != "HOLD" {
				t.Fatalf("control/cancelled queue became model prompts: %+v", got)
			}
			assertPaneNoPromptBytes(t, r, append(texts, "/new", "/exit")...)
			for _, rec := range paneRecords(t, path) {
				if cancelled[rec.Data.ID] > 0 && (rec.Kind == "submission_started" || rec.Kind == "turn_outcome") {
					t.Fatalf("cancelled submission later executed: %+v", rec)
				}
			}
			if pidGone(sentinel.PanePID) {
				t.Fatal("control killed unrelated terminal worker")
			}
			t.Logf("instance=%s control=%s accepted=%v cancelled=%v", id, tc.control, accepted, cancelled)
		})
	}
}

// Questions, caller recreation and a cancellation alone cannot prove unknown-origin
// requests gone; the cancelled turn's completion does (b.gwj), and a new
// conversation keeps that proof.
func TestCursorPaneControlsPublicIndependentHoldsUntilTurnEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("real pane independent public holds")
	}
	script := relayCapturedScenario(t)
	script.CompleteBeforeAnswers = true
	script.WaitForRelease = true
	for _, q := range cursorfix.SourceDerivedQuestions(t, script.SessionID) {
		script.Requests = append(script.Requests, relayFrame(q.ID, q.Method, q.Params))
	}
	f, r, launch := newCursorPaneScenario(t, false, "", cursorPaneOptions{Script: &script, Relay: "on", Timeout: 20})
	id := launch.ID
	bin := buildHookCLI(t, t.TempDir())
	c := newPaneCaller(t, f, "MCP", bin)
	c.send(id, "permission plus plan plus denied questions")
	rows := relayRequests(t, f, id, 2)
	path := readRow(t, f.DBPath, id).JSONLPath.(string)
	diag := func() string {
		return fmt.Sprintf("instance=%s rows=%+v replies=%+v records=%+v", id, relayRequests(t, f, id, 2), relayReplies(t, r), paneRecords(t, path))
	}
	waitFor(t, "all question denials", func() bool { return len(relayReplies(t, r)) == 3 }, diag)
	for _, reply := range relayReplies(t, r) {
		result := string(reply["result"])
		if !strings.Contains(result, `"cancelled"`) && !strings.Contains(result, `"optionId":"__ask_question_skip__"`) {
			t.Fatalf("question authorized: %v", reply)
		}
	}
	inspect := func(retired bool) {
		t.Helper()
		got := panePublicRow(t, c, id)
		want := store.StateCheckPermission
		if retired {
			want = store.StateWaiting
		}
		if got.State != want {
			t.Fatalf("row state %s, want %s: %+v", got.State, want, got)
		}
		for _, original := range rows {
			raw := c.must("get-permission", map[string]any{"request_token": original.RequestToken})
			var request api.GetPermissionResult
			if err := json.Unmarshal(raw, &request); err != nil || request.ToolInput != original.ToolInput {
				t.Fatalf("request identity changed token=%s: %s %v", original.RequestToken, raw, err)
			}
		}
		for _, row := range relayRequests(t, f, id, 2) {
			if row.MainTurnEndEligible || row.ProvenGone() != retired || (retired && row.ProvenGoneHow != "turn_end") || row.Decision != "" || row.RequestToken == "" {
				t.Fatalf("unknown hold proof, want retired=%t: %+v", retired, row)
			}
		}
	}
	inspect(false)
	c.close()
	c = newPaneCaller(t, f, "MCP", bin)
	inspect(false)
	launch.rt.must(t, "send-keys", "-t", launch.Row.PaneID.(string), "C-c")
	waitFor(t, "actual cancellation notification", func() bool {
		for _, frame := range r.Frames(t) {
			if string(frame["method"]) == `"session/cancel"` {
				return true
			}
		}
		return false
	}, diag)
	// The peer holds its turn open until the release; only store reads come
	// before it, as the gateway's turn-end write must land within cancelWait (3 s).
	for _, row := range relayRequests(t, f, id, 2) {
		if row.ProvenGone() {
			t.Fatalf("cancellation alone proved a request gone: %+v", row)
		}
	}
	if state := readRow(t, f.DBPath, id).State; state != store.StateCheckPermission {
		t.Fatalf("cancellation alone moved the row to %s", state)
	}
	if err := os.WriteFile(r.Release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "cancelled turn retires its requests", func() bool {
		return paneRecordCount(t, path, "turn_outcome") == 1 && readRow(t, f.DBPath, id).State == store.StateWaiting
	}, diag)
	inspect(true)
	// Local controls remain accessible even though public ordinary prompts/pause are refused.
	r.Configure(t, cursorfix.Scenario{SessionID: script.SessionID})
	launch.rt.must(t, "send-keys", "-t", launch.Row.PaneID.(string), "-l", "--", "/new")
	launch.rt.must(t, "send-keys", "-t", launch.Row.PaneID.(string), "Enter")
	waitFor(t, "new conversation keeps the retired requests", func() bool { return readRow(t, f.DBPath, id).SessionID != script.SessionID }, diag)
	inspect(true)
	assertRetiredRefusal(t, rows, "public pause", func() string {
		return paneErrName(c, "pause", map[string]any{"instance_id": id})
	})
	assertRetiredRefusal(t, rows, "public send-keys", func() string {
		return paneErrName(c, "send-keys", map[string]any{"instance_id": id, "text": "retired-requests-refuse-input"})
	})
	launch.rt.must(t, "send-keys", "-t", launch.Row.PaneID.(string), "-l", "--", "/exit")
	launch.rt.must(t, "send-keys", "-t", launch.Row.PaneID.(string), "Enter")
	waitForObserved(t, 3*time.Second, "owned death before final guard raw observation", func() bool {
		return pidGone(paneRuntimePID(t, r)) && pidGone(int(launch.Row.PanePID.(int64)))
	}, diag)
	assertPaneNoPromptBytes(t, r, "retired-requests-refuse-input")
	if len(panePrompts(t, r)) != 1 {
		t.Fatal("request inspection/recreation/new session replayed prompt")
	}
	t.Logf("instance=%s retired unknown-origin tokens=%s/%s at the cancelled turn's end, kept across /new", id, rows[0].RequestToken, rows[1].RequestToken)
}

// The caller never consumes its send response; independent admission cannot become a replay.
func TestCursorPaneControlsPublicDisconnectBeforeObservingSend(t *testing.T) {
	if testing.Short() {
		t.Skip("real pane disconnected MCP caller")
	}
	f, r, launch := newCursorPane(t, false)
	id := launch.ID
	bin := buildHookCLI(t, t.TempDir())
	c := newPaneCaller(t, f, "MCP", bin)
	path := readRow(t, f.DBPath, id).JSONLPath.(string)
	diag := func() string {
		return fmt.Sprintf("instance=%s records=%+v prompts=%+v", id, paneRecords(t, path), panePrompts(t, r))
	}
	c.send(id, "HOLD")
	waitFor(t, "active turn receipt", func() bool { return len(panePrompts(t, r)) == 1 }, diag)
	unknown := startStdioMCP(t, bin, []string{"serve", "--stdio", "--store-path", f.DBPath}, os.Environ(), f.CWD)
	unknown.request("initialize", map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "disconnect-before-reply", "version": "1"}})
	text := "unobserved caller result 🐝\nnever replay automatically"
	request, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": "send-keys", "arguments": map[string]any{"instance_id": id, "text": text}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = unknown.in.Write(append(request, '\n')); err != nil {
		t.Fatal(err)
	}
	// Only the independent protected transcript is observed, not this caller's response channel.
	waitFor(t, "independent queued admission before caller loss", func() bool { return paneRecordCount(t, path, "submission_accepted") == 2 }, diag)
	unknown.stop()
	c.close()
	c = newPaneCaller(t, f, "MCP", bin)
	before := panePublicRow(t, c, id)
	if before.State != store.StateWorking || len(panePrompts(t, r)) != 1 {
		t.Fatalf("recreated caller changed active/queued work: %+v %s", before, diag())
	}
	if err := os.WriteFile(r.Release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "original queue completes without replay", func() bool {
		return paneRecordCount(t, path, "turn_outcome") == 2 && readRow(t, f.DBPath, id).State == store.StateWaiting
	}, diag)
	prompts := panePrompts(t, r)
	if len(prompts) != 2 || prompts[1].Text != text || prompts[1].Session != before.SessionID {
		t.Fatalf("unobserved result duplicated/rebound: %s", diag())
	}
	c.must("pause", map[string]any{"instance_id": id})
	if paneRecordCount(t, path, "submission_accepted") != 2 || paneRecordCount(t, path, "submission_started") != 2 {
		t.Fatalf("caller recreation replayed acceptance/dispatch: %s", diag())
	}
	t.Logf("instance=%s unobserved caller response; independent accepted=2 dispatched=2 completed=2; no replay", id)
}

// Each representative fault uses a different public surface, retaining the focused fault owners.
func TestCursorPaneControlsPublicFailureInspection(t *testing.T) {
	if testing.Short() {
		t.Skip("real pane public failure inspection")
	}
	for _, tc := range []struct{ surface, fault string }{{"MCP", "rejected rotation"}, {"Bun", "failed rotation publication"}, {"CLI", "terminal loss"}, {"Go", "protected transcript permission"}} {
		t.Run(tc.surface+tc.fault, func(t *testing.T) {
			f, r, launch := newCursorPane(t, false)
			id := launch.ID
			bin := buildHookCLI(t, t.TempDir())
			c := newPaneCaller(t, f, tc.surface, bin)
			before := readRow(t, f.DBPath, id)
			path := before.JSONLPath.(string)
			pid := paneRuntimePID(t, r)
			gateway := int(before.PanePID.(int64))
			sentinel := launch.rt.startSession(t, uniqueName())
			diag := func() string {
				return fmt.Sprintf("instance=%s fault=%s runtime=%s gateway=%s row=%+v records=%+v raw=%s", id, tc.fault, procState(pid), procState(gateway), readRow(t, f.DBPath, id), paneRecords(t, path), rawInput(t, r))
			}
			texts := []string{"held public never replay 🐝", "held terminal never replay"}
			if strings.Contains(tc.fault, "rotation") {
				scenario := cursorfix.Scenario{SessionID: before.SessionID.(string), WaitForNew: true}
				if tc.fault == "rejected rotation" {
					scenario.NewReplies = []json.RawMessage{json.RawMessage(`{"error":{"code":-32000,"message":"synthetic rejection"}}`)}
				}
				r.Configure(t, scenario)
				c.send(id, "/new")
				waitFor(t, "unconfirmed new request", func() bool {
					n := 0
					for _, frame := range r.Frames(t) {
						if string(frame["method"]) == `"session/new"` {
							n++
						}
					}
					return n == 2
				}, diag)
				c.send(id, texts[0])
				launch.rt.must(t, "send-keys", "-t", launch.Row.PaneID.(string), "-l", "--", texts[1])
				launch.rt.must(t, "send-keys", "-t", launch.Row.PaneID.(string), "Enter")
				waitFor(t, "both provisional records", func() bool { return paneRecordCount(t, path, "input_held") == 2 }, diag)
				// Disconnect while no confirmed replacement exists, then inspect without resubmitting.
				c.close()
				c = newPaneCaller(t, f, tc.surface, bin)
				if got := panePublicRow(t, c, id); got.SessionID != before.SessionID || len(got.PriorSessions) != 0 {
					t.Fatalf("caller restart fabricated rotation: %+v", got)
				}
				if tc.fault == "failed rotation publication" {
					storefix.InjectWriteFailure(t, f.DBPath, storefix.WriteFailReuseArchive, id)
				}
				if err := os.WriteFile(r.NewRelease, nil, 0600); err != nil {
					t.Fatal(err)
				}
			} else if tc.fault == "terminal loss" {
				launch.rt.must(t, "kill-pane", "-t", launch.Row.PaneID.(string))
			} else {
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
				c.send(id, texts[0])
			}
			waitForObserved(t, 3*time.Second, "owned gateway/runtime gone after failure", func() bool { return pidGone(gateway) && pidGone(pid) }, diag)
			c.close()
			c = newPaneCaller(t, f, tc.surface, bin)
			got := panePublicRow(t, c, id)
			durableEnd := false
			for _, rec := range paneRecords(t, path) {
				if rec.Kind == "session_end" {
					if !rec.Data.RuntimeGone || !pidGone(pid) {
						t.Fatalf("end without owned-death evidence: %+v", rec)
					}
					durableEnd = true
				}
			}
			if got.State == store.StateEnded && !durableEnd || got.SessionID != before.SessionID || len(got.PriorSessions) != 0 || got.JSONLPath != path {
				t.Fatalf("failed launch public inspection fabricated end/history: %+v", got)
			}
			if tc.fault != "terminal loss" && durableEnd || paneRecordCount(t, path, "submission_accepted") != 0 || paneRecordCount(t, path, "submission_started") != 0 {
				t.Fatalf("failed input gained durable execution/end: %s", diag())
			}
			assertPaneNoPromptBytes(t, r, texts...)
			if strings.Contains(tc.fault, "rotation") && paneRecordCount(t, path, "held_input_refused") != 2 {
				t.Fatalf("held identities not individually refused: %s", diag())
			}
			if tc.fault == "terminal loss" {
				c.must("find-missing", map[string]any{})
				reconciled := panePublicRow(t, c, id)
				if reconciled.State != store.StateMissing && !(reconciled.State == store.StateEnded && durableEnd) {
					t.Fatalf("public process reconciliation left dead launch live: %+v", reconciled)
				}
			}
			priorRaw := append([]byte(nil), rawInput(t, r)...)
			c.must("get", map[string]any{"instance_id": id})
			if !reflect.DeepEqual(priorRaw, rawInput(t, r)) || !pidGone(pid) {
				t.Fatal("public inspection replayed or recreated failed runtime")
			}
			if pidGone(sentinel.PanePID) {
				t.Fatal("failure cleanup killed unrelated worker")
			}
			t.Logf("instance=%s surface=%s fault=%s state=%s session=%s accepting runtime gone; no replay or false ended", id, tc.surface, tc.fault, got.State, got.SessionID)
		})
	}
}
