package realtmux_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
	"github.com/gabemahoney/agent-director/pkg/api"
)

func TestCursorRelayNativeFallbackClaims(t *testing.T) {
	// Serial: the shared real-pane fixture changes HOME, PATH and tmux environment.
	for _, mode := range []string{"off", "on"} {
		t.Run(mode, func(t *testing.T) {
			script := cursorfix.Scenario{SessionID: "native-session", Requests: []json.RawMessage{relayFrame("native", "session/request_permission", cursorfix.Permission("native-session", "native-tool", "execute"))}}
			f, r, launch := newCursorPaneScenario(t, false, "", cursorPaneOptions{Script: &script, Relay: mode, Timeout: 1})
			if _, e := f.API.SendKeys(api.SendKeysParams{InstanceID: launch.ID, Text: "native fallback"}); e != nil {
				t.Fatal(e)
			}
			row := relayRequests(t, f, launch.ID, 1)[0]
			// Relay-on timeout would acknowledge a deny; inject failed ack to preserve the
			// same open native request after relay settlement, rather than replay a queue.
			if mode == "on" {
				db, e := openRelayFaultDB(f.DBPath)
				if e != nil {
					t.Fatal(e)
				}
				defer db.Close()
			}
			assertRelayPromptGuard(t, f, r, launch.ID, "plain prompt must refuse")
			if _, e := f.API.Pause(context.Background(), api.PauseParams{InstanceID: launch.ID}); e == nil {
				t.Fatal("pause bypassed open request")
			} else if !errors.Is(e, api.ErrSpawnNotPausable) && !errors.Is(e, api.ErrSendKeysWhileRelayed) && !errors.Is(e, api.ErrRelayFallenBack) && !errors.Is(e, api.ErrDialogMaybeOpen) {
				t.Fatalf("unexpected pause refusal: %v", e)
			}
			timer := time.NewTimer(time.Until(row.SettledAt.Add(2100 * time.Millisecond)))
			defer timer.Stop()
			<-timer.C
			p, e := f.API.ReadPane(api.ReadPaneParams{InstanceID: launch.ID, NLines: 100})
			if e != nil {
				t.Fatal(e)
			}
			before := string(rawInput(t, r))
			params := api.SendKeysParams{InstanceID: launch.ID, RequestToken: row.RequestToken, As: "allow", Key: "1", NLines: 100, ExpectPaneSHA256: strings.Repeat("0", 64)}
			if _, e = f.API.SendKeys(params); e == nil {
				t.Fatal("stale pane hash accepted")
			}
			if string(rawInput(t, r)) != before {
				t.Fatal("stale hash emitted response bytes")
			}
			params.ExpectPaneSHA256 = p.PaneSHA256
			if _, e = f.API.SendKeys(params); e != nil {
				t.Fatal(e)
			}
			waitFor(t, "connected native once choice", func() bool { return len(relayReplies(t, r)) == 1 }, func() string { return fmt.Sprint(relayReplies(t, r)) })
			got := relayRequests(t, f, launch.ID, 1)[0]
			if !got.DeliveredAt.IsZero() || !got.ProvenGoneAt.IsZero() || got.PaneAnswer != "sent" {
				t.Fatalf("native claim became relay ack/proof: %+v", got)
			}
			if !strings.Contains(string(relayReplies(t, r)[0]["result"]), `"optionId":"allow-once"`) {
				t.Fatal("native selected wrong option")
			}
		})
	}
}

