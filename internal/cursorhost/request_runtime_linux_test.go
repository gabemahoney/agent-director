//go:build linux

package cursorhost

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"

	"regexp"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
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
	permission, _ := json.Marshal(frame{JSONRPC: "2.0", ID: []byte(`7`), Method: "session/request_permission", Params: cursorfix.Permission("synthetic-session-1", "genuine", "execute")})
	question := json.RawMessage(`{"jsonrpc":"2.0","id":"question","method":"cursor/ask_question","params":{"toolCallId":"AskQuestion","questions":[{"id":"q1","prompt":"Continue?","options":[{"id":"yes","label":"Yes"}],"allowMultiple":false}]}}`)
	e.runtime.Configure(t, cursorfix.Scenario{SessionID: "synthetic-session-1", Requests: []json.RawMessage{question, permission}})
	master, slave := e.prepareRunPTY(t)
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

// prepareRunPTY reuses the verified pane for Run's fresh runtime registration.
func (e *requestEnv) prepareRunPTY(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	if err := e.process.terminate(); err != nil {
		t.Fatal(err)
	}
	e.exec(t, `DELETE FROM cursor_runtimes`)
	e.exec(t, `UPDATE spawns SET state='pending',session_id=NULL`)
	t.Setenv("AD_TEST_CURSOR_REPORT_HELPER", "1")
	t.Setenv("AD_TEST_CURSOR_REPORT_DB", e.path)
	t.Setenv("AD_TEST_CURSOR_REPORT_MODE", "current")
	t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", e.binding.InstanceID)
	t.Setenv("AGENT_DIRECTOR_AGENT", "cursor")
	t.Setenv("AGENT_DIRECTOR_CURSOR_STORE_PATH", e.path)
	return terminalPair(t)
}

