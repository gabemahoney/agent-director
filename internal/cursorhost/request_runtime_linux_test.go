//go:build linux

package cursorhost

import (
	"bytes"
	"encoding/json"
	"io"
	"regexp"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
)

func TestCursorRuntimeWaitsForIndependentPlanAndPermission(t *testing.T) {
	// Serial: shared verified pane fixture sets HOME and PATH.
	captures := cursorfix.Authorizations(t)
	var permission, plan cursorfix.Authorization
	for _, tc := range captures {
		if tc.Name == "execute" {
			permission = tc
		}
		if tc.Name == "plan" {
			plan = tc
		}
	}
	var env struct {
		Session string `json:"sessionId"`
	}
	if err := json.Unmarshal(permission.Params, &env); err != nil {
		t.Fatal(err)
	}
	e := newRequestEnv(t, "on", env.Session)
	var requests []json.RawMessage
	for _, tc := range []cursorfix.Authorization{permission, plan} {
		raw, err := json.Marshal(frame{JSONRPC: "2.0", ID: tc.ID, Method: tc.Method, Params: tc.Params})
		if err != nil {
			t.Fatal(err)
		}
		requests = append(requests, raw)
	}
	e.runtime.Configure(t, cursorfix.Scenario{SessionID: env.Session, Requests: requests})
	writer, err := e.process.protocolInput()
	if err != nil {
		t.Fatal(err)
	}
	p := newProtocol(writer, e.process.output)
	defer p.close()
	c := newRequestController(e.rep, p, io.Discard)
	for _, method := range []string{"initialize", "session/new"} {
		if _, err = p.request(method, nil); err != nil {
			t.Fatal(err)
		}
		if err = completedWrite(t, p); err != nil {
			t.Fatal(err)
		}
		f := nextFrame(t, p)
		if f.err != nil {
			t.Fatal(f.err)
		}
		if _, err = p.response(f.frame); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = p.request("session/prompt", map[string]any{"sessionId": env.Session, "prompt": []any{map[string]string{"type": "text", "text": "scripted"}}}); err != nil {
		t.Fatal(err)
	}
	if err = completedWrite(t, p); err != nil {
		t.Fatal(err)
	}
	var pendingPlan, pendingPermission *pendingAuthorization
	for i := 0; i < 2; i++ {
		f := nextFrame(t, p)
		if f.err != nil {
			t.Fatal(f.err)
		}
		r := e.admit(t, c, string(f.frame.ID), f.frame.Method, f.frame.Params)
		if r.auth.plan {
			pendingPlan = r
		} else {
			pendingPermission = r
		}
	}
	select {
	case f := <-p.frames:
		t.Fatalf("prompt unblocked before decisions: %+v", f)
	default:
	}
	c.choose(pendingPlan.token, "reject")
	if err = c.tick(); err != nil {
		t.Fatal(err)
	}
	if err = completedWrite(t, p); err != nil {
		t.Fatal(err)
	}
	if !e.request(t, pendingPermission.token).DeliveredAt.IsZero() {
		t.Fatal("plan answer acknowledged permission")
	}
	e.decide(t, pendingPermission.token, "allow")
	if err = c.tick(); err != nil {
		t.Fatal(err)
	}
	if err = completedWrite(t, p); err != nil {
		t.Fatal(err)
	}
	until := time.After(time.Second)
	for {
		select {
		case f := <-p.frames:
			if f.err != nil {
				t.Fatal(f.err)
			}
			if f.frame.Method == "" {
				if _, err = p.response(f.frame); err != nil {
					t.Fatal(err)
				}
				goto finished
			}
		case <-until:
			t.Fatal("runtime did not receive both independent answers")
		}
	}
finished:
	replies := 0
	for _, f := range e.runtime.Frames(t) {
		if len(f["result"]) > 0 {
			replies++
		}
	}
	if replies != 2 {
		t.Fatalf("peer received %d answers; want exactly one per request", replies)
	}
}

func TestCursorSessionDirectQuestionDoesNotCancelGenuineRequest(t *testing.T) {
	// Serial: Run installs signal handlers and fixture sets process-wide HOME/PATH/launch identity.
	e := newRequestEnv(t, "off", "synthetic-session-1")
	// Reuse the verified pane setup for Run's fresh registration, without a second harness.
	if err := e.process.terminate(); err != nil {
		t.Fatal(err)
	}
	e.exec(t, `DELETE FROM cursor_runtimes`)
	e.exec(t, `UPDATE spawns SET state='pending',session_id=NULL`)
	permission, _ := json.Marshal(frame{JSONRPC: "2.0", ID: []byte(`7`), Method: "session/request_permission", Params: cursorfix.Permission("synthetic-session-1", "genuine", "execute")})
	question := json.RawMessage(`{"jsonrpc":"2.0","id":"question","method":"cursor/ask_question","params":{"toolCallId":"AskQuestion","questions":[{"id":"q1","prompt":"Continue?","options":[{"id":"yes","label":"Yes"}],"allowMultiple":false}]}}`)
	e.runtime.Configure(t, cursorfix.Scenario{SessionID: "synthetic-session-1", Requests: []json.RawMessage{question, permission}})
	t.Setenv("AD_TEST_CURSOR_REPORT_HELPER", "1")
	t.Setenv("AD_TEST_CURSOR_REPORT_DB", e.path)
	t.Setenv("AD_TEST_CURSOR_REPORT_MODE", "current")
	t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", e.binding.InstanceID)
	t.Setenv("AGENT_DIRECTOR_AGENT", "cursor")
	t.Setenv("AGENT_DIRECTOR_CURSOR_STORE_PATH", e.path)
	master, slave := terminalPair(t)
	pane := &observedWire{}
	done := make(chan error, 1)
	go func() { done <- Run(Options{Runtime: e.runtime.Program, InitialPrompt: "scripted"}, slave, pane) }()
	ended := false
	t.Cleanup(func() {
		if !ended {
			slave.Close()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("session cleanup did not finish")
			}
		}
	})
	until := time.Now().Add(4 * time.Second)
	var key string
	keyPattern := regexp.MustCompile(`\[([^]]+)\] Allow once`)
	for time.Now().Before(until) {
		match := keyPattern.FindSubmatch(pane.bytes())
		if len(match) == 2 {
			key = string(match[1])
			break
		}
		select {
		case err := <-done:
			ended = true
			t.Fatalf("session ended before admission: %v; pane %s", err, pane.bytes())
		default:
		}
		time.Sleep(5 * time.Millisecond)
	}
	if key == "" {
		master.Write([]byte("/exit\r"))
		t.Fatalf("no genuine request pane: %s", pane.bytes())
	}
	rows, err := e.s.PermissionRequestsForSpawn(e.binding.InstanceID)
	if err != nil || len(rows) != 1 || rows[0].ToolUseID != "genuine" {
		t.Fatalf("question admitted or genuine lost: %+v %v", rows, err)
	}
	for _, f := range e.runtime.Frames(t) {
		if string(f["method"]) == `"session/cancel"` {
			t.Fatal("direct question cancelled genuine request")
		}
		if string(f["method"]) == `"initialize"` && bytes.Contains(bytes.ToLower(f["params"]), []byte("askquestion")) {
			t.Fatal("gateway advertised question capability")
		}
	}
	if _, err = master.Write([]byte(key)); err != nil {
		t.Fatal(err)
	}
	until = time.Now().Add(3 * time.Second)
	for time.Now().Before(until) {
		if bytes.Contains(pane.bytes(), []byte("Synthetic reply: scripted")) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !bytes.Contains(pane.bytes(), []byte("Synthetic reply: scripted")) {
		t.Fatalf("genuine request did not complete: %s", pane.bytes())
	}
	if _, err = master.Write([]byte("/exit\r")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		ended = true
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("session did not exit")
	}
}