func TestCursorRelayQuestionPolicyPreservesGenuineRequest(t *testing.T) {
	// Serial: the shared real-pane fixture changes HOME, PATH and tmux environment.
	for _, tc := range []struct {
		disabled bool
		mode     string
	}{{false, "on"}, {true, "on"}, {false, "off"}, {true, "off"}} {
		t.Run(fmt.Sprintf("%v-%s", tc.disabled, tc.mode), func(t *testing.T) {
			const session = "question-policy-session"
			script := cursorfix.Scenario{SessionID: session, Requests: []json.RawMessage{relayFrame("genuine", "session/request_permission", cursorfix.Permission(session, "genuine-tool", "execute"))}}
			for _, q := range cursorfix.SourceDerivedQuestions(t, session) {
				script.Requests = append(script.Requests, relayFrame(q.ID, q.Method, q.Params))
			}

			f, r, launch := newCursorPaneScenario(t, tc.disabled, "", cursorPaneOptions{Script: &script, Relay: tc.mode, Timeout: 20})
			if _, e := f.API.SendKeys(api.SendKeysParams{InstanceID: launch.ID, Text: "questions plus genuine permission"}); e != nil {
				t.Fatal(e)
			}
			rows := relayRequests(t, f, launch.ID, 1)
			waitFor(t, "three source-derived question rejections", func() bool { return len(relayReplies(t, r)) == 3 }, func() string { return fmt.Sprint(relayReplies(t, r)) })
			for _, reply := range relayReplies(t, r) {
				result := string(reply["result"])
				if !strings.Contains(result, `"cancelled"`) && !strings.Contains(result, `"optionId":"__ask_question_skip__"`) {
					t.Fatalf("question not rejected: %v", reply)
				}
			}
			assertRelayPromptGuard(t, f, r, launch.ID, "must stay held")
			if _, e := f.API.Decide(api.DecideParams{InstanceID: launch.ID, RequestToken: "question-direct", Decision: "allow"}); e == nil {
				t.Fatal("question acquired approval authority")
			}
			if tc.mode == "on" {
				if _, e := f.API.Decide(api.DecideParams{InstanceID: launch.ID, RequestToken: rows[0].RequestToken, Decision: "allow"}); e != nil {
					t.Fatal(e)
				}
			} else {
				launch.rt.must(t, "send-keys", "-t", launch.Row.PaneID.(string), "-l", "--", "1")
			}
			waitFor(t, "independent genuine answer", func() bool { return len(relayReplies(t, r)) == 4 }, func() string { return fmt.Sprint(relayReplies(t, r)) })
			for _, frame := range r.Frames(t) {
				if string(frame["method"]) == `"session/cancel"` {
					t.Fatal("question denial cancelled independent permission")
				}
				if string(frame["method"]) == `"initialize"` && strings.Contains(string(frame["params"]), "askQuestion") {
					t.Fatal("advertised question capability")
				}
			}
		})
	}
}

func assertRelayPromptGuard(t *testing.T, f *spawnFix, r cursorfix.Runtime, id, text string) {
	t.Helper()
	if _, e := f.API.SendKeys(api.SendKeysParams{InstanceID: id, Text: text}); e == nil {
		waitFor(t, "gateway refuses ordinary input while request is open", func() bool {
			p, e := f.API.ReadPane(api.ReadPaneParams{InstanceID: id, NLines: 100})
			return e == nil && strings.Contains(p.Pane, "Cursor input refused while starting or busy; nothing sent")
		}, func() string { return fmt.Sprint(relayReplies(t, r)) })
	} else if !errors.Is(e, api.ErrSendKeysWhileRelayed) && !errors.Is(e, api.ErrRelayFallenBack) && !errors.Is(e, api.ErrDialogMaybeOpen) {
		t.Fatalf("unexpected prompt refusal: %v", e)
	}
	for _, frame := range r.Frames(t) {
		if strings.Contains(string(frame["params"]), text) {
			t.Fatal("guarded prompt reached ACP")
		}
	}
}