func TestCursorPublicWaitingPromptAdmission(t *testing.T) {
	// Serial: b.jf1 Task3 .08 regression uses Run's actual report subprocess and PTY.
	if testing.Short() {
		t.Skip("protected runtime and report subprocess")
	}
	for _, tc := range []struct {
		name, event, phase, initial, keys string
		state                             string
		wantPrompts                       int
		second                            bool
		loss                              string
	}{
		{"SessionStart waiting", "SessionStart", "after", "", "typed\r", store.StateWaiting, 1, false, ""},
		{"Stop waiting", "Stop", "after", "initial", "typed\r", store.StateWaiting, 2, false, ""},
		{"pending refuses", "SessionStart", "before", "", "typed\r", store.StatePending, 0, false, ""},
		{"busy refuses after later completion", "UserPromptSubmit", "after", "initial", "typed\r", store.StateWorking, 1, false, ""},
		{"busy new clear refuse", "UserPromptSubmit", "after", "initial", "/new\r/clear\rtyped\r", store.StateWorking, 1, false, ""},
		{"initial prompt does not double admit", "SessionStart", "after", "initial", "typed\r", store.StateWaiting, 1, false, ""},
		{"SessionStart two Enters across confirm", "SessionStart", "after", "", "typed\r", store.StateWaiting, 1, true, ""},
		{"SessionStart launch replacement refuses", "SessionStart", "after", "", "typed\r", store.StateWaiting, 0, false, "launch"},
		{"SessionStart session replacement refuses", "SessionStart", "after", "", "typed\r", store.StateWaiting, 0, false, "session"},
		{"SessionStart runtime replacement refuses", "SessionStart", "after", "", "typed\r", store.StateWaiting, 0, false, "runtime"},
		{"SessionStart held replacement refuses", "SessionStart", "after", "", "typed\r", store.StateWaiting, 0, false, "held"},
		{"Stop two Enters across confirm", "Stop", "after", "initial", "typed\r", store.StateWaiting, 2, true, ""},
		{"Stop launch replacement refuses", "Stop", "after", "initial", "typed\r", store.StateWaiting, 1, false, "launch"},
		{"Stop session replacement refuses", "Stop", "after", "initial", "typed\r", store.StateWaiting, 1, false, "session"},
		{"Stop runtime replacement refuses", "Stop", "after", "initial", "typed\r", store.StateWaiting, 1, false, "runtime"},
		{"Stop held replacement refuses", "Stop", "after", "initial", "typed\r", store.StateWaiting, 1, false, "held"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newRequestEnv(t, "off", "synthetic-session-1")
			e.runtime.Configure(t, cursorfix.Scenario{SessionID: "synthetic-session-1", Requests: []json.RawMessage{}})
			master, slave := e.prepareRunPTY(t)
			dir := t.TempDir()
			t.Setenv("AD_TEST_CURSOR_BARRIER_EVENT", tc.event)
			t.Setenv("AD_TEST_CURSOR_BARRIER_PHASE", tc.phase)
			t.Setenv("AD_TEST_CURSOR_BARRIER_DIR", dir)
			secondDir := t.TempDir()
			if tc.second {
				t.Setenv("AD_TEST_CURSOR_SECOND_BARRIER_DIR", secondDir)
			}
			consumed := make(chan struct{})
			secondConsumed := make(chan struct{})
			var once sync.Once
			// The redraw of an unsubmitted sentinel proves Enter's admission callback
			// has run. It conveys input consumption only, never inferred public state.
			pane := &observedWire{observe: func(p []byte) {
				if bytes.Equal(p, []byte("\r\x1b[2K> ~")) {
					once.Do(func() { close(consumed) })
				}
				if bytes.Equal(p, []byte("\r\x1b[2K> ^")) {
					close(secondConsumed)
				}
			}}
			done := make(chan error, 1)
			ended := false
			go func() { done <- Run(Options{Runtime: e.runtime.Program, InitialPrompt: tc.initial}, slave, pane) }()
			t.Cleanup(func() {
				if ended {
					return
				}
				os.WriteFile(filepath.Join(dir, "release"), nil, 0600)
				os.WriteFile(filepath.Join(secondDir, "release"), nil, 0600)
				master.Write([]byte("\x15/exit\r"))
				select {
				case err := <-done:
					if err != nil {
						t.Errorf("Run exit: %v; pane %s", err, pane.bytes())
					}
				case <-time.After(5 * time.Second):
					slave.Close()
					t.Error("Run cleanup timed out")
				}
			})
			until := time.Now().Add(3 * time.Second)
			for {
				if _, err := os.Stat(filepath.Join(dir, "entered")); err == nil {
					break
				}
				if time.Now().After(until) {
					t.Fatalf("actual %s/%s report barrier not reached; pane %s", tc.event, tc.phase, pane.bytes())
				}
				time.Sleep(time.Millisecond)
			}
			row, err := e.s.GetSpawn(e.binding.InstanceID)
			if err != nil || row.State != tc.state {
				t.Fatalf("actual committed row=%+v err=%v want=%s", row, err, tc.state)
			}
			rows, err := e.s.PermissionRequestsForSpawn(e.binding.InstanceID)
			if err != nil || len(rows) != 0 {
				t.Fatalf("unexpected permissions before prompt: %v %v", rows, err)
			}
			bound, err := e.s.ReadCursorRuntime(e.binding.InstanceID)
			if err != nil {
				t.Fatal(err)
			}
			switch tc.loss {
			case "launch":
				e.exec(t, `UPDATE spawns SET launch_token='2222222222222222'`)
			case "session":
				e.exec(t, `UPDATE spawns SET session_id='replacement'`)
			case "runtime":
				e.exec(t, `UPDATE cursor_runtimes SET bound_connection_id='replacement'`)
			case "held":
				g := store.HookGate{Event: "PermissionRequest", ParentPID: bound.GatewayPID, ParentStart: bound.GatewayStart, SessionID: bound.SessionID, Cursor: &bound}
				if _, a, err := e.s.InsertRelayRequest(bound.InstanceID, g, store.RelayRequest{RequestToken: "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa", ToolName: "Bash", ToolInput: "{}", SettledAt: time.Now().Add(time.Minute)}, 0, store.DefaultLockWait); err != nil || !a.Applied {
					t.Fatalf("held request: %+v %v", a, err)
				}
			}
			if _, err = master.Write([]byte(tc.keys + "~")); err != nil {
				t.Fatal(err)
			}
			select {
			case <-consumed:
			case <-time.After(time.Second):
				t.Fatal("terminal reader did not consume submitted Enter before hook release")
			}
			// Restore authority only after consumption: a refused Enter must never
			// become a queued turn when readiness later returns.
			switch tc.loss {
			case "launch":
				e.exec(t, `UPDATE spawns SET launch_token=?`, bound.Token)
			case "session":
				e.exec(t, `UPDATE spawns SET session_id=?`, bound.SessionID)
			case "runtime":
				e.exec(t, `UPDATE cursor_runtimes SET bound_connection_id=?`, bound.Connection)
			case "held":
				e.exec(t, `UPDATE permission_requests SET closed_at=CURRENT_TIMESTAMP`)
				e.exec(t, `UPDATE spawns SET state='waiting'`)
			}
			if err = os.WriteFile(filepath.Join(dir, "release"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			if tc.second {
				until = time.Now().Add(3 * time.Second)
				for {
					if _, err := os.Stat(filepath.Join(secondDir, "entered")); err == nil {
						break
					}
					if time.Now().After(until) {
						t.Fatalf("first turn did not reach post-confirm submission barrier: %s", pane.bytes())
					}
					time.Sleep(time.Millisecond)
				}
				if _, err := master.Write([]byte("\x15second\r^")); err != nil {
					t.Fatal(err)
				}
				select {
				case <-secondConsumed:
				case <-time.After(time.Second):
					t.Fatal("second Enter not consumed before first turn completes")
				}
				if err := os.WriteFile(filepath.Join(secondDir, "release"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			until = time.Now().Add(2 * time.Second)
			for {
				frames := e.runtime.Frames(t)
				prompts, typed := 0, false
				for _, f := range frames {
					if string(f["method"]) == `"session/prompt"` {
						prompts++
						typed = typed || bytes.Contains(f["params"], []byte(`"typed"`))
					}
				}
				refused := bytes.Contains(pane.bytes(), []byte("Cursor input refused while starting or busy"))
				completed, getErr := e.s.GetSpawn(e.binding.InstanceID)
				initialSeen := tc.initial == "" || prompts >= 1
				if getErr == nil && completed.State == store.StateWaiting && initialSeen && (refused || typed) {
					break
				}
				if time.Now().After(until) {
					t.Fatalf("no actual admission/refusal outcome after hook release: frames=%v pane=%s", frames, pane.bytes())
				}
				time.Sleep(time.Millisecond)
			}
			if _, err = master.Write([]byte("\x15/exit\r")); err != nil {
				t.Fatal(err)
			}
			select {
			case err = <-done:
				if err != nil {
					t.Fatalf("Run ended: %v; pane %s", err, pane.bytes())
				}
				ended = true
			case <-time.After(3 * time.Second):
				t.Fatal("Run did not process queued submission and exit")
			}
			frames := e.runtime.Frames(t)
			prompts := 0
			for _, f := range frames {
				if string(f["method"]) == `"session/prompt"` {
					prompts++
					t.Logf("peer prompt: %s", f["params"])
					if bytes.Contains(f["params"], []byte(`"second"`)) {
						t.Error("second Enter became a queued prompt")
					}
				}
			}
			t.Logf("barrier=%s/%s publicState=%s EnterConsumedBeforeRelease=true prompts=%d want=%d pane=%q", tc.event, tc.phase, row.State, prompts, tc.wantPrompts, pane.bytes())
			if prompts != tc.wantPrompts {
				t.Errorf("public waiting/admission mismatch: peer received %d prompts; want %d", prompts, tc.wantPrompts)
			}
		})
	}
}