func TestCursorRelayUnknownOriginRequiresCorrelatedExecution(t *testing.T) {
	// Serial: the shared real-pane fixture changes HOME, PATH and tmux environment.
	const session = "proof-session"
	update := json.RawMessage(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"proof-session","update":{"sessionUpdate":"tool_call_update","toolCallId":"proof-tool","status":"completed","rawOutput":{"exitCode":0,"stdout":"synthetic proof","stderr":""}}}}`)
	script := cursorfix.Scenario{SessionID: session, WaitForRelease: true, Requests: []json.RawMessage{relayFrame("proof", "session/request_permission", cursorfix.Permission(session, "proof-tool", "execute"))}, AfterAnswers: []json.RawMessage{update}}
	f, r, launch := newCursorPaneScenario(t, false, "", cursorPaneOptions{Script: &script, Relay: "on", Timeout: 20})
	if _, e := f.API.SendKeys(api.SendKeysParams{InstanceID: launch.ID, Text: "correlated proof"}); e != nil {
		t.Fatal(e)
	}
	row := relayRequests(t, f, launch.ID, 1)[0]
	if row.MainTurnEndEligible || row.AgentID != "" {
		t.Fatal("unknown origin became proven main")
	}
	if _, e := f.API.Decide(api.DecideParams{InstanceID: launch.ID, RequestToken: row.RequestToken, Decision: "allow"}); e != nil {
		t.Fatal(e)
	}
	assertRelayPromptGuard(t, f, r, launch.ID, "no proof yet")
	if e := os.WriteFile(r.Release, nil, 0600); e != nil {
		t.Fatal(e)
	}
	waitFor(t, "validated actual gateway proof", func() bool { rows := relayRequests(t, f, launch.ID, 1); return rows[0].ProvenGoneHow == "tool_ran" }, func() string { return fmt.Sprint(relayRequests(t, f, launch.ID, 1)) })
	got := relayRequests(t, f, launch.ID, 1)[0]
	if got.MainTurnEndEligible || got.PaneAnswer != "none" || got.DecisionReason != "tool_ran" || got.Decision != "allow" {
		t.Fatalf("wrong proof fields: %+v", got)
	}
	if readRow(t, f.DBPath, launch.ID).SessionID != session {
		t.Fatal("tool proof changed main identity")
	}
}

func TestCursorRelayTurnEndKeepsIndependentPlan(t *testing.T) {
	// Serial: the shared real-pane fixture changes HOME, PATH and tmux environment.
	script := relayCapturedScenario(t)
	script.CompleteBeforeAnswers = true
	f, r, launch := newCursorPaneScenario(t, false, "", cursorPaneOptions{Script: &script, Relay: "on", Timeout: 20})
	if _, e := f.API.SendKeys(api.SendKeysParams{InstanceID: launch.ID, Text: "end before answers"}); e != nil {
		t.Fatal(e)
	}
	rows := relayRequests(t, f, launch.ID, 2)
	waitFor(t, "normal outcome while approvals remain open", func() bool {
		row := readRow(t, f.DBPath, launch.ID)
		raw, e := os.ReadFile(row.JSONLPath.(string))
		return e == nil && strings.Contains(string(raw), `"kind":"turn_outcome"`)
	}, func() string { return fmt.Sprint(relayRequests(t, f, launch.ID, 2)) })
	for _, row := range rows {
		if !row.ProvenGoneAt.IsZero() || row.Decision != "" || row.MainTurnEndEligible {
			t.Fatal("normal outcome released unknown-origin request")
		}
	}
	if readRow(t, f.DBPath, launch.ID).State != "check_permission" {
		t.Fatal("turn end cleared blocking plan state")
	}
	assertRelayPromptGuard(t, f, r, launch.ID, "plan and permission still held")
	for _, row := range rows {
		var p struct {
			Tool string `json:"toolCallId"`
		}
		json.Unmarshal([]byte(row.ToolInput), &p)
		if p.Tool != "" {
			if _, e := f.API.Decide(api.DecideParams{InstanceID: launch.ID, RequestToken: row.RequestToken, Decision: "deny"}); e != nil {
				t.Fatal(e)
			}
		}
	}
	waitFor(t, "only plan answered", func() bool { return len(relayReplies(t, r)) == 1 }, func() string { return fmt.Sprint(relayReplies(t, r)) })
	for _, row := range relayRequests(t, f, launch.ID, 2) {
		if row.Decision == "" && !row.DeliveredAt.IsZero() {
			t.Fatal("independent permission acquired plan acknowledgement")
		}
		if !row.ProvenGoneAt.IsZero() {
			t.Fatal("plan answer supplied execution proof")
		}
	}
	if pidGone(int(launch.Row.PanePID.(int64))) {
		t.Fatal("gateway died while native request remains open")
	}
}
